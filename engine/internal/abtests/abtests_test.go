package abtests_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/abtests"
	"github.com/outflareai/outflare/engine/internal/db"
)

func TestCheckGoals(t *testing.T) {
	for _, c := range []struct {
		goals []abtests.Goal
		ok    bool
	}{
		{[]abtests.Goal{{Kind: "event", Name: "signup_started"}}, true},
		{[]abtests.Goal{{Kind: "page", Path: "/thanks"}, {Kind: "page", Path: "/docs/*"}}, true},
		{nil, false},
		{[]abtests.Goal{{Kind: "event", Name: "Signup Started"}}, false},
		{[]abtests.Goal{{Kind: "event", Name: abtests.ExposureEvent}}, false},
		{[]abtests.Goal{{Kind: "page", Path: "thanks"}}, false},
		{[]abtests.Goal{{Kind: "page", Path: "/a/*/b"}}, false},
		{[]abtests.Goal{{Kind: "click"}}, false},
		{make([]abtests.Goal, abtests.MaxGoals+1), false},
		{[]abtests.Goal{{Kind: "event", Name: "order_completed", Metric: "value", Property: "revenue", Cap: 1000, WindowHours: 72}}, true},
		{[]abtests.Goal{{Kind: "page", Path: "/pricing", Metric: "count"}}, true},
		{[]abtests.Goal{{Kind: "deal", Metric: "value"}, {Kind: "deal"}}, true},
		{[]abtests.Goal{{Kind: "event", Name: "order_completed", Metric: "value"}}, false},
		{[]abtests.Goal{{Kind: "event", Name: "order_completed", Metric: "value", Property: "a b"}}, false},
		{[]abtests.Goal{{Kind: "event", Name: "signup", Property: "revenue"}}, false},
		{[]abtests.Goal{{Kind: "page", Path: "/thanks", Metric: "value"}}, false},
		{[]abtests.Goal{{Kind: "deal", Metric: "count"}}, false},
		{[]abtests.Goal{{Kind: "event", Name: "signup", Metric: "mean"}}, false},
		{[]abtests.Goal{{Kind: "event", Name: "signup", Cap: 5}}, false},
		{[]abtests.Goal{{Kind: "event", Name: "signup", WindowHours: -1}}, false},
	} {
		if got := abtests.CheckGoals(c.goals) == ""; got != c.ok {
			t.Errorf("CheckGoals(%+v) ok = %v, want %v", c.goals, got, c.ok)
		}
	}
}

func TestWinner(t *testing.T) {
	r := abtests.Result{Goals: []abtests.GoalResult{{Comparisons: []abtests.Comparison{{Variant: "b", ChanceToBeat: 0.99, PValue: 0.01}}}}}
	if w := abtests.Winner(r, "control"); w != "b" {
		t.Errorf("winner = %q", w)
	}
	r.SampleRatio = true
	if w := abtests.Winner(r, "control"); w != "" {
		t.Errorf("a sample ratio mismatch has no winner, got %q", w)
	}
	r = abtests.Result{Goals: []abtests.GoalResult{{Comparisons: []abtests.Comparison{{Variant: "b", ChanceToBeat: 0.01, PValue: 0.01}}}}}
	if w := abtests.Winner(r, "control"); w != "control" {
		t.Errorf("control should win, got %q", w)
	}
	r = abtests.Result{Goals: []abtests.GoalResult{{Comparisons: []abtests.Comparison{{Variant: "b", ChanceToBeat: 0.97, PValue: 0.2}}}}}
	if w := abtests.Winner(r, "control"); w != "" {
		t.Errorf("not proven yet, got %q", w)
	}
}

