package ui

import (
	"context"
	"fmt"
	"image"
	"strconv"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/prefs"
)

// Collection statistics with the index advisor (table view "Stats"), and
// the slow operations of a database (profiler tab).

type statsView struct {
	stats   *db.CollectionStats
	usage   []db.IndexUsage
	err     error
	loading bool
	loaded  bool
	grid    *DataGrid

	refreshBtn, hideBtn, unhideBtn, dropBtn widget.Clickable
}

func (t *TableTab) loadStats() {
	a := t.a
	sv := &t.statsV
	tools := t.cs().docTools()
	if tools == nil || sv.loading {
		return
	}
	sv.loading, sv.loaded = true, true
	ref := t.ref
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		st, err := tools.CollectionStats(ctx, ref)
		var us []db.IndexUsage
		if err == nil {
			us, err = tools.IndexUsage(ctx, ref)
		}
		a.post(func() {
			sv.loading, sv.stats, sv.usage, sv.err = false, st, us, err
			m := &sliceModel{cols: textCols("index", "keys", "uses", "since", "size", "flags", "advice")}
			m.cols[2].Kind = db.KindInt
			for _, u := range us {
				var flags []string
				if u.Unique {
					flags = append(flags, "unique")
				}
				if u.Hidden {
					flags = append(flags, "hidden")
				}
				since := ""
				if !u.Since.IsZero() {
					since = u.Since.Local().Format("2006-01-02 15:04")
				}
				m.rows = append(m.rows, []any{u.Name, u.Keys, u.Ops, since, db.HumanBytes(u.Size), strings.Join(flags, ", "), u.Issue})
			}
			sv.grid.SetModel(m)
		})
	}()
}

// selectedIndex is the index selected in the usage grid.
func (t *TableTab) selectedIndex() (db.IndexUsage, bool) {
	sv := &t.statsV
	sel, ok := sv.grid.Selection()
	if !ok || sel.r1 >= len(sv.usage) {
		t.a.flash("Select an index first", false)
		return db.IndexUsage{}, false
	}
	u := sv.usage[sel.r1]
	if u.Name == "_id_" {
		t.a.flash("The _id index can't be hidden or dropped", true)
		return db.IndexUsage{}, false
	}
	return u, true
}

// indexAction reviews and runs a hide / unhide / drop statement.
func (t *TableTab) indexAction(op, title string) {
	dl, ok := t.cs().docLanguage()
	u, sel := t.selectedIndex()
	if !ok || !sel {
		return
	}
	stmt, err := dl.AdminStatement(op, t.ref, u.Name)
	if err != nil {
		t.a.flash(err.Error(), true)
		return
	}
	t.a.reviewRun(t.connID, title+" "+u.Name, stmt, "", func() {
		t.statsV.loaded = false
		t.afterSchemaChange()
	})
}

func (t *TableTab) layoutStats(gtx C) D {
	th := t.a.th
	sv := &t.statsV
	if sv.grid == nil {
		sv.grid = newDataGrid(t.a)
	}
	if !sv.loaded {
		t.loadStats()
	}
	if sv.refreshBtn.Clicked(gtx) {
		t.loadStats()
	}
	if sv.hideBtn.Clicked(gtx) {
		t.indexAction("hideIndex", "Hide index")
	}
	if sv.unhideBtn.Clicked(gtx) {
		t.indexAction("unhideIndex", "Unhide index")
	}
	if sv.dropBtn.Clicked(gtx) {
		t.indexAction("dropIndex", "Drop index")
	}
	var tiles [][2]string
	if st := sv.stats; st != nil {
		tiles = [][2]string{{"Documents", strconv.FormatInt(st.Documents, 10)}, {"Data size", db.HumanBytes(st.DataSize)},
			{"Storage size", db.HumanBytes(st.StorageSize)}, {"Index size", db.HumanBytes(st.IndexSize)},
			{"Total size", db.HumanBytes(st.TotalSize)}, {"Average document", db.HumanBytes(st.AvgDocSize)},
			{"Indexes", strconv.Itoa(st.Indexes)}}
		if st.Capped {
			tiles = append(tiles, [2]string{"Capped", "yes"})
		}
	}
	issues := 0
	for _, u := range sv.usage {
		if u.Issue != "" {
			issues++
		}
	}
	ro := t.readOnlyReason() != ""
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return inset(8).Layout(gtx, func(gtx C) D {
				switch {
				case sv.err != nil:
					return lbl(gtx, th, fontSans, 12.5, pal.Err, db.AsError(sv.err).Error())
				case sv.stats == nil:
					return lbl(gtx, th, fontSans, 12.5, pal.TextDim, "Loading statistics…")
				}
				return drawTiles(gtx, th, tiles)
			})
		}),
		layout.Rigid(func(gtx C) D { return hline(gtx, pal.Border) }),
		layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: 10, Right: 8, Top: 5, Bottom: 5}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx C) D {
						s := fmt.Sprintf("Index usage since the server started · %d index(es)", len(sv.usage))
						c := pal.Text
						if issues > 0 {
							s += fmt.Sprintf(" · %d look useless", issues)
							c = pal.Warn
						}
						return lbl(gtx, th, fontSans, 12, c, s)
					}),
					layout.Rigid(func(gtx C) D {
						return pillButton(gtx, th, &sv.refreshBtn, icRefresh, "Refresh", pal.Text, colorNRGBA{})
					}),
					layout.Rigid(func(gtx C) D {
						if ro {
							return D{}
						}
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &sv.hideBtn, icEyeOff, "Hide", pal.Text, colorNRGBA{}) }),
							layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &sv.unhideBtn, icEye, "Unhide", pal.Text, colorNRGBA{}) }),
							layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &sv.dropBtn, icDelete, "Drop…", pal.Text, colorNRGBA{}) }),
						)
					}),
				)
			})
		}),
		layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: 10, Bottom: 4}.Layout(gtx, func(gtx C) D {
				return lbl(gtx, th, fontSans, 11, pal.TextDim, "Hide an index first: the planner stops using it, unhiding it is instant (a dropped index must be rebuilt).")
			})
		}),
		layout.Flexed(1, sv.grid.Layout),
	)
}

