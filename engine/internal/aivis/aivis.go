// Package aivis measures a brand's AI visibility: what AI models answer
// when people ask them the questions the brand wants to be the answer to.
//
// A cohort is a set of prompts about one brand. A run asks each prompt to
// every model the workspace picked, a set number of times, and keeps each
// answer as a sample. From the samples Outflare counts which brands each
// answer names and in what order (mentions), which pages it cites
// (citations), and so the brand's share of voice against its competitors.
// A prompt whose answers never name the brand goes on the workspace's
// fixes list.
//
// Everything runs on the workspace's own accounts, because Outflare never
// supplies its API keys for AI (AGENTS.md, "Bring your own keys"): each
// model is one of the workspace's own AI connections, or an assistant's own
// screen read through its own answer reader (DataForSEO), and what a run costs
// is booked against the workspace's monthly AI limit and the sampling
// limit in its AI visibility settings. Answers are external content: they
// are measured, never followed.
package aivis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/llm"
	"github.com/outflareai/outflare/engine/internal/retry"
	"github.com/outflareai/outflare/engine/internal/track"
)

// JobStep runs one step of a run: a batch of samples.
const JobStep = "ai_visibility.step"

// JobBenchmark counts a cohort's brand and competitors in the AI answers a
// demand reader keeps; the engine's API package runs it.
const JobBenchmark = "ai_visibility.benchmark"

// Task is the AI model task for Outflare's own reading of answers (such as
// checking them against a brand's facts), as opposed to the sampling,
// which asks each model the workspace picked.
const Task = "ai_visibility"

const (
	// What one sample is assumed to cost before it runs, so a workspace at
	// its limit never starts a call it cannot pay for. Searching the web
	// costs more.
	estimateCents          = 2
	estimateWebSearchCents = 5
	// A screen answer costs a fraction of a cent; a whole cent keeps the
	// check simple and safe.
	estimateScreenCents = 1
	// An answer is a few paragraphs, as a person would read it.
	maxAnswerTokens = 2000
	// One sample gives up after this long.
	sampleTimeout = 2 * time.Minute
	// A step works this long, then queues the next, so one long run never
	// holds a worker.
	stepFor = 90 * time.Second
	// screensAtOnce is how many screen answers a step asks at once.
	screensAtOnce = 5
)

// MaxModels is how many models a workspace may sample at once.
const MaxModels = 8

// Target is one model a run asks: one of the workspace's AI model
// connections, the model, and whether it may search the web first.
type Target struct {
	IntegrationID string `json:"integration_id"`
	// The connection's provider (claude, openai, gemini, mistral,
	// openrouter, or dataforseo for a screen), set when the settings are
	// saved.
	Provider string `json:"provider"`
	// Surface is api (the model's API, the default) or screen (the
	// assistant's own screen, read by an answer reader). For a screen,
	// Model is the assistant: chatgpt or gemini.
	Surface   string `json:"surface,omitempty"`
	Model     string `json:"model"`
	WebSearch bool   `json:"web_search"`
	// What the model costs, when Outflare does not know and the provider
	// does not say (US cents per million tokens).
	InputCents  *float64 `json:"input_cents_per_million,omitempty"`
	OutputCents *float64 `json:"output_cents_per_million,omitempty"`
}

// Choice is a model opened with the workspace's own key and allowed to
// spend.
type Choice struct {
	Provider      llm.Provider
	IntegrationID string
	Model         llm.Model
	Price         llm.Price
	// Task is the tool the model was opened for, as the AI usage log
	// names it.
	Task string
}

// Models opens the workspace's own AI models and books what they spend.
// The engine's API server implements it, so the workspace's monthly AI
// limit is checked in one place.
type Models interface {
	// Open opens one of the workspace's AI model connections with a model,
	// refusing when it is not connected, its price is unknown, or the
	// month's AI limit would not cover about cents more.
	Open(ctx context.Context, ws string, t Target, cents int64) (Choice, error)
	// ForTask opens the model the workspace chose for a task.
	ForTask(ctx context.Context, ws, task string, cents int64) (Choice, error)
	// Spent books what a call cost on the workspace's own provider bill
	// and returns the cents.
	Spent(ctx context.Context, ws string, c Choice, u llm.Usage, ref, note string) (int64, error)
}

// Surfaces a target asks on.
const (
	SurfaceAPI    = "api"
	SurfaceScreen = "screen"
)

// IsScreen says whether the target reads the assistant's own screen.
func (t Target) IsScreen() bool { return t.Surface == SurfaceScreen }

// Screens asks AI assistants on their own screens (ChatGPT, Gemini) as a
// signed-out person in a country, through one of the workspace's answer
// reader connections (DataForSEO), paid from that connection's budget. The
// engine's API server implements it.
type Screens interface {
	// Ask asks the target's assistant one question and books what it cost
	// under ref, as Models.Spent does. It returns Refused when the
	// connection is gone or its budget would not cover the answer.
	Ask(ctx context.Context, ws string, t Target, q connectors.AnswerQuery, ref string) (connectors.AnswerResult, error)
}

