package ui

import (
	"image"
	"io"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"gioui.org/f32"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/transfer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"github.com/mattn/go-runewidth"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/db/sqlx"
)

const tabWidth = 4

// lineHL caches the tokens of one line and the lexer state at its start.
type lineHL struct {
	start, end sqlx.State
	toks       []sqlx.Token
	dirty      bool // text changed: must be lexed again
	ok         bool // tokens computed (possibly with a stale start state)
}

// CodeEditor is a SQL editor: line-based buffer, incremental syntax
// highlighting, undo/redo, completion, find/replace.
type CodeEditor struct {
	app      *App
	buf      *textBuf
	caret    tpos
	anchor   tpos
	desiredX int // visual column kept by vertical moves (-1 = from caret)
	ReadOnly bool

	scrollY, scrollX int
	scrollCaret      bool
	viewH, viewW     int
	maxVisCols       int

	// metrics for the current font size
	size     unit.Sp
	charW    float32
	lineH    int
	ascent   int
	measured unit.Sp
	gutterW  int

	lex     *sqlx.Options
	dialect db.Dialect
	kw      map[string]bool
	funcs   map[string]bool
	types   map[string]bool
	hl      []lineHL
	hlValid int // lines [0, hlValid) were lexed with their correct start state
	json    bool // highlight JSON instead of SQL (value panel)

	focused     bool
	blinkStart  time.Time
	dragging    bool
	clicks      int
	lastClick   time.Time
	lastClickAt tpos
	sbDrag      bool
	sbGrab      int

	// IME: the snippet is the caret line, offsets are runes in it
	imeLine int
	imeText string
	// skipNL: a Return key press just inserted a newline; an EditEvent "\n"
	// for the same keystroke (platforms sending both) is ignored
	skipNL bool

	comp *completion
	find *findBar

	// decorations set by the owner
	StmtFrom, StmtTo int // lines of the current statement (-1 = none)
	ErrLine          int // line with the last error (-1 = none)
	ErrCol           int

	// owner hooks
	OnCommand     func(cmd string) bool
	OnChange      func()
	OnContextMenu func(at image.Point)
	Completer     func(e *CodeEditor) *compRequest
}

func newCodeEditor(a *App, txt string) *CodeEditor {
	e := &CodeEditor{app: a, desiredX: -1, StmtFrom: -1, StmtTo: -1, ErrLine: -1}
	e.buf = newTextBuf(txt)
	e.buf.onSplice = e.splice
	e.hl = make([]lineHL, e.buf.lineCount())
	for i := range e.hl {
		e.hl[i].dirty = true
	}
	e.setDialect(nil)
	e.find = newFindBar(e)
	return e
}

// setDialect selects lexing rules and keyword sets.
func (e *CodeEditor) setDialect(d db.Dialect) {
	e.dialect = d
	if d == nil {
		e.lex = &sqlx.Options{}
		e.kw = db.CommonKeywords
		e.funcs = toSet(db.CommonFunctions)
		e.types = map[string]bool{}
	} else {
		e.lex = d.Lex()
		e.kw = d.Keywords()
		e.funcs = toSet(d.Functions())
		e.types = toSet(d.Types())
	}
	for i := range e.hl {
		e.hl[i].dirty = true
	}
	e.hlValid = 0
}

func toSet(l []string) map[string]bool {
	m := make(map[string]bool, len(l))
	for _, s := range l {
		m[strings.ToLower(s)] = true
	}
	return m
}

// splice keeps the highlight cache aligned with the buffer lines.
func (e *CodeEditor) splice(from, removed, added int) {
	if from > len(e.hl) {
		from = len(e.hl)
	}
	end := min(from+removed, len(e.hl))
	nl := make([]lineHL, 0, len(e.hl)-(end-from)+added)
	nl = append(nl, e.hl[:from]...)
	for i := 0; i < added; i++ {
		nl = append(nl, lineHL{dirty: true})
	}
	nl = append(nl, e.hl[end:]...)
	e.hl = nl
	e.hlValid = min(e.hlValid, from)
	if e.comp != nil {
		e.comp.stale = true
	}
}

// Text returns the whole text.
func (e *CodeEditor) Text() string { return e.buf.text() }

// SetText replaces the text (not undoable) and moves the caret to the end.
func (e *CodeEditor) SetText(s string) {
	e.buf.setText(s)
	e.caret = e.buf.end()
	e.anchor = e.caret
	e.scrollCaret = true
	e.maxVisCols = 0
}

// Selected returns the selected text.
func (e *CodeEditor) Selected() string {
	if e.caret == e.anchor {
		return ""
	}
	return e.buf.slice(e.anchor, e.caret)
}

// CaretOffset is the caret byte offset in Text().
func (e *CodeEditor) CaretOffset() int { return e.buf.offset(e.caret) }

// SelectionOffsets returns the selection as byte offsets.
func (e *CodeEditor) SelectionOffsets() (int, int) {
	a, z := minPos(e.caret, e.anchor), maxPos(e.caret, e.anchor)
	return e.buf.offset(a), e.buf.offset(z)
}

// SetCaretOffset moves the caret (and clears the selection).
func (e *CodeEditor) SetCaretOffset(off int) {
	e.caret = e.buf.posAt(off)
	e.anchor = e.caret
	e.scrollCaret = true
}

// SelectRange selects [a, z) given as byte offsets.
func (e *CodeEditor) SelectRange(a, z int) {
	e.anchor, e.caret = e.buf.posAt(a), e.buf.posAt(z)
	e.scrollCaret = true
}

// Insert replaces the selection with s (undoable).
func (e *CodeEditor) Insert(s string) {
	e.replaceSel(normalizeNewlines(s), true)
}

