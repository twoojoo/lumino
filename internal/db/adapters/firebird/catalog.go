package firebird

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
}

// recs runs a catalog query in its own read-only transaction, committed
// right away: an idle pooled attachment that keeps a transaction open holds
// metadata locks and blocks DDL from other sessions.
func (c *catalog) recs(ctx context.Context, query string, args ...any) ([]sqlbase.Rec, error) {
	// not ReadOnly: the driver would keep the connection read-only afterwards
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	recs, err := sqlbase.Recs(ctx, tx, query, args...)
	if err != nil {
		return nil, err
	}
	return recs, tx.Commit()
}

func (c *catalog) parallel(ctx context.Context, qs ...sqlbase.Q) ([][]sqlbase.Rec, error) {
	out := make([][]sqlbase.Rec, len(qs))
	errs := make([]error, len(qs))
	var wg sync.WaitGroup
	for i, q := range qs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i], errs[i] = c.recs(ctx, q.SQL, q.Args...)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (c *catalog) one(ctx context.Context, query string, args ...any) (string, error) {
	recs, err := c.recs(ctx, query, args...)
	if err != nil || len(recs) == 0 {
		return "", err
	}
	for _, v := range recs[0] {
		return v, nil
	}
	return "", nil
}

func (c *catalog) strings(ctx context.Context, query string, args ...any) ([]string, error) {
	recs, err := c.recs(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		for _, v := range r {
			out = append(out, v)
		}
	}
	return out, nil
}

func (c *catalog) Catalogs(ctx context.Context) ([]string, error) { return nil, db.ErrNotSupported }

func (c *catalog) Schemas(ctx context.Context, _ string) ([]db.Schema, error) {
	return nil, db.ErrNotSupported
}

func (c *catalog) Objects(ctx context.Context, _ db.ObjectRef, types ...db.ObjectType) ([]db.Object, error) {
	res, err := c.parallel(ctx,
		sqlbase.Q{SQL: `SELECT TRIM(r.RDB$RELATION_NAME) AS name, CASE WHEN r.RDB$VIEW_BLR IS NULL THEN 0 ELSE 1 END AS isview,
       r.RDB$DESCRIPTION AS cmt
FROM RDB$RELATIONS r WHERE COALESCE(r.RDB$SYSTEM_FLAG, 0) = 0 ORDER BY 1`},
		sqlbase.Q{SQL: `SELECT TRIM(RDB$PROCEDURE_NAME) AS name, RDB$DESCRIPTION AS cmt FROM RDB$PROCEDURES
WHERE COALESCE(RDB$SYSTEM_FLAG, 0) = 0 AND RDB$PACKAGE_NAME IS NULL ORDER BY 1`},
		sqlbase.Q{SQL: `SELECT TRIM(RDB$FUNCTION_NAME) AS name, RDB$DESCRIPTION AS cmt FROM RDB$FUNCTIONS
WHERE COALESCE(RDB$SYSTEM_FLAG, 0) = 0 AND RDB$PACKAGE_NAME IS NULL ORDER BY 1`},
		sqlbase.Q{SQL: `SELECT TRIM(RDB$GENERATOR_NAME) AS name FROM RDB$GENERATORS WHERE COALESCE(RDB$SYSTEM_FLAG, 0) = 0 ORDER BY 1`},
	)
	if err != nil {
		return nil, mapError(err)
	}
	var out []db.Object
	add := func(t db.ObjectType, recs []sqlbase.Rec) {
		if !sqlbase.Wants(types, t) {
			return
		}
		for _, r := range recs {
			out = append(out, db.Object{Ref: db.ObjectRef{Name: r["name"], Type: t}, Comment: r["cmt"]})
		}
	}
	var tables, views []sqlbase.Rec
	for _, r := range res[0] {
		if r["isview"] == "1" {
			views = append(views, r)
		} else {
			tables = append(tables, r)
		}
	}
	add(db.ObjTable, tables)
	add(db.ObjView, views)
	add(db.ObjProcedure, res[1])
	add(db.ObjFunction, res[2])
	add(db.ObjSequence, res[3])
	return out, nil
}

