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
	got := cutout(art, 10)
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

// A dark navy surround (brighter than black) is cut at the tile's lighter
// rim when -black sits between the two.
func TestCutoutNavySurround(t *testing.T) {
	art := image.NewRGBA(image.Rect(0, 0, 200, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 200; x++ {
			c := color.RGBA{10, 14, 22, 255}
			switch {
			case x >= 20 && x < 180 && y >= 20 && y < 180 && (x < 22 || x >= 178 || y < 22 || y >= 178):
				c = color.RGBA{30, 40, 60, 255} // rim
			case x >= 20 && x < 180 && y >= 20 && y < 180:
				c = color.RGBA{9, 13, 20, 255} // tile as dark as the surround
			}
			art.Set(x, y, c)
		}
	}
	if got := cutout(art, 10); got.NRGBAAt(margin+1, margin+1).A != 255 {
		t.Fatalf("black=10 should keep the navy surround")
	}
	got := cutout(art, 28)
	if a := got.NRGBAAt(margin-2, S/2).A; a != 0 {
		t.Errorf("surround alpha %d, want 0", a)
	}
	if a := got.NRGBAAt(S/2, S/2).A; a != 255 {
		t.Errorf("tile alpha %d, want 255", a)
	}
}
