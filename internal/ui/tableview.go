package ui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"sort"
	"strconv"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/prefs"
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

// dropApplied removes the first n changes (in the order of changes())
// from the pending ones.
func (t *tableData) dropApplied(n int) {
	// both lists first: changes() skips the edits of deleted rows
	dels := make([]int, 0, len(t.deleted))
	for i := range t.deleted {
		dels = append(dels, i)
	}
	sort.Ints(dels)
	var upd []int
	for i, e := range t.edits {
		if len(e) > 0 && !t.deleted[i] {
			upd = append(upd, i)
		}
	}
	sort.Ints(upd)
	for _, i := range dels {
		if n == 0 {
			return
		}
		delete(t.deleted, i)
		delete(t.edits, i)
		n--
	}
	for _, i := range upd {
		if n == 0 {
			return
		}
		delete(t.edits, i)
		n--
	}
	k := min(n, len(t.inserted))
	t.inserted, t.insSet = t.inserted[k:], t.insSet[k:]
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

	sess                  db.Session
	sessGen               int
	saving                bool // a save runs: edits are blocked until it ends
	opening               bool
	sessWait              []sessWaiter
	disposed              bool
	data                  *tableData
	grid                  *DataGrid
	value                 *valuePanel
	where                 *CodeEditor // filter (SQL condition, filter document…)
	proj                  *CodeEditor // projection document (document languages)
	sortEd                *CodeEditor // sort document (document languages)
	limit                 widget.Editor
	langSet               db.Language // language the query editors follow
	mode                  int         // viewTable | viewTree | viewJSON (document results)
	modeBtns              [3]widget.Clickable
	docs                  *docView
	insDocBtn, editDocBtn widget.Clickable
	order                 []db.SortKey // sort of the browse query
	sortCol               int
	sortDsc               bool
	query                 string
	running               bool
	loadErr               error
	count                 string
	gen                   int

	ddl      *CodeEditor
	ddlState int // 0 none, 1 loading, 2 loaded
	ddlGen   int
	ddlErr   error

	structGrids [6]*DataGrid
	structTab   int

	viewBtns                                                                                  [4]widget.Clickable
	tools                                                                                     tableTools // document databases
	refreshBtn, addBtn, delBtn, saveBtn, revertBtn, countBtn, exportBtn, valueBtn, clearWhere widget.Clickable
	consoleBtn, copyDDLBtn, refreshDDL                                                        widget.Clickable
	structBtns                                                                                [6]widget.Clickable
}

func newTableTab(a *App, st prefs.TabState) *TableTab {
	t := &TableTab{a: a, connID: st.ConnID, ref: st.Ref, view: "data", sortCol: -1}
	if !t.ref.Type.Relation() {
		t.view = "ddl"
	}
	fields := t.fieldNames
	for _, ed := range []**CodeEditor{&t.where, &t.proj, &t.sortEd} {
		e := newCodeEditor(a, "")
		e.SingleLine = true
		e.OnSubmit = func() { t.refresh(nil) }
		e.Completer = func(e *CodeEditor) *compRequest { return queryItems(e.lang, fields) }
		*ed = e
	}
	t.proj.Hint, t.sortEd.Hint = "{name: 1, _id: 0}", "{created: -1}"
	t.limit.SingleLine, t.limit.Submit = true, true
	t.docs = newDocView(a)
	t.docs.Menu = t.docMenu
	t.docs.OnActivate = t.editDocument
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
	return t.where.Focused(gtx) || t.proj.Focused(gtx) || t.sortEd.Focused(gtx) || gtx.Focused(&t.limit) ||
		t.grid.Focused(gtx) || t.value.focused(gtx) || t.ddl.Focused(gtx)
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
	t.gen++ // results of loads in flight are dropped (and their cursors closed)
	if s := t.sess; s != nil {
		t.sess = nil
		go s.Close() // stops a running statement first
	}
	if t.data != nil {
		t.data.rs.detach()
	}
	if t.running && t.data == nil {
		// don't reconnect by itself from Layout: Reload does
		t.loadErr = errors.New("disconnected")
	}
	t.running, t.count = false, ""
}

