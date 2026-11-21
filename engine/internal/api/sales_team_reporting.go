package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type salesTeamReportMember struct {
	UserID string         `json:"user_id"`
	Name   string         `json:"name"`
	Role   string         `json:"role"`
	Active bool           `json:"active"`
	Counts map[string]int `json:"counts"`
}
type salesTeamActivity struct {
	ID         string    `json:"id"`
	LeadID     string    `json:"lead_id"`
	RecordID   string    `json:"record_id"`
	Name       string    `json:"name"`
	Company    string    `json:"company"`
	ActorID    *string   `json:"actor_id"`
	ActorName  string    `json:"actor_name"`
	ActorRole  string    `json:"actor_role"`
	Action     string    `json:"action"`
	OccurredAt time.Time `json:"occurred_at"`
}
type salesActivityCursor struct {
	At time.Time `json:"at"`
	ID string    `json:"id"`
}

var salesActivityAction = regexp.MustCompile(`^[a-z][a-z0-9_]{0,79}$`)

// Earlier Seen actions were audited but not written to sales_events. Include
// that retained evidence once, without inferring the actor's historical role.
// New audits link event_id, so they are already represented by sales_events.
const salesTeamEvidence = `WITH evidence AS (
 SELECT id::text,workspace_id,brand_id,record_id,lead_id,actor_id,actor_role,action,occurred_at
 FROM sales_events WHERE workspace_id=$1 AND brand_id=$2 AND occurred_at >= $3 AND occurred_at < $4
 UNION ALL
 SELECT 'seen-audit:'||ae.id::text,ae.workspace_id,ae.brand_id,l.record_id,l.id,u.id,'unrecorded','lead_seen',ae.created_at
 FROM audit_events ae
 JOIN sales_leads l ON l.workspace_id=ae.workspace_id AND l.brand_id=ae.brand_id AND l.id=CASE WHEN ae.target_id ~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$' THEN ae.target_id::uuid END
 LEFT JOIN users u ON u.actor_id=ae.actor_id
 WHERE ae.workspace_id=$1 AND ae.brand_id=$2 AND ae.created_at >= $3 AND ae.created_at < $4
 AND ae.action='sales.lead_seen' AND ae.target_type='sales_lead' AND NOT (ae.data ? 'event_id')
)`

var salesLegacyActivityID = regexp.MustCompile(`^seen-audit:[0-9]{1,20}$`)

