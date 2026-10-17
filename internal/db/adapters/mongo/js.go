package mongo

// Dynamic statements: a JavaScript runtime (goja) with the mongosh API —
// db, collections, cursors, ObjectId()/ISODate()… helpers, print — kept
// per session, so variables survive between statements like in mongosh.
// Every database operation a script performs is built as a statement,
// classified with its actual arguments and checked before it runs: a
// read-only session refuses writes, the session Guard (set by the console
// on protected connections) confirms them.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dop251/goja"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/twoojoo/lumino/internal/db"
)

// jsShell is the runtime of a session.
type jsShell struct {
	s       *session
	r       *goja.Runtime
	ctx     context.Context
	out     []string
	cursors map[*goja.Object]*jsCursor
}

// jsCursor is a find / aggregate not run yet (modifiers are chained) or
// being iterated.
type jsCursor struct {
	st  *statement
	res *result
}

// jsBSON wraps BSON values with no JavaScript equivalent (ObjectId,
// Decimal128, Long, binary…): methods are visible to scripts in camel case.
type jsBSON struct{ V any }

func (b *jsBSON) ToString() string {
	switch x := b.V.(type) {
	case bson.ObjectID:
		return x.Hex()
	case bson.Decimal128:
		return x.String()
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case bson.Binary:
		if x.Subtype == 4 && len(x.Data) == 16 {
			return uuidString(x.Data)
		}
	}
	return shellLit(b.V)
}

func (b *jsBSON) ToHexString() string { return b.ToString() }
func (b *jsBSON) Inspect() string     { return shellLit(b.V) }
func (b *jsBSON) ToJSON() any         { return nested(b.V) }

// ValueOf makes Long and Double usable in arithmetic.
func (b *jsBSON) ValueOf() any {
	switch x := b.V.(type) {
	case int64:
		return float64(x)
	case float64:
		return x
	case bson.Decimal128:
		f, _ := strconv.ParseFloat(x.String(), 64)
		return f
	}
	return b.ToString()
}

func (b *jsBSON) ToNumber() float64 {
	f, _ := b.ValueOf().(float64)
	return f
}

// Equals compares with another value (ObjectId.equals()).
func (b *jsBSON) Equals(o goja.Value) bool {
	if o == nil {
		return false
	}
	if x, ok := o.Export().(*jsBSON); ok {
		return shellLit(x.V) == shellLit(b.V)
	}
	return o.String() == b.ToString()
}

// GetTimestamp is the creation time of an ObjectId.
func (b *jsBSON) GetTimestamp() time.Time {
	if id, ok := b.V.(bson.ObjectID); ok {
		return id.Timestamp()
	}
	return time.Time{}
}

// shell returns the runtime of the session.
func (s *session) shell() *jsShell {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.js == nil {
		s.js = newJSShell(s)
	}
	return s.js
}

func (j *jsShell) interrupt() { j.r.Interrupt(context.Canceled) }

func newJSShell(s *session) *jsShell {
	j := &jsShell{s: s, r: goja.New(), cursors: map[*goja.Object]*jsCursor{}}
	r := j.r
	r.SetFieldNameMapper(goja.UncapFieldNameMapper())
	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	must(r.Set("db", j.dbObject("")))
	printer := func(call goja.FunctionCall) goja.Value {
		parts := make([]string, len(call.Arguments))
		for i, a := range call.Arguments {
			parts[i] = j.display(a)
		}
		j.out = append(j.out, strings.Join(parts, " "))
		return goja.Undefined()
	}
	must(r.Set("print", printer))
	must(r.Set("printjson", printer))
	console := r.NewObject()
	must(console.Set("log", printer))
	must(r.Set("console", console))
	must(r.Set("sleep", func(ms int64) {
		select {
		case <-time.After(time.Duration(ms) * time.Millisecond):
		case <-j.ctx.Done():
		}
	}))
	for _, name := range []string{"ObjectId", "ObjectID", "ISODate", "NumberLong", "Long", "NumberInt", "Int32", "Double",
		"NumberDecimal", "Decimal128", "UUID", "BinData", "HexData", "Timestamp", "MinKey", "MaxKey", "Code"} {
		name := name
		must(r.Set(name, func(call goja.ConstructorCall) *goja.Object {
			return j.helper(name, call.Arguments).ToObject(r)
		}))
	}
	return j
}

