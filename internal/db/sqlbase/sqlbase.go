// Package sqlbase implements db.Conn, db.Session and db.Result on top of
// database/sql. Adapters provide a catalog, a dialect and hooks for the
// behaviour that differs between drivers.
package sqlbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/db/sqlx"
)

// Hooks customize the generic implementation.
type Hooks struct {
	// Kind maps a database type name (as reported by ColumnTypes, upper
	// case) to a value kind. Unknown types fall back to the scan type.
	Kind func(dbType string) db.Kind
	// Column adjusts a result column (e.g. NUMBER(10,0) is an integer).
	Column func(c *db.Column)
	// Convert adjusts a scanned value before db.Normalize (e.g. decode
	// MySQL []byte to the right type).
	Convert func(col *db.Column, v any) any
	// ReturnsRows overrides the statement classification.
	ReturnsRows func(stmt string) bool
	// SessionInit runs on every new session connection.
	SessionInit func(ctx context.Context, c *sql.Conn) error
	// SessionID returns the server id of a session (for Cancel).
	SessionID func(ctx context.Context, c *sql.Conn) (string, error)
	// StatementContext decorates the context of every statement and returns
	// an id passed to Cancel (ClickHouse query ids).
	StatementContext func(ctx context.Context) (context.Context, string)
	// Cancel aborts the statement running on a session out of band. When
	// it returns true the statement's context is left alone (cancelling it
	// would close the connection with some drivers).
	Cancel           func(ctx context.Context, db *sql.DB, sessionID string) bool
	CurrentSchema    func(ctx context.Context, q Querier) (string, error)
	SetCurrentSchema func(ctx context.Context, q Querier, schema string) error
	SetReadOnly      func(ctx context.Context, q Querier, on bool) error
	// MapError converts driver errors (position, code).
	MapError func(err error) error
	// TxOptions used by Begin (nil = driver default).
	TxOptions *sql.TxOptions
	// NoTx: the database has no transactions (Begin returns ErrNotSupported).
	NoTx bool
}

// Querier is satisfied by *sql.DB, *sql.Conn and *sql.Tx.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Conn is a database/sql backed db.Conn.
type Conn struct {
	DB      *sql.DB
	Inf     db.Info
	Cap     db.Capabilities
	D       db.Dialect
	Cat     db.Catalog
	Version string
	H       Hooks
	InitSQL string
	// OnClose runs after the pool is closed (tunnels, temp files).
	OnClose []func()

	closeOnce sync.Once
}

var _ db.Conn = (*Conn)(nil)

func (c *Conn) Info() db.Info                  { return c.Inf }
func (c *Conn) Caps() db.Capabilities          { return c.Cap }
func (c *Conn) Dialect() db.Dialect            { return c.D }
func (c *Conn) ServerVersion() string          { return c.Version }
func (c *Conn) Catalog() db.Catalog            { return c.Cat }
func (c *Conn) Ping(ctx context.Context) error { return c.mapErr(c.DB.PingContext(ctx)) }

func (c *Conn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		err = c.DB.Close()
		for _, f := range c.OnClose {
			f()
		}
	})
	return err
}

func (c *Conn) mapErr(err error) error {
	if err == nil {
		return nil
	}
	if c.H.MapError != nil {
		return c.H.MapError(err)
	}
	return err
}

