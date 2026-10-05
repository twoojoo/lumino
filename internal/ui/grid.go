package ui

import (
	"image"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/giovanni/lumino/internal/db"
)

type rowState uint8

const (
	rowNormal rowState = iota
	rowInserted
	rowModified
	rowDeleted
)

// gridModel is the data shown by a DataGrid.
type gridModel interface {
	Columns() []db.Column
	Rows() int
	Value(r, c int) any
	RowState(r int) rowState
	CellChanged(r, c int) bool
}

// cellRange is a rectangular selection (inclusive).
type cellRange struct{ r0, c0, r1, c1 int }

func (s cellRange) norm() cellRange {
	if s.r0 > s.r1 {
		s.r0, s.r1 = s.r1, s.r0
	}
	if s.c0 > s.c1 {
		s.c0, s.c1 = s.c1, s.c0
	}
	return s
}

func (s cellRange) has(r, c int) bool {
	n := s.norm()
	return r >= n.r0 && r <= n.r1 && c >= n.c0 && c <= n.c1
}

// DataGrid is a virtualized table with cell selection and editing.
type DataGrid struct {
	app   *App
	model gridModel

	colW      []int
	colsKey   string
	scrollX   int
	scrollY   int
	viewW     int
	viewH     int
	rowH      int
	headerH   int
	rowNumW   int
	sel       cellRange
	hasSel    bool
	dragging  bool
	dragMode  int // 1 cells, 2 rows
	resizeCol int
	resizeX   float32
	resizeW   int
	sbDrag    int // 1 vertical, 2 horizontal
	lastClick time.Time
	lastCell  [2]int
	tag       int
	focused   bool
	edgeHover bool

	SortCol  int // -1 none
	SortDesc bool

	editing  bool
	editR    int
	editC    int
	editor   widget.Editor
	editInit bool

	// owner hooks (all optional)
	OnSort        func(col int)
	Editable      func(r, c int) bool
	OnEdit        func(r, c int, text string)
	OnSetNull     func(r, c int)
	OnDeleteRows  func(rows []int)
	OnContextMenu func(at image.Point)
	OnNearEnd     func()
	OnActivate    func(r, c int) // double click on a read-only cell
	Empty         string         // text shown without rows
}

func newDataGrid(a *App) *DataGrid {
	g := &DataGrid{app: a, SortCol: -1, resizeCol: -1}
	g.editor.SingleLine, g.editor.Submit = true, true
	return g
}

// SetModel replaces the data; columns widths are kept when the columns
// didn't change.
func (g *DataGrid) SetModel(m gridModel) {
	g.model = m
	g.editing = false
	key := colsKeyOf(m)
	if key != g.colsKey {
		g.colsKey = key
		g.colW = nil
		g.scrollX, g.scrollY = 0, 0
		g.hasSel = false
		g.SortCol = -1
	}
}

func colsKeyOf(m gridModel) string {
	if m == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range m.Columns() {
		b.WriteString(c.Name)
		b.WriteByte(0)
	}
	return b.String()
}

// Selection returns the selected range (normalized) and whether there is one.
func (g *DataGrid) Selection() (cellRange, bool) { return g.sel.norm(), g.hasSel }

// Focused reports whether the grid (or its cell editor) has the keyboard.
func (g *DataGrid) Focused(gtx C) bool {
	return gtx.Focused(&g.tag) || g.editing && gtx.Focused(&g.editor)
}

// SelectCell moves the focus cell.
func (g *DataGrid) SelectCell(r, c int) {
	g.sel = cellRange{r, c, r, c}
	g.hasSel = true
	g.ensureVisible(r, c)
}

func (g *DataGrid) rows() int {
	if g.model == nil {
		return 0
	}
	return g.model.Rows()
}

func (g *DataGrid) cols() []db.Column {
	if g.model == nil {
		return nil
	}
	return g.model.Columns()
}

