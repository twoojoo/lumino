package ui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/giovanni/lumino/internal/ai"
	"github.com/giovanni/lumino/internal/backend"
)

type chatKind int

const (
	chatUser chatKind = iota
	chatAssistant
	chatTool
	chatProposal
	chatError
	chatNotice
)

const (
	propPending = iota
	propRunning
	propDone
	propFailed
	propRejected
)

// chatItem is one rendered entry of the conversation.
type chatItem struct {
	kind     chatKind
	text     string
	blocks   []mdBlock
	dirty    bool
	tool     *ai.ToolRun
	running  bool
	prop     *ai.Proposal
	cluster  *backend.Cluster // where the proposal was made
	state    int
	result   string
	expanded bool
	md       mdView

	toggle, approve, reject, copyBtn, diffBtn widget.Clickable
}

func (it *chatItem) md2() []mdBlock {
	if it.dirty || it.blocks == nil {
		it.blocks, it.dirty = parseMarkdown(it.text), false
	}
	return it.blocks
}

// AIPanel is the right-hand cluster assistant.
type AIPanel struct {
	app   *App
	open  bool
	cfg   *ai.Config
	agent *ai.Agent
	agCtx string // kube context the agent belongs to

	items   []*chatItem
	cur     *chatItem
	list    widget.List
	input   widget.Editor
	running bool
	cancel  context.CancelFunc
	runSeq  int      // events of older runs (stopped, new chat, other context) are dropped
	notes   []string // results of approved actions, sent with the next message
	attach  bool

	sendBtn, stopBtn, newBtn, setBtn, closeBtn, detachBtn widget.Clickable
	suggest                                               [4]widget.Clickable
}

var suggestions = [4]string{
	"Why are pods failing in this namespace?",
	"Summarize the health of the cluster",
	"Explain the selected resource and check for problems",
	"Which workloads have no resource limits?",
}

func newAIPanel(a *App) *AIPanel {
	p := AIPanel{app: a, cfg: ai.LoadConfig(), attach: true}
	p.list.Axis = layout.Vertical
	p.list.ScrollToEnd = true
	p.input.Submit = true
	return &p
}

func (p *AIPanel) focused(gtx C) bool { return p.open && gtx.Focused(&p.input) }

func (p *AIPanel) toggle(gtx C) {
	p.open = !p.open
	if p.open {
		gtx.Execute(key.FocusCmd{Tag: &p.input})
	}
}

func (p *AIPanel) reset() {
	if p.cancel != nil {
		p.cancel()
	}
	p.runSeq++
	p.items, p.cur, p.agent, p.notes, p.running = nil, nil, nil, nil, false
	p.attach = true
}

// selection returns the resource currently selected in the UI.
func (p *AIPanel) selection() (gvr, path string) {
	a := p.app
	if a.page == xrayPage || a.page == overviewGVR || a.view.gvr == "" {
		return "", ""
	}
	if r, ok := a.view.selected(); ok {
		return a.view.gvr, r.ID
	}
	return "", ""
}

func (p *AIPanel) contextInfo() ai.ContextInfo {
	a := p.app
	ci := ai.ContextInfo{Namespace: a.ns, View: a.pageTitleSafe()}
	if a.cluster != nil {
		ci.KubeContext, ci.K8sVersion = a.cluster.Context, a.cluster.Version()
	}
	if gvr, path := p.selection(); gvr != "" && p.attach {
		ci.Selected = titleFor(gvr) + " (" + gvr + ") " + path
	}
	return ci
}

func (p *AIPanel) add(it *chatItem) *chatItem {
	it.md.app, it.md.id = p.app, fmt.Sprintf("ai/%p/md", it)
	it.dirty = true
	p.items = append(p.items, it)
	return it
}

