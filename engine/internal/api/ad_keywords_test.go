package api_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/adsuggest"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Keywords on Google: a search that converts becomes a keyword, one that
// wastes money a negative, a keyword that carries its ad group gets its
// own; approving each makes the call on the workspace's own account. The
// account's own recommendations are read live.
func TestAdKeywordSuggestions(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	googleSuffix.Lock()
	googleSuffix.mutates, googleSuffix.refuse = nil, false
	googleSuffix.Unlock()
	var id string
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		i, err := e.integrations.Insert(ctx, tx, ws, "google_ads", connectors.AuthOAuth2, "Google Ads", connectors.Creds{"access_token": "gt"}, connectors.Account{}, "")
		id = i.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ads.SyncOnce(ctx, ws, id); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	var camp, group, acct string
	if err := e.pool.QueryRow(ctx, `SELECT c.id, g.id, c.ad_account_id FROM ad_entities c JOIN ad_entities g ON g.parent_id = c.id
		WHERE c.workspace_id = $1 AND c.external_id = '7' AND g.external_id = '71'`, ws).Scan(&camp, &group, &acct); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE ad_entities SET target_cost_per_conversion_micros = 10000000 WHERE id = $1`, camp)
	// The brand's rules out of shadow mode, so findings are offered.
	exec(`INSERT INTO ad_brand_economics (brand_id, workspace_id, shadow_mode) SELECT brand_id, workspace_id, false FROM ad_accounts WHERE id = $1 AND brand_id IS NOT NULL
		ON CONFLICT (brand_id) DO UPDATE SET shadow_mode = false`, acct)
	exec(`DELETE FROM ad_search_terms_daily WHERE workspace_id = $1`, ws)
	exec(`DELETE FROM ad_keyword_metrics_daily WHERE workspace_id = $1`, ws)
	term := func(text string, spend, clicks int64, conv float64) {
		t.Helper()
		exec(`INSERT INTO ad_search_terms_daily (workspace_id, ad_account_id, ad_group_id, day, term, currency, spend_micros, clicks, conversions, brand_id)
			VALUES ($1, $2, $3, current_date - 3, $4, 'EUR', $5, $6, $7, outflare_first_brand($1::uuid))`, ws, acct, group, text, spend, clicks, conv)
	}
	term("acme crm for agencies", 25_000_000, 60, 3) // 60 clicks, converts at 8.33 under the target of 10: a keyword
	term("free crm download", 60_000_000, 14, 0)     // 14 clicks, six targets, nothing: a negative
	term("acme crm", 30_000_000, 20, 5)              // already a keyword
	var kw string
	if err := e.pool.QueryRow(ctx, `SELECT id FROM ad_keywords WHERE ad_group_id = $1`, group).Scan(&kw); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO ad_keywords (workspace_id, ad_account_id, ad_group_id, external_id, text, match_type, status)
		VALUES ($1, $2, $3, '92', 'crm tool', 'phrase', 'active'), ($1, $2, $3, '93', 'sales crm', 'broad', 'active')`, ws, acct, group)
	exec(`INSERT INTO ad_keyword_metrics_daily (keyword_id, day, workspace_id, currency, spend_micros, conversions)
		VALUES ($1, current_date - 3, $2, 'EUR', 80000000, 12)`, kw, ws)

	run := &adsuggest.Runner{Pool: e.pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if _, err := run.Run(ctx, ws); err != nil {
		t.Fatal(err)
	}
	by := map[string]map[string]any{}
	for _, s := range owner.must(200, "GET", base+"/ads/suggestions?status=open&campaign_id="+camp, nil)["data"].([]any) {
		s := s.(map[string]any)
		by[s["kind"].(string)] = s
	}
	add, neg := by["keyword_add"], by["negative_add"]
	if add == nil || neg == nil ||
		add["proposed"].(map[string]any)["keyword"] != "acme crm for agencies" || add["proposed"].(map[string]any)["ad_group_id"] != "71" ||
		neg["proposed"].(map[string]any)["keyword"] != "free crm download" || add["unit"] != "platform_conversions" || neg["rule"] != "negative_keyword" {
		t.Fatalf("keyword suggestions: %v", by)
	}
	// Single-keyword ad groups are obsolete: no split is suggested any more.
	if by["keyword_split"] != nil {
		t.Fatalf("a keyword split: %v", by["keyword_split"])
	}

	for _, s := range []map[string]any{add, neg} {
		ok := owner.must(202, "POST", base+"/ads/suggestions/"+s["id"].(string)+"/approve", nil)
		if ok["status"] != "measuring" {
			t.Fatalf("approve %s: %v", s["kind"], ok)
		}
		if err := e.ads.ApplyChange(ctx, ws, ok["change_id"].(string), false); err != nil {
			t.Fatal(err)
		}
	}
	changes := owner.must(200, "GET", base+"/ads/changes?campaign_id="+camp+"&status=applied", nil)["data"].([]any)
	if len(changes) != 2 {
		t.Fatalf("changes: %v", changes)
	}
	googleSuffix.Lock()
	calls := strings.Join(googleSuffix.mutates, "\n")
	googleSuffix.Unlock()
	for _, want := range []string{
		`adGroupCriteria:mutate {"operations":[{"create":{"adGroup":"customers/123/adGroups/71","keyword":{"matchType":"EXACT","text":"acme crm for agencies"},"status":"ENABLED"}}]}`,
		`campaignCriteria:mutate {"operations":[{"create":{"campaign":"customers/123/campaigns/7","keyword":{"matchType":"EXACT","text":"free crm download"},"negative":true}}]}`,
	} {
		if !strings.Contains(calls, want) {
			t.Fatalf("no call %s in:\n%s", want, calls)
		}
	}

	rec := owner.must(200, "GET", base+"/ads/recommendations?account_id="+acct, nil)
	if l := rec["data"].([]any); rec["supported"] != true || len(l) != 1 || l[0].(map[string]any)["type"] != "KEYWORD" || l[0].(map[string]any)["campaign_id"] != "7" {
		t.Fatalf("recommendations: %v", rec)
	}
}
