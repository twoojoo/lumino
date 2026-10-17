package cassandra

import (
	"context"
	"fmt"
	"sort"
	"strings"

	gocql "github.com/apache/cassandra-gocql-driver/v2"

	"github.com/twoojoo/lumino/internal/db"
)

type catalog struct {
	s *gocql.Session
	d *dialect
}

type rec map[string]any

func (r rec) str(k string) string {
	switch v := r[k].(type) {
	case nil:
		return ""
	case string:
		return v
	}
	return fmt.Sprint(r[k])
}

func (r rec) num(k string) int {
	switch v := r[k].(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	}
	return 0
}

func (c *catalog) query(ctx context.Context, q string, args ...any) ([]rec, error) {
	ctx, cancel := context.WithTimeout(ctx, db.MetaTimeout)
	defer cancel()
	it := c.s.Query(q, args...).WithContext(ctx).Iter()
	var out []rec
	for {
		m := map[string]any{}
		if !it.MapScan(m) {
			break
		}
		out = append(out, m)
	}
	return out, mapError(it.Close())
}

func (c *catalog) Catalogs(ctx context.Context) ([]string, error) { return nil, db.ErrNotSupported }

func isSystem(ks string) bool {
	return ks == "system" || strings.HasPrefix(ks, "system_") || ks == "dse_system" || ks == "solr_admin"
}

