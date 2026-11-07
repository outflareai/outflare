package api_test

import (
	"testing"
)

// Page speed from real visits: the tag's readings are kept with no visitor,
// don't count as events, and the report gives each metric's 75th
// percentile, per page, with pages that got slower than the week before.
func TestPageSpeed(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	brand := brandOf(t, e, `SELECT outflare_first_brand($1)::text`, ws)
	bbase := "/v1/brands/" + brand

	empty := owner.must(200, "GET", bbase+"/analytics/page-speed", nil)
	if empty["loads"] != 0.0 || empty["metrics"].(map[string]any)["lcp"].(map[string]any)["p75"] != nil || len(empty["pages"].([]any)) != 0 {
		t.Fatalf("empty: %v", empty)
	}

	key := owner.must(201, "POST", "/v1/workspaces/"+ws+"/site-keys", map[string]any{"name": "Site", "origins": []string{"acme.example"}})["key"].(string)
	vitals := func(path string, props map[string]any) {
		t.Helper()
		code, out := sendSite(t, e.h, "https://acme.example", key, map[string]any{"type": "vitals", "messageId": "m-" + path,
			"properties": props, "context": map[string]any{"page": map[string]any{"url": "https://acme.example" + path + "?email=ana@acme.example", "path": path}}})
		if code != 200 || out["accepted"] != 1.0 {
			t.Fatalf("vitals: %d %v", code, out)
		}
		e.pump(t)
	}
	vitals("/pricing", map[string]any{"lcp": 3400.4, "inp": 240, "cls": 0.02, "fcp": 1200, "ttfb": 300})
	vitals("/pricing", map[string]any{"lcp": 1800, "cls": 0.3})
	vitals("/", map[string]any{"lcp": 1500, "inp": 80})

	// No visitor id, no query string, and not counted as events.
	var anyQuery, events int
	if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE path LIKE '%?%' OR path LIKE '%email%') FROM page_vitals WHERE workspace_id = $1`, ws).Scan(&anyQuery); err != nil || anyQuery != 0 {
		t.Fatalf("paths kept a query: %d %v", anyQuery, err)
	}
	if err := e.pool.QueryRow(t.Context(), `SELECT coalesce(sum(events), 0) FROM site_key_days WHERE workspace_id = $1`, ws).Scan(&events); err != nil || events != 0 {
		t.Fatalf("vitals counted as events: %d %v", events, err)
	}

	// A broken reading is refused; a mixed batch keeps the good ones.
	code, out := sendSite(t, e.h, "https://acme.example", key,
		map[string]any{"type": "vitals", "properties": map[string]any{"lcp": -5}, "context": map[string]any{"page": map[string]any{"url": "https://acme.example/"}}},
		map[string]any{"type": "vitals", "properties": map[string]any{"lcp": 999999}, "context": map[string]any{"page": map[string]any{"url": "https://acme.example/"}}})
	if code != 200 || out["accepted"] != 0.0 || out["rejected"] != 2.0 {
		t.Fatalf("bad vitals: %d %v", code, out)
	}

	got := owner.must(200, "GET", bbase+"/analytics/page-speed", nil)
	if got["loads"] != 3.0 {
		t.Fatalf("loads: %v", got)
	}
	lcp := got["metrics"].(map[string]any)["lcp"].(map[string]any)
	if lcp["measured"] != 3.0 || lcp["good"] != 2.0 || lcp["needs_improvement"] != 1.0 || lcp["poor"] != 0.0 || lcp["grade"] == "" {
		t.Fatalf("lcp: %v", lcp)
	}
	// Fast: every measured metric good. /pricing's first (slow LCP) and
	// second (jumpy) are not; / is.
	if fs := got["fast_share"].(float64); fs < 0.33 || fs > 0.34 {
		t.Fatalf("fast share: %v", got["fast_share"])
	}
	pages := got["pages"].([]any)
	if len(pages) != 2 || pages[0].(map[string]any)["path"] != "/pricing" || pages[0].(map[string]any)["website"] != "acme.example" || pages[0].(map[string]any)["loads"] != 2.0 {
		t.Fatalf("pages: %v", pages)
	}
	if ws := got["websites"].([]any); len(ws) != 1 || ws[0] != "acme.example" {
		t.Fatalf("websites: %v", ws)
	}

	// A page that got slower than the week before, with enough loads in
	// both, is named; one with few loads is not.
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO page_vitals (workspace_id, brand_id, host, path, device, lcp_ms, occurred_at)
		SELECT $1::uuid, $2::uuid, 'acme.example', '/blog', 'desktop', 1500, now() - interval '10 days' FROM generate_series(1, 40)
		UNION ALL SELECT $1, $2, 'acme.example', '/blog', 'mobile', 3600, now() - interval '1 day' FROM generate_series(1, 40)
		UNION ALL SELECT $1, $2, 'acme.example', '/rare', 'desktop', 1000, now() - interval '10 days' FROM generate_series(1, 5)
		UNION ALL SELECT $1, $2, 'acme.example', '/rare', 'desktop', 5000, now() - interval '1 day' FROM generate_series(1, 5)`, ws, brand); err != nil {
		t.Fatal(err)
	}
	got = owner.must(200, "GET", bbase+"/analytics/page-speed", nil)
	slower := got["slower"].([]any)
	if len(slower) != 1 {
		t.Fatalf("slower: %v", slower)
	}
	if s := slower[0].(map[string]any); s["path"] != "/blog" || s["metric"] != "lcp" || s["before"] != 1500.0 || s["after"] != 3600.0 {
		t.Fatalf("slower: %v", s)
	}
	for _, p := range got["pages"].([]any) {
		if p := p.(map[string]any); p["path"] == "/blog" && p["previous_lcp"] != 1500.0 {
			t.Fatalf("blog previous: %v", p)
		}
	}
	// One kind of device.
	mobile := owner.must(200, "GET", bbase+"/analytics/page-speed?device=mobile", nil)
	if mobile["loads"] != 40.0 {
		t.Fatalf("mobile: %v", mobile["loads"])
	}
	owner.must(400, "GET", bbase+"/analytics/page-speed?device=watch", nil)
	owner.must(400, "GET", bbase+"/analytics/page-speed?from=2026-01-01&to=2026-06-01", nil)

	// Readings over 90 days old are deleted.
	if _, err := e.pool.Exec(t.Context(), `UPDATE page_vitals SET occurred_at = now() - interval '91 days' WHERE path = '/rare'`); err != nil {
		t.Fatal(err)
	}
	if n, err := e.srv.PrunePageVitals(t.Context()); err != nil || n != 10 {
		t.Fatalf("prune: %d %v", n, err)
	}
}

