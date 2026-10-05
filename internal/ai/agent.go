package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

const maxSteps = 14

// EventKind classifies agent events.
type EventKind int

const (
	EvText      EventKind = iota // text delta of the current assistant turn
	EvTurnEnd                    // assistant turn finished (Text = final text)
	EvToolStart                  // Tool started
	EvToolEnd                    // Tool finished
	EvProposal                   // Proposal raised
	EvDone                       // loop finished
	EvError                      // fatal error (Err)
)

// ToolRun describes one tool execution for the UI.
type ToolRun struct {
	ID       string
	Name     string
	Args     string
	Output   string
	IsError  bool
	Duration time.Duration
}

// Event is emitted by Agent.Run.
type Event struct {
	Kind     EventKind
	Text     string
	Tool     *ToolRun
	Proposal *Proposal
	Err      error
}

// Agent runs the tool loop for one conversation. History is append-only
// (keeps provider thinking blocks valid).
type Agent struct {
	Provider Provider
	Model    string
	Tools    *Tools
	History  []Message
}

// ContextInfo is what the user is looking at.
type ContextInfo struct {
	KubeContext string
	K8sVersion  string
	Namespace   string // "" = all
	View        string // e.g. "Pods"
	Selected    string // e.g. "v1/pods demo/web-123"
}

func systemPrompt(ci ContextInfo) string {
	ns := ci.Namespace
	if ns == "" {
		ns = "all namespaces"
	}
	var b strings.Builder
	b.WriteString(`You are the assistant built into Kubeon, a desktop Kubernetes IDE. You help the user understand and troubleshoot their cluster.

How to work:
- Use the tools to gather evidence before answering; don't guess cluster state. Prefer a few targeted calls (describe, events, previous logs) over broad listing.
- Call tools directly through the tool-calling interface; never write tool calls as text or JSON in your reply, and never use placeholder values. If you don't know an exact name, call list_resources first to find it.
- Your tools are read-only. To change anything (scale, restart, delete, apply, cordon), call propose_action: the user reviews and confirms it in the UI. Never claim a change was made.
- Ground conclusions in what the tools returned: quote the relevant event, status or log line. If evidence is inconclusive, say what is missing.
- Secret values and credentials are redacted before you see them; never ask the user to paste secrets.
- Answer in the user's language. Be concise and practical: root cause first, then the fix. Use Markdown: short paragraphs, bullet lists, ` + "`code`" + ` and fenced YAML blocks.
`)
	fmt.Fprintf(&b, "\nCurrent UI context:\n- kube context: %s", ci.KubeContext)
	if ci.K8sVersion != "" {
		fmt.Fprintf(&b, " (Kubernetes %s)", ci.K8sVersion)
	}
	fmt.Fprintf(&b, "\n- namespace filter: %s\n", ns)
	if ci.View != "" {
		fmt.Fprintf(&b, "- open view: %s\n", ci.View)
	}
	if ci.Selected != "" {
		fmt.Fprintf(&b, "- selected resource: %s (\"this\" usually refers to it)\n", ci.Selected)
	}
	return b.String()
}