// autosize computes column widths from the header and sampled values.
func (g *DataGrid) autosize(gtx C) {
	cols := g.cols()
	if len(g.colW) == len(cols) {
		return
	}
	th := g.app.th
	g.colW = make([]int, len(cols))
	n := min(g.rows(), 300)
	for c, col := range cols {
		longest := col.Name
		lw := utf8.RuneCountInString(col.Name) + 3
		for r := 0; r < n; r++ {
			s := db.CellText(g.model.Value(r, c), col.Kind)
			if l := utf8.RuneCountInString(s); l > lw {
				lw, longest = l, s
			}
		}
		if lw > 60 {
			longest = string([]rune(longest)[:60])
		}
		w := measureText(gtx, th, fontSans, 12.5, longest) + gtx.Dp(26)
		g.colW[c] = max(gtx.Dp(60), min(w, gtx.Dp(420)))
	}
}

// measureText returns the width of s in px.
func measureText(gtx C, th *material.Theme, f font.Font, size unit.Sp, s string) int {
	m := op.Record(gtx.Ops)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(1<<20, 1<<20)}
	d := widget.Label{MaxLines: 1}.Layout(g, th.Shaper, f, size, s, op.CallOp{})
	m.Stop()
	return d.Size.X
}

func (g *DataGrid) totalW() int {
	w := g.rowNumW
	for _, c := range g.colW {
		w += c
	}
	return w
}

// cellAt maps a point to a cell (-1 for header / row numbers).
func (g *DataGrid) cellAt(p f32.Point) (int, int) {
	r := -1
	if int(p.Y) >= g.headerH {
		r = (int(p.Y) - g.headerH + g.scrollY) / g.rowH
	}
	c := -1
	if int(p.X) >= g.rowNumW {
		x := int(p.X) - g.rowNumW + g.scrollX
		for i, w := range g.colW {
			if x < w {
				c = i
				break
			}
			x -= w
		}
		if c < 0 {
			c = len(g.colW) - 1
		}
	}
	return r, c
}

// colEdge returns the column whose right edge is near x (resizing).
func (g *DataGrid) colEdge(x float32, slop int) int {
	cx := g.rowNumW - g.scrollX
	for i, w := range g.colW {
		cx += w
		if int(x) >= cx-slop && int(x) <= cx+slop {
			return i
		}
	}
	return -1
}

func (g *DataGrid) ensureVisible(r, c int) {
	if g.rowH == 0 {
		return
	}
	bodyH := g.viewH - g.headerH
	if y := r * g.rowH; y < g.scrollY {
		g.scrollY = y
	} else if y+g.rowH > g.scrollY+bodyH {
		g.scrollY = y + g.rowH - bodyH
	}
	if c >= 0 && c < len(g.colW) {
		x := 0
		for i := 0; i < c; i++ {
			x += g.colW[i]
		}
		bodyW := g.viewW - g.rowNumW
		if x < g.scrollX {
			g.scrollX = x
		} else if x+g.colW[c] > g.scrollX+bodyW {
			g.scrollX = x + g.colW[c] - bodyW
		}
	}
}

func (g *DataGrid) clampScroll() {
	bodyH := g.viewH - g.headerH
	g.scrollY = max(0, min(g.scrollY, g.rows()*g.rowH-bodyH+g.rowH))
	g.scrollX = max(0, min(g.scrollX, g.totalW()-g.viewW+gtx0(12)))
	g.scrollY = max(0, g.scrollY)
	g.scrollX = max(0, g.scrollX)
}

// gtx0 converts dp at the default density (scroll margins only).
func gtx0(dp int) int { return dp }

// copySelection copies the selection as TSV.
func (g *DataGrid) copySelection(gtx C, withHeader bool) {
	s, ok := g.Selection()
	if !ok {
		return
	}
	cols := g.cols()
	var b strings.Builder
	if withHeader {
		for c := s.c0; c <= s.c1; c++ {
			if c > s.c0 {
				b.WriteByte('\t')
			}
			b.WriteString(cols[c].Name)
		}
		b.WriteByte('\n')
	}
	for r := s.r0; r <= s.r1 && r < g.rows(); r++ {
		for c := s.c0; c <= s.c1; c++ {
			if c > s.c0 {
				b.WriteByte('\t')
			}
			if v := g.model.Value(r, c); v != nil {
				b.WriteString(tsvEscape(db.FormatValue(v, cols[c].Kind)))
			}
		}
		b.WriteByte('\n')
	}
	gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(strings.TrimSuffix(b.String(), "\n")))})
	n := (s.r1 - s.r0 + 1) * (s.c1 - s.c0 + 1)
	g.app.flash("Copied "+itoa(n)+" cell(s)", false)
}

