package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/accounts"
	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/llm"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/research"
	"github.com/outflareai/outflare/engine/internal/secrets"
	"github.com/outflareai/outflare/engine/internal/track"
)

// JobTask runs one browser task. Only a process with a browser handles
// it (OUTFLARE_ROLE=browser, or any working role with Chrome on the host),
// so on an install without one, tasks wait in the queue and the
// outflare_browser_tasks_queued metric says so.
const JobTask = "browser.task"

// AITask is the AI usage task browser runs are booked under.
const AITask = "browser_tasks"

// Service runs queued tasks in jobs.
type Service struct {
	Pool   *pgxpool.Pool
	Box    *secrets.Box // the account vault
	Models research.Models
	Opener Opener
	Log    *slog.Logger
	// AllowPrivate lets tasks reach private addresses and plain http
	// (OUTFLARE_BROWSER_ALLOW_PRIVATE), for local development.
	AllowPrivate bool
	Now          func() time.Time
}

// Register handles browser tasks on q.
func (s *Service) Register(q *jobs.Queue) { q.Handle(JobTask, s.job) }

type payload struct {
	Task string `json:"task"`
}

// Enqueue queues a task, in the transaction that created or resumed it.
func Enqueue(ctx context.Context, q jobs.Querier, ws, taskID string) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobTask, WorkspaceID: &ws, Payload: payload{Task: taskID},
		IdempotencyKey: "browser-task:" + taskID + ":" + time.Now().UTC().Format(time.RFC3339), MaxAttempts: 2})
	return err
}

// Resume queues the tasks that waited on a takeover a person handed back,
// in the handback's transaction. It returns how many.
func Resume(ctx context.Context, tx pgx.Tx, takeoverID string) (int, error) {
	rows, err := tx.Query(ctx, `UPDATE browser_tasks SET status = 'queued', resumed = true, updated_at = now()
		WHERE takeover_id = $1 AND status = 'waiting_person' RETURNING id::text, workspace_id::text`, takeoverID)
	if err != nil {
		return 0, err
	}
	type row struct{ id, ws string }
	tasks, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		return x, r.Scan(&x.id, &x.ws)
	})
	if err != nil {
		return 0, err
	}
	for _, t := range tasks {
		if err := Enqueue(ctx, tx, t.ws, t.id); err != nil {
			return 0, err
		}
	}
	return len(tasks), nil
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// taskRow is a browser_tasks row as the job reads it.
type taskRow struct {
	Task
	Status  string
	Runs    int
	Account struct {
		Identity string
	}
}

func (s *Service) load(ctx context.Context, id string) (taskRow, error) {
	var t taskRow
	var account, content []byte
	var accountID *string
	err := s.Pool.QueryRow(ctx, `SELECT t.id::text, t.workspace_id::text, t.brand_id::text, t.account_id::text, t.goal, t.hosts, t.operations, t.content, t.max_steps, t.max_minutes, t.max_cents, t.status, t.runs, t.resumed,
			COALESCE((SELECT json_build_object('identity', a.identity)::text FROM brand_accounts a WHERE a.id = t.account_id), '{}')
		FROM browser_tasks t WHERE t.id = $1`, id).Scan(&t.ID, &t.WorkspaceID, &t.BrandID, &accountID, &t.Goal, &t.Hosts, &t.Operations, &content, &t.MaxSteps, &t.MaxMinutes, &t.MaxCents, &t.Status, &t.Runs, &t.Resumed, &account)
	if err != nil {
		return t, err
	}
	if accountID != nil {
		t.AccountID = *accountID
	}
	_ = json.Unmarshal(content, &t.Content)
	_ = json.Unmarshal(account, &t.Account)
	return t, nil
}

// job runs one task from the queue.
func (s *Service) job(ctx context.Context, job jobs.Job) error {
	var p payload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
	}
	return s.RunTask(ctx, p.Task)
}

// RunTask runs a queued task now (the job does this): one run, until the
// model finishes, asks for a person or hits a ceiling. A task that isn't
// queued is left alone.
func (s *Service) RunTask(ctx context.Context, taskID string) error {
	t, err := s.load(ctx, taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // deleted with its brand
	}
	if err != nil {
		return err
	}
	if t.Status != "queued" {
		return nil // cancelled, or already run by another worker
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE browser_tasks SET status = 'running', runs = runs + 1, started_at = COALESCE(started_at, now()), updated_at = now() WHERE id = $1 AND status = 'queued'`, t.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	run := t.Runs + 1
	out := s.run(ctx, t, run)
	cents := int64(0)
	if out.Usage.Model != "" {
		cents = int64(out.Usage.CostUSD * 100)
	}
	ev, _ := json.Marshal(out.Evidence)
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var takeover *string
		if out.TakeoverID != "" {
			takeover = &out.TakeoverID
		}
		finished := out.Status != StatusWaitingPerson
		if _, err := tx.Exec(ctx, `UPDATE browser_tasks SET status = $2, summary = $3, final_url = $4, evidence = $5, takeover_id = $6, steps = steps + $7, ai_cents = ai_cents + $8,
				finished_at = CASE WHEN $9 THEN now() ELSE NULL END, resumed = false, updated_at = now() WHERE id = $1`,
			t.ID, out.Status, out.Summary, out.FinalURL, ev, takeover, len(out.Steps), cents, finished); err != nil {
			return err
		}
		for _, st := range out.Steps {
			in, _ := json.Marshal(st.Input)
			if _, err := tx.Exec(ctx, `INSERT INTO browser_task_steps (task_id, workspace_id, brand_id, run, n, tool, input, result, at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
				t.ID, t.WorkspaceID, t.BrandID, run, st.N, st.Tool, in, st.Result, st.At); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: t.WorkspaceID, ActorType: "system", Action: "browser_task." + out.Status, TargetType: "browser_task", TargetID: t.ID,
			Data:  map[string]any{"brand_id": t.BrandID, "steps": len(out.Steps), "run": run, "summary": out.Summary, "final_url": out.FinalURL},
			Props: map[string]any{"brand_id": t.BrandID, "steps": len(out.Steps), "run": run, "ai_cents": cents}})
	})
	if err != nil {
		return err
	}
	metrics.BrowserTask(out.Status)
	return nil
}

