package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/playbooks"
)

// Agent runs (ADR 13): a goal Outflare's own agent works toward in turns,
// on the action layer. A run acts as an agent token of its own, whose
// grants come from the mode a person chose when they started it: copilot
// holds every change for a person, autonomous runs changes within a spend
// limit, dry run refuses and keeps every change so people see what it
// would have done. Every change is an action on the Agents page. Runs
// replace missions for playbooks; missions keep working beside them until
// they are retired.

func (s *Server) routesAgentRuns(mux *http.ServeMux) {
	read, run := auth.ScopeAgentsRead, auth.ScopeAgentsRun
	base := "/v1/workspaces/{ws}/agent-runs"
	mux.HandleFunc("GET "+base, s.inBrand(read, s.listAgentRuns))
	mux.HandleFunc("GET "+base+"/{id}", s.inBrand(read, s.getAgentRun))
	mux.HandleFunc("GET "+base+"/{id}/updates", s.inBrand(read, s.listAgentRunUpdates))
	mux.HandleFunc("POST "+base+"/{id}/updates", s.inBrand(run, s.addAgentRunUpdate))
	mux.HandleFunc("POST "+base+"/{id}/pause", s.inBrand(run, s.pauseAgentRun))
	mux.HandleFunc("POST "+base+"/{id}/resume", s.inBrand(run, s.resumeAgentRun))
	mux.HandleFunc("POST "+base+"/{id}/stop", s.inBrand(run, s.stopAgentRun))
	mux.HandleFunc("POST "+base+"/{id}/complete", s.inBrand(run, s.completeAgentRun))
}

// isAgentRunRoute reports whether r manages a run itself: a run's own
// bookkeeping (its updates, completing it), never a change to the
// workspace, so the action gate leaves it alone.
func isAgentRunRoute(r *http.Request) bool {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/workspaces/"+r.PathValue("ws"))
	return strings.HasPrefix(rest, "/agent-runs")
}

// Run modes.
const (
	runModeCopilot    = "copilot"
	runModeAutonomous = "autonomous"
	runModeDryRun     = "dry_run"
)