func normalizeNewlines(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

func (e *CodeEditor) Focus(gtx C) { gtx.Execute(key.FocusCmd{Tag: e}) }

func (e *CodeEditor) Focused(gtx C) bool {
	return gtx.Focused(e) || e.find.focused(gtx)
}

func (e *CodeEditor) changed() {
	e.desiredX = -1
	e.scrollCaret = true
	e.ErrLine = -1
	if e.OnChange != nil {
		e.OnChange()
	}
}

// replaceSel replaces the selection (or inserts at the caret).
func (e *CodeEditor) replaceSel(s string, brk bool) {
	if e.ReadOnly {
		return
	}
	a, z := minPos(e.caret, e.anchor), maxPos(e.caret, e.anchor)
	end := e.buf.replace(a, z, s, e.caret, e.anchor, brk)
	e.caret, e.anchor = end, end
	e.changed()
}

func (e *CodeEditor) replaceRange(a, z tpos, s string, brk bool) {
	if e.ReadOnly {
		return
	}
	end := e.buf.replace(a, z, s, e.caret, e.anchor, brk)
	e.caret, e.anchor = end, end
	e.changed()
}

// ---- metrics ----

func (e *CodeEditor) measure(gtx C) {
	size := unit.Sp(e.app.prefs.FontSize)
	if size <= 0 {
		size = 13
	}
	e.size = size
	if e.measured == size && e.charW > 0 {
		return
	}
	m := op.Record(gtx.Ops)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(1<<20, 1<<20)}
	d := widget.Label{MaxLines: 1}.Layout(g, e.app.th.Shaper, fontMono, size, strings.Repeat("0", 40), op.CallOp{})
	m.Stop()
	e.charW = float32(d.Size.X) / 40
	e.lineH = int(math.Ceil(float64(gtx.Sp(size)) * 1.55))
	e.ascent = (e.lineH - d.Size.Y) / 2
	e.measured = size
}

// visCol converts a byte column to a visual column (tabs, wide runes).
func visCol(s string, col int) int {
	v := 0
	for i, r := range s {
		if i >= col {
			break
		}
		switch {
		case r == '\t':
			v += tabWidth - v%tabWidth
		case r < 0x80:
			v++
		default:
			v += max(1, runewidth.RuneWidth(r))
		}
	}
	return v
}

// colAtVis converts a visual column to the nearest byte column.
func colAtVis(s string, vis int) int {
	v := 0
	for i, r := range s {
		w := 1
		switch {
		case r == '\t':
			w = tabWidth - v%tabWidth
		case r >= 0x80:
			w = max(1, runewidth.RuneWidth(r))
		}
		if v+w/2 >= vis && v+w > vis || v >= vis {
			if vis-v > w/2 && w > 1 {
				return i + utf8.RuneLen(r)
			}
			return i
		}
		v += w
	}
	return len(s)
}

func expandTabs(s string, startVis int) string {
	if !strings.ContainsRune(s, '\t') {
		return s
	}
	var b strings.Builder
	v := startVis
	for _, r := range s {
		if r == '\t' {
			n := tabWidth - v%tabWidth
			b.WriteString(strings.Repeat(" ", n))
			v += n
			continue
		}
		b.WriteRune(r)
		v++
	}
	return b.String()
}

// ---- highlighting ----

// tokens returns the highlighted tokens of line i, lexing incrementally.
func (e *CodeEditor) tokens(i int) []sqlx.Token {
	if i >= len(e.hl) {
		return nil
	}
	if i < e.hlValid {
		return e.hl[i].toks
	}
	// lex forward from the last line known to be right: a cached line is
	// reused only when its start state still matches
	j := e.hlValid
	var st sqlx.State
	if j > 0 {
		st = e.hl[j-1].end
	}
	e.hlValid = i + 1
	for k := j; k <= i; k++ {
		h := &e.hl[k]
		if h.ok && !h.dirty && h.start == st {
			st = h.end
			continue
		}
		var toks []sqlx.Token
		end := st
		if e.json {
			toks = jsonLexLine(e.buf.lines[k])
		} else {
			toks, end = sqlx.LexLine(e.buf.lines[k], st, e.lex)
		}
		*h = lineHL{start: st, end: end, toks: toks, ok: true}
		st = end
	}
	return e.hl[i].toks
}

// setJSON switches highlighting to JSON.
func (e *CodeEditor) setJSON() {
	e.json = true
	for i := range e.hl {
		e.hl[i].dirty = true
	}
	e.hlValid = 0
}

func (e *CodeEditor) tokColor(line string, toks []sqlx.Token, i int) colorNRGBA {
	t := toks[i]
	if e.json {
		switch t.Kind {
		case sqlx.TQuotedIdent: // object key
			return pal.Func
		case sqlx.TString:
			return pal.String
		case sqlx.TNumber:
			return pal.Number
		case sqlx.TWord:
			return pal.Constant
		case sqlx.TPunct:
			return pal.TextDim
		case sqlx.TOther:
			return pal.Err // not valid JSON
		}
		return pal.Text
	}
	switch t.Kind {
	case sqlx.TComment:
		return pal.Comment
	case sqlx.TString:
		return pal.String
	case sqlx.TNumber:
		return pal.Number
	case sqlx.TOperator:
		return pal.Operator
	case sqlx.TParam:
		return pal.Param
	case sqlx.TPunct:
		return pal.TextDim
	case sqlx.TQuotedIdent:
		return pal.TextBright
	case sqlx.TWord:
		w := line[t.Start:t.End]
		lw := strings.ToLower(w)
		if lw == "null" || lw == "true" || lw == "false" {
			return pal.Constant
		}
		// a word followed by "(" is a call
		next := i + 1
		for next < len(toks) && toks[next].Kind == sqlx.TSpace {
			next++
		}
		call := next < len(toks) && toks[next].Kind == sqlx.TPunct && line[toks[next].Start] == '('
		if call && (e.funcs[lw] || !e.kw[strings.ToUpper(w)]) {
			return pal.Func
		}
		if e.kw[strings.ToUpper(w)] {
			return pal.Keyword
		}
		if e.types[lw] {
			return pal.Type
		}
	}
	return pal.Text
}

