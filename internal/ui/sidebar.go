package ui

import (
	"fmt"
	"image"
	"sort"
	"strings"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/giovanni/lumino/internal/db"
)

type nodeKind int

const (
	nConn nodeKind = iota
	nCatalog
	nSchema
	nSysFolder // "System schemas"
	nFolder    // objects of one type
	nObject
	nColumn
	nInfo // loading / error / empty
)

// treeRow is one flattened line of the navigation tree.
type treeRow struct {
	key        string
	kind       nodeKind
	depth      int
	label      string
	extra      string
	icon       *widget.Icon
	iconColor  colorNRGBA
	connID     string
	ref        db.ObjectRef
	folder     db.ObjectType
	expandable bool
	isErr      bool
	isKey      bool
}

// Sidebar is the connection / object tree.
type Sidebar struct {
	app      *App
	list     widget.List
	clicks   map[string]*widget.Clickable
	tags     map[string]*int
	expanded map[string]bool
	sel      string
	filter   widget.Editor
	addBtn   widget.Clickable
	refBtn   widget.Clickable
	colBtn   widget.Clickable
	focusTag int
	rows     []treeRow
	width    int // px, resizable
	dragTag  int
	dragging bool
	grabX    float32
	scrollTo string
}

func newSidebar(a *App) *Sidebar {
	s := &Sidebar{app: a, clicks: map[string]*widget.Clickable{}, tags: map[string]*int{}, expanded: map[string]bool{}}
	s.list.Axis = layout.Vertical
	s.filter.SingleLine = true
	for _, k := range a.prefs.Expanded {
		s.expanded[k] = true
	}
	s.width = a.prefs.SidebarWidth
	return s
}

func connKey(id string) string              { return "c|" + id }
func catKey(id, cat string) string          { return connKey(id) + "|k|" + cat }
func schemaKey(id string, k metaKey) string { return catKey(id, k.catalog) + "|s|" + k.schema }
func sysKey(id, cat string) string          { return catKey(id, cat) + "|sys" }
func folderKey(id string, k metaKey, t db.ObjectType) string {
	return schemaKey(id, k) + "|f|" + string(t)
}
func objKey(id string, r db.ObjectRef) string {
	return folderKey(id, keyOf(r), r.Type) + "|o|" + r.Name + r.Signature
}

func (s *Sidebar) expand(key string, on bool) {
	if on {
		s.expanded[key] = true
	} else {
		delete(s.expanded, key)
	}
	s.app.scheduleSave()
}

