package ui

import (
	"image"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/widget"

	"github.com/giovanni/lumino/internal/db/sqlx"
)

// compItem is a completion candidate.
type compItem struct {
	label  string
	kind   string // keyword function type table view column schema alias
	detail string
	insert string // "" = label
	score  int
}

// compRequest is what the owner answers for the current context.
type compRequest struct {
	items []compItem
}

// completion is the open popup.
type completion struct {
	all     []compItem
	items   []compItem
	sel     int
	top     int
	start   tpos // start of the replaced prefix
	stale   bool // buffer changed under the popup
	fuzzy   bool // opened explicitly (Ctrl+Space): subsequence matches too
	hovered bool
	tag     int
	rowH    int
}

// sqlRef is a table referenced by a statement.
type sqlRef struct {
	schema, name, alias string
}

// sqlContext describes the caret position inside a statement.
type sqlContext struct {
	qualifier   string // "t" in "t.col|"
	qualParts   []string
	qualStart   int // offset where the qualifier chain starts
	prefix      string
	refs        []sqlRef
	expectTable bool // after FROM / JOIN / INTO / UPDATE / TABLE
}

var tableKeywords = map[string]bool{"FROM": true, "JOIN": true, "INTO": true, "UPDATE": true, "TABLE": true, "DESCRIBE": true,
	"DESC": false, "TRUNCATE": true}

var clauseEnd = map[string]bool{"WHERE": true, "GROUP": true, "ORDER": true, "HAVING": true, "LIMIT": true, "ON": true,
	"SET": true, "VALUES": true, "UNION": true, "EXCEPT": true, "INTERSECT": true, "USING": true, "SELECT": true,
	"RETURNING": true, "WINDOW": true, "OFFSET": true, "FETCH": true, "FOR": true}

// unquoteIdent strips identifier quotes.
func unquoteIdent(s string) string {
	if len(s) >= 2 {
		switch {
		case s[0] == '"' && s[len(s)-1] == '"', s[0] == '`' && s[len(s)-1] == '`', s[0] == '[' && s[len(s)-1] == ']':
			return s[1 : len(s)-1]
		}
	}
	return s
}

