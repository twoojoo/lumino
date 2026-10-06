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

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/giovanni/lumino/internal/db"
)

// valuePanel shows (and edits) the selected cell in full.
type valuePanel struct {
	app  *App
	open bool
	mode int           // 0 text, 1 JSON, 2 hex
	ed   widget.Editor // text and hex (wraps long values)
	jed  *CodeEditor   // JSON (highlighted)
	// jsonChanged: the JSON editor changed since the edit state was checked
	jsonChanged bool
	jsonBad     bool // JSON mode shows a value that isn't valid JSON
	// manualCol: column whose mode the user picked (other columns get JSON
	// mode automatically when their type is JSON)
	manualCol int
	list      widget.List
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
	indentBtn, compactBtn                widget.Clickable
	// width of the drawer (px, 0 = default), resized from its inner edge
	width    int
	dragTag  int
	dragging bool
	grabX    float32
	grabW    int
}

func newValuePanel(a *App) *valuePanel {
	v := &valuePanel{app: a, r: -1, c: -1, manualCol: -1}
	v.list.Axis = layout.Vertical
	v.jed = newCodeEditor(a, "")
	v.jed.setJSON()
	v.jed.OnChange = func() { v.jsonChanged = true }
	return v
}

func (v *valuePanel) focused(gtx C) bool {
	return v.open && (gtx.Focused(&v.ed) || v.jed.Focused(gtx))
}

// editorText is the text of the editor of the current mode.
func (v *valuePanel) editorText() string {
	if v.mode == 1 {
		return v.jed.Text()
	}
	return v.ed.Text()
}

func (v *valuePanel) setEditorText(s string) {
	if v.mode == 1 {
		v.jed.SetText(s)
		v.jed.SetCaretOffset(0)
		v.jsonChanged = false
		return
	}
	v.ed.SetText(s)
}

// jsonEditable reports whether a value can be edited as JSON: a JSON or
// text column holding valid JSON.
func jsonEditable(col db.Column, shown string) bool {
	switch col.Kind {
	case db.KindJSON, db.KindText, db.KindOther:
	default:
		return false
	}
	return json.Valid([]byte(shown))
}

// jsonForSave validates edited JSON and formats it like the stored value:
// compact when the original had no line breaks (the panel indents it for
// reading only).
func jsonForSave(edited, original string) (string, error) {
	if !json.Valid([]byte(edited)) {
		var x any
		err := json.Unmarshal([]byte(edited), &x)
		return "", fmt.Errorf("not valid JSON: %v", err)
	}
	if !strings.Contains(original, "\n") {
		var b bytes.Buffer
		if err := json.Compact(&b, []byte(edited)); err == nil {
			return b.String(), nil
		}
	}
	return edited, nil
}

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
	if v.width == 0 {
		v.width = v.app.prefs.ValuePanelWidth
	}
	w := v.width
	if w == 0 {
		w = gtx.Dp(380)
	}
	maxW := max(gtx.Dp(260), gtx.Constraints.Max.X-gtx.Dp(220)) // the grid keeps some room
	w = max(gtx.Dp(260), min(w, maxW))
	dims := layout.Flex{}.Layout(gtx,
		layout.Flexed(1, g.Layout),
		layout.Rigid(func(gtx C) D { return vline(gtx, pal.Border) }),
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
			gtx.Constraints.Min.Y = gtx.Constraints.Max.Y
			fill(gtx, pal.Sidebar)
			return v.layoutPanel(gtx, g, get, edit)
		}),
	)
	v.layoutResize(gtx, dims.Size, w)
	return dims
}

