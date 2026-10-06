package ui

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// tpos is a text position: line index and byte offset in the line.
type tpos struct{ line, col int }

func (p tpos) before(q tpos) bool { return p.line < q.line || p.line == q.line && p.col < q.col }

func minPos(a, b tpos) tpos {
	if b.before(a) {
		return b
	}
	return a
}

func maxPos(a, b tpos) tpos {
	if a.before(b) {
		return b
	}
	return a
}

// textBuf is a line-based text buffer with grouped undo/redo.
type textBuf struct {
	lines   []string
	version int // bumped on every change

	undo, redo []bufEdit
	lastEdit   time.Time
	size_      int
	sizeVer    int
	groupOpen  bool
	// onSplice reports replaced line ranges: lines [from, from+removed)
	// became [from, from+added). Used to keep per-line caches aligned.
	onSplice func(from, removed, added int)
}

// bufEdit is one reversible change: removed was replaced by inserted at at.
type bufEdit struct {
	at       tpos
	removed  string
	inserted string
	// caret/anchor before and after, restored by undo/redo
	before, after [2]tpos
	group         int
}

func newTextBuf(s string) *textBuf {
	b := &textBuf{}
	b.setText(s)
	return b
}

func (b *textBuf) setText(s string) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	old := len(b.lines)
	b.lines = strings.Split(s, "\n")
	b.version++
	b.undo, b.redo = nil, nil
	if b.onSplice != nil {
		b.onSplice(0, old, len(b.lines))
	}
}

func (b *textBuf) text() string { return strings.Join(b.lines, "\n") }

func (b *textBuf) lineCount() int { return len(b.lines) }

// size approximates the text length in bytes (cached per version).
func (b *textBuf) size() int {
	if b.sizeVer != b.version {
		n := 0
		for _, l := range b.lines {
			n += len(l) + 1
		}
		b.size_, b.sizeVer = n, b.version
	}
	return b.size_
}

func (b *textBuf) clamp(p tpos) tpos {
	if p.line < 0 {
		return tpos{}
	}
	if p.line >= len(b.lines) {
		l := len(b.lines) - 1
		return tpos{l, len(b.lines[l])}
	}
	l := b.lines[p.line]
	if p.col < 0 {
		p.col = 0
	}
	if p.col > len(l) {
		p.col = len(l)
	}
	for p.col > 0 && p.col < len(l) && !utf8.RuneStart(l[p.col]) {
		p.col--
	}
	return p
}

func (b *textBuf) end() tpos {
	l := len(b.lines) - 1
	return tpos{l, len(b.lines[l])}
}

// slice returns the text in [a, z).
func (b *textBuf) slice(a, z tpos) string {
	a, z = minPos(a, z), maxPos(a, z)
	if a.line == z.line {
		return b.lines[a.line][a.col:z.col]
	}
	var sb strings.Builder
	sb.WriteString(b.lines[a.line][a.col:])
	for l := a.line + 1; l < z.line; l++ {
		sb.WriteByte('\n')
		sb.WriteString(b.lines[l])
	}
	sb.WriteByte('\n')
	sb.WriteString(b.lines[z.line][:z.col])
	return sb.String()
}

// endOf returns the position after inserting s at p.
func endOf(p tpos, s string) tpos {
	n := strings.Count(s, "\n")
	if n == 0 {
		return tpos{p.line, p.col + len(s)}
	}
	return tpos{p.line + n, len(s) - strings.LastIndexByte(s, '\n') - 1}
}

// raw replaces [a, z) with s without recording undo; returns the end of s.
func (b *textBuf) raw(a, z tpos, s string) tpos {
	a, z = minPos(a, z), maxPos(a, z)
	head := b.lines[a.line][:a.col]
	tail := b.lines[z.line][z.col:]
	ins := strings.Split(s, "\n")
	ins[0] = head + ins[0]
	ins[len(ins)-1] += tail
	removed := z.line - a.line + 1
	nl := make([]string, 0, len(b.lines)-removed+len(ins))
	nl = append(nl, b.lines[:a.line]...)
	nl = append(nl, ins...)
	nl = append(nl, b.lines[z.line+1:]...)
	b.lines = nl
	b.version++
	if b.onSplice != nil {
		b.onSplice(a.line, removed, len(ins))
	}
	return endOf(a, s)
}

