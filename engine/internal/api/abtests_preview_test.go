package api_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// Preview links show each version on the pages that show the flag, and a
// person's check per version feeds the checklist until the content changes.
func TestABTestPreviewAndQA(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/flags", map[string]any{"key": "cta", "name": "Call to action",
		"variants": []map[string]any{{"key": "control", "weight": 50}, {"key": "bold", "weight": 50, "payload": map[string]any{"text": "Start now"}}}})
	ab := owner.must(201, "POST", base+"/ab-tests", map[string]any{"flag": "cta", "goals": []map[string]any{{"kind": "event", "name": "signup_started"}}})
	id := ab["id"].(string)
	previews := base + "/ab-tests/" + id + "/previews"
	qa := func() string {
		t.Helper()
		for _, c := range owner.must(200, "GET", base+"/ab-tests/"+id+"/checklist", nil)["checks"].([]any) {
			if c := c.(map[string]any); c["id"] == "qa" {
				return c["status"].(string)
			}
		}
		t.Fatal("no qa line in the checklist")
		return ""
	}

	// No page has shown the flag: no links, and a hint that says so.
	p := owner.must(200, "GET", previews, nil)
	if len(p["pages"].([]any)) != 0 || len(p["versions"].([]any)) != 2 || !strings.Contains(p["hint"].(string), "No page") {
		t.Fatalf("empty previews: %v", p)
	}
	if qa() != "warn" {
		t.Fatal("unchecked versions should warn")
	}
	// A page given by address keeps its own query.
	owner.must(400, "GET", previews+"?page=javascript:alert(1)", nil)
	p = owner.must(200, "GET", previews+"?page="+url.QueryEscape("https://acme.example/pricing?plan=team"), nil)
	links := p["pages"].([]any)[0].(map[string]any)["links"].([]any)
	if l := links[1].(map[string]any); l["variant"] != "bold" || l["url"] != "https://acme.example/pricing?of_flag=cta%3Abold&plan=team" {
		t.Fatalf("link: %v", l)
	}

	// Pages that showed it, busiest first, without their query strings.
	if _, err := e.pool.Exec(t.Context(), `
		INSERT INTO customer_events (workspace_id, kind, name, anonymous_id, properties, context, occurred_at)
		SELECT $1::uuid, 'track', 'experiment_viewed', 'p-' || i, '{"flag": "cta", "variant": "control"}'::jsonb,
			jsonb_build_object('page', jsonb_build_object('url', CASE WHEN i % 4 = 0 THEN 'https://acme.example/' ELSE 'https://acme.example/pricing?utm_source=x' || i END)),
			now() - interval '1 day' FROM generate_series(1, 40) i`, ws); err != nil {
		t.Fatal(err)
	}
	pages := owner.must(200, "GET", previews, nil)["pages"].([]any)
	if len(pages) != 2 {
		t.Fatalf("pages: %v", pages)
	}
	if first := pages[0].(map[string]any); first["url"] != "https://acme.example/pricing" || first["visitors"] != 30.0 || len(first["links"].([]any)) != 2 {
		t.Fatalf("busiest page: %v", first)
	}

	// Checks: a broken version stops the test from starting.
	checks := base + "/ab-tests/" + id + "/checks"
	owner.must(400, "POST", checks, map[string]any{"variant": "loud", "status": "ok"})
	owner.must(400, "POST", checks, map[string]any{"variant": "bold", "status": "fine"})
	out := owner.must(200, "POST", checks, map[string]any{"variant": "bold", "status": "broken", "note": "The button overlaps the price"})
	if c := out["versions"].([]any)[1].(map[string]any)["check"].(map[string]any); c["status"] != "broken" || c["stale"] != false || c["checked_by"] == nil {
		t.Fatalf("check: %v", out)
	}
	if qa() != "fail" || owner.must(200, "GET", base+"/ab-tests/"+id+"/checklist", nil)["ready"] != false {
		t.Fatal("a broken version should fail the checklist")
	}
	owner.must(200, "POST", checks, map[string]any{"variant": "bold", "status": "ok"})
	owner.must(200, "POST", checks, map[string]any{"variant": "control", "status": "ok"})
	if qa() != "ok" {
		t.Fatal("every version checked should pass")
	}
	// New content needs a new look.
	owner.must(200, "PATCH", base+"/flags/cta", map[string]any{"variants": []map[string]any{{"key": "control", "weight": 50}, {"key": "bold", "weight": 50, "payload": map[string]any{"text": "Start today"}}}})
	if v := owner.must(200, "GET", previews, nil)["versions"].([]any); v[1].(map[string]any)["check"].(map[string]any)["stale"] != true || v[0].(map[string]any)["check"].(map[string]any)["stale"] != false {
		t.Fatalf("stale after a change: %v", v)
	}
	if qa() != "warn" {
		t.Fatal("a changed version should warn")
	}

	// A finished test takes no more checks.
	owner.must(200, "POST", base+"/ab-tests/"+id+"/start", nil)
	owner.must(200, "POST", base+"/ab-tests/"+id+"/end", map[string]any{"decision": ""})
	owner.must(409, "POST", checks, map[string]any{"variant": "bold", "status": "ok"})

	n := 0
	for _, ev := range got() {
		if ev.Name == "ab_test.version_checked" {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("version_checked events: %d", n)
	}
}
