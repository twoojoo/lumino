package ui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/twoojoo/lumino/internal/db"
)

// Collection tools of the table view (document databases): schema
// analysis, index and validation management, explain, and the entries to
// the pipeline builder, change streams and imports.

type tableTools struct {
	schema     *db.SchemaReport
	schemaErr  error
	analyzing  bool
	schemaGrid *DataGrid
	sampleEd   widget.Editor

	analyzeBtn, validatorBtn                     widget.Clickable
	createIdxBtn, dropIdxBtn, validationBtn      widget.Clickable
	explainBtn, pipelineBtn, watchBtn, importBtn widget.Clickable
}

// isDocCollection reports whether the tab shows a collection of a
// document database.
func (t *TableTab) isDocCollection() bool {
	_, ok := t.cs().docLanguage()
	return ok && t.ref.Type == db.ObjCollection
}

// ---- data view entries ----

func (t *TableTab) dataTools(gtx C) D {
	a, th := t.a, t.a.th
	tt := &t.tools
	if tt.explainBtn.Clicked(gtx) {
		t.explain()
	}
	if tt.pipelineBtn.Clicked(gtx) {
		a.openPipeline(t.connID, t.ref, strings.TrimSpace(t.where.Text()))
	}
	if tt.watchBtn.Clicked(gtx) {
		a.openWatch(t.connID, t.ref)
	}
	if tt.importBtn.Clicked(gtx) {
		a.openImport(t.connID, t.ref, func() { t.refresh(nil) })
	}
	if !t.isDocCollection() {
		return D{}
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return pillButton(gtx, th, &tt.explainBtn, icDescribe, "Explain", pal.Text, colorNRGBA{})
		}),
		layout.Rigid(func(gtx C) D {
			return pillButton(gtx, th, &tt.pipelineBtn, icTree, "Aggregate…", pal.Text, colorNRGBA{})
		}),
		layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &tt.watchBtn, icEye, "Watch", pal.Text, colorNRGBA{}) }),
		layout.Rigid(func(gtx C) D {
			return pillButton(gtx, th, &tt.importBtn, icDownload, "Import…", pal.Text, colorNRGBA{})
		}),
	)
}

// ---- schema analysis ----

func (t *TableTab) analyzeSchema() {
	a := t.a
	tt := &t.tools
	tools := t.cs().docTools()
	if tools == nil || tt.analyzing {
		return
	}
	n := 1000
	if v, err := strconv.Atoi(strings.TrimSpace(tt.sampleEd.Text())); err == nil && v > 0 {
		n = min(v, 100000)
	}
	tt.analyzing, tt.schemaErr = true, nil
	ref := t.ref
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		rep, err := tools.AnalyzeSchema(ctx, ref, n)
		a.post(func() {
			tt.analyzing, tt.schema, tt.schemaErr = false, rep, err
			if rep != nil {
				tt.schemaGrid.SetModel(schemaModel(rep))
			}
		})
	}()
}

// schemaModel lists the fields of a report.
func schemaModel(rep *db.SchemaReport) *sliceModel {
	m := &sliceModel{cols: textCols("Field", "Presence", "Types", "Min", "Max", "Frequent values", "Distinct")}
	m.cols[1].Kind, m.cols[6].Kind = db.KindDecimal, db.KindInt
	for _, f := range rep.Fields {
		var types []string
		for _, ts := range f.Types {
			types = append(types, fmt.Sprintf("%s %.0f%%", ts.Type, ts.Share*100))
		}
		var top []string
		for _, v := range f.Top {
			top = append(top, fmt.Sprintf("%s ×%d", v.Value, v.Count))
		}
		distinct := strconv.Itoa(f.Distinct)
		if f.Distinct >= 1000 {
			distinct = "1000+"
		}
		m.rows = append(m.rows, []any{f.Path, fmt.Sprintf("%.1f%%", f.Presence*100), strings.Join(types, ", "), f.Min, f.Max,
			strings.Join(top, "  "), distinct})
	}
	return m
}

