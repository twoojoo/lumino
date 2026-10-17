package mongo

// Management features (db.DocumentTools): schema analysis, explain with
// index suggestions, server status, running operations, change streams,
// import and export.

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/twoojoo/lumino/internal/db"
)

var _ db.DocumentTools = (*conn)(nil)

func (c *conn) dbOf(ref db.ObjectRef) string {
	if ref.Schema != "" {
		return ref.Schema
	}
	return c.database
}

// ---- schema analysis ----

// maxDistinct bounds the values counted per field.
const maxDistinct = 1000

type fieldAcc struct {
	path     string
	present  int
	types    map[string]int
	min, max any
	values   map[string]int
	capped   bool
}

// compareBSON orders values of the same family (numbers, dates, strings);
// ok is false for values that don't compare.
func compareBSON(a, b any) (int, bool) {
	fa, okA := numberOf(a)
	fb, okB := numberOf(b)
	if okA && okB {
		return cmpFloat(fa, fb), true
	}
	if da, ok := a.(bson.DateTime); ok {
		if dbt, ok := b.(bson.DateTime); ok {
			return cmpFloat(float64(da), float64(dbt)), true
		}
	}
	if sa, ok := a.(string); ok {
		if sb, ok := b.(string); ok {
			return strings.Compare(sa, sb), true
		}
	}
	return 0, false
}

