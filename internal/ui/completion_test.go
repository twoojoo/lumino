package ui

import (
	"testing"

	"github.com/giovanni/lumino/internal/db"
	"github.com/giovanni/lumino/internal/db/sqlx"
)

func TestAnalyzeSQL(t *testing.T) {
	o := &sqlx.Options{}
	kw := db.CommonKeywords
	cases := []struct {
		sql    string
		qual   string
		prefix string
		expect bool
		refs   []sqlRef
	}{
		{"SELECT u.na FROM users u", "u", "na", false, []sqlRef{{name: "users", alias: "u"}}},
		{"SELECT * FROM pub", "", "pub", true, []sqlRef{{name: "pub"}}},
		{"SELECT * FROM public.users AS x JOIN orders o ON o.uid = x.", "x", "", false,
			[]sqlRef{{schema: "public", name: "users", alias: "x"}, {name: "orders", alias: "o"}}},
		{"SELECT * FROM a, b WHERE ", "", "", false, []sqlRef{{name: "a"}, {name: "b"}}},
		{"SELECT * FROM a, ", "", "", true, []sqlRef{{name: "a"}}},
		{`SELECT "T".c FROM "My Table" "T"`, "T", "c", false, []sqlRef{{name: "My Table", alias: "T"}}},
		{"UPDATE accounts SET bal", "", "bal", false, []sqlRef{{name: "accounts"}}},
	}
	for _, c := range cases {
		caret := len(c.sql)
		if c.sql[:9] == "SELECT u." || c.sql[:9] == `SELECT "T` {
			caret = len(c.sql[:indexOf(c.sql, " FROM")])
		}
		ctx := analyzeSQL(c.sql, caret, o, kw)
		if ctx.qualifier != c.qual || ctx.prefix != c.prefix || ctx.expectTable != c.expect {
			t.Errorf("%q: qual %q prefix %q expect %v", c.sql, ctx.qualifier, ctx.prefix, ctx.expectTable)
		}
		if len(ctx.refs) != len(c.refs) {
			t.Errorf("%q: refs %+v", c.sql, ctx.refs)
			continue
		}
		for i := range c.refs {
			if ctx.refs[i] != c.refs[i] {
				t.Errorf("%q: ref %d = %+v want %+v", c.sql, i, ctx.refs[i], c.refs[i])
			}
		}
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestFilterItems(t *testing.T) {
	items := []compItem{{label: "users", kind: "table"}, {label: "user_id", kind: "column"}, {label: "UPDATE", kind: "keyword"}, {label: "status", kind: "column"}}
	got := filterItems(items, "us")
	if len(got) != 3 || got[0].label != "user_id" {
		t.Fatalf("%+v", got)
	}
	if got := filterItems(items, "sts"); len(got) != 1 || got[0].label != "status" {
		t.Fatalf("subsequence: %+v", got)
	}
}
