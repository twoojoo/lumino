package ai

import (
	"strings"
	"testing"
)

func TestRedactText(t *testing.T) {
	in := strings.Join([]string{
		`connecting with password=hunter22 to db`,
		`{"api_key": "abc123456", "user":"bob"}`,
		`Authorization: Bearer abc.def.ghi`,
		`postgres://admin:s3cr3t@db:5432/app`,
		`token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U`,
		`AKIAIOSFODNN7EXAMPLE`,
		"-----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY-----",
		`normal line with level=info msg=ok`,
	}, "\n")
	out := RedactText(in)
	for _, leak := range []string{"hunter22", "abc123456", "abc.def.ghi", "s3cr3t", "eyJzdWIi", "AKIAIOSFODNN7EXAMPLE", "MIIEow"} {
		if strings.Contains(out, leak) {
			t.Errorf("leaked %q in:\n%s", leak, out)
		}
	}
	if !strings.Contains(out, "level=info msg=ok") || !strings.Contains(out, `"user":"bob"`) {
		t.Errorf("over-redacted:\n%s", out)
	}
}

func TestRedactManifest(t *testing.T) {
	sec := `apiVersion: v1
kind: Secret
metadata:
  name: db
  annotations:
    kubectl.kubernetes.io/last-applied-configuration: '{"data":{"password":"czNjcjN0"}}'
data:
  password: czNjcjN0
  username: YWRtaW4=
`
	out := RedactManifest("secrets", sec)
	if strings.Contains(out, "czNjcjN0") || strings.Contains(out, "YWRtaW4=") {
		t.Fatalf("secret leaked:\n%s", out)
	}
	if !strings.Contains(out, "password: <redacted>") || !strings.Contains(out, "username") {
		t.Fatalf("keys should stay visible:\n%s", out)
	}
	pod := `kind: Pod
spec:
  containers:
  - name: app
    image: nginx
    env:
    - name: DB_PASSWORD
      value: hunter22
    - name: LOG_LEVEL
      value: debug
`
	out = RedactManifest("pods", pod)
	if strings.Contains(out, "hunter22") || !strings.Contains(out, "debug") {
		t.Fatalf("env redaction wrong:\n%s", out)
	}
}

func TestTextToolCalls(t *testing.T) {
	specs := []ToolSpec{{Name: "get_logs"}, {Name: "describe_resource"}}
	txt := "I'll check:\n```json\n{\"name\": \"get_logs\", \"parameters\": {\"name\": \"crasher\", \"namespace\": \"demo\"}}\n{\"name\": \"describe_resource\", \"parameters\": {\"kind\": \"pods\", \"name\": \"<pod_name>\"}}\n```"
	calls := textToolCalls(txt, specs)
	if len(calls) != 1 || calls[0].Name != "get_logs" {
		t.Fatalf("got %+v", calls)
	}
}

func TestRedactLastAppliedAndMultiDoc(t *testing.T) {
	dep := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  annotations:
    kubectl.kubernetes.io/last-applied-configuration: '{"kind":"Deployment","spec":{"template":{"spec":{"containers":[{"env":[{"name":"DB_PASSWORD","value":"hunter2xyz"}]}]}}}}'
`
	if out := RedactManifest("deployments", dep); strings.Contains(out, "hunter2xyz") {
		t.Fatalf("last-applied leaked:\n%s", out)
	}
	multi := "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: a}\n---\napiVersion: v1\nkind: Secret\nmetadata: {name: b}\ndata:\n  tls.key: c3VwZXJzZWNyZXQ=\n"
	out := RedactManifest("", multi)
	if strings.Contains(out, "c3VwZXJzZWNyZXQ=") || !strings.Contains(out, "ConfigMap") {
		t.Fatalf("multi-doc:\n%s", out)
	}
}

func TestCheckApplyManifest(t *testing.T) {
	p := &Proposal{Action: "apply", Kind: "configmaps", GVR: "v1/configmaps", Name: "settings", Namespace: "demo"}
	p.Manifest = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: settings\ndata: {a: b}\n"
	m, err := checkApplyManifest(p)
	if err != nil || !strings.Contains(m, "namespace: demo") {
		t.Fatalf("fill namespace: %v\n%s", err, m)
	}
	for _, bad := range []string{
		"apiVersion: v1\nkind: ConfigMap\nmetadata: {name: other, namespace: demo}\n",
		"apiVersion: v1\nkind: ConfigMap\nmetadata: {name: settings, namespace: prod}\n",
		"apiVersion: v1\nkind: ConfigMap\nmetadata: {name: settings}\ndata: {pw: <redacted>}\n",
	} {
		p.Manifest = bad
		if _, err := checkApplyManifest(p); err == nil {
			t.Errorf("accepted:\n%s", bad)
		}
	}
}