// expandedKeys lists open nodes for the workspace (connections and
// schemas only: objects reload lazily anyway).
func (s *Sidebar) expandedKeys() []string {
	var out []string
	for k := range s.expanded {
		if !strings.Contains(k, "|o|") {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func (s *Sidebar) focused(gtx C) bool { return gtx.Focused(&s.filter) }

func (s *Sidebar) selectedRow() (treeRow, bool) {
	for _, r := range s.rows {
		if r.key == s.sel {
			return r, true
		}
	}
	return treeRow{}, false
}

func (s *Sidebar) selectedConn() string {
	if r, ok := s.selectedRow(); ok {
		return r.connID
	}
	if len(s.app.prefs.Connections) == 1 {
		return s.app.prefs.Connections[0].ID
	}
	return ""
}

func (s *Sidebar) selectedSchema() string {
	if r, ok := s.selectedRow(); ok {
		return r.ref.Schema
	}
	return ""
}

var folderOrder = []db.ObjectType{db.ObjTable, db.ObjView, db.ObjMatView, db.ObjFunction, db.ObjProcedure, db.ObjPackage, db.ObjSequence}

// build flattens the tree, triggering lazy loads of expanded nodes.
func (s *Sidebar) build() []treeRow {
	a := s.app
	var rr []treeRow
	q := strings.ToLower(strings.TrimSpace(s.filter.Text()))
	for _, cfg := range a.prefs.Connections {
		id := cfg.ID
		cs := a.connState(id)
		ck := connKey(id)
		extra := cfg.Host
		if cfg.File != "" {
			extra = shortPath(cfg.File)
		}
		rr = append(rr, treeRow{key: ck, kind: nConn, label: cfg.Name, extra: extra, connID: id, expandable: true,
			icon: icDatabase, iconColor: a.connColor(id)})
		if !s.expanded[ck] {
			continue
		}
		switch cs.status {
		case stIdle:
			a.connect(id, nil)
			fallthrough
		case stConnecting:
			rr = append(rr, treeRow{key: ck + "|loading", kind: nInfo, depth: 1, label: "Connecting…", connID: id})
			continue
		case stFailed:
			rr = append(rr, treeRow{key: ck + "|err", kind: nInfo, depth: 1, label: errLine(cs.err), isErr: true, connID: id})
			continue
		}
		caps := cs.caps()
		switch {
		case caps.Catalogs:
			if cs.catalogs == nil {
				rr = s.pending(rr, cs, "catalogs", ck, 1, cs.loadCatalogs)
				continue
			}
			for _, cat := range cs.catalogs {
				kk := catKey(id, cat)
				rr = append(rr, treeRow{key: kk, kind: nCatalog, depth: 1, label: cat, connID: id, icon: icDatabase,
					iconColor: pal.TextDim, expandable: true, ref: db.ObjectRef{Catalog: cat}})
				if s.expanded[kk] {
					rr = s.schemas(rr, cs, cat, 2, q)
				}
			}
		case caps.Schemas:
			rr = s.schemas(rr, cs, "", 1, q)
		default:
			rr = s.folders(rr, cs, metaKey{}, 1, q)
		}
	}
	return rr
}

// pending shows the state of a load (and starts it).
func (s *Sidebar) pending(rr []treeRow, cs *connState, key, parent string, depth int, start func()) []treeRow {
	if err := cs.errs[key]; err != nil {
		return append(rr, treeRow{key: parent + "|err", kind: nInfo, depth: depth, label: errLine(err), isErr: true, connID: cs.id})
	}
	start()
	return append(rr, treeRow{key: parent + "|loading", kind: nInfo, depth: depth, label: "Loading…", connID: cs.id})
}

func (s *Sidebar) schemas(rr []treeRow, cs *connState, cat string, depth int, q string) []treeRow {
	id := cs.id
	list, ok := cs.schemas[cat]
	if !ok {
		return s.pending(rr, cs, "schemas:"+cat, catKey(id, cat), depth, func() { cs.loadSchemas(cat) })
	}
	var sys []db.Schema
	for _, sc := range list {
		if sc.System {
			sys = append(sys, sc)
			continue
		}
		rr = s.schemaRow(rr, cs, cat, sc, depth, q)
	}
	if len(sys) > 0 {
		sk := sysKey(id, cat)
		rr = append(rr, treeRow{key: sk, kind: nSysFolder, depth: depth, label: "System schemas", extra: itoa(len(sys)),
			connID: id, icon: icSettings, iconColor: pal.TextDim, expandable: true})
		if s.expanded[sk] {
			for _, sc := range sys {
				rr = s.schemaRow(rr, cs, cat, sc, depth+1, q)
			}
		}
	}
	return rr
}

func (s *Sidebar) schemaRow(rr []treeRow, cs *connState, cat string, sc db.Schema, depth int, q string) []treeRow {
	k := metaKey{cat, sc.Name}
	sk := schemaKey(cs.id, k)
	ic := icSchema
	if s.expanded[sk] {
		ic = icSchemaOpen
	}
	extra := ""
	if sc.Current {
		extra = "default"
	}
	rr = append(rr, treeRow{key: sk, kind: nSchema, depth: depth, label: sc.Name, extra: extra, connID: cs.id,
		icon: ic, iconColor: pal.Accent, expandable: true, ref: db.ObjectRef{Catalog: cat, Schema: sc.Name, Type: db.ObjSchema}})
	if s.expanded[sk] || q != "" && cs.objects[k] != nil {
		rr = s.folders(rr, cs, k, depth+1, q)
	}
	return rr
}

func (s *Sidebar) folders(rr []treeRow, cs *connState, k metaKey, depth int, q string) []treeRow {
	objs, ok := cs.objects[k]
	parent := schemaKey(cs.id, k)
	if !ok {
		return s.pending(rr, cs, "objects:"+k.catalog+"."+k.schema, parent, depth, func() { cs.loadObjects(k) })
	}
	byType := map[db.ObjectType][]db.Object{}
	for _, o := range objs {
		if q != "" && !strings.Contains(strings.ToLower(o.Ref.Name), q) {
			continue
		}
		byType[o.Ref.Type] = append(byType[o.Ref.Type], o)
	}
	if len(objs) == 0 {
		return append(rr, treeRow{key: parent + "|empty", kind: nInfo, depth: depth, label: "No objects", connID: cs.id})
	}
	for _, t := range folderOrder {
		list := byType[t]
		if len(list) == 0 {
			continue
		}
		fk := folderKey(cs.id, k, t)
		open := s.expanded[fk] || q != ""
		rr = append(rr, treeRow{key: fk, kind: nFolder, depth: depth, label: t.Plural(), extra: itoa(len(list)), connID: cs.id,
			icon: objIcon(t), iconColor: pal.TextDim, expandable: true, folder: t, ref: db.ObjectRef{Catalog: k.catalog, Schema: k.schema}})
		if !open {
			continue
		}
		for _, o := range list {
			ok := objKey(cs.id, o.Ref)
			label := o.Ref.Name
			if o.Ref.Signature != "" && o.Ref.Signature != "()" {
				label += o.Ref.Signature
			}
			rr = append(rr, treeRow{key: ok, kind: nObject, depth: depth + 1, label: label, extra: o.Extra, connID: cs.id,
				icon: objIcon(t), iconColor: objColor(t), expandable: t.Relation(), ref: o.Ref})
			if t.Relation() && s.expanded[ok] {
				rr = s.columns(rr, cs, o.Ref, depth+2)
			}
		}
	}
	return rr
}

func objColor(t db.ObjectType) colorNRGBA {
	switch t {
	case db.ObjTable:
		return pal.Info
	case db.ObjView, db.ObjMatView:
		return pal.Ok
	case db.ObjFunction, db.ObjProcedure, db.ObjPackage:
		return pal.Func
	case db.ObjSequence:
		return pal.Number
	}
	return pal.TextDim
}

func (s *Sidebar) columns(rr []treeRow, cs *connState, ref db.ObjectRef, depth int) []treeRow {
	r := ref
	r.Signature = ""
	t, ok := cs.tables[r]
	parent := objKey(cs.id, ref)
	if !ok {
		return s.pending(rr, cs, tableKey(r), parent, depth, func() { cs.loadTable(r, nil) })
	}
	for _, c := range t.Columns {
		ic, col := icColumn, pal.TextDim
		if c.PrimaryKeyNo > 0 {
			ic, col = icKey, pal.Accent
		}
		typ := c.Type
		if !c.Nullable {
			typ += " not null"
		}
		rr = append(rr, treeRow{key: parent + "|col|" + c.Name, kind: nColumn, depth: depth, label: c.Name, extra: typ,
			connID: cs.id, icon: ic, iconColor: col, ref: r, isKey: c.PrimaryKeyNo > 0})
	}
	return rr
}

func errLine(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i > 0 {
		s = s[:i]
	}
	return s
}

func (s *Sidebar) click(key string) *widget.Clickable {
	c, ok := s.clicks[key]
	if !ok {
		c = new(widget.Clickable)
		s.clicks[key] = c
	}
	return c
}

func (s *Sidebar) tag(key string) *int {
	t, ok := s.tags[key]
	if !ok {
		t = new(int)
		s.tags[key] = t
	}
	return t
}

// activate is a double click / Enter on a row.
func (s *Sidebar) activate(r treeRow) {
	a := s.app
	switch r.kind {
	case nObject:
		switch {
		case r.ref.Type.Relation():
			a.openObject(r.connID, r.ref, "data")
		default:
			a.openObject(r.connID, r.ref, "ddl")
		}
	case nColumn:
		a.openObject(r.connID, r.ref, "structure")
	case nConn:
		cs := a.connState(r.connID)
		if cs.status == stFailed {
			cs.status = stIdle
		}
		s.expand(r.key, !s.expanded[r.key])
	default:
		if r.expandable {
			s.expand(r.key, !s.expanded[r.key])
		}
	}
}

// sidebarWidth is the sidebar width in px.
func (a *App) sidebarWidth(gtx C) int {
	w := a.sidebar.width
	if w == 0 {
		w = gtx.Dp(270)
	}
	return max(gtx.Dp(180), min(w, gtx.Constraints.Max.X/2))
}

// Layout draws the sidebar.
func (s *Sidebar) Layout(gtx C) D {
	a, th := s.app, s.app.th
	w := a.sidebarWidth(gtx)
	gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
	gtx.Constraints.Min.Y = gtx.Constraints.Max.Y
	fill(gtx, pal.Sidebar)
	if s.addBtn.Clicked(gtx) {
		a.openConnDialog("")
	}
	if s.refBtn.Clicked(gtx) {
		if r, ok := s.selectedRow(); ok {
			s.refresh(r)
		}
	}
	if s.colBtn.Clicked(gtx) {
		s.expanded = map[string]bool{}
		a.scheduleSave()
	}
	for {
		ev, ok := s.filter.Update(gtx)
		if !ok {
			break
		}
		_ = ev
	}
	d := layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: 12, Right: 6, Top: 8, Bottom: 6}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx C) D { return lbl(gtx, th, fontBold, 11, pal.TextDim, "CONNECTIONS") }),
					layout.Rigid(func(gtx C) D { return iconOnly(gtx, &s.colBtn, icExpandLess, 15) }),
					layout.Rigid(func(gtx C) D { return iconOnly(gtx, &s.refBtn, icRefresh, 15) }),
					layout.Rigid(func(gtx C) D { return iconOnly(gtx, &s.addBtn, icAdd, 15) }),
				)
			})
		}),
		layout.Rigid(func(gtx C) D {
			if len(a.prefs.Connections) == 0 {
				return D{}
			}
			return layout.Inset{Left: 8, Right: 8, Bottom: 6}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx C) D { return textField(gtx, th, &s.filter, "Filter objects…", false) }),
				)
			})
		}),
		layout.Rigid(func(gtx C) D { return hline(gtx, pal.Border) }),
		layout.Flexed(1, func(gtx C) D {
			s.rows = s.build()
			if len(s.rows) == 0 {
				return inset(16).Layout(gtx, func(gtx C) D {
					l := material.Body2(th, "No connections yet. Click + to add one.")
					l.Color = pal.TextDim
					return l.Layout(gtx)
				})
			}
			return s.layoutList(gtx)
		}),
	)
	s.layoutResize(gtx, w)
	return d
}

