package api

import (
	"context"
	"net/http"
	"net/url"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/searchinsights"
	"github.com/outflareai/outflare/engine/internal/seointel"
)

func (s *Server) refreshSearchInsights(w http.ResponseWriter, r *http.Request) {
	site, ok := s.insightSite(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	var req struct {
		IntegrationID string `json:"integration_id"`
		Page          string `json:"page"`
		Device        string `json:"device"`
	}
	if !decode(w, r, &req) {
		return
	}
	p := searchinsights.Request{IntegrationID: req.IntegrationID, Page: req.Page, Device: req.Device}
	if !s.validateSearchCollection(w, r, site, &p) {
		return
	}
	var e error
	p.WorkspaceID, p.BrandID, p.SiteID = a.WorkspaceID, a.Brand, site.ID
	e = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		_, e := jobs.Enqueue(r.Context(), tx, jobs.NewJob{Kind: "search_insights.refresh", WorkspaceID: &a.WorkspaceID, Payload: p})
		if e != nil {
			return e
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "search_insights.refresh_requested", "seo_site", site.ID, map[string]any{"brand_id": a.Brand, "integration_id": p.IntegrationID})
	})
	if e != nil {
		s.internal(w, r, e)
		return
	}
	writeJSON(w, 200, map[string]bool{"queued": true})
}
func (s *Server) insightVitals(ctx context.Context, ws, brand, site string, win searchWindow, out *seointel.InsightReport) error {
	rows, e := s.pool.Query(ctx, `SELECT page,device,day::text,metric,p75,good_share FROM search_vitals WHERE workspace_id=$1 AND brand_id=$2 AND site_id=$3 AND day BETWEEN $4 AND $5 ORDER BY page,device,metric,day`, ws, brand, site, win.from, win.to)
	if e != nil {
		return e
	}
	list, e := pgx.CollectRows(rows, func(r pgx.CollectableRow) (seointel.InsightRow, error) {
		var x seointel.InsightRow
		e := r.Scan(&x.Page, &x.Device, &x.Date, &x.Metric, &x.Value, &x.GoodShare)
		x.Key = x.Page + " · " + x.Device + " · " + x.Metric + " · " + x.Date
		x.Status = "measured"
		return x, e
	})
	if e != nil {
		return e
	}
	previous := map[string]float64{}
	for _, x := range list {
		key := x.Page + x.Device + x.Metric
		limit := 0.0
		switch x.Metric {
		case "largest_contentful_paint":
			limit = 2500
		case "interaction_to_next_paint":
			limit = 200
		case "cumulative_layout_shift":
			limit = .1
		}
		if limit > 0 {
			x.Status = "good"
			if x.Value > limit {
				x.Status = "needs_improvement"
			}
		}
		if p, ok := previous[key]; ok && p > 0 {
			x.Before = p
			x.Change = (x.Value - p) / p
			if x.Change >= .2 && x.Value > limit && limit > 0 {
				x.Status = "regressed"
			}
		}
		previous[key] = x.Value
		out.Data = append(out.Data, x)
	}
	sort.Slice(out.Data, func(i, j int) bool { return out.Data[i].Date > out.Data[j].Date })
	out.Notes = append(out.Notes, "CrUX field data uses overlapping 28-day windows, not individual-day measurements. Milliseconds for LCP/INP, unitless CLS. A 20% worsening above the good threshold is flagged as a regression. Low-traffic URLs may have no field data.")
	if len(list) == 0 {
		out.Status = "not_collected"
	}
	return nil
}
func (s *Server) insightUpdateContext(ctx context.Context, ws, brand, site string, win searchWindow, out *seointel.InsightReport) error {
	rows, e := s.pool.Query(ctx, `SELECT id,name,started_at::text,COALESCE(ended_at,CURRENT_DATE)::text FROM search_algorithm_updates WHERE workspace_id=$1 AND brand_id=$2 AND site_id=$3 AND started_at<=$5 AND COALESCE(ended_at,CURRENT_DATE)>=$4 ORDER BY started_at`, ws, brand, site, win.from, win.to)
	if e != nil {
		return e
	}
	updates, e := pgx.CollectRows(rows, func(r pgx.CollectableRow) (seointel.InsightRow, error) {
		var x seointel.InsightRow
		var end string
		e := r.Scan(&x.Key, &x.Detail, &x.Date, &end)
		x.Status = "google_update"
		x.Source = "https://status.search.google.com"
		x.Target = end
		return x, e
	})
	if e != nil {
		return e
	}
	out.Data = append(out.Data, updates...)
	rows, e = s.pool.Query(ctx, `SELECT id::text,deployed_at::date::text FROM deploys WHERE workspace_id=$1 AND brand_id=$2 AND site_id=$3 AND state='ready' AND deployed_at::date BETWEEN $4 AND $5 ORDER BY deployed_at`, ws, brand, site, win.from, win.to)
	if e != nil {
		return e
	}
	deploys, e := pgx.CollectRows(rows, func(r pgx.CollectableRow) (seointel.InsightRow, error) {
		var x seointel.InsightRow
		e := r.Scan(&x.Key, &x.Date)
		x.Status = "deploy"
		return x, e
	})
	out.Data = append(out.Data, deploys...)
	out.Notes = append(out.Notes, "Google updates and production deploys are timing context, not proof of cause. Refresh field data to collect the latest official Google update feed.")
	return e
}

func (s *Server) validateSearchCollection(w http.ResponseWriter, r *http.Request, site *seoSite, p *searchinsights.Request) bool {
	if !s.needIntegrations(w, r) {
		return false
	}
	a := accessFrom(r.Context())
	var e error
	if p.IntegrationID != "" {
		if !validUUID(p.IntegrationID) {
			writeError(w, r, 400, "invalid_request", "Choose your Chrome UX Report connection.")
			return false
		}
		o, e := s.integrations.Open(r.Context(), s.pool, a.WorkspaceID, p.IntegrationID)
		if e != nil {
			writeError(w, r, 404, "not_found", "connection not found")
			return false
		}
		all, e := s.integrations.List(r.Context(), s.pool, a.WorkspaceID, "")
		if e != nil {
			s.internal(w, r, e)
			return false
		}
		if !o.Serves(a.Brand, all) {
			writeError(w, r, 404, "not_found", "connection not found in this brand")
			return false
		}
		if _, ok := o.Connector.(connectors.WebVitalsReader); !ok {
			writeError(w, r, 400, "invalid_request", "Choose a Chrome UX Report connection.")
			return false
		}
	}
	if p.Device == "" {
		p.Device = "PHONE"
	}
	if p.Device != "PHONE" && p.Device != "DESKTOP" && p.Device != "TABLET" {
		writeError(w, r, 400, "invalid_request", "device is PHONE, DESKTOP or TABLET")
		return false
	}
	if p.Page == "" {
		p.Page = "https://" + site.Host
	}
	u, e := url.Parse(p.Page)
	if e != nil || !migrationURL(u, site.Host) || u.Scheme != "https" || u.RawQuery != "" {
		writeError(w, r, 400, "invalid_request", "Choose an https URL on this site without credentials, query or fragment.")
		return false
	}

	return true
}
