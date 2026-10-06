package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/db/sqlbase"
)

// Catalog reads pg_catalog. CockroachDB reuses most of it.
type Catalog struct {
	DB *sql.DB
	D  *Dialect
	// Cockroach disables PostgreSQL-only features.
	Cockroach bool
}

func (c *Catalog) Catalogs(ctx context.Context) ([]string, error) { return nil, db.ErrNotSupported }

func (c *Catalog) Schemas(ctx context.Context, _ string) ([]db.Schema, error) {
	recs, err := sqlbase.Recs(ctx, c.DB, `
SELECT n.nspname AS name,
       n.nspname IN ('pg_catalog','information_schema','crdb_internal','pg_extension') AS system,
       n.nspname = current_schema() AS cur
FROM pg_catalog.pg_namespace n
WHERE n.nspname NOT LIKE 'pg\_toast%' AND n.nspname NOT LIKE 'pg\_temp\_%'
ORDER BY 2, 1`)
	if err != nil {
		return nil, MapError(err)
	}
	out := make([]db.Schema, len(recs))
	for i, r := range recs {
		out[i] = db.Schema{Name: r["name"], System: r.Bool("system"), Current: r.Bool("cur")}
	}
	return out, nil
}

func (c *Catalog) Objects(ctx context.Context, schema db.ObjectRef, types ...db.ObjectType) ([]db.Object, error) {
	var out []db.Object
	kinds := map[db.ObjectType]string{db.ObjTable: "'r','p','f'", db.ObjView: "'v'", db.ObjMatView: "'m'", db.ObjSequence: "'S'"}
	var rel []string
	for _, t := range []db.ObjectType{db.ObjTable, db.ObjView, db.ObjMatView, db.ObjSequence} {
		if sqlbase.Wants(types, t) {
			rel = append(rel, kinds[t])
		}
	}
	if len(rel) > 0 {
		recs, err := sqlbase.Recs(ctx, c.DB, `
SELECT c.relname AS name, c.relkind AS kind, COALESCE(obj_description(c.oid, 'pg_class'), '') AS comment,
       c.reltuples::bigint AS est
FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind IN (`+strings.Join(rel, ",")+`)
  AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_inherits i WHERE i.inhrelid = c.oid AND c.relispartition)
  AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_depend d WHERE d.classid = 'pg_catalog.pg_class'::regclass
                  AND d.objid = c.oid AND d.deptype IN ('i','e'))
ORDER BY c.relname`, schema.Schema)
		if err != nil {
			return nil, MapError(err)
		}
		for _, r := range recs {
			t := db.ObjTable
			switch r["kind"] {
			case "v":
				t = db.ObjView
			case "m":
				t = db.ObjMatView
			case "S":
				t = db.ObjSequence
			}
			o := db.Object{Ref: db.ObjectRef{Schema: schema.Schema, Name: r["name"], Type: t}, Comment: r["comment"]}
			if t == db.ObjTable && r.Int("est") > 0 {
				o.Extra = fmt.Sprintf("~%d rows", r.Int("est"))
			}
			if r["kind"] == "p" {
				o.Extra = strings.TrimSpace("partitioned " + o.Extra)
			}
			out = append(out, o)
		}
	}
	if !c.Cockroach && (sqlbase.Wants(types, db.ObjFunction) || sqlbase.Wants(types, db.ObjProcedure)) {
		recs, err := sqlbase.Recs(ctx, c.DB, `
SELECT p.proname AS name, p.prokind AS kind, pg_catalog.pg_get_function_identity_arguments(p.oid) AS args,
       COALESCE(pg_catalog.pg_get_function_result(p.oid), '') AS result
FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = $1 AND p.prokind IN ('f','p')
  AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_depend d WHERE d.objid = p.oid AND d.deptype = 'e')
ORDER BY p.proname, 3`, schema.Schema)
		if err != nil {
			return nil, MapError(err)
		}
		for _, r := range recs {
			t := db.ObjFunction
			if r["kind"] == "p" {
				t = db.ObjProcedure
			}
			if !sqlbase.Wants(types, t) {
				continue
			}
			out = append(out, db.Object{Ref: db.ObjectRef{Schema: schema.Schema, Name: r["name"], Type: t,
				Signature: "(" + r["args"] + ")"}, Extra: r["result"]})
		}
	}
	return out, nil
}

