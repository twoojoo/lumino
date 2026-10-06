// Package sqlite is the SQLite adapter (modernc.org/sqlite, no cgo).
package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	sqlite "modernc.org/sqlite"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/db/sqlbase"
	"github.com/giovanni/lumino/internal/db/sqlx"
)

func init() { db.Register(Driver{}) }

// Driver opens SQLite database files.
type Driver struct{}

var caps = db.Capabilities{
	Schemas: true, Transactions: true, ReadOnlyTx: true, Cancel: true, ForeignKeys: true, Triggers: true, Views: true,
	Insert: true, Update: true, Delete: true, SingleWriter: true,
}

func (Driver) Info() db.Info {
	return db.Info{ID: "sqlite", Name: "SQLite", DatabaseLabel: "File", Fields: []db.Field{db.FieldFile}, Caps: caps,
		Schemes: []string{"sqlite", "sqlite3", "file"}, DialectName: "sqlite"}
}

var reserved = db.WordSet(`
ABORT ACTION ADD AFTER ALL ALTER ALWAYS ANALYZE AND AS ASC ATTACH AUTOINCREMENT BEFORE BEGIN BETWEEN BY CASCADE CASE
CAST CHECK COLLATE COLUMN COMMIT CONFLICT CONSTRAINT CREATE CROSS CURRENT CURRENT_DATE CURRENT_TIME CURRENT_TIMESTAMP
DATABASE DEFAULT DEFERRABLE DEFERRED DELETE DESC DETACH DISTINCT DO DROP EACH ELSE END ESCAPE EXCEPT EXCLUDE EXCLUSIVE
EXISTS EXPLAIN FAIL FILTER FIRST FOLLOWING FOR FOREIGN FROM FULL GENERATED GLOB GROUP GROUPS HAVING IF IGNORE
IMMEDIATE IN INDEX INDEXED INITIALLY INNER INSERT INSTEAD INTERSECT INTO IS ISNULL JOIN KEY LAST LEFT LIKE LIMIT MATCH
MATERIALIZED NATURAL NO NOT NOTHING NOTNULL NULL NULLS OF OFFSET ON OR ORDER OTHERS OUTER OVER PARTITION PLAN PRAGMA
PRECEDING PRIMARY QUERY RAISE RANGE RECURSIVE REFERENCES REGEXP REINDEX RELEASE RENAME REPLACE RESTRICT RETURNING
RIGHT ROLLBACK ROW ROWS SAVEPOINT SELECT SET TABLE TEMP TEMPORARY THEN TIES TO TRANSACTION TRIGGER UNBOUNDED UNION
UNIQUE UPDATE USING VACUUM VALUES VIEW VIRTUAL WHEN WHERE WINDOW WITH WITHOUT
`)

var functions = append(db.CommonFunctions, strings.Fields(`
changes char date datetime glob hex ifnull iif instr julianday last_insert_rowid like likelihood printf quote
random randomblob sqlite_version strftime substr time total total_changes typeof unicode unixepoch zeroblob
group_concat json json_array json_extract json_object json_group_array json_group_object json_each json_tree
`)...)

var lexOptions = sqlx.Options{
	Backticks: true, Brackets: true,
	BlockStart: func(w []string) bool {
		if len(w) < 2 || w[0] != "CREATE" {
			return false
		}
		for _, x := range w[1:] {
			switch x {
			case "TRIGGER":
				return true
			case "TABLE", "VIEW", "INDEX":
				return false
			}
		}
		return false
	},
}

// Dialect is the SQLite dialect.
type Dialect struct{ db.BaseDialect }

// NewDialect builds the SQLite dialect.
func NewDialect() *Dialect {
	return &Dialect{db.BaseDialect{
		ID: "sqlite", QuoteL: `"`, QuoteR: `"`, Fold: db.FoldNone, Params: db.ParamQuestion, LexOpts: lexOptions,
		Reserved: reserved, KeywordSet: db.Merge(reserved, db.CommonKeywords), FuncList: functions,
		TypeList: strings.Fields("integer real text blob numeric boolean date datetime timestamp varchar decimal"),
		BoolTrue: "1", BoolFalse: "0",
	}}
}

