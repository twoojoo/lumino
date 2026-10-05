// Package cockroach is the CockroachDB adapter: the PostgreSQL adapter with
// CockroachDB's catalog, DDL and cancellation differences.
package cockroach

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/db/adapters/postgres"
	"github.com/giovanni/lumino/internal/db/sqlbase"
)

func init() { db.Register(Driver{}) }

// Driver opens CockroachDB connections.
type Driver struct{}

var caps = db.Capabilities{
	Schemas: true, Transactions: true, ReadOnlyTx: true, Cancel: true, ForeignKeys: true, Sequences: true,
	Views: true, MatViews: true, Insert: true, Update: true, Delete: true, TLS: true,
}

func (Driver) Info() db.Info {
	return db.Info{ID: "cockroachdb", Name: "CockroachDB", DefaultPort: 26257, DefaultUser: "root", DefaultDB: "defaultdb",
		DatabaseLabel: "Database", Fields: []db.Field{db.FieldHost, db.FieldPort, db.FieldDatabase, db.FieldUser, db.FieldPassword},
		Caps: caps, Schemes: []string{"cockroachdb", "cockroach", "crdb", "cr"}, DialectName: "postgres"}
}

func (d Driver) Open(ctx context.Context, cfg *db.Config) (db.Conn, error) {
	cc, err := postgres.ConnConfig(cfg, 26257, "defaultdb")
	if err != nil {
		return nil, err
	}
	sdb, err := postgres.OpenDB(ctx, cc)
	if err != nil {
		return nil, err
	}
	ver, _ := sqlbase.One(ctx, sdb, "SELECT version()")
	if i := strings.Index(ver, " ("); i > 0 {
		ver = ver[:i]
	}
	dia := postgres.NewDialect()
	h := postgres.Hooks()
	h.SessionID = func(ctx context.Context, c *sql.Conn) (string, error) {
		return sqlbase.One(ctx, c, "SHOW session_id")
	}
	h.Cancel = func(ctx context.Context, sdb *sql.DB, id string) bool {
		_, err := sdb.ExecContext(ctx, "CANCEL QUERIES (SELECT query_id FROM [SHOW CLUSTER STATEMENTS] WHERE session_id = $1)", id)
		return err == nil
	}
	h.SetReadOnly = func(ctx context.Context, q sqlbase.Querier, on bool) error {
		v := "off"
		if on {
			v = "on"
		}
		_, err := q.ExecContext(ctx, "SET default_transaction_read_only = "+v)
		return err
	}
	c := &sqlbase.Conn{DB: sdb, Inf: d.Info(), Cap: caps, D: dia, Version: ver, H: h, InitSQL: cfg.InitSQL}
	c.Cat = &catalog{Catalog: postgres.Catalog{DB: sdb, D: dia, Cockroach: true}}
	return c, nil
}

// catalog uses SHOW CREATE for DDL (CockroachDB's pg_catalog functions
// don't reconstruct every clause).
type catalog struct{ postgres.Catalog }

func (c *catalog) Schemas(ctx context.Context, cat string) ([]db.Schema, error) {
	ss, err := c.Catalog.Schemas(ctx, cat)
	for i := range ss {
		if ss[i].Name == "crdb_internal" || ss[i].Name == "pg_extension" {
			ss[i].System = true
		}
	}
	return ss, err
}

func (c *catalog) DDL(ctx context.Context, ref db.ObjectRef) (string, error) {
	s, err := sqlbase.One(ctx, c.DB, "SELECT create_statement FROM [SHOW CREATE "+c.D.Qualified(ref)+"]")
	if err != nil {
		return "", postgres.MapError(err)
	}
	if s == "" {
		return "", fmt.Errorf("%s not found", ref)
	}
	return strings.TrimRight(s, ";\n") + ";\n", nil
}