// analyzeSQL inspects a statement (caret is a byte offset in it).
func analyzeSQL(stmt string, caret int, lex *sqlx.Options, kw map[string]bool) sqlContext {
	var ctx sqlContext
	caret = max(0, min(caret, len(stmt)))
	// prefix and qualifier: identifier chain right before the caret
	i := caret
	for i > 0 {
		r, n := utf8.DecodeLastRuneInString(stmt[:i])
		if !isWordRune(r) {
			break
		}
		i -= n
	}
	ctx.prefix = stmt[i:caret]
	j := i
	// an identifier being typed after an opening quote: u."na, "public"."us,
	// [dbo].[us — the qualifier chain starts before the quote
	if i >= 2 && stmt[i-2] == '.' && (stmt[i-1] == '"' || stmt[i-1] == '`' || stmt[i-1] == '[') {
		j = i - 1
	}
	for j > 0 && stmt[j-1] == '.' {
		k := j - 1
		// quoted part
		if k > 0 && (stmt[k-1] == '"' || stmt[k-1] == '`' || stmt[k-1] == ']') {
			q := stmt[k-1]
			open := q
			if q == ']' {
				open = '['
			}
			s := strings.LastIndexByte(stmt[:k-1], open)
			if s < 0 {
				break
			}
			ctx.qualParts = append([]string{stmt[s+1 : k-1]}, ctx.qualParts...)
			j = s
			continue
		}
		s := k
		for s > 0 {
			r, n := utf8.DecodeLastRuneInString(stmt[:s])
			if !isWordRune(r) {
				break
			}
			s -= n
		}
		if s == k {
			break
		}
		ctx.qualParts = append([]string{stmt[s:k]}, ctx.qualParts...)
		j = s
	}
	ctx.qualStart = j
	if len(ctx.qualParts) > 0 {
		ctx.qualifier = ctx.qualParts[len(ctx.qualParts)-1]
	}

	toks := sqlx.Tokens(stmt, lex)
	type word struct {
		text   string
		start  int
		quoted bool
	}
	var words []word // significant tokens (words, quoted idents, punctuation)
	for _, t := range toks {
		switch t.Kind {
		case sqlx.TSpace, sqlx.TComment:
			continue
		}
		words = append(words, word{text: stmt[t.Start:t.End], start: t.Start, quoted: t.Kind == sqlx.TQuotedIdent})
	}
	isKW := func(w word) bool { return !w.quoted && kw[strings.ToUpper(w.text)] }
	// table references
	for k := 0; k < len(words); k++ {
		up := strings.ToUpper(words[k].text)
		if !tableKeywords[up] || words[k].quoted {
			continue
		}
		k++
		for k < len(words) {
			// identifier chain a.b.c
			var parts []string
			for k < len(words) {
				w := words[k]
				if w.text == "(" || w.text == ")" || w.text == "," || w.text == ";" || isKW(w) {
					break
				}
				if w.text == "." {
					k++
					continue
				}
				parts = append(parts, unquoteIdent(w.text))
				k++
				if k < len(words) && words[k].text != "." {
					break
				}
			}
			if len(parts) == 0 {
				break
			}
			ref := sqlRef{name: parts[len(parts)-1]}
			if len(parts) > 1 {
				ref.schema = parts[len(parts)-2]
			}
			// alias: [AS] name
			aliasOK := func(w word) bool {
				switch w.text {
				case ",", "(", ")", ";", ".":
					return false
				}
				return w.quoted || !isKW(w)
			}
			if k < len(words) && strings.EqualFold(words[k].text, "AS") && !words[k].quoted {
				k++
			}
			if k < len(words) && aliasOK(words[k]) {
				ref.alias = unquoteIdent(words[k].text)
				k++
			}
			ctx.refs = append(ctx.refs, ref)
			if k < len(words) && words[k].text == "," {
				k++
				continue
			}
			break
		}
		k--
	}
	// expected object type from the last significant word before the prefix
	stop := ctx.qualStart
	var last string
	inFrom := false
	for _, w := range words {
		if w.start >= stop {
			break
		}
		up := strings.ToUpper(w.text)
		if tableKeywords[up] && !w.quoted {
			inFrom = up == "FROM"
		} else if clauseEnd[up] && !w.quoted {
			inFrom = false
		}
		last = up
	}
	ctx.expectTable = tableKeywords[last] || last == "," && inFrom
	return ctx
}

// filterItems keeps the items matching prefix, best first (fuzzy).
func filterItems(all []compItem, prefix string) []compItem {
	return filterItemsMode(all, prefix, true)
}

// filterItemsMode keeps the items matching prefix. Without fuzzy (popups
// opened while typing) only the label start or the start of one of its
// words (after "_" or ".") may match: "8" must not become int8.
func filterItemsMode(all []compItem, prefix string, fuzzy bool) []compItem {
	p := strings.ToLower(prefix)
	out := make([]compItem, 0, 64)
	for _, it := range all {
		l := strings.ToLower(it.label)
		switch {
		case p == "":
			it.score = 1
		case l == p:
			it.score = 5
		case strings.HasPrefix(l, p):
			it.score = 4
		case !fuzzy:
			if !wordStartMatch(l, p) {
				continue
			}
			it.score = 3
		case strings.Contains(l, p):
			it.score = 2
		case subsequence(l, p):
			it.score = 1
		default:
			continue
		}
		it.score = it.score*10 + kindRank[it.kind]
		out = append(out, it)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return len(out[i].label) < len(out[j].label)
	})
	if len(out) > 300 {
		out = out[:300]
	}
	// identical exact match only: nothing left to complete
	if len(out) == 1 && strings.EqualFold(out[0].label, prefix) && out[0].insert == "" {
		return nil
	}
	return out
}

