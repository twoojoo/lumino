package main

import (
	"image"
	"image/color"
	"testing"
)

// A dark tile with a black hole inside, on a black surround: the surround
// becomes transparent, the tile (hole included) fills the safe area.
func TestCutout(t *testing.T) {
	art := image.NewRGBA(image.Rect(0, 0, 300, 280))
	for y := 0; y < 280; y++ {
		for x := 0; x < 300; x++ {
			c := color.RGBA{0, 0, 0, 255}
			if x >= 30 && x < 270 && y >= 20 && y < 260 {
				c = color.RGBA{20, 30, 70, 255}
				if x >= 140 && x < 160 && y >= 130 && y < 150 {
					c = color.RGBA{0, 0, 0, 255}
				}
			}
			art.Set(x, y, c)
		}
	}
	got := cutout(art)
	if got.Rect != image.Rect(0, 0, S, S) {
		t.Fatalf("size %v", got.Rect)
	}
	alpha := func(x, y int) uint8 { return got.NRGBAAt(x, y).A }
	for _, p := range []image.Point{{0, 0}, {margin - 2, S / 2}, {S / 2, margin - 2}, {S - 1, S - 1}} {
		if a := alpha(p.X, p.Y); a != 0 {
			t.Errorf("surround %v alpha %d, want 0", p, a)
		}
	}
	for _, p := range []image.Point{{margin + 2, S / 2}, {S / 2, margin + 2}, {S - margin - 3, S - margin - 3}, {S / 2, S / 2}} {
		if a := alpha(p.X, p.Y); a != 255 {
			t.Errorf("tile %v alpha %d, want 255", p, a)
		}
	}
}
