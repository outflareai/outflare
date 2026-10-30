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

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/missions"
	"github.com/outflareai/outflare/engine/internal/playbooks"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/webhooks"
)

// Playbook automations: a playbook that starts by itself, every day, week
// or month, or when an event from the webhook catalog happens. A person
// approves an automation once, with the mode its missions run in, who runs
// them and the spend limits. Each run is then a mission started from the
// playbook and approved inside those limits, carrying that person's
// approval and acting with their current rights. A run that would go past
// a limit, or can't run (a connection or the AI model gone, the approver
// gone), holds the automation until a person looks.
//
// This is the open tool. Choosing which playbooks to run toward a target
// and changing course from the results (an autopilot) is not here.

const (
	automationsBase = "/v1/workspaces/{ws}/playbook-automations"
	// Events listed in a gathered run's brief; the rest are counted.
	automationBriefEvents = 50
	// Runs an "each" automation starts in one pass, so one burst can't
	// hold up every other workspace.
	automationEachPerPass = 5
	// How long a run that failed for a passing reason waits to try again.
	automationRetry = 15 * time.Minute
)

func (s *Server) routesPlaybookAutomations(mux *http.ServeMux) {
	read, run, approve := auth.ScopeMissionsRead, auth.ScopeMissionsRun, auth.ScopeMissionsApprove
	mux.HandleFunc("GET "+automationsBase, s.inWorkspace(read, s.listPlaybookAutomations))
	mux.HandleFunc("POST "+automationsBase, s.inWorkspace(run, s.createPlaybookAutomation))
	mux.HandleFunc("GET "+automationsBase+"/{id}", s.inWorkspace(read, s.getPlaybookAutomation))
	mux.HandleFunc("PATCH "+automationsBase+"/{id}", s.inWorkspace(run, s.updatePlaybookAutomation))
	mux.HandleFunc("DELETE "+automationsBase+"/{id}", s.inWorkspace(run, s.deletePlaybookAutomation))
	mux.HandleFunc("POST "+automationsBase+"/{id}/approve", s.inWorkspace(approve, s.approvePlaybookAutomation))
	mux.HandleFunc("POST "+automationsBase+"/{id}/pause", s.inWorkspace(run, s.pausePlaybookAutomation))
	mux.HandleFunc("POST "+automationsBase+"/{id}/run", s.inWorkspace(run, s.runPlaybookAutomation))
	mux.HandleFunc("GET "+automationsBase+"/{id}/runs", s.inWorkspace(read, s.listPlaybookAutomationRuns))
}

type automationView struct {
	ID                    string            `json:"id"`
	PlaybookID            string            `json:"playbook_id"`
	Name                  string            `json:"name"`
	BrandID               *string           `json:"brand_id"`
	Inputs                map[string]string `json:"inputs"`
	Kind                  string            `json:"kind"`
	Cadence               *string           `json:"cadence"`
	RunHour               int               `json:"run_hour"`
	RunWeekday            int               `json:"run_weekday"`
	RunMonthday           int               `json:"run_monthday"`
	EventType             *string           `json:"event_type"`
	Gather                string            `json:"gather"`
	DailyCap              int               `json:"daily_cap"`
	Mode                  string            `json:"mode"`
	Runner                string            `json:"runner"`
	TokenID               *string           `json:"token_id"`
	MaxSpendCentsPerRun   *int64            `json:"max_spend_cents_per_run"`
	MaxSpendCentsPerMonth *int64            `json:"max_spend_cents_per_month"`
	// Spent by this month's runs, from the outcomes ledger.
	SpentThisMonthCents int64      `json:"spent_this_month_cents"`
	Status              string     `json:"status"`
	HeldReason          string     `json:"held_reason"`
	NextRunAt           *time.Time `json:"next_run_at"`
	LastRunAt           *time.Time `json:"last_run_at"`
	LastMissionID       *string    `json:"last_mission_id"`
	Runs                int        `json:"runs"`
	ApprovedBy          *string    `json:"approved_by"`
	ApprovedAt          *time.Time `json:"approved_at"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`

	lastEventID int64
}

const automationColumns = `id, playbook_id, name, brand_id::text, inputs, kind, cadence, run_hour, run_weekday, run_monthday,
	event_type, gather, daily_cap, mode, runner, token_id::text, max_spend_cents_per_run, max_spend_cents_per_month,
	status, held_reason, next_run_at, last_run_at, last_mission_id::text, runs, approved_by::text, approved_at,
	created_at, updated_at, last_event_id`

func scanAutomation(row pgx.Row) (automationView, error) {
	var v automationView
	var inputs []byte
	err := row.Scan(&v.ID, &v.PlaybookID, &v.Name, &v.BrandID, &inputs, &v.Kind, &v.Cadence, &v.RunHour, &v.RunWeekday, &v.RunMonthday,
		&v.EventType, &v.Gather, &v.DailyCap, &v.Mode, &v.Runner, &v.TokenID, &v.MaxSpendCentsPerRun, &v.MaxSpendCentsPerMonth,
		&v.Status, &v.HeldReason, &v.NextRunAt, &v.LastRunAt, &v.LastMissionID, &v.Runs, &v.ApprovedBy, &v.ApprovedAt,
		&v.CreatedAt, &v.UpdatedAt, &v.lastEventID)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	if err != nil {
		return v, err
	}
	v.Inputs = map[string]string{}
	return v, json.Unmarshal(inputs, &v.Inputs)
}