var kindRank = map[string]int{"column": 6, "alias": 5, "table": 4, "view": 4, "schema": 3, "function": 2, "keyword": 1, "type": 1}

// wordStartMatch reports whether p starts one of the words of l after
// the first ("_" and "." separate words).
func wordStartMatch(l, p string) bool {
	for i := 0; i < len(l); i++ {
		if (l[i] == '_' || l[i] == '.') && strings.HasPrefix(l[i+1:], p) {
			return true
		}
	}
	return false
}

func subsequence(s, sub string) bool {
	i := 0
	for j := 0; j < len(s) && i < len(sub); j++ {
		if s[j] == sub[i] {
			i++
		}
	}
	return i == len(sub)
}

// triggerCompletion opens (or refreshes) the popup at the caret. Explicit
// requests (Ctrl+Space) match fuzzily; automatic ones (typing) only by
// prefix, never on numbers, comments or string literals.
func (e *CodeEditor) triggerCompletion(explicit bool) {
	e.openCompletion(explicit, explicit)
}

func (e *CodeEditor) openCompletion(explicit, fuzzy bool) {
	if e.Completer == nil || e.ReadOnly {
		return
	}
	line := e.buf.lines[e.caret.line]
	start, _ := e.buf.wordAt(e.caret)
	start.col = min(start.col, e.caret.col)
	prefix := line[start.col:e.caret.col]
	afterDot := start.col > 0 && line[start.col-1] == '.'
	if e.comp != nil && e.comp.fuzzy && e.comp.start == start {
		fuzzy = true // keep the mode of a popup opened explicitly
	}
	if !explicit && prefix == "" && !afterDot && e.comp == nil {
		return
	}
	if !explicit && !afterDot && len(prefix) < 1 {
		e.comp = nil
		return
	}
	if !explicit && !e.completableAt(start, prefix) {
		e.comp = nil
		return
	}
	req := e.Completer(e)
	if req == nil || len(req.items) == 0 {
		e.comp = nil
		return
	}
	items := filterItemsMode(req.items, prefix, fuzzy)
	if len(items) == 0 {
		e.comp = nil
		return
	}
	sel := 0
	if e.comp != nil && e.comp.start == start {
		// keep the selection on the same label while typing
		cur := ""
		if e.comp.sel < len(e.comp.items) {
			cur = e.comp.items[e.comp.sel].label
		}
		for i, it := range items {
			if it.label == cur {
				sel = i
			}
		}
	}
	nc := &completion{all: req.items, items: items, sel: sel, start: start, fuzzy: fuzzy}
	if e.comp != nil && e.comp.start == start {
		nc.top, nc.hovered = e.comp.top, e.comp.hovered
		nc.move(0)
	}
	e.comp = nc
}

// completableAt reports whether typing may open the popup: not on a
// number (LIMIT 8) and not inside a comment or a string literal (quoted
// identifiers do complete).
func (e *CodeEditor) completableAt(start tpos, prefix string) bool {
	if r, _ := utf8.DecodeRuneInString(prefix); prefix != "" && unicode.IsDigit(r) {
		return false
	}
	col := e.caret.col
	if col == 0 {
		return true
	}
	for _, t := range e.tokens(e.caret.line) {
		if t.Start < col && col <= t.End {
			switch t.Kind {
			case sqlx.TComment, sqlx.TString, sqlx.TNumber:
				return false
			}
			break
		}
	}
	return true
}

func (e *CodeEditor) refreshCompletion() {
	if e.comp != nil {
		if e.caret.line != e.comp.start.line || e.caret.col < e.comp.start.col {
			e.comp = nil
			return
		}
		e.openCompletion(true, e.comp.fuzzy)
	}
}

func (c *completion) move(d int) {
	c.sel = max(0, min(len(c.items)-1, c.sel+d))
	if c.sel < c.top {
		c.top = c.sel
	} else if c.sel >= c.top+10 {
		c.top = c.sel - 9
	}
}

