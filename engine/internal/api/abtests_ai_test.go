package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// abAIPrompts keeps what the A/B test AI was sent, by kind.
var abAIPrompts struct {
	sync.Mutex
	last map[string]string
}

// answerABTestAI answers fakeClaude's A/B test calls.
func answerABTestAI(w http.ResponseWriter, body string) {
	var answer any
	kind := "summary"
	switch {
	case strings.Contains(body, "proposing A/B tests"):
		kind = "suggest"
		answer = map[string]any{"suggestions": []any{
			map[string]any{"title": "Shorter pricing headline", "hypothesis": "Because most visitors land on /pricing, a shorter headline will lift signups.",
				"page": "/pricing", "change": "Cut the headline to five words.", "goal": map[string]any{"kind": "event", "name": "signup_started", "path": ""},
				"versions": []any{map[string]any{"key": "control", "description": "Today's page"}, map[string]any{"key": "short", "description": "Five words"}}},
			// A goal that isn't an event name is dropped.
			map[string]any{"title": "Bad goal", "hypothesis": "x", "page": "/", "change": "x", "goal": map[string]any{"kind": "event", "name": "Not An Event", "path": ""},
				"versions": []any{}},
		}}
	case strings.Contains(body, "You write versions of website copy"):
		kind = "versions"
		answer = map[string]any{"versions": []any{
			map[string]any{"key": "urgent", "content": map[string]any{"headline": "Ship today, not next quarter"}, "rationale": "Urgency for agencies."},
			// A key the flag already has is renamed.
			map[string]any{"key": "control", "content": map[string]any{"headline": "Built for agencies"}, "rationale": "Names the audience."},
		}}
	default:
		answer = map[string]any{"headline": "The faster headline won", "summary": "It lifted signups a lot.", "next_steps": []string{"Ship it", "Archive the flag"}}
	}
	abAIPrompts.Lock()
	if abAIPrompts.last == nil {
		abAIPrompts.last = map[string]string{}
	}
	abAIPrompts.last[kind] = body
	abAIPrompts.Unlock()
	text, _ := json.Marshal(answer)
	json.NewEncoder(w).Encode(map[string]any{
		"id": "msg_ab", "type": "message", "role": "assistant", "model": "claude-sonnet-5-5",
		"content":     []any{map[string]any{"type": "text", "text": string(text)}},
		"stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 1500, "output_tokens": 300},
	})
}