// fieldSQL is the field columns query fragment (RDB$FIELDS aliased f).
const fieldSQL = `f.RDB$FIELD_TYPE AS ftype, COALESCE(f.RDB$FIELD_SUB_TYPE, 0) AS sub, f.RDB$FIELD_LENGTH AS len,
       COALESCE(f.RDB$CHARACTER_LENGTH, 0) AS clen, COALESCE(f.RDB$FIELD_PRECISION, 0) AS prec,
       COALESCE(f.RDB$FIELD_SCALE, 0) AS scale, COALESCE(f.RDB$CHARACTER_SET_ID, 0) AS cs`

// typeSQL renders an RDB$FIELDS type.
func typeSQL(r sqlbase.Rec) (string, db.Kind) {
	scale := -r.Int("scale")
	prec := r.Int("prec")
	sub := r.Int("sub")
	num := func(base string, defPrec int64) (string, db.Kind) {
		if scale > 0 || sub == 1 || sub == 2 {
			if prec == 0 {
				prec = defPrec
			}
			n := "NUMERIC"
			if sub == 2 {
				n = "DECIMAL"
			}
			return fmt.Sprintf("%s(%d,%d)", n, prec, scale), db.KindDecimal
		}
		return base, db.KindInt
	}
	switch r.Int("ftype") {
	case 7:
		return num("SMALLINT", 4)
	case 8:
		return num("INTEGER", 9)
	case 16:
		return num("BIGINT", 18)
	case 26:
		t, k := num("INT128", 38)
		if k == db.KindInt {
			k = db.KindDecimal
		}
		return t, k
	case 10:
		return "FLOAT", db.KindFloat
	case 27:
		return "DOUBLE PRECISION", db.KindFloat
	case 12:
		return "DATE", db.KindDate
	case 13:
		return "TIME", db.KindTime
	case 28:
		return "TIME WITH TIME ZONE", db.KindTime
	case 35:
		return "TIMESTAMP", db.KindTimestamp
	case 29:
		return "TIMESTAMP WITH TIME ZONE", db.KindTimestampTZ
	case 14:
		if r.Int("cs") == 1 {
			return fmt.Sprintf("BINARY(%d)", r.Int("len")), db.KindBytes
		}
		return fmt.Sprintf("CHAR(%d)", r.Int("clen")), db.KindText
	case 37:
		if r.Int("cs") == 1 {
			return fmt.Sprintf("VARBINARY(%d)", r.Int("len")), db.KindBytes
		}
		return fmt.Sprintf("VARCHAR(%d)", r.Int("clen")), db.KindText
	case 261:
		if sub == 1 {
			return "BLOB SUB_TYPE TEXT", db.KindText
		}
		return "BLOB SUB_TYPE BINARY", db.KindBytes
	case 23:
		return "BOOLEAN", db.KindBool
	case 24:
		return "DECFLOAT(16)", db.KindDecimal
	case 25:
		return "DECFLOAT(34)", db.KindDecimal
	}
	return fmt.Sprintf("TYPE %d", r.Int("ftype")), db.KindOther
}

func stripKeyword(s, kw string) string {
	s = strings.TrimSpace(s)
	if len(s) >= len(kw) && strings.EqualFold(s[:len(kw)], kw) {
		return strings.TrimSpace(s[len(kw):])
	}
	return s
}

var fbRule = map[string]string{"RESTRICT": "NO ACTION", "NO ACTION": "NO ACTION", "CASCADE": "CASCADE", "SET NULL": "SET NULL", "SET DEFAULT": "SET DEFAULT"}

