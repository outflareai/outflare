package api_test

import (
	"context"
	"testing"
	"time"
)

// Insights, site analytics and the digest read the brand's reporting time
// zone (plans/ad-tracker.md, B4b): a day is the brand's, ?timezone=
// overrides it, and periods stay whole across a daylight-saving change.
func TestBrandTimezoneInsights(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	owner.must(200, "PATCH", "/v1/workspaces/"+ws+"/brands/"+brand, map[string]any{"timezone": "Europe/Berlin"})
	base := "/v1/brands/" + brand
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	var ana, bo string
	for _, x := range []*string{&ana, &bo} {
		if err := e.pool.QueryRow(ctx, `INSERT INTO records (workspace_id, brand_id, record_type, name) VALUES ($1, $2, 'person', 'P') RETURNING id::text`, ws, brand).Scan(x); err != nil {
			t.Fatal(err)
		}
	}
	event := func(rec string, at time.Time) {
		exec(`INSERT INTO customer_events (workspace_id, brand_id, record_id, kind, name, occurred_at, properties) VALUES ($1, $2, $3, 'track', 'opened_app', $4, '{}')`,
			ws, brand, rec, at)
	}

	// 23:30 UTC on 10 October is 01:30 on 11 October in Berlin.
	late := time.Date(2026, 10, 10, 23, 30, 0, 0, time.UTC)
	event(ana, late)
	exec(`INSERT INTO site_sessions (workspace_id, brand_id, anonymous_id, started_at, last_seen_at, landing_path) VALUES ($1, $2, 'v1', $3, $3, '/')`, ws, brand, late)
	trend := func(query, day string) float64 {
		t.Helper()
		res := owner.must(200, "POST", base+"/insights/trend"+query, map[string]any{"interval": "day",
			"date_range": map[string]any{"from": day, "to": day},
			"series":     []map[string]any{{"kind": "event", "event": "opened_app"}}})
		s := res["series"].([]any)[0].(map[string]any)
		if total := s["total"]; total != nil {
			return total.(float64)
		}
		return 0
	}
	if n := trend("", "2026-10-11"); n != 1 {
		t.Errorf("Berlin's 11 October: %v", n)
	}
	if n := trend("", "2026-10-10"); n != 0 {
		t.Errorf("Berlin's 10 October: %v", n)
	}
	if n := trend("?timezone=UTC", "2026-10-10"); n != 1 {
		t.Errorf("UTC's 10 October: %v", n)
	}
	owner.must(400, "POST", base+"/insights/trend?timezone=Nowhere/Land", map[string]any{"series": []map[string]any{{"kind": "visit"}}})

	// Site analytics counts the visit on Berlin's day, and its series is
	// in Berlin's days.
	site := owner.must(200, "GET", base+"/analytics/tracking?from=2026-10-11&to=2026-10-11", nil)
	if s := site["series"].([]any); len(s) != 1 || s[0].(map[string]any)["date"] != "2026-10-11" || s[0].(map[string]any)["sessions"] != float64(1) {
		t.Fatalf("site series: %v", s)
	}
	if n := owner.must(200, "GET", base+"/analytics/tracking?from=2026-10-10&to=2026-10-10", nil)["totals"].(map[string]any)["sessions"]; n != float64(0) {
		t.Fatalf("site visits on Berlin's 10 October: %v", n)
	}

	// Berlin's clocks go back on 25 October, a 25-hour day. Ana is active
	// on the 24th, 25th and 26th: returning on the 25th and 26th. Bo on
	// the 24th and 26th: resurrected on the 26th, dormant on the 25th.
	at := func(day int) time.Time { return time.Date(2026, 10, day, 12, 0, 0, 0, time.UTC) }
	for _, d := range []int{24, 25, 26} {
		event(ana, at(d))
	}
	event(bo, at(24))
	event(bo, at(26))
	res := owner.must(200, "POST", base+"/insights/lifecycle", map[string]any{"step": map[string]any{"kind": "event", "event": "opened_app"}, "interval": "day",
		"date_range": map[string]any{"from": "2026-10-24", "to": "2026-10-26"}})
	periods := res["periods"].([]any)
	if len(periods) != 3 {
		t.Fatalf("lifecycle periods: %v", periods)
	}
	p25, p26 := periods[1].(map[string]any), periods[2].(map[string]any)
	// Periods start at the brand's midnights, and say so in their offset.
	if p25["start"] != "2026-10-25T00:00:00+02:00" || p26["start"] != "2026-10-26T00:00:00+01:00" {
		t.Fatalf("period starts: %v, %v", p25["start"], p26["start"])
	}
	if p25["returning"] != float64(1) || p25["dormant"] != float64(1) || p26["returning"] != float64(1) || p26["resurrected"] != float64(1) {
		t.Fatalf("lifecycle across the clock change: %v / %v", p25, p26)
	}

	// The digest is sent and counted in the brand's zone until it has its
	// own.
	if d := owner.must(200, "GET", base+"/digest-settings", nil); d["timezone"] != "Europe/Berlin" {
		t.Fatalf("digest time zone: %v", d["timezone"])
	}
}