func (t *TableTab) Dispose() {
	t.disposed = true
	t.Disconnected()
}

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
	t.a.connectOr(t.connID, func(cs *connState) {
		cs.loadTable(t.ref, func(info *db.TableInfo, err error) {
			t.loading = false
			t.info, t.infoErr = info, err
			if info != nil {
				t.fillStructure()
			}
		})
	}, func(err error) { t.loading, t.infoErr = false, err })
}

func (t *TableTab) editable(r, c int) bool {
	if t.data == nil || t.info == nil || t.saving || !t.ref.Type.Writable() || r < 0 || r >= t.data.Rows() || c < 0 || c >= len(t.data.rs.cols) || t.a.connReadOnly(t.connID) {
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
	case !t.ref.Type.Writable():
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

// withSession runs then with the tab's session (opened once, shared by
// concurrent callers); failed runs instead when that is impossible.
func (t *TableTab) withSession(then func(db.Session), failed func(error)) {
	a := t.a
	a.connectOr(t.connID, func(cs *connState) {
		if t.disposed {
			failed(errTabClosed)
			return
		}
		if t.sess != nil && t.sessGen == cs.gen {
			then(t.sess)
			return
		}
		t.sessWait = append(t.sessWait, sessWaiter{then, failed})
		if t.opening {
			return
		}
		t.opening = true
		conn, gen := cs.conn, cs.gen
		ro := a.connReadOnly(t.connID)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), db.ConnectTimeout)
			defer cancel()
			s, err := conn.Session(ctx)
			if err == nil && ro {
				if e := s.SetReadOnly(ctx, true); e != nil && !errors.Is(e, db.ErrNotSupported) {
					s.Close()
					s, err = nil, fmt.Errorf("read-only session: %w", e)
				}
			}
			a.post(func() {
				t.opening = false
				w := t.sessWait
				t.sessWait = nil
				if err == nil && (cs.gen != gen || t.disposed) {
					go s.Close()
					err = errTabClosed
				}
				if err != nil {
					for _, f := range w {
						f.fail(err)
					}
					return
				}
				t.sess, t.sessGen = s, gen
				for _, f := range w {
					f.ok(s)
				}
			})
		}()
	}, failed)
}

// refresh reloads the data after change (filter edits…), asking first
// when unsaved edits would be discarded.
func (t *TableTab) refresh(change func()) {
	do := func() {
		if change != nil {
			change()
		}
		if t.data != nil {
			t.data.revert()
		}
		t.loadErr = nil
		t.reload()
	}
	if t.data == nil || t.data.pending() == 0 {
		do()
		return
	}
	c := newConfirm(t.a, "Discard changes?", fmt.Sprintf("Reloading discards %d unsaved change(s).", t.data.pending()), true, func(bool) { do() })
	c.okLabel = "Discard and reload"
	t.a.overlay = c
}

