//go:build integration

package conformance

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/db/dbtest"
)

func msrOpen(t *testing.T, host string) (db.Conn, db.Session) {
	t.Helper()
	tg := dbtest.Targets()["sqlserver"]
	needs(t, "127.0.0.1:55433")
	if tg.Setup != nil {
		tg.Setup(t)
	}
	cfg := tg.Config
	if host != "" {
		cfg.Host = host
	}
	conn, err := db.Open(context.Background(), &cfg, db.OpenOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	s, err := conn.Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return conn, s
}

func msrSchema(t *testing.T, s db.Session, tables ...string) {
	t.Helper()
	drop := func() {
		for _, tb := range tables {
			_, _, _ = dbtest.Query(context.Background(), s, "IF OBJECT_ID('lumino_regress."+tb+"') IS NOT NULL DROP TABLE lumino_regress."+tb)
		}
		_, _, _ = dbtest.Query(context.Background(), s, "IF SCHEMA_ID('lumino_regress') IS NOT NULL DROP SCHEMA lumino_regress")
	}
	drop()
	pgrExec(t, s, "CREATE SCHEMA lumino_regress")
	t.Cleanup(drop)
}

// bug 6: IPv6 hosts.
func TestMSSQLRegressIPv6Host(t *testing.T) {
	for _, h := range []string{"::ffff:127.0.0.1", "[::ffff:127.0.0.1]"} {
		_, s := msrOpen(t, h)
		if got := pgrScalar(t, s, "SELECT 1"); got != "1" {
			t.Errorf("%s: %q", h, got)
		}
	}
}

// bug 7: rows keyed on datetime columns.
func TestMSSQLRegressDatetimeKey(t *testing.T) {
	conn, s := msrOpen(t, "")
	msrSchema(t, s, "dtk")
	ctx := context.Background()
	for _, c := range []struct{ typ, vals string }{
		{"datetime", "('2024-02-29T13:14:15.003', 1), ('2024-02-29T13:14:15.007', 3), ('2024-02-29T13:16:15.997', 2)"},
		{"smalldatetime", "('2024-02-29T13:14:00', 1), ('2024-02-29T13:16:00', 2)"},
		{"datetime2(3)", "('2024-02-29T13:14:15.003', 1), ('2024-02-29T13:16:15.997', 2)"},
		{"datetime2", "('2024-02-29T13:14:15.003', 1), ('2024-02-29T13:14:15.0033333', 3), ('2024-02-29T13:16:15.997', 2)"},
	} {
		typ := c.typ
		t.Run(typ, func(t *testing.T) {
			pgrExec(t, s, "IF OBJECT_ID('lumino_regress.dtk') IS NOT NULL DROP TABLE lumino_regress.dtk")
			pgrExec(t, s, "CREATE TABLE lumino_regress.dtk (k "+typ+" PRIMARY KEY, v int)")
			pgrExec(t, s, "INSERT INTO lumino_regress.dtk VALUES "+c.vals)
			cols, rows, err := dbtest.Query(ctx, s, "SELECT k, v FROM lumino_regress.dtk ORDER BY k")
			if err != nil {
				t.Fatal(err)
			}
			ref := db.ObjectRef{Schema: "lumino_regress", Name: "dtk", Type: db.ObjTable}
			d := conn.Dialect()
			for _, r := range rows {
				key := []db.ColValue{{Column: "k", Kind: cols[0].Kind, Value: r[0]}}
				// filter by this value
				q := "SELECT v FROM lumino_regress.dtk WHERE k = " + d.Literal(r[0], cols[0].Kind)
				_, got, err := dbtest.Query(ctx, s, q)
				if err != nil || len(got) != 1 {
					t.Errorf("%s: %d rows, %v", q, len(got), err)
				}
				if _, err := db.ApplyChanges(ctx, s, conn.Caps(), d, ref, []db.Change{{Op: db.OpUpdate, Key: key,
					Values: []db.ColValue{{Column: "v", Kind: db.KindInt, Value: int64(10)}}}}); err != nil {
					t.Errorf("update %v: %v", r[0], err)
				}
				if _, err := db.ApplyChanges(ctx, s, conn.Caps(), d, ref, []db.Change{{Op: db.OpDelete, Key: key}}); err != nil {
					t.Errorf("delete %v: %v", r[0], err)
				}
			}
			if n := pgrScalar(t, s, "SELECT COUNT(*) FROM lumino_regress.dtk"); n != "0" {
				t.Errorf("%s rows left", n)
			}
		})
	}
	// literal of a value with sub-millisecond precision
	if got := conn.Dialect().Literal(time.Date(2024, 2, 29, 13, 14, 15, 3333300, time.UTC), db.KindTimestamp); got != "'2024-02-29T13:14:15.0033333'" {
		t.Errorf("literal %s", got)
	}
}

// bug 8: index DESC/INCLUDE and PERSISTED computed columns in the DDL.
func TestMSSQLRegressDDL(t *testing.T) {
	conn, s := msrOpen(t, "")
	msrSchema(t, s, "ddl")
	pgrExec(t, s, `CREATE TABLE lumino_regress.ddl (
		a int NOT NULL, b int NOT NULL, c nvarchar(20) NULL, d int NULL,
		tot AS (a * 2) PERSISTED NOT NULL, virt AS (b + 1),
		CONSTRAINT pk_ddl PRIMARY KEY CLUSTERED (a DESC, b),
		CONSTRAINT uq_ddl UNIQUE NONCLUSTERED (b DESC))`)
	pgrExec(t, s, "CREATE NONCLUSTERED INDEX ix_ddl ON lumino_regress.ddl (b DESC, a) INCLUDE (c, d) WHERE d > 0")
	ref := db.ObjectRef{Schema: "lumino_regress", Name: "ddl", Type: db.ObjTable}
	ctx := context.Background()
	before, err := conn.Catalog().DDL(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"tot AS ([a]*(2)) PERSISTED NOT NULL", "virt AS ([b]+(1)),", "PRIMARY KEY CLUSTERED (a DESC, b)",
		"UNIQUE NONCLUSTERED (b DESC)", "(b DESC, a) INCLUDE (c, d) WHERE ([d]>(0))"} {
		if !strings.Contains(before, p) {
			t.Errorf("DDL lacks %q:\n%s", p, before)
		}
	}
	pgrExec(t, s, "DROP TABLE lumino_regress.ddl")
	if err := dbtest.Load(ctx, conn, before); err != nil {
		t.Fatalf("recreate: %v\n%s", err, before)
	}
	after, err := conn.Catalog().DDL(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("DDL changed after round trip:\n%s\n---\n%s", before, after)
	}
}

// bug 9: batches starting with DML/SET/PRINT followed by a SELECT.
func TestMSSQLRegressBatchResults(t *testing.T) {
	_, s := msrOpen(t, "")
	msrSchema(t, s, "bat", "bat2")
	ctx := context.Background()
	pgrExec(t, s, "CREATE TABLE lumino_regress.bat (a int PRIMARY KEY, b int)")
	pgrExec(t, s, "CREATE TABLE lumino_regress.bat2 (a int PRIMARY KEY, b int)")
	pgrExec(t, s, "INSERT INTO lumino_regress.bat VALUES (1, 1), (2, 2)")
	for _, c := range []struct{ q, want string }{
		{"UPDATE lumino_regress.bat SET b = 3 WHERE a = 1\nSELECT @@ROWCOUNT AS n", "1"},
		{"SET NOCOUNT ON\nSELECT COUNT(*) FROM lumino_regress.bat", "2"},
		{"PRINT 'x'\nSELECT b FROM lumino_regress.bat WHERE a = 1", "3"},
		{"DELETE FROM lumino_regress.bat2; SELECT 7", "7"},
		{"INSERT INTO lumino_regress.bat2 (a, b) SELECT a, b FROM lumino_regress.bat WHERE a = 1\nSELECT COUNT(*) FROM lumino_regress.bat2", "1"},
		{"UPDATE lumino_regress.bat WITH (ROWLOCK) SET b = 3 WHERE a = 1;\nWITH x AS (SELECT a FROM lumino_regress.bat) SELECT COUNT(*) FROM x", "2"},
	} {
		q, want := c.q, c.want
		_, rows, err := dbtest.Query(ctx, s, q)
		if err != nil {
			t.Errorf("%s: %v", q, err)
			continue
		}
		if len(rows) != 1 || db.FormatValue(rows[0][0], db.KindInt) != want {
			t.Errorf("%s: rows %v, want %s", q, rows, want)
		}
	}
	pgrExec(t, s, "SET NOCOUNT OFF")
	// plain DML keeps its affected count
	for _, c := range []struct {
		q    string
		want int64
	}{
		{"UPDATE lumino_regress.bat SET b = 4 WHERE a IN (SELECT a FROM lumino_regress.bat WHERE a = 2)", 1},
		{"INSERT INTO lumino_regress.bat2 (a, b) SELECT a + 10, b FROM lumino_regress.bat", 2},
		{"INSERT INTO lumino_regress.bat2 (a, b) SELECT 100, 1 UNION ALL SELECT 101, 1", 2},
		{"DELETE FROM lumino_regress.bat2 WHERE a >= 100", 2},
	} {
		q, want := c.q, c.want
		res, err := s.Execute(ctx, q)
		if err != nil {
			t.Errorf("%s: %v", q, err)
			continue
		}
		if n := res.RowsAffected(); n != want {
			t.Errorf("%s: affected %d, want %d", q, n, want)
		}
		res.Close()
	}
}
