package ui

import (
	"fmt"
	"image"

	"gioui.org/layout"
)

// protected reports whether the connected context is production-like.
func (a *App) protected() bool {
	return a.cluster != nil && a.prefs.Protected(a.cluster.Context)
}

// readOnly reports whether the connected context forbids mutations.
func (a *App) readOnly() bool {
	return a.cluster != nil && a.prefs.ReadOnly(a.cluster.Context)
}

// allowMutation is the gate in front of every state-changing action.
func (a *App) allowMutation(what string) bool {
	if a.readOnly() {
		a.flash(fmt.Sprintf("%s blocked: context %s is read-only", what, a.cluster.Context), true)
		return false
	}
	return true
}

// guard applies the context policy to a confirmation: on protected
// contexts the user must type the resource name.
func (a *App) guard(c *Confirm, path string) *Confirm {
	if a.protected() {
		c.typeName = shortName(path)
		c.prodCtx = a.cluster.Context
		c.danger = true
	}
	return c
}

// confirmMutation shows a policy-aware confirmation, then runs fn.
func (a *App) confirmMutation(title, msg, path string, danger bool, fn func()) {
	if !a.allowMutation(title) {
		return
	}
	a.overlay = a.guard(newConfirm(a, title, msg, danger, func(bool) { fn() }), path)
}

// layoutPolicyFrame outlines the window on protected contexts.
func (a *App) layoutPolicyFrame(gtx C) {
	if !a.protected() {
		return
	}
	sz := gtx.Constraints.Max
	t := gtx.Dp(2)
	c := pal.Err
	fillRect(gtx, c, image.Rect(0, 0, sz.X, t))
	fillRect(gtx, c, image.Rect(0, sz.Y-t, sz.X, sz.Y))
	fillRect(gtx, c, image.Rect(0, 0, t, sz.Y))
	fillRect(gtx, c, image.Rect(sz.X-t, 0, sz.X, sz.Y))
}

// policyBadges draws PRODUCTION / READ-ONLY pills.
func (a *App) policyBadges(gtx C) D {
	if a.cluster == nil {
		return D{}
	}
	th := a.th
	var ch []layout.FlexChild
	if a.protected() {
		ch = append(ch, layout.Rigid(func(gtx C) D {
			return layout.Inset{Right: 6}.Layout(gtx, func(gtx C) D {
				return rbg(gtx, pal.Err, 5, func(gtx C) D {
					return insetHV(7, 2).Layout(gtx, func(gtx C) D { return iconText(gtx, th, icWarning, 11, pal.OnAccent, "PRODUCTION") })
				})
			})
		}))
	}
	if a.readOnly() {
		ch = append(ch, layout.Rigid(func(gtx C) D {
			return rbg(gtx, pal.Warn, 5, func(gtx C) D {
				return insetHV(7, 2).Layout(gtx, func(gtx C) D { return iconText(gtx, th, icLock, 11, pal.OnAccent, "READ-ONLY") })
			})
		}))
	}
	if len(ch) == 0 {
		return D{}
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx, ch...)
}
