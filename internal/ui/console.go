package ui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"strings"
	"time"
	"unicode/utf8"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/db/sqlx"
	"github.com/twoojoo/lumino/internal/history"
	"github.com/twoojoo/lumino/internal/notify"
	"github.com/twoojoo/lumino/internal/prefs"
)

// ConsoleTab is a SQL console: an editor bound to a dedicated session,
// with streamed results.
type ConsoleTab struct {
	a      *App
	connID string
	schema string
	ed     *CodeEditor
	title  string

	sess     db.Session
	sessGen  int
	opening  bool
	sessWait []sessWaiter // callers waiting for the session being opened
	disposed bool
	manual   bool // manual transactions
	txActive bool

	running   bool
	runGen    int
	runStart  time.Time
	runStmt   string
	cancelRun context.CancelFunc

	results  []*resultSet
	resIdx   int // len(results) = messages
	messages []consoleMsg
	grid     *DataGrid
	value    *valuePanel
	msgList  widget.List

	split    float32 // editor share of the height
	divTag   int
	dragging bool
	grabY    float32
	curH     int
	divTop   int // y of the divider in the last frame

	stmtVer  int
	stmts    []sqlx.Statement
	stmtText string
	lang     db.Language // the connection's (nil until connected)

	runBtn, scriptBtn, stopBtn, commitBtn, rollbackBtn, schemaBtn widget.Clickable
	histBtn, saveBtn, aiBtn, fetchAllBtn, exportBtn, valueBtn     widget.Clickable
	modeBtns                                                      [2]widget.Clickable
	resTabs                                                       []widget.Clickable
	msgTab, gotoErrBtn                                            widget.Clickable
}

type consoleMsg struct {
	at    time.Time
	text  string
	isErr bool
}

func newConsoleTab(a *App, st prefs.TabState) *ConsoleTab {
	t := &ConsoleTab{a: a, connID: st.ConnID, schema: st.Schema, split: 0.45, manual: st.Manual}
	t.ed = newCodeEditor(a, st.Text)
	t.ed.OnCommand = t.command
	t.ed.Completer = t.complete
	t.ed.OnChange = func() { a.scheduleSave() }
	t.ed.OnContextMenu = t.editorMenu
	t.grid = newDataGrid(a)
	t.grid.OnSort = func(c int) {
		if rs := t.current(); rs != nil {
			rs.sortBy(c)
			t.grid.SortCol, t.grid.SortDesc = rs.sortCol, rs.sortDesc
			if rs.more {
				a.flash("Sorted the "+itoa(len(rs.rows))+" fetched rows (fetch all to sort everything)", false)
			}
		}
	}
	t.grid.OnNearEnd = func() {
		if rs := t.current(); rs != nil && rs.more && !rs.fetching {
			rs.fetchMore(a, a.prefs.Fetch(), false, nil)
		}
	}
	t.grid.OnContextMenu = t.gridMenu
	t.grid.OnActivate = func(r, c int) { t.value.open = true }
	t.value = newValuePanel(a)
	t.msgList.Axis = layout.Vertical
	if c, ok := a.prefs.Conn(st.ConnID); ok && prefsProtected(c) && st.Kind == "console" && st.Text == "" {
		t.manual = true // protected connections start with manual transactions
	}
	n := 1
	for _, x := range a.tabs {
		if c, ok := x.(*ConsoleTab); ok && c.connID == st.ConnID {
			n++
		}
	}
	t.title = "console"
	if n > 1 {
		t.title = fmt.Sprintf("console %d", n)
	}
	if st.Title != "" {
		t.title = st.Title
	}
	return t
}

func prefsProtected(c *db.Config) bool { return prefs.Protected(c) }

func (t *ConsoleTab) cs() *connState { return t.a.connState(t.connID) }

// unsavedQuery returns the console text when it holds a query that isn't
// one of the saved queries (an empty console has nothing to save).
func (t *ConsoleTab) unsavedQuery() (string, bool) {
	text := t.ed.Text()
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", false
	}
	for _, q := range t.a.prefs.QueriesFor(t.connID) {
		if strings.TrimSpace(q.SQL) == trimmed {
			return "", false
		}
	}
	return text, true
}

// queryName suggests a saved query name: the console title unless it is
// the default "console N".
func (t *ConsoleTab) queryName() string {
	if strings.HasPrefix(t.title, "console") {
		return ""
	}
	return t.title
}

func (t *ConsoleTab) Title() string {
	return t.cs().name() + " · " + t.title
}
func (t *ConsoleTab) Icon() *widget.Icon { return icConsole }
func (t *ConsoleTab) ConnID() string     { return t.connID }
func (t *ConsoleTab) Focused(gtx C) bool {
	return t.ed.Focused(gtx) || t.grid.Focused(gtx) || t.value.focused(gtx)
}

func (t *ConsoleTab) Dirty() string {
	switch {
	case t.txActive:
		return "The transaction of this console is still open: closing rolls it back."
	case t.running:
		return "A statement is still running."
	}
	return ""
}

func (t *ConsoleTab) StatusInfo() string {
	var parts []string
	if t.schema != "" {
		parts = append(parts, "schema "+t.schema)
	}
	mode := "auto-commit"
	if t.manual {
		mode = "manual tx"
	}
	if t.txActive {
		mode += " · TRANSACTION OPEN"
	}
	parts = append(parts, mode)
	if t.running {
		parts = append(parts, "running "+fmtDur(time.Since(t.runStart)))
	}
	return strings.Join(parts, " · ")
}

func (t *ConsoleTab) State() prefs.TabState {
	return prefs.TabState{Kind: "console", ConnID: t.connID, Schema: t.schema, Text: t.ed.Text(), Title: t.title, Manual: t.manual}
}

