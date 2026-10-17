//go:build integration

package conformance

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/db/dbtest"
)

func pgcTable(t *testing.T, conn db.Conn, schema, name string) *db.TableInfo {
	t.Helper()
	ti, err := conn.Catalog().Table(context.Background(), db.ObjectRef{Schema: schema, Name: name, Type: db.ObjTable})
	if err != nil {
		t.Fatalf("Table %s: %v", name, err)
	}
	return ti
}

func pgcDDL(t *testing.T, conn db.Conn, schema, name string, typ db.ObjectType) string {
	t.Helper()
	d, err := conn.Catalog().DDL(context.Background(), db.ObjectRef{Schema: schema, Name: name, Type: typ})
	if err != nil {
		t.Fatalf("DDL %s: %v", name, err)
	}
	return d
}

func pgcContains(t *testing.T, d string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(d, p) {
			t.Errorf("DDL lacks %q:\n%s", p, d)
		}
	}
}

func pgcColNames(ti *db.TableInfo) []string {
	var out []string
	for _, c := range ti.Columns {
		out = append(out, c.Name)
	}
	return out
}

// pgcRoundTrip drops an object, recreates it from its DDL and compares the
// DDL again.
func pgcRoundTrip(t *testing.T, conn db.Conn, s db.Session, schema, name string, typ db.ObjectType, drop string) {
	t.Helper()
	before := pgcDDL(t, conn, schema, name, typ)
	pgrExec(t, s, drop)
	if err := dbtest.Load(context.Background(), conn, before); err != nil {
		t.Fatalf("recreate %s: %v\n%s", name, err, before)
	}
	if after := pgcDDL(t, conn, schema, name, typ); after != before {
		t.Errorf("%s DDL changed after round trip:\n%s\n---\n%s", name, before, after)
	}
}

// bug 2: trigger events and timing from tgtype.
func TestPgCatalogRegressTriggers(t *testing.T) {
	conn, s := pgrOpen(t, "postgres", nil)
	const sch = "pgc_trg"
	pgrSchema(t, s, sch)
	for _, q := range []string{
		`CREATE TABLE ` + sch + `.t (id int PRIMARY KEY, stamp timestamptz)`,
		`CREATE VIEW ` + sch + `.v AS SELECT * FROM ` + sch + `.t`,
		`CREATE FUNCTION ` + sch + `.update_stamp() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RETURN NEW; END$$`,
		`CREATE TRIGGER update_trg AFTER INSERT ON ` + sch + `.t FOR EACH ROW EXECUTE FUNCTION ` + sch + `.update_stamp()`,
		`CREATE TRIGGER before_trg BEFORE UPDATE OR DELETE ON ` + sch + `.t FOR EACH ROW EXECUTE FUNCTION ` + sch + `.update_stamp()`,
		`CREATE TRIGGER trunc_after AFTER TRUNCATE ON ` + sch + `.t FOR EACH STATEMENT EXECUTE FUNCTION ` + sch + `.update_stamp()`,
	} {
		pgrExec(t, s, q)
	}
	ti := pgcTable(t, conn, sch, "t")
	got := map[string]string{}
	for _, tr := range ti.Triggers {
		got[tr.Name] = tr.Timing + "|" + tr.Events
	}
	want := map[string]string{"update_trg": "AFTER|INSERT", "before_trg": "BEFORE|UPDATE, DELETE", "trunc_after": "AFTER|TRUNCATE"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("triggers %v, want %v", got, want)
	}
}

