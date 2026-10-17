package ui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/prefs"
)

// previewLimit is how many documents each stage preview shows.
const previewLimit = 20

// pipeStage is a stage of the pipeline builder.
type pipeStage struct {
	op      string
	body    *CodeEditor
	enabled bool

	selBtn, opBtn, onBtn, upBtn, downBtn, delBtn widget.Clickable
}

// PipelineTab builds an aggregation pipeline stage by stage, with the
// output of the selected stage previewed on the collection.
type PipelineTab struct {
	a      *App
	connID string
	ref    db.ObjectRef
	stages []*pipeStage
	sel    int

	sess     db.Session
	gen      int
	editVer  int // stage texts changed (debounced preview)
	lastEdit time.Time
	shown    int // editVer of the preview
	running  bool
	rs       *resultSet
	err      error
	note     string

	grid  *DataGrid
	docs  *docView
	mode  int
	modeB [3]widget.Clickable
	list  widget.List

	addBtn, consoleBtn, codeBtn, refreshBtn widget.Clickable
}

// openPipeline opens the builder on a collection (filter: an initial
// $match).
func (a *App) openPipeline(connID string, ref db.ObjectRef, filter string) {
	dl, ok := a.connState(connID).docLanguage()
	if !ok {
		return
	}
	t := &PipelineTab{a: a, connID: connID, ref: ref, grid: newDataGrid(a), docs: newDocView(a), mode: viewTree}
	t.list.Axis = layout.Vertical
	if filter != "" && filter != "{}" {
		t.addStage(dl, "$match", filter)
	} else {
		t.addStage(dl, "$match", "{}")
	}
	a.addTab(t)
}

func (t *PipelineTab) addStage(lang db.Language, op, body string) {
	st := &pipeStage{op: op, enabled: true, body: newCodeEditor(t.a, body)}
	st.body.setLanguage(lang)
	st.body.OnChange = func() { t.editVer++; t.lastEdit = time.Now() }
	st.body.Completer = func(e *CodeEditor) *compRequest {
		return queryItems(lang, func() []string {
			if info := t.a.connState(t.connID).tables[t.ref]; info != nil {
				var out []string
				for _, c := range info.Columns {
					out = append(out, c.Name, "$"+c.Name)
				}
				return out
			}
			return nil
		})
	}
	t.stages = append(t.stages, st)
	t.sel = len(t.stages) - 1
	t.editVer++
}

func (t *PipelineTab) Title() string      { return t.ref.Name + " · pipeline" }
func (t *PipelineTab) Icon() *widget.Icon { return icTree }
func (t *PipelineTab) ConnID() string     { return t.connID }
func (t *PipelineTab) Dirty() string      { return "" }
func (t *PipelineTab) StatusInfo() string {
	return t.ref.String() + " · " + itoa(len(t.stages)) + " stage(s)"
}
func (t *PipelineTab) State() prefs.TabState {
	return prefs.TabState{} // not restored
}

func (t *PipelineTab) Focused(gtx C) bool {
	for _, s := range t.stages {
		if s.body.Focused(gtx) {
			return true
		}
	}
	return t.grid.Focused(gtx)
}

func (t *PipelineTab) Disconnected() {
	t.gen++
	if s := t.sess; s != nil {
		t.sess = nil
		go s.Close()
	}
	t.running = false
}

func (t *PipelineTab) Dispose() { t.Disconnected() }

// stageText is the stage document of a stage.
func (s *pipeStage) stageText() string {
	return "{" + s.op + ": " + strings.TrimSpace(s.body.Text()) + "}"
}

// statement builds the pipeline up to stage upTo (-1 = all), without the
// stages writing to collections when preview.
func (t *PipelineTab) statement(upTo int, preview bool) (string, string, error) {
	dl, ok := t.a.connState(t.connID).docLanguage()
	if !ok {
		return "", "", errors.New("not connected")
	}
	var parts []string
	note := ""
	for i, s := range t.stages {
		if upTo >= 0 && i > upTo {
			break
		}
		if !s.enabled {
			continue
		}
		if preview && (s.op == "$out" || s.op == "$merge") {
			note = s.op + " is not run in previews: it writes to a collection"
			continue
		}
		parts = append(parts, s.stageText())
	}
	limit := 0
	if preview {
		limit = previewLimit
	}
	q, err := dl.Aggregate(t.ref, parts, limit)
	return q, note, err
}

