package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestLinkROIReadsStoredMetricsOnly(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)
	root := "/v1/brands/" + brand + "/link-orders"
	ownerID := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	owner.must(200, "PATCH", root+"/settings", map[string]any{"approver_user_id": ownerID})
	owner.must(201, "POST", "/v1/brands/"+brand+"/coverage/budgets", map[string]any{
		"title": "USD", "currency": "USD", "amount_minor": 50000,
		"starts_on": "2000-01-01", "ends_on": "2099-12-31", "tags": "link_orders",
	})

	empty := owner.must(200, "GET", "/v1/brands/"+brand+"/link-roi", nil)
	if empty["causality"] != false || empty["note"] == "" {
		t.Fatalf("empty: %v", empty)
	}
	if empty["totals"].(map[string]any)["orders"] != 0.0 || len(empty["orders"].([]any)) != 0 {
		t.Fatalf("empty totals: %v", empty["totals"])
	}

	donor := owner.must(201, "POST", root+"/catalog", map[string]any{
		"domain":        "donor.example",
		"domain_rating": 45,
		"traffic":       12000,
		"prices":        []map[string]any{{"currency": "USD", "amount_minor": 1000}},
	})
	order := owner.must(201, "POST", root, map[string]any{
		"donor_profile_id": donor["id"], "target_url": "https://northpeak.example/page",
	})
	path := root + "/" + order["id"].(string)
	owner.must(200, "POST", path+"/quote", map[string]any{"amount_minor": 1000, "currency": "USD"})
	owner.must(200, "POST", path+"/approve", nil)
	owner.must(200, "POST", path+"/publish", map[string]any{"published_url": "https://donor.example/post"})

	// Still requested, so it counts as open and is not in the published rows.
	owner.must(201, "POST", root, map[string]any{
		"donor_profile_id": donor["id"], "target_url": "https://northpeak.example/other",
	})

	pubAt := time.Now().UTC().AddDate(0, 0, -100).Truncate(time.Hour)
	if _, err := e.pool.Exec(ctx, `UPDATE coverage_link_orders SET published_at = $1 WHERE id = $2`, pubAt, order["id"]); err != nil {
		t.Fatal(err)
	}

	site := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://northpeak.example/", "brand_id": brand})["id"].(string)
	seo := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})["id"].(string)
	if _, err := e.pool.Exec(ctx, `DELETE FROM jobs WHERE kind = 'seo.sync'`); err != nil {
		t.Fatal(err)
	}
	page := "https://northpeak.example/page"
	ins := func(day time.Time, clicks, impressions int, pos float64) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `INSERT INTO search_daily (workspace_id, integration_id, site, host, day, page, query, clicks, impressions, ctr, position)
			VALUES ($1, $2, 'sc-domain:northpeak.example', 'northpeak.example', $3, $4, 'proxy', $5, $6, 0.1, $7)`,
			ws, seo, day, page, clicks, impressions, pos); err != nil {
			t.Fatal(err)
		}
	}
	ins(pubAt.AddDate(0, 0, -10), 10, 100, 8)
	ins(pubAt.AddDate(0, 0, 10), 25, 200, 5)
	if _, err := e.pool.Exec(ctx, `INSERT INTO seo_snapshots (workspace_id, integration_id, connector, host, report, day, key, url, metrics)
		VALUES ($1, $2, 'dataforseo', 'northpeak.example', 'keywords', $3, 'proxy', $4, '{"position": 8}'::jsonb),
		       ($1, $2, 'dataforseo', 'northpeak.example', 'keywords', $5, 'proxy', $4, '{"position": 4}'::jsonb)`,
		ws, seo, pubAt.AddDate(0, 0, -5), page, pubAt.AddDate(0, 0, 5)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO backlinks (workspace_id, brand_id, site_id, source_url, target_url, source_domain, first_seen_at)
		VALUES ($1, $2, $3, 'https://old.example/a', $4, 'old.example', $5),
		       ($1, $2, $3, 'https://donor.example/post', $4, 'donor.example', $6)`,
		ws, brand, site, page, pubAt.AddDate(0, 0, -20), pubAt.AddDate(0, 0, 3)); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "provider must not be called", http.StatusTeapot)
	}))
	t.Cleanup(fake.Close)
	if e.integrations.Client.BaseURLs == nil {
		e.integrations.Client.BaseURLs = map[string]string{}
	}
	for _, id := range []string{"dataforseo", "ahrefs", "se_ranking", "google_search_console"} {
		e.integrations.Client.BaseURLs[id] = fake.URL
	}

	before := calls.Load()
	got := owner.must(200, "GET", "/v1/brands/"+brand+"/link-roi", nil)
	if calls.Load() != before {
		t.Fatalf("link roi called a provider")
	}
	if got["causality"] != false || got["note"] == "" {
		t.Fatalf("payload: %v", got)
	}
	tot := got["totals"].(map[string]any)
	if tot["orders"] != 2.0 || tot["published"] != 1.0 || tot["open"] != 1.0 {
		t.Fatalf("totals: %v", tot)
	}
	if tot["published_with_cost"] != 1.0 || tot["with_traffic"] != 1.0 || tot["with_rank"] != 1.0 || tot["with_referring_domains"] != 1.0 {
		t.Fatalf("coverage: %v", tot)
	}
	spend := tot["spend"].([]any)
	if len(spend) != 1 || spend[0].(map[string]any)["currency"] != "USD" || spend[0].(map[string]any)["amount_minor"] != 1000.0 {
		t.Fatalf("spend: %v", spend)
	}
	rows := got["orders"].([]any)
	if len(rows) != 1 {
		t.Fatalf("orders: %v", rows)
	}
	row := rows[0].(map[string]any)
	if row["domain"] != "donor.example" || row["target_url"] != page || row["amount_minor"] != 1000.0 || row["donor_domain_rating"] != 45.0 || row["donor_traffic"] != 12000.0 {
		t.Fatalf("order: %v", row)
	}
	var w30 map[string]any
	for _, w := range row["windows"].([]any) {
		m := w.(map[string]any)
		if m["days"] == 30.0 {
			w30 = m
		}
		if m["elapsed"] != true {
			t.Fatalf("window should have elapsed: %v", m)
		}
	}
	if w30 == nil || w30["traffic"] == nil {
		t.Fatalf("30-day window: %v", row["windows"])
	}
	tr := w30["traffic"].(map[string]any)
	if tr["clicks_before"] != 10.0 || tr["clicks_after"] != 25.0 || tr["clicks_change"] != 15.0 {
		t.Fatalf("traffic: %v", tr)
	}
	if w30["cost_per_click_gained"] != 1000.0/15.0 {
		t.Fatalf("cost per click: %v", w30["cost_per_click_gained"])
	}
	rk := w30["rank"].(map[string]any)
	if rk["position_before"] != 8.0 || rk["position_after"] != 4.0 || rk["position_change"] != -4.0 {
		t.Fatalf("rank: %v", rk)
	}
	rd := w30["referring_domains"].(map[string]any)
	if rd["before"] != 1.0 || rd["after"] != 2.0 || rd["change"] != 1.0 {
		t.Fatalf("referring domains: %v", rd)
	}
	if w30["cost_per_referring_domain_gained"] != 1000.0 {
		t.Fatalf("cost per rd: %v", w30["cost_per_referring_domain_gained"])
	}
	pages := got["pages"].([]any)
	if len(pages) != 1 || pages[0].(map[string]any)["links"] != 1.0 {
		t.Fatalf("pages: %v", pages)
	}

	other := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Other"})["id"].(string)
	obr := owner.must(200, "GET", "/v1/workspaces/"+other+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	isolated := owner.must(200, "GET", "/v1/brands/"+obr+"/link-roi", nil)
	if isolated["totals"].(map[string]any)["orders"] != 0.0 {
		t.Fatalf("other brand: %v", isolated)
	}
}