// Right now: the visitors on the site in the last minutes, the pages they
// are on, where they came from, and a feed of what they do.
func TestSiteLive(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	brand := brandOf(t, e, `SELECT outflare_first_brand($1)::text`, ws)
	bbase := "/v1/brands/" + brand

	empty := owner.must(200, "GET", bbase+"/analytics/live", nil)
	if empty["now"] != 0.0 || len(empty["feed"].([]any)) != 0 || len(empty["per_minute"].([]any)) != 30 {
		t.Fatalf("empty: %v", empty)
	}

	key := owner.must(201, "POST", "/v1/workspaces/"+ws+"/site-keys", map[string]any{"name": "Site", "origins": []string{"acme.example"}})["key"].(string)
	send := func(m map[string]any) {
		t.Helper()
		code, out := sendSite(t, e.h, "https://acme.example", key, m)
		if code != 200 || out["accepted"] != 1.0 {
			t.Fatalf("send: %d %v", code, out)
		}
		e.pump(t)
	}
	page := func(anon, path string) {
		send(map[string]any{"type": "page", "anonymousId": anon, "context": map[string]any{"page": map[string]any{"path": path, "url": "https://acme.example" + path}}})
	}
	page("lv-1", "/")
	page("lv-1", "/pricing")
	page("lv-2", "/pricing")
	page("lv-3", "/blog")
	send(map[string]any{"type": "track", "event": "Signup Started", "anonymousId": "lv-2", "context": map[string]any{"page": map[string]any{"path": "/pricing", "url": "https://acme.example/pricing"}}})
	// Someone who left an hour ago is not on the site now.
	if _, err := e.pool.Exec(t.Context(), `UPDATE site_sessions SET last_seen_at = now() - interval '1 hour' WHERE anonymous_id = 'lv-3'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(t.Context(), `UPDATE customer_events SET occurred_at = now() - interval '1 hour' WHERE anonymous_id = 'lv-3'`); err != nil {
		t.Fatal(err)
	}

	got := owner.must(200, "GET", bbase+"/analytics/live", nil)
	if got["now"] != 2.0 || got["last_30_minutes"] != 2.0 {
		t.Fatalf("now: %v %v", got["now"], got["last_30_minutes"])
	}
	pages := got["pages"].([]any)
	if len(pages) != 1 || pages[0].(map[string]any)["path"] != "/pricing" || pages[0].(map[string]any)["visitors"] != 2.0 || pages[0].(map[string]any)["website"] != "acme.example" {
		t.Fatalf("pages: %v", pages)
	}
	if s := got["sources"].([]any); len(s) != 1 || s[0].(map[string]any)["key"] != "(direct)" || s[0].(map[string]any)["visitors"] != 2.0 {
		t.Fatalf("sources: %v", s)
	}
	if ev := got["events"].([]any); len(ev) != 1 || ev[0].(map[string]any)["name"] != "signup_started" || ev[0].(map[string]any)["count"] != 1.0 {
		t.Fatalf("events: %v", ev)
	}
	feed := got["feed"].([]any)
	if len(feed) != 4 {
		t.Fatalf("feed: %v", feed)
	}
	if f := feed[0].(map[string]any); f["kind"] != "track" || f["name"] != "signup_started" || f["path"] != "/pricing" || f["source"] != "(direct)" {
		t.Fatalf("newest: %v", f)
	}
	var minutes float64
	for _, m := range got["per_minute"].([]any) {
		minutes += m.(map[string]any)["visitors"].(float64)
	}
	if minutes < 2 {
		t.Fatalf("per minute: %v", got["per_minute"])
	}
}
