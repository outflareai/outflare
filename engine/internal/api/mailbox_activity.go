package api

import (
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// mailboxDay is what one mailbox did on one UTC day.
type mailboxDay struct {
	Date    string `json:"date"`
	Sent    int    `json:"sent"`
	Replies int    `json:"replies"`
	Bounced int    `json:"bounced"`
}

// mailboxActivity returns a mailbox's sent, replies and bounces per day:
// Outflare's own sends plus what connected sequencers report
// (mailbox_platform_days). Every day in the range is there, oldest first.
// Replies a sequencer also brought into the inbox are counted once.
func (s *Server) mailboxActivity(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	m, ok := s.findMailbox(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	days := 14
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 90 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "days must be a number from 1 to 90")
			return
		}
		days = n
	}
	rows, err := s.pool.Query(r.Context(), `
		WITH d AS (SELECT generate_series((now() AT TIME ZONE 'UTC')::date - ($3::int - 1), (now() AT TIME ZONE 'UTC')::date, interval '1 day')::date AS day),
		own AS (SELECT (created_at AT TIME ZONE 'UTC')::date AS day, count(*)::int AS n FROM messages
		        WHERE mailbox_id = $2 AND status = 'sent' AND created_at > now() - make_interval(days => $3::int + 1) GROUP BY 1),
		bnc AS (SELECT (updated_at AT TIME ZONE 'UTC')::date AS day, count(*)::int AS n FROM enrollments
		        WHERE mailbox_id = $2 AND status = 'bounced' AND updated_at > now() - make_interval(days => $3::int + 1) GROUP BY 1),
		rep AS (SELECT (received_at AT TIME ZONE 'UTC')::date AS day, count(*)::int AS n FROM inbound_messages
		        WHERE mailbox_id = $2 AND kind = 'reply' AND received_at > now() - make_interval(days => $3::int + 1) GROUP BY 1),
		seq AS (SELECT day, sum(sent)::int AS sent, sum(replies)::int AS replies, sum(bounced)::int AS bounced FROM mailbox_platform_days
		        WHERE workspace_id = $1 AND mailbox_id = $2 GROUP BY 1)
		SELECT d.day::text, COALESCE(own.n, 0) + COALESCE(seq.sent, 0), GREATEST(COALESCE(rep.n, 0), COALESCE(seq.replies, 0)),
		       COALESCE(bnc.n, 0) + COALESCE(seq.bounced, 0)
		FROM d LEFT JOIN own USING (day) LEFT JOIN bnc USING (day) LEFT JOIN rep USING (day) LEFT JOIN seq USING (day)
		ORDER BY d.day`, a.WorkspaceID, m.ID, days)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (mailboxDay, error) {
		var x mailboxDay
		return x, row.Scan(&x.Date, &x.Sent, &x.Replies, &x.Bounced)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}
