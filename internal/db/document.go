package db

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
}