// reload runs the browse query.
func (t *TableTab) reload() {
	if t.running {
		return
	}
	a := t.a
	// default order: the primary key (needs the table description), where
	// the language can sort browse results (not CQL)
	pkOrder := t.sortCol < 0 && t.ref.Type.Writable() && a.prefs.OrderByPrimaryKey() && t.cs().language().SortUnsupported() == ""
	if pkOrder && t.info == nil && t.infoErr == nil {
		t.ensureInfo() // Layout calls reload again once it is loaded
		return
	}
	t.order = nil
	bq := db.BrowseQuery{Filter: t.where.Text()}
	dl, isDoc := t.cs().language().(db.DocumentLanguage)
	var typedSort []db.SortKey
	if isDoc {
		bq.Projection = t.proj.Text()
		if n := strings.TrimSpace(t.limit.Text()); n != "" {
			v, err := strconv.ParseInt(n, 10, 64)
			if err != nil || v < 0 {
				t.loadErr = fmt.Errorf("limit: %q is not a number", n)
				return
			}
			bq.Limit = v
		}
		ks, err := dl.ParseSort(t.sortEd.Text())
		if err != nil {
			t.loadErr = err
			return
		}
		typedSort = ks
	}
	if t.sortCol >= 0 && t.data != nil && t.sortCol < len(t.data.rs.cols) {
		t.order = []db.SortKey{{Field: t.data.rs.cols[t.sortCol].Name, Desc: t.sortDsc}}
	} else if len(typedSort) > 0 {
		t.order = typedSort
	} else if pkOrder && t.info != nil {
		for _, k := range t.info.PrimaryKey {
			t.order = append(t.order, db.SortKey{Field: k})
		}
	}
	t.running, t.loadErr, t.count = true, nil, ""
	t.gen++
	gen := t.gen
	if err := t.checkWhere(strings.TrimSpace(t.where.Text())); err != nil {
		t.running, t.loadErr = false, err
		return
	}
	t.withSession(func(s db.Session) {
		bq.Sort = t.order
		q, err := t.cs().language().Browse(t.ref, bq)
		if err != nil {
			t.running, t.loadErr = false, err
			return
		}
		t.query = q
		fetch := a.prefs.Fetch()
		go func() {
			start := time.Now()
			rs := &resultSet{sql: q, sortCol: -1, affected: -1, started: start, isQuery: true}
			res, err := s.Execute(context.Background(), q)
			if err == nil {
				rs.cols = res.Columns()
				rs.rows, rs.more, err = res.Fetch(context.Background(), fetch)
				rs.docs = docsOf(res, len(rs.rows))
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
				t.docs.reset()
				t.grid.SetModel(t.data)
				t.grid.SortCol, t.grid.SortDesc = t.sortCol, t.sortDsc
				if t.sortCol < 0 && len(t.order) > 0 {
					t.grid.SortCol = t.pkSortCol() // the default key order shows on its column
				}
			})
		}()
	}, func(err error) {
		if gen == t.gen {
			t.running, t.loadErr = false, err
		}
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
	if why := t.cs().language().SortUnsupported(); why != "" {
		t.a.flash(why, true)
		return
	}
	switch {
	case t.sortCol < 0 && c == t.pkSortCol():
		// already ascending by this key column (default order): descending
		t.sortCol, t.sortDsc = c, true
	case t.sortCol != c:
		t.sortCol, t.sortDsc = c, false
	case !t.sortDsc:
		t.sortDsc = true
	default:
		t.sortCol = -1
	}
	t.reload() // builds the ORDER BY
}

// pkSortCol is the result column of a single-column primary key used as
// the default order (-1 otherwise).
func (t *TableTab) pkSortCol() int {
	if t.info == nil || len(t.info.PrimaryKey) != 1 || t.data == nil || !t.a.prefs.OrderByPrimaryKey() || t.cs().language().SortUnsupported() != "" {
		return -1
	}
	for i, c := range t.data.rs.cols {
		if c.Name == t.info.PrimaryKey[0] {
			return i
		}
	}
	return -1
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
	if t.data == nil || t.saving || !t.cs().caps().Insert || t.readOnlyReason() == "views are read-only" || t.a.connReadOnly(t.connID) {
		t.a.flash("Rows can't be added here", true)
		return
	}
	t.data.inserted = append(t.data.inserted, make(db.Row, len(t.data.rs.cols)))
	t.data.insSet = append(t.data.insSet, map[int]bool{})
	r := t.data.Rows() - 1
	t.grid.SelectCell(r, 0)
}

func (t *TableTab) deleteRows(rows []int) {
	if t.data == nil || t.saving || t.readOnlyReason() != "" || !t.cs().caps().Delete {
		t.a.flash("Rows can't be deleted: "+t.readOnlyReason(), true)
		return
	}
	// inserted rows are simply dropped (from the last one)
	sort.Sort(sort.Reverse(sort.IntSlice(rows)))
	n := t.data.Rows()
	for k, r := range rows {
		if r < 0 || r >= n || k > 0 && rows[k-1] == r {
			continue
		}
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
	lang := t.cs().language()
	var b strings.Builder
	for _, ch := range changes {
		b.WriteString(lang.Terminate(lang.RenderChange(t.ref, ch)) + "\n")
	}
	warn := t.cs().caps().EditNeedsWarning
	a.overlay = newSQLReview(a, t.connID, "Save changes to "+t.ref.Name, b.String(), warn, len(changes), func() { t.apply(changes) })
}

func (t *TableTab) apply(changes []db.Change) {
	a := t.a
	if t.saving {
		return
	}
	a.releaseCursors(t.connID)
	t.saving = true
	t.withSession(func(s db.Session) {
		cs := t.cs()
		conn, ref := cs.conn, t.ref
		desc := fmt.Sprintf("Saving %d change(s) to %s", len(changes), ref.Name)
		a.flash(desc+"…", false)
		go func() {
			applied, err := db.Apply(context.Background(), s, conn, ref, changes)
			a.post(func() {
				t.saving = false
				delete(cs.tables, ref)
				if err != nil {
					if len(applied) > 0 && t.data != nil {
						// no transactions: what was applied must not be
						// applied again
						t.data.dropApplied(len(applied))
						a.flash(fmt.Sprintf("%s: %d of %d change(s) were applied before the error (reload to see them): %v", desc, len(applied), len(changes), err), true)
						return
					}
					a.flash(fmt.Sprintf("%s failed: %v", desc, err), true)
					return
				}
				a.flash(desc+" — done", false)
				if t.data != nil {
					t.data.revert()
				}
				t.running = false
				t.reload()
			})
		}()
	}, func(err error) {
		t.saving = false
		a.flash("Save: "+err.Error(), true)
	})
}

func (t *TableTab) countRows() {
	a := t.a
	w := strings.TrimSpace(t.where.Text())
	if err := t.checkWhere(w); err != nil {
		a.flash(err.Error(), true)
		return
	}
	t.count = "counting…"
	gen := t.gen
	// on its own short-lived session: running it on the tab session would
	// close the open cursor of the browsed rows
	a.connectOr(t.connID, func(cs *connState) {
		conn := cs.conn
		q, err := cs.language().Count(t.ref, w)
		if err != nil {
			t.count = ""
			a.flash("Count: "+err.Error(), true)
			return
		}
		ro := a.connReadOnly(t.connID)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			var n string
			s, err := conn.Session(ctx)
			if err == nil {
				defer s.Close()
				if ro {
					if e := s.SetReadOnly(ctx, true); e != nil && !errors.Is(e, db.ErrNotSupported) {
						err = e
					}
				}
			}
			if err == nil {
				var res db.Result
				res, err = s.Execute(ctx, q)
				if err == nil {
					rows, _, ferr := res.Fetch(ctx, 1)
					res.Close()
					err = ferr
					if len(rows) == 1 && len(rows[0]) == 1 {
						n = db.FormatValue(rows[0][0], db.KindInt)
					}
				}
			}
			a.post(func() {
				if gen != t.gen {
					t.count = ""
					return
				}
				if err != nil {
					t.count = ""
					a.flash("Count: "+err.Error(), true)
					return
				}
				t.count = n
			})
		}()
	}, func(error) { t.count = "" })
}

// checkWhere validates the filter: one condition, nothing that changes
// data (it is pasted into the browse query, so "1=1; DELETE …" must not
// pass).
func (t *TableTab) checkWhere(w string) error {
	return t.cs().language().ValidateFilter(w)
}

// loadDDL fetches the object's DDL.
func (t *TableTab) loadDDL() {
	if t.ddlState != 0 {
		return
	}
	t.ddlState = 1
	t.ddlGen++
	gen := t.ddlGen
	a := t.a
	a.connectOr(t.connID, func(cs *connState) {
		conn, ref := cs.conn, t.ref
		t.ddl.setLanguage(conn.Language())
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), db.MetaTimeout)
			defer cancel()
			s, err := conn.Catalog().DDL(ctx, ref)
			a.post(func() {
				if gen != t.ddlGen {
					return // reloaded meanwhile
				}
				t.ddlState, t.ddlErr = 2, err
				if err == nil {
					t.ddl.SetText(s)
					t.ddl.SetCaretOffset(0)
				}
			})
		}()
	}, func(err error) {
		if gen == t.ddlGen {
			t.ddlState, t.ddlErr = 2, err
		}
	})
}

