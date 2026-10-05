// Package cassandra is the Apache Cassandra / ScyllaDB adapter (gocql).
// CQL has no database/sql driver: the Conn, Session and Result interfaces
// are implemented on gocql directly, with server-side paging.
package cassandra

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	gocql "github.com/apache/cassandra-gocql-driver/v2"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/db/sqlx"
)

func init() { db.Register(Driver{}) }

// Driver opens Cassandra/ScyllaDB connections.
type Driver struct{}

var caps = db.Capabilities{
	Schemas: true, Cancel: true, Views: true, MatViews: true, Functions: true,
	Insert: true, Update: true, Delete: true, TLS: true,
}

func (Driver) Info() db.Info {
	return db.Info{ID: "cassandra", Name: "Cassandra / ScyllaDB", DefaultPort: 9042, DefaultUser: "cassandra",
		DatabaseLabel: "Keyspace", Fields: []db.Field{db.FieldHost, db.FieldPort, db.FieldDatabase, db.FieldUser, db.FieldPassword},
		Caps: caps, Schemes: []string{"cassandra", "cql", "scylla", "ca", "scy"}, DialectName: "cql"}
}

var reserved = db.WordSet(`
ADD AGGREGATE ALL ALLOW ALTER AND ANY APPLY AS ASC AUTHORIZE BATCH BEGIN BY COLUMNFAMILY CREATE DELETE DESC DESCRIBE
DROP ENTRIES EXECUTE FROM FULL GRANT IF IN INDEX INFINITY INSERT INTO KEYSPACE LIMIT MODIFY NAN NORECURSIVE NOT NULL
OF ON OR ORDER PRIMARY RENAME REPLACE REVOKE SCHEMA SELECT SET TABLE TO TOKEN TRUNCATE UNLOGGED UPDATE USE USING
VIEW WHERE WITH
`)

var lexOptions = sqlx.Options{
	DollarQuotes: true, SlashComments: true,
	BlockStart: func(w []string) bool {
		// BEGIN [UNLOGGED|COUNTER] BATCH … APPLY BATCH
		return w[0] == "BEGIN"
	},
	ApplyEndsBlock: true,
}

// dialect inlines values gocql can't bind from text.
type dialect struct{ db.BaseDialect }

// BuildChange validates every edited value for its column kind: values
// gocql can't bind from grid text (decimals, UUIDs, big integers, floats,
// which may target float or double) are inlined as CQL literals built only
// from validated text; invalid ones make Execute fail with a clear error.
func (d *dialect) BuildChange(ref db.ObjectRef, chg db.Change, literal bool) (string, []any) {
	prep := func(vals []db.ColValue) []db.ColValue {
		out := make([]db.ColValue, len(vals))
		for i, cv := range vals {
			cv.Value = prepare(cv)
			out[i] = cv
		}
		return out
	}
	chg.Values, chg.Key = prep(chg.Values), prep(chg.Key)
	q, args := db.StandardChange(d, ref, chg, literal)
	return q, args
}

// Literal renders CQL literals: bare UUIDs, NaN/Infinity, durations.
func (d *dialect) Literal(v any, k db.Kind) string {
	if s, ok := literal(&d.BaseDialect, v, k); ok {
		return s
	}
	return d.BaseDialect.Literal(v, k)
}

func (d *dialect) Placeholder(i int) string { return "?" }

// NewDialect builds the CQL dialect.
func NewDialect() *dialect {
	d := &db.BaseDialect{
		ID: "cql", QuoteL: `"`, QuoteR: `"`, Fold: db.FoldLower, Params: db.ParamQuestion, LexOpts: lexOptions,
		Reserved: reserved, KeywordSet: db.Merge(reserved, db.WordSet(`KEYSPACES TABLES CLUSTERING COMPACT STORAGE
TTL WRITETIME TIMESTAMP EXISTS STATIC FILTERING DISTINCT JSON CONTAINS KEY MATERIALIZED REPLICATION DURABLE_WRITES
TYPE FUNCTION RETURNS CALLED INPUT LANGUAGE COUNTER LIST MAP FROZEN TUPLE PER PARTITION GROUP`)),
		FuncList: strings.Fields("token now uuid toDate toTimestamp toUnixTimestamp minTimeuuid maxTimeuuid writetime ttl count min max sum avg cast blobAsText textAsBlob currentTimestamp currentDate"),
		TypeList: strings.Fields("ascii bigint blob boolean counter date decimal double duration float inet int smallint text time timestamp timeuuid tinyint uuid varchar varint list set map tuple frozen"),
		BytesLit: func(b []byte) string { return "0x" + hex.EncodeToString(b) },
	}
	d.TimeLit = func(t time.Time, k db.Kind) string {
		if k == db.KindDate {
			return "'" + t.Format("2006-01-02") + "'"
		}
		return "'" + t.UTC().Format("2006-01-02T15:04:05.000Z") + "'"
	}
	return &dialect{*d}
}