// replace records an undoable change of [a, z) with s. Typing within a
// short interval is grouped into one undo step unless brk is set.
func (b *textBuf) replace(a, z tpos, s string, caret, anchor tpos, brk bool) tpos {
	a, z = minPos(a, z), maxPos(a, z)
	removed := b.slice(a, z)
	if removed == "" && s == "" {
		return a
	}
	end := b.raw(a, z, s)
	e := bufEdit{at: a, removed: removed, inserted: s, before: [2]tpos{caret, anchor}, after: [2]tpos{end, end}}
	now := time.Now()
	group := len(b.undo)
	if n := len(b.undo); n > 0 && !brk && b.groupOpen {
		// an IME composition replaces its own previous text (each preedit
		// update): one undo step whatever the typing speed
		if prev := b.undo[n-1]; removed != "" && prev.at == a && prev.inserted == removed && !strings.Contains(s, "\n") {
			group = prev.group
		}
	}
	if n := len(b.undo); n > 0 && !brk && b.groupOpen && now.Sub(b.lastEdit) < 1200*time.Millisecond {
		prev := b.undo[n-1]
		// typing continues where the previous edit ended
		if endOf(prev.at, prev.inserted) == a && !strings.Contains(s, "\n") && removed == "" {
			group = prev.group
		}
		// backspacing continues where the previous one started
		if s == "" && prev.inserted == "" && z == prev.at {
			group = prev.group
		}
	}
	e.group = group
	b.undo = append(b.undo, e)
	b.redo = nil
	b.lastEdit = now
	b.groupOpen = !brk
	return end
}

// breakGroup ends the current undo group (cursor moved, focus lost…).
func (b *textBuf) breakGroup() { b.groupOpen = false }

// undoStep reverts the last group; returns caret and anchor to restore.
func (b *textBuf) undoStep() (tpos, tpos, bool) {
	n := len(b.undo)
	if n == 0 {
		return tpos{}, tpos{}, false
	}
	g := b.undo[n-1].group
	var caret, anchor tpos
	for len(b.undo) > 0 && b.undo[len(b.undo)-1].group == g {
		e := b.undo[len(b.undo)-1]
		b.undo = b.undo[:len(b.undo)-1]
		b.raw(e.at, endOf(e.at, e.inserted), e.removed)
		b.redo = append(b.redo, e)
		caret, anchor = e.before[0], e.before[1]
	}
	b.groupOpen = false
	return b.clamp(caret), b.clamp(anchor), true
}

func (b *textBuf) redoStep() (tpos, tpos, bool) {
	n := len(b.redo)
	if n == 0 {
		return tpos{}, tpos{}, false
	}
	g := b.redo[n-1].group
	var caret, anchor tpos
	for len(b.redo) > 0 && b.redo[len(b.redo)-1].group == g {
		e := b.redo[len(b.redo)-1]
		b.redo = b.redo[:len(b.redo)-1]
		b.raw(e.at, endOf(e.at, e.removed), e.inserted)
		b.undo = append(b.undo, e)
		caret, anchor = e.after[0], e.after[1]
	}
	b.groupOpen = false
	return b.clamp(caret), b.clamp(anchor), true
}

// offset converts a position to a byte offset in text().
func (b *textBuf) offset(p tpos) int {
	n := 0
	for l := 0; l < p.line && l < len(b.lines); l++ {
		n += len(b.lines[l]) + 1
	}
	return n + p.col
}

// posAt converts a byte offset in text() to a position.
func (b *textBuf) posAt(off int) tpos {
	for l, s := range b.lines {
		if off <= len(s) {
			return tpos{l, max(0, off)}
		}
		off -= len(s) + 1
	}
	return b.end()
}

// ---- cursor motion ----

func (b *textBuf) left(p tpos) tpos {
	if p.col > 0 {
		_, n := utf8.DecodeLastRuneInString(b.lines[p.line][:p.col])
		return tpos{p.line, p.col - n}
	}
	if p.line > 0 {
		return tpos{p.line - 1, len(b.lines[p.line-1])}
	}
	return p
}

