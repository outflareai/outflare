package api_test

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Who is dominating: the brand and its tracked competitors get a share and
// rank in each area, an overall score and rank, and the brand learns where
// it leads, where it trails, whether its share is gaining and when it
// passes the one ahead, all from stored data.
func TestCompetition(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Trailcast", "handle": "trailcast"})["id"].(string)
	root := "/v1/brands/" + brand + "/competition"

	// Nothing tracked yet: only the brand, measured nowhere.
	got := owner.must(200, "GET", root, nil)
	if got["tracked"] != 0.0 || len(got["entries"].([]any)) != 1 || len(got["standings"].([]any)) != 0 {
		t.Fatalf("empty: %v", got)
	}
	for _, a := range got["areas"].([]any) {
		if a.(map[string]any)["measured"] != false || a.(map[string]any)["needs"] == "" {
			t.Fatalf("area: %v", a)
		}
	}

	exec := func(q string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, q+` RETURNING id::text`, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	site := exec(`INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token) VALUES ($1, $2, 'www.northpeak.example', 'https://www.northpeak.example/', 'tok')`, ws, brand)
	rival := exec(`INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, name, status) VALUES ($1, $2, $3, 'rival.example', 'Rival', 'tracked')`, ws, site, brand)
	small := exec(`INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, name, status) VALUES ($1, $2, $3, 'small.example', 'Small', 'tracked')`, ws, site, brand)
	exec(`INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, name, status) VALUES ($1, $2, $3, 'gone.example', 'Gone', 'dismissed')`, ws, site, brand)
	integ := exec(`INSERT INTO integrations (workspace_id, connector, name, credentials) VALUES ($1, 'dataforseo', 'DataForSEO', '\x00')`, ws)

	// Ten weekly reads: the brand grows 100 visits a week, Rival 20, Small
	// stays put. Another connection's numbers are on another scale and are
	// left out.
	today := time.Now().UTC()
	ahrefs := exec(`INSERT INTO integrations (workspace_id, connector, name, credentials) VALUES ($1, 'ahrefs', 'Ahrefs', '\x00')`, ws)
	snap := func(conn, host string, day time.Time, traffic, domains float64) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `INSERT INTO seo_snapshots (workspace_id, integration_id, connector, host, report, day, key, metrics) VALUES ($1, $2, $3, $4, 'overview', $5, '', $6)`,
			ws, map[string]string{"dataforseo": integ, "ahrefs": ahrefs}[conn], conn, host, day.Format(time.DateOnly), fmt.Sprintf(`{"organic_traffic": %v, "referring_domains": %v}`, traffic, domains)); err != nil {
			t.Fatal(err)
		}
	}
	for k := 9; k >= 0; k-- {
		day := today.AddDate(0, 0, -7*k)
		snap("dataforseo", "northpeak.example", day, 1900-100*float64(k), 200)
		snap("dataforseo", "rival.example", day, 3180-20*float64(k), 400)
		snap("dataforseo", "small.example", day, 500, 100)
	}
	snap("ahrefs", "small.example", today, 999999, 999999)

	// In AI answers this month, three name the brand and one Rival.
	cohort := exec(`INSERT INTO ai_cohorts (workspace_id, brand_id, name) VALUES ($1, $2, 'Buyers')`, ws, brand)
	prompt := exec(`INSERT INTO ai_prompts (workspace_id, cohort_id, text) VALUES ($1, $2, 'best gear')`, ws, cohort)
	run := exec(`INSERT INTO ai_runs (workspace_id, cohort_id, trigger) VALUES ($1, $2, 'manual')`, ws, cohort)
	for i, who := range []string{"brand", "brand", "brand", "Rival"} {
		sample := exec(`INSERT INTO ai_samples (workspace_id, run_id, cohort_id, prompt_id, brand_id, provider, model, status, answer) VALUES ($1, $2, $3, $4, $5, 'openai', $6, 'ok', 'an answer')`, ws, run, cohort, prompt, brand, fmt.Sprint("test-", i))
		entity, name := "competitor", who
		if who == "brand" {
			entity, name = "brand", "Northpeak"
		}
		if _, err := e.pool.Exec(ctx, `INSERT INTO ai_mentions (sample_id, workspace_id, cohort_id, entity, name, position, count, sampled_at) VALUES ($1, $2, $3, $4, $5, 1, 1, now() - make_interval(days => $6))`,
			sample, ws, cohort, entity, name, i); err != nil {
			t.Fatal(err)
		}
	}

	got = owner.must(200, "GET", root, nil)
	if got["tracked"] != 2.0 || got["no_website"] != false {
		t.Fatalf("overview: %v", got)
	}
	entries := map[string]map[string]any{}
	for _, en := range got["entries"].([]any) {
		m := en.(map[string]any)
		entries[m["id"].(string)] = m
	}
	if len(entries) != 3 || entries["brand"] == nil || entries[rival] == nil || entries[small] == nil {
		t.Fatalf("entries: %v", got["entries"])
	}
	area := func(id, key string) map[string]any {
		return entries[id]["areas"].(map[string]any)[key].(map[string]any)
	}
	if v := area(small, "search"); v["value"] != 500.0 {
		t.Fatalf("one connection only: %v", v)
	}
	if v := area(rival, "search"); v["rank"] != 1.0 || v["value"] != 3180.0 {
		t.Fatalf("rival leads search: %v", v)
	}
	if v := area("brand", "ai"); v["rank"] != 1.0 || v["share"] != 75.0 {
		t.Fatalf("brand leads AI answers: %v", v)
	}
	if v := area(small, "ai"); v["value"] != 0.0 || v["rank"] != 3.0 {
		t.Fatalf("small is measured and never named: %v", v)
	}
	// Brand: search 1900/3180, links 200/400, AI 100 → 70; Rival: 100, 100, 33 → 78.
	if entries["brand"]["score"] != 70.0 || entries[rival]["score"] != 78.0 || entries[rival]["rank"] != 1.0 || entries["brand"]["rank"] != 2.0 {
		t.Fatalf("scores: brand %v rival %v", entries["brand"], entries[rival])
	}
	leaders := map[string]any{}
	for _, a := range got["areas"].([]any) {
		m := a.(map[string]any)
		leaders[m["key"].(string)] = m["leader_id"]
	}
	if leaders["search"] != rival || leaders["ai"] != "brand" || leaders["placements"] != nil {
		t.Fatalf("leaders: %v", leaders)
	}
	standings := map[string]map[string]any{}
	for _, s := range got["standings"].([]any) {
		m := s.(map[string]any)
		standings[m["area"].(string)] = m
	}
	if s := standings["search"]; s["standing"] != "level" || s["trend"] != "gaining" {
		t.Fatalf("search standing: %v", s)
	}
	if s := standings["ai"]; s["standing"] != "leading" {
		t.Fatalf("ai standing: %v", s)
	}
	if s := standings["links"]; s["standing"] != "behind" || s["trend"] != "steady" {
		t.Fatalf("links standing: %v", s)
	}
	// 1,280 visits behind, closing 80 a week: 16 weeks.
	var pass map[string]any
	for _, c := range got["catch_ups"].([]any) {
		if m := c.(map[string]any); m["area"] == "search" && m["direction"] == "you_pass" {
			pass = m
		}
	}
	if pass == nil || pass["competitor_id"] != rival || pass["weeks"] != 16.0 {
		t.Fatalf("catch up: %v", got["catch_ups"])
	}
	if pts := got["series"].(map[string]any)["search"].([]any); len(pts) != 10 {
		t.Fatalf("series: %v", pts)
	}

	// Weights change the score: counting only AI answers, the brand leads.
	got = owner.must(200, "GET", root+"?weights=search:0,keywords:0,links:0,placements:0", nil)
	for _, en := range got["entries"].([]any) {
		if m := en.(map[string]any); m["id"] == "brand" && (m["score"] != 100.0 || m["rank"] != 1.0) {
			t.Fatalf("weighted: %v", m)
		}
	}
	owner.must(400, "GET", root+"?weights=search:11", nil)
	owner.must(400, "GET", root+"?weights=followers:1", nil)

	// Another brand sees none of this brand's competitors.
	if got := owner.must(200, "GET", "/v1/brands/"+other+"/competition", nil); got["tracked"] != 0.0 {
		t.Fatalf("other brand: %v", got)
	}
	owner.must(404, "GET", "/v1/brands/not-a-brand/competition", nil)
}