func (p *AIPanel) send(text string) {
	a := p.app
	text = strings.TrimSpace(text)
	if text == "" || p.running {
		return
	}
	if a.cluster == nil {
		p.add(&chatItem{kind: chatNotice, text: "Connect to a cluster first."})
		return
	}
	if !p.cfg.ContextEnabled(a.cluster.Context) {
		p.add(&chatItem{kind: chatNotice, text: "The assistant is disabled for context **" + a.cluster.Context + "**. Enable it in settings."})
		return
	}
	prov, model, err := p.cfg.Build()
	if err != nil {
		p.add(&chatItem{kind: chatError, text: err.Error() + " — open settings (gear icon)."})
		return
	}
	if p.agent == nil || p.agCtx != a.cluster.Context {
		p.agent = &ai.Agent{Tools: &ai.Tools{}}
		p.agCtx = a.cluster.Context
	}
	p.agent.Provider, p.agent.Model = prov, model
	p.agent.Tools.Cluster, p.agent.Tools.Namespace = a.cluster, a.ns

	p.add(&chatItem{kind: chatUser, text: text})
	if len(p.notes) > 0 {
		text = "[Since my last message: " + strings.Join(p.notes, "; ") + "]\n\n" + text
		p.notes = nil
	}
	p.input.SetText("")
	p.running = true
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.runSeq++
	ag, ci, seq, c := p.agent, p.contextInfo(), p.runSeq, a.cluster
	go ag.Run(ctx, ci, text, func(e ai.Event) {
		a.post(func() {
			if seq == p.runSeq && c == a.cluster {
				p.onEvent(e)
			}
		})
	})
}

func (p *AIPanel) onEvent(e ai.Event) {
	switch e.Kind {
	case ai.EvText:
		if p.cur == nil {
			p.cur = p.add(&chatItem{kind: chatAssistant})
		}
		p.cur.text += e.Text
		p.cur.dirty = true
	case ai.EvTurnEnd:
		if p.cur == nil && strings.TrimSpace(e.Text) != "" {
			p.cur = p.add(&chatItem{kind: chatAssistant})
		}
		if p.cur != nil {
			if strings.TrimSpace(e.Text) != "" {
				p.cur.text = e.Text
			}
			p.cur.dirty = true
			if strings.TrimSpace(p.cur.text) == "" {
				p.items = slices.DeleteFunc(p.items, func(it *chatItem) bool { return it == p.cur })
			}
		}
		p.cur = nil
	case ai.EvToolStart:
		p.add(&chatItem{kind: chatTool, tool: e.Tool, running: true})
	case ai.EvToolEnd:
		for i := len(p.items) - 1; i >= 0; i-- {
			if it := p.items[i]; it.kind == chatTool && it.tool.ID == e.Tool.ID {
				it.tool, it.running = e.Tool, false
				break
			}
		}
	case ai.EvProposal:
		p.add(&chatItem{kind: chatProposal, prop: e.Proposal, cluster: p.app.cluster})
	case ai.EvError:
		p.running, p.cur = false, nil
		p.add(&chatItem{kind: chatError, text: e.Err.Error()})
	case ai.EvDone:
		p.running, p.cur = false, nil // a stopped answer must not receive the next one
	}
	p.app.invalidate()
}

func (p *AIPanel) stop() {
	if p.cancel != nil {
		p.cancel()
	}
	// input stays locked until the run reports EvDone: a new message must
	// not start a second run on the same agent history
}

// execute runs an approved proposal through the regular backend actions.
func (p *AIPanel) execute(it *chatItem) {
	a, c, pr := p.app, p.app.cluster, it.prop
	if c == nil {
		return
	}
	if it.cluster != c { // never run a proposal on another cluster than proposed
		it.state, it.result = propFailed, "context changed since the proposal"
		a.flash("Proposal made for another context: not executed", true)
		return
	}
	it.state = propRunning
	go func() {
		var err error
		path := pr.Path()
		switch pr.Action {
		case "scale":
			err = c.Scale(pr.GVR, path, *pr.Replicas)
		case "restart":
			err = c.Restart(pr.GVR, path)
		case "delete":
			err = c.Delete(pr.GVR, path, false)
		case "apply":
			err = c.Apply(pr.GVR, pr.Manifest)
		case "cordon", "uncordon":
			err = c.Cordon(pr.Name, pr.Action == "cordon")
		default:
			err = fmt.Errorf("unsupported action %q", pr.Action)
		}
		a.post(func() {
			desc := proposalTitle(pr)
			if err != nil {
				it.state, it.result = propFailed, err.Error()
				p.notes = append(p.notes, "the user approved \""+desc+"\" but it failed: "+err.Error())
				a.flash(desc+" failed: "+err.Error(), true)
				return
			}
			it.state, it.result = propDone, "done"
			p.notes = append(p.notes, "the user approved and executed \""+desc+"\" successfully")
			a.flash(desc+" — done", false)
		})
	}()
}

func proposalTitle(pr *ai.Proposal) string {
	t := strings.ToUpper(pr.Action[:1]) + pr.Action[1:] + " " + singular(strings.ToLower(titleFor(pr.GVR))) + " " + pr.Path()
	if pr.Action == "scale" && pr.Replicas != nil {
		t += fmt.Sprintf(" to %d", *pr.Replicas)
	}
	return t
}