// TimeText renders a time the way SQLite stores dates as text: date only
// for a midnight DATE, "YYYY-MM-DD HH:MM:SS[.fff]" otherwise, with the
// offset only when the value had one (the driver parses text without an
// offset as UTC). It reproduces the stored text of the usual formats, so
// edits neither rewrite untouched values nor miss rows keyed by a date.
func TimeText(t time.Time, k db.Kind) string {
	utc := t.Location() == time.UTC
	if k == db.KindDate && utc && t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 && t.Nanosecond() == 0 {
		return t.Format("2006-01-02")
	}
	s := t.Format("2006-01-02 15:04:05")
	if t.Nanosecond() != 0 {
		s += strings.TrimRight(t.Format(".000000000"), "0")
	}
	if !utc {
		s += t.Format("-07:00")
	}
	return s
}

// Literal renders times like TimeText.
func (d *Dialect) Literal(v any, k db.Kind) string {
	if t, ok := v.(time.Time); ok {
		return d.QuoteString(TimeText(t, k))
	}
	return d.BaseDialect.Literal(v, k)
}

// BuildChange binds times as text in SQLite's format (the driver would
// write "… +00:00" and a bound time never equals a stored date text).
// Time keys also match by instant (julianday) so rows stored in another
// text format ('T' separator, no seconds, 'Z') can still be edited; a
// key matching several rows fails the batch as usual.
func (d *Dialect) BuildChange(ref db.ObjectRef, ch db.Change, literal bool) (string, []any) {
	var args []any
	val := func(cv db.ColValue) string {
		if r, ok := cv.Value.(db.Raw); ok {
			return string(r)
		}
		if literal || cv.Value == nil {
			return d.Literal(cv.Value, cv.Kind)
		}
		v := cv.Value
		if t, ok := v.(time.Time); ok {
			v = TimeText(t, cv.Kind)
		}
		args = append(args, v)
		return "?"
	}
	table := d.Qualified(ref)
	where := func() string {
		conds := make([]string, len(ch.Key))
		for i, cv := range ch.Key {
			col := d.Quote(cv.Column)
			if cv.Value == nil {
				conds[i] = col + " IS NULL"
				continue
			}
			if _, ok := cv.Value.(time.Time); ok {
				conds[i] = "(" + col + " = " + val(cv) + " OR julianday(" + col + ") = julianday(" + val(cv) + "))"
				continue
			}
			conds[i] = col + " = " + val(cv)
		}
		return strings.Join(conds, " AND ")
	}
	switch ch.Op {
	case db.OpInsert:
		if len(ch.Values) == 0 {
			return "INSERT INTO " + table + " DEFAULT VALUES", nil
		}
		cols := make([]string, len(ch.Values))
		vals := make([]string, len(ch.Values))
		for i, cv := range ch.Values {
			cols[i], vals[i] = d.Quote(cv.Column), val(cv)
		}
		return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", table, strings.Join(cols, ", "), strings.Join(vals, ", ")), args
	case db.OpUpdate:
		sets := make([]string, len(ch.Values))
		for i, cv := range ch.Values {
			sets[i] = d.Quote(cv.Column) + " = " + val(cv)
		}
		set := strings.Join(sets, ", ")
		return fmt.Sprintf("UPDATE %s SET %s WHERE %s", table, set, where()), args
	}
	return fmt.Sprintf("DELETE FROM %s WHERE %s", table, where()), args
}

