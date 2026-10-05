package ui

import (
	"context"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gioui.org/io/clipboard"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/desktop"
	"github.com/giovanni/lumino/internal/history"
	"github.com/giovanni/lumino/internal/prefs"
	"github.com/giovanni/lumino/internal/secrets"
)

func opInvalidateIn(gtx C, d time.Duration) op.InvalidateCmd {
	return op.InvalidateCmd{At: gtx.Now.Add(d)}
}

// shortPath abbreviates the home directory.
func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

// pasteInto pastes the clipboard into an editor (next frame).
func (a *App) pasteInto(ed *CodeEditor) {
	a.pendingPaste = ed
	a.invalidate()
}

func (a *App) duplicateConn(id string) {
	c, ok := a.prefs.Conn(id)
	if !ok {
		return
	}
	n := *c
	n.ID = newID()
	n.Name = c.Name + " (copy)"
	if c.SSH != nil {
		s := *c.SSH
		n.SSH = &s
	}
	if pw, ok := secrets.Get(id, secrets.Password); ok {
		_ = secrets.Set(n.ID, secrets.Password, pw, n.SavePassword)
	}
	if s, ok := secrets.Get(id, secrets.SSH); ok {
		_ = secrets.Set(n.ID, secrets.SSH, s, n.SavePassword)
	}
	a.prefs.PutConn(n)
	a.savePrefs()
	a.openConnDialog(n.ID)
}

func (a *App) deleteConn(id string) {
	c, ok := a.prefs.Conn(id)
	if !ok {
		return
	}
	name := c.Name
	cf := newConfirm(a, "Delete connection", fmt.Sprintf("Delete the connection %q, its saved password, saved queries and history?\nThe database itself is not touched.", name), true, func(bool) {
		for i := len(a.tabs) - 1; i >= 0; i-- {
			if a.tabs[i].ConnID() == id {
				a.removeTab(a.tabs[i])
			}
		}
		a.disconnect(id)
		delete(a.conns, id)
		secrets.DeleteAll(id)
		_ = history.Clear(id)
		a.prefs.RemoveConn(id)
		a.savePrefs()
		a.flash("Deleted "+name, false)
	})
	cf.okLabel = "Delete"
	a.overlay = cf
}

// dropObject drops (or truncates) an object after a policy-aware confirmation.
func (a *App) dropObject(connID string, ref db.ObjectRef, truncate bool) {
	if !a.allowMutation(connID, "Drop") {
		return
	}
	cs := a.connState(connID)
	d := cs.dialect()
	if d == nil {
		return
	}
	var stmt string
	switch {
	case truncate:
		stmt = "TRUNCATE TABLE " + d.Qualified(ref)
	case ref.Type == db.ObjMatView:
		stmt = "DROP MATERIALIZED VIEW " + d.Qualified(ref)
	case ref.Type == db.ObjFunction || ref.Type == db.ObjProcedure:
		stmt = "DROP " + strings.ToUpper(string(ref.Type)) + " " + d.Qualified(ref)
		if ref.Signature != "" && d.Name() == "postgres" {
			stmt += ref.Signature
		}
	default:
		stmt = "DROP " + strings.ToUpper(string(ref.Type)) + " " + d.Qualified(ref)
	}
	verb := "Drop"
	if truncate {
		verb = "Truncate"
	}
	c := newConfirm(a, verb+" "+string(ref.Type), "This runs:\n\n"+stmt+"\n\nIt can't be undone.", true, func(bool) {
		a.releaseCursors(connID)
		conn := cs.conn
		if conn == nil {
			a.flash("Not connected", true)
			return
		}
		a.do(verb+" "+ref.Name, func() error {
			s, err := conn.Session(context.Background())
			if err != nil {
				return err
			}
			defer s.Close()
			res, err := s.Execute(context.Background(), stmt)
			if err == nil {
				res.Close()
			}
			return err
		}, func() {
			cs.refreshSchema(keyOf(ref))
			if !truncate {
				for i := len(a.tabs) - 1; i >= 0; i-- {
					if t, ok := a.tabs[i].(*TableTab); ok && t.connID == connID && t.ref == ref {
						a.removeTab(t)
					}
				}
			}
		})
	})
	c.typeName = ref.Name // always typed: dropping is irreversible
	c.prodCtx = cs.name()
	c.okLabel = verb
	a.overlay = c
}

