package ui

import (
	"context"
	"fmt"
	"image"
	"sort"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/prefs"
)

// cellEdit is an edited value (Value nil = NULL).
type cellEdit struct{ value any }

// tableData overlays pending edits on the fetched rows.
type tableData struct {
	rs       *resultSet
	edits    map[int]map[int]cellEdit // base row → column → value
	deleted  map[int]bool
	inserted []db.Row
	insSet   []map[int]bool // columns given a value in inserted rows
}

func newTableData(rs *resultSet) *tableData {
	return &tableData{rs: rs, edits: map[int]map[int]cellEdit{}, deleted: map[int]bool{}}
}

func (t *tableData) Columns() []db.Column { return t.rs.cols }
func (t *tableData) Rows() int            { return len(t.rs.rows) + len(t.inserted) }

func (t *tableData) base(r int) (int, bool) {
	if r < len(t.rs.rows) {
		return t.rs.row(r), true
	}
	return r - len(t.rs.rows), false
}

func (t *tableData) Value(r, c int) any {
	i, isBase := t.base(r)
	if !isBase {
		if i < len(t.inserted) && c < len(t.inserted[i]) {
			return t.inserted[i][c]
		}
		return nil
	}
	if e, ok := t.edits[i][c]; ok {
		return e.value
	}
	if c < len(t.rs.rows[i]) {
		return t.rs.rows[i][c]
	}
	return nil
}

func (t *tableData) RowState(r int) rowState {
	i, isBase := t.base(r)
	switch {
	case !isBase:
		return rowInserted
	case t.deleted[i]:
		return rowDeleted
	case len(t.edits[i]) > 0:
		return rowModified
	}
	return rowNormal
}

func (t *tableData) CellChanged(r, c int) bool {
	i, isBase := t.base(r)
	if !isBase {
		return t.insSet[i][c]
	}
	_, ok := t.edits[i][c]
	return ok
}

func (t *tableData) pending() int {
	n := len(t.inserted) + len(t.deleted)
	for i, e := range t.edits {
		if len(e) > 0 && !t.deleted[i] {
			n++
		}
	}
	return n
}

func (t *tableData) set(r, c int, v any) {
	i, isBase := t.base(r)
	if !isBase {
		t.inserted[i][c] = v
		t.insSet[i][c] = true
		return
	}
	orig := t.rs.rows[i][c]
	if t.edits[i] == nil {
		t.edits[i] = map[int]cellEdit{}
	}
	if sameCell(orig, v, t.rs.cols[c].Kind) {
		delete(t.edits[i], c) // back to the original value
		return
	}
	t.edits[i][c] = cellEdit{v}
}

func sameCell(a, b any, k db.Kind) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return db.FormatValue(a, k) == db.FormatValue(b, k)
}

func (t *tableData) revert() {
	t.edits, t.deleted, t.inserted, t.insSet = map[int]map[int]cellEdit{}, map[int]bool{}, nil, nil
}

// changes builds the DML batch: deletes, updates, inserts.
func (t *tableData) changes(info *db.TableInfo) ([]db.Change, error) {
	cols := t.rs.cols
	idx := map[string]int{}
	for i, c := range cols {
		idx[c.Name] = i
	}
	key := func(row db.Row) ([]db.ColValue, error) {
		var out []db.ColValue
		for _, k := range info.PrimaryKey {
			i, ok := idx[k]
			if !ok {
				return nil, fmt.Errorf("primary key column %s is not in the result", k)
			}
			out = append(out, db.ColValue{Column: k, Kind: cols[i].Kind, Value: row[i]})
		}
		return out, nil
	}
	var out []db.Change
	rows := make([]int, 0, len(t.deleted))
	for i := range t.deleted {
		rows = append(rows, i)
	}
	sort.Ints(rows)
	for _, i := range rows {
		k, err := key(t.rs.rows[i])
		if err != nil {
			return nil, err
		}
		out = append(out, db.Change{Op: db.OpDelete, Key: k})
	}
	rows = rows[:0]
	for i, e := range t.edits {
		if len(e) > 0 && !t.deleted[i] {
			rows = append(rows, i)
		}
	}
	sort.Ints(rows)
	for _, i := range rows {
		k, err := key(t.rs.rows[i])
		if err != nil {
			return nil, err
		}
		var vals []db.ColValue
		cs := make([]int, 0, len(t.edits[i]))
		for c := range t.edits[i] {
			cs = append(cs, c)
		}
		sort.Ints(cs)
		for _, c := range cs {
			vals = append(vals, db.ColValue{Column: cols[c].Name, Kind: cols[c].Kind, Value: t.edits[i][c].value})
		}
		out = append(out, db.Change{Op: db.OpUpdate, Key: k, Values: vals})
	}
	for j, row := range t.inserted {
		var vals []db.ColValue
		for c := range cols {
			if t.insSet[j][c] {
				vals = append(vals, db.ColValue{Column: cols[c].Name, Kind: cols[c].Kind, Value: row[c]})
			}
		}
		out = append(out, db.Change{Op: db.OpInsert, Values: vals})
	}
	return out, nil
}