func (s *Server) loadAutomation(ctx context.Context, q pgx.Tx, ws, id string, lock bool) (automationView, error) {
	if !validUUID(id) {
		return automationView{}, errNotFound
	}
	sql := `SELECT ` + automationColumns + ` FROM playbook_automations WHERE id = $1 AND workspace_id = $2`
	if lock {
		sql += ` FOR UPDATE`
	}
	var row pgx.Row
	if q != nil {
		row = q.QueryRow(ctx, sql, id, ws)
	} else {
		row = s.pool.QueryRow(ctx, sql, id, ws)
	}
	return scanAutomation(row)
}

// automationSpend is what the automation's runs have spent since from,
// from the outcomes ledger.
func automationSpend(ctx context.Context, q pgx.Tx, ws, id string, from time.Time) (int64, error) {
	rows, err := q.Query(ctx, `SELECT id::text FROM missions WHERE workspace_id = $1 AND automation_id = $2 AND created_at >= $3`, ws, id, from)
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	var total int64
	for _, m := range ids {
		p, err := missions.Spent(ctx, q, ws, m)
		if err != nil {
			return 0, err
		}
		total += p
	}
	return total, nil
}

func (s *Server) automationOut(ctx context.Context, ws string, v automationView) (automationView, error) {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		v.SpentThisMonthCents, err = automationSpend(ctx, tx, ws, v.ID, monthStart(time.Now()))
		return err
	})
	return v, err
}

func (s *Server) automationError(w http.ResponseWriter, r *http.Request, err error) bool {
	var inv errInvalid
	var conflict errMissionState
	switch {
	case err == nil:
		return false
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "automation not found")
	case errors.As(err, new(errDenied)):
		writeError(w, r, http.StatusForbidden, "forbidden", err.Error())
	case errors.As(err, &conflict):
		writeError(w, r, http.StatusConflict, "automation_state", string(conflict))
	case errors.As(err, &inv):
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
	case errors.As(err, new(errConnectorNeeded)):
		writeError(w, r, http.StatusConflict, "connector_needed", err.Error())
	case errors.As(err, new(errAI)):
		s.aiError(w, r, err)
	default:
		s.internal(w, r, err)
	}
	return true
}

// errConnectorNeeded is a playbook connection the workspace hasn't made.
type errConnectorNeeded string

func (e errConnectorNeeded) Error() string { return string(e) }

