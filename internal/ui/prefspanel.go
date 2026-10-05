package ui

import (
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/giovanni/lumino/internal/prefs"
)

// PrefsPanel edits appearance, context safety, bookmarks and forwards.
type PrefsPanel struct {
	app  *App
	done bool

	darkBtn, lightBtn, protBtn, roBtn, pfBtn, closeBtn widget.Clickable
	updAutoBtn, updNowBtn                              widget.Clickable
	bmDel                                              map[string]*widget.Clickable
	bmOpen                                             map[string]*widget.Clickable
	list                                               widget.List
}

func newPrefsPanel(a *App) *PrefsPanel {
	p := &PrefsPanel{app: a, bmDel: map[string]*widget.Clickable{}, bmOpen: map[string]*widget.Clickable{}}
	p.list.Axis = layout.Vertical
	return p
}

func (p *PrefsPanel) Done() bool { return p.done }

func clickOf(m map[string]*widget.Clickable, k string) *widget.Clickable {
	c := m[k]
	if c == nil {
		c = new(widget.Clickable)
		m[k] = c
	}
	return c
}

func (p *PrefsPanel) Layout(gtx C) D {
	a, th := p.app, p.app.th
	save := func() {
		if err := a.prefs.Save(); err != nil {
			a.flash("save preferences: "+err.Error(), true)
		}
	}
	if p.updAutoBtn.Clicked(gtx) {
		a.prefs.SetUpdates(!a.prefs.UpdatesEnabled())
		save()
		if a.prefs.UpdatesEnabled() {
			a.checkUpdates(false)
		}
	}
	if p.updNowBtn.Clicked(gtx) {
		p.done = true
		a.checkUpdates(true)
	}
	if p.darkBtn.Clicked(gtx) && a.prefs.Theme != "dark" {
		a.toggleTheme()
	}
	if p.lightBtn.Clicked(gtx) && a.prefs.Theme != "light" {
		a.toggleTheme()
	}
	ctx := ""
	if a.cluster != nil {
		ctx = a.cluster.Context
	}
	if ctx != "" && p.protBtn.Clicked(gtx) {
		a.prefs.SetPolicy(ctx, !a.prefs.Protected(ctx), a.prefs.ReadOnly(ctx))
		save()
	}
	if ctx != "" && p.roBtn.Clicked(gtx) {
		a.prefs.SetPolicy(ctx, a.prefs.Protected(ctx), !a.prefs.ReadOnly(ctx))
		save()
	}
	if p.pfBtn.Clicked(gtx) {
		a.overlay = newPortForwardPanel(a)
		return D{}
	}
	if p.closeBtn.Clicked(gtx) {
		p.done = true
	}
	var bms []prefs.Bookmark
	if ctx != "" {
		bms = a.prefs.BookmarksFor(ctx)
	}
	for _, b := range bms {
		b := b
		if clickOf(p.bmDel, b.Name).Clicked(gtx) {
			a.prefs.RemoveBookmark(b)
			save()
		}
		if clickOf(p.bmOpen, b.Name).Clicked(gtx) {
			p.done = true
			a.openBookmark(b)
		}
	}

	section := func(title string) layout.FlexChild {
		return layout.Rigid(func(gtx C) D {
			return layout.Inset{Top: 14, Bottom: 6}.Layout(gtx, func(gtx C) D {
				return lbl(gtx, th, fontSans, 11, pal.TextDim, title)
			})
		})
	}
	w := min(gtx.Dp(560), gtx.Constraints.Max.X-gtx.Dp(40))
	gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
	return rborder(gtx, pal.Border, pal.Panel, 10, func(gtx C) D {
		return inset(18).Layout(gtx, func(gtx C) D {
			items := []layout.FlexChild{
				layout.Rigid(func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, func(gtx C) D { return iconText(gtx, th, icSettings, 16, pal.TextBright, "Preferences") }),
						layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontMono, 11, pal.TextDim, AppName+" "+Version) }),
						layout.Rigid(layout.Spacer{Width: 10}.Layout),
						layout.Rigid(func(gtx C) D { return iconOnly(gtx, &p.closeBtn, icClose, 16) }),
					)
				}),
				section("APPEARANCE"),
				layout.Rigid(func(gtx C) D {
					seg := func(cl *widget.Clickable, ic *widget.Icon, txt string, on bool) layout.FlexChild {
						return layout.Rigid(func(gtx C) D {
							b, c := colorNRGBA{}, pal.TextDim
							if on {
								b, c = pal.Selected, pal.TextBright
							}
							return pillButton(gtx, th, cl, ic, txt, c, b)
						})
					}
					return rborder(gtx, pal.Border, pal.Bg, 7, func(gtx C) D {
						return inset(2).Layout(gtx, func(gtx C) D {
							return layout.Flex{}.Layout(gtx,
								seg(&p.darkBtn, icMoon, "Dark", !pal.Light),
								seg(&p.lightBtn, icSun, "Light", pal.Light),
							)
						})
					})
				}),
			}
			if ctx != "" {
				items = append(items,
					section("SAFETY FOR CONTEXT "+upper(ctx)),
					layout.Rigid(func(gtx C) D {
						return toggle(gtx, th, &p.protBtn, "Protected (production): red frame, type the name to confirm changes, confirm tunnels", a.prefs.Protected(ctx))
					}),
					layout.Rigid(func(gtx C) D {
						note := "set explicitly"
						if _, ok := a.prefs.Contexts[ctx]; !ok || a.prefs.Contexts[ctx].Protected == nil {
							note = "auto-detected from the context name"
						}
						return layout.Inset{Left: 40, Bottom: 6}.Layout(gtx, func(gtx C) D {
							return lbl(gtx, th, fontSans, 11, pal.TextDim, note)
						})
					}),
					layout.Rigid(func(gtx C) D {
						return toggle(gtx, th, &p.roBtn, "Read-only: block edits, deletes, scaling, shells and assistant actions (port-forwards stay allowed)", a.prefs.ReadOnly(ctx))
					}),
					section("UPDATES"),
					layout.Rigid(func(gtx C) D {
						return toggle(gtx, th, &p.updAutoBtn, "Check for new releases (daily, GitHub); updates are verified before installing", a.prefs.UpdatesEnabled())
					}),
					layout.Rigid(func(gtx C) D {
						return layout.Inset{Top: 6}.Layout(gtx, func(gtx C) D {
							return pillButton(gtx, th, &p.updNowBtn, icDownload, "Check now", pal.Text, pal.Hover)
						})
					}),
					section("PORT-FORWARDS"),
					layout.Rigid(func(gtx C) D {
						n := len(a.prefs.SavedForwards(ctx))
						return pillButton(gtx, th, &p.pfBtn, icForward, "Manage port-forwards ("+itoa(n)+" pinned)", pal.Text, pal.Hover)
					}),
					section("BOOKMARKS"),
					layout.Rigid(func(gtx C) D {
						if len(bms) == 0 {
							return lbl(gtx, th, fontSans, 12, pal.TextDim, "None yet — press m on a resource view (or right-click → Bookmark).")
						}
						var ch []layout.FlexChild
						for _, b := range bms {
							b := b
							ch = append(ch, layout.Rigid(func(gtx C) D {
								return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
									layout.Flexed(1, func(gtx C) D {
										return clickOf(p.bmOpen, b.Name).Layout(gtx, func(gtx C) D {
											return insetHV(4, 4).Layout(gtx, func(gtx C) D {
												return iconText(gtx, th, iconFor(b.GVR), 12, pal.Text, b.Name)
											})
										})
									}),
									layout.Rigid(func(gtx C) D { return iconOnly(gtx, clickOf(p.bmDel, b.Name), icDelete, 14) }),
								)
							}))
						}
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx, ch...)
					}),
				)
			}
			items = append(items, layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: 14}.Layout(gtx, func(gtx C) D {
					return lbl(gtx, th, fontSans, 11, pal.TextDim, "Saved in "+shortPath(prefs.Dir())+"/settings.json")
				})
			}))
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, items...)
		})
	})
}
