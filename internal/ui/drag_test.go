//go:build dev && integration

package ui

import (
	"testing"

	"gioui.org/f32"
	"gioui.org/io/pointer"

	"github.com/twoojoo/lumino/internal/db"
)

// findCursor moves the pointer along a line (x varies when vertical is
// false, y otherwise) and returns the first position showing the cursor.
func (h *harness) findCursor(t *testing.T, vertical bool, fixed, from, to int, want pointer.Cursor) float32 {
	t.Helper()
	for p := from; p < to; p++ {
		pt := f32.Pt(float32(p), float32(fixed))
		if vertical {
			pt = f32.Pt(float32(fixed), float32(p))
		}
		h.r.Queue(pointer.Event{Kind: pointer.Move, Position: pt, Source: pointer.Mouse})
		h.frame()
		if h.r.Cursor() == want {
			return float32(p)
		}
	}
	t.Fatalf("no %v cursor between %d and %d", want, from, to)
	return 0
}

// dragSteps presses at p and moves the pointer by d, n times, checking
// after every frame that the dragged size moved by exactly as much as the
// pointer (the handle must not lag, overshoot or jitter).
func (h *harness) dragSteps(t *testing.T, p, d f32.Point, n int, size func() int, sign int) {
	t.Helper()
	h.events(
		pointer.Event{Kind: pointer.Move, Position: p, Source: pointer.Mouse},
		pointer.Event{Kind: pointer.Press, Position: p, Buttons: pointer.ButtonPrimary, Source: pointer.Mouse},
	)
	s0 := size()
	for i := 1; i <= n; i++ {
		q := p.Add(d.Mul(float32(i)))
		h.events(pointer.Event{Kind: pointer.Move, Position: q, Buttons: pointer.ButtonPrimary, Source: pointer.Mouse})
		moved := int(d.X+d.Y) * i * sign
		if got := size() - s0; got != moved {
			t.Fatalf("step %d: size moved by %d, pointer by %d", i, got, moved)
		}
	}
	q := p.Add(d.Mul(float32(n)))
	h.events(pointer.Event{Kind: pointer.Release, Position: q, Source: pointer.Mouse})
}

// TestUIDragHandles: the sidebar edge, the editor/results divider and the
// value panel edge follow the pointer exactly over a multi-frame drag.
func TestUIDragHandles(t *testing.T) {
	path := sqliteFixture(t, 10)
	h := newHarness(t, []db.Config{{ID: "lite", Name: "local sqlite", Driver: "sqlite", File: path}}, Options{Connection: "local sqlite"})
	a := h.a
	h.waitFor("console", func() bool { return h.console() != nil && a.connState("lite").status == stReady })
	c := h.console()
	c.ed.SetText("SELECT * FROM customers ORDER BY id")
	c.run(false)
	h.waitFor("results", func() bool { return len(c.results) == 1 && !c.running })

	t.Run("sidebar", func(t *testing.T) {
		x := h.findCursor(t, false, 600, 0, h.size.X/2, pointer.CursorColResize)
		h.dragSteps(t, f32.Pt(x+1, 600), f32.Pt(12, 0), 8, func() int { return a.sidebar.width }, 1)
		h.dragSteps(t, f32.Pt(x+1+96, 600), f32.Pt(-9, 0), 6, func() int { return a.sidebar.width }, 1)
	})

	t.Run("console divider", func(t *testing.T) {
		x := h.size.X - 200
		y := h.findCursor(t, true, x, 0, h.size.Y, pointer.CursorRowResize)
		h.dragSteps(t, f32.Pt(float32(x), y+2), f32.Pt(0, 11), 8, func() int { return c.curH }, 1)
		h.dragSteps(t, f32.Pt(float32(x), y+2+88), f32.Pt(0, -7), 10, func() int { return c.curH }, 1)
	})

	t.Run("value panel", func(t *testing.T) {
		c.value.open, c.value.width = true, 380
		c.grid.SelectCell(0, 1)
		h.frame()
		x := float32(h.size.X - 380)
		h.dragSteps(t, f32.Pt(x, 600), f32.Pt(-10, 0), 8, func() int { return c.value.width }, -1)
		h.dragSteps(t, f32.Pt(x-80, 600), f32.Pt(13, 0), 5, func() int { return c.value.width }, -1)
	})
}
