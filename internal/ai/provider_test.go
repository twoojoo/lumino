package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type recorded struct {
	mx     sync.Mutex
	bodies []map[string]any
	header []http.Header
}

func (r *recorded) add(req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	r.mx.Lock()
	r.bodies = append(r.bodies, m)
	r.header = append(r.header, req.Header.Clone())
	r.mx.Unlock()
}

func sse(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		fmt.Fprint(w, e)
	}
}

func anthropicEvent(typ, data string) string {
	return "event: " + typ + "\ndata: " + data + "\n\n"
}

func TestAnthropicProviderToolRoundTrip(t *testing.T) {
	var rec recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		n := len(rec.bodies)
		start := anthropicEvent("message_start", `{"type":"message_start","message":{"id":"msg_`+fmt.Sprint(n)+`","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}`)
		if n == 1 {
			sse(w, start,
				anthropicEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
				anthropicEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me check."}}`),
				anthropicEvent("content_block_stop", `{"type":"content_block_stop","index":0}`),
				anthropicEvent("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"list_resources","input":{}}}`),
				anthropicEvent("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"kind\":\"pods\"}"}}`),
				anthropicEvent("content_block_stop", `{"type":"content_block_stop","index":1}`),
				anthropicEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":20}}`),
				anthropicEvent("message_stop", `{"type":"message_stop"}`))
			return
		}
		sse(w, start,
			anthropicEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
			anthropicEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"All pods are fine."}}`),
			anthropicEvent("content_block_stop", `{"type":"content_block_stop","index":0}`),
			anthropicEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`),
			anthropicEvent("message_stop", `{"type":"message_stop"}`))
	}))
	defer srv.Close()

	p := NewAnthropic("anthropic-test", "test-key", srv.URL)
	tools := []ToolSpec{{Name: "list_resources", Description: "list", Properties: map[string]any{"kind": strProp("k")}, Required: []string{"kind"}}}
	hist := []Message{{Role: RoleUser, Text: "how are pods?"}}
	var streamed strings.Builder
	turn, err := p.Stream(context.Background(), Request{Model: "claude-opus-5-5", System: "sys", Messages: hist, Tools: tools},
		func(s string) { streamed.WriteString(s) })
	if err != nil {
		t.Fatal(err)
	}
	if turn.StopReason != "tool_use" || len(turn.Calls) != 1 || turn.Calls[0].Name != "list_resources" ||
		string(turn.Calls[0].Args) != `{"kind":"pods"}` || streamed.String() != "Let me check." {
		t.Fatalf("turn %+v streamed %q", turn, streamed.String())
	}
	b := rec.bodies[0]
	if b["model"] != "claude-opus-5-5" || b["fallbacks"] != "default" {
		t.Fatalf("model/fallbacks: %v %v", b["model"], b["fallbacks"])
	}
	if !strings.Contains(rec.header[0].Get("anthropic-beta"), "server-side-fallback-2026-07-01") {
		t.Fatalf("beta header: %q", rec.header[0].Get("anthropic-beta"))
	}
	tl := b["tools"].([]any)[0].(map[string]any)
	if tl["name"] != "list_resources" || tl["eager_input_streaming"] != true {
		t.Fatalf("tool def %v", tl)
	}

	hist = append(hist,
		Message{Role: RoleAssistant, Text: turn.Text, Calls: turn.Calls, Native: turn.Native, NativeFrom: p.ID()},
		Message{Role: RoleTool, Results: []ToolResult{{CallID: "toolu_1", Name: "list_resources", Content: "pod-a Running"}}},
	)
	turn2, err := p.Stream(context.Background(), Request{Model: "claude-opus-5-5", Messages: hist, Tools: tools}, nil)
	if err != nil || turn2.Text != "All pods are fine." || turn2.StopReason != "end" {
		t.Fatalf("turn2 %+v %v", turn2, err)
	}
	msgs := rec.bodies[1]["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("history len %d", len(msgs))
	}
	asst := msgs[1].(map[string]any)["content"].([]any)
	if asst[1].(map[string]any)["type"] != "tool_use" || asst[1].(map[string]any)["id"] != "toolu_1" {
		t.Fatalf("assistant replay %v", asst)
	}
	res := msgs[2].(map[string]any)
	block := res["content"].([]any)[0].(map[string]any)
	if res["role"] != "user" || block["type"] != "tool_result" || block["tool_use_id"] != "toolu_1" {
		t.Fatalf("tool result %v", res)
	}
}

