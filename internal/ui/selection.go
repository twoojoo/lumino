package ui

import (
	"fmt"
	"image"
	"strings"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/giovanni/lumino/internal/backend"
)

// ---- multi-selection ----

func (v *ResourceView) mark(id string, on bool) {
	if v.marked == nil {
		v.marked = map[string]bool{}
	}
	if on {
		v.marked[id] = true
	} else {
		delete(v.marked, id)
	}
}

func (v *ResourceView) toggleMark(id string) { v.mark(id, !v.marked[id]) }

func (v *ResourceView) clearMarks() { v.marked = nil }

func (v *ResourceView) markAll() {
	if v.snap == nil {
		return
	}
	for _, r := range v.snap.Rows {
		v.mark(r.ID, true)
	}
}

// markedIDs returns marked rows still present, in table order.
func (v *ResourceView) markedIDs() []string {
	if v.snap == nil || len(v.marked) == 0 {
		return nil
	}
	var out []string
	for _, r := range v.snap.Rows {
		if v.marked[r.ID] {
			out = append(out, r.ID)
		}
	}
	return out
}

// targets is the bulk selection, or the selected row.
func (v *ResourceView) targets() []string {
	if ids := v.markedIDs(); len(ids) > 0 {
		return ids
	}
	if r, ok := v.selected(); ok {
		return []string{r.ID}
	}
	return nil
}

// bulk runs fn on every id, reporting one summary.
func (a *App) bulk(verb string, ids []string, fn func(id string) error) {
	desc := fmt.Sprintf("%s %d resources", verb, len(ids))
	if len(ids) == 1 {
		desc = verb + " " + ids[0]
	}
	a.do(desc, func() error {
		var failed []string
		for _, id := range ids {
			if err := fn(id); err != nil {
				failed = append(failed, shortName(id)+": "+err.Error())
			}
		}
		if len(failed) > 0 {
			return fmt.Errorf("%d/%d failed — %s", len(failed), len(ids), strings.Join(failed, "; "))
		}
		return nil
	})
}

// confirmBulk confirms an action on several resources; protected contexts
// require typing the context name.
func (a *App) confirmBulk(verb string, ids []string, danger bool, fn func(id string, force bool) error) {
	if !a.allowMutation(verb) || len(ids) == 0 {
		return
	}
	list := ids
	if len(list) > 8 {
		list = append(append([]string{}, ids[:8]...), fmt.Sprintf("… and %d more", len(ids)-8))
	}
	msg := fmt.Sprintf("%s %d resources?\n\n%s", verb, len(ids), strings.Join(list, "\n"))
	c := newConfirm(a, verb, msg, danger, func(force bool) {
		a.bulk(verb, ids, func(id string) error { return fn(id, force) })
	})
	if a.protected() {
		c.typeName, c.prodCtx, c.danger = a.cluster.Context, a.cluster.Context, true
	}
	a.overlay = c
}

func (v *ResourceView) bulkDelete() {
	ids, gvr, c := v.targets(), v.gvr, v.app.cluster
	switch len(ids) {
	case 0:
		return
	case 1: // the marked row if any, else the cursor row
		v.confirmDeleteID(ids[0])
		return
	}
	if isHelm(gvr) {
		v.app.confirmBulk("Uninstall", ids, true, func(id string, _ bool) error { return c.HelmUninstall(id) })
		return
	}
	if !backend.CanVerb(gvr, "delete") {
		return
	}
	v.app.confirmBulk("Delete", ids, true, func(id string, force bool) error { return c.Delete(gvr, id, force) })
}

func (v *ResourceView) bulkRestart() {
	ids, gvr, c := v.targets(), v.gvr, v.app.cluster
	switch len(ids) {
	case 0:
		return
	case 1:
		v.restartID(ids[0])
		return
	}
	if c.CanRestart(gvr) {
		v.app.confirmBulk("Restart", ids, false, func(id string, _ bool) error { return c.Restart(gvr, id) })
	}
}