// run opens the model and the browser and carries the task out.
func (s *Service) run(ctx context.Context, t taskRow, run int) (out Outcome) {
	fail := func(code, msg string, err error) Outcome {
		track.Fail(ctx, s.Log, track.Event{Name: "browser_task.failed", WorkspaceID: t.WorkspaceID, ActorType: "system", Surface: "job",
			Props: map[string]any{"brand_id": t.BrandID, "task_id": t.ID, "run": run}}, code, err)
		return Outcome{Status: StatusFailed, Summary: msg, Evidence: map[string]any{"error_code": code}}
	}
	if s.Models == nil {
		return fail("no_ai", "This server has no AI for browser tasks.", errors.New("no models"))
	}
	choice, err := s.Models.ForTask(ctx, t.WorkspaceID, AITask, t.MaxCents)
	if err != nil {
		return fail("no_ai", "The workspace's AI isn't available: "+llm.Scrub(err.Error()), err)
	}
	if s.Opener == nil {
		return fail("no_browser", "This worker has no browser.", errors.New("no opener"))
	}
	drv, err := s.Opener.Open(ctx, Boundary{Hosts: t.Hosts, AllowPrivate: s.AllowPrivate})
	if err != nil {
		return fail("browser_failed", "The browser could not start: "+err.Error(), err)
	}
	defer drv.Close()
	deps := Deps{Driver: drv, Model: choice.Model, Price: choice.Price, Now: s.now,
		Cancelled: func(ctx context.Context) bool {
			var c bool
			_ = s.Pool.QueryRow(ctx, `SELECT cancel_requested FROM browser_tasks WHERE id = $1`, t.ID).Scan(&c)
			return c
		}}
	if t.AccountID != "" {
		deps.Vault = vaultFor{s: s, t: t}
		deps.Accounts = accountsFor{s: s, t: t}
	}
	if t.Account.Identity != "" {
		t.Goal += "\n\naccount_identity: " + t.Account.Identity
	}
	out = Run(ctx, t.Task, deps)
	if err := s.Models.Spent(ctx, t.WorkspaceID, choice, out.Usage, "browser_task:"+t.ID, "Ran a browser task"); err != nil {
		s.Log.Warn("browser task: AI spend not booked", "err", err)
	}
	for _, st := range out.Steps {
		metrics.BrowserStep(st.Tool)
	}
	return out
}

type vaultFor struct {
	s *Service
	t taskRow
}

func (v vaultFor) Open(ctx context.Context, kind string) (string, error) {
	var out string
	err := pgx.BeginFunc(ctx, v.s.Pool, func(tx pgx.Tx) (err error) {
		out, err = accounts.Vault{Box: v.s.Box}.Open(ctx, tx, v.t.WorkspaceID, v.t.BrandID, v.t.AccountID, kind)
		return err
	})
	return out, err
}

type accountsFor struct {
	s *Service
	t taskRow
}

func (a accountsFor) Record(ctx context.Context, purpose, outcome string, evidence map[string]any) error {
	err := pgx.BeginFunc(ctx, a.s.Pool, func(tx pgx.Tx) error {
		attemptID, incidentID, err := accounts.Record(ctx, tx, a.t.WorkspaceID, a.t.BrandID, a.t.AccountID, accounts.Attempt{Purpose: purpose, Outcome: outcome, ActorType: "system", ActorID: "browser.task", Evidence: evidence})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: a.t.WorkspaceID, ActorType: "system", Action: "account.attempt_recorded", TargetType: "account", TargetID: a.t.AccountID,
			Data:  map[string]any{"brand_id": a.t.BrandID, "purpose": purpose, "outcome": outcome, "attempt_id": attemptID, "incident_id": incidentID, "task_id": a.t.ID},
			Props: map[string]any{"brand_id": a.t.BrandID, "purpose": purpose, "outcome": outcome, "incident": incidentID != ""}})
	})
	if err == nil {
		metrics.AccountAttempt(purpose, outcome)
	}
	return err
}

// NeedPerson opens a takeover request on the task's account, or joins
// the one already waiting or under way.
func (a accountsFor) NeedPerson(ctx context.Context, reason, note string) (string, error) {
	var id string
	err := pgx.BeginFunc(ctx, a.s.Pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT id::text FROM brand_account_takeovers WHERE account_id = $1 AND status IN ('requested', 'active') LIMIT 1`, a.t.AccountID).Scan(&id)
		if err == nil {
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO brand_account_takeovers (workspace_id, brand_id, account_id, reason, note)
			SELECT workspace_id, brand_id, id, $4, $5 FROM brand_accounts WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 AND status <> 'archived' RETURNING id::text`,
			a.t.AccountID, a.t.WorkspaceID, a.t.BrandID, reason, note).Scan(&id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errors.New("the account is archived")
			}
			return err
		}
		props := map[string]any{"brand_id": a.t.BrandID, "account_id": a.t.AccountID, "reason": reason, "task_id": a.t.ID}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: a.t.WorkspaceID, ActorType: "system", Action: "account.takeover_requested", TargetType: "account_takeover", TargetID: id, Data: props, Props: props})
	})
	if err == nil {
		metrics.AccountTakeover("requested")
	}
	return id, err
}
