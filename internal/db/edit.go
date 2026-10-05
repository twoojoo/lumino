package db

import (
	"context"
	"fmt"
	"strings"
)

// ChangeOp is a grid edit operation.
type ChangeOp int

const (
	OpInsert ChangeOp = iota
	OpUpdate
	OpDelete
)

func (o ChangeOp) String() string {
	switch o {
	case OpInsert:
		return "insert"
	case OpUpdate:
		return "update"
	}
	return "delete"
}

// ColValue is a column value of a change.
type ColValue struct {
	Column string
	Kind   Kind
	Value  any // nil = NULL
}

// Change is one row edit. Key identifies the row (primary key columns and
// their original values); Values are the inserted or updated columns.
type Change struct {
	Op     ChangeOp
	Key    []ColValue
	Values []ColValue
}

// Raw is a value rendered verbatim into the statement (never bound): a
// validated literal for types a driver can't bind from text.
type Raw string

// ChangeBuilder is implemented by dialects whose DML differs from the
// standard (ClickHouse mutations, Cassandra).
type ChangeBuilder interface {
	BuildChange(ref ObjectRef, ch Change, literal bool) (string, []any)
}

// BuildChange renders a change as a statement with bind parameters, or,
// with literal, as a self-contained statement for previews.
func BuildChange(d Dialect, ref ObjectRef, ch Change, literal bool) (string, []any) {
	if cb, ok := d.(ChangeBuilder); ok {
		return cb.BuildChange(ref, ch, literal)
	}
	return StandardChange(d, ref, ch, literal)
}

// StandardChange renders INSERT/UPDATE/DELETE in standard SQL.
func StandardChange(d Dialect, ref ObjectRef, ch Change, literal bool) (string, []any) {
	var args []any
	val := func(cv ColValue) string {
		if r, ok := cv.Value.(Raw); ok {
			return string(r)
		}
		if literal {
			return d.Literal(cv.Value, cv.Kind)
		}
		if cv.Value == nil {
			return "NULL"
		}
		args = append(args, cv.Value)
		return d.Placeholder(len(args))
	}
	var b strings.Builder
	table := d.Qualified(ref)
	switch ch.Op {
	case OpInsert:
		cols := make([]string, len(ch.Values))
		vals := make([]string, len(ch.Values))
		for i, cv := range ch.Values {
			cols[i] = d.Quote(cv.Column)
			vals[i] = val(cv)
		}
		if len(cols) == 0 {
			fmt.Fprintf(&b, "INSERT INTO %s DEFAULT VALUES", table)
		} else {
			fmt.Fprintf(&b, "INSERT INTO %s (%s) VALUES (%s)", table, strings.Join(cols, ", "), strings.Join(vals, ", "))
		}
		return b.String(), args
	case OpUpdate:
		sets := make([]string, len(ch.Values))
		for i, cv := range ch.Values {
			sets[i] = d.Quote(cv.Column) + " = " + val(cv)
		}
		fmt.Fprintf(&b, "UPDATE %s SET %s WHERE %s", table, strings.Join(sets, ", "), KeyWhere(d, ch.Key, val))
	case OpDelete:
		fmt.Fprintf(&b, "DELETE FROM %s WHERE %s", table, KeyWhere(d, ch.Key, val))
	}
	return b.String(), args
}

// KeyWhere renders the row identification predicate.
func KeyWhere(d Dialect, key []ColValue, val func(ColValue) string) string {
	conds := make([]string, len(key))
	for i, cv := range key {
		if cv.Value == nil {
			conds[i] = d.Quote(cv.Column) + " IS NULL"
		} else {
			conds[i] = d.Quote(cv.Column) + " = " + val(cv)
		}
	}
	return strings.Join(conds, " AND ")
}

// ApplyResult reports one applied change.
type ApplyResult struct {
	Change   int // index in the batch
	Affected int64
	SQL      string
}

// ApplyChanges executes a batch of changes on a session in a transaction
// (unless the session already has one open, in which case the caller owns
// commit/rollback). Updates and deletes must touch exactly one row,
// otherwise everything is rolled back. Databases without transactions
// apply changes one by one and stop at the first failure.
func ApplyChanges(ctx context.Context, s Session, caps Capabilities, d Dialect, ref ObjectRef, changes []Change) ([]ApplyResult, error) {
	own := false
	if caps.Transactions && !s.TxActive() {
		if err := s.Begin(ctx); err != nil {
			return nil, err
		}
		own = true
	}
	fail := func(err error) ([]ApplyResult, error) {
		if own {
			_ = s.Rollback(ctx)
		}
		return nil, err
	}
	var out []ApplyResult
	for i, ch := range changes {
		q, args := BuildChange(d, ref, ch, false)
		res, err := s.Execute(ctx, q, args...)
		if err != nil {
			return fail(fmt.Errorf("%s row %d: %w", ch.Op, i+1, err))
		}
		n := res.RowsAffected()
		res.Close()
		if ch.Op != OpInsert && n >= 0 && n != 1 && caps.Transactions {
			if n == 0 {
				return fail(fmt.Errorf("%s row %d: no row matched (it was changed or deleted by someone else); nothing was saved", ch.Op, i+1))
			}
			return fail(fmt.Errorf("%s row %d: %d rows matched the key; nothing was saved", ch.Op, i+1, n))
		}
		out = append(out, ApplyResult{Change: i, Affected: n, SQL: q})
	}
	if own {
		if err := s.Commit(ctx); err != nil {
			return nil, err
		}
	}
	return out, nil
}
