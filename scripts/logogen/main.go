// Command logogen turns a logo artwork into packaging/logo-source.png
// (1024×1024 RGBA): the rounded tile of -src is cut out of its flat dark
// surround (made transparent) and centered in the same 64 px safe-area
// margin the icons always had. Run it, then `make icons`.
//
//	go run ./scripts/logogen -src path/to/artwork.png [-black 28]
//
// -black is the brightest channel of a surround pixel: raise it when the
// surround is not pure black (it must stay below the tile's rim).
package main

import (
	"flag"
	"image"
	_ "image/jpeg"
	"image/png"
	"log"
	"os"

	"golang.org/x/image/draw"
)

const (
	S      = 1024 // output size
	margin = 64   // transparent safe area around the tile
)

// cutout returns the tile of art (whatever is not reachable from the image
// border through dark pixels, max channel <= black) on a transparent S×S
// canvas.
func cutout(art image.Image, black uint8) *image.NRGBA {
	b := art.Bounds()
	w, h := b.Dx(), b.Dy()
	src := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(src, src.Rect, art, b.Min, draw.Src)
	dark := func(i int) bool {
		p := src.Pix[i*4 : i*4+4]
		return p[3] == 0 || max(p[0], p[1], p[2]) <= black
	}
	// flood fill the surround from every border pixel
	out := make([]bool, w*h)
	var stack []int
	push := func(x, y int) {
		if x < 0 || y < 0 || x >= w || y >= h {
			return
		}
		if i := y*w + x; !out[i] && dark(i) {
			out[i] = true
			stack = append(stack, i)
		}
	}
	for x := 0; x < w; x++ {
		push(x, 0)
		push(x, h-1)
	}
	for y := 0; y < h; y++ {
		push(0, y)
		push(w-1, y)
	}
	for len(stack) > 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		x, y := i%w, i/w
		push(x+1, y)
		push(x-1, y)
		push(x, y+1)
		push(x, y-1)
	}
	tile := image.Rectangle{}
	for i, o := range out {
		if o {
			copy(src.Pix[i*4:i*4+4], []uint8{0, 0, 0, 0})
		} else {
			tile = tile.Union(image.Rect(i%w, i/w, i%w+1, i/w+1))
		}
	}
	dst := image.NewNRGBA(image.Rect(0, 0, S, S))
	if tile.Empty() {
		return dst
	}
	// fit the tile in the safe area, keeping its aspect ratio
	side := S - 2*margin
	tw, th := side, side
	if tile.Dx() > tile.Dy() {
		th = side * tile.Dy() / tile.Dx()
	} else {
		tw = side * tile.Dx() / tile.Dy()
	}
	at := image.Rect((S-tw)/2, (S-th)/2, (S-tw)/2+tw, (S-th)/2+th)
	draw.CatmullRom.Scale(dst, at, src, tile, draw.Src, nil)
	return dst
}

func main() {
	in := flag.String("src", "", "logo artwork (rounded tile on a black or transparent surround)")
	out := flag.String("out", "packaging/logo-source.png", "output PNG")
	black := flag.Uint("black", 10, "max channel (0–255) of a surround pixel")
	flag.Parse()
	if *in == "" {
		log.Fatal("-src is required")
	}
	f, err := os.Open(*in)
	if err != nil {
		log.Fatal(err)
	}
	art, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		log.Fatal(err)
	}
	o, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	defer o.Close()
	if err := png.Encode(o, cutout(art, uint8(min(*black, 255)))); err != nil {
		log.Fatal(err)
	}
}