// ---- layout ----

func (p *AIPanel) Layout(gtx C) D {
	a := p.app
	th := a.th
	if p.closeBtn.Clicked(gtx) {
		p.open = false
		return D{}
	}
	if p.newBtn.Clicked(gtx) {
		p.reset()
	}
	if p.setBtn.Clicked(gtx) {
		a.overlay = newAISettings(a, p)
	}
	if p.stopBtn.Clicked(gtx) {
		p.stop()
	}
	if p.sendBtn.Clicked(gtx) {
		p.send(p.input.Text())
	}
	if p.detachBtn.Clicked(gtx) {
		p.attach = !p.attach
	}
	for i := range p.suggest {
		if p.suggest[i].Clicked(gtx) {
			p.send(suggestions[i])
		}
	}
	for {
		ev, ok := p.input.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			p.send(p.input.Text())
		}
	}

	w := gtx.Dp(420)
	gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
	gtx.Constraints.Min.Y = gtx.Constraints.Max.Y
	fill(gtx, pal.Sidebar)
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(p.layoutHeader),
		layout.Rigid(func(gtx C) D { return hline(gtx, pal.Border) }),
		layout.Flexed(1, func(gtx C) D {
			if len(p.items) == 0 {
				return p.layoutEmpty(gtx)
			}
			return layout.Inset{Left: 12, Right: 4}.Layout(gtx, func(gtx C) D {
				return material.List(th, &p.list).Layout(gtx, len(p.items), func(gtx C, i int) D {
					return layout.Inset{Top: 6, Bottom: 6, Right: 8}.Layout(gtx, func(gtx C) D {
						return p.layoutItem(gtx, p.items[i])
					})
				})
			})
		}),
		layout.Rigid(func(gtx C) D { return hline(gtx, pal.Border) }),
		layout.Rigid(p.layoutInput),
	)
}

func (p *AIPanel) layoutHeader(gtx C) D {
	th := p.app.th
	pr, pc := p.cfg.Current()
	model := pc.Model
	if model == "" {
		model = "choose model"
	}
	return insetHV(12, 9).Layout(gtx, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return rbg(gtx, withAlpha(pal.Accent, 0x30), 7, func(gtx C) D {
					return inset(5).Layout(gtx, func(gtx C) D { return mdi(gtx, icAI, pal.Accent, 18) })
				})
			}),
			layout.Rigid(layout.Spacer{Width: 9}.Layout),
			layout.Flexed(1, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontSans, 14, pal.TextBright, "Assistant") }),
					layout.Rigid(func(gtx C) D {
						return lbl(gtx, th, fontSans, 11, pal.TextDim, pr.Name+" · "+model)
					}),
				)
			}),
			layout.Rigid(func(gtx C) D { return iconOnly(gtx, &p.newBtn, icNewChat, 16) }),
			layout.Rigid(func(gtx C) D { return iconOnly(gtx, &p.setBtn, icSettings, 16) }),
			layout.Rigid(func(gtx C) D { return iconOnly(gtx, &p.closeBtn, icClose, 16) }),
		)
	})
}

