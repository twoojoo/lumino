package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/twoojoo/lumino/internal/db"
)

const maxToolOutput = 14000

// Proposal is a SQL statement suggested by the model: it is never run by
// the agent, only shown to the user, who can insert it in a console.
type Proposal struct {
	SQL    string `json:"sql"`
	Reason string `json:"reason,omitempty"`
}

// Tools runs the assistant's read-only database tools.
type Tools struct {
	Conn      db.Conn
	Schema    string // current schema of the UI (default for names)
	OnPropose func(*Proposal)
	// AllowQueries lets the model run read-only queries (user setting).
	AllowQueries bool
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

// Specs lists the tools exposed to the model.
func (t *Tools) Specs() []ToolSpec {
	mk := func(name, desc string, props map[string]any, req ...string) ToolSpec {
		return ToolSpec{Name: name, Description: desc, Properties: props, Required: req}
	}
	schema := strProp("Schema (database, keyspace…); omit for the current one")
	lang := "SQL"
	if t.Conn != nil {
		lang = t.Conn.Language().Display()
	}
	specs := []ToolSpec{
		mk("list_schemas", "List the schemas of the database.", map[string]any{}),
		mk("list_objects", "List tables, views, functions and other objects of a schema.", map[string]any{"schema": schema}),
		mk("describe_table", "Columns (type, nullability, default), primary key, indexes, foreign keys and constraints of a table or view.",
			map[string]any{"schema": schema, "table": strProp("Table or view name")}, "table"),
		mk("get_ddl", "The CREATE statement of an object.",
			map[string]any{"schema": schema, "name": strProp("Object name"), "type": strProp("table, view, function, procedure, sequence… (default table)")}, "name"),
		mk("sample_rows", "A few rows of a table (sensitive columns are redacted).",
			map[string]any{"schema": schema, "table": strProp("Table or view name"), "limit": intProp("Rows, at most 20 (default 5)")}, "table"),
		mk("propose_sql", "Propose a "+lang+" statement to the user (shown with an Insert button). Use it for any statement that changes data or schema, and for queries the user should run.",
			map[string]any{"sql": strProp("The " + lang + " statement(s)"), "reason": strProp("Short explanation")}, "sql"),
	}
	if t.AllowQueries {
		specs = append(specs,
			mk("run_query", "Run a read-only query (SELECT/SHOW/EXPLAIN) and get at most 50 rows. Statements that change anything are refused.",
				map[string]any{"sql": strProp("A single read-only statement")}, "sql"),
			mk("explain", "Get the execution plan of a query.", map[string]any{"sql": strProp("The query")}, "sql"))
	}
	return specs
}

type toolArgs struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Limit  int    `json:"limit"`
	SQL    string `json:"sql"`
	Reason string `json:"reason"`
}

func clip(s string) string {
	if len(s) > maxToolOutput {
		return s[:maxToolOutput] + "\n…(truncated)"
	}
	return s
}

func (t *Tools) schema(a string) string {
	if a != "" {
		return a
	}
	return t.Schema
}

// findObject resolves a name case-insensitively in a schema.
func (t *Tools) findObject(ctx context.Context, schema, name string) (db.ObjectRef, error) {
	objs, err := t.Conn.Catalog().Objects(ctx, db.ObjectRef{Schema: schema})
	if err != nil {
		return db.ObjectRef{}, err
	}
	for _, o := range objs {
		if o.Ref.Name == name {
			return o.Ref, nil
		}
	}
	for _, o := range objs {
		if strings.EqualFold(o.Ref.Name, name) {
			return o.Ref, nil
		}
	}
	return db.ObjectRef{}, fmt.Errorf("no object %q in schema %q (call list_objects)", name, schema)
}