// Session opens a dedicated session.
func (c *Conn) Session(ctx context.Context) (db.Session, error) {
	s := &Session{c: c}
	if err := s.connect(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Session is a dedicated connection with its transaction state.
type Session struct {
	c        *Conn
	conn     *sql.Conn
	tx       *sql.Tx
	id       string
	readOnly bool

	mu      sync.Mutex
	cancel  context.CancelFunc // of the running statement / open cursor
	running bool
	runID   string // StatementContext id of the current statement
	cur     *Result
}

var _ db.Session = (*Session)(nil)

func (s *Session) connect(ctx context.Context) error {
	conn, err := s.c.DB.Conn(ctx)
	if err != nil {
		return s.c.mapErr(err)
	}
	if h := s.c.H.SessionInit; h != nil {
		if err := h(ctx, conn); err != nil {
			conn.Close()
			return s.c.mapErr(err)
		}
	}
	if init := strings.TrimSpace(s.c.InitSQL); init != "" {
		for _, st := range sqlx.Split(init, s.c.D.Lex()) {
			if _, err := conn.ExecContext(ctx, st.Text); err != nil {
				conn.Close()
				return fmt.Errorf("init SQL %q: %w", st.Text, s.c.mapErr(err))
			}
		}
	}
	if h := s.c.H.SessionID; h != nil {
		s.id, _ = h(ctx, conn)
	}
	if s.readOnly && s.c.H.SetReadOnly != nil {
		_ = s.c.H.SetReadOnly(ctx, conn, true)
	}
	s.conn = conn
	return nil
}

// q returns the transaction if one is open, else the session connection.
func (s *Session) q() Querier {
	if s.tx != nil {
		return s.tx
	}
	return s.conn
}

func isConnLost(err error) bool {
	return errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone)
}

// ErrSessionReset is reported when the connection broke and a new session
// was opened: session state and any open transaction are lost.
var ErrSessionReset = errors.New("connection lost: a new session was opened (session settings and the open transaction were discarded)")

func (s *Session) recover(ctx context.Context, err error) error {
	if !isConnLost(err) {
		return err
	}
	if s.conn != nil {
		s.conn.Close()
	}
	s.tx, s.conn = nil, nil
	if cerr := s.connect(ctx); cerr != nil {
		return fmt.Errorf("%w; reconnect failed: %v", err, cerr)
	}
	return fmt.Errorf("%w (%v)", ErrSessionReset, err)
}

func (s *Session) closeCursor() {
	s.mu.Lock()
	cur := s.cur
	s.cur = nil
	s.mu.Unlock()
	if cur != nil {
		cur.Close()
	}
}

// txVerb maps client transaction statements to session calls.
func txVerb(stmt string, o *sqlx.Options) string {
	w := sqlx.Words(stmt, o, 4)
	switch len(w) {
	case 1:
		switch w[0] {
		case "BEGIN", "COMMIT", "ROLLBACK":
			return w[0]
		case "END":
			return "COMMIT"
		}
	case 2:
		switch {
		case (w[0] == "BEGIN" || w[0] == "START") && (w[1] == "TRANSACTION" || w[1] == "WORK" || w[1] == "TRAN"):
			return "BEGIN"
		case (w[0] == "COMMIT" || w[0] == "ROLLBACK") && (w[1] == "TRANSACTION" || w[1] == "WORK" || w[1] == "TRAN"):
			return w[0]
		}
	}
	return ""
}

// Execute runs one statement.
func (s *Session) Execute(ctx context.Context, stmt string, args ...any) (db.Result, error) {
	s.closeCursor()
	if s.conn == nil {
		if err := s.connect(ctx); err != nil {
			return nil, err
		}
	}
	start := time.Now()
	if len(args) == 0 {
		switch txVerb(stmt, s.c.D.Lex()) {
		case "BEGIN":
			return &Result{exec: true, affected: -1, elapsed: time.Since(start)}, s.Begin(ctx)
		case "COMMIT":
			return &Result{exec: true, affected: -1, elapsed: time.Since(start)}, s.Commit(ctx)
		case "ROLLBACK":
			return &Result{exec: true, affected: -1, elapsed: time.Since(start)}, s.Rollback(ctx)
		}
	}
	rctx, cancel := context.WithCancel(context.Background())
	// caller cancellation goes through Cancel: cancelling the statement's
	// context directly would make several drivers drop the connection
	stop := context.AfterFunc(ctx, s.Cancel)
	runID := ""
	if f := s.c.H.StatementContext; f != nil {
		rctx, runID = f(rctx)
	}
	s.mu.Lock()
	s.cancel, s.running, s.runID = cancel, true, runID
	s.mu.Unlock()
	done := func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}

	rows := sqlx.ReturnsRows(stmt, s.c.D.Lex())
	if s.c.H.ReturnsRows != nil {
		rows = s.c.H.ReturnsRows(stmt)
	}
	if !rows {
		res, err := s.q().ExecContext(rctx, stmt, args...)
		done()
		stop()
		cancel()
		if err != nil {
			return nil, s.c.mapErr(s.recover(ctx, err))
		}
		n, err := res.RowsAffected()
		if err != nil {
			n = -1
		}
		return &Result{exec: true, affected: n, elapsed: time.Since(start)}, nil
	}
	r, err := s.q().QueryContext(rctx, stmt, args...)
	done()
	if err != nil {
		stop()
		cancel()
		return nil, s.c.mapErr(s.recover(ctx, err))
	}
	res := &Result{s: s, rows: r, cancel: func() { stop(); cancel() }, affected: -1, elapsed: time.Since(start)}
	if err := res.loadColumns(); err != nil {
		res.Close()
		return nil, s.c.mapErr(err)
	}
	s.mu.Lock()
	s.cur = res
	s.mu.Unlock()
	return res, nil
}