// Refused is a reason a model can't be used, with an error code from the
// API's catalog (ai_budget_reached, ai_model_needed, connector_needed).
type Refused struct{ Code, Message string }

func (e Refused) Error() string { return e.Message }

// Service runs AI visibility runs in the background.
type Service struct {
	Pool *pgxpool.Pool
	// Models is the workspace's AI models; nil leaves runs failing with
	// ai_model_needed.
	Models Models
	// Screens reads assistants' own screens; nil leaves screen targets
	// failing with connector_needed.
	Screens Screens
	Log     *slog.Logger
	// Now is the clock, for tests.
	Now func() time.Time
}

// Register handles run jobs and finding competitors' websites.
func (s *Service) Register(q *jobs.Queue) {
	q.Handle(JobStep, s.job)
	q.Handle(JobWebsites, s.websitesJob)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Errors StartRun returns.
var (
	ErrRunning   = errors.New("a run of this cohort is already going")
	ErrNoModels  = errors.New("pick the models to ask under SEO → AI visibility → Settings first")
	ErrNoPrompts = errors.New("the cohort has no active prompts")
)

// Settings are a brand's AI visibility settings.
type Settings struct {
	Models           []Target   `json:"models"`
	SamplesPerPrompt int        `json:"samples_per_prompt"`
	MonthlyLimit     int64      `json:"monthly_limit_cents"`
	UpdatedAt        *time.Time `json:"updated_at"`
}

// DefaultLimitCents is the sampling limit of a brand that never set one:
// US$10 a month.
const DefaultLimitCents = 1000

// brandSQL is the brand a query is about: $n, or the workspace's first
// brand when it is empty.
func brandSQL(ws, brand string) string {
	return "COALESCE(NULLIF(" + brand + ", '')::uuid, outflare_first_brand(" + ws + "))"
}

// LoadSettings reads a brand's settings (the first brand's when brand is
// empty), or the defaults.
func LoadSettings(ctx context.Context, q jobs.Querier, ws, brand string) (Settings, error) {
	set := Settings{Models: []Target{}, SamplesPerPrompt: 1, MonthlyLimit: DefaultLimitCents}
	var raw []byte
	err := q.QueryRow(ctx, `SELECT models, samples_per_prompt, monthly_limit_cents, updated_at FROM ai_visibility_settings
		WHERE workspace_id = $1 AND brand_id = `+brandSQL("$1", "$2"), ws, brand).
		Scan(&raw, &set.SamplesPerPrompt, &set.MonthlyLimit, &set.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return set, nil
	}
	if err != nil {
		return set, err
	}
	return set, json.Unmarshal(raw, &set.Models)
}

// MonthSpent is what sampling and checking answers against the facts
// cost the brand (the first brand when brand is empty) this calendar
// month (UTC), in cents: screen answers, kept in millionths of a dollar,
// round up.
func MonthSpent(ctx context.Context, q jobs.Querier, ws, brand string, now time.Time) (int64, error) {
	var cents, micros int64
	start := time.Date(now.UTC().Year(), now.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	err := q.QueryRow(ctx, `SELECT COALESCE(sum(cost_cents + facts_cost_cents + stance_cost_cents), 0), COALESCE(sum(cost_micros), 0) FROM ai_samples
		WHERE workspace_id = $1 AND sampled_at >= $2 AND brand_id = `+brandSQL("$1", "$3"), ws, start, brand).Scan(&cents, &micros)
	return cents + (micros+9_999)/10_000, err
}

// StartRun queues a run of a cohort: every active prompt, asked to every
// model in the settings, samples_per_prompt times. trigger is manual or
// schedule.
func StartRun(ctx context.Context, tx pgx.Tx, ws, cohortID, trigger string, by *string) (string, error) {
	var brand string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(brand_id::text, '') FROM ai_cohorts WHERE workspace_id = $1 AND id = $2`, ws, cohortID).Scan(&brand); err != nil {
		return "", err
	}
	set, err := LoadSettings(ctx, tx, ws, brand)
	if err != nil {
		return "", err
	}
	if len(set.Models) == 0 {
		return "", ErrNoModels
	}
	var prompts int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM ai_prompts WHERE workspace_id = $1 AND cohort_id = $2 AND status = 'active'`, ws, cohortID).Scan(&prompts); err != nil {
		return "", err
	}
	if prompts == 0 {
		return "", ErrNoPrompts
	}
	models, _ := json.Marshal(set.Models)
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO ai_runs (workspace_id, cohort_id, trigger, models, attempts, samples_planned, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		ws, cohortID, trigger, models, set.SamplesPerPrompt, prompts*len(set.Models)*set.SamplesPerPrompt, by).Scan(&id)
	if isUnique(err) {
		return "", ErrRunning
	}
	if err != nil {
		return "", err
	}
	_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobStep, WorkspaceID: &ws, Payload: stepPayload{RunID: id, Step: 0},
		IdempotencyKey: fmt.Sprintf("%s:0", id), MaxAttempts: 5})
	return id, err
}

