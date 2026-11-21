package api_test

import (
	"context"
	"testing"
	"time"
)

func TestSEOBoard(t *testing.T) {
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

	northpeak := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "website": "northpeak.example"})["id"].(string)
	summitly := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Summitly", "website": "summitly.example"})["id"].(string)
	board := "/v1/brands/" + northpeak + "/seo/board"

	// Nothing stored: every tile, every number null, not zero.
	empty := owner.must(200, "GET", board, nil)
	tiles := empty["periods"].([]any)
	if len(tiles) != 5 || tiles[0].(map[string]any)["id"] != "today" || tiles[4].(map[string]any)["id"] != "last_month" {
		t.Fatalf("tiles: %v", tiles)
	}
	if m := tiles[2].(map[string]any)["metrics"].(map[string]any); m["revenue"] != nil || m["clicks"] != nil {
		t.Fatalf("nothing stored is null: %v", m)
	}
	if empty["search_through"] != nil || len(empty["pages"].([]any)) != 0 || len(empty["countries"].([]any)) != 0 {
		t.Fatalf("empty board: %v", empty)
	}
	owner.must(400, "GET", board+"?period=nope", nil)

	today, err := time.Parse(time.DateOnly, empty["today"].(string))
	if err != nil {
		t.Fatal(err)
	}
	day := func(n int) string { return today.AddDate(0, 0, -n).Format(time.DateOnly) }
	integration := func(name string) string {
		return owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "name": name, "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})["id"].(string)
	}
	ph, sc := integration("PostHog"), integration("Search Console")

	conv := `INSERT INTO organic_conversions (workspace_id, brand_id, integration_id, site, day, host, path, canonical_path, organic_sessions, signups, payments, revenue_usd)
		VALUES ($1, $2, $3, '1', $4, $5, '/pricing', '/pricing', $6, $7, $8, $9)`
	exec(conv, ws, northpeak, ph, day(0), "www.northpeak.example", 5, 2, 1, 10)
	exec(conv, ws, northpeak, ph, day(7), "www.northpeak.example", 3, 1, 1, 5)
	// Another brand's money never shows on this one.
	exec(conv, ws, summitly, ph, day(0), "summitly.example", 50, 20, 10, 999)

	// Search Console is three days behind.
	exec(`INSERT INTO analytics_daily (workspace_id, brand_id, integration_id, connector, site, host, report, day, key, clicks, impressions, position)
		VALUES ($1, $2, $3, 'google_search_console', 'sc-domain:northpeak.example', 'northpeak.example', 'traffic', $4, '', 100, 1000, 5)`, ws, northpeak, sc, day(3))
	seg := `INSERT INTO search_segments (workspace_id, brand_id, integration_id, site, host, day, page, country, device, clicks, impressions, position)
		VALUES ($1, $2, $3, 'sc-domain:northpeak.example', 'northpeak.example', $4, 'https://www.northpeak.example/pricing', $5, 'DESKTOP', $6, $7, $8)`
	exec(seg, ws, northpeak, sc, day(3), "usa", 60, 600, 4)
	exec(seg, ws, northpeak, sc, day(3), "deu", 40, 400, 6)

	res := owner.must(200, "GET", board+"?period=today", nil)
	if res["search_through"] != day(3) || res["conversions_through"] != day(0) {
		t.Fatalf("through: %v %v", res["search_through"], res["conversions_through"])
	}
	td := res["periods"].([]any)[0].(map[string]any)
	if td["compare_from"] != day(7) {
		t.Fatalf("today is set against the same weekday last week: %v", td)
	}
	m, prev := td["metrics"].(map[string]any), td["previous"].(map[string]any)
	if m["revenue"] != 10.0 || m["payments"] != 1.0 || m["signups"] != 2.0 || m["organic_sessions"] != 5.0 || prev["revenue"] != 5.0 {
		t.Fatalf("today: %v against %v", m, prev)
	}
	if m["clicks"] != nil {
		t.Fatalf("no Search Console day yet is null: %v", m)
	}
	pages := res["pages"].([]any)
	if len(pages) != 1 {
		t.Fatalf("one page, this brand's only: %v", pages)
	}
	if p := pages[0].(map[string]any); p["host"] != "northpeak.example" || p["path"] != "/pricing" || p["revenue"] != 10.0 || p["previous_revenue"] != 5.0 {
		t.Fatalf("page: %v", p)
	}

	// The Search Console day is in this month or, early in a month, the last.
	tile, idx := "month_to_date", 2
	if today.AddDate(0, 0, -3).Month() != today.Month() {
		tile, idx = "last_month", 4
	}
	res = owner.must(200, "GET", board+"?period="+tile, nil)
	m = res["periods"].([]any)[idx].(map[string]any)["metrics"].(map[string]any)
	if m["clicks"] != 100.0 || m["impressions"] != 1000.0 || m["ctr"] != 0.1 || m["position"] != 5.0 {
		t.Fatalf("%s: %v", tile, m)
	}
	pages = res["pages"].([]any)
	if len(pages) != 1 {
		t.Fatalf("pages: %v", pages)
	}
	if p := pages[0].(map[string]any); p["clicks"] != 100.0 || p["impressions"] != 1000.0 || p["position"] != 4.8 {
		t.Fatalf("page from the segments: %v", p)
	}
	countries := res["countries"].([]any)
	if len(countries) != 2 || countries[0].(map[string]any)["country"] != "usa" || countries[0].(map[string]any)["clicks"] != 60.0 {
		t.Fatalf("countries: %v", countries)
	}

	// The forecast scales the month so far to the whole month.
	if tile == "month_to_date" {
		fc := res["periods"].([]any)[3].(map[string]any)
		monthDays := float64(time.Date(today.Year(), today.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day())
		through := float64(today.AddDate(0, 0, -3).Day())
		if fm := fc["metrics"].(map[string]any); fc["forecast"] != true || fm["clicks"] != 100/through*monthDays {
			t.Fatalf("forecast: %v", fc)
		}
	}
}
