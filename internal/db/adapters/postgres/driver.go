// Package postgres is the PostgreSQL adapter (pgx). CockroachDB reuses it.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/db/sqlbase"
)

func init() { db.Register(Driver{}) }

// Driver opens PostgreSQL connections.
type Driver struct{}

var fields = []db.Field{db.FieldHost, db.FieldPort, db.FieldDatabase, db.FieldUser, db.FieldPassword}

// Caps are PostgreSQL's capabilities.
var Caps = db.Capabilities{
	Schemas: true, Transactions: true, ReadOnlyTx: true, Cancel: true, ForeignKeys: true, Sequences: true,
	Functions: true, Procedures: true, Triggers: true, Views: true, MatViews: true,
	Insert: true, Update: true, Delete: true, TLS: true,
}

func (Driver) Info() db.Info {
	return db.Info{
		ID: "postgres", Name: "PostgreSQL", DefaultPort: 5432, DefaultUser: "postgres", DefaultDB: "postgres",
		DatabaseLabel: "Database", Fields: fields, Caps: Caps,
		Schemes: []string{"postgres", "postgresql", "pg", "pgsql"}, DialectName: "postgres",
	}
}

// ConnConfig builds the pgx configuration of cfg.
func ConnConfig(cfg *db.Config, defPort int, defDB string) (*pgx.ConnConfig, error) {
	cc, err := pgx.ParseConfig("")
	if err != nil {
		return nil, err
	}
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		host = "localhost"
	}
	port := cfg.Port
	if port == 0 {
		port = defPort
	}
	cc.Host, cc.Port = host, uint16(port)
	cc.Database = cfg.Database
	if cc.Database == "" {
		cc.Database = defDB
	}
	cc.User, cc.Password = cfg.User, cfg.Password
	cc.ConnectTimeout = 15 * time.Second
	cc.Fallbacks = nil
	cc.RuntimeParams = map[string]string{"application_name": "lumino"}
	cc.OnNotice = collectNotice
	tc, err := cfg.TLS.TLS(host)
	if err != nil {
		return nil, err
	}
	cc.TLSConfig = tc
	// "" (driver default) behaves like sslmode=prefer
	if cfg.TLS.Mode == "" {
		pc, err := pgconn.ParseConfig(fmt.Sprintf("host=%s sslmode=prefer", host))
		if err == nil {
			cc.TLSConfig = pc.TLSConfig
			for _, fb := range pc.Fallbacks {
				f := *fb
				f.Host, f.Port = host, uint16(port)
				cc.Fallbacks = append(cc.Fallbacks, &f)
			}
		}
	}
	if err := applyParams(cc, cfg, host, uint16(port)); err != nil {
		return nil, err
	}
	return cc, nil
}

// libpqOnly are connection-string keywords that aren't server settings:
// sending them as startup parameters fails with "unrecognized
// configuration parameter".
var libpqOnly = map[string]bool{
	"host": true, "hostaddr": true, "port": true, "dbname": true, "database": true, "user": true, "password": true,
	"passfile": true, "connect_timeout": true,
	"fallback_application_name": true, "keepalives": true, "keepalives_idle": true, "keepalives_interval": true,
	"keepalives_count": true, "tcp_user_timeout": true, "gssencmode": true, "gsslib": true,
	"gssdelegation": true, "sslmode": true, "sslnegotiation": true, "sslcompression": true, "sslcert": true,
	"sslkey": true, "sslpassword": true, "sslcertmode": true, "sslrootcert": true, "sslcrl": true, "sslcrldir": true,
	"sslsni": true, "requirepeer": true, "requiressl": true, "ssl_min_protocol_version": true,
	"ssl_max_protocol_version": true, "min_protocol_version": true, "max_protocol_version": true,
	"krbsrvname": true, "krbspn": true, "service": true, "servicefile": true, "target_session_attrs": true,
	"load_balance_hosts": true, "channel_binding": true, "require_auth": true, "statement_cache_capacity": true,
	"description_cache_capacity": true, "default_query_exec_mode": true,
}

