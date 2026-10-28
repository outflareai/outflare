package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/abtests"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/flags"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// A/B tests: a question asked of a feature flag (flags.go). The flag
// splits visitors; the test names the control and the goals, and its
// results come from product events (engine/internal/abtests counts them).
// A test is a draft until it starts, running until it ends, then done with
// its results kept.

const (
	maxABTests       = 500
	maxABHypothesis  = 2000
	abResultsTimeout = 30 * time.Second
)

var abStatuses = []string{"draft", "running", "done"}

func (s *Server) routesABTests(mux *http.ServeMux) {
	read, manage := auth.ScopeAnalyticsRead, auth.ScopeAnalyticsManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/ab-tests", s.inWorkspace(read, s.listABTests))
	mux.HandleFunc("POST /v1/workspaces/{ws}/ab-tests", s.inWorkspace(manage, s.createABTest))
	mux.HandleFunc("POST /v1/workspaces/{ws}/ab-tests/plan", s.inWorkspace(read, s.planABTest))
	mux.HandleFunc("GET /v1/workspaces/{ws}/ab-tests/{test}", s.inWorkspace(read, s.getABTest))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/ab-tests/{test}", s.inWorkspace(manage, s.updateABTest))
	mux.HandleFunc("POST /v1/workspaces/{ws}/ab-tests/{test}/start", s.inWorkspace(manage, s.startABTest))
	mux.HandleFunc("POST /v1/workspaces/{ws}/ab-tests/{test}/end", s.inWorkspace(manage, s.endABTest))
	mux.HandleFunc("GET /v1/workspaces/{ws}/ab-tests/{test}/results", s.inWorkspace(read, s.abTestResults))
}

