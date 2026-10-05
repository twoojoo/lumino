// Package ui is the native Gio front-end of lumino.
package ui

import (
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

	"github.com/giovanni/lumino/internal/notify"
	"github.com/giovanni/lumino/internal/prefs"
)

// Options configure the UI.
type Options struct {
	// Connection opens (and connects) a saved connection by name or id.
	Connection string
}

// App is the root UI state. All fields are owned by the UI goroutine;
// background work hands results back through post.
type App struct {
	w    *app.Window
	th   *material.Theme
	opts Options

	inboxMx sync.Mutex
	inbox   []func()

	prefs *prefs.Prefs
	conns map[string]*connState

	status    string
	statusErr bool
	statusAt  time.Time

	sidebar *Sidebar
	title   *TitleBar
	ai      *AIPanel

	maximized, winFocused  bool
	rootTag                int
	pointer                image.Point
	pendingCopy            *string
	pendingPaste           *CodeEditor
	tabs                   []Tab
	tabIdx                 int
	tabBtns                []widget.Clickable
	tabClose               []widget.Clickable
	tabTags                []int
	newTabBtn              widget.Clickable
	newConnBtn             widget.Clickable
	backdropTag, dialogTag int
	overlay                overlay
	welcome                welcome

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

	notifier *notify.Notifier
	saveAt   time.Time // debounced workspace save

	onInvalidate func() // headless harness hook
}

// overlay is a modal layer (picker, dialog, menu).
type overlay interface {
	Layout(gtx C) D
	// Done reports whether the overlay asked to be dismissed.
	Done() bool
}

// canceler is implemented by overlays that must know when they are
// dismissed from outside (Esc, click on the backdrop).
type canceler interface{ cancelled() }

// Version is the lumino release, shown in the preferences.
var Version = "dev"

// Run opens the main window and blocks until it is closed.
func Run(opts Options) error {
	a := newApp(opts, new(app.Window))
	a.w.Option(app.Title(AppName), app.Size(unit.Dp(1440), unit.Dp(900)), app.MinSize(unit.Dp(860), unit.Dp(520)),
		app.Decorated(false))
	a.start()

	errc := make(chan error, 1)
	go func() { errc <- a.loop() }()
	app.Main()
	return <-errc
}

func newApp(opts Options, w *app.Window) *App {
	a := App{opts: opts, th: newTheme(), w: w, prefs: prefs.Load(), conns: map[string]*connState{},
		tipTags: map[string]*tipTag{}, sels: map[string]*selState{}}
	setTheme(a.th, a.prefs.Theme)
	if w != nil {
		a.notifier = notify.New(AppName)
	}
	a.sidebar = newSidebar(&a)
	a.title = newTitleBar(&a)
	a.ai = newAIPanel(&a)
	return &a
}

