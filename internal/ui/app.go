// Package ui is the native Gio front-end of kubeon.
package ui

import (
	"context"
	"fmt"
	"image"
	"log/slog"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/giovanni/lumino/internal/backend"
	"github.com/giovanni/lumino/internal/notify"
	"github.com/giovanni/lumino/internal/prefs"
	"github.com/giovanni/lumino/internal/safety"
)

// Options configure the UI.
type Options struct {
	Kubeconfig string
	Context    string
	Namespace  string
	Resource   string
}

// App is the root UI state. All fields are owned by the UI goroutine;
// background work hands results back through post.
type App struct {
	w    *app.Window
	th   *material.Theme
	opts Options

	inboxMx sync.Mutex
	inbox   []func()

	contexts   []string
	cluster    *backend.Cluster
	connecting string
	connErr    error
	namespaces []string
	ns         string // "" = all namespaces

	status    string
	statusErr bool
	statusAt  time.Time

	sidebar *Sidebar
	title   *TitleBar
	ai      *AIPanel
	prefs   *prefs.Prefs

	maximized, winFocused  bool
	rootTag                int
	pointer                image.Point
	pendingCopy            *string
	tabs                   []*tabState
	tabIdx                 int
	newTabBtn              widget.Clickable
	backdropTag, dialogTag int
	view                   *ResourceView
	ovw                    *OverviewView
	xray                   *XRayView
	san                    *SanitizerView
	rbac                   *RBACView
	page                   string // current gvr, overviewGVR or xrayPage
	navStack               []navState
	drawer                 *Drawer
	dock                   *Dock
	overlay                overlay

	ctxBtn        widget.Clickable
	pfBtn         widget.Clickable
	bellBtn       widget.Clickable
	updBtn        widget.Clickable
	upd           updState
	tip           tooltip
	tipTags       map[string]*tipTag
	frame         uint64
	sels          map[string]*selState
	selFocused    bool        // a selectable text has the keyboard focus (this frame)
	selWasFocused bool        // … in the previous frame (keys are handled first)
	lineSel       *lineSelect // line selection with the focus (this frame)
	lineSelPrev   *lineSelect

	events       []backend.HealthEvent
	unread       int
	notifyOn     bool
	notifier     *notify.Notifier
	healthCancel context.CancelFunc

	onInvalidate func() // headless harness hook
}

// overlay is a modal layer (picker, confirm dialog).
type overlay interface {
	Layout(gtx C) D
	// Close reports whether the overlay asked to be dismissed.
	Done() bool
}

// Run opens the main window and blocks until it is closed.
// Version is the kubeon release, shown in the preferences.
var Version = "dev"

func Run(opts Options) error {
	a := newApp(opts, new(app.Window))
	a.w.Option(app.Title(AppName), app.Size(unit.Dp(1440), unit.Dp(900)), app.MinSize(unit.Dp(800), unit.Dp(500)),
		app.Decorated(false))
	a.start()

	errc := make(chan error, 1)
	go func() { errc <- a.loop() }()
	app.Main()
	return <-errc
}

func newApp(opts Options, w *app.Window) *App {
	a := App{opts: opts, th: newTheme(), ns: opts.Namespace, w: w, notifyOn: true, prefs: prefs.Load(), tipTags: map[string]*tipTag{}, sels: map[string]*selState{}}
	setTheme(a.th, a.prefs.Theme)
	if w != nil {
		a.notifier = notify.New(AppName)
	}
	a.sidebar = newSidebar(&a)
	a.title = newTitleBar(&a)
	a.ai = newAIPanel(&a)
	a.view = newResourceView(&a)
	a.ovw = newOverview(&a)
	a.xray = newXRay(&a)
	a.san = newSanitizer(&a)
	a.rbac = newRBACView(&a)
	a.drawer = newDrawer(&a)
	a.dock = newDock(&a)
	return &a
}

