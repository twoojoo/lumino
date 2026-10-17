package ui

import (
	"context"
	"errors"
	"image"
	"strconv"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/twoojoo/lumino/internal/db"
)

// Shared helpers of the management features of document databases:
// statements reviewed then run under the connection policies, reads of
// documents, and a small form dialog.

// docTools returns the management features of a connection (nil when the
// database has none or is not connected).
func (cs *connState) docTools() db.DocumentTools {
	if cs.conn == nil {
		return nil
	}
	t, _ := cs.conn.(db.DocumentTools)
	return t
}

// docLanguage returns the document language of a connection.
func (cs *connState) docLanguage() (db.DocumentLanguage, bool) {
	dl, ok := cs.language().(db.DocumentLanguage)
	return dl, ok
}

// runStatement runs one statement on a fresh session (read-only on
// read-only connections) and drains its result.
func runStatement(ctx context.Context, conn db.Conn, ro bool, stmt string) error {
	s, err := conn.Session(ctx)
	if err != nil {
		return err
	}
	defer s.Close()
	if ro {
		if err := s.SetReadOnly(ctx, true); err != nil && !errors.Is(err, db.ErrNotSupported) {
			return err
		}
	}
	res, err := s.Execute(ctx, stmt)
	if err != nil {
		return err
	}
	defer res.Close()
	for res.Columns() != nil {
		_, more, err := res.Fetch(ctx, 1000)
		if err != nil {
			return err
		}
		if !more {
			break
		}
	}
	return nil
}

// reviewRun shows a management statement for review (typed confirmation
// on protected connections), then runs it; shown replaces the statement
// in the review (passwords). done runs after success.
func (a *App) reviewRun(connID, title, stmt, shown string, done func()) {
	cs := a.connState(connID)
	lang := cs.language()
	if k := lang.Classify(stmt); k.Modifies() && !a.allowMutation(connID, title) {
		return
	}
	if shown == "" {
		shown = stmt
	}
	a.overlay = newSQLReview(a, connID, title, lang.Terminate(shown)+"\n", "", 1, func() {
		a.connectOr(connID, func(cs *connState) {
			conn, ro := cs.conn, a.connReadOnly(connID)
			a.do(title, func() error {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				defer cancel()
				return runStatement(ctx, conn, ro, stmt)
			}, func() {
				if done != nil {
					done()
				}
			})
		}, func(err error) { a.flash(err.Error(), true) })
	})
}

// queryDocs reads the documents of a read statement on a fresh session.
func (a *App) queryDocs(connID, stmt string, then func([]db.DocNode, error)) {
	cs := a.connState(connID)
	if k := cs.language().Classify(stmt); k.Modifies() {
		then(nil, errors.New("not a read statement"))
		return
	}
	a.connectOr(connID, func(cs *connState) {
		conn := cs.conn
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			var docs []db.DocNode
			err := func() error {
				s, err := conn.Session(ctx)
				if err != nil {
					return err
				}
				defer s.Close()
				_ = s.SetReadOnly(ctx, true)
				res, err := s.Execute(ctx, stmt)
				if err != nil {
					return err
				}
				defer res.Close()
				for res.Columns() != nil && len(docs) < 10000 {
					rows, more, err := res.Fetch(ctx, 1000)
					if err != nil {
						return err
					}
					d := docsOf(res, len(rows))
					if d == nil {
						return errors.New("the result has no documents")
					}
					docs = append(docs, d...)
					if !more {
						break
					}
				}
				return nil
			}()
			a.post(func() { then(docs, err) })
		}()
	}, func(err error) { then(nil, err) })
}

// ---- form dialog ----

// formField is a field of a FormDialog: code (in the connection's
// language), multi-line code, or plain text (masked for passwords).
type formField struct {
	label  string
	hint   string
	code   *CodeEditor
	text   widget.Editor
	multi  bool
	plain  bool
	masked bool
}

// FormDialog asks a few values; submit validates them and returns an
// error to show, or nil to close.
type FormDialog struct {
	app      *App
	title    string
	note     string
	fields   []*formField
	submit   func(vals []string) error
	err      string
	done     bool
	ok, canc widget.Clickable
	okLabel  string
	d        dialog
}

