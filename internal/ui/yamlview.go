package ui

import (
	"fmt"
	"image"
	"regexp"
	"strconv"
	"strings"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"gioui.org/x/richtext"
	"sigs.k8s.io/yaml"
)

func tokColor(k tokKind) colorNRGBA {
	switch k {
	case tkKey:
		return pal.Info
	case tkString:
		return pal.Ok
	case tkNumber:
		return pal.Warn
	case tkBool:
		if pal.Light {
			return rgb(0x8a3fc4)
		}
		return rgb(0xc792ea)
	case tkComment, tkPunct:
		return pal.TextDim
	}
	return pal.Text
}

// yamlView is a read-only, highlighted, virtualized YAML viewer with line
// numbers.
type yamlView struct {
	text      string
	lines     [][]token
	list      widget.List
	rt        richtext.InteractiveText
	highlight map[int]colorNRGBA // optional per-line background (errors)
	raw       []string
	sel       *lineSelect
}

func newYAMLView(a *App) *yamlView {
	v := &yamlView{}
	v.list.Axis = layout.Vertical
	v.sel = newLineSelect(a, func(i int) string { return v.raw[i] })
	return v
}

func (v *yamlView) set(text string) {
	if text == v.text && v.lines != nil {
		return
	}
	v.text = text
	raw := strings.Split(strings.TrimRight(text, "\n"), "\n")
	v.raw = raw
	v.sel.clear()
	v.lines = make([][]token, len(raw))
	for i, l := range raw {
		v.lines[i] = yamlTokens(l)
	}
}

func (v *yamlView) Layout(gtx C, th *material.Theme) D {
	numW := gtx.Dp(unit.Dp(10 + 7*len(strconv.Itoa(len(v.lines)))))
	v.sel.begin(gtx, len(v.lines))
	defer v.sel.end(&v.list)
	return material.List(th, &v.list).Layout(gtx, len(v.lines), func(gtx C, i int) D {
		return v.sel.item(gtx, i, func(gtx C) D { return v.line(gtx, th, i, numW) })
	})
}

func (v *yamlView) line(gtx C, th *material.Theme, i, numW int) D {
	const size = unit.Sp(12)
	{
		spans := make([]richtext.SpanStyle, 0, len(v.lines[i]))
		for _, t := range v.lines[i] {
			if t.text != "" {
				spans = append(spans, richtext.SpanStyle{Content: t.text, Font: fontMono, Size: size, Color: tokColor(t.kind)})
			}
		}
		if len(spans) == 0 {
			spans = append(spans, richtext.SpanStyle{Content: " ", Font: fontMono, Size: size})
		}
		return layout.Flex{}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				gtx.Constraints.Min.X, gtx.Constraints.Max.X = numW, numW
				return layout.NE.Layout(gtx, func(gtx C) D {
					return layout.Inset{Right: 8}.Layout(gtx, func(gtx C) D {
						return lbl(gtx, th, fontMono, 11, withAlpha(pal.TextDim, 0xa0), strconv.Itoa(i+1))
					})
				})
			}),
			layout.Flexed(1, func(gtx C) D {
				d := richtext.Text(&v.rt, th.Shaper, spans...).Layout(gtx)
				if c, ok := v.highlight[i+1]; ok {
					// paint after measuring: overlay a translucent band
					fillRect(gtx, withAlpha(c, 0x30), image.Rect(0, 0, d.Size.X, d.Size.Y))
				}
				return d
			}),
		)
	}
}

var yamlLineRx = regexp.MustCompile(`line (\d+)`)