// helper runs a value helper with JavaScript arguments.
func (j *jsShell) helper(name string, args []goja.Value) goja.Value {
	a := make([]any, len(args))
	for i, v := range args {
		a[i] = j.mustBSON(v)
	}
	v, err := helperValue(name, true, a)
	if err != nil {
		panic(j.r.NewGoError(err))
	}
	if name == "ISODate" {
		return j.toJS(v)
	}
	return j.r.ToValue(&jsBSON{V: v})
}

// run executes a script; the value of its last expression is the result.
func (j *jsShell) run(ctx context.Context, src string) (*result, error) {
	j.ctx, j.out = ctx, nil
	j.r.ClearInterrupt()
	stop := context.AfterFunc(ctx, j.interrupt)
	defer stop()
	v, err := j.r.RunString(src)
	if err != nil {
		return nil, j.scriptError(ctx, err)
	}
	res, err := j.toResult(ctx, v)
	if err != nil {
		return nil, j.scriptError(ctx, err)
	}
	res.msgs = append(append([]string{}, j.out...), res.msgs...)
	return res, nil
}

// positions in runtime errors (stack) and in syntax errors (message)
// jsNoise is the part of messages repeated by the line/column fields
var jsNoise = regexp.MustCompile(`\(anonymous\): |Line \d+:\d+ `)

var jsPos = regexp.MustCompile(`<eval>:(\d+):(\d+)|Line (\d+):(\d+)`)

// scriptError maps runtime and syntax errors to *db.Error (line, column).
func (j *jsShell) scriptError(ctx context.Context, err error) error {
	var ie *goja.InterruptedError
	if errors.As(err, &ie) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("interrupted")
	}
	var ex *goja.Exception
	if errors.As(err, &ex) {
		// errors thrown by the Go side keep their *db.Error
		if ge, ok := ex.Value().Export().(error); ok {
			var de *db.Error
			if errors.As(ge, &de) {
				return de
			}
			if errors.Is(ge, context.Canceled) && ctx.Err() != nil {
				return ctx.Err()
			}
		}
		msg := jsNoise.ReplaceAllString(ex.Value().String(), "")
		msg = strings.Replace(msg, "SyntaxError: SyntaxError: ", "SyntaxError: ", 1)
		e := &db.Error{Msg: msg, Err: err}
		if m := jsPos.FindStringSubmatch(ex.String()); m != nil {
			l, c := m[1], m[2]
			if l == "" {
				l, c = m[3], m[4]
			}
			e.Line, _ = strconv.Atoi(l)
			e.Column, _ = strconv.Atoi(c)
		}
		return e
	}
	var se *goja.CompilerSyntaxError
	if errors.As(err, &se) {
		e := &db.Error{Msg: "SyntaxError: " + jsNoise.ReplaceAllString(se.Message, ""), Err: err}
		e.Line, e.Column = se.File.Position(se.Offset).Line, se.File.Position(se.Offset).Column
		return e
	}
	return err
}

// display renders a value for print().
func (j *jsShell) display(v goja.Value) string {
	if v == nil || goja.IsUndefined(v) {
		return "undefined"
	}
	if s, ok := v.Export().(string); ok {
		return s
	}
	if o, ok := v.(*goja.Object); ok {
		if _, isCursor := j.cursors[o]; isCursor {
			return "[cursor]"
		}
		if _, isFn := goja.AssertFunction(v); isFn {
			return "[function]"
		}
	}
	b, err := j.toBSON(v)
	if err != nil {
		return v.String()
	}
	return shellLit(b)
}

// ---- conversions ----

func (j *jsShell) mustBSON(v goja.Value) any {
	b, err := j.toBSON(v)
	if err != nil {
		panic(j.r.NewGoError(err))
	}
	return b
}

