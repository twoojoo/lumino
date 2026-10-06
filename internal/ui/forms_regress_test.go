package ui

import (
	"image"
	"strings"
	"testing"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/giovanni/lumino/internal/db"
	_ "github.com/giovanni/lumino/internal/db/adapters/postgres"
	_ "github.com/giovanni/lumino/internal/db/adapters/sqlite"
	"github.com/giovanni/lumino/internal/prefs"
	"github.com/giovanni/lumino/internal/secrets"
)

// newTestConnDialog builds a ConnDialog on a driver without opening the UI.
func newTestConnDialog(t *testing.T, a *App, driver, editing string) *ConnDialog {
	t.Helper()
	d := &ConnDialog{app: a, editing: editing, drivers: db.Drivers(), colorBtns: make([]widget.Clickable, len(connColors))}
	d.drv = -1
	for i, dr := range d.drivers {
		if dr.Info().ID == driver {
			d.drv = i
		}
	}
	if d.drv < 0 {
		t.Fatalf("driver %s not registered", driver)
	}
	return d
}

func TestFormsRegressPasswordNotSavedNotStoredEmpty(t *testing.T) {
	secrets.UseMock()
	// new session, "don't save" connection: the form opens with an empty
	// password; saving it must not store "" (connect would then use it)
	cfg := &db.Config{ID: "c1", Driver: "postgres", Name: "x", Host: "h", User: "u"}
	d := newTestConnDialog(t, nil, "postgres", "c1")
	d.load(cfg)
	c, err := d.config()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.storeSecrets(&c); err != nil {
		t.Fatal(err)
	}
	if pw, ok := secrets.Get("c1", secrets.Password); ok {
		t.Fatalf("empty unsaved password stored: %q", pw)
	}

	// known for the session and then cleared: forgotten (connect asks)
	_ = secrets.Set("c2", secrets.Password, "old", false)
	d = newTestConnDialog(t, nil, "postgres", "c2")
	d.load(&db.Config{ID: "c2", Driver: "postgres", Name: "y", User: "u"})
	d.password.SetText("")
	c, _ = d.config()
	_ = d.storeSecrets(&c)
	if _, ok := secrets.Get("c2", secrets.Password); ok {
		t.Fatal("cleared unsaved password still stored")
	}

	// typed: kept for the session
	d.password.SetText("typed")
	c, _ = d.config()
	_ = d.storeSecrets(&c)
	if pw, _ := secrets.Get("c2", secrets.Password); pw != "typed" {
		t.Fatalf("typed password = %q", pw)
	}

	// saved password, unknown and empty (keyring cleared): not stored
	d = newTestConnDialog(t, nil, "postgres", "c3")
	d.load(&db.Config{ID: "c3", Driver: "postgres", Name: "z", User: "u", SavePassword: true})
	c, _ = d.config()
	_ = d.storeSecrets(&c)
	if _, ok := secrets.Get("c3", secrets.Password); ok {
		t.Fatal("unknown empty saved password stored")
	}

	// new connection with an explicitly empty saved password: stored
	d = newTestConnDialog(t, nil, "postgres", "")
	d.savePw = true
	c, _ = d.config()
	_ = d.storeSecrets(&c)
	if pw, ok := secrets.Get(c.ID, secrets.Password); !ok || pw != "" {
		t.Fatalf("new empty saved password: %q %v", pw, ok)
	}
}

func TestFormsRegressCreateFileToggleOff(t *testing.T) {
	secrets.UseMock()
	cfg := &db.Config{ID: "s1", Driver: "sqlite", Name: "s", File: "/tmp/x.db", Params: map[string]string{"create": "true", "foo": "bar"}}
	d := newTestConnDialog(t, nil, "sqlite", "s1")
	d.load(cfg)
	if !d.createFile {
		t.Fatal("toggle not loaded")
	}
	if strings.Contains(d.params.Text(), "create") {
		t.Fatalf("create duplicated in the parameters text: %q", d.params.Text())
	}
	d.createFile = false
	c, err := d.config()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Params["create"]; ok {
		t.Fatalf("create still set after unchecking: %v", c.Params)
	}
	if c.Params["foo"] != "bar" {
		t.Fatalf("other params lost: %v", c.Params)
	}
	d.createFile = true
	c, _ = d.config()
	if c.Params["create"] != "true" {
		t.Fatalf("create not set: %v", c.Params)
	}
}

