package mssql

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
	recs, err := sqlbase.Recs(ctx, c.db, `
SELECT s.name, CASE WHEN s.name IN ('sys','INFORMATION_SCHEMA','guest') THEN 1 ELSE 0 END AS system,
       CASE WHEN s.name = SCHEMA_NAME() THEN 1 ELSE 0 END AS cur
FROM sys.schemas s
WHERE s.schema_id < 16384 OR s.schema_id > 16399
ORDER BY 2, s.name`)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]db.Schema, len(recs))
	for i, r := range recs {
		out[i] = db.Schema{Name: r["name"], System: r.Bool("system"), Current: r.Bool("cur")}
	}
	return out, nil
}

var objTypes = map[string]db.ObjectType{"U": db.ObjTable, "V": db.ObjView, "P": db.ObjProcedure, "FN": db.ObjFunction,
	"IF": db.ObjFunction, "TF": db.ObjFunction, "SO": db.ObjSequence}

func (c *catalog) Objects(ctx context.Context, schema db.ObjectRef, types ...db.ObjectType) ([]db.Object, error) {
	recs, err := sqlbase.Recs(ctx, c.db, `
SELECT o.name, RTRIM(o.type) AS type, COALESCE(CAST(ep.value AS nvarchar(4000)), '') AS comment,
       COALESCE((SELECT SUM(p.rows) FROM sys.partitions p WHERE p.object_id = o.object_id AND p.index_id IN (0, 1)), -1) AS est
FROM sys.objects o
LEFT JOIN sys.extended_properties ep ON ep.major_id = o.object_id AND ep.minor_id = 0 AND ep.class = 1 AND ep.name = 'MS_Description'
WHERE o.schema_id = SCHEMA_ID(@p1) AND o.type IN ('U','V','P','FN','IF','TF','SO') AND o.is_ms_shipped = 0
ORDER BY o.name`, schema.Schema)
	if err != nil {
		return nil, mapError(err)
	}
	var out []db.Object
	for _, r := range recs {
		t := objTypes[r["type"]]
		if !sqlbase.Wants(types, t) {
			continue
		}
		o := db.Object{Ref: db.ObjectRef{Schema: schema.Schema, Name: r["name"], Type: t}, Comment: r["comment"]}
		if t == db.ObjTable && r.Int("est") >= 0 {
			o.Extra = fmt.Sprintf("%d rows", r.Int("est"))
		}
		out = append(out, o)
	}
	return out, nil
}

func (c *catalog) objectID(ctx context.Context, ref db.ObjectRef) (string, error) {
	id, err := sqlbase.One(ctx, c.db, "SELECT OBJECT_ID(@p1)", c.d.QuoteAlways(ref.Schema)+"."+c.d.QuoteAlways(ref.Name))
	if err != nil {
		return "", mapError(err)
	}
	if id == "" {
		return "", fmt.Errorf("%s not found", ref)
	}
	return id, nil
}

// typeSQL renders a column type like SSMS does.
func typeSQL(r sqlbase.Rec) string {
	t := r["type"]
	ml := r.Int("max_length")
	switch t {
	case "varchar", "char", "varbinary", "binary":
		if ml == -1 {
			return t + "(max)"
		}
		return fmt.Sprintf("%s(%d)", t, ml)
	case "nvarchar", "nchar":
		if ml == -1 {
			return t + "(max)"
		}
		return fmt.Sprintf("%s(%d)", t, ml/2)
	case "decimal", "numeric":
		return fmt.Sprintf("%s(%d,%d)", t, r.Int("precision"), r.Int("scale"))
	case "datetime2", "time", "datetimeoffset":
		return fmt.Sprintf("%s(%d)", t, r.Int("scale"))
	case "float":
		if p := r.Int("precision"); p != 53 {
			return fmt.Sprintf("float(%d)", p)
		}
	}
	return t
}

var fkAction = map[string]string{"NO_ACTION": "NO ACTION", "CASCADE": "CASCADE", "SET_NULL": "SET NULL", "SET_DEFAULT": "SET DEFAULT"}