// validateManifest checks YAML syntax and identity fields against the
// original object. It returns an error message and the offending line (0 = n/a).
func validateManifest(original, edited string) (msg string, line int, warn string) {
	var obj map[string]any
	if err := yaml.Unmarshal([]byte(edited), &obj); err != nil {
		m := err.Error()
		if i := strings.Index(m, "yaml: "); i >= 0 {
			m = m[i+6:]
		}
		if mm := yamlLineRx.FindStringSubmatch(m); mm != nil {
			line, _ = strconv.Atoi(mm[1])
			m = strings.TrimPrefix(m, mm[0]+": ")
		}
		return m, line, ""
	}
	if obj == nil {
		return "empty manifest", 0, ""
	}
	var orig map[string]any
	_ = yaml.Unmarshal([]byte(original), &orig)
	ident := func(o map[string]any) (string, string, string, string) {
		md, _ := o["metadata"].(map[string]any)
		return fmt.Sprint(o["apiVersion"]), fmt.Sprint(o["kind"]), fmt.Sprint(md["name"]), fmt.Sprint(md["namespace"])
	}
	if orig != nil {
		a1, k1, n1, ns1 := ident(orig)
		a2, k2, n2, ns2 := ident(obj)
		switch {
		case k1 != k2:
			return fmt.Sprintf("kind changed (%s → %s)", k1, k2), 0, ""
		case n1 != n2 || ns1 != ns2:
			return "metadata.name/namespace changed: this would target another object", 0, ""
		case a1 != a2:
			warn = fmt.Sprintf("apiVersion changed (%s → %s)", a1, a2)
		}
	}
	return "", 0, warn
}

// ---- diff review ----

// DiffReview shows the changes of an apply, validates them with a
// server-side dry run, then applies through the context policy.
type DiffReview struct {
	app      *App
	title    string
	path     string
	lines    []diffLine
	add, del int
	done     bool
	dry      string // "", "running", "ok", or error
	apply    func()
	list     widget.List
	sel      *lineSelect

	applyBtn, cancelBtn widget.Clickable
}

// newDiffReview builds the review and starts the dry run.
func newDiffReview(a *App, title, path, gvr, before, after string, apply func()) *DiffReview {
	r := &DiffReview{app: a, title: title, path: path, apply: apply, dry: "running"}
	r.list.Axis = layout.Vertical
	r.sel = newLineSelect(a, func(i int) string {
		switch l := r.lines[i]; l.op {
		case opAdd:
			return "+" + l.text
		case opDel:
			return "-" + l.text
		case opGap:
			return "…"
		default:
			return " " + l.text
		}
	})
	full := lineDiff(strings.Split(strings.TrimRight(before, "\n"), "\n"), strings.Split(strings.TrimRight(after, "\n"), "\n"))
	r.add, r.del = diffStats(full)
	r.lines = collapse(full, 3)
	c := a.cluster
	go func() {
		err := c.ApplyDryRun(gvr, after)
		a.post(func() {
			if err != nil {
				r.dry = err.Error()
			} else {
				r.dry = "ok"
			}
		})
	}()
	return r
}

func (r *DiffReview) Done() bool { return r.done }