func (v *ResourceView) bulkCordon(on bool) {
	ids, c := v.targets(), v.app.cluster
	verb := "Cordon"
	if !on {
		verb = "Uncordon"
	}
	v.app.confirmBulk(verb, ids, on, func(id string, _ bool) error { return c.Cordon(id, on) })
}

// ---- context menu ----

type menuItem struct {
	icon    *widget.Icon
	label   string
	hint    string
	danger  bool
	sep     bool
	action  func()
	clicker widget.Clickable
}

// ContextMenu is a small popup anchored at the pointer.
type ContextMenu struct {
	app   *App
	at    image.Point
	title string
	items []*menuItem
	done  bool
	sel   int
}

func (m *ContextMenu) Done() bool { return m.done }

// anchor implements positioned overlays.
func (m *ContextMenu) anchor() image.Point { return m.at }

func (m *ContextMenu) Layout(gtx C) D {
	th := m.app.th
	for {
		ev, ok := gtx.Event(key.Filter{Name: key.NameUpArrow}, key.Filter{Name: key.NameDownArrow}, key.Filter{Name: key.NameReturn}, key.Filter{Name: key.NameEnter})
		if !ok {
			break
		}
		e, ok := ev.(key.Event)
		if !ok || e.State != key.Press {
			continue
		}
		switch e.Name {
		case key.NameUpArrow:
			m.step(-1)
		case key.NameDownArrow:
			m.step(1)
		default:
			if m.sel >= 0 && m.sel < len(m.items) && m.items[m.sel].action != nil {
				m.done = true
				m.items[m.sel].action()
			}
		}
	}
	for _, it := range m.items {
		if it.action != nil && it.clicker.Clicked(gtx) {
			m.done = true
			it.action()
		}
	}
	gtx.Constraints.Min.X = gtx.Dp(230)
	gtx.Constraints.Max.X = gtx.Dp(300)
	return rborder(gtx, pal.Border, pal.Panel, 8, func(gtx C) D {
		return inset(4).Layout(gtx, func(gtx C) D {
			var ch []layout.FlexChild
			if m.title != "" {
				ch = append(ch, layout.Rigid(func(gtx C) D {
					return insetHV(10, 6).Layout(gtx, func(gtx C) D {
						return lbl(gtx, th, fontSans, 11, pal.TextDim, m.title)
					})
				}))
			}
			for i, it := range m.items {
				i, it := i, it
				if it.sep {
					ch = append(ch, layout.Rigid(func(gtx C) D {
						return layout.Inset{Top: 3, Bottom: 3}.Layout(gtx, func(gtx C) D { return hline(gtx, pal.Border) })
					}))
					continue
				}
				ch = append(ch, layout.Rigid(func(gtx C) D {
					return it.clicker.Layout(gtx, func(gtx C) D {
						hot := it.clicker.Hovered() || i == m.sel
						b := colorNRGBA{}
						if hot {
							b = pal.Hover
						}
						c := pal.Text
						if it.danger {
							c = pal.Err
						}
						return rbg(gtx, b, 5, func(gtx C) D {
							gtx.Constraints.Min.X = gtx.Constraints.Max.X
							return insetHV(10, 6).Layout(gtx, func(gtx C) D {
								return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
									layout.Rigid(func(gtx C) D { return mdi(gtx, it.icon, c, 15) }),
									layout.Rigid(layout.Spacer{Width: 9}.Layout),
									layout.Flexed(1, func(gtx C) D { return lbl(gtx, th, fontSans, 13, c, it.label) }),
									layout.Rigid(func(gtx C) D {
										if it.hint == "" {
											return D{}
										}
										return lbl(gtx, th, fontMono, 11, pal.TextDim, it.hint)
									}),
								)
							})
						})
					})
				}))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, ch...)
		})
	})
}

func (m *ContextMenu) step(d int) {
	for i := 0; i < len(m.items); i++ {
		m.sel = (m.sel + d + len(m.items)) % len(m.items)
		if !m.items[m.sel].sep {
			return
		}
	}
}

