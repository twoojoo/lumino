package ui

import (
	"image"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"
)

// dockTab is a session shown in the bottom dock (logs, shell).
type dockTab interface {
	Title() string
	Icon() *widget.Icon
	Layout(gtx C) D
	Close()
	Focused(gtx C) bool
}

// Dock is Lens' bottom panel hosting log and terminal tabs.
type Dock struct {
	app     *App
	tabs    []dockTab
	active  int
	height  int // px; 0 = default
	tabBtn  []widget.Clickable
	closeBt []widget.Clickable
	minBtn  widget.Clickable
	hidden  bool

	dragTag  int
	dragging bool
	grabY    float32
	curH     int
}

func newDock(a *App) *Dock { return &Dock{app: a} }

func (d *Dock) add(t dockTab) {
	d.tabs = append(d.tabs, t)
	d.active = len(d.tabs) - 1
	d.hidden = false
}

func (d *Dock) closeTab(i int) {
	if i < 0 || i >= len(d.tabs) {
		return
	}
	d.tabs[i].Close()
	d.tabs = append(d.tabs[:i], d.tabs[i+1:]...)
	// click states follow their tabs
	if i < len(d.tabBtn) {
		d.tabBtn = append(d.tabBtn[:i], d.tabBtn[i+1:]...)
	}
	if i < len(d.closeBt) {
		d.closeBt = append(d.closeBt[:i], d.closeBt[i+1:]...)
	}
	if i < d.active { // keep the same tab active
		d.active--
	}
	if d.active >= len(d.tabs) {
		d.active = len(d.tabs) - 1
	}
}

func (d *Dock) closeAll() {
	for _, t := range d.tabs {
		t.Close()
	}
	d.tabs, d.active = nil, 0
}

func (d *Dock) focused(gtx C) bool {
	if d.hidden || d.active < 0 || d.active >= len(d.tabs) {
		return false
	}
	return d.tabs[d.active].Focused(gtx)
}

func (d *Dock) openLogs(gvr, path string, previous bool) {
	d.add(newLogView(d.app, gvr, path, previous))
}

func (d *Dock) openShell(path, container string) {
	d.add(newTerminal(d.app, path, container))
}

// Layout draws the dock (nothing when there are no tabs).
func (d *Dock) Layout(gtx C) D {
	if len(d.tabs) == 0 {
		return D{}
	}
	if len(d.tabBtn) < len(d.tabs) {
		d.tabBtn = make([]widget.Clickable, len(d.tabs)+4)
		d.closeBt = make([]widget.Clickable, len(d.tabs)+4)
	}
	for i := range d.tabs {
		if d.tabBtn[i].Clicked(gtx) {
			d.active, d.hidden = i, false
		}
	}
	for i := range d.tabs {
		if d.closeBt[i].Clicked(gtx) {
			d.closeTab(i)
			if len(d.tabs) == 0 {
				return D{}
			}
			break
		}
	}
	if d.minBtn.Clicked(gtx) {
		d.hidden = !d.hidden
	}

	maxH := gtx.Constraints.Max.Y
	h := d.height
	if h == 0 {
		h = maxH * 42 / 100
	}
	h = max(gtx.Dp(120), min(h, maxH-gtx.Dp(120)))
	barH := gtx.Dp(32)
	if d.hidden {
		h = barH
	} else {
		d.curH = h
	}

	gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, h))
	fill(gtx, pal.Bg)
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return d.layoutBar(gtx, barH) }),
		layout.Flexed(1, func(gtx C) D {
			if d.hidden {
				return D{}
			}
			return d.tabs[d.active].Layout(gtx)
		}),
	)
}

func (d *Dock) layoutBar(gtx C, barH int) D {
	th := d.app.th
	sz := image.Pt(gtx.Constraints.Max.X, barH)
	fillRect(gtx, pal.Header, image.Rectangle{Max: sz})
	fillRect(gtx, pal.Border, image.Rect(0, 0, sz.X, gtx.Dp(1)))

	// drag the top edge to resize
	grip := clip.Rect{Max: image.Pt(sz.X, gtx.Dp(5))}.Push(gtx.Ops)
	pointer.CursorRowResize.Add(gtx.Ops)
	event.Op(gtx.Ops, &d.dragTag)
	grip.Pop()
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &d.dragTag, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel})
		if !ok {
			break
		}
		pe, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		switch pe.Kind {
		case pointer.Press:
			d.dragging, d.grabY, d.height = true, pe.Position.Y, d.curH
		case pointer.Drag:
			// positions are relative to the grip, which follows the pointer
			if d.dragging {
				d.height -= int(pe.Position.Y - d.grabY)
			}
		case pointer.Release, pointer.Cancel:
			d.dragging = false
		}
	}

	gtx.Constraints = layout.Exact(sz)
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Flexed(1, func(gtx C) D {
			var items []layout.FlexChild
			for i, t := range d.tabs {
				i, t := i, t
				items = append(items, layout.Rigid(func(gtx C) D {
					active := i == d.active
					m := op.Record(gtx.Ops)
					dims := layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							return d.tabBtn[i].Layout(gtx, func(gtx C) D {
								c := pal.TextDim
								if active || d.tabBtn[i].Hovered() {
									c = pal.TextBright
								}
								return layout.Inset{Left: 12, Right: 4, Top: 6, Bottom: 6}.Layout(gtx, func(gtx C) D {
									return iconText(gtx, th, t.Icon(), 12, c, t.Title())
								})
							})
						}),
						layout.Rigid(func(gtx C) D {
							return layout.Inset{Right: 6}.Layout(gtx, func(gtx C) D { return iconOnly(gtx, &d.closeBt[i], icClose, 12) })
						}),
					)
					call := m.Stop()
					if active {
						fillRect(gtx, pal.Panel, image.Rectangle{Max: image.Pt(dims.Size.X, barH)})
						fillRect(gtx, pal.Accent, image.Rect(0, barH-gtx.Dp(2), dims.Size.X, barH))
					}
					call.Add(gtx.Ops)
					return D{Size: image.Pt(dims.Size.X, barH)}
				}))
			}
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, items...)
		}),
		layout.Rigid(func(gtx C) D {
			ic := icExpand
			if d.hidden {
				ic = icExpandLess
			}
			return iconOnly(gtx, &d.minBtn, ic, 16)
		}),
		layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout),
	)
}
