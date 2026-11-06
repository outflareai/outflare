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
	// Richer changes, the version's CSS and a split URL redirect save too;
	// a redirect off the brand's domains and CSS that loads anything don't.
	site.must(400, "POST", path, map[string]any{"token": edit, "changes": []any{}, "redirect": "https://elsewhere.example/"})
	site.must(400, "POST", path, map[string]any{"token": edit, "changes": []any{}, "css": "h1{background:url(https://x.example/a.png)}"})
	site.must(400, "POST", path, map[string]any{"token": edit, "changes": []map[string]any{{"selector": "h1", "style": map[string]string{"background-image": "none"}}}})
	rich := site.must(200, "POST", path, map[string]any{"token": edit, "css": "h1{letter-spacing:-.02em}", "redirect": "/pricing-b", "changes": []map[string]any{
		{"selector": "h1", "rich": "**Ship** faster"}, {"selector": "h1", "style": map[string]string{"color": "#0f766e"}, "device": "mobile"},
		{"selector": ".cta", "move": "before", "target": "h1"}, {"selector": "h1", "insert": map[string]any{"where": "after", "kind": "button", "text": "Start", "href": "/signup"}},
	}})
	if rich["css"] != "h1{letter-spacing:-.02em}" || rich["redirect"] != "/pricing-b" || len(rich["changes"].([]any)) != 4 {
		t.Fatalf("rich save: %v", rich)
	}
	// Absent css and redirect stay; "" removes them.
	kept := site.must(200, "POST", path, map[string]any{"token": edit, "changes": rich["changes"], "redirect": ""})
	if kept["css"] != "h1{letter-spacing:-.02em}" || kept["redirect"] != "" {
		t.Fatalf("kept: %v", kept)
	}
	saved = site.must(200, "POST", path, map[string]any{"token": edit, "css": "", "changes": []map[string]any{
		{"selector": "h1", "text": "Ship faster"}, {"selector": ".promo", "hide": true},
	}})
	if saved["css"] != "" {
		t.Fatalf("css not removed: %v", saved)
	}

	// Visitors in the version get them.
	// The site runs tests on its own (no data-flags needed), and those in
	// the version are told what to hide on their next visit.
	sk := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "Site"})
	key := sk["key"].(string)
	if sk["tests"] != true || !strings.Contains(sk["flicker_guard"].(string), "of_hide") {
		t.Fatalf("site key: %v", sk)
	}
	var faster, control string
	for i := 0; i < 40 && (faster == "" || control == ""); i++ {
		anon := "visitor-" + strings.Repeat("a", i+1)
		_, out := askSiteFlags(t, e, "https://northpeak.example", key, anon, "https://northpeak.example/")
		if out["tests"] != true {
			t.Fatalf("tests not on: %v", out)
		}
		hide := out["hide"].([]any)
		// Every visitor the page puts in the test counts as shown, the
		// control included.
		if ex := out["expose"].([]any); len(ex) != 1 || ex[0] != "hero" {
			t.Fatalf("expose: %v", out)
		}
		if out["flags"].(map[string]any)["hero"] == "faster" {
			if cs := out["payloads"].(map[string]any)["hero"].(map[string]any)["changes"]; cs == nil {
				t.Fatalf("no changes served: %v", out)
			}
			if len(hide) != 2 || hide[0] != ".promo" || hide[1] != "h1" {
				t.Fatalf("hide: %v", hide)
			}
			faster = anon
		} else {
			if len(hide) != 0 {
				t.Fatalf("control told to hide: %v", hide)
			}
			control = anon
		}
	}
	// Turned off, the script no longer asks by itself.
	owner.must(200, "PATCH", base+"/site-keys/"+sk["id"].(string), map[string]any{"tests": false})
	if _, out := askSiteFlags(t, e, "https://northpeak.example", key, faster, "https://northpeak.example/"); out["tests"] != false {
		t.Fatalf("tests still on: %v", out)
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