// openContextMenu builds the actions for the selection (or marked rows).
func (v *ResourceView) openContextMenu(at image.Point) {
	a, c := v.app, v.app.cluster
	r, ok := v.selected()
	if !ok || c == nil {
		return
	}
	rw := !a.readOnly()
	var items []*menuItem
	add := func(show bool, ic *widget.Icon, label, hint string, danger bool, fn func()) {
		if show {
			items = append(items, &menuItem{icon: ic, label: label, hint: hint, danger: danger, action: fn})
		}
	}
	sep := func() {
		if len(items) > 0 && !items[len(items)-1].sep {
			items = append(items, &menuItem{sep: true})
		}
	}

	ids := v.markedIDs()
	if len(ids) > 1 {
		title := fmt.Sprintf("%d %s selected", len(ids), strings.ToLower(v.title))
		add(true, icCopyText, "Copy names", "", false, func() { a.copyLater(strings.Join(ids, "\n")) })
		add(true, icClear, "Clear selection", "esc", false, v.clearMarks)
		sep()
		if v.gvr == "v1/nodes" {
			add(rw, icCordon, "Cordon all", "", false, func() { v.bulkCordon(true) })
			add(rw, icUncordon, "Uncordon all", "", false, func() { v.bulkCordon(false) })
		}
		add(rw && c.CanRestart(v.gvr), icRestart, "Restart all", "", false, v.bulkRestart)
		delLabel := "Delete all"
		if v.gvr == backend.HelmGVR {
			delLabel = "Uninstall all"
		}
		add(rw && (v.gvr == backend.HelmGVR || (!isHelm(v.gvr) && backend.CanVerb(v.gvr, "delete"))), icDelete, delLabel, "ctrl+d", true, v.bulkDelete)
		a.overlay = &ContextMenu{app: a, at: at, title: title, items: items, sel: -1}
		return
	}

	// single-row actions read the selection when clicked: pin it back to
	// the right-clicked row (live updates or tab switches may have moved it)
	gvr, rowID := v.gvr, r.ID
	add = func(show bool, ic *widget.Icon, label, hint string, danger bool, fn func()) {
		if !show {
			return
		}
		bound := func() {
			if a.view != v || v.gvr != gvr || !v.selectID(rowID) {
				a.flash(shortName(rowID)+" is no longer listed", true)
				return
			}
			fn()
		}
		items = append(items, &menuItem{icon: ic, label: label, hint: hint, danger: danger, action: bound})
	}
	isPod := v.gvr == "v1/pods"
	helm := isHelm(v.gvr)
	add(true, icDescribe, "Describe", "d", false, func() { v.details(tabDescribe) })
	add(!helm, icYAML, "View YAML", "y", false, func() { v.details(tabYAML) })
	add(!helm && rw, icEdit, "Edit", "e", false, v.edit)
	add(v.gvr == "v1/secrets", icSecret, "Decode secret", "x", false, func() { v.details(tabData) })
	add(helm, icValues, "Values", "v", false, func() { v.details(tabValues) })
	add(v.gvr == backend.HelmGVR, icHistory, "History", "enter", false, v.helmHistory)
	sep()
	add(c.IsLoggable(v.gvr), icLogs, "Logs", "l", false, func() { v.logs(false) })
	add(isPod, icLogs, "Previous logs", "p", false, func() { v.logs(true) })
	add(isPod && rw, icTerminal, "Shell", "s", false, v.shell)
	add(canForward(v.gvr), icForward, "Port-forward…", "shift+f", false, v.portForward)
	add(backend.CanXRay(v.gvr), icXRay, "XRay", "x", false, v.xray)
	add(true, icAI, "Ask the assistant", "", false, func() { a.askAbout(v.gvr, r.ID) })
	sep()
	add(true, icCopyText, "Copy name", "", false, func() { a.copyLater(shortName(r.ID)) })
	add(r.ID != shortName(r.ID), icCopyText, "Copy namespace/name", "", false, func() { a.copyLater(r.ID) })
	add(true, icBookmark, "Bookmark this view", "", false, a.bookmarkCurrent)
	sep()
	if v.gvr == "v1/nodes" && rw {
		if nodeCordoned(r) {
			add(true, icUncordon, "Uncordon", "u", false, v.toggleCordon)
		} else {
			add(true, icCordon, "Cordon", "c", false, v.toggleCordon)
		}
		add(true, icDrain, "Drain…", "r", true, v.drain)
	}
	add(rw && c.CanScale(v.gvr), icScale, "Scale…", "", false, v.scale)
	add(rw && c.CanRestart(v.gvr), icRestart, "Restart", "r", false, v.restart)
	add(rw && v.gvr == backend.HelmHistoryGVR, icRollback, "Rollback", "", true, v.rollback)
	delLabel := "Delete"
	if v.gvr == backend.HelmGVR {
		delLabel = "Uninstall"
	}
	add(rw && (v.gvr == backend.HelmGVR || (!helm && backend.CanVerb(v.gvr, "delete"))), icDelete, delLabel, "ctrl+d", true, v.confirmDelete)
	if n := len(items); n > 0 && items[n-1].sep {
		items = items[:n-1]
	}
	a.overlay = &ContextMenu{app: a, at: at, title: shortName(r.ID), items: items, sel: -1}
}