func numberOf(v any) (float64, bool) {
	switch x := v.(type) {
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case float64:
		return x, !math.IsNaN(x)
	case bson.Decimal128:
		f, err := strconv.ParseFloat(x.String(), 64)
		return f, err == nil
	}
	return 0, false
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// analyze accumulates the fields of documents (nested documents and the
// documents inside arrays, as "a.b" and "list[].x"), in order of first
// appearance.
func analyze(docs []bson.D) []*fieldAcc {
	var order []*fieldAcc
	by := map[string]*fieldAcc{}
	var walk func(prefix string, d bson.D, depth int, seen map[string]bool)
	add := func(path string, v any, seen map[string]bool) {
		f := by[path]
		if f == nil {
			f = &fieldAcc{path: path, types: map[string]int{}, values: map[string]int{}}
			by[path] = f
			order = append(order, f)
		}
		if !seen[path] {
			seen[path] = true
			f.present++
		}
		f.types[typeName(v)]++
		switch v.(type) {
		case bson.D, bson.A:
			return
		}
		if c, ok := compareBSON(v, f.min); f.min == nil || ok && c < 0 {
			if _, comparable := compareBSON(v, v); comparable {
				f.min = v
			}
		}
		if c, ok := compareBSON(v, f.max); f.max == nil || ok && c > 0 {
			if _, comparable := compareBSON(v, v); comparable {
				f.max = v
			}
		}
		key := shellLit(v)
		if len(key) > 120 {
			key = key[:120] + "…"
		}
		if _, ok := f.values[key]; ok || len(f.values) < maxDistinct {
			f.values[key]++
		} else {
			f.capped = true
		}
	}
	walk = func(prefix string, d bson.D, depth int, seen map[string]bool) {
		for _, e := range d {
			path := prefix + e.Key
			add(path, e.Value, seen)
			if depth >= 4 {
				continue
			}
			switch x := e.Value.(type) {
			case bson.D:
				walk(path+".", x, depth+1, seen)
			case bson.A:
				for _, item := range x {
					add(path+"[]", item, seen)
					if sub, ok := item.(bson.D); ok {
						walk(path+"[].", sub, depth+1, seen)
					}
				}
			}
		}
	}
	for _, d := range docs {
		walk("", d, 0, map[string]bool{})
	}
	return order
}

func (c *conn) AnalyzeSchema(ctx context.Context, ref db.ObjectRef, sample int) (*db.SchemaReport, error) {
	if sample <= 0 {
		sample = sampleSize
	}
	dbName := c.dbOf(ref)
	total := c.cat.estimate(ctx, dbName, ref.Name)
	docs, err := c.cat.sample(ctx, dbName, ref.Name, sample, total > int64(sample))
	if err != nil {
		return nil, err
	}
	rep := &db.SchemaReport{Sampled: len(docs), Total: total}
	for _, f := range analyze(docs) {
		sf := db.SchemaField{Path: f.path, Distinct: len(f.values)}
		parentPresence := len(docs)
		if strings.Contains(f.path, "[]") {
			parentPresence = f.present // array items: share among documents having them
		}
		sf.Presence = float64(f.present) / float64(max(1, parentPresence))
		n := 0
		for _, k := range f.types {
			n += k
		}
		for t, k := range f.types {
			sf.Types = append(sf.Types, db.TypeShare{Type: t, Share: float64(k) / float64(max(1, n))})
		}
		sort.Slice(sf.Types, func(i, j int) bool {
			if sf.Types[i].Share != sf.Types[j].Share {
				return sf.Types[i].Share > sf.Types[j].Share
			}
			return sf.Types[i].Type < sf.Types[j].Type
		})
		if f.min != nil {
			sf.Min, sf.Max = shellLit(f.min), shellLit(f.max)
		}
		for v, k := range f.values {
			sf.Top = append(sf.Top, db.ValueCount{Value: v, Count: k})
		}
		sort.Slice(sf.Top, func(i, j int) bool {
			if sf.Top[i].Count != sf.Top[j].Count {
				return sf.Top[i].Count > sf.Top[j].Count
			}
			return sf.Top[i].Value < sf.Top[j].Value
		})
		if len(sf.Top) > 5 {
			sf.Top = sf.Top[:5]
		}
		rep.Fields = append(rep.Fields, sf)
	}
	return rep, nil
}

// ---- explain ----

// findCommandOf builds the find command of browse parameters.
func findCommandOf(coll string, q db.BrowseQuery, lang *language) (bson.D, error) {
	f, err := parseDocument(q.Filter)
	if err != nil {
		return nil, fmt.Errorf("filter: %w", err)
	}
	cmd := bson.D{{Key: "find", Value: coll}, {Key: "filter", Value: f}}
	if strings.TrimSpace(q.Projection) != "" {
		p, err := parseDocument(q.Projection)
		if err != nil {
			return nil, fmt.Errorf("projection: %w", err)
		}
		cmd = append(cmd, bson.E{Key: "projection", Value: p})
	}
	if len(q.Sort) > 0 {
		s, _ := parseDocument(lang.FormatSort(q.Sort))
		cmd = append(cmd, bson.E{Key: "sort", Value: s})
	}
	if q.Limit > 0 {
		cmd = append(cmd, bson.E{Key: "limit", Value: q.Limit})
	}
	return cmd, nil
}

func (c *conn) ExplainFind(ctx context.Context, ref db.ObjectRef, q db.BrowseQuery) (*db.ExplainReport, error) {
	cmd, err := findCommandOf(ref.Name, q, c.lang)
	if err != nil {
		return nil, err
	}
	stmt, _ := c.lang.Browse(ref, q)
	var d bson.D
	err = c.cl.Database(c.dbOf(ref)).RunCommand(ctx, bson.D{{Key: "explain", Value: cmd}, {Key: "verbosity", Value: "executionStats"}}).Decode(&d)
	if err != nil {
		return nil, mapError(err)
	}
	rep := &db.ExplainReport{Statement: stmt}
	es, _ := fieldOr(d, "executionStats").(bson.D)
	rep.Returned, _ = argInt(fieldOr(es, "nReturned"))
	rep.DocsExamined, _ = argInt(fieldOr(es, "totalDocsExamined"))
	rep.KeysExamined, _ = argInt(fieldOr(es, "totalKeysExamined"))
	rep.Millis, _ = argInt(fieldOr(es, "executionTimeMillis"))
	if st, ok := fieldOr(es, "executionStages").(bson.D); ok {
		rep.Plan = planNode(st)
	} else if qp, ok := fieldOr(d, "queryPlanner").(bson.D); ok {
		wp, _ := fieldOr(qp, "winningPlan").(bson.D)
		if inner, ok := fieldOr(wp, "queryPlan").(bson.D); ok {
			wp = inner // slot-based engine
		}
		rep.Plan = planNode(wp)
	}
	specs, _ := c.cat.indexSpecs(ctx, c.dbOf(ref), ref.Name)
	rep.Suggestions = suggestIndexes(ref, cmd, rep, specs, c.lang)
	return rep, nil
}

// planNode reads a stage of a plan (execution stats when present).
func planNode(st bson.D) db.PlanNode {
	n := db.PlanNode{}
	n.Stage, _ = fieldOr(st, "stage").(string)
	n.Index, _ = fieldOr(st, "indexName").(string)
	n.NReturned, _ = argInt(fieldOr(st, "nReturned"))
	n.DocsExamined, _ = argInt(fieldOr(st, "docsExamined"))
	n.KeysExamined, _ = argInt(fieldOr(st, "keysExamined"))
	n.Millis, _ = argInt(fieldOr(st, "executionTimeMillisEstimate"))
	var details []string
	for _, k := range []string{"keyPattern", "filter", "sortPattern", "indexBounds", "limitAmount", "skipAmount", "transformBy"} {
		if v, ok := field(st, k); ok {
			s := shellLit(v)
			if len(s) > 200 {
				s = s[:200] + "…"
			}
			details = append(details, k+": "+s)
		}
	}
	n.Details = strings.Join(details, "  ·  ")
	if in, ok := fieldOr(st, "inputStage").(bson.D); ok {
		n.Children = append(n.Children, planNode(in))
	}
	if ins, ok := fieldOr(st, "inputStages").(bson.A); ok {
		for _, x := range ins {
			if d, ok := x.(bson.D); ok {
				n.Children = append(n.Children, planNode(d))
			}
		}
	}
	return n
}

func hasStage(n db.PlanNode, stage string) bool {
	if n.Stage == stage {
		return true
	}
	for _, c := range n.Children {
		if hasStage(c, stage) {
			return true
		}
	}
	return false
}

// rangeOps are the operators that make a field a range predicate.
var rangeOps = map[string]bool{"$gt": true, "$gte": true, "$lt": true, "$lte": true, "$ne": true, "$nin": true,
	"$regex": true, "$exists": true, "$not": true, "$type": true}

// suggestIndexes proposes an index following the equality, sort, range
// rule when the plan scans the collection, sorts in memory or examines
// many more documents than it returns.
func suggestIndexes(ref db.ObjectRef, find bson.D, rep *db.ExplainReport, specs []bson.D, lang *language) []db.IndexSuggestion {
	var reason string
	switch {
	case hasStage(rep.Plan, "COLLSCAN") && rep.DocsExamined > 0:
		reason = fmt.Sprintf("The query scans the collection: %d documents examined for %d returned.", rep.DocsExamined, rep.Returned)
	case hasStage(rep.Plan, "SORT"):
		reason = "The results are sorted in memory: an index can return them in order."
	case rep.DocsExamined > 10*max(1, rep.Returned) && rep.DocsExamined > 100:
		reason = fmt.Sprintf("The index used is not selective: %d documents examined for %d returned.", rep.DocsExamined, rep.Returned)
	default:
		return nil
	}
	filter, _ := fieldOr(find, "filter").(bson.D)
	var eq, rng []string
	var walk func(d bson.D)
	walk = func(d bson.D) {
		for _, e := range d {
			if e.Key == "$and" {
				if a, ok := e.Value.(bson.A); ok {
					for _, x := range a {
						if sub, ok := x.(bson.D); ok {
							walk(sub)
						}
					}
				}
				continue
			}
			if strings.HasPrefix(e.Key, "$") {
				continue // $or, $expr…: not indexable as a prefix
			}
			isRange := false
			if ops, ok := e.Value.(bson.D); ok && len(ops) > 0 && strings.HasPrefix(ops[0].Key, "$") {
				for _, op := range ops {
					if rangeOps[op.Key] {
						isRange = true
					}
				}
			}
			if _, isRegex := e.Value.(bson.Regex); isRegex {
				isRange = true
			}
			if isRange {
				rng = append(rng, e.Key)
			} else {
				eq = append(eq, e.Key)
			}
		}
	}
	walk(filter)
	keys := bson.D{}
	seen := map[string]bool{}
	addKey := func(k string, dir int32) {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, bson.E{Key: k, Value: dir})
		}
	}
	for _, k := range eq {
		addKey(k, 1)
	}
	if s, ok := fieldOr(find, "sort").(bson.D); ok {
		for _, e := range s {
			dir := int32(1)
			if n, _ := toFloat(e.Value); n < 0 {
				dir = -1
			}
			addKey(e.Key, dir)
		}
	}
	for _, k := range rng {
		addKey(k, 1)
	}
	if len(keys) == 0 {
		return nil
	}
	// an existing index starting with these keys already serves the query
	want := shellLit(keys)
	for _, sp := range specs {
		if k, ok := fieldOr(sp, "key").(bson.D); ok && len(k) >= len(keys) && shellLit(k[:len(keys)]) == want {
			return nil
		}
	}
	stmt, _ := lang.AdminStatement("createIndex", ref, want, "{}")
	return []db.IndexSuggestion{{Reason: reason + " Equality fields first, then the sort, then ranges:", Keys: want, Statement: stmt}}
}