func (c *catalog) Table(ctx context.Context, ref db.ObjectRef) (*db.TableInfo, error) {
	name := ref.Name
	res, err := c.parallel(ctx,
		sqlbase.Q{SQL: `SELECT TRIM(rf.RDB$FIELD_NAME) AS name, ` + fieldSQL + `,
       COALESCE(rf.RDB$NULL_FLAG, f.RDB$NULL_FLAG, 0) AS notnull,
       COALESCE(rf.RDB$DEFAULT_SOURCE, f.RDB$DEFAULT_SOURCE) AS def,
       f.RDB$COMPUTED_SOURCE AS comp, rf.RDB$IDENTITY_TYPE AS ident,
       rf.RDB$DESCRIPTION AS cmt
FROM RDB$RELATION_FIELDS rf JOIN RDB$FIELDS f ON f.RDB$FIELD_NAME = rf.RDB$FIELD_SOURCE
WHERE rf.RDB$RELATION_NAME = ? ORDER BY rf.RDB$FIELD_POSITION`, Args: []any{name}},
		sqlbase.Q{SQL: `SELECT TRIM(rc.RDB$CONSTRAINT_NAME) AS name, TRIM(rc.RDB$CONSTRAINT_TYPE) AS type, TRIM(rc.RDB$INDEX_NAME) AS idx,
       TRIM(refc.RDB$CONST_NAME_UQ) AS uq, TRIM(refc.RDB$UPDATE_RULE) AS upd, TRIM(refc.RDB$DELETE_RULE) AS del
FROM RDB$RELATION_CONSTRAINTS rc LEFT JOIN RDB$REF_CONSTRAINTS refc ON refc.RDB$CONSTRAINT_NAME = rc.RDB$CONSTRAINT_NAME
WHERE rc.RDB$RELATION_NAME = ? ORDER BY 1`, Args: []any{name}},
		sqlbase.Q{SQL: `SELECT TRIM(s.RDB$INDEX_NAME) AS idx, TRIM(s.RDB$FIELD_NAME) AS col FROM RDB$INDEX_SEGMENTS s
JOIN RDB$INDICES i ON i.RDB$INDEX_NAME = s.RDB$INDEX_NAME WHERE i.RDB$RELATION_NAME = ? ORDER BY 1, s.RDB$FIELD_POSITION`, Args: []any{name}},
		sqlbase.Q{SQL: `SELECT TRIM(i.RDB$INDEX_NAME) AS name, COALESCE(i.RDB$UNIQUE_FLAG, 0) AS uniq, COALESCE(i.RDB$INDEX_TYPE, 0) AS descending,
       i.RDB$EXPRESSION_SOURCE AS expr
FROM RDB$INDICES i WHERE i.RDB$RELATION_NAME = ? ORDER BY 1`, Args: []any{name}},
		sqlbase.Q{SQL: `SELECT TRIM(rc.RDB$CONSTRAINT_NAME) AS name, TRIM(rc.RDB$RELATION_NAME) AS rel, TRIM(s.RDB$FIELD_NAME) AS col
FROM RDB$RELATION_CONSTRAINTS rc JOIN RDB$INDEX_SEGMENTS s ON s.RDB$INDEX_NAME = rc.RDB$INDEX_NAME
WHERE rc.RDB$CONSTRAINT_NAME IN (SELECT refc.RDB$CONST_NAME_UQ FROM RDB$REF_CONSTRAINTS refc
  JOIN RDB$RELATION_CONSTRAINTS x ON x.RDB$CONSTRAINT_NAME = refc.RDB$CONSTRAINT_NAME WHERE x.RDB$RELATION_NAME = ?)
ORDER BY 1, s.RDB$FIELD_POSITION`, Args: []any{name}},
		sqlbase.Q{SQL: `SELECT DISTINCT TRIM(cc.RDB$CONSTRAINT_NAME) AS name, t.RDB$TRIGGER_SOURCE AS src
FROM RDB$CHECK_CONSTRAINTS cc JOIN RDB$TRIGGERS t ON t.RDB$TRIGGER_NAME = cc.RDB$TRIGGER_NAME
JOIN RDB$RELATION_CONSTRAINTS rc ON rc.RDB$CONSTRAINT_NAME = cc.RDB$CONSTRAINT_NAME
WHERE rc.RDB$RELATION_NAME = ? AND rc.RDB$CONSTRAINT_TYPE = 'CHECK' AND t.RDB$TRIGGER_TYPE = 1`, Args: []any{name}},
		sqlbase.Q{SQL: `SELECT TRIM(RDB$TRIGGER_NAME) AS name, RDB$TRIGGER_TYPE AS ttype, COALESCE(RDB$TRIGGER_SEQUENCE, 0) AS pos,
       COALESCE(RDB$TRIGGER_INACTIVE, 0) AS inactive, RDB$TRIGGER_SOURCE AS src
FROM RDB$TRIGGERS WHERE RDB$RELATION_NAME = ? AND COALESCE(RDB$SYSTEM_FLAG, 0) = 0 ORDER BY RDB$TRIGGER_SEQUENCE, 1`, Args: []any{name}},
		sqlbase.Q{SQL: `SELECT RDB$DESCRIPTION AS cmt FROM RDB$RELATIONS WHERE RDB$RELATION_NAME = ?`, Args: []any{name}},
	)
	if err != nil {
		return nil, mapError(err)
	}
	cols, cons, segs, idx, refs, checks, trg, meta := res[0], res[1], res[2], res[3], res[4], res[5], res[6], res[7]
	if len(cols) == 0 {
		return nil, fmt.Errorf("%s not found", ref)
	}
	t := &db.TableInfo{Ref: ref, RowEstimate: -1}
	for _, r := range cols {
		typ, kind := typeSQL(r)
		tc := db.TableColumn{Name: r["name"], Type: typ, Kind: kind, Nullable: r.Int("notnull") == 0, Comment: r["cmt"],
			AutoIncr: r["ident"] != ""}
		if comp := strings.TrimSpace(r["comp"]); comp != "" {
			tc.Generated = strings.TrimSuffix(strings.TrimPrefix(comp, "("), ")")
		} else if def := stripKeyword(r["def"], "DEFAULT"); def != "" {
			tc.Default, tc.HasDefault = def, true
		}
		t.Columns = append(t.Columns, tc)
	}
	if len(meta) == 1 {
		t.Comment = meta[0]["cmt"]
	}
	colsOf := map[string][]string{}
	for _, r := range segs {
		colsOf[r["idx"]] = append(colsOf[r["idx"]], r["col"])
	}
	refTarget := map[string]sqlbase.Rec{}
	refCols := map[string][]string{}
	for _, r := range refs {
		refTarget[r["name"]] = r
		refCols[r["name"]] = append(refCols[r["name"]], r["col"])
	}
	backing := map[string]string{} // index → constraint type
	for _, r := range cons {
		backing[r["idx"]] = r["type"]
		switch r["type"] {
		case "PRIMARY KEY":
			t.PrimaryKey = colsOf[r["idx"]]
		case "UNIQUE":
			t.Constraints = append(t.Constraints, db.Constraint{Name: r["name"], Type: "UNIQUE", Columns: colsOf[r["idx"]]})
		case "FOREIGN KEY":
			tg := refTarget[r["uq"]]
			t.ForeignKeys = append(t.ForeignKeys, db.ForeignKey{Name: r["name"], Columns: colsOf[r["idx"]],
				RefTable: db.ObjectRef{Name: tg["rel"], Type: db.ObjTable}, RefColumns: refCols[r["uq"]],
				OnUpdate: fbRule[r["upd"]], OnDelete: fbRule[r["del"]]})
		}
	}
	for _, r := range checks {
		t.Constraints = append(t.Constraints, db.Constraint{Name: r["name"], Type: "CHECK", Definition: stripKeyword(r["src"], "CHECK")})
	}
	for i, pk := range t.PrimaryKey {
		for j := range t.Columns {
			if t.Columns[j].Name == pk {
				t.Columns[j].PrimaryKeyNo = i + 1
			}
		}
	}
	for _, r := range idx {
		ix := db.Index{Name: r["name"], Unique: r.Int("uniq") == 1, Primary: backing[r["name"]] == "PRIMARY KEY", Columns: colsOf[r["name"]]}
		if e := strings.TrimSpace(r["expr"]); e != "" {
			ix.Columns = []string{stripKeyword(e, "COMPUTED BY")}
		}
		if r.Int("descending") == 1 {
			ix.Method = "descending"
		}
		t.Indexes = append(t.Indexes, ix)
	}
	for _, r := range trg {
		tm, ev := triggerType(r.Int("ttype"))
		t.Triggers = append(t.Triggers, db.Trigger{Name: r["name"], Timing: tm, Events: ev,
			Definition: triggerDDL(c.d, r, t.Ref.Name)})
	}
	return t, nil
}