// applyParams routes the connection parameters: libpq keywords configure
// the connection, everything else is sent as a server setting.
func applyParams(cc *pgx.ConnConfig, cfg *db.Config, host string, port uint16) error {
	ssl := map[string]string{}
	for k, v := range cfg.Params {
		key := strings.ToLower(strings.TrimSpace(k))
		if !libpqOnly[key] {
			cc.RuntimeParams[k] = v
			continue
		}
		v = strings.TrimSpace(v)
		switch key {
		case "connect_timeout":
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return fmt.Errorf("invalid connect_timeout %q", v)
			}
			// libpq: 0 means wait forever, values below 2 count as 2
			if n == 0 {
				cc.ConnectTimeout = 0
			} else {
				cc.ConnectTimeout = time.Duration(max(n, 2)) * time.Second
			}
		case "fallback_application_name":
			if _, ok := cfg.Params["application_name"]; !ok {
				cc.RuntimeParams["application_name"] = v
			}
		case "target_session_attrs":
			switch v {
			case "", "any", "prefer-standby":
				cc.ValidateConnect = nil
			case "read-write":
				cc.ValidateConnect = pgconn.ValidateConnectTargetSessionAttrsReadWrite
			case "read-only":
				cc.ValidateConnect = pgconn.ValidateConnectTargetSessionAttrsReadOnly
			case "primary":
				cc.ValidateConnect = pgconn.ValidateConnectTargetSessionAttrsPrimary
			case "standby":
				cc.ValidateConnect = pgconn.ValidateConnectTargetSessionAttrsStandby
			default:
				return fmt.Errorf("invalid target_session_attrs %q", v)
			}
		case "sslmode", "sslrootcert", "sslcert", "sslkey", "sslpassword", "sslsni", "sslnegotiation":
			ssl[key] = v
		}
		// other keywords (keepalives, gssencmode…) have no pgx equivalent
		// here: ignored rather than sent to the server
	}
	// ssl* keywords apply only when the TLS settings are left to the default
	if len(ssl) > 0 && cfg.TLS.Mode == "" {
		if ssl["sslmode"] == "" {
			ssl["sslmode"] = "prefer"
		}
		conn := "host=" + quoteConnValue(host)
		for k, v := range ssl {
			conn += " " + k + "=" + quoteConnValue(v)
		}
		pc, err := pgconn.ParseConfig(conn)
		if err != nil {
			return fmt.Errorf("TLS parameters: %w", err)
		}
		cc.TLSConfig = pc.TLSConfig
		cc.Fallbacks = nil
		for _, fb := range pc.Fallbacks {
			f := *fb
			f.Host, f.Port = host, port
			cc.Fallbacks = append(cc.Fallbacks, &f)
		}
	}
	return nil
}

// quoteConnValue quotes a key=value connection string value.
func quoteConnValue(v string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v) + "'"
}

// OpenDB opens the pool.
func OpenDB(ctx context.Context, cc *pgx.ConnConfig) (*sql.DB, error) {
	sdb := stdlib.OpenDB(*cc)
	sdb.SetMaxIdleConns(4)
	sdb.SetConnMaxIdleTime(5 * time.Minute)
	if err := sdb.PingContext(ctx); err != nil {
		sdb.Close()
		return nil, MapError(err)
	}
	return sdb, nil
}

func (d Driver) Open(ctx context.Context, cfg *db.Config) (db.Conn, error) {
	cc, err := ConnConfig(cfg, 5432, "postgres")
	if err != nil {
		return nil, err
	}
	sdb, err := OpenDB(ctx, cc)
	if err != nil {
		return nil, err
	}
	ver, _ := sqlbase.One(ctx, sdb, "SHOW server_version")
	dia := NewDialect()
	c := &sqlbase.Conn{DB: sdb, Inf: d.Info(), Cap: Caps, D: dia, Version: "PostgreSQL " + ver, H: Hooks(), InitSQL: cfg.InitSQL}
	c.Cat = &Catalog{DB: sdb, D: dia}
	return c, nil
}