// ---- events ----

func (e *CodeEditor) update(gtx C) {
	e.find.update(gtx)
	wasFocused := e.focused
	e.focused = gtx.Focused(e)
	if e.focused != wasFocused {
		e.blinkStart = gtx.Now
		e.buf.breakGroup()
		if !e.focused && e.comp != nil && !e.comp.hovered {
			e.comp = nil
		}
	}
	filters := []event.Filter{
		key.FocusFilter{Target: e},
		transfer.TargetFilter{Target: e, Type: "application/text"},
		key.Filter{Focus: e, Name: key.NameLeftArrow, Optional: key.ModShortcutAlt | key.ModShift | key.ModCtrl},
		key.Filter{Focus: e, Name: key.NameRightArrow, Optional: key.ModShortcutAlt | key.ModShift | key.ModCtrl},
		key.Filter{Focus: e, Name: key.NameUpArrow, Optional: key.ModShift | key.ModAlt | key.ModShortcut},
		key.Filter{Focus: e, Name: key.NameDownArrow, Optional: key.ModShift | key.ModAlt | key.ModShortcut},
		key.Filter{Focus: e, Name: key.NameHome, Optional: key.ModShortcut | key.ModShift},
		key.Filter{Focus: e, Name: key.NameEnd, Optional: key.ModShortcut | key.ModShift},
		key.Filter{Focus: e, Name: key.NamePageUp, Optional: key.ModShift},
		key.Filter{Focus: e, Name: key.NamePageDown, Optional: key.ModShift},
		key.Filter{Focus: e, Name: key.NameDeleteBackward, Optional: key.ModShortcutAlt | key.ModShift | key.ModCtrl},
		key.Filter{Focus: e, Name: key.NameDeleteForward, Optional: key.ModShortcutAlt | key.ModShift | key.ModCtrl},
		key.Filter{Focus: e, Name: key.NameTab, Optional: key.ModShift},
		// plain Return inserts a newline (most platforms send no EditEvent
		// for it), Ctrl/Cmd(+Shift)+Return run statements
		key.Filter{Focus: e, Name: key.NameReturn, Optional: key.ModShortcut | key.ModShift},
		key.Filter{Focus: e, Name: key.NameEnter, Optional: key.ModShortcut | key.ModShift},
		key.Filter{Focus: e, Name: "A", Required: key.ModShortcut},
		key.Filter{Focus: e, Name: "C", Required: key.ModShortcut},
		key.Filter{Focus: e, Name: "X", Required: key.ModShortcut},
		key.Filter{Focus: e, Name: "V", Required: key.ModShortcut},
		key.Filter{Focus: e, Name: "Z", Required: key.ModShortcut, Optional: key.ModShift},
		key.Filter{Focus: e, Name: "Y", Required: key.ModShortcut},
		key.Filter{Focus: e, Name: "D", Required: key.ModShortcut},
		key.Filter{Focus: e, Name: "F", Required: key.ModShortcut},
		key.Filter{Focus: e, Name: "H", Required: key.ModShortcut},
		key.Filter{Focus: e, Name: "K", Required: key.ModShortcut | key.ModShift},
		key.Filter{Focus: e, Name: "/", Required: key.ModShortcut, Optional: key.ModShift},
		key.Filter{Focus: e, Name: key.NameSpace, Required: key.ModCtrl},
		key.Filter{Focus: e, Name: key.NameF3, Optional: key.ModShift},
		key.Filter{Focus: e, Name: "U", Required: key.ModShortcut | key.ModShift},
	}
	if e.comp != nil || e.find.open {
		filters = append(filters, key.Filter{Focus: e, Name: key.NameEscape})
	}
	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			break
		}
		e.blinkStart = gtx.Now
		switch ev := ev.(type) {
		case key.FocusEvent:
			if ev.Focus && !e.ReadOnly {
				gtx.Execute(key.SoftKeyboardCmd{Show: true})
			}
		case key.Event:
			if ev.State == key.Press {
				e.skipNL = false
				e.onKey(gtx, ev)
			}
		case key.EditEvent:
			if e.skipNL && ev.Text == "\n" {
				e.skipNL = false
				continue
			}
			e.skipNL = false
			e.onEdit(ev)
		case key.SnippetEvent:
			// snippets always cover the caret line
		case key.SelectionEvent:
			if e.imeLine == e.caret.line {
				l := e.buf.lines[e.caret.line]
				e.caret = tpos{e.caret.line, runeToByte(l, ev.Start)}
				e.anchor = tpos{e.caret.line, runeToByte(l, ev.End)}
			}
		case transfer.DataEvent:
			if b, err := io.ReadAll(ev.Open()); err == nil {
				e.replaceSel(normalizeNewlines(string(b)), true)
			}
		}
	}
}

func runeToByte(s string, r int) int {
	i := 0
	for r > 0 && i < len(s) {
		_, n := utf8.DecodeRuneInString(s[i:])
		i += n
		r--
	}
	return i
}

