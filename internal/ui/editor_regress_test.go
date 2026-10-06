package ui

import (
	"strings"
	"testing"

	"gioui.org/io/key"
	"gioui.org/layout"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/db/sqlx"
)

// regressEditor is an editor with a fixed completer (no App needed for
// the code paths exercised here).
func regressEditor(txt string) *CodeEditor {
	e := newCodeEditor(nil, txt)
	e.SetCaretOffset(len(txt))
	e.Completer = func(*CodeEditor) *compRequest {
		return &compRequest{items: []compItem{
			{label: "int8", kind: "type"},
			{label: "users", kind: "table"},
			{label: "name", kind: "column"},
			{label: "user_id", kind: "column"},
			{label: "select", kind: "keyword"},
			{label: "éte", kind: "column"},
		}}
	}
	return e
}

func press(e *CodeEditor, name key.Name, mods key.Modifiers) {
	e.onKey(layout.Context{}, key.Event{Name: name, Modifiers: mods, State: key.Press})
}

func TestEditorRegressReturnKey(t *testing.T) {
	e := regressEditor("SELECT (")
	var cmds []string
	e.OnCommand = func(c string) bool { cmds = append(cmds, c); return true }
	press(e, key.NameReturn, 0)
	if got := e.Text(); got != "SELECT (\n    " {
		t.Fatalf("plain Return: %q", got)
	}
	press(e, key.NameEnter, key.ModShift)
	if got := e.Text(); got != "SELECT (\n    \n    " {
		t.Fatalf("Shift+Enter: %q", got)
	}
	before := e.Text()
	press(e, key.NameReturn, key.ModShortcut)
	press(e, key.NameReturn, key.ModShortcut|key.ModShift)
	if e.Text() != before || strings.Join(cmds, ",") != "run,runAll" {
		t.Fatalf("shortcut Return: text %q cmds %v", e.Text(), cmds)
	}
	// an open popup: Return accepts the completion, no newline
	e = regressEditor("SELECT * FROM us")
	e.triggerCompletion(false)
	if e.comp == nil {
		t.Fatal("no popup")
	}
	want := "SELECT * FROM " + e.comp.items[e.comp.sel].label
	press(e, key.NameReturn, 0)
	if got := e.Text(); got != want {
		t.Fatalf("accept: %q", got)
	}
}

func TestEditorRegressReadOnly(t *testing.T) {
	e := regressEditor("one\ntwo")
	e.ReadOnly = true
	e.SetCaretOffset(2)
	caret := e.caret
	cmds := []struct {
		name key.Name
		mods key.Modifiers
	}{
		{"D", key.ModShortcut}, {"K", key.ModShortcut | key.ModShift}, {"/", key.ModShortcut},
		{key.NameTab, key.ModShift}, {key.NameUpArrow, key.ModAlt}, {key.NameDownArrow, key.ModAlt},
		{key.NameReturn, 0}, {"Z", key.ModShortcut}, {"Y", key.ModShortcut}, {"D", key.ModShortcut},
	}
	for _, c := range cmds {
		press(e, c.name, c.mods)
		if e.Text() != "one\ntwo" || e.caret != caret || e.anchor != caret {
			t.Fatalf("%s: text %q caret %v anchor %v", c.name, e.Text(), e.caret, e.anchor)
		}
	}
	e.SelectRange(0, 3)
	press(e, "U", key.ModShortcut|key.ModShift)
	e.Insert("x")
	e.find.query.SetText("o")
	e.find.open = true
	e.find.replaceAll() // would flash "Replaced" through the (nil) app
	e.find.replaceOne()
	if e.Text() != "one\ntwo" {
		t.Fatalf("read-only edited: %q", e.Text())
	}
}

func TestEditorRegressNumbersDontComplete(t *testing.T) {
	items := []compItem{{label: "int8", kind: "type"}, {label: "user_id", kind: "column"}, {label: "users", kind: "table"}}
	if got := filterItemsMode(items, "8", false); len(got) != 0 {
		t.Fatalf("prefix mode matched %+v", got)
	}
	if got := filterItemsMode(items, "id", false); len(got) != 1 || got[0].label != "user_id" {
		t.Fatalf("word start: %+v", got)
	}
	if got := filterItemsMode(items, "usr", false); len(got) != 0 {
		t.Fatalf("subsequence in prefix mode: %+v", got)
	}
	if got := filterItemsMode(items, "usr", true); len(got) != 2 {
		t.Fatalf("fuzzy: %+v", got)
	}
	e := regressEditor("SELECT * FROM t LIMIT ")
	e.onEdit(key.EditEvent{Text: "8"})
	if e.comp != nil {
		t.Fatalf("popup on a number: %+v", e.comp.items)
	}
	press(e, key.NameReturn, 0)
	if got := e.Text(); got != "SELECT * FROM t LIMIT 8\n" {
		t.Fatalf("LIMIT 8 + Enter: %q", got)
	}
	// typing keeps prefix mode, Ctrl+Space is fuzzy
	e = regressEditor("SELECT ")
	e.onEdit(key.EditEvent{Text: "u"})
	e.onEdit(key.EditEvent{Text: "s"})
	e.onEdit(key.EditEvent{Text: "r"})
	if e.comp != nil {
		t.Fatalf("auto popup matched fuzzily: %+v", e.comp.items)
	}
	press(e, key.NameSpace, key.ModCtrl)
	if e.comp == nil || len(e.comp.items) == 0 {
		t.Fatal("explicit popup should match fuzzily")
	}
}

