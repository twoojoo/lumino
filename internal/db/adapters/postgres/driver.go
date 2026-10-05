// Package postgres is the PostgreSQL adapter (pgx). CockroachDB reuses it.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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
	for k, v := range cfg.Params {
		cc.RuntimeParams[k] = v
	}
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
	return cc, nil
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
			path := NewDialect().QuoteAlways(schema)
			if schema != "public" {
				path += ", public"
			}
			_, err := q.ExecContext(ctx, "SET search_path TO "+path)
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
		MapError: MapError,
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
