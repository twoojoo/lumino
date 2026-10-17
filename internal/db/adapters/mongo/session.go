package mongo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/twoojoo/lumino/internal/db"
)

// prefetch is how many documents are read before the columns are fixed:
// fields first seen in later documents are not shown as columns.
const prefetch = 1000

// session keeps the current database, a transaction and the read-only
// flag. Statements run as server commands, so every option of find,
// aggregate, update… is passed through as written.
type session struct {
	c        *conn
	database string

	mu       sync.Mutex
	cancel   context.CancelFunc
	cur      *result
	ms       *mongo.Session
	readOnly bool
	guard    db.Guard
	js       *jsShell // runtime of dynamic statements (created on first use)
}

var _ db.Guarded = (*session)(nil)

// SetGuard sets the check of the operations of dynamic statements.
func (s *session) SetGuard(g db.Guard) {
	s.mu.Lock()
	s.guard = g
	s.mu.Unlock()
}

func (s *session) db(name string) *mongo.Database {
	if name == "" {
		name = s.database
	}
	return s.c.cl.Database(name)
}

func (s *session) closeCursor() {
	if s.cur != nil {
		s.cur.Close()
		s.cur = nil
	}
}

func (s *session) Execute(ctx context.Context, stmt string, args ...any) (db.Result, error) {
	s.closeCursor()
	if len(args) > 0 {
		return nil, errors.New("MongoDB statements take no bind parameters")
	}
	st, perr := parseStatement(strings.TrimSpace(stmt))
	if perr == nil {
		if k := classify(st); k.Modifies() && s.readOnly {
			return nil, fmt.Errorf("read-only session: %s statements are refused", k)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel = cancel
	ms := s.ms
	s.mu.Unlock()
	if ms != nil {
		ctx = mongo.NewSessionContext(ctx, ms)
	}
	var res *result
	var err error
	if perr == nil {
		res, err = s.run(ctx, st)
	} else {
		// variables, loops, functions…: JavaScript, each operation checked
		// while it runs
		res, err = s.shell().run(ctx, stmt)
	}
	if err != nil {
		cancel()
		return nil, mapError(err)
	}
	res.cancel = cancel
	s.cur = res
	return res, nil
}

func (s *session) Cancel() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	if s.js != nil {
		s.js.interrupt()
	}
}

func (s *session) Begin(ctx context.Context) error {
	if !s.c.caps.Transactions {
		return db.ErrNotSupported
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ms != nil {
		return errors.New("a transaction is already open")
	}
	ms, err := s.c.cl.StartSession()
	if err != nil {
		return mapError(err)
	}
	if err := ms.StartTransaction(); err != nil {
		ms.EndSession(ctx)
		return mapError(err)
	}
	s.ms = ms
	return nil
}

func (s *session) endTx(ctx context.Context, commit bool) error {
	s.closeCursor()
	s.mu.Lock()
	ms := s.ms
	s.ms = nil
	s.mu.Unlock()
	if ms == nil {
		return nil
	}
	defer ms.EndSession(context.Background())
	if commit {
		return mapError(ms.CommitTransaction(ctx))
	}
	return mapError(ms.AbortTransaction(ctx))
}

func (s *session) Commit(ctx context.Context) error   { return s.endTx(ctx, true) }
func (s *session) Rollback(ctx context.Context) error { return s.endTx(ctx, false) }

func (s *session) TxActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ms != nil
}

// SetReadOnly refuses statements that change anything (enforced here:
// every statement goes through the same parser as the classification).
func (s *session) SetReadOnly(_ context.Context, on bool) error {
	s.readOnly = on
	return nil
}

func (s *session) CurrentSchema(context.Context) (string, error) { return s.database, nil }

func (s *session) SetCurrentSchema(_ context.Context, schema string) error {
	if schema == "" {
		return errors.New("empty database name")
	}
	s.database = schema
	return nil
}

func (s *session) Close() error {
	s.closeCursor()
	return s.endTx(context.Background(), false)
}

// ---- results ----

// result streams documents (or reports a write).
type result struct {
	cols     []db.Column
	buf      []bson.D
	cur      *mongo.Cursor
	affected int64
	msgs     []string
	noRows   bool
	cancel   context.CancelFunc
	last     []bson.D // documents of the rows of the last Fetch
	info     bson.D   // result document of a write (insertedId, matchedCount…)
}

// next returns the next document (for scripts iterating a result).
func (r *result) next(ctx context.Context) (bson.D, bool, error) {
	if len(r.buf) > 0 {
		d := r.buf[0]
		r.buf = r.buf[1:]
		return d, true, nil
	}
	if r.cur == nil {
		return nil, false, nil
	}
	if !r.cur.Next(ctx) {
		err := r.cur.Err()
		r.closeCur()
		return nil, false, err
	}
	var d bson.D
	if err := bson.Unmarshal(r.cur.Current, &d); err != nil {
		return nil, false, err
	}
	return d, true, nil
}

// hasNext reports whether next would return a document (it reads the
// next batch when needed).
func (r *result) hasNext(ctx context.Context) (bool, error) {
	if len(r.buf) > 0 {
		return true, nil
	}
	d, ok, err := r.next(ctx)
	if ok {
		r.buf = append(r.buf, d)
	}
	return ok, err
}

var _ db.DocumentResult = (*result)(nil)

// Documents returns the whole documents of the last fetched rows.
func (r *result) Documents() []db.DocNode {
	out := make([]db.DocNode, len(r.last))
	for i, d := range r.last {
		out[i] = docNode("", d)
	}
	return out
}

func (r *result) Columns() []db.Column {
	if r.noRows {
		return nil
	}
	if r.cols == nil {
		return []db.Column{}
	}
	return r.cols
}

func (r *result) Fetch(ctx context.Context, n int) ([]db.Row, bool, error) {
	if r.noRows {
		return nil, false, nil
	}
	var rows []db.Row
	r.last = r.last[:0]
	for len(rows) < n && len(r.buf) > 0 {
		rows = append(rows, row(r.buf[0], r.cols))
		r.last = append(r.last, r.buf[0])
		r.buf = r.buf[1:]
	}
	for len(rows) < n && r.cur != nil {
		if !r.cur.Next(ctx) {
			err := r.cur.Err()
			r.closeCur()
			if err != nil {
				return rows, false, mapError(err)
			}
			break
		}
		var d bson.D
		if err := bson.Unmarshal(r.cur.Current, &d); err != nil {
			return rows, false, err
		}
		rows = append(rows, row(d, r.cols))
		r.last = append(r.last, d)
	}
	return rows, len(r.buf) > 0 || r.cur != nil, nil
}

func (r *result) closeCur() {
	if r.cur != nil {
		r.cur.Close(context.Background())
		r.cur = nil
	}
}

func (r *result) RowsAffected() int64                         { return r.affected }
func (r *result) NextResultSet(context.Context) (bool, error) { return false, nil }
func (r *result) Messages() []string                          { return r.msgs }

func (r *result) Close() error {
	r.closeCur()
	r.buf = nil
	if r.cancel != nil {
		r.cancel()
	}
	return nil
}

// docs returns a result over documents already read.
func docs(ds []bson.D) *result {
	cols, _ := inferColumns(ds)
	return &result{cols: cols, buf: ds, affected: -1}
}

// written reports a write.
func written(n int64, msgs ...string) *result {
	return &result{noRows: true, affected: n, msgs: msgs}
}

// cursorResult reads the first documents of a cursor to fix the columns.
func cursorResult(ctx context.Context, cur *mongo.Cursor) (*result, error) {
	var ds []bson.D
	for len(ds) < prefetch {
		if !cur.Next(ctx) {
			err := cur.Err()
			cur.Close(context.Background())
			if err != nil {
				return nil, err
			}
			return docs(ds), nil
		}
		var d bson.D
		if err := bson.Unmarshal(cur.Current, &d); err != nil {
			cur.Close(context.Background())
			return nil, err
		}
		ds = append(ds, d)
	}
	r := docs(ds)
	r.cur = cur
	return r, nil
}

// ---- execution ----

func (s *session) run(ctx context.Context, st *statement) (*result, error) {
	switch st.kind {
	case "use":
		s.database = st.arg
		return written(-1, "switched to db "+st.arg), nil
	case "show":
		return s.show(ctx, st.arg)
	case "db":
		return s.dbMethod(ctx, st)
	}
	return s.collMethod(ctx, st)
}

func (s *session) show(ctx context.Context, what string) (*result, error) {
	switch what {
	case "dbs", "databases":
		res, err := s.c.cl.ListDatabases(ctx, bson.D{})
		if err != nil {
			return nil, err
		}
		var ds []bson.D
		for _, d := range res.Databases {
			ds = append(ds, bson.D{{Key: "name", Value: d.Name}, {Key: "sizeOnDisk", Value: d.SizeOnDisk}, {Key: "empty", Value: d.Empty}})
		}
		return docs(ds), nil
	case "collections", "tables":
		cur, err := s.db("").RunCommandCursor(ctx, bson.D{{Key: "listCollections", Value: 1}, {Key: "nameOnly", Value: true}})
		if err != nil {
			return nil, err
		}
		return cursorResult(ctx, cur)
	case "users":
		return s.command(ctx, "", bson.D{{Key: "usersInfo", Value: 1}})
	case "roles":
		return s.command(ctx, "", bson.D{{Key: "rolesInfo", Value: 1}})
	}
	return nil, fmt.Errorf("show %s is not supported (dbs, collections, users, roles)", what)
}

// command runs a command and shows its reply as one document.
func (s *session) command(ctx context.Context, dbName string, cmd bson.D) (*result, error) {
	var d bson.D
	if err := s.db(dbName).RunCommand(ctx, cmd).Decode(&d); err != nil {
		return nil, err
	}
	if err := writeErrors(d); err != nil {
		return nil, err
	}
	return docs([]bson.D{d}), nil
}

// cursorCommand runs a command returning a cursor.
func (s *session) cursorCommand(ctx context.Context, dbName string, cmd bson.D) (*result, error) {
	cur, err := s.db(dbName).RunCommandCursor(ctx, cmd)
	if err != nil {
		return nil, err
	}
	return cursorResult(ctx, cur)
}

// writeErrors turns the error fields of a write command reply into an
// error (RunCommand reports only command failures).
func writeErrors(d bson.D) error {
	if we, ok := field(d, "writeErrors"); ok {
		if a, ok := we.(bson.A); ok && len(a) > 0 {
			if e, ok := a[0].(bson.D); ok {
				msg, _ := field(e, "errmsg")
				code, _ := field(e, "code")
				return &db.Error{Msg: fmt.Sprint(msg), Code: fmt.Sprint(code)}
			}
		}
	}
	if wc, ok := field(d, "writeConcernError"); ok {
		if e, ok := wc.(bson.D); ok {
			msg, _ := field(e, "errmsg")
			return &db.Error{Msg: "write concern: " + fmt.Sprint(msg)}
		}
	}
	return nil
}

func argDoc(c call, i int, what string) (bson.D, error) {
	if i >= len(c.args) {
		return bson.D{}, nil
	}
	switch d := c.args[i].(type) {
	case bson.D:
		return d, nil
	case nil:
		return bson.D{}, nil
	}
	return nil, fmt.Errorf("%s(): %s must be a document", c.name, what)
}

func argArray(c call, i int, what string) (bson.A, error) {
	if i < len(c.args) {
		if a, ok := c.args[i].(bson.A); ok {
			return a, nil
		}
	}
	return nil, fmt.Errorf("%s(): %s must be an array", c.name, what)
}

func argInt(v any) (int64, bool) {
	switch x := v.(type) {
	case int32:
		return int64(x), true
	case int64:
		return x, true
	case float64:
		return int64(x), true
	}
	return 0, false
}

// merge appends the fields of opts to cmd (command options as written).
func merge(cmd bson.D, opts bson.D) bson.D {
	for _, e := range opts {
		cmd = append(cmd, e)
	}
	return cmd
}

func (s *session) dbMethod(ctx context.Context, st *statement) (*result, error) {
	c := st.calls[0]
	dbn := st.dbName
	switch c.name {
	case "getCollectionNames":
		return s.cursorCommand(ctx, dbn, bson.D{{Key: "listCollections", Value: 1}, {Key: "nameOnly", Value: true}})
	case "getCollectionInfos":
		f, err := argDoc(c, 0, "the filter")
		if err != nil {
			return nil, err
		}
		return s.cursorCommand(ctx, dbn, bson.D{{Key: "listCollections", Value: 1}, {Key: "filter", Value: f}})
	case "runCommand", "adminCommand":
		cmd, err := argDoc(c, 0, "the command")
		if err != nil || len(cmd) == 0 {
			return nil, fmt.Errorf("%s needs a command document", c.name)
		}
		if c.name == "adminCommand" {
			dbn = "admin"
		}
		if _, ok := field(cmd, "cursor"); ok || isCursorCommand(cmd[0].Key) {
			return s.cursorCommand(ctx, dbn, cmd)
		}
		return s.command(ctx, dbn, cmd)
	case "createCollection":
		name, ok := firstString(c.args)
		if !ok {
			return nil, errors.New("createCollection needs a name")
		}
		opts, err := argDoc(c, 1, "the options")
		if err != nil {
			return nil, err
		}
		if _, err := s.command(ctx, dbn, merge(bson.D{{Key: "create", Value: name}}, opts)); err != nil {
			return nil, err
		}
		return written(-1, "created collection "+name), nil
	case "createView":
		name, ok := firstString(c.args)
		src, ok2 := "", false
		if len(c.args) > 1 {
			src, ok2 = c.args[1].(string)
		}
		if !ok || !ok2 {
			return nil, errors.New("createView needs a name and a source collection")
		}
		pipe, err := argArray(c, 2, "the pipeline")
		if err != nil {
			return nil, err
		}
		opts, err := argDoc(c, 3, "the options")
		if err != nil {
			return nil, err
		}
		if _, err := s.command(ctx, dbn, merge(bson.D{{Key: "create", Value: name}, {Key: "viewOn", Value: src}, {Key: "pipeline", Value: pipe}}, opts)); err != nil {
			return nil, err
		}
		return written(-1, "created view "+name), nil
	case "dropDatabase":
		if _, err := s.command(ctx, dbn, bson.D{{Key: "dropDatabase", Value: 1}}); err != nil {
			return nil, err
		}
		return written(-1, "dropped database "+s.dbName(dbn)), nil
	case "stats":
		return s.command(ctx, dbn, bson.D{{Key: "dbStats", Value: 1}})
	case "serverStatus":
		return s.command(ctx, "admin", bson.D{{Key: "serverStatus", Value: 1}})
	case "hello", "isMaster":
		return s.command(ctx, dbn, bson.D{{Key: c.name, Value: 1}})
	case "hostInfo":
		return s.command(ctx, "admin", bson.D{{Key: "hostInfo", Value: 1}})
	case "version":
		var bi bson.D
		if err := s.db("admin").RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&bi); err != nil {
			return nil, err
		}
		v, _ := field(bi, "version")
		return docs([]bson.D{{{Key: "version", Value: v}}}), nil
	case "getName":
		return docs([]bson.D{{{Key: "name", Value: s.dbName(dbn)}}}), nil
	case "currentOp":
		f, err := argDoc(c, 0, "the filter")
		if err != nil {
			return nil, err
		}
		return s.cursorCommand(ctx, "admin", bson.D{{Key: "aggregate", Value: 1},
			{Key: "pipeline", Value: bson.A{bson.D{{Key: "$currentOp", Value: bson.D{}}}, bson.D{{Key: "$match", Value: f}}}},
			{Key: "cursor", Value: bson.D{}}})
	case "killOp":
		if len(c.args) == 0 {
			return nil, errors.New("killOp needs an operation id")
		}
		if _, err := s.command(ctx, "admin", bson.D{{Key: "killOp", Value: 1}, {Key: "op", Value: c.args[0]}}); err != nil {
			return nil, err
		}
		return written(-1, "kill requested"), nil
	case "aggregate":
		pipe, err := argArray(c, 0, "the pipeline")
		if err != nil {
			return nil, err
		}
		opts, err := argDoc(c, 1, "the options")
		if err != nil {
			return nil, err
		}
		return s.cursorCommand(ctx, dbn, merge(bson.D{{Key: "aggregate", Value: 1}, {Key: "pipeline", Value: pipe}, {Key: "cursor", Value: bson.D{}}}, opts))
	}
	return nil, fmt.Errorf("db.%s() is not supported", c.name)
}