// layoutResize lets the right edge be dragged.
func (s *Sidebar) layoutResize(gtx C, w int) {
	grip := image.Rect(w-gtx.Dp(4), 0, w, gtx.Constraints.Max.Y)
	st := clip.Rect(grip).Push(gtx.Ops)
	pointer.CursorColResize.Add(gtx.Ops)
	event.Op(gtx.Ops, &s.dragTag)
	st.Pop()
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &s.dragTag, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel})
		if !ok {
			break
		}
		pe, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		switch pe.Kind {
		case pointer.Press:
			s.dragging, s.grabX = true, pe.Position.X
			s.width = w
		case pointer.Drag:
			if s.dragging {
				s.width = w + int(pe.Position.X-s.grabX)
			}
		case pointer.Release, pointer.Cancel:
			s.dragging = false
			s.app.prefs.SidebarWidth = s.width
			s.app.scheduleSave()
		}
	}
}

func (s *Sidebar) layoutList(gtx C) D {
	th := s.app.th
	// keyboard navigation while the tree has the focus
	area := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
	event.Op(gtx.Ops, &s.focusTag)
	area.Pop()
	for {
		ev, ok := gtx.Event(
			key.FocusFilter{Target: &s.focusTag},
			key.Filter{Focus: &s.focusTag, Name: key.NameUpArrow},
			key.Filter{Focus: &s.focusTag, Name: key.NameDownArrow},
			key.Filter{Focus: &s.focusTag, Name: key.NameLeftArrow},
			key.Filter{Focus: &s.focusTag, Name: key.NameRightArrow},
			key.Filter{Focus: &s.focusTag, Name: key.NameReturn},
			key.Filter{Focus: &s.focusTag, Name: key.NameEnter},
			key.Filter{Focus: &s.focusTag, Name: key.NameF5},
			key.Filter{Focus: &s.focusTag, Name: "C", Required: key.ModShortcut},
		)
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			s.onKey(e)
		}
	}
	rh := gtx.Dp(26)
	return material.List(th, &s.list).Layout(gtx, len(s.rows), func(gtx C, i int) D {
		r := s.rows[i]
		cl := s.click(r.key)
		for {
			c, ok := cl.Update(gtx)
			if !ok {
				break
			}
			s.sel = r.key
			gtx.Execute(key.FocusCmd{Tag: &s.focusTag})
			if c.NumClicks >= 2 {
				s.activate(r)
			} else if r.expandable && r.kind != nObject && r.kind != nConn {
				s.expand(r.key, !s.expanded[r.key])
			}
		}
		tg := s.tag(r.key)
		for {
			ev, ok := gtx.Event(pointer.Filter{Target: tg, Kinds: pointer.Press})
			if !ok {
				break
			}
			if pe, ok := ev.(pointer.Event); ok && pe.Buttons == pointer.ButtonSecondary {
				s.sel = r.key
				s.contextMenu(r)
			}
		}
		return cl.Layout(gtx, func(gtx C) D {
			sz := image.Pt(gtx.Constraints.Max.X, rh)
			m := gtx.Dp(4)
			pillR := image.Rect(m, 0, sz.X-m, rh)
			switch {
			case s.sel == r.key:
				fillRRect(gtx, pal.Selected, pillR, gtx.Dp(4))
			case cl.Hovered():
				fillRRect(gtx, pal.Hover, pillR, gtx.Dp(4))
			}
			if s.scrollTo == r.key {
				s.scrollTo = ""
			}
			area := clip.Rect{Max: sz}.Push(gtx.Ops)
			event.Op(gtx.Ops, tg)
			area.Pop()
			gtx.Constraints = layout.Exact(sz)
			left := unit.Dp(8 + 14*r.depth)
			return layout.Inset{Left: left, Right: 8}.Layout(gtx, func(gtx C) D {
				return layout.W.Layout(gtx, func(gtx C) D { return s.layoutRow(gtx, th, r) })
			})
		})
	})
}

