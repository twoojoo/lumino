//go:build integration

package conformance

import (
	"context"
	"testing"
	"time"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/db/dbtest"
)

// bug 10: rows keyed on DATE/TIMESTAMP columns with a non-UTC client.
// Also run with TZ=Europe/Rome: the test switches time.Local itself too.
func TestOracleRegressDateKey(t *testing.T) {
	needs(t, "127.0.0.1:55521")
	rome, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		t.Skip(err)
	}
	old := time.Local
	time.Local = rome
	t.Cleanup(func() { time.Local = old })

	tg := dbtest.Targets()["oracle"]
	cfg := tg.Config
	conn, err := db.Open(context.Background(), &cfg, db.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx := context.Background()
	s, err := conn.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	drop := func() {
		_, _, _ = dbtest.Query(ctx, s, "BEGIN EXECUTE IMMEDIATE 'DROP TABLE lumino_regress_dk PURGE'; EXCEPTION WHEN OTHERS THEN NULL; END;")
	}
	d := conn.Dialect()
	ref := db.ObjectRef{Schema: tg.Schema, Name: "LUMINO_REGRESS_DK", Type: db.ObjTable}
	for _, c := range []struct{ typ, v1, v2 string }{
		{"DATE", "DATE '2024-02-29' + 13/24 + 14/1440", "DATE '2024-07-01'"},
		{"TIMESTAMP(6)", "TIMESTAMP '2024-02-29 13:14:15.123456'", "TIMESTAMP '2024-07-01 00:30:00'"},
		{"TIMESTAMP(6) WITH TIME ZONE", "TIMESTAMP '2024-02-29 13:14:15.123456 +02:00'", "TIMESTAMP '2024-07-01 00:30:00 -05:00'"},
	} {
		t.Run(c.typ, func(t *testing.T) {
			drop()
			defer drop()
			pgrExec(t, s, "CREATE TABLE lumino_regress_dk (k "+c.typ+" NOT NULL, v NUMBER(5), d DATE)")
			pgrExec(t, s, "INSERT INTO lumino_regress_dk (k, v) VALUES ("+c.v1+", 1)")
			pgrExec(t, s, "INSERT INTO lumino_regress_dk (k, v) VALUES ("+c.v2+", 2)")
			pgrExec(t, s, "COMMIT")
			cols, rows, err := dbtest.Query(ctx, s, "SELECT k, v FROM lumino_regress_dk ORDER BY v")
			if err != nil {
				t.Fatal(err)
			}
			newDate := time.Date(2025, 3, 30, 2, 30, 0, 0, time.UTC) // a DST gap in Rome
			for _, r := range rows {
				key := []db.ColValue{{Column: "K", Kind: cols[0].Kind, Value: r[0]}}
				if _, err := db.ApplyChanges(ctx, s, conn.Caps(), d, ref, []db.Change{{Op: db.OpUpdate, Key: key,
					Values: []db.ColValue{{Column: "V", Kind: db.KindInt, Value: int64(10)}, {Column: "D", Kind: db.KindTimestamp, Value: newDate}}}}); err != nil {
					t.Errorf("update %v: %v", r[0], err)
				}
			}
			if got := pgrScalar(t, s, "SELECT TO_CHAR(MIN(d), 'YYYY-MM-DD HH24:MI:SS') || '/' || COUNT(*) FROM lumino_regress_dk WHERE v = 10"); got != "2025-03-30 02:30:00/2" {
				t.Errorf("updated date %q", got)
			}
			for _, r := range rows {
				key := []db.ColValue{{Column: "K", Kind: cols[0].Kind, Value: r[0]}}
				if _, err := db.ApplyChanges(ctx, s, conn.Caps(), d, ref, []db.Change{{Op: db.OpDelete, Key: key}}); err != nil {
					t.Errorf("delete %v: %v", r[0], err)
				}
			}
			if n := pgrScalar(t, s, "SELECT COUNT(*) FROM lumino_regress_dk"); n != "0" {
				t.Errorf("%s rows left", n)
			}
		})
	}
}