// sliceModel is a static grid model.
type sliceModel struct {
	cols []db.Column
	rows [][]any
}

func (m *sliceModel) Columns() []db.Column      { return m.cols }
func (m *sliceModel) Rows() int                 { return len(m.rows) }
func (m *sliceModel) Value(r, c int) any        { return m.rows[r][c] }
func (m *sliceModel) RowState(int) rowState     { return rowNormal }
func (m *sliceModel) CellChanged(int, int) bool { return false }

func textCols(names ...string) []db.Column {
	out := make([]db.Column, len(names))
	for i, n := range names {
		out[i] = db.Column{Name: n, DBType: "TEXT", Kind: db.KindText}
	}
	return out
}

// TableTab browses and edits a relation, or shows an object's DDL.
type TableTab struct {
	a      *App
	connID string
	ref    db.ObjectRef
	view   string // data | structure | ddl

	info    *db.TableInfo
	infoErr error
	loading bool

	sess    db.Session
	sessGen int
	data    *tableData
	grid    *DataGrid
	value   *valuePanel
	where   widget.Editor
	order   []string // ORDER BY terms
	sortCol int
	sortDsc bool
	query   string
	running bool
	loadErr error
	count   string
	gen     int

	ddl      *CodeEditor
	ddlState int // 0 none, 1 loading, 2 loaded
	ddlErr   error

	structGrids [6]*DataGrid
	structTab   int

	viewBtns                                                                                  [3]widget.Clickable
	refreshBtn, addBtn, delBtn, saveBtn, revertBtn, countBtn, exportBtn, valueBtn, clearWhere widget.Clickable
	consoleBtn, copyDDLBtn, refreshDDL                                                        widget.Clickable
	structBtns                                                                                [6]widget.Clickable
}

func newTableTab(a *App, st prefs.TabState) *TableTab {
	t := &TableTab{a: a, connID: st.ConnID, ref: st.Ref, view: "data", sortCol: -1}
	if !t.ref.Type.Relation() {
		t.view = "ddl"
	}
	t.where.SingleLine, t.where.Submit = true, true
	t.grid = newDataGrid(a)
	t.value = newValuePanel(a)
	t.grid.OnSort = t.sortBy
	t.grid.OnNearEnd = func() {
		if t.data != nil && t.data.rs.more && !t.data.rs.fetching {
			t.data.rs.fetchMore(a, a.prefs.Fetch(), false, nil)
		}
	}
	t.grid.Editable = t.editable
	t.grid.OnEdit = func(r, c int, s string) { t.setText(r, c, s, false) }
	t.grid.OnSetNull = func(r, c int) { t.setText(r, c, "", true) }
	t.grid.OnDeleteRows = t.deleteRows
	t.grid.OnContextMenu = t.gridMenu
	t.grid.OnActivate = func(r, c int) { t.value.open = true }
	t.ddl = newCodeEditor(a, "")
	t.ddl.ReadOnly = true
	for i := range t.structGrids {
		t.structGrids[i] = newDataGrid(a)
	}
	return t
}

func (t *TableTab) cs() *connState { return t.a.connState(t.connID) }

func (t *TableTab) Title() string      { return t.ref.Name }
func (t *TableTab) Icon() *widget.Icon { return objIcon(t.ref.Type) }
func (t *TableTab) ConnID() string     { return t.connID }
func (t *TableTab) Focused(gtx C) bool {
	return gtx.Focused(&t.where) || t.grid.Focused(gtx) || t.value.focused(gtx) || t.ddl.Focused(gtx)
}

