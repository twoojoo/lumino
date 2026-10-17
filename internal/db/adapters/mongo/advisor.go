package mongo

// Collection statistics, index usage with the useless indexes, and the
// slow operations recorded by the profiler.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/twoojoo/lumino/internal/db"
)

func num(v any) int64 {
	if n, ok := argInt(v); ok {
		return n
	}
	return 0
}

// collStats reads the storage statistics of a collection.
func (c *conn) collStats(ctx context.Context, ref db.ObjectRef) (bson.D, error) {
	cur, err := c.cl.Database(c.dbOf(ref)).Collection(ref.Name).Aggregate(ctx, bson.A{
		bson.D{{Key: "$collStats", Value: bson.D{{Key: "storageStats", Value: bson.D{}}}}},
	})
	if err != nil {
		return nil, mapError(err)
	}
	defer cur.Close(context.Background())
	if !cur.Next(ctx) {
		if err := cur.Err(); err != nil {
			return nil, mapError(err)
		}
		return nil, fmt.Errorf("no statistics for %s", ref.Name)
	}
	var d bson.D
	if err := bson.Unmarshal(cur.Current, &d); err != nil {
		return nil, err
	}
	ss, _ := fieldOr(d, "storageStats").(bson.D)
	return ss, nil
}

func (c *conn) CollectionStats(ctx context.Context, ref db.ObjectRef) (*db.CollectionStats, error) {
	ss, err := c.collStats(ctx, ref)
	if err != nil {
		return nil, err
	}
	return &db.CollectionStats{
		Documents: num(fieldOr(ss, "count")), DataSize: num(fieldOr(ss, "size")), StorageSize: num(fieldOr(ss, "storageSize")),
		IndexSize: num(fieldOr(ss, "totalIndexSize")), TotalSize: num(fieldOr(ss, "totalSize")), AvgDocSize: num(fieldOr(ss, "avgObjSize")),
		Indexes: int(num(fieldOr(ss, "nindexes"))), Capped: fieldOr(ss, "capped") == true,
	}, nil
}

// indexOptions are the options that make two indexes with the same keys
// different.
var indexOptions = []string{"unique", "sparse", "partialFilterExpression", "expireAfterSeconds", "collation", "weights", "hidden"}

func optionsKey(spec bson.D) string {
	var b strings.Builder
	for _, k := range indexOptions {
		if v, ok := field(spec, k); ok && k != "hidden" {
			b.WriteString(k + "=" + shellLit(v) + ";")
		}
	}
	return b.String()
}

// onlyFiltering reports whether the options of an index only restrict
// the documents it holds (sparse, partial): an index with the same keys
// and no options serves every query it serves.
func onlyFiltering(spec bson.D) bool {
	opts := optionsKey(spec)
	if opts == "" {
		return false
	}
	for _, part := range strings.Split(strings.TrimSuffix(opts, ";"), ";") {
		name, _, _ := strings.Cut(part, "=")
		if name != "sparse" && name != "partialFilterExpression" {
			return false
		}
	}
	return true
}

// prefixOf reports whether keys a are a strict prefix of keys b (same
// fields and directions).
func prefixOf(a, b bson.D) bool {
	if len(a) >= len(b) {
		return false
	}
	for i := range a {
		if a[i].Key != b[i].Key || shellLit(a[i].Value) != shellLit(b[i].Value) {
			return false
		}
	}
	return true
}

