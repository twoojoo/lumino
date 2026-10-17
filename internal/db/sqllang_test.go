package db

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/twoojoo/lumino/internal/db/sqlx"
)

func pgLang() *SQLLanguage {
	return NewSQLLanguage(&BaseDialect{ID: "postgres", QuoteL: `"`, QuoteR: `"`, Fold: FoldLower, Params: ParamDollar,
		LexOpts: sqlx.Options{DollarQuotes: true}, Reserved: WordSet("SELECT FROM WHERE ORDER USER")})
}

func TestSQLLanguageBrowseCount(t *testing.T) {
	l := pgLang()
	ref := ObjectRef{Schema: "public", Name: "Users", Type: ObjTable}
	q, err := l.Browse(ref, BrowseQuery{Filter: "id > 1", Sort: []SortKey{{Field: "id"}, {Field: "order", Desc: true}}})
	if err != nil || q != `SELECT * FROM public."Users" WHERE id > 1 ORDER BY id ASC, "order" DESC` {
		t.Fatalf("browse: %q %v", q, err)
	}
	if q, _ := l.Browse(ref, BrowseQuery{}); q != `SELECT * FROM public."Users"` {
		t.Fatalf("browse all: %q", q)
	}
	if q, _ := l.Count(ref, " "); q != `SELECT COUNT(*) FROM public."Users"` {
		t.Fatalf("count: %q", q)
	}
	if q, _ := l.Count(ref, "a = 1"); q != `SELECT COUNT(*) FROM public."Users" WHERE a = 1` {
		t.Fatalf("count filter: %q", q)
	}
}

func TestSQLLanguageFilters(t *testing.T) {
	l := pgLang()
	for _, ok := range []string{"", "id > 10 AND name LIKE 'a;%'", "x IN (SELECT 1)"} {
		if err := l.ValidateFilter(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"1=1; DELETE FROM t", "1=1; SELECT 2", "id = (DELETE FROM t RETURNING 1)"} {
		if err := l.ValidateFilter(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if got := l.EqualsFilter("user", nil, KindText); got != `"user" IS NULL` {
		t.Errorf("null: %q", got)
	}
	if got := l.EqualsFilter("name", "o'k", KindText); got != `name = 'o''k'` {
		t.Errorf("text: %q", got)
	}
	if l.SortUnsupported() != "" || l.FilterHint() == "" || l.LineComment() != "--" || l.Fence() != "sql" || l.Display() != "SQL" {
		t.Error("defaults")
	}
}

func TestSQLLanguageStatements(t *testing.T) {
	l := pgLang()
	if k := l.Classify("DROP TABLE t"); k != StmtDestructive {
		t.Errorf("classify: %v", k)
	}
	if st := l.Split("SELECT 1; SELECT $$a;b$$"); len(st) != 2 {
		t.Errorf("split: %+v", st)
	}
	toks, st := l.LexLine("SELECT 'abc", nil)
	if len(toks) == 0 || st == (sqlx.State{}) {
		t.Fatalf("lex: %v %+v", toks, st)
	}
	if toks, _ := l.LexLine("def'", st); len(toks) == 0 || toks[0].Kind != sqlx.TString {
		t.Fatalf("continued string: %+v", toks)
	}
	ref := ObjectRef{Name: "t", Type: ObjTable}
	if got := l.OpenTemplate(ref); got != "SELECT * FROM t;" {
		t.Errorf("open: %q", got)
	}
	if got := l.InsertText(ref, []Column{{Name: "a", Kind: KindInt}, {Name: "b", Kind: KindText}}, Row{int64(1), nil}); got != "INSERT INTO t (a, b) VALUES (1, NULL);" {
		t.Errorf("insert: %q", got)
	}
	ch := Change{Op: OpUpdate, Key: []ColValue{{Column: "id", Kind: KindInt, Value: int64(3)}}, Values: []ColValue{{Column: "b", Kind: KindText, Value: "x"}}}
	if got := l.RenderChange(ref, ch); got != "UPDATE t SET b = 'x' WHERE id = 3" {
		t.Errorf("change: %q", got)
	}
	if q, err := l.Explain("SELECT 1;"); err != nil || q != "EXPLAIN SELECT 1" {
		t.Errorf("explain: %q %v", q, err)
	}
	if _, err := NewSQLLanguage(&BaseDialect{ID: "oracle"}).Explain("SELECT 1"); !errors.Is(err, ErrNotSupported) {
		t.Errorf("oracle explain: %v", err)
	}
	if q, _ := NewSQLLanguage(&BaseDialect{ID: "sqlite"}).Explain("SELECT 1"); q != "EXPLAIN QUERY PLAN SELECT 1" {
		t.Errorf("sqlite explain: %q", q)
	}
	names := l.ColumnNames(`SELECT u.password, "token" FROM secrets JOIN auth_user ON 1=1`)
	if !slices.Contains(names, "password") || !slices.Contains(names, "token") || slices.Contains(names, "secrets") ||
		slices.Contains(names, "auth_user") || slices.Contains(names, "u") {
		t.Errorf("column names: %v", names)
	}
	if c := l.CompletionContext("SELECT * FROM pub", 17); !c.ExpectObject || c.Prefix != "pub" {
		t.Errorf("completion: %+v", c)
	}
}

// fakeConn is a connection whose language is not SQL.
type fakeConn struct{ Conn }

func (fakeConn) Language() Language { return nil }

type applierSession struct {
	Session
	got []Change
}

func (s *applierSession) ApplyChanges(_ context.Context, _ ObjectRef, chs []Change) ([]ApplyResult, error) {
	s.got = chs
	return []ApplyResult{{Affected: 1, SQL: "native"}}, nil
}

func TestApplyDispatch(t *testing.T) {
	if DialectOf(fakeConn{}) != nil {
		t.Fatal("non-SQL language has a dialect")
	}
	if d, ok := SQLDialect(pgLang()); !ok || d.Name() != "postgres" {
		t.Fatal("SQLDialect")
	}
	s := &applierSession{}
	res, err := Apply(context.Background(), s, fakeConn{}, ObjectRef{Name: "c"}, []Change{{Op: OpDelete}})
	if err != nil || len(res) != 1 || res[0].SQL != "native" || len(s.got) != 1 {
		t.Fatalf("native apply: %v %v", res, err)
	}
	if !ObjCollection.Relation() || !ObjCollection.Writable() || ObjView.Writable() || !strings.EqualFold(ObjCollection.Plural(), "collections") {
		t.Fatal("collection type")
	}
}
