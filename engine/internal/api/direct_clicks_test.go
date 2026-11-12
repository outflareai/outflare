package api_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// Direct mode (plans/ad-tracker.md, T8): an ad straight to the brand's page
// with a tracker campaign's ofc gets its click from the visit: the site
// script names the click id and the new visit records the click with the
// traffic source's values from the page's URL. A postback can then name it.
func TestDirectClicks(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	host := ownTrackingDomain(t, e, ws, brand, "go.northpeak.example")

	ts := owner.must(201, "POST", base+"/traffic-sources", map[string]any{"name": "Acme Native",
		"click_id":   map[string]any{"param": "clickid", "token": "{clickid}"},
		"cost":       map[string]any{"param": "cpc", "token": "{cpc}"},
		"parameters": []any{map[string]any{"name": "Zone", "param": "zone_id", "token": "{zone}", "field": 1}}})
	tracker := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Native push", "channel": "paid_ads", "source": "acme",
		"traffic_source": ts["id"]})
	code := tracker["code"].(string)
	owner.must(201, "POST", base+"/tracking-campaigns/"+code+"/links", map[string]any{"url": "https://northpeak.example/pricing"})
	winter := owner.must(201, "POST", base+"/tracking-campaigns/"+code+"/links", map[string]any{"url": "https://northpeak.example/winter"})
	plain := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Newsletter", "channel": "lifecycle_email", "source": "newsletter"})
	sk := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "Northpeak", "origins": []string{"https://northpeak.example"}})

	n := 0
	visit := func(anon, ofc, key string, direct bool) {
		t.Helper()
		n++
		props := map[string]any{"ofc": ofc}
		if key != "" {
			props["ofclk"], props["direct"] = key, direct
		}
		page := "https://northpeak.example/winter?ofc=" + ofc + "&clickid=net-9&zone_id=Z1&cpc=0.05"
		ev := map[string]any{"type": "page", "anonymousId": anon, "messageId": "direct-" + string(rune('a'+n)), "properties": props,
			"context": map[string]any{"page": map[string]any{"url": page, "path": "/winter", "search": "?ofc=" + ofc + "&clickid=net-9&zone_id=Z1&cpc=0.05"},
				"campaign": map[string]any{"source": "acme", "medium": "paid", "name": tracker["slug"]}}}
		if status, out := sendSite(t, e.h, "https://northpeak.example", sk["key"].(string), ev); status != 200 {
			t.Fatalf("site event = %d %v", status, out)
		}
	}
	clicks := func() int {
		t.Helper()
		var c int
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM tracking_link_clicks WHERE workspace_id = $1`, ws).Scan(&c); err != nil {
			t.Fatal(err)
		}
		return c
	}

	// The visit records the click, on the link for its page, with the
	// source's click id, cost and field, claimed by the visit itself.
	const key = "d1rect0click0key"
	visit("ana", code, key, true)
	var link, sourceClickID, field, anon string
	var direct bool
	var cost int64
	if err := e.pool.QueryRow(ctx, `SELECT k.link_id::text, k.source_click_id, k.fields->>'1', k.cost_micros, k.direct, s.anonymous_id
		FROM tracking_link_clicks k JOIN site_sessions s ON s.id = k.session_id WHERE k.click_key = $1 AND k.landed_at IS NOT NULL`, key).
		Scan(&link, &sourceClickID, &field, &cost, &direct, &anon); err != nil {
		t.Fatal(err)
	}
	if link != winter["id"] || sourceClickID != "net-9" || field != "Z1" || cost != 50_000 || !direct || anon != "ana" {
		t.Fatalf("click: link %s, source click %q, field %q, cost %d, direct %v, visitor %q", link, sourceClickID, field, cost, direct, anon)
	}

	// A reload in the same visit isn't a second click; another visitor
	// can't take the same id; a plain UTM campaign and a key the script
	// didn't name record nothing.
	visit("ana", code, key, true)
	visit("ben", code, key, true)
	visit("cat", plain["code"].(string), "plain0campaign00", true)
	visit("dan", code, "redirect0key0000", false)
	if c := clicks(); c != 1 {
		t.Fatalf("clicks = %d, want 1", c)
	}

	// The network's postback names the click.
	src := owner.must(201, "POST", base+"/postback-sources", map[string]any{"name": "Acme Network"})
	token := strings.TrimPrefix(strings.SplitN(src["postback_url"].(string), "?", 2)[0], "https://"+host)
	req := httptest.NewRequest("GET", token+"?cid="+key+"&payout=4&txid=D-1", nil)
	req.Host = host
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("postback: %d %s", rec.Code, rec.Body)
	}
	conv := owner.must(200, "GET", base+"/tracker-conversions", nil)["data"].([]any)
	if len(conv) != 1 || conv[0].(map[string]any)["click_key"] != key || conv[0].(map[string]any)["link_id"] != winter["id"] {
		t.Fatalf("conversions: %v", conv)
	}
}
