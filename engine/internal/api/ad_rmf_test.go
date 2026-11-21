package api_test

import (
	"context"
	"math"
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
	if days := owner.must(200, "GET", bb+"/ads/performance"+rng, nil)["daily"].([]any); len(days) == 0 || days[0].(map[string]any)["spend_micros"] == nil {
		t.Fatalf("performance per day: %v", days)
	}
	perf := owner.must(200, "GET", bb+"/ads/performance"+rng, nil)["data"].([]any)
	if len(perf) == 0 || perf[0].(map[string]any)["platform_all_conversions"] == nil {
		t.Fatalf("performance: %v", perf)
	}
	// An account billed in another currency is shown in the brand's: spend
	// and costs are converted at the latest rate, the budget stays in the
	// account's own.
	if _, err := e.pool.Exec(ctx, `INSERT INTO exchange_rates (currency, day, usd_per_unit) VALUES ('XTS', current_date, 0.5)
		ON CONFLICT (currency, day) DO UPDATE SET usd_per_unit = 0.5`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.pool.Exec(context.Background(), `DELETE FROM exchange_rates WHERE currency = 'XTS'`) })
	var billed string
	if err := e.pool.QueryRow(ctx, `SELECT currency FROM ad_accounts WHERE id = $1`, acct).Scan(&billed); err != nil {
		t.Fatal(err)
	}
	spent := perf[0].(map[string]any)["spend_micros"].(float64)
	if _, err := e.pool.Exec(ctx, `UPDATE ad_accounts SET currency = 'XTS' WHERE id = $1`, acct); err != nil {
		t.Fatal(err)
	}
	conv := owner.must(200, "GET", bb+"/ads/performance"+rng, nil)
	if row := conv["data"].([]any)[0].(map[string]any); conv["currency"] != "USD" || row["currency"] != "USD" || row["account_currency"] != "XTS" || row["spend_micros"] != math.Round(spent/2) {
		t.Fatalf("performance in the brand's currency: %v, was %v", row, spent)
	}
	for _, d := range conv["daily"].([]any) {
		if d.(map[string]any)["currency"] != "USD" {
			t.Fatalf("a day in the brand's currency: %v", d)
		}
	}
	if row := owner.must(200, "GET", bb+"/ads/summary"+rng+"&group=connector", nil)["data"].([]any)[0].(map[string]any); row["currency"] != "USD" || row["totals"].(map[string]any)["spend_micros"] != math.Round(spent/2) {
		t.Fatalf("summary in the brand's currency: %v", row)
	}
	if c := owner.must(200, "GET", bb+"/ads/campaigns"+rng, nil)["data"].([]any)[0].(map[string]any); c["currency"] != "XTS" || c["report_currency"] != "USD" || c["exchange_rate"] != 0.5 ||
		c["daily_budget_micros"] != camp["daily_budget_micros"] || c["totals"].(map[string]any)["spend_micros"] != math.Round(spent/2) {
		t.Fatalf("campaign in the brand's currency: %v", c)
	}
	for _, path := range []string{"/ads/items", "/ads/keywords", "/ads/search-terms"} {
		rows := owner.must(200, "GET", bb+path+rng, nil)["data"].([]any)
		if len(rows) == 0 {
			t.Fatalf("%s: no rows", path)
		}
		if x := rows[0].(map[string]any); x["currency"] != "XTS" || x["report_currency"] != "USD" || x["exchange_rate"] != 0.5 {
			t.Fatalf("%s in the brand's currency: %v", path, x)
		}
	}
	if _, err := e.pool.Exec(ctx, `UPDATE ad_accounts SET currency = $2 WHERE id = $1`, acct, billed); err != nil {
		t.Fatal(err)
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
	// Its money is in the brand's currency, like every other table's.
	fx := row["exchange_rate"].(float64)
	if row["type"] != "target_cpa" || row["clicks"] != 40.0 || row["cpc_micros"] != math.Round(2_000_000*fx) || row["cost_per_conversion_micros"] != math.Round(20_000_000*fx) || row["ad_account_id"] != acct ||
		row["report_currency"] == "" || (fx == 1) != (row["report_currency"] == row["currency"]) ||
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
	// Removing is a permission of its own. A broad run grant (ads/**)
	// never covers a removal: the agent is told which permission it lacks.
	refused := func(st int, res map[string]any, perm string) {
		t.Helper()
		if e, _ := res["error"].(map[string]any); st != 403 || e == nil || e["code"] != "permission_required" || e["permission"] != perm {
			t.Fatalf("want permission_required %s: %d %v", perm, st, res)
		}
	}
	st, res := agent.do("POST", bb+"/ads/campaigns/"+camp["id"].(string)+"/remove", map[string]any{"confirm": true})
	refused(st, res, "ads.remove_campaign")
	// An API token needs the scope ads.remove.
	tok := &client{t: t, h: e.h, bearer: owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Script", "scopes": []string{"ads.manage", "analytics.read"}})["token"].(string)}
	st, res = tok.do("POST", bb+"/ads/campaigns/"+camp["id"].(string)+"/remove", map[string]any{"confirm": true})
	refused(st, res, "ads.remove")
	// So does a person's role: one that manages ads but may not remove.
	role := owner.must(201, "POST", base+"/roles", map[string]any{"name": "Ads, no removing", "scopes": []string{"analytics.read", "ads.manage"}})
	member, _ := joinAs(t, owner, ws, "mo@acme.example", map[string]any{"role": role["id"]})
	st, res = member.do("POST", bb+"/ads/campaigns/"+camp["id"].(string)+"/remove", map[string]any{"confirm": true})
	refused(st, res, "ads.remove")
	// Off: a refuse grant names the permission too.
	off := owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "method": "POST", "path": "brands/*/ads/campaigns/*/remove", "mode": "refuse"})
	if off["permission"] != "ads.remove_campaign" {
		t.Fatalf("grant: %v", off)
	}
	st, res = agent.do("POST", bb+"/ads/campaigns/"+camp["id"].(string)+"/remove", map[string]any{"confirm": true})
	refused(st, res, "ads.remove_campaign")
	// On, kept to paused campaigns: the campaign is active, so it is refused.
	owner.must(400, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "ads/**", "mode": "run", "conditions": map[string]any{"paused_campaign_only": true}})
	owner.must(400, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "brands/*/ads/keywords/*/remove", "mode": "run", "conditions": map[string]any{"older_than": 3}})
	owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "method": "POST", "path": "brands/*/ads/keywords/*/remove", "mode": "run", "conditions": map[string]any{"paused_campaign_only": true}})
	st, res = agent.do("POST", bb+"/ads/keywords/"+kw["id"].(string)+"/remove", map[string]any{"confirm": true})
	refused(st, res, "ads.remove_keyword")
	if !strings.Contains(res["error"].(map[string]any)["message"].(string), "paused campaign") {
		t.Fatalf("why: %v", res)
	}

	// Ask: the agent's removal of an ad waits for a person, who approves
	// it; it is queued and recorded with what was removed and the action.
	owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "method": "POST", "path": "brands/*/ads/items/*/remove", "mode": "hold"})
	var adID, kwID string
	if err := e.pool.QueryRow(ctx, `SELECT id FROM ad_entities WHERE workspace_id = $1 AND level = 'ad' AND external_id = '81'`, ws).Scan(&adID); err != nil {
		t.Fatal(err)
	}
	st, res = agent.do("POST", bb+"/ads/items/"+adID+"/remove", map[string]any{"confirm": true})
	if st != 202 || res["approval_required"] != true {
		t.Fatalf("agent removal held: %d %v", st, res)
	}
	act := owner.must(200, "POST", bb+"/actions/"+res["action"].(map[string]any)["id"].(string)+"/approve", nil)
	if act["status"] != "executed" || act["result_status"] != 202.0 {
		t.Fatalf("approved removal: %v", act)
	}
	var snap map[string]any
	var actionID string
	if err := e.pool.QueryRow(ctx, `SELECT data->'snapshot', data->>'action_id' FROM audit_events WHERE workspace_id = $1 AND action = 'ad_ad.removed'`, ws).Scan(&snap, &actionID); err != nil {
		t.Fatal(err)
	}
	if actionID != act["id"] || snap["name"] == "" || snap["campaign_id"] != camp["id"] || snap["ad_group_name"] == nil || snap["spend_30d_micros"] == nil {
		t.Fatalf("snapshot: %s %v", actionID, snap)
	}
	var changeID string
	if err := e.pool.QueryRow(ctx, `SELECT id FROM ad_changes WHERE workspace_id = $1 AND kind = 'ad_remove'`, ws).Scan(&changeID); err != nil {
		t.Fatal(err)
	}

	// M.130, M.140: a person removes an ad and a keyword, M.110 the
	// campaign; each is read back and marked removed. The ad's removal is
	// the one the person approved above.
	kwID = kw["id"].(string)
	owner.must(400, "POST", bb+"/ads/keywords/"+kwID+"/remove", map[string]any{})
	owner.must(400, "POST", bb+"/ads/keywords/"+kwID+"/remove", map[string]any{"confirm": false})
	owner.must(409, "POST", bb+"/ads/items/"+adID+"/remove", map[string]any{"confirm": true})
	if c := applyChange(map[string]any{"change": map[string]any{"id": changeID}}); c["kind"] != "ad_remove" || c["status"] != "applied" || c["verified_at"] == nil || c["after"].(map[string]any)["ad_id"] != "81" {
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
