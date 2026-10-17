package ui

import (
	"context"
	"fmt"
	"image"
	"sort"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/prefs"
)

// Live views of document databases: change streams and server activity.

// maxEvents bounds the events kept by a change stream view.
const maxEvents = 10000

// WatchTab follows the change stream of a collection (or database).
type WatchTab struct {
	a      *App
	connID string
	ref    db.ObjectRef
	filter *CodeEditor
	cancel context.CancelFunc
	err    error
	paused bool
	rs     *resultSet
	grid   *DataGrid
	docs   *docView
	mode   int
	modeB  [3]widget.Clickable

	startBtn, clearBtn widget.Clickable
}

func (a *App) openWatch(connID string, ref db.ObjectRef) {
	cs := a.connState(connID)
	if cs.docTools() == nil {
		a.flash("Change streams are not available on this connection", true)
		return
	}
	t := &WatchTab{a: a, connID: connID, ref: ref, grid: newDataGrid(a), docs: newDocView(a)}
	t.filter = newCodeEditor(a, "")
	t.filter.SingleLine = true
	t.filter.Hint = `pipeline, e.g. [{$match: {operationType: "insert"}}]  (Enter to restart)`
	t.filter.setLanguage(cs.language())
	t.filter.OnSubmit = func() { t.stop(); t.start() }
	t.clear()
	a.addTab(t)
	t.start()
}

func (t *WatchTab) clear() {
	t.rs = &resultSet{sortCol: -1, affected: -1, isQuery: true, docs: []db.DocNode{},
		cols: textCols("time", "operation", "_id", "fields", "namespace")}
	t.grid.SetModel(t.rs)
	t.docs.reset()
}

func (t *WatchTab) start() {
	a := t.a
	tools := a.connState(t.connID).docTools()
	if tools == nil || t.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel, t.err = cancel, nil
	ref, pipe := t.ref, t.filter.Text()
	go func() {
		err := tools.Watch(ctx, ref, pipe, func(ev db.ChangeEvent) {
			at := time.Now()
			a.post(func() {
				if t.paused {
					return
				}
				row := db.Row{at.Format("15:04:05.000"), ev.Op, ev.Key, ev.Fields, ev.NS}
				t.rs.rows = append([]db.Row{row}, t.rs.rows...) // newest first
				t.rs.docs = append([]db.DocNode{ev.Doc}, t.rs.docs...)
				if len(t.rs.rows) > maxEvents {
					t.rs.rows, t.rs.docs = t.rs.rows[:maxEvents], t.rs.docs[:maxEvents]
				}
				t.rs.resort()
			})
		})
		a.post(func() {
			if t.cancel != nil {
				t.cancel()
				t.cancel = nil
			}
			t.err = err
		})
	}()
}

func (t *WatchTab) stop() {
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
}

func (t *WatchTab) Title() string {
	if t.ref.Name == "" {
		return t.ref.Schema + " · changes"
	}
	return t.ref.Name + " · changes"
}
func (t *WatchTab) Icon() *widget.Icon    { return icEye }
func (t *WatchTab) ConnID() string        { return t.connID }
func (t *WatchTab) Focused(gtx C) bool    { return t.filter.Focused(gtx) || t.grid.Focused(gtx) }
func (t *WatchTab) Dirty() string         { return "" }
func (t *WatchTab) State() prefs.TabState { return prefs.TabState{} }
func (t *WatchTab) Disconnected()         { t.stop() }
func (t *WatchTab) Dispose()              { t.stop() }
func (t *WatchTab) StatusInfo() string {
	return fmt.Sprintf("%s · %d change(s)", t.Title(), len(t.rs.rows))
}

func (t *WatchTab) Layout(gtx C) D {
	a, th := t.a, t.a.th
	if t.startBtn.Clicked(gtx) {
		if t.cancel != nil {
			t.stop()
		} else {
			t.start()
		}
	}
	if t.clearBtn.Clicked(gtx) {
		t.clear()
	}
	for i := range t.modeB {
		if t.modeB[i].Clicked(gtx) {
			t.mode = i
		}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return bg(gtx, pal.Header, func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Inset{Left: 10, Right: 8, Top: 5, Bottom: 5}.Layout(gtx, func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx C) D { return iconText(gtx, th, icEye, 13, pal.TextBright, "Changes of "+t.ref.String()) }),
						layout.Rigid(layout.Spacer{Width: 12}.Layout),
						layout.Flexed(1, func(gtx C) D { return queryField(gtx, t.filter) }),
						layout.Rigid(layout.Spacer{Width: 8}.Layout),
						layout.Rigid(func(gtx C) D {
							if t.cancel != nil {
								return pillButton(gtx, th, &t.startBtn, icStop, "Stop", pal.Text, pal.Hover)
							}
							return pillButton(gtx, th, &t.startBtn, icRun, "Watch", pal.OnAccent, pal.Accent)
						}),
						layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &t.clearBtn, icClear, "Clear", pal.Text, colorNRGBA{}) }),
						layout.Rigid(func(gtx C) D {
							return layout.Inset{Left: 8}.Layout(gtx, func(gtx C) D { return segmented(gtx, th, t.modeB[:], viewNames[:], t.mode) })
						}),
						layout.Rigid(func(gtx C) D {
							return layout.Inset{Left: 8}.Layout(gtx, func(gtx C) D { return a.connBadges(gtx, t.connID) })
						}),
					)
				})
			})
		}),
		layout.Rigid(func(gtx C) D { return hline(gtx, pal.Border) }),
		layout.Rigid(func(gtx C) D {
			s := "Watching: changes appear as they happen (newest first)."
			c := pal.TextDim
			switch {
			case t.err != nil:
				s, c = db.AsError(t.err).Error(), pal.Err
			case t.cancel == nil:
				s = "Stopped."
			}
			return layout.Inset{Left: 10, Top: 4, Bottom: 4}.Layout(gtx, func(gtx C) D { return lbl(gtx, th, fontSans, 11.5, c, s) })
		}),
		layout.Flexed(1, func(gtx C) D {
			if t.mode != viewTable {
				return t.docs.Layout(gtx, t.rs, a.connState(t.connID).language(), t.mode)
			}
			return t.grid.Layout(gtx)
		}),
	)
}