// generateDML opens a console with INSERT/UPDATE/DELETE templates.
func (a *App) generateDML(connID string, ref db.ObjectRef) {
	cs := a.connState(connID)
	cs.loadTable(ref, func(info *db.TableInfo, err error) {
		if err != nil {
			a.flash(err.Error(), true)
			return
		}
		d := cs.dialect()
		var cols, params, sets []string
		for i, c := range info.Columns {
			if c.Generated != "" {
				continue
			}
			cols = append(cols, d.Quote(c.Name))
			params = append(params, d.Placeholder(i+1))
			if c.PrimaryKeyNo == 0 {
				sets = append(sets, d.Quote(c.Name)+" = "+d.Placeholder(i+1))
			}
		}
		var keys []string
		for i, k := range info.PrimaryKey {
			keys = append(keys, d.Quote(k)+" = "+d.Placeholder(len(info.Columns)+i+1))
		}
		where := strings.Join(keys, " AND ")
		if where == "" {
			where = "/* no primary key: add a condition */"
		}
		name := d.Qualified(ref)
		var b strings.Builder
		fmt.Fprintf(&b, "SELECT %s\nFROM %s;\n\n", strings.Join(cols, ", "), name)
		fmt.Fprintf(&b, "INSERT INTO %s (%s)\nVALUES (%s);\n\n", name, strings.Join(cols, ", "), strings.Join(params, ", "))
		if len(sets) > 0 {
			fmt.Fprintf(&b, "UPDATE %s\nSET %s\nWHERE %s;\n\n", name, strings.Join(sets, ",\n    "), where)
		}
		fmt.Fprintf(&b, "DELETE FROM %s\nWHERE %s;\n", name, where)
		a.newConsole(connID, ref.Schema, b.String())
	})
}

// ---- SQL review (grid edits) ----

// SQLReview shows generated statements before applying them.
type SQLReview struct {
	app        *App
	connID     string
	title      string
	warn       string
	n          int
	ed         *CodeEditor
	apply      func()
	done       bool
	typed      widget.Editor
	ok, cancel widget.Clickable
	d          dialog
}

func newSQLReview(a *App, connID, title, sql, warn string, n int, apply func()) *SQLReview {
	r := &SQLReview{app: a, connID: connID, title: title, warn: warn, n: n, apply: apply}
	r.ed = newCodeEditor(a, sql)
	r.ed.ReadOnly = true
	if d := a.connState(connID).dialect(); d != nil {
		r.ed.setDialect(d)
	}
	r.ed.SetCaretOffset(0)
	r.typed.SingleLine = true
	return r
}

func (r *SQLReview) Done() bool { return r.done }

func (r *SQLReview) Layout(gtx C) D {
	a, th := r.app, r.app.th
	protected := a.connProtected(r.connID)
	name := a.connState(r.connID).name()
	armed := !protected || strings.TrimSpace(r.typed.Text()) == name
	if r.cancel.Clicked(gtx) || r.d.close.Clicked(gtx) {
		r.done = true
	}
	if r.ok.Clicked(gtx) && armed {
		r.done = true
		r.apply()
	}
	return r.d.layout(gtx, th, 860, icSave, r.title, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return lbl(gtx, th, fontSans, 12.5, pal.Text, fmt.Sprintf("%d statement(s), applied in one transaction where supported:", r.n))
			}),
			layout.Rigid(layout.Spacer{Height: 8}.Layout),
			layout.Rigid(func(gtx C) D {
				h := min(gtx.Dp(360), max(gtx.Dp(120), r.ed.buf.lineCount()*gtx.Dp(21)+gtx.Dp(30)))
				gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, h))
				return rborder(gtx, pal.Border, pal.CodeBg, 6, func(gtx C) D { return inset(1).Layout(gtx, r.ed.Layout) })
			}),
			layout.Rigid(func(gtx C) D {
				if r.warn == "" {
					return D{}
				}
				return layout.Inset{Top: 10}.Layout(gtx, func(gtx C) D {
					l := material.Body2(th, "⚠ "+r.warn)
					l.Color, l.TextSize = pal.Warn, 12
					return l.Layout(gtx)
				})
			}),
			layout.Rigid(func(gtx C) D {
				if !protected {
					return D{}
				}
				return layout.Inset{Top: 12}.Layout(gtx, func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							return iconText(gtx, th, icWarning, 12, pal.Err, "Protected connection: type "+name+" to confirm")
						}),
						layout.Rigid(layout.Spacer{Height: 6}.Layout),
						layout.Rigid(func(gtx C) D { return textField(gtx, th, &r.typed, name, true) }),
					)
				})
			}),
		)
	}, footerButtons(nil,
		func(gtx C) D { return pillButton(gtx, th, &r.cancel, nil, "Cancel", pal.Text, pal.Hover) },
		func(gtx C) D {
			c := pal.Accent
			if !armed {
				c = pal.Border
			}
			return button(gtx, th, &r.ok, "Apply", c)
		},
	))
}

