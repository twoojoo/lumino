package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/twoojoo/lumino/internal/db"
)

// ---- import ----

var importFormats = []string{"json", "csv", "bson"}

// ImportDialog imports a JSON, CSV or BSON file into a collection.
type ImportDialog struct {
	app      *App
	connID   string
	ref      db.ObjectRef
	path     widget.Editor
	format   int
	drop     bool
	typed    bool
	running  bool
	n        atomic.Int64
	result   string
	failed   bool
	cancel   context.CancelFunc
	after    func()
	done     bool
	d        dialog
	fmtBtns  [3]widget.Clickable
	dropBtn  widget.Clickable
	typedBtn widget.Clickable
	ok, stop widget.Clickable
	lastPath string
}

func (a *App) openImport(connID string, ref db.ObjectRef, after func()) {
	if a.connState(connID).docTools() == nil {
		a.flash("Import is not available on this connection", true)
		return
	}
	if !a.allowMutation(connID, "Import") {
		return
	}
	d := &ImportDialog{app: a, connID: connID, ref: ref, typed: true, after: after}
	d.path.SingleLine = true
	d.path.SetText(downloadsDir() + string(filepath.Separator))
	a.overlay = d
}

func (d *ImportDialog) Done() bool { return d.done }

func (d *ImportDialog) cancelled() {
	if d.cancel != nil {
		d.cancel()
	}
}

// begin asks the protected confirmation (typed when dropping), then runs.
func (d *ImportDialog) begin() {
	a := d.app
	path := strings.TrimSpace(d.path.Text())
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	if st, err := os.Stat(path); err != nil || st.IsDir() {
		d.result, d.failed = "Choose a file to import", true
		return
	}
	if a.connProtected(d.connID) || d.drop {
		what := fmt.Sprintf("Import %s into %s", filepath.Base(path), d.ref.String())
		if d.drop {
			what += " after dropping the collection (its documents and indexes are lost)"
		}
		c := newConfirm(a, "Import", what+".", true, func(bool) {
			a.overlay = d
			d.run(path)
		})
		if d.drop {
			c.typeName = d.ref.Name
		}
		c.prodCtx = a.connState(d.connID).name()
		c.okLabel = "Import"
		a.overlay = c
		return
	}
	d.run(path)
}

