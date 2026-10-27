package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// openAIModel calls a provider that speaks OpenAI's chat completions API:
// OpenAI itself, Gemini (through Google's OpenAI-compatible endpoint),
// Mistral and OpenRouter.
type openAIModel struct {
	c    Config
	http *http.Client
}

func (m *openAIModel) Provider() Provider { return m.c.Provider }
func (m *openAIModel) ID() string         { return m.c.Model }

type oaMessage struct {
	Role       string       `json:"role"`
	Content    *string      `json:"content"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
	Refusal    *string      `json:"refusal,omitempty"`
	// Pages a search model or OpenRouter's web plugin cited.
	Annotations []struct {
		Type        string `json:"type"`
		URLCitation struct {
			URL   string `json:"url"`
			Title string `json:"title"`
		} `json:"url_citation"`
	} `json:"annotations,omitempty"`
}

type oaToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message      oaMessage `json:"message"`
		FinishReason string    `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		Details          struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		// OpenRouter's usage accounting: what the call cost, in dollars.
		Cost float64 `json:"cost"`
	} `json:"usage"`
}

func str(s string) *string { return &s }

func (m *openAIModel) Chat(ctx context.Context, req Request) (Response, error) {
	system := req.System
	if req.Reference != "" {
		system += "\n\n" + req.Reference
	}
	msgs := []oaMessage{{Role: "system", Content: str(system)}}
	for _, msg := range req.Messages {
		if msg.Role == "assistant" {
			am := oaMessage{Role: "assistant", Content: str(msg.Text)}
			for _, c := range msg.ToolCalls {
				tc := oaToolCall{ID: c.ID, Type: "function"}
				tc.Function.Name, tc.Function.Arguments = c.Name, string(c.Input)
				am.ToolCalls = append(am.ToolCalls, tc)
			}
			msgs = append(msgs, am)
			continue
		}
		for _, r := range msg.ToolResults {
			content := r.Content
			if r.IsError {
				content = "ERROR: " + content
			}
			msgs = append(msgs, oaMessage{Role: "tool", ToolCallID: r.CallID, Content: str(content)})
		}
		if msg.Text != "" {
			msgs = append(msgs, oaMessage{Role: "user", Content: str(msg.Text)})
		}
	}
	body := map[string]any{"model": m.c.Model, "messages": msgs}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 16000
	}
	if m.c.Provider.ID == OpenAI.ID {
		body["max_completion_tokens"] = maxTokens
	} else {
		body["max_tokens"] = maxTokens
	}
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
				"name": t.Name, "description": t.Description, "parameters": t.Schema}})
		}
		body["tools"] = tools
	}
	if m.c.Provider.ReportsCost {
		body["usage"] = map[string]any{"include": true}
	}
	if req.WebSearch && SupportsWebSearch(m.c.Provider.ID, m.c.Model) {
		if m.c.Provider.ID == OpenRouter.ID {
			body["plugins"] = []map[string]any{{"id": "web", "max_results": 5}}
		} else {
			body["web_search_options"] = map[string]any{}
		}
	}
	if req.JSONSchema != nil {
		body["response_format"] = map[string]any{"type": "json_schema",
			"json_schema": map[string]any{"name": "answer", "schema": req.JSONSchema}}
	}
	var out oaResponse
	if err := m.do(ctx, http.MethodPost, "/chat/completions", body, &out); err != nil {
		return Response{Usage: Usage{Model: m.c.Model}}, err
	}
	model := out.Model
	if model == "" {
		model = m.c.Model
	}
	cached := min(out.Usage.Details.CachedTokens, out.Usage.PromptTokens)
	res := Response{Usage: Usage{Model: model, InputTokens: out.Usage.PromptTokens - cached, OutputTokens: out.Usage.CompletionTokens, CacheRead: cached}}
	if m.c.Provider.ReportsCost {
		res.Usage.CostUSD = out.Usage.Cost
	}
	if len(out.Choices) == 0 {
		return res, fmt.Errorf("%s returned no answer", m.c.Provider.Name)
	}
	ch := out.Choices[0]
	seen := map[string]bool{}
	for _, a := range ch.Message.Annotations {
		if u := a.URLCitation.URL; a.Type == "url_citation" && u != "" && !seen[u] {
			seen[u] = true
			res.Citations = append(res.Citations, Citation{URL: u, Title: a.URLCitation.Title})
		}
	}
	res.Message = Message{Role: "assistant"}
	if ch.Message.Content != nil {
		res.Message.Text = strings.TrimSpace(*ch.Message.Content)
	}
	for _, tc := range ch.Message.ToolCalls {
		args := strings.TrimSpace(tc.Function.Arguments)
		if args == "" {
			args = "{}"
		}
		res.Message.ToolCalls = append(res.Message.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Input: json.RawMessage(args)})
	}
	switch {
	case ch.Message.Refusal != nil && *ch.Message.Refusal != "", ch.FinishReason == "content_filter":
		res.Stop = StopRefusal
	case ch.FinishReason == "length":
		res.Stop = StopMaxTokens
	case len(res.Message.ToolCalls) > 0:
		res.Stop = StopToolUse
	default:
		res.Stop = StopEnd
	}
	return res, nil
}