type abTestView struct {
	ID         string          `json:"id"`
	FlagID     string          `json:"flag_id"`
	FlagKey    string          `json:"flag_key"`
	BrandID    *string         `json:"brand_id"`
	Name       string          `json:"name"`
	Hypothesis string          `json:"hypothesis"`
	Status     string          `json:"status"`
	Control    string          `json:"control"`
	Goals      []abtests.Goal  `json:"goals"`
	MinEffect  float64         `json:"min_effect"`
	Baselines  []float64       `json:"baselines"`
	StartedAt  *time.Time      `json:"started_at"`
	EndedAt    *time.Time      `json:"ended_at"`
	Decision   string          `json:"decision"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
	result     json.RawMessage // the snapshot kept when it ended
}

type abTestInput struct {
	Flag       *string         `json:"flag"`
	Name       *string         `json:"name"`
	Hypothesis *string         `json:"hypothesis"`
	Control    *string         `json:"control"`
	Goals      *[]abtests.Goal `json:"goals"`
	MinEffect  *float64        `json:"min_effect"`
}

const abTestCols = `t.id::text, t.flag_id::text, f.key, t.brand_id::text, t.name, t.hypothesis, t.status, t.control, t.goals, t.min_effect, t.baselines,
	t.started_at, t.ended_at, t.decision, t.created_at, t.updated_at, t.result`

func loadABTests(ctx context.Context, q abtests.Querier, ws, where string, args ...any) ([]abTestView, error) {
	rows, err := q.Query(ctx, `SELECT `+abTestCols+` FROM ab_tests t JOIN flags f ON f.id = t.flag_id AND f.workspace_id = t.workspace_id
		WHERE t.workspace_id = $1`+where+` ORDER BY t.created_at DESC, t.id`, append([]any{ws}, args...)...)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (abTestView, error) {
		var v abTestView
		var goals []byte
		if err := row.Scan(&v.ID, &v.FlagID, &v.FlagKey, &v.BrandID, &v.Name, &v.Hypothesis, &v.Status, &v.Control, &goals, &v.MinEffect, &v.Baselines,
			&v.StartedAt, &v.EndedAt, &v.Decision, &v.CreatedAt, &v.UpdatedAt, &v.result); err != nil {
			return v, err
		}
		return v, json.Unmarshal(goals, &v.Goals)
	})
	if list == nil {
		list = []abTestView{}
	}
	return list, err
}

func (s *Server) listABTests(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	where, args := "", []any{a.WorkspaceID}
	if st := q.Get("status"); st != "" {
		if !slices.Contains(abStatuses, st) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "status is draft, running or done; without it, every test")
			return
		}
		args = append(args, st)
		where += ` AND t.status = $2`
	}
	if ref := q.Get("flag"); ref != "" {
		args = append(args, ref)
		n := fmt.Sprintf("$%d", len(args))
		if validUUID(ref) {
			where += ` AND t.flag_id = ` + n + `::uuid`
		} else {
			where += ` AND f.key = ` + n
		}
	}
	// A brand's tests include those of shared flags, which reach its site
	// too.
	if sel.id != "" && sel.id != "none" {
		args = append(args, sel.id)
		where += fmt.Sprintf(` AND (t.brand_id IS NULL OR t.brand_id = $%d)`, len(args))
		sel.id = ""
	}
	where += sel.sql("t.brand_id", &args)
	list, err := loadABTests(r.Context(), s.pool, a.WorkspaceID, where, args[1:]...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := list[:0]
	for _, t := range list {
		if a.sees(nil, t.BrandID) {
			out = append(out, t)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// oneABTest loads the test in the path, or answers 404.
func (s *Server) oneABTest(w http.ResponseWriter, r *http.Request) (abTestView, bool) {
	a := accessFrom(r.Context())
	ref := r.PathValue("test")
	if validUUID(ref) {
		list, err := loadABTests(r.Context(), s.pool, a.WorkspaceID, ` AND t.id = $2`, ref)
		if err != nil {
			s.internal(w, r, err)
			return abTestView{}, false
		}
		if len(list) == 1 && a.sees(nil, list[0].BrandID) {
			return list[0], true
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "A/B test not found")
	return abTestView{}, false
}

func (s *Server) getABTest(w http.ResponseWriter, r *http.Request) {
	if t, ok := s.oneABTest(w, r); ok {
		writeJSON(w, http.StatusOK, t)
	}
}

// errABConflict is a change the test's state doesn't allow (409).
type errABConflict string

func (e errABConflict) Error() string { return string(e) }

func (s *Server) abWriteError(w http.ResponseWriter, r *http.Request, err error) bool {
	var c errABConflict
	switch {
	case errors.As(err, &c):
		writeError(w, r, http.StatusConflict, "conflict", string(c))
		return true
	case isUnique(err):
		writeError(w, r, http.StatusConflict, "conflict", "another test of this flag is running: end it first, since a flag answers one test at a time")
		return true
	}
	return s.allocError(w, r, err)
}

// abFlag loads the flag a test asks about, by id or key, in tx.
func (s *Server) abFlag(ctx context.Context, tx pgx.Tx, ws, ref string) (flagView, error) {
	var list []flagView
	var err error
	switch {
	case validUUID(ref):
		list, err = s.loadFlags(ctx, tx, ws, ` AND id = $2`, ref)
	case flags.ValidKey(ref):
		list, err = s.loadFlags(ctx, tx, ws, ` AND key = $2 AND status <> 'archived'`, ref)
	}
	if err != nil {
		return flagView{}, err
	}
	if len(list) != 1 {
		return flagView{}, errInvalid("flag is required: the id or key of a live feature flag")
	}
	return list[0], nil
}

func variantKeys(f flagView) []string {
	out := make([]string, len(f.Variants))
	for i, v := range f.Variants {
		out[i] = v.Key
	}
	return out
}

// applyABInput checks in and sets it on t. Control and goals are fixed
// once a test starts, so its results can't be reshaped after the fact.
func applyABInput(t *abTestView, f flagView, in abTestInput) error {
	if in.Name != nil {
		t.Name = strings.TrimSpace(*in.Name)
	}
	if t.Name == "" {
		t.Name = f.Name
	}
	if len(t.Name) > 200 {
		return errInvalid("name is at most 200 characters")
	}
	if in.Hypothesis != nil {
		t.Hypothesis = strings.TrimSpace(*in.Hypothesis)
	}
	if len(t.Hypothesis) > maxABHypothesis {
		return errInvalid("hypothesis is at most 2,000 characters")
	}
	if (in.Control != nil || in.Goals != nil || in.MinEffect != nil) && t.Status != "draft" {
		return errABConflict("the control, goals and smallest change are fixed once a test starts, so its results can't be reshaped; start a new test to measure something else")
	}
	if in.MinEffect != nil {
		t.MinEffect = *in.MinEffect
	}
	if t.MinEffect == 0 {
		t.MinEffect = abtests.DefaultMinEffect
	}
	if t.MinEffect < abtests.MinEffectLow || t.MinEffect > abtests.MinEffectHigh {
		return errInvalid("min_effect is the smallest relative change worth detecting, from 0.01 (+1%) to 1 (+100%), such as 0.25")
	}
	if in.Control != nil {
		t.Control = *in.Control
	}
	if t.Control == "" {
		t.Control = f.Variants[0].Key
		if abtests.HasVariant(variantKeys(f), "control") {
			t.Control = "control"
		}
	}
	if t.Status == "draft" && !abtests.HasVariant(variantKeys(f), t.Control) {
		return errInvalid("control is one of the flag's variants: " + strings.Join(variantKeys(f), ", "))
	}
	if in.Goals != nil {
		t.Goals = *in.Goals
	}
	if msg := abtests.CheckGoals(t.Goals); msg != "" {
		return errInvalid(msg)
	}
	return nil
}

// abAuditData is what the audit log and product events keep of a test
// (ids, enums and counts):
// never its name, hypothesis or goal names.
func abAuditData(t abTestView) map[string]any {
	kinds, metrics := make([]string, len(t.Goals)), make([]string, len(t.Goals))
	for i, g := range t.Goals {
		kinds[i], metrics[i] = g.Kind, g.MetricOf()
	}
	return map[string]any{"flag_id": t.FlagID, "status": t.Status, "goals": len(t.Goals), "goal_kinds": kinds, "goal_metrics": metrics,
		"min_effect": t.MinEffect, "brand_id": t.BrandID}
}

func (s *Server) createABTest(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in abTestInput
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if in.Flag == nil {
			return errInvalid("flag is required: the id or key of a live feature flag")
		}
		f, err := s.abFlag(ctx, tx, a.WorkspaceID, *in.Flag)
		if err != nil {
			return err
		}
		if f.Status == "archived" {
			return errInvalid("this flag is archived; test a live one")
		}
		if !a.writes(nil, f.BrandID) {
			return errDenied(brandLimitedMessage)
		}
		t := abTestView{FlagID: f.ID, BrandID: f.BrandID, Status: "draft"}
		if err := applyABInput(&t, f, in); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM ab_tests WHERE workspace_id = $1`, a.WorkspaceID).Scan(&n); err != nil {
			return err
		}
		if n >= maxABTests {
			return errInvalid("a workspace can have at most 500 A/B tests")
		}
		goals, _ := json.Marshal(t.Goals)
		if err := tx.QueryRow(ctx, `INSERT INTO ab_tests (workspace_id, brand_id, flag_id, name, hypothesis, control, goals, min_effect, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id::text`,
			a.WorkspaceID, t.BrandID, t.FlagID, t.Name, t.Hypothesis, t.Control, goals, t.MinEffect, nullIfEmpty(a.ActorID)).Scan(&id); err != nil {
			return err
		}
		data := abAuditData(t)
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "ab_test.created", "ab_test", id, data, data)
	})
	if s.abWriteError(w, r, err) {
		return
	}
	s.writeABTest(w, r, id, http.StatusCreated)
}