func (t *TableTab) Dirty() string {
	if t.data != nil && t.data.pending() > 0 {
		return fmt.Sprintf("%d unsaved change(s) to %s.", t.data.pending(), t.ref.Name)
	}
	return ""
}

func (t *TableTab) StatusInfo() string {
	s := t.ref.String()
	if t.data != nil {
		s += fmt.Sprintf(" · %d row(s) loaded", len(t.data.rs.rows))
		if t.data.rs.more {
			s += "+"
		}
	}
	if t.count != "" {
		s += " · " + t.count + " total"
	}
	return s
}

func (t *TableTab) State() prefs.TabState {
	return prefs.TabState{Kind: "table", ConnID: t.connID, Ref: t.ref}
}

func (t *TableTab) Disconnected() {
	if s := t.sess; s != nil {
		t.sess = nil
		go s.Close()
	}
	if t.data != nil {
		t.data.rs.detach()
	}
	t.running = false
}

func (t *TableTab) Dispose() { t.Disconnected() }

func (t *TableTab) show(view string) {
	if view == "data" && !t.ref.Type.Relation() {
		view = "ddl"
	}
	t.view = view
}

// ensureInfo loads the table structure once.
func (t *TableTab) ensureInfo() {
	if t.info != nil || t.loading || !t.ref.Type.Relation() {
		return
	}
	t.loading = true
	t.a.connect(t.connID, func(cs *connState) {
		cs.loadTable(t.ref, func(info *db.TableInfo, err error) {
			t.loading = false
			t.info, t.infoErr = info, err
			if info != nil {
				t.fillStructure()
			}
		})
	})
}

func (t *TableTab) editable(r, c int) bool {
	if t.data == nil || t.info == nil || t.ref.Type != db.ObjTable || t.a.connReadOnly(t.connID) {
		return false
	}
	caps := t.cs().caps()
	if t.data.RowState(r) == rowInserted {
		return caps.Insert
	}
	if !caps.Update || len(t.info.PrimaryKey) == 0 || t.data.RowState(r) == rowDeleted {
		return false
	}
	return true
}

// readOnlyReason explains why the data can't be edited ("" = editable).
func (t *TableTab) readOnlyReason() string {
	caps := t.cs().caps()
	switch {
	case t.ref.Type != db.ObjTable:
		return "views are read-only"
	case t.a.connReadOnly(t.connID):
		return "read-only connection"
	case !caps.Update && !caps.Insert:
		return "this database doesn't support editing"
	case t.info != nil && len(t.info.PrimaryKey) == 0:
		return "no primary key: rows can't be identified"
	}
	return ""
}

func (t *TableTab) withSession(then func(db.Session)) {
	a := t.a
	a.connect(t.connID, func(cs *connState) {
		if t.sess != nil && t.sessGen == cs.gen {
			then(t.sess)
			return
		}
		conn, gen := cs.conn, cs.gen
		ro := a.connReadOnly(t.connID)
		go func() {
			s, err := conn.Session(context.Background())
			if err == nil && ro {
				_ = s.SetReadOnly(context.Background(), true)
			}
			a.post(func() {
				if err != nil {
					t.loadErr = err
					t.running = false
					return
				}
				if cs.gen != gen {
					s.Close()
					return
				}
				t.sess, t.sessGen = s, gen
				then(s)
			})
		}()
	})
}

// reload runs the browse query.
func (t *TableTab) reload() {
	if t.running {
		return
	}
	a := t.a
	t.running, t.loadErr, t.count = true, nil, ""
	t.gen++
	gen := t.gen
	t.withSession(func(s db.Session) {
		dia := t.cs().dialect()
		q := dia.SelectAll(t.ref, t.where.Text(), strings.Join(t.order, ", "))
		t.query = q
		fetch := a.prefs.Fetch()
		go func() {
			start := time.Now()
			rs := &resultSet{sql: q, sortCol: -1, affected: -1, started: start, isQuery: true}
			res, err := s.Execute(context.Background(), q)
			if err == nil {
				rs.cols = res.Columns()
				rs.rows, rs.more, err = res.Fetch(context.Background(), fetch)
				if rs.more {
					rs.res = res
				} else {
					res.Close()
				}
			}
			rs.elapsed, rs.err = time.Since(start), err
			a.post(func() {
				if gen != t.gen {
					rs.closeCursor()
					return
				}
				t.running = false
				if err != nil {
					t.loadErr = err
					return
				}
				if t.data != nil {
					t.data.rs.closeCursor()
				}
				t.data = newTableData(rs)
				t.grid.SetModel(t.data)
				t.grid.SortCol, t.grid.SortDesc = t.sortCol, t.sortDsc
			})
		}()
	})
}

