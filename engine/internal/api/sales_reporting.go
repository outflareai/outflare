package api

import (
	"encoding/json"
	"fmt"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/leadportal"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

var salesReportStages = []string{"discovered", "researched", "verified", "emailed", "replied", "sdr", "closed"}

func salesReportRange(r *http.Request, now time.Time) (time.Time, time.Time, string, error) {
	q := r.URL.Query()
	period := q.Get("period")
	if period == "" {
		period = "24h"
	}
	n, ok := map[string]int{"24h": 24, "48h": 48, "7d": 7, "30d": 30, "365d": 365}[period]
	if !ok {
		return time.Time{}, time.Time{}, "", fmt.Errorf("invalid period")
	}
	step, bucket := time.Hour, "hour"
	if strings.HasSuffix(period, "d") {
		step = 24 * time.Hour
		bucket = "day"
	}
	to := now.UTC().Truncate(step)
	from := to.Add(-time.Duration(n) * step)
	if q.Has("from") || q.Has("to") {
		var err error
		from, err = time.Parse(time.RFC3339, q.Get("from"))
		if err != nil {
			return from, to, bucket, err
		}
		to, err = time.Parse(time.RFC3339, q.Get("to"))
		if err != nil {
			return from, to, bucket, err
		}
	}
	if !to.After(from) || to.Sub(from) > 366*24*time.Hour || to.After(now.Add(time.Minute)) {
		return from, to, bucket, fmt.Errorf("use a past range of at most 366 days")
	}
	return from, to, bucket, nil
}
func (s *Server) getSalesReporting(w http.ResponseWriter, r *http.Request) {
	if !s.salesReportingAllowed(w, r) {
		return
	}
	a := accessFrom(r.Context())
	from, to, bucket, err := salesReportRange(r, time.Now())
	if err != nil {
		writeError(w, r, 400, "invalid_request", err.Error())
		return
	}
	detail := strings.HasSuffix(r.URL.Path, "/leads")
	q := r.URL.Query()
	offset := 0
	if q.Has("offset") {
		offset, err = strconv.Atoi(q.Get("offset"))
		if err != nil || offset < 0 || offset > 1000000 {
			writeError(w, r, 400, "invalid_request", "Invalid page offset")
			return
		}
	}
	selected := []string{}
	if detail {
		for _, v := range strings.Split(q.Get("stages"), ",") {
			if !slices.Contains(salesReportStages, v) {
				writeError(w, r, 400, "invalid_request", "Choose valid stages")
				return
			}
			if !slices.Contains(selected, v) {
				selected = append(selected, v)
			}
		}
	}
	source := map[string]any{"status": "missing", "connections": []any{}}
	var connection *connectors.Integration
	if s.integrations != nil {
		list, e := s.integrations.List(r.Context(), s.pool, a.WorkspaceID, "")
		if e != nil {
			s.internal(w, r, e)
			return
		}
		eligible := []connectors.Integration{}
		choices := []any{}
		for _, i := range list { // A portal represents a single brand. Shared connections cannot expose its whole dataset.
			if i.Connector == leadportal.ID && i.BrandID != nil && *i.BrandID == a.Brand {
				eligible = append(eligible, i)
				choices = append(choices, map[string]string{"id": i.ID, "name": i.Name})
			}
		}
		source["connections"] = choices
		if q.Get("integration") != "" {
			for _, i := range eligible {
				if i.ID == q.Get("integration") {
					v := i
					connection = &v
				}
			}
			if connection == nil {
				writeError(w, r, 404, "not_found", "Reporting connection not found for this brand")
				return
			}
		} else if len(eligible) == 1 {
			connection = &eligible[0]
		} else if len(eligible) > 1 {
			source["status"] = "choose"
		}
	}
	// The discovery lane board lives only in the connected portal: there is no native part to combine.
	if strings.HasSuffix(r.URL.Path, "/lanes") {
		status := q.Get("status")
		if status != "" && !salesLaneStatus.MatchString(status) {
			writeError(w, r, 400, "invalid_request", "Choose a valid lane status")
			return
		}
		trace := q.Get("trace") == "true"
		if q.Has("trace") && q.Get("trace") != "true" && q.Get("trace") != "false" {
			writeError(w, r, 400, "invalid_request", "Choose a valid trace option")
			return
		}
		for _, key := range []string{"mission", "lane", "run"} {
			if q.Has(key) && !salesTraceID.MatchString(q.Get(key)) {
				writeError(w, r, 400, "invalid_request", "Choose valid discovery IDs")
				return
			}
		}
		if trace && offset > 100000 {
			writeError(w, r, 400, "invalid_request", "Trace offset is too large")
			return
		}
		page := map[string]any{"source": source, "lanes": nil, "trace": nil}
		if connection != nil {
			source["status"] = "connected"
			source["integration_id"] = connection.ID
			opened, e := s.integrations.Open(r.Context(), s.pool, a.WorkspaceID, connection.ID)
			if e != nil {
				s.internal(w, r, e)
				return
			}
			args := url.Values{"lanes": {"1"}, "offset": {strconv.Itoa(offset)}}
			if status != "" {
				args.Set("lane_status", status)
			}
			if trace {
				args.Del("lanes")
				args.Set("trace", "1")
				for _, key := range []string{"mission", "lane", "run"} {
					if q.Has(key) {
						args.Set(key, q.Get(key))
					}
				}
			}
			response, e := (leadportal.Connector{}).Read(r.Context(), opened.Client, opened.Creds, args)
			if e != nil {
				writeError(w, r, 502, "connector_unavailable", "Lead Portal discovery lanes could not be read. Check its connection and retry.")
				return
			}
			page["lanes"] = response["lanes"]
			page["trace"] = response["trace"]
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, 200, page)
		return
	}
	out := map[string]any{"schema_version": 1, "generated_at": time.Now().UTC(), "from": from, "to": to, "bucket": bucket, "buckets": []any{}, "totals": map[string]any{}, "cohort": nil, "tools": nil, "capacity": nil, "discovery": nil, "groups": []any{}}
	upstream := []string{}
	for _, v := range selected {
		if v != "sdr" && v != "closed" {
			upstream = append(upstream, v)
		}
	}
	if connection != nil {
		source["status"] = "connected"
		source["integration_id"] = connection.ID
		if !detail || len(upstream) > 0 {
			opened, e := s.integrations.Open(r.Context(), s.pool, a.WorkspaceID, connection.ID)
			if e != nil {
				s.internal(w, r, e)
				return
			}
			args := url.Values{"from": {from.Format(time.RFC3339)}, "to": {to.Format(time.RFC3339)}, "period": {q.Get("period")}}
			if args.Get("period") == "" {
				args.Set("period", "24h")
			}
			if detail {
				args.Set("stages", strings.Join(upstream, ","))
				args.Set("offset", strconv.Itoa(offset))
			}
			response, e := (leadportal.Connector{}).Read(r.Context(), opened.Client, opened.Creds, args)
			if e != nil {
				writeError(w, r, 502, "connector_unavailable", "Lead Portal reporting could not be read. Check its connection and retry.")
				return
			}
			for k, v := range response {
				out[k] = v
			}
		}
	}
	// Native results never infer a lost deal from an archive or a payment from a deal value.
	native, err := s.nativeSalesReporting(r, from, to, bucket, selected, offset, detail)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.enrichSalesReport(r.Context(), a, out, native, connection, detail)
	if detail {
		groups, _ := out["groups"].([]any)
		more, _ := native["groups"].([]any)
		groups = append(groups, more...)
		out["groups"] = groups
	} else {
		counts, _ := out["totals"].(map[string]any)
		if counts == nil {
			counts = map[string]any{}
		}
		nt, _ := native["totals"].(map[string]any)
		for k, v := range nt {
			counts[k] = v
		}
		out["totals"] = counts
		all := map[string]map[string]any{}
		for _, src := range []any{out["buckets"], native["buckets"]} {
			xs, _ := src.([]any)
			for _, x := range xs {
				b := x.(map[string]any)
				key := b["at"].(string)
				tm, e := time.Parse(time.RFC3339, key)
				if e != nil {
					continue
				}
				key = tm.UTC().Format(time.RFC3339)
				if all[key] == nil {
					all[key] = map[string]any{}
				}
				cs, _ := b["counts"].(map[string]any)
				for k, v := range cs {
					all[key][k] = v
				}
			}
		}
		keys := make([]string, 0, len(all))
		for k := range all {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		bs := []any{}
		for _, k := range keys {
			bs = append(bs, map[string]any{"at": k, "counts": all[k]})
		}
		out["buckets"] = bs
		out["results"] = native["results"]
	}
	out["source"] = source
	out["schema_version"] = 1
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, 200, out)
}

var salesLaneStatus = regexp.MustCompile(`^[a-z_]{1,40}$`)

const salesReportingNative = `WITH milestones AS (
 SELECT l.id,l.record_id,l.owner_id,l.amount_minor,l.currency,l.state,r.name,l.source,l.source_id,
 (SELECT min(e.occurred_at) FROM sales_events e WHERE e.workspace_id=l.workspace_id AND e.brand_id=l.brand_id AND e.lead_id=l.id AND e.action='lead_taken' AND e.actor_role='sdr') AS picked,
 (SELECT min(e.occurred_at) FROM sales_events e WHERE e.workspace_id=l.workspace_id AND e.brand_id=l.brand_id AND e.lead_id=l.id AND e.action='customer_confirmed') AS closed
 FROM sales_leads l JOIN records r ON r.id=l.record_id AND r.workspace_id=l.workspace_id AND r.brand_id=l.brand_id
 WHERE l.workspace_id=$1 AND l.brand_id=$2 AND r.deleted_at IS NULL AND r.merged_into IS NULL
), events AS (SELECT m.*,x.stage,x.at FROM milestones m CROSS JOIN LATERAL(VALUES('sdr',picked),('closed',closed)) x(stage,at) WHERE x.at >=$3 AND x.at<$4)`

func (s *Server) nativeSalesReporting(r *http.Request, from, to time.Time, bucket string, stages []string, offset int, detail bool) (map[string]any, error) {
	a := accessFrom(r.Context())
	var raw []byte
	var err error
	if detail {
		err = s.pool.QueryRow(r.Context(), salesReportingNative+`, ranked AS(SELECT *,row_number() OVER(PARTITION BY stage ORDER BY at DESC,id) n FROM events)
 SELECT jsonb_build_object('groups',coalesce(jsonb_agg(jsonb_build_object('stage',s.stage,'total',(SELECT count(*) FROM events e WHERE e.stage=s.stage),'offset',$6::int,
 'leads',coalesce((SELECT jsonb_agg(jsonb_build_object('id',v.id,'record_id',v.record_id,'company',v.name,'status',v.state,'at',v.at,'amount_minor',v.amount_minor,'currency',v.currency,'owner_id',v.owner_id,'owner_name',(SELECT name FROM users WHERE id=v.owner_id),'origin','outflare','source',v.source,'source_id',v.source_id) ORDER BY v.n) FROM ranked v WHERE v.stage=s.stage AND v.n>$6 AND v.n<=$6+50),'[]'::jsonb))),'[]'::jsonb)) FROM unnest($5::text[]) s(stage) WHERE s.stage IN ('sdr','closed')`, a.WorkspaceID, a.Brand, from, to, stages, offset).Scan(&raw)
	} else {
		err = s.pool.QueryRow(r.Context(), salesReportingNative+`, counts AS(SELECT date_trunc($5,at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' at,stage,count(*) n FROM events GROUP BY 1,2), buckets AS(SELECT at,jsonb_object_agg(stage,n) counts FROM counts GROUP BY at), amounts AS(SELECT currency,sum(amount_minor) FILTER(WHERE amount_minor IS NOT NULL) amount_minor,count(*) FILTER(WHERE amount_minor IS NOT NULL) valued,count(*) FILTER(WHERE amount_minor IS NULL) unvalued FROM events WHERE stage='closed' GROUP BY currency)
 SELECT jsonb_build_object('source_outcomes',coalesce((SELECT jsonb_agg(jsonb_build_object('source',source,'source_id',source_id,'stage',stage)) FROM events),'[]'::jsonb),'buckets',coalesce((SELECT jsonb_agg(to_jsonb(b) ORDER BY at) FROM buckets b),'[]'::jsonb),'totals',jsonb_build_object('sdr',(SELECT count(*) FROM events WHERE stage='sdr'),'closed',(SELECT count(*) FROM events WHERE stage='closed')),'results',jsonb_build_object('amounts',coalesce((SELECT jsonb_agg(to_jsonb(v)) FROM amounts v),'[]'::jsonb),'payments_available',false,'lost_available',false))`, a.WorkspaceID, a.Brand, from, to, bucket).Scan(&raw)
	}
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = json.Unmarshal(raw, &out)
	return out, err
}

var salesTraceID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (s *Server) salesReportingAllowed(w http.ResponseWriter, r *http.Request) bool {
	a := accessFrom(r.Context())
	allowed := auth.Grants(a.Scopes, auth.ScopeMembersManage)
	if !allowed && a.UserID != "" && a.TokenID == "" && a.ViewID == "" {
		m, e := salesMemberAt(r.Context(), s.pool, a, a.UserID)
		if e != nil {
			s.allocError(w, r, e)
			return false
		}
		allowed = m.Role == "manager"
	}
	if !allowed {
		writeError(w, r, 403, "forbidden", "Sales reporting requires a Sales manager or workspace owner")
		return false
	}
	return true
}
