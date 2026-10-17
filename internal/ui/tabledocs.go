package ui

import (
	"errors"
	"image"
	"strings"

	"gioui.org/layout"

	"github.com/twoojoo/lumino/internal/db"
)

// Document actions of the table view (document databases): editing a
// whole document as text, one field by path, inserting and deleting.

// queryField draws a one-line code editor of a query bar.
func queryField(gtx C, e *CodeEditor) D {
	b := pal.Border
	if e.focused {
		b = pal.Accent
	}
	h := e.lineHeight(gtx) + gtx.Dp(10)
	gtx.Constraints.Min.Y, gtx.Constraints.Max.Y = h, h
	return rborder(gtx, b, pal.CodeBg, 6, func(gtx C) D {
		return layout.Inset{Top: 5, Bottom: 5, Left: 2, Right: 4}.Layout(gtx, func(gtx C) D {
			gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, e.lineHeight(gtx)))
			return e.Layout(gtx)
		})
	})
}

// fieldNames are the (dotted) fields of the relation, for completion.
func (t *TableTab) fieldNames() []string {
	if t.info == nil {
		return nil
	}
	out := make([]string, 0, len(t.info.Columns))
	for _, c := range t.info.Columns {
		out = append(out, c.Name)
	}
	return out
}

// currentRow is the displayed row of the selection (grid, tree or text).
func (t *TableTab) currentRow() (int, bool) {
	if t.data == nil {
		return 0, false
	}
	if t.mode != viewTable && t.data.rs.hasDocs() {
		r, _, ok := t.docs.selected(t.data.rs, t.mode)
		return r, ok
	}
	if s, ok := t.grid.Selection(); ok && s.r1 < len(t.data.rs.rows) {
		return s.r1, true
	}
	return 0, false
}

func (t *TableTab) docLang() (db.DocumentLanguage, bool) {
	dl, ok := t.cs().language().(db.DocumentLanguage)
	return dl, ok
}

// canEditDocs reports why documents can't be edited ("" = they can).
func (t *TableTab) docEditBlocked() string {
	switch {
	case t.data == nil:
		return "nothing loaded"
	case t.readOnlyReason() != "":
		return t.readOnlyReason()
	case t.data.pending() > 0:
		return "save or revert the pending grid changes first"
	case t.saving:
		return "a save is running"
	}
	return ""
}

// reviewDoc shows the statement of document changes, then applies them.
func (t *TableTab) reviewDoc(title string, changes []db.Change) error {
	a := t.a
	if why := t.docEditBlocked(); why != "" {
		return errors.New(why)
	}
	if !a.allowMutation(t.connID, "Saving changes") {
		return errors.New("blocked by the connection policy")
	}
	lang := t.cs().language()
	var b strings.Builder
	for _, ch := range changes {
		q := lang.RenderChange(t.ref, ch)
		if strings.HasPrefix(q, "// ") {
			return errors.New(strings.TrimPrefix(q, "// "))
		}
		b.WriteString(lang.Terminate(q) + "\n")
	}
	a.overlay = newSQLReview(a, t.connID, title, b.String(), "", len(changes), func() { t.apply(changes) })
	return nil
}

// editDocument edits the document of a displayed row as text.
func (t *TableTab) editDocument(r int) {
	a := t.a
	dl, ok := t.docLang()
	if !ok || t.data == nil {
		return
	}
	if why := t.docEditBlocked(); why != "" {
		a.flash("Documents can't be edited: "+why, true)
		return
	}
	d, ok := t.data.rs.doc(r)
	if !ok {
		return
	}
	note := "Changed and new fields are set by path, removed ones unset; the _id can't change."
	a.overlay = newDocEditor(a, dl, "Edit document of "+t.ref.Name, note, dl.FormatDocument(d), false, t.fieldNames, func(text string) error {
		key, vals, err := dl.DocumentChanges(d, text)
		if err != nil {
			return err
		}
		if len(vals) == 0 {
			return errors.New("nothing changed")
		}
		return t.reviewDoc("Save the document", []db.Change{{Op: db.OpUpdate, Key: key, Values: vals}})
	})
}

// insertDocument edits a new document (a copy of the selected one
// without its _id, or the fields of the collection).
func (t *TableTab) insertDocument() {
	a := t.a
	dl, ok := t.docLang()
	if !ok {
		return
	}
	if why := t.docEditBlocked(); why != "" {
		a.flash("Documents can't be inserted: "+why, true)
		return
	}
	text := "{\n  \n}"
	if r, ok := t.currentRow(); ok {
		if d, ok := t.data.rs.doc(r); ok {
			cp := d
			cp.Children = nil
			for _, c := range d.Children {
				if c.Name != "_id" {
					cp.Children = append(cp.Children, c)
				}
			}
			text = dl.FormatDocument(cp)
		}
	}
	note := "A new _id (ObjectId) is generated when the document has none."
	a.overlay = newDocEditor(a, dl, "Insert into "+t.ref.Name, note, text, false, t.fieldNames, func(text string) error {
		vals, err := dl.NewDocument(text)
		if err != nil {
			return err
		}
		return t.reviewDoc("Insert the document", []db.Change{{Op: db.OpInsert, Values: vals}})
	})
}

// docMenu adds the editing actions of a tree line.
func (t *TableTab) docMenu(r int, path string, n db.DocNode) []*menuItem {
	a := t.a
	dl, ok := t.docLang()
	if !ok {
		return nil
	}
	d, ok := t.data.rs.doc(r)
	if !ok {
		return nil
	}
	var items []*menuItem
	if path != "" && n.Lit != "" {
		lit, kind := n.Lit, n.Kind
		items = append(items, &menuItem{icon: icFilter, label: "Filter by this value", action: func() {
			t.refresh(func() { t.where.SetText(oneLine(dl.EqualsFilter(path, db.Raw(lit), kind))) })
		}})
	}
	if t.readOnlyReason() != "" {
		return items
	}
	key, hasKey := docKey(d)
	if !hasKey {
		return items
	}
	if path != "" && path != "_id" {
		text := n.Lit
		if text == "" {
			text = oneLine(dl.FormatDocument(n))
		}
		items = append(items,
			&menuItem{icon: icEdit, label: "Edit value…", action: func() {
				a.overlay = newDocEditor(a, dl, "Edit "+path, "The value is written in the shell syntax: its type is kept as written (NumberLong(…), ISODate(…)…).",
					text, true, t.fieldNames, func(text string) error {
						return t.reviewDoc("Set "+path, []db.Change{{Op: db.OpUpdate, Key: key, Values: []db.ColValue{{Column: path, Kind: db.KindOther, Value: db.Raw(text)}}}})
					})
			}},
			&menuItem{icon: icClear, label: "Remove field", danger: true, action: func() {
				if err := t.reviewDoc("Remove "+path, []db.Change{{Op: db.OpUpdate, Key: key, Values: []db.ColValue{{Column: path, Value: db.Unset}}}}); err != nil {
					a.flash(err.Error(), true)
				}
			}})
	}
	items = append(items,
		&menuItem{icon: icEdit, label: "Edit document…", action: func() { t.editDocument(r) }},
		&menuItem{icon: icRowDel, label: "Delete document", danger: true, action: func() {
			if err := t.reviewDoc("Delete the document", []db.Change{{Op: db.OpDelete, Key: key}}); err != nil {
				a.flash(err.Error(), true)
			}
		}},
	)
	return items
}
