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
	"github.com/outflareai/outflare/engine/internal/track"
)

// The findings queue by brand: the rules write a finding in payers, the
// queue lists and filters it, the status says a decision waits, a person
// approves it (an agent only a saving an extension's hook allows), the change is
// made and can be reverted, the levers this layer added apply through
// their own job, alerts are acknowledged, and a refresh within the hour
// is a no-op that says when the next may be.
func TestAdFindingsQueue(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	googleSuffix.Lock()
	googleSuffix.mutates, googleSuffix.refuse = nil, false
	googleSuffix.Unlock()
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
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	var camp, acct, brand string
	if err := e.pool.QueryRow(ctx, `SELECT e.id, a.id, outflare_first_brand($1)::text FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id WHERE e.workspace_id = $1 AND e.external_id = '7' AND e.level = 'campaign'`, ws).Scan(&camp, &acct, &brand); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE ad_accounts SET brand_id = $2 WHERE id = $1`, acct, brand)
	exec(`UPDATE ad_entities SET brand_id = $2, daily_budget_micros = 30000000, bidding_strategy = 'maximize_conversions', platform_target_cpa_micros = 10000000 WHERE ad_account_id = $1`, acct, brand)
	base := "/v1/brands/" + brand + "/ads"
	owner.must(200, "PUT", base+"/economics", map[string]any{"target_cost_per_payer_micros": 10_000_000, "shadow_mode": false})

	// 30 days at 30 a day with no new paying customer: three targets'
	// worth and more, so a pause (and a cut).
	exec(`DELETE FROM ad_metrics_daily WHERE workspace_id = $1`, ws)
	exec(`INSERT INTO ad_metrics_daily (ad_entity_id, day, workspace_id, currency, spend_micros, clicks, impressions, conversions)
		SELECT $1, current_date - d, $2, 'EUR', 30000000, 30, 500, 1 FROM generate_series(1, 30) d`, camp, ws)
	run := &adsuggest.Runner{Pool: e.pool, Store: e.integrations, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if res, err := run.Run(ctx, ws); err != nil || res.Suggested == 0 {
		t.Fatalf("run: %+v %v", res, err)
	}
	list := owner.must(200, "GET", base+"/findings?status=open&autonomy=approve", nil)["data"].([]any)
	by := map[string]map[string]any{}
	for _, x := range list {
		x := x.(map[string]any)
		by[x["kind"].(string)] = x
	}
	pause := by["pause"]
	if pause == nil || pause["unit"] != "payers" || pause["autonomy"] != "approve" || pause["rule"] != "pause" || pause["rule_version"] != 2.0 || pause["shadow"] != false || !strings.HasSuffix(pause["reason"].(string), "none.") {
		t.Fatalf("the pause finding: %v", list)
	}
	if ev := pause["evidence"].(map[string]any); ev["unit"] != "payers" || ev["payers"] != 0.0 || ev["target_micros"] != 10_000_000.0 {
		t.Fatalf("evidence: %v", ev)
	}
	if l := owner.must(200, "GET", base+"/findings?kind=pause", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("by kind: %v", l)
	}
	if l := owner.must(200, "GET", base+"/findings?campaign_id="+camp+"&status=judged", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("judged: %v", l)
	}
	owner.must(400, "GET", base+"/findings?status=nope", nil)
	owner.must(404, "GET", "/v1/brands/00000000-0000-4000-8000-000000000001/ads/findings", nil)
	// The workspace route lists the same rows with the same fields.
	if l := owner.must(200, "GET", "/v1/workspaces/"+ws+"/ads/suggestions?kind=pause&status=open", nil)["data"].([]any); len(l) != 1 || l[0].(map[string]any)["impact_micros"] == nil && pause["impact_micros"] != nil {
		t.Fatalf("workspace route: %v", l)
	}
	// The status says decisions wait.
	st := owner.must(200, "GET", "/v1/workspaces/"+ws+"/status", nil)
	if b := statusBlockerOf(st, "ads_decisions_waiting"); b == nil || b["area"] != "ads" || b["severity"] != "info" || b["fix"].(map[string]any)["tool"] != "list_ad_findings" {
		t.Fatalf("decisions blocker: %v", st["blockers"])
	}

	// Only a person approves while savings don't apply on their own.
	tok := &client{t: t, h: e.h, bearer: owner.must(201, "POST", "/v1/workspaces/"+ws+"/api-tokens", map[string]any{"name": "Agent", "scopes": []string{"ads.manage", "analytics.read"}})["token"].(string)}
	tok.must(403, "POST", base+"/findings/"+pause["id"].(string)+"/approve", nil)
	ok := owner.must(202, "POST", base+"/findings/"+pause["id"].(string)+"/approve", nil)
	if ok["status"] != "measuring" || ok["change_id"] == nil || ok["measure_from"] == nil || !strings.Contains(ok["window_note"].(string), "measured") {
		t.Fatalf("approved: %v", ok)
	}
	owner.must(409, "POST", base+"/findings/"+pause["id"].(string)+"/approve", nil)
	owner.must(409, "POST", base+"/findings/"+pause["id"].(string)+"/dismiss", nil)
	// Not applied yet: nothing to revert.
	owner.must(409, "POST", base+"/findings/"+pause["id"].(string)+"/revert", nil)
	if err := e.ads.ApplyChange(ctx, ws, ok["change_id"].(string), false); err != nil {
		t.Fatal(err)
	}
	// The fake network keeps saying the campaign is enabled, so the
	// mirror is set to what the platform would show once the pause took.
	exec(`UPDATE ad_entities SET status = 'paused' WHERE id = $1`, camp)
	// Reverting resumes the campaign through the same job.
	rv := owner.must(202, "POST", base+"/findings/"+pause["id"].(string)+"/revert", nil)
	if rv["reverted_at"] == nil || rv["revert_change_id"] == nil || rv["status"] != "measuring" {
		t.Fatalf("reverted: %v", rv)
	}
	owner.must(409, "POST", base+"/findings/"+pause["id"].(string)+"/revert", nil)
	var kind, after string
	if err := e.pool.QueryRow(ctx, `SELECT kind, after::text FROM ad_changes WHERE id = $1`, rv["revert_change_id"]).Scan(&kind, &after); err != nil || kind != "resume" || !strings.Contains(after, `"active"`) {
		t.Fatalf("the inverse change: %s %s %v", kind, after, err)
	}
	if err := e.ads.ApplyChange(ctx, ws, rv["revert_change_id"].(string), false); err != nil {
		t.Fatal(err)
	}
	if l := owner.must(200, "GET", base+"/findings", nil)["data"].([]any); l[0].(map[string]any)["kind"] != "budget_down" {
		// Ranked: the open cut (a decision) comes before the measuring pause.
		t.Fatalf("ranking: %v", l)
	}

	// The levers this layer added: a target move waits for a person and
	// applies through the findings job, with the mirror updated.
	var tid string
	if err := e.pool.QueryRow(ctx, `INSERT INTO ad_suggestions (workspace_id, brand_id, ad_entity_id, kind, status, autonomy, unit, rule, reason, proposed)
		VALUES ($1, $2, $3, 'target_tighten', 'open', 'approve', 'payers', 'target_tighten', 'Lower the target?', '{"target_cpa_micros": 8000000}') RETURNING id::text`, ws, brand, camp).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	exec(`DELETE FROM ad_suggestions WHERE kind = 'budget_down' AND workspace_id = $1`, ws)
	tt := owner.must(202, "POST", base+"/findings/"+tid+"/approve", nil)
	if err := e.pool.QueryRow(ctx, `SELECT kind, after::text FROM ad_changes WHERE id = $1`, tt["change_id"]).Scan(&kind, &after); err != nil || kind != "target" || !strings.Contains(after, `"target_cpa_micros": 8000000`) {
		t.Fatalf("the target change: %s %s %v", kind, after, err)
	}
	if err := run.ApplyFindingChange(ctx, ws, tt["change_id"].(string), false); err != nil {
		t.Fatal(err)
	}
	var status string
	var mirrored *int64
	if err := e.pool.QueryRow(ctx, `SELECT c.status, e.platform_target_cpa_micros FROM ad_changes c JOIN ad_entities e ON e.id = c.ad_entity_id WHERE c.id = $1`, tt["change_id"]).Scan(&status, &mirrored); err != nil || status != "applied" || mirrored == nil || *mirrored != 8_000_000 {
		t.Fatalf("target applied: %s %v %v", status, mirrored, err)
	}
	googleSuffix.Lock()
	calls := strings.Join(googleSuffix.mutates, "\n")
	googleSuffix.Unlock()
	if !strings.Contains(calls, `"maximizeConversions":{"targetCpaMicros":"8000000"}`) {
		t.Fatalf("no target call in:\n%s", calls)
	}
	// Reverting puts the target back.
	rt := owner.must(202, "POST", base+"/findings/"+tid+"/revert", nil)
	if err := e.pool.QueryRow(ctx, `SELECT kind, after::text FROM ad_changes WHERE id = $1`, rt["revert_change_id"]).Scan(&kind, &after); err != nil || kind != "target" || !strings.Contains(after, `"target_cpa_micros": 10000000`) {
		t.Fatalf("the target back: %s %s %v", kind, after, err)
	}
	if err := run.ApplyFindingChange(ctx, ws, rt["revert_change_id"].(string), false); err != nil {
		t.Fatal(err)
	}

	// An exclusion applied on its own reverts through the findings job too.
	var xid, xchange string
	if err := e.pool.QueryRow(ctx, `INSERT INTO ad_changes (workspace_id, brand_id, ad_entity_id, kind, before, after, status, applied_at)
		VALUES ($1, $2, $3, 'exclusion_add', '{}', '{"exclusion": {"kind": "device", "value": "tablet"}}', 'applied', now()) RETURNING id::text`, ws, brand, camp).Scan(&xchange); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO ad_suggestions (workspace_id, brand_id, ad_entity_id, kind, status, autonomy, unit, rule, reason, change_id, applied_auto, decided_at, measure_from, measure_until)
		VALUES ($1, $2, $3, 'exclusion_add', 'measuring', 'auto', 'payers', 'exclusion', 'Stop showing on tablets?', $4, true, now(), current_date + 1, current_date + 15) RETURNING id::text`, ws, brand, camp, xchange).Scan(&xid); err != nil {
		t.Fatal(err)
	}
	rx := owner.must(202, "POST", base+"/findings/"+xid+"/revert", nil)
	if err := e.pool.QueryRow(ctx, `SELECT kind FROM ad_changes WHERE id = $1`, rx["revert_change_id"]).Scan(&kind); err != nil || kind != "exclusion_remove" {
		t.Fatalf("exclusion back: %s %v", kind, err)
	}
	if err := run.ApplyFindingChange(ctx, ws, rx["revert_change_id"].(string), false); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT status FROM ad_changes WHERE id = $1`, rx["revert_change_id"]).Scan(&status); err != nil || status != "applied" {
		t.Fatalf("exclusion removed: %s %v", status, err)
	}

	// A saving where the extension's hook allows it: an agent may approve
	// it; anything that spends more it may not, and the hook isn't asked.
	// Without the hook only a person approves.
	var sid, uid string
	if err := e.pool.QueryRow(ctx, `INSERT INTO ad_suggestions (workspace_id, brand_id, ad_entity_id, kind, status, autonomy, unit, rule, reason, proposed)
		VALUES ($1, $2, $3, 'budget_down', 'open', 'auto', 'payers', 'budget_cut', 'Cut?', '{"daily_budget_micros": 24000000}') RETURNING id::text`, ws, brand, camp).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO ad_suggestions (workspace_id, brand_id, ad_entity_id, kind, status, autonomy, unit, rule, reason, proposed)
		VALUES ($1, $2, $3, 'budget_up', 'open', 'approve', 'payers', 'budget_raise', 'Raise?', '{"daily_budget_micros": 36000000}') RETURNING id::text`, ws, brand, camp).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	tok.must(403, "POST", base+"/findings/"+sid+"/approve", nil)
	var asked []string
	e.srv.SetAdAutoApprove(func(_ context.Context, w, b, kind string) bool {
		asked = append(asked, kind)
		return w == ws && b == brand && adsuggest.Saves(kind)
	})
	t.Cleanup(func() { e.srv.SetAdAutoApprove(nil) })
	tok.must(403, "POST", base+"/findings/"+uid+"/approve", nil)
	if len(asked) != 0 {
		t.Fatalf("the hook was asked about %v", asked)
	}
	if s := tok.must(202, "POST", base+"/findings/"+sid+"/approve", nil); s["status"] != "measuring" {
		t.Fatalf("agent approves a saving: %v", s)
	}
	// Undoing a saving spends more again: an agent's undo waits for a
	// person whatever its grant says.
	gated, gatedID := gatedAgent(t, owner, "/v1/workspaces/"+ws, "Findings agent", "analytics.read", "ads.manage")
	owner.must(201, "POST", "/v1/workspaces/"+ws+"/action-grants", map[string]any{"token_id": gatedID, "path": "ads/**", "mode": "run"})
	if st, held := gated.do("POST", base+"/findings/"+sid+"/revert", nil); st != 202 || held["approval_required"] != true {
		t.Fatalf("an agent's undo of a saving: %d %v", st, held)
	}
	e.srv.SetAdAutoApprove(nil)
	if d := owner.must(200, "POST", base+"/findings/"+uid+"/dismiss", nil); d["status"] != "dismissed" {
		t.Fatalf("dismissed: %v", d)
	}

	// Alerts are acknowledged, not approved.
	var aid string
	if err := e.pool.QueryRow(ctx, `INSERT INTO ad_suggestions (workspace_id, brand_id, ad_entity_id, kind, status, autonomy, unit, rule, reason)
		VALUES ($1, $2, $3, 'alert.target_stale', 'open', 'alert', 'platform_conversions', 'stale_target', 'Still right?')
		ON CONFLICT (ad_entity_id, kind) WHERE status = 'open' DO UPDATE SET reason = EXCLUDED.reason RETURNING id::text`, ws, brand, camp).Scan(&aid); err != nil {
		t.Fatal(err)
	}
	owner.must(409, "POST", base+"/findings/"+aid+"/approve", nil)
	if a := owner.must(200, "POST", base+"/findings/"+aid+"/acknowledge", nil); a["status"] != "acknowledged" {
		t.Fatalf("acknowledged: %v", a)
	}
	owner.must(409, "POST", base+"/findings/"+sid+"/acknowledge", nil)

	// A refresh within the hour is a no-op that says when the next may be.
	exec(`DELETE FROM jobs WHERE kind = 'ads.suggest' AND workspace_id = $1`, ws)
	if r := owner.must(202, "POST", base+"/findings/refresh", nil); r["queued"] != true || r["next_at"] == nil {
		t.Fatalf("refresh: %v", r)
	}
	if r := owner.must(202, "POST", base+"/findings/refresh", nil); r["queued"] != false || r["next_at"] == nil {
		t.Fatalf("second refresh: %v", r)
	}
	if r := owner.must(202, "POST", "/v1/workspaces/"+ws+"/ads/suggestions/refresh", nil); r["queued"] != false {
		t.Fatalf("workspace refresh: %v", r)
	}

	names := map[string]int{}
	reverts, autos := 0, 0
	for _, ev := range got() {
		names[ev.Name]++
		if ev.Props["revert"] == true {
			reverts++
		}
		if ev.Name == "ad_suggestion.approved" && ev.Props["auto"] == true {
			autos++
		}
	}
	for _, n := range []string{"ad_suggestion.created", "ad_suggestion.approved", "ad_suggestion.reverted", "ad_suggestion.dismissed", "ad_suggestion.acknowledged",
		"ad_campaign.paused", "ad_campaign.resumed", "ad_campaign.target_changed", "ad_campaign.exclusion_removed", "ad_change.applied"} {
		if names[n] == 0 {
			t.Fatalf("no %s event: %v", n, names)
		}
	}
	if reverts != 3 || autos != 1 {
		t.Fatalf("reverts %d, agent approvals %d: %v", reverts, autos, names)
	}
}
