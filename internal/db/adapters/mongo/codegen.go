package mongo

// Driver code from shell statements: Go (mongo-go-driver v2), Python
// (PyMongo), Node.js (mongodb), Java (sync driver) and C# (.NET driver).
// Go, Python and Node get native literals with the BSON types kept;
// Java and C# parse Extended JSON (longs as $numberLong), the idiomatic
// way to write documents there.

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// CodeTargets are the languages of GenerateCode.
var CodeTargets = []string{"go", "python", "node", "java", "csharp"}

// codeOp is the operation of a statement, its arguments split.
type codeOp struct {
	db, coll    string
	method      string
	filter, doc any // filter / document or update
	projection  any
	sort        any
	limit, skip int64
	pipeline    bson.A
	docs        bson.A
	field       string
	upsert      bool
	update      any
}

func argAt(a []any, i int) any {
	if i < len(a) {
		return a[i]
	}
	return nil
}

func docOr(v any) any {
	if v == nil {
		return bson.D{}
	}
	return v
}

// opOf reads the operation of a statement.
func opOf(stmt, dbName string) (*codeOp, error) {
	st, err := parseStatement(strings.TrimSpace(stmt))
	if err != nil {
		return nil, fmt.Errorf("only shell statements can be exported (not scripts): %w", err)
	}
	if st.kind != "coll" {
		return nil, errors.New("export a collection operation (db.collection.find(…), aggregate(…), insertOne(…)…)")
	}
	c := st.calls[0]
	op := &codeOp{db: st.dbName, coll: st.coll, method: c.name, limit: -1}
	if op.db == "" {
		op.db = dbName
	}
	opts, _ := argAt(c.args, 2).(bson.D)
	switch c.name {
	case "find", "findOne":
		op.filter, op.projection = docOr(argAt(c.args, 0)), argAt(c.args, 1)
		for _, m := range st.calls[1:] {
			v := argAt(m.args, 0)
			switch m.name {
			case "sort":
				op.sort = v
			case "projection", "project":
				op.projection = v
			case "limit":
				op.limit, _ = argInt(v)
			case "skip":
				op.skip, _ = argInt(v)
			case "pretty", "toArray":
			default:
				return nil, fmt.Errorf("%s() can't be exported", m.name)
			}
		}
	case "aggregate":
		if a, ok := argAt(c.args, 0).(bson.A); ok {
			op.pipeline = a
		} else {
			op.pipeline = bson.A(c.args)
		}
	case "countDocuments", "count", "deleteOne", "deleteMany":
		op.filter = docOr(argAt(c.args, 0))
		if c.name == "count" {
			op.method = "countDocuments"
		}
	case "distinct":
		f, ok := argAt(c.args, 0).(string)
		if !ok {
			return nil, errors.New("distinct needs a field name")
		}
		op.field, op.filter = f, docOr(argAt(c.args, 1))
	case "insertOne":
		op.doc = docOr(argAt(c.args, 0))
	case "insertMany":
		a, ok := argAt(c.args, 0).(bson.A)
		if !ok {
			return nil, errors.New("insertMany needs an array of documents")
		}
		op.docs = a
	case "updateOne", "updateMany", "replaceOne":
		op.filter, op.update = docOr(argAt(c.args, 0)), docOr(argAt(c.args, 1))
		op.upsert = fieldOr(opts, "upsert") == true
	default:
		return nil, fmt.Errorf("%s() can't be exported to code", c.name)
	}
	return op, nil
}

func (*language) GenerateCode(stmt, dbName, target string, program bool) (string, error) {
	op, err := opOf(stmt, dbName)
	if err != nil {
		return "", err
	}
	switch target {
	case "go":
		body, helpers := genGo(op)
		if program {
			return goProgram(body, helpers), nil
		}
		return body + helpers, nil
	case "python":
		if program {
			return pyProgram(genPython(op)), nil
		}
		return genPython(op), nil
	case "node":
		if program {
			return nodeProgram(genNode(op)), nil
		}
		return genNode(op), nil
	case "java":
		if program {
			return javaProgram(genJava(op)), nil
		}
		return genJava(op), nil
	case "csharp":
		if program {
			return csProgram(genCSharp(op)), nil
		}
		return genCSharp(op), nil
	}
	return "", fmt.Errorf("unknown code target %q", target)
}

