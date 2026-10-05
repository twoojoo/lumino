package db

import (
	"strconv"
	"testing"
)

type nilStringer struct{ n int }

func (s nilStringer) String() string { return strconv.Itoa(s.n) }

func TestNormalizeReviewCases(t *testing.T) {
	if v := Normalize(float32(0.1)); v != 0.1 {
		t.Errorf("float32: %v", v)
	}
	var p *nilStringer
	if v := Normalize(p); v != nil {
		t.Errorf("typed nil Stringer: %v", v)
	}
	if v := Normalize([]*nilStringer{nil, {3}}); FormatValue(v, KindArray) == "" {
		t.Errorf("array of nil pointers: %v", v)
	}
	for _, bad := range []string{"1/3", "0x1p-2", "NaN", "inf", ""} {
		if _, err := ParseValue(bad, KindDecimal); err == nil {
			t.Errorf("decimal %q accepted", bad)
		}
	}
	for _, ok := range []string{"1", "-1.5", ".5", "1e10", "+3."} {
		if _, err := ParseValue(ok, KindDecimal); err != nil {
			t.Errorf("decimal %q: %v", ok, err)
		}
	}
	d := &BaseDialect{}
	if l := d.Literal("NaN", KindDecimal); l != "'NaN'" {
		t.Errorf("NaN literal: %s", l)
	}
}
