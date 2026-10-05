package ui

import (
	"strconv"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/giovanni/lumino/internal/prefs"
	"github.com/giovanni/lumino/internal/secrets"
)

// PrefsPanel edits appearance, data and update preferences.
type PrefsPanel struct {
	app  *App
	done bool
	d    dialog

	themeBtns             []widget.Clickable
	fontBtns              [5]widget.Clickable
	fetch, maxRows, notif widget.Editor
	updAutoBtn, updNowBtn widget.Clickable
	saveBtn, closeBtn     widget.Clickable
	err                   string
}

var fontSizes = []int{12, 13, 14, 15, 16}

func newPrefsPanel(a *App) *PrefsPanel {
	p := &PrefsPanel{app: a, themeBtns: make([]widget.Clickable, len(Themes))}
	for _, e := range []*widget.Editor{&p.fetch, &p.maxRows, &p.notif} {
		e.SingleLine = true
	}
	p.fetch.SetText(strconv.Itoa(a.prefs.Fetch()))
	p.maxRows.SetText(strconv.Itoa(a.prefs.RowLimit()))
	p.notif.SetText(strconv.Itoa(a.prefs.NotifySeconds()))
	return p
}

func (p *PrefsPanel) Done() bool { return p.done }

func (p *PrefsPanel) apply() {
	a := p.app
	num := func(e *widget.Editor, lo, hi int, name string) (int, bool) {
		n, err := strconv.Atoi(e.Text())
		if err != nil || n < lo || n > hi {
			p.err = name + ": enter a number between " + strconv.Itoa(lo) + " and " + strconv.Itoa(hi)
			return 0, false
		}
		return n, true
	}
	f, ok1 := num(&p.fetch, 50, 100000, "Rows per page")
	m, ok2 := num(&p.maxRows, 1000, 50_000_000, "Rows kept in memory")
	n, ok3 := num(&p.notif, 1, 3600, "Notify after")
	if !ok1 || !ok2 || !ok3 {
		return
	}
	a.prefs.FetchSize, a.prefs.MaxRows, a.prefs.NotifyAfter = f, m, n
	a.savePrefs()
	p.done = true
}

func (p *PrefsPanel) Layout(gtx C) D {
	a, th := p.app, p.app.th
	for i := range p.themeBtns {
		if p.themeBtns[i].Clicked(gtx) {
			a.setThemeName(Themes[i].ID)
		}
	}
	for i := range p.fontBtns {
		if p.fontBtns[i].Clicked(gtx) {
			a.prefs.FontSize = fontSizes[i]
			a.savePrefs()
		}
	}
	if p.updAutoBtn.Clicked(gtx) {
		a.prefs.SetUpdates(!a.prefs.UpdatesEnabled())
		a.savePrefs()
		if a.prefs.UpdatesEnabled() {
			a.checkUpdates(false)
		}
	}
	if p.updNowBtn.Clicked(gtx) {
		p.done = true
		a.checkUpdates(true)
	}
	if p.saveBtn.Clicked(gtx) {
		p.apply()
	}
	if p.closeBtn.Clicked(gtx) || p.d.close.Clicked(gtx) {
		p.done = true
	}
	themeSel, names := 0, make([]string, len(Themes))
	for i, t := range Themes {
		names[i] = t.Name
		if t.ID == a.prefs.Theme {
			themeSel = i
		}
	}
	fontSel, fontNames := 1, make([]string, len(fontSizes))
	for i, s := range fontSizes {
		fontNames[i] = strconv.Itoa(s)
		if s == a.prefs.FontSize {
			fontSel = i
		}
	}
	lw := unitDp(170)
	keyring := "System keyring available: saved passwords persist securely."
	if !secrets.Available() {
		keyring = "No system keyring: passwords are kept for the session only."
	}
	return p.d.layout(gtx, th, 600, icSettings, "Preferences", func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return sectionTitle(gtx, th, "Appearance") }),
			layout.Rigid(func(gtx C) D {
				return formRow(gtx, th, "Theme", lw, func(gtx C) D { return segmented(gtx, th, p.themeBtns, names, themeSel) })
			}),
			layout.Rigid(func(gtx C) D {
				return formRow(gtx, th, "Editor font size", lw, func(gtx C) D { return segmented(gtx, th, p.fontBtns[:], fontNames, fontSel) })
			}),
			layout.Rigid(func(gtx C) D { return sectionTitle(gtx, th, "Results") }),
			layout.Rigid(func(gtx C) D {
				return formRow(gtx, th, "Rows per page", lw, func(gtx C) D { return textField(gtx, th, &p.fetch, "500", true) })
			}),
			layout.Rigid(func(gtx C) D {
				return formRow(gtx, th, "Rows kept in memory", lw, func(gtx C) D { return textField(gtx, th, &p.maxRows, "1000000", true) })
			}),
			layout.Rigid(func(gtx C) D {
				return formRow(gtx, th, "Notify after (s)", lw, func(gtx C) D { return textField(gtx, th, &p.notif, "10", true) })
			}),
			layout.Rigid(func(gtx C) D { return sectionTitle(gtx, th, "Security") }),
			layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontSans, 12, pal.Text, keyring) }),
			layout.Rigid(func(gtx C) D { return sectionTitle(gtx, th, "Updates") }),
			layout.Rigid(func(gtx C) D {
				return toggle(gtx, th, &p.updAutoBtn, "Check for new releases (daily, GitHub); updates are verified before installing", a.prefs.UpdatesEnabled())
			}),
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: 6}.Layout(gtx, func(gtx C) D {
					return pillButton(gtx, th, &p.updNowBtn, icDownload, "Check now", pal.Text, pal.Hover)
				})
			}),
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: 14}.Layout(gtx, func(gtx C) D {
					return lbl(gtx, th, fontSans, 11, pal.TextDim, AppName+" "+Version+" · settings in "+shortPath(prefs.Dir()))
				})
			}),
			layout.Rigid(func(gtx C) D {
				if p.err == "" {
					return D{}
				}
				return layout.Inset{Top: 8}.Layout(gtx, func(gtx C) D { return lbl(gtx, th, fontSans, 12, pal.Err, p.err) })
			}),
		)
	}, footerButtons(nil,
		func(gtx C) D { return pillButton(gtx, th, &p.closeBtn, nil, "Close", pal.Text, pal.Hover) },
		func(gtx C) D { return button(gtx, th, &p.saveBtn, "Save", pal.Accent) },
	))
}
