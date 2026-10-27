package aivis

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/crawl"
)

// FixSource is the fixes list's source for AI visibility.
const FixSource = "ai_visibility"

// Rules are what AI visibility puts on the fixes list. Ids are a promise:
// never rename or remove one, add new ones.
var Rules = []crawl.Rule{
	{ID: "ai_brand_absent", Severity: crawl.SevWarning, Category: "ai_visibility", Title: "AI answers leave the brand out",
		Why: "When people ask AI models this question, none of the answers name the brand, so its buyers hear only about others.",
		Fix: "Publish a page that answers this exact question plainly, with the brand's name, what it offers and proof (prices, numbers, reviews), and earn mentions on the pages AI answers cite for it (the sample's citations list them)."},
	{ID: "ai_fact_wrong", Severity: crawl.SevError, Category: "ai_visibility", Title: "AI answers get a fact about the brand wrong",
		Why: "AI answers tell buyers something about the brand that contradicts what the brand says is true, such as an old price or a feature it has, so buyers decide on wrong information.",
		Fix: "State the fact plainly on the brand's own pages (pricing, features, about) where models read it, correct it on the third-party pages the answers cite, and keep old pages and listings up to date. The fact checks show the wrong words each answer used."},
}

var ruleByID = func() map[string]crawl.Rule {
	m := map[string]crawl.Rule{}
	for _, r := range Rules {
		m[r.ID] = r
	}
	return m
}()

// RuleFor returns a rule by id.
func RuleFor(id string) (crawl.Rule, bool) { r, ok := ruleByID[id]; return r, ok }

// FixChanges is what a run changed on the fixes list.
type FixChanges struct {
	New, Reopened, Confirmed int
}

// syncFixes updates the fixes list from a finished run: a prompt whose
// answers in this run (at least one) never name the brand is a fix on that
// prompt; a prompt that is a fix and whose answers now name the brand is
// done, confirmed by the run.
func syncFixes(ctx context.Context, tx pgx.Tx, ws, cohortID, runID string) (FixChanges, error) {
	var ch FixChanges
	rows, err := tx.Query(ctx, `
		WITH answers AS (
			SELECT s.prompt_id, count(*) AS total,
			       count(*) FILTER (WHERE EXISTS (SELECT 1 FROM ai_mentions m WHERE m.sample_id = s.id AND m.entity = 'brand')) AS named
			FROM ai_samples s WHERE s.run_id = $3 AND s.workspace_id = $1 AND s.status = 'ok' GROUP BY s.prompt_id
		),
		prev AS (SELECT id, status FROM fixes WHERE workspace_id = $1 AND source = $4 AND rule = 'ai_brand_absent'),
		up AS (
			INSERT INTO fixes (workspace_id, brand_id, source, subject_type, subject_id, rule, severity, group_key, count, unit, found_by, last_found_by)
			SELECT $1, c.brand_id, $4, 'ai_prompt', a.prompt_id, 'ai_brand_absent', 'warning', '', a.total, 'answers', $3, $3
			FROM answers a JOIN ai_cohorts c ON c.id = $2
			WHERE a.named = 0
			ON CONFLICT (workspace_id, source, subject_id, rule, group_key) DO UPDATE SET
				count = EXCLUDED.count, last_found_by = EXCLUDED.last_found_by, brand_id = EXCLUDED.brand_id,
				status = CASE WHEN fixes.status = 'done' THEN 'open' ELSE fixes.status END,
				confirmed_by = CASE WHEN fixes.status = 'done' THEN NULL ELSE fixes.confirmed_by END,
				fixed_at = CASE WHEN fixes.status = 'done' THEN NULL ELSE fixes.fixed_at END,
				updated_at = now()
			RETURNING id
		)
		SELECT up.id::text, prev.status FROM up LEFT JOIN prev ON prev.id = up.id`, ws, cohortID, runID, FixSource)
	if err != nil {
		return ch, err
	}
	var id string
	var was *string
	var reopened []string
	if _, err := pgx.ForEachRow(rows, []any{&id, &was}, func() error {
		switch {
		case was == nil:
			ch.New++
		case *was == "done":
			reopened = append(reopened, id)
		}
		return nil
	}); err != nil {
		return ch, err
	}
	for _, f := range reopened {
		ch.Reopened++
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "fix.reopened",
			TargetType: "fix", TargetID: f, Data: map[string]any{"source": FixSource, "rule": "ai_brand_absent", "run_id": runID}}); err != nil {
			return ch, err
		}
	}
	// Prompts this run answered with the brand named are done.
	rows, err = tx.Query(ctx, `
		UPDATE fixes f SET status = 'done', count = 0, confirmed_by = $3, fixed_at = now(), updated_at = now()
		WHERE f.workspace_id = $1 AND f.source = $4 AND f.rule = 'ai_brand_absent' AND f.status <> 'ignored'
		  AND f.last_found_by IS DISTINCT FROM $3 AND f.confirmed_by IS NULL
		  AND EXISTS (SELECT 1 FROM ai_samples s JOIN ai_mentions m ON m.sample_id = s.id AND m.entity = 'brand'
		              WHERE s.run_id = $3 AND s.prompt_id = f.subject_id)
		  AND EXISTS (SELECT 1 FROM ai_prompts p WHERE p.id = f.subject_id AND p.cohort_id = $2)
		RETURNING f.id::text`, ws, cohortID, runID, FixSource)
	if err != nil {
		return ch, err
	}
	var confirmed []string
	if _, err := pgx.ForEachRow(rows, []any{&id}, func() error {
		confirmed = append(confirmed, id)
		return nil
	}); err != nil {
		return ch, err
	}
	for _, f := range confirmed {
		ch.Confirmed++
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "fix.confirmed",
			TargetType: "fix", TargetID: f, Data: map[string]any{"source": FixSource, "rule": "ai_brand_absent", "run_id": runID}}); err != nil {
			return ch, err
		}
	}
	return ch, nil
}

