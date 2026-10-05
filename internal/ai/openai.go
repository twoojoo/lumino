package ai

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
)

// OpenAICompat talks to any OpenAI Chat Completions compatible endpoint:
// OpenAI, Gemini (OpenAI endpoint), Mistral, Groq, OpenRouter, DeepSeek,
// xAI, Ollama, LM Studio, vLLM, llama.cpp server, LocalAI…
type OpenAICompat struct {
	id     string
	client openai.Client
}

// NewOpenAICompat builds a provider for baseURL. Local servers usually
// accept any non-empty key.
func NewOpenAICompat(id, apiKey, baseURL string) *OpenAICompat {
	if apiKey == "" {
		apiKey = "not-needed"
	}
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &OpenAICompat{id: id, client: openai.NewClient(opts...)}
}

func (o *OpenAICompat) ID() string { return o.id }

func (o *OpenAICompat) Models(ctx context.Context) ([]string, error) {
	pager := o.client.Models.ListAutoPaging(ctx)
	var out []string
	for pager.Next() {
		out = append(out, pager.Current().ID)
	}
	sort.Strings(out)
	return out, pager.Err()
}

func (o *OpenAICompat) tools(specs []ToolSpec) []openai.ChatCompletionToolUnionParam {
	out := make([]openai.ChatCompletionToolUnionParam, 0, len(specs))
	for _, s := range specs {
		params := shared.FunctionParameters{
			"type":       "object",
			"properties": s.Properties,
		}
		if len(s.Required) > 0 {
			params["required"] = s.Required
		}
		out = append(out, openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        s.Name,
			Description: openai.String(s.Description),
			Parameters:  params,
		}))
	}
	return out
}

func (o *OpenAICompat) messages(system string, history []Message) []openai.ChatCompletionMessageParamUnion {
	var out []openai.ChatCompletionMessageParamUnion
	if system != "" {
		out = append(out, openai.SystemMessage(system))
	}
	for _, m := range history {
		switch m.Role {
		case RoleUser:
			out = append(out, openai.UserMessage(m.Text))
		case RoleAssistant:
			if p, ok := m.Native.(openai.ChatCompletionMessageParamUnion); ok && m.NativeFrom == o.id {
				out = append(out, p)
				continue
			}
			a := openai.ChatCompletionAssistantMessageParam{}
			if m.Text != "" {
				a.Content.OfString = openai.String(m.Text)
			} else if len(m.Calls) == 0 { // empty turns are rejected
				a.Content.OfString = openai.String("(no output)")
			}
			for _, c := range m.Calls {
				a.ToolCalls = append(a.ToolCalls, openai.ChatCompletionMessageToolCallUnionParam{
					OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
						ID: c.ID,
						Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
							Name:      c.Name,
							Arguments: string(c.Args),
						},
					},
				})
			}
			out = append(out, openai.ChatCompletionMessageParamUnion{OfAssistant: &a})
		case RoleTool:
			for _, r := range m.Results {
				content := r.Content
				if r.IsError {
					content = "ERROR: " + content
				}
				out = append(out, openai.ToolMessage(content, r.CallID))
			}
		}
	}
	return out
}

func (o *OpenAICompat) Stream(ctx context.Context, req Request, onText func(string)) (*Turn, error) {
	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(req.Model),
		Messages: o.messages(req.System, req.Messages),
	}
	if len(req.Tools) > 0 {
		params.Tools = o.tools(req.Tools)
	}
	if req.MaxTokens > 0 {
		params.MaxCompletionTokens = openai.Int(req.MaxTokens)
	}
	stream := o.client.Chat.Completions.NewStreaming(ctx, params)
	acc := openai.ChatCompletionAccumulator{}
	for stream.Next() {
		chunk := stream.Current()
		acc.AddChunk(chunk)
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" && onText != nil {
			onText(chunk.Choices[0].Delta.Content)
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	turn := Turn{StopReason: "end"}
	if len(acc.Choices) == 0 {
		return &turn, nil
	}
	ch := acc.Choices[0]
	turn.Text = ch.Message.Content
	turn.Native = ch.Message.ToParam()
	for _, tc := range ch.Message.ToolCalls {
		id := tc.ID
		if id == "" { // some local servers omit ids: use neutral history
			id = newCallID("call")
			turn.Native = nil
		}
		args := tc.Function.Arguments
		if args == "" {
			args = "{}"
		}
		turn.Calls = append(turn.Calls, ToolCall{ID: id, Name: tc.Function.Name, Args: json.RawMessage(args)})
	}
	switch {
	case len(turn.Calls) > 0:
		turn.StopReason = "tool_use"
	case ch.FinishReason == "length":
		turn.StopReason = "max_tokens"
	case ch.FinishReason == "content_filter":
		turn.StopReason = "refusal"
	}
	return &turn, nil
}