// oid resolves a relation.
func (c *Catalog) oid(ctx context.Context, ref db.ObjectRef) (string, error) {
	oid, err := sqlbase.One(ctx, c.DB, `
SELECT c.oid FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2`, ref.Schema, ref.Name)
	if err != nil {
		return "", MapError(err)
	}
	if oid == "" {
		return "", fmt.Errorf("%s not found", ref)
	}
	return oid, nil
}

// splitUnit splits a list aggregated with the unit separator (column names
// can contain commas and spaces).
func splitUnit(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x1f")
}

var fkRule = map[string]string{"a": "NO ACTION", "r": "RESTRICT", "c": "CASCADE", "n": "SET NULL", "d": "SET DEFAULT"}

func (c *Catalog) Table(ctx context.Context, ref db.ObjectRef) (*db.TableInfo, error) {
	oid, err := c.oid(ctx, ref)
	if err != nil {
		return nil, err
	}
	t := &db.TableInfo{Ref: ref, RowEstimate: -1}
	generated := "CASE WHEN a.attgenerated = 's' THEN pg_catalog.pg_get_expr(d.adbin, d.adrelid) ELSE '' END"
	defExpr := "CASE WHEN a.attgenerated = '' THEN pg_catalog.pg_get_expr(d.adbin, d.adrelid) END"
	hidden := "false"
	if c.Cockroach {
		// CockroachDB: computed columns and hidden columns (the implicit
		// rowid of tables without a primary key) are in information_schema
		generated = "COALESCE(ic.generation_expression, '')"
		defExpr = "CASE WHEN COALESCE(ic.generation_expression, '') = '' THEN pg_catalog.pg_get_expr(d.adbin, d.adrelid) END"
		hidden = "COALESCE(ic.is_hidden = 'YES', false)"
	}
	icJoin := ""
	if c.Cockroach {
		icJoin = `
LEFT JOIN information_schema.columns ic ON ic.table_schema = $2 AND ic.table_name = $3 AND ic.column_name = a.attname`
	}
	args := []any{oid}
	if c.Cockroach {
		args = append(args, ref.Schema, ref.Name)
	}
	cols, err := sqlbase.Recs(ctx, c.DB, `
SELECT a.attname AS name, pg_catalog.format_type(a.atttypid, a.atttypmod) AS type, NOT a.attnotnull AS nullable,
       COALESCE(`+defExpr+`, '') AS def, d.adbin IS NOT NULL AS hasdef,
       COALESCE(a.attidentity, '') AS ident, `+generated+` AS gen, `+hidden+` AS hidden,
       COALESCE(pg_catalog.col_description(a.attrelid, a.attnum), '') AS comment
FROM pg_catalog.pg_attribute a
LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum`+icJoin+`
WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum`, args...)
	if err != nil {
		return nil, MapError(err)
	}
	hiddenCols := map[string]bool{}
	for _, r := range cols {
		if r.Bool("hidden") {
			hiddenCols[r["name"]] = true
			continue
		}
		def := r["def"]
		tc := db.TableColumn{Name: r["name"], Type: r["type"], Kind: KindOfFormatted(r["type"]), Nullable: r.Bool("nullable"),
			Default: def, HasDefault: def != "", Comment: r["comment"], Generated: r["gen"]}
		if r["ident"] != "" || strings.HasPrefix(def, "nextval(") || strings.Contains(def, "unique_rowid()") {
			tc.AutoIncr = true
		}
		t.Columns = append(t.Columns, tc)
	}
	meta, err := sqlbase.Recs(ctx, c.DB, `
SELECT COALESCE(obj_description(c.oid, 'pg_class'), '') AS comment, c.reltuples::bigint AS est
FROM pg_catalog.pg_class c WHERE c.oid = $1`, oid)
	if err == nil && len(meta) == 1 {
		t.Comment = meta[0]["comment"]
		if n := meta[0].Int("est"); n >= 0 {
			t.RowEstimate = n
		}
	}

	// constraints
	cons, err := sqlbase.Recs(ctx, c.DB, `
SELECT con.conname AS name, con.contype AS type, pg_catalog.pg_get_constraintdef(con.oid, true) AS def,
       COALESCE((SELECT string_agg(a.attname, E'\x1f' ORDER BY k.ord) FROM unnest(con.conkey) WITH ORDINALITY k(n, ord)
                 JOIN pg_catalog.pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.n), '') AS cols,
       COALESCE(rn.nspname, '') AS rschema, COALESCE(rc.relname, '') AS rtable,
       COALESCE((SELECT string_agg(a.attname, E'\x1f' ORDER BY k.ord) FROM unnest(con.confkey) WITH ORDINALITY k(n, ord)
                 JOIN pg_catalog.pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.n), '') AS rcols,
       con.confupdtype AS upd, con.confdeltype AS del
FROM pg_catalog.pg_constraint con
LEFT JOIN pg_catalog.pg_class rc ON rc.oid = con.confrelid
LEFT JOIN pg_catalog.pg_namespace rn ON rn.oid = rc.relnamespace
WHERE con.conrelid = $1
ORDER BY con.contype, con.conname`, oid)
	if err != nil {
		return nil, MapError(err)
	}
	for _, r := range cons {
		cols := splitUnit(r["cols"])
		switch r["type"] {
		case "p":
			if !anyIn(cols, hiddenCols) {
				t.PrimaryKey = cols
			}
		case "f":
			t.ForeignKeys = append(t.ForeignKeys, db.ForeignKey{Name: r["name"], Columns: cols,
				RefTable:   db.ObjectRef{Schema: r["rschema"], Name: r["rtable"], Type: db.ObjTable},
				RefColumns: splitUnit(r["rcols"]), OnUpdate: fkRule[r["upd"]], OnDelete: fkRule[r["del"]]})
		case "u":
			t.Constraints = append(t.Constraints, db.Constraint{Name: r["name"], Type: "UNIQUE", Columns: cols, Definition: r["def"]})
		case "c":
			t.Constraints = append(t.Constraints, db.Constraint{Name: r["name"], Type: "CHECK", Columns: cols, Definition: r["def"]})
		case "x":
			t.Constraints = append(t.Constraints, db.Constraint{Name: r["name"], Type: "EXCLUDE", Columns: cols, Definition: r["def"]})
		}
	}
	for i, pk := range t.PrimaryKey {
		for j := range t.Columns {
			if t.Columns[j].Name == pk {
				t.Columns[j].PrimaryKeyNo = i + 1
			}
		}
	}

	// indexes
	idx, err := sqlbase.Recs(ctx, c.DB, `
SELECT ic.relname AS name, i.indisunique AS uniq, i.indisprimary AS prim, am.amname AS method,
       COALESCE(pg_catalog.pg_get_expr(i.indpred, i.indrelid, true), '') AS pred,
       (SELECT string_agg(COALESCE(a.attname, pg_catalog.pg_get_indexdef(i.indexrelid, k, true)), E'\x1f' ORDER BY k)
          FROM generate_series(1, i.indnkeyatts) k
          LEFT JOIN pg_catalog.pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = i.indkey[k - 1] AND i.indkey[k - 1] <> 0) AS cols,
       pg_catalog.pg_get_indexdef(i.indexrelid) AS def,
       EXISTS (SELECT 1 FROM pg_catalog.pg_constraint con WHERE con.conindid = i.indexrelid AND con.conrelid = i.indrelid) AS bycons
FROM pg_catalog.pg_index i
JOIN pg_catalog.pg_class ic ON ic.oid = i.indexrelid
JOIN pg_catalog.pg_am am ON am.oid = ic.relam
WHERE i.indrelid = $1
ORDER BY i.indisprimary DESC, ic.relname`, oid)
	if err != nil {
		return nil, MapError(err)
	}
	for _, r := range idx {
		ix := db.Index{Name: r["name"], Unique: r.Bool("uniq"), Primary: r.Bool("prim"), Method: r["method"], Where: r["pred"],
			Columns: strings.Split(r["cols"], "\x1f")}
		if anyIn(ix.Columns, hiddenCols) {
			// the implicit primary index on CockroachDB's hidden rowid
			continue
		}
		if ix.Method == "btree" || ix.Method == "prefix" {
			ix.Method = ""
		}
		t.Indexes = append(t.Indexes, ix)
	}

	if !c.Cockroach {
		trg, err := sqlbase.Recs(ctx, c.DB, `
SELECT t.tgname AS name, pg_catalog.pg_get_triggerdef(t.oid, true) AS def, t.tgtype::int AS tgtype
FROM pg_catalog.pg_trigger t WHERE t.tgrelid = $1 AND NOT t.tgisinternal ORDER BY t.tgname`, oid)
		if err != nil {
			return nil, MapError(err)
		}
		for _, r := range trg {
			tr := db.Trigger{Name: r["name"], Definition: r["def"]}
			tr.Timing, tr.Events = triggerType(r.Int("tgtype"))
			t.Triggers = append(t.Triggers, tr)
		}
	}
	return t, nil
}

