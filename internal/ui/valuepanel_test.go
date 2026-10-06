//go:build dev && integration

package ui

import (
	"context"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/pointer"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/db/dbtest"
)

// TestUIValuePanelJSON: JSON columns open in highlighted JSON mode, edits
// are validated and stored compact, Indent/Compact reformat, and the drawer
// resizes from its inner edge.
func TestUIValuePanelJSON(t *testing.T) {
	path := sqliteFixture(t, 10)
	cfg := db.Config{Driver: "sqlite", File: path}
	conn, err := db.Open(context.Background(), &cfg, db.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := dbtest.Load(context.Background(), conn, `CREATE TABLE docs (id INTEGER PRIMARY KEY, doc JSON, note TEXT);
INSERT INTO docs VALUES (1, '{"a":1,"b":[1,2]}', 'plain'), (2, 'not json', 'x');`); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	h := newHarness(t, []db.Config{{ID: "lite", Name: "local sqlite", Driver: "sqlite", File: path}}, Options{Connection: "local sqlite"})
	a := h.a
	h.waitFor("connected", func() bool { return a.connState("lite").status == stReady })
	a.openObject("lite", db.ObjectRef{Schema: "main", Name: "docs", Type: db.ObjTable}, "data")
	var tt *TableTab
	h.waitFor("table", func() bool {
		tt, _ = a.activeTab().(*TableTab)
		return tt != nil && tt.data != nil && tt.info != nil && len(tt.data.rs.rows) == 2
	})
	v := tt.value
	v.open = true
	tt.grid.SelectCell(0, 1)
	h.settle(100 * time.Millisecond)
	if v.mode != 1 {
		t.Fatalf("JSON column opened in mode %d", v.mode)
	}
	if got := v.jed.Text(); got != "{\n  \"a\": 1,\n  \"b\": [\n    1,\n    2\n  ]\n}" {
		t.Fatalf("indented JSON: %q", got)
	}
	h.shot("40-value-json")

	// compact button, then an edit moved away from: stored compact
	v.jed.SetText(`{"a": 2, "b": []}`)
	v.jsonChanged = true
	h.settle(50 * time.Millisecond)
	tt.grid.SelectCell(0, 2) // the plain text column: back to text mode
	h.settle(100 * time.Millisecond)
	if got := tt.data.Value(0, 1); got != `{"a":2,"b":[]}` {
		t.Fatalf("saved JSON edit: %#v", got)
	}
	if v.mode != 0 {
		t.Fatalf("text column in mode %d", v.mode)
	}

	// invalid JSON is not applied
	tt.grid.SelectCell(0, 1)
	h.settle(100 * time.Millisecond)
	v.jed.SetText(`{"a": `)
	v.jsonChanged = true
	h.settle(50 * time.Millisecond)
	tt.grid.SelectCell(1, 2)
	h.settle(100 * time.Millisecond)
	if got := tt.data.Value(0, 1); got != `{"a":2,"b":[]}` {
		t.Fatalf("invalid JSON applied: %#v", got)
	}

	// a JSON column holding text: shown as stored, flagged, not editable as JSON
	tt.grid.SelectCell(1, 1)
	h.settle(100 * time.Millisecond)
	if !v.jsonBad || !v.jed.ReadOnly {
		t.Fatalf("invalid stored JSON: bad=%v readOnly=%v", v.jsonBad, v.jed.ReadOnly)
	}

	// drag the inner edge 120px to the left: wider drawer
	h.frame()
	w0 := v.width
	if w0 == 0 {
		w0 = 380
	}
	x := float32(h.size.X - w0)
	y := float32(400)
	h.events(
		pointer.Event{Kind: pointer.Move, Position: f32.Pt(x, y), Source: pointer.Mouse},
		pointer.Event{Kind: pointer.Press, Position: f32.Pt(x, y), Buttons: pointer.ButtonPrimary, Source: pointer.Mouse},
		pointer.Event{Kind: pointer.Move, Position: f32.Pt(x-120, y), Buttons: pointer.ButtonPrimary, Source: pointer.Mouse},
		pointer.Event{Kind: pointer.Release, Position: f32.Pt(x-120, y), Source: pointer.Mouse},
	)
	h.settle(50 * time.Millisecond)
	if got := a.prefs.ValuePanelWidth; got < w0+100 || got > w0+140 {
		t.Fatalf("drawer width after drag: %d (was %d)", got, w0)
	}
	h.shot("41-value-json-wide")
}

// TestUITableDefaultOrder: table data is sorted by primary key unless a
// column is sorted or the preference is off.
func TestUITableDefaultOrder(t *testing.T) {
	path := sqliteFixture(t, 10)
	cfg := db.Config{Driver: "sqlite", File: path}
	conn, err := db.Open(context.Background(), &cfg, db.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := dbtest.Load(context.Background(), conn, `CREATE TABLE ord (k TEXT PRIMARY KEY, v INT);
INSERT INTO ord VALUES ('b', 1), ('a', 2), ('c', 3);`); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	h := newHarness(t, []db.Config{{ID: "lite", Name: "local sqlite", Driver: "sqlite", File: path}}, Options{Connection: "local sqlite"})
	a := h.a
	h.waitFor("connected", func() bool { return a.connState("lite").status == stReady })
	keys := func(tt *TableTab) string {
		s := ""
		for i := range tt.data.rs.rows {
			s += tt.data.Value(i, 0).(string)
		}
		return s
	}
	open := func() *TableTab {
		a.openObject("lite", db.ObjectRef{Schema: "main", Name: "ord", Type: db.ObjTable}, "data")
		var tt *TableTab
		h.waitFor("table", func() bool {
			tt, _ = a.activeTab().(*TableTab)
			return tt != nil && tt.data != nil && len(tt.data.rs.rows) == 3
		})
		return tt
	}
	tt := open()
	if got := keys(tt); got != "abc" || tt.grid.SortCol != 0 {
		t.Fatalf("default order %q (sort col %d, query %s)", got, tt.grid.SortCol, tt.query)
	}
	tt.sortBy(0) // the key column header: descending
	h.waitFor("desc", func() bool { return !tt.running && tt.data != nil && keys(tt) == "cba" })

	a.removeTab(tt)
	a.prefs.SetOrderByPrimaryKey(false)
	tt = open()
	if got := keys(tt); got != "bac" || tt.grid.SortCol != -1 {
		t.Fatalf("natural order %q (sort col %d)", got, tt.grid.SortCol)
	}
}
