// Package mysql is the adapter for MySQL, MariaDB and TiDB
// (go-sql-driver/mysql).
package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/db/sqlbase"
	"github.com/giovanni/lumino/internal/db/sqlx"
)

// Flavor distinguishes the MySQL-compatible servers.
type Flavor int

const (
	MySQL Flavor = iota
	MariaDB
	TiDB
)

func init() {
	db.Register(Driver{Flavor: MySQL})
	db.Register(Driver{Flavor: MariaDB})
	db.Register(Driver{Flavor: TiDB})
}

// Driver opens MySQL-compatible connections.
type Driver struct{ Flavor Flavor }

var fields = []db.Field{db.FieldHost, db.FieldPort, db.FieldDatabase, db.FieldUser, db.FieldPassword}

func (d Driver) caps() db.Capabilities {
	c := db.Capabilities{
		Schemas: true, Transactions: true, ReadOnlyTx: true, Cancel: true, ForeignKeys: true,
		Functions: true, Procedures: true, Triggers: true, Views: true,
		Insert: true, Update: true, Delete: true, TLS: true, MultiResultSets: true,
	}
	switch d.Flavor {
	case MariaDB:
		c.Sequences = true
	case TiDB:
		c.Functions, c.Procedures, c.Triggers, c.Sequences = false, false, false, true
	}
	return c
}

func (d Driver) Info() db.Info {
	switch d.Flavor {
	case MariaDB:
		return db.Info{ID: "mariadb", Name: "MariaDB", DefaultPort: 3306, DefaultUser: "root", DatabaseLabel: "Database",
			Fields: fields, Caps: d.caps(), Schemes: []string{"mariadb", "maria"}, DialectName: "mysql"}
	case TiDB:
		return db.Info{ID: "tidb", Name: "TiDB", DefaultPort: 4000, DefaultUser: "root", DatabaseLabel: "Database",
			Fields: fields, Caps: d.caps(), Schemes: []string{"tidb", "ti"}, DialectName: "mysql"}
	}
	return db.Info{ID: "mysql", Name: "MySQL", DefaultPort: 3306, DefaultUser: "root", DatabaseLabel: "Database",
		Fields: fields, Caps: d.caps(), Schemes: []string{"mysql", "my"}, DialectName: "mysql"}
}

var tlsSeq atomic.Int64

func (d Driver) Open(ctx context.Context, cfg *db.Config) (db.Conn, error) {
	info := d.Info()
	mc, err := mysql.ParseDSN("/?charset=utf8mb4")
	if err != nil {
		return nil, err
	}
	mc.User, mc.Passwd = cfg.User, cfg.Password
	mc.Net = "tcp"
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port == 0 {
		port = info.DefaultPort
	}
	mc.Addr = fmt.Sprintf("%s:%d", host, port)
	mc.DBName = cfg.Database
	mc.ParseTime = true
	mc.Loc = time.UTC
	mc.Timeout = 15 * time.Second
	mc.AllowNativePasswords = true
	mc.AllowCleartextPasswords = cfg.TLS.Enabled()
	mc.MultiStatements = false
	if mc.Params == nil {
		mc.Params = map[string]string{}
	}
	for k, v := range cfg.Params {
		mc.Params[k] = v
	}
	mc.ConnectionAttributes = "program_name:lumino"
	tc, err := cfg.TLS.TLS(host)
	if err != nil {
		return nil, err
	}
	switch {
	case tc != nil:
		name := "lumino" + strconv.FormatInt(tlsSeq.Add(1), 10)
		if err := mysql.RegisterTLSConfig(name, tc); err != nil {
			return nil, err
		}
		mc.TLSConfig = name
	case cfg.TLS.Mode == "":
		mc.TLSConfig = "preferred"
	}
	connector, err := mysql.NewConnector(mc)
	if err != nil {
		return nil, err
	}
	sdb := sql.OpenDB(connector)
	sdb.SetMaxIdleConns(4)
	sdb.SetConnMaxIdleTime(5 * time.Minute)
	if err := sdb.PingContext(ctx); err != nil {
		sdb.Close()
		return nil, mapError(err)
	}
	ver, _ := sqlbase.One(ctx, sdb, "SELECT VERSION()")
	flavor := d.Flavor
	// a "mysql" connection to a MariaDB/TiDB server behaves like them
	lv := strings.ToLower(ver)
	if flavor == MySQL && strings.Contains(lv, "mariadb") {
		flavor = MariaDB
	} else if flavor == MySQL && strings.Contains(lv, "tidb") {
		flavor = TiDB
	}
	name := map[Flavor]string{MySQL: "MySQL ", MariaDB: "MariaDB ", TiDB: "TiDB "}[flavor]
	caps := Driver{Flavor: flavor}.caps()
	if flavor == TiDB {
		caps.ReadOnlyTx = false // accepted but not enforced
	}
	dia := NewDialect(info.ID)
	c := &sqlbase.Conn{DB: sdb, Inf: info, Cap: caps, D: dia, Version: name + ver, H: hooks(flavor), InitSQL: cfg.InitSQL}
	c.Cat = &Catalog{DB: sdb, D: dia, Flavor: flavor}
	return c, nil
}

