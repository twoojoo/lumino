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

	"github.com/giovanni/lumino/internal/backend"
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
	CodeBg, TermBg                                         color.NRGBA
	Backdrop                                               color.NRGBA // modal dimming
	Light                                                  bool
}

var darkPal = Palette{
	Bg:         rgb(0x1e2124),
	Sidebar:    rgb(0x252a2e),
	Panel:      rgb(0x2b3035),
	Header:     rgb(0x262b2f),
	Border:     rgb(0x363c42),
	Hover:      rgb(0x30363c),
	Selected:   rgb(0x2f4a63),
	Text:       rgb(0xb8bcc2),
	TextDim:    rgb(0x7d848c),
	TextBright: rgb(0xeceef1),
	Accent:     rgb(0x3d90ce),
	Ok:         rgb(0x4caf50),
	Warn:       rgb(0xff9800),
	Err:        rgb(0xe85555),
	Info:       rgb(0x5fb3e8),
	OnAccent:   rgb(0xffffff),
	CodeBg:     rgb(0x16191b),
	TermBg:     rgb(0x101214),
	Backdrop:   color.NRGBA{A: 0x90},
}

var lightPal = Palette{
	Bg:         rgb(0xf6f7f9),
	Sidebar:    rgb(0xeceff2),
	Panel:      rgb(0xffffff),
	Header:     rgb(0xf1f3f6),
	Border:     rgb(0xd5dae0),
	Hover:      rgb(0xe3e7ec),
	Selected:   rgb(0xd3e5f6),
	Text:       rgb(0x262c33),
	TextDim:    rgb(0x5d6570),
	TextBright: rgb(0x0d1014),
	Accent:     rgb(0x2b7bbd),
	Ok:         rgb(0x2a9446),
	Warn:       rgb(0xc96f00),
	Err:        rgb(0xd03c3c),
	Info:       rgb(0x1f7fc4),
	OnAccent:   rgb(0xffffff),
	CodeBg:     rgb(0xeff1f4),
	TermBg:     rgb(0xfbfbfc),
	Backdrop:   color.NRGBA{R: 0x20, G: 0x26, B: 0x2e, A: 0x55},
	Light:      true,
}

var pal = darkPal

// setTheme switches the palette ("dark" | "light").
func setTheme(th *material.Theme, name string) {
	pal = darkPal
	fontSans = font.Font{Typeface: "Go"}
	if name == "light" {
		pal = lightPal
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
	case pal.Accent, pal.Err, pal.Warn, pal.Ok, pal.Info:
		return pal.OnAccent
	}
	return fallback
}

func rgb(c uint32) color.NRGBA {
	return color.NRGBA{R: uint8(c >> 16), G: uint8(c >> 8), B: uint8(c), A: 0xff}
}

func stateColor(s backend.RowState) color.NRGBA {
	switch s {
	case backend.StateAdd:
		return pal.Info
	case backend.StateMod:
		return pal.Text
	case backend.StatePending:
		return pal.Warn
	case backend.StateErr:
		return pal.Err
	case backend.StateKill:
		return pal.TextDim
	case backend.StateCompleted:
		return pal.TextDim
	case backend.StateHighlight:
		return pal.Accent
	default:
		return pal.Text
	}
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

var textOps = map[color.NRGBA]op.CallOp{}

// colorMat returns a cached paint material for text.
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
