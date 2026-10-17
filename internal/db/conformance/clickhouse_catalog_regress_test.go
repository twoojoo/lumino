//go:build integration

package conformance

import (
	"context"
	"reflect"
	"testing"

	"github.com/twoojoo/lumino/internal/db"
)

// Primary keys with expressions, commas and quoted names give real columns.
func TestClickHouseCatalogRegressKey(t *testing.T) {
	conn, s := chRegressConn(t)
	chExec(t, s, "DROP DATABASE IF EXISTS ccr_cat")
	chExec(t, s, "CREATE DATABASE ccr_cat")
	t.Cleanup(func() { chExec(t, s, "DROP DATABASE IF EXISTS ccr_cat") })
	chExec(t, s, "CREATE TABLE ccr_cat.k (a UInt32, b String, `we,ird` UInt8, `x``y` UInt8, c UInt8) ENGINE = MergeTree "+
		"ORDER BY (a, cityHash64(b, a), `we,ird`, `x``y`)")
	chExec(t, s, "CREATE TABLE ccr_cat.d (ts DateTime, id UInt64, v String) ENGINE = MergeTree ORDER BY (toDate(ts), id)")
	chExec(t, s, "CREATE TABLE ccr_cat.n (v String) ENGINE = MergeTree ORDER BY tuple()")
	chExec(t, s, "CREATE TABLE ccr_cat.p (a UInt8, b UInt8, c UInt8) ENGINE = MergeTree PRIMARY KEY (b, a) ORDER BY (b, a, c)")
	for name, want := range map[string][]string{"k": {"a", "b", "we,ird", "x`y"}, "d": {"ts", "id"}, "n": nil, "p": {"b", "a"}} {
		ti, err := conn.Catalog().Table(context.Background(), db.ObjectRef{Schema: "ccr_cat", Name: name, Type: db.ObjTable})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(ti.PrimaryKey, want) {
			t.Errorf("%s: primary key %q, want %q", name, ti.PrimaryKey, want)
		}
		for _, c := range ti.Columns {
			wantNo := 0
			for i, k := range want {
				if k == c.Name {
					wantNo = i + 1
				}
			}
			if c.PrimaryKeyNo != wantNo {
				t.Errorf("%s.%s: PrimaryKeyNo %d, want %d", name, c.Name, c.PrimaryKeyNo, wantNo)
			}
		}
	}
}