// jsonSchema builds a validator from a report: top-level fields with
// their types, required when present in every sampled document.
func jsonSchema(rep *db.SchemaReport) string {
	bsonType := map[string]string{"objectId": "objectId", "string": "string", "int": "int", "long": "long", "double": "double",
		"decimal": "decimal", "bool": "bool", "date": "date", "object": "object", "array": "array", "binData": "binData",
		"null": "null", "timestamp": "timestamp", "regex": "regex"}
	var req, props []string
	for _, f := range rep.Fields {
		if strings.ContainsAny(f.Path, ".[") {
			continue
		}
		var types []string
		for _, ts := range f.Types {
			if bt, ok := bsonType[ts.Type]; ok {
				types = append(types, strconv.Quote(bt))
			}
		}
		sort.Strings(types)
		tv := "[" + strings.Join(types, ", ") + "]"
		if len(types) == 1 {
			tv = types[0]
		}
		props = append(props, fmt.Sprintf("      %s: {bsonType: %s}", strconv.Quote(f.Path), tv))
		if f.Presence >= 1 {
			req = append(req, strconv.Quote(f.Path))
		}
	}
	return "{\n  $jsonSchema: {\n    bsonType: \"object\",\n    required: [" + strings.Join(req, ", ") + "],\n    properties: {\n" +
		strings.Join(props, ",\n") + "\n    }\n  }\n}"
}

func (t *TableTab) layoutSchema(gtx C) D {
	th := t.a.th
	tt := &t.tools
	if tt.schemaGrid == nil {
		tt.schemaGrid = newDataGrid(t.a)
		tt.sampleEd.SingleLine, tt.sampleEd.Submit = true, true
		tt.sampleEd.SetText("1000")
	}
	if tt.schema == nil && tt.schemaErr == nil && !tt.analyzing {
		t.analyzeSchema()
	}
	if tt.analyzeBtn.Clicked(gtx) {
		t.analyzeSchema()
	}
	for {
		ev, ok := tt.sampleEd.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			t.analyzeSchema()
		}
	}
	if tt.validatorBtn.Clicked(gtx) && tt.schema != nil {
		t.validationDialog(jsonSchema(tt.schema))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: 8, Right: 8, Top: 6, Bottom: 6}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontSans, 12, pal.Text, "Sample ") }),
					layout.Rigid(func(gtx C) D {
						gtx.Constraints.Min.X, gtx.Constraints.Max.X = gtx.Dp(80), gtx.Dp(80)
						return textField(gtx, th, &tt.sampleEd, "1000", true)
					}),
					layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontSans, 12, pal.Text, " documents ") }),
					layout.Rigid(func(gtx C) D {
						return pillButton(gtx, th, &tt.analyzeBtn, icRefresh, "Analyze", pal.Text, colorNRGBA{})
					}),
					layout.Rigid(func(gtx C) D {
						if tt.schema == nil || t.readOnlyReason() != "" {
							return D{}
						}
						return pillButton(gtx, th, &tt.validatorBtn, icSecurity, "Validator from this schema…", pal.Text, colorNRGBA{})
					}),
					layout.Flexed(1, func(gtx C) D {
						s := ""
						switch {
						case tt.analyzing:
							s = "Analyzing…"
						case tt.schema != nil:
							s = fmt.Sprintf("%d field(s) in %d sampled document(s)", len(tt.schema.Fields), tt.schema.Sampled)
							if tt.schema.Total >= 0 {
								s += fmt.Sprintf(" of about %d", tt.schema.Total)
							}
						}
						return layout.E.Layout(gtx, func(gtx C) D { return lbl(gtx, th, fontSans, 11.5, pal.TextDim, s) })
					}),
				)
			})
		}),
		layout.Rigid(func(gtx C) D { return hline(gtx, pal.Border) }),
		layout.Flexed(1, func(gtx C) D {
			if tt.schemaErr != nil {
				return inset(16).Layout(gtx, func(gtx C) D { return lbl(gtx, th, fontSans, 12.5, pal.Err, tt.schemaErr.Error()) })
			}
			return tt.schemaGrid.Layout(gtx)
		}),
	)
}

// ---- indexes and validation ----

func (t *TableTab) structureTools(gtx C) D {
	th := t.a.th
	tt := &t.tools
	if !t.isDocCollection() || t.readOnlyReason() != "" {
		return D{}
	}
	if tt.createIdxBtn.Clicked(gtx) {
		t.createIndexDialog("")
	}
	if tt.dropIdxBtn.Clicked(gtx) {
		t.dropSelectedIndex()
	}
	if tt.validationBtn.Clicked(gtx) {
		cur := "{}"
		if t.info != nil {
			for _, c := range t.info.Constraints {
				if c.Type == "VALIDATOR" {
					cur = c.Definition
				}
			}
		}
		t.validationDialog(cur)
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return pillButton(gtx, th, &tt.createIdxBtn, icAdd, "Create index…", pal.Text, colorNRGBA{})
		}),
		layout.Rigid(func(gtx C) D {
			if t.structTab != 1 {
				return D{}
			}
			return pillButton(gtx, th, &tt.dropIdxBtn, icDelete, "Drop selected index", pal.Text, colorNRGBA{})
		}),
		layout.Rigid(func(gtx C) D {
			return pillButton(gtx, th, &tt.validationBtn, icSecurity, "Validation…", pal.Text, colorNRGBA{})
		}),
	)
}

