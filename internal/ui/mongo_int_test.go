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