func tsvEscape(s string) string {
	if strings.ContainsAny(s, "\t\n\r\"") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

func (g *DataGrid) beginEdit(gtx C, r, c int) bool {
	if g.Editable == nil || !g.Editable(r, c) || r >= g.rows() {
		return false
	}
	g.editing, g.editR, g.editC, g.editInit = true, r, c, true
	v := g.model.Value(r, c)
	txt := ""
	if v != nil {
		txt = db.FormatValue(v, g.cols()[c].Kind)
	}
	if strings.ContainsRune(txt, '\n') || len(txt) > 2000 {
		// multi-line values are edited in the value panel
		g.editing = false
		if g.OnActivate != nil {
			g.OnActivate(r, c)
		}
		return false
	}
	g.editor.SetText(txt)
	g.editor.SetCaret(len(txt), 0)
	return true
}

func (g *DataGrid) commitEdit(gtx C) {
	if !g.editing {
		return
	}
	g.editing = false
	if g.OnEdit != nil {
		g.OnEdit(g.editR, g.editC, g.editor.Text())
	}
	gtx.Execute(key.FocusCmd{Tag: &g.tag})
}

func (g *DataGrid) cancelEdit(gtx C) {
	g.editing = false
	gtx.Execute(key.FocusCmd{Tag: &g.tag})
}

func (g *DataGrid) onKey(gtx C, k key.Event) {
	if !g.hasSel && g.rows() > 0 && len(g.colW) > 0 {
		g.SelectCell(0, 0)
		return
	}
	shift := k.Modifiers.Contain(key.ModShift)
	short := k.Modifiers.Contain(key.ModShortcut)
	r, c := g.sel.r1, g.sel.c1
	page := max(1, (g.viewH-g.headerH)/max(1, g.rowH)-1)
	move := func(nr, nc int) {
		nr = max(0, min(g.rows()-1, nr))
		nc = max(0, min(len(g.colW)-1, nc))
		if shift {
			g.sel.r1, g.sel.c1 = nr, nc
		} else {
			g.sel = cellRange{nr, nc, nr, nc}
		}
		g.hasSel = true
		g.ensureVisible(nr, nc)
	}
	switch k.Name {
	case key.NameUpArrow:
		if short {
			move(0, c)
		} else {
			move(r-1, c)
		}
	case key.NameDownArrow:
		if short {
			move(g.rows()-1, c)
		} else {
			move(r+1, c)
		}
	case key.NameLeftArrow:
		if short {
			move(r, 0)
		} else {
			move(r, c-1)
		}
	case key.NameRightArrow:
		if short {
			move(r, len(g.colW)-1)
		} else {
			move(r, c+1)
		}
	case key.NamePageUp:
		move(r-page, c)
	case key.NamePageDown:
		move(r+page, c)
	case key.NameHome:
		if short {
			move(0, 0)
		} else {
			move(r, 0)
		}
	case key.NameEnd:
		if short {
			move(g.rows()-1, len(g.colW)-1)
		} else {
			move(r, len(g.colW)-1)
		}
	case key.NameTab:
		shift = false
		if k.Modifiers.Contain(key.ModShift) {
			move(r, c-1)
		} else {
			move(r, c+1)
		}
	case key.NameReturn, key.NameEnter, key.NameF2:
		if !g.beginEdit(gtx, r, c) && g.OnActivate != nil {
			g.OnActivate(r, c)
		}
	case key.NameDeleteForward:
		if g.OnDeleteRows != nil {
			s := g.sel.norm()
			var rows []int
			for i := s.r0; i <= s.r1; i++ {
				rows = append(rows, i)
			}
			g.OnDeleteRows(rows)
		}
	case "C":
		g.copySelection(gtx, shift)
	case "A":
		if g.rows() > 0 {
			g.sel = cellRange{0, 0, g.rows() - 1, len(g.colW) - 1}
			g.hasSel = true
		}
	case "N":
		if g.OnSetNull != nil {
			g.OnSetNull(r, c)
		}
	}
}

// Layout draws the grid filling the constraints.
func (g *DataGrid) Layout(gtx C) D {
	size := gtx.Constraints.Max
	gtx.Constraints = layout.Exact(size)
	th := g.app.th
	g.viewW, g.viewH = size.X, size.Y
	g.rowH = gtx.Dp(26)
	g.headerH = gtx.Dp(30)
	fill(gtx, pal.Bg)
	if g.model == nil {
		return D{Size: size}
	}
	g.autosize(gtx)
	cols := g.cols()
	nrows := g.rows()
	g.rowNumW = measureText(gtx, th, fontMono, 11, itoa(max(10, nrows))) + gtx.Dp(18)

	// input
	area := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, &g.tag)
	for {
		ev, ok := gtx.Event(
			pointer.Filter{Target: &g.tag, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel | pointer.Scroll | pointer.Move,
				ScrollY: pointer.ScrollRange{Min: -1e6, Max: 1e6}, ScrollX: pointer.ScrollRange{Min: -1e6, Max: 1e6}},
			key.FocusFilter{Target: &g.tag},
			key.Filter{Focus: &g.tag, Name: key.NameUpArrow, Optional: key.ModShift | key.ModShortcut},
			key.Filter{Focus: &g.tag, Name: key.NameDownArrow, Optional: key.ModShift | key.ModShortcut},
			key.Filter{Focus: &g.tag, Name: key.NameLeftArrow, Optional: key.ModShift | key.ModShortcut},
			key.Filter{Focus: &g.tag, Name: key.NameRightArrow, Optional: key.ModShift | key.ModShortcut},
			key.Filter{Focus: &g.tag, Name: key.NamePageUp, Optional: key.ModShift},
			key.Filter{Focus: &g.tag, Name: key.NamePageDown, Optional: key.ModShift},
			key.Filter{Focus: &g.tag, Name: key.NameHome, Optional: key.ModShift | key.ModShortcut},
			key.Filter{Focus: &g.tag, Name: key.NameEnd, Optional: key.ModShift | key.ModShortcut},
			key.Filter{Focus: &g.tag, Name: key.NameTab, Optional: key.ModShift},
			key.Filter{Focus: &g.tag, Name: key.NameReturn},
			key.Filter{Focus: &g.tag, Name: key.NameEnter},
			key.Filter{Focus: &g.tag, Name: key.NameF2},
			key.Filter{Focus: &g.tag, Name: key.NameDeleteForward},
			key.Filter{Focus: &g.tag, Name: "C", Required: key.ModShortcut, Optional: key.ModShift},
			key.Filter{Focus: &g.tag, Name: "A", Required: key.ModShortcut},
			key.Filter{Focus: &g.tag, Name: "N", Required: key.ModShortcut | key.ModShift},
		)
		if !ok {
			break
		}
		switch e := ev.(type) {
		case key.FocusEvent:
			g.focused = e.Focus
		case key.Event:
			if e.State == key.Press {
				g.onKey(gtx, e)
			}
		case pointer.Event:
			g.onPointer(gtx, e)
		}
	}
	if g.resizeCol >= 0 || g.edgeHover {
		pointer.CursorColResize.Add(gtx.Ops)
	}
	area.Pop()
	g.clampScroll()

	first := g.scrollY / g.rowH
	last := min(nrows-1, (g.scrollY+size.Y-g.headerH)/g.rowH)
	sel := g.sel.norm()
	bodyClip := clip.Rect{Min: image.Pt(g.rowNumW, g.headerH), Max: size}.Push(gtx.Ops)
	for r := first; r <= last; r++ {
		y := g.headerH + r*g.rowH - g.scrollY
		state := g.model.RowState(r)
		bgc := pal.Bg
		if r%2 == 1 {
			bgc = pal.RowAlt
		}
		fillRect(gtx, bgc, image.Rect(g.rowNumW, y, size.X, y+g.rowH))
		switch state {
		case rowInserted:
			fillRect(gtx, pal.Inserted, image.Rect(g.rowNumW, y, size.X, y+g.rowH))
		case rowDeleted:
			fillRect(gtx, pal.Deleted, image.Rect(g.rowNumW, y, size.X, y+g.rowH))
		}
		x := g.rowNumW - g.scrollX
		for c, w := range g.colW {
			if x+w < g.rowNumW {
				x += w
				continue
			}
			if x > size.X {
				break
			}
			cell := image.Rect(x, y, x+w, y+g.rowH)
			if g.model.CellChanged(r, c) {
				fillRect(gtx, pal.Modified, cell)
			}
			if g.hasSel && sel.has(r, c) {
				fillRect(gtx, pal.CellSel, cell)
			}
			if !(g.editing && g.editR == r && g.editC == c) {
				g.drawCell(gtx, th, cell, g.model.Value(r, c), cols[c].Kind, state == rowDeleted)
			}
			fillRect(gtx, withAlpha(pal.Border, 0x90), image.Rect(x+w-gtx.Dp(1), y, x+w, y+g.rowH))
			x += w
		}
		fillRect(gtx, withAlpha(pal.Border, 0x70), image.Rect(g.rowNumW, y+g.rowH-gtx.Dp(1), size.X, y+g.rowH))
		// focus cell outline
		if g.hasSel && g.focused && r == g.sel.r1 {
			fx := g.rowNumW - g.scrollX
			for i := 0; i < g.sel.c1 && i < len(g.colW); i++ {
				fx += g.colW[i]
			}
			if g.sel.c1 < len(g.colW) {
				strokeRect(gtx, pal.Accent, image.Rect(fx, y, fx+g.colW[g.sel.c1], y+g.rowH), gtx.Dp(1))
			}
		}
	}
	// inline editor
	if g.editing {
		g.layoutEditor(gtx, th)
	}
	bodyClip.Pop()

	// header
	fillRect(gtx, pal.Header, image.Rect(0, 0, size.X, g.headerH))
	hc := clip.Rect{Min: image.Pt(g.rowNumW, 0), Max: image.Pt(size.X, g.headerH)}.Push(gtx.Ops)
	x := g.rowNumW - g.scrollX
	for c, w := range g.colW {
		if x+w >= g.rowNumW && x <= size.X {
			g.drawHeader(gtx, th, image.Rect(x, 0, x+w, g.headerH), c, cols[c], g.hasSel && c >= sel.c0 && c <= sel.c1)
		}
		x += w
	}
	hc.Pop()
	fillRect(gtx, pal.Border, image.Rect(0, g.headerH-gtx.Dp(1), size.X, g.headerH))

	// row numbers
	fillRect(gtx, pal.Header, image.Rect(0, g.headerH, g.rowNumW, size.Y))
	rc := clip.Rect{Min: image.Pt(0, g.headerH), Max: image.Pt(g.rowNumW, size.Y)}.Push(gtx.Ops)
	for r := first; r <= last; r++ {
		y := g.headerH + r*g.rowH - g.scrollY
		c := withAlpha(pal.TextDim, 0xb0)
		if g.hasSel && r >= sel.r0 && r <= sel.r1 {
			fillRect(gtx, withAlpha(pal.Accent, 0x30), image.Rect(0, y, g.rowNumW, y+g.rowH))
			c = pal.TextBright
		}
		switch g.model.RowState(r) {
		case rowInserted:
			c = pal.Ok
		case rowDeleted:
			c = pal.Err
		case rowModified:
			c = pal.Info
		}
		st := op.Offset(image.Pt(0, y)).Push(gtx.Ops)
		gg := gtx
		gg.Constraints = layout.Exact(image.Pt(g.rowNumW-gtx.Dp(8), g.rowH))
		layout.E.Layout(gg, func(gtx C) D { return lbl(gtx, th, fontMono, 11, c, itoa(r+1)) })
		st.Pop()
	}
	rc.Pop()
	fillRect(gtx, pal.Border, image.Rect(g.rowNumW-gtx.Dp(1), 0, g.rowNumW, size.Y))

	if nrows == 0 {
		msg := g.Empty
		if msg == "" {
			msg = "No rows"
		}
		st := op.Offset(image.Pt(0, g.headerH)).Push(gtx.Ops)
		gg := gtx
		gg.Constraints = layout.Exact(image.Pt(size.X, gtx.Dp(60)))
		layout.Center.Layout(gg, func(gtx C) D { return lbl(gtx, th, fontSans, 12.5, pal.TextDim, msg) })
		st.Pop()
	}
	g.drawScrollbars(gtx, size)
	if g.OnNearEnd != nil && nrows > 0 && last >= nrows-80 {
		g.OnNearEnd()
	}
	return D{Size: size}
}

