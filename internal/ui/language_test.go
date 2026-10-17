package ui

import (
	"strings"
	"testing"

	"github.com/twoojoo/lumino/internal/db"
	"github.com/twoojoo/lumino/internal/db/sqlx"
)

// docLang is a non-SQL language (a document database shell): the editor,
// completion and exports must follow it instead of SQL rules.
type docLang struct{ db.Language }

func (docLang) Name() string        { return "doc" }
func (docLang) LineComment() string { return "//" }
func (docLang) QuoteIdent(s string) string {
	if strings.ContainsAny(s, " .") {
		return `["` + s + `"]`
	}
	return s
}

// LexLine marks every line as one string token and counts lines in the
// state, proving the state is carried from line to line.
func (docLang) LexLine(line string, st db.LexState) ([]sqlx.Token, db.LexState) {
	n, _ := st.(int)
	return []sqlx.Token{{Kind: sqlx.TString, Start: 0, End: len(line)}}, n + 1
}

func (docLang) InsertText(ref db.ObjectRef, cols []db.Column, row db.Row) string {
	return "db." + ref.Name + ".insertOne({" + cols[0].Name + ": " + db.FormatValue(row[0], cols[0].Kind) + "})"
}

func (docLang) InsertFormat() (string, string) { return "insertOne", ".js" }

func newDocLangEditor(txt string) *CodeEditor {
	e := regressEditor(txt)
	e.setLanguage(docLang{db.GenericSQL})
	return e
}

func TestLanguageEditorFollowsLanguage(t *testing.T) {
	e := newDocLangEditor("a\nb")
	e.SelectRange(0, 3)
	e.toggleComment()
	if e.Text() != "// a\n// b" {
		t.Fatalf("comment: %q", e.Text())
	}
	e.toggleComment()
	if e.Text() != "a\nb" {
		t.Fatalf("uncomment: %q", e.Text())
	}
	if toks := e.tokens(1); len(toks) != 1 || toks[0].Kind != sqlx.TString {
		t.Fatalf("tokens: %+v", toks)
	}
	if e.hl[1].start != 1 || e.hl[1].end != 2 {
		t.Fatalf("state not carried: %+v", e.hl[1])
	}
	// back to SQL: "--" comments
	e.setLanguage(nil)
	e.SelectRange(0, 0)
	e.toggleComment()
	if !strings.HasPrefix(e.Text(), "-- a") {
		t.Fatalf("generic SQL comment: %q", e.Text())
	}
}

func TestLanguageCompletionQuotes(t *testing.T) {
	e := newDocLangEditor("db.my")
	e.Completer = func(*CodeEditor) *compRequest {
		return &compRequest{items: []compItem{{label: "my coll", kind: "table"}}}
	}
	e.triggerCompletion(true)
	if e.comp == nil {
		t.Fatal("no popup")
	}
	e.acceptCompletion()
	if got := e.Text(); got != `db.["my coll"]` {
		t.Fatalf("accept: %q", got)
	}
}

func TestLanguageExportInsert(t *testing.T) {
	lang := docLang{db.GenericSQL}
	cols := []db.Column{{Name: "n", Kind: db.KindInt}}
	var b strings.Builder
	if err := writeRows(&b, "insert", cols, [][]any{{int64(1)}, {int64(2)}}, lang, db.ObjectRef{Name: "c"}, true); err != nil {
		t.Fatal(err)
	}
	if want := "db.c.insertOne({n: 1})\ndb.c.insertOne({n: 2})\n"; b.String() != want {
		t.Fatalf("insert:\n%s", b.String())
	}
	f := exportFormatsFor(lang)
	found := false
	for _, x := range f {
		found = found || x == [3]string{"insert", "insertOne", ".js"}
	}
	if !found || exportFormats[3][1] != "SQL INSERT" || insertLabel(nil) != "SQL INSERT" {
		t.Fatalf("formats: %v", f)
	}
	b.Reset()
	if err := writeRows(&b, "insert", cols, [][]any{{int64(1)}}, nil, db.ObjectRef{Name: "t"}, true); err != nil || b.String() != "INSERT INTO t (n) VALUES (1);\n" {
		t.Fatalf("generic SQL insert: %q %v", b.String(), err)
	}
}
