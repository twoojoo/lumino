// Command icongen builds every icon file from the logo artwork
// (packaging/logo-source.png, 1024×1024 RGBA): PNGs 16–1024 for Linux,
// kubeon.ico (Windows) and kubeon.icns (macOS) in -out, and the tile-only
// logo the UI draws in -ui.
//
//	go run ./scripts/icongen
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/png"
	"log"
	"os"
	"path/filepath"

	"golang.org/x/image/draw"
)

// resize scales src to size×size with a high-quality filter.
func resize(src image.Image, size int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	draw.CatmullRom.Scale(dst, dst.Rect, src, src.Bounds(), draw.Src, nil)
	return dst
}

// tileBounds is the opaque part of the artwork (the rounded tile).
func tileBounds(img image.Image) image.Rectangle {
	b := img.Bounds()
	r := image.Rectangle{Min: b.Max, Max: b.Min}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a > 0x8000 {
				r = r.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return r
}

func encode(img image.Image) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		log.Fatal(err)
	}
	return b.Bytes()
}

// writeICO stores PNG-compressed entries (Windows Vista+).
func writeICO(path string, sizes []int, pngs map[int][]byte) error {
	var b bytes.Buffer
	le := binary.LittleEndian
	_ = binary.Write(&b, le, []uint16{0, 1, uint16(len(sizes))})
	off := 6 + 16*len(sizes)
	for _, s := range sizes {
		d := uint8(s)
		if s >= 256 {
			d = 0
		}
		_ = binary.Write(&b, le, []uint8{d, d, 0, 0})
		_ = binary.Write(&b, le, []uint16{1, 32})
		_ = binary.Write(&b, le, []uint32{uint32(len(pngs[s])), uint32(off)})
		off += len(pngs[s])
	}
	for _, s := range sizes {
		b.Write(pngs[s])
	}
	return os.WriteFile(path, b.Bytes(), 0o644)
}

// writeICNS stores PNG entries (macOS 10.7+).
func writeICNS(path string, pngs map[int][]byte) error {
	types := []struct {
		t string
		s int
	}{{"icp4", 16}, {"icp5", 32}, {"icp6", 64}, {"ic07", 128}, {"ic08", 256}, {"ic09", 512}, {"ic10", 1024}, {"ic11", 32}, {"ic12", 64}, {"ic13", 256}, {"ic14", 512}}
	var body bytes.Buffer
	for _, e := range types {
		body.WriteString(e.t)
		_ = binary.Write(&body, binary.BigEndian, uint32(8+len(pngs[e.s])))
		body.Write(pngs[e.s])
	}
	var b bytes.Buffer
	b.WriteString("icns")
	_ = binary.Write(&b, binary.BigEndian, uint32(8+body.Len()))
	b.Write(body.Bytes())
	return os.WriteFile(path, b.Bytes(), 0o644)
}

func main() {
	src := flag.String("src", "packaging/logo-source.png", "1024×1024 logo artwork")
	out := flag.String("out", "packaging/icons", "output directory")
	ui := flag.String("ui", "internal/ui/assets", "UI assets directory")
	flag.Parse()
	f, err := os.Open(*src)
	if err != nil {
		log.Fatal(err)
	}
	art, err := png.Decode(f)
	f.Close()
	if err != nil {
		log.Fatal(err)
	}
	for _, d := range []string{*out, *ui} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			log.Fatal(err)
		}
	}
	sizes := []int{16, 24, 32, 48, 64, 128, 256, 512, 1024}
	pngs := map[int][]byte{}
	for _, s := range sizes {
		pngs[s] = encode(resize(art, s))
		if err := os.WriteFile(filepath.Join(*out, fmt.Sprintf("kubeon-%d.png", s)), pngs[s], 0o644); err != nil {
			log.Fatal(err)
		}
	}
	if err := writeICO(filepath.Join(*out, "kubeon.ico"), []int{16, 24, 32, 48, 64, 128, 256}, pngs); err != nil {
		log.Fatal(err)
	}
	if err := writeICNS(filepath.Join(*out, "kubeon.icns"), pngs); err != nil {
		log.Fatal(err)
	}
	// UI: the tile without the transparent safe-area margin, sized for
	// HiDPI title bars (24dp at up to ~4x) and the welcome screen
	tile := image.NewNRGBA(image.Rect(0, 0, 0, 0))
	if sub, ok := art.(interface {
		SubImage(image.Rectangle) image.Image
	}); ok {
		t := sub.SubImage(tileBounds(art))
		tile = resize(t, 512) // the UI scales it down per size (internal/ui/logo.go)
	}
	if err := os.WriteFile(filepath.Join(*ui, "logo.png"), encode(tile), 0o644); err != nil {
		log.Fatal(err)
	}
}
