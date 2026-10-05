package ui

import (
	"gioui.org/io/pointer"
	"image"
	"strings"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"golang.org/x/exp/shiny/materialdesign/icons"
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
	icDashboard  = mustIcon(icons.ActionDashboard)
	icNode       = mustIcon(icons.HardwareComputer)
	icNamespace  = mustIcon(icons.FileFolder)
	icEvent      = mustIcon(icons.ActionEvent)
	icPod        = mustIcon(icons.DeviceWidgets)
	icDeploy     = mustIcon(icons.MapsLayers)
	icDaemon     = mustIcon(icons.HardwareDeveloperBoard)
	icStateful   = mustIcon(icons.DeviceStorage)
	icReplica    = mustIcon(icons.ActionViewModule)
	icJob        = mustIcon(icons.AVPlayArrow)
	icCron       = mustIcon(icons.ActionSchedule)
	icConfig     = mustIcon(icons.ActionSettings)
	icConfigMap  = mustIcon(icons.EditorInsertDriveFile)
	icSecret     = mustIcon(icons.CommunicationVPNKey)
	icQuota      = mustIcon(icons.ImageTune)
	icHPA        = mustIcon(icons.ActionTrendingUp)
	icPDB        = mustIcon(icons.ActionVerifiedUser)
	icPriority   = mustIcon(icons.EditorFormatListBulleted)
	icNetwork    = mustIcon(icons.HardwareRouter)
	icService    = mustIcon(icons.ActionSettingsEthernet)
	icEndpoint   = mustIcon(icons.CommunicationCallSplit)
	icIngress    = mustIcon(icons.SocialPublic)
	icNetPol     = mustIcon(icons.ActionLock)
	icStorage    = mustIcon(icons.DeviceStorage)
	icPVC        = mustIcon(icons.FileCloudQueue)
	icPV         = mustIcon(icons.FileCloud)
	icSC         = mustIcon(icons.ActionViewList)
	icAccess     = mustIcon(icons.ActionSupervisorAccount)
	icSA         = mustIcon(icons.SocialPerson)
	icRole       = mustIcon(icons.ActionAssignment)
	icBinding    = mustIcon(icons.ContentLink)
	icCRD        = mustIcon(icons.ActionExtension)
	icCluster    = mustIcon(icons.FileCloudDone)
	icSearch     = mustIcon(icons.ActionSearch)
	icClose      = mustIcon(icons.NavigationClose)
	icExpand     = mustIcon(icons.NavigationExpandMore)
	icCollapse   = mustIcon(icons.NavigationChevronRight)
	icUp         = mustIcon(icons.NavigationArrowUpward)
	icDown       = mustIcon(icons.NavigationArrowDownward)
	icLogs       = mustIcon(icons.EditorShortText)
	icTerminal   = mustIcon(icons.ActionCode)
	icDescribe   = mustIcon(icons.ActionDescription)
	icYAML       = mustIcon(icons.ActionCode)
	icEdit       = mustIcon(icons.ImageEdit)
	icDelete     = mustIcon(icons.ActionDelete)
	icScale      = mustIcon(icons.ActionSwapHoriz)
	icRestart    = mustIcon(icons.NavigationRefresh)
	icForward    = mustIcon(icons.CommunicationImportExport)
	icWarning    = mustIcon(icons.AlertWarning)
	icError      = mustIcon(icons.AlertError)
	icInfo       = mustIcon(icons.ActionInfo)
	icCopy       = mustIcon(icons.ContentArchive)
	icClear      = mustIcon(icons.ContentClear)
	icFollow     = mustIcon(icons.NavigationArrowDownward)
	icCPU        = mustIcon(icons.HardwareMemory)
	icMem        = mustIcon(icons.HardwareDeveloperBoard)
	icCommand    = mustIcon(icons.NavigationApps)
	icCloudOff   = mustIcon(icons.FileCloudOff)
	icStatusDone = mustIcon(icons.FileCloudDone)
	icEye        = mustIcon(icons.ActionVisibility)
	icEyeOff     = mustIcon(icons.ActionVisibilityOff)
	icCopyText   = mustIcon(icons.ContentContentCopy)
	icDownload   = mustIcon(icons.FileFileDownload)
	icBell       = mustIcon(icons.SocialNotifications)
	icBellOff    = mustIcon(icons.SocialNotificationsOff)
	icBellActive = mustIcon(icons.SocialNotificationsActive)
	icCordon     = mustIcon(icons.ContentBlock)
	icUncordon   = mustIcon(icons.AVPlayCircleOutline)
	icDrain      = mustIcon(icons.ActionEject)
	icHelm       = mustIcon(icons.ContentUnarchive)
	icHistory    = mustIcon(icons.ActionHistory)
	icRollback   = mustIcon(icons.ContentUndo)
	icXRay       = mustIcon(icons.HardwareDeviceHub)
	icTree       = mustIcon(icons.ActionTOC)
	icBack       = mustIcon(icons.NavigationArrowBack)
	icValues     = mustIcon(icons.ImageTune)
	icContainer  = mustIcon(icons.DeviceWidgets)
	icBug        = mustIcon(icons.ActionBugReport)
	icOK         = mustIcon(icons.ActionCheckCircle)
	icFilter     = mustIcon(icons.ContentFilterList)
	icLock       = mustIcon(icons.ActionLock)
	icSun        = mustIcon(icons.ImageBrightness7)
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
)

