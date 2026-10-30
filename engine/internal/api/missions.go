package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/missions"
)

// Missions: a goal an agent works toward for a brand. The agent acts
// with its ordinary API calls plus the Outflare-Mission header; missionGate
// decides, per call, whether it runs, waits for a person or is a dry run.

func (s *Server) routesMissions(mux *http.ServeMux) {
	read, run, approve := auth.ScopeMissionsRead, auth.ScopeMissionsRun, auth.ScopeMissionsApprove
	base := "/v1/workspaces/{ws}/missions"
	mux.HandleFunc("GET "+base, s.inWorkspace(read, s.listMissions))
	mux.HandleFunc("POST "+base, s.inWorkspace(run, s.createMission))
	mux.HandleFunc("POST "+base+"/plan", s.inWorkspace(run, s.planMission))
	mux.HandleFunc("GET "+base+"/{id}", s.inWorkspace(read, s.getMission))
	mux.HandleFunc("PATCH "+base+"/{id}", s.inWorkspace(run, s.updateMission))
	mux.HandleFunc("POST "+base+"/{id}/submit", s.inWorkspace(run, s.submitMission))
	mux.HandleFunc("POST "+base+"/{id}/approve", s.inWorkspace(approve, s.approveMission))
	mux.HandleFunc("POST "+base+"/{id}/pause", s.inWorkspace(run, s.pauseMission))
	mux.HandleFunc("POST "+base+"/{id}/resume", s.inWorkspace(approve, s.resumeMission))
	mux.HandleFunc("POST "+base+"/{id}/stop", s.inWorkspace(run, s.stopMission))
	mux.HandleFunc("POST "+base+"/{id}/complete", s.inWorkspace(run, s.completeMission))
	mux.HandleFunc("GET "+base+"/{id}/calls", s.inWorkspace(read, s.listMissionCalls))
	mux.HandleFunc("GET "+base+"/{id}/actions", s.inWorkspace(read, s.listMissionActions))
	mux.HandleFunc("GET "+base+"/{id}/actions/{action}", s.inWorkspace(read, s.getMissionAction))
	mux.HandleFunc("POST "+base+"/{id}/actions/{action}/approve", s.inWorkspace(approve, s.approveMissionAction))
	mux.HandleFunc("POST "+base+"/{id}/actions/{action}/reject", s.inWorkspace(approve, s.rejectMissionAction))
	mux.HandleFunc("GET "+base+"/{id}/updates", s.inWorkspace(read, s.listMissionUpdates))
	mux.HandleFunc("POST "+base+"/{id}/updates", s.inWorkspace(run, s.postMissionUpdate))
	mux.HandleFunc("POST "+base+"/{id}/updates/{update}/resolve", s.inWorkspace(run, s.resolveMissionUpdate))
	// Everything waiting for a person, across missions: the approval inbox.
	mux.HandleFunc("GET /v1/workspaces/{ws}/mission-actions", s.inWorkspace(read, s.listPendingActions))
}

// ---- Views ----

type missionGoal struct {
	Text   string  `json:"text"`
	Metric *string `json:"metric"`
	Target *int    `json:"target"`
	// Progress on the metric, and whether the target is reached.
	Current int  `json:"current"`
	Reached bool `json:"reached"`
}

type missionLimits struct {
	MaxSends      *int       `json:"max_sends"`
	MaxSpendCents *int64     `json:"max_spend_cents"`
	EndsAt        *time.Time `json:"ends_at"`
}

type missionView struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	BrandID      *string           `json:"brand_id"`
	Goal         missionGoal       `json:"goal"`
	Instructions string            `json:"instructions"`
	Plan         string            `json:"plan"`
	PlanUpdated  *time.Time        `json:"plan_updated_at"`
	Mode         string            `json:"mode"`
	Status       string            `json:"status"`
	StatusReason string            `json:"status_reason"`
	TokenID      *string           `json:"token_id"`
	TokenName    *string           `json:"token_name"`
	Limits       missionLimits     `json:"limits"`
	Progress     missions.Progress `json:"progress"`
	// What a write does right now: run, wait_for_approval, dry_run or refused.
	Writes         string     `json:"writes"`
	PendingActions int        `json:"pending_actions"`
	Calls          int        `json:"calls"`
	LastCallAt     *time.Time `json:"last_call_at"`
	BriefVersion   *int       `json:"brief_version"`
	ApprovedBy     *string    `json:"approved_by"`
	ApprovedByName *string    `json:"approved_by_name"`
	ApprovedAt     *time.Time `json:"approved_at"`
	StartedAt      *time.Time `json:"started_at"`
	EndedAt        *time.Time `json:"ended_at"`
	Report         string     `json:"report"`
	CreatedBy      *string    `json:"created_by"`
	CreatedByName  *string    `json:"created_by_name"`
	StartedByAgent bool       `json:"started_by_agent"`
	// Who carries the mission out: external (your own agent) or builtin
	// (Outflare's agent, in turns).
	Runner string       `json:"runner"`
	Turns  missionTurns `json:"turns"`
	// The playbook the mission was started from, if any.
	PlaybookID      *string   `json:"playbook_id"`
	PlaybookVersion *int      `json:"playbook_version"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	creatorIsToken  bool
	tokenActorID    *string
	workspaceID     string
}

// missionTurns is the built-in agent's schedule.
type missionTurns struct {
	Count  int        `json:"count"`
	LastAt *time.Time `json:"last_at"`
	NextAt *time.Time `json:"next_at"`
	// What the agent said it did last turn, and why the last turn failed.
	Summary string `json:"summary"`
	Error   string `json:"error"`
}

const missionColumns = `m.id, m.name, m.brand_id::text, m.goal, m.goal_metric, m.goal_target, m.instructions,
	m.plan, m.plan_updated_at, m.mode, m.status, m.status_reason, m.token_id::text, t.name, t.actor_id::text,
	m.max_sends, m.max_spend_cents, m.ends_at, m.brief_version, m.approved_by::text, ap.display_name, m.approved_at,
	m.started_at, m.ended_at, m.report, m.created_by::text, cr.display_name, COALESCE(cr.type = 'api_token', false),
	m.created_at, m.updated_at, m.workspace_id::text,
	(SELECT count(*) FROM mission_actions x WHERE x.mission_id = m.id AND x.status = 'pending'),
	(SELECT count(*) FROM mission_calls c WHERE c.mission_id = m.id),
	(SELECT max(created_at) FROM mission_calls c WHERE c.mission_id = m.id),
	m.runner, m.turns, m.last_turn_at, m.next_turn_at, m.turn_summary, m.turn_error, m.playbook_id, m.playbook_version`

const missionFrom = `FROM missions m
	LEFT JOIN api_tokens t ON t.id = m.token_id
	LEFT JOIN actors ap ON ap.id = m.approved_by
	LEFT JOIN actors cr ON cr.id = m.created_by`

func scanMission(row pgx.Row) (missionView, error) {
	var v missionView
	err := row.Scan(&v.ID, &v.Name, &v.BrandID, &v.Goal.Text, &v.Goal.Metric, &v.Goal.Target, &v.Instructions,
		&v.Plan, &v.PlanUpdated, &v.Mode, &v.Status, &v.StatusReason, &v.TokenID, &v.TokenName, &v.tokenActorID,
		&v.Limits.MaxSends, &v.Limits.MaxSpendCents, &v.Limits.EndsAt, &v.BriefVersion, &v.ApprovedBy, &v.ApprovedByName, &v.ApprovedAt,
		&v.StartedAt, &v.EndedAt, &v.Report, &v.CreatedBy, &v.CreatedByName, &v.creatorIsToken,
		&v.CreatedAt, &v.UpdatedAt, &v.workspaceID, &v.PendingActions, &v.Calls, &v.LastCallAt,
		&v.Runner, &v.Turns.Count, &v.Turns.LastAt, &v.Turns.NextAt, &v.Turns.Summary, &v.Turns.Error, &v.PlaybookID, &v.PlaybookVersion)
	v.StartedByAgent = v.creatorIsToken
	v.Writes = writePolicy(v.Status, v.Mode)
	return v, err
}

// builtinHeld are the writes Outflare's agent never makes on its own: one-off
// emails to an address it chose (lifecycle emails too), and changes to
// email providers, mailboxes, sending servers,
// senders, sign-ins, webhooks, the suppression list and the company context.
var builtinHeld = regexp.MustCompile(`^/v1/workspaces/[^/]+/(inbox/[^/]+/reply|mailboxes/[^/]+/test-send|mailboxes|mailboxes/import|` +
	`sending-servers(/.*)?|senders(/.*)?|oauth-connections(/.*)?|webhooks(/.*)?|suppressions/[^/]+|context|api-tokens(/.*)?|integrations(/.*)?|emails|email-providers(/.*)?)/?$`)

// A mailbox by id: changing or removing one also waits for a person.
var builtinHeldMailbox = regexp.MustCompile(`^/v1/workspaces/[^/]+/mailboxes/[0-9a-fA-F-]{36}/?$`)

func heldForBuiltin(method, path string) bool {
	switch method {
	case http.MethodGet, http.MethodHead:
		return false
	case http.MethodPatch, http.MethodPut, http.MethodDelete:
		if builtinHeldMailbox.MatchString(path) {
			return true
		}
	}
	return builtinHeld.MatchString(path)
}

// writePolicy is what happens to a write made in a mission.
func writePolicy(status, mode string) string {
	switch {
	case missions.Ended(status) || status == missions.StatusPaused:
		return "refused"
	case status != missions.StatusRunning || mode == missions.ModeDryRun:
		return "dry_run"
	case mode == missions.ModeApproveWrites:
		return "wait_for_approval"
	}
	return "run"
}

// loadMission reads one mission, ending it first if its end time passed.
func (s *Server) loadMission(ctx context.Context, ws, id string) (missionView, error) {
	if !validUUID(id) {
		return missionView{}, errNotFound
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE missions SET status = 'completed', status_reason = 'Reached its end time.', ended_at = ends_at, updated_at = now()
		WHERE id = $1 AND workspace_id = $2 AND ends_at <= now() AND status IN ('draft', 'pending_approval', 'running', 'paused')`, id, ws); err != nil {
		return missionView{}, err
	}
	v, err := scanMission(s.pool.QueryRow(ctx, `SELECT `+missionColumns+` `+missionFrom+` WHERE m.id = $1 AND m.workspace_id = $2`, id, ws))
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	if err != nil {
		return v, err
	}
	if v.Progress, err = missions.GetProgress(ctx, s.pool, ws, id); err != nil {
		return v, err
	}
	if v.Goal.Metric != nil {
		v.Goal.Current = v.Progress.Value(*v.Goal.Metric)
		v.Goal.Reached = v.Goal.Target != nil && v.Goal.Current >= *v.Goal.Target
	}
	return v, nil
}