var fontItalic = font.Font{Typeface: "Go", Style: font.Italic}

func (g *DataGrid) drawCell(gtx C, th *material.Theme, r image.Rectangle, v any, k db.Kind, deleted bool) {
	pad := gtx.Dp(8)
	txt := db.CellText(v, k)
	c := pal.Text
	f := fontSans
	align := text.Start
	switch {
	case v == nil:
		c, f = pal.Null, fontItalic
	case k.Numeric():
		align = text.End
		c = pal.Number
	case k == db.KindBool:
		c = pal.Constant
	case k == db.KindBytes:
		c = pal.TextDim
	}
	if deleted {
		c = pal.TextDim
	}
	st := op.Offset(image.Pt(r.Min.X+pad, r.Min.Y)).Push(gtx.Ops)
	gg := gtx
	gg.Constraints = layout.Exact(image.Pt(max(0, r.Dx()-2*pad), r.Dy()))
	layout.W.Layout(gg, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return lblAlign(gtx, th, f, 12.5, c, txt, align)
	})
	st.Pop()
	if deleted {
		fillRect(gtx, withAlpha(pal.Err, 0x90), image.Rect(r.Min.X+pad, r.Min.Y+r.Dy()/2, r.Max.X-pad, r.Min.Y+r.Dy()/2+gtx.Dp(1)))
	}
}

