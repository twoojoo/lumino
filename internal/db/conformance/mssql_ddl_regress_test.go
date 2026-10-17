//go:build integration

package conformance

import (
	"context"
	"strings"
	"testing"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/db/dbtest"
)

// mdrRoundTrip checks the DDL contains every fragment, drops the table,
// recreates it from the DDL and expects the same DDL back.
func mdrRoundTrip(t *testing.T, conn db.Conn, s db.Session, name string, want, avoid []string) string {
	t.Helper()
	ctx := context.Background()
	ref := db.ObjectRef{Schema: "lumino_regress", Name: name, Type: db.ObjTable}
	before, err := conn.Catalog().DDL(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range want {
		if !strings.Contains(before, p) {
			t.Errorf("DDL lacks %q:\n%s", p, before)
		}
	}
	for _, p := range avoid {
		if strings.Contains(before, p) {
			t.Errorf("DDL contains %q:\n%s", p, before)
		}
	}
	pgrExec(t, s, "DROP TABLE lumino_regress."+name)
	if err := dbtest.Load(ctx, conn, before); err != nil {
		t.Fatalf("recreate: %v\n%s", err, before)
	}
	after, err := conn.Catalog().DDL(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("DDL changed after round trip:\n%s\n---\n%s", before, after)
	}
	return before
}

// named default constraints are kept, system-named ones are not.
func TestMSSQLDDLRegressDefaults(t *testing.T) {
	conn, s := msrOpen(t, "")
	msrSchema(t, s, "dflt")
	pgrExec(t, s, `CREATE TABLE lumino_regress.dflt (
		id int NOT NULL PRIMARY KEY,
		a int NOT NULL CONSTRAINT df_dflt_a DEFAULT 7,
		b nvarchar(10) NULL DEFAULT N'x')`)
	ddl := mdrRoundTrip(t, conn, s, "dflt", []string{"a int NOT NULL CONSTRAINT df_dflt_a DEFAULT ((7))", "b nvarchar(10) NULL DEFAULT (N'x')"}, nil)
	if strings.Count(ddl, "CONSTRAINT DF__") > 0 {
		t.Errorf("system-named default scripted:\n%s", ddl)
	}
}

// columnstore, XML and spatial indexes.
func TestMSSQLDDLRegressIndexKinds(t *testing.T) {
	conn, s := msrOpen(t, "")
	msrSchema(t, s, "ccs", "ixk")
	pgrExec(t, s, "CREATE TABLE lumino_regress.ccs (a int NOT NULL, b int NULL)")
	pgrExec(t, s, "CREATE CLUSTERED COLUMNSTORE INDEX cci_ccs ON lumino_regress.ccs")
	mdrRoundTrip(t, conn, s, "ccs", []string{"CREATE CLUSTERED COLUMNSTORE INDEX cci_ccs ON lumino_regress.ccs;"}, nil)

	pgrExec(t, s, `CREATE TABLE lumino_regress.ixk (id int NOT NULL CONSTRAINT pk_ixk PRIMARY KEY CLUSTERED,
		a int NULL, b int NULL, doc xml NULL, g geometry NULL, geo geography NULL)`)
	pgrExec(t, s, "CREATE NONCLUSTERED COLUMNSTORE INDEX ncci_ixk ON lumino_regress.ixk (a, b)")
	pgrExec(t, s, "CREATE PRIMARY XML INDEX pxml_ixk ON lumino_regress.ixk (doc)")
	pgrExec(t, s, "CREATE XML INDEX axml_ixk ON lumino_regress.ixk (doc) USING XML INDEX pxml_ixk FOR PATH")
	pgrExec(t, s, "CREATE SPATIAL INDEX sp_ixk ON lumino_regress.ixk (g) USING GEOMETRY_GRID WITH (BOUNDING_BOX = (0, 0, 100, 50), GRIDS = (LOW, MEDIUM, HIGH, LOW), CELLS_PER_OBJECT = 32)")
	pgrExec(t, s, "CREATE SPATIAL INDEX spg_ixk ON lumino_regress.ixk (geo) USING GEOGRAPHY_AUTO_GRID")
	ddl := mdrRoundTrip(t, conn, s, "ixk", []string{
		"CREATE NONCLUSTERED COLUMNSTORE INDEX ncci_ixk ON lumino_regress.ixk (a, b);",
		"CREATE PRIMARY XML INDEX pxml_ixk ON lumino_regress.ixk (doc);",
		"CREATE XML INDEX axml_ixk ON lumino_regress.ixk (doc) USING XML INDEX pxml_ixk FOR PATH;",
		"CREATE SPATIAL INDEX sp_ixk ON lumino_regress.ixk (g) USING GEOMETRY_GRID WITH (BOUNDING_BOX = (0, 0, 100, 50), GRIDS = (LEVEL_1 = LOW, LEVEL_2 = MEDIUM, LEVEL_3 = HIGH, LEVEL_4 = LOW), CELLS_PER_OBJECT = 32);",
		"CREATE SPATIAL INDEX spg_ixk ON lumino_regress.ixk (geo) USING GEOGRAPHY_AUTO_GRID WITH (CELLS_PER_OBJECT = 12);",
	}, []string{"not scripted"})
	if strings.Index(ddl, "PRIMARY XML") > strings.Index(ddl, "USING XML INDEX") {
		t.Errorf("secondary XML index before the primary:\n%s", ddl)
	}
}