// ---- server activity ----

// MonitorTab shows server statistics and running operations, refreshed
// every two seconds.
type MonitorTab struct {
	a       *App
	connID  string
	stats   *db.ServerStats
	prev    *db.ServerStats
	prevAt  time.Time
	at      time.Time
	rates   map[string]float64
	ops     []db.Operation
	err     error
	loading bool
	paused  bool
	idle    bool
	grid    *DataGrid
	list    widget.List

	pauseBtn, idleBtn, killBtn widget.Clickable
}

func (a *App) openMonitor(connID string) {
	if a.connState(connID).docTools() == nil {
		a.flash("Server activity is not available on this connection", true)
		return
	}
	t := &MonitorTab{a: a, connID: connID, grid: newDataGrid(a), rates: map[string]float64{}}
	t.list.Axis = layout.Vertical
	a.addTab(t)
	t.refresh()
}

func (t *MonitorTab) Title() string         { return t.a.connState(t.connID).name() + " · activity" }
func (t *MonitorTab) Icon() *widget.Icon    { return icCluster }
func (t *MonitorTab) ConnID() string        { return t.connID }
func (t *MonitorTab) Focused(gtx C) bool    { return t.grid.Focused(gtx) }
func (t *MonitorTab) Dirty() string         { return "" }
func (t *MonitorTab) State() prefs.TabState { return prefs.TabState{} }
func (t *MonitorTab) Disconnected()         {}
func (t *MonitorTab) Dispose()              { t.paused = true }
func (t *MonitorTab) StatusInfo() string    { return fmt.Sprintf("%d operation(s)", len(t.ops)) }

func (t *MonitorTab) refresh() {
	a := t.a
	tools := a.connState(t.connID).docTools()
	if tools == nil || t.loading {
		return
	}
	t.loading = true
	idle := t.idle
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		st, err := tools.ServerStats(ctx)
		var ops []db.Operation
		if err == nil {
			ops, err = tools.Operations(ctx, idle)
		}
		now := time.Now()
		a.post(func() {
			t.loading, t.err = false, err
			if err != nil {
				return
			}
			t.prev, t.prevAt = t.stats, t.at
			t.stats, t.at = st, now
			if t.prev != nil {
				dt := now.Sub(t.prevAt).Seconds()
				for k, v := range st.Counters {
					if dt > 0 {
						t.rates[k] = (v - t.prev.Counters[k]) / dt
					}
				}
			}
			sort.Slice(ops, func(i, j int) bool { return ops[i].Seconds > ops[j].Seconds })
			t.ops = ops
			m := &sliceModel{cols: textCols("opid", "op", "namespace", "seconds", "client", "description", "command")}
			m.cols[3].Kind = db.KindInt
			for _, o := range ops {
				m.rows = append(m.rows, []any{o.ID, o.Op, o.NS, o.Seconds, o.Client, o.Desc, o.Command})
			}
			t.grid.SetModel(m)
		})
	}()
}

func (t *MonitorTab) kill() {
	a := t.a
	sel, ok := t.grid.Selection()
	if !ok || sel.r1 >= len(t.ops) {
		a.flash("Select an operation first", false)
		return
	}
	dl, ok := a.connState(t.connID).docLanguage()
	if !ok {
		return
	}
	op := t.ops[sel.r1]
	stmt, err := dl.AdminStatement("killOp", db.ObjectRef{}, op.ID)
	if err != nil {
		a.flash(err.Error(), true)
		return
	}
	a.reviewRun(t.connID, "Kill operation "+op.ID, stmt, "", t.refresh)
}

func fmtRate(v float64) string {
	switch {
	case v >= 1e6:
		return fmt.Sprintf("%.1fM/s", v/1e6)
	case v >= 1e3:
		return fmt.Sprintf("%.1fk/s", v/1e3)
	}
	return fmt.Sprintf("%.1f/s", v)
}

