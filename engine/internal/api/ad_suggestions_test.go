package api_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/outflareai/outflare/engine/internal/adsuggest"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Suggestions: the run suggests a budget raise for a campaign that spends
// its whole budget under target and a resume for a paused one that did
// well; a person approves one (the change is made and measured) and
// dismisses the other; after the window the run judges it.
func TestAdSuggestions(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	metaState.Lock()
	metaState.campaigns, metaState.posts, metaState.refuse = nil, nil, false
	metaState.Unlock()

	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	if _, err := e.ads.SyncOnce(ctx, ws, i["id"].(string)); err != nil {
		t.Fatal(err)
	}
	active, paused := metaCampaigns(t, e, ws)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// days writes a campaign's numbers for days ago from..to (1 is yesterday).
	days := func(id string, from, to int, spend int64, conv float64) {
		t.Helper()
		exec(`INSERT INTO ad_metrics_daily (ad_entity_id, day, workspace_id, currency, spend_micros, conversions)
			SELECT $1, current_date - d, $2, 'USD', $3, $4 FROM generate_series($5::int, $6::int) d
			ON CONFLICT (ad_entity_id, day) DO UPDATE SET spend_micros = EXCLUDED.spend_micros, conversions = EXCLUDED.conversions`, id, ws, spend, conv, to, from)
	}
	exec(`DELETE FROM ad_metrics_daily WHERE workspace_id = $1`, ws)
	exec(`UPDATE ad_entities SET target_cost_per_conversion_micros = 10000000 WHERE id = ANY($1::uuid[])`, []string{active, paused})
	days(active, 14, 1, 30_000_000, 5) // the whole budget, at 6 a conversion
	days(paused, 40, 21, 10_000_000, 3)

	run := &adsuggest.Runner{Pool: e.pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if res, err := run.Run(ctx, ws); err != nil || res.Suggested != 2 {
		t.Fatalf("run: %+v %v", res, err)
	}
	if res, _ := run.Run(ctx, ws); res.Suggested != 0 || res.Expired != 0 {
		t.Fatalf("a second run suggests nothing new: %+v", res)
	}
	list := owner.must(200, "GET", base+"/ads/suggestions?status=open&connector=meta_ads", nil)["data"].([]any)
	by := map[string]map[string]any{}
	for _, s := range list {
		s := s.(map[string]any)
		by[s["kind"].(string)] = s
	}
	up, resume := by["budget_up"], by["resume"]
	if len(list) != 2 || up == nil || resume == nil || up["campaign_id"] != active || up["proposed"].(map[string]any)["daily_budget_micros"] != 36_000_000.0 || up["reason"] == "" {
		t.Fatalf("suggestions: %v", list)
	}
	if l := owner.must(200, "GET", base+"/ads/suggestions?connector=google_ads", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("another network: %v", l)
	}

	// Only a person approves; an agent's token is refused.
	tok := &client{t: t, h: e.h, bearer: owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Agent", "scopes": []string{"ads.manage", "analytics.read"}})["token"].(string)}
	tok.must(403, "POST", base+"/ads/suggestions/"+up["id"].(string)+"/approve", nil)

	ok := owner.must(202, "POST", base+"/ads/suggestions/"+up["id"].(string)+"/approve", nil)
	if ok["status"] != "measuring" || ok["change_id"] == nil || ok["measure_from"] == nil || ok["window_note"] == "" {
		t.Fatalf("approved: %v", ok)
	}
	owner.must(409, "POST", base+"/ads/suggestions/"+up["id"].(string)+"/approve", nil)
	if d := owner.must(200, "POST", base+"/ads/suggestions/"+resume["id"].(string)+"/dismiss", nil); d["status"] != "dismissed" {
		t.Fatalf("dismissed: %v", d)
	}
	changes := owner.must(200, "GET", base+"/ads/changes?campaign_id="+active, nil)["data"].([]any)
	if len(changes) != 1 || changes[0].(map[string]any)["after"].(map[string]any)["daily_budget_micros"] != 36_000_000.0 {
		t.Fatalf("the change: %v", changes)
	}
	if err := e.ads.ApplyChange(ctx, ws, ok["change_id"].(string), false); err != nil {
		t.Fatal(err)
	}

	// Twenty days later: 17 days before the change and 17 after it.
	exec(`UPDATE ad_changes SET applied_at = now() - interval '20 days', created_at = now() - interval '20 days' WHERE id = $1`, ok["change_id"])
	exec(`UPDATE ad_suggestions SET measure_from = current_date - 17, measure_until = current_date - 1 WHERE id = $1`, up["id"])
	exec(`DELETE FROM ad_metrics_daily WHERE workspace_id = $1`, ws)
	days(active, 37, 21, 30_000_000, 5)
	days(active, 17, 1, 36_000_000, 6) // a sixth more spend, a sixth more conversions: 6 each
	if res, err := run.Run(ctx, ws); err != nil || res.Judged != 1 {
		t.Fatalf("judge: %+v %v", res, err)
	}
	j := owner.must(200, "GET", base+"/ads/suggestions?campaign_id="+active+"&status=judged", nil)["data"].([]any)
	if len(j) != 1 || j[0].(map[string]any)["verdict"] != "worked" || j[0].(map[string]any)["verdict_note"] == "" {
		t.Fatalf("judged: %v", j)
	}

	names := map[string]int{}
	for _, ev := range got() {
		names[ev.Name]++
	}
	for _, n := range []string{"ad_suggestion.created", "ad_suggestion.approved", "ad_suggestion.dismissed", "ad_suggestion.judged", "ad_suggestions.refreshed"} {
		if names[n] == 0 {
			t.Fatalf("no %s event: %v", n, names)
		}
	}
	owner.must(202, "POST", base+"/ads/suggestions/refresh", nil)
}

// An agent approves a suggestion only where an extension lets the brand's
// savings apply on their own, and then only one that saves money.
func TestAdSuggestionsApproveOnTheirOwn(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	metaState.Lock()
	metaState.campaigns, metaState.posts, metaState.refuse = nil, nil, false
	metaState.Unlock()
	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	if _, err := e.ads.SyncOnce(ctx, ws, i["id"].(string)); err != nil {
		t.Fatal(err)
	}
	active, paused := metaCampaigns(t, e, ws)
	brand := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	suggest := func(campaign, kind string, proposed string) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, `INSERT INTO ad_suggestions (workspace_id, brand_id, ad_entity_id, kind, reason, proposed)
			VALUES ($1, $2, $3, $4, 'Spent for two weeks with no conversion.', $5) RETURNING id`, ws, brand, campaign, kind, proposed).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	pause := suggest(active, "pause", `{"status":"paused"}`)
	resume := suggest(paused, "resume", `{"status":"active"}`)
	tok := &client{t: t, h: e.h, bearer: owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Agent", "scopes": []string{"ads.manage", "analytics.read"}})["token"].(string)}

	// No hook: only a person.
	tok.must(403, "POST", base+"/ads/suggestions/"+pause+"/approve", nil)
	var asked []string
	e.srv.SetAdAutoApprove(func(_ context.Context, w, b, kind string) bool {
		asked = append(asked, kind)
		return w == ws && b == brand
	})
	t.Cleanup(func() { e.srv.SetAdAutoApprove(nil) })
	// Spending more always waits for a person; the hook isn't even asked.
	tok.must(403, "POST", base+"/ads/suggestions/"+resume+"/approve", nil)
	if len(asked) != 0 {
		t.Fatalf("the hook was asked about %v", asked)
	}
	ok := tok.must(202, "POST", base+"/ads/suggestions/"+pause+"/approve", nil)
	if ok["status"] != "measuring" || ok["change_id"] == nil || len(asked) != 1 || asked[0] != "pause" {
		t.Fatalf("approved on its own: %v %v", ok, asked)
	}
	// The hook says no for this brand now.
	e.srv.SetAdAutoApprove(func(context.Context, string, string, string) bool { return false })
	down := suggest(paused, "budget_down", `{"daily_budget_micros":8000000}`)
	tok.must(403, "POST", base+"/ads/suggestions/"+down+"/approve", nil)

	auto := 0
	for _, ev := range got() {
		if ev.Name == "ad_suggestion.approved" && ev.Props["auto"] == true {
			auto++
		}
	}
	if auto != 1 {
		t.Fatalf("approved on their own: %d events", auto)
	}
}
