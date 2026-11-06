package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/missions"
)

// Missions: goals agents worked toward before the action layer. They are
// retired: what each one did stays readable, and every change to one, or
// sent with the Outflare-Mission header, answers 410 mission_retired.

func (s *Server) routesMissions(mux *http.ServeMux) {
	read, run, approve := auth.ScopeMissionsRead, auth.ScopeMissionsRun, auth.ScopeMissionsApprove
	base := "/v1/workspaces/{ws}/missions"
	// Missions are retired (ADR 13): what they did stays readable, and
	// every change to one answers 410 mission_retired. Agents work as
	// their own token on the action layer, and Outflare's agent runs
	// playbooks as agent runs.
	mux.HandleFunc("GET "+base, s.inWorkspace(read, s.listMissions))
	mux.HandleFunc("GET "+base+"/{id}", s.inWorkspace(read, s.getMission))
	mux.HandleFunc("GET "+base+"/{id}/calls", s.inWorkspace(read, s.listMissionCalls))
	mux.HandleFunc("GET "+base+"/{id}/actions", s.inWorkspace(read, s.listMissionActions))
	mux.HandleFunc("GET "+base+"/{id}/actions/{action}", s.inWorkspace(read, s.getMissionAction))
	mux.HandleFunc("GET "+base+"/{id}/updates", s.inWorkspace(read, s.listMissionUpdates))
	// The changes keep the scopes they had, so who gets 410 rather than
	// 403 is as before.
	for route, scope := range map[string]string{
		"POST " + base:                                    run,
		"POST " + base + "/plan":                          run,
		"PATCH " + base + "/{id}":                         run,
		"POST " + base + "/{id}/submit":                   run,
		"POST " + base + "/{id}/approve":                  approve,
		"POST " + base + "/{id}/pause":                    run,
		"POST " + base + "/{id}/resume":                   approve,
		"POST " + base + "/{id}/stop":                     run,
		"POST " + base + "/{id}/complete":                 run,
		"POST " + base + "/{id}/actions/{action}/approve": approve,
		"POST " + base + "/{id}/actions/{action}/reject":  approve,
		"POST " + base + "/{id}/updates":                  run,
		"POST " + base + "/{id}/updates/{update}/resolve": run,
	} {
		mux.HandleFunc(route, s.inWorkspace(scope, missionRetired))
	}
	// What the retired missions held for a person: kept as history.
	mux.HandleFunc("GET /v1/workspaces/{ws}/mission-actions", s.inWorkspace(read, s.listPendingActions))
}

// missionRetired answers every change to a mission, and every change made
// with the Outflare-Mission header.
func missionRetired(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusGone, "mission_retired", "missions are retired: what they did stays readable, and nothing changes one any more. Make changes as your own API token on the action layer (GET /v1/workspaces/{ws}/actions shows what ran, waits or was refused), or start a playbook as an agent run (POST /v1/workspaces/{ws}/playbooks/{id}/agent-runs, the MCP tool run_playbook)")
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
	PlaybookID      *string `json:"playbook_id"`
	PlaybookVersion *int    `json:"playbook_version"`
	// The playbook automation that started it, if any.
	AutomationID   *string   `json:"automation_id"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	creatorIsToken bool
	tokenActorID   *string
	workspaceID    string
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
	m.max_sends, m.max_spend_cents, m.ends_at, m.approved_by::text, ap.display_name, m.approved_at,
	m.started_at, m.ended_at, m.report, m.created_by::text, cr.display_name, COALESCE(cr.type = 'api_token', false),
	m.created_at, m.updated_at, m.workspace_id::text,
	(SELECT count(*) FROM mission_actions x WHERE x.mission_id = m.id AND x.status = 'pending'),
	(SELECT count(*) FROM mission_calls c WHERE c.mission_id = m.id),
	(SELECT max(created_at) FROM mission_calls c WHERE c.mission_id = m.id),
	m.runner, m.turns, m.last_turn_at, m.next_turn_at, m.turn_summary, m.turn_error, m.playbook_id, m.playbook_version, m.automation_id::text`

const missionFrom = `FROM missions m
	LEFT JOIN api_tokens t ON t.id = m.token_id
	LEFT JOIN actors ap ON ap.id = m.approved_by
	LEFT JOIN actors cr ON cr.id = m.created_by`

func scanMission(row pgx.Row) (missionView, error) {
	var v missionView
	err := row.Scan(&v.ID, &v.Name, &v.BrandID, &v.Goal.Text, &v.Goal.Metric, &v.Goal.Target, &v.Instructions,
		&v.Plan, &v.PlanUpdated, &v.Mode, &v.Status, &v.StatusReason, &v.TokenID, &v.TokenName, &v.tokenActorID,
		&v.Limits.MaxSends, &v.Limits.MaxSpendCents, &v.Limits.EndsAt, &v.ApprovedBy, &v.ApprovedByName, &v.ApprovedAt,
		&v.StartedAt, &v.EndedAt, &v.Report, &v.CreatedBy, &v.CreatedByName, &v.creatorIsToken,
		&v.CreatedAt, &v.UpdatedAt, &v.workspaceID, &v.PendingActions, &v.Calls, &v.LastCallAt,
		&v.Runner, &v.Turns.Count, &v.Turns.LastAt, &v.Turns.NextAt, &v.Turns.Summary, &v.Turns.Error, &v.PlaybookID, &v.PlaybookVersion, &v.AutomationID)
	v.StartedByAgent = v.creatorIsToken
	v.Writes = writePolicy(v.Status, v.Mode)
	return v, err
}

