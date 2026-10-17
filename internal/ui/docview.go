package ui

import (
	"fmt"
	"image"
	"strings"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/twoojoo/lumino/internal/db"
)

// Result views of document databases: the grid shows the documents
// flattened into columns ("address.city"); the tree and the JSON text show
// them whole.
const (
	viewTable = iota
	viewTree
	viewJSON
)

var viewNames = [3]string{"Table", "Tree", "JSON"}

// docLine is a visible line of the tree.
type docLine struct {
	key   string // row/path
	row   int    // displayed row
	path  string // dotted field path ("" = the document)
	depth int
	node  db.DocNode
}

// docView draws the documents of a result as a tree or as text.
type docView struct {
	a         *App
	collapsed map[string]bool // documents (open by default)
	expanded  map[string]bool // nested fields (closed by default)
	sel       string
	list      widget.List
	clicks    map[string]*widget.Clickable
	tags      map[string]*int
	moreBtn   widget.Clickable
	lang      db.Language // of the last Layout

	json      *CodeEditor
	jsonOf    *resultSet
	jsonN     int
	jsonLines []int // first line of each displayed document

	// Menu adds the owner's actions for a line (row, field path, node).
	Menu func(row int, path string, n db.DocNode) []*menuItem
	// OnActivate: double-click on a document.
	OnActivate func(row int)
}

func newDocView(a *App) *docView {
	v := &docView{a: a, collapsed: map[string]bool{}, expanded: map[string]bool{}, clicks: map[string]*widget.Clickable{}, tags: map[string]*int{}}
	v.list.Axis = layout.Vertical
	v.json = newCodeEditor(a, "")
	v.json.ReadOnly = true
	return v
}

// reset forgets the state of the previous result.
func (v *docView) reset() {
	v.collapsed, v.expanded, v.sel = map[string]bool{}, map[string]bool{}, ""
	v.clicks, v.tags = map[string]*widget.Clickable{}, map[string]*int{}
	v.jsonOf = nil
	v.list.Position = layout.Position{}
}

func (v *docView) open(key string, depth int) bool {
	if depth == 0 {
		return !v.collapsed[key]
	}
	return v.expanded[key]
}

func (v *docView) toggle(l docLine) {
	if l.depth == 0 {
		v.collapsed[l.key] = !v.collapsed[l.key]
	} else {
		v.expanded[l.key] = !v.expanded[l.key]
	}
}

// lines flattens the visible tree.
func (v *docView) lines(rs *resultSet) []docLine {
	var out []docLine
	var walk func(row int, key, path string, n db.DocNode, depth int)
	walk = func(row int, key, path string, n db.DocNode, depth int) {
		out = append(out, docLine{key: key, row: row, path: path, depth: depth, node: n})
		if len(n.Children) == 0 || !v.open(key, depth) {
			return
		}
		for _, c := range n.Children {
			p := c.Name
			if path != "" {
				p = path + "." + c.Name
			}
			walk(row, key+"\x00"+c.Name, p, c, depth+1)
		}
	}
	for i := 0; i < rs.Rows(); i++ {
		if d, ok := rs.doc(i); ok {
			walk(i, itoa(rs.row(i)), "", d, 0)
		}
	}
	return out
}

// selected returns the displayed row and field path of the selection.
func (v *docView) selected(rs *resultSet, mode int) (row int, path string, ok bool) {
	if mode == viewJSON {
		if rs != v.jsonOf || len(v.jsonLines) == 0 {
			return 0, "", false
		}
		line := v.json.caret.line
		row = 0
		for i, l := range v.jsonLines {
			if l <= line {
				row = i
			}
		}
		return row, "", row < rs.Rows()
	}
	for _, l := range v.lines(rs) {
		if l.key == v.sel {
			return l.row, l.path, true
		}
	}
	return 0, "", false
}

func (v *docView) click(k string) *widget.Clickable {
	c := v.clicks[k]
	if c == nil {
		c = new(widget.Clickable)
		v.clicks[k] = c
	}
	return c
}

func (v *docView) tag(k string) *int {
	t := v.tags[k]
	if t == nil {
		t = new(int)
		v.tags[k] = t
	}
	return t
}