// bug 3: view options, check option; matview indexes, WITH NO DATA, comments.
func TestPgCatalogRegressViews(t *testing.T) {
	conn, s := pgrOpen(t, "postgres", nil)
	const sch = "pgc_views"
	pgrSchema(t, s, sch)
	for _, q := range []string{
		`CREATE TABLE ` + sch + `.t (id int PRIMARY KEY, v text)`,
		`CREATE VIEW ` + sch + `.sb WITH (security_barrier=true) AS SELECT id, v FROM ` + sch + `.t WHERE id > 0 WITH LOCAL CHECK OPTION`,
		`CREATE VIEW ` + sch + `.cc AS SELECT id FROM ` + sch + `.t WITH CASCADED CHECK OPTION`,
		`COMMENT ON VIEW ` + sch + `.cc IS 'view ''comment'''`,
		`CREATE MATERIALIZED VIEW ` + sch + `.mv AS SELECT id, v FROM ` + sch + `.t WITH NO DATA`,
		`CREATE UNIQUE INDEX mv_id ON ` + sch + `.mv (id)`,
		`COMMENT ON MATERIALIZED VIEW ` + sch + `.mv IS 'mat comment'`,
		`COMMENT ON COLUMN ` + sch + `.mv.v IS 'col comment'`,
	} {
		pgrExec(t, s, q)
	}
	pgcContains(t, pgcDDL(t, conn, sch, "sb", db.ObjView), "VIEW "+sch+".sb WITH (security_barrier=true) AS", "WITH LOCAL CHECK OPTION;")
	pgcContains(t, pgcDDL(t, conn, sch, "cc", db.ObjView), "WITH CASCADED CHECK OPTION;", "COMMENT ON VIEW "+sch+".cc IS 'view ''comment''';")
	d := pgcDDL(t, conn, sch, "mv", db.ObjMatView)
	pgcContains(t, d, "WITH NO DATA;", "CREATE UNIQUE INDEX mv_id ON "+sch+".mv USING btree (id);",
		"COMMENT ON MATERIALIZED VIEW "+sch+".mv IS 'mat comment';", "COMMENT ON COLUMN "+sch+".mv.v IS 'col comment';")
	pgcRoundTrip(t, conn, s, sch, "mv", db.ObjMatView, "DROP MATERIALIZED VIEW "+sch+".mv")
	pgcRoundTrip(t, conn, s, sch, "sb", db.ObjView, "DROP VIEW "+sch+".sb")
	pgrExec(t, s, "REFRESH MATERIALIZED VIEW "+sch+".mv")
	if d := pgcDDL(t, conn, sch, "mv", db.ObjMatView); strings.Contains(d, "NO DATA") {
		t.Errorf("populated matview rendered WITH NO DATA:\n%s", d)
	}
}

// bug 4: UNLOGGED, COLLATE, INHERITS.
func TestPgCatalogRegressTableDDL(t *testing.T) {
	conn, s := pgrOpen(t, "postgres", nil)
	const sch = "pgc_tddl"
	pgrSchema(t, s, sch)
	for _, q := range []string{
		`CREATE UNLOGGED TABLE ` + sch + `.ul (id int PRIMARY KEY, name text COLLATE "C", other varchar(10) COLLATE "POSIX", plain text)`,
		`CREATE TABLE ` + sch + `.par (id int NOT NULL, v text, CONSTRAINT par_chk CHECK (id > 0))`,
		`CREATE TABLE ` + sch + `.par2 (w int)`,
		`CREATE TABLE ` + sch + `.kid (extra text, CONSTRAINT kid_chk CHECK (extra <> '')) INHERITS (` + sch + `.par, ` + sch + `.par2)`,
	} {
		pgrExec(t, s, q)
	}
	d := pgcDDL(t, conn, sch, "ul", db.ObjTable)
	pgcContains(t, d, "CREATE UNLOGGED TABLE "+sch+".ul", `name text COLLATE "C"`, `other character varying(10) COLLATE "POSIX"`)
	if strings.Contains(d, "plain text COLLATE") {
		t.Errorf("default collation rendered:\n%s", d)
	}
	pgcRoundTrip(t, conn, s, sch, "ul", db.ObjTable, "DROP TABLE "+sch+".ul")

	d = pgcDDL(t, conn, sch, "kid", db.ObjTable)
	pgcContains(t, d, "INHERITS ("+sch+".par, "+sch+".par2)", "extra text", "kid_chk")
	for _, bad := range []string{"par_chk", "    id ", "    v ", "    w "} {
		if strings.Contains(d, bad) {
			t.Errorf("child DDL contains inherited %q:\n%s", bad, d)
		}
	}
	pgcRoundTrip(t, conn, s, sch, "kid", db.ObjTable, "DROP TABLE "+sch+".kid")
}

