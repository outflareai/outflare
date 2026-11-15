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