// kindOf maps CQL types.
func kindOf(t gocql.Type) db.Kind {
	switch t {
	case gocql.TypeInt, gocql.TypeBigInt, gocql.TypeSmallInt, gocql.TypeTinyInt, gocql.TypeCounter, gocql.TypeVarint:
		return db.KindInt
	case gocql.TypeFloat, gocql.TypeDouble:
		return db.KindFloat
	case gocql.TypeDecimal:
		return db.KindDecimal
	case gocql.TypeBoolean:
		return db.KindBool
	case gocql.TypeAscii, gocql.TypeText, gocql.TypeVarchar, gocql.TypeInet:
		return db.KindText
	case gocql.TypeTimestamp:
		return db.KindTimestampTZ
	case gocql.TypeDate:
		return db.KindDate
	case gocql.TypeTime:
		return db.KindTime
	case gocql.TypeUUID, gocql.TypeTimeUUID:
		return db.KindUUID
	case gocql.TypeBlob:
		return db.KindBytes
	case gocql.TypeList, gocql.TypeSet, gocql.TypeMap, gocql.TypeTuple, gocql.TypeUDT:
		return db.KindArray
	case gocql.TypeDuration:
		return db.KindInterval
	}
	return db.KindOther
}

// kindOfName maps type names from system_schema.
func kindOfName(t string) db.Kind {
	l := strings.ToLower(strings.TrimSpace(t))
	if i := strings.IndexByte(l, '<'); i > 0 {
		if strings.HasPrefix(l, "frozen<") {
			return kindOfName(l[7 : len(l)-1])
		}
		return db.KindArray
	}
	switch l {
	case "int", "bigint", "smallint", "tinyint", "counter", "varint":
		return db.KindInt
	case "float", "double":
		return db.KindFloat
	case "decimal":
		return db.KindDecimal
	case "boolean":
		return db.KindBool
	case "ascii", "text", "varchar", "inet":
		return db.KindText
	case "timestamp":
		return db.KindTimestampTZ
	case "date":
		return db.KindDate
	case "time":
		return db.KindTime
	case "uuid", "timeuuid":
		return db.KindUUID
	case "blob":
		return db.KindBytes
	case "duration":
		return db.KindInterval
	}
	return db.KindOther
}

func (d Driver) Open(ctx context.Context, cfg *db.Config) (db.Conn, error) {
	var hosts []string
	for _, h := range strings.Split(cfg.Host, ",") {
		if h = strings.TrimSpace(h); h != "" {
			hosts = append(hosts, h)
		}
	}
	if len(hosts) == 0 {
		hosts = []string{"127.0.0.1"}
	}
	cl := gocql.NewCluster(hosts...)
	if cfg.Port != 0 {
		cl.Port = cfg.Port
	}
	cl.Keyspace = cfg.Database
	cl.ConnectTimeout = 15 * time.Second
	cl.Timeout = 2 * time.Minute
	cl.Consistency = gocql.LocalQuorum
	if c := cfg.Param("consistency"); c != "" {
		if cons, err := gocql.ParseConsistencyWrapper(strings.ToUpper(c)); err == nil {
			cl.Consistency = cons
		}
	}
	// a tunnel or a single address: don't follow the node's own addresses
	if cfg.Param("discovery") != "true" {
		cl.DisableInitialHostLookup = true
		cl.HostFilter = gocql.WhiteListHostFilter(hosts...)
	}
	if cfg.User != "" {
		cl.Authenticator = gocql.PasswordAuthenticator{Username: cfg.User, Password: cfg.Password}
	}
	tc, err := cfg.TLS.TLS(hosts[0])
	if err != nil {
		return nil, err
	}
	if tc != nil {
		// gocql only knows the nodes' IP addresses: each node is verified
		// against the name it was configured with (unless ServerName is set)
		hd := &tlsDialer{tc: tc, names: map[string]string{}, timeout: cl.ConnectTimeout, fixed: cfg.TLS.ServerName != ""}
		if !hd.fixed {
			hd.resolve(ctx, hosts, cl.Port)
		}
		cl.HostDialer = hd
	}
	sess, err := cl.CreateSession()
	if err != nil {
		return nil, mapError(err)
	}
	var rel string
	_ = sess.Query("SELECT release_version FROM system.local").WithContext(ctx).Scan(&rel)
	ver := "Cassandra " + rel
	var scylla string
	if err := sess.Query("SELECT version FROM system.versions").WithContext(ctx).Scan(&scylla); err == nil && scylla != "" {
		ver = "ScyllaDB " + scylla
	}
	dia := NewDialect()
	return &conn{s: sess, cluster: cl, d: dia, ver: ver, keyspace: cfg.Database, init: cfg.InitSQL,
		cat: &catalog{s: sess, d: dia}, byKS: map[string]*gocql.Session{}}, nil
}

