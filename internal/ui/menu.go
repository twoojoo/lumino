package ui

import (
	"image"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"
)

// ---- context menu ----

type menuItem struct {
	icon    *widget.Icon
	label   string
	hint    string
	danger  bool
	sep     bool
	action  func()
	clicker widget.Clickable
}

// ContextMenu is a small popup anchored at the pointer.
type ContextMenu struct {
	app   *App
	at    image.Point
	title string
	items []*menuItem
	done  bool
	sel   int
}

func (m *ContextMenu) Done() bool { return m.done }

// anchor implements positioned overlays.
func (m *ContextMenu) anchor() image.Point { return m.at }

func (m *ContextMenu) Layout(gtx C) D {
	th := m.app.th
	for {
		ev, ok := gtx.Event(key.Filter{Name: key.NameUpArrow}, key.Filter{Name: key.NameDownArrow}, key.Filter{Name: key.NameReturn}, key.Filter{Name: key.NameEnter})
		if !ok {
			break
		}
		e, ok := ev.(key.Event)
		if !ok || e.State != key.Press {
			continue
		}
		switch e.Name {
		case key.NameUpArrow:
			m.step(-1)
		case key.NameDownArrow:
			m.step(1)
		default:
			if m.sel >= 0 && m.sel < len(m.items) && m.items[m.sel].action != nil {
				m.done = true
				m.items[m.sel].action()
			}
		}
	}
	for _, it := range m.items {
		if it.action != nil && it.clicker.Clicked(gtx) {
			m.done = true
			it.action()
		}
	}
	gtx.Constraints.Min.X = gtx.Dp(230)
	gtx.Constraints.Max.X = gtx.Dp(300)
	return rborder(gtx, pal.Border, pal.Panel, 8, func(gtx C) D {
		return inset(4).Layout(gtx, func(gtx C) D {
			var ch []layout.FlexChild
			if m.title != "" {
				ch = append(ch, layout.Rigid(func(gtx C) D {
					return insetHV(10, 6).Layout(gtx, func(gtx C) D {
						return lbl(gtx, th, fontSans, 11, pal.TextDim, m.title)
					})
				}))
			}
			for i, it := range m.items {
				i, it := i, it
				if it.sep {
					ch = append(ch, layout.Rigid(func(gtx C) D {
						return layout.Inset{Top: 3, Bottom: 3}.Layout(gtx, func(gtx C) D { return hline(gtx, pal.Border) })
					}))
					continue
				}
				ch = append(ch, layout.Rigid(func(gtx C) D {
					return it.clicker.Layout(gtx, func(gtx C) D {
						hot := it.clicker.Hovered() || i == m.sel
						b := colorNRGBA{}
						if hot {
							b = pal.Hover
						}
						c := pal.Text
						if it.danger {
							c = pal.Err
						}
						return rbg(gtx, b, 5, func(gtx C) D {
							gtx.Constraints.Min.X = gtx.Constraints.Max.X
							return insetHV(10, 6).Layout(gtx, func(gtx C) D {
								return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
									layout.Rigid(func(gtx C) D { return mdi(gtx, it.icon, c, 15) }),
									layout.Rigid(layout.Spacer{Width: 9}.Layout),
									layout.Flexed(1, func(gtx C) D { return lbl(gtx, th, fontSans, 13, c, it.label) }),
									layout.Rigid(func(gtx C) D {
										if it.hint == "" {
											return D{}
										}
										return lbl(gtx, th, fontMono, 11, pal.TextDim, it.hint)
									}),
								)
							})
						})
					})
				}))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, ch...)
		})
	})
}

func (m *ContextMenu) step(d int) {
	for i := 0; i < len(m.items); i++ {
		m.sel = (m.sel + d + len(m.items)) % len(m.items)
		if !m.items[m.sel].sep {
			return
		}
	}
}
