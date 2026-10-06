package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/db/sqlbase"
	"github.com/giovanni/lumino/internal/db/sqlx"
)

// Catalog reads information_schema.
type Catalog struct {
	DB     *sql.DB
	D      *db.BaseDialect
	Flavor Flavor
}

var systemSchemas = map[string]bool{"information_schema": true, "mysql": true, "performance_schema": true, "sys": true,
	"metrics_schema": true}

func (c *Catalog) Catalogs(ctx context.Context) ([]string, error) { return nil, db.ErrNotSupported }

func (c *Catalog) Schemas(ctx context.Context, _ string) ([]db.Schema, error) {
	recs, err := sqlbase.Recs(ctx, c.DB, "SELECT schema_name AS name, schema_name = DATABASE() AS cur FROM information_schema.schemata ORDER BY schema_name")
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]db.Schema, 0, len(recs))
	for _, r := range recs {
		n := r["name"]
		out = append(out, db.Schema{Name: n, System: systemSchemas[strings.ToLower(n)], Current: r.Bool("cur")})
	}
	sort.SliceStable(out, func(i, j int) bool { return !out[i].System && out[j].System })
	return out, nil
}

func (c *Catalog) Objects(ctx context.Context, schema db.ObjectRef, types ...db.ObjectType) ([]db.Object, error) {
	var out []db.Object
	if sqlbase.Wants(types, db.ObjTable) || sqlbase.Wants(types, db.ObjView) || sqlbase.Wants(types, db.ObjSequence) {
		recs, err := sqlbase.Recs(ctx, c.DB, `
SELECT table_name AS name, table_type AS type, COALESCE(table_comment, '') AS comment, COALESCE(engine, '') AS engine,
       COALESCE(table_rows, -1) AS est
FROM information_schema.tables WHERE table_schema = ? ORDER BY table_name`, schema.Schema)
		if err != nil {
			return nil, mapError(err)
		}
		for _, r := range recs {
			t := db.ObjTable
			switch r["type"] {
			case "VIEW", "SYSTEM VIEW":
				t = db.ObjView
			case "SEQUENCE":
				t = db.ObjSequence
			}
			if !sqlbase.Wants(types, t) {
				continue
			}
			o := db.Object{Ref: db.ObjectRef{Schema: schema.Schema, Name: r["name"], Type: t}}
			if t == db.ObjTable {
				o.Comment = r["comment"]
				o.Extra = r["engine"]
			}
			if t == db.ObjView && r["comment"] != "VIEW" {
				o.Comment = r["comment"]
			}
			out = append(out, o)
		}
	}
	if c.Flavor != TiDB && (sqlbase.Wants(types, db.ObjFunction) || sqlbase.Wants(types, db.ObjProcedure)) {
		recs, err := sqlbase.Recs(ctx, c.DB, `
SELECT routine_name AS name, routine_type AS type, COALESCE(routine_comment, '') AS comment, COALESCE(dtd_identifier, '') AS ret
FROM information_schema.routines WHERE routine_schema = ? ORDER BY routine_name`, schema.Schema)
		if err != nil {
			return nil, mapError(err)
		}
		for _, r := range recs {
			t := db.ObjFunction
			if r["type"] == "PROCEDURE" {
				t = db.ObjProcedure
			}
			if sqlbase.Wants(types, t) {
				out = append(out, db.Object{Ref: db.ObjectRef{Schema: schema.Schema, Name: r["name"], Type: t}, Comment: r["comment"], Extra: r["ret"]})
			}
		}
	}
	return out, nil
}

