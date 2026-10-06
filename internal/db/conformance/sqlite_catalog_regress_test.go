//go:build integration

package conformance

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/db/dbtest"
)

func scrConn(t *testing.T, stmts ...string) db.Conn {
	t.Helper()
	conn, err := sqliteOpen(t, filepath.Join(t.TempDir(), "cat.sqlite"), map[string]string{"create": "true"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := conn.Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, q := range stmts {
		sqliteExec(t, s, q)
	}
	return conn
}

func scrTable(t *testing.T, conn db.Conn, name string) *db.TableInfo {
	t.Helper()
	ti, err := conn.Catalog().Table(context.Background(), db.ObjectRef{Schema: "main", Name: name, Type: db.ObjTable})
	if err != nil {
		t.Fatalf("Table(%s): %v", name, err)
	}
	return ti
}

// FKs without a column list reference the parent's primary key.
func TestSQLiteCatalogRegressFKImplicit(t *testing.T) {
	conn := scrConn(t,
		"CREATE TABLE parent (a INTEGER, b TEXT, PRIMARY KEY (b, a))",
		"CREATE TABLE single (id INTEGER PRIMARY KEY)",
		"CREATE TABLE child (x INTEGER, y TEXT, sid INTEGER REFERENCES single, FOREIGN KEY (y, x) REFERENCES parent)",
		"CREATE TABLE nopk (pid INTEGER REFERENCES single)",
	)
	ti := scrTable(t, conn, "child")
	got := map[string][]string{}
	for _, fk := range ti.ForeignKeys {
		got[fk.RefTable.Name] = fk.RefColumns
	}
	if !reflect.DeepEqual(got["parent"], []string{"b", "a"}) || !reflect.DeepEqual(got["single"], []string{"id"}) {
		t.Fatalf("ref columns: %v", got)
	}
	ti = scrTable(t, conn, "nopk")
	if len(ti.ForeignKeys) != 1 || !reflect.DeepEqual(ti.ForeignKeys[0].RefColumns, []string{"id"}) {
		t.Fatalf("nopk fks: %+v", ti.ForeignKeys)
	}
}

// Only true rowid aliases are auto-increment.
func TestSQLiteCatalogRegressRowidAlias(t *testing.T) {
	conn := scrConn(t,
		"CREATE TABLE alias (id INTEGER PRIMARY KEY, v TEXT)",
		"CREATE TABLE aliasdesc (id INTEGER, v TEXT, PRIMARY KEY (id DESC))", // table-level DESC still aliases
		"CREATE TABLE wr (id INTEGER PRIMARY KEY, v TEXT) WITHOUT ROWID",
		"CREATE TABLE coldesc (id INTEGER PRIMARY KEY DESC, v TEXT)",
	)
	for name, want := range map[string]bool{"alias": true, "aliasdesc": true, "wr": false, "coldesc": false} {
		ti := scrTable(t, conn, name)
		if ti.Columns[0].AutoIncr != want {
			t.Errorf("%s: AutoIncr = %v, want %v", name, ti.Columns[0].AutoIncr, want)
		}
	}
}

// Lookups by a differently cased name find the same metadata.
func TestSQLiteCatalogRegressNameCase(t *testing.T) {
	conn := scrConn(t,
		"CREATE TABLE gen (a INTEGER, b INTEGER GENERATED ALWAYS AS (a * 2), CHECK (a > 0))",
		"CREATE INDEX gen_a ON gen (a) WHERE a > 1",
		"CREATE TRIGGER gen_trg AFTER INSERT ON gen BEGIN SELECT 1; END",
	)
	ti := scrTable(t, conn, "GEN")
	if ti.Columns[1].Generated != "a * 2" {
		t.Errorf("generated = %q", ti.Columns[1].Generated)
	}
	var checks int
	for _, c := range ti.Constraints {
		if c.Type == "CHECK" {
			checks++
		}
	}
	if checks != 1 || len(ti.Triggers) != 1 {
		t.Errorf("checks %d, triggers %d", checks, len(ti.Triggers))
	}
	if len(ti.Indexes) != 1 || ti.Indexes[0].Where != "a > 1" {
		t.Errorf("indexes: %+v", ti.Indexes)
	}
	ddl, err := conn.Catalog().DDL(context.Background(), db.ObjectRef{Schema: "main", Name: "GEN", Type: db.ObjTable})
	if err != nil {
		t.Fatalf("DDL: %v", err)
	}
	if !strings.Contains(ddl, "CREATE TABLE gen") || !strings.Contains(ddl, "gen_a") || !strings.Contains(ddl, "gen_trg") {
		t.Errorf("DDL: %s", ddl)
	}
}

// Partial index predicates and trigger events survive newlines.
func TestSQLiteCatalogRegressMultiline(t *testing.T) {
	conn := scrConn(t,
		"CREATE TABLE m (a INTEGER, b TEXT)",
		"CREATE INDEX m_a ON m (a)\nWHERE\n  b IS NOT NULL",
		"CREATE TRIGGER m_upd\nAFTER\nUPDATE OF b\nON m\nBEGIN\n  SELECT 1;\nEND",
		"CREATE TRIGGER \"m before\"\n\tBEFORE DELETE ON m BEGIN SELECT 1; END",
		"CREATE TRIGGER IF NOT EXISTS main.m_ins INSERT ON m BEGIN SELECT 'update'; END",
	)
	ti := scrTable(t, conn, "m")
	if len(ti.Indexes) != 1 || ti.Indexes[0].Where != "b IS NOT NULL" {
		t.Errorf("indexes: %+v", ti.Indexes)
	}
	got := map[string]string{}
	for _, tr := range ti.Triggers {
		got[tr.Name] = tr.Timing + "/" + tr.Events
	}
	want := map[string]string{"m_upd": "AFTER/UPDATE", "m before": "BEFORE/DELETE", "m_ins": "BEFORE/INSERT"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("triggers: %v, want %v", got, want)
	}
}

// FTS5 shadow tables are not listed.
func TestSQLiteCatalogRegressShadowTables(t *testing.T) {
	conn := scrConn(t, "CREATE VIRTUAL TABLE docs USING fts5(body)", "CREATE TABLE plain (x INTEGER)")
	objs, err := conn.Catalog().Objects(context.Background(), db.ObjectRef{Schema: "main"})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, o := range objs {
		names = append(names, o.Ref.Name)
	}
	if !reflect.DeepEqual(names, []string{"docs", "plain"}) {
		t.Fatalf("objects: %v", names)
	}
}

// Closing a session keeps a :memory: database alive and resets read-only mode.
func TestSQLiteCatalogRegressMemoryKeepConn(t *testing.T) {
	ctx := context.Background()
	conn, err := sqliteOpen(t, ":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	a, err := conn.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sqliteExec(t, a, "CREATE TABLE x (a)")
	a.Close()
	b, err := conn.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sqliteExec(t, b, "SELECT * FROM x")
	if err := b.SetReadOnly(ctx, true); err != nil {
		t.Fatal(err)
	}
	if res, err := b.Execute(ctx, "INSERT INTO x VALUES (1)"); err == nil {
		res.Close()
		t.Fatal("insert in read-only session succeeded")
	}
	b.Close()
	c, err := conn.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	sqliteExec(t, c, "INSERT INTO x VALUES (2)")
}

// Catalog queries and other sessions don't wait for a session holding an
// open cursor, on files and in memory.
func TestSQLiteCatalogRegressConcurrentCatalog(t *testing.T) {
	for name, file := range map[string]string{"file": filepath.Join(t.TempDir(), "conc.sqlite"), "memory": ":memory:"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			conn, err := sqliteOpen(t, file, map[string]string{"create": "true"})
			if err != nil {
				t.Fatal(err)
			}
			a, err := conn.Session(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			sqliteExec(t, a, "CREATE TABLE big (n INTEGER)")
			sqliteExec(t, a, "INSERT INTO big WITH RECURSIVE c(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM c WHERE n < 5000) SELECT n FROM c")
			res, err := a.Execute(ctx, "SELECT n FROM big")
			if err != nil {
				t.Fatal(err)
			}
			defer res.Close()
			if _, _, err := res.Fetch(ctx, 1); err != nil {
				t.Fatal(err)
			}
			qctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			objs, err := conn.Catalog().Objects(qctx, db.ObjectRef{Schema: "main"})
			if err != nil || len(objs) != 1 {
				t.Fatalf("Objects while a cursor is open: %v %v", objs, err)
			}
			if _, err := conn.Catalog().Table(qctx, db.ObjectRef{Schema: "main", Name: "big"}); err != nil {
				t.Fatalf("Table while a cursor is open: %v", err)
			}
			b, err := conn.Session(qctx)
			if err != nil {
				t.Fatalf("second session: %v", err)
			}
			defer b.Close()
			r2, err := b.Execute(qctx, "SELECT count(*) FROM big")
			if err != nil {
				t.Fatalf("second session query: %v", err)
			}
			r2.Close()
		})
	}
}

// A :memory: database is shared by the Conn's sessions and survives when
// none is open.
func TestSQLiteCatalogRegressMemorySurvives(t *testing.T) {
	ctx := context.Background()
	conn, err := sqliteOpen(t, ":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := conn.Session(ctx)
	sqliteExec(t, a, "CREATE TABLE x (a)")
	sqliteExec(t, a, "INSERT INTO x VALUES (1)")
	b, _ := conn.Session(ctx)
	sqliteExec(t, b, "INSERT INTO x VALUES (2)") // another connection, same database
	a.Close()
	b.Close()
	time.Sleep(200 * time.Millisecond)
	objs, err := conn.Catalog().Objects(ctx, db.ObjectRef{Schema: "main"})
	if err != nil || len(objs) != 1 {
		t.Fatalf("objects after closing sessions: %v %v", objs, err)
	}
	c, _ := conn.Session(ctx)
	defer c.Close()
	_, rows, err := dbtest.Query(ctx, c, "SELECT count(*) FROM x")
	if err != nil || fmt.Sprint(rows[0][0]) != "2" {
		t.Fatalf("rows: %v %v", rows, err)
	}
	// separate Conns get separate databases
	other, err := sqliteOpen(t, ":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	if objs, _ := other.Catalog().Objects(ctx, db.ObjectRef{Schema: "main"}); len(objs) != 0 {
		t.Fatalf("second :memory: Conn sees %v", objs)
	}
}
