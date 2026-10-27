package planner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/llm"
)

func model(t *testing.T, p llm.Provider, base, name string) llm.Model {
	t.Helper()
	m, err := llm.New(llm.Config{Provider: p, Key: "sk-test", BaseURL: base, Model: name})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPlanWithClaude(t *testing.T) {
	draft := Draft{
		Name: "Fintech CFOs, Australia", Goal: "Book 10 meetings with fintech CFOs in Australia.",
		GoalMetric: "meetings", GoalTarget: 10, EndsOn: "2026-11-30",
		Steps:    []Step{{Title: "Find leads", Detail: "Search Apollo for CFOs."}, {Title: "Verify", Detail: "Verify with ZeroBounce."}},
		MaxSends: 3000,
		Connectors: []ConnectorNeed{
			{ID: "zerobounce", Why: "verify emails"},
			{ID: "zerobounce", Why: "duplicate"},
			{ID: "apollo", Why: "already connected"},
			{ID: "nope", Why: "not in the catalog"},
		},
	}
	answer, _ := json.Marshal(draft)
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "sk-test" {
			t.Errorf("api key header = %q", r.Header.Get("X-Api-Key"))
		}
		if !strings.Contains(r.Header.Get("Anthropic-Beta"), "server-side-fallback-2026-07-01") {
			t.Errorf("beta header = %q", r.Header.Get("Anthropic-Beta"))
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus-5-5",
			"content":     []any{map[string]any{"type": "text", "text": string(answer)}},
			"stop_reason": "end_turn",
			"usage":       map[string]any{"input_tokens": 1200, "output_tokens": 400},
		})
	}))
	defer srv.Close()

	snap := Snapshot{
		Today:     time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC),
		Connected: []string{"apollo"},
		Catalog:   []CatalogItem{{ID: "zerobounce", Name: "ZeroBounce"}, {ID: "apollo", Name: "Apollo"}},
	}
	d, usage, err := Plan(context.Background(), LLM{M: model(t, llm.Claude, srv.URL, "")}, "book 10 meetings with fintech CFOs in Australia by 30 November", snap)
	if err != nil {
		t.Fatal(err)
	}
	if got["model"] != llm.Claude.DefaultModel || got["fallbacks"] != "default" {
		t.Errorf("request model/fallbacks = %v / %v", got["model"], got["fallbacks"])
	}
	oc, _ := got["output_config"].(map[string]any)
	if f, _ := oc["format"].(map[string]any); f["type"] != "json_schema" {
		t.Errorf("output_config = %v", oc)
	}
	if usage.InputTokens != 1200 || usage.OutputTokens != 400 {
		t.Errorf("usage = %+v", usage)
	}
	if len(d.Connectors) != 1 || d.Connectors[0].ID != "zerobounce" {
		t.Errorf("connectors = %+v", d.Connectors)
	}
	if d.EndsOn != "2026-11-30" || d.GoalTarget != 10 {
		t.Errorf("draft = %+v", d)
	}
	if md := d.Markdown(); !strings.HasPrefix(md, "1. **Find leads.** Search Apollo") {
		t.Errorf("markdown = %q", md)
	}
}

func TestDraftCheck(t *testing.T) {
	snap := Snapshot{Today: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)}
	d := Draft{Name: "x", Goal: "y", GoalMetric: "revenue", GoalTarget: 5, EndsOn: "2020-01-01", Steps: []Step{{Title: "a"}}, MaxSends: -1}
	if err := d.check(snap); err != nil {
		t.Fatal(err)
	}
	if d.GoalMetric != "" || d.GoalTarget != 0 || d.EndsOn != "" || d.MaxSends != 0 {
		t.Errorf("check kept bad values: %+v", d)
	}
	if err := (&Draft{Name: "x", Goal: "y"}).check(snap); err == nil {
		t.Error("a draft with no steps should be rejected")
	}
}

func TestPlanWithOpenAICompatible(t *testing.T) {
	answer, _ := json.Marshal(Draft{Name: "x", Goal: "Book 5 meetings.", GoalMetric: "meetings", GoalTarget: 5, Steps: []Step{{Title: "Find leads"}}})
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("request = %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   "gpt-test",
			"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": string(answer)}}},
			"usage":   map[string]any{"prompt_tokens": 900, "completion_tokens": 300, "prompt_tokens_details": map[string]any{"cached_tokens": 100}},
		})
	}))
	defer srv.Close()
	d, usage, err := Plan(context.Background(), LLM{M: model(t, llm.OpenAI, srv.URL, "gpt-test")}, "book 5 meetings", Snapshot{Today: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if rf, _ := got["response_format"].(map[string]any); rf["type"] != "json_schema" {
		t.Errorf("response_format = %v", got["response_format"])
	}
	if got["max_completion_tokens"] == nil {
		t.Errorf("OpenAI takes max_completion_tokens: %v", got)
	}
	if d.GoalTarget != 5 || usage.InputTokens != 900 || usage.LLM.CacheRead != 100 || usage.Model != "gpt-test" {
		t.Errorf("draft %+v usage %+v", d, usage)
	}
}
