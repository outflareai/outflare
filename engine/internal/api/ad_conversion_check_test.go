package api_test

import (
	"context"
	"testing"
)

// Conversions sent against what the network counts: per action, by day,
// with a status that says when the network counts too few.
func TestAdConversionCheck(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	exec := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	brand := exec(`SELECT outflare_first_brand($1)::text`, ws)
	check := "/v1/brands/" + brand + "/ads/conversion-check"
	if got := owner.must(200, "GET", check, nil); len(got["data"].([]any)) != 0 {
		t.Fatalf("nothing sent: %v", got)
	}
	owner.must(400, "GET", check+"?days=3", nil)

	integ := exec(`INSERT INTO integrations (workspace_id, connector, name, credentials) VALUES ($1, 'google_ads', 'Google Ads', '\x00') RETURNING id::text`, ws)
	acct := exec(`INSERT INTO ad_accounts (workspace_id, brand_id, integration_id, connector, external_id, name, currency) VALUES ($1, $2, $3, 'google_ads', '123', 'Northpeak search', 'USD') RETURNING id::text`, ws, brand, integ)
	camp := exec(`INSERT INTO ad_entities (workspace_id, ad_account_id, external_id, name, status) VALUES ($1, $2, '7', 'Brand search', 'active') RETURNING id::text`, ws, acct)
	send := func(action string, n int, keys string) {
		t.Helper()
		rule := exec(`INSERT INTO ad_conversion_rules (workspace_id, ad_account_id, event_name, target_id, target_name) VALUES ($1, $2, 'signed_up', $3, $3) RETURNING id::text`, ws, acct, action)
		for i := range n {
			ev := exec(`INSERT INTO customer_events (workspace_id, kind, name, occurred_at) VALUES ($1, 'track', 'signed_up', now() - interval '4 days') RETURNING id::text`, ws)
			exec(`INSERT INTO ad_conversion_sends (workspace_id, rule_id, event_row_id, event_id, occurred_at, status, match_keys, sent_at)
				VALUES ($1, $2, $3, $4, now() - interval '4 days', 'sent', $5::text[], now() - interval '4 days') RETURNING id::text`, ws, rule, ev, action+"-"+string(rune('a'+i)), keys)
		}
	}
	count := func(action string, n float64) {
		t.Helper()
		exec(`INSERT INTO ad_conversion_actions_daily (ad_entity_id, day, action, workspace_id, brand_id, conversions) VALUES ($1, current_date - 4, $2, $3, $4, $5) RETURNING 'x'`, camp, action, ws, brand, n)
	}
	send("Purchase", 12, "{gclid,email}")
	count("Purchase", 9)
	send("Sign-up", 12, "{gclid}")
	count("Sign-up", 3)
	send("Trial", 12, "{gclid}")
	send("Lead", 12, "{email}")

	got := owner.must(200, "GET", check, nil)
	rows := map[string]map[string]any{}
	for _, x := range got["data"].([]any) {
		m := x.(map[string]any)
		rows[m["action"].(string)] = m
	}
	if len(rows) != 4 {
		t.Fatalf("rows: %v", got)
	}
	if p := rows["Purchase"]; p["status"] != "ok" || p["sent"] != 12.0 || p["sent_with_click_id"] != 12.0 || p["counted"] != 9.0 || p["share"] != 0.75 || p["account_name"] != "Northpeak search" {
		t.Fatalf("purchase: %v", p)
	}
	if d := rows["Purchase"]["days"].([]any); len(d) != 1 || d[0].(map[string]any)["sent"] != 12.0 || d[0].(map[string]any)["counted"] != 9.0 {
		t.Fatalf("purchase days: %v", d)
	}
	if s := rows["Sign-up"]; s["status"] != "low" || s["share"] != 0.25 {
		t.Fatalf("sign-up: %v", s)
	}
	if x := rows["Trial"]; x["status"] != "none" || x["counted"] != 0.0 || x["share"] != nil {
		t.Fatalf("trial: %v", x)
	}
	// Sends with no click id can't be counted: too little to judge.
	if x := rows["Lead"]; x["status"] != "too_few" || x["sent_with_click_id"] != 0.0 {
		t.Fatalf("lead: %v", x)
	}
}
