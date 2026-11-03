package api_test

import (
	"context"
	"testing"
)

// placementOf is the coverage placement a play wrote for a page: its kind
// and stage, or "" when none, so the link, news and reviews tests can see
// that an appearance they produced reached coverage.
func placementOf(t *testing.T, e *env, ws, url string) (kind, stage string) {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM coverage_placements WHERE workspace_id = $1 AND url = $2`, ws, url).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		return "", ""
	}
	var obs int
	if err := e.pool.QueryRow(context.Background(), `SELECT p.kind, p.stage, (SELECT count(*) FROM coverage_observations o WHERE o.placement_id = p.id)
		FROM coverage_placements p WHERE p.workspace_id = $1 AND p.url = $2`, ws, url).Scan(&kind, &stage, &obs); err != nil {
		t.Fatal(err)
	}
	if obs == 0 {
		t.Fatalf("placement for %s has no observation", url)
	}
	return kind, stage
}
