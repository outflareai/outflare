package seo

import (
	"strings"
	"testing"

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
