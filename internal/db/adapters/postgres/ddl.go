package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/db/sqlbase"
)

// sequence reads the options of a sequence.
func (c *Catalog) sequence(ctx context.Context, ref db.ObjectRef) (sqlbase.Rec, error) {
	recs, err := sqlbase.Recs(ctx, c.DB, `
SELECT data_type, start_value, min_value, max_value, increment_by, cycle, cache_size
FROM pg_catalog.pg_sequences WHERE schemaname = $1 AND sequencename = $2`, ref.Schema, ref.Name)
	if err != nil {
		return nil, MapError(err)
	}
	if len(recs) == 0 {
		return nil, fmt.Errorf("%s not found", ref)
	}
	return recs[0], nil
}

// identityOptions renders the sequence options of an identity column that
// differ from the defaults.
func identityOptions(r sqlbase.Rec) string {
	maxv := map[string]string{"smallint": "32767", "integer": "2147483647", "bigint": "9223372036854775807"}[r["data_type"]]
	minv := map[string]string{"smallint": "-32768", "integer": "-2147483648", "bigint": "-9223372036854775808"}[r["data_type"]]
	inc := r["increment_by"]
	defMin, defMax, defStart := "1", maxv, "1"
	if strings.HasPrefix(inc, "-") {
		defMin, defMax, defStart = minv, "-1", "-1"
	}
	var o []string
	if r["start_value"] != defStart {
		o = append(o, "START WITH "+r["start_value"])
	}
	if inc != "1" {
		o = append(o, "INCREMENT BY "+inc)
	}
	if r["min_value"] != defMin {
		o = append(o, "MINVALUE "+r["min_value"])
	}
	if defMax != "" && r["max_value"] != defMax {
		o = append(o, "MAXVALUE "+r["max_value"])
	}
	if r["cache_size"] != "1" && r["cache_size"] != "" {
		o = append(o, "CACHE "+r["cache_size"])
	}
	if r.Bool("cycle") {
		o = append(o, "CYCLE")
	}
	if len(o) == 0 {
		return ""
	}
	return " (" + strings.Join(o, " ") + ")"
}

// fdwOptions renders "key=value" option arrays (aggregated with \x1f) as an
// OPTIONS clause.
func (c *Catalog) fdwOptions(list string) string {
	if list == "" {
		return ""
	}
	var o []string
	for _, kv := range strings.Split(list, "\x1f") {
		k, v, _ := strings.Cut(kv, "=")
		o = append(o, c.D.Quote(k)+" "+c.D.QuoteString(v))
	}
	return " OPTIONS (" + strings.Join(o, ", ") + ")"
}

type ownedSeq struct {
	col, deptype string
	ref          db.ObjectRef
}