// StopRun stops a queued or running run; samples taken stay.
func StopRun(ctx context.Context, tx pgx.Tx, ws, runID string) (bool, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE ai_runs SET status = 'stopped', reason = 'stopped', step = step + 1, finished_at = now()
		WHERE id = $1 AND workspace_id = $2 AND status IN ('queued', 'running')`, runID, ws)
	return tag.RowsAffected() == 1, err
}

type stepPayload struct {
	RunID string `json:"run_id"`
	Step  int    `json:"step"`
}

func (s *Service) job(ctx context.Context, j jobs.Job) error {
	var p stepPayload
	if err := json.Unmarshal(j.Payload, &p); err != nil || j.WorkspaceID == nil || p.RunID == "" {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	err := s.Step(ctx, *j.WorkspaceID, p.RunID, p.Step)
	if err != nil && j.Attempt >= j.MaxAttempts {
		// The last try: end the run rather than leave it running forever.
		_ = s.finish(context.WithoutCancel(ctx), *j.WorkspaceID, p.RunID, "failed", "internal", "internal")
	}
	return err
}

// Run starts scheduled runs until ctx ends: daily and weekly cohorts whose
// last run is that old. Once a day it also queues finding the websites of
// brands the answers name.
func (s *Service) Run(ctx context.Context) {
	websitesDay := "" // the day finding websites was last queued
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := s.Schedule(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.Log, track.Event{Name: "ai_run.schedule_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		if day := s.now().UTC().Format(time.DateOnly); day != websitesDay {
			if err := s.scheduleWebsites(ctx); err != nil {
				if ctx.Err() == nil {
					track.Fail(ctx, s.Log, track.Event{Name: "ai_visibility.websites_schedule_failed", ActorType: "system", Surface: "system"}, "internal", err)
				}
			} else {
				websitesDay = day
			}
		}
	}
}

// Schedule starts the runs that are due now.
func (s *Service) Schedule(ctx context.Context) error {
	// A run that failed, or got no answer at all, doesn't hold the schedule
	// for its whole period: it is tried again after retry.After the failed
	// runs since the last that worked, so the day still gets its answers.
	// A run a person stopped, or one the AI budget stopped, still holds it.
	rows, err := s.Pool.Query(ctx, `
		SELECT c.id, c.workspace_id FROM ai_cohorts c
		JOIN ai_visibility_settings v ON v.workspace_id = c.workspace_id AND jsonb_array_length(v.models) > 0
			AND v.brand_id = COALESCE(c.brand_id, outflare_first_brand(c.workspace_id))
		LEFT JOIN LATERAL (SELECT count(*) AS n, max(r.finished_at) AS at FROM ai_runs r WHERE r.cohort_id = c.id
			AND (r.status = 'failed' OR (r.status = 'done' AND r.samples_done = 0 AND r.samples_failed > 0))
			AND r.created_at > COALESCE((SELECT max(g.created_at) FROM ai_runs g WHERE g.cohort_id = c.id
				AND NOT (g.status = 'failed' OR (g.status = 'done' AND g.samples_done = 0 AND g.samples_failed > 0))), '-infinity')) f ON true
		WHERE c.schedule <> 'off' AND c.status = 'active'
		  AND EXISTS (SELECT 1 FROM ai_prompts p WHERE p.cohort_id = c.id AND p.status = 'active')
		  AND NOT EXISTS (SELECT 1 FROM ai_runs r WHERE r.cohort_id = c.id
		      AND (r.status IN ('queued', 'running')
		           OR (NOT (r.status = 'failed' OR (r.status = 'done' AND r.samples_done = 0 AND r.samples_failed > 0))
		               AND r.created_at > now() - CASE c.schedule WHEN 'daily' THEN interval '23 hours' ELSE interval '167 hours' END)))
		  AND (f.n = 0 OR f.at IS NULL OR f.at < now() - `+retry.SQL("f.n")+`)
		ORDER BY c.created_at LIMIT 100`)
	if err != nil {
		return err
	}
	type due struct{ id, ws string }
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (due, error) {
		var d due
		return d, row.Scan(&d.id, &d.ws)
	})
	if err != nil {
		return err
	}
	for _, d := range list {
		err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			id, err := StartRun(ctx, tx, d.ws, d.id, "schedule", nil)
			if errors.Is(err, ErrRunning) || errors.Is(err, ErrNoModels) || errors.Is(err, ErrNoPrompts) {
				return nil
			}
			if err != nil {
				return err
			}
			return audit.Record(ctx, tx, audit.Event{WorkspaceID: d.ws, ActorType: "system", Action: "ai_run.started",
				TargetType: "ai_run", TargetID: id, Data: map[string]any{"cohort_id": d.id, "trigger": "schedule"}})
		})
		if err != nil {
			track.Fail(ctx, s.Log, track.Event{Name: "ai_run.schedule_failed", WorkspaceID: d.ws, ActorType: "system", Surface: "system",
				Props: map[string]any{"cohort_id": d.id}}, "internal", err)
		}
	}
	return nil
}

func isUnique(err error) bool {
	var pg interface{ SQLState() string }
	return errors.As(err, &pg) && pg.SQLState() == "23505"
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
