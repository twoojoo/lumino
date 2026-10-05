package ai

import (
	"regexp"
)

// Redaction keeps credentials out of what is sent to the model.

const redacted = "<redacted>"

var sensitiveName = regexp.MustCompile(`(?i)(pass(word|wd)?|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credential|auth|cookie|session|signature|dsn|connection[_-]?string)`)

var textRules = []struct {
	rx   *regexp.Regexp
	repl string
}{
	// PEM private keys
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`), "-----BEGIN PRIVATE KEY-----" + redacted + "-----END PRIVATE KEY-----"},
	// Authorization headers
	{regexp.MustCompile(`(?i)(authorization["']?\s*[:=]\s*["']?(bearer|basic|token)\s+)[^\s"',]+`), "${1}" + redacted},
	// key=value / key: value with sensitive keys
	{regexp.MustCompile(`(?i)\b([a-z0-9_.-]*(?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|client[_-]?secret)[a-z0-9_.-]*)(["']?\s*[:=]\s*["']?)([^\s"',;&}]{3,})`), "${1}${2}" + redacted},
	// credentials embedded in URLs: scheme://user:pass@host
	{regexp.MustCompile(`([a-z][a-z0-9+.-]*://[^\s:/@]+:)[^\s@/]+@`), "${1}" + redacted + "@"},
	// JWTs
	{regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), redacted},
	// AWS access keys, GitHub/GitLab/Slack/OpenAI/Anthropic style tokens
	{regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`), redacted},
	{regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{20,}|glpat-[A-Za-z0-9_-]{20,}|xox[abpr]-[A-Za-z0-9-]{10,}|sk-[A-Za-z0-9_-]{20,})\b`), redacted},
}

// RedactText scrubs credentials from free text (logs, describe output).
func RedactText(s string) string {
	for _, r := range textRules {
		s = r.rx.ReplaceAllString(s, r.repl)
	}
	return s
}

// SensitiveColumn reports whether a column name looks like it holds
// credentials (its values are redacted in samples sent to the model).
func SensitiveColumn(name string) bool { return sensitiveName.MatchString(name) }