// missionError writes err as a response and reports whether there was one.
func (s *Server) missionError(w http.ResponseWriter, r *http.Request, err error) bool {
	var inv errInvalid
	var conflict errMissionState
	switch {
	case err == nil:
		return false
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "mission not found")
	case errors.As(err, new(errDenied)):
		writeError(w, r, http.StatusForbidden, "forbidden", err.Error())
	case errors.As(err, &conflict):
		writeError(w, r, http.StatusConflict, "mission_state", string(conflict))
	case errors.As(err, &inv):
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
	default:
		s.internal(w, r, err)
	}
	return true
}

// errMissionState is a request the mission's current status does not allow.
type errMissionState string

func (e errMissionState) Error() string { return string(e) }

// ---- Listing and reading ----

func (s *Server) listMissions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	q := r.URL.Query()
	where := []string{"m.workspace_id = $1"}
	args := []any{a.WorkspaceID}
	if st := q.Get("status"); st != "" {
		args = append(args, strings.Split(st, ","))
		where = append(where, "m.status = ANY($"+strconv.Itoa(len(args))+")")
	}
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	if cond := brand.sql("m.brand_id", &args); cond != "" {
		where = append(where, strings.TrimPrefix(cond, " AND "))
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE missions SET status = 'completed', status_reason = 'Reached its end time.', ended_at = ends_at, updated_at = now()
		WHERE workspace_id = $1 AND ends_at <= now() AND status IN ('draft', 'pending_approval', 'running', 'paused')`, a.WorkspaceID); err != nil {
		s.internal(w, r, err)
		return
	}
	rows, err := s.pool.Query(ctx, `SELECT `+missionColumns+` `+missionFrom+` WHERE `+strings.Join(where, " AND ")+` ORDER BY
		CASE m.status WHEN 'pending_approval' THEN 0 WHEN 'running' THEN 1 WHEN 'paused' THEN 2 WHEN 'draft' THEN 3 ELSE 4 END, m.created_at DESC
		LIMIT 500`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (missionView, error) { return scanMission(r) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for i := range list {
		if list[i].Progress, err = missions.GetProgress(ctx, s.pool, a.WorkspaceID, list[i].ID); err != nil {
			s.internal(w, r, err)
			return
		}
		if m := list[i].Goal.Metric; m != nil {
			list[i].Goal.Current = list[i].Progress.Value(*m)
			list[i].Goal.Reached = list[i].Goal.Target != nil && list[i].Goal.Current >= *list[i].Goal.Target
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// getMission is what an agent reads when it checks in: the mission, the
// context it works from, what it may do and the latest notes from people.
func (s *Server) getMission(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	if _, err := s.resolveConnectedRequests(ctx, a.WorkspaceID); err != nil {
		s.internal(w, r, err)
		return
	}
	v, err := s.loadMission(ctx, a.WorkspaceID, r.PathValue("id"))
	if s.missionError(w, r, err) {
		return
	}
	// What Outflare knows about the company and the mission's brand, so the
	// agent never asks the person again: their context and pinned memories.
	company, err := s.loadContext(ctx, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	pinned := true
	memories, _, err := s.loadMemories(ctx, a, memoryFilter{pinned: &pinned, limit: 50})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// What the knowledge base holds for the mission's brand, and how to use it.
	brandKey := ""
	if v.BrandID != nil {
		brandKey = *v.BrandID
	}
	knowledge, err := s.knowledgeSummary(ctx, a, brandKey)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	updates, err := s.missionUpdates(ctx, v.ID, 0, 20, false)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	requests, err := s.missionUpdates(ctx, v.ID, 0, 100, true)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var brand *brandContextView
	md := contextMarkdown(company, memories)
	if v.BrandID != nil {
		b, err := s.loadBrandContext(ctx, a.WorkspaceID, *v.BrandID)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		brand = &b
		md += brandMarkdown(brand)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mission": v,
		"brand":   brand,
		"company": map[string]any{"context": company, "memories": memories, "knowledge": knowledge, "markdown": md + knowledgeMarkdown(knowledge)},
		"updates": updates,
		// Connectors the agent asked a person to set up, still open.
		"open_connect_requests": requests,
		"how_to_act": map[string]string{
			"header":        missions.Header + ": " + v.ID,
			"reason_header": missions.ReasonHeader + ": <why you are making the call>",
			"writes":        writesHelp(v.Writes),
		},
	})
}

func writesHelp(policy string) string {
	switch policy {
	case "run":
		return "Writes run straight away, within the mission's and your token's limits."
	case "wait_for_approval":
		return "Each write is held and answered with 202 and a mission_action. A person approves or rejects it; poll the action to see the result."
	case "dry_run":
		return "Writes are recorded as dry runs and not run. Reads work as normal. Submit your plan for approval when ready."
	}
	return "The mission is paused or over; writes in it are refused."
}

// ---- Creating and changing ----

type missionInput struct {
	Name *string `json:"name"`
	// Ignored: projects were removed. Accepted so older clients still work.
	ProjectID     optional[string] `json:"project_id"`
	Goal          *string          `json:"goal"`
	GoalMetric    optional[string] `json:"goal_metric"`
	GoalTarget    optional[int]    `json:"goal_target"`
	Instructions  *string          `json:"instructions"`
	Plan          *string          `json:"plan"`
	Mode          *string          `json:"mode"`
	TokenID       optional[string] `json:"token_id"`
	MaxSends      optional[int]    `json:"max_sends"`
	MaxSpendCents optional[int64]  `json:"max_spend_cents"`
	EndsAt        optional[string] `json:"ends_at"`
	endsAt        *time.Time
	// Set when a playbook starts the mission (playbooks.go), never from JSON.
	playbookID      *string
	playbookVersion *int
}

func (in *missionInput) validate() error {
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if *in.Name == "" || len(*in.Name) > 200 {
			return errInvalid("name is required, up to 200 characters")
		}
	}
	for _, f := range []*string{in.Goal, in.Instructions, in.Plan} {
		if f != nil && len(*f) > 50000 {
			return errInvalid("goal, instructions and plan are up to 50,000 characters each")
		}
	}
	if m := in.GoalMetric.Value; m != nil {
		switch *m {
		case "meetings", "replies", "sends", "leads", "revenue_attributed", "revenue_deals", "revenue_referred":
		default:
			return errInvalid("goal_metric is meetings, replies, sends, leads, revenue_attributed, revenue_deals or revenue_referred, or null")
		}
	}
	if t := in.GoalTarget.Value; t != nil && (*t < 1 || *t > 1000000) {
		return errInvalid("goal_target must be 1 to 1000000")
	}
	if in.GoalMetric.Set != in.GoalTarget.Set || (in.GoalMetric.Value == nil) != (in.GoalTarget.Value == nil) {
		return errInvalid("set goal_metric and goal_target together, for example meetings and 10")
	}
	if in.Mode != nil && *in.Mode != missions.ModeDryRun && *in.Mode != missions.ModeApproveWrites && *in.Mode != missions.ModeAutonomous {
		return errInvalid("mode is dry_run, approve_writes or autonomous")
	}
	if n := in.MaxSends.Value; n != nil && (*n < 1 || *n > 10000000) {
		return errInvalid("max_sends must be 1 to 10000000, or null for no mission limit")
	}
	if n := in.MaxSpendCents.Value; n != nil && (*n < 0 || *n > 100000000000) {
		return errInvalid("max_spend_cents must be 0 or more, or null for no mission limit")
	}
	if e := in.EndsAt.Value; e != nil {
		t, err := time.Parse(time.RFC3339, *e)
		if err != nil {
			return errInvalid("ends_at must be an RFC 3339 time, such as 2026-11-30T17:00:00+11:00")
		}
		if !t.After(time.Now()) {
			return errInvalid("ends_at must be in the future")
		}
		in.endsAt = &t
	}
	if id := in.TokenID.Value; id != nil && !validUUID(*id) {
		return errInvalid("token_id must be an id from this workspace")
	}
	return nil
}

// checkMissionRefs confirms the token belongs to the workspace, and that a
// token binds a mission only to itself.
func (s *Server) checkMissionRefs(ctx context.Context, tx pgx.Tx, a *access, in *missionInput, creating bool) error {
	if a.TokenID != "" {
		if creating && !in.TokenID.Set {
			in.TokenID = optional[string]{Set: true, Value: &a.TokenID}
		}
		if in.TokenID.Set && (in.TokenID.Value == nil || *in.TokenID.Value != a.TokenID) {
			return errDenied("a token can only run missions itself; a person can assign another token")
		}
	}
	if t := in.TokenID.Value; t != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM api_tokens WHERE id = $1 AND workspace_id = $2 AND revoked_at IS NULL)`, *t, a.WorkspaceID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errInvalid("token not found")
		}
	}
	return nil
}

