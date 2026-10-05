package oracle

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/db/sqlbase"
)

type catalog struct {
	db *sql.DB
	d  *dialect

	// DBMS_METADATA is slow to initialize in every session (seconds): DDL
	// requests share one session with the transforms already set
	ddlMu   sync.Mutex
	ddlConn *sql.Conn
}

func (c *catalog) metaConn(ctx context.Context) (*sql.Conn, error) {
	if c.ddlConn != nil {
		if err := c.ddlConn.PingContext(ctx); err == nil {
			return c.ddlConn, nil
		}
		c.ddlConn.Close()
		c.ddlConn = nil
	}
	conn, err := c.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	// session transforms: no storage clauses, statement terminators
	if _, err := conn.ExecContext(ctx, `BEGIN
  DBMS_METADATA.SET_TRANSFORM_PARAM(DBMS_METADATA.SESSION_TRANSFORM, 'DEFAULT', TRUE);
  DBMS_METADATA.SET_TRANSFORM_PARAM(DBMS_METADATA.SESSION_TRANSFORM, 'SEGMENT_ATTRIBUTES', FALSE);
  DBMS_METADATA.SET_TRANSFORM_PARAM(DBMS_METADATA.SESSION_TRANSFORM, 'STORAGE', FALSE);
  DBMS_METADATA.SET_TRANSFORM_PARAM(DBMS_METADATA.SESSION_TRANSFORM, 'SQLTERMINATOR', TRUE);
  DBMS_METADATA.SET_TRANSFORM_PARAM(DBMS_METADATA.SESSION_TRANSFORM, 'PRETTY', TRUE);
END;`); err != nil {
		conn.Close()
		return nil, err
	}
	c.ddlConn = conn
	return conn, nil
}

func (c *catalog) Catalogs(ctx context.Context) ([]string, error) { return nil, db.ErrNotSupported }

func (c *catalog) Schemas(ctx context.Context, _ string) ([]db.Schema, error) {
	recs, err := sqlbase.Recs(ctx, c.db, `
SELECT u.username AS name, u.oracle_maintained AS sys,
       CASE WHEN u.username = SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') THEN 'Y' ELSE 'N' END AS cur
FROM all_users u ORDER BY CASE WHEN u.oracle_maintained = 'Y' THEN 1 ELSE 0 END, u.username`)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]db.Schema, len(recs))
	for i, r := range recs {
		out[i] = db.Schema{Name: r["name"], System: r.Bool("sys"), Current: r.Bool("cur")}
	}
	return out, nil
}

var objTypes = map[string]db.ObjectType{"TABLE": db.ObjTable, "VIEW": db.ObjView, "MATERIALIZED VIEW": db.ObjMatView,
	"SEQUENCE": db.ObjSequence, "FUNCTION": db.ObjFunction, "PROCEDURE": db.ObjProcedure, "PACKAGE": db.ObjPackage}

func (c *catalog) Objects(ctx context.Context, schema db.ObjectRef, types ...db.ObjectType) ([]db.Object, error) {
	recs, err := sqlbase.Recs(ctx, c.db, `
SELECT o.object_name AS name, o.object_type AS type, o.status,
       COALESCE((SELECT t.num_rows FROM all_tables t WHERE t.owner = o.owner AND t.table_name = o.object_name), -1) AS est,
       COALESCE((SELECT tc.comments FROM all_tab_comments tc WHERE tc.owner = o.owner AND tc.table_name = o.object_name), '') AS cmt
FROM all_objects o
WHERE o.owner = :1 AND o.object_type IN ('TABLE','VIEW','MATERIALIZED VIEW','SEQUENCE','FUNCTION','PROCEDURE','PACKAGE')
  AND o.object_name NOT LIKE 'BIN$%' AND o.generated = 'N'
  AND NOT (o.object_type = 'TABLE' AND EXISTS (SELECT 1 FROM all_mviews m WHERE m.owner = o.owner AND m.mview_name = o.object_name))
ORDER BY o.object_name`, schema.Schema)
	if err != nil {
		return nil, mapError(err)
	}
	var out []db.Object
	for _, r := range recs {
		t := objTypes[r["type"]]
		if !sqlbase.Wants(types, t) {
			continue
		}
		o := db.Object{Ref: db.ObjectRef{Schema: schema.Schema, Name: r["name"], Type: t}, Comment: r["cmt"]}
		if r["status"] == "INVALID" {
			o.Extra = "invalid"
		} else if t == db.ObjTable && r.Int("est") > 0 {
			o.Extra = fmt.Sprintf("~%d rows", r.Int("est"))
		}
		out = append(out, o)
	}
	return out, nil
}

