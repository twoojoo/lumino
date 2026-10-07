package ui

import (
	"image"
	"strings"

	"gioui.org/io/pointer"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"golang.org/x/exp/shiny/materialdesign/icons"

	"github.com/giovanni/lumino/internal/db"
)

// Material Design icons (IconVG, already a Gio dependency).
func mustIcon(data []byte) *widget.Icon {
	ic, err := widget.NewIcon(data)
	if err != nil {
		panic(err)
	}
	return ic
}

var (
	icDatabase   = mustIcon(icons.DeviceStorage)
	icSchema     = mustIcon(icons.FileFolder)
	icSchemaOpen = mustIcon(icons.FileFolderOpen)
	icTable      = mustIcon(icons.ImageGridOn)
	icView       = mustIcon(icons.ActionVisibility)
	icMatView    = mustIcon(icons.ActionViewModule)
	icFunction   = mustIcon(icons.EditorFunctions)
	icProcedure  = mustIcon(icons.ActionBuild)
	icSequence   = mustIcon(icons.EditorFormatListNumbered)
	icTrigger    = mustIcon(icons.ImageFlashOn)
	icIndex      = mustIcon(icons.ActionViewList)
	icColumn     = mustIcon(icons.ActionViewColumn)
	icKey        = mustIcon(icons.CommunicationVPNKey)
	icPackage    = mustIcon(icons.ContentArchive)
	icConsole    = mustIcon(icons.ActionCode)
	icRun        = mustIcon(icons.AVPlayArrow)
	icRunAll     = mustIcon(icons.AVPlaylistAdd)
	icSave       = mustIcon(icons.ContentSave)
	icRefresh    = mustIcon(icons.NavigationRefresh)
	icUndo       = mustIcon(icons.ContentUndo)
	icCommit     = mustIcon(icons.ActionDone)
	icExport     = mustIcon(icons.FileFileUpload)
	icPlug       = mustIcon(icons.ActionPowerSettingsNew)
	icLink       = mustIcon(icons.ContentLink)
	icMore       = mustIcon(icons.NavigationMoreVert)
	icDot        = mustIcon(icons.AVFiberManualRecord)
	icChecked    = mustIcon(icons.ToggleCheckBox)
	icUnchecked  = mustIcon(icons.ToggleCheckBoxOutlineBlank)
	icRadioOn    = mustIcon(icons.ToggleRadioButtonChecked)
	icRadioOff   = mustIcon(icons.ToggleRadioButtonUnchecked)
	icFirst      = mustIcon(icons.NavigationFirstPage)
	icLast       = mustIcon(icons.NavigationLastPage)
	icHelp       = mustIcon(icons.ActionHelp)
	icWrap       = mustIcon(icons.EditorWrapText)
	icPaste      = mustIcon(icons.ContentContentPaste)
	icRowAdd     = mustIcon(icons.ContentAddBox)
	icRowDel     = mustIcon(icons.ContentRemoveCircle)
	icSecurity   = mustIcon(icons.HardwareSecurity)
	icCluster    = mustIcon(icons.DeviceStorage)
	icSearch     = mustIcon(icons.ActionSearch)
	icClose      = mustIcon(icons.NavigationClose)
	icExpand     = mustIcon(icons.NavigationExpandMore)
	icCollapse   = mustIcon(icons.NavigationChevronRight)
	icUp         = mustIcon(icons.NavigationArrowUpward)
	icDown       = mustIcon(icons.NavigationArrowDownward)
	icDescribe   = mustIcon(icons.ActionDescription)
	icEdit       = mustIcon(icons.ImageEdit)
	icDelete     = mustIcon(icons.ActionDelete)
	icWarning    = mustIcon(icons.AlertWarning)
	icError      = mustIcon(icons.AlertError)
	icInfo       = mustIcon(icons.ActionInfo)
	icClear      = mustIcon(icons.ContentClear)
	icCommand    = mustIcon(icons.NavigationApps)
	icCloudOff   = mustIcon(icons.FileCloudOff)
	icStatusDone = mustIcon(icons.ActionCheckCircle)
	icEye        = mustIcon(icons.ActionVisibility)
	icEyeOff     = mustIcon(icons.ActionVisibilityOff)
	icCopyText   = mustIcon(icons.ContentContentCopy)
	icDownload   = mustIcon(icons.FileFileDownload)
	icBell       = mustIcon(icons.SocialNotifications)
	icHistory    = mustIcon(icons.ActionHistory)
	icBack       = mustIcon(icons.NavigationArrowBack)
	icOK         = mustIcon(icons.ActionCheckCircle)
	icFilter     = mustIcon(icons.ContentFilterList)
	icLock       = mustIcon(icons.ActionLock)
	icSun        = mustIcon(icons.ImageBrightness7)
	icZoomIn     = mustIcon(icons.ActionZoomIn)
	icZoomOut    = mustIcon(icons.ActionZoomOut)
	icMoon       = mustIcon(icons.ImageBrightness3)
	icBookmark   = mustIcon(icons.ActionBookmark)
	icTab        = mustIcon(icons.ActionTab)
	icOpen       = mustIcon(icons.ActionOpenInBrowser)
	icAdd        = mustIcon(icons.ContentAdd)
	icAI         = mustIcon(icons.ImageFlare)
	icSend       = mustIcon(icons.ContentSend)
	icStop       = mustIcon(icons.AVStop)
	icNewChat    = mustIcon(icons.CommunicationChat)
	icSettings   = mustIcon(icons.ActionSettings)
	icTool       = mustIcon(icons.ActionBuild)
	icIdea       = mustIcon(icons.ActionLightbulbOutline)
	icPerson     = mustIcon(icons.SocialPerson)
	icTree       = mustIcon(icons.ActionTOC)
	icRestart    = mustIcon(icons.NavigationRefresh)
	icForward    = mustIcon(icons.CommunicationImportExport)
	icLogs       = mustIcon(icons.EditorShortText)
)

