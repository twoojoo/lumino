package ui

import (
	"image"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// Vector icons: the bundled Go fonts lack most UI symbols.
type iconKind int

const (
	iconChevronDown iconKind = iota
	iconChevronRight
	iconChevronUp
	iconClose
)

func drawIcon(gtx C, k iconKind, c colorNRGBA, size unit.Dp) D {
	s := float32(gtx.Dp(size))
	sz := image.Pt(int(s), int(s))
	var p clip.Path
	p.Begin(gtx.Ops)
	stroke := s * 0.13
	switch k {
	case iconChevronDown:
		p.MoveTo(f32.Pt(s*0.25, s*0.38))
		p.LineTo(f32.Pt(s*0.5, s*0.63))
		p.LineTo(f32.Pt(s*0.75, s*0.38))
	case iconChevronUp:
		p.MoveTo(f32.Pt(s*0.25, s*0.62))
		p.LineTo(f32.Pt(s*0.5, s*0.37))
		p.LineTo(f32.Pt(s*0.75, s*0.62))
	case iconChevronRight:
		p.MoveTo(f32.Pt(s*0.38, s*0.25))
		p.LineTo(f32.Pt(s*0.63, s*0.5))
		p.LineTo(f32.Pt(s*0.38, s*0.75))
	case iconClose:
		p.MoveTo(f32.Pt(s*0.28, s*0.28))
		p.LineTo(f32.Pt(s*0.72, s*0.72))
		p.MoveTo(f32.Pt(s*0.72, s*0.28))
		p.LineTo(f32.Pt(s*0.28, s*0.72))
	}
	spec := p.End()
	paint.FillShape(gtx.Ops, c, clip.Stroke{Path: spec, Width: stroke}.Op())
	return D{Size: sz}
}

// iconButton is a small clickable icon.
func iconButton(gtx C, cl *widget.Clickable, k iconKind) D {
	return cl.Layout(gtx, func(gtx C) D {
		c := pal.TextDim
		if cl.Hovered() {
			c = pal.TextBright
		}
		return insetHV(6, 4).Layout(gtx, func(gtx C) D { return drawIcon(gtx, k, c, 14) })
	})
}

// labelIcon draws text followed by an icon, vertically centered.
func labelIcon(gtx C, th *material.Theme, size unit.Sp, c colorNRGBA, txt string, k iconKind) D {
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontSans, size, c, txt) }),
		layout.Rigid(layout.Spacer{Width: 4}.Layout),
		layout.Rigid(func(gtx C) D { return drawIcon(gtx, k, c, unit.Dp(size)) }),
	)
}

// iconLabel draws an icon followed by text.
func iconLabel(gtx C, th *material.Theme, size unit.Sp, c colorNRGBA, k iconKind, txt string) D {
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return drawIcon(gtx, k, c, unit.Dp(size)) }),
		layout.Rigid(layout.Spacer{Width: 3}.Layout),
		layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontSans, size, c, txt) }),
	)
}
