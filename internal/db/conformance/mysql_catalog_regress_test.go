//go:build integration

package conformance

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/db/dbtest"
	"github.com/twoojoo/lumino/internal/db/sqlx"
)

func TestMySQLCatalogRegress(t *testing.T)   { mcrRun(t, "mysql") }
func TestMariaDBCatalogRegress(t *testing.T) { mcrRun(t, "mariadb") }
func TestTiDBCatalogRegress(t *testing.T)    { mcrRun(t, "tidb") }

func mcrRun(t *testing.T, name string) {
	tg := dbtest.Targets()[name]
	if !dbtest.Reachable(tg.Config.Addr(0)) {
		t.Skipf("%s not reachable (make db-up)", name)
	}
	ctx := context.Background()
	conn, err := db.Open(ctx, &tg.Config, db.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	s := mysqlSession(t, conn)
	for _, q := range []string{"DROP DATABASE IF EXISTS mcr_cat", "DROP DATABASE IF EXISTS mcr_other",
		"CREATE DATABASE mcr_cat", "CREATE DATABASE mcr_other"} {
		mysqlExec(t, s, q)
	}
	t.Cleanup(func() {
		mysqlExec(t, s, "DROP DATABASE IF EXISTS mcr_cat")
		mysqlExec(t, s, "DROP DATABASE IF EXISTS mcr_other")
	})
	expr := ", e int DEFAULT (1+2)"
	hcol := " h varchar(20) DEFAULT 'CURRENT_TIMESTAMP',"
	wantI := "-1.50"
	if name == "tidb" {
		// TiDB: no expression defaults; SHOW CREATE TABLE prints a varchar
		// default 'CURRENT_TIMESTAMP' unquoted (server bug); decimals are
		// reported unpadded
		expr, hcol, wantI = "", "", "-1.5"
	}
	mysqlExec(t, s, `CREATE TABLE mcr_cat.t (id int PRIMARY KEY, a varchar(10) DEFAULT 'abc', b varchar(10) NOT NULL DEFAULT '',
  c timestamp NULL DEFAULT CURRENT_TIMESTAMP, d int DEFAULT 5,`+hcol+`
  j varchar(9) DEFAULT 'it''s', q varchar(5) DEFAULT 'a\\b', i decimal(5,2) DEFAULT -1.5, k varchar(3) DEFAULT NULL`+expr+`)`)
	if name != "tidb" {
		// unqualified, lower case: MariaDB keeps the original text
		mysqlExec(t, s, "USE mcr_cat")
		mysqlExec(t, s, "create trigger `my trg` before insert on t for each row set new.d = 7")
	}
	ref := db.ObjectRef{Schema: "mcr_cat", Name: "t", Type: db.ObjTable}
	ti, err := conn.Catalog().Table(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	defs := map[string]string{}
	for _, c := range ti.Columns {
		defs[c.Name] = c.Default
		if c.Name == "k" && c.HasDefault && c.Default != "" && c.Default != "NULL" {
			t.Errorf("k default = %q", c.Default)
		}
	}
	for col, want := range map[string]string{"a": "'abc'", "b": "''", "d": "5", "h": "'CURRENT_TIMESTAMP'", "j": "'it''s'", "i": wantI} {
		if hcol == "" && col == "h" {
			continue
		}
		if defs[col] != want {
			t.Errorf("default of %s = %q, want %q", col, defs[col], want)
		}
	}
	if !strings.HasPrefix(strings.ToUpper(defs["c"]), "CURRENT_TIMESTAMP") {
		t.Errorf("default of c = %q", defs["c"])
	}
	// every default evaluates to the value a plain INSERT stores
	mysqlExec(t, s, "INSERT INTO mcr_cat.t (id) VALUES (1)")
	for _, col := range []string{"a", "b", "h", "j", "q", "i"} {
		if hcol == "" && col == "h" {
			continue
		}
		if v := fmt.Sprint(mysqlScalar(t, s, "SELECT "+col+" = "+defs[col]+" FROM mcr_cat.t WHERE id = 1")); v != "1" && v != "true" {
			t.Errorf("stored %s differs from its default expression %s (%s)", col, defs[col], v)
		}
	}

	// DDL round trip from another current database
	ddl, err := conn.Catalog().DDL(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	mysqlExec(t, s, "DROP TABLE mcr_cat.t")
	mysqlExec(t, s, "USE mcr_other")
	for _, st := range sqlx.Split(ddl, conn.Dialect().Lex()) {
		mysqlExec(t, s, st.Text)
	}
	ti2, err := conn.Catalog().Table(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range ti2.Columns {
		if c.Default != ti.Columns[i].Default {
			t.Errorf("round trip default of %s: %q, was %q", c.Name, c.Default, ti.Columns[i].Default)
		}
	}
	if name != "tidb" {
		if len(ti2.Triggers) != 1 || ti2.Triggers[0].Name != "my trg" {
			t.Errorf("triggers after round trip: %+v\n%s", ti2.Triggers, ddl)
		}
		if n := fmt.Sprint(mysqlScalar(t, s, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'mcr_other'")); n != "0" {
			t.Errorf("DDL created %s tables in the current database:\n%s", n, ddl)
		}
	}
}
