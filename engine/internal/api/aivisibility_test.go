package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/aivis"
	"github.com/outflareai/outflare/engine/internal/track"
)

// fakeOpenRouter answers like OpenRouter: the key check, the model list,
// and chat completions with web citations and the call's cost.
type fakeOpenRouter struct {
	bodies *[]map[string]any
	// judge is what a fact check answers (JSON), when set.
	judge *string
}

func (f fakeOpenRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer sk-or-good" {
		w.WriteHeader(401)
		io.WriteString(w, `{"error":{"message":"No auth credentials found","code":401}}`)
		return
	}
	switch r.URL.Path {
	case "/key":
		io.WriteString(w, `{"data":{"label":"sk-or-...good","usage":0,"limit":null}}`)
		return
	case "/models":
		io.WriteString(w, `{"data":[{"id":"openai/gpt-5","name":"OpenAI: GPT-5"},{"id":"perplexity/sonar","name":"Perplexity: Sonar"}]}`)
		return
	}
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	*f.bodies = append(*f.bodies, body)
	msgs := body["messages"].([]any)
	question := msgs[len(msgs)-1].(map[string]any)["content"].(string)
	answer, cites := "I can't say.", []map[string]any{}
	switch {
	case body["response_format"] != nil && f.judge != nil:
		answer = *f.judge
	case strings.Contains(question, "best proxy"):
		answer = "Top picks: **Northpeak** is the best known. Acme (acme.example) is cheaper, and ACME has a free trial. See https://review.example/proxies?utm_source=openai."
		cites = []map[string]any{{"type": "url_citation", "url_citation": map[string]any{"url": "https://www.acme.example/pricing", "title": "Acme pricing"}}}
	case strings.Contains(question, "cheapest"):
		answer = "Northpeak's starter plan is the cheapest. Acmeville sells none."
		cites = []map[string]any{{"type": "url_citation", "url_citation": map[string]any{"url": "https://northpeak.example/plans#starter", "title": "Plans"}}}
	}
	json.NewEncoder(w).Encode(map[string]any{
		"model": body["model"],
		"choices": []map[string]any{{"finish_reason": "stop",
			"message": map[string]any{"role": "assistant", "content": answer, "annotations": cites}}},
		"usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 60, "cost": 0.0123},
	})
}