func (t *ConsoleTab) Disconnected() {
	t.closeSession()
	for _, rs := range t.results {
		rs.detach()
	}
	t.running = false
}

func (t *ConsoleTab) Dispose() {
	t.disposed = true
	t.closeSession()
}

func (t *ConsoleTab) closeSession() {
	if t.cancelRun != nil {
		t.cancelRun()
	}
	t.runGen++ // the run in flight must not report a transaction state
	t.running = false
	if s := t.sess; s != nil {
		t.sess = nil
		go func() {
			s.Cancel()
			s.Close()
		}()
	}
	t.txActive = false
}

func (t *ConsoleTab) current() *resultSet {
	if t.resIdx >= 0 && t.resIdx < len(t.results) {
		return t.results[t.resIdx]
	}
	return nil
}

func (t *ConsoleTab) log(isErr bool, f string, args ...any) {
	t.messages = append(t.messages, consoleMsg{at: time.Now(), text: fmt.Sprintf(f, args...), isErr: isErr})
	if len(t.messages) > 2000 {
		t.messages = t.messages[len(t.messages)-2000:]
	}
}

// withSession runs then with an open session (connecting first); failed
// runs instead when the connection or the session can't be opened.
func (t *ConsoleTab) withSession(then func(db.Session), failed func(error)) {
	a := t.a
	a.connectOr(t.connID, func(cs *connState) {
		if t.disposed {
			failed(errTabClosed)
			return
		}
		if t.sess != nil && t.sessGen == cs.gen {
			then(t.sess)
			return
		}
		t.sessWait = append(t.sessWait, sessWaiter{then, failed})
		if t.opening {
			return
		}
		t.opening = true
		conn, gen := cs.conn, cs.gen
		schema := t.schema
		ro := a.connReadOnly(t.connID)
		t.lang = conn.Language()
		t.ed.setLanguage(t.lang)
		t.stmts = nil // split again with the language's rules
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), db.ConnectTimeout)
			defer cancel()
			s, err := conn.Session(ctx)
			var warn error
			if err == nil && schema != "" {
				if e := s.SetCurrentSchema(ctx, schema); e != nil && !errors.Is(e, db.ErrNotSupported) {
					warn = e
				}
			}
			if err == nil && ro {
				if e := s.SetReadOnly(ctx, true); e != nil && !errors.Is(e, db.ErrNotSupported) {
					warn = e
				}
			}
			cur := ""
			if err == nil && schema == "" {
				cur, _ = s.CurrentSchema(ctx)
			}
			a.post(func() {
				t.opening = false
				w := t.sessWait
				t.sessWait = nil
				if err == nil && (cs.gen != gen || t.disposed) {
					go s.Close()
					err = errTabClosed
					if !t.disposed {
						err = errors.New("the connection was reset")
					}
				}
				if err != nil {
					if !t.disposed {
						t.log(true, "Session: %v", err)
						a.flash("Open session: "+err.Error(), true)
					}
					for _, f := range w {
						f.fail(err)
					}
					return
				}
				if warn != nil {
					t.log(true, "Session setup: %v", warn)
				}
				if t.schema == "" {
					t.schema = cur
				}
				t.sess, t.sessGen = s, gen
				for _, f := range w {
					f.ok(s)
				}
			})
		}()
	}, failed)
}

// sessWaiter is a caller waiting for a tab session.
type sessWaiter struct {
	ok   func(db.Session)
	fail func(error)
}

var errTabClosed = errors.New("tab closed")

// command handles editor shortcuts.
func (t *ConsoleTab) command(cmd string) bool {
	switch cmd {
	case "run":
		t.run(false)
	case "runAll":
		t.run(true)
	default:
		return false
	}
	return true
}

// statements returns the parsed statements of the text (cached).
func (t *ConsoleTab) statements() []sqlx.Statement {
	if t.stmtVer != t.ed.buf.version || t.stmts == nil {
		t.stmtVer = t.ed.buf.version
		t.stmtText = t.ed.Text()
		t.stmts = t.ed.lang.Split(t.stmtText)
	}
	return t.stmts
}

// statementsLazy is statements() for per-frame decorations: big scripts
// are re-split only once typing pauses (nil = not available yet).
func (t *ConsoleTab) statementsLazy(now time.Time) []sqlx.Statement {
	if t.stmtVer == t.ed.buf.version && t.stmts != nil {
		return t.stmts
	}
	if t.ed.buf.size() > 200_000 && now.Sub(t.ed.buf.lastEdit) < 400*time.Millisecond {
		return nil
	}
	return t.statements()
}

// window returns the text around the caret (whole lines) and the caret
// offset in it: completion of big scripts doesn't split everything.
func (t *ConsoleTab) window(before, after int) (string, int) {
	e := t.ed
	from := max(0, e.caret.line-before)
	to := min(e.buf.lineCount()-1, e.caret.line+after)
	var b strings.Builder
	caret := 0
	for l := from; l <= to; l++ {
		if l == e.caret.line {
			caret = b.Len() + e.caret.col
		}
		b.WriteString(e.buf.lines[l])
		if l < to {
			b.WriteByte('\n')
		}
	}
	return b.String(), caret
}

// targets picks what to execute: the selection, the statement at the
// caret, or (all) every statement.
func (t *ConsoleTab) targets(all bool) []sqlx.Statement {
	if sel := t.ed.Selected(); strings.TrimSpace(sel) != "" {
		a, _ := t.ed.SelectionOffsets()
		st := t.ed.lang.Split(sel)
		for i := range st {
			st[i].Start += a
			st[i].End += a
		}
		if !all && len(st) > 1 {
			return st
		}
		return st
	}
	stmts := t.statements()
	if all {
		return stmts
	}
	i := sqlx.StatementAt(stmts, t.stmtText, t.ed.CaretOffset())
	if i < 0 {
		return nil
	}
	return stmts[i : i+1]
}

