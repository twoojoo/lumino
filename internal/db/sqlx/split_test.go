package sqlx

import (
	"reflect"
	"strings"
	"testing"
)

func texts(st []Statement) []string {
	out := make([]string, len(st))
	for i, s := range st {
		out[i] = s.Text
	}
	return out
}

var pgOpts = Options{DollarQuotes: true, NestedComments: true,
	BlockStart: func(w []string) bool { return hasSeq(w, "BEGIN", "ATOMIC") }}

var myOpts = Options{Backticks: true, HashComments: true, BackslashEscape: true, Delimiter: true, AtParams: true,
	BlockStart: func(w []string) bool {
		return len(w) >= 2 && w[0] == "CREATE" && anyOf(w[1:], "PROCEDURE", "FUNCTION", "TRIGGER", "EVENT")
	}}

var msOpts = Options{Brackets: true, AtParams: true, GoBatches: true, BatchBlocks: true,
	BlockStart: func(w []string) bool {
		return len(w) >= 2 && (w[0] == "CREATE" || w[0] == "ALTER") && anyOf(w[1:], "PROCEDURE", "PROC", "FUNCTION", "TRIGGER")
	}}

var oraOpts = Options{ColonParams: true, SlashLine: true, BatchBlocks: true, BlockSemicolon: true,
	BlockStart: func(w []string) bool {
		return w[0] == "BEGIN" || w[0] == "DECLARE" ||
			w[0] == "CREATE" && anyOf(w[1:], "PROCEDURE", "FUNCTION", "PACKAGE", "TRIGGER", "TYPE")
	}}

var fbOpts = Options{SetTerm: true, BodyRequired: true,
	BlockStart: func(w []string) bool {
		return w[0] == "EXECUTE" && len(w) > 1 && w[1] == "BLOCK" ||
			(w[0] == "CREATE" || w[0] == "RECREATE" || w[0] == "ALTER") && anyOf(w[1:], "PROCEDURE", "FUNCTION", "TRIGGER", "PACKAGE")
	}}

var sqliteOpts = Options{Backticks: true, Brackets: true,
	BlockStart: func(w []string) bool { return len(w) >= 2 && w[0] == "CREATE" && anyOf(w[1:], "TRIGGER") }}

func hasSeq(w []string, a, b string) bool {
	for i := 0; i+1 < len(w); i++ {
		if w[i] == a && w[i+1] == b {
			return true
		}
	}
	return false
}

func anyOf(w []string, set ...string) bool {
	for _, x := range w {
		for _, s := range set {
			if x == s {
				return true
			}
		}
	}
	return false
}

