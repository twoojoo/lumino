package mongo

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/twoojoo/lumino/internal/db"
)

func testShell() *jsShell {
	j := newJSShell(&session{database: "test"})
	j.ctx = context.Background()
	return j
}

func eval(t *testing.T, j *jsShell, src string) any {
	t.Helper()
	v, err := j.r.RunString(src)
	if err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	b, err := j.toBSON(v)
	if err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	return b
}

func TestJSValues(t *testing.T) {
	j := testShell()
	for src, want := range map[string]string{
		`({b: 1, a: [1, 2.5, "x", null], n: NumberLong("9007199254740993"), d: NumberDecimal("1.10")})`: `{b: 1, a: [1, 2.5, "x", null], n: NumberLong("9007199254740993"), d: NumberDecimal("1.10")}`,
		`ObjectId("507f1f77bcf86cd799439011")`:                                                          `ObjectId("507f1f77bcf86cd799439011")`,
		`new ObjectId("507f1f77bcf86cd799439011")`:                                                      `ObjectId("507f1f77bcf86cd799439011")`,
		`ISODate("2024-02-29T13:14:15.123Z")`:                                                           `ISODate("2024-02-29T13:14:15.123Z")`,
		`new Date(Date.UTC(2024, 0, 2))`:                                                                `ISODate("2024-01-02T00:00:00.000Z")`,
		`({u: undefined, k: 1})`:                                                                        `{k: 1}`,
		`/ab+c/i`:                                                                                       `RegExp("ab+c", "i")`,
		`UUID("8f3e1c2a-4b5d-4e6f-8a9b-0c1d2e3f4a5b")`:                                                  `UUID("8f3e1c2a-4b5d-4e6f-8a9b-0c1d2e3f4a5b")`,
		`Double(2)`:  `Double(2)`,
		`3000000000`: `NumberLong("3000000000")`,
	} {
		if got := shellLit(eval(t, j, src)); got != want {
			t.Errorf("%s = %s, want %s", src, got, want)
		}
	}
	// helpers are usable in JavaScript
	if got := eval(t, j, `ObjectId("507f1f77bcf86cd799439011").toString()`); got != "507f1f77bcf86cd799439011" {
		t.Errorf("toString %v", got)
	}
	if got := eval(t, j, `ObjectId("507f1f77bcf86cd799439011").equals(ObjectId("507f1f77bcf86cd799439011"))`); got != true {
		t.Errorf("equals %v", got)
	}
	if got := eval(t, j, `NumberLong("5") * 2`); got != int32(10) {
		t.Errorf("long arithmetic %#v", got)
	}
	if got := eval(t, j, `"id " + ObjectId("507f1f77bcf86cd799439011")`); got != "id 507f1f77bcf86cd799439011" {
		t.Errorf("concatenation %#v", got)
	}
	// BSON values come back with their types
	id := bson.NewObjectID()
	dec, _ := bson.ParseDecimal128("2.50")
	doc := bson.D{{Key: "_id", Value: id}, {Key: "n", Value: int64(1 << 60)}, {Key: "small", Value: int64(5)}, {Key: "d", Value: dec},
		{Key: "when", Value: bson.NewDateTimeFromTime(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC))}, {Key: "arr", Value: bson.A{int32(1), "x"}}}
	if err := j.r.Set("doc", j.toJS(doc)); err != nil {
		t.Fatal(err)
	}
	if got := shellLit(eval(t, j, "doc")); got != shellLit(bson.D{{Key: "_id", Value: id}, {Key: "n", Value: int64(1 << 60)},
		{Key: "small", Value: int32(5)}, {Key: "d", Value: dec}, {Key: "when", Value: doc[4].Value}, {Key: "arr", Value: doc[5].Value}}) {
		t.Errorf("round trip %s", got)
	}
	if got := eval(t, j, "doc.when.getUTCFullYear()"); got != int32(2024) {
		t.Errorf("date %v", got)
	}
	if got := eval(t, j, "doc._id.getTimestamp() instanceof Date || typeof doc._id.getTimestamp()"); got == nil {
		t.Error("getTimestamp")
	}
}

