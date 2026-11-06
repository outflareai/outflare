package api

import "testing"

// TestAgentRunsCallBrandRoutes: a run's call to a route that moved under
// /v1/brands/{brand}/ goes to the run's brand; the team's routes stay on
// the workspace.
func TestAgentRunsCallBrandRoutes(t *testing.T) {
	for _, c := range []struct {
		method, path string
		brand        bool
	}{
		{"GET", "/records", true},
		{"GET", "/records?limit=5", true},
		{"GET", "/members", false},
		{"GET", "/no-such-route", false},
	} {
		if got := onBrandRoute(c.method, c.path); got != c.brand {
			t.Errorf("%s %s: brand route %v, want %v", c.method, c.path, got, c.brand)
		}
	}
}