// run executes the statement at the caret (or the script).
func (t *ConsoleTab) run(all bool) {
	a := t.a
	if t.running {
		a.flash("A statement is already running (Esc / Stop to cancel it)", true)
		return
	}
	if t.lang == nil {
		// split with the database's rules ($$ bodies, DELIMITER, GO…):
		// connect first
		a.connectOr(t.connID, func(cs *connState) {
			if t.lang == nil {
				t.lang = cs.conn.Language()
				t.ed.setLanguage(t.lang)
				t.stmts = nil
			}
			t.run(all)
		}, func(err error) {
			if err != errConnectCancelled {
				t.log(true, "%v", err)
			}
		})
		return
	}
	stmts := t.targets(all)
	if len(stmts) == 0 {
		a.flash("Nothing to run", false)
		return
	}
	lang := t.ed.lang
	worst := db.StmtRead
	for _, s := range stmts {
		if k := lang.Classify(s.Text); k > worst {
			worst = k
		}
	}
	if worst.Modifies() && a.connReadOnly(t.connID) {
		a.flash("Blocked: "+t.cs().name()+" is read-only ("+worst.String()+" statement)", true)
		return
	}
	exec := func() {
		if worst.Modifies() {
			a.releaseCursors(t.connID)
		}
		t.execute(stmts)
	}
	if worst.Modifies() && a.connProtected(t.connID) && !t.manual {
		msg := fmt.Sprintf("Run %d statement(s) with %s changes on the protected connection %s? Auto-commit is on: changes are permanent.",
			len(stmts), worst.String(), t.cs().name())
		c := newConfirm(a, "Run on production", msg+"\n\n"+preview(stmts), true, func(bool) { exec() })
		if worst == db.StmtDestructive {
			c.typeName, c.prodCtx = t.cs().name(), t.cs().name()
		}
		c.okLabel = "Run"
		a.overlay = c
		return
	}
	exec()
}

func preview(stmts []sqlx.Statement) string {
	var b strings.Builder
	for i, s := range stmts {
		if i == 4 {
			fmt.Fprintf(&b, "… and %d more", len(stmts)-4)
			break
		}
		txt := s.Text
		if len(txt) > 160 {
			txt = txt[:160] + "…"
		}
		b.WriteString(strings.ReplaceAll(txt, "\n", " ") + "\n")
	}
	return b.String()
}

