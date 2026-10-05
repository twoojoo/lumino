package ui

import (
	"strings"
	"testing"
)

func TestLineDiff(t *testing.T) {
	a := strings.Split("a\nb\nc\nd\ne\nf\ng\nh", "\n")
	b := strings.Split("a\nb\nC\nd\ne\nf\ng\nh\ni", "\n")
	d := lineDiff(a, b)
	add, del := diffStats(d)
	if add != 2 || del != 1 {
		t.Fatalf("add %d del %d: %+v", add, del, d)
	}
	c := collapse(d, 1)
	var ops []string
	for _, l := range c {
		ops = append(ops, map[diffOp]string{opEq: "=", opDel: "-", opAdd: "+", opGap: "~"}[l.op])
	}
	if got := strings.Join(ops, ""); got != "~=-+=~=+" {
		t.Fatalf("collapsed %q", got)
	}
	if same := lineDiff(a, a); len(collapse(same, 3)) != 1 {
		t.Fatal("identical input should collapse to one gap")
	}
}

func TestYAMLTokens(t *testing.T) {
	kinds := func(l string) string {
		var s []string
		for _, tk := range yamlTokens(l) {
			if strings.TrimSpace(tk.text) == "" {
				continue
			}
			s = append(s, map[tokKind]string{tkPlain: "p", tkKey: "k", tkString: "s", tkNumber: "n", tkBool: "b", tkComment: "c", tkPunct: ":"}[tk.kind])
		}
		return strings.Join(s, "")
	}
	cases := map[string]string{
		"apiVersion: v1":            "k:p",
		"  replicas: 3":             "k:n",
		"  - name: web":             ":k:p",
		`    image: "nginx:1.2"`:    "k:s",
		"  enabled: true # comment": "k:bc",
		"# full comment":            "c",
		"  - --flag":                ":p",
		"  annotations: {}":         "k::",
		"    url: http://a:8080/x":  "k:p",
	}
	for in, want := range cases {
		if got := kinds(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}
