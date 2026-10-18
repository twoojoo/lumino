//go:build dev && integration

package ui

import (
	"testing"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/db/dbtest"
)

// TestUIMongoCompletion: the console lists the collections at once and
// samples the fields of a collection only when a statement uses it; the
// read preference in the connection form.
func TestUIMongoCompletion(t *testing.T) {
	tg := dbtest.Targets()["mongo"]
	dbtest.Prepare(t, tg)
	cfg := tg.Config
	cfg.ID, cfg.Name, cfg.SavePassword = "db-mongo-completion", "mongo completion", true
	h := newHarness(t, []db.Config{cfg}, Options{Connection: cfg.Name})
	a := h.a
	h.waitFor("console", func() bool { return h.console() != nil && a.connState(cfg.ID).status == stReady })
	c := h.console()
	cs := a.connState(cfg.ID)
	has := func(req *compRequest, label, kind string) bool {
		for _, it := range req.items {
			if it.label == label && it.kind == kind {
				return true
			}
		}
		return false
	}
	c.ed.SetText("db.cus")
	h.waitFor("collections", func() bool { h.frame(); return has(c.complete(c.ed), "customers", "table") })
	k := metaKey{schema: tg.Schema}
	if fields, ok := cs.completion[k]["customers"]; !ok || fields != nil {
		t.Fatalf("fields loaded with the collection list: %v", fields)
	}
	c.ed.SetText("db.customers.find({")
	h.waitFor("fields", func() bool { h.frame(); return has(c.complete(c.ed), "address.city", "column") })
	if cs.completion[k]["orders"] != nil {
		t.Fatal("fields of another collection were sampled")
	}

	// the read preference is a choice of the connection form
	a.openConnDialog(cfg.ID)
	d := a.overlay.(*ConnDialog)
	d.choiceSel["readPreference"] = 3
	h.frame()
	h.shot("mongo-27-read-preference")
	if c, err := d.config(); err != nil || c.Param("readPreference") != "secondaryPreferred" {
		t.Fatalf("form: %v %v", c.Params, err)
	}
	a.overlay = nil
}
