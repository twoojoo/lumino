package ui

import (
	"image"

	"gioui.org/f32"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

const titleBarHeight = unit.Dp(40)

// TitleBar replaces the platform/Gio decorations (app.Decorated(false)).
// Empty regions are move areas: drag moves the window (also when
// maximized), double-click toggles maximize, right-click opens the window
// menu (see third_party/_gio-patches).
type TitleBar struct {
	app                      *App
	minBtn, maxBtn, closeBtn widget.Clickable
	searchBtn, aiBtn         widget.Clickable
	themeBtn, prefsBtn       widget.Clickable
}

func newTitleBar(a *App) *TitleBar { return &TitleBar{app: a} }

func (t *TitleBar) perform(act system.Action) {
	if t.app.w != nil {
		t.app.w.Perform(act)
	}
}

func (t *TitleBar) Layout(gtx C) D {
	a := t.app
	th := a.th
	if t.minBtn.Clicked(gtx) {
		t.perform(system.ActionMinimize)
	}
	if t.maxBtn.Clicked(gtx) {
		if a.maximized {
			t.perform(system.ActionUnmaximize)
		} else {
			t.perform(system.ActionMaximize)
		}
	}
	if t.closeBtn.Clicked(gtx) {
		a.requestClose(func() { t.perform(system.ActionClose) })
	}
	if t.prefsBtn.Clicked(gtx) {
		a.overlay = newPrefsPanel(a)
	}
	if t.themeBtn.Clicked(gtx) {
		a.toggleTheme()
	}
	if t.aiBtn.Clicked(gtx) {
		a.ai.open = !a.ai.open
	}
	if t.searchBtn.Clicked(gtx) {
		a.openPalette()
	}

	h := gtx.Dp(titleBarHeight)
	w := gtx.Constraints.Max.X
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	dim := !a.winFocused && a.w != nil
	fg := pal.TextBright
	if dim {
		fg = pal.TextDim
	}
	// same surface as the sidebar/panels; the brand block lines up with
	// the sidebar via a divider
	fillRect(gtx, pal.Sidebar, image.Rect(0, 0, w, h))
	sideW := a.sidebarWidth(gtx)
	fillRect(gtx, pal.Border, image.Rect(sideW, gtx.Dp(8), sideW+gtx.Dp(1), h-gtx.Dp(8)))
	fillRect(gtx, pal.Border, image.Rect(0, h-gtx.Dp(1), w, h))

	ctrlW := 3 * gtx.Dp(46)
	brandX := gtx.Dp(14)
	if nativeWindowControls {
		ctrlW, brandX = 0, gtx.Dp(trafficLightsWidth)
	}
	bw := min(gtx.Dp(420), max(gtx.Dp(160), w-2*sideW-ctrlW))
	bh := gtx.Dp(28)
	sx := (w - bw) / 2

	nsX := sideW + gtx.Dp(10)
	var nsDims D
	_ = nsX
	// drag handles on the empty regions
	moveRect := func(r image.Rectangle) {
		if r.Dx() <= 0 {
			return
		}
		st := clip.Rect(r).Push(gtx.Ops)
		system.ActionInputOp(system.ActionMove).Add(gtx.Ops)
		st.Pop()
	}
	moveRect(image.Rect(0, 0, sx, h))
	// theme toggle + assistant, left of the window controls
	var aiCall op.CallOp
	var aiDims D
	{
		m := op.Record(gtx.Ops)
		ag := gtx
		ag.Constraints = layout.Constraints{Max: image.Pt(gtx.Dp(220), h)}
		aiDims = layout.Flex{Alignment: layout.Middle}.Layout(ag,
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Right: 2}.Layout(gtx, func(gtx C) D { return iconOnly(gtx, &t.prefsBtn, icSettings, 16) })
			}),
			layout.Rigid(func(gtx C) D {
				ic := icMoon
				if pal.Light {
					ic = icSun
				}
				_ = nsDims
				return layout.Inset{Right: 4}.Layout(gtx, func(gtx C) D { return iconOnly(gtx, &t.themeBtn, ic, 16) })
			}),
			layout.Rigid(func(gtx C) D { return t.aiButton(gtx) }),
		)
		aiCall = m.Stop()
	}

	edge := gtx.Dp(8)
	if ctrlW == 0 {
		edge = gtx.Dp(14)
	}
	aiX := w - ctrlW - edge - aiDims.Size.X
	// policy badges, right-aligned before the toolbar buttons
	{
		m := op.Record(gtx.Ops)
		bg := gtx
		bg.Constraints = layout.Constraints{Max: image.Pt(gtx.Dp(260), h)}
		bd := D{}
		if tab := a.activeTab(); tab != nil {
			bd = a.connBadges(bg, tab.ConnID())
		}
		call := m.Stop()
		if bd.Size.X > 0 {
			x := max(sx+bw+gtx.Dp(8), aiX-bd.Size.X-gtx.Dp(12))
			st := op.Offset(image.Pt(x, (h-bd.Size.Y)/2)).Push(gtx.Ops)
			call.Add(gtx.Ops)
			st.Pop()
		}
	}
	moveRect(image.Rect(sx+bw, 0, aiX, h))
	if aiDims.Size.X > 0 {
		moveRect(image.Rect(aiX+aiDims.Size.X, 0, w-ctrlW, h))
		st := op.Offset(image.Pt(aiX, (h-aiDims.Size.Y)/2)).Push(gtx.Ops)
		aiCall.Add(gtx.Ops)
		st.Pop()
	}

	// brand, aligned with the sidebar
	st := op.Offset(image.Pt(brandX, 0)).Push(gtx.Ops)
	bg := gtx
	bg.Constraints = layout.Exact(image.Pt(sideW-brandX, h))
	layout.W.Layout(bg, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return t.logo(gtx) }),
			layout.Rigid(layout.Spacer{Width: 10}.Layout),
			layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontSans, 14, fg, AppName) }),
		)
	})
	st.Pop()

	// command/search box, centered on the window
	{
		st := op.Offset(image.Pt(sx, (h-bh)/2)).Push(gtx.Ops)
		sg := gtx
		sg.Constraints = layout.Exact(image.Pt(bw, bh))
		t.searchBtn.Layout(sg, func(gtx C) D {
			b := pal.Border
			if t.searchBtn.Hovered() {
				b = pal.TextDim
			}
			return rborder(gtx, b, pal.Bg, 7, func(gtx C) D {
				gtx.Constraints.Min = gtx.Constraints.Max
				return insetHV(10, 0).Layout(gtx, func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx C) D { return mdi(gtx, icSearch, pal.TextDim, 15) }),
						layout.Rigid(layout.Spacer{Width: 8}.Layout),
						layout.Flexed(1, func(gtx C) D {
							gtx.Constraints.Min.Y = gtx.Constraints.Max.Y
							return layout.W.Layout(gtx, func(gtx C) D {
								return lbl(gtx, th, fontSans, 12, pal.TextDim, "Search objects, connections, commands…")
							})
						}),
						layout.Rigid(func(gtx C) D { return keyChip(gtx, th, shortcutLabel("K")) }),
					)
				})
			})
		})
		st.Pop()
	}

	if nativeWindowControls {
		return D{Size: image.Pt(w, h)}
	}
	// window controls
	st = op.Offset(image.Pt(w-ctrlW, 0)).Push(gtx.Ops)
	cg := gtx
	cg.Constraints = layout.Exact(image.Pt(ctrlW, h))
	layout.Flex{}.Layout(cg,
		layout.Rigid(func(gtx C) D { return t.winButton(gtx, &t.minBtn, glyphMinimize, false) }),
		layout.Rigid(func(gtx C) D {
			g := glyphMaximize
			if a.maximized {
				g = glyphRestore
			}
			return t.winButton(gtx, &t.maxBtn, g, false)
		}),
		layout.Rigid(func(gtx C) D { return t.winButton(gtx, &t.closeBtn, glyphClose, true) }),
	)
	st.Pop()
	return D{Size: image.Pt(w, h)}
}

