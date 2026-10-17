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
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/db/sqlx"
)

// Hooks customize the generic implementation.
type Hooks struct {
	// KeepConnOnClose: Session.Close returns the connection to the pool
	// (resetting read-only mode) instead of discarding it, for databases
	// living in the connection (SQLite :memory:).
	KeepConnOnClose bool
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
	// Notices returns a function draining the server notices of a session
	// connection (RAISE NOTICE, PRINT…), registered when the session opens.
	Notices func(c *sql.Conn) func() []string
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
	// Lang overrides the language (nil = db.SQLLanguage over D).
	Lang db.Language

	closeOnce sync.Once
	langOnce  sync.Once
}

var _ db.Conn = (*Conn)(nil)

func (c *Conn) Info() db.Info         { return c.Inf }
func (c *Conn) Caps() db.Capabilities { return c.Cap }
func (c *Conn) Dialect() db.Dialect   { return c.D }

func (c *Conn) Language() db.Language {
	c.langOnce.Do(func() {
		if c.Lang == nil {
			c.Lang = db.NewSQLLanguage(c.D)
		}
	})
	return c.Lang
}

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
	rawTx    bool // a transaction opened by a raw BEGIN variant (BEGIN IMMEDIATE…)
	id       string
	readOnly bool

	notices func() []string

	// op serializes the session operations with Close (which may run on
	// another goroutine: closing a tab while a statement runs)
	op     sync.Mutex
	closed bool // under mu

	mu      sync.Mutex
	cancel  context.CancelFunc // of the running statement / open cursor
	running bool
	runID   string // StatementContext id of the current statement
	seq     int    // statement number (cancellation hooks of old statements are ignored)
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
		id, _ := h(ctx, conn)
		s.mu.Lock()
		s.id = id
		s.mu.Unlock()
	}
	if s.readOnly && s.c.H.SetReadOnly != nil {
		_ = s.c.H.SetReadOnly(ctx, conn, true)
	}
	if h := s.c.H.Notices; h != nil {
		s.notices = h(conn)
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
	s.tx, s.conn, s.rawTx = nil, nil, false
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

// txVerb maps client transaction statements to session calls: BEGIN,
// COMMIT, ROLLBACK, or RAWBEGIN for a BEGIN variant database/sql can't
// express (isolation levels, BEGIN IMMEDIATE, named transactions), which
// runs as is and is tracked by the session.
func txVerb(stmt string, o *sqlx.Options) string {
	w := sqlx.Words(stmt, o, 8)
	if len(w) == 0 {
		return ""
	}
	txWord := func(x string) bool { return x == "TRANSACTION" || x == "WORK" || x == "TRAN" }
	switch w[0] {
	case "BEGIN", "START":
		if len(w) == 1 {
			if w[0] == "BEGIN" {
				return "BEGIN"
			}
			return ""
		}
		if len(w) == 2 && txWord(w[1]) {
			return "BEGIN"
		}
		switch w[1] {
		case "TRANSACTION", "WORK", "TRAN", "DEFERRED", "IMMEDIATE", "EXCLUSIVE", "ISOLATION", "READ", "NOT", "DISTRIBUTED":
			return "RAWBEGIN"
		}
	case "COMMIT", "ROLLBACK", "END", "ABORT":
		for _, x := range w[1:] {
			if x == "TO" || x == "PREPARED" || x == "CHAIN" {
				return "" // ROLLBACK TO SAVEPOINT, two-phase commit, AND CHAIN
			}
		}
		if len(w) > 2 || len(w) == 2 && !txWord(w[1]) {
			return "" // named transactions (T-SQL ROLLBACK TRANSACTION sp) run as is
		}
		switch w[0] {
		case "END":
			return "COMMIT"
		case "ABORT":
			return "ROLLBACK"
		}
		return w[0]
	}
	return ""
}

// Execute runs one statement.
func (s *Session) Execute(ctx context.Context, stmt string, args ...any) (_ db.Result, err error) {
	if err := s.enter(); err != nil {
		return nil, err
	}
	defer s.op.Unlock()
	s.closeCursor()
	if s.conn == nil {
		if err := s.connect(ctx); err != nil {
			return nil, err
		}
	}
	start := time.Now()
	if len(args) == 0 {
		ok := &Result{exec: true, affected: -1}
		conn0 := s.conn
		// the raw transaction state changes only when the statement ran on
		// the same connection without error
		ranOK := func() bool { return err == nil && s.conn != nil && s.conn == conn0 }
		switch verb := txVerb(stmt, s.c.D.Lex()); verb {
		case "BEGIN":
			return ok, s.begin(ctx)
		case "COMMIT", "ROLLBACK":
			if s.tx != nil || !s.rawTx && !s.c.H.NoTx {
				if verb == "COMMIT" {
					return ok, s.commit(ctx)
				}
				return ok, s.rollback(ctx)
			}
			defer func() {
				if ranOK() || err != nil && s.conn != conn0 {
					s.rawTx = false
				}
			}()
		case "RAWBEGIN":
			if s.tx != nil || s.rawTx {
				return nil, fmt.Errorf("a transaction is already open")
			}
			if !s.c.H.NoTx {
				defer func() {
					if ranOK() {
						s.rawTx = true
					}
				}()
			}
		case "":
			// named T-SQL transactions (COMMIT TRAN t1, ROLLBACK TRANSACTION
			// t1) and COMMIT AND NO CHAIN end a raw transaction too
			if w := sqlx.Words(stmt, s.c.D.Lex(), 8); s.rawTx && len(w) > 0 && (w[0] == "COMMIT" || w[0] == "ROLLBACK") && !slices.Contains(w, "TO") {
				defer func() {
					if ranOK() {
						s.rawTx = false
					}
				}()
			}
		}
	}
	rctx, cancel := context.WithCancel(context.Background())
	runID := ""
	if f := s.c.H.StatementContext; f != nil {
		rctx, runID = f(rctx)
	}
	s.mu.Lock()
	s.seq++
	seq := s.seq
	s.cancel, s.running, s.runID = cancel, true, runID
	s.mu.Unlock()
	// caller cancellation goes through Cancel: cancelling the statement's
	// context directly would make several drivers drop the connection.
	// Registered once the statement is marked running, so an already
	// expired context cancels it too.
	stop := context.AfterFunc(ctx, func() {
		// only while this statement or one of its fetches runs: an open
		// cursor outlives the context of the Execute call
		s.mu.Lock()
		running := s.running && s.seq == seq
		s.mu.Unlock()
		if running {
			s.Cancel()
		}
	})
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
		return &Result{s: s, exec: true, affected: n, elapsed: time.Since(start)}, nil
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

func (s *Session) begin(ctx context.Context) error {
	if s.c.H.NoTx {
		return db.ErrNotSupported
	}
	if s.tx != nil || s.rawTx {
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

func (s *Session) commit(ctx context.Context) error {
	if s.tx == nil {
		if s.rawTx {
			return s.rawEnd(ctx, "COMMIT")
		}
		return nil
	}
	s.closeCursor()
	err := s.tx.Commit()
	s.tx = nil
	return s.c.mapErr(s.recover(ctx, err))
}

func (s *Session) rollback(ctx context.Context) error {
	if s.tx == nil {
		if s.rawTx {
			return s.rawEnd(ctx, "ROLLBACK")
		}
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

// rawEnd ends a transaction opened by a raw BEGIN variant.
func (s *Session) rawEnd(ctx context.Context, verb string) error {
	s.closeCursor()
	s.rawTx = false
	if s.conn == nil {
		return nil
	}
	_, err := s.conn.ExecContext(ctx, verb)
	return s.c.mapErr(s.recover(ctx, err))
}

// ErrSessionClosed is returned by operations on a closed session (it never
// reconnects silently: a half-applied batch must not continue in autocommit).
var ErrSessionClosed = errors.New("the session is closed")

// enter takes the operation lock of an open session.
func (s *Session) enter() error {
	s.op.Lock()
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		s.op.Unlock()
		return ErrSessionClosed
	}
	return nil
}

func (s *Session) Begin(ctx context.Context) error {
	if err := s.enter(); err != nil {
		return err
	}
	defer s.op.Unlock()
	return s.begin(ctx)
}

func (s *Session) Commit(ctx context.Context) error {
	if err := s.enter(); err != nil {
		return err
	}
	defer s.op.Unlock()
	return s.commit(ctx)
}

func (s *Session) Rollback(ctx context.Context) error {
	if err := s.enter(); err != nil {
		return err
	}
	defer s.op.Unlock()
	return s.rollback(ctx)
}

func (s *Session) TxActive() bool { return s.tx != nil || s.rawTx }

func (s *Session) SetReadOnly(ctx context.Context, on bool) error {
	if err := s.enter(); err != nil {
		return err
	}
	defer s.op.Unlock()
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
	if err := s.enter(); err != nil {
		return "", err
	}
	defer s.op.Unlock()
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
	if err := s.enter(); err != nil {
		return err
	}
	defer s.op.Unlock()
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

// Close ends the session. Its connection is discarded rather than returned
// to the pool: session state (read-only mode, search_path, USE, SET ROLE,
// temporary tables, a raw transaction) must not leak into other sessions
// or metadata queries.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	running := s.running
	s.mu.Unlock()
	if running {
		s.Cancel() // a statement on another goroutine: stop it first
	}
	s.op.Lock()
	defer s.op.Unlock()
	s.closeCursor()
	if s.tx != nil {
		_ = s.tx.Rollback()
		s.tx = nil
	}
	if s.conn == nil {
		return nil
	}
	if s.rawTx {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = s.conn.ExecContext(ctx, "ROLLBACK")
		cancel()
		s.rawTx = false
	}
	if s.c.H.KeepConnOnClose {
		// the pool is the database (SQLite :memory:): reset instead
		if s.readOnly && s.c.H.SetReadOnly != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = s.c.H.SetReadOnly(ctx, s.conn, false)
			cancel()
		}
	} else {
		_ = s.conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	err := s.conn.Close()
	s.conn = nil
	if errors.Is(err, sql.ErrConnDone) {
		err = nil
	}
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
	msgs     []string
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
	// zero columns can still mean rows (PostgreSQL: SELECT FROM t): they
	// are counted, the cursor ends by itself otherwise
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

// Messages drains the server notices received so far.
func (r *Result) Messages() []string {
	if r.s == nil || r.s.notices == nil {
		return r.msgs
	}
	return append(r.msgs, r.s.notices()...)
}

// Fetch reads up to n rows. A panic inside the driver (seen with some
// ClickHouse types) becomes an error instead of crashing the app.
func (r *Result) Fetch(ctx context.Context, n int) (rows []db.Row, more bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	defer func() {
		if p := recover(); p != nil {
			r.done = true
			rows, more, err = nil, false, fmt.Errorf("the driver failed reading this result: %v", p)
		}
	}()
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
	done := r.done || r.exec
	r.mu.Unlock()
	var err error
	if r.rows != nil {
		if !done && r.s != nil && r.s.c.H.Cancel != nil && !r.s.TxActive() {
			// closing a partly read cursor makes drivers read the rest of
			// the result: stop the statement on the server first. Never
			// inside a transaction (PostgreSQL aborts it on cancel), and
			// only through the server hook (cancelling the context drops
			// the connection of several drivers).
			r.s.mu.Lock()
			id := r.s.id
			if r.s.runID != "" {
				id = r.s.runID
			}
			r.s.mu.Unlock()
			if id != "" {
				ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
				r.s.c.H.Cancel(ctx, r.s.c.DB, id)
				c()
			}
		}
		func() {
			defer func() {
				if p := recover(); p != nil { // broken driver rows (see Fetch)
					err = fmt.Errorf("closing the result: %v", p)
				}
			}()
			err = r.rows.Close()
		}()
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
