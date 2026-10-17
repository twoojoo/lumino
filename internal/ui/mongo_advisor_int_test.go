//go:build dev && integration

package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/db/dbtest"
)

// TestUIMongoAdvisor: collection statistics with index advice (hide and
// drop reviewed under the policies) and the slow queries of the profiler
// with the index they suggest.
func TestUIMongoAdvisor(t *testing.T) {
	tg := dbtest.Targets()["mongo"]
	conn := dbtest.Prepare(t, tg)
	cfg := tg.Config
	cfg.ID, cfg.Name, cfg.SavePassword = "db-mongo-advisor", "mongo advisor", true
	h := newHarness(t, []db.Config{cfg}, Options{Connection: cfg.Name})
	a := h.a
	h.waitFor("console", func() bool { return h.console() != nil && a.connState(cfg.ID).status == stReady })
	run := func(stmt string) {
		t.Helper()
		s, err := conn.Session(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if _, _, err := dbtest.Query(context.Background(), s, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	for _, st := range []string{`db.adv_ui.drop()`, `for (let i = 0; i < 300; i++) db.adv_ui.insertOne({a: i % 7, b: i, c: i})`,
		`db.adv_ui.createIndex({a: 1})`, `db.adv_ui.createIndex({a: 1, b: 1})`, `db.adv_ui.createIndex({c: 1})`,
		`db.adv_ui.find({a: 2, b: 9}).hint("a_1_b_1")`} {
		run(st)
	}
	applyReview := func(want string, done func() bool) {
		t.Helper()
		var rev *SQLReview
		h.waitFor("review", func() bool { rev, _ = a.overlay.(*SQLReview); return rev != nil })
		if !strings.Contains(rev.ed.Text(), want) {
			t.Fatalf("review %q misses %q", rev.ed.Text(), want)
		}
		rev.done = true
		a.overlay = nil
		rev.apply()
		h.waitFor("applied "+want, done)
	}

	ref := db.ObjectRef{Schema: tg.Schema, Name: "adv_ui", Type: db.ObjCollection}
	a.openObject(cfg.ID, ref, "stats")
	tt := a.activeTab().(*TableTab)
	h.waitFor("stats", func() bool { h.frame(); return tt.statsV.stats != nil && !tt.statsV.loading || tt.statsV.err != nil })
	if tt.statsV.err != nil || tt.statsV.stats.Documents != 300 || len(tt.statsV.usage) != 4 {
		t.Fatalf("stats %+v %v", tt.statsV.stats, tt.statsV.err)
	}
	issues := map[string]string{}
	row := map[string]int{}
	for i, u := range tt.statsV.usage {
		issues[u.Name], row[u.Name] = u.Issue, i
	}
	if !strings.HasPrefix(issues["a_1"], "prefix of a_1_b_1") || !strings.HasPrefix(issues["c_1"], "never used") || issues["a_1_b_1"] != "" {
		t.Fatalf("advice %v", issues)
	}
	h.frame()
	h.shot("mongo-22-stats")
	tt.statsV.grid.SelectCell(row["c_1"], 0)
	tt.indexAction("hideIndex", "Hide index")
	applyReview(`index: {name: "c_1", hidden: true}`, func() bool {
		h.frame()
		if tt.statsV.loading || tt.statsV.usage == nil {
			return false
		}
		for _, u := range tt.statsV.usage {
			if u.Name == "c_1" && u.Hidden {
				return true
			}
		}
		return false
	})
	for i, u := range tt.statsV.usage {
		if u.Name == "a_1" {
			tt.statsV.grid.SelectCell(i, 0)
		}
	}
	tt.indexAction("dropIndex", "Drop index")
	applyReview(`dropIndex("a_1")`, func() bool { h.frame(); return !tt.statsV.loading && len(tt.statsV.usage) == 3 })
	tt.statsV.grid.SelectCell(0, 0) // _id_
	if _, ok := tt.selectedIndex(); ok {
		t.Fatal("the _id index can be selected for dropping")
	}

	// slow queries: turn the profiler on, record a scan, create its index
	a.openProfiler(cfg.ID, tg.Schema)
	pt := a.activeTab().(*ProfilerTab)
	h.waitFor("profiler", func() bool { return !pt.loading && pt.status != nil })
	pt.levelDialog()
	fd := a.overlay.(*FormDialog)
	fd.fields[0].text.SetText("2")
	fd.fields[1].text.SetText("0")
	fd.run()
	applyReview("profile: 2, slowms: 0", func() bool { return !pt.loading && pt.status != nil && pt.status.Level == 2 })
	defer run(`db.runCommand({profile: 0, slowms: 100})`)
	run(`db.adv_ui.find({b: 77}).sort({c: -1})`)
	pt.refresh()
	h.waitFor("recorded", func() bool { return !pt.loading && len(pt.queries) > 0 })
	idx := -1
	for i, q := range pt.queries {
		if strings.Contains(q.Command, "b: 77") && q.Suggestion != nil {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("slow query not found: %+v", pt.queries)
	}
	pt.grid.SelectCell(idx, 0)
	h.frame()
	if !strings.Contains(pt.detail.Text(), "b: 77") || pt.queries[idx].Suggestion.Keys != "{b: 1, c: -1}" {
		t.Fatalf("detail %q %+v", pt.detail.Text(), pt.queries[idx].Suggestion)
	}
	h.shot("mongo-23-slow-queries")
	pt.createBtn.Click()
	h.frame()
	applyReview("createIndex({b: 1, c: -1})", func() bool { return true })
	h.waitFor("index created", func() bool {
		s, _ := conn.Session(context.Background())
		defer s.Close()
		_, rows, err := dbtest.Query(context.Background(), s, `db.adv_ui.getIndexes()`)
		return err == nil && len(rows) == 4
	})
}
