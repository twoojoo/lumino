//go:build dev && integration

package ui

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/gpu/headless"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/giovanni/lumino/internal/db"
	_ "github.com/giovanni/lumino/internal/db/adapters/cassandra"
	_ "github.com/giovanni/lumino/internal/db/adapters/clickhouse"
	_ "github.com/giovanni/lumino/internal/db/adapters/cockroach"
	_ "github.com/giovanni/lumino/internal/db/adapters/firebird"
	_ "github.com/giovanni/lumino/internal/db/adapters/mssql"
	_ "github.com/giovanni/lumino/internal/db/adapters/mysql"
	_ "github.com/giovanni/lumino/internal/db/adapters/oracle"
	_ "github.com/giovanni/lumino/internal/db/adapters/postgres"
	_ "github.com/giovanni/lumino/internal/db/adapters/sqlite"
	_ "github.com/giovanni/lumino/internal/db/adapters/trino"
	"github.com/giovanni/lumino/internal/db/dbtest"
	"github.com/giovanni/lumino/internal/prefs"
	"github.com/giovanni/lumino/internal/secrets"
)

// harness renders the real App offscreen and lets tests inject input and
// capture PNG screenshots (in .dev/shots).
type harness struct {
	t    *testing.T
	a    *App
	win  *headless.Window
	r    input.Router
	ops  op.Ops
	size image.Point
	dir  string
}

func devDir(p ...string) string {
	d, _ := filepath.Abs(filepath.Join(append([]string{"..", "..", ".dev"}, p...)...))
	return d
}

// newHarness starts the app with the given saved connections.
func newHarness(t *testing.T, conns []db.Config, opts Options) *harness {
	t.Helper()
	cfgDir := devDir("test-config", fmt.Sprintf("%s-%d", t.Name(), time.Now().UnixNano()))
	t.Setenv("LUMINO_CONFIG_DIR", cfgDir)
	t.Cleanup(func() { os.RemoveAll(cfgDir) })
	secrets.UseMock()
	p := prefs.Load()
	for _, c := range conns {
		if c.SavePassword {
			_ = secrets.Set(c.ID, secrets.Password, c.Password, true)
		}
		p.PutConn(c)
	}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	size := image.Pt(1440, 900)
	win, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Skipf("headless gpu unavailable: %v", err)
	}
	h := &harness{t: t, win: win, size: size, dir: devDir("shots")}
	_ = os.MkdirAll(h.dir, 0o755)
	h.a = newApp(opts, nil)
	h.a.winFocused = true
	h.a.start()
	t.Cleanup(func() {
		h.a.shutdown()
		win.Release()
	})
	return h
}

func (h *harness) frame() {
	h.a.drain()
	h.ops.Reset()
	gtx := layout.Context{
		Ops:         &h.ops,
		Now:         time.Now(),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(h.size),
		Source:      h.r.Source(),
	}
	h.a.layout(gtx)
	h.r.Frame(&h.ops)
	if err := h.win.Frame(&h.ops); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) waitFor(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		h.frame()
		if cond() {
			h.frame()
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	h.shot("timeout-" + strings.ReplaceAll(what, " ", "_"))
	h.t.Fatalf("timeout waiting for %s (status: %q)", what, h.a.status)
}

func (h *harness) settle(d time.Duration) {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		h.frame()
		time.Sleep(20 * time.Millisecond)
	}
}

func (h *harness) events(ee ...event.Event) {
	h.frame()
	h.r.Queue(ee...)
	h.frame()
	h.frame()
}

func (h *harness) press(name key.Name, mods ...key.Modifiers) {
	var m key.Modifiers
	for _, x := range mods {
		m |= x
	}
	h.events(key.Event{Name: name, Modifiers: m, State: key.Press}, key.Event{Name: name, Modifiers: m, State: key.Release})
}

func (h *harness) typeText(s string) {
	for _, r := range s {
		h.events(key.EditEvent{Text: string(r)})
	}
}

func (h *harness) click(x, y float32, btn pointer.Buttons) {
	p := f32.Pt(x, y)
	h.events(
		pointer.Event{Kind: pointer.Move, Position: p, Source: pointer.Mouse},
		pointer.Event{Kind: pointer.Press, Position: p, Buttons: btn, Source: pointer.Mouse},
		pointer.Event{Kind: pointer.Release, Position: p, Buttons: btn, Source: pointer.Mouse},
	)
}