func (s *Server) writeABTest(w http.ResponseWriter, r *http.Request, id string, code int) {
	list, err := loadABTests(r.Context(), s.pool, accessFrom(r.Context()).WorkspaceID, ` AND t.id = $2`, id)
	if err != nil || len(list) != 1 {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, code, list[0])
}

// changeABTest loads the test in the path and its flag in one
// transaction, checks the caller may change it, and runs fn.
func (s *Server) changeABTest(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, tx pgx.Tx, t *abTestView, f flagView) error) {
	a := accessFrom(r.Context())
	cur, ok := s.oneABTest(w, r)
	if !ok {
		return
	}
	if !a.writes(nil, cur.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", brandLimitedMessage)
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Lock the test and read it again, so changes can't cross.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM ab_tests WHERE id = $1 AND workspace_id = $2 FOR UPDATE`, cur.ID, a.WorkspaceID); err != nil {
			return err
		}
		list, err := loadABTests(ctx, tx, a.WorkspaceID, ` AND t.id = $2`, cur.ID)
		if err != nil || len(list) != 1 {
			return errors.Join(err, errNotFound)
		}
		cur = list[0]
		f, err := s.abFlag(ctx, tx, a.WorkspaceID, cur.FlagID)
		if err != nil {
			return err
		}
		return fn(ctx, tx, &cur, f)
	})
	if s.abWriteError(w, r, err) {
		return
	}
	s.writeABTest(w, r, cur.ID, http.StatusOK)
}