func (s *session) dbName(n string) string {
	if n == "" {
		return s.database
	}
	return n
}

// isCursorCommand reports commands whose reply is a cursor.
func isCursorCommand(name string) bool {
	switch name {
	case "find", "aggregate", "listCollections", "listIndexes":
		return true
	}
	return false
}

// findCommand builds the find command of db.c.find(…).sort(…)….
func findCommand(st *statement) (bson.D, error) {
	c := st.calls[0]
	f, err := argDoc(c, 0, "the filter")
	if err != nil {
		return nil, err
	}
	cmd := bson.D{{Key: "find", Value: st.coll}, {Key: "filter", Value: f}}
	if len(c.args) > 1 {
		p, err := argDoc(c, 1, "the projection")
		if err != nil {
			return nil, err
		}
		cmd = append(cmd, bson.E{Key: "projection", Value: p})
	}
	if len(c.args) > 2 {
		o, err := argDoc(c, 2, "the options")
		if err != nil {
			return nil, err
		}
		cmd = merge(cmd, o)
	}
	for _, m := range st.calls[1:] {
		arg := func() (any, error) {
			if len(m.args) != 1 {
				return nil, fmt.Errorf("%s() takes one argument", m.name)
			}
			return m.args[0], nil
		}
		switch m.name {
		case "sort", "projection", "project", "hint", "collation", "comment", "min", "max", "let":
			v, err := arg()
			if err != nil {
				return nil, err
			}
			k := m.name
			if k == "project" {
				k = "projection"
			}
			cmd = append(cmd, bson.E{Key: k, Value: v})
		case "limit", "skip", "batchSize", "maxTimeMS":
			v, err := arg()
			if err != nil {
				return nil, err
			}
			n, ok := argInt(v)
			if !ok {
				return nil, fmt.Errorf("%s() needs a number", m.name)
			}
			cmd = append(cmd, bson.E{Key: m.name, Value: n})
		case "allowDiskUse":
			cmd = append(cmd, bson.E{Key: "allowDiskUse", Value: true})
		case "readConcern":
			v, err := arg()
			if err != nil {
				return nil, err
			}
			cmd = append(cmd, bson.E{Key: "readConcern", Value: bson.D{{Key: "level", Value: v}}})
		case "toArray", "pretty", "explain", "count", "itcount", "size":
		default:
			return nil, fmt.Errorf("cursor method %s() is not supported", m.name)
		}
	}
	return cmd, nil
}

