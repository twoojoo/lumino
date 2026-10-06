package ui

import (
	"image"
	"strings"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/widget"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/prefs"
)

// Tab is a main-area page bound to a connection.
type Tab interface {
	Title() string
	Icon() *widget.Icon
	ConnID() string
	Layout(gtx C) D
	// Focused reports whether one of its text inputs has the keyboard.
	Focused(gtx C) bool
	// Dirty explains why closing would lose work ("" = nothing to lose).
	Dirty() string
	// StatusInfo is shown in the status bar while the tab is active.
	StatusInfo() string
	// State is persisted to restore the workspace (Kind "" = don't).
	State() prefs.TabState
	// Disconnected: the connection was closed under the tab.
	Disconnected()
	// Dispose releases the tab's sessions.
	Dispose()
}

func (a *App) activeTab() Tab {
	if a.tabIdx < 0 || a.tabIdx >= len(a.tabs) {
		return nil
	}
	return a.tabs[a.tabIdx]
}

func (a *App) findTab(match func(Tab) bool) int {
	for i, t := range a.tabs {
		if match(t) {
			return i
		}
	}
	return -1
}

func (a *App) addTab(t Tab) {
	a.tabs = append(a.tabs, t)
	a.tabIdx = len(a.tabs) - 1
	a.scheduleSave()
	a.invalidate()
}

// closeTab asks before discarding work.
func (a *App) closeTab(i int) {
	if i < 0 || i >= len(a.tabs) {
		return
	}
	a.closeTabs([]Tab{a.tabs[i]})
}

// closeTabs closes tabs one after the other. For each one it asks before
// discarding open transactions or unsaved edits, and offers to save an
// unsaved console query; Cancel stops the whole sequence.
func (a *App) closeTabs(ts []Tab) {
	for len(ts) > 0 {
		t, rest := ts[0], ts[1:]
		if a.findTab(func(x Tab) bool { return x == t }) < 0 {
			ts = rest
			continue
		}
		if why := t.Dirty(); why != "" {
			c := newConfirm(a, "Close tab?", why+"\n\nClose "+t.Title()+" anyway?", true, func(bool) {
				if !a.askSaveQuery(t, rest) {
					a.removeTab(t)
					a.closeTabs(rest)
				}
			})
			c.okLabel = "Discard and close"
			a.overlay = c
			return
		}
		if a.askSaveQuery(t, rest) {
			return
		}
		a.removeTab(t)
		ts = rest
	}
}

// askSaveQuery offers to save the query of a console being closed (then
// closes it and continues with rest). It reports whether it asked.
func (a *App) askSaveQuery(t Tab, rest []Tab) bool {
	ct, ok := t.(*ConsoleTab)
	if !ok {
		return false
	}
	sql, unsaved := ct.unsavedQuery()
	if !unsaved {
		return false
	}
	closeAndGo := func() {
		a.removeTab(t)
		a.closeTabs(rest)
	}
	if i := a.findTab(func(x Tab) bool { return x == t }); i >= 0 {
		a.tabIdx = i // show the query being asked about
	}
	c := newConfirm(a, "Save the query?", "The query in "+ct.Title()+" isn't saved.\nSave it before closing the tab?", false, func(bool) {
		a.saveQueryDialogThen(ct.connID, sql, ct.queryName(), closeAndGo, nil)
	})
	c.okLabel = "Save…"
	c.altLabel = "Don't save"
	c.onAlt = closeAndGo
	a.overlay = c
	return true
}

// tabMenu is the context menu of tab i.
func (a *App) tabMenu(i int) {
	if i < 0 || i >= len(a.tabs) {
		return
	}
	t := a.tabs[i]
	all := append([]Tab(nil), a.tabs...)
	var others []Tab
	for j, x := range all {
		if j != i {
			others = append(others, x)
		}
	}
	right := append([]Tab(nil), all[i+1:]...)
	items := []*menuItem{
		{icon: icClose, label: "Close", hint: shortcutLabel("W"), action: func() { a.closeTabs([]Tab{t}) }},
		{icon: icClose, label: "Close others", action: func() { a.closeTabs(others) }},
		{icon: icClose, label: "Close tabs to the right", action: func() { a.closeTabs(right) }},
		{icon: icClose, label: "Close all", action: func() { a.closeTabs(all) }},
	}
	switch x := t.(type) {
	case *ConsoleTab:
		items = append(items, &menuItem{sep: true},
			&menuItem{icon: icBookmark, label: "Save query…", action: func() {
				a.saveQueryDialogThen(x.connID, x.ed.Text(), x.queryName(), nil, nil)
			}},
			&menuItem{icon: icAdd, label: "Duplicate console", action: func() {
				a.newConsole(x.connID, x.schema, x.ed.Text())
			}},
		)
	case *TableTab:
		items = append(items, &menuItem{sep: true},
			&menuItem{icon: icRefresh, label: "Refresh", action: func() { x.refresh(nil) }},
		)
	}
	a.overlay = &ContextMenu{app: a, at: a.pointer, title: t.Title(), items: items, sel: -1}
}