func (t *TableTab) sortBy(c int) {
	if t.data == nil {
		return
	}
	if t.data.pending() > 0 {
		t.a.flash("Save or revert the pending changes before sorting", true)
		return
	}
	switch {
	case t.sortCol != c:
		t.sortCol, t.sortDsc = c, false
	case !t.sortDsc:
		t.sortDsc = true
	default:
		t.sortCol = -1
	}
	t.order = nil
	if t.sortCol >= 0 {
		dir := " ASC"
		if t.sortDsc {
			dir = " DESC"
		}
		t.order = []string{t.cs().dialect().Quote(t.data.rs.cols[t.sortCol].Name) + dir}
	}
	t.reload()
}

// setText applies a grid edit (parsed for the column kind).
func (t *TableTab) setText(r, c int, s string, null bool) {
	if t.data == nil || !t.editable(r, c) {
		return
	}
	if null {
		t.data.set(r, c, nil)
		return
	}
	col := t.data.rs.cols[c]
	kind := col.Kind
	if tc, ok := t.info.Column(col.Name); ok && tc.Kind != db.KindOther {
		kind = tc.Kind
	}
	v, err := db.ParseValue(s, kind)
	if err != nil {
		t.a.flash(col.Name+": "+err.Error(), true)
		return
	}
	t.data.set(r, c, v)
}

func (t *TableTab) addRow() {
	if t.data == nil || !t.cs().caps().Insert || t.readOnlyReason() == "views are read-only" || t.a.connReadOnly(t.connID) {
		t.a.flash("Rows can't be added here", true)
		return
	}
	t.data.inserted = append(t.data.inserted, make(db.Row, len(t.data.rs.cols)))
	t.data.insSet = append(t.data.insSet, map[int]bool{})
	r := t.data.Rows() - 1
	t.grid.SelectCell(r, 0)
}

func (t *TableTab) deleteRows(rows []int) {
	if t.data == nil || t.readOnlyReason() != "" || !t.cs().caps().Delete {
		t.a.flash("Rows can't be deleted: "+t.readOnlyReason(), true)
		return
	}
	// inserted rows are simply dropped (from the last one)
	sort.Sort(sort.Reverse(sort.IntSlice(rows)))
	for _, r := range rows {
		i, isBase := t.data.base(r)
		if !isBase {
			t.data.inserted = append(t.data.inserted[:i], t.data.inserted[i+1:]...)
			t.data.insSet = append(t.data.insSet[:i], t.data.insSet[i+1:]...)
			continue
		}
		t.data.deleted[i] = !t.data.deleted[i]
		if !t.data.deleted[i] {
			delete(t.data.deleted, i)
		}
	}
}

// review shows the generated SQL before applying it.
func (t *TableTab) review() {
	a := t.a
	if t.data == nil || t.data.pending() == 0 {
		return
	}
	if !a.allowMutation(t.connID, "Saving changes") {
		return
	}
	changes, err := t.data.changes(t.info)
	if err != nil {
		a.flash(err.Error(), true)
		return
	}
	dia := t.cs().dialect()
	var b strings.Builder
	for _, ch := range changes {
		q, _ := db.BuildChange(dia, t.ref, ch, true)
		b.WriteString(q + ";\n")
	}
	warn := t.cs().caps().EditNeedsWarning
	a.overlay = newSQLReview(a, t.connID, "Save changes to "+t.ref.Name, b.String(), warn, len(changes), func() { t.apply(changes) })
}

func (t *TableTab) apply(changes []db.Change) {
	a := t.a
	a.releaseCursors(t.connID)
	t.withSession(func(s db.Session) {
		cs := t.cs()
		caps, dia, ref := cs.caps(), cs.dialect(), t.ref
		a.do(fmt.Sprintf("Saving %d change(s) to %s", len(changes), ref.Name), func() error {
			_, err := db.ApplyChanges(context.Background(), s, caps, dia, ref, changes)
			return err
		}, func() {
			t.data.revert()
			delete(cs.tables, ref)
			t.reload()
		})
	})
}

