package api

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

type salesMessagePreview struct {
	Body       string    `json:"body"`
	Direction  string    `json:"direction"`
	Kind       string    `json:"kind"`
	OccurredAt time.Time `json:"occurred_at"`
}

type salesLeadActivity struct {
	Action     string    `json:"action"`
	ActorID    *string   `json:"actor_id"`
	ActorRole  string    `json:"actor_role"`
	OccurredAt time.Time `json:"occurred_at"`
}

// Enrich only the already-authorized page, with at most two message excerpts
// per lead. The existing lead/time index bounds each lateral lookup; neither
// full threads nor one HTTP/database round trip per row are needed.
func (s *Server) salesQueuePreviews(ctx context.Context, a *access, leads []salesLead, manager bool) error {
	if len(leads) == 0 {
		return nil
	}
	ids := make([]string, len(leads))
	byID := make(map[string]*salesLead, len(leads))
	for i := range leads {
		ids[i] = leads[i].ID
		byID[leads[i].ID] = &leads[i]
	}
	rows, err := s.pool.Query(ctx, `SELECT wanted.id::text,pick.incoming,m.id::text,m.direction,m.kind,m.occurred_at,m.content_sealed
 FROM unnest($3::uuid[]) AS wanted(id)
 CROSS JOIN (VALUES(false),(true)) AS pick(incoming)
 CROSS JOIN LATERAL (
  SELECT id,direction,kind,occurred_at,content_sealed FROM sales_messages
  WHERE workspace_id=$1 AND brand_id=$2 AND lead_id=wanted.id AND superseded_by IS NULL
   AND (NOT pick.incoming OR direction='incoming')
  ORDER BY occurred_at DESC,id DESC LIMIT 1
 ) m`, a.WorkspaceID, a.Brand, ids)
	if err != nil {
		return err
	}
	_, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (struct{}, error) {
		var lead, id string
		var incoming bool
		var sealed []byte
		var p salesMessagePreview
		if err := row.Scan(&lead, &incoming, &id, &p.Direction, &p.Kind, &p.OccurredAt, &sealed); err != nil {
			return struct{}{}, err
		}
		var m salesMessage
		if err := s.salesOpen(sealed, id, &m); err != nil {
			return struct{}{}, err
		}
		body := []rune(m.Body)
		if len(body) > 1200 {
			body = body[:1200]
		}
		p.Body = string(body)
		if incoming {
			byID[lead].LatestReply = &p
		} else {
			byID[lead].LatestMessage = &p
		}
		return struct{}{}, nil
	})
	if err != nil || !manager {
		return err
	}
	rows, err = s.pool.Query(ctx, `SELECT wanted.id::text,e.activity
 FROM unnest($3::uuid[]) AS wanted(id)
 CROSS JOIN LATERAL (
  SELECT jsonb_build_object('action',action,'actor_id',actor_id,'actor_role',actor_role,'occurred_at',occurred_at) AS activity
  FROM sales_events WHERE workspace_id=$1 AND brand_id=$2 AND lead_id=wanted.id
  ORDER BY occurred_at DESC,id DESC LIMIT 1
 ) e`, a.WorkspaceID, a.Brand, ids)
	if err != nil {
		return err
	}
	_, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (struct{}, error) {
		var lead string
		var raw []byte
		if err := row.Scan(&lead, &raw); err != nil {
			return struct{}{}, err
		}
		var activity salesLeadActivity
		if err := json.Unmarshal(raw, &activity); err != nil {
			return struct{}{}, err
		}
		byID[lead].LastActivity = &activity
		return struct{}{}, nil
	})
	return err
}