// triggerType decodes RDB$TRIGGER_TYPE of table triggers.
func triggerType(tt int64) (string, string) {
	timing := "AFTER"
	if (tt+1)&1 == 0 {
		timing = "BEFORE"
	}
	names := map[int64]string{1: "INSERT", 2: "UPDATE", 3: "DELETE"}
	var ev []string
	for _, shift := range []uint{1, 3, 5} {
		if n := names[((tt+1)>>shift)&3]; n != "" {
			ev = append(ev, n)
		}
	}
	return timing, strings.Join(ev, " OR ")
}

func triggerDDL(d *dialect, r sqlbase.Rec, table string) string {
	tm, ev := triggerType(r.Int("ttype"))
	active := "ACTIVE"
	if r.Int("inactive") == 1 {
		active = "INACTIVE"
	}
	return fmt.Sprintf("CREATE OR ALTER TRIGGER %s FOR %s %s %s %s POSITION %d\n%s",
		d.Quote(r["name"]), d.Quote(table), active, tm, ev, r.Int("pos"), strings.TrimSpace(r["src"]))
}

// psql wraps PSQL statements for scripts (their bodies contain ";").
func psql(stmts ...string) string {
	var b strings.Builder
	b.WriteString("SET TERM ^ ;\n")
	for _, s := range stmts {
		b.WriteString("\n" + strings.TrimSpace(s) + "^\n")
	}
	b.WriteString("\nSET TERM ; ^\n")
	return b.String()
}

