package seo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

func TestDataForSEOAnswer(t *testing.T) {
	cl, calls := fake(t, map[string]any{
		"POST /v3/ai_optimization/chat_gpt/llm_scraper/live/advanced": dfs(0.004, map[string]any{
			"model":     "gpt-6",
			"markdown":  "The best picks are **Northpeak** and **Acme**.",
			"check_url": "https://chatgpt.com/?q=best+widgets",
			"brand_entities": []any{
				map[string]any{"title": "Northpeak"}, map[string]any{"title": "northpeak"}, map[string]any{"title": "Acme"},
			},
			"sources": []any{
				map[string]any{"url": "https://www.reviews.example/best-widgets", "domain": "[reviews](x)", "title": " Best widgets "},
				map[string]any{"url": "https://www.reviews.example/best-widgets"},
				map[string]any{"url": "javascript:alert(1)"},
			},
			"search_results": []any{
				map[string]any{"url": "https://www.reviews.example/best-widgets", "title": "Best widgets"},
				map[string]any{"url": "https://blog.acme.example/widgets", "title": "Acme widgets"},
			},
			"fan_out_queries": []any{"best widgets 2026", " ", "northpeak vs acme"},
		}),
	})
	creds := c.Creds{"login": "jo@example.com", "password": "pw"}
	d := DataForSEO{}
	var _ c.AnswerReader = d
	if !d.Definition().Has(c.CapAIAnswers) || len(d.AnswerEngines()) != 2 || d.EstimateAnswer("chatgpt").USD != 0.004 {
		t.Fatalf("definition: %+v %+v", d.Definition(), d.AnswerEngines())
	}
	got, err := d.Answer(ctx, cl, creds, "chatgpt", c.AnswerQuery{Prompt: "  best widgets for a small team  ", Country: "us", WebSearch: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "gpt-6" || !strings.Contains(got.Text, "Northpeak") || got.CheckURL != "https://chatgpt.com/?q=best+widgets" ||
		strings.Join(got.Brands, ",") != "Northpeak,Acme" || strings.Join(got.Searches, "|") != "best widgets 2026|northpeak vs acme" ||
		got.Cost.USD != 0.004 || got.Cost.Estimated {
		t.Fatalf("answer: %+v", got)
	}
	if len(got.Cited) != 1 || got.Cited[0].Domain != "reviews.example" || got.Cited[0].Title != "Best widgets" || got.Cited[0].Position != 1 {
		t.Fatalf("cited: %+v", got.Cited)
	}
	if len(got.Read) != 2 || got.Read[1].Domain != "blog.acme.example" || got.Read[1].Position != 2 {
		t.Fatalf("read: %+v", got.Read)
	}
	body := (*calls)[0]
	for _, want := range []string{`"keyword":"best widgets for a small team"`, `"location_code":2840`, `"language_code":"en"`, `"force_web_search":true`} {
		if !strings.Contains(body, want) {
			t.Fatalf("request %s lacks %s", body, want)
		}
	}
	if _, err := d.Answer(ctx, cl, creds, "claude", c.AnswerQuery{Prompt: "x"}); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("engine: %v", err)
	}
	if _, err := d.Answer(ctx, cl, creds, "gemini", c.AnswerQuery{Prompt: "x", Country: "ZZ"}); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("country: %v", err)
	}
}

// An assistant takes a minute or more to answer, longer than the client's
// usual timeout: the answer call waits for it, and one that still runs out
// of time says so rather than that DataForSEO is down.
func TestDataForSEOAnswerWaitsForSlowAnswers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		json.NewEncoder(w).Encode(dfs(0.004, map[string]any{"model": "gpt-6", "markdown": "Northpeak."}))
	}))
	t.Cleanup(srv.Close)
	cl := &c.Client{HTTP: &http.Client{Timeout: 50 * time.Millisecond}, BaseURLs: map[string]string{"dataforseo": srv.URL}}
	creds := c.Creds{"login": "jo@example.com", "password": "pw"}
	got, err := DataForSEO{}.Answer(ctx, cl, creds, "chatgpt", c.AnswerQuery{Prompt: "best widgets"})
	if err != nil || got.Text != "Northpeak." || got.Cost.USD != 0.004 {
		t.Fatalf("a slow answer was cut off: %+v %v", got, err)
	}
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	_, err = DataForSEO{}.Answer(short, cl, creds, "chatgpt", c.AnswerQuery{Prompt: "best widgets"})
	if c.KindOf(err) != c.ErrUnavailable || !c.IsTimeout(err) {
		t.Fatalf("an answer that ran out of time: %v", err)
	}
	if _, err := (DataForSEO{}).Answer(ctx, &c.Client{BaseURLs: map[string]string{"dataforseo": "http://127.0.0.1:1"}}, creds, "chatgpt", c.AnswerQuery{Prompt: "x"}); err == nil || c.IsTimeout(err) {
		t.Fatalf("an unreachable service is not a timeout: %v", err)
	}
}
