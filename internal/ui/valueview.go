package ui

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"strings"
	"unicode/utf8"

	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/giovanni/lumino/internal/db"
)

// valuePanel shows (and edits) the selected cell in full.
type valuePanel struct {
	app  *App
	open bool
	mode int // 0 text, 1 JSON, 2 hex
	ed   widget.Editor
	list widget.List
	// the cell shown in the editor
	r, c                                 int
	shown                                string
	isNull                               bool
	edited                               bool
	modeBtn                              [3]widget.Clickable
	copyBtn, nullBtn, applyBtn, closeBtn widget.Clickable
}

func newValuePanel(a *App) *valuePanel {
	v := &valuePanel{app: a, r: -1, c: -1}
	v.list.Axis = layout.Vertical
	return v
}

func (v *valuePanel) focused(gtx C) bool { return v.open && gtx.Focused(&v.ed) }

// renderValue formats a value for the panel.
func renderValue(val any, col db.Column, mode int) string {
	if val == nil {
		return ""
	}
	switch mode {
	case 1:
		s := db.FormatValue(val, col.Kind)
		var out bytes.Buffer
		if json.Indent(&out, []byte(s), "", "  ") == nil {
			return out.String()
		}
		return s
	case 2:
		var b []byte
		switch x := val.(type) {
		case []byte:
			b = x
		default:
			b = []byte(db.FormatValue(val, col.Kind))
		}
		if len(b) > 64*1024 {
			b = b[:64*1024]
		}
		return hex.Dump(b)
	}
	if b, ok := val.([]byte); ok && (col.Kind == db.KindBytes || !utf8.Valid(b)) {
		return "0x" + hex.EncodeToString(b)
	}
	return db.FormatValue(val, col.Kind)
}

// layoutWith draws the grid with the panel on its right. get returns the
// value of a cell; edit (nil = read-only) applies a new value.
func (v *valuePanel) layoutWith(gtx C, g *DataGrid, get func(r, c int) (any, db.Column, bool), edit func(r, c int, text string, null bool)) D {
	if !v.open {
		return g.Layout(gtx)
	}
	w := min(gtx.Dp(380), gtx.Constraints.Max.X/2)
	return layout.Flex{}.Layout(gtx,
		layout.Flexed(1, g.Layout),
		layout.Rigid(func(gtx C) D { return vline(gtx, pal.Border) }),
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
			gtx.Constraints.Min.Y = gtx.Constraints.Max.Y
			fill(gtx, pal.Sidebar)
			return v.layoutPanel(gtx, g, get, edit)
		}),
	)
}