func (d *ImportDialog) run(path string) {
	a := d.app
	tools := a.connState(d.connID).docTools()
	if tools == nil {
		d.result, d.failed = "Not connected", true
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel, d.running, d.result, d.failed = cancel, true, "", false
	d.n.Store(0)
	o := db.ImportOptions{Format: importFormats[d.format], Drop: d.drop, CSVTypes: d.typed}
	ref := d.ref
	go func() {
		defer cancel()
		f, err := os.Open(path)
		var n int64
		if err == nil {
			n, err = tools.Import(ctx, ref, f, o, func(k int64) { d.n.Store(k); a.invalidate() })
			f.Close()
		}
		a.post(func() {
			d.running = false
			if err != nil {
				d.result, d.failed = fmt.Sprintf("Imported %d document(s), then: %v", n, db.AsError(err)), true
			} else {
				d.result = fmt.Sprintf("Imported %d document(s) into %s", n, ref.String())
			}
			if n > 0 && d.after != nil {
				d.after()
			}
		})
	}()
}

func (d *ImportDialog) Layout(gtx C) D {
	a, th := d.app, d.app.th
	if d.d.close.Clicked(gtx) {
		d.cancelled()
		d.done = true
	}
	if d.ok.Clicked(gtx) && !d.running {
		d.begin()
	}
	if d.stop.Clicked(gtx) {
		if d.running {
			d.cancelled()
		} else {
			d.done = true
		}
	}
	if d.dropBtn.Clicked(gtx) {
		d.drop = !d.drop
	}
	if d.typedBtn.Clicked(gtx) {
		d.typed = !d.typed
	}
	for i := range d.fmtBtns {
		if d.fmtBtns[i].Clicked(gtx) {
			d.format = i
		}
	}
	// the format follows the extension
	if p := d.path.Text(); p != d.lastPath {
		d.lastPath = p
		switch strings.ToLower(filepath.Ext(p)) {
		case ".json", ".jsonl", ".ndjson":
			d.format = 0
		case ".csv":
			d.format = 1
		case ".bson":
			d.format = 2
		}
	}
	return d.d.layout(gtx, th, 640, icDownload, "Import into "+d.ref.String(), func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return textField(gtx, th, &d.path, "/path/to/file.json", true) }),
			layout.Rigid(layout.Spacer{Height: 10}.Layout),
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontSans, 12, pal.Text, "Format ") }),
					layout.Rigid(func(gtx C) D {
						return segmented(gtx, th, d.fmtBtns[:], []string{"JSON / JSON lines", "CSV", "BSON"}, d.format)
					}),
				)
			}),
			layout.Rigid(layout.Spacer{Height: 8}.Layout),
			layout.Rigid(func(gtx C) D {
				return lbl(gtx, th, fontSans, 11.5, pal.TextDim, "JSON: an array or one document per line, Extended JSON keeps the types. "+
					"CSV: a header line, dotted names make nested documents. BSON: mongodump files.")
			}),
			layout.Rigid(layout.Spacer{Height: 8}.Layout),
			layout.Rigid(func(gtx C) D {
				return toggle(gtx, th, &d.typedBtn, "CSV: type numbers, booleans and ISO dates", d.typed)
			}),
			layout.Rigid(func(gtx C) D { return toggle(gtx, th, &d.dropBtn, "Drop the collection first", d.drop) }),
			layout.Rigid(layout.Spacer{Height: 8}.Layout),
			layout.Rigid(func(gtx C) D {
				switch {
				case d.running:
					return lbl(gtx, th, fontSans, 12.5, pal.Text, fmt.Sprintf("Importing… %d document(s)", d.n.Load()))
				case d.result != "":
					c := pal.Ok
					if d.failed {
						c = pal.Err
					}
					return a.selText(gtx, "importres", fontSans, 12.5, c, d.result, 0)
				}
				return D{}
			}),
		)
	}, footerButtons(nil,
		func(gtx C) D {
			label := "Close"
			if d.running {
				label = "Stop"
			}
			return pillButton(gtx, th, &d.stop, nil, label, pal.Text, pal.Hover)
		},
		func(gtx C) D {
			c := pal.Accent
			if d.running {
				c = pal.Border
			}
			return button(gtx, th, &d.ok, "Import", c)
		},
	))
}

// ---- users and roles ----

// UsersDialog lists the users of a database and manages them.
type UsersDialog struct {
	app    *App
	connID string
	dbName string
	grid   *DataGrid
	users  []string
	err    error
	done   bool
	d      dialog

	createBtn, dropBtn, grantBtn, revokeBtn, refreshBtn, closeBtn widget.Clickable
}

func (a *App) openUsers(connID, dbName string) {
	if _, ok := a.connState(connID).docLanguage(); !ok {
		return
	}
	u := &UsersDialog{app: a, connID: connID, dbName: dbName, grid: newDataGrid(a)}
	u.load()
	a.overlay = u
}

func (u *UsersDialog) Done() bool { return u.done }

func (u *UsersDialog) load() {
	a := u.app
	dl, _ := a.connState(u.connID).docLanguage()
	stmt, err := dl.AdminStatement("listUsers", db.ObjectRef{Schema: u.dbName})
	if err != nil {
		u.err = err
		return
	}
	a.queryDocs(u.connID, stmt, func(docs []db.DocNode, err error) {
		u.err = err
		m := &sliceModel{cols: textCols("user", "roles", "mechanisms")}
		u.users = nil
		if len(docs) == 1 {
			list, _ := docs[0].Field("users")
			for _, usr := range list.Children {
				name, _ := usr.Field("user")
				roles, _ := usr.Field("roles")
				var rs []string
				for _, r := range roles.Children {
					rn, _ := r.Field("role")
					rd, _ := r.Field("db")
					rs = append(rs, fmt.Sprintf("%v@%v", rn.Value, rd.Value))
				}
				mech, _ := usr.Field("mechanisms")
				var ms []string
				for _, x := range mech.Children {
					ms = append(ms, fmt.Sprint(x.Value))
				}
				u.users = append(u.users, fmt.Sprint(name.Value))
				m.rows = append(m.rows, []any{fmt.Sprint(name.Value), strings.Join(rs, ", "), strings.Join(ms, ", ")})
			}
		}
		u.grid.SetModel(m)
	})
}

