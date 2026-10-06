package ui

import (
	"testing"

	"github.com/giovanni/lumino/internal/db/sqlx"
)

func TestJSONLexLine(t *testing.T) {
	line := `  "na\"me" : "Ada", "n": -1.5e3, "ok": true, "x": null, "é": "ü"}`
	var got []string
	var kinds []sqlx.TokKind
	for _, tk := range jsonLexLine(line) {
		if tk.Kind == sqlx.TSpace {
			continue
		}
		got = append(got, line[tk.Start:tk.End])
		kinds = append(kinds, tk.Kind)
	}
	want := []string{`"na\"me"`, ":", `"Ada"`, ",", `"n"`, ":", "-1.5e3", ",", `"ok"`, ":", "true", ",", `"x"`, ":", "null", ",", `"é"`, ":", `"ü"`, "}"}
	if len(got) != len(want) {
		t.Fatalf("tokens %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("token %d: %q want %q (all %q)", i, got[i], want[i], got)
		}
	}
	if kinds[0] != sqlx.TQuotedIdent || kinds[2] != sqlx.TString || kinds[6] != sqlx.TNumber || kinds[10] != sqlx.TWord {
		t.Fatalf("kinds %v", kinds)
	}
	// unterminated string and junk don't loop or panic
	for _, s := range []string{`"abc`, `"a\`, `{ foo: bar }`, "\xff\xfe", ""} {
		end := 0
		for _, tk := range jsonLexLine(s) {
			if tk.Start != end || tk.End <= tk.Start {
				t.Fatalf("%q: bad token %+v", s, tk)
			}
			end = tk.End
		}
		if end != len(s) {
			t.Fatalf("%q: tokens cover %d of %d bytes", s, end, len(s))
		}
	}
}
