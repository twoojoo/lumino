package mongo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var codegenStatements = []string{
	`db.orders.find({status: "A", total: {$gte: NumberDecimal("10.5")}, at: {$lt: ISODate("2026-01-02T03:04:05.000Z")}}, {status: 1}).sort({total: -1}).skip(5).limit(10)`,
	`db.orders.findOne({_id: ObjectId("507f1f77bcf86cd799439011")})`,
	`db.orders.aggregate([{$match: {n: NumberLong("9007199254740993"), f: 2.5, d: Double(3)}}, {$group: {_id: "$customer", total: {$sum: "$total"}}}])`,
	`db.orders.countDocuments({tags: {$in: ["a", "b"]}, name: /^ad/i})`,
	`db.orders.distinct("status", {total: {$gt: 1}})`,
	`db.orders.insertOne({u: UUID("8f3e1c2a-4b5d-4e6f-8a9b-0c1d2e3f4a5b"), ok: true, none: null, nested: {x: [1, {y: 2}]}})`,
	`db.orders.insertMany([{a: 1}, {a: 2}])`,
	`db.orders.updateMany({status: "A"}, {$set: {status: "B"}}, {upsert: true})`,
	`db.orders.updateOne({_id: 1}, [{$set: {n: {$add: ["$n", 1]}}}])`,
	`db.orders.replaceOne({_id: 1}, {a: 1})`,
	`db.orders.deleteMany({status: "Z"})`,
	`db.getSiblingDB("shop").getCollection("my orders").find({})`,
}

func TestGenerateCode(t *testing.T) {
	l := &language{}
	for _, st := range codegenStatements {
		for _, target := range CodeTargets {
			code, err := l.GenerateCode(st, "lumino", target, false)
			if err != nil || strings.TrimSpace(code) == "" {
				t.Errorf("%s / %s: %v", target, st, err)
			}
		}
	}
	got, _ := l.GenerateCode(codegenStatements[0], "lumino", "python", false)
	for _, want := range []string{`coll = client["lumino"]["orders"]`, `Decimal128("10.5")`, `datetime(2026, 1, 2, 3, 4, 5, 0, tzinfo=timezone.utc)`,
		`.sort([("total", -1)]).skip(5).limit(10)`} {
		if !strings.Contains(got, want) {
			t.Errorf("python misses %q:\n%s", want, got)
		}
	}
	got, _ = l.GenerateCode(codegenStatements[2], "lumino", "node", false)
	if !strings.Contains(got, `Long.fromString("9007199254740993")`) || !strings.Contains(got, `d: new Double(3)`) || !strings.Contains(got, "f: 2.5") {
		t.Errorf("node literals:\n%s", got)
	}
	got, _ = l.GenerateCode(codegenStatements[1], "lumino", "java", false)
	if !strings.Contains(got, `Document.parse("{\"_id\": {\"$oid\":\"507f1f77bcf86cd799439011\"}}")`) || !strings.Contains(got, "found.first()") {
		t.Errorf("java:\n%s", got)
	}
	got, _ = l.GenerateCode(codegenStatements[7], "lumino", "csharp", false)
	if !strings.Contains(got, "UpdateManyAsync") || !strings.Contains(got, "IsUpsert = true") {
		t.Errorf("c#:\n%s", got)
	}
	got, _ = l.GenerateCode(codegenStatements[11], "lumino", "go", false)
	if !strings.Contains(got, `client.Database("shop").Collection("my orders")`) {
		t.Errorf("go database:\n%s", got)
	}
	for _, bad := range []string{"let x = 1", "db.getCollectionNames()", "db.c.drop()", "db.c.find().explain()"} {
		if _, err := l.GenerateCode(bad, "lumino", "go", false); err == nil {
			t.Errorf("%q exported", bad)
		}
	}
	if _, err := l.GenerateCode(codegenStatements[0], "lumino", "cobol", false); err == nil {
		t.Error("unknown target")
	}
}

// TestGeneratedSyntax compiles the Go programs (go vet) and checks the
// syntax of the Python and Node.js ones with the local interpreters.
func TestGeneratedSyntax(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	l := &language{}
	dir, err := os.MkdirTemp(".", "codegencheck")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	py3, _ := exec.LookPath("python3")
	node, _ := exec.LookPath("node")
	for i, st := range codegenStatements {
		sub := filepath.Join(dir, "p"+itoa(i))
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		write := func(name, target string) string {
			code, err := l.GenerateCode(st, "lumino", target, true)
			if err != nil {
				t.Fatalf("%s / %s: %v", target, st, err)
			}
			f := filepath.Join(sub, name)
			if err := os.WriteFile(f, []byte(code), 0o644); err != nil {
				t.Fatal(err)
			}
			return f
		}
		write("main.go", "go")
		if f := write("main.py", "python"); py3 != "" {
			if out, err := exec.Command(py3, "-m", "py_compile", f).CombinedOutput(); err != nil {
				t.Errorf("generated Python for %s: %v\n%s", st, err, out)
			}
		}
		if f := write("main.js", "node"); node != "" {
			if out, err := exec.Command(node, "--check", f).CombinedOutput(); err != nil {
				t.Errorf("generated JavaScript for %s: %v\n%s", st, err, out)
			}
		}
	}
	if out, err := exec.Command("go", "vet", "./"+dir+"/...").CombinedOutput(); err != nil {
		t.Fatalf("generated Go doesn't compile: %v\n%s", err, out)
	}
}

func TestGeneratedProgram(t *testing.T) {
	l := &language{}
	got, _ := l.GenerateCode(codegenStatements[0], "lumino", "python", true)
	for _, want := range []string{"from datetime import datetime, timezone\n", "from bson import Decimal128\n", `MongoClient(os.environ["MONGODB_URI"])`} {
		if !strings.Contains(got, want) {
			t.Errorf("python program misses %q:\n%s", want, got)
		}
	}
	got, _ = l.GenerateCode(codegenStatements[2], "lumino", "node", true)
	if !strings.HasPrefix(got, `const { MongoClient, Double, Long } = require("mongodb");`) || !strings.Contains(got, "    const coll = client.db(") {
		t.Errorf("node program:\n%s", got)
	}
	got, _ = l.GenerateCode(codegenStatements[1], "lumino", "java", true)
	if !strings.Contains(got, `MongoClients.create(System.getenv("MONGODB_URI"))`) || !strings.Contains(got, "            Document doc = found.first();") {
		t.Errorf("java program:\n%s", got)
	}
	got, _ = l.GenerateCode(codegenStatements[6], "lumino", "csharp", true)
	if !strings.HasPrefix(got, "using System;") || !strings.Contains(got, `new MongoClient(Environment.GetEnvironmentVariable("MONGODB_URI"))`) {
		t.Errorf("c# program:\n%s", got)
	}
	got, _ = l.GenerateCode(codegenStatements[10], "lumino", "go", true)
	if strings.Contains(got, `"time"`) || strings.Contains(got, `"encoding/hex"`) || !strings.Contains(got, "func run(ctx context.Context, client *mongo.Client) error {") {
		t.Errorf("go program:\n%s", got)
	}
}

func itoa(i int) string { return string(rune('a' + i)) }