// ---- history / saved queries ----

func (a *App) openHistory(connID string) {
	entries, err := history.Load(connID)
	if err != nil {
		a.flash("History: "+err.Error(), true)
		return
	}
	if len(entries) == 0 {
		a.flash("No history yet for "+a.connState(connID).name(), false)
		return
	}
	items := make([]string, len(entries))
	for i, e := range entries {
		sql := strings.Join(strings.Fields(e.SQL), " ")
		if len(sql) > 140 {
			sql = sql[:140] + "…"
		}
		info := e.At.Format("Jan 2 15:04") + " · " + fmtDur(time.Duration(e.Millis)*time.Millisecond)
		if e.Error != "" {
			info += " · error"
		} else if e.Rows >= 0 {
			info += " · " + itoa(int(e.Rows)) + " rows"
		}
		items[i] = sql + "  ·  " + info
	}
	p := newPicker(a, "History of "+a.connState(connID).name()+" (Enter opens the statement in the console)", items, false, func(item string) {
		for i, it := range items {
			if it == item {
				a.insertInConsole(connID, entries[i].SQL, entries[i].Schema)
				return
			}
		}
	})
	p.icon = icHistory
	a.overlay = p
}

// insertInConsole appends SQL to the active console of the connection
// (or opens a new one).
func (a *App) insertInConsole(connID, sql, schema string) {
	if t, ok := a.activeTab().(*ConsoleTab); ok && t.connID == connID {
		ed := t.ed
		end := ed.buf.end()
		ed.caret, ed.anchor = end, end
		prefix := ""
		if strings.TrimSpace(ed.Text()) != "" {
			prefix = "\n\n"
		}
		ed.Insert(prefix + strings.TrimSpace(sql) + ";")
		return
	}
	a.newConsole(connID, schema, strings.TrimSpace(sql)+";\n")
}

// SaveQueryDialog names a saved query.
type SaveQueryDialog struct {
	app     *App
	connID  string
	sql     string
	name    widget.Editor
	anyConn bool
	anyBtn  widget.Clickable
	ok      widget.Clickable
	cancel  widget.Clickable
	done    bool
	focused bool
	d       dialog
}

func (a *App) saveQueryDialog(connID, sql string) {
	if strings.TrimSpace(sql) == "" {
		a.flash("Nothing to save", false)
		return
	}
	d := &SaveQueryDialog{app: a, connID: connID, sql: sql}
	d.name.SingleLine, d.name.Submit = true, true
	a.overlay = d
}

func (d *SaveQueryDialog) Done() bool { return d.done }

func (d *SaveQueryDialog) save() {
	name := strings.TrimSpace(d.name.Text())
	if name == "" {
		return
	}
	q := prefs.SavedQuery{Name: name, ConnID: d.connID, SQL: d.sql}
	if d.anyConn {
		q.ConnID = ""
	}
	d.app.prefs.SaveQuery(q)
	d.app.savePrefs()
	d.app.flash("Saved query "+name, false)
	d.done = true
}