func TestABTestAI(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	e.runServerJobs(t)

	owner.must(201, "POST", base+"/flags", map[string]any{"key": "hero", "name": "Pricing headline",
		"variants": []map[string]any{{"key": "control", "weight": 50, "payload": map[string]any{"headline": "Outflare for teams"}}, {"key": "faster", "weight": 50, "payload": map[string]any{"headline": "Ship faster"}}}})
	if _, err := e.pool.Exec(t.Context(), `
		INSERT INTO customer_events (workspace_id, kind, name, anonymous_id, properties, context, occurred_at)
		SELECT $1::uuid, 'page', 'page.viewed', 'hist-' || i, '{}'::jsonb, jsonb_build_object('page', jsonb_build_object('path', '/pricing')), now() - interval '3 days'
		FROM generate_series(1, 400) i
		UNION ALL
		SELECT $1::uuid, 'track', 'signup_started', 'hist-' || i, '{}'::jsonb, '{}'::jsonb, now() - interval '2 days' FROM generate_series(1, 400) i WHERE i % 10 = 0`, ws); err != nil {
		t.Fatal(err)
	}

	// Without the workspace's own AI model, nothing runs.
	if c := owner.must(409, "POST", base+"/ab-tests/suggestions", nil)["error"].(map[string]any)["code"]; c != "connector_needed" {
		t.Fatalf("no AI: %v", c)
	}
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "claude", "credentials": map[string]any{"api_key": "sk-ant-good"}})
	owner.must(400, "POST", base+"/ab-tests/suggestions", map[string]any{"count": 9})
	owner.must(400, "POST", base+"/ab-tests/versions", map[string]any{"flag": "hero"})
	owner.must(400, "POST", base+"/ab-tests/versions", map[string]any{"flag": "hero", "prompt": "x", "fields": []string{"Head Line"}})

	run := func(path string, body any) map[string]any {
		t.Helper()
		r := owner.must(202, "POST", base+path, body)
		if r["status"] != "queued" {
			t.Fatalf("%s: %v", path, r)
		}
		return waitFor(t, owner, base+"/ab-test-ai-runs/"+r["id"].(string), func(m map[string]any) bool { return m["status"] == "ready" || m["status"] == "failed" })
	}

	// Suggestions come with the engine's own plan for each.
	sg := run("/ab-tests/suggestions", map[string]any{"notes": "Our visitors are agencies"})
	list := sg["output"].(map[string]any)["suggestions"].([]any)
	if sg["status"] != "ready" || len(list) != 1 {
		t.Fatalf("suggestions: %v", sg)
	}
	first := list[0].(map[string]any)
	if first["title"] != "Shorter pricing headline" || first["baseline"] != 0.1 || first["weekly_visitors"] != 100.0 || first["warning"] == "" {
		t.Fatalf("suggestion: %v", first)
	}
	// Versions from a prompt, with fields read from the flag's content.
	vs := run("/ab-tests/versions", map[string]any{"flag": "hero", "prompt": "More urgent, for agencies", "count": 2})
	out := vs["output"].(map[string]any)
	versions := out["versions"].([]any)
	if vs["status"] != "ready" || len(versions) != 2 || out["flag_key"] != "hero" || vs["flag_id"] == nil {
		t.Fatalf("versions: %v", vs)
	}
	if v := versions[1].(map[string]any); v["key"] == "control" || v["key"] == "faster" || v["content"].(map[string]any)["headline"] != "Built for agencies" {
		t.Fatalf("a used key wasn't renamed: %v", v)
	}

	// A summary needs a started test; the engine, not the AI, recommends.
	ab := owner.must(201, "POST", base+"/ab-tests", map[string]any{"flag": "hero", "goals": []map[string]any{{"kind": "event", "name": "signup_started"}}})
	owner.must(409, "POST", base+"/ab-tests/"+ab["id"].(string)+"/summary", nil)
	owner.must(200, "POST", base+"/ab-tests/"+ab["id"].(string)+"/start", nil)
	sum := run("/ab-tests/"+ab["id"].(string)+"/summary", nil)
	so := sum["output"].(map[string]any)
	if sum["status"] != "ready" || so["recommendation"] != "wait" || so["headline"] != "The faster headline won" || len(so["next_steps"].([]any)) != 2 {
		t.Fatalf("summary: %v", sum)
	}
	if l := owner.must(200, "GET", base+"/ab-test-ai-runs?test="+ab["id"].(string), nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("runs for the test: %v", l)
	}
	if l := owner.must(200, "GET", base+"/ab-test-ai-runs?kind=versions", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("versions runs: %v", l)
	}
	owner.must(404, "GET", base+"/ab-test-ai-runs/00000000-0000-0000-0000-000000000000", nil)

	// What the AI was told: the data, the safety rules, the engine's call.
	abAIPrompts.Lock()
	sent := abAIPrompts.last
	abAIPrompts.Unlock()
	for kind, want := range map[string][]string{
		"suggest":  {"/pricing: 400", "signup_started", "Our visitors are agencies", "Never invent facts"},
		"versions": {"Outflare for teams", "More urgent, for agencies", "headline"},
		"summary":  {"wait:", "signup_started", "Never invent facts"},
	} {
		for _, w := range want {
			if !strings.Contains(sent[kind], w) {
				t.Errorf("%s prompt lacks %q", kind, w)
			}
		}
	}
	seen := map[string]int{}
	for _, ev := range got() {
		seen[ev.Name]++
	}
	if seen["ab_test.ai_requested"] != 3 || seen["ab_test.ai_ready"] != 3 {
		t.Errorf("events: %v", seen)
	}
}