func (e *CodeEditor) onEdit(ev key.EditEvent) {
	if e.ReadOnly {
		return
	}
	s := ev.Text
	l := e.buf.lines[e.caret.line]
	// IME replacement of a range of the caret line
	if e.imeLine == e.caret.line && ev.Range.Start != ev.Range.End {
		a := tpos{e.caret.line, runeToByte(l, ev.Range.Start)}
		z := tpos{e.caret.line, runeToByte(l, ev.Range.End)}
		e.replaceRange(a, z, s, false)
		return
	}
	switch s {
	case "\n":
		e.newline()
		return
	case ")", "]", "}":
		// type over an auto-inserted closing bracket
		if e.caret == e.anchor && e.caret.col < len(l) && l[e.caret.col] == s[0] {
			e.caret.col++
			e.anchor = e.caret
			return
		}
	case "(":
		if e.caret == e.anchor {
			next := byte(' ')
			if e.caret.col < len(l) {
				next = l[e.caret.col]
			}
			if next == ' ' || next == ')' || next == ',' || next == ';' || e.caret.col == len(l) {
				e.replaceSel("()", false)
				e.caret.col--
				e.anchor = e.caret
				e.triggerCompletion(false)
				return
			}
		}
	}
	e.replaceSel(s, strings.ContainsAny(s, " \n\t,;"))
	if r, n := utf8.DecodeRuneInString(s); n > 0 && n == len(s) && (isWordRune(r) || r == '.') {
		e.triggerCompletion(false)
	} else if e.comp != nil {
		e.comp = nil
	}
}

// newline inserts a line break keeping the indentation.
func (e *CodeEditor) newline() {
	if e.ReadOnly {
		return
	}
	l := e.buf.lines[e.caret.line]
	ind := indentOf(l[:min(len(l), e.caret.col)])
	trimmed := strings.TrimRight(l[:e.caret.col], " \t")
	if strings.HasSuffix(trimmed, "(") {
		ind += "    "
	}
	e.comp = nil
	e.replaceSel("\n"+ind, true)
}

func (e *CodeEditor) moveTo(p tpos, extend bool) {
	e.caret = e.buf.clamp(p)
	if !extend {
		e.anchor = e.caret
	}
	e.scrollCaret = true
	e.buf.breakGroup()
}

func (e *CodeEditor) pageLines() int {
	if e.lineH == 0 {
		return 20
	}
	return max(1, e.viewH/e.lineH-1)
}

func (e *CodeEditor) vertical(d int, extend bool) {
	if e.desiredX < 0 {
		e.desiredX = visCol(e.buf.lines[e.caret.line], e.caret.col)
	}
	ln := max(0, min(e.buf.lineCount()-1, e.caret.line+d))
	if ln == e.caret.line && d != 0 {
		if d < 0 {
			e.moveTo(tpos{ln, 0}, extend)
		} else {
			e.moveTo(tpos{ln, len(e.buf.lines[ln])}, extend)
		}
		return
	}
	dx := e.desiredX
	e.moveTo(tpos{ln, colAtVis(e.buf.lines[ln], dx)}, extend)
	e.desiredX = dx
}