func (c *catalog) params(ctx context.Context, q string, name string) ([]sqlbase.Rec, error) {
	recs, err := c.recs(ctx, q, name)
	return recs, mapError(err)
}

func (c *catalog) DDL(ctx context.Context, ref db.ObjectRef) (string, error) {
	q := c.d.Quote
	switch ref.Type {
	case db.ObjView:
		src, err := c.one(ctx, "SELECT RDB$VIEW_SOURCE FROM RDB$RELATIONS WHERE RDB$RELATION_NAME = ?", ref.Name)
		if err != nil {
			return "", mapError(err)
		}
		cols, err := c.strings(ctx, "SELECT TRIM(RDB$FIELD_NAME) FROM RDB$RELATION_FIELDS WHERE RDB$RELATION_NAME = ? ORDER BY RDB$FIELD_POSITION", ref.Name)
		if err != nil {
			return "", mapError(err)
		}
		for i := range cols {
			cols[i] = q(cols[i])
		}
		return fmt.Sprintf("CREATE OR ALTER VIEW %s (%s) AS\n%s;\n", q(ref.Name), strings.Join(cols, ", "), strings.TrimSpace(src)), nil
	case db.ObjSequence:
		recs, err := c.recs(ctx, "SELECT COALESCE(RDB$INITIAL_VALUE, 0) AS init, COALESCE(RDB$GENERATOR_INCREMENT, 1) AS inc FROM RDB$GENERATORS WHERE RDB$GENERATOR_NAME = ?", ref.Name)
		if err != nil {
			return "", mapError(err)
		}
		if len(recs) == 0 {
			return "", fmt.Errorf("%s not found", ref)
		}
		return fmt.Sprintf("CREATE SEQUENCE %s START WITH %s INCREMENT BY %s;\n", q(ref.Name), recs[0]["init"], recs[0]["inc"]), nil
	case db.ObjProcedure:
		src, err := c.one(ctx, "SELECT RDB$PROCEDURE_SOURCE FROM RDB$PROCEDURES WHERE RDB$PROCEDURE_NAME = ? AND RDB$PACKAGE_NAME IS NULL", ref.Name)
		if err != nil {
			return "", mapError(err)
		}
		ps, err := c.params(ctx, `SELECT TRIM(p.RDB$PARAMETER_NAME) AS name, p.RDB$PARAMETER_TYPE AS io, `+fieldSQL+`
FROM RDB$PROCEDURE_PARAMETERS p JOIN RDB$FIELDS f ON f.RDB$FIELD_NAME = p.RDB$FIELD_SOURCE
WHERE p.RDB$PROCEDURE_NAME = ? AND p.RDB$PACKAGE_NAME IS NULL ORDER BY p.RDB$PARAMETER_TYPE, p.RDB$PARAMETER_NUMBER`, ref.Name)
		if err != nil {
			return "", err
		}
		var in, out []string
		for _, p := range ps {
			typ, _ := typeSQL(p)
			if p["io"] == "0" {
				in = append(in, q(p["name"])+" "+typ)
			} else {
				out = append(out, q(p["name"])+" "+typ)
			}
		}
		head := "CREATE OR ALTER PROCEDURE " + q(ref.Name)
		if len(in) > 0 {
			head += " (" + strings.Join(in, ", ") + ")"
		}
		if len(out) > 0 {
			head += "\nRETURNS (" + strings.Join(out, ", ") + ")"
		}
		return psql(head + "\nAS\n" + strings.TrimSpace(src)), nil
	case db.ObjFunction:
		src, err := c.one(ctx, "SELECT RDB$FUNCTION_SOURCE FROM RDB$FUNCTIONS WHERE RDB$FUNCTION_NAME = ? AND RDB$PACKAGE_NAME IS NULL", ref.Name)
		if err != nil {
			return "", mapError(err)
		}
		if strings.TrimSpace(src) == "" {
			return "", fmt.Errorf("%s is an external function: no source", ref)
		}
		ps, err := c.params(ctx, `SELECT TRIM(a.RDB$ARGUMENT_NAME) AS name, a.RDB$ARGUMENT_POSITION AS pos, `+fieldSQL+`
FROM RDB$FUNCTION_ARGUMENTS a JOIN RDB$FIELDS f ON f.RDB$FIELD_NAME = a.RDB$FIELD_SOURCE
WHERE a.RDB$FUNCTION_NAME = ? AND a.RDB$PACKAGE_NAME IS NULL ORDER BY a.RDB$ARGUMENT_POSITION`, ref.Name)
		if err != nil {
			return "", err
		}
		var args []string
		ret := ""
		for _, p := range ps {
			typ, _ := typeSQL(p)
			if p["pos"] == "0" {
				ret = typ
			} else {
				args = append(args, q(p["name"])+" "+typ)
			}
		}
		return psql(fmt.Sprintf("CREATE OR ALTER FUNCTION %s (%s)\nRETURNS %s\nAS\n%s", q(ref.Name), strings.Join(args, ", "), ret, strings.TrimSpace(src))), nil
	}
	t, err := c.Table(ctx, ref)
	if err != nil {
		return "", err
	}
	var defs []string
	for _, col := range t.Columns {
		s := "    " + q(col.Name) + " "
		switch {
		case col.Generated != "":
			defs = append(defs, s+"GENERATED ALWAYS AS ("+col.Generated+")")
			continue
		case col.AutoIncr:
			s += col.Type + " GENERATED BY DEFAULT AS IDENTITY"
		default:
			s += col.Type
			if col.HasDefault {
				s += " DEFAULT " + col.Default
			}
		}
		if !col.Nullable {
			s += " NOT NULL"
		}
		defs = append(defs, s)
	}
	list := func(cols []string) string {
		out := make([]string, len(cols))
		for i, x := range cols {
			out[i] = q(x)
		}
		return strings.Join(out, ", ")
	}
	// generated constraint names (INTEG_n) are left to the server
	named := func(n string) string {
		if n == "" || strings.HasPrefix(n, "INTEG_") {
			return "    "
		}
		return "    CONSTRAINT " + q(n) + " "
	}
	backing := map[string]bool{}
	if len(t.PrimaryKey) > 0 {
		pk, _ := c.one(ctx, "SELECT TRIM(RDB$CONSTRAINT_NAME) FROM RDB$RELATION_CONSTRAINTS WHERE RDB$RELATION_NAME = ? AND RDB$CONSTRAINT_TYPE = 'PRIMARY KEY'", ref.Name)
		defs = append(defs, named(pk)+"PRIMARY KEY ("+list(t.PrimaryKey)+")")
	}
	for _, ct := range t.Constraints {
		switch ct.Type {
		case "UNIQUE":
			defs = append(defs, named(ct.Name)+"UNIQUE ("+list(ct.Columns)+")")
		case "CHECK":
			defs = append(defs, named(ct.Name)+"CHECK "+ct.Definition)
		}
	}
	for _, fk := range t.ForeignKeys {
		s := fmt.Sprintf("%sFOREIGN KEY (%s) REFERENCES %s (%s)", named(fk.Name), list(fk.Columns), q(fk.RefTable.Name), list(fk.RefColumns))
		if fk.OnUpdate != "" && fk.OnUpdate != "NO ACTION" {
			s += " ON UPDATE " + fk.OnUpdate
		}
		if fk.OnDelete != "" && fk.OnDelete != "NO ACTION" {
			s += " ON DELETE " + fk.OnDelete
		}
		defs = append(defs, s)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE %s (\n%s\n);\n", q(ref.Name), strings.Join(defs, ",\n"))
	// indexes created by constraints share the constraint name
	cons, _ := c.strings(ctx, "SELECT TRIM(RDB$INDEX_NAME) FROM RDB$RELATION_CONSTRAINTS WHERE RDB$RELATION_NAME = ? AND RDB$INDEX_NAME IS NOT NULL", ref.Name)
	for _, n := range cons {
		backing[n] = true
	}
	for _, ix := range t.Indexes {
		if backing[ix.Name] {
			continue
		}
		u := ""
		if ix.Unique {
			u = "UNIQUE "
		}
		if ix.Method == "descending" {
			u += "DESCENDING "
		}
		cols := "(" + list(ix.Columns) + ")"
		if len(ix.Columns) == 1 && !isIdent(ix.Columns[0]) {
			cols = "COMPUTED BY (" + strings.TrimSuffix(strings.TrimPrefix(ix.Columns[0], "("), ")") + ")"
		}
		fmt.Fprintf(&b, "\nCREATE %sINDEX %s ON %s %s;\n", u, q(ix.Name), q(ref.Name), cols)
	}
	if t.Comment != "" {
		fmt.Fprintf(&b, "\nCOMMENT ON TABLE %s IS %s;\n", q(ref.Name), c.d.QuoteString(t.Comment))
	}
	if len(t.Triggers) > 0 {
		var trg []string
		for _, tr := range t.Triggers {
			trg = append(trg, tr.Definition)
		}
		b.WriteString("\n" + psql(trg...))
	}
	return b.String(), nil
}

func isIdent(s string) bool {
	for _, c := range s {
		if !(c == '_' || c == '$' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return s != ""
}

func (c *catalog) Completion(ctx context.Context, _ db.ObjectRef) (map[string][]string, error) {
	recs, err := c.recs(ctx, `SELECT TRIM(rf.RDB$RELATION_NAME) AS t, TRIM(rf.RDB$FIELD_NAME) AS c
FROM RDB$RELATION_FIELDS rf JOIN RDB$RELATIONS r ON r.RDB$RELATION_NAME = rf.RDB$RELATION_NAME
WHERE COALESCE(r.RDB$SYSTEM_FLAG, 0) = 0 ORDER BY 1, rf.RDB$FIELD_POSITION`)
	if err != nil {
		return nil, mapError(err)
	}
	return sqlbase.GroupCompletion(recs, "t", "c"), nil
}
