package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/url"
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
	// Where the campaign is held back and where its money goes, read live.
	bb := "/v1/brands/" + brandOf(t, e, `SELECT COALESCE(e.brand_id, a.brand_id)::text FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id WHERE e.id = $1`, gcamp)
	// Whether Google says it is really serving, why, and its own score.
	for _, c := range owner.must(200, "GET", bb+"/ads/campaigns?connector=google_ads&days=3650", nil)["data"].([]any) {
		c := c.(map[string]any)
		if c["id"] != gcamp {
			continue
		}
		if why := c["primary_status_reasons"].([]any); c["primary_status"] != "limited" || len(why) != 1 || why[0] != "budget_constrained" || c["optimization_score"] != 0.8214 || c["budget_lost_share"] != 0.23 || c["rank_lost_share"] != 0.4 {
			t.Fatalf("primary status: %v", c)
		}
	}
	var gdetails map[string]string
	if err := e.pool.QueryRow(ctx, `SELECT details FROM ad_entities WHERE id = $1`, gcamp).Scan(&gdetails); err != nil || gdetails["primary_status"] != "limited" {
		t.Fatalf("campaign details: %v %v", gdetails, err)
	}
	sig := owner.must(200, "GET", bb+"/ads/campaigns/"+gcamp+"/signals?from=2026-10-01&to=2026-10-28", nil)
	if sig["supported"] != true || sig["from"] != "2026-10-01" || sig["segments"] == nil || sig["budget_lost_impression_share"] == nil {
		t.Fatalf("signals: %v", sig)
	}
	owner.must(400, "GET", bb+"/ads/campaigns/"+gcamp+"/signals?from=nope", nil)
	owner.must(404, "GET", bb+"/ads/campaigns/00000000-0000-0000-0000-000000000000/signals", nil)
	tg := owner.must(200, "GET", base+"/ads/campaigns/"+gcamp+"/targeting", nil)
	tt := tg["targeting"].(map[string]any)
	if tg["supported"] != true || len(tg["bidding_methods"].([]any)) != 5 || tt["bidding"].(map[string]any)["strategy"] != "maximize_conversions" ||
		tt["bidding"].(map[string]any)["target_cpa_micros"] != 4_000_000.0 || len(tt["countries"].([]any)) != 1 || tt["countries"].([]any)[0] != "AU" ||
		tt["audiences"].([]any)[0].(map[string]any)["name"] != "Trial users" {
		t.Fatalf("google targeting: %v", tg)
	}
	// Every place by name, with the ids a change needs: what it shows in,
	// and what it is kept out of.
	in, out := tt["included_locations"].([]any), tt["excluded_locations"].([]any)
	if tg["observed_at"] == nil || tg["provider_campaign_resource_name"] != "customers/123/campaigns/7" || tt["target_all_locations"] != false ||
		tt["positive_geo_target_type"] != "PRESENCE" || len(in) != 1 || in[0].(map[string]any)["name"] != "Australia" || len(out) != 2 ||
		out[0].(map[string]any)["name"] != "Kenya" || out[0].(map[string]any)["criterion_id"] != "2404" || out[0].(map[string]any)["country_code"] != "KE" ||
		out[0].(map[string]any)["resource_name"] != "customers/123/campaignCriteria/7~2404" || out[0].(map[string]any)["geo_target_constant"] != "geoTargetConstants/2404" ||
		out[1].(map[string]any)["name"] != "Lisbon,Lisbon,Portugal" || out[1].(map[string]any)["target_type"] != "City" ||
		len(tt["excluded_countries"].([]any)) != 1 || tt["excluded_countries"].([]any)[0] != "KE" {
		t.Fatalf("google places: %v", tt)
	}
	googleSuffix.Lock()
	googleSuffix.keepBidding = true
	googleSuffix.Unlock()
	// Any of the account's data, read live in Google's own terms, and what
	// can be asked for. A condition can't carry another clause.
	var gacct string
	if err := e.pool.QueryRow(ctx, `SELECT ad_account_id FROM ad_entities WHERE id = $1`, gcamp).Scan(&gacct); err != nil {
		t.Fatal(err)
	}
	qp := bb + "/ads/accounts/" + gacct + "/query"
	live := owner.must(200, "GET", qp+"?resource=campaign&select=campaign.id,campaign.name&where="+url.QueryEscape("campaign.status = 'ENABLED'")+"&order_by="+url.QueryEscape("campaign.id DESC")+"&limit=5", nil)
	if live["query"] != "SELECT campaign.id, campaign.name FROM campaign WHERE campaign.status = 'ENABLED' ORDER BY campaign.id DESC LIMIT 5" || live["complete"] != true ||
		live["row_count"] != 1.0 || live["observed_at"] == nil || live["rows"].([]any)[0].(map[string]any)["campaign"].(map[string]any)["id"] != "7" {
		t.Fatalf("live read: %v", live)
	}
	owner.must(400, "GET", qp+"?resource=campaign", nil)
	owner.must(400, "GET", qp+"?resource=campaign&select=campaign.id&limit=5000", nil)
	owner.must(422, "GET", qp+"?resource=campaign&select=campaign.id&where="+url.QueryEscape("campaign.id > 0 LIMIT 99999"), nil)
	owner.must(422, "GET", qp+"?resource=campaign&select="+url.QueryEscape("campaign.id FROM customer"), nil)
	owner.must(404, "GET", bb+"/ads/accounts/00000000-0000-0000-0000-000000000000/query?resource=campaign&select=campaign.id", nil)
	fl := owner.must(200, "GET", bb+"/ads/accounts/"+gacct+"/query-fields?resource=campaign", nil)["data"].([]any)
	if len(fl) != 2 || fl[1].(map[string]any)["name"] != "campaign.status" || fl[1].(map[string]any)["data_type"] != "enum" || len(fl[1].(map[string]any)["enum_values"].([]any)) != 3 {
		t.Fatalf("fields: %v", fl)
	}
	if rl := owner.must(200, "GET", bb+"/ads/accounts/"+gacct+"/query-fields", nil)["data"].([]any); len(rl) != 1 || rl[0].(map[string]any)["category"] != "resource" {
		t.Fatalf("resources: %v", rl)
	}
	t.Cleanup(func() {
		googleSuffix.Lock()
		googleSuffix.bidding, googleSuffix.keepBidding = nil, false
		googleSuffix.Unlock()
	})
	// Target impression share needs where on the page, the share and the most to pay for a click.
	owner.must(400, "POST", base+"/ads/campaigns/"+gcamp+"/bidding", map[string]any{"strategy": "target_impression_share", "impression_share": 1})
	owner.must(400, "POST", base+"/ads/campaigns/"+gcamp+"/bidding", map[string]any{"strategy": "target_impression_share", "impression_share_location": "absolute_top_of_page", "impression_share": 1.5, "max_cpc_micros": 5_000_000})
	share := apply(owner.must(202, "POST", base+"/ads/campaigns/"+gcamp+"/bidding", map[string]any{"strategy": "target_impression_share",
		"impression_share_location": "absolute_top_of_page", "impression_share": 1, "max_cpc_micros": 5_000_000}))
	if share["status"] != "applied" || share["verified_at"] == nil {
		t.Fatalf("impression share: %v", share)
	}
	sb := owner.must(200, "GET", base+"/ads/campaigns/"+gcamp+"/targeting", nil)["targeting"].(map[string]any)["bidding"].(map[string]any)
	if sb["strategy"] != "target_impression_share" || sb["impression_share_location"] != "absolute_top_of_page" || sb["impression_share"] != 1.0 || sb["max_cpc_micros"] != 5_000_000.0 {
		t.Fatalf("impression share read back: %v", sb)
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
	for _, want := range []string{`"targetImpressionShare":{"cpcBidCeilingMicros":"5000000","location":"ABSOLUTE_TOP_OF_PAGE","locationFractionMicros":"1000000"}`,
		`"maximizeConversionValue":{"targetRoas":4}`, `"updateMask":"maximize_conversion_value.target_roas"`,
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

	// A campaign's places set together. Seven places are excluded; five
	// come out and Kenya and Nigeria stay, in one request, planned on the
	// places as they were read.
	googleSuffix.Lock()
	googleSuffix.places = map[string]bool{"2036": false, "2404": true, "2566": true, "2554": true, "2356": true, "2586": true, "1011742": true}
	googleSuffix.mutates = nil
	googleSuffix.Unlock()
	t.Cleanup(func() {
		googleSuffix.Lock()
		googleSuffix.places, googleSuffix.name = nil, ""
		googleSuffix.Unlock()
	})
	lp := bb + "/ads/campaigns/" + gcamp + "/locations"
	read := owner.must(200, "GET", base+"/ads/campaigns/"+gcamp+"/targeting", nil)
	before := read["targeting"].(map[string]any)
	fp, _ := read["location_fingerprint"].(string)
	if fp == "" || len(before["excluded_locations"].([]any)) != 6 {
		t.Fatalf("places before: %v", before)
	}
	owner.must(400, "POST", lp+"/preview", map[string]any{})
	owner.must(422, "POST", lp+"/preview", map[string]any{"excluded": []string{"Atlantis"}})
	owner.must(400, "POST", lp+"/preview", map[string]any{"target_all_locations": true, "included": []string{"AU"}})
	owner.must(400, "POST", lp+"/preview", map[string]any{"target_all_locations": false})
	keep := map[string]any{"excluded": []string{"KE", "geoTargetConstants/2566"}, "expected_fingerprint": fp}
	pv := owner.must(200, "POST", lp+"/preview", keep)
	if pv["changes"] != true || pv["reaches_more"] != true || pv["location_fingerprint"] != fp || len(pv["remove_excluded"].([]any)) != 4 || len(pv["add_excluded"].([]any)) != 0 ||
		len(pv["remove_included"].([]any)) != 0 || len(pv["after"].(map[string]any)["excluded_locations"].([]any)) != 2 || len(pv["after"].(map[string]any)["included_locations"].([]any)) != 1 {
		t.Fatalf("preview: %v", pv)
	}
	// A preview changes nothing, and an agent's is answered straight away.
	if st, res := agent.do("POST", lp+"/preview", keep); st != 200 || res["changes"] != true {
		t.Fatalf("agent preview: %d %v", st, res)
	}
	googleSuffix.Lock()
	if len(googleSuffix.mutates) != 0 {
		t.Fatalf("a preview changed something: %v", googleSuffix.mutates)
	}
	googleSuffix.Unlock()
	// An agent's change waits for a person, whatever it was granted.
	if st, res := agent.do("POST", lp, keep); st != 202 || res["approval_required"] != true || res["action"].(map[string]any)["decided_by_rule"] != "always_person" {
		t.Fatalf("agent places: %d %v", st, res)
	}
	owner.must(409, "POST", lp, map[string]any{"excluded": []string{"KE"}, "expected_fingerprint": "not-the-mark"})
	owner.must(409, "POST", lp, map[string]any{"included": []string{"AU"}})
	set := owner.must(202, "POST", lp, keep)
	if set["plan"].(map[string]any)["changes"] != true {
		t.Fatalf("set: %v", set)
	}
	owner.must(409, "POST", lp, keep)
	if c := apply(set); c["status"] != "applied" || c["kind"] != "locations" || c["verified_at"] == nil {
		t.Fatalf("places change: %v", c)
	}
	googleSuffix.Lock()
	if len(googleSuffix.mutates) != 1 || strings.Count(googleSuffix.mutates[0], `"remove"`) != 4 || strings.Contains(googleSuffix.mutates[0], `"create"`) ||
		strings.Contains(googleSuffix.mutates[0], "7~2404") || strings.Contains(googleSuffix.mutates[0], "7~2566") || strings.Contains(googleSuffix.mutates[0], "7~2036") {
		t.Fatalf("places mutate: %v", googleSuffix.mutates)
	}
	googleSuffix.Unlock()
	read = owner.must(200, "GET", base+"/ads/campaigns/"+gcamp+"/targeting", nil)
	after := read["targeting"].(map[string]any)
	if ex := after["excluded_locations"].([]any); len(ex) != 2 || read["location_fingerprint"] == fp || len(after["included_locations"].([]any)) != 1 {
		t.Fatalf("places after: %v", after)
	}
	// A change planned on places that have moved since is refused in its
	// job, and nothing is sent.
	stale := owner.must(202, "POST", lp, map[string]any{"excluded": []string{"KE", "NG", "NZ"}})
	googleSuffix.Lock()
	googleSuffix.places["2586"] = true
	googleSuffix.mutates = nil
	googleSuffix.Unlock()
	if c := apply(stale); c["status"] != "failed" || !strings.Contains(c["error"].(string), "changed") {
		t.Fatalf("stale places: %v", c)
	}
	googleSuffix.Lock()
	if len(googleSuffix.mutates) != 0 {
		t.Fatalf("a stale change was sent: %v", googleSuffix.mutates)
	}
	googleSuffix.Unlock()
	// Every place, said outright, with the exclusions left as they are.
	all := owner.must(202, "POST", lp, map[string]any{"target_all_locations": true})
	if p := all["plan"].(map[string]any); len(p["remove_included"].([]any)) != 1 || len(p["remove_excluded"].([]any)) != 0 || p["after"].(map[string]any)["target_all_locations"] != true {
		t.Fatalf("everywhere: %v", all)
	}
	if c := apply(all); c["status"] != "applied" {
		t.Fatalf("everywhere change: %v", c)
	}
	if tt := owner.must(200, "GET", base+"/ads/campaigns/"+gcamp+"/targeting", nil)["targeting"].(map[string]any); tt["target_all_locations"] != true || len(tt["excluded_locations"].([]any)) != 3 {
		t.Fatalf("everywhere after: %v", tt)
	}

	// A campaign renamed, here and on Google.
	rp := bb + "/ads/campaigns/" + gcamp + "/rename"
	owner.must(400, "POST", rp, map[string]any{"name": "  "})
	if st, res := agent.do("POST", rp, map[string]any{"name": "Brand search AU"}); st != 202 || res["queued"] != true {
		t.Fatalf("agent rename: %d %v", st, res)
	}
	owner.must(409, "POST", rp, map[string]any{"name": "Brand AU two"})
	var renameID string
	if err := e.pool.QueryRow(ctx, `SELECT id FROM ad_changes WHERE workspace_id = $1 AND kind = 'rename' AND status = 'queued'`, ws).Scan(&renameID); err != nil {
		t.Fatal(err)
	}
	if c := apply(map[string]any{"change": map[string]any{"id": renameID}}); c["status"] != "applied" || c["verified_at"] == nil || c["after"].(map[string]any)["name"] != "Brand search AU" {
		t.Fatalf("rename: %v", c)
	}
	var mirrored string
	if err := e.pool.QueryRow(ctx, `SELECT name FROM ad_entities WHERE id = $1`, gcamp).Scan(&mirrored); err != nil || mirrored != "Brand search AU" {
		t.Fatalf("renamed mirror: %q %v", mirrored, err)
	}
	owner.must(409, "POST", rp, map[string]any{"name": "Brand search AU"})

	evs := got()
	for name, want := range map[string]int{"ad_campaign.bidding_changed": 3, "ad_campaign.country_added": 2, "ad_campaign.country_removed": 1, "ad_campaign.audience_added": 1,
		"ad_campaign.audience_removed": 1, "ad_change.failed": 2, "ad_suggestion.approved": 1,
		"ad_campaign.locations_changed": 3, "ad_campaign.renamed": 1} {
		if n := len(named(evs, name)); n != want {
			t.Errorf("%s: %d events, want %d", name, n, want)
		}
	}
}
