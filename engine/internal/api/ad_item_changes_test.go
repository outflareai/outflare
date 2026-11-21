package api_test

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/adsuggest"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Changes below the campaign (plans/ad-tracker.md, B8): one ad set or ad
// is paused and resumed, and an ad set's own daily budget set, each a
// queued change on the item's campaign that a job carries out on the
// workspace's own account. An agent may pause, and lower a budget, within
// its grant; a resume and a raise always wait for a person.
func TestAdItemChanges(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	bb := "/v1/brands/" + brand
	metaState.Lock()
	metaState.campaigns, metaState.posts, metaState.refuse, metaState.adsetBudget = nil, nil, false, ""
	metaState.Unlock()
	t.Cleanup(func() {
		metaState.Lock()
		metaState.adsetBudget, metaState.refuse = "", false
		metaState.Unlock()
	})
	m := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	sync := func() {
		t.Helper()
		if _, err := e.ads.SyncOnce(ctx, ws, m["id"].(string)); err != nil {
			t.Fatal(err)
		}
	}
	sync()
	item := func(level, external string) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, `SELECT id::text FROM ad_entities WHERE workspace_id = $1 AND level = $2 AND external_id = $3`, ws, level, external).Scan(&id); err != nil {
			t.Fatalf("%s %s: %v", level, external, err)
		}
		return id
	}
	set, ad := item("ad_group", "31"), item("ad", "41")
	camp, _ := metaCampaigns(t, e, ws)
	run := &adsuggest.Runner{Pool: e.pool, Store: e.integrations, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	apply := func(res map[string]any) map[string]any {
		t.Helper()
		id := res["change"].(map[string]any)["id"].(string)
		if err := run.ApplyFindingChange(ctx, ws, id, false); err != nil {
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
	status := func(id string) string {
		t.Helper()
		var st string
		if err := e.pool.QueryRow(ctx, `SELECT status FROM ad_entities WHERE id = $1`, id).Scan(&st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	posts := func() []string {
		metaState.Lock()
		defer metaState.Unlock()
		return slices.Clone(metaState.posts)
	}

	// Not an item, a campaign's id, and an item that is already as asked.
	owner.must(404, "POST", bb+"/ads/items/not-an-id/pause", nil)
	owner.must(404, "POST", bb+"/ads/items/"+camp+"/pause", nil)
	owner.must(409, "POST", bb+"/ads/items/"+set+"/resume", nil)

	// The ad set is paused: the change is on its campaign and names it.
	res := owner.must(202, "POST", bb+"/ads/items/"+set+"/pause", nil)
	ch := res["change"].(map[string]any)
	if ch["kind"] != "ad_group_pause" || ch["campaign_id"] != camp || ch["after"].(map[string]any)["ad_group_id"] != "31" || ch["after"].(map[string]any)["name"] != "Lookalike" ||
		ch["after"].(map[string]any)["status"] != "paused" || ch["before"].(map[string]any)["status"] != "active" {
		t.Fatalf("queued ad set pause: %v", ch)
	}
	// One change at a time for the same ad set; the ad is another matter.
	owner.must(409, "POST", bb+"/ads/items/"+set+"/pause", nil)
	if ch := apply(res); ch["status"] != "applied" || status(set) != "paused" {
		t.Fatalf("applied ad set pause: %v (mirror %s)", ch, status(set))
	}
	// And resumed.
	if ch := apply(owner.must(202, "POST", bb+"/ads/items/"+set+"/resume", nil)); ch["kind"] != "ad_group_resume" || ch["status"] != "applied" || status(set) != "active" {
		t.Fatalf("ad set resume: %v (mirror %s)", ch, status(set))
	}

	// The ad is paused and resumed the same way.
	res = owner.must(202, "POST", bb+"/ads/items/"+ad+"/pause", nil)
	if ch := res["change"].(map[string]any); ch["kind"] != "ad_pause" || ch["after"].(map[string]any)["ad_id"] != "41" || ch["after"].(map[string]any)["ad_group_id"] != "31" {
		t.Fatalf("queued ad pause: %v", ch)
	}
	if ch := apply(res); ch["status"] != "applied" || status(ad) != "paused" {
		t.Fatalf("applied ad pause: %v (mirror %s)", ch, status(ad))
	}
	if ch := apply(owner.must(202, "POST", bb+"/ads/items/"+ad+"/resume", nil)); ch["kind"] != "ad_resume" || status(ad) != "active" {
		t.Fatalf("ad resume: %v", ch)
	}
	if got, want := strings.Join(posts(), "\n"), "POST 31 PAUSED\nPOST 31 ACTIVE\nPOST 41 PAUSED\nPOST 41 ACTIVE"; got != want {
		t.Fatalf("Meta was asked:\n%s", got)
	}

	// A budget: an ad has none, a bad amount is refused, and an ad set
	// whose campaign holds the budget says to change it there.
	owner.must(400, "PATCH", bb+"/ads/items/"+set, map[string]any{})
	owner.must(400, "PATCH", bb+"/ads/items/"+set, map[string]any{"daily_budget_micros": 5})
	owner.must(400, "PATCH", bb+"/ads/items/"+ad, map[string]any{"daily_budget_micros": 20_000_000})
	if st, body := owner.do("PATCH", bb+"/ads/items/"+set, map[string]any{"daily_budget_micros": 20_000_000}); st != 409 ||
		!strings.Contains(body["error"].(map[string]any)["message"].(string), "its campaign holds the budget") {
		t.Fatalf("an ad set without its own budget: %d %v", st, body)
	}
	// With its own budget of 40.00 a day, it is set to 25.00.
	metaState.Lock()
	metaState.adsetBudget = "4000"
	metaState.Unlock()
	sync()
	owner.must(409, "PATCH", bb+"/ads/items/"+set, map[string]any{"daily_budget_micros": 40_000_000})
	res = owner.must(202, "PATCH", bb+"/ads/items/"+set, map[string]any{"daily_budget_micros": 25_000_000})
	if ch := res["change"].(map[string]any); ch["kind"] != "ad_group_budget" || ch["before"].(map[string]any)["daily_budget_micros"] != float64(40_000_000) ||
		ch["after"].(map[string]any)["daily_budget_micros"] != float64(25_000_000) {
		t.Fatalf("queued ad set budget: %v", ch)
	}
	owner.must(409, "PATCH", bb+"/ads/items/"+set, map[string]any{"daily_budget_micros": 26_000_000})
	var budget int64
	if ch := apply(res); ch["status"] != "applied" {
		t.Fatalf("applied ad set budget: %v", ch)
	}
	if err := e.pool.QueryRow(ctx, `SELECT daily_budget_micros FROM ad_entities WHERE id = $1`, set).Scan(&budget); err != nil || budget != 25_000_000 {
		t.Fatalf("mirror after the budget: %d %v", budget, err)
	}
	if p := posts(); p[len(p)-1] != "POST 31 2500" {
		t.Fatalf("budget post: %v", p)
	}
	// Meta refusing it ends the change as failed, and the mirror stays.
	metaState.Lock()
	metaState.refuse = true
	metaState.Unlock()
	if ch := apply(owner.must(202, "PATCH", bb+"/ads/items/"+set, map[string]any{"daily_budget_micros": 30_000_000})); ch["status"] != "failed" || ch["error"] == "" {
		t.Fatalf("a refused budget: %v", ch)
	}
	metaState.Lock()
	metaState.refuse = false
	metaState.Unlock()

	// An agent: a pause and a lower budget run within its grant; a resume
	// and a raise always wait for a person.
	agent, agentID := gatedAgent(t, owner, base, "Ads agent", "analytics.read", "ads.manage")
	owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "ads/items/**", "mode": "run"})
	st, r := agent.do("POST", bb+"/ads/items/"+ad+"/pause", nil)
	if st != 202 || r["queued"] != true {
		t.Fatalf("an agent's ad pause: %d %v", st, r)
	}
	apply(r)
	st, r = agent.do("POST", bb+"/ads/items/"+ad+"/resume", nil)
	if st != 202 || r["approval_required"] != true || r["action"].(map[string]any)["decided_by_rule"] != "always_person" {
		t.Fatalf("an agent's ad resume: %d %v", st, r)
	}
	st, r = agent.do("PATCH", bb+"/ads/items/"+set, map[string]any{"daily_budget_micros": 20_000_000})
	if st != 202 || r["queued"] != true {
		t.Fatalf("an agent lowering an ad set's budget: %d %v", st, r)
	}
	apply(r)
	st, r = agent.do("PATCH", bb+"/ads/items/"+set, map[string]any{"daily_budget_micros": 21_000_000})
	if st != 202 || r["approval_required"] != true || r["action"].(map[string]any)["decided_by_rule"] != "always_person" {
		t.Fatalf("an agent raising an ad set's budget: %d %v", st, r)
	}

	// Google: an ad group is paused; it has no budget of its own to set.
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
	group := item("ad_group", "71")
	if ch := apply(owner.must(202, "POST", bb+"/ads/items/"+group+"/pause", nil)); ch["status"] != "applied" || status(group) != "paused" {
		t.Fatalf("Google ad group pause: %v", ch)
	}
	googleSuffix.Lock()
	mutates := strings.Join(googleSuffix.mutates, "\n")
	googleSuffix.Unlock()
	if !strings.Contains(mutates, "/adGroups:mutate") || !strings.Contains(mutates, `"resourceName":"customers/123/adGroups/71"`) || !strings.Contains(mutates, `"status":"PAUSED"`) {
		t.Fatalf("Google was asked:\n%s", mutates)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE ad_entities SET daily_budget_micros = 10000000 WHERE id = $1`, group); err != nil {
		t.Fatal(err)
	}
	owner.must(422, "PATCH", bb+"/ads/items/"+group, map[string]any{"daily_budget_micros": 12_000_000})

	names := map[string]int{}
	for _, ev := range got() {
		names[ev.Name]++
		for k, v := range ev.Props {
			if s, ok := v.(string); ok && (s == "Lookalike" || s == "Demo video") {
				t.Fatalf("event %s names the item in %s", ev.Name, k)
			}
		}
	}
	if names["ad_campaign.ad_group_paused"] != 2 || names["ad_campaign.ad_group_resumed"] != 1 || names["ad_campaign.ad_paused"] != 2 || names["ad_campaign.ad_resumed"] != 1 ||
		names["ad_campaign.ad_group_budget_changed"] != 3 {
		t.Fatalf("events: %v", names)
	}
}