func (c *Catalog) Table(ctx context.Context, ref db.ObjectRef) (*db.TableInfo, error) {
	t := &db.TableInfo{Ref: ref, RowEstimate: -1}
	cols, err := sqlbase.Recs(ctx, c.DB, `
SELECT column_name AS name, column_type AS type, data_type AS dtype, is_nullable AS nullable, column_default AS def,
       column_default IS NOT NULL AS hasdef, extra, COALESCE(generation_expression, '') AS gen, column_comment AS comment
FROM information_schema.columns WHERE table_schema = ? AND table_name = ? ORDER BY ordinal_position`, ref.Schema, ref.Name)
	if err != nil {
		return nil, mapError(err)
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("%s not found", ref)
	}
	for _, r := range cols {
		extra := strings.ToLower(r["extra"])
		tc := db.TableColumn{Name: r["name"], Type: r["type"], Kind: KindOf(r["dtype"]), Nullable: r.Bool("nullable"),
			Default: r["def"], HasDefault: r.Bool("hasdef"), Comment: r["comment"],
			AutoIncr: strings.Contains(extra, "auto_increment")}
		if strings.Contains(extra, "generated") && r["gen"] != "" {
			tc.Generated = r["gen"]
			tc.HasDefault, tc.Default = false, ""
		}
		if tc.Kind == db.KindInt && strings.HasPrefix(strings.ToLower(tc.Type), "tinyint(1)") {
			tc.Kind = db.KindInt
		}
		if c.Flavor == MariaDB && tc.Default == "NULL" {
			tc.Default, tc.HasDefault = "", false
		}
		if c.Flavor != MariaDB && tc.HasDefault && tc.Generated == "" {
			// MariaDB reports SQL expressions; MySQL/TiDB report literals raw
			tc.Default = c.defaultExpr(tc.Default, strings.ToLower(r["dtype"]), extra)
		}
		t.Columns = append(t.Columns, tc)
	}
	meta, err := sqlbase.Recs(ctx, c.DB, `SELECT COALESCE(table_comment, '') AS comment, COALESCE(table_rows, -1) AS est, COALESCE(engine, '') AS engine,
       COALESCE(table_collation, '') AS coll
FROM information_schema.tables WHERE table_schema = ? AND table_name = ?`, ref.Schema, ref.Name)
	if err == nil && len(meta) == 1 {
		if meta[0]["comment"] != "VIEW" {
			t.Comment = meta[0]["comment"]
		}
		t.RowEstimate = meta[0].Int("est")
		if e := meta[0]["engine"]; e != "" {
			t.Extra = append(t.Extra, [2]string{"Engine", e})
		}
		if e := meta[0]["coll"]; e != "" {
			t.Extra = append(t.Extra, [2]string{"Collation", e})
		}
	}

	// indexes (the PRIMARY one gives the key)
	idx, err := sqlbase.Recs(ctx, c.DB, `
SELECT index_name AS name, non_unique AS nonuniq, column_name AS col, index_type AS method, seq_in_index AS seq
FROM information_schema.statistics WHERE table_schema = ? AND table_name = ? ORDER BY index_name = 'PRIMARY' DESC, index_name, seq_in_index`, ref.Schema, ref.Name)
	if err != nil {
		return nil, mapError(err)
	}
	var cur *db.Index
	for _, r := range idx {
		if cur == nil || cur.Name != r["name"] {
			t.Indexes = append(t.Indexes, db.Index{Name: r["name"], Unique: !r.Bool("nonuniq"), Primary: r["name"] == "PRIMARY", Method: r["method"]})
			cur = &t.Indexes[len(t.Indexes)-1]
			if cur.Method == "BTREE" {
				cur.Method = ""
			}
		}
		col := r["col"]
		if col == "" {
			col = "(expression)"
		}
		cur.Columns = append(cur.Columns, col)
		if cur.Primary {
			t.PrimaryKey = append(t.PrimaryKey, col)
		}
	}
	for i, pk := range t.PrimaryKey {
		for j := range t.Columns {
			if t.Columns[j].Name == pk {
				t.Columns[j].PrimaryKeyNo = i + 1
			}
		}
	}

	// foreign keys
	fks, err := sqlbase.Recs(ctx, c.DB, `
SELECT k.constraint_name AS name, k.column_name AS col, k.referenced_table_schema AS rschema,
       k.referenced_table_name AS rtable, k.referenced_column_name AS rcol, r.update_rule AS upd, r.delete_rule AS del
FROM information_schema.key_column_usage k
JOIN information_schema.referential_constraints r ON r.constraint_schema = k.constraint_schema AND r.constraint_name = k.constraint_name
     AND r.table_name = k.table_name
WHERE k.table_schema = ? AND k.table_name = ? AND k.referenced_table_name IS NOT NULL
ORDER BY k.constraint_name, k.ordinal_position`, ref.Schema, ref.Name)
	if err != nil {
		return nil, mapError(err)
	}
	var fk *db.ForeignKey
	for _, r := range fks {
		if fk == nil || fk.Name != r["name"] {
			t.ForeignKeys = append(t.ForeignKeys, db.ForeignKey{Name: r["name"], OnUpdate: r["upd"], OnDelete: r["del"],
				RefTable: db.ObjectRef{Schema: r["rschema"], Name: r["rtable"], Type: db.ObjTable}})
			fk = &t.ForeignKeys[len(t.ForeignKeys)-1]
		}
		fk.Columns = append(fk.Columns, r["col"])
		fk.RefColumns = append(fk.RefColumns, r["rcol"])
	}

	// check constraints (MySQL 8.0.16+, MariaDB 10.2+)
	q := `SELECT tc.constraint_name AS name, cc.check_clause AS def FROM information_schema.table_constraints tc
JOIN information_schema.check_constraints cc ON cc.constraint_schema = tc.constraint_schema AND cc.constraint_name = tc.constraint_name
WHERE tc.table_schema = ? AND tc.table_name = ? AND tc.constraint_type = 'CHECK' ORDER BY tc.constraint_name`
	if c.Flavor == MariaDB {
		q = `SELECT constraint_name AS name, check_clause AS def FROM information_schema.check_constraints
WHERE constraint_schema = ? AND table_name = ? ORDER BY constraint_name`
	}
	if c.Flavor != TiDB {
		if chk, err := sqlbase.Recs(ctx, c.DB, q, ref.Schema, ref.Name); err == nil {
			for _, r := range chk {
				// MariaDB's JSON type is LONGTEXT CHECK (json_valid(col))
				if m := jsonValidRx.FindStringSubmatch(r["def"]); m != nil {
					for j := range t.Columns {
						if t.Columns[j].Name == m[1] {
							t.Columns[j].Kind = db.KindJSON
						}
					}
					continue
				}
				t.Constraints = append(t.Constraints, db.Constraint{Name: r["name"], Type: "CHECK", Definition: r["def"]})
			}
		}
	}
	for _, ix := range t.Indexes {
		if ix.Unique && !ix.Primary {
			t.Constraints = append(t.Constraints, db.Constraint{Name: ix.Name, Type: "UNIQUE", Columns: ix.Columns})
		}
	}

	if c.Flavor != TiDB {
		trg, err := sqlbase.Recs(ctx, c.DB, `
SELECT trigger_name AS name, action_timing AS timing, event_manipulation AS ev, action_statement AS body
FROM information_schema.triggers WHERE event_object_schema = ? AND event_object_table = ? ORDER BY trigger_name`, ref.Schema, ref.Name)
		if err != nil {
			return nil, mapError(err)
		}
		for _, r := range trg {
			t.Triggers = append(t.Triggers, db.Trigger{Name: r["name"], Timing: r["timing"], Events: r["ev"], Definition: r["body"]})
		}
	}
	return t, nil
}

