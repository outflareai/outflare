package api_test

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// Every browser on the site has a profile with its journey, anonymous or
// not; a person's timeline says when their browser became them; a visitor
// link joins a second device; and the site key says whether its events
// come through the site's own domain.
func TestVisitorBrowsersAndLinks(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := brandOf(t, e, `SELECT outflare_first_brand($1)::text`, ws)
	bbase := "/v1/brands/" + brand

	key := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "Site", "origins": []string{"https://acme.example"}})["key"].(string)
	page := func(anon, path string, props map[string]any) {
		t.Helper()
		if props == nil {
			props = map[string]any{}
		}
		code, out := sendSite(t, e.h, "https://acme.example", key, map[string]any{"type": "page", "anonymousId": anon, "properties": props,
			"context": map[string]any{"page": map[string]any{"path": path, "url": "https://acme.example" + path, "title": "Acme " + path}}})
		if code != 200 || out["accepted"] != 1.0 {
			t.Fatalf("page: %d %v", code, out)
		}
		e.pump(t)
	}

	// An anonymous visitor reads three pages and clicks a button.
	page("b-anon-1", "/", nil)
	page("b-anon-1", "/pricing", nil)
	page("b-anon-1", "/docs", nil)
	code, out := sendSite(t, e.h, "https://acme.example", key, map[string]any{"type": "track", "event": "Demo Clicked", "anonymousId": "b-anon-1",
		"context": map[string]any{"page": map[string]any{"path": "/pricing"}}})
	if code != 200 || out["accepted"] != 1.0 {
		t.Fatalf("track: %d %v", code, out)
	}
	e.pump(t)

	list := owner.must(200, "GET", bbase+"/visitors/browsers?known=false", nil)["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["anonymous_id"] != "b-anon-1" || list[0].(map[string]any)["record_id"] != nil ||
		list[0].(map[string]any)["pageviews"] != 3.0 {
		t.Fatalf("anonymous list: %v", list)
	}
	prof := owner.must(200, "GET", bbase+"/visitors/browsers/b-anon-1", nil)
	visits := prof["visits"].([]any)
	if len(visits) != 1 || visits[0].(map[string]any)["landing_path"] != "/" || visits[0].(map[string]any)["known"] != false {
		t.Fatalf("visits: %v", prof)
	}
	events := owner.must(200, "GET", bbase+"/visitors/browsers/b-anon-1/events?limit=2", nil)
	if evs := events["data"].([]any); len(evs) != 2 || events["next_cursor"] == nil {
		t.Fatalf("events page 1: %v", events)
	}
	all := owner.must(200, "GET", bbase+"/visitors/browsers/b-anon-1/events?session="+visits[0].(map[string]any)["id"].(string), nil)["data"].([]any)
	if len(all) != 4 || all[len(all)-1].(map[string]any)["path"] != "/" {
		t.Fatalf("events of the visit: %v", all)
	}
	owner.must(404, "GET", bbase+"/visitors/browsers/b-nobody", nil)

	// They sign up: the server identifies them with the browser's id. The
	// browser is theirs now, and their timeline says so.
	owner.must(200, "POST", base+"/customer-events", map[string]any{"type": "identify", "userId": "u-ana", "anonymousId": "b-anon-1",
		"traits": map[string]any{"email": "ana@northpeak.example", "name": "Ana"}})
	prof = owner.must(200, "GET", bbase+"/visitors/browsers/b-anon-1", nil)
	b := prof["browser"].(map[string]any)
	rec, _ := b["record_id"].(string)
	if rec == "" || b["identified_by"] != "product" || b["identified_at"] == nil || b["name"] != "Ana" {
		t.Fatalf("known browser: %v", b)
	}
	// The sign-up itself is in the journey, newest first.
	if after := owner.must(200, "GET", bbase+"/visitors/browsers/b-anon-1/events", nil)["data"].([]any); len(after) != 5 || after[0].(map[string]any)["kind"] != "identify" {
		t.Fatalf("events after identify: %v", after)
	}
	tl := owner.must(200, "GET", bbase+"/records/"+rec+"/timeline?kinds=identified", nil)["data"].([]any)
	if len(tl) != 1 {
		t.Fatalf("timeline: %v", tl)
	}
	d := tl[0].(map[string]any)["detail"].(map[string]any)
	if d["by"] != "product" || d["anonymous_id"] != "b-anon-1" || d["visits_before"] != 1.0 || d["events_before"] != 4.0 || d["first_seen_at"] == nil {
		t.Fatalf("identified item: %v", tl[0])
	}

	// A visitor link for Ana opens on her phone: the phone joins her, its
	// earlier visit included. A second browser with the same link doesn't.
	owner.must(400, "POST", bbase+"/visitors/links", map[string]any{"url": "https://elsewhere.example/x", "user_id": "u-ana"})
	owner.must(400, "POST", bbase+"/visitors/links", map[string]any{"url": "https://acme.example/x"})
	owner.must(404, "POST", bbase+"/visitors/links", map[string]any{"url": "https://acme.example/x", "user_id": "u-nobody"})
	link := owner.must(201, "POST", bbase+"/visitors/links", map[string]any{"url": "https://acme.example/welcome?plan=pro", "user_id": "u-ana"})
	if link["record_id"] != rec {
		t.Fatalf("link: %v", link)
	}
	u, err := url.Parse(link["url"].(string))
	if err != nil || u.Host != "acme.example" || u.Query().Get("plan") != "pro" || !strings.HasPrefix(u.Query().Get("ofvisit"), "v_") {
		t.Fatalf("link url: %v", link["url"])
	}
	token := u.Query().Get("ofvisit")
	page("b-phone", "/blog", nil)
	page("b-phone", "/welcome", map[string]any{"ofvisit": token})
	page("b-laptop-2", "/welcome", map[string]any{"ofvisit": token})
	// A forged link token names nobody.
	page("b-forged", "/welcome", map[string]any{"ofvisit": "v_1.AAAAAAAAAAAAAAAAAAAAAA"})
	phone := owner.must(200, "GET", bbase+"/visitors/browsers/b-phone", nil)["browser"].(map[string]any)
	if phone["record_id"] != rec || phone["identified_by"] != "link" {
		t.Fatalf("phone: %v", phone)
	}
	for _, other := range []string{"b-laptop-2", "b-forged"} {
		if o := owner.must(200, "GET", bbase+"/visitors/browsers/"+other, nil)["browser"].(map[string]any); o["record_id"] != nil {
			t.Fatalf("%s joined: %v", other, o)
		}
	}
	tl = owner.must(200, "GET", bbase+"/records/"+rec+"/timeline?kinds=identified", nil)["data"].([]any)
	if len(tl) != 2 || tl[0].(map[string]any)["detail"].(map[string]any)["by"] != "link" ||
		tl[0].(map[string]any)["detail"].(map[string]any)["visits_before"] != 1.0 {
		t.Fatalf("timeline after link: %v", tl)
	}
	var stored int
	if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM customer_events WHERE properties ? 'ofvisit'`).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("token stored in events: %d %v", stored, err)
	}

	// Someone else signs in on the phone: they take it back, and Ana's
	// timeline shows the browser released.
	owner.must(200, "POST", base+"/customer-events", map[string]any{"type": "identify", "userId": "u-ben", "anonymousId": "b-phone",
		"traits": map[string]any{"email": "ben@northpeak.example"}})
	if p := owner.must(200, "GET", bbase+"/visitors/browsers/b-phone", nil)["browser"].(map[string]any); p["record_id"] == rec {
		t.Fatalf("phone stayed Ana's: %v", p)
	}
	tl = owner.must(200, "GET", bbase+"/records/"+rec+"/timeline?kinds=identified", nil)["data"].([]any)
	if len(tl) != 3 || tl[0].(map[string]any)["detail"].(map[string]any)["change"] != "released" {
		t.Fatalf("timeline after release: %v", tl)
	}

	if made := named(got(), "visitor_link.created"); len(made) != 1 || made[0].Outcome != track.Success {
		t.Fatalf("visitor_link.created: %+v", made)
	}

	// Own domain: events through a forwarded path say same-origin; Safari
	// straight to Outflare is counted as at risk.
	sendVia := func(ua, fetchSite string) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"key": key, "batch": []map[string]any{{"type": "page", "anonymousId": "b-own", "context": map[string]any{"page": map[string]any{"path": "/"}}}}})
		req := httptest.NewRequest("POST", "/v1/site/events", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
		req.Header.Set("Origin", "https://acme.example")
		req.Header.Set("User-Agent", ua)
		if fetchSite != "" {
			req.Header.Set("Sec-Fetch-Site", fetchSite)
		}
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("send: %d %s", rec.Code, rec.Body)
		}
	}
	sendVia(iphoneUA, "cross-site")
	sendVia(iphoneUA, "same-origin")
	sendVia(desktopUA, "same-origin")
	keys := owner.must(200, "GET", base+"/site-keys", nil)["data"].([]any)
	own := keys[0].(map[string]any)["own_domain"].(map[string]any)
	if own["through_own_domain"] != 2.0 || own["safari_direct"] != 1.0 || own["status"] != "partly" ||
		!strings.Contains(own["snippet"].(string), `src="/ofx/site.js"`) {
		t.Fatalf("own domain: %v", own)
	}
}
