package api_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/outflareai/outflare/engine/internal/aivis"
)

// AI visibility reads ChatGPT's own screen through the workspace's own
// DataForSEO, paid from that connection and kept in millionths of a dollar.
func TestAIVisibilityScreens(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "website": "acme.example"})
	cid := owner.must(201, "POST", base+"/ai-cohorts", map[string]any{"name": "Proxy buyers", "brand_id": "acme", "locale": "en-US",
		"competitors": []map[string]any{{"name": "Northpeak", "domains": []string{"northpeak.example"}}}})["id"].(string)
	owner.must(201, "POST", base+"/ai-cohorts/"+cid+"/prompts", map[string]any{"prompts": []map[string]any{{"text": "What is the best proxy provider?", "intent": "discovery"}}})

	// No answer reader yet: nothing to pick.
	if v := owner.must(200, "GET", base+"/ai-visibility/settings?brand=acme", nil); len(v["screens"].([]any)) != 0 {
		t.Fatalf("screens: %v", v)
	}
	dfs := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})["id"].(string)
	v := owner.must(200, "GET", base+"/ai-visibility/settings?brand=acme", nil)
	if sc := v["screens"].([]any); len(sc) != 2 || sc[0].(map[string]any)["model"] != "chatgpt" || sc[0].(map[string]any)["provider_name"] != "DataForSEO" {
		t.Fatalf("screens: %v", v)
	}
	owner.must(400, "PUT", base+"/ai-visibility/settings?brand=acme", map[string]any{"models": []map[string]any{{"integration_id": dfs, "surface": "screen", "model": "claude"}}})
	owner.must(400, "PUT", base+"/ai-visibility/settings?brand=acme", map[string]any{"models": []map[string]any{{"integration_id": dfs, "surface": "page", "model": "chatgpt"}}})
	// A DataForSEO connection isn't an AI model connection for the API.
	owner.must(400, "PUT", base+"/ai-visibility/settings?brand=acme", map[string]any{"models": []map[string]any{{"integration_id": dfs, "model": "chatgpt"}}})
	set := owner.must(200, "PUT", base+"/ai-visibility/settings?brand=acme", map[string]any{"models": []map[string]any{{"integration_id": dfs, "surface": "screen", "model": "chatgpt", "web_search": true}}})
	m0 := set["models"].([]any)[0].(map[string]any)
	if m0["provider"] != "dataforseo" || m0["surface"] != "screen" || m0["connected"] != true {
		t.Fatalf("settings: %v", set)
	}

	rid := owner.must(201, "POST", base+"/ai-cohorts/"+cid+"/runs", nil)["id"].(string)
	svc := &aivis.Service{Pool: e.pool, Models: e.srv.AIVisibilityModels(), Screens: e.srv.AIVisibilityScreens(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := svc.Step(ctx, ws, rid, 0); err != nil {
		t.Fatal(err)
	}
	run := owner.must(200, "GET", base+"/ai-runs/"+rid, nil)
	if run["status"] != "done" || run["samples_done"] != 1.0 || run["cost_micros"] != 4000.0 || run["cost_cents"] != 0.0 {
		t.Fatalf("run: %v", run)
	}
	s := owner.must(200, "GET", base+"/ai-samples?brand=acme&run_id="+rid, nil)["data"].([]any)[0].(map[string]any)
	if s["surface"] != "screen" || s["answer_model"] != "gpt-6" || s["brand_named"] != true || s["check_url"] != "https://chatgpt.com/?q=best+proxy" ||
		len(s["searches"].([]any)) != 1 || len(s["brands_named"].([]any)) != 3 {
		t.Fatalf("sample: %v", s)
	}
	ms := s["mentions"].([]any)
	if len(ms) != 2 || ms[0].(map[string]any)["name"] != "Northpeak" {
		t.Fatalf("mentions: %v", ms)
	}
	cs := s["citations"].([]any)
	if len(cs) != 2 || cs[0].(map[string]any)["url"] != "https://review.example/proxies" || cs[1].(map[string]any)["owner"] != "brand" {
		t.Fatalf("citations: %v", cs)
	}
	// Read but not cited: Northpeak's blog, a page close to being a source.
	pr := s["pages_read"].([]any)
	if len(pr) != 2 || pr[0].(map[string]any)["cited"] != true || pr[1].(map[string]any)["cited"] != nil || pr[1].(map[string]any)["competitor"] != "Northpeak" {
		t.Fatalf("pages read: %v", pr)
	}
	// Paid on the workspace's own DataForSEO, as AI answers for the brand.
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM spend_entries WHERE workspace_id = $1 AND provider = 'dataforseo' AND channel = 'ai_answers' AND brand_id IS NOT NULL`, ws).Scan(&n); err != nil || n != 1 {
		t.Fatalf("spend: %d %v", n, err)
	}
	if v := owner.must(200, "GET", base+"/ai-visibility/settings?brand=acme", nil); v["spent_this_month_cents"] != 1.0 {
		t.Fatalf("spent: %v", v)
	}

	// With the connection removed, the sample fails and says what to add.
	owner.must(204, "DELETE", base+"/integrations/"+dfs, nil)
	rid = owner.must(201, "POST", base+"/ai-cohorts/"+cid+"/runs", nil)["id"].(string)
	if err := svc.Step(ctx, ws, rid, 0); err != nil {
		t.Fatal(err)
	}
	s = owner.must(200, "GET", base+"/ai-samples?brand=acme&run_id="+rid, nil)["data"].([]any)[0].(map[string]any)
	if s["status"] != "failed" || s["error_code"] != "connector_needed" {
		t.Fatalf("no connection: %v", s)
	}
}
