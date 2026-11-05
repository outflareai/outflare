package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var png = Image{MediaType: "image/png", Data: []byte("\x89PNG\r\n\x1a\nnot-a-real-image")}

func TestCheckImage(t *testing.T) {
	if CheckImage(png) != nil {
		t.Fatal("png refused")
	}
	if CheckImage(Image{MediaType: "image/tiff", Data: []byte("x")}) == nil {
		t.Fatal("tiff accepted")
	}
	if CheckImage(Image{MediaType: "image/png", Data: make([]byte, MaxImageBytes+1)}) == nil {
		t.Fatal("too large accepted")
	}
	if SupportsImages(DeepSeek.ID) || !SupportsImages(Claude.ID) || !SupportsImages(OpenAI.ID) {
		t.Fatal("SupportsImages")
	}
}

func TestOpenAISendsImages(t *testing.T) {
	var sent struct {
		Messages []json.RawMessage `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &sent)
		w.Write([]byte(`{"model":"m","choices":[{"message":{"role":"assistant","content":"A login page."},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	m, _ := New(Config{Provider: OpenAI, Key: "sk-test-not-a-real-key", BaseURL: srv.URL, Model: "gpt-test"})
	res, err := m.Chat(context.Background(), Request{Messages: []Message{
		{Role: "user", Text: "What is this?", Images: []Image{png}},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "screenshot", Input: json.RawMessage(`{}`)}}},
		{Role: "user", ToolResults: []ToolResult{{CallID: "c1", Content: "taken", Images: []Image{png}}}},
	}})
	if err != nil || res.Message.Text != "A login page." {
		t.Fatalf("%v %+v", err, res)
	}
	all := ""
	for _, m := range sent.Messages {
		all += string(m) + "\n"
	}
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png.Data)
	if strings.Count(all, want) != 2 || !strings.Contains(all, `"type":"image_url"`) || !strings.Contains(all, `"role":"tool"`) {
		t.Fatalf("messages:\n%s", all)
	}
}

func TestClaudeSendsImages(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"A chart."}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`))
	}))
	defer srv.Close()
	m, _ := New(Config{Provider: Claude, Key: "sk-ant-test-not-a-real-key", BaseURL: srv.URL, Model: "claude-test"})
	res, err := m.Chat(context.Background(), Request{Messages: []Message{
		{Role: "user", Text: "Describe", Images: []Image{png}},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "t1", Name: "shot", Input: json.RawMessage(`{}`)}}, Text: "Taking one"},
		{Role: "user", ToolResults: []ToolResult{{CallID: "t1", Content: "here", Images: []Image{png}}}},
	}})
	if err != nil || res.Message.Text != "A chart." {
		t.Fatalf("%v %+v", err, res)
	}
	data := base64.StdEncoding.EncodeToString(png.Data)
	if strings.Count(body, data) != 2 || !strings.Contains(body, `"media_type":"image/png"`) || !strings.Contains(body, `"tool_result"`) {
		t.Fatalf("body: %s", body)
	}
	// The system prefix and the conversation so far are both cached, so
	// an agent's next step reads them back cheaply.
	if strings.Count(body, `"cache_control":{"type":"ephemeral"}`) != 2 {
		t.Fatalf("cache breakpoints: %s", body)
	}
	// The assistant message was rebuilt from stored state, so its tool
	// call is sent as a tool_use block the tool_result can answer.
	if !strings.Contains(body, `"type":"tool_use"`) || !strings.Contains(body, `"id":"t1"`) || !strings.Contains(body, `"name":"shot"`) {
		t.Fatalf("tool_use missing: %s", body)
	}
}
