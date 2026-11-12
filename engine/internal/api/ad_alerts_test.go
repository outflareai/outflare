package api_test

import (
	"context"
	"testing"

	"github.com/outflareai/outflare/engine/internal/adsync"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Alerts from the alert job show in the suggestions list with the findings
// queue's fields and filters, a person acknowledges one, the workspace
// status carries an ads_alert blocker while one is open, and the
// platform's own changes are listed beside Outflare's.
func TestAdAlertsAndPlatformChanges(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	// Ads are the brand's: acknowledging and the platform's changes are
	// brand routes only.
	brandBase := "/v1/brands/" + brandOf(t, e, `SELECT outflare_first_brand($1)::text`, ws)
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
	// No alert yet: nothing to see.
	if s := owner.must(200, "GET", base+"/status", nil); hasBlocker(s, "ads_alert") {
		t.Fatalf("an alert blocker before any alert: %v", s["blockers"])
	}
	// The active campaign spent 3,000 today on a budget of 30 (the mirror
	// says 30,000,000 micros): an overspend, and no economics to let
	// Outflare pause it, so an alert.
	exec(`DELETE FROM ad_metrics_daily WHERE workspace_id = $1`, ws)
	exec(`INSERT INTO ad_metrics_daily (ad_entity_id, day, workspace_id, currency, spend_micros, impressions, clicks)
		SELECT $1, current_date, $2, 'USD', 3000000000, 100, 10`, active, ws)
	res, err := e.ads.RunAlerts(ctx, ws)
	if err != nil || res.Raised[adsync.OverspendPause] != 1 || res.Paused != 0 {
		t.Fatalf("alerts: %+v %v", res, err)
	}

	// The month's pacing is off too on any day past the 7th (one day's
	// spend against a month of budgets), so pick the alerts by kind.
	list := owner.must(200, "GET", base+"/ads/suggestions?autonomy=alert", nil)["data"].([]any)
	var al map[string]any
	for _, x := range list {
		if x := x.(map[string]any); x["kind"] == adsync.OverspendPause {
			al = x
		}
		if x := x.(map[string]any); x["autonomy"] != "alert" {
			t.Fatalf("not an alert: %v", x)
		}
	}
	if al == nil {
		t.Fatalf("alerts listed: %v", list)
	}
	if al["autonomy"] != "alert" || al["unit"] != "platform_conversions" || al["rule"] != adsync.OverspendPause || al["rule_version"] != 1.0 ||
		al["shadow"] != false || al["applied_auto"] != false || al["impact_micros"] != nil || al["reverted_at"] != nil || al["revert_change_id"] != nil || al["status"] != "open" {
		t.Fatalf("the alert: %v", al)
	}
	if ev := al["evidence"].(map[string]any); ev["multiple"] != 100.0 || ev["window"] != "today so far" {
		t.Fatalf("evidence: %v", ev)
	}
	if l := owner.must(200, "GET", base+"/ads/suggestions?kind="+adsync.OverspendPause, nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("by kind: %v", l)
	}
	if l := owner.must(200, "GET", base+"/ads/suggestions?kind=alert.flatline", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("another kind: %v", l)
	}
	if l := owner.must(200, "GET", base+"/ads/suggestions?autonomy=approve", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("approve-class: %v", l)
	}
	owner.must(400, "GET", base+"/ads/suggestions?autonomy=maybe", nil)
	owner.must(400, "GET", base+"/ads/suggestions?kind=Not%20A%20Kind", nil)

	// The status says so, critical: a campaign is spending twice its budget.
	s := owner.must(200, "GET", base+"/status", nil)
	b := statusBlockerOf(s, "ads_alert")
	if b == nil || b["severity"] != "critical" || b["area"] != "ads" || b["count"].(float64) < 1 || b["fix"].(map[string]any)["tool"] != "list_ad_suggestions" {
		t.Fatalf("ads_alert blocker: %v", s["blockers"])
	}

	// Only a person with ads.manage acknowledges; the reader can't.
	reader := &client{t: t, h: e.h, bearer: owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Reader", "scopes": []string{"analytics.read"}})["token"].(string)}
	reader.must(403, "POST", brandBase+"/ads/suggestions/"+al["id"].(string)+"/acknowledge", nil)
	ack := owner.must(200, "POST", brandBase+"/ads/suggestions/"+al["id"].(string)+"/acknowledge", nil)
	if ack["status"] != "acknowledged" || ack["decided_by"] == nil || ack["decided_at"] == nil {
		t.Fatalf("acknowledged: %v", ack)
	}
	owner.must(409, "POST", brandBase+"/ads/suggestions/"+al["id"].(string)+"/acknowledge", nil)
	if l := owner.must(200, "GET", base+"/ads/suggestions?status=acknowledged", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("acknowledged listed: %v", l)
	}
	// Without the overspend the rest (pacing) is at most a warning.
	if b := statusBlockerOf(owner.must(200, "GET", base+"/status", nil), "ads_alert"); b != nil && b["severity"] == "critical" {
		t.Fatalf("still critical after acknowledging: %v", b)
	}
	// Acknowledged for a day, the job doesn't raise it again while it holds.
	if res, err := e.ads.RunAlerts(ctx, ws); err != nil || len(res.Raised) != 0 {
		t.Fatalf("raised again: %+v %v", res, err)
	}
	// A suggestion that isn't an alert can't be acknowledged.
	var sugg string
	if err := e.pool.QueryRow(ctx, `INSERT INTO ad_suggestions (workspace_id, brand_id, ad_entity_id, kind, autonomy, reason) VALUES ($1, outflare_first_brand($1), $2, 'budget_down', 'approve', 'Cut it') RETURNING id`, ws, active).Scan(&sugg); err != nil {
		t.Fatal(err)
	}
	owner.must(409, "POST", brandBase+"/ads/suggestions/"+sugg+"/acknowledge", nil)
	if l := owner.must(200, "GET", base+"/ads/suggestions?status=open&autonomy=approve", nil)["data"].([]any); len(l) != 1 || l[0].(map[string]any)["kind"] != "budget_down" {
		t.Fatalf("the suggestion: %v", l)
	}

	// The platform's own changes, as the change-event loop stores them.
	var acct string
	if err := e.pool.QueryRow(ctx, `SELECT ad_account_id FROM ad_entities WHERE id = $1`, active).Scan(&acct); err != nil {
		t.Fatal(err)
	}
	var ext string
	if err := e.pool.QueryRow(ctx, `SELECT external_id FROM ad_entities WHERE id = $1`, active).Scan(&ext); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO ad_change_events (workspace_id, ad_account_id, external_id, changed_at, actor, source, resource_type, operation, campaign_external_id, summary, detail)
		VALUES ($1, $2, 'ev-1', now() - interval '2 hours', 'google', 'recommendation_auto_apply', 'ad_group_criterion', 'create', $3, 'created ad group criterion', '{"changed_fields": []}'),
		       ($1, $2, 'ev-2', now() - interval '1 hour', 'outflare', 'api', 'campaign', 'update', $3, 'updated campaign: status', '{"changed_fields": ["campaign.status"]}'),
		       ($1, $2, 'ev-3', now() - interval '30 minutes', 'a1b2c3', 'web', 'campaign_budget', 'update', '', 'updated campaign budget: amount_micros', '{}')`, ws, acct, ext)
	// The sync stores each with its account's brand.
	exec(`UPDATE ad_change_events c SET brand_id = a.brand_id FROM ad_accounts a WHERE a.id = c.ad_account_id AND c.workspace_id = $1`, ws)
	all := owner.must(200, "GET", brandBase+"/ads/platform-changes", nil)
	data := all["data"].([]any)
	if len(data) != 3 || all["next_cursor"] != nil {
		t.Fatalf("platform changes: %v", all)
	}
	first := data[0].(map[string]any)
	if first["external_id"] != "ev-3" || first["actor"] != "a1b2c3" || first["source"] != "web" || first["campaign_id"] != nil || first["account_id"] != acct || first["connector"] != "meta_ads" {
		t.Fatalf("newest first: %v", first)
	}
	if second := data[1].(map[string]any); second["campaign_id"] != active || second["summary"] != "updated campaign: status" {
		t.Fatalf("the campaign is matched: %v", second)
	}
	if l := owner.must(200, "GET", brandBase+"/ads/platform-changes?actor=google", nil)["data"].([]any); len(l) != 1 || l[0].(map[string]any)["source"] != "recommendation_auto_apply" {
		t.Fatalf("google's: %v", l)
	}
	if l := owner.must(200, "GET", brandBase+"/ads/platform-changes?actor=people", nil)["data"].([]any); len(l) != 1 || l[0].(map[string]any)["external_id"] != "ev-3" {
		t.Fatalf("people's: %v", l)
	}
	if l := owner.must(200, "GET", brandBase+"/ads/platform-changes?campaign_id="+active+"&source=api", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("by campaign and source: %v", l)
	}
	page := owner.must(200, "GET", brandBase+"/ads/platform-changes?limit=2", nil)
	if len(page["data"].([]any)) != 2 || page["next_cursor"] == nil {
		t.Fatalf("page one: %v", page)
	}
	if rest := owner.must(200, "GET", brandBase+"/ads/platform-changes?limit=2&cursor="+page["next_cursor"].(string), nil)["data"].([]any); len(rest) != 1 || rest[0].(map[string]any)["external_id"] != "ev-1" {
		t.Fatalf("page two: %v", rest)
	}
	owner.must(400, "GET", brandBase+"/ads/platform-changes?actor=robots", nil)
	owner.must(400, "GET", brandBase+"/ads/platform-changes?source=carrier_pigeon", nil)
	reader.must(200, "GET", brandBase+"/ads/platform-changes", nil)

	names := map[string]int{}
	for _, ev := range got() {
		names[ev.Name]++
	}
	for _, n := range []string{"ad_alert.raised", "ad_alerts.checked", "ad_suggestion.acknowledged"} {
		if names[n] == 0 {
			t.Fatalf("no %s event: %v", n, names)
		}
	}
}

func statusBlockerOf(status map[string]any, code string) map[string]any {
	for _, b := range status["blockers"].([]any) {
		if b := b.(map[string]any); b["code"] == code {
			return b
		}
	}
	return nil
}

func hasBlocker(status map[string]any, code string) bool { return statusBlockerOf(status, code) != nil }