func (s *Server) createMission(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in missionInput
	if !decode(w, r, &in) {
		return
	}
	if in.Name == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	if err := in.validate(); s.missionError(w, r, err) {
		return
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		id, err = s.insertMission(ctx, tx, a, &in)
		return err
	})
	if s.missionError(w, r, err) {
		return
	}
	v, err := s.loadMission(ctx, a.WorkspaceID, id)
	if s.missionError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// insertMission creates a draft mission from validated input, in tx.
func (s *Server) insertMission(ctx context.Context, tx pgx.Tx, a *access, in *missionInput) (string, error) {
	var id string
	if err := s.checkMissionRefs(ctx, tx, a, in, true); err != nil {
		return "", err
	}
	{
		plan := ""
		if in.Plan != nil {
			plan = *in.Plan
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO missions (workspace_id, name, goal, goal_metric, goal_target, instructions, plan, plan_updated_at,
			                      mode, token_id, max_sends, max_spend_cents, ends_at, created_by, playbook_id, playbook_version)
			VALUES ($1, $2, COALESCE($3, ''), $4, $5, COALESCE($6, ''), $7, CASE WHEN $7 <> '' THEN now() END,
			        COALESCE($8, 'dry_run'), $9, $10, $11, $12, $13, $14, $15)
			RETURNING id`,
			a.WorkspaceID, *in.Name, in.Goal, in.GoalMetric.Value, in.GoalTarget.Value, in.Instructions, plan,
			in.Mode, in.TokenID.Value, in.MaxSends.Value, in.MaxSpendCents.Value, in.endsAt, a.ActorID, in.playbookID, in.playbookVersion).Scan(&id); err != nil {
			return "", err
		}
	}
	return id, record(ctx, tx, &a.principal, a.WorkspaceID, "mission.created", "mission", id, map[string]any{"name": *in.Name})
}

// lockMission reads the mission's state for a change, in tx.
func lockMission(ctx context.Context, tx pgx.Tx, ws, id string) (status string, mode string, tokenID *string, err error) {
	if !validUUID(id) {
		return "", "", nil, errNotFound
	}
	err = tx.QueryRow(ctx, `SELECT status, mode, token_id::text FROM missions WHERE id = $1 AND workspace_id = $2 FOR UPDATE`, id, ws).
		Scan(&status, &mode, &tokenID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = errNotFound
	}
	return
}

func (s *Server) updateMission(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var in missionInput
	if !decode(w, r, &in) {
		return
	}
	if err := in.validate(); s.missionError(w, r, err) {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		status, _, _, err := lockMission(ctx, tx, a.WorkspaceID, id)
		if err != nil {
			return err
		}
		if missions.Ended(status) {
			return errMissionState("the mission is over and can no longer change")
		}
		// Once approved, what the agent may do changes only with approval.
		guarded := in.Mode != nil || in.TokenID.Set || in.MaxSends.Set || in.MaxSpendCents.Set || in.EndsAt.Set
		if guarded && (status == missions.StatusRunning || status == missions.StatusPaused) && !a.has(auth.ScopeMissionsApprove) {
			return errDenied("changing the mode, limits or token of an approved mission needs missions.approve")
		}
		if guarded && a.has(auth.ScopeMissionsApprove) && (status == missions.StatusRunning || status == missions.StatusPaused) {
			if err := s.notOwnMission(ctx, tx, a, id); err != nil {
				return err
			}
		}
		if err := s.checkMissionRefs(ctx, tx, a, &in, false); err != nil {
			return err
		}
		sets := []string{"updated_at = now()"}
		args := []any{id, a.WorkspaceID}
		set := func(col string, v any) {
			args = append(args, v)
			sets = append(sets, col+" = $"+strconv.Itoa(len(args)))
		}
		if in.Name != nil {
			set("name", *in.Name)
		}
		if in.Goal != nil {
			set("goal", *in.Goal)
		}
		if in.GoalMetric.Set {
			set("goal_metric", in.GoalMetric.Value)
			set("goal_target", in.GoalTarget.Value)
		}
		if in.Instructions != nil {
			set("instructions", *in.Instructions)
		}
		if in.Plan != nil {
			set("plan", *in.Plan)
			sets = append(sets, "plan_updated_at = now()")
		}
		if in.Mode != nil {
			set("mode", *in.Mode)
		}
		if in.TokenID.Set {
			set("token_id", in.TokenID.Value)
		}
		if in.MaxSends.Set {
			set("max_sends", in.MaxSends.Value)
		}
		if in.MaxSpendCents.Set {
			set("max_spend_cents", in.MaxSpendCents.Value)
		}
		if in.EndsAt.Set {
			set("ends_at", in.endsAt)
		}
		if _, err := tx.Exec(ctx, `UPDATE missions SET `+strings.Join(sets, ", ")+` WHERE id = $1 AND workspace_id = $2`, args...); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "mission.updated", "mission", id, map[string]any{"guarded": guarded})
	})
	if s.missionError(w, r, err) {
		return
	}
	v, err := s.loadMission(ctx, a.WorkspaceID, id)
	if s.missionError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// notOwnMission refuses an approval by the agent the mission belongs to: an
// agent never approves its own mission or its own writes.
func (s *Server) notOwnMission(ctx context.Context, tx pgx.Tx, a *access, id string) error {
	if a.TokenID == "" {
		return nil
	}
	var own bool
	err := tx.QueryRow(ctx, `
		SELECT m.token_id = $2 OR m.created_by = $3
		FROM missions m WHERE m.id = $1`, id, a.TokenID, a.ActorID).Scan(&own)
	if err != nil {
		return err
	}
	if own {
		return errDenied("an agent cannot approve its own mission; a person or another approver token must")
	}
	return nil
}

// transition moves a mission between statuses and writes a system update.
func (s *Server) transition(w http.ResponseWriter, r *http.Request, from []string, to, action string, prepare func(ctx context.Context, tx pgx.Tx, id string) (reason string, err error)) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		status, _, _, err := lockMission(ctx, tx, a.WorkspaceID, id)
		if err != nil {
			return err
		}
		ok := false
		for _, f := range from {
			ok = ok || f == status
		}
		if !ok {
			return errMissionState("the mission is " + strings.ReplaceAll(status, "_", " ") + ", so it cannot be " + strings.ReplaceAll(action, "_", " "))
		}
		reason := ""
		if prepare != nil {
			if reason, err = prepare(ctx, tx, id); err != nil {
				return err
			}
		}
		ended := ""
		if missions.Ended(to) {
			ended = ", ended_at = now()"
			// Writes still waiting will never run.
			if _, err := tx.Exec(ctx, `UPDATE mission_actions SET status = 'canceled', decided_at = now(), decision_note = 'The mission ended first.'
				WHERE mission_id = $1 AND status = 'pending'`, id); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE missions SET status = $2, status_reason = $3, updated_at = now()`+ended+` WHERE id = $1`, id, to, reason); err != nil {
			return err
		}
		body := "Mission " + action + " by " + actorLabel(ctx, tx, a) + "."
		if reason != "" {
			body += " " + reason
		}
		if err := addUpdate(ctx, tx, id, a.WorkspaceID, "", "system", "note", body); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "mission."+action, "mission", id, map[string]any{"from": status, "to": to, "reason": reason})
	})
	if s.missionError(w, r, err) {
		return
	}
	v, err := s.loadMission(ctx, a.WorkspaceID, id)
	if s.missionError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// actorLabel names who did something, for notes in the mission's activity.