func (s *Sidebar) layoutRow(gtx C, th *material.Theme, r treeRow) D {
	a := s.app
	textC := pal.Text
	if s.sel == r.key {
		textC = pal.TextBright
	}
	var ch []layout.FlexChild
	ch = append(ch, layout.Rigid(func(gtx C) D {
		if !r.expandable {
			return D{Size: image.Pt(gtx.Dp(14), gtx.Dp(14))}
		}
		ic := icCollapse
		if s.expanded[r.key] {
			ic = icExpand
		}
		return mdi(gtx, ic, pal.TextDim, 14)
	}))
	if r.kind == nInfo {
		c := pal.TextDim
		if r.isErr {
			c = pal.Err
		}
		ch = append(ch, layout.Rigid(layout.Spacer{Width: 4}.Layout),
			layout.Flexed(1, func(gtx C) D { return a.tipLbl(gtx, "tree/"+r.key, fontSans, 12, c, r.label) }))
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, ch...)
	}
	ch = append(ch,
		layout.Rigid(layout.Spacer{Width: 3}.Layout),
		layout.Rigid(func(gtx C) D {
			if r.kind == nConn {
				return s.connIcon(gtx, r)
			}
			return mdi(gtx, r.icon, r.iconColor, 15)
		}),
		layout.Rigid(layout.Spacer{Width: 7}.Layout),
		layout.Rigid(func(gtx C) D {
			f := fontSans
			if r.kind == nColumn {
				f = fontMono
			}
			size := unit.Sp(12.5)
			if r.kind == nConn {
				size = 13
			}
			gtx.Constraints.Max.X = max(gtx.Dp(40), gtx.Constraints.Max.X*3/4)
			return a.tipLbl(gtx, "tree/"+r.key, f, size, textC, r.label)
		}),
		layout.Flexed(1, func(gtx C) D {
			if r.extra == "" {
				return D{Size: image.Pt(gtx.Constraints.Min.X, 0)}
			}
			return layout.Inset{Left: 6}.Layout(gtx, func(gtx C) D {
				f := fontSans
				if r.kind == nColumn {
					f = fontMono
				}
				return lbl(gtx, th, f, 11, pal.TextDim, r.extra)
			})
		}),
	)
	if r.kind == nConn {
		ch = append(ch, layout.Rigid(func(gtx C) D { return a.connBadges(gtx, r.connID) }))
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx, ch...)
}