// indexIssues finds the useless indexes: never used, duplicates (same
// keys and options) and prefixes of another index (the longer one serves
// the same queries), the _id index and unique, partial, sparse or TTL
// ones apart (they have a job besides reads).
func indexIssues(us []db.IndexUsage, specs map[string]bson.D) {
	for i := range us {
		u := &us[i]
		if u.Name == "_id_" {
			continue
		}
		spec := specs[u.Name]
		keys, _ := fieldOr(spec, "key").(bson.D)
		special := optionsKey(spec) != ""
		for _, o := range us {
			if o.Name == u.Name || o.Name == "_id_" {
				continue
			}
			ospec := specs[o.Name]
			okeys, _ := fieldOr(ospec, "key").(bson.D)
			sameKeys := shellLit(okeys) == shellLit(keys)
			switch {
			case sameKeys && optionsKey(ospec) == optionsKey(spec) && o.Name < u.Name:
				u.Issue = "duplicate of " + o.Name
			case u.Issue == "" && sameKeys && optionsKey(ospec) == "" && onlyFiltering(spec):
				u.Issue = "same keys as " + o.Name + ", which covers every document"
			case u.Issue == "" && !special && prefixOf(keys, okeys):
				u.Issue = "prefix of " + o.Name + " (which serves the same queries)"
			}
		}
		if u.Issue == "" && u.Ops == 0 && !special {
			u.Issue = "never used since " + u.Since.Format("2006-01-02 15:04")
		}
	}
}

func (c *conn) IndexUsage(ctx context.Context, ref db.ObjectRef) ([]db.IndexUsage, error) {
	coll := c.cl.Database(c.dbOf(ref)).Collection(ref.Name)
	cur, err := coll.Aggregate(ctx, bson.A{bson.D{{Key: "$indexStats", Value: bson.D{}}}})
	if err != nil {
		return nil, mapError(err)
	}
	defer cur.Close(context.Background())
	sizes := bson.D{}
	if ss, err := c.collStats(ctx, ref); err == nil {
		sizes, _ = fieldOr(ss, "indexSizes").(bson.D)
	}
	specs := map[string]bson.D{}
	var out []db.IndexUsage
	for cur.Next(ctx) {
		var d bson.D
		if err := bson.Unmarshal(cur.Current, &d); err != nil {
			return nil, err
		}
		name, _ := fieldOr(d, "name").(string)
		spec, _ := fieldOr(d, "spec").(bson.D)
		specs[name] = spec
		u := db.IndexUsage{Name: name, Keys: shellLit(fieldOr(d, "key")), Size: num(fieldOr(sizes, name)),
			Unique: fieldOr(spec, "unique") == true || name == "_id_", Hidden: fieldOr(spec, "hidden") == true}
		if acc, ok := fieldOr(d, "accesses").(bson.D); ok {
			u.Ops = num(fieldOr(acc, "ops"))
			if t, ok := fieldOr(acc, "since").(bson.DateTime); ok {
				u.Since = t.Time()
			}
		}
		out = append(out, u)
	}
	if err := cur.Err(); err != nil {
		return nil, mapError(err)
	}
	// $indexStats lists the indexes of every node: one line per index
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	merged := out[:0]
	for _, u := range out {
		if n := len(merged); n > 0 && merged[n-1].Name == u.Name {
			merged[n-1].Ops += u.Ops
			continue
		}
		merged = append(merged, u)
	}
	indexIssues(merged, specs)
	return merged, nil
}

// ---- profiler ----

func (c *conn) Profiler(ctx context.Context, dbName string) (*db.ProfilerStatus, error) {
	var d bson.D
	if err := c.cl.Database(dbName).RunCommand(ctx, bson.D{{Key: "profile", Value: -1}}).Decode(&d); err != nil {
		return nil, mapError(err)
	}
	return &db.ProfilerStatus{Level: int(num(fieldOr(d, "was"))), SlowMs: num(fieldOr(d, "slowms"))}, nil
}