// Cancel aborts the running statement or fetch.
func (s *Session) Cancel() {
	s.mu.Lock()
	cancel, running, id := s.cancel, s.running, s.id
	if s.runID != "" {
		id = s.runID
	}
	s.mu.Unlock()
	if running && s.c.H.Cancel != nil && id != "" {
		ctx, c := context.WithTimeout(context.Background(), 10*time.Second)
		handled := s.c.H.Cancel(ctx, s.c.DB, id)
		c()
		if handled {
			return
		}
	}
	if cancel != nil {
		cancel()
	}
}

func (s *Session) Begin(ctx context.Context) error {
	if s.c.H.NoTx {
		return db.ErrNotSupported
	}
	if s.tx != nil {
		return fmt.Errorf("a transaction is already open")
	}
	s.closeCursor()
	if s.conn == nil {
		if err := s.connect(ctx); err != nil {
			return err
		}
	}
	// the transaction lives until commit/rollback: not bound to ctx
	opts := s.c.H.TxOptions
	if s.readOnly {
		o := sql.TxOptions{ReadOnly: true}
		if opts != nil {
			o.Isolation = opts.Isolation
		}
		opts = &o
	}
	tx, err := s.conn.BeginTx(context.Background(), opts)
	if err != nil && s.readOnly && opts != nil {
		// some drivers reject read-only transactions; the session itself
		// is read-only already where supported
		tx, err = s.conn.BeginTx(context.Background(), s.c.H.TxOptions)
	}
	if err != nil {
		return s.c.mapErr(s.recover(ctx, err))
	}
	s.tx = tx
	return nil
}

func (s *Session) Commit(ctx context.Context) error {
	if s.tx == nil {
		return nil
	}
	s.closeCursor()
	err := s.tx.Commit()
	s.tx = nil
	return s.c.mapErr(s.recover(ctx, err))
}

func (s *Session) Rollback(ctx context.Context) error {
	if s.tx == nil {
		return nil
	}
	s.closeCursor()
	err := s.tx.Rollback()
	s.tx = nil
	if errors.Is(err, sql.ErrTxDone) {
		err = nil
	}
	return s.c.mapErr(s.recover(ctx, err))
}

func (s *Session) TxActive() bool { return s.tx != nil }

func (s *Session) SetReadOnly(ctx context.Context, on bool) error {
	s.readOnly = on
	if s.c.H.SetReadOnly == nil {
		return db.ErrNotSupported
	}
	if s.conn == nil {
		return nil
	}
	return s.c.mapErr(s.c.H.SetReadOnly(ctx, s.q(), on))
}

func (s *Session) CurrentSchema(ctx context.Context) (string, error) {
	if s.c.H.CurrentSchema == nil {
		return "", db.ErrNotSupported
	}
	if s.conn == nil {
		if err := s.connect(ctx); err != nil {
			return "", err
		}
	}
	v, err := s.c.H.CurrentSchema(ctx, s.q())
	return v, s.c.mapErr(err)
}

func (s *Session) SetCurrentSchema(ctx context.Context, schema string) error {
	if s.c.H.SetCurrentSchema == nil {
		return db.ErrNotSupported
	}
	s.closeCursor()
	if s.conn == nil {
		if err := s.connect(ctx); err != nil {
			return err
		}
	}
	return s.c.mapErr(s.c.H.SetCurrentSchema(ctx, s.q(), schema))
}

func (s *Session) Close() error {
	s.closeCursor()
	if s.tx != nil {
		_ = s.tx.Rollback()
		s.tx = nil
	}
	if s.conn == nil {
		return nil
	}
	err := s.conn.Close()
	s.conn = nil
	return err
}

// Result streams the rows of a query.
type Result struct {
	s        *Session
	rows     *sql.Rows
	cancel   func()
	cols     []db.Column
	exec     bool
	affected int64
	elapsed  time.Duration
	done     bool
	closed   bool
	mu       sync.Mutex
}