func (s *Server) updateABTest(w http.ResponseWriter, r *http.Request) {
	var in abTestInput
	if !decode(w, r, &in) {
		return
	}
	a := accessFrom(r.Context())
	s.changeABTest(w, r, func(ctx context.Context, tx pgx.Tx, t *abTestView, f flagView) error {
		if in.Flag != nil && *in.Flag != t.FlagID && *in.Flag != t.FlagKey {
			return errInvalid("a test's flag can't change; add a new test for another flag")
		}
		if err := applyABInput(t, f, in); err != nil {
			return err
		}
		goals, _ := json.Marshal(t.Goals)
		if _, err := tx.Exec(ctx, `UPDATE ab_tests SET name = $3, hypothesis = $4, control = $5, goals = $6, min_effect = $7, updated_at = now() WHERE id = $1 AND workspace_id = $2`,
			t.ID, a.WorkspaceID, t.Name, t.Hypothesis, t.Control, goals, t.MinEffect); err != nil {
			return err
		}
		data := abAuditData(*t)
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "ab_test.updated", "ab_test", t.ID, data, data)
	})
}

// startABTest starts measuring: the flag must still split visitors between
// the control and at least one other version. A flag that is off is
// turned on, since a test of a flag nobody sees measures nothing.
func (s *Server) startABTest(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	turnedOn := false
	s.changeABTest(w, r, func(ctx context.Context, tx pgx.Tx, t *abTestView, f flagView) error {
		if t.Status != "draft" {
			return errABConflict("this test has already started; a test runs once")
		}
		switch {
		case f.Status == "archived":
			return errABConflict("this test's flag is archived; add a test of a live flag")
		case len(f.Variants) < 2:
			return errABConflict("the flag has one version: give it at least two (the control and one to try) before starting")
		case !abtests.HasVariant(variantKeys(f), t.Control):
			return errABConflict("the control is no longer one of the flag's variants (" + strings.Join(variantKeys(f), ", ") + "): change the control first")
		}
		if !a.writes(nil, f.BrandID) {
			return errDenied(brandLimitedMessage)
		}
		if f.Status == "off" {
			if _, err := tx.Exec(ctx, `UPDATE flags SET status = 'on', updated_at = now() WHERE id = $1 AND workspace_id = $2`, f.ID, a.WorkspaceID); err != nil {
				return err
			}
			data := flagAuditData(f)
			data["status"], data["was"] = "on", "off"
			if err := record(ctx, tx, &a.principal, a.WorkspaceID, "flag.updated", "flag", f.ID, data); err != nil {
				return err
			}
			turnedOn = true
		}
		// Record each goal's recent rate on the site, so the sequential
		// test keeps one tuning for the test's whole life.
		now := time.Now()
		baselines, recorded := make([]float64, len(t.Goals)), 0
		for i, g := range t.Goals {
			tr, err := abtests.ReadTraffic(ctx, tx, a.WorkspaceID, g, "", now.Add(-abtests.TrafficWindow), now)
			if err != nil {
				return err
			}
			if baselines[i] = abtests.Baseline(tr, g.MetricOf()); baselines[i] > 0 {
				recorded++
			}
		}
		var stored any
		if recorded > 0 {
			stored, _ = json.Marshal(baselines)
		}
		if _, err := tx.Exec(ctx, `UPDATE ab_tests SET status = 'running', started_at = now(), baselines = $3, updated_at = now() WHERE id = $1 AND workspace_id = $2`,
			t.ID, a.WorkspaceID, stored); err != nil {
			return err
		}
		t.Status = "running"
		data := abAuditData(*t)
		data["variants"], data["flag_turned_on"], data["baselines_recorded"] = len(f.Variants), turnedOn, recorded
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "ab_test.started", "ab_test", t.ID, data, data)
	})
	if turnedOn {
		s.flagCache.forget(a.WorkspaceID)
	}
}

