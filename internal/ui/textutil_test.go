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
