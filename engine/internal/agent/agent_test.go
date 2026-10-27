package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/llm"
)

func newModel(t *testing.T, p llm.Provider, base, model string) llm.Model {
	t.Helper()
	m, err := llm.New(llm.Config{Provider: p, Key: testKey, BaseURL: base, Model: model})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

const testKey = "sk-ant-test-secret-key-0123456789"

type fakeCaller struct{ calls []string }

func (f *fakeCaller) Call(_ context.Context, method, path string, _ json.RawMessage, reason string) (int, []byte, error) {
	f.calls = append(f.calls, method+" "+path+" ("+reason+")")
	return 200, []byte(`{"data":[{"email":"a@x-com.example","status":"active"}]}`), nil
}

func toolUse(id, name string, input any) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}
}

func TestTurn(t *testing.T) {
	answers := []map[string]any{
		{"stop_reason": "tool_use", "content": []any{
			toolUse("t1", "call_api", map[string]any{"method": "GET", "path": "/mailboxes", "reason": "check capacity"}),
			toolUse("t2", "call_api", map[string]any{"method": "GET", "path": "/v1/workspaces/x/leads", "reason": "bad path"}),
		}},
		{"stop_reason": "tool_use", "content": []any{
			toolUse("t3", "finish_turn", map[string]any{"summary": "Checked mailboxes.", "next_check_in_minutes": 60, "done": false}),
		}},
	}
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), testKey) {
			t.Error("the API key appeared in what the model reads")
		}
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		requests = append(requests, req)
		a := answers[len(requests)-1]
		a["id"], a["type"], a["role"], a["model"] = "msg", "message", "assistant", llm.Claude.DefaultModel
		a["usage"] = map[string]any{"input_tokens": 100, "output_tokens": 20, "cache_read_input_tokens": 50}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(a)
	}))
	defer srv.Close()

	caller := &fakeCaller{}
	res, err := Agent{Model: newModel(t, llm.Claude, srv.URL, "")}.Turn(context.Background(), caller, Input{CheckIn: json.RawMessage(`{"mission":{"id":"m1"}}`), Trigger: "started", Guide: "GET /mailboxes"})
	if err != nil {
		t.Fatal(err)
	}
	if len(caller.calls) != 1 || caller.calls[0] != "GET /mailboxes (check capacity)" {
		t.Errorf("calls = %v", caller.calls)
	}
	if res.Calls != 1 || res.Summary != "Checked mailboxes." || res.NextCheckInMinutes != 60 || res.Done {
		t.Errorf("result = %+v", res)
	}
	if res.Usage.InputTokens != 200 || res.Usage.CacheRead != 100 {
		t.Errorf("usage = %+v", res.Usage)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d", len(requests))
	}
	// The second request carries both tool results, the bad path as an error.
	msgs := requests[1]["messages"].([]any)
	last, _ := json.Marshal(msgs[len(msgs)-1])
	if !strings.Contains(string(last), "HTTP 200") || !strings.Contains(string(last), "workspace-relative") {
		t.Errorf("tool results = %s", last)
	}
	if !strings.Contains(string(last), `\u003capi_response\u003e`) {
		t.Errorf("API answers should be marked as data: %s", last)
	}
	if requests[0]["fallbacks"] != "default" {
		t.Errorf("fallbacks = %v", requests[0]["fallbacks"])
	}
}

// The same turn on a provider that speaks OpenAI's API (OpenAI, Gemini,
// Mistral).
func TestTurnOpenAICompatible(t *testing.T) {
	call := func(id, name string, args any) map[string]any {
		b, _ := json.Marshal(args)
		return map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(b)}}
	}
	answers := []map[string]any{
		{"finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{
			call("c1", "call_api", map[string]any{"method": "GET", "path": "/mailboxes", "reason": "check capacity"})}}},
		{"finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "content": "", "tool_calls": []any{
			call("c2", "finish_turn", map[string]any{"summary": "Checked.", "next_check_in_minutes": 30, "done": false})}}},
	}
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "key-for-mistral-123456") {
			t.Error("the API key appeared in what the model reads")
		}
		if r.Header.Get("Authorization") != "Bearer key-for-mistral-123456" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		requests = append(requests, req)
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "mistral-test", "choices": []any{answers[len(requests)-1]},
			"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 10}})
	}))
	defer srv.Close()
	m, err := llm.New(llm.Config{Provider: llm.Mistral, Key: "key-for-mistral-123456", BaseURL: srv.URL, Model: "mistral-test"})
	if err != nil {
		t.Fatal(err)
	}
	caller := &fakeCaller{}
	res, err := Agent{Model: m}.Turn(context.Background(), caller, Input{CheckIn: json.RawMessage(`{}`), Trigger: "started", Guide: "GET /mailboxes"})
	if err != nil {
		t.Fatal(err)
	}
	if len(caller.calls) != 1 || res.Summary != "Checked." || res.NextCheckInMinutes != 30 || res.Usage.InputTokens != 200 {
		t.Errorf("calls %v result %+v", caller.calls, res)
	}
	msgs := requests[1]["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "tool" || last["tool_call_id"] != "c1" || !strings.Contains(last["content"].(string), "<api_response>") {
		t.Errorf("tool result = %v", last)
	}
	if requests[0]["max_tokens"] == nil || len(requests[0]["tools"].([]any)) != 3 {
		t.Errorf("request = %v", requests[0])
	}
}

func TestGuide(t *testing.T) {
	spec := []byte(`openapi: 3.1.0
paths:
  /v1/auth/login:
    post:
      summary: Sign in
  /v1/workspaces/{ws}/mailboxes:
    parameters: []
    get:
      operationId: listMailboxes
      summary: List mailboxes
    post:
      summary: "Add a mailbox"
components:
  schemas: {}
`)
	got := Guide(spec)
	want := "GET /mailboxes: List mailboxes\nPOST /mailboxes: Add a mailbox\n"
	if got != want {
		t.Errorf("guide = %q", got)
	}
}
