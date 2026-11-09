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

	// A search is one brand's: an agent that works for every brand searches
	// on the brand's route, and the change waiting for a person is that
	// brand's alone, so another brand's Agents page never shows it.
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Geonode", "website": "https://www.geonode.com/"})
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "website": "https://repocket.com/"})
	brands := map[string]string{}
	for _, b := range owner.must(200, "GET", base+"/brands", nil)["data"].([]any) {
		m := b.(map[string]any)
		brands[m["handle"].(string)] = m["id"].(string)
	}
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Claude", "scopes": []string{"seo.manage", "actions.read"}, "action_gate": true})
	agent := &client{t: t, h: owner.h, bearer: tok["token"].(string)}
	st, held := agent.do("POST", "/v1/brands/"+brands["geonode"]+"/search-results", map[string]any{"query": "residential proxies"})
	if st != 202 || held["action"].(map[string]any)["brand_id"] != brands["geonode"] {
		t.Fatalf("a search on the brand route waits as the brand's: %d %v", st, held)
	}
	if l := owner.must(200, "GET", "/v1/brands/"+brands["repocket"]+"/actions?status=held", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("another brand waits on it: %v", l)
	}
	if l := owner.must(200, "GET", "/v1/brands/"+brands["geonode"]+"/actions?status=held", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("the brand waits on it: %v", l)
	}
}
