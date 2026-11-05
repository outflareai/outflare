package api_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/adsync"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Tests inside the networks: a person starts a Google Ads experiment that
// bids to a lower target; the job makes it, reads both arms, judges it,
// and keeps it when the person says so. On Meta a budget suggestion is
// tested as a split test against a copy of the campaign and rolled back.
func TestAdTests(t *testing.T) {
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
	step := func(id, s string) {
		t.Helper()
		if err := e.ads.RunTestStep(ctx, ws, id, s, false); err != nil {
			t.Fatal(err)
		}
	}

	// Google: an experiment on the search campaign.
	googleSuffix.Lock()
	googleSuffix.mutates, googleSuffix.refuse = nil, false
	googleSuffix.Unlock()
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
	var camp string
	if err := e.pool.QueryRow(ctx, `SELECT id FROM ad_entities WHERE workspace_id = $1 AND level = 'campaign' AND external_id = '7'`, ws).Scan(&camp); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE ad_entities SET target_cost_per_conversion_micros = 10000000 WHERE id = $1`, camp)

	tok := &client{t: t, h: e.h, bearer: owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Agent", "scopes": []string{"ads.manage", "analytics.read"}})["token"].(string)}
	body := map[string]any{"campaign_id": camp, "kind": "bidding", "target_cpa_micros": 5_000_000}
	tok.must(403, "POST", base+"/ads/tests", body)
	owner.must(422, "POST", base+"/ads/tests", map[string]any{"campaign_id": camp, "kind": "budget", "daily_budget_micros": 40_000_000})
	owner.must(400, "POST", base+"/ads/tests", map[string]any{"campaign_id": camp, "kind": "bidding"})
	test := owner.must(202, "POST", base+"/ads/tests", body)
	id := test["id"].(string)
	if test["status"] != "starting" || test["split_percent"] != 50.0 || test["window_note"] == "" || test["measure_from"] == test["starts_on"] {
		t.Fatalf("created: %v", test)
	}
	owner.must(409, "POST", base+"/ads/tests", body)

	step(id, adsync.TestStart)
	list := owner.must(200, "GET", base+"/ads/tests?connector=google_ads&status=running", nil)["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["platform_test_id"] != "customers/123/experiments/9" || list[0].(map[string]any)["trial_campaign_id"] != "7001" {
		t.Fatalf("running: %v", list)
	}
	googleSuffix.Lock()
	calls := strings.Join(googleSuffix.mutates, "\n")
	googleSuffix.Unlock()
	for _, want := range []string{`experiments:mutate {"operations":[{"create":{"endDate":`, `"type":"SEARCH_CUSTOM"`,
		`"campaigns":["customers/123/campaigns/7"],"control":true`, `"trafficSplit":50`,
		`"maximizeConversions":{"targetCpaMicros":"5000000"},"resourceName":"customers/123/campaigns/7001"`, `experiments/9:scheduleExperiment`} {
		if !strings.Contains(calls, want) {
			t.Fatalf("no call %s in:\n%s", want, calls)
		}
	}

	// A check before it ends keeps reading; after its days and the lag, it
	// is judged: the trial converted 80 times for the money the campaign
	// converted 50.
	exec(`UPDATE ad_tests SET starts_on = current_date - 30, measure_from = current_date - 23, ends_on = current_date - 1 WHERE id = $1`, id)
	step(id, adsync.TestCheck)
	if v := owner.must(200, "GET", base+"/ads/tests?campaign_id="+camp, nil)["data"].([]any)[0].(map[string]any); v["status"] != "running" || v["result"].(map[string]any)["verdict_so_far"] != "worked" {
		t.Fatalf("checked: %v", v)
	}
	exec(`UPDATE ad_tests SET ends_on = current_date - 6 WHERE id = $1`, id)
	step(id, adsync.TestCheck)
	v := owner.must(200, "GET", base+"/ads/tests?campaign_id="+camp, nil)["data"].([]any)[0].(map[string]any)
	if v["status"] != "judged" || v["verdict"] != "worked" || v["verdict_note"] == "" {
		t.Fatalf("judged: %v", v)
	}
	tok.must(403, "POST", base+"/ads/tests/"+id+"/keep", nil)
	if k := owner.must(202, "POST", base+"/ads/tests/"+id+"/keep", nil); k["status"] != "ending" || k["decision"] != "keep" {
		t.Fatalf("keep: %v", k)
	}
	owner.must(409, "POST", base+"/ads/tests/"+id+"/roll-back", nil)
	step(id, adsync.TestEnd)
	googleSuffix.Lock()
	calls = strings.Join(googleSuffix.mutates, "\n")
	googleSuffix.Unlock()
	if !strings.Contains(calls, "experiments/9:promoteExperiment") {
		t.Fatalf("no promote in:\n%s", calls)
	}
	if v := owner.must(200, "GET", base+"/ads/tests?status=kept", nil)["data"].([]any); len(v) != 1 {
		t.Fatalf("kept: %v", v)
	}

	// Meta: a budget suggestion tested as a split test, then rolled back.
	metaState.Lock()
	metaState.campaigns, metaState.posts, metaState.refuse = nil, nil, false
	metaState.Unlock()
	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	if _, err := e.ads.SyncOnce(ctx, ws, i["id"].(string)); err != nil {
		t.Fatal(err)
	}
	active, _ := metaCampaigns(t, e, ws)
	var sid string
	if err := e.pool.QueryRow(ctx, `INSERT INTO ad_suggestions (workspace_id, ad_entity_id, kind, reason, proposed)
		VALUES ($1, $2, 'budget_up', 'Spends its budget under target.', '{"daily_budget_micros": 36000000}') RETURNING id`, ws, active).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	s := owner.must(202, "POST", base+"/ads/suggestions/"+sid+"/approve", map[string]any{"test": true})
	if s["status"] != "testing" || s["test_id"] == nil || s["change_id"] != nil {
		t.Fatalf("tested suggestion: %v", s)
	}
	mid := s["test_id"].(string)
	step(mid, adsync.TestStart)
	mt := owner.must(200, "GET", base+"/ads/tests?connector=meta_ads", nil)["data"].([]any)[0].(map[string]any)
	if mt["status"] != "running" || mt["platform_test_id"] != "700" || mt["trial_campaign_id"] != "90" || mt["kind"] != "budget" || mt["suggestion_id"] != sid {
		t.Fatalf("meta test: %v", mt)
	}
	owner.must(202, "POST", base+"/ads/tests/"+mid+"/roll-back", nil)
	step(mid, adsync.TestEnd)
	metaState.Lock()
	posts := strings.Join(metaState.posts, "\n")
	metaState.Unlock()
	for _, want := range []string{"COPY 9", "POST 90 3600", `STUDY 600_ad_studies`, `"campaigns":["9"]`, `"campaigns":["90"]`, "POST 90 ACTIVE", "POST 90 PAUSED", "STUDY 700"} {
		if !strings.Contains(strings.ReplaceAll(posts, "600/ad_studies", "600_ad_studies"), want) {
			t.Fatalf("no %s in:\n%s", want, posts)
		}
	}
	if strings.Contains(posts, "POST 9 3600") {
		t.Fatalf("a rolled back test changed the campaign:\n%s", posts)
	}
	j := owner.must(200, "GET", base+"/ads/suggestions?status=judged&campaign_id="+active, nil)["data"].([]any)
	if len(j) != 1 || !strings.HasPrefix(j[0].(map[string]any)["verdict_note"].(string), "Tested, and rolled back.") {
		t.Fatalf("suggestion after the test: %v", j)
	}

	names := map[string]int{}
	for _, ev := range got() {
		names[ev.Name]++
	}
	for _, n := range []string{"ad_test.created", "ad_test.started", "ad_test.judged", "ad_test.kept", "ad_test.rolled_back", "ad_test.ended"} {
		if names[n] == 0 {
			t.Fatalf("no %s event: %v", n, names)
		}
	}
}