// endABTest stops measuring and keeps the results as they stand, with the
// version the team chose. The flag is left as it is: ship the winner by
// changing the flag.
func (s *Server) endABTest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Decision *string `json:"decision"`
	}
	if !decode(w, r, &in) {
		return
	}
	a := accessFrom(r.Context())
	s.changeABTest(w, r, func(ctx context.Context, tx pgx.Tx, t *abTestView, f flagView) error {
		if t.Status != "running" {
			return errABConflict("only a running test can end")
		}
		if in.Decision == nil {
			return errInvalid(`decision is required: the variant you chose to keep, or "" for none`)
		}
		res, err := s.abResults(ctx, tx, a.WorkspaceID, *t, f, time.Now())
		if err != nil {
			return err
		}
		if d := *in.Decision; d != "" && !abtests.HasVariant(variantKeys(f), d) && !slices.ContainsFunc(res.Arms, func(x abtests.Arm) bool { return x.Variant == d }) {
			return errInvalid(`decision is one of the flag's variants (` + strings.Join(variantKeys(f), ", ") + `), or "" for none`)
		}
		snap, _ := json.Marshal(res)
		if _, err := tx.Exec(ctx, `UPDATE ab_tests SET status = 'done', ended_at = now(), decision = $3, result = $4, updated_at = now() WHERE id = $1 AND workspace_id = $2`,
			t.ID, a.WorkspaceID, *in.Decision, snap); err != nil {
			return err
		}
		t.Status = "done"
		data := abAuditData(*t)
		winner := abtests.Winner(res, t.Control)
		data["decided"], data["had_winner"], data["chose_winner"] = *in.Decision != "", winner != "", winner != "" && winner == *in.Decision
		data["chose_control"] = *in.Decision == t.Control
		data["sample_ratio_mismatch"] = res.SampleRatio
		var visitors int64
		for _, arm := range res.Arms {
			visitors += arm.Visitors
		}
		data["visitors"] = visitors
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "ab_test.ended", "ab_test", t.ID, data, data)
	})
}

// abResults counts a test's results up to `to`, timed for the dashboard.
func (s *Server) abResults(ctx context.Context, q abtests.Querier, ws string, t abTestView, f flagView, to time.Time) (abtests.Result, error) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, abResultsTimeout)
	defer cancel()
	spec := abtests.Spec{FlagKey: f.Key, Control: t.Control, Goals: t.Goals, MinEffect: t.MinEffect, Baselines: t.Baselines, To: to}
	if t.StartedAt != nil {
		spec.From = *t.StartedAt
	}
	for _, v := range f.Variants {
		spec.Variants = append(spec.Variants, v.Key)
		spec.Weights = append(spec.Weights, v.Weight)
	}
	res, err := abtests.Results(ctx, q, ws, spec)
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	metrics.ABResults(outcome, time.Since(start))
	return res, err
}

