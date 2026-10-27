package api_test

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAnalyticsChangesAndCatalog(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	// Nothing connected: the answer says what to connect.
	empty := owner.must(200, "GET", base+"/analytics/changes", nil)
	if len(empty["websites"].([]any)) != 0 || !hasGap(empty["gaps"], "No website traffic") || !hasGap(empty["gaps"], "No SEO tool") {
		t.Fatalf("empty: %v", empty)
	}
	owner.must(400, "GET", base+"/analytics/changes?days=0", nil)
	owner.must(400, "GET", base+"/analytics/changes?host=not%20a%20domain", nil)

	pl := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "name": "Plausible", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})["id"].(string)
	sc := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "name": "Search Console", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})["id"].(string)
	dfs := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})["id"].(string)

	to := time.Now().UTC().AddDate(0, 0, -1)
	day := func(n int) string { return to.AddDate(0, 0, -n).Format(time.DateOnly) }
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{pl, sc} {
		exec(`INSERT INTO analytics_syncs (integration_id, workspace_id, site, host, first_day, last_day, backfilled, last_run_at, last_ok_at)
			VALUES ($1, $2, 'example.com', 'example.com', $3, $4, true, now(), now())`, id, ws, day(41), day(0))
	}
	row := `INSERT INTO analytics_daily (workspace_id, integration_id, connector, site, host, report, day, key, visitors, pageviews, clicks, impressions, position)
		VALUES ($1, $2, $3, 'example.com', 'example.com', $4, $5, $6, $7, $8, $9, $10, $11)`
	for n := 0; n < 42; n++ {
		// Plausible: 100 visitors a day, 130 this week, and one spike.
		v := 100.0
		if n < 7 {
			v = 130
		}
		if n == 2 {
			v = 400
		}
		exec(row, ws, pl, "plausible", "traffic", day(n), "", v, v*2, nil, nil, nil)
		// Search Console runs two days behind.
		if n >= 2 {
			clicks, pos := 10.0, 8.0
			if n < 9 {
				clicks, pos = 20, 6
			}
			exec(row, ws, sc, "google_search_console", "traffic", day(n), "", nil, nil, clicks, clicks*10, pos)
		}
		if n < 14 {
			pricing, blog := 10.0, 20.0
			if n < 7 {
				pricing, blog = 30, 5
			}
			exec(row, ws, pl, "plausible", "pages", day(n), "/pricing", nil, pricing, nil, nil, nil)
			exec(row, ws, pl, "plausible", "pages", day(n), "/blog", nil, blog, nil, nil, nil)
		}
		if n >= 2 && n < 16 {
			q := 2.0
			if n < 9 {
				q = 8
			}
			exec(row, ws, sc, "google_search_console", "search_queries", day(n), "cold email", nil, nil, q, q*10, 5)
		}
	}
	snap := `INSERT INTO seo_snapshots (workspace_id, integration_id, connector, host, report, day, country, key, metrics) VALUES ($1, $2, 'dataforseo', 'example.com', $3, $4, 'US', $5, $6)`
	exec(snap, ws, dfs, "overview", day(7), "", `{"domain_rating": 40, "organic_keywords": 1000, "top3": 12}`)
	exec(snap, ws, dfs, "overview", day(0), "", `{"domain_rating": 42, "organic_keywords": 1100, "top3": 15}`)
	exec(snap, ws, dfs, "keywords", day(7), "cold email", `{"position": 6, "volume": 900}`)
	exec(snap, ws, dfs, "keywords", day(0), "cold email", `{"position": 4, "volume": 900}`)
	exec(snap, ws, dfs, "keywords", day(7), "gone", `{"position": 30}`)
	exec(snap, ws, dfs, "keywords", day(0), "new one", `{"position": 9}`)

	got := owner.must(200, "GET", base+"/analytics/changes", nil)
	sites := got["websites"].([]any)
	if len(sites) != 1 {
		t.Fatalf("websites: %v", sites)
	}
	site := sites[0].(map[string]any)
	if site["host"] != "example.com" || len(site["tools"].([]any)) != 2 {
		t.Fatalf("site: %v", site)
	}
	tools := map[string]map[string]any{}
	for _, x := range site["tools"].([]any) {
		tool := x.(map[string]any)
		tools[tool["name"].(string)] = tool
	}
	visitors := metric(tools["Plausible"], "visitors")
	if visitors["now"].(float64) != 1180 || visitors["before"].(float64) != 700 || visitors["change_pct"].(float64) != 68.6 {
		t.Fatalf("visitors: %v", visitors)
	}
	gsc := tools["Search Console"]
	if gsc["now"].(map[string]any)["to"] != day(2) || gsc["now"].(map[string]any)["days_stored"].(float64) != 7 {
		t.Fatalf("lagging tool keeps its own dates: %v", gsc)
	}
	if p := metric(gsc, "position"); p["now"].(float64) != 6 || p["before"].(float64) != 8 {
		t.Fatalf("position: %v", p)
	}
	unusual := site["unusual_days"].([]any)
	if len(unusual) != 1 || unusual[0].(map[string]any)["day"] != day(2) || !strings.Contains(unusual[0].(map[string]any)["what"].(string), "times the usual") {
		t.Fatalf("unusual: %v", unusual)
	}
	movers := map[string]map[string]any{}
	for _, x := range site["movers"].([]any) {
		m := x.(map[string]any)
		movers[m["report"].(string)] = m
	}
	pages := movers["pages"]
	up, down := pages["up"].([]any)[0].(map[string]any), pages["down"].([]any)[0].(map[string]any)
	if pages["metric"] != "pageviews" || up["key"] != "/pricing" || up["change"].(float64) != 140 || down["key"] != "/blog" || down["change"].(float64) != -105 {
		t.Fatalf("pages: %v", pages)
	}
	if q := movers["search_queries"]["up"].([]any)[0].(map[string]any); q["key"] != "cold email" || q["change"].(float64) != 42 {
		t.Fatalf("queries: %v", movers["search_queries"])
	}
	seo := site["seo"].([]any)[0].(map[string]any)
	if dr := metric(seo, "domain_rating"); dr["now"].(float64) != 42 || dr["before"].(float64) != 40 {
		t.Fatalf("seo: %v", seo)
	}
	if kw := seo["keywords_up"].([]any)[0].(map[string]any); kw["keyword"] != "cold email" || kw["previous"].(float64) != 6 || seo["keywords_new"].(float64) != 1 || seo["keywords_lost"].(float64) != 1 {
		t.Fatalf("keywords: %v", seo)
	}
	head := strings.Join(toStrings(site["headline"]), "\n")
	for _, want := range []string{"Visitors 1,180, up 68.6% on the 7 days before (700) (Plausible, " + day(6) + " to " + day(0) + ")",
		"Search clicks 140, up 100%", "Biggest rise by page: /pricing", "Unusual day: " + day(2), "SEO (DataForSEO", "domain rating 42.0 (+2.0)"} {
		if !strings.Contains(head, want) {
			t.Fatalf("headline missing %q:\n%s", want, head)
		}
	}
	if hasGap(site["gaps"], "No search console") || !hasGap(site["gaps"], "No conversions") || hasGap(site["gaps"], "No SEO") {
		t.Fatalf("gaps: %v", site["gaps"])
	}
	if got["units"].(map[string]any)["position"] == nil {
		t.Fatalf("units: %v", got["units"])
	}
	// A shorter period, one website.
	short := owner.must(200, "GET", base+"/analytics/changes?days=1&host=www.example.com&limit=1", nil)
	if v := metric(short["websites"].([]any)[0].(map[string]any)["tools"].([]any)[0].(map[string]any), "visitors"); v["now"] == nil {
		t.Fatalf("short: %v", short)
	}

	cat := owner.must(200, "GET", base+"/analytics/catalog", nil)
	c := cat["data"].([]any)[0].(map[string]any)
	tool := c["tools"].([]any)[0].(map[string]any)
	if c["host"] != "example.com" || len(c["tools"].([]any)) != 2 || len(tool["measures"].(map[string]any)["traffic"].([]any)) == 0 || tool["last_day"] != day(0) {
		t.Fatalf("catalog: %v", c)
	}
	s := c["seo"].([]any)[0].(map[string]any)
	if s["snapshots"].(float64) != 2 || s["budget_unit"] != "USD" || cat["ask"].(map[string]any)["what changed"] == nil {
		t.Fatalf("catalog seo: %v", cat)
	}
}

func metric(tool map[string]any, name string) map[string]any {
	for _, x := range tool["metrics"].([]any) {
		if m := x.(map[string]any); m["metric"] == name {
			return m
		}
	}
	return map[string]any{}
}

func hasGap(v any, what string) bool {
	list, _ := v.([]any)
	for _, x := range list {
		if strings.HasPrefix(x.(map[string]any)["what"].(string), what) {
			return true
		}
	}
	return false
}
