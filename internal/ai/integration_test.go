//go:build integration && dev

package ai

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/giovanni/lumino/internal/backend"
	"github.com/giovanni/lumino/internal/safety"
)

func devCluster(t *testing.T) *backend.Cluster {
	t.Helper()
	kc, err := safety.ResolveKubeconfig(filepath.Join("..", "..", ".dev", "kubeconfig"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(kc); err != nil {
		t.Skip("dev cluster not running")
	}
	safety.IsolateCaches(kc)
	names, cur, err := backend.Contexts(kc)
	if err != nil || len(safety.FilterContexts(names)) != len(names) {
		t.Fatalf("refusing kubeconfig: %v", names)
	}
	c, err := backend.Connect(kc, cur)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestToolsDirect(t *testing.T) {
	c := devCluster(t)
	tl := &Tools{Cluster: c, Namespace: "demo"}
	ctx := context.Background()
	for _, tc := range []struct{ name, args, want string }{
		{"cluster_overview", `{}`, "Nodes: 1/1"},
		{"list_resources", `{"kind":"po","filter":"crasher"}`, "crasher-"}, // status flips between Error and CrashLoopBackOff
		{"get_resource_yaml", `{"kind":"secret","name":"db-creds"}`, "password: <redacted>"},
		{"describe_resource", `{"kind":"deploy","name":"web"}`, "Replicas"},
		{"get_events", `{"warnings_only":true}`, "BackOff"},
		{"get_logs", `{"kind":"deploy","name":"jsonlog","tail_lines":5}`, "request handled"},
		{"xray", `{"kind":"deploy","name":"web"}`, "container"},
		{"list_api_resources", `{}`, "Widget"},
		{"lint_cluster", `{}`, "POD-FAILING"},
	} {
		out, err := tl.Run(ctx, ToolCall{Name: tc.name, Args: []byte(tc.args)})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !strings.Contains(out, tc.want) {
			t.Fatalf("%s: missing %q in:\n%s", tc.name, tc.want, out)
		}
		if strings.Contains(out, "s3cr3t") {
			t.Fatalf("%s leaked a secret", tc.name)
		}
	}
	var prop *Proposal
	tl.OnPropose = func(p *Proposal) { prop = p }
	if _, err := tl.Run(ctx, ToolCall{Name: "propose_action", Args: []byte(`{"action":"scale","kind":"deploy","name":"web","replicas":2,"reason":"test"}`)}); err != nil {
		t.Fatal(err)
	}
	if prop == nil || prop.GVR != "apps/v1/deployments" || prop.Path() != "demo/web" || *prop.Replicas != 2 {
		t.Fatalf("proposal %+v", prop)
	}
}

// Real end-to-end run against a local Ollama model (free, offline).
func TestAgentOllama(t *testing.T) {
	model := os.Getenv("KUBEON_TEST_OLLAMA_MODEL")
	if model == "" {
		t.Skip("set KUBEON_TEST_OLLAMA_MODEL (e.g. llama3.1:8b)")
	}
	c := devCluster(t)
	p := NewOpenAICompat("ollama", "", "http://localhost:11434/v1")
	ag := &Agent{Provider: p, Model: model, Tools: &Tools{Cluster: c, Namespace: "demo"}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var tools []string
	var final string
	ag.Run(ctx, ContextInfo{KubeContext: "kind-kubeon-dev", Namespace: "demo", View: "Pods"},
		"Why is the crasher pod in namespace demo failing? Check its logs and events.", func(e Event) {
			switch e.Kind {
			case EvToolEnd:
				tools = append(tools, e.Tool.Name+"("+e.Tool.Args+")")
				t.Logf("tool %s %s -> %d bytes err=%v", e.Tool.Name, e.Tool.Args, len(e.Tool.Output), e.Tool.IsError)
			case EvTurnEnd:
				if e.Text != "" {
					final = e.Text
				}
			case EvError:
				t.Fatalf("agent error: %v", e.Err)
			}
		})
	t.Logf("tools: %v", tools)
	t.Logf("answer:\n%s", final)
	if len(tools) == 0 {
		t.Fatal("model used no tools")
	}
	if final == "" {
		t.Fatal("no final answer")
	}
}