func (s *Server) listPlaybookAutomations(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID}
	where := ""
	if p := r.URL.Query().Get("playbook_id"); p != "" {
		args = append(args, p)
		where = ` AND playbook_id = $2`
	}
	where += brand.sql("x.brand_id", &args)
	rows, err := s.pool.Query(ctx, `SELECT `+automationColumns+` FROM playbook_automations x WHERE workspace_id = $1`+where+` ORDER BY created_at DESC LIMIT 200`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var out []automationView
	for rows.Next() {
		v, err := scanAutomation(rows)
		if err != nil {
			rows.Close()
			s.internal(w, r, err)
			return
		}
		out = append(out, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	for i := range out {
		if out[i], err = s.automationOut(ctx, a.WorkspaceID, out[i]); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	if out == nil {
		out = []automationView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (s *Server) getPlaybookAutomation(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := s.loadAutomation(r.Context(), nil, a.WorkspaceID, r.PathValue("id"), false)
	if s.automationError(w, r, err) {
		return
	}
	v, err = s.automationOut(r.Context(), a.WorkspaceID, v)
	if s.automationError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type automationInput struct {
	PlaybookID            *string            `json:"playbook_id"`
	Name                  *string            `json:"name"`
	BrandID               optional[string]   `json:"brand_id"`
	Inputs                *map[string]string `json:"inputs"`
	Kind                  *string            `json:"kind"`
	Cadence               optional[string]   `json:"cadence"`
	RunHour               *int               `json:"run_hour"`
	RunWeekday            *int               `json:"run_weekday"`
	RunMonthday           *int               `json:"run_monthday"`
	EventType             optional[string]   `json:"event_type"`
	Gather                *string            `json:"gather"`
	DailyCap              *int               `json:"daily_cap"`
	Mode                  *string            `json:"mode"`
	Runner                *string            `json:"runner"`
	TokenID               optional[string]   `json:"token_id"`
	MaxSpendCentsPerRun   optional[int64]    `json:"max_spend_cents_per_run"`
	MaxSpendCentsPerMonth optional[int64]    `json:"max_spend_cents_per_month"`
}

// apply checks in and sets it on v. Anything but the name changes what a
// person approved, so the caller sends the automation back for approval.
func (in *automationInput) apply(ctx context.Context, tx pgx.Tx, a *access, v *automationView) (approvedChanged bool, err error) {
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len(n) > 200 {
			return false, errInvalid("name is required, up to 200 characters")
		}
		v.Name = n
	}
	set := func() { approvedChanged = true }
	if in.PlaybookID != nil {
		v.PlaybookID = *in.PlaybookID
		set()
	}
	p, ok := playbooks.Get(v.PlaybookID)
	if !ok {
		return false, errInvalid("no playbook " + v.PlaybookID + "; GET /v1/workspaces/{ws}/playbooks lists them")
	}
	if in.BrandID.Set {
		v.BrandID = in.BrandID.Value
		set()
	}
	if v.BrandID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM brands WHERE id::text = $1 AND workspace_id = $2)`, *v.BrandID, a.WorkspaceID).Scan(&ok); err != nil {
			return false, err
		}
		if !ok {
			return false, errInvalid("brand not found")
		}
	}
	if in.Inputs != nil {
		v.Inputs = *in.Inputs
		set()
	}
	if v.Inputs == nil {
		v.Inputs = map[string]string{}
	}
	for k, val := range v.Inputs {
		if !slices.ContainsFunc(p.Inputs, func(d playbooks.Input) bool { return d.Name == k }) {
			return false, errInvalid("this playbook has no input " + k)
		}
		if len(val) > 500 {
			return false, errInvalid("inputs are up to 500 characters each")
		}
	}
	for _, d := range p.Inputs {
		if !d.Optional && strings.TrimSpace(v.Inputs[d.Name]) == "" {
			return false, errInvalid(d.Name + " is required (" + d.Label + ")")
		}
	}
	if in.Kind != nil {
		v.Kind = *in.Kind
		set()
	}
	if in.Cadence.Set {
		v.Cadence = in.Cadence.Value
		set()
	}
	if in.EventType.Set {
		v.EventType = in.EventType.Value
		set()
	}
	for _, f := range []struct {
		in       *int
		out      *int
		min, max int
		name     string
	}{
		{in.RunHour, &v.RunHour, 0, 23, "run_hour"},
		{in.RunWeekday, &v.RunWeekday, 0, 6, "run_weekday"},
		{in.RunMonthday, &v.RunMonthday, 1, 28, "run_monthday"},
		{in.DailyCap, &v.DailyCap, 1, 100, "daily_cap"},
	} {
		if f.in == nil {
			continue
		}
		if *f.in < f.min || *f.in > f.max {
			return false, errInvalid(fmt.Sprintf("%s is %d to %d", f.name, f.min, f.max))
		}
		*f.out = *f.in
		set()
	}
	if in.Gather != nil {
		v.Gather = *in.Gather
		set()
	}
	switch v.Kind {
	case "schedule":
		if v.Cadence == nil || !slices.Contains(playbooks.Cadences, *v.Cadence) {
			return false, errInvalid("a schedule needs cadence: daily, weekly or monthly")
		}
		v.EventType = nil
	case "event":
		if v.EventType == nil || !webhooks.Delivered(*v.EventType) || *v.EventType == webhooks.TestEvent {
			return false, errInvalid("an event automation needs event_type, an event from the webhook catalog (GET /v1/webhook-events)")
		}
		if !slices.Contains(playbooks.GatherModes, v.Gather) {
			return false, errInvalid("gather is each, hourly or daily")
		}
		v.Cadence = nil
	default:
		return false, errInvalid("kind is schedule or event")
	}
	if in.Mode != nil {
		v.Mode = *in.Mode
		set()
	}
	if v.Mode != missions.ModeDryRun && v.Mode != missions.ModeApproveWrites && v.Mode != missions.ModeAutonomous {
		return false, errInvalid("mode is dry_run, approve_writes or autonomous")
	}
	if in.Runner != nil {
		v.Runner = *in.Runner
		set()
	}
	if in.TokenID.Set {
		v.TokenID = in.TokenID.Value
		set()
	}
	switch v.Runner {
	case runnerBuiltin:
		v.TokenID = nil
	case runnerExternal:
		if v.TokenID == nil {
			return false, errInvalid("an external runner needs token_id: the API token of the agent that runs the missions")
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM api_tokens WHERE id::text = $1 AND workspace_id = $2 AND revoked_at IS NULL)`, *v.TokenID, a.WorkspaceID).Scan(&ok); err != nil {
			return false, err
		}
		if !ok {
			return false, errInvalid("token not found")
		}
		if a.TokenID != "" && *v.TokenID != a.TokenID {
			return false, errDenied("a token can only run automations itself; a person can choose another token")
		}
	default:
		return false, errInvalid("runner is builtin (Outflare's agent runs each mission) or external (your own agent does)")
	}
	for _, lim := range []struct {
		in  optional[int64]
		out **int64
	}{{in.MaxSpendCentsPerRun, &v.MaxSpendCentsPerRun}, {in.MaxSpendCentsPerMonth, &v.MaxSpendCentsPerMonth}} {
		if !lim.in.Set {
			continue
		}
		if lim.in.Value != nil && (*lim.in.Value < 0 || *lim.in.Value > 100_000_000) {
			return false, errInvalid("spend limits are 0 to 100000000 cents, or null for none")
		}
		*lim.out = lim.in.Value
		set()
	}
	return approvedChanged, nil
}

func (s *Server) createPlaybookAutomation(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var in automationInput
	if !decode(w, r, &in) {
		return
	}
	if in.PlaybookID == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "playbook_id is required")
		return
	}
	p, ok := playbooks.Get(*in.PlaybookID)
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "no playbook "+*in.PlaybookID+"; GET /v1/workspaces/{ws}/playbooks lists them")
		return
	}
	// The playbook's suggestion fills what the caller leaves out.
	v := automationView{PlaybookID: p.ID, Name: p.Title, RunHour: 9, RunWeekday: 1, RunMonthday: 1, Gather: "daily", DailyCap: 10,
		Mode: missions.ModeApproveWrites, Runner: runnerBuiltin}
	if sg := p.Automation; sg != nil {
		if in.Kind == nil {
			k := "schedule"
			if sg.Schedule == "" {
				k = "event"
			}
			in.Kind = &k
		}
		if !in.Cadence.Set && sg.Schedule != "" {
			v.Cadence = &sg.Schedule
		}
		if !in.EventType.Set && len(sg.Events) > 0 {
			v.EventType = &sg.Events[0]
		}
		if sg.Gather != "" {
			v.Gather = sg.Gather
		}
	}
	if a.TokenID != "" && in.Runner == nil && !in.TokenID.Set {
		// An agent's automation runs its missions itself unless it asks
		// for Outflare's agent.
		ext := runnerExternal
		in.Runner, in.TokenID = &ext, optional[string]{Set: true, Value: &a.TokenID}
	}
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := in.apply(ctx, tx, a, &v); err != nil {
			return err
		}
		inputs, _ := json.Marshal(v.Inputs)
		if err := tx.QueryRow(ctx, `
			INSERT INTO playbook_automations (workspace_id, brand_id, playbook_id, name, inputs, kind, cadence, run_hour, run_weekday, run_monthday,
				event_type, gather, daily_cap, mode, runner, token_id, max_spend_cents_per_run, max_spend_cents_per_month, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19) RETURNING id`,
			a.WorkspaceID, v.BrandID, v.PlaybookID, v.Name, inputs, v.Kind, v.Cadence, v.RunHour, v.RunWeekday, v.RunMonthday,
			v.EventType, v.Gather, v.DailyCap, v.Mode, v.Runner, v.TokenID, v.MaxSpendCentsPerRun, v.MaxSpendCentsPerMonth, a.ActorID).Scan(&id); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "playbook_automation.created", "playbook_automation", id,
			map[string]any{"playbook_id": v.PlaybookID, "name": v.Name, "kind": v.Kind},
			automationProps(v))
	})
	if s.automationError(w, r, err) {
		return
	}
	out, err := s.loadAutomation(ctx, nil, a.WorkspaceID, id, false)
	if s.automationError(w, r, err) {
		return
	}
	out, err = s.automationOut(ctx, a.WorkspaceID, out)
	if s.automationError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// automationProps are an automation's product event properties: enums and
