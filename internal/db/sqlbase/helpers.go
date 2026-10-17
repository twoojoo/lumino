package sqlbase

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/twoojoo/lumino/internal/db"
)

// Rec is one metadata row: column name (lower case) → value as text.
type Rec map[string]string

// Str returns a field.
func (r Rec) Str(k string) string { return r[k] }

// Bool reports a boolean-ish field (YES, Y, 1, t, true).
func (r Rec) Bool(k string) bool {
	switch strings.ToUpper(strings.TrimSpace(r[k])) {
	case "YES", "Y", "1", "T", "TRUE":
		return true
	}
	return false
}

// Int returns a numeric field (0 when empty or invalid).
func (r Rec) Int(k string) int64 {
	var n int64
	fmt.Sscan(strings.TrimSpace(r[k]), &n)
	return n
}

// Recs runs a metadata query and returns rows as text records. NULLs are
// empty strings; [] bytes are decoded as text.
func Recs(ctx context.Context, q Querier, query string, args ...any) ([]Rec, error) {
	ctx, cancel := context.WithTimeout(ctx, db.MetaTimeout)
	defer cancel()
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	for i := range cols {
		cols[i] = strings.ToLower(cols[i])
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	var out []Rec
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		r := make(Rec, len(cols))
		for i, v := range vals {
			r[cols[i]] = text(v)
			vals[i] = nil
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func text(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(x)
	case string:
		return x
	case time.Time:
		return x.Format(time.RFC3339Nano)
	}
	return db.FormatValue(db.Normalize(v), db.KindOther)
}

// Strings runs a query returning one text column.
func Strings(ctx context.Context, q Querier, query string, args ...any) ([]string, error) {
	recs, err := Recs(ctx, q, query, args...)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		for _, v := range r {
			out = append(out, v)
		}
	}
	return out, nil
}

// One runs a query returning a single text value ("" when no rows).
func One(ctx context.Context, q Querier, query string, args ...any) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, db.MetaTimeout)
	defer cancel()
	var v any
	err := q.QueryRowContext(ctx, query, args...).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return text(v), nil
}

// SplitList splits a comma separated column list from metadata queries.
func SplitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// GroupCompletion builds the completion map from (table, column) records.
func GroupCompletion(recs []Rec, tableKey, colKey string) map[string][]string {
	out := map[string][]string{}
	for _, r := range recs {
		t := r[tableKey]
		out[t] = append(out[t], r[colKey])
	}
	return out
}

// Types returns the requested object types, or all of them when empty.
func Types(types []db.ObjectType, all ...db.ObjectType) []db.ObjectType {
	if len(types) == 0 {
		return all
	}
	return types
}

// Wants reports whether t is in types (empty = all).
func Wants(types []db.ObjectType, t db.ObjectType) bool {
	if len(types) == 0 {
		return true
	}
	for _, x := range types {
		if x == t {
			return true
		}
	}
	return false
}

// Q is a query for RecsParallel.
type Q struct {
	SQL  string
	Args []any
}

// RecsParallel runs independent metadata queries concurrently (catalogs
// whose dictionary views are slow per query, e.g. Oracle).
func RecsParallel(ctx context.Context, q Querier, qs ...Q) ([][]Rec, error) {
	out := make([][]Rec, len(qs))
	errs := make([]error, len(qs))
	done := make(chan struct{}, len(qs))
	for i, x := range qs {
		go func() {
			out[i], errs[i] = Recs(ctx, q, x.SQL, x.Args...)
			done <- struct{}{}
		}()
	}
	for range qs {
		<-done
	}
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
