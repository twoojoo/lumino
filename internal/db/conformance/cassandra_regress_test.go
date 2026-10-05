//go:build integration

package conformance

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/db/dbtest"
)

func TestCassandraRegress(t *testing.T) { cqlRegress(t, "cassandra") }
func TestScyllaRegress(t *testing.T)    { cqlRegress(t, "scylla") }

const regressKS = "lumino_regress"

func cqlExec(t *testing.T, s db.Session, q string) {
	t.Helper()
	res, err := s.Execute(context.Background(), q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	res.Close()
}

func cqlRegress(t *testing.T, name string) {
	tg := dbtest.Targets()[name]
	needs(t, fmt.Sprintf("%s:%d", tg.Config.Host, tg.Config.Port))
	ctx := context.Background()
	cfg := tg.Config
	conn, err := db.Open(ctx, &cfg, db.OpenOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()
	s, err := conn.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cqlExec(t, s, "DROP KEYSPACE IF EXISTS "+regressKS)
	cqlExec(t, s, `DROP KEYSPACE IF EXISTS "LuminoRegress"`)
	cqlExec(t, s, "CREATE KEYSPACE "+regressKS+" WITH replication = {'class': 'SimpleStrategy', 'replication_factor': 1}")
	defer func() {
		cl, err := conn.Session(ctx)
		if err == nil {
			for _, q := range []string{"DROP KEYSPACE IF EXISTS " + regressKS, `DROP KEYSPACE IF EXISTS "LuminoRegress"`} {
				if res, err := cl.Execute(ctx, q); err == nil {
					res.Close()
				}
			}
			cl.Close()
		}
	}()
	d := conn.Dialect()

	t.Run("tuples", func(t *testing.T) {
		cqlExec(t, s, "CREATE TABLE "+regressKS+".tup (id int PRIMARY KEY, tp tuple<int, text>, nt tuple<int, frozen<tuple<text, uuid>>>, lt list<frozen<tuple<int, text>>>)")
		cqlExec(t, s, "INSERT INTO "+regressKS+".tup (id, tp, nt, lt) VALUES (1, (7, 'seven'), (1, ('x', 11111111-2222-3333-4444-555555555555)), [(1, 'a'), (2, null)])")
		cqlExec(t, s, "INSERT INTO "+regressKS+".tup (id) VALUES (2)")
		cols, rows, err := dbtest.Query(ctx, s, "SELECT id, tp, nt, lt FROM "+regressKS+".tup WHERE id IN (1, 2)")
		if err != nil {
			t.Fatalf("tuple select: %v", err)
		}
		if len(rows) != 2 {
			t.Fatalf("rows %v", rows)
		}
		if cols[1].DBType != "frozen<tuple<int, text>>" {
			t.Errorf("tuple DBType %q", cols[1].DBType)
		}
		for _, r := range rows {
			switch r[0] {
			case int64(1):
				if got := fmt.Sprint(r[1]); got != "[7 seven]" {
					t.Errorf("tuple %#v", r[1])
				}
				if got := fmt.Sprint(r[2]); got != "[1 [x 11111111-2222-3333-4444-555555555555]]" {
					t.Errorf("nested tuple %#v", r[2])
				}
				if got := fmt.Sprint(r[3]); got != "[[1 a] [2 <nil>]]" {
					t.Errorf("list of tuples %#v", r[3])
				}
			case int64(2):
				if r[1] != nil || r[2] != nil || r[3] != nil {
					t.Errorf("NULL tuples read as %#v", r)
				}
			}
		}
	})

	t.Run("paging after execute ctx cancelled", func(t *testing.T) {
		cqlExec(t, s, "CREATE TABLE "+regressKS+".pages (id int PRIMARY KEY)")
		for b := 0; b < 25; b++ {
			var sb strings.Builder
			sb.WriteString("BEGIN UNLOGGED BATCH\n")
			for i := b * 100; i < b*100+100; i++ {
				fmt.Fprintf(&sb, "INSERT INTO %s.pages (id) VALUES (%d);\n", regressKS, i)
			}
			sb.WriteString("APPLY BATCH")
			cqlExec(t, s, sb.String())
		}
		ectx, cancel := context.WithCancel(ctx)
		res, err := s.Execute(ectx, "SELECT id FROM "+regressKS+".pages")
		cancel() // the UI cancels the Execute context, keeping the cursor
		if err != nil {
			t.Fatal(err)
		}
		defer res.Close()
		n := 0
		for {
			rows, more, err := res.Fetch(ctx, 500)
			if err != nil {
				t.Fatalf("fetch after %d rows: %v", n, err)
			}
			n += len(rows)
			if !more {
				break
			}
		}
		if n != 2500 {
			t.Fatalf("got %d rows, want 2500", n)
		}
		// a cancelled Fetch context still interrupts
		res2, err := s.Execute(ctx, "SELECT id FROM "+regressKS+".pages")
		if err != nil {
			t.Fatal(err)
		}
		defer res2.Close()
		fctx, fcancel := context.WithCancel(ctx)
		fcancel()
		if _, _, err := res2.Fetch(fctx, 5000); err == nil {
			t.Error("cancelled fetch succeeded")
		}
	})

	t.Run("grid edits", func(t *testing.T) {
		cqlExec(t, s, "CREATE TABLE "+regressKS+".edits (id uuid PRIMARY KEY, f float, dbl double, tm time, dec decimal, du duration, l list<int>, vi varint, b bigint)")
		ref := db.ObjectRef{Schema: regressKS, Name: "edits", Type: db.ObjTable}
		info, err := conn.Catalog().Table(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		kind := func(c string) db.Kind {
			tc, _ := info.Column(c)
			return tc.Kind
		}
		val := func(c, txt string) db.ColValue {
			v, err := db.ParseValue(txt, kind(c))
			if err != nil {
				t.Fatalf("parse %s=%q: %v", c, txt, err)
			}
			return db.ColValue{Column: c, Kind: kind(c), Value: v}
		}
		id := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
		ins := db.Change{Op: db.OpInsert, Values: []db.ColValue{val("id", id), val("f", "1.5"), val("dbl", "2.25"),
			val("tm", "13:14:15.5"), val("dec", "-1.25e3"), val("du", "1h30m"), val("vi", "123456789012345678901234567890"), val("b", "42")}}
		if _, err := db.ApplyChanges(ctx, s, conn.Caps(), d, ref, []db.Change{ins}); err != nil {
			t.Fatalf("insert: %v", err)
		}
		_, rows, err := dbtest.Query(ctx, s, "SELECT f, dbl, tm, dec, du, vi, b FROM "+regressKS+".edits WHERE id = "+id)
		if err != nil || len(rows) != 1 {
			t.Fatalf("read back: %v %v", rows, err)
		}
		if got := fmt.Sprint(rows[0]...); got != fmt.Sprint(1.5, 2.25, "13:14:15.5", "-1250", "0mo0d5400000000000ns", "123456789012345678901234567890", int64(42)) {
			t.Errorf("read back %#v", rows[0])
		}
		// the preview literals execute too
		lq, _ := db.BuildChange(d, ref, ins, true)
		if res, err := s.Execute(ctx, lq); err != nil {
			t.Errorf("literal insert %s: %v", lq, err)
		} else {
			res.Close()
		}
		// edits keyed by the values read back (uuid, float, time)
		_, rows, err = dbtest.Query(ctx, s, "SELECT id, f, tm, du FROM "+regressKS+".edits WHERE id = "+id)
		if err != nil || len(rows) != 1 {
			t.Fatal(err)
		}
		key := []db.ColValue{{Column: "id", Kind: db.KindUUID, Value: rows[0][0]}}
		upd := db.Change{Op: db.OpUpdate, Key: key, Values: []db.ColValue{{Column: "f", Kind: db.KindFloat, Value: rows[0][1]},
			{Column: "tm", Kind: db.KindTime, Value: rows[0][2]}, {Column: "du", Kind: db.KindInterval, Value: rows[0][3]}}}
		if _, err := db.ApplyChanges(ctx, s, conn.Caps(), d, ref, []db.Change{upd}); err != nil {
			t.Fatalf("update with read values: %v", err)
		}
		refused := map[string]db.ColValue{
			"uuid()":     val("id", "uuid()"),
			"1/3":        {Column: "dec", Kind: db.KindDecimal, Value: "1/3"},
			"list":       val("l", "[1, 2]"),
			"bad time":   {Column: "tm", Kind: db.KindTime, Value: "25:00:00"},
			"injected":   {Column: "dec", Kind: db.KindDecimal, Value: "1); DROP KEYSPACE x; --"},
			"bad varint": {Column: "vi", Kind: db.KindInt, Value: "12 OR 1=1"},
		}
		for what, cv := range refused {
			var ch db.Change
			if cv.Column == "id" {
				ch = db.Change{Op: db.OpInsert, Values: []db.ColValue{cv}}
			} else {
				ch = db.Change{Op: db.OpUpdate, Key: key, Values: []db.ColValue{cv}}
			}
			q, _ := db.BuildChange(d, ref, ch, false)
			if strings.Contains(q, "uuid()") || strings.Contains(q, "1/3") || strings.Contains(q, "DROP") || strings.Contains(q, "OR 1") {
				t.Errorf("%s: user text inlined: %s", what, q)
			}
			_, err := db.ApplyChanges(ctx, s, conn.Caps(), d, ref, []db.Change{ch})
			if err == nil || !strings.Contains(err.Error(), cv.Column) {
				t.Errorf("%s: accepted or unclear error: %v", what, err)
			}
		}
		// Literal renders UUIDs bare: "filter by this value"
		where := "id = " + d.Literal(id, db.KindUUID)
		_, rows, err = dbtest.Query(ctx, s, d.SelectAll(ref, where, ""))
		if err != nil || len(rows) != 1 {
			t.Errorf("filter %s: %v %v", where, rows, err)
		}
	})

	t.Run("lightweight transactions", func(t *testing.T) {
		cqlExec(t, s, "CREATE TABLE "+regressKS+".lwt (id int PRIMARY KEY, v int)")
		applied := func(q string) bool {
			t.Helper()
			cols, rows, err := dbtest.Query(ctx, s, q)
			if err != nil {
				t.Fatalf("%s: %v", q, err)
			}
			if len(cols) == 0 || cols[0].Name != "[applied]" || len(rows) != 1 {
				t.Fatalf("%s: no [applied] row: %v %v", q, cols, rows)
			}
			return rows[0][0] == true
		}
		if !applied("INSERT INTO " + regressKS + ".lwt (id, v) VALUES (1, 1) IF NOT EXISTS") {
			t.Error("first insert not applied")
		}
		if applied("INSERT INTO " + regressKS + ".lwt (id, v) VALUES (1, 2) if not exists") {
			t.Error("duplicate insert applied")
		}
		if applied("UPDATE " + regressKS + ".lwt SET v = 3 WHERE id = 1 IF v = 99") {
			t.Error("conditional update applied")
		}
		if applied("DELETE FROM " + regressKS + ".lwt WHERE id = 1 IF v = 99") {
			t.Error("conditional delete applied")
		}
		// plain DML and DDL with IF stay row-less
		res, err := s.Execute(ctx, "CREATE TABLE IF NOT EXISTS "+regressKS+".lwt (id int PRIMARY KEY, v int)")
		if err != nil {
			t.Fatal(err)
		}
		if res.Columns() != nil {
			t.Error("DDL returned columns")
		}
		res.Close()
	})

	t.Run("use folds case", func(t *testing.T) {
		cqlExec(t, s, `CREATE KEYSPACE "LuminoRegress" WITH replication = {'class': 'SimpleStrategy', 'replication_factor': 1}`)
		s2, err := conn.Session(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer s2.Close()
		cqlExec(t, s2, "USE LUMINO_REGRESS")
		if ks, _ := s2.CurrentSchema(ctx); ks != regressKS {
			t.Errorf("USE LUMINO_REGRESS -> %q", ks)
		}
		cqlExec(t, s2, `USE "LuminoRegress";`)
		if ks, _ := s2.CurrentSchema(ctx); ks != "LuminoRegress" {
			t.Errorf(`USE "LuminoRegress" -> %q`, ks)
		}
		if res, err := s2.Execute(ctx, "USE LuminoRegress"); err == nil {
			res.Close()
			t.Error("unquoted mixed case matched a case-sensitive keyspace")
		}
	})

	t.Run("column types", func(t *testing.T) {
		cqlExec(t, s, "CREATE TYPE "+regressKS+".addr (street text, no int)")
		cqlExec(t, s, "CREATE TABLE "+regressKS+".types (id int PRIMARY KEY, m map<text, int>, l list<int>, st set<text>, fl frozen<list<frozen<map<int, text>>>>, a frozen<addr>, tu timeuuid, v varchar)")
		cqlExec(t, s, "INSERT INTO "+regressKS+".types (id, m, a) VALUES (1, {'k': 1}, {street: 'Main', no: 3})")
		cols, rows, err := dbtest.Query(ctx, s, "SELECT id, m, l, st, fl, a, tu, v FROM "+regressKS+".types")
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"int", "map<text, int>", "list<int>", "set<text>", "list<frozen<map<int, text>>>", "addr", "timeuuid", "text"}
		for i, w := range want {
			if cols[i].DBType != w {
				t.Errorf("%s: DBType %q, want %q", cols[i].Name, cols[i].DBType, w)
			}
		}
		if len(rows) != 1 || fmt.Sprint(rows[0][5]) != "map[no:3 street:Main]" {
			t.Errorf("udt value %v", rows)
		}
	})
}

// TestCassandraTLSHostNames: verify-full checks every node against its own
// name (the SNI each node receives), not against the first host's.
func TestCassandraTLSHostNames(t *testing.T) {
	var mu sync.Mutex
	sni := map[string][]string{}
	listen := func(tag string) int {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		cfg := &tls.Config{GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			mu.Lock()
			sni[tag] = append(sni[tag], h.ServerName)
			mu.Unlock()
			return nil, errors.New("test listener")
		}}
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func() {
					defer c.Close()
					_ = c.SetDeadline(time.Now().Add(5 * time.Second))
					_ = tls.Server(c, cfg).Handshake()
				}()
			}
		}()
		return ln.Addr().(*net.TCPAddr).Port
	}
	pa, pb := listen("ip"), listen("localhost")
	cfg := db.Config{Driver: "cassandra", Host: fmt.Sprintf("127.0.0.1:%d,localhost:%d", pa, pb),
		TLS: db.TLSConfig{Mode: "verify-full", CAFile: devPath("certs", "ca.crt")}}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if c, err := db.Open(ctx, &cfg, db.OpenOptions{}); err == nil {
		c.Close()
		t.Fatal("connected to a fake node")
	}
	mu.Lock()
	defer mu.Unlock()
	got := sni["localhost"]
	if len(got) == 0 {
		t.Skipf("the driver never dialled the second host (%v)", sni)
	}
	for _, n := range got {
		if n != "localhost" {
			t.Errorf("second node verified as %q, want localhost (all: %v)", n, sni)
		}
	}
}