// indent prefixes the non-empty lines of s.
func indent(s, pad string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = pad + l
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// usedNames lists the names called, constructed or used as a namespace
// (Long.fromString) in code.
func usedNames(code string, names ...string) []string {
	var out []string
	for _, n := range names {
		if regexp.MustCompile(`\b` + n + `[(.]`).MatchString(code) {
			out = append(out, n)
		}
	}
	return out
}

func goProgram(body, helpers string) string {
	all := body + helpers
	std := []string{"context", "fmt", "log", "os"}
	if strings.Contains(all, "hex.") {
		std = append(std, "encoding/hex")
	}
	if strings.Contains(all, "math.") {
		std = append(std, "math")
	}
	if strings.Contains(all, "time.") {
		std = append(std, "time")
	}
	sort.Strings(std)
	var b strings.Builder
	b.WriteString("package main\n\nimport (\n")
	for _, p := range std {
		b.WriteString("\t" + strconv.Quote(p) + "\n")
	}
	b.WriteString("\n")
	if strings.Contains(all, "bson.") {
		b.WriteString("\t\"go.mongodb.org/mongo-driver/v2/bson\"\n")
	}
	b.WriteString("\t\"go.mongodb.org/mongo-driver/v2/mongo\"\n\t\"go.mongodb.org/mongo-driver/v2/mongo/options\"\n)\n\n")
	b.WriteString("func main() {\n\tctx := context.Background()\n" +
		"\tclient, err := mongo.Connect(options.Client().ApplyURI(os.Getenv(\"MONGODB_URI\")))\n" +
		"\tif err != nil {\n\t\tlog.Fatal(err)\n\t}\n\tdefer client.Disconnect(ctx)\n" +
		"\tif err := run(ctx, client); err != nil {\n\t\tlog.Fatal(err)\n\t}\n}\n\n")
	b.WriteString("func run(ctx context.Context, client *mongo.Client) error {\n" + indent(body, "\t") + "\treturn nil\n}\n")
	b.WriteString(helpers)
	return b.String()
}

func pyProgram(body string) string {
	var b strings.Builder
	b.WriteString("import os\n")
	if strings.Contains(body, "datetime(") {
		b.WriteString("from datetime import datetime, timezone\n\n")
	} else {
		b.WriteString("\n")
	}
	if names := usedNames(body, "Binary", "Code", "Decimal128", "Int64", "MaxKey", "MinKey", "ObjectId", "Regex", "Timestamp"); len(names) > 0 {
		b.WriteString("from bson import " + strings.Join(names, ", ") + "\n")
	}
	b.WriteString("from pymongo import MongoClient\n\nclient = MongoClient(os.environ[\"MONGODB_URI\"])\n" + body)
	return b.String()
}

func nodeProgram(body string) string {
	names := append([]string{"MongoClient"}, usedNames(body, "BSONRegExp", "Binary", "Code", "Decimal128", "Double", "Long", "MaxKey", "MinKey", "ObjectId", "Timestamp")...)
	sort.Strings(names[1:])
	return "const { " + strings.Join(names, ", ") + " } = require(\"mongodb\");\n\n" +
		"async function main() {\n  const client = new MongoClient(process.env.MONGODB_URI);\n  try {\n" +
		indent(body, "    ") + "  } finally {\n    await client.close();\n  }\n}\n\nmain().catch((err) => {\n  console.error(err);\n  process.exit(1);\n});\n"
}

func javaProgram(body string) string {
	return "import com.mongodb.client.*;\nimport com.mongodb.client.model.*;\nimport com.mongodb.client.result.*;\n" +
		"import java.util.Arrays;\nimport java.util.List;\nimport org.bson.BsonValue;\nimport org.bson.Document;\n\n" +
		"public class Main {\n    public static void main(String[] args) {\n" +
		"        try (MongoClient client = MongoClients.create(System.getenv(\"MONGODB_URI\"))) {\n" +
		indent(body, "            ") + "        }\n    }\n}\n"
}

func csProgram(body string) string {
	return "using System;\nusing System.Linq;\nusing MongoDB.Bson;\nusing MongoDB.Driver;\n\n" +
		"var client = new MongoClient(Environment.GetEnvironmentVariable(\"MONGODB_URI\"));\n" + body
}

// ---- literals ----

func dateParts(t time.Time) []int {
	t = t.UTC()
	return []int{t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond()}
}

func floatLit(f float64, integral string) string {
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eEn") { // NaN / Inf apart
		return fmt.Sprintf(integral, s)
	}
	return s
}