type agentRunView struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	BrandID         *string           `json:"brand_id"`
	Goal            string            `json:"goal"`
	Instructions    string            `json:"instructions"`
	Plan            string            `json:"plan"`
	Inputs          map[string]string `json:"inputs"`
	PlaybookID      *string           `json:"playbook_id"`
	PlaybookVersion *int              `json:"playbook_version"`
	AutomationID    *string           `json:"automation_id"`
	Mode            string            `json:"mode"`
	MaxSpendCents   *int64            `json:"max_spend_cents"`
	Status          string            `json:"status"`
	StatusReason    string            `json:"status_reason"`
	Report          string            `json:"report"`
	TokenID         *string           `json:"token_id"`
	Turns           int               `json:"turns"`
	NextTurnAt      *time.Time        `json:"next_turn_at"`
	LastTurnAt      *time.Time        `json:"last_turn_at"`
	TurnSummary     string            `json:"turn_summary"`
	TurnError       string            `json:"turn_error"`
	InTurn          bool              `json:"in_turn"`
	// What its changes came to: held waits for a person; spend is what
	// they spent on the workspace's own accounts, in cents.
	Actions     int   `json:"actions"`
	HeldActions int   `json:"held_actions"`
	SpendCents  int64 `json:"spend_cents"`
	// What its turns cost on the workspace's AI model, in cents.
	AICents       int64      `json:"ai_cents"`
	CreatedBy     *string    `json:"created_by"`
	CreatedByName *string    `json:"created_by_name"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	EndedAt       *time.Time `json:"ended_at"`
}

const agentRunColumns = `r.id, r.name, r.brand_id::text, r.goal, r.instructions, r.plan, r.inputs, r.playbook_id, r.playbook_version, r.automation_id::text,
	r.mode, r.max_spend_cents, r.status, r.status_reason, r.report, r.token_id::text, r.turns, r.next_turn_at, r.last_turn_at, r.turn_summary, r.turn_error,
	r.turn_started_at IS NOT NULL,
	(SELECT count(*) FROM actions x WHERE x.token_id = r.token_id),
	(SELECT count(*) FROM actions x WHERE x.token_id = r.token_id AND x.status = 'held' AND (x.expires_at IS NULL OR x.expires_at > now())),
	(SELECT coalesce(sum(e.amount_cents), 0) FROM actions x JOIN spend_entries e ON e.action_id = x.id WHERE x.token_id = r.token_id),
	(SELECT coalesce(sum(e.amount_cents), 0) FROM spend_entries e WHERE e.workspace_id = r.workspace_id AND e.ref LIKE 'agent-run-turn:' || r.id::text || ':%'),
	r.created_by::text, c.display_name, r.created_at, r.updated_at, r.ended_at`

const agentRunFrom = `FROM agent_runs r LEFT JOIN actors c ON c.id = r.created_by`

func scanAgentRun(row pgx.Row) (agentRunView, error) {
	var v agentRunView
	var inputs []byte
	err := row.Scan(&v.ID, &v.Name, &v.BrandID, &v.Goal, &v.Instructions, &v.Plan, &inputs, &v.PlaybookID, &v.PlaybookVersion, &v.AutomationID,
		&v.Mode, &v.MaxSpendCents, &v.Status, &v.StatusReason, &v.Report, &v.TokenID, &v.Turns, &v.NextTurnAt, &v.LastTurnAt, &v.TurnSummary, &v.TurnError,
		&v.InTurn, &v.Actions, &v.HeldActions, &v.SpendCents, &v.AICents, &v.CreatedBy, &v.CreatedByName, &v.CreatedAt, &v.UpdatedAt, &v.EndedAt)
	if err != nil {
		return v, err
	}
	v.Inputs = map[string]string{}
	_ = json.Unmarshal(inputs, &v.Inputs)
	return v, nil
}

func (s *Server) loadAgentRun(ctx context.Context, a *access, id string) (agentRunView, error) {
	if !validUUID(id) {
		return agentRunView{}, errNotFound
	}
	v, err := scanAgentRun(s.pool.QueryRow(ctx, `SELECT `+agentRunColumns+` `+agentRunFrom+` WHERE r.id = $1 AND r.workspace_id = $2`, id, a.WorkspaceID))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.sees(v.BrandID)) {
		return agentRunView{}, errNotFound
	}
	return v, err
}

// errRunState is a change a run's state doesn't allow.
type errRunState string

func (e errRunState) Error() string { return string(e) }

func (s *Server) agentRunError(w http.ResponseWriter, r *http.Request, err error) bool {
	var st errRunState
	var denied errDenied
	var invalid errInvalid
	switch {
	case err == nil:
		return false
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "agent run not found")
	case errors.As(err, &st):
		writeError(w, r, http.StatusConflict, "mission_state", st.Error())
	case errors.As(err, &denied):
		writeError(w, r, http.StatusForbidden, "forbidden", denied.Error())
	case errors.As(err, &invalid):
		writeError(w, r, http.StatusBadRequest, "invalid_request", invalid.Error())
	default:
		s.internal(w, r, err)
	}
	return true
}

func (s *Server) listAgentRuns(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	where, args := "r.workspace_id = $1", []any{a.WorkspaceID}
	// A run without a brand works for the whole workspace, so a picked
	// brand shows it beside its own, as for actions.
	where += actionBrandSQL(sel, "r.brand_id", &args)
	if st := r.URL.Query().Get("status"); st != "" {
		args = append(args, strings.Split(st, ","))
		where += " AND r.status = ANY($" + itoa(len(args)) + ")"
	}
	if p := r.URL.Query().Get("playbook_id"); p != "" {
		args = append(args, p)
		where += " AND r.playbook_id = $" + itoa(len(args))
	}
	limit, ok := pageLimit(w, r, 50, 200)
	if !ok {
		return
	}
	offset, ok := pageOffsetParam(w, r)
	if !ok {
		return
	}
	args = append(args, limit+1, offset)
	rows, err := s.pool.Query(r.Context(), `SELECT `+agentRunColumns+` `+agentRunFrom+` WHERE `+where+
		` ORDER BY r.created_at DESC, r.id DESC LIMIT $`+itoa(len(args)-1)+` OFFSET $`+itoa(len(args)), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (agentRunView, error) { return scanAgentRun(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *int
	if len(list) > limit {
		list = list[:limit]
		n := offset + limit
		next = &n
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_cursor": nextCursor(pageOffset, next)})
}

// getAgentRun is the run's check-in: the run, what Outflare knows about
// the company and its brand, the latest updates, and how to act. The
// agent reads it at the start of every turn.
func (s *Server) getAgentRun(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	v, err := s.loadAgentRun(ctx, a, r.PathValue("id"))
	if s.agentRunError(w, r, err) {
		return
	}
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
	brandKey := ""
	if v.BrandID != nil {
		brandKey = *v.BrandID
	}
	knowledge, err := s.knowledgeSummary(ctx, a, brandKey)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	updates, err := s.agentRunUpdates(ctx, v.ID, 30)
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
		"run":     v,
		"brand":   brand,
		"company": map[string]any{"context": company, "memories": memories, "knowledge": knowledge, "markdown": md + knowledgeMarkdown(knowledge)},
		"updates": updates,
		"how_to_act": map[string]string{
			"changes":  runChangesHelp(v.Mode),
			"updates":  "POST /agent-runs/" + v.ID + "/updates with kind progress, question or report and a body; a person's instructions come back as updates with author person.",
			"complete": "POST /agent-runs/" + v.ID + "/complete with a report when the goal is reached or clearly can't be.",
		},
	})
}

func runChangesHelp(mode string) string {
	switch mode {
	case runModeAutonomous:
		return "changes run on their own within the spend limit; some always wait for a person (202), and a 403 action_refused means the team doesn't allow it"
	case runModeDryRun:
		return "this is a dry run: every change is refused (403 action_refused) and kept, so people see what you would have done; carry on as if it had run"
	}
	return "every change waits for a person (202): make it once, carry on, and see the decision next turn"
}

type agentRunUpdateView struct {
	ID        int64     `json:"id"`
	Author    string    `json:"author"`
	ActorID   *string   `json:"actor_id"`
	ActorName *string   `json:"actor_name"`
	Kind      string    `json:"kind"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) agentRunUpdates(ctx context.Context, runID string, limit int) ([]agentRunUpdateView, error) {
	rows, err := s.pool.Query(ctx, `SELECT u.id, u.author, u.actor_id::text, ac.display_name, u.kind, u.body, u.created_at
		FROM agent_run_updates u LEFT JOIN actors ac ON ac.id = u.actor_id WHERE u.run_id = $1 ORDER BY u.id DESC LIMIT $2`, runID, limit)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (agentRunUpdateView, error) {
		var u agentRunUpdateView
		err := row.Scan(&u.ID, &u.Author, &u.ActorID, &u.ActorName, &u.Kind, &u.Body, &u.CreatedAt)
		return u, err
	})
	if list == nil {
		list = []agentRunUpdateView{}
	}
	slices.Reverse(list)
	return list, err
}