func (c *catalog) Schemas(ctx context.Context, _ string) ([]db.Schema, error) {
	recs, err := c.query(ctx, "SELECT keyspace_name FROM system_schema.keyspaces")
	if err != nil {
		return nil, err
	}
	out := make([]db.Schema, len(recs))
	for i, r := range recs {
		n := r.str("keyspace_name")
		out[i] = db.Schema{Name: n, System: isSystem(n)}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].System != out[j].System {
			return !out[i].System
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func wants(types []db.ObjectType, t db.ObjectType) bool {
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

func (c *catalog) Objects(ctx context.Context, schema db.ObjectRef, types ...db.ObjectType) ([]db.Object, error) {
	var out []db.Object
	ks := schema.Schema
	if wants(types, db.ObjTable) {
		recs, err := c.query(ctx, "SELECT table_name, comment FROM system_schema.tables WHERE keyspace_name = ?", ks)
		if err != nil {
			return nil, err
		}
		for _, r := range recs {
			out = append(out, db.Object{Ref: db.ObjectRef{Schema: ks, Name: r.str("table_name"), Type: db.ObjTable}, Comment: r.str("comment")})
		}
	}
	if wants(types, db.ObjMatView) {
		recs, err := c.query(ctx, "SELECT view_name, base_table_name FROM system_schema.views WHERE keyspace_name = ?", ks)
		if err != nil {
			return nil, err
		}
		// ScyllaDB backs secondary indexes with views named <index>_index
		indexViews := map[string]bool{}
		if idx, err := c.query(ctx, "SELECT index_name FROM system_schema.indexes WHERE keyspace_name = ?", ks); err == nil {
			for _, r := range idx {
				indexViews[r.str("index_name")+"_index"] = true
			}
		}
		for _, r := range recs {
			if indexViews[r.str("view_name")] {
				continue
			}
			out = append(out, db.Object{Ref: db.ObjectRef{Schema: ks, Name: r.str("view_name"), Type: db.ObjMatView}, Extra: "on " + r.str("base_table_name")})
		}
	}
	if wants(types, db.ObjFunction) {
		recs, err := c.query(ctx, "SELECT function_name, argument_types, return_type FROM system_schema.functions WHERE keyspace_name = ?", ks)
		if err == nil { // UDFs may be disabled
			for _, r := range recs {
				args := ""
				if a, ok := r["argument_types"].([]string); ok {
					args = strings.Join(a, ", ")
				}
				out = append(out, db.Object{Ref: db.ObjectRef{Schema: ks, Name: r.str("function_name"), Type: db.ObjFunction, Signature: "(" + args + ")"},
					Extra: r.str("return_type")})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Ref.Type != out[j].Ref.Type {
			return false
		}
		return out[i].Ref.Name < out[j].Ref.Name
	})
	return out, nil
}

func (c *catalog) Table(ctx context.Context, ref db.ObjectRef) (*db.TableInfo, error) {
	recs, err := c.query(ctx, "SELECT column_name, type, kind, position, clustering_order FROM system_schema.columns WHERE keyspace_name = ? AND table_name = ?", ref.Schema, ref.Name)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, fmt.Errorf("%s not found", ref)
	}
	order := map[string]int{"partition_key": 0, "clustering": 1, "static": 2, "regular": 3}
	sort.Slice(recs, func(i, j int) bool {
		a, b := recs[i], recs[j]
		if order[a.str("kind")] != order[b.str("kind")] {
			return order[a.str("kind")] < order[b.str("kind")]
		}
		if a.num("position") != b.num("position") {
			return a.num("position") < b.num("position")
		}
		return a.str("column_name") < b.str("column_name")
	})
	t := &db.TableInfo{Ref: ref, RowEstimate: -1}
	var parts, clust []string
	for _, r := range recs {
		name, kind := r.str("column_name"), r.str("kind")
		tc := db.TableColumn{Name: name, Type: r.str("type"), Kind: kindOfName(r.str("type")), Nullable: true}
		switch kind {
		case "partition_key":
			parts = append(parts, name)
			tc.Nullable = false
		case "clustering":
			clust = append(clust, name)
			tc.Nullable = false
			if o := r.str("clustering_order"); o != "" && o != "none" {
				tc.Comment = "clustering " + strings.ToUpper(o)
			}
		case "static":
			tc.Comment = "static"
		}
		t.Columns = append(t.Columns, tc)
	}
	t.PrimaryKey = append(parts, clust...)
	for i, pk := range t.PrimaryKey {
		for j := range t.Columns {
			if t.Columns[j].Name == pk {
				t.Columns[j].PrimaryKeyNo = i + 1
			}
		}
	}
	t.Extra = append(t.Extra, [2]string{"Partition key", strings.Join(parts, ", ")})
	if len(clust) > 0 {
		t.Extra = append(t.Extra, [2]string{"Clustering columns", strings.Join(clust, ", ")})
	}
	if meta, err := c.query(ctx, "SELECT comment, default_time_to_live FROM system_schema.tables WHERE keyspace_name = ? AND table_name = ?", ref.Schema, ref.Name); err == nil && len(meta) == 1 {
		t.Comment = meta[0].str("comment")
		if ttl := meta[0].num("default_time_to_live"); ttl > 0 {
			t.Extra = append(t.Extra, [2]string{"Default TTL", fmt.Sprintf("%ds", ttl)})
		}
	}
	if idx, err := c.query(ctx, "SELECT index_name, kind, options FROM system_schema.indexes WHERE keyspace_name = ? AND table_name = ?", ref.Schema, ref.Name); err == nil {
		for _, r := range idx {
			target := ""
			if o, ok := r["options"].(map[string]string); ok {
				target = o["target"]
			}
			t.Indexes = append(t.Indexes, db.Index{Name: r.str("index_name"), Columns: []string{target}, Method: strings.ToLower(r.str("kind"))})
		}
	}
	return t, nil
}

func (c *catalog) DDL(ctx context.Context, ref db.ObjectRef) (string, error) {
	kw := "TABLE"
	switch ref.Type {
	case db.ObjMatView:
		kw = "MATERIALIZED VIEW"
	case db.ObjFunction:
		kw = "FUNCTION"
	}
	name := c.d.Qualified(ref)
	if ref.Type == db.ObjFunction {
		name = c.d.Quote(ref.Schema) + "." + c.d.Quote(ref.Name)
		if ref.Signature != "" {
			name += ref.Signature
		}
	}
	// server-side DESCRIBE (Cassandra 4+, ScyllaDB 5+)
	recs, err := c.query(ctx, "DESCRIBE "+kw+" "+name)
	if err == nil && len(recs) > 0 {
		var b strings.Builder
		for _, r := range recs {
			b.WriteString(strings.TrimSpace(r.str("create_statement")) + "\n\n")
		}
		return strings.TrimSpace(b.String()) + "\n", nil
	}
	if ref.Type == db.ObjFunction {
		return "", fmt.Errorf("DESCRIBE not supported by this server: %v", err)
	}
	t, terr := c.Table(ctx, ref)
	if terr != nil {
		return "", terr
	}
	var parts, clust, defs []string
	var order []string
	for _, col := range t.Columns {
		def := "    " + c.d.Quote(col.Name) + " " + col.Type
		if col.Comment == "static" {
			def += " STATIC"
		}
		defs = append(defs, def)
		switch {
		case col.PrimaryKeyNo > 0 && strings.HasPrefix(col.Comment, "clustering"):
			clust = append(clust, c.d.Quote(col.Name))
			order = append(order, c.d.Quote(col.Name)+" "+strings.TrimPrefix(col.Comment, "clustering "))
		case col.PrimaryKeyNo > 0:
			parts = append(parts, c.d.Quote(col.Name))
		}
	}
	pk := "(" + strings.Join(parts, ", ") + ")"
	if len(clust) > 0 {
		pk += ", " + strings.Join(clust, ", ")
	}
	s := fmt.Sprintf("CREATE TABLE %s (\n%s,\n    PRIMARY KEY (%s)\n)", c.d.Qualified(ref), strings.Join(defs, ",\n"), pk)
	if len(order) > 0 {
		s += " WITH CLUSTERING ORDER BY (" + strings.Join(order, ", ") + ")"
	}
	return s + ";\n", nil
}

func (c *catalog) Completion(ctx context.Context, schema db.ObjectRef) (map[string][]string, error) {
	recs, err := c.query(ctx, "SELECT table_name, column_name FROM system_schema.columns WHERE keyspace_name = ?", schema.Schema)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, r := range recs {
		out[r.str("table_name")] = append(out[r.str("table_name")], r.str("column_name"))
	}
	return out, nil
}
