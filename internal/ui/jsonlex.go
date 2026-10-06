package ui

import (
	"github.com/giovanni/lumino/internal/db/sqlx"
)

// jsonLexLine tokenizes one line of JSON for highlighting. JSON strings
// can't span lines, so lines are independent. Object keys (a string
// followed by ':') are TQuotedIdent, other strings TString, numbers
// TNumber, true/false/null TWord, structure TPunct; anything else
// (invalid JSON) is TOther.
func jsonLexLine(s string) []sqlx.Token {
	var out []sqlx.Token
	n := len(s)
	for i := 0; i < n; {
		c := s[i]
		start := i
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			for i < n && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r') {
				i++
			}
			out = append(out, sqlx.Token{Kind: sqlx.TSpace, Start: start, End: i})
		case c == '"':
			i++
			for i < n && s[i] != '"' {
				if s[i] == '\\' && i+1 < n {
					i++
				}
				i++
			}
			if i < n {
				i++ // closing quote
			}
			kind := sqlx.TString
			j := i
			for j < n && (s[j] == ' ' || s[j] == '\t') {
				j++
			}
			if j < n && s[j] == ':' {
				kind = sqlx.TQuotedIdent // object key
			}
			out = append(out, sqlx.Token{Kind: kind, Start: start, End: i})
		case c == '-' || c >= '0' && c <= '9':
			i++
			for i < n && (s[i] >= '0' && s[i] <= '9' || s[i] == '.' || s[i] == 'e' || s[i] == 'E' || s[i] == '+' || s[i] == '-') {
				i++
			}
			out = append(out, sqlx.Token{Kind: sqlx.TNumber, Start: start, End: i})
		case c == '{' || c == '}' || c == '[' || c == ']' || c == ',' || c == ':':
			i++
			out = append(out, sqlx.Token{Kind: sqlx.TPunct, Start: start, End: i})
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			for i < n && (s[i] >= 'a' && s[i] <= 'z' || s[i] >= 'A' && s[i] <= 'Z') {
				i++
			}
			kind := sqlx.TOther
			switch s[start:i] {
			case "true", "false", "null":
				kind = sqlx.TWord
			}
			out = append(out, sqlx.Token{Kind: kind, Start: start, End: i})
		default:
			// invalid byte (or a UTF-8 sequence outside strings): one rune
			i++
			for i < n && s[i]&0xc0 == 0x80 {
				i++
			}
			out = append(out, sqlx.Token{Kind: sqlx.TOther, Start: start, End: i})
		}
	}
	return out
}