// toBSON converts a script value: objects keep their key order, integers
// are Int32 (Int64 beyond), Date is a BSON date.
func (j *jsShell) toBSON(v goja.Value) (any, error) {
	if v == nil || goja.IsUndefined(v) {
		return bson.Undefined{}, nil
	}
	if goja.IsNull(v) {
		return nil, nil
	}
	o, isObj := v.(*goja.Object)
	if !isObj {
		switch x := v.Export().(type) {
		case int64:
			return intValue(x), nil
		case float64:
			if x == math.Trunc(x) && math.Abs(x) < 1<<53 && !math.IsInf(x, 0) {
				return intValue(int64(x)), nil
			}
			return x, nil
		case string, bool:
			return x, nil
		}
		return v.String(), nil
	}
	if _, isCursor := j.cursors[o]; isCursor {
		return nil, errors.New("a cursor is not a value: call toArray()")
	}
	switch x := o.Export().(type) {
	case *jsBSON:
		return x.V, nil
	case time.Time:
		return bson.NewDateTimeFromTime(x), nil
	}
	switch o.ClassName() {
	case "Array":
		n := int(o.Get("length").ToInteger())
		a := make(bson.A, n)
		for i := 0; i < n; i++ {
			e, err := j.toBSON(o.Get(strconv.Itoa(i)))
			if err != nil {
				return nil, err
			}
			if _, u := e.(bson.Undefined); u {
				e = nil
			}
			a[i] = e
		}
		return a, nil
	case "Date":
		if t, ok := o.Export().(time.Time); ok {
			return bson.NewDateTimeFromTime(t), nil
		}
	case "RegExp":
		return bson.Regex{Pattern: o.Get("source").String(), Options: o.Get("flags").String()}, nil
	case "Function":
		return nil, errors.New("a function is not a value")
	}
	d := bson.D{}
	for _, k := range o.Keys() {
		e, err := j.toBSON(o.Get(k))
		if err != nil {
			return nil, err
		}
		if _, u := e.(bson.Undefined); u {
			continue // like the drivers: undefined fields are left out
		}
		d = append(d, bson.E{Key: k, Value: e})
	}
	return d, nil
}

// toJS converts a BSON value for scripts.
func (j *jsShell) toJS(v any) goja.Value {
	r := j.r
	switch x := v.(type) {
	case nil:
		return goja.Null()
	case bson.Undefined:
		return goja.Undefined()
	case bson.D:
		o := r.NewObject()
		for _, e := range x {
			_ = o.Set(e.Key, j.toJS(e.Value))
		}
		return o
	case bson.A:
		items := make([]any, len(x))
		for i, e := range x {
			items[i] = j.toJS(e)
		}
		return r.NewArray(items...)
	case int32:
		return r.ToValue(int64(x))
	case int64:
		if x > -(1<<53) && x < 1<<53 {
			return r.ToValue(x)
		}
		return r.ToValue(&jsBSON{V: x})
	case float64, string, bool:
		return r.ToValue(x)
	case bson.DateTime:
		d, err := r.New(r.Get("Date"), r.ToValue(int64(x)))
		if err != nil {
			panic(err)
		}
		return d
	case bson.Regex:
		re, err := r.New(r.Get("RegExp"), r.ToValue(x.Pattern), r.ToValue(x.Options))
		if err == nil {
			return re
		}
	}
	return r.ToValue(&jsBSON{V: v})
}

// args converts the arguments of a call.
func (j *jsShell) args(call goja.FunctionCall) []any {
	a := make([]any, len(call.Arguments))
	for i, v := range call.Arguments {
		b := j.mustBSON(v)
		if _, u := b.(bson.Undefined); u {
			b = nil
		}
		a[i] = b
	}
	// trailing undefined arguments are absent ones
	for len(a) > 0 && a[len(a)-1] == nil && goja.IsUndefined(call.Arguments[len(a)-1]) {
		a = a[:len(a)-1]
	}
	return a
}

// ---- operations ----

