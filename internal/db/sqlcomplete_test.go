package db

import (
	"strings"
	"testing"

	"github.com/twoojoo/lumino/internal/db/sqlx"
)

func TestAnalyzeSQL(t *testing.T) {
	o := &sqlx.Options{}
	kw := CommonKeywords
	cases := []struct {
		sql    string
		qual   string
		prefix string
		expect bool
		refs   []CompletionRef
	}{
		{"SELECT u.na FROM users u", "u", "na", false, []CompletionRef{{Name: "users", Alias: "u"}}},
		{"SELECT * FROM pub", "", "pub", true, []CompletionRef{{Name: "pub"}}},
		{"SELECT * FROM public.users AS x JOIN orders o ON o.uid = x.", "x", "", false,
			[]CompletionRef{{Schema: "public", Name: "users", Alias: "x"}, {Name: "orders", Alias: "o"}}},
		{"SELECT * FROM a, b WHERE ", "", "", false, []CompletionRef{{Name: "a"}, {Name: "b"}}},
		{"SELECT * FROM a, ", "", "", true, []CompletionRef{{Name: "a"}}},
		{`SELECT "T".c FROM "My Table" "T"`, "T", "c", false, []CompletionRef{{Name: "My Table", Alias: "T"}}},
		{"UPDATE accounts SET bal", "", "bal", false, []CompletionRef{{Name: "accounts"}}},
	}
	for _, c := range cases {
		caret := len(c.sql)
		if c.sql[:9] == "SELECT u." || c.sql[:9] == `SELECT "T` {
			caret = len(c.sql[:strings.Index(c.sql, " FROM")])
		}
		ctx := AnalyzeSQL(c.sql, caret, o, kw)
		if ctx.Qualifier != c.qual || ctx.Prefix != c.prefix || ctx.ExpectObject != c.expect {
			t.Errorf("%q: qual %q prefix %q expect %v", c.sql, ctx.Qualifier, ctx.Prefix, ctx.ExpectObject)
		}
		if len(ctx.Refs) != len(c.refs) {
			t.Errorf("%q: refs %+v", c.sql, ctx.Refs)
			continue
		}
		for i := range c.refs {
			if ctx.Refs[i] != c.refs[i] {
				t.Errorf("%q: ref %d = %+v want %+v", c.sql, i, ctx.Refs[i], c.refs[i])
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