// ids only.
func automationProps(v automationView) map[string]any {
	p := map[string]any{"playbook_id": v.PlaybookID, "kind": v.Kind, "gather": v.Gather, "mode": v.Mode, "runner": v.Runner,
		"has_run_limit": v.MaxSpendCentsPerRun != nil, "has_month_limit": v.MaxSpendCentsPerMonth != nil}
	if v.Cadence != nil {
		p["cadence"] = *v.Cadence
	}
	if v.EventType != nil {
		p["event_type"] = *v.EventType
	}
	return p
}

// changeAutomation runs change on the locked automation and returns it.
func (s *Server) changeAutomation(w http.ResponseWriter, r *http.Request, change func(ctx context.Context, tx pgx.Tx, a *access, v *automationView) error) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	id := r.PathValue("id")
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		v, err := s.loadAutomation(ctx, tx, a.WorkspaceID, id, true)
		if err != nil {
			return err
		}
		return change(ctx, tx, a, &v)
	})
	if s.automationError(w, r, err) {
		return
	}
	v, err := s.loadAutomation(ctx, nil, a.WorkspaceID, id, false)
	if s.automationError(w, r, err) {
		return
	}
	v, err = s.automationOut(ctx, a.WorkspaceID, v)
	if s.automationError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) updatePlaybookAutomation(w http.ResponseWriter, r *http.Request) {
	var in automationInput
	if !decode(w, r, &in) {
		return
	}
	if in.PlaybookID != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "an automation's playbook can't change; make a new automation")
		return
	}
	s.changeAutomation(w, r, func(ctx context.Context, tx pgx.Tx, a *access, v *automationView) error {
		changed, err := in.apply(ctx, tx, a, v)
		if err != nil {
			return err
		}
		inputs, _ := json.Marshal(v.Inputs)
		status := v.Status
		if changed && status != "draft" {
			// What the person approved has changed: it waits for them again.
			status = "draft"
		}
		if _, err := tx.Exec(ctx, `
			UPDATE playbook_automations SET name = $3, brand_id = $4, inputs = $5, kind = $6, cadence = $7, run_hour = $8, run_weekday = $9,
				run_monthday = $10, event_type = $11, gather = $12, daily_cap = $13, mode = $14, runner = $15, token_id = $16,
				max_spend_cents_per_run = $17, max_spend_cents_per_month = $18, status = $19,
				approved_by = CASE WHEN $19 = 'draft' THEN NULL ELSE approved_by END,
				approved_at = CASE WHEN $19 = 'draft' THEN NULL ELSE approved_at END,
				next_run_at = CASE WHEN $19 = 'draft' THEN NULL ELSE next_run_at END,
				held_reason = CASE WHEN $19 = 'draft' THEN '' ELSE held_reason END, updated_at = now()
			WHERE id = $1 AND workspace_id = $2`,
			v.ID, a.WorkspaceID, v.Name, v.BrandID, inputs, v.Kind, v.Cadence, v.RunHour, v.RunWeekday, v.RunMonthday, v.EventType,
			v.Gather, v.DailyCap, v.Mode, v.Runner, v.TokenID, v.MaxSpendCentsPerRun, v.MaxSpendCentsPerMonth, status); err != nil {
			return err
		}
		props := automationProps(*v)
		props["needs_approval"] = changed && v.Status != "draft"
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "playbook_automation.updated", "playbook_automation", v.ID,
			map[string]any{"playbook_id": v.PlaybookID, "name": v.Name, "needs_approval": props["needs_approval"]}, props)
	})
}

