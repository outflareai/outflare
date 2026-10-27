package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/journeys"
)

// Journeys: if-this-then-that flows for people on customer records. A
// customer event (or a manual add) starts a run that sends lifecycle
// emails, waits, branches and exits. internal/journeys moves the runs;
// these handlers make and change journeys and show their runs.

func (s *Server) routesJourneys(mux *http.ServeMux) {
	read, send, manage := auth.ScopeEmailsRead, auth.ScopeEmailsSend, auth.ScopeEmailsManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/journeys", s.inWorkspace(read, s.listJourneys))
	mux.HandleFunc("POST /v1/workspaces/{ws}/journeys", s.inWorkspace(manage, s.createJourney))
	mux.HandleFunc("GET /v1/workspaces/{ws}/journeys/{id}", s.inWorkspace(read, s.getJourney))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/journeys/{id}", s.inWorkspace(manage, s.updateJourney))
	mux.HandleFunc("POST /v1/workspaces/{ws}/journeys/{id}/activate", s.inWorkspace(manage, s.activateJourney))
	mux.HandleFunc("POST /v1/workspaces/{ws}/journeys/{id}/pause", s.inWorkspace(manage, s.pauseJourney))
	mux.HandleFunc("POST /v1/workspaces/{ws}/journeys/{id}/archive", s.inWorkspace(manage, s.archiveJourney))
	mux.HandleFunc("GET /v1/workspaces/{ws}/journeys/{id}/runs", s.inWorkspace(read, s.listJourneyRuns))
	mux.HandleFunc("POST /v1/workspaces/{ws}/journeys/{id}/runs", s.inWorkspace(send, s.addJourneyRun))
	mux.HandleFunc("GET /v1/workspaces/{ws}/journeys/{id}/runs/{run}", s.inWorkspace(read, s.getJourneyRun))
	mux.HandleFunc("POST /v1/workspaces/{ws}/journeys/{id}/runs/{run}/exit", s.inWorkspace(manage, s.exitJourneyRun))
}

type journeyTrigger struct {
	Kind   string               `json:"kind"`
	Event  *string              `json:"event"`
	Filter []journeys.Condition `json:"filter"`
}

type journeyStats struct {
	Entered   int `json:"entered"`
	Active    int `json:"active"`
	Completed int `json:"completed"`
	Exited    int `json:"exited"`
	Failed    int `json:"failed"`
}

type journeyView struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Status      string          `json:"status"`
	BrandID     *string         `json:"brand_id"`
	Trigger     journeyTrigger  `json:"trigger"`
	Entry       string          `json:"entry"`
	GoalEvent   *string         `json:"goal_event"`
	Steps       []journeys.Step `json:"steps"`
	Version     int             `json:"version"`
	ActivatedAt *time.Time      `json:"activated_at"`
	Stats       journeyStats    `json:"stats"`
	// What each step did, by step id and outcome (sent, skipped, yes,
	// no, waited): only on a single journey.
	StepStats map[string]map[string]int `json:"step_stats,omitempty"`
	CreatedAt time.Time                 `json:"created_at"`
	UpdatedAt time.Time                 `json:"updated_at"`
}

const journeySelect = `
	SELECT j.id, j.name, j.description, j.status, j.brand_id, j.trigger_kind, j.trigger_event, j.trigger_filter, j.entry, j.goal_event,
	       j.steps, j.version, j.activated_at,
	       COALESCE(st.entered, 0), COALESCE(st.active, 0), COALESCE(st.completed, 0), COALESCE(st.exited, 0), COALESCE(st.failed, 0),
	       j.created_at, j.updated_at
	FROM journeys j
	LEFT JOIN LATERAL (
		SELECT count(*)::int AS entered,
		       count(*) FILTER (WHERE r.status = 'active')::int AS active,
		       count(*) FILTER (WHERE r.status = 'completed')::int AS completed,
		       count(*) FILTER (WHERE r.status = 'exited')::int AS exited,
		       count(*) FILTER (WHERE r.status = 'failed')::int AS failed
		FROM journey_runs r WHERE r.journey_id = j.id) st ON true
	WHERE j.workspace_id = $1`

