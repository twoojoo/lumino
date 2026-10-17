// Package dbtest is the conformance suite every adapter must pass against
// a real server (scripts/dbs/compose.yml). Each target loads the same
// logical fixture written in its own dialect and runs the same checks.
package dbtest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/twoojoo/lumino/internal/db"
)

// Target is one database under test.
type Target struct {
	Name    string
	Config  db.Config
	Fixture string // SQL script creating the test objects (idempotent)
	// Loader creates the test objects instead of Fixture (databases whose
	// language has no scripts of DDL statements).
	Loader func(ctx context.Context, c db.Conn) error
	// Schema holding the fixture ("" for databases without schemas).
	Schema  string
	Catalog string
	// BigRows is the row count of the "big" table.
	BigRows int
	// Sleep is a statement that runs for ~30s (cancel test); "" skips.
	Sleep string
	// Count returns rows of a table: SELECT COUNT(*) FROM <t>.
	// Quirks:
	EmptyIsNull    bool // '' is NULL (Oracle)
	NoErrPosition  bool // errors carry no statement position
	NoDDLRoundTrip bool
	// TypeColumns of all_types to round-trip through the editor
	// (column → literal text to write); empty = all non-key columns with
	// a default sample for their kind.
	Samples map[string]string
	// Fold maps unquoted identifiers as the server reports them (Oracle and
	// Firebird report upper case).
	Upper bool
	// Eventual: writes are not immediately visible (ClickHouse mutations).
	Eventual bool
	// ExtraObjects lists object types expected besides tables and views.
	Expect []db.ObjectType
	// Cassandra-like quirks: columns ordered by key then name, no NOT NULL,
	// ORDER BY only within a partition, filters need ALLOW FILTERING.
	UnorderedColumns bool
	NoNotNull        bool
	NoOrderBy        bool
	FilterSuffix     string
	// NoViews: only materialized views (Cassandra).
	NoViews bool
	// NoKeys: no primary keys or NOT NULL constraints (Trino).
	NoKeys bool
	// AfterLoad runs after the fixture (bulk data the script can't create).
	AfterLoad func(ctx context.Context, c db.Conn) error
	// Setup runs before the suite (e.g. waits for a slow server).
	Setup func(t *testing.T)
}

func (tg *Target) id(s string) string {
	if tg.Upper {
		return strings.ToUpper(s)
	}
	return s
}

func (tg *Target) ref(name string) db.ObjectRef {
	return db.ObjectRef{Catalog: tg.Catalog, Schema: tg.Schema, Name: tg.id(name), Type: db.ObjTable}
}