func TestFormsRegressEditKeepsGroup(t *testing.T) {
	secrets.UseMock()
	on := true
	old := db.Config{ID: "g1", Driver: "sqlite", Name: "s", File: "/tmp/x.db", Group: "Team", Color: "#59c2ff", ReadOnly: true, Protected: &on, InitSQL: "PRAGMA x"}
	a := &App{prefs: &prefs.Prefs{Connections: []db.Config{old}}}
	d := newTestConnDialog(t, a, "sqlite", "g1")
	d.load(&old)
	c, err := d.config()
	if err != nil {
		t.Fatal(err)
	}
	if c.Group != "Team" || c.Color != old.Color || !c.ReadOnly || c.Protected == nil || !*c.Protected || c.InitSQL != old.InitSQL {
		t.Fatalf("fields lost on edit: %+v", c)
	}
	// form fields still win: unprotect, switch driver → stale host fields cleared
	d.protected = 2
	d.drv = newTestConnDialog(t, a, "postgres", "g1").drv
	d.host.SetText("db")
	c, _ = d.config()
	if c.Protected == nil || *c.Protected || c.File != "" || c.Host != "db" || c.Group != "Team" {
		t.Fatalf("form edits not applied: %+v", c)
	}
}

func TestFormsRegressPickerIndex(t *testing.T) {
	items := []string{"f  ·  public · function", "f  ·  public · function", "g"}
	var got []int
	var gotStr []string
	p := newPicker(nil, "t", items, false, func(s string) { gotStr = append(gotStr, s) })
	p.onPickIndex = func(i int) { got = append(got, i) }
	p.refilter()
	p.sel = 1
	idx, val, ok := p.resolve()
	if !ok {
		t.Fatal("nothing resolved")
	}
	p.deliver(idx, val)
	if len(got) != 1 || got[0] != 1 || len(gotStr) != 0 {
		t.Fatalf("picked %v / %v, want index 1", got, gotStr)
	}
	// string callback still works for callers without onPickIndex
	p2 := newPicker(nil, "t", items, false, func(s string) { gotStr = append(gotStr, s) })
	p2.refilter()
	p2.sel = 2
	idx, val, _ = p2.resolve()
	p2.deliver(idx, val)
	if len(gotStr) != 1 || gotStr[0] != "g" {
		t.Fatalf("string callback: %v", gotStr)
	}
}

func TestFormsRegressDropAndTruncate(t *testing.T) {
	ref := db.ObjectRef{Schema: "main", Name: "t", Type: db.ObjTable}
	for _, id := range []string{"sqlite", "firebird"} {
		d := &db.BaseDialect{ID: id, QuoteL: `"`, QuoteR: `"`}
		stmt, verb := dropStatement(d, ref, true)
		if !strings.HasPrefix(stmt, "DELETE FROM ") || verb != "Delete all rows" || truncateLabel(id) != "Delete all rows…" {
			t.Fatalf("%s: %q %q", id, stmt, verb)
		}
	}
	pg := &db.BaseDialect{ID: "postgres", QuoteL: `"`, QuoteR: `"`, Fold: db.FoldLower, Params: db.ParamDollar}
	if stmt, _ := dropStatement(pg, ref, true); stmt != "TRUNCATE TABLE main.t" {
		t.Fatalf("pg truncate: %q", stmt)
	}
	cql := &db.BaseDialect{ID: "cql", QuoteL: `"`, QuoteR: `"`, Fold: db.FoldLower}
	fn := db.ObjectRef{Schema: "ks", Name: "f", Type: db.ObjFunction, Signature: "(int)"}
	if stmt, _ := dropStatement(cql, fn, false); stmt != "DROP FUNCTION ks.f(int)" {
		t.Fatalf("cql drop function: %q", stmt)
	}
	if stmt, _ := dropStatement(pg, fn, false); stmt != "DROP FUNCTION ks.f(int)" {
		t.Fatalf("pg drop function: %q", stmt)
	}
}

