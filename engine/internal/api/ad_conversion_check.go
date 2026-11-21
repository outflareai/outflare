package api

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// The daily check of conversions sent against what the ad network counts:
// for each conversion action a rule sends to, how many conversions
// Outflare sent each day and how many the network counted for that action,
// so a gap shows the day it opens instead of weeks later in a report.

// A network reports a conversion a day or two late, so the check ends two
// days ago; under adCheckMin sends that carried the network's own click
// id there is too little to judge; and a network that counts under half
// of them is flagged.
const (
	adCheckLagDays  = 2
	adCheckMin      = 10
	adCheckLowShare = 0.5
)

type adCheckDay struct {
	Day     string   `json:"day"`
	Sent    int      `json:"sent"`
	Counted *float64 `json:"counted"`
}

type adCheckRow struct {
	AdAccountID string `json:"ad_account_id"`
	AccountName string `json:"account_name"`
	Connector   string `json:"connector"`
	Action      string `json:"action"`
	// Sent is every conversion the network accepted; SentWithClickID those
	// that carried its own click id, the only ones it can count.
	Sent            int `json:"sent"`
	SentWithClickID int `json:"sent_with_click_id"`
	// Counted is what the network counts for the action over the same
	// days, null when it doesn't report by conversion action; Share is
	// Counted over SentWithClickID.
	Counted *float64 `json:"counted"`
	Share   *float64 `json:"share"`
	// ok, low (under half counted), none (nothing counted), too_few (not
	// enough sends to judge) or not_reported.
	Status string       `json:"status"`
	Days   []adCheckDay `json:"days"`
}

func (s *Server) adConversionCheck(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	days, ok := intParam(w, r, "days", 14, 7, 90)
	if !ok {
		return
	}
	to := time.Now().UTC().AddDate(0, 0, -adCheckLagDays)
	from := to.AddDate(0, 0, -(days - 1))
	f, t := from.Format(time.DateOnly), to.Format(time.DateOnly)
	rows, err := s.pool.Query(r.Context(), `
		WITH sent AS (
			SELECT r.ad_account_id, r.target_name, (q.occurred_at AT TIME ZONE 'UTC')::date AS day, count(*)::int AS n,
				count(*) FILTER (WHERE q.match_keys && ARRAY['gclid', 'gbraid', 'wbraid', 'fbc', 'msclkid', 'twclid', 'rdt_cid', 'ttclid', 'oppref', 'li_fat_id'])::int AS clicked
			FROM ad_conversion_sends q JOIN ad_conversion_rules r ON r.id = q.rule_id
			WHERE q.workspace_id = $1 AND r.brand_id = $2 AND q.status = 'sent' AND r.target_name <> ''
			  AND (q.occurred_at AT TIME ZONE 'UTC')::date BETWEEN $3::date AND $4::date
			GROUP BY 1, 2, 3),
		counted AS (
			SELECT e.ad_account_id, x.action, x.day, sum(x.conversions)::float8 AS n
			FROM ad_conversion_actions_daily x JOIN ad_entities e ON e.id = x.ad_entity_id
			WHERE x.workspace_id = $1 AND e.level = 'campaign' AND x.day BETWEEN $3::date AND $4::date
			  AND (e.ad_account_id, x.action) IN (SELECT ad_account_id, target_name FROM sent)
			GROUP BY 1, 2, 3),
		reports AS (
			SELECT DISTINCT e.ad_account_id FROM ad_conversion_actions_daily x JOIN ad_entities e ON e.id = x.ad_entity_id
			WHERE x.workspace_id = $1 AND x.day >= $3::date - 35)
		SELECT a.id, a.name, a.connector, k.target_name, to_char(k.day, 'YYYY-MM-DD'), COALESCE(s.n, 0), COALESCE(s.clicked, 0), c.n,
			EXISTS (SELECT 1 FROM reports p WHERE p.ad_account_id = a.id)
		FROM (SELECT ad_account_id, target_name, day FROM sent UNION SELECT ad_account_id, action, day FROM counted) k
		JOIN ad_accounts a ON a.id = k.ad_account_id
		LEFT JOIN sent s ON s.ad_account_id = k.ad_account_id AND s.target_name = k.target_name AND s.day = k.day
		LEFT JOIN counted c ON c.ad_account_id = k.ad_account_id AND c.action = k.target_name AND c.day = k.day
		ORDER BY a.connector, lower(a.name), a.id, lower(k.target_name), k.target_name, k.day DESC`, a.WorkspaceID, a.Brand, f, t)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := []adCheckRow{}
	var id, name, connector, action, day string
	var sent, clicked int
	var counted *float64
	var reports bool
	_, err = pgx.ForEachRow(rows, []any{&id, &name, &connector, &action, &day, &sent, &clicked, &counted, &reports}, func() error {
		if n := len(out); n == 0 || out[n-1].AdAccountID != id || out[n-1].Action != action {
			row := adCheckRow{AdAccountID: id, AccountName: name, Connector: connector, Action: action, Days: []adCheckDay{}}
			if reports {
				row.Counted = new(float64)
			}
			out = append(out, row)
		}
		x := &out[len(out)-1]
		d := adCheckDay{Day: day, Sent: sent}
		if x.Counted != nil {
			d.Counted = new(float64)
			if counted != nil {
				*d.Counted = *counted
				*x.Counted += *counted
			}
		}
		x.Sent += sent
		x.SentWithClickID += clicked
		x.Days = append(x.Days, d)
		return nil
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for i := range out {
		x := &out[i]
		switch {
		case x.Counted == nil:
			x.Status = "not_reported"
		case x.SentWithClickID < adCheckMin:
			x.Status = "too_few"
		case *x.Counted == 0:
			x.Status = "none"
		default:
			share := *x.Counted / float64(x.SentWithClickID)
			x.Share = &share
			x.Status = "ok"
			if share < adCheckLowShare {
				x.Status = "low"
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": f, "to": t, "data": out})
}
