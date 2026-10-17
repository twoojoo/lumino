package db

import (
	"errors"
	"fmt"
	"strings"

	"github.com/twoojoo/lumino/internal/db/sqlx"
)

// SQLLanguage is the Language of SQL databases, built on their Dialect.
// Adapters with a SQL-like language (CQL) embed it and override what
// differs.
type SQLLanguage struct {
	D     Dialect
	Label string // Display ("" = "SQL")
}

// NewSQLLanguage returns the language of a SQL dialect.
func NewSQLLanguage(d Dialect) *SQLLanguage { return &SQLLanguage{D: d} }

// GenericSQL is the language used before a connection is open (and where
// no connection is known): ANSI keywords and double-quoted identifiers.
var GenericSQL Language = NewSQLLanguage(&BaseDialect{QuoteL: `"`, QuoteR: `"`, KeywordSet: CommonKeywords, FuncList: CommonFunctions})

// Dialect returns the SQL dialect (see SQLDialect).
func (l *SQLLanguage) Dialect() Dialect { return l.D }

func (l *SQLLanguage) Name() string { return l.D.Name() }

func (l *SQLLanguage) Display() string {
	if l.Label != "" {
		return l.Label
	}
	return "SQL"
}

func (l *SQLLanguage) Fence() string { return "sql" }

func (l *SQLLanguage) LexLine(line string, st LexState) ([]sqlx.Token, LexState) {
	s, _ := st.(sqlx.State)
	return sqlx.LexLine(line, s, l.D.Lex())
}

func (l *SQLLanguage) Tokens(text string) []sqlx.Token { return sqlx.Tokens(text, l.D.Lex()) }
func (l *SQLLanguage) Keywords() map[string]bool       { return l.D.Keywords() }
func (l *SQLLanguage) Functions() []string             { return l.D.Functions() }
func (l *SQLLanguage) Types() []string                 { return l.D.Types() }
func (l *SQLLanguage) LineComment() string             { return "--" }

func (l *SQLLanguage) Split(script string) []sqlx.Statement { return sqlx.Split(script, l.D.Lex()) }

// Terminate trims a statement and appends ";" unless it already ends with
// one (PL/SQL "END;" must not become "END;;") or with a block terminator
// line ("/" in Oracle scripts, "GO" in SQL Server ones).
func (l *SQLLanguage) Terminate(stmt string) string {
	stmt = strings.TrimSpace(stmt)
	if stmt == "" || strings.HasSuffix(stmt, ";") {
		return stmt
	}
	last := stmt
	if i := strings.LastIndexByte(stmt, '\n'); i >= 0 {
		last = stmt[i+1:]
	}
	if last = strings.TrimSpace(last); last == "/" || strings.EqualFold(last, "go") {
		return stmt
	}
	return stmt + ";"
}

func (l *SQLLanguage) Classify(stmt string) StmtKind { return Classify(stmt, l.D.Lex()) }

func (l *SQLLanguage) CompletionContext(stmt string, caret int) CompletionContext {
	return AnalyzeSQL(stmt, caret, l.D.Lex(), l.D.Keywords())
}

func (l *SQLLanguage) QuoteIdent(name string) string { return l.D.Quote(name) }

func (l *SQLLanguage) Browse(ref ObjectRef, q BrowseQuery) (string, error) {
	order := make([]string, len(q.Sort))
	for i, k := range q.Sort {
		dir := " ASC"
		if k.Desc {
			dir = " DESC"
		}
		order[i] = l.D.Quote(k.Field) + dir
	}
	return l.D.SelectAll(ref, q.Filter, strings.Join(order, ", ")), nil
}

func (l *SQLLanguage) Count(ref ObjectRef, filter string) (string, error) {
	q := "SELECT COUNT(*) FROM " + l.D.Qualified(ref)
	if w := strings.TrimSpace(filter); w != "" {
		q += " WHERE " + w
	}
	return q, nil
}

// ValidateFilter accepts one condition and nothing that changes data (it
// is pasted into a SELECT, so "1=1; DELETE …" must not pass).
func (l *SQLLanguage) ValidateFilter(filter string) error {
	w := strings.TrimSpace(filter)
	if w == "" {
		return nil
	}
	lex := l.D.Lex()
	q := "SELECT 1 FROM t WHERE " + w
	if st := sqlx.Split(q, lex); len(st) != 1 || st[0].Text != strings.TrimRight(strings.TrimSpace(q), ";") {
		return errors.New("the filter must be a single condition (no ';')")
	}
	if k := Classify(q, lex); k != StmtRead {
		return fmt.Errorf("the filter can't contain %s statements", k)
	}
	return nil
}

func (l *SQLLanguage) EqualsFilter(field string, v any, k Kind) string {
	if v == nil {
		return l.D.Quote(field) + " IS NULL"
	}
	return l.D.Quote(field) + " = " + l.D.Literal(v, k)
}

func (l *SQLLanguage) FilterHint() string {
	return "filter, e.g. id > 10 AND name LIKE 'a%'  (Enter to apply)"
}

func (l *SQLLanguage) SortUnsupported() string { return "" }

func (l *SQLLanguage) FilterLabel() string { return "WHERE" }

func (l *SQLLanguage) MenuLabels() (string, string) {
	return "SELECT in new console", "Generate INSERT / UPDATE / DELETE…"
}

func (l *SQLLanguage) DisplayName(ref ObjectRef) string { return l.D.Qualified(ref) }

func (l *SQLLanguage) OpenTemplate(ref ObjectRef) string {
	return "SELECT * FROM " + l.D.Qualified(ref) + ";"
}

