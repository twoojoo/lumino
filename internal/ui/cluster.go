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

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/prefs"
)

// Cluster views of document databases (replica set members and oplog,
// sharding) and the export of statements to driver code.

// ClusterTab shows the replica set or the sharded cluster of a connection,
// refreshed every five seconds.
type ClusterTab struct {
	a        *App
	connID   string
	topology string
	rs       *db.ReplicaSetStatus
	sh       *db.ShardingStatus
	err      error
	at       time.Time
	loading  bool
	paused   bool
	grids    [3]*DataGrid
	titles   [3]string

	pauseBtn, refreshBtn widget.Clickable
}

func (a *App) openCluster(connID string) {
	tools := a.connState(connID).docTools()
	if tools == nil {
		a.flash("The cluster view is not available on this connection", true)
		return
	}
	t := &ClusterTab{a: a, connID: connID, topology: tools.Topology()}
	for i := range t.grids {
		t.grids[i] = newDataGrid(a)
	}
	a.addTab(t)
	t.refresh()
}

func (t *ClusterTab) Title() string         { return t.a.connState(t.connID).name() + " · cluster" }
func (t *ClusterTab) Icon() *widget.Icon    { return icCluster }
func (t *ClusterTab) ConnID() string        { return t.connID }
func (t *ClusterTab) Dirty() string         { return "" }
func (t *ClusterTab) State() prefs.TabState { return prefs.TabState{} }
func (t *ClusterTab) Disconnected()         {}
func (t *ClusterTab) Dispose()              { t.paused = true }
func (t *ClusterTab) Focused(gtx C) bool {
	for _, g := range t.grids {
		if g.Focused(gtx) {
			return true
		}
	}
	return false
}

func (t *ClusterTab) StatusInfo() string {
	switch {
	case t.rs != nil:
		return fmt.Sprintf("%d member(s)", len(t.rs.Members))
	case t.sh != nil:
		return fmt.Sprintf("%d shard(s)", len(t.sh.Shards))
	}
	return ""
}

func (t *ClusterTab) refresh() {
	a := t.a
	tools := a.connState(t.connID).docTools()
	if tools == nil || t.loading || t.topology == "standalone" {
		return
	}
	t.loading = true
	topology := t.topology
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var rs *db.ReplicaSetStatus
		var sh *db.ShardingStatus
		var err error
		if topology == "sharded" {
			sh, err = tools.Sharding(ctx)
		} else {
			rs, err = tools.ReplicaSet(ctx)
		}
		a.post(func() {
			t.loading, t.err, t.at = false, err, time.Now()
			if err != nil {
				return
			}
			t.rs, t.sh = rs, sh
			if rs != nil {
				t.setReplicaSet(rs)
			} else {
				t.setSharding(sh)
			}
		})
	}()
}

// fmtTime formats a server time ("" when unknown).
func fmtTime(v time.Time) string {
	if v.IsZero() {
		return ""
	}
	return v.Local().Format("2006-01-02 15:04:05")
}

func (t *ClusterTab) setReplicaSet(rs *db.ReplicaSetStatus) {
	m := &sliceModel{cols: textCols("member", "state", "health", "uptime", "optime", "lag", "ping ms", "sync source", "message")}
	m.cols[6].Kind = db.KindInt
	for _, mb := range rs.Members {
		name := mb.Name
		if mb.Self {
			name += " (this server)"
		}
		health := "ok"
		if !mb.Healthy {
			health = "down"
		}
		lag := ""
		if mb.State == "SECONDARY" {
			lag = mb.Lag.String()
		}
		m.rows = append(m.rows, []any{name, mb.State, health, (time.Duration(mb.Uptime) * time.Second).String(),
			fmtTime(mb.Optime), lag, mb.PingMs, mb.SyncSource, mb.Message})
	}
	t.grids[0].SetModel(m)
	t.titles[0] = fmt.Sprintf("Members (%d)", len(rs.Members))
}

func (t *ClusterTab) setSharding(sh *db.ShardingStatus) {
	m := &sliceModel{cols: textCols("shard", "host", "state", "draining")}
	for _, s := range sh.Shards {
		draining := ""
		if s.Draining {
			draining = "yes"
		}
		state := fmt.Sprint(s.State)
		if s.State == 1 {
			state = "active"
		}
		m.rows = append(m.rows, []any{s.ID, s.Host, state, draining})
	}
	t.grids[0].SetModel(m)
	t.titles[0] = fmt.Sprintf("Shards (%d)", len(sh.Shards))

	m = &sliceModel{cols: textCols("collection", "shard key", "unique", "chunks", "chunks per shard", "jumbo", "balancing")}
	m.cols[3].Kind, m.cols[5].Kind = db.KindInt, db.KindInt
	for _, c := range sh.Collections {
		var ids []string
		total := 0
		for id, n := range c.Chunks {
			ids = append(ids, id)
			total += n
		}
		sort.Strings(ids)
		var per []string
		for _, id := range ids {
			per = append(per, fmt.Sprintf("%s: %d", id, c.Chunks[id]))
		}
		unique, balance := "", "on"
		if c.Unique {
			unique = "yes"
		}
		if !c.Balance {
			balance = "off"
		}
		m.rows = append(m.rows, []any{c.NS, c.Key, unique, total, strings.Join(per, ", "), c.Jumbo, balance})
	}
	t.grids[1].SetModel(m)
	t.titles[1] = fmt.Sprintf("Sharded collections (%d)", len(sh.Collections))

	m = &sliceModel{cols: textCols("database", "primary shard")}
	for _, d := range sh.Databases {
		m.rows = append(m.rows, []any{d.Name, d.Primary})
	}
	t.grids[2].SetModel(m)
	t.titles[2] = fmt.Sprintf("Databases (%d)", len(sh.Databases))
}

