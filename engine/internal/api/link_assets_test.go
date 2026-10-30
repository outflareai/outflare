package api_test

import (
	"context"
	"strings"
	"testing"
)

func TestLinkAssetsStartThePromotePlaybook(t *testing.T) {
	ctx := context.Background()
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	siteID := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.northpeak.example/"})["id"].(string)
	// The playbook searches and reads links on the workspace's own account.
	if _, err := e.pool.Exec(ctx, `INSERT INTO integrations (workspace_id, connector, name, credentials) VALUES ($1, 'dataforseo', 'DataForSEO', '\x00')`, ws); err != nil {
		t.Fatal(err)
	}
	out := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Agent", "scopes": []string{"missions.read", "missions.run"}})
	tokenID := out["api_token"].(map[string]any)["id"].(string)

	// It suggests a run per published page, with no inputs needed.
	auto := owner.must(201, "POST", base+"/playbook-automations", map[string]any{"playbook_id": "link-promote-an-asset",
		"inputs": map[string]any{"site": "northpeak.example"}, "runner": "external", "token_id": tokenID})
	if auto["kind"] != "event" || auto["event_type"] != "asset.published" || auto["gather"] != "each" {
		t.Fatalf("automation: %v", auto)
	}
	aid := auto["id"].(string)
	owner.must(200, "POST", base+"/playbook-automations/"+aid+"/approve", nil)

	// Marking a page published: on its site, once.
	owner.must(400, "POST", base+"/link-building/assets", map[string]any{"url": "northpeak.example/guide"})
	a := owner.must(201, "POST", base+"/link-building/assets", map[string]any{"url": "https://northpeak.example/guides/layering", "title": "Layering guide", "topic": "how to layer for hiking"})
	if a["site_id"] != siteID || a["title"] != "Layering guide" || a["published_at"] == nil {
		t.Fatalf("asset: %v", a)
	}
	owner.must(409, "POST", base+"/link-building/assets", map[string]any{"url": "https://northpeak.example/guides/layering"})
	off := owner.must(201, "POST", base+"/link-building/assets", map[string]any{"url": "https://elsewhere.example/page"})
	if off["site_id"] != nil {
		t.Fatalf("not on a site: %v", off)
	}
	if l := owner.must(200, "GET", base+"/link-building/assets", nil)["data"].([]any); len(l) != 2 {
		t.Fatalf("list: %v", l)
	}

	// Each one starts a run of the playbook, which names the event.
	if _, err := e.pool.Exec(ctx, `UPDATE playbook_automations SET next_run_at = now() WHERE id = $1`, aid); err != nil {
		t.Fatal(err)
	}
	if n, err := e.srv.RunDueAutomations(ctx); err != nil || n != 2 {
		t.Fatalf("runs: %d, %v", n, err)
	}
	runs := owner.must(200, "GET", base+"/playbook-automations/"+aid+"/runs", nil)["data"].([]any)
	if len(runs) != 2 || !strings.Contains(runs[1].(map[string]any)["instructions"].(string), "asset.published link_asset "+a["id"].(string)) {
		t.Fatalf("runs: %v", runs)
	}
}
