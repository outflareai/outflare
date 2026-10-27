package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/llm"
	"github.com/outflareai/outflare/engine/internal/planner"
)

// Planning a mission from a goal typed in plain words. Claude drafts it
// with the workspace's own key; the draft is saved as a mission waiting
// for approval, so nothing runs until a person says so.

// What a plan is assumed to cost when checking the workspace's AI limit.
const planEstimateCents = 10

func (s *Server) planMission(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Goal      string  `json:"goal"`
		ProjectID *string `json:"project_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Goal = strings.TrimSpace(req.Goal)
	if req.Goal == "" || len(req.Goal) > 2000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "goal is required, up to 2000 characters")
		return
	}
	ctx := r.Context()
	ai, err := s.aiModel(ctx, a.WorkspaceID, taskPlan)
	if s.aiError(w, r, err) {
		return
	}
	if s.aiError(w, r, s.checkAIBudget(ctx, a.WorkspaceID, ai, planEstimateCents)) {
		return
	}
	name := ai.Provider.Name
	in := missionInput{ProjectID: optional[string]{Set: req.ProjectID != nil, Value: req.ProjectID}}
	// Check the project and the token's project before spending on a plan.
	if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return s.checkMissionRefs(ctx, tx, a, &in, true) }); s.missionError(w, r, err) {
		return
	}
	snap, err := s.planSnapshot(ctx, a.WorkspaceID, in.ProjectID.Value)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	draft, usage, err := planner.Plan(ctx, planner.LLM{M: ai.Model}, req.Goal, snap)
	if err != nil {
		// A failed call can still have cost something.
		if serr := recordAISpend(ctx, s.pool, ai, a.WorkspaceID, nil, in.ProjectID.Value, &a.ActorID, usage.LLM, "mission-plan-failed:"+RequestID(ctx), name+" tried to plan a mission"); serr != nil {
			s.log.Error("plan spend", "err", serr)
		}
		switch {
		case errors.Is(err, llm.ErrAuth):
			writeError(w, r, http.StatusUnprocessableEntity, "connector_auth", name+" did not accept the API key. Reconnect "+name+" under Connectors with a working key.")
		case errors.Is(err, planner.ErrRefused), errors.Is(err, planner.ErrBadDraft):
			writeError(w, r, http.StatusBadGateway, "plan_failed", name+" could not draft a usable plan for this goal. Try rewording it, or add detail. ("+err.Error()+")")
		default:
			writeError(w, r, http.StatusBadGateway, "plan_failed", "Could not reach "+name+": "+err.Error())
		}
		return
	}

	name, goal, plan, instr := draft.Name, draft.Goal, draft.Markdown(), draft.Instructions
	in.Name, in.Goal, in.Plan, in.Instructions = &name, &goal, &plan, &instr
	if draft.GoalMetric != "" {
		in.GoalMetric = optional[string]{Set: true, Value: &draft.GoalMetric}
		in.GoalTarget = optional[int]{Set: true, Value: &draft.GoalTarget}
	}
	if draft.MaxSends > 0 {
		in.MaxSends = optional[int]{Set: true, Value: &draft.MaxSends}
	}
	if draft.MaxSpendCents > 0 {
		in.MaxSpendCents = optional[int64]{Set: true, Value: &draft.MaxSpendCents}
	}
	if draft.EndsOn != "" {
		end := draft.EndsOn + "T23:59:00Z"
		in.EndsAt = optional[string]{Set: true, Value: &end}
	}
	if err := in.validate(); s.missionError(w, r, err) {
		return
	}

	var id string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if id, err = s.insertMission(ctx, tx, a, &in); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE missions SET status = 'pending_approval', updated_at = now() WHERE id = $1`, id); err != nil {
			return err
		}
		note := "Planned by " + name + " (" + usage.Model + ") from the goal: “" + req.Goal + "”. Review the plan, then approve to start."
		if err := addUpdateData(ctx, tx, id, a.WorkspaceID, "", "system", "note", note, map[string]any{"planner": usage.Model}); err != nil {
			return err
		}
		for _, q := range draft.Questions {
			if q = strings.TrimSpace(q); q != "" {
				if err := addUpdateData(ctx, tx, id, a.WorkspaceID, "", "agent", "question", q, map[string]any{"planner": true}); err != nil {
					return err
				}
			}
		}
		for _, c := range draft.Connectors {
			data := map[string]any{"connector": c.ID, "planner": true}
			if d, ok := s.integrations.Registry.Definition(c.ID); ok {
				data["connector_name"] = d.Name
			}
			if err := addUpdateData(ctx, tx, id, a.WorkspaceID, "", "agent", "connect_request", strings.TrimSpace(c.Why), data); err != nil {
				return err
			}
		}
		// What the plan cost, on the workspace's own bill with the provider.
		if err := recordAISpend(ctx, tx, ai, a.WorkspaceID, &id, in.ProjectID.Value, &a.ActorID, usage.LLM, "mission-plan:"+id, name+" planned the mission"); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "mission.planned", "mission", id, map[string]any{
			"provider": ai.Provider.ID, "model": usage.Model, "input_tokens": usage.InputTokens, "output_tokens": usage.OutputTokens})
	})
	if s.missionError(w, r, err) {
		return
	}
	v, err := s.loadMission(ctx, a.WorkspaceID, id)
	if s.missionError(w, r, err) {
		return
	}
	needs := make([]map[string]string, 0, len(draft.Connectors))
	for _, c := range draft.Connectors {
		n := map[string]string{"id": c.ID, "why": c.Why, "name": c.ID}
		if d, ok := s.integrations.Registry.Definition(c.ID); ok {
			n["name"] = d.Name
		}
		needs = append(needs, n)
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"mission": v,
		"draft": map[string]any{
			"steps": nonNil(draft.Steps), "connectors": needs,
			"questions": nonNil(draft.Questions), "assumptions": nonNil(draft.Assumptions),
		},
		"usage": usage,
	})
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// planSnapshot summarises the workspace for the planner. It holds counts
// and names, never lead lists, email addresses or credentials.
func (s *Server) planSnapshot(ctx context.Context, ws string, projectID *string) (planner.Snapshot, error) {
	snap := planner.Snapshot{Today: time.Now().UTC(), Sequences: []planner.Sequence{}, Connected: []string{}, Catalog: []planner.CatalogItem{}}
	if err := s.pool.QueryRow(ctx, `SELECT name FROM workspaces WHERE id = $1`, ws).Scan(&snap.Workspace); err != nil {
		return snap, err
	}
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE status = 'active'), COALESCE(sum(daily_limit) FILTER (WHERE status = 'active'), 0)
		FROM mailboxes WHERE workspace_id = $1 AND ($2::uuid IS NULL OR project_id = $2)`, ws, projectID).
		Scan(&snap.Mailboxes.Total, &snap.Mailboxes.Ready, &snap.Mailboxes.DailyCapacity); err != nil {
		return snap, err
	}
	if err := s.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM leads WHERE workspace_id = $1), (SELECT count(*) FROM suppressions WHERE workspace_id = $1)`, ws).
		Scan(&snap.Leads.Total, &snap.Leads.Suppressed); err != nil {
		return snap, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, status FROM sequences WHERE workspace_id = $1 AND ($2::uuid IS NULL OR project_id = $2)
		ORDER BY updated_at DESC LIMIT 20`, ws, projectID)
	if err != nil {
		return snap, err
	}
	snap.Sequences, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (planner.Sequence, error) {
		var q planner.Sequence
		return q, r.Scan(&q.ID, &q.Name, &q.Status)
	})
	if err != nil {
		return snap, err
	}
	if projectID != nil {
		var p planner.Project
		var name, offer, audience, market, brief string
		if err := s.pool.QueryRow(ctx, `SELECT id, name, COALESCE(offer, ''), COALESCE(audience, ''), COALESCE(market, ''), COALESCE(brief, '') FROM projects WHERE id = $1`, *projectID).
			Scan(&p.ID, &name, &offer, &audience, &market, &brief); err != nil {
			return snap, err
		}
		p.Name = name
		p.Brief, _ = json.Marshal(map[string]string{"offer": offer, "audience": audience, "market": market, "brief": brief})
		snap.Project = &p
	}
	if s.integrations != nil {
		list, err := s.integrations.List(ctx, s.pool, ws, "")
		if err != nil {
			return snap, err
		}
		for _, i := range list {
			if i.Status == connectors.StatusConnected {
				snap.Connected = append(snap.Connected, i.Connector)
			}
		}
		for _, d := range s.integrations.Registry.All() {
			snap.Catalog = append(snap.Catalog, planner.CatalogItem{ID: d.ID, Name: d.Name, Category: d.Category, Status: d.Status, Summary: d.Summary})
		}
	}
	return snap, nil
}
