package api

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"math"
	"net/http"
	"strings"
	"time"
)

func (s *Server) coverageReport(ctx context.Context, a *access, from, to, filters string) (json.RawMessage, error) {
	now := time.Now().UTC()
	if to == "" {
		to = now.Format("2006-01-02")
	}
	if from == "" {
		from = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -11, 0).Format("2006-01-02")
	}
	first, e := time.Parse("2006-01-02", from)
	if e != nil {
		return nil, errInvalid("from must be YYYY-MM-DD")
	}
	last, e := time.Parse("2006-01-02", to)
	if e != nil || last.Before(first) || last.Sub(first) > 732*24*time.Hour {
		return nil, errInvalid("Choose a period up to two years, ending on or after its start")
	}
	args := []any{a.WorkspaceID, a.Brand, from, to, filters}
	where := "workspace_id=$1 AND brand_id=$2"
	extra, e := coverageFilterSQL(filters, coverageResources["placements"], &args)
	if e != nil {
		return nil, e
	}
	where += extra
	var parsed coverageFilters
	if json.Unmarshal([]byte(filters), &parsed) == nil && len(parsed.Rules) == 0 {
		filters = ""
		args[4] = ""
	}
	var data json.RawMessage
	err := s.pool.QueryRow(ctx, `WITH p AS (SELECT * FROM coverage_placements WHERE `+where+`),
 conclusive AS (SELECT placement_id,created_at,evidence->>'effective_health' AS health FROM coverage_observations WHERE workspace_id=$1 AND brand_id=$2 AND source='page_read' AND placement_id IN (SELECT id FROM p) AND created_at<$4::date+interval '1 day' AND evidence->>'effective_health' IN ('up','gone')),
 transitions AS (SELECT *,lag(health) OVER (PARTITION BY placement_id ORDER BY created_at) AS previous FROM conclusive),
 months AS (SELECT generate_series(date_trunc('month',$3::date),date_trunc('month',$4::date),interval '1 month') AS month)
 SELECT jsonb_build_object(
 'as_of',now(),'from',$3::text,'to',$4::text,'filters',$5::text,'placements',(SELECT count(*) FROM p),'publishers',(SELECT count(DISTINCT cp.record_id) FROM coverage_publishers cp JOIN p ON p.publisher_id=cp.id WHERE cp.workspace_id=$1 AND cp.brand_id=$2),
 'live',(SELECT count(*) FROM p WHERE health='up'),'unverified',(SELECT count(*) FROM p WHERE health IN ('unchecked','blocked','unreachable','redirected')),
 'lost',(SELECT count(*) FROM p WHERE health='gone'),'work',(SELECT count(*) FROM coverage_tasks WHERE workspace_id=$1 AND brand_id=$2 AND ($5='' OR placement_id IN (SELECT id FROM p)) AND (status IN ('open','in_progress') OR status='snoozed' AND snooze_until<=current_date)),
 'publisher_concentration',coalesce((SELECT jsonb_agg(x) FROM (SELECT cp.domain,count(*) AS placements FROM p JOIN coverage_publishers cp ON cp.id=p.publisher_id AND cp.workspace_id=$1 AND cp.brand_id=$2 GROUP BY cp.domain ORDER BY count(*) DESC LIMIT 20)x),'[]'),
 'by_stage',coalesce((SELECT jsonb_agg(x) FROM (SELECT stage,count(*) AS count FROM p GROUP BY stage)x),'[]'),'by_sentiment',coalesce((SELECT jsonb_agg(x) FROM (SELECT sentiment,count(*) AS count FROM p GROUP BY sentiment)x),'[]'),'by_kind',coalesce((SELECT jsonb_agg(x) FROM (SELECT kind,count(*) AS count FROM p GROUP BY kind ORDER BY count(*) DESC)x),'[]'),
 'by_arrangement',coalesce((SELECT jsonb_agg(x) FROM (SELECT arrangement,count(*) AS count FROM p GROUP BY arrangement)x),'[]'),
 'growth',coalesce((SELECT jsonb_agg(x) FROM (SELECT month::date::text AS month,(SELECT count(*) FROM p WHERE created_at>=greatest(m.month,$3::date) AND created_at<least(m.month+interval '1 month',$4::date+interval '1 day')) AS discovered,(SELECT count(*) FROM p WHERE first_verified_at>=greatest(m.month,$3::date) AND first_verified_at<least(m.month+interval '1 month',$4::date+interval '1 day')) AS first_verified,(SELECT count(*) FROM p WHERE baseline AND first_verified_at>=greatest(m.month,$3::date) AND first_verified_at<least(m.month+interval '1 month',$4::date+interval '1 day')) AS baseline_verified,(SELECT count(*) FROM p WHERE published_on>=greatest(m.month,$3::date) AND published_on<least(m.month+interval '1 month',$4::date+interval '1 day')) AS published,(SELECT count(*) FROM transitions t WHERE t.created_at>=greatest(m.month,$3::date) AND t.created_at<least(m.month+interval '1 month',$4::date+interval '1 day') AND t.health='gone' AND t.previous='up') AS lost,(SELECT count(*) FROM transitions t JOIN p ON p.id=t.placement_id WHERE t.created_at>=greatest(m.month,$3::date) AND t.created_at<least(m.month+interval '1 month',$4::date+interval '1 day') AND t.health='up' AND t.previous='gone' AND p.first_verified_at<t.created_at) AS regained,(SELECT count(*) FROM p WHERE (SELECT c.health FROM conclusive c WHERE c.placement_id=p.id AND c.created_at<m.month+interval '1 month' ORDER BY c.created_at DESC LIMIT 1)='up') AS last_confirmed_available FROM months m ORDER BY month)x),'[]'),
 'retention',coalesce((SELECT jsonb_agg(x) FROM (SELECT date_trunc('month',v.first_verified_at)::date::text AS cohort,d.days,count(*) AS eligible,count(*) FILTER(WHERE evidence.health='up') AS available,count(*) FILTER(WHERE evidence.health='gone') AS gone,count(*) FILTER(WHERE evidence.health IS NULL) AS unverified FROM p v CROSS JOIN (VALUES (30),(90),(180)) d(days) LEFT JOIN LATERAL (SELECT c.health FROM conclusive c WHERE c.placement_id=v.id AND c.created_at BETWEEN v.first_verified_at+make_interval(days=>d.days-7) AND v.first_verified_at+make_interval(days=>d.days+7) ORDER BY c.created_at DESC LIMIT 1) evidence ON true WHERE v.first_verified_at+make_interval(days=>d.days+7)<=$4::date+interval '1 day' GROUP BY date_trunc('month',v.first_verified_at),d.days ORDER BY cohort,d.days)x),'[]'),
 'reported_delivery',coalesce((SELECT jsonb_agg(x) FROM (SELECT d.source,sum(d.impressions) AS impressions,sum(d.clicks) AS clicks,sum(d.sessions) AS sessions,sum(d.leads) AS leads,sum(d.conversions) AS conversions,count(*) AS reports FROM coverage_delivery_reports d JOIN p ON p.id=d.placement_id WHERE d.workspace_id=$1 AND d.brand_id=$2 AND d.starts_on>=$3::date AND d.ends_on<=$4::date GROUP BY d.source)x),'[]'),'measurement_note','Discovery, publication and first verification are separate. Historical imports are not new publication. Missing referral and ad delivery data are unknown. Reported delivery counts are source claims, not deduplicated or causal attribution; overlapping reports can overlap. Historical segments use current placement metadata. Last-confirmed inventory is evidence as of each month, not continuous verification.')`, args...).Scan(&data)
	if err != nil {
		return nil, err
	}
	if data, err = s.coverageMomentum(ctx, a, where, args, data); err != nil {
		return nil, err
	}
	return coverageFound(ctx, s.pool, a, data)
}

