package api_test

import (
	"context"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// A person pauses, resumes and re-budgets a campaign: each is a queued
// change with its audit event, the job makes the one call on the
// workspace's own account, reads the campaign back and records that the
// platform shows it, and the mirror follows.
func TestAdCampaignChanges(t *testing.T) {
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

	// Checked before anything is queued.
	owner.must(404, "POST", base+"/ads/campaigns/00000000-0000-0000-0000-000000000000/pause", nil)
	owner.must(409, "POST", base+"/ads/campaigns/"+paused+"/pause", nil)
	owner.must(409, "POST", base+"/ads/campaigns/"+active+"/resume", nil)
	owner.must(400, "PATCH", base+"/ads/campaigns/"+active, map[string]any{})
	owner.must(400, "PATCH", base+"/ads/campaigns/"+active, map[string]any{"daily_budget_micros": 0})
	reader := &client{t: t, h: e.h, bearer: owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Reader", "scopes": []string{"analytics.read"}})["token"].(string)}
	reader.must(403, "POST", base+"/ads/campaigns/"+active+"/pause", nil)

	// Pause: queued with the campaign as it was, then applied and shown.
	res := owner.must(202, "POST", base+"/ads/campaigns/"+active+"/pause", nil)
	ch := res["change"].(map[string]any)
	if res["queued"] != true || ch["kind"] != "pause" || ch["status"] != "queued" || ch["before"].(map[string]any)["status"] != "active" ||
		ch["before"].(map[string]any)["daily_budget_micros"] != 30_000_000.0 || ch["after"].(map[string]any)["status"] != "paused" || ch["actor_id"] == nil {
		t.Fatalf("pause: %v", res)
	}
	owner.must(409, "POST", base+"/ads/campaigns/"+active+"/pause", nil) // already queued
	if err := e.ads.ApplyChange(ctx, ws, ch["id"].(string), false); err != nil {
		t.Fatal(err)
	}
	l := owner.must(200, "GET", base+"/ads/changes?campaign_id="+active, nil)["data"].([]any)
	if len(l) != 1 || l[0].(map[string]any)["status"] != "applied" || l[0].(map[string]any)["verified_at"] == nil || l[0].(map[string]any)["verify_note"] != "" {
		t.Fatalf("applied: %v", l)
	}
	if c := owner.must(200, "GET", base+"/ads/campaigns?status=paused", nil)["data"].([]any); len(c) != 1 || c[0].(map[string]any)["id"] != active {
		t.Fatalf("mirror after the pause: %v", c)
	}

	// Resume, and a budget in micros, which Meta takes in cents.
	res = owner.must(202, "POST", base+"/ads/campaigns/"+active+"/resume", nil)
	if err := e.ads.ApplyChange(ctx, ws, res["change"].(map[string]any)["id"].(string), false); err != nil {
		t.Fatal(err)
	}
	res = owner.must(202, "PATCH", base+"/ads/campaigns/"+active, map[string]any{"daily_budget_micros": 42_500_000})
	if err := e.ads.ApplyChange(ctx, ws, res["change"].(map[string]any)["id"].(string), false); err != nil {
		t.Fatal(err)
	}
	metaState.Lock()
	posts := strings.Join(metaState.posts, ",")
	metaState.Unlock()
	if posts != "POST 9 PAUSED,POST 9 ACTIVE,POST 9 4250" {
		t.Fatalf("what Meta was told: %s", posts)
	}
	for _, c := range owner.must(200, "GET", base+"/ads/campaigns", nil)["data"].([]any) {
		if m := c.(map[string]any); m["id"] == active && (m["status"] != "active" || m["daily_budget_micros"] != 42_500_000.0) {
			t.Fatalf("mirror after the changes: %v", m)
		}
	}

	// The platform refuses: the change fails with its reason, and the
	// mirror stays.
	metaState.Lock()
	metaState.refuse = true
	metaState.Unlock()
	res = owner.must(202, "PATCH", base+"/ads/campaigns/"+active, map[string]any{"daily_budget_micros": 50_000_000})
	if err := e.ads.ApplyChange(ctx, ws, res["change"].(map[string]any)["id"].(string), false); err != nil {
		t.Fatal(err)
	}
	l = owner.must(200, "GET", base+"/ads/changes?status=failed", nil)["data"].([]any)
	if len(l) != 1 || !strings.Contains(l[0].(map[string]any)["error"].(string), "Invalid parameter") {
		t.Fatalf("refused: %v", l)
	}
	metaState.Lock()
	metaState.refuse = false
	metaState.Unlock()

	// The kill switch pauses every active campaign of the brand at once.
	bid := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	accts := owner.must(200, "GET", base+"/ads/accounts", nil)["data"].([]any)
	owner.must(200, "PATCH", base+"/ads/accounts/"+accts[0].(map[string]any)["id"].(string), map[string]any{"brand": bid})
	res = owner.must(202, "POST", "/v1/brands/"+bid+"/ads/pause-all", nil)
	changes := res["changes"].([]any)
	if len(changes) != 1 || changes[0].(map[string]any)["campaign_id"] != active || changes[0].(map[string]any)["batch_id"] != res["batch_id"] {
		t.Fatalf("pause all: %v", res)
	}
	if err := e.ads.ApplyChange(ctx, ws, changes[0].(map[string]any)["id"].(string), false); err != nil {
		t.Fatal(err)
	}
	if c := owner.must(200, "GET", base+"/ads/campaigns?status=active", nil)["data"].([]any); len(c) != 0 {
		t.Fatalf("still active: %v", c)
	}

	evs := got()
	for name, want := range map[string]int{"ad_campaign.paused": 2, "ad_campaign.resumed": 1, "ad_campaign.budget_changed": 2, "ads.paused_all": 1, "ad_change.applied": 4, "ad_change.failed": 1} {
		if n := len(named(evs, name)); n != want {
			t.Errorf("%s: %d events, want %d", name, n, want)
		}
	}
}

// An agent's change goes through the action layer's paid ads rules: a
// pause runs under its grant, a raise past a fifth or a budget where
// there was none always waits for a person, and a change that would put
// the brand's campaigns over the grant's caps waits too.
func TestAdCampaignChangesByAnAgent(t *testing.T) {
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
	agent, agentID := gatedAgent(t, owner, base, "Ads agent", "analytics.read", "ads.manage")
	// A run grant on the campaigns with the default daily cap, $50, and a
	// month wide enough not to matter here.
	g := owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "ads/campaigns/**", "mode": "run", "monthly_spend_cents": 1_000_000})
	if g["daily_spend_cents"] != 5000.0 {
		t.Fatalf("grant: %v", g)
	}

	// Within the caps, a lower budget and a raise within a fifth run; each
	// is applied before the next, since one queued change per kind is all
	// a campaign takes.
	apply := func(res map[string]any) {
		t.Helper()
		if err := e.ads.ApplyChange(ctx, ws, res["change"].(map[string]any)["id"].(string), false); err != nil {
			t.Fatal(err)
		}
	}
	st, res := agent.do("PATCH", base+"/ads/campaigns/"+active, map[string]any{"daily_budget_micros": 20_000_000})
	if st != 202 || res["queued"] != true {
		t.Fatalf("lower: %d %v", st, res)
	}
	apply(res)
	st, res = agent.do("PATCH", base+"/ads/campaigns/"+active, map[string]any{"daily_budget_micros": 24_000_000})
	if st != 202 || res["queued"] != true {
		t.Fatalf("raise within a fifth: %d %v", st, res)
	}
	apply(res)
	// Past a fifth: always a person.
	st, res = agent.do("PATCH", base+"/ads/campaigns/"+active, map[string]any{"daily_budget_micros": 40_000_000})
	if st != 202 || res["approval_required"] != true || res["action"].(map[string]any)["decided_by_rule"] != "always_person" {
		t.Fatalf("big raise: %d %v", st, res)
	}
	// Resuming the paused campaign ($5 a day) would put the brand's
	// campaigns at $29 a day, under the cap: it runs. The platform refuses
	// that one, so the campaign stays paused.
	st, res = agent.do("POST", base+"/ads/campaigns/"+paused+"/resume", nil)
	if st != 202 || res["queued"] != true {
		t.Fatalf("resume under the cap: %d %v", st, res)
	}
	metaState.Lock()
	metaState.refuse = true
	metaState.Unlock()
	apply(res)
	metaState.Lock()
	metaState.refuse = false
	metaState.Unlock()
	// With the active campaign budgeted higher, resuming would pass the
	// cap: it waits for a person.
	if _, err := e.pool.Exec(ctx, `UPDATE ad_entities SET daily_budget_micros = 48000000 WHERE id = $1`, active); err != nil {
		t.Fatal(err)
	}
	st, res = agent.do("POST", base+"/ads/campaigns/"+paused+"/resume", map[string]any{"reason": "second try"})
	if st != 202 || res["approval_required"] != true || res["action"].(map[string]any)["decided_by_rule"] != "spend_limit" ||
		!strings.Contains(res["action"].(map[string]any)["hold_reason"].(string), "$53.00 a day") {
		t.Fatalf("resume over the cap: %d %v", st, res)
	}
	// A pause spends nothing: it runs whatever the totals.
	st, res = agent.do("POST", base+"/ads/campaigns/"+active+"/pause", nil)
	if st != 202 || res["queued"] != true {
		t.Fatalf("pause: %d %v", st, res)
	}
	apply(res)
	// A person can still approve the held raise, and it is then queued as
	// the agent's change, linked to the action.
	held := owner.must(200, "GET", base+"/actions?status=held", nil)["data"].([]any)
	var raise string
	for _, h := range held {
		if m := h.(map[string]any); m["method"] == "PATCH" {
			raise = m["id"].(string)
		}
	}
	done := owner.must(200, "POST", base+"/actions/"+raise+"/approve", nil)
	if done["status"] != "executed" {
		t.Fatalf("approved raise: %v", done)
	}
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM ad_changes WHERE workspace_id = $1 AND action_id = $2 AND kind = 'budget'`, ws, raise).Scan(&n); err != nil || n != 1 {
		t.Fatalf("change linked to the action: %d %v", n, err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT id FROM ad_changes WHERE workspace_id = $1 AND action_id = $2`, ws, raise).Scan(&raise); err != nil {
		t.Fatal(err)
	}
	if err := e.ads.ApplyChange(ctx, ws, raise, false); err != nil {
		t.Fatal(err)
	}

	// The monthly cap: what the month has spent plus the daily total for
	// the rest of it. $500 a month can't take $48 a day on top of what the
	// sync booked, even for a raise within a fifth.
	owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "ads/campaigns/*", "method": "PATCH", "mode": "run", "daily_spend_cents": 5000, "monthly_spend_cents": 50000})
	st, res = agent.do("PATCH", base+"/ads/campaigns/"+active, map[string]any{"daily_budget_micros": 46_000_000})
	if st != 202 || res["approval_required"] != true || res["action"].(map[string]any)["decided_by_rule"] != "spend_limit" ||
		!strings.Contains(res["action"].(map[string]any)["hold_reason"].(string), "this month") {
		t.Fatalf("over the month: %d %v", st, res)
	}
}

// metaCampaigns finds the fake Meta account's two campaigns after a sync:
// the active one (9) and the paused one (10), which no list shows because
// it never spent.
func metaCampaigns(t *testing.T, e *env, ws string) (active, paused string) {
	t.Helper()
	for ext, id := range map[string]*string{"9": &active, "10": &paused} {
		if err := e.pool.QueryRow(context.Background(), `SELECT id FROM ad_entities WHERE workspace_id = $1 AND level = 'campaign' AND external_id = $2`, ws, ext).Scan(id); err != nil {
			t.Fatalf("campaign %s: %v", ext, err)
		}
	}
	return active, paused
}