func (d *SaveQueryDialog) Layout(gtx C) D {
	th := d.app.th
	if !d.focused {
		d.focused = true
		gtx.Execute(key.FocusCmd{Tag: &d.name})
	}
	for {
		ev, ok := d.name.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			d.save()
		}
	}
	if d.ok.Clicked(gtx) {
		d.save()
	}
	if d.cancel.Clicked(gtx) || d.d.close.Clicked(gtx) {
		d.done = true
	}
	if d.anyBtn.Clicked(gtx) {
		d.anyConn = !d.anyConn
	}
	return d.d.layout(gtx, th, 460, icBookmark, "Save query", func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return formRow(gtx, th, "Name", 70, func(gtx C) D { return textField(gtx, th, &d.name, "monthly report", false) })
			}),
			layout.Rigid(func(gtx C) D { return toggle(gtx, th, &d.anyBtn, "Available on every connection", d.anyConn) }),
		)
	}, footerButtons(nil,
		func(gtx C) D { return pillButton(gtx, th, &d.cancel, nil, "Cancel", pal.Text, pal.Hover) },
		func(gtx C) D { return button(gtx, th, &d.ok, "Save", pal.Accent) },
	))
}

// openSavedQueries lists saved queries of the current connection.
func (a *App) openSavedQueries() {
	id := a.currentConn()
	qs := a.prefs.QueriesFor(id)
	if len(qs) == 0 {
		a.flash("No saved queries (save one from a console toolbar)", false)
		return
	}
	items := make([]string, len(qs))
	for i, q := range qs {
		items[i] = q.Name + "  ·  " + strings.Join(strings.Fields(q.SQL), " ")
	}
	p := newPicker(a, "Saved queries", items, false, func(item string) {
		for i, it := range items {
			if it == item {
				if id == "" {
					a.flash("Select a connection first", true)
					return
				}
				a.insertInConsole(id, qs[i].SQL, "")
				return
			}
		}
	})
	p.icon = icBookmark
	a.overlay = p
}

// ---- command palette ----

type paletteCmd struct {
	label string
	icon  *widget.Icon
	run   func()
}

func (a *App) openPalette() {
	var cmds []paletteCmd
	add := func(ic *widget.Icon, label string, f func()) { cmds = append(cmds, paletteCmd{label, ic, f}) }
	cur := a.currentConn()
	if cur != "" {
		add(icConsole, "New console  ·  "+a.connState(cur).name(), func() { a.newConsole(cur, a.currentSchema(), "") })
		add(icHistory, "Query history  ·  "+a.connState(cur).name(), func() { a.openHistory(cur) })
	}
	add(icBookmark, "Saved queries", a.openSavedQueries)
	add(icAdd, "New connection…", func() { a.openConnDialog("") })
	for _, c := range a.prefs.Connections {
		id := c.ID
		add(icDatabase, "Connect  ·  "+c.Name, func() {
			a.sidebar.expand(connKey(id), true)
			a.connect(id, func(cs *connState) { a.newConsole(id, cs.defaultSchema(""), "") })
		})
	}
	// cached objects of connected databases: quick open
	var objs []paletteCmd
	for _, c := range a.prefs.Connections {
		cs, ok := a.conns[c.ID]
		if !ok || cs.status != stReady {
			continue
		}
		id := c.ID
		for k, list := range cs.objects {
			for _, o := range list {
				ref := o.Ref
				label := ref.Name + "  ·  " + strings.TrimPrefix(k.catalog+"."+k.schema, ".") + " · " + string(ref.Type) + " · " + c.Name
				view := "data"
				if !ref.Type.Relation() {
					view = "ddl"
				}
				objs = append(objs, paletteCmd{label, objIcon(ref.Type), func() { a.openObject(id, ref, view) }})
			}
		}
	}
	sort.Slice(objs, func(i, j int) bool { return objs[i].label < objs[j].label })
	cmds = append(cmds, objs...)
	for _, t := range Themes {
		id := t.ID
		add(icSun, "Theme  ·  "+t.Name, func() { a.setThemeName(id) })
	}
	add(icSettings, "Preferences", func() { a.overlay = newPrefsPanel(a) })
	add(icAI, "Assistant", func() { a.ai.open = !a.ai.open })
	add(icDownload, "Check for updates", func() { a.checkUpdates(true) })
	items := make([]string, len(cmds))
	icons := make([]*widget.Icon, len(cmds))
	for i, c := range cmds {
		items[i], icons[i] = c.label, c.icon
	}
	p := newPicker(a, "Commands, connections and objects", items, false, func(item string) {
		for _, c := range cmds {
			if c.label == item {
				c.run()
				return
			}
		}
	})
	p.itemIcons = icons
	a.overlay = p
}