func scanJourney(row pgx.Row) (journeyView, error) {
	var v journeyView
	var filter, steps []byte
	err := row.Scan(&v.ID, &v.Name, &v.Description, &v.Status, &v.BrandID, &v.Trigger.Kind, &v.Trigger.Event, &filter, &v.Entry, &v.GoalEvent,
		&steps, &v.Version, &v.ActivatedAt, &v.Stats.Entered, &v.Stats.Active, &v.Stats.Completed, &v.Stats.Exited, &v.Stats.Failed,
		&v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return v, err
	}
	v.Trigger.Filter, v.Steps = []journeys.Condition{}, []journeys.Step{}
	_ = json.Unmarshal(filter, &v.Trigger.Filter)
	_ = json.Unmarshal(steps, &v.Steps)
	return v, nil
}

var journeyStatuses = []string{"draft", "active", "paused", "archived"}

func (s *Server) listJourneys(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	status := r.URL.Query().Get("status")
	if status != "" && !slices.Contains(journeyStatuses, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is one of "+strings.Join(journeyStatuses, ", "))
		return
	}
	// Archived journeys only when asked for.
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID, status}
	rows, err := s.pool.Query(r.Context(), journeySelect+` AND (($2 = '' AND j.status <> 'archived') OR j.status = $2)`+sel.sql("j.brand_id", &args)+`
		ORDER BY j.created_at DESC LIMIT 200`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (journeyView, error) { return scanJourney(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []journeyView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) findJourney(w http.ResponseWriter, r *http.Request, ws, id string) (*journeyView, bool) {
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "journey not found")
		return nil, false
	}
	v, err := scanJourney(s.pool.QueryRow(r.Context(), journeySelect+` AND j.id = $2`, ws, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "journey not found")
		return nil, false
	}
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	return &v, true
}

