package mongo

// Replica set and sharded cluster status.

import (
	"context"
	"errors"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/twoojoo/lumino/internal/db"
)

func (c *conn) Topology() string { return c.topology }

// optime reads the time of an optime ({ts: Timestamp, t: term}).
func optime(v any) time.Time {
	if d, ok := v.(bson.D); ok {
		if ts, ok := fieldOr(d, "ts").(bson.Timestamp); ok {
			return time.Unix(int64(ts.T), 0)
		}
	}
	if dt, ok := v.(bson.DateTime); ok {
		return dt.Time()
	}
	return time.Time{}
}

func (c *conn) ReplicaSet(ctx context.Context) (*db.ReplicaSetStatus, error) {
	if c.topology != "replicaset" {
		return nil, errors.New("not a replica set member (" + c.topology + ")")
	}
	var d bson.D
	if err := c.cl.Database("admin").RunCommand(ctx, bson.D{{Key: "replSetGetStatus", Value: 1}}).Decode(&d); err != nil {
		return nil, mapError(err)
	}
	st := &db.ReplicaSetStatus{}
	st.Name, _ = fieldOr(d, "set").(string)
	members, _ := fieldOr(d, "members").(bson.A)
	var primary time.Time
	for _, m := range members {
		md, ok := m.(bson.D)
		if !ok {
			continue
		}
		rm := db.ReplicaMember{Uptime: num(fieldOr(md, "uptime")), PingMs: num(fieldOr(md, "pingMs")),
			Healthy: num(fieldOr(md, "health")) == 1, Self: fieldOr(md, "self") == true}
		rm.Name, _ = fieldOr(md, "name").(string)
		rm.State, _ = fieldOr(md, "stateStr").(string)
		rm.SyncSource, _ = fieldOr(md, "syncSourceHost").(string)
		rm.Message, _ = fieldOr(md, "lastHeartbeatMessage").(string)
		if s, ok := fieldOr(md, "infoMessage").(string); ok && rm.Message == "" {
			rm.Message = s
		}
		rm.Optime = optime(fieldOr(md, "optime"))
		if rm.Optime.IsZero() {
			rm.Optime = optime(fieldOr(md, "optimeDate"))
		}
		if rm.State == "PRIMARY" {
			primary = rm.Optime
		}
		st.Members = append(st.Members, rm)
	}
	for i := range st.Members {
		m := &st.Members[i]
		if !primary.IsZero() && !m.Optime.IsZero() && m.State == "SECONDARY" {
			m.Lag = max(0, primary.Sub(m.Optime))
		}
	}
	// the oplog window
	local := c.cl.Database("local")
	edge := func(dir int32) time.Time {
		var e bson.D
		err := local.RunCommand(ctx, bson.D{{Key: "find", Value: "oplog.rs"}, {Key: "sort", Value: bson.D{{Key: "$natural", Value: dir}}},
			{Key: "limit", Value: int64(1)}, {Key: "projection", Value: bson.D{{Key: "ts", Value: 1}}}, {Key: "singleBatch", Value: true}}).Decode(&e)
		if err != nil {
			return time.Time{}
		}
		cur, _ := fieldOr(e, "cursor").(bson.D)
		batch, _ := fieldOr(cur, "firstBatch").(bson.A)
		if len(batch) == 0 {
			return time.Time{}
		}
		doc, _ := batch[0].(bson.D)
		if ts, ok := fieldOr(doc, "ts").(bson.Timestamp); ok {
			return time.Unix(int64(ts.T), 0)
		}
		return time.Time{}
	}
	st.OplogFirst, st.OplogLast = edge(1), edge(-1)
	if ss, err := c.collStats(ctx, db.ObjectRef{Schema: "local", Name: "oplog.rs"}); err == nil {
		st.OplogSize, st.OplogMax = num(fieldOr(ss, "size")), num(fieldOr(ss, "maxSize"))
	}
	return st, nil
}