// ensureResources reloads the process-wide resource registry when an
// abandoned, slower connect to another context overwrote it.
func (a *App) ensureResources() {
	c := a.cluster
	if c == nil || c.ResourcesLoaded() {
		return
	}
	go func() {
		if err := c.RefreshResources(); err != nil {
			return
		}
		a.post(func() {
			if a.cluster == c {
				a.sidebar.reload(c)
			}
		})
	}()
}

// start lists contexts and connects to the initial one.
func (a *App) start() {
	if err := a.prefs.LoadError; err != nil {
		a.flash("Settings reset: "+err.Error(), true)
	}
	a.startUpdateChecks()
	opts := a.opts
	names, current, err := backend.Contexts(opts.Kubeconfig)
	if err != nil {
		a.connErr = err
	}
	a.contexts = safety.FilterContexts(names)
	target := opts.Context
	if target == "" {
		target = current
	}
	if target != "" {
		a.connect(target)
	} else if err == nil {
		a.connErr = fmt.Errorf("no usable context in %s", opts.Kubeconfig)
	}
}

func (a *App) loop() error {
	var ops op.Ops
	for {
		switch e := a.w.Event().(type) {
		case app.DestroyEvent:
			a.shutdown()
			return e.Err
		case app.ConfigEvent:
			a.maximized = e.Config.Mode == app.Maximized || e.Config.Mode == app.Fullscreen
			a.winFocused = e.Config.Focused
			a.w.Invalidate()
		case app.FrameEvent:
			a.drain()
			gtx := app.NewContext(&ops, e)
			a.layout(gtx)
			e.Frame(gtx.Ops)
		}
	}
}

// post schedules f on the UI goroutine and wakes the window.
func (a *App) post(f func()) {
	a.inboxMx.Lock()
	a.inbox = append(a.inbox, f)
	a.inboxMx.Unlock()
	a.invalidate()
}

func (a *App) drain() {
	a.inboxMx.Lock()
	q := a.inbox
	a.inbox = nil
	a.inboxMx.Unlock()
	for _, f := range q {
		f()
	}
}

func (a *App) shutdown() {
	if a.healthCancel != nil {
		a.healthCancel()
	}
	a.view.close()
	a.ovw.close()
	a.xray.close()
	a.dock.closeAll()
	if a.cluster != nil {
		a.cluster.Close()
	}
}

// flash shows a status message.
func (a *App) flash(msg string, isErr bool) {
	a.status, a.statusErr, a.statusAt = msg, isErr, time.Now()
	if isErr {
		slog.Warn(msg)
	}
	a.invalidate()
}

// do runs fn in background and flashes its outcome.
func (a *App) do(desc string, fn func() error) {
	a.flash(desc+"…", false)
	go func() {
		err := fn()
		a.post(func() {
			if err != nil {
				a.flash(fmt.Sprintf("%s failed: %v", desc, err), true)
				return
			}
			a.flash(desc+" — done", false)
		})
	}()
}

func (a *App) connect(kctx string) {
	if err := safety.CheckContext(kctx); err != nil {
		a.connErr = err
		return
	}
	a.view.close()
	a.ovw.close()
	a.xray.close()
	a.dock.closeAll()
	a.drawer.close()
	if a.cluster != nil {
		a.cluster.Close()
		a.cluster = nil
	}
	if a.healthCancel != nil {
		a.healthCancel()
		a.healthCancel = nil
	}
	a.connecting, a.connErr = kctx, nil
	go func() {
		c, err := backend.Connect(a.opts.Kubeconfig, kctx)
		var nss []string
		if err == nil {
			nss, _ = c.Namespaces()
		}
		a.post(func() {
			if a.connecting != kctx {
				if c != nil {
					c.Close()
				}
				a.ensureResources()
				return
			}
			a.connecting = ""
			if err != nil {
				a.connErr = err
				return
			}
			a.cluster, a.namespaces = c, nss
			a.ensureResources()
			a.ai.reset()
			a.tabs, a.tabIdx = nil, 0
			a.startSavedForwards(c)
			a.events, a.unread = nil, 0
			a.startHealth(c)
			a.sidebar.reload(c)
			res := a.opts.Resource
			if res == "" {
				res = overviewGVR
			} else if g, ok := backend.LookupAlias(res); ok {
				res = g
			}
			a.openResource(res)
			a.flash(fmt.Sprintf("Connected to %s (%s)", c.Context, c.Version()), false)
		})
	}()
}