// execute runs statements one after the other on the session.
func (t *ConsoleTab) execute(stmts []sqlx.Statement) {
	a := t.a
	t.running, t.runStart = true, time.Now()
	t.runGen++
	gen := t.runGen
	bufVer := t.ed.buf.version
	t.ed.ErrLine = -1
	for _, rs := range t.results {
		rs.detach()
	}
	t.results, t.resIdx = nil, 0
	t.grid.SetModel(nil)
	t.withSession(func(s db.Session) {
		if gen != t.runGen {
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		t.cancelRun = cancel
		manual := t.manual
		fetch := a.prefs.Fetch()
		connID := t.connID
		schema := t.schema
		lang := t.ed.lang
		go func() {
			defer cancel()
			ddl := false
			if manual && !s.TxActive() {
				if err := s.Begin(ctx); err != nil && !errors.Is(err, db.ErrNotSupported) {
					a.post(func() { t.log(true, "BEGIN: %v", err) })
				}
			}
			for i, st := range stmts {
				if ctx.Err() != nil {
					break
				}
				last := i == len(stmts)-1
				rs := &resultSet{sql: st.Text, stmtStart: st.Start, bufVer: bufVer, sortCol: -1, affected: -1, started: time.Now(), gen: gen}
				a.post(func() {
					if gen == t.runGen {
						t.runStmt = st.Text
					}
				})
				res, err := s.Execute(ctx, st.Text)
				rs.elapsed = time.Since(rs.started)
				if err == nil && res.Columns() != nil {
					rs.isQuery, rs.cols = true, res.Columns()
					rows, more, ferr := res.Fetch(ctx, fetch)
					rs.rows, rs.more, err = rows, more, ferr
					rs.elapsed = time.Since(rs.started)
					switch {
					case more && last:
						rs.res = res // keep the cursor: rows stream in on scroll
					case more:
						rs.partial = "first " + itoa(len(rows)) + " rows (re-run the statement alone to fetch more)"
						rs.more = false
						res.Close()
					default:
						res.Close()
					}
				} else if err == nil {
					rs.affected = res.RowsAffected()
					res.Close()
				}
				rs.err = err
				if res != nil {
					rs.notices = res.Messages()
				}
				if k := lang.Classify(st.Text); k == db.StmtDDL || k == db.StmtDestructive {
					ddl = true
				}
				entry := history.Entry{At: rs.started, SQL: st.Text, Millis: rs.elapsed.Milliseconds(), Rows: int64(len(rs.rows)), Schema: schema}
				if !rs.isQuery {
					entry.Rows = rs.affected
				}
				if err != nil {
					entry.Error = err.Error()
				}
				_ = history.Add(connID, entry)
				a.post(func() { t.addResult(gen, rs) })
				if err != nil {
					break // stop the script at the first error
				}
			}
			tx := s.TxActive()
			a.post(func() {
				if gen != t.runGen {
					return
				}
				t.running, t.cancelRun = false, nil
				t.txActive = tx
				if ddl {
					t.cs().refreshAll()
				}
				el := time.Since(t.runStart)
				if el > time.Duration(a.prefs.NotifySeconds())*time.Second && a.notifier != nil && !a.winFocused {
					msg := fmt.Sprintf("%d statement(s) finished in %s", len(stmts), fmtDur(el))
					if rs := t.lastErr(); rs != nil {
						msg = "Failed: " + rs.err.Error()
					}
					title := AppName + " · " + t.cs().name()
					go func() { _ = a.notifier.Send(title, msg, notify.Normal) }()
				}
			})
		}()
	}, func(err error) {
		if gen == t.runGen && !t.disposed {
			t.running, t.cancelRun = false, nil
			if err != errConnectCancelled {
				t.log(true, "%v", err)
			}
		}
	})
}

func (t *ConsoleTab) lastErr() *resultSet {
	for _, rs := range t.results {
		if rs.err != nil {
			return rs
		}
	}
	return nil
}

// addResult shows a finished statement.
func (t *ConsoleTab) addResult(gen int, rs *resultSet) {
	if gen != t.runGen {
		rs.closeCursor()
		return
	}
	t.results = append(t.results, rs)
	short := strings.ReplaceAll(rs.sql, "\n", " ")
	if len(short) > 90 {
		short = short[:90] + "…"
	}
	for _, n := range rs.notices {
		t.log(false, "%s", n)
	}
	if rs.err != nil {
		t.log(true, "%s\n%s", short, rs.err)
		t.markError(rs)
	} else {
		t.log(false, "%s — %s", short, rs.summary())
	}
	// show the last result set with rows, else the last result
	t.resIdx = len(t.results) - 1
	if rs.isQuery || rs.err != nil {
		t.grid.SetModel(rs)
		t.grid.SortCol = -1
	}
}

// markError moves the editor to the error position (not when the text
// changed since the statement ran: the offsets would point elsewhere).
func (t *ConsoleTab) markError(rs *resultSet) {
	if rs.bufVer != t.ed.buf.version {
		return
	}
	e := db.AsError(rs.err)
	off := rs.stmtStart
	exact := true
	switch {
	case e.Position > 0 && e.PositionBytes:
		off += min(e.Position-1, len(rs.sql))
	case e.Position > 0:
		// position counts characters in the statement
		off += runeOffset(rs.sql, e.Position-1)
	case e.Line > 0:
		lines := strings.SplitN(rs.sql, "\n", e.Line)
		for _, l := range lines[:min(len(lines), e.Line)-1] {
			off += len(l) + 1
		}
		if e.Column > 0 && e.Line <= len(lines) {
			off += runeOffset(lines[e.Line-1], e.Column-1)
		} else {
			exact = false
		}
	default:
		exact = false
	}
	p := t.ed.buf.posAt(off)
	t.ed.ErrLine = p.line
	if exact {
		t.ed.SetCaretOffset(off)
	}
}

func runeOffset(s string, n int) int {
	i := 0
	for n > 0 && i < len(s) {
		_, w := utf8.DecodeRuneInString(s[i:])
		i += w
		n--
	}
	return i
}

func (t *ConsoleTab) cancel() {
	if !t.running {
		return
	}
	if s := t.sess; s != nil {
		go s.Cancel()
	}
	if t.cancelRun != nil {
		t.cancelRun()
	}
	t.a.flash("Cancelling…", false)
}

func (t *ConsoleTab) commit(rollback bool) {
	s := t.sess
	if s == nil || t.running {
		return
	}
	verb := "Commit"
	if rollback {
		verb = "Rollback"
	}
	a := t.a
	a.flash(verb+"…", false)
	go func() {
		var err error
		if rollback {
			err = s.Rollback(context.Background())
		} else {
			err = s.Commit(context.Background())
		}
		active := s.TxActive()
		a.post(func() {
			if t.sess == s {
				t.txActive = active // also after a failed COMMIT (the server may have ended it)
			}
			if err != nil {
				t.log(true, "%s: %v", strings.ToUpper(verb), err)
				a.flash(fmt.Sprintf("%s failed: %v", verb, err), true)
				return
			}
			t.log(false, "%s", strings.ToUpper(verb))
			a.flash(verb+" — done", false)
		})
	}()
}

func (t *ConsoleTab) setManual(on bool) {
	if t.manual == on {
		return
	}
	if !on && t.txActive {
		t.a.flash("Commit or roll back the open transaction first", true)
		return
	}
	t.manual = on
	t.a.scheduleSave()
}

// pickSchema changes the session schema.
func (t *ConsoleTab) pickSchema() {
	a := t.a
	a.connect(t.connID, func(cs *connState) {
		list, ok := cs.schemas[""]
		if !ok {
			cs.loadSchemas("")
			a.flash("Loading schemas…", false)
			return
		}
		var names []string
		for _, s := range list {
			names = append(names, s.Name)
		}
		p := newPicker(a, "Schema of this console", names, false, func(name string) {
			t.schema = name
			if s := t.sess; s != nil {
				go func() {
					err := s.SetCurrentSchema(context.Background(), name)
					a.post(func() {
						if err != nil && !errors.Is(err, db.ErrNotSupported) {
							a.flash("Set schema: "+err.Error(), true)
						} else if errors.Is(err, db.ErrNotSupported) {
							a.flash("This database has no session schema: qualify names (completion uses "+name+")", false)
						}
					})
				}()
			}
			a.scheduleSave()
		})
		p.icon = icSchema
		a.overlay = p
	})
}

// Layout draws the console.
func (t *ConsoleTab) Layout(gtx C) D {
	if t.lang == nil {
		// highlighting and completion follow the database once connected
		if cs := t.cs(); cs.status == stReady && cs.conn != nil {
			t.lang = cs.conn.Language()
			t.ed.setLanguage(t.lang)
			t.stmts = nil
		}
	}
	t.handleButtons(gtx)
	// current statement marker
	if stmts := t.statementsLazy(gtx.Now); len(stmts) > 0 && t.ed.focused {
		if i := sqlx.StatementAt(stmts, t.stmtText, t.ed.CaretOffset()); i >= 0 {
			t.ed.StmtFrom = t.ed.buf.posAt(stmts[i].Start).line
			t.ed.StmtTo = t.ed.buf.posAt(stmts[i].End).line
		}
	} else {
		t.ed.StmtFrom, t.ed.StmtTo = -1, -1
	}
	if t.running {
		gtx.Execute(opInvalidateIn(gtx, 250*time.Millisecond))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(t.layoutToolbar),
		layout.Flexed(1, func(gtx C) D {
			h := gtx.Constraints.Max.Y
			edH := int(float32(h) * t.split)
			edH = max(gtx.Dp(60), min(edH, h-gtx.Dp(90)))
			t.curH = edH
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, edH))
					return t.ed.Layout(gtx)
				}),
				layout.Rigid(func(gtx C) D { return t.layoutDivider(gtx, h) }),
				layout.Flexed(1, t.layoutResults),
			)
		}),
	)
}

