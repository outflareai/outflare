package api

import (
	"encoding/json"
	"github.com/outflareai/outflare/engine/internal/auth"
	"net/http"
)

func (s *Server) coverageOutcomes(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !auth.Grants(a.Scopes, auth.ScopeAnalyticsRead) {
		writeError(w, r, 403, "forbidden", "analytics.read is required for recorded attribution")
		return
	}
	f, ok := s.attributionQuery(w, r)
	if !ok {
		return
	}
	var data json.RawMessage
	err := s.pool.QueryRow(r.Context(), `WITH hosts AS (
 SELECT DISTINCT regexp_replace(lower(domain),'^www[.]','') AS domain FROM coverage_publishers WHERE workspace_id=$1 AND brand_id=$2 AND status<>'archived'),
 sessions AS (SELECT s.* FROM site_sessions s WHERE s.workspace_id=$1 AND s.brand_id=$2),
 source_sessions AS (SELECT h.domain,s.id,s.started_at FROM hosts h JOIN sessions s ON regexp_replace(lower(s.referrer_host),'^www[.]','')=h.domain),
 credits AS (SELECT ss.domain,c.name,c.currency,cr.weight,cr.weight*c.value_minor AS value_minor FROM attribution_credits cr JOIN attribution_conversions c ON c.event_id=cr.event_id AND c.workspace_id=cr.workspace_id JOIN source_sessions ss ON ss.id=cr.session_id WHERE cr.workspace_id=$1 AND c.brand_id=$2 AND cr.model=$5 AND c.occurred_at>=$3::date AND c.occurred_at<$4::date+interval '1 day'),
 revenue AS (SELECT domain,currency,round(sum(value_minor)) AS amount_minor FROM credits WHERE currency IS NOT NULL AND currency<>'' AND value_minor IS NOT NULL GROUP BY domain,currency)
 SELECT jsonb_build_object('from',$3::text,'to',$4::text,'model',$5::text,'lookback_days',$6::int,'attribution_refreshed_at',$7::timestamptz,
 'measurement_state',CASE WHEN EXISTS(SELECT 1 FROM sessions) THEN 'recorded' ELSE 'no_sessions_observed' END,
 'data',coalesce((SELECT jsonb_agg(x) FROM (SELECT h.domain,(SELECT count(*) FROM source_sessions ss WHERE ss.domain=h.domain AND ss.started_at>=$3::date AND ss.started_at<$4::date+interval '1 day') AS sessions,coalesce((SELECT sum(weight) FROM credits WHERE domain=h.domain AND name<>'payment.refunded'),0) AS credited_conversions,coalesce((SELECT jsonb_agg(jsonb_build_object('currency',currency,'amount_minor',amount_minor)) FROM revenue WHERE domain=h.domain),'[]') AS revenue FROM hosts h ORDER BY h.domain)x),'[]'),
 'measurement_note','Observed referrer-host sessions and the selected Outflare attribution model. Publisher websites are deduplicated across placements. Referrers can be absent; indexed backlinks and paid reports do not establish attributed outcomes. Conversions use their occurrence date; eligible touches may precede this reporting period. Figures describe recorded attribution, not causal lift.')`, a.WorkspaceID, a.Brand, f.from, f.to, f.model, f.settings.LookbackDays, f.settings.BackfilledAt).Scan(&data)
	if s.coverageError(w, r, err) {
		return
	}
	writeJSON(w, 200, data)
}
