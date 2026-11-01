package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/crawl"
)

func coverageRuleEnabled(ctx context.Context, tx pgx.Tx, ws, brand, kind string) (bool, error) {
	var enabled bool
	err := tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM coverage_rules WHERE workspace_id=$1 AND brand_id=$2 AND kind=$3 AND NOT enabled)`, ws, brand, kind).Scan(&enabled)
	return enabled, err
}
func coverageRelMatches(evidence map[string]any, expected string) bool {
	if expected == "" || expected == "any" {
		return true
	}
	raw, _ := json.Marshal(evidence["links"])
	var links []crawl.Link
	if json.Unmarshal(raw, &links) != nil {
		return false
	}
	for _, link := range links {
		if expected == "follow" {
			if !slices.Contains(link.Rel, "nofollow") && !slices.Contains(link.Rel, "sponsored") && !slices.Contains(link.Rel, "ugc") {
				return true
			}
		} else if slices.Contains(link.Rel, expected) {
			return true
		}
	}
	return false
}

// Finance work is separately permissioned. Neither a reminder nor a recorded
// approval executes a renewal, commits to a purchase or moves money.
func (s *Server) queueCoverageCommercialDeadlines(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `WITH due AS (
 SELECT a.workspace_id,a.brand_id,a.id AS agreement_id,NULL::uuid AS invoice_id,a.publisher_id,a.owner_id,'renewal_due' AS reason,'Review upcoming renewal' AS title,a.renewal_on AS due_on,'Agreement '||a.id::text||' renewal '||a.renewal_on::text AS evidence FROM coverage_agreements a WHERE a.status='active' AND a.renewal_on<=current_date+30
 UNION ALL SELECT a.workspace_id,a.brand_id,a.id,NULL::uuid,a.publisher_id,a.owner_id,'notice_due','Review cancellation notice deadline',a.notice_on,'Agreement '||a.id::text||' notice '||a.notice_on::text FROM coverage_agreements a WHERE a.status='active' AND a.notice_on<=current_date+30
 UNION ALL SELECT i.workspace_id,i.brand_id,a.id,i.id,a.publisher_id,a.owner_id,'invoice_due','Review an invoice due',i.due_on,'Invoice '||i.id::text||' due '||i.due_on::text FROM coverage_invoices i JOIN coverage_agreements a ON a.id=i.agreement_id AND a.workspace_id=i.workspace_id AND a.brand_id=i.brand_id WHERE i.kind='invoice' AND i.status IN ('received','disputed') AND i.due_on<=current_date+7 AND i.amount_minor>coalesce((SELECT sum(CASE WHEN p.kind='reversal' THEN -p.amount_minor ELSE p.amount_minor END) FROM coverage_payments p WHERE p.workspace_id=i.workspace_id AND p.brand_id=i.brand_id AND p.invoice_id=i.id),0)
 UNION ALL SELECT d.workspace_id,d.brand_id,a.id,NULL::uuid,a.publisher_id,a.owner_id,'delivery_overdue','Review overdue delivery',d.due_on,'Deliverable '||d.id::text||' due '||d.due_on::text FROM coverage_deliverables d JOIN coverage_agreements a ON a.id=d.agreement_id AND a.workspace_id=d.workspace_id AND a.brand_id=d.brand_id WHERE d.status IN ('pending','partial') AND d.due_on<current_date AND a.status='active'
 ) INSERT INTO coverage_finance_tasks(workspace_id,brand_id,agreement_id,invoice_id,publisher_id,owner_id,reason,title,due_on,evidence)
 SELECT workspace_id,brand_id,agreement_id,invoice_id,publisher_id,owner_id,reason,title,due_on,evidence FROM due d WHERE NOT EXISTS(SELECT 1 FROM coverage_finance_tasks t WHERE t.workspace_id=d.workspace_id AND t.brand_id=d.brand_id AND t.reason=d.reason AND t.evidence=d.evidence) ORDER BY due_on LIMIT 100
 ON CONFLICT(workspace_id,brand_id,reason,evidence) WHERE reason<>'manual' DO NOTHING
 RETURNING workspace_id::text,brand_id::text,id::text,reason`)
		if err != nil {
			return err
		}
		type made struct{ ws, brand, id, reason string }
		list := []made{}
		for rows.Next() {
			var c made
			if err = rows.Scan(&c.ws, &c.brand, &c.id, &c.reason); err != nil {
				rows.Close()
				return err
			}
			list = append(list, c)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		for _, c := range list {
			if err = coverageHistory(ctx, tx, &access{WorkspaceID: c.ws, Brand: c.brand}, "finance_tasks", c.id, "created", map[string]any{"reason": c.reason}); err != nil {
				return err
			}
		}
		return nil
	})
}

func coverageRaiseTask(ctx context.Context, tx pgx.Tx, a *access, placement, title, reason, evidence string) error {
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO coverage_tasks(workspace_id,brand_id,placement_id,publisher_id,title,reason,owner_id,evidence)
 SELECT p.workspace_id,p.brand_id,p.id,p.publisher_id,$4,$5,coalesce(r.owner_id,p.owner_id,pub.owner_id),$6
 FROM coverage_placements p JOIN coverage_publishers pub ON pub.id=p.publisher_id AND pub.workspace_id=p.workspace_id AND pub.brand_id=p.brand_id
 LEFT JOIN coverage_rules r ON r.workspace_id=p.workspace_id AND r.brand_id=p.brand_id AND r.kind=$5
 WHERE p.workspace_id=$1 AND p.brand_id=$2 AND p.id=$3 AND coalesce(r.enabled,true) AND ($5<>'relationship_followup' OR pub.relationship_status<>'do_not_contact')
 AND NOT EXISTS(SELECT 1 FROM coverage_tasks t WHERE t.workspace_id=p.workspace_id AND t.brand_id=p.brand_id AND t.placement_id=p.id AND t.reason=$5 AND t.evidence=$6 AND t.status IN ('resolved','dismissed'))
 ON CONFLICT(workspace_id,brand_id,placement_id,reason) WHERE reason<>'manual' AND status IN ('open','in_progress','snoozed') DO NOTHING RETURNING id::text`, a.WorkspaceID, a.Brand, placement, title, reason, evidence).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return coverageHistory(ctx, tx, a, "tasks", id, "created", map[string]any{"reason": reason, "placement_id": placement})
}
func coverageResolveTask(ctx context.Context, tx pgx.Tx, a *access, placement, reason, resolution string) error {
	var id string
	err := tx.QueryRow(ctx, `UPDATE coverage_tasks SET status='resolved',resolution=$5,updated_at=now(),version=version+1 WHERE workspace_id=$1 AND brand_id=$2 AND placement_id=$3 AND reason=$4 AND status IN ('open','in_progress','snoozed') RETURNING id::text`, a.WorkspaceID, a.Brand, placement, reason, resolution).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return coverageHistory(ctx, tx, a, "tasks", id, "updated", map[string]any{"fields": []string{"status", "resolution"}, "reason": reason, "placement_id": placement})
}

func (s *Server) coverageJobObserved(ctx context.Context, ws, brand, placement, source string, jobID int64) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM coverage_observations WHERE workspace_id=$1 AND brand_id=$2 AND placement_id=$3 AND source=$4 AND evidence->>'job_id'=$5)`, ws, brand, placement, source, fmt.Sprint(jobID)).Scan(&exists)
	return exists, err
}
