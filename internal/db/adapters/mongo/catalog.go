package mongo

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/twoojoo/lumino/internal/db"
)

// sampleSize is how many documents describe a collection's fields.
const sampleSize = 1000

// catalog reads databases, collections and their inferred fields.
type catalog struct{ c *conn }

var systemDBs = map[string]bool{"admin": true, "config": true, "local": true}

func (k *catalog) Catalogs(context.Context) ([]string, error) { return nil, db.ErrNotSupported }

func (k *catalog) Schemas(ctx context.Context, _ string) ([]db.Schema, error) {
	names, err := k.c.cl.ListDatabaseNames(ctx, bson.D{}, nil)
	if err != nil {
		// users without listDatabases see their own database
		names = []string{k.c.database}
	}
	if !contains(names, k.c.database) {
		names = append(names, k.c.database)
	}
	sort.Strings(names)
	out := make([]db.Schema, len(names))
	for i, n := range names {
		out[i] = db.Schema{Name: n, System: systemDBs[n], Current: n == k.c.database}
	}
	return out, nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func (k *catalog) dbName(ref db.ObjectRef) string {
	if ref.Schema != "" {
		return ref.Schema
	}
	return k.c.database
}

// collInfo is a listCollections entry.
type collInfo struct {
	name, typ string
	options   bson.D
}

func (k *catalog) collections(ctx context.Context, dbName string, filter bson.D) ([]collInfo, error) {
	cur, err := k.c.cl.Database(dbName).RunCommandCursor(ctx, bson.D{{Key: "listCollections", Value: 1}, {Key: "filter", Value: filter}}, k.c.readOpts()...)
	if err != nil {
		return nil, mapError(err)
	}
	defer cur.Close(context.Background())
	var out []collInfo
	for cur.Next(ctx) {
		var d bson.D
		if err := bson.Unmarshal(cur.Current, &d); err != nil {
			return nil, err
		}
		name, _ := fieldOr(d, "name").(string)
		typ, _ := fieldOr(d, "type").(string)
		opts, _ := fieldOr(d, "options").(bson.D)
		out = append(out, collInfo{name: name, typ: typ, options: opts})
	}
	return out, mapError(cur.Err())
}

func objType(typ string) db.ObjectType {
	if typ == "view" {
		return db.ObjView
	}
	return db.ObjCollection
}

func (k *catalog) Objects(ctx context.Context, schema db.ObjectRef, types ...db.ObjectType) ([]db.Object, error) {
	dbName := k.dbName(schema)
	colls, err := k.collections(ctx, dbName, bson.D{})
	if err != nil {
		return nil, err
	}
	want := func(t db.ObjectType) bool {
		if len(types) == 0 {
			return true
		}
		for _, x := range types {
			if x == t {
				return true
			}
		}
		return false
	}
	var out []db.Object
	for _, c := range colls {
		if strings.HasPrefix(c.name, "system.") || c.name == "" {
			continue
		}
		t := objType(c.typ)
		if !want(t) {
			continue
		}
		o := db.Object{Ref: db.ObjectRef{Schema: dbName, Name: c.name, Type: t}}
		switch {
		case c.typ == "timeseries":
			o.Extra = "time series"
		case fieldOr(c.options, "capped") == true:
			o.Extra = "capped"
		}
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.Name < out[j].Ref.Name })
	return out, nil
}