func (e *CodeEditor) onKey(gtx C, k key.Event) {
	shift := k.Modifiers.Contain(key.ModShift)
	short := k.Modifiers.Contain(key.ModShortcut)
	word := k.Modifiers.Contain(key.ModShortcutAlt) || k.Modifiers.Contain(key.ModCtrl)
	if e.comp != nil && e.comp.onKey(k) {
		return
	}
	if e.comp != nil && !short && (k.Name == key.NameReturn || k.Name == key.NameEnter || k.Name == key.NameTab) {
		if e.acceptCompletion() {
			return
		}
		// the popup went away (stale after an undo…): the key acts normally
	}
	resetX := true
	switch k.Name {
	case key.NameEscape:
		if e.comp != nil {
			e.comp = nil
		} else if e.find.open {
			e.find.close(gtx)
		}
	case key.NameReturn, key.NameEnter:
		if short {
			cmd := "run"
			if shift {
				cmd = "runAll"
			}
			if e.OnCommand != nil {
				e.OnCommand(cmd)
			}
		} else {
			e.newline()
			e.skipNL = true
		}
	case key.NameLeftArrow:
		switch {
		case e.caret != e.anchor && !shift:
			e.moveTo(minPos(e.caret, e.anchor), false)
		case word:
			e.moveTo(e.buf.wordLeft(e.caret), shift)
		default:
			e.moveTo(e.buf.left(e.caret), shift)
		}
		e.comp = nil
	case key.NameRightArrow:
		switch {
		case e.caret != e.anchor && !shift:
			e.moveTo(maxPos(e.caret, e.anchor), false)
		case word:
			e.moveTo(e.buf.wordRight(e.caret), shift)
		default:
			e.moveTo(e.buf.right(e.caret), shift)
		}
		e.comp = nil
	case key.NameUpArrow:
		switch {
		case k.Modifiers.Contain(key.ModAlt):
			e.moveLines(-1)
		case short:
			e.scrollY -= e.lineH
		default:
			e.vertical(-1, shift)
		}
		resetX = false
	case key.NameDownArrow:
		switch {
		case k.Modifiers.Contain(key.ModAlt):
			e.moveLines(1)
		case short:
			e.scrollY += e.lineH
		default:
			e.vertical(1, shift)
		}
		resetX = false
	case key.NamePageUp:
		e.vertical(-e.pageLines(), shift)
		resetX = false
	case key.NamePageDown:
		e.vertical(e.pageLines(), shift)
		resetX = false
	case key.NameHome:
		if short {
			e.moveTo(tpos{}, shift)
		} else {
			// smart home: first non-blank, then column 0
			fn := firstNonSpace(e.buf.lines[e.caret.line])
			col := fn
			if e.caret.col == fn {
				col = 0
			}
			e.moveTo(tpos{e.caret.line, col}, shift)
		}
		e.comp = nil
	case key.NameEnd:
		if short {
			e.moveTo(e.buf.end(), shift)
		} else {
			e.moveTo(tpos{e.caret.line, len(e.buf.lines[e.caret.line])}, shift)
		}
		e.comp = nil
	case key.NameDeleteBackward:
		if e.ReadOnly {
			return
		}
		if e.caret == e.anchor {
			from := e.buf.left(e.caret)
			if word {
				from = e.buf.wordLeft(e.caret)
			} else if l := e.buf.lines[e.caret.line]; e.caret.col > 0 && strings.Trim(l[:e.caret.col], " ") == "" && e.caret.col%4 == 0 {
				from = tpos{e.caret.line, e.caret.col - 4} // unindent
			} else if e.caret.col > 0 && e.caret.col < len(l) {
				// remove an empty bracket pair
				if pair := l[e.caret.col-1 : e.caret.col+1]; pair == "()" || pair == "[]" || pair == "''" {
					e.replaceRange(from, tpos{e.caret.line, e.caret.col + 1}, "", false)
					e.refreshCompletion()
					return
				}
			}
			e.replaceRange(from, e.caret, "", false)
		} else {
			e.replaceSel("", true)
		}
		e.refreshCompletion()
	case key.NameDeleteForward:
		if e.ReadOnly {
			return
		}
		if e.caret == e.anchor {
			to := e.buf.right(e.caret)
			if word {
				to = e.buf.wordRight(e.caret)
			}
			e.replaceRange(e.caret, to, "", false)
		} else {
			e.replaceSel("", true)
		}
	case key.NameTab:
		if e.ReadOnly {
			return
		}
		if shift || e.caret.line != e.anchor.line {
			e.indent(!shift)
		} else {
			n := 4 - visCol(e.buf.lines[e.caret.line], e.caret.col)%4
			e.replaceSel(strings.Repeat(" ", n), true)
		}
	case key.NameSpace:
		e.triggerCompletion(true)
	case key.NameF3:
		e.find.next(!shift)
	case "/":
		e.toggleComment()
	case "A":
		e.anchor, e.caret = tpos{}, e.buf.end()
	case "C", "X":
		txt := e.Selected()
		lineCut := false
		if txt == "" { // whole line
			txt = e.buf.lines[e.caret.line] + "\n"
			lineCut = true
		}
		gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(txt))})
		if k.Name == "X" && !e.ReadOnly {
			if lineCut {
				a := tpos{e.caret.line, 0}
				z := tpos{e.caret.line + 1, 0}
				if e.caret.line == e.buf.lineCount()-1 {
					z = tpos{e.caret.line, len(e.buf.lines[e.caret.line])}
					if e.caret.line > 0 {
						a = tpos{e.caret.line - 1, len(e.buf.lines[e.caret.line-1])}
					}
				}
				e.replaceRange(a, z, "", true)
			} else {
				e.replaceSel("", true)
			}
		}
	case "V":
		if !e.ReadOnly {
			gtx.Execute(clipboard.ReadCmd{Tag: e})
		}
	case "Z":
		if e.ReadOnly {
			return
		}
		var c, an tpos
		var ok bool
		if shift {
			c, an, ok = e.buf.redoStep()
		} else {
			c, an, ok = e.buf.undoStep()
		}
		if ok {
			e.caret, e.anchor = c, an
			e.changed()
		}
	case "Y":
		if e.ReadOnly {
			return
		}
		if c, an, ok := e.buf.redoStep(); ok {
			e.caret, e.anchor = c, an
			e.changed()
		}
	case "D":
		e.duplicate()
	case "K":
		e.deleteLines()
	case "U":
		if s := e.Selected(); s != "" && !e.ReadOnly {
			up := strings.ToUpper(s)
			if up == s {
				up = strings.ToLower(s)
			}
			a, z := minPos(e.caret, e.anchor), maxPos(e.caret, e.anchor)
			e.replaceRange(a, z, up, true)
			e.anchor = a
		}
	case "F", "H":
		e.find.show(gtx, k.Name == "H", e.Selected())
	}
	if resetX {
		e.desiredX = -1
	}
}

// selLines returns the line span of the selection.
func (e *CodeEditor) selLines() (int, int) {
	a, z := minPos(e.caret, e.anchor), maxPos(e.caret, e.anchor)
	last := z.line
	if z.col == 0 && z.line > a.line {
		last--
	}
	return a.line, last
}

// indent shifts the selected lines by 4 spaces.
func (e *CodeEditor) indent(in bool) {
	if e.ReadOnly {
		return
	}
	from, to := e.selLines()
	lines := make([]string, 0, to-from+1)
	for l := from; l <= to; l++ {
		s := e.buf.lines[l]
		if in {
			if s != "" {
				s = "    " + s
			}
		} else {
			n := 0
			for n < 4 && n < len(s) && s[n] == ' ' {
				n++
			}
			if n == 0 && strings.HasPrefix(s, "\t") {
				n = 1
			}
			s = s[n:]
		}
		lines = append(lines, s)
	}
	e.replaceLines(from, to, lines)
}

// replaceLines replaces whole lines [from, to] and selects them. Without
// a selection the caret stays a caret, shifted by the edit of its line.
func (e *CodeEditor) replaceLines(from, to int, lines []string) {
	if e.ReadOnly {
		return
	}
	single := e.caret == e.anchor
	caret := e.caret
	oldLine := e.buf.lines[caret.line]
	a := tpos{from, 0}
	z := tpos{to, len(e.buf.lines[to])}
	e.replaceRange(a, z, strings.Join(lines, "\n"), true)
	if single && len(lines) == to-from+1 && caret.line >= from && caret.line <= to {
		e.caret = e.buf.clamp(tpos{caret.line, shiftCol(oldLine, e.buf.lines[caret.line], caret.col)})
		e.anchor = e.caret
		return
	}
	e.anchor = tpos{from, 0}
	e.caret = tpos{from + len(lines) - 1, len(e.buf.lines[from+len(lines)-1])}
}