func (g *DataGrid) drawHeader(gtx C, th *material.Theme, r image.Rectangle, c int, col db.Column, selected bool) {
	if selected {
		fillRect(gtx, withAlpha(pal.Accent, 0x22), r)
	}
	fillRect(gtx, pal.Border, image.Rect(r.Max.X-gtx.Dp(1), r.Min.Y+gtx.Dp(6), r.Max.X, r.Max.Y-gtx.Dp(6)))
	st := op.Offset(image.Pt(r.Min.X+gtx.Dp(8), r.Min.Y)).Push(gtx.Ops)
	gg := gtx
	gg.Constraints = layout.Exact(image.Pt(max(0, r.Dx()-gtx.Dp(14)), r.Dy()))
	layout.W.Layout(gg, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx C) D { return lbl(gtx, th, fontBold, 12, pal.TextBright, col.Name) }),
			layout.Rigid(func(gtx C) D {
				if g.SortCol != c {
					return D{}
				}
				ic := icUp
				if g.SortDesc {
					ic = icDown
				}
				return mdi(gtx, ic, pal.Accent, 13)
			}),
		)
	})
	st.Pop()
	g.app.hintRect(gtx, "grid/h/"+col.Name, r, col.Name+"\n"+strings.ToLower(col.DBType)+nullability(col))
}

