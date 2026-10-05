package ui

import (
	"strings"
	"testing"

	"github.com/giovanni/lumino/internal/db"
)

func TestExportWriters(t *testing.T) {
	cols := []db.Column{{Name: "a", Kind: db.KindText}, {Name: "a", Kind: db.KindInt}, {Name: "c", Kind: db.KindText}}
	rows := [][]any{{nil, int64(1), ""}, {"x,y", nil, ` q"`}}
	var b strings.Builder
	if err := writeRows(&b, "csv", cols, rows, nil, db.ObjectRef{Name: "t"}, true); err != nil {
		t.Fatal(err)
	}
	if want := "a,a,c\n,1,\"\"\n\"x,y\",,\" q\"\"\"\n"; b.String() != want {
		t.Errorf("csv:\n%q\nwant\n%q", b.String(), want)
	}
	b.Reset()
	if err := writeRows(&b, "json", cols, rows[:1], nil, db.ObjectRef{Name: "t"}, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `"a": null, "a_2": 1, "c": ""`) {
		t.Errorf("json: %s", b.String())
	}
}
