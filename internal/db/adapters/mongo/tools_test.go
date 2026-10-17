package mongo

import (
	"bytes"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/twoojoo/lumino/internal/db"
)

func TestTranslateSQL(t *testing.T) {
	l := &language{}
	cases := map[string]string{
		"SELECT * FROM users": `db.users.find({})`,
		"select name, email from users where age > 30 and status = 'A'":                                                                              `db.users.find({age: {$gt: 30}, status: "A"}, {name: 1, email: 1, _id: 0})`,
		"SELECT * FROM users WHERE a = 1 OR (b <> 'x' AND c IS NOT NULL) ORDER BY name DESC, _id LIMIT 10 OFFSET 20":                                 `db.users.find({$or: [{a: 1}, {b: {$ne: "x"}, c: {$ne: null}}]}).sort({name: -1, _id: 1}).skip(20).limit(10)`,
		"SELECT * FROM t WHERE name LIKE 'Ad_%' AND x NOT IN (1, 2) AND d BETWEEN 1 AND 5":                                                           `db.t.find({name: RegExp("^Ad..*$", ""), x: {$nin: [1, 2]}, d: {$gte: 1, $lte: 5}})`,
		"SELECT * FROM t WHERE a = 1 AND a < 5":                                                                                                      `db.t.find({$and: [{a: 1}, {a: {$lt: 5}}]})`,
		"SELECT * FROM t WHERE NOT a = 1":                                                                                                            `db.t.find({$nor: [{a: 1}]})`,
		"SELECT COUNT(*) FROM orders WHERE status = 'paid'":                                                                                          `db.orders.countDocuments({status: "paid"})`,
		"SELECT DISTINCT status FROM orders WHERE total > 10":                                                                                        `db.orders.distinct("status", {total: {$gt: 10}})`,
		"SELECT customer, COUNT(*) AS n, SUM(total) FROM orders WHERE status = 'paid' GROUP BY customer HAVING COUNT(*) > 1 ORDER BY n DESC LIMIT 5": `db.orders.aggregate([{$match: {status: "paid"}}, {$group: {_id: "$customer", n: {$sum: 1}, sum_total: {$sum: "$total"}}}, {$match: {n: {$gt: 1}}}, {$project: {_id: 0, customer: "$_id", n: 1, sum_total: 1}}, {$sort: {n: -1}}, {$limit: NumberLong("5")}])`,
		"SELECT a.b, c, AVG(x) avg_x FROM t GROUP BY a.b, c":                                                                                         `db.t.aggregate([{$group: {_id: {a_b: "$a.b", c: "$c"}, avg_x: {$avg: "$x"}}}, {$project: {_id: 0, "a.b": "$_id.a_b", c: "$_id.c", avg_x: 1}}])`,
		"INSERT INTO users (name, address.city, age) VALUES ('Ada', 'London', 36)":                                                                   `db.users.insertOne({name: "Ada", address: {city: "London"}, age: 36})`,
		"INSERT INTO t (a) VALUES (1), (-2.5), (NULL);":                                                                                              `db.t.insertMany([{a: 1}, {a: -2.5}, {a: null}])`,
		"UPDATE users SET status = 'B', n = 2 WHERE age >= 18":                                                                                       `db.users.updateMany({age: {$gte: 18}}, {$set: {status: "B", n: 2}})`,
		"DELETE FROM users WHERE status = 'X'":                                                                                                       `db.users.deleteMany({status: "X"})`,
		`SELECT "my field" FROM "my coll"`:                                                                                                           `db.getCollection("my coll").find({}, {"my field": 1, _id: 0})`,
	}
	for sql, want := range cases {
		got, err := l.TranslateSQL(sql)
		if err != nil || got != want {
			t.Errorf("%s\n got %s (%v)\nwant %s", sql, got, err, want)
			continue
		}
		if _, err := parseStatement(got); err != nil {
			t.Errorf("%s: translation doesn't parse: %v", got, err)
		}
	}
	for _, bad := range []string{"SELECT * FROM a JOIN b ON a.x = b.y", "SELECT name, COUNT(*) FROM t", "CREATE TABLE t (a int)",
		"SELECT * FROM t WHERE", "SELECT * FROM t HAVING a > 1", "SELECT DISTINCT a, b FROM t"} {
		if got, err := l.TranslateSQL(bad); err == nil {
			t.Errorf("%s translated to %s", bad, got)
		}
	}
}