// onKey handles navigation keys while the popup is open.
func (c *completion) onKey(k key.Event) bool {
	if k.Modifiers.Contain(key.ModShortcut) {
		return false
	}
	switch k.Name {
	case key.NameUpArrow:
		c.move(-1)
	case key.NameDownArrow:
		c.move(1)
	case key.NamePageUp:
		c.move(-9)
	case key.NamePageDown:
		c.move(9)
	case key.NameReturn, key.NameEnter, key.NameTab:
		return false // handled by acceptOnKey
	default:
		return false
	}
	return true
}

// acceptCompletion inserts the selected item; false when there was
// nothing to accept (the popup is closed).
func (e *CodeEditor) acceptCompletion() bool {
	c := e.comp
	if c != nil && c.stale {
		// the buffer changed under the popup (undo in the same frame…)
		c.stale = false
		e.refreshCompletion()
		c = e.comp
	}
	if c == nil || c.sel >= len(c.items) {
		e.comp = nil
		return false
	}
	if c.start.line != e.caret.line || c.start.line >= e.buf.lineCount() || c.start.col > e.caret.col || c.start.col > len(e.buf.lines[c.start.line]) {
		e.comp = nil
		return false
	}
	it := c.items[c.sel]
	ins := it.insert
	if ins == "" {
		ins = it.label
		if e.dialect != nil && (it.kind == "table" || it.kind == "view" || it.kind == "column" || it.kind == "schema") {
			ins = e.dialect.Quote(it.label)
		}
	}
	e.comp = nil
	_, end := e.buf.wordAt(e.caret)
	end.col = max(end.col, e.caret.col)
	start := c.start
	// an identifier typed after an opening quote: the quote is replaced too
	// (closed around identifiers, dropped for function calls)
	if line := e.buf.lines[start.line]; openQuoteBefore(line, start.col) {
		open := line[start.col-1]
		cl := map[byte]string{'"': `"`, '`': "`", '[': "]"}[open]
		if it.insert == "" {
			ins = string(open) + strings.ReplaceAll(it.label, cl, cl+cl) + cl
		}
		start.col--
		if end.col < len(line) && line[end.col] == cl[0] {
			end.col++
		}
	}
	e.replaceRange(start, end, ins, true)
	if strings.HasSuffix(ins, "()") {
		e.caret.col--
		e.anchor = e.caret
	}
	return true
}

// openQuoteBefore reports whether line[col-1] opens a quoted identifier
// (an odd number of " or ` before it: a closing quote doesn't count).
func openQuoteBefore(line string, col int) bool {
	if col <= 0 || col > len(line) {
		return false
	}
	switch q := line[col-1]; q {
	case '[':
		return true
	case '"', '`':
		return strings.Count(line[:col-1], string(q))%2 == 0
	}
	return false
}

