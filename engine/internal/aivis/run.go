package aivis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/llm"
	"github.com/outflareai/outflare/engine/internal/track"
)

// system is all a sampled model is told: answer as for anyone, so the
// answer is the one a customer would get.
const system = "You are a helpful assistant. Answer the user's question as you would for anyone who asked it."

// cohortQuerier is a pool or a transaction.
type cohortQuerier interface {
	jobs.Querier
	rowsQuerier
}

// Cohort is what a run needs to know about its cohort.
type Cohort struct {
	ID          string
	BrandID     *string
	Locale      string
	Brand       Entity
	Competitors []Entity
}

// LoadCohort reads a cohort with its brand (or, for no brand, the
// company) and competitors.
func LoadCohort(ctx context.Context, q cohortQuerier, ws, id string) (Cohort, error) {
	c := Cohort{ID: id}
	var aliases []string
	var comps []byte
	var name, website *string
	var domains []string
	err := q.QueryRow(ctx, `
		SELECT c.brand_id::text, c.locale, c.aliases, c.competitors,
		       COALESCE(b.name, x.company), COALESCE(b.website, x.website), COALESCE(b.domains, '{}')
		FROM ai_cohorts c
		LEFT JOIN brands b ON b.id = c.brand_id AND b.workspace_id = c.workspace_id
		LEFT JOIN workspace_context x ON x.workspace_id = c.workspace_id
		WHERE c.id = $1 AND c.workspace_id = $2`, id, ws).
		Scan(&c.BrandID, &c.Locale, &aliases, &comps, &name, &website, &domains)
	if err != nil {
		return c, err
	}
	if name != nil {
		c.Brand.Name = *name
	}
	c.Brand.Aliases = aliases
	if website != nil && *website != "" {
		domains = append(domains, Host(*website))
	}
	c.Brand.Domains = dedupe(domains)
	if err := json.Unmarshal(comps, &c.Competitors); err != nil {
		return c, err
	}
	tracked, err := SEOCompetitors(ctx, q, ws, c.BrandID)
	if err != nil {
		return c, err
	}
	c.Competitors = MergeCompetitors(c.Competitors, tracked)
	return c, nil
}

func dedupe(list []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, v := range list {
		if v = strings.TrimSpace(v); v != "" && !seen[strings.ToLower(v)] {
			seen[strings.ToLower(v)] = true
			out = append(out, v)
		}
	}
	return out
}

type runRow struct {
	status   string
	step     int
	cohortID string
	models   []Target
	attempts int
}

type pending struct {
	promptID, text string
	target         Target
	attempt        int
}

// Step takes the next batch of a run's samples, then queues the next step
// or finishes the run.
func (s *Service) Step(ctx context.Context, ws, runID string, step int) error {
	var r runRow
	var models []byte
	err := s.Pool.QueryRow(ctx, `SELECT status, step, cohort_id, models, attempts FROM ai_runs WHERE id = $1 AND workspace_id = $2`, runID, ws).
		Scan(&r.status, &r.step, &r.cohortID, &models, &r.attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if (r.status != "queued" && r.status != "running") || r.step != step {
		return nil // stopped, finished, or a stale copy of the job
	}
	if err := json.Unmarshal(models, &r.models); err != nil {
		return err
	}
	if r.status == "queued" {
		if _, err := s.Pool.Exec(ctx, `UPDATE ai_runs SET status = 'running', started_at = now() WHERE id = $1 AND status = 'queued'`, runID); err != nil {
			return err
		}
	}
	cohort, err := LoadCohort(ctx, s.Pool, ws, r.cohortID)
	if err != nil {
		return err
	}
	todo, err := s.todo(ctx, ws, runID, r)
	if err != nil {
		return err
	}
	set, err := LoadSettings(ctx, s.Pool, ws)
	if err != nil {
		return err
	}
	until := s.now().Add(stepFor)
	for len(todo) > 0 && s.now().Before(until) {
		p := todo[0]
		todo = todo[1:]
		stop, err := s.sample(ctx, ws, runID, cohort, set, p)
		if err != nil {
			return err
		}
		if stop != "" {
			return s.finish(ctx, ws, runID, "stopped", stop, stop)
		}
		// Someone may have stopped the run meanwhile.
		var status string
		if err := s.Pool.QueryRow(ctx, `SELECT status FROM ai_runs WHERE id = $1`, runID).Scan(&status); err != nil {
			return err
		}
		if status != "running" {
			return nil
		}
	}
	more := len(todo) > 0
	if !more {
		// Every answer is in: check the ones naming the brand against its facts.
		var stop string
		more, stop, err = s.checkFacts(ctx, ws, runID, cohort, set, until)
		if err != nil {
			return err
		}
		if stop != "" {
			return s.finish(ctx, ws, runID, "stopped", stop, stop)
		}
	}
	if more {
		return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `UPDATE ai_runs SET step = step + 1 WHERE id = $1 AND step = $2 AND status = 'running'`, runID, step)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobStep, WorkspaceID: &ws, Payload: stepPayload{RunID: runID, Step: step + 1},
				IdempotencyKey: fmt.Sprintf("%s:%d", runID, step+1), MaxAttempts: 5})
			return err
		})
	}
	return s.finish(ctx, ws, runID, "done", "", "")
}