func actorLabel(ctx context.Context, q pgx.Tx, a *access) string {
	var name string
	if err := q.QueryRow(ctx, `SELECT display_name FROM actors WHERE id = $1`, a.ActorID).Scan(&name); err == nil && name != "" {
		return name
	}
	if a.TokenID != "" {
		return "an agent"
	}
	return "a person"
}

// readReason reads an optional {"reason"|"note"|"report": "..."} body.
func readText(w http.ResponseWriter, r *http.Request, field string) (string, bool) {
	if r.ContentLength == 0 {
		return "", true
	}
	var body map[string]*string
	if !decode(w, r, &body) {
		return "", false
	}
	for k := range body {
		if k != field && !(field == "plan" && k == "note") {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "unknown field "+k+"; this endpoint takes "+field)
			return "", false
		}
	}
	if v := body[field]; v != nil {
		if len(*v) > 50000 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", field+" is up to 50,000 characters")
			return "", false
		}
		return strings.TrimSpace(*v), true
	}
	return "", true
}

// submitMission sends the plan to a person for approval. Until then the
// agent can rehearse it: writes in a draft mission are dry runs.
func (s *Server) submitMission(w http.ResponseWriter, r *http.Request) {
	plan, ok := readText(w, r, "plan")
	if !ok {
		return
	}
	s.transition(w, r, []string{missions.StatusDraft, missions.StatusPendingApproval}, missions.StatusPendingApproval, "submitted",
		func(ctx context.Context, tx pgx.Tx, id string) (string, error) {
			if plan != "" {
				if _, err := tx.Exec(ctx, `UPDATE missions SET plan = $2, plan_updated_at = now() WHERE id = $1`, id, plan); err != nil {
					return "", err
				}
			}
			return "", nil
		})
}

