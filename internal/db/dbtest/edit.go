package dbtest

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/db/sqlx"
)

// defaultSamples are written to all_types columns by name.
var defaultSamples = map[string]string{
	"c_int":     "-123456",
	"c_bigint":  "9007199254740993",
	"c_dec":     "12345678.1234",
	"c_float":   "3.25",
	"c_bool":    "true",
	"c_text":    "héllo\nwörld ✓ 'q' \\ \"dq\"",
	"c_varchar": "abc;def",
	"c_date":    "2024-02-29",
	"c_time":    "13:14:15",
	"c_ts":      "2024-02-29 13:14:15.123456",
	"c_tstz":    "2024-02-29 13:14:15.123456 +02:00",
	"c_bytes":   "0x00ff10",
	"c_json":    `{"a": [1, 2], "b": "x"}`,
	"c_uuid":    "8f3e1c2a-4b5d-4e6f-8a9b-0c1d2e3f4a5b",
}

func readRow(t *testing.T, ctx context.Context, s db.Session, d db.Dialect, tg *Target, table, where string) ([]db.Column, db.Row) {
	t.Helper()
	q := "SELECT * FROM " + d.Qualified(tg.ref(table)) + " WHERE " + where
	cols, rows, err := Query(ctx, s, q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	if len(rows) != 1 {
		t.Fatalf("%s: %d rows", q, len(rows))
	}
	return cols, rows[0]
}

func colIndex(cols []db.Column, name string) int {
	for i, c := range cols {
		if strings.EqualFold(c.Name, name) {
			return i
		}
	}
	return -1
}

func waitEventual(tg *Target) {
	if tg.Eventual {
		time.Sleep(1500 * time.Millisecond)
	}
}

func rowEdit(t *testing.T, ctx context.Context, conn db.Conn, tg *Target) {
	caps := conn.Caps()
	if !caps.Insert {
		t.Skip("read-only database")
	}
	d := conn.Dialect()
	s := session(t, conn)
	ref := tg.ref("customers")
	id, name, email := tg.id("id"), tg.id("name"), tg.id("email")
	key := []db.ColValue{{Column: id, Kind: db.KindInt, Value: int64(9100)}}
	apply := func(chs ...db.Change) error {
		_, err := db.ApplyChanges(ctx, s, caps, d, ref, chs)
		waitEventual(tg)
		return err
	}
	ins := db.Change{Op: db.OpInsert, Values: []db.ColValue{
		{Column: id, Kind: db.KindInt, Value: int64(9100)},
		{Column: name, Kind: db.KindText, Value: "edit-me"},
		{Column: email, Kind: db.KindText, Value: "edit@example.com"},
	}}
	if err := apply(ins); err != nil {
		t.Fatalf("insert: %v", err)
	}
	where := d.Quote(id) + " = 9100"
	cols, row := readRow(t, ctx, s, d, tg, "customers", where)
	if got := row[colIndex(cols, name)]; got != "edit-me" {
		t.Fatalf("inserted name %#v", got)
	}
	if !caps.Update {
		return
	}
	upd := db.Change{Op: db.OpUpdate, Key: key, Values: []db.ColValue{{Column: name, Kind: db.KindText, Value: "edited"}, {Column: email, Kind: db.KindText, Value: nil}}}
	if err := apply(upd); err != nil {
		t.Fatalf("update: %v", err)
	}
	cols, row = readRow(t, ctx, s, d, tg, "customers", where)
	if got := row[colIndex(cols, name)]; got != "edited" {
		t.Fatalf("updated name %#v", got)
	}
	if got := row[colIndex(cols, email)]; got != nil {
		t.Fatalf("email should be NULL, got %#v", got)
	}
	if caps.Transactions {
		missing := db.Change{Op: db.OpUpdate, Key: []db.ColValue{{Column: id, Kind: db.KindInt, Value: int64(424242)}},
			Values: []db.ColValue{{Column: name, Kind: db.KindText, Value: "x"}}}
		// the batch fails as a whole: the first update must be rolled back
		first := db.Change{Op: db.OpUpdate, Key: key, Values: []db.ColValue{{Column: name, Kind: db.KindText, Value: "should-rollback"}}}
		if err := apply(first, missing); err == nil {
			t.Fatal("update of a missing row succeeded")
		}
		cols, row = readRow(t, ctx, s, d, tg, "customers", where)
		if got := row[colIndex(cols, name)]; got != "edited" {
			t.Fatalf("failed batch was not rolled back: name %#v", got)
		}
	}
	// composite key
	items := tg.ref("order_items")
	ik := []db.ColValue{{Column: tg.id("order_id"), Kind: db.KindInt, Value: int64(1)}, {Column: tg.id("line_no"), Kind: db.KindInt, Value: int64(99)}}
	if _, err := db.ApplyChanges(ctx, s, caps, d, items, []db.Change{{Op: db.OpInsert, Values: append(append([]db.ColValue{}, ik...),
		db.ColValue{Column: tg.id("sku"), Kind: db.KindText, Value: "SKU-99"}, db.ColValue{Column: tg.id("qty"), Kind: db.KindInt, Value: int64(2)})}}); err != nil {
		t.Fatalf("composite insert: %v", err)
	}
	waitEventual(tg)
	if _, err := db.ApplyChanges(ctx, s, caps, d, items, []db.Change{{Op: db.OpUpdate, Key: ik,
		Values: []db.ColValue{{Column: tg.id("qty"), Kind: db.KindInt, Value: int64(7)}}}}); err != nil {
		t.Fatalf("composite update: %v", err)
	}
	waitEventual(tg)
	iw := d.Quote(tg.id("order_id")) + " = 1 AND " + d.Quote(tg.id("line_no")) + " = 99"
	cols, row = readRow(t, ctx, s, d, tg, "order_items", iw)
	if got := db.FormatValue(row[colIndex(cols, tg.id("qty"))], db.KindInt); got != "7" {
		t.Fatalf("composite update qty %q", got)
	}
	if caps.Delete {
		if err := apply(db.Change{Op: db.OpDelete, Key: key}); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if _, err := db.ApplyChanges(ctx, s, caps, d, items, []db.Change{{Op: db.OpDelete, Key: ik}}); err != nil {
			t.Fatalf("composite delete: %v", err)
		}
		waitEventual(tg)
		_, rows, err := Query(ctx, s, "SELECT * FROM "+d.Qualified(ref)+" WHERE "+where)
		if err != nil || len(rows) != 0 {
			t.Fatalf("row still there after delete (%v)", err)
		}
	}
	// previews render every value as a literal
	q, args := db.BuildChange(d, ref, upd, true)
	if len(args) != 0 || !strings.Contains(q, "'edited'") {
		t.Errorf("literal preview %q %v", q, args)
	}
}

func typesRoundTrip(t *testing.T, ctx context.Context, conn db.Conn, tg *Target) {
	caps := conn.Caps()
	if !caps.Update {
		t.Skip("no updates")
	}
	d := conn.Dialect()
	s := session(t, conn)
	ref := tg.ref("all_types")
	info, err := conn.Catalog().Table(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	idc := tg.id("id")
	key := []db.ColValue{{Column: idc, Kind: db.KindInt, Value: int64(1)}}
	where := d.Quote(idc) + " = 1"
	for _, col := range info.Columns {
		if col.PrimaryKeyNo > 0 {
			continue
		}
		lc := strings.ToLower(col.Name)
		sample, ok := tg.Samples[lc]
		if !ok {
			sample, ok = defaultSamples[lc]
		}
		if !ok || sample == "-" {
			continue
		}
		t.Run(lc, func(t *testing.T) {
			v, err := db.ParseValue(sample, col.Kind)
			if err != nil {
				t.Fatalf("parse %q as %s: %v", sample, col.Kind, err)
			}
			ch := db.Change{Op: db.OpUpdate, Key: key, Values: []db.ColValue{{Column: col.Name, Kind: col.Kind, Value: v}}}
			if _, err := db.ApplyChanges(ctx, s, caps, d, ref, []db.Change{ch}); err != nil {
				t.Fatalf("write %s (%s, kind %s) = %q: %v", col.Name, col.Type, col.Kind, sample, err)
			}
			waitEventual(tg)
			cols, row := readRow(t, ctx, s, d, tg, "all_types", where)
			i := colIndex(cols, col.Name)
			got := row[i]
			if !sameValue(col.Kind, sample, got, cols[i].Kind) {
				t.Errorf("%s (%s, kind %s/%s): wrote %q, read %#v (%q)", col.Name, col.Type, col.Kind, cols[i].Kind, sample, got, db.FormatValue(got, cols[i].Kind))
			}
			// the preview literal is valid SQL for the same value
			lq, _ := db.BuildChange(d, ref, ch, true)
			if res, err := s.Execute(ctx, lq); err != nil {
				t.Errorf("literal statement failed: %s: %v", lq, err)
			} else {
				res.Close()
			}
			waitEventual(tg)
			if col.Nullable {
				ch.Values[0].Value = nil
				if _, err := db.ApplyChanges(ctx, s, caps, d, ref, []db.Change{ch}); err != nil {
					t.Fatalf("write NULL: %v", err)
				}
				waitEventual(tg)
				cols, row = readRow(t, ctx, s, d, tg, "all_types", where)
				if got := row[colIndex(cols, col.Name)]; got != nil {
					t.Errorf("NULL read back as %#v", got)
				}
			}
			lt := strings.ToLower(col.Type)
			textual := strings.Contains(lt, "char") || strings.Contains(lt, "text") || strings.Contains(lt, "string") || strings.Contains(lt, "clob")
			if col.Kind == db.KindText && col.Nullable && textual {
				ch.Values[0].Value = ""
				if _, err := db.ApplyChanges(ctx, s, caps, d, ref, []db.Change{ch}); err != nil {
					t.Fatalf("write empty: %v", err)
				}
				waitEventual(tg)
				cols, row = readRow(t, ctx, s, d, tg, "all_types", where)
				got := row[colIndex(cols, col.Name)]
				if tg.EmptyIsNull {
					if got != nil {
						t.Errorf("empty string read back as %#v", got)
					}
				} else if got != "" {
					t.Errorf("empty string read back as %#v", got)
				}
			}
		})
	}
}

// sameValue compares a written sample with the value read back.
func sameValue(k db.Kind, sample string, got any, readKind db.Kind) bool {
	if got == nil {
		return false
	}
	txt := db.FormatValue(got, readKind)
	switch k {
	case db.KindInt, db.KindDecimal:
		a, ok1 := new(big.Rat).SetString(strings.TrimSpace(sample))
		b, ok2 := new(big.Rat).SetString(strings.TrimSpace(txt))
		return ok1 && ok2 && a.Cmp(b) == 0
	case db.KindFloat:
		a, err1 := strconv.ParseFloat(sample, 64)
		b, err2 := strconv.ParseFloat(txt, 64)
		return err1 == nil && err2 == nil && math.Abs(a-b) < 1e-6*math.Max(1, math.Abs(a))
	case db.KindBool:
		b, ok := got.(bool)
		if !ok {
			return txt == "1" && sample == "true" || txt == "0" && sample == "false"
		}
		return b == (sample == "true")
	case db.KindBytes:
		want, _ := db.ParseValue(sample, db.KindBytes)
		gb, ok := got.([]byte)
		if !ok {
			if s, ok := got.(string); ok {
				gb = []byte(s)
			}
		}
		return bytes.Equal(want.([]byte), gb)
	case db.KindJSON:
		var a, b any
		if json.Unmarshal([]byte(sample), &a) != nil || json.Unmarshal([]byte(txt), &b) != nil {
			return false
		}
		return reflect.DeepEqual(a, b)
	case db.KindArray:
		return strings.ReplaceAll(txt, " ", "") == strings.ReplaceAll(sample, " ", "")
	case db.KindUUID:
		return strings.EqualFold(strings.ReplaceAll(txt, "-", ""), strings.ReplaceAll(sample, "-", ""))
	case db.KindDate, db.KindTime, db.KindTimestamp, db.KindTimestampTZ:
		want, err := db.ParseTime(sample, k)
		if err != nil {
			return false
		}
		var gt time.Time
		switch x := got.(type) {
		case time.Time:
			gt = x
		case string:
			if gt, err = db.ParseTime(x, k); err != nil {
				return false
			}
		default:
			return false
		}
		switch k {
		case db.KindDate:
			return gt.Format("2006-01-02") == want.Format("2006-01-02")
		case db.KindTime:
			return gt.Format("15:04:05") == want.Format("15:04:05")
		case db.KindTimestampTZ:
			return gt.Equal(want)
		}
		// wall clock, ignoring the zone the driver attached
		return gt.Format("2006-01-02 15:04:05.000000") == want.Format("2006-01-02 15:04:05.000000")
	}
	return txt == sample
}

func script(t *testing.T, ctx context.Context, conn db.Conn, tg *Target) {
	d := conn.Dialect()
	cust := d.Qualified(tg.ref("customers"))
	sc := "-- leading comment\nSELECT COUNT(*) FROM " + cust + ";\n/* block; comment */\nSELECT " + d.Quote(tg.id("name")) +
		" FROM " + cust + " WHERE " + d.Quote(tg.id("name")) + " = 'a;b'" + tg.FilterSuffix + ";\n"
	stmts := sqlx.Split(sc, d.Lex())
	if len(stmts) != 2 {
		t.Fatalf("split into %d statements: %+v", len(stmts), stmts)
	}
	s := session(t, conn)
	for _, st := range stmts {
		if _, _, err := Query(ctx, s, st.Text); err != nil {
			t.Fatalf("%s: %v", st.Text, err)
		}
	}
}