// shiftCol maps a column of old to new when a prefix was inserted or
// removed (indent, comment toggle): columns before the change stay, the
// others move by the length difference.
func shiftCol(old, new string, col int) int {
	p := 0
	for p < len(old) && p < len(new) && old[p] == new[p] {
		p++
	}
	if col < p {
		return col
	}
	return max(p, col+len(new)-len(old))
}

func (e *CodeEditor) toggleComment() {
	if e.ReadOnly {
		return
	}
	from, to := e.selLines()
	all := true
	for l := from; l <= to; l++ {
		s := strings.TrimSpace(e.buf.lines[l])
		if s != "" && !strings.HasPrefix(s, "--") {
			all = false
		}
	}
	var lines []string
	for l := from; l <= to; l++ {
		s := e.buf.lines[l]
		if all {
			i := strings.Index(s, "--")
			if i >= 0 {
				rest := s[i+2:]
				rest = strings.TrimPrefix(rest, " ")
				s = s[:i] + rest
			}
		} else if strings.TrimSpace(s) != "" {
			ind := indentOf(s)
			s = ind + "-- " + s[len(ind):]
		}
		lines = append(lines, s)
	}
	e.replaceLines(from, to, lines)
}

func (e *CodeEditor) duplicate() {
	if e.ReadOnly {
		return
	}
	if s := e.Selected(); s != "" {
		z := maxPos(e.caret, e.anchor)
		e.replaceRange(z, z, s, true)
		return
	}
	l := e.caret.line
	line := e.buf.lines[l]
	col := e.caret.col
	e.replaceRange(tpos{l, len(line)}, tpos{l, len(line)}, "\n"+line, true)
	e.caret = tpos{l + 1, col}
	e.anchor = e.caret
}

func (e *CodeEditor) deleteLines() {
	if e.ReadOnly {
		return
	}
	from, to := e.selLines()
	a := tpos{from, 0}
	z := tpos{to + 1, 0}
	if to == e.buf.lineCount()-1 {
		z = tpos{to, len(e.buf.lines[to])}
		if from > 0 {
			a = tpos{from - 1, len(e.buf.lines[from-1])}
		}
	}
	e.replaceRange(a, z, "", true)
}

// moveLines moves the selected lines up or down.
func (e *CodeEditor) moveLines(d int) {
	if e.ReadOnly {
		return
	}
	from, to := e.selLines()
	if d < 0 && from == 0 || d > 0 && to >= e.buf.lineCount()-1 {
		return
	}
	block := append([]string(nil), e.buf.lines[from:to+1]...)
	caret, anchor := e.caret, e.anchor
	if d < 0 {
		other := e.buf.lines[from-1]
		e.replaceLines(from-1, to, append(block, other))
	} else {
		other := e.buf.lines[to+1]
		e.replaceLines(from, to+1, append([]string{other}, block...))
	}
	caret.line += d
	anchor.line += d
	e.caret, e.anchor = e.buf.clamp(caret), e.buf.clamp(anchor)
}

// posAt maps a point in the text area to a position.
func (e *CodeEditor) posAt(p f32.Point) tpos {
	ln := (int(p.Y) + e.scrollY) / max(1, e.lineH)
	ln = max(0, min(e.buf.lineCount()-1, ln))
	vis := int(math.Round(float64((p.X - float32(e.gutterW) + float32(e.scrollX)) / e.charW)))
	return tpos{ln, colAtVis(e.buf.lines[ln], max(0, vis))}
}

func (e *CodeEditor) onPointer(gtx C, pe pointer.Event, size image.Point) {
	switch pe.Kind {
	case pointer.Scroll:
		e.scrollY += int(pe.Scroll.Y)
		e.scrollX += int(pe.Scroll.X)
		if pe.Modifiers.Contain(key.ModShift) && pe.Scroll.X == 0 {
			e.scrollX += int(pe.Scroll.Y)
			e.scrollY -= int(pe.Scroll.Y)
		}
	case pointer.Press:
		gtx.Execute(key.FocusCmd{Tag: e})
		// vertical scrollbar
		if pe.Position.X >= float32(size.X-gtx.Dp(10)) && e.contentH() > size.Y {
			e.sbDrag, e.sbGrab = true, int(pe.Position.Y)
			e.scrollBarJump(int(pe.Position.Y), size.Y)
			return
		}
		if pe.Buttons == pointer.ButtonSecondary {
			p := e.posAt(pe.Position)
			a, z := minPos(e.caret, e.anchor), maxPos(e.caret, e.anchor)
			if a == z || p.before(a) || z.before(p) {
				e.moveTo(p, false)
			}
			if e.OnContextMenu != nil {
				e.OnContextMenu(e.app.pointer)
			}
			return
		}
		if pe.Buttons != pointer.ButtonPrimary {
			return
		}
		p := e.posAt(pe.Position)
		if gtx.Now.Sub(e.lastClick) < 450*time.Millisecond && p.line == e.lastClickAt.line {
			e.clicks = e.clicks%3 + 1
		} else {
			e.clicks = 1
		}
		e.lastClick, e.lastClickAt = gtx.Now, p
		e.comp = nil
		switch {
		case pe.Modifiers.Contain(key.ModShift):
			e.moveTo(p, true)
		case e.clicks == 2:
			a, z := e.buf.wordAt(p)
			e.anchor, e.caret = a, z
		case e.clicks == 3:
			e.anchor = tpos{p.line, 0}
			e.caret = e.buf.clamp(tpos{p.line + 1, 0})
			if p.line == e.buf.lineCount()-1 {
				e.caret = tpos{p.line, len(e.buf.lines[p.line])}
			}
		default:
			e.moveTo(p, false)
		}
		e.dragging = true
		e.desiredX = -1
	case pointer.Drag:
		if e.sbDrag {
			e.scrollBarJump(int(pe.Position.Y), size.Y)
			return
		}
		if e.dragging {
			p := e.posAt(pe.Position)
			if e.clicks == 2 {
				a, z := e.buf.wordAt(p)
				if p.before(e.anchor) {
					e.caret = a
				} else {
					e.caret = z
				}
			} else {
				e.caret = p
			}
			// autoscroll beyond the edges
			if pe.Position.Y < 0 {
				e.scrollY -= e.lineH
			} else if int(pe.Position.Y) > size.Y {
				e.scrollY += e.lineH
			}
		}
	case pointer.Release, pointer.Cancel:
		e.dragging, e.sbDrag = false, false
	}
}