func hooks(f Flavor) sqlbase.Hooks {
	return sqlbase.Hooks{
		Kind:    KindOf,
		Convert: convert,
		ReturnsRows: func(stmt string) bool {
			if sqlx.ReturnsRows(stmt, &lexOptions) {
				return true
			}
			w := sqlx.Words(stmt, &lexOptions, 1)
			if len(w) == 0 {
				return false
			}
			switch w[0] {
			case "CHECK", "ANALYZE", "OPTIMIZE", "REPAIR", "CHECKSUM", "HELP", "TRACE", "ADMIN":
				return true
			}
			return false
		},
		SessionID: func(ctx context.Context, c *sql.Conn) (string, error) {
			return sqlbase.One(ctx, c, "SELECT CONNECTION_ID()")
		},
		// cancelling the context would close the connection (and lose the
		// session); KILL QUERY stops only the statement
		Cancel: func(ctx context.Context, sdb *sql.DB, id string) bool {
			q := "KILL QUERY " + id
			if f == TiDB {
				q = "KILL TIDB QUERY " + id
			}
			_, err := sdb.ExecContext(ctx, q)
			return err == nil
		},
		CurrentSchema: func(ctx context.Context, q sqlbase.Querier) (string, error) {
			return sqlbase.One(ctx, q, "SELECT DATABASE()")
		},
		SetCurrentSchema: func(ctx context.Context, q sqlbase.Querier, schema string) error {
			_, err := q.ExecContext(ctx, "USE "+NewDialect("").QuoteAlways(schema))
			return err
		},
		SetReadOnly: func(ctx context.Context, q sqlbase.Querier, on bool) error {
			m := "READ WRITE"
			if on {
				m = "READ ONLY"
			}
			_, err := q.ExecContext(ctx, "SET SESSION TRANSACTION "+m)
			return err
		},
		MapError: mapError,
	}
}

// convert decodes text-protocol values by column kind.
func convert(col *db.Column, v any) any {
	b, ok := v.([]byte)
	if !ok {
		return v
	}
	s := string(b)
	switch col.Kind {
	case db.KindInt:
		if col.DBType == "BIT" {
			return b
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n
		}
		if n, err := strconv.ParseUint(s, 10, 64); err == nil {
			return n
		}
		return s
	case db.KindFloat:
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
		return s
	case db.KindBytes:
		return b
	case db.KindText, db.KindDecimal, db.KindJSON, db.KindOther:
		if utf8.Valid(b) {
			return s
		}
		return b
	case db.KindTime:
		return s // TIME can exceed 24h: keep the server text
	}
	return v
}

var lineRx = regexp.MustCompile(`at line (\d+)$`)

func mapError(err error) error {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		e := &db.Error{Msg: me.Message, Code: strconv.Itoa(int(me.Number)), Err: err}
		if m := lineRx.FindStringSubmatch(me.Message); m != nil {
			e.Line, _ = strconv.Atoi(m[1])
		}
		return e
	}
	return err
}