// connIcon is the database icon with a status dot.
func (s *Sidebar) connIcon(gtx C, r treeRow) D {
	cs := s.app.connState(r.connID)
	d := mdi(gtx, icDatabase, r.iconColor, 16)
	var c colorNRGBA
	switch cs.status {
	case stReady:
		c = pal.Ok
	case stConnecting:
		c = pal.Warn
	case stFailed:
		c = pal.Err
	default:
		return d
	}
	sz := gtx.Dp(6)
	fillRRect(gtx, pal.Sidebar, image.Rect(d.Size.X-sz-gtx.Dp(1), d.Size.Y-sz-gtx.Dp(1), d.Size.X+gtx.Dp(1), d.Size.Y+gtx.Dp(1)), sz)
	fillRRect(gtx, c, image.Rect(d.Size.X-sz, d.Size.Y-sz, d.Size.X, d.Size.Y), sz/2)
	return d
}

// connBadges shows PROD / RO markers in the tree.
func (a *App) connBadges(gtx C, id string) D {
	th := a.th
	var ch []layout.FlexChild
	if a.connProtected(id) {
		ch = append(ch, layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: 4}.Layout(gtx, func(gtx C) D { return badge(gtx, th, "PROD", rgb(0xffffff), pal.Err) })
		}))
	}
	if a.connReadOnly(id) {
		ch = append(ch, layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: 4}.Layout(gtx, func(gtx C) D { return badge(gtx, th, "RO", rgb(0xffffff), pal.Warn) })
		}))
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx, ch...)
}