// copyLater copies text on the next frame (needs a gtx).
func (a *App) copyLater(s string) {
	a.pendingCopy = &s
	a.invalidate()
}

// askAbout opens the assistant with the resource attached.
func (a *App) askAbout(gvr, path string) {
	a.ai.open = true
	a.ai.attach = true
	a.ai.send(fmt.Sprintf("Explain %s %s and check it for problems.", strings.ToLower(singular(titleFor(gvr))), path))
}

type bulkButtons struct {
	clear, del, restart, cordon, uncordon, copy widget.Clickable
}

func (v *ResourceView) layoutBulkBar(gtx C) D {
	a, th, c := v.app, v.app.th, v.app.cluster
	b := &v.bulkBtns
	ids := v.markedIDs()
	if b.clear.Clicked(gtx) {
		v.clearMarks()
	}
	if b.del.Clicked(gtx) {
		v.bulkDelete()
	}
	if b.restart.Clicked(gtx) {
		v.bulkRestart()
	}
	if b.cordon.Clicked(gtx) {
		v.bulkCordon(true)
	}
	if b.uncordon.Clicked(gtx) {
		v.bulkCordon(false)
	}
	if b.copy.Clicked(gtx) {
		copyText(gtx, strings.Join(ids, "\n"))
		a.flash(fmt.Sprintf("Copied %d names", len(ids)), false)
	}
	rw := !a.readOnly()
	items := []layout.FlexChild{
		layout.Rigid(func(gtx C) D {
			return badge(gtx, th, fmt.Sprintf("%d selected", len(ids)), pal.OnAccent, pal.Accent)
		}),
		layout.Rigid(layout.Spacer{Width: 8}.Layout),
		layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &b.clear, icClear, "Clear", pal.TextDim, colorNRGBA{}) }),
		layout.Flexed(1, layout.Spacer{}.Layout),
	}
	add := func(show bool, cl *widget.Clickable, ic *widget.Icon, txt string, danger bool) {
		if !show {
			return
		}
		fg := pal.Text
		if danger {
			fg = pal.Err
		}
		items = append(items, layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: 4}.Layout(gtx, func(gtx C) D { return pillButton(gtx, th, cl, ic, txt, fg, colorNRGBA{}) })
		}))
	}
	add(true, &b.copy, icCopyText, "Copy names", false)
	add(rw && v.gvr == "v1/nodes", &b.cordon, icCordon, "Cordon", false)
	add(rw && v.gvr == "v1/nodes", &b.uncordon, icUncordon, "Uncordon", false)
	add(rw && c.CanRestart(v.gvr), &b.restart, icRestart, "Restart", false)
	delTxt := "Delete"
	if v.gvr == backend.HelmGVR {
		delTxt = "Uninstall"
	}
	add(rw && (v.gvr == backend.HelmGVR || (!isHelm(v.gvr) && backend.CanVerb(v.gvr, "delete"))), &b.del, icDelete, delTxt, true)
	return bg(gtx, withAlpha(pal.Accent, 0x22), func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Inset{Left: 16, Right: 10, Top: 4, Bottom: 4}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, items...)
		})
	})
}
