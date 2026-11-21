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

// Manual bids below the campaign (plans/ad-tracker.md, B8: manual CPC): an
// ad group's default bid and a keyword's own are set as queued bid
// changes on a campaign that bids by hand; the job keeps the bid in effect
// before, and with only_lower refuses a raise. A campaign that bids
// automatically is refused, and an agent's change without only_lower
// waits for a person.
func TestAdUnitBids(t *testing.T) {
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
	googleSuffix.Lock()
	googleSuffix.mutates, googleSuffix.refuse, googleSuffix.groupBid, googleSuffix.keywordBid = nil, false, "", ""
	googleSuffix.Unlock()
	t.Cleanup(func() {
		googleSuffix.Lock()
		googleSuffix.groupBid, googleSuffix.keywordBid = "", ""
		googleSuffix.Unlock()
	})
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
	var camp, group, ad, kw string
	if err := e.pool.QueryRow(ctx, `SELECT c.id::text, g.id::text, a.id::text, k.id::text FROM ad_entities c JOIN ad_entities g ON g.parent_id = c.id
		JOIN ad_entities a ON a.parent_id = g.id AND a.level = 'ad' JOIN ad_keywords k ON k.ad_group_id = g.id
		WHERE c.workspace_id = $1 AND c.external_id = '7' AND g.external_id = '71' AND k.external_id = '91'`, ws).Scan(&camp, &group, &ad, &kw); err != nil {
		t.Fatal(err)
	}
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
	bidOf := func(ch map[string]any, side string) float64 {
		b, _ := ch[side].(map[string]any)["bid"].(map[string]any)
		v, _ := b["micros"].(float64)
		return v
	}
	groupBid, keywordBid := bb+"/ads/items/"+group+"/bid", bb+"/ads/keywords/"+kw+"/bid"

	// The campaign bids automatically: a bid by hand would be ignored.
	if st, body := owner.do("POST", groupBid, map[string]any{"bid_micros": 1_200_000}); st != 409 || !strings.Contains(body["error"].(map[string]any)["message"].(string), "bids automatically") {
		t.Fatalf("on a campaign that bids automatically: %d %v", st, body)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE ad_entities SET bidding_strategy = 'manual_cpc' WHERE id = $1`, camp); err != nil {
		t.Fatal(err)
	}
	// Refused before anything is queued.
	owner.must(400, "POST", groupBid, map[string]any{})
	owner.must(400, "POST", groupBid, map[string]any{"bid_micros": 0})
	owner.must(400, "POST", groupBid, map[string]any{"bid_micros": 5_000_000_000})
	owner.must(400, "POST", bb+"/ads/items/"+ad+"/bid", map[string]any{"bid_micros": 1_200_000})
	owner.must(404, "POST", bb+"/ads/keywords/"+group+"/bid", map[string]any{"bid_micros": 1_200_000})

	// The ad group's default bid, 1.50 on Google, set to 1.20 by a person.
	res := owner.must(202, "POST", groupBid, map[string]any{"bid_micros": 1_200_000})
	ch := res["change"].(map[string]any)
	if ch["kind"] != "bid" || ch["campaign_id"] != camp || ch["after"].(map[string]any)["ad_group_id"] != "71" || bidOf(ch, "after") != 1_200_000 {
		t.Fatalf("queued ad group bid: %v", ch)
	}
	owner.must(409, "POST", groupBid, map[string]any{"bid_micros": 1_300_000})
	if ch := apply(res); ch["status"] != "applied" || bidOf(ch, "before") != 1_500_000 || ch["before"].(map[string]any)["ad_group_id"] != "71" {
		t.Fatalf("applied ad group bid: %v", ch)
	}

	// The keyword has no bid of its own, so it bids its ad group's 1.20.
	// Lowered to 1.00 with only_lower: applied.
	res = owner.must(202, "POST", keywordBid, map[string]any{"bid_micros": 1_000_000, "only_lower": true})
	if a := res["change"].(map[string]any)["after"].(map[string]any); a["keyword_id"] != "91" || a["ad_group_id"] != "71" || a["keyword"] != "acme crm" || a["match_type"] != "exact" {
		t.Fatalf("queued keyword bid: %v", a)
	}
	if ch := apply(res); ch["status"] != "applied" || bidOf(ch, "before") != 1_200_000 {
		t.Fatalf("applied keyword bid: %v", ch)
	}
	// only_lower refuses a raise, and the bid stays.
	res = owner.must(202, "POST", keywordBid, map[string]any{"bid_micros": 1_400_000, "only_lower": true})
	if ch := apply(res); ch["status"] != "failed" || !strings.Contains(ch["error"].(string), "would not go down") {
		t.Fatalf("a raise under only_lower: %v", ch)
	}
	googleSuffix.Lock()
	mutates, gb, kb := strings.Join(googleSuffix.mutates, "\n"), googleSuffix.groupBid, googleSuffix.keywordBid
	googleSuffix.Unlock()
	if gb != "1200000" || kb != "1000000" || !strings.Contains(mutates, `"resourceName":"customers/123/adGroups/71"`) ||
		!strings.Contains(mutates, `"resourceName":"customers/123/adGroupCriteria/71~91"`) || !strings.Contains(mutates, `"updateMask":"cpc_bid_micros"`) {
		t.Fatalf("Google holds %s and %s after:\n%s", gb, kb, mutates)
	}

	// An agent: lowering with only_lower runs within its grant; a plain
	// bid change always waits for a person.
	agent, agentID := gatedAgent(t, owner, base, "Ads agent", "analytics.read", "ads.manage")
	owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "ads/**", "mode": "run"})
	st, r := agent.do("POST", groupBid, map[string]any{"bid_micros": 1_100_000, "only_lower": true})
	if st != 202 || r["queued"] != true {
		t.Fatalf("an agent lowering an ad group's bid: %d %v", st, r)
	}
	st, r = agent.do("POST", keywordBid, map[string]any{"bid_micros": 900_000})
	if st != 202 || r["approval_required"] != true || r["action"].(map[string]any)["decided_by_rule"] != "always_person" {
		t.Fatalf("an agent's plain keyword bid: %d %v", st, r)
	}

	// A platform without bids by hand here answers 422.
	metaState.Lock()
	metaState.campaigns, metaState.posts, metaState.refuse = nil, nil, false
	metaState.Unlock()
	m := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	if _, err := e.ads.SyncOnce(ctx, ws, m["id"].(string)); err != nil {
		t.Fatal(err)
	}
	var set string
	if err := e.pool.QueryRow(ctx, `SELECT id::text FROM ad_entities WHERE workspace_id = $1 AND level = 'ad_group' AND external_id = '31'`, ws).Scan(&set); err != nil {
		t.Fatal(err)
	}
	owner.must(422, "POST", bb+"/ads/items/"+set+"/bid", map[string]any{"bid_micros": 1_200_000})

	n := 0
	for _, ev := range got() {
		if ev.Name != "ad_campaign.bid_changed" {
			continue
		}
		n++
		for k, v := range ev.Props {
			if s, ok := v.(string); ok && strings.Contains(s, "acme") {
				t.Fatalf("the event names the keyword in %s", k)
			}
		}
	}
	if n != 4 {
		t.Fatalf("%d bid events", n)
	}
}
