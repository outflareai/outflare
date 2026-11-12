package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The knowledge sheet, reviewed: a read that can't run says so instead
// of leaving the sheet silently empty, a person (and only a person) signs
// the sheet off, and a read that changes what the brand sells clears the
// review.
func TestBrandKnowledgeReview(t *testing.T) {
	e := freshEnv(t)
	events := track.Capture(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	// A brand created with a website and no page reader: nothing can be
	// read, and the sheet says so.
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "website": "northpeak.example"})
	var bid string
	if err := e.pool.QueryRow(ctx, `SELECT id::text FROM brands WHERE workspace_id = $1 AND handle = 'northpeak'`, ws).Scan(&bid); err != nil {
		t.Fatal(err)
	}
	sheet := "/v1/brands/" + bid + "/knowledge"
	k := owner.must(200, "GET", sheet, nil)
	if k["read_error"] != "connector_needed" || k["trusted"] != false || k["reviewed_at"] != nil || k["refresh_daily"] != false {
		t.Fatalf("sheet without a page reader: %v", k)
	}
	failed := false
	for _, ev := range events() {
		failed = failed || ev.Name == "brand.knowledge_read_failed" && ev.ErrorCode == "connector_needed"
	}
	if !failed {
		t.Fatal("no brand.knowledge_read_failed event with connector_needed")
	}

	// With a page reader but no AI model, the read records that too.
	var bodies []map[string]any
	judge := `{"summary":"Northpeak sells residential proxies to data teams.","industry":"Proxy networks",
		"lists":{"products":[{"text":"Residential proxy plans from $49 a month","page":1}],"services":[],"features":[],"integrations":[],"capabilities":[],
		"audiences":[{"text":"Data teams","page":1}],"use_cases":[]}}`
	site := httptest.NewServer(geonodeJobs(nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/usage/concurrency" {
			io.WriteString(w, `{"work_concurrency_in_use": 0, "work_concurrency_limit": 5}`)
			return
		}
		var req struct {
			URL string `json:"url"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		page, status := "", 404
		if req.URL == "https://northpeak.example/" {
			page, status = `<html><head><title>Northpeak</title></head><body><p>Northpeak sells residential proxies to data teams from $49 a month.</p></body></html>`, 200
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"html": page}, "metadata": map[string]any{"http_status": status, "url": req.URL}, "tokens_charged": 1})
	}))
	t.Cleanup(site.Close)
	e.integrations.Client.BaseURLs["geonode"] = site.URL
	or := httptest.NewServer(fakeOpenRouter{bodies: &bodies, judge: &judge})
	t.Cleanup(or.Close)
	e.integrations.Client.BaseURLs["openrouter"] = or.URL
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "geonode", "credentials": map[string]string{"api_key": "gn-key"}})
	if err := e.srv.ReadBrandKnowledge(ctx, ws, bid); err != nil {
		t.Fatal(err)
	}
	if k = owner.must(200, "GET", sheet, nil); k["read_error"] != "ai_model_needed" || k["trusted"] != false {
		t.Fatalf("sheet without an AI model: %v", k)
	}

	// Only a person reviews the sheet; an agent is refused even with a
	// grant that lets its writes run.
	agent, agentID := gatedAgent(t, owner, base, "Writer", "workspace.read", "context.manage")
	owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "brands/*/knowledge/**", "mode": "run"})
	if st, res := agent.do("POST", sheet+"/review", nil); st != 403 {
		t.Fatalf("an agent reviewed the sheet: %d %v", st, res)
	}
	k = owner.must(200, "POST", sheet+"/review", nil)
	if k["reviewed_at"] == nil || k["reviewed_by"] == nil || k["trusted"] != false {
		t.Fatalf("reviewed while the read fails: %v", k)
	}
	if !containsEvent(events(), "brand.knowledge_reviewed") {
		t.Fatal("no brand.knowledge_reviewed event")
	}
	// The brand's context carries the same, so agents see it.
	c := owner.must(200, "GET", base+"/brands/northpeak/context", nil)
	if c["knowledge_reviewed_at"] == nil || c["knowledge_trusted"] != false || c["knowledge_error"] != "ai_model_needed" {
		t.Fatalf("context: %v", c)
	}

	// A read that works clears the error, and since it changed what the
	// brand sells (nothing before, a product now) it clears the review.
	conn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "openrouter", "credentials": map[string]string{"api_key": "sk-or-good"}})["id"].(string)
	owner.must(200, "PUT", base+"/ai-settings", map[string]any{"integration_id": conn, "model": "openai/gpt-5"})
	if err := e.srv.ReadBrandKnowledge(ctx, ws, bid); err != nil {
		t.Fatal(err)
	}
	k = owner.must(200, "GET", sheet, nil)
	if k["read_error"] != "" || k["reviewed_at"] != nil || k["changed_since_review"] != true || k["changed_at"] == nil || k["trusted"] != false {
		t.Fatalf("after a read that changed the offer: %v", k)
	}
	if k = owner.must(200, "POST", sheet+"/review", nil); k["trusted"] != true || k["changed_since_review"] != false {
		t.Fatalf("reviewed: %v", k)
	}
	// The same answer again changes nothing; a new price clears the review.
	if err := e.srv.ReadBrandKnowledge(ctx, ws, bid); err != nil {
		t.Fatal(err)
	}
	if k = owner.must(200, "GET", sheet, nil); k["trusted"] != true {
		t.Fatalf("an unchanged read cleared the review: %v", k)
	}
	judge = strings.Replace(judge, "$49", "$59", 1)
	if err := e.srv.ReadBrandKnowledge(ctx, ws, bid); err != nil {
		t.Fatal(err)
	}
	if k = owner.must(200, "GET", sheet, nil); k["trusted"] != false || k["changed_since_review"] != true {
		t.Fatalf("a new price kept the review: %v", k)
	}
}

// The gate on new ads: connecting an ad account to a brand turns on the
// daily read and raises the blocker until a person reviews the sheet;
// the draft writer refuses meanwhile (a person may override, an agent
// can't), and every draft carries its checks.
func TestBrandKnowledgeGate(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
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
	brand := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	sheet := "/v1/brands/" + brand + "/knowledge"
	drafts := "/v1/brands/" + brand + "/ads/drafts"
	var acct, ggroup string
	if err := e.pool.QueryRow(ctx, `SELECT a.id, g.id FROM ad_accounts a JOIN ad_entities c ON c.ad_account_id = a.id AND c.level = 'campaign' AND c.external_id = '7'
		JOIN ad_entities g ON g.parent_id = c.id AND g.level = 'ad_group' AND g.external_id = '71' WHERE a.workspace_id = $1`, ws).Scan(&acct, &ggroup); err != nil {
		t.Fatal(err)
	}
	// A new account takes the workspace's only brand; start from one with
	// none, as an account of a workspace with several brands is.
	if _, err := e.pool.Exec(ctx, `UPDATE ad_accounts SET brand_id = NULL WHERE id = $1`, acct); err != nil {
		t.Fatal(err)
	}
	blocker := func() map[string]any {
		t.Helper()
		for _, b := range owner.must(200, "GET", base+"/status", nil)["blockers"].([]any) {
			if b := b.(map[string]any); b["code"] == "ads_knowledge_unreviewed" {
				return b
			}
		}
		return nil
	}

	// No ad account on the brand: no blocker, and the website isn't read
	// daily.
	if blocker() != nil {
		t.Fatal("blocker without an ad account")
	}
	// Connecting the account to the brand turns the daily read on, queues
	// the first read and raises the blocker: nobody has reviewed yet.
	owner.must(200, "PATCH", base+"/ads/accounts/"+acct, map[string]any{"brand": brand})
	k := owner.must(200, "GET", sheet, nil)
	if k["refresh_daily"] != true || k["trusted"] != false {
		t.Fatalf("sheet after connecting an ad account: %v", k)
	}
	var queued int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'brand.read_knowledge' AND workspace_id = $1`, ws).Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("reads queued: %d %v", queued, err)
	}
	b := blocker()
	if b == nil || b["severity"] != "warning" || b["area"] != "ads" || b["count"] != 1.0 {
		t.Fatalf("blocker: %v", b)
	}
	if fix := b["fix"].(map[string]any); fix["tool"] != "review_brand_knowledge" || fix["who"] != "person" || !strings.Contains(fix["page"].(string), "ads") {
		t.Fatalf("fix: %v", fix)
	}
	if items := b["items"].([]any); len(items) != 1 || items[0].(map[string]any)["id"] != brand || !strings.Contains(items[0].(map[string]any)["reason"].(string), "nobody has reviewed") {
		t.Fatalf("items: %v", items)
	}
	// The hourly sweep turns the daily read on for accounts that got
	// their brand another way.
	if _, err := e.pool.Exec(ctx, `UPDATE brand_context SET knowledge_refresh_daily = false WHERE brand_id = $1`, brand); err != nil {
		t.Fatal(err)
	}
	if _, err := e.srv.QueueDueBrandKnowledge(ctx); err != nil {
		t.Fatal(err)
	}
	if k = owner.must(200, "GET", sheet, nil); k["refresh_daily"] != true {
		t.Fatalf("the sweep left the daily read off: %v", k)
	}

	// The sheet: one product with its price, and a tracked competitor.
	owner.must(200, "PUT", base+"/brands/"+brand+"/knowledge", map[string]any{"summary": "Acme is a CRM for small teams.", "industry": "CRM", "pages": []string{},
		"lists": map[string]any{"products": []map[string]any{{"text": "Acme CRM plans from $49 a month", "url": ""}}}})
	if _, err := e.pool.Exec(ctx, `WITH s AS (INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token) VALUES ($1, $2, 'acme.example', 'https://acme.example/', 'tok') RETURNING id)
		INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, name, aliases, status) SELECT $1, id, $2, 'rivalcorp.example', 'Rival Corp', '{Rival}', 'tracked' FROM s`, ws, brand); err != nil {
		t.Fatal(err)
	}
	// A second enabled ad in the ad group, beside the one the sync found.
	if _, err := e.pool.Exec(ctx, `INSERT INTO ad_entities (workspace_id, ad_account_id, level, parent_id, external_id, name, status, provider_status)
		VALUES ($1, $2, 'ad', $3, '82', 'Second ad', 'active', 'ENABLED')`, ws, acct, ggroup); err != nil {
		t.Fatal(err)
	}

	ad := func(headlines []string, kv ...any) map[string]any {
		m := map[string]any{"kind": "ad", "ad_group_id": ggroup, "final_url": "https://acme.example/pricing", "headlines": headlines,
			"descriptions": []string{"The CRM small teams set up in a day.", "Free for 14 days, no card needed."}}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	plain := []string{"Acme CRM", "Close deals faster", "Try it free"}
	agent, agentID := gatedAgent(t, owner, base, "Ads writer", "analytics.read", "ads.manage")
	owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "brands/*/ads/**", "mode": "run"})

	// Unreviewed: the agent is refused, with or without the override; a
	// person can override for one draft.
	for _, body := range []map[string]any{ad(plain), ad(plain, "override_knowledge", true)} {
		if st, res := agent.do("POST", drafts, body); st != 422 || res["error"].(map[string]any)["code"] != "knowledge_unreviewed" {
			t.Fatalf("agent draft from an unreviewed sheet: %d %v", st, res)
		}
	}
	if st, res := owner.do("POST", drafts, ad(plain)); st != 422 {
		t.Fatalf("person's draft from an unreviewed sheet without override: %d %v", st, res)
	}
	d := owner.must(201, "POST", drafts, ad(plain, "override_knowledge", true))
	codes := func(d map[string]any) map[string]string {
		out := map[string]string{}
		for _, c := range d["checks"].([]any) {
			c := c.(map[string]any)
			out[c["code"].(string)] = c["level"].(string)
		}
		return out
	}
	// Three plain headlines: too few, and the ad group is crowded.
	if got := codes(d); got["few_headlines"] != "warning" || got["rsa_crowded"] != "warning" || len(got) != 2 {
		t.Fatalf("checks on a plain draft: %v", d["checks"])
	}

	// Reviewed: the blocker goes and the agent writes.
	owner.must(200, "POST", sheet+"/review", nil)
	if blocker() != nil {
		t.Fatal("blocker after the review")
	}
	st, d := agent.do("POST", drafts, ad([]string{"Acme CRM", "Save 30% today", "The Best CRM", "Beats Rival Corp", "Close Deals Faster Today", "A headline that is long"},
		"headline_pins", []int{1, 1, 2, 0, 0, 0}, "reason", "Checks."))
	if st != 201 {
		t.Fatalf("agent draft after the review: %d %v", st, d)
	}
	got := codes(d)
	for _, code := range []string{"price_not_in_sheet", "superlative", "competitor_named", "few_headlines", "pins_thin", "title_case", "rsa_crowded"} {
		if got[code] != "warning" {
			t.Errorf("no %s warning: %v", code, d["checks"])
		}
	}
	if got["long_headline"] != "note" {
		t.Errorf("no long_headline note: %v", d["checks"])
	}
	// A draft that follows the guidance has no checks: a new campaign (no
	// crowded group), 11 short sentence-case headlines, the sheet's price,
	// nothing pinned. Rewriting it keeps the checks current.
	clean := []string{"Acme CRM", "CRM for small teams", "From $49 a month", "Set up in a day", "Free for 14 days", "No card needed", "Close deals faster",
		"See every deal", "Try Acme free", "Built for teams", "Start today"}
	d = owner.must(201, "POST", drafts, map[string]any{"kind": "campaign", "account_id": acct, "name": "Acme brand", "final_url": "https://acme.example/pricing", "headlines": clean,
		"descriptions": []string{"The CRM small teams set up in a day.", "Free for 14 days, no card needed."}, "keywords": []any{map[string]any{"text": "acme crm"}},
		"daily_budget_micros": 20_000_000, "countries": []string{"nz"}})
	if len(d["checks"].([]any)) != 0 {
		t.Fatalf("checks on a clean draft: %v", d["checks"])
	}
	d = owner.must(200, "PATCH", drafts+"/"+d["id"].(string), map[string]any{"name": "Acme brand", "final_url": "https://acme.example/pricing", "headlines": append(clean[:10:10], "Cheapest Deals Around Here"),
		"descriptions": []string{"The CRM small teams set up in a day.", "Free for 14 days, no card needed."}, "keywords": []any{map[string]any{"text": "acme crm"}},
		"daily_budget_micros": 20_000_000, "countries": []string{"nz"}})
	if got := codes(d); got["superlative"] != "warning" || got["title_case"] != "warning" {
		t.Fatalf("checks after a rewrite: %v", d["checks"])
	}
	// Pins that don't match the headlines are Google's rule, so a refusal.
	owner.must(400, "POST", drafts, ad(plain, "headline_pins", []int{1}))

	// A daily read that changed what the brand sells clears the review:
	// the blocker is back and the agent is refused again.
	if _, err := e.pool.Exec(ctx, `UPDATE brand_context SET knowledge_changed_at = now(), knowledge_reviewed_at = NULL, knowledge_reviewed_by = NULL WHERE brand_id = $1`, brand); err != nil {
		t.Fatal(err)
	}
	if b := blocker(); b == nil || !strings.Contains(b["items"].([]any)[0].(map[string]any)["reason"].(string), "changed what the brand sells") {
		t.Fatalf("blocker after a change: %v", b)
	}
	if st, res := agent.do("POST", drafts, ad(clean)); st != 422 {
		t.Fatalf("agent draft after a change: %d %v", st, res)
	}
}