func (a *App) refreshNamespaces() {
	c := a.cluster
	if c == nil {
		return
	}
	go func() {
		nss, err := c.Namespaces()
		if err == nil {
			a.post(func() { a.namespaces = nss })
		}
	}()
}

// openResource navigates from the sidebar/palette: resets history.
func (a *App) openResource(gvr string) {
	a.navStack = nil
	a.show(navState{page: gvr, gvr: gvr})
}

func (a *App) setNamespace(ns string) {
	a.ns = ns
	a.show(a.current())
}

// textFocused reports whether a text input currently owns the keyboard.
func (a *App) textFocused(gtx C) bool {
	if a.overlay != nil {
		return true
	}
	return gtx.Focused(&a.view.filter) || a.drawer.focused(gtx) || a.dock.focused(gtx) || a.ai.focused(gtx) ||
		a.rbac.focused(gtx)
}

func (a *App) layout(gtx C) {
	a.frame++
	a.selWasFocused, a.selFocused = a.selFocused, false
	a.lineSelPrev, a.lineSel = a.lineSel, nil
	fill(gtx, pal.Bg)
	if a.overlay != nil && a.overlay.Done() {
		a.overlay = nil
		gtx.Execute(key.FocusCmd{})
	}
	a.handleKeys(gtx)
	if a.pendingCopy != nil {
		copyText(gtx, *a.pendingCopy)
		a.flash("Copied to clipboard", false)
		a.pendingCopy = nil
	}

	// root handler: tracks the pointer in window coordinates (context menus)
	root := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
	event.Op(gtx.Ops, &a.rootTag)
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &a.rootTag, Kinds: pointer.Move | pointer.Press | pointer.Drag})
		if !ok {
			break
		}
		if pe, ok := ev.(pointer.Event); ok {
			a.pointer = pe.Position.Round()
		}
	}
	defer root.Pop()

	layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.title.Layout),
		layout.Flexed(1, func(gtx C) D {
			return layout.Flex{}.Layout(gtx,
				layout.Rigid(a.sidebar.Layout),
				layout.Rigid(func(gtx C) D { return vline(gtx, pal.Border) }),
				layout.Flexed(1, a.layoutMain),
				layout.Rigid(func(gtx C) D {
					if !a.ai.open {
						return D{}
					}
					return layout.Flex{}.Layout(gtx,
						layout.Rigid(func(gtx C) D { return vline(gtx, pal.Border) }),
						layout.Rigid(a.ai.Layout),
					)
				}),
			)
		}),
		layout.Rigid(a.layoutStatus),
	)

	a.layoutPolicyFrame(gtx)
	if a.overlay != nil {
		a.layoutModal(gtx)
	}
	a.layoutTooltip(gtx)
	a.pruneSels()
}