// Run executes a tool call.
func (t *Tools) Run(ctx context.Context, call ToolCall) (string, error) {
	if t.Conn == nil {
		return "", errors.New("not connected to a database")
	}
	var a toolArgs
	if len(call.Args) > 0 {
		if err := json.Unmarshal(call.Args, &a); err != nil {
			return "", fmt.Errorf("invalid arguments: %v", err)
		}
	}
	cat := t.Conn.Catalog()
	lang := t.Conn.Language()
	switch call.Name {
	case "list_schemas":
		ss, err := cat.Schemas(ctx, "")
		if errors.Is(err, db.ErrNotSupported) {
			return "This database has no schemas: objects live directly in the database.", nil
		}
		if err != nil {
			return "", err
		}
		var b strings.Builder
		for _, s := range ss {
			b.WriteString(s.Name)
			if s.System {
				b.WriteString(" (system)")
			}
			if s.Current {
				b.WriteString(" (current)")
			}
			b.WriteByte('\n')
		}
		return clip(b.String()), nil
	case "list_objects":
		objs, err := cat.Objects(ctx, db.ObjectRef{Schema: t.schema(a.Schema)})
		if err != nil {
			return "", err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d objects in %s:\n", len(objs), t.schema(a.Schema))
		for i, o := range objs {
			if i == 500 {
				fmt.Fprintf(&b, "…%d more\n", len(objs)-i)
				break
			}
			fmt.Fprintf(&b, "%s %s%s", o.Ref.Type, o.Ref.Name, o.Ref.Signature)
			if o.Comment != "" {
				fmt.Fprintf(&b, " — %s", o.Comment)
			}
			b.WriteByte('\n')
		}
		return clip(b.String()), nil
	case "describe_table":
		ref, err := t.findObject(ctx, t.schema(a.Schema), a.Table)
		if err != nil {
			return "", err
		}
		info, err := cat.Table(ctx, ref)
		if err != nil {
			return "", err
		}
		return clip(describeTable(info)), nil
	case "get_ddl":
		ref, err := t.findObject(ctx, t.schema(a.Schema), a.Name)
		if err != nil {
			return "", err
		}
		s, err := cat.DDL(ctx, ref)
		if err != nil {
			return "", err
		}
		return clip(RedactText(s)), nil
	case "sample_rows":
		ref, err := t.findObject(ctx, t.schema(a.Schema), a.Table)
		if err != nil {
			return "", err
		}
		n := a.Limit
		if n <= 0 {
			n = 5
		}
		n = min(n, 20)
		q, err := lang.Browse(ref, db.BrowseQuery{})
		if err != nil {
			return "", err
		}
		return t.query(ctx, q, n)
	case "run_query":
		if !t.AllowQueries {
			return "", errors.New("running queries is disabled in the assistant settings")
		}
		return t.query(ctx, a.SQL, 50)
	case "explain":
		if !t.AllowQueries {
			return "", errors.New("running queries is disabled in the assistant settings")
		}
		q, err := lang.Explain(a.SQL)
		if errors.Is(err, db.ErrNotSupported) {
			return "", fmt.Errorf("explain is not available for %s from the assistant", lang.Name())
		}
		if err != nil {
			return "", err
		}
		return t.query(ctx, q, 200)
	case "propose_sql":
		if strings.TrimSpace(a.SQL) == "" {
			return "", errors.New("sql is required")
		}
		p := Proposal{SQL: strings.TrimSpace(a.SQL), Reason: a.Reason}
		if t.OnPropose != nil {
			t.OnPropose(&p)
		}
		return "Shown to the user, who decides whether to use it. Don't claim it was executed.", nil
	}
	return "", fmt.Errorf("unknown tool %q", call.Name)
}

// query runs one read-only statement on a read-only session. Several
// layers guard it: the statement must split to exactly one read statement,
// the session is set read-only by the server (or, where that is not
// available, the statement runs in a transaction rolled back afterwards).
func (t *Tools) query(ctx context.Context, sql string, limit int) (string, error) {
	lang := t.Conn.Language()
	// a script (variables, loops…) runs whole on a session that refuses
	// every write it attempts (checked operation by operation)
	dynamic := db.IsDynamic(lang, strings.TrimSpace(sql))
	if !dynamic {
		stmts := lang.Split(sql)
		if len(stmts) != 1 {
			return "", fmt.Errorf("refused: run exactly one statement (got %d)", len(stmts))
		}
		sql = strings.TrimSuffix(strings.TrimSpace(stmts[0].Text), ";")
		if k := lang.Classify(sql); k != db.StmtRead {
			return "", fmt.Errorf("refused: this is a %s statement; only read-only queries can run (use propose_sql)", k)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s, err := t.Conn.Session(ctx)
	if err != nil {
		return "", err
	}
	defer s.Close()
	if g, ok := s.(db.Guarded); ok {
		g.SetGuard(func(_ context.Context, k db.StmtKind, desc string) error {
			if k.Modifies() {
				return fmt.Errorf("refused: %s is a %s operation; only reads can run (use propose_sql)", desc, k)
			}
			return nil
		})
	} else if dynamic {
		return "", errors.New("refused: scripts can't be checked on this database")
	}
	if err := s.SetReadOnly(ctx, true); err != nil {
		if dynamic {
			return "", fmt.Errorf("refused: can't make the session read-only: %w", err)
		}
		if !errors.Is(err, db.ErrNotSupported) {
			return "", fmt.Errorf("refused: can't make the session read-only: %w", err)
		}
		if t.Conn.Caps().Transactions {
			if err := s.Begin(ctx); err != nil {
				return "", fmt.Errorf("refused: can't open a transaction to roll back: %w", err)
			}
			defer s.Rollback(context.Background())
		}
	}
	if t.Schema != "" {
		_ = s.SetCurrentSchema(ctx, t.Schema)
	}
	res, err := s.Execute(ctx, sql)
	if err != nil {
		return "", err
	}
	defer res.Close()
	cols := res.Columns()
	if cols == nil {
		return "OK (no rows)", nil
	}
	rows, more, err := res.Fetch(ctx, limit)
	if err != nil {
		return "", err
	}
	// columns can be renamed (SELECT password AS x): a query reading a
	// sensitive column gets all its values redacted
	all := false
	// only names used as columns count: not tables (FROM auth_user) nor
	// qualifiers (auth_user.id)
	for _, name := range lang.ColumnNames(sql) {
		if SensitiveColumn(name) {
			all = true
			break
		}
	}
	var b strings.Builder
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Name
	}
	b.WriteString(strings.Join(names, "\t") + "\n")
	for _, r := range rows {
		cells := make([]string, len(r))
		for i, v := range r {
			switch {
			case v == nil:
				cells[i] = "NULL"
			case all || SensitiveColumn(cols[i].Name):
				cells[i] = redacted
			default:
				cells[i] = RedactText(db.CellText(v, cols[i].Kind))
				if len(cells[i]) > 200 {
					cells[i] = cells[i][:200] + "…"
				}
			}
		}
		b.WriteString(strings.Join(cells, "\t") + "\n")
	}
	if more {
		fmt.Fprintf(&b, "…more rows not shown (limit %d)\n", limit)
	}
	if all {
		b.WriteString("(values redacted: the query reads columns that look sensitive)\n")
	}
	return clip(RedactText(b.String())), nil
}

func describeTable(t *db.TableInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s", t.Ref.Type, t.Ref.String())
	if t.Comment != "" {
		fmt.Fprintf(&b, " — %s", t.Comment)
	}
	b.WriteString("\ncolumns:\n")
	for _, c := range t.Columns {
		fmt.Fprintf(&b, "- %s %s", c.Name, c.Type)
		if !c.Nullable {
			b.WriteString(" NOT NULL")
		}
		if c.Default != "" {
			fmt.Fprintf(&b, " DEFAULT %s", c.Default)
		}
		if c.Generated != "" {
			fmt.Fprintf(&b, " GENERATED (%s)", c.Generated)
		}
		if c.AutoIncr {
			b.WriteString(" auto-increment")
		}
		if c.Comment != "" {
			fmt.Fprintf(&b, " — %s", c.Comment)
		}
		b.WriteByte('\n')
	}
	if len(t.PrimaryKey) > 0 {
		fmt.Fprintf(&b, "primary key: (%s)\n", strings.Join(t.PrimaryKey, ", "))
	}
	for _, ix := range t.Indexes {
		if ix.Primary {
			continue
		}
		u := ""
		if ix.Unique {
			u = "unique "
		}
		fmt.Fprintf(&b, "%sindex %s (%s)\n", u, ix.Name, strings.Join(ix.Columns, ", "))
	}
	for _, fk := range t.ForeignKeys {
		fmt.Fprintf(&b, "foreign key %s (%s) → %s (%s)\n", fk.Name, strings.Join(fk.Columns, ", "), fk.RefTable.String(), strings.Join(fk.RefColumns, ", "))
	}
	for _, c := range t.Constraints {
		fmt.Fprintf(&b, "%s constraint %s %s\n", strings.ToLower(c.Type), c.Name, c.Definition)
	}
	if t.RowEstimate >= 0 {
		fmt.Fprintf(&b, "estimated rows: %d\n", t.RowEstimate)
	}
	return b.String()
}
