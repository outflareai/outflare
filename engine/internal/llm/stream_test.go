package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sse writes server-sent events, one data line each.
func sse(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		fmt.Fprintf(w, "data: %s\n\n", e)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

func TestOpenAIStreams(t *testing.T) {
	const key = "sk-test-not-a-real-key"
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			t.Errorf("auth header: %q", r.Header.Get("Authorization"))
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &sent)
		sse(w,
			`{"model":"gpt-test-1","choices":[{"delta":{"content":"Hel"}}]}`,
			`{"choices":[{"delta":{"content":"lo"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"find","arguments":"{\"q\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"acme\"}"}}]},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":5}}`,
			`[DONE]`)
	}))
	defer srv.Close()
	m, err := New(Config{Provider: OpenAI, Key: key, BaseURL: srv.URL, Model: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	res, err := m.Chat(context.Background(), Request{System: "s", Messages: []Message{{Role: "user", Text: "hi"}},
		Tools: []Tool{{Name: "find", Schema: map[string]any{"type": "object"}}}, OnText: func(d string) { got = append(got, d) }})
	if err != nil {
		t.Fatal(err)
	}
	if sent["stream"] != true {
		t.Fatalf("not streamed: %v", sent)
	}
	if strings.Join(got, "|") != "Hel|lo" || res.Message.Text != "Hello" {
		t.Fatalf("text: %v %q", got, res.Message.Text)
	}
	if len(res.Message.ToolCalls) != 1 || res.Message.ToolCalls[0].ID != "call_1" || string(res.Message.ToolCalls[0].Input) != `{"q":"acme"}` {
		t.Fatalf("tool calls: %+v", res.Message.ToolCalls)
	}
	if res.Stop != StopToolUse || res.Usage.InputTokens != 12 || res.Usage.OutputTokens != 5 || res.Usage.Model != "gpt-test-1" {
		t.Fatalf("response: %+v", res)
	}

	// A rate limit before the stream starts is retried, like a call
	// without streaming; the reply still arrives once.
	calls := 0
	flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(429)
			return
		}
		sse(w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, `[DONE]`)
	}))
	defer flaky.Close()
	m, _ = New(Config{Provider: OpenAI, Key: key, BaseURL: flaky.URL, Model: "gpt-test"})
	got = nil
	res, err = m.Chat(context.Background(), Request{OnText: func(d string) { got = append(got, d) }})
	if err != nil || calls != 2 || strings.Join(got, "") != "ok" || res.Message.Text != "ok" {
		t.Fatalf("retry: %v calls=%d got=%v", err, calls, got)
	}

	// A refused key is ErrAuth, streamed or not.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer bad.Close()
	m, _ = New(Config{Provider: OpenAI, Key: key, BaseURL: bad.URL, Model: "gpt-test"})
	if _, err := m.Chat(context.Background(), Request{OnText: func(string) {}}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("401: %v", err)
	}
}

func TestClaudeStreams(t *testing.T) {
	const key = "sk-ant-test-not-a-real-key"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != key {
			t.Errorf("key header: %q", r.Header.Get("X-Api-Key"))
		}
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		if body["stream"] != true {
			t.Errorf("not streamed: %v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range []struct{ name, data string }{
			{"message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"usage":{"input_tokens":9,"output_tokens":0}}}`},
			{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi "}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"there"}}`},
			{"content_block_stop", `{"type":"content_block_stop","index":0}`},
			{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`},
			{"message_stop", `{"type":"message_stop"}`},
		} {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.name, e.data)
		}
	}))
	defer srv.Close()
	m, err := New(Config{Provider: Claude, Key: key, BaseURL: srv.URL, Model: "claude-test"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	res, err := m.Chat(context.Background(), Request{System: "s", Messages: []Message{{Role: "user", Text: "hi"}}, OnText: func(d string) { got = append(got, d) }})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "|") != "Hi |there" || res.Message.Text != "Hi there" || res.Stop != StopEnd {
		t.Fatalf("stream: %v %+v", got, res)
	}
	if res.Usage.InputTokens != 9 || res.Usage.OutputTokens != 4 {
		t.Fatalf("usage: %+v", res.Usage)
	}
}