var _ db.Result = (*Result)(nil)

// Elapsed is the time until the first row was available.
func (r *Result) Elapsed() time.Duration { return r.elapsed }

func (r *Result) loadColumns() error {
	cts, err := r.rows.ColumnTypes()
	if err != nil {
		return err
	}
	r.cols = make([]db.Column, len(cts))
	for i, ct := range cts {
		r.cols[i] = ColumnOf(ct, r.s.c.H.Kind)
		if f := r.s.c.H.Column; f != nil {
			f(&r.cols[i])
		}
	}
	r.done = len(r.cols) == 0
	return nil
}

// ColumnOf converts a database/sql column type.
func ColumnOf(ct *sql.ColumnType, kind func(string) db.Kind) db.Column {
	c := db.Column{Name: ct.Name(), DBType: strings.ToUpper(ct.DatabaseTypeName()), Nullable: true}
	if n, ok := ct.Nullable(); ok {
		c.Nullable = n
	}
	if l, ok := ct.Length(); ok && l > 0 && l < 1<<40 {
		c.Length = l
	}
	if p, s, ok := ct.DecimalSize(); ok {
		c.Prec, c.Scale = p, s
	}
	if kind != nil {
		c.Kind = kind(c.DBType)
	}
	if c.Kind == db.KindOther {
		c.Kind = KindOfScanType(ct.ScanType())
	}
	return c
}

// KindOfScanType guesses a kind from a driver scan type.
func KindOfScanType(t reflect.Type) db.Kind {
	if t == nil {
		return db.KindOther
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return db.KindInt
	case reflect.Float32, reflect.Float64:
		return db.KindFloat
	case reflect.Bool:
		return db.KindBool
	case reflect.String:
		return db.KindText
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return db.KindBytes
		}
		return db.KindArray
	}
	switch t.String() {
	case "time.Time":
		return db.KindTimestamp
	case "sql.NullString":
		return db.KindText
	case "sql.NullInt64", "sql.NullInt32", "sql.NullInt16", "sql.NullByte":
		return db.KindInt
	case "sql.NullFloat64":
		return db.KindFloat
	case "sql.NullBool":
		return db.KindBool
	case "sql.NullTime":
		return db.KindTimestamp
	}
	return db.KindOther
}

func (r *Result) Columns() []db.Column {
	if r.exec {
		return nil
	}
	return r.cols
}

func (r *Result) RowsAffected() int64 { return r.affected }

func (r *Result) Messages() []string { return nil }

// Fetch reads up to n rows.
func (r *Result) Fetch(ctx context.Context, n int) ([]db.Row, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.exec || r.done || r.closed {
		return nil, false, nil
	}
	s := r.s
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()
	conv := s.c.H.Convert
	ncol := len(r.cols)
	vals := make([]any, ncol)
	ptrs := make([]any, ncol)
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	out := make([]db.Row, 0, min(n, 4096))
	for len(out) < n {
		if err := ctx.Err(); err != nil {
			return out, true, err
		}
		if !r.rows.Next() {
			r.done = true
			if err := r.rows.Err(); err != nil {
				return out, false, s.c.mapErr(err)
			}
			return out, false, nil
		}
		if err := r.rows.Scan(ptrs...); err != nil {
			return out, true, s.c.mapErr(err)
		}
		row := make(db.Row, ncol)
		for i, v := range vals {
			if conv != nil {
				v = conv(&r.cols[i], v)
			}
			row[i] = db.Normalize(v)
			vals[i] = nil
		}
		out = append(out, row)
	}
	return out, true, nil
}

func (r *Result) NextResultSet(ctx context.Context) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.exec || r.closed {
		return false, nil
	}
	if !r.rows.NextResultSet() {
		return false, s(r).mapErr(r.rows.Err())
	}
	if err := r.loadColumns(); err != nil {
		return false, s(r).mapErr(err)
	}
	return true, nil
}

func s(r *Result) *Conn { return r.s.c }

func (r *Result) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()
	var err error
	if r.rows != nil {
		err = r.rows.Close()
	}
	if r.cancel != nil {
		r.cancel()
	}
	if r.s != nil {
		r.s.mu.Lock()
		if r.s.cur == r {
			r.s.cur = nil
		}
		r.s.mu.Unlock()
	}
	return err
}