func (s *Server) approveMission(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Mode   *string `json:"mode"`
		Note   string  `json:"note"`
		Runner string  `json:"runner"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	if req.Mode != nil && *req.Mode != missions.ModeDryRun && *req.Mode != missions.ModeApproveWrites && *req.Mode != missions.ModeAutonomous {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "mode is dry_run, approve_writes or autonomous")
		return
	}
	switch req.Runner {
	case "", runnerExternal:
	case runnerBuiltin:
		// The AI model must be ready before the agent is handed the mission.
		if _, err := s.aiModel(r.Context(), a.WorkspaceID, taskTurn); s.aiError(w, r, err) {
			return
		}
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "runner is builtin (Outflare's agent runs it) or external (your own agent does)")
		return
	}
	s.transition(w, r, []string{missions.StatusDraft, missions.StatusPendingApproval}, missions.StatusRunning, "approved",
		func(ctx context.Context, tx pgx.Tx, id string) (string, error) {
			if err := s.notOwnMission(ctx, tx, a, id); err != nil {
				return "", err
			}
			if _, err := tx.Exec(ctx, `
				UPDATE missions m SET mode = COALESCE($2, m.mode), approved_by = $3, approved_at = now(), started_at = COALESCE(m.started_at, now())
				WHERE m.id = $1`, id, req.Mode, a.ActorID); err != nil {
				return "", err
			}
			switch req.Runner {
			case runnerBuiltin:
				if err := useBuiltinRunner(ctx, tx, a, id); err != nil {
					return "", err
				}
			case runnerExternal:
				if _, err := tx.Exec(ctx, `UPDATE missions SET runner = 'external', next_turn_at = NULL WHERE id = $1`, id); err != nil {
					return "", err
				}
			}
			return strings.TrimSpace(req.Note), nil
		})
}

func (s *Server) pauseMission(w http.ResponseWriter, r *http.Request) {
	reason, ok := readText(w, r, "reason")
	if !ok {
		return
	}
	s.transition(w, r, []string{missions.StatusRunning}, missions.StatusPaused, "paused",
		func(context.Context, pgx.Tx, string) (string, error) { return reason, nil })
}

func (s *Server) resumeMission(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	s.transition(w, r, []string{missions.StatusPaused}, missions.StatusRunning, "resumed",
		func(ctx context.Context, tx pgx.Tx, id string) (string, error) {
			if err := s.notOwnMission(ctx, tx, a, id); err != nil {
				return "", err
			}
			_, err := tx.Exec(ctx, `UPDATE missions SET next_turn_at = now(), turn_failures = 0, turn_error = '' WHERE id = $1 AND runner = 'builtin'`, id)
			return "", err
		})
}

// stopMission ends the mission at once: held writes are canceled and the
// campaigns it started are paused. A person can restart a campaign by hand.
func (s *Server) stopMission(w http.ResponseWriter, r *http.Request) {
	reason, ok := readText(w, r, "reason")
	if !ok {
		return
	}
	s.transition(w, r, []string{missions.StatusDraft, missions.StatusPendingApproval, missions.StatusRunning, missions.StatusPaused}, missions.StatusStopped, "stopped",
		func(ctx context.Context, tx pgx.Tx, id string) (string, error) {
			return reason, pauseMissionSequences(ctx, tx, id)
		})
}

// completeMission is the agent saying it is done, with its report.
func (s *Server) completeMission(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	report, ok := readText(w, r, "report")
	if !ok {
		return
	}
	s.transition(w, r, []string{missions.StatusRunning, missions.StatusPaused}, missions.StatusCompleted, "completed",
		func(ctx context.Context, tx pgx.Tx, id string) (string, error) {
			if _, err := tx.Exec(ctx, `UPDATE missions SET report = $2 WHERE id = $1`, id, report); err != nil {
				return "", err
			}
			if report != "" {
				if err := addUpdate(ctx, tx, id, a.WorkspaceID, a.ActorID, author(a), "report", report); err != nil {
					return "", err
				}
			}
			return "", pauseMissionSequences(ctx, tx, id)
		})
}

func pauseMissionSequences(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `UPDATE sequences SET status = 'paused', updated_at = now() WHERE mission_id = $1 AND status = 'active'`, id)
	return err
}

// ---- Updates ----

type missionUpdateView struct {
	ID         int64          `json:"id"`
	Author     string         `json:"author"`
	ActorID    *string        `json:"actor_id"`
	ActorName  *string        `json:"actor_name"`
	Kind       string         `json:"kind"`
	Body       string         `json:"body"`
	Data       map[string]any `json:"data"`
	ResolvedAt *time.Time     `json:"resolved_at"`
	CreatedAt  time.Time      `json:"created_at"`
}

func author(a *access) string {
	if a.TokenID != "" {
		return "agent"
	}
	return "person"
}

func addUpdate(ctx context.Context, tx pgx.Tx, missionID, ws, actorID, author, kind, body string) error {
	return addUpdateData(ctx, tx, missionID, ws, actorID, author, kind, body, nil)
}

func addUpdateData(ctx context.Context, tx pgx.Tx, missionID, ws, actorID, author, kind, body string, data map[string]any) error {
	var actor *string
	if actorID != "" {
		actor = &actorID
	}
	if data == nil {
		data = map[string]any{}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO mission_updates (mission_id, workspace_id, actor_id, author, kind, body, data) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		missionID, ws, actor, author, kind, body, data); err != nil {
		return err
	}
	if author == "person" {
		// Outflare's agent answers a person at once rather than at its next check-in.
		return wakeBuiltin(ctx, tx, missionID)
	}
	return nil
}

// validConnectorID accepts connector ids as the registry writes them:
// lower case letters, digits, dashes and underscores.
func validConnectorID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// resolveMissionUpdate closes a connect request: a person connected the
// tool, or decided not to. The agent sees it gone from open requests.
func (s *Server) resolveMissionUpdate(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	note, ok := readText(w, r, "note")
	if !ok {
		return
	}
	id := r.PathValue("id")
	uid, _ := strconv.ParseInt(r.PathValue("update"), 10, 64)
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, _, _, err := lockMission(ctx, tx, a.WorkspaceID, id); err != nil {
			return err
		}
		var connector string
		err := tx.QueryRow(ctx, `UPDATE mission_updates SET resolved_at = now(), resolved_by = $3
			WHERE id = $1 AND mission_id = $2 AND kind = 'connect_request' AND resolved_at IS NULL RETURNING data->>'connector'`, uid, id, a.ActorID).Scan(&connector)
		if errors.Is(err, pgx.ErrNoRows) {
			return errMissionState("no open connect request with that id")
		}
		if err != nil {
			return err
		}
		body := "Connect request for " + connector + " resolved."
		if note != "" {
			body += " " + note
		}
		if err := addUpdate(ctx, tx, id, a.WorkspaceID, a.ActorID, author(a), "instruction", body); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "mission.connect_request_resolved", "mission", id, map[string]any{"connector": connector})
	})
	if s.missionError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) missionUpdates(ctx context.Context, missionID string, before int64, limit int, openRequests bool) ([]missionUpdateView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id, u.author, u.actor_id::text, a.display_name, u.kind, u.body, u.data, u.resolved_at, u.created_at
		FROM mission_updates u LEFT JOIN actors a ON a.id = u.actor_id
		WHERE u.mission_id = $1 AND ($2 = 0 OR u.id < $2) AND (NOT $4 OR (u.kind = 'connect_request' AND u.resolved_at IS NULL))
		ORDER BY u.id DESC LIMIT $3`, missionID, before, limit, openRequests)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (missionUpdateView, error) {
		var v missionUpdateView
		err := r.Scan(&v.ID, &v.Author, &v.ActorID, &v.ActorName, &v.Kind, &v.Body, &v.Data, &v.ResolvedAt, &v.CreatedAt)
		return v, err
	})
}