// KindOf applies SQLite's type affinity rules to a declared type.
func KindOf(t string) db.Kind {
	u := strings.ToUpper(t)
	switch {
	case u == "":
		return db.KindOther
	case strings.Contains(u, "INT"):
		return db.KindInt
	case strings.Contains(u, "CHAR") || strings.Contains(u, "CLOB") || strings.Contains(u, "TEXT"):
		if strings.Contains(u, "JSON") {
			return db.KindJSON
		}
		return db.KindText
	case strings.Contains(u, "BLOB"):
		return db.KindBytes
	case strings.Contains(u, "REAL") || strings.Contains(u, "FLOA") || strings.Contains(u, "DOUB"):
		return db.KindFloat
	case strings.HasPrefix(u, "BOOL"):
		return db.KindBool
	case u == "DATE":
		return db.KindDate
	case strings.HasPrefix(u, "DATETIME") || strings.HasPrefix(u, "TIMESTAMP"):
		return db.KindTimestamp
	case u == "TIME":
		return db.KindTime
	case strings.HasPrefix(u, "DECIMAL") || strings.HasPrefix(u, "NUMERIC"):
		return db.KindDecimal
	case u == "JSON":
		return db.KindJSON
	}
	return db.KindOther
}

func expand(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	return p
}

// fileURI builds the SQLite URI of a file path: the path is made absolute
// and percent-encoded, so '?', '#', '%' and the like name the file instead
// of starting the query, a fragment or an escape.
func fileURI(path string, q url.Values) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // C:/x → /C:/x
	}
	u := url.URL{Scheme: "file", Path: p, RawQuery: q.Encode()}
	// EscapedPath leaves some characters alone that are fine in URLs; make
	// sure none of SQLite's delimiters remains
	return "file://" + strings.NewReplacer("?", "%3F", "#", "%23").Replace(u.EscapedPath()) + "?" + u.RawQuery, nil
}

func (d Driver) Open(ctx context.Context, cfg *db.Config) (db.Conn, error) {
	path := strings.TrimSpace(cfg.File)
	if path == "" {
		return nil, errors.New("no database file")
	}
	memory := path == ":memory:"
	var err error
	if !memory {
		path = expand(path)
		if cfg.Param("create") != "true" {
			if _, err := os.Stat(path); err != nil {
				return nil, fmt.Errorf("database file %s: %w", path, err)
			}
		}
	}
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	if cfg.Param("mode") == "ro" {
		q.Set("mode", "ro")
	}
	q.Set("_time_format", "sqlite")
	var dsn string
	if memory {
		// a named memdb database is shared by every connection of the pool
		// (plain :memory: would give each connection its own database) and
		// uses normal locking, so busy_timeout applies
		var rnd [8]byte
		_, _ = rand.Read(rnd[:])
		q.Set("vfs", "memdb")
		dsn = "file:/lumino-mem-" + hex.EncodeToString(rnd[:]) + "?" + q.Encode()
	} else {
		if dsn, err = fileURI(path, q); err != nil {
			return nil, err
		}
	}
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	var keep *sql.Conn
	if memory {
		// the database lives while one connection is open: hold one for the
		// Conn's lifetime (idle connections are never expired)
		if keep, err = sdb.Conn(ctx); err != nil {
			sdb.Close()
			return nil, mapError(err)
		}
	} else {
		sdb.SetConnMaxIdleTime(10 * time.Minute)
	}
	if err := sdb.PingContext(ctx); err != nil {
		if keep != nil {
			keep.Close()
		}
		sdb.Close()
		return nil, mapError(err)
	}
	// a file that isn't a database fails only on the first read
	if _, err := sdb.ExecContext(ctx, "SELECT count(*) FROM sqlite_master"); err != nil {
		sdb.Close()
		return nil, mapError(err)
	}
	ver, _ := sqlbase.One(ctx, sdb, "SELECT sqlite_version()")
	dia := NewDialect()
	h := sqlbase.Hooks{
		Kind:     KindOf,
		Convert:  convert,
		MapError: mapError,
		ReturnsRows: func(stmt string) bool {
			// PRAGMA x = y returns nothing, PRAGMA x returns rows: query both
			return sqlx.ReturnsRows(stmt, &lexOptions)
		},
		CurrentSchema: func(ctx context.Context, q sqlbase.Querier) (string, error) { return "main", nil },
		SetReadOnly: func(ctx context.Context, q sqlbase.Querier, on bool) error {
			v := "0"
			if on {
				v = "1"
			}
			_, err := q.ExecContext(ctx, "PRAGMA query_only = "+v)
			return err
		},
	}
	c := &sqlbase.Conn{DB: sdb, Inf: d.Info(), Cap: caps, D: dia, Version: "SQLite " + ver, H: h, InitSQL: cfg.InitSQL}
	c.Cat = &catalog{db: sdb, d: dia}
	if keep != nil {
		c.OnClose = append(c.OnClose, func() { keep.Close() })
	}
	return c, nil
}