func TestAnalyzeAndSuggest(t *testing.T) {
	docs := []bson.D{
		{{Key: "_id", Value: int32(1)}, {Key: "n", Value: int32(5)}, {Key: "tags", Value: bson.A{"a", "b"}}, {Key: "o", Value: bson.D{{Key: "x", Value: "k"}}}},
		{{Key: "_id", Value: int32(2)}, {Key: "n", Value: 2.5}, {Key: "tags", Value: bson.A{"a"}}},
		{{Key: "_id", Value: int32(3)}, {Key: "n", Value: "text"}},
	}
	fs := analyze(docs)
	got := map[string]*fieldAcc{}
	var paths []string
	for _, f := range fs {
		got[f.path] = f
		paths = append(paths, f.path)
	}
	if strings.Join(paths, ",") != "_id,n,tags,tags[],o,o.x" {
		t.Fatalf("paths %v", paths)
	}
	if n := got["n"]; n.present != 3 || n.types["int"] != 1 || n.types["double"] != 1 || n.types["string"] != 1 || shellLit(n.min) != "2.5" || shellLit(n.max) != "5" {
		t.Fatalf("n: %+v", n)
	}
	if tg := got["tags[]"]; tg.values[`"a"`] != 2 || tg.present != 2 {
		t.Fatalf("tags[]: %+v", tg)
	}
	lang := &language{}
	ref := db.ObjectRef{Name: "orders"}
	find := bson.D{{Key: "find", Value: "orders"}, {Key: "filter", Value: bson.D{{Key: "total", Value: bson.D{{Key: "$gt", Value: int32(5)}}}, {Key: "status", Value: "A"}}},
		{Key: "sort", Value: bson.D{{Key: "created", Value: int32(-1)}}}}
	rep := &db.ExplainReport{Plan: db.PlanNode{Stage: "SORT", Children: []db.PlanNode{{Stage: "COLLSCAN"}}}, DocsExamined: 1000, Returned: 3}
	s := suggestIndexes(ref, find, rep, nil, lang)
	if len(s) != 1 || s[0].Keys != "{status: 1, created: -1, total: 1}" || s[0].Statement != "db.orders.createIndex({status: 1, created: -1, total: 1})" {
		t.Fatalf("suggestion %+v", s)
	}
	// an index with these keys already exists
	if s := suggestIndexes(ref, find, rep, []bson.D{{{Key: "key", Value: bson.D{{Key: "status", Value: int32(1)}, {Key: "created", Value: int32(-1)}, {Key: "total", Value: int32(1)}, {Key: "x", Value: int32(1)}}}}}, lang); s != nil {
		t.Fatalf("existing index: %+v", s)
	}
	// a selective index scan needs nothing
	ok := &db.ExplainReport{Plan: db.PlanNode{Stage: "FETCH", Children: []db.PlanNode{{Stage: "IXSCAN"}}}, DocsExamined: 3, Returned: 3}
	if s := suggestIndexes(ref, find, ok, nil, lang); s != nil {
		t.Fatalf("no suggestion expected: %+v", s)
	}
	n := planNode(bson.D{{Key: "stage", Value: "FETCH"}, {Key: "nReturned", Value: int32(3)}, {Key: "inputStage", Value: bson.D{{Key: "stage", Value: "IXSCAN"},
		{Key: "indexName", Value: "a_1"}, {Key: "keyPattern", Value: bson.D{{Key: "a", Value: int32(1)}}}}}})
	if n.Stage != "FETCH" || n.NReturned != 3 || len(n.Children) != 1 || n.Children[0].Index != "a_1" || !strings.Contains(n.Children[0].Details, "keyPattern: {a: 1}") {
		t.Fatalf("plan %+v", n)
	}
}

