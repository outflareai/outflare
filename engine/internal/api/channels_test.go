package api_test

import (
	"context"
	"testing"
)

// The channel catalog says what each channel needs and whether the
// workspace, or one brand, can use it now: a missing connection is
// connect first, connected without data is needs data, and a brand only
// counts the connections that serve it.
func TestChannelsAndTheirReadiness(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	find := func(list map[string]any, id string) map[string]any {
		t.Helper()
		for _, x := range list["data"].([]any) {
			if c := x.(map[string]any); c["id"] == id {
				return c
			}
		}
		t.Fatalf("no channel %s in %v", id, list)
		return nil
	}
	all := owner.must(200, "GET", base+"/channels", nil)
	if len(all["data"].([]any)) < 9 {
		t.Fatalf("channels: %d", len(all["data"].([]any)))
	}
	ads := find(all, "paid_ads")
	if r := ads["readiness"].(map[string]any); r["state"] != "connect_first" || len(r["missing_connections"].([]any)) != 1 {
		t.Fatalf("paid ads with nothing connected: %v", r)
	}
	if acts := ads["actions"].([]any); len(acts) != 4 || acts[0].(map[string]any)["category"] != "spend" {
		t.Fatalf("paid ads actions: %v", acts)
	}

	// Connected but no account read yet: needs data; after a sync: ready.
	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	if r := find(owner.must(200, "GET", base+"/channels", nil), "paid_ads")["readiness"].(map[string]any); r["state"] != "needs_data" {
		t.Fatalf("paid ads before a sync: %v", r)
	}
	if _, err := e.ads.SyncOnce(ctx, ws, i["id"].(string)); err != nil {
		t.Fatal(err)
	}
	if r := find(owner.must(200, "GET", base+"/channels", nil), "paid_ads")["readiness"].(map[string]any); r["state"] != "ready" {
		t.Fatalf("paid ads after a sync: %v", r)
	}

	// Content: the CMS connected, but the brand's context not written yet.
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "wordpress", "credentials": map[string]string{"site_url": "https://www.example.com", "username": "ana", "api_key": "app pass"}})
	if r := find(owner.must(200, "GET", "/v1/brands/"+brand+"/channels", nil), "content")["readiness"].(map[string]any); r["state"] != "needs_data" || len(r["missing_data"].([]any)) != 1 {
		t.Fatalf("content before the brand's context: %v", r)
	}
	owner.must(200, "PATCH", base+"/brands/"+brand+"/context", map[string]any{"about": "Northpeak makes climbing gear.", "audience": "Climbers"})
	if r := find(owner.must(200, "GET", "/v1/brands/"+brand+"/channels", nil), "content")["readiness"].(map[string]any); r["state"] != "ready" {
		t.Fatalf("content with the context written: %v", r)
	}

	// Another brand doesn't share the first one's connections when they are
	// kept to it; a brand that isn't the workspace's is 404.
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak Outdoors", "handle": "outdoors"})["id"].(string)
	if r := find(owner.must(200, "GET", "/v1/brands/"+other+"/channels", nil), "content")["readiness"].(map[string]any); r["state"] == "" {
		t.Fatalf("other brand: %v", r)
	}
	owner.must(404, "GET", "/v1/brands/00000000-0000-0000-0000-000000000000/channels", nil)
}
