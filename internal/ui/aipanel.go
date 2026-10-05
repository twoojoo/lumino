package ui

import (
	"gioui.org/layout"

	"github.com/giovanni/lumino/internal/db"
)

// AIPanel is the assistant side panel (filled in by ai.go).
type AIPanel struct {
	app  *App
	open bool
}

func newAIPanel(a *App) *AIPanel { return &AIPanel{app: a} }

func (p *AIPanel) focused(gtx C) bool { return false }

func (p *AIPanel) Layout(gtx C) D {
	w := gtx.Dp(380)
	gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
	gtx.Constraints.Min.Y = gtx.Constraints.Max.Y
	fill(gtx, pal.Sidebar)
	return layout.Center.Layout(gtx, func(gtx C) D { return lbl(gtx, p.app.th, fontSans, 12, pal.TextDim, "Assistant") })
}

func (a *App) askAboutSQL(connID, sql string, failed *resultSet) { a.ai.open = true }

func (a *App) askAboutObject(connID string, ref db.ObjectRef) { a.ai.open = true }
