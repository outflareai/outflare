package api_test

import (
	"context"
	"testing"
)

// Keywords and the searches they matched carry Outflare's own count of
// who their clicks brought: sign-ups, how many paid, and what a new
// paying customer cost.
func TestAdKeywordQuality(t *testing.T) {
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
	integ := exec(`INSERT INTO integrations (workspace_id, connector, name, credentials) VALUES ($1, 'google_ads', 'Google Ads', '\x00') RETURNING id::text`, ws)
	acct := exec(`INSERT INTO ad_accounts (workspace_id, brand_id, integration_id, connector, external_id, name, currency) VALUES ($1, $2, $3, 'google_ads', '123', 'Northpeak search', 'USD') RETURNING id::text`, ws, brand, integ)
	camp := exec(`INSERT INTO ad_entities (workspace_id, ad_account_id, external_id, name, status) VALUES ($1, $2, '7', 'Search', 'active') RETURNING id::text`, ws, acct)
	group := exec(`INSERT INTO ad_entities (workspace_id, ad_account_id, external_id, name, status, level, parent_id) VALUES ($1, $2, '71', 'CRM', 'active', 'ad_group', $3) RETURNING id::text`, ws, acct, camp)
	keyword := func(ext, text, match string, spend int64) {
		t.Helper()
		id := exec(`INSERT INTO ad_keywords (workspace_id, ad_account_id, ad_group_id, external_id, text, match_type, status) VALUES ($1, $2, $3, $4, $5, $6, 'active') RETURNING id::text`, ws, acct, group, ext, text, match)
		exec(`INSERT INTO ad_keyword_metrics_daily (keyword_id, day, workspace_id, currency, spend_micros, clicks) VALUES ($1, current_date - 3, $2, 'USD', $3, 10) RETURNING 'x'`, id, ws, spend)
	}
	keyword("91", "acme crm", "exact", 90_000_000)
	keyword("92", "acme crm", "phrase", 40_000_000)
	keyword("93", "free crm", "broad", 30_000_000)
	exec(`INSERT INTO ad_search_terms_daily (workspace_id, ad_account_id, ad_group_id, day, term, keyword_text, keyword_match_type, currency, spend_micros, clicks, brand_id)
		VALUES ($1, $2, $3, current_date - 3, 'free crm download', 'free crm', 'broad', 'USD', 20000000, 8, $4) RETURNING 'x'`, ws, acct, group, brand)

	// Who each keyword's clicks brought. A click Google listed knows its
	// match type; one known only from the landing page's tags doesn't.
	n := 0
	person := func(kw, click, match, signed, paid string) {
		t.Helper()
		n++
		rec := exec(`INSERT INTO records (workspace_id, record_type, name) VALUES ($1, 'person', $2) RETURNING id::text`, ws, "Person "+string(rune('A'+n)))
		if click != "" {
			exec(`INSERT INTO ad_clicks (workspace_id, brand_id, ad_account_id, click_id, day, keyword, match_type) VALUES ($1, $2, $3, $4, current_date - 3, $5, $6) RETURNING 'x'`, ws, brand, acct, click, kw, match)
		}
		exec(`INSERT INTO ad_acquisitions (workspace_id, brand_id, record_id, connector, ad_account_id, campaign_entity_id, ad_group_entity_id, keyword, click_id, clicked_at, window_days, signed_up_at, first_paid_at)
			VALUES ($1, $2, $3, 'google_ads', $4, $5, $6, $7, $8, now() - interval '3 days', 30, now() - $9::interval, CASE WHEN $10 = '' THEN NULL ELSE now() - $10::interval END) RETURNING 'x'`,
			ws, brand, rec, acct, camp, group, kw, click, signed, paid)
	}
	person("Acme CRM", "g1", "exact", "3 days", "2 days")
	person("acme crm", "g2", "exact", "3 days", "")
	person("acme crm", "g3", "phrase", "3 days", "")
	person("acme crm", "", "", "60 days", "1 day") // signed up before the range, paid in it
	for _, c := range []string{"g4", "g5", "g6"} {
		person("free crm", c, "broad", "2 days", "")
	}

	base := "/v1/brands/" + brand
	rows := map[string]map[string]any{}
	for _, x := range owner.must(200, "GET", base+"/ads/keywords", nil)["data"].([]any) {
		m := x.(map[string]any)
		rows[m["text"].(string)+" "+m["match_type"].(string)] = m
	}
	if k := rows["acme crm exact"]; k["signups"] != 2.0 || k["signups_paid"] != 1.0 || k["paid_share"] != 0.5 || k["new_payers"] != 2.0 || k["cost_per_new_payer_micros"] != 45_000_000.0 {
		t.Fatalf("exact keyword: %v", k)
	}
	if k := rows["acme crm phrase"]; k["signups"] != 1.0 || k["signups_paid"] != 0.0 || k["paid_share"] != 0.0 || k["new_payers"] != 0.0 {
		t.Fatalf("phrase keyword: %v", k)
	}
	if k := rows["free crm broad"]; k["signups"] != 3.0 || k["paid_share"] != 0.0 || k["new_payers"] != 0.0 || k["cost_per_new_payer_micros"] != nil {
		t.Fatalf("broad keyword: %v", k)
	}
	st := owner.must(200, "GET", base+"/ads/search-terms", nil)["data"].([]any)
	if len(st) != 1 || st[0].(map[string]any)["keyword_signups"] != 3.0 || st[0].(map[string]any)["keyword_signups_paid"] != 0.0 || st[0].(map[string]any)["keyword_paid_share"] != 0.0 {
		t.Fatalf("search terms: %v", st)
	}
	// Outside the range nothing counts.
	old := owner.must(200, "GET", base+"/ads/keywords?from=2020-01-01&to=2020-01-31", nil)["data"].([]any)
	if len(old) != 0 {
		t.Fatalf("an old range: %v", old)
	}
}