func pageArgs(r *http.Request) (before int64, limit int) {
	before, _ = strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 200 {
		limit = 50
	}
	return
}

func (s *Server) listMissionUpdates(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := s.loadMission(r.Context(), a.WorkspaceID, r.PathValue("id"))
	if s.missionError(w, r, err) {
		return
	}
	before, limit := pageArgs(r)
	list, err := s.missionUpdates(r.Context(), v.ID, before, limit, r.URL.Query().Get("open_requests") == "true")
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *int64
	if len(list) == limit {
		next = &list[len(list)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}

func (s *Server) postMissionUpdate(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Kind      string `json:"kind"`
		Body      string `json:"body"`
		Connector string `json:"connector"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Body = strings.TrimSpace(req.Body)
	if req.Body == "" || len(req.Body) > 50000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "body is required, up to 50,000 characters")
		return
	}
	if req.Kind == "" {
		req.Kind = "note"
	}
	data := map[string]any{}
	switch req.Kind {
	case "note", "progress", "question", "instruction":
		if req.Connector != "" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "connector goes with kind connect_request")
			return
		}
	case "connect_request":
		if !validConnectorID(req.Connector) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "connect_request needs connector: the connector id from GET .../connectors, such as zerobounce")
			return
		}
		data["connector"] = req.Connector
		if s.integrations != nil {
			d, ok := s.integrations.Registry.Definition(req.Connector)
			if !ok {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "unknown connector "+req.Connector+"; GET /v1/connectors lists them")
				return
			}
			data["connector_name"] = d.Name
		}
		connected, err := connectorConnected(r.Context(), s.pool, a.WorkspaceID, req.Connector)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if connected {
			writeError(w, r, http.StatusConflict, "already_connected", req.Connector+" is already connected in this workspace; use it (GET /v1/workspaces/{ws}/integrations lists connections)")
			return
		}
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is note, progress, question, instruction or connect_request; finish with POST .../complete for the report")
		return
	}
	id := r.PathValue("id")
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		status, _, _, err := lockMission(ctx, tx, a.WorkspaceID, id)
		if err != nil {
			return err
		}
		if missions.Ended(status) {
			return errMissionState("the mission is over")
		}
		return addUpdateData(ctx, tx, id, a.WorkspaceID, a.ActorID, author(a), req.Kind, req.Body, data)
	})
	if s.missionError(w, r, err) {
		return
	}
	list, err := s.missionUpdates(ctx, id, 0, 1, false)
	if err != nil || len(list) == 0 {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, list[0])
}

// ---- Calls ----

type missionCallView struct {
	ID         int64     `json:"id"`
	ActorID    *string   `json:"actor_id"`
	ActorName  *string   `json:"actor_name"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	Query      string    `json:"query"`
	Outcome    string    `json:"outcome"`
	StatusCode int       `json:"status_code"`
	DurationMS int       `json:"duration_ms"`
	Reason     string    `json:"reason"`
	ActionID   *string   `json:"action_id"`
	RequestID  *string   `json:"request_id"`
	CreatedAt  time.Time `json:"created_at"`
}

func (s *Server) listMissionCalls(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := s.loadMission(r.Context(), a.WorkspaceID, r.PathValue("id"))
	if s.missionError(w, r, err) {
		return
	}
	before, limit := pageArgs(r)
	writesOnly := r.URL.Query().Get("writes") == "true"
	rows, err := s.pool.Query(r.Context(), `
		SELECT c.id, c.actor_id::text, ac.display_name, c.method, c.path, c.query, c.outcome, c.status_code, c.duration_ms,
		       c.reason, c.action_id::text, c.request_id, c.created_at
		FROM mission_calls c LEFT JOIN actors ac ON ac.id = c.actor_id
		WHERE c.mission_id = $1 AND ($2 = 0 OR c.id < $2) AND (NOT $4 OR c.method NOT IN ('GET', 'HEAD'))
		ORDER BY c.id DESC LIMIT $3`, v.ID, before, limit, writesOnly)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (missionCallView, error) {
		var c missionCallView
		err := r.Scan(&c.ID, &c.ActorID, &c.ActorName, &c.Method, &c.Path, &c.Query, &c.Outcome, &c.StatusCode, &c.DurationMS,
			&c.Reason, &c.ActionID, &c.RequestID, &c.CreatedAt)
		return c, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *int64
	if len(list) == limit {
		next = &list[len(list)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}

// ---- Actions ----

type missionActionView struct {
	ID           string          `json:"id"`
	MissionID    string          `json:"mission_id"`
	MissionName  string          `json:"mission_name"`
	ActorID      *string         `json:"actor_id"`
	ActorName    *string         `json:"actor_name"`
	Method       string          `json:"method"`
	Path         string          `json:"path"`
	Query        string          `json:"query"`
	Body         json.RawMessage `json:"body"`
	Reason       string          `json:"reason"`
	Summary      string          `json:"summary"`
	Status       string          `json:"status"`
	DecidedBy    *string         `json:"decided_by"`
	DecidedName  *string         `json:"decided_by_name"`
	DecidedAt    *time.Time      `json:"decided_at"`
	DecisionNote string          `json:"decision_note"`
	ResultStatus *int            `json:"result_status"`
	Result       json.RawMessage `json:"result"`
	RequestID    *string         `json:"request_id"`
	CreatedAt    time.Time       `json:"created_at"`
	contentType  string
	tokenID      *string
	rawBody      string
}

const actionColumns = `x.id, x.mission_id, m.name, x.actor_id::text, ac.display_name, x.method, x.path, x.query, x.body, x.content_type,
	x.reason, x.status, x.decided_by::text, dc.display_name, x.decided_at, x.decision_note, x.result_status, x.result, x.request_id,
	x.created_at, x.token_id::text`

const actionFrom = `FROM mission_actions x JOIN missions m ON m.id = x.mission_id
	LEFT JOIN actors ac ON ac.id = x.actor_id LEFT JOIN actors dc ON dc.id = x.decided_by`

func scanAction(row pgx.Row) (missionActionView, error) {
	var v missionActionView
	var result []byte
	err := row.Scan(&v.ID, &v.MissionID, &v.MissionName, &v.ActorID, &v.ActorName, &v.Method, &v.Path, &v.Query, &v.rawBody, &v.contentType,
		&v.Reason, &v.Status, &v.DecidedBy, &v.DecidedName, &v.DecidedAt, &v.DecisionNote, &v.ResultStatus, &result, &v.RequestID,
		&v.CreatedAt, &v.tokenID)
	if err != nil {
		return v, err
	}
	if json.Valid([]byte(v.rawBody)) {
		v.Body = json.RawMessage(v.rawBody)
	} else if v.rawBody != "" {
		b, _ := json.Marshal(v.rawBody)
		v.Body = b
	}
	if len(result) > 0 {
		v.Result = result
	}
	v.Summary = summarize(v.Method, v.Path)
	return v, nil
}

// summarize names a write in words, for people approving it.
func summarize(method, path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	// v1 workspaces {ws} <resource> [id] [sub...]
	if len(parts) < 4 {
		return method + " " + path
	}
	rest := parts[3:]
	noun := strings.ReplaceAll(rest[0], "-", " ")
	single := strings.TrimSuffix(noun, "s")
	switch {
	case len(rest) == 1 && method == http.MethodPost:
		return "Create " + single
	case len(rest) == 2 && method == http.MethodPatch:
		return "Change " + single
	case len(rest) == 2 && method == http.MethodDelete:
		return "Delete " + single
	case len(rest) >= 3:
		return strings.ToUpper(rest[len(rest)-1][:1]) + strings.ReplaceAll(rest[len(rest)-1][1:], "-", " ") + " (" + single + ")"
	}
	return method + " " + noun
}

func (s *Server) listMissionActions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := s.loadMission(r.Context(), a.WorkspaceID, r.PathValue("id"))
	if s.missionError(w, r, err) {
		return
	}
	args := []any{v.ID}
	where := "x.mission_id = $1"
	if st := r.URL.Query().Get("status"); st != "" {
		args = append(args, strings.Split(st, ","))
		where += " AND x.status = ANY($2)"
	}
	s.writeActions(w, r, where, args)
}

func (s *Server) listPendingActions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	s.writeActions(w, r, "x.workspace_id = $1 AND x.status = 'pending'", []any{a.WorkspaceID})
}

func (s *Server) writeActions(w http.ResponseWriter, r *http.Request, where string, args []any) {
	rows, err := s.pool.Query(r.Context(), `SELECT `+actionColumns+` `+actionFrom+` WHERE `+where+` ORDER BY x.created_at DESC LIMIT 500`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (missionActionView, error) { return scanAction(r) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) findAction(ctx context.Context, ws, missionID, id string) (missionActionView, error) {
	if !validUUID(missionID) || !validUUID(id) {
		return missionActionView{}, errNotFound
	}
	v, err := scanAction(s.pool.QueryRow(ctx, `SELECT `+actionColumns+` `+actionFrom+` WHERE x.id = $1 AND x.mission_id = $2 AND x.workspace_id = $3`, id, missionID, ws))
	if errors.Is(err, pgx.ErrNoRows) {
		err = errNotFound
	}
	return v, err
}

func (s *Server) getMissionAction(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := s.findAction(r.Context(), a.WorkspaceID, r.PathValue("id"), r.PathValue("action"))
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "action not found")
		return
	}
	if s.missionError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) decisionNote(w http.ResponseWriter, r *http.Request) (string, bool) {
	return readText(w, r, "note")
}

// approveMissionAction runs a held write, or a dry run once the mission is
// running, as the agent that made it.
func (s *Server) approveMissionAction(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	note, ok := s.decisionNote(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	missionID, actionID := r.PathValue("id"), r.PathValue("action")
	var act missionActionView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		status, _, _, err := lockMission(ctx, tx, a.WorkspaceID, missionID)
		if err != nil {
			return err
		}
		if status != missions.StatusRunning {
			return errMissionState("the mission is " + strings.ReplaceAll(status, "_", " ") + "; approve the mission (or resume it) before approving its writes")
		}
		if act, err = s.findAction(ctx, a.WorkspaceID, missionID, actionID); err != nil {
			return err
		}
		if act.Status != "pending" && act.Status != "simulated" {
			return errMissionState("this action is already " + act.Status)
		}
		if act.ActorID != nil && *act.ActorID == a.ActorID {
			return errDenied("an agent cannot approve its own writes")
		}
		if err := s.notOwnMission(ctx, tx, a, missionID); err != nil {
			return err
		}
		// Claim it, so two approvals cannot run it twice.
		tag, err := tx.Exec(ctx, `UPDATE mission_actions SET decided_by = $2, decided_at = now(), decision_note = $3
			WHERE id = $1 AND status IN ('pending', 'simulated') AND decided_at IS NULL`, actionID, a.ActorID, note)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errMissionState("this action is already being decided")
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "mission.action_approved", "mission_action", actionID, map[string]any{"mission_id": missionID, "path": act.Path, "method": act.Method})
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "action not found")
		return
	}
	if s.missionError(w, r, err) {
		return
	}

	code, result, runErr := s.replay(ctx, a.WorkspaceID, missionID, act)
	status := "executed"
	if runErr != nil || code >= 300 {
		status = "failed"
	}
	if runErr != nil {
		code = 0
		result, _ = json.Marshal(map[string]any{"error": map[string]string{"code": "not_run", "message": runErr.Error()}})
	}
	if !json.Valid(result) {
		result, _ = json.Marshal(string(result))
	}
	if _, err := s.pool.Exec(context.WithoutCancel(ctx), `UPDATE mission_actions SET status = $2, result_status = NULLIF($3, 0), result = $4 WHERE id = $1`,
		actionID, status, code, result); err != nil {
		s.internal(w, r, err)
		return
	}
	// Outflare's agent picks up from the change it was waiting on.
	if _, err := s.pool.Exec(context.WithoutCancel(ctx), `UPDATE missions SET next_turn_at = now() WHERE id = $1 AND runner = 'builtin' AND status = 'running'`, missionID); err != nil {
		s.internal(w, r, err)
		return
	}
	v, err := s.findAction(ctx, a.WorkspaceID, missionID, actionID)
	if s.missionError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) rejectMissionAction(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	note, ok := s.decisionNote(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	missionID, actionID := r.PathValue("id"), r.PathValue("action")
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, _, _, err := lockMission(ctx, tx, a.WorkspaceID, missionID); err != nil {
			return err
		}
		act, err := s.findAction(ctx, a.WorkspaceID, missionID, actionID)
		if err != nil {
			return err
		}
		if act.ActorID != nil && *act.ActorID == a.ActorID {
			return errDenied("an agent cannot decide on its own writes")
		}
		tag, err := tx.Exec(ctx, `UPDATE mission_actions SET status = 'rejected', decided_by = $2, decided_at = now(), decision_note = $3
			WHERE id = $1 AND status IN ('pending', 'simulated') AND decided_at IS NULL`, actionID, a.ActorID, note)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errMissionState("this action is already " + act.Status)
		}
		if note != "" {
			if err := addUpdate(ctx, tx, missionID, a.WorkspaceID, a.ActorID, author(a), "instruction", "Rejected \""+act.Summary+"\": "+note); err != nil {
				return err
			}
		}
		if err := wakeBuiltin(ctx, tx, missionID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "mission.action_rejected", "mission_action", actionID, map[string]any{"mission_id": missionID, "note": note})
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "action not found")
		return
	}
	if s.missionError(w, r, err) {
		return
	}
	v, err := s.findAction(ctx, a.WorkspaceID, missionID, actionID)
	if s.missionError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// ---- The gate ----