var nowRx = regexp.MustCompile(`(?i)^(current_timestamp|now|localtime|localtimestamp)(\(\d*\))?$`)

// defaultExpr turns information_schema.columns.column_default of MySQL/TiDB
// into an SQL expression: string literals are reported without quotes
// (DEFAULT ” is an empty string), expressions (extra DEFAULT_GENERATED,
// MySQL 8.0.13+) as text with escaped quotes.
func (c *Catalog) defaultExpr(def, dtype, extra string) string {
	if strings.Contains(extra, "default_generated") {
		def = strings.ReplaceAll(def, `\'`, "'")
		if nowRx.MatchString(def) || strings.HasPrefix(def, "(") {
			return def
		}
		return "(" + def + ")"
	}
	switch dtype {
	case "tinyint", "smallint", "mediumint", "int", "integer", "bigint", "decimal", "numeric", "float", "double", "real", "year":
		if _, err := strconv.ParseFloat(def, 64); err == nil {
			return def
		}
	case "bit":
		if strings.HasPrefix(def, "b'") {
			return def
		}
	case "binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob":
		if strings.HasPrefix(def, "0x") {
			return def
		}
	case "timestamp", "datetime":
		if nowRx.MatchString(def) { // MySQL 5.7, TiDB: no DEFAULT_GENERATED
			return def
		}
	}
	return c.D.QuoteString(def)
}

var jsonValidRx = regexp.MustCompile("^json_valid\\(`([^`]+)`\\)$")

// qualify rewrites the object name of a SHOW CREATE statement (and
// unqualified foreign key targets) with the schema, so the DDL runs from
// any current database.
func (c *Catalog) qualify(ddl string, ref db.ObjectRef, kind string) string {
	q := c.D.QuoteAlways(ref.Name)
	full := c.D.QuoteAlways(ref.Schema) + "." + q
	if i := strings.Index(ddl, kind+" "+q); i >= 0 {
		ddl = ddl[:i] + kind + " " + full + ddl[i+len(kind)+1+len(q):]
	}
	if kind == "TABLE" {
		ddl = refRx.ReplaceAllString(ddl, "REFERENCES "+c.D.QuoteAlways(ref.Schema)+".$1 (")
	}
	return ddl
}