func (t *TableTab) countRows() {
	a := t.a
	t.count = "counting…"
	t.withSession(func(s db.Session) {
		dia := t.cs().dialect()
		q := "SELECT COUNT(*) FROM " + dia.Qualified(t.ref)
		if w := strings.TrimSpace(t.where.Text()); w != "" {
			q += " WHERE " + w
		}
		go func() {
			res, err := s.Execute(context.Background(), q)
			var n string
			if err == nil {
				rows, _, ferr := res.Fetch(context.Background(), 1)
				res.Close()
				err = ferr
				if len(rows) == 1 && len(rows[0]) == 1 {
					n = db.FormatValue(rows[0][0], db.KindInt)
				}
			}
			a.post(func() {
				if err != nil {
					t.count = ""
					a.flash("Count: "+err.Error(), true)
					return
				}
				t.count = n
			})
		}()
	})
}

// loadDDL fetches the object's DDL.
func (t *TableTab) loadDDL() {
	if t.ddlState != 0 {
		return
	}
	t.ddlState = 1
	a := t.a
	a.connect(t.connID, func(cs *connState) {
		conn, ref := cs.conn, t.ref
		t.ddl.setDialect(conn.Dialect())
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), db.MetaTimeout)
			defer cancel()
			s, err := conn.Catalog().DDL(ctx, ref)
			a.post(func() {
				t.ddlState, t.ddlErr = 2, err
				if err == nil {
					t.ddl.SetText(s)
					t.ddl.SetCaretOffset(0)
				}
			})
		}()
	})
}

func (t *TableTab) Layout(gtx C) D {
	a, th := t.a, t.a.th
	for i, v := range []string{"data", "structure", "ddl"} {
		if t.viewBtns[i].Clicked(gtx) {
			t.show(v)
		}
	}
	if t.ref.Type.Relation() {
		t.ensureInfo()
	}
	switch t.view {
	case "data":
		if t.data == nil && !t.running && t.loadErr == nil {
			t.reload()
		}
	case "ddl":
		t.loadDDL()
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return bg(gtx, pal.Header, func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Inset{Left: 10, Right: 8, Top: 5, Bottom: 5}.Layout(gtx, func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							return iconText(gtx, th, objIcon(t.ref.Type), 13, pal.TextBright, t.ref.String())
						}),
						layout.Rigid(layout.Spacer{Width: 14}.Layout),
						layout.Rigid(func(gtx C) D {
							if !t.ref.Type.Relation() {
								return D{}
							}
							sel := map[string]int{"data": 0, "structure": 1, "ddl": 2}[t.view]
							return segmented(gtx, th, t.viewBtns[:], []string{"Data", "Structure", "DDL"}, sel)
						}),
						layout.Flexed(1, func(gtx C) D { return D{Size: image.Pt(gtx.Constraints.Min.X, 1)} }),
						layout.Rigid(func(gtx C) D { return a.connBadges(gtx, t.connID) }),
					)
				})
			})
		}),
		layout.Rigid(func(gtx C) D { return hline(gtx, pal.Border) }),
		layout.Flexed(1, func(gtx C) D {
			switch t.view {
			case "structure":
				return t.layoutStructure(gtx)
			case "ddl":
				return t.layoutDDL(gtx)
			}
			return t.layoutData(gtx)
		}),
	)
}