func (a *App) removeTab(t Tab) {
	i := a.findTab(func(x Tab) bool { return x == t })
	if i < 0 {
		return
	}
	t.Dispose()
	a.tabs = append(a.tabs[:i], a.tabs[i+1:]...)
	if i < len(a.tabBtns) {
		a.tabBtns = append(a.tabBtns[:i], a.tabBtns[i+1:]...)
		a.tabClose = append(a.tabClose[:i], a.tabClose[i+1:]...)
		a.tabTags = append(a.tabTags[:i], a.tabTags[i+1:]...)
	}
	if i < a.tabIdx || a.tabIdx >= len(a.tabs) {
		a.tabIdx--
	}
	a.tabIdx = max(0, a.tabIdx)
	a.scheduleSave()
}

func (a *App) cycleTab(d int) {
	if n := len(a.tabs); n > 1 {
		a.tabIdx = (a.tabIdx + d + n) % n
	}
}

// currentConn is the connection of the active tab, else the one selected
// in the sidebar.
func (a *App) currentConn() string {
	if t := a.activeTab(); t != nil {
		return t.ConnID()
	}
	return a.sidebar.selectedConn()
}

// newConsole opens a SQL console on a connection.
func (a *App) newConsole(connID, schema, text string) *ConsoleTab {
	t := newConsoleTab(a, prefs.TabState{Kind: "console", ConnID: connID, Schema: schema, Text: text})
	a.addTab(t)
	return t
}

// openObject opens a relation's data (or a routine's DDL), reusing a tab
// already showing it.
func (a *App) openObject(connID string, ref db.ObjectRef, view string) {
	if i := a.findTab(func(t Tab) bool {
		tt, ok := t.(*TableTab)
		return ok && tt.connID == connID && tt.ref == ref
	}); i >= 0 {
		a.tabIdx = i
		if view != "" {
			a.tabs[i].(*TableTab).show(view)
		}
		return
	}
	t := newTableTab(a, prefs.TabState{Kind: "table", ConnID: connID, Ref: ref})
	if view != "" {
		t.show(view)
	}
	a.addTab(t)
}

// restoreTabs reopens the saved workspace (connections connect lazily).
func (a *App) restoreTabs() {
	active := 0
	for i, st := range a.prefs.Tabs {
		if _, ok := a.prefs.Conn(st.ConnID); !ok {
			continue // its connection was deleted
		}
		switch st.Kind {
		case "console":
			a.tabs = append(a.tabs, newConsoleTab(a, st))
		case "table":
			a.tabs = append(a.tabs, newTableTab(a, st))
		default:
			continue
		}
		if i <= a.prefs.ActiveTab {
			active = len(a.tabs) - 1 // the saved active tab, or the closest before it
		}
	}
	a.tabIdx = max(0, min(active, len(a.tabs)-1))
}

// saveWorkspace persists open tabs and console text.
func (a *App) saveWorkspace() {
	var st []prefs.TabState
	for _, t := range a.tabs {
		if s := t.State(); s.Kind != "" {
			st = append(st, s)
		}
	}
	a.prefs.Tabs, a.prefs.ActiveTab = st, a.tabIdx
	a.prefs.Expanded = a.sidebar.expandedKeys()
	a.savePrefs()
}

