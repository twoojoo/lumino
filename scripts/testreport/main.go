// Command testreport reads `go test -json` on stdin, prints the output of
// the tests as it comes and writes the full output of every failed test
// (and of packages that failed to build or crashed) to a file, so that a
// failure of a long integration run can be read afterwards. It exits with
// status 1 when something failed.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// event is a line of `go test -json` (test2json).
type event struct {
	Time       time.Time
	Action     string
	Package    string
	ImportPath string
	Test       string
	Output     string
	Elapsed    float64
}

type entry struct {
	name    string
	out     strings.Builder
	failed  bool
	elapsed float64
}

// report collects the outputs by test.
type report struct {
	order   []string
	entries map[string]*entry
	failed  bool
}

func (r *report) entry(name string) *entry {
	e := r.entries[name]
	if e == nil {
		e = &entry{name: name}
		r.entries[name] = e
		r.order = append(r.order, name)
	}
	return e
}

// process copies the test output to w and returns the collected report.
func process(in io.Reader, w io.Writer) *report {
	r := &report{entries: map[string]*entry{}}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		var ev event
		if len(line) == 0 || line[0] != '{' || json.Unmarshal(line, &ev) != nil {
			// not JSON: compiler or runtime output outside test2json
			fmt.Fprintln(w, string(line))
			e := r.entry("(other output)")
			e.out.Write(line)
			e.out.WriteByte('\n')
			continue
		}
		pkg := ev.Package
		if pkg == "" {
			pkg = ev.ImportPath
		}
		name := pkg
		if ev.Test != "" {
			name += " " + ev.Test
		}
		switch ev.Action {
		case "output", "build-output":
			fmt.Fprint(w, ev.Output)
			r.entry(name).out.WriteString(ev.Output)
		case "fail", "build-fail":
			e := r.entry(name)
			e.failed, e.elapsed = true, ev.Elapsed
			r.failed = true
		}
	}
	if err := sc.Err(); err != nil {
		e := r.entry("(input)")
		e.failed = true
		e.out.WriteString(err.Error())
		r.failed = true
	}
	// output that came outside test2json only matters when something failed
	if e := r.entries["(other output)"]; e != nil && r.failed {
		e.failed = true
	}
	return r
}

// text is the failure report.
func (r *report) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Failures of %s\n\n", time.Now().Format(time.RFC3339))
	for _, n := range r.order {
		e := r.entries[n]
		if !e.failed {
			continue
		}
		fmt.Fprintf(&b, "=== FAIL %s (%.2fs)\n%s\n", e.name, e.elapsed, e.out.String())
	}
	return b.String()
}

func main() {
	out := flag.String("out", ".dev/test-failures.txt", "file receiving the output of the failed tests")
	flag.Parse()
	r := process(os.Stdin, os.Stdout)
	if !r.failed {
		os.Remove(*out)
		return
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err == nil {
		if err := os.WriteFile(*out, []byte(r.text()), 0o644); err == nil {
			fmt.Fprintf(os.Stderr, "\nfailures written to %s\n", *out)
		}
	}
	os.Exit(1)
}