// ---- server status and operations ----

func path(d bson.D, p string) any {
	v, _ := lookup(d, p)
	return v
}

func (c *conn) ServerStats(ctx context.Context) (*db.ServerStats, error) {
	var d bson.D
	if err := c.cl.Database("admin").RunCommand(ctx, bson.D{{Key: "serverStatus", Value: 1}}).Decode(&d); err != nil {
		return nil, mapError(err)
	}
	st := &db.ServerStats{Counters: map[string]float64{}}
	st.Version, _ = path(d, "version").(string)
	st.Host, _ = path(d, "host").(string)
	if f, ok := numberOf(path(d, "uptime")); ok {
		st.Uptime = int64(f)
	}
	switch {
	case path(d, "repl.setName") == nil:
		st.Role = "standalone"
	case path(d, "repl.isWritablePrimary") == true || path(d, "repl.ismaster") == true:
		st.Role = "primary of " + fmt.Sprint(path(d, "repl.setName"))
	default:
		st.Role = "secondary of " + fmt.Sprint(path(d, "repl.setName"))
	}
	gauge := func(name, p string, unit string) {
		if f, ok := numberOf(path(d, p)); ok {
			st.Gauges = append(st.Gauges, [2]string{name, strconv.FormatFloat(f, 'f', -1, 64) + unit})
		}
	}
	gauge("Connections", "connections.current", "")
	gauge("Available connections", "connections.available", "")
	gauge("Active clients", "globalLock.activeClients.total", "")
	gauge("Queued operations", "globalLock.currentQueue.total", "")
	gauge("Resident memory", "mem.resident", " MB")
	gauge("Virtual memory", "mem.virtual", " MB")
	if used, ok := numberOf(path(d, "wiredTiger.cache.bytes currently in the cache")); ok {
		if mx, ok := numberOf(path(d, "wiredTiger.cache.maximum bytes configured")); ok && mx > 0 {
			st.Gauges = append(st.Gauges, [2]string{"WiredTiger cache", fmt.Sprintf("%s of %s", db.HumanBytes(int64(used)), db.HumanBytes(int64(mx)))})
		}
	}
	for _, k := range []string{"insert", "query", "update", "delete", "getmore", "command"} {
		if f, ok := numberOf(path(d, "opcounters."+k)); ok {
			st.Counters["ops."+k] = f
		}
	}
	for _, k := range []string{"bytesIn", "bytesOut", "numRequests"} {
		if f, ok := numberOf(path(d, "network."+k)); ok {
			st.Counters["net."+k] = f
		}
	}
	return st, nil
}

