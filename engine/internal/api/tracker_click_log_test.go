package api_test

import (
	"context"
	"net/url"
	"testing"
	"time"
)

// The tracker click logs (plans/ad-tracker-ui.md B6): the brand's redirect
// and lander clicks, newest first, without likely bots unless asked, with
// filters and a cursor.
func TestTrackerClickLog(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Push US", "channel": "paid_ads", "source": "acme", "redirect_links": true})
	l := owner.must(201, "POST", base+"/tracking-campaigns/"+c["id"].(string)+"/links", map[string]any{"url": "https://offers.northpeak.example/o1"})
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	// Clicks are written by the redirect handler in batches; the test writes
	// them as it would.
	for i, k := range []struct {
		key, country, device, fields string
		bot                          bool
		fraud                        []string
		cost                         int64
	}{
		{"aaaaaaaaaaaaaaa1", "US", "mobile", `{"1":"zone7"}`, false, nil, 250000},
		{"aaaaaaaaaaaaaaa2", "US", "desktop", `{}`, true, nil, 0},
		{"aaaaaaaaaaaaaaa3", "DE", "desktop", `{}`, false, []string{"datacenter_network"}, 0},
	} {
		if k.fraud == nil {
			k.fraud = []string{}
		}
		if _, err := e.pool.Exec(ctx, `INSERT INTO tracking_link_clicks (workspace_id, brand_id, link_id, click_key, likely_bot, clicked_at, country, device,
				fields, cost_micros, source_click_id, fraud_reasons)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, 'net-'||$4, $11)`,
			ws, brand, l["id"], k.key, k.bot, at.Add(time.Duration(i)*time.Minute), k.country, k.device, k.fields, k.cost, k.fraud); err != nil {
			t.Fatal(err)
		}
	}
	keys := func(r map[string]any) []string {
		var out []string
		for _, row := range r["data"].([]any) {
			out = append(out, row.(map[string]any)["click_key"].(string))
		}
		return out
	}
	same := func(name string, got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: %v, want %v", name, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s: %v, want %v", name, got, want)
			}
		}
	}

	// Newest first, without the bot; a click's details.
	r := owner.must(200, "GET", base+"/tracker-clicks", nil)
	same("default", keys(r), "aaaaaaaaaaaaaaa3", "aaaaaaaaaaaaaaa1")
	first := r["data"].([]any)[1].(map[string]any)
	if first["campaign_id"] != c["id"] || first["link_id"] != l["id"] || first["country"] != "US" || first["device"] != "mobile" ||
		first["fields"].(map[string]any)["1"] != "zone7" || first["cost"] != "0.25" || first["source_click_id"] != "net-aaaaaaaaaaaaaaa1" {
		t.Fatalf("click: %v", first)
	}
	if fr := r["data"].([]any)[0].(map[string]any)["fraud_reasons"].([]any); len(fr) != 1 || fr[0] != "datacenter_network" {
		t.Fatalf("fraud reasons: %v", fr)
	}
	// Filters.
	same("bots", keys(owner.must(200, "GET", base+"/tracker-clicks?bots=true", nil)), "aaaaaaaaaaaaaaa3", "aaaaaaaaaaaaaaa2", "aaaaaaaaaaaaaaa1")
	same("click id", keys(owner.must(200, "GET", base+"/tracker-clicks?click_id=AAAAAAAAAAAAAAA1", nil)), "aaaaaaaaaaaaaaa1")
	same("campaign", keys(owner.must(200, "GET", base+"/tracker-clicks?campaign="+c["id"].(string), nil)), "aaaaaaaaaaaaaaa3", "aaaaaaaaaaaaaaa1")
	same("other campaign", keys(owner.must(200, "GET", base+"/tracker-clicks?campaign=00000000-0000-0000-0000-000000000000", nil)))
	same("since", keys(owner.must(200, "GET", base+"/tracker-clicks?since="+url.QueryEscape(at.Add(90*time.Second).Format(time.RFC3339)), nil)), "aaaaaaaaaaaaaaa3")
	// The cursor pages on.
	page := owner.must(200, "GET", base+"/tracker-clicks?limit=1", nil)
	same("page 1", keys(page), "aaaaaaaaaaaaaaa3")
	cursor, _ := page["next_cursor"].(string)
	if cursor == "" {
		t.Fatalf("no cursor: %v", page)
	}
	same("page 2", keys(owner.must(200, "GET", base+"/tracker-clicks?limit=1&cursor="+url.QueryEscape(cursor), nil)), "aaaaaaaaaaaaaaa1")
	for _, bad := range []string{"campaign=nope", "bots=maybe", "since=yesterday", "click_id=" + "x123456789012345678901234567890123"} {
		owner.must(400, "GET", base+"/tracker-clicks?"+bad, nil)
	}

	// Lander clicks.
	for i, bot := range []bool{false, true} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO tracker_lander_clicks (workspace_id, brand_id, click_id, click_key, cta, likely_bot, clicked_at)
			VALUES ($1, $2, 1, 'aaaaaaaaaaaaaaa1', $3, $4, $5)`, ws, brand, i+1, bot, at.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	lc := owner.must(200, "GET", base+"/tracker-lander-clicks", nil)["data"].([]any)
	if len(lc) != 1 || lc[0].(map[string]any)["cta"] != float64(1) || lc[0].(map[string]any)["click_key"] != "aaaaaaaaaaaaaaa1" {
		t.Fatalf("lander clicks: %v", lc)
	}
	if n := len(owner.must(200, "GET", base+"/tracker-lander-clicks?bots=true", nil)["data"].([]any)); n != 2 {
		t.Fatalf("lander clicks with bots: %d", n)
	}
	owner.must(400, "GET", base+"/tracker-lander-clicks?campaign="+c["id"].(string), nil)
}
