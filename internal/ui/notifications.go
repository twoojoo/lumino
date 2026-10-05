package ui

import (
	"context"
	"image"
	"strings"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"k8s.io/apimachinery/pkg/util/duration"

	"github.com/giovanni/lumino/internal/backend"
	"github.com/giovanni/lumino/internal/notify"
)

const maxEvents = 300

// startHealth watches cluster health and turns transitions into in-app
// events and desktop notifications.
func (a *App) startHealth(c *backend.Cluster) {
	if a.healthCancel != nil {
		a.healthCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.healthCancel = cancel
	go c.WatchHealth(ctx, 5*time.Second, func(ev []backend.HealthEvent) {
		a.post(func() {
			if ctx.Err() != nil {
				return
			}
			a.onHealth(ev)
		})
	})
}

func (a *App) onHealth(ev []backend.HealthEvent) {
	a.events = append(a.events, ev...)
	if n := len(a.events) - maxEvents; n > 0 {
		a.events = append(a.events[:0:0], a.events[n:]...)
	}
	a.unread += len(ev)
	if !a.notifyOn {
		return
	}
	// desktop: coalesce bursts into one notification
	if a.notifier != nil {
		summary, body, urg := ev[0].Title, ev[0].Body, urgency(ev[0].Severity)
		if len(ev) > 1 {
			summary = itoa(len(ev)) + " cluster events"
			var b strings.Builder
			for i, e := range ev {
				if i == 5 {
					b.WriteString("…")
					break
				}
				b.WriteString(e.Title + ": " + e.Body + "\n")
				urg = max(urg, urgency(e.Severity))
			}
			body = b.String()
		}
		n := a.notifier
		go func() { _ = n.Send(AppName+" · "+summary, body, urg) }()
	}
	last := ev[len(ev)-1]
	a.flash(last.Title+" — "+last.Body, last.Severity == backend.SevErr)
}

func urgency(s backend.Severity) byte {
	switch s {
	case backend.SevErr:
		return notify.Critical
	case backend.SevWarn:
		return notify.Normal
	}
	return notify.Low
}

func sevStyle(s backend.Severity) (*widget.Icon, colorNRGBA) {
	switch s {
	case backend.SevErr:
		return icError, pal.Err
	case backend.SevWarn:
		return icWarning, pal.Warn
	}
	return icOK, pal.Ok
}

// NotifPanel lists recent health events; clicking one navigates to it.
type NotifPanel struct {
	app               *App
	done              bool
	list              widget.List
	tag               int
	toggleBtn, clrBtn widget.Clickable
}

func newNotifPanel(a *App) *NotifPanel {
	p := NotifPanel{app: a}
	p.list.Axis = layout.Vertical
	a.unread = 0
	return &p
}

func (p *NotifPanel) Done() bool { return p.done }

func (p *NotifPanel) Layout(gtx C) D {
	a := p.app
	th := a.th
	if p.toggleBtn.Clicked(gtx) {
		a.notifyOn = !a.notifyOn
	}
	if p.clrBtn.Clicked(gtx) {
		a.events = nil
	}
	w := min(gtx.Dp(620), gtx.Constraints.Max.X-gtx.Dp(40))
	gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
	return rborder(gtx, pal.Border, pal.Panel, 10, func(gtx C) D {
		return inset(1).Layout(gtx, func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					return insetHV(14, 10).Layout(gtx, func(gtx C) D {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx C) D { return mdi(gtx, icBell, pal.Accent, 20) }),
							layout.Rigid(layout.Spacer{Width: 10}.Layout),
							layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontSans, 16, pal.TextBright, "Notifications") }),
							layout.Rigid(layout.Spacer{Width: 8}.Layout),
							layout.Rigid(func(gtx C) D { return badge(gtx, th, itoa(len(a.events)), pal.Text, pal.Bg) }),
							layout.Flexed(1, layout.Spacer{}.Layout),
							layout.Rigid(func(gtx C) D { return toggle(gtx, th, &p.toggleBtn, "Desktop notifications", a.notifyOn) }),
							layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &p.clrBtn, icClear, "Clear", pal.TextDim, colorNRGBA{}) }),
						)
					})
				}),
				layout.Rigid(func(gtx C) D { return hline(gtx, pal.Border) }),
				layout.Rigid(func(gtx C) D {
					if len(a.events) == 0 {
						return inset(24).Layout(gtx, func(gtx C) D {
							gtx.Constraints.Min.X = gtx.Constraints.Max.X
							return layout.Center.Layout(gtx, func(gtx C) D {
								return iconText(gtx, th, icOK, 13, pal.TextDim, "All quiet — no cluster events yet")
							})
						})
					}
					rh := gtx.Dp(54)
					gtx.Constraints.Max.Y = min(gtx.Constraints.Max.Y, gtx.Dp(460))
					n := len(a.events)
					area := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
					event.Op(gtx.Ops, &p.tag)
					pointer.CursorPointer.Add(gtx.Ops)
					for {
						ev, ok := gtx.Event(pointer.Filter{Target: &p.tag, Kinds: pointer.Press})
						if !ok {
							break
						}
						if pe, ok := ev.(pointer.Event); ok {
							i := p.list.Position.First + (int(pe.Position.Y)+p.list.Position.Offset)/rh
							if i >= 0 && i < n {
								e := a.events[n-1-i]
								p.done = true
								gtx.Execute(key.FocusCmd{})
								a.goTo(e.GVR, e.Path)
							}
						}
					}
					d := material.List(th, &p.list).Layout(gtx, n, func(gtx C, i int) D {
						e := a.events[n-1-i] // newest first
						ic, c := sevStyle(e.Severity)
						sz := image.Pt(gtx.Constraints.Max.X, rh)
						gtx.Constraints = layout.Exact(sz)
						fillRect(gtx, withAlpha(pal.Border, 0x60), image.Rect(0, rh-gtx.Dp(1), sz.X, rh))
						return insetHV(14, 6).Layout(gtx, func(gtx C) D {
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(func(gtx C) D {
									return rbg(gtx, withAlpha(c, 0x30), 14, func(gtx C) D {
										return inset(6).Layout(gtx, func(gtx C) D { return mdi(gtx, ic, c, 16) })
									})
								}),
								layout.Rigid(layout.Spacer{Width: 12}.Layout),
								layout.Flexed(1, func(gtx C) D {
									return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
										layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontSans, 13, pal.TextBright, e.Title) }),
										layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontSans, 12, pal.TextDim, e.Body) }),
									)
								}),
								layout.Rigid(func(gtx C) D {
									return lbl(gtx, th, fontSans, 11, pal.TextDim, duration.HumanDuration(time.Since(e.At))+" ago")
								}),
							)
						})
					})
					area.Pop()
					return d
				}),
			)
		})
	})
}

// goTo opens the table of gvr with path selected.
func (a *App) goTo(gvr, path string) {
	if gvr == "" {
		return
	}
	if backend.IsNamespaced(gvr) {
		if i := strings.IndexByte(path, '/'); i > 0 {
			a.ns = path[:i]
		}
	}
	a.navStack = nil
	a.show(navState{page: gvr, gvr: gvr, selID: path})
}