func (t *ClusterTab) tiles() [][2]string {
	if rs := t.rs; rs != nil {
		out := [][2]string{{"Replica set", rs.Name}}
		var primary string
		var maxLag time.Duration
		healthy := 0
		for _, mb := range rs.Members {
			if mb.State == "PRIMARY" {
				primary = mb.Name
			}
			if mb.State == "SECONDARY" && mb.Lag > maxLag {
				maxLag = mb.Lag
			}
			if mb.Healthy {
				healthy++
			}
		}
		if primary == "" {
			primary = "none"
		}
		out = append(out, [2]string{"Primary", primary}, [2]string{"Healthy members", fmt.Sprintf("%d / %d", healthy, len(rs.Members))},
			[2]string{"Max replication lag", maxLag.String()})
		if !rs.OplogFirst.IsZero() {
			out = append(out, [2]string{"Oplog window", rs.OplogLast.Sub(rs.OplogFirst).Round(time.Second).String()},
				[2]string{"Oplog first entry", fmtTime(rs.OplogFirst)}, [2]string{"Oplog last entry", fmtTime(rs.OplogLast)})
		}
		if rs.OplogMax > 0 {
			out = append(out, [2]string{"Oplog size", db.HumanBytes(rs.OplogSize) + " / " + db.HumanBytes(rs.OplogMax)})
		}
		return out
	}
	if sh := t.sh; sh != nil {
		chunks := 0
		for _, c := range sh.Collections {
			for _, n := range c.Chunks {
				chunks += n
			}
		}
		routers := fmt.Sprint(len(sh.Routers))
		if len(sh.Routers) == 1 {
			routers = sh.Routers[0]
		}
		return [][2]string{{"Balancer", sh.Balancer}, {"Shards", fmt.Sprint(len(sh.Shards))}, {"Routers", routers},
			{"Sharded collections", fmt.Sprint(len(sh.Collections))}, {"Chunks", fmt.Sprint(chunks)}}
	}
	return nil
}

func (t *ClusterTab) Layout(gtx C) D {
	a, th := t.a, t.a.th
	if t.pauseBtn.Clicked(gtx) {
		t.paused = !t.paused
	}
	if t.refreshBtn.Clicked(gtx) {
		t.refresh()
	}
	if !t.paused && !t.loading && gtx.Now.Sub(t.at) > 5*time.Second {
		t.refresh()
	}
	if !t.paused && t.topology != "standalone" {
		gtx.Execute(opInvalidateIn(gtx, time.Second))
	}
	title := "Replica set of "
	if t.topology == "sharded" {
		title = "Sharded cluster of "
	}
	children := []layout.FlexChild{
		layout.Rigid(func(gtx C) D {
			return bg(gtx, pal.Header, func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Inset{Left: 10, Right: 8, Top: 5, Bottom: 5}.Layout(gtx, func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							return iconText(gtx, th, icCluster, 13, pal.TextBright, title+a.connState(t.connID).name())
						}),
						layout.Rigid(layout.Spacer{Width: 12}.Layout),
						layout.Rigid(func(gtx C) D {
							label := "Pause"
							if t.paused {
								label = "Resume"
							}
							return pillButton(gtx, th, &t.pauseBtn, nil, label, pal.Text, pal.Hover)
						}),
						layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &t.refreshBtn, icRefresh, "Refresh", pal.Text, colorNRGBA{}) }),
						layout.Flexed(1, func(gtx C) D { return D{Size: image.Pt(gtx.Constraints.Min.X, 1)} }),
						layout.Rigid(func(gtx C) D { return a.connBadges(gtx, t.connID) }),
					)
				})
			})
		}),
		layout.Rigid(func(gtx C) D { return hline(gtx, pal.Border) }),
	}
	switch {
	case t.topology == "standalone":
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, append(children, layout.Rigid(func(gtx C) D {
			return inset(12).Layout(gtx, func(gtx C) D {
				return lbl(gtx, th, fontSans, 12.5, pal.TextDim, "This server is a standalone instance: it is not part of a replica set or a sharded cluster.")
			})
		}))...)
	case t.err != nil:
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, append(children, layout.Rigid(func(gtx C) D {
			return inset(12).Layout(gtx, func(gtx C) D { return lbl(gtx, th, fontSans, 12.5, pal.Err, db.AsError(t.err).Error()) })
		}))...)
	}
	children = append(children,
		layout.Rigid(func(gtx C) D { return inset(8).Layout(gtx, func(gtx C) D { return drawTiles(gtx, th, t.tiles()) }) }),
	)
	n := 1
	if t.topology == "sharded" {
		n = 3
	}
	for i := 0; i < n; i++ {
		i := i
		children = append(children,
			layout.Rigid(func(gtx C) D { return hline(gtx, pal.Border) }),
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Left: 10, Top: 4, Bottom: 4}.Layout(gtx, func(gtx C) D {
					return lbl(gtx, th, fontSans, 12, pal.Text, t.titles[i])
				})
			}),
			layout.Flexed(1, t.grids[i].Layout),
		)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