func (c *conn) Operations(ctx context.Context, all bool) ([]db.Operation, error) {
	match := bson.D{}
	if !all {
		match = bson.D{{Key: "active", Value: true}}
	}
	cur, err := c.cl.Database("admin").Aggregate(ctx, bson.A{
		bson.D{{Key: "$currentOp", Value: bson.D{{Key: "allUsers", Value: true}, {Key: "idleConnections", Value: all}}}},
		bson.D{{Key: "$match", Value: match}},
	})
	if err != nil {
		return nil, mapError(err)
	}
	defer cur.Close(context.Background())
	var out []db.Operation
	for cur.Next(ctx) {
		var d bson.D
		if err := bson.Unmarshal(cur.Current, &d); err != nil {
			return nil, err
		}
		op := db.Operation{}
		if id, ok := field(d, "opid"); ok {
			op.ID = shellLit(id)
		}
		op.Op, _ = fieldOr(d, "op").(string)
		op.NS, _ = fieldOr(d, "ns").(string)
		op.Client, _ = fieldOr(d, "client").(string)
		op.Desc, _ = fieldOr(d, "desc").(string)
		op.Seconds, _ = argInt(fieldOr(d, "secs_running"))
		if cmd, ok := field(d, "command"); ok {
			op.Command = shellLit(cmd)
			if len(op.Command) > 300 {
				op.Command = op.Command[:300] + "…"
			}
		}
		out = append(out, op)
	}
	return out, mapError(cur.Err())
}