// layoutResize lets the inner (left) edge of the drawer be dragged. The
// grip lives in the parent's coordinates, which don't move with the edge.
func (v *valuePanel) layoutResize(gtx C, size image.Point, w int) {
	x := size.X - w
	grip := image.Rect(x-gtx.Dp(3), 0, x+gtx.Dp(3), size.Y)
	st := clip.Rect(grip).Push(gtx.Ops)
	pointer.CursorColResize.Add(gtx.Ops)
	event.Op(gtx.Ops, &v.dragTag)
	st.Pop()
	if v.dragging {
		fillRect(gtx, withAlpha(pal.Accent, 0x90), image.Rect(x-gtx.Dp(1), 0, x+gtx.Dp(1), size.Y))
	}
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &v.dragTag, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel})
		if !ok {
			break
		}
		pe, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		switch pe.Kind {
		case pointer.Press:
			v.dragging, v.grabX, v.grabW = true, pe.Position.X, w
		case pointer.Drag:
			if v.dragging {
				v.width = v.grabW - int(pe.Position.X-v.grabX)
			}
		case pointer.Release, pointer.Cancel:
			if v.dragging {
				v.dragging = false
				if v.width == 0 {
					v.width = w
				}
				v.width = max(gtx.Dp(260), v.width) // the upper bound depends on the window
				v.app.prefs.ValuePanelWidth = v.width
				v.app.scheduleSave()
			}
		}
	}
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
	// the text to save (JSON mode: validated, compacted like the original)
	saveText := func(r, c int) (string, bool) {
		t := v.editorText()
		if v.mode != 1 {
			return t, true
		}
		val, col, _ := get(r, c)
		out, err := jsonForSave(t, db.FormatValue(val, col.Kind))
		if err != nil {
			v.app.flash("JSON edit not applied: "+err.Error(), true)
			return "", false
		}
		return out, true
	}
	keepEdit := func() {
		if v.edited && v.wasEditable && edit != nil && v.r >= 0 {
			if t, ok := saveText(v.r, v.c); ok {
				edit(v.r, v.c, t, false)
			}
			v.edited = false
		}
	}
	for i := range v.modeBtn {
		if v.modeBtn[i].Clicked(gtx) {
			keepEdit()
			v.mode, v.manualCol = i, c
			v.r = -1 // re-render
		}
	}
	// JSON columns open in JSON mode unless the user chose a mode there
	if has && c != v.c && c != v.manualCol && v.mode != 2 {
		want := 0
		if col.Kind == db.KindJSON {
			want = 1
		}
		if want != v.mode {
			keepEdit()
			v.mode, v.r = want, -1
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
		v.jsonBad = v.mode == 1 && val != nil && !truncated && !json.Valid([]byte(text))
		v.setEditorText(text)
	}
	for {
		ev, ok := v.ed.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.ChangeEvent); ok && v.mode != 1 {
			v.edited = v.ed.Text() != v.shown
		}
	}
	if v.jsonChanged && v.mode == 1 {
		v.jsonChanged = false
		v.edited = v.jed.Text() != v.shown
	}
	// Indent / Compact reformat the JSON editor (an edit when the cell is
	// editable, a view change otherwise)
	for _, b := range []struct {
		btn    *widget.Clickable
		indent bool
	}{{&v.indentBtn, true}, {&v.compactBtn, false}} {
		if !b.btn.Clicked(gtx) || v.mode != 1 {
			continue
		}
		src := []byte(v.jed.Text())
		var out bytes.Buffer
		var err error
		if b.indent {
			err = json.Indent(&out, src, "", "  ")
		} else {
			err = json.Compact(&out, src)
		}
		if err != nil {
			v.app.flash("Not valid JSON: "+err.Error(), true)
			continue
		}
		v.jed.SetText(out.String())
		v.jed.SetCaretOffset(0)
		// a read-only cell: "edited" only pins the reformatted view (it is
		// never written: wasEditable is false)
		v.edited = out.String() != v.shown
	}
	modeOK := v.mode == 0 || v.mode == 1 && (val == nil || jsonEditable(col, v.shown))
	editable := edit != nil && has && modeOK && !v.truncated && g.Editable != nil && g.Editable(r, c)
	v.ed.ReadOnly = !editable
	v.jed.ReadOnly = !editable
	v.wasEditable = editable
	if v.copyBtn.Clicked(gtx) && has {
		v.app.copyLater(v.editorText())
	}
	if v.nullBtn.Clicked(gtx) && editable {
		edit(r, c, "", true)
		v.r = -1
	}
	if v.applyBtn.Clicked(gtx) && editable && v.edited {
		if t, ok := saveText(r, c); ok {
			edit(r, c, t, false)
			v.edited = false
			v.r = -1
		}
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
			return layout.Inset{Left: 8, Right: 8, Bottom: 6}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						return segmented(gtx, th, v.modeBtn[:], []string{"Text", "JSON", "Hex"}, v.mode)
					}),
					layout.Flexed(1, func(gtx C) D { return D{Size: image.Pt(gtx.Constraints.Min.X, 1)} }),
					layout.Rigid(func(gtx C) D {
						if v.mode != 1 || !has || val == nil && !v.edited {
							return D{}
						}
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &v.indentBtn, nil, "Indent", pal.Text, pal.Hover) }),
							layout.Rigid(func(gtx C) D { return D{Size: image.Pt(gtx.Dp(4), 1)} }),
							layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &v.compactBtn, nil, "Compact", pal.Text, pal.Hover) }),
						)
					}),
				)
			})
		}),
		layout.Rigid(func(gtx C) D { return hline(gtx, pal.Border) }),
		layout.Rigid(func(gtx C) D {
			if !v.jsonBad {
				return D{}
			}
			return layout.Inset{Left: 10, Top: 6, Right: 8}.Layout(gtx, func(gtx C) D {
				return lbl(gtx, th, fontSans, 11, pal.Warn, "Not valid JSON: shown as stored")
			})
		}),
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
	if v.mode == 1 {
		return rborder(gtx, pal.Border, pal.CodeBg, 6, func(gtx C) D {
			gtx.Constraints.Min = gtx.Constraints.Max
			return v.jed.Layout(gtx)
		})
	}
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
