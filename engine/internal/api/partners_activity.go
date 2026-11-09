package api

import (
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

type partnerActivityView struct {
	ID         string         `json:"id"`
	Action     string         `json:"action"`
	ActorType  string         `json:"actor_type"`
	ActorName  string         `json:"actor_name"`
	TargetType string         `json:"target_type"`
	TargetID   string         `json:"target_id"`
	Data       map[string]any `json:"data"`
	CreatedAt  time.Time      `json:"created_at"`
}

// partnerActivityKeys are the audit data a partner's history shows. Payout
// details never leave as values: changed names the fields only.
var partnerActivityKeys = []string{"status", "previous_status", "group_id", "by", "kind", "side", "amount_cents", "currency", "reference",
	"reason", "code", "via", "conversion_id", "payout_id", "reward_ids", "changed"}

// portalChanges are the fields partner.portal_updated flags as sent.
var portalChanges = []string{"payout_method", "payout_type", "postback_url", "notify"}

func partnerActivityData(action string, raw map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range partnerActivityKeys {
		if v, ok := raw[k]; ok {
			out[k] = v
		}
	}
	if action == "partner.portal_updated" {
		changed := []string{}
		for _, k := range portalChanges {
			if raw[k] == true {
				changed = append(changed, k)
			}
		}
		out["changed"] = changed
	}
	return out
}

// listPartnerActivity is one partner's history from the audit log, newest
// first. The WHERE repeats audit_events_partner_idx's expression and
// predicate (migration 0487) so the index serves it.
func (s *Server) listPartnerActivity(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findPartner(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	limit, ok := pageLimit(w, r, 100, 500)
	if !ok {
		return
	}
	cursor, ok := pageCursor(w, r, pageBefore)
	if !ok {
		return
	}
	before := int64(math.MaxInt64)
	if cursor != "" {
		n, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || n < 1 {
			invalidCursor(w, r)
			return
		}
		before = n
	}
	rows, err := s.pool.Query(r.Context(), `SELECT e.id::text, e.action, e.actor_type, COALESCE(ac.display_name, ''), e.target_type, e.target_id, e.data, e.created_at
		FROM audit_events e LEFT JOIN actors ac ON ac.id = e.actor_id
		WHERE e.workspace_id = $1 AND e.target_type IN ('partner', 'partner_referral', 'partner_reward', 'partner_payout')
		  AND (CASE WHEN e.target_type = 'partner' THEN e.target_id ELSE e.data->>'partner_id' END) = $2 AND e.id < $3
		ORDER BY e.id DESC LIMIT $4`, a.WorkspaceID, cur.ID, before, limit+1)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (partnerActivityView, error) {
		var v partnerActivityView
		err := row.Scan(&v.ID, &v.Action, &v.ActorType, &v.ActorName, &v.TargetType, &v.TargetID, &v.Data, &v.CreatedAt)
		v.Data = partnerActivityData(v.Action, v.Data)
		return v, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next any
	if len(list) > limit {
		list = list[:limit]
		next = list[limit-1].ID
	}
	if list == nil {
		list = []partnerActivityView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_cursor": nextCursor(pageBefore, next)})
}
