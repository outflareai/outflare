package api_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestSEOTargetReport(t *testing.T) {
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

	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/":
			io.WriteString(w, `<html><head><title>Proxy pricing plans</title></head><body><h1>Proxy plans</h1><a href="/blog">blog</a></body></html>`)
		case "/blog":
			io.WriteString(w, `<html><head><title>Proxy blog</title></head><body><h1>Proxy blog</h1><a href="/">home</a></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer page.Close()
	u := func(p string) string { return page.URL + p }

	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Target Example", "website": "https://example.com"})["id"].(string)
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Other Example", "website": "https://other.example"})["id"].(string)
	report := "/v1/brands/" + brand + "/seo/targets"

	// No sites: an empty report, not an error.
	empty := owner.must(200, "GET", report, nil)
	if len(empty["targets"].([]any)) != 0 || len(empty["sites"].([]any)) != 0 {
		t.Fatalf("empty: %v", empty)
	}

	site := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": u("/"), "brand_id": brand})["id"].(string)
	root := "/v1/brands/" + brand + "/seo-sites/" + site
	e.runCrawls(t)
	rid := owner.must(202, "POST", root+"/crawls", nil)["id"].(string)
	waitFor(t, owner, "/v1/brands/"+brand+"/crawls/"+rid, func(m map[string]any) bool { return m["status"] == "done" })

	owner.must(200, "PUT", root+"/targets", map[string]any{"url": u("/"), "keyword": "proxy pricing", "secondary": []string{"proxy plans"}})
	owner.must(200, "PUT", root+"/targets", map[string]any{"url": u("/blog"), "keyword": "proxy blog"})

	// Before Search Console: the targets, with no numbers.
	r := owner.must(200, "GET", report, nil)
	if len(r["targets"].([]any)) != 2 || r["summary"].(map[string]any)["none"] != 2.0 {
		t.Fatalf("before search: %v", r)
	}

	sc := connectSearchConsole(t, e, owner, base, &fakeSearchConsole{})
	newest := time.Now().UTC().AddDate(0, 0, -3).Truncate(24 * time.Hour)
	exec(`INSERT INTO search_syncs (integration_id, site, workspace_id, host, oldest_day, newest_day) VALUES ($1, 'sc-domain:127.0.0.1', $2, '127.0.0.1', $3::date - 100, $3)
		ON CONFLICT (integration_id, site) DO UPDATE SET oldest_day = EXCLUDED.oldest_day, newest_day = EXCLUDED.newest_day`, sc, ws, newest)
	owner.must(200, "PUT", base+"/integrations/"+sc+"/accounts/"+url.PathEscape("sc-domain:127.0.0.1"), map[string]any{"brand_id": brand})
	day := func(n int) time.Time { return newest.AddDate(0, 0, -n) }
	row := `INSERT INTO search_daily (workspace_id, brand_id, integration_id, site, host, day, page, query, clicks, impressions, ctr, position)
		VALUES ($1, $2, $3, 'sc-domain:127.0.0.1', '127.0.0.1', $4, $5, $6, $7, $8, 0, $9)`
	// The home page's target: position 4 now, 8 a month before.
	exec(row, ws, brand, sc, day(1), u("/"), "proxy pricing", 10, 100, 4)
	exec(row, ws, brand, sc, day(30), u("/"), "proxy pricing", 5, 100, 8)
	exec(row, ws, brand, sc, day(2), u("/"), "proxy plans", 1, 50, 12)
	// The blog shows for the home page's keyword too.
	exec(row, ws, brand, sc, day(1), u("/blog"), "proxy pricing", 0, 30, 20)
	// Under the floor: not a competing page.
	exec(row, ws, brand, sc, day(1), u("/other"), "proxy pricing", 0, 5, 30)
	// Older than 12 weeks: not read.
	exec(row, ws, brand, sc, day(100), u("/"), "proxy pricing", 50, 500, 1)

	// The rank tracker checks the blog's keyword in the US.
	var tracked string
	if err := e.pool.QueryRow(ctx, `INSERT INTO tracked_keywords (workspace_id, brand_id, site_id, keyword, country, device) VALUES ($1, $2, $3, 'proxy blog', 'US', 'desktop') RETURNING id::text`,
		ws, brand, site).Scan(&tracked); err != nil {
		t.Fatal(err)
	}
	obs := `INSERT INTO rank_observations (workspace_id, brand_id, tracked_id, day, position, url, depth, engine) VALUES ($1, $2, $3, $4, $5, $6, 100, 'google')`
	exec(obs, ws, brand, tracked, day(7), 5, u("/blog"))
	exec(obs, ws, brand, tracked, day(0), 2, u("/blog/"))

	r = owner.must(200, "GET", report, nil)
	sum := r["summary"].(map[string]any)
	if sum["targets"] != 2.0 || sum["top3"] != 1.0 || sum["top10"] != 1.0 || sum["improved"] != 1.0 || sum["competing"] != 1.0 || sum["untracked"] != 1.0 {
		t.Fatalf("summary: %v", sum)
	}
	if src := r["sites"].([]any)[0].(map[string]any)["source"]; src == nil {
		t.Fatalf("source: %v", r["sites"])
	}
	by := map[string]map[string]any{}
	for _, x := range r["targets"].([]any) {
		x := x.(map[string]any)
		by[x["url"].(string)] = x
	}

	home := by[u("/")]
	prim := home["primary"].(map[string]any)
	cur, prev := prim["current"].(map[string]any), prim["previous"].(map[string]any)
	if cur["position"] != 4.0 || cur["clicks"] != 10.0 || prev["position"] != 8.0 || home["trend"] != "improved" || home["band"] != "top10" {
		t.Fatalf("home: %v", home)
	}
	weekly := prim["weekly"].([]any)
	if len(weekly) != 12 || weekly[11] != 4.0 || weekly[7] != 8.0 || weekly[0] != nil {
		t.Fatalf("weekly: %v", weekly)
	}
	sec := home["secondary"].([]any)
	if len(sec) != 1 || sec[0].(map[string]any)["keyword"] != "proxy plans" || sec[0].(map[string]any)["current"].(map[string]any)["position"] != 12.0 {
		t.Fatalf("secondary: %v", sec)
	}
	comp := home["competing"].([]any)
	if len(comp) != 1 || comp[0].(map[string]any)["url"] != u("/blog") || comp[0].(map[string]any)["impressions"] != 30.0 {
		t.Fatalf("competing: %v", comp)
	}
	if c := home["checks"].(map[string]any); c["in_title"] != true || c["in_h1"] != false {
		t.Fatalf("checks: %v", c)
	}

	blog := by[u("/blog")]
	tr := blog["tracked"].([]any)
	if len(tr) != 1 || blog["band"] != "top3" {
		t.Fatalf("blog: %v", blog)
	}
	if x := tr[0].(map[string]any); x["position"] != 2.0 || x["previous_position"] != 5.0 || x["on_target"] != true || x["country"] != "US" {
		t.Fatalf("tracked: %v", x)
	}
	if blog["trend"] != "unknown" {
		t.Fatalf("no search history is unknown: %v", blog["trend"])
	}

	// Another brand sees none of it.
	if o := owner.must(200, "GET", "/v1/brands/"+other+"/seo/targets", nil); len(o["targets"].([]any)) != 0 {
		t.Fatalf("other brand: %v", o)
	}
}

func TestPageTargetPassport(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Passport Example", "website": "https://example.com"})["id"].(string)
	site := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://example.com", "brand_id": brand})["id"].(string)
	root := "/v1/brands/" + brand + "/seo-sites/" + site

	// One import: good rows are saved, wrong ones are listed.
	res := owner.must(200, "POST", root+"/targets/import", map[string]any{"targets": []map[string]any{
		{"url": "https://example.com/pricing", "keyword": "Proxy Pricing", "secondary": []string{"proxy plans", "proxy pricing"},
			"intent": "Informational", "page_type": "pricing", "summary": "## Plans\nResidential from $1 a GB."},
		{"url": "https://example.com/blog", "keyword": "proxy guide", "intent": "curious"},
		{"url": "https://example.com/new"},
		{"url": "https://other.example/x", "keyword": "elsewhere"},
		{"url": "https://example.com/pricing", "keyword": "twice"},
		{"url": "https://example.com/guide", "keyword": "proxy guide", "intent": "informational", "page_type": "blog"},
	}})
	if res["created"] != 2.0 || res["updated"] != 0.0 || res["skipped"] != 4.0 {
		t.Fatalf("import: %v", res)
	}
	byIndex := map[float64]string{}
	for _, x := range res["errors"].([]any) {
		x := x.(map[string]any)
		byIndex[x["index"].(float64)] = x["message"].(string)
	}
	if byIndex[1] == "" || byIndex[2] != "a new page needs a keyword" || byIndex[3] == "" || byIndex[4] != "the page is in the import twice" {
		t.Fatalf("errors: %v", byIndex)
	}
	owner.must(400, "POST", root+"/targets/import", map[string]any{"targets": []map[string]any{}})
	owner.must(400, "PUT", root+"/targets", map[string]any{"url": "https://example.com/pricing", "keyword": "x", "page_type": "landing"})

	list := owner.must(200, "GET", root+"/targets?status=set", nil)["data"].([]any)
	var pricing map[string]any
	for _, x := range list {
		if x.(map[string]any)["url"] == "https://example.com/pricing" {
			pricing = x.(map[string]any)
		}
	}
	if pricing == nil || pricing["keyword"] != "proxy pricing" || pricing["intent"] != "informational" || pricing["page_type"] != "pricing" ||
		pricing["summary_length"] != float64(len("## Plans\nResidential from $1 a GB.")) || pricing["summary"] != nil {
		t.Fatalf("listed: %v", pricing)
	}
	if sec := pricing["secondary"].([]any); len(sec) != 1 || sec[0] != "proxy plans" {
		t.Fatalf("secondary drops the primary: %v", sec)
	}
	id := pricing["id"].(string)
	one := owner.must(200, "GET", root+"/targets/"+id, nil)
	if one["summary"] != "## Plans\nResidential from $1 a GB." {
		t.Fatalf("one target has its summary: %v", one)
	}

	// A write that leaves the passport out keeps it; a later import updates
	// only what it names.
	owner.must(200, "PUT", root+"/targets", map[string]any{"url": "https://example.com/pricing", "keyword": "proxy pricing", "secondary": []string{"proxy plans"}})
	res = owner.must(200, "POST", root+"/targets/import", map[string]any{"targets": []map[string]any{{"url": "https://example.com/pricing", "page_type": "pricing"}}})
	if res["updated"] != 1.0 || res["created"] != 0.0 {
		t.Fatalf("update import: %v", res)
	}
	one = owner.must(200, "GET", root+"/targets/"+id, nil)
	if one["intent"] != "informational" || one["keyword"] != "proxy pricing" || one["summary_length"] == 0.0 {
		t.Fatalf("kept: %v", one)
	}

	// The report judges each target.
	r := owner.must(200, "GET", "/v1/brands/"+brand+"/seo/targets", nil)
	by := map[string]map[string]any{}
	for _, x := range r["targets"].([]any) {
		by[x.(map[string]any)["url"].(string)] = x.(map[string]any)
	}
	p := by["https://example.com/pricing"]
	if p["status"] != "wrong_intent" || p["intent"] != "informational" || p["page_type"] != "pricing" {
		t.Fatalf("pricing: %v", p)
	}
	kinds := map[string]bool{}
	for _, s := range p["suggestions"].([]any) {
		kinds[s.(map[string]any)["kind"].(string)] = true
	}
	if !kinds["intent"] || !kinds["cover"] || kinds["summary"] {
		t.Fatalf("pricing suggestions: %v", p["suggestions"])
	}
	g := by["https://example.com/guide"]
	if g["status"] != "no_data" || g["summary_length"] != 0.0 {
		t.Fatalf("guide: %v", g)
	}
	sum := r["summary"].(map[string]any)
	if sum["wrong_intent"] != 1.0 || sum["no_data"] != 1.0 || sum["no_summary"] != 1.0 {
		t.Fatalf("summary: %v", sum)
	}

	// PATCH clears the intent with an empty string.
	owner.must(200, "PATCH", root+"/targets/"+id, map[string]any{"intent": ""})
	if one = owner.must(200, "GET", root+"/targets/"+id, nil); one["intent"] != "" {
		t.Fatalf("cleared: %v", one)
	}
}
