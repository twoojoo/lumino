//go:build integration

package conformance

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/db/dbtest"
)

// pgrOpen connects to a dev target and returns a session.
func pgrOpen(t *testing.T, name string, params map[string]string) (db.Conn, db.Session) {
	t.Helper()
	tg := dbtest.Targets()[name]
	host := tg.Config.Host + ":" + strconv.Itoa(tg.Config.Port)
	needs(t, host)
	cfg := tg.Config
	cfg.Params = params
	conn, err := db.Open(context.Background(), &cfg, db.OpenOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	s, err := conn.Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return conn, s
}

func pgrExec(t *testing.T, s db.Session, q string) {
	t.Helper()
	if _, _, err := dbtest.Query(context.Background(), s, q); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func pgrScalar(t *testing.T, s db.Session, q string) string {
	t.Helper()
	cols, rows, err := dbtest.Query(context.Background(), s, q)
	if err != nil || len(rows) != 1 {
		t.Fatalf("%s: %v (%d rows)", q, err, len(rows))
	}
	return db.FormatValue(rows[0][0], cols[0].Kind)
}

func pgrSchema(t *testing.T, s db.Session, schema string) {
	t.Helper()
	pgrExec(t, s, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	pgrExec(t, s, "CREATE SCHEMA "+schema)
	t.Cleanup(func() {
		_, _, _ = dbtest.Query(context.Background(), s, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	})
}

// commaKeys checks bug 1: key column lists with commas and spaces.
func commaKeys(t *testing.T, conn db.Conn, s db.Session, schema string) {
	pgrExec(t, s, `CREATE TABLE `+schema+`.parent (id int, "a,b" int, " c d " int, PRIMARY KEY (id, "a,b"), UNIQUE (" c d ", id))`)
	pgrExec(t, s, `CREATE TABLE `+schema+`.child (x int PRIMARY KEY, "p,id" int, "p ab" int, FOREIGN KEY ("p,id", "p ab") REFERENCES `+schema+`.parent (id, "a,b"))`)
	ctx := context.Background()
	ti, err := conn.Catalog().Table(ctx, db.ObjectRef{Schema: schema, Name: "parent", Type: db.ObjTable})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"id", "a,b"}; !reflect.DeepEqual(ti.PrimaryKey, want) {
		t.Errorf("primary key %q, want %q", ti.PrimaryKey, want)
	}
	var uniq []string
	for _, c := range ti.Constraints {
		if c.Type == "UNIQUE" {
			uniq = c.Columns
		}
	}
	if want := []string{" c d ", "id"}; !reflect.DeepEqual(uniq, want) {
		t.Errorf("unique columns %q, want %q", uniq, want)
	}
	ci, err := conn.Catalog().Table(ctx, db.ObjectRef{Schema: schema, Name: "child", Type: db.ObjTable})
	if err != nil {
		t.Fatal(err)
	}
	if len(ci.ForeignKeys) != 1 {
		t.Fatalf("foreign keys %+v", ci.ForeignKeys)
	}
	fk := ci.ForeignKeys[0]
	if !reflect.DeepEqual(fk.Columns, []string{"p,id", "p ab"}) || !reflect.DeepEqual(fk.RefColumns, []string{"id", "a,b"}) {
		t.Errorf("foreign key columns %q → %q", fk.Columns, fk.RefColumns)
	}
}

func TestPostgresRegressKeyColumns(t *testing.T) {
	conn, s := pgrOpen(t, "postgres", nil)
	pgrSchema(t, s, "lumino_regress")
	commaKeys(t, conn, s, "lumino_regress")
}

func TestCockroachRegressKeyColumns(t *testing.T) {
	conn, s := pgrOpen(t, "cockroach", nil)
	pgrSchema(t, s, "lumino_regress")
	commaKeys(t, conn, s, "lumino_regress")
	// identity-owned sequences filter must not break the listing
	pgrExec(t, s, "CREATE SEQUENCE lumino_regress.seq1")
	objs, err := conn.Catalog().Objects(context.Background(), db.ObjectRef{Schema: "lumino_regress"}, db.ObjSequence, db.ObjTable)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 3 {
		t.Errorf("objects %+v", objs)
	}
}

// bug 2: libpq keywords in the connection parameters.
func TestPostgresRegressParams(t *testing.T) {
	_, s := pgrOpen(t, "postgres", map[string]string{
		"connect_timeout": "10", "sslmode": "disable", "application_name": "lumino-regress",
		"target_session_attrs": "read-write", "keepalives": "1", "search_path": "pg_catalog", "statement_timeout": "12345",
	})
	if got := pgrScalar(t, s, "SELECT current_setting('application_name')"); got != "lumino-regress" {
		t.Errorf("application_name %q", got)
	}
	if got := pgrScalar(t, s, "SELECT current_setting('statement_timeout')"); got != "12345ms" {
		t.Errorf("statement_timeout %q", got)
	}
	if got := pgrScalar(t, s, "SELECT ssl::text FROM pg_stat_ssl WHERE pid = pg_backend_pid()"); got != "false" {
		t.Errorf("sslmode=disable: ssl %q", got)
	}
	tg := dbtest.Targets()["postgres"]
	cfg := tg.Config
	cfg.Params = map[string]string{"target_session_attrs": "standby"}
	if c, err := db.Open(context.Background(), &cfg, db.OpenOptions{}); err == nil {
		c.Close()
		t.Error("target_session_attrs=standby accepted a primary")
	}
}

// bug 3 and 4: DDL of identity, partitioned, foreign and serial tables.
func TestPostgresRegressDDL(t *testing.T) {
	conn, s := pgrOpen(t, "postgres", nil)
	const sch = "lumino_regress"
	pgrSchema(t, s, sch)
	pgrExec(t, s, "DROP SERVER IF EXISTS lumino_regress_srv CASCADE")
	pgrExec(t, s, "DROP FOREIGN DATA WRAPPER IF EXISTS lumino_regress_fdw CASCADE")
	t.Cleanup(func() {
		_, _, _ = dbtest.Query(context.Background(), s, "DROP FOREIGN DATA WRAPPER IF EXISTS lumino_regress_fdw CASCADE")
	})
	for _, q := range []string{
		`CREATE TABLE ` + sch + `.ident (id int GENERATED ALWAYS AS IDENTITY (START WITH 100 INCREMENT BY 5) PRIMARY KEY,
			b bigint GENERATED BY DEFAULT AS IDENTITY, v text)`,
		`CREATE TABLE ` + sch + `.ser (id serial PRIMARY KEY, big bigserial, v text)`,
		`CREATE TABLE ` + sch + `.parted (id int NOT NULL, d date NOT NULL, v text) PARTITION BY RANGE (d)`,
		`CREATE TABLE ` + sch + `.parted_2024 PARTITION OF ` + sch + `.parted FOR VALUES FROM ('2024-01-01') TO ('2025-01-01')`,
		`CREATE TABLE ` + sch + `.parted_rest PARTITION OF ` + sch + `.parted DEFAULT PARTITION BY LIST (id)`,
		`CREATE TABLE ` + sch + `.parted_rest_1 PARTITION OF ` + sch + `.parted_rest FOR VALUES IN (1, 2)`,
		`CREATE INDEX parted_v ON ` + sch + `.parted (v)`,
		`CREATE FOREIGN DATA WRAPPER lumino_regress_fdw`,
		`CREATE SERVER lumino_regress_srv FOREIGN DATA WRAPPER lumino_regress_fdw OPTIONS (host 'example.invalid')`,
		`CREATE FOREIGN TABLE ` + sch + `.ft (a int OPTIONS (column_name 'remote_a') NOT NULL, b text) SERVER lumino_regress_srv OPTIONS (table_name 'remote''t')`,
	} {
		pgrExec(t, s, q)
	}
	ctx := context.Background()
	ddl := func(name string, typ db.ObjectType) string {
		t.Helper()
		d, err := conn.Catalog().DDL(ctx, db.ObjectRef{Schema: sch, Name: name, Type: typ})
		if err != nil {
			t.Fatalf("DDL %s: %v", name, err)
		}
		return d
	}
	contains := func(d string, parts ...string) {
		t.Helper()
		for _, p := range parts {
			if !strings.Contains(d, p) {
				t.Errorf("DDL lacks %q:\n%s", p, d)
			}
		}
	}
	// round trip: drop, recreate from the DDL, same DDL again
	roundTrip := func(name, drop string) {
		t.Helper()
		before := ddl(name, db.ObjTable)
		pgrExec(t, s, drop)
		if err := dbtest.Load(ctx, conn, before); err != nil {
			t.Fatalf("recreate %s: %v\n%s", name, err, before)
		}
		if after := ddl(name, db.ObjTable); after != before {
			t.Errorf("%s DDL changed after round trip:\n%s\n---\n%s", name, before, after)
		}
	}

	d := ddl("ident", db.ObjTable)
	contains(d, `id integer GENERATED ALWAYS AS IDENTITY (START WITH 100 INCREMENT BY 5) NOT NULL`,
		`b bigint GENERATED BY DEFAULT AS IDENTITY NOT NULL`)
	roundTrip("ident", "DROP TABLE "+sch+".ident")

	d = ddl("ser", db.ObjTable)
	contains(d, "CREATE SEQUENCE "+sch+".ser_id_seq", "OWNED BY "+sch+".ser.id")
	if strings.Index(d, "CREATE SEQUENCE") > strings.Index(d, "CREATE TABLE") {
		t.Errorf("sequence created after the table:\n%s", d)
	}
	roundTrip("ser", "DROP TABLE "+sch+".ser")

	d = ddl("parted", db.ObjTable)
	contains(d, "PARTITION BY RANGE (d)", "PARTITION OF "+sch+".parted\n    FOR VALUES FROM ('2024-01-01') TO ('2025-01-01')",
		"PARTITION OF "+sch+".parted\n    DEFAULT\n    PARTITION BY LIST (id)", "PARTITION OF "+sch+".parted_rest\n    FOR VALUES IN (1, 2)")
	roundTrip("parted", "DROP TABLE "+sch+".parted")

	d = ddl("ft", db.ObjTable)
	contains(d, "CREATE FOREIGN TABLE "+sch+".ft", `a integer OPTIONS (column_name 'remote_a') NOT NULL`,
		"SERVER lumino_regress_srv OPTIONS (table_name 'remote''t')")
	roundTrip("ft", "DROP FOREIGN TABLE "+sch+".ft")

	// bug 4: identity sequences aren't user objects; serial ones are
	objs, err := conn.Catalog().Objects(ctx, db.ObjectRef{Schema: sch}, db.ObjSequence)
	if err != nil {
		t.Fatal(err)
	}
	var seqs []string
	for _, o := range objs {
		seqs = append(seqs, o.Ref.Name)
	}
	if want := []string{"ser_big_seq", "ser_id_seq"}; !reflect.DeepEqual(seqs, want) {
		t.Errorf("sequences %q, want %q", seqs, want)
	}
}

// bug 5: timetz values are edited as text.
func TestPostgresRegressTimeTZ(t *testing.T) {
	conn, s := pgrOpen(t, "postgres", nil)
	const sch = "lumino_regress"
	pgrSchema(t, s, sch)
	pgrExec(t, s, "CREATE TABLE "+sch+".tz (id int PRIMARY KEY, t timetz, t2 time with time zone)")
	ctx := context.Background()
	ref := db.ObjectRef{Schema: sch, Name: "tz", Type: db.ObjTable}
	ti, err := conn.Catalog().Table(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range ti.Columns[1:] {
		if _, err := db.ParseValue("11:00:00+03", c.Kind); err != nil {
			t.Errorf("column %s (%s, kind %v): %v", c.Name, c.Type, c.Kind, err)
		}
	}
	cols, _, err := dbtest.Query(ctx, s, "SELECT t FROM "+sch+".tz")
	if err != nil {
		t.Fatal(err)
	}
	if cols[0].Kind != ti.Columns[1].Kind {
		t.Errorf("result kind %v, catalog kind %v", cols[0].Kind, ti.Columns[1].Kind)
	}
	v, _ := db.ParseValue("11:00:00+03", ti.Columns[1].Kind)
	if _, err := db.ApplyChanges(ctx, s, conn.Caps(), conn.Dialect(), ref, []db.Change{{Op: db.OpInsert, Values: []db.ColValue{
		{Column: "id", Kind: db.KindInt, Value: int64(1)}, {Column: "t", Kind: ti.Columns[1].Kind, Value: v}}}}); err != nil {
		t.Fatal(err)
	}
	if got := pgrScalar(t, s, "SELECT t::text FROM "+sch+".tz"); got != "11:00:00+03" {
		t.Errorf("stored %q", got)
	}
}
