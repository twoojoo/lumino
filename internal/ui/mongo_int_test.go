//go:build dev && integration

package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/db/dbtest"
)

// TestUIMongo drives the main flows on MongoDB: shell statements in the
// console (policies included), collection data with a filter, a cell
// edit saved by _id, structure, DDL and the navigator.
func TestUIMongo(t *testing.T) {
	tg := dbtest.Targets()["mongo"]
	conn := dbtest.Prepare(t, tg)
	cfg := tg.Config
	cfg.ID, cfg.Name, cfg.SavePassword = "db-mongo", "mongo dev", true
	h := newHarness(t, []db.Config{cfg}, Options{Connection: cfg.Name})
	a := h.a
	h.waitFor("console", func() bool { return h.console() != nil && a.connState(cfg.ID).status == stReady })
	c := h.console()
	if c.ed.lang == nil || c.ed.lang.Name() != "mongo" || c.ed.lang.LineComment() != "//" {
		t.Fatalf("console language: %v", c.ed.lang)
	}
	c.ed.SetText("// customers by name\ndb.customers.find({})\n  .sort({name: 1})\n\ndb.orders.countDocuments({})")
	c.ed.SetCaretOffset(5)
	c.run(true)
	h.waitFor("query", func() bool { return !c.running && len(c.results) == 2 })
	if rs := c.results[0]; rs.err != nil || len(rs.rows) != 3 || rs.cols[0].Name != "_id" {
		t.Fatalf("console: err=%v rows=%d", rs.err, len(rs.rows))
	}
	if rs := c.results[1]; rs.err != nil || len(rs.rows) != 1 || db.FormatValue(rs.rows[0][0], db.KindInt) != "2" {
		t.Fatalf("count: %v %v", rs.err, rs.rows)
	}
	h.shot("mongo-1-console")

	// a destructive statement on a protected connection needs the typed name
	prot := true
	stored, _ := a.prefs.Conn(cfg.ID)
	stored.Protected = &prot
	c.ed.SetText("db.capped_log.deleteMany({})")
	c.run(false)
	cf, ok := a.overlay.(*Confirm)
	if !ok || cf.typeName == "" {
		t.Fatalf("no typed confirmation: %T", a.overlay)
	}
	a.overlay = nil
	stored.Protected = nil

	cust := db.ObjectRef{Schema: tg.Schema, Name: "customers", Type: db.ObjCollection}
	a.openObject(cfg.ID, cust, "data")
	tt := a.activeTab().(*TableTab)
	h.waitFor("table", func() bool { return tt.data != nil && tt.info != nil || tt.loadErr != nil })
	if tt.loadErr != nil {
		t.Fatalf("collection data: %v", tt.loadErr)
	}
	if why := tt.readOnlyReason(); why != "" {
		t.Fatalf("collection read-only: %s", why)
	}
	h.shot("mongo-2-collection")
	row, nameCol := -1, -1
	for i, col := range tt.data.rs.cols {
		if col.Name == "name" {
			nameCol = i
		}
	}
	for r := 0; r < tt.data.Rows(); r++ {
		if tt.data.Value(r, 0) == int64(1) {
			row = r
		}
	}
	if row < 0 || nameCol < 0 {
		t.Fatal("customer 1 not found")
	}
	tt.setText(row, nameCol, "Ada (edited in mongo)", false)
	tt.review()
	rev, ok := a.overlay.(*SQLReview)
	if !ok || !strings.Contains(rev.ed.Text(), `db.customers.updateOne({_id: 1}, {$set: {name: "Ada (edited in mongo)"}});`) {
		t.Fatalf("review: %T %q", a.overlay, a.status)
	}
	h.frame()
	h.shot("mongo-3-review")
	rev.done = true
	rev.apply()
	h.waitFor("saved", func() bool { return tt.data != nil && tt.data.pending() == 0 && !tt.running })
	s, err := conn.Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, rows, err := dbtest.Query(context.Background(), s, `db.customers.find({_id: 1}, {name: 1, _id: 0})`)
	s.Close()
	if err != nil || len(rows) != 1 || rows[0][0] != "Ada (edited in mongo)" {
		t.Fatalf("edit not saved: %v %v (status %q)", rows, err, a.status)
	}

	// filter with a query document, then sort by a field
	tt.refresh(func() { tt.where.SetText(`{age: {$gt: 40}}`) })
	h.waitFor("filtered", func() bool { return tt.data != nil && !tt.running })
	if tt.loadErr != nil || tt.data.Rows() != 1 {
		t.Fatalf("filter: %v %d rows", tt.loadErr, tt.data.Rows())
	}
	tt.refresh(func() { tt.where.SetText("age > 40") })
	h.waitFor("bad filter", func() bool { return !tt.running })
	if tt.loadErr == nil {
		t.Fatal("SQL filter accepted")
	}

	count := func(filter string) string {
		t.Helper()
		s, err := conn.Session(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		_, rows, err := dbtest.Query(context.Background(), s, "db.customers.countDocuments("+filter+")")
		if err != nil || len(rows) != 1 {
			t.Fatalf("count %s: %v", filter, err)
		}
		return db.FormatValue(rows[0][0], db.KindInt)
	}
	loaded := func(what string) {
		t.Helper()
		h.waitFor(what, func() bool { return tt.data != nil && !tt.running && !tt.saving || tt.loadErr != nil })
		if tt.loadErr != nil {
			t.Fatalf("%s: %v", what, tt.loadErr)
		}
	}
	applyReview := func(want string) {
		t.Helper()
		rev, ok := a.overlay.(*SQLReview)
		if !ok {
			t.Fatalf("no review for %q: %T (status %q)", want, a.overlay, a.status)
		}
		if !strings.Contains(rev.ed.Text(), want) {
			t.Fatalf("review %q misses %q", rev.ed.Text(), want)
		}
		rev.done = true
		rev.apply()
		h.waitFor("saved", func() bool { return !tt.saving && !tt.running && tt.data != nil })
		a.overlay = nil
	}
	rowOf := func(id int64) int {
		t.Helper()
		for r := 0; r < tt.data.Rows(); r++ {
			if tt.data.Value(r, 0) == id {
				return r
			}
		}
		t.Fatalf("document %d not loaded", id)
		return -1
	}

	// query bar: projection, sort document and limit; nested documents are
	// dotted columns
	tt.refresh(func() {
		tt.where.SetText("")
		tt.proj.SetText("{name: 1, address: 1}")
		tt.sortEd.SetText("{age: -1}")
		tt.limit.SetText("2")
	})
	loaded("query bar")
	var names []string
	for _, c := range tt.data.rs.cols {
		names = append(names, c.Name)
	}
	if tt.data.Rows() != 2 || tt.data.Value(0, 0) != int64(2) || strings.Join(names, ",") != "_id,name,address.city,address.zip" {
		t.Fatalf("query bar: %d rows, first %v, columns %v (%s)", tt.data.Rows(), tt.data.Value(0, 0), names, tt.query)
	}
	h.frame()
	h.shot("mongo-7-querybar")
	tt.refresh(func() { tt.proj.SetText(""); tt.sortEd.SetText(""); tt.limit.SetText("") })
	loaded("all documents")

	// a nested field edited in the grid is set by path
	city := -1
	for i, c := range tt.data.rs.cols {
		if c.Name == "address.city" {
			city = i
		}
	}
	tt.setText(rowOf(1), city, "Paris", false)
	tt.review()
	applyReview(`$set: {"address.city": "Paris"}`)
	if v := count(`{_id: 1, "address.city": "Paris", "address.zip": "N1"}`); v != "1" {
		t.Fatalf("path edit: %s", v)
	}

	// tree view: edit a value and remove a field by path
	tt.mode = viewTree
	h.frame()
	h.shot("mongo-8-tree")
	line := func(id int64, path string) docLine {
		t.Helper()
		r := rowOf(id)
		for _, l := range tt.docs.lines(tt.data.rs) {
			if l.row == r && l.path == path {
				return l
			}
		}
		t.Fatalf("no tree line %d %s", id, path)
		return docLine{}
	}
	action := func(l docLine, label string) {
		t.Helper()
		for _, it := range tt.docMenu(l.row, l.path, l.node) {
			if it.label == label {
				it.action()
				return
			}
		}
		t.Fatalf("no %q action for %s", label, l.path)
	}
	tt.docs.expanded[line(1, "address").key] = true
	action(line(1, "address.zip"), "Edit value…")
	de, ok := a.overlay.(*DocEditor)
	if !ok || de.ed.Text() != `"N1"` {
		t.Fatalf("value editor: %T", a.overlay)
	}
	de.ed.SetText(`"E1"`)
	de.submit()
	applyReview(`$set: {"address.zip": "E1"}`)
	action(line(1, "tags"), "Remove field")
	applyReview(`$unset: {tags: ""}`)
	if v := count(`{_id: 1, "address.zip": "E1", tags: {$exists: false}}`); v != "1" {
		t.Fatalf("tree edits: %s", v)
	}

	// JSON view and the document editor
	tt.mode = viewJSON
	h.frame()
	if !strings.Contains(tt.docs.json.Text(), "city: \"Paris\"") {
		t.Fatalf("json view: %q", tt.docs.json.Text())
	}
	h.shot("mongo-9-json")
	tt.editDocument(rowOf(2))
	de, ok = a.overlay.(*DocEditor)
	if !ok || !strings.Contains(de.ed.Text(), `name: "Linus"`) {
		t.Fatalf("document editor: %T", a.overlay)
	}
	h.frame()
	h.shot("mongo-10-doc-editor")
	de.ed.SetText(strings.Replace(strings.Replace(de.ed.Text(), `"Linus"`, `"Linus T."`, 1), "age: 54", `age: 55, langs: ["C"]`, 1))
	de.submit()
	applyReview(`$set: {name: "Linus T.", age: 55, langs: ["C"]}`)
	if v := count(`{_id: 2, name: "Linus T.", age: 55, langs: "C"}`); v != "1" {
		t.Fatalf("document edit: %s", v)
	}
	// a changed _id is refused
	tt.editDocument(rowOf(2))
	de = a.overlay.(*DocEditor)
	de.ed.SetText(strings.Replace(de.ed.Text(), "_id: 2", "_id: 20", 1))
	de.submit()
	if de.done || !strings.Contains(de.err, "_id") {
		t.Fatalf("_id change: %q", de.err)
	}
	a.overlay = nil

	// insert a document, then delete it from the tree
	tt.mode = viewTree
	tt.insertDocument()
	de = a.overlay.(*DocEditor)
	de.ed.SetText(`{_id: 77, name: "new", email: "new@example.com", when: ISODate("2026-01-01T00:00:00Z")}`)
	de.submit()
	applyReview(`insertOne({_id: 77, name: "new"`)
	if v := count(`{_id: 77, when: {$type: "date"}}`); v != "1" {
		t.Fatalf("insert: %s", v)
	}
	action(line(77, ""), "Delete document")
	applyReview(`deleteOne({_id: 77})`)
	if v := count(`{_id: 77}`); v != "0" {
		t.Fatalf("delete: %s", v)
	}
	tt.mode = viewTable

	// console results as a tree
	a.tabIdx = a.findTab(func(x Tab) bool { return x == c })
	c.ed.SetText("db.customers.find({}).sort({_id: 1})")
	c.run(false)
	h.waitFor("console docs", func() bool { return !c.running && len(c.results) == 1 && c.results[0].hasDocs() })
	c.resMode = viewTree
	h.frame()
	h.shot("mongo-11-console-tree")
	c.resMode = viewJSON
	h.frame()
	if !strings.Contains(c.docs.json.Text(), `"Linus T."`) {
		t.Fatalf("console json: %q", c.docs.json.Text())
	}
	a.tabIdx = a.findTab(func(x Tab) bool { return x == tt })

	// navigator labels of the language
	if open, tmpl := conn.Language().MenuLabels(); !strings.HasPrefix(open, "Find") || !strings.Contains(tmpl, "insert") {
		t.Fatalf("labels %q %q", open, tmpl)
	}

	tt.show("structure")
	h.waitFor("structure", func() bool { return tt.info != nil })
	h.shot("mongo-4-structure")
	tt.show("ddl")
	h.waitFor("ddl", func() bool { return tt.ddlState == 2 })
	if tt.ddlErr != nil || !strings.Contains(tt.ddl.Text(), "createIndex") {
		t.Fatalf("ddl: %v %q", tt.ddlErr, tt.ddl.Text())
	}
	h.shot("mongo-5-ddl")

	k := metaKey{schema: tg.Schema}
	a.sidebar.expand(schemaKey(cfg.ID, k), true)
	a.sidebar.expand(folderKey(cfg.ID, k, db.ObjCollection), true)
	h.waitFor("tree", func() bool {
		for _, r := range a.sidebar.rows {
			if r.kind == nObject && r.ref.Name == "customers" && r.ref.Type == db.ObjCollection {
				return true
			}
		}
		return false
	})
	h.shot("mongo-6-tree")
}