func newFormDialog(a *App, lang db.Language, title, note string, submit func([]string) error, fields ...*formField) *FormDialog {
	f := &FormDialog{app: a, title: title, note: note, fields: fields, submit: submit, okLabel: "Review…"}
	for _, fl := range fields {
		if fl.plain {
			fl.text.SingleLine, fl.text.Submit = true, true
			if fl.masked {
				fl.text.Mask = '•'
			}
			continue
		}
		init := ""
		if fl.code != nil {
			init = fl.code.Text()
		}
		fl.code = newCodeEditor(a, init)
		fl.code.setLanguage(lang)
		fl.code.SingleLine = !fl.multi
		fl.code.Hint = fl.hint
		fl.code.OnSubmit = f.run
		fl.code.OnCommand = func(cmd string) bool { f.run(); return true }
		fl.code.Completer = func(e *CodeEditor) *compRequest { return queryItems(lang, nil) }
	}
	return f
}

// codeField is a one-line code field with an initial text.
func codeField(label, hint, text string) *formField {
	e := newCodeEditor(nil, text)
	return &formField{label: label, hint: hint, code: e}
}

// multiField is a multi-line code field.
func multiField(label, text string) *formField {
	f := codeField(label, "", text)
	f.multi = true
	return f
}

// textField is a plain text field.
func plainField(label, hint, text string, masked bool) *formField {
	f := &formField{label: label, hint: hint, plain: true, masked: masked}
	f.text.SetText(text)
	return f
}

func (f *FormDialog) values() []string {
	out := make([]string, len(f.fields))
	for i, fl := range f.fields {
		if fl.plain {
			out[i] = fl.text.Text()
		} else {
			out[i] = fl.code.Text()
		}
	}
	return out
}

func (f *FormDialog) run() {
	if err := f.submit(f.values()); err != nil {
		f.err = err.Error()
		return
	}
	f.done = true
}

func (f *FormDialog) Done() bool { return f.done }

func (f *FormDialog) Layout(gtx C) D {
	a, th := f.app, f.app.th
	if f.canc.Clicked(gtx) || f.d.close.Clicked(gtx) {
		f.done = true
	}
	if f.ok.Clicked(gtx) {
		f.run()
	}
	for _, fl := range f.fields {
		if !fl.plain {
			continue
		}
		for {
			ev, ok := fl.text.Update(gtx)
			if !ok {
				break
			}
			if _, ok := ev.(widget.SubmitEvent); ok {
				f.run()
			}
		}
	}
	var rows []layout.FlexChild
	if f.note != "" {
		rows = append(rows, layout.Rigid(func(gtx C) D {
			return layout.Inset{Bottom: 10}.Layout(gtx, func(gtx C) D { return lbl(gtx, th, fontSans, 12, pal.TextDim, f.note) })
		}))
	}
	for _, fl := range f.fields {
		fl := fl
		rows = append(rows, layout.Rigid(func(gtx C) D {
			return layout.Inset{Bottom: 8}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontSans, 12, pal.Text, fl.label) }),
					layout.Rigid(layout.Spacer{Height: 4}.Layout),
					layout.Rigid(func(gtx C) D {
						switch {
						case fl.plain:
							return textField(gtx, th, &fl.text, fl.hint, false)
						case fl.multi:
							gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, gtx.Dp(200)))
							return rborder(gtx, pal.Border, pal.CodeBg, 6, func(gtx C) D { return inset(1).Layout(gtx, fl.code.Layout) })
						}
						return queryField(gtx, fl.code)
					}),
				)
			})
		}))
	}
	if f.err != "" {
		rows = append(rows, layout.Rigid(func(gtx C) D {
			return a.selText(gtx, "formerr", fontMono, 12, pal.Err, f.err, 0)
		}))
	}
	return f.d.layout(gtx, th, 640, icEdit, f.title, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
	}, footerButtons(nil,
		func(gtx C) D { return pillButton(gtx, th, &f.canc, nil, "Cancel", pal.Text, pal.Hover) },
		func(gtx C) D { return button(gtx, th, &f.ok, f.okLabel, pal.Accent) },
	))
}

// maskPassword hides a password literal in a statement shown for review.
func maskPassword(stmt, pw string) string {
	if pw == "" {
		return stmt
	}
	return strings.Replace(stmt, "pwd: "+strconv.Quote(pw), `pwd: "••••••"`, 1)
}