// profiledFind is the filter and sort an operation recorded by the
// profiler ran (find, count, aggregate starting with $match, update and
// remove), as a find command for index suggestions.
func profiledFind(cmd bson.D) (bson.D, bool) {
	if len(cmd) == 0 {
		return nil, false
	}
	var filter, sortKeys bson.D
	switch cmd[0].Key {
	case "find":
		filter, _ = fieldOr(cmd, "filter").(bson.D)
		sortKeys, _ = fieldOr(cmd, "sort").(bson.D)
	case "count":
		filter, _ = fieldOr(cmd, "query").(bson.D)
	case "aggregate":
		p, _ := fieldOr(cmd, "pipeline").(bson.A)
		if len(p) == 0 {
			return nil, false
		}
		st, _ := p[0].(bson.D)
		if len(st) == 0 || st[0].Key != "$match" {
			return nil, false
		}
		filter, _ = st[0].Value.(bson.D)
	default:
		q, ok := fieldOr(cmd, "q").(bson.D) // update / remove: {q: …, u: …}
		if !ok {
			return nil, false
		}
		filter = q
	}
	find := bson.D{{Key: "find", Value: ""}, {Key: "filter", Value: filter}}
	if len(sortKeys) > 0 {
		find = append(find, bson.E{Key: "sort", Value: sortKeys})
	}
	return find, true
}

// withoutSessionFields drops the fields the drivers add to commands
// (session, cluster time, read preference): they say nothing about the
// query.
func withoutSessionFields(cmd bson.D) bson.D {
	out := make(bson.D, 0, len(cmd))
	for _, e := range cmd {
		switch e.Key {
		case "lsid", "$clusterTime", "$db", "$readPreference", "txnNumber", "autocommit", "startTransaction", "apiVersion":
			continue
		}
		out = append(out, e)
	}
	return out
}

func (c *conn) SlowQueries(ctx context.Context, dbName string, limit int) ([]db.SlowQuery, error) {
	if limit <= 0 {
		limit = 200
	}
	database := c.cl.Database(dbName)
	cur, err := database.RunCommandCursor(ctx, bson.D{{Key: "find", Value: "system.profile"},
		{Key: "filter", Value: bson.D{{Key: "ns", Value: bson.D{{Key: "$not", Value: bson.Regex{Pattern: `\.system\.profile$`}}}}}},
		{Key: "sort", Value: bson.D{{Key: "ts", Value: int32(-1)}}}, {Key: "limit", Value: int64(limit)}})
	if err != nil {
		return nil, mapError(err)
	}
	defer cur.Close(context.Background())
	specCache := map[string][]bson.D{}
	var out []db.SlowQuery
	for cur.Next(ctx) {
		var d bson.D
		if err := bson.Unmarshal(cur.Current, &d); err != nil {
			return nil, err
		}
		q := db.SlowQuery{Millis: num(fieldOr(d, "millis")), DocsExamined: num(fieldOr(d, "docsExamined")),
			KeysExamined: num(fieldOr(d, "keysExamined")), Returned: num(fieldOr(d, "nreturned"))}
		q.NS, _ = fieldOr(d, "ns").(string)
		q.Op, _ = fieldOr(d, "op").(string)
		q.Plan, _ = fieldOr(d, "planSummary").(string)
		if t, ok := fieldOr(d, "ts").(bson.DateTime); ok {
			q.At = t.Time()
		}
		cmd, _ := fieldOr(d, "command").(bson.D)
		q.Command = shellLit(withoutSessionFields(cmd))
		if len(q.Command) > 400 {
			q.Command = q.Command[:400] + "…"
		}
		collName := strings.TrimPrefix(q.NS, dbName+".")
		if find, ok := profiledFind(cmd); ok && collName != "" {
			ref := db.ObjectRef{Schema: dbName, Name: collName, Type: db.ObjCollection}
			plan := db.PlanNode{Stage: strings.Fields(q.Plan + " ?")[0]}
			if fieldOr(d, "hasSortStage") == true {
				plan = db.PlanNode{Stage: "SORT", Children: []db.PlanNode{plan}}
			}
			specs, cached := specCache[collName]
			if !cached {
				specs, _ = c.cat.indexSpecs(ctx, dbName, collName)
				specCache[collName] = specs
			}
			rep := &db.ExplainReport{Plan: plan, DocsExamined: q.DocsExamined, Returned: q.Returned}
			if s := suggestIndexes(ref, find, rep, specs, c.lang); len(s) > 0 {
				q.Suggestion = &s[0]
			}
		}
		out = append(out, q)
	}
	return out, mapError(cur.Err())
}