func (v *valuePanel) layoutPanel(gtx C, g *DataGrid, get func(r, c int) (any, db.Column, bool), edit func(r, c int, text string, null bool)) D {
	th := v.app.th
	sel, ok := g.Selection()
	r, c := sel.r1, sel.c1
	if !ok {
		r, c = -1, -1
	}
	val, col, has := get(r, c)
	if v.closeBtn.Clicked(gtx) {
		v.open = false
	}
	for i := range v.modeBtn {
		if v.modeBtn[i].Clicked(gtx) {
			v.mode = i
			v.r = -1 // re-render
		}
	}
	text := renderValue(val, col, v.mode)
	// load the cell into the editor when the selection moves (or the value
	// changed under an unedited editor)
	if r != v.r || c != v.c || !v.edited && text != v.shown {
		v.r, v.c, v.shown, v.isNull, v.edited = r, c, text, val == nil, false
		v.ed.SetText(text)
	}
	for {
		ev, ok := v.ed.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.ChangeEvent); ok {
			v.edited = v.ed.Text() != v.shown
		}
	}
	editable := edit != nil && has && v.mode == 0 && g.Editable != nil && g.Editable(r, c)
	v.ed.ReadOnly = !editable
	if v.copyBtn.Clicked(gtx) && has {
		v.app.copyLater(v.ed.Text())
	}
	if v.nullBtn.Clicked(gtx) && editable {
		edit(r, c, "", true)
		v.r = -1
	}
	if v.applyBtn.Clicked(gtx) && editable && v.edited {
		edit(r, c, v.ed.Text(), false)
		v.edited = false
		v.r = -1
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: 10, Right: 6, Top: 6, Bottom: 6}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx C) D {
						if !has {
							return lbl(gtx, th, fontSans, 12, pal.TextDim, "No cell selected")
						}
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontBold, 12.5, pal.TextBright, col.Name) }),
							layout.Rigid(func(gtx C) D {
								info := strings.ToLower(col.DBType)
								if val == nil {
									info += " · NULL"
								} else if s, ok := val.(string); ok {
									info += fmt.Sprintf(" · %d chars", utf8.RuneCountInString(s))
								} else if b, ok := val.([]byte); ok {
									info += " · " + db.HumanBytes(int64(len(b)))
								}
								return lbl(gtx, th, fontSans, 11, pal.TextDim, info)
							}),
						)
					}),
					layout.Rigid(func(gtx C) D { return iconOnly(gtx, &v.copyBtn, icCopyText, 15) }),
					layout.Rigid(func(gtx C) D { return iconOnly(gtx, &v.closeBtn, icClose, 15) }),
				)
			})
		}),
		layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: 8, Bottom: 6}.Layout(gtx, func(gtx C) D {
				return segmented(gtx, th, v.modeBtn[:], []string{"Text", "JSON", "Hex"}, v.mode)
			})
		}),
		layout.Rigid(func(gtx C) D { return hline(gtx, pal.Border) }),
		layout.Flexed(1, func(gtx C) D {
			if has && val == nil && !v.edited {
				return inset(12).Layout(gtx, func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontItalic, 13, pal.Null, "NULL") }),
						layout.Rigid(func(gtx C) D {
							if !editable {
								return D{}
							}
							return layout.Inset{Top: 8}.Layout(gtx, func(gtx C) D {
								gtx.Constraints.Min.Y = gtx.Dp(120)
								return v.editorBox(gtx, th, "type a value to replace NULL")
							})
						}),
					)
				})
			}
			return inset(8).Layout(gtx, func(gtx C) D { return v.editorBox(gtx, th, "") })
		}),
		layout.Rigid(func(gtx C) D {
			if !editable {
				return D{}
			}
			return layout.Inset{Left: 8, Right: 8, Bottom: 8}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &v.nullBtn, nil, "Set NULL", pal.Text, pal.Hover) }),
					layout.Flexed(1, func(gtx C) D { return D{Size: image.Pt(gtx.Constraints.Min.X, 1)} }),
					layout.Rigid(func(gtx C) D {
						col := pal.Border
						if v.edited {
							col = pal.Accent
						}
						return button(gtx, th, &v.applyBtn, "Apply", col)
					}),
				)
			})
		}),
	)
}

func (v *valuePanel) editorBox(gtx C, th *material.Theme, hint string) D {
	return rborder(gtx, pal.Border, pal.CodeBg, 6, func(gtx C) D {
		gtx.Constraints.Min = gtx.Constraints.Max
		return inset(8).Layout(gtx, func(gtx C) D {
			return material.List(th, &v.list).Layout(gtx, 1, func(gtx C, _ int) D {
				e := material.Editor(th, &v.ed, hint)
				e.Font, e.TextSize, e.Color, e.HintColor = fontMono, 12, pal.TextBright, pal.TextDim
				e.SelectionColor = withAlpha(pal.Accent, 0x50)
				return e.Layout(gtx)
			})
		})
	})
}

// ---- copy menu (shared by result grids) ----

// copyMenuItems builds "Copy as …" items for the grid selection.
func copyMenuItems(a *App, g *DataGrid, cols []db.Column, value func(r, c int) any, d db.Dialect, ref db.ObjectRef) []*menuItem {
	sel, ok := g.Selection()
	if !ok {
		return nil
	}
	collect := func() ([]db.Column, [][]any) {
		var cs []db.Column
		for c := sel.c0; c <= sel.c1 && c < len(cols); c++ {
			cs = append(cs, cols[c])
		}
		var rows [][]any
		for r := sel.r0; r <= sel.r1; r++ {
			var row []any
			for c := sel.c0; c <= sel.c1 && c < len(cols); c++ {
				row = append(row, value(r, c))
			}
			rows = append(rows, row)
		}
		return cs, rows
	}
	format := func(f string) func() {
		return func() {
			cs, rows := collect()
			var b bytes.Buffer
			if err := writeRows(&b, f, cs, rows, d, ref, true); err != nil {
				a.flash("Copy: "+err.Error(), true)
				return
			}
			a.copyLater(strings.TrimSuffix(b.String(), "\n"))
		}
	}
	return []*menuItem{
		{icon: icCopyText, label: "Copy", hint: shortcutLabel("C"), action: format("tsv-noheader")},
		{icon: icCopyText, label: "Copy with headers", hint: shortcutLabel("Shift C"), action: format("tsv")},
		{icon: icCopyText, label: "Copy as CSV", action: format("csv")},
		{icon: icCopyText, label: "Copy as JSON", action: format("json")},
		{icon: icCopyText, label: "Copy as SQL INSERT", action: format("sql")},
		{icon: icCopyText, label: "Copy as Markdown", action: format("markdown")},
	}
}
