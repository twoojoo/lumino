package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/giovanni/lumino/internal/db"
	_ "github.com/giovanni/lumino/internal/db/adapters/sqlite"
	"github.com/giovanni/lumino/internal/db/dbtest"
)

func sqliteConn(t *testing.T) db.Conn {
	t.Helper()
	cfg := db.Config{Driver: "sqlite", File: filepath.Join(t.TempDir(), "ai.sqlite"), Params: map[string]string{"create": "true"}}
	c, err := db.Open(context.Background(), &cfg, db.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	err = dbtest.Load(context.Background(), c, `
CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL, password_hash TEXT, api_token TEXT);
INSERT INTO users VALUES (1, 'ada', 'pbkdf2$secret$hash', 'tok_123'), (2, 'linus', 'x', 'y');
CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER REFERENCES users(id), total REAL);`)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func call(t *testing.T, tl *Tools, name string, args any) (string, error) {
	t.Helper()
	b, _ := json.Marshal(args)
	return tl.Run(context.Background(), ToolCall{ID: "x", Name: name, Args: b})
}

func TestToolsDirect(t *testing.T) {
	c := sqliteConn(t)
	var proposed *Proposal
	tl := &Tools{Conn: c, Schema: "main", AllowQueries: true, OnPropose: func(p *Proposal) { proposed = p }}
	out, err := call(t, tl, "list_objects", map[string]any{})
	if err != nil || !strings.Contains(out, "table users") || !strings.Contains(out, "table orders") {
		t.Fatalf("list_objects: %v\n%s", err, out)
	}
	out, err = call(t, tl, "describe_table", map[string]any{"table": "ORDERS"})
	if err != nil || !strings.Contains(out, "foreign key") || !strings.Contains(out, "users (id)") {
		t.Fatalf("describe_table: %v\n%s", err, out)
	}
	out, err = call(t, tl, "sample_rows", map[string]any{"table": "users", "limit": 100})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "pbkdf2") || strings.Contains(out, "tok_123") || !strings.Contains(out, "ada") {
		t.Fatalf("sample_rows must redact sensitive columns:\n%s", out)
	}
	if _, err := call(t, tl, "run_query", map[string]any{"sql": "DELETE FROM users"}); err == nil {
		t.Fatal("run_query accepted a DELETE")
	}
	out, err = call(t, tl, "run_query", map[string]any{"sql": "SELECT count(*) AS n FROM users"})
	if err != nil || !strings.Contains(out, "2") {
		t.Fatalf("run_query: %v %s", err, out)
	}
	if _, err := call(t, tl, "explain", map[string]any{"sql": "SELECT * FROM users WHERE id = 1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, tl, "propose_sql", map[string]any{"sql": "DELETE FROM users WHERE id = 2", "reason": "cleanup"}); err != nil || proposed == nil {
		t.Fatalf("propose: %v %v", err, proposed)
	}
	// rows were not deleted
	out, _ = call(t, tl, "run_query", map[string]any{"sql": "SELECT count(*) FROM users"})
	if !strings.Contains(out, "2") {
		t.Fatal("proposal was executed")
	}
	tl.AllowQueries = false
	if _, err := call(t, tl, "run_query", map[string]any{"sql": "SELECT 1"}); err == nil {
		t.Fatal("queries disabled but run_query ran")
	}
	for _, s := range tl.Specs() {
		if s.Name == "run_query" {
			t.Fatal("run_query offered while disabled")
		}
	}
}

// TestAgentWithTools drives a whole tool loop against a mock
// OpenAI-compatible server.
func TestAgentWithTools(t *testing.T) {
	c := sqliteConn(t)
	var n atomic.Int32
	var lastToolResult string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if m := body.Messages[len(body.Messages)-1]; m["role"] == "tool" {
			lastToolResult, _ = m["content"].(string)
		}
		chunk := func(delta, finish string) string {
			f := "null"
			if finish != "" {
				f = `"` + finish + `"`
			}
			return `data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + f + `}]}` + "\n\n"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch n.Add(1) {
		case 1:
			fmt.Fprint(w, chunk(`{"role":"assistant","tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"describe_table","arguments":"{\"table\":\"users\"}"}}]}`, ""),
				chunk(`{}`, "tool_calls"), "data: [DONE]\n\n")
		case 2:
			fmt.Fprint(w, chunk(`{"role":"assistant","tool_calls":[{"index":0,"id":"c2","type":"function","function":{"name":"propose_sql","arguments":"{\"sql\":\"SELECT name FROM users ORDER BY name\"}"}}]}`, ""),
				chunk(`{}`, "tool_calls"), "data: [DONE]\n\n")
		default:
			fmt.Fprint(w, chunk(`{"role":"assistant","content":"Here is the query."}`, ""), chunk(`{}`, "stop"), "data: [DONE]\n\n")
		}
	}))
	defer srv.Close()
	ag := &Agent{Provider: NewOpenAICompat("mock", "", srv.URL+"/v1"), Model: "m", Tools: &Tools{Conn: c, Schema: "main"}}
	var events []EventKind
	var prop *Proposal
	ag.Run(context.Background(), ContextInfo{Connection: "test", Database: "SQLite", Dialect: "sqlite"}, "list users by name", func(e Event) {
		events = append(events, e.Kind)
		if e.Kind == EvProposal {
			prop = e.Proposal
		}
		if e.Kind == EvError {
			t.Errorf("agent error: %v", e.Err)
		}
	})
	if prop == nil || prop.SQL != "SELECT name FROM users ORDER BY name" {
		t.Fatalf("proposal %+v (events %v)", prop, events)
	}
	if !strings.Contains(lastToolResult, "Shown to the user") {
		t.Fatalf("last tool result %q", lastToolResult)
	}
	if events[len(events)-1] != EvDone {
		t.Fatalf("events %v", events)
	}
}
