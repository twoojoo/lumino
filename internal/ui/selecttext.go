package ui

import (
	"image"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"
)

// selState keeps a selectable text across frames.
type selState struct {
	sel   widget.Selectable
	frame uint64
	tip   tipTag
}

// selText draws read-only text the user can select and copy (drag, double
// click for a word, triple click for a line, Ctrl+C). maxLines 1 truncates
// with an ellipsis and offers the full text as a tooltip; 0 wraps freely.
// key must be stable across frames.
func (a *App) selText(gtx C, key string, f font.Font, size unit.Sp, c colorNRGBA, s string, maxLines int) D {
	st := a.sels[key]
	if st == nil {
		st = &selState{}
		st.sel.Truncator = "…"
		st.sel.LineHeightScale = 1.05
		a.sels[key] = st
	}
	st.frame = a.frame
	if st.sel.Text() != s {
		st.sel.SetText(s)
	}
	st.sel.MaxLines = maxLines
	if st.sel.Focused() {
		a.selFocused = true
	} else if st.sel.SelectionLen() > 0 {
		st.sel.ClearSelection() // one selection at a time: clicking elsewhere drops it
	}
	if maxLines == 1 {
		gtx.Constraints.Min.Y = 0
	}
	d := st.sel.Layout(gtx, a.th.Shaper, f, size, colorMat(gtx, c), colorMat(gtx, withAlpha(pal.Accent, 0x60)))
	if maxLines == 1 {
		// full text of a truncated value on hover
		area := clip.Rect{Max: image.Pt(d.Size.X, d.Size.Y)}.Push(gtx.Ops)
		event.Op(gtx.Ops, &st.tip)
		area.Pop()
		for {
			ev, ok := gtx.Event(pointer.Filter{Target: &st.tip, Kinds: pointer.Enter | pointer.Leave | pointer.Cancel})
			if !ok {
				break
			}
			if pe, ok := ev.(pointer.Event); ok {
				st.tip.hovered = pe.Kind == pointer.Enter
			}
		}
		if st.tip.hovered && st.sel.Truncated() {
			a.hint(key, s)
		}
	}
	return d
}

// pruneSels forgets selectable texts no longer laid out.
func (a *App) pruneSels() {
	if len(a.sels) <= 4000 {
		return
	}
	for k, st := range a.sels {
		if st.frame != a.frame {
			delete(a.sels, k)
		}
	}
}

// selLbl is selText on one line (lbl replacement for dynamic values).
func (a *App) selLbl(gtx C, key string, f font.Font, size unit.Sp, c colorNRGBA, s string) D {
	return a.selText(gtx, key, f, size, c, s, 1)
}