// coverageFound adds what discovery has found to a report: every page that
// links to or mentions the brand, from its links, news, social, web
// searches and AI answers, whether or not anyone has reviewed it, so the
// overview shows the brand's whole footprint and not only the placements
// someone tracks.
func coverageFound(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, a *access, data json.RawMessage) (json.RawMessage, error) {
	var out json.RawMessage
	err := q.QueryRow(ctx, `WITH c AS (SELECT source,kind,status,created_at,lower(regexp_replace(split_part(split_part(url,'://',2),'/',1),'^www\.','')) AS host
 FROM coverage_candidates WHERE workspace_id=$1 AND brand_id=$2 AND status NOT IN ('dismissed','spam'))
 SELECT $3::jsonb || jsonb_build_object('found',jsonb_build_object(
 'pages',(SELECT count(*) FROM c),'sites',(SELECT count(DISTINCT host) FROM c),
 'new_28d',(SELECT count(*) FROM c WHERE created_at>now()-interval '28 days'),
 'unreviewed',(SELECT count(*) FROM c WHERE status='new'),
 'by_source',coalesce((SELECT jsonb_agg(x) FROM (SELECT source,count(*) AS pages,count(DISTINCT host) AS sites FROM c GROUP BY source ORDER BY count(*) DESC)x),'[]'),
 'top_sites',coalesce((SELECT jsonb_agg(x) FROM (SELECT host,count(*) AS pages FROM c GROUP BY host ORDER BY count(*) DESC,host LIMIT 20)x),'[]')))`,
		a.WorkspaceID, a.Brand, data).Scan(&out)
	return out, err
}