func nullability(c db.Column) string {
	if !c.Nullable {
		return " · not null"
	}
	return ""
}

func strokeRect(gtx C, c colorNRGBA, r image.Rectangle, w int) {
	fillRect(gtx, c, image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+w))
	fillRect(gtx, c, image.Rect(r.Min.X, r.Max.Y-w, r.Max.X, r.Max.Y))
	fillRect(gtx, c, image.Rect(r.Min.X, r.Min.Y, r.Min.X+w, r.Max.Y))
	fillRect(gtx, c, image.Rect(r.Max.X-w, r.Min.Y, r.Max.X, r.Max.Y))
}

func (g *DataGrid) drawScrollbars(gtx C, size image.Point) {
	bodyH := size.Y - g.headerH
	total := g.rows()*g.rowH + g.rowH
	if total > bodyH {
		th := max(gtx.Dp(24), bodyH*bodyH/total)
		ty := g.headerH + int(float64(g.scrollY)/float64(max(1, total-bodyH))*float64(bodyH-th))
		fillRRect(gtx, withAlpha(pal.TextDim, 0x60), image.Rect(size.X-gtx.Dp(7), ty, size.X-gtx.Dp(2), ty+th), gtx.Dp(3))
	}
	bodyW := size.X - g.rowNumW
	tw := g.totalW() - g.rowNumW
	if tw > bodyW {
		thw := max(gtx.Dp(24), bodyW*bodyW/tw)
		tx := g.rowNumW + int(float64(g.scrollX)/float64(max(1, tw-bodyW+gtx.Dp(12)))*float64(bodyW-thw))
		fillRRect(gtx, withAlpha(pal.TextDim, 0x60), image.Rect(tx, size.Y-gtx.Dp(7), tx+thw, size.Y-gtx.Dp(2)), gtx.Dp(3))
	}
}