// truncateUnsupported lists the dialects without TRUNCATE TABLE: emptying
// a table there is a DELETE without WHERE.
var truncateUnsupported = map[string]bool{"sqlite": true, "firebird": true}

// DropStatement builds the DROP (or TRUNCATE / DELETE-all) statement of an
// object and the verb shown in the confirmation.
func (l *SQLLanguage) DropStatement(ref ObjectRef, truncate bool) (stmt, verb string, ok bool) {
	d := l.D
	switch {
	case truncate && truncateUnsupported[d.Name()]:
		return "DELETE FROM " + d.Qualified(ref), "Delete all rows", true
	case truncate:
		return "TRUNCATE TABLE " + d.Qualified(ref), "Truncate", true
	case ref.Type == ObjMatView:
		stmt = "DROP MATERIALIZED VIEW " + d.Qualified(ref)
	case ref.Type == ObjFunction || ref.Type == ObjProcedure:
		stmt = "DROP " + strings.ToUpper(string(ref.Type)) + " " + d.Qualified(ref)
		// the signature picks the overload (PostgreSQL, CockroachDB,
		// Cassandra UDFs); adapters without overloads leave it empty
		if ref.Signature != "" {
			stmt += ref.Signature
		}
	default:
		stmt = "DROP " + strings.ToUpper(string(ref.Type)) + " " + d.Qualified(ref)
	}
	return stmt, "Drop", true
}

// Templates renders SELECT/INSERT/UPDATE/DELETE templates of a table.
// Placeholders are numbered sequentially per statement (skipped generated
// columns must not leave gaps: PostgreSQL rejects $1, $3).
func (l *SQLLanguage) Templates(ref ObjectRef, info *TableInfo) string {
	d := l.D
	var cols, params, sets []string
	for _, c := range info.Columns {
		if c.Generated != "" {
			continue
		}
		cols = append(cols, d.Quote(c.Name))
		params = append(params, d.Placeholder(len(params)+1))
		if c.PrimaryKeyNo == 0 {
			sets = append(sets, d.Quote(c.Name)+" = "+d.Placeholder(len(sets)+1))
		}
	}
	next := len(sets) // UPDATE: the key placeholders follow the SET ones
	var keys, delKeys []string
	for i, k := range info.PrimaryKey {
		keys = append(keys, d.Quote(k)+" = "+d.Placeholder(next+i+1))
		delKeys = append(delKeys, d.Quote(k)+" = "+d.Placeholder(i+1))
	}
	where := strings.Join(keys, " AND ")
	delWhere := strings.Join(delKeys, " AND ")
	if where == "" {
		where = "/* no primary key: add a condition */"
		delWhere = where
	}
	name := d.Qualified(ref)
	var b strings.Builder
	fmt.Fprintf(&b, "SELECT %s\nFROM %s;\n\n", strings.Join(cols, ", "), name)
	fmt.Fprintf(&b, "INSERT INTO %s (%s)\nVALUES (%s);\n\n", name, strings.Join(cols, ", "), strings.Join(params, ", "))
	if len(sets) > 0 {
		fmt.Fprintf(&b, "UPDATE %s\nSET %s\nWHERE %s;\n\n", name, strings.Join(sets, ",\n    "), where)
	}
	fmt.Fprintf(&b, "DELETE FROM %s\nWHERE %s;\n", name, delWhere)
	return b.String()
}

func (l *SQLLanguage) RenderChange(ref ObjectRef, ch Change) string {
	q, _ := BuildChange(l.D, ref, ch, true)
	return q
}

// InsertText renders the standard INSERT (exports must stay portable: it
// bypasses the dialect's ChangeBuilder).
func (l *SQLLanguage) InsertText(ref ObjectRef, cols []Column, row Row) string {
	vals := make([]ColValue, len(row))
	for i, v := range row {
		vals[i] = ColValue{Column: cols[i].Name, Kind: cols[i].Kind, Value: v}
	}
	q, _ := StandardChange(l.D, ref, Change{Op: OpInsert, Values: vals}, true)
	if strings.TrimSpace(q) == "" {
		return ""
	}
	return q + ";"
}

func (l *SQLLanguage) InsertFormat() (label, ext string) { return "SQL INSERT", ".sql" }

func (l *SQLLanguage) Explain(stmt string) (string, error) {
	prefix := "EXPLAIN "
	switch l.D.Name() {
	case "sqlite":
		prefix = "EXPLAIN QUERY PLAN "
	case "oracle", "sqlserver", "firebird", "cql":
		return "", fmt.Errorf("explain is not available for %s: %w", l.D.Name(), ErrNotSupported)
	}
	return prefix + strings.TrimSuffix(strings.TrimSpace(stmt), ";"), nil
}

// ColumnNames returns the words of a statement that can name a column:
// not the ones following FROM/JOIN/INTO/UPDATE/TABLE (tables) nor
// qualifiers (t.id).
func (l *SQLLanguage) ColumnNames(stmt string) []string {
	var sig []string
	for _, tk := range sqlx.Tokens(stmt, l.D.Lex()) {
		if tk.Kind != sqlx.TSpace && tk.Kind != sqlx.TComment {
			sig = append(sig, stmt[tk.Start:tk.End])
		}
	}
	var out []string
	for i, w := range sig {
		prev, next := "", ""
		if i > 0 {
			prev = strings.ToUpper(sig[i-1])
		}
		if i+1 < len(sig) {
			next = sig[i+1]
		}
		if prev == "FROM" || prev == "JOIN" || prev == "INTO" || prev == "UPDATE" || prev == "TABLE" || next == "." {
			continue
		}
		out = append(out, strings.Trim(w, "\"`[]"))
	}
	return out
}