func (t *TableTab) layoutData(gtx C) D {
	a, th := t.a, t.a.th
	for {
		ev, ok := t.where.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			if t.data != nil && t.data.pending() > 0 {
				a.flash("Save or revert the pending changes before filtering", true)
			} else {
				t.reload()
			}
		}
	}
	if t.refreshBtn.Clicked(gtx) {
		if t.data != nil && t.data.pending() > 0 {
			c := newConfirm(a, "Discard changes?", fmt.Sprintf("Reloading discards %d unsaved change(s).", t.data.pending()), true, func(bool) {
				t.data.revert()
				t.reload()
			})
			c.okLabel = "Discard and reload"
			a.overlay = c
		} else {
			t.loadErr = nil
			t.reload()
		}
	}
	if t.clearWhere.Clicked(gtx) {
		t.where.SetText("")
		t.reload()
	}
	if t.addBtn.Clicked(gtx) {
		t.addRow()
	}
	if t.delBtn.Clicked(gtx) {
		if s, ok := t.grid.Selection(); ok {
			var rows []int
			for r := s.r0; r <= s.r1; r++ {
				rows = append(rows, r)
			}
			t.deleteRows(rows)
		}
	}
	if t.saveBtn.Clicked(gtx) {
		t.review()
	}
	if t.revertBtn.Clicked(gtx) && t.data != nil {
		t.data.revert()
	}
	if t.countBtn.Clicked(gtx) {
		t.countRows()
	}
	if t.exportBtn.Clicked(gtx) && t.data != nil {
		ref := t.ref
		a.overlay = newExportDialog(a, t.connID, t.data.rs, &ref)
	}
	if t.valueBtn.Clicked(gtx) {
		t.value.open = !t.value.open
	}
	pending := 0
	if t.data != nil {
		pending = t.data.pending()
	}
	ro := t.readOnlyReason()
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: 8, Right: 8, Top: 6, Bottom: 6}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontMono, 12, pal.Keyword, "WHERE ") }),
					layout.Flexed(1, func(gtx C) D {
						return textField(gtx, th, &t.where, "filter, e.g. id > 10 AND name LIKE 'a%'  (Enter to apply)", true)
					}),
					layout.Rigid(func(gtx C) D {
						if t.where.Len() == 0 {
							return D{}
						}
						return iconOnly(gtx, &t.clearWhere, icClear, 15)
					}),
					layout.Rigid(layout.Spacer{Width: 6}.Layout),
					layout.Rigid(func(gtx C) D { return iconOnly(gtx, &t.refreshBtn, icRefresh, 17) }),
					layout.Rigid(func(gtx C) D { return layout.Inset{Left: 6, Right: 6}.Layout(gtx, vsep) }),
					layout.Rigid(func(gtx C) D {
						if ro != "" {
							return lbl(gtx, th, fontSans, 11.5, pal.TextDim, "read-only: "+ro)
						}
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &t.addBtn, icRowAdd, "Row", pal.Text, colorNRGBA{}) }),
							layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &t.delBtn, icRowDel, "Delete", pal.Text, colorNRGBA{}) }),
						)
					}),
					layout.Rigid(func(gtx C) D {
						if pending == 0 {
							return D{}
						}
						return layout.Inset{Left: 6}.Layout(gtx, func(gtx C) D {
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &t.revertBtn, icUndo, "Revert", pal.Text, pal.Hover) }),
								layout.Rigid(layout.Spacer{Width: 4}.Layout),
								layout.Rigid(func(gtx C) D {
									return pillButton(gtx, th, &t.saveBtn, icSave, fmt.Sprintf("Save %d change(s)…", pending), pal.OnAccent, pal.Accent)
								}),
							)
						})
					}),
					layout.Rigid(func(gtx C) D { return layout.Inset{Left: 6, Right: 6}.Layout(gtx, vsep) }),
					layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &t.countBtn, nil, "Count", pal.TextDim, colorNRGBA{}) }),
					layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &t.exportBtn, icExport, "", pal.TextDim, colorNRGBA{}) }),
					layout.Rigid(func(gtx C) D {
						b := colorNRGBA{}
						if t.value.open {
							b = pal.Selected
						}
						return pillButton(gtx, th, &t.valueBtn, icDescribe, "", pal.TextDim, b)
					}),
				)
			})
		}),
		layout.Rigid(func(gtx C) D { return hline(gtx, pal.Border) }),
		layout.Flexed(1, func(gtx C) D {
			switch {
			case t.loadErr != nil:
				return inset(16).Layout(gtx, func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx C) D { return iconText(gtx, th, icError, 13, pal.Err, "Query failed") }),
						layout.Rigid(layout.Spacer{Height: 6}.Layout),
						layout.Rigid(func(gtx C) D {
							return a.selText(gtx, "tblerr/"+t.ref.String(), fontMono, 12, pal.TextBright, db.AsError(t.loadErr).Error(), 0)
						}),
					)
				})
			case t.data == nil:
				return layout.Center.Layout(gtx, func(gtx C) D { return lbl(gtx, th, fontSans, 12.5, pal.TextDim, "Loading…") })
			}
			return t.value.layoutWith(gtx, t.grid, func(r, c int) (any, db.Column, bool) {
				if r >= 0 && r < t.data.Rows() && c >= 0 && c < len(t.data.rs.cols) {
					return t.data.Value(r, c), t.data.rs.cols[c], true
				}
				return nil, db.Column{}, false
			}, t.setText)
		}),
		layout.Rigid(func(gtx C) D {
			if t.data == nil {
				return D{}
			}
			return bg(gtx, pal.Header, func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Inset{Left: 10, Right: 10, Top: 3, Bottom: 3}.Layout(gtx, func(gtx C) D {
					return lbl(gtx, th, fontMono, 11, pal.TextDim, t.query+"   ·   "+t.data.rs.summary())
				})
			})
		}),
	)
}

