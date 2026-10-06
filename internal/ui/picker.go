package ui

import (
	"image"
	"strings"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// Picker is a filterable list overlay (namespaces, contexts, command palette).
type Picker struct {
	app       *App
	title     string
	items     []string
	matches   []int
	sel       int
	allowFree bool
	onPick    func(string)
	// onPickIndex, when set, receives the index (in items) of the chosen
	// item instead of its label: items with the same label stay distinct.
	// Free text (allowFree) still goes to onPick.
	onPickIndex func(int)
	exact       func(string) bool // free text accepted as-is when true
	icon        *widget.Icon      // item icon override
	itemIcons   []*widget.Icon    // per item (parallel to items), optional
	done        bool
	focused     bool
	lastQ       string

	editor widget.Editor
	list   widget.List
	tag    int
}

func newPicker(a *App, title string, items []string, allowFree bool, onPick func(string)) *Picker {
	p := Picker{app: a, title: title, items: items, allowFree: allowFree, onPick: onPick, lastQ: "\x00"}
	p.editor.SingleLine = true
	p.editor.Submit = true
	p.list.Axis = layout.Vertical
	return &p
}

func (p *Picker) Done() bool { return p.done }

func (p *Picker) refilter() {
	q := strings.ToLower(strings.TrimSpace(p.editor.Text()))
	if q == p.lastQ {
		return
	}
	p.lastQ = q
	p.matches = p.matches[:0]
	terms := strings.Fields(q)
	var prefix, rest []int
	for i, it := range p.items {
		l := strings.ToLower(it)
		ok := true
		for _, t := range terms {
			if !strings.Contains(l, t) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		if len(terms) > 0 && strings.HasPrefix(l, terms[0]) {
			prefix = append(prefix, i)
		} else {
			rest = append(rest, i)
		}
	}
	p.matches = append(append(p.matches, prefix...), rest...)
	p.sel = 0
	p.list.Position = layout.Position{}
}

func (p *Picker) pick(gtx C) {
	idx, val, ok := p.resolve()
	if !ok {
		return
	}
	p.done = true
	gtx.Execute(key.FocusCmd{})
	p.deliver(idx, val)
}

// resolve returns the chosen item index (-1 for free text) and label.
func (p *Picker) resolve() (idx int, val string, ok bool) {
	raw := strings.TrimSpace(p.editor.Text())
	switch {
	case p.allowFree && raw != "" && p.exact != nil && p.exact(raw):
		return -1, raw, true
	case p.sel >= 0 && p.sel < len(p.matches):
		idx = p.matches[p.sel]
		return idx, p.items[idx], true
	case p.allowFree:
		return -1, raw, true
	}
	return -1, "", false
}

// deliver calls onPickIndex for items (when set), onPick otherwise.
func (p *Picker) deliver(idx int, val string) {
	if idx >= 0 && p.onPickIndex != nil {
		p.onPickIndex(idx)
		return
	}
	if val != "" && p.onPick != nil {
		p.onPick(val)
	}
}

func (p *Picker) Layout(gtx C) D {
	th := p.app.th
	if !p.focused {
		gtx.Execute(key.FocusCmd{Tag: &p.editor})
		p.focused = true
	}
	for {
		ev, ok := gtx.Event(
			key.Filter{Focus: &p.editor, Name: key.NameUpArrow},
			key.Filter{Focus: &p.editor, Name: key.NameDownArrow},
			key.Filter{Focus: &p.editor, Name: key.NameTab},
		)
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			switch e.Name {
			case key.NameUpArrow:
				p.sel = max(0, p.sel-1)
			case key.NameDownArrow, key.NameTab:
				p.sel = min(len(p.matches)-1, p.sel+1)
			}
			p.list.ScrollTo(max(0, p.sel-5))
		}
	}
	for {
		ev, ok := p.editor.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			p.pick(gtx)
		}
	}
	p.refilter()

	w := min(gtx.Dp(640), gtx.Constraints.Max.X-gtx.Dp(40))
	gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
	return rborder(gtx, pal.Border, pal.Panel, 10, func(gtx C) D {
		return inset(1).Layout(gtx, func(gtx C) D {
			return layout.Inset{}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						return layout.Inset{Left: 14, Right: 14, Top: 10}.Layout(gtx, func(gtx C) D {
							return lbl(gtx, th, fontSans, 12, pal.TextDim, p.title)
						})
					}),
					layout.Rigid(func(gtx C) D {
						return insetHV(14, 10).Layout(gtx, func(gtx C) D {
							gtx.Constraints.Min.X = gtx.Constraints.Max.X
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(func(gtx C) D { return mdi(gtx, icSearch, pal.Accent, 20) }),
								layout.Rigid(layout.Spacer{Width: 10}.Layout),
								layout.Flexed(1, func(gtx C) D {
									e := material.Editor(th, &p.editor, "Type to filter…")
									e.TextSize = 16
									e.Color = pal.TextBright
									e.HintColor = pal.TextDim
									return e.Layout(gtx)
								}),
							)
						})
					}),
					layout.Rigid(func(gtx C) D { return hline(gtx, pal.Border) }),
					layout.Rigid(func(gtx C) D {
						if len(p.items) == 0 {
							return D{}
						}
						rh := gtx.Dp(32)
						n := min(len(p.matches), 14)
						gtx.Constraints.Max.Y = max(rh, n*rh)
						gtx.Constraints.Min.Y = gtx.Constraints.Max.Y
						area := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
						event.Op(gtx.Ops, &p.tag)
						pointer.CursorPointer.Add(gtx.Ops)
						for {
							ev, ok := gtx.Event(pointer.Filter{Target: &p.tag, Kinds: pointer.Press})
							if !ok {
								break
							}
							if pe, ok := ev.(pointer.Event); ok {
								idx := p.list.Position.First + (int(pe.Position.Y)+p.list.Position.Offset)/rh
								if idx < len(p.matches) {
									p.sel = idx
									p.pick(gtx)
								}
							}
						}
						d := material.List(th, &p.list).Layout(gtx, len(p.matches), func(gtx C, i int) D {
							sz := image.Pt(gtx.Constraints.Max.X, rh)
							if i == p.sel {
								fillRRect(gtx, pal.Selected, image.Rect(gtx.Dp(6), 0, sz.X-gtx.Dp(6), rh), gtx.Dp(5))
							}
							gtx.Constraints = layout.Exact(sz)
							return layout.Inset{Left: unit.Dp(16)}.Layout(gtx, func(gtx C) D {
								return layout.W.Layout(gtx, func(gtx C) D {
									c, ic := pal.Text, pal.TextDim
									if i == p.sel {
										c, ic = pal.TextBright, pal.Accent
									}
									item := p.items[p.matches[i]]
									title, sub, hasSub := strings.Cut(item, "  ·  ")
									return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
										layout.Rigid(func(gtx C) D {
											ico := p.icon
											if p.itemIcons != nil && p.itemIcons[p.matches[i]] != nil {
												ico = p.itemIcons[p.matches[i]]
											}
											if ico == nil {
												ico = pickerIcon(item)
											}
											return mdi(gtx, ico, ic, 16)
										}),
										layout.Rigid(layout.Spacer{Width: 10}.Layout),
										layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontSans, 13, c, title) }),
										layout.Rigid(func(gtx C) D {
											if !hasSub {
												return D{}
											}
											return layout.Inset{Left: 10}.Layout(gtx, func(gtx C) D {
												return lbl(gtx, th, fontMono, 11, pal.TextDim, sub)
											})
										}),
									)
								})
							})
						})
						area.Pop()
						return d
					}),
				)
			})
		})
	})
}