// aggregateCommand builds the aggregate command of db.c.aggregate(…).
func aggregateCommand(st *statement) (bson.D, error) {
	c := st.calls[0]
	var pipe bson.A
	switch {
	case len(c.args) == 0:
		pipe = bson.A{}
	default:
		if a, ok := c.args[0].(bson.A); ok {
			pipe = a
		} else {
			// mongosh also accepts stages as arguments
			pipe = bson.A(c.args)
		}
	}
	cmd := bson.D{{Key: "aggregate", Value: st.coll}, {Key: "pipeline", Value: pipe}, {Key: "cursor", Value: bson.D{}}}
	if a, ok := c.args, len(c.args) > 1; ok {
		if o, ok := a[1].(bson.D); ok {
			if _, isArr := a[0].(bson.A); isArr {
				cmd = merge(cmd, o)
			}
		}
	}
	for _, m := range st.calls[1:] {
		switch m.name {
		case "toArray", "pretty", "explain", "itcount":
		default:
			return nil, fmt.Errorf("aggregate cursor method %s() is not supported", m.name)
		}
	}
	return cmd, nil
}

// hasCall reports whether the chain calls name after the operation.
func hasCall(st *statement, names ...string) (string, bool) {
	for _, m := range st.calls[1:] {
		for _, n := range names {
			if m.name == n {
				return n, true
			}
		}
	}
	return "", false
}

