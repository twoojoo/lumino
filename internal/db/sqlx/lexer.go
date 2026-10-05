// Package sqlx tokenizes and splits SQL scripts for every supported dialect.
// The lexer works line by line with an explicit carry-over state, so the
// editor can re-highlight only the lines after an edit; the splitter uses
// the same lexer over a whole script and reports statement offsets.
//
// The statement splitting rules were inspired by usql's stmt package
// (github.com/xo/usql, MIT) and extended with offsets, block bodies and
// client-side delimiters (DELIMITER, GO, "/", SET TERM).
package sqlx

import "strings"

// TokKind is a token class.
type TokKind uint8

const (
	TSpace TokKind = iota
	TComment
	TWord // identifier or keyword (the highlighter classifies keywords)
	TQuotedIdent
	TString
	TNumber
	TOperator
	TPunct // ( ) , ; .
	TParam // $1 ? :name @name
	TOther
)

// Token is a lexical token. Start/End are byte offsets (in the line for
// LexLine, in the whole text for Tokens).
type Token struct {
	Kind       TokKind
	Start, End int
}

// Options select dialect lexical rules.
type Options struct {
	DollarQuotes    bool // PostgreSQL $tag$…$tag$
	Backticks       bool // MySQL/ClickHouse `ident`
	Brackets        bool // SQL Server [ident]
	HashComments    bool // MySQL # comment
	SlashComments   bool // // comment (Cassandra)
	NestedComments  bool // PostgreSQL nested /* /* */ */
	BackslashEscape bool // MySQL 'it\'s'
	// DoubleQuoteStrings: "…" is a string, not an identifier (MySQL default
	// mode, ClickHouse no, Cassandra no).
	DoubleQuoteStrings bool
	AtParams           bool // @name variables/params (SQL Server, MySQL user vars)
	ColonParams        bool // :name binds (Oracle)

	// Splitter rules.
	Delimiter bool // MySQL client "DELIMITER //" lines
	GoBatches bool // SQL Server "GO" lines
	SlashLine bool // Oracle "/" lines terminate PL/SQL units
	SetTerm   bool // Firebird "SET TERM ^ ;"
	// BlockStart reports whether a statement whose leading words are given
	// (upper case) contains BEGIN … END bodies whose ";" don't terminate.
	BlockStart func(words []string) bool
	// BatchBlocks: block statements run until the next GO / "/" line (SQL
	// Server, Oracle) instead of being tracked by BEGIN/END depth.
	BatchBlocks bool
	// ApplyEndsBlock: APPLY closes a BEGIN … block (CQL BEGIN BATCH … APPLY BATCH).
	ApplyEndsBlock bool
	// BodyRequired: block statements always have a BEGIN body, and ";"
	// before it (PSQL declarations) doesn't terminate them (Firebird).
	BodyRequired bool
	// BlockSemicolon: keep/add the ";" after END of block statements
	// (Oracle PL/SQL requires it).
	BlockSemicolon bool
}

// mode of the carry-over state.
type mode uint8

const (
	mNormal mode = iota
	mBlockComment
	mString
	mQuotedIdent
	mDollar
)

// State carries multi-line constructs from one line to the next.
type State struct {
	m     mode
	depth int    // nested comment depth
	quote byte   // closing quote of a string/identifier
	tag   string // dollar-quote tag, including the $ signs
}

// InCode reports whether the state is outside strings and comments.
func (s State) InCode() bool { return s.m == mNormal }

func isIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func isIdent(c byte) bool { return isIdentStart(c) || c >= '0' && c <= '9' || c == '$' }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' }

const operatorChars = "+-*/<>=~!@#%^&|`?:"

// LexLine tokenizes one line (without its newline) starting in state st and
// returns the tokens and the state at the end of the line.
func LexLine(line string, st State, o *Options) ([]Token, State) {
	return lex(line, st, o, nil)
}