// Hooks are the PostgreSQL session hooks.
func Hooks() sqlbase.Hooks {
	return sqlbase.Hooks{
		Kind: KindOf,
		// cancel through the server: cancelling the context would make pgx
		// drop the connection (and the session's transaction with it)
		SessionID: func(ctx context.Context, c *sql.Conn) (string, error) {
			return sqlbase.One(ctx, c, "SELECT pg_backend_pid()")
		},
		Cancel: func(ctx context.Context, sdb *sql.DB, id string) bool {
			_, err := sdb.ExecContext(ctx, "SELECT pg_cancel_backend($1)", id)
			return err == nil
		},
		CurrentSchema: func(ctx context.Context, q sqlbase.Querier) (string, error) {
			return sqlbase.One(ctx, q, "SELECT current_schema()")
		},
		SetCurrentSchema: func(ctx context.Context, q sqlbase.Querier, schema string) error {
			// prepend to the session's default path (not the current one:
			// a previously chosen schema must not stay behind the new one);
			// keeps "$user", extension schemas…
			cur, err := sqlbase.One(ctx, q, "SELECT reset_val FROM pg_settings WHERE name = 'search_path'")
			if err != nil || cur == "" {
				if cur, err = sqlbase.One(ctx, q, "SELECT current_setting('search_path')"); err != nil {
					return err
				}
			}
			_, err = q.ExecContext(ctx, "SET search_path TO "+PrependSearchPath(cur, schema))
			return err
		},
		SetReadOnly: func(ctx context.Context, q sqlbase.Querier, on bool) error {
			mode := "READ WRITE"
			if on {
				mode = "READ ONLY"
			}
			_, err := q.ExecContext(ctx, "SET SESSION CHARACTERISTICS AS TRANSACTION "+mode)
			return err
		},
		Notices:  sessionNotices,
		MapError: MapError,
	}
}

// notices buffers the server notices of each session connection.
var notices sync.Map // *pgconn.PgConn → *noticeBuf

type noticeBuf struct {
	mu   sync.Mutex
	msgs []string
}

func collectNotice(pc *pgconn.PgConn, n *pgconn.Notice) {
	v, ok := notices.Load(pc)
	if !ok {
		return // pool connections: nobody reads them
	}
	b := v.(*noticeBuf)
	b.mu.Lock()
	if len(b.msgs) < 1000 {
		b.msgs = append(b.msgs, n.Severity+": "+n.Message)
	}
	b.mu.Unlock()
}

// sessionNotices registers a session connection and returns its drain.
func sessionNotices(c *sql.Conn) func() []string {
	var pc *pgconn.PgConn
	_ = c.Raw(func(dc any) error {
		if sc, ok := dc.(*stdlib.Conn); ok {
			pc = sc.Conn().PgConn()
		}
		return nil
	})
	if pc == nil {
		return nil
	}
	b := &noticeBuf{}
	notices.Store(pc, b)
	return func() []string {
		b.mu.Lock()
		defer b.mu.Unlock()
		out := b.msgs
		b.msgs = nil
		return out
	}
}

// MapError converts pgx errors.
func MapError(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		e := &db.Error{Msg: pe.Message, Code: pe.Code, Position: int(pe.Position), Detail: pe.Detail, Hint: pe.Hint, Err: err}
		if pe.Severity != "" && pe.Severity != "ERROR" {
			e.Msg = pe.Severity + ": " + pe.Message
		}
		return e
	}
	return err
}

// PrependSearchPath puts schema (quoted) first in a search_path value,
// removing other occurrences of it; the remaining elements are kept as they
// are (already valid SQL identifiers).
func PrependSearchPath(path, schema string) string {
	out := []string{NewDialect().QuoteAlways(schema)}
	for _, el := range splitSearchPath(path) {
		if name := searchPathName(el); name != "" && name != schema {
			out = append(out, el)
		}
	}
	return strings.Join(out, ", ")
}

// splitSearchPath splits a search_path value at the commas outside double
// quotes.
func splitSearchPath(path string) []string {
	var out []string
	var cur strings.Builder
	inq := false
	for _, r := range path {
		switch {
		case r == '"':
			inq = !inq
			cur.WriteRune(r)
		case r == ',' && !inq:
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" || len(out) > 0 {
		out = append(out, s)
	}
	return out
}

// searchPathName is the schema name an element refers to ("" if invalid).
func searchPathName(el string) string {
	if len(el) >= 2 && el[0] == '"' && el[len(el)-1] == '"' {
		return strings.ReplaceAll(el[1:len(el)-1], `""`, `"`)
	}
	if strings.ContainsAny(el, "\" \t;") {
		return ""
	}
	return strings.ToLower(el)
}