func (g *DataGrid) onPointer(gtx C, e pointer.Event) {
	switch e.Kind {
	case pointer.Move:
		g.edgeHover = int(e.Position.Y) < g.headerH && g.colEdge(e.Position.X, gtx.Dp(5)) >= 0
	case pointer.Scroll:
		dy, dx := e.Scroll.Y, e.Scroll.X
		if e.Modifiers.Contain(key.ModShift) && dx == 0 {
			dx, dy = dy, 0
		}
		g.scrollY += int(dy)
		g.scrollX += int(dx)
	case pointer.Press:
		gtx.Execute(key.FocusCmd{Tag: &g.tag})
		if g.editing {
			g.commitEdit(gtx)
		}
		// scrollbars
		if e.Position.X >= float32(g.viewW-gtx.Dp(10)) && int(e.Position.Y) > g.headerH {
			g.sbDrag = 1
			g.scrollTo(e.Position)
			return
		}
		if e.Position.Y >= float32(g.viewH-gtx.Dp(10)) && int(e.Position.X) > g.rowNumW {
			g.sbDrag = 2
			g.scrollTo(e.Position)
			return
		}
		r, c := g.cellAt(e.Position)
		if r < 0 { // header
			if col := g.colEdge(e.Position.X, gtx.Dp(5)); col >= 0 && e.Buttons == pointer.ButtonPrimary {
				if gtx.Now.Sub(g.lastClick) < 400*time.Millisecond && g.lastCell == [2]int{-1, col} {
					g.colW[col] = 0 // double click on the edge: autosize this column
					g.autosizeCol(gtx, col)
					return
				}
				g.lastClick, g.lastCell = gtx.Now, [2]int{-1, col}
				g.resizeCol, g.resizeX, g.resizeW = col, e.Position.X, g.colW[col]
				return
			}
			if c >= 0 && e.Buttons == pointer.ButtonPrimary {
				if e.Modifiers.Contain(key.ModShift) || g.OnSort == nil {
					// select the column
					if g.rows() > 0 {
						g.sel = cellRange{0, c, g.rows() - 1, c}
						g.hasSel = true
					}
				} else {
					g.OnSort(c)
				}
			}
			return
		}
		if r >= g.rows() {
			return
		}
		if e.Buttons == pointer.ButtonSecondary {
			if !g.hasSel || !g.sel.has(r, max(0, c)) {
				g.SelectCell(r, max(0, c))
			}
			if g.OnContextMenu != nil {
				g.OnContextMenu(g.app.pointer)
			}
			return
		}
		if c < 0 { // row number: whole rows
			if e.Modifiers.Contain(key.ModShift) && g.hasSel {
				g.sel.r1, g.sel.c0, g.sel.c1 = r, 0, len(g.colW)-1
			} else {
				g.sel = cellRange{r, 0, r, len(g.colW) - 1}
			}
			g.hasSel, g.dragging, g.dragMode = true, true, 2
			return
		}
		double := gtx.Now.Sub(g.lastClick) < 400*time.Millisecond && g.lastCell == [2]int{r, c}
		g.lastClick, g.lastCell = gtx.Now, [2]int{r, c}
		if double {
			if !g.beginEdit(gtx, r, c) && g.OnActivate != nil {
				g.OnActivate(r, c)
			}
			return
		}
		if e.Modifiers.Contain(key.ModShift) && g.hasSel {
			g.sel.r1, g.sel.c1 = r, c
		} else {
			g.sel = cellRange{r, c, r, c}
		}
		g.hasSel, g.dragging, g.dragMode = true, true, 1
	case pointer.Drag:
		if g.sbDrag != 0 {
			g.scrollTo(e.Position)
			return
		}
		if g.resizeCol >= 0 {
			g.colW[g.resizeCol] = max(gtx.Dp(30), g.resizeW+int(e.Position.X-g.resizeX))
			return
		}
		if g.dragging {
			r, c := g.cellAt(e.Position)
			r = max(0, min(g.rows()-1, r))
			if r < 0 {
				return
			}
			if g.dragMode == 2 {
				g.sel.r1 = r
			} else {
				g.sel.r1, g.sel.c1 = r, max(0, c)
			}
			if int(e.Position.Y) > g.viewH {
				g.scrollY += g.rowH
			} else if int(e.Position.Y) < g.headerH {
				g.scrollY -= g.rowH
			}
		}
	case pointer.Release, pointer.Cancel:
		g.dragging, g.resizeCol, g.sbDrag = false, -1, 0
	}
}