// layoutModal draws the active overlay over a dimmed backdrop. A click on
// the backdrop (outside the dialog) dismisses it, like Esc.
func (a *App) layoutModal(gtx C) {
	gtx.Constraints.Min = gtx.Constraints.Max
	size := gtx.Constraints.Max
	ov := a.overlay

	// lay out the dialog first (recorded) to know its bounds
	m := op.Record(gtx.Ops)
	top := gtx.Dp(90)
	dg := gtx
	dg.Constraints.Min = image.Point{}
	dg.Constraints.Max.Y = max(0, size.Y-top)
	anchored, isMenu := ov.(interface{ anchor() image.Point })
	if isMenu {
		dg.Constraints.Max.Y = size.Y
	}
	dims := ov.Layout(dg)
	content := m.Stop()
	x := (size.X - dims.Size.X) / 2
	box := image.Rectangle{Min: image.Pt(x, top), Max: image.Pt(x+dims.Size.X, top+dims.Size.Y)}
	if isMenu {
		// popup at the pointer, kept inside the window
		p := anchored.anchor()
		p.X = max(0, min(p.X, size.X-dims.Size.X-gtx.Dp(4)))
		if p.Y+dims.Size.Y > size.Y {
			p.Y = max(0, p.Y-dims.Size.Y)
		}
		box = image.Rectangle{Min: p, Max: p.Add(dims.Size)}
	}

	// backdrop: dims the app, swallows input, dismisses on press
	backdrop := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, &a.backdropTag)
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &a.backdropTag, Kinds: pointer.Press | pointer.Release | pointer.Cancel | pointer.Scroll, ScrollY: pointer.ScrollRange{Min: -1e6, Max: 1e6}})
		if !ok {
			break
		}
		if pe, ok := ev.(pointer.Event); ok && pe.Kind == pointer.Press && !pe.Position.Round().In(box) && a.overlay == ov {
			a.overlay = nil
			gtx.Execute(key.FocusCmd{})
		}
	}
	if !isMenu {
		fill(gtx, pal.Backdrop)
	}
	backdrop.Pop()

	// dialog: a blocker under its content keeps clicks on padding/labels
	// from reaching the backdrop
	st := op.Offset(box.Min).Push(gtx.Ops)
	blk := clip.Rect{Max: dims.Size}.Push(gtx.Ops)
	event.Op(gtx.Ops, &a.dialogTag)
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &a.dialogTag, Kinds: pointer.Press | pointer.Release | pointer.Cancel | pointer.Scroll, ScrollY: pointer.ScrollRange{Min: -1e6, Max: 1e6}}); !ok {
			break
		}
	}
	content.Add(gtx.Ops)
	blk.Pop()
	st.Pop()
}

func (a *App) layoutMain(gtx C) D {
	if a.cluster == nil {
		return a.layoutWelcome(gtx)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.layoutTabs),
		layout.Flexed(1, func(gtx C) D {
			switch a.page {
			case overviewGVR:
				return a.ovw.Layout(gtx)
			case rbacPage:
				if a.drawer.open {
					return layout.Flex{}.Layout(gtx,
						layout.Flexed(0.55, a.rbac.Layout),
						layout.Rigid(func(gtx C) D { return vline(gtx, pal.Border) }),
						layout.Flexed(0.45, a.drawer.Layout),
					)
				}
				return a.rbac.Layout(gtx)
			case sanitizerPage:
				if a.drawer.open {
					return layout.Flex{}.Layout(gtx,
						layout.Flexed(0.55, a.san.Layout),
						layout.Rigid(func(gtx C) D { return vline(gtx, pal.Border) }),
						layout.Flexed(0.45, a.drawer.Layout),
					)
				}
				return a.san.Layout(gtx)
			case xrayPage:
				if a.drawer.open {
					return layout.Flex{}.Layout(gtx,
						layout.Flexed(0.55, a.xray.Layout),
						layout.Rigid(func(gtx C) D { return vline(gtx, pal.Border) }),
						layout.Flexed(0.45, a.drawer.Layout),
					)
				}
				return a.xray.Layout(gtx)
			}
			if !a.drawer.open {
				return a.view.Layout(gtx)
			}
			return layout.Flex{}.Layout(gtx,
				layout.Flexed(0.55, a.view.Layout),
				layout.Rigid(func(gtx C) D { return vline(gtx, pal.Border) }),
				layout.Flexed(0.45, a.drawer.Layout),
			)
		}),
		layout.Rigid(a.dock.Layout),
	)
}