func (s *Server) getSalesTeamReporting(w http.ResponseWriter, r *http.Request) {
	if !s.salesReportingAllowed(w, r) {
		return
	}
	a := accessFrom(r.Context())
	q := r.URL.Query()
	from, e1 := time.Parse(time.RFC3339, q.Get("from"))
	to, e2 := time.Parse(time.RFC3339, q.Get("to"))
	if e1 != nil || e2 != nil || !to.After(from) || to.Sub(from) > 366*24*time.Hour || to.After(time.Now().Add(time.Minute)) {
		writeError(w, r, 400, "invalid_request", "Choose a past time range of at most 366 days")
		return
	}
	user, action, search := q.Get("user_id"), q.Get("action"), strings.TrimSpace(q.Get("search"))
	if (user != "" && !validUUID(user)) || (action != "" && !salesActivityAction.MatchString(action)) || len(search) > 200 {
		writeError(w, r, 400, "invalid_request", "Choose a valid teammate, action and search under 200 characters")
		return
	}
	cursor := salesActivityCursor{At: to, ID: "ffffffff-ffff-ffff-ffff-ffffffffffff"}
	if q.Has("cursor") {
		raw, err := base64.RawURLEncoding.DecodeString(q.Get("cursor"))
		if err != nil || json.Unmarshal(raw, &cursor) != nil || (!validUUID(cursor.ID) && !salesLegacyActivityID.MatchString(cursor.ID)) || cursor.At.Before(from) || cursor.At.After(to) {
			writeError(w, r, 400, "invalid_request", "Invalid activity cursor")
			return
		}
	}
	// A snapshot keeps the summary and page consistent while teammates are working.
	tx, err := s.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if s.allocError(w, r, err) {
		return
	}
	defer tx.Rollback(r.Context())
	rows, err := tx.Query(r.Context(), salesTeamEvidence+`, events AS MATERIALIZED (
 SELECT e.actor_id,e.actor_role,e.action,e.lead_id,e.occurred_at FROM evidence e
 JOIN records r ON r.workspace_id=e.workspace_id AND r.brand_id=e.brand_id AND r.id=e.record_id AND r.deleted_at IS NULL AND r.merged_into IS NULL
 WHERE e.workspace_id=$1 AND e.brand_id=$2 AND e.occurred_at >= $3 AND e.occurred_at < $4 AND e.actor_id IS NOT NULL
 AND e.actor_role NOT IN ('bridge','sync')
 ), people AS (
 SELECT sm.user_id FROM sales_members sm JOIN memberships m ON m.workspace_id=sm.workspace_id AND m.user_id=sm.user_id
 JOIN users u ON u.id=sm.user_id AND u.disabled_at IS NULL
 WHERE sm.workspace_id=$1 AND sm.brand_id=$2 AND sm.role IN ('sdr','ae','manager')
 AND (m.brand_ids IS NULL OR cardinality(m.brand_ids)=0 OR $2::uuid=ANY(m.brand_ids))
 UNION SELECT actor_id FROM events
 ), counts AS (
 SELECT actor_id,action,count(DISTINCT lead_id)::int AS n FROM events GROUP BY actor_id,action
 )
 SELECT p.user_id::text,u.name,
 COALESCE(NULLIF(sm.role,'none'),(SELECT e.actor_role FROM events e WHERE e.actor_id=p.user_id AND e.actor_role IN ('sdr','ae','manager') ORDER BY e.occurred_at DESC LIMIT 1),'none'),
 COALESCE(m.user_id IS NOT NULL AND u.disabled_at IS NULL AND sm.role IN ('sdr','ae','manager') AND (m.brand_ids IS NULL OR cardinality(m.brand_ids)=0 OR $2::uuid=ANY(m.brand_ids)),false),
 COALESCE((SELECT jsonb_object_agg(action,n) FROM counts c WHERE c.actor_id=p.user_id),'{}'::jsonb)
 FROM people p JOIN users u ON u.id=p.user_id
 LEFT JOIN memberships m ON m.workspace_id=$1 AND m.user_id=p.user_id
 LEFT JOIN sales_members sm ON sm.workspace_id=$1 AND sm.brand_id=$2 AND sm.user_id=p.user_id
 ORDER BY lower(u.name),p.user_id`, a.WorkspaceID, a.Brand, from, to)
	if s.allocError(w, r, err) {
		return
	}
	members, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (salesTeamReportMember, error) {
		var m salesTeamReportMember
		err := row.Scan(&m.UserID, &m.Name, &m.Role, &m.Active, &m.Counts)
		return m, err
	})
	if s.allocError(w, r, err) {
		return
	}
	if members == nil {
		members = []salesTeamReportMember{}
	}
	// STRPOS treats search text literally, including percent/underscore characters.
	rows, err = tx.Query(r.Context(), salesTeamEvidence+` SELECT e.id,e.lead_id::text,e.record_id::text,r.name,COALESCE(c.name,''),e.actor_id::text,COALESCE(u.name,''),e.actor_role,e.action,e.occurred_at
 FROM evidence e JOIN records r ON r.workspace_id=e.workspace_id AND r.brand_id=e.brand_id AND r.id=e.record_id AND r.deleted_at IS NULL AND r.merged_into IS NULL
 LEFT JOIN records c ON c.workspace_id=e.workspace_id AND c.brand_id=e.brand_id AND c.id::text=r.attributes->'companies'->>0 AND c.deleted_at IS NULL AND c.merged_into IS NULL
 LEFT JOIN users u ON u.id=e.actor_id
 WHERE e.workspace_id=$1 AND e.brand_id=$2 AND e.occurred_at >= $3 AND e.occurred_at < $4
 AND ($5::uuid IS NULL OR e.actor_id=$5) AND ($6='' OR e.action=$6)
 AND ($7='' OR strpos(lower(r.name),lower($7))>0 OR strpos(lower(COALESCE(c.name,'')),lower($7))>0)
 AND e.occurred_at <= $8 AND (e.occurred_at,e.id)<($8,$9::text)
 ORDER BY e.occurred_at DESC,e.id DESC LIMIT 51`, a.WorkspaceID, a.Brand, from, to, nullIfEmpty(user), action, search, cursor.At, cursor.ID)
	if s.allocError(w, r, err) {
		return
	}
	activity, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (salesTeamActivity, error) {
		var v salesTeamActivity
		err := row.Scan(&v.ID, &v.LeadID, &v.RecordID, &v.Name, &v.Company, &v.ActorID, &v.ActorName, &v.ActorRole, &v.Action, &v.OccurredAt)
		return v, err
	})
	if s.allocError(w, r, err) {
		return
	}
	if activity == nil {
		activity = []salesTeamActivity{}
	}
	var next *string
	if len(activity) > 50 {
		activity = activity[:50]
		last := activity[49]
		raw, _ := json.Marshal(salesActivityCursor{At: last.OccurredAt, ID: last.ID})
		value := base64.RawURLEncoding.EncodeToString(raw)
		next = &value
	}
	if s.allocError(w, r, tx.Commit(r.Context())) {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, 200, map[string]any{"from": from, "to": to, "members": members, "data": activity, "next_cursor": next})
}