// afterSchemaChange reloads the structure and the DDL.
func (t *TableTab) afterSchemaChange() {
	cs := t.cs()
	delete(cs.tables, t.ref)
	t.info, t.ddlState = nil, 0
	t.ensureInfo()
}

func (t *TableTab) createIndexDialog(keys string) {
	a := t.a
	dl, ok := t.cs().docLanguage()
	if !ok {
		return
	}
	if keys == "" {
		keys = "{field: 1}"
	}
	f := newFormDialog(a, dl, "Create an index on "+t.ref.Name, "Keys: 1 ascending, -1 descending, \"text\", \"2dsphere\", \"hashed\". "+
		"Options: name, unique, sparse, expireAfterSeconds, partialFilterExpression, hidden, collation.",
		func(v []string) error {
			stmt, err := dl.AdminStatement("createIndex", t.ref, v[0], v[1])
			if err != nil {
				return err
			}
			a.reviewRun(t.connID, "Create index", stmt, "", t.afterSchemaChange)
			return nil
		},
		codeField("Keys", "{email: 1}", keys),
		codeField("Options", "{unique: true}", "{}"))
	a.overlay = f
}

func (t *TableTab) dropSelectedIndex() {
	a := t.a
	dl, ok := t.cs().docLanguage()
	if !ok || t.info == nil {
		return
	}
	sel, has := t.structGrids[1].Selection()
	if !has || sel.r1 >= len(t.info.Indexes) {
		a.flash("Select an index in the list first", false)
		return
	}
	ix := t.info.Indexes[sel.r1]
	if ix.Primary {
		a.flash("The _id index can't be dropped", true)
		return
	}
	stmt, err := dl.AdminStatement("dropIndex", t.ref, ix.Name)
	if err != nil {
		a.flash(err.Error(), true)
		return
	}
	a.reviewRun(t.connID, "Drop index "+ix.Name, stmt, "", t.afterSchemaChange)
}

func (t *TableTab) validationDialog(validator string) {
	a := t.a
	dl, ok := t.cs().docLanguage()
	if !ok {
		return
	}
	f := newFormDialog(a, dl, "Validation of "+t.ref.Name, "The validator is a query document or {$jsonSchema: …}; {} removes the rules. "+
		"Level: strict (all writes), moderate (valid documents only), off. Action: error (refuse) or warn (log).",
		func(v []string) error {
			stmt, err := dl.AdminStatement("setValidator", t.ref, v[0], strings.TrimSpace(v[1]), strings.TrimSpace(v[2]))
			if err != nil {
				return err
			}
			a.reviewRun(t.connID, "Change the validation rules", stmt, "", t.afterSchemaChange)
			return nil
		},
		multiField("Validator", validator),
		plainField("Level", "strict | moderate | off", "strict", false),
		plainField("Action", "error | warn", "error", false))
	a.overlay = f
}

// ---- explain ----

// ExplainView shows the executed plan of the browse query.
type ExplainView struct {
	app    *App
	t      *TableTab
	rep    *db.ExplainReport
	err    error
	done   bool
	d      dialog
	create []widget.Clickable
	closeB widget.Clickable
}

func (t *TableTab) explain() {
	a := t.a
	tools := t.cs().docTools()
	dl, ok := t.cs().docLanguage()
	if tools == nil || !ok {
		return
	}
	q := db.BrowseQuery{Filter: t.where.Text(), Projection: t.proj.Text()}
	if ks, err := dl.ParseSort(t.sortEd.Text()); err == nil {
		q.Sort = ks
	}
	if n, err := strconv.ParseInt(strings.TrimSpace(t.limit.Text()), 10, 64); err == nil {
		q.Limit = n
	}
	v := &ExplainView{app: a, t: t}
	a.overlay = v
	ref := t.ref
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		rep, err := tools.ExplainFind(ctx, ref, q)
		a.post(func() {
			v.rep, v.err = rep, err
			if rep != nil {
				v.create = make([]widget.Clickable, len(rep.Suggestions))
			}
		})
	}()
}

func (v *ExplainView) Done() bool { return v.done }

// stageColor marks costly stages.
func stageColor(stage string) colorNRGBA {
	switch stage {
	case "COLLSCAN", "SORT":
		return pal.Err
	case "IXSCAN", "IDHACK", "EXPRESS_IXSCAN", "EXPRESS_IDHACK", "COUNT_SCAN", "DISTINCT_SCAN":
		return pal.Ok
	}
	return pal.Info
}