// gate classifies an operation with its actual arguments: a read-only
// session refuses writes, the session guard decides on the others.
func (j *jsShell) gate(st *statement) error {
	s := j.s
	k := classify(st)
	if !k.Modifies() {
		return nil
	}
	if s.readOnly {
		return fmt.Errorf("read-only session: %s refused (%s)", st.String(), k)
	}
	s.mu.Lock()
	g := s.guard
	s.mu.Unlock()
	if g != nil {
		return g(j.ctx, k, st.String())
	}
	return nil
}

// op checks an operation, then runs it.
func (j *jsShell) op(st *statement) *result {
	s := j.s
	if err := j.gate(st); err != nil {
		panic(j.r.NewGoError(err))
	}
	res, err := s.run(j.ctx, st)
	if err != nil {
		panic(j.r.NewGoError(mapError(err)))
	}
	return res
}

// docs reads every document of a result.
func (j *jsShell) docs(res *result) []bson.D {
	var out []bson.D
	for {
		d, ok, err := res.next(j.ctx)
		if err != nil {
			panic(j.r.NewGoError(mapError(err)))
		}
		if !ok {
			return out
		}
		out = append(out, d)
	}
}

// methods of db (the others are collections).
var jsDBMethods = map[string]bool{
	"getCollectionNames": true, "getCollectionInfos": true, "runCommand": true, "adminCommand": true,
	"createCollection": true, "createView": true, "dropDatabase": true, "stats": true, "serverStatus": true,
	"hello": true, "isMaster": true, "hostInfo": true, "version": true, "currentOp": true, "killOp": true, "aggregate": true,
}

// dbDyn is the db object: methods, getCollection/getSiblingDB, and any
// other property is a collection.
type dbDyn struct {
	j    *jsShell
	name string
}

func (d *dbDyn) Get(key string) goja.Value {
	j := d.j
	switch key {
	case "getName":
		return j.r.ToValue(func() string { return j.s.dbName(d.name) })
	case "getCollection":
		return j.r.ToValue(func(name string) goja.Value { return j.collObject(d.name, name) })
	case "getSiblingDB", "getDB":
		return j.r.ToValue(func(name string) goja.Value { return j.dbObject(name) })
	case "toString":
		return j.r.ToValue(func() string { return j.s.dbName(d.name) })
	}
	if jsDBMethods[key] {
		return j.r.ToValue(func(fc goja.FunctionCall) goja.Value {
			st := &statement{kind: "db", dbName: d.name, calls: []call{{name: key, args: j.args(fc)}}}
			return j.value(key, j.op(st))
		})
	}
	return j.collObject(d.name, key)
}

func (d *dbDyn) Set(string, goja.Value) bool { return false }
func (d *dbDyn) Has(string) bool             { return true }
func (d *dbDyn) Delete(string) bool          { return false }
func (d *dbDyn) Keys() []string              { return nil }

func (j *jsShell) dbObject(name string) *goja.Object {
	return j.r.NewDynamicObject(&dbDyn{j: j, name: name})
}

// collDyn is a collection: methods, or a sub-collection (db.a.b).
type collDyn struct {
	j      *jsShell
	db, nm string
}

// jsCollMethods are the collection methods (cursor ones apart).
var jsCollMethods = map[string]bool{
	"findOne": true, "countDocuments": true, "estimatedDocumentCount": true, "count": true, "distinct": true,
	"insertOne": true, "insertMany": true, "insert": true, "updateOne": true, "updateMany": true, "update": true,
	"replaceOne": true, "deleteOne": true, "deleteMany": true, "remove": true, "findOneAndUpdate": true,
	"findOneAndReplace": true, "findOneAndDelete": true, "findAndModify": true, "bulkWrite": true,
	"createIndex": true, "createIndexes": true, "ensureIndex": true, "dropIndex": true, "dropIndexes": true,
	"getIndexes": true, "indexes": true, "drop": true, "renameCollection": true, "stats": true, "validate": true,
}