var icExpandLess = mustIcon(icons.NavigationExpandLess)

// objIcon is the tree/tab icon of an object type.
func objIcon(t db.ObjectType) *widget.Icon {
	switch t {
	case db.ObjTable:
		return icTable
	case db.ObjView:
		return icView
	case db.ObjMatView:
		return icMatView
	case db.ObjFunction:
		return icFunction
	case db.ObjProcedure:
		return icProcedure
	case db.ObjSequence:
		return icSequence
	case db.ObjTrigger:
		return icTrigger
	case db.ObjIndex:
		return icIndex
	case db.ObjPackage:
		return icPackage
	case db.ObjSchema:
		return icSchema
	case db.ObjCatalog:
		return icDatabase
	}
	return icTable
}

// mdi draws a material icon of the given size.
func mdi(gtx C, ic *widget.Icon, c colorNRGBA, size unit.Dp) D {
	s := gtx.Dp(size)
	gtx.Constraints = layout.Exact(image.Pt(s, s))
	return ic.Layout(gtx, c)
}

// mdiInline is mdi nudged up to match the optical center of Go font text
// (labels carry extra descent below the glyphs).
func mdiInline(gtx C, ic *widget.Icon, c colorNRGBA, size unit.Dp) D {
	return mdi(gtx, ic, c, size)
}

// rbg paints a rounded background behind w.
func rbg(gtx C, c colorNRGBA, radius unit.Dp, w layout.Widget) D {
	m := op.Record(gtx.Ops)
	d := w(gtx)
	call := m.Stop()
	r := gtx.Dp(radius)
	paint.FillShape(gtx.Ops, c, clip.UniformRRect(image.Rectangle{Max: d.Size}, r).Op(gtx.Ops))
	call.Add(gtx.Ops)
	return d
}

// rborder paints a rounded 1px border with fill behind w.
func rborder(gtx C, border, fillc colorNRGBA, radius unit.Dp, w layout.Widget) D {
	m := op.Record(gtx.Ops)
	d := w(gtx)
	call := m.Stop()
	r := gtx.Dp(radius)
	b := gtx.Dp(1)
	paint.FillShape(gtx.Ops, border, clip.UniformRRect(image.Rectangle{Max: d.Size}, r).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, fillc, clip.UniformRRect(image.Rect(b, b, d.Size.X-b, d.Size.Y-b), max(0, r-b)).Op(gtx.Ops))
	call.Add(gtx.Ops)
	return d
}

