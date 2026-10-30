package api

// Overlapping tests: two tests of different flags running at once can
// share visitors. The checklist warns before a test starts, and the
// interactions endpoint checks a started test against every test whose
// time overlapped it, unless an exclusion group keeps their visitors apart.

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/outflareai/outflare/engine/internal/abtests"
)

// maxInteractionTests is how many overlapping tests the check looks at,
// the most recent first.
const maxInteractionTests = 10

type abOverlap struct {
	TestID  string `json:"test_id"`
	Name    string `json:"name"`
	FlagKey string `json:"flag_key"`
	Status  string `json:"status"`
}

// abOverlapping are the other tests, of other flags outside f's exclusion
// group, that ran during t (running now, or ended after t started).
func (s *Server) abOverlapping(ctx context.Context, ws string, t abTestView, f flagView) ([]abOverlap, error) {
	since := time.Now()
	if t.StartedAt != nil {
		since = *t.StartedAt
	}
	until := time.Now()
	if t.EndedAt != nil {
		until = *t.EndedAt
	}
	rows, err := s.pool.Query(ctx, `
		SELECT o.id::text, o.name, of.key, o.status FROM ab_tests o JOIN flags of ON of.id = o.flag_id AND of.workspace_id = o.workspace_id
		WHERE o.workspace_id = $1 AND o.id <> $2 AND o.flag_id <> $3 AND o.status IN ('running', 'done')
			AND o.started_at < $5 AND (o.ended_at IS NULL OR o.ended_at > $4)
			AND ($6 = '' OR of.exclusion_group <> $6)
		ORDER BY o.started_at DESC LIMIT $7`, ws, t.ID, t.FlagID, since, until, f.ExclusionGroup, maxInteractionTests)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []abOverlap{}
	for rows.Next() {
		var o abOverlap
		if err := rows.Scan(&o.TestID, &o.Name, &o.FlagKey, &o.Status); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// abTestInteractions checks a started test's first goal against each
// overlapping test: whether a version's effect differs by the other test's
// version.
func (s *Server) abTestInteractions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	t, ok := s.oneABTest(w, r)
	if !ok {
		return
	}
	if t.Status == "draft" {
		writeError(w, r, http.StatusConflict, "conflict", "this test hasn't started: interactions need visitors")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), abResultsTimeout)
	defer cancel()
	f, err := s.abFlag(ctx, nil, a.WorkspaceID, t.FlagID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	others, err := s.abOverlapping(ctx, a.WorkspaceID, t, f)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	to := time.Now()
	if t.EndedAt != nil {
		to = *t.EndedAt
	}
	spec, err := abSpec(ctx, s.pool, a.WorkspaceID, t, f, to)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	type row struct {
		abOverlap
		abtests.InteractionResult
	}
	out := []row{}
	for _, o := range others {
		res, err := abtests.Interaction(ctx, s.pool, a.WorkspaceID, spec, o.FlagKey)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		out = append(out, row{o, res})
	}
	writeJSON(w, http.StatusOK, map[string]any{"test_id": t.ID, "interactions": out})
}

// abOverlapCheck is the checklist's line for tests running at the same
// time on flags outside this one's exclusion group.
func (s *Server) abOverlapCheck(ctx context.Context, ws string, t abTestView, f flagView) (abCheck, bool, error) {
	others, err := s.abOverlapping(ctx, ws, t, f)
	if err != nil {
		return abCheck{}, false, err
	}
	var running []abOverlap
	for _, o := range others {
		if o.Status == "running" {
			running = append(running, o)
		}
	}
	if len(running) == 0 {
		return abCheck{}, false, nil
	}
	text := fmt.Sprintf("%d other test(s) are running, such as %q on flag %s, so visitors may be in both. Put the flags in one exclusion group to keep them apart, or check interactions once this one runs.",
		len(running), running[0].Name, running[0].FlagKey)
	return abCheck{"overlap", "warn", text}, true, nil
}