// abTestResults gives a running test's results now, or a finished test's
// as they stood when it ended, with the version they favour.
func (s *Server) abTestResults(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	t, ok := s.oneABTest(w, r)
	if !ok {
		return
	}
	var res abtests.Result
	switch t.Status {
	case "draft":
		writeError(w, r, http.StatusConflict, "conflict", "this test hasn't started: start it to measure")
		return
	case "done":
		if err := json.Unmarshal(t.result, &res); err != nil {
			s.internal(w, r, err)
			return
		}
	default:
		f, err := s.abFlag(r.Context(), nil, a.WorkspaceID, t.FlagID)
		if err == nil {
			res, err = s.abResults(r.Context(), s.pool, a.WorkspaceID, t, f, time.Now())
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"test_id": t.ID, "status": t.Status, "control": t.Control,
		"winner": abtests.Winner(res, t.Control), "result": res,
	})
}

// planABTest says how long a test of a flag is likely to take, from the
// last 28 days of the site's traffic and the goal's rate, before anyone
// starts it.
func (s *Server) planABTest(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Flag      string       `json:"flag"`
		Goal      abtests.Goal `json:"goal"`
		Page      string       `json:"page"`
		MinEffect float64      `json:"min_effect"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), abResultsTimeout)
	defer cancel()
	f, err := s.abFlag(ctx, nil, a.WorkspaceID, in.Flag)
	if s.abWriteError(w, r, err) {
		return
	}
	if !a.sees(nil, f.BrandID) {
		writeError(w, r, http.StatusNotFound, "not_found", "flag not found: use its id or key")
		return
	}
	in.Page = strings.TrimSpace(in.Page)
	switch {
	case abtests.CheckGoals([]abtests.Goal{in.Goal}) != "":
		writeError(w, r, http.StatusBadRequest, "invalid_request", abtests.CheckGoals([]abtests.Goal{in.Goal}))
		return
	case in.Page != "" && abtests.CheckGoals([]abtests.Goal{{Kind: "page", Path: in.Page}}) != "":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "page is a path such as /pricing, or /docs/* for every page under /docs")
		return
	case in.MinEffect != 0 && (in.MinEffect < abtests.MinEffectLow || in.MinEffect > abtests.MinEffectHigh):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "min_effect is from 0.01 (+1%) to 1 (+100%), such as 0.25")
		return
	}
	now := time.Now()
	tr, err := abtests.ReadTraffic(ctx, s.pool, a.WorkspaceID, in.Goal, in.Page, now.Add(-abtests.TrafficWindow), now)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	weights := make([]int, len(f.Variants))
	for i, v := range f.Variants {
		weights[i] = v.Weight
	}
	weekly := float64(tr.Visitors) / 4 * float64(f.Rollout) / 100
	m := in.Goal.MetricOf()
	plan := abtests.MakePlan(tr.Moments(m), m, weekly, weights, in.MinEffect)
	writeJSON(w, http.StatusOK, map[string]any{
		"flag_id": f.ID, "page": in.Page, "window_days": 28, "traffic": tr, "plan": plan, "warning": planWarning(f, tr, plan),
	})
}

// planWarning says in words when a plan can't work or will take too long.
func planWarning(f flagView, tr abtests.Traffic, p abtests.Plan) string {
	switch {
	case len(f.Variants) < 2:
		return "The flag has one version: give it at least two before planning a test."
	case tr.Visitors == 0:
		return "No page views in the last 28 days, so there's nothing to plan from. Check the website script is installed."
	case tr.Converted == 0:
		return "No visitor reached this goal in the last 28 days, so a test of it can't find a winner yet. Pick a goal people already reach."
	case p.Weeks == 0 || p.Weeks > 8:
		if p.Detectable8Weeks == 0 {
			return "At this traffic, even a very large change is unlikely to be safe to stop within 8 weeks. Test a busier page or an earlier goal."
		}
		return fmt.Sprintf("At this traffic, a change of +%.0f%% is unlikely to be safe to stop within 8 weeks. Only changes of about +%.0f%% or more will show in that time.",
			p.MinEffect*100, p.Detectable8Weeks*100)
	}
	return ""
}
