package db

import (
	"context"
	"io"
	"strconv"
	"strings"
	"time"
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
	// GenerateCode writes a statement (find, findOne, aggregate,
	// countDocuments, distinct, insert, update, replace, delete) as driver
	// code: target go, python, node, java or csharp; dbName is the
	// database of unqualified collections. A program has the imports and
	// connects to the URI of the MONGODB_URI environment variable; a
	// snippet expects a connected client (and ctx in Go).
	GenerateCode(stmt, dbName, target string, program bool) (string, error)
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
	// ready (optional) runs once the stream is open: changes made after it
	// are seen.
	Watch(ctx context.Context, ref ObjectRef, pipeline string, ready func(), fn func(ChangeEvent)) error
	// Import inserts the documents read from r; progress gets the count.
	Import(ctx context.Context, ref ObjectRef, r io.Reader, o ImportOptions, progress func(int64)) (int64, error)
	// ExportDocument encodes a document: "ejson" (relaxed Extended JSON,
	// one line: small longs read back as ints), "ejson-canonical" (every
	// type kept) or "bson".
	ExportDocument(d DocNode, format string) ([]byte, error)
	// CollectionStats reads the sizes of a collection.
	CollectionStats(ctx context.Context, ref ObjectRef) (*CollectionStats, error)
	// IndexUsage lists the indexes of a collection with their use since
	// the server started and the problems found (unused, duplicate,
	// prefix of another index).
	IndexUsage(ctx context.Context, ref ObjectRef) ([]IndexUsage, error)
	// Profiler reads the profiling level of a database; SlowQueries the
	// operations it recorded (newest first), with an index suggested for
	// the ones scanning or sorting.
	Profiler(ctx context.Context, dbName string) (*ProfilerStatus, error)
	SlowQueries(ctx context.Context, dbName string, limit int) ([]SlowQuery, error)
	// Topology is "standalone", "replicaset" or "sharded" (connected to a
	// router).
	Topology() string
	ReplicaSet(ctx context.Context) (*ReplicaSetStatus, error)
	Sharding(ctx context.Context) (*ShardingStatus, error)
}

// ReplicaSetStatus is the state of a replica set.
type ReplicaSetStatus struct {
	Name    string
	Members []ReplicaMember
	// Oplog window: the time span the oplog of the primary keeps (a
	// member behind more than that must resync).
	OplogFirst, OplogLast time.Time
	OplogSize, OplogMax   int64
}

// ReplicaMember is a member of a replica set.
type ReplicaMember struct {
	Name       string
	State      string // PRIMARY, SECONDARY, ARBITER…
	Healthy    bool
	Self       bool
	Uptime     int64 // seconds
	Optime     time.Time
	Lag        time.Duration // behind the primary
	PingMs     int64
	SyncSource string
	Message    string
}

// ShardingStatus is the state of a sharded cluster.
type ShardingStatus struct {
	Shards      []Shard
	Balancer    string // running / enabled / disabled…
	Routers     []string
	Databases   []ShardedDatabase
	Collections []ShardedCollection
}

// Shard is a shard of a cluster.
type Shard struct {
	ID, Host string
	State    int
	Draining bool
}

// ShardedDatabase is a database of a cluster with its primary shard.
type ShardedDatabase struct {
	Name, Primary string
}

// ShardedCollection is a sharded collection with its chunks per shard.
type ShardedCollection struct {
	NS      string
	Key     string
	Unique  bool
	Chunks  map[string]int // shard → chunks
	Jumbo   int
	Balance bool // the balancer moves its chunks
}

// CollectionStats are the sizes of a collection (bytes).
type CollectionStats struct {
	Documents   int64
	DataSize    int64 // uncompressed documents
	StorageSize int64 // on disk, documents
	IndexSize   int64 // on disk, all indexes
	TotalSize   int64
	AvgDocSize  int64
	Indexes     int
	Capped      bool
}

// IndexUsage is an index with its use and the problems found.
type IndexUsage struct {
	Name   string
	Keys   string // key pattern, as written in the language
	Ops    int64  // accesses since Since
	Since  time.Time
	Size   int64
	Unique bool
	Hidden bool
	// Issue: "" or why the index looks useless (never used, duplicate
	// of another one, prefix of another one).
	Issue string
}

// ProfilerStatus is the profiling level of a database.
type ProfilerStatus struct {
	Level  int // 0 off, 1 slow operations, 2 all
	SlowMs int64
}

// SlowQuery is an operation recorded by the profiler.
type SlowQuery struct {
	At           time.Time
	NS           string
	Op           string
	Millis       int64
	DocsExamined int64
	KeysExamined int64
	Returned     int64
	Plan         string
	Command      string
	Suggestion   *IndexSuggestion
}

// Field paths of documents: names joined by dots; a name containing a dot
// or a quote, or starting with $, is written quoted ("a.b") so that paths
// stay unambiguous.

// fieldNeedsQuote reports whether a field name must be quoted in a path.
func fieldNeedsQuote(name string) bool {
	return name == "" || strings.ContainsAny(name, `."`) || strings.HasPrefix(name, "$")
}

// JoinFieldPath appends a field name to a document path ("" = top level).
func JoinFieldPath(parent, name string) string {
	if fieldNeedsQuote(name) {
		name = strconv.Quote(name)
	}
	if parent == "" {
		return name
	}
	return parent + "." + name
}

// SplitFieldPath returns the field names of a path.
func SplitFieldPath(p string) []string {
	var out []string
	for len(p) > 0 {
		if p[0] == '"' {
			// quoted name: up to the closing quote
			i := 1
			for i < len(p) && p[i] != '"' {
				if p[i] == '\\' {
					i++
				}
				i++
			}
			end := min(i+1, len(p))
			name, err := strconv.Unquote(p[:end])
			if err != nil {
				name = strings.Trim(p[:end], `"`)
			}
			out = append(out, name)
			p = strings.TrimPrefix(p[end:], ".")
			continue
		}
		i := strings.IndexByte(p, '.')
		if i < 0 {
			out = append(out, p)
			break
		}
		out = append(out, p[:i])
		p = p[i+1:]
	}
	return out
}

// PlainFieldPath reports whether every name of a path can be used in a
// dotted path of the database (no quoted names).
func PlainFieldPath(p string) bool {
	for _, n := range SplitFieldPath(p) {
		if fieldNeedsQuote(n) {
			return false
		}
	}
	return true
}