func (s *Server) listAgentRunUpdates(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := s.loadAgentRun(r.Context(), a, r.PathValue("id"))
	if s.agentRunError(w, r, err) {
		return
	}
	list, err := s.agentRunUpdates(r.Context(), v.ID, 200)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// addRunUpdate writes one update in tx; a person's brings the agent's next
// turn forward.
func addRunUpdate(ctx context.Context, tx pgx.Tx, runID, ws, actorID, author, kind, body string) error {
	var actor *string
	if actorID != "" {
		actor = &actorID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO agent_run_updates (run_id, workspace_id, brand_id, actor_id, author, kind, body)
		VALUES ($1, $2, (SELECT brand_id FROM agent_runs WHERE id = $1), $3, $4, $5, $6)`,
		runID, ws, actor, author, kind, body); err != nil {
		return err
	}
	if author == "person" {
		_, err := tx.Exec(ctx, `UPDATE agent_runs SET next_turn_at = now(), updated_at = now() WHERE id = $1 AND status = 'running'`, runID)
		return err
	}
	return nil
}

// addAgentRunUpdate: a person instructs or asks; the run's own agent
// reports progress, asks or files a report.
func (s *Server) addAgentRunUpdate(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	v, err := s.loadAgentRun(ctx, a, r.PathValue("id"))
	if s.agentRunError(w, r, err) {
		return
	}
	var in struct {
		Kind string `json:"kind"`
		Body string `json:"body"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Body = strings.TrimSpace(in.Body)
	// The run's own agent reports; everyone else (a person, or their own
	// app on a token) instructs.
	who := "person"
	if a.TokenID != "" && v.TokenID != nil && *v.TokenID == a.TokenID {
		who = "agent"
	}
	switch {
	case in.Body == "" || len(in.Body) > 20000:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "body is required, up to 20,000 characters")
		return
	case who == "person" && in.Kind != "instruction" && in.Kind != "note":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "write an instruction or a note; the run's own agent reports progress, questions and reports")
		return
	case who == "agent" && !slices.Contains([]string{"progress", "question", "report", "note"}, in.Kind):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "an agent writes progress, a question, a report or a note")
		return
	case who == "person" && a.TokenActionGate:
		writeError(w, r, http.StatusForbidden, "forbidden", "an agent on the action layer doesn't instruct other runs")
		return
	case v.Status != "running" && v.Status != "paused":
		writeError(w, r, http.StatusConflict, "mission_state", "the run is "+v.Status)
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := addRunUpdate(ctx, tx, v.ID, a.WorkspaceID, a.ActorID, who, in.Kind, in.Body); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "agent_run.updated", "agent_run", v.ID,
			map[string]any{"kind": in.Kind, "author": who}, map[string]any{"kind": in.Kind, "author": who})
	})
	if s.agentRunError(w, r, err) {
		return
	}
	list, err := s.agentRunUpdates(ctx, v.ID, 1)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, list[len(list)-1])
}

