package ui

import (
	"strings"
	"testing"

	"gioui.org/io/key"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/db/adapters/mongo"
)

func TestSingleLineEditor(t *testing.T) {
	e := newCodeEditor(nil, "")
	e.SingleLine = true
	submitted := 0
	e.OnSubmit = func() { submitted++ }
	e.onEdit(key.EditEvent{Text: "{a: 1}"})
	press(e, key.NameReturn, 0)
	if submitted != 1 || e.Text() != "{a: 1}" {
		t.Fatalf("enter: %d %q", submitted, e.Text())
	}
	e.onEdit(key.EditEvent{Text: "\n"})
	if submitted != 2 {
		t.Fatal("newline event")
	}
	e.onEdit(key.EditEvent{Text: ", b: {\n  c: 2\r\n}"})
	if strings.ContainsAny(e.Text(), "\r\n") || e.buf.lineCount() != 1 {
		t.Fatalf("paste: %q", e.Text())
	}
}

func testDocs() *resultSet {
	doc := db.DocNode{Type: "object", Kind: db.KindJSON, Children: []db.DocNode{
		{Name: "_id", Type: "int", Kind: db.KindInt, Value: int64(1), Lit: "1"},
		{Name: "a", Type: "object", Kind: db.KindJSON, Children: []db.DocNode{{Name: "b", Type: "string", Kind: db.KindText, Value: "x", Lit: `"x"`}}},
		{Name: "l", Type: "array", Kind: db.KindArray, Children: []db.DocNode{{Name: "0", Type: "int", Kind: db.KindInt, Value: int64(1), Lit: "1"}}},
	}}
	doc2 := db.DocNode{Type: "object", Kind: db.KindJSON, Children: []db.DocNode{{Name: "_id", Type: "int", Kind: db.KindInt, Value: int64(2), Lit: "2"}}}
	return &resultSet{cols: []db.Column{{Name: "_id", Kind: db.KindInt}, {Name: "a.b", Kind: db.KindText}},
		rows: []db.Row{{int64(1), "x"}, {int64(2), nil}}, docs: []db.DocNode{doc, doc2}, sortCol: -1, isQuery: true}
}

func mongoLang() db.Language { return mongo.Language() }

func TestDocViewTree(t *testing.T) {
	rs := testDocs()
	v := newDocView(nil)
	lines := v.lines(rs)
	// documents open, nested fields closed
	var paths []string
	for _, l := range lines {
		paths = append(paths, itoa(l.row)+":"+l.path)
	}
	if strings.Join(paths, ",") != "0:,0:_id,0:a,0:l,1:,1:_id" {
		t.Fatalf("lines %v", paths)
	}
	v.toggle(lines[2]) // open a
	v.toggle(lines[4]) // close document 2
	paths = paths[:0]
	for _, l := range v.lines(rs) {
		paths = append(paths, itoa(l.row)+":"+l.path)
	}
	if strings.Join(paths, ",") != "0:,0:_id,0:a,0:a.b,0:l,1:" {
		t.Fatalf("after toggles %v", paths)
	}
	v.sel = v.lines(rs)[3].key
	if r, p, ok := v.selected(rs, viewTree); !ok || r != 0 || p != "a.b" {
		t.Fatalf("selected %d %q %v", r, p, ok)
	}
	// client-side sort: documents follow their rows
	rs.sortCol, rs.sortDesc = 0, true
	rs.resort()
	if d, _ := rs.doc(0); d.Children[0].Lit != "2" {
		t.Fatalf("sorted doc %+v", d)
	}
	if !rs.hasDocs() {
		t.Fatal("hasDocs")
	}
	rs.addDocs(nil, false)
	if rs.hasDocs() {
		t.Fatal("rows without documents must drop them")
	}
	if summary(lines[0].node) != "{ _id: 1 … 3 field(s) }" || summary(lines[3].node) != "[ 1 item(s) ]" {
		t.Fatalf("summaries %q %q", summary(lines[0].node), summary(lines[3].node))
	}
}

func TestQueryItems(t *testing.T) {
	items := queryItems(mongoLang(), func() []string { return []string{"address.city"} })
	has := map[string]bool{}
	for _, it := range items.req() {
		has[it.label+"/"+it.kind] = true
	}
	if !has["address.city/column"] || !has["$gt/keyword"] || !has["ObjectId/function"] || has["find/function"] {
		t.Fatalf("mongo items %v", has)
	}
	sql := queryItems(db.GenericSQL, nil)
	found := false
	for _, it := range sql.items {
		found = found || it.label == "AND"
	}
	if !found {
		t.Fatal("SQL keywords")
	}
	k, ok := docKey(mongo.Language(), testDocs().docs[0])
	if !ok || k[0].Value != db.Raw("1") {
		t.Fatalf("key %v", k)
	}
	if oneLine("{\n  a: 1\n}") != "{ a: 1 }" {
		t.Fatal("oneLine")
	}
}

func (r *compRequest) req() []compItem { return r.items }

func TestGrowColumns(t *testing.T) {
	rs := &resultSet{cols: []db.Column{{Name: "_id"}}, rows: []db.Row{{int64(1)}}, sortCol: -1}
	rs.growColumns([]db.Column{{Name: "_id"}, {Name: "late"}})
	if len(rs.cols) != 2 || len(rs.rows[0]) != 2 || rs.rows[0][1] != nil {
		t.Fatalf("grow %v %v", rs.cols, rs.rows)
	}
	rs.growColumns([]db.Column{{Name: "_id"}}) // never shrinks
	if len(rs.cols) != 2 {
		t.Fatal("shrunk")
	}
	// a row inserted before the growth gets the new column when set
	td := newTableData(rs)
	td.inserted = append(td.inserted, make(db.Row, 1))
	td.insSet = append(td.insSet, map[int]bool{})
	rs.growColumns([]db.Column{{Name: "_id"}, {Name: "late"}, {Name: "later"}})
	td.set(1, 2, "x")
	if td.Value(1, 2) != "x" || td.Value(0, 2) != nil {
		t.Fatalf("inserted row %v", td.inserted)
	}
	// the grid keeps the widths of the columns it had
	g := newDataGrid(nil)
	g.SetModel(rs)
	g.colW = []int{10, 20}
	g.colsKey = colsKeyOf(&resultSet{cols: rs.cols[:2]})
	g.validate()
	if len(g.colW) != 3 || g.colW[0] != 10 || g.colW[2] != 0 {
		t.Fatalf("widths %v", g.colW)
	}
}

func TestFieldPaths(t *testing.T) {
	p := db.JoinFieldPath(db.JoinFieldPath("", "a"), "b.c")
	if p != `a."b.c"` || db.JoinFieldPath("", "$x") != `"$x"` {
		t.Fatalf("join %q", p)
	}
	if s := db.SplitFieldPath(`a."b.c".d."q\"x"`); len(s) != 4 || s[1] != "b.c" || s[3] != `q"x` {
		t.Fatalf("split %q", s)
	}
	if db.PlainFieldPath(`a."b.c"`) || !db.PlainFieldPath("a.b") {
		t.Fatal("plain")
	}
}
