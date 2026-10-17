package db

import (
	"context"

	"github.com/twoojoo/lumino/internal/db/sqlx"
)

// LexState is the lexer state carried from one line to the next. It must
// be comparable (the editor re-lexes following lines only when the state at
// the end of a line changes).
type LexState any

// SortKey is one sort term of a browse query.
type SortKey struct {
	Field string
	Desc  bool
}

// BrowseQuery describes the rows of a relation shown by the table view:
// Filter is in the language's own filter syntax (a SQL condition, a
// MongoDB filter document…).
type BrowseQuery struct {
	Filter string
	Sort   []SortKey
	// Projection and Limit are used by document languages (see
	// DocumentLanguage); "" / 0 = all fields, no limit.
	Projection string
	Limit      int64
}

// CompletionRef is an object referenced by a statement.
type CompletionRef struct {
	Schema, Name, Alias string
}

// CompletionContext describes the caret position inside a statement.
type CompletionContext struct {
	Qualifier    string   // "t" in "t.col|"
	QualParts    []string // the whole qualifier chain ("public", "t")
	QualStart    int      // offset where the qualifier chain starts
	Prefix       string   // word being typed
	Refs         []CompletionRef
	ExpectObject bool // the caret is where an object name goes (after FROM…)
}

// Language is the query language of a connection: everything the UI and
// the assistant need to edit, split, classify and build statements without
// knowing their syntax. SQL databases use SQLLanguage over their Dialect.
type Language interface {
	// Name identifies the language (the dialect name for SQL: postgres,
	// cql…); Display is shown to the user ("SQL", "CQL"); Fence is the
	// Markdown code fence tag.
	Name() string
	Display() string
	Fence() string

	// LexLine tokenizes one line of a script (nil state = start of text).
	LexLine(line string, st LexState) ([]sqlx.Token, LexState)
	// Tokens tokenizes a whole text.
	Tokens(text string) []sqlx.Token
	Keywords() map[string]bool
	Functions() []string
	Types() []string
	// LineComment starts a line comment ("--", "//").
	LineComment() string

	// Split splits a script into statements.
	Split(script string) []sqlx.Statement
	// Terminate returns a statement ready to be appended to a script.
	Terminate(stmt string) string
	// Classify inspects a statement for the safety policies: unknown
	// statements must count as writes.
	Classify(stmt string) StmtKind

	// CompletionContext analyses the statement around caret (byte offset).
	CompletionContext(stmt string, caret int) CompletionContext
	// QuoteIdent quotes a name inserted by completion, when needed.
	QuoteIdent(name string) string

	// Browse builds the statement listing the rows of a relation.
	Browse(ref ObjectRef, q BrowseQuery) (string, error)
	// Count builds a statement returning one row with the number of rows
	// matching filter.
	Count(ref ObjectRef, filter string) (string, error)
	// ValidateFilter rejects filters that are not a single read-only
	// condition ("1=1; DELETE …").
	ValidateFilter(filter string) error
	// EqualsFilter is the filter matching rows where field equals v (nil =
	// NULL).
	EqualsFilter(field string, v any, k Kind) string
	// FilterHint is the placeholder of the filter field.
	FilterHint() string
	// FilterLabel names the filter field ("WHERE", "Filter").
	FilterLabel() string
	// SortUnsupported explains why browse results can't be sorted by
	// arbitrary fields ("" = they can, including by primary key).
	SortUnsupported() string

	// DisplayName is the name of an object as written in statements.
	DisplayName(ref ObjectRef) string
	// OpenTemplate is the statement reading a relation, for a new console.
	OpenTemplate(ref ObjectRef) string
	// DropStatement drops (or empties, with truncate) an object; verb
	// names the action. ok is false when the object can't be dropped.
	DropStatement(ref ObjectRef, truncate bool) (stmt, verb string, ok bool)
	// MenuLabels are the navigator actions opening OpenTemplate and
	// Templates in a new console.
	MenuLabels() (open, templates string)
	// Templates renders statement templates (insert/update/delete…) of a
	// table.
	Templates(ref ObjectRef, info *TableInfo) string

	// RenderChange renders a grid edit as a self-contained statement for
	// the review dialog.
	RenderChange(ref ObjectRef, ch Change) string
	// InsertText renders a row as an insert statement (exports, copies);
	// "" when it can't be rendered.
	InsertText(ref ObjectRef, cols []Column, row Row) string
	// InsertFormat is the export format of InsertText (label, extension).
	InsertFormat() (label, ext string)

	// Explain returns the statement showing the plan of stmt
	// (ErrNotSupported when unavailable).
	Explain(stmt string) (string, error)
	// ColumnNames lists the names a statement reads as columns or fields
	// (not object names nor qualifiers): the assistant redacts results
	// reading sensitive ones.
	ColumnNames(stmt string) []string
}

// ChangeApplier is implemented by sessions applying grid edits natively
// (document databases); the others go through ApplyChanges.
type ChangeApplier interface {
	ApplyChanges(ctx context.Context, ref ObjectRef, changes []Change) ([]ApplyResult, error)
}

// DialectOf returns the SQL dialect of a connection (nil when its
// language is not SQL).
func DialectOf(c Conn) Dialect {
	d, _ := SQLDialect(c.Language())
	return d
}

// SQLDialect returns the dialect of a SQL language.
func SQLDialect(l Language) (Dialect, bool) {
	if s, ok := l.(interface{ Dialect() Dialect }); ok {
		return s.Dialect(), true
	}
	return nil, false
}
