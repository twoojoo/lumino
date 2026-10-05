// Package cassandra is the Apache Cassandra / ScyllaDB adapter (gocql).
// CQL has no database/sql driver: the Conn, Session and Result interfaces
// are implemented on gocql directly, with server-side paging.
package cassandra

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
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

// BuildChange binds values except decimals, UUIDs and big integers, which
// arrive as validated text and are inlined as CQL literals.
func (d *dialect) BuildChange(ref db.ObjectRef, chg db.Change, literal bool) (string, []any) {
	inline := func(vals []db.ColValue) []db.ColValue {
		out := make([]db.ColValue, len(vals))
		for i, cv := range vals {
			if s, ok := cv.Value.(string); ok && (cv.Kind == db.KindDecimal || cv.Kind == db.KindUUID || cv.Kind == db.KindInt) {
				cv.Value = db.Raw(s)
			}
			out[i] = cv
		}
		return out
	}
	chg.Values, chg.Key = inline(chg.Values), inline(chg.Key)
	q, args := db.StandardChange(d, ref, chg, literal)
	return q, args
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
		cl.SslOpts = &gocql.SslOptions{Config: tc, EnableHostVerification: cfg.TLS.Mode == "verify-full"}
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
	w := sqlx.Words(stmt, &lexOptions, 2)
	if len(w) == 2 && w[0] == "USE" {
		ks := strings.Trim(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(stmt[strings.Index(strings.ToUpper(stmt), "USE")+3:]), ";")), `"`)
		return &result{affected: -1, done: true}, s.SetCurrentSchema(ctx, ks)
	}
	rctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
	gs, err := s.c.sessionFor(s.keyspace)
	if err != nil {
		cancel()
		return nil, err
	}
	q := gs.Query(stmt, args...).WithContext(rctx).PageSize(1000)
	if !sqlx.ReturnsRows(stmt, &lexOptions) {
		err := q.Exec()
		cancel()
		if err != nil {
			return nil, mapError(err)
		}
		return &result{affected: -1, done: true}, nil
	}
	it := q.Iter()
	r := &result{it: it, cancel: cancel, affected: -1}
	for _, ci := range it.Columns() {
		r.cols = append(r.cols, db.Column{Name: ci.Name, DBType: strings.ToUpper(fmt.Sprint(ci.TypeInfo)), Kind: kindOf(ci.TypeInfo.Type()), Nullable: true})
		r.types = append(r.types, ci.TypeInfo)
	}
	if len(r.cols) == 0 {
		// statement returned no rows metadata: check for an error now
		if err := it.Close(); err != nil {
			cancel()
			return nil, mapError(err)
		}
		r.done = true
	}
	s.mu.Lock()
	s.cur = r
	s.mu.Unlock()
	return r, nil
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
	// **T destinations: gocql leaves the *T nil for NULL cells
	dest := make([]any, len(r.types))
	for i, ti := range r.types {
		dest[i] = reflect.New(reflect.PointerTo(reflect.TypeOf(ti.Zero()))).Interface()
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
			return out, false, mapError(err)
		}
		row := make(db.Row, len(dest))
		for i, p := range dest {
			ptr := reflect.ValueOf(p).Elem() // *T
			if ptr.IsNil() {
				row[i] = nil
			} else {
				row[i] = value(r.types[i], ptr.Elem().Interface())
			}
			ptr.Set(reflect.Zero(ptr.Type()))
		}
		out = append(out, row)
	}
	return out, true, nil
}

// value converts a scanned CQL value (zero values of absent cells become
// NULL only for pointer-like types: gocql scans NULL as the zero value).
func value(ti gocql.TypeInfo, v any) any {
	switch x := v.(type) {
	case time.Time:
		return x
	case time.Duration: // time of day
		if ti.Type() == gocql.TypeTime {
			h := x / time.Hour
			m := x % time.Hour / time.Minute
			sec := x % time.Minute / time.Second
			ns := x % time.Second
			s := fmt.Sprintf("%02d:%02d:%02d", h, m, sec)
			if ns != 0 {
				s += strings.TrimRight(fmt.Sprintf(".%09d", ns), "0")
			}
			return s
		}
		return x.String()
	case gocql.UUID:
		return x.String()
	case []byte:
		return append([]byte{}, x...)
	case gocql.Duration:
		return fmt.Sprintf("%dmo%dd%dns", x.Months, x.Days, x.Nanoseconds)
	}
	return db.Normalize(v)
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