// handleKeys implements global shortcuts.
func (a *App) handleKeys(gtx C) {
	if a.pendingPaste != nil {
		gtx.Execute(clipboard.ReadCmd{Tag: a.pendingPaste})
		a.pendingPaste = nil
	}
	for {
		ev, ok := gtx.Event(
			key.Filter{Name: key.NameEscape},
			key.Filter{Name: "K", Required: key.ModShortcut},
			key.Filter{Name: "P", Required: key.ModShortcut, Optional: key.ModShift},
			key.Filter{Name: "T", Required: key.ModShortcut},
			key.Filter{Name: "W", Required: key.ModShortcut},
			key.Filter{Name: "N", Required: key.ModShortcut | key.ModShift},
			key.Filter{Name: key.NameTab, Required: key.ModCtrl, Optional: key.ModShift},
			key.Filter{Name: ",", Required: key.ModShortcut},
			key.Filter{Name: "S", Required: key.ModShortcut},
			key.Filter{Name: "R", Required: key.ModShortcut},
			key.Filter{Name: "J", Required: key.ModShortcut},
			key.Filter{Name: key.NameF5},
		)
		if !ok {
			break
		}
		e, ok := ev.(key.Event)
		if !ok || e.State != key.Press {
			continue
		}
		a.onKey(gtx, e)
	}
}

func (a *App) onKey(gtx C, e key.Event) {
	short := e.Modifiers.Contain(key.ModShortcut)
	switch {
	case e.Name == key.NameEscape:
		a.escape(gtx)
	case a.overlay != nil:
		return
	case short && (e.Name == "K" || e.Name == "P"):
		a.openPalette()
	case short && e.Name == "T":
		if id := a.currentConn(); id != "" {
			a.newConsole(id, a.currentSchema(), "")
		} else {
			a.flash("Select a connection first", true)
		}
	case short && e.Name == "N":
		a.openConnDialog("")
	case short && e.Name == "W":
		a.closeTab(a.tabIdx)
	case e.Name == key.NameTab:
		if e.Modifiers.Contain(key.ModShift) {
			a.cycleTab(-1)
		} else {
			a.cycleTab(1)
		}
	case short && e.Name == ",":
		a.overlay = newPrefsPanel(a)
	case short && e.Name == "S":
		if t, ok := a.activeTab().(*TableTab); ok {
			t.review()
		} else if t, ok := a.activeTab().(*ConsoleTab); ok {
			a.saveQueryDialog(t.connID, t.ed.Text())
		}
	case short && e.Name == "J":
		a.ai.open = !a.ai.open
	case short && e.Name == "R" || e.Name == key.NameF5:
		switch t := a.activeTab().(type) {
		case *TableTab:
			t.refresh(nil)
		case *ConsoleTab:
			t.run(false)
		}
	}
}

func (a *App) escape(gtx C) {
	switch {
	case a.overlay != nil:
		a.dismissOverlay(gtx)
	case a.selWasFocused || a.lineSelActive():
		gtx.Execute(key.FocusCmd{})
		a.clearLineSels()
	default:
		if t, ok := a.activeTab().(*ConsoleTab); ok && t.running {
			t.cancel()
		}
	}
}

// ---- policy frame ----

