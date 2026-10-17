//go:build dev && integration

package ui

import (
	"testing"
	"time"

	"gioui.org/io/key"
	"gioui.org/io/pointer"

	"github.com/twoojoo/lumino/internal/db"
)

// TestUIRegressions covers bugs found in review: tab clicks swallowed by
// the middle-click area, closing the last tab from the strip, the password
// prompt recreated every frame, and a console stuck "running" after a
// cancelled connect.
func TestUIRegressions(t *testing.T) {
	path := sqliteFixture(t, 10)
	h := newHarness(t, []db.Config{
		{ID: "lite", Name: "local sqlite", Driver: "sqlite", File: path},
		{ID: "pg", Name: "pg nopw", Driver: "postgres", Host: "127.0.0.1", Port: 55432, User: "lumino", Database: "lumino"},
	}, Options{Connection: "local sqlite"})
	a := h.a
	h.waitFor("console", func() bool { return h.console() != nil && a.connState("lite").status == stReady })
	a.newConsole("lite", "", "")
	h.settle(100 * time.Millisecond)
	if len(a.tabs) != 2 || a.tabIdx != 1 {
		t.Fatalf("tabs %d idx %d", len(a.tabs), a.tabIdx)
	}
	// find the first tab in the strip by clicking along it
	hit := float32(-1)
	for x := float32(150); x < 700 && hit < 0; x += 10 {
		for _, y := range []float32{45, 57} {
			h.click(x, y, pointer.ButtonPrimary)
			if a.tabIdx == 0 {
				hit = x
				break
			}
		}
	}
	if hit < 0 {
		h.shot("regress-tab-click")
		t.Fatal("clicking a tab never selected it")
	}
	// close down to one tab, then middle-click the last one
	a.removeTab(a.tabs[1])
	h.settle(50 * time.Millisecond)
	for _, y := range []float32{45, 57} {
		if len(a.tabs) == 0 {
			break
		}
		h.click(hit, y, pointer.ButtonTertiary)
		h.settle(50 * time.Millisecond)
	}
	if len(a.tabs) != 0 {
		t.Fatalf("middle click didn't close the last tab (%d left)", len(a.tabs))
	}

	// password prompt: stable across frames, gone for good after cancel
	a.sidebar.expand(connKey("pg"), true)
	h.settle(100 * time.Millisecond)
	p1, ok := a.overlay.(*SecretPrompt)
	if !ok {
		t.Fatalf("no password prompt (overlay %T)", a.overlay)
	}
	h.settle(100 * time.Millisecond)
	if a.overlay != p1 {
		t.Fatal("password prompt recreated while open")
	}
	h.press(key.NameEscape)
	h.settle(200 * time.Millisecond)
	if _, again := a.overlay.(*SecretPrompt); again {
		t.Fatal("password prompt came back after cancel")
	}
	if st := a.connState("pg").status; st != stFailed {
		t.Fatalf("status after cancel: %v", st)
	}

	// a console run waiting for a cancelled connect stops running
	a.connState("pg").status = stIdle
	c := a.newConsole("pg", "", "SELECT 1")
	h.settle(50 * time.Millisecond)
	c.run(true)
	h.waitFor("prompt", func() bool { _, ok := a.overlay.(*SecretPrompt); return ok })
	h.press(key.NameEscape)
	h.waitFor("not running", func() bool { return !c.running })
}

