package api

// Multivariate tests: a test can add up to two other flags (factors), each
// with its control, and is then read cell by cell (every combination of
// versions) as well as on its own flag. Factors are set while it is a
// draft, kept apart from exclusion groups that would keep them from
// overlapping, and held by the test while it runs.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/abtests"
	"github.com/outflareai/outflare/engine/internal/flags"
)

// abFactor is one flag a multivariate test adds to its own.
type abFactor struct {
	FlagID  string `json:"flag_id"`
	FlagKey string `json:"flag_key"`
	Control string `json:"control"`
}

type abFactorInput struct {
	Flag    string `json:"flag"`
	Control string `json:"control"`
}

// setABFactors checks in for t (of flag f) and gives t its factors.
func (s *Server) setABFactors(ctx context.Context, tx pgx.Tx, ws string, t *abTestView, f flagView, in []abFactorInput) error {
	if t.Status != "draft" {
		return errABConflict("a test's factors are fixed once it starts")
	}
	if len(in) > abtests.MaxFactors {
		return errInvalid(fmt.Sprintf("a multivariate test adds at most %d flags to its own", abtests.MaxFactors))
	}
	out := []abFactor{}
	for _, x := range in {
		g, err := s.abFlag(ctx, tx, ws, x.Flag)
		if err != nil {
			return errInvalid("each factor's flag is the id or key of a live feature flag")
		}
		switch {
		case g.ID == f.ID || slices.ContainsFunc(out, func(o abFactor) bool { return o.FlagID == g.ID }):
			return errInvalid("each factor is a different flag from the test's own and the other factor")
		case g.Status == "archived":
			return errInvalid("factor " + g.Key + " is archived")
		case len(g.Variants) < 2:
			return errInvalid("factor " + g.Key + " has one version: give it at least two")
		case f.ExclusionGroup != "" && g.ExclusionGroup == f.ExclusionGroup:
			return errInvalid("factor " + g.Key + " is in the same exclusion group as the test's flag, so no visitor would see both: take one out of the group")
		}
		control := x.Control
		if control == "" {
			control = g.Variants[0].Key
			if slices.ContainsFunc(g.Variants, func(v flags.Variant) bool { return v.Key == "control" }) {
				control = "control"
			}
		}
		if !abtests.HasVariant(variantKeys(g), control) {
			return errInvalid("factor " + g.Key + "'s control is one of its versions: " + strings.Join(variantKeys(g), ", "))
		}
		out = append(out, abFactor{FlagID: g.ID, FlagKey: g.Key, Control: control})
	}
	t.Factors = out
	raw, _ := json.Marshal(out)
	_, err := tx.Exec(ctx, `UPDATE ab_tests SET factors = $3, updated_at = now() WHERE id = $1 AND workspace_id = $2`, t.ID, ws, raw)
	return err
}

// abFactorsBusy says whether flag (or a factor of t) is already in another
// running test, as its flag or a factor: a flag answers one test at a time.
func abFactorsBusy(ctx context.Context, tx pgx.Tx, ws string, t abTestView) error {
	ids := []string{t.FlagID}
	for _, x := range t.Factors {
		ids = append(ids, x.FlagID)
	}
	var name string
	err := tx.QueryRow(ctx, `
		SELECT o.name FROM ab_tests o
		WHERE o.workspace_id = $1 AND o.status = 'running' AND o.id <> $2
			AND (o.flag_id::text = ANY($3) OR EXISTS (SELECT 1 FROM jsonb_array_elements(o.factors) x WHERE x->>'flag_id' = ANY($3)))
		LIMIT 1`, ws, t.ID, ids).Scan(&name)
	switch {
	case err == pgx.ErrNoRows:
		return nil
	case err != nil:
		return err
	}
	return errABConflict(fmt.Sprintf("test %q is running on one of these flags: end it first, since a flag answers one test at a time", name))
}

// startABFactors turns on each factor's flag that is off, after checking
// it is still fit to test.
func (s *Server) startABFactors(ctx context.Context, tx pgx.Tx, a *access, t abTestView) error {
	for _, x := range t.Factors {
		g, err := s.abFlag(ctx, tx, a.WorkspaceID, x.FlagID)
		if err != nil {
			return err
		}
		switch {
		case g.Status == "archived":
			return errABConflict("factor " + g.Key + " is archived; change the factors first")
		case len(g.Variants) < 2 || !abtests.HasVariant(variantKeys(g), x.Control):
			return errABConflict("factor " + g.Key + " no longer has two versions with its control " + x.Control + "; change the factors first")
		case !a.writes(g.BrandID):
			return errDenied(brandLimitedMessage)
		}
		if g.Status == "off" {
			if _, err := tx.Exec(ctx, `UPDATE flags SET status = 'on', updated_at = now() WHERE id = $1 AND workspace_id = $2`, g.ID, a.WorkspaceID); err != nil {
				return err
			}
			data := flagAuditData(g)
			data["status"], data["was"] = "on", "off"
			if err := record(ctx, tx, &a.principal, a.WorkspaceID, "flag.updated", "flag", g.ID, data); err != nil {
				return err
			}
		}
	}
	return nil
}

// abTestFactorial reads a multivariate test's first goal cell by cell.
func (s *Server) abTestFactorial(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	t, ok := s.oneABTest(w, r)
	if !ok {
		return
	}
	switch {
	case len(t.Factors) == 0:
		writeError(w, r, http.StatusConflict, "conflict", "this test has no factors: add flags to it (factors) while it is a draft to test them together")
		return
	case t.Status == "draft":
		writeError(w, r, http.StatusConflict, "conflict", "this test hasn't started: start it to measure")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), abResultsTimeout)
	defer cancel()
	f, err := s.abFlag(ctx, nil, a.WorkspaceID, t.FlagID)
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
	factors := make([]abtests.Factor, len(t.Factors))
	for i, x := range t.Factors {
		factors[i] = abtests.Factor{FlagKey: x.FlagKey, Control: x.Control}
	}
	res, err := abtests.Factorial(ctx, s.pool, a.WorkspaceID, spec, factors)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"test_id": t.ID, "status": t.Status, "result": res})
}