func (e *CodeEditor) contentH() int { return e.buf.lineCount()*e.lineH + e.lineH*3 }

func (e *CodeEditor) scrollBarJump(y, h int) {
	total := e.contentH()
	e.scrollY = int(float64(y)/float64(max(1, h))*float64(total)) - h/2
}

// Layout draws the editor filling the constraints.
func (e *CodeEditor) Layout(gtx C) D {
	e.measure(gtx)
	e.update(gtx)
	size := gtx.Constraints.Max
	gtx.Constraints = layout.Exact(size)
	fill(gtx, pal.CodeBg)
	e.viewH, e.viewW = size.Y, size.X

	digits := len(itoa(e.buf.lineCount()))
	e.gutterW = int(float32(max(3, digits))*e.charW) + gtx.Dp(28)

	// pointer + keyboard area
	area := clip.Rect{Max: size}.Push(gtx.Ops)
	pointer.CursorText.Add(gtx.Ops)
	event.Op(gtx.Ops, e)
	if !e.ReadOnly {
		key.InputHintOp{Tag: e, Hint: key.HintAny}.Add(gtx.Ops)
	}
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: e, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel | pointer.Scroll,
			ScrollY: pointer.ScrollRange{Min: -1e6, Max: 1e6}, ScrollX: pointer.ScrollRange{Min: -1e6, Max: 1e6}})
		if !ok {
			break
		}
		if pe, ok := ev.(pointer.Event); ok {
			e.onPointer(gtx, pe, size)
		}
	}

	// keep the caret visible
	if e.scrollCaret {
		e.scrollCaret = false
		cy := e.caret.line * e.lineH
		if cy < e.scrollY {
			e.scrollY = cy
		} else if cy+e.lineH > e.scrollY+size.Y {
			e.scrollY = cy + e.lineH - size.Y
		}
		vc := visCol(e.buf.lines[e.caret.line], e.caret.col)
		e.maxVisCols = max(e.maxVisCols, vc)
		cx := int(float32(vc) * e.charW)
		textW := size.X - e.gutterW - gtx.Dp(14)
		if cx < e.scrollX {
			e.scrollX = max(0, cx-gtx.Dp(40))
		} else if cx > e.scrollX+textW {
			e.scrollX = cx - textW + gtx.Dp(40)
		}
	}
	maxY := max(0, e.contentH()-size.Y)
	e.scrollY = max(0, min(e.scrollY, maxY))
	maxX := max(0, int(float32(e.maxVisCols)*e.charW)-(size.X-e.gutterW)+gtx.Dp(60))
	e.scrollX = max(0, min(e.scrollX, maxX))

	first := e.scrollY / max(1, e.lineH)
	last := min(e.buf.lineCount()-1, (e.scrollY+size.Y)/max(1, e.lineH))
	selA, selZ := minPos(e.caret, e.anchor), maxPos(e.caret, e.anchor)
	hasSel := selA != selZ
	var bA, bB tpos
	hasBr := false
	if e.focused && !hasSel {
		bA, bB, hasBr = e.buf.matchBracket(e.caret)
	}
	matches := e.find.visibleMatches(first, last)
	th := e.app.th
	textX := e.gutterW - e.scrollX

	for ln := first; ln <= last; ln++ {
		y := ln*e.lineH - e.scrollY
		line := e.buf.lines[ln]
		if utf8.RuneCountInString(line) > e.maxVisCols {
			e.maxVisCols = visCol(line, len(line))
		}
		// backgrounds
		if ln == e.caret.line && !hasSel && e.focused {
			fillRect(gtx, pal.LineHL, image.Rect(e.gutterW, y, size.X, y+e.lineH))
		}
		if ln == e.ErrLine {
			fillRect(gtx, withAlpha(pal.Err, 0x22), image.Rect(e.gutterW, y, size.X, y+e.lineH))
		}
		for _, m := range matches {
			if m.a.line == ln {
				x0 := textX + int(float32(visCol(line, m.a.col))*e.charW)
				x1 := textX + int(float32(visCol(line, m.z.col))*e.charW)
				c := withAlpha(pal.Warn, 0x40)
				if m.current {
					c = withAlpha(pal.Warn, 0x90)
				}
				fillRect(gtx, c, image.Rect(x0, y, x1, y+e.lineH))
			}
		}
		if hasSel && ln >= selA.line && ln <= selZ.line {
			c0, c1 := 0, visCol(line, len(line))+1
			if ln == selA.line {
				c0 = visCol(line, selA.col)
			}
			if ln == selZ.line {
				c1 = visCol(line, selZ.col)
			}
			fillRect(gtx, withAlpha(pal.Info, 0x45), image.Rect(textX+int(float32(c0)*e.charW), y, textX+int(float32(c1)*e.charW), y+e.lineH))
		}
		if hasBr {
			for _, p := range []tpos{bA, bB} {
				if p.line == ln {
					x := textX + int(float32(visCol(line, p.col))*e.charW)
					r := image.Rect(x, y+gtx.Dp(1), x+int(e.charW), y+e.lineH-gtx.Dp(1))
					fillRect(gtx, withAlpha(pal.Accent, 0x40), r)
				}
			}
		}
		// text
		e.drawLine(gtx, th, ln, line, textX, y, size.X)
	}

	// gutter
	fillRect(gtx, pal.CodeBg, image.Rect(0, 0, e.gutterW-gtx.Dp(8), size.Y))
	for ln := first; ln <= last; ln++ {
		y := ln*e.lineH - e.scrollY
		c := withAlpha(pal.TextDim, 0x90)
		if ln == e.caret.line {
			c = pal.Text
		}
		if ln >= e.StmtFrom && ln <= e.StmtTo && e.StmtFrom >= 0 {
			fillRect(gtx, withAlpha(pal.Accent, 0xa0), image.Rect(e.gutterW-gtx.Dp(12), y, e.gutterW-gtx.Dp(9), y+e.lineH))
		}
		if ln == e.ErrLine {
			c = pal.Err
		}
		st := op.Offset(image.Pt(0, y+e.ascent)).Push(gtx.Ops)
		g := gtx
		g.Constraints = layout.Exact(image.Pt(e.gutterW-gtx.Dp(16), e.lineH))
		lblAlign(g, th, fontMono, e.size*0.92, c, itoa(ln+1), text.End)
		st.Pop()
	}

	// caret
	if e.focused && !e.ReadOnly {
		blink := 530 * time.Millisecond
		el := gtx.Now.Sub(e.blinkStart)
		if (el/blink)%2 == 0 {
			x := textX + int(float32(visCol(e.buf.lines[e.caret.line], e.caret.col))*e.charW)
			y := e.caret.line*e.lineH - e.scrollY
			if x >= e.gutterW-gtx.Dp(2) {
				fillRect(gtx, pal.Accent, image.Rect(x, y+gtx.Dp(2), x+gtx.Dp(2), y+e.lineH-gtx.Dp(2)))
			}
		}
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(blink - el%blink)})
		e.updateIME(gtx, textX)
	}

	// scrollbars
	if total := e.contentH(); total > size.Y {
		th := max(gtx.Dp(24), size.Y*size.Y/total)
		ty := int(float64(e.scrollY) / float64(total-size.Y) * float64(size.Y-th))
		fillRRect(gtx, withAlpha(pal.TextDim, 0x50), image.Rect(size.X-gtx.Dp(7), ty, size.X-gtx.Dp(2), ty+th), gtx.Dp(3))
	}
	area.Pop()

	e.find.layout(gtx, size)
	if e.comp != nil {
		e.layoutCompletion(gtx, textX, size)
	}
	return D{Size: size}
}

