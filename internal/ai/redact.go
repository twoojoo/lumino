package ai

import (
	"regexp"
	"strings"

	"sigs.k8s.io/yaml"
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

// lastApplied holds the full previous manifest as JSON: it must be scrubbed
// like the object itself, on every kind.
const lastApplied = "kubectl.kubernetes.io/last-applied-configuration"

var docSep = regexp.MustCompile(`(?m)^---[ \t]*$`)

// RedactManifest scrubs YAML (one or more documents, e.g. a Helm release
// manifest): Secret data/stringData values, env vars with sensitive names,
// last-applied-configuration annotations, then text rules.
func RedactManifest(kind, y string) string {
	docs := docSep.Split(y, -1)
	if len(docs) > 1 {
		for i, d := range docs {
			if strings.TrimSpace(d) != "" {
				docs[i] = "\n" + strings.TrimLeft(RedactManifest("", d), "\n")
			}
		}
		return strings.TrimLeft(strings.Join(docs, "---"), "\n")
	}
	var obj map[string]any
	if err := yaml.Unmarshal([]byte(y), &obj); err != nil || obj == nil {
		return RedactText(y)
	}
	redactObject(kind, obj)
	b, err := yaml.Marshal(obj)
	if err != nil {
		return RedactText(y)
	}
	return RedactText(string(b))
}

func redactObject(kind string, obj map[string]any) {
	isSecret := strings.EqualFold(kind, "secret") || strings.EqualFold(kind, "secrets") ||
		strings.EqualFold(str(obj["kind"]), "Secret")
	if isSecret {
		for _, k := range []string{"data", "stringData"} {
			if m, ok := obj[k].(map[string]any); ok {
				for key := range m {
					m[key] = redacted
				}
			}
		}
	}
	if md, ok := obj["metadata"].(map[string]any); ok {
		if an, ok := md["annotations"].(map[string]any); ok {
			if la, ok := an[lastApplied].(string); ok {
				var prev map[string]any
				if isSecret || yaml.Unmarshal([]byte(la), &prev) != nil || prev == nil {
					an[lastApplied] = redacted
				} else {
					redactObject("", prev)
					b, _ := yaml.Marshal(prev)
					an[lastApplied] = string(b)
				}
			}
		}
	}
	walk(obj)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// walk redacts {name: <sensitive>, value: x} env entries anywhere.
func walk(v any) {
	switch t := v.(type) {
	case map[string]any:
		if name, ok := t["name"].(string); ok && sensitiveName.MatchString(name) {
			if _, has := t["value"]; has {
				t["value"] = redacted
			}
		}
		for _, c := range t {
			walk(c)
		}
	case []any:
		for _, c := range t {
			walk(c)
		}
	}
}