// layoutDivider resizes the editor. Pointer positions are relative to the
// divider, which moves while dragging: they are turned into absolute
// positions with the divider's offset of the frame they were hit-tested in,
// and the editor height is set from that absolute position (no deltas, so
// the divider follows the pointer exactly).
func (t *ConsoleTab) layoutDivider(gtx C, total int) D {
	h := gtx.Dp(5)
	prevTop := t.divTop
	sz := image.Pt(gtx.Constraints.Max.X, h)
	fillRect(gtx, pal.Header, image.Rectangle{Max: sz})
	fillRect(gtx, pal.Border, image.Rect(0, h/2, sz.X, h/2+gtx.Dp(1)))
	area := clip.Rect{Max: sz}.Push(gtx.Ops)
	pointer.CursorRowResize.Add(gtx.Ops)
	event.Op(gtx.Ops, &t.divTag)
	area.Pop()
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &t.divTag, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel})
		if !ok {
			break
		}
		pe, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		switch pe.Kind {
		case pointer.Press:
			t.dragging, t.grabY = true, pe.Position.Y // pointer offset in the divider
		case pointer.Drag:
			if t.dragging && total > 0 {
				edH := int(float32(prevTop) + pe.Position.Y - t.grabY)
				// +0.5: int(total*split) gives back edH exactly
				t.split = max(0.1, min((float32(edH)+0.5)/float32(total), 0.9))
			}
		case pointer.Release, pointer.Cancel:
			t.dragging = false
		}
	}
	t.divTop = t.curH // where it is laid out this frame
	return D{Size: sz}
}

func (t *ConsoleTab) handleButtons(gtx C) {
	a := t.a
	if t.runBtn.Clicked(gtx) {
		t.run(false)
	}
	if t.scriptBtn.Clicked(gtx) {
		t.run(true)
	}
	if t.stopBtn.Clicked(gtx) {
		t.cancel()
	}
	if t.commitBtn.Clicked(gtx) {
		t.commit(false)
	}
	if t.rollbackBtn.Clicked(gtx) {
		t.commit(true)
	}
	if t.schemaBtn.Clicked(gtx) {
		t.pickSchema()
	}
	if t.modeBtns[0].Clicked(gtx) {
		t.setManual(false)
	}
	if t.modeBtns[1].Clicked(gtx) {
		t.setManual(true)
	}
	if t.histBtn.Clicked(gtx) {
		a.openHistory(t.connID)
	}
	if t.saveBtn.Clicked(gtx) {
		a.saveQueryDialog(t.connID, t.ed.Text())
	}
	if t.aiBtn.Clicked(gtx) {
		a.askAboutSQL(t.connID, t.ed.Text(), t.lastErr())
	}
	if t.fetchAllBtn.Clicked(gtx) {
		if rs := t.current(); rs != nil {
			rs.fetchMore(a, 10000, true, nil)
		}
	}
	if t.exportBtn.Clicked(gtx) {
		if rs := t.current(); rs != nil && rs.isQuery {
			ed := newExportDialog(a, t.connID, rs, nil)
			ed.schema = t.schema
			a.overlay = ed
		}
	}
	if t.valueBtn.Clicked(gtx) {
		t.value.open = !t.value.open
	}
	if t.msgTab.Clicked(gtx) {
		t.resIdx = len(t.results)
	}
	if t.gotoErrBtn.Clicked(gtx) {
		if rs := t.lastErr(); rs != nil {
			t.markError(rs)
			t.ed.Focus(gtx)
		}
	}
	for len(t.resTabs) < len(t.results) {
		t.resTabs = append(t.resTabs, widget.Clickable{})
	}
	for i := range t.results {
		if t.resTabs[i].Clicked(gtx) {
			t.resIdx = i
			if rs := t.results[i]; rs.isQuery {
				t.grid.SetModel(rs)
				t.grid.SortCol, t.grid.SortDesc = rs.sortCol, rs.sortDesc
			}
		}
	}
}