// ---- export to code ----

var (
	codeTargets      = []string{"go", "python", "node", "java", "csharp"}
	codeTargetLabels = map[string]string{"go": "Go", "python": "Python", "node": "Node.js", "java": "Java", "csharp": "C#"}
)

// CodeDialog shows a statement as driver code in the language picked.
type CodeDialog struct {
	app     *App
	dl      db.DocumentLanguage
	stmt    string
	dbName  string
	targets []string
	sel     int
	program bool
	shown   string
	out     *CodeEditor
	err     string
	done    bool
	d       dialog

	targetBtns                    []widget.Clickable
	programBtn, copyBtn, closeBtn widget.Clickable
}

func (a *App) openCodeExport(connID, stmt, dbName string) {
	dl, ok := a.connState(connID).docLanguage()
	if !ok {
		a.flash("Code export is for MongoDB connections", true)
		return
	}
	if strings.TrimSpace(stmt) == "" {
		a.flash("Write or select a statement first", false)
		return
	}
	t := &CodeDialog{app: a, dl: dl, stmt: strings.TrimSpace(stmt), dbName: dbName, targets: codeTargets}
	t.targetBtns = make([]widget.Clickable, len(t.targets))
	t.out = newCodeEditor(a, "")
	t.out.ReadOnly = true
	t.out.setLanguage(dl)
	a.overlay = t
}

func (t *CodeDialog) Done() bool { return t.done }

func (t *CodeDialog) Layout(gtx C) D {
	a, th := t.app, t.app.th
	for i := range t.targetBtns {
		if t.targetBtns[i].Clicked(gtx) {
			t.sel = i
		}
	}
	if t.programBtn.Clicked(gtx) {
		t.program = !t.program
	}
	if key := fmt.Sprint(t.sel, t.program); key != t.shown {
		t.shown = key
		code, err := t.dl.GenerateCode(t.stmt, t.dbName, t.targets[t.sel], t.program)
		t.err = ""
		if err != nil {
			t.err = err.Error()
			code = ""
		}
		t.out.SetText(code)
	}
	if t.closeBtn.Clicked(gtx) || t.d.close.Clicked(gtx) {
		t.done = true
	}
	if t.copyBtn.Clicked(gtx) && t.err == "" {
		t.done = true
		a.copyLaterMsg(t.out.Text(), "Code copied")
	}
	labels := make([]string, len(t.targets))
	for i, tg := range t.targets {
		labels[i] = codeTargetLabels[tg]
	}
	return t.d.layout(gtx, th, 900, icCommand, "Export to code", func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return segmented(gtx, th, t.targetBtns, labels, t.sel) }),
					layout.Rigid(layout.Spacer{Width: 12}.Layout),
					layout.Rigid(func(gtx C) D { return toggle(gtx, th, &t.programBtn, "Complete program", t.program) }),
					layout.Rigid(layout.Spacer{Width: 12}.Layout),
					layout.Flexed(1, func(gtx C) D {
						hint := "Code for the official driver, with the BSON types of the statement; it expects a connected client."
						if t.program {
							hint = "Imports included; the program connects to the URI of the MONGODB_URI environment variable."
						}
						return lbl(gtx, th, fontSans, 12, pal.TextDim, hint)
					}),
				)
			}),
			layout.Rigid(layout.Spacer{Height: 8}.Layout),
			layout.Rigid(func(gtx C) D {
				if t.err != "" {
					return lbl(gtx, th, fontMono, 12, pal.Err, t.err)
				}
				gtx.Constraints.Min.Y, gtx.Constraints.Max.Y = gtx.Dp(380), gtx.Dp(380)
				return rborder(gtx, pal.Border, pal.CodeBg, 6, func(gtx C) D { return inset(1).Layout(gtx, t.out.Layout) })
			}),
		)
	}, footerButtons(nil,
		func(gtx C) D { return pillButton(gtx, th, &t.closeBtn, nil, "Close", pal.Text, pal.Hover) },
		func(gtx C) D { return button(gtx, th, &t.copyBtn, "Copy", pal.Accent) },
	))
}