// tlsDialer dials nodes over TLS, verifying each against its own host name.
type tlsDialer struct {
	tc      *tls.Config
	names   map[string]string // ip:port and ip -> configured host name
	timeout time.Duration
	fixed   bool // ServerName set explicitly
}

func (d *tlsDialer) resolve(ctx context.Context, hosts []string, defPort int) {
	for _, h := range hosts {
		name, port := h, strconv.Itoa(defPort)
		if hh, pp, err := net.SplitHostPort(h); err == nil {
			name, port = hh, pp
		}
		if net.ParseIP(name) != nil {
			continue
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, name)
		if err != nil {
			continue
		}
		for _, ip := range ips {
			d.names[net.JoinHostPort(ip.IP.String(), port)] = name
			if _, ok := d.names[ip.IP.String()]; !ok {
				d.names[ip.IP.String()] = name
			}
		}
	}
}

func (d *tlsDialer) DialHost(ctx context.Context, host *gocql.HostInfo) (*gocql.DialedHost, error) {
	addr := host.ConnectAddressAndPort()
	c, err := (&net.Dialer{Timeout: d.timeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	tc := d.tc.Clone()
	if !d.fixed {
		ip := host.ConnectAddress().String()
		name, ok := d.names[addr]
		if !ok {
			name, ok = d.names[ip]
		}
		if !ok {
			name = ip // discovered node: its address
		}
		tc.ServerName = name
	}
	return gocql.WrapTLS(ctx, c, addr, tc)
}

type conn struct {
	s        *gocql.Session
	cluster  *gocql.ClusterConfig
	d        *dialect
	ver      string
	keyspace string
	init     string
	cat      *catalog

	// USE changes the keyspace of unqualified names: the protocol binds it
	// to the connection, so every keyspace gets its own driver session
	mu   sync.Mutex
	byKS map[string]*gocql.Session
}

// sessionFor returns the driver session whose default keyspace is ks.
func (c *conn) sessionFor(ks string) (*gocql.Session, error) {
	if ks == c.keyspace {
		return c.s, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if s := c.byKS[ks]; s != nil {
		return s, nil
	}
	cl := *c.cluster
	cl.Keyspace = ks
	s, err := cl.CreateSession()
	if err != nil {
		return nil, mapError(err)
	}
	c.byKS[ks] = s
	return s, nil
}

func (c *conn) Info() db.Info         { return Driver{}.Info() }
func (c *conn) Caps() db.Capabilities { return caps }
func (c *conn) Dialect() db.Dialect   { return c.d }
func (c *conn) ServerVersion() string { return c.ver }
func (c *conn) Catalog() db.Catalog   { return c.cat }
func (c *conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.byKS {
		s.Close()
	}
	c.byKS = nil
	c.s.Close()
	return nil
}
func (c *conn) Ping(ctx context.Context) error {
	return mapError(c.s.Query("SELECT now() FROM system.local").WithContext(ctx).Exec())
}

func (c *conn) Session(ctx context.Context) (db.Session, error) {
	s := &session{c: c, keyspace: c.keyspace}
	for _, st := range sqlx.Split(c.init, &lexOptions) {
		if res, err := s.Execute(ctx, st.Text); err != nil {
			return nil, fmt.Errorf("init CQL %q: %w", st.Text, err)
		} else {
			res.Close()
		}
	}
	return s, nil
}

// session: CQL sessions are pooled by the driver; the only session state
// kept is the current keyspace (USE), applied by qualifying nothing but
// remembering it for unqualified statements via the query keyspace.
type session struct {
	c        *conn
	keyspace string

	mu     sync.Mutex
	cancel context.CancelFunc
	cur    *result
}

func (s *session) Execute(ctx context.Context, stmt string, args ...any) (db.Result, error) {
	s.closeCursor()
	if ks, ok := useKeyspace(stmt); ok {
		if ks == "" {
			return nil, errors.New("USE: keyspace name expected")
		}
		return &result{affected: -1, done: true}, s.SetCurrentSchema(ctx, ks)
	}
	for _, a := range args {
		if b, ok := a.(badValue); ok {
			return nil, b
		}
	}
	// the iterator outlives ctx (an open cursor is scrolled after Execute
	// returns): caller cancellation is forwarded only while Execute or a
	// Fetch runs
	rctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	gs, err := s.c.sessionFor(s.keyspace)
	if err != nil {
		cancel()
		return nil, err
	}
	q := gs.Query(stmt, args...).WithContext(rctx).PageSize(1000)
	if !sqlx.ReturnsRows(stmt, &lexOptions) && !conditional(stmt) {
		err := q.Exec()
		cancel()
		if err != nil {
			return nil, ctxErr(ctx, mapError(err))
		}
		return &result{affected: -1, done: true}, nil
	}
	it := q.Iter()
	r := &result{it: it, cancel: cancel, affected: -1}
	for _, ci := range it.Columns() {
		r.cols = append(r.cols, db.Column{Name: ci.Name, DBType: typeName(ci.TypeInfo, false), Kind: kindOf(ci.TypeInfo.Type()), Nullable: true})
		r.types = append(r.types, ci.TypeInfo)
	}
	if len(r.cols) == 0 {
		// statement returned no rows metadata: check for an error now
		if err := it.Close(); err != nil {
			cancel()
			return nil, ctxErr(ctx, mapError(err))
		}
		r.done = true
	}
	s.mu.Lock()
	s.cur = r
	s.mu.Unlock()
	return r, nil
}

// ctxErr prefers the caller's cancellation over the driver's error.
func ctxErr(ctx context.Context, err error) error {
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// useKeyspace parses USE ks: unquoted names fold to lower case, quoted
// ones keep their case.
func useKeyspace(stmt string) (string, bool) {
	toks := sqlx.Tokens(stmt, &lexOptions)
	seen := false
	for _, t := range toks {
		if t.Kind == sqlx.TSpace || t.Kind == sqlx.TComment {
			continue
		}
		txt := stmt[t.Start:t.End]
		if !seen {
			if t.Kind != sqlx.TWord || !strings.EqualFold(txt, "USE") {
				return "", false
			}
			seen = true
			continue
		}
		switch t.Kind {
		case sqlx.TWord:
			return strings.ToLower(txt), true
		case sqlx.TQuotedIdent:
			if len(txt) >= 2 && txt[0] == '"' && txt[len(txt)-1] == '"' {
				return strings.ReplaceAll(txt[1:len(txt)-1], `""`, `"`), true
			}
		}
		return "", true
	}
	return "", seen
}

// conditional reports a lightweight transaction (INSERT … IF NOT EXISTS,
// UPDATE/DELETE … IF cond, batches of them): the server answers with an
// [applied] row the user must see.
func conditional(stmt string) bool {
	w := sqlx.Words(stmt, &lexOptions, 1)
	if len(w) == 0 {
		return false
	}
	switch w[0] {
	case "INSERT", "UPDATE", "DELETE", "BEGIN":
	default:
		return false
	}
	for _, t := range sqlx.Tokens(stmt, &lexOptions) {
		// IF is reserved: a column named if is a quoted identifier
		if t.Kind == sqlx.TWord && strings.EqualFold(stmt[t.Start:t.End], "IF") {
			return true
		}
	}
	return false
}

func (s *session) closeCursor() {
	s.mu.Lock()
	cur := s.cur
	s.cur = nil
	s.mu.Unlock()
	if cur != nil {
		cur.Close()
	}
}

func (s *session) Cancel() {
	s.mu.Lock()
	c := s.cancel
	s.mu.Unlock()
	if c != nil {
		c()
	}
}

func (s *session) Begin(context.Context) error    { return db.ErrNotSupported }
func (s *session) Commit(context.Context) error   { return nil }
func (s *session) Rollback(context.Context) error { return nil }
func (s *session) TxActive() bool                 { return false }
func (s *session) SetReadOnly(context.Context, bool) error {
	return db.ErrNotSupported
}

func (s *session) CurrentSchema(context.Context) (string, error) { return s.keyspace, nil }

func (s *session) SetCurrentSchema(ctx context.Context, ks string) error {
	var name string
	if err := s.c.s.Query("SELECT keyspace_name FROM system_schema.keyspaces WHERE keyspace_name = ?", ks).WithContext(ctx).Scan(&name); err != nil {
		return fmt.Errorf("keyspace %q: %w", ks, mapError(err))
	}
	s.keyspace = name
	return nil
}

func (s *session) Close() error {
	s.closeCursor()
	return nil
}

type result struct {
	it       *gocql.Iter
	cancel   context.CancelFunc
	cols     []db.Column
	types    []gocql.TypeInfo
	affected int64
	done     bool
	mu       sync.Mutex
}

func (r *result) Columns() []db.Column {
	if r.it == nil {
		return nil
	}
	return r.cols
}

func (r *result) RowsAffected() int64 { return r.affected }
func (r *result) Messages() []string  { return nil }

func (r *result) NextResultSet(context.Context) (bool, error) { return false, nil }

func (r *result) Fetch(ctx context.Context, n int) ([]db.Row, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done || r.it == nil {
		return nil, false, nil
	}
	if r.cancel != nil {
		stop := context.AfterFunc(ctx, r.cancel)
		defer stop()
	}
	// top-level tuples are scanned into one destination per element
	var dest []any
	start := make([]int, len(r.types))
	for i, ti := range r.types {
		start[i] = len(dest)
		if tt, ok := ti.(gocql.TupleTypeInfo); ok && len(tt.Elems) > 0 {
			for _, e := range tt.Elems {
				dest = append(dest, scanDest(e))
			}
			continue
		}
		dest = append(dest, scanDest(ti))
	}
	var out []db.Row
	for len(out) < n {
		if err := ctx.Err(); err != nil {
			return out, true, err
		}
		if !r.it.Scan(dest...) {
			r.done = true
			err := r.it.Close()
			if r.cancel != nil {
				r.cancel()
			}
			return out, false, ctxErr(ctx, mapError(err))
		}
		row := make(db.Row, len(r.types))
		for i, ti := range r.types {
			if tt, ok := ti.(gocql.TupleTypeInfo); ok && len(tt.Elems) > 0 {
				el := make([]any, len(tt.Elems))
				null := true
				for j, e := range tt.Elems {
					if el[j] = takeScanned(e, dest[start[i]+j]); el[j] != nil {
						null = false
					}
				}
				if !null {
					row[i] = el
				}
				continue
			}
			row[i] = takeScanned(ti, dest[start[i]])
		}
		out = append(out, row)
	}
	return out, true, nil
}

func (r *result) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.it != nil && !r.done {
		r.done = true
		_ = r.it.Close()
	}
	if r.cancel != nil {
		r.cancel()
	}
	return nil
}

var lineColRx = regexp.MustCompile(`line (\d+):(\d+)`)

func mapError(err error) error {
	if err == nil {
		return nil
	}
	var re gocql.RequestError
	if errors.As(err, &re) {
		e := &db.Error{Msg: re.Message(), Code: fmt.Sprintf("0x%04x", re.Code()), Err: err}
		if m := lineColRx.FindStringSubmatch(re.Message()); m != nil {
			e.Line, _ = strconv.Atoi(m[1])
			col, _ := strconv.Atoi(m[2])
			if e.Line == 1 {
				e.Position = col + 1
			}
		}
		return e
	}
	return err
}
