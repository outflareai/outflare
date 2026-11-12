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

// New ads: an agent writes drafts under its grant; a person approves one,
// which the job makes on the platform paused, and turns it on. Google
// makes ads, ad groups and campaigns in one batch; Meta makes ads in an
// ad set.
func TestAdDrafts(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	googleSuffix.Lock()
	googleSuffix.mutates, googleSuffix.refuse = nil, false
	googleSuffix.Unlock()
	metaState.Lock()
	metaState.campaigns, metaState.posts, metaState.refuse, metaState.targeting = nil, nil, false, nil
	metaState.Unlock()

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
	brand := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	drafts := "/v1/brands/" + brand + "/ads/drafts"
	var acct, gcamp, ggroup string
	if err := e.pool.QueryRow(ctx, `SELECT a.id, c.id, g.id FROM ad_accounts a JOIN ad_entities c ON c.ad_account_id = a.id AND c.level = 'campaign' AND c.external_id = '7'
		JOIN ad_entities g ON g.parent_id = c.id AND g.level = 'ad_group' AND g.external_id = '71' WHERE a.workspace_id = $1`, ws).Scan(&acct, &gcamp, &ggroup); err != nil {
		t.Fatal(err)
	}
	run := func(id, step string) map[string]any {
		t.Helper()
		if err := e.ads.RunDraft(ctx, ws, id, step, false); err != nil {
			t.Fatal(err)
		}
		return owner.must(200, "GET", drafts+"/"+id, nil)
	}
	ad := map[string]any{"final_url": "https://acme.example/pricing", "headlines": []string{"Acme CRM", "Close deals faster", "Try it free"},
		"descriptions": []string{"The CRM small teams set up in a day.", "Free for 14 days, no card needed."}, "path1": "pricing"}
	with := func(m map[string]any, kv ...any) map[string]any {
		out := map[string]any{}
		for k, v := range m {
			out[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			out[kv[i].(string)] = kv[i+1]
		}
		return out
	}

	// The platform's rules are checked before anything is kept.
	owner.must(400, "POST", drafts, with(ad, "kind", "ad", "ad_group_id", ggroup, "headlines", []string{"Acme CRM", "Close deals"}))
	owner.must(400, "POST", drafts, with(ad, "kind", "ad", "ad_group_id", ggroup, "headlines", []string{"Acme CRM", "Close deals", "This headline is far too long for Google"}))
	owner.must(400, "POST", drafts, with(ad, "kind", "ad", "ad_group_id", ggroup, "final_url", "acme.example"))
	owner.must(400, "POST", drafts, with(ad, "kind", "ad_group", "campaign_id", gcamp, "name", "Pricing"))
	owner.must(400, "POST", drafts, with(ad, "kind", "campaign", "account_id", acct, "name", "Brand", "keywords", []any{map[string]any{"text": "acme crm"}}, "daily_budget_micros", 20_000_000))
	owner.must(400, "POST", drafts, with(ad, "kind", "banner", "ad_group_id", ggroup))
	owner.must(404, "POST", drafts, with(ad, "kind", "ad", "ad_group_id", gcamp))

	// An agent writes a draft under its grant, once a person has reviewed
	// the brand's knowledge sheet the ads are written from
	// (TestBrandKnowledgeGate has the rest of the gate).
	agent, agentID := gatedAgent(t, owner, base, "Ads writer", "analytics.read", "ads.manage")
	owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "brands/*/ads/**", "mode": "run"})
	if st, res := agent.do("POST", drafts, with(ad, "kind", "ad", "ad_group_id", ggroup, "reason", "The group's only ad is two years old.")); st != 422 || res["error"].(map[string]any)["code"] != "knowledge_unreviewed" {
		t.Fatalf("draft from an unreviewed sheet: %d %v", st, res)
	}
	owner.must(200, "POST", "/v1/brands/"+brand+"/knowledge/review", nil)
	st, d := agent.do("POST", drafts, with(ad, "kind", "ad", "ad_group_id", ggroup, "reason", "The group's only ad is two years old."))
	if st != 201 || d["status"] != "draft" || d["kind"] != "ad" || d["campaign_id"] != gcamp || d["action_id"] == nil {
		t.Fatalf("agent draft: %d %v", st, d)
	}
	id := d["id"].(string)
	// Approving and turning on are a person's.
	if st, res := agent.do("POST", drafts+"/"+id+"/approve", nil); st == 202 && res["status"] == "creating" {
		t.Fatalf("an agent approved a new ad: %v", res)
	}
	// A person edits it, then approves: made paused, then turned on.
	d = owner.must(200, "PATCH", drafts+"/"+id, with(ad, "headlines", []string{"Acme CRM", "Close deals faster", "Start free today"}))
	if d["content"].(map[string]any)["headlines"].([]any)[2] != "Start free today" || d["reason"] != "The group's only ad is two years old." {
		t.Fatalf("edited: %v", d)
	}
	d = owner.must(202, "POST", drafts+"/"+id+"/approve", nil)
	if d["status"] != "creating" || d["approved_by"] == nil {
		t.Fatalf("approved: %v", d)
	}
	owner.must(409, "PATCH", drafts+"/"+id, ad)
	d = run(id, adsync.DraftCreate)
	made := d["made"].(map[string]any)
	if d["status"] != "paused" || made["ad_id"] != "91" || made["ad_group_id"] != "71" || len(made["made"].([]any)) != 1 {
		t.Fatalf("made: %v", d)
	}
	owner.must(202, "POST", drafts+"/"+id+"/go-live", nil)
	if d = run(id, adsync.DraftGoLive); d["status"] != "live" || d["live_at"] == nil {
		t.Fatalf("live: %v", d)
	}

	// A new campaign, in one batch: budget, campaign, country, ad group,
	// keywords and ad, everything paused; turning it on enables all three.
	d = owner.must(201, "POST", drafts, with(ad, "kind", "campaign", "account_id", acct, "name", "Acme brand", "keywords", []any{map[string]any{"text": "acme crm", "match_type": "exact"}, map[string]any{"text": "acme pricing"}},
		"daily_budget_micros", 20_000_000, "countries", []string{"nz"}, "bidding", map[string]any{"strategy": "maximize_conversions", "target_cpa_micros": 30_000_000}))
	cid := d["id"].(string)
	owner.must(202, "POST", drafts+"/"+cid+"/approve", nil)
	d = run(cid, adsync.DraftCreate)
	made = d["made"].(map[string]any)
	if d["status"] != "paused" || made["campaign_id"] != "7002" || made["ad_group_id"] != "73" || made["ad_id"] != "91" || len(made["made"].([]any)) != 3 {
		t.Fatalf("campaign made: %v", d)
	}
	owner.must(202, "POST", drafts+"/"+cid+"/go-live", nil)
	run(cid, adsync.DraftGoLive)
	googleSuffix.Lock()
	mutates := strings.Join(googleSuffix.mutates, "\n")
	googleSuffix.Unlock()
	for _, want := range []string{`"adGroup":"customers/123/adGroups/71"`, `"status":"PAUSED"`, `"text":"Start free today"`, `"path1":"pricing"`,
		`"amountMicros":"20000000"`, `"advertisingChannelType":"SEARCH"`, `"maximizeConversions":{"targetCpaMicros":"30000000"}`,
		`"campaign":"customers/123/campaigns/-2"`, `"geoTargetConstant":"geoTargetConstants/2554"`, `"matchType":"EXACT"`, `"matchType":"PHRASE"`,
		`"resourceName":"customers/123/adGroupAds/71~91","status":"ENABLED"`, `"resourceName":"customers/123/campaigns/7002","status":"ENABLED"`,
		`"resourceName":"customers/123/adGroups/73","status":"ENABLED"`} {
		if !strings.Contains(mutates, want) {
			t.Errorf("google mutates lack %s:\n%s", want, mutates)
		}
	}
	if strings.Contains(mutates, `"resourceName":"customers/123/adGroups/71","status":"ENABLED"`) {
		t.Error("turning on a new ad turned on the ad group it went in")
	}

	// A draft can be dismissed until it is made.
	d = owner.must(201, "POST", drafts, with(ad, "kind", "ad_group", "campaign_id", gcamp, "name", "Pricing", "keywords", []any{map[string]any{"text": "crm pricing"}}))
	owner.must(200, "POST", drafts+"/"+d["id"].(string)+"/dismiss", nil)
	owner.must(409, "POST", drafts+"/"+d["id"].(string)+"/approve", nil)
	owner.must(409, "POST", drafts+"/"+id+"/dismiss", nil)
	if l := owner.must(200, "GET", drafts+"?status=live", nil)["data"].([]any); len(l) != 2 {
		t.Fatalf("live drafts: %v", l)
	}
	owner.must(400, "GET", drafts+"?status=maybe", nil)

	// Meta: an ad with an image in the campaign's ad set, as the
	// account's page.
	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	if _, err := e.ads.SyncOnce(ctx, ws, i["id"].(string)); err != nil {
		t.Fatal(err)
	}
	var mset string
	if err := e.pool.QueryRow(ctx, `SELECT id FROM ad_entities WHERE workspace_id = $1 AND level = 'ad_group' AND external_id = '31'`, ws).Scan(&mset); err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{"kind": "ad", "ad_group_id": mset, "final_url": "https://acme.example/demo", "headlines": []string{"See Acme in 5 minutes"},
		"primary_text": "Small teams close deals faster with Acme.", "image_url": "https://acme.example/banner.png", "call_to_action": "sign_up"}
	owner.must(400, "POST", drafts, with(meta, "image_url", ""))
	owner.must(400, "POST", drafts, with(meta, "kind", "ad_group"))
	d = owner.must(201, "POST", drafts, meta)
	owner.must(202, "POST", drafts+"/"+d["id"].(string)+"/approve", nil)
	if d = run(d["id"].(string), adsync.DraftCreate); d["status"] != "paused" || d["made"].(map[string]any)["ad_id"] != "42" {
		t.Fatalf("meta made: %v", d)
	}
	owner.must(202, "POST", drafts+"/"+d["id"].(string)+"/go-live", nil)
	run(d["id"].(string), adsync.DraftGoLive)
	metaState.Lock()
	posts := strings.Join(metaState.posts, "\n")
	metaState.Unlock()
	for _, want := range []string{`POST act_55/adcreatives`, `\"page_id\":\"880\"`, `\"picture\":\"https://acme.example/banner.png\"`, `\"type\":\"SIGN_UP\"`,
		`POST act_55/ads {"adset_id":"31","creative":"{\"creative_id\":\"551\"}"`, `"status":"PAUSED"`, `POST 42 {"status":"ACTIVE"}`} {
		if !strings.Contains(posts, want) {
			t.Errorf("meta posts lack %s:\n%s", want, posts)
		}
	}

	evs := got()
	for name, want := range map[string]int{"ad_draft.written": 4, "ad_draft.edited": 1, "ad_draft.approved": 3, "ad_draft.launched": 3, "ad_draft.dismissed": 1,
		"ad_draft.made": 3, "ad_draft.went_live": 3} {
		if n := len(named(evs, name)); n != want {
			t.Errorf("%s: %d events, want %d", name, n, want)
		}
	}
}