func (s *Server) getJourney(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.findJourney(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT step, outcome, count(*)::int FROM journey_run_steps
		WHERE workspace_id = $1 AND journey_id = $2 GROUP BY step, outcome`, a.WorkspaceID, v.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	v.StepStats = map[string]map[string]int{}
	for rows.Next() {
		var step, outcome string
		var n int
		if err := rows.Scan(&step, &outcome, &n); err != nil {
			rows.Close()
			s.internal(w, r, err)
			return
		}
		if v.StepStats[step] == nil {
			v.StepStats[step] = map[string]int{}
		}
		v.StepStats[step][outcome] = n
	}
	if err := rows.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type journeyFields struct {
	Name        *string          `json:"name"`
	Description *string          `json:"description"`
	BrandID     optional[string] `json:"brand_id"`
	Trigger     *journeyTrigger  `json:"trigger"`
	Entry       *string          `json:"entry"`
	GoalEvent   optional[string] `json:"goal_event"`
	Steps       *[]journeys.Step `json:"steps"`
}

// onlyLabels reports whether f changes nothing but the name and
// description, which can change while a journey runs.
func (f journeyFields) onlyLabels() bool {
	return !f.BrandID.Set && f.Trigger == nil && f.Entry == nil && !f.GoalEvent.Set && f.Steps == nil
}

// applyJourney checks f and applies it to v.
func (s *Server) applyJourney(r *http.Request, ws string, f journeyFields, v *journeyView) error {
	if f.Name != nil {
		if n := strings.TrimSpace(*f.Name); n == "" || len(n) > 200 {
			return errInvalid("name must be 1 to 200 characters")
		}
		v.Name = strings.TrimSpace(*f.Name)
	}
	if f.Description != nil {
		if len(*f.Description) > 2000 {
			return errInvalid("description must be at most 2,000 characters")
		}
		v.Description = *f.Description
	}
	if f.BrandID.Set {
		if f.BrandID.Value != nil && !validUUID(*f.BrandID.Value) {
			return errInvalid("brand_id must be a brand id")
		}
		v.BrandID = f.BrandID.Value
	}
	if f.Trigger != nil {
		t := *f.Trigger
		switch t.Kind {
		case "event":
			if t.Event == nil || !journeys.ValidEventName(*t.Event) {
				return errInvalid("an event trigger needs event, an event name such as order_completed")
			}
		case "manual":
			if t.Event != nil || len(t.Filter) > 0 {
				return errInvalid("a manual trigger has no event or filter: people are added through the API or the app")
			}
		default:
			return errInvalid("trigger kind is event or manual")
		}
		if t.Filter == nil {
			t.Filter = []journeys.Condition{}
		}
		if msg := journeys.CheckFilter(t.Filter); msg != "" {
			return errInvalid("trigger: " + msg)
		}
		v.Trigger = t
	}
	if f.Entry != nil {
		if *f.Entry != "once" && *f.Entry != "again" {
			return errInvalid("entry is once or again")
		}
		v.Entry = *f.Entry
	}
	if f.GoalEvent.Set {
		if f.GoalEvent.Value != nil && !journeys.ValidEventName(*f.GoalEvent.Value) {
			return errInvalid("goal_event must be an event name such as order_completed")
		}
		v.GoalEvent = f.GoalEvent.Value
	}
	if f.Steps != nil {
		if msg := journeys.Check(*f.Steps); msg != "" {
			return errInvalid(msg)
		}
		if err := s.checkJourneyTemplates(r, ws, *f.Steps, false); err != nil {
			return err
		}
		v.Steps = *f.Steps
	}
	return nil
}

// checkJourneyTemplates checks that every email step names a lifecycle
// template that is not archived, and, to go live, that each is published.
func (s *Server) checkJourneyTemplates(r *http.Request, ws string, steps []journeys.Step, live bool) error {
	for _, ref := range journeys.Templates(steps) {
		var purpose, status, handle string
		var published *int
		err := s.pool.QueryRow(r.Context(), `SELECT purpose, status, handle, published_version FROM email_templates
			WHERE workspace_id = $1 AND (id::text = $2 OR handle = $2)`, ws, ref).Scan(&purpose, &status, &handle, &published)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return errInvalid("template not found: " + truncate(ref, 63))
		case err != nil:
			return err
		case purpose != "lifecycle":
			return errInvalid("a journey sends lifecycle templates; " + handle + " is transactional")
		case status != "active":
			return errInvalid("the template " + handle + " is archived; restore it first")
		case live && published == nil:
			return errInvalid("the template " + handle + " has no published version yet; publish it first")
		}
	}
	return nil
}

func (s *Server) createJourney(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var f journeyFields
	if !decode(w, r, &f) {
		return
	}
	if f.Name == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	v := journeyView{Trigger: journeyTrigger{Kind: "manual", Filter: []journeys.Condition{}}, Entry: "once", Steps: []journeys.Step{}}
	if !s.journeyApplied(w, r, s.applyJourney(r, a.WorkspaceID, f, &v)) {
		return
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		filter, _ := json.Marshal(v.Trigger.Filter)
		steps, _ := json.Marshal(v.Steps)
		var createdBy *string
		if a.ActorID != "" {
			createdBy = &a.ActorID
		}
		if err := tx.QueryRow(ctx, `INSERT INTO journeys (workspace_id, brand_id, name, description, trigger_kind, trigger_event, trigger_filter,
			entry, goal_event, steps, created_by) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id`,
			a.WorkspaceID, v.BrandID, v.Name, v.Description, v.Trigger.Kind, v.Trigger.Event, filter, v.Entry, v.GoalEvent, steps, createdBy).Scan(&id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "journey.created", "journey", id,
			map[string]any{"trigger": v.Trigger.Kind, "steps": len(v.Steps)})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findJourney(w, r, a.WorkspaceID, id); ok {
		writeJSON(w, http.StatusCreated, v)
	}
}

func (s *Server) journeyApplied(w http.ResponseWriter, r *http.Request, err error) bool {
	var inv errInvalid
	if errors.As(err, &inv) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
		return false
	}
	if err != nil {
		s.internal(w, r, err)
		return false
	}
	return true
}

// updateJourney changes a draft or paused journey. While it is active only
// its name and description change: pause it to change its steps, so no
// run is midway through steps that are being rewritten.
func (s *Server) updateJourney(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findJourney(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var f journeyFields
	if !decode(w, r, &f) {
		return
	}
	switch {
	case cur.Status == "archived":
		writeError(w, r, http.StatusConflict, "conflict", "the journey is archived")
		return
	case cur.Status == "active" && !f.onlyLabels():
		writeError(w, r, http.StatusConflict, "conflict", "the journey is active; pause it to change more than its name and description")
		return
	}
	v := *cur
	if !s.journeyApplied(w, r, s.applyJourney(r, a.WorkspaceID, f, &v)) {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		filter, _ := json.Marshal(v.Trigger.Filter)
		steps, _ := json.Marshal(v.Steps)
		bump := 0
		if f.Steps != nil {
			bump = 1
		}
		if _, err := tx.Exec(ctx, `UPDATE journeys SET brand_id = $3, name = $4, description = $5, trigger_kind = $6, trigger_event = $7,
			trigger_filter = $8, entry = $9, goal_event = $10, steps = $11, version = version + $12, updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, cur.ID, v.BrandID, v.Name, v.Description, v.Trigger.Kind, v.Trigger.Event,
			filter, v.Entry, v.GoalEvent, steps, bump); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "journey.updated", "journey", cur.ID, map[string]any{"steps_changed": f.Steps != nil})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findJourney(w, r, a.WorkspaceID, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// activateJourney starts a draft or paused journey, after checking that it
// can work: it has steps, an event trigger has its event, and every email
// step's template is published. A paused journey's runs move on.
func (s *Server) activateJourney(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findJourney(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	if cur.Status != "draft" && cur.Status != "paused" {
		writeError(w, r, http.StatusConflict, "conflict", "the journey is "+cur.Status+"; only a draft or paused journey can be activated")
		return
	}
	if len(cur.Steps) == 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "add a step before activating the journey")
		return
	}
	if !s.journeyApplied(w, r, s.checkJourneyTemplates(r, a.WorkspaceID, cur.Steps, true)) {
		return
	}
	s.setJourneyStatus(w, r, a, cur, "active", []string{"draft", "paused"})
}

