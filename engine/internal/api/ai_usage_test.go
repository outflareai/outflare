package api_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestAIUsage(t *testing.T) {
	e := freshEnv(t)
	events := track.Capture(t)
	var used []string
	oa := httptest.NewServer(fakeOpenAI{t, &sync.Mutex{}, &used})
	defer oa.Close()
	e.integrations.Client.BaseURLs["openai"] = oa.URL
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `UPDATE users SET email_verified_at = now()`); err != nil {
		t.Fatal(err)
	}

	// Before any call every tool is listed, with nothing spent.
	u := owner.must(200, "GET", base+"/ai-usage", nil)
	if u["month"] != time.Now().UTC().Format("2006-01") || u["total"].(map[string]any)["calls"].(float64) != 0 || len(u["tools"].([]any)) != 18 {
		t.Fatalf("empty usage = %v", u)
	}
	owner.must(400, "GET", base+"/ai-usage?month=2026-13", nil)

	cid := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "openai", "credentials": map[string]any{"api_key": openAIKey}})["id"].(string)
	owner.must(200, "PUT", base+"/ai-settings", map[string]any{"integration_id": cid, "model": "gpt-test",
		"input_cents_per_million": 250, "output_cents_per_million": 1000, "monthly_limit_cents": 5000})

	// A plan is a call by the planning tool: 2000 tokens in at 250, 500
	// out at 1000, 1 cent.
	owner.must(201, "POST", base+"/missions/plan", map[string]any{"goal": "Book 5 meetings with fintech CFOs"})
	u = owner.must(200, "GET", base+"/ai-usage", nil)
	tot := u["total"].(map[string]any)
	if tot["calls"].(float64) != 1 || tot["input_tokens"].(float64) != 2000 || tot["output_tokens"].(float64) != 500 || tot["cost_cents"].(float64) != 1 || u["spent_cents"].(float64) != 1 {
		t.Fatalf("usage after a plan = %v", u)
	}
	first := u["tools"].([]any)[0].(map[string]any)
	if first["task"] != "plan" || first["name"] != "Planning" || first["calls"].(float64) != 1 {
		t.Errorf("tools = %v", u["tools"])
	}
	if m := u["models"].([]any); len(m) != 1 || m[0].(map[string]any)["provider"] != "openai" || m[0].(map[string]any)["model"] != "gpt-test" {
		t.Errorf("models = %v", m)
	}
	if r := u["recent"].([]any); len(r) != 1 || r[0].(map[string]any)["tool_name"] != "Planning" || r[0].(map[string]any)["cost_reported"] != false {
		t.Errorf("recent = %v", r)
	}
	if d := u["days"].([]any); len(d) != 1 || d[0].(map[string]any)["cost_cents"].(float64) != 1 {
		t.Errorf("days = %v", d)
	}
	// Another workspace sees none of it.
	ws2 := owner.must(201, "POST", "/v1/workspaces", map[string]string{"name": "Other"})["id"].(string)
	if u2 := owner.must(200, "GET", "/v1/workspaces/"+ws2+"/ai-usage", nil); u2["total"].(map[string]any)["calls"].(float64) != 0 {
		t.Errorf("other workspace usage = %v", u2["total"])
	}

	// A limit per tool. Planning's own limit stops it even under the
	// workspace's limit; other tools carry on.
	owner.must(400, "PUT", base+"/ai-usage/limits", map[string]any{"limits": map[string]any{"Bad Tool": 100}})
	owner.must(400, "PUT", base+"/ai-usage/limits", map[string]any{"limits": map[string]any{"plan": -1}})
	u = owner.must(200, "PUT", base+"/ai-usage/limits", map[string]any{"limits": map[string]any{"plan": 5, "lead_finder": 300, "check_in": nil}})
	limits := map[string]any{}
	for _, x := range u["tools"].([]any) {
		limits[x.(map[string]any)["task"].(string)] = x.(map[string]any)["monthly_limit_cents"]
	}
	if limits["plan"] != float64(5) || limits["lead_finder"] != float64(300) || limits["check_in"] != nil {
		t.Errorf("limits = %v", limits)
	}
	code, out := owner.do("POST", base+"/missions/plan", map[string]any{"goal": "Another goal"})
	if code != 409 || out["error"].(map[string]any)["code"] != "ai_budget_reached" || !strings.Contains(out["error"].(map[string]any)["message"].(string), "Planning") {
		t.Errorf("plan over its tool limit = %d %v", code, out)
	}
	// The workspace settings keep the limits.
	if s := owner.must(200, "GET", base+"/ai-settings", nil); s["monthly_limit_cents"].(float64) != 5000 {
		t.Errorf("settings after limits = %v", s["monthly_limit_cents"])
	}

	// A call that takes the month to 80% of a limit warns once; reaching
	// it says so once, and owners are emailed each time. At these prices a
	// plan costs 100 cents.
	e.runCRMImports(t) // works the server's jobs, the warning emails among them
	before := e.outbox.count()
	owner.must(200, "PUT", base+"/ai-settings", map[string]any{"integration_id": cid, "model": "gpt-test",
		"input_cents_per_million": 25000, "output_cents_per_million": 100000, "monthly_limit_cents": 5000})
	owner.must(200, "PUT", base+"/ai-usage/limits", map[string]any{"limits": map[string]any{"plan": 125}})
	owner.must(201, "POST", base+"/missions/plan", map[string]any{"goal": "Goal two"}) // planning: 101 of 125, past 80%
	owner.must(200, "PUT", base+"/ai-usage/limits", map[string]any{"limits": map[string]any{}})
	owner.must(200, "PUT", base+"/ai-settings", map[string]any{"integration_id": cid, "model": "gpt-test",
		"input_cents_per_million": 25000, "output_cents_per_million": 100000, "monthly_limit_cents": 200})
	owner.must(201, "POST", base+"/missions/plan", map[string]any{"goal": "Goal three"}) // workspace: 201 of 200
	code, out = owner.do("POST", base+"/missions/plan", map[string]any{"goal": "One too many"})
	if code != 409 || out["error"].(map[string]any)["code"] != "ai_budget_reached" {
		t.Errorf("plan over the workspace limit = %d %v", code, out)
	}
	var warned, reached int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE action = 'ai_budget.warned'), count(*) FILTER (WHERE action = 'ai_budget.reached')
		FROM audit_events WHERE workspace_id = $1`, ws).Scan(&warned, &reached); err != nil {
		t.Fatal(err)
	}
	// Planning reached 80% of 12; the workspace jumped past 80% to 100%
	// of 11, which also counts as its warning, without a second event.
	if warned != 1 || reached != 1 {
		t.Errorf("warned %d, reached %d", warned, reached)
	}
	var alerts int
	e.pool.QueryRow(ctx, `SELECT count(*) FROM ai_budget_alerts WHERE workspace_id = $1`, ws).Scan(&alerts)
	if alerts != 3 {
		t.Errorf("alerts = %d", alerts)
	}
	deadline := time.Now().Add(5 * time.Second)
	for e.outbox.count() < before+2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	e.outbox.mu.Lock()
	sent := e.outbox.sent[before:]
	e.outbox.mu.Unlock()
	if len(sent) != 2 {
		t.Fatalf("warning emails = %d", len(sent))
	}
	var sawReached bool
	for _, m := range sent {
		if m.To != "jack@example.com" || !strings.Contains(m.Text, "/acme-outbound/ai-usage") || !strings.Contains(m.Text, "Outflare doesn't pay for them") {
			t.Errorf("email = %+v", m)
		}
		sawReached = sawReached || strings.Contains(m.Subject, "limit reached")
	}
	if !sawReached {
		t.Error("no email said the limit was reached")
	}

	// Calls past the retention window are purged.
	if _, err := e.pool.Exec(ctx, `UPDATE ai_calls SET occurred_at = now() - interval '14 months' WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	if n := e.srv.PurgeAICalls(ctx); n != 3 {
		t.Errorf("purged %d", n)
	}

	// A limit can warn only: calls go on past it. The workspace's limit
	// (201 of 200 spent) stops plans until it is set to warn only.
	if u["hard_stop"] != true {
		t.Errorf("hard_stop by default = %v", u["hard_stop"])
	}
	owner.must(400, "PUT", base+"/ai-usage/limits", map[string]any{"limits": map[string]any{}, "hard_stops": map[string]any{"Bad Tool": false}})
	u = owner.must(200, "PUT", base+"/ai-usage/limits", map[string]any{"limits": map[string]any{"plan": 5}, "hard_stop": false, "hard_stops": map[string]any{"plan": false}})
	stops := map[string]any{}
	for _, x := range u["tools"].([]any) {
		stops[x.(map[string]any)["task"].(string)] = x.(map[string]any)["hard_stop"]
	}
	if u["hard_stop"] != false || stops["plan"] != false || stops["check_in"] != true {
		t.Errorf("after warn only: hard_stop %v, tools %v", u["hard_stop"], stops)
	}
	owner.must(201, "POST", base+"/missions/plan", map[string]any{"goal": "Past both limits"})
	// Leaving the switches out keeps them; setting them back stops calls.
	if u = owner.must(200, "PUT", base+"/ai-usage/limits", map[string]any{"limits": map[string]any{"plan": 5}}); u["hard_stop"] != false {
		t.Errorf("hard_stop after a PUT without it = %v", u["hard_stop"])
	}
	owner.must(200, "PUT", base+"/ai-usage/limits", map[string]any{"limits": map[string]any{"plan": 5}, "hard_stop": true, "hard_stops": map[string]any{}})
	if code, _ := owner.do("POST", base+"/missions/plan", map[string]any{"goal": "Stopped again"}); code != 409 {
		t.Errorf("plan after hard stop is back = %d", code)
	}
	var ws2Limits int
	e.pool.QueryRow(ctx, `SELECT count(*) FROM workspace_ai_settings WHERE workspace_id = $1 AND (limit_warn_only OR warn_only_tasks <> '{}')`, ws2).Scan(&ws2Limits)
	if ws2Limits != 0 {
		t.Errorf("another workspace's limits changed")
	}

	names := map[string]bool{}
	for _, ev := range events() {
		names[ev.Name] = true
	}
	for _, want := range []string{"ai_limits.updated", "ai_budget.warned", "ai_budget.reached", "ai_budget.emailed", "ai_calls.purged"} {
		if !names[want] {
			t.Errorf("no %s event", want)
		}
	}
}

// An extension's AI tool is listed with its own name and description, and
// takes a limit of its own.
func TestAIUsageExtensionTool(t *testing.T) {
	e := freshEnvWith(t, func(o *api.Options) {
		o.AITools = []api.AITool{{ID: "lead_finder", Name: "Lead finder", Description: "Finding and checking companies for lead lists.", ModelTask: "classify_domain"}}
	})
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	u := owner.must(200, "PUT", base+"/ai-usage/limits", map[string]any{"limits": map[string]any{"lead_finder": 300}})
	tools := u["tools"].([]any)
	if len(tools) != 19 {
		t.Fatalf("tools = %v", tools)
	}
	var found bool
	for _, x := range tools {
		if x := x.(map[string]any); x["task"] == "lead_finder" {
			found = x["name"] == "Lead finder" && x["description"] == "Finding and checking companies for lead lists." && x["monthly_limit_cents"] == float64(300)
		}
	}
	if !found {
		t.Errorf("tools = %v", tools)
	}
}