func TestOpenAICompatToolRoundTrip(t *testing.T) {
	var rec recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"object":"list","data":[{"id":"m-b","object":"model"},{"id":"m-a","object":"model"}]}`)
			return
		}
		rec.add(r)
		chunk := func(delta string, finish string) string {
			f := "null"
			if finish != "" {
				f = `"` + finish + `"`
			}
			return `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m-a","choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + f + `}]}` + "\n\n"
		}
		if len(rec.bodies) == 1 {
			sse(w,
				chunk(`{"role":"assistant","content":"Checking"}`, ""),
				chunk(`{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_logs","arguments":""}}]}`, ""),
				chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"{\"name\":\"web\"}"}}]}`, ""),
				chunk(`{}`, "tool_calls"),
				"data: [DONE]\n\n")
			return
		}
		sse(w, chunk(`{"role":"assistant","content":"Logs look fine."}`, ""), chunk(`{}`, "stop"), "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := NewOpenAICompat("oa-test", "", srv.URL+"/v1")
	models, err := p.Models(context.Background())
	if err != nil || len(models) != 2 || models[0] != "m-a" {
		t.Fatalf("models %v %v", models, err)
	}
	tools := []ToolSpec{{Name: "get_logs", Description: "logs", Properties: map[string]any{"name": strProp("n")}, Required: []string{"name"}}}
	hist := []Message{{Role: RoleUser, Text: "logs?"}}
	turn, err := p.Stream(context.Background(), Request{Model: "m-a", System: "sys", Messages: hist, Tools: tools}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if turn.StopReason != "tool_use" || len(turn.Calls) != 1 || string(turn.Calls[0].Args) != `{"name":"web"}` || turn.Text != "Checking" {
		t.Fatalf("turn %+v", turn)
	}
	b := rec.bodies[0]
	if b["model"] != "m-a" || b["stream"] != true || b["messages"].([]any)[0].(map[string]any)["role"] != "system" {
		t.Fatalf("req %v", b)
	}
	hist = append(hist,
		Message{Role: RoleAssistant, Text: turn.Text, Calls: turn.Calls, Native: turn.Native, NativeFrom: p.ID()},
		Message{Role: RoleTool, Results: []ToolResult{{CallID: "call_1", Name: "get_logs", Content: "ok"}}},
	)
	turn2, err := p.Stream(context.Background(), Request{Model: "m-a", Messages: hist, Tools: tools}, nil)
	if err != nil || turn2.Text != "Logs look fine." {
		t.Fatalf("turn2 %+v %v", turn2, err)
	}
	msgs := rec.bodies[1]["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "tool" || last["tool_call_id"] != "call_1" {
		t.Fatalf("tool msg %v", last)
	}
	asst := msgs[len(msgs)-2].(map[string]any)
	if tc := asst["tool_calls"].([]any); len(tc) != 1 {
		t.Fatalf("assistant tool_calls %v", asst)
	}
}

// Switching provider mid-conversation must use the neutral history.
func TestCrossProviderHistory(t *testing.T) {
	var rec recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		sse(w, `data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`+"\n\n", "data: [DONE]\n\n")
	}))
	defer srv.Close()
	p := NewOpenAICompat("oa", "", srv.URL)
	hist := []Message{
		{Role: RoleUser, Text: "q"},
		{Role: RoleAssistant, Text: "t", Calls: []ToolCall{{ID: "toolu_9", Name: "get_logs", Args: []byte(`{"name":"x"}`)}}, NativeFrom: "anthropic", Native: struct{}{}},
		{Role: RoleTool, Results: []ToolResult{{CallID: "toolu_9", Content: "r"}}},
		{Role: RoleUser, Text: "q2"},
	}
	if _, err := p.Stream(context.Background(), Request{Model: "m", Messages: hist}, nil); err != nil {
		t.Fatal(err)
	}
	msgs := rec.bodies[0]["messages"].([]any)
	asst := msgs[1].(map[string]any)
	tc := asst["tool_calls"].([]any)[0].(map[string]any)
	if tc["id"] != "toolu_9" || tc["function"].(map[string]any)["arguments"] != `{"name":"x"}` {
		t.Fatalf("neutral replay %v", asst)
	}
}
