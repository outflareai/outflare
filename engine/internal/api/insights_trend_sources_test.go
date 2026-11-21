package api_test

import (
	"context"
	"testing"
	"time"
)

// Trend series from stored tool data: Search Console and Bing daily
// totals (a property read through two connections counts once), the
// brand's referring domains over time, and its link-order spend. Another
// brand's rows never show.
func TestTrendSourceSeries(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	brand := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	other := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Acme", "handle": "acme-two"})["id"].(string)
	base := "/v1/brands/" + brand
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	integration := func(connector, name string) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, `INSERT INTO integrations (workspace_id, connector, name, credentials) VALUES ($1, $2, $3, '\x00') RETURNING id::text`, ws, connector, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	gsc1, gsc2, bing := integration("google_search_console", "Search Console"), integration("google_search_console", "Search Console 2"), integration("bing_webmaster", "Bing")
	today := time.Now().UTC().Truncate(24 * time.Hour)
	day := func(n int) string { return today.AddDate(0, 0, -n).Format(time.DateOnly) }
	row := `INSERT INTO analytics_daily (workspace_id, integration_id, brand_id, connector, site, host, report, day, key, clicks, impressions, position, synced_at)
		VALUES ($1, $2, $3, $4, $5, 'northpeak.example', 'traffic', $6, '', $7, $8, $9, $10)`
	for n := 1; n <= 3; n++ {
		// The same property through two Search Console connections: the
		// newer sync counts, once.
		exec(row, ws, gsc1, brand, "google_search_console", "sc-domain:northpeak.example", day(n), 999, 9999, 50, today.Add(-48*time.Hour))
		exec(row, ws, gsc2, brand, "google_search_console", "sc-domain:northpeak.example", day(n), 10, 100, 4, today)
		exec(row, ws, bing, brand, "bing_webmaster", "https://northpeak.example/", day(n), 2, 40, nil, today)
		exec(row, ws, bing, other, "bing_webmaster", "https://acme.example/", day(n), 500, 500, nil, today)
	}
	trend := func(series ...map[string]any) map[string]any {
		return owner.must(200, "POST", base+"/insights/trend", map[string]any{
			"date_range": map[string]any{"preset": "last_7_days"}, "interval": "day", "series": series,
		})
	}
	total := func(res map[string]any, i int) float64 {
		t.Helper()
		v, ok := res["series"].([]any)[i].(map[string]any)["total"].(float64)
		if !ok {
			t.Fatalf("series %d has no total: %v", i, res["series"].([]any)[i])
		}
		return v
	}
	res := trend(
		map[string]any{"kind": "analytics", "connector": "google_search_console"},
		map[string]any{"kind": "analytics", "measure": "impressions"},
		map[string]any{"kind": "analytics", "connector": "google_search_console", "measure": "position"},
		map[string]any{"kind": "analytics", "connector": "google_search_console", "measure": "ctr"},
	)
	if total(res, 0) != 30 || total(res, 1) != 420 || total(res, 2) != 4 || total(res, 3) != 0.1 {
		t.Fatalf("analytics totals: %v %v %v %v", total(res, 0), total(res, 1), total(res, 2), total(res, 3))
	}
	if label := res["series"].([]any)[0].(map[string]any)["label"]; label != "Search Console clicks" {
		t.Fatalf("label: %v", label)
	}
	byTool := owner.must(200, "POST", base+"/insights/trend", map[string]any{
		"date_range": map[string]any{"preset": "last_7_days"}, "interval": "week", "breakdown": "connector",
		"series": []map[string]any{{"kind": "analytics"}},
	})
	if n := len(byTool["breakdown"].([]any)); n != 2 {
		t.Fatalf("breakdown by connector: %v", byTool["breakdown"])
	}

	// Referring domains: one live since 20 days ago, one found 5 days ago,
	// one lost 2 days ago, one nofollow; another brand's link stays out.
	var site, otherSite string
	if err := e.pool.QueryRow(ctx, `INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token) VALUES ($1, $2, 'northpeak.example', 'https://northpeak.example/', 'tok') RETURNING id`, ws, brand).Scan(&site); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token) VALUES ($1, $2, 'acme.example', 'https://acme.example/', 'tok2') RETURNING id`, ws, other).Scan(&otherSite); err != nil {
		t.Fatal(err)
	}
	link := `INSERT INTO backlinks (workspace_id, brand_id, site_id, source_url, target_url, source_domain, nofollow, status, first_seen_at, lost_at)
		VALUES ($1, $2, $3, $4, 'https://northpeak.example/', $5, $6, $7, $8, $9)`
	ago := func(n int) time.Time { return today.Add(-time.Duration(n)*24*time.Hour + time.Hour) }
	exec(link, ws, brand, site, "https://old.example/a", "old.example", false, "live", ago(20), nil)
	exec(link, ws, brand, site, "https://new.example/a", "new.example", false, "live", ago(5), nil)
	exec(link, ws, brand, site, "https://gone.example/a", "gone.example", false, "lost", ago(30), ago(2))
	exec(link, ws, brand, site, "https://nf.example/a", "nf.example", true, "live", ago(25), nil)
	exec(link, ws, other, otherSite, "https://other.example/a", "other.example", false, "live", ago(25), nil)
	res = trend(
		map[string]any{"kind": "backlinks"},
		map[string]any{"kind": "backlinks", "measure": "dofollow_referring_domains"},
		map[string]any{"kind": "backlinks", "measure": "new_referring_domains"},
		map[string]any{"kind": "backlinks", "measure": "lost_referring_domains"},
	)
	if total(res, 0) != 3 || total(res, 1) != 2 || total(res, 2) != 1 || total(res, 3) != 1 {
		t.Fatalf("backlinks totals: %v %v %v %v", total(res, 0), total(res, 1), total(res, 2), total(res, 3))
	}
	// Seven days ago only old, gone and nf were live.
	if first := res["series"].([]any)[0].(map[string]any)["points"].([]any)[0].(map[string]any)["value"]; first != 3.0 {
		t.Fatalf("first point: %v", first)
	}

	// Link orders: one paid in pounds (its US-dollar amount counts), one
	// published and not paid.
	donor := owner.must(201, "POST", base+"/link-orders/catalog", map[string]any{"domain": "donor.example"})
	order := func(page string) string {
		return owner.must(201, "POST", base+"/link-orders", map[string]any{"donor_profile_id": donor["id"], "target_url": "https://northpeak.example/" + page})["id"].(string)
	}
	paid, published := order("one"), order("two")
	exec(`UPDATE coverage_link_orders SET status = 'paid', approved_at = $2, published_at = $2, paid_at = $3, usd_amount_minor = 12650, currency = 'USD', donor_amount_minor = 12650 WHERE id = $1`, paid, ago(4), ago(1))
	exec(`UPDATE coverage_link_orders SET status = 'published', approved_at = $2, published_at = $2, quoted_usd_minor = 9000 WHERE id = $1`, published, ago(3))
	res = trend(
		map[string]any{"kind": "link_orders"},
		map[string]any{"kind": "link_orders", "measure": "published"},
		map[string]any{"kind": "link_orders", "measure": "paid"},
	)
	s0 := res["series"].([]any)[0].(map[string]any)
	if total(res, 0) != 126.5 || s0["currency"] != "USD" || total(res, 1) != 2 || total(res, 2) != 1 {
		t.Fatalf("link orders: %v %v %v %v", total(res, 0), s0["currency"], total(res, 1), total(res, 2))
	}

	// Search Console detail: by query, and by country and device. The
	// same property through two connections counts once.
	exec(`INSERT INTO search_daily (workspace_id, brand_id, integration_id, site, host, day, page, query, clicks, impressions, position)
		SELECT $1, $2, i, 'sc-domain:northpeak.example', 'northpeak.example', current_date - 1, pg, q, cl * m, im * m, pos
		FROM (VALUES ($3::uuid, 100), ($4::uuid, 1)) ig(i, m),
			(VALUES ('https://northpeak.example/pricing', 'northpeak pricing', 10, 100, 2.0), ('https://northpeak.example/blog/a', 'what is northpeak', 2, 100, 8.0)) v(pg, q, cl, im, pos)`, ws, brand, gsc1, gsc2)
	exec(`UPDATE search_daily SET day = current_date - 3 WHERE integration_id = $1`, gsc1)
	exec(`INSERT INTO search_segments (workspace_id, brand_id, integration_id, site, host, day, page, country, device, clicks, impressions, position)
		VALUES ($1, $2, $3, 'sc-domain:northpeak.example', 'northpeak.example', current_date - 1, 'https://northpeak.example/pricing', 'usa', 'DESKTOP', 9, 150, 3),
			($1, $2, $3, 'sc-domain:northpeak.example', 'northpeak.example', current_date - 1, 'https://northpeak.example/pricing', 'deu', 'MOBILE', 3, 50, 6)`, ws, brand, gsc2)
	res = trend(
		map[string]any{"kind": "search"},
		map[string]any{"kind": "search", "measure": "position"},
		map[string]any{"kind": "search", "filters": []map[string]any{{"property": "query", "op": "contains", "value": "pricing"}}},
		map[string]any{"kind": "search", "filters": []map[string]any{{"property": "country", "op": "eq", "value": "USA"}}},
	)
	if total(res, 0) != 12 || total(res, 1) != 5 || total(res, 2) != 10 || total(res, 3) != 9 {
		t.Fatalf("search totals: %v %v %v %v", total(res, 0), total(res, 1), total(res, 2), total(res, 3))
	}
	for by, want := range map[string]int{"query": 2, "country": 2, "device": 2, "page": 2} {
		got := owner.must(200, "POST", base+"/insights/trend", map[string]any{
			"date_range": map[string]any{"preset": "last_7_days"}, "interval": "week", "breakdown": by,
			"series": []map[string]any{{"kind": "search"}},
		})
		if n := len(got["breakdown"].([]any)); n != want {
			t.Fatalf("search by %s: %d groups: %v", by, n, got["breakdown"])
		}
	}
	owner.must(400, "POST", base+"/insights/trend", map[string]any{"breakdown": "country", "series": []map[string]any{{"kind": "search", "filters": []map[string]any{{"property": "query", "op": "eq", "value": "x"}}}}})
	owner.must(400, "POST", base+"/insights/trend", map[string]any{"series": []map[string]any{{"kind": "search", "filters": []map[string]any{{"property": "plan", "op": "eq", "value": "x"}}}}})

	for _, bad := range []map[string]any{
		{"series": []map[string]any{{"kind": "analytics", "measure": "people"}}},
		{"series": []map[string]any{{"kind": "backlinks", "connector": "bing_webmaster"}}},
		{"series": []map[string]any{{"kind": "link_orders", "event": "signed_up"}}},
		{"series": []map[string]any{{"kind": "backlinks"}}, "breakdown": "connector"},
		{"series": []map[string]any{{"kind": "event", "event": "signed_up", "connector": "bing_webmaster"}}},
	} {
		owner.must(400, "POST", base+"/insights/trend", bad)
	}
	cat := owner.must(200, "GET", base+"/insights/catalog", nil)
	if conns := cat["analytics_connectors"].([]any); len(conns) != 2 {
		t.Fatalf("catalog connectors: %v", conns)
	}
}