// convert keeps text in typed columns readable: values stored with another
// type than the declared one are returned as they are.
func convert(col *db.Column, v any) any {
	if b, ok := v.([]byte); ok && col.Kind != db.KindBytes && col.Kind != db.KindOther {
		return string(b)
	}
	return v
}

func mapError(err error) error {
	var se *sqlite.Error
	if errors.As(err, &se) {
		msg := se.Error()
		// "SQL logic error: no such column: x (1)" → "no such column: x"
		if i := strings.Index(msg, ": "); i >= 0 && strings.HasSuffix(msg, ")") {
			msg = msg[i+2:]
			if j := strings.LastIndex(msg, " ("); j > 0 {
				msg = msg[:j]
			}
		}
		code := sqlite.ErrorCodeString[se.Code()]
		return &db.Error{Msg: msg, Code: code, Err: err}
	}
	return err
}

type catalog struct {
	db *sql.DB
	d  *Dialect
}

func (c *catalog) Catalogs(ctx context.Context) ([]string, error) { return nil, db.ErrNotSupported }

func (c *catalog) Schemas(ctx context.Context, _ string) ([]db.Schema, error) {
	recs, err := sqlbase.Recs(ctx, c.db, "PRAGMA database_list")
	if err != nil {
		return nil, mapError(err)
	}
	var out []db.Schema
	for _, r := range recs {
		out = append(out, db.Schema{Name: r["name"], Current: r["name"] == "main", System: r["name"] == "temp"})
	}
	return out, nil
}

func (c *catalog) master(schema string) string {
	return c.d.QuoteAlways(schemaOr(schema)) + ".sqlite_master"
}

func schemaOr(s string) string {
	if s == "" {
		return "main"
	}
	return s
}

func (c *catalog) Objects(ctx context.Context, schema db.ObjectRef, types ...db.ObjectType) ([]db.Object, error) {
	// FTS5/R-Tree shadow tables (docs_content, docs_idx…) are internal: hide them.
	recs, err := sqlbase.Recs(ctx, c.db, "SELECT type, name FROM "+c.master(schema.Schema)+
		" m WHERE type IN ('table','view') AND name NOT LIKE 'sqlite\\_%' ESCAPE '\\'"+
		" AND NOT EXISTS (SELECT 1 FROM pragma_table_list p WHERE p.schema = ? AND p.name = m.name AND p.type = 'shadow')"+
		" ORDER BY name", schemaOr(schema.Schema))
	if err != nil { // pragma_table_list needs SQLite 3.37
		recs, err = sqlbase.Recs(ctx, c.db, "SELECT type, name FROM "+c.master(schema.Schema)+
			" WHERE type IN ('table','view') AND name NOT LIKE 'sqlite\\_%' ESCAPE '\\' ORDER BY name")
	}
	if err != nil {
		return nil, mapError(err)
	}
	var out []db.Object
	for _, r := range recs {
		t := db.ObjTable
		if r["type"] == "view" {
			t = db.ObjView
		}
		if sqlbase.Wants(types, t) {
			out = append(out, db.Object{Ref: db.ObjectRef{Schema: schemaOr(schema.Schema), Name: r["name"], Type: t}})
		}
	}
	return out, nil
}

func (c *catalog) pragma(ctx context.Context, name, schema, arg string) ([]sqlbase.Rec, error) {
	q := fmt.Sprintf("PRAGMA %s.%s(%s)", c.d.QuoteAlways(schemaOr(schema)), name, c.d.QuoteString(arg))
	recs, err := sqlbase.Recs(ctx, c.db, q)
	return recs, mapError(err)
}

