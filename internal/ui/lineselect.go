package ui

import (
	"image"
	"io"
	"strings"

	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/widget"
)

// lineSelect adds line-granular selection to a list of colored lines
// (YAML, values, logs, diffs), whose rich text Gio can't select by
// character: press and drag (or Shift+click) selects lines, Ctrl+A all of
// them, Ctrl+C copies, Esc or a click elsewhere clears.
//
//	ls.begin(gtx, n)                       // before the list, on its area
//	list.Layout(gtx, n, func(gtx C, i int) D { return ls.item(gtx, i, draw) })
//	ls.end(&list)
type lineSelect struct {
	app            *App
	text           func(i int) string // line i, for copying
	anchor, cursor int                // selected range (inclusive), -1 = none
	dragging       bool

	n       int
	heights map[int]int // laid-out items this frame
	tops    []linePos   // visible items of the previous frame
	area    clip.Stack  // pushed by begin, popped by end: the list is nested
}

type linePos struct{ idx, top, h int }

func newLineSelect(a *App, text func(i int) string) *lineSelect {
	return &lineSelect{app: a, text: text, anchor: -1, cursor: -1, heights: map[int]int{}}
}

func (s *lineSelect) active() bool { return s.anchor >= 0 }

func (s *lineSelect) clear() { s.anchor, s.cursor, s.dragging = -1, -1, false }

func (s *lineSelect) bounds() (lo, hi int) { return min(s.anchor, s.cursor), max(s.anchor, s.cursor) }

func (s *lineSelect) selected(i int) bool {
	if !s.active() {
		return false
	}
	lo, hi := s.bounds()
	return i >= lo && i <= hi
}

// hit maps a y in the list area to a line (clamped to the visible ones).
func (s *lineSelect) hit(y int) int {
	if len(s.tops) == 0 {
		return -1
	}
	for _, p := range s.tops {
		if y < p.top+p.h {
			return p.idx
		}
	}
	return s.tops[len(s.tops)-1].idx
}

// begin handles input on the list area (call before laying out the list).
func (s *lineSelect) begin(gtx C, n int) {
	s.n = n
	if s.anchor >= n {
		s.clear()
	}
	// the list (and its scroll handler) is laid out inside this area, so
	// both receive pointer events
	s.area = clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
	event.Op(gtx.Ops, s)
	for {
		ev, ok := gtx.Event(
			pointer.Filter{Target: s, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel},
			key.FocusFilter{Target: s},
			key.Filter{Focus: s, Name: "C", Required: key.ModShortcut},
			key.Filter{Focus: s, Name: "A", Required: key.ModShortcut},
		)
		if !ok {
			break
		}
		switch e := ev.(type) {
		case pointer.Event:
			switch e.Kind {
			case pointer.Press:
				if e.Buttons != pointer.ButtonPrimary {
					continue
				}
				i := s.hit(int(e.Position.Y))
				if i < 0 {
					continue
				}
				gtx.Execute(key.FocusCmd{Tag: s})
				if e.Modifiers.Contain(key.ModShift) && s.active() {
					s.cursor = i
				} else {
					s.anchor, s.cursor = i, i
				}
				s.dragging = true
			case pointer.Drag:
				if s.dragging {
					if i := s.hit(int(e.Position.Y)); i >= 0 {
						s.cursor = i
					}
				}
			case pointer.Release, pointer.Cancel:
				s.dragging = false
			}
		case key.FocusEvent:
			if !e.Focus {
				s.clear()
			}
		case key.Event:
			if e.State != key.Press {
				continue
			}
			switch e.Name {
			case "A":
				if n > 0 {
					s.anchor, s.cursor = 0, n-1
				}
			case "C":
				if s.active() {
					s.copy(gtx)
				}
			}
		}
	}
	if gtx.Focused(s) && s.active() {
		s.app.lineSel = s
	}
	clear(s.heights)
}

func (s *lineSelect) copy(gtx C) {
	lo, hi := s.bounds()
	var b strings.Builder
	for i := lo; i <= hi && i < s.n; i++ {
		b.WriteString(s.text(i))
		b.WriteByte('\n')
	}
	gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(b.String()))})
	n := hi - lo + 1
	s.app.flash("Copied "+itoa(n)+" line(s)", false)
}

// item draws line i with the selection band underneath.
func (s *lineSelect) item(gtx C, i int, w layout.Widget) D {
	m := op.Record(gtx.Ops)
	d := w(gtx)
	call := m.Stop()
	if s.selected(i) {
		fillRect(gtx, withAlpha(pal.Accent, 0x40), image.Rect(0, 0, gtx.Constraints.Max.X, d.Size.Y))
	}
	call.Add(gtx.Ops)
	s.heights[i] = d.Size.Y
	return d
}

// end records where the visible lines are (list area coordinates).
func (s *lineSelect) end(l *widget.List) {
	s.area.Pop()
	s.tops = s.tops[:0]
	y := -l.Position.Offset
	for i := l.Position.First; ; i++ {
		h, ok := s.heights[i]
		if !ok {
			break
		}
		s.tops = append(s.tops, linePos{idx: i, top: y, h: h})
		y += h
	}
}

// lineSelActive: a line selection had the focus in the previous frame.
func (a *App) lineSelActive() bool { return a.lineSelPrev != nil }

func (a *App) clearLineSels() {
	if a.lineSelPrev != nil {
		a.lineSelPrev.clear()
	}
}