func (p *AIPanel) layoutEmpty(gtx C) D {
	th := p.app.th
	return inset(18).Layout(gtx, func(gtx C) D {
		var ch []layout.FlexChild
		ch = append(ch,
			layout.Rigid(func(gtx C) D { return mdi(gtx, icAI, withAlpha(pal.Accent, 0xb0), 36) }),
			layout.Rigid(layout.Spacer{Height: 10}.Layout),
			layout.Rigid(func(gtx C) D {
				return lbl(gtx, th, fontSans, 15, pal.TextBright, "Ask about your cluster")
			}),
			layout.Rigid(func(gtx C) D {
				l := material.Body2(th, "The assistant reads resources, events, logs and XRay trees. It never changes anything by itself: actions are proposed for you to approve. Secret values are redacted before anything is sent.")
				l.Color = pal.TextDim
				return layout.Inset{Top: 6, Bottom: 16}.Layout(gtx, l.Layout)
			}),
		)
		for i := range suggestions {
			i := i
			ch = append(ch, layout.Rigid(func(gtx C) D {
				return layout.Inset{Bottom: 6}.Layout(gtx, func(gtx C) D {
					return p.suggest[i].Layout(gtx, func(gtx C) D {
						b := pal.Border
						if p.suggest[i].Hovered() {
							b = pal.TextDim
						}
						return rborder(gtx, b, pal.Panel, 8, func(gtx C) D {
							gtx.Constraints.Min.X = gtx.Constraints.Max.X
							return insetHV(10, 8).Layout(gtx, func(gtx C) D {
								return iconText(gtx, th, icIdea, 12, pal.Text, suggestions[i])
							})
						})
					})
				})
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, ch...)
	})
}

func (p *AIPanel) layoutItem(gtx C, it *chatItem) D {
	th := p.app.th
	copyFn := func(s string) {
		copyText(gtx, s)
		p.app.flash("Copied to clipboard", false)
	}
	switch it.kind {
	case chatUser:
		return layout.E.Layout(gtx, func(gtx C) D {
			gtx.Constraints.Max.X = gtx.Constraints.Max.X * 85 / 100
			return rbg(gtx, pal.Selected, 10, func(gtx C) D {
				return insetHV(11, 8).Layout(gtx, func(gtx C) D {
					return p.app.selText(gtx, fmt.Sprintf("ai/%p/user", it), fontSans, 13, pal.TextBright, it.text, 0)
				})
			})
		})
	case chatAssistant:
		if it.copyBtn.Clicked(gtx) {
			copyFn(it.text)
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return it.md.layout(gtx, th, it.md2(), pal.Text, copyFn) }),
			layout.Rigid(func(gtx C) D {
				if p.running && p.cur == it {
					return D{}
				}
				return layout.W.Layout(gtx, func(gtx C) D { return iconOnly(gtx, &it.copyBtn, icCopyText, 12) })
			}),
		)
	case chatTool:
		return p.layoutTool(gtx, it)
	case chatProposal:
		return p.layoutProposal(gtx, it)
	case chatError:
		return rbg(gtx, withAlpha(pal.Err, 0x22), 8, func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return inset(10).Layout(gtx, func(gtx C) D {
				return layout.Flex{}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return mdi(gtx, icError, pal.Err, 16) }),
					layout.Rigid(layout.Spacer{Width: 8}.Layout),
					layout.Flexed(1, func(gtx C) D {
						return p.app.selText(gtx, fmt.Sprintf("ai/%p/err", it), fontSans, 12.5, lighten(pal.Err, 0.3), it.text, 0)
					}),
				)
			})
		})
	default:
		return rbg(gtx, pal.Panel, 8, func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return inset(10).Layout(gtx, func(gtx C) D { return it.md.layout(gtx, th, it.md2(), pal.TextDim, copyFn) })
		})
	}
}

func (p *AIPanel) layoutTool(gtx C, it *chatItem) D {
	th := p.app.th
	if it.toggle.Clicked(gtx) && !it.running {
		it.expanded = !it.expanded
	}
	t := it.tool
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return it.toggle.Layout(gtx, func(gtx C) D {
				b := pal.Panel
				if it.toggle.Hovered() {
					b = pal.Hover
				}
				return rbg(gtx, b, 7, func(gtx C) D {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return insetHV(9, 6).Layout(gtx, func(gtx C) D {
						c, ic := pal.TextDim, icTool
						status := ""
						switch {
						case it.running:
							status, c = "running…", pal.Info
						case t.IsError:
							status, c, ic = "failed", pal.Err, icError
						default:
							status = fmt.Sprintf("%.1fs", t.Duration.Seconds())
						}
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx C) D { return mdi(gtx, ic, c, 14) }),
							layout.Rigid(layout.Spacer{Width: 7}.Layout),
							layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontMono, 11.5, pal.Text, t.Name) }),
							layout.Rigid(layout.Spacer{Width: 6}.Layout),
							layout.Flexed(1, func(gtx C) D { return lbl(gtx, th, fontMono, 11, pal.TextDim, t.Args) }),
							layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontSans, 11, c, status) }),
							layout.Rigid(func(gtx C) D {
								if it.running {
									return D{}
								}
								return mdi(gtx, chevronIcon(!it.expanded), pal.TextDim, 14)
							}),
						)
					})
				})
			})
		}),
		layout.Rigid(func(gtx C) D {
			if !it.expanded {
				return D{}
			}
			out := t.Output
			if len(out) > 4000 {
				out = out[:4000] + "\n…"
			}
			return layout.Inset{Top: 4}.Layout(gtx, func(gtx C) D {
				return rbg(gtx, pal.CodeBg, 6, func(gtx C) D {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return inset(8).Layout(gtx, func(gtx C) D {
						return p.app.selText(gtx, fmt.Sprintf("ai/%p/out", it), fontMono, 11, pal.TextDim, out, 40)
					})
				})
			})
		}),
	)
}