// layoutTabs draws the tab strip.
func (a *App) layoutTabs(gtx C) D {
	th := a.th
	h := gtx.Dp(34)
	if len(a.tabBtns) < len(a.tabs) {
		a.tabBtns = append(a.tabBtns, make([]widget.Clickable, len(a.tabs)-len(a.tabBtns)+4)...)
		a.tabClose = append(a.tabClose, make([]widget.Clickable, len(a.tabs)-len(a.tabClose)+4)...)
		a.tabTags = append(a.tabTags, make([]int, len(a.tabs)-len(a.tabTags)+4)...)
	}
	for i := range a.tabs {
		if a.tabBtns[i].Clicked(gtx) {
			a.tabIdx = i
		}
	}
	for i := range a.tabs {
		if a.tabClose[i].Clicked(gtx) {
			a.closeTab(i)
			break
		}
	}
	a.middleClickTabs(gtx)
	if a.newTabBtn.Clicked(gtx) {
		if id := a.currentConn(); id != "" {
			a.newConsole(id, a.currentSchema(), "")
		}
	}
	sz := image.Pt(gtx.Constraints.Max.X, h)
	fillRect(gtx, pal.Header, image.Rectangle{Max: sz})
	fillRect(gtx, pal.Border, image.Rect(0, h-gtx.Dp(1), sz.X, h))
	gtx.Constraints = layout.Exact(sz)
	var items []layout.FlexChild
	n := len(a.tabs)
	// tabs shrink to share the strip once they don't fit
	maxW := gtx.Dp(220)
	if n > 0 {
		maxW = max(gtx.Dp(90), min(maxW, (sz.X-gtx.Dp(40))/n))
	}
	for i, t := range a.tabs {
		i, t := i, t
		items = append(items, layout.Rigid(func(gtx C) D {
			active := i == a.tabIdx
			gtx.Constraints.Max.X = maxW
			m := op.Record(gtx.Ops)
			dims := layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D {
					return a.tabBtns[i].Layout(gtx, func(gtx C) D {
						c := pal.TextDim
						if active || a.tabBtns[i].Hovered() {
							c = pal.TextBright
						}
						return layout.Inset{Left: 12, Right: 4, Top: 8, Bottom: 8}.Layout(gtx, func(gtx C) D {
							return iconText(gtx, th, t.Icon(), 12, c, t.Title())
						})
					})
				}),
				layout.Rigid(func(gtx C) D {
					// the dot of a dirty tab turns into the close button when
					// the tab is hovered (the button gets no hover while hidden)
					if t.Dirty() != "" && !a.tabClose[i].Hovered() && !a.tabBtns[i].Hovered() {
						return layout.Inset{Right: 9, Left: 3}.Layout(gtx, func(gtx C) D { return dot(gtx, pal.Accent, 8) })
					}
					return layout.Inset{Right: 6}.Layout(gtx, func(gtx C) D { return iconOnly(gtx, &a.tabClose[i], icClose, 12) })
				}),
			)
			call := m.Stop()
			w := dims.Size.X
			if active {
				fillRect(gtx, pal.Bg, image.Rect(0, 0, w, h))
			}
			// connection color, red when protected
			c := a.connColor(t.ConnID())
			if active {
				fillRect(gtx, c, image.Rect(0, 0, w, gtx.Dp(2)))
			} else {
				fillRect(gtx, withAlpha(c, 0x80), image.Rect(0, 0, w, gtx.Dp(1)))
			}
			fillRect(gtx, pal.Border, image.Rect(w-gtx.Dp(1), gtx.Dp(6), w, h-gtx.Dp(6)))
			call.Add(gtx.Ops)
			// middle click closes (passing the other clicks to the buttons)
			pass := pointer.PassOp{}.Push(gtx.Ops)
			area := clip.Rect{Max: image.Pt(w, h)}.Push(gtx.Ops)
			event.Op(gtx.Ops, &a.tabTags[i])
			area.Pop()
			pass.Pop()
			return D{Size: image.Pt(w, h)}
		}))
	}
	items = append(items, layout.Rigid(func(gtx C) D {
		return layout.Inset{Left: 4, Top: 6}.Layout(gtx, func(gtx C) D { return iconOnly(gtx, &a.newTabBtn, icAdd, 16) })
	}))
	return layout.Flex{Alignment: layout.Start}.Layout(gtx, items...)
}

// middleClickTabs closes a tab on middle click.
func (a *App) middleClickTabs(gtx C) {
	for i := range min(len(a.tabs), len(a.tabTags)) {
		for {
			ev, ok := gtx.Event(pointer.Filter{Target: &a.tabTags[i], Kinds: pointer.Press})
			if !ok {
				break
			}
			if pe, ok := ev.(pointer.Event); ok && pe.Buttons == pointer.ButtonTertiary {
				a.closeTab(i)
				return
			}
			if pe, ok := ev.(pointer.Event); ok && pe.Buttons == pointer.ButtonSecondary {
				a.tabMenu(i)
				return
			}
		}
	}
}

// connColor is the accent of a connection: red when protected, else its
// configured color, else the theme accent.
func (a *App) connColor(id string) colorNRGBA {
	c, ok := a.prefs.Conn(id)
	if !ok {
		return pal.Border
	}
	if a.connProtected(id) {
		return pal.Err
	}
	if col, ok := parseHex(c.Color); ok {
		return col
	}
	return pal.Accent
}

// currentSchema is the schema of the active console or the selection.
func (a *App) currentSchema() string {
	if t, ok := a.activeTab().(*ConsoleTab); ok {
		return t.schema
	}
	return a.sidebar.selectedSchema()
}

// requestClose asks before quitting with open transactions or unsaved
// edits, then runs close.
// requestDisconnect disconnects id, asking first when its tabs hold open
// transactions or unsaved edits; then runs afterwards (may be nil).
func (a *App) requestDisconnect(id string, then func()) {
	var why []string
	for _, t := range a.tabs {
		if t.ConnID() != id {
			continue
		}
		if d := t.Dirty(); d != "" {
			why = append(why, "• "+t.Title()+": "+d)
		}
	}
	do := func() {
		a.disconnect(id)
		if then != nil {
			then()
		}
	}
	if len(why) == 0 {
		do()
		return
	}
	c := newConfirm(a, "Disconnect "+a.connState(id).name()+"?", strings.Join(why, "\n")+"\n\nOpen transactions are rolled back and running statements cancelled.", true, func(bool) { do() })
	c.okLabel = "Disconnect"
	a.overlay = c
}

func (a *App) requestClose(close func()) {
	var why []string
	for _, t := range a.tabs {
		if d := t.Dirty(); d != "" {
			why = append(why, "• "+t.Title()+": "+d)
		}
	}
	if len(why) == 0 {
		close()
		return
	}
	c := newConfirm(a, "Quit "+AppName+"?", strings.Join(why, "\n")+"\n\nOpen transactions are rolled back.", true, func(bool) { close() })
	c.okLabel = "Quit anyway"
	a.overlay = c
}