// goLit writes a value as Go (mongo-go-driver v2) code.
func goLit(b *strings.Builder, v any, depth int) {
	pad := strings.Repeat("\t", depth+1)
	switch x := v.(type) {
	case bson.D:
		if len(x) == 0 {
			b.WriteString("bson.D{}")
			return
		}
		b.WriteString("bson.D{\n")
		for _, e := range x {
			b.WriteString(pad + "{Key: " + strconv.Quote(e.Key) + ", Value: ")
			goLit(b, e.Value, depth+1)
			b.WriteString("},\n")
		}
		b.WriteString(strings.Repeat("\t", depth) + "}")
	case bson.A:
		b.WriteString("bson.A{")
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			goLit(b, e, depth)
		}
		b.WriteString("}")
	case nil:
		b.WriteString("nil")
	case string:
		b.WriteString(strconv.Quote(x))
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case int32:
		fmt.Fprintf(b, "int32(%d)", x)
	case int64:
		fmt.Fprintf(b, "int64(%d)", x)
	case float64:
		switch {
		case math.IsNaN(x):
			b.WriteString("math.NaN()")
		case math.IsInf(x, 0):
			fmt.Fprintf(b, "math.Inf(%d)", map[bool]int{true: 1, false: -1}[x > 0])
		default:
			b.WriteString(floatLit(x, "float64(%s)"))
		}
	case bson.ObjectID:
		b.WriteString(`mustObjectID("` + x.Hex() + `")`)
	case bson.Decimal128:
		b.WriteString(`mustDecimal("` + x.String() + `")`)
	case bson.DateTime:
		p := dateParts(x.Time())
		fmt.Fprintf(b, "bson.NewDateTimeFromTime(time.Date(%d, %d, %d, %d, %d, %d, %d, time.UTC))", p[0], p[1], p[2], p[3], p[4], p[5], p[6])
	case bson.Binary:
		fmt.Fprintf(b, "bson.Binary{Subtype: %d, Data: mustHex(%q)}", x.Subtype, hex.EncodeToString(x.Data))
	case bson.Regex:
		fmt.Fprintf(b, "bson.Regex{Pattern: %q, Options: %q}", x.Pattern, x.Options)
	case bson.Timestamp:
		fmt.Fprintf(b, "bson.Timestamp{T: %d, I: %d}", x.T, x.I)
	case bson.MinKey:
		b.WriteString("bson.MinKey{}")
	case bson.MaxKey:
		b.WriteString("bson.MaxKey{}")
	case bson.JavaScript:
		fmt.Fprintf(b, "bson.JavaScript(%q)", string(x))
	default:
		b.WriteString(strconv.Quote(shellLit(v)))
	}
}

func goVal(v any) string {
	var b strings.Builder
	goLit(&b, v, 0)
	return b.String()
}

// pyLit writes a value as Python (PyMongo) code.
func pyLit(b *strings.Builder, v any) {
	switch x := v.(type) {
	case bson.D:
		b.WriteString("{")
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(strconv.Quote(e.Key) + ": ")
			pyLit(b, e.Value)
		}
		b.WriteString("}")
	case bson.A:
		b.WriteString("[")
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			pyLit(b, e)
		}
		b.WriteString("]")
	case nil:
		b.WriteString("None")
	case bool:
		b.WriteString(map[bool]string{true: "True", false: "False"}[x])
	case string:
		b.WriteString(strconv.Quote(x))
	case int32:
		b.WriteString(strconv.FormatInt(int64(x), 10))
	case int64:
		fmt.Fprintf(b, "Int64(%d)", x)
	case float64:
		switch {
		case math.IsNaN(x):
			b.WriteString(`float("nan")`)
		case math.IsInf(x, 1):
			b.WriteString(`float("inf")`)
		case math.IsInf(x, -1):
			b.WriteString(`float("-inf")`)
		default:
			b.WriteString(floatLit(x, "%s.0"))
		}
	case bson.ObjectID:
		b.WriteString(`ObjectId("` + x.Hex() + `")`)
	case bson.Decimal128:
		b.WriteString(`Decimal128("` + x.String() + `")`)
	case bson.DateTime:
		p := dateParts(x.Time())
		fmt.Fprintf(b, "datetime(%d, %d, %d, %d, %d, %d, %d, tzinfo=timezone.utc)", p[0], p[1], p[2], p[3], p[4], p[5], p[6]/1000)
	case bson.Binary:
		fmt.Fprintf(b, `Binary(bytes.fromhex("%s"), %d)`, hex.EncodeToString(x.Data), x.Subtype)
	case bson.Regex:
		fmt.Fprintf(b, "Regex(%s, %s)", strconv.Quote(x.Pattern), strconv.Quote(x.Options))
	case bson.Timestamp:
		fmt.Fprintf(b, "Timestamp(%d, %d)", x.T, x.I)
	case bson.MinKey:
		b.WriteString("MinKey()")
	case bson.MaxKey:
		b.WriteString("MaxKey()")
	case bson.JavaScript:
		fmt.Fprintf(b, "Code(%s)", strconv.Quote(string(x)))
	default:
		b.WriteString(strconv.Quote(shellLit(v)))
	}
}

