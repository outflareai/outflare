package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/aivis"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The question library: questions carry awareness, type, persona, topic
// and demand; the workspace's own AI drafts more, from the brand and its
// own Search Console queries, and they wait as suggested.
func TestAIVisibilityQuestionLibrary(t *testing.T) {
	e := freshEnv(t)
	events := track.Capture(t)
	judge := `{"questions":[
		{"text":"how do I stop getting blocked when scraping prices","awareness":"problem","question_type":"how_to","persona":"Data engineer","topic":"web scraping","competitor":"","why":"They hit blocks before they know proxies help.","search_query":"How do I stop getting blocked when scraping"},
		{"text":"best alternatives to Northpeak","awareness":"competitor","question_type":"alternatives","persona":"","topic":"proxies","competitor":"northpeak","why":"Northpeak users shopping around.","search_query":""},
		{"text":"Is Acme legit?","awareness":"brand","question_type":"about","persona":"","topic":"","competitor":"Made Up Inc","why":"Checking before buying.","search_query":"not a query it was shown"},
		{"text":"What is the best proxy provider?","awareness":"solution","question_type":"best","persona":"","topic":"","competitor":"","why":"","search_query":""},
		{"text":"best residential proxies for scraping","awareness":"solution","question_type":"best","persona":"","topic":"proxies","competitor":"","why":"Asked in ChatGPT.","search_query":"Best residential proxies for scraping"},
		{"text":"x","awareness":"brand","question_type":"about","persona":"","topic":"","competitor":"","why":"","search_query":""}
	]}`
	var bodies []map[string]any
	or := httptest.NewServer(fakeOpenRouter{bodies: &bodies, judge: &judge})
	t.Cleanup(or.Close)
	e.integrations.Client.BaseURLs["openrouter"] = or.URL
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "website": "https://www.acme.example"})
	cid := owner.must(201, "POST", base+"/ai-cohorts", map[string]any{"name": "Proxy buyers", "brand_id": "acme", "locale": "en-US",
		"competitors": []map[string]any{{"name": "Northpeak"}}})["id"].(string)
	added := owner.must(201, "POST", base+"/ai-cohorts/"+cid+"/prompts", map[string]any{"prompts": []map[string]any{
		{"text": "What is the best proxy provider?", "awareness": "solution", "question_type": "best", "persona": " Agency SEO lead ", "demand": 900},
	}})["data"].([]any)[0].(map[string]any)
	if added["intent"] != "discovery" || added["persona"] != "Agency SEO lead" || added["demand"] != 900.0 || added["demand_source"] != "manual" ||
		added["status"] != "active" || added["source"] != "manual" {
		t.Fatalf("added: %v", added)
	}
	pid := added["id"].(string)
	owner.must(400, "POST", base+"/ai-cohorts/"+cid+"/prompts", map[string]any{"prompts": []map[string]any{{"text": "Which is cheapest?", "awareness": "curious"}}})
	owner.must(400, "POST", base+"/ai-cohorts/"+cid+"/prompts", map[string]any{"prompts": []map[string]any{{"text": "Which is cheapest?", "question_type": "rant"}}})
	owner.must(400, "PATCH", base+"/ai-cohorts/"+cid+"/prompts/"+pid, map[string]any{"status": "suggested"})
	p := owner.must(200, "PATCH", base+"/ai-cohorts/"+cid+"/prompts/"+pid, map[string]any{"demand": -1, "topic": "proxies"})
	if p["demand"] != nil || p["demand_source"] != "" || p["topic"] != "proxies" || p["awareness"] != "solution" {
		t.Fatalf("patched: %v", p)
	}

	// No AI model for the ai_visibility task yet: says so now.
	owner.must(409, "POST", base+"/ai-cohorts/"+cid+"/prompts/draft", map[string]any{})
	conn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "openrouter", "credentials": map[string]string{"api_key": "sk-or-good"}})["id"].(string)
	owner.must(200, "PUT", base+"/ai-settings", map[string]any{"integration_id": conn, "model": "openai/gpt-5"})
	owner.must(400, "POST", base+"/ai-cohorts/"+cid+"/prompts/draft", map[string]any{"count": 100})

	// The brand's own Search Console queries feed the draft.
	sc := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})["id"].(string)
	day := time.Now().UTC().AddDate(0, 0, -5)
	for q, imp := range map[string]int{"How do I stop getting blocked when scraping": 3000, "acme": 9000} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO search_daily (workspace_id, integration_id, site, host, day, page, query, clicks, impressions, ctr, position)
			VALUES ($1, $2, 'sc-domain:acme.example', 'acme.example', $3, 'https://acme.example/', $4, 1, $5, 0.01, 8)`, ws, sc, day, q, imp); err != nil {
			t.Fatal(err)
		}
	}

	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 10 * time.Millisecond
	e.srv.RegisterJobs(q)
	run, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = q.Run(run, 1); close(done) }()
	defer func() { cancel(); <-done }()

	if v := owner.must(202, "POST", base+"/ai-cohorts/"+cid+"/prompts/draft", map[string]any{"count": 10, "personas": []string{"Data engineer"}}); v["queued"] != true {
		t.Fatalf("draft: %v", v)
	}
	var list []any
	deadline := time.Now().Add(10 * time.Second)
	for {
		list = owner.must(200, "GET", base+"/ai-cohorts/"+cid+"/prompts", nil)["data"].([]any)
		if len(list) > 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The one the cohort had and the too-short one are skipped.
	if len(list) != 5 {
		t.Fatalf("prompts: %v", list)
	}
	byText := map[string]map[string]any{}
	for _, x := range list {
		byText[x.(map[string]any)["text"].(string)] = x.(map[string]any)
	}
	how := byText["how do I stop getting blocked when scraping prices"]
	if how["status"] != "suggested" || how["source"] != "drafted" || how["demand"] != 1000.0 || how["demand_source"] != "search_console" ||
		how["intent"] != "problem" || how["persona"] != "Data engineer" || how["why"] == "" {
		t.Fatalf("drafted: %v", how)
	}
	if alt := byText["best alternatives to Northpeak"]; alt["competitor"] != "Northpeak" || alt["intent"] != "comparison" {
		t.Fatalf("alternatives: %v", alt)
	}
	// A competitor or search query it wasn't given never sticks; how often
	// people ask AI assistants the question is its demand.
	if legit := byText["Is Acme legit?"]; legit["competitor"] != "" || legit["demand"] != 320.0 || legit["demand_source"] != "ai_search" {
		t.Fatalf("legit: %v", legit)
	}
	// A real question people asked ChatGPT keeps how often it is asked; a
	// volume of 0 sets no demand.
	if res := byText["best residential proxies for scraping"]; res["demand"] != 4400.0 || res["demand_source"] != "ai_search" {
		t.Fatalf("asked in ChatGPT: %v", res)
	}
	if alt := byText["best alternatives to Northpeak"]; alt["demand"] != nil {
		t.Fatalf("no AI volume: %v", alt)
	}
	// Reading AI demand is spent on the brand's AI answers channel.
	var spent int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM spend_entries WHERE workspace_id = $1 AND provider = 'dataforseo' AND channel = 'ai_answers' AND brand_id IS NOT NULL AND ref LIKE 'ai_demand:%'`, ws).Scan(&spent); err != nil || spent != 2 {
		t.Fatalf("demand spend: %d %v", spent, err)
	}
	// The drafter saw the brand, the competitors, the persona and the long query, not the one-word one.
	asked := bodies[len(bodies)-1]["messages"].([]any)
	prompt := asked[len(asked)-1].(map[string]any)["content"].(string)
	if !strings.Contains(prompt, "Northpeak") || !strings.Contains(prompt, "Data engineer") || !strings.Contains(prompt, "how do i stop getting blocked when scraping: 3000") ||
		strings.Contains(prompt, "- acme:") || !strings.Contains(prompt, "What is the best proxy provider?") ||
		!strings.Contains(prompt, "- Best residential proxies for scraping: 4400; Northpeak, Acme") {
		t.Fatalf("prompt: %s", prompt)
	}
	// Runs skip suggested questions until a person keeps one.
	cohort := owner.must(200, "GET", base+"/ai-cohorts/"+cid, nil)
	if cohort["prompts"] != 1.0 {
		t.Fatalf("cohort: %v", cohort)
	}
	kept := owner.must(200, "PATCH", base+"/ai-cohorts/"+cid+"/prompts/"+how["id"].(string), map[string]any{"status": "active"})
	if kept["status"] != "active" {
		t.Fatalf("kept: %v", kept)
	}
	// The benchmark counts the stored AI answers naming the brand and each
	// competitor, beside the count the day before.
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	if _, err := e.pool.Exec(ctx, `INSERT INTO ai_benchmarks (workspace_id, brand_id, cohort_id, day, entity, name, platform, mentions)
		SELECT workspace_id, brand_id, id, $2, 'brand', '', '', 100 FROM ai_cohorts WHERE id = $1`, cid, yesterday); err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.Enqueue(ctx, e.pool, jobs.NewJob{Kind: aivis.JobBenchmark, WorkspaceID: &ws, Payload: map[string]any{"cohort_id": cid}}); err != nil {
		t.Fatal(err)
	}
	var vis map[string]any
	for deadline := time.Now().Add(10 * time.Second); ; {
		vis = owner.must(200, "GET", base+"/ai-visibility?brand=acme", nil)["data"].([]any)[0].(map[string]any)
		if vis["benchmark_day"] != yesterday && vis["benchmark_day"] != nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	brand := vis["brand"].(map[string]any)
	if brand["benchmark_mentions"] != 120.0 || brand["benchmark_share"] != 0.3 || brand["benchmark_change"] != 20.0 {
		t.Fatalf("benchmark brand: %v", brand)
	}
	var rival map[string]any
	for _, c := range vis["competitors"].([]any) {
		if c.(map[string]any)["name"] == "Northpeak" {
			rival = c.(map[string]any)
		}
	}
	if rival == nil || rival["benchmark_mentions"] != 280.0 || rival["benchmark_change"] != nil {
		t.Fatalf("benchmark competitors: %v", vis["competitors"])
	}
	var platforms int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM ai_benchmarks WHERE cohort_id = $1 AND entity = 'brand' AND platform IN ('chatgpt', 'ai_overview')`, cid).Scan(&platforms); err != nil || platforms != 2 {
		t.Fatalf("benchmark platforms: %d %v", platforms, err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM spend_entries WHERE workspace_id = $1 AND channel = 'ai_answers' AND brand_id IS NOT NULL AND ref LIKE 'ai_benchmark:%'`, ws).Scan(&spent); err != nil || spent != 1 {
		t.Fatalf("benchmark spend: %d %v", spent, err)
	}
	seen := map[string]bool{}
	for _, ev := range events() {
		seen[ev.Name] = true
	}
	if !seen["ai_prompt.suggested"] || !seen["ai_prompt.drafted"] || !seen["ai_prompt.draft_requested"] || !seen["ai_benchmark.read"] {
		t.Fatalf("events: %v", seen)
	}
}