func (s *session) collMethod(ctx context.Context, st *statement) (*result, error) {
	c := st.calls[0]
	dbn := st.dbName
	coll := st.coll
	switch c.name {
	case "find":
		cmd, err := findCommand(st)
		if err != nil {
			return nil, err
		}
		if _, ok := hasCall(st, "explain"); ok {
			return s.command(ctx, dbn, bson.D{{Key: "explain", Value: cmd}, {Key: "verbosity", Value: explainVerbosity(st)}})
		}
		if _, ok := hasCall(st, "count", "itcount", "size"); ok {
			return s.count(ctx, dbn, coll, cmd)
		}
		return s.cursorCommand(ctx, dbn, cmd)
	case "findOne":
		cmd, err := findCommand(&statement{coll: coll, calls: []call{{name: "find", args: c.args}}})
		if err != nil {
			return nil, err
		}
		cmd = append(cmd, bson.E{Key: "limit", Value: int64(1)}, bson.E{Key: "singleBatch", Value: true})
		return s.cursorCommand(ctx, dbn, cmd)
	case "aggregate":
		cmd, err := aggregateCommand(st)
		if err != nil {
			return nil, err
		}
		if _, ok := hasCall(st, "explain"); ok {
			return s.command(ctx, dbn, bson.D{{Key: "explain", Value: cmd}, {Key: "verbosity", Value: explainVerbosity(st)}})
		}
		return s.cursorCommand(ctx, dbn, cmd)
	case "countDocuments", "count":
		f, err := argDoc(c, 0, "the filter")
		if err != nil {
			return nil, err
		}
		cmd := bson.D{{Key: "find", Value: coll}, {Key: "filter", Value: f}}
		if o, err := argDoc(c, 1, "the options"); err == nil {
			for _, e := range o {
				if e.Key == "limit" || e.Key == "skip" || e.Key == "hint" || e.Key == "collation" {
					cmd = append(cmd, e)
				}
			}
		}
		return s.count(ctx, dbn, coll, cmd)
	case "estimatedDocumentCount":
		var d bson.D
		if err := s.db(dbn).RunCommand(ctx, bson.D{{Key: "count", Value: coll}}).Decode(&d); err != nil {
			return nil, err
		}
		n, _ := field(d, "n")
		return docs([]bson.D{{{Key: "count", Value: n}}}), nil
	case "distinct":
		k, ok := firstString(c.args)
		if !ok {
			return nil, errors.New("distinct needs a field name")
		}
		q, err := argDoc(c, 1, "the filter")
		if err != nil {
			return nil, err
		}
		var d bson.D
		if err := s.db(dbn).RunCommand(ctx, bson.D{{Key: "distinct", Value: coll}, {Key: "key", Value: k}, {Key: "query", Value: q}}).Decode(&d); err != nil {
			return nil, err
		}
		vals, _ := field(d, "values")
		var ds []bson.D
		if a, ok := vals.(bson.A); ok {
			for _, v := range a {
				ds = append(ds, bson.D{{Key: k, Value: v}})
			}
		}
		r := docs(ds)
		if len(ds) == 0 {
			r.cols = []db.Column{{Name: k, Kind: db.KindOther}}
		}
		return r, nil
	case "insertOne", "insertMany", "insert":
		var list bson.A
		switch {
		case c.name == "insertMany":
			a, err := argArray(c, 0, "the documents")
			if err != nil {
				return nil, err
			}
			list = a
		case len(c.args) > 0:
			if a, ok := c.args[0].(bson.A); ok && c.name == "insert" {
				list = a
			} else {
				list = bson.A{c.args[0]}
			}
		default:
			return nil, fmt.Errorf("%s needs a document", c.name)
		}
		var ids []string
		for i, v := range list {
			d, ok := v.(bson.D)
			if !ok {
				return nil, fmt.Errorf("%s: document %d is not a document", c.name, i+1)
			}
			id, has := field(d, "_id")
			if !has {
				id = bson.NewObjectID()
				d = append(bson.D{{Key: "_id", Value: id}}, d...)
				list[i] = d
			}
			ids = append(ids, shellLit(id))
		}
		cmd := bson.D{{Key: "insert", Value: coll}, {Key: "documents", Value: list}}
		if o, err := argDoc(c, 1, "the options"); err == nil {
			cmd = merge(cmd, o)
		}
		d, err := s.write(ctx, dbn, cmd)
		if err != nil {
			return nil, err
		}
		n, _ := argInt(fieldOr(d, "n"))
		msg := "insertedId: " + strings.Join(ids, ", ")
		info := bson.D{{Key: "acknowledged", Value: true}}
		if c.name == "insertOne" || len(list) == 1 && c.name == "insert" {
			info = append(info, bson.E{Key: "insertedId", Value: fieldOr(list[0].(bson.D), "_id")})
		} else {
			idList := bson.A{}
			for _, d := range list {
				idList = append(idList, fieldOr(d.(bson.D), "_id"))
			}
			info = append(info, bson.E{Key: "insertedIds", Value: idList})
		}
		if len(ids) > 1 {
			msg = "insertedIds: " + strings.Join(ids, ", ")
		}
		r := written(n, msg)
		r.info = info
		return r, nil
	case "updateOne", "updateMany", "replaceOne", "update":
		f, err := argDoc(c, 0, "the filter")
		if err != nil {
			return nil, err
		}
		if len(c.args) < 2 {
			return nil, fmt.Errorf("%s needs a filter and an update", c.name)
		}
		u := c.args[1]
		switch x := u.(type) {
		case bson.D:
			if c.name == "replaceOne" && len(x) > 0 && strings.HasPrefix(x[0].Key, "$") {
				return nil, errors.New("replaceOne takes a document, not update operators")
			}
			if c.name != "replaceOne" && c.name != "update" && (len(x) == 0 || !strings.HasPrefix(x[0].Key, "$")) {
				return nil, fmt.Errorf("%s needs update operators ($set…); use replaceOne to replace a document", c.name)
			}
		case bson.A: // pipeline update
		default:
			return nil, fmt.Errorf("%s: the update must be a document or a pipeline", c.name)
		}
		opts, err := argDoc(c, 2, "the options")
		if err != nil {
			return nil, err
		}
		multi := c.name == "updateMany"
		upd := bson.D{{Key: "q", Value: f}, {Key: "u", Value: u}}
		for _, e := range opts {
			switch e.Key {
			case "multi":
				multi, _ = e.Value.(bool)
			case "writeConcern", "ordered", "bypassDocumentValidation", "comment", "let":
			default:
				upd = append(upd, e)
			}
		}
		upd = append(upd, bson.E{Key: "multi", Value: multi})
		cmd := bson.D{{Key: "update", Value: coll}, {Key: "updates", Value: bson.A{upd}}}
		cmd = topOptions(cmd, opts)
		d, err := s.write(ctx, dbn, cmd)
		if err != nil {
			return nil, err
		}
		matched, _ := argInt(fieldOr(d, "n"))
		modified, _ := argInt(fieldOr(d, "nModified"))
		msgs := []string{fmt.Sprintf("matched: %d, modified: %d", matched, modified)}
		info := bson.D{{Key: "acknowledged", Value: true}, {Key: "matchedCount", Value: matched}, {Key: "modifiedCount", Value: modified}}
		if up, ok := field(d, "upserted"); ok {
			if a, ok := up.(bson.A); ok && len(a) > 0 {
				if e, ok := a[0].(bson.D); ok {
					id, _ := field(e, "_id")
					msgs = append(msgs, "upsertedId: "+shellLit(id))
					info = append(info, bson.E{Key: "upsertedId", Value: id})
				}
			}
		}
		r := written(modified, msgs...)
		r.info = info
		return r, nil
	case "deleteOne", "deleteMany", "remove":
		f, err := argDoc(c, 0, "the filter")
		if err != nil {
			return nil, err
		}
		limit := int32(0)
		if c.name == "deleteOne" {
			limit = 1
		}
		opts := bson.D{}
		if len(c.args) > 1 {
			switch x := c.args[1].(type) {
			case bool:
				if x {
					limit = 1 // remove(filter, justOne)
				}
			case bson.D:
				opts = x
				if j, ok := field(x, "justOne"); ok && j == true {
					limit = 1
				}
			}
		}
		del := bson.D{{Key: "q", Value: f}, {Key: "limit", Value: limit}}
		for _, e := range opts {
			if e.Key == "collation" || e.Key == "hint" {
				del = append(del, e)
			}
		}
		cmd := topOptions(bson.D{{Key: "delete", Value: coll}, {Key: "deletes", Value: bson.A{del}}}, opts)
		d, err := s.write(ctx, dbn, cmd)
		if err != nil {
			return nil, err
		}
		n, _ := argInt(fieldOr(d, "n"))
		r := written(n, fmt.Sprintf("deleted: %d", n))
		r.info = bson.D{{Key: "acknowledged", Value: true}, {Key: "deletedCount", Value: n}}
		return r, nil
	case "findOneAndUpdate", "findOneAndReplace", "findOneAndDelete", "findAndModify":
		cmd := bson.D{{Key: "findAndModify", Value: coll}}
		var opts bson.D
		if c.name == "findAndModify" {
			o, err := argDoc(c, 0, "the options")
			if err != nil {
				return nil, err
			}
			cmd = merge(cmd, o)
		} else {
			f, err := argDoc(c, 0, "the filter")
			if err != nil {
				return nil, err
			}
			cmd = append(cmd, bson.E{Key: "query", Value: f})
			oi := 2
			if c.name == "findOneAndDelete" {
				cmd = append(cmd, bson.E{Key: "remove", Value: true})
				oi = 1
			} else {
				if len(c.args) < 2 {
					return nil, fmt.Errorf("%s needs a filter and an update", c.name)
				}
				cmd = append(cmd, bson.E{Key: "update", Value: c.args[1]})
			}
			o, err := argDoc(c, oi, "the options")
			if err != nil {
				return nil, err
			}
			opts = o
			for _, e := range opts {
				switch e.Key {
				case "returnDocument":
					cmd = append(cmd, bson.E{Key: "new", Value: e.Value == "after"})
				case "returnNewDocument":
					cmd = append(cmd, bson.E{Key: "new", Value: e.Value})
				case "projection":
					cmd = append(cmd, bson.E{Key: "fields", Value: e.Value})
				default:
					cmd = append(cmd, e)
				}
			}
		}
		d, err := s.write(ctx, dbn, cmd)
		if err != nil {
			return nil, err
		}
		v, _ := field(d, "value")
		if doc, ok := v.(bson.D); ok {
			return docs([]bson.D{doc}), nil
		}
		return docs(nil), nil
	case "bulkWrite":
		return s.bulkWrite(ctx, dbn, coll, c)
	case "createIndex", "createIndexes", "ensureIndex":
		var specs bson.A
		var keysList bson.A
		if c.name == "createIndexes" {
			a, err := argArray(c, 0, "the index keys")
			if err != nil {
				return nil, err
			}
			keysList = a
		} else {
			if len(c.args) == 0 {
				return nil, errors.New("createIndex needs the index keys")
			}
			keysList = bson.A{c.args[0]}
		}
		opts, err := argDoc(c, 1, "the options")
		if err != nil {
			return nil, err
		}
		var names []string
		for _, k := range keysList {
			kd, ok := k.(bson.D)
			if !ok || len(kd) == 0 {
				return nil, errors.New("index keys must be a document like {field: 1}")
			}
			spec := bson.D{{Key: "key", Value: kd}}
			name, has := field(opts, "name")
			if !has || c.name == "createIndexes" {
				name = indexName(kd)
			}
			spec = append(spec, bson.E{Key: "name", Value: name})
			for _, e := range opts {
				if e.Key != "name" {
					spec = append(spec, e)
				}
			}
			names = append(names, fmt.Sprint(name))
			specs = append(specs, spec)
		}
		if _, err := s.write(ctx, dbn, bson.D{{Key: "createIndexes", Value: coll}, {Key: "indexes", Value: specs}}); err != nil {
			return nil, err
		}
		return written(-1, "created index "+strings.Join(names, ", ")), nil
	case "dropIndex", "dropIndexes":
		var idx any = "*"
		if len(c.args) > 0 {
			idx = c.args[0]
		} else if c.name == "dropIndex" {
			return nil, errors.New("dropIndex needs an index name or keys")
		}
		if _, err := s.write(ctx, dbn, bson.D{{Key: "dropIndexes", Value: coll}, {Key: "index", Value: idx}}); err != nil {
			return nil, err
		}
		return written(-1, "dropped index "+shellLit(idx)), nil
	case "getIndexes", "indexes":
		return s.cursorCommand(ctx, dbn, bson.D{{Key: "listIndexes", Value: coll}})
	case "drop":
		if _, err := s.write(ctx, dbn, bson.D{{Key: "drop", Value: coll}}); err != nil {
			return nil, err
		}
		return written(-1, "dropped collection "+coll), nil
	case "renameCollection":
		to, ok := firstString(c.args)
		if !ok {
			return nil, errors.New("renameCollection needs the new name")
		}
		drop := false
		if len(c.args) > 1 {
			drop, _ = c.args[1].(bool)
		}
		ns := s.dbName(dbn) + "."
		if _, err := s.command(ctx, "admin", bson.D{{Key: "renameCollection", Value: ns + coll}, {Key: "to", Value: ns + to}, {Key: "dropTarget", Value: drop}}); err != nil {
			return nil, err
		}
		return written(-1, "renamed "+coll+" to "+to), nil
	case "stats":
		return s.cursorCommand(ctx, dbn, bson.D{{Key: "aggregate", Value: coll},
			{Key: "pipeline", Value: bson.A{bson.D{{Key: "$collStats", Value: bson.D{{Key: "storageStats", Value: bson.D{}}, {Key: "count", Value: bson.D{}}}}}}},
			{Key: "cursor", Value: bson.D{}}})
	case "validate":
		return s.command(ctx, dbn, bson.D{{Key: "validate", Value: coll}})
	}
	return nil, fmt.Errorf("%s() is not supported on a collection", c.name)
}

