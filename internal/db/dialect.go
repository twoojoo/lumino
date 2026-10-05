package db

import (
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/giovanni/lumino/internal/db/sqlx"
)

// Dialect captures the SQL syntax differences between databases.
type Dialect interface {
	Name() string
	// Quote quotes an identifier when needed (reserved word, case, chars).
	Quote(ident string) string
	// Qualified returns the SQL name of an object, quoted where needed.
	Qualified(ref ObjectRef) string
	// Placeholder is the i-th (1-based) bind parameter.
	Placeholder(i int) string
	// Literal renders a value as a SQL literal (exports, previews).
	Literal(v any, k Kind) string
	// Lex returns the lexer/splitter options.
	Lex() *sqlx.Options
	// SelectAll builds the browse query of a relation.
	SelectAll(ref ObjectRef, where, orderBy string) string
	// Keywords, Functions and Types feed highlighting and completion.
	Keywords() map[string]bool
	Functions() []string
	Types() []string
}

// Fold is how unquoted identifiers are folded by the server.
type Fold int

const (
	FoldNone Fold = iota
	FoldLower
	FoldUpper
)

// ParamStyle is the bind parameter syntax.
type ParamStyle int

const (
	ParamQuestion ParamStyle = iota // ?
	ParamDollar                     // $1
	ParamAtP                        // @p1
	ParamColon                      // :1
)

// BaseDialect is a configurable Dialect; adapters fill its fields.
type BaseDialect struct {
	ID         string
	QuoteL     string
	QuoteR     string
	Fold       Fold
	Params     ParamStyle
	LexOpts    sqlx.Options
	Reserved   map[string]bool // words that must be quoted as identifiers
	KeywordSet map[string]bool // highlighted keywords (Reserved ∪ others)
	FuncList   []string
	TypeList   []string
	// BoolLiterals renders booleans; default TRUE/FALSE.
	BoolTrue, BoolFalse string
	// BytesLit renders binary literals; default X'..'.
	BytesLit func([]byte) string
	// TimeLit renders timestamps; default 'YYYY-MM-DD HH:MM:SS.ffffff'.
	TimeLit func(t time.Time, k Kind) string
	// StringBackslash: backslashes in string literals must be doubled (MySQL).
	StringBackslash bool
	// QualifyCatalog includes the catalog in qualified names.
	QualifyCatalog bool
}

func (d *BaseDialect) Name() string        { return d.ID }
func (d *BaseDialect) Lex() *sqlx.Options  { return &d.LexOpts }
func (d *BaseDialect) Functions() []string { return d.FuncList }
func (d *BaseDialect) Types() []string     { return d.TypeList }

func (d *BaseDialect) Keywords() map[string]bool {
	if d.KeywordSet != nil {
		return d.KeywordSet
	}
	return d.Reserved
}

// NeedsQuote reports whether ident must be quoted.
func (d *BaseDialect) NeedsQuote(ident string) bool {
	if ident == "" {
		return true
	}
	if d.Reserved[strings.ToUpper(ident)] {
		return true
	}
	for i, c := range ident {
		switch {
		case c == '_':
		case c >= 'a' && c <= 'z':
			if d.Fold == FoldUpper {
				return true
			}
		case c >= 'A' && c <= 'Z':
			if d.Fold == FoldLower {
				return true
			}
		case c >= '0' && c <= '9':
			if i == 0 {
				return true
			}
		case c == '$' && i > 0 && d.ID != "postgres":
		default:
			return true
		}
	}
	return false
}

func (d *BaseDialect) Quote(ident string) string {
	if !d.NeedsQuote(ident) {
		return ident
	}
	return d.QuoteAlways(ident)
}

// QuoteAlways quotes ident unconditionally.
func (d *BaseDialect) QuoteAlways(ident string) string {
	r := d.QuoteR
	return d.QuoteL + strings.ReplaceAll(ident, r, r+r) + r
}

func (d *BaseDialect) Qualified(ref ObjectRef) string {
	var parts []string
	if d.QualifyCatalog && ref.Catalog != "" {
		parts = append(parts, d.Quote(ref.Catalog))
	}
	if ref.Schema != "" {
		parts = append(parts, d.Quote(ref.Schema))
	}
	parts = append(parts, d.Quote(ref.Name))
	return strings.Join(parts, ".")
}

func (d *BaseDialect) Placeholder(i int) string {
	switch d.Params {
	case ParamDollar:
		return "$" + strconv.Itoa(i)
	case ParamAtP:
		return "@p" + strconv.Itoa(i)
	case ParamColon:
		return ":" + strconv.Itoa(i)
	}
	return "?"
}

