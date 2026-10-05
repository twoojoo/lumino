//go:build dev && integration

package ui

import (
	"testing"
	"time"

	"gioui.org/io/key"
	"gioui.org/io/pointer"

	"github.com/giovanni/lumino/internal/db"
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