func (b *textBuf) right(p tpos) tpos {
	l := b.lines[p.line]
	if p.col < len(l) {
		_, n := utf8.DecodeRuneInString(l[p.col:])
		return tpos{p.line, p.col + n}
	}
	if p.line < len(b.lines)-1 {
		return tpos{p.line + 1, 0}
	}
	return p
}

func isWordRune(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// wordLeft moves to the start of the previous word.
func (b *textBuf) wordLeft(p tpos) tpos {
	if p.col == 0 {
		return b.left(p)
	}
	l := b.lines[p.line]
	i := p.col
	for i > 0 {
		r, n := utf8.DecodeLastRuneInString(l[:i])
		if !unicode.IsSpace(r) {
			break
		}
		i -= n
	}
	if i > 0 {
		r, _ := utf8.DecodeLastRuneInString(l[:i])
		word := isWordRune(r)
		for i > 0 {
			r, n := utf8.DecodeLastRuneInString(l[:i])
			if isWordRune(r) != word || unicode.IsSpace(r) {
				break
			}
			i -= n
		}
	}
	return tpos{p.line, i}
}

// wordRight moves to the end of the next word.
func (b *textBuf) wordRight(p tpos) tpos {
	l := b.lines[p.line]
	if p.col >= len(l) {
		return b.right(p)
	}
	i := p.col
	for i < len(l) {
		r, n := utf8.DecodeRuneInString(l[i:])
		if !unicode.IsSpace(r) {
			break
		}
		i += n
	}
	if i < len(l) {
		r, _ := utf8.DecodeRuneInString(l[i:])
		word := isWordRune(r)
		for i < len(l) {
			r, n := utf8.DecodeRuneInString(l[i:])
			if isWordRune(r) != word || unicode.IsSpace(r) {
				break
			}
			i += n
		}
	}
	return tpos{p.line, i}
}

// wordAt returns the word around p (for double-click and completion).
func (b *textBuf) wordAt(p tpos) (tpos, tpos) {
	l := b.lines[p.line]
	s, e := p.col, p.col
	for s > 0 {
		r, n := utf8.DecodeLastRuneInString(l[:s])
		if !isWordRune(r) {
			break
		}
		s -= n
	}
	for e < len(l) {
		r, n := utf8.DecodeRuneInString(l[e:])
		if !isWordRune(r) {
			break
		}
		e += n
	}
	return tpos{p.line, s}, tpos{p.line, e}
}

// indentOf returns the leading whitespace of a line.
func indentOf(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return s[:i]
}

// firstNonSpace is the column of the first non blank character.
func firstNonSpace(s string) int { return len(indentOf(s)) }

// matchBracket finds the bracket matching the one at or before p.
func (b *textBuf) matchBracket(p tpos) (tpos, tpos, bool) {
	pairs := map[byte]byte{'(': ')', '[': ']', '{': '}', ')': '(', ']': '[', '}': '{'}
	try := func(at tpos) (tpos, bool) {
		l := b.lines[at.line]
		if at.col < 0 || at.col >= len(l) {
			return tpos{}, false
		}
		c := l[at.col]
		m, ok := pairs[c]
		if !ok {
			return tpos{}, false
		}
		dir := 1
		if c == ')' || c == ']' || c == '}' {
			dir = -1
		}
		depth := 0
		line, col := at.line, at.col
		for steps := 0; steps < 200000; steps++ {
			s := b.lines[line]
			if col >= 0 && col < len(s) {
				switch s[col] {
				case c:
					depth++
				case m:
					depth--
					if depth == 0 {
						return tpos{line, col}, true
					}
				}
			}
			col += dir
			for col < 0 || col >= len(b.lines[line]) {
				line += dir
				if line < 0 || line >= len(b.lines) {
					return tpos{}, false
				}
				if dir > 0 {
					col = 0
				} else {
					col = len(b.lines[line]) - 1
				}
				if len(b.lines[line]) > 0 {
					break
				}
			}
		}
		return tpos{}, false
	}
	if m, ok := try(p); ok {
		return p, m, true
	}
	q := tpos{p.line, p.col - 1}
	if m, ok := try(q); ok {
		return q, m, true
	}
	return tpos{}, tpos{}, false
}
