package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/journeys"
)

// Journey versions: a live journey is read-only. Editing it makes a draft
// version beside it; publishing swaps the draft's trigger, entry, goal and
// steps into the journey as its next version. Every version that went live
// is kept. People already in the journey finish on the version they
// entered on, unless the publish moves them onto the new one.

type journeyVersionView struct {
	ID          string          `json:"id"`
	Status      string          `json:"status"`
	Version     *int            `json:"version"`
	Trigger     journeyTrigger  `json:"trigger"`
	Entry       string          `json:"entry"`
	GoalEvent   *string         `json:"goal_event"`
	Steps       []journeys.Step `json:"steps"`
	RunsActive  int             `json:"runs_active"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	PublishedAt *time.Time      `json:"published_at"`
}

// The active runs on each version: a run with no version of its own goes
// through the journey's current one.
const journeyVersionSelect = `
	SELECT v.id, v.status, v.version, v.trigger_kind, v.trigger_event, v.trigger_filter, v.entry, v.goal_event, v.steps,
	       CASE WHEN v.status = 'draft' THEN 0 ELSE (SELECT count(*)::int FROM journey_runs r
	         WHERE r.journey_id = v.journey_id AND r.status = 'active' AND COALESCE(r.journey_version, j.version) = v.version) END,
	       v.created_at, v.updated_at, v.published_at
	FROM journey_versions v JOIN journeys j ON j.id = v.journey_id
	WHERE v.workspace_id = $1 AND v.journey_id = $2`

func scanJourneyVersion(row pgx.Row) (journeyVersionView, error) {
	var v journeyVersionView
	var filter, steps []byte
	err := row.Scan(&v.ID, &v.Status, &v.Version, &v.Trigger.Kind, &v.Trigger.Event, &filter, &v.Entry, &v.GoalEvent, &steps, &v.RunsActive,
		&v.CreatedAt, &v.UpdatedAt, &v.PublishedAt)
	if err != nil {
		return v, err
	}
	v.Trigger.Filter, v.Steps = []journeys.Condition{}, []journeys.Step{}
	_ = json.Unmarshal(filter, &v.Trigger.Filter)
	_ = json.Unmarshal(steps, &v.Steps)
	return v, nil
}

// keepJourneyVersion keeps the journey's current trigger, entry, goal and
// steps as a published version, if that version isn't kept yet.
func keepJourneyVersion(ctx context.Context, tx pgx.Tx, a *access, journeyID string) error {
	var by *string
	if a.ActorID != "" {
		by = &a.ActorID
	}
	_, err := tx.Exec(ctx, `INSERT INTO journey_versions (workspace_id, journey_id, status, version, trigger_kind, trigger_event, trigger_filter,
		entry, goal_event, steps, created_by, published_by, published_at)
		SELECT workspace_id, id, 'published', version, trigger_kind, trigger_event, trigger_filter, entry, goal_event, steps, $3, $3, now()
		FROM journeys WHERE workspace_id = $1 AND id = $2
		ON CONFLICT (journey_id, version) WHERE status = 'published' DO NOTHING`, a.WorkspaceID, journeyID, by)
	return err
}

func (s *Server) listJourneyVersions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findJourney(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), journeyVersionSelect+` ORDER BY v.status = 'draft' DESC, v.version DESC LIMIT 200`, a.WorkspaceID, cur.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (journeyVersionView, error) { return scanJourneyVersion(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []journeyVersionView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) findJourneyDraft(ctx context.Context, ws, journeyID string) (*journeyVersionView, error) {
	v, err := scanJourneyVersion(s.pool.QueryRow(ctx, journeyVersionSelect+` AND v.status = 'draft'`, ws, journeyID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// journeyDraftable finds a journey that can have a draft version: one
// that has gone live. A journey still in draft is changed directly.
func (s *Server) journeyDraftable(w http.ResponseWriter, r *http.Request) (*journeyView, bool) {
	a := accessFrom(r.Context())
	cur, ok := s.findJourney(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return nil, false
	}
	switch cur.Status {
	case "draft":
		writeError(w, r, http.StatusConflict, "conflict", "the journey hasn't gone live yet; change it directly with PATCH")
		return nil, false
	case "archived":
		writeError(w, r, http.StatusConflict, "conflict", "the journey is archived")
		return nil, false
	}
	return cur, true
}

// createJourneyDraft starts a draft version from what is live, or returns
// the draft already there.
func (s *Server) createJourneyDraft(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.journeyDraftable(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if d, err := s.findJourneyDraft(ctx, a.WorkspaceID, cur.ID); err != nil || d != nil {
		if err != nil {
			s.internal(w, r, err)
		} else {
			writeJSON(w, http.StatusOK, d)
		}
		return
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var by *string
		if a.ActorID != "" {
			by = &a.ActorID
		}
		tag, err := tx.Exec(ctx, `INSERT INTO journey_versions (workspace_id, journey_id, status, trigger_kind, trigger_event, trigger_filter,
			entry, goal_event, steps, created_by)
			SELECT workspace_id, id, 'draft', trigger_kind, trigger_event, trigger_filter, entry, goal_event, steps, $3
			FROM journeys WHERE workspace_id = $1 AND id = $2
			ON CONFLICT (journey_id) WHERE status = 'draft' DO NOTHING`, a.WorkspaceID, cur.ID, by)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "journey.draft_created", "journey", cur.ID, map[string]any{"from_version": cur.Version})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	d, err := s.findJourneyDraft(ctx, a.WorkspaceID, cur.ID)
	if err != nil || d == nil {
		s.internal(w, r, errors.Join(err, errors.New("draft not found after creating it")))
		return
	}
	writeJSON(w, http.StatusCreated, d)
}

func (s *Server) getJourneyDraft(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findJourney(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	d, err := s.findJourneyDraft(r.Context(), a.WorkspaceID, cur.ID)
	switch {
	case err != nil:
		s.internal(w, r, err)
	case d == nil:
		writeError(w, r, http.StatusNotFound, "not_found", "the journey has no draft version")
	default:
		writeJSON(w, http.StatusOK, d)
	}
}

// updateJourneyDraft changes the draft's trigger, entry, goal or steps.
// The name and description belong to the journey and change there.
func (s *Server) updateJourneyDraft(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.journeyDraftable(w, r)
	if !ok {
		return
	}
	var in struct {
		Trigger   *journeyTrigger  `json:"trigger"`
		Entry     *string          `json:"entry"`
		GoalEvent optional[string] `json:"goal_event"`
		Steps     *[]journeys.Step `json:"steps"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	d, err := s.findJourneyDraft(ctx, a.WorkspaceID, cur.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if d == nil {
		writeError(w, r, http.StatusNotFound, "not_found", "the journey has no draft version; start one with POST .../draft")
		return
	}
	v := journeyView{Trigger: d.Trigger, Entry: d.Entry, GoalEvent: d.GoalEvent, Steps: d.Steps}
	f := journeyFields{Trigger: in.Trigger, Entry: in.Entry, GoalEvent: in.GoalEvent, Steps: in.Steps}
	if !s.journeyApplied(w, r, s.applyJourney(r, a.WorkspaceID, f, &v)) {
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		filter, _ := json.Marshal(v.Trigger.Filter)
		steps, _ := json.Marshal(v.Steps)
		tag, err := tx.Exec(ctx, `UPDATE journey_versions SET trigger_kind = $3, trigger_event = $4, trigger_filter = $5, entry = $6, goal_event = $7,
			steps = $8, updated_at = now() WHERE workspace_id = $1 AND id = $2 AND status = 'draft'`,
			a.WorkspaceID, d.ID, v.Trigger.Kind, v.Trigger.Event, filter, v.Entry, v.GoalEvent, steps)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errConflict
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "journey.draft_updated", "journey", cur.ID, map[string]any{"steps": len(v.Steps)})
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, http.StatusConflict, "conflict", "the draft was published or discarded; reload the journey")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if d, err = s.findJourneyDraft(ctx, a.WorkspaceID, cur.ID); err != nil || d == nil {
		s.internal(w, r, errors.Join(err, errors.New("draft not found after saving it")))
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) discardJourneyDraft(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findJourney(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM journey_versions WHERE workspace_id = $1 AND journey_id = $2 AND status = 'draft'`, a.WorkspaceID, cur.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errConflict
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "journey.draft_discarded", "journey", cur.ID, nil)
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, http.StatusNotFound, "not_found", "the journey has no draft version")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// publishJourney swaps the draft in as the journey's next version, after
// the checks activating does, plus that every step can be reached. runs
// says what happens to the people in it now: keep (the default) lets them
// finish on the version they entered on; move puts them on the new one at
// the same step, and anyone on a step it removed exits.
func (s *Server) publishJourney(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.journeyDraftable(w, r)
	if !ok {
		return
	}
	var in struct {
		Runs string `json:"runs"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	switch in.Runs {
	case "":
		in.Runs = "keep"
	case "keep", "move":
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "runs is keep or move")
		return
	}
	ctx := r.Context()
	d, err := s.findJourneyDraft(ctx, a.WorkspaceID, cur.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if d == nil {
		writeError(w, r, http.StatusNotFound, "not_found", "the journey has no draft version to publish")
		return
	}
	if len(d.Steps) == 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "add a step before publishing")
		return
	}
	if lost := journeys.Unreachable(d.Steps); len(lost) > 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "nothing leads to "+strings.Join(lost, ", ")+"; link it from another step or remove it")
		return
	}
	if !s.journeyApplied(w, r, s.checkJourneyTemplates(r, a.WorkspaceID, d.Steps, true)) {
		return
	}
	data := map[string]any{"runs": in.Runs, "from_version": cur.Version, "version": cur.Version + 1}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// The version going out stays kept, for the people finishing it.
		if err := keepJourneyVersion(ctx, tx, a, cur.ID); err != nil {
			return err
		}
		filter, _ := json.Marshal(d.Trigger.Filter)
		steps, _ := json.Marshal(d.Steps)
		tag, err := tx.Exec(ctx, `UPDATE journeys SET trigger_kind = $4, trigger_event = $5, trigger_filter = $6, entry = $7, goal_event = $8,
			steps = $9, version = version + 1, updated_at = now()
			WHERE workspace_id = $1 AND id = $2 AND version = $3 AND status IN ('active', 'paused')`,
			a.WorkspaceID, cur.ID, cur.Version, d.Trigger.Kind, d.Trigger.Event, filter, d.Entry, d.GoalEvent, steps)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errConflict
		}
		var by *string
		if a.ActorID != "" {
			by = &a.ActorID
		}
		tag, err = tx.Exec(ctx, `UPDATE journey_versions SET status = 'published', version = $3, published_by = $4, published_at = now(), updated_at = now()
			WHERE workspace_id = $1 AND id = $2 AND status = 'draft'`, a.WorkspaceID, d.ID, cur.Version+1, by)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errConflict
		}
		if in.Runs == "move" {
			tag, err = tx.Exec(ctx, `UPDATE journey_runs SET journey_version = $3, updated_at = now()
				WHERE workspace_id = $1 AND journey_id = $2 AND status = 'active'`, a.WorkspaceID, cur.ID, cur.Version+1)
			if err != nil {
				return err
			}
			data["runs_moved"] = tag.RowsAffected()
		} else {
			// Runs from before versions follow the current steps: pin them
			// to the version they were on.
			if _, err := tx.Exec(ctx, `UPDATE journey_runs SET journey_version = $3
				WHERE workspace_id = $1 AND journey_id = $2 AND status = 'active' AND journey_version IS NULL`, a.WorkspaceID, cur.ID, cur.Version); err != nil {
				return err
			}
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "journey.published", "journey", cur.ID, data)
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, http.StatusConflict, "conflict", "the journey changed; reload it")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findJourney(w, r, a.WorkspaceID, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}