// changeAgentRun runs change on the locked run and answers the run.
func (s *Server) changeAgentRun(w http.ResponseWriter, r *http.Request, change func(ctx context.Context, tx pgx.Tx, a *access, v *agentRunView) error) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if !validUUID(r.PathValue("id")) {
			return errNotFound
		}
		v, err := scanAgentRun(tx.QueryRow(ctx, `SELECT `+agentRunColumns+` `+agentRunFrom+` WHERE r.id = $1 AND r.workspace_id = $2 FOR UPDATE OF r`, r.PathValue("id"), a.WorkspaceID))
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.sees(v.BrandID)) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if !a.writes(v.BrandID) {
			return errDenied("this run is another brand's")
		}
		if a.TokenActionGate && (v.TokenID == nil || *v.TokenID != a.TokenID) {
			return errDenied("an agent on the action layer doesn't manage other runs")
		}
		id = v.ID
		return change(ctx, tx, a, &v)
	})
	if s.agentRunError(w, r, err) {
		return
	}
	v, err := s.loadAgentRun(ctx, a, id)
	if s.agentRunError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) pauseAgentRun(w http.ResponseWriter, r *http.Request) {
	s.changeAgentRun(w, r, func(ctx context.Context, tx pgx.Tx, a *access, v *agentRunView) error {
		if v.Status != "running" {
			return errRunState("the run is " + v.Status + ", so there is nothing to pause")
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_runs SET status = 'paused', status_reason = 'paused by a person', next_turn_at = NULL, updated_at = now() WHERE id = $1`, v.ID); err != nil {
			return err
		}
		if err := addRunUpdate(ctx, tx, v.ID, a.WorkspaceID, "", "system", "note", "Paused by "+actorLabel(ctx, tx, a)+"."); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "agent_run.paused", "agent_run", v.ID, map[string]any{"name": v.Name})
	})
}

func (s *Server) resumeAgentRun(w http.ResponseWriter, r *http.Request) {
	s.changeAgentRun(w, r, func(ctx context.Context, tx pgx.Tx, a *access, v *agentRunView) error {
		if v.Status != "paused" {
			return errRunState("the run is " + v.Status + ", so there is nothing to resume")
		}
		if a.ActorType != "human" {
			return errDenied("only a person resumes a run")
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_runs SET status = 'running', status_reason = '', next_turn_at = now(), turn_error = '', turn_failures = 0, updated_at = now() WHERE id = $1`, v.ID); err != nil {
			return err
		}
		if err := addRunUpdate(ctx, tx, v.ID, a.WorkspaceID, "", "system", "note", "Resumed by "+actorLabel(ctx, tx, a)+"."); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "agent_run.resumed", "agent_run", v.ID, map[string]any{"name": v.Name})
	})
}