func pyVal(v any) string {
	var b strings.Builder
	pyLit(&b, v)
	return b.String()
}

// nodeLit writes a value as JavaScript (Node.js driver) code.
func nodeLit(b *strings.Builder, v any) {
	switch x := v.(type) {
	case bson.D:
		b.WriteString("{")
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(key(e.Key) + ": ")
			nodeLit(b, e.Value)
		}
		b.WriteString("}")
	case bson.A:
		b.WriteString("[")
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			nodeLit(b, e)
		}
		b.WriteString("]")
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case string:
		b.WriteString(strconv.Quote(x))
	case int32:
		b.WriteString(strconv.FormatInt(int64(x), 10))
	case int64:
		fmt.Fprintf(b, `Long.fromString("%d")`, x)
	case float64:
		switch {
		case math.IsNaN(x):
			b.WriteString("NaN")
		case math.IsInf(x, 1):
			b.WriteString("Infinity")
		case math.IsInf(x, -1):
			b.WriteString("-Infinity")
		default:
			b.WriteString(floatLit(x, "new Double(%s)"))
		}
	case bson.ObjectID:
		b.WriteString(`new ObjectId("` + x.Hex() + `")`)
	case bson.Decimal128:
		b.WriteString(`Decimal128.fromString("` + x.String() + `")`)
	case bson.DateTime:
		b.WriteString(`new Date("` + x.Time().UTC().Format("2006-01-02T15:04:05.000Z") + `")`)
	case bson.Binary:
		fmt.Fprintf(b, `new Binary(Buffer.from("%s", "hex"), %d)`, hex.EncodeToString(x.Data), x.Subtype)
	case bson.Regex:
		fmt.Fprintf(b, "new BSONRegExp(%s, %s)", strconv.Quote(x.Pattern), strconv.Quote(x.Options))
	case bson.Timestamp:
		fmt.Fprintf(b, "new Timestamp({t: %d, i: %d})", x.T, x.I)
	case bson.MinKey:
		b.WriteString("new MinKey()")
	case bson.MaxKey:
		b.WriteString("new MaxKey()")
	case bson.JavaScript:
		fmt.Fprintf(b, "new Code(%s)", strconv.Quote(string(x)))
	default:
		b.WriteString(strconv.Quote(shellLit(v)))
	}
}

func nodeVal(v any) string {
	var b strings.Builder
	nodeLit(&b, v)
	return b.String()
}

// javaString is a Java / C# string literal.
func javaString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// ---- Go ----

const goCheck = "if err != nil {\n\treturn err\n}\n"

const goResults = "var results []bson.M\nif err := cursor.All(ctx, &results); err != nil {\n\treturn err\n}\n" +
	"for _, doc := range results {\n\tfmt.Println(doc)\n}\n"

