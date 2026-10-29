package api_test

import (
	"context"
	"testing"
	"time"
)

func TestAdTrackingHealthReport(t *testing.T) {
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

	// Nothing yet: an empty, healthy report.
	h := owner.must(200, "GET", base+"/ads/tracking-health", nil)
	if h["days"] != float64(7) || len(h["networks"].([]any)) != 0 || len(h["issues"].([]any)) != 0 {
		t.Fatalf("empty = %v", h)
	}
	owner.must(400, "GET", base+"/ads/tracking-health?days=0", nil)

	// Meta: 30 paid visits, 10 with fbclid, 5 with only the template's ad
	// ids, 15 with neither (a pixel cookie isn't a click id). Google: 25,
	// all with gclid. Organic visits don't count.
	at := time.Now().Add(-time.Hour)
	visit := func(n int, source, medium, ids, ad string) {
		t.Helper()
		for range n {
			exec(`INSERT INTO site_sessions (workspace_id, anonymous_id, started_at, utm_source, utm_medium, click_ids, ad)
				VALUES ($1, gen_random_uuid()::text, $2, $3, $4, $5::jsonb, $6::jsonb) RETURNING id::text`, ws, at, source, medium, ids, ad)
		}
	}
	visit(10, "facebook", "paid_social", `{"fbclid": "fb-1"}`, `{}`)
	visit(5, "facebook", "paid_social", `{}`, `{"campaign": "111"}`)
	visit(15, "facebook", "paid_social", `{"fbp": "fb.1.2.3"}`, `{}`)
	visit(25, "google", "cpc", `{"gclid": "g-1"}`, `{}`)
	visit(40, "google", "organic", `{}`, `{}`)

	// Two synced Meta campaigns spent: 111 has visits, 999 has none.
	integ := exec(`INSERT INTO integrations (workspace_id, connector, name, credentials) VALUES ($1, 'meta_ads', 'Meta Ads', '\x00') RETURNING id::text`, ws)
	acct := exec(`INSERT INTO ad_accounts (workspace_id, integration_id, connector, external_id, name, currency) VALUES ($1, $2, 'meta_ads', '4455', 'Northpeak', 'USD') RETURNING id::text`, ws, integ)
	for _, c := range []string{"111", "999"} {
		ent := exec(`INSERT INTO ad_entities (workspace_id, ad_account_id, external_id, name, status) VALUES ($1, $2, $3, $4, 'active') RETURNING id::text`, ws, acct, c, "Campaign "+c)
		exec(`INSERT INTO ad_metrics_daily (ad_entity_id, day, workspace_id, currency, spend_micros) VALUES ($1, current_date, $2, 'USD', 7000000) RETURNING 'x'`, ent, ws)
	}

	// Meta refused 3 of 5 conversions sent back.
	rule := exec(`INSERT INTO ad_conversion_rules (workspace_id, ad_account_id, event_name, target_id) VALUES ($1, $2, 'order_completed', '777') RETURNING id::text`, ws, acct)
	for i, status := range []string{"sent", "sent", "failed", "failed", "failed", "skipped"} {
		ev := exec(`INSERT INTO customer_events (workspace_id, kind, name, occurred_at) VALUES ($1, 'track', 'order_completed', now()) RETURNING id::text`, ws)
		reason := ""
		if status == "failed" {
			reason = "the pixel isn't assigned to the system user"
		}
		exec(`INSERT INTO ad_conversion_sends (workspace_id, rule_id, event_row_id, event_id, occurred_at, status, reason) VALUES ($1, $2, $3, $4, now(), $5, $6) RETURNING id::text`,
			ws, rule, ev, "msg-"+string(rune('a'+i)), status, reason)
	}

	h = owner.must(200, "GET", base+"/ads/tracking-health", nil)
	nets := map[string]map[string]any{}
	for _, x := range h["networks"].([]any) {
		m := x.(map[string]any)
		nets[m["network"].(string)] = m
	}
	meta, google := nets["meta"], nets["google"]
	if len(nets) != 2 || meta == nil || google == nil {
		t.Fatalf("networks = %v", h["networks"])
	}
	if meta["visits"] != float64(30) || meta["click_id"] != float64(10) || meta["ad_ids_only"] != float64(5) || meta["untracked"] != float64(15) ||
		google["visits"] != float64(25) || google["click_id"] != float64(25) || google["untracked"] != float64(0) {
		t.Fatalf("meta %v google %v", meta, google)
	}
	camps := h["campaigns_without_visits"].([]any)
	if len(camps) != 1 || camps[0].(map[string]any)["external_id"] != "999" || camps[0].(map[string]any)["spend"].([]any)[0].(map[string]any)["amount_cents"] != float64(700) {
		t.Fatalf("campaigns = %v", camps)
	}
	sends := h["sends"].([]any)
	if len(sends) != 1 || sends[0].(map[string]any)["failed"] != float64(3) || sends[0].(map[string]any)["sent"] != float64(2) ||
		sends[0].(map[string]any)["top_failure"] != "the pixel isn't assigned to the system user" {
		t.Fatalf("sends = %v", sends)
	}
	// Failing sends first, then Meta's untracked clicks and its campaign;
	// Google is healthy.
	codes := []string{}
	for _, x := range h["issues"].([]any) {
		codes = append(codes, x.(map[string]any)["code"].(string)+":"+x.(map[string]any)["subject"].(string))
	}
	if len(codes) != 3 || codes[0] != "sends_failing:meta_ads" || codes[1] != "untracked_clicks:meta" || codes[2] != "campaigns_without_visits:meta_ads" {
		t.Fatalf("issues = %v", codes)
	}
}