// Reachable reports whether host:port accepts TCP connections.
func Reachable(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// Prepare connects to a target (waiting for the server to start) and
// loads its fixture. It skips the test when the server isn't reachable.
func Prepare(t *testing.T, tg Target) db.Conn {
	t.Helper()
	if tg.Config.Host != "" {
		addr := tg.Config.Addr(0)
		if !Reachable(addr) {
			if os.Getenv("LUMINO_REQUIRE_DBS") != "" {
				t.Fatalf("%s not reachable at %s", tg.Name, addr)
			}
			t.Skipf("%s not reachable at %s (scripts/dbs/dbs.sh up)", tg.Name, addr)
		}
	}
	if tg.Setup != nil {
		tg.Setup(t)
	}
	ctx := context.Background()
	var conn db.Conn
	var err error
	deadline := time.Now().Add(3 * time.Minute) // servers may still be starting
	for {
		conn, err = db.Open(ctx, &tg.Config, db.OpenOptions{})
		var dbe *db.Error
		if err == nil || time.Now().After(deadline) || errors.As(err, &dbe) || strings.Contains(err.Error(), "unknown driver") {
			break // connected, gave up, or the server answered with an error
		}
		t.Logf("open %s: %v (retrying)", tg.Name, err)
		time.Sleep(3 * time.Second)
	}
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	t.Logf("%s: %s", tg.Name, conn.ServerVersion())
	if tg.Loader != nil {
		err = tg.Loader(ctx, conn)
	} else {
		err = Load(ctx, conn, tg.Fixture)
	}
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if tg.AfterLoad != nil {
		if err := tg.AfterLoad(ctx, conn); err != nil {
			t.Fatalf("fixture data: %v", err)
		}
	}
	return conn
}

// Ref names a fixture table of the target.
func (tg *Target) Ref(name string) db.ObjectRef { return tg.ref(name) }

// ID folds a fixture identifier like the server.
func (tg *Target) ID(s string) string { return tg.id(s) }

// Run executes the conformance suite.
func Run(t *testing.T, tg Target) {
	conn := Prepare(t, tg)
	ctx := context.Background()
	caps := conn.Caps()
	cat := conn.Catalog()
	lang := conn.Language()
	// d is nil for databases whose language is not SQL: the checks running
	// SQL text skip there (sqlOnly)
	d := db.DialectOf(conn)
	sqlOnly := func(t *testing.T) {
		t.Helper()
		if d == nil {
			t.Skip("needs SQL")
		}
	}
	countOf := func(t *testing.T, ref db.ObjectRef) string {
		t.Helper()
		q, err := lang.Count(ref, "")
		if err != nil {
			t.Fatal(err)
		}
		return q
	}

	t.Run("Ping", func(t *testing.T) {
		if err := conn.Ping(ctx); err != nil {
			t.Fatal(err)
		}
		if conn.ServerVersion() == "" {
			t.Error("empty server version")
		}
	})

	t.Run("Catalogs", func(t *testing.T) {
		if !caps.Catalogs {
			return
		}
		cs, err := cat.Catalogs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(cs, tg.Catalog) {
			t.Fatalf("catalog %q missing from %v", tg.Catalog, cs)
		}
	})

	t.Run("Schemas", func(t *testing.T) {
		if !caps.Schemas {
			return
		}
		ss, err := cat.Schemas(ctx, tg.Catalog)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, s := range ss {
			names = append(names, s.Name)
		}
		if !slices.Contains(names, tg.Schema) {
			t.Fatalf("schema %q missing from %v", tg.Schema, names)
		}
	})

	t.Run("Objects", func(t *testing.T) {
		parent := db.ObjectRef{Catalog: tg.Catalog, Schema: tg.Schema}
		objs, err := cat.Objects(ctx, parent)
		if err != nil {
			t.Fatal(err)
		}
		have := map[db.ObjectType][]string{}
		for _, o := range objs {
			have[o.Ref.Type] = append(have[o.Ref.Type], o.Ref.Name)
		}
		for _, n := range []string{"customers", "orders", "order_items", "all_types", "big"} {
			if !slices.Contains(have[db.ObjTable], tg.id(n)) {
				t.Errorf("table %s missing: %v", n, have[db.ObjTable])
			}
		}
		if caps.Views && !tg.NoViews && !slices.Contains(have[db.ObjView], tg.id("customer_totals")) {
			t.Errorf("view customer_totals missing: %v", have[db.ObjView])
		}
		for _, ty := range tg.Expect {
			if len(have[ty]) == 0 {
				t.Errorf("no %s listed", ty)
			}
		}
		// filtering by type
		only, err := cat.Objects(ctx, parent, db.ObjView)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range only {
			if o.Ref.Type != db.ObjView {
				t.Errorf("type filter returned %s %s", o.Ref.Type, o.Ref.Name)
			}
		}
		// every listed object has a DDL
		for _, o := range objs {
			if o.Ref.Type == db.ObjTrigger || o.Ref.Type == db.ObjIndex {
				continue
			}
			ddl, err := cat.DDL(ctx, o.Ref)
			if err != nil {
				t.Errorf("DDL %s %s: %v", o.Ref.Type, o.Ref, err)
				continue
			}
			if !strings.Contains(strings.ToLower(ddl), strings.ToLower(o.Ref.Name)) {
				t.Errorf("DDL of %s doesn't mention it:\n%s", o.Ref, ddl)
			}
		}
	})

	t.Run("Describe", func(t *testing.T) {
		cust, err := cat.Table(ctx, tg.ref("customers"))
		if err != nil {
			t.Fatal(err)
		}
		got := colNames(cust)
		want := ids(&tg, "id", "name", "email", "created_at")
		if tg.UnorderedColumns {
			slices.Sort(got)
			slices.Sort(want)
		}
		if !slices.Equal(got, want) {
			t.Errorf("customers columns %v", got)
		}
		if tg.NoKeys {
			return
		}
		if !slices.Equal(cust.PrimaryKey, ids(&tg, "id")) {
			t.Errorf("customers pk %v", cust.PrimaryKey)
		}
		if c, _ := cust.Column(tg.id("name")); c.Nullable && !tg.NoNotNull {
			t.Error("customers.name should be NOT NULL")
		}
		if c, _ := cust.Column(tg.id("email")); !c.Nullable {
			t.Error("customers.email should be nullable")
		}
		items, err := cat.Table(ctx, tg.ref("order_items"))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(items.PrimaryKey, ids(&tg, "order_id", "line_no")) {
			t.Errorf("order_items composite pk %v", items.PrimaryKey)
		}
		ord, err := cat.Table(ctx, tg.ref("orders"))
		if err != nil {
			t.Fatal(err)
		}
		if caps.ForeignKeys {
			if len(ord.ForeignKeys) != 1 {
				t.Fatalf("orders fks %+v", ord.ForeignKeys)
			}
			fk := ord.ForeignKeys[0]
			if !slices.Equal(fk.Columns, ids(&tg, "customer_id")) || fk.RefTable.Name != tg.id("customers") || !slices.Equal(fk.RefColumns, ids(&tg, "id")) {
				t.Errorf("orders fk %+v", fk)
			}
			if !strings.EqualFold(fk.OnDelete, "CASCADE") {
				t.Errorf("orders fk on delete %q", fk.OnDelete)
			}
		}
		found := false
		for _, ix := range ord.Indexes {
			if strings.EqualFold(ix.Name, "orders_status_idx") {
				found = true
				if !slices.Equal(ix.Columns, ids(&tg, "status")) {
					t.Errorf("index columns %v", ix.Columns)
				}
			}
		}
		if !found && caps.ForeignKeys {
			t.Errorf("index orders_status_idx missing: %+v", ord.Indexes)
		}
	})

	t.Run("DDLRoundTrip", func(t *testing.T) {
		if tg.NoDDLRoundTrip {
			t.Skip("not supported")
		}
		sqlOnly(t)
		ref := tg.ref("ddl_rt")
		before, err := cat.Table(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		ddl, err := cat.DDL(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		if err := Load(ctx, conn, "DROP TABLE "+d.Qualified(ref)); err != nil {
			t.Fatal(err)
		}
		if err := Load(ctx, conn, ddl); err != nil {
			t.Fatalf("re-running DDL: %v\n%s", err, ddl)
		}
		after, err := cat.Table(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		if a, b := describe(before), describe(after); a != b {
			t.Errorf("structure changed after DDL round trip:\nbefore:\n%s\nafter:\n%s\nDDL:\n%s", a, b, ddl)
		}
	})

	t.Run("Completion", func(t *testing.T) {
		m, err := cat.Completion(ctx, db.ObjectRef{Catalog: tg.Catalog, Schema: tg.Schema})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(m[tg.id("customers")], tg.id("email")) {
			t.Errorf("completion: customers columns %v", m[tg.id("customers")])
		}
	})

	t.Run("Streaming", func(t *testing.T) {
		s := session(t, conn)
		var bq db.BrowseQuery
		if !tg.NoOrderBy {
			bq.Sort = []db.SortKey{{Field: tg.id("id")}}
		}
		q, err := lang.Browse(tg.ref("big"), bq)
		if err != nil {
			t.Fatal(err)
		}
		res, err := s.Execute(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Close()
		if len(res.Columns()) < 2 {
			t.Fatalf("columns %+v", res.Columns())
		}
		rows, more, err := res.Fetch(ctx, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != min(1000, tg.BigRows) || !more && tg.BigRows > 1000 {
			t.Fatalf("first page: %d rows, more=%v", len(rows), more)
		}
		total := len(rows)
		for more {
			rows, more, err = res.Fetch(ctx, 5000)
			if err != nil {
				t.Fatal(err)
			}
			total += len(rows)
		}
		if total != tg.BigRows {
			t.Fatalf("streamed %d rows, want %d", total, tg.BigRows)
		}
		// the session is usable again
		if v := scalar(t, s, countOf(t, tg.ref("customers"))); v == "" {
			t.Fatal("count failed")
		}
	})

	t.Run("Cancel", func(t *testing.T) {
		if tg.Sleep == "" || !caps.Cancel {
			t.Skip("no sleep statement")
		}
		s := session(t, conn)
		start := time.Now()
		done := make(chan error, 1)
		go func() {
			res, err := s.Execute(ctx, tg.Sleep)
			if err == nil {
				_, _, err = res.Fetch(ctx, 10)
				res.Close()
			}
			done <- err
		}()
		time.Sleep(700 * time.Millisecond)
		s.Cancel()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("cancelled statement reported success")
			}
			t.Logf("cancelled after %v: %v", time.Since(start).Round(time.Millisecond), err)
		case <-time.After(15 * time.Second):
			t.Fatal("cancel didn't stop the statement")
		}
		if v := scalar(t, s, countOf(t, tg.ref("customers"))); v == "" {
			t.Fatal("session unusable after cancel")
		}
	})

	t.Run("Transactions", func(t *testing.T) {
		if !caps.Transactions {
			t.Skip("no transactions")
		}
		s := session(t, conn)
		other := session(t, conn)
		sqlOnly(t)
		count := countOf(t, tg.ref("customers"))
		n0 := scalar(t, s, count)
		if err := s.Begin(ctx); err != nil {
			t.Fatal(err)
		}
		if !s.TxActive() {
			t.Fatal("TxActive false after Begin")
		}
		exec(t, s, insertCustomer(d, &tg, 9001, "tx-rollback"))
		if got := scalar(t, s, count); got == n0 {
			t.Fatal("insert not visible inside the transaction")
		}
		if err := s.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		if got := scalar(t, s, count); got != n0 {
			t.Fatalf("rollback: count %s, want %s", got, n0)
		}
		if err := s.Begin(ctx); err != nil {
			t.Fatal(err)
		}
		exec(t, s, insertCustomer(d, &tg, 9002, "tx-commit"))
		// locking databases (SQL Server) block the reader instead: a
		// timeout means the row isn't visible either
		tctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		cols, rows, err := Query(tctx, other, count)
		cancel()
		if err == nil && db.FormatValue(rows[0][0], cols[0].Kind) != n0 {
			t.Fatalf("uncommitted insert visible to another session")
		}
		if err := s.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if got := scalar(t, other, count); got == n0 {
			t.Fatal("committed insert not visible to another session")
		}
		// client statements map to the session transaction
		exec(t, s, "BEGIN")
		if !s.TxActive() {
			t.Fatal("BEGIN statement didn't open a transaction")
		}
		exec(t, s, "ROLLBACK")
		if s.TxActive() {
			t.Fatal("ROLLBACK statement didn't close the transaction")
		}
		exec(t, s, "DELETE FROM "+d.Qualified(tg.ref("customers"))+" WHERE "+d.Quote(tg.id("id"))+" = 9002")
	})

	t.Run("ReadOnly", func(t *testing.T) {
		if !caps.ReadOnlyTx {
			t.Skip("no read-only sessions")
		}
		sqlOnly(t)
		s := session(t, conn)
		if err := s.SetReadOnly(ctx, true); err != nil {
			t.Fatal(err)
		}
		res, err := s.Execute(ctx, insertCustomer(d, &tg, 9003, "ro"))
		if err == nil {
			res.Close()
			t.Fatal("insert allowed in a read-only session")
		}
		if err := s.SetReadOnly(ctx, false); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("Schema", func(t *testing.T) {
		s := session(t, conn)
		if err := s.SetCurrentSchema(ctx, tg.Schema); errors.Is(err, db.ErrNotSupported) || tg.Schema == "" {
			t.Skip("no session schema")
		} else if err != nil {
			t.Fatal(err)
		}
		cur, err := s.CurrentSchema(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.EqualFold(cur, tg.Schema) {
			t.Fatalf("current schema %q, want %q", cur, tg.Schema)
		}
		// unqualified names resolve in it
		if v := scalar(t, s, countOf(t, db.ObjectRef{Name: tg.id("customers"), Type: db.ObjTable})); v == "" {
			t.Fatal("unqualified query failed")
		}
	})

	t.Run("Errors", func(t *testing.T) {
		sqlOnly(t)
		s := session(t, conn)
		_, err := s.Execute(ctx, "SELECT * FROM "+d.Qualified(tg.ref("customers"))+" WHERE nosuchcolumn_xyz = 1")
		if err == nil {
			t.Fatal("expected an error")
		}
		e := db.AsError(err)
		if e.Msg == "" {
			t.Fatal("empty error message")
		}
		if !tg.NoErrPosition && e.Position == 0 && e.Line == 0 {
			t.Errorf("error without position: %+v", e)
		}
		// the session survives errors (also inside a transaction)
		if v := scalar(t, s, countOf(t, tg.ref("customers"))); v == "" {
			t.Fatal("session unusable after error")
		}
	})

	t.Run("ValueTypes", func(t *testing.T) {
		// every value is normalized to the documented Row value set
		s := session(t, conn)
		q, err := lang.Browse(tg.ref("all_types"), db.BrowseQuery{})
		if err != nil {
			t.Fatal(err)
		}
		cols, rows, err := Query(ctx, s, q)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			for i, v := range r {
				switch v.(type) {
				case nil, string, int64, uint64, float64, bool, time.Time, []byte, []any, map[string]any:
				default:
					t.Errorf("column %s (%s): unnormalized value %T", cols[i].Name, cols[i].DBType, v)
				}
				_ = db.CellText(v, cols[i].Kind)
			}
		}
	})

	t.Run("RowEdit", func(t *testing.T) { rowEdit(t, ctx, conn, &tg) })
	t.Run("TypesRoundTrip", func(t *testing.T) { typesRoundTrip(t, ctx, conn, &tg) })
	t.Run("Script", func(t *testing.T) { script(t, ctx, conn, &tg) })
}

func ids(tg *Target, s ...string) []string {
	out := make([]string, len(s))
	for i, x := range s {
		out[i] = tg.id(x)
	}
	return out
}

func colNames(t *db.TableInfo) []string {
	var out []string
	for _, c := range t.Columns {
		out = append(out, c.Name)
	}
	return out
}

func describe(t *db.TableInfo) string {
	var b strings.Builder
	for _, c := range t.Columns {
		fmt.Fprintf(&b, "col %s %s null=%v def=%q gen=%q auto=%v\n", c.Name, c.Type, c.Nullable, c.Default, c.Generated, c.AutoIncr)
	}
	fmt.Fprintf(&b, "pk %v\n", t.PrimaryKey)
	ix := make([]string, 0, len(t.Indexes))
	for _, i := range t.Indexes {
		if i.Primary {
			continue
		}
		ix = append(ix, fmt.Sprintf("index %v unique=%v method=%s where=%s", i.Columns, i.Unique, i.Method, i.Where))
	}
	slices.Sort(ix)
	for _, s := range ix {
		b.WriteString(s + "\n")
	}
	for _, c := range t.Constraints {
		fmt.Fprintf(&b, "constraint %s %v %s\n", c.Type, c.Columns, c.Definition)
	}
	for _, f := range t.ForeignKeys {
		fmt.Fprintf(&b, "fk %v -> %s %v %s %s\n", f.Columns, f.RefTable.Name, f.RefColumns, f.OnUpdate, f.OnDelete)
	}
	return b.String()
}

func session(t *testing.T, c db.Conn) db.Session {
	t.Helper()
	s, err := c.Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func exec(t *testing.T, s db.Session, q string, args ...any) int64 {
	t.Helper()
	res, err := s.Execute(context.Background(), q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer res.Close()
	if res.Columns() != nil {
		for {
			_, more, err := res.Fetch(context.Background(), 1000)
			if err != nil {
				t.Fatalf("%s: %v", q, err)
			}
			if !more {
				break
			}
		}
	}
	return res.RowsAffected()
}

// Query runs a query and returns all rows.
func Query(ctx context.Context, s db.Session, q string, args ...any) ([]db.Column, []db.Row, error) {
	res, err := s.Execute(ctx, q, args...)
	if err != nil {
		return nil, nil, err
	}
	defer res.Close()
	var all []db.Row
	for {
		rows, more, err := res.Fetch(ctx, 1000)
		if err != nil {
			return nil, nil, err
		}
		all = append(all, rows...)
		if !more {
			return res.Columns(), all, nil
		}
	}
}

func scalar(t *testing.T, s db.Session, q string) string {
	t.Helper()
	cols, rows, err := Query(context.Background(), s, q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	if len(rows) == 0 || len(rows[0]) == 0 {
		t.Fatalf("%s: no rows", q)
	}
	return db.FormatValue(rows[0][0], cols[0].Kind)
}

func insertCustomer(d db.Dialect, tg *Target, id int, name string) string {
	return fmt.Sprintf("INSERT INTO %s (%s, %s, %s) VALUES (%d, '%s', '%s@example.com')", d.Qualified(tg.ref("customers")),
		d.Quote(tg.id("id")), d.Quote(tg.id("name")), d.Quote(tg.id("email")), id, name, name)
}

// Load runs a script statement by statement on a fresh session.
func Load(ctx context.Context, c db.Conn, script string) error {
	s, err := c.Session(ctx)
	if err != nil {
		return err
	}
	defer s.Close()
	for _, st := range c.Language().Split(script) {
		res, err := s.Execute(ctx, st.Text)
		if err != nil {
			return fmt.Errorf("%s: %w", firstLine(st.Text), err)
		}
		if res.Columns() != nil {
			for {
				_, more, err := res.Fetch(ctx, 10000)
				if err != nil {
					res.Close()
					return fmt.Errorf("%s: %w", firstLine(st.Text), err)
				}
				if !more {
					break
				}
			}
		}
		res.Close()
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}