// typeSQL renders a dictionary type like SQL Developer.
func typeSQL(r sqlbase.Rec) string {
	t := r["data_type"]
	switch t {
	case "VARCHAR2", "NVARCHAR2", "CHAR", "NCHAR":
		n := r.Int("char_length")
		if t[0] == 'N' {
			return fmt.Sprintf("%s(%d)", t, n)
		}
		unit := "BYTE"
		if r["char_used"] == "C" {
			unit = "CHAR"
		}
		return fmt.Sprintf("%s(%d %s)", t, n, unit)
	case "NUMBER":
		if r["data_precision"] == "" {
			if r["data_scale"] == "0" {
				return "NUMBER(*,0)"
			}
			return "NUMBER"
		}
		if s := r.Int("data_scale"); s != 0 {
			return fmt.Sprintf("NUMBER(%s,%d)", r["data_precision"], s)
		}
		return fmt.Sprintf("NUMBER(%s)", r["data_precision"])
	case "RAW":
		return fmt.Sprintf("RAW(%d)", r.Int("data_length"))
	case "FLOAT":
		return fmt.Sprintf("FLOAT(%s)", r["data_precision"])
	}
	return t
}

var ruleMap = map[string]string{"CASCADE": "CASCADE", "SET NULL": "SET NULL", "NO ACTION": "NO ACTION"}