func (v *ExplainView) layoutPlan(gtx C, th *material.Theme, n db.PlanNode, depth int) []layout.FlexChild {
	ch := []layout.FlexChild{layout.Rigid(func(gtx C) D {
		return layout.Inset{Left: unit16(depth), Bottom: 6}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontMono, 12.5, stageColor(n.Stage), n.Stage) }),
						layout.Rigid(func(gtx C) D {
							s := fmt.Sprintf("   returned %d · docs examined %d · keys examined %d · ~%d ms", n.NReturned, n.DocsExamined, n.KeysExamined, n.Millis)
							if n.Index != "" {
								s = "   index " + n.Index + s
							}
							return lbl(gtx, th, fontSans, 11.5, pal.TextDim, s)
						}),
					)
				}),
				layout.Rigid(func(gtx C) D {
					if n.Details == "" {
						return D{}
					}
					return lbl(gtx, th, fontMono, 11, pal.Text, n.Details)
				}),
			)
		})
	})}
	for _, c := range n.Children {
		ch = append(ch, v.layoutPlan(gtx, th, c, depth+1)...)
	}
	return ch
}

func unit16(depth int) unit.Dp { return unit.Dp(16 * depth) }

func (v *ExplainView) Layout(gtx C) D {
	a, th := v.app, v.app.th
	if v.closeB.Clicked(gtx) || v.d.close.Clicked(gtx) {
		v.done = true
	}
	for i := range v.create {
		if v.create[i].Clicked(gtx) {
			s := v.rep.Suggestions[i]
			v.done = true
			a.reviewRun(v.t.connID, "Create index", s.Statement, "", v.t.afterSchemaChange)
		}
	}
	return v.d.layout(gtx, th, 900, icDescribe, "Explain "+v.t.ref.Name, func(gtx C) D {
		switch {
		case v.err != nil:
			return lbl(gtx, th, fontSans, 12.5, pal.Err, v.err.Error())
		case v.rep == nil:
			return lbl(gtx, th, fontSans, 12.5, pal.TextDim, "Running the query with execution statistics…")
		}
		r := v.rep
		ch := []layout.FlexChild{
			layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontMono, 12, pal.TextBright, r.Statement) }),
			layout.Rigid(layout.Spacer{Height: 8}.Layout),
			layout.Rigid(func(gtx C) D {
				return lbl(gtx, th, fontSans, 12.5, pal.Text, fmt.Sprintf("%d document(s) returned · %d examined · %d index keys examined · %d ms",
					r.Returned, r.DocsExamined, r.KeysExamined, r.Millis))
			}),
			layout.Rigid(layout.Spacer{Height: 12}.Layout),
		}
		for i, s := range r.Suggestions {
			i, s := i, s
			ch = append(ch, layout.Rigid(func(gtx C) D {
				return rborder(gtx, withAlpha(pal.Warn, 0x90), pal.Panel, 8, func(gtx C) D {
					return inset(10).Layout(gtx, func(gtx C) D {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(func(gtx C) D { return iconText(gtx, th, icIdea, 12.5, pal.Warn, s.Reason) }),
							layout.Rigid(layout.Spacer{Height: 6}.Layout),
							layout.Rigid(func(gtx C) D {
								return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
									layout.Flexed(1, func(gtx C) D { return lbl(gtx, th, fontMono, 12.5, pal.TextBright, s.Keys) }),
									layout.Rigid(func(gtx C) D { return button(gtx, th, &v.create[i], "Create this index…", pal.Accent) }),
								)
							}),
						)
					})
				})
			}), layout.Rigid(layout.Spacer{Height: 12}.Layout))
		}
		if len(r.Suggestions) == 0 {
			ch = append(ch, layout.Rigid(func(gtx C) D {
				return iconText(gtx, th, icOK, 12.5, pal.Ok, "No index suggestion: the plan doesn't scan the collection or sort in memory.")
			}), layout.Rigid(layout.Spacer{Height: 12}.Layout))
		}
		ch = append(ch, layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontSans, 12, pal.TextDim, "Plan (top stage first)") }),
			layout.Rigid(layout.Spacer{Height: 6}.Layout))
		ch = append(ch, v.layoutPlan(gtx, th, r.Plan, 0)...)
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, ch...)
	}, footerButtons(nil, func(gtx C) D { return pillButton(gtx, th, &v.closeB, nil, "Close", pal.Text, pal.Hover) }))
}