func (p *AIPanel) layoutProposal(gtx C, it *chatItem) D {
	a := p.app
	th := a.th
	pr := it.prop
	if it.approve.Clicked(gtx) && it.state == propPending && a.allowMutation("Approve") {
		danger := pr.Action == "delete" || pr.Action == "apply" || pr.Action == "cordon"
		a.overlay = a.guard(newConfirm(a, "Approve", proposalTitle(pr)+"?", danger, func(bool) { p.execute(it) }), pr.Path())
	}
	if it.diffBtn.Clicked(gtx) && it.state == propPending && pr.Action == "apply" && a.allowMutation("Apply") {
		c := a.cluster
		go func() {
			live, err := c.YAML(pr.GVR, pr.Path())
			if err != nil {
				live = "" // new object: everything is an addition
			}
			a.post(func() {
				a.overlay = newDiffReview(a, "Review proposed manifest", pr.Path(), pr.GVR, live, pr.Manifest, func() { p.execute(it) })
			})
		}()
	}
	if it.reject.Clicked(gtx) && it.state == propPending {
		it.state = propRejected
		p.notes = append(p.notes, "the user rejected \""+proposalTitle(pr)+"\"")
	}
	accent := pal.Warn
	if pr.Action == "delete" {
		accent = pal.Err
	}
	return rborder(gtx, withAlpha(accent, 0x90), pal.Panel, 9, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return inset(11).Layout(gtx, func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx C) D { return mdi(gtx, iconFor(pr.GVR), accent, 16) }),
						layout.Rigid(layout.Spacer{Width: 8}.Layout),
						layout.Flexed(1, func(gtx C) D { return lbl(gtx, th, fontSans, 13, pal.TextBright, proposalTitle(pr)) }),
					)
				}),
				layout.Rigid(func(gtx C) D {
					if pr.Reason == "" {
						return D{}
					}
					return layout.Inset{Top: 6}.Layout(gtx, func(gtx C) D {
						return p.app.selText(gtx, fmt.Sprintf("ai/%p/reason", it), fontSans, 12, pal.Text, pr.Reason, 0)
					})
				}),
				layout.Rigid(func(gtx C) D {
					if pr.Manifest == "" {
						return D{}
					}
					return layout.Inset{Top: 6}.Layout(gtx, func(gtx C) D {
						return rbg(gtx, pal.CodeBg, 6, func(gtx C) D {
							gtx.Constraints.Min.X = gtx.Constraints.Max.X
							return inset(8).Layout(gtx, func(gtx C) D {
								return p.app.selText(gtx, fmt.Sprintf("ai/%p/manifest", it), fontMono, 11, pal.Text, pr.Manifest, 18)
							})
						})
					})
				}),
				layout.Rigid(layout.Spacer{Height: 9}.Layout),
				layout.Rigid(func(gtx C) D {
					switch it.state {
					case propPending:
						return layout.Flex{}.Layout(gtx,
							layout.Rigid(func(gtx C) D {
								if pr.Action == "apply" {
									return pillButton(gtx, th, &it.diffBtn, icEdit, "Review diff", pal.OnAccent, accent)
								}
								return pillButton(gtx, th, &it.approve, icOK, "Approve", pal.TextBright, accent)
							}),
							layout.Rigid(layout.Spacer{Width: 6}.Layout),
							layout.Rigid(func(gtx C) D {
								return pillButton(gtx, th, &it.reject, icClose, "Reject", pal.Text, pal.Hover)
							}),
						)
					case propRunning:
						return lbl(gtx, th, fontSans, 12, pal.Info, "Executing…")
					case propDone:
						return iconText(gtx, th, icOK, 12, pal.Ok, "Approved and executed")
					case propFailed:
						return iconText(gtx, th, icError, 12, pal.Err, "Failed: "+it.result)
					default:
						return iconText(gtx, th, icClose, 12, pal.TextDim, "Rejected")
					}
				}),
			)
		})
	})
}