// bug 5: index columns are raw names; expressions only for expression keys.
func TestPgCatalogRegressIndexColumns(t *testing.T) {
	conn, s := pgrOpen(t, "postgres", nil)
	const sch = "pgc_idx"
	pgrSchema(t, s, sch)
	pgrExec(t, s, `CREATE TABLE `+sch+`.t (id int PRIMARY KEY, "Col ""1" text, b int)`)
	pgrExec(t, s, `CREATE INDEX t_mix ON `+sch+`.t ("Col ""1" DESC, lower("Col ""1"), b)`)
	ti := pgcTable(t, conn, sch, "t")
	var got []string
	for _, ix := range ti.Indexes {
		if ix.Name == "t_mix" {
			got = ix.Columns
		}
	}
	want := []string{`Col "1`, `lower("Col ""1")`, "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("index columns %q, want %q", got, want)
	}
}

// bug 6: switching schema keeps the rest of the search_path.
func TestPgCatalogRegressSearchPath(t *testing.T) {
	pgcSearchPath(t, "postgres")
}

func pgcSearchPath(t *testing.T, target string) {
	_, s := pgrOpen(t, target, nil)
	pgrSchema(t, s, "pgc_sp")
	pgrSchema(t, s, `"pgc ""x"`)
	ctx := context.Background()
	pgrExec(t, s, `SET search_path TO "$user", public, pgc_sp`)
	if err := s.SetCurrentSchema(ctx, `pgc "x`); err != nil {
		t.Fatal(err)
	}
	if got := pgrScalar(t, s, "SHOW search_path"); got != `"pgc ""x", "$user", public` { // the session default, not the SET path
		t.Errorf("search_path %s", got)
	}
	if err := s.SetCurrentSchema(ctx, "pgc_sp"); err != nil {
		t.Fatal(err)
	}
	if got := pgrScalar(t, s, "SHOW search_path"); got != `pgc_sp, "$user", public` { // the previous choice doesn't linger
		t.Errorf("search_path %s", got)
	}
	if cur, _ := s.CurrentSchema(ctx); cur != "pgc_sp" {
		t.Errorf("current schema %q", cur)
	}
}

// bug 1: CockroachDB hidden rowid and computed columns.
func TestPgCatalogRegressCockroachColumns(t *testing.T) {
	conn, s := pgrOpen(t, "cockroach", nil)
	const sch = "pgc_crdb"
	pgrSchema(t, s, sch)
	pgrExec(t, s, `CREATE TABLE `+sch+`.nopk (v int)`)
	pgrExec(t, s, `CREATE TABLE `+sch+`.comp (id int PRIMARY KEY, x int, g int AS (x + 1) STORED, h int AS (x * 2) VIRTUAL)`)
	ti := pgcTable(t, conn, sch, "nopk")
	if got := pgcColNames(ti); !reflect.DeepEqual(got, []string{"v"}) || len(ti.PrimaryKey) != 0 {
		t.Errorf("nopk columns %v pk %v", got, ti.PrimaryKey)
	}
	for _, ix := range ti.Indexes {
		for _, c := range ix.Columns {
			if c == "rowid" {
				t.Errorf("index on hidden rowid exposed: %+v", ix)
			}
		}
	}
	ti = pgcTable(t, conn, sch, "comp")
	gen := map[string]string{}
	for _, c := range ti.Columns {
		gen[c.Name] = c.Generated
		if c.Generated != "" && c.HasDefault {
			t.Errorf("computed column %s has default %q", c.Name, c.Default)
		}
	}
	if gen["g"] == "" || gen["h"] == "" || gen["x"] != "" {
		t.Errorf("generated %q", gen)
	}
	comp, err := conn.Catalog().Completion(context.Background(), db.ObjectRef{Schema: sch})
	if err != nil {
		t.Fatal(err)
	}
	if got := comp["nopk"]; !reflect.DeepEqual(got, []string{"v"}) {
		t.Errorf("completion nopk %v", got)
	}
}

func TestPgCatalogRegressCockroachSearchPath(t *testing.T) {
	pgcSearchPath(t, "cockroach")
}