func (u *UsersDialog) selected() (string, bool) {
	sel, ok := u.grid.Selection()
	if !ok || sel.r1 >= len(u.users) {
		u.app.flash("Select a user first", false)
		return "", false
	}
	return u.users[sel.r1], true
}

func (u *UsersDialog) Layout(gtx C) D {
	a, th := u.app, u.app.th
	dl, _ := a.connState(u.connID).docLanguage()
	ref := db.ObjectRef{Schema: u.dbName}
	reopen := func() { u.load(); a.overlay = u }
	if u.closeBtn.Clicked(gtx) || u.d.close.Clicked(gtx) {
		u.done = true
	}
	if u.refreshBtn.Clicked(gtx) {
		u.load()
	}
	if u.createBtn.Clicked(gtx) {
		a.overlay = newFormDialog(a, dl, "Create a user in "+u.dbName, "Roles: names of this database or {role, db} documents.",
			func(v []string) error {
				stmt, err := dl.AdminStatement("createUser", ref, strings.TrimSpace(v[0]), v[1], v[2])
				if err != nil {
					return err
				}
				a.reviewRun(u.connID, "Create user "+v[0], stmt, maskPassword(stmt, v[1]), reopen)
				return nil
			},
			plainField("User", "name", "", false), plainField("Password", "", "", true), codeField("Roles", `["readWrite"]`, `["readWrite"]`))
	}
	if u.dropBtn.Clicked(gtx) {
		if name, ok := u.selected(); ok {
			if stmt, err := dl.AdminStatement("dropUser", ref, name); err == nil {
				a.reviewRun(u.connID, "Drop user "+name, stmt, "", reopen)
			}
		}
	}
	for _, g := range []struct {
		btn *widget.Clickable
		op  string
	}{{&u.grantBtn, "grantRoles"}, {&u.revokeBtn, "revokeRoles"}} {
		if g.btn.Clicked(gtx) {
			name, ok := u.selected()
			if !ok {
				continue
			}
			op := g.op
			a.overlay = newFormDialog(a, dl, map[string]string{"grantRoles": "Grant roles to ", "revokeRoles": "Revoke roles from "}[op]+name, "",
				func(v []string) error {
					stmt, err := dl.AdminStatement(op, ref, name, v[0])
					if err != nil {
						return err
					}
					a.reviewRun(u.connID, op+" "+name, stmt, "", reopen)
					return nil
				}, codeField("Roles", `["read"]`, `["read"]`))
		}
	}
	return u.d.layout(gtx, th, 760, icPerson, "Users of "+u.dbName, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				if u.err != nil {
					return layout.Inset{Bottom: 8}.Layout(gtx, func(gtx C) D { return lbl(gtx, th, fontSans, 12.5, pal.Err, db.AsError(u.err).Error()) })
				}
				return D{}
			}),
			layout.Rigid(func(gtx C) D {
				gtx.Constraints.Min.Y, gtx.Constraints.Max.Y = gtx.Dp(260), gtx.Dp(260)
				return rborder(gtx, pal.Border, pal.Bg, 6, func(gtx C) D { return inset(1).Layout(gtx, u.grid.Layout) })
			}),
		)
	}, footerButtons(func(gtx C) D {
		ro := a.connReadOnly(u.connID)
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &u.refreshBtn, icRefresh, "", pal.Text, colorNRGBA{}) }),
			layout.Rigid(func(gtx C) D {
				if ro {
					return D{}
				}
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &u.createBtn, icAdd, "Create…", pal.Text, colorNRGBA{}) }),
					layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &u.grantBtn, nil, "Grant…", pal.Text, colorNRGBA{}) }),
					layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &u.revokeBtn, nil, "Revoke…", pal.Text, colorNRGBA{}) }),
					layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &u.dropBtn, icDelete, "Drop", pal.Text, colorNRGBA{}) }),
				)
			}),
		)
	}, func(gtx C) D { return pillButton(gtx, th, &u.closeBtn, nil, "Close", pal.Text, pal.Hover) }))
}

