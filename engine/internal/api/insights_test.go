package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

// TestFunnels covers people funnels (the owner's ask of 2026-11-04): only
// people who did step 1 count later, each step has its own time limit
// from an earlier step, a visit before sign-up joins the person once they
// are identified, "first payment" means the first, and drop-offs list the
// people behind them. Saved funnels are audited.
func TestFunnels(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand, other string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	other = owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Other brand"})["id"].(string)
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana"})["id"].(string)
	bo := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Bo"})["id"].(string)
	cy := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Cy"})["id"].(string)

	day := 24 * time.Hour
	t0 := time.Now().UTC().Truncate(day).Add(-10*day + time.Hour)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	visit := func(b, anon string, record any, clickIDs string, at time.Time, campaign string) {
		exec(`INSERT INTO site_sessions (workspace_id, brand_id, anonymous_id, record_id, started_at, last_seen_at, click_ids, channel, utm_campaign, landing_path)
			VALUES ($1, $2, $3, $4, $5, $5, $6::jsonb, 'paid_ads', $7, '/pricing')`, ws, b, anon, record, at, clickIDs, campaign)
	}
	event := func(name string, record any, anon any, at time.Time) {
		exec(`INSERT INTO customer_events (workspace_id, brand_id, record_id, anonymous_id, kind, name, occurred_at)
			VALUES ($1, $2, $3, $4, 'track', $5, $6)`, ws, brand, record, anon, name, at)
	}
	payment := func(record string, id string, at time.Time) {
		exec(`INSERT INTO transactions (workspace_id, brand_id, record_id, kind, amount_minor, brand_amount_minor, source, source_id, occurred_at)
			VALUES ($1, $2, $3, 'payment', 4900, 4900, 'stripe', $4, $5)`, ws, brand, record, id, at)
	}
	// Ana and Bo came from Google ads before signing up (their browsers
	// were identified since), someone else came from Google and never
	// signed up, Cy came from Meta. One visit belongs to another brand.
	visit(brand, "b-ana", ana, `{"gclid":"x1"}`, t0, "brand-search")
	visit(brand, "b-bo", bo, `{"gclid":"x2"}`, t0.Add(time.Hour), "brand-search")
	visit(brand, "b-anon", nil, `{"gbraid":"x3"}`, t0.Add(2*time.Hour), "competitors")
	visit(brand, "b-cy", cy, `{"fbclid":"x4"}`, t0.Add(3*time.Hour), "")
	visit(other, "b-other", nil, `{"gclid":"x5"}`, t0, "brand-search")
	event("signed_up", ana, "b-ana", t0.Add(30*time.Minute))
	event("signed_up", bo, "b-bo", t0.Add(2*time.Hour))
	event("signed_up", cy, "b-cy", t0.Add(4*time.Hour))
	// Signing up again later doesn't count twice.
	event("signed_up", ana, nil, t0.Add(day))
	// Ana pays within 3 days of signing up; Bo after 5 days; Cy paid once
	// before (so their next one isn't a first payment).
	payment(ana, "in_a1", t0.Add(2*day))
	payment(ana, "in_a2", t0.Add(3*day))
	payment(bo, "in_b1", t0.Add(5*day))
	payment(cy, "in_c0", t0.Add(-30*day))
	payment(cy, "in_c1", t0.Add(5*time.Hour))

	from := t0.Add(-day).Format(time.DateOnly)
	to := time.Now().UTC().Format(time.DateOnly)
	two := 2
	funnel := map[string]any{
		"date_range": map[string]any{"from": from, "to": to},
		"steps": []map[string]any{
			{"kind": "visit", "filters": []map[string]any{{"property": "network", "op": "eq", "value": "google"}}},
			{"kind": "event", "event": "signed_up", "within": map[string]any{"value": 7, "unit": "day"}},
			{"kind": "payment", "first": true, "within": map[string]any{"value": 3, "unit": "day", "from": two}},
		},
		"breakdown": "utm_campaign",
		"interval":  "day",
		"ad_clicks": map[string]any{"network": "google"},
	}
	res := owner.must(200, "POST", base+"/insights/funnel", funnel)
	steps := res["steps"].([]any)
	count := func(s any) float64 { return s.(map[string]any)["count"].(float64) }
	if len(steps) != 3 || count(steps[0]) != 3 || count(steps[1]) != 2 || count(steps[2]) != 1 {
		t.Fatalf("steps: %v", steps)
	}
	s2 := steps[1].(map[string]any)
	if s2["dropped"] != 1.0 || s2["label"] != "signed_up" || s2["median_seconds"] == nil {
		t.Fatalf("step 2: %v", s2)
	}
	if steps[2].(map[string]any)["label"] != "First payment" {
		t.Fatalf("label: %v", steps[2])
	}
	groups := res["breakdown"].([]any)
	if len(groups) != 2 || groups[0].(map[string]any)["value"] != "brand-search" || count(groups[0].(map[string]any)["steps"].([]any)[2]) != 1 {
		t.Fatalf("breakdown: %v", groups)
	}
	if len(res["over_time"].([]any)) != 1 {
		t.Fatalf("over time: %v", res["over_time"])
	}
	if ac := res["ad_clicks"].(map[string]any); ac["clicks"] != 0.0 {
		t.Fatalf("ad clicks: %v", ac)
	}

	// Without the 3-day limit Bo's payment counts; Cy's never does from
	// Meta, and with any network Cy's isn't a first payment.
	delete(funnel["steps"].([]map[string]any)[2], "within")
	if c := count(owner.must(200, "POST", base+"/insights/funnel", funnel)["steps"].([]any)[2]); c != 2 {
		t.Fatalf("no limit: %v", c)
	}
	delete(funnel["steps"].([]map[string]any)[0], "filters")
	res = owner.must(200, "POST", base+"/insights/funnel", funnel)
	if steps := res["steps"].([]any); count(steps[0]) != 4 || count(steps[1]) != 3 || count(steps[2]) != 2 {
		t.Fatalf("all networks: %v", steps)
	}

	// The people behind a drop-off: who visited from Google and never
	// signed up, a browser with no record yet.
	funnel["steps"].([]map[string]any)[0]["filters"] = []map[string]any{{"property": "network", "op": "eq", "value": "google"}}
	people := owner.must(200, "POST", base+"/insights/funnel/people", map[string]any{"funnel": funnel, "step": 2, "dropped": true})
	list := people["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["anonymous_id"] != "b-anon" || list[0].(map[string]any)["record_id"] != nil {
		t.Fatalf("dropped: %v", people)
	}
	people = owner.must(200, "POST", base+"/insights/funnel/people", map[string]any{"funnel": funnel, "step": 3, "breakdown": "brand-search"})
	if list := people["data"].([]any); len(list) != 2 || list[0].(map[string]any)["name"] != "Bo" || len(list[0].(map[string]any)["times"].([]any)) != 3 {
		t.Fatalf("converted: %v", people)
	}
	owner.must(400, "POST", base+"/insights/funnel/people", map[string]any{"funnel": funnel, "step": 1, "dropped": true})

	// Specs the caller can fix.
	for _, bad := range []map[string]any{
		{"steps": []map[string]any{{"kind": "visit"}}},
		{"steps": []map[string]any{{"kind": "visit"}, {"kind": "event", "event": "Not A Name"}}},
		{"steps": []map[string]any{{"kind": "visit"}, {"kind": "conversion", "conversion": "nope"}}},
		{"steps": []map[string]any{{"kind": "visit", "filters": []map[string]any{{"property": "colour", "op": "eq", "value": "x"}}}, {"kind": "page"}}},
		{"steps": []map[string]any{{"kind": "visit"}, {"kind": "page", "within": map[string]any{"value": 2, "unit": "year"}}}},
		{"steps": []map[string]any{{"kind": "visit"}, {"kind": "page"}}, "breakdown": "colour"},
		{"steps": []map[string]any{{"kind": "visit"}, {"kind": "page"}}, "date_range": map[string]any{"preset": "forever"}},
	} {
		owner.must(400, "POST", base+"/insights/funnel", bad)
	}

	// Page and event filters, including a query parameter.
	exec(`INSERT INTO customer_events (workspace_id, brand_id, anonymous_id, kind, name, occurred_at, context)
		VALUES ($1, $2, 'b-anon', 'page', 'page.viewed', $3, '{"page": {"path": "/pricing", "search": "?ref=hn&x=1"}}')`, ws, brand, t0.Add(3*time.Hour))
	pageFunnel := map[string]any{"date_range": map[string]any{"from": from, "to": to}, "steps": []map[string]any{
		{"kind": "visit"},
		{"kind": "page", "filters": []map[string]any{{"property": "param:ref", "op": "eq", "value": "HN"}, {"property": "path", "op": "contains", "value": "pric"}}},
	}}
	if c := count(owner.must(200, "POST", base+"/insights/funnel", pageFunnel)["steps"].([]any)[1]); c != 1 {
		t.Fatalf("page by parameter: %v", c)
	}

	// The catalog lists what funnels can use.
	cat := owner.must(200, "GET", base+"/insights/catalog", nil)
	if len(cat["events"].([]any)) < 2 || cat["breakdowns"].(map[string]any)["visit"] == nil {
		t.Fatalf("catalog: %v", cat)
	}

	// Saved funnels.
	saved := owner.must(201, "POST", base+"/insights", map[string]any{"name": "Google ads to first purchase", "spec": map[string]any{
		"date_range": map[string]any{"preset": "last_30_days"}, "steps": funnel["steps"]}})
	id := saved["id"].(string)
	owner.must(400, "POST", base+"/insights", map[string]any{"name": "Bad", "spec": map[string]any{"steps": []any{}}})
	owner.must(400, "POST", base+"/insights", map[string]any{"name": "", "spec": saved["spec"]})
	if l := owner.must(200, "GET", base+"/insights", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("list: %v", l)
	}
	up := owner.must(200, "PATCH", base+"/insights/"+id, map[string]any{"name": "Ads to purchase"})
	if up["name"] != "Ads to purchase" || up["spec"].(map[string]any)["date_range"].(map[string]any)["preset"] != "last_30_days" {
		t.Fatalf("updated: %v", up)
	}
	// Another brand can't see it.
	owner.must(404, "GET", "/v1/brands/"+other+"/insights/"+id, nil)
	owner.must(204, "DELETE", base+"/insights/"+id, nil)
	owner.must(404, "GET", base+"/insights/"+id, nil)

	var audits int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action IN ('insight.created', 'insight.updated', 'insight.deleted')`,
		ws).Scan(&audits); err != nil || audits != 3 {
		t.Fatalf("audits: %d %v", audits, err)
	}
	if ev := named(got(), "insight.created"); len(ev) != 1 || ev[0].Props["steps"] != 3 {
		t.Fatalf("event: %v", ev)
	}
}