func (t *MonitorTab) tiles() [][2]string {
	st := t.stats
	if st == nil {
		return nil
	}
	up := time.Duration(st.Uptime) * time.Second
	out := [][2]string{{"Server", "MongoDB " + st.Version}, {"Role", st.Role}, {"Uptime", up.String()}}
	out = append(out, st.Gauges...)
	for _, k := range [][2]string{{"insert", "Inserts"}, {"query", "Queries"}, {"update", "Updates"}, {"delete", "Deletes"},
		{"getmore", "Get mores"}, {"command", "Commands"}} {
		if v, ok := t.rates["ops."+k[0]]; ok {
			out = append(out, [2]string{k[1], fmtRate(v)})
		}
	}
	if v, ok := t.rates["net.bytesIn"]; ok {
		out = append(out, [2]string{"Network in", db.HumanBytes(int64(v)) + "/s"})
	}
	if v, ok := t.rates["net.bytesOut"]; ok {
		out = append(out, [2]string{"Network out", db.HumanBytes(int64(v)) + "/s"})
	}
	return out
}

func (t *MonitorTab) Layout(gtx C) D {
	a, th := t.a, t.a.th
	if t.pauseBtn.Clicked(gtx) {
		t.paused = !t.paused
	}
	if t.idleBtn.Clicked(gtx) {
		t.idle = !t.idle
		t.refresh()
	}
	if t.killBtn.Clicked(gtx) {
		t.kill()
	}
	if !t.paused && !t.loading && gtx.Now.Sub(t.at) > 2*time.Second {
		t.refresh()
	}
	if !t.paused {
		gtx.Execute(opInvalidateIn(gtx, time.Second))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return bg(gtx, pal.Header, func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Inset{Left: 10, Right: 8, Top: 5, Bottom: 5}.Layout(gtx, func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							return iconText(gtx, th, icCluster, 13, pal.TextBright, "Server activity of "+a.connState(t.connID).name())
						}),
						layout.Rigid(layout.Spacer{Width: 12}.Layout),
						layout.Rigid(func(gtx C) D {
							label := "Pause"
							if t.paused {
								label = "Resume"
							}
							return pillButton(gtx, th, &t.pauseBtn, nil, label, pal.Text, pal.Hover)
						}),
						layout.Rigid(func(gtx C) D { return toggle(gtx, th, &t.idleBtn, "Idle connections", t.idle) }),
						layout.Rigid(func(gtx C) D {
							return pillButton(gtx, th, &t.killBtn, icStop, "Kill selected…", pal.Text, colorNRGBA{})
						}),
						layout.Flexed(1, func(gtx C) D { return D{Size: image.Pt(gtx.Constraints.Min.X, 1)} }),
						layout.Rigid(func(gtx C) D { return a.connBadges(gtx, t.connID) }),
					)
				})
			})
		}),
		layout.Rigid(func(gtx C) D { return hline(gtx, pal.Border) }),
		layout.Rigid(func(gtx C) D {
			if t.err != nil {
				return inset(12).Layout(gtx, func(gtx C) D { return lbl(gtx, th, fontSans, 12.5, pal.Err, db.AsError(t.err).Error()) })
			}
			return inset(8).Layout(gtx, func(gtx C) D { return t.layoutTiles(gtx, th) })
		}),
		layout.Rigid(func(gtx C) D { return hline(gtx, pal.Border) }),
		layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: 10, Top: 4, Bottom: 4}.Layout(gtx, func(gtx C) D {
				return lbl(gtx, th, fontSans, 12, pal.Text, fmt.Sprintf("Running operations (%d), longest first", len(t.ops)))
			})
		}),
		layout.Flexed(1, t.grid.Layout),
	)
}

// layoutTiles draws the statistics in rows of tiles.
func (t *MonitorTab) layoutTiles(gtx C, th *material.Theme) D {
	tiles := t.tiles()
	if len(tiles) == 0 {
		return lbl(gtx, th, fontSans, 12.5, pal.TextDim, "Loading…")
	}
	tileW := gtx.Dp(170)
	perRow := max(1, gtx.Constraints.Max.X/(tileW+gtx.Dp(8)))
	var rows []layout.FlexChild
	for i := 0; i < len(tiles); i += perRow {
		row := tiles[i:min(len(tiles), i+perRow)]
		rows = append(rows, layout.Rigid(func(gtx C) D {
			var ch []layout.FlexChild
			for _, tl := range row {
				tl := tl
				ch = append(ch, layout.Rigid(func(gtx C) D {
					return layout.Inset{Right: 8, Bottom: 8}.Layout(gtx, func(gtx C) D {
						gtx.Constraints.Min.X, gtx.Constraints.Max.X = tileW, tileW
						return rborder(gtx, pal.Border, pal.Panel, 8, func(gtx C) D {
							return inset(8).Layout(gtx, func(gtx C) D {
								return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
									layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontSans, 11, pal.TextDim, tl[0]) }),
									layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontSans, 15, pal.TextBright, tl[1]) }),
								)
							})
						})
					})
				}))
			}
			return layout.Flex{}.Layout(gtx, ch...)
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
}