func TestImportReaders(t *testing.T) {
	var got []bson.D
	emit := func(d bson.D) error { got = append(got, d); return nil }
	if err := readJSON(strings.NewReader(` [{"_id": {"$oid": "507f1f77bcf86cd799439011"}, "n": {"$numberLong": "5"}}, {"a": 1.5}]`), emit); err != nil {
		t.Fatal(err)
	}
	if err := readJSON(strings.NewReader("{\"x\": 1}\n{\"x\": {\"$date\": \"2024-01-01T00:00:00Z\"}}\n"), emit); err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || shellLit(got[0]) != `{_id: ObjectId("507f1f77bcf86cd799439011"), n: NumberLong("5")}` || shellLit(got[3]) != `{x: ISODate("2024-01-01T00:00:00.000Z")}` {
		t.Fatalf("json %v", got)
	}
	got = nil
	if err := readCSV(strings.NewReader("\ufeffname,address.city,age,ok,when\nAda,London,36,true,2024-01-02\nBob,,x,false,\n"), true, emit); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || shellLit(got[0]) != `{name: "Ada", address: {city: "London"}, age: 36, ok: true, when: ISODate("2024-01-02T00:00:00.000Z")}` ||
		shellLit(got[1]) != `{name: "Bob", age: "x", ok: false}` {
		t.Fatalf("csv %v", got)
	}
	got = nil
	var buf bytes.Buffer
	for _, d := range []bson.D{{{Key: "a", Value: int32(1)}}, {{Key: "b", Value: "x"}}} {
		b, _ := bson.Marshal(d)
		buf.Write(b)
	}
	if err := readBSON(&buf, emit); err != nil || len(got) != 2 || shellLit(got[1]) != `{b: "x"}` {
		t.Fatalf("bson %v %v", got, err)
	}
	if err := readBSON(strings.NewReader("not bson at all"), emit); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestAdminStatements(t *testing.T) {
	l := &language{}
	coll := db.ObjectRef{Schema: "shop", Name: "orders"}
	cases := []struct {
		op   string
		ref  db.ObjectRef
		args []string
		want string
		kind db.StmtKind
	}{
		{"createIndex", coll, []string{"{status: 1}", "{unique: true}"}, `db.getSiblingDB("shop").getCollection("orders").createIndex({status: 1}, {unique: true})`, db.StmtDDL},
		{"dropIndex", coll, []string{"status_1"}, `db.getSiblingDB("shop").getCollection("orders").dropIndex("status_1")`, db.StmtDDL},
		{"setValidator", coll, []string{"{$jsonSchema: {required: [\"a\"]}}", "moderate", "warn"}, `db.getSiblingDB("shop").runCommand({collMod: "orders", validator: {$jsonSchema: {required: ["a"]}}, validationLevel: "moderate", validationAction: "warn"})`, db.StmtDDL},
		{"listUsers", db.ObjectRef{Schema: "shop"}, nil, `db.getSiblingDB("shop").runCommand({usersInfo: 1})`, db.StmtRead},
		{"createUser", db.ObjectRef{Schema: "shop"}, []string{"ada", "s3cret", `["readWrite"]`}, `db.getSiblingDB("shop").runCommand({createUser: "ada", pwd: "s3cret", roles: ["readWrite"]})`, db.StmtWrite},
		{"dropUser", db.ObjectRef{Schema: "shop"}, []string{"ada"}, `db.getSiblingDB("shop").runCommand({dropUser: "ada"})`, db.StmtWrite},
		{"grantRoles", db.ObjectRef{Schema: "shop"}, []string{"ada", `[{role: "read", db: "reports"}]`}, `db.getSiblingDB("shop").runCommand({grantRolesToUser: "ada", roles: [{role: "read", db: "reports"}]})`, db.StmtWrite},
		{"killOp", db.ObjectRef{}, []string{"123"}, `db.killOp(123)`, db.StmtWrite},
	}
	for _, c := range cases {
		got, err := l.AdminStatement(c.op, c.ref, c.args...)
		if err != nil || got != c.want {
			t.Errorf("%s: %s (%v)", c.op, got, err)
			continue
		}
		if k := l.Classify(got); k != c.kind {
			t.Errorf("%s classified %v", got, k)
		}
	}
	for _, bad := range [][]string{{"createIndex", "{}"}, {"createIndex", "{a: }"}, {"setValidator", "{}", "loose"}, {"createUser", "u", ""}, {"grantRoles", "u", "{}"}, {"nope"}} {
		if _, err := l.AdminStatement(bad[0], coll, bad[1:]...); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	q, err := l.Aggregate(db.ObjectRef{Name: "c"}, []string{"{$match: {a: 1}}", `{$group: {_id: "$a"}}`}, 20)
	if err != nil || q != `db.c.aggregate([{$match: {a: 1}}, {$group: {_id: "$a"}}, {$limit: 20}])` {
		t.Fatalf("aggregate %q %v", q, err)
	}
	if _, err := l.Aggregate(db.ObjectRef{Name: "c"}, []string{"{a: 1}"}, 0); err == nil {
		t.Fatal("bad stage accepted")
	}
	if len(l.StageTemplates()) < 15 {
		t.Fatal("stage templates")
	}
	for _, st := range l.StageTemplates() {
		if _, err := l.Aggregate(db.ObjectRef{Name: "c"}, []string{"{" + st[0] + ": " + st[1] + "}"}, 0); err != nil {
			t.Errorf("template %s: %v", st[0], err)
		}
	}
}