func (g *DataGrid) scrollTo(p f32.Point) {
	if g.sbDrag == 1 {
		bodyH := g.viewH - g.headerH
		total := g.rows()*g.rowH + g.rowH
		g.scrollY = int((p.Y-float32(g.headerH))/float32(max(1, bodyH))*float32(total)) - bodyH/2
	} else {
		bodyW := g.viewW - g.rowNumW
		g.scrollX = int((p.X-float32(g.rowNumW))/float32(max(1, bodyW))*float32(g.totalW())) - bodyW/2
	}
}

func (g *DataGrid) autosizeCol(gtx C, c int) {
	col := g.cols()[c]
	longest := col.Name
	n := min(g.rows(), 2000)
	for r := 0; r < n; r++ {
		if s := db.CellText(g.model.Value(r, c), col.Kind); len(s) > len(longest) {
			longest = s
		}
	}
	if utf8.RuneCountInString(longest) > 200 {
		longest = string([]rune(longest)[:200])
	}
	g.colW[c] = max(gtx.Dp(50), min(measureText(gtx, g.app.th, fontSans, 12.5, longest)+gtx.Dp(26), gtx.Dp(900)))
}

func (g *DataGrid) layoutEditor(gtx C, th *material.Theme) {
	x := g.rowNumW - g.scrollX
	for i := 0; i < g.editC && i < len(g.colW); i++ {
		x += g.colW[i]
	}
	y := g.headerH + g.editR*g.rowH - g.scrollY
	w := max(g.colW[g.editC], gtx.Dp(160))
	if g.editInit {
		g.editInit = false
		gtx.Execute(key.FocusCmd{Tag: &g.editor})
	}
	for {
		ev, ok := g.editor.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			g.commitEdit(gtx)
			g.onKey(gtx, key.Event{Name: key.NameDownArrow})
			return
		}
	}
	for {
		ev, ok := gtx.Event(key.Filter{Focus: &g.editor, Name: key.NameEscape}, key.Filter{Focus: &g.editor, Name: key.NameTab, Optional: key.ModShift})
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			if e.Name == key.NameEscape {
				g.cancelEdit(gtx)
				return
			}
			g.commitEdit(gtx)
			g.onKey(gtx, e)
			return
		}
	}
	st := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
	gg := gtx
	gg.Constraints = layout.Exact(image.Pt(w, g.rowH))
	rborder(gg, pal.Accent, pal.Panel, 2, func(gtx C) D {
		return insetHV(7, 4).Layout(gtx, func(gtx C) D {
			e := material.Editor(th, &g.editor, "")
			e.TextSize, e.Color = 12.5, pal.TextBright
			e.SelectionColor = withAlpha(pal.Accent, 0x50)
			return e.Layout(gtx)
		})
	})
	st.Pop()
}
