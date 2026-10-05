package ui

import (
	"strings"

	"gioui.org/layout"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"gioui.org/x/richtext"
)

// mdBlock is a parsed Markdown block.
type mdBlock struct {
	code    bool
	lang    string
	heading int
	bullet  bool
	text    string
}

// parseMarkdown splits text into blocks: fenced code, headings, bullets and
// paragraphs. Good enough for chat answers; not a full CommonMark parser.
func parseMarkdown(s string) []mdBlock {
	var out []mdBlock
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	var para []string
	flush := func() {
		if len(para) > 0 {
			out = append(out, mdBlock{text: strings.Join(para, " ")})
			para = nil
		}
	}
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, "```"):
			flush()
			lang := strings.TrimPrefix(t, "```")
			var code []string
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```"); i++ {
				code = append(code, lines[i])
			}
			out = append(out, mdBlock{code: true, lang: lang, text: strings.Join(code, "\n")})
		case t == "":
			flush()
		case strings.HasPrefix(t, "#"):
			flush()
			n := len(t) - len(strings.TrimLeft(t, "#"))
			out = append(out, mdBlock{heading: n, text: strings.TrimSpace(t[n:])})
		case strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") || strings.HasPrefix(t, "• "):
			flush()
			_, rest, _ := strings.Cut(t, " ") // "•" is 3 bytes
			out = append(out, mdBlock{bullet: true, text: strings.TrimSpace(rest)})
		case len(t) > 2 && t[0] >= '0' && t[0] <= '9' && (strings.HasPrefix(t[1:], ". ") || (len(t) > 3 && t[1] >= '0' && t[1] <= '9' && strings.HasPrefix(t[2:], ". "))):
			flush()
			out = append(out, mdBlock{text: t}) // numbered item kept as its own line
		default:
			para = append(para, t)
		}
	}
	flush()
	return out
}

// inlineSpans renders **bold** and `code` spans.
func inlineSpans(s string, size unit.Sp, c colorNRGBA) []richtext.SpanStyle {
	var out []richtext.SpanStyle
	add := func(txt string, f fontT, col colorNRGBA) {
		if txt != "" {
			out = append(out, richtext.SpanStyle{Content: txt, Font: f, Size: size, Color: col})
		}
	}
	for len(s) > 0 {
		ib := strings.Index(s, "**")
		ic := strings.IndexByte(s, '`')
		switch {
		case ic >= 0 && (ib < 0 || ic < ib):
			end := strings.IndexByte(s[ic+1:], '`')
			if end < 0 {
				add(s, fontSans, c)
				return out
			}
			add(s[:ic], fontSans, c)
			add(s[ic+1:ic+1+end], fontMono, pal.Info)
			s = s[ic+2+end:]
		case ib >= 0:
			end := strings.Index(s[ib+2:], "**")
			if end < 0 {
				add(s, fontSans, c)
				return out
			}
			add(s[:ib], fontSans, c)
			add(s[ib+2:ib+2+end], fontBold, pal.TextBright)
			s = s[ib+4+end:]
		default:
			add(s, fontSans, c)
			return out
		}
	}
	return out
}

// mdView lays out parsed Markdown blocks.
type mdView struct {
	rt    richtext.InteractiveText
	codes []widget.Clickable
	// app and id make code blocks selectable (optional)
	app *App
	id  string
}

func (m *mdView) layout(gtx C, th *material.Theme, blocks []mdBlock, c colorNRGBA, onCopy func(string)) D {
	if len(m.codes) < len(blocks) {
		m.codes = make([]widget.Clickable, len(blocks)+4)
	}
	var ch []layout.FlexChild
	for i, b := range blocks {
		i, b := i, b
		ch = append(ch, layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X = 0
			return layout.Inset{Bottom: 6}.Layout(gtx, func(gtx C) D {
				switch {
				case b.code:
					if m.codes[i].Clicked(gtx) && onCopy != nil {
						onCopy(b.text)
					}
					return rbg(gtx, pal.CodeBg, 6, func(gtx C) D {
						gtx.Constraints.Min.X = gtx.Constraints.Max.X
						return inset(8).Layout(gtx, func(gtx C) D {
							return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
								layout.Rigid(func(gtx C) D {
									return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
										layout.Flexed(1, func(gtx C) D {
											return lbl(gtx, th, fontMono, 10, pal.TextDim, b.lang)
										}),
										layout.Rigid(func(gtx C) D { return iconOnly(gtx, &m.codes[i], icCopyText, 13) }),
									)
								}),
								layout.Rigid(func(gtx C) D {
									if m.app != nil {
										return m.app.selText(gtx, m.id+"/code/"+itoa(i), fontMono, 11.5, pal.Text, b.text, 0)
									}
									l := material.Body2(th, b.text)
									l.Font = fontMono
									l.TextSize = 11.5
									l.Color = pal.Text
									return l.Layout(gtx)
								}),
							)
						})
					})
				case b.heading > 0:
					bf := fontBold
					size := unit.Sp(15)
					if b.heading >= 3 {
						size = 13.5
					}
					return richtext.Text(&m.rt, th.Shaper, richtext.SpanStyle{Content: b.text, Font: bf, Size: size, Color: pal.TextBright}).Layout(gtx)
				case b.bullet:
					return layout.Flex{}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							return layout.Inset{Left: 4, Right: 8, Top: 6}.Layout(gtx, func(gtx C) D { return dot(gtx, pal.TextDim, 5) })
						}),
						layout.Flexed(1, func(gtx C) D {
							return richtext.Text(&m.rt, th.Shaper, inlineSpans(b.text, 13, c)...).Layout(gtx)
						}),
					)
				default:
					t := richtext.Text(&m.rt, th.Shaper, inlineSpans(b.text, 13, c)...)
					t.Alignment = text.Start
					return t.Layout(gtx)
				}
			})
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, ch...)
}
