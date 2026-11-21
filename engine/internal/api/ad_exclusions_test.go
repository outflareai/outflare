package api_test

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/adsuggest"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Placement blocks (plans/ad-tracker.md, B8): a Zeropark campaign is
// paused like any other, and one of its targets is paused (an exclusion
// added) and resumed (taken out) through the exclusions endpoint, each a
// queued change a job carries out on the workspace's own account. An
// agent may add an exclusion within its grant; taking one out always
// waits for a person.
func TestAdExclusions(t *testing.T) {
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
	const cid = "11111111-1111-4111-8111-111111111111"
	zeroparkFake.campaign(cid, "Push US")

	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "zeropark_ads", "credentials": map[string]string{"api_token": "test-good"}})
	if _, err := e.ads.SyncOnce(ctx, ws, i["id"].(string)); err != nil {
		t.Fatal(err)
	}
	var camp string
	if err := e.pool.QueryRow(ctx, `SELECT id::text FROM ad_entities WHERE workspace_id = $1 AND level = 'campaign' AND external_id = $2`, ws, cid).Scan(&camp); err != nil {
		t.Fatal(err)
	}
	run := &adsuggest.Runner{Pool: e.pool, Store: e.integrations, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	// The campaign pauses like any other platform's.
	res := owner.must(202, "POST", bb+"/ads/campaigns/"+camp+"/pause", nil)
	if err := e.ads.ApplyChange(ctx, ws, res["change"].(map[string]any)["id"].(string), false); err != nil {
		t.Fatal(err)
	}
	if asked := zeroparkFake.asked(); len(asked) != 1 || asked[0] != "/api/campaign/"+cid+"/pause?" {
		t.Fatalf("pause calls: %v", asked)
	}
	var status string
	if err := e.pool.QueryRow(ctx, `SELECT status FROM ad_entities WHERE id = $1`, camp).Scan(&status); err != nil || status != "paused" {
		t.Fatalf("mirror after the pause: %s %v", status, err)
	}

	// Refused before anything is queued.
	owner.must(400, "POST", bb+"/ads/campaigns/"+camp+"/exclusions", map[string]any{"kind": "placement", "value": "pub-17", "action": "block"})
	owner.must(400, "POST", bb+"/ads/campaigns/"+camp+"/exclusions", map[string]any{"kind": "placement", "value": "", "action": "add"})
	if _, body := owner.do("POST", bb+"/ads/campaigns/"+camp+"/exclusions", map[string]any{"kind": "device", "value": "mobile", "action": "add"}); !strings.Contains(body["error"].(map[string]any)["message"].(string), "placement") {
		t.Fatalf("a kind Zeropark doesn't take: %v", body)
	}
	owner.must(404, "POST", bb+"/ads/campaigns/00000000-0000-0000-0000-000000000000/exclusions", map[string]any{"kind": "placement", "value": "pub-17", "action": "add"})

	// A target is paused: one queued change, then the call.
	res = owner.must(202, "POST", bb+"/ads/campaigns/"+camp+"/exclusions", map[string]any{"kind": "placement", "value": "pub-17", "label": "Site 17", "action": "add"})
	ch := res["change"].(map[string]any)
	if res["queued"] != true || ch["kind"] != "exclusion_add" || ch["status"] != "queued" || ch["after"].(map[string]any)["exclusion"].(map[string]any)["value"] != "pub-17" {
		t.Fatalf("queued exclusion: %v", res)
	}
	owner.must(409, "POST", bb+"/ads/campaigns/"+camp+"/exclusions", map[string]any{"kind": "placement", "value": "pub-17", "action": "add"})
	// Another target can be queued beside it.
	other := owner.must(202, "POST", bb+"/ads/campaigns/"+camp+"/exclusions", map[string]any{"kind": "placement", "value": "pub-18", "action": "add"})
	for _, id := range []string{ch["id"].(string), other["change"].(map[string]any)["id"].(string)} {
		if err := run.ApplyFindingChange(ctx, ws, id, false); err != nil {
			t.Fatal(err)
		}
	}
	asked := zeroparkFake.asked()
	if !slices.Contains(asked, "/api/campaign/"+cid+"/targets/pause?hashOrAddress=pub-17") || !slices.Contains(asked, "/api/campaign/"+cid+"/targets/pause?hashOrAddress=pub-18") {
		t.Fatalf("target pause calls: %v", asked)
	}
	if err := e.pool.QueryRow(ctx, `SELECT status FROM ad_changes WHERE id = $1`, ch["id"]).Scan(&status); err != nil || status != "applied" {
		t.Fatalf("the change: %s %v", status, err)
	}

	// A person takes it out again.
	res = owner.must(202, "POST", bb+"/ads/campaigns/"+camp+"/exclusions", map[string]any{"kind": "placement", "value": "pub-17", "action": "remove"})
	if err := run.ApplyFindingChange(ctx, ws, res["change"].(map[string]any)["id"].(string), false); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(zeroparkFake.asked(), "/api/campaign/"+cid+"/targets/resume?hashOrAddress=pub-17") {
		t.Fatalf("target resume call: %v", zeroparkFake.asked())
	}

	// An agent with a run grant pauses a target on its own; resuming one
	// always waits for a person.
	agent, agentID := gatedAgent(t, owner, base, "Ads agent", "analytics.read", "ads.manage")
	owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "ads/campaigns/**", "mode": "run"})
	st, r := agent.do("POST", bb+"/ads/campaigns/"+camp+"/exclusions", map[string]any{"kind": "placement", "value": "pub-19", "action": "add"})
	if st != 202 || r["queued"] != true {
		t.Fatalf("an agent's exclusion: %d %v", st, r)
	}
	st, r = agent.do("POST", bb+"/ads/campaigns/"+camp+"/exclusions", map[string]any{"kind": "placement", "value": "pub-18", "action": "remove"})
	if st != 202 || r["approval_required"] != true || r["action"].(map[string]any)["decided_by_rule"] != "always_person" {
		t.Fatalf("an agent taking an exclusion out: %d %v", st, r)
	}

	// A platform that takes only audiences as exclusions (Meta) answers
	// 400 for a placement, naming what it does take.
	metaState.Lock()
	metaState.campaigns, metaState.posts, metaState.refuse = nil, nil, false
	metaState.Unlock()
	m := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	if _, err := e.ads.SyncOnce(ctx, ws, m["id"].(string)); err != nil {
		t.Fatal(err)
	}
	active, _ := metaCampaigns(t, e, ws)
	owner.must(400, "POST", bb+"/ads/campaigns/"+active+"/exclusions", map[string]any{"kind": "placement", "value": "site.example", "action": "add"})

	names := map[string]int{}
	for _, ev := range got() {
		names[ev.Name]++
	}
	if names["ad_campaign.exclusion_added"] != 3 || names["ad_campaign.exclusion_removed"] != 1 {
		t.Fatalf("events: added %d, removed %d", names["ad_campaign.exclusion_added"], names["ad_campaign.exclusion_removed"])
	}
}