func (s *Server) pauseJourney(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findJourney(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	if cur.Status != "active" {
		writeError(w, r, http.StatusConflict, "conflict", "the journey is "+cur.Status+"; only an active journey can be paused")
		return
	}
	s.setJourneyStatus(w, r, a, cur, "paused", []string{"active"})
}

// archiveJourney stops a journey for good and ends its active runs.
func (s *Server) archiveJourney(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findJourney(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	if cur.Status == "archived" {
		writeError(w, r, http.StatusConflict, "conflict", "the journey is already archived")
		return
	}
	s.setJourneyStatus(w, r, a, cur, "archived", []string{"draft", "active", "paused"})
}

func (s *Server) setJourneyStatus(w http.ResponseWriter, r *http.Request, a *access, cur *journeyView, to string, from []string) {
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE journeys SET status = $3, activated_at = CASE WHEN $3 = 'active' THEN now() ELSE activated_at END,
			updated_at = now() WHERE workspace_id = $1 AND id = $2 AND status = ANY($4)`, a.WorkspaceID, cur.ID, to, from)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errConflict
		}
		data := map[string]any{"from": cur.Status}
		switch to {
		case "active":
			if cur.Status == "paused" {
				err = journeys.Resume(ctx, tx, a.WorkspaceID, cur.ID)
			}
		case "archived":
			var n int
			n, err = journeys.ExitRuns(ctx, tx, a.WorkspaceID, cur.ID, "", "archived")
			data["runs_exited"] = n
		}
		if err != nil {
			return err
		}
		action := map[string]string{"active": "journey.activated", "paused": "journey.paused", "archived": "journey.archived"}[to]
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "journey", cur.ID, data)
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

type journeyRunStepView struct {
	Step    string    `json:"step"`
	Kind    string    `json:"kind"`
	Outcome string    `json:"outcome"`
	EmailID *string   `json:"email_id"`
	Detail  *string   `json:"detail"`
	At      time.Time `json:"at"`
}

type journeyRunView struct {
	ID         string         `json:"id"`
	JourneyID  string         `json:"journey_id"`
	RecordID   string         `json:"record_id"`
	Name       string         `json:"name"`
	Status     string         `json:"status"`
	Step       *string        `json:"step"`
	NextAt     *time.Time     `json:"next_at"`
	ExitReason *string        `json:"exit_reason"`
	StepsTaken int            `json:"steps_taken"`
	Context    map[string]any `json:"context"`
	EnteredAt  time.Time      `json:"entered_at"`
	FinishedAt *time.Time     `json:"finished_at"`
	// What each step did, in order: only on a single run.
	History []journeyRunStepView `json:"history,omitempty"`
}

const journeyRunSelect = `
	SELECT r.id, r.journey_id, r.record_id, COALESCE(rc.name, ''), r.status, r.step, r.next_at, r.exit_reason, r.steps_taken, r.context,
	       r.entered_at, r.finished_at
	FROM journey_runs r LEFT JOIN records rc ON rc.id = r.record_id
	WHERE r.workspace_id = $1 AND r.journey_id = $2`

func scanJourneyRun(row pgx.Row) (journeyRunView, error) {
	var v journeyRunView
	var ctxJSON []byte
	err := row.Scan(&v.ID, &v.JourneyID, &v.RecordID, &v.Name, &v.Status, &v.Step, &v.NextAt, &v.ExitReason, &v.StepsTaken, &ctxJSON,
		&v.EnteredAt, &v.FinishedAt)
	v.Context = map[string]any{}
	_ = json.Unmarshal(ctxJSON, &v.Context)
	return v, err
}

var journeyRunStatuses = []string{"active", "completed", "exited", "failed"}

// listJourneyRuns is the people in a journey, newest first.
func (s *Server) listJourneyRuns(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findJourney(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	q := r.URL.Query()
	status, after, record := q.Get("status"), q.Get("after"), q.Get("record")
	switch {
	case status != "" && !slices.Contains(journeyRunStatuses, status):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is one of "+strings.Join(journeyRunStatuses, ", "))
		return
	case after != "" && !validUUID(after):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "after must be a run id")
		return
	case record != "" && !validUUID(record):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "record must be a record id")
		return
	}
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit must be 1 to 200")
			return
		}
		limit = n
	}
	rows, err := s.pool.Query(r.Context(), journeyRunSelect+` AND ($3 = '' OR r.status = $3) AND ($4 = '' OR r.record_id::text = $4)
		AND ($5 = '' OR (r.entered_at, r.id) < (SELECT entered_at, id FROM journey_runs WHERE id = $5::uuid))
		ORDER BY r.entered_at DESC, r.id DESC LIMIT $6`, a.WorkspaceID, cur.ID, status, record, after, limit+1)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (journeyRunView, error) { return scanJourneyRun(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *string
	if len(list) > limit {
		list = list[:limit]
		next = &list[limit-1].ID
	}
	if list == nil {
		list = []journeyRunView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_after": next})
}

func (s *Server) findJourneyRun(w http.ResponseWriter, r *http.Request, ws, journeyID, id string) (*journeyRunView, bool) {
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "run not found")
		return nil, false
	}
	v, err := scanJourneyRun(s.pool.QueryRow(r.Context(), journeyRunSelect+` AND r.id = $3`, ws, journeyID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "run not found")
		return nil, false
	}
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	rows, err := s.pool.Query(r.Context(), `SELECT step, kind, outcome, email_id, detail, at FROM journey_run_steps
		WHERE workspace_id = $1 AND run_id = $2 ORDER BY id`, ws, id)
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	v.History, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (journeyRunStepView, error) {
		var h journeyRunStepView
		err := row.Scan(&h.Step, &h.Kind, &h.Outcome, &h.EmailID, &h.Detail, &h.At)
		return h, err
	})
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	if v.History == nil {
		v.History = []journeyRunStepView{}
	}
	return &v, true
}

func (s *Server) getJourneyRun(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findJourney(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	if v, ok := s.findJourneyRun(w, r, a.WorkspaceID, cur.ID, r.PathValue("run")); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// addJourneyRun adds a person to an active journey by hand, with data
// its emails can use as event.*. Trigger conditions don't apply; its
// entry rule does.
func (s *Server) addJourneyRun(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findJourney(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var req struct {
		RecordID string         `json:"record_id"`
		Data     map[string]any `json:"data"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !validUUID(req.RecordID) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "record_id must be a record id")
		return
	}
	if raw, _ := json.Marshal(req.Data); len(raw) > 32<<10 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "data must be at most 32 KB of JSON")
		return
	}
	if cur.Status != "active" {
		writeError(w, r, http.StatusConflict, "conflict", "the journey is "+cur.Status+"; people can only be added to an active journey")
		return
	}
	ctx := r.Context()
	var id, why string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		id, why, err = journeys.Enroll(ctx, tx, a.WorkspaceID, cur.ID, req.RecordID, nil, req.Data, true)
		if err != nil || id == "" {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "journey_run.entered", "journey", cur.ID, map[string]any{"via": "manual"})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if id == "" {
		writeError(w, r, http.StatusConflict, "conflict", "not added: "+why)
		return
	}
	if v, ok := s.findJourneyRun(w, r, a.WorkspaceID, cur.ID, id); ok {
		writeJSON(w, http.StatusCreated, v)
	}
}

// exitJourneyRun takes a person out of a journey before it ends.
func (s *Server) exitJourneyRun(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findJourney(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	run, ok := s.findJourneyRun(w, r, a.WorkspaceID, cur.ID, r.PathValue("run"))
	if !ok {
		return
	}
	if run.Status != "active" {
		writeError(w, r, http.StatusConflict, "conflict", "the run is already "+run.Status)
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE journey_runs SET status = 'exited', exit_reason = 'manual', finished_at = now(), next_at = NULL,
			updated_at = now() WHERE workspace_id = $1 AND id = $2 AND status = 'active'`, a.WorkspaceID, run.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errConflict
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "journey_run.exited", "journey", cur.ID, map[string]any{"reason": "manual"})
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, http.StatusConflict, "conflict", "the run has already ended")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findJourneyRun(w, r, a.WorkspaceID, cur.ID, run.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}