// TestResults stores exposures and goal events and checks the counts:
// a visitor counts once under the first version they saw, converts only
// when the goal follows the exposure, and events outside the window or of
// another flag or workspace are left out.
func TestResults(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	newWS := func() string {
		var ws string
		if err := pool.QueryRow(ctx, `INSERT INTO workspaces (name, slug) VALUES ('AB', 'ab-' || substr(gen_random_uuid()::text, 1, 8)) RETURNING id::text`).Scan(&ws); err != nil {
			t.Fatal(err)
		}
		return ws
	}
	ws, other := newWS(), newWS()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	at := func(h int) time.Time { return start.Add(time.Duration(h) * time.Hour) }
	add := func(ws, anon, name string, when time.Time, props, context string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, kind, name, anonymous_id, properties, context, occurred_at)
			VALUES ($1, 'track', $2, $3, $4, $5, $6)`, ws, name, anon, props, context, when); err != nil {
			t.Fatal(err)
		}
	}
	seen := func(ws, anon, flag, variant string, h int) {
		add(ws, anon, abtests.ExposureEvent, at(h), fmt.Sprintf(`{"flag": %q, "variant": %q}`, flag, variant), `{}`)
	}
	page := func(anon, path string, h int) {
		add(ws, anon, "page.viewed", at(h), `{}`, fmt.Sprintf(`{"page": {"path": %q}}`, path))
	}
	// Control: 4 visitors, 1 converts after seeing it, 1 converted only
	// before (doesn't count).
	for i := range 4 {
		seen(ws, fmt.Sprint("c", i), "hero", "control", 1)
	}
	add(ws, "c0", "signup_started", at(2), `{}`, `{}`)
	add(ws, "c1", "signup_started", at(0), `{}`, `{}`)
	// New: 3 visitors, 2 convert; n0 later also sees control (still new).
	for i := range 3 {
		seen(ws, fmt.Sprint("n", i), "hero", "new", 1)
	}
	seen(ws, "n0", "hero", "control", 3)
	add(ws, "n0", "signup_started", at(4), `{}`, `{}`)
	add(ws, "n1", "signup_started", at(4), `{}`, `{}`)
	add(ws, "n1", "signup_started", at(5), `{}`, `{}`)
	page("n2", "/docs/start", 6)
	page("c3", "/thanks", 6)
	// Left out: another flag, another workspace, after the end.
	seen(ws, "x0", "pricing", "new", 1)
	seen(other, "o0", "hero", "new", 1)
	seen(ws, "late", "hero", "new", 100)

	spec := abtests.Spec{FlagKey: "hero", Variants: []string{"new", "control"}, Weights: []int{50, 50}, Control: "control",
		Goals: []abtests.Goal{{Kind: "event", Name: "signup_started"}, {Kind: "page", Path: "/docs/*"}, {Kind: "page", Path: "/thanks"}},
		From:  start, To: at(48)}
	res, err := abtests.Results(ctx, pool, ws, spec)
	if err != nil {
		t.Fatal(err)
	}
	want := func(g int, variant string, visitors, conversions int64) {
		t.Helper()
		for _, a := range res.Goals[g].Arms {
			if a.Variant == variant {
				if a.Visitors != visitors || a.Conversions != conversions {
					t.Errorf("goal %d %s = %d/%d, want %d/%d", g, variant, a.Conversions, a.Visitors, conversions, visitors)
				}
				return
			}
		}
		t.Errorf("goal %d has no %s arm", g, variant)
	}
	if res.Arms[0].Variant != "control" || len(res.Arms) != 2 {
		t.Fatalf("arms = %+v, want control first", res.Arms)
	}
	want(0, "control", 4, 1)
	want(0, "new", 3, 2)
	want(1, "control", 4, 0)
	want(1, "new", 3, 1)
	want(2, "control", 4, 1)
	want(2, "new", 3, 0)
	if len(res.Goals[0].Comparisons) != 1 || res.Goals[0].Comparisons[0].Variant != "new" {
		t.Errorf("comparisons = %+v", res.Goals[0].Comparisons)
	}
	if res.SampleRatio {
		t.Errorf("4 vs 3 is not a mismatch: p = %v", res.SampleRatioP)
	}
	// Everything happened on 1 October: one daily look, for the first goal
	// only, ending where the totals are.
	if d := res.Goals[0].Days; len(d) != 1 || d[0].Date != "2026-10-01" || d[0].Arms[1].Conversions != 2 || res.Goals[1].Days != nil {
		t.Errorf("days = %+v", res.Goals[0].Days)
	}

	// Traffic for the planner: visitors with a page view, and those who
	// reached the goal after their first one.
	page("p0", "/", 0)
	add(ws, "p0", "signup_started", at(1), `{}`, `{}`)
	signup := abtests.Goal{Kind: "event", Name: "signup_started"}
	for _, tc := range []struct {
		page                string
		visitors, converted int64
	}{{"", 3, 1}, {"/docs/*", 1, 0}, {"/", 1, 1}} {
		tr, err := abtests.ReadTraffic(ctx, pool, ws, signup, tc.page, start, at(48))
		if err != nil {
			t.Fatal(err)
		}
		if tr.Visitors != tc.visitors || tr.Converted != tc.converted {
			t.Errorf("traffic on %q = %+v, want %d/%d", tc.page, tr, tc.converted, tc.visitors)
		}
	}
}

// TestResultsMetrics checks goals measured per visitor: how many times
// each reached the goal, the value on their events (capped), a conversion
// window, and the value of won deals of the person a browser belongs to.
func TestResultsMetrics(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var ws string
	if err := pool.QueryRow(ctx, `INSERT INTO workspaces (name, slug) VALUES ('AB', 'ab-' || substr(gen_random_uuid()::text, 1, 8)) RETURNING id::text`).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	at := func(h int) time.Time { return start.Add(time.Duration(h) * time.Hour) }
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	add := func(anon, name string, h int, props string) {
		exec(`INSERT INTO customer_events (workspace_id, kind, name, anonymous_id, properties, occurred_at) VALUES ($1, 'track', $2, $3, $4, $5)`,
			ws, name, anon, props, at(h))
	}
	order := func(anon string, h int, revenue string) { add(anon, "order_completed", h, `{"revenue": `+revenue+`}`) }
	for _, v := range []struct{ anon, variant string }{{"c0", "control"}, {"c1", "control"}, {"n0", "new"}, {"n1", "new"}} {
		add(v.anon, abtests.ExposureEvent, 1, fmt.Sprintf(`{"flag": "checkout", "variant": %q}`, v.variant))
	}
	// c0 buys twice on day one (50 + 30); c1 buys nothing. n0 buys 500 on
	// day one and 20 a day later; n1 sends a revenue that isn't a number,
	// and buys 40 more than 3 days after joining.
	order("c0", 2, "50")
	order("c0", 3, "30")
	order("n0", 2, "500")
	order("n0", 26, "20")
	order("n1", 2, `"lots"`)
	order("n1", 80, "40")
	// c1's browser belongs to a person with a won deal of $1,200 after they
	// joined, an open one, and a won one before they joined.
	var person string
	if err := pool.QueryRow(ctx, `INSERT INTO records (workspace_id, record_type, name) VALUES ($1, 'person', 'Ana') RETURNING id::text`, ws).Scan(&person); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO site_visitors (workspace_id, anonymous_id, record_id, first_seen_at, last_seen_at) VALUES ($1, 'c1', $2, $3, $3)`, ws, person, at(0))
	for _, d := range []struct {
		state string
		cents int
		h     int
	}{{"won", 120000, 30}, {"open", 90000, 30}, {"won", 70000, 0}} {
		exec(`INSERT INTO deals (workspace_id, source, state, amount_cents, currency, record_id, closed_at) VALUES ($1, 'manual', $2, $3, 'USD', $4, $5)`,
			ws, d.state, d.cents, person, at(d.h))
	}

	goals := []abtests.Goal{
		{Kind: "event", Name: "order_completed", Metric: abtests.MetricCount},
		{Kind: "event", Name: "order_completed", Metric: abtests.MetricValue, Property: "revenue"},
		{Kind: "event", Name: "order_completed", Metric: abtests.MetricValue, Property: "revenue", Cap: 100, WindowHours: 72},
		{Kind: "deal", Metric: abtests.MetricValue},
		{Kind: "deal"},
	}
	res, err := abtests.Results(ctx, pool, ws, abtests.Spec{FlagKey: "checkout", Variants: []string{"control", "new"}, Weights: []int{50, 50},
		Control: "control", Goals: goals, From: start, To: at(24 * 7)})
	if err != nil {
		t.Fatal(err)
	}
	want := func(g int, conversions []int64, sums []float64) {
		t.Helper()
		gr := res.Goals[g]
		for i, a := range gr.Arms {
			if a.Visitors != 2 || a.Conversions != conversions[i] || a.Sum != sums[i] {
				t.Errorf("goal %d %s: %d visitors, %d converted, sum %v; want 2, %d, %v", g, a.Variant, a.Visitors, a.Conversions, a.Sum, conversions[i], sums[i])
			}
		}
		if gr.Metric != goals[g].MetricOf() || gr.Exploratory != (g > 0) {
			t.Errorf("goal %d: metric %q, exploratory %v", g, gr.Metric, gr.Exploratory)
		}
	}
	want(0, []int64{1, 2}, []float64{2, 4})
	want(1, []int64{1, 2}, []float64{80, 560})
	// Capped at 100 each: c0 80, n0 100; n1's 40 is past the window.
	want(2, []int64{1, 1}, []float64{80, 100})
	want(3, []int64{1, 0}, []float64{1200, 0})
	want(4, []int64{1, 0}, []float64{0, 0})
	if m := res.Goals[1].Arms[1].Mean; m != 280 {
		t.Errorf("mean revenue per visitor of new = %v, want 280", m)
	}
	// The count goal's days: new's orders grow 2, 3, 4 over 1, 2 and 4
	// October, and the sum of squares follows each visitor's running total
	// (1+1, then 4+1, then 4+4).
	d := res.Goals[0].Days
	if len(d) != 3 || d[2].Date != "2026-10-04" || d[1].Arms[1].Sum != 3 || d[1].Arms[1].SumSq != 5 || d[2].Arms[1].SumSq != 8 {
		t.Errorf("count goal days = %+v", d)
	}
	if res.CorrectedFor != 1 || len(res.Goals[1].Comparisons) != 1 {
		t.Errorf("corrected for %d, comparisons %+v", res.CorrectedFor, res.Goals[1].Comparisons)
	}

	// The planner reads the same values from recent traffic.
	add("c0", "page.viewed", 0, `{}`)
	add("n0", "page.viewed", 0, `{}`)
	tr, err := abtests.ReadTraffic(ctx, pool, ws, goals[1], "", start, at(24*7))
	if err != nil {
		t.Fatal(err)
	}
	if tr.Visitors != 2 || tr.Converted != 2 || tr.Sum != 600 || tr.SumSq != 80*80+520*520 {
		t.Errorf("traffic = %+v", tr)
	}
	if b := abtests.Baseline(tr, abtests.MetricValue); b != 0 {
		t.Errorf("2 visitors is too few for a baseline, got %v", b)
	}
}