// Layout draws the result in the mode (tree or JSON).
func (v *docView) Layout(gtx C, rs *resultSet, lang db.Language, mode int) D {
	v.lang = lang
	if v.moreBtn.Clicked(gtx) && rs.more {
		rs.fetchMore(v.a, v.a.prefs.Fetch(), false, nil)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Flexed(1, func(gtx C) D {
			if mode == viewJSON {
				return v.layoutJSON(gtx, rs, lang)
			}
			return v.layoutTree(gtx, rs)
		}),
		layout.Rigid(func(gtx C) D {
			if !rs.more {
				return D{}
			}
			return layout.Inset{Top: 4, Bottom: 4, Left: 10}.Layout(gtx, func(gtx C) D {
				label := "Load more documents"
				if rs.fetching {
					label = "Loading…"
				}
				return pillButton(gtx, v.a.th, &v.moreBtn, icDownload, label, pal.Text, pal.Hover)
			})
		}),
	)
}

func (v *docView) layoutJSON(gtx C, rs *resultSet, lang db.Language) D {
	if rs != v.jsonOf || len(rs.docs) != v.jsonN {
		dl, _ := lang.(db.DocumentLanguage)
		var b strings.Builder
		v.jsonLines = v.jsonLines[:0]
		line := 0
		for i := 0; i < rs.Rows(); i++ {
			d, ok := rs.doc(i)
			if !ok {
				continue
			}
			text := summary(d)
			if dl != nil {
				text = dl.FormatDocument(d)
			}
			if i > 0 {
				b.WriteString("\n\n")
				line += 2
			}
			v.jsonLines = append(v.jsonLines, line)
			b.WriteString(text)
			line += strings.Count(text, "\n")
		}
		v.json.setLanguage(lang)
		v.json.SetText(b.String())
		v.json.SetCaretOffset(0)
		v.jsonOf, v.jsonN = rs, len(rs.docs)
	}
	return v.json.Layout(gtx)
}

func (v *docView) layoutTree(gtx C, rs *resultSet) D {
	a, th := v.a, v.a.th
	lines := v.lines(rs)
	if len(lines) == 0 {
		return layout.Center.Layout(gtx, func(gtx C) D { return lbl(gtx, th, fontSans, 12.5, pal.TextDim, "No documents") })
	}
	rh := gtx.Dp(24)
	return material.List(th, &v.list).Layout(gtx, len(lines), func(gtx C, i int) D {
		l := lines[i]
		cl := v.click(l.key)
		for {
			c, ok := cl.Update(gtx)
			if !ok {
				break
			}
			v.sel = l.key
			if c.NumClicks >= 2 && l.depth == 0 && v.OnActivate != nil {
				v.OnActivate(l.row)
			} else if len(l.node.Children) > 0 {
				v.toggle(l)
			}
		}
		tg := v.tag(l.key)
		for {
			ev, ok := gtx.Event(pointer.Filter{Target: tg, Kinds: pointer.Press})
			if !ok {
				break
			}
			if pe, ok := ev.(pointer.Event); ok && pe.Buttons == pointer.ButtonSecondary {
				v.sel = l.key
				v.menu(rs, l)
			}
		}
		return cl.Layout(gtx, func(gtx C) D {
			sz := image.Pt(gtx.Constraints.Max.X, rh)
			switch {
			case v.sel == l.key:
				fillRect(gtx, pal.Selected, image.Rect(0, 0, sz.X, rh))
			case cl.Hovered():
				fillRect(gtx, pal.Hover, image.Rect(0, 0, sz.X, rh))
			}
			area := clip.Rect{Max: sz}.Push(gtx.Ops)
			event.Op(gtx.Ops, tg)
			area.Pop()
			gtx.Constraints = layout.Exact(sz)
			return layout.Inset{Left: unit.Dp(8 + 16*l.depth), Right: 10}.Layout(gtx, func(gtx C) D {
				return layout.W.Layout(gtx, func(gtx C) D { return v.layoutLine(gtx, th, l, a) })
			})
		})
	})
}

// valueColor colors a leaf by type.
func valueColor(n db.DocNode) colorNRGBA {
	switch n.Type {
	case "string", "symbol":
		return pal.String
	case "int", "long", "double", "decimal":
		return pal.Number
	case "objectId", "date", "binData", "timestamp", "regex":
		return pal.Func
	}
	return pal.Constant
}