func (c *collDyn) Get(key string) goja.Value {
	j := c.j
	switch {
	case key == "find" || key == "aggregate":
		return j.r.ToValue(func(fc goja.FunctionCall) goja.Value {
			a := j.args(fc)
			if key == "aggregate" && len(a) > 0 {
				if _, isArr := a[0].(bson.A); !isArr {
					a = []any{bson.A(a)} // stages as arguments
				}
			}
			st := &statement{kind: "coll", dbName: c.db, coll: c.nm, calls: []call{{name: key, args: a}}}
			return j.cursorObject(&jsCursor{st: st})
		})
	case key == "getName":
		return j.r.ToValue(func() string { return c.nm })
	case key == "getFullName" || key == "toString":
		return j.r.ToValue(func() string { return j.s.dbName(c.db) + "." + c.nm })
	case jsCollMethods[key]:
		return j.r.ToValue(func(fc goja.FunctionCall) goja.Value {
			st := &statement{kind: "coll", dbName: c.db, coll: c.nm, calls: []call{{name: key, args: j.args(fc)}}}
			return j.value(key, j.op(st))
		})
	}
	return j.collObject(c.db, c.nm+"."+key)
}

func (c *collDyn) Set(string, goja.Value) bool { return false }
func (c *collDyn) Has(string) bool             { return true }
func (c *collDyn) Delete(string) bool          { return false }
func (c *collDyn) Keys() []string              { return nil }

func (j *jsShell) collObject(dbName, name string) *goja.Object {
	return j.r.NewDynamicObject(&collDyn{j: j, db: dbName, nm: name})
}

// value converts the result of an operation for scripts.
func (j *jsShell) value(method string, res *result) goja.Value {
	defer res.Close()
	if res.info != nil {
		return j.toJS(res.info)
	}
	if res.noRows {
		return goja.Undefined()
	}
	docs := j.docs(res)
	first := func() any {
		if len(docs) == 0 {
			return nil
		}
		return docs[0]
	}
	switch method {
	case "findOne", "findOneAndUpdate", "findOneAndReplace", "findOneAndDelete", "findAndModify",
		"runCommand", "adminCommand", "stats", "serverStatus", "hello", "isMaster", "hostInfo", "validate":
		return j.toJS(first())
	case "countDocuments", "estimatedDocumentCount", "count":
		if d, ok := first().(bson.D); ok {
			return j.toJS(fieldOr(d, "count"))
		}
		return j.r.ToValue(0)
	case "version":
		if d, ok := first().(bson.D); ok {
			return j.toJS(fieldOr(d, "version"))
		}
	case "distinct":
		a := bson.A{}
		for _, d := range docs {
			if len(d) > 0 {
				a = append(a, d[0].Value)
			}
		}
		return j.toJS(a)
	case "getCollectionNames":
		a := bson.A{}
		for _, d := range docs {
			a = append(a, fieldOr(d, "name"))
		}
		return j.toJS(a)
	}
	a := make(bson.A, len(docs))
	for i, d := range docs {
		a[i] = d
	}
	return j.toJS(a)
}