func (t *ConsoleTab) layoutToolbar(gtx C) D {
	th := t.a.th
	return bg(gtx, pal.Header, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Inset{Left: 8, Right: 8, Top: 4, Bottom: 4}.Layout(gtx, func(gtx C) D {
			btn := func(cl *widget.Clickable, ic *widget.Icon, txt, tip string, c colorNRGBA) layout.FlexChild {
				return layout.Rigid(func(gtx C) D {
					return layout.Inset{Right: 4}.Layout(gtx, func(gtx C) D {
						d := pillButton(gtx, th, cl, ic, txt, c, colorNRGBA{})
						t.a.hintRect(gtx, "tb/"+tip, image.Rectangle{Max: d.Size}, tip)
						return d
					})
				})
			}
			items := []layout.FlexChild{}
			if t.running {
				items = append(items, btn(&t.stopBtn, icStop, "Stop", "Cancel the running statement", pal.Err))
			} else {
				items = append(items, btn(&t.runBtn, icRun, "Run", "Run the statement at the caret or the selection ("+shortcutLabel("Enter")+")", pal.Ok))
			}
			items = append(items,
				btn(&t.scriptBtn, icRunAll, "Script", "Run every statement ("+shortcutLabel("Shift Enter")+")", pal.Text),
				layout.Rigid(func(gtx C) D { return layout.Inset{Left: 6, Right: 6}.Layout(gtx, vsep) }),
				layout.Rigid(func(gtx C) D {
					sel := 0
					if t.manual {
						sel = 1
					}
					return segmented(gtx, th, t.modeBtns[:], []string{"Auto", "Manual"}, sel)
				}),
			)
			if t.manual || t.txActive {
				c := pal.TextDim
				if t.txActive {
					c = pal.Accent
				}
				items = append(items,
					layout.Rigid(layout.Spacer{Width: 6}.Layout),
					btn(&t.commitBtn, icCommit, "Commit", "Commit the transaction", c),
					btn(&t.rollbackBtn, icUndo, "Rollback", "Roll back the transaction", c),
				)
			}
			schema := t.schema
			if schema == "" {
				schema = "default schema"
			}
			items = append(items,
				layout.Rigid(func(gtx C) D { return layout.Inset{Left: 6, Right: 6}.Layout(gtx, vsep) }),
				btn(&t.schemaBtn, icSchema, schema, "Schema used for unqualified names", pal.Text),
				layout.Flexed(1, func(gtx C) D { return D{Size: image.Pt(gtx.Constraints.Min.X, 1)} }),
				btn(&t.aiBtn, icAI, "", "Ask the assistant about this SQL", pal.TextDim),
				btn(&t.saveBtn, icBookmark, "", "Save this query", pal.TextDim),
				btn(&t.histBtn, icHistory, "", "Query history", pal.TextDim),
			)
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, items...)
		})
	})
}

func vsep(gtx C) D {
	h := gtx.Dp(18)
	fillRect(gtx, pal.Border, image.Rect(0, 0, gtx.Dp(1), h))
	return D{Size: image.Pt(gtx.Dp(1), h)}
}

func (t *ConsoleTab) layoutResults(gtx C) D {
	th := t.a.th
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return bg(gtx, pal.Header, func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Inset{Left: 4, Right: 8}.Layout(gtx, func(gtx C) D {
					var tabs []layout.FlexChild
					for i, rs := range t.results {
						i, rs := i, rs
						tabs = append(tabs, layout.Rigid(func(gtx C) D {
							ic, label := icTable, "Result "+itoa(i+1)
							c := pal.TextDim
							switch {
							case rs.err != nil:
								ic, c, label = icError, pal.Err, "Error"
							case !rs.isQuery:
								ic, label = icCommit, "Update "+itoa(i+1)
							}
							return t.resTab(gtx, th, &t.resTabs[i], ic, label, c, i == t.resIdx)
						}))
					}
					tabs = append(tabs, layout.Rigid(func(gtx C) D {
						c := pal.TextDim
						if len(t.messages) > 0 && t.messages[len(t.messages)-1].isErr {
							c = pal.Err
						}
						return t.resTab(gtx, th, &t.msgTab, icLogs, "Messages", c, t.resIdx >= len(t.results))
					}))
					tabs = append(tabs, layout.Flexed(1, func(gtx C) D { return D{Size: image.Pt(gtx.Constraints.Min.X, 1)} }))
					if rs := t.current(); rs != nil {
						tabs = append(tabs, layout.Rigid(func(gtx C) D {
							return lbl(gtx, th, fontSans, 11.5, pal.TextDim, rs.summary())
						}))
						if rs.isQuery {
							if rs.more {
								tabs = append(tabs, layout.Rigid(func(gtx C) D {
									return layout.Inset{Left: 8}.Layout(gtx, func(gtx C) D {
										return pillButton(gtx, th, &t.fetchAllBtn, icDownload, "Fetch all", pal.Text, pal.Hover)
									})
								}))
							}
							tabs = append(tabs,
								layout.Rigid(func(gtx C) D {
									return layout.Inset{Left: 4}.Layout(gtx, func(gtx C) D {
										return pillButton(gtx, th, &t.exportBtn, icExport, "Export", pal.Text, colorNRGBA{})
									})
								}),
								layout.Rigid(func(gtx C) D {
									b := colorNRGBA{}
									if t.value.open {
										b = pal.Selected
									}
									return layout.Inset{Left: 4}.Layout(gtx, func(gtx C) D {
										return pillButton(gtx, th, &t.valueBtn, icDescribe, "Value", pal.Text, b)
									})
								}))
						}
					} else if t.running {
						tabs = append(tabs, layout.Rigid(func(gtx C) D {
							return lbl(gtx, th, fontSans, 11.5, pal.Warn, "Running "+fmtDur(time.Since(t.runStart))+"…")
						}))
					}
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx, tabs...)
				})
			})
		}),
		layout.Rigid(func(gtx C) D { return hline(gtx, pal.Border) }),
		layout.Flexed(1, func(gtx C) D {
			rs := t.current()
			switch {
			case rs == nil && t.resIdx >= len(t.results) && len(t.results) > 0 || rs == nil && len(t.messages) > 0 && !t.running:
				return t.layoutMessages(gtx)
			case rs == nil && t.running:
				return t.layoutRunning(gtx)
			case rs == nil:
				return layout.Center.Layout(gtx, func(gtx C) D {
					return lbl(gtx, th, fontSans, 12.5, pal.TextDim, "Run a statement with "+shortcutLabel("Enter")+" — results appear here")
				})
			case rs.err != nil:
				return t.layoutError(gtx, rs)
			case !rs.isQuery:
				return layout.Center.Layout(gtx, func(gtx C) D {
					return iconText(gtx, th, icOK, 14, pal.Ok, rs.summary())
				})
			}
			return t.value.layoutWith(gtx, t.grid, func(r, c int) (any, db.Column, bool) {
				if r >= 0 && c >= 0 && r < rs.Rows() && c < len(rs.cols) {
					return rs.Value(r, c), rs.cols[c], true
				}
				return nil, db.Column{}, false
			}, nil)
		}),
	)
}

