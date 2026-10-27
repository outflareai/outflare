package llm

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
)

// claudeModel calls the Claude API through Anthropic's SDK.
type claudeModel struct{ c Config }

func (m *claudeModel) Provider() Provider { return m.c.Provider }
func (m *claudeModel) ID() string         { return m.c.Model }

// claudeClient is a client that uses only the workspace's key. Environment
// defaults are off, so an operator's own ANTHROPIC_API_KEY, profile or
// custom headers never mix into a workspace's calls.
func claudeClient(c Config) anthropic.Client {
	opts := []option.RequestOption{
		option.WithoutEnvironmentDefaults(),
		option.WithAPIKey(c.Key),
		option.WithBaseURL(c.Provider.Base(c.BaseURL)),
		option.WithHTTPClient(httpClient(c.HTTP)),
	}
	return anthropic.NewClient(opts...)
}

// The most web searches one answer may run when Request.WebSearch is set.
const maxWebSearches = 3

// Models that re-run a refused request on a fallback model server-side.
var claudeFallbacks = []string{"claude-fable-5-1", "claude-opus-5-5", "claude-opus-5", "claude-sonnet-5-5"}

func (m *claudeModel) Chat(ctx context.Context, req Request) (Response, error) {
	client := claudeClient(m.c)
	system := []anthropic.BetaTextBlockParam{{Text: req.System}}
	if req.Reference != "" {
		system = append(system, anthropic.BetaTextBlockParam{Text: req.Reference})
	}
	// Cache the stable prefix: tools, the system prompt and the reference.
	system[len(system)-1].CacheControl = anthropic.NewBetaCacheControlEphemeralParam()

	messages := make([]anthropic.BetaMessageParam, 0, len(req.Messages))
	for _, msg := range req.Messages {
		if n, ok := msg.native.(anthropic.BetaMessage); ok {
			messages = append(messages, n.ToParam())
			continue
		}
		var blocks []anthropic.BetaContentBlockParamUnion
		for _, r := range msg.ToolResults {
			blocks = append(blocks, anthropic.NewBetaToolResultBlock(r.CallID, r.Content, r.IsError))
		}
		if msg.Text != "" {
			blocks = append(blocks, anthropic.NewBetaTextBlock(msg.Text))
		}
		if msg.Role == "assistant" {
			messages = append(messages, anthropic.BetaMessageParam{Role: anthropic.BetaMessageParamRoleAssistant, Content: blocks})
		} else {
			messages = append(messages, anthropic.NewBetaUserMessage(blocks...))
		}
	}
	var tools []anthropic.BetaToolUnionParam
	for _, t := range req.Tools {
		props, _ := t.Schema["properties"]
		required, _ := t.Schema["required"].([]string)
		tools = append(tools, anthropic.BetaToolUnionParam{OfTool: &anthropic.BetaToolParam{
			Name: t.Name, Description: anthropic.String(t.Description),
			InputSchema: anthropic.BetaToolInputSchemaParam{Properties: props, Required: required},
		}})
	}
	if req.WebSearch {
		tools = append(tools, anthropic.BetaToolUnionParam{OfWebSearchTool20250305: &anthropic.BetaWebSearchTool20250305Param{MaxUses: anthropic.Int(maxWebSearches)}})
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 16000
	}
	params := anthropic.BetaMessageNewParams{
		Model:        anthropic.Model(m.c.Model),
		MaxTokens:    int64(maxTokens),
		System:       system,
		Messages:     messages,
		Tools:        tools,
		OutputConfig: anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortMedium},
	}
	if req.JSONSchema != nil {
		params.OutputConfig.Format = anthropic.BetaJSONOutputFormatParam{Schema: req.JSONSchema}
	}
	if slices.Contains(claudeFallbacks, m.c.Model) {
		// On a refusal, the API re-runs the request on a fallback model.
		params.Fallbacks = anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
	}
	msg, err := client.Beta.Messages.New(ctx, params)
	if err != nil {
		return Response{Usage: Usage{Model: m.c.Model}}, claudeErr(err, m.c.Key)
	}
	res := Response{
		Message: Message{Role: "assistant", native: *msg},
		Usage: Usage{Model: string(msg.Model), InputTokens: msg.Usage.InputTokens, OutputTokens: msg.Usage.OutputTokens,
			CacheRead: msg.Usage.CacheReadInputTokens, CacheWrite: msg.Usage.CacheCreationInputTokens,
			WebSearches: msg.Usage.ServerToolUse.WebSearchRequests},
	}
	var text []string
	seen := map[string]bool{}
	for _, block := range msg.Content {
		switch b := block.AsAny().(type) {
		case anthropic.BetaTextBlock:
			text = append(text, b.Text)
			for _, c := range b.Citations {
				if c.Type == "web_search_result_location" && c.URL != "" && !seen[c.URL] {
					seen[c.URL] = true
					res.Citations = append(res.Citations, Citation{URL: c.URL, Title: c.Title})
				}
			}
		case anthropic.BetaToolUseBlock:
			res.Message.ToolCalls = append(res.Message.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Input: []byte(b.JSON.Input.Raw())})
		}
	}
	res.Message.Text = strings.TrimSpace(strings.Join(text, "\n"))
	switch msg.StopReason {
	case anthropic.BetaStopReasonRefusal:
		res.Stop = StopRefusal
	case anthropic.BetaStopReasonMaxTokens:
		res.Stop = StopMaxTokens
	case anthropic.BetaStopReasonToolUse:
		res.Stop = StopToolUse
	default:
		res.Stop = StopEnd
	}
	return res, nil
}

func claudeErr(err error, key string) error {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode == 401 || apiErr.StatusCode == 403 {
			return fmt.Errorf("Claude API returned %d: %w", apiErr.StatusCode, ErrAuth)
		}
		return fmt.Errorf("Claude API returned %d", apiErr.StatusCode)
	}
	return scrubErr(err, key)
}

func listClaudeModels(ctx context.Context, c Config) ([]ModelInfo, error) {
	client := claudeClient(c)
	page, err := client.Models.List(ctx, anthropic.ModelListParams{Limit: anthropic.Int(100)})
	if err != nil {
		return nil, claudeErr(err, c.Key)
	}
	out := []ModelInfo{}
	for _, m := range page.Data {
		out = append(out, ModelInfo{ID: m.ID, Name: m.DisplayName})
	}
	return out, nil
}