func fieldOr(d bson.D, k string) any {
	v, _ := field(d, k)
	return v
}

// topOptions copies the command-level options of a write.
func topOptions(cmd, opts bson.D) bson.D {
	for _, e := range opts {
		switch e.Key {
		case "writeConcern", "ordered", "bypassDocumentValidation", "comment", "let":
			cmd = append(cmd, e)
		}
	}
	return cmd
}

func explainVerbosity(st *statement) string {
	for _, m := range st.calls[1:] {
		if m.name == "explain" && len(m.args) > 0 {
			if v, ok := m.args[0].(string); ok {
				return v
			}
			if b, ok := m.args[0].(bool); ok && b {
				return "allPlansExecution"
			}
		}
	}
	return "queryPlanner"
}

// write runs a write command and checks its write errors.
func (s *session) write(ctx context.Context, dbName string, cmd bson.D) (bson.D, error) {
	var d bson.D
	if err := s.db(dbName).RunCommand(ctx, cmd).Decode(&d); err != nil {
		return nil, err
	}
	return d, writeErrors(d)
}

// count counts the documents a find command would return.
func (s *session) count(ctx context.Context, dbName, coll string, find bson.D) (*result, error) {
	f, _ := field(find, "filter")
	pipe := bson.A{bson.D{{Key: "$match", Value: f}}}
	if v, ok := field(find, "skip"); ok {
		pipe = append(pipe, bson.D{{Key: "$skip", Value: v}})
	}
	if v, ok := field(find, "limit"); ok {
		if n, _ := argInt(v); n > 0 {
			pipe = append(pipe, bson.D{{Key: "$limit", Value: n}})
		}
	}
	pipe = append(pipe, bson.D{{Key: "$group", Value: bson.D{{Key: "_id", Value: nil}, {Key: "n", Value: bson.D{{Key: "$sum", Value: 1}}}}}})
	cmd := bson.D{{Key: "aggregate", Value: coll}, {Key: "pipeline", Value: pipe}, {Key: "cursor", Value: bson.D{}}}
	for _, k := range []string{"hint", "collation"} {
		if v, ok := field(find, k); ok {
			cmd = append(cmd, bson.E{Key: k, Value: v})
		}
	}
	cur, err := s.db(dbName).RunCommandCursor(ctx, cmd)
	if err != nil {
		return nil, err
	}
	defer cur.Close(context.Background())
	n := int64(0)
	if cur.Next(ctx) {
		var d bson.D
		if err := bson.Unmarshal(cur.Current, &d); err != nil {
			return nil, err
		}
		n, _ = argInt(fieldOr(d, "n"))
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	return docs([]bson.D{{{Key: "count", Value: n}}}), nil
}

// indexName is the server's default index name ("a_1_b_-1").
func indexName(keys bson.D) string {
	parts := make([]string, 0, 2*len(keys))
	for _, e := range keys {
		parts = append(parts, e.Key, fmt.Sprint(cell(e.Value)))
	}
	return strings.Join(parts, "_")
}

func (s *session) bulkWrite(ctx context.Context, dbName, coll string, c call) (*result, error) {
	ops, err := argArray(c, 0, "the operations")
	if err != nil {
		return nil, err
	}
	ordered := true
	if o, err := argDoc(c, 1, "the options"); err == nil {
		if v, ok := field(o, "ordered"); ok {
			ordered, _ = v.(bool)
		}
	}
	var total int64
	for i, op := range ops {
		d, ok := op.(bson.D)
		if !ok || len(d) != 1 {
			return nil, fmt.Errorf("bulkWrite: operation %d must be {insertOne: …}, {updateOne: …}…", i+1)
		}
		body, _ := d[0].Value.(bson.D)
		var sub call
		switch d[0].Key {
		case "insertOne":
			doc, _ := field(body, "document")
			sub = call{name: "insertOne", args: []any{doc}}
		case "updateOne", "updateMany":
			f, _ := field(body, "filter")
			u, _ := field(body, "update")
			sub = call{name: d[0].Key, args: []any{f, u, optionsOf(body, "filter", "update")}}
		case "replaceOne":
			f, _ := field(body, "filter")
			r, _ := field(body, "replacement")
			sub = call{name: "replaceOne", args: []any{f, r, optionsOf(body, "filter", "replacement")}}
		case "deleteOne", "deleteMany":
			f, _ := field(body, "filter")
			sub = call{name: d[0].Key, args: []any{f, optionsOf(body, "filter")}}
		default:
			return nil, fmt.Errorf("bulkWrite: unknown operation %s", d[0].Key)
		}
		r, err := s.collMethod(ctx, &statement{kind: "coll", dbName: dbName, coll: coll, calls: []call{sub}})
		if err != nil {
			if ordered {
				return nil, fmt.Errorf("bulkWrite operation %d (%d applied before): %w", i+1, i, err)
			}
			continue
		}
		total += max(0, r.affected)
	}
	return written(total, fmt.Sprintf("%d operation(s), %d document(s) written", len(ops), total)), nil
}

func optionsOf(d bson.D, skip ...string) bson.D {
	out := bson.D{}
	for _, e := range d {
		keep := true
		for _, s := range skip {
			keep = keep && e.Key != s
		}
		if keep {
			out = append(out, e)
		}
	}
	return out
}