func (c *catalog) Table(ctx context.Context, ref db.ObjectRef) (*db.TableInfo, error) {
	cols, err := c.pragma(ctx, "table_xinfo", ref.Schema, ref.Name)
	if err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("%s not found", ref)
	}
	sqlText, _ := sqlbase.One(ctx, c.db, "SELECT sql FROM "+c.master(ref.Schema)+" WHERE name = ? COLLATE NOCASE AND type IN ('table','view')", ref.Name)
	t := &db.TableInfo{Ref: ref, RowEstimate: -1}
	pk := map[int64]string{}
	for _, r := range cols {
		hidden := r.Int("hidden")
		if hidden == 1 { // hidden virtual-table column
			continue
		}
		def := r["dflt_value"]
		tc := db.TableColumn{Name: r["name"], Type: r["type"], Kind: KindOf(r["type"]), Nullable: !r.Bool("notnull"),
			Default: def, HasDefault: def != ""}
		if hidden == 2 || hidden == 3 {
			tc.Generated = generatedExpr(sqlText, tc.Name)
		}
		if n := r.Int("pk"); n > 0 {
			pk[n] = tc.Name
			tc.PrimaryKeyNo = int(n)
			// rowid alias candidate; confirmed below (no WITHOUT ROWID, no
			// "INTEGER PRIMARY KEY DESC" quirk)
			tc.AutoIncr = strings.EqualFold(tc.Type, "INTEGER")
		}
		t.Columns = append(t.Columns, tc)
	}
	for i := int64(1); i <= int64(len(pk)); i++ {
		t.PrimaryKey = append(t.PrimaryKey, pk[i])
	}
	idx, err := c.pragma(ctx, "index_list", ref.Schema, ref.Name)
	if err != nil {
		return nil, err
	}
	// Only a single INTEGER PRIMARY KEY aliases the rowid, and only when
	// SQLite did not build a separate primary key index for it (it does for
	// WITHOUT ROWID tables and for column-level INTEGER PRIMARY KEY DESC).
	rowidAlias := len(pk) == 1
	for _, r := range idx {
		if r["origin"] == "pk" {
			rowidAlias = false
		}
	}
	if !rowidAlias {
		for i := range t.Columns {
			t.Columns[i].AutoIncr = false
		}
	}
	for _, r := range idx {
		ix := db.Index{Name: r["name"], Unique: r.Bool("unique"), Primary: r["origin"] == "pk"}
		ic, err := c.pragma(ctx, "index_xinfo", ref.Schema, ix.Name)
		if err != nil {
			return nil, err
		}
		for _, x := range ic {
			if !x.Bool("key") {
				continue
			}
			name := x["name"]
			if name == "" {
				name = "(expression)"
			}
			ix.Columns = append(ix.Columns, name)
		}
		if r.Bool("partial") {
			isql, _ := sqlbase.One(ctx, c.db, "SELECT sql FROM "+c.master(ref.Schema)+" WHERE type = 'index' AND name = ? COLLATE NOCASE", ix.Name)
			ix.Where = indexWhere(isql)
		}
		if r["origin"] == "u" {
			t.Constraints = append(t.Constraints, db.Constraint{Name: ix.Name, Type: "UNIQUE", Columns: ix.Columns})
		}
		t.Indexes = append(t.Indexes, ix)
	}
	fks, err := c.pragma(ctx, "foreign_key_list", ref.Schema, ref.Name)
	if err != nil {
		return nil, err
	}
	var cur *db.ForeignKey
	curID := ""
	parentPK := map[string][]string{}
	for _, r := range fks {
		if cur == nil || r["id"] != curID {
			curID = r["id"]
			t.ForeignKeys = append(t.ForeignKeys, db.ForeignKey{RefTable: db.ObjectRef{Schema: schemaOr(ref.Schema), Name: r["table"], Type: db.ObjTable},
				OnUpdate: r["on_update"], OnDelete: r["on_delete"]})
			cur = &t.ForeignKeys[len(t.ForeignKeys)-1]
		}
		cur.Columns = append(cur.Columns, r["from"])
		to := r["to"]
		if to == "" { // REFERENCES parent: the parent's primary key, in order
			ppk, ok := parentPK[r["table"]]
			if !ok {
				ppk = c.primaryKey(ctx, ref.Schema, r["table"])
				parentPK[r["table"]] = ppk
			}
			if n := int(r.Int("seq")); n < len(ppk) {
				to = ppk[n]
			}
		}
		cur.RefColumns = append(cur.RefColumns, to)
	}
	for _, chk := range checkClauses(sqlText) {
		t.Constraints = append(t.Constraints, db.Constraint{Type: "CHECK", Definition: chk})
	}
	trg, err := sqlbase.Recs(ctx, c.db, "SELECT name, sql FROM "+c.master(ref.Schema)+" WHERE type = 'trigger' AND tbl_name = ? COLLATE NOCASE ORDER BY name", ref.Name)
	if err != nil {
		return nil, mapError(err)
	}
	for _, r := range trg {
		tr := db.Trigger{Name: r["name"], Definition: r["sql"]}
		tr.Timing, tr.Events = triggerTimingEvent(r["sql"])
		t.Triggers = append(t.Triggers, tr)
	}
	return t, nil
}