func lex(s string, st State, o *Options, out []Token) ([]Token, State) {
	i := 0
	n := len(s)
	emit := func(k TokKind, a, b int) {
		if b > a {
			out = append(out, Token{Kind: k, Start: a, End: b})
		}
	}
	// continue a construct opened on a previous line
	switch st.m {
	case mBlockComment:
		j, ok := scanBlockComment(s, 0, &st, o)
		emit(TComment, 0, j)
		i = j
		if ok {
			st.m = mNormal
		}
	case mString, mQuotedIdent:
		j, ok := scanQuoted(s, 0, st.quote, o.BackslashEscape && st.m == mString)
		k := TString
		if st.m == mQuotedIdent {
			k = TQuotedIdent
		}
		emit(k, 0, j)
		i = j
		if ok {
			st.m = mNormal
		}
	case mDollar:
		if j := strings.Index(s, st.tag); j >= 0 {
			emit(TString, 0, j+len(st.tag))
			i = j + len(st.tag)
			st.m, st.tag = mNormal, ""
		} else {
			emit(TString, 0, n)
			i = n
		}
	}
	for i < n {
		c := s[i]
		start := i
		switch {
		case isSpace(c):
			for i < n && isSpace(s[i]) {
				i++
			}
			emit(TSpace, start, i)
		case c == '-' && i+1 < n && s[i+1] == '-',
			o.HashComments && c == '#',
			o.SlashComments && c == '/' && i+1 < n && s[i+1] == '/':
			emit(TComment, start, n)
			i = n
		case c == '/' && i+1 < n && s[i+1] == '*':
			st.m, st.depth = mBlockComment, 1
			j, ok := scanBlockComment(s, i+2, &st, o)
			emit(TComment, start, j)
			i = j
			if ok {
				st.m = mNormal
			}
		case c == '\'' || c == '"' && o.DoubleQuoteStrings:
			// E'…' / N'…' / X'…' prefixes are lexed as part of the word before
			j, ok := scanQuoted(s, i+1, c, o.BackslashEscape)
			emit(TString, start, j)
			i = j
			if !ok {
				st.m, st.quote = mString, c
			}
		case c == '"' || c == '`' && o.Backticks:
			j, ok := scanQuoted(s, i+1, c, false)
			emit(TQuotedIdent, start, j)
			i = j
			if !ok {
				st.m, st.quote = mQuotedIdent, c
			}
		case c == '[' && o.Brackets:
			j, ok := scanQuoted(s, i+1, ']', false)
			emit(TQuotedIdent, start, j)
			i = j
			if !ok {
				st.m, st.quote = mQuotedIdent, ']'
			}
		case c == '$' && o.DollarQuotes && dollarTag(s, i) != "":
			tag := dollarTag(s, i)
			i += len(tag)
			if j := strings.Index(s[i:], tag); j >= 0 {
				i += j + len(tag)
				emit(TString, start, i)
			} else {
				emit(TString, start, n)
				i = n
				st.m, st.tag = mDollar, tag
			}
		case c == '$' && i+1 < n && isDigit(s[i+1]):
			i++
			for i < n && isDigit(s[i]) {
				i++
			}
			emit(TParam, start, i)
		case c == '?':
			i++
			emit(TParam, start, i)
		case c == ':' && o.ColonParams && i+1 < n && (isIdentStart(s[i+1]) || isDigit(s[i+1])) && (i == 0 || s[i-1] != ':'):
			i++
			for i < n && isIdent(s[i]) {
				i++
			}
			emit(TParam, start, i)
		case c == '@' && o.AtParams && i+1 < n && (isIdentStart(s[i+1]) || s[i+1] == '@'):
			i++
			for i < n && (isIdent(s[i]) || s[i] == '@') {
				i++
			}
			emit(TParam, start, i)
		case isDigit(c) || c == '.' && i+1 < n && isDigit(s[i+1]):
			i = scanNumber(s, i)
			emit(TNumber, start, i)
		case isIdentStart(c):
			for i < n && isIdent(s[i]) {
				i++
			}
			// prefixed strings: E'..', N'..', X'..', B'..', U&'..'
			if i < n && s[i] == '\'' && i-start <= 2 {
				j, ok := scanQuoted(s, i+1, '\'', o.BackslashEscape || strings.EqualFold(s[start:i], "e"))
				emit(TString, start, j)
				i = j
				if !ok {
					st.m, st.quote = mString, '\''
				}
				continue
			}
			emit(TWord, start, i)
		case c == '(' || c == ')' || c == ',' || c == ';' || c == '.' || c == '[' || c == ']' || c == '{' || c == '}':
			i++
			emit(TPunct, start, i)
		case strings.IndexByte(operatorChars, c) >= 0:
			for i < n && strings.IndexByte(operatorChars, s[i]) >= 0 {
				// don't swallow the start of a comment
				if i > start && (s[i] == '-' && i+1 < n && s[i+1] == '-' || s[i] == '/' && i+1 < n && s[i+1] == '*') {
					break
				}
				i++
			}
			emit(TOperator, start, i)
		default:
			// unknown byte (or a UTF-8 continuation): one rune
			i++
			for i < n && s[i]&0xc0 == 0x80 {
				i++
			}
			emit(TOther, start, i)
		}
	}
	return out, st
}