func (a *App) layoutWelcome(gtx C) D {
	return layout.Center.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				if a.connErr != nil {
					return rbg(gtx, withAlpha(pal.Err, 0x30), 36, func(gtx C) D {
						return inset(18).Layout(gtx, func(gtx C) D { return mdi(gtx, icCloudOff, pal.Err, 40) })
					})
				}
				return drawLogo(gtx, 76)
			}),
			layout.Rigid(layout.Spacer{Height: 14}.Layout),
			layout.Rigid(func(gtx C) D {
				return lbl(gtx, a.th, fontSans, 28, pal.TextBright, AppName)
			}),
			layout.Rigid(func(gtx C) D {
				return lbl(gtx, a.th, fontSans, 13, pal.TextDim, "native Kubernetes IDE · powered by k9s")
			}),
			layout.Rigid(layout.Spacer{Height: 12}.Layout),
			layout.Rigid(func(gtx C) D {
				switch {
				case a.connecting != "":
					return lbl(gtx, a.th, fontSans, 15, pal.Text, "Connecting to "+a.connecting+"…")
				case a.connErr != nil:
					gtx.Constraints.Max.X = gtx.Dp(700)
					return a.selText(gtx, "connErr", fontSans, 16, pal.Err, a.connErr.Error(), 0)
				default:
					return lbl(gtx, a.th, fontSans, 15, pal.Text, "Select a context")
				}
			}),
			layout.Rigid(layout.Spacer{Height: 16}.Layout),
			layout.Rigid(func(gtx C) D {
				if a.ctxBtn.Clicked(gtx) {
					a.pickContext()
				}
				if a.connecting != "" {
					return D{}
				}
				return pillButton(gtx, a.th, &a.ctxBtn, icCluster, "Choose context", pal.TextBright, pal.Accent)
			}),
		)
	})
}

