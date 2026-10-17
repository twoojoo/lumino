package mongo

import (
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/db/sqlx"
)

func TestShellLiterals(t *testing.T) {
	v, err := parseValue(`{a: 1, 'b': "x\ny", "c.d": -2.5, e: [true, null, undefined], big: 3000000000,
		id: ObjectId("507f1f77bcf86cd799439011"), d: ISODate("2024-02-29T13:14:15.123Z"), l: NumberLong("9"),
		n: NumberInt(3), dec: NumberDecimal("1.10"), u: UUID("8f3e1c2a-4b5d-4e6f-8a9b-0c1d2e3f4a5b"),
		r: /ab+c/i, ts: Timestamp(5, 1), $gt: Infinity, hex: 0x10, dbl: Double(2), // comment
		/* block */ nested: {x: {y: [1, {z: 2}]}}, trailing: 1,}`)
	if err != nil {
		t.Fatal(err)
	}
	d := v.(bson.D)
	want := map[string]any{
		"a": int32(1), "b": "x\ny", "c.d": -2.5, "big": int64(3000000000), "l": int64(9), "n": int32(3),
		"$gt": math.Inf(1), "hex": int32(16), "dbl": 2.0, "trailing": int32(1),
	}
	for k, w := range want {
		if got := fieldOr(d, k); got != w {
			t.Errorf("%s = %#v, want %#v", k, got, w)
		}
	}
	if id := fieldOr(d, "id").(bson.ObjectID); id.Hex() != "507f1f77bcf86cd799439011" {
		t.Error("ObjectId")
	}
	if dt := fieldOr(d, "d").(bson.DateTime); !dt.Time().Equal(time.Date(2024, 2, 29, 13, 14, 15, 123e6, time.UTC)) {
		t.Errorf("ISODate %v", dt.Time())
	}
	if dec := fieldOr(d, "dec").(bson.Decimal128); dec.String() != "1.10" {
		t.Errorf("decimal %s", dec)
	}
	if u := fieldOr(d, "u").(bson.Binary); u.Subtype != 4 || len(u.Data) != 16 {
		t.Errorf("uuid %+v", u)
	}
	if r := fieldOr(d, "r").(bson.Regex); r.Pattern != "ab+c" || r.Options != "i" {
		t.Errorf("regex %+v", r)
	}
	if ts := fieldOr(d, "ts").(bson.Timestamp); ts.T != 5 || ts.I != 1 {
		t.Errorf("timestamp %+v", ts)
	}
	if e := fieldOr(d, "e").(bson.A); len(e) != 3 || e[0] != true || e[1] != nil || e[2] != (bson.Undefined{}) {
		t.Errorf("array %#v", e)
	}
	for _, bad := range []string{`{a: }`, `{a: 1`, `{a: foo}`, `{a: ObjectId("zz")}`, `[1, 2`, `{a: 1} x`} {
		if _, err := parseValue(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	// a value written back reads with the same type
	for _, lit := range []string{shellLit(d), shellLit(bson.D{{Key: "x", Value: 3.0}, {Key: "y", Value: int64(2)}})} {
		v2, err := parseValue(lit)
		if err != nil {
			t.Fatalf("%s: %v", lit, err)
		}
		if shellLit(v2) != lit {
			t.Errorf("round trip:\n%s\n%s", lit, shellLit(v2))
		}
	}
}

func TestShellStatements(t *testing.T) {
	cases := []struct {
		src, kind, coll, method string
		calls                   int
	}{
		{`db.users.find({age: {$gt: 1}}).sort({name: 1}).limit(5)`, "coll", "users", "find", 3},
		{`db.getCollection("my coll").find()`, "coll", "my coll", "find", 1},
		{`db["a-b"].insertOne({x: 1});`, "coll", "a-b", "insertOne", 1},
		{`db.system.profile.find()`, "coll", "system.profile", "find", 1},
		{`db.getSiblingDB("other").c.countDocuments({})`, "coll", "c", "countDocuments", 1},
		{`db.getCollectionNames()`, "db", "", "getCollectionNames", 1},
		{`db.runCommand({ping: 1})`, "db", "", "runCommand", 1},
		{`await db.c.drop()`, "coll", "c", "drop", 1},
	}
	for _, c := range cases {
		st, err := parseStatement(c.src)
		if err != nil {
			t.Errorf("%s: %v", c.src, err)
			continue
		}
		if st.kind != c.kind || st.coll != c.coll || st.method() != c.method || len(st.calls) != c.calls {
			t.Errorf("%s: %+v", c.src, st)
		}
	}
	if st, err := parseStatement("db.getSiblingDB('other').c.find()"); err != nil || st.dbName != "other" {
		t.Errorf("sibling: %+v %v", st, err)
	}
	if st, err := parseStatement("use shop"); err != nil || st.kind != "use" || st.arg != "shop" {
		t.Errorf("use: %+v %v", st, err)
	}
	for _, bad := range []string{"", "select 1", "db.c", "db.c.find(", "db.c.find() x", "db.c.find().length", "show"} {
		if _, err := parseStatement(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	_, err := parseStatement("db.c.find({a: })")
	pe, ok := err.(*parseError)
	if !ok || pe.pos != strings.Index("db.c.find({a: })", "}") {
		t.Errorf("error position: %#v", err)
	}
}

func TestShellSplitAndLex(t *testing.T) {
	src := "db.a.find({\n  x: 1\n})\n  .sort({x: -1})\n// note; here\ndb.b.drop(); db.c.find({s: 'a;b'})\nuse x"
	st := split(src)
	var got []string
	for _, s := range st {
		got = append(got, s.Text)
	}
	want := []string{"db.a.find({\n  x: 1\n})\n  .sort({x: -1})", "db.b.drop()", "db.c.find({s: 'a;b'})", "use x"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("split:\n%q", got)
	}
	for _, s := range st {
		if src[s.Start:s.End] != s.Text {
			t.Errorf("offsets of %q", s.Text)
		}
	}
	toks, state := lexLine(`db.c.find({$gt: "s"}) /* open`, lexState{})
	if !state.comment {
		t.Fatal("block comment state")
	}
	kinds := map[sqlx.TokKind]bool{}
	for _, tk := range toks {
		kinds[tk.Kind] = true
	}
	for _, k := range []sqlx.TokKind{sqlx.TWord, sqlx.TParam, sqlx.TString, sqlx.TPunct, sqlx.TComment} {
		if !kinds[k] {
			t.Errorf("missing token kind %v in %+v", k, toks)
		}
	}
	if toks, st := lexLine("still */ db", lexState{comment: true}); st.comment || toks[0].Kind != sqlx.TComment {
		t.Errorf("comment end: %+v", toks)
	}
	// a division is not a regex
	toks, _ = lexLine("a / 2 / 3", lexState{})
	for _, tk := range toks {
		if tk.Kind == sqlx.TString {
			t.Errorf("division lexed as regex: %+v", toks)
		}
	}
}

func TestClassify(t *testing.T) {
	l := &language{}
	cases := map[string]db.StmtKind{
		"db.c.find({})":                                                        db.StmtRead,
		"db.c.find().explain()":                                                db.StmtRead,
		"db.c.aggregate([{$match: {}}])":                                       db.StmtRead,
		"db.c.aggregate([{$match: {}}, {$out: 'x'}])":                          db.StmtDestructive,
		"db.c.aggregate([{$merge: {into: 'x'}}])":                              db.StmtWrite,
		"db.c.insertOne({a: 1})":                                               db.StmtWrite,
		"db.c.updateOne({a: 1}, {$set: {b: 2}})":                               db.StmtWrite,
		"db.c.updateMany({}, {$set: {b: 2}})":                                  db.StmtDestructive,
		"db.c.deleteMany({a: 1})":                                              db.StmtWrite,
		"db.c.deleteMany({})":                                                  db.StmtDestructive,
		"db.c.drop()":                                                          db.StmtDestructive,
		"db.c.createIndex({a: 1})":                                             db.StmtDDL,
		"db.createCollection('x')":                                             db.StmtDDL,
		"db.dropDatabase()":                                                    db.StmtDestructive,
		"db.runCommand({ping: 1})":                                             db.StmtRead,
		"db.runCommand({drop: 'c'})":                                           db.StmtDestructive,
		"db.runCommand({delete: 'c', deletes: [{q: {}, limit: 0}]})":           db.StmtDestructive,
		"db.runCommand({insert: 'c', documents: []})":                          db.StmtWrite,
		"db.runCommand({aggregate: 'c', pipeline: [{$out: 'x'}], cursor: {}})": db.StmtDestructive,
		"use x":                           db.StmtSession,
		"show dbs":                        db.StmtRead,
		"db.c.mystery()":                  db.StmtWrite,
		"this is not a statement":         db.StmtWrite,
		"db.c.find({$where: 'sleep(1)'})": db.StmtRead,
	}
	for src, want := range cases {
		if got := l.Classify(src); got != want {
			t.Errorf("%s: %v, want %v", src, got, want)
		}
	}
}

func TestLanguage(t *testing.T) {
	l := &language{}
	ref := db.ObjectRef{Schema: "shop", Name: "orders", Type: db.ObjCollection}
	q, err := l.Browse(ref, db.BrowseQuery{Filter: "{status: 'A'}", Sort: []db.SortKey{{Field: "_id"}, {Field: "total", Desc: true}}})
	if err != nil || q != `db.orders.find({status: 'A'}).sort({_id: 1, total: -1})` {
		t.Fatalf("browse %q %v", q, err)
	}
	if q, _ := l.Browse(db.ObjectRef{Name: "find"}, db.BrowseQuery{}); q != `db.getCollection("find").find({})` {
		t.Errorf("shadowing name: %q", q)
	}
	if q, _ := l.Count(ref, ""); q != "db.orders.countDocuments({})" {
		t.Errorf("count %q", q)
	}
	if err := l.ValidateFilter("status = 1"); err == nil {
		t.Error("SQL filter accepted")
	}
	if err := l.ValidateFilter("{a: 1}; db.c.drop()"); err == nil {
		t.Error("two statements accepted")
	}
	if got := l.EqualsFilter("_id", "507f1f77bcf86cd799439011", db.KindObjectID); got != `{_id: ObjectId("507f1f77bcf86cd799439011")}` {
		t.Errorf("equals: %s", got)
	}
	if got := l.EqualsFilter("x", nil, db.KindText); got != "{x: null}" {
		t.Errorf("equals null: %s", got)
	}
	if s, verb, ok := l.DropStatement(ref, true); !ok || s != "db.orders.deleteMany({})" || verb == "" {
		t.Errorf("truncate %q", s)
	}
	if l.Classify(l.OpenTemplate(ref)) != db.StmtRead {
		t.Error("open template not a read")
	}
	ch := db.Change{Op: db.OpUpdate, Key: []db.ColValue{{Column: "_id", Kind: db.KindObjectID, Value: "507f1f77bcf86cd799439011"}},
		Values: []db.ColValue{{Column: "n", Kind: db.KindInt, Value: int64(5)}, {Column: "x", Kind: db.KindText, Value: nil}}}
	if got := l.RenderChange(ref, ch); got != `db.orders.updateOne({_id: ObjectId("507f1f77bcf86cd799439011")}, {$set: {n: 5, x: null}})` {
		t.Errorf("render: %s", got)
	}
	ins := l.InsertText(ref, []db.Column{{Name: "_id", Kind: db.KindObjectID}, {Name: "when", Kind: db.KindTimestampTZ}, {Name: "gone", Kind: db.KindText}},
		db.Row{"507f1f77bcf86cd799439011", time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC), nil})
	if ins != `db.orders.insertOne({_id: ObjectId("507f1f77bcf86cd799439011"), when: ISODate("2024-01-02T03:04:05.000Z")});` {
		t.Errorf("insert: %s", ins)
	}
	if q, err := l.Explain("db.c.find({a: 1});"); err != nil || q != `db.c.find({a: 1}).explain("queryPlanner")` {
		t.Errorf("explain %q %v", q, err)
	}
	if _, err := l.Explain("db.c.drop()"); err == nil {
		t.Error("explain of drop")
	}
	names := l.ColumnNames(`db.users.find({password: 1, "profile.token": 1}, {"$secret": 1})`)
	for _, w := range []string{"password", "token", "secret", "profile"} {
		if !contains(names, w) {
			t.Errorf("column names %v miss %s", names, w)
		}
	}
	c := l.CompletionContext("db.", 3)
	if !c.ExpectObject {
		t.Errorf("db. context %+v", c)
	}
	c = l.CompletionContext(`db.orders.find({sta`, 19)
	if c.Prefix != "sta" || len(c.Refs) != 1 || c.Refs[0].Name != "orders" || c.ExpectObject {
		t.Errorf("field context %+v", c)
	}
	c = l.CompletionContext(`db.getCollection("my c").find({x`, 32)
	if len(c.Refs) != 1 || c.Refs[0].Name != "my c" {
		t.Errorf("getCollection context %+v", c)
	}
	if l.Terminate("db.c.find()") != "db.c.find();" || l.LineComment() != "//" {
		t.Error("terminate/comment")
	}
}

func TestValues(t *testing.T) {
	id := bson.NewObjectID()
	now := time.Date(2024, 2, 29, 13, 14, 15, 123e6, time.UTC)
	dec, _ := bson.ParseDecimal128("12.50")
	doc := bson.D{{Key: "_id", Value: id}, {Key: "i", Value: int32(1)}, {Key: "l", Value: int64(2)}, {Key: "f", Value: 1.5},
		{Key: "dec", Value: dec}, {Key: "d", Value: bson.NewDateTimeFromTime(now)}, {Key: "b", Value: bson.Binary{Data: []byte{1, 2}}},
		{Key: "u", Value: bson.Binary{Subtype: 4, Data: make([]byte, 16)}},
		{Key: "o", Value: bson.D{{Key: "x", Value: int64(7)}, {Key: "id", Value: id}, {Key: "a", Value: bson.A{int32(1), "s"}}}}}
	// o is a document in one and a string in the other: one mixed column
	docs := []bson.D{doc, {{Key: "_id", Value: "str"}, {Key: "i", Value: 2.5}, {Key: "extra", Value: true}, {Key: "o", Value: "plain"}}}
	cols, _ := inferColumns(docs)
	kinds := map[string]db.Kind{}
	for _, c := range cols {
		kinds[c.Name] = c.Kind
	}
	if cols[0].Name != "_id" || kinds["_id"] != db.KindOther || kinds["i"] != db.KindFloat || kinds["l"] != db.KindInt ||
		kinds["dec"] != db.KindDecimal || kinds["d"] != db.KindTimestampTZ || kinds["o"] != db.KindOther || kinds["u"] != db.KindUUID {
		t.Fatalf("kinds %v", kinds)
	}
	r := row(doc, cols)
	for i, v := range r {
		switch v.(type) {
		case nil, string, int64, float64, bool, time.Time, []byte, []any, map[string]any:
		default:
			t.Errorf("%s: unnormalized %T", cols[i].Name, v)
		}
	}
	oi := slices.IndexFunc(cols, func(c db.Column) bool { return c.Name == "o" })
	o := r[oi].(map[string]any)
	if o["x"].(map[string]any)["$numberLong"] != "7" || o["id"].(map[string]any)["$oid"] != id.Hex() {
		t.Errorf("nested %v", o)
	}
	// nested values go back with their types
	back, err := toBSON(o, db.KindJSON, nil)
	if err != nil {
		t.Fatal(err)
	}
	bd := back.(bson.D)
	if fieldOr(bd, "x") != int64(7) || fieldOr(bd, "id") != id {
		t.Errorf("back %#v", bd)
	}
	if a := fieldOr(bd, "a").(bson.A); a[0] != int32(1) || a[1] != "s" {
		t.Errorf("array back %#v", a)
	}
	// edited JSON text with wrappers
	v, err := toBSON(`{"when": {"$date": "2024-01-01T00:00:00Z"}, "n": {"$numberDecimal": "1.5"}}`, db.KindJSON, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := fieldOr(v.(bson.D), "when").(bson.DateTime); !ok {
		t.Errorf("$date: %#v", v)
	}
	// types of the stored value are kept
	if v, _ := toBSON(int64(5), db.KindInt, int64(1)); v != int64(5) {
		t.Errorf("long kept: %#v", v)
	}
	if v, _ := toBSON(int64(5), db.KindInt, 1.5); v != 5.0 {
		t.Errorf("double kept: %#v", v)
	}
	if v, _ := toBSON(int64(5), db.KindInt, nil); v != int32(5) {
		t.Errorf("default int: %#v", v)
	}
	if v, _ := toBSON("1.25", db.KindDecimal, nil); v.(bson.Decimal128).String() != "1.25" {
		t.Errorf("decimal: %#v", v)
	}
	if _, err := toBSON("nothex", db.KindObjectID, nil); err == nil {
		t.Error("bad ObjectId accepted")
	}
	if v, _ := toBSON(`{"$regex": "a", "$options": "i"}`, db.KindJSON, nil); fieldOr(v.(bson.D), "$regex") != "a" {
		t.Errorf("query $regex kept as operator: %#v", v)
	}
}

func TestURL(t *testing.T) {
	d := Driver{}
	cfg, ok, err := d.ParseURL("mongodb://u%40x:p%3Aw@h1:27018/shop?authSource=admin&replicaSet=rs0&tls=true&tlsCAFile=/ca.pem")
	if !ok || err != nil || cfg.Host != "h1" || cfg.Port != 27018 || cfg.User != "u@x" || cfg.Password != "p:w" || cfg.Database != "shop" ||
		cfg.Param("authSource") != "admin" || cfg.Param("replicaSet") != "rs0" || cfg.TLS.Mode != "verify-full" || cfg.TLS.CAFile != "/ca.pem" {
		t.Fatalf("%+v %v", cfg, err)
	}
	cfg, _, err = d.ParseURL("mongodb+srv://cluster0.example.net/?retryWrites=true")
	if err != nil || cfg.Param("srv") != "true" || cfg.Host != "cluster0.example.net" || cfg.TLS.Mode != "verify-full" {
		t.Fatalf("srv %+v %v", cfg, err)
	}
	cfg, _, _ = d.ParseURL("mongodb://a:1,b:2/")
	if cfg.Host != "a:1,b:2" || cfg.Port != 0 {
		t.Fatalf("hosts %+v", cfg)
	}
	if _, ok, _ := d.ParseURL("postgres://x"); ok {
		t.Fatal("foreign scheme")
	}
	u, err := buildURI(&db.Config{Host: "127.0.0.1", Port: 55017, Database: "shop", Params: map[string]string{"authSource": "admin", "tls": "true"}}, 27017)
	if err != nil || u != "mongodb://127.0.0.1:55017/shop?authSource=admin&directConnection=true" {
		t.Fatalf("uri %q %v", u, err)
	}
	u, _ = buildURI(&db.Config{Host: "a, b:2", Port: 3}, 27017)
	if u != "mongodb://a:3,b:2/" {
		t.Fatalf("multi %q", u)
	}
	if _, err := buildURI(&db.Config{Host: "127.0.0.1", Params: map[string]string{"srv": "true"}}, 27017); err == nil {
		t.Fatal("srv with an IP accepted")
	}
}

func TestFlatten(t *testing.T) {
	docs := []bson.D{
		{{Key: "_id", Value: int32(1)}, {Key: "address", Value: bson.D{{Key: "city", Value: "London"}, {Key: "geo", Value: bson.D{{Key: "lat", Value: 1.5}}}}},
			{Key: "tags", Value: bson.A{"a"}}, {Key: "odd", Value: bson.D{{Key: "a.b", Value: 1}}}, {Key: "empty", Value: bson.D{}}},
		{{Key: "_id", Value: int32(2)}, {Key: "address", Value: bson.D{{Key: "zip", Value: "N1"}}}, {Key: "address2", Value: nil}},
	}
	cols, _ := inferColumns(docs)
	var names []string
	for _, c := range cols {
		names = append(names, c.Name)
	}
	if got := strings.Join(names, ","); got != "_id,address.city,address.geo.lat,address.zip,tags,odd,empty,address2" {
		t.Fatalf("columns %s", got)
	}
	r := row(docs[0], cols)
	if r[1] != "London" || r[2] != 1.5 || r[3] != nil {
		t.Fatalf("row %v", r)
	}
	if v, _ := lookup(docs[0], "odd"); v == nil {
		t.Fatal("top-level lookup")
	}
	d := setPath(bson.D{{Key: "a", Value: bson.D{{Key: "x", Value: int32(1)}}}}, "a.y.z", "v")
	if shellLit(d) != `{a: {x: 1, y: {z: "v"}}}` {
		t.Fatalf("setPath %s", shellLit(d))
	}
	n := docNode("", docs[0])
	if !n.IsObject() || n.Children[1].Name != "address" || len(n.Children[1].Children) != 2 || n.Children[0].Lit != "1" || n.Children[0].Value != int64(1) {
		t.Fatalf("doc node %+v", n)
	}
	if tg := n.Children[2]; !tg.IsArray() || tg.Children[0].Name != "0" || tg.Children[0].Lit != `"a"` {
		t.Fatalf("array node %+v", tg)
	}
}

func TestDocumentEditing(t *testing.T) {
	l := &language{}
	orig := docNode("", bson.D{{Key: "_id", Value: bson.NewObjectID()}, {Key: "n", Value: int64(5)}, {Key: "a", Value: bson.D{{Key: "x", Value: int32(1)}, {Key: "y", Value: "k"}}},
		{Key: "arr", Value: bson.A{int32(1), int32(2)}}, {Key: "gone", Value: true}, {Key: "d", Value: bson.DateTime(0)}})
	text := l.FormatDocument(orig)
	if !strings.Contains(text, "n: NumberLong(\"5\")") || !strings.Contains(text, "\n  a: {\n    x: 1,") || !strings.Contains(text, "arr: [1, 2]") {
		t.Fatalf("format:\n%s", text)
	}
	key, vals, err := l.DocumentChanges(orig, text)
	if err != nil || len(vals) != 0 || key[0].Column != "_id" {
		t.Fatalf("no change: %v %v", vals, err)
	}
	edited := strings.Replace(text, "x: 1", "x: 2", 1)
	edited = strings.Replace(edited, "  gone: true,\n", "", 1)
	edited = strings.Replace(edited, "arr: [1, 2]", "arr: [1, 2, 3], added: {z: 1}", 1)
	_, vals, err = l.DocumentChanges(orig, edited)
	if err != nil {
		t.Fatalf("%v\n%s", err, edited)
	}
	got := map[string]any{}
	for _, v := range vals {
		got[v.Column] = v.Value
	}
	if got["a.x"] != db.Raw("2") || got["arr"] != db.Raw("[1, 2, 3]") || got["added"] != db.Raw("{z: 1}") || got["gone"] != db.Unset || len(got) != 4 {
		t.Fatalf("changes %v", got)
	}
	upd, err := updateDoc(vals, noLike)
	if err != nil || shellLit(upd) != `{$set: {"a.x": 2, arr: [1, 2, 3], added: {z: 1}}, $unset: {gone: ""}}` {
		t.Fatalf("update %s %v", shellLit(upd), err)
	}
	if _, _, err := l.DocumentChanges(orig, strings.Replace(text, "ObjectId(", "String(", 1)); err == nil {
		t.Fatal("bad text accepted")
	}
	id := orig.Children[0].Lit
	if _, _, err := l.DocumentChanges(orig, strings.Replace(text, id, `"other"`, 1)); err == nil {
		t.Fatal("_id change accepted")
	}
	nd, err := l.NewDocument(`{name: "x", when: ISODate("2024-01-01T00:00:00Z")}`)
	if err != nil || len(nd) != 2 || nd[1].Value != db.Raw(`ISODate("2024-01-01T00:00:00.000Z")`) {
		t.Fatalf("new document %v %v", nd, err)
	}
	ins, err := insertDoc([]db.ColValue{{Column: "a.b", Kind: db.KindInt, Value: int64(1)}, {Column: "c", Value: nil}, {Column: "d", Kind: db.KindJSON, Value: db.Raw("{e: 1}")}}, noLike)
	if err != nil || shellLit(ins) != "{a: {b: 1}, d: {e: 1}}" {
		t.Fatalf("insert %s %v", shellLit(ins), err)
	}
	if s, err := l.ParseSort("{a: 1, b: -1}"); err != nil || len(s) != 2 || !s[1].Desc || l.FormatSort(s) != "{a: 1, b: -1}" {
		t.Fatalf("sort %v %v", s, err)
	}
	if _, err := l.ParseSort("{a: 2}"); err == nil {
		t.Fatal("bad sort accepted")
	}
	q, err := l.Browse(db.ObjectRef{Name: "c"}, db.BrowseQuery{Filter: "{a: 1}", Projection: "{a: 1}", Sort: []db.SortKey{{Field: "a.b", Desc: true}}, Limit: 10})
	if err != nil || q != `db.c.find({a: 1}, {a: 1}).sort({"a.b": -1}).limit(10)` {
		t.Fatalf("browse %q %v", q, err)
	}
	if l.Classify(q) != db.StmtRead {
		t.Fatal("browse classification")
	}
}
