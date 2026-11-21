package api_test

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestSEOCountryPages(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	near := func(a any, b float64) bool {
		f, ok := a.(float64)
		return ok && math.Abs(f-b) < 1e-6
	}

	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Country Example", "website": "https://example.com"})["id"].(string)
	site := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://example.com", "brand_id": brand})["id"].(string)
	report := "/v1/brands/" + brand + "/seo/country-pages"
	if r := owner.must(200, "GET", report, nil); r["from"] != nil || len(r["pages"].([]any)) != 0 {
		t.Fatalf("empty: %v", r)
	}

	integ := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "name": "Search", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})["id"].(string)
	newest := time.Now().UTC().AddDate(0, 0, -3).Truncate(24 * time.Hour)
	day := func(n int) time.Time { return newest.AddDate(0, 0, -n) }
	seg := `INSERT INTO search_segments (workspace_id, brand_id, integration_id, site, host, day, page, country, device, clicks, impressions, position)
		VALUES ($1, $2, $3, 'sc-domain:example.com', 'example.com', $4, $5, $6, 'DESKTOP', $7, $8, $9)`
	add := func(d int, path, country string, c, i, p float64) {
		exec(seg, ws, brand, integ, day(d), "https://example.com"+path, country, c, i, p)
	}
	add(0, "/proxies/germany", "deu", 50, 1000, 5)
	add(0, "/proxies/germany", "usa", 5, 500, 20)
	add(30, "/proxies/germany", "deu", 40, 1000, 7)
	add(0, "/proxies/japan", "jpn", 10, 400, 12)
	add(30, "/proxies/japan", "jpn", 5, 400, 15)
	add(0, "/proxies/france", "fra", 0, 100, 30)
	add(0, "/blog/a", "usa", 100, 1000, 3)

	// Germany's page targets a keyword tracked in Germany.
	owner.must(200, "PUT", "/v1/brands/"+brand+"/seo-sites/"+site+"/targets", map[string]any{"url": "https://example.com/proxies/germany", "keyword": "proxy germany"})
	var tracked string
	if err := e.pool.QueryRow(ctx, `INSERT INTO tracked_keywords (workspace_id, brand_id, site_id, keyword, country, device) VALUES ($1, $2, $3, 'proxy germany', 'DE', 'desktop') RETURNING id::text`,
		ws, brand, site).Scan(&tracked); err != nil {
		t.Fatal(err)
	}
	obs := `INSERT INTO rank_observations (workspace_id, brand_id, tracked_id, day, position, url, depth, engine) VALUES ($1, $2, $3, $4, $5, 'https://example.com/proxies/germany', 100, 'google')`
	exec(obs, ws, brand, tracked, day(7), 6)
	exec(obs, ws, brand, tracked, day(0), 4)

	r := owner.must(200, "GET", report+"?prefix=/proxies/", nil)
	by := map[string]map[string]any{}
	for _, p := range r["pages"].([]any) {
		by[p.(map[string]any)["path"].(string)] = p.(map[string]any)
	}
	if len(by) != 3 || by["/blog/a"] != nil {
		t.Fatalf("pages: %v", by)
	}
	de := by["/proxies/germany"]
	if de["target_country"] != "deu" || de["country_from"] != "tracked" || de["tracked_position"] != 4.0 || de["tracked_previous"] != 6.0 || de["band"] != "top10" || de["untracked"] != false {
		t.Fatalf("germany: %v", de)
	}
	cur, prev, all := de["current"].(map[string]any), de["previous"].(map[string]any), de["all_countries"].(map[string]any)
	if !near(cur["clicks"], 50) || !near(cur["position"], 5) || !near(prev["position"], 7) || !near(all["clicks"], 55) {
		t.Fatalf("germany numbers: %v %v %v", cur, prev, all)
	}
	jp := by["/proxies/japan"]
	if jp["target_country"] != "jpn" || jp["country_from"] != "search" || jp["band"] != "striking" || jp["untracked"] != true {
		t.Fatalf("japan: %v", jp)
	}
	if by["/proxies/france"]["band"] != "beyond" {
		t.Fatalf("france: %v", by["/proxies/france"])
	}
	tpl := r["templates"].([]any)
	if len(tpl) != 1 {
		t.Fatalf("templates: %v", tpl)
	}
	g := tpl[0].(map[string]any)
	if g["template"] != "/proxies/" || g["pages"] != 3.0 || !near(g["median_position"], 12) || !near(g["median_previous"], 11) || g["top10"] != 1.0 || g["striking"] != 1.0 || g["untracked"] != 2.0 {
		t.Fatalf("template: %v", g)
	}
	if c := r["template_choices"].([]any); len(c) != 1 || c[0].(map[string]any)["template"] != "/proxies/" {
		t.Fatalf("choices: %v", c)
	}
	sum := r["summary"].(map[string]any)
	if sum["pages"] != 3.0 || sum["top10"] != 1.0 || sum["striking"] != 1.0 || sum["beyond"] != 1.0 || sum["untracked"] != 2.0 {
		t.Fatalf("summary: %v", sum)
	}
	if r := owner.must(200, "GET", report, nil); len(r["pages"].([]any)) != 4 {
		t.Fatalf("all pages: %v", r["pages"])
	}
}