// cursorObject is the script object of a cursor.
func (j *jsShell) cursorObject(c *jsCursor) goja.Value {
	r := j.r
	o := r.NewObject()
	j.cursors[o] = c
	chain := func(name string) func(goja.FunctionCall) goja.Value {
		return func(fc goja.FunctionCall) goja.Value {
			if c.res != nil {
				panic(r.NewTypeError("the cursor is already being read"))
			}
			c.st.calls = append(c.st.calls, call{name: name, args: j.args(fc)})
			return o
		}
	}
	for _, m := range []string{"sort", "limit", "skip", "projection", "project", "hint", "collation", "comment", "min", "max",
		"batchSize", "maxTimeMS", "allowDiskUse", "readConcern", "let"} {
		_ = o.Set(m, chain(m))
	}
	_ = o.Set("pretty", func() goja.Value { return o })
	open := func() *result {
		if c.res == nil {
			c.res = j.op(c.st)
		}
		return c.res
	}
	next := func() goja.Value {
		d, ok, err := open().next(j.ctx)
		if err != nil {
			panic(r.NewGoError(mapError(err)))
		}
		if !ok {
			return goja.Null()
		}
		return j.toJS(d)
	}
	_ = o.Set("next", next)
	_ = o.Set("tryNext", next)
	_ = o.Set("hasNext", func() bool {
		ok, err := open().hasNext(j.ctx)
		if err != nil {
			panic(r.NewGoError(mapError(err)))
		}
		return ok
	})
	_ = o.Set("toArray", func() goja.Value {
		docs := j.docs(open())
		items := make([]any, len(docs))
		for i, d := range docs {
			items[i] = j.toJS(d)
		}
		return r.NewArray(items...)
	})
	each := func(fn goja.Value, collect bool) goja.Value {
		f, ok := goja.AssertFunction(fn)
		if !ok {
			panic(r.NewTypeError("a function is needed"))
		}
		res := open()
		var out []any
		for {
			d, ok, err := res.next(j.ctx)
			if err != nil {
				panic(r.NewGoError(mapError(err)))
			}
			if !ok {
				break
			}
			v, err := f(goja.Undefined(), j.toJS(d))
			if err != nil {
				panic(err)
			}
			if collect {
				out = append(out, v)
			} else if v != nil && v.Export() == false {
				break // forEach stops when the callback returns false
			}
		}
		if collect {
			return r.NewArray(out...)
		}
		return goja.Undefined()
	}
	_ = o.Set("forEach", func(fn goja.Value) goja.Value { return each(fn, false) })
	_ = o.Set("map", func(fn goja.Value) goja.Value { return each(fn, true) })
	count := func() int64 {
		st := *c.st
		st.calls = append(append([]call{}, c.st.calls...), call{name: "count"})
		res := j.op(&st)
		defer res.Close()
		if d := j.docs(res); len(d) > 0 {
			n, _ := argInt(fieldOr(d[0], "count"))
			return n
		}
		return 0
	}
	_ = o.Set("count", count)
	_ = o.Set("itcount", count)
	_ = o.Set("size", count)
	_ = o.Set("explain", func(fc goja.FunctionCall) goja.Value {
		st := *c.st
		st.calls = append(append([]call{}, c.st.calls...), call{name: "explain", args: j.args(fc)})
		res := j.op(&st)
		defer res.Close()
		d := j.docs(res)
		if len(d) == 0 {
			return goja.Null()
		}
		return j.toJS(d[0])
	})
	_ = o.Set("close", func() {
		if c.res != nil {
			c.res.Close()
		}
	})
	return o
}

// toResult turns the value of a script into a console result: a cursor
// streams its documents, documents and arrays are rows, other values one
// "value" row; undefined shows the printed output only.
func (j *jsShell) toResult(ctx context.Context, v goja.Value) (*result, error) {
	if v == nil || goja.IsUndefined(v) {
		return written(-1), nil
	}
	if o, ok := v.(*goja.Object); ok {
		if c, isCursor := j.cursors[o]; isCursor {
			if c.res != nil {
				// partly read: the remaining documents
				res := c.res
				return cursorRest(ctx, res)
			}
			if err := j.gate(c.st); err != nil {
				return nil, err
			}
			return j.s.run(ctx, c.st)
		}
		if _, isFn := goja.AssertFunction(v); isFn {
			return written(-1, "[function]"), nil
		}
	}
	b, err := j.toBSON(v)
	if err != nil {
		return nil, err
	}
	switch x := b.(type) {
	case bson.D:
		return docs([]bson.D{x}), nil
	case bson.A:
		var ds []bson.D
		for _, e := range x {
			d, isDoc := e.(bson.D)
			if !isDoc {
				d = bson.D{{Key: "value", Value: e}}
			}
			ds = append(ds, d)
		}
		return docs(ds), nil
	}
	return docs([]bson.D{{{Key: "value", Value: b}}}), nil
}

// cursorRest is a result over the documents left in a partly read one.
func cursorRest(ctx context.Context, res *result) (*result, error) {
	var ds []bson.D
	for len(ds) < prefetch {
		d, ok, err := res.next(ctx)
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		ds = append(ds, d)
	}
	out := docs(ds)
	out.cur, res.cur = res.cur, nil
	return out, nil
}