// Confirm is a yes/no modal with optional boolean options.
type Confirm struct {
	app        *App
	title, msg string
	danger     bool
	optLabels  []string
	opts       []bool
	optBtns    []widget.Clickable
	onOK       func(opts []bool)
	onCancel   func() // also when dismissed with Esc or a click outside
	okLabel    string
	done       bool
	ok, cancel widget.Clickable

	// protected contexts: the user must type typeName to enable OK
	typeName string
	prodCtx  string
	typed    widget.Editor
	focused  bool
}

func (c *Confirm) armed() bool {
	return c.typeName == "" || strings.TrimSpace(c.typed.Text()) == c.typeName
}

func (c *Confirm) accept() {
	if !c.armed() {
		return
	}
	c.done = true
	c.onCancel = nil
	c.onOK(c.opts)
}

// newConfirm is a yes/no dialog (onOK's argument is unused).
func newConfirm(a *App, title, msg string, danger bool, onOK func(bool)) *Confirm {
	return newConfirmOpts(a, title, msg, danger, nil, nil, func([]bool) { onOK(false) })
}

func newConfirmOpts(a *App, title, msg string, danger bool, labels []string, defaults []bool, onOK func([]bool)) *Confirm {
	return &Confirm{app: a, title: title, msg: msg, danger: danger, optLabels: labels,
		opts: append([]bool(nil), defaults...), optBtns: make([]widget.Clickable, len(labels)), onOK: onOK}
}

func (c *Confirm) Done() bool { return c.done }

// cancelled implements canceler.
func (c *Confirm) cancelled() {
	if c.onCancel != nil {
		f := c.onCancel
		c.onCancel = nil
		f()
	}
}