// summary is the text of a collapsed document or array.
func summary(n db.DocNode) string {
	switch {
	case n.IsArray():
		return fmt.Sprintf("[ %d item(s) ]", len(n.Children))
	case n.IsObject():
		if id, ok := n.Field("_id"); ok && id.Lit != "" {
			return fmt.Sprintf("{ _id: %s … %d field(s) }", id.Lit, len(n.Children))
		}
		return fmt.Sprintf("{ %d field(s) }", len(n.Children))
	}
	return n.Lit
}

func (v *docView) layoutLine(gtx C, th *material.Theme, l docLine, a *App) D {
	n := l.node
	ch := []layout.FlexChild{layout.Rigid(func(gtx C) D {
		if len(n.Children) == 0 {
			return D{Size: image.Pt(gtx.Dp(16), gtx.Dp(14))}
		}
		ic := icCollapse
		if v.open(l.key, l.depth) {
			ic = icExpand
		}
		return layout.Inset{Right: 2}.Layout(gtx, func(gtx C) D { return mdi(gtx, ic, pal.TextDim, 14) })
	})}
	if l.depth == 0 {
		ch = append(ch, layout.Rigid(func(gtx C) D {
			return lbl(gtx, th, fontSans, 12, pal.TextDim, fmt.Sprintf("%d  ", l.row+1))
		}))
	} else {
		ch = append(ch, layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontMono, 12.5, pal.TextBright, n.Name) }),
			layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontMono, 12.5, pal.TextDim, ": ") }))
	}
	ch = append(ch, layout.Flexed(1, func(gtx C) D {
		if len(n.Children) > 0 || n.IsObject() || n.IsArray() {
			return lbl(gtx, th, fontMono, 12.5, pal.TextDim, summary(n))
		}
		s := n.Lit
		if len(s) > 300 {
			s = s[:300] + "…"
		}
		return lbl(gtx, th, fontMono, 12.5, valueColor(n), strings.ReplaceAll(s, "\n", " "))
	}), layout.Rigid(func(gtx C) D {
		if l.depth == 0 {
			return D{}
		}
		return lbl(gtx, th, fontSans, 11, withAlpha(pal.TextDim, 0xb0), n.Type)
	}))
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx, ch...)
}

// menu opens the context menu of a line: copies, then the owner's actions.
func (v *docView) menu(rs *resultSet, l docLine) {
	a := v.a
	items := []*menuItem{}
	if l.node.Lit != "" {
		lit := l.node.Lit
		items = append(items, &menuItem{icon: icCopyText, label: "Copy value", action: func() { a.copyLater(lit) }})
	}
	if l.path != "" {
		p := l.path
		items = append(items, &menuItem{icon: icCopyText, label: "Copy field path", action: func() { a.copyLater(p) }})
	}
	if d, ok := rs.doc(l.row); ok {
		if dl, ok := v.lang.(db.DocumentLanguage); ok {
			text := dl.FormatDocument(d)
			items = append(items, &menuItem{icon: icCopyText, label: "Copy document", action: func() { a.copyLater(text) }})
		}
	}
	if v.Menu != nil {
		if extra := v.Menu(l.row, l.path, l.node); len(extra) > 0 {
			items = append(items, &menuItem{sep: true})
			items = append(items, extra...)
		}
	}
	a.overlay = &ContextMenu{app: a, at: a.pointer, items: items, sel: -1}
}

// viewSwitch draws the Table / Tree / JSON toggle.
func viewSwitch(gtx C, th *material.Theme, btns *[3]widget.Clickable, mode *int) D {
	for i := range btns {
		if btns[i].Clicked(gtx) {
			*mode = i
		}
	}
	var ch []layout.FlexChild
	for i := range btns {
		i := i
		ch = append(ch, layout.Rigid(func(gtx C) D {
			fg, b := pal.TextDim, colorNRGBA{}
			if *mode == i {
				fg, b = pal.TextBright, pal.Selected
			}
			return pillButton(gtx, th, &btns[i], nil, viewNames[i], fg, b)
		}))
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx, ch...)
}
