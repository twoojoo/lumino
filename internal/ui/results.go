package ui

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/giovanni/lumino/internal/db"
)

// resultSet is the output of one statement: rows streamed from an open
// cursor, or an update count, or an error.
type resultSet struct {
	sql       string
	stmtStart int // byte offset of the statement in the console text
	cols      []db.Column
	rows      []db.Row
	res       db.Result
	more      bool
	fetching  bool
	err       error
	affected  int64
	isQuery   bool
	elapsed   time.Duration
	started   time.Time
	partial   string // why the cursor was closed before the end
	limitHit  bool
	gen       int // console run generation (stale guard)

	sortCol  int
	sortDesc bool
	order    []int // client-side sort permutation (nil = natural)
}

var _ gridModel = (*resultSet)(nil)

func (r *resultSet) Columns() []db.Column { return r.cols }
func (r *resultSet) Rows() int            { return len(r.rows) }
func (r *resultSet) RowState(int) rowState {
	return rowNormal
}
func (r *resultSet) CellChanged(int, int) bool { return false }

func (r *resultSet) Value(i, c int) any {
	if r.order != nil && i < len(r.order) {
		i = r.order[i]
	}
	if i >= len(r.rows) || c >= len(r.rows[i]) {
		return nil
	}
	return r.rows[i][c]
}

// row returns the underlying row index of a displayed row.
func (r *resultSet) row(i int) int {
	if r.order != nil && i < len(r.order) {
		return r.order[i]
	}
	return i
}

// summary is the status line of the result.
func (r *resultSet) summary() string {
	switch {
	case r.err != nil:
		return "Error"
	case !r.isQuery:
		if r.affected >= 0 {
			return fmt.Sprintf("%d row(s) affected · %s", r.affected, fmtDur(r.elapsed))
		}
		return "OK · " + fmtDur(r.elapsed)
	}
	s := fmt.Sprintf("%d row(s)", len(r.rows))
	if r.more {
		s += "+ (scroll to fetch more)"
	}
	if r.fetching {
		s += " · fetching…"
	}
	if r.partial != "" {
		s += " · " + r.partial
	}
	if r.limitHit {
		s += " · row limit reached"
	}
	return s + " · " + fmtDur(r.elapsed)
}

func fmtDur(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}

// fetchMore loads the next page in background.
func (r *resultSet) fetchMore(a *App, n int, all bool, done func()) {
	if !r.more || r.fetching || r.res == nil {
		return
	}
	limit := a.prefs.RowLimit()
	if len(r.rows) >= limit && !all {
		r.limitHit = true
		return
	}
	r.fetching = true
	res := r.res
	have := len(r.rows)
	go func() {
		var got []db.Row
		more := true
		var err error
		total := have
		for {
			var rows []db.Row
			rows, more, err = res.Fetch(context.Background(), n)
			got = append(got, rows...)
			total += len(rows)
			if !all || !more || err != nil || total >= limit*4 {
				break
			}
			if len(got) >= 50000 { // hand rows to the UI in chunks
				chunk := got
				got = nil
				a.post(func() { r.rows = append(r.rows, chunk...); r.resort() })
			}
		}
		a.post(func() {
			r.fetching = false
			r.rows = append(r.rows, got...)
			r.more = more && err == nil
			if err != nil {
				r.err = err
				a.flash("Fetch failed: "+err.Error(), true)
			}
			if len(r.rows) >= limit && r.more && !all {
				r.limitHit = true
			}
			if !r.more {
				r.closeCursor()
			}
			r.resort()
			if done != nil {
				done()
			}
		})
	}()
}

func (r *resultSet) closeCursor() {
	if r.res != nil {
		res := r.res
		r.res = nil
		go res.Close()
	}
}

// detach marks the cursor closed by a later statement on the session.
func (r *resultSet) detach() {
	if r.more {
		r.partial = "cursor closed by a later statement (re-run to fetch more)"
	}
	r.more = false
	r.res = nil
}

// sortBy sorts the fetched rows client-side (asc → desc → natural).
func (r *resultSet) sortBy(c int) {
	switch {
	case r.sortCol != c:
		r.sortCol, r.sortDesc = c, false
	case !r.sortDesc:
		r.sortDesc = true
	default:
		r.sortCol, r.order = -1, nil
		return
	}
	r.resort()
}

func (r *resultSet) resort() {
	if r.sortCol < 0 || r.sortCol >= len(r.cols) {
		r.order = nil
		return
	}
	c, desc := r.sortCol, r.sortDesc
	if r.order == nil || len(r.order) != len(r.rows) {
		r.order = make([]int, len(r.rows))
		for i := range r.order {
			r.order[i] = i
		}
	}
	k := r.cols[c].Kind
	sort.SliceStable(r.order, func(i, j int) bool {
		cmp := compareValues(r.rows[r.order[i]][c], r.rows[r.order[j]][c], k)
		if desc {
			return cmp > 0
		}
		return cmp < 0
	})
}

// compareValues orders values of a column (NULL last).
func compareValues(a, b any, k db.Kind) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	}
	switch x := a.(type) {
	case int64:
		if y, ok := b.(int64); ok {
			return cmpOrd(x, y)
		}
	case uint64:
		if y, ok := b.(uint64); ok {
			return cmpOrd(x, y)
		}
	case float64:
		if y, ok := b.(float64); ok {
			return cmpOrd(x, y)
		}
	case time.Time:
		if y, ok := b.(time.Time); ok {
			return x.Compare(y)
		}
	case bool:
		if y, ok := b.(bool); ok {
			if x == y {
				return 0
			}
			if !x {
				return -1
			}
			return 1
		}
	}
	sa, sb := db.FormatValue(a, k), db.FormatValue(b, k)
	if k.Numeric() {
		ra, ok1 := new(big.Rat).SetString(sa)
		rb, ok2 := new(big.Rat).SetString(sb)
		if ok1 && ok2 {
			return ra.Cmp(rb)
		}
	}
	return strings.Compare(sa, sb)
}

func cmpOrd[T int64 | uint64 | float64](a, b T) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
