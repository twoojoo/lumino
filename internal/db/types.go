// Package db is lumino's database API: a driver-neutral model of
// connections, sessions, result cursors, catalogs and SQL dialects that the
// UI talks to. Each supported database implements it in an adapter
// (internal/db/adapters/...).
package db

import (
	"errors"
	"fmt"
	"strings"
)

// Kind classifies a column's values for formatting, editing and alignment.
type Kind int

const (
	KindOther Kind = iota
	KindText
	KindInt
	KindFloat
	KindDecimal // exact numerics, kept as decimal strings
	KindBool
	KindDate
	KindTime
	KindTimestamp
	KindTimestampTZ
	KindInterval
	KindBytes
	KindJSON
	KindUUID
	KindArray
)

func (k Kind) String() string {
	switch k {
	case KindText:
		return "text"
	case KindInt:
		return "int"
	case KindFloat:
		return "float"
	case KindDecimal:
		return "decimal"
	case KindBool:
		return "bool"
	case KindDate:
		return "date"
	case KindTime:
		return "time"
	case KindTimestamp:
		return "timestamp"
	case KindTimestampTZ:
		return "timestamptz"
	case KindInterval:
		return "interval"
	case KindBytes:
		return "bytes"
	case KindJSON:
		return "json"
	case KindUUID:
		return "uuid"
	case KindArray:
		return "array"
	}
	return "other"
}

// Numeric reports whether values are right-aligned numbers.
func (k Kind) Numeric() bool { return k == KindInt || k == KindFloat || k == KindDecimal }

// Column describes a result column.
type Column struct {
	Name     string
	DBType   string // database type name as reported by the driver, upper case
	Kind     Kind
	Nullable bool // unknown → true
	Length   int64
	Prec     int64
	Scale    int64
}

// Row is one result row. Values are normalized by the adapter to: nil (SQL
// NULL), int64, uint64, float64, bool, string (also decimals, intervals,
// UUIDs, JSON), []byte, time.Time, or []any (arrays, collections).
type Row []any

// ObjectType is the type of a catalog object.
type ObjectType string

const (
	ObjTable     ObjectType = "table"
	ObjView      ObjectType = "view"
	ObjMatView   ObjectType = "materialized view"
	ObjFunction  ObjectType = "function"
	ObjProcedure ObjectType = "procedure"
	ObjSequence  ObjectType = "sequence"
	ObjTrigger   ObjectType = "trigger"
	ObjIndex     ObjectType = "index"
	ObjType      ObjectType = "type"
	ObjPackage   ObjectType = "package"
	ObjSchema    ObjectType = "schema"
	ObjCatalog   ObjectType = "catalog"
)

// Plural is the folder title of a type in the navigation tree.
func (t ObjectType) Plural() string {
	switch t {
	case ObjTable:
		return "Tables"
	case ObjView:
		return "Views"
	case ObjMatView:
		return "Materialized views"
	case ObjFunction:
		return "Functions"
	case ObjProcedure:
		return "Procedures"
	case ObjSequence:
		return "Sequences"
	case ObjTrigger:
		return "Triggers"
	case ObjIndex:
		return "Indexes"
	case ObjType:
		return "Types"
	case ObjPackage:
		return "Packages"
	}
	return string(t)
}

// Relation reports whether the object holds rows (can be browsed).
func (t ObjectType) Relation() bool { return t == ObjTable || t == ObjView || t == ObjMatView }

// ObjectRef identifies a catalog object. Catalog is empty for databases
// without a catalog level; Schema is empty for databases without schemas
// (Firebird). Name is empty for a schema or catalog reference.
type ObjectRef struct {
	Catalog string     `json:"catalog,omitempty"`
	Schema  string     `json:"schema,omitempty"`
	Name    string     `json:"name,omitempty"`
	Type    ObjectType `json:"type,omitempty"`
	// Signature disambiguates overloaded routines, e.g. "(integer, text)".
	Signature string `json:"signature,omitempty"`
}

// String is a human readable dotted name (not quoted for SQL).
func (r ObjectRef) String() string {
	var parts []string
	for _, p := range []string{r.Catalog, r.Schema, r.Name} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ".") + r.Signature
}

// Object is an entry of a catalog listing.
type Object struct {
	Ref     ObjectRef
	Comment string
	// Extra is a short driver-specific detail (e.g. engine, row estimate).
	Extra string
}

// Schema is a schema (or database, keyspace…) listed by a catalog.
type Schema struct {
	Name    string
	System  bool // pg_catalog, information_schema, sys…
	Current bool // the session's default schema
}

// TableColumn is a column of a table definition.
type TableColumn struct {
	Name         string
	Type         string // full type, e.g. varchar(20), numeric(10,2)
	Kind         Kind
	Nullable     bool
	Default      string // SQL expression, "" = none
	HasDefault   bool
	AutoIncr     bool // identity / serial / auto_increment
	Generated    string
	Comment      string
	PrimaryKeyNo int // 1-based position in the primary key, 0 = not part of it
}

// Index is an index of a table.
type Index struct {
	Name    string
	Columns []string // column names or expressions
	Unique  bool
	Primary bool
	Method  string // btree, hash… ("" = default)
	Where   string // partial index predicate
}

// ForeignKey is a foreign key constraint.
type ForeignKey struct {
	Name       string
	Columns    []string
	RefTable   ObjectRef
	RefColumns []string
	OnUpdate   string
	OnDelete   string
}

// Constraint is a check or unique constraint.
type Constraint struct {
	Name       string
	Type       string // CHECK | UNIQUE | EXCLUDE…
	Columns    []string
	Definition string
}

// Trigger is a table trigger.
type Trigger struct {
	Name       string
	Timing     string // BEFORE, AFTER, INSTEAD OF
	Events     string // INSERT, UPDATE…
	Definition string
}

// TableInfo is the structure of a table or view.
type TableInfo struct {
	Ref         ObjectRef
	Columns     []TableColumn
	PrimaryKey  []string // column names in key order
	Indexes     []Index
	ForeignKeys []ForeignKey
	Constraints []Constraint
	Triggers    []Trigger
	Comment     string
	RowEstimate int64 // -1 = unknown
	// Extra lists driver specific properties (engine, partition key…).
	Extra [][2]string
}

// Column returns the column named name.
func (t *TableInfo) Column(name string) (TableColumn, bool) {
	for _, c := range t.Columns {
		if c.Name == name {
			return c, true
		}
	}
	for _, c := range t.Columns {
		if strings.EqualFold(c.Name, name) {
			return c, true
		}
	}
	return TableColumn{}, false
}

// ErrNotSupported is returned for operations a database doesn't offer.
var ErrNotSupported = errors.New("not supported by this database")

// Error is a database error with its optional position in the statement.
type Error struct {
	Msg      string
	Code     string
	Position int // 1-based character offset in the statement, 0 = unknown
	Line     int // 1-based line in the statement, 0 = unknown
	Detail   string
	Hint     string
	Err      error
}

func (e *Error) Error() string {
	s := e.Msg
	if e.Code != "" {
		s = fmt.Sprintf("%s (%s)", s, e.Code)
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }

// AsError returns err as an *Error (wrapping plain errors).
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Msg: err.Error(), Err: err}
}