// cursorDocs reads every document of a command cursor.
func (c *conn) cursorDocs(ctx context.Context, dbName string, cmd bson.D) ([]bson.D, error) {
	cur, err := c.cl.Database(dbName).RunCommandCursor(ctx, cmd)
	if err != nil {
		return nil, mapError(err)
	}
	defer cur.Close(context.Background())
	var out []bson.D
	for cur.Next(ctx) {
		var d bson.D
		if err := bson.Unmarshal(cur.Current, &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, mapError(cur.Err())
}

func (c *conn) Sharding(ctx context.Context) (*db.ShardingStatus, error) {
	if c.topology != "sharded" {
		return nil, errors.New("not connected to a sharded cluster router (mongos)")
	}
	st := &db.ShardingStatus{}
	var ls bson.D
	if err := c.cl.Database("admin").RunCommand(ctx, bson.D{{Key: "listShards", Value: 1}}).Decode(&ls); err != nil {
		return nil, mapError(err)
	}
	shards, _ := fieldOr(ls, "shards").(bson.A)
	for _, s := range shards {
		sd, _ := s.(bson.D)
		sh := db.Shard{State: int(num(fieldOr(sd, "state"))), Draining: fieldOr(sd, "draining") == true}
		sh.ID, _ = fieldOr(sd, "_id").(string)
		sh.Host, _ = fieldOr(sd, "host").(string)
		st.Shards = append(st.Shards, sh)
	}
	var bs bson.D
	if err := c.cl.Database("admin").RunCommand(ctx, bson.D{{Key: "balancerStatus", Value: 1}}).Decode(&bs); err == nil {
		mode, _ := fieldOr(bs, "mode").(string)
		st.Balancer = mode
		if fieldOr(bs, "inBalancerRound") == true {
			st.Balancer += ", balancing now"
		}
	}
	config := "config"
	if routers, err := c.cursorDocs(ctx, config, bson.D{{Key: "find", Value: "mongos"}}); err == nil {
		for _, r := range routers {
			id, _ := fieldOr(r, "_id").(string)
			st.Routers = append(st.Routers, id)
		}
	}
	dbs, err := c.cursorDocs(ctx, config, bson.D{{Key: "find", Value: "databases"}, {Key: "sort", Value: bson.D{{Key: "_id", Value: int32(1)}}}})
	if err != nil {
		return nil, err
	}
	for _, d := range dbs {
		sd := db.ShardedDatabase{}
		sd.Name, _ = fieldOr(d, "_id").(string)
		sd.Primary, _ = fieldOr(d, "primary").(string)
		st.Databases = append(st.Databases, sd)
	}
	colls, err := c.cursorDocs(ctx, config, bson.D{{Key: "find", Value: "collections"}, {Key: "sort", Value: bson.D{{Key: "_id", Value: int32(1)}}}})
	if err != nil {
		return nil, err
	}
	// chunks per collection (uuid) and shard
	counts, _ := c.cursorDocs(ctx, config, bson.D{{Key: "aggregate", Value: "chunks"}, {Key: "pipeline", Value: bson.A{
		bson.D{{Key: "$group", Value: bson.D{{Key: "_id", Value: bson.D{{Key: "uuid", Value: "$uuid"}, {Key: "shard", Value: "$shard"}}},
			{Key: "n", Value: bson.D{{Key: "$sum", Value: int32(1)}}},
			{Key: "jumbo", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{bson.D{{Key: "$eq", Value: bson.A{"$jumbo", true}}}, int32(1), int32(0)}}}}}}}}},
	}}, {Key: "cursor", Value: bson.D{}}})
	for _, cd := range colls {
		ns, _ := fieldOr(cd, "_id").(string)
		if len(ns) > 7 && ns[:7] == "config." {
			continue // the cluster's own sessions collection
		}
		sc := db.ShardedCollection{NS: ns, Key: shellLit(fieldOr(cd, "key")), Unique: fieldOr(cd, "unique") == true,
			Balance: fieldOr(cd, "noBalance") != true, Chunks: map[string]int{}}
		uuid := shellLit(fieldOr(cd, "uuid"))
		for _, k := range counts {
			id, _ := fieldOr(k, "_id").(bson.D)
			if shellLit(fieldOr(id, "uuid")) != uuid {
				continue
			}
			shard, _ := fieldOr(id, "shard").(string)
			sc.Chunks[shard] += int(num(fieldOr(k, "n")))
			sc.Jumbo += int(num(fieldOr(k, "jumbo")))
		}
		st.Collections = append(st.Collections, sc)
	}
	sort.Slice(st.Collections, func(i, j int) bool { return st.Collections[i].NS < st.Collections[j].NS })
	return st, nil
}