// Run appends the user message and drives the model/tool loop, emitting
// events. It returns when the model stops calling tools, on error, or when
// ctx is cancelled.
func (a *Agent) Run(ctx context.Context, ci ContextInfo, user string, emit func(Event)) {
	a.History = append(a.History, Message{Role: RoleUser, Text: user})
	specs := a.Tools.Specs()
	sys := systemPrompt(ci)
	for step := 0; step < maxSteps; step++ {
		turn, err := a.Provider.Stream(ctx, Request{
			Model:    a.Model,
			System:   sys,
			Messages: a.History,
			Tools:    specs,
		}, func(d string) { emit(Event{Kind: EvText, Text: d}) })
		if err != nil {
			if errors.Is(err, context.Canceled) {
				emit(Event{Kind: EvDone})
				return
			}
			emit(Event{Kind: EvError, Err: err})
			return
		}
		if _, claude := a.Provider.(*Anthropic); len(turn.Calls) == 0 && !claude {
			// small local models sometimes print the call instead of making it
			// (never on Claude: a JSON example in an answer must stay text)
			if calls := textToolCalls(turn.Text, specs); len(calls) > 0 {
				turn.Calls, turn.Native, turn.Text = calls, nil, ""
				turn.StopReason = "tool_use"
			}
		}
		a.History = append(a.History, Message{
			Role: RoleAssistant, Text: turn.Text, Calls: turn.Calls,
			Native: turn.Native, NativeFrom: a.Provider.ID(),
		})
		emit(Event{Kind: EvTurnEnd, Text: turn.Text})
		if len(turn.Calls) == 0 || turn.StopReason == "max_tokens" || turn.StopReason == "refusal" {
			// every tool call needs a result, or the provider rejects the
			// whole conversation from the next request on
			if len(turn.Calls) > 0 {
				a.History = append(a.History, Message{Role: RoleTool, Results: errorResults(turn.Calls, "call not executed (turn ended: "+turn.StopReason+")")})
			}
			emit(Event{Kind: EvDone})
			return
		}
		results := make([]ToolResult, 0, len(turn.Calls))
		for _, call := range turn.Calls {
			run := &ToolRun{ID: call.ID, Name: call.Name, Args: compactJSON(call.Args)}
			emit(Event{Kind: EvToolStart, Tool: run})
			start := time.Now()
			out, err := a.runTool(ctx, call, emit)
			run.Duration = time.Since(start)
			if err != nil {
				out, run.IsError = err.Error(), true
			}
			run.Output = out
			emit(Event{Kind: EvToolEnd, Tool: run})
			results = append(results, ToolResult{CallID: call.ID, Name: call.Name, Content: out, IsError: run.IsError})
			if ctx.Err() != nil {
				// stopped: the remaining calls still need a result
				results = append(results, errorResults(turn.Calls[len(results):], "cancelled by the user")...)
				break
			}
		}
		// all results of the turn go back together
		a.History = append(a.History, Message{Role: RoleTool, Results: results})
		if ctx.Err() != nil {
			emit(Event{Kind: EvDone})
			return
		}
	}
	emit(Event{Kind: EvError, Err: fmt.Errorf("stopped after %d steps", maxSteps)})
}

func (a *Agent) runTool(ctx context.Context, call ToolCall, emit func(Event)) (string, error) {
	tctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	prev := a.Tools.OnPropose
	a.Tools.OnPropose = func(p *Proposal) { emit(Event{Kind: EvProposal, Proposal: p}) }
	defer func() { a.Tools.OnPropose = prev }()
	if !json.Valid(call.Args) && len(call.Args) > 0 {
		return "", fmt.Errorf("invalid JSON arguments")
	}
	return a.Tools.Run(tctx, call)
}

var callSeq atomic.Uint64

// newCallID returns a conversation-unique id for calls without one
// (providers reject duplicate tool call ids in a history).
func newCallID(prefix string) string { return fmt.Sprintf("%s_%d", prefix, callSeq.Add(1)) }

func errorResults(calls []ToolCall, msg string) []ToolResult {
	out := make([]ToolResult, 0, len(calls))
	for _, c := range calls {
		out = append(out, ToolResult{CallID: c.ID, Name: c.Name, Content: msg, IsError: true})
	}
	return out
}

func compactJSON(b json.RawMessage) string {
	var v map[string]any
	if json.Unmarshal(b, &v) != nil || len(v) == 0 {
		return ""
	}
	parts := make([]string, 0, len(v))
	for _, k := range []string{"action", "kind", "namespace", "name", "container", "filter", "object", "previous", "tail_lines", "grep", "replicas"} {
		if x, ok := v[k]; ok && fmt.Sprint(x) != "" && fmt.Sprint(x) != "false" {
			parts = append(parts, fmt.Sprintf("%s=%v", k, x))
		}
	}
	return strings.Join(parts, " ")
}

var jsonObjRx = regexp.MustCompile(`\{[^{}]*"(?:name|function)"\s*:\s*"[a-z_]+"[^{}]*\{[^{}]*\}[^{}]*\}`)

// textToolCalls extracts tool calls written as JSON text, e.g.
// {"name":"get_logs","parameters":{...}}. Calls with placeholder values
// ("<pod_name>") are ignored.
func textToolCalls(text string, specs []ToolSpec) []ToolCall {
	if !strings.Contains(text, "{") {
		return nil
	}
	known := map[string]bool{}
	for _, s := range specs {
		known[s.Name] = true
	}
	var out []ToolCall
	for _, m := range jsonObjRx.FindAllString(text, 4) {
		var v struct {
			Name       string          `json:"name"`
			Function   string          `json:"function"`
			Parameters json.RawMessage `json:"parameters"`
			Arguments  json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal([]byte(m), &v) != nil {
			continue
		}
		name := v.Name
		if name == "" {
			name = v.Function
		}
		args := v.Parameters
		if len(args) == 0 {
			args = v.Arguments
		}
		if !known[name] || len(args) == 0 || strings.Contains(string(args), "<") {
			continue
		}
		out = append(out, ToolCall{ID: newCallID("text_call"), Name: name, Args: args})
	}
	return out
}