func genGo(op *codeOp) (body, helpers string) {
	var b, h strings.Builder
	used := map[string]bool{}
	val := func(v any) string {
		s := goVal(v)
		for _, h := range []string{"mustObjectID", "mustDecimal", "mustHex"} {
			if strings.Contains(s, h+"(") {
				used[h] = true
			}
		}
		return s
	}
	fmt.Fprintf(&b, "coll := client.Database(%q).Collection(%q)\n", op.db, op.coll)
	switch op.method {
	case "find", "findOne":
		fmt.Fprintf(&b, "filter := %s\n", val(op.filter))
		ctor := "options.Find()"
		if op.method == "findOne" {
			ctor = "options.FindOne()"
		}
		var opts []string
		if op.projection != nil {
			opts = append(opts, ".\n\tSetProjection("+val(op.projection)+")")
		}
		if op.sort != nil {
			opts = append(opts, ".\n\tSetSort("+val(op.sort)+")")
		}
		if op.skip > 0 {
			opts = append(opts, fmt.Sprintf(".\n\tSetSkip(%d)", op.skip))
		}
		if op.limit >= 0 && op.method == "find" {
			opts = append(opts, fmt.Sprintf(".\n\tSetLimit(%d)", op.limit))
		}
		optArg := ""
		if len(opts) > 0 {
			fmt.Fprintf(&b, "opts := %s%s\n", ctor, strings.Join(opts, ""))
			optArg = ", opts"
		}
		if op.method == "findOne" {
			fmt.Fprintf(&b, "var result bson.M\nif err := coll.FindOne(ctx, filter%s).Decode(&result); err != nil {\n\treturn err\n}\nfmt.Println(result)\n", optArg)
			break
		}
		fmt.Fprintf(&b, "cursor, err := coll.Find(ctx, filter%s)\nif err != nil {\n\treturn err\n}\ndefer cursor.Close(ctx)\n", optArg)
		b.WriteString(goResults)
	case "aggregate":
		b.WriteString("pipeline := mongo.Pipeline{\n")
		for _, st := range op.pipeline {
			s := goVal(st)
			val(st)
			b.WriteString("\t" + strings.ReplaceAll(s, "\n", "\n\t") + ",\n")
		}
		b.WriteString("}\ncursor, err := coll.Aggregate(ctx, pipeline)\nif err != nil {\n\treturn err\n}\ndefer cursor.Close(ctx)\n")
		b.WriteString(goResults)
	case "countDocuments":
		fmt.Fprintf(&b, "n, err := coll.CountDocuments(ctx, %s)\n%sfmt.Println(n)\n", val(op.filter), goCheck)
	case "distinct":
		fmt.Fprintf(&b, "var values []any\nif err := coll.Distinct(ctx, %q, %s).Decode(&values); err != nil {\n\treturn err\n}\nfmt.Println(values)\n", op.field, val(op.filter))
	case "insertOne":
		fmt.Fprintf(&b, "res, err := coll.InsertOne(ctx, %s)\n%sfmt.Println(res.InsertedID)\n", val(op.doc), goCheck)
	case "insertMany":
		b.WriteString("docs := []any{\n")
		for _, d := range op.docs {
			b.WriteString("\t" + strings.ReplaceAll(val(d), "\n", "\n\t") + ",\n")
		}
		b.WriteString("}\nres, err := coll.InsertMany(ctx, docs)\n" + goCheck + "fmt.Println(res.InsertedIDs)\n")
	case "updateOne", "updateMany", "replaceOne":
		name := map[string]string{"updateOne": "UpdateOne", "updateMany": "UpdateMany", "replaceOne": "ReplaceOne"}[op.method]
		opt := ""
		if op.upsert {
			opt = ", options." + name + "().SetUpsert(true)"
		}
		fmt.Fprintf(&b, "filter := %s\nupdate := %s\nres, err := coll.%s(ctx, filter, update%s)\n%sfmt.Println(res.MatchedCount, res.ModifiedCount)\n",
			val(op.filter), val(op.update), name, opt, goCheck)
	case "deleteOne", "deleteMany":
		name := map[string]string{"deleteOne": "DeleteOne", "deleteMany": "DeleteMany"}[op.method]
		fmt.Fprintf(&b, "res, err := coll.%s(ctx, %s)\n%sfmt.Println(res.DeletedCount)\n", name, val(op.filter), goCheck)
	}
	if used["mustObjectID"] {
		h.WriteString("\nfunc mustObjectID(s string) bson.ObjectID {\n\tid, err := bson.ObjectIDFromHex(s)\n\tif err != nil {\n\t\tpanic(err)\n\t}\n\treturn id\n}\n")
	}
	if used["mustDecimal"] {
		h.WriteString("\nfunc mustDecimal(s string) bson.Decimal128 {\n\td, err := bson.ParseDecimal128(s)\n\tif err != nil {\n\t\tpanic(err)\n\t}\n\treturn d\n}\n")
	}
	if used["mustHex"] {
		h.WriteString("\nfunc mustHex(s string) []byte {\n\tb, err := hex.DecodeString(s)\n\tif err != nil {\n\t\tpanic(err)\n\t}\n\treturn b\n}\n")
	}
	return b.String(), h.String()
}

// ---- Python ----

