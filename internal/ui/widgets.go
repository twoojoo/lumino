package ui

import (
	"image"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/giovanni/lumino/internal/db"
)

// textField draws a bordered single-line editor.
func textField(gtx C, th *material.Theme, ed *widget.Editor, hint string, mono bool) D {
	ed.SingleLine = true
	b := pal.Border
	if gtx.Focused(ed) {
		b = pal.Accent
	}
	return rborder(gtx, b, pal.Bg, 6, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return insetHV(8, 6).Layout(gtx, func(gtx C) D {
			e := material.Editor(th, ed, hint)
			e.TextSize = 13
			if mono {
				e.Font = fontMono
			}
			e.Color, e.HintColor = pal.TextBright, withAlpha(pal.TextDim, 0xb0)
			e.SelectionColor = withAlpha(pal.Accent, 0x50)
			return e.Layout(gtx)
		})
	})
}

// textArea draws a bordered multi-line editor of a fixed height.
func textArea(gtx C, th *material.Theme, ed *widget.Editor, hint string, height unit.Dp) D {
	ed.SingleLine = false
	b := pal.Border
	if gtx.Focused(ed) {
		b = pal.Accent
	}
	h := gtx.Dp(height)
	return rborder(gtx, b, pal.Bg, 6, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		gtx.Constraints.Min.Y, gtx.Constraints.Max.Y = h, h
		return insetHV(8, 6).Layout(gtx, func(gtx C) D {
			e := material.Editor(th, ed, hint)
			e.TextSize, e.Font = 12.5, fontMono
			e.Color, e.HintColor = pal.TextBright, withAlpha(pal.TextDim, 0xb0)
			e.SelectionColor = withAlpha(pal.Accent, 0x50)
			return e.Layout(gtx)
		})
	})
}

// formRow lays out "label  [widget]" with a fixed label column.
func formRow(gtx C, th *material.Theme, label string, labelW unit.Dp, w layout.Widget) D {
	return layout.Inset{Bottom: 8}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				lw := gtx.Dp(labelW)
				gtx.Constraints.Min.X, gtx.Constraints.Max.X = lw, lw
				return lbl(gtx, th, fontSans, 12.5, pal.Text, label)
			}),
			layout.Flexed(1, w),
		)
	})
}

// toggle is a checkbox with a label.
func toggle(gtx C, th *material.Theme, cl *widget.Clickable, label string, on bool) D {
	return cl.Layout(gtx, func(gtx C) D {
		ic, c := icUnchecked, pal.TextDim
		if on {
			ic, c = icChecked, pal.Accent
		}
		tc := pal.Text
		if cl.Hovered() {
			tc = pal.TextBright
		}
		return insetHV(2, 4).Layout(gtx, func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return mdi(gtx, ic, c, 18) }),
				layout.Rigid(layout.Spacer{Width: 8}.Layout),
				layout.Flexed(1, func(gtx C) D {
					l := material.Body2(th, label)
					l.Color, l.TextSize = tc, 12.5
					return l.Layout(gtx)
				}),
			)
		})
	})
}

// segmented draws mutually exclusive choices; it returns the clicked index
// (or -1) through the clickables.
func segmented(gtx C, th *material.Theme, btns []widget.Clickable, labels []string, sel int) D {
	return rborder(gtx, pal.Border, pal.Bg, 7, func(gtx C) D {
		return inset(2).Layout(gtx, func(gtx C) D {
			var ch []layout.FlexChild
			for i := range labels {
				i := i
				ch = append(ch, layout.Rigid(func(gtx C) D {
					b, c := colorNRGBA{}, pal.TextDim
					if i == sel {
						b, c = pal.Selected, pal.TextBright
					}
					return pillButton(gtx, th, &btns[i], nil, labels[i], c, b)
				}))
			}
			return layout.Flex{}.Layout(gtx, ch...)
		})
	})
}

