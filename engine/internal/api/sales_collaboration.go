package api

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

type salesLeadView struct {
	UserID string    `json:"user_id"`
	SeenAt time.Time `json:"seen_at"`
}

type salesLeadFeedback struct {
	ID        string    `json:"id"`
	AuthorID  *string   `json:"author_id"`
	CreatedAt time.Time `json:"created_at"`
	Category  string    `json:"category"`
	Body      string    `json:"body"`
}

// Views are recorded when someone opens or explicitly marks a conversation
// seen, not when the queue is displayed. Read only the already-authorized page of leads.
func (s *Server) salesTeamSignals(ctx context.Context, a *access, leads []salesLead) error {
	if len(leads) == 0 {
		return nil
	}
	ids := make([]string, len(leads))
	byID := make(map[string]*salesLead, len(leads))
	for i := range leads {
		ids[i] = leads[i].ID
		byID[ids[i]] = &leads[i]
	}
	rows, err := s.pool.Query(ctx, `SELECT wanted.id::text,
 COALESCE((SELECT jsonb_agg(v) FROM (SELECT seen.user_id::text,seen.seen_at FROM sales_seen seen
 JOIN memberships m ON m.workspace_id=seen.workspace_id AND m.user_id=seen.user_id
 JOIN users u ON u.id=seen.user_id AND u.disabled_at IS NULL
 WHERE seen.workspace_id=$1 AND seen.brand_id=$2 AND seen.lead_id=wanted.id
 AND (m.brand_ids IS NULL OR cardinality(m.brand_ids)=0 OR $2::uuid=ANY(m.brand_ids))
 ORDER BY seen.seen_at DESC,seen.user_id LIMIT 5) v),'[]'::jsonb),
 (SELECT count(*) FROM sales_events e WHERE e.workspace_id=$1 AND e.brand_id=$2 AND e.lead_id=wanted.id AND e.action='feedback_added')
 FROM unnest($3::uuid[]) wanted(id)`, a.WorkspaceID, a.Brand, ids)
	if err != nil {
		return err
	}
	_, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (struct{}, error) {
		var id string
		var raw []byte
		var count int
		if err := row.Scan(&id, &raw, &count); err != nil {
			return struct{}{}, err
		}
		l := byID[id]
		l.FeedbackCount = count
		return struct{}{}, json.Unmarshal(raw, &l.ViewedBy)
	})
	return err
}

// Comments stay in the encrypted, append-only Sales evidence. The same event
// is linked to the canonical record's history by salesEvent.
func (s *Server) salesFeedback(ctx context.Context, a *access, lead string) ([]salesLeadFeedback, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text,actor_id::text,occurred_at,content_sealed FROM sales_events WHERE workspace_id=$1 AND brand_id=$2 AND lead_id=$3 AND action='feedback_added' ORDER BY occurred_at DESC,id DESC LIMIT 50`, a.WorkspaceID, a.Brand, lead)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (salesLeadFeedback, error) {
		var f salesLeadFeedback
		var sealed []byte
		if err := row.Scan(&f.ID, &f.AuthorID, &f.CreatedAt, &sealed); err != nil {
			return f, err
		}
		var evidence struct {
			After struct {
				Feedback struct {
					Category string
					Body     string
				}
			}
		}
		if err := s.salesOpen(sealed, f.ID, &evidence); err != nil {
			return f, err
		}
		f.Category, f.Body = evidence.After.Feedback.Category, evidence.After.Feedback.Body
		return f, nil
	})
	if items == nil {
		items = []salesLeadFeedback{}
	}
	return items, err
}
