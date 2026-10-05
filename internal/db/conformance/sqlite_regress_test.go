//go:build integration

package conformance

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/db/dbtest"
)

func sqliteOpen(t *testing.T, file string, params map[string]string) (db.Conn, error) {
	t.Helper()
	cfg := db.Config{Driver: "sqlite", File: file, Params: params}
	conn, err := db.Open(context.Background(), &cfg, db.OpenOptions{})
	if err == nil {
		t.Cleanup(func() { conn.Close() })
	}
	return conn, err
}

func sqliteExec(t *testing.T, s db.Session, q string, args ...any) {
	t.Helper()
	res, err := s.Execute(context.Background(), q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	res.Close()
}

// The exact file is opened whatever characters its path contains.
func TestSQLiteRegressPaths(t *testing.T) {
	for _, name := range []string{"my#db.sqlite", "a%41b.sqlite", "q?x=1&y.sqlite", "sp ace.sqlite", "ünï çødé ✓.sqlite",
		"a%2Fb.sqlite", "semi;colon.sqlite"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, name)
			if _, err := sqliteOpen(t, path, nil); err == nil {
				t.Fatal("missing file opened without create")
			}
			conn, err := sqliteOpen(t, path, map[string]string{"create": "true"})
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			s, err := conn.Session(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			sqliteExec(t, s, "CREATE TABLE marker (x INTEGER)")
			s.Close()
			conn.Close()
			ents, _ := os.ReadDir(dir)
			var names []string
			for _, e := range ents {
				names = append(names, e.Name())
			}
			if len(names) != 1 || names[0] != name {
				t.Fatalf("files in dir: %q, want only %q", names, name)
			}
			// reopen read-only: the table is there
			conn, err = sqliteOpen(t, path, map[string]string{"mode": "ro"})
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}
			if _, err := conn.Catalog().Table(context.Background(), db.ObjectRef{Schema: "main", Name: "marker", Type: db.ObjTable}); err != nil {
				t.Fatalf("table in reopened file: %v", err)
			}
		})
	}
}

