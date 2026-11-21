package api_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/outflareai/outflare/engine/internal/adconv"
	"github.com/outflareai/outflare/engine/internal/adsuggest"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/track"
)

// An excluded audience is a list of people no campaign should show to:
// its sync queues the list's exclusion on every active campaign of the
// account once, a campaign a person took it off stays without it, and
// turning the switch off takes it out of the rest.
func TestAdAudienceExclusion(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	metaState.Lock()
	metaState.campaigns, metaState.posts, metaState.refuse, metaState.targeting = nil, nil, false, nil
	metaState.Unlock()
	brand := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	b := "/v1/brands/" + brand + "/ads/audiences"

	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	if _, err := e.ads.SyncOnce(ctx, ws, i["id"].(string)); err != nil {
		t.Fatal(err)
	}
	acct := owner.must(200, "GET", base+"/ads/accounts", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	active, paused := metaCampaigns(t, e, ws)

	ana, err := customers.EnsurePerson(ctx, e.pool, ws, "email", "ana@acme.example", true, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO site_sessions (workspace_id, anonymous_id, record_id, country, started_at, last_seen_at)
		VALUES ($1, 'anon-ana', $2, 'US', now(), now())`, ws, ana); err != nil {
		t.Fatal(err)
	}
	var list string
	if err := e.pool.QueryRow(ctx, `INSERT INTO smart_lists (workspace_id, brand_id, name, kind) VALUES ($1, $2, 'Refunded', 'static') RETURNING id`, ws, brand).Scan(&list); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO static_list_members (workspace_id, list_id, record_id) VALUES ($1, $2, $3)`, ws, list, ana); err != nil {
		t.Fatal(err)
	}

	page := owner.must(200, "GET", b, nil)
	if a := page["accounts"].([]any)[0].(map[string]any); a["can_exclude"] != true {
		t.Fatalf("accounts: %v", a)
	}
	a := owner.must(201, "POST", b, map[string]any{"name": "Refunded", "list_id": list, "ad_account_ids": []string{acct}, "exclude": true})
	id := a["id"].(string)
	dest := a["destinations"].([]any)[0].(map[string]any)
	if a["exclude"] != true || dest["can_exclude"] != true || dest["excluded_campaigns"] != 0.0 {
		t.Fatalf("created: %v", a)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := &adconv.Sender{Pool: e.pool, Store: e.integrations, Log: log}
	run := &adsuggest.Runner{Pool: e.pool, Store: e.integrations, Log: log}
	sync := func() adconv.AudienceResult {
		t.Helper()
		res, err := s.SyncAudience(ctx, ws, dest["id"].(string))
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	changes := func(campaign string) (kinds, statuses []string) {
		t.Helper()
		rows, err := e.pool.Query(ctx, `SELECT kind, status FROM ad_changes WHERE ad_entity_id = $1 AND after->'exclusion'->>'kind' = 'audience' ORDER BY created_at, id`, campaign)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var k, st string
			if err := rows.Scan(&k, &st); err != nil {
				t.Fatal(err)
			}
			kinds, statuses = append(kinds, k), append(statuses, st)
		}
		return kinds, statuses
	}
	applyQueued := func() {
		t.Helper()
		rows, err := e.pool.Query(ctx, `SELECT id::text FROM ad_changes WHERE workspace_id = $1 AND status = 'queued' AND kind IN ('exclusion_add', 'exclusion_remove') ORDER BY created_at, id`, ws)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		rows.Close()
		for _, id := range ids {
			if err := run.ApplyFindingChange(ctx, ws, id, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	excluded := func() []any {
		metaState.Lock()
		defer metaState.Unlock()
		x, _ := metaState.targeting["excluded_custom_audiences"].([]any)
		return x
	}

	// The first sync makes the list and queues its exclusion on the
	// active campaign only.
	if res := sync(); !res.Created || res.Excluded != 1 {
		t.Fatalf("first sync: %+v", res)
	}
	if k, _ := changes(active); len(k) != 1 || k[0] != "exclusion_add" {
		t.Fatalf("active campaign: %v", k)
	}
	if k, _ := changes(paused); len(k) != 0 {
		t.Fatalf("paused campaign: %v", k)
	}
	// It is the findings job's to carry out, under its own change kind.
	var jobsQueued int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs j JOIN ad_changes c ON c.id::text = j.payload->>'change_id'
		WHERE j.kind = $1 AND c.ad_entity_id = $2 AND c.kind = $3`, adsuggest.JobApply, active, adsuggest.ChangeExclusionAdd).Scan(&jobsQueued); err != nil || jobsQueued != 1 {
		t.Fatalf("the change's job: %d %v", jobsQueued, err)
	}
	applyQueued()
	if x := excluded(); len(x) != 1 || x[0].(map[string]any)["id"] != "801" {
		t.Fatalf("ad set targeting: %v", x)
	}
	metaState.Lock()
	age := metaState.targeting["age_min"]
	metaState.Unlock()
	if age != 25.0 {
		t.Fatalf("the rest of the ad set's targeting was lost: %v", age)
	}
	d := owner.must(200, "GET", b+"/"+id, nil)["destinations"].([]any)[0].(map[string]any)
	if d["excluded_campaigns"] != 1.0 {
		t.Fatalf("destination: %v", d)
	}
	// The next sync queues nothing more.
	if res := sync(); res.Excluded != 0 {
		t.Fatalf("second sync: %+v", res)
	}

	// A person takes it off the campaign: the sync leaves it off.
	owner.must(202, "POST", "/v1/brands/"+brand+"/ads/campaigns/"+active+"/exclusions", map[string]any{"kind": "audience", "value": "801", "action": "remove"})
	applyQueued()
	if x := excluded(); len(x) != 0 {
		t.Fatalf("after a person's removal: %v", x)
	}
	if res := sync(); res.Excluded != 0 {
		t.Fatalf("sync after a person's removal: %+v", res)
	}
	// They put it back, then the audience stops excluding: it comes out.
	owner.must(202, "POST", "/v1/brands/"+brand+"/ads/campaigns/"+active+"/exclusions", map[string]any{"kind": "audience", "value": "801", "action": "add"})
	applyQueued()
	if x := excluded(); len(x) != 1 {
		t.Fatalf("put back: %v", x)
	}
	off := owner.must(200, "PATCH", b+"/"+id, map[string]any{"exclude": false})
	if off["exclude"] != false {
		t.Fatalf("off: %v", off)
	}
	applyQueued()
	if x := excluded(); len(x) != 0 {
		t.Fatalf("after the switch went off: %v", x)
	}
	if res := sync(); res.Excluded != 0 {
		t.Fatalf("sync with the switch off: %+v", res)
	}
	// Back on: the removal was the switch's, so the sync excludes again.
	owner.must(200, "PATCH", b+"/"+id, map[string]any{"exclude": true})
	if res := sync(); res.Excluded != 1 {
		t.Fatalf("sync with the switch back on: %+v", res)
	}

	auto, removed := false, false
	for _, ev := range got() {
		if ev.Name == "ad_campaign.exclusion_added" && ev.Props["rule"] == "audience_exclude" && ev.ActorType == "system" {
			auto = true
		}
		if ev.Name == "ad_campaign.exclusion_removed" && ev.Props["exclusion_kind"] == "audience" {
			removed = true
		}
	}
	if !auto || !removed {
		t.Errorf("events: added by the sync %v, removed %v", auto, removed)
	}
}
