package api_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/adsuggest"
)

// Bid changes (plans/ad-tracker.md, B8): a Zeropark campaign's bid, or one
// target's, is set as a queued change; the job keeps the bid the network
// showed before, and with only_lower refuses a raise. An agent may lower a
// bid within its grant; anything else waits for a person.
func TestAdBids(t *testing.T) {
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
	bid := bb + "/ads/campaigns/" + camp + "/bid"
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

	// Refused before anything is queued.
	owner.must(400, "POST", bid, map[string]any{})
	owner.must(400, "POST", bid, map[string]any{"bid_micros": 0})
	if _, body := owner.do("POST", bid, map[string]any{"bid_micros": 9_000_000}); !strings.Contains(body["error"].(map[string]any)["message"].(string), "0.0001 to 5") {
		t.Fatalf("a bid over the range: %v", body)
	}

	// The campaign's bid (0.01 on the network) is raised to 0.012 by a
	// person; the change keeps what it was before.
	res := owner.must(202, "POST", bid, map[string]any{"bid_micros": 12_000})
	if ch := res["change"].(map[string]any); ch["kind"] != "bid" || ch["after"].(map[string]any)["bid"].(map[string]any)["micros"] != float64(12_000) {
		t.Fatalf("queued bid: %v", res)
	}
	owner.must(409, "POST", bid, map[string]any{"bid_micros": 13_000})
	ch := apply(res)
	if ch["status"] != "applied" || ch["before"].(map[string]any)["bid"].(map[string]any)["micros"] != float64(10_000) || zeroparkFake.bidOf(cid) != 0.012 {
		t.Fatalf("applied bid: %v (network %v)", ch, zeroparkFake.bidOf(cid))
	}

	// One target's bid, lowered with only_lower: applied.
	res = owner.must(202, "POST", bid, map[string]any{"bid_micros": 8_000, "placement": "pub-17", "only_lower": true})
	if ch := apply(res); ch["status"] != "applied" || zeroparkFake.bidOf(cid+"|pub-17") != 0.008 {
		t.Fatalf("target bid: %v (network %v)", ch, zeroparkFake.bidOf(cid+"|pub-17"))
	}
	// only_lower refuses a raise: the change fails and the bid stays.
	res = owner.must(202, "POST", bid, map[string]any{"bid_micros": 20_000, "placement": "pub-17", "only_lower": true})
	if ch := apply(res); ch["status"] != "failed" || !strings.Contains(ch["error"].(string), "would not go down") || zeroparkFake.bidOf(cid+"|pub-17") != 0.008 {
		t.Fatalf("a raise under only_lower: %v (network %v)", ch, zeroparkFake.bidOf(cid+"|pub-17"))
	}

	// An agent: lowering with only_lower runs within its grant; a plain
	// bid change always waits for a person.
	agent, agentID := gatedAgent(t, owner, base, "Ads agent", "analytics.read", "ads.manage")
	owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "ads/campaigns/**", "mode": "run"})
	st, r := agent.do("POST", bid, map[string]any{"bid_micros": 9_000, "only_lower": true})
	if st != 202 || r["queued"] != true {
		t.Fatalf("an agent lowering a bid: %d %v", st, r)
	}
	st, r = agent.do("POST", bid, map[string]any{"bid_micros": 9_000, "placement": "pub-18"})
	if st != 202 || r["approval_required"] != true || r["action"].(map[string]any)["decided_by_rule"] != "always_person" {
		t.Fatalf("an agent's plain bid change: %d %v", st, r)
	}

	// A platform whose bids can't be set here answers 422.
	metaState.Lock()
	metaState.campaigns, metaState.posts, metaState.refuse = nil, nil, false
	metaState.Unlock()
	m := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	if _, err := e.ads.SyncOnce(ctx, ws, m["id"].(string)); err != nil {
		t.Fatal(err)
	}
	active, _ := metaCampaigns(t, e, ws)
	owner.must(422, "POST", bb+"/ads/campaigns/"+active+"/bid", map[string]any{"bid_micros": 12_000})
}