type ctxReplayKey struct{}
type ctxInjectedKey struct{}

// replay runs a stored write as the agent that made it, through the full
// handler so every rule (scopes, limits, policy, audit) applies again.
func (s *Server) replay(ctx context.Context, ws, missionID string, act missionActionView) (int, []byte, error) {
	if act.tokenID == nil && act.ActorID == nil {
		return 0, nil, errors.New("the agent that made this request no longer exists")
	}
	var p *principal
	if act.tokenID != nil {
		p = &principal{ActorType: "api_token"}
		err := s.pool.QueryRow(ctx, `SELECT id, actor_id, workspace_id, scopes FROM api_tokens
			WHERE id = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())
			  AND NOT EXISTS (SELECT 1 FROM users u WHERE u.id = api_tokens.created_by AND u.disabled_at IS NOT NULL)`, *act.tokenID).
			Scan(&p.TokenID, &p.ActorID, &p.TokenWorkspaceID, &p.TokenScopes)
		p.TokenScopes = auth.Rename(p.TokenScopes)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, errors.New("the agent's token was revoked or expired, so the request was not run")
		}
		if err != nil {
			return 0, nil, err
		}
	} else {
		p = &principal{ActorType: "human", ActorID: *act.ActorID}
		err := s.pool.QueryRow(ctx, `SELECT id FROM users WHERE actor_id = $1 AND disabled_at IS NULL`, *act.ActorID).Scan(&p.UserID)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, errors.New("the person who made this request can no longer sign in")
		}
		if err != nil {
			return 0, nil, err
		}
	}
	target := act.Path
	if act.Query != "" {
		target += "?" + act.Query
	}
	rctx := context.WithValue(context.WithValue(context.WithoutCancel(ctx), ctxInjectedKey{}, p), ctxReplayKey{}, act.ID)
	req, err := http.NewRequestWithContext(rctx, act.Method, target, strings.NewReader(act.rawBody))
	if err != nil {
		return 0, nil, err
	}
	if act.contentType != "" {
		req.Header.Set("Content-Type", act.contentType)
	}
	req.Header.Set(missions.Header, missionID)
	req.Header.Set(missions.ReasonHeader, "Approved: "+act.Reason)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes(), nil
}

