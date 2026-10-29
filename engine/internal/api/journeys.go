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
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/journeys"
)

// Journeys: if-this-then-that flows for people on customer records. A
// customer event (or a manual add) starts a run that sends lifecycle
// emails, waits, branches and exits. internal/journeys moves the runs;
// these handlers make and change journeys and show their runs.

func (s *Server) routesJourneys(mux *http.ServeMux) {
	read, send, manage := auth.ScopeEmailsRead, auth.ScopeEmailsSend, auth.ScopeEmailsManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/journeys", s.inWorkspace(read, s.listJourneys))
	mux.HandleFunc("GET /v1/workspaces/{ws}/journeys/step-kinds", s.inWorkspace(read, s.listJourneyStepKinds))
	mux.HandleFunc("GET /v1/workspaces/{ws}/automation-map", s.inWorkspace(read, s.getAutomationMap))
	mux.HandleFunc("POST /v1/workspaces/{ws}/journeys", s.inWorkspace(manage, s.createJourney))
	mux.HandleFunc("GET /v1/workspaces/{ws}/journeys/{id}", s.inWorkspace(read, s.getJourney))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/journeys/{id}", s.inWorkspace(manage, s.updateJourney))
	mux.HandleFunc("POST /v1/workspaces/{ws}/journeys/{id}/activate", s.inWorkspace(manage, s.activateJourney))
	mux.HandleFunc("POST /v1/workspaces/{ws}/journeys/{id}/pause", s.inWorkspace(manage, s.pauseJourney))
	mux.HandleFunc("POST /v1/workspaces/{ws}/journeys/{id}/archive", s.inWorkspace(manage, s.archiveJourney))
	mux.HandleFunc("GET /v1/workspaces/{ws}/journeys/{id}/versions", s.inWorkspace(read, s.listJourneyVersions))
	mux.HandleFunc("POST /v1/workspaces/{ws}/journeys/{id}/draft", s.inWorkspace(manage, s.createJourneyDraft))
	mux.HandleFunc("GET /v1/workspaces/{ws}/journeys/{id}/draft", s.inWorkspace(read, s.getJourneyDraft))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/journeys/{id}/draft", s.inWorkspace(manage, s.updateJourneyDraft))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/journeys/{id}/draft", s.inWorkspace(manage, s.discardJourneyDraft))
	mux.HandleFunc("POST /v1/workspaces/{ws}/journeys/{id}/publish", s.inWorkspace(manage, s.publishJourney))
	mux.HandleFunc("POST /v1/workspaces/{ws}/journeys/{id}/test", s.inWorkspace(read, s.testJourney))
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
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Status      string         `json:"status"`
	BrandID     *string        `json:"brand_id"`
	Trigger     journeyTrigger `json:"trigger"`
	Entry       string         `json:"entry"`
	GoalEvent   *string        `json:"goal_event"`
	// Exit criteria: these events end a run at once; these conditions (any
	// of them) end it before its next step, and keep people who already
	// meet them from starting.
	ExitEvents     []string             `json:"exit_events"`
	ExitConditions []journeys.Condition `json:"exit_conditions"`
	// Who never enters: people on these lists or with these tags, or at a
	// company with one.
	Exclude     customers.Exclusion `json:"exclude"`
	Steps       []journeys.Step     `json:"steps"`
	Version     int                 `json:"version"`
	ActivatedAt *time.Time          `json:"activated_at"`
	Stats       journeyStats        `json:"stats"`
	// When its draft version last changed, or nil when it has none.
	DraftUpdatedAt *time.Time `json:"draft_updated_at"`
	// The flow kit that added it, if one did.
	Kit *kitRef `json:"kit"`
	// What each step did, by step id and outcome (sent, skipped, yes,
	// no, waited), and how many people are on each step now: only on a
	// single journey.
	StepStats map[string]map[string]int `json:"step_stats,omitempty"`
	AtStep    map[string]int            `json:"at_step,omitempty"`
	// What became of each email step's emails: delivered, opened and
	// clicked, by step id. Only on a single journey.
	EmailStats map[string]map[string]int `json:"email_stats,omitempty"`
	CreatedAt  time.Time                 `json:"created_at"`
	UpdatedAt  time.Time                 `json:"updated_at"`
}

