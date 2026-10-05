package ai

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// DefaultAnthropicModel is used when no model is configured.
const DefaultAnthropicModel = "claude-opus-5-5"

// models that accept the server-side refusal fallback ("default" routing).
var anthropicFallbackModels = map[string]bool{
	"claude-fable-5-1":  true,
	"claude-opus-5-5":   true,
	"claude-opus-5":     true,
	"claude-sonnet-5-5": true,
}

// Anthropic talks to the Claude API through the official Go SDK.
type Anthropic struct {
	id     string
	client anthropic.Client
}

// NewAnthropic builds a Claude provider. apiKey may be empty (the SDK then
// uses ANTHROPIC_API_KEY / ant auth profiles); baseURL is optional.
func NewAnthropic(id, apiKey, baseURL string) *Anthropic {
	var opts []option.RequestOption
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &Anthropic{id: id, client: anthropic.NewClient(opts...)}
}

func (a *Anthropic) ID() string { return a.id }

func (a *Anthropic) Models(ctx context.Context) ([]string, error) {
	pager := a.client.Beta.Models.ListAutoPaging(ctx, anthropic.BetaModelListParams{})
	var out []string
	for pager.Next() {
		out = append(out, pager.Current().ID)
	}
	return out, pager.Err()
}

func (a *Anthropic) tools(specs []ToolSpec) []anthropic.BetaToolUnionParam {
	out := make([]anthropic.BetaToolUnionParam, 0, len(specs))
	for _, s := range specs {
		t := anthropic.BetaToolParam{
			Name:        s.Name,
			Description: anthropic.String(s.Description),
			InputSchema: anthropic.BetaToolInputSchemaParam{
				Properties: s.Properties,
				Required:   s.Required,
			},
			// stream large inputs (manifests) as generated; inputs are
			// validated by the agent before running the tool.
			EagerInputStreaming: anthropic.Bool(true),
		}
		out = append(out, anthropic.BetaToolUnionParam{OfTool: &t})
	}
	return out
}

func (a *Anthropic) messages(history []Message) []anthropic.BetaMessageParam {
	var out []anthropic.BetaMessageParam
	for _, m := range history {
		switch m.Role {
		case RoleUser:
			out = append(out, anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(m.Text)))
		case RoleAssistant:
			if p, ok := m.Native.(anthropic.BetaMessageParam); ok && m.NativeFrom == a.id {
				out = append(out, p) // unchanged: keeps thinking blocks valid
				continue
			}
			var blocks []anthropic.BetaContentBlockParamUnion
			if m.Text != "" {
				blocks = append(blocks, anthropic.NewBetaTextBlock(m.Text))
			}
			for _, c := range m.Calls {
				var in any = map[string]any{}
				_ = json.Unmarshal(c.Args, &in)
				blocks = append(blocks, anthropic.NewBetaToolUseBlock(c.ID, in, c.Name))
			}
			if len(blocks) == 0 {
				blocks = append(blocks, anthropic.NewBetaTextBlock("(no output)"))
			}
			out = append(out, anthropic.BetaMessageParam{Role: anthropic.BetaMessageParamRoleAssistant, Content: blocks})
		case RoleTool:
			blocks := make([]anthropic.BetaContentBlockParamUnion, 0, len(m.Results))
			for _, r := range m.Results {
				blocks = append(blocks, anthropic.NewBetaToolResultBlock(r.CallID, r.Content, r.IsError))
			}
			// all results of one assistant turn go back in a single user message
			out = append(out, anthropic.NewBetaUserMessage(blocks...))
		}
	}
	return out
}

func (a *Anthropic) Stream(ctx context.Context, req Request, onText func(string)) (*Turn, error) {
	model := req.Model
	if model == "" {
		model = DefaultAnthropicModel
	}
	maxTok := req.MaxTokens
	if maxTok == 0 {
		maxTok = 64000
	}
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: maxTok,
		Messages:  a.messages(req.Messages),
		Tools:     a.tools(req.Tools),
	}
	if req.System != "" {
		params.System = []anthropic.BetaTextBlockParam{{Text: req.System}}
	}
	if anthropicFallbackModels[model] {
		// a policy refusal is re-served by a fallback model inside the call
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
		params.Fallbacks = anthropic.BetaFallbacksParamOfDefault()
	}

	stream := a.client.Beta.Messages.NewStreaming(ctx, params)
	msg := anthropic.BetaMessage{}
	for stream.Next() {
		ev := stream.Current()
		if err := msg.Accumulate(ev); err != nil {
			return nil, err
		}
		if d, ok := ev.AsAny().(anthropic.BetaRawContentBlockDeltaEvent); ok {
			if t, ok := d.Delta.AsAny().(anthropic.BetaTextDelta); ok && onText != nil {
				onText(t.Text)
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}

	turn := Turn{Native: msg.ToParam()}
	for _, b := range msg.Content {
		switch v := b.AsAny().(type) {
		case anthropic.BetaTextBlock:
			turn.Text += v.Text
		case anthropic.BetaToolUseBlock:
			turn.Calls = append(turn.Calls, ToolCall{ID: v.ID, Name: v.Name, Args: json.RawMessage(v.JSON.Input.Raw())})
		}
	}
	switch msg.StopReason {
	case anthropic.BetaStopReasonEndTurn, anthropic.BetaStopReasonStopSequence:
		turn.StopReason = "end"
	case anthropic.BetaStopReasonToolUse:
		turn.StopReason = "tool_use"
	case anthropic.BetaStopReasonMaxTokens:
		turn.StopReason = "max_tokens"
	case anthropic.BetaStopReasonRefusal:
		turn.StopReason = "refusal"
		if turn.Text == "" {
			turn.Text = fmt.Sprintf("_The model declined this request (%s)._", msg.StopDetails.Category)
		}
	default:
		turn.StopReason = string(msg.StopReason)
	}
	return &turn, nil
}
