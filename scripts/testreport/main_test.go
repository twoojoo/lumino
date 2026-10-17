package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestProcess(t *testing.T) {
	in := strings.Join([]string{
		`{"Action":"run","Package":"p","Test":"TestA"}`,
		`{"Action":"output","Package":"p","Test":"TestA","Output":"=== RUN   TestA\n"}`,
		`{"Action":"output","Package":"p","Test":"TestA/sub","Output":"    a_test.go:9: boom\n"}`,
		`{"Action":"fail","Package":"p","Test":"TestA/sub","Elapsed":0.5}`,
		`{"Action":"fail","Package":"p","Test":"TestA","Elapsed":0.6}`,
		`{"Action":"output","Package":"p","Test":"TestB","Output":"ok\n"}`,
		`{"Action":"pass","Package":"p","Test":"TestB"}`,
		`{"Action":"build-output","ImportPath":"q","Output":"q.go:1: syntax error\n"}`,
		`{"Action":"build-fail","ImportPath":"q"}`,
		`not json`,
	}, "\n")
	var out bytes.Buffer
	r := process(strings.NewReader(in), &out)
	if !r.failed {
		t.Fatal("failures not seen")
	}
	txt := r.text()
	for _, want := range []string{"=== FAIL p TestA/sub (0.50s)\n    a_test.go:9: boom", "=== FAIL p TestA (0.60s)", "=== FAIL q (0.00s)\nq.go:1: syntax error", "not json"} {
		if !strings.Contains(txt, want) {
			t.Errorf("report misses %q:\n%s", want, txt)
		}
	}
	if strings.Contains(txt, "TestB") {
		t.Errorf("passing test reported:\n%s", txt)
	}
	if !strings.Contains(out.String(), "a_test.go:9: boom") || !strings.Contains(out.String(), "not json") {
		t.Errorf("output not copied: %q", out.String())
	}
	if r := process(strings.NewReader(`{"Action":"pass","Package":"p"}`+"\nnoise"), &bytes.Buffer{}); r.failed || strings.Contains(r.text(), "noise") {
		t.Fatal("no failure expected")
	}
}