func fillRRect(gtx C, c colorNRGBA, r image.Rectangle, radius int) {
	paint.FillShape(gtx.Ops, c, clip.UniformRRect(r, radius).Op(gtx.Ops))
}

func dot(gtx C, c colorNRGBA, size unit.Dp) D {
	s := gtx.Dp(size)
	fillRRect(gtx, c, image.Rect(0, 0, s, s), s/2)
	return D{Size: image.Pt(s, s)}
}

// iconText lays out icon + text.
func iconText(gtx C, th *material.Theme, ic *widget.Icon, size unit.Sp, c colorNRGBA, txt string) D {
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return mdiInline(gtx, ic, c, unit.Dp(size)+3) }),
		layout.Rigid(layout.Spacer{Width: 6}.Layout),
		layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontSans, size, c, txt) }),
	)
}

// pillButton is a rounded icon+label button.
func pillButton(gtx C, th *material.Theme, cl *widget.Clickable, ic *widget.Icon, txt string, fg, bgc colorNRGBA) D {
	return cl.Layout(gtx, func(gtx C) D {
		b := bgc
		c := onColor(bgc, fg)
		if cl.Hovered() {
			b = pal.Hover
			if bgc.A > 0 {
				b = lighten(bgc, 0.12)
			}
			c = onColor(bgc, pal.TextBright)
		}
		return rbg(gtx, b, 6, func(gtx C) D {
			return insetHV(9, 5).Layout(gtx, func(gtx C) D {
				if ic == nil {
					return lbl(gtx, th, fontSans, 13, c, txt)
				}
				if txt == "" {
					return mdi(gtx, ic, c, 16)
				}
				return iconText(gtx, th, ic, 13, c, txt)
			})
		})
	})
}

// iconOnly is a small round hoverable icon button.
func iconOnly(gtx C, cl *widget.Clickable, ic *widget.Icon, size unit.Dp) D {
	return cl.Layout(gtx, func(gtx C) D {
		c := pal.TextDim
		b := colorNRGBA{}
		if cl.Hovered() {
			c, b = pal.TextBright, pal.Hover
		}
		return rbg(gtx, b, 4, func(gtx C) D {
			return inset(3).Layout(gtx, func(gtx C) D { return mdi(gtx, ic, c, size) })
		})
	})
}

// badge is a small rounded counter/label.
func badge(gtx C, th *material.Theme, txt string, fg, bgc colorNRGBA) D {
	return rbg(gtx, bgc, 9, func(gtx C) D {
		return insetHV(8, 2).Layout(gtx, func(gtx C) D {
			return lbl(gtx, th, fontSans, 11, fg, txt)
		})
	})
}

// avatar draws a colored square with the connection initials.
func avatar(gtx C, th *material.Theme, name string, size unit.Dp) D {
	s := gtx.Dp(size)
	h := 0
	for _, r := range name {
		h = h*31 + int(r)
	}
	if h < 0 {
		h = -h
	}
	cols := []uint32{0x3d90ce, 0x8e5bd6, 0x2f9e72, 0xd2793a, 0xc94f6d, 0x3aa6b9}
	fillRRect(gtx, rgb(cols[h%len(cols)]), image.Rect(0, 0, s, s), gtx.Dp(6))
	ini := initials(name)
	g := gtx
	g.Constraints = layout.Exact(image.Pt(s, s))
	layout.Center.Layout(g, func(gtx C) D {
		return lbl(gtx, th, fontSans, unit.Sp(float32(size)*0.4), pal.OnAccent, ini)
	})
	return D{Size: image.Pt(s, s)}
}

func initials(name string) string {
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' || r == '.' || r == '@' || r == ':' || r == '/' })
	var b []rune
	for _, p := range parts {
		if p == "" {
			continue
		}
		b = append(b, []rune(strings.ToUpper(p))[0])
		if len(b) == 2 {
			break
		}
	}
	if len(b) == 0 {
		return "?"
	}
	return string(b)
}

// handCursor shows the pointer (hand) cursor over a sz area at the origin:
// rows and items whose clicks are handled by a parent area.
func handCursor(gtx C, sz image.Point) {
	defer clip.Rect{Max: sz}.Push(gtx.Ops).Pop()
	pointer.CursorPointer.Add(gtx.Ops)
}
