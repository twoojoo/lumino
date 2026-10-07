//go:build dev && integration

package ui

import (
	"testing"
	"time"

	"gioui.org/io/key"
)

// The zoom shortcuts scale the whole interface, also with a dialog open,
// and the Preferences show the current size.
func TestZoomShortcuts(t *testing.T) {
	h := newHarness(t, nil, Options{})
	h.settle(200 * time.Millisecond)
	for range 3 { // 110%, 125%, 140%
		h.press("=", key.ModShortcut)
	}
	if s := h.a.uiScale(); s != 1.4 {
		t.Fatalf("scale after 3 zoom-ins: %v", s)
	}
	h.settle(100 * time.Millisecond)
	h.shot("zoom-01-welcome-140")
	h.press(",", key.ModShortcut)
	h.press("-", key.ModShortcut)
	if s := h.a.uiScale(); s != 1.25 {
		t.Fatalf("zoom out with Preferences open: %v", s)
	}
	h.settle(100 * time.Millisecond)
	h.shot("zoom-02-prefs-125")
	h.press("0", key.ModShortcut)
	if s := h.a.uiScale(); s != 1 {
		t.Fatalf("zoom reset: %v", s)
	}
	h.settle(100 * time.Millisecond)
	h.shot("zoom-03-prefs-100")
}
