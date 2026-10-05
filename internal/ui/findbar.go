package ui

import (
	"image"
	"strings"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"
)

type findMatch struct {
	a, z    tpos
	current bool
}

// findBar is the editor's find / replace overlay.
type findBar struct {
	e        *CodeEditor
	open     bool
	replace  bool
	query    widget.Editor
	repl     widget.Editor
	caseSens bool
	matches  []findMatch
	cur      int
	version  int // buffer version of matches
	lastQ    string
	lastCase bool

	caseBtn, prevBtn, nextBtn, replBtn, allBtn, closeBtn widget.Clickable
	focusQ                                               bool
}

func newFindBar(e *CodeEditor) *findBar {
	f := &findBar{e: e, cur: -1}
	f.query.SingleLine, f.query.Submit = true, true
	f.repl.SingleLine, f.repl.Submit = true, true
	return f
}

func (f *findBar) focused(gtx C) bool {
	return f.open && (gtx.Focused(&f.query) || gtx.Focused(&f.repl))
}

func (f *findBar) show(gtx C, replace bool, sel string) {
	f.open, f.replace = true, replace
	if sel != "" && !strings.Contains(sel, "\n") {
		f.query.SetText(sel)
	}
	f.query.SetCaret(f.query.Len(), 0)
	f.focusQ = true
	f.version = -1
}

func (f *findBar) close(gtx C) {
	f.open = false
	f.matches = nil
	f.version = -1
	gtx.Execute(key.FocusCmd{Tag: f.e})
}

// search recomputes matches when the query or the text changed.
func (f *findBar) search() {
	q := f.query.Text()
	if f.version == f.e.buf.version && q == f.lastQ && f.caseSens == f.lastCase {
		return
	}
	f.version, f.lastQ, f.lastCase = f.e.buf.version, q, f.caseSens
	f.matches = f.matches[:0]
	if q == "" {
		f.cur = -1
		return
	}
	for ln, s := range f.e.buf.lines {
		if len(f.matches) >= maxFindMatches {
			break
		}
		for _, m := range findAll(s, q, !f.caseSens) {
			f.matches = append(f.matches, findMatch{a: tpos{ln, m[0]}, z: tpos{ln, m[1]}})
		}
	}
	// current: first match at or after the caret
	f.cur = -1
	for i, m := range f.matches {
		if !m.a.before(minPos(f.e.caret, f.e.anchor)) {
			f.cur = i
			break
		}
	}
	if f.cur < 0 && len(f.matches) > 0 {
		f.cur = 0
	}
}

const maxFindMatches = 100000

// findAll returns the byte ranges of the non-overlapping occurrences of q
// in s. Case-insensitive matching lowers rune by rune and maps the offsets
// back to s: lowering can change byte lengths (Ⱥ, İ, the Kelvin sign).
func findAll(s, q string, fold bool) [][2]int {
	var out [][2]int
	if q == "" {
		return nil
	}
	if !fold {
		for off := 0; ; {
			i := strings.Index(s[off:], q)
			if i < 0 {
				return out
			}
			a := off + i
			out = append(out, [2]int{a, a + len(q)})
			off = a + len(q)
		}
	}
	var low strings.Builder
	orig := make([]int, 0, len(s)+1) // lowered byte offset → s offset (-1 inside a rune)
	for i, r := range s {
		l := strings.ToLower(string(r))
		low.WriteString(l)
		orig = append(orig, i)
		for k := 1; k < len(l); k++ {
			orig = append(orig, -1)
		}
	}
	orig = append(orig, len(s))
	hay, needle := low.String(), strings.ToLower(q)
	for off := 0; off < len(hay); {
		i := strings.Index(hay[off:], needle)
		if i < 0 {
			break
		}
		a, z := off+i, off+i+len(needle)
		if orig[a] >= 0 && orig[z] >= 0 {
			out = append(out, [2]int{orig[a], orig[z]})
			off = z
			continue
		}
		off = a + 1
	}
	return out
}

// next selects the next (or previous) match.
func (f *findBar) next(forward bool) {
	if !f.open && f.query.Text() == "" {
		return
	}
	f.search()
	if len(f.matches) == 0 {
		return
	}
	if forward {
		start := maxPos(f.e.caret, f.e.anchor)
		f.cur = 0 // wrap around
		for i, m := range f.matches {
			if !m.a.before(start) {
				f.cur = i
				break
			}
		}
	} else {
		f.cur = len(f.matches) - 1
		a := minPos(f.e.caret, f.e.anchor)
		for i := len(f.matches) - 1; i >= 0; i-- {
			if f.matches[i].a.before(a) {
				f.cur = i
				break
			}
		}
	}
	m := f.matches[f.cur]
	f.e.anchor, f.e.caret = m.a, m.z
	f.e.scrollCaret = true
}

func (f *findBar) replaceOne() {
	f.search()
	if f.cur < 0 || f.cur >= len(f.matches) {
		return
	}
	m := f.matches[f.cur]
	// only replace when the match is selected (first click selects it)
	if f.e.anchor != m.a || f.e.caret != m.z {
		f.next(true)
		return
	}
	f.e.replaceRange(m.a, m.z, f.repl.Text(), true)
	f.next(true)
}