func TestJSPrintAndResults(t *testing.T) {
	j := testShell()
	res, err := j.run(context.Background(), `let total = 0; for (let i = 1; i <= 3; i++) { total += i } print("total", total, {a: 1}); total`)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.msgs) != 1 || res.msgs[0] != "total 6 {a: 1}" {
		t.Errorf("print %v", res.msgs)
	}
	rows, _, _ := res.Fetch(context.Background(), 10)
	if len(rows) != 1 || rows[0][0] != int64(6) || res.cols[0].Name != "value" {
		t.Errorf("value result %v %v", res.cols, rows)
	}
	// variables survive between statements
	res, err = j.run(context.Background(), `[{a: total}, {a: total * 2}]`)
	if err != nil {
		t.Fatal(err)
	}
	if rows, _, _ := res.Fetch(context.Background(), 10); len(rows) != 2 || rows[1][0] != int64(12) {
		t.Errorf("array result %v", rows)
	}
	if res, err := j.run(context.Background(), `const f = () => 1`); err != nil || res.Columns() != nil {
		t.Errorf("undefined result %v", err)
	}
	// errors carry their position
	_, err = j.run(context.Background(), "let x = 1\nx.y.z")
	e := db.AsError(err)
	if err == nil || e.Line != 2 || !strings.Contains(e.Msg, "TypeError") {
		t.Errorf("runtime error %+v", e)
	}
	_, err = j.run(context.Background(), "let = ;")
	if e := db.AsError(err); err == nil || !strings.HasPrefix(e.Msg, "SyntaxError") || e.Line != 1 {
		t.Errorf("syntax error %+v", e)
	}
	// cancel stops a loop
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	_, err = j.run(ctx, "while (true) {}")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancel: %v", err)
	}
	if _, err := j.run(context.Background(), "1"); err != nil {
		t.Errorf("after cancel: %v", err)
	}
}

func TestJSGate(t *testing.T) {
	j := testShell()
	ins := &statement{kind: "coll", coll: "c", calls: []call{{name: "insertOne", args: []any{bson.D{{Key: "a", Value: int32(1)}}}}}}
	find := &statement{kind: "coll", coll: "c", calls: []call{{name: "find", args: []any{bson.D{}}}}}
	drop := &statement{kind: "coll", coll: "c", calls: []call{{name: "deleteMany", args: []any{bson.D{}}}}}
	j.s.readOnly = true
	if err := j.gate(ins); err == nil || !strings.Contains(err.Error(), "db.c.insertOne({a: 1})") {
		t.Errorf("read-only: %v", err)
	}
	if err := j.gate(find); err != nil {
		t.Errorf("read: %v", err)
	}
	j.s.readOnly = false
	var seen []string
	j.s.SetGuard(func(_ context.Context, k db.StmtKind, desc string) error {
		seen = append(seen, k.String()+" "+desc)
		if k == db.StmtDestructive {
			return errors.New("not confirmed")
		}
		return nil
	})
	if err := j.gate(ins); err != nil {
		t.Error(err)
	}
	if err := j.gate(drop); err == nil {
		t.Error("guard ignored")
	}
	if j.gate(find) != nil || len(seen) != 2 || !strings.HasPrefix(seen[1], db.StmtDestructive.String()+" db.c.deleteMany({})") {
		t.Errorf("guard calls %v", seen)
	}
	// a runCommand is classified by its command
	cmd := &statement{kind: "db", calls: []call{{name: "runCommand", args: []any{bson.D{{Key: "drop", Value: "c"}}}}}}
	if err := j.gate(cmd); err == nil {
		t.Error("runCommand drop not guarded")
	}
}

func TestJSLanguage(t *testing.T) {
	l := &language{}
	for _, s := range []string{"let x = 1", "db.c.find({a: x})", "for (const d of db.c.find().toArray()) print(d)"} {
		if !l.Dynamic(s) || l.Classify(s) != db.StmtWrite {
			t.Errorf("%q: dynamic %v, %v", s, l.Dynamic(s), l.Classify(s))
		}
	}
	if l.Dynamic("db.c.find({a: 1})") || l.Dynamic("use x") || l.Dynamic("") {
		t.Error("static statements are not dynamic")
	}
	src := "let n = 0\nif (n == 0)\n  print('zero')\nelse\n  print('other')\nfor (let i = 0; i < 2; i++) {\n  n++\n}\nn--\nconst add = (a, b) =>\n  a + b\ndb.c.find()\n  .limit(1)"
	var got []string
	for _, st := range split(src) {
		got = append(got, strings.ReplaceAll(st.Text, "\n", "⏎"))
	}
	want := []string{"let n = 0", "if (n == 0)⏎  print('zero')⏎else⏎  print('other')", "for (let i = 0; i < 2; i++) {⏎  n++⏎}",
		"n--", "const add = (a, b) =>⏎  a + b", "db.c.find()⏎  .limit(1)"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("split:\n%q\nwant\n%q", got, want)
	}
	if !keywords["let"] || !keywords["function"] {
		t.Error("JavaScript keywords")
	}
}
