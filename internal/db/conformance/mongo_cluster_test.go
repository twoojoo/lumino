//go:build integration

package conformance

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/db/dbtest"
)

// TestMongoReplicaSetStatus reads the members and the oplog window of the
// development replica set.
func TestMongoReplicaSetStatus(t *testing.T) {
	needs(t, "127.0.0.1:55017")
	conn := ping(t, dbtest.Targets()["mongo"].Config, db.OpenOptions{})
	tools := conn.(db.DocumentTools)
	if tools.Topology() != "replicaset" {
		t.Fatalf("topology %s", tools.Topology())
	}
	st, err := tools.ReplicaSet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Name != "rs0" || len(st.Members) != 1 || st.Members[0].State != "PRIMARY" || !st.Members[0].Healthy || !st.Members[0].Self ||
		st.Members[0].Optime.IsZero() || st.OplogFirst.IsZero() || st.OplogLast.Before(st.OplogFirst) || st.OplogSize <= 0 || st.OplogMax <= 0 {
		t.Fatalf("replica set %+v", st)
	}
	if _, err := tools.Sharding(context.Background()); err == nil {
		t.Fatal("sharding status of a replica set")
	}
}

// TestMongoShardedCluster reads the cluster of scripts/dbs (two shards,
// lumino_shard.events hashed on k) through mongos.
func TestMongoShardedCluster(t *testing.T) {
	needs(t, "127.0.0.1:55020")
	cfg := db.Config{Driver: "mongodb", Host: "127.0.0.1", Port: 55020, Database: "lumino_shard"}
	conn := ping(t, cfg, db.OpenOptions{})
	tools := conn.(db.DocumentTools)
	if tools.Topology() != "sharded" || !conn.Caps().Transactions {
		t.Fatalf("topology %s, caps %+v", tools.Topology(), conn.Caps())
	}
	s, err := conn.Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res, err := s.Execute(context.Background(), `for (let i = 0; i < 200; i++) db.events.insertOne({k: i, at: new Date()})`)
	if err != nil {
		t.Fatal(err)
	}
	res.Close()
	st, err := tools.Sharding(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, sh := range st.Shards {
		ids[sh.ID] = strings.Contains(sh.Host, "shard-")
	}
	if !ids["sa"] || !ids["sb"] || st.Balancer == "" || len(st.Routers) == 0 {
		t.Fatalf("cluster %+v", st)
	}
	var ev *db.ShardedCollection
	for i, c := range st.Collections {
		if c.NS == "lumino_shard.events" {
			ev = &st.Collections[i]
		}
	}
	if ev == nil || ev.Key != `{k: "hashed"}` || ev.Chunks["sa"] == 0 || ev.Chunks["sb"] == 0 {
		t.Fatalf("sharded collection %+v", ev)
	}
	found := false
	for _, d := range st.Databases {
		found = found || d.Name == "lumino_shard" && d.Primary != ""
	}
	if !found {
		t.Fatalf("databases %+v", st.Databases)
	}
	if _, err := tools.ReplicaSet(context.Background()); err == nil {
		t.Fatal("replica set status through mongos")
	}
}

// TestMongoGeneratedGoRuns runs the Go code generated for statements
// against the development server and checks what it prints.
func TestMongoGeneratedGoRuns(t *testing.T) {
	needs(t, "127.0.0.1:55017")
	conn := ping(t, dbtest.Targets()["mongo"].Config, db.OpenOptions{})
	dl := conn.Language().(db.DocumentLanguage)
	steps := []string{
		`db.cg.deleteMany({})`,
		`db.cg.insertMany([{_id: ObjectId("507f1f77bcf86cd799439011"), a: 1, d: NumberDecimal("1.50"), at: ISODate("2026-01-02T03:04:05.000Z")}, {_id: 2, a: 5, l: NumberLong("9007199254740993")}, {_id: 3, a: 3}])`,
		`db.cg.updateOne({_id: 3}, {$set: {tag: "x"}}, {upsert: true})`,
		`db.cg.find({a: {$gt: 1}}, {a: 1}).sort({a: -1}).limit(5)`,
		`db.cg.aggregate([{$group: {_id: null, total: {$sum: "$a"}}}])`,
		`db.cg.countDocuments({d: NumberDecimal("1.50"), at: {$gte: ISODate("2026-01-01T00:00:00Z")}})`,
		`db.cg.findOne({_id: ObjectId("507f1f77bcf86cd799439011")})`,
		`db.cg.distinct("a", {l: NumberLong("9007199254740993")})`,
	}
	dir, err := os.MkdirTemp("../adapters/mongo", "codegenrun")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	var names []string
	for i, st := range steps {
		code, err := dl.GenerateCode(st, "lumino_codegen", "go", true)
		if err != nil {
			t.Fatalf("%s: %v", st, err)
		}
		name := "step" + string(rune('a'+i))
		names = append(names, name)
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, "main.go"), []byte(code), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	bin, err := filepath.Abs(filepath.Join(dir, "bin"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin+"/", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated programs: %v\n%s", err, out)
	}
	var out []byte
	for _, name := range names {
		cmd := exec.CommandContext(ctx, filepath.Join(bin, name))
		cmd.Env = append(os.Environ(), "MONGODB_URI=mongodb://127.0.0.1:55017/?directConnection=true")
		o, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", name, err, o)
		}
		out = append(append(out, "-- "+name+"\n"...), o...)
	}
	o := string(out)
	part := func(step string) string {
		_, after, _ := strings.Cut(o, "-- "+step+"\n")
		before, _, _ := strings.Cut(after, "-- ")
		return before
	}
	d := part("stepd")
	i5, i3 := strings.Index(d, `"a":{"$numberInt":"5"}`), strings.Index(d, `"a":{"$numberInt":"3"}`)
	switch {
	case strings.TrimSpace(part("stepa")) == "":
		t.Errorf("deleteMany printed nothing:\n%s", o)
	case !strings.Contains(part("stepb"), `ObjectID("507f1f77bcf86cd799439011") 2 3`):
		t.Errorf("insertMany:\n%s", o)
	case strings.TrimSpace(part("stepc")) != "1 1":
		t.Errorf("updateOne:\n%s", o)
	case i5 < 0 || i3 < 0 || i5 > i3 || strings.Contains(d, `"$numberInt":"1"}`):
		t.Errorf("find with projection, sort and filter:\n%s", o)
	case !strings.Contains(part("stepe"), `"total":{"$numberInt":"9"}`):
		t.Errorf("aggregate:\n%s", o)
	case strings.TrimSpace(part("stepf")) != "1":
		t.Errorf("countDocuments with decimal and date:\n%s", o)
	case !strings.Contains(part("stepg"), `"d":{"$numberDecimal":"1.50"}`):
		t.Errorf("findOne by ObjectId:\n%s", o)
	case strings.TrimSpace(part("steph")) != "[5]":
		t.Errorf("distinct with a long:\n%s", o)
	}
}