// todo lists the run's samples not taken yet, prompt by prompt.
func (s *Service) todo(ctx context.Context, ws, runID string, r runRow) ([]pending, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, text FROM ai_prompts WHERE workspace_id = $1 AND cohort_id = $2 AND status = 'active' ORDER BY created_at, id`, ws, r.cohortID)
	if err != nil {
		return nil, err
	}
	type prompt struct{ id, text string }
	prompts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (prompt, error) {
		var p prompt
		return p, row.Scan(&p.id, &p.text)
	})
	if err != nil {
		return nil, err
	}
	rows, err = s.Pool.Query(ctx, `SELECT prompt_id, integration_id::text, model, web_search, attempt FROM ai_samples WHERE run_id = $1`, runID)
	if err != nil {
		return nil, err
	}
	done := map[string]bool{}
	var pid, model string
	var integ *string
	var web bool
	var attempt int
	if _, err := pgx.ForEachRow(rows, []any{&pid, &integ, &model, &web, &attempt}, func() error {
		i := ""
		if integ != nil {
			i = *integ
		}
		done[fmt.Sprint(pid, i, model, web, attempt)] = true
		return nil
	}); err != nil {
		return nil, err
	}
	var out []pending
	for _, p := range prompts {
		for a := 1; a <= r.attempts; a++ {
			for _, t := range r.models {
				if !done[fmt.Sprint(p.id, t.IntegrationID, t.Model, t.WebSearch, a)] {
					out = append(out, pending{promptID: p.id, text: p.text, target: t, attempt: a})
				}
			}
		}
	}
	return out, nil
}

// sample asks one model one prompt and stores the answer. It returns the
// reason to stop the whole run (ai_budget_reached), or "" to go on.
func (s *Service) sample(ctx context.Context, ws, runID string, c Cohort, set Settings, p pending) (string, error) {
	estimate := int64(estimateCents)
	if p.target.WebSearch {
		estimate = estimateWebSearchCents
	}
	spent, err := MonthSpent(ctx, s.Pool, ws, s.now())
	if err != nil {
		return "", err
	}
	if spent+estimate > set.MonthlyLimit {
		return "sampling_limit_reached", nil
	}
	row := sampleRow{runID: runID, cohort: c, p: p, provider: p.target.Provider, status: "failed"}
	if s.Models == nil {
		row.errorCode = "ai_model_needed"
		return "", s.store(ctx, ws, row)
	}
	choice, err := s.Models.Open(ctx, ws, p.target, estimate)
	var refused Refused
	switch {
	case errors.As(err, &refused) && refused.Code == "ai_budget_reached":
		return "ai_budget_reached", nil
	case errors.As(err, &refused):
		row.errorCode = refused.Code
		s.emitFailed(ctx, ws, runID, row)
		return "", s.store(ctx, ws, row)
	case err != nil:
		row.errorCode = codeOf(err)
		s.emitFailed(ctx, ws, runID, row)
		return "", s.store(ctx, ws, row)
	}
	row.provider = choice.Provider.ID
	text := p.text
	if c.Locale != "" {
		text = "(I am in " + c.Locale + ".) " + text
	}
	cctx, cancel := context.WithTimeout(ctx, sampleTimeout)
	started := s.now()
	res, err := choice.Model.Chat(cctx, llm.Request{System: system, MaxTokens: maxAnswerTokens, WebSearch: p.target.WebSearch,
		Messages: []llm.Message{{Role: "user", Text: text}}})
	cancel()
	row.duration = s.now().Sub(started)
	row.usage = res.Usage
	if res.Usage.InputTokens+res.Usage.OutputTokens > 0 || res.Usage.CostUSD > 0 {
		cents, serr := s.Models.Spent(ctx, ws, choice, res.Usage, "ai_sample:"+runID+":"+fmt.Sprint(p.promptID, ":", choice.IntegrationID, ":", p.target.Model, ":", p.target.WebSearch, ":", p.attempt), "AI visibility sample")
		if serr != nil {
			return "", serr
		}
		row.cents = cents
	}
	switch {
	case err != nil:
		row.errorCode = codeOf(err)
		s.emitFailed(ctx, ws, runID, row)
	case res.Stop == llm.StopRefusal:
		row.status, row.errorCode = "refused", "refused"
	default:
		row.status, row.answer = "ok", res.Message.Text
		row.mentions = Mentions(row.answer, c.Brand, c.Competitors)
		row.citations = Citations(row.answer, res.Citations, c.Brand, c.Competitors)
	}
	return "", s.store(ctx, ws, row)
}

// codeOf names what went wrong with a model call, for the sample and its
// event; the error's text stays in the log.
func codeOf(err error) string {
	switch {
	case errors.Is(err, llm.ErrAuth):
		return "provider_auth"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	return "provider_error"
}

func (s *Service) emitFailed(ctx context.Context, ws, runID string, r sampleRow) {
	track.Emit(ctx, track.Event{Name: "ai_sample.failed", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Outcome: track.Error, ErrorCode: r.errorCode,
		Props: map[string]any{"run_id": runID, "cohort_id": r.cohort.ID, "provider": r.provider, "web_search": r.p.target.WebSearch}})
}

type sampleRow struct {
	runID     string
	cohort    Cohort
	p         pending
	provider  string
	status    string
	errorCode string
	answer    string
	usage     llm.Usage
	cents     int64
	duration  time.Duration
	mentions  []Mention
	citations []Citation
}

// maxStoredAnswer keeps a hostile or runaway answer from filling the table.
const maxStoredAnswer = 40_000

func (s *Service) store(ctx context.Context, ws string, r sampleRow) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var integ *string
		if r.p.target.IntegrationID != "" {
			integ = &r.p.target.IntegrationID
		}
		var id string
		var at time.Time
		err := tx.QueryRow(ctx, `
			INSERT INTO ai_samples (workspace_id, run_id, cohort_id, prompt_id, brand_id, provider, integration_id, model, web_search, attempt,
				status, error_code, answer, input_tokens, output_tokens, cost_cents, duration_ms)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
			ON CONFLICT (run_id, prompt_id, provider, model, web_search, attempt) DO NOTHING
			RETURNING id, sampled_at`,
			ws, r.runID, r.cohort.ID, r.p.promptID, r.cohort.BrandID, r.provider, integ, r.p.target.Model, r.p.target.WebSearch, r.p.attempt,
			r.status, r.errorCode, clip(r.answer, maxStoredAnswer), r.usage.InputTokens+r.usage.CacheRead+r.usage.CacheWrite, r.usage.OutputTokens,
			r.cents, r.duration.Milliseconds()).Scan(&id, &at)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // a retried step took it already
		}
		if err != nil {
			return err
		}
		for _, m := range r.mentions {
			if _, err := tx.Exec(ctx, `INSERT INTO ai_mentions (sample_id, workspace_id, cohort_id, entity, name, position, count, sampled_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT DO NOTHING`, id, ws, r.cohort.ID, m.Entity, clip(m.Name, 200), m.Position, m.Count, at); err != nil {
				return err
			}
		}
		for _, c := range r.citations {
			if _, err := tx.Exec(ctx, `INSERT INTO ai_citations (sample_id, workspace_id, cohort_id, url, host, title, position, owner, competitor, sampled_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) ON CONFLICT DO NOTHING`, id, ws, r.cohort.ID, c.URL, c.Host, c.Title, c.Position, c.Owner, clip(c.Competitor, 200), at); err != nil {
				return err
			}
		}
		ok, failed := 0, 0
		if r.status == "failed" {
			failed = 1
		} else {
			ok = 1
		}
		_, err = tx.Exec(ctx, `UPDATE ai_runs SET samples_done = samples_done + $2, samples_failed = samples_failed + $3, cost_cents = cost_cents + $4 WHERE id = $1`,
			r.runID, ok, failed, r.cents)
		return err
	})
}

// finish ends a run: done, stopped (with why) or failed, and for a run
// that finished, updates the fixes list from its answers.
func (s *Service) finish(ctx context.Context, ws, runID, status, reason, code string) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var cohortID string
		var planned, done, failed int
		var cost int64
		err := tx.QueryRow(ctx, `
			UPDATE ai_runs SET status = $3, reason = $4, error_code = $5, step = step + 1, finished_at = now()
			WHERE id = $1 AND workspace_id = $2 AND status IN ('queued', 'running')
			RETURNING cohort_id, samples_planned, samples_done, samples_failed, cost_cents`, runID, ws, status, reason, code).
			Scan(&cohortID, &planned, &done, &failed, &cost)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		data := map[string]any{"cohort_id": cohortID, "status": status, "samples_planned": planned, "samples_done": done,
			"samples_failed": failed, "cost_cents": cost}
		if reason != "" {
			data["reason"] = reason
		}
		if status == "done" {
			ch, err := syncFixes(ctx, tx, ws, cohortID, runID)
			if err != nil {
				return err
			}
			fc, err := syncFactFixes(ctx, tx, ws, runID)
			if err != nil {
				return err
			}
			ch.New, ch.Reopened, ch.Confirmed = ch.New+fc.New, ch.Reopened+fc.Reopened, ch.Confirmed+fc.Confirmed
			data["fixes_new"], data["fixes_reopened"], data["fixes_confirmed"] = ch.New, ch.Reopened, ch.Confirmed
		}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "ai_run.finished",
			TargetType: "ai_run", TargetID: runID, Data: data})
	})
}