func (c *catalog) Table(ctx context.Context, ref db.ObjectRef) (*db.TableInfo, error) {
	t := &db.TableInfo{Ref: ref, RowEstimate: -1}
	// the dictionary views are slow: query them concurrently
	res, err := sqlbase.RecsParallel(ctx, c.db,
		sqlbase.Q{SQL: `
SELECT c.column_name, c.data_type, c.data_length, c.char_length, c.char_used, c.data_precision, c.data_scale,
       c.nullable, c.data_default, c.identity_column, c.virtual_column, cc.comments
FROM all_tab_cols c
LEFT JOIN all_col_comments cc ON cc.owner = c.owner AND cc.table_name = c.table_name AND cc.column_name = c.column_name
WHERE c.owner = :1 AND c.table_name = :2 AND c.hidden_column = 'NO'
ORDER BY c.column_id`, Args: []any{ref.Schema, ref.Name}},
		sqlbase.Q{SQL: `
SELECT k.constraint_name AS name, k.constraint_type AS type, k.search_condition_vc AS cond, k.generated AS gen,
       k.delete_rule AS del, k.r_owner AS rowner, k.r_constraint_name AS rname
FROM all_constraints k
WHERE k.owner = :1 AND k.table_name = :2 AND k.constraint_type IN ('P','U','C','R')
ORDER BY k.constraint_type, k.constraint_name`, Args: []any{ref.Schema, ref.Name}},
		sqlbase.Q{SQL: `SELECT constraint_name AS name, column_name AS col FROM all_cons_columns
WHERE owner = :1 AND table_name = :2 ORDER BY constraint_name, position`, Args: []any{ref.Schema, ref.Name}},
		sqlbase.Q{SQL: `SELECT rc.owner, rc.constraint_name AS name, rc.table_name AS tbl, rc.column_name AS col
FROM all_cons_columns rc WHERE (rc.owner, rc.constraint_name) IN
  (SELECT k.r_owner, k.r_constraint_name FROM all_constraints k WHERE k.owner = :1 AND k.table_name = :2 AND k.constraint_type = 'R')
ORDER BY rc.owner, rc.constraint_name, rc.position`, Args: []any{ref.Schema, ref.Name}},
		sqlbase.Q{SQL: `
SELECT i.index_name AS name, i.uniqueness, i.index_type AS itype, ic.column_name AS col, ie.column_expression AS expr,
       CASE WHEN EXISTS (SELECT 1 FROM all_constraints k WHERE k.owner = i.table_owner AND k.table_name = i.table_name
                         AND k.index_name = i.index_name AND k.constraint_type = 'P') THEN 'Y' ELSE 'N' END AS prim
FROM all_indexes i
JOIN all_ind_columns ic ON ic.index_owner = i.owner AND ic.index_name = i.index_name
LEFT JOIN all_ind_expressions ie ON ie.index_owner = ic.index_owner AND ie.index_name = ic.index_name AND ie.column_position = ic.column_position
WHERE i.table_owner = :1 AND i.table_name = :2 AND i.index_type NOT IN ('LOB')
ORDER BY prim DESC, i.index_name, ic.column_position`, Args: []any{ref.Schema, ref.Name}},
		sqlbase.Q{SQL: `
SELECT trigger_name AS name, trigger_type AS ttype, triggering_event AS ev, description, trigger_body AS body
FROM all_triggers WHERE table_owner = :1 AND table_name = :2 ORDER BY trigger_name`, Args: []any{ref.Schema, ref.Name}},
		sqlbase.Q{SQL: "SELECT comments FROM all_tab_comments WHERE owner = :1 AND table_name = :2", Args: []any{ref.Schema, ref.Name}},
		sqlbase.Q{SQL: "SELECT num_rows FROM all_tables WHERE owner = :1 AND table_name = :2", Args: []any{ref.Schema, ref.Name}},
	)
	if err != nil {
		return nil, mapError(err)
	}
	cols, cons, consCols, refCols, idx, trg, cmt, est := res[0], res[1], res[2], res[3], res[4], res[5], res[6], res[7]
	if len(cols) == 0 {
		return nil, fmt.Errorf("%s not found", ref)
	}
	for _, r := range cols {
		def := strings.TrimSpace(r["data_default"])
		tc := db.TableColumn{Name: r["column_name"], Type: typeSQL(r), Nullable: r["nullable"] == "Y",
			Kind:    kindOfDef(r["data_type"], r.Int("data_precision"), r.Int("data_scale"), r["data_precision"] != "" || r["data_scale"] == "0"),
			Comment: r["comments"], AutoIncr: r["identity_column"] == "YES"}
		switch {
		case r["virtual_column"] == "YES":
			tc.Generated = def
		case tc.AutoIncr:
			// the default is the identity sequence
		case def != "" && !strings.EqualFold(def, "NULL"):
			tc.Default, tc.HasDefault = def, true
		}
		t.Columns = append(t.Columns, tc)
	}
	colsOf := map[string][]string{}
	for _, r := range consCols {
		colsOf[r["name"]] = append(colsOf[r["name"]], r["col"])
	}
	type target struct {
		table string
		cols  []string
	}
	refs := map[string]*target{}
	for _, r := range refCols {
		k := r["owner"] + "." + r["name"]
		if refs[k] == nil {
			refs[k] = &target{table: r["tbl"]}
		}
		refs[k].cols = append(refs[k].cols, r["col"])
	}
	for _, r := range cons {
		cols := colsOf[r["name"]]
		switch r["type"] {
		case "P":
			t.PrimaryKey = cols
		case "U":
			t.Constraints = append(t.Constraints, db.Constraint{Name: r["name"], Type: "UNIQUE", Columns: cols})
		case "C":
			cond := r["cond"]
			// implicit NOT NULL checks are shown as column nullability
			if r["gen"] == "GENERATED NAME" && strings.HasSuffix(cond, " IS NOT NULL") && len(cols) == 1 {
				continue
			}
			t.Constraints = append(t.Constraints, db.Constraint{Name: r["name"], Type: "CHECK", Columns: cols, Definition: cond})
		case "R":
			fk := db.ForeignKey{Name: r["name"], Columns: cols, OnDelete: ruleMap[r["del"]],
				RefTable: db.ObjectRef{Schema: r["rowner"], Type: db.ObjTable}}
			if tg := refs[r["rowner"]+"."+r["rname"]]; tg != nil {
				fk.RefTable.Name, fk.RefColumns = tg.table, tg.cols
			}
			t.ForeignKeys = append(t.ForeignKeys, fk)
		}
	}
	for i, pk := range t.PrimaryKey {
		for j := range t.Columns {
			if t.Columns[j].Name == pk {
				t.Columns[j].PrimaryKeyNo = i + 1
			}
		}
	}
	var cur *db.Index
	for _, r := range idx {
		if cur == nil || cur.Name != r["name"] {
			t.Indexes = append(t.Indexes, db.Index{Name: r["name"], Unique: r["uniqueness"] == "UNIQUE", Primary: r["prim"] == "Y"})
			cur = &t.Indexes[len(t.Indexes)-1]
			if it := r["itype"]; it != "NORMAL" && it != "FUNCTION-BASED NORMAL" {
				cur.Method = strings.ToLower(it)
			}
		}
		col := r["col"]
		if e := strings.TrimSpace(r["expr"]); e != "" {
			col = e
		}
		cur.Columns = append(cur.Columns, col)
	}
	for _, r := range trg {
		tm := r["ttype"]
		if i := strings.Index(tm, " "); i > 0 {
			tm = tm[:i]
		}
		if strings.HasPrefix(r["ttype"], "INSTEAD") {
			tm = "INSTEAD OF"
		}
		t.Triggers = append(t.Triggers, db.Trigger{Name: r["name"], Timing: tm, Events: strings.ReplaceAll(r["ev"], " OR ", ", "),
			Definition: "CREATE OR REPLACE TRIGGER " + strings.TrimSpace(r["description"]) + "\n" + r["body"]})
	}
	if len(cmt) == 1 {
		t.Comment = cmt[0]["comments"]
	}
	if len(est) == 1 && est[0]["num_rows"] != "" {
		t.RowEstimate = est[0].Int("num_rows")
	}
	return t, nil
}