func (t *ConsoleTab) resTab(gtx C, th *material.Theme, cl *widget.Clickable, ic *widget.Icon, label string, c colorNRGBA, active bool) D {
	return cl.Layout(gtx, func(gtx C) D {
		tc := pal.TextDim
		if active || cl.Hovered() {
			tc = pal.TextBright
		}
		if c == pal.Err {
			tc = pal.Err
		}
		d := layout.Inset{Left: 10, Right: 10, Top: 7, Bottom: 7}.Layout(gtx, func(gtx C) D {
			return iconText(gtx, th, ic, 11.5, tc, label)
		})
		if active {
			fillRect(gtx, pal.Accent, image.Rect(0, d.Size.Y-gtx.Dp(2), d.Size.X, d.Size.Y))
		}
		return d
	})
}

func (t *ConsoleTab) layoutRunning(gtx C) D {
	th := t.a.th
	return layout.Center.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return lbl(gtx, th, fontSans, 13, pal.Text, "Running for "+fmtDur(time.Since(t.runStart).Round(100*time.Millisecond))+"…")
			}),
			layout.Rigid(layout.Spacer{Height: 10}.Layout),
			layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &t.stopBtn, icStop, "Cancel", pal.Err, pal.Hover) }),
		)
	})
}

func (t *ConsoleTab) layoutError(gtx C, rs *resultSet) D {
	th := t.a.th
	e := db.AsError(rs.err)
	return inset(14).Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				title := "Error"
				if e.Code != "" {
					title += " " + e.Code
				}
				return iconText(gtx, th, icError, 14, pal.Err, title)
			}),
			layout.Rigid(layout.Spacer{Height: 8}.Layout),
			layout.Rigid(func(gtx C) D {
				return t.a.selText(gtx, "err/"+itoa(rs.gen), fontMono, 12.5, pal.TextBright, e.Msg, 0)
			}),
			layout.Rigid(func(gtx C) D {
				var extra []string
				if e.Detail != "" {
					extra = append(extra, "Detail: "+e.Detail)
				}
				if e.Hint != "" {
					extra = append(extra, "Hint: "+e.Hint)
				}
				if e.Line > 0 {
					extra = append(extra, "Line "+itoa(e.Line)+" of the statement")
				}
				if len(extra) == 0 {
					return D{}
				}
				return layout.Inset{Top: 8}.Layout(gtx, func(gtx C) D {
					return t.a.selText(gtx, "errx/"+itoa(rs.gen), fontSans, 12, pal.Text, strings.Join(extra, "\n"), 0)
				})
			}),
			layout.Rigid(layout.Spacer{Height: 12}.Layout),
			layout.Rigid(func(gtx C) D {
				return layout.Flex{}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						return pillButton(gtx, th, &t.gotoErrBtn, icEdit, "Show in editor", pal.Text, pal.Hover)
					}),
					layout.Rigid(layout.Spacer{Width: 8}.Layout),
					layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &t.aiBtn, icAI, "Ask the assistant", pal.Text, pal.Hover) }),
				)
			}),
		)
	})
}

func (t *ConsoleTab) layoutMessages(gtx C) D {
	th := t.a.th
	return material.List(th, &t.msgList).Layout(gtx, len(t.messages), func(gtx C, i int) D {
		m := t.messages[len(t.messages)-1-i]
		c := pal.Text
		if m.isErr {
			c = pal.Err
		}
		return layout.Inset{Left: 12, Right: 12, Top: 4, Bottom: 4}.Layout(gtx, func(gtx C) D {
			return layout.Flex{}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontMono, 11, pal.TextDim, m.at.Format("15:04:05")) }),
				layout.Rigid(layout.Spacer{Width: 10}.Layout),
				layout.Flexed(1, func(gtx C) D {
					return t.a.selText(gtx, "msg/"+m.at.String()+itoa(i), fontMono, 11.5, c, m.text, 0)
				}),
			)
		})
	})
}

// editorMenu is the right-click menu of the editor.
func (t *ConsoleTab) editorMenu(at image.Point) {
	a := t.a
	ed := t.ed
	items := []*menuItem{
		{icon: icRun, label: "Run statement", hint: shortcutLabel("Enter"), action: func() { t.run(false) }},
		{icon: icRunAll, label: "Run script", hint: shortcutLabel("Shift Enter"), action: func() { t.run(true) }},
		{sep: true},
		{icon: icCopyText, label: "Copy", hint: shortcutLabel("C"), action: func() {
			if s := ed.Selected(); s != "" {
				a.copyLater(s)
			}
		}},
		{icon: icClear, label: "Cut", hint: shortcutLabel("X"), action: func() {
			if s := ed.Selected(); s != "" {
				a.copyLater(s)
				ed.Insert("")
			}
		}},
		{icon: icPaste, label: "Paste", hint: shortcutLabel("V"), action: func() { a.pasteInto(ed) }},
		{icon: icSearch, label: "Find / replace", hint: shortcutLabel("F"), action: func() { ed.find.show(layout.Context{}, true, ed.Selected()) }},
		{sep: true},
		{icon: icEdit, label: "Toggle comment", hint: shortcutLabel("/"), action: ed.toggleComment},
		{icon: icEdit, label: "Upper / lower case", hint: shortcutLabel("Shift U"), action: func() {
			ed.onKey(layout.Context{}, keyEvent("U"))
		}},
		{sep: true},
		{icon: icAI, label: "Explain with the assistant", action: func() { a.askAboutSQL(t.connID, t.pickedSQL(), nil) }},
		{icon: icBookmark, label: "Save query…", action: func() { a.saveQueryDialog(t.connID, t.pickedSQL()) }},
	}
	a.overlay = &ContextMenu{app: a, at: at, items: items, sel: -1}
}

