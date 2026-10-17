//go:build integration

package conformance

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/db/dbtest"
)

func ocrOpen(t *testing.T) (db.Conn, db.Session) {
	t.Helper()
	needs(t, "127.0.0.1:55521")
	tg := dbtest.Targets()["oracle"]
	cfg := tg.Config
	conn, err := db.Open(context.Background(), &cfg, db.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	s, err := conn.Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return conn, s
}

func ocrDrop(s db.Session, stmt string) {
	_, _, _ = dbtest.Query(context.Background(), s, "BEGIN EXECUTE IMMEDIATE '"+stmt+"'; EXCEPTION WHEN OTHERS THEN NULL; END;")
}

var ocrSlashLine = regexp.MustCompile(`(?m)^/\s*$`)

// ocrCheckSlashes: no doubled "/" terminators (two "/" lines with only
// blank lines between them).
func ocrCheckSlashes(t *testing.T, ddl string) {
	t.Helper()
	if regexp.MustCompile(`(?m)^/\s*\n(\s*\n)*/\s*$`).MatchString(ddl) {
		t.Errorf("doubled / terminator:\n%s", ddl)
	}
}

// bug 1: package DDL printed the body twice.
func TestOracleDDLRegressPackage(t *testing.T) {
	conn, s := ocrOpen(t)
	ctx := context.Background()
	ocrDrop(s, "DROP PACKAGE ocr_pkg")
	t.Cleanup(func() { ocrDrop(s, "DROP PACKAGE ocr_pkg") })
	pgrExec(t, s, "CREATE OR REPLACE PACKAGE ocr_pkg AS FUNCTION twice(n NUMBER) RETURN NUMBER; END ocr_pkg;")
	pgrExec(t, s, "CREATE OR REPLACE PACKAGE BODY ocr_pkg AS FUNCTION twice(n NUMBER) RETURN NUMBER IS BEGIN RETURN n * 2; END; END ocr_pkg;")
	ref := db.ObjectRef{Schema: "LUMINO", Name: "OCR_PKG", Type: db.ObjPackage}
	before, err := conn.Catalog().DDL(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(strings.ToUpper(before), "PACKAGE BODY"); n != 1 {
		t.Errorf("%d package bodies:\n%s", n, before)
	}
	if n := len(ocrSlashLine.FindAllString(before, -1)); n != 2 {
		t.Errorf("%d / lines, want 2:\n%s", n, before)
	}
	ocrCheckSlashes(t, before)
	pgrExec(t, s, "DROP PACKAGE ocr_pkg")
	if err := dbtest.Load(ctx, conn, before); err != nil {
		t.Fatalf("recreate: %v\n%s", err, before)
	}
	if got := pgrScalar(t, s, "SELECT ocr_pkg.twice(21) FROM dual"); got != "42" {
		t.Errorf("twice(21) = %q", got)
	}
	if st := pgrScalar(t, s, "SELECT COUNT(*) FROM user_objects WHERE object_name = 'OCR_PKG' AND status = 'VALID'"); st != "2" {
		t.Errorf("%s valid package objects", st)
	}
	after, err := conn.Catalog().DDL(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	// DBMS_METADATA doesn't list triggers in a stable order: compare the
	// blocks as a set
	if ocrBlocks(after) != ocrBlocks(before) {
		t.Errorf("DDL changed after round trip:\n%s\n---\n%s", before, after)
	}
}

// bug 2: trigger DDL had a doubled "/" and only the first trigger was fixed.
func TestOracleDDLRegressTriggers(t *testing.T) {
	conn, s := ocrOpen(t)
	ctx := context.Background()
	ocrDrop(s, "DROP TABLE ocr_trg PURGE")
	t.Cleanup(func() { ocrDrop(s, "DROP TABLE ocr_trg PURGE") })
	pgrExec(t, s, "CREATE TABLE ocr_trg (id NUMBER PRIMARY KEY, v NUMBER, w NUMBER)")
	pgrExec(t, s, "CREATE OR REPLACE TRIGGER ocr_trg_a BEFORE INSERT ON ocr_trg FOR EACH ROW BEGIN :NEW.v := NVL(:NEW.v, 0) + 1; END;")
	pgrExec(t, s, "CREATE OR REPLACE TRIGGER ocr_trg_b BEFORE INSERT ON ocr_trg FOR EACH ROW BEGIN :NEW.w := 5; END;")
	pgrExec(t, s, "ALTER TRIGGER ocr_trg_b DISABLE")
	ref := db.ObjectRef{Schema: "LUMINO", Name: "OCR_TRG", Type: db.ObjTable}
	before, err := conn.Catalog().DDL(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	ocrCheckSlashes(t, before)
	if n := len(ocrSlashLine.FindAllString(before, -1)); n != 2 {
		t.Errorf("%d / lines, want 2:\n%s", n, before)
	}
	pgrExec(t, s, "DROP TABLE ocr_trg PURGE")
	if err := dbtest.Load(ctx, conn, before); err != nil {
		t.Fatalf("recreate: %v\n%s", err, before)
	}
	if got := pgrScalar(t, s, "SELECT LISTAGG(trigger_name || ':' || status, ',') WITHIN GROUP (ORDER BY trigger_name) FROM user_triggers WHERE table_name = 'OCR_TRG'"); got != "OCR_TRG_A:ENABLED,OCR_TRG_B:DISABLED" {
		t.Errorf("triggers %q", got)
	}
	pgrExec(t, s, "INSERT INTO ocr_trg (id) VALUES (1)")
	if got := pgrScalar(t, s, "SELECT v || '/' || NVL(TO_CHAR(w), '-') FROM ocr_trg"); got != "1/-" {
		t.Errorf("row %q", got)
	}
	after, err := conn.Catalog().DDL(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	// DBMS_METADATA doesn't list triggers in a stable order: compare the
	// blocks as a set
	if ocrBlocks(after) != ocrBlocks(before) {
		t.Errorf("DDL changed after round trip:\n%s\n---\n%s", before, after)
	}
}

// ocrBlocks splits a DDL script at its CREATE statements and returns the
// blocks sorted, joined.
func ocrBlocks(ddl string) string {
	parts := strings.Split(ddl, "CREATE ")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	sort.Strings(parts)
	return strings.Join(parts, "\n|\n")
}
