package api_test

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSEOOverviewByBrand(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}

	// Nothing stored: the brand's entry, with empty weeks.
	empty := owner.must(200, "GET", base+"/seo/overview", nil)["data"].([]any)
	if len(empty) != 1 || empty[0].(map[string]any)["brand"] == nil || len(empty[0].(map[string]any)["weeks"].([]any)) != 12 {
		t.Fatalf("empty: %v", empty)
	}
	if st := empty[0].(map[string]any)["seo_status"].(map[string]any); st["state"] != "not_connected" || st["integration_id"] != nil {
		t.Fatalf("no SEO tool: %v", st)
	}
	owner.must(400, "GET", base+"/seo/overview?weeks=3", nil)
	owner.must(404, "GET", base+"/seo/overview?brand=nope", nil)

	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Geonode", "website": "geonode.com"})
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "website": "repocket.com"})
	sc := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "name": "Search Console", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})["id"].(string)
	pl := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "name": "Plausible", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})["id"].(string)
	dfs := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})["id"].(string)

	// The overview ends on the last Sunday that is over.
	today := time.Now().UTC().Truncate(24 * time.Hour)
	to := today.AddDate(0, 0, -int((today.Weekday()+6)%7)-1)
	day := func(n int) string { return to.AddDate(0, 0, -n).Format(time.DateOnly) }
	for _, id := range []string{sc, pl} {
		exec(`INSERT INTO analytics_syncs (integration_id, workspace_id, site, host, first_day, last_day, backfilled) VALUES ($1, $2, 'repocket.com', 'repocket.com', $3, $4, true)`, id, ws, day(83), day(0))
	}
	exec(`INSERT INTO analytics_syncs (integration_id, workspace_id, site, host, first_day, last_day, backfilled) VALUES ($1, $2, 'example.com', 'example.com', $3, $4, true)`, owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "name": "Other", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})["id"], ws, day(83), day(0))
	row := `INSERT INTO analytics_daily (workspace_id, integration_id, connector, site, host, report, day, key, visitors, sessions, clicks, impressions)
		VALUES ($1, $2, $3, 'repocket.com', 'repocket.com', $4, $5, $6, $7, $8, $9, $10)`
	for n := 0; n < 56; n++ {
		// Branded searches doubled in the last four weeks; the rest did not.
		branded := 10.0
		if n < 28 {
			branded = 20
		}
		exec(row, ws, sc, "google_search_console", "traffic", day(n), "", nil, nil, branded+50, (branded+50)*10)
		exec(row, ws, sc, "google_search_console", "search_queries", day(n), "Repocket app", nil, nil, branded, branded*10)
		exec(row, ws, sc, "google_search_console", "search_queries", day(n), "passive income", nil, nil, 50.0, 500.0)
		exec(row, ws, pl, "plausible", "traffic", day(n), "", 100.0, 120.0, nil, nil)
	}
	snap := `INSERT INTO seo_snapshots (workspace_id, integration_id, connector, host, report, day, key, metrics) VALUES ($1, $2, $3, 'repocket.com', 'overview', $4, '', $5)`
	exec(snap, ws, dfs, "ahrefs", day(35), `{"domain_rating": 40, "backlinks": 1000, "referring_domains": 100}`)
	exec(snap, ws, dfs, "ahrefs", day(0), `{"domain_rating": 44, "backlinks": 900, "referring_domains": 101}`)
	owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://repocket.com/", "brand_id": "repocket"})

	// Connecting an SEO tool reads it straight away, not at the next
	// weekly snapshot.
	var queued int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'seo.sync' AND payload->>'integration_id' = $1`, dfs).Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("first read queued on connect: %d %v", queued, err)
	}

	res := owner.must(200, "GET", base+"/seo/overview?brand=repocket", nil)
	if res["to"] != day(0) {
		t.Fatalf("to: %v", res["to"])
	}
	list := res["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("one brand: %v", list)
	}
	rp := list[0].(map[string]any)
	if rp["brand"].(map[string]any)["name"] != "Repocket" || !hasString(rp["terms"], "repocket") || !hasString(rp["hosts"], "repocket.com") {
		t.Fatalf("brand: %v %v %v", rp["brand"], rp["terms"], rp["hosts"])
	}
	weeks := rp["weeks"].([]any)
	last := weeks[len(weeks)-1].(map[string]any)
	if last["branded_clicks"] != 140.0 || last["search_clicks"] != 490.0 || last["non_branded_clicks"] != 350.0 || last["visitors"] != 700.0 || last["sessions"] != 840.0 {
		t.Fatalf("last week: %v", last)
	}
	if last["search_ctr"] != 0.1 {
		t.Fatalf("non-branded CTR: %v", last["search_ctr"])
	}
	if first := weeks[0].(map[string]any); first["branded_clicks"] != nil || first["visitors"] != nil || first["non_branded_clicks"] != nil {
		t.Fatalf("a week with no data is unknown: %v", first)
	}
	trend := func(name string) map[string]any { return rp["trends"].(map[string]any)[name].(map[string]any) }
	if b := trend("branded_search"); b["direction"] != "up" || b["current"] != 560.0 || b["previous"] != 280.0 || b["change"] != 1.0 {
		t.Fatalf("branded: %v", b)
	}
	if n := trend("non_branded_search"); n["direction"] != "flat" || n["current"] != 1400.0 || n["previous"] != 1400.0 {
		t.Fatalf("non-branded: %v", n)
	}
	if c := trend("search_ctr"); c["direction"] != "flat" || c["current"] != 0.1 {
		t.Fatalf("ctr: %v", c)
	}
	if v := trend("visitors"); v["direction"] != "flat" {
		t.Fatalf("visitors: %v", v)
	}
	if d := trend("domain_rating"); d["direction"] != "up" || d["current"] != 44.0 || d["previous"] != 40.0 {
		t.Fatalf("dr: %v", d)
	}
	if b := trend("backlinks"); b["direction"] != "down" {
		t.Fatalf("backlinks: %v", b)
	}
	if r := trend("referring_domains"); r["direction"] != "flat" {
		t.Fatalf("referring domains: %v", r)
	}
	if seo := rp["seo"].([]any); len(seo) != 2 || seo[1].(map[string]any)["connector"] != "ahrefs" {
		t.Fatalf("seo: %v", seo)
	}
	// A period of its own: the weeks picked against as many before them.
	for _, q := range []string{"weeks=4", "from=" + day(27) + "&to=" + day(3), "from=" + day(25) + "&to=" + today.AddDate(1, 0, 0).Format(time.DateOnly)} {
		res := owner.must(200, "GET", base+"/seo/overview?brand=repocket&"+q, nil)
		if res["from"] != day(27) || res["to"] != day(0) || res["previous_from"] != day(55) || res["previous_to"] != day(28) {
			t.Fatalf("%s: period %v to %v, before %v to %v", q, res["from"], res["to"], res["previous_from"], res["previous_to"])
		}
		b := res["data"].([]any)[0].(map[string]any)
		if len(b["weeks"].([]any)) != 4 || len(b["previous_weeks"].([]any)) != 4 || b["previous_weeks"].([]any)[0].(map[string]any)["week"] != day(55) {
			t.Fatalf("%s: weeks %v before %v", q, b["weeks"], b["previous_weeks"])
		}
		period := b["period"].(map[string]any)
		if p := period["branded_search"].(map[string]any); p["direction"] != "up" || p["current"] != 560.0 || p["previous"] != 280.0 {
			t.Fatalf("%s: branded over the period: %v", q, p)
		}
		if p := period["domain_rating"].(map[string]any); p["current"] != 44.0 || p["previous"] != 40.0 {
			t.Fatalf("%s: rating over the period: %v", q, p)
		}
		// Trends still set the last 4 weeks against the 4 before.
		if tr := b["trends"].(map[string]any)["branded_search"].(map[string]any); tr["previous"] != 280.0 {
			t.Fatalf("%s: trend: %v", q, tr)
		}
	}
	// An earlier period ends where it was asked to.
	if res := owner.must(200, "GET", base+"/seo/overview?brand=repocket&from="+day(55)+"&to="+day(29), nil); res["to"] != day(28) || len(res["data"].([]any)[0].(map[string]any)["weeks"].([]any)) != 4 {
		t.Fatalf("earlier period: %v to %v", res["from"], res["to"])
	}
	for _, q := range []string{"weeks=105", "from=" + day(0) + "&to=" + day(7), "from=2020-01-01", "from=yesterday"} {
		owner.must(400, "GET", base+"/seo/overview?brand=repocket&"+q, nil)
	}
	if sites := rp["sites"].([]any); len(sites) != 1 || rp["open_fixes"] != 0.0 {
		t.Fatalf("sites: %v fixes %v", sites, rp["open_fixes"])
	}
	// The brand's websites are read first, then its analytics domains.
	srcs := owner.must(200, "GET", base+"/seo?brand=repocket", nil)["data"].([]any)
	var followed []string
	for _, d := range srcs[0].(map[string]any)["domains"].([]any) {
		followed = append(followed, d.(map[string]any)["host"].(string))
	}
	if strings.Join(followed, " ") != "repocket.com" {
		t.Fatalf("domains read: %v", followed)
	}
	status := func(c *client, brand string) map[string]any {
		t.Helper()
		return c.must(200, "GET", base+"/seo/overview?brand="+brand, nil)["data"].([]any)[0].(map[string]any)["seo_status"].(map[string]any)
	}
	if st := status(owner, "repocket"); st["state"] != "reading" || st["integration_id"] != dfs || st["connector"] != "dataforseo" || !strings.Contains(st["message"].(string), "repocket.com") {
		t.Fatalf("first read: %v", st)
	}
	exec(`INSERT INTO seo_syncs (integration_id, host, workspace_id, last_run_at, last_ok_at, last_cost) VALUES ($1, 'repocket.com', $2, now(), now(), 1)`, dfs, ws)
	if st := status(owner, "repocket"); st["state"] != "ready" || st["last_ok_at"] == nil {
		t.Fatalf("read: %v", st)
	}
	exec(`UPDATE seo_syncs SET last_error = 'DataForSEO''s monthly budget is $10.00 and $10.00 is spent.' WHERE integration_id = $1`, dfs)
	if st := status(owner, "repocket"); st["state"] != "failed" || !strings.Contains(st["message"].(string), "monthly budget") || st["last_ok_at"] == nil {
		t.Fatalf("failed: %v", st)
	}
	// A domain the connection was told not to read says so.
	owner.must(200, "PUT", base+"/seo/"+dfs+"/settings", map[string]any{"domains": []string{"example.com"}})
	if st := status(owner, "repocket"); st["state"] != "not_followed" || !strings.Contains(st["message"].(string), "example.com") {
		t.Fatalf("not followed: %v", st)
	}
	owner.must(200, "PUT", base+"/seo/"+dfs+"/settings", map[string]any{"domains": nil})
	// New settings, such as a raised budget, try the failed read again now.
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'seo.sync' AND payload->>'integration_id' = $1 AND (payload->>'retry')::boolean`, dfs).Scan(&queued); err != nil || queued == 0 {
		t.Fatalf("retry queued on settings: %d %v", queued, err)
	}

	// A snapshot taken this week shows straight away, before the week ends.
	exec(snap, ws, dfs, "ahrefs", today.Format(time.DateOnly), `{"domain_rating": 45, "backlinks": 950, "referring_domains": 102, "organic_traffic": 3000, "organic_keywords": 120}`)
	rp = owner.must(200, "GET", base+"/seo/overview?brand=repocket", nil)["data"].([]any)[0].(map[string]any)
	if seo := rp["seo"].([]any); seo[len(seo)-1].(map[string]any)["day"] != today.Format(time.DateOnly) {
		t.Fatalf("this week's snapshot: %v", seo)
	}
	tr := rp["trends"].(map[string]any)
	if tr["domain_rating"].(map[string]any)["current"] != 45.0 || tr["organic_traffic"].(map[string]any)["current"] != 3000.0 || tr["organic_keywords"].(map[string]any)["current"] != 120.0 {
		t.Fatalf("latest numbers: %v", tr)
	}
	// A later snapshot from another tool must not replace the Ahrefs series.
	dfs2 := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})["id"].(string)
	exec(snap, ws, dfs2, "dataforseo", today.AddDate(0, 0, 1).Format(time.DateOnly), `{"domain_rating": 20, "organic_traffic": 1, "top3": 9}`)
	exec(`UPDATE seo_snapshots SET metrics = metrics || '{"top3": 80}' WHERE connector = 'ahrefs' AND day = $1`, today.Format(time.DateOnly))
	rp = owner.must(200, "GET", base+"/seo/overview?brand=repocket", nil)["data"].([]any)[0].(map[string]any)
	tr = rp["trends"].(map[string]any)
	if tr["domain_rating"].(map[string]any)["current"] != 45.0 || tr["keywords_top3"].(map[string]any)["current"] != 80.0 {
		t.Fatalf("one connector: %v", tr)
	}
	if seo := rp["seo"].([]any); seo[len(seo)-1].(map[string]any)["connector"] != "ahrefs" {
		t.Fatalf("mixed connector: %v", seo[len(seo)-1])
	}
	// Each brand's own overview: never another brand's, nor a shared entry.
	one := func(brand string) any {
		t.Helper()
		l := owner.must(200, "GET", base+"/seo/overview?brand="+brand, nil)["data"].([]any)
		if len(l) != 1 {
			t.Fatalf("%s: %v", brand, l)
		}
		return l[0]
	}
	all := []any{one("geonode"), one("repocket")}
	if gn := all[0].(map[string]any); gn["brand"].(map[string]any)["name"] != "Geonode" || gn["trends"].(map[string]any)["branded_search"].(map[string]any)["direction"] != "unknown" {
		t.Fatalf("geonode: %v", gn)
	}
	// Search and visitor numbers for other websites only: say which, not
	// "connect a tool".
	source := func(b any, kind string) map[string]any {
		return b.(map[string]any)["sources"].(map[string]any)[kind].(map[string]any)
	}
	for _, kind := range []string{"search", "traffic"} {
		if s := source(all[1], kind); s["state"] != "ready" {
			t.Fatalf("repocket %s: %v", kind, s)
		}
		if s := source(all[0], kind); s["state"] != "other_sites" || !hasString(s["hosts"], "repocket.com") {
			t.Fatalf("geonode %s: %v", kind, s)
		}
	}
	// An SEO tool shared with other brands only is "not shared", not
	// "not connected".
	owner.must(200, "PATCH", base+"/integrations/"+dfs, map[string]any{"brand_ids": []string{"repocket"}})
	owner.must(200, "PATCH", base+"/integrations/"+dfs2, map[string]any{"brand_ids": []string{"repocket"}})
	if st := status(owner, "geonode"); st["state"] != "not_shared" || st["integration_id"] != dfs && st["integration_id"] != dfs2 {
		t.Fatalf("not shared: %v", st)
	}
	owner.must(200, "PATCH", base+"/integrations/"+dfs, map[string]any{"brand_ids": []string{}})
	owner.must(200, "PATCH", base+"/integrations/"+dfs2, map[string]any{"brand_ids": []string{}})

	// A token that may read analytics but not SEO sites gets no audits.
	secret := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Reports", "scopes": []string{"analytics.read"}})["token"].(string)
	bot := &client{t: t, h: e.h, bearer: secret}
	got := bot.must(200, "GET", base+"/seo/overview?brand=repocket", nil)["data"].([]any)[0].(map[string]any)
	if len(got["sites"].([]any)) != 0 || got["open_fixes"] != nil {
		t.Fatalf("no seo.read: %v %v", got["sites"], got["open_fixes"])
	}
	// Search totals without query rows are not zero branded clicks.
	exec(`INSERT INTO analytics_daily (workspace_id, integration_id, connector, site, host, report, day, key, clicks, impressions)
		VALUES ($1, $2, 'google_search_console', 'geonode.com', 'geonode.com', 'traffic', $3, '', 100, 1000)`, ws, sc, day(0))
	gn := owner.must(200, "GET", base+"/seo/overview?brand=geonode", nil)["data"].([]any)[0].(map[string]any)
	lastGN := gn["weeks"].([]any)[len(gn["weeks"].([]any))-1].(map[string]any)
	if lastGN["search_clicks"] != 100.0 || lastGN["branded_clicks"] != nil || lastGN["non_branded_clicks"] != nil {
		t.Fatalf("missing queries are unknown: %v", lastGN)
	}

	// Nor why a read failed: that is in the SEO settings.
	if st := status(bot, "repocket"); st["state"] != "failed" || strings.Contains(st["message"].(string), "budget") {
		t.Fatalf("failure reason without seo.read: %v", st)
	}
}

func hasString(list any, want string) bool {
	for _, v := range list.([]any) {
		if v == want {
			return true
		}
	}
	return false
}
