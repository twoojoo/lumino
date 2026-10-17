//go:build dev && integration

package ui

import (
	"testing"

	"gioui.org/io/key"

	"github.com/twoojoo/lumino/internal/db"
)

// TestEditorRegressWidgets covers editor bugs that need the real event
// router: Return without EditEvent, Return followed by an EditEvent "\n"
// (one newline), Shift+Enter in the find bar, the IME selection range.
func TestEditorRegressWidgets(t *testing.T) {
	path := sqliteFixture(t, 3)
	h := newHarness(t, []db.Config{{ID: "lite", Name: "edreg", Driver: "sqlite", File: path}}, Options{Connection: "edreg"})
	h.waitFor("console", func() bool { return h.console() != nil })
	c := h.console()
	ran := 0
	prev := c.ed.OnCommand
	c.ed.OnCommand = func(cmd string) bool { ran++; return true }
	c.ed.SetText("SELECT 1")
	h.focusEditor(c)

	h.press(key.NameReturn)
	if got := c.ed.Text(); got != "SELECT 1\n" {
		t.Fatalf("plain Return: %q", got)
	}
	// platforms sending both the key and the text: one newline
	h.events(key.Event{Name: key.NameReturn, State: key.Press}, key.EditEvent{Text: "\n"}, key.Event{Name: key.NameReturn, State: key.Release})
	if got := c.ed.Text(); got != "SELECT 1\n\n" {
		t.Fatalf("Return + EditEvent: %q", got)
	}
	// EditEvent alone (X11, tests) still works
	h.typeText("\n")
	if got := c.ed.Text(); got != "SELECT 1\n\n\n" {
		t.Fatalf("EditEvent newline: %q", got)
	}
	h.press(key.NameReturn, key.ModShortcut)
	h.press(key.NameEnter, key.ModShortcut|key.ModShift)
	if ran != 2 || c.ed.Text() != "SELECT 1\n\n\n" {
		t.Fatalf("Ctrl+Enter: ran %d text %q", ran, c.ed.Text())
	}
	c.ed.OnCommand = prev

	// IME selection: Start is the caret, End the anchor
	c.ed.SetText("abcdef")
	c.ed.SelectRange(1, 4) // anchor 1, caret 4
	h.frame()
	h.frame()
	if r := h.r.EditorState().Selection.Range; r.Start != 4 || r.End != 1 {
		t.Fatalf("selection range %+v", r)
	}

	// find bar: Enter = next, Shift+Enter = previous
	c.ed.SetText("x a x a x a")
	c.ed.SetCaretOffset(0)
	h.press("F", key.ModShortcut)
	h.frame()
	h.typeText("a")
	if c.ed.anchor != (tpos{0, 2}) {
		t.Fatalf("first match: %v", c.ed.anchor)
	}
	h.press(key.NameReturn)
	if c.ed.anchor != (tpos{0, 6}) {
		t.Fatalf("Enter: %v", c.ed.anchor)
	}
	h.press(key.NameReturn, key.ModShift)
	if c.ed.anchor != (tpos{0, 2}) {
		t.Fatalf("Shift+Enter should go back: %v", c.ed.anchor)
	}
}
