//go:build integration

package conformance

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/db/adapters/trino"
	"github.com/twoojoo/lumino/internal/db/dbtest"
)

func trRegressSession(t *testing.T) db.Session {
	t.Helper()
	if !dbtest.Reachable("127.0.0.1:55080") {
		t.Skip("trino not reachable (make db-up)")
	}
	ctx := context.Background()
	cfg := dbtest.Targets()["trino"].Config
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
	return s
}

func TestTrinoRegressKinds(t *testing.T) {
	for typ, want := range map[string]db.Kind{
		"TIMESTAMP(3) WITH TIME ZONE": db.KindTimestampTZ,
		"TIMESTAMP WITH TIME ZONE":    db.KindTimestampTZ,
		"TIMESTAMP(6)":                db.KindTimestamp,
		"TIME WITH TIME ZONE":         db.KindTime,
		"TIME(3) WITH TIME ZONE":      db.KindTime,
		"INTERVAL DAY TO SECOND":      db.KindInterval,
		"DECIMAL(10,2)":               db.KindDecimal,
		"ROW(X INTEGER, Y VARCHAR)":   db.KindArray,
		"VARCHAR(5)":                  db.KindText,
	} {
		if got := trino.KindOf(typ); got != want {
			t.Errorf("KindOf(%q) = %v, want %v", typ, got, want)
		}
	}
}

func TestTrinoRegressTimeZonesAndRows(t *testing.T) {
	s := trRegressSession(t)
	cols, rows, err := dbtest.Query(context.Background(), s, `SELECT
  TIMESTAMP '2024-02-29 13:14:15.123 +02:00' AS tstz,
  CAST(TIMESTAMP '2024-02-29 13:14:15 UTC' AS timestamp(0) with time zone) AS tstz0,
  TIME '13:14:15 +02:00' AS ttz,
  CAST(ROW('x', 1) AS ROW(a varchar, b integer)) AS named,
  ROW('y', 2) AS anon,
  ARRAY[CAST(ROW(1) AS ROW(n integer))] AS arr,
  CAST(NULL AS ROW(a integer)) AS nullrow`)
	if err != nil {
		t.Fatal(err)
	}
	r := rows[0]
	for i, want := range []db.Kind{db.KindTimestampTZ, db.KindTimestampTZ, db.KindTime, db.KindArray, db.KindArray, db.KindArray} {
		if cols[i].Kind != want {
			t.Errorf("%s (%s): kind %v, want %v", cols[i].Name, cols[i].DBType, cols[i].Kind, want)
		}
	}
	if ts, ok := r[0].(time.Time); !ok || !ts.Equal(time.Date(2024, 2, 29, 11, 14, 15, 123e6, time.UTC)) {
		t.Errorf("timestamp with time zone value %#v", r[0])
	}
	if !reflect.DeepEqual(r[3], map[string]any{"a": "x", "b": int64(1)}) {
		t.Errorf("named row %#v", r[3])
	}
	if !reflect.DeepEqual(r[4], []any{"y", int64(2)}) {
		t.Errorf("anonymous row %#v", r[4])
	}
	if !reflect.DeepEqual(r[5], []any{map[string]any{"n": int64(1)}}) {
		t.Errorf("array of rows %#v", r[5])
	}
	if r[6] != nil {
		t.Errorf("NULL row %#v", r[6])
	}
	for i := 3; i < 6; i++ {
		if f := db.FormatValue(r[i], cols[i].Kind); strings.Contains(f, "true}") || strings.Contains(f, "[[") {
			t.Errorf("%s displayed as %s", cols[i].Name, f)
		}
	}
}

func TestTrinoRegressIPv6Host(t *testing.T) {
	cfg := dbtest.Targets()["trino"].Config
	cfg.Host = "::1" // nothing listens there: the dial must fail, not the URL parsing
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := db.Open(ctx, &cfg, db.OpenOptions{})
	if err == nil {
		conn.Close()
		return
	}
	if msg := err.Error(); strings.Contains(msg, "invalid port") || strings.Contains(msg, "too many colons") || strings.Contains(msg, "parse") {
		t.Fatalf("IPv6 address not bracketed: %v", err)
	}
}