var icExpandLess = mustIcon(icons.NavigationExpandLess)

var gvrIcons = map[string]*widget.Icon{
	overviewGVR:               icDashboard,
	"v1/nodes":                icNode,
	"v1/namespaces":           icNamespace,
	"events.k8s.io/v1/events": icEvent,
	"v1/pods":                 icPod,
	"apps/v1/deployments":     icDeploy,
	"apps/v1/daemonsets":      icDaemon,
	"apps/v1/statefulsets":    icStateful,
	"apps/v1/replicasets":     icReplica,
	"batch/v1/jobs":           icJob,
	"batch/v1/cronjobs":       icCron,
	"v1/configmaps":           icConfigMap,
	"v1/secrets":              icSecret,
	"v1/resourcequotas":       icQuota,
	"v1/limitranges":          icQuota,
	"autoscaling/v2/horizontalpodautoscalers":           icHPA,
	"policy/v1/poddisruptionbudgets":                    icPDB,
	"scheduling.k8s.io/v1/priorityclasses":              icPriority,
	"v1/services":                                       icService,
	"discovery.k8s.io/v1/endpointslices":                icEndpoint,
	"networking.k8s.io/v1/ingresses":                    icIngress,
	"networking.k8s.io/v1/ingressclasses":               icIngress,
	"networking.k8s.io/v1/networkpolicies":              icNetPol,
	"v1/persistentvolumeclaims":                         icPVC,
	"v1/persistentvolumes":                              icPV,
	"storage.k8s.io/v1/storageclasses":                  icSC,
	"v1/serviceaccounts":                                icSA,
	"rbac.authorization.k8s.io/v1/roles":                icRole,
	"rbac.authorization.k8s.io/v1/rolebindings":         icBinding,
	"rbac.authorization.k8s.io/v1/clusterroles":         icRole,
	"rbac.authorization.k8s.io/v1/clusterrolebindings":  icBinding,
	"apiextensions.k8s.io/v1/customresourcedefinitions": icCRD,
	"helm":          icHelm,
	"sanitizer":     icBug,
	"rbac-explorer": icAccess,
	"helm-history":  icHistory,
	"containers":    icTerminal,
}

// kindIcons maps xray node kinds (resource plurals) to icons.
var kindIcons = map[string]*widget.Icon{
	"deployments":            icDeploy,
	"replicasets":            icReplica,
	"statefulsets":           icStateful,
	"daemonsets":             icDaemon,
	"pods":                   icPod,
	"containers":             icTerminal,
	"services":               icService,
	"configmaps":             icConfigMap,
	"secrets":                icSecret,
	"serviceaccounts":        icSA,
	"persistentvolumeclaims": icPVC,
	"namespaces":             icNamespace,
	"nodes":                  icNode,
}

var sectionIcons = map[string]*widget.Icon{
	"Cluster":          icCluster,
	"Workloads":        icPod,
	"Config":           icConfig,
	"Network":          icNetwork,
	"Storage":          icStorage,
	"Access Control":   icAccess,
	"Helm":             icHelm,
	"Bookmarks":        icBookmark,
	"Custom Resources": icCRD,
}

func iconFor(gvr string) *widget.Icon {
	if ic, ok := gvrIcons[gvr]; ok {
		return ic
	}
	return icCRD
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

// statusTone classifies a k8s status string for coloring.
func statusTone(s string) (colorNRGBA, bool) {
	switch strings.ToLower(s) {
	case "running", "active", "bound", "ready", "available", "true", "healthy", "ok", "normal":
		return pal.Ok, true
	case "completed", "succeeded", "complete", "released":
		return pal.TextDim, true
	case "pending", "containercreating", "podinitializing", "terminating", "unknown",
		"suspended", "notready", "schedulingdisabled", "warning":
		return pal.Warn, true
	}
	l := strings.ToLower(s)
	switch {
	case strings.HasPrefix(l, "init:"):
		if strings.Contains(l, "error") || strings.Contains(l, "crash") {
			return pal.Err, true
		}
		return pal.Warn, true
	case strings.Contains(l, "error"), strings.Contains(l, "crash"), strings.Contains(l, "backoff"),
		strings.Contains(l, "failed"), strings.Contains(l, "oomkilled"), strings.Contains(l, "evicted"),
		strings.Contains(l, "invalid"), l == "lost":
		return pal.Err, true
	}
	return colorNRGBA{}, false
}

// pill draws a status pill with a dot.
func pill(gtx C, th *material.Theme, txt string, c colorNRGBA) D {
	gtx.Constraints.Min = image.Point{}
	return rbg(gtx, withAlpha(c, 0x2a), 10, func(gtx C) D {
		return insetHV(8, 2).Layout(gtx, func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return dot(gtx, c, 6) }),
				layout.Rigid(layout.Spacer{Width: 5}.Layout),
				layout.Rigid(func(gtx C) D { return lbl(gtx, th, fontSans, 12, c, txt) }),
			)
		})
	})
}

// avatar draws a Lens-style colored square with the cluster initials.
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