func TestSplitBasic(t *testing.T) {
	got := texts(Split("select 1; select ';' as x;\n\n-- comment\nselect 2", &pgOpts))
	want := []string{"select 1", "select ';' as x", "select 2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
}

func TestSplitOffsets(t *testing.T) {
	s := "  select 1;\n  select 2  ;"
	st := Split(s, &pgOpts)
	if len(st) != 2 {
		t.Fatal(st)
	}
	for _, x := range st {
		if s[x.Start:x.End] != x.Text {
			t.Fatalf("offset mismatch %q vs %q", s[x.Start:x.End], x.Text)
		}
	}
}

func TestSplitDollar(t *testing.T) {
	s := `create function f() returns int as $body$ begin; return 1; end $body$ language plpgsql;
select $$a;b$$; select $1::int;`
	got := texts(Split(s, &pgOpts))
	if len(got) != 3 || !strings.HasSuffix(got[0], "plpgsql") || got[1] != "select $$a;b$$" {
		t.Fatalf("got %q", got)
	}
}

func TestSplitNestedComment(t *testing.T) {
	got := texts(Split("select /* a /* b; */ c; */ 1; select 2", &pgOpts))
	if len(got) != 2 {
		t.Fatalf("got %q", got)
	}
}

func TestSplitBeginAtomic(t *testing.T) {
	s := "create function f() returns int language sql begin atomic select 1; select 2; end; select 3;"
	got := texts(Split(s, &pgOpts))
	if len(got) != 2 || got[1] != "select 3" {
		t.Fatalf("got %q", got)
	}
}

func TestSplitMySQLDelimiter(t *testing.T) {
	s := "DELIMITER //\nCREATE PROCEDURE p() BEGIN SELECT 1; SELECT 2; END//\nDELIMITER ;\nSELECT 'a\\';b'; # c;\nselect 3"
	got := texts(Split(s, &myOpts))
	want := []string{"CREATE PROCEDURE p() BEGIN SELECT 1; SELECT 2; END", "SELECT 'a\\';b'", "select 3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
}

func TestSplitMySQLBlockWithoutDelimiter(t *testing.T) {
	s := `CREATE PROCEDURE p(IN x INT)
BEGIN
  IF x > 0 THEN SELECT 1; ELSE SELECT 2; END IF;
  CASE x WHEN 1 THEN SELECT 3; END CASE;
  lbl: LOOP LEAVE lbl; END LOOP lbl;
END;
SELECT 4;`
	got := texts(Split(s, &myOpts))
	if len(got) != 2 || got[1] != "SELECT 4" || !strings.HasSuffix(got[0], "END") {
		t.Fatalf("got %q", got)
	}
}

func TestSplitGo(t *testing.T) {
	s := "CREATE PROCEDURE p AS\nSELECT 1;\nSELECT 2;\nGO\nselect [a;b] from t;\nselect 2\ngo 2\n"
	got := texts(Split(s, &msOpts))
	want := []string{"CREATE PROCEDURE p AS\nSELECT 1;\nSELECT 2;", "select [a;b] from t", "select 2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
}

func TestSplitOracle(t *testing.T) {
	s := "select 1 from dual;\nBEGIN\n  null;\nEND;\n/\ncreate or replace procedure p as begin null; end;\n/\nselect :x from dual"
	got := texts(Split(s, &oraOpts))
	want := []string{"select 1 from dual", "BEGIN\n  null;\nEND;", "create or replace procedure p as begin null; end;", "select :x from dual"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
}

func TestSplitOracleNoSlash(t *testing.T) {
	// without a "/" line the block runs to the end of the script
	got := texts(Split("begin null; end;", &oraOpts))
	if len(got) != 1 || got[0] != "begin null; end;" {
		t.Fatalf("got %q", got)
	}
}

func TestSplitFirebirdSetTerm(t *testing.T) {
	s := "SET TERM ^ ;\nCREATE PROCEDURE P AS BEGIN EXIT; END^\nSET TERM ; ^\nSELECT 1 FROM RDB$DATABASE;"
	got := texts(Split(s, &fbOpts))
	want := []string{"CREATE PROCEDURE P AS BEGIN EXIT; END", "SELECT 1 FROM RDB$DATABASE"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
}

func TestSplitFirebirdBlockDepth(t *testing.T) {
	s := "EXECUTE BLOCK AS DECLARE x INT; BEGIN x = 1; END;\nSELECT 1 FROM RDB$DATABASE;"
	got := texts(Split(s, &fbOpts))
	if len(got) != 2 {
		t.Fatalf("got %q", got)
	}
}

func TestSplitSQLiteTrigger(t *testing.T) {
	s := "CREATE TRIGGER t AFTER INSERT ON a BEGIN INSERT INTO b VALUES (1); UPDATE c SET x=1; END; select 1;"
	got := texts(Split(s, &sqliteOpts))
	if len(got) != 2 || got[1] != "select 1" {
		t.Fatalf("got %q", got)
	}
}

func TestSplitUnterminatedString(t *testing.T) {
	got := texts(Split("select 'abc; select 2", &pgOpts))
	if len(got) != 1 {
		t.Fatalf("got %q", got)
	}
}

func TestStatementAt(t *testing.T) {
	s := "select 1;\nselect 2;   \n\nselect 3"
	st := Split(s, &pgOpts)
	cases := map[int]int{0: 0, 9: 0, 10: 1, 20: 1, 22: 1, 23: 2, len(s): 2}
	for p, want := range cases {
		if got := StatementAt(st, s, p); got != want {
			t.Errorf("at %d: got %d want %d", p, got, want)
		}
	}
}

func TestLexLineState(t *testing.T) {
	o := &pgOpts
	toks, st := LexLine("select 'abc", State{}, o)
	if st.InCode() || toks[len(toks)-1].Kind != TString {
		t.Fatal("expected open string")
	}
	toks, st = LexLine("def' , /* x", st, o)
	if toks[0].Kind != TString || toks[0].End != 4 || st.InCode() {
		t.Fatalf("toks %v", toks)
	}
	_, st = LexLine("*/ select", st, o)
	if !st.InCode() {
		t.Fatal("comment should close")
	}
	_, st = LexLine("select $f$ x", State{}, o)
	toks, st = LexLine("y $f$ z", st, o)
	if !st.InCode() || toks[0].Kind != TString {
		t.Fatalf("dollar %v", toks)
	}
}

func TestReturnsRows(t *testing.T) {
	cases := map[string]bool{
		"select 1":                              true,
		"  (select 1)":                          true,
		"-- c\nWITH x AS (select 1) select *":   true,
		"insert into t values (1)":              false,
		"insert into t values (1) returning id": true,
		"update t set a=1":                      false,
		"create table x (a int)":                false,
		"show tables":                           true,
		"DELETE FROM t OUTPUT deleted.*":        true,
	}
	for s, want := range cases {
		if got := ReturnsRows(s, &pgOpts); got != want {
			t.Errorf("%q: got %v", s, got)
		}
	}
}

func TestSplitCQLBatch(t *testing.T) {
	o := &Options{SlashComments: true, ApplyEndsBlock: true, BlockStart: func(w []string) bool { return w[0] == "BEGIN" }}
	s := "BEGIN BATCH\nINSERT INTO t (a) VALUES (1);\nINSERT INTO t (a) VALUES (2);\nAPPLY BATCH;\n// c\nSELECT * FROM t;"
	got := texts(Split(s, o))
	if len(got) != 2 || !strings.HasSuffix(got[0], "APPLY BATCH") || got[1] != "SELECT * FROM t" {
		t.Fatalf("got %q", got)
	}
}

func TestReturnsRowsClause(t *testing.T) {
	o := &Options{}
	cases := map[string]bool{
		"UPDATE t SET output = $1 WHERE id = $2":            false,
		"UPDATE t SET a = 1, returning = 2 WHERE id = 1":    false,
		"INSERT INTO t (a, output) VALUES (1, 2)":           false,
		"INSERT INTO t VALUES (1) RETURNING id":             true,
		"UPDATE t SET a = 1 OUTPUT inserted.a WHERE id = 1": true,
		"DELETE FROM t OUTPUT deleted.* WHERE id = 1":       true,
		"DELETE FROM t WHERE x IN (SELECT 1) RETURNING *":   true,
	}
	for q, want := range cases {
		if got := ReturnsRows(q, o); got != want {
			t.Errorf("%q: got %v", q, got)
		}
	}
}

func TestSplitReviewCases(t *testing.T) {
	my := &Options{Backticks: true, HashComments: true, BackslashEscape: true, DoubleQuoteStrings: true, Delimiter: true, DashSpace: true}
	script := "DELIMITER $$\nCREATE PROCEDURE p1() BEGIN SELECT 1; END$$\nCREATE PROCEDURE p2() BEGIN SELECT 2; END$$\nDELIMITER ;\nSELECT 3;"
	if got := Split(script, my); len(got) != 3 || !strings.HasSuffix(got[1].Text, "END") || got[2].Text != "SELECT 3" {
		t.Errorf("END$$: %+v", got)
	}
	if got := Split("SELECT 5--3; SELECT 4", my); len(got) != 2 {
		t.Errorf("MySQL --3 is not a comment: %+v", got)
	}
	if got := Split("SELECT 1 -- c; x\n; SELECT 2", my); len(got) != 2 {
		t.Errorf("MySQL '-- ' comment: %+v", got)
	}
	pg := &Options{DollarQuotes: true, NestedComments: true}
	if got := Split("SELECT E'a\nb\\'c'; SELECT 2; SELECT 3", pg); len(got) != 3 {
		t.Errorf("multi-line E string: %+v", got)
	}
}
