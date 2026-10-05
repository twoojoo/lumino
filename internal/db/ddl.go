package db

import (
	"fmt"
	"strings"
)

// DDLOptions tune GenerateTableDDL.
type DDLOptions struct {
	// IndexDDL renders a CREATE INDEX; nil uses the standard form.
	IndexDDL func(table string, ix Index) string
	// SkipIndex drops indexes backing constraints (they're created by them).
	SkipIndex func(ix Index) bool
	// ColumnExtra renders driver specific column clauses after the type.
	ColumnExtra func(c TableColumn) string
	// Suffix is appended after the closing parenthesis (ENGINE=…).
	Suffix string
	// NoInlineFK skips FOREIGN KEY clauses (SQLite keeps them, others too).
	NoInlineFK bool
	// Terminator ends each statement (default ";").
	Terminator string
}

// GenerateTableDDL renders CREATE TABLE (+ CREATE INDEX) from a table
// definition, for databases without a server-side "SHOW CREATE".
func GenerateTableDDL(d Dialect, t *TableInfo, o DDLOptions) string {
	term := o.Terminator
	if term == "" {
		term = ";"
	}
	name := d.Qualified(t.Ref)
	var defs []string
	for _, c := range t.Columns {
		def := "    " + d.Quote(c.Name) + " " + c.Type
		if o.ColumnExtra != nil {
			if x := o.ColumnExtra(c); x != "" {
				def += " " + x
			}
		}
		if c.Generated != "" {
			def += " GENERATED ALWAYS AS (" + c.Generated + ")"
		}
		if c.HasDefault && c.Default != "" && c.Generated == "" {
			def += " DEFAULT " + c.Default
		}
		if !c.Nullable {
			def += " NOT NULL"
		}
		defs = append(defs, def)
	}
	quoteList := func(cols []string) string {
		q := make([]string, len(cols))
		for i, c := range cols {
			q[i] = d.Quote(c)
		}
		return strings.Join(q, ", ")
	}
	if len(t.PrimaryKey) > 0 {
		defs = append(defs, "    PRIMARY KEY ("+quoteList(t.PrimaryKey)+")")
	}
	for _, c := range t.Constraints {
		prefix := "    "
		if c.Name != "" {
			prefix += "CONSTRAINT " + d.Quote(c.Name) + " "
		}
		switch strings.ToUpper(c.Type) {
		case "UNIQUE":
			defs = append(defs, prefix+"UNIQUE ("+quoteList(c.Columns)+")")
		case "CHECK":
			def := c.Definition
			if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(def)), "CHECK") {
				def = "CHECK (" + def + ")"
			}
			defs = append(defs, prefix+def)
		default:
			if c.Definition != "" {
				defs = append(defs, prefix+c.Definition)
			}
		}
	}
	if !o.NoInlineFK {
		for _, fk := range t.ForeignKeys {
			def := "    "
			if fk.Name != "" {
				def += "CONSTRAINT " + d.Quote(fk.Name) + " "
			}
			def += fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s (%s)", quoteList(fk.Columns), d.Qualified(fk.RefTable), quoteList(fk.RefColumns))
			if r := strings.ToUpper(fk.OnUpdate); r != "" && r != "NO ACTION" {
				def += " ON UPDATE " + r
			}
			if r := strings.ToUpper(fk.OnDelete); r != "" && r != "NO ACTION" {
				def += " ON DELETE " + r
			}
			defs = append(defs, def)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE %s (\n%s\n)", name, strings.Join(defs, ",\n"))
	if o.Suffix != "" {
		b.WriteString(" " + o.Suffix)
	}
	b.WriteString(term + "\n")
	for _, ix := range t.Indexes {
		if ix.Primary || o.SkipIndex != nil && o.SkipIndex(ix) {
			continue
		}
		b.WriteString("\n")
		if o.IndexDDL != nil {
			b.WriteString(o.IndexDDL(name, ix))
		} else {
			b.WriteString(StandardIndexDDL(d, name, ix))
		}
		b.WriteString(term + "\n")
	}
	return b.String()
}

// StandardIndexDDL renders CREATE [UNIQUE] INDEX.
func StandardIndexDDL(d Dialect, table string, ix Index) string {
	cols := make([]string, len(ix.Columns))
	for i, c := range ix.Columns {
		if isPlainIdent(c) {
			cols[i] = d.Quote(c)
		} else {
			cols[i] = c // expression
		}
	}
	u := ""
	if ix.Unique {
		u = "UNIQUE "
	}
	s := fmt.Sprintf("CREATE %sINDEX %s ON %s (%s)", u, d.Quote(ix.Name), table, strings.Join(cols, ", "))
	if ix.Where != "" {
		s += " WHERE " + ix.Where
	}
	return s
}

func isPlainIdent(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80) {
			return false
		}
	}
	return true
}