func (s *Sidebar) onKey(e key.Event) {
	idx := -1
	for i, r := range s.rows {
		if r.key == s.sel {
			idx = i
		}
	}
	switch e.Name {
	case key.NameDownArrow:
		idx = min(len(s.rows)-1, idx+1)
	case key.NameUpArrow:
		idx = max(0, idx-1)
	case key.NameRightArrow:
		if idx >= 0 && s.rows[idx].expandable && !s.expanded[s.rows[idx].key] {
			s.expand(s.rows[idx].key, true)
		} else {
			idx = min(len(s.rows)-1, idx+1)
		}
	case key.NameLeftArrow:
		if idx >= 0 && s.expanded[s.rows[idx].key] {
			s.expand(s.rows[idx].key, false)
		} else if idx >= 0 {
			// to the parent
			d := s.rows[idx].depth
			for j := idx - 1; j >= 0; j-- {
				if s.rows[j].depth < d {
					idx = j
					break
				}
			}
		}
	case key.NameReturn, key.NameEnter:
		if idx >= 0 {
			s.activate(s.rows[idx])
		}
	case key.NameF5:
		if idx >= 0 {
			s.refresh(s.rows[idx])
		}
	case "C":
		if idx >= 0 {
			s.app.copyLater(s.rows[idx].label)
		}
	}
	if idx >= 0 && idx < len(s.rows) {
		s.sel = s.rows[idx].key
		s.ensureVisible(idx)
	}
}

func (s *Sidebar) ensureVisible(i int) {
	p := s.list.Position
	if i < p.First {
		s.list.ScrollTo(i)
	} else if p.Count > 0 && i >= p.First+p.Count-1 {
		s.list.ScrollTo(max(0, i-p.Count+2))
	}
}

// refresh reloads the metadata under a node.
func (s *Sidebar) refresh(r treeRow) {
	cs := s.app.connState(r.connID)
	switch r.kind {
	case nConn, nInfo:
		if cs.status == stFailed {
			cs.status = stIdle
			s.expand(connKey(r.connID), true)
			return
		}
		cs.refreshAll()
	case nCatalog:
		delete(cs.schemas, r.ref.Catalog)
	case nSchema, nFolder:
		cs.refreshSchema(keyOf(r.ref))
	case nObject, nColumn:
		ref := r.ref
		ref.Signature = ""
		delete(cs.tables, ref)
		cs.refreshSchema(keyOf(r.ref))
	}
	s.app.invalidate()
}

