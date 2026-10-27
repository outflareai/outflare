// Package fixes keeps a source's findings on the workspace's fixes list.
// Each source (site audits, search intelligence) works out what it finds
// on a subject and hands it to Sync, which adds new fixes, counts known
// ones again, opens again what came back and confirms what is gone.
package fixes

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
)

// Found is one fix a source found: a rule on a group of its subject.
type Found struct {
	Rule     string
	Severity string
	Group    string
	Count    int
	Unit     string
}

// Run is one pass of a source over a subject.
type Run struct {
	Workspace   string
	Source      string
	SubjectType string
	SubjectID   string
	BrandID     *string
	// RunID stamps found_by and last_found_by, and confirmed_by on what is
	// gone: the source's run (a crawl's id) or a fresh id per pass.
	RunID string
	// Rules are the rules this pass checked. Fixes of other rules are left
	// alone.
	Rules []string
	// Keep are groups this pass could not check, so their fixes are left
	// alone.
	Keep []string
	// Reopen opens again a fix marked done that is still found. A pass with
	// new evidence (a crawl) reopens; one that only re-reads it does not, so
	// marking a fix done holds until the next crawl.
	Reopen bool
}

// Changes is what a pass changed.
type Changes struct {
	New, Reopened, Confirmed int
}

// Sync puts what a pass found on the fixes list and confirms the fixes of
// its rules it no longer finds. Each reopened and confirmed fix is an
// audit event (fix.reopened, fix.confirmed) in tx.
func Sync(ctx context.Context, tx pgx.Tx, run Run, found []Found) (Changes, error) {
	var ch Changes
	rules, sevs, groups, counts, units := make([]string, len(found)), make([]string, len(found)), make([]string, len(found)), make([]int32, len(found)), make([]string, len(found))
	for i, f := range found {
		rules[i], sevs[i], groups[i], counts[i], units[i] = f.Rule, f.Severity, f.Group, int32(f.Count), f.Unit
	}
	type changed struct{ id, rule string }
	var reopened []changed
	rows, err := tx.Query(ctx, `
		WITH prev AS (SELECT id, status FROM fixes WHERE workspace_id = $1 AND source = $2 AND subject_id = $4),
		up AS (
			INSERT INTO fixes (workspace_id, brand_id, source, subject_type, subject_id, rule, severity, group_key, count, unit, found_by, last_found_by)
			SELECT $1, $5::uuid, $2, $3, $4, f.rule, f.severity, f.grp, f.count, f.unit, $6, $6
			FROM unnest($7::text[], $8::text[], $9::text[], $10::int[], $11::text[]) AS f(rule, severity, grp, count, unit)
			ON CONFLICT (workspace_id, source, subject_id, rule, group_key) DO UPDATE SET
				count = EXCLUDED.count, severity = EXCLUDED.severity, unit = EXCLUDED.unit, last_found_by = EXCLUDED.last_found_by, brand_id = EXCLUDED.brand_id,
				status = CASE WHEN fixes.status = 'done' AND $12 THEN 'open' ELSE fixes.status END,
				confirmed_by = CASE WHEN fixes.status = 'done' AND $12 THEN NULL ELSE fixes.confirmed_by END,
				fixed_at = CASE WHEN fixes.status = 'done' AND $12 THEN NULL ELSE fixes.fixed_at END,
				updated_at = now()
			RETURNING id, rule, status
		)
		SELECT up.id::text, up.rule, prev.status, up.status FROM up LEFT JOIN prev ON prev.id = up.id`,
		run.Workspace, run.Source, run.SubjectType, run.SubjectID, run.BrandID, run.RunID, rules, sevs, groups, counts, units, run.Reopen)
	if err != nil {
		return ch, err
	}
	var id, rule, now string
	var was *string
	_, err = pgx.ForEachRow(rows, []any{&id, &rule, &was, &now}, func() error {
		switch {
		case was == nil:
			ch.New++
		case *was == "done" && now == "open":
			reopened = append(reopened, changed{id, rule})
		}
		return nil
	})
	if err != nil {
		return ch, err
	}
	for _, f := range reopened {
		ch.Reopened++
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: run.Workspace, ActorType: "system", Action: "fix.reopened",
			TargetType: "fix", TargetID: f.id, Data: map[string]any{"source": run.Source, "rule": f.rule, "run_id": run.RunID}}); err != nil {
			return ch, err
		}
	}
	keep := run.Keep
	if keep == nil {
		keep = []string{}
	}
	rows, err = tx.Query(ctx, `
		UPDATE fixes SET status = 'done', count = 0, confirmed_by = $3, fixed_at = now(), updated_at = now()
		WHERE workspace_id = $1 AND source = $2 AND subject_id = $4 AND rule = ANY($5) AND NOT group_key = ANY($6)
		  AND status <> 'ignored' AND last_found_by IS DISTINCT FROM $3 AND confirmed_by IS NULL
		RETURNING id::text, rule`, run.Workspace, run.Source, run.RunID, run.SubjectID, run.Rules, keep)
	if err != nil {
		return ch, err
	}
	var confirmed []changed
	_, err = pgx.ForEachRow(rows, []any{&id, &rule}, func() error {
		confirmed = append(confirmed, changed{id, rule})
		return nil
	})
	if err != nil {
		return ch, err
	}
	for _, f := range confirmed {
		ch.Confirmed++
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: run.Workspace, ActorType: "system", Action: "fix.confirmed",
			TargetType: "fix", TargetID: f.id, Data: map[string]any{"source": run.Source, "rule": f.rule, "run_id": run.RunID}}); err != nil {
			return ch, err
		}
	}
	return ch, nil
}