func (r *DiffReview) Layout(gtx C) D {
	a, th := r.app, r.app.th
	if r.cancelBtn.Clicked(gtx) {
		r.done = true
	}
	if r.applyBtn.Clicked(gtx) && r.dry == "ok" && (r.add+r.del) > 0 {
		r.done = true
		if a.protected() {
			a.overlay = a.guard(newConfirm(a, "Apply", fmt.Sprintf("Apply %d additions and %d deletions to %s?", r.add, r.del, r.path), true, func(bool) { r.apply() }), r.path)
		} else {
			r.apply()
		}
	}
	w := min(gtx.Dp(900), gtx.Constraints.Max.X-gtx.Dp(40))
	gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
	return rborder(gtx, pal.Border, pal.Panel, 10, func(gtx C) D {
		return inset(16).Layout(gtx, func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx C) D { return iconText(gtx, th, icEdit, 16, pal.TextBright, r.title) }),
						layout.Rigid(layout.Spacer{Width: 10}.Layout),
						layout.Rigid(func(gtx C) D { return badge(gtx, th, fmt.Sprintf("+%d", r.add), pal.Ok, withAlpha(pal.Ok, 0x26)) }),
						layout.Rigid(layout.Spacer{Width: 4}.Layout),
						layout.Rigid(func(gtx C) D { return badge(gtx, th, fmt.Sprintf("−%d", r.del), pal.Err, withAlpha(pal.Err, 0x26)) }),
						layout.Flexed(1, layout.Spacer{}.Layout),
						layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontMono, 11.5, pal.TextDim, r.path) }),
					)
				}),
				layout.Rigid(layout.Spacer{Height: 10}.Layout),
				layout.Rigid(func(gtx C) D {
					gtx.Constraints.Max.Y = min(gtx.Constraints.Max.Y-gtx.Dp(120), gtx.Dp(520))
					gtx.Constraints.Min.Y = 0
					return rbg(gtx, pal.CodeBg, 6, func(gtx C) D {
						gtx.Constraints.Min.X = gtx.Constraints.Max.X
						if r.add+r.del == 0 {
							return inset(16).Layout(gtx, func(gtx C) D {
								return lbl(gtx, th, fontSans, 13, pal.TextDim, "No changes.")
							})
						}
						return inset(6).Layout(gtx, func(gtx C) D {
							r.sel.begin(gtx, len(r.lines))
							defer r.sel.end(&r.list)
							return material.List(th, &r.list).Layout(gtx, len(r.lines), func(gtx C, i int) D {
								return r.sel.item(gtx, i, func(gtx C) D { return r.layoutLine(gtx, r.lines[i]) })
							})
						})
					})
				}),
				layout.Rigid(layout.Spacer{Height: 10}.Layout),
				layout.Rigid(func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, func(gtx C) D {
							switch r.dry {
							case "running":
								return iconText(gtx, th, icRestart, 12, pal.Info, "Validating with a server-side dry run…")
							case "ok":
								return iconText(gtx, th, icOK, 12, pal.Ok, "Server-side dry run passed")
							default:
								return a.selText(gtx, "diff/dry", fontSans, 12, pal.Err, "Dry run failed: "+r.dry, 4)
							}
						}),
						layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &r.cancelBtn, nil, "Back", pal.Text, pal.Hover) }),
						layout.Rigid(layout.Spacer{Width: 8}.Layout),
						layout.Rigid(func(gtx C) D {
							c := pal.Accent
							if r.dry != "ok" || r.add+r.del == 0 {
								c = pal.Border
							}
							return pillButton(gtx, th, &r.applyBtn, icStatusDone, "Apply", pal.OnAccent, c)
						}),
					)
				}),
			)
		})
	})
}

func (r *DiffReview) layoutLine(gtx C, l diffLine) D {
	th := r.app.th
	if l.op == opGap {
		return insetHV(8, 3).Layout(gtx, func(gtx C) D {
			return lbl(gtx, th, fontSans, 11, pal.TextDim, fmt.Sprintf("··· %d unchanged lines", l.oldNo))
		})
	}
	sign, c, bgc := " ", pal.Text, colorNRGBA{}
	switch l.op {
	case opAdd:
		sign, c, bgc = "+", pal.Ok, withAlpha(pal.Ok, 0x1c)
	case opDel:
		sign, c, bgc = "-", pal.Err, withAlpha(pal.Err, 0x1c)
	}
	num := func(n int) string {
		if n == 0 {
			return ""
		}
		return strconv.Itoa(n)
	}
	w := gtx.Constraints.Max.X
	rec := op.Record(gtx.Ops)
	d := layout.Flex{}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X, gtx.Constraints.Max.X = gtx.Dp(36), gtx.Dp(36)
			return layout.NE.Layout(gtx, func(gtx C) D { return lbl(gtx, th, fontMono, 10.5, pal.TextDim, num(l.oldNo)) })
		}),
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X, gtx.Constraints.Max.X = gtx.Dp(36), gtx.Dp(36)
			return layout.NE.Layout(gtx, func(gtx C) D { return lbl(gtx, th, fontMono, 10.5, pal.TextDim, num(l.newNo)) })
		}),
		layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: 8, Right: 6}.Layout(gtx, func(gtx C) D { return lbl(gtx, th, fontMono, 12, c, sign) })
		}),
		layout.Flexed(1, func(gtx C) D { return lbl(gtx, th, fontMono, 12, c, l.text) }),
	)
	call := rec.Stop()
	if bgc.A > 0 {
		fillRect(gtx, bgc, image.Rect(0, 0, w, d.Size.Y))
	}
	call.Add(gtx.Ops)
	return d
}
