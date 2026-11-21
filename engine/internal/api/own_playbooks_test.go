package api_test

import (
	"context"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// A brand's own playbook groups what the brand already has, shows what
// each part has done, never takes another brand's parts, and leaves the
// parts alone when it is removed.
func TestOwnPlaybooks(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	rule := owner.must(201, "POST", base+"/rules", map[string]any{"name": "Big order", "emit_event": "order.big",
		"trigger": map[string]any{"kind": "event", "event": "order_placed"}})["id"].(string)
	list := owner.must(201, "POST", base+"/smart-lists", map[string]any{"name": "Big spenders", "kind": "static"})["id"].(string)
	journey := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Thank big spenders", "steps": []map[string]any{{"id": "done", "kind": "exit"}}})["id"].(string)
	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme"})["id"].(string)
	owner.must(201, "POST", base+"/signals", map[string]any{"record_id": acme, "type": "upsell", "title": "Asked about seats"})

	if got := owner.must(200, "GET", base+"/own-playbooks", nil); len(got["data"].([]any)) != 0 {
		t.Fatalf("none yet: %v", got)
	}
	part := func(kind, id string) map[string]any { return map[string]any{"kind": kind, "id": id} }
	owner.must(400, "POST", base+"/own-playbooks", map[string]any{"title": ""})
	owner.must(400, "POST", base+"/own-playbooks", map[string]any{"title": "x", "parts": []any{part("score", rule)}})
	owner.must(400, "POST", base+"/own-playbooks", map[string]any{"title": "x", "parts": []any{part("rule", rule), part("rule", rule)}})
	owner.must(400, "POST", base+"/own-playbooks", map[string]any{"title": "x", "parts": []any{part("signal_type", "no_such_type")}})

	captured := track.Capture(t)
	made := owner.must(201, "POST", base+"/own-playbooks", map[string]any{"title": "Grow big spenders", "summary": "Spot big orders and thank them.",
		"parts": []any{part("rule", rule), part("journey", journey), part("smart_list", list), part("signal_type", "upsell")}})
	id := made["id"].(string)
	parts := made["parts"].([]any)
	if len(parts) != 4 || parts[0].(map[string]any)["name"] != "Big order" || parts[3].(map[string]any)["key"] != "upsell" || parts[3].(map[string]any)["count"] != float64(1) {
		t.Fatalf("parts: %v", parts)
	}
	if made["signals"].(map[string]any)["open"] != float64(1) {
		t.Fatalf("signals: %v", made["signals"])
	}
	found := false
	for _, ev := range captured() {
		found = found || ev.Name == "own_playbook.created"
	}
	if !found {
		t.Fatal("no own_playbook.created event")
	}

	// Only what is sent changes; parts replaces the list.
	got := owner.must(200, "PATCH", base+"/own-playbooks/"+id, map[string]any{"title": "Big spenders"})
	if got["title"] != "Big spenders" || got["summary"] != "Spot big orders and thank them." || len(got["parts"].([]any)) != 4 {
		t.Fatalf("renamed: %v", got)
	}
	got = owner.must(200, "PATCH", base+"/own-playbooks/"+id, map[string]any{"parts": []any{part("journey", journey), part("rule", rule)}})
	if p := got["parts"].([]any); len(p) != 2 || p[0].(map[string]any)["kind"] != "journey" {
		t.Fatalf("reordered: %v", got["parts"])
	}
	if one := owner.must(200, "GET", base+"/own-playbooks/"+id, nil); one["title"] != "Big spenders" {
		t.Fatalf("one: %v", one)
	}

	// Another brand neither sees it nor lends its parts.
	other := "/v1/brands/" + owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Second"})["id"].(string)
	if got := owner.must(200, "GET", other+"/own-playbooks", nil); len(got["data"].([]any)) != 0 {
		t.Fatalf("leaked: %v", got)
	}
	owner.must(404, "GET", other+"/own-playbooks/"+id, nil)
	owner.must(404, "PATCH", other+"/own-playbooks/"+id, map[string]any{"title": "Mine now"})
	owner.must(400, "POST", other+"/own-playbooks", map[string]any{"title": "Borrowed", "parts": []any{part("rule", rule)}})

	// Removing it leaves the parts.
	owner.must(204, "DELETE", base+"/own-playbooks/"+id, nil)
	owner.must(404, "DELETE", base+"/own-playbooks/"+id, nil)
	owner.must(200, "GET", base+"/rules/"+rule, nil)
	owner.must(200, "GET", base+"/journeys/"+journey, nil)
}

// A playbook of the brand's own can also hold a score, a routing rule, an
// email and a metric, and says whether its journeys worked.
func TestOwnPlaybookPartsAndLift(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	me := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	score := owner.must(201, "POST", base+"/scores", map[string]any{"key": "fit", "label": "Fit", "record_type": "account", "base": 50,
		"high_from": 60, "low_below": 40, "factors": []any{map[string]any{"label": "Has a name", "points": 10,
			"conditions": []map[string]any{{"attribute": "name", "op": "exists"}}}}})["id"].(string)
	routed := owner.must(200, "PUT", base+"/routing", map[string]any{"rules": []any{
		map[string]any{"name": "Everyone", "record_type": "account", "members": []string{me}},
	}})["data"].([]any)[0].(map[string]any)["id"].(string)
	email := owner.must(201, "POST", base+"/email-templates", map[string]any{"name": "Welcome back", "subject": "Hello", "html": "<p>Hello</p>"})["id"].(string)
	var metric string
	if err := e.pool.QueryRow(ctx, `INSERT INTO metric_definitions (workspace_id, brand_id, key, name, aggregate, events, window_days)
		VALUES ($1, $2, 'logins_30d', 'Logins', 'count', '{user.login}', 30) RETURNING id::text`, ws, brand).Scan(&metric); err != nil {
		t.Fatal(err)
	}
	plain := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Plain", "steps": []map[string]any{{"id": "done", "kind": "exit"}}})["id"].(string)
	split := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Win back", "steps": []map[string]any{
		{"id": "test", "kind": "split", "split_by": "random", "paths": []map[string]any{
			{"id": "go", "weight": 80, "next": "later"}, {"id": "control", "weight": 20, "holdout": true}}},
		{"id": "later", "kind": "wait", "wait_seconds": 60},
	}})["id"].(string)

	part := func(kind, id string) map[string]any { return map[string]any{"kind": kind, "id": id} }
	// Another kind's id is not one of this kind.
	owner.must(400, "POST", base+"/own-playbooks", map[string]any{"title": "x", "parts": []any{part("routing_rule", score)}})
	owner.must(400, "POST", base+"/own-playbooks", map[string]any{"title": "x", "parts": []any{part("email", metric)}})
	made := owner.must(201, "POST", base+"/own-playbooks", map[string]any{"title": "Win back quiet accounts",
		"parts": []any{part("score", score), part("routing_rule", routed), part("email", email), part("metric", metric), part("journey", plain), part("journey", split)}})
	parts := made["parts"].([]any)
	if len(parts) != 6 {
		t.Fatalf("parts: %v", parts)
	}
	want := []string{"Fit", "Everyone", "Welcome back", "Logins"}
	for i, name := range want {
		if p := parts[i].(map[string]any); p["name"] != name || p["status"] != "active" {
			t.Fatalf("part %d: %v", i, p)
		}
	}
	if parts[3].(map[string]any)["count"] != float64(0) || parts[3].(map[string]any)["kind"] != "metric" {
		t.Fatalf("metric: %v", parts[3])
	}

	id := made["id"].(string)
	lift := owner.must(200, "GET", base+"/own-playbooks/"+id+"/lift", nil)
	js := lift["journeys"].([]any)
	if lift["measured"] != true || lift["playbook_id"] != id || len(js) != 2 {
		t.Fatalf("lift: %v", lift)
	}
	if p, w := js[0].(map[string]any), js[1].(map[string]any); p["name"] != "Plain" || p["measured"] != false || w["measured"] != true || w["holdout_percent"] != float64(20) || w["ran"] == nil {
		t.Fatalf("journeys: %v", js)
	}
	owner.must(404, "GET", base+"/own-playbooks/00000000-0000-4000-8000-000000000000/lift", nil)
	// A playbook with no journey has nothing to measure.
	none := owner.must(201, "POST", base+"/own-playbooks", map[string]any{"title": "Scores only", "parts": []any{part("score", score)}})["id"].(string)
	if got := owner.must(200, "GET", base+"/own-playbooks/"+none+"/lift", nil); got["measured"] != false || len(got["journeys"].([]any)) != 0 {
		t.Fatalf("no journeys: %v", got)
	}
}
