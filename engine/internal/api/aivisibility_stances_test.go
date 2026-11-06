package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/aivis"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Stances: after a run, the workspace's model reads how each answer treats
// the brands it names, the untracked brands it recommends (suggested
// competitors) and what kind of site each cited page is on.
func TestAIVisibilityStances(t *testing.T) {
	e := freshEnv(t)
	events := track.Capture(t)
	var bodies []map[string]any
	judge := `{"companies":[{"company":1,"stance":"listed","quote":"Acme (acme.example) is cheaper","attributes":[{"text":"Cheapest plans.","sentiment":"positive"},{"text":"cheapest plans","sentiment":"positive"},{"text":"","sentiment":"neutral"},{"text":"US only","sentiment":"bad"}],"objections":[{"text":"No static IPs","quote":"Acme has no static IPs"}]},{"company":2,"stance":"recommended","quote":"Northpeak is the best known","attributes":[{"text":"Best known","sentiment":"positive"}],"objections":[]},{"company":9,"stance":"warned","quote":"x","attributes":[],"objections":[]},{"company":1,"stance":"warned","quote":"dup","attributes":[],"objections":[]}],
		"other_brands":[{"name":"Lumen  Proxies","website":"https://www.lumenproxies.example/pricing"},"acme","NORTHPEAK"," ",{"name":"lumen proxies","website":""}],
		"sources":[{"source":1,"type":"review"},{"source":5,"type":"news"}]}`
	or := httptest.NewServer(fakeOpenRouter{bodies: &bodies, judge: &judge})
	t.Cleanup(or.Close)
	e.integrations.Client.BaseURLs["openrouter"] = or.URL
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "website": "acme.example"})
	owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.acme.example/", "brand_id": "acme"})
	conn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "openrouter", "credentials": map[string]string{"api_key": "sk-or-good"}})["id"].(string)
	owner.must(200, "PUT", base+"/ai-settings", map[string]any{"integration_id": conn, "model": "openai/gpt-5"})
	owner.must(200, "PUT", base+"/ai-visibility/settings?brand=acme", map[string]any{"models": []map[string]any{{"integration_id": conn, "model": "openai/gpt-5"}}})
	cid := owner.must(201, "POST", base+"/ai-cohorts", map[string]any{"name": "Proxy buyers", "brand_id": "acme",
		"competitors": []map[string]any{{"name": "Northpeak", "domains": []string{"northpeak.example"}}}})["id"].(string)
	owner.must(201, "POST", base+"/ai-cohorts/"+cid+"/prompts", map[string]any{"prompts": []map[string]any{{"text": "What is the best proxy provider?"}, {"text": "Which residential proxies are best?"}}})

	svc := &aivis.Service{Pool: e.pool, Models: e.srv.AIVisibilityModels(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rid := owner.must(201, "POST", base+"/ai-cohorts/"+cid+"/runs", nil)["id"].(string)
	if err := svc.Step(ctx, ws, rid, 0); err != nil {
		t.Fatal(err)
	}
	// The reader saw the brand first, then the competitor, and the page
	// that is neither's.
	var read string
	for _, b := range bodies {
		if u := b["messages"].([]any)[1].(map[string]any)["content"].(string); b["response_format"] != nil && strings.Contains(u, "1. Acme") {
			read = u
		}
	}
	if !strings.Contains(read, "1. Acme\n2. Northpeak") || !strings.Contains(read, "Source pages:\n1. https://review.example/proxies") || strings.Contains(read, "acme.example/pricing") {
		t.Fatalf("read: %s", read)
	}
	s := owner.must(200, "GET", base+"/ai-samples?run_id="+rid+"&brand_named=true", nil)["data"].([]any)[0].(map[string]any)
	if s["stance_status"] != "judged" || strings.Join(toStrings(s["other_brands"]), ",") != "Lumen Proxies" {
		t.Fatalf("sample: %v", s)
	}
	stance := map[string]string{}
	for _, m := range s["mentions"].([]any) {
		m := m.(map[string]any)
		stance[m["name"].(string)] = m["stance"].(string)
	}
	if stance["Acme"] != "listed" || stance["Northpeak"] != "recommended" {
		t.Fatalf("stances: %v", s["mentions"])
	}
	types := map[string]any{}
	for _, c := range s["citations"].([]any) {
		c := c.(map[string]any)
		types[c["host"].(string)] = c["source_type"]
	}
	if types["review.example"] != "review" || types["acme.example"] != "brand" {
		t.Fatalf("source types: %v", s["citations"])
	}

	ov := owner.must(200, "GET", base+"/ai-visibility", nil)["data"].([]any)[0].(map[string]any)
	brand := ov["brand"].(map[string]any)
	comp := ov["competitors"].([]any)[0].(map[string]any)
	sug := ov["suggested_competitors"].([]any)
	if brand["listed"] != 1.0 || brand["recommended"] != 0.0 || brand["recommend_rate"] != 0.0 || comp["recommended"] != 1.0 || comp["listed"] != 1.0 || comp["recommend_rate"] != 0.5 ||
		len(sug) != 1 || sug[0].(map[string]any)["name"] != "Lumen Proxies" || sug[0].(map[string]any)["answers"] != 2.0 || sug[0].(map[string]any)["host"] != "lumenproxies.example" {
		t.Fatalf("overview: %v", ov)
	}
	// A brand the answers showed a website for is on the brand's one
	// competitor list as a suggestion, to track or dismiss there.
	var cstatus, csource, cname string
	if err := e.pool.QueryRow(ctx, `SELECT status, source, name FROM seo_competitors WHERE workspace_id = $1 AND host = 'lumenproxies.example'`, ws).Scan(&cstatus, &csource, &cname); err != nil {
		t.Fatal(err)
	}
	if cstatus != "suggested" || csource != "ai_visibility" || cname != "Lumen Proxies" || sug[0].(map[string]any)["competitor_id"] == nil {
		t.Fatalf("competitor list: %s %s %s %v", cstatus, csource, cname, sug[0])
	}
	if !slices.ContainsFunc(events(), func(ev track.Event) bool {
		return ev.Name == "seo_competitors.suggested" && ev.Props["source"] == "ai_visibility"
	}) {
		t.Fatalf("no seo_competitors.suggested event")
	}
	// The score: Acme is listed on one question and left out on the other
	// (0.6 and 0, so 30); Northpeak recommended and listed (1 and 0.6, so 80).
	grid := ov["grid"].([]any)
	if brand["score"] != 30.0 || comp["score"] != 80.0 || brand["score_low"].(float64) >= 30 || brand["score_high"].(float64) <= 30 ||
		len(grid) != 1 || grid[0].(map[string]any)["questions"] != 2.0 || grid[0].(map[string]any)["score"] != 30.0 {
		t.Fatalf("score: %v %v %v", brand, comp, grid)
	}
	if wk := ov["weeks"].([]any); len(wk) != 1 || wk[0].(map[string]any)["score"] != 30.0 || wk[0].(map[string]any)["real_change"] != false {
		t.Fatalf("weeks: %v", wk)
	}
	st := map[string]any{}
	for _, x := range ov["source_types"].([]any) {
		st[x.(map[string]any)["type"].(string)] = x.(map[string]any)["answers"]
	}
	if st["review"] != 2.0 || st["brand"] != 1.0 {
		t.Fatalf("source types: %v", ov["source_types"])
	}

	// What AI answers brought the brand's site: visits from assistants and
	// the payment credited to them; other brands' and channels' don't count.
	one := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	acme := one(`SELECT brand_id::text FROM ai_cohorts WHERE id = $1`, cid)

	// By question: Acme is listed on the first question and left out of
	// the second; Northpeak is recommended on one and listed on the other.
	// Nothing was asked in the period before.
	qs := owner.must(200, "GET", "/v1/brands/"+acme+"/ai-visibility/questions?days=7", nil)["data"].([]any)
	if len(qs) != 2 {
		t.Fatalf("questions: %v", qs)
	}
	byText := map[string]map[string]any{}
	for _, q := range qs {
		q := q.(map[string]any)
		byText[q["text"].(string)] = q
	}
	q1, q2 := byText["What is the best proxy provider?"], byText["Which residential proxies are best?"]
	if q1 == nil || q2 == nil {
		t.Fatalf("questions: %v", qs)
	}
	var listed, absent map[string]any
	if q1["named"] == 1.0 {
		listed, absent = q1, q2
	} else {
		listed, absent = q2, q1
	}
	if listed["answers"] != 1.0 || listed["listed"] != 1.0 || listed["recommended"] != 0.0 || listed["score"] != 60.0 || listed["mention_rate"] != 1.0 || listed["avg_position"] == nil ||
		listed["previous"] != nil || listed["change"] != nil || listed["status"] != "active" {
		t.Fatalf("listed question: %v", listed)
	}
	if absent["answers"] != 1.0 || absent["named"] != 0.0 || absent["score"] != 0.0 || absent["mention_rate"] != 0.0 || absent["avg_position"] != nil {
		t.Fatalf("absent question: %v", absent)
	}
	comps := listed["competitors"].([]any)
	if len(comps) != 1 || comps[0].(map[string]any)["name"] != "Northpeak" || comps[0].(map[string]any)["answers"] != 1.0 {
		t.Fatalf("competitors: %v", comps)
	}
	others := listed["other_brands"].([]any)
	if len(others) != 1 || others[0].(map[string]any)["name"] != "Lumen Proxies" {
		t.Fatalf("other brands: %v", others)
	}
	ba := listed["by_assistant"].([]any)
	if len(ba) != 1 || ba[0].(map[string]any)["model"] != "openai/gpt-5" || ba[0].(map[string]any)["surface"] != "api" || ba[0].(map[string]any)["score"] != 60.0 {
		t.Fatalf("by assistant: %v", ba)
	}
	if got := owner.must(200, "GET", "/v1/brands/"+acme+"/ai-visibility/questions?cohort="+cid, nil)["data"].([]any); len(got) != 2 {
		t.Fatalf("by cohort: %v", got)
	}
	owner.must(400, "GET", "/v1/brands/"+acme+"/ai-visibility/questions?days=0", nil)

	// Week by week against competitors: Acme scores 30 (listed on one of
	// two, 0.6 and 0), Northpeak 80 (1 and 0.6); every line has this week.
	sr := owner.must(200, "GET", "/v1/brands/"+acme+"/ai-visibility/series?days=7", nil)
	lines := sr["series"].([]any)
	if sr["by"] != "competitor" || len(lines) != 2 || len(sr["weeks"].([]any)) < 1 {
		t.Fatalf("series: %v", sr)
	}
	you, rival := lines[0].(map[string]any), lines[1].(map[string]any)
	if you["you"] != true || you["key"] != "brand" || you["label"] != "Acme" || you["score"] != 30.0 || you["answers"] != 2.0 || you["questions"] != 2.0 || you["mention_rate"] != 0.5 {
		t.Fatalf("brand series: %v", you)
	}
	if rival["you"] != false || rival["label"] != "Northpeak" || rival["score"] != 80.0 || rival["mention_rate"] != 1.0 {
		t.Fatalf("competitor series: %v", rival)
	}
	wks := you["weeks"].([]any)
	last := wks[len(wks)-1].(map[string]any)
	if last["answers"] != 2.0 || last["score"] != 30.0 || last["week"] != sr["weeks"].([]any)[len(wks)-1] {
		t.Fatalf("brand weeks: %v", wks)
	}
	byA := owner.must(200, "GET", "/v1/brands/"+acme+"/ai-visibility/series?by=assistant&days=7&cohort="+cid, nil)["series"].([]any)
	if len(byA) != 1 || byA[0].(map[string]any)["model"] != "openai/gpt-5" || byA[0].(map[string]any)["score"] != 30.0 || byA[0].(map[string]any)["web_search"] != false {
		t.Fatalf("by assistant series: %v", byA)
	}
	if byT := owner.must(200, "GET", "/v1/brands/"+acme+"/ai-visibility/series?by=topic&days=7", nil)["series"].([]any); len(byT) != 1 || byT[0].(map[string]any)["label"] != "Not set" {
		t.Fatalf("by topic series: %v", byT)
	}
	owner.must(400, "GET", "/v1/brands/"+acme+"/ai-visibility/series?by=colour", nil)
	// By market: the question set's locale, once it has one.
	if byM := owner.must(200, "GET", "/v1/brands/"+acme+"/ai-visibility/series?by=market&days=7", nil)["series"].([]any); len(byM) != 1 || byM[0].(map[string]any)["key"] != "" || byM[0].(map[string]any)["score"] != 30.0 {
		t.Fatalf("by market series: %v", byM)
	}
	owner.must(200, "PATCH", base+"/ai-cohorts/"+cid, map[string]any{"locale": "en-US"})
	if byM := owner.must(200, "GET", "/v1/brands/"+acme+"/ai-visibility/series?by=market&days=7", nil)["series"].([]any); len(byM) != 1 || byM[0].(map[string]any)["key"] != "en-US" || byM[0].(map[string]any)["label"] != "en-US" {
		t.Fatalf("by market series: %v", byM)
	}

	// What the answers say: Acme is "cheapest plans" (once; the duplicate,
	// the empty and the bad sentiment are dropped) with the objection "No
	// static IPs" and its quote; Northpeak "Best known". The objection
	// cites the review page; disputing it is a fix with the pages.
	cl := owner.must(200, "GET", "/v1/brands/"+acme+"/ai-visibility/claims?days=7", nil)
	attrs, objs := cl["attributes"].([]any), cl["objections"].([]any)
	if len(attrs) != 1 || len(objs) != 1 {
		t.Fatalf("claims: %v", cl)
	}
	at, ob := attrs[0].(map[string]any), objs[0].(map[string]any)
	if at["key"] != "cheapest plans" || at["text"] != "Cheapest plans." || at["sentiment"] != "positive" || at["answers"] != 1.0 || at["share"] != 1.0 || at["previous_answers"] != 0.0 || at["questions"] != 1.0 || at["fix_id"] != nil {
		t.Fatalf("attribute: %v", at)
	}
	srcs := ob["sources"].([]any)
	if ob["key"] != "no static ips" || ob["kind"] != "objection" || ob["sentiment"] != "negative" || ob["quote"] != "Acme has no static IPs" || len(srcs) != 1 || srcs[0].(map[string]any)["host"] != "review.example" ||
		len(ob["assistants"].([]any)) != 1 {
		t.Fatalf("objection: %v", ob)
	}
	cc := cl["competitors"].([]any)
	// The second answer's first company is Northpeak, so it gets both
	// answers' "company 1" claims too, in key order.
	if len(cc) != 1 || cc[0].(map[string]any)["name"] != "Northpeak" || len(cc[0].(map[string]any)["attributes"].([]any)) != 2 || cc[0].(map[string]any)["attributes"].([]any)[0].(map[string]any)["key"] != "best known" ||
		len(cc[0].(map[string]any)["objections"].([]any)) != 1 {
		t.Fatalf("competitor claims: %v", cc)
	}
	disputed := owner.must(200, "POST", "/v1/brands/"+acme+"/ai-visibility/claims/dispute", map[string]any{"key": "No static IPs", "note": "Static IPs ship on every plan"})
	fixID := disputed["fix_id"].(string)
	if disputed["answers"] != 1.0 {
		t.Fatalf("dispute: %v", disputed)
	}
	fix := owner.must(200, "GET", base+"/fixes/"+fixID, nil)
	if fix["source"] != "ai_visibility" || fix["rule"].(map[string]any)["id"] != "ai_wrong_claim" || fix["note"] != "Static IPs ship on every plan" || fix["status"] != "open" || fix["brand_id"] != acme {
		t.Fatalf("fix: %v", fix)
	}
	if again := owner.must(200, "POST", "/v1/brands/"+acme+"/ai-visibility/claims/dispute", map[string]any{"key": "no static ips"}); again["fix_id"] != fixID {
		t.Fatalf("dispute again: %v", again)
	}
	if got := owner.must(200, "GET", "/v1/brands/"+acme+"/ai-visibility/claims?days=7", nil)["objections"].([]any)[0].(map[string]any)["fix_id"]; got != fixID {
		t.Fatalf("disputed claim: %v", got)
	}
	owner.must(404, "POST", "/v1/brands/"+acme+"/ai-visibility/claims/dispute", map[string]any{"key": "never said"})
	owner.must(400, "POST", "/v1/brands/"+acme+"/ai-visibility/claims/dispute", map[string]any{"key": "no static ips", "kind": "rumour"})
	if !containsEvent(events(), "ai_claim.disputed") {
		t.Fatalf("no ai_claim.disputed event")
	}

	// Placements: the list page cited by the answer that leaves Acme out
	// is worth the most; the review page cited by the answer naming it a
	// quarter of that. Northpeak is named on both. Acme's own cited page
	// is listed apart.
	pl := owner.must(200, "GET", "/v1/brands/"+acme+"/ai-visibility/placements", nil)
	places := pl["data"].([]any)
	if len(places) != 2 {
		t.Fatalf("placements: %v", places)
	}
	top, next := places[0].(map[string]any), places[1].(map[string]any)
	if top["host"] != "list.example" || top["answers"] != 1.0 || top["named"] != 0.0 || top["questions"] != 1.0 || top["value"] != 100.0 || top["source_type"] != "review" ||
		next["url"] != "https://review.example/proxies" || next["named"] != 1.0 || next["value"] != 25.0 {
		t.Fatalf("placements: %v %v", top, next)
	}
	if pc := top["competitors"].([]any); len(pc) != 1 || pc[0].(map[string]any)["name"] != "Northpeak" || pc[0].(map[string]any)["answers"] != 1.0 {
		t.Fatalf("placement competitors: %v", top["competitors"])
	}
	if own := pl["own_pages"].([]any); len(own) != 1 || own[0].(map[string]any)["host"] != "acme.example" || own[0].(map[string]any)["answers"] != 1.0 {
		t.Fatalf("own pages: %v", pl["own_pages"])
	}
	visit := func(anon, channel, source, path string) string {
		return one(`INSERT INTO site_sessions (workspace_id, brand_id, anonymous_id, channel, source, landing_path) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`,
			ws, acme, anon, channel, source, path)
	}
	gpt := visit("a1", "ai_answers", "chatgpt", "/pricing")
	visit("a2", "ai_answers", "chatgpt", "/")
	visit("a3", "ai_answers", "perplexity", "/pricing")
	visit("a4", "seo", "google", "/pricing")
	paid := one(`INSERT INTO customer_events (workspace_id, kind, name, occurred_at) VALUES ($1, 'track', 'payment.succeeded', now()) RETURNING id::text`, ws)
	one(`INSERT INTO attribution_conversions (event_id, workspace_id, brand_id, name, occurred_at, value_minor, currency) VALUES ($1, $2, $3, 'payment.succeeded', now(), 4900, 'USD') RETURNING event_id::text`, paid, ws, acme)
	one(`INSERT INTO attribution_credits (event_id, workspace_id, model, position, session_id, channel, source, weight) VALUES ($1, $2, 'last_non_direct', 0, $3, 'ai_answers', 'chatgpt', 1) RETURNING event_id::text`, paid, ws, gpt)
	tr := owner.must(200, "GET", base+"/ai-visibility", nil)["data"].([]any)[0].(map[string]any)["traffic"].(map[string]any)
	srcs, pages := tr["sources"].([]any), tr["landing_pages"].([]any)
	if tr["visits"] != 3.0 || tr["conversions"] != 1.0 || tr["model"] != "last_non_direct" || len(tr["revenue"].([]any)) != 1 ||
		tr["revenue"].([]any)[0].(map[string]any)["amount_cents"] != 4900.0 || len(srcs) != 2 ||
		srcs[0].(map[string]any)["name"] != "chatgpt" || srcs[0].(map[string]any)["visits"] != 2.0 || srcs[0].(map[string]any)["conversions"] != 1.0 ||
		pages[0].(map[string]any)["name"] != "/pricing" || pages[0].(map[string]any)["visits"] != 2.0 {
		t.Fatalf("traffic: %v", tr)
	}

	// A page an answer cited without naming the brand is an opportunity on
	// Presence's list; one whose answers all named the brand is not.
	var reason, source string
	var factors map[string]any
	if err := e.pool.QueryRow(ctx, `SELECT reason, source, factors FROM placement_opportunities WHERE workspace_id = $1 AND host = 'list.example'`, ws).Scan(&reason, &source, &factors); err != nil ||
		reason != "ai_cited" || source != "ai_visibility" || factors["ai_citations"] != 1.0 || factors["competitors"] != 1.0 {
		t.Fatalf("opportunity: %s %s %v %v", reason, source, factors, err)
	}
	var onReview int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM placement_opportunities WHERE workspace_id = $1 AND host = 'review.example'`, ws).Scan(&onReview); err != nil || onReview != 0 {
		t.Fatalf("review.example: %d %v", onReview, err)
	}

	// The question whose answer names Acme but recommends Northpeak is a
	// fix; the one leaving Acme out is the absent fix, not both.
	fixRules := func() map[string]string {
		out := map[string]string{}
		rows, err := e.pool.Query(ctx, `SELECT f.rule || ':' || p.text, f.status || '/' || f.count FROM fixes f JOIN ai_prompts p ON p.id = f.subject_id WHERE f.workspace_id = $1 AND f.source = 'ai_visibility'`, ws)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				t.Fatal(err)
			}
			out[k] = v
		}
		return out
	}
	if f := fixRules(); len(f) != 2 || f["ai_brand_absent:Which residential proxies are best?"] != "open/1" && f["ai_brand_absent:What is the best proxy provider?"] != "open/1" ||
		f["ai_competitor_preferred:What is the best proxy provider?"] != "open/1" && f["ai_competitor_preferred:Which residential proxies are best?"] != "open/1" {
		t.Fatalf("fixes: %v", f)
	}

	// The weekly digest's section: the answers read so far, dated a day
	// back so they sit inside the digest's period (which ends at the
	// minute) and keep every page's window.
	if _, err := e.pool.Exec(ctx, `UPDATE ai_samples SET sampled_at = now() - interval '1 day' WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE ai_claims SET sampled_at = now() - interval '1 day' WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	var digest map[string]any
	for _, sec := range owner.must(200, "GET", "/v1/brands/"+acme+"/digests/preview?frequency=weekly", nil)["sections"].([]any) {
		if sec.(map[string]any)["id"] == "ai_visibility" {
			digest = sec.(map[string]any)
		}
	}
	if digest == nil {
		t.Fatal("no ai_visibility digest section")
	}
	metrics := map[string]map[string]any{}
	for _, m := range digest["metrics"].([]any) {
		metrics[m.(map[string]any)["key"].(string)] = m.(map[string]any)
	}
	if metrics["ai_answers"]["value"] != float64(2) || metrics["ai_named_pct"]["value"] != float64(50) || metrics["ai_recommended_pct"]["value"] != float64(0) ||
		metrics["ai_score"]["value"] != float64(30) || metrics["ai_answers"]["previous"] != nil {
		t.Fatalf("digest metrics: %v", digest["metrics"])
	}
	if items := digest["items"].([]any); len(items) != 1 || items[0].(map[string]any)["text"] != "New objection in 1 answer: No static IPs" || items[0].(map[string]any)["tab"] != "ai-perception" {
		t.Fatalf("digest items: %v", digest["items"])
	}

	// The questions and the placements as CSV.
	if code, ct, body := owner.text("/v1/brands/" + acme + "/ai-visibility/questions?days=7&format=csv"); code != 200 || !strings.HasPrefix(ct, "text/csv") ||
		!strings.HasPrefix(body, "question,") || !strings.Contains(body, "What is the best proxy provider?") {
		t.Fatalf("questions csv: %d %s %q", code, ct, body)
	}
	if code, ct, body := owner.text("/v1/brands/" + acme + "/ai-visibility/placements?days=7&format=csv"); code != 200 || !strings.HasPrefix(ct, "text/csv") ||
		!strings.HasPrefix(body, "url,") || !strings.Contains(body, "list.example") {
		t.Fatalf("placements csv: %d %s %q", code, ct, body)
	}
	owner.must(400, "GET", "/v1/brands/"+acme+"/ai-visibility/questions?format=xml", nil)

	// A share link: the brand's overview, trend and questions for anyone
	// with it, with a password when asked for one.
	anon := &client{t: t, h: e.h}
	if out := owner.must(200, "GET", "/v1/brands/"+acme+"/ai-visibility/share", nil); out["share"] != nil {
		t.Fatalf("a brand starts without a link: %v", out)
	}
	owner.must(400, "POST", "/v1/brands/"+acme+"/ai-visibility/share", map[string]any{"password": "short"})
	link := owner.must(201, "POST", "/v1/brands/"+acme+"/ai-visibility/share", map[string]any{"expires_in_days": 30})
	token := link["token"].(string)
	if !strings.HasPrefix(token, "ofv_") || !strings.HasSuffix(link["url"].(string), "/share/ai/"+token) || link["share"].(map[string]any)["has_password"] != false || link["share"].(map[string]any)["expires_at"] == nil {
		t.Fatalf("link: %v", link)
	}
	anon.must(404, "POST", "/v1/shared/ai-visibility/ofv_not_a_real_token", map[string]any{})
	open := anon.must(200, "POST", "/v1/shared/ai-visibility/"+token, map[string]any{})
	if open["brand"] != "Acme" || open["days"] != float64(28) || open["key"] != "" || len(open["questions"].([]any)) != 2 || len(open["series"].([]any)) != 2 ||
		open["data"].([]any)[0].(map[string]any)["brand"].(map[string]any)["score"] != float64(30) {
		t.Fatalf("open: %v", open)
	}
	if share := owner.must(200, "GET", "/v1/brands/"+acme+"/ai-visibility/share", nil)["share"].(map[string]any); share["views"] != float64(1) || share["last_viewed_at"] == nil {
		t.Fatalf("share after a view: %v", share)
	}
	// A new link with a password replaces it; the key opens it again.
	link = owner.must(201, "POST", "/v1/brands/"+acme+"/ai-visibility/share", map[string]any{"password": "acme numbers"})
	anon.must(404, "POST", "/v1/shared/ai-visibility/"+token, map[string]any{})
	token = link["token"].(string)
	if code, out := anon.do("POST", "/v1/shared/ai-visibility/"+token, map[string]any{}); code != 401 || out["error"].(map[string]any)["code"] != "password_required" {
		t.Fatalf("without the password: %d %v", code, out)
	}
	if code, out := anon.do("POST", "/v1/shared/ai-visibility/"+token, map[string]any{"password": "wrong one"}); code != 401 || out["error"].(map[string]any)["code"] != "wrong_password" {
		t.Fatalf("wrong password: %d %v", code, out)
	}
	open = anon.must(200, "POST", "/v1/shared/ai-visibility/"+token, map[string]any{"password": "acme numbers"})
	key := open["key"].(string)
	if key == "" {
		t.Fatalf("no key: %v", open)
	}
	anon.must(200, "POST", "/v1/shared/ai-visibility/"+token, map[string]any{"key": key})
	anon.must(401, "POST", "/v1/shared/ai-visibility/"+token, map[string]any{"key": "not the key"})
	// Taken back, the link is dead.
	owner.must(204, "DELETE", "/v1/brands/"+acme+"/ai-visibility/share", nil)
	owner.must(404, "DELETE", "/v1/brands/"+acme+"/ai-visibility/share", nil)
	anon.must(404, "POST", "/v1/shared/ai-visibility/"+token, map[string]any{})
	shared, revoked, wrong := false, false, false
	for _, ev := range events() {
		shared = shared || ev.Name == "ai_visibility.shared"
		revoked = revoked || ev.Name == "ai_visibility.share_revoked"
		wrong = wrong || (ev.Name == "ai_visibility_share.password_failed" && ev.ErrorCode == "wrong_password")
	}
	if !shared || !revoked || !wrong {
		t.Fatalf("share events: shared %v revoked %v wrong %v", shared, revoked, wrong)
	}

	// A reader whose answer doesn't parse leaves the stances unread and says why.
	bad := "not json"
	judge = bad
	rid = owner.must(201, "POST", base+"/ai-cohorts/"+cid+"/runs", nil)["id"].(string)
	if err := svc.Step(ctx, ws, rid, 0); err != nil {
		t.Fatal(err)
	}
	if s := owner.must(200, "GET", base+"/ai-samples?run_id="+rid+"&brand_named=true", nil)["data"].([]any)[0].(map[string]any); s["stance_status"] != "failed" {
		t.Fatalf("failed read: %v", s)
	}
	// Unread stances neither find nor confirm the preferred fix.
	if f := fixRules(); len(f) != 2 {
		t.Fatalf("fixes after unread stances: %v", f)
	}
	failed := false
	for _, ev := range events() {
		failed = failed || (ev.Name == "ai_stance.failed" && ev.ErrorCode == "ai_failed")
	}
	if !failed {
		t.Fatal("no ai_stance.failed event")
	}
}
