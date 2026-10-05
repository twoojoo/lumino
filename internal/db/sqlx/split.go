package sqlx

import (
	"slices"
	"strings"
)

// Statement is one statement of a script.
type Statement struct {
	Text       string // without the terminator, trimmed
	Start, End int    // byte offsets of Text in the script
}

// Split splits a script into statements. Client directives (DELIMITER,
// GO, "/", SET TERM) are consumed and never returned as statements.
func Split(script string, o *Options) []Statement {
	sp := splitter{s: script, o: o, toks: Tokens(script, o), term: ";"}
	return sp.run()
}

type splitter struct {
	s    string
	o    *Options
	toks []Token
	term string // current terminator (";" or a custom DELIMITER / SET TERM)
	out  []Statement
}

// lineBounds returns the line containing offset p (without the newline).
func (sp *splitter) lineBounds(p int) (int, int) {
	a := strings.LastIndexByte(sp.s[:p], '\n') + 1
	b := strings.IndexByte(sp.s[p:], '\n')
	if b < 0 {
		return a, len(sp.s)
	}
	return a, p + b
}

// directive recognizes a client directive line starting at token i (the
// first non-space token of its line). It returns the token index after the
// line and whether it was one; it applies DELIMITER / SET TERM changes.
func (sp *splitter) directive(i int, inStmt bool) (int, string, bool) {
	t := sp.toks[i]
	la, lb := sp.lineBounds(t.Start)
	if strings.TrimSpace(sp.s[la:t.Start]) != "" {
		return i, "", false
	}
	line := strings.TrimSpace(sp.s[t.Start:lb])
	word := strings.ToUpper(firstWord(line))
	next := func() int {
		j := i
		for j < len(sp.toks) && sp.toks[j].Start < lb {
			j++
		}
		return j
	}
	switch {
	case sp.o.Delimiter && word == "DELIMITER" && !inStmt:
		d := strings.TrimSpace(line[len("DELIMITER"):])
		if d != "" {
			sp.term = d
		}
		return next(), "delimiter", true
	case sp.o.GoBatches && word == "GO" && isGoLine(line):
		return next(), "go", true
	case sp.o.SlashLine && line == "/":
		return next(), "slash", true
	}
	return i, "", false
}

