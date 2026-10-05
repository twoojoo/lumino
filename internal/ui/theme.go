package ui

import (
	"image"
	"image/color"

	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

type (
	C = layout.Context
	D = layout.Dimensions
)

// Palette holds the theme colors. pal is swapped at runtime by setTheme.
type Palette struct {
	Bg, Sidebar, Panel, Header, Border, Hover, Selected    color.NRGBA
	Text, TextDim, TextBright, Accent, Ok, Warn, Err, Info color.NRGBA
	OnAccent                                               color.NRGBA // text on accent/status-colored surfaces
	CodeBg, LineHL                                         color.NRGBA // editor background, current line
	Backdrop                                               color.NRGBA // modal dimming
	// syntax
	Keyword, String, Number, Comment, Func, Type, Operator, Param, Constant color.NRGBA
	// data grid
	Null, RowAlt, Modified, Inserted, Deleted, CellSel color.NRGBA
	Light                                              bool
}

// Ayu (https://github.com/ayu-theme/ayu-colors), dark/mirage/light.
var ayuDark = Palette{
	Bg:         rgb(0x0d1017),
	Sidebar:    rgb(0x0f131a),
	Panel:      rgb(0x141821),
	Header:     rgb(0x11151c),
	Border:     rgb(0x1f2430),
	Hover:      rgb(0x161b24),
	Selected:   rgb(0x1b2a3d),
	Text:       rgb(0xbfbdb6),
	TextDim:    rgb(0x6c7380),
	TextBright: rgb(0xe6e1cf),
	Accent:     rgb(0xe6b450),
	Ok:         rgb(0x7fd962),
	Warn:       rgb(0xffb454),
	Err:        rgb(0xd95757),
	Info:       rgb(0x59c2ff),
	OnAccent:   rgb(0x0d1017),
	CodeBg:     rgb(0x0b0e14),
	LineHL:     rgb(0x131721),
	Backdrop:   color.NRGBA{A: 0xa0},
	Keyword:    rgb(0xff8f40),
	String:     rgb(0xaad94c),
	Number:     rgb(0xd2a6ff),
	Comment:    rgb(0x5c6773),
	Func:       rgb(0xffb454),
	Type:       rgb(0x39bae6),
	Operator:   rgb(0xf29668),
	Param:      rgb(0x95e6cb),
	Constant:   rgb(0xd2a6ff),
	Null:       rgb(0x565b66),
	RowAlt:     rgb(0x0f131a),
	Modified:   withAlpha(rgb(0x73b8ff), 0x30),
	Inserted:   withAlpha(rgb(0x7fd962), 0x26),
	Deleted:    withAlpha(rgb(0xf26d78), 0x2c),
	CellSel:    withAlpha(rgb(0x409fff), 0x40),
}

var ayuMirage = Palette{
	Bg:         rgb(0x1f2430),
	Sidebar:    rgb(0x1c212b),
	Panel:      rgb(0x242936),
	Header:     rgb(0x1a1f29),
	Border:     rgb(0x2d3340),
	Hover:      rgb(0x272d38),
	Selected:   rgb(0x33415e),
	Text:       rgb(0xcccac2),
	TextDim:    rgb(0x707a8c),
	TextBright: rgb(0xf3f0e6),
	Accent:     rgb(0xffcc66),
	Ok:         rgb(0x87d96c),
	Warn:       rgb(0xffad66),
	Err:        rgb(0xff6666),
	Info:       rgb(0x73d0ff),
	OnAccent:   rgb(0x1f2430),
	CodeBg:     rgb(0x1a1f29),
	LineHL:     rgb(0x1a1f29),
	Backdrop:   color.NRGBA{A: 0x90},
	Keyword:    rgb(0xffad66),
	String:     rgb(0xd5ff80),
	Number:     rgb(0xdfbfff),
	Comment:    rgb(0x6e7c8f),
	Func:       rgb(0xffd173),
	Type:       rgb(0x5ccfe6),
	Operator:   rgb(0xf29e74),
	Param:      rgb(0x95e6cb),
	Constant:   rgb(0xdfbfff),
	Null:       rgb(0x6e7c8f),
	RowAlt:     rgb(0x1c212b),
	Modified:   withAlpha(rgb(0x80bfff), 0x30),
	Inserted:   withAlpha(rgb(0x87d96c), 0x26),
	Deleted:    withAlpha(rgb(0xf27983), 0x2c),
	CellSel:    withAlpha(rgb(0x409fff), 0x40),
}

var ayuLight = Palette{
	Bg:         rgb(0xfcfcfc),
	Sidebar:    rgb(0xf3f4f5),
	Panel:      rgb(0xffffff),
	Header:     rgb(0xf0f1f2),
	Border:     rgb(0xdcdfe3),
	Hover:      rgb(0xe9ebed),
	Selected:   rgb(0xd5e6f7),
	Text:       rgb(0x5c6166),
	TextDim:    rgb(0x8a9199),
	TextBright: rgb(0x242936),
	Accent:     rgb(0xf29718),
	Ok:         rgb(0x4ea81f),
	Warn:       rgb(0xe08a00),
	Err:        rgb(0xe65050),
	Info:       rgb(0x399ee6),
	OnAccent:   rgb(0xffffff),
	CodeBg:     rgb(0xfafafa),
	LineHL:     rgb(0xf0f1f2),
	Backdrop:   color.NRGBA{R: 0x20, G: 0x26, B: 0x2e, A: 0x55},
	Keyword:    rgb(0xfa8d3e),
	String:     rgb(0x86b300),
	Number:     rgb(0xa37acc),
	Comment:    rgb(0xa0a6ac),
	Func:       rgb(0xf2ae49),
	Type:       rgb(0x399ee6),
	Operator:   rgb(0xed9366),
	Param:      rgb(0x4cbf99),
	Constant:   rgb(0xa37acc),
	Null:       rgb(0xadb3ba),
	RowAlt:     rgb(0xf6f7f8),
	Modified:   withAlpha(rgb(0x478acc), 0x26),
	Inserted:   withAlpha(rgb(0x6cbf43), 0x26),
	Deleted:    withAlpha(rgb(0xff7383), 0x2c),
	CellSel:    withAlpha(rgb(0x399ee6), 0x38),
	Light:      true,
}

// Themes lists the selectable themes.
var Themes = []struct{ ID, Name string }{
	{"ayu-dark", "Ayu Dark"},
	{"ayu-mirage", "Ayu Mirage"},
	{"ayu-light", "Ayu Light"},
}

var pal = ayuDark

// setTheme switches the palette.
func setTheme(th *material.Theme, name string) {
	pal = ayuDark
	fontSans = font.Font{Typeface: "Go"}
	switch name {
	case "ayu-mirage":
		pal = ayuMirage
	case "ayu-light":
		pal = ayuLight
		// Gio blends text in linear space without stem darkening: dark
		// glyphs on a light background come out thin and grey. Go Medium
		// (its own family in the Go fonts) compensates; light text on dark
		// backgrounds doesn't need it.
		fontSans = font.Font{Typeface: "Go Medium", Weight: font.Medium}
	}
	if th != nil {
		th.Palette = material.Palette{Bg: pal.Bg, Fg: pal.Text, ContrastBg: pal.Accent, ContrastFg: pal.OnAccent}
	}
}

// onColor picks readable text for a filled surface.
func onColor(bg, fallback color.NRGBA) color.NRGBA {
	switch bg {
	case pal.Accent:
		return pal.OnAccent
	case pal.Err, pal.Warn, pal.Ok, pal.Info:
		return rgb(0xffffff)
	}
	return fallback
}

func rgb(c uint32) color.NRGBA {
	return color.NRGBA{R: uint8(c >> 16), G: uint8(c >> 8), B: uint8(c), A: 0xff}
}

// parseHex parses #rrggbb (connection colors).
func parseHex(s string) (color.NRGBA, bool) {
	if len(s) != 7 || s[0] != '#' {
		return color.NRGBA{}, false
	}
	var v uint32
	for _, c := range s[1:] {
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= uint32(c - '0')
		case c >= 'a' && c <= 'f':
			v |= uint32(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v |= uint32(c-'A') + 10
		default:
			return color.NRGBA{}, false
		}
	}
	return rgb(v), true
}

var (
	fontSans = font.Font{Typeface: "Go"} // "Go Medium" in the light theme (setTheme)
	fontMono = font.Font{Typeface: "Go Mono"}
	fontBold = font.Font{Typeface: "Go", Weight: font.SemiBold} // the Go fonts' bold face
)

func newTheme() *material.Theme {
	th := material.NewTheme()
	th.Shaper = text.NewShaper(text.WithCollection(gofont.Collection()), text.NoSystemFonts())
	th.Palette = material.Palette{
		Bg:         pal.Bg,
		Fg:         pal.Text,
		ContrastBg: pal.Accent,
		ContrastFg: pal.TextBright,
	}
	th.TextSize = 13
	th.Face = "Go"
	return th
}

func fill(gtx C, c color.NRGBA) D {
	sz := gtx.Constraints.Min
	paint.FillShape(gtx.Ops, c, clip.Rect{Max: sz}.Op())
	return D{Size: sz}
}

func fillRect(gtx C, c color.NRGBA, r image.Rectangle) {
	paint.FillShape(gtx.Ops, c, clip.Rect(r).Op())
}

// bg paints c behind w, sized to w.
func bg(gtx C, c color.NRGBA, w layout.Widget) D {
	m := op.Record(gtx.Ops)
	d := w(gtx)
	call := m.Stop()
	fillRect(gtx, c, image.Rectangle{Max: d.Size})
	call.Add(gtx.Ops)
	return d
}

func hline(gtx C, c color.NRGBA) D {
	h := gtx.Dp(1)
	fillRect(gtx, c, image.Rect(0, 0, gtx.Constraints.Max.X, h))
	return D{Size: image.Pt(gtx.Constraints.Max.X, h)}
}

func vline(gtx C, c color.NRGBA) D {
	w := gtx.Dp(1)
	fillRect(gtx, c, image.Rect(0, 0, w, gtx.Constraints.Max.Y))
	return D{Size: image.Pt(w, gtx.Constraints.Max.Y)}
}

// colorMat returns a paint material for text.
func colorMat(gtx C, c color.NRGBA) op.CallOp {
	m := op.Record(gtx.Ops)
	paint.ColorOp{Color: c}.Add(gtx.Ops)
	return m.Stop()
}

// lbl draws a single-line truncated label.
func lbl(gtx C, th *material.Theme, f font.Font, size unit.Sp, c color.NRGBA, s string) D {
	gtx.Constraints.Min.Y = 0 // let W/E directions center the line vertically
	l := widget.Label{MaxLines: 1, Truncator: "…", LineHeightScale: 1.05}
	return l.Layout(gtx, th.Shaper, f, size, s, colorMat(gtx, c))
}

func lblAlign(gtx C, th *material.Theme, f font.Font, size unit.Sp, c color.NRGBA, s string, a text.Alignment) D {
	gtx.Constraints.Min.Y = 0
	l := widget.Label{MaxLines: 1, Truncator: "…", Alignment: a, LineHeightScale: 1.05}
	return l.Layout(gtx, th.Shaper, f, size, s, colorMat(gtx, c))
}

func inset(v unit.Dp) layout.Inset { return layout.UniformInset(v) }

func insetHV(h, v unit.Dp) layout.Inset {
	return layout.Inset{Left: h, Right: h, Top: v, Bottom: v}
}

// button draws a flat Lens-like button.
func button(gtx C, th *material.Theme, cl *widget.Clickable, txt string, c color.NRGBA) D {
	return cl.Layout(gtx, func(gtx C) D {
		bgc := c
		if cl.Hovered() {
			bgc = lighten(c, 0.12)
		}
		return rbg(gtx, bgc, 6, func(gtx C) D {
			return insetHV(12, 6).Layout(gtx, func(gtx C) D {
				return lbl(gtx, th, fontSans, 13, pal.OnAccent, txt)
			})
		})
	})
}

// flatButton is a text-only button.
func flatButton(gtx C, th *material.Theme, cl *widget.Clickable, txt string, active bool) D {
	return cl.Layout(gtx, func(gtx C) D {
		c := pal.TextDim
		if active || cl.Hovered() {
			c = pal.TextBright
		}
		return insetHV(8, 4).Layout(gtx, func(gtx C) D {
			return lbl(gtx, th, fontSans, 13, c, txt)
		})
	})
}

func lighten(c color.NRGBA, f float32) color.NRGBA {
	l := func(v uint8) uint8 { return uint8(float32(v) + (255-float32(v))*f) }
	return color.NRGBA{R: l(c.R), G: l(c.G), B: l(c.B), A: c.A}
}

func withAlpha(c color.NRGBA, a uint8) color.NRGBA { c.A = a; return c }

func rect(x0, y0, x1, y1 int) image.Rectangle { return image.Rect(x0, y0, x1, y1) }

type colorNRGBA = color.NRGBA

type fontT = font.Font

func unitDp(v int) unit.Dp { return unit.Dp(v) }