// layoutPolicyFrame outlines the window while a protected connection is
// the active tab.
func (a *App) layoutPolicyFrame(gtx C) {
	t := a.activeTab()
	if t == nil || !a.connProtected(t.ConnID()) {
		return
	}
	sz := gtx.Constraints.Max
	th := gtx.Dp(2)
	c := pal.Err
	fillRect(gtx, c, image.Rect(0, 0, sz.X, th))
	fillRect(gtx, c, image.Rect(0, sz.Y-th, sz.X, sz.Y))
	fillRect(gtx, c, image.Rect(0, 0, th, sz.Y))
	fillRect(gtx, c, image.Rect(sz.X-th, 0, sz.X, sz.Y))
}

// ---- welcome ----

type welcome struct {
	newBtn  widget.Clickable
	connBtn []widget.Clickable
}

func (w *welcome) layout(gtx C, a *App) D {
	th := a.th
	if w.newBtn.Clicked(gtx) {
		a.openConnDialog("")
	}
	conns := a.prefs.Connections
	if len(w.connBtn) < len(conns) {
		w.connBtn = make([]widget.Clickable, len(conns)+4)
	}
	for i, c := range conns {
		if w.connBtn[i].Clicked(gtx) {
			id := c.ID
			a.sidebar.expand(connKey(id), true)
			a.connect(id, func(cs *connState) { a.newConsole(id, cs.defaultSchema(""), "") })
		}
	}
	return layout.Center.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return drawLogo(gtx, 72) }),
			layout.Rigid(layout.Spacer{Height: 12}.Layout),
			layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontSans, 28, pal.TextBright, AppName) }),
			layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontSans, 13, pal.TextDim, "a fast, native database IDE") }),
			layout.Rigid(layout.Spacer{Height: 18}.Layout),
			layout.Rigid(func(gtx C) D {
				return pillButton(gtx, th, &w.newBtn, icAdd, "New connection", pal.OnAccent, pal.Accent)
			}),
			layout.Rigid(layout.Spacer{Height: 18}.Layout),
			layout.Rigid(func(gtx C) D {
				if len(conns) == 0 {
					return D{}
				}
				var ch []layout.FlexChild
				ch = append(ch, layout.Rigid(func(gtx C) D {
					return layout.Inset{Bottom: 6}.Layout(gtx, func(gtx C) D { return lbl(gtx, th, fontBold, 11, pal.TextDim, "OPEN A CONNECTION") })
				}))
				for i, c := range conns {
					if i == 8 {
						break
					}
					i, c := i, c
					ch = append(ch, layout.Rigid(func(gtx C) D {
						return w.connBtn[i].Layout(gtx, func(gtx C) D {
							b := colorNRGBA{}
							if w.connBtn[i].Hovered() {
								b = pal.Hover
							}
							return rbg(gtx, b, 6, func(gtx C) D {
								gtx.Constraints.Min.X = gtx.Dp(320)
								return insetHV(10, 6).Layout(gtx, func(gtx C) D {
									return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
										layout.Rigid(func(gtx C) D { return mdi(gtx, icDatabase, a.connColor(c.ID), 16) }),
										layout.Rigid(layout.Spacer{Width: 8}.Layout),
										layout.Flexed(1, func(gtx C) D { return lbl(gtx, th, fontSans, 13, pal.Text, c.Name) }),
										layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontSans, 11, pal.TextDim, driverName(c.Driver)) }),
									)
								})
							})
						})
					}))
				}
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, ch...)
			}),
			layout.Rigid(layout.Spacer{Height: 18}.Layout),
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return keyHint(gtx, th, shortcutLabel("K"), "commands") }),
					layout.Rigid(layout.Spacer{Width: 14}.Layout),
					layout.Rigid(func(gtx C) D { return keyHint(gtx, th, shortcutLabel("Shift N"), "new connection") }),
				)
			}),
		)
	})
}

func driverName(id string) string {
	if d, ok := db.Lookup(id); ok {
		return d.Info().Name
	}
	return id
}

// keyEvent builds a synthetic key press.
func keyEvent(name key.Name) key.Event {
	return key.Event{Name: name, Modifiers: key.ModShortcut | key.ModShift, State: key.Press}
}

var _ = filepath.Join

func copyText(gtx C, s string) {
	gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(s))})
}

// openBrowser opens url with the desktop handler.
func openBrowser(url string) error { return desktop.Open(url) }
