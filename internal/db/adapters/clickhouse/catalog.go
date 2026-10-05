package clickhouse

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/db/sqlbase"
)

type catalog struct {
	db *sql.DB
	d  *dialect
}

func (c *catalog) Catalogs(ctx context.Context) ([]string, error) { return nil, db.ErrNotSupported }

func (c *catalog) Schemas(ctx context.Context, _ string) ([]db.Schema, error) {
	recs, err := sqlbase.Recs(ctx, c.db, `SELECT name, name IN ('system','INFORMATION_SCHEMA','information_schema') AS sys,
       name = currentDatabase() AS cur FROM system.databases ORDER BY sys, name`)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]db.Schema, len(recs))
	for i, r := range recs {
		out[i] = db.Schema{Name: r["name"], System: r.Bool("sys"), Current: r.Bool("cur")}
	}
	return out, nil
}

func (c *catalog) Objects(ctx context.Context, schema db.ObjectRef, types ...db.ObjectType) ([]db.Object, error) {
	recs, err := sqlbase.Recs(ctx, c.db, `SELECT name, engine, comment, total_rows AS est FROM system.tables
WHERE database = ? AND NOT is_temporary AND name NOT LIKE '.inner%' ORDER BY name`, schema.Schema)
	if err != nil {
		return nil, mapError(err)
	}
	var out []db.Object
	for _, r := range recs {
		t := db.ObjTable
		switch r["engine"] {
		case "View":
			t = db.ObjView
		case "MaterializedView":
			t = db.ObjMatView
		}
		if !sqlbase.Wants(types, t) {
			continue
		}
		o := db.Object{Ref: db.ObjectRef{Schema: schema.Schema, Name: r["name"], Type: t}, Comment: r["comment"], Extra: r["engine"]}
		if t == db.ObjTable && r["est"] != "" {
			o.Extra += fmt.Sprintf(" · %s rows", r["est"])
		}
		out = append(out, o)
	}
	return out, nil
}

func (c *catalog) Table(ctx context.Context, ref db.ObjectRef) (*db.TableInfo, error) {
	res, err := sqlbase.RecsParallel(ctx, c.db,
		sqlbase.Q{SQL: `SELECT name, type, default_kind, default_expression, comment, is_in_primary_key, is_in_sorting_key
FROM system.columns WHERE database = ? AND table = ? ORDER BY position`, Args: []any{ref.Schema, ref.Name}},
		sqlbase.Q{SQL: `SELECT engine, primary_key, sorting_key, partition_key, comment, total_rows, total_bytes
FROM system.tables WHERE database = ? AND name = ?`, Args: []any{ref.Schema, ref.Name}},
		sqlbase.Q{SQL: `SELECT name, type, expr, granularity FROM system.data_skipping_indices WHERE database = ? AND table = ? ORDER BY name`,
			Args: []any{ref.Schema, ref.Name}},
	)
	if err != nil {
		return nil, mapError(err)
	}
	cols, meta, idx := res[0], res[1], res[2]
	if len(cols) == 0 {
		return nil, fmt.Errorf("%s not found", ref)
	}
	t := &db.TableInfo{Ref: ref, RowEstimate: -1}
	for _, r := range cols {
		typ := r["type"]
		tc := db.TableColumn{Name: r["name"], Type: typ, Kind: KindOf(typ), Comment: r["comment"],
			Nullable: strings.HasPrefix(unwrapLC(typ), "Nullable(")}
		switch r["default_kind"] {
		case "DEFAULT":
			tc.Default, tc.HasDefault = r["default_expression"], true
		case "MATERIALIZED", "ALIAS":
			tc.Generated = r["default_expression"]
		}
		t.Columns = append(t.Columns, tc)
	}
	if len(meta) == 1 {
		m := meta[0]
		t.Comment = m["comment"]
		if m["total_rows"] != "" {
			t.RowEstimate = m.Int("total_rows")
		}
		t.Extra = append(t.Extra, [2]string{"Engine", m["engine"]})
		for _, k := range [][2]string{{"Sorting key", "sorting_key"}, {"Partition key", "partition_key"}} {
			if v := m[k[1]]; v != "" {
				t.Extra = append(t.Extra, [2]string{k[0], v})
			}
		}
		if b := m.Int("total_bytes"); b > 0 {
			t.Extra = append(t.Extra, [2]string{"Size", db.HumanBytes(b)})
		}
		for _, k := range sqlbase.SplitList(m["primary_key"]) {
			t.PrimaryKey = append(t.PrimaryKey, strings.Trim(k, "`"))
		}
	}
	for i, pk := range t.PrimaryKey {
		for j := range t.Columns {
			if t.Columns[j].Name == pk {
				t.Columns[j].PrimaryKeyNo = i + 1
			}
		}
	}
	for _, r := range idx {
		t.Indexes = append(t.Indexes, db.Index{Name: r["name"], Columns: []string{r["expr"]},
			Method: fmt.Sprintf("%s GRANULARITY %s", r["type"], r["granularity"])})
	}
	return t, nil
}

// unwrapLC strips LowCardinality(…) only.
func unwrapLC(t string) string {
	if strings.HasPrefix(t, "LowCardinality(") && strings.HasSuffix(t, ")") {
		return t[len("LowCardinality(") : len(t)-1]
	}
	return t
}

func (c *catalog) DDL(ctx context.Context, ref db.ObjectRef) (string, error) {
	kw := "TABLE"
	if ref.Type == db.ObjView || ref.Type == db.ObjMatView {
		kw = "VIEW"
	}
	s, err := sqlbase.One(ctx, c.db, "SHOW CREATE "+kw+" "+c.d.Qualified(ref))
	if err != nil {
		return "", mapError(err)
	}
	if s == "" {
		return "", fmt.Errorf("%s not found", ref)
	}
	return strings.TrimSpace(s) + ";\n", nil
}

func (c *catalog) Completion(ctx context.Context, schema db.ObjectRef) (map[string][]string, error) {
	recs, err := sqlbase.Recs(ctx, c.db, `SELECT table AS t, name AS c FROM system.columns WHERE database = ? ORDER BY table, position`, schema.Schema)
	if err != nil {
		return nil, mapError(err)
	}
	return sqlbase.GroupCompletion(recs, "t", "c"), nil
}
