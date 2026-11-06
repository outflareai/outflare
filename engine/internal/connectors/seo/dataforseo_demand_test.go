package seo

import (
	"strings"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

func TestDataForSEODemand(t *testing.T) {
	cl, calls := fake(t, map[string]any{
		"POST /v3/ai_optimization/llm_mentions/search_mentions/live": dfs(0.1, map[string]any{"items": []any{
			map[string]any{"platform": "chat_gpt", "question": "  best   widgets for teams ", "ai_search_volume": 1200.0,
				"brand_entities": []any{map[string]any{"title": "Acme"}, map[string]any{"title": "acme"}, map[string]any{"title": "Northpeak"}}},
			map[string]any{"platform": "chat_gpt", "question": "Best widgets for teams"},
			map[string]any{"platform": "google", "question": "is northpeak legit", "ai_search_volume": -3.0},
			map[string]any{"platform": "google", "question": " "},
		}}),
		"POST /v3/ai_optimization/llm_mentions/multi_target_metrics/live": dfs(0.14, map[string]any{"items": []any{
			map[string]any{"key": "brand", "total": map[string]any{"mentions": 120.0, "ai_search_volume": 54000.0},
				"platform": []any{map[string]any{"key": "chat_gpt", "mentions": 70.0}, map[string]any{"key": "google", "mentions": 50.0}}},
			map[string]any{"key": "Northpeak", "total": map[string]any{"mentions": 300.0}},
			map[string]any{"total": map[string]any{"mentions": 1.0}},
		}}),
		"POST /v3/ai_optimization/ai_keyword_data/keywords_search_volume/live": dfs(0.0102, map[string]any{"items": []any{
			map[string]any{"keyword": "best widgets for teams", "ai_search_volume": 880.0},
			map[string]any{"keyword": "unknown phrase"},
		}}),
	})
	creds := c.Creds{"login": "jo@example.com", "password": "pw"}
	d := DataForSEO{}
	var _ c.DemandReader = d
	if !d.Definition().Has(c.CapAIDemand) {
		t.Fatal("no ai.demand capability")
	}
	got, err := d.AIQuestions(ctx, cl, creds, c.AIQuestionQuery{Domains: []string{"www.Northpeak.example", " "}, Keywords: []string{"northpeak"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Questions) != 2 || got.Questions[0].Question != "best widgets for teams" || got.Questions[0].Volume != 1200 ||
		got.Questions[0].Platform != "chatgpt" || strings.Join(got.Questions[0].Brands, ",") != "Acme,Northpeak" ||
		got.Questions[1].Platform != "ai_overview" || got.Questions[1].Volume != 0 || got.Cost.USD != 0.1 {
		t.Fatalf("questions: %+v", got)
	}
	body := (*calls)[0]
	for _, want := range []string{`"domain":"northpeak.example"`, `"include_subdomains":true`, `"keyword":"northpeak"`, `"location_code":2840`, `"ai_search_volume,desc"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("request %s lacks %s", body, want)
		}
	}
	if strings.Contains(body, `"platform"`) {
		t.Fatalf("US English asks both platforms: %s", body)
	}
	if _, err := d.AIQuestions(ctx, cl, creds, c.AIQuestionQuery{Domains: []string{"northpeak.example"}, Country: "DE"}); err != nil {
		t.Fatal(err)
	}
	if body := (*calls)[1]; !strings.Contains(body, `"platform":"google"`) || !strings.Contains(body, `"language_code":"de"`) {
		t.Fatalf("outside the US only AI Overviews: %s", body)
	}
	if _, err := d.AIQuestions(ctx, cl, creds, c.AIQuestionQuery{}); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("no targets: %v", err)
	}
	vol, err := d.AIVolume(ctx, cl, creds, []string{"Best  widgets for teams", "best widgets for teams", "unknown phrase", ""}, "us", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(vol.Volume) != 1 || vol.Volume["best widgets for teams"] != 880 || vol.Cost.USD != 0.0102 {
		t.Fatalf("volume: %+v", vol)
	}
	if body := (*calls)[len(*calls)-1]; !strings.Contains(body, `"keywords":["best widgets for teams","unknown phrase"]`) {
		t.Fatalf("volume request: %s", body)
	}
	bench, err := d.AIBenchmark(ctx, cl, creds, []c.AIBenchmarkTarget{
		{Key: "brand", Domains: []string{"acme.example"}, Keywords: []string{"Acme"}}, {Key: "Northpeak", Domains: []string{"northpeak.example"}}, {Key: "empty"},
	}, "US", "en")
	if err != nil {
		t.Fatal(err)
	}
	if b := bench.Counts["brand"]; b.Mentions != 120 || b.AISearchVolume != 54000 || b.Platforms["chatgpt"] != 70 || b.Platforms["ai_overview"] != 50 ||
		bench.Counts["Northpeak"].Mentions != 300 || len(bench.Counts) != 2 || bench.Cost.USD != 0.14 {
		t.Fatalf("benchmark: %+v", bench)
	}
	if body := (*calls)[len(*calls)-1]; !strings.Contains(body, `"key":"brand","target":[{"domain":"acme.example","include_subdomains":true},{"keyword":"Acme"}]`) || strings.Contains(body, `"empty"`) {
		t.Fatalf("benchmark request: %s", body)
	}
	if e := d.EstimateAIVolume(100); e.USD <= 0.01 || !e.Estimated {
		t.Fatalf("estimate: %+v", e)
	}
}