// ---- SQL translation ----

// TranslateDialog translates SQL to the connection's language as you type.
type TranslateDialog struct {
	app    *App
	connID string
	sql    *CodeEditor
	out    *CodeEditor
	dl     db.DocumentLanguage
	shown  string // SQL of the translation shown
	err    string
	done   bool
	d      dialog

	insertBtn, closeBtn widget.Clickable
}

func (a *App) openTranslate(connID, sql string) {
	dl, ok := a.connState(connID).docLanguage()
	if !ok {
		a.flash("SQL translation is for MongoDB connections", true)
		return
	}
	if strings.TrimSpace(sql) == "" {
		sql = "SELECT name, COUNT(*) AS n\nFROM customers\nWHERE status = 'A'\nGROUP BY name\nORDER BY n DESC\nLIMIT 10"
	}
	t := &TranslateDialog{app: a, connID: connID, dl: dl}
	t.sql = newCodeEditor(a, sql)
	t.sql.setLanguage(db.GenericSQL)
	t.out = newCodeEditor(a, "")
	t.out.ReadOnly = true
	t.out.setLanguage(dl)
	t.shown = "\x00"
	a.overlay = t
}

func (t *TranslateDialog) Done() bool { return t.done }

func (t *TranslateDialog) Layout(gtx C) D {
	a, th := t.app, t.app.th
	if src := t.sql.Text(); src != t.shown {
		t.shown = src
		q, err := t.dl.TranslateSQL(src)
		t.err = ""
		if err != nil {
			t.err = err.Error()
		} else {
			t.out.SetText(q)
		}
	}
	if t.closeBtn.Clicked(gtx) || t.d.close.Clicked(gtx) {
		t.done = true
	}
	if t.insertBtn.Clicked(gtx) && t.err == "" {
		t.done = true
		a.insertInConsole(t.connID, t.out.Text(), "")
	}
	pane := func(e *CodeEditor) layout.Widget {
		return func(gtx C) D {
			gtx.Constraints.Min.Y, gtx.Constraints.Max.Y = gtx.Dp(260), gtx.Dp(260)
			return rborder(gtx, pal.Border, pal.CodeBg, 6, func(gtx C) D { return inset(1).Layout(gtx, e.Layout) })
		}
	}
	return t.d.layout(gtx, th, 1000, icCommand, "Translate SQL to MongoDB", func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return lbl(gtx, th, fontSans, 12, pal.TextDim, "SELECT with WHERE, GROUP BY, HAVING, ORDER BY, LIMIT/OFFSET, DISTINCT; INSERT; UPDATE; DELETE — on one collection.")
			}),
			layout.Rigid(layout.Spacer{Height: 8}.Layout),
			layout.Rigid(func(gtx C) D {
				return layout.Flex{}.Layout(gtx,
					layout.Flexed(1, pane(t.sql)),
					layout.Rigid(layout.Spacer{Width: 10}.Layout),
					layout.Flexed(1, pane(t.out)),
				)
			}),
			layout.Rigid(func(gtx C) D {
				if t.err == "" {
					return D{}
				}
				return layout.Inset{Top: 8}.Layout(gtx, func(gtx C) D { return lbl(gtx, th, fontMono, 12, pal.Err, t.err) })
			}),
		)
	}, footerButtons(nil,
		func(gtx C) D { return pillButton(gtx, th, &t.closeBtn, nil, "Close", pal.Text, pal.Hover) },
		func(gtx C) D { return button(gtx, th, &t.insertBtn, "Insert in console", pal.Accent) },
	))
}
