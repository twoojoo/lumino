package clickhouse

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/db/sqlbase"
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
		t.PrimaryKey = keyColumns(m["primary_key"], t.Columns)
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

// keyColumns returns the columns identifying a row for a primary key
// expression such as "a, cityHash64(b, a), `we,ird`": plain columns in
// order, and for expressions the columns they reference (a superset that
// still identifies the same rows). An expression referencing no column
// makes the key unusable for row identification: nil.
func keyColumns(key string, cols []db.TableColumn) []string {
	isCol := map[string]bool{}
	for _, c := range cols {
		isCol[c.Name] = true
	}
	toks := keyTokens(key)
	punct := func(i int, p string) bool { return i < len(toks) && toks[i].kind == 'p' && toks[i].s == p }
	ident := func(i int) bool { return i < len(toks) && toks[i].kind == 'i' }
	var out []string
	seen := map[string]bool{}
	// elements separated by top-level commas
	for start := 0; start < len(toks); {
		end, depth := start, 0
		for ; end < len(toks); end++ {
			if punct(end, "(") || punct(end, "[") {
				depth++
			} else if punct(end, ")") || punct(end, "]") {
				depth--
			} else if depth == 0 && punct(end, ",") {
				break
			}
		}
		refs := 0
		for i := start; i < end; {
			if !ident(i) {
				i++
				continue
			}
			parts := []string{toks[i].s}
			for punct(i+1, ".") && ident(i+2) {
				parts = append(parts, toks[i+2].s)
				i += 2
			}
			i++
			if punct(i, "(") { // function call
				continue
			}
			if name := strings.Join(parts, "."); isCol[name] {
				refs++
				if !seen[name] {
					seen[name] = true
					out = append(out, name)
				}
			}
		}
		if refs == 0 && end > start {
			return nil
		}
		start = end + 1
	}
	return out
}

type keyTok struct {
	kind byte // 'i' identifier (unquoted), 'p' punctuation, 'o' other
	s    string
}

// keyTokens tokenizes a ClickHouse key expression as system.tables shows it
// (`a\`b` quoting, 'strings', numbers, operators).
func keyTokens(s string) []keyTok {
	var out []keyTok
	isWord := func(c byte) bool {
		return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80
	}
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '`' || c == '"' || c == '\'':
			var b strings.Builder
			j := i + 1
			for j < len(s) {
				if s[j] == '\\' && j+1 < len(s) {
					b.WriteByte(s[j+1])
					j += 2
					continue
				}
				if s[j] == c {
					if j+1 < len(s) && s[j+1] == c {
						b.WriteByte(c)
						j += 2
						continue
					}
					break
				}
				b.WriteByte(s[j])
				j++
			}
			kind := byte('i')
			if c == '\'' {
				kind = 'o'
			}
			out = append(out, keyTok{kind, b.String()})
			i = j + 1
		case strings.IndexByte("(),.[]", c) >= 0:
			out = append(out, keyTok{'p', string(c)})
			i++
		case isWord(c):
			j := i
			for j < len(s) && isWord(s[j]) {
				j++
			}
			kind := byte('i')
			if c >= '0' && c <= '9' {
				kind = 'o'
			}
			out = append(out, keyTok{kind, s[i:j]})
			i = j
		default:
			out = append(out, keyTok{'o', string(c)})
			i++
		}
	}
	return out
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
