package api_test

import (
	"context"
	"strings"
	"testing"
)

// Paid ads in Query: ad accounts, campaigns and their daily numbers, for
// callers with analytics.read, one brand at a time, without secret columns.
func TestQueryAds(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	exec := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	np := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	acme := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "handle": "acme"})["id"].(string)
	integ := exec(`INSERT INTO integrations (workspace_id, connector, name, credentials) VALUES ($1, 'google_ads', 'Google Ads', '\x00') RETURNING id::text`, ws)
	campaign := func(brand, ext, name string, spend int64) {
		t.Helper()
		acct := exec(`INSERT INTO ad_accounts (workspace_id, brand_id, integration_id, connector, external_id, name, currency) VALUES ($1, $2, $3, 'google_ads', $4, $5, 'USD') RETURNING id::text`, ws, brand, integ, ext, name+" account")
		camp := exec(`INSERT INTO ad_entities (workspace_id, ad_account_id, external_id, name, status) VALUES ($1, $2, $3, $4, 'active') RETURNING id::text`, ws, acct, ext+"-c", name)
		exec(`INSERT INTO ad_metrics_daily (ad_entity_id, day, workspace_id, currency, spend_micros, impressions, clicks) VALUES ($1, current_date - 1, $2, 'USD', $3, 1000, 40) RETURNING 'x'`, camp, ws, spend)
	}
	campaign(np, "111", "Brand search", 25_000_000)
	campaign(acme, "222", "Acme search", 9_000_000)

	out := owner.must(200, "POST", "/v1/brands/"+np+"/query", map[string]any{
		"sql": "select e.name, e.currency, m.level, m.spend_micros from ad_metrics_daily m join ad_entities e on e.id = m.ad_entity_id",
	})
	if rows := queryRows(t, out); len(rows) != 1 || rows[0][0] != "Brand search" || rows[0][1] != "USD" || rows[0][2] != "campaign" || rows[0][3] != 25e6 {
		t.Fatalf("northpeak ads: %v", out)
	}
	out = owner.must(200, "POST", "/v1/brands/"+acme+"/query", map[string]any{"sql": "select name from ad_accounts"})
	if rows := queryRows(t, out); len(rows) != 1 || rows[0][0] != "Acme search account" {
		t.Fatalf("acme accounts: %v", out)
	}
	// Spend points at the ad behind it.
	owner.must(200, "POST", "/v1/brands/"+np+"/query", map[string]any{"sql": "select ad_entity_id from spend limit 1"})

	// Secret and machinery columns stay off the views.
	for _, sql := range []string{
		"select meta from ad_accounts",
		"select match_keys from ad_conversion_sends",
		"select postback_url from tracker_traffic_sources",
		"select ip_hash from tracking_link_clicks",
	} {
		code, got := owner.do("POST", "/v1/brands/"+np+"/query", map[string]any{"sql": sql})
		if code != 400 || queryCode(got) != "query_invalid" || !strings.Contains(strings.ToLower(got["error"].(map[string]any)["message"].(string)), "does not exist") {
			t.Fatalf("%s: %d %v", sql, code, got)
		}
	}

	// The catalog explains micros.
	one := owner.must(200, "GET", "/v1/brands/"+np+"/data/catalog/ad_metrics_daily", nil)
	if one["table"].(map[string]any)["area"] != "ads" || one["table"].(map[string]any)["rows"] != 1.0 {
		t.Fatalf("catalog table: %v", one["table"])
	}
	for _, x := range one["columns"].([]any) {
		if m := x.(map[string]any); m["name"] == "spend_micros" && !strings.Contains(m["unit"].(string), "1,000,000") {
			t.Fatalf("spend_micros: %v", m)
		}
	}

	// Without analytics.read the ads tables aren't readable.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "CRM agent", "scopes": []string{"customers.read"}, "brand": "northpeak"})
	agent := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	if code, _ := agent.do("POST", "/v1/brands/"+np+"/query", map[string]any{"sql": "select count(*) from ad_metrics_daily"}); code == 200 {
		t.Fatalf("ad_metrics_daily read without analytics.read")
	}
}