// primaryKey returns the primary key columns of a table, in key order.
func (c *catalog) primaryKey(ctx context.Context, schema, table string) []string {
	cols, err := c.pragma(ctx, "table_info", schema, table)
	if err != nil {
		return nil
	}
	pk := map[int64]string{}
	for _, r := range cols {
		if n := r.Int("pk"); n > 0 {
			pk[n] = r["name"]
		}
	}
	var out []string
	for i := int64(1); i <= int64(len(pk)); i++ {
		out = append(out, pk[i])
	}
	return out
}

// sigToks returns the tokens that are neither spaces nor comments.
func sigToks(sqlText string) []sqlx.Token {
	var out []sqlx.Token
	for _, t := range sqlx.Tokens(sqlText, &lexOptions) {
		if t.Kind != sqlx.TSpace && t.Kind != sqlx.TComment {
			out = append(out, t)
		}
	}
	return out
}

// indexWhere returns the WHERE predicate of a partial CREATE INDEX.
func indexWhere(sqlText string) string {
	depth := 0
	for _, t := range sigToks(sqlText) {
		s := sqlText[t.Start:t.End]
		switch {
		case t.Kind == sqlx.TPunct && s == "(":
			depth++
		case t.Kind == sqlx.TPunct && s == ")":
			depth--
		case t.Kind == sqlx.TWord && depth == 0 && strings.EqualFold(s, "WHERE"):
			return strings.TrimSpace(strings.TrimRight(strings.TrimSpace(sqlText[t.End:]), ";"))
		}
	}
	return ""
}

// triggerTimingEvent reads "CREATE [TEMP] TRIGGER [IF NOT EXISTS] name
// [BEFORE|AFTER|INSTEAD OF] INSERT|UPDATE|DELETE …".
func triggerTimingEvent(sqlText string) (timing, event string) {
	toks := sigToks(sqlText)
	word := func(i int) string {
		if i < len(toks) && toks[i].Kind == sqlx.TWord {
			return strings.ToUpper(sqlText[toks[i].Start:toks[i].End])
		}
		return ""
	}
	i := 0
	for i < len(toks) && word(i) != "TRIGGER" {
		i++
	}
	i++
	if word(i) == "IF" && word(i+1) == "NOT" && word(i+2) == "EXISTS" {
		i += 3
	}
	i++ // name
	for i+1 < len(toks) && toks[i].Kind == sqlx.TPunct && sqlText[toks[i].Start:toks[i].End] == "." {
		i += 2 // schema.name
	}
	switch word(i) {
	case "BEFORE", "AFTER":
		timing = word(i)
		i++
	case "INSTEAD":
		timing = "INSTEAD OF"
		i += 2
	default:
		timing = "BEFORE" // SQLite's default
	}
	switch w := word(i); w {
	case "INSERT", "UPDATE", "DELETE":
		event = w
	}
	return timing, event
}