// contextMenu builds the actions of a node.
func (s *Sidebar) contextMenu(r treeRow) {
	a := s.app
	var items []*menuItem
	add := func(ic *widget.Icon, label, hint string, danger bool, fn func()) {
		items = append(items, &menuItem{icon: ic, label: label, hint: hint, danger: danger, action: fn})
	}
	sep := func() {
		if len(items) > 0 && !items[len(items)-1].sep {
			items = append(items, &menuItem{sep: true})
		}
	}
	cs := a.connState(r.connID)
	title := r.label
	switch r.kind {
	case nConn:
		if cs.status == stReady {
			add(icConsole, "New console", shortcutLabel("T"), false, func() { a.newConsole(r.connID, cs.defaultSchema(""), "") })
			add(icPlug, "Disconnect", "", false, func() { a.requestDisconnect(r.connID, nil) })
		} else {
			add(icPlug, "Connect", "", false, func() {
				cs.status = stIdle
				s.expand(r.key, true)
				a.connect(r.connID, nil)
			})
		}
		add(icRefresh, "Refresh", "F5", false, func() { s.refresh(r) })
		sep()
		add(icEdit, "Edit connection…", "", false, func() { a.openConnDialog(r.connID) })
		add(icCopyText, "Duplicate", "", false, func() { a.duplicateConn(r.connID) })
		add(icHistory, "Query history", "", false, func() { a.openHistory(r.connID) })
		sep()
		add(icDelete, "Delete connection", "", true, func() { a.deleteConn(r.connID) })
	case nCatalog, nSchema:
		add(icConsole, "New console here", "", false, func() { a.newConsole(r.connID, r.ref.Schema, "") })
		add(icRefresh, "Refresh", "F5", false, func() { s.refresh(r) })
		add(icCopyText, "Copy name", "", false, func() { a.copyLater(r.label) })
	case nFolder:
		add(icRefresh, "Refresh", "F5", false, func() { s.refresh(r) })
	case nObject:
		ref := r.ref
		d := cs.dialect()
		if ref.Type.Relation() {
			add(icTable, "Open data", "⏎", false, func() { a.openObject(r.connID, ref, "data") })
			add(icColumn, "Structure", "", false, func() { a.openObject(r.connID, ref, "structure") })
		}
		add(icDescribe, "DDL", "", false, func() { a.openObject(r.connID, ref, "ddl") })
		sep()
		if ref.Type.Relation() && d != nil {
			add(icConsole, "SELECT in new console", "", false, func() {
				a.newConsole(r.connID, ref.Schema, "SELECT * FROM "+d.Qualified(ref)+";\n")
			})
			add(icConsole, "Generate INSERT / UPDATE / DELETE…", "", false, func() { a.generateDML(r.connID, ref) })
		}
		add(icCopyText, "Copy name", "", false, func() { a.copyLater(ref.Name) })
		if d != nil {
			add(icCopyText, "Copy qualified name", "", false, func() { a.copyLater(d.Qualified(ref)) })
		}
		add(icAI, "Ask the assistant", "", false, func() { a.askAboutObject(r.connID, ref) })
		sep()
		add(icRefresh, "Refresh", "F5", false, func() { s.refresh(r) })
		if ref.Type == db.ObjTable {
			add(icClear, "Truncate…", "", true, func() { a.dropObject(r.connID, ref, true) })
		}
		add(icDelete, "Drop…", "", true, func() { a.dropObject(r.connID, ref, false) })
	case nColumn:
		add(icCopyText, "Copy name", "", false, func() { a.copyLater(r.label) })
		add(icColumn, "Structure", "", false, func() { a.openObject(r.connID, r.ref, "structure") })
	default:
		add(icRefresh, "Retry", "", false, func() { s.refresh(r) })
		if r.isErr {
			add(icCopyText, "Copy error", "", false, func() { a.copyLater(r.label) })
			add(icEdit, "Edit connection…", "", false, func() { a.openConnDialog(r.connID) })
		}
	}
	if n := len(items); n > 0 && items[n-1].sep {
		items = items[:n-1]
	}
	a.overlay = &ContextMenu{app: a, at: a.pointer, title: title, items: items, sel: -1}
}

// dialect returns the connection's dialect (nil when not connected).
func (cs *connState) dialect() db.Dialect {
	if cs.conn != nil {
		return cs.conn.Dialect()
	}
	return nil
}

func itoa(n int) string { return fmt.Sprint(n) }
