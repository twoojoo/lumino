//go:build dev && integration

package ui

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/db/dbtest"
)

// TestUIMongoTools drives the management features: schema analysis,
// explain with an index suggestion, index and validation dialogs, the
// pipeline builder, change streams, server activity, users, import,
// export and SQL translation.
func TestUIMongoTools(t *testing.T) {
	tg := dbtest.Targets()["mongo"]
	conn := dbtest.Prepare(t, tg)
	cfg := tg.Config
	cfg.ID, cfg.Name, cfg.SavePassword = "db-mongo-tools", "mongo tools", true
	h := newHarness(t, []db.Config{cfg}, Options{Connection: cfg.Name})
	a := h.a
	h.waitFor("console", func() bool { return h.console() != nil && a.connState(cfg.ID).status == stReady })
	ref := func(n string) db.ObjectRef { return db.ObjectRef{Schema: tg.Schema, Name: n, Type: db.ObjCollection} }
	query := func(stmt string) []db.Row {
		t.Helper()
		s, err := conn.Session(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		_, rows, err := dbtest.Query(context.Background(), s, stmt)
		if err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
		return rows
	}
	count := func(stmt string) string {
		t.Helper()
		return db.FormatValue(query(stmt)[0][0], db.KindInt)
	}
	// applyReview applies the review dialog and waits for the statement
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

	// schema analysis
	a.openObject(cfg.ID, ref("customers"), "schema")
	tt := a.activeTab().(*TableTab)
	h.waitFor("schema", func() bool { return tt.tools.schema != nil || tt.tools.schemaErr != nil })
	if tt.tools.schemaErr != nil || tt.tools.schema.Sampled != 3 || !strings.Contains(jsonSchema(tt.tools.schema), `required: ["_id", "name", "email"]`) {
		t.Fatalf("schema: %v %s", tt.tools.schemaErr, jsonSchema(tt.tools.schema))
	}
	h.frame()
	h.shot("mongo-14-schema")

	// validation from the analysed schema
	tt.validationDialog(jsonSchema(tt.tools.schema))
	fd := a.overlay.(*FormDialog)
	h.frame()
	fd.run()
	applyReview(`collMod: "customers"`, func() bool { return tt.info != nil })
	if _, err := conn.Session(context.Background()); err == nil {
		s, _ := conn.Session(context.Background())
		if _, err := s.Execute(context.Background(), `db.customers.insertOne({_id: 9900})`); err == nil {
			t.Fatal("validator not applied")
		}
		s.Close()
	}
	tt.validationDialog("{}")
	fd = a.overlay.(*FormDialog)
	fd.fields[1].text.SetText("off")
	fd.run()
	applyReview(`validator: {}, validationLevel: "off"`, func() bool { return tt.info != nil })

	// explain → suggested index → created, then dropped from the structure
	a.openObject(cfg.ID, ref("big"), "data")
	big := a.activeTab().(*TableTab)
	h.waitFor("big", func() bool { return big.data != nil && !big.running })
	big.where.SetText(`{label: "row 7"}`)
	big.explain()
	ev := a.overlay.(*ExplainView)
	h.waitFor("explain", func() bool { return ev.rep != nil || ev.err != nil })
	if ev.err != nil || len(ev.rep.Suggestions) != 1 || ev.rep.Suggestions[0].Keys != "{label: 1}" {
		t.Fatalf("explain: %v %+v", ev.err, ev.rep)
	}
	h.frame()
	h.shot("mongo-15-explain")
	h.frame() // the view handles clicks in Layout: apply the suggestion like the button
	ev.done = true
	a.reviewRun(cfg.ID, "Create index", ev.rep.Suggestions[0].Statement, "", big.afterSchemaChange)
	applyReview(`createIndex({label: 1})`, func() bool { return big.info != nil && len(big.info.Indexes) == 2 })
	big.show("structure")
	big.structTab = 1
	h.frame()
	for i, ix := range big.info.Indexes {
		if ix.Name == "label_1" {
			big.structGrids[1].SelectCell(i, 0)
		}
	}
	big.dropSelectedIndex()
	applyReview(`dropIndex("label_1")`, func() bool { return big.info != nil && len(big.info.Indexes) == 1 })
	big.createIndexDialog("{v: -1}")
	fd = a.overlay.(*FormDialog)
	fd.fields[1].code.SetText("{name: \"v_desc\"}")
	fd.run()
	applyReview(`createIndex({v: -1}, {name: "v_desc"})`, func() bool { return big.info != nil && len(big.info.Indexes) == 2 })
	h.frame()
	h.shot("mongo-16-indexes")

	// pipeline builder: the preview follows the selected stage
	a.openPipeline(cfg.ID, ref("customers"), `{age: {$gt: 1}}`)
	pt := a.activeTab().(*PipelineTab)
	dl, _ := a.connState(cfg.ID).docLanguage()
	pt.addStage(dl, "$group", `{_id: null, total: {$sum: "$age"}}`)
	h.waitFor("preview", func() bool { h.frame(); return pt.rs != nil && !pt.running && pt.shown == pt.editVer })
	if pt.err != nil || len(pt.rs.rows) != 1 || db.FormatValue(pt.rs.rows[0][1], db.KindInt) != "90" {
		t.Fatalf("pipeline preview: %v %v", pt.err, pt.rs.rows)
	}
	h.shot("mongo-17-pipeline")
	pt.sel = 0
	pt.editVer++
	h.waitFor("first stage", func() bool {
		h.frame()
		return !pt.running && pt.shown == pt.editVer && pt.rs != nil && len(pt.rs.rows) == 2
	})
	pt.addStage(dl, "$out", `"copy"`)
	if q, note, _ := pt.statement(-1, true); strings.Contains(q, "$out") || note == "" {
		t.Fatalf("preview with $out: %s", q)
	}

	// change stream
	a.openWatch(cfg.ID, ref("watch_ui"))
	wt := a.activeTab().(*WatchTab)
	h.waitFor("watching", func() bool { return wt.open })
	query(`db.watch_ui.insertOne({at: new Date(), n: 1})`)
	h.waitFor("event", func() bool { return len(wt.rs.rows) > 0 })
	if wt.rs.rows[0][1] != "insert" || !wt.rs.hasDocs() {
		t.Fatalf("event %v", wt.rs.rows[0])
	}
	wt.mode = viewTree
	h.frame()
	h.shot("mongo-18-watch")
	wt.stop()

	// server activity
	a.openMonitor(cfg.ID)
	mt := a.activeTab().(*MonitorTab)
	h.waitFor("stats", func() bool { h.frame(); return mt.stats != nil && mt.prev != nil || mt.err != nil })
	if mt.err != nil || len(mt.tiles()) < 8 {
		t.Fatalf("monitor: %v %v", mt.err, mt.tiles())
	}
	h.shot("mongo-19-activity")
	mt.paused = true

	// users and roles (the password is not shown in the review)
	a.openUsers(cfg.ID, tg.Schema)
	ud := a.overlay.(*UsersDialog)
	h.frame()
	ud.createBtn.Click()
	h.frame()
	fd = a.overlay.(*FormDialog)
	fd.fields[0].text.SetText("ui_user")
	fd.fields[1].text.SetText("Very-Secret-1")
	fd.run()
	var rev *SQLReview
	h.waitFor("user review", func() bool { rev, _ = a.overlay.(*SQLReview); return rev != nil })
	if strings.Contains(rev.ed.Text(), "Very-Secret-1") || !strings.Contains(rev.ed.Text(), "createUser") {
		t.Fatalf("password shown: %q", rev.ed.Text())
	}
	applyReview("createUser", func() bool { u, ok := a.overlay.(*UsersDialog); return ok && len(u.users) > 0 })
	ud = a.overlay.(*UsersDialog)
	h.frame()
	h.shot("mongo-20-users")
	ud.grid.SelectCell(0, 0)
	ud.dropBtn.Click()
	h.frame()
	applyReview(`dropUser: "ui_user"`, func() bool { u, ok := a.overlay.(*UsersDialog); return ok && len(u.users) == 0 && u.err == nil })
	a.overlay = nil

	// import a JSON lines file, export the collection as BSON
	path := devDir("ui-import.jsonl")
	os.WriteFile(path, []byte("{\"_id\": 1, \"when\": {\"$date\": \"2024-01-01T00:00:00Z\"}}\n{\"_id\": 2, \"n\": {\"$numberLong\": \"5\"}}\n"), 0o600)
	defer os.Remove(path)
	a.openImport(cfg.ID, ref("import_ui"), nil)
	id := a.overlay.(*ImportDialog)
	id.path.SetText(path)
	id.drop = true
	h.frame()
	id.begin()
	cf := a.overlay.(*Confirm) // dropping asks for the collection name
	if cf.typeName != "import_ui" {
		t.Fatalf("drop confirmation %q", cf.typeName)
	}
	cf.onOK(nil)
	h.waitFor("import", func() bool { return !id.running && id.result != "" })
	if id.failed || count(`db.import_ui.countDocuments({when: {$type: "date"}})`) != "1" || count(`db.import_ui.countDocuments({n: {$type: "long"}})`) != "1" {
		t.Fatalf("import: %s", id.result)
	}
	a.overlay = nil
	a.openObject(cfg.ID, ref("import_ui"), "data")
	it := a.activeTab().(*TableTab)
	h.waitFor("imported data", func() bool { return it.data != nil && !it.running })
	ed := newExportDialog(a, cfg.ID, it.data.rs, &it.ref)
	for i, f := range ed.formats {
		if f[0] == "docs-bson" {
			ed.format = i
		}
	}
	out := devDir("ui-export.bson")
	os.Remove(out)
	defer os.Remove(out)
	ed.path.SetText(out)
	ed.start()
	h.waitFor("export", func() bool { return !ed.running })
	if st, err := os.Stat(out); err != nil || st.Size() < 40 {
		t.Fatalf("export: %v %s", err, ed.result)
	}
	a.openImport(cfg.ID, ref("import_ui2"), nil)
	id = a.overlay.(*ImportDialog)
	id.path.SetText(out)
	h.frame()
	if id.format != 2 {
		t.Fatalf("format from the extension: %d", id.format)
	}
	id.begin()
	h.waitFor("import bson", func() bool { return !id.running && id.result != "" })
	if id.failed || count(`db.import_ui2.countDocuments({n: {$type: "long"}})`) != "1" {
		t.Fatalf("bson round trip: %s", id.result)
	}
	a.overlay = nil

	// SQL translation into the console
	c := h.console()
	if c == nil {
		a.newConsole(cfg.ID, tg.Schema, "")
	}
	a.openTranslate(cfg.ID, "SELECT name FROM customers WHERE age >= 40 ORDER BY name")
	td := a.overlay.(*TranslateDialog)
	h.frame()
	if td.err != "" || td.out.Text() != `db.customers.find({age: {$gte: 40}}, {name: 1, _id: 0}).sort({name: 1})` {
		t.Fatalf("translation %q %s", td.out.Text(), td.err)
	}
	h.shot("mongo-21-translate")
	td.sql.SetText("SELECT * FROM a JOIN b ON a.x = b.y")
	h.frame()
	if td.err == "" {
		t.Fatal("join translated")
	}
}