const journeySelect = `
	SELECT j.id, j.name, j.description, j.status, j.brand_id, j.trigger_kind, j.trigger_event, j.trigger_filter, j.entry, j.goal_event,
	       j.exit_events, j.exit_conditions, j.exclude, j.steps, j.version, j.activated_at,
	       COALESCE(st.entered, 0), COALESCE(st.active, 0), COALESCE(st.completed, 0), COALESCE(st.exited, 0), COALESCE(st.failed, 0),
	       j.created_at, j.updated_at,
	       (SELECT d.updated_at FROM journey_versions d WHERE d.journey_id = j.id AND d.status = 'draft'), j.kit_id, j.kit_key
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
	var filter, steps, exitConds, exclude []byte
	var kitID, kitKey *string
	err := row.Scan(&v.ID, &v.Name, &v.Description, &v.Status, &v.BrandID, &v.Trigger.Kind, &v.Trigger.Event, &filter, &v.Entry, &v.GoalEvent,
		&v.ExitEvents, &exitConds, &exclude, &steps, &v.Version, &v.ActivatedAt, &v.Stats.Entered, &v.Stats.Active, &v.Stats.Completed, &v.Stats.Exited, &v.Stats.Failed,
		&v.CreatedAt, &v.UpdatedAt, &v.DraftUpdatedAt, &kitID, &kitKey)
	if err != nil {
		return v, err
	}
	v.Kit = newKitRef(kitID, kitKey)
	v.Trigger.Filter, v.Steps, v.ExitConditions = []journeys.Condition{}, []journeys.Step{}, []journeys.Condition{}
	_ = json.Unmarshal(exitConds, &v.ExitConditions)
	_ = json.Unmarshal(exclude, &v.Exclude)
	v.Exclude = v.Exclude.Clean()
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
	rows, err = s.pool.Query(r.Context(), `SELECT step, count(*)::int FROM journey_runs
		WHERE workspace_id = $1 AND journey_id = $2 AND status = 'active' AND step IS NOT NULL GROUP BY step`, a.WorkspaceID, v.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	v.AtStep = map[string]int{}
	for rows.Next() {
		var step string
		var n int
		if err := rows.Scan(&step, &n); err != nil {
			rows.Close()
			s.internal(w, r, err)
			return
		}
		v.AtStep[step] = n
	}
	if err := rows.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	rows, err = s.pool.Query(r.Context(), `SELECT rs.step, count(*) FILTER (WHERE e.delivered_at IS NOT NULL)::int,
		count(*) FILTER (WHERE e.opened_at IS NOT NULL)::int, count(*) FILTER (WHERE e.clicked_at IS NOT NULL)::int
		FROM journey_run_steps rs JOIN emails e ON e.id = rs.email_id AND e.workspace_id = rs.workspace_id
		WHERE rs.workspace_id = $1 AND rs.journey_id = $2 GROUP BY rs.step`, a.WorkspaceID, v.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	v.EmailStats = map[string]map[string]int{}
	for rows.Next() {
		var step string
		var delivered, opened, clicked int
		if err := rows.Scan(&step, &delivered, &opened, &clicked); err != nil {
			rows.Close()
			s.internal(w, r, err)
			return
		}
		v.EmailStats[step] = map[string]int{"delivered": delivered, "opened": opened, "clicked": clicked}
	}
	if err := rows.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type journeyFields struct {
	Name           *string               `json:"name"`
	Description    *string               `json:"description"`
	BrandID        optional[string]      `json:"brand_id"`
	Trigger        *journeyTrigger       `json:"trigger"`
	Entry          *string               `json:"entry"`
	GoalEvent      optional[string]      `json:"goal_event"`
	ExitEvents     *[]string             `json:"exit_events"`
	ExitConditions *[]journeys.Condition `json:"exit_conditions"`
	Exclude        *customers.Exclusion  `json:"exclude"`
	Steps          *[]journeys.Step      `json:"steps"`
}

// onlyLabels reports whether f changes nothing but the name and
// description, which can change while a journey runs.
func (f journeyFields) onlyLabels() bool {
	return !f.BrandID.Set && f.Trigger == nil && f.Entry == nil && !f.GoalEvent.Set && f.Steps == nil && f.ExitEvents == nil && f.ExitConditions == nil && f.Exclude == nil
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
	if f.ExitEvents != nil {
		if len(*f.ExitEvents) > 10 {
			return errInvalid("a journey has at most 10 exit events")
		}
		for _, e := range *f.ExitEvents {
			if !journeys.ValidEventName(e) {
				return errInvalid("exit_events are event names such as order_completed")
			}
		}
		v.ExitEvents = *f.ExitEvents
	}
	if f.ExitConditions != nil {
		if msg := journeys.CheckFilter(*f.ExitConditions); msg != "" {
			return errInvalid("exit_conditions: " + msg)
		}
		v.ExitConditions = *f.ExitConditions
	}
	if f.Exclude != nil {
		x, err := s.checkExclusion(r, ws, *f.Exclude)
		if err != nil {
			return err
		}
		v.Exclude = x
	}
	if f.Steps != nil {
		if msg := journeys.Check(*f.Steps); msg != "" {
			return errInvalid(msg)
		}
		if err := s.checkJourneyTemplates(r, ws, *f.Steps, false); err != nil {
			return err
		}
		if err := s.checkJourneyActionRefs(r.Context(), ws, *f.Steps, false); err != nil {
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
	v := journeyView{Trigger: journeyTrigger{Kind: "manual", Filter: []journeys.Condition{}}, Entry: "once", Steps: []journeys.Step{},
		ExitEvents: []string{}, ExitConditions: []journeys.Condition{}, Exclude: customers.Exclusion{}.Clean()}
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
		exitConds, _ := json.Marshal(v.ExitConditions)
		if err := tx.QueryRow(ctx, `INSERT INTO journeys (workspace_id, brand_id, name, description, trigger_kind, trigger_event, trigger_filter,
			entry, goal_event, steps, created_by, exit_events, exit_conditions, exclude) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) RETURNING id`,
			a.WorkspaceID, v.BrandID, v.Name, v.Description, v.Trigger.Kind, v.Trigger.Event, filter, v.Entry, v.GoalEvent, steps, createdBy,
			v.ExitEvents, exitConds, v.Exclude).Scan(&id); err != nil {
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
// its name and description change: make a draft version and publish it,
// or pause it, so no run is midway through steps that are being
// rewritten. Changing a paused journey moves everyone in it onto the
// change, as a publish with runs move does.
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
		// Any change to what it does is a new version; a paused journey's
		// runs go through it.
		bump := 0
		if !f.onlyLabels() {
			bump = 1
		}
		exitConds, _ := json.Marshal(v.ExitConditions)
		if _, err := tx.Exec(ctx, `UPDATE journeys SET brand_id = $3, name = $4, description = $5, trigger_kind = $6, trigger_event = $7,
			trigger_filter = $8, entry = $9, goal_event = $10, steps = $11, version = version + $12, exit_events = $13, exit_conditions = $14,
			exclude = $15, updated_at = now() WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, cur.ID, v.BrandID, v.Name, v.Description, v.Trigger.Kind, v.Trigger.Event,
			filter, v.Entry, v.GoalEvent, steps, bump, v.ExitEvents, exitConds, v.Exclude); err != nil {
			return err
		}
		if bump == 1 && cur.Status == "paused" {
			if _, err := tx.Exec(ctx, `UPDATE journey_runs SET journey_version = $3, updated_at = now()
				WHERE workspace_id = $1 AND journey_id = $2 AND status = 'active'`, a.WorkspaceID, cur.ID, cur.Version+1); err != nil {
				return err
			}
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
	if !s.journeyApplied(w, r, s.checkJourneyRefs(r, a.WorkspaceID, cur.Steps)) {
		return
	}
	if !s.journeyApplied(w, r, s.checkJourneyKeys(r, a.WorkspaceID, cur.ID, cur.Steps)) {
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
			// The version going live is kept, for the runs that enter on it.
			err = keepJourneyVersion(ctx, tx, a, cur.ID)
			if err == nil && cur.Status == "paused" {
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
	Version    *int           `json:"version"`
	StepsTaken int            `json:"steps_taken"`
	Context    map[string]any `json:"context"`
	// What its steps saved for later steps, such as a webhook's answer.
	Vars       map[string]any `json:"vars"`
	EnteredAt  time.Time      `json:"entered_at"`
	FinishedAt *time.Time     `json:"finished_at"`
	// What each step did, in order: only on a single run.
	History []journeyRunStepView `json:"history,omitempty"`
}

const journeyRunSelect = `
	SELECT r.id, r.journey_id, r.record_id, COALESCE(rc.name, ''), r.status, r.step, r.next_at, r.exit_reason, r.journey_version, r.steps_taken, r.context,
	       r.entered_at, r.finished_at, r.vars
	FROM journey_runs r LEFT JOIN records rc ON rc.id = r.record_id
	WHERE r.workspace_id = $1 AND r.journey_id = $2`

func scanJourneyRun(row pgx.Row) (journeyRunView, error) {
	var v journeyRunView
	var ctxJSON, varsJSON []byte
	err := row.Scan(&v.ID, &v.JourneyID, &v.RecordID, &v.Name, &v.Status, &v.Step, &v.NextAt, &v.ExitReason, &v.Version, &v.StepsTaken, &ctxJSON,
		&v.EnteredAt, &v.FinishedAt, &varsJSON)
	v.Context, v.Vars = map[string]any{}, map[string]any{}
	_ = json.Unmarshal(ctxJSON, &v.Context)
	_ = json.Unmarshal(varsJSON, &v.Vars)
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
	status, after, record, step := q.Get("status"), q.Get("after"), q.Get("record"), q.Get("step")
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
		AND ($7 = '' OR r.step = $7)
		ORDER BY r.entered_at DESC, r.id DESC LIMIT $6`, a.WorkspaceID, cur.ID, status, record, after, limit+1, step)
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

// listJourneyStepKinds is the catalog of step kinds the flow builder's
// step picker shows: each kind's group, label, settings and outputs.
func (s *Server) listJourneyStepKinds(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"data": journeys.StepKinds()})
}

// checkJourneyRefs checks, to go live, that the lists, journeys and
// sequences action steps name are the workspace's: a static list, a
// journey that isn't archived, a sequence that isn't archived.
func (s *Server) checkJourneyRefs(r *http.Request, ws string, steps []journeys.Step) error {
	for _, st := range steps {
		var q, ref, what string
		switch {
		case st.ListID != "" && (st.Kind == "add_to_list" || st.Kind == "remove_from_list"):
			q, ref, what = `SELECT EXISTS (SELECT 1 FROM smart_lists WHERE workspace_id = $1 AND id = $2 AND kind = 'static')`, st.ListID, "a static list"
		case st.JourneyID != "" && (st.Kind == "enter_journey" || st.Kind == "exit_journey"):
			q, ref, what = `SELECT EXISTS (SELECT 1 FROM journeys WHERE workspace_id = $1 AND id = $2 AND status <> 'archived')`, st.JourneyID, "a journey"
		case st.SequenceID != "" && (st.Kind == "enroll_in_sequence" || st.Kind == "stop_sequences"):
			q, ref, what = `SELECT EXISTS (SELECT 1 FROM sequences WHERE workspace_id = $1 AND id = $2 AND status <> 'archived')`, st.SequenceID, "a sequence"
		default:
			continue
		}
		var ok bool
		if err := s.pool.QueryRow(r.Context(), q, ws, ref).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errInvalid("step " + st.ID + ": " + ref + " is not " + what + " in this workspace")
		}
	}
	return s.checkJourneyActionRefs(r.Context(), ws, steps, true)
}

// checkExclusion checks an exclusion someone wants to save: its shape, and
// that its lists are the workspace's.
func (s *Server) checkExclusion(r *http.Request, ws string, x customers.Exclusion) (customers.Exclusion, error) {
	if msg := x.Check(); msg != "" {
		return x, errInvalid(msg)
	}
	ok, err := customers.CheckLists(r.Context(), s.pool, ws, x)
	if err != nil {
		return x, err
	}
	if !ok {
		return x, errInvalid("exclude.lists names a list that isn't in this workspace")
	}
	return x.Clean(), nil
}
