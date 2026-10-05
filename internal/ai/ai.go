// Package ai implements the lumino database assistant: a provider-neutral
// agent loop with read-only database tools, on top of Anthropic (official
// SDK) and any OpenAI-compatible API (OpenAI, Gemini, Mistral, Groq,
// OpenRouter, DeepSeek, xAI, Ollama, LM Studio, vLLM, llama.cpp…).
package ai

import (
	"context"
	"encoding/json"
)

// ToolSpec describes a function the model may call.
type ToolSpec struct {
	Name        string
	Description string
	// Schema is a JSON-schema object: {"type":"object","properties":…,"required":…}
	Properties map[string]any
	Required   []string
}

// ToolCall is a model request to run a tool.
type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

// ToolResult answers a ToolCall.
type ToolResult struct {
	CallID  string
	Name    string
	Content string
	IsError bool
}

// Role of a history entry.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool" // a batch of tool results
)

// Message is a provider-neutral history entry. Assistant turns also keep the
// provider's native representation (e.g. Anthropic thinking blocks) so the
// same provider gets them back unchanged; other providers use the neutral
// fields.
type Message struct {
	Role    Role
	Text    string
	Calls   []ToolCall
	Results []ToolResult

	NativeFrom string // provider id that produced Native
	Native     any
}

// Request is one model call.
type Request struct {
	Model     string
	System    string
	Messages  []Message
	Tools     []ToolSpec
	MaxTokens int64
}

// Turn is the model's reply to a Request.
type Turn struct {
	Text       string
	Calls      []ToolCall
	StopReason string // end | tool_use | max_tokens | refusal | other
	Native     any
}

// Provider is a chat backend with tool calling and streaming.
type Provider interface {
	// ID identifies the provider instance (used to match Native history).
	ID() string
	// Stream runs one model call; onText receives text deltas as they arrive.
	Stream(ctx context.Context, req Request, onText func(string)) (*Turn, error)
	// Models lists available model ids (best effort).
	Models(ctx context.Context) ([]string, error)
}
