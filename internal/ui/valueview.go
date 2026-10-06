package ui

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"reflect"
	"strings"
	"unicode/utf8"
	"unsafe"

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
	edited, wasEditable                  bool
	truncated                            bool    // shown text is cut (never editable)
	model                                uintptr // identity of the grid model the cell belongs to
	cache                                renderCache
	modeBtn                              [3]widget.Clickable
	copyBtn, nullBtn, applyBtn, closeBtn widget.Clickable
}

func newValuePanel(a *App) *valuePanel {
	v := &valuePanel{app: a, r: -1, c: -1}
	v.list.Axis = layout.Vertical
	return v
}

func (v *valuePanel) focused(gtx C) bool { return v.open && gtx.Focused(&v.ed) }

// Limits of the text shown by the panel (huge values would freeze the
// editor): the rest is replaced by a note and the value becomes read-only.
const (
	maxPanelText = 1 << 20
	maxPanelHex  = 64 * 1024 // bytes dumped in hex mode
)

// renderValue formats a value for the panel.
func renderValue(val any, col db.Column, mode int) string {
	s, _ := renderValueCapped(val, col, mode)
	return s
}

// renderValueCapped formats a value for the panel, reporting whether the
// text was truncated.
func renderValueCapped(val any, col db.Column, mode int) (string, bool) {
	if val == nil {
		return "", false
	}
	switch mode {
	case 1:
		s := db.FormatValue(val, col.Kind)
		if len(s) <= maxPanelText {
			var out bytes.Buffer
			if json.Indent(&out, []byte(s), "", "  ") == nil {
				s = out.String()
			}
		}
		return capText(s)
	case 2:
		var b []byte
		switch x := val.(type) {
		case []byte:
			b = x
		default:
			b = []byte(db.FormatValue(val, col.Kind))
		}
		if len(b) > maxPanelHex {
			return hex.Dump(b[:maxPanelHex]) + truncNote(maxPanelHex, len(b)), true
		}
		return hex.Dump(b), false
	}
	if b, ok := val.([]byte); ok && (col.Kind == db.KindBytes || !utf8.Valid(b)) {
		if n := maxPanelText/2 - 1; len(b) > n {
			return "0x" + hex.EncodeToString(b[:n]) + truncNote(n, len(b)), true
		}
		return "0x" + hex.EncodeToString(b), false
	}
	return capText(db.FormatValue(val, col.Kind))
}

func capText(s string) (string, bool) {
	if len(s) <= maxPanelText {
		return s, false
	}
	n := maxPanelText
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + truncNote(n, len(s)), true
}

func truncNote(shown, total int) string {
	return "\n\n… truncated: showing " + db.HumanBytes(int64(shown)) + " of " + db.HumanBytes(int64(total)) + " (copy the cell to get the full value)"
}

// renderCache keeps the rendered text of the shown cell: rendering (JSON
// indent, hex dumps) is too slow for every frame on big values.
type renderCache struct {
	ok        bool
	r, c      int
	mode      int
	model     uintptr
	ptr       uintptr // data pointer and length of the string/[]byte value
	n         int
	text      string
	truncated bool
}

// valueRef identifies a string or []byte value without comparing contents
// (other values are cheap to render and are not cached).
func valueRef(val any) (uintptr, int, bool) {
	switch x := val.(type) {
	case string:
		return uintptr(unsafe.Pointer(unsafe.StringData(x))), len(x), true
	case []byte:
		return uintptr(unsafe.Pointer(unsafe.SliceData(x))), len(x), true
	}
	return 0, 0, false
}

func (v *valuePanel) render(r, c int, val any, col db.Column, model uintptr) (string, bool) {
	ptr, n, cacheable := valueRef(val)
	k := &v.cache
	if cacheable && k.ok && k.r == r && k.c == c && k.mode == v.mode && k.model == model && k.ptr == ptr && k.n == n {
		return k.text, k.truncated
	}
	text, trunc := renderValueCapped(val, col, v.mode)
	*k = renderCache{ok: cacheable, r: r, c: c, mode: v.mode, model: model, ptr: ptr, n: n, text: text, truncated: trunc}
	return text, trunc
}

// modelID identifies the grid's model: a reload, a new result or a sort
// done by replacing the model invalidates row/column indexes.
func modelID(g *DataGrid) uintptr {
	if g == nil || g.model == nil {
		return 0
	}
	rv := reflect.ValueOf(g.model)
	if rv.Kind() == reflect.Pointer {
		return rv.Pointer()
	}
	return 0
}

// layoutWith draws the grid with the panel on its right. get returns the
// value of a cell; edit (nil = read-only) applies a new value.
func (v *valuePanel) layoutWith(gtx C, g *DataGrid, get func(r, c int) (any, db.Column, bool), edit func(r, c int, text string, null bool)) D {
	if !v.open {
		// closed (button, toggle…): an abandoned edit must not be written
		// when the panel reopens
		v.edited, v.r, v.c = false, -1, -1
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
		v.edited = false
	}
	model := modelID(g)
	if v.edited && v.r >= 0 && v.dropEdit(model, g, get) {
		// the cell the edit belongs to is gone or now holds another record
		v.edited, v.r, v.c = false, -1, -1
	}
	// a pending edit is kept as a grid change when the panel moves away
	// from its cell (they are only buffered until Save)
	keepEdit := func() {
		if v.edited && v.wasEditable && edit != nil && v.r >= 0 {
			edit(v.r, v.c, v.ed.Text(), false)
			v.edited = false
		}
	}
	for i := range v.modeBtn {
		if v.modeBtn[i].Clicked(gtx) {
			keepEdit()
			v.mode = i
			v.r = -1 // re-render
		}
	}
	text, truncated := v.render(r, c, val, col, model)
	// load the cell into the editor when the selection moves (or the value
	// changed under an unedited editor)
	if r != v.r || c != v.c {
		keepEdit()
	}
	if r != v.r || c != v.c || !v.edited && text != v.shown {
		v.r, v.c, v.shown, v.isNull, v.edited = r, c, text, val == nil, false
		v.model, v.truncated = model, truncated
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
	editable := edit != nil && has && v.mode == 0 && !v.truncated && g.Editable != nil && g.Editable(r, c)
	v.ed.ReadOnly = !editable
	v.wasEditable = editable
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

// dropEdit reports whether the pending edit no longer belongs to the cell
// at (v.r, v.c): the model was replaced, shrank below the row, or the cell
// now renders differently from what the edit started from.
func (v *valuePanel) dropEdit(model uintptr, g *DataGrid, get func(r, c int) (any, db.Column, bool)) bool {
	if model != v.model {
		return true
	}
	if g != nil && g.model != nil && v.r >= g.model.Rows() {
		return true
	}
	val, col, has := get(v.r, v.c)
	if !has {
		return true
	}
	text, _ := v.render(v.r, v.c, val, col, model)
	return text != v.shown
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