// ---- change streams ----

func (c *conn) Watch(ctx context.Context, ref db.ObjectRef, pipeline string, ready func(), fn func(db.ChangeEvent)) error {
	pipe := bson.A{}
	if strings.TrimSpace(pipeline) != "" {
		v, err := parseValue(pipeline)
		if err != nil {
			return fmt.Errorf("pipeline: %w", err)
		}
		a, ok := v.(bson.A)
		if !ok {
			return errors.New("pipeline: an array of stages is needed")
		}
		pipe = a
	}
	opts := options.ChangeStream().SetFullDocument(options.UpdateLookup)
	database := c.cl.Database(c.dbOf(ref))
	var err error
	type stream interface {
		Next(context.Context) bool
		Decode(any) error
		Err() error
		Close(context.Context) error
	}
	var cs stream
	if ref.Name == "" {
		cs, err = database.Watch(ctx, pipe, opts)
	} else {
		cs, err = database.Collection(ref.Name).Watch(ctx, pipe, opts)
	}
	if err != nil {
		return mapError(err)
	}
	defer cs.Close(context.Background())
	// the server opened the stream: later changes are delivered
	if ready != nil {
		ready()
	}
	for cs.Next(ctx) {
		var d bson.D
		if err := cs.Decode(&d); err != nil {
			return err
		}
		ev := db.ChangeEvent{Doc: docNode("", d)}
		ev.Op, _ = fieldOr(d, "operationType").(string)
		if ns, ok := fieldOr(d, "ns").(bson.D); ok {
			ev.NS = fmt.Sprintf("%v.%v", fieldOr(ns, "db"), fieldOr(ns, "coll"))
		}
		if k, ok := fieldOr(d, "documentKey").(bson.D); ok {
			if id, ok := field(k, "_id"); ok {
				ev.Key = shellLit(id)
			}
		}
		if ud, ok := fieldOr(d, "updateDescription").(bson.D); ok {
			var fs []string
			if u, ok := fieldOr(ud, "updatedFields").(bson.D); ok {
				for _, e := range u {
					fs = append(fs, e.Key)
				}
			}
			if r, ok := fieldOr(ud, "removedFields").(bson.A); ok {
				for _, e := range r {
					fs = append(fs, fmt.Sprint("-", e))
				}
			}
			ev.Fields = strings.Join(fs, ", ")
		}
		fn(ev)
	}
	if ctx.Err() != nil {
		return nil
	}
	return mapError(cs.Err())
}

// ---- import / export ----

// importBatch is how many documents are inserted at once.
const importBatch = 1000

func (c *conn) Import(ctx context.Context, ref db.ObjectRef, r io.Reader, o db.ImportOptions, progress func(int64)) (int64, error) {
	coll := c.cl.Database(c.dbOf(ref)).Collection(ref.Name)
	if o.Drop {
		if err := coll.Drop(ctx); err != nil {
			return 0, mapError(err)
		}
	}
	var n int64
	var batch []any
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if _, err := coll.InsertMany(ctx, batch); err != nil {
			return fmt.Errorf("documents %d-%d: %w", n+1, n+int64(len(batch)), mapError(err))
		}
		n += int64(len(batch))
		batch = batch[:0]
		if progress != nil {
			progress(n)
		}
		return nil
	}
	emit := func(d bson.D) error {
		batch = append(batch, d)
		if len(batch) >= importBatch {
			return flush()
		}
		return nil
	}
	var err error
	switch o.Format {
	case "json":
		err = readJSON(r, emit)
	case "csv":
		err = readCSV(r, o.CSVTypes, emit)
	case "bson":
		err = readBSON(r, emit)
	default:
		return 0, fmt.Errorf("unknown import format %q", o.Format)
	}
	if err == nil {
		err = flush()
	}
	return n, err
}