func (c *Confirm) Layout(gtx C) D {
	th := c.app.th
	for {
		ev, ok := gtx.Event(key.Filter{Name: key.NameReturn}, key.Filter{Name: key.NameEnter})
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			c.accept()
		}
	}
	if c.typeName != "" {
		if !c.focused {
			c.typed.SingleLine, c.typed.Submit = true, true
			gtx.Execute(key.FocusCmd{Tag: &c.typed})
			c.focused = true
		}
		for {
			ev, ok := c.typed.Update(gtx)
			if !ok {
				break
			}
			if _, ok := ev.(widget.SubmitEvent); ok {
				c.accept()
			}
		}
	}
	if c.ok.Clicked(gtx) {
		c.accept()
	}
	if c.cancel.Clicked(gtx) {
		c.done = true
		c.cancelled()
	}
	for i := range c.optBtns {
		if c.optBtns[i].Clicked(gtx) {
			c.opts[i] = !c.opts[i]
		}
	}
	w := min(gtx.Dp(460), gtx.Constraints.Max.X-gtx.Dp(40))
	gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
	return rborder(gtx, pal.Border, pal.Panel, 10, func(gtx C) D {
		return inset(1).Layout(gtx, func(gtx C) D {
			return layout.Inset{}.Layout(gtx, func(gtx C) D {
				return inset(18).Layout(gtx, func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							ic, col := icInfo, pal.Accent
							if c.danger {
								ic, col = icWarning, pal.Err
							}
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(func(gtx C) D {
									return rbg(gtx, withAlpha(col, 0x30), 18, func(gtx C) D {
										return inset(7).Layout(gtx, func(gtx C) D { return mdi(gtx, ic, col, 20) })
									})
								}),
								layout.Rigid(layout.Spacer{Width: 12}.Layout),
								layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontSans, 17, pal.TextBright, c.title) }),
							)
						}),
						layout.Rigid(layout.Spacer{Height: 10}.Layout),
						layout.Rigid(func(gtx C) D {
							l := material.Body1(th, c.msg)
							l.Color = pal.Text
							return l.Layout(gtx)
						}),
						layout.Rigid(func(gtx C) D {
							if c.typeName == "" {
								return D{}
							}
							return layout.Inset{Top: 12}.Layout(gtx, func(gtx C) D {
								return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
									layout.Rigid(func(gtx C) D {
										return rbg(gtx, withAlpha(pal.Err, 0x22), 6, func(gtx C) D {
											gtx.Constraints.Min.X = gtx.Constraints.Max.X
											return insetHV(9, 6).Layout(gtx, func(gtx C) D {
												return iconText(gtx, th, icWarning, 12, pal.Err, "Protected connection: "+c.prodCtx)
											})
										})
									}),
									layout.Rigid(layout.Spacer{Height: 10}.Layout),
									layout.Rigid(func(gtx C) D {
										return lbl(gtx, th, fontSans, 12, pal.Text, "Type "+c.typeName+" to confirm")
									}),
									layout.Rigid(layout.Spacer{Height: 4}.Layout),
									layout.Rigid(func(gtx C) D {
										b := pal.Border
										if c.armed() {
											b = pal.Ok
										} else if gtx.Focused(&c.typed) {
											b = pal.Err
										}
										return rborder(gtx, b, pal.Bg, 6, func(gtx C) D {
											gtx.Constraints.Min.X = gtx.Constraints.Max.X
											return insetHV(8, 6).Layout(gtx, func(gtx C) D {
												e := material.Editor(th, &c.typed, c.typeName)
												e.Font, e.TextSize, e.Color, e.HintColor = fontMono, 13, pal.TextBright, withAlpha(pal.TextDim, 0x80)
												return e.Layout(gtx)
											})
										})
									}),
								)
							})
						}),
						layout.Rigid(func(gtx C) D {
							if len(c.optLabels) == 0 {
								return D{}
							}
							var ch []layout.FlexChild
							for i := range c.optLabels {
								i := i
								ch = append(ch, layout.Rigid(func(gtx C) D {
									return toggle(gtx, th, &c.optBtns[i], c.optLabels[i], c.opts[i])
								}))
							}
							return layout.Inset{Top: 12}.Layout(gtx, func(gtx C) D {
								return layout.Flex{Axis: layout.Vertical}.Layout(gtx, ch...)
							})
						}),
						layout.Rigid(layout.Spacer{Height: 16}.Layout),
						layout.Rigid(func(gtx C) D {
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
								layout.Flexed(1, layout.Spacer{}.Layout),
								layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &c.cancel, nil, "Cancel", pal.Text, pal.Hover) }),
								layout.Rigid(layout.Spacer{Width: 8}.Layout),
								layout.Rigid(func(gtx C) D {
									col := pal.Accent
									if c.danger {
										col = pal.Err
									}
									if !c.armed() {
										col = pal.Border
									}
									lbl := c.okLabel
									if lbl == "" {
										lbl = c.title
									}
									return button(gtx, th, &c.ok, lbl, col)
								}),
							)
						}),
					)
				})
			})
		})
	})
}

// pickerIcon picks an icon for a picker entry ("title  ·  sub" items may
// carry an icon key in PickerIcons).
func pickerIcon(item string) *widget.Icon {
	return icCommand
}
