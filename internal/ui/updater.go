package ui

import (
	"context"
	"fmt"
	"image"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/twoojoo/lumino/internal/safety"
	"github.com/twoojoo/lumino/internal/update"
)

// updState is the self-update state shown in the status bar.
type updState struct {
	rel         *update.Release // newer release, nil = none known
	plan        update.Plan
	checking    bool
	downloading bool
	done, total int64
	ready       bool // installed, restart pending
	err         string
}

// updatesAllowed: release builds only (dev builds never touch themselves).
func updatesAllowed() bool { return !safety.Dev && update.IsRelease(Version) }

// startUpdateChecks checks shortly after start and then daily.
func (a *App) startUpdateChecks() {
	if !updatesAllowed() || !a.prefs.UpdatesEnabled() {
		return
	}
	go func() {
		time.Sleep(5 * time.Second)
		for {
			a.post(func() {
				if a.prefs.UpdatesEnabled() {
					a.checkUpdates(false)
				}
			})
			time.Sleep(24 * time.Hour)
		}
	}()
}

// checkUpdates queries the latest release in the background; manual checks
// report "up to date" and errors.
func (a *App) checkUpdates(manual bool) {
	if !updatesAllowed() {
		if manual {
			a.flash("Development build ("+Version+"): updates are disabled", true)
		}
		return
	}
	if a.upd.checking || a.upd.downloading || a.upd.ready {
		return
	}
	a.upd.checking = true
	go func() {
		rel, err := update.Check(context.Background(), Version)
		plan := update.Detect()
		a.post(func() {
			a.upd.checking = false
			switch {
			case err != nil:
				if manual {
					a.flash("Update check failed: "+err.Error(), true)
				}
			case rel == nil:
				if manual {
					a.flash(AppName+" "+Version+" is up to date", false)
				}
			default:
				a.upd.rel, a.upd.plan, a.upd.err = rel, plan, ""
				if manual {
					a.overlay = newUpdateDialog(a)
				}
			}
		})
	}()
}

// UpdateDialog shows the new release and installs it.
type UpdateDialog struct {
	app    *App
	done   bool
	blocks []mdBlock
	md     mdView
	list   widget.List

	installBtn, pageBtn, laterBtn, restartBtn widget.Clickable
}

func newUpdateDialog(a *App) *UpdateDialog {
	d := &UpdateDialog{app: a, blocks: parseMarkdown(a.upd.rel.Notes)}
	d.md.app, d.md.id = a, "update-notes"
	d.list.Axis = layout.Vertical
	return d
}

func (d *UpdateDialog) Done() bool { return d.done }

func (d *UpdateDialog) install() {
	a, u := d.app, &d.app.upd
	u.downloading, u.err, u.done, u.total = true, "", 0, 0
	rel, plan := u.rel, u.plan
	go func() {
		last := time.Time{}
		err := update.Install(context.Background(), rel, plan, func(done, total int64) {
			if time.Since(last) < 150*time.Millisecond && done != total {
				return
			}
			last = time.Now()
			a.post(func() { u.done, u.total = done, total })
		})
		a.post(func() {
			u.downloading = false
			if err != nil {
				u.err = err.Error()
				return
			}
			u.ready = true
		})
	}()
}

func (d *UpdateDialog) restart() {
	a := d.app
	a.shutdown()
	if err := update.Restart(a.upd.plan); err != nil {
		a.flash("Updated — "+err.Error(), false)
		d.done = true
	}
}

func mb(n int64) string { return fmt.Sprintf("%.1f MB", float64(n)/(1<<20)) }

func (d *UpdateDialog) Layout(gtx C) D {
	a, th, u := d.app, d.app.th, &d.app.upd
	if d.laterBtn.Clicked(gtx) {
		d.done = true
	}
	if d.pageBtn.Clicked(gtx) {
		if err := openBrowser(u.rel.PageURL); err != nil {
			a.flash("open browser: "+err.Error(), true)
		}
	}
	auto := u.plan.Kind != update.KindManual
	if d.installBtn.Clicked(gtx) && auto && !u.downloading && !u.ready {
		d.install()
	}
	if d.restartBtn.Clicked(gtx) && u.ready {
		d.restart()
	}
	w := min(gtx.Dp(620), gtx.Constraints.Max.X-gtx.Dp(40))
	gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
	return rborder(gtx, pal.Border, pal.Panel, 10, func(gtx C) D {
		return inset(18).Layout(gtx, func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					return iconText(gtx, th, icDownload, 16, pal.TextBright, AppName+" "+u.rel.Version+" is available")
				}),
				layout.Rigid(func(gtx C) D {
					return layout.Inset{Top: 4, Bottom: 10}.Layout(gtx, func(gtx C) D {
						return lbl(gtx, th, fontSans, 12, pal.TextDim, "You have "+Version+".")
					})
				}),
				layout.Rigid(func(gtx C) D {
					gtx.Constraints.Max.Y = min(gtx.Constraints.Max.Y, gtx.Dp(320))
					return rbg(gtx, pal.Bg, 8, func(gtx C) D {
						gtx.Constraints.Min.X = gtx.Constraints.Max.X
						return inset(12).Layout(gtx, func(gtx C) D {
							if len(d.blocks) == 0 {
								return lbl(gtx, th, fontSans, 12, pal.TextDim, "No release notes.")
							}
							return material.List(th, &d.list).Layout(gtx, 1, func(gtx C, _ int) D {
								return d.md.layout(gtx, th, d.blocks, pal.Text, nil)
							})
						})
					})
				}),
				layout.Rigid(func(gtx C) D {
					return layout.Inset{Top: 12, Bottom: 12}.Layout(gtx, func(gtx C) D {
						var msg string
						c := pal.TextDim
						switch {
						case u.err != "":
							msg, c = "Update failed: "+u.err, pal.Err
						case u.ready:
							msg, c = "Installed. Restart "+AppName+" to use "+u.rel.Version+" (shells and port-forwards are closed; pinned forwards reopen).", pal.Ok
						case u.downloading && u.total > 0:
							msg = fmt.Sprintf("Downloading… %s / %s", mb(u.done), mb(u.total))
						case u.downloading:
							msg = "Downloading… " + mb(u.done)
						case !auto:
							msg = u.plan.Reason
						default:
							msg = "The download is verified (signed checksums) before it replaces this copy."
						}
						l := material.Body2(th, msg)
						l.TextSize, l.Color = 12, c
						return l.Layout(gtx)
					})
				}),
				layout.Rigid(func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							return pillButton(gtx, th, &d.pageBtn, icOpen, "Release page", pal.Text, pal.Hover)
						}),
						layout.Flexed(1, func(gtx C) D { return D{Size: image.Pt(gtx.Constraints.Min.X, 0)} }),
						layout.Rigid(func(gtx C) D {
							return pillButton(gtx, th, &d.laterBtn, nil, "Later", pal.Text, pal.Hover)
						}),
						layout.Rigid(layout.Spacer{Width: 8}.Layout),
						layout.Rigid(func(gtx C) D {
							switch {
							case u.ready:
								return pillButton(gtx, th, &d.restartBtn, icRestart, "Restart now", pal.OnAccent, pal.Accent)
							case !auto:
								return D{}
							case u.downloading:
								return pillButton(gtx, th, &d.installBtn, icDownload, "Downloading…", pal.TextDim, pal.Hover)
							}
							return pillButton(gtx, th, &d.installBtn, icDownload, "Update now", pal.OnAccent, pal.Accent)
						}),
					)
				}),
			)
		})
	})
}