func TestFormsRegressDMLPlaceholdersSequential(t *testing.T) {
	pg := &db.BaseDialect{ID: "postgres", QuoteL: `"`, QuoteR: `"`, Fold: db.FoldLower, Params: db.ParamDollar}
	info := &db.TableInfo{
		Columns: []db.TableColumn{
			{Name: "id", PrimaryKeyNo: 1},
			{Name: "total", Generated: "a+b"},
			{Name: "a"},
			{Name: "b"},
		},
		PrimaryKey: []string{"id"},
	}
	sql := dmlTemplates(pg, db.ObjectRef{Name: "t", Type: db.ObjTable}, info)
	for _, want := range []string{
		"VALUES ($1, $2, $3)",
		"SET a = $1,\n    b = $2\nWHERE id = $3",
		"DELETE FROM t\nWHERE id = $1",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("missing %q in:\n%s", want, sql)
		}
	}
}

func TestFormsRegressWithTerminator(t *testing.T) {
	for in, want := range map[string]string{
		"SELECT 1":                    "SELECT 1;",
		"SELECT 1;":                   "SELECT 1;",
		"BEGIN\n  NULL;\nEND;\n":      "BEGIN\n  NULL;\nEND;",
		"BEGIN NULL; END;\n/":         "BEGIN NULL; END;\n/",
		"CREATE PROCEDURE p AS x\nGO": "CREATE PROCEDURE p AS x\nGO",
	} {
		if got := withTerminator(in); got != want {
			t.Errorf("withTerminator(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormsRegressPrefNumbers(t *testing.T) {
	f, m, n, err := parsePrefNumbers(" 500 ", "1_000_000 ", "\t10")
	if err != "" || f != 500 || m != 1_000_000 || n != 10 {
		t.Fatalf("%d %d %d %q", f, m, n, err)
	}
	if _, _, _, err := parsePrefNumbers("10", "1000", "10"); err == "" {
		t.Fatal("fetch size below range accepted")
	}
	if _, _, _, err := parsePrefNumbers("5000", "2000", "10"); err == "" {
		t.Fatal("fetch size above max rows accepted")
	}
	if _, _, _, err := parsePrefNumbers("500", "abc", "10"); !strings.Contains(err, "Rows kept in memory") {
		t.Fatalf("err = %q", err)
	}
}

func TestFormsRegressTheme(t *testing.T) {
	if ayuMirage.CodeBg == ayuMirage.LineHL {
		t.Fatal("Ayu Mirage: no current-line highlight")
	}
	for _, p := range []Palette{ayuDark, ayuMirage, ayuLight} {
		saved := pal
		pal = p
		if buttonText(pal.Border) != pal.TextDim || buttonText(pal.Accent) != pal.OnAccent {
			pal = saved
			t.Fatal("disabled button text color")
		}
		pal = saved
	}
}

// tipLbl must not swallow pointer events meant for the row under it.
func TestFormsRegressTipLblPassThrough(t *testing.T) {
	a := &App{th: newTheme(), tipTags: map[string]*tipTag{}}
	var r input.Router
	var ops op.Ops
	row := new(int)
	frame := func() {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: layout.Exact(image.Pt(300, 40)), Source: r.Source()}
		// the row's handler is a sibling under the label (as a Clickable
		// laid out before its content's labels)
		area := clip.Rect{Max: image.Pt(300, 40)}.Push(gtx.Ops)
		event.Op(gtx.Ops, row)
		area.Pop()
		gtx.Constraints.Min = image.Point{}
		a.tipLbl(gtx, "k", fontSans, 13, pal.Text, "a label")
		r.Frame(&ops)
	}
	frame()
	for {
		if _, ok := r.Event(pointer.Filter{Target: row, Kinds: pointer.Press}); !ok {
			break
		}
	}
	r.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonSecondary, Position: f32.Pt(5, 5)})
	got := false
	for {
		ev, ok := r.Event(pointer.Filter{Target: row, Kinds: pointer.Press})
		if !ok {
			break
		}
		if pe, ok := ev.(pointer.Event); ok && pe.Kind == pointer.Press {
			got = true
		}
	}
	if !got {
		t.Fatal("right-click on the label did not reach the row")
	}
}
