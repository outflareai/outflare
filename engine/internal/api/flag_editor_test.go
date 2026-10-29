package api_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// The visual editor: an edit link carries a signed token for one version
// of one flag, and the editor reads and saves that version's changes with
// it, as long as its person can still manage analytics.
func TestFlagVisualEditor(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/flags", map[string]any{
		"key": "hero", "name": "Hero", "status": "on",
		"variants": []map[string]any{{"key": "control", "weight": 50}, {"key": "faster", "weight": 50, "payload": map[string]any{"headline": "Ship faster"}}},
	})

	// Bad versions and pages are refused, and API tokens can't open it.
	owner.must(400, "POST", base+"/flags/hero/edit-link", map[string]any{"variant": "nope", "url": "https://northpeak.example/"})
	owner.must(400, "POST", base+"/flags/hero/edit-link", map[string]any{"variant": "faster", "url": "javascript:alert(1)"})
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Bot", "scopes": []string{"analytics.manage", "analytics.read"}})["token"].(string)
	bot := &client{t: t, h: e.h, bearer: tok}
	bot.must(403, "POST", base+"/flags/hero/edit-link", map[string]any{"variant": "faster", "url": "https://northpeak.example/"})

	link := owner.must(200, "POST", base+"/flags/hero/edit-link", map[string]any{"variant": "faster", "url": "https://northpeak.example/pricing?ref=a"})
	u, err := url.Parse(link["url"].(string))
	if err != nil || u.Host != "northpeak.example" || u.Path != "/pricing" || u.Query().Get("ref") != "a" || u.Query().Get("of_flag") != "hero:faster" {
		t.Fatalf("link: %v", link)
	}
	edit := u.Query().Get("of_edit")
	site := &client{t: t, h: e.h}
	path := "/v1/site/flags/edits"

	info := site.must(200, "GET", path+"?token="+url.QueryEscape(edit), nil)
	if info["flag_key"] != "hero" || info["variant"] != "faster" || len(info["changes"].([]any)) != 0 {
		t.Fatalf("read: %v", info)
	}

	// Tampered, unsafe and malformed saves are refused.
	site.must(401, "GET", path+"?token="+url.QueryEscape(edit+"x"), nil)
	msg, _, _ := strings.Cut(edit, ".")
	site.must(401, "GET", path+"?token="+url.QueryEscape(msg+".bm90LWEtcmVhbC1zaWduYXR1cmU"), nil)
	site.must(400, "POST", path, map[string]any{"token": edit, "changes": []map[string]any{{"selector": "a.cta", "href": "javascript:alert(1)"}}})
	site.must(400, "POST", path, map[string]any{"token": edit, "changes": []map[string]any{{"selector": "h1", "text": "x", "hide": true}}})
	site.must(400, "POST", path, map[string]any{"token": edit})

	// A save sets the changes and keeps the rest of the payload.
	saved := site.must(200, "POST", path, map[string]any{"token": edit, "changes": []map[string]any{
		{"selector": "h1", "text": "Ship faster"}, {"selector": ".promo", "hide": true},
	}})
	if len(saved["changes"].([]any)) != 2 {
		t.Fatalf("saved: %v", saved)
	}
	f := owner.must(200, "GET", base+"/flags/hero", nil)
	p := f["variants"].([]any)[1].(map[string]any)["payload"].(map[string]any)
	if p["headline"] != "Ship faster" || len(p["changes"].([]any)) != 2 {
		t.Fatalf("payload: %v", p)
	}
	// Visitors in the version get them.
	key := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "Site"})["key"].(string)
	for i := 0; i < 40; i++ {
		_, out := askSiteFlags(t, e, "https://northpeak.example", key, "visitor-"+strings.Repeat("a", i+1), "https://northpeak.example/")
		if out["flags"].(map[string]any)["hero"] == "faster" {
			if cs := out["payloads"].(map[string]any)["hero"].(map[string]any)["changes"]; cs == nil {
				t.Fatalf("no changes served: %v", out)
			}
			break
		}
	}

	// Someone who can no longer manage analytics can't use their link.
	mate, mateID := joinAs(t, owner, ws, "mira@example.com", map[string]any{"role": "admin"})
	mlink := mate.must(200, "POST", base+"/flags/hero/edit-link", map[string]any{"variant": "control", "url": "https://northpeak.example/"})
	mu, _ := url.Parse(mlink["url"].(string))
	mtok := mu.Query().Get("of_edit")
	site.must(200, "GET", path+"?token="+url.QueryEscape(mtok), nil)
	owner.must(200, "PATCH", base+"/members/"+mateID, map[string]any{"role": "viewer"})
	site.must(401, "POST", path, map[string]any{"token": mtok, "changes": []map[string]any{}})

	// An archived flag's link stops working.
	owner.must(204, "DELETE", base+"/flags/hero", nil)
	site.must(404, "GET", path+"?token="+url.QueryEscape(edit), nil)

	var visual bool
	for _, ev := range got() {
		if ev.Name == "flag.updated" && ev.ActorType == "human" && ev.WorkspaceID == ws {
			visual = true
		}
	}
	if !visual {
		t.Fatal("no flag.updated event for the save")
	}
}