// drawLine draws the tokens of one line (visual columns are advanced
// incrementally: lines can be very long).
func (e *CodeEditor) drawLine(gtx C, th *material.Theme, ln int, line string, x0, y, maxX int) {
	toks := e.tokens(ln)
	vis, at := 0, 0
	advance := func(to int) {
		for at < to && at < len(line) {
			r, n := utf8.DecodeRuneInString(line[at:])
			switch {
			case r == '\t':
				vis += tabWidth - vis%tabWidth
			case r < 0x80:
				vis++
			default:
				vis += max(1, runewidth.RuneWidth(r))
			}
			at += n
		}
	}
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(1<<20, e.lineH)}
	for i, t := range toks {
		if t.Kind == sqlx.TSpace {
			continue
		}
		advance(t.Start)
		sx := x0 + int(float32(vis)*e.charW)
		if sx > maxX {
			break
		}
		startVis := vis
		advance(t.End)
		if x0+int(float32(vis)*e.charW) < e.gutterW-gtx.Dp(8) {
			continue
		}
		seg := expandTabs(line[t.Start:t.End], startVis)
		st := op.Offset(image.Pt(sx, y+e.ascent)).Push(gtx.Ops)
		widget.Label{MaxLines: 1}.Layout(g, th.Shaper, fontMono, e.size, seg, colorMat(gtx, e.tokColor(line, toks, i)))
		st.Pop()
	}
}

func (e *CodeEditor) updateIME(gtx C, textX int) {
	l := e.buf.lines[e.caret.line]
	if e.imeLine != e.caret.line || e.imeText != l {
		e.imeLine, e.imeText = e.caret.line, l
		gtx.Execute(key.SnippetCmd{Tag: e, Snippet: key.Snippet{Range: key.Range{Start: 0, End: utf8.RuneCountInString(l)}, Text: l}})
	}
	c := utf8.RuneCountInString(l[:e.caret.col])
	a := c
	if e.anchor.line == e.caret.line {
		a = utf8.RuneCountInString(l[:e.anchor.col])
	}
	x := float32(textX) + float32(visCol(l, e.caret.col))*e.charW
	y := float32(e.caret.line*e.lineH - e.scrollY + e.lineH)
	// Gio convention (widget.Editor, SelectionEvent): Start is the caret,
	// End the other end of the selection
	gtx.Execute(key.SelectionCmd{Tag: e, Range: key.Range{Start: c, End: a},
		Caret: key.Caret{Pos: f32.Pt(x, y), Ascent: float32(e.lineH), Descent: 0}})
}
