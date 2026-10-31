package api

import (
	"github.com/jackc/pgx/v5"
	"net/http"
	"time"
)

type reviewRequestView struct {
	ID         string     `json:"id"`
	RecordID   string     `json:"record_id"`
	Customer   string     `json:"customer"`
	CampaignID string     `json:"campaign_id"`
	Campaign   string     `json:"campaign"`
	Platforms  []string   `json:"platforms"`
	Status     string     `json:"status"`
	Reason     string     `json:"reason"`
	CreatedAt  time.Time  `json:"created_at"`
	SentAt     *time.Time `json:"sent_at"`
	ClickedAt  *time.Time `json:"clicked_at"`
	EmailID    *string    `json:"email_id"`
}

func (s *Server) listReviewRequests(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	campaign, status, after := q.Get("campaign"), q.Get("status"), q.Get("after")
	if campaign != "" && !validUUID(campaign) || after != "" && !validUUID(after) {
		writeError(w, r, 400, "invalid_request", "campaign and after must be valid ids")
		return
	}
	from := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Now().Add(time.Minute)
	for _, v := range []struct {
		name string
		dst  *time.Time
	}{{"from", &from}, {"to", &to}} {
		if text := q.Get(v.name); text != "" {
			parsed, err := time.Parse(time.RFC3339, text)
			if err != nil {
				writeError(w, r, 400, "invalid_request", "from and to must be RFC3339 dates")
				return
			}
			*v.dst = parsed
		}
	}
	if !from.Before(to) {
		writeError(w, r, 400, "invalid_request", "from must precede to")
		return
	}
	// Review-link clicks are counted from destination URLs only, excluding
	// unsubscribe links and security-scanner clicks, unlike email-wide clicks.
	const base = ` FROM review_requests r JOIN review_request_campaigns c ON c.id=r.campaign_id AND c.workspace_id=r.workspace_id AND c.brand_id=r.brand_id JOIN records p ON p.id=r.record_id AND p.workspace_id=r.workspace_id AND p.brand_id=r.brand_id LEFT JOIN emails e ON e.id=r.email_id AND e.workspace_id=r.workspace_id LEFT JOIN LATERAL(SELECT min(lc.clicked_at) AS clicked_at FROM tracked_links tl JOIN link_clicks lc ON lc.link_id=tl.id WHERE tl.email_id=e.id AND lc.workspace_id=r.workspace_id AND NOT lc.likely_bot AND EXISTS(SELECT 1 FROM jsonb_array_elements(r.destinations) d WHERE d->>'url'=tl.url)) clicks ON true WHERE r.workspace_id=$1 AND r.brand_id=$2 AND ($3='' OR c.id::text=$3) AND r.created_at>=$4 AND r.created_at<$5`
	const state = `CASE WHEN r.status IN ('skipped','failed','pending') THEN r.status WHEN e.status='suppressed' THEN 'skipped' ELSE COALESCE(e.status,r.status) END`
	args := []any{a.WorkspaceID, a.Brand, campaign, from, to, status, after}
	rows, err := s.pool.Query(r.Context(), `SELECT r.id::text,r.record_id::text,p.name,c.id::text,c.config->>'name',r.platforms,`+state+`,COALESCE(NULLIF(r.reason,''),e.status_reason,''),r.created_at,e.sent_at,clicks.clicked_at,r.email_id::text`+base+` AND ($6='' OR (`+state+`)=$6 OR ($6='clicked' AND clicks.clicked_at IS NOT NULL)) AND ($7='' OR (r.created_at,r.id)<(SELECT created_at,id FROM review_requests WHERE workspace_id=$1 AND brand_id=$2 AND id=NULLIF($7,'')::uuid)) ORDER BY r.created_at DESC,r.id DESC LIMIT 101`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (reviewRequestView, error) {
		var v reviewRequestView
		err := row.Scan(&v.ID, &v.RecordID, &v.Customer, &v.CampaignID, &v.Campaign, &v.Platforms, &v.Status, &v.Reason, &v.CreatedAt, &v.SentAt, &v.ClickedAt, &v.EmailID)
		return v, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []reviewRequestView{}
	}
	next := ""
	if len(list) > 100 {
		list = list[:100]
		next = list[99].ID
	}
	report := map[string]any{}
	var total, sent, delivered, clicked, skipped, failed, pending int
	err = s.pool.QueryRow(r.Context(), `SELECT count(*)::int,count(*) FILTER(WHERE e.sent_at IS NOT NULL)::int,count(*) FILTER(WHERE e.delivered_at IS NOT NULL)::int,count(*) FILTER(WHERE clicks.clicked_at IS NOT NULL)::int,count(*) FILTER(WHERE r.status='skipped' OR e.status='suppressed')::int,count(*) FILTER(WHERE r.status='failed' OR e.status='failed')::int,count(*) FILTER(WHERE r.status='pending' OR e.status IN ('queued','scheduled','sending'))::int`+base, args[:5]...).Scan(&total, &sent, &delivered, &clicked, &skipped, &failed, &pending)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	type platformClicks struct {
		Platform string `json:"platform"`
		Clickers int    `json:"clickers"`
	}
	byPlatform := []platformClicks{}
	rows, err = s.pool.Query(r.Context(), `SELECT d->>'platform',count(DISTINCT r.record_id)::int FROM review_requests r CROSS JOIN LATERAL jsonb_array_elements(r.destinations) d JOIN tracked_links tl ON tl.email_id=r.email_id AND tl.url=d->>'url' JOIN link_clicks lc ON lc.link_id=tl.id AND lc.workspace_id=r.workspace_id AND NOT lc.likely_bot WHERE r.workspace_id=$1 AND r.brand_id=$2 AND ($3='' OR r.campaign_id::text=$3) AND r.created_at>=$4 AND r.created_at<$5 GROUP BY d->>'platform'`, args[:5]...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	byPlatform, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (platformClicks, error) {
		var v platformClicks
		err := row.Scan(&v.Platform, &v.Clickers)
		return v, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if byPlatform == nil {
		byPlatform = []platformClicks{}
	}
	report = map[string]any{"total": total, "sent": sent, "delivered": delivered, "clicked": clicked, "skipped": skipped, "failed": failed, "pending": pending, "platforms": byPlatform}
	writeJSON(w, 200, map[string]any{"data": list, "next": next, "report": report})
}