// sectionTitle is a small caps title inside dialogs.
func sectionTitle(gtx C, th *material.Theme, s string) D {
	return layout.Inset{Top: 12, Bottom: 6}.Layout(gtx, func(gtx C) D {
		return lbl(gtx, th, fontBold, 11, pal.TextDim, upper(s))
	})
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

// dialog draws a modal frame: header, scrollable body, footer.
type dialog struct {
	list  widget.List
	close widget.Clickable
}

func (d *dialog) layout(gtx C, th *material.Theme, width unit.Dp, ic *widget.Icon, title string, body, footer layout.Widget) D {
	d.list.Axis = layout.Vertical
	w := min(gtx.Dp(width), gtx.Constraints.Max.X-gtx.Dp(40))
	gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
	maxH := gtx.Constraints.Max.Y - gtx.Dp(20)
	return rborder(gtx, pal.Border, pal.Panel, 10, func(gtx C) D {
		return inset(1).Layout(gtx, func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					return layout.Inset{Left: 18, Right: 12, Top: 14, Bottom: 10}.Layout(gtx, func(gtx C) D {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx C) D {
								if ic == nil {
									return D{}
								}
								return layout.Inset{Right: 10}.Layout(gtx, func(gtx C) D { return mdi(gtx, ic, pal.Accent, 20) })
							}),
							layout.Flexed(1, func(gtx C) D { return lbl(gtx, th, fontSans, 16, pal.TextBright, title) }),
							layout.Rigid(func(gtx C) D { return iconOnly(gtx, &d.close, icClose, 16) }),
						)
					})
				}),
				layout.Rigid(func(gtx C) D { return hline(gtx, pal.Border) }),
				layout.Rigid(func(gtx C) D {
					gtx.Constraints.Max.Y = max(gtx.Dp(80), maxH-gtx.Dp(130))
					gtx.Constraints.Min.Y = 0
					return material.List(th, &d.list).Layout(gtx, 1, func(gtx C, _ int) D {
						return layout.Inset{Left: 18, Right: 18, Top: 12, Bottom: 8}.Layout(gtx, body)
					})
				}),
				layout.Rigid(func(gtx C) D {
					if footer == nil {
						return D{}
					}
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx C) D { return hline(gtx, pal.Border) }),
						layout.Rigid(func(gtx C) D {
							return layout.Inset{Left: 18, Right: 18, Top: 10, Bottom: 12}.Layout(gtx, footer)
						}),
					)
				}),
			)
		})
	})
}

// footerButtons right-aligns buttons; left is an optional widget on the left.
func footerButtons(left layout.Widget, buttons ...layout.Widget) layout.Widget {
	return func(gtx C) D {
		ch := []layout.FlexChild{layout.Flexed(1, func(gtx C) D {
			gtx.Constraints.Min.X = 0
			if left == nil {
				return D{Size: image.Pt(0, 1)}
			}
			return left(gtx)
		})}
		for i, b := range buttons {
			if i > 0 {
				ch = append(ch, layout.Rigid(layout.Spacer{Width: 8}.Layout))
			}
			ch = append(ch, layout.Rigid(b))
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, ch...)
	}
}

// ---- simple dialogs ----

// Message is an informational/error modal with an OK button.
type Message struct {
	app        *App
	title, msg string
	isErr      bool
	done       bool
	ok         widget.Clickable
	d          dialog
}

func newMessage(a *App, title, msg string, isErr bool) *Message {
	return &Message{app: a, title: title, msg: msg, isErr: isErr}
}

func (m *Message) Done() bool { return m.done }

func (m *Message) Layout(gtx C) D {
	th := m.app.th
	if m.ok.Clicked(gtx) || m.d.close.Clicked(gtx) {
		m.done = true
	}
	for {
		ev, ok := gtx.Event(key.Filter{Name: key.NameReturn}, key.Filter{Name: key.NameEnter})
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			m.done = true
		}
	}
	ic := icInfo
	if m.isErr {
		ic = icError
	}
	return m.d.layout(gtx, th, 520, ic, m.title, func(gtx C) D {
		return m.app.selText(gtx, "msg/"+m.title, fontSans, 13, pal.Text, m.msg, 0)
	}, footerButtons(nil, func(gtx C) D { return button(gtx, th, &m.ok, "OK", pal.Accent) }))
}