func TestEditorRegressNoCompletionInCommentsAndStrings(t *testing.T) {
	for _, txt := range []string{"SELECT 1 -- se", "SELECT 'se", "/* multi\nline se", "SELECT 1."} {
		e := regressEditor(txt)
		e.triggerCompletion(false)
		if e.comp != nil {
			t.Errorf("%q: popup opened", txt)
		}
	}
	for _, txt := range []string{`SELECT "na`, "SELECT na", "SELECT u."} {
		e := regressEditor(txt)
		e.triggerCompletion(false)
		if e.comp == nil {
			t.Errorf("%q: no popup", txt)
		}
	}
}

func TestEditorRegressCommentIndentKeepCaret(t *testing.T) {
	e := regressEditor("  select 1")
	e.SetCaretOffset(5) // "  sel|ect"
	press(e, "/", key.ModShortcut)
	if e.Text() != "  -- select 1" || e.caret != e.anchor || e.caret != (tpos{0, 8}) {
		t.Fatalf("comment: %q caret %v anchor %v", e.Text(), e.caret, e.anchor)
	}
	press(e, "/", key.ModShortcut)
	if e.Text() != "  select 1" || e.caret != e.anchor || e.caret != (tpos{0, 5}) {
		t.Fatalf("uncomment: %q caret %v anchor %v", e.Text(), e.caret, e.anchor)
	}
	e = regressEditor("        x")
	e.SetCaretOffset(9)
	press(e, key.NameTab, key.ModShift)
	if e.Text() != "    x" || e.caret != e.anchor || e.caret != (tpos{0, 5}) {
		t.Fatalf("unindent: %q caret %v anchor %v", e.Text(), e.caret, e.anchor)
	}
	// with a selection the lines stay selected
	e = regressEditor("a\nb")
	e.SelectRange(0, 3)
	press(e, "/", key.ModShortcut)
	if e.Text() != "-- a\n-- b" || e.anchor != (tpos{0, 0}) || e.caret != (tpos{1, 4}) {
		t.Fatalf("selection: %q caret %v anchor %v", e.Text(), e.caret, e.anchor)
	}
}

func TestEditorRegressQuotedQualifier(t *testing.T) {
	o := &sqlx.Options{Brackets: true}
	cases := []struct{ sql, qual, prefix string }{
		{`SELECT u."na`, "u", "na"},
		{`SELECT * FROM "public"."us`, "public", "us"},
		{`SELECT * FROM [dbo].[us`, "dbo", "us"},
		{`SELECT * FROM "us`, "", "us"},
	}
	for _, c := range cases {
		ctx := analyzeSQL(c.sql, len(c.sql), o, db.CommonKeywords)
		if ctx.qualifier != c.qual || ctx.prefix != c.prefix {
			t.Errorf("%q: qual %q prefix %q", c.sql, ctx.qualifier, ctx.prefix)
		}
	}
	e := regressEditor(`SELECT u."na`)
	e.triggerCompletion(false)
	if e.comp == nil {
		t.Fatal("no popup")
	}
	e.acceptCompletion()
	if got := e.Text(); got != `SELECT u."name"` {
		t.Fatalf("accept: %q", got)
	}
	// a closing quote before the word is not an opening one
	if openQuoteBefore(`"My Table"x`, 10) || !openQuoteBefore(`u."x`, 3) || !openQuoteBefore(`[x`, 1) {
		t.Fatal("openQuoteBefore")
	}
}

func TestEditorRegressIMEOneUndoStep(t *testing.T) {
	e := regressEditor("ab")
	e.SetCaretOffset(1)
	e.imeLine = 0
	// preedit updates: "k", "ka", "kan", commit "漢"
	e.onEdit(key.EditEvent{Range: key.Range{Start: 1, End: 1}, Text: "k"})
	e.onEdit(key.EditEvent{Range: key.Range{Start: 1, End: 2}, Text: "ka"})
	e.onEdit(key.EditEvent{Range: key.Range{Start: 1, End: 3}, Text: "kan"})
	e.onEdit(key.EditEvent{Range: key.Range{Start: 1, End: 4}, Text: "漢"})
	if e.Text() != "a漢b" {
		t.Fatalf("composed: %q", e.Text())
	}
	press(e, "Z", key.ModShortcut)
	if e.Text() != "ab" {
		t.Fatalf("one undo should drop the whole composition: %q", e.Text())
	}
}