// TestBreakdown groups visitors by the visit they first saw their version
// in: device, source and new or returning, "unknown" without a visit, and
// the smaller groups as "other"; every comparison is corrected for every
// version in every group.
func TestBreakdown(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var ws string
	if err := pool.QueryRow(ctx, `INSERT INTO workspaces (name, slug) VALUES ('AB', 'ab-' || substr(gen_random_uuid()::text, 1, 8)) RETURNING id::text`).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	at := func(h int) time.Time { return start.Add(time.Duration(h) * time.Hour) }
	session := func(anon, device, utm, referrer, country string, h int) string {
		t.Helper()
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO site_sessions (workspace_id, anonymous_id, started_at, last_seen_at, device, utm_source, referrer_host, country)
			VALUES ($1, $2, $3, $3, $4, $5, $6, $7) RETURNING id::text`, ws, anon, at(h), device, utm, referrer, country).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	add := func(anon, name string, h int, props, context string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, kind, name, anonymous_id, properties, context, occurred_at) VALUES ($1, 'track', $2, $3, $4, $5, $6)`,
			ws, name, anon, props, context, at(h)); err != nil {
			t.Fatal(err)
		}
	}
	type visitor struct {
		anon, variant, device, utm, referrer, country string
		before, signup                                bool
	}
	visitors := []visitor{
		{"a", "control", "mobile", "newsletter", "", "DE", true, true},
		{"b", "control", "desktop", "", "news.example", "FR", false, false},
		{"c", "new", "mobile", "", "", "DE", false, true},
		{"d", "new", "mobile", "", "", "US", false, true},
		{"e", "new", "desktop", "Newsletter", "", "GB", false, false},
	}
	for i, v := range visitors {
		if v.before {
			session(v.anon, v.device, "", "", v.country, 0)
		}
		id := session(v.anon, v.device, v.utm, v.referrer, v.country, 1)
		add(v.anon, abtests.ExposureEvent, 1, fmt.Sprintf(`{"flag": "hero", "variant": %q}`, v.variant), fmt.Sprintf(`{"sessionId": %q}`, id))
		if v.signup {
			add(v.anon, "signup_started", 2+i, `{}`, `{}`)
		}
	}
	// A server-sent exposure has no visit.
	add("srv", abtests.ExposureEvent, 1, `{"flag": "hero", "variant": "new"}`, `{}`)
	// Nine more countries, one control visitor each, to fold into "other".
	for i, c := range []string{"AT", "BE", "CH", "DK", "ES", "FI", "IE", "IT", "NL"} {
		anon := fmt.Sprint("x", i)
		id := session(anon, "desktop", "", "", c, 1)
		add(anon, abtests.ExposureEvent, 1, `{"flag": "hero", "variant": "control"}`, fmt.Sprintf(`{"sessionId": %q}`, id))
	}

	spec := abtests.Spec{FlagKey: "hero", Variants: []string{"control", "new"}, Weights: []int{50, 50}, Control: "control",
		Goals: []abtests.Goal{{Kind: "event", Name: "signup_started"}}, From: start, To: at(48)}
	seg := func(b abtests.BreakdownResult) map[string][]abtests.Arm {
		m := map[string][]abtests.Arm{}
		for _, s := range b.Segments {
			m[s.Key] = s.Arms
		}
		return m
	}
	check := func(dim string, want map[string][4]int64) abtests.BreakdownResult {
		t.Helper()
		b, err := abtests.Breakdown(ctx, pool, ws, spec, dim)
		if err != nil {
			t.Fatal(err)
		}
		got := seg(b)
		for key, w := range want {
			a := got[key]
			if len(a) != 2 || a[0].Visitors != w[0] || a[0].Conversions != w[1] || a[1].Visitors != w[2] || a[1].Conversions != w[3] {
				t.Errorf("%s %s = %+v, want control %d/%d, new %d/%d", dim, key, a, w[1], w[0], w[3], w[2])
			}
		}
		return b
	}
	dev := check("device", map[string][4]int64{"mobile": {1, 1, 2, 2}, "desktop": {10, 0, 1, 0}, "unknown": {0, 0, 1, 0}})
	if dev.CorrectedFor != 3 || len(dev.Segments) != 3 || dev.Segments[0].Key != "desktop" || dev.Segments[0].Comparisons == nil {
		t.Errorf("device breakdown: %+v", dev)
	}
	check("source", map[string][4]int64{"newsletter": {1, 1, 1, 0}, "news.example": {1, 0, 0, 0}, "direct": {9, 0, 2, 2}})
	check("visitor", map[string][4]int64{"returning": {1, 1, 0, 0}, "new": {10, 0, 3, 2}, "unknown": {0, 0, 1, 0}})
	// 14 countries plus unknown: the largest 7, then the rest as other.
	country := check("country", map[string][4]int64{"DE": {1, 1, 1, 1}})
	if n := len(country.Segments); n != abtests.MaxSegments || country.Segments[n-1].Key != "other" || country.CorrectedFor != abtests.MaxSegments {
		t.Errorf("country breakdown: %d segments, last %q, corrected for %d", n, country.Segments[n-1].Key, country.CorrectedFor)
	}
	if b, err := abtests.Breakdown(ctx, pool, ws, spec, "browser"); err != nil || len(b.Segments) != 0 {
		t.Errorf("unknown dimension: %+v, %v", b, err)
	}
}
