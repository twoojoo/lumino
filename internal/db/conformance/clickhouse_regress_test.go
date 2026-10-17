//go:build integration

package conformance

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/db/adapters/clickhouse"
	"github.com/twoojoo/lumino/internal/db/dbtest"
)

func chRegressConn(t *testing.T) (db.Conn, db.Session) {
	t.Helper()
	if !dbtest.Reachable("127.0.0.1:55900") {
		t.Skip("clickhouse not reachable (make db-up)")
	}
	ctx := context.Background()
	cfg := dbtest.Targets()["clickhouse"].Config
	conn, err := db.Open(ctx, &cfg, db.OpenOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	s, err := conn.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return conn, s
}

func chExec(t *testing.T, s db.Session, q string) {
	t.Helper()
	res, err := s.Execute(context.Background(), q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	res.Close()
}

const chBig = "170141183460469231731687303715884105728" // 2^127, beyond Int128

func TestClickHouseRegressKinds(t *testing.T) {
	for typ, want := range map[string]db.Kind{
		"UInt128": db.KindDecimal, "Nullable(Int256)": db.KindDecimal, "UInt64": db.KindInt, "Int8": db.KindInt,
		"IntervalDay": db.KindInterval, "IntervalSecond": db.KindInterval, "INTERVALYEAR": db.KindInterval,
	} {
		if got := clickhouse.KindOf(typ); got != want {
			t.Errorf("KindOf(%q) = %v, want %v", typ, got, want)
		}
	}
}

// Map columns whose keys or values are themselves (U)Int128/256 panic inside
// clickhouse-go (column.Map.row assigns big.Int to *big.Int), before the
// adapter sees the value, and unnamed tuples holding them come back as NULL
// (Tuple.Row swallows a scan error): not covered here.
func TestClickHouseRegressBigInts(t *testing.T) {
	_, s := chRegressConn(t)
	cols, rows, err := dbtest.Query(context.Background(), s, `SELECT toUInt128('`+chBig+`') AS u128, toInt256(-5) AS i256,
  [toUInt128(1), toUInt128(2)] AS arr, CAST(NULL AS Nullable(Int128)) AS nul, toNullable(toInt128(7)) AS nn,
  map('a', [toUInt128(3)]) AS m, CAST((9, 'a') AS Tuple(n UInt256, s String)) AS tup, toIntervalDay(3) AS iv`)
	if err != nil {
		t.Fatal(err)
	}
	r := rows[0]
	want := []any{chBig, "-5", []any{"1", "2"}, nil, "7", map[string]any{"a": []any{"3"}}, map[string]any{"n": "9", "s": "a"}}
	for i, w := range want {
		if !reflect.DeepEqual(r[i], w) {
			t.Errorf("%s (%s) = %#v, want %#v", cols[i].Name, cols[i].DBType, r[i], w)
		}
	}
	if cols[0].Kind != db.KindDecimal || cols[1].Kind != db.KindDecimal {
		t.Errorf("big int kinds %v %v", cols[0].Kind, cols[1].Kind)
	}
	if cols[7].Kind == db.KindInt {
		t.Errorf("interval column %s has kind int", cols[7].DBType)
	}
}

func TestClickHouseRegressEditBigIntKey(t *testing.T) {
	conn, s := chRegressConn(t)
	ctx := context.Background()
	chExec(t, s, "DROP TABLE IF EXISTS default.lumino_reg_u128")
	t.Cleanup(func() { chExec(t, s, "DROP TABLE IF EXISTS default.lumino_reg_u128") })
	chExec(t, s, "CREATE TABLE default.lumino_reg_u128 (id UInt128, v String) ENGINE = MergeTree ORDER BY id")
	chExec(t, s, "INSERT INTO default.lumino_reg_u128 VALUES ("+chBig+", 'old'), (1, 'other')")
	ref := db.ObjectRef{Schema: "default", Name: "lumino_reg_u128", Type: db.ObjTable}
	ti, err := conn.Catalog().Table(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	cols, rows, err := dbtest.Query(ctx, s, "SELECT id, v FROM default.lumino_reg_u128 WHERE v = 'old'")
	if err != nil || len(rows) != 1 {
		t.Fatalf("%v %v", rows, err)
	}
	// the grid keys rows on the values it read
	key := []db.ColValue{{Column: "id", Kind: ti.Columns[0].Kind, Value: rows[0][0]}}
	if cols[0].Kind != ti.Columns[0].Kind {
		t.Errorf("result kind %v, catalog kind %v", cols[0].Kind, ti.Columns[0].Kind)
	}
	ch := db.Change{Op: db.OpUpdate, Key: key, Values: []db.ColValue{{Column: "v", Kind: db.KindText, Value: "new"}}}
	if sql, _ := db.BuildChange(db.DialectOf(conn), ref, ch, true); !strings.Contains(sql, chBig) {
		t.Errorf("preview: %s", sql)
	}
	if _, err := db.ApplyChanges(ctx, s, conn.Caps(), db.DialectOf(conn), ref, []db.Change{ch}); err != nil {
		t.Fatalf("update keyed on UInt128: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	_, rows, err = dbtest.Query(ctx, s, "SELECT v FROM default.lumino_reg_u128 ORDER BY id")
	if err != nil || len(rows) != 2 || rows[0][0] != "other" || rows[1][0] != "new" {
		t.Fatalf("after update: %v %v", rows, err)
	}
	del := db.Change{Op: db.OpDelete, Key: key}
	if _, err := db.ApplyChanges(ctx, s, conn.Caps(), db.DialectOf(conn), ref, []db.Change{del}); err != nil {
		t.Fatalf("delete keyed on UInt128: %v", err)
	}
}

func TestClickHouseRegressIPv6Host(t *testing.T) {
	cfg := dbtest.Targets()["clickhouse"].Config
	cfg.Host = "::1" // nothing listens there: the dial must fail, not the address parsing
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := db.Open(ctx, &cfg, db.OpenOptions{})
	if err == nil {
		conn.Close()
		return
	}
	if msg := err.Error(); strings.Contains(msg, "too many colons") || strings.Contains(msg, "missing port") {
		t.Fatalf("IPv6 address not bracketed: %v", err)
	}
}