func TestEditorRegressNonASCIITriggersCompletion(t *testing.T) {
	e := regressEditor("SELECT ")
	e.onEdit(key.EditEvent{Text: "é"})
	if e.comp == nil {
		t.Fatal("non-ASCII letter did not open the popup")
	}
	e.onEdit(key.EditEvent{Text: "t"})
	if e.comp == nil {
		t.Fatal("popup closed")
	}
}

func TestEditorRegressStaleAccept(t *testing.T) {
	e := regressEditor("")
	e.Insert("SELECT ")
	e.Insert("us")
	e.triggerCompletion(true)
	if e.comp == nil {
		t.Fatal("no popup")
	}
	press(e, "Z", key.ModShortcut)
	press(e, "Z", key.ModShortcut)
	if e.Text() != "" || e.comp == nil || !e.comp.stale {
		t.Fatalf("setup: %q %+v", e.Text(), e.comp)
	}
	press(e, key.NameReturn, 0) // must not panic on the stale start
	if e.comp != nil || e.Text() != "\n" {
		t.Fatalf("after stale Return: %q comp %v", e.Text(), e.comp != nil)
	}
}

func TestEditorRegressInsertNormalizesNewlines(t *testing.T) {
	e := regressEditor("")
	e.Insert("a\r\nb\rc")
	if e.Text() != "a\nb\nc" || e.buf.lineCount() != 3 {
		t.Fatalf("%q", e.Text())
	}
}

// fakeModel is a minimal grid model.
type fakeModel struct {
	rows [][]any
}

func (m *fakeModel) Columns() []db.Column      { return []db.Column{{Name: "v", Kind: db.KindText}} }
func (m *fakeModel) Rows() int                 { return len(m.rows) }
func (m *fakeModel) Value(r, c int) any        { return m.rows[r][c] }
func (m *fakeModel) RowState(int) rowState     { return rowNormal }
func (m *fakeModel) CellChanged(int, int) bool { return false }

func TestEditorRegressValuePanelDropsStaleEdit(t *testing.T) {
	g := &DataGrid{}
	m := &fakeModel{rows: [][]any{{"alice"}, {"bob"}}}
	g.model = m
	get := func(r, c int) (any, db.Column, bool) {
		if r >= 0 && r < g.model.Rows() && c == 0 {
			return g.model.Value(r, c), g.model.Columns()[0], true
		}
		return nil, db.Column{}, false
	}
	v := newValuePanel(nil)
	v.r, v.c, v.shown, v.model, v.edited = 0, 0, "alice", modelID(g), true
	if v.dropEdit(modelID(g), g, get) {
		t.Fatal("unchanged cell dropped")
	}
	// reload: a new model object
	g.model = &fakeModel{rows: [][]any{{"alice"}, {"bob"}}}
	if !v.dropEdit(modelID(g), g, get) {
		t.Fatal("new model kept the edit")
	}
	// in-place reorder: same model, another record at the row
	g.model = m
	m.rows[0], m.rows[1] = m.rows[1], m.rows[0]
	if !v.dropEdit(modelID(g), g, get) {
		t.Fatal("reordered rows kept the edit")
	}
	// rows shrank below the edited row
	v.r, v.shown = 1, "alice"
	m.rows = m.rows[:1]
	if !v.dropEdit(modelID(g), g, get) {
		t.Fatal("missing row kept the edit")
	}
}

func TestEditorRegressValuePanelRenderCap(t *testing.T) {
	big := strings.Repeat("x", maxPanelText+10)
	s, trunc := renderValueCapped(big, db.Column{Kind: db.KindText}, 0)
	if !trunc || len(s) > maxPanelText+200 || !strings.Contains(s, "truncated") {
		t.Fatalf("text cap: trunc %v len %d", trunc, len(s))
	}
	blob := make([]byte, maxPanelText)
	s, trunc = renderValueCapped(blob, db.Column{Kind: db.KindBytes}, 0)
	if !trunc || len(s) > maxPanelText+200 {
		t.Fatalf("bytes cap: trunc %v len %d", trunc, len(s))
	}
	if _, trunc = renderValueCapped(blob, db.Column{Kind: db.KindBytes}, 2); !trunc {
		t.Fatal("hex cap")
	}
	if s, trunc = renderValueCapped(`{"a":1}`, db.Column{Kind: db.KindText}, 1); trunc || s != "{\n  \"a\": 1\n}" {
		t.Fatalf("json: %q", s)
	}
	// cached by cell, mode, model and value identity
	v := newValuePanel(nil)
	val := strings.Repeat("y", 100)
	a, _ := v.render(0, 0, val, db.Column{Kind: db.KindText}, 1)
	if !v.cache.ok || v.cache.text != a {
		t.Fatal("not cached")
	}
	v.cache.text = "sentinel"
	if b, _ := v.render(0, 0, val, db.Column{Kind: db.KindText}, 1); b != "sentinel" {
		t.Fatal("cache not used")
	}
	if b, _ := v.render(0, 0, val, db.Column{Kind: db.KindText}, 2); b != val {
		t.Fatal("other model must re-render")
	}
}