// ---- slow queries ----

// ProfilerTab lists the operations recorded by the profiler of a
// database, with the index suggested for the ones scanning or sorting.
type ProfilerTab struct {
	a       *App
	connID  string
	dbName  string
	status  *db.ProfilerStatus
	queries []db.SlowQuery
	err     error
	loading bool
	grid    *DataGrid
	detail  *CodeEditor
	shown   int

	refreshBtn, levelBtn, createBtn, consoleBtn widget.Clickable
}

func (a *App) openProfiler(connID, dbName string) {
	if a.connState(connID).docTools() == nil {
		a.flash("The profiler is not available on this connection", true)
		return
	}
	t := &ProfilerTab{a: a, connID: connID, dbName: dbName, grid: newDataGrid(a), shown: -1}
	t.detail = newCodeEditor(a, "")
	t.detail.ReadOnly = true
	t.detail.setLanguage(a.connState(connID).language())
	a.addTab(t)
	t.refresh()
}

func (t *ProfilerTab) Title() string         { return t.dbName + " · slow queries" }
func (t *ProfilerTab) Icon() *widget.Icon    { return icHistory }
func (t *ProfilerTab) ConnID() string        { return t.connID }
func (t *ProfilerTab) Focused(gtx C) bool    { return t.grid.Focused(gtx) || t.detail.Focused(gtx) }
func (t *ProfilerTab) Dirty() string         { return "" }
func (t *ProfilerTab) State() prefs.TabState { return prefs.TabState{} }
func (t *ProfilerTab) Disconnected()         {}
func (t *ProfilerTab) Dispose()              {}
func (t *ProfilerTab) StatusInfo() string {
	return fmt.Sprintf("%d recorded operation(s)", len(t.queries))
}

func (t *ProfilerTab) refresh() {
	a := t.a
	tools := a.connState(t.connID).docTools()
	if tools == nil || t.loading {
		return
	}
	t.loading = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		st, err := tools.Profiler(ctx, t.dbName)
		var qs []db.SlowQuery
		if err == nil {
			qs, err = tools.SlowQueries(ctx, t.dbName, 500)
		}
		a.post(func() {
			t.loading, t.status, t.queries, t.err, t.shown = false, st, qs, err, -1
			m := &sliceModel{cols: textCols("time", "namespace", "op", "ms", "docs examined", "keys examined", "returned", "plan", "suggested index")}
			for _, i := range []int{3, 4, 5, 6} {
				m.cols[i].Kind = db.KindInt
			}
			for _, q := range qs {
				sug := ""
				if q.Suggestion != nil {
					sug = q.Suggestion.Keys
				}
				m.rows = append(m.rows, []any{q.At.Local().Format("01-02 15:04:05.000"), q.NS, q.Op, q.Millis, q.DocsExamined, q.KeysExamined,
					q.Returned, q.Plan, sug})
			}
			t.grid.SetModel(m)
		})
	}()
}

func (t *ProfilerTab) selected() (db.SlowQuery, bool) {
	sel, ok := t.grid.Selection()
	if !ok || sel.r1 >= len(t.queries) {
		return db.SlowQuery{}, false
	}
	return t.queries[sel.r1], true
}