// checkClauses extracts CHECK (…) clauses from a CREATE TABLE statement.
func checkClauses(sqlText string) []string {
	toks := sqlx.Tokens(sqlText, &lexOptions)
	var out []string
	for i, t := range toks {
		if t.Kind != sqlx.TWord || !strings.EqualFold(sqlText[t.Start:t.End], "CHECK") {
			continue
		}
		depth, start := 0, -1
		for _, u := range toks[i+1:] {
			if u.Kind != sqlx.TPunct {
				continue
			}
			switch sqlText[u.Start:u.End] {
			case "(":
				if depth == 0 {
					start = u.End
				}
				depth++
			case ")":
				depth--
				if depth == 0 && start >= 0 {
					out = append(out, strings.TrimSpace(sqlText[start:u.Start]))
				}
			}
			if depth == 0 && start >= 0 {
				break
			}
		}
	}
	return out
}

// generatedExpr finds "<col> … AS (expr)" in a CREATE TABLE statement.
func generatedExpr(sqlText, col string) string {
	toks := sqlx.Tokens(sqlText, &lexOptions)
	for i, t := range toks {
		name := strings.Trim(sqlText[t.Start:t.End], "\"`[]")
		if (t.Kind != sqlx.TWord && t.Kind != sqlx.TQuotedIdent) || !strings.EqualFold(name, col) {
			continue
		}
		depth := 0
		for j := i + 1; j < len(toks); j++ {
			u := toks[j]
			s := sqlText[u.Start:u.End]
			if u.Kind == sqlx.TPunct && s == "(" {
				depth++
			}
			if u.Kind == sqlx.TPunct && s == ")" {
				depth--
			}
			if u.Kind == sqlx.TPunct && s == "," && depth == 0 || depth < 0 {
				break
			}
			if u.Kind == sqlx.TWord && strings.EqualFold(s, "AS") && depth == 0 {
				// the parenthesized expression after AS
				d, start := 0, -1
				for _, v := range toks[j+1:] {
					if v.Kind != sqlx.TPunct {
						continue
					}
					switch sqlText[v.Start:v.End] {
					case "(":
						if d == 0 {
							start = v.End
						}
						d++
					case ")":
						d--
						if d == 0 && start >= 0 {
							return strings.TrimSpace(sqlText[start:v.Start])
						}
					}
				}
			}
		}
	}
	return "(generated)"
}

func (c *catalog) DDL(ctx context.Context, ref db.ObjectRef) (string, error) {
	main, err := sqlbase.One(ctx, c.db, "SELECT sql FROM "+c.master(ref.Schema)+" WHERE name = ? COLLATE NOCASE AND type IN ('table','view','index','trigger')", ref.Name)
	if err != nil {
		return "", mapError(err)
	}
	if main == "" {
		return "", fmt.Errorf("%s not found", ref)
	}
	var b strings.Builder
	b.WriteString(main + ";\n")
	if ref.Type == db.ObjTable || ref.Type == "" {
		recs, err := sqlbase.Recs(ctx, c.db, "SELECT sql FROM "+c.master(ref.Schema)+" WHERE tbl_name = ? COLLATE NOCASE AND type IN ('index','trigger') AND sql IS NOT NULL ORDER BY type, name", ref.Name)
		if err != nil {
			return "", mapError(err)
		}
		for _, r := range recs {
			b.WriteString("\n" + r["sql"] + ";\n")
		}
	}
	return b.String(), nil
}

func (c *catalog) Completion(ctx context.Context, schema db.ObjectRef) (map[string][]string, error) {
	s := schemaOr(schema.Schema)
	recs, err := sqlbase.Recs(ctx, c.db, "SELECT m.name AS t, p.name AS c FROM "+c.master(s)+
		" m JOIN pragma_table_info(m.name, ?) p WHERE m.type IN ('table','view') ORDER BY m.name, p.cid", s)
	if err != nil {
		return nil, mapError(err)
	}
	return sqlbase.GroupCompletion(recs, "t", "c"), nil
}