func (t *TableTab) gridMenu(at image.Point) {
	a := t.a
	if t.data == nil {
		return
	}
	sel, ok := t.grid.Selection()
	if !ok {
		return
	}
	items := copyMenuItems(a, t.grid, t.data.rs.cols, t.data.Value, t.cs().dialect(), t.ref)
	items = append(items, &menuItem{sep: true})
	if t.editable(sel.r1, sel.c1) {
		r, c := sel.r1, sel.c1
		items = append(items,
			&menuItem{icon: icEdit, label: "Edit cell", hint: "F2", action: func() { t.value.open = true }},
			&menuItem{icon: icClear, label: "Set NULL", hint: shortcutLabel("Shift N"), action: func() { t.setText(r, c, "", true) }},
		)
	}
	if t.readOnlyReason() == "" {
		items = append(items,
			&menuItem{icon: icRowAdd, label: "Add row", action: t.addRow},
			&menuItem{icon: icRowDel, label: "Delete selected rows", hint: "Del", danger: true, action: func() {
				var rows []int
				for r := sel.r0; r <= sel.r1; r++ {
					rows = append(rows, r)
				}
				t.deleteRows(rows)
			}},
		)
	}
	items = append(items, &menuItem{icon: icFilter, label: "Filter by this value", action: func() {
		col := t.data.rs.cols[sel.c1]
		v := t.data.Value(sel.r1, sel.c1)
		dia := t.cs().dialect()
		cond := dia.Quote(col.Name) + " IS NULL"
		if v != nil {
			cond = dia.Quote(col.Name) + " = " + dia.Literal(v, col.Kind)
		}
		t.where.SetText(cond)
		t.reload()
	}})
	a.overlay = &ContextMenu{app: a, at: at, items: items, sel: -1}
}

// fillStructure prepares the structure grids.
func (t *TableTab) fillStructure() {
	info := t.info
	var cols [][]any
	for _, c := range info.Columns {
		pk := ""
		if c.PrimaryKeyNo > 0 {
			pk = "PK " + itoa(c.PrimaryKeyNo)
		}
		null := "NOT NULL"
		if c.Nullable {
			null = ""
		}
		def := c.Default
		if c.Generated != "" {
			def = "generated: " + c.Generated
		}
		if c.AutoIncr && def == "" {
			def = "auto increment"
		}
		cols = append(cols, []any{c.Name, c.Type, null, def, pk, c.Comment})
	}
	t.structGrids[0].SetModel(&sliceModel{cols: textCols("Column", "Type", "Null", "Default", "Key", "Comment"), rows: cols})
	var idx [][]any
	for _, ix := range info.Indexes {
		kind := ""
		switch {
		case ix.Primary:
			kind = "PRIMARY"
		case ix.Unique:
			kind = "UNIQUE"
		}
		idx = append(idx, []any{ix.Name, strings.Join(ix.Columns, ", "), kind, ix.Method, ix.Where})
	}
	t.structGrids[1].SetModel(&sliceModel{cols: textCols("Index", "Columns", "Kind", "Method", "Where"), rows: idx})
	var fks [][]any
	for _, fk := range info.ForeignKeys {
		fks = append(fks, []any{fk.Name, strings.Join(fk.Columns, ", "), fk.RefTable.String(), strings.Join(fk.RefColumns, ", "), fk.OnUpdate, fk.OnDelete})
	}
	t.structGrids[2].SetModel(&sliceModel{cols: textCols("Foreign key", "Columns", "References", "Columns", "On update", "On delete"), rows: fks})
	var cons [][]any
	for _, c := range info.Constraints {
		cons = append(cons, []any{c.Name, c.Type, strings.Join(c.Columns, ", "), c.Definition})
	}
	t.structGrids[3].SetModel(&sliceModel{cols: textCols("Constraint", "Type", "Columns", "Definition"), rows: cons})
	var trg [][]any
	for _, tr := range info.Triggers {
		trg = append(trg, []any{tr.Name, tr.Timing, tr.Events, oneLineSQL(tr.Definition)})
	}
	t.structGrids[4].SetModel(&sliceModel{cols: textCols("Trigger", "Timing", "Events", "Definition"), rows: trg})
	var props [][]any
	if info.Comment != "" {
		props = append(props, []any{"Comment", info.Comment})
	}
	if info.RowEstimate >= 0 {
		props = append(props, []any{"Estimated rows", itoa(int(info.RowEstimate))})
	}
	for _, kv := range info.Extra {
		props = append(props, []any{kv[0], kv[1]})
	}
	t.structGrids[5].SetModel(&sliceModel{cols: textCols("Property", "Value"), rows: props})
}

