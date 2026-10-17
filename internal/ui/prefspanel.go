package ui

import (
	"fmt"
	"strconv"
	"strings"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/twoojoo/lumino/internal/prefs"
	"github.com/twoojoo/lumino/internal/secrets"
)

// PrefsPanel edits appearance, data and update preferences.
type PrefsPanel struct {
	app  *App
	done bool
	d    dialog

	themeBtns             []widget.Clickable
	fontBtns              [5]widget.Clickable
	scaleBtns             [8]widget.Clickable
	fetch, maxRows, notif widget.Editor
	updAutoBtn, updNowBtn widget.Clickable
	orderPKBtn            widget.Clickable
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
	f, m, n, err := parsePrefNumbers(p.fetch.Text(), p.maxRows.Text(), p.notif.Text())
	if err != "" {
		p.err = err
		return
	}
	p.err = ""
	a.prefs.FetchSize, a.prefs.MaxRows, a.prefs.NotifyAfter = f, m, n
	a.savePrefs()
	p.done = true
}

// parsePrefNumbers validates the numeric preferences (surrounding spaces
// and "_" digit separators are tolerated); err is "" when they are valid.
func parsePrefNumbers(fetch, maxRows, notify string) (f, m, n int, err string) {
	num := func(s string, lo, hi int, name string) (int, bool) {
		s = strings.TrimSpace(s)
		s = strings.ReplaceAll(s, "_", "")
		v, e := strconv.Atoi(s)
		if e != nil || v < lo || v > hi {
			if err == "" {
				err = name + ": enter a number between " + strconv.Itoa(lo) + " and " + strconv.Itoa(hi)
			}
			return 0, false
		}
		return v, true
	}
	f, ok1 := num(fetch, 50, 100000, "Rows per page")
	m, ok2 := num(maxRows, 1000, 50_000_000, "Rows kept in memory")
	n, ok3 := num(notify, 1, 3600, "Notify after")
	if !ok1 || !ok2 || !ok3 {
		return 0, 0, 0, err
	}
	if f > m {
		return 0, 0, 0, "Rows per page can't exceed the rows kept in memory"
	}
	return f, m, n, ""
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
	for i := range uiScales {
		if p.scaleBtns[i].Clicked(gtx) {
			a.setUIScale(uiScales[i])
		}
	}
	if p.updAutoBtn.Clicked(gtx) {
		a.prefs.SetUpdates(!a.prefs.UpdatesEnabled())
		a.savePrefs()
		if a.prefs.UpdatesEnabled() {
			a.checkUpdates(false)
		}
	}
	if p.orderPKBtn.Clicked(gtx) {
		a.prefs.SetOrderByPrimaryKey(!a.prefs.OrderByPrimaryKey())
		a.savePrefs()
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
	scaleSel, scaleNames := 0, make([]string, len(uiScales))
	for i, s := range uiScales {
		scaleNames[i] = fmt.Sprintf("%d%%", int(s*100+0.5))
		if s == a.uiScale() {
			scaleSel = i
		}
	}
	lw := unitDp(150)
	keyring := "System keyring available: saved passwords persist securely."
	if !secrets.Available() {
		keyring = "No system keyring: passwords are kept for the session only."
	}
	return p.d.layout(gtx, th, 640, icSettings, "Preferences", func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return sectionTitle(gtx, th, "Appearance") }),
			layout.Rigid(func(gtx C) D {
				return formRow(gtx, th, "Theme", lw, func(gtx C) D { return segmented(gtx, th, p.themeBtns, names, themeSel) })
			}),
			layout.Rigid(func(gtx C) D {
				return formRow(gtx, th, "Editor font size", lw, func(gtx C) D { return segmented(gtx, th, p.fontBtns[:], fontNames, fontSel) })
			}),
			layout.Rigid(func(gtx C) D {
				return formRow(gtx, th, "Interface size", lw, func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx C) D { return segmented(gtx, th, p.scaleBtns[:], scaleNames, scaleSel) }),
						layout.Rigid(func(gtx C) D {
							return layout.Inset{Top: 4}.Layout(gtx, func(gtx C) D {
								return lbl(gtx, th, fontSans, 11, pal.TextDim, "Also with "+shortcutLabel("+")+", "+shortcutLabel("−")+" and "+shortcutLabel("0"))
							})
						}),
					)
				})
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
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: 4}.Layout(gtx, func(gtx C) D {
					return toggle(gtx, th, &p.orderPKBtn, "Sort table data by primary key unless a column is sorted", a.prefs.OrderByPrimaryKey())
				})
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