// sample reads up to n documents of a collection or view: random ones
// when the collection is bigger.
func (k *catalog) sample(ctx context.Context, dbName, coll string, n int, random bool) ([]bson.D, error) {
	var pipe bson.A
	if random {
		pipe = bson.A{bson.D{{Key: "$sample", Value: bson.D{{Key: "size", Value: n}}}}}
	} else {
		pipe = bson.A{bson.D{{Key: "$limit", Value: n}}}
	}
	cur, err := k.c.cl.Database(dbName).RunCommandCursor(ctx, bson.D{{Key: "aggregate", Value: coll}, {Key: "pipeline", Value: pipe}, {Key: "cursor", Value: bson.D{}}}, k.c.readOpts()...)
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

func (k *catalog) estimate(ctx context.Context, dbName, coll string) int64 {
	var d bson.D
	if err := k.c.cl.Database(dbName).RunCommand(ctx, bson.D{{Key: "count", Value: coll}}, k.c.readOpts()...).Decode(&d); err != nil {
		return -1
	}
	n, ok := argInt(fieldOr(d, "n"))
	if !ok {
		return -1
	}
	return n
}

func (k *catalog) info(ctx context.Context, ref db.ObjectRef) (collInfo, error) {
	cs, err := k.collections(ctx, k.dbName(ref), bson.D{{Key: "name", Value: ref.Name}})
	if err != nil {
		return collInfo{}, err
	}
	if len(cs) == 0 {
		return collInfo{}, fmt.Errorf("no collection %s in %s", ref.Name, k.dbName(ref))
	}
	return cs[0], nil
}

// Table describes a collection: its fields are inferred from a sample of
// documents (type names joined by | when they differ), _id is the key.
func (k *catalog) Table(ctx context.Context, ref db.ObjectRef) (*db.TableInfo, error) {
	dbName := k.dbName(ref)
	ci, err := k.info(ctx, ref)
	if err != nil {
		return nil, err
	}
	t := &db.TableInfo{Ref: db.ObjectRef{Schema: dbName, Name: ref.Name, Type: objType(ci.typ)}, RowEstimate: -1}
	if ci.typ != "view" {
		t.RowEstimate = k.estimate(ctx, dbName, ref.Name)
	}
	ds, err := k.sample(ctx, dbName, ref.Name, sampleSize, t.RowEstimate > sampleSize)
	if err != nil {
		return nil, err
	}
	cols, stats := inferColumns(ds)
	for i, c := range cols {
		tc := db.TableColumn{Name: c.Name, Type: stats[i].typeLabel(), Kind: c.Kind, Nullable: c.Nullable}
		if pct := 100 * stats[i].present / max(1, len(ds)); pct < 100 {
			tc.Comment = fmt.Sprintf("in %d%% of the sampled documents", pct)
		}
		if c.Name == "_id" && ci.typ != "view" {
			tc.PrimaryKeyNo = 1
			tc.Nullable = false
		}
		t.Columns = append(t.Columns, tc)
	}
	if ci.typ != "view" {
		t.PrimaryKey = []string{"_id"}
		if len(cols) == 0 || cols[0].Name != "_id" {
			// empty collection: the key is still there
			t.Columns = append([]db.TableColumn{{Name: "_id", Type: "objectId", Kind: db.KindObjectID, PrimaryKeyNo: 1}}, t.Columns...)
		}
		idx, err := k.indexes(ctx, dbName, ref.Name)
		if err != nil {
			return nil, err
		}
		t.Indexes = idx
	}
	if v, ok := field(ci.options, "validator"); ok {
		t.Constraints = append(t.Constraints, db.Constraint{Name: "validator", Type: "VALIDATOR", Definition: shellLit(v)})
	}
	if len(ds) > 0 {
		t.Comment = fmt.Sprintf("Fields inferred from %d sampled document(s)", len(ds))
	}
	t.Extra = append(t.Extra, [2]string{"Type", ci.typ})
	for _, e := range ci.options {
		if e.Key == "validator" {
			continue
		}
		t.Extra = append(t.Extra, [2]string{e.Key, shellLit(e.Value)})
	}
	return t, nil
}

// indexSpecs lists the index specifications of a collection.
func (k *catalog) indexSpecs(ctx context.Context, dbName, coll string) ([]bson.D, error) {
	cur, err := k.c.cl.Database(dbName).RunCommandCursor(ctx, bson.D{{Key: "listIndexes", Value: coll}}, k.c.readOpts()...)
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

func (k *catalog) indexes(ctx context.Context, dbName, coll string) ([]db.Index, error) {
	specs, err := k.indexSpecs(ctx, dbName, coll)
	if err != nil {
		return nil, err
	}
	var out []db.Index
	for _, s := range specs {
		name, _ := fieldOr(s, "name").(string)
		ix := db.Index{Name: name, Primary: name == "_id_"}
		keys, _ := fieldOr(s, "key").(bson.D)
		for _, e := range keys {
			switch v := e.Value.(type) {
			case string:
				ix.Columns = append(ix.Columns, e.Key)
				ix.Method = v // text, 2dsphere, hashed…
			default:
				if n, ok := toFloat(v); ok && n < 0 {
					ix.Columns = append(ix.Columns, e.Key+" DESC")
				} else {
					ix.Columns = append(ix.Columns, e.Key)
				}
			}
		}
		ix.Unique = fieldOr(s, "unique") == true || ix.Primary
		if p, ok := field(s, "partialFilterExpression"); ok {
			ix.Where = shellLit(p)
		}
		out = append(out, ix)
	}
	return out, nil
}

// DDL renders the shell statements recreating a collection (options and
// indexes) or a view.
func (k *catalog) DDL(ctx context.Context, ref db.ObjectRef) (string, error) {
	dbName := k.dbName(ref)
	ci, err := k.info(ctx, ref)
	if err != nil {
		return "", err
	}
	name := strconvQuote(ref.Name)
	if ci.typ == "view" {
		on := fieldOr(ci.options, "viewOn")
		pipe := fieldOr(ci.options, "pipeline")
		if pipe == nil {
			pipe = bson.A{}
		}
		rest := optionsOf(ci.options, "viewOn", "pipeline")
		s := fmt.Sprintf("db.createView(%s, %s, %s", name, shellLit(on), shellLit(pipe))
		if len(rest) > 0 {
			s += ", " + shellLit(rest)
		}
		return s + ");\n", nil
	}
	var b strings.Builder
	if len(ci.options) > 0 {
		fmt.Fprintf(&b, "db.createCollection(%s, %s);\n", name, shellLit(ci.options))
	} else {
		fmt.Fprintf(&b, "db.createCollection(%s);\n", name)
	}
	specs, err := k.indexSpecs(ctx, dbName, ref.Name)
	if err != nil {
		return "", err
	}
	coll := collExpr(ref)
	for _, s := range specs {
		if fieldOr(s, "name") == "_id_" {
			continue
		}
		keys := fieldOr(s, "key")
		opts := optionsOf(s, "key", "v", "ns")
		fmt.Fprintf(&b, "%s.createIndex(%s, %s);\n", coll, shellLit(keys), shellLit(opts))
	}
	return b.String(), nil
}

func strconvQuote(s string) string { return shellLit(s) }

// completionSample is how many documents give the completion fields.
const completionSample = 100

var _ db.FieldCompleter = (*catalog)(nil)

// Completion returns the collections of a database; their fields are
// sampled one collection at a time by CompletionFields.
func (k *catalog) Completion(ctx context.Context, schema db.ObjectRef) (map[string][]string, error) {
	colls, err := k.collections(ctx, k.dbName(schema), bson.D{})
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, c := range colls {
		if !strings.HasPrefix(c.name, "system.") {
			out[c.name] = nil
		}
	}
	return out, nil
}

// CompletionFields returns the fields (dotted paths of nested documents,
// two levels deep) of a collection, from a small sample.
func (k *catalog) CompletionFields(ctx context.Context, ref db.ObjectRef) ([]string, error) {
	ds, err := k.sample(ctx, k.dbName(ref), ref.Name, completionSample, false)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	fields := []string{}
	var walk func(prefix string, d bson.D, depth int)
	walk = func(prefix string, d bson.D, depth int) {
		for _, e := range d {
			p := prefix + e.Key
			if !seen[p] {
				seen[p] = true
				fields = append(fields, p)
			}
			if sub, ok := e.Value.(bson.D); ok && depth < 2 {
				walk(p+".", sub, depth+1)
			}
		}
	}
	for _, d := range ds {
		walk("", d, 0)
	}
	return fields, nil
}