func genPython(op *codeOp) string {
	var b strings.Builder
	fmt.Fprintf(&b, "coll = client[%q][%q]\n", op.db, op.coll)
	switch op.method {
	case "find", "findOne":
		args := pyVal(op.filter)
		if op.projection != nil {
			args += ", " + pyVal(op.projection)
		}
		if op.method == "findOne" {
			extra := ""
			if op.sort != nil {
				extra = ", sort=" + pySort(op.sort)
			}
			if op.skip > 0 {
				extra += fmt.Sprintf(", skip=%d", op.skip)
			}
			fmt.Fprintf(&b, "doc = coll.find_one(%s%s)\nprint(doc)\n", args, extra)
			break
		}
		fmt.Fprintf(&b, "cursor = coll.find(%s)", args)
		if op.sort != nil {
			b.WriteString(".sort(" + pySort(op.sort) + ")")
		}
		if op.skip > 0 {
			fmt.Fprintf(&b, ".skip(%d)", op.skip)
		}
		if op.limit >= 0 {
			fmt.Fprintf(&b, ".limit(%d)", op.limit)
		}
		b.WriteString("\nfor doc in cursor:\n    print(doc)\n")
	case "aggregate":
		b.WriteString("pipeline = [\n")
		for _, st := range op.pipeline {
			b.WriteString("    " + pyVal(st) + ",\n")
		}
		b.WriteString("]\nfor doc in coll.aggregate(pipeline):\n    print(doc)\n")
	case "countDocuments":
		fmt.Fprintf(&b, "n = coll.count_documents(%s)\nprint(n)\n", pyVal(op.filter))
	case "distinct":
		fmt.Fprintf(&b, "values = coll.distinct(%q, %s)\nprint(values)\n", op.field, pyVal(op.filter))
	case "insertOne":
		fmt.Fprintf(&b, "res = coll.insert_one(%s)\nprint(res.inserted_id)\n", pyVal(op.doc))
	case "insertMany":
		fmt.Fprintf(&b, "res = coll.insert_many(%s)\nprint(res.inserted_ids)\n", pyVal(op.docs))
	case "updateOne", "updateMany", "replaceOne":
		name := map[string]string{"updateOne": "update_one", "updateMany": "update_many", "replaceOne": "replace_one"}[op.method]
		up := ""
		if op.upsert {
			up = ", upsert=True"
		}
		fmt.Fprintf(&b, "res = coll.%s(%s, %s%s)\nprint(res.matched_count, res.modified_count)\n", name, pyVal(op.filter), pyVal(op.update), up)
	case "deleteOne", "deleteMany":
		name := map[string]string{"deleteOne": "delete_one", "deleteMany": "delete_many"}[op.method]
		fmt.Fprintf(&b, "res = coll.%s(%s)\nprint(res.deleted_count)\n", name, pyVal(op.filter))
	}
	return b.String()
}