func (h *harness) shot(name string) {
	img := image.NewRGBA(image.Rectangle{Max: h.size})
	if err := h.win.Screenshot(img); err != nil {
		h.t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(h.dir, name+".png"))
	if err != nil {
		h.t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		h.t.Fatal(err)
	}
}

// focusEditor gives the keyboard to a console editor.
func (h *harness) focusEditor(c *ConsoleTab) {
	h.frame()
	h.r.Source().Execute(key.FocusCmd{Tag: c.ed})
	h.frame()
}

func (h *harness) console() *ConsoleTab {
	c, _ := h.a.activeTab().(*ConsoleTab)
	return c
}

// sqliteFixture creates a small database file.
func sqliteFixture(t *testing.T, rows int) string {
	t.Helper()
	path := devDir(fmt.Sprintf("ui-%s.sqlite", strings.ReplaceAll(t.Name(), "/", "_")))
	os.Remove(path)
	cfg := db.Config{Driver: "sqlite", File: path, Params: map[string]string{"create": "true"}}
	conn, err := db.Open(context.Background(), &cfg, db.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	script := fmt.Sprintf(`
CREATE TABLE customers (id INTEGER PRIMARY KEY, name TEXT NOT NULL, email TEXT, balance DECIMAL(10,2), joined DATE);
INSERT INTO customers VALUES (1, 'Ada Lovelace', 'ada@example.com', 120.50, '2024-01-15'), (2, 'Linus', NULL, 0, '2023-07-01'),
  (3, 'Grace Hopper', 'grace@example.com', 9999.99, '2022-12-09');
CREATE TABLE orders (id INTEGER PRIMARY KEY, customer_id INTEGER REFERENCES customers(id), total REAL, note TEXT);
CREATE VIEW rich AS SELECT * FROM customers WHERE balance > 100;
CREATE TABLE big (id INTEGER PRIMARY KEY, label TEXT, v REAL, payload TEXT);
WITH RECURSIVE g(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM g WHERE n < %d)
INSERT INTO big SELECT n, 'row ' || n, n * 0.5, printf('%%040d', n) FROM g;
`, rows)
	if err := dbtest.Load(context.Background(), conn, script); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUIConsoleAndTable(t *testing.T) {
	path := sqliteFixture(t, 200000)
	h := newHarness(t, []db.Config{{ID: "lite", Name: "local sqlite", Driver: "sqlite", File: path}}, Options{Connection: "local sqlite"})
	a := h.a
	h.waitFor("console", func() bool { return h.console() != nil && a.connState("lite").status == stReady })
	h.shot("01-console-empty")

	// type a query and run it with Ctrl+Enter
	c := h.console()
	h.focusEditor(c)
	h.typeText("SELECT * FROM customers ORDER BY id;")
	h.press(key.NameReturn, key.ModShortcut)
	h.waitFor("results", func() bool { return len(c.results) == 1 && !c.running })
	rs := c.results[0]
	if rs.err != nil || len(rs.rows) != 3 || len(rs.cols) != 5 {
		t.Fatalf("result: err=%v rows=%d cols=%d", rs.err, len(rs.rows), len(rs.cols))
	}
	h.shot("02-console-results")

	// script with an error: stops at the failing statement and marks it
	c.ed.SetText("SELECT 1;\nSELECT nosuch FROM customers;\nSELECT 2;")
	h.press(key.NameReturn, key.ModShortcut|key.ModShift)
	h.waitFor("script", func() bool { return !c.running && len(c.results) >= 2 })
	if len(c.results) != 2 || c.results[1].err == nil {
		t.Fatalf("script results: %d, err %v", len(c.results), c.results[len(c.results)-1].err)
	}
	if c.ed.ErrLine != 1 {
		t.Errorf("error line %d, want 1", c.ed.ErrLine)
	}
	h.shot("03-console-error")

	// streaming: big result, fetch more on scroll
	c.ed.SetText("SELECT * FROM big")
	h.press(key.NameReturn, key.ModShortcut)
	h.waitFor("big", func() bool { return len(c.results) == 1 && !c.running })
	rs = c.results[0]
	if !rs.more || len(rs.rows) != a.prefs.Fetch() {
		t.Fatalf("first page: %d rows, more=%v", len(rs.rows), rs.more)
	}
	start := time.Now()
	frames := 0
	for time.Since(start) < 2*time.Second {
		c.grid.scrollY += 4000
		h.frame()
		frames++
	}
	t.Logf("grid scroll: %d frames in 2s (%.1f ms/frame), %d rows loaded", frames, 2000/float64(frames), len(rs.rows))
	if len(rs.rows) <= a.prefs.Fetch() {
		t.Fatal("scrolling didn't fetch more rows")
	}
	h.shot("04-console-big")

	// completion
	c.ed.SetText("SELECT * FROM cu")
	h.focusEditor(c)
	h.typeText("s")
	h.waitFor("completion", func() bool { return c.ed.comp != nil || c.cs().completion[metaKey{schema: "main"}] != nil })
	c.ed.SetText("SELECT * FROM cu")
	h.typeText("s")
	h.settle(100 * time.Millisecond)
	if c.ed.comp == nil || len(c.ed.comp.items) == 0 || c.ed.comp.items[0].label != "customers" {
		t.Fatalf("completion: %+v", c.ed.comp)
	}
	h.shot("05-completion")
	h.press(key.NameReturn)
	if got := c.ed.Text(); got != "SELECT * FROM customers" {
		t.Fatalf("after completion %q", got)
	}

	// table tab: open, edit a cell, add a row, save
	a.openObject("lite", db.ObjectRef{Schema: "main", Name: "customers", Type: db.ObjTable}, "data")
	tt := a.activeTab().(*TableTab)
	h.waitFor("table data", func() bool { return tt.data != nil && tt.info != nil })
	h.shot("06-table-data")
	tt.setText(1, 2, "linus@example.com", false) // email of row 2
	tt.addRow()
	r := tt.data.Rows() - 1
	tt.setText(r, 0, "4", false)
	tt.setText(r, 1, "Barbara Liskov", false)
	if tt.data.pending() != 2 {
		t.Fatalf("pending %d", tt.data.pending())
	}
	h.frame()
	h.shot("07-table-pending")
	tt.review()
	rev, ok := a.overlay.(*SQLReview)
	if !ok {
		t.Fatalf("no review dialog: %T", a.overlay)
	}
	h.frame()
	h.shot("08-review")
	if txt := rev.ed.Text(); !strings.Contains(txt, "UPDATE") || !strings.Contains(txt, "INSERT") {
		t.Fatalf("review SQL:\n%s", txt)
	}
	rev.done = true
	rev.apply()
	h.waitFor("saved", func() bool {
		return tt.data != nil && tt.data.pending() == 0 && len(tt.data.rs.rows) == 4 && !tt.running
	})
	if v := tt.data.Value(1, 2); v != "linus@example.com" {
		t.Fatalf("update not saved: %#v", v)
	}
	// structure and DDL views
	tt.show("structure")
	h.waitFor("structure", func() bool { return tt.info != nil })
	h.shot("09-structure")
	tt.show("ddl")
	h.waitFor("ddl", func() bool { return tt.ddlState == 2 })
	if !strings.Contains(tt.ddl.Text(), "CREATE TABLE customers") {
		t.Fatalf("ddl %q", tt.ddl.Text())
	}
	h.shot("10-ddl")

	// light theme
	a.setThemeName("ayu-light")
	tt.show("data")
	h.settle(200 * time.Millisecond)
	h.shot("11-light-table")
	a.tabIdx = 0
	h.settle(200 * time.Millisecond)
	h.shot("12-light-console")
	a.setThemeName("ayu-dark")

	// sidebar tree expanded
	a.sidebar.expand(schemaKey("lite", metaKey{schema: "main"}), true)
	a.sidebar.expand(folderKey("lite", metaKey{schema: "main"}, db.ObjTable), true)
	h.waitFor("tree objects", func() bool { return a.connState("lite").objects[metaKey{schema: "main"}] != nil })
	h.settle(200 * time.Millisecond)
	h.shot("13-tree")

	// connection dialog
	a.openConnDialog("lite")
	h.settle(100 * time.Millisecond)
	h.shot("14-conn-dialog")
	a.overlay = nil
	a.openConnDialog("")
	h.settle(100 * time.Millisecond)
	h.shot("15-new-conn")
	a.overlay = nil

	// palette
	a.openPalette()
	h.typeText("cust")
	h.settle(100 * time.Millisecond)
	h.shot("16-palette")
	a.overlay = nil
}

func TestUIPostgresTransactions(t *testing.T) {
	if !dbtest.Reachable("127.0.0.1:55432") {
		t.Skip("postgres not running (make db-up)")
	}
	cfg := db.Config{ID: "pg", Name: "pg dev", Driver: "postgres", Host: "127.0.0.1", Port: 55432, Database: "lumino", User: "lumino",
		Password: "lumino", SavePassword: true}
	conn, err := db.Open(context.Background(), &cfg, db.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := dbtest.Load(context.Background(), conn, `DROP TABLE IF EXISTS ui_tx; CREATE TABLE ui_tx (id int primary key, v text);`); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	h := newHarness(t, []db.Config{cfg}, Options{Connection: "pg dev"})
	a := h.a
	h.waitFor("console", func() bool { return h.console() != nil && a.connState("pg").status == stReady })
	c := h.console()
	c.setManual(true)
	c.ed.SetText("INSERT INTO ui_tx VALUES (1, 'a')")
	c.run(false)
	h.waitFor("insert", func() bool { return !c.running && len(c.results) == 1 })
	if !c.txActive || c.results[0].err != nil {
		t.Fatalf("manual mode: tx %v err %v", c.txActive, c.results[0].err)
	}
	if c.Dirty() == "" {
		t.Fatal("open transaction should make the tab dirty")
	}
	h.shot("20-pg-tx-open")
	c.commit(true) // rollback
	h.waitFor("rollback", func() bool { return !c.txActive })
	c.ed.SetText("SELECT count(*) FROM ui_tx")
	c.run(false)
	h.waitFor("count", func() bool { return !c.running && len(c.results) == 1 })
	if got := db.FormatValue(c.results[0].rows[0][0], db.KindInt); got != "0" {
		t.Fatalf("rollback left %s rows", got)
	}
	// cancel a long statement with Esc
	c.ed.SetText("SELECT pg_sleep(30)")
	c.run(false)
	h.settle(500 * time.Millisecond)
	if !c.running {
		t.Fatal("statement not running")
	}
	h.shot("21-pg-running")
	h.press(key.NameEscape)
	h.waitFor("cancelled", func() bool { return !c.running })
	if len(c.results) != 1 || c.results[0].err == nil {
		t.Fatalf("cancelled statement should fail: %+v", c.results)
	}
	h.shot("22-pg-cancelled")
	// the cancelled statement aborted the transaction: roll it back, then
	// the session (and its manual transaction mode) still works
	if !c.txActive {
		t.Fatal("manual transaction should still be open after a cancel")
	}
	c.commit(true)
	h.waitFor("rollback after cancel", func() bool { return !c.txActive })
	c.ed.SetText("SELECT 42")
	c.run(false)
	h.waitFor("after cancel", func() bool { return !c.running && len(c.results) == 1 && c.results[0].err == nil })
}

// TestUIAllDatabases drives the main flows on every development database:
// console query, table data, cell edit + save, structure and DDL.
func TestUIAllDatabases(t *testing.T) {
	for _, name := range []string{"postgres", "cockroach", "mysql", "mariadb", "tidb", "sqlite", "sqlserver", "oracle",
		"clickhouse", "firebird", "trino", "scylla", "cassandra"} {
		t.Run(name, func(t *testing.T) {
			tg := dbtest.Targets()[name]
			conn := dbtest.Prepare(t, tg)
			d := conn.Dialect()
			cfg := tg.Config
			cfg.ID, cfg.Name, cfg.SavePassword = "db-"+name, name+" dev", true
			h := newHarness(t, []db.Config{cfg}, Options{Connection: cfg.Name})
			a := h.a
			h.waitFor("console", func() bool { return h.console() != nil && a.connState(cfg.ID).status == stReady })
			c := h.console()
			cust := tg.Ref("customers")
			q := "SELECT * FROM " + d.Qualified(cust)
			if !tg.NoOrderBy {
				q += " ORDER BY " + d.Quote(tg.ID("id"))
			}
			c.ed.SetText(q)
			c.run(false)
			h.waitFor("query", func() bool { return !c.running && len(c.results) == 1 })
			if rs := c.results[0]; rs.err != nil || len(rs.rows) < 3 {
				t.Fatalf("console: err=%v rows=%d", rs.err, len(rs.rows))
			}
			h.shot("all-" + name + "-1-console")

			a.openObject(cfg.ID, cust, "data")
			tt := a.activeTab().(*TableTab)
			h.waitFor("table", func() bool { return tt.data != nil && tt.info != nil || tt.loadErr != nil })
			if tt.loadErr != nil {
				t.Fatalf("table data: %v", tt.loadErr)
			}
			h.shot("all-" + name + "-2-table")
			if why := tt.readOnlyReason(); why == "" {
				// edit the name of the customer with id 1
				row, nameCol := -1, -1
				for i, col := range tt.data.rs.cols {
					if strings.EqualFold(col.Name, "name") {
						nameCol = i
					}
				}
				for r := 0; r < tt.data.Rows(); r++ {
					for i, col := range tt.data.rs.cols {
						if strings.EqualFold(col.Name, "id") && db.FormatValue(tt.data.Value(r, i), col.Kind) == "1" {
							row = r
						}
					}
				}
				if row < 0 || nameCol < 0 {
					t.Fatalf("customer 1 not found")
				}
				tt.setText(row, nameCol, "Ada (edited in "+name+")", false)
				if tt.data.pending() != 1 {
					t.Fatalf("pending %d", tt.data.pending())
				}
				tt.review()
				rev, ok := a.overlay.(*SQLReview)
				if !ok {
					t.Fatalf("no review: %T (status %q)", a.overlay, a.status)
				}
				h.frame()
				h.shot("all-" + name + "-3-review")
				rev.done = true
				rev.apply()
				h.waitFor("saved", func() bool { return tt.data != nil && tt.data.pending() == 0 && !tt.running })
				if tg.Eventual {
					time.Sleep(1500 * time.Millisecond)
				}
				s, err := conn.Session(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				_, rows, err := dbtest.Query(context.Background(), s, "SELECT "+d.Quote(tg.ID("name"))+" FROM "+d.Qualified(cust)+" WHERE "+d.Quote(tg.ID("id"))+" = 1")
				s.Close()
				if err != nil || len(rows) != 1 || rows[0][0] != "Ada (edited in "+name+")" {
					t.Fatalf("edit not saved: %v %v (status %q)", rows, err, a.status)
				}
			} else {
				t.Logf("read-only table: %s", why)
			}
			tt.show("structure")
			h.waitFor("structure", func() bool { return tt.info != nil })
			h.shot("all-" + name + "-4-structure")
			tt.show("ddl")
			h.waitFor("ddl", func() bool { return tt.ddlState == 2 })
			if tt.ddlErr != nil || !strings.Contains(strings.ToLower(tt.ddl.Text()), "customers") {
				t.Fatalf("ddl: %v %q", tt.ddlErr, tt.ddl.Text())
			}
			h.shot("all-" + name + "-5-ddl")
			// navigator: expand down to the tables
			cs := a.connState(cfg.ID)
			k := metaKey{catalog: tg.Catalog, schema: tg.Schema}
			if cs.caps().Catalogs {
				a.sidebar.expand(catKey(cfg.ID, tg.Catalog), true)
			}
			a.sidebar.expand(schemaKey(cfg.ID, k), true)
			a.sidebar.expand(folderKey(cfg.ID, k, db.ObjTable), true)
			h.waitFor("tree", func() bool {
				for _, r := range a.sidebar.rows {
					if r.kind == nObject && strings.EqualFold(r.ref.Name, "customers") {
						return true
					}
				}
				return false
			})
			h.shot("all-" + name + "-6-tree")
		})
	}
}

// TestUIEditorPerformance: a 50k-line script stays fluid.
func TestUIEditorPerformance(t *testing.T) {
	path := sqliteFixture(t, 10)
	h := newHarness(t, []db.Config{{ID: "lite", Name: "perf", Driver: "sqlite", File: path}}, Options{Connection: "perf"})
	h.waitFor("console", func() bool { return h.console() != nil })
	c := h.console()
	var b strings.Builder
	for i := 0; i < 50000; i++ {
		fmt.Fprintf(&b, "SELECT id, name, 'str %d' AS s, %d * 2 FROM customers WHERE id = %d; -- comment\n", i, i, i%3)
	}
	c.ed.SetText(b.String())
	c.ed.SetCaretOffset(0)
	h.focusEditor(c)
	h.frame()
	start := time.Now()
	frames := 0
	for time.Since(start) < 2*time.Second {
		c.ed.scrollY += 9000
		h.frame()
		frames++
	}
	scroll := 2000 / float64(frames)
	// typing at the top re-highlights incrementally
	start = time.Now()
	for i := 0; i < 50; i++ {
		h.events(key.EditEvent{Text: "x"})
	}
	typing := float64(time.Since(start).Milliseconds()) / 50
	t.Logf("50k lines: scroll %.1f ms/frame, typing %.1f ms/keystroke (3 frames each)", scroll, typing)
	if scroll > 50 || typing > 150 {
		t.Fatalf("editor too slow: scroll %.1f ms/frame, typing %.1f ms", scroll, typing)
	}
	// run a statement in the middle of the big script
	c.ed.SetCaretOffset(len(c.ed.Text()) / 2)
	c.run(false)
	h.waitFor("run in big script", func() bool { return !c.running && len(c.results) == 1 && c.results[0].err == nil })
	h.shot("30-big-script")
}