func TestAIVisibility(t *testing.T) {
	e := freshEnv(t)
	events := track.Capture(t)
	var bodies []map[string]any
	or := httptest.NewServer(fakeOpenRouter{bodies: &bodies})
	t.Cleanup(or.Close)
	e.integrations.Client.BaseURLs["openrouter"] = or.URL
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "website": "acme.example"})
	// Runs need models first.
	cohort := owner.must(201, "POST", base+"/ai-cohorts", map[string]any{"name": "Proxy buyers", "brand_id": "acme", "locale": "en-US",
		"competitors": []map[string]any{{"name": "Northpeak", "domains": []string{"https://northpeak.example/"}}}})
	cid := cohort["id"].(string)
	if cohort["brand_id"] == nil || cohort["competitors"].([]any)[0].(map[string]any)["domains"].([]any)[0] != "northpeak.example" {
		t.Fatalf("cohort: %v", cohort)
	}
	owner.must(400, "POST", base+"/ai-cohorts", map[string]any{"name": "Bad", "locale": "english"})
	owner.must(400, "POST", base+"/ai-cohorts", map[string]any{"name": "Proxy buyers"})
	added := owner.must(201, "POST", base+"/ai-cohorts/"+cid+"/prompts", map[string]any{"prompts": []map[string]any{
		{"text": "What is the best proxy provider?", "intent": "discovery"},
		{"text": "Which proxy is the cheapest?", "intent": "comparison"},
		{"text": "what is the BEST proxy provider?"},
	}})
	if len(added["data"].([]any)) != 2 || added["skipped"] != 1.0 {
		t.Fatalf("prompts: %v", added)
	}
	owner.must(409, "POST", base+"/ai-cohorts/"+cid+"/runs", nil)

	// Settings: only the workspace's own AI connections, and only models
	// that can search when asked to.
	owner.must(400, "PUT", base+"/ai-visibility/settings", map[string]any{"models": []map[string]any{{"integration_id": "00000000-0000-0000-0000-000000000000", "model": "openai/gpt-5"}}})
	owner.must(422, "POST", base+"/integrations", map[string]any{"connector": "openrouter", "credentials": map[string]string{"api_key": "sk-ant-wrong-vendor"}})
	conn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "openrouter", "credentials": map[string]string{"api_key": "sk-or-good"}})["id"].(string)
	models := owner.must(200, "GET", base+"/integrations/"+conn+"/models", nil)
	if len(models["data"].([]any)) != 2 {
		t.Fatalf("models: %v", models)
	}
	set := owner.must(200, "PUT", base+"/ai-visibility/settings", map[string]any{"samples_per_prompt": 1, "monthly_limit_cents": 500,
		"models": []map[string]any{{"integration_id": conn, "model": "openai/gpt-5", "web_search": true}}})
	m0 := set["models"].([]any)[0].(map[string]any)
	if m0["provider"] != "openrouter" || m0["connected"] != true || set["monthly_limit_cents"] != 500.0 {
		t.Fatalf("settings: %v", set)
	}
	owner.must(400, "PUT", base+"/ai-visibility/settings", map[string]any{"models": []map[string]any{
		{"integration_id": conn, "model": "openai/gpt-5"}, {"integration_id": conn, "model": "openai/gpt-5"}}})

	run := owner.must(201, "POST", base+"/ai-cohorts/"+cid+"/runs", nil)
	rid := run["id"].(string)
	if run["samples_planned"] != 2.0 || run["status"] != "queued" {
		t.Fatalf("run: %v", run)
	}
	owner.must(409, "POST", base+"/ai-cohorts/"+cid+"/runs", nil)

	svc := &aivis.Service{Pool: e.pool, Models: e.srv.AIVisibilityModels(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := svc.Step(ctx, ws, rid, 0); err != nil {
		t.Fatal(err)
	}
	run = owner.must(200, "GET", base+"/ai-runs/"+rid, nil)
	if run["status"] != "done" || run["samples_done"] != 2.0 || run["cost_cents"] != 4.0 {
		t.Fatalf("finished run: %v", run)
	}
	// Each call asked for web search and the cost, in the cohort's locale.
	if len(bodies) != 2 || bodies[0]["plugins"] == nil || bodies[0]["usage"] == nil ||
		!strings.Contains(bodies[0]["messages"].([]any)[1].(map[string]any)["content"].(string), "en-US") {
		t.Fatalf("requests: %v", bodies)
	}

	samples := owner.must(200, "GET", base+"/ai-samples?run_id="+rid, nil)["data"].([]any)
	if len(samples) != 2 {
		t.Fatalf("samples: %v", samples)
	}
	var best, cheap map[string]any
	for _, s := range samples {
		s := s.(map[string]any)
		if strings.Contains(s["prompt"].(string), "best") {
			best = s
		} else {
			cheap = s
		}
	}
	ms := best["mentions"].([]any)
	if len(ms) != 2 || ms[0].(map[string]any)["name"] != "Northpeak" || ms[1].(map[string]any)["entity"] != "brand" ||
		ms[1].(map[string]any)["count"] != 3.0 || best["brand_named"] != true {
		t.Fatalf("mentions: %v", ms)
	}
	cs := best["citations"].([]any)
	if len(cs) != 2 || cs[0].(map[string]any)["owner"] != "brand" || cs[1].(map[string]any)["url"] != "https://review.example/proxies" {
		t.Fatalf("citations: %v", cs)
	}
	// "Acmeville" is not Acme.
	if cheap["brand_named"] != false || cheap["citations"].([]any)[0].(map[string]any)["competitor"] != "Northpeak" {
		t.Fatalf("cheap: %v", cheap)
	}
	if n := len(owner.must(200, "GET", base+"/ai-samples?brand_named=false", nil)["data"].([]any)); n != 1 {
		t.Fatalf("brand_named=false: %d", n)
	}

	// Share of voice: Acme in 1 of 2 answers, Northpeak in 2.
	ov := owner.must(200, "GET", base+"/ai-visibility?brand=acme", nil)["data"].([]any)
	c := ov[0].(map[string]any)
	b := c["brand"].(map[string]any)
	if c["answers"] != 2.0 || b["name"] != "Acme" || b["mention_rate"] != 0.5 || b["share_of_voice"] != 0.333 || b["citation_rate"] != 0.5 {
		t.Fatalf("overview: %v", c)
	}
	comp := c["competitors"].([]any)[0].(map[string]any)
	if comp["mention_rate"] != 1.0 || comp["share_of_voice"] != 0.667 || c["open_fixes"] != 1.0 || len(c["weeks"].([]any)) != 1 {
		t.Fatalf("competitors: %v", c)
	}

	// The prompt whose answer left Acme out is a fix.
	fixes := owner.must(200, "GET", base+"/fixes?source=ai_visibility", nil)["data"].([]any)
	if len(fixes) != 1 {
		t.Fatalf("fixes: %v", fixes)
	}
	fix := fixes[0].(map[string]any)
	if fix["rule"].(map[string]any)["id"] != "ai_brand_absent" || fix["subject"].(map[string]any)["id"] != cheap["prompt_id"] || fix["brand_id"] == nil {
		t.Fatalf("fix: %v", fix)
	}
	// As a prompt it is a content task, checked by the cohort's next run.
	fp := owner.must(200, "GET", base+"/fixes/"+fix["id"].(string)+"/prompt", nil)
	if !strings.Contains(fp["prompt"].(string), "Northpeak") || !strings.HasSuffix(fp["verify_url"].(string), "/ai-cohorts/"+cid+"/runs") {
		t.Fatalf("fix prompt: %v", fp)
	}
	owner.must(409, "POST", base+"/fixes/"+fix["id"].(string)+"/verify", nil)

	// The spend is on the workspace's own OpenRouter bill.
	var spend int64
	if err := e.pool.QueryRow(ctx, `SELECT COALESCE(sum(amount_cents), 0) FROM spend_entries WHERE workspace_id = $1 AND provider = 'openrouter' AND channel = 'ai_answers' AND brand_id IS NOT NULL`, ws).Scan(&spend); err != nil || spend != 4 {
		t.Fatalf("spend: %d %v", spend, err)
	}
	if v := owner.must(200, "GET", base+"/ai-visibility/settings", nil); v["spent_this_month_cents"] != 4.0 {
		t.Fatalf("spent: %v", v)
	}

	// At the sampling limit, a run stops before calling the model.
	owner.must(200, "PUT", base+"/ai-visibility/settings", map[string]any{"monthly_limit_cents": 4})
	rid = owner.must(201, "POST", base+"/ai-cohorts/"+cid+"/runs", nil)["id"].(string)
	if err := svc.Step(ctx, ws, rid, 0); err != nil {
		t.Fatal(err)
	}
	run = owner.must(200, "GET", base+"/ai-runs/"+rid, nil)
	if run["status"] != "stopped" || run["reason"] != "sampling_limit_reached" || len(bodies) != 2 {
		t.Fatalf("limited run: %v", run)
	}

	// Stopping, pausing and removing.
	owner.must(200, "PUT", base+"/ai-visibility/settings", map[string]any{"monthly_limit_cents": 500})
	rid = owner.must(201, "POST", base+"/ai-cohorts/"+cid+"/runs", nil)["id"].(string)
	if owner.must(200, "POST", base+"/ai-runs/"+rid+"/stop", nil)["status"] != "stopped" {
		t.Fatal("stop")
	}
	prompt := cheap["prompt_id"].(string)
	owner.must(200, "PATCH", base+"/ai-cohorts/"+cid+"/prompts/"+prompt, map[string]any{"status": "paused"})
	owner.must(204, "DELETE", base+"/ai-cohorts/"+cid+"/prompts/"+prompt, nil)
	if n := len(owner.must(200, "GET", base+"/fixes?source=ai_visibility", nil)["data"].([]any)); n != 0 {
		t.Fatalf("fix kept after its prompt: %d", n)
	}
	if n := len(owner.must(200, "GET", base+"/ai-runs?cohort_id="+cid, nil)["data"].([]any)); n != 3 {
		t.Fatalf("runs: %d", n)
	}
	owner.must(200, "PATCH", base+"/ai-cohorts/"+cid, map[string]any{"schedule": "weekly", "aliases": []string{"Acme Proxies"}})
	owner.must(204, "DELETE", base+"/ai-cohorts/"+cid, nil)
	owner.must(404, "GET", base+"/ai-cohorts/"+cid, nil)

	seen := map[string]bool{}
	for _, ev := range events() {
		seen[ev.Name] = true
	}
	for _, name := range []string{"ai_cohort.created", "ai_prompt.added", "ai_visibility.settings_updated", "ai_run.started", "ai_run.finished",
		"ai_run.stopped", "ai_prompt.updated", "ai_prompt.deleted", "ai_cohort.updated", "ai_cohort.deleted"} {
		if !seen[name] {
			t.Errorf("no %s event", name)
		}
	}
}
