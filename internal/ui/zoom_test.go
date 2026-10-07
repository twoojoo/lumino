package ui

import (
	"math"
	"testing"

	"github.com/giovanni/lumino/internal/prefs"
)

func TestUIScaleClamp(t *testing.T) {
	t.Setenv("LUMINO_CONFIG_DIR", t.TempDir())
	a := &App{prefs: prefs.Load()}
	for in, want := range map[float64]float64{0: 1, -2: 1, math.NaN(): 1, math.Inf(1): 1, 100: 1.8, 0.01: 0.8, 1.25: 1.25} {
		a.prefs.UIScale = in
		if got := a.uiScale(); got != want {
			t.Errorf("uiScale(%v) = %v, want %v", in, got, want)
		}
	}
	a.prefs.UIScale = 1.8
	a.zoom(1)
	if a.uiScale() != 1.8 {
		t.Error("zoom past the largest step")
	}
	a.zoom(-1)
	if a.uiScale() != 1.6 {
		t.Errorf("zoom out %v", a.uiScale())
	}
	if a.status != "Interface size 160%" {
		t.Errorf("flash %q", a.status)
	}
	if got := prefs.Load().UIScale; got != 1.6 {
		t.Errorf("saved scale %v", got)
	}
	a.zoom(0)
	if a.uiScale() != 1 {
		t.Error("zoom reset")
	}
	a.prefs.UIScale = 0.8
	a.zoom(-1)
	if a.uiScale() != 0.8 {
		t.Error("zoom past the smallest step")
	}
}
