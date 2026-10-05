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
	Connection string // connection name
	Database   string // product and version
	Dialect    string
	Schema     string // current schema
	View       string // e.g. "Console" or "Table public.users"
	SQL        string // SQL in the editor (or the selected statement)
	Error      string // last error of the console
	ReadOnly   bool
	Protected  bool
}

func systemPrompt(ci ContextInfo) string {
	var b strings.Builder
	b.WriteString(`You are the assistant built into Lumino, a desktop database IDE. You help the user write, understand, fix and optimize SQL, and understand their database schema.

How to work:
- Use the tools to look at the real schema (list_objects, describe_table, get_ddl, sample_rows) before writing SQL against it; never invent table or column names. Prefer a few targeted calls.
- Call tools directly through the tool-calling interface; never write tool calls as text or JSON in your reply, and never use placeholder values.
- Your tools are read-only. To change anything (INSERT, UPDATE, DELETE, DDL), call propose_sql: the user reviews it and runs it themselves. Never claim a change was made.
- Write SQL in the dialect of this database. Quote identifiers only when needed.
- Sample rows and query results have sensitive columns redacted; never ask the user to paste credentials.
- Answer in the user's language. Be concise and practical. Use Markdown, with SQL in fenced sql blocks.
`)
	fmt.Fprintf(&b, "\nCurrent UI context:\n- connection: %s\n- database: %s (dialect %s)\n", ci.Connection, ci.Database, ci.Dialect)
	if ci.Schema != "" {
		fmt.Fprintf(&b, "- current schema: %s\n", ci.Schema)
	}
	if ci.ReadOnly {
		b.WriteString("- the connection is READ-ONLY: don't propose changes\n")
	}
	if ci.Protected {
		b.WriteString("- the connection is a PROTECTED production database: be careful, explain the risks of every change\n")
	}
	if ci.View != "" {
		fmt.Fprintf(&b, "- open view: %s\n", ci.View)
	}
	if ci.SQL != "" {
		sql := ci.SQL
		if len(sql) > 6000 {
			sql = sql[:6000] + "\n…"
		}
		fmt.Fprintf(&b, "- SQL in the editor (\"this query\" usually refers to it):\n```sql\n%s\n```\n", sql)
	}
	if ci.Error != "" {
		fmt.Fprintf(&b, "- last error: %s\n", ci.Error)
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
	for _, k := range []string{"schema", "table", "name", "type", "limit", "sql"} {
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