// readJSON reads an array of documents or one document per line
// (Extended JSON, relaxed or canonical).
func readJSON(r io.Reader, emit func(bson.D) error) error {
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		b, err := br.Peek(1)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if b[0] == ' ' || b[0] == '\n' || b[0] == '\r' || b[0] == '\t' {
			br.ReadByte()
			continue
		}
		break
	}
	dec := json.NewDecoder(br)
	one := func(raw json.RawMessage, i int) error {
		var d bson.D
		if err := bson.UnmarshalExtJSON(raw, false, &d); err != nil {
			return fmt.Errorf("document %d: %w", i, err)
		}
		return emit(d)
	}
	if b, _ := br.Peek(1); len(b) == 1 && b[0] == '[' {
		if _, err := dec.Token(); err != nil {
			return err
		}
		for i := 1; dec.More(); i++ {
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				return fmt.Errorf("document %d: %w", i, err)
			}
			if err := one(raw, i); err != nil {
				return err
			}
		}
		return nil
	}
	for i := 1; ; i++ {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err == io.EOF {
			return nil
		} else if err != nil {
			return fmt.Errorf("document %d: %w", i, err)
		}
		if err := one(raw, i); err != nil {
			return err
		}
	}
}

// csvValue types a CSV field (numbers, booleans, ISO dates).
func csvValue(s string) any {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return intValue(n)
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && !strings.ContainsAny(s, "xX") {
		return f
	}
	switch s {
	case "true":
		return true
	case "false":
		return false
	}
	if len(s) >= 10 && s[4] == '-' && s[7] == '-' {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return bson.NewDateTimeFromTime(t)
		}
		if t, err := time.Parse("2006-01-02", s); err == nil {
			return bson.NewDateTimeFromTime(t)
		}
	}
	return s
}

// readCSV reads a header line (dotted names make nested documents) and
// one document per line.
func readCSV(r io.Reader, typed bool, emit func(bson.D) error) error {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	head, err := cr.Read()
	if err != nil {
		if err == io.EOF {
			return nil
		}
		return err
	}
	if len(head) > 0 {
		head[0] = strings.TrimPrefix(head[0], "\ufeff")
	}
	for line := 2; ; line++ {
		rec, err := cr.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
		d := bson.D{}
		for i, v := range rec {
			if i >= len(head) || head[i] == "" {
				continue
			}
			var val any = v
			if typed {
				if v == "" {
					continue
				}
				val = csvValue(v)
			}
			d = setPath(d, head[i], val)
		}
		if err := emit(d); err != nil {
			return err
		}
	}
}

// readBSON reads concatenated BSON documents (mongodump files).
func readBSON(r io.Reader, emit func(bson.D) error) error {
	br := bufio.NewReaderSize(r, 1<<20)
	for i := 1; ; i++ {
		var size [4]byte
		if _, err := io.ReadFull(br, size[:]); err == io.EOF {
			return nil
		} else if err != nil {
			return fmt.Errorf("document %d: %w", i, err)
		}
		n := binary.LittleEndian.Uint32(size[:])
		if n < 5 || n > 16<<20 {
			return fmt.Errorf("document %d: invalid size %d (not a BSON file?)", i, n)
		}
		buf := make([]byte, n)
		copy(buf, size[:])
		if _, err := io.ReadFull(br, buf[4:]); err != nil {
			return fmt.Errorf("document %d: %w", i, err)
		}
		var d bson.D
		if err := bson.Unmarshal(buf, &d); err != nil {
			return fmt.Errorf("document %d: %w", i, err)
		}
		if err := emit(d); err != nil {
			return err
		}
	}
}

func (c *conn) ExportDocument(d db.DocNode, format string) ([]byte, error) {
	doc, err := parseDocument(c.lang.FormatDocument(d))
	if err != nil {
		return nil, err
	}
	switch format {
	case "ejson":
		return bson.MarshalExtJSON(doc, false, false)
	case "ejson-canonical":
		return bson.MarshalExtJSON(doc, true, false)
	case "bson":
		return bson.Marshal(doc)
	}
	return nil, fmt.Errorf("unknown export format %q", format)
}