func (t *TableTab) Layout(gtx C) D {
	a, th := t.a, t.a.th
	views, labels := []string{"data", "structure", "ddl"}, []string{"Data", "Structure", "DDL"}
	if t.isDocCollection() {
		views, labels = append(views, "schema"), append(labels, "Schema")
	}
	for i, v := range views {
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
							sel := map[string]int{"data": 0, "structure": 1, "ddl": 2, "schema": 3}[t.view]
							return segmented(gtx, th, t.viewBtns[:len(views)], labels, sel)
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
			case "schema":
				return t.layoutSchema(gtx)
			case "ddl":
				return t.layoutDDL(gtx)
			}
			return t.layoutData(gtx)
		}),
	)
}

func (t *TableTab) layoutData(gtx C) D {
	a, th := t.a, t.a.th
	lang := t.cs().language()
	if lang != t.langSet {
		// highlighting and completion follow the connection's language
		t.langSet = lang
		for _, e := range []*CodeEditor{t.where, t.proj, t.sortEd} {
			e.setLanguage(lang)
		}
	}
	t.where.Hint = lang.FilterHint()
	_, isDoc := lang.(db.DocumentLanguage)
	for {
		ev, ok := t.limit.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			t.refresh(nil)
		}
	}
	if t.insDocBtn.Clicked(gtx) {
		t.insertDocument()
	}
	if t.editDocBtn.Clicked(gtx) {
		if r, ok := t.currentRow(); ok {
			t.editDocument(r)
		} else {
			a.flash("Select a document first", false)
		}
	}
	docs := isDoc && t.data != nil && t.data.rs.hasDocs()
	if t.refreshBtn.Clicked(gtx) {
		t.refresh(nil)
	}
	if t.clearWhere.Clicked(gtx) {
		t.refresh(func() { t.where.SetText("") })
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
					layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontMono, 12, pal.Keyword, lang.FilterLabel()+" ") }),
					layout.Flexed(1, func(gtx C) D { return queryField(gtx, t.where) }),
					layout.Rigid(func(gtx C) D {
						if t.where.Text() == "" {
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
		layout.Rigid(func(gtx C) D {
			if !isDoc {
				return D{}
			}
			return layout.Inset{Left: 8, Right: 8, Bottom: 6}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontMono, 12, pal.Keyword, "Project ") }),
					layout.Flexed(1, func(gtx C) D { return queryField(gtx, t.proj) }),
					layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontMono, 12, pal.Keyword, "  Sort ") }),
					layout.Flexed(0.7, func(gtx C) D { return queryField(gtx, t.sortEd) }),
					layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontMono, 12, pal.Keyword, "  Limit ") }),
					layout.Rigid(func(gtx C) D {
						gtx.Constraints.Min.X, gtx.Constraints.Max.X = gtx.Dp(80), gtx.Dp(80)
						return textField(gtx, th, &t.limit, "none", true)
					}),
					layout.Rigid(func(gtx C) D { return layout.Inset{Left: 8, Right: 8}.Layout(gtx, vsep) }),
					layout.Rigid(func(gtx C) D {
						for i := range t.modeBtns {
							if t.modeBtns[i].Clicked(gtx) {
								t.mode = i
							}
						}
						return segmented(gtx, th, t.modeBtns[:], viewNames[:], t.mode)
					}),
					layout.Rigid(func(gtx C) D { return layout.Inset{Left: 8}.Layout(gtx, t.dataTools) }),
					layout.Rigid(func(gtx C) D {
						if t.readOnlyReason() != "" {
							return D{}
						}
						return layout.Inset{Left: 8}.Layout(gtx, func(gtx C) D {
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &t.insDocBtn, icAdd, "Insert…", pal.Text, colorNRGBA{}) }),
								layout.Rigid(func(gtx C) D {
									return pillButton(gtx, th, &t.editDocBtn, icEdit, "Edit document…", pal.Text, colorNRGBA{})
								}),
							)
						})
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
			case docs && t.mode != viewTable:
				return t.docs.Layout(gtx, t.data.rs, lang, t.mode)
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
	items := copyMenuItems(a, t.grid, t.data.rs.cols, t.data.Value, t.cs().language(), t.ref)
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
		cond := oneLine(t.cs().language().EqualsFilter(col.Name, v, col.Kind))
		t.refresh(func() { t.where.SetText(cond) })
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
			return layout.Inset{Left: 8, Right: 8, Top: 6, Bottom: 6}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return segmented(gtx, th, t.structBtns[:], labels, t.structTab) }),
					layout.Flexed(1, func(gtx C) D { return D{Size: image.Pt(gtx.Constraints.Min.X, 1)} }),
					layout.Rigid(t.structureTools),
				)
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