// builtinHeld are the writes Outflare's agent never makes on its own: one-off
// emails to an address it chose (lifecycle emails too), and changes to
// email providers, mailboxes, sending servers,
// senders, sign-ins, webhooks, the suppression list and a brand's context.
var builtinHeld = regexp.MustCompile(`^/v1/(workspaces|brands)/[^/]+/(inbox/[^/]+/reply|mailboxes/[^/]+/test-send|mailboxes|mailboxes/import|` +
	`sending-servers(/.*)?|senders(/.*)?|webhooks(/.*)?|suppressions/[^/]+|context|brands/[^/]+/context|api-tokens(/.*)?|integrations(/.*)?|emails|email-providers(/.*)?)/?$`)

// A mailbox by id: changing or removing one also waits for a person.
var builtinHeldMailbox = regexp.MustCompile(`^/v1/(workspaces|brands)/[^/]+/mailboxes/[0-9a-fA-F-]{36}/?$`)

func heldForBuiltin(method, path string) bool {
	switch method {
	case http.MethodGet, http.MethodHead:
		return false
	case http.MethodPatch, http.MethodPut, http.MethodDelete:
		if builtinHeldMailbox.MatchString(path) {
			return true
		}
	}
	return builtinHeld.MatchString(path) || builtinHeldAccountSecret.MatchString(path) || builtinHeldOffboard.MatchString(path)
}

// An account's secret in the vault: a person sets or removes it; an agent
// asking always waits (secrets never pass through a model).
var builtinHeldAccountSecret = regexp.MustCompile(`^/v1/brands/[^/]+/accounts/[^/]+/secrets/[^/]+/?$`)

// Offboarding a brand's accounts is a person's decision: it archives
// them all, empties their vault and revokes the brand's grants.
var builtinHeldOffboard = regexp.MustCompile(`^/v1/brands/[^/]+/accounts/offboard/?$`)

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
	v, err := s.loadMission(ctx, a.WorkspaceID, r.PathValue("id"))
	if s.missionError(w, r, err) {
		return
	}
	// What Outflare knows about the company and the mission's brand, so the
	// agent never asks the person again: their context and pinned memories.
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
	// The brand's context: each brand's is its own. One with no brand
	// reads the workspace's first brand's.
	var brand *brandContextView
	contextBrand := brandKey
	if contextBrand == "" {
		if err := s.pool.QueryRow(ctx, `SELECT COALESCE(outflare_first_brand($1)::text, '')`, a.WorkspaceID).Scan(&contextBrand); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	company := contextView{Version: 1, Missing: append([]string{}, onboardingFields...), AskForMissing: true}
	var read *brandContextView
	if contextBrand != "" {
		b, err := s.loadBrandContext(ctx, a.WorkspaceID, contextBrand)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		read, company = &b, companyView(b)
		if v.BrandID != nil {
			brand = read
		}
	}
	md := contextMarkdown(read, memories)
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

// pageArgs reads ?cursor= (a position before an id) and ?limit=; ok is
// false once it has answered a bad cursor.
func pageArgs(w http.ResponseWriter, r *http.Request) (before int64, limit int, ok bool) {
	v, ok := pageCursor(w, r, pageBefore)
	if !ok {
		return 0, 0, false
	}
	if v != "" {
		if before, _ = strconv.ParseInt(v, 10, 64); before <= 0 {
			invalidCursor(w, r)
			return 0, 0, false
		}
	}
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
	before, limit, ok := pageArgs(w, r)
	if !ok {
		return
	}
	list, err := s.missionUpdates(r.Context(), v.ID, before, limit, r.URL.Query().Get("open_requests") == "true")
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *int64
	if len(list) == limit {
		next = &list[len(list)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_cursor": nextCursor(pageBefore, next)})
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
	before, limit, ok := pageArgs(w, r)
	if !ok {
		return
	}
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
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_cursor": nextCursor(pageBefore, next)})
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

// summarize names a write in words, for people approving it: the
// operation's own summary in api/openapi.yaml ("Remember something"), else
// a name made from the path.
func summarize(method, path string) string {
	path, _, _ = strings.Cut(path, "?")
	if s := specSummary(method, path); s != "" {
		return s
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	// v1 workspaces {ws} <resource> [id] [sub...]
	if len(parts) < 4 {
		return method + " " + path
	}
	rest := parts[3:]
	noun := strings.ReplaceAll(rest[0], "-", " ")
	single := strings.TrimSuffix(noun, "s")
	if strings.HasSuffix(noun, "ies") {
		single = strings.TrimSuffix(noun, "ies") + "y"
	}
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

// ---- The gate ----

type ctxReplayKey struct{}
type ctxInjectedKey struct{}

// isMissionRoute reports whether r manages a mission itself; those calls
// are not gated, so an agent can always report and stop.
func isMissionRoute(r *http.Request) bool {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/workspaces/"+r.PathValue("ws"))
	return strings.HasPrefix(rest, "/missions") || strings.HasPrefix(rest, "/mission-actions")
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