// preview runs the pipeline up to the selected stage.
func (t *PipelineTab) preview() {
	a := t.a
	q, note, err := t.statement(t.sel, true)
	t.shown, t.note = t.editVer, note
	if err != nil {
		t.err, t.rs = err, nil
		return
	}
	t.gen++
	gen := t.gen
	t.running, t.err = true, nil
	a.connectOr(t.connID, func(cs *connState) {
		conn, sess := cs.conn, t.sess
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			var rs *resultSet
			err := func() error {
				if sess == nil {
					s, err := conn.Session(ctx)
					if err != nil {
						return err
					}
					_ = s.SetReadOnly(ctx, true) // previews never write
					sess = s
				}
				start := time.Now()
				res, err := sess.Execute(ctx, q)
				if err != nil {
					return err
				}
				defer res.Close()
				rs = &resultSet{sql: q, sortCol: -1, affected: -1, isQuery: true, started: start, cols: res.Columns()}
				rs.rows, _, err = res.Fetch(ctx, previewLimit)
				rs.docs = docsOf(res, len(rs.rows))
				rs.elapsed = time.Since(start)
				return err
			}()
			a.post(func() {
				t.sess = sess
				if gen != t.gen {
					return
				}
				t.running, t.err = false, err
				if err == nil {
					t.rs = rs
					t.grid.SetModel(rs)
					t.docs.reset()
				}
			})
		}()
	}, func(err error) { t.running, t.err = false, err })
}

func (t *PipelineTab) Layout(gtx C) D {
	a, th := t.a, t.a.th
	dl, ok := a.connState(t.connID).docLanguage()
	if !ok {
		return layout.Center.Layout(gtx, func(gtx C) D { return lbl(gtx, th, fontSans, 12.5, pal.TextDim, "Connecting…") })
	}
	if t.addBtn.Clicked(gtx) {
		var names []string
		for _, st := range dl.StageTemplates() {
			names = append(names, st[0])
		}
		p := newPicker(a, "Add a stage", names, false, nil)
		p.onPickIndex = func(i int) {
			st := dl.StageTemplates()[i]
			t.addStage(dl, st[0], st[1])
		}
		a.overlay = p
	}
	if t.consoleBtn.Clicked(gtx) {
		if q, _, err := t.statement(-1, false); err != nil {
			a.flash(err.Error(), true)
		} else {
			a.newConsole(t.connID, t.ref.Schema, q+"\n")
		}
	}
	if t.codeBtn.Clicked(gtx) {
		if q, _, err := t.statement(-1, false); err != nil {
			a.flash(err.Error(), true)
		} else {
			a.openCodeExport(t.connID, q, t.ref.Schema)
		}
	}
	if t.refreshBtn.Clicked(gtx) {
		t.preview()
	}
	for i := 0; i < len(t.stages); i++ {
		s := t.stages[i]
		switch {
		case s.selBtn.Clicked(gtx):
			t.sel = i
			t.editVer++
		case s.onBtn.Clicked(gtx):
			s.enabled = !s.enabled
			t.editVer++
		case s.upBtn.Clicked(gtx) && i > 0:
			t.stages[i-1], t.stages[i] = t.stages[i], t.stages[i-1]
			t.sel = i - 1
			t.editVer++
		case s.downBtn.Clicked(gtx) && i < len(t.stages)-1:
			t.stages[i+1], t.stages[i] = t.stages[i], t.stages[i+1]
			t.sel = i + 1
			t.editVer++
		case s.delBtn.Clicked(gtx):
			t.stages = append(t.stages[:i], t.stages[i+1:]...)
			t.sel = min(t.sel, len(t.stages)-1)
			t.editVer++
		case s.opBtn.Clicked(gtx):
			var names []string
			for _, st := range dl.StageTemplates() {
				names = append(names, st[0])
			}
			p := newPicker(a, "Stage operator", names, false, nil)
			p.onPickIndex = func(k int) {
				st := dl.StageTemplates()[k]
				s.op = st[0]
				if strings.TrimSpace(s.body.Text()) == "" || strings.TrimSpace(s.body.Text()) == "{}" {
					s.body.SetText(st[1])
				}
				t.editVer++
			}
			a.overlay = p
		}
	}
	// preview the selected stage once typing pauses
	if t.shown != t.editVer && len(t.stages) > 0 && !t.running && gtx.Now.Sub(t.lastEdit) > 500*time.Millisecond {
		t.preview()
	} else if t.shown != t.editVer {
		gtx.Execute(opInvalidateIn(gtx, 200*time.Millisecond))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return bg(gtx, pal.Header, func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Inset{Left: 10, Right: 8, Top: 5, Bottom: 5}.Layout(gtx, func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx C) D { return iconText(gtx, th, icTree, 13, pal.TextBright, "Pipeline on "+t.ref.String()) }),
						layout.Rigid(layout.Spacer{Width: 14}.Layout),
						layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &t.addBtn, icAdd, "Add stage", pal.Text, pal.Hover) }),
						layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &t.refreshBtn, icRefresh, "Preview", pal.Text, colorNRGBA{}) }),
						layout.Rigid(func(gtx C) D {
							return pillButton(gtx, th, &t.consoleBtn, icConsole, "Open in console", pal.Text, colorNRGBA{})
						}),
						layout.Rigid(func(gtx C) D {
							return pillButton(gtx, th, &t.codeBtn, icCode, "Export to code…", pal.Text, colorNRGBA{})
						}),
						layout.Flexed(1, func(gtx C) D { return D{Size: image.Pt(gtx.Constraints.Min.X, 1)} }),
						layout.Rigid(func(gtx C) D { return a.connBadges(gtx, t.connID) }),
					)
				})
			})
		}),
		layout.Rigid(func(gtx C) D { return hline(gtx, pal.Border) }),
		layout.Flexed(1, func(gtx C) D {
			return layout.Flex{}.Layout(gtx,
				layout.Flexed(0.45, func(gtx C) D { return t.layoutStages(gtx, th) }),
				layout.Rigid(func(gtx C) D { return vline(gtx, pal.Border) }),
				layout.Flexed(0.55, func(gtx C) D { return t.layoutPreview(gtx, th, dl) }),
			)
		}),
	)
}