// qualifyTrigger qualifies the trigger name and its ON table in SHOW CREATE
// TRIGGER output (MySQL quotes them, MariaDB keeps the original text).
func (c *Catalog) qualifyTrigger(ddl, schema string) string {
	var toks []sqlx.Token
	for _, t := range sqlx.Tokens(ddl, &lexOptions) {
		if t.Kind != sqlx.TSpace && t.Kind != sqlx.TComment {
			toks = append(toks, t)
		}
	}
	text := func(i int) string { return ddl[toks[i].Start:toks[i].End] }
	isWord := func(i int, w string) bool {
		return i < len(toks) && toks[i].Kind == sqlx.TWord && strings.EqualFold(text(i), w)
	}
	isName := func(i int) bool {
		return i < len(toks) && (toks[i].Kind == sqlx.TWord || toks[i].Kind == sqlx.TQuotedIdent)
	}
	var at []int // tokens to prefix with the schema
	i := 0
	for i < len(toks) && !isWord(i, "TRIGGER") {
		i++
	}
	i++
	if isWord(i, "IF") && isWord(i+1, "NOT") && isWord(i+2, "EXISTS") {
		i += 3
	}
	if !isName(i) {
		return ddl
	}
	if i+1 < len(toks) && text(i+1) == "." {
		i += 2
	} else {
		at = append(at, i)
	}
	for i++; i < len(toks) && !isWord(i, "ON"); i++ {
	}
	i++
	if isName(i) && !(i+1 < len(toks) && text(i+1) == ".") {
		at = append(at, i)
	}
	prefix := c.D.QuoteAlways(schema) + "."
	for k := len(at) - 1; k >= 0; k-- {
		p := toks[at[k]].Start
		ddl = ddl[:p] + prefix + ddl[p:]
	}
	return ddl
}

var refRx = regexp.MustCompile("REFERENCES (`[^`]+`) \\(")

// showCreate runs SHOW CREATE <kind> and returns the column holding the DDL.
func (c *Catalog) showCreate(ctx context.Context, kind string, ref db.ObjectRef) (string, error) {
	recs, err := sqlbase.Recs(ctx, c.DB, "SHOW CREATE "+kind+" "+c.D.Qualified(ref))
	if err != nil {
		return "", mapError(err)
	}
	if len(recs) == 0 {
		return "", fmt.Errorf("%s not found", ref)
	}
	for _, k := range []string{"create table", "create view", "create procedure", "create function", "sql original statement", "create sequence"} {
		if v, ok := recs[0][k]; ok && v != "" {
			return v, nil
		}
	}
	return "", fmt.Errorf("no definition returned for %s (missing privileges?)", ref)
}

func (c *Catalog) DDL(ctx context.Context, ref db.ObjectRef) (string, error) {
	switch ref.Type {
	case db.ObjView:
		s, err := c.showCreate(ctx, "VIEW", ref)
		return c.qualify(s, ref, "VIEW") + ";\n", err
	case db.ObjProcedure:
		s, err := c.showCreate(ctx, "PROCEDURE", ref)
		return c.qualify(s, ref, "PROCEDURE") + ";\n", err
	case db.ObjFunction:
		s, err := c.showCreate(ctx, "FUNCTION", ref)
		return c.qualify(s, ref, "FUNCTION") + ";\n", err
	case db.ObjSequence:
		s, err := c.showCreate(ctx, "SEQUENCE", ref)
		if err != nil && c.Flavor == TiDB {
			s, err = c.showCreate(ctx, "TABLE", ref)
		}
		return c.qualify(s, ref, "SEQUENCE") + ";\n", err
	}
	s, err := c.showCreate(ctx, "TABLE", ref)
	if err != nil {
		return "", err
	}
	s = c.qualify(s, ref, "TABLE")
	var b strings.Builder
	b.WriteString(s + ";\n")
	if c.Flavor != TiDB {
		names, err := sqlbase.Strings(ctx, c.DB, "SELECT trigger_name FROM information_schema.triggers WHERE event_object_schema = ? AND event_object_table = ? ORDER BY action_order", ref.Schema, ref.Name)
		if err == nil {
			for _, n := range names {
				tr, err := c.showCreate(ctx, "TRIGGER", db.ObjectRef{Schema: ref.Schema, Name: n})
				if err == nil {
					tr = c.qualifyTrigger(tr, ref.Schema)
					b.WriteString("\n" + tr + ";\n")
				}
			}
		}
	}
	return b.String(), nil
}

func (c *Catalog) Completion(ctx context.Context, schema db.ObjectRef) (map[string][]string, error) {
	recs, err := sqlbase.Recs(ctx, c.DB, `SELECT table_name AS t, column_name AS c FROM information_schema.columns
WHERE table_schema = ? ORDER BY table_name, ordinal_position`, schema.Schema)
	if err != nil {
		return nil, mapError(err)
	}
	return sqlbase.GroupCompletion(recs, "t", "c"), nil
}