// levelDialog changes the profiling level.
func (t *ProfilerTab) levelDialog() {
	a := t.a
	dl, ok := a.connState(t.connID).docLanguage()
	if !ok {
		return
	}
	level, slow := "1", "100"
	if t.status != nil {
		level, slow = strconv.Itoa(t.status.Level), strconv.FormatInt(t.status.SlowMs, 10)
	}
	a.overlay = newFormDialog(a, dl, "Profiling of "+t.dbName, "Level 0 records nothing, 1 the operations slower than the threshold, 2 every operation "+
		"(it slows the server down: use it briefly).",
		func(v []string) error {
			stmt, err := dl.AdminStatement("setProfiler", db.ObjectRef{Schema: t.dbName}, v[0], v[1])
			if err != nil {
				return err
			}
			a.reviewRun(t.connID, "Change the profiling level", stmt, "", t.refresh)
			return nil
		}, plainField("Level", "0, 1 or 2", level, false), plainField("Slow threshold (ms)", "100", slow, false))
}

func (t *ProfilerTab) Layout(gtx C) D {
	a, th := t.a, t.a.th
	if t.refreshBtn.Clicked(gtx) {
		t.refresh()
	}
	if t.levelBtn.Clicked(gtx) {
		t.levelDialog()
	}
	q, has := t.selected()
	if sel, ok := t.grid.Selection(); ok && sel.r1 != t.shown && has {
		t.shown = sel.r1
		t.detail.SetText(q.Command)
		t.detail.SetCaretOffset(0)
	}
	if t.createBtn.Clicked(gtx) && has && q.Suggestion != nil {
		a.reviewRun(t.connID, "Create index", q.Suggestion.Statement, "", nil)
	}
	if t.consoleBtn.Clicked(gtx) && has && q.Suggestion != nil {
		a.newConsole(t.connID, t.dbName, q.Suggestion.Statement+"\n")
	}
	status := "Reading the profiler…"
	switch {
	case t.err != nil:
		status = db.AsError(t.err).Error()
	case t.status != nil && t.status.Level == 0:
		status = fmt.Sprintf("Profiling is off (threshold %d ms): turn it on to record slow operations.", t.status.SlowMs)
	case t.status != nil:
		status = fmt.Sprintf("Profiling level %d, threshold %d ms · %d operation(s) recorded", t.status.Level, t.status.SlowMs, len(t.queries))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return bg(gtx, pal.Header, func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Inset{Left: 10, Right: 8, Top: 5, Bottom: 5}.Layout(gtx, func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx C) D { return iconText(gtx, th, icHistory, 13, pal.TextBright, "Slow queries of "+t.dbName) }),
						layout.Rigid(layout.Spacer{Width: 12}.Layout),
						layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &t.refreshBtn, icRefresh, "Refresh", pal.Text, pal.Hover) }),
						layout.Rigid(func(gtx C) D {
							if a.connReadOnly(t.connID) {
								return D{}
							}
							return pillButton(gtx, th, &t.levelBtn, icSettings, "Profiling…", pal.Text, colorNRGBA{})
						}),
						layout.Flexed(1, func(gtx C) D { return D{Size: image.Pt(gtx.Constraints.Min.X, 1)} }),
						layout.Rigid(func(gtx C) D { return a.connBadges(gtx, t.connID) }),
					)
				})
			})
		}),
		layout.Rigid(func(gtx C) D {
			c := pal.TextDim
			if t.err != nil {
				c = pal.Err
			}
			return layout.Inset{Left: 10, Top: 4, Bottom: 4}.Layout(gtx, func(gtx C) D { return lbl(gtx, th, fontSans, 12, c, status) })
		}),
		layout.Rigid(func(gtx C) D { return hline(gtx, pal.Border) }),
		layout.Flexed(0.62, t.grid.Layout),
		layout.Rigid(func(gtx C) D { return hline(gtx, pal.Border) }),
		layout.Rigid(func(gtx C) D {
			if !has || q.Suggestion == nil {
				return D{}
			}
			return inset(8).Layout(gtx, func(gtx C) D {
				return rborder(gtx, withAlpha(pal.Warn, 0x90), pal.Panel, 8, func(gtx C) D {
					return inset(10).Layout(gtx, func(gtx C) D {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Flexed(1, func(gtx C) D {
								return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
									layout.Rigid(func(gtx C) D { return iconText(gtx, th, icIdea, 12, pal.Warn, q.Suggestion.Reason) }),
									layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontMono, 12.5, pal.TextBright, q.Suggestion.Statement) }),
								)
							}),
							layout.Rigid(func(gtx C) D {
								return pillButton(gtx, th, &t.consoleBtn, icConsole, "In console", pal.Text, colorNRGBA{})
							}),
							layout.Rigid(func(gtx C) D {
								if a.connReadOnly(t.connID) {
									return D{}
								}
								return button(gtx, th, &t.createBtn, "Create this index…", pal.Accent)
							}),
						)
					})
				})
			})
		}),
		layout.Flexed(0.38, func(gtx C) D {
			if !has {
				return layout.Center.Layout(gtx, func(gtx C) D {
					return lbl(gtx, th, fontSans, 12, pal.TextDim, "Select an operation to see its command")
				})
			}
			return t.detail.Layout(gtx)
		}),
	)
}