func (c *Catalog) tableDDL(ctx context.Context, ref db.ObjectRef) (string, error) {
	oid, err := c.oid(ctx, ref)
	if err != nil {
		return "", err
	}
	t, err := c.Table(ctx, ref)
	if err != nil {
		return "", err
	}
	rels, err := sqlbase.Recs(ctx, c.DB, `
SELECT c.relkind AS kind, c.relispartition AS ispart,
       CASE WHEN c.relkind = 'p' THEN pg_catalog.pg_get_partkeydef(c.oid) ELSE '' END AS partkey,
       CASE WHEN c.relispartition THEN pg_catalog.pg_get_expr(c.relpartbound, c.oid) ELSE '' END AS bound,
       COALESCE((SELECT pn.nspname || E'\x1f' || pc.relname FROM pg_catalog.pg_inherits i
                 JOIN pg_catalog.pg_class pc ON pc.oid = i.inhparent JOIN pg_catalog.pg_namespace pn ON pn.oid = pc.relnamespace
                 WHERE i.inhrelid = c.oid AND c.relispartition), '') AS parent,
       COALESCE((SELECT s.srvname FROM pg_catalog.pg_foreign_table ft JOIN pg_catalog.pg_foreign_server s ON s.oid = ft.ftserver
                 WHERE ft.ftrelid = c.oid), '') AS server,
       COALESCE((SELECT array_to_string(ft.ftoptions, E'\x1f') FROM pg_catalog.pg_foreign_table ft WHERE ft.ftrelid = c.oid), '') AS ftopts
FROM pg_catalog.pg_class c WHERE c.oid = $1`, oid)
	if err != nil {
		return "", MapError(err)
	}
	if len(rels) != 1 {
		return "", fmt.Errorf("%s not found", ref)
	}
	rel := rels[0]
	foreign := rel["kind"] == "f"

	// sequences owned by columns: serial ('a') and identity ('i')
	seqRecs, err := sqlbase.Recs(ctx, c.DB, `
SELECT a.attname AS col, d.deptype AS dt, sn.nspname AS sschema, s.relname AS sname
FROM pg_catalog.pg_depend d
JOIN pg_catalog.pg_class s ON s.oid = d.objid AND s.relkind = 'S'
JOIN pg_catalog.pg_namespace sn ON sn.oid = s.relnamespace
JOIN pg_catalog.pg_attribute a ON a.attrelid = d.refobjid AND a.attnum = d.refobjsubid
WHERE d.classid = 'pg_catalog.pg_class'::regclass AND d.refclassid = 'pg_catalog.pg_class'::regclass
  AND d.refobjid = $1 AND d.deptype IN ('a','i')
ORDER BY a.attnum`, oid)
	if err != nil {
		return "", MapError(err)
	}
	var owned []ownedSeq
	identSeq := map[string]db.ObjectRef{}
	for _, r := range seqRecs {
		s := ownedSeq{col: r["col"], deptype: r["dt"], ref: db.ObjectRef{Schema: r["sschema"], Name: r["sname"], Type: db.ObjSequence}}
		if s.deptype == "i" {
			identSeq[s.col] = s.ref
		} else {
			owned = append(owned, s)
		}
	}
	extra, err := sqlbase.Recs(ctx, c.DB, `
SELECT a.attname AS name, a.attidentity AS ident,
       COALESCE(array_to_string(a.attfdwoptions, E'\x1f'), '') AS fdwopts
FROM pg_catalog.pg_attribute a WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped`, oid)
	if err != nil {
		return "", MapError(err)
	}
	identKind, fdwOpts := map[string]string{}, map[string]string{}
	for _, r := range extra {
		identKind[r["name"]], fdwOpts[r["name"]] = r["ident"], r["fdwopts"]
	}

	var b strings.Builder
	for _, s := range owned {
		sd, err := c.DDL(ctx, s.ref)
		if err != nil {
			return "", err
		}
		b.WriteString(sd + "\n")
	}

	if rel.Bool("ispart") && rel["parent"] != "" {
		ps, pn, _ := strings.Cut(rel["parent"], "\x1f")
		fmt.Fprintf(&b, "CREATE TABLE %s PARTITION OF %s\n    %s", c.D.Qualified(ref),
			c.D.Qualified(db.ObjectRef{Schema: ps, Name: pn, Type: db.ObjTable}), rel["bound"])
		if rel["partkey"] != "" {
			b.WriteString("\n    PARTITION BY " + rel["partkey"])
		}
		b.WriteString(";\n")
	} else {
		var defs []string
		for _, col := range t.Columns {
			s := "    " + c.D.Quote(col.Name) + " " + col.Type
			if foreign {
				s += c.fdwOptions(fdwOpts[col.Name])
			}
			switch ik := identKind[col.Name]; {
			case col.Generated != "":
				s += " GENERATED ALWAYS AS (" + col.Generated + ") STORED"
			case ik == "a" || ik == "d":
				kw := "BY DEFAULT"
				if ik == "a" {
					kw = "ALWAYS"
				}
				s += " GENERATED " + kw + " AS IDENTITY"
				if sref, ok := identSeq[col.Name]; ok {
					if r, err := c.sequence(ctx, sref); err == nil {
						s += identityOptions(r)
					}
				}
			case col.AutoIncr && !strings.HasPrefix(col.Default, "nextval("):
				s += " GENERATED BY DEFAULT AS IDENTITY"
			case col.HasDefault:
				s += " DEFAULT " + col.Default
			}
			if !col.Nullable {
				s += " NOT NULL"
			}
			defs = append(defs, s)
		}
		cons, err := sqlbase.Recs(ctx, c.DB, `
SELECT con.conname AS name, pg_catalog.pg_get_constraintdef(con.oid, true) AS def
FROM pg_catalog.pg_constraint con WHERE con.conrelid = $1 AND con.contype IN ('p','u','c','f','x')
ORDER BY CASE con.contype WHEN 'p' THEN 0 WHEN 'u' THEN 1 WHEN 'c' THEN 2 WHEN 'x' THEN 3 ELSE 4 END, con.conname`, oid)
		if err != nil {
			return "", MapError(err)
		}
		for _, r := range cons {
			defs = append(defs, "    CONSTRAINT "+c.D.Quote(r["name"])+" "+r["def"])
		}
		kw := "CREATE TABLE"
		if foreign {
			kw = "CREATE FOREIGN TABLE"
		}
		fmt.Fprintf(&b, "%s %s (\n%s\n)", kw, c.D.Qualified(ref), strings.Join(defs, ",\n"))
		if rel["partkey"] != "" {
			b.WriteString("\nPARTITION BY " + rel["partkey"])
		}
		if foreign {
			b.WriteString("\nSERVER " + c.D.Quote(rel["server"]) + c.fdwOptions(rel["ftopts"]))
		}
		b.WriteString(";\n")
	}

	idx, err := sqlbase.Recs(ctx, c.DB, `
SELECT pg_catalog.pg_get_indexdef(i.indexrelid) AS def FROM pg_catalog.pg_index i
WHERE i.indrelid = $1 AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_constraint con WHERE con.conindid = i.indexrelid AND con.conrelid = i.indrelid)
  AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_inherits ih WHERE ih.inhrelid = i.indexrelid)
ORDER BY 1`, oid)
	if err != nil {
		return "", MapError(err)
	}
	for _, r := range idx {
		// indexes of partitioned tables: create them on the partitions too
		def := strings.Replace(r["def"], " ON ONLY ", " ON ", 1)
		b.WriteString("\n" + def + ";\n")
	}
	for _, tr := range t.Triggers {
		b.WriteString("\n" + tr.Definition + ";\n")
	}
	for _, s := range owned {
		fmt.Fprintf(&b, "\nALTER SEQUENCE %s OWNED BY %s.%s;\n", c.D.Qualified(s.ref), c.D.Qualified(ref), c.D.Quote(s.col))
	}
	if t.Comment != "" {
		fmt.Fprintf(&b, "\nCOMMENT ON %s %s IS %s;\n", map[bool]string{true: "FOREIGN TABLE", false: "TABLE"}[foreign], c.D.Qualified(ref), c.D.QuoteString(t.Comment))
	}
	for _, col := range t.Columns {
		if col.Comment != "" {
			fmt.Fprintf(&b, "COMMENT ON COLUMN %s.%s IS %s;\n", c.D.Qualified(ref), c.D.Quote(col.Name), c.D.QuoteString(col.Comment))
		}
	}
	if rel["kind"] == "p" {
		if err := c.partitionsDDL(ctx, &b, oid, 0); err != nil {
			return "", err
		}
	}
	return b.String(), nil
}