// layoutCompletion draws the popup under the caret (above other content).
func (e *CodeEditor) layoutCompletion(gtx C, textX int, size image.Point) {
	c := e.comp
	if c.stale {
		c.stale = false
		e.refreshCompletion()
		if e.comp == nil {
			return
		}
	}
	th := e.app.th
	rowH := gtx.Dp(24)
	c.rowH = rowH
	n := min(10, len(c.items))
	w := gtx.Dp(380)
	h := n*rowH + gtx.Dp(6)
	line := e.buf.lines[c.start.line]
	x := textX + int(float32(visCol(line, c.start.col))*e.charW) - gtx.Dp(30)
	y := (e.caret.line+1)*e.lineH - e.scrollY + gtx.Dp(2)
	if y+h > size.Y && y-e.lineH-h > 0 {
		y = y - e.lineH - h - gtx.Dp(4)
	}
	x = max(gtx.Dp(2), x)
	m := op.Record(gtx.Ops)
	st := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
	// shadow + frame
	for i, alpha := range []uint8{0x20, 0x14, 0x0a} {
		o := gtx.Dp(unitDp(1 + i))
		fillRRect(gtx, colorNRGBA{A: alpha}, image.Rect(-o, o, w+o, h+2*o), gtx.Dp(6)+o)
	}
	g := gtx
	g.Constraints = layout.Exact(image.Pt(w, h))
	rborder(g, pal.Border, pal.Panel, 6, func(gtx C) D {
		area := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
		event.Op(gtx.Ops, &c.tag)
		for {
			ev, ok := gtx.Event(pointer.Filter{Target: &c.tag, Kinds: pointer.Press | pointer.Enter | pointer.Leave | pointer.Scroll,
				ScrollY: pointer.ScrollRange{Min: -1e5, Max: 1e5}})
			if !ok {
				break
			}
			pe, ok := ev.(pointer.Event)
			if !ok {
				continue
			}
			switch pe.Kind {
			case pointer.Enter:
				c.hovered = true
			case pointer.Leave:
				c.hovered = false
			case pointer.Scroll:
				if pe.Scroll.Y > 0 {
					c.top = min(max(0, len(c.items)-10), c.top+1)
				} else if pe.Scroll.Y < 0 {
					c.top = max(0, c.top-1)
				}
			case pointer.Press:
				i := c.top + (int(pe.Position.Y)-gtx.Dp(3))/rowH
				if i >= 0 && i < len(c.items) {
					c.sel = i
					defer e.acceptCompletion()
					gtx.Execute(key.FocusCmd{Tag: e})
				}
			}
		}
		pointer.CursorPointer.Add(gtx.Ops)
		area.Pop()
		for i := 0; i < n; i++ {
			idx := c.top + i
			if idx >= len(c.items) {
				break
			}
			it := c.items[idx]
			ry := gtx.Dp(3) + i*rowH
			if idx == c.sel {
				fillRRect(gtx, pal.Selected, image.Rect(gtx.Dp(3), ry, w-gtx.Dp(3), ry+rowH), gtx.Dp(4))
			}
			rs := op.Offset(image.Pt(gtx.Dp(8), ry)).Push(gtx.Ops)
			rg := gtx
			rg.Constraints = layout.Exact(image.Pt(w-gtx.Dp(16), rowH))
			layout.Flex{Alignment: layout.Middle}.Layout(rg,
				layout.Rigid(func(gtx C) D { return mdi(gtx, compIcon(it.kind), compColor(it.kind), 14) }),
				layout.Rigid(layout.Spacer{Width: 8}.Layout),
				layout.Flexed(1, func(gtx C) D {
					c := pal.Text
					if idx == e.comp.sel {
						c = pal.TextBright
					}
					return lbl(gtx, th, fontMono, 12.5, c, it.label)
				}),
				layout.Rigid(func(gtx C) D {
					if it.detail == "" {
						return D{}
					}
					gtx.Constraints.Max.X = gtx.Dp(150)
					return layout.Inset{Left: 8}.Layout(gtx, func(gtx C) D { return lbl(gtx, th, fontSans, 11, pal.TextDim, it.detail) })
				}),
			)
			rs.Pop()
		}
		return D{Size: gtx.Constraints.Max}
	})
	st.Pop()
	op.Defer(gtx.Ops, m.Stop())
}

func compIcon(kind string) *widget.Icon {
	switch kind {
	case "table":
		return icTable
	case "view":
		return icView
	case "column":
		return icColumn
	case "schema":
		return icSchema
	case "function":
		return icFunction
	case "alias":
		return icLink
	case "type":
		return icSequence
	}
	return icCommand
}

func compColor(kind string) colorNRGBA {
	switch kind {
	case "table":
		return pal.Info
	case "view":
		return pal.Ok
	case "column":
		return pal.Accent
	case "function":
		return pal.Func
	case "keyword":
		return pal.Keyword
	case "type":
		return pal.Type
	}
	return pal.TextDim
}
