package ui

import (
	"regexp"
	"strings"
)

// ---- line diff ----

type diffOp uint8

const (
	opEq diffOp = iota
	opDel
	opAdd
	opGap // collapsed unchanged lines
)

type diffLine struct {
	op           diffOp
	text         string
	oldNo, newNo int
}

// lineDiff computes an LCS line diff (O(n·m), capped; larger inputs fall back
// to "all removed / all added").
func lineDiff(a, b []string) []diffLine {
	// trim common prefix/suffix first: edits are usually small
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	am, bm := a[pre:len(a)-suf], b[pre:len(b)-suf]
	var out []diffLine
	for i := 0; i < pre; i++ {
		out = append(out, diffLine{op: opEq, text: a[i], oldNo: i + 1, newNo: i + 1})
	}
	n, m := len(am), len(bm)
	if n*m > 4_000_000 {
		for i, l := range am {
			out = append(out, diffLine{op: opDel, text: l, oldNo: pre + i + 1})
		}
		for j, l := range bm {
			out = append(out, diffLine{op: opAdd, text: l, newNo: pre + j + 1})
		}
	} else {
		// dp[i][j] = LCS length of am[i:], bm[j:]
		dp := make([][]int32, n+1)
		for i := range dp {
			dp[i] = make([]int32, m+1)
		}
		for i := n - 1; i >= 0; i-- {
			for j := m - 1; j >= 0; j-- {
				if am[i] == bm[j] {
					dp[i][j] = dp[i+1][j+1] + 1
				} else {
					dp[i][j] = max(dp[i+1][j], dp[i][j+1])
				}
			}
		}
		i, j := 0, 0
		for i < n || j < m {
			switch {
			case i < n && j < m && am[i] == bm[j]:
				out = append(out, diffLine{op: opEq, text: am[i], oldNo: pre + i + 1, newNo: pre + j + 1})
				i, j = i+1, j+1
			case i < n && (j == m || dp[i+1][j] >= dp[i][j+1]):
				out = append(out, diffLine{op: opDel, text: am[i], oldNo: pre + i + 1})
				i++
			default:
				out = append(out, diffLine{op: opAdd, text: bm[j], newNo: pre + j + 1})
				j++
			}
		}
	}
	for k := 0; k < suf; k++ {
		out = append(out, diffLine{op: opEq, text: a[len(a)-suf+k], oldNo: len(a) - suf + k + 1, newNo: len(b) - suf + k + 1})
	}
	return out
}

// collapse keeps ctx lines of context around changes; other runs of
// unchanged lines become a single opGap line.
func collapse(lines []diffLine, ctx int) []diffLine {
	keep := make([]bool, len(lines))
	for i, l := range lines {
		if l.op != opEq {
			for k := max(0, i-ctx); k <= min(len(lines)-1, i+ctx); k++ {
				keep[k] = true
			}
		}
	}
	var out []diffLine
	gap := 0
	for i, l := range lines {
		if keep[i] {
			if gap > 0 {
				out = append(out, diffLine{op: opGap, oldNo: gap})
				gap = 0
			}
			out = append(out, l)
		} else {
			gap++
		}
	}
	if gap > 0 {
		out = append(out, diffLine{op: opGap, oldNo: gap})
	}
	return out
}

func diffStats(lines []diffLine) (add, del int) {
	for _, l := range lines {
		switch l.op {
		case opAdd:
			add++
		case opDel:
			del++
		}
	}
	return
}

// ---- YAML highlighting ----

type tokKind uint8

const (
	tkPlain tokKind = iota
	tkKey
	tkString
	tkNumber
	tkBool
	tkComment
	tkPunct
)

type token struct {
	kind tokKind
	text string
}

var (
	yamlKeyRx  = regexp.MustCompile(`^(\s*)(- )?("[^"]*"|'[^']*'|[^\s#'"][^:#]*?)(:)(\s+|$)`)
	yamlNumRx  = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][-+]?[0-9]+)?$`)
	yamlBoolRx = regexp.MustCompile(`^(true|false|null|~|yes|no|True|False|Null)$`)
)

// yamlTokens splits one YAML line into highlight tokens.
func yamlTokens(line string) []token {
	var out []token
	rest := line
	trimmed := strings.TrimLeft(rest, " ")
	if strings.HasPrefix(trimmed, "#") {
		return []token{{tkComment, line}}
	}
	if m := yamlKeyRx.FindStringSubmatch(rest); m != nil {
		out = append(out, token{tkPlain, m[1]})
		if m[2] != "" {
			out = append(out, token{tkPunct, m[2]})
		}
		out = append(out, token{tkKey, m[3]}, token{tkPunct, m[4] + m[5]})
		rest = rest[len(m[0]):]
	} else {
		ind := len(rest) - len(trimmed)
		out = append(out, token{tkPlain, rest[:ind]})
		rest = trimmed
		if strings.HasPrefix(rest, "- ") || rest == "-" {
			out = append(out, token{tkPunct, rest[:min(2, len(rest))]})
			rest = rest[min(2, len(rest)):]
		}
	}
	if rest == "" {
		return out
	}
	// trailing comment (not inside quotes)
	comment := ""
	if i := strings.Index(rest, " #"); i >= 0 && !strings.ContainsAny(rest[:i], `"'`) {
		rest, comment = rest[:i], rest[i:]
	}
	v := strings.TrimSpace(rest)
	switch {
	case v == "|" || v == ">" || v == "|-" || v == ">-" || v == "{}" || v == "[]":
		out = append(out, token{tkPunct, rest})
	case strings.HasPrefix(v, `"`) || strings.HasPrefix(v, `'`):
		out = append(out, token{tkString, rest})
	case yamlNumRx.MatchString(v):
		out = append(out, token{tkNumber, rest})
	case yamlBoolRx.MatchString(v):
		out = append(out, token{tkBool, rest})
	default:
		out = append(out, token{tkPlain, rest})
	}
	if comment != "" {
		out = append(out, token{tkComment, comment})
	}
	return out
}