var ddlTypes = map[db.ObjectType]string{db.ObjTable: "TABLE", db.ObjView: "VIEW", db.ObjMatView: "MATERIALIZED_VIEW",
	db.ObjSequence: "SEQUENCE", db.ObjFunction: "FUNCTION", db.ObjProcedure: "PROCEDURE", db.ObjPackage: "PACKAGE"}

func (c *catalog) DDL(ctx context.Context, ref db.ObjectRef) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, db.MetaTimeout)
	defer cancel()
	c.ddlMu.Lock()
	defer c.ddlMu.Unlock()
	conn, err := c.metaConn(ctx)
	if err != nil {
		return "", mapError(err)
	}
	typ := ddlTypes[ref.Type]
	if typ == "" {
		typ = "TABLE"
	}
	get := func(q string, args ...any) (string, error) {
		var v sql.NullString
		err := conn.QueryRowContext(ctx, q, args...).Scan(&v)
		return v.String, err
	}
	main, err := get("SELECT DBMS_METADATA.GET_DDL(:1, :2, :3) FROM dual", typ, ref.Name, ref.Schema)
	if err != nil {
		return "", mapError(err)
	}
	var b strings.Builder
	b.WriteString(fixPLSQL(strings.TrimSpace(main), typ))
	b.WriteString("\n")
	if typ == "PACKAGE" {
		if body, err := get("SELECT DBMS_METADATA.GET_DDL('PACKAGE_BODY', :1, :2) FROM dual", ref.Name, ref.Schema); err == nil && body != "" {
			b.WriteString("\n" + fixPLSQL(strings.TrimSpace(body), typ) + "\n")
		}
	}
	if typ == "TABLE" {
		// indexes not created by PRIMARY KEY / UNIQUE constraints
		rows, err := conn.QueryContext(ctx, `SELECT i.index_name FROM all_indexes i WHERE i.table_owner = :1 AND i.table_name = :2
  AND i.index_type NOT IN ('LOB') AND NOT EXISTS (SELECT 1 FROM all_constraints k WHERE k.owner = i.table_owner
  AND k.table_name = i.table_name AND k.index_name = i.index_name AND k.constraint_type IN ('P','U'))
ORDER BY i.index_name`, ref.Schema, ref.Name)
		if err == nil {
			var names []string
			for rows.Next() {
				var n string
				if rows.Scan(&n) == nil {
					names = append(names, n)
				}
			}
			rows.Close()
			for _, n := range names {
				if s, err := get("SELECT DBMS_METADATA.GET_DDL('INDEX', :1, :2) FROM dual", n, ref.Schema); err == nil {
					b.WriteString("\n" + strings.TrimSpace(s) + "\n")
				}
			}
		}
		for _, dep := range []string{"TRIGGER", "COMMENT"} {
			// ORA-31608: no dependent objects of this type
			s, err := get("SELECT DBMS_METADATA.GET_DEPENDENT_DDL(:1, :2, :3) FROM dual", dep, ref.Name, ref.Schema)
			if err != nil || strings.TrimSpace(s) == "" {
				continue
			}
			if dep == "TRIGGER" {
				s = fixPLSQL(strings.TrimSpace(s), "TRIGGER")
			}
			b.WriteString("\n" + strings.TrimSpace(s) + "\n")
		}
	}
	return b.String(), nil
}

// fixPLSQL terminates PL/SQL units with "/" lines (SQL*Plus convention,
// understood by the splitter) instead of the ";" DBMS_METADATA appends.
func fixPLSQL(s, typ string) string {
	switch typ {
	case "FUNCTION", "PROCEDURE", "PACKAGE", "TRIGGER":
	default:
		return s
	}
	if strings.HasSuffix(s, "\n/") {
		return s
	}
	// trigger DDL is "CREATE TRIGGER …END;\nALTER TRIGGER … ENABLE;"
	if i := strings.Index(s, "\nALTER TRIGGER"); i > 0 {
		return strings.TrimSpace(s[:i]) + "\n/\n" + strings.TrimSpace(s[i:])
	}
	return s + "\n/"
}

func (c *catalog) Completion(ctx context.Context, schema db.ObjectRef) (map[string][]string, error) {
	recs, err := sqlbase.Recs(ctx, c.db, `SELECT table_name AS t, column_name AS c FROM all_tab_columns WHERE owner = :1 ORDER BY table_name, column_id`, schema.Schema)
	if err != nil {
		return nil, mapError(err)
	}
	return sqlbase.GroupCompletion(recs, "t", "c"), nil
}