// partitionsDDL appends the partitions of a partitioned table (recursively).
func (c *Catalog) partitionsDDL(ctx context.Context, b *strings.Builder, parent string, depth int) error {
	if depth > 8 {
		return nil
	}
	recs, err := sqlbase.Recs(ctx, c.DB, `
SELECT c.oid AS oid, n.nspname AS schema, c.relname AS name, pg_catalog.pg_get_expr(c.relpartbound, c.oid) AS bound,
       c.relkind AS kind, CASE WHEN c.relkind = 'p' THEN pg_catalog.pg_get_partkeydef(c.oid) ELSE '' END AS partkey,
       pn.nspname AS pschema, pc.relname AS pname
FROM pg_catalog.pg_inherits i
JOIN pg_catalog.pg_class c ON c.oid = i.inhrelid JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
JOIN pg_catalog.pg_class pc ON pc.oid = i.inhparent JOIN pg_catalog.pg_namespace pn ON pn.oid = pc.relnamespace
WHERE i.inhparent = $1 AND c.relispartition
ORDER BY c.relname`, parent)
	if err != nil {
		return MapError(err)
	}
	for _, r := range recs {
		fmt.Fprintf(b, "\nCREATE TABLE %s PARTITION OF %s\n    %s",
			c.D.Qualified(db.ObjectRef{Schema: r["schema"], Name: r["name"], Type: db.ObjTable}),
			c.D.Qualified(db.ObjectRef{Schema: r["pschema"], Name: r["pname"], Type: db.ObjTable}), r["bound"])
		if r["partkey"] != "" {
			b.WriteString("\n    PARTITION BY " + r["partkey"])
		}
		b.WriteString(";\n")
		if r["kind"] == "p" {
			if err := c.partitionsDDL(ctx, b, r["oid"], depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}
