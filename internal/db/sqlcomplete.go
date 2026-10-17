package db

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/twoojoo/lumino/internal/db/sqlx"
)

var tableKeywords = map[string]bool{"FROM": true, "JOIN": true, "INTO": true, "UPDATE": true, "TABLE": true, "DESCRIBE": true,
	"DESC": false, "TRUNCATE": true}

var clauseEnd = map[string]bool{"WHERE": true, "GROUP": true, "ORDER": true, "HAVING": true, "LIMIT": true, "ON": true,
	"SET": true, "VALUES": true, "UNION": true, "EXCEPT": true, "INTERSECT": true, "USING": true, "SELECT": true,
	"RETURNING": true, "WINDOW": true, "OFFSET": true, "FETCH": true, "FOR": true}

// UnquoteIdent strips identifier quotes.
func UnquoteIdent(s string) string {
	if len(s) >= 2 {
		switch {
		case s[0] == '"' && s[len(s)-1] == '"', s[0] == '`' && s[len(s)-1] == '`', s[0] == '[' && s[len(s)-1] == ']':
			return s[1 : len(s)-1]
		}
	}
	return s
}

// AnalyzeSQL inspects a SQL statement for completion (caret is a byte
// offset in it).
func AnalyzeSQL(stmt string, caret int, lex *sqlx.Options, kw map[string]bool) CompletionContext {
	var ctx CompletionContext
	caret = max(0, min(caret, len(stmt)))
	// prefix and qualifier: identifier chain right before the caret
	i := caret
	for i > 0 {
		r, n := utf8.DecodeLastRuneInString(stmt[:i])
		if !identRune(r) {
			break
		}
		i -= n
	}
	ctx.Prefix = stmt[i:caret]
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
			ctx.QualParts = append([]string{stmt[s+1 : k-1]}, ctx.QualParts...)
			j = s
			continue
		}
		s := k
		for s > 0 {
			r, n := utf8.DecodeLastRuneInString(stmt[:s])
			if !identRune(r) {
				break
			}
			s -= n
		}
		if s == k {
			break
		}
		ctx.QualParts = append([]string{stmt[s:k]}, ctx.QualParts...)
		j = s
	}
	ctx.QualStart = j
	if len(ctx.QualParts) > 0 {
		ctx.Qualifier = ctx.QualParts[len(ctx.QualParts)-1]
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
				parts = append(parts, UnquoteIdent(w.text))
				k++
				if k < len(words) && words[k].text != "." {
					break
				}
			}
			if len(parts) == 0 {
				break
			}
			ref := CompletionRef{Name: parts[len(parts)-1]}
			if len(parts) > 1 {
				ref.Schema = parts[len(parts)-2]
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
				ref.Alias = UnquoteIdent(words[k].text)
				k++
			}
			ctx.Refs = append(ctx.Refs, ref)
			if k < len(words) && words[k].text == "," {
				k++
				continue
			}
			break
		}
		k--
	}
	// expected object type from the last significant word before the prefix
	stop := ctx.QualStart
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
	ctx.ExpectObject = tableKeywords[last] || last == "," && inFrom
	return ctx
}

// identRune reports whether r can be part of an unquoted identifier.
func identRune(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
}