// isMissionRoute reports whether r manages a mission itself; those calls
// are not gated, so an agent can always report and stop.
func isMissionRoute(r *http.Request) bool {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/workspaces/"+r.PathValue("ws"))
	return strings.HasPrefix(rest, "/missions") || strings.HasPrefix(rest, "/mission-actions")
}

// missionGate runs one call made inside a mission: it checks the caller
// may act in the mission, then runs, holds or dry-runs a write according
// to the mission's status and mode, and logs the call.
func (s *Server) missionGate(w http.ResponseWriter, r *http.Request, a *access, missionID string, next http.HandlerFunc) {
	start := time.Now()
	ctx := r.Context()
	reason := strings.TrimSpace(r.Header.Get(missions.ReasonHeader))
	if len(reason) > 1000 {
		reason = reason[:1000]
	}
	m, err := s.loadMission(ctx, a.WorkspaceID, missionID)
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "mission_not_found", "mission not found; check the "+missions.Header+" header")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	write := r.Method != http.MethodGet && r.Method != http.MethodHead
	replayID, replay := ctx.Value(ctxReplayKey{}).(string)
	call := missionCall{mission: m.ID, ws: a.WorkspaceID, actor: a.ActorID, r: r, reason: reason, start: start}
	if replay {
		call.action = replayID
	}
	refuse := func(status int, code, msg string) {
		call.outcome, call.status = "refused", status
		s.logCall(call)
		writeError(w, r, status, code, msg)
	}
	switch {
	case m.TokenID != nil && a.TokenID != *m.TokenID:
		refuse(http.StatusForbidden, "forbidden", "this mission is run by another API token")
		return
	case m.TokenID == nil && !a.has(auth.ScopeMissionsRun):
		refuse(http.StatusForbidden, "forbidden", "acting in a mission needs scope "+auth.ScopeMissionsRun)
		return
	case missions.Ended(m.Status):
		refuse(http.StatusConflict, "mission_ended", "the mission is "+m.Status+"; calls in it are refused")
		return
	}
	ctx = missions.WithID(ctx, m.ID)
	r = r.WithContext(ctx)
	call.r = r

	policy := writePolicy(m.Status, m.Mode)
	if policy == "run" && write && m.Runner == runnerBuiltin && heldForBuiltin(r.Method, r.URL.Path) {
		// Outflare's agent reads text from outside (replies, lead fields,
		// web pages) that may try to steer it, so the writes that could send
		// data out or change where mail goes wait for a person even when
		// the mission runs on its own.
		policy = "wait_for_approval"
	}
	heldWhy := ""
	if policy == "run" && write && !replay {
		// Fix autonomy: taking a fix the setting doesn't let through, or
		// changing the setting, waits for a person.
		if heldWhy, err = s.fixHold(ctx, a.WorkspaceID, r); err != nil {
			s.internal(w, r, err)
			return
		}
		if heldWhy != "" {
			policy = "wait_for_approval"
		}
	}
	if !write || replay || policy == "run" {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next(rec, r)
		call.outcome, call.status = "executed", rec.status
		s.logCall(call)
		return
	}
	if policy == "refused" {
		refuse(http.StatusConflict, "mission_paused", "the mission is paused; only reads are allowed until a person resumes it")
		return
	}

	// Hold the write: a dry run, or one waiting for a person.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		refuse(http.StatusRequestEntityTooLarge, "invalid_request", "a held request body is limited to 1 MiB")
		return
	}
	status := "simulated"
	if policy == "wait_for_approval" {
		status = "pending"
	}
	var tokenID *string
	if a.TokenID != "" {
		tokenID = &a.TokenID
	}
	var actionID string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO mission_actions (mission_id, workspace_id, actor_id, token_id, method, path, query, content_type, body, reason, status, request_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING id`,
			m.ID, a.WorkspaceID, a.ActorID, tokenID, r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Content-Type"),
			string(bytes.ToValidUTF8(body, nil)), reason, status, RequestID(ctx)).Scan(&actionID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "mission.action_"+map[string]string{"simulated": "simulated", "pending": "held"}[status],
			"mission_action", actionID, map[string]any{"method": r.Method, "path": r.URL.Path})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	call.action = actionID
	call.outcome, call.status = map[string]string{"simulated": "simulated", "pending": "queued"}[status], http.StatusAccepted
	s.logCall(call)
	act, err := s.findAction(ctx, a.WorkspaceID, m.ID, actionID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	msg := "Dry run: recorded in the mission and not run. Nothing changed."
	if status == "pending" {
		msg = "Held for approval: a person must approve this before it runs. Read the action to see the decision and result."
		if heldWhy != "" {
			msg = "Held for approval: " + heldWhy + ". Read the action to see the decision and result."
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"mission_action":    act,
		"dry_run":           status == "simulated",
		"approval_required": status == "pending",
		"message":           msg,
	})
}

type missionCall struct {
	mission, ws, actor string
	r                  *http.Request
	reason             string
	start              time.Time
	outcome            string
	status             int
	action             string
}

// logCall records a call. It runs after the call's own transaction, so a
// failure here is logged and never undoes the call.
func (s *Server) logCall(c missionCall) {
	var action *string
	if c.action != "" {
		action = &c.action
	}
	var actor *string
	if c.actor != "" {
		actor = &c.actor
	}
	_, err := s.pool.Exec(context.WithoutCancel(c.r.Context()), `
		INSERT INTO mission_calls (mission_id, workspace_id, actor_id, method, path, query, outcome, status_code, duration_ms, reason, action_id, request_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		c.mission, c.ws, actor, c.r.Method, c.r.URL.Path, c.r.URL.RawQuery, c.outcome, c.status,
		int(time.Since(c.start).Milliseconds()), c.reason, action, RequestID(c.r.Context()))
	if err != nil {
		s.log.Error("mission call log", "err", err, "mission_id", c.mission, "request_id", RequestID(c.r.Context()))
	}
}
