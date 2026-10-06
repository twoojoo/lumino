package ui

import (
	"image"
	"time"
	"unicode/utf8"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// Full-text tooltips for truncated table cells: a hovered cell whose text
// doesn't fit calls hint during layout; after a short delay the full text
// shows next to the pointer, drawn last (above everything).

const (
	tipDelay    = 450 * time.Millisecond
	tipMaxRunes = 1500 // "really long" values are cut
)

type tooltip struct {
	key, text string
	since     time.Time
	seen      bool // claimed during the current frame
}

// hint offers text as the tooltip of the hovered element identified by key.
func (a *App) hint(key, text string) {
	if a.overlay != nil {
		return
	}
	if a.tip.key != key {
		a.tip = tooltip{key: key, text: text, since: time.Now()}
	}
	a.tip.text, a.tip.seen = text, true
}

// truncated reports whether text needs more than width px on one line.
func truncated(gtx C, th *material.Theme, f font.Font, size unit.Sp, text string, width int) bool {
	m := op.Record(gtx.Ops)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(1<<24, 1<<24)}
	l := widget.Label{MaxLines: 1}
	d := l.Layout(g, th.Shaper, f, size, text, op.CallOp{})
	m.Stop() // measured only, never drawn
	return d.Size.X > width
}

// layoutTooltip draws the pending tooltip, if any (end of the frame).
func (a *App) layoutTooltip(gtx C) {
	if len(a.tipTags) > 4000 { // forget labels no longer on screen
		for k, tg := range a.tipTags {
			if tg.frame != a.frame {
				delete(a.tipTags, k)
			}
		}
	}
	t := &a.tip
	if !t.seen {
		*t = tooltip{}
		return
	}
	t.seen = false
	if wait := tipDelay - time.Since(t.since); wait > 0 {
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(wait)})
		return
	}
	text := t.text
	if utf8.RuneCountInString(text) > tipMaxRunes {
		text = string([]rune(text)[:tipMaxRunes]) + "…"
	}
	th := a.th
	maxW := min(gtx.Dp(560), gtx.Constraints.Max.X-gtx.Dp(16))
	m := op.Record(gtx.Ops)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(maxW, gtx.Constraints.Max.Y/2)}
	bgc := pal.Panel
	if !pal.Light {
		bgc = lighten(pal.Panel, 0.10)
	}
	d := rborder(g, pal.Border, bgc, 6, func(gtx C) D {
		return insetHV(8, 5).Layout(gtx, func(gtx C) D {
			l := material.Body2(th, text)
			l.Font, l.TextSize, l.Color, l.MaxLines = fontSans, 12, pal.TextBright, 20
			return l.Layout(gtx)
		})
	})
	call := m.Stop()
	// below-right of the pointer, kept inside the window
	p := a.pointer.Add(image.Pt(gtx.Dp(12), gtx.Dp(18)))
	ws := gtx.Constraints.Max
	if p.X+d.Size.X > ws.X-gtx.Dp(4) {
		p.X = max(gtx.Dp(4), ws.X-gtx.Dp(4)-d.Size.X)
	}
	if p.Y+d.Size.Y > ws.Y-gtx.Dp(4) {
		p.Y = max(gtx.Dp(4), a.pointer.Y-gtx.Dp(8)-d.Size.Y)
	}
	st := op.Offset(p).Push(gtx.Ops)
	// soft shadow so it stands out over panels of the same color
	for i, alpha := range []uint8{0x18, 0x10, 0x08} {
		o := gtx.Dp(unit.Dp(1 + i))
		fillRRect(gtx, colorNRGBA{A: alpha}, image.Rect(-o, o, d.Size.X+o, d.Size.Y+2*o), gtx.Dp(6)+o)
	}
	call.Add(gtx.Ops)
	st.Pop()
}

// tipTag tracks hover over one label laid out with tipLbl.
type tipTag struct {
	hovered bool
	frame   uint64 // last frame it was laid out
}

// tipLbl is lbl that offers its full text as a tooltip when it is
// truncated and hovered. key must be stable across frames.
func (a *App) tipLbl(gtx C, key string, f font.Font, size unit.Sp, c colorNRGBA, s string) D {
	d := lbl(gtx, a.th, f, size, c, s)
	tag := a.tipTags[key]
	if tag == nil {
		tag = new(tipTag)
		a.tipTags[key] = tag
	}
	tag.frame = a.frame
	// pass-through: the label must not swallow clicks (e.g. right-click
	// context menus of the tree row under it)
	pass := pointer.PassOp{}.Push(gtx.Ops)
	area := clip.Rect{Max: d.Size}.Push(gtx.Ops)
	event.Op(gtx.Ops, tag)
	area.Pop()
	pass.Pop()
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: tag, Kinds: pointer.Enter | pointer.Leave | pointer.Cancel})
		if !ok {
			break
		}
		if pe, ok := ev.(pointer.Event); ok {
			tag.hovered = pe.Kind == pointer.Enter
		}
	}
	if tag.hovered && truncated(gtx, a.th, f, size, s, d.Size.X+1) {
		a.hint(key, s)
	}
	return d
}

// hintRect offers text as the tooltip of an arbitrary area (pass-through:
// the area doesn't take pointer events from the widgets under it).
func (a *App) hintRect(gtx C, key string, r image.Rectangle, text string) {
	tag := a.tipTags[key]
	if tag == nil {
		tag = new(tipTag)
		a.tipTags[key] = tag
	}
	tag.frame = a.frame
	pass := pointer.PassOp{}.Push(gtx.Ops)
	area := clip.Rect(r).Push(gtx.Ops)
	event.Op(gtx.Ops, tag)
	area.Pop()
	pass.Pop()
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: tag, Kinds: pointer.Enter | pointer.Leave | pointer.Cancel})
		if !ok {
			break
		}
		if pe, ok := ev.(pointer.Event); ok {
			tag.hovered = pe.Kind == pointer.Enter
		}
	}
	if tag.hovered {
		a.hint(key, text)
	}
}
