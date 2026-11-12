package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Google's Required Minimum Functionality: a website conversion action
// and its tag, account callouts, portfolio strategies and their report,
// all conversions, keyword bid estimates and search term match types, and
// removing a campaign, an ad or a keyword, which only a person's own
// session can do. Account changes are queued, made by the change job and
// read back; an agent's always waits for a person.
func TestAdRMF(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	googleSuffix.Lock()
	googleSuffix.mutates, googleSuffix.refuse = nil, false
	googleSuffix.Unlock()
	mutates := func() string {
		googleSuffix.Lock()
		defer googleSuffix.Unlock()
		return strings.Join(googleSuffix.mutates, "\n")
	}
	var integ string
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		i, err := e.integrations.Insert(ctx, tx, ws, "google_ads", connectors.AuthOAuth2, "Google Ads", connectors.Creds{"access_token": "gt"}, connectors.Account{}, "")
		integ = i.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ads.SyncOnce(ctx, ws, integ); err != nil {
		t.Fatal(err)
	}
	acct := owner.must(200, "GET", base+"/ads/accounts?connector=google_ads", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	brand := brandOf(t, e, `SELECT brand_id::text FROM ad_accounts WHERE id = $1`, acct)
	bb := "/v1/brands/" + brand
	ab := bb + "/ads/accounts/" + acct
	applyAccount := func(res map[string]any) map[string]any {
		t.Helper()
		id := res["change"].(map[string]any)["id"].(string)
		if err := e.ads.ApplyAccountChange(ctx, ws, id, false); err != nil {
			t.Fatal(err)
		}
		return owner.must(200, "GET", ab+"/changes/"+id, nil)
	}
	applyChange := func(res map[string]any) map[string]any {
		t.Helper()
		id := res["change"].(map[string]any)["id"].(string)
		if err := e.ads.ApplyChange(ctx, ws, id, false); err != nil {
			t.Fatal(err)
		}
		for _, c := range owner.must(200, "GET", bb+"/ads/changes", nil)["data"].([]any) {
			if c.(map[string]any)["id"] == id {
				return c.(map[string]any)
			}
		}
		t.Fatalf("change %s not listed", id)
		return nil
	}

	// R.10, R.20: all conversions beside conversions, per campaign and in
	// the summary and performance.
	from, to := time.Now().AddDate(0, 0, -6).Format(time.DateOnly), time.Now().AddDate(0, 0, 1).Format(time.DateOnly)
	rng := "?from=" + from + "&to=" + to
	camps := owner.must(200, "GET", bb+"/ads/campaigns"+rng, nil)["data"].([]any)
	if len(camps) != 1 {
		t.Fatalf("campaigns: %v", camps)
	}
	camp := camps[0].(map[string]any)
	if tt := camp["totals"].(map[string]any); tt["all_conversions"] == nil || tt["all_conversions"].(float64) <= tt["conversions"].(float64) || tt["all_conversion_value_micros"] == nil {
		t.Fatalf("campaign totals: %v", tt)
	}
	sum := owner.must(200, "GET", bb+"/ads/summary"+rng+"&group=account", nil)
	if row := sum["data"].([]any)[0].(map[string]any)["totals"].(map[string]any); row["all_conversions"] == nil {
		t.Fatalf("summary: %v", sum)
	}
	perf := owner.must(200, "GET", bb+"/ads/performance"+rng, nil)["data"].([]any)
	if len(perf) == 0 || perf[0].(map[string]any)["platform_all_conversions"] == nil {
		t.Fatalf("performance: %v", perf)
	}
	// R.50, R.70: bid estimates and quality score on keywords, the match
	// type on search terms.
	kws := owner.must(200, "GET", bb+"/ads/keywords"+rng, nil)["data"].([]any)
	kw := kws[0].(map[string]any)
	if kw["first_page_cpc_micros"] != 1_100_000.0 || kw["top_of_page_cpc_micros"] != 2_300_000.0 || kw["first_position_cpc_micros"] != 3_900_000.0 || kw["quality_score"] != 8.0 {
		t.Fatalf("keyword: %v", kw)
	}
	terms := owner.must(200, "GET", bb+"/ads/search-terms"+rng, nil)["data"].([]any)
	if terms[0].(map[string]any)["match_type"] != "near_exact" {
		t.Fatalf("search term: %v", terms)
	}

	// C.65: a website conversion action, made by the job, with its tag.
	list := owner.must(200, "GET", ab+"/conversion-actions", nil)
	if len(list["data"].([]any)) != 1 || len(list["categories"].([]any)) != 6 {
		t.Fatalf("conversion actions: %v", list)
	}
	owner.must(400, "POST", ab+"/conversion-actions", map[string]any{"name": "Sign-up", "category": "refund"})
	owner.must(400, "POST", ab+"/conversion-actions", map[string]any{"name": "Sign-up", "category": "sign_up", "click_window_days": 120})
	owner.must(400, "POST", ab+"/conversion-actions", map[string]any{"name": "", "category": "sign_up"})
	res := owner.must(202, "POST", ab+"/conversion-actions", map[string]any{"name": "Sign-up", "category": "sign_up", "counting": "one", "default_value_micros": 5_000_000})
	owner.must(409, "POST", ab+"/conversion-actions", map[string]any{"name": "Sign-up", "category": "sign_up", "counting": "one", "default_value_micros": 5_000_000})
	if res["change"].(map[string]any)["request"].(map[string]any)["conversion"].(map[string]any)["currency"] != "EUR" {
		t.Fatalf("the account's currency is the default: %v", res)
	}
	ch := applyAccount(res)
	if ch["status"] != "applied" || ch["verified_at"] == nil || ch["result"].(map[string]any)["conversion_action_id"] != "600" {
		t.Fatalf("conversion change: %v", ch)
	}
	if m := mutates(); !strings.Contains(m, `"type":"WEBPAGE"`) || !strings.Contains(m, `"category":"SIGNUP"`) || !strings.Contains(m, `"countingType":"ONE_PER_CLICK"`) {
		t.Fatalf("conversion mutate: %s", m)
	}
	var web map[string]any
	for _, x := range owner.must(200, "GET", ab+"/conversion-actions", nil)["data"].([]any) {
		if x.(map[string]any)["id"] == "600" {
			web = x.(map[string]any)
		}
	}
	if web == nil || len(web["snippets"].([]any)) != 1 || !strings.Contains(web["snippets"].([]any)[0].(map[string]any)["event_snippet"].(string), "conversion") {
		t.Fatalf("website action: %v", web)
	}

	// C.75: callouts.
	co := owner.must(200, "GET", ab+"/callouts", nil)
	if co["max_length"] != 25.0 || co["data"].([]any)[0].(map[string]any)["text"] != "Free trial" {
		t.Fatalf("callouts: %v", co)
	}
	owner.must(400, "POST", ab+"/callouts", map[string]any{"text": strings.Repeat("x", 26)})
	if ch := applyAccount(owner.must(202, "POST", ab+"/callouts", map[string]any{"text": "Setup in a day"})); ch["status"] != "applied" || ch["verified_at"] == nil || ch["result"].(map[string]any)["callout_id"] != "555" {
		t.Fatalf("callout added: %v", ch)
	}
	owner.must(404, "DELETE", ab+"/callouts/abc", nil)
	if ch := applyAccount(owner.must(202, "DELETE", ab+"/callouts/501", nil)); ch["status"] != "applied" || ch["verified_at"] == nil {
		t.Fatalf("callout removed: %v", ch)
	}
	if m := mutates(); !strings.Contains(m, `"fieldType":"CALLOUT"`) || !strings.Contains(m, `"remove":"customers/123/customerAssets/501~CALLOUT"`) {
		t.Fatalf("callout mutates: %s", m)
	}

	// C.96, C.97, M.96, M.97: portfolio strategies.
	ps := owner.must(200, "GET", ab+"/bidding-strategies", nil)["data"].([]any)
	if len(ps) != 1 || ps[0].(map[string]any)["type"] != "target_cpa" || ps[0].(map[string]any)["target_cpa_micros"] != 20_000_000.0 {
		t.Fatalf("strategies: %v", ps)
	}
	owner.must(400, "POST", ab+"/bidding-strategies", map[string]any{"name": "Value", "type": "target_roas"})
	owner.must(400, "POST", ab+"/bidding-strategies", map[string]any{"name": "Value", "type": "manual_cpc", "target_roas": 4})
	if ch := applyAccount(owner.must(202, "POST", ab+"/bidding-strategies", map[string]any{"name": "Value", "type": "target_roas", "target_roas": 4})); ch["status"] != "applied" ||
		ch["result"].(map[string]any)["bidding_strategy_id"] != "902" {
		t.Fatalf("strategy created: %v", ch)
	}
	owner.must(400, "PATCH", ab+"/bidding-strategies/900", map[string]any{})
	if ch := applyAccount(owner.must(202, "PATCH", ab+"/bidding-strategies/900", map[string]any{"target_cpa_micros": 18_000_000})); ch["status"] != "applied" || ch["verified_at"] == nil {
		t.Fatalf("strategy target: %v", ch)
	}
	if m := mutates(); !strings.Contains(m, `"targetRoas":{"targetRoas":4}`) || !strings.Contains(m, `"updateMask":"target_cpa.target_cpa_micros"`) {
		t.Fatalf("strategy mutates: %s", m)
	}
	// A campaign joins a portfolio strategy.
	owner.must(400, "POST", bb+"/ads/campaigns/"+camp["id"].(string)+"/bidding", map[string]any{"strategy": "portfolio", "bidding_strategy_id": "nine"})
	owner.must(400, "POST", bb+"/ads/campaigns/"+camp["id"].(string)+"/bidding", map[string]any{"strategy": "portfolio", "bidding_strategy_id": "900", "target_cpa_micros": 10_000_000})
	if c := applyChange(owner.must(202, "POST", bb+"/ads/campaigns/"+camp["id"].(string)+"/bidding", map[string]any{"strategy": "portfolio", "bidding_strategy_id": "900"})); c["status"] != "applied" ||
		c["after"].(map[string]any)["bidding"].(map[string]any)["bidding_strategy_id"] != "900" {
		t.Fatalf("attached: %v", c)
	}
	if !strings.Contains(mutates(), `"biddingStrategy":"customers/123/biddingStrategies/900"`) {
		t.Fatalf("attach mutate: %s", mutates())
	}
	// R.130: the report.
	rep := owner.must(200, "GET", bb+"/ads/bidding-strategies"+rng, nil)
	row := rep["data"].([]any)[0].(map[string]any)
	if row["type"] != "target_cpa" || row["clicks"] != 40.0 || row["cpc_micros"] != 2_000_000.0 || row["cost_per_conversion_micros"] != 20_000_000.0 || row["ad_account_id"] != acct ||
		len(rep["errors"].([]any)) != 0 {
		t.Fatalf("report: %v", rep)
	}

	// An agent's account change waits for a person; its removal is refused.
	agent, agentID := gatedAgent(t, owner, base, "Ads agent", "analytics.read", "ads.manage")
	owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "ads/**", "mode": "run"})
	for _, x := range []struct{ method, path string }{{"POST", ab + "/callouts"}, {"PATCH", ab + "/bidding-strategies/900"}, {"POST", ab + "/conversion-actions"}} {
		body := map[string]any{"text": "Agent callout", "target_cpa_micros": 30_000_000, "name": "Agent", "category": "lead"}
		if st, res := agent.do(x.method, x.path, body); st != 202 || res["approval_required"] != true || res["action"].(map[string]any)["decided_by_rule"] != "always_person" {
			t.Fatalf("agent %s %s: %d %v", x.method, x.path, st, res)
		}
	}
	if st, res := agent.do("POST", bb+"/ads/campaigns/"+camp["id"].(string)+"/remove", map[string]any{"confirm": true}); st != 403 || res["error"].(map[string]any)["code"] != "person_only" {
		t.Fatalf("agent removal: %d %v", st, res)
	}
	tok := &client{t: t, h: e.h, bearer: owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Script", "scopes": []string{"ads.manage", "analytics.read"}})["token"].(string)}
	if st, res := tok.do("POST", bb+"/ads/campaigns/"+camp["id"].(string)+"/remove", map[string]any{"confirm": true}); st != 403 || res["error"].(map[string]any)["code"] != "person_only" {
		t.Fatalf("token removal: %d %v", st, res)
	}

	// M.130, M.140: a person removes an ad and a keyword, M.110 the
	// campaign; each is read back and marked removed.
	var adID, kwID string
	if err := e.pool.QueryRow(ctx, `SELECT id FROM ad_entities WHERE workspace_id = $1 AND level = 'ad' AND external_id = '81'`, ws).Scan(&adID); err != nil {
		t.Fatal(err)
	}
	kwID = kw["id"].(string)
	owner.must(400, "POST", bb+"/ads/items/"+adID+"/remove", map[string]any{})
	owner.must(400, "POST", bb+"/ads/items/"+adID+"/remove", map[string]any{"confirm": false})
	res = owner.must(202, "POST", bb+"/ads/items/"+adID+"/remove", map[string]any{"confirm": true})
	owner.must(409, "POST", bb+"/ads/items/"+adID+"/remove", map[string]any{"confirm": true})
	if c := applyChange(res); c["kind"] != "ad_remove" || c["status"] != "applied" || c["verified_at"] == nil || c["after"].(map[string]any)["ad_id"] != "81" {
		t.Fatalf("ad removed: %v", c)
	}
	if c := applyChange(owner.must(202, "POST", bb+"/ads/keywords/"+kwID+"/remove", map[string]any{"confirm": true})); c["kind"] != "keyword_remove" || c["verified_at"] == nil {
		t.Fatalf("keyword removed: %v", c)
	}
	owner.must(409, "POST", bb+"/ads/keywords/"+kwID+"/remove", map[string]any{"confirm": true})
	if c := applyChange(owner.must(202, "POST", bb+"/ads/campaigns/"+camp["id"].(string)+"/remove", map[string]any{"confirm": true})); c["kind"] != "campaign_remove" || c["verified_at"] == nil {
		t.Fatalf("campaign removed: %v", c)
	}
	owner.must(409, "POST", bb+"/ads/campaigns/"+camp["id"].(string)+"/remove", map[string]any{"confirm": true})
	for _, want := range []string{`"remove":"customers/123/adGroupAds/71~81"`, `"remove":"customers/123/adGroupCriteria/71~91"`, `"remove":"customers/123/campaigns/7"`} {
		if !strings.Contains(mutates(), want) {
			t.Errorf("no %s in %s", want, mutates())
		}
	}
	var adStatus, kwStatus, campStatus string
	if err := e.pool.QueryRow(ctx, `SELECT (SELECT status FROM ad_entities WHERE id = $1), (SELECT status FROM ad_keywords WHERE id = $2), (SELECT status FROM ad_entities WHERE id = $3)`,
		adID, kwID, camp["id"]).Scan(&adStatus, &kwStatus, &campStatus); err != nil || adStatus != "removed" || kwStatus != "removed" || campStatus != "removed" {
		t.Fatalf("mirror: %s %s %s %v", adStatus, kwStatus, campStatus, err)
	}

	// Another platform answers connector_invalid.
	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	if _, err := e.ads.SyncOnce(ctx, ws, i["id"].(string)); err != nil {
		t.Fatal(err)
	}
	var meta string
	if err := e.pool.QueryRow(ctx, `UPDATE ad_accounts SET brand_id = $2 WHERE workspace_id = $1 AND connector = 'meta_ads' RETURNING id`, ws, brand).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	owner.must(422, "GET", bb+"/ads/accounts/"+meta+"/callouts", nil)
	owner.must(422, "POST", bb+"/ads/accounts/"+meta+"/bidding-strategies", map[string]any{"name": "x", "type": "target_cpa", "target_cpa_micros": 20_000_000})
	mcamp, _ := metaCampaigns(t, e, ws)
	owner.must(422, "POST", bb+"/ads/campaigns/"+mcamp+"/remove", map[string]any{"confirm": true})
	owner.must(404, "GET", bb+"/ads/accounts/"+acct+"/changes/"+acct, nil)
	if n := len(owner.must(200, "GET", ab+"/changes", nil)["data"].([]any)); n != 5 {
		t.Fatalf("account changes: %d", n)
	}

	evs := got()
	for name, want := range map[string]int{"ad_conversion_action.created": 1, "ad_callout.added": 1, "ad_callout.removed": 1, "ad_bidding_strategy.created": 1,
		"ad_bidding_strategy.updated": 1, "ad_ad.removed": 1, "ad_keyword.removed": 1, "ad_campaign.removed": 1} {
		if n := len(named(evs, name)); n != want {
			t.Errorf("%s: %d events, want %d", name, n, want)
		}
	}
	for _, ev := range named(evs, "ad_callout.added") {
		for k, v := range ev.Props {
			if s, ok := v.(string); ok && strings.Contains(s, "Setup in a day") {
				t.Errorf("the callout's text is in the event's %s", k)
			}
		}
	}
}
