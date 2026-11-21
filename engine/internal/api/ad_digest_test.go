package api_test

import (
	"context"
	"strings"
	"testing"
)

// The digest's Paid ads section: spend, what the ads brought and what a
// new paying customer cost, with the campaign that spent the most.
func TestDigestPaidAds(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := brandOf(t, e, `SELECT outflare_first_brand($1)::text`, ws)
	metaState.Lock()
	metaState.campaigns, metaState.posts, metaState.refuse = nil, nil, false
	metaState.Unlock()
	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	if _, err := e.ads.SyncOnce(ctx, ws, i["id"].(string)); err != nil {
		t.Fatal(err)
	}
	active, _ := metaCampaigns(t, e, ws)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`DELETE FROM ad_metrics_daily WHERE workspace_id = $1`, ws)
	exec(`INSERT INTO ad_metrics_daily (ad_entity_id, day, workspace_id, currency, spend_micros, impressions, clicks)
		VALUES ($1, current_date - 2, $2, 'USD', 300000000, 9000, 120), ($1, current_date - 9, $2, 'USD', 100000000, 3000, 40)`, active, ws)
	for n, paid := range []bool{true, true, false} {
		rec := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Person"})["id"].(string)
		exec(`INSERT INTO ad_acquisitions (workspace_id, brand_id, record_id, connector, campaign_entity_id, clicked_at, window_days, signed_up_at, first_paid_at)
			VALUES ($1, $2, $3, 'meta_ads', $4, now() - interval '3 days', 30, now() - interval '2 days', CASE WHEN $5 THEN now() - interval '1 day' END)`,
			ws, brand, rec, active, paid)
		_ = n
	}

	p := owner.must(200, "GET", "/v1/brands/"+brand+"/digests/preview", nil)
	var sec map[string]any
	for _, s := range p["sections"].([]any) {
		if s := s.(map[string]any); s["id"] == "paid_ads" {
			sec = s
		}
	}
	if sec == nil {
		t.Fatalf("no paid ads section: %v", p["sections"])
	}
	got := map[string][2]float64{}
	for _, m := range sec["metrics"].([]any) {
		m := m.(map[string]any)
		got[m["key"].(string)] = [2]float64{m["value"].(float64), m["previous"].(float64)}
	}
	want := map[string][2]float64{"ad_spend_usd": {300, 100}, "ad_clicks": {120, 40}, "ad_signups": {3, 0}, "ad_new_payers": {2, 0}, "ad_cost_per_new_payer_usd": {150, 0}}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	items := sec["items"].([]any)
	if len(items) == 0 || !strings.Contains(items[0].(map[string]any)["text"].(string), "spent 300.00 USD and brought 2 new paying customers, 150.00 USD each.") ||
		items[0].(map[string]any)["tab"] != "ads" {
		t.Errorf("items: %v", items)
	}
}
