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
	{ID: "ai_competitor_preferred", Severity: crawl.SevWarning, Category: "ai_visibility", Title: "AI answers recommend competitors instead of the brand",
		Why: "When people ask AI models this question, the answers name the brand but recommend a competitor, so buyers who would choose it are pointed elsewhere.",
		Fix: "Read the answers' reasons for the competitor (the sample shows each brand's stance and the words used), then answer them on the brand's own pages: a comparison page for this question, proof on the points where the competitor wins (prices, numbers, reviews), and a mention on the pages these answers cite."},
	{ID: "ai_fact_wrong", Severity: crawl.SevError, Category: "ai_visibility", Title: "AI answers get a fact about the brand wrong",
		Why: "AI answers tell buyers something about the brand that contradicts what the brand says is true, such as an old price or a feature it has, so buyers decide on wrong information.",
		Fix: "State the fact plainly on the brand's own pages (pricing, features, about) where models read it, correct it on the third-party pages the answers cite, and keep old pages and listings up to date. The fact checks show the wrong words each answer used."},
	{ID: "ai_wrong_claim", Severity: crawl.SevError, Category: "ai_visibility", Title: "AI answers hold something against the brand that isn't true",
		Why: "AI answers give buyers a reason against the brand, or for picking a competitor instead, that the brand says is wrong (a price it doesn't charge, a feature it has, a limit it lifted), so buyers rule it out on a false point.",
		Fix: "Say the opposite plainly where models read: the brand's own page on that point (pricing, features, a comparison page), with proof. Then correct the third-party pages these answers cite (the claim lists them) and ask for an update where someone else wrote it. The next runs show whether the answers stop saying it."},
	{ID: "ai_page_unclear", Severity: crawl.SevNotice, Category: "ai_visibility", Title: "Page is hard for AI answers to quote",
		Why: "An AI check read the page the way an AI answer does and found it doesn't answer up front, or makes claims that are vague or unsupported, so answers may skip it or quote it wrongly.",
		Fix: "Make the changes the AI check lists, most useful first, then check the page again on Your pages in AI visibility."},
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

// promptRule is a fix on a prompt, from its answers in one run: found
// when they match found, done when the run answered the prompt and they
// match done. The answers CTE gives per prompt: total (answers), named
// (answers naming the brand), judged (answers read for stances), brand_rec
// (judged answers recommending the brand) and rival_rec (judged answers
// recommending a competitor).
type promptRule struct {
	id, severity, found, count, done string
}

var promptRules = []promptRule{
	// The answers never name the brand.
	{id: "ai_brand_absent", severity: "warning", found: "a.named = 0", count: "a.total", done: "a.named > 0"},
	// The answers name the brand but recommend only competitors.
	{id: "ai_competitor_preferred", severity: "warning", found: "a.named > 0 AND a.brand_rec = 0 AND a.rival_rec > 0", count: "a.rival_rec",
		done: "a.judged > 0 AND (a.brand_rec > 0 OR a.rival_rec = 0)"},
}

const promptAnswers = `answers AS (
			SELECT s.prompt_id, count(*) AS total,
			       count(*) FILTER (WHERE EXISTS (SELECT 1 FROM ai_mentions m WHERE m.sample_id = s.id AND m.entity = 'brand')) AS named,
			       count(*) FILTER (WHERE s.stance_status = 'judged') AS judged,
			       count(*) FILTER (WHERE s.stance_status = 'judged' AND EXISTS (SELECT 1 FROM ai_mentions m WHERE m.sample_id = s.id AND m.entity = 'brand' AND m.stance = 'recommended')) AS brand_rec,
			       count(*) FILTER (WHERE s.stance_status = 'judged' AND EXISTS (SELECT 1 FROM ai_mentions m WHERE m.sample_id = s.id AND m.entity = 'competitor' AND m.stance = 'recommended')) AS rival_rec
			FROM ai_samples s WHERE s.run_id = $3 AND s.workspace_id = $1 AND s.status = 'ok' GROUP BY s.prompt_id
		)`

// syncFixes updates the fixes list from a finished run, rule by rule (see
// promptRules): a prompt whose answers in this run match a rule is a fix on
// that prompt, reopened if it was done; a fix whose prompt's answers now
// pass is done, confirmed by the run.
func syncFixes(ctx context.Context, tx pgx.Tx, ws, cohortID, runID string) (FixChanges, error) {
	var ch FixChanges
	for _, r := range promptRules {
		c, err := syncPromptRule(ctx, tx, ws, cohortID, runID, r)
		if err != nil {
			return ch, err
		}
		ch.New, ch.Reopened, ch.Confirmed = ch.New+c.New, ch.Reopened+c.Reopened, ch.Confirmed+c.Confirmed
	}
	return ch, nil
}

func syncPromptRule(ctx context.Context, tx pgx.Tx, ws, cohortID, runID string, r promptRule) (FixChanges, error) {
	var ch FixChanges
	rows, err := tx.Query(ctx, `
		WITH `+promptAnswers+`,
		prev AS (SELECT id, status FROM fixes WHERE workspace_id = $1 AND source = $4 AND rule = $5),
		up AS (
			INSERT INTO fixes (workspace_id, brand_id, source, subject_type, subject_id, rule, severity, group_key, count, unit, found_by, last_found_by)
			SELECT $1, c.brand_id, $4, 'ai_prompt', a.prompt_id, $5, $6, '', `+r.count+`, 'answers', $3, $3
			FROM answers a JOIN ai_cohorts c ON c.id = $2
			WHERE `+r.found+`
			ON CONFLICT (workspace_id, source, subject_id, rule, group_key) DO UPDATE SET
				count = EXCLUDED.count, last_found_by = EXCLUDED.last_found_by, brand_id = EXCLUDED.brand_id,
				status = CASE WHEN fixes.status = 'done' THEN 'open' ELSE fixes.status END,
				confirmed_by = CASE WHEN fixes.status = 'done' THEN NULL ELSE fixes.confirmed_by END,
				fixed_at = CASE WHEN fixes.status = 'done' THEN NULL ELSE fixes.fixed_at END,
				updated_at = now()
			RETURNING id
		)
		SELECT up.id::text, prev.status FROM up LEFT JOIN prev ON prev.id = up.id`, ws, cohortID, runID, FixSource, r.id, r.severity)
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
			TargetType: "fix", TargetID: f, Data: map[string]any{"source": FixSource, "rule": r.id, "run_id": runID}}); err != nil {
			return ch, err
		}
	}
	// Prompts whose answers in this run pass are done.
	rows, err = tx.Query(ctx, `
		WITH `+promptAnswers+`
		UPDATE fixes f SET status = 'done', count = 0, confirmed_by = $3, fixed_at = now(), updated_at = now()
		FROM answers a
		WHERE f.workspace_id = $1 AND f.source = $4 AND f.rule = $5 AND f.status <> 'ignored'
		  AND f.last_found_by IS DISTINCT FROM $3 AND f.confirmed_by IS NULL
		  AND a.prompt_id = f.subject_id AND `+r.done+`
		  AND EXISTS (SELECT 1 FROM ai_prompts p WHERE p.id = f.subject_id AND p.cohort_id = $2)
		RETURNING f.id::text`, ws, cohortID, runID, FixSource, r.id)
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
			TargetType: "fix", TargetID: f, Data: map[string]any{"source": FixSource, "rule": r.id, "run_id": runID}}); err != nil {
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