func (c *catalog) Table(ctx context.Context, ref db.ObjectRef) (*db.TableInfo, error) {
	id, err := c.objectID(ctx, ref)
	if err != nil {
		return nil, err
	}
	t := &db.TableInfo{Ref: ref, RowEstimate: -1}
	cols, err := sqlbase.Recs(ctx, c.db, `
SELECT c.name, TYPE_NAME(c.user_type_id) AS type, c.max_length, c.precision, c.scale, c.is_nullable,
       c.is_identity, COALESCE(dc.definition, '') AS def, COALESCE(cc.definition, '') AS gen,
       COALESCE(CAST(ic.seed_value AS nvarchar(40)), '') AS seed, COALESCE(CAST(ic.increment_value AS nvarchar(40)), '') AS incr,
       COALESCE(CAST(ep.value AS nvarchar(4000)), '') AS comment, COALESCE(dc.name, '') AS defname
FROM sys.columns c
LEFT JOIN sys.default_constraints dc ON dc.object_id = c.default_object_id
LEFT JOIN sys.computed_columns cc ON cc.object_id = c.object_id AND cc.column_id = c.column_id
LEFT JOIN sys.identity_columns ic ON ic.object_id = c.object_id AND ic.column_id = c.column_id
LEFT JOIN sys.extended_properties ep ON ep.major_id = c.object_id AND ep.minor_id = c.column_id AND ep.class = 1 AND ep.name = 'MS_Description'
WHERE c.object_id = @p1 ORDER BY c.column_id`, id)
	if err != nil {
		return nil, mapError(err)
	}
	for _, r := range cols {
		typ := typeSQL(r)
		tc := db.TableColumn{Name: r["name"], Type: typ, Kind: KindOf(r["type"]), Nullable: r.Bool("is_nullable"),
			Default: r["def"], HasDefault: r["def"] != "", AutoIncr: r.Bool("is_identity"), Generated: r["gen"], Comment: r["comment"]}
		if tc.AutoIncr {
			tc.Type = fmt.Sprintf("%s IDENTITY(%s,%s)", typ, r["seed"], r["incr"])
		}
		t.Columns = append(t.Columns, tc)
	}
	idx, err := sqlbase.Recs(ctx, c.db, `
SELECT i.name, i.is_unique, i.is_primary_key, i.is_unique_constraint, i.type_desc, COALESCE(i.filter_definition, '') AS filter,
       col.name AS col, ic.key_ordinal, ic.is_descending_key
FROM sys.indexes i
JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id AND ic.is_included_column = 0
JOIN sys.columns col ON col.object_id = ic.object_id AND col.column_id = ic.column_id
WHERE i.object_id = @p1 AND i.type > 0
ORDER BY i.is_primary_key DESC, i.name, ic.key_ordinal`, id)
	if err != nil {
		return nil, mapError(err)
	}
	var cur *db.Index
	for _, r := range idx {
		if cur == nil || cur.Name != r["name"] {
			t.Indexes = append(t.Indexes, db.Index{Name: r["name"], Unique: r.Bool("is_unique"), Primary: r.Bool("is_primary_key"),
				Method: strings.ToLower(r["type_desc"]), Where: r["filter"]})
			cur = &t.Indexes[len(t.Indexes)-1]
			if r.Bool("is_unique_constraint") {
				defer func(name string) {
					for _, ix := range t.Indexes {
						if ix.Name == name {
							t.Constraints = append(t.Constraints, db.Constraint{Name: name, Type: "UNIQUE", Columns: ix.Columns})
						}
					}
				}(r["name"])
			}
		}
		col := r["col"]
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
	fks, err := sqlbase.Recs(ctx, c.db, `
SELECT fk.name, pc.name AS col, SCHEMA_NAME(rt.schema_id) AS rschema, rt.name AS rtable, rc.name AS rcol,
       fk.update_referential_action_desc AS upd, fk.delete_referential_action_desc AS del
FROM sys.foreign_keys fk
JOIN sys.foreign_key_columns fkc ON fkc.constraint_object_id = fk.object_id
JOIN sys.columns pc ON pc.object_id = fkc.parent_object_id AND pc.column_id = fkc.parent_column_id
JOIN sys.tables rt ON rt.object_id = fkc.referenced_object_id
JOIN sys.columns rc ON rc.object_id = fkc.referenced_object_id AND rc.column_id = fkc.referenced_column_id
WHERE fk.parent_object_id = @p1 ORDER BY fk.name, fkc.constraint_column_id`, id)
	if err != nil {
		return nil, mapError(err)
	}
	var fk *db.ForeignKey
	for _, r := range fks {
		if fk == nil || fk.Name != r["name"] {
			t.ForeignKeys = append(t.ForeignKeys, db.ForeignKey{Name: r["name"], OnUpdate: fkAction[r["upd"]], OnDelete: fkAction[r["del"]],
				RefTable: db.ObjectRef{Schema: r["rschema"], Name: r["rtable"], Type: db.ObjTable}})
			fk = &t.ForeignKeys[len(t.ForeignKeys)-1]
		}
		fk.Columns = append(fk.Columns, r["col"])
		fk.RefColumns = append(fk.RefColumns, r["rcol"])
	}
	chk, err := sqlbase.Recs(ctx, c.db, `SELECT name, definition FROM sys.check_constraints WHERE parent_object_id = @p1 ORDER BY name`, id)
	if err != nil {
		return nil, mapError(err)
	}
	for _, r := range chk {
		t.Constraints = append(t.Constraints, db.Constraint{Name: r["name"], Type: "CHECK", Definition: r["definition"]})
	}
	trg, err := sqlbase.Recs(ctx, c.db, `
SELECT t.name, OBJECT_DEFINITION(t.object_id) AS def, t.is_instead_of_trigger,
       STUFF((SELECT ', ' + te.type_desc FROM sys.trigger_events te WHERE te.object_id = t.object_id FOR XML PATH('')), 1, 2, '') AS events
FROM sys.triggers t WHERE t.parent_id = @p1 ORDER BY t.name`, id)
	if err != nil {
		return nil, mapError(err)
	}
	for _, r := range trg {
		tm := "AFTER"
		if r.Bool("is_instead_of_trigger") {
			tm = "INSTEAD OF"
		}
		t.Triggers = append(t.Triggers, db.Trigger{Name: r["name"], Timing: tm, Events: r["events"], Definition: r["def"]})
	}
	if v, err := sqlbase.One(ctx, c.db, `SELECT CAST(value AS nvarchar(4000)) FROM sys.extended_properties WHERE major_id = @p1 AND minor_id = 0 AND class = 1 AND name = 'MS_Description'`, id); err == nil {
		t.Comment = v
	}
	if n, err := sqlbase.One(ctx, c.db, `SELECT SUM(rows) FROM sys.partitions WHERE object_id = @p1 AND index_id IN (0, 1)`, id); err == nil && n != "" {
		fmt.Sscan(n, &t.RowEstimate)
	}
	return t, nil
}

func (c *catalog) DDL(ctx context.Context, ref db.ObjectRef) (string, error) {
	switch ref.Type {
	case db.ObjView, db.ObjProcedure, db.ObjFunction:
		id, err := c.objectID(ctx, ref)
		if err != nil {
			return "", err
		}
		def, err := sqlbase.One(ctx, c.db, "SELECT OBJECT_DEFINITION(@p1)", id)
		if err != nil {
			return "", mapError(err)
		}
		if def == "" {
			return "", fmt.Errorf("the definition of %s is not visible (encrypted or missing VIEW DEFINITION permission)", ref)
		}
		return strings.TrimSpace(def) + "\nGO\n", nil
	case db.ObjSequence:
		recs, err := sqlbase.Recs(ctx, c.db, `
SELECT TYPE_NAME(user_type_id) AS type, CAST(start_value AS nvarchar(40)) AS start, CAST(increment AS nvarchar(40)) AS inc,
       CAST(minimum_value AS nvarchar(40)) AS minv, CAST(maximum_value AS nvarchar(40)) AS maxv, is_cycling
FROM sys.sequences WHERE object_id = OBJECT_ID(@p1)`, c.d.QuoteAlways(ref.Schema)+"."+c.d.QuoteAlways(ref.Name))
		if err != nil {
			return "", mapError(err)
		}
		if len(recs) == 0 {
			return "", fmt.Errorf("%s not found", ref)
		}
		r := recs[0]
		cyc := "NO CYCLE"
		if r.Bool("is_cycling") {
			cyc = "CYCLE"
		}
		return fmt.Sprintf("CREATE SEQUENCE %s AS %s START WITH %s INCREMENT BY %s MINVALUE %s MAXVALUE %s %s;\n",
			c.d.Qualified(ref), r["type"], r["start"], r["inc"], r["minv"], r["maxv"], cyc), nil
	}
	t, err := c.Table(ctx, ref)
	if err != nil {
		return "", err
	}
	q := c.d.Quote
	var defs []string
	for _, col := range t.Columns {
		if col.Generated != "" {
			defs = append(defs, "    "+q(col.Name)+" AS "+col.Generated)
			continue
		}
		s := "    " + q(col.Name) + " " + col.Type
		if !col.Nullable {
			s += " NOT NULL"
		} else {
			s += " NULL"
		}
		if col.HasDefault {
			s += " DEFAULT " + col.Default
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
	var after []string
	for _, ix := range t.Indexes {
		switch {
		case ix.Primary:
			defs = append(defs, fmt.Sprintf("    CONSTRAINT %s PRIMARY KEY %s (%s)", q(ix.Name), strings.ToUpper(ix.Method), list(ix.Columns)))
		case isConstraint(t, ix.Name):
			defs = append(defs, fmt.Sprintf("    CONSTRAINT %s UNIQUE %s (%s)", q(ix.Name), strings.ToUpper(ix.Method), list(ix.Columns)))
		default:
			u := ""
			if ix.Unique {
				u = "UNIQUE "
			}
			s := fmt.Sprintf("CREATE %s%s INDEX %s ON %s (%s)", u, strings.ToUpper(ix.Method), q(ix.Name), c.d.Qualified(ref), list(ix.Columns))
			if ix.Where != "" {
				s += " WHERE " + ix.Where
			}
			after = append(after, s+";")
		}
	}
	for _, ch := range t.Constraints {
		if ch.Type == "CHECK" {
			defs = append(defs, fmt.Sprintf("    CONSTRAINT %s CHECK %s", q(ch.Name), ch.Definition))
		}
	}
	for _, fk := range t.ForeignKeys {
		s := fmt.Sprintf("    CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s (%s)", q(fk.Name), list(fk.Columns), c.d.Qualified(fk.RefTable), list(fk.RefColumns))
		if fk.OnUpdate != "" && fk.OnUpdate != "NO ACTION" {
			s += " ON UPDATE " + fk.OnUpdate
		}
		if fk.OnDelete != "" && fk.OnDelete != "NO ACTION" {
			s += " ON DELETE " + fk.OnDelete
		}
		defs = append(defs, s)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE %s (\n%s\n);\n", c.d.Qualified(ref), strings.Join(defs, ",\n"))
	for _, s := range after {
		b.WriteString("\n" + s + "\n")
	}
	for _, tr := range t.Triggers {
		b.WriteString("GO\n" + strings.TrimSpace(tr.Definition) + "\nGO\n")
	}
	return b.String(), nil
}

func isConstraint(t *db.TableInfo, name string) bool {
	for _, c := range t.Constraints {
		if c.Type == "UNIQUE" && c.Name == name {
			return true
		}
	}
	return false
}

func (c *catalog) Completion(ctx context.Context, schema db.ObjectRef) (map[string][]string, error) {
	recs, err := sqlbase.Recs(ctx, c.db, `
SELECT o.name AS t, c.name AS c FROM sys.objects o JOIN sys.columns c ON c.object_id = o.object_id
WHERE o.schema_id = SCHEMA_ID(@p1) AND o.type IN ('U','V') ORDER BY o.name, c.column_id`, schema.Schema)
	if err != nil {
		return nil, mapError(err)
	}
	return sqlbase.GroupCompletion(recs, "t", "c"), nil
}