func isGoLine(line string) bool {
	f := strings.Fields(line)
	if len(f) == 1 {
		return true
	}
	if len(f) == 2 {
		for _, c := range f[1] {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	}
	return false
}

func firstWord(s string) string {
	for i, c := range s {
		if c == ' ' || c == '\t' || c == ';' {
			return s[:i]
		}
	}
	return s
}

// atTerm reports whether the custom terminator starts at token i.
func (sp *splitter) atTerm(i int) bool {
	t := sp.toks[i]
	switch t.Kind {
	case TSpace, TComment, TString, TQuotedIdent:
		return false
	}
	return strings.HasPrefix(sp.s[t.Start:], sp.term)
}

func (sp *splitter) emit(a, b int, block bool) {
	if a >= b {
		return
	}
	text := sp.s[a:b]
	// trim trailing space and comments-only tails are kept (they're harmless)
	trimmed := strings.TrimRight(text, " \t\r\n")
	b = a + len(trimmed)
	if b <= a {
		return
	}
	if block && sp.o.BlockSemicolon && !strings.HasSuffix(trimmed, ";") {
		trimmed += ";"
	}
	sp.out = append(sp.out, Statement{Text: trimmed, Start: a, End: b})
}

func (sp *splitter) run() []Statement {
	toks := sp.toks
	start := -1 // offset of the current statement, -1 = none
	var words []string
	block := false // the statement has BEGIN … END bodies
	depth := 0     // BEGIN/CASE nesting in depth mode
	body := false  // a BEGIN body was opened
	skipCase := false
	lineStart := true
	for i := 0; i < len(toks); {
		t := toks[i]
		if lineStart && t.Kind != TSpace {
			if j, kind, ok := sp.directive(i, start >= 0); ok {
				if start >= 0 {
					sp.emit(start, t.Start, block)
				}
				start, words, block, depth, body = -1, nil, false, 0, false
				_ = kind
				i = j
				lineStart = true
				continue
			}
		}
		if t.Kind == TSpace {
			if strings.IndexByte(sp.s[t.Start:t.End], '\n') >= 0 {
				lineStart = true
			}
			i++
			continue
		}
		lineStart = false
		if t.Kind == TComment && start < 0 {
			i++
			continue
		}
		if start < 0 {
			start = t.Start
		}
		text := sp.s[t.Start:t.End]
		if t.Kind == TWord {
			up := strings.ToUpper(text)
			if len(words) < 64 {
				words = append(words, up)
				if !block && sp.o.BlockStart != nil && sp.o.BlockStart(words) {
					block = true
				}
			}
			// Firebird SET TERM x ;
			if sp.o.SetTerm && len(words) == 2 && words[0] == "SET" && words[1] == "TERM" {
				// read the new terminator up to the current one
				// (SET TERM ^ ; ends with the old terminator)
				rest := sp.s[t.End:]
				old := sp.term
				if k := strings.Index(rest, old); k >= 0 {
					if nt := strings.TrimSpace(rest[:k]); nt != "" {
						sp.term = nt
					}
					end := t.End + k + len(old)
					for i < len(toks) && toks[i].Start < end {
						i++
					}
					start, words, block, depth, body = -1, nil, false, 0, false
					continue
				}
			}
			// BEGIN/CASE … END nesting (used only by block statements)
			switch up {
			case "BEGIN", "CASE":
				if up == "CASE" && skipCase {
					skipCase = false
					break
				}
				depth++
				if up == "BEGIN" {
					body = true
				}
			case "APPLY":
				if sp.o.ApplyEndsBlock {
					depth = max(0, depth-1)
				}
			case "END":
				switch sp.nextWord(i + 1) {
				case "IF", "LOOP", "WHILE", "REPEAT", "FOR":
				case "CASE":
					skipCase = true
					depth = max(0, depth-1)
				default:
					depth = max(0, depth-1)
				}
			}
		}
		if sp.atTerm(i) {
			inBody := block && (sp.o.BatchBlocks || depth > 0 || sp.o.BodyRequired && !body)
			if !inBody || sp.term != ";" {
				end := t.Start
				if block && sp.o.BlockSemicolon && sp.term == ";" {
					end = t.Start + 1 // keep "END;"
				}
				sp.emit(start, end, block)
				start, words, block, depth, body = -1, nil, false, 0, false
				// skip the terminator (it may span several tokens)
				te := t.Start + len(sp.term)
				for i < len(toks) && toks[i].Start < te {
					i++
				}
				continue
			}
		}
		i++
	}
	if start >= 0 {
		sp.emit(start, len(sp.s), block)
	}
	return sp.out
}

// nextWord returns the next word token after index i (upper case).
func (sp *splitter) nextWord(i int) string {
	for ; i < len(sp.toks); i++ {
		t := sp.toks[i]
		switch t.Kind {
		case TSpace, TComment:
			continue
		case TWord:
			return strings.ToUpper(sp.s[t.Start:t.End])
		}
		return ""
	}
	return ""
}

// StatementAt returns the index of the statement at byte offset p: the one
// containing it, else the closest one ending before p on the same line,
// else the next one. -1 when there are none.
func StatementAt(stmts []Statement, script string, p int) int {
	if len(stmts) == 0 {
		return -1
	}
	for i, s := range stmts {
		if p >= s.Start && p <= s.End {
			return i
		}
	}
	// after a statement on the same line (e.g. right after its ";")
	for i := len(stmts) - 1; i >= 0; i-- {
		s := stmts[i]
		if s.End <= p {
			if !strings.ContainsRune(script[s.End:p], '\n') {
				return i
			}
			break
		}
	}
	// otherwise: previous statement if only blank lines separate us from it
	// and nothing follows, else the next one
	for i, s := range stmts {
		if s.Start >= p {
			return i
		}
	}
	return len(stmts) - 1
}

// Words returns the leading words of a statement (upper case, skipping
// comments and opening parentheses).
func Words(stmt string, o *Options, n int) []string {
	var out []string
	for _, t := range Tokens(stmt, o) {
		switch t.Kind {
		case TSpace, TComment:
			continue
		case TWord:
			out = append(out, strings.ToUpper(stmt[t.Start:t.End]))
			if len(out) == n {
				return out
			}
		case TPunct:
			if stmt[t.Start:t.End] == "(" && len(out) == 0 {
				continue
			}
			return out
		default:
			if len(out) == 0 {
				continue
			}
			return out
		}
	}
	return out
}

// ReturnsRows guesses whether a statement produces a result set.
func ReturnsRows(stmt string, o *Options) bool {
	w := Words(stmt, o, 1)
	if len(w) == 0 {
		return false
	}
	switch w[0] {
	case "SELECT", "WITH", "SHOW", "DESCRIBE", "DESC", "EXPLAIN", "VALUES", "TABLE", "PRAGMA",
		"CALL", "EXEC", "EXECUTE", "LIST", "FETCH":
		return true
	case "INSERT", "UPDATE", "DELETE", "MERGE", "REPLACE", "UPSERT":
		return hasClause(stmt, o, "RETURNING", "OUTPUT")
	}
	return false
}

// hasClause reports whether one of the keywords appears as a top-level
// clause: not inside parentheses and not used as a column name
// (SET output = 1, (a, returning)).
func hasClause(stmt string, o *Options, kws ...string) bool {
	toks := Tokens(stmt, o)
	depth := 0
	prev := ""
	for i, t := range toks {
		txt := stmt[t.Start:t.End]
		switch t.Kind {
		case TSpace, TComment:
			continue
		case TPunct:
			switch txt {
			case "(":
				depth++
			case ")":
				depth = max(0, depth-1)
			}
		case TWord:
			up := strings.ToUpper(txt)
			if depth == 0 && slices.Contains(kws, up) && prev != "SET" && prev != "," && prev != "." {
				next := ""
				for _, n := range toks[i+1:] {
					if n.Kind != TSpace && n.Kind != TComment {
						next = stmt[n.Start:n.End]
						break
					}
				}
				if next != "=" && next != "." && next != "," {
					return true
				}
			}
			prev = up
			continue
		}
		prev = txt
	}
	return false
}

