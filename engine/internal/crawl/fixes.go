package crawl

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
)

// FixChanges is what a crawl changed on its site's fixes list.
type FixChanges struct {
	New, Reopened, Confirmed int
}

// FixSource is the fixes list's source for site audits.
const FixSource = "seo_audit"

// syncFixes updates the workspace's fixes list from a crawl's issues: each
// rule and template the crawl found is a fix on the site, with its pages
// counted again; a fix marked done that came back is open again. Only a
// complete crawl (every page found was read) can confirm a fix: what it no
// longer finds is done, confirmed by it.
func syncFixes(ctx context.Context, tx pgx.Tx, ws, siteID, runID string, complete bool) (FixChanges, error) {
	var ch FixChanges
	rows, err := tx.Query(ctx, `
		WITH prev AS (SELECT id, status FROM fixes WHERE workspace_id = $1 AND source = $4 AND subject_id = $2),
		site AS (SELECT brand_id FROM seo_sites WHERE id = $2 AND workspace_id = $1),
		up AS (
			INSERT INTO fixes (workspace_id, brand_id, source, subject_type, subject_id, rule, severity, group_key, count, unit, found_by, last_found_by)
			SELECT $1, (SELECT brand_id FROM site), $4, 'seo_site', $2, rule, max(severity), template, count(DISTINCT page_id), 'pages', $3, $3
			FROM crawl_issues WHERE run_id = $3 AND workspace_id = $1 GROUP BY rule, template
			ON CONFLICT (workspace_id, source, subject_id, rule, group_key) DO UPDATE SET
				count = EXCLUDED.count, severity = EXCLUDED.severity, last_found_by = EXCLUDED.last_found_by, brand_id = EXCLUDED.brand_id,
				status = CASE WHEN fixes.status = 'done' THEN 'open' ELSE fixes.status END,
				confirmed_by = CASE WHEN fixes.status = 'done' THEN NULL ELSE fixes.confirmed_by END,
				fixed_at = CASE WHEN fixes.status = 'done' THEN NULL ELSE fixes.fixed_at END,
				updated_at = now()
			RETURNING id, rule
		)
		SELECT up.id::text, up.rule, prev.status FROM up LEFT JOIN prev ON prev.id = up.id`, ws, siteID, runID, FixSource)
	if err != nil {
		return ch, err
	}
	type changed struct{ id, rule string }
	var found, reopened []changed
	var id, rule string
	var was *string
	_, err = pgx.ForEachRow(rows, []any{&id, &rule, &was}, func() error {
		switch {
		case was == nil:
			found = append(found, changed{id, rule})
		case *was == "done":
			reopened = append(reopened, changed{id, rule})
		}
		return nil
	})
	if err != nil {
		return ch, err
	}
	// One event per new fix, so a coding agent subscribed to fix.found can
	// pick it up (GET .../fixes/{id}/prompt) without polling the list.
	for _, f := range found {
		ch.New++
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "fix.found",
			TargetType: "fix", TargetID: f.id, Data: map[string]any{"source": FixSource, "rule": f.rule, "subject_type": "seo_site", "subject_id": siteID, "run_id": runID}}); err != nil {
			return ch, err
		}
	}
	for _, f := range reopened {
		ch.Reopened++
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "fix.reopened",
			TargetType: "fix", TargetID: f.id, Data: map[string]any{"source": FixSource, "rule": f.rule, "subject_type": "seo_site", "subject_id": siteID, "run_id": runID}}); err != nil {
			return ch, err
		}
	}
	if !complete {
		return ch, nil
	}
	rows, err = tx.Query(ctx, `
		UPDATE fixes SET status = 'done', count = 0, confirmed_by = $3, fixed_at = now(), updated_at = now()
		WHERE workspace_id = $1 AND source = $4 AND subject_id = $2 AND status <> 'ignored' AND last_found_by IS DISTINCT FROM $3 AND confirmed_by IS NULL
		RETURNING id::text, rule`, ws, siteID, runID, FixSource)
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
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "fix.confirmed",
			TargetType: "fix", TargetID: f.id, Data: map[string]any{"source": FixSource, "rule": f.rule, "subject_type": "seo_site", "subject_id": siteID, "run_id": runID}}); err != nil {
			return ch, err
		}
	}
	return ch, nil
}