// pySort writes a sort document as PyMongo's list of pairs.
func pySort(v any) string {
	d, ok := v.(bson.D)
	if !ok {
		return pyVal(v)
	}
	parts := make([]string, len(d))
	for i, e := range d {
		parts[i] = "(" + strconv.Quote(e.Key) + ", " + pyVal(e.Value) + ")"
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// ---- Node.js ----

func genNode(op *codeOp) string {
	var b strings.Builder
	fmt.Fprintf(&b, "const coll = client.db(%q).collection(%q);\n", op.db, op.coll)
	switch op.method {
	case "find", "findOne":
		opts := []string{}
		if op.projection != nil {
			opts = append(opts, "projection: "+nodeVal(op.projection))
		}
		if op.method == "findOne" {
			if op.sort != nil {
				opts = append(opts, "sort: "+nodeVal(op.sort))
			}
			if op.skip > 0 {
				opts = append(opts, fmt.Sprintf("skip: %d", op.skip))
			}
		}
		arg := nodeVal(op.filter)
		if len(opts) > 0 {
			arg += ", {" + strings.Join(opts, ", ") + "}"
		}
		if op.method == "findOne" {
			fmt.Fprintf(&b, "const doc = await coll.findOne(%s);\nconsole.log(doc);\n", arg)
			break
		}
		fmt.Fprintf(&b, "const docs = await coll.find(%s)", arg)
		if op.sort != nil {
			b.WriteString("\n  .sort(" + nodeVal(op.sort) + ")")
		}
		if op.skip > 0 {
			fmt.Fprintf(&b, "\n  .skip(%d)", op.skip)
		}
		if op.limit >= 0 {
			fmt.Fprintf(&b, "\n  .limit(%d)", op.limit)
		}
		b.WriteString("\n  .toArray();\nconsole.log(docs);\n")
	case "aggregate":
		b.WriteString("const pipeline = [\n")
		for _, st := range op.pipeline {
			b.WriteString("  " + nodeVal(st) + ",\n")
		}
		b.WriteString("];\nconst docs = await coll.aggregate(pipeline).toArray();\nconsole.log(docs);\n")
	case "countDocuments":
		fmt.Fprintf(&b, "const n = await coll.countDocuments(%s);\nconsole.log(n);\n", nodeVal(op.filter))
	case "distinct":
		fmt.Fprintf(&b, "const values = await coll.distinct(%q, %s);\nconsole.log(values);\n", op.field, nodeVal(op.filter))
	case "insertOne":
		fmt.Fprintf(&b, "const res = await coll.insertOne(%s);\nconsole.log(res.insertedId);\n", nodeVal(op.doc))
	case "insertMany":
		fmt.Fprintf(&b, "const res = await coll.insertMany(%s);\nconsole.log(res.insertedIds);\n", nodeVal(op.docs))
	case "updateOne", "updateMany", "replaceOne":
		up := ""
		if op.upsert {
			up = ", {upsert: true}"
		}
		fmt.Fprintf(&b, "const res = await coll.%s(%s, %s%s);\nconsole.log(res.matchedCount, res.modifiedCount);\n", op.method, nodeVal(op.filter), nodeVal(op.update), up)
	case "deleteOne", "deleteMany":
		fmt.Fprintf(&b, "const res = await coll.%s(%s);\nconsole.log(res.deletedCount);\n", op.method, nodeVal(op.filter))
	}
	return b.String()
}

// ---- Java ----

func javaDoc(v any) string { return "Document.parse(" + javaString(ejsonText(v)) + ")" }

func genJava(op *codeOp) string {
	var b strings.Builder
	fmt.Fprintf(&b, "MongoCollection<Document> coll = client.getDatabase(%s).getCollection(%s);\n", javaString(op.db), javaString(op.coll))
	switch op.method {
	case "find", "findOne":
		fmt.Fprintf(&b, "FindIterable<Document> found = coll.find(%s)", javaDoc(op.filter))
		if op.projection != nil {
			b.WriteString("\n    .projection(" + javaDoc(op.projection) + ")")
		}
		if op.sort != nil {
			b.WriteString("\n    .sort(" + javaDoc(op.sort) + ")")
		}
		if op.skip > 0 {
			fmt.Fprintf(&b, "\n    .skip(%d)", op.skip)
		}
		if op.limit >= 0 && op.method == "find" {
			fmt.Fprintf(&b, "\n    .limit(%d)", op.limit)
		}
		if op.method == "findOne" {
			b.WriteString(";\nDocument doc = found.first();\nSystem.out.println(doc == null ? null : doc.toJson());\n")
		} else {
			b.WriteString(";\nfor (Document doc : found) {\n    System.out.println(doc.toJson());\n}\n")
		}
	case "aggregate":
		b.WriteString("List<Document> pipeline = Arrays.asList(\n")
		for i, st := range op.pipeline {
			sep := ","
			if i == len(op.pipeline)-1 {
				sep = ""
			}
			b.WriteString("    " + javaDoc(st) + sep + "\n")
		}
		b.WriteString(");\nfor (Document doc : coll.aggregate(pipeline)) {\n    System.out.println(doc.toJson());\n}\n")
	case "countDocuments":
		fmt.Fprintf(&b, "long n = coll.countDocuments(%s);\nSystem.out.println(n);\n", javaDoc(op.filter))
	case "distinct":
		fmt.Fprintf(&b, "DistinctIterable<BsonValue> values = coll.distinct(%s, %s, BsonValue.class);\nfor (BsonValue v : values) {\n    System.out.println(v);\n}\n", javaString(op.field), javaDoc(op.filter))
	case "insertOne":
		fmt.Fprintf(&b, "InsertOneResult res = coll.insertOne(%s);\nSystem.out.println(res.getInsertedId());\n", javaDoc(op.doc))
	case "insertMany":
		b.WriteString("List<Document> docs = Arrays.asList(\n")
		for i, d := range op.docs {
			sep := ","
			if i == len(op.docs)-1 {
				sep = ""
			}
			b.WriteString("    " + javaDoc(d) + sep + "\n")
		}
		b.WriteString(");\nInsertManyResult res = coll.insertMany(docs);\nSystem.out.println(res.getInsertedIds());\n")
	case "updateOne", "updateMany", "replaceOne":
		update := javaDoc(op.update)
		if a, ok := op.update.(bson.A); ok { // update pipeline
			parts := make([]string, len(a))
			for i, st := range a {
				parts[i] = javaDoc(st)
			}
			update = "Arrays.asList(" + strings.Join(parts, ", ") + ")"
		}
		opt := ""
		if op.upsert {
			opt = ", new UpdateOptions().upsert(true)"
			if op.method == "replaceOne" {
				opt = ", new ReplaceOptions().upsert(true)"
			}
		}
		result := "UpdateResult"
		fmt.Fprintf(&b, "%s res = coll.%s(%s, %s%s);\nSystem.out.println(res.getMatchedCount() + \" \" + res.getModifiedCount());\n", result, op.method, javaDoc(op.filter), update, opt)
	case "deleteOne", "deleteMany":
		fmt.Fprintf(&b, "DeleteResult res = coll.%s(%s);\nSystem.out.println(res.getDeletedCount());\n", op.method, javaDoc(op.filter))
	}
	return b.String()
}

// ---- C# ----

func csDoc(v any) string { return "BsonDocument.Parse(" + javaString(ejsonText(v)) + ")" }

func genCSharp(op *codeOp) string {
	var b strings.Builder
	fmt.Fprintf(&b, "var coll = client.GetDatabase(%s).GetCollection<BsonDocument>(%s);\n", javaString(op.db), javaString(op.coll))
	switch op.method {
	case "find", "findOne":
		fmt.Fprintf(&b, "var found = coll.Find(%s)", csDoc(op.filter))
		if op.projection != nil {
			b.WriteString("\n    .Project(" + csDoc(op.projection) + ")")
		}
		if op.sort != nil {
			b.WriteString("\n    .Sort(" + csDoc(op.sort) + ")")
		}
		if op.skip > 0 {
			fmt.Fprintf(&b, "\n    .Skip(%d)", op.skip)
		}
		if op.limit >= 0 && op.method == "find" {
			fmt.Fprintf(&b, "\n    .Limit(%d)", op.limit)
		}
		if op.method == "findOne" {
			b.WriteString(";\nvar doc = await found.FirstOrDefaultAsync();\nConsole.WriteLine(doc);\n")
		} else {
			b.WriteString(";\nvar docs = await found.ToListAsync();\nforeach (var doc in docs)\n{\n    Console.WriteLine(doc);\n}\n")
		}
	case "aggregate":
		b.WriteString("var pipeline = new[]\n{\n")
		for _, st := range op.pipeline {
			b.WriteString("    " + csDoc(st) + ",\n")
		}
		b.WriteString("};\nvar docs = await coll.Aggregate<BsonDocument>(pipeline).ToListAsync();\nforeach (var doc in docs)\n{\n    Console.WriteLine(doc);\n}\n")
	case "countDocuments":
		fmt.Fprintf(&b, "var n = await coll.CountDocumentsAsync(%s);\nConsole.WriteLine(n);\n", csDoc(op.filter))
	case "distinct":
		fmt.Fprintf(&b, "var values = await (await coll.DistinctAsync<BsonValue>(%s, %s)).ToListAsync();\nConsole.WriteLine(string.Join(\", \", values));\n", javaString(op.field), csDoc(op.filter))
	case "insertOne":
		fmt.Fprintf(&b, "var doc = %s;\nawait coll.InsertOneAsync(doc);\nConsole.WriteLine(doc[\"_id\"]);\n", csDoc(op.doc))
	case "insertMany":
		b.WriteString("var docs = new[]\n{\n")
		for _, d := range op.docs {
			b.WriteString("    " + csDoc(d) + ",\n")
		}
		b.WriteString("};\nawait coll.InsertManyAsync(docs);\nConsole.WriteLine(string.Join(\", \", docs.Select(d => d[\"_id\"])));\n")
	case "updateOne", "updateMany":
		name := map[string]string{"updateOne": "UpdateOneAsync", "updateMany": "UpdateManyAsync"}[op.method]
		update := csDoc(op.update)
		if a, ok := op.update.(bson.A); ok {
			parts := make([]string, len(a))
			for i, st := range a {
				parts[i] = csDoc(st)
			}
			update = "Builders<BsonDocument>.Update.Pipeline(new[] { " + strings.Join(parts, ", ") + " })"
		}
		opt := ""
		if op.upsert {
			opt = ", new UpdateOptions { IsUpsert = true }"
		}
		fmt.Fprintf(&b, "var res = await coll.%s(%s, %s%s);\nConsole.WriteLine($\"{res.MatchedCount} {res.ModifiedCount}\");\n", name, csDoc(op.filter), update, opt)
	case "replaceOne":
		opt := ""
		if op.upsert {
			opt = ", new ReplaceOptions { IsUpsert = true }"
		}
		fmt.Fprintf(&b, "var res = await coll.ReplaceOneAsync(%s, %s%s);\nConsole.WriteLine($\"{res.MatchedCount} {res.ModifiedCount}\");\n", csDoc(op.filter), csDoc(op.update), opt)
	case "deleteOne", "deleteMany":
		name := map[string]string{"deleteOne": "DeleteOneAsync", "deleteMany": "DeleteManyAsync"}[op.method]
		fmt.Fprintf(&b, "var res = await coll.%s(%s);\nConsole.WriteLine(res.DeletedCount);\n", name, csDoc(op.filter))
	}
	return b.String()
}
