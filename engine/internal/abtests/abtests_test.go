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