// start restores the workspace and opens the requested connection.
func (a *App) start() {
	if err := a.prefs.LoadError; err != nil {
		a.flash("Settings reset: "+err.Error(), true)
	}
	a.startUpdateChecks()
	a.restoreTabs()
	if n := a.opts.Connection; n != "" {
		for _, c := range a.prefs.Connections {
			if c.Name == n || c.ID == n {
				id := c.ID
				a.connect(id, func(cs *connState) {
					if a.findTab(func(t Tab) bool { return t.ConnID() == id }) < 0 {
						a.newConsole(id, "", "")
					}
				})
				return
			}
		}
		a.flash("No saved connection named "+n, true)
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
	a.saveWorkspace()
	for _, t := range a.tabs {
		t.Dispose()
	}
	for _, cs := range a.conns {
		if cs.conn != nil {
			cs.conn.Close()
		}
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
func (a *App) do(desc string, fn func() error, then func()) {
	a.flash(desc+"…", false)
	go func() {
		err := fn()
		a.post(func() {
			if err != nil {
				a.flash(fmt.Sprintf("%s failed: %v", desc, err), true)
				return
			}
			a.flash(desc+" — done", false)
			if then != nil {
				then()
			}
		})
	}()
}

// copyLater copies text on the next frame (needs a gtx).
func (a *App) copyLater(s string) {
	a.pendingCopy = &s
	a.invalidate()
}

// textFocused reports whether a text input currently owns the keyboard.
func (a *App) textFocused(gtx C) bool {
	if a.overlay != nil {
		return true
	}
	if t := a.activeTab(); t != nil && t.Focused(gtx) {
		return true
	}
	return a.sidebar.focused(gtx) || a.ai.focused(gtx)
}

// dismissOverlay closes the overlay from outside (Esc, backdrop).
func (a *App) dismissOverlay(gtx C) {
	if c, ok := a.overlay.(canceler); ok {
		c.cancelled()
	}
	a.overlay = nil
	gtx.Execute(key.FocusCmd{})
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
	if !a.saveAt.IsZero() && gtx.Now.After(a.saveAt) {
		a.saveAt = time.Time{}
		a.saveWorkspace()
	} else if !a.saveAt.IsZero() {
		gtx.Execute(op.InvalidateCmd{At: a.saveAt})
	}
}

// layoutModal draws the active overlay over a dimmed backdrop. A click on
// the backdrop (outside the dialog) dismisses it, like Esc.
func (a *App) layoutModal(gtx C) {
	gtx.Constraints.Min = gtx.Constraints.Max
	size := gtx.Constraints.Max
	ov := a.overlay

	// lay out the dialog first (recorded) to know its bounds
	m := op.Record(gtx.Ops)
	top := gtx.Dp(70)
	dg := gtx
	dg.Constraints.Min = image.Point{}
	dg.Constraints.Max.Y = max(0, size.Y-top-gtx.Dp(20))
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
			a.dismissOverlay(gtx)
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
	if len(a.tabs) == 0 {
		return a.welcome.layout(gtx, a)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.layoutTabs),
		layout.Flexed(1, func(gtx C) D {
			gtx.Constraints.Min = gtx.Constraints.Max
			if a.tabIdx < 0 || a.tabIdx >= len(a.tabs) { // the last tab was closed by the strip
				return D{Size: gtx.Constraints.Max}
			}
			return a.tabs[a.tabIdx].Layout(gtx)
		}),
	)
}

func (a *App) layoutStatus(gtx C) D {
	h := gtx.Dp(24)
	gtx.Constraints.Min.Y, gtx.Constraints.Max.Y = h, h
	fill(gtx, pal.Header)
	fillRect(gtx, pal.Border, image.Rect(0, 0, gtx.Constraints.Max.X, gtx.Dp(1)))
	th := a.th
	fg := pal.TextDim
	center := func(w layout.Widget) layout.Widget {
		return func(gtx C) D {
			gtx.Constraints.Min.Y = h
			return layout.W.Layout(gtx, w)
		}
	}
	if a.updBtn.Clicked(gtx) && a.upd.rel != nil {
		a.overlay = newUpdateDialog(a)
	}
	t := a.activeTab()
	var cs *connState
	if t != nil {
		cs = a.conns[t.ConnID()]
	}
	return insetHV(10, 0).Layout(gtx, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(center(func(gtx C) D {
				if cs == nil {
					return iconText(gtx, th, icCloudOff, 11, fg, "no connection")
				}
				c := pal.TextDim
				switch cs.status {
				case stReady:
					c = pal.Ok
				case stConnecting:
					c = pal.Warn
				case stFailed:
					c = pal.Err
				}
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return dot(gtx, c, 7) }),
					layout.Rigid(layout.Spacer{Width: 6}.Layout),
					layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontSans, 11, pal.Text, cs.name()) }),
				)
			})),
			layout.Rigid(center(func(gtx C) D {
				if cs == nil || cs.conn == nil {
					return D{}
				}
				return layout.Inset{Left: 14}.Layout(gtx, func(gtx C) D {
					return lbl(gtx, th, fontSans, 11, fg, cs.conn.ServerVersion())
				})
			})),
			layout.Rigid(center(func(gtx C) D {
				if t == nil {
					return D{}
				}
				s := t.StatusInfo()
				if s == "" {
					return D{}
				}
				return layout.Inset{Left: 14}.Layout(gtx, func(gtx C) D { return lbl(gtx, th, fontSans, 11, fg, s) })
			})),
			layout.Rigid(layout.Spacer{Width: 20}.Layout),
			layout.Flexed(1, center(func(gtx C) D {
				if a.status == "" || time.Since(a.statusAt) > 8*time.Second && !a.statusErr {
					return D{}
				}
				if a.statusErr {
					return iconText(gtx, th, icError, 11, pal.Err, a.status)
				}
				return iconText(gtx, th, icStatusDone, 11, pal.Text, a.status)
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
						return rbg(gtx, pal.Accent, 4, func(gtx C) D {
							return insetHV(6, 2).Layout(gtx, func(gtx C) D {
								return iconText(gtx, th, icDownload, 11, pal.OnAccent, txt)
							})
						})
					})
				})
			})),
			layout.Rigid(center(func(gtx C) D {
				hints := [][2]string{{shortcutLabel("K"), "commands"}, {shortcutLabel("Enter"), "run"}, {shortcutLabel("T"), "console"}}
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
		layout.Rigid(func(gtx C) D { return keyChip(gtx, th, k) }),
		layout.Rigid(layout.Spacer{Width: 4}.Layout),
		layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontSans, 11, pal.TextDim, what) }),
	)
}

func (a *App) invalidate() {
	if a.w != nil {
		a.w.Invalidate()
	} else if a.onInvalidate != nil {
		a.onInvalidate()
	}
}

// setThemeName switches and persists the theme.
func (a *App) setThemeName(name string) {
	a.prefs.Theme = name
	setTheme(a.th, name)
	a.savePrefs()
	a.invalidate()
}

// toggleTheme cycles dark → mirage → light.
func (a *App) toggleTheme() {
	for i, t := range Themes {
		if t.ID == a.prefs.Theme {
			a.setThemeName(Themes[(i+1)%len(Themes)].ID)
			return
		}
	}
	a.setThemeName(Themes[0].ID)
}

func (a *App) savePrefs() {
	if err := a.prefs.Save(); err != nil {
		a.flash("save preferences: "+err.Error(), true)
	}
}

// scheduleSave saves the workspace shortly (debounced).
func (a *App) scheduleSave() {
	if a.saveAt.IsZero() {
		a.saveAt = time.Now().Add(2 * time.Second)
		a.invalidate()
	}
}
