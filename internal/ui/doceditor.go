package ui

import (
	"image"
	"strings"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/twoojoo/lumino/internal/db"
)

// DocEditor edits a document (or one field value) as text in the
// connection's language; save validates it and hands it to the owner,
// which shows the generated statement for review.
type DocEditor struct {
	app      *App
	title    string
	note     string
	ed       *CodeEditor
	save     func(text string) error
	err      string
	done     bool
	ok, canc widget.Clickable
	d        dialog
	single   bool
}

// newDocEditor opens text for editing; single edits one value on one line.
func newDocEditor(a *App, lang db.Language, title, note, text string, single bool, fields func() []string, save func(string) error) *DocEditor {
	de := &DocEditor{app: a, title: title, note: note, save: save, single: single}
	de.ed = newCodeEditor(a, text)
	de.ed.setLanguage(lang)
	de.ed.SingleLine = single
	de.ed.OnSubmit = de.submit
	de.ed.OnCommand = func(cmd string) bool {
		if cmd == "run" || cmd == "runAll" {
			de.submit()
			return true
		}
		return false
	}
	de.ed.Completer = func(*CodeEditor) *compRequest { return queryItems(lang, fields) }
	de.ed.SetCaretOffset(0)
	return de
}

func (de *DocEditor) Done() bool { return de.done }

func (de *DocEditor) submit() {
	if err := de.save(de.ed.Text()); err != nil {
		de.err = err.Error()
		return
	}
	de.done = true
}

func (de *DocEditor) Layout(gtx C) D {
	a, th := de.app, de.app.th
	if de.canc.Clicked(gtx) || de.d.close.Clicked(gtx) {
		de.done = true
	}
	if de.ok.Clicked(gtx) {
		de.submit()
	}
	return de.d.layout(gtx, th, 820, icEdit, de.title, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				if de.note == "" {
					return D{}
				}
				return layout.Inset{Bottom: 8}.Layout(gtx, func(gtx C) D { return lbl(gtx, th, fontSans, 12, pal.TextDim, de.note) })
			}),
			layout.Rigid(func(gtx C) D {
				h := gtx.Dp(420)
				if de.single {
					h = de.ed.lineHeight(gtx) + gtx.Dp(10)
				}
				gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, h))
				return rborder(gtx, pal.Border, pal.CodeBg, 6, func(gtx C) D {
					return layout.Inset{Top: 4, Bottom: 4, Left: 1, Right: 1}.Layout(gtx, de.ed.Layout)
				})
			}),
			layout.Rigid(func(gtx C) D {
				if de.err == "" {
					return D{}
				}
				return layout.Inset{Top: 8}.Layout(gtx, func(gtx C) D {
					return a.selText(gtx, "docerr", fontMono, 12, pal.Err, de.err, 0)
				})
			}),
		)
	}, footerButtons(func(gtx C) D {
		return lbl(gtx, th, fontSans, 11.5, pal.TextDim, shortcutLabel("Enter")+" to save")
	},
		func(gtx C) D { return pillButton(gtx, th, &de.canc, nil, "Cancel", pal.Text, pal.Hover) },
		func(gtx C) D { return button(gtx, th, &de.ok, "Review changes…", pal.Accent) },
	))
}

// queryItems are the completion items of query bars and document
// editors: the fields, then the operators (document languages) or the
// keywords and functions (SQL).
func queryItems(lang db.Language, fields func() []string) *compRequest {
	var items []compItem
	if fields != nil {
		for _, f := range fields() {
			items = append(items, compItem{label: f, kind: "column"})
		}
	}
	if dl, ok := lang.(db.DocumentLanguage); ok {
		for _, op := range dl.Operators() {
			items = append(items, compItem{label: op, kind: "keyword"})
		}
		for _, f := range lang.Functions() {
			if f != "" && f[0] >= 'A' && f[0] <= 'Z' { // value helpers: ObjectId(), ISODate()…
				items = append(items, compItem{label: f, kind: "function", insert: f + "()"})
			}
		}
		return &compRequest{items: items}
	}
	for kw := range lang.Keywords() {
		items = append(items, compItem{label: kw, kind: "keyword"})
	}
	for _, f := range lang.Functions() {
		items = append(items, compItem{label: f, kind: "function", insert: f + "()"})
	}
	return &compRequest{items: items}
}

// docKey is the key of a document for edits (its _id, as written).
func docKey(dl db.DocumentLanguage, d db.DocNode) ([]db.ColValue, bool) {
	id, ok := d.Field("_id")
	if !ok {
		return nil, false
	}
	lit := id.Lit
	if lit == "" {
		// a composite _id document, its fields in their order
		lit = dl.FormatDocument(id)
	}
	return []db.ColValue{{Column: "_id", Kind: id.Kind, Value: db.Raw(lit)}}, true
}

// oneLine joins a literal on one line (single-line editors).
func oneLine(s string) string {
	if !strings.Contains(s, "\n") {
		return s
	}
	return strings.Join(strings.Fields(s), " ")
}
