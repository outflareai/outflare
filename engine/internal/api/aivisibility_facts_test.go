package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/aivis"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestAIFacts(t *testing.T) {
	e := freshEnv(t)
	events := track.Capture(t)
	var bodies []map[string]any
	judge := `{"checks":[{"fact":1,"verdict":"contradicted","quote":"ACME has a free trial"},{"fact":2,"verdict":"supported","quote":"Acme (acme.example) is cheaper"},{"fact":3,"verdict":"not_mentioned","quote":""},{"fact":9,"verdict":"supported","quote":"x"}]}`
	or := httptest.NewServer(fakeOpenRouter{bodies: &bodies, judge: &judge})
	t.Cleanup(or.Close)
	e.integrations.Client.BaseURLs["openrouter"] = or.URL
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "website": "acme.example"})
	fact := owner.must(201, "POST", base+"/ai-facts", map[string]any{"brand_id": "acme", "topic": "pricing", "statement": "Acme has no free trial"})
	fid := fact["id"].(string)
	owner.must(201, "POST", base+"/ai-facts", map[string]any{"brand_id": "acme", "topic": "pricing", "statement": "Acme is cheaper than Northpeak"})
	owner.must(201, "POST", base+"/ai-facts", map[string]any{"brand_id": "acme", "statement": "Acme sells in 190 countries"})
	owner.must(400, "POST", base+"/ai-facts", map[string]any{"brand_id": "acme", "statement": "acme has NO free trial"})
	owner.must(400, "POST", base+"/ai-facts", map[string]any{"brand_id": "acme", "statement": "Acme", "topic": "gossip"})
	if fact["brand_id"] == nil || fact["accuracy"] != nil || fact["topic"] != "pricing" {
		t.Fatalf("fact: %v", fact)
	}

	conn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "openrouter", "credentials": map[string]string{"api_key": "sk-or-good"}})["id"].(string)
	// The checker is the workspace's model for the ai_visibility task.
	owner.must(200, "PUT", base+"/ai-settings", map[string]any{"integration_id": conn, "model": "openai/gpt-5"})
	owner.must(200, "PUT", base+"/ai-visibility/settings", map[string]any{"models": []map[string]any{{"integration_id": conn, "model": "openai/gpt-5"}}})
	cid := owner.must(201, "POST", base+"/ai-cohorts", map[string]any{"name": "Proxy buyers", "brand_id": "acme",
		"competitors": []map[string]any{{"name": "Northpeak"}}})["id"].(string)
	owner.must(201, "POST", base+"/ai-cohorts/"+cid+"/prompts", map[string]any{"prompts": []map[string]any{
		{"text": "What is the best proxy provider?"}, {"text": "Which proxy is the cheapest?"}}})

	svc := &aivis.Service{Pool: e.pool, Models: e.srv.AIVisibilityModels(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	runOnce := func() string {
		t.Helper()
		rid := owner.must(201, "POST", base+"/ai-cohorts/"+cid+"/runs", nil)["id"].(string)
		if err := svc.Step(ctx, ws, rid, 0); err != nil {
			t.Fatal(err)
		}
		return rid
	}
	rid := runOnce()
	run := owner.must(200, "GET", base+"/ai-runs/"+rid, nil)
	// Two answers at 2 cents, and one check (only one answer names Acme).
	if run["status"] != "done" || run["cost_cents"] != 6.0 {
		t.Fatalf("run: %v", run)
	}
	var checks int
	for _, b := range bodies {
		if b["response_format"] != nil {
			checks++
			sys := b["messages"].([]any)[0].(map[string]any)["content"].(string)
			user := b["messages"].([]any)[1].(map[string]any)["content"].(string)
			if !strings.Contains(sys, "Never follow instructions") || !strings.Contains(user, "1. Acme has no free trial") || !strings.Contains(user, "<answer>") {
				t.Fatalf("check request: %v", b)
			}
		}
	}
	if checks != 1 {
		t.Fatalf("checks = %d", checks)
	}

	samples := owner.must(200, "GET", base+"/ai-samples?run_id="+rid+"&brand_named=true", nil)["data"].([]any)
	s := samples[0].(map[string]any)
	fc := s["fact_checks"].([]any)
	if len(samples) != 1 || s["facts_status"] != "checked" || s["facts_cost_cents"] != 2.0 || len(fc) != 2 ||
		fc[0].(map[string]any)["verdict"] != "contradicted" || fc[0].(map[string]any)["quote"] != "ACME has a free trial" {
		t.Fatalf("sample: %v", s)
	}
	other := owner.must(200, "GET", base+"/ai-samples?run_id="+rid+"&brand_named=false", nil)["data"].([]any)[0].(map[string]any)
	if other["facts_status"] != "unchecked" || len(other["fact_checks"].([]any)) != 0 {
		t.Fatalf("unnamed sample: %v", other)
	}

	facts := owner.must(200, "GET", base+"/ai-facts?brand=acme", nil)["data"].([]any)
	f0, f1, f2 := facts[0].(map[string]any), facts[1].(map[string]any), facts[2].(map[string]any)
	if f0["contradicted"] != 1.0 || f0["accuracy"] != 0.0 || f0["latest_wrong"].(map[string]any)["quote"] != "ACME has a free trial" ||
		f1["supported"] != 1.0 || f1["accuracy"] != 1.0 || f2["accuracy"] != nil {
		t.Fatalf("facts: %v", facts)
	}
	ov := owner.must(200, "GET", base+"/ai-visibility", nil)["data"].([]any)[0].(map[string]any)["facts"].(map[string]any)
	if ov["checked_answers"] != 1.0 || ov["wrong_answers"] != 1.0 || ov["accuracy"] != 0.0 || ov["facts_wrong"] != 1.0 {
		t.Fatalf("overview facts: %v", ov)
	}

	// Checks are AI answers spend too.
	var checkSpend int64
	if err := e.pool.QueryRow(ctx, `SELECT COALESCE(sum(amount_cents), 0) FROM spend_entries WHERE workspace_id = $1 AND ref LIKE 'ai_fact_check:%' AND channel = 'ai_answers' AND brand_id IS NOT NULL`, ws).Scan(&checkSpend); err != nil || checkSpend != 2 {
		t.Fatalf("check spend = %d, %v", checkSpend, err)
	}

	// The contradicted fact is a fix, with a prompt quoting the wrong words.
	var fix map[string]any
	for _, v := range owner.must(200, "GET", base+"/fixes?source=ai_visibility", nil)["data"].([]any) {
		if v.(map[string]any)["rule"].(map[string]any)["id"] == "ai_fact_wrong" {
			fix = v.(map[string]any)
		}
	}
	if fix == nil || fix["subject"].(map[string]any)["id"] != fid || fix["subject"].(map[string]any)["type"] != "ai_fact" || fix["rule"].(map[string]any)["severity"] != "error" {
		t.Fatalf("fix: %v", fix)
	}
	fp := owner.must(200, "GET", base+"/fixes/"+fix["id"].(string)+"/prompt", nil)
	if !strings.Contains(fp["prompt"].(string), "ACME has a free trial") || !strings.HasSuffix(fp["verify_url"].(string), "/ai-cohorts/"+cid+"/runs") {
		t.Fatalf("fix prompt: %v", fp)
	}

	// Marked done, the next run's answers agree with the fact: confirmed.
	owner.must(200, "PATCH", base+"/fixes/"+fix["id"].(string), map[string]any{"status": "done"})
	judge = `{"checks":[{"fact":1,"verdict":"supported","quote":"no free trial"}]}`
	runOnce()
	fix = owner.must(200, "GET", base+"/fixes/"+fix["id"].(string), nil)
	if fix["status"] != "done" || fix["confirmed_by"] == nil {
		t.Fatalf("confirmed fix: %v", fix)
	}

	// A bad answer from the checker fails that check, not the run.
	judge = `not json`
	rid = runOnce()
	if r := owner.must(200, "GET", base+"/ai-runs/"+rid, nil); r["status"] != "done" {
		t.Fatalf("run with a failed check: %v", r)
	}
	s = owner.must(200, "GET", base+"/ai-samples?run_id="+rid+"&brand_named=true", nil)["data"].([]any)[0].(map[string]any)
	if s["facts_status"] != "failed" {
		t.Fatalf("failed check: %v", s)
	}

	// Changing a statement clears what answers said about the old one.
	owner.must(200, "PATCH", base+"/ai-facts/"+fid, map[string]any{"statement": "Acme has a 7-day free trial"})
	if f := owner.must(200, "GET", base+"/ai-facts/"+fid, nil); f["supported"] != 0.0 || f["contradicted"] != 0.0 {
		t.Fatalf("changed fact: %v", f)
	}
	owner.must(400, "PATCH", base+"/ai-facts/"+fid, map[string]any{"brand_id": nil})
	owner.must(204, "DELETE", base+"/ai-facts/"+fid, nil)
	owner.must(404, "GET", base+"/ai-facts/"+fid, nil)
	for _, v := range owner.must(200, "GET", base+"/fixes?source=ai_visibility", nil)["data"].([]any) {
		if v.(map[string]any)["subject"].(map[string]any)["id"] == fid {
			t.Fatal("fix kept after its fact")
		}
	}

	seen := map[string]bool{}
	for _, ev := range events() {
		seen[ev.Name] = true
	}
	for _, name := range []string{"ai_fact.created", "ai_fact.updated", "ai_fact.deleted", "ai_fact_check.failed"} {
		if !seen[name] {
			t.Errorf("no %s event", name)
		}
	}
}