func (d *BaseDialect) SelectAll(ref ObjectRef, where, orderBy string) string {
	var b strings.Builder
	b.WriteString("SELECT * FROM ")
	b.WriteString(d.Qualified(ref))
	if w := strings.TrimSpace(where); w != "" {
		b.WriteString(" WHERE ")
		b.WriteString(w)
	}
	if o := strings.TrimSpace(orderBy); o != "" {
		b.WriteString(" ORDER BY ")
		b.WriteString(o)
	}
	return b.String()
}

// QuoteString renders a string literal.
func (d *BaseDialect) QuoteString(s string) string {
	s = strings.ReplaceAll(s, "'", "''")
	if d.StringBackslash {
		s = strings.ReplaceAll(s, `\`, `\\`)
	}
	return "'" + s + "'"
}

func (d *BaseDialect) Literal(v any, k Kind) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case bool:
		t, f := d.BoolTrue, d.BoolFalse
		if t == "" {
			t, f = "TRUE", "FALSE"
		}
		if x {
			return t
		}
		return f
	case int64, uint64, int, int32:
		return fmt.Sprint(x)
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return d.QuoteString(strconv.FormatFloat(x, 'g', -1, 64))
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	case []byte:
		if d.BytesLit != nil {
			return d.BytesLit(x)
		}
		return "X'" + hex.EncodeToString(x) + "'"
	case time.Time:
		if d.TimeLit != nil {
			return d.TimeLit(x, k)
		}
		return d.QuoteString(FormatTime(x, k))
	case string:
		if (k == KindDecimal || k == KindInt || k == KindFloat) && IsPlainNumber(x) {
			return x
		}
		return d.QuoteString(x)
	}
	return d.QuoteString(FormatValue(v, k))
}

// WordSet builds a keyword set from space separated words.
func WordSet(lists ...string) map[string]bool {
	m := map[string]bool{}
	for _, l := range lists {
		for _, w := range strings.Fields(l) {
			m[strings.ToUpper(w)] = true
		}
	}
	return m
}

// Merge returns the union of keyword sets.
func Merge(sets ...map[string]bool) map[string]bool {
	m := map[string]bool{}
	for _, s := range sets {
		for k := range s {
			m[k] = true
		}
	}
	return m
}

// Words returns the sorted words of a set.
func Words(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// CommonKeywords are ANSI keywords highlighted in every dialect.
var CommonKeywords = WordSet(`
ADD ALL ALTER AND ANY AS ASC AUTHORIZATION BEGIN BETWEEN BY CASCADE CASE CAST CHECK COLLATE COLUMN COMMENT COMMIT
CONSTRAINT CREATE CROSS CURRENT_DATE CURRENT_TIME CURRENT_TIMESTAMP CURRENT_USER DATABASE DEFAULT DELETE DESC DISTINCT
DROP ELSE END ESCAPE EXCEPT EXISTS EXPLAIN FALSE FETCH FIRST FOR FOREIGN FROM FULL FUNCTION GRANT GROUP HAVING IF IN
INDEX INNER INSERT INTERSECT INTO IS JOIN KEY LAST LEFT LIKE LIMIT NATURAL NOT NULL NULLS OFFSET ON ONLY OR ORDER OUTER
OVER PARTITION PRIMARY PROCEDURE REFERENCES RENAME REPLACE RESTRICT RETURNING REVOKE RIGHT ROLLBACK ROW ROWS SAVEPOINT
SCHEMA SELECT SEQUENCE SET SHOW SOME TABLE THEN TO TRANSACTION TRIGGER TRUE TRUNCATE UNION UNIQUE UPDATE USING VALUES
VIEW WHEN WHERE WINDOW WITH RECURSIVE LATERAL TEMPORARY TEMP UNLOGGED EXISTS IDENTITY GENERATED ALWAYS MATERIALIZED
NO ACTION MERGE MATCHED DO NOTHING CONFLICT ILIKE SIMILAR ASYMMETRIC SYMMETRIC INTERVAL ZONE AT LOCAL WITHOUT
`)

// CommonFunctions are completed and highlighted everywhere.
var CommonFunctions = strings.Fields(`
abs avg ceil coalesce concat count floor greatest least length lower ltrim max min mod nullif position power
replace round rtrim sign sqrt substring sum trim upper cast extract row_number rank dense_rank lag lead
first_value last_value ntile
`)