// endAgentRun ends a run in tx: its token is revoked, so nothing more can
// act as it.
func (s *Server) endAgentRun(ctx context.Context, tx pgx.Tx, a *access, v *agentRunView, status, reason, report string) error {
	if _, err := tx.Exec(ctx, `UPDATE agent_runs SET status = $2, status_reason = $3, report = CASE WHEN $4 <> '' THEN $4 ELSE report END,
		next_turn_at = NULL, ended_at = now(), updated_at = now() WHERE id = $1`, v.ID, status, reason, report); err != nil {
		return err
	}
	if v.TokenID != nil {
		if err := s.RevokeAgentToken(ctx, tx, a.WorkspaceID, *v.TokenID, a.UserID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) stopAgentRun(w http.ResponseWriter, r *http.Request) {
	s.changeAgentRun(w, r, func(ctx context.Context, tx pgx.Tx, a *access, v *agentRunView) error {
		if v.Status != "running" && v.Status != "paused" {
			return errRunState("the run is already " + v.Status)
		}
		if err := s.endAgentRun(ctx, tx, a, v, "stopped", "stopped by "+actorLabel(ctx, tx, a), ""); err != nil {
			return err
		}
		if err := addRunUpdate(ctx, tx, v.ID, a.WorkspaceID, "", "system", "note", "Stopped by "+actorLabel(ctx, tx, a)+"."); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "agent_run.stopped", "agent_run", v.ID, map[string]any{"name": v.Name, "turns": v.Turns})
	})
}

func (s *Server) completeAgentRun(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Report string `json:"report"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	in.Report = strings.TrimSpace(in.Report)
	if len(in.Report) > 50000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "report is up to 50,000 characters")
		return
	}
	s.changeAgentRun(w, r, func(ctx context.Context, tx pgx.Tx, a *access, v *agentRunView) error {
		if v.Status != "running" && v.Status != "paused" {
			return errRunState("the run is already " + v.Status)
		}
		if a.TokenID != "" && (v.TokenID == nil || *v.TokenID != a.TokenID) {
			return errDenied("only the run's own agent, or a person, completes it")
		}
		if err := s.endAgentRun(ctx, tx, a, v, "completed", "", in.Report); err != nil {
			return err
		}
		if in.Report != "" {
			if err := addRunUpdate(ctx, tx, v.ID, a.WorkspaceID, a.ActorID, author(a), "report", in.Report); err != nil {
				return err
			}
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "agent_run.completed", "agent_run", v.ID, map[string]any{"name": v.Name, "turns": v.Turns})
	})
}

// ---- Starting a run ----

// runSpec is what a run starts from.
type runSpec struct {
	Name, Goal, Instructions, Plan string
	Inputs                         map[string]string
	BrandID                        *string
	Mode                           string
	MaxSpendCents                  *int64
	PlaybookID                     *string
	PlaybookVersion                *int
	AutomationID                   *string
	// Note is the run's first system update; empty for the default.
	Note string
}

// runScopes are what a run's token holds: what an operator can do, never
// approving, and never more than the person who started it.
func runScopes(a *access) []string {
	var out []string
	for _, sc := range auth.RoleScopes(auth.RoleOperator) {
		if sc != auth.ScopeMissionsApprove && sc != auth.ScopeActionsApprove && a.has(sc) {
			out = append(out, sc)
		}
	}
	return out
}

// createAgentRun starts a run in tx as the caller: its token on the action
// layer, its grants from the mode, and its first turn now. The caller's
// rights cap the token's; a brand-limited caller's run is kept to a brand.
func (s *Server) createAgentRun(ctx context.Context, tx pgx.Tx, a *access, spec runSpec) (string, error) {
	switch {
	case strings.TrimSpace(spec.Name) == "":
		return "", errInvalid("name is required")
	case spec.Mode != runModeCopilot && spec.Mode != runModeAutonomous && spec.Mode != runModeDryRun:
		return "", errInvalid("mode is copilot, autonomous or dry_run")
	case spec.MaxSpendCents != nil && (*spec.MaxSpendCents < 1 || *spec.MaxSpendCents > 100_000_000_000):
		return "", errInvalid("max_spend_cents is 1 to 100,000,000,000; for a run that spends nothing, choose copilot")
	case !a.writes(spec.BrandID) && !(spec.BrandID == nil && a.Brands == nil):
		return "", errDenied("you can only start a run for your own brands")
	case spec.BrandID == nil && a.Brands != nil:
		return "", errInvalid("choose one of your brands for the run")
	}
	scopes := runScopes(a)
	if !a.has(auth.ScopeAgentsRun) {
		return "", errDenied("you need the agents.run scope to start a run")
	}
	name := strings.TrimSpace(spec.Name)
	if len(name) > 200 {
		name = name[:200]
	}
	tokenID, err := s.CreateAgentToken(ctx, tx, AgentSpec{WorkspaceID: a.WorkspaceID, Name: "Outflare agent: " + name, Scopes: scopes, BrandID: spec.BrandID, CreatedBy: a.UserID})
	if err != nil {
		return "", err
	}
	// The mode as grants: a person chose it, which is their approval of
	// what the run may do on its own.
	switch spec.Mode {
	case runModeAutonomous:
		// The run's budget is for good, not a month's: a run that lives
		// past the month doesn't get it again.
		in := grantInput{tokenID: tokenID, brand: spec.BrandID, path: "**", mode: "run", note: "Autonomous run: " + name, totalSpend: spec.MaxSpendCents}
		if _, err := insertGrantTx(ctx, tx, a, in); err != nil {
			return "", err
		}
	case runModeDryRun:
		if _, err := insertGrantTx(ctx, tx, a, grantInput{tokenID: tokenID, brand: spec.BrandID, path: "**", mode: "refuse", note: "Dry run: " + name}); err != nil {
			return "", err
		}
	}
	inputs, _ := json.Marshal(spec.Inputs)
	if spec.Inputs == nil {
		inputs = []byte("{}")
	}
	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO agent_runs (workspace_id, brand_id, token_id, name, goal, instructions, plan, inputs, playbook_id, playbook_version, automation_id,
		                        mode, max_spend_cents, next_turn_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, now(), $14) RETURNING id`,
		a.WorkspaceID, spec.BrandID, tokenID, name, spec.Goal, spec.Instructions, spec.Plan, inputs, spec.PlaybookID, spec.PlaybookVersion, spec.AutomationID,
		spec.Mode, spec.MaxSpendCents, a.ActorID).Scan(&id); err != nil {
		return "", err
	}
	note := spec.Note
	if note == "" {
		note = "Started by " + actorLabel(ctx, tx, a) + " in " + strings.ReplaceAll(spec.Mode, "_", " ") + " mode."
	}
	if err := addRunUpdate(ctx, tx, id, a.WorkspaceID, "", "system", "note", note); err != nil {
		return "", err
	}
	return id, recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "agent_run.created", "agent_run", id,
		map[string]any{"name": name, "mode": spec.Mode, "brand_id": spec.BrandID, "token_id": tokenID, "playbook_id": spec.PlaybookID, "automation_id": spec.AutomationID},
		map[string]any{"mode": spec.Mode, "playbook": spec.PlaybookID != nil, "automation": spec.AutomationID != nil})
}