func keyChip(gtx C, th *material.Theme, k string) D {
	return rborder(gtx, pal.Border, pal.Panel, 4, func(gtx C) D {
		return insetHV(5, 1).Layout(gtx, func(gtx C) D {
			return lbl(gtx, th, fontSans, 10, pal.TextDim, k)
		})
	})
}

// logo draws the app mark.
func (t *TitleBar) logo(gtx C) D { return drawLogo(gtx, 24) }

type winGlyph int

const (
	glyphMinimize winGlyph = iota
	glyphMaximize
	glyphRestore
	glyphClose
)

func (t *TitleBar) winButton(gtx C, cl *widget.Clickable, g winGlyph, danger bool) D {
	w, h := gtx.Dp(46), gtx.Dp(titleBarHeight)
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	return cl.Layout(gtx, func(gtx C) D {
		c := pal.Text
		if !t.app.winFocused && t.app.w != nil {
			c = pal.TextDim
		}
		if cl.Hovered() {
			if danger {
				fillRect(gtx, rgb(0xd83b3b), image.Rect(0, 0, w, h-gtx.Dp(1)))
				c = pal.OnAccent
			} else {
				fillRect(gtx, pal.Hover, image.Rect(0, 0, w, h-gtx.Dp(1)))
				c = pal.TextBright
			}
		}
		s := float32(gtx.Dp(10))
		ox, oy := (float32(w)-s)/2, (float32(h)-s)/2
		sw := float32(gtx.Dp(1.2))
		var p clip.Path
		p.Begin(gtx.Ops)
		rectPath := func(x, y, rw, rh float32) {
			p.MoveTo(f32.Pt(x, y))
			p.LineTo(f32.Pt(x+rw, y))
			p.LineTo(f32.Pt(x+rw, y+rh))
			p.LineTo(f32.Pt(x, y+rh))
			p.Close()
		}
		switch g {
		case glyphMinimize:
			p.MoveTo(f32.Pt(ox, oy+s/2+sw))
			p.LineTo(f32.Pt(ox+s, oy+s/2+sw))
		case glyphMaximize:
			rectPath(ox, oy, s, s)
		case glyphRestore:
			o := s * 0.25
			rectPath(ox, oy+o, s-o, s-o)
			p.MoveTo(f32.Pt(ox+o, oy+o))
			p.LineTo(f32.Pt(ox+o, oy))
			p.LineTo(f32.Pt(ox+s, oy))
			p.LineTo(f32.Pt(ox+s, oy+s-o))
			p.LineTo(f32.Pt(ox+s-o, oy+s-o))
		case glyphClose:
			p.MoveTo(f32.Pt(ox, oy))
			p.LineTo(f32.Pt(ox+s, oy+s))
			p.MoveTo(f32.Pt(ox+s, oy))
			p.LineTo(f32.Pt(ox, oy+s))
		}
		paint.FillShape(gtx.Ops, c, clip.Stroke{Path: p.End(), Width: sw}.Op())
		return D{Size: image.Pt(w, h)}
	})
}

func (t *TitleBar) aiButton(gtx C) D {
	a, th := t.app, t.app.th
	return t.aiBtn.Layout(gtx, func(gtx C) D {
		b, c := colorNRGBA{}, pal.Text
		switch {
		case a.ai.open:
			b, c = withAlpha(pal.Accent, 0x40), pal.TextBright
		case t.aiBtn.Hovered():
			b = pal.Hover
		}
		return rbg(gtx, b, 6, func(gtx C) D {
			return insetHV(9, 5).Layout(gtx, func(gtx C) D {
				return iconText(gtx, th, icAI, 12, c, "Assistant")
			})
		})
	})
}