func (f *findBar) replaceAll() {
	f.search()
	if len(f.matches) == 0 {
		return
	}
	// rebuild the text once (one undo step)
	var b strings.Builder
	last := tpos{}
	r := f.repl.Text()
	for _, m := range f.matches {
		b.WriteString(f.e.buf.slice(last, m.a))
		b.WriteString(r)
		last = m.z
	}
	b.WriteString(f.e.buf.slice(last, f.e.buf.end()))
	n := len(f.matches)
	// keep the caret (shifted by the replacements before it) and the view
	caret := f.e.buf.offset(f.e.caret)
	shift := 0
	for _, m := range f.matches {
		if !f.e.caret.before(m.z) {
			shift += len(r) - (m.z.col - m.a.col) // matches never span lines
		}
	}
	sy, sx := f.e.scrollY, f.e.scrollX
	f.e.replaceRange(tpos{}, f.e.buf.end(), b.String(), true)
	f.e.SetCaretOffset(caret + shift)
	f.e.scrollY, f.e.scrollX = sy, sx
	msg := "Replaced " + itoa(n) + " occurrence(s)"
	if n >= maxFindMatches {
		msg += " (limit reached: run Replace All again)"
	}
	f.e.app.flash(msg, false)
}

func (f *findBar) visibleMatches(first, last int) []findMatch {
	if !f.open {
		return nil
	}
	f.search()
	var out []findMatch
	for i, m := range f.matches {
		if m.a.line > last {
			break
		}
		if m.a.line >= first {
			m.current = i == f.cur
			out = append(out, m)
		}
	}
	return out
}

func (f *findBar) update(gtx C) {
	if !f.open {
		return
	}
	for {
		ev, ok := f.query.Update(gtx)
		if !ok {
			break
		}
		switch ev.(type) {
		case widget.SubmitEvent:
			f.next(true)
		case widget.ChangeEvent:
			f.version = -1
			f.search()
			if f.cur >= 0 {
				m := f.matches[f.cur]
				f.e.anchor, f.e.caret = m.a, m.z
				f.e.scrollCaret = true
			}
		}
	}
	for {
		ev, ok := f.repl.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			f.replaceOne()
		}
	}
	for {
		ev, ok := gtx.Event(
			key.Filter{Focus: &f.query, Name: key.NameEscape},
			key.Filter{Focus: &f.repl, Name: key.NameEscape},
			key.Filter{Focus: &f.query, Name: key.NameReturn, Required: key.ModShift},
		)
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			if e.Name == key.NameEscape {
				f.close(gtx)
			} else {
				f.next(false)
			}
		}
	}
	if f.caseBtn.Clicked(gtx) {
		f.caseSens = !f.caseSens
	}
	if f.nextBtn.Clicked(gtx) {
		f.next(true)
	}
	if f.prevBtn.Clicked(gtx) {
		f.next(false)
	}
	if f.replBtn.Clicked(gtx) {
		f.replaceOne()
	}
	if f.allBtn.Clicked(gtx) {
		f.replaceAll()
	}
	if f.closeBtn.Clicked(gtx) {
		f.close(gtx)
	}
}

func (f *findBar) layout(gtx C, size image.Point) {
	if !f.open {
		return
	}
	th := f.e.app.th
	if f.focusQ {
		f.focusQ = false
		gtx.Execute(key.FocusCmd{Tag: &f.query})
	}
	w := min(gtx.Dp(460), size.X-gtx.Dp(20))
	m := op.Record(gtx.Ops)
	g := gtx
	g.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: image.Pt(w, size.Y)}
	d := rborder(g, pal.Border, pal.Panel, 6, func(gtx C) D {
		return inset(6).Layout(gtx, func(gtx C) D {
			row := func(ed *widget.Editor, hint string, extra ...layout.FlexChild) layout.FlexChild {
				return layout.Rigid(func(gtx C) D {
					ch := []layout.FlexChild{layout.Flexed(1, func(gtx C) D { return textField(gtx, th, ed, hint, true) })}
					ch = append(ch, extra...)
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx, ch...)
				})
			}
			count := "no results"
			if f.query.Text() == "" {
				count = ""
			} else if len(f.matches) > 0 {
				count = itoa(f.cur+1) + "/" + itoa(len(f.matches))
			}
			children := []layout.FlexChild{
				row(&f.query, "Find",
					layout.Rigid(func(gtx C) D {
						gtx.Constraints.Min.X = gtx.Dp(64)
						return layout.Center.Layout(gtx, func(gtx C) D { return lbl(gtx, th, fontSans, 11, pal.TextDim, count) })
					}),
					layout.Rigid(func(gtx C) D {
						b, c := colorNRGBA{}, pal.TextDim
						if f.caseSens {
							b, c = pal.Selected, pal.TextBright
						}
						return pillButton(gtx, th, &f.caseBtn, nil, "Aa", c, b)
					}),
					layout.Rigid(func(gtx C) D { return iconOnly(gtx, &f.prevBtn, icUp, 15) }),
					layout.Rigid(func(gtx C) D { return iconOnly(gtx, &f.nextBtn, icDown, 15) }),
					layout.Rigid(func(gtx C) D { return iconOnly(gtx, &f.closeBtn, icClose, 15) }),
				),
			}
			if f.replace {
				children = append(children,
					layout.Rigid(layout.Spacer{Height: 4}.Layout),
					row(&f.repl, "Replace",
						layout.Rigid(layout.Spacer{Width: 6}.Layout),
						layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &f.replBtn, nil, "Replace", pal.Text, pal.Hover) }),
						layout.Rigid(layout.Spacer{Width: 4}.Layout),
						layout.Rigid(func(gtx C) D { return pillButton(gtx, th, &f.allBtn, nil, "All", pal.Text, pal.Hover) }),
					))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
		})
	})
	call := m.Stop()
	st := op.Offset(image.Pt(size.X-d.Size.X-gtx.Dp(16), gtx.Dp(6))).Push(gtx.Ops)
	call.Add(gtx.Ops)
	st.Pop()
}
