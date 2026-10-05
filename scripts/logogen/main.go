// Command logogen draws the Lumino logo artwork (packaging/logo-source.png,
// 1024×1024 RGBA): a rounded Ayu tile with a database cylinder lit by a
// spark. Run it, then `make icons`.
//
//	go run ./scripts/logogen
package main

import (
	"flag"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
	"math"
	"os"

	"golang.org/x/image/vector"
)

const S = 1024

func rgb(c uint32, a uint8) color.NRGBA {
	return color.NRGBA{uint8(c >> 16), uint8(c >> 8), uint8(c), a}
}

// fill rasterizes a path and paints it with c (or a vertical gradient).
func fill(dst *image.NRGBA, build func(r *vector.Rasterizer), src image.Image) {
	r := vector.NewRasterizer(S, S)
	build(r)
	r.Draw(dst, dst.Bounds(), src, image.Point{})
}

func roundRect(r *vector.Rasterizer, x0, y0, x1, y1, rad float32) {
	k := float32(0.5523) * rad
	r.MoveTo(x0+rad, y0)
	r.LineTo(x1-rad, y0)
	r.CubeTo(x1-rad+k, y0, x1, y0+rad-k, x1, y0+rad)
	r.LineTo(x1, y1-rad)
	r.CubeTo(x1, y1-rad+k, x1-rad+k, y1, x1-rad, y1)
	r.LineTo(x0+rad, y1)
	r.CubeTo(x0+rad-k, y1, x0, y1-rad+k, x0, y1-rad)
	r.LineTo(x0, y0+rad)
	r.CubeTo(x0, y0+rad-k, x0+rad-k, y0, x0+rad, y0)
	r.ClosePath()
}

// ellipseArc adds an elliptical arc (angles in radians) to the path.
func ellipse(r *vector.Rasterizer, cx, cy, rx, ry float32, from, to float64, move bool) {
	steps := 64
	for i := 0; i <= steps; i++ {
		a := from + (to-from)*float64(i)/float64(steps)
		x := cx + rx*float32(math.Cos(a))
		y := cy + ry*float32(math.Sin(a))
		if i == 0 && move {
			r.MoveTo(x, y)
		} else {
			r.LineTo(x, y)
		}
	}
}

type vgrad struct {
	top, bottom color.NRGBA
	y0, y1      float64
}

func (g vgrad) ColorModel() color.Model { return color.NRGBAModel }
func (g vgrad) Bounds() image.Rectangle { return image.Rect(-1e9, -1e9, 1e9, 1e9) }
func (g vgrad) At(x, y int) color.Color {
	t := math.Max(0, math.Min(1, (float64(y)-g.y0)/(g.y1-g.y0)))
	m := func(a, b uint8) uint8 { return uint8(float64(a) + (float64(b)-float64(a))*t) }
	return color.NRGBA{m(g.top.R, g.bottom.R), m(g.top.G, g.bottom.G), m(g.top.B, g.bottom.B), m(g.top.A, g.bottom.A)}
}

func main() {
	out := flag.String("out", "packaging/logo-source.png", "output PNG")
	flag.Parse()
	img := image.NewNRGBA(image.Rect(0, 0, S, S))
	// tile
	fill(img, func(r *vector.Rasterizer) { roundRect(r, 64, 64, S-64, S-64, 210) },
		vgrad{rgb(0x242936, 255), rgb(0x0b0e14, 255), 64, S - 64})
	// glow behind the cylinder
	glow := image.NewNRGBA(img.Bounds())
	for y := 0; y < S; y++ {
		for x := 0; x < S; x++ {
			d := math.Hypot(float64(x-512), float64(y-560)) / 360
			if d < 1 {
				a := uint8(70 * (1 - d) * (1 - d))
				glow.SetNRGBA(x, y, rgb(0xe6b450, a))
			}
		}
	}
	mask := image.NewAlpha(img.Bounds())
	mr := vector.NewRasterizer(S, S)
	roundRect(mr, 64, 64, S-64, S-64, 210)
	mr.Draw(mask, mask.Bounds(), image.Opaque, image.Point{})
	draw.DrawMask(img, img.Bounds(), glow, image.Point{}, mask, image.Point{}, draw.Over)

	cx, rx, ry := float32(512), float32(250), float32(78)
	gold := vgrad{rgb(0xffcc66, 255), rgb(0xe08a1e, 255), 300, 820}
	// three stacked drums, bottom first
	for i, top := range []float32{640, 500, 360} {
		h := float32(118)
		fill(img, func(r *vector.Rasterizer) {
			ellipse(r, cx, top, rx, ry, math.Pi, 0, true)       // back of the top rim
			r.LineTo(cx+rx, top+h)                              // right side
			ellipse(r, cx, top+h, rx, ry, 0, math.Pi, false)     // front of the bottom rim
			r.LineTo(cx-rx, top)
			r.ClosePath()
		}, gold)
		// band separator shadow
		fill(img, func(r *vector.Rasterizer) {
			ellipse(r, cx, top+h, rx, ry, 0, math.Pi, true)
			ellipse(r, cx, top+h-10, rx, ry, math.Pi, 0, false)
			r.ClosePath()
		}, image.NewUniform(rgb(0x8a4b10, 150)))
		_ = i
	}
	// top face
	fill(img, func(r *vector.Rasterizer) { ellipse(r, cx, 360, rx, ry, 0, 2*math.Pi, true); r.ClosePath() },
		vgrad{rgb(0xfff0c0, 255), rgb(0xffd173, 255), 282, 438})
	fill(img, func(r *vector.Rasterizer) { ellipse(r, cx, 360, rx-36, ry-16, 0, 2*math.Pi, true); r.ClosePath() },
		image.NewUniform(rgb(0xffffff, 60)))
	// spark (4-point star) top right
	star := func(x, y, R, rIn float32, c color.NRGBA) {
		fill(img, func(r *vector.Rasterizer) {
			for i := 0; i < 8; i++ {
				a := float64(i) * math.Pi / 4
				rad := R
				if i%2 == 1 {
					rad = rIn
				}
				px, py := x+rad*float32(math.Sin(a)), y-rad*float32(math.Cos(a))
				if i == 0 {
					r.MoveTo(px, py)
				} else {
					r.LineTo(px, py)
				}
			}
			r.ClosePath()
		}, image.NewUniform(c))
	}
	star(770, 250, 120, 26, rgb(0x73d0ff, 255))
	star(770, 250, 60, 13, rgb(0xffffff, 230))
	star(690, 170, 42, 10, rgb(0x95e6cb, 230))

	f, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		log.Fatal(err)
	}
}