func oneLineSQL(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func (t *TableTab) layoutStructure(gtx C) D {
	th := t.a.th
	if t.info == nil {
		return layout.Center.Layout(gtx, func(gtx C) D {
			if t.infoErr != nil {
				return lbl(gtx, th, fontSans, 12.5, pal.Err, t.infoErr.Error())
			}
			return lbl(gtx, th, fontSans, 12.5, pal.TextDim, "Loading structure…")
		})
	}
	labels := []string{
		fmt.Sprintf("Columns (%d)", len(t.info.Columns)),
		fmt.Sprintf("Indexes (%d)", len(t.info.Indexes)),
		fmt.Sprintf("Foreign keys (%d)", len(t.info.ForeignKeys)),
		fmt.Sprintf("Constraints (%d)", len(t.info.Constraints)),
		fmt.Sprintf("Triggers (%d)", len(t.info.Triggers)),
		"Properties",
	}
	for i := range t.structBtns {
		if t.structBtns[i].Clicked(gtx) {
			t.structTab = i
		}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: 8, Top: 6, Bottom: 6}.Layout(gtx, func(gtx C) D {
				return segmented(gtx, th, t.structBtns[:], labels, t.structTab)
			})
		}),
		layout.Rigid(func(gtx C) D { return hline(gtx, pal.Border) }),
		layout.Flexed(1, t.structGrids[t.structTab].Layout),
	)
}

func (t *TableTab) layoutDDL(gtx C) D {
	a, th := t.a, t.a.th
	if t.copyDDLBtn.Clicked(gtx) {
		a.copyLater(t.ddl.Text())
	}
	if t.consoleBtn.Clicked(gtx) {
		a.newConsole(t.connID, t.ref.Schema, t.ddl.Text())
	}
	if t.refreshDDL.Clicked(gtx) {
		t.ddlState = 0
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: 8, Right: 8, Top: 5, Bottom: 5}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &t.copyDDLBtn, icCopyText, "Copy", pal.Text, colorNRGBA{}) }),
					layout.Rigid(func(gtx C) D {
						return pillButton(gtx, th, &t.consoleBtn, icConsole, "Open in console", pal.Text, colorNRGBA{})
					}),
					layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &t.refreshDDL, icRefresh, "Reload", pal.Text, colorNRGBA{}) }),
				)
			})
		}),
		layout.Rigid(func(gtx C) D { return hline(gtx, pal.Border) }),
		layout.Flexed(1, func(gtx C) D {
			switch {
			case t.ddlState < 2:
				return layout.Center.Layout(gtx, func(gtx C) D { return lbl(gtx, th, fontSans, 12.5, pal.TextDim, "Loading DDL…") })
			case t.ddlErr != nil:
				return inset(16).Layout(gtx, func(gtx C) D {
					return a.selText(gtx, "ddlerr/"+t.ref.String(), fontSans, 12.5, pal.Err, t.ddlErr.Error(), 0)
				})
			}
			return t.ddl.Layout(gtx)
		}),
	)
}

// allowMutation applies the read-only policy of a connection.
func (a *App) allowMutation(connID, what string) bool {
	if a.connReadOnly(connID) {
		a.flash(what+" blocked: "+a.connState(connID).name()+" is read-only", true)
		return false
	}
	return true
}
