package api

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/coverage/linkorders"
	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

func (s *Server) applyLinkOrderCheck(ctx context.Context, tx pgx.Tx, a *access, placementID, health string, evidence map[string]any) error {
	ws, err := uuid.Parse(a.WorkspaceID)
	if err != nil {
		return err
	}
	applied, err := linkorders.ApplyPlacementCheck(ctx, tx, ws, placementID, checkEvidenceFrom(health, evidence))
	if err != nil {
		return err
	}
	for _, item := range applied {
		if item.Order == nil {
			continue
		}
		o := item.Order
		data := map[string]any{
			"brand_id":                a.Brand,
			"status":                  o.Status,
			"placement_id":            placementID,
			"last_check_live":         o.LastCheckLive,
			"last_check_target_match": o.LastCheckTargetMatch,
		}
		props := map[string]any{"brand_id": a.Brand}
		if err := recordWithWebhook(ctx, tx, nil, a.WorkspaceID, "link_order.checked", "link_order", o.ID.String(), data, props, data); err != nil {
			return err
		}
		if !item.MarkedLost {
			continue
		}
		lost := map[string]any{"brand_id": a.Brand, "status": o.Status, "reason": o.LostReason, "placement_id": placementID}
		if err := recordWithWebhook(ctx, tx, nil, a.WorkspaceID, "link_order.lost", "link_order", o.ID.String(), lost, props, lost); err != nil {
			return err
		}
		if o.PlacementID == nil {
			continue
		}
		evidence := "link_order_replace:" + o.ID.String()
		if err := coverageRaiseTask(ctx, tx, a, o.PlacementID.String(), "Replace a lost link order", "manual", evidence); err != nil {
			return err
		}
	}
	return nil
}

func checkEvidenceFrom(health string, evidence map[string]any) linkorders.CheckEvidence {
	ev := linkorders.CheckEvidence{At: time.Now().UTC(), Health: health}
	if v, ok := evidence["link_present"].(bool); ok {
		ev.LinkPresent = v
	}
	switch v := evidence["links"].(type) {
	case []crawl.Link:
		ev.Links = v
	case []any:
		raw, err := json.Marshal(v)
		if err == nil {
			_ = json.Unmarshal(raw, &ev.Links)
		}
	}
	return ev
}

// queueLinkOrderChecks enqueues coverage.check for published, paid, or
// verified orders whose guarantee has not expired, when a check is due
// and no check is already queued for the placement.
func (s *Server) queueLinkOrderChecks(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT o.workspace_id::text, o.brand_id::text, o.placement_id::text
			FROM coverage_link_orders o
			WHERE o.status IN ('published', 'paid', 'verified')
			  AND o.placement_id IS NOT NULL
			  AND (o.guarantee_until IS NULL OR o.guarantee_until >= now())
			  AND (o.last_check_at IS NULL OR o.last_check_at < now() - interval '7 days')
			  AND NOT EXISTS (
			      SELECT 1 FROM jobs j
			      WHERE j.kind = $1 AND j.status IN ('queued', 'running')
			        AND j.payload->>'placement_id' = o.placement_id::text
			  )
			ORDER BY o.last_check_at NULLS FIRST
			LIMIT 50
			FOR UPDATE OF o SKIP LOCKED`, jobCoverageCheck)
		if err != nil {
			return err
		}
		type due struct{ ws, brand, placement string }
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (due, error) {
			var d due
			return d, row.Scan(&d.ws, &d.brand, &d.placement)
		})
		if err != nil {
			return err
		}
		day := time.Now().UTC().Format("2006-01-02")
		for _, d := range list {
			_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{
				Kind:           jobCoverageCheck,
				WorkspaceID:    &d.ws,
				Payload:        map[string]string{"placement_id": d.placement, "brand_id": d.brand, "scheduled": "true"},
				IdempotencyKey: d.placement + ":link_order:" + day,
				MaxAttempts:    3,
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// queueLinkOrderReminders opens coverage tasks for an approved order past
// its TAT (publication overdue) and for a published order whose last
// live-link check passed but has not been marked paid. Reasons stay on
// the existing coverage_tasks.reason list (manual) so this needs no
// migration.
func (s *Server) queueLinkOrderReminders(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := raiseLinkOrderTasks(ctx, tx, `
			SELECT o.workspace_id, o.brand_id, o.placement_id, o.publisher_id,
			       'Publication of a paid link order is overdue',
			       'link_order_overdue:' || o.id::text
			FROM coverage_link_orders o
			JOIN coverage_donor_profiles d ON d.id = o.donor_profile_id
			WHERE o.status = 'approved'
			  AND o.approved_at IS NOT NULL
			  AND o.approved_at + make_interval(days => coalesce(d.tat_days, 14)) < now()
			LIMIT 100`); err != nil {
			return err
		}
		return raiseLinkOrderTasks(ctx, tx, `
			SELECT o.workspace_id, o.brand_id, o.placement_id, o.publisher_id,
			       'Mark this link order paid',
			       'link_order_pay:' || o.id::text
			FROM coverage_link_orders o
			WHERE o.status = 'published'
			  AND o.payment_id IS NULL
			  AND o.last_check_live IS TRUE
			  AND o.last_check_target_match IS TRUE
			LIMIT 100`)
	})
}

func raiseLinkOrderTasks(ctx context.Context, tx pgx.Tx, sql string, args ...any) error {
	rows, err := tx.Query(ctx, `
		INSERT INTO coverage_tasks (workspace_id, brand_id, placement_id, publisher_id, title, reason, evidence)
		SELECT workspace_id, brand_id, placement_id, publisher_id, title, 'manual', evidence
		FROM (`+sql+`) q(workspace_id, brand_id, placement_id, publisher_id, title, evidence)
		WHERE NOT EXISTS (
		    SELECT 1 FROM coverage_tasks t
		    WHERE t.workspace_id = q.workspace_id AND t.brand_id = q.brand_id
		      AND t.reason = 'manual' AND t.evidence = q.evidence
		      AND t.status IN ('open', 'in_progress', 'snoozed')
		)
		RETURNING workspace_id::text, brand_id::text, id::text, evidence`, args...)
	if err != nil {
		return err
	}
	type made struct{ ws, brand, id, evidence string }
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (made, error) {
		var c made
		return c, row.Scan(&c.ws, &c.brand, &c.id, &c.evidence)
	})
	if err != nil {
		return err
	}
	for _, c := range list {
		if err := coverageHistory(ctx, tx, &access{WorkspaceID: c.ws, Brand: c.brand}, "tasks", c.id, "created", map[string]any{"reason": "manual", "evidence": c.evidence}); err != nil {
			return err
		}
	}
	return nil
}