// syncFactFixes updates the fixes list from a finished run's fact checks:
// a fact that at least one of the run's answers contradicts is a fix on
// that fact; a fact that is a fix and that the run's answers mention
// without contradicting is done, confirmed by the run.
func syncFactFixes(ctx context.Context, tx pgx.Tx, ws, runID string) (FixChanges, error) {
	var ch FixChanges
	rows, err := tx.Query(ctx, `
		WITH wrong AS (
			SELECT k.fact_id, count(*) AS answers FROM ai_fact_checks k
			WHERE k.run_id = $2 AND k.workspace_id = $1 AND k.verdict = 'contradicted' GROUP BY k.fact_id
		),
		prev AS (SELECT id, status FROM fixes WHERE workspace_id = $1 AND source = $3 AND rule = 'ai_fact_wrong'),
		up AS (
			INSERT INTO fixes (workspace_id, brand_id, source, subject_type, subject_id, rule, severity, group_key, count, unit, found_by, last_found_by)
			SELECT $1, f.brand_id, $3, 'ai_fact', w.fact_id, 'ai_fact_wrong', 'error', '', w.answers, 'answers', $2, $2
			FROM wrong w JOIN ai_brand_facts f ON f.id = w.fact_id AND f.workspace_id = $1
			ON CONFLICT (workspace_id, source, subject_id, rule, group_key) DO UPDATE SET
				count = EXCLUDED.count, last_found_by = EXCLUDED.last_found_by, brand_id = EXCLUDED.brand_id,
				status = CASE WHEN fixes.status = 'done' THEN 'open' ELSE fixes.status END,
				confirmed_by = CASE WHEN fixes.status = 'done' THEN NULL ELSE fixes.confirmed_by END,
				fixed_at = CASE WHEN fixes.status = 'done' THEN NULL ELSE fixes.fixed_at END,
				updated_at = now()
			RETURNING id
		)
		SELECT up.id::text, prev.status FROM up LEFT JOIN prev ON prev.id = up.id`, ws, runID, FixSource)
	if err != nil {
		return ch, err
	}
	var id string
	var was *string
	var reopened []string
	if _, err := pgx.ForEachRow(rows, []any{&id, &was}, func() error {
		switch {
		case was == nil:
			ch.New++
		case *was == "done":
			reopened = append(reopened, id)
		}
		return nil
	}); err != nil {
		return ch, err
	}
	for _, f := range reopened {
		ch.Reopened++
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "fix.reopened",
			TargetType: "fix", TargetID: f, Data: map[string]any{"source": FixSource, "rule": "ai_fact_wrong", "run_id": runID}}); err != nil {
			return ch, err
		}
	}
	rows, err = tx.Query(ctx, `
		UPDATE fixes f SET status = 'done', count = 0, confirmed_by = $2, fixed_at = now(), updated_at = now()
		WHERE f.workspace_id = $1 AND f.source = $3 AND f.rule = 'ai_fact_wrong' AND f.status <> 'ignored'
		  AND f.last_found_by IS DISTINCT FROM $2 AND f.confirmed_by IS NULL
		  AND EXISTS (SELECT 1 FROM ai_fact_checks k WHERE k.run_id = $2 AND k.fact_id = f.subject_id AND k.verdict = 'supported')
		  AND NOT EXISTS (SELECT 1 FROM ai_fact_checks k WHERE k.run_id = $2 AND k.fact_id = f.subject_id AND k.verdict = 'contradicted')
		RETURNING f.id::text`, ws, runID, FixSource)
	if err != nil {
		return ch, err
	}
	var confirmed []string
	if _, err := pgx.ForEachRow(rows, []any{&id}, func() error {
		confirmed = append(confirmed, id)
		return nil
	}); err != nil {
		return ch, err
	}
	for _, f := range confirmed {
		ch.Confirmed++
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "fix.confirmed",
			TargetType: "fix", TargetID: f, Data: map[string]any{"source": FixSource, "rule": "ai_fact_wrong", "run_id": runID}}); err != nil {
			return ch, err
		}
	}
	return ch, nil
}