func (t *PipelineTab) layoutStages(gtx C, th *material.Theme) D {
	return material.List(th, &t.list).Layout(gtx, len(t.stages), func(gtx C, i int) D {
		s := t.stages[i]
		border := pal.Border
		if i == t.sel {
			border = pal.Accent
		}
		return layout.Inset{Left: 8, Right: 8, Top: 6, Bottom: 2}.Layout(gtx, func(gtx C) D {
			return rborder(gtx, border, pal.Panel, 8, func(gtx C) D {
				return inset(6).Layout(gtx, func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(func(gtx C) D {
									return s.selBtn.Layout(gtx, func(gtx C) D {
										return lbl(gtx, th, fontSans, 12, pal.TextDim, fmt.Sprintf("Stage %d  ", i+1))
									})
								}),
								layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &s.opBtn, nil, s.op, pal.Keyword, pal.Hover) }),
								layout.Flexed(1, func(gtx C) D {
									return s.selBtn.Layout(gtx, func(gtx C) D { return D{Size: image.Pt(gtx.Constraints.Max.X, gtx.Dp(20))} })
								}),
								layout.Rigid(func(gtx C) D {
									ic := icUnchecked
									if s.enabled {
										ic = icChecked
									}
									return iconOnly(gtx, &s.onBtn, ic, 15)
								}),
								layout.Rigid(func(gtx C) D { return iconOnly(gtx, &s.upBtn, icUp, 15) }),
								layout.Rigid(func(gtx C) D { return iconOnly(gtx, &s.downBtn, icDown, 15) }),
								layout.Rigid(func(gtx C) D { return iconOnly(gtx, &s.delBtn, icDelete, 15) }),
							)
						}),
						layout.Rigid(layout.Spacer{Height: 4}.Layout),
						layout.Rigid(func(gtx C) D {
							lines := min(12, max(3, s.body.buf.lineCount()+1))
							h := lines*s.body.lineHeight(gtx) + gtx.Dp(8)
							gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, h))
							return rborder(gtx, pal.Border, pal.CodeBg, 6, func(gtx C) D { return inset(1).Layout(gtx, s.body.Layout) })
						}),
					)
				})
			})
		})
	})
}

func (t *PipelineTab) layoutPreview(gtx C, th *material.Theme, lang db.Language) D {
	for i := range t.modeB {
		if t.modeB[i].Clicked(gtx) {
			t.mode = i
		}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: 10, Right: 8, Top: 6, Bottom: 6}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx C) D {
						s := fmt.Sprintf("Output of stage %d (first %d documents)", t.sel+1, previewLimit)
						switch {
						case t.running:
							s += " · running…"
						case t.rs != nil:
							s += fmt.Sprintf(" · %d document(s) · %s", len(t.rs.rows), fmtDur(t.rs.elapsed))
						}
						return lbl(gtx, th, fontSans, 12, pal.Text, s)
					}),
					layout.Rigid(func(gtx C) D { return segmented(gtx, th, t.modeB[:], viewNames[:], t.mode) }),
				)
			})
		}),
		layout.Rigid(func(gtx C) D {
			if t.note == "" {
				return D{}
			}
			return layout.Inset{Left: 10, Bottom: 4}.Layout(gtx, func(gtx C) D { return iconText(gtx, th, icWarning, 12, pal.Warn, t.note) })
		}),
		layout.Rigid(func(gtx C) D { return hline(gtx, pal.Border) }),
		layout.Flexed(1, func(gtx C) D {
			switch {
			case t.err != nil:
				return inset(14).Layout(gtx, func(gtx C) D {
					return t.a.selText(gtx, "pipeerr", fontMono, 12, pal.Err, db.AsError(t.err).Error(), 0)
				})
			case t.rs == nil:
				return layout.Center.Layout(gtx, func(gtx C) D { return lbl(gtx, th, fontSans, 12.5, pal.TextDim, "Preview…") })
			case t.mode != viewTable && t.rs.hasDocs():
				return t.docs.Layout(gtx, t.rs, lang, t.mode)
			}
			return t.grid.Layout(gtx)
		}),
	)
}