// runPlaybook starts a playbook as an agent run: Outflare's agent carries
// its steps out in turns, as an agent token whose grants come from the
// mode, on the workspace's own AI model.
func (s *Server) runPlaybook(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	p, ok := playbooks.Get(r.PathValue("id"))
	if !ok {
		writeError(w, r, http.StatusNotFound, "not_found", "no playbook "+r.PathValue("id")+"; GET /v1/workspaces/{ws}/playbooks lists them")
		return
	}
	var in struct {
		Inputs        map[string]string `json:"inputs"`
		Mode          string            `json:"mode"`
		MaxSpendCents *int64            `json:"max_spend_cents"`
		Brand         *string           `json:"brand"`
		ApplyKit      bool              `json:"apply_kit"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Mode == "" {
		in.Mode = runModeCopilot
	}
	if msg := checkPlaybookInputs(p, in.Inputs); msg != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	var kit playbooks.Kit
	if in.ApplyKit {
		var ok bool
		if kit, ok = playbooks.GetKit(p.Kit); !ok {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "this playbook has no setup kit, so apply_kit is not for it")
			return
		}
		if !a.has(auth.ScopeCatalogManage) {
			writeError(w, r, http.StatusForbidden, "forbidden", "applying the setup kit needs the catalog.manage scope; start without apply_kit, or ask someone who can")
			return
		}
	}
	ready, err := s.playbookReadiness(ctx, a.WorkspaceID, []playbooks.Playbook{p})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if missing := ready[0].Readiness.MissingConnections; len(missing) > 0 {
		labels := make([]string, 0, len(missing))
		for _, m := range missing {
			labels = append(labels, m.Label)
		}
		writeError(w, r, http.StatusConflict, "connector_needed", "connect "+strings.Join(labels, " and ")+" under Connectors first; this playbook runs only on the workspace's own accounts")
		return
	}
	if _, err := s.aiModel(ctx, a.WorkspaceID, taskTurn); err != nil {
		var aiErr errAI
		if errors.As(err, &aiErr) {
			writeError(w, r, http.StatusConflict, aiErr.code, aiErr.msg)
			return
		}
		s.internal(w, r, err)
		return
	}
	var brand *string
	if in.Brand != nil {
		id, err := s.brandIDFor(ctx, a.WorkspaceID, *in.Brand)
		if errors.Is(err, errNotFound) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "brand not found: brand is a brand's id or handle")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		brand = &id
	} else if len(a.Brands) == 1 {
		brand = &a.Brands[0]
	}
	instructions, plan := playbookBrief(p, in.Inputs, true)
	var id string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if in.ApplyKit {
			kitInputs := map[string]string{}
			for _, ki := range kit.Inputs {
				if v, ok := in.Inputs[ki.Name]; ok {
					kitInputs[ki.Name] = v
				}
			}
			if _, err := applyKit(ctx, tx, a, kit, kitInputs, kitFromCatalog); err != nil {
				return err
			}
		}
		var err error
		id, err = s.createAgentRun(ctx, tx, a, runSpec{Name: p.Title, Goal: p.Summary, Instructions: instructions, Plan: plan, Inputs: in.Inputs,
			BrandID: brand, Mode: in.Mode, MaxSpendCents: in.MaxSpendCents, PlaybookID: &p.ID, PlaybookVersion: &p.Version})
		if err != nil {
			return err
		}
		props := map[string]any{"playbook_id": p.ID, "playbook_version": p.Version, "mode": in.Mode, "publisher": p.Publisher, "kit_applied": in.ApplyKit, "run_id": id}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "playbook.started", "agent_run", id,
			map[string]any{"playbook_id": p.ID, "playbook_version": p.Version, "run_id": id, "mode": in.Mode}, props)
	})
	if (errors.As(err, new(errKitConflict)) || errors.As(err, new(errInvalid)) || errors.As(err, new(errDenied))) && s.kitError(w, r, err) {
		return
	}
	if errors.Is(err, ErrAgentSpec) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if s.agentRunError(w, r, err) {
		return
	}
	v, err := s.loadAgentRun(ctx, a, id)
	if s.agentRunError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, v)
}
