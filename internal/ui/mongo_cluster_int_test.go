//go:build dev && integration

package ui

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/db/dbtest"
)

// TestUIMongoCluster: the cluster view of the replica set and of the
// sharded cluster (mongos), and the export of a console statement to code.
func TestUIMongoCluster(t *testing.T) {
	tg := dbtest.Targets()["mongo"]
	dbtest.Prepare(t, tg)
	cfg := tg.Config
	cfg.ID, cfg.Name, cfg.SavePassword = "db-mongo-cluster", "mongo cluster", true
	shard := db.Config{ID: "db-mongos", Name: "mongos", Driver: "mongodb", Host: "127.0.0.1", Port: 55020, Database: "lumino_shard"}
	if c, err := net.DialTimeout("tcp", "127.0.0.1:55020", time.Second); err != nil {
		shard.ID = ""
	} else {
		c.Close()
	}
	cfgs := []db.Config{cfg}
	if shard.ID != "" {
		cfgs = append(cfgs, shard)
	}
	h := newHarness(t, cfgs, Options{Connection: cfg.Name})
	a := h.a
	h.waitFor("console", func() bool { return h.console() != nil && a.connState(cfg.ID).status == stReady })

	c := h.console()
	a.openCluster(cfg.ID)
	ct := a.activeTab().(*ClusterTab)
	h.waitFor("replica set", func() bool { h.frame(); return ct.rs != nil || ct.err != nil })
	if ct.err != nil || ct.topology != "replicaset" || ct.grids[0].model.Rows() != 1 || ct.grids[0].model.Value(0, 1) != "PRIMARY" {
		t.Fatalf("replica set %+v %v", ct.rs, ct.err)
	}
	tiles := map[string]string{}
	for _, tl := range ct.tiles() {
		tiles[tl[0]] = tl[1]
	}
	if tiles["Replica set"] != "rs0" || tiles["Healthy members"] != "1 / 1" || tiles["Oplog window"] == "" || tiles["Oplog size"] == "" {
		t.Fatalf("tiles %v", tiles)
	}
	h.shot("mongo-24-replica-set")
	ct.paused = true

	// export of the statement at the caret
	c.ed.SetText("db.customers.find({age: {$gte: 40}}, {name: 1}).sort({name: 1}).limit(5)")
	a.openCodeExport(cfg.ID, c.pickedSQL(), tg.Schema)
	cd := a.overlay.(*CodeDialog)
	h.frame()
	if cd.err != "" || !strings.Contains(cd.out.Text(), `coll := client.Database("`+tg.Schema+`").Collection("customers")`) ||
		!strings.Contains(cd.out.Text(), "SetLimit(5)") {
		t.Fatalf("go code %q %s", cd.out.Text(), cd.err)
	}
	cd.targetBtns[1].Click()
	cd.programBtn.Click()
	h.frame()
	if cd.err != "" || !strings.Contains(cd.out.Text(), "from pymongo import MongoClient") || !strings.Contains(cd.out.Text(), `os.environ["MONGODB_URI"]`) || !strings.Contains(cd.out.Text(), ".limit(5)") {
		t.Fatalf("python code %q %s", cd.out.Text(), cd.err)
	}
	h.shot("mongo-25-export-code")
	cd.done = true
	a.overlay = nil
	a.openCodeExport(cfg.ID, "let x = 1; db.a.find()", tg.Schema)
	cd = a.overlay.(*CodeDialog)
	h.frame()
	if cd.err == "" {
		t.Fatal("script exported")
	}
	a.overlay = nil

	if shard.ID == "" {
		t.Log("mongos (127.0.0.1:55020) is not running: sharding view not tested")
		return
	}
	a.connect(shard.ID, nil)
	h.waitFor("mongos", func() bool { return a.connState(shard.ID).status == stReady })
	a.openCluster(shard.ID)
	st := a.activeTab().(*ClusterTab)
	h.waitFor("sharding", func() bool { h.frame(); return st.sh != nil || st.err != nil })
	if st.err != nil || st.topology != "sharded" || st.grids[0].model.Rows() != 2 {
		t.Fatalf("sharding %+v %v", st.sh, st.err)
	}
	found := false
	m := st.grids[1].model
	for r := 0; r < m.Rows(); r++ {
		if m.Value(r, 0) == "lumino_shard.events" {
			found = m.Value(r, 1) == `{k: "hashed"}` && strings.Contains(m.Value(r, 4).(string), "sa: ")
		}
	}
	if !found {
		t.Fatalf("sharded collections %+v", st.sh.Collections)
	}
	h.shot("mongo-26-sharding")
	st.paused = true
}
