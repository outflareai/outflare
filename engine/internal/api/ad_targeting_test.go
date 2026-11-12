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

// Targeting: a person reads a campaign's bidding, audiences and countries
// live, and changes them as queued changes the job makes on the platform;
// an agent's change always waits for a person. Google suggests a target
// once a campaign that maximises conversions has 30 in four weeks, and
// approving it sets the target.
func TestAdTargeting(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	apply := func(res map[string]any) map[string]any {
		t.Helper()
		id := res["change"].(map[string]any)["id"].(string)
		if err := e.ads.ApplyChange(ctx, ws, id, false); err != nil {
			t.Fatal(err)
		}
		for _, c := range owner.must(200, "GET", base+"/ads/changes", nil)["data"].([]any) {
			if c.(map[string]any)["id"] == id {
				return c.(map[string]any)
			}
		}
		t.Fatalf("change %s not listed", id)
		return nil
	}
	googleSuffix.Lock()
	googleSuffix.mutates, googleSuffix.refuse = nil, false
	googleSuffix.Unlock()
	metaState.Lock()
	metaState.campaigns, metaState.posts, metaState.refuse, metaState.targeting = nil, nil, false, nil
	metaState.Unlock()

	// Google.
	var gid string
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		i, err := e.integrations.Insert(ctx, tx, ws, "google_ads", connectors.AuthOAuth2, "Google Ads", connectors.Creds{"access_token": "gt"}, connectors.Account{}, "")
		gid = i.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ads.SyncOnce(ctx, ws, gid); err != nil {
		t.Fatal(err)
	}
	var gcamp string
	if err := e.pool.QueryRow(ctx, `SELECT id FROM ad_entities WHERE workspace_id = $1 AND level = 'campaign' AND external_id = '7'`, ws).Scan(&gcamp); err != nil {
		t.Fatal(err)
	}
	tg := owner.must(200, "GET", base+"/ads/campaigns/"+gcamp+"/targeting", nil)
	tt := tg["targeting"].(map[string]any)
	if tg["supported"] != true || len(tg["bidding_methods"].([]any)) != 3 || tt["bidding"].(map[string]any)["strategy"] != "maximize_conversions" ||
		tt["bidding"].(map[string]any)["target_cpa_micros"] != 4_000_000.0 || len(tt["countries"].([]any)) != 1 || tt["countries"].([]any)[0] != "AU" ||
		tt["audiences"].([]any)[0].(map[string]any)["name"] != "Trial users" {
		t.Fatalf("google targeting: %v", tg)
	}
	owner.must(400, "POST", base+"/ads/campaigns/"+gcamp+"/bidding", map[string]any{"strategy": "manual_cpc"})
	owner.must(400, "POST", base+"/ads/campaigns/"+gcamp+"/bidding", map[string]any{"strategy": "maximize_conversions", "target_roas": 3})
	owner.must(400, "POST", base+"/ads/campaigns/"+gcamp+"/countries", map[string]any{"country": "Australia", "action": "add"})
	owner.must(400, "POST", base+"/ads/campaigns/"+gcamp+"/audiences", map[string]any{"audience_id": "501", "action": "keep"})
	res := owner.must(202, "POST", base+"/ads/campaigns/"+gcamp+"/bidding", map[string]any{"strategy": "maximize_conversion_value", "target_roas": 4})
	owner.must(409, "POST", base+"/ads/campaigns/"+gcamp+"/bidding", map[string]any{"strategy": "maximize_conversions"})
	if c := apply(res); c["status"] != "applied" || c["kind"] != "bidding" {
		t.Fatalf("bidding: %v", c)
	}
	if c := apply(owner.must(202, "POST", base+"/ads/campaigns/"+gcamp+"/countries", map[string]any{"country": "nz", "action": "add"})); c["status"] != "applied" || c["after"].(map[string]any)["country"] != "NZ" {
		t.Fatalf("country: %v", c)
	}
	// AU is the campaign's only place: taking it out is refused by the
	// connector, and the change fails.
	if c := apply(owner.must(202, "POST", base+"/ads/campaigns/"+gcamp+"/countries", map[string]any{"country": "AU", "action": "remove"})); c["status"] != "failed" || !strings.Contains(c["error"].(string), "only place") {
		t.Fatalf("last place: %v", c)
	}
	if c := apply(owner.must(202, "POST", base+"/ads/campaigns/"+gcamp+"/audiences", map[string]any{"audience_id": "501", "audience_name": "Trial users", "action": "remove"})); c["status"] != "applied" {
		t.Fatalf("audience: %v", c)
	}
	googleSuffix.Lock()
	mutates := strings.Join(googleSuffix.mutates, "\n")
	googleSuffix.Unlock()
	for _, want := range []string{`"maximizeConversionValue":{"targetRoas":4}`, `"updateMask":"maximize_conversion_value.target_roas"`,
		`"location":{"geoTargetConstant":"geoTargetConstants/2554"}`, `"remove":"customers/123/campaignCriteria/7~501"`} {
		if !strings.Contains(mutates, want) {
			t.Errorf("google mutates lack %s:\n%s", want, mutates)
		}
	}
	var strategy string
	var roas *float64
	if err := e.pool.QueryRow(ctx, `SELECT bidding_strategy, platform_target_roas FROM ad_entities WHERE id = $1`, gcamp).Scan(&strategy, &roas); err != nil || strategy != "maximize_conversion_value" || roas == nil || *roas != 4 {
		t.Fatalf("mirror: %s %v %v", strategy, roas, err)
	}

	// An agent's targeting change always waits for a person.
	agent, agentID := gatedAgent(t, owner, base, "Ads agent", "analytics.read", "ads.manage")
	owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "ads/campaigns/**", "mode": "run"})
	if st, res := agent.do("POST", base+"/ads/campaigns/"+gcamp+"/countries", map[string]any{"country": "NZ", "action": "remove"}); st != 202 || res["approval_required"] != true ||
		res["action"].(map[string]any)["decided_by_rule"] != "always_person" {
		t.Fatalf("agent: %d %v", st, res)
	}

	// Meta: audiences and countries on every ad set, bidding not here.
	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	if _, err := e.ads.SyncOnce(ctx, ws, i["id"].(string)); err != nil {
		t.Fatal(err)
	}
	mcamp, _ := metaCampaigns(t, e, ws)
	tg = owner.must(200, "GET", base+"/ads/campaigns/"+mcamp+"/targeting", nil)
	if len(tg["bidding_methods"].([]any)) != 0 || tg["targeting"].(map[string]any)["countries"].([]any)[0] != "AU" {
		t.Fatalf("meta targeting: %v", tg)
	}
	owner.must(422, "POST", base+"/ads/campaigns/"+mcamp+"/bidding", map[string]any{"strategy": "maximize_conversions"})
	apply(owner.must(202, "POST", base+"/ads/campaigns/"+mcamp+"/audiences", map[string]any{"audience_id": "801", "action": "add"}))
	apply(owner.must(202, "POST", base+"/ads/campaigns/"+mcamp+"/countries", map[string]any{"country": "NZ", "action": "add"}))
	tt = owner.must(200, "GET", base+"/ads/campaigns/"+mcamp+"/targeting", nil)["targeting"].(map[string]any)
	if len(tt["countries"].([]any)) != 2 || tt["audiences"].([]any)[0].(map[string]any)["id"] != "801" {
		t.Fatalf("meta after: %v", tt)
	}
	metaState.Lock()
	age := metaState.targeting["age_min"]
	metaState.Unlock()
	if age != 25.0 {
		t.Fatalf("the rest of the ad set's targeting was lost: %v", age)
	}

	// The bidding ladder on Google: no target, 30 conversions in four weeks.
	exec(`UPDATE ad_entities SET bidding_strategy = 'maximize_conversions', platform_target_cpa_micros = NULL, platform_target_roas = NULL, target_cost_per_conversion_micros = NULL WHERE id = $1`, gcamp)
	exec(`DELETE FROM ad_changes WHERE ad_entity_id = $1`, gcamp)
	exec(`DELETE FROM ad_metrics_daily WHERE ad_entity_id = $1`, gcamp)
	// The brand's rules out of shadow mode, so the finding is offered.
	exec(`INSERT INTO ad_brand_economics (brand_id, workspace_id, shadow_mode) SELECT a.brand_id, a.workspace_id, false FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id
		WHERE e.id = $1 AND a.brand_id IS NOT NULL ON CONFLICT (brand_id) DO UPDATE SET shadow_mode = false`, gcamp)
	exec(`INSERT INTO ad_metrics_daily (ad_entity_id, day, workspace_id, currency, spend_micros, conversions)
		SELECT $1, current_date - d, $2, 'EUR', 20000000, 1.5 FROM generate_series(1, 28) d`, gcamp, ws)
	run := &adsuggest.Runner{Pool: e.pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if _, err := run.Run(ctx, ws); err != nil {
		t.Fatal(err)
	}
	var sid string
	for _, s := range owner.must(200, "GET", base+"/ads/suggestions?status=open&campaign_id="+gcamp, nil)["data"].([]any) {
		s := s.(map[string]any)
		if s["kind"] == "bidding_target" {
			sid = s["id"].(string)
			// What a conversion cost, to the cent and never above it.
			if s["proposed"].(map[string]any)["target_cpa_micros"] != 13_330_000.0 {
				t.Fatalf("ladder: %v", s)
			}
		}
	}
	if sid == "" {
		t.Fatal("no bidding_target suggestion")
	}
	sug := owner.must(202, "POST", base+"/ads/suggestions/"+sid+"/approve", nil)
	if sug["status"] != "measuring" || !strings.Contains(sug["window_note"].(string), "14 days to settle") {
		t.Fatalf("approved: %v", sug)
	}
	if c := apply(map[string]any{"change": map[string]any{"id": sug["change_id"]}}); c["after"].(map[string]any)["bidding"].(map[string]any)["target_cpa_micros"] != 13_330_000.0 {
		t.Fatalf("ladder change: %v", c)
	}

	evs := got()
	for name, want := range map[string]int{"ad_campaign.bidding_changed": 2, "ad_campaign.country_added": 2, "ad_campaign.country_removed": 1, "ad_campaign.audience_added": 1,
		"ad_campaign.audience_removed": 1, "ad_change.failed": 1, "ad_suggestion.approved": 1} {
		if n := len(named(evs, name)); n != want {
			t.Errorf("%s: %d events, want %d", name, n, want)
		}
	}
}
