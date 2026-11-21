package api_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestClickActions(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	geo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "website": "https://northpeak.example"})["id"].(string)
	rp := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Summitly", "website": "https://summitly.example"})["id"].(string)
	g := "/v1/brands/" + geo

	k := owner.must(201, "POST", g+"/site-keys", map[string]any{"name": "northpeak.example", "origins": []string{"https://northpeak.example"}})
	key := k["key"].(string)
	other := owner.must(201, "POST", "/v1/brands/"+rp+"/site-keys", map[string]any{"name": "summitly.example", "origins": []string{"https://summitly.example"}})["key"].(string)

	// Nothing captures clicks yet, and the list says so.
	if out := owner.must(200, "GET", g+"/click-actions", nil); out["capturing"] != false || len(out["data"].([]any)) != 0 {
		t.Fatalf("empty list: %v", out)
	}
	owner.must(200, "PATCH", g+"/site-keys/"+k["id"].(string), map[string]any{"capture_clicks": true})

	n := 0
	click := func(origin, key, path string, props map[string]any) {
		t.Helper()
		n++
		props["path"] = path
		code, out := sendSite(t, e.h, origin, key, map[string]any{"type": "track", "event": "Element Clicked", "anonymousId": "anon-1", "messageId": fmt.Sprintf("click-%d", n),
			"properties": props, "context": map[string]any{"page": map[string]any{"url": origin + path, "path": path}}})
		if code != 200 || out["accepted"] != 1.0 {
			t.Fatalf("click: %d %v", code, out)
		}
	}
	count := func(brand, name string) int {
		t.Helper()
		var c int
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM customer_events WHERE workspace_id = $1 AND brand_id = $2 AND name = $3`, ws, brand, name).Scan(&c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	start := map[string]any{"tag": "a", "text": "Start free", "href": "https://app.northpeak.example/signup", "classes": "btn btn-primary"}
	clone := func() map[string]any {
		out := map[string]any{}
		for k, v := range start {
			out[k] = v
		}
		return out
	}
	// A click before it has a name is only Element Clicked.
	click("https://northpeak.example", key, "/pricing", clone())

	// What a rule must say.
	for _, bad := range []map[string]any{
		{"name": "Signup Clicked"},
		{"label": "Start free"},
		{"name": "Element Clicked", "label": "Start free"},
		{"name": "ui.clicked", "label": "Start free"},
		{"name": "Signup Clicked", "label": "Start free", "label_match": "fuzzy"},
		{"name": "Signup Clicked", "label": "Start free", "path": "pricing"},
		{"name": "Signup Clicked", "class": "btn primary"},
	} {
		owner.must(400, "POST", g+"/click-actions", bad)
	}

	events := track.Capture(t)
	act := owner.must(201, "POST", g+"/click-actions", map[string]any{"name": "Signup Clicked", "label": "start free", "href_contains": "/signup", "path": "/pricing*"})
	id := act["id"].(string)
	if act["event"] != "signup_clicked" || act["label_match"] != "exact" || act["enabled"] != true {
		t.Fatalf("created: %v", act)
	}
	if made := named(events(), "click_action.created"); len(made) != 1 || made[0].Props["brand_id"] != geo {
		t.Fatalf("click_action.created: %+v", made)
	}
	owner.must(400, "POST", g+"/click-actions", map[string]any{"name": "signup clicked", "label": "x"})

	// From now on a matching click is stored under the name too.
	click("https://northpeak.example", key, "/pricing", clone())
	click("https://northpeak.example", key, "/pricing/teams", clone())
	// Another page, another label and another brand's click are not it.
	click("https://northpeak.example", key, "/", clone())
	click("https://northpeak.example", key, "/pricing", map[string]any{"tag": "a", "text": "Start free today", "href": "https://app.northpeak.example/signup"})
	click("https://summitly.example", other, "/pricing", clone())
	if got := count(geo, "signup_clicked"); got != 2 {
		t.Fatalf("signup_clicked for the brand: %d", got)
	}
	if got := count(geo, "element_clicked"); got != 5 {
		t.Fatalf("element_clicked for the brand: %d", got)
	}
	if got := count(rp, "signup_clicked"); got != 0 {
		t.Fatalf("the other brand got the named click: %d", got)
	}
	// The same click sent twice is stored once under each name.
	code, _ := sendSite(t, e.h, "https://northpeak.example", key, map[string]any{"type": "track", "event": "Element Clicked", "anonymousId": "anon-1", "messageId": "click-2",
		"properties": map[string]any{"tag": "a", "text": "Start free", "href": "https://app.northpeak.example/signup", "path": "/pricing"}})
	if code != 200 || count(geo, "signup_clicked") != 2 {
		t.Fatalf("a retry stored the named click again: %d", count(geo, "signup_clicked"))
	}

	// The list counts what it matches; it matches the earlier click too.
	out := owner.must(200, "GET", g+"/click-actions", nil)
	list := out["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["matches_7d"] != 3.0 || out["capturing"] != true || out["clicks_7d"] != 5.0 {
		t.Fatalf("list: %v", out)
	}
	if theirs := owner.must(200, "GET", "/v1/brands/"+rp+"/click-actions", nil); len(theirs["data"].([]any)) != 0 {
		t.Fatalf("the other brand sees it: %v", theirs)
	}

	// Clicks seen, most clicked first, saying which already have a name.
	seen := owner.must(200, "GET", g+"/click-actions/seen", nil)
	groups := seen["data"].([]any)
	if len(groups) != 2 || seen["clicks"] != 5.0 {
		t.Fatalf("seen: %v", seen)
	}
	if top := groups[0].(map[string]any); top["count"] != 4.0 || top["text"] != "Start free" || top["action"] != "Signup Clicked" {
		t.Fatalf("top click: %v", top)
	}
	if second := groups[1].(map[string]any); second["count"] != 1.0 || second["action"] != "" {
		t.Fatalf("second click: %v", second)
	}
	owner.must(400, "GET", g+"/click-actions/seen?days=0", nil)

	// Off: nothing more is stored. Changed: the new rule counts.
	events = track.Capture(t)
	if off := owner.must(200, "PATCH", g+"/click-actions/"+id, map[string]any{"enabled": false}); off["enabled"] != false || off["label"] != "start free" {
		t.Fatalf("off: %v", off)
	}
	if up := named(events(), "click_action.updated"); len(up) != 1 || up[0].Props["enabled"] != false {
		t.Fatalf("click_action.updated: %+v", up)
	}
	click("https://northpeak.example", key, "/pricing", clone())
	if got := count(geo, "signup_clicked"); got != 2 {
		t.Fatalf("stored while off: %d", got)
	}
	owner.must(200, "PATCH", g+"/click-actions/"+id, map[string]any{"enabled": true, "label": "today", "label_match": "contains", "path": ""})
	click("https://northpeak.example", key, "/", map[string]any{"tag": "a", "text": "Start free today", "href": "https://app.northpeak.example/signup"})
	if got := count(geo, "signup_clicked"); got != 3 {
		t.Fatalf("after the change: %d", got)
	}
	owner.must(400, "PATCH", g+"/click-actions/"+id, map[string]any{"label": "", "href_contains": ""})
	owner.must(404, "PATCH", "/v1/brands/"+rp+"/click-actions/"+id, map[string]any{"enabled": false})
	owner.must(404, "PATCH", g+"/click-actions/not-an-id", map[string]any{"enabled": false})

	// Deleted: gone from the list, its events stay.
	owner.must(404, "DELETE", "/v1/brands/"+rp+"/click-actions/"+id, nil)
	owner.must(204, "DELETE", g+"/click-actions/"+id, nil)
	if out := owner.must(200, "GET", g+"/click-actions", nil); len(out["data"].([]any)) != 0 {
		t.Fatalf("after deleting: %v", out)
	}
	if got := count(geo, "signup_clicked"); got != 3 {
		t.Fatalf("events after deleting: %d", got)
	}
}