// coverageMomentum adds the CMO view to a report: are placements growing,
// and how fast (the last 28 days against the 28 before, weighted by links
// and traction), twelve weeks of new against lost, and the brand's share of
// voice against each tracked competitor.
func (s *Server) coverageMomentum(ctx context.Context, a *access, where string, args []any, data json.RawMessage) (json.RawMessage, error) {
	var extra struct {
		Momentum     map[string]any   `json:"momentum"`
		ShareOfVoice []map[string]any `json:"share_of_voice"`
	}
	var raw json.RawMessage
	err := s.pool.QueryRow(ctx, `WITH p AS (SELECT * FROM coverage_placements WHERE `+where+`),
 lost AS (SELECT o.placement_id,o.created_at FROM (SELECT placement_id,created_at,evidence->>'effective_health' AS health,lag(evidence->>'effective_health') OVER (PARTITION BY placement_id ORDER BY created_at) AS previous FROM coverage_observations WHERE workspace_id=$1 AND brand_id=$2 AND source='page_read' AND placement_id IN (SELECT id FROM p) AND evidence->>'effective_health' IN ('up','gone')) o WHERE o.health='gone' AND o.previous='up'),
 w AS (SELECT n, $4::date+interval '1 day'-make_interval(days=>28*(n+1)) AS starts, $4::date+interval '1 day'-make_interval(days=>28*n) AS ends FROM (VALUES (0),(1)) v(n) WHERE $3::text IS NOT NULL AND $5::text IS NOT NULL),
 win AS (SELECT w.n,
   (SELECT count(*) FROM p WHERE first_verified_at>=w.starts AND first_verified_at<w.ends) AS gained,
   (SELECT count(*) FROM lost l WHERE l.created_at>=w.starts AND l.created_at<w.ends) AS lost,
   (SELECT coalesce(sum(1+CASE WHEN link_status='present' THEN 1 ELSE 0 END+CASE traction WHEN 'steady' THEN 1 WHEN 'taking_off' THEN 2 ELSE 0 END),0) FROM p WHERE first_verified_at>=w.starts AND first_verified_at<w.ends) AS weight_gained,
   (SELECT coalesce(sum(1+CASE WHEN p.link_status='present' THEN 1 ELSE 0 END+CASE p.traction WHEN 'steady' THEN 1 WHEN 'taking_off' THEN 2 ELSE 0 END),0) FROM lost l JOIN p ON p.id=l.placement_id WHERE l.created_at>=w.starts AND l.created_at<w.ends) AS weight_lost
   FROM w),
 weeks AS (SELECT n, $4::date+interval '1 day'-make_interval(days=>7*(n+1)) AS starts, $4::date+interval '1 day'-make_interval(days=>7*n) AS ends FROM generate_series(0,11) n)
 SELECT jsonb_build_object(
  'momentum',(SELECT jsonb_build_object('days',28,
    'gained',max(gained) FILTER(WHERE n=0),'lost',max(lost) FILTER(WHERE n=0),'net',max(gained-lost) FILTER(WHERE n=0),
    'weighted_net',max(weight_gained-weight_lost) FILTER(WHERE n=0),
    'previous_gained',max(gained) FILTER(WHERE n=1),'previous_lost',max(lost) FILTER(WHERE n=1),'previous_net',max(gained-lost) FILTER(WHERE n=1),
    'previous_weighted_net',max(weight_gained-weight_lost) FILTER(WHERE n=1),
    'taking_off',(SELECT count(*) FROM p WHERE health='up' AND traction='taking_off'),
    'quiet',(SELECT count(*) FROM p WHERE health='up' AND traction='quiet')) FROM win),
  'weeks',coalesce((SELECT jsonb_agg(jsonb_build_object('week',starts::date::text,'gained',(SELECT count(*) FROM p WHERE first_verified_at>=weeks.starts AND first_verified_at<weeks.ends),'lost',(SELECT count(*) FROM lost l WHERE l.created_at>=weeks.starts AND l.created_at<weeks.ends)) ORDER BY n DESC) FROM weeks),'[]'),
  'share_of_voice',(SELECT jsonb_agg(x ORDER BY x.placements DESC) FROM (
    SELECT 'brand' AS who,NULL::uuid AS competitor_id,NULL::text AS name,(SELECT count(*) FROM p WHERE health='up') AS placements
    UNION ALL
    SELECT 'competitor',c.id,coalesce(nullif(c.name,''),c.host),(SELECT count(*) FROM coverage_competitor_placements cc WHERE cc.workspace_id=$1 AND cc.brand_id=$2 AND cc.competitor_id=c.id AND cc.gone_at IS NULL)
    FROM seo_competitors c WHERE c.workspace_id=$1 AND c.brand_id=$2 AND c.status='tracked') x))`, args...).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var parts struct {
		Momentum     map[string]any   `json:"momentum"`
		Weeks        []map[string]any `json:"weeks"`
		ShareOfVoice []map[string]any `json:"share_of_voice"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, err
	}
	extra.Momentum = parts.Momentum
	extra.Momentum["trend"] = momentumTrend(num(parts.Momentum["weighted_net"]), num(parts.Momentum["previous_weighted_net"]))
	extra.Momentum["weeks"] = parts.Weeks
	extra.ShareOfVoice = parts.ShareOfVoice
	if extra.ShareOfVoice == nil {
		extra.ShareOfVoice = []map[string]any{}
	}
	var total float64
	for _, r := range extra.ShareOfVoice {
		total += num(r["placements"])
	}
	for _, r := range extra.ShareOfVoice {
		share := 0.0
		if total > 0 {
			share = math.Round(num(r["placements"])/total*1000) / 10
		}
		r["share"] = share
	}
	var report map[string]any
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, err
	}
	report["momentum"] = extra.Momentum
	report["share_of_voice"] = extra.ShareOfVoice
	return json.Marshal(report)
}

// momentumTrend names the direction and pace: shrinking below zero, flat
// at zero, then growing, accelerating or slowing against the period before.
func momentumTrend(now, before float64) string {
	switch {
	case now < 0:
		return "shrinking"
	case now == 0:
		return "flat"
	case before <= 0:
		return "growing"
	case now/before >= 1.25:
		return "accelerating"
	case now/before >= 0.8:
		return "growing"
	}
	return "slowing"
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}
func (s *Server) coverageListSnapshots(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	s.coverageRows(w, r, `SELECT to_jsonb(x) FROM (SELECT id,title,created_at FROM coverage_report_snapshots WHERE workspace_id=$1 AND brand_id=$2 ORDER BY created_at DESC LIMIT 100)x`, a.WorkspaceID, a.Brand)
}
func (s *Server) coverageGetSnapshot(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !validUUID(r.PathValue("id")) {
		writeError(w, r, 404, "not_found", "not found")
		return
	}
	var data json.RawMessage
	err := s.pool.QueryRow(r.Context(), `SELECT to_jsonb(x)-'workspace_id' FROM coverage_report_snapshots x WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, r.PathValue("id")).Scan(&data)
	if s.coverageError(w, r, err) {
		return
	}
	writeJSON(w, 200, data)
}
func (s *Server) coverageSaveSnapshot(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Title   string `json:"title"`
		From    string `json:"from"`
		To      string `json:"to"`
		Filters string `json:"filters"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" || len(req.Title) > 500 {
		writeError(w, r, 400, "invalid_request", "Use a title of 1 to 500 characters")
		return
	}
	data, err := s.coverageReport(r.Context(), a, req.From, req.To, req.Filters)
	if s.coverageError(w, r, err) {
		return
	}
	var output json.RawMessage
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(r.Context(), `INSERT INTO coverage_report_snapshots AS x(workspace_id,brand_id,title,data) VALUES($1,$2,$3,$4) RETURNING id::text,to_jsonb(x)-'workspace_id'`, a.WorkspaceID, a.Brand, req.Title, data).Scan(&id, &output); err != nil {
			return err
		}
		return coverageHistory(r.Context(), tx, a, "report_snapshots", id, "created", nil)
	})
	if s.coverageError(w, r, err) {
		return
	}
	writeJSON(w, 201, output)
}