func (a *App) layoutStatus(gtx C) D {
	h := gtx.Dp(26)
	gtx.Constraints.Min.Y, gtx.Constraints.Max.Y = h, h
	fill(gtx, pal.Accent)
	th := a.th
	fg := pal.OnAccent
	center := func(w layout.Widget) layout.Widget {
		return func(gtx C) D {
			gtx.Constraints.Min.Y = h
			return layout.W.Layout(gtx, w)
		}
	}
	if a.pfBtn.Clicked(gtx) {
		a.overlay = newPortForwardPanel(a)
	}
	if a.bellBtn.Clicked(gtx) {
		a.overlay = newNotifPanel(a)
	}
	if a.updBtn.Clicked(gtx) && a.upd.rel != nil {
		a.overlay = newUpdateDialog(a)
	}
	return insetHV(10, 0).Layout(gtx, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(center(func(gtx C) D {
				if a.cluster == nil {
					return iconText(gtx, th, icCloudOff, 12, fg, "no cluster")
				}
				return iconText(gtx, th, icCluster, 12, fg, a.cluster.Context)
			})),
			layout.Rigid(center(func(gtx C) D {
				if a.cluster == nil {
					return D{}
				}
				return layout.Inset{Left: 16}.Layout(gtx, func(gtx C) D {
					return iconText(gtx, th, icInfo, 12, fg, "Kubernetes "+a.cluster.Version())
				})
			})),
			layout.Rigid(center(func(gtx C) D {
				if a.cluster == nil {
					return D{}
				}
				n := len(a.cluster.PortForwards())
				if n == 0 {
					return D{}
				}
				return layout.Inset{Left: 16}.Layout(gtx, func(gtx C) D {
					return a.pfBtn.Layout(gtx, func(gtx C) D {
						return iconText(gtx, th, icForward, 12, fg, itoa(n)+" port-forward(s)")
					})
				})
			})),
			layout.Rigid(layout.Spacer{Width: 20}.Layout),
			layout.Flexed(1, center(func(gtx C) D {
				if a.status == "" || time.Since(a.statusAt) > 8*time.Second && !a.statusErr {
					return D{}
				}
				if a.statusErr {
					return rbg(gtx, pal.Err, 4, func(gtx C) D {
						return insetHV(6, 2).Layout(gtx, func(gtx C) D {
							return iconText(gtx, th, icError, 12, fg, a.status)
						})
					})
				}
				return iconText(gtx, th, icStatusDone, 12, fg, a.status)
			})),
			layout.Rigid(center(func(gtx C) D {
				if a.upd.rel == nil {
					return D{}
				}
				txt := "Update " + a.upd.rel.Version
				if a.upd.ready {
					txt = "Restart to update"
				}
				return layout.Inset{Right: 12}.Layout(gtx, func(gtx C) D {
					return a.updBtn.Layout(gtx, func(gtx C) D {
						return rbg(gtx, withAlpha(rgb(0x000000), 0x30), 4, func(gtx C) D {
							return insetHV(6, 2).Layout(gtx, func(gtx C) D {
								return iconText(gtx, th, icDownload, 12, fg, txt)
							})
						})
					})
				})
			})),
			layout.Rigid(center(func(gtx C) D {
				if a.cluster == nil {
					return D{}
				}
				return layout.Inset{Right: 6}.Layout(gtx, func(gtx C) D {
					return a.bellBtn.Layout(gtx, func(gtx C) D {
						ic := icBell
						switch {
						case !a.notifyOn:
							ic = icBellOff
						case a.unread > 0:
							ic = icBellActive
						}
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx C) D { return mdi(gtx, ic, fg, 16) }),
							layout.Rigid(func(gtx C) D {
								if a.unread == 0 {
									return D{}
								}
								return layout.Inset{Left: 3}.Layout(gtx, func(gtx C) D {
									return badge(gtx, th, itoa(a.unread), pal.OnAccent, pal.Err)
								})
							}),
						)
					})
				})
			})),
			layout.Rigid(center(func(gtx C) D {
				hints := [][2]string{{":", "command"}, {"/", "filter"}, {"l", "logs"}, {"s", "shell"}, {"d", "describe"}, {"y", "yaml"}, {"^d", "delete"}}
				var ch []layout.FlexChild
				for _, k := range hints {
					k := k
					ch = append(ch, layout.Rigid(func(gtx C) D {
						return layout.Inset{Left: 10}.Layout(gtx, func(gtx C) D { return keyHint(gtx, th, k[0], k[1]) })
					}))
				}
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx, ch...)
			})),
		)
	})
}

// keyHint draws a keyboard chip followed by its meaning.
func keyHint(gtx C, th *material.Theme, k, what string) D {
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return rbg(gtx, withAlpha(rgb(0x000000), 0x40), 3, func(gtx C) D {
				return insetHV(5, 1).Layout(gtx, func(gtx C) D {
					return lbl(gtx, th, fontMono, 11, pal.OnAccent, k)
				})
			})
		}),
		layout.Rigid(layout.Spacer{Width: 4}.Layout),
		layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontSans, 11, withAlpha(pal.OnAccent, 0xd0), what) }),
	)
}

func (a *App) invalidate() {
	if a.w != nil {
		a.w.Invalidate()
	} else if a.onInvalidate != nil {
		a.onInvalidate()
	}
}

// pageTitleSafe names the current main page (for assistant context).
func (a *App) pageTitleSafe() string {
	switch a.page {
	case overviewGVR:
		return "Cluster overview"
	case sanitizerPage:
		return "Sanitizer"
	case rbacPage:
		return "RBAC Explorer"
	case xrayPage:
		return "XRay of " + titleFor(a.xray.gvr)
	case "":
		return ""
	}
	return a.view.title
}

// toggleTheme switches dark/light and persists the choice.
func (a *App) toggleTheme() {
	if a.prefs.Theme == "light" {
		a.prefs.Theme = "dark"
	} else {
		a.prefs.Theme = "light"
	}
	setTheme(a.th, a.prefs.Theme)
	if err := a.prefs.Save(); err != nil {
		a.flash("save preferences: "+err.Error(), true)
	}
	a.invalidate()
}