// pickedSQL is the selection or the statement at the caret.
func (t *ConsoleTab) pickedSQL() string {
	st := t.targets(false)
	var parts []string
	for _, s := range st {
		parts = append(parts, s.Text)
	}
	return strings.Join(parts, ";\n")
}

// gridMenu is the right-click menu of the result grid.
func (t *ConsoleTab) gridMenu(at image.Point) {
	rs := t.current()
	if rs == nil {
		return
	}
	a := t.a
	items := copyMenuItems(a, t.grid, rs.cols, func(r, c int) any { return rs.Value(r, c) }, t.lang, db.ObjectRef{Name: "result"})
	items = append(items, &menuItem{sep: true},
		&menuItem{icon: icDescribe, label: "Show value", action: func() { t.value.open = true }},
		&menuItem{icon: icExport, label: "Export…", action: func() {
			ed := newExportDialog(a, t.connID, rs, nil)
			ed.schema = t.schema
			a.overlay = ed
		}},
	)
	a.overlay = &ContextMenu{app: a, at: at, items: items, sel: -1}
}

// complete answers the editor's completion requests from the cached
// metadata (missing metadata is loaded for the next keystroke).
func (t *ConsoleTab) complete(e *CodeEditor) *compRequest {
	cs := t.cs()
	if cs.status != stReady {
		return t.keywordItems(nil)
	}
	text, rel := t.window(300, 100)
	stmts := e.lang.Split(text)
	i := sqlx.StatementAt(stmts, text, rel)
	if i >= 0 && rel >= stmts[i].Start && rel <= stmts[i].End+1 {
		end := min(len(text), max(stmts[i].End, rel))
		text, rel = text[stmts[i].Start:end], rel-stmts[i].Start
	} else {
		// caret outside statements (new line): use the current line
		l := e.buf.lines[e.caret.line]
		text, rel = l, e.caret.col
	}
	ctx := e.lang.CompletionContext(text, rel)
	schema := t.schema
	if schema == "" {
		schema = cs.defaultSchema("")
	}
	k := metaKey{schema: schema}
	cs.loadCompletion(k)
	comp := cs.completion[k]
	var items []compItem
	tableCols := func(sch, name string) []string {
		kk := metaKey{catalog: k.catalog, schema: sch}
		if sch == "" {
			kk = k
		} else {
			// the schema as the catalog spells it (Oracle folds to upper case)
			exact := false
			for _, sc := range cs.schemas[k.catalog] {
				exact = exact || sc.Name == sch
			}
			for _, sc := range cs.schemas[k.catalog] {
				if !exact && strings.EqualFold(sc.Name, sch) {
					kk.schema = sc.Name
					break
				}
			}
		}
		cs.loadCompletion(kk)
		m := cs.completion[kk]
		if cols, ok := m[name]; ok {
			return cols
		}
		for n, cols := range m {
			if strings.EqualFold(n, name) {
				return cols
			}
		}
		return nil
	}
	if ctx.Qualifier != "" {
		// alias/table columns
		for _, r := range ctx.Refs {
			if strings.EqualFold(r.Alias, ctx.Qualifier) || r.Alias == "" && strings.EqualFold(r.Name, ctx.Qualifier) {
				for _, c := range tableCols(r.Schema, r.Name) {
					items = append(items, compItem{label: c, kind: "column", detail: r.Name})
				}
				return &compRequest{items: items}
			}
		}
		// schema.tables
		kk := metaKey{schema: ctx.Qualifier}
		for _, s := range cs.schemas[""] {
			if strings.EqualFold(s.Name, ctx.Qualifier) {
				kk.schema = s.Name
			}
		}
		cs.loadCompletion(kk)
		for name := range cs.completion[kk] {
			items = append(items, compItem{label: name, kind: "table", detail: kk.schema})
		}
		if len(items) == 0 {
			// table.column without alias
			for _, c := range tableCols("", ctx.Qualifier) {
				items = append(items, compItem{label: c, kind: "column", detail: ctx.Qualifier})
			}
		}
		return &compRequest{items: items}
	}
	if ctx.ExpectObject {
		for name := range comp {
			items = append(items, compItem{label: name, kind: "table", detail: schema})
		}
		for _, s := range cs.schemas[""] {
			if !s.System {
				items = append(items, compItem{label: s.Name, kind: "schema", detail: "schema"})
			}
		}
		return &compRequest{items: items}
	}
	for _, r := range ctx.Refs {
		if r.Alias != "" {
			items = append(items, compItem{label: r.Alias, kind: "alias", detail: r.Name})
		}
		for _, c := range tableCols(r.Schema, r.Name) {
			items = append(items, compItem{label: c, kind: "column", detail: r.Name})
		}
	}
	for name := range comp {
		items = append(items, compItem{label: name, kind: "table", detail: schema})
	}
	return t.keywordItems(items)
}

func (t *ConsoleTab) keywordItems(items []compItem) *compRequest {
	e := t.ed
	for kw := range e.kw {
		items = append(items, compItem{label: kw, kind: "keyword"})
	}
	for f := range e.funcs {
		items = append(items, compItem{label: f, kind: "function", insert: f + "()"})
	}
	for ty := range e.types {
		items = append(items, compItem{label: ty, kind: "type"})
	}
	return &compRequest{items: items}
}