// SecretPrompt asks for a connection password and/or SSH secret.
type SecretPrompt struct {
	app             *App
	cfg             *db.Config
	needPw, needSSH bool
	pw, ssh         widget.Editor
	save            bool
	saveBtn         widget.Clickable
	ok, cancel      widget.Clickable
	onOK            func(pw, ssh string, save bool)
	onCancel        func()
	done, focused   bool
	d               dialog
}

func newSecretPrompt(a *App, cfg *db.Config, needPw, needSSH bool, ok func(pw, ssh string, save bool), cancel func()) *SecretPrompt {
	p := &SecretPrompt{app: a, cfg: cfg, needPw: needPw, needSSH: needSSH, onOK: ok, onCancel: cancel, save: cfg.SavePassword}
	p.pw.Mask, p.ssh.Mask = '•', '•'
	p.pw.Submit, p.ssh.Submit = true, true
	return p
}

func (p *SecretPrompt) Done() bool { return p.done }

func (p *SecretPrompt) cancelled() {
	if p.onCancel != nil {
		p.onCancel()
		p.onCancel = nil
	}
}

func (p *SecretPrompt) accept() {
	p.done = true
	p.onCancel = nil
	p.onOK(p.pw.Text(), p.ssh.Text(), p.save)
}

func (p *SecretPrompt) Layout(gtx C) D {
	th := p.app.th
	if !p.focused {
		p.focused = true
		if p.needPw {
			gtx.Execute(key.FocusCmd{Tag: &p.pw})
		} else {
			gtx.Execute(key.FocusCmd{Tag: &p.ssh})
		}
	}
	for _, ed := range []*widget.Editor{&p.pw, &p.ssh} {
		for {
			ev, ok := ed.Update(gtx)
			if !ok {
				break
			}
			if _, ok := ev.(widget.SubmitEvent); ok {
				p.accept()
			}
		}
	}
	if p.ok.Clicked(gtx) {
		p.accept()
	}
	if p.cancel.Clicked(gtx) || p.d.close.Clicked(gtx) {
		p.done = true
		p.cancelled()
	}
	if p.saveBtn.Clicked(gtx) {
		p.save = !p.save
	}
	return p.d.layout(gtx, th, 460, icLock, "Connect to "+p.cfg.Name, func(gtx C) D {
		var ch []layout.FlexChild
		if p.needPw {
			ch = append(ch, layout.Rigid(func(gtx C) D {
				return formRow(gtx, th, "Password", 110, func(gtx C) D { return textField(gtx, th, &p.pw, p.cfg.User, false) })
			}))
		}
		if p.needSSH {
			ch = append(ch, layout.Rigid(func(gtx C) D {
				return formRow(gtx, th, "SSH password", 110, func(gtx C) D { return textField(gtx, th, &p.ssh, p.cfg.SSH.User+"@"+p.cfg.SSH.Host, false) })
			}))
		}
		ch = append(ch, layout.Rigid(func(gtx C) D {
			if !p.cfg.SavePassword {
				return lbl(gtx, th, fontSans, 11.5, pal.TextDim, "Not saved: asked at every connect (edit the connection to save it).")
			}
			label := "Save in the system keyring"
			if !secretsAvailable() {
				label = "Remember for this session (no system keyring available)"
			}
			return toggle(gtx, th, &p.saveBtn, label, p.save)
		}))
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, ch...)
	}, footerButtons(nil,
		func(gtx C) D { return pillButton(gtx, th, &p.cancel, nil, "Cancel", pal.Text, pal.Hover) },
		func(gtx C) D { return button(gtx, th, &p.ok, "Connect", pal.Accent) },
	))
}