func (p *AIPanel) layoutInput(gtx C) D {
	th := p.app.th
	return inset(10).Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				gvr, path := p.selection()
				if gvr == "" {
					return D{}
				}
				return layout.Inset{Bottom: 6}.Layout(gtx, func(gtx C) D {
					return p.detachBtn.Layout(gtx, func(gtx C) D {
						c, txt := pal.Text, path
						bgc := withAlpha(pal.Accent, 0x26)
						if !p.attach {
							c, bgc, txt = pal.TextDim, pal.Panel, path+" (not attached)"
						}
						return rbg(gtx, bgc, 6, func(gtx C) D {
							return insetHV(8, 3).Layout(gtx, func(gtx C) D {
								return iconText(gtx, th, iconFor(gvr), 11, c, txt)
							})
						})
					})
				})
			}),
			layout.Rigid(func(gtx C) D {
				b := pal.Border
				if gtx.Focused(&p.input) {
					b = pal.Accent
				}
				return rborder(gtx, b, pal.Bg, 9, func(gtx C) D {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return inset(8).Layout(gtx, func(gtx C) D {
						return layout.Flex{Alignment: layout.End}.Layout(gtx,
							layout.Flexed(1, func(gtx C) D {
								gtx.Constraints.Max.Y = gtx.Dp(120)
								e := material.Editor(th, &p.input, "Ask about the cluster…  (Enter to send)")
								e.TextSize = 13
								e.Color, e.HintColor = pal.TextBright, pal.TextDim
								return layout.Inset{Top: 2, Bottom: 2, Left: 2}.Layout(gtx, e.Layout)
							}),
							layout.Rigid(layout.Spacer{Width: 6}.Layout),
							layout.Rigid(func(gtx C) D {
								if p.running {
									return pillButton(gtx, th, &p.stopBtn, icStop, "", pal.TextBright, pal.Err)
								}
								c := pal.Accent
								if strings.TrimSpace(p.input.Text()) == "" {
									c = pal.Hover
								}
								return pillButton(gtx, th, &p.sendBtn, icSend, "", pal.TextBright, c)
							}),
						)
					})
				})
			}),
			layout.Rigid(func(gtx C) D {
				if !p.running {
					return D{}
				}
				return layout.Inset{Top: 5}.Layout(gtx, func(gtx C) D {
					dots := strings.Repeat("•", int(time.Now().UnixMilli()/400%4))
					p.app.invalidate()
					return lbl(gtx, th, fontSans, 11, pal.TextDim, "Working"+dots)
				})
			}),
		)
	})
}

// ---- settings ----

// AISettings edits provider, model, endpoint and key.
type AISettings struct {
	app      *App
	panel    *AIPanel
	done     bool
	sel      string
	model    widget.Editor
	baseURL  widget.Editor
	apiKey   widget.Editor
	models   []string
	loading  bool
	err      string
	ctxOn    bool
	provBtns map[string]*widget.Clickable
	modelBtn []widget.Clickable
	list     widget.List
	mlist    widget.List

	loadBtn, saveBtn, cancelBtn, ctxBtn widget.Clickable
}

func newAISettings(a *App, p *AIPanel) *AISettings {
	s := AISettings{app: a, panel: p, sel: p.cfg.Provider, provBtns: map[string]*widget.Clickable{}, ctxOn: true}
	s.model.SingleLine, s.baseURL.SingleLine, s.apiKey.SingleLine = true, true, true
	s.apiKey.Mask = '•'
	s.list.Axis, s.mlist.Axis = layout.Vertical, layout.Vertical
	if a.cluster != nil {
		s.ctxOn = p.cfg.ContextEnabled(a.cluster.Context)
	}
	s.load(s.sel)
	return &s
}

func (s *AISettings) Done() bool { return s.done }

func (s *AISettings) load(id string) {
	s.sel = id
	pr, _ := ai.PresetByID(id)
	pc := s.panel.cfg.Providers[id]
	m := pc.Model
	if m == "" {
		m = pr.Model
	}
	u := pc.BaseURL
	if u == "" {
		u = pr.BaseURL
	}
	s.model.SetText(m)
	s.baseURL.SetText(u)
	s.apiKey.SetText(pc.APIKey)
	s.models, s.err = nil, ""
}

func (s *AISettings) current() (ai.Preset, ai.ProviderConfig) {
	pr, _ := ai.PresetByID(s.sel)
	pc := ai.ProviderConfig{Model: strings.TrimSpace(s.model.Text()), BaseURL: strings.TrimSpace(s.baseURL.Text()), APIKey: strings.TrimSpace(s.apiKey.Text())}
	if pc.BaseURL == pr.BaseURL {
		pc.BaseURL = ""
	}
	return pr, pc
}

