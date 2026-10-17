package db

import (
	"context"
	"io"
)

// Document databases: whole documents next to the (flattened) rows, and
// the language features of document editing.

// DocNode is a field of a document. Name is the key (the index for array
// items), Type the database type name (objectId, string, int, long,
// double, decimal, date, object, array…), Kind its grid kind. Leaves have
// Value (in the Row value set) and Lit, the value written in the
// connection's language (it reads back with the same type); objects and
// arrays have Children.
type DocNode struct {
	Name     string
	Type     string
	Kind     Kind
	Value    any
	Lit      string
	Children []DocNode
}

// IsObject reports whether n is a document (an object).
func (n DocNode) IsObject() bool { return n.Type == "object" }

// IsArray reports whether n is an array.
func (n DocNode) IsArray() bool { return n.Type == "array" }

// Field returns the child named name.
func (n DocNode) Field(name string) (DocNode, bool) {
	for _, c := range n.Children {
		if c.Name == name {
			return c, true
		}
	}
	return DocNode{}, false
}

// DocumentResult is implemented by results of document databases:
// Documents returns the whole documents of the rows returned by the last
// Fetch, in the same order (fields not shown as columns included).
type DocumentResult interface {
	Documents() []DocNode
}

// unset is the type of Unset.
type unset struct{}

func (unset) String() string { return "(remove field)" }

// Unset as a ColValue value removes the field (document databases).
var Unset any = unset{}

// DocumentLanguage is implemented by the languages of document databases:
// the browse query has a projection, a sort document and a limit, and
// documents are edited as text.
type DocumentLanguage interface {
	Language
	// ValidateProjection accepts a projection document ("" = all fields).
	ValidateProjection(text string) error
	// ParseSort reads a sort document ({a: 1, b: -1}); FormatSort writes one.
	ParseSort(text string) ([]SortKey, error)
	FormatSort(keys []SortKey) string
	// FormatDocument renders a document for editing (indented, types
	// written so that they read back unchanged).
	FormatDocument(d DocNode) string
	// DocumentChanges compares an edited document with the original: the
	// per-path values to set (Raw literals) or remove (Unset), and the key
	// of the original document. No values: nothing changed.
	DocumentChanges(orig DocNode, edited string) (key, values []ColValue, err error)
	// NewDocument parses a document to insert (top-level fields as Raw
	// literals).
	NewDocument(text string) ([]ColValue, error)
	// Operators lists the query operators (completion of the query bar).
	Operators() []string
	// StageTemplates lists the aggregation stages with a template body
	// ({"$match", "{}"}…), for the pipeline builder.
	StageTemplates() [][2]string
	// Aggregate builds the statement running stages (each one a stage
	// document) on a collection, with a final limit (0 = none).
	Aggregate(ref ObjectRef, stages []string, limit int) (string, error)
	// TranslateSQL translates a SQL statement (SELECT with WHERE, GROUP
	// BY, ORDER BY, LIMIT; INSERT; UPDATE; DELETE) to the language.
	TranslateSQL(sql string) (string, error)
	// AdminStatement builds a management statement: createIndex (keys,
	// options), dropIndex (name), setValidator (validator, level, action),
	// listUsers, createUser (user, password, roles), dropUser (user),
	// grantRoles / revokeRoles (user, roles), killOp (operation id).
	// ref names the collection or (Schema) the database.
	AdminStatement(op string, ref ObjectRef, args ...string) (string, error)
}

// SchemaReport describes the fields of a collection from a sample.
type SchemaReport struct {
	Sampled int
	Total   int64 // estimated documents (-1 = unknown)
	Fields  []SchemaField
}

// SchemaField is a field path of a schema report.
type SchemaField struct {
	Path     string
	Presence float64 // share of the sampled documents with the field
	Types    []TypeShare
	Min, Max string // numbers, dates, strings (as written in the language)
	Top      []ValueCount
	Distinct int // distinct values seen (capped)
}

// TypeShare is the share of a type among the values of a field.
type TypeShare struct {
	Type  string
	Share float64
}

// ValueCount is a frequent value.
type ValueCount struct {
	Value string
	Count int
}

// PlanNode is a stage of a query plan.
type PlanNode struct {
	Stage        string
	Index        string
	Details      string
	NReturned    int64
	DocsExamined int64
	KeysExamined int64
	Millis       int64
	Children     []PlanNode
}

// ExplainReport is the executed plan of a query with index suggestions.
type ExplainReport struct {
	Statement    string
	Plan         PlanNode
	Returned     int64
	DocsExamined int64
	KeysExamined int64
	Millis       int64
	Suggestions  []IndexSuggestion
}

// IndexSuggestion is an index that would serve a query better.
type IndexSuggestion struct {
	Reason    string
	Keys      string
	Statement string // creates it
}

// ServerStats is a snapshot of the server status.
type ServerStats struct {
	Version string
	Host    string
	Uptime  int64 // seconds
	Role    string
	// Gauges are current values (connections, memory…), Counters are
	// cumulative (operations, bytes): rates come from two snapshots.
	Gauges   [][2]string
	Counters map[string]float64
}

// Operation is a running server operation.
type Operation struct {
	ID      string // as written in the language (killOp argument)
	Op      string
	NS      string
	Client  string
	Desc    string
	Seconds int64
	Command string
}

// ChangeEvent is a change seen by a change stream.
type ChangeEvent struct {
	Op     string
	NS     string
	Key    string
	Fields string // updated / removed fields
	Doc    DocNode
}

// ImportOptions configure an import.
type ImportOptions struct {
	Format string // json (array or one document per line, Extended JSON), csv, bson
	Drop   bool   // drop the collection first
	// CSVTypes: numbers, booleans and ISO dates of CSV files are typed
	// (else every value is a string).
	CSVTypes bool
}

// DocumentTools are the management features of document database
// connections.
type DocumentTools interface {
	AnalyzeSchema(ctx context.Context, ref ObjectRef, sample int) (*SchemaReport, error)
	// ExplainFind runs the query of the browse parameters with
	// executionStats and suggests indexes.
	ExplainFind(ctx context.Context, ref ObjectRef, q BrowseQuery) (*ExplainReport, error)
	ServerStats(ctx context.Context) (*ServerStats, error)
	Operations(ctx context.Context, all bool) ([]Operation, error)
	// Watch streams the changes of a collection (Name = "": of the
	// database) until ctx is done; pipeline filters them ("" = all).
	Watch(ctx context.Context, ref ObjectRef, pipeline string, fn func(ChangeEvent)) error
	// Import inserts the documents read from r; progress gets the count.
	Import(ctx context.Context, ref ObjectRef, r io.Reader, o ImportOptions, progress func(int64)) (int64, error)
	// ExportDocument encodes a document: "ejson" (relaxed Extended JSON,
	// one line: small longs read back as ints), "ejson-canonical" (every
	// type kept) or "bson".
	ExportDocument(d DocNode, format string) ([]byte, error)
}