// Editing DATE/DATETIME columns keeps SQLite's text format and finds rows
// keyed by a date.
func TestSQLiteRegressDateEdit(t *testing.T) {
	conn, err := sqliteOpen(t, filepath.Join(t.TempDir(), "dates.sqlite"), map[string]string{"create": "true"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := conn.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sqliteExec(t, s, "CREATE TABLE ev (d DATE PRIMARY KEY, ts DATETIME, tm TIMESTAMP, n TEXT)")
	sqliteExec(t, s, `INSERT INTO ev VALUES
		('2024-01-01', '2024-01-01 10:00:00', '2024-01-01 10:00:00.5', 'a'),
		('2024-01-02', '2024-01-02T08:30:00', '2024-01-02 10:00:00+02:00', 'b'),
		('2024-01-03', '2024-01-03 10:00', NULL, 'c')`)
	ref := db.ObjectRef{Schema: "main", Name: "ev", Type: db.ObjTable}
	d, caps := conn.Dialect(), conn.Caps()
	texts := func() map[string][3]any {
		_, rows, err := dbtest.Query(ctx, s, "SELECT n, d || '', ts || '', tm || '' FROM ev")
		if err != nil {
			t.Fatal(err)
		}
		out := map[string][3]any{}
		for _, r := range rows {
			out[r[0].(string)] = [3]any{r[1], r[2], r[3]}
		}
		return out
	}
	before := texts()
	cols, rows, err := dbtest.Query(ctx, s, "SELECT d, ts, tm, n FROM ev ORDER BY n")
	if err != nil {
		t.Fatal(err)
	}
	if cols[0].Kind != db.KindDate || cols[1].Kind != db.KindTimestamp {
		t.Fatalf("kinds %s %s", cols[0].Kind, cols[1].Kind)
	}
	key := func(r db.Row) []db.ColValue { return []db.ColValue{{Column: "d", Kind: cols[0].Kind, Value: r[0]}} }
	// update an untouched column of every row, sending the other values back
	// as they were read (what a grid edit of the row does)
	var chs []db.Change
	for _, r := range rows {
		chs = append(chs, db.Change{Op: db.OpUpdate, Key: key(r), Values: []db.ColValue{
			{Column: "n", Kind: db.KindText, Value: r[3].(string) + "2"},
			{Column: "ts", Kind: cols[1].Kind, Value: r[1]},
			{Column: "tm", Kind: cols[2].Kind, Value: r[2]},
		}})
	}
	if _, err := db.ApplyChanges(ctx, s, caps, d, ref, chs); err != nil {
		t.Fatalf("update by date key: %v", err)
	}
	after := texts()
	if after["a2"] != before["a"] {
		t.Errorf("row a: stored text changed from %q to %q", before["a"], after["a2"])
	}
	// 'T' separator, no seconds: rewritten in SQLite's canonical format, same instant
	if got := after["c2"]; got != [3]any{"2024-01-03", "2024-01-03 10:00:00", nil} {
		t.Errorf("row c: %q", got)
	}
	if got := after["b2"]; got[0] != "2024-01-02" || got[1] != "2024-01-02 08:30:00" || got[2] != "2024-01-02 10:00:00+02:00" {
		t.Errorf("row b: %q", got)
	}
	// typed values: date only for DATE, no offset for DATETIME
	dv, _ := db.ParseValue("2024-03-01", db.KindDate)
	tv, _ := db.ParseValue("2024-02-01 11:00:00", db.KindTimestamp)
	fv, _ := db.ParseValue("2024-02-01 11:00:00.25", db.KindTimestamp)
	if _, err := db.ApplyChanges(ctx, s, caps, d, ref, []db.Change{{Op: db.OpUpdate, Key: key(rows[0]),
		Values: []db.ColValue{{Column: "d", Kind: db.KindDate, Value: dv}, {Column: "ts", Kind: db.KindTimestamp, Value: tv},
			{Column: "tm", Kind: db.KindTimestamp, Value: fv}}}}); err != nil {
		t.Fatalf("update key: %v", err)
	}
	if got := texts()["a2"]; got != [3]any{"2024-03-01", "2024-02-01 11:00:00", "2024-02-01 11:00:00.25"} {
		t.Errorf("typed values stored as %q", got)
	}
	// insert and delete with time keys (row b was stored with 'T' before)
	nv, _ := db.ParseValue("2024-05-06", db.KindDate)
	if _, err := db.ApplyChanges(ctx, s, caps, d, ref, []db.Change{{Op: db.OpInsert, Values: []db.ColValue{
		{Column: "d", Kind: db.KindDate, Value: nv}, {Column: "n", Kind: db.KindText, Value: "new"}}}}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if got := texts()["new"]; got[0] != "2024-05-06" {
		t.Errorf("inserted date stored as %q", got[0])
	}
	sqliteExec(t, s, "UPDATE ev SET d = '2024-01-02T00:00:00' WHERE n = 'b2'")
	_, rows, err = dbtest.Query(ctx, s, "SELECT d, ts, tm, n FROM ev ORDER BY n")
	if err != nil {
		t.Fatal(err)
	}
	var dels []db.Change
	for _, r := range rows {
		dels = append(dels, db.Change{Op: db.OpDelete, Key: key(r)})
	}
	if _, err := db.ApplyChanges(ctx, s, caps, d, ref, dels); err != nil {
		t.Fatalf("delete by date key: %v", err)
	}
	if n := len(texts()); n != 0 {
		t.Errorf("%d rows left", n)
	}
	// previews render times in the same format
	q, args := db.BuildChange(d, ref, db.Change{Op: db.OpUpdate, Key: key(rows[0]),
		Values: []db.ColValue{{Column: "ts", Kind: db.KindTimestamp, Value: tv}}}, true)
	if len(args) != 0 || !strings.Contains(q, "'2024-02-01 11:00:00'") {
		t.Errorf("preview %q %v", q, args)
	}
}