// scanBlockComment scans from i inside a block comment; it returns the end
// offset and whether the comment closed.
func scanBlockComment(s string, i int, st *State, o *Options) (int, bool) {
	n := len(s)
	for i < n {
		if s[i] == '*' && i+1 < n && s[i+1] == '/' {
			i += 2
			st.depth--
			if st.depth <= 0 || !o.NestedComments {
				st.depth = 0
				return i, true
			}
			continue
		}
		if o.NestedComments && s[i] == '/' && i+1 < n && s[i+1] == '*' {
			st.depth++
			i += 2
			continue
		}
		i++
	}
	return n, false
}

// scanQuoted scans a quoted token body from i (after the opening quote) to
// the closing quote q; a doubled quote is an escaped quote.
func scanQuoted(s string, i int, q byte, backslash bool) (int, bool) {
	n := len(s)
	for i < n {
		c := s[i]
		if backslash && c == '\\' {
			i += 2
			continue
		}
		if c == q {
			if i+1 < n && s[i+1] == q && q != ']' || q == ']' && i+1 < n && s[i+1] == ']' {
				i += 2
				continue
			}
			return i + 1, true
		}
		i++
	}
	return n, false
}

// dollarTag returns the $tag$ starting at i, or "".
func dollarTag(s string, i int) string {
	j := i + 1
	if j < len(s) && isDigit(s[j]) {
		return "" // $1 positional parameter
	}
	for j < len(s) && (isIdent(s[j]) && s[j] != '$') {
		j++
	}
	if j < len(s) && s[j] == '$' {
		return s[i : j+1]
	}
	return ""
}

func scanNumber(s string, i int) int {
	n := len(s)
	if s[i] == '0' && i+1 < n && (s[i+1] == 'x' || s[i+1] == 'X') {
		i += 2
		for i < n && (isDigit(s[i]) || s[i] >= 'a' && s[i] <= 'f' || s[i] >= 'A' && s[i] <= 'F') {
			i++
		}
		return i
	}
	for i < n && isDigit(s[i]) {
		i++
	}
	if i < n && s[i] == '.' && !(i+1 < n && s[i+1] == '.') {
		i++
		for i < n && isDigit(s[i]) {
			i++
		}
	}
	if i < n && (s[i] == 'e' || s[i] == 'E') {
		j := i + 1
		if j < n && (s[j] == '+' || s[j] == '-') {
			j++
		}
		if j < n && isDigit(s[j]) {
			i = j
			for i < n && isDigit(s[i]) {
				i++
			}
		}
	}
	return i
}

// Tokens tokenizes a whole text; offsets are relative to text.
func Tokens(text string, o *Options) []Token {
	var out []Token
	var st State
	off := 0
	for {
		nl := strings.IndexByte(text[off:], '\n')
		line := text[off:]
		if nl >= 0 {
			line = text[off : off+nl]
		}
		first := len(out)
		out, st = lex(line, st, o, out)
		for k := first; k < len(out); k++ {
			out[k].Start += off
			out[k].End += off
		}
		if nl < 0 {
			break
		}
		// the newline belongs to whatever construct is open
		k := TSpace
		switch st.m {
		case mBlockComment:
			k = TComment
		case mString, mDollar:
			k = TString
		case mQuotedIdent:
			k = TQuotedIdent
		}
		out = append(out, Token{Kind: k, Start: off + nl, End: off + nl + 1})
		off += nl + 1
	}
	return mergeAdjacent(out)
}

// mergeAdjacent joins consecutive tokens of a multi-line construct.
func mergeAdjacent(t []Token) []Token {
	if len(t) == 0 {
		return t
	}
	out := t[:1]
	for _, x := range t[1:] {
		last := &out[len(out)-1]
		if last.End == x.Start && last.Kind == x.Kind &&
			(x.Kind == TSpace || x.Kind == TString || x.Kind == TQuotedIdent || x.Kind == TComment) {
			last.End = x.End
			continue
		}
		out = append(out, x)
	}
	return out
}