func (s *Server) deletePlaybookAutomation(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		v, err := s.loadAutomation(ctx, tx, a.WorkspaceID, r.PathValue("id"), true)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM playbook_automations WHERE id = $1`, v.ID); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "playbook_automation.deleted", "playbook_automation", v.ID,
			map[string]any{"playbook_id": v.PlaybookID, "name": v.Name}, automationProps(v))
	})
	if s.automationError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// approvePlaybookAutomation turns an automation on. Only a person can:
// every run carries their approval, so an agent can set one up but not
// let it loose.
func (s *Server) approvePlaybookAutomation(w http.ResponseWriter, r *http.Request) {
	s.changeAutomation(w, r, func(ctx context.Context, tx pgx.Tx, a *access, v *automationView) error {
		if a.ActorType != "human" || a.UserID == "" {
			return errDenied("only a person can approve an automation: each run starts a mission with their approval")
		}
		if v.Status == "active" {
			return errMissionState("the automation is already running")
		}
		if err := s.automationReady(ctx, a, v); err != nil {
			return err
		}
		if v.Runner == runnerBuiltin {
			if !slices.Contains(builtinScopes(a), auth.ScopeMissionsRun) {
				return errDenied("you need the missions.run scope to hand the runs to Outflare's agent")
			}
		}
		// Events from before it was turned on (or while it was paused by
		// hand) never start a run. A held automation keeps them: they
		// waited for a person.
		cursor := v.lastEventID
		if v.Kind == "event" && v.Status != "held" {
			if err := tx.QueryRow(ctx, `SELECT COALESCE(max(id), 0) FROM audit_events WHERE workspace_id = $1`, a.WorkspaceID).Scan(&cursor); err != nil {
				return err
			}
		}
		next := nextAutomationRun(*v, time.Now())
		if _, err := tx.Exec(ctx, `
			UPDATE playbook_automations SET status = 'active', held_reason = '', approved_by = $3, approved_at = now(),
				next_run_at = $4, last_event_id = $5, updated_at = now()
			WHERE id = $1 AND workspace_id = $2`, v.ID, a.WorkspaceID, a.UserID, next, cursor); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "playbook_automation.approved", "playbook_automation", v.ID,
			map[string]any{"playbook_id": v.PlaybookID, "name": v.Name, "from": v.Status}, automationProps(*v))
	})
}

func (s *Server) pausePlaybookAutomation(w http.ResponseWriter, r *http.Request) {
	s.changeAutomation(w, r, func(ctx context.Context, tx pgx.Tx, a *access, v *automationView) error {
		if v.Status != "active" && v.Status != "held" {
			return errMissionState("the automation is " + v.Status + ", so there is nothing to pause")
		}
		if _, err := tx.Exec(ctx, `UPDATE playbook_automations SET status = 'paused', next_run_at = NULL, updated_at = now() WHERE id = $1`, v.ID); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "playbook_automation.paused", "playbook_automation", v.ID,
			map[string]any{"playbook_id": v.PlaybookID, "name": v.Name}, automationProps(*v))
	})
}

// runPlaybookAutomation starts a run now, inside the approved limits, as
// if its time had come: to try it, or to catch up.
func (s *Server) runPlaybookAutomation(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var missionID string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		v, err := s.loadAutomation(ctx, tx, a.WorkspaceID, r.PathValue("id"), true)
		if err != nil {
			return err
		}
		if v.Status != "active" {
			return errMissionState("the automation is " + v.Status + "; a person approves it before it runs")
		}
		missionID, err = s.fireAutomation(ctx, tx, v, "manual", nil, 0)
		return err
	})
	var held errHeld
	if errors.As(err, &held) {
		writeError(w, r, http.StatusConflict, "automation_state", "the automation is held: "+string(held))
		return
	}
	if s.automationError(w, r, err) {
		return
	}
	v, err := s.loadMission(ctx, a.WorkspaceID, missionID)
	if s.missionError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) listPlaybookAutomationRuns(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	v, err := s.loadAutomation(ctx, nil, a.WorkspaceID, r.PathValue("id"), false)
	if s.automationError(w, r, err) {
		return
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text FROM missions WHERE workspace_id = $1 AND automation_id = $2 ORDER BY created_at DESC LIMIT 50`, a.WorkspaceID, v.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := make([]missionView, 0, len(ids))
	for _, id := range ids {
		m, err := s.loadMission(ctx, a.WorkspaceID, id)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// automationReady checks what a run needs before it starts: the
// playbook's connections, and the AI model when Outflare's agent runs it.
func (s *Server) automationReady(ctx context.Context, a *access, v *automationView) error {
	p, ok := playbooks.Get(v.PlaybookID)
	if !ok {
		return errInvalid("the playbook " + v.PlaybookID + " is no longer shipped")
	}
	ready, err := s.playbookReadiness(ctx, a.WorkspaceID, []playbooks.Playbook{p})
	if err != nil {
		return err
	}
	if missing := ready[0].Readiness.MissingConnections; len(missing) > 0 {
		labels := make([]string, 0, len(missing))
		for _, m := range missing {
			labels = append(labels, m.Label)
		}
		return errConnectorNeeded("connect " + strings.Join(labels, " and ") + " under Connectors first; this playbook runs only on the workspace's own accounts")
	}
	if v.Runner == runnerBuiltin {
		if _, err := s.aiModel(ctx, a.WorkspaceID, taskTurn); err != nil {
			return err
		}
	}
	return nil
}

// nextAutomationRun is when an active automation next looks at whether to
// run: its schedule, the next hour or day for gathered events, or a minute
// from now for a run per event.
func nextAutomationRun(v automationView, now time.Time) time.Time {
	now = now.UTC()
	at := func(d time.Time) time.Time {
		return time.Date(d.Year(), d.Month(), d.Day(), v.RunHour, 0, 0, 0, time.UTC)
	}
	if v.Kind == "event" {
		switch v.Gather {
		case "each":
			return now.Add(time.Minute)
		case "hourly":
			return now.Truncate(time.Hour).Add(time.Hour)
		}
		t := at(now)
		if !t.After(now) {
			t = t.AddDate(0, 0, 1)
		}
		return t
	}
	cadence := ""
	if v.Cadence != nil {
		cadence = *v.Cadence
	}
	switch cadence {
	case "weekly":
		t := at(now).AddDate(0, 0, (v.RunWeekday-int(now.Weekday())+7)%7)
		if !t.After(now) {
			t = t.AddDate(0, 0, 7)
		}
		return t
	case "monthly":
		t := time.Date(now.Year(), now.Month(), v.RunMonthday, v.RunHour, 0, 0, 0, time.UTC)
		if !t.After(now) {
			t = t.AddDate(0, 1, 0)
		}
		return t
	}
	t := at(now)
	if !t.After(now) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

// ---- Running ----

// errHeld stops a run for a reason a person must act on; the automation
// is held with it.
type errHeld string

func (e errHeld) Error() string { return string(e) }

// automationEvent is one event a run was started for.
type automationEvent struct {
	ID         int64
	Action     string
	TargetType *string
	TargetID   *string
	At         time.Time
}

// approverAccess is the approving person's current rights in the
// workspace: a run never does more than they still could.
func (s *Server) approverAccess(ctx context.Context, tx pgx.Tx, v automationView, ws string) (*access, error) {
	if v.ApprovedBy == nil {
		return nil, errHeld("nobody has approved it; a person approves it again")
	}
	p, err := s.personPrincipal(ctx, tx, *v.ApprovedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errHeld("the person who approved it no longer has an account; a person approves it again")
	}
	if err != nil {
		return nil, err
	}
	a := &access{principal: *p, WorkspaceID: ws}
	var brands, stored []string
	err = tx.QueryRow(ctx, `SELECT role, brand_ids::text[], role_id::text, scopes FROM (`+workspaceRoles+`) r ORDER BY rank DESC, cardinality(brand_ids) LIMIT 1`, ws, p.UserID).
		Scan(&a.Role, &brands, &a.RoleID, &stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errHeld("the person who approved it is no longer a member of the workspace; a person approves it again")
	}
	if err != nil {
		return nil, err
	}
	a.Scopes = effectiveScopes(a.Role, stored)
	if len(brands) > 0 && auth.BrandLimitable(a.Scopes) {
		return nil, errHeld("the person who approved it is now limited to some brands; someone with every brand approves it again")
	}
	if !a.has(auth.ScopeMissionsApprove) || !a.has(auth.ScopeMissionsRun) {
		return nil, errHeld("the person who approved it can no longer run and approve missions; someone who can approves it again")
	}
	return a, nil
}

// fireAutomation starts one run of v in tx: a mission from the playbook,
// approved with the automation's mode and limits. trigger is schedule,
// event or manual; events are what started it, with total counting any
// not listed.
func (s *Server) fireAutomation(ctx context.Context, tx pgx.Tx, v automationView, trigger string, events []automationEvent, total int) (string, error) {
	ws, err := automationWorkspace(ctx, tx, v.ID)
	if err != nil {
		return "", err
	}
	a, err := s.approverAccess(ctx, tx, v, ws)
	if err != nil {
		return "", err
	}
	if err := s.automationReady(ctx, a, &v); err != nil {
		var inv errInvalid
		var conn errConnectorNeeded
		var ai errAI
		switch {
		case errors.As(err, &inv):
			return "", errHeld(string(inv))
		case errors.As(err, &conn):
			return "", errHeld(string(conn))
		case errors.As(err, &ai):
			return "", errHeld(ai.msg)
		}
		return "", err
	}
	runLimit := v.MaxSpendCentsPerRun
	if v.MaxSpendCentsPerMonth != nil {
		spent, err := automationSpend(ctx, tx, ws, v.ID, monthStart(time.Now()))
		if err != nil {
			return "", err
		}
		left := *v.MaxSpendCentsPerMonth - spent
		if left <= 0 {
			return "", errHeld(fmt.Sprintf("this month's runs have spent its monthly limit of $%.2f; raise the limit or approve it again next month", float64(*v.MaxSpendCentsPerMonth)/100))
		}
		if runLimit == nil || *runLimit > left {
			runLimit = &left
		}
	}
	p, _ := playbooks.Get(v.PlaybookID)
	instructions, plan := playbookBrief(p, v.Inputs)
	instructions += "\n\n" + automationBrief(v, trigger, events, total)
	name := v.Name + ", " + time.Now().UTC().Format("2 Jan 2006")
	if len(name) > 200 {
		name = name[:200]
	}
	goal := p.Summary
	mi := missionInput{Name: &name, Goal: &goal, Instructions: &instructions, Plan: &plan, Mode: &v.Mode,
		MaxSpendCents: optional[int64]{Set: runLimit != nil, Value: runLimit},
		playbookID:    &p.ID, playbookVersion: &p.Version}
	if v.Runner == runnerExternal {
		mi.TokenID = optional[string]{Set: true, Value: v.TokenID}
	}
	if err := mi.validate(); err != nil {
		return "", err
	}
	id, err := s.insertMission(ctx, tx, a, &mi)
	if err != nil {
		var inv errInvalid
		if errors.As(err, &inv) {
			return "", errHeld(string(inv))
		}
		return "", err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE missions SET automation_id = $2, status = 'running', approved_by = $3, approved_at = now(), started_at = now(), updated_at = now()
		WHERE id = $1`, id, v.ID, a.ActorID); err != nil {
		return "", err
	}
	switch v.Runner {
	case runnerBuiltin:
		if err := useBuiltinRunner(ctx, tx, a, id); err != nil {
			return "", err
		}
	case runnerExternal:
		if _, err := tx.Exec(ctx, `UPDATE missions SET runner = 'external', next_turn_at = NULL WHERE id = $1`, id); err != nil {
			return "", err
		}
	}
	body := "Started and approved by the automation " + v.Name + ", with the approval " + actorLabel(ctx, tx, a) + " gave it."
	if err := addUpdate(ctx, tx, id, ws, "", "system", "note", body); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE playbook_automations SET last_run_at = now(), last_mission_id = $2, runs = runs + 1, updated_at = now() WHERE id = $1`, v.ID, id); err != nil {
		return "", err
	}
	props := automationProps(v)
	props["trigger"], props["events"], props["mission_id"] = trigger, total, id
	data := map[string]any{"automation_id": v.ID, "playbook_id": p.ID, "playbook_version": p.Version, "mission_id": id, "mode": v.Mode, "trigger": trigger, "events": total}
	if err := recordWithProps(ctx, tx, &a.principal, ws, "playbook.started", "mission", id, data, props); err != nil {
		return "", err
	}
	if err := record(ctx, tx, &a.principal, ws, "mission.approved", "mission", id, map[string]any{"from": missions.StatusDraft, "to": missions.StatusRunning, "automation_id": v.ID}); err != nil {
		return "", err
	}
	return id, recordWithProps(ctx, tx, &a.principal, ws, "playbook_automation.ran", "playbook_automation", v.ID, data, props)
}

func automationWorkspace(ctx context.Context, tx pgx.Tx, id string) (string, error) {
	var ws string
	err := tx.QueryRow(ctx, `SELECT workspace_id::text FROM playbook_automations WHERE id = $1`, id).Scan(&ws)
	return ws, err
}

// automationBrief tells the agent why this run started and what to look
// at: only what is new since the last run, or the events listed.
func automationBrief(v automationView, trigger string, events []automationEvent, total int) string {
	var b strings.Builder
	switch trigger {
	case "schedule":
		fmt.Fprintf(&b, "This run was started by the automation %q, which runs %s.", v.Name, deref(v.Cadence))
	case "event":
		fmt.Fprintf(&b, "This run was started by the automation %q, for %d %s event(s):", v.Name, total, deref(v.EventType))
		for _, e := range events {
			fmt.Fprintf(&b, "\n- %s %s %s at %s (GET /v1/workspaces/{ws}/audit-events lists it as event %d)", e.Action, deref(e.TargetType), deref(e.TargetID), e.At.UTC().Format(time.RFC3339), e.ID)
		}
		if total > len(events) {
			fmt.Fprintf(&b, "\n- and %d more of the same kind", total-len(events))
		}
		b.WriteString("\nWork on these first.")
	default:
		fmt.Fprintf(&b, "This run of the automation %q was started by hand.", v.Name)
	}
	if v.LastRunAt != nil {
		fmt.Fprintf(&b, " The previous run was at %s: look only at what is new since then, and skip anything already in the workspace (prospects, pitches, fixes).", v.LastRunAt.UTC().Format(time.RFC3339))
	}
	b.WriteString(" When you finish, complete the mission with a short report; the next run starts by itself.")
	return b.String()
}

// RunAutomations starts playbook automations' runs as they fall due,
// until ctx ends.
func (s *Server) RunAutomations(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := s.RunDueAutomations(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.log, track.Event{Name: "playbook_automation.runner_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// RunDueAutomations looks at every automation that is due, one at a time,
// and reports how many runs it started.
func (s *Server) RunDueAutomations(ctx context.Context) (int, error) {
	var lag *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT min(next_run_at) FROM playbook_automations WHERE status = 'active' AND next_run_at <= now()`).Scan(&lag); err != nil {
		return 0, err
	}
	if lag != nil {
		metrics.AutomationLag(time.Since(*lag))
	} else {
		metrics.AutomationLag(0)
	}
	started := 0
	seen := map[string]bool{}
	for ctx.Err() == nil {
		n, id, err := s.runOneAutomation(ctx, seen)
		if err != nil {
			return started, err
		}
		if id == "" {
			break
		}
		seen[id] = true
		started += n
	}
	return started, nil
}

// runOneAutomation claims the most overdue automation not yet seen in this
// pass and runs what is due for it.
func (s *Server) runOneAutomation(ctx context.Context, seen map[string]bool) (started int, id string, err error) {
	skip := make([]string, 0, len(seen))
	for k := range seen {
		skip = append(skip, k)
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		v, err := scanAutomation(tx.QueryRow(ctx, `
			SELECT `+automationColumns+` FROM playbook_automations
			WHERE status = 'active' AND next_run_at <= now() AND NOT (id::text = ANY($1))
			ORDER BY next_run_at LIMIT 1 FOR UPDATE SKIP LOCKED`, skip))
		if errors.Is(err, errNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		id = v.ID
		started, err = s.dueAutomation(ctx, tx, v)
		return err
	})
	return started, id, err
}

// dueAutomation runs v's due work in tx: a scheduled run, or the runs its
// new events call for.
func (s *Server) dueAutomation(ctx context.Context, tx pgx.Tx, v automationView) (int, error) {
	now := time.Now()
	next := nextAutomationRun(v, now)
	fire := func(trigger string, events []automationEvent, total int) (bool, error) {
		sp, err := tx.Begin(ctx)
		if err != nil {
			return false, err
		}
		_, err = s.fireAutomation(ctx, sp, v, trigger, events, total)
		var held errHeld
		switch {
		case err == nil:
			metrics.AutomationRun(v.Kind, "started")
			return true, sp.Commit(ctx)
		case errors.As(err, &held):
			_ = sp.Rollback(ctx)
			metrics.AutomationRun(v.Kind, "held")
			return false, s.holdAutomation(ctx, tx, v, string(held))
		default:
			_ = sp.Rollback(ctx)
			metrics.AutomationRun(v.Kind, "failed")
			ws, _ := automationWorkspace(ctx, tx, v.ID)
			track.Fail(ctx, s.log, track.Event{Name: "playbook_automation.run_failed", ActorType: "system", Surface: "system", WorkspaceID: ws,
				Props: map[string]any{"automation_id": v.ID, "playbook_id": v.PlaybookID, "kind": v.Kind}}, "internal", err)
			// Try again soon, without losing the events.
			next = now.Add(automationRetry)
			return false, nil
		}
	}
	started := 0
	if v.Kind == "schedule" {
		ok, err := fire("schedule", nil, 0)
		if err != nil {
			return 0, err
		}
		if ok {
			started++
		}
		_, err = tx.Exec(ctx, `UPDATE playbook_automations SET next_run_at = $2 WHERE id = $1 AND status = 'active'`, v.ID, next)
		return started, err
	}
	ws, err := automationWorkspace(ctx, tx, v.ID)
	if err != nil {
		return 0, err
	}
	cursor := v.lastEventID
	if v.Gather == "each" {
		var today int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM missions WHERE automation_id = $1 AND created_at >= date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'`, v.ID).Scan(&today); err != nil {
			return 0, err
		}
		room := min(v.DailyCap-today, automationEachPerPass)
		events, total, _, err := automationEvents(ctx, tx, ws, v, max(room, 0))
		if err != nil {
			return 0, err
		}
		for _, e := range events {
			ok, err := fire("event", []automationEvent{e}, 1)
			if err != nil {
				return started, err
			}
			if !ok {
				break
			}
			started++
			cursor = e.ID
		}
		if total > started && today+started >= v.DailyCap {
			// Today's cap is reached: the rest wait for tomorrow.
			metrics.AutomationRun(v.Kind, "capped")
			y, m, d := now.UTC().Date()
			next = time.Date(y, m, d+1, 0, 0, 0, 0, time.UTC)
		}
	} else {
		events, total, last, err := automationEvents(ctx, tx, ws, v, automationBriefEvents)
		if err != nil {
			return 0, err
		}
		if total > 0 {
			ok, err := fire("event", events, total)
			if err != nil {
				return 0, err
			}
			if ok {
				started++
				cursor = last
			}
		}
	}
	_, err = tx.Exec(ctx, `UPDATE playbook_automations SET next_run_at = $2, last_event_id = $3 WHERE id = $1 AND status = 'active'`, v.ID, next, cursor)
	return started, err
}

// automationEvents reads the events after v's cursor that start it: the
// first limit of them, how many there are, and the last one's id.
func automationEvents(ctx context.Context, tx pgx.Tx, ws string, v automationView, limit int) ([]automationEvent, int, int64, error) {
	filter := `workspace_id = $1 AND action = $2 AND id > $3 AND ($4::uuid IS NULL OR brand_id = $4::uuid)`
	args := []any{ws, deref(v.EventType), v.lastEventID, v.BrandID}
	var total int
	var last int64
	if err := tx.QueryRow(ctx, `SELECT count(*), COALESCE(max(id), 0) FROM audit_events WHERE `+filter, args...).Scan(&total, &last); err != nil {
		return nil, 0, 0, err
	}
	if total == 0 || limit <= 0 {
		return nil, total, last, nil
	}
	rows, err := tx.Query(ctx, `SELECT id, action, target_type, target_id, created_at FROM audit_events WHERE `+filter+` ORDER BY id LIMIT `+fmt.Sprint(limit), args...)
	if err != nil {
		return nil, 0, 0, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (automationEvent, error) {
		var e automationEvent
		err := row.Scan(&e.ID, &e.Action, &e.TargetType, &e.TargetID, &e.At)
		return e, err
	})
	return out, total, last, err
}

// holdAutomation stops v until a person looks, saying why.
func (s *Server) holdAutomation(ctx context.Context, tx pgx.Tx, v automationView, reason string) error {
	if len(reason) > 500 {
		reason = reason[:500]
	}
	if reason != "" {
		// Shown as a sentence on its own.
		reason = strings.ToUpper(reason[:1]) + reason[1:]
	}
	ws, err := automationWorkspace(ctx, tx, v.ID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE playbook_automations SET status = 'held', held_reason = $2, next_run_at = NULL, updated_at = now() WHERE id = $1`, v.ID, reason); err != nil {
		return err
	}
	return recordWithProps(ctx, tx, nil, ws, "playbook_automation.held", "playbook_automation", v.ID,
		map[string]any{"playbook_id": v.PlaybookID, "name": v.Name, "reason": reason}, automationProps(v))
}