// TestUIRegressions2 covers the second review round: Disconnect stays
// disconnected, background prompts queue instead of replacing each other,
// and a modal takes the keyboard from the editor behind it.
func TestUIRegressions2(t *testing.T) {
	path := sqliteFixture(t, 10)
	h := newHarness(t, []db.Config{
		{ID: "lite", Name: "local sqlite", Driver: "sqlite", File: path},
		{ID: "pg1", Name: "pg one", Driver: "postgres", Host: "127.0.0.1", Port: 55432, User: "lumino", Database: "lumino"},
		{ID: "pg2", Name: "pg two", Driver: "postgres", Host: "127.0.0.1", Port: 55432, User: "lumino", Database: "lumino"},
	}, Options{Connection: "local sqlite"})
	a := h.a
	h.waitFor("console", func() bool { return h.console() != nil && a.connState("lite").status == stReady })

	// keys typed while a confirm is open don't reach the editor
	c := h.console()
	h.focusEditor(c)
	h.typeText("abc")
	a.overlay = newConfirm(a, "Test", "body", false, func(bool) {})
	h.settle(50 * time.Millisecond)
	h.typeText("x")
	if got := c.ed.Text(); got != "abc" {
		t.Fatalf("typed behind the modal: %q", got)
	}
	h.press(key.NameEscape)
	h.settle(50 * time.Millisecond)

	// Disconnect: the node collapses and stays disconnected
	a.requestDisconnect("lite", nil)
	h.settle(300 * time.Millisecond)
	if st := a.connState("lite").status; st != stIdle {
		t.Fatalf("status after disconnect: %v", st)
	}

	// two connections asking for a password: prompts queue
	a.sidebar.expand(connKey("pg1"), true)
	a.sidebar.expand(connKey("pg2"), true)
	h.settle(100 * time.Millisecond)
	p1, ok := a.overlay.(*SecretPrompt)
	if !ok {
		t.Fatalf("no prompt (%T)", a.overlay)
	}
	h.press(key.NameEscape)
	h.settle(100 * time.Millisecond)
	p2, ok := a.overlay.(*SecretPrompt)
	if !ok || p2 == p1 {
		t.Fatalf("second prompt not shown after the first (%T)", a.overlay)
	}
	h.press(key.NameEscape)
	h.settle(100 * time.Millisecond)
	for _, id := range []string{"pg1", "pg2"} {
		if st := a.connState(id).status; st != stFailed {
			t.Fatalf("%s stuck in %v", id, st)
		}
	}
}

// TestUITabCloseAndMenu: closing a console with an unsaved query offers to
// save it (Save / Don't save / Cancel), also for "Close all" from the tab
// context menu, opened with a right click on the tab.
func TestUITabCloseAndMenu(t *testing.T) {
	path := sqliteFixture(t, 10)
	h := newHarness(t, []db.Config{{ID: "lite", Name: "local sqlite", Driver: "sqlite", File: path}}, Options{Connection: "local sqlite"})
	a := h.a
	h.waitFor("console", func() bool { return h.console() != nil && a.connState("lite").status == stReady })
	c1 := h.console()
	c1.ed.SetText("SELECT 1")
	confirm := func() *Confirm {
		t.Helper()
		h.settle(50 * time.Millisecond)
		c, ok := a.overlay.(*Confirm)
		if !ok || c.altLabel == "" {
			t.Fatalf("no save prompt (%T)", a.overlay)
		}
		return c
	}

	// Cancel (Esc): the tab stays
	a.closeTab(0)
	confirm()
	h.press(key.NameEscape)
	h.settle(50 * time.Millisecond)
	if len(a.tabs) != 1 {
		t.Fatal("cancel closed the tab")
	}

	// Save…: named, saved, then closed
	a.closeTab(0)
	confirm().accept()
	h.settle(50 * time.Millisecond)
	d, ok := a.overlay.(*SaveQueryDialog)
	if !ok {
		t.Fatalf("no save dialog (%T)", a.overlay)
	}
	d.name.SetText("one")
	d.save()
	h.settle(50 * time.Millisecond)
	if len(a.tabs) != 0 || len(a.prefs.QueriesFor("lite")) != 1 {
		t.Fatalf("tabs %d saved %d", len(a.tabs), len(a.prefs.QueriesFor("lite")))
	}

	// a console holding a saved query closes without asking
	a.newConsole("lite", "", "SELECT 1")
	h.settle(50 * time.Millisecond)
	a.closeTab(0)
	h.settle(50 * time.Millisecond)
	if len(a.tabs) != 0 {
		t.Fatal("saved query asked again")
	}

	// right click on a tab: menu; Close all asks for each unsaved console
	a.newConsole("lite", "", "SELECT 2")
	a.newConsole("lite", "", "SELECT 3")
	h.settle(50 * time.Millisecond)
	opened := false
	for x := float32(150); x < 700 && !opened; x += 20 {
		for _, y := range []float32{45, 57} {
			h.click(x, y, pointer.ButtonSecondary)
			if m, ok := a.overlay.(*ContextMenu); ok {
				opened = true
				h.shot("50-tab-menu")
				for _, it := range m.items {
					if it.label == "Close all" {
						a.overlay = nil
						it.action()
					}
				}
				break
			}
		}
	}
	if !opened {
		t.Fatal("no tab context menu on right click")
	}
	first := confirm()
	h.shot("51-save-prompt")
	first.onAlt() // don't save the first
	confirm().onAlt() // nor the second
	h.settle(50 * time.Millisecond)
	if len(a.tabs) != 0 {
		t.Fatalf("Close all left %d tabs", len(a.tabs))
	}
}
