package api_test

import (
	"context"
	"testing"
	"time"
)

// A tracker campaign's cost model (plans/ad-tracker.md, B6): the tracker
// report's cost per click (cpc), per conversion (cpa), as a share of the
// payouts (revshare) or as the traffic source reported it (token).
func TestCampaignCostModels(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE brands SET currency = 'USD' WHERE id = $1`, brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Push US", "channel": "paid_ads", "source": "push", "redirect_links": true,
		"cost": map[string]any{"model": "cpc", "amount": "0.05"}})
	cost := func(v map[string]any) map[string]any { return v["cost"].(map[string]any) }
	if got := cost(c); got["model"] != "cpc" || got["amount"] != "0.05" || got["currency"] != "" {
		t.Fatalf("cost: %v", got)
	}
	id := c["id"].(string)
	l := owner.must(201, "POST", base+"/tracking-campaigns/"+id+"/links", map[string]any{"url": "https://offers.northpeak.example/o1"})
	src := owner.must(201, "POST", base+"/postback-sources", map[string]any{"name": "Network"})
	at := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	var clicks []int64
	for i, bot := range []bool{false, false, false, true} {
		var k int64
		if err := e.pool.QueryRow(ctx, `INSERT INTO tracking_link_clicks (workspace_id, brand_id, link_id, click_key, likely_bot, clicked_at, cost_micros)
			VALUES ($1, $2, $3, $4, $5, $6, 30000) RETURNING id`, ws, brand, l["id"], "bbbbbbbbbbbbbbb"+string(rune('1'+i)), bot, at).Scan(&k); err != nil {
			t.Fatal(err)
		}
		clicks = append(clicks, k)
	}
	for i, x := range []struct {
		status string
		payout int64
	}{{"approved", 2_500000}, {"pending", 1_000000}, {"declined", 9_000000}} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO tracker_conversions (workspace_id, brand_id, source_id, click_id, click_key, campaign_id, link_id, clicked_at,
				transaction_id, status, payout_micros, currency, converted_at)
			VALUES ($1, $2, $3, $4, 'k', $5, $6, $7::timestamptz, $8, $9, $10, 'USD', $7::timestamptz)`,
			ws, brand, src["id"], clicks[i], id, l["id"], at, "tx-"+x.status, x.status, x.payout); err != nil {
			t.Fatal(err)
		}
	}
	report := func() map[string]any {
		t.Helper()
		return owner.must(200, "GET", base+"/drilldown?source=tracker&from=2026-10-01&to=2026-10-10&timezone=UTC", nil)["totals"].(map[string]any)
	}
	set := func(c map[string]any) {
		t.Helper()
		owner.must(200, "PATCH", base+"/tracking-campaigns/"+id, map[string]any{"cost": c})
	}
	// cpc: three clicks (the bot left out) at 0.05.
	if got := report()["cost"]; got != 0.15 {
		t.Fatalf("cpc cost: %v", got)
	}
	// cpa: two conversions (the declined one left out) at 2.
	set(map[string]any{"model": "cpa", "amount": 2})
	if got := report(); got["cost"] != float64(4) || got["profit"] != -0.5 {
		t.Fatalf("cpa: %v", got)
	}
	// revshare: half of 3.5 in payouts; the amount stays for later.
	set(map[string]any{"model": "revshare", "share": 50})
	if got := report()["cost"]; got != 1.75 {
		t.Fatalf("revshare cost: %v", got)
	}
	if got := cost(owner.must(200, "GET", base+"/tracking-campaigns/"+id, nil)); got["model"] != "revshare" || got["share"] != float64(50) || got["amount"] != "2" {
		t.Fatalf("stored cost: %v", got)
	}
	// token: what the source reported on each click, 0.03 each.
	set(map[string]any{"model": "token"})
	if got := report()["cost"]; got != 0.09 {
		t.Fatalf("token cost: %v", got)
	}
	// Bad models are refused.
	for _, bad := range []map[string]any{
		{"model": "cpm"},
		{"model": "cpc", "amount": 0},
		{"model": "revshare", "share": 0},
		{"share": 150},
		{"amount": "lots"},
		{"currency": "dollars"},
	} {
		owner.must(400, "PATCH", base+"/tracking-campaigns/"+id, map[string]any{"cost": bad})
	}
	owner.must(400, "POST", base+"/tracking-campaigns", map[string]any{"name": "Bad", "channel": "paid_ads", "source": "push", "cost": map[string]any{"model": "cpa"}})
}