func (s *AISettings) fetchModels() {
	pr, pc := s.current()
	tmp := *s.panel.cfg
	tmp.Providers = map[string]ai.ProviderConfig{}
	for k, v := range s.panel.cfg.Providers {
		tmp.Providers[k] = v
	}
	if pc.Model == "" {
		pc.Model = "probe"
	}
	tmp.Set(pr.ID, pc)
	prov, _, err := tmp.Build()
	if err != nil {
		s.err = err.Error()
		return
	}
	s.loading, s.err = true, ""
	a := s.app
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		mm, err := prov.Models(ctx)
		a.post(func() {
			s.loading = false
			if err != nil {
				s.err = "could not list models: " + err.Error()
				return
			}
			s.models = mm
			if len(mm) == 0 {
				s.err = "no models returned"
			}
		})
	}()
}

func (s *AISettings) Layout(gtx C) D {
	a := s.app
	th := a.th
	for _, pr := range ai.Presets {
		if cl := s.provBtns[pr.ID]; cl != nil && cl.Clicked(gtx) {
			s.load(pr.ID)
		}
	}
	if s.loadBtn.Clicked(gtx) {
		s.fetchModels()
	}
	for i := range s.modelBtn {
		if i < len(s.models) && s.modelBtn[i].Clicked(gtx) {
			s.model.SetText(s.models[i])
		}
	}
	if s.ctxBtn.Clicked(gtx) {
		s.ctxOn = !s.ctxOn
	}
	if s.cancelBtn.Clicked(gtx) {
		s.done = true
	}
	if s.saveBtn.Clicked(gtx) {
		pr, pc := s.current()
		cfg := s.panel.cfg
		cfg.Set(pr.ID, pc)
		if a.cluster != nil {
			cfg.SetContextEnabled(a.cluster.Context, s.ctxOn)
		}
		if err := cfg.Save(); err != nil {
			s.err = err.Error()
		} else {
			s.done = true
			gtx.Execute(key.FocusCmd{})
			a.flash("Assistant settings saved ("+ai.ConfigPath()+")", false)
		}
	}
	if len(s.modelBtn) < len(s.models) {
		s.modelBtn = make([]widget.Clickable, len(s.models))
	}

	w := min(gtx.Dp(680), gtx.Constraints.Max.X-gtx.Dp(40))
	gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
	pr, pcNow := s.current()
	return rborder(gtx, pal.Border, pal.Panel, 10, func(gtx C) D {
		return inset(18).Layout(gtx, func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					return iconText(gtx, th, icSettings, 16, pal.TextBright, "Assistant settings")
				}),
				layout.Rigid(layout.Spacer{Height: 14}.Layout),
				layout.Rigid(func(gtx C) D {
					return layout.Flex{}.Layout(gtx,
						// provider list
						layout.Rigid(func(gtx C) D {
							gtx.Constraints.Min.X, gtx.Constraints.Max.X = gtx.Dp(230), gtx.Dp(230)
							var ch []layout.FlexChild
							for _, p := range ai.Presets {
								p := p
								cl := s.provBtns[p.ID]
								if cl == nil {
									cl = new(widget.Clickable)
									s.provBtns[p.ID] = cl
								}
								ch = append(ch, layout.Rigid(func(gtx C) D {
									return cl.Layout(gtx, func(gtx C) D {
										b := colorNRGBA{}
										c := pal.Text
										switch {
										case p.ID == s.sel:
											b, c = pal.Selected, pal.TextBright
										case cl.Hovered():
											b = pal.Hover
										}
										return rbg(gtx, b, 6, func(gtx C) D {
											gtx.Constraints.Min.X = gtx.Constraints.Max.X
											return insetHV(9, 6).Layout(gtx, func(gtx C) D {
												ic := icCluster
												if p.Local {
													ic = icNode
												}
												return iconText(gtx, th, ic, 12, c, p.Name)
											})
										})
									})
								}))
							}
							return layout.Flex{Axis: layout.Vertical}.Layout(gtx, ch...)
						}),
						layout.Rigid(layout.Spacer{Width: 16}.Layout),
						// fields
						layout.Flexed(1, func(gtx C) D {
							field := func(label string, ed *widget.Editor, hint string) layout.FlexChild {
								return layout.Rigid(func(gtx C) D {
									return layout.Inset{Bottom: 10}.Layout(gtx, func(gtx C) D {
										return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
											layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontSans, 11, pal.TextDim, label) }),
											layout.Rigid(layout.Spacer{Height: 4}.Layout),
											layout.Rigid(func(gtx C) D {
												b := pal.Border
												if gtx.Focused(ed) {
													b = pal.Accent
												}
												return rborder(gtx, b, pal.Bg, 6, func(gtx C) D {
													gtx.Constraints.Min.X = gtx.Constraints.Max.X
													return insetHV(8, 6).Layout(gtx, func(gtx C) D {
														e := material.Editor(th, ed, hint)
														e.TextSize, e.Color, e.HintColor = 12.5, pal.TextBright, pal.TextDim
														return e.Layout(gtx)
													})
												})
											}),
										)
									})
								})
							}
							items := []layout.FlexChild{
								layout.Rigid(func(gtx C) D {
									kind := "OpenAI-compatible API"
									if pr.Kind == ai.KindAnthropic {
										kind = "Anthropic API (official SDK)"
									}
									return layout.Inset{Bottom: 10}.Layout(gtx, func(gtx C) D {
										return lbl(gtx, th, fontSans, 12, pal.Text, pr.Name+" · "+kind)
									})
								}),
								field("MODEL", &s.model, "model id"),
								layout.Rigid(func(gtx C) D {
									return layout.Inset{Bottom: 8}.Layout(gtx, func(gtx C) D {
										txt := "Load available models"
										if s.loading {
											txt = "Loading…"
										}
										return pillButton(gtx, th, &s.loadBtn, icExpand, txt, pal.Text, pal.Hover)
									})
								}),
								layout.Rigid(func(gtx C) D {
									if len(s.models) == 0 {
										return D{}
									}
									gtx.Constraints.Max.Y = gtx.Dp(130)
									return layout.Inset{Bottom: 10}.Layout(gtx, func(gtx C) D {
										return material.List(th, &s.mlist).Layout(gtx, len(s.models), func(gtx C, i int) D {
											return s.modelBtn[i].Layout(gtx, func(gtx C) D {
												c := pal.Text
												if s.models[i] == strings.TrimSpace(s.model.Text()) {
													c = pal.Accent
												} else if s.modelBtn[i].Hovered() {
													c = pal.TextBright
												}
												return insetHV(6, 3).Layout(gtx, func(gtx C) D {
													return lbl(gtx, th, fontMono, 11.5, c, s.models[i])
												})
											})
										})
									})
								}),
							}
							if pr.Kind != ai.KindAnthropic || pr.ID == "anthropic" {
								items = append(items, field("BASE URL", &s.baseURL, "default endpoint"))
							}
							keyHint := "leave empty to use $" + pr.KeyEnv
							if pr.KeyEnv == "" {
								keyHint = "usually not needed for local servers"
							}
							items = append(items,
								field("API KEY (stored in "+shortPath(ai.ConfigPath())+", mode 600)", &s.apiKey, keyHint),
								layout.Rigid(func(gtx C) D {
									return lbl(gtx, th, fontSans, 11, pal.TextDim, "Key source: "+ai.KeySource(pr, pcNow))
								}),
							)
							return layout.Flex{Axis: layout.Vertical}.Layout(gtx, items...)
						}),
					)
				}),
				layout.Rigid(layout.Spacer{Height: 12}.Layout),
				layout.Rigid(func(gtx C) D {
					if a.cluster == nil {
						return D{}
					}
					return toggle(gtx, th, &s.ctxBtn, "Enable the assistant for context "+a.cluster.Context, s.ctxOn)
				}),
				layout.Rigid(func(gtx C) D {
					if s.err == "" {
						return D{}
					}
					return layout.Inset{Top: 8}.Layout(gtx, func(gtx C) D {
						return iconText(gtx, th, icError, 12, pal.Err, s.err)
					})
				}),
				layout.Rigid(layout.Spacer{Height: 14}.Layout),
				layout.Rigid(func(gtx C) D {
					return layout.Flex{}.Layout(gtx,
						layout.Flexed(1, func(gtx C) D {
							gtx.Constraints.Min.Y = gtx.Dp(28)
							return layout.W.Layout(gtx, func(gtx C) D {
								return lbl(gtx, th, fontSans, 11, pal.TextDim, "Data sent: resource summaries, events, logs (credentials redacted). Never secret values.")
							})
						}),
						layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &s.cancelBtn, nil, "Cancel", pal.Text, pal.Hover) }),
						layout.Rigid(layout.Spacer{Width: 8}.Layout),
						layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &s.saveBtn, icOK, "Save", pal.TextBright, pal.Accent) }),
					)
				}),
			)
		})
	})
}

func shortPath(p string) string {
	if i := strings.Index(p, "/.config/"); i >= 0 {
		return "~" + p[i:]
	}
	return p
}