// anyIn reports whether one of names is in set.
func anyIn(names []string, set map[string]bool) bool {
	for _, n := range names {
		if set[n] {
			return true
		}
	}
	return false
}

// triggerType decodes pg_trigger.tgtype: timing and events.
func triggerType(tgtype int64) (timing, events string) {
	switch {
	case tgtype&64 != 0:
		timing = "INSTEAD OF"
	case tgtype&2 != 0:
		timing = "BEFORE"
	default:
		timing = "AFTER"
	}
	var ev []string
	for _, e := range []struct {
		bit  int64
		name string
	}{{4, "INSERT"}, {16, "UPDATE"}, {8, "DELETE"}, {32, "TRUNCATE"}} {
		if tgtype&e.bit != 0 {
			ev = append(ev, e.name)
		}
	}
	return timing, strings.Join(ev, ", ")
}

func (c *Catalog) DDL(ctx context.Context, ref db.ObjectRef) (string, error) {
	switch ref.Type {
	case db.ObjView, db.ObjMatView:
		return c.viewDDL(ctx, ref)
	case db.ObjFunction, db.ObjProcedure:
		sig := strings.TrimSuffix(strings.TrimPrefix(ref.Signature, "("), ")")
		def, err := sqlbase.One(ctx, c.DB, `
SELECT pg_catalog.pg_get_functiondef(p.oid) FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = $1 AND p.proname = $2 AND pg_catalog.pg_get_function_identity_arguments(p.oid) = $3`, ref.Schema, ref.Name, sig)
		if err != nil {
			return "", MapError(err)
		}
		if def == "" {
			return "", fmt.Errorf("%s not found", ref)
		}
		return strings.TrimRight(def, "\n") + ";\n", nil
	case db.ObjSequence:
		r, err := c.sequence(ctx, ref)
		if err != nil {
			return "", err
		}
		cyc := "NO CYCLE"
		if r.Bool("cycle") {
			cyc = "CYCLE"
		}
		return fmt.Sprintf("CREATE SEQUENCE %s\n    AS %s\n    INCREMENT BY %s\n    MINVALUE %s\n    MAXVALUE %s\n    START WITH %s\n    CACHE %s\n    %s;\n",
			c.D.Qualified(ref), r["data_type"], r["increment_by"], r["min_value"], r["max_value"], r["start_value"], r["cache_size"], cyc), nil
	}
	return c.tableDDL(ctx, ref)
}

func (c *Catalog) Completion(ctx context.Context, schema db.ObjectRef) (map[string][]string, error) {
	hiddenFilter := ""
	if c.Cockroach {
		hiddenFilter = `
  AND NOT EXISTS (SELECT 1 FROM information_schema.columns ic WHERE ic.table_schema = n.nspname
                  AND ic.table_name = c.relname AND ic.column_name = a.attname AND ic.is_hidden = 'YES')`
	}
	recs, err := sqlbase.Recs(ctx, c.DB, `
SELECT c.relname AS t, a.attname AS c
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
WHERE n.nspname = $1 AND c.relkind IN ('r','p','v','m','f')`+hiddenFilter+`
ORDER BY c.relname, a.attnum`, schema.Schema)
	if err != nil {
		return nil, MapError(err)
	}
	return sqlbase.GroupCompletion(recs, "t", "c"), nil
}