// Most of an answer read, so a broken or hostile server can't fill memory.
const maxAnswer = 8 << 20

// do makes one call, retrying rate limits and server errors twice.
func (m *openAIModel) do(ctx context.Context, method, path string, body, out any) error {
	return doOpenAI(ctx, m.c, m.http, method, path, body, out)
}

func doOpenAI(ctx context.Context, c Config, h *http.Client, method, path string, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	url := c.Provider.Base(c.BaseURL) + path
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt*2) * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.Key)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := h.Do(req)
		if err != nil {
			lastErr = scrubErr(fmt.Errorf("could not reach %s: %w", c.Provider.Name, err), c.Key)
			continue
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
		resp.Body.Close()
		if err != nil {
			lastErr = scrubErr(err, c.Key)
			continue
		}
		switch {
		case resp.StatusCode == 401 || resp.StatusCode == 403:
			return fmt.Errorf("%s API returned %d: %w", c.Provider.Name, resp.StatusCode, ErrAuth)
		case resp.StatusCode == 429 || resp.StatusCode >= 500:
			lastErr = providerErr(c, resp.StatusCode, data)
			continue
		case resp.StatusCode >= 300:
			return providerErr(c, resp.StatusCode, data)
		}
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("%s returned an answer Outflare could not read", c.Provider.Name)
		}
		return nil
	}
	return lastErr
}

// providerErr words a provider's error, scrubbed and cut short: the text
// can quote the request, so it never carries more than a line.
func providerErr(c Config, status int, data []byte) error {
	var e struct {
		Error json.RawMessage `json:"error"`
	}
	msg := ""
	if json.Unmarshal(data, &e) == nil && len(e.Error) > 0 {
		var obj struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(e.Error, &obj) == nil && obj.Message != "" {
			msg = obj.Message
		} else {
			_ = json.Unmarshal(e.Error, &msg)
		}
	}
	// Gemini wraps its errors in a list.
	var list []struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if msg == "" && json.Unmarshal(data, &list) == nil && len(list) > 0 {
		msg = list[0].Error.Message
	}
	msg = strings.Join(strings.Fields(Scrub(msg, c.Key)), " ")
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	if msg == "" {
		return fmt.Errorf("%s API returned %d", c.Provider.Name, status)
	}
	return fmt.Errorf("%s API returned %d: %s", c.Provider.Name, status, msg)
}

// Words in model ids that are not chat models.
var notChat = []string{"embed", "tts", "whisper", "transcribe", "dall-e", "image", "imagen", "moderation", "audio", "realtime",
	"search", "ocr", "aqa", "veo", "babbage", "davinci", "computer-use", "codex"}

func listOpenAIModels(ctx context.Context, c Config, h *http.Client) ([]ModelInfo, error) {
	if c.Provider.ID == OpenRouter.ID {
		// OpenRouter lists its models to anyone, so ask about the key
		// itself first: that proves it works, at no cost.
		var key struct {
			Data json.RawMessage `json:"data"`
		}
		if err := doOpenAI(ctx, c, h, http.MethodGet, "/key", nil, &key); err != nil {
			return nil, err
		}
	}
	var out struct {
		Data []struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			Capabilities *struct {
				FunctionCalling *bool `json:"function_calling"`
			} `json:"capabilities"`
		} `json:"data"`
	}
	if err := doOpenAI(ctx, c, h, http.MethodGet, "/models", nil, &out); err != nil {
		return nil, err
	}
	models := []ModelInfo{}
	seen := map[string]bool{}
	for _, d := range out.Data {
		id := strings.TrimPrefix(d.ID, "models/")
		low := strings.ToLower(id)
		if id == "" || seen[id] || containsAny(low, notChat) {
			continue
		}
		// Mistral says which models can call tools; the agent needs them.
		if d.Capabilities != nil && d.Capabilities.FunctionCalling != nil && !*d.Capabilities.FunctionCalling {
			continue
		}
		if c.Provider.ID == Gemini.ID && !strings.HasPrefix(low, "gemini") {
			continue
		}
		seen[id] = true
		name := d.Name
		if name == "" || name == d.ID {
			name = id
		}
		models = append(models, ModelInfo{ID: id, Name: name})
	}
	return models, nil
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}
