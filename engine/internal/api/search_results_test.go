package api_test

import (
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestSearchResults(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	// Searching needs the workspace's own SERP connection.
	owner.must(409, "POST", base+"/search-results", map[string]any{"query": "proxy pricing"})
	dfs := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})["id"].(string)

	for _, bad := range []map[string]any{{"query": ""}, {"query": "x", "country": "USA"}, {"query": "x", "device": "tablet"},
		{"query": "x", "depth": 5}, {"query": "x", "integration_id": "00000000-0000-0000-0000-000000000000"}} {
		owner.must(400, "POST", base+"/search-results", bad)
	}

	est := owner.must(200, "POST", base+"/search-results", map[string]any{"query": "proxy pricing", "estimate_only": true})
	if est["integration_id"] != dfs || est["estimate"].(float64) <= 0 || est["budget"].(map[string]any)["unit"] != "USD" {
		t.Fatalf("estimate: %v", est)
	}
	if spend := owner.must(200, "GET", base+"/spend", nil)["data"].([]any); len(spend) != 0 {
		t.Fatalf("an estimate spent: %v", spend)
	}

	res := owner.must(200, "POST", base+"/search-results", map[string]any{"query": "proxy pricing", "country": "gb"})
	items := res["items"].([]any)
	if res["engine"] != "google" || res["country"] != "GB" || len(items) == 0 || res["spent"].(float64) <= 0 {
		t.Fatalf("search: %v", res)
	}
	if first := items[0].(map[string]any); first["position"].(float64) != 1 || first["url"] == "" || first["domain"] == "" {
		t.Fatalf("first result: %v", first)
	}
	// The search is spend on the connection, and an event without the query.
	if spend := owner.must(200, "GET", base+"/spend", nil)["data"].([]any); len(spend) != 1 || spend[0].(map[string]any)["provider"] != "dataforseo" {
		t.Fatalf("spend: %v", spend)
	}
	var found bool
	for _, ev := range got() {
		if ev.Name == "web_search.done" {
			found = true
			if ev.Props["results"] != len(items) || ev.Props["query"] != nil || ev.Props["connector"] != "dataforseo" {
				t.Errorf("event props: %v", ev.Props)
			}
		}
	}
	if !found {
		t.Error("no web_search.done event")
	}
}
