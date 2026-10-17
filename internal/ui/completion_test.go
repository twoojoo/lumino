package ui

import "testing"

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
