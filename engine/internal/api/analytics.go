package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/analyticsync"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Analytics reads connected web analytics, product analytics and search
// consoles through one shape (connectors.AnalyticsReader). Outflare stores
// none of it beyond a short cache, so the numbers are always the
// provider's own.

func (s *Server) routesAnalytics(mux *http.ServeMux) {
	read := auth.ScopeAnalyticsRead
	mux.HandleFunc("GET /v1/workspaces/{ws}/analytics", s.inWorkspace(read, s.listAnalytics))
	mux.HandleFunc("GET /v1/workspaces/{ws}/analytics/summary", s.inWorkspace(read, s.analyticsSummary))
	mux.HandleFunc("GET /v1/workspaces/{ws}/analytics/{id}/sites", s.inBrand(read, s.analyticsSites))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/analytics/{id}/site", s.inWorkspace(auth.ScopeIntegrationsManage, s.setAnalyticsSite))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/analytics/{id}/sites/{site}", s.inBrand(auth.ScopeIntegrationsManage, s.updateAnalyticsSite))
	mux.HandleFunc("GET /v1/workspaces/{ws}/analytics/{id}/reports/{report}", s.inWorkspace(read, s.analyticsReport))
}

// analyticsCacheFor is how long an answer is kept unless the connector
// asks for longer.
const analyticsCacheFor = 15 * time.Minute

type analyticsSource struct {
	ID            string             `json:"id"`
	Name          string             `json:"name"`
	Connector     string             `json:"connector"`
	ConnectorName string             `json:"connector_name"`
	Status        string             `json:"status"`
	LastError     *string            `json:"last_error"`
	Account       connectors.Account `json:"account"`
	// Site is the default property, site or project; empty until one is
	// picked, unless the account has only one.
	Site    string   `json:"site"`
	Reports []string `json:"reports"`
}

func siteOf(i connectors.Integration) string {
	var cfg struct {
		Site string `json:"site"`
	}
	_ = json.Unmarshal(i.Config, &cfg)
	return cfg.Site
}

// analyticsSources lists the analytics connections a brand filter's brand
// uses.
func (s *Server) analyticsSources(ctx context.Context, ws string, sel brandSel) ([]analyticsSource, error) {
	list, err := s.integrations.List(ctx, s.pool, ws, "")
	if err != nil {
		return nil, err
	}
	list = integrationsFor(list, sel)
	out := []analyticsSource{}
	for _, i := range list {
		c, ok := s.integrations.Registry.Get(i.Connector)
		if !ok {
			continue
		}
		r, ok := c.(connectors.AnalyticsReader)
		if !ok {
			continue
		}
		out = append(out, analyticsSource{ID: i.ID, Name: i.Name, Connector: i.Connector, ConnectorName: c.Definition().Name,
			Status: i.Status, LastError: i.LastError, Account: i.Account, Site: siteOf(i), Reports: r.Reports()})
	}
	return out, nil
}

func (s *Server) listAnalytics(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	out, err := s.analyticsSources(r.Context(), accessFrom(r.Context()).WorkspaceID, sel)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// siteInBrand reports whether a connection's site belongs to a brand
// filter's brand, by the site's own brand, the connection's, or its domain.
func (s *Server) siteInBrand(ctx context.Context, ws, integration, site string, sel brandSel) (bool, error) {
	if sel.all() {
		return true, nil
	}
	var brand *string
	err := s.pool.QueryRow(ctx, `SELECT analytics_site_brand($1, $2, $3,
		COALESCE((SELECT host FROM analytics_syncs WHERE integration_id = $2 AND site = $3 LIMIT 1), ''))::text`, ws, integration, site).Scan(&brand)
	return sel.matches(brand), err
}

// openAnalytics loads an analytics integration with its credentials.
func (s *Server) openAnalytics(w http.ResponseWriter, r *http.Request) (*connectors.Opened, connectors.AnalyticsReader, bool) {
	if !s.needIntegrations(w, r) {
		return nil, nil, false
	}
	i, ok := s.loadIntegration(w, r)
	if !ok {
		return nil, nil, false
	}
	c, _ := s.integrations.Registry.Get(i.Connector)
	reader, isReader := c.(connectors.AnalyticsReader)
	if !isReader {
		writeError(w, r, http.StatusBadRequest, "not_analytics", i.Name+" is not an analytics connection; GET /v1/workspaces/{ws}/analytics lists them")
		return nil, nil, false
	}
	o, err := s.integrations.Open(r.Context(), s.pool, i.WorkspaceID, i.ID)
	if err != nil {
		if connectors.KindOf(err) != connectors.ErrUnavailable {
			connectorError(w, r, err)
		} else {
			s.internal(w, r, err)
		}
		return nil, nil, false
	}
	return o, reader, true
}

// called records a failed provider call on the integration, so a revoked
// key shows on the Connectors page, then answers with the explanation.
func (s *Server) analyticsFailed(w http.ResponseWriter, r *http.Request, o *connectors.Opened, err error) {
	_ = s.integrations.Checked(r.Context(), s.pool, o.WorkspaceID, o.ID, nil, err)
	connectorError(w, r, err)
}

func cacheTTL(reader connectors.AnalyticsReader) time.Duration {
	if c, ok := reader.(connectors.AnalyticsCacher); ok && c.CacheFor() > analyticsCacheFor {
		return c.CacheFor()
	}
	return analyticsCacheFor
}

// cached returns a kept answer younger than ttl.
func (s *Server) cached(ctx context.Context, o *connectors.Opened, key string, ttl time.Duration, v any) (time.Time, bool) {
	var raw []byte
	var at time.Time
	err := s.pool.QueryRow(ctx, `SELECT report, fetched_at FROM analytics_cache
		WHERE workspace_id = $1 AND integration_id = $2 AND key = $3 AND fetched_at > now() - make_interval(secs => $4)`,
		o.WorkspaceID, o.ID, key, ttl.Seconds()).Scan(&raw, &at)
	if err != nil || json.Unmarshal(raw, v) != nil {
		return time.Time{}, false
	}
	return at, true
}

func (s *Server) keep(ctx context.Context, o *connectors.Opened, key string, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	_, _ = s.pool.Exec(ctx, `INSERT INTO analytics_cache (workspace_id, integration_id, key, report) VALUES ($1, $2, $3, $4)
		ON CONFLICT (workspace_id, integration_id, key) DO UPDATE SET report = EXCLUDED.report, fetched_at = now()`, o.WorkspaceID, o.ID, key, raw)
	_, _ = s.pool.Exec(ctx, `DELETE FROM analytics_cache WHERE workspace_id = $1 AND integration_id = $2 AND fetched_at < now() - interval '1 day'`, o.WorkspaceID, o.ID)
}

func (s *Server) sites(ctx context.Context, o *connectors.Opened, reader connectors.AnalyticsReader, fresh bool) ([]connectors.AnalyticsSite, error) {
	var sites []connectors.AnalyticsSite
	if !fresh {
		if _, ok := s.cached(ctx, o, "sites", cacheTTL(reader), &sites); ok {
			return sites, nil
		}
	}
	sites, err := reader.Sites(ctx, o.Client, o.Creds)
	if err != nil {
		return nil, err
	}
	s.keep(ctx, o, "sites", sites)
	return sites, nil
}

func (s *Server) analyticsSites(w http.ResponseWriter, r *http.Request) {
	o, reader, ok := s.openAnalytics(w, r)
	if !ok {
		return
	}
	sites, err := s.sites(r.Context(), o, reader, r.URL.Query().Get("fresh") == "true")
	if err != nil {
		s.analyticsFailed(w, r, o, err)
		return
	}
	plan := analyticsync.Plan(analyticsync.ParseConfig(o.Integration.Config), sites)
	if a := accessFrom(r.Context()); a.Brands != nil {
		// Someone kept to some brands sees their brands' sites and the ones
		// nobody has placed yet.
		owners, err := s.planOwners(r.Context(), o, plan)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		plan = slices.DeleteFunc(plan, func(p analyticsync.SitePlan) bool { return !a.placesSite(owners[p.ID]) })
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": plan, "site": siteOf(o.Integration)})
}

func (s *Server) planOwners(ctx context.Context, o *connectors.Opened, plan []analyticsync.SitePlan) (map[string]siteOwner, error) {
	ids, hosts := make([]string, len(plan)), make([]string, len(plan))
	for i, p := range plan {
		ids[i], hosts[i] = p.ID, p.Host
	}
	return s.siteOwners(ctx, o.WorkspaceID, o.ID, ids, hosts)
}

// updateAnalyticsSite switches one site's sync on or off and sets the
// domain it counts under. Switching a site off deletes what was stored
// for it, so totals by domain stop counting it.
func (s *Server) updateAnalyticsSite(w http.ResponseWriter, r *http.Request) {
	o, reader, ok := s.openAnalytics(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	id := r.PathValue("site")
	var req struct {
		Sync             *bool     `json:"sync"`
		Domain           *string   `json:"domain"`
		ConversionEvents *[]string `json:"conversion_events"`
	}
	if !decode(w, r, &req) {
		return
	}
	var events []string
	if req.ConversionEvents != nil {
		if _, ok := reader.(connectors.PageConversionReader); !ok {
			writeError(w, r, http.StatusBadRequest, "invalid_request", o.Name+" cannot count conversions by landing page; PostHog can")
			return
		}
		events = []string{}
		for _, e := range *req.ConversionEvents {
			e = strings.TrimSpace(e)
			if !connectors.ValidEventName(e) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "conversion_events are event names: letters, digits, spaces and _ $ . : / -, up to 200 characters")
				return
			}
			if !slices.Contains(events, e) {
				events = append(events, e)
			}
		}
		if len(events) > connectors.MaxConversionEvents {
			writeError(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("at most %d conversion events", connectors.MaxConversionEvents))
			return
		}
	}
	domain := ""
	if req.Domain != nil {
		if domain = analyticsync.Host(*req.Domain); domain == "" && strings.TrimSpace(*req.Domain) != "" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "domain is the website's domain, like example.com")
			return
		}
	}
	sites, err := s.sites(r.Context(), o, reader, true)
	if err != nil {
		s.analyticsFailed(w, r, o, err)
		return
	}
	cfg := analyticsync.ParseConfig(o.Integration.Config)
	var plan *analyticsync.SitePlan
	for _, p := range analyticsync.Plan(cfg, sites) {
		if p.ID == id {
			plan = &p
			break
		}
	}
	if plan == nil {
		writeError(w, r, http.StatusNotFound, "unknown_site", "this connection cannot read "+id+"; GET .../analytics/"+o.ID+"/sites lists the ones it can")
		return
	}
	if a.Brands != nil {
		// Someone kept to some brands changes their brands' sites and the
		// ones nobody has placed yet, and can't move one into another brand
		// by its domain.
		owners, err := s.planOwners(r.Context(), o, []analyticsync.SitePlan{*plan})
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if !a.placesSite(owners[id]) {
			writeError(w, r, http.StatusNotFound, "unknown_site", "this connection cannot read "+id+"; GET .../analytics/"+o.ID+"/sites lists the ones it can")
			return
		}
		if req.Domain != nil && domain != "" {
			moved := *plan
			moved.Host = domain
			after, err := s.planOwners(r.Context(), o, []analyticsync.SitePlan{moved})
			if err != nil {
				s.internal(w, r, err)
				return
			}
			if b := after[id].brand; b != nil && !slices.Contains(a.Brands, *b) {
				writeError(w, r, http.StatusForbidden, "forbidden", domain+" is another brand's website; you can place sites only in your own brands")
				return
			}
		}
	}
	entry := analyticsync.SiteConfig{ID: id, Domain: plan.Domain, Off: !plan.Sync, ConversionEvents: plan.ConversionEvents}
	if len(entry.ConversionEvents) == 0 {
		entry.ConversionEvents = nil
	}
	kept := cfg.Sites[:0]
	for _, x := range cfg.Sites {
		if x.ID != id {
			kept = append(kept, x)
		}
	}
	if req.Sync != nil {
		entry.Off = !*req.Sync
	}
	if req.Domain != nil {
		entry.Domain = domain
	}
	if req.ConversionEvents != nil {
		entry.ConversionEvents = events
		if len(events) == 0 {
			entry.ConversionEvents = nil
		}
	}
	cfg.Sites = append(kept, entry)
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `UPDATE integrations SET config = jsonb_set(config, '{sites}', $3::jsonb), updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, o.ID, cfg.Sites); err != nil {
			return err
		}
		if entry.Off {
			if _, err := tx.Exec(r.Context(), `DELETE FROM analytics_daily WHERE workspace_id = $1 AND integration_id = $2 AND site = $3`, a.WorkspaceID, o.ID, id); err != nil {
				return err
			}
			if _, err := tx.Exec(r.Context(), `DELETE FROM analytics_syncs WHERE workspace_id = $1 AND integration_id = $2 AND site = $3`, a.WorkspaceID, o.ID, id); err != nil {
				return err
			}
			for _, q := range []string{
				`DELETE FROM page_conversions WHERE workspace_id = $1 AND integration_id = $2 AND site = $3`,
				`DELETE FROM page_conversion_syncs WHERE workspace_id = $1 AND integration_id = $2 AND site = $3`,
			} {
				if _, err := tx.Exec(r.Context(), q, a.WorkspaceID, o.ID, id); err != nil {
					return err
				}
			}
		} else if req.Domain != nil {
			host := domain
			if host == "" {
				host = analyticsync.HostOf(plan.AnalyticsSite)
			}
			// Stored history follows the new domain straight away.
			for _, q := range []string{
				`UPDATE analytics_daily SET host = $4 WHERE workspace_id = $1 AND integration_id = $2 AND site = $3`,
				`UPDATE analytics_syncs SET host = $4 WHERE workspace_id = $1 AND integration_id = $2 AND site = $3`,
			} {
				if _, err := tx.Exec(r.Context(), q, a.WorkspaceID, o.ID, id, host); err != nil {
					return err
				}
			}
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "analytics.site_updated", "integration", o.ID, map[string]any{"site": id, "sync": !entry.Off, "domain": entry.Domain, "conversion_events": len(entry.ConversionEvents)})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	plan.Sync, plan.Domain = !entry.Off, entry.Domain
	if plan.ConversionEvents = entry.ConversionEvents; plan.ConversionEvents == nil {
		plan.ConversionEvents = []string{}
	}
	if plan.Domain != "" {
		plan.Host = plan.Domain
	} else {
		plan.Host = analyticsync.HostOf(plan.AnalyticsSite)
	}
	if plan.Sync {
		// A site switched on starts filling in now rather than in six hours.
		_, _ = analyticsync.Enqueue(r.Context(), s.pool, a.WorkspaceID, o.ID, "")
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) setAnalyticsSite(w http.ResponseWriter, r *http.Request) {
	o, reader, ok := s.openAnalytics(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	var req struct {
		Site string `json:"site"`
		// Domain lines the site up with other tools when the tool does
		// not show one (Google Analytics properties, Vercel projects).
		Domain *string `json:"domain"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Site = strings.TrimSpace(req.Site)
	domain := ""
	if req.Domain != nil {
		if domain = analyticsync.Host(*req.Domain); domain == "" && strings.TrimSpace(*req.Domain) != "" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "domain is the website's domain, like example.com")
			return
		}
	}
	if req.Site != "" {
		sites, err := s.sites(r.Context(), o, reader, true)
		if err != nil {
			s.analyticsFailed(w, r, o, err)
			return
		}
		found := false
		for _, x := range sites {
			found = found || x.ID == req.Site
		}
		if !found {
			writeError(w, r, http.StatusBadRequest, "unknown_site", "this connection cannot read "+req.Site+"; GET .../analytics/"+o.ID+"/sites lists the ones it can")
			return
		}
	}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `UPDATE integrations SET config = jsonb_set(config, '{site}', to_jsonb($3::text)), updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, o.ID, req.Site); err != nil {
			return err
		}
		if req.Domain != nil {
			if _, err := tx.Exec(r.Context(), `UPDATE integrations SET config = jsonb_set(config, '{domain}', to_jsonb($3::text)) WHERE workspace_id = $1 AND id = $2`,
				a.WorkspaceID, o.ID, domain); err != nil {
				return err
			}
			// Stored history follows the new domain straight away.
			if _, err := tx.Exec(r.Context(), `UPDATE analytics_daily SET host = $3 WHERE workspace_id = $1 AND integration_id = $2 AND site = $4`, a.WorkspaceID, o.ID, domain, req.Site); err != nil {
				return err
			}
			if _, err := tx.Exec(r.Context(), `UPDATE analytics_syncs SET host = $3 WHERE workspace_id = $1 AND integration_id = $2 AND site = $4`, a.WorkspaceID, o.ID, domain, req.Site); err != nil {
				return err
			}
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "analytics.site_set", "integration", o.ID, map[string]any{"site": req.Site, "domain": req.Domain})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := map[string]string{"site": req.Site}
	if req.Domain != nil {
		out["domain"] = domain
	}
	writeJSON(w, http.StatusOK, out)
}

// errPickSite means the account has several sites and none was named.
var errPickSite = errors.New("pick a site")

// resolveSite is the named site, else the connection's default, else the
// first site the connection syncs.
func (s *Server) resolveSite(ctx context.Context, o *connectors.Opened, reader connectors.AnalyticsReader, named string) (string, error) {
	if named = strings.TrimSpace(named); named != "" {
		return named, nil
	}
	if d := siteOf(o.Integration); d != "" {
		return d, nil
	}
	sites, err := s.sites(ctx, o, reader, false)
	if err != nil {
		return "", err
	}
	// No default: the first site the sync stores, so a connection that
	// sees many sites answers without anyone picking one.
	plan := analyticsync.Plan(analyticsync.ParseConfig(o.Integration.Config), sites)
	for _, p := range plan {
		if p.Sync {
			return p.ID, nil
		}
	}
	if len(plan) > 0 {
		return plan[0].ID, nil
	}
	return "", errPickSite
}

type analyticsAnswer struct {
	connectors.AnalyticsReport
	Integration string    `json:"integration_id"`
	Connector   string    `json:"connector"`
	Cached      bool      `json:"cached"`
	FetchedAt   time.Time `json:"fetched_at"`
}

// analyticsFetch answers one query, from the cache when it can.
func (s *Server) analyticsFetch(ctx context.Context, o *connectors.Opened, reader connectors.AnalyticsReader, q connectors.AnalyticsQuery, fresh bool) (analyticsAnswer, error) {
	out := analyticsAnswer{Integration: o.ID, Connector: o.Connector.Definition().ID}
	if !connectors.Supports(reader, q.Report) {
		return out, connectors.NotSupported(o.Connector.Definition().Name, q.Report)
	}
	site, err := s.resolveSite(ctx, o, reader, q.Site)
	if err != nil {
		return out, err
	}
	q.Site = site
	key := strings.Join([]string{q.Report, q.Site, q.From, q.To, strconv.Itoa(q.Limit)}, "|")
	if !fresh {
		if at, ok := s.cached(ctx, o, key, cacheTTL(reader), &out.AnalyticsReport); ok {
			out.Cached, out.FetchedAt = true, at
			return out, nil
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	rep, err := reader.Report(ctx, o.Client, o.Creds, q)
	if err != nil {
		return out, err
	}
	if rep.Series == nil && q.Report == connectors.ReportTraffic {
		rep.Series = []connectors.AnalyticsPoint{}
	}
	if rep.Rows == nil && q.Report != connectors.ReportTraffic {
		rep.Rows = []connectors.AnalyticsRow{}
	}
	if rep.Notes == nil {
		rep.Notes = []string{}
	}
	s.keep(ctx, o, key, rep)
	out.AnalyticsReport, out.FetchedAt = rep, time.Now().UTC()
	return out, nil
}

func analyticsQuery(r *http.Request, report string) (connectors.AnalyticsQuery, error) {
	v := r.URL.Query()
	q := connectors.AnalyticsQuery{Report: report, Site: v.Get("site"), From: v.Get("from"), To: v.Get("to")}
	if l := v.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil {
			return q, connectors.Errf(connectors.ErrInvalid, "limit should be a number from 1 to 1000")
		}
		q.Limit = n
	}
	// Accept full timestamps too, as other report endpoints do.
	q.From, q.To = dateOnly(q.From), dateOnly(q.To)
	return connectors.CheckAnalyticsQuery(q, time.Now())
}

func dateOnly(s string) string {
	if len(s) > 10 && s[4] == '-' {
		return s[:10]
	}
	return s
}

func validReport(name string) bool {
	for _, x := range connectors.AllReports {
		if x == name {
			return true
		}
	}
	return false
}

func (s *Server) analyticsReport(w http.ResponseWriter, r *http.Request) {
	report := r.PathValue("report")
	if !validReport(report) {
		writeError(w, r, http.StatusNotFound, "not_found", "no report "+report+"; reports are "+strings.Join(connectors.AllReports, ", "))
		return
	}
	q, err := analyticsQuery(r, report)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	o, reader, ok := s.openAnalytics(w, r)
	if !ok {
		return
	}
	out, err := s.analyticsFetch(r.Context(), o, reader, q, r.URL.Query().Get("fresh") == "true")
	switch {
	case errors.Is(err, errPickSite):
		writeError(w, r, http.StatusBadRequest, "pick_site", o.Name+" can read several sites: name one with ?site=, or set a default with PUT .../analytics/"+o.ID+"/site. GET .../analytics/"+o.ID+"/sites lists them.")
	case err != nil && connectors.KindOf(err) == connectors.ErrInvalid:
		connectorError(w, r, err)
	case err != nil:
		s.analyticsFailed(w, r, o, err)
	default:
		writeJSON(w, http.StatusOK, out)
	}
}

// analyticsSummary is every analytics connection's traffic totals over one
// range, for "how is the site doing" in one call. A connection that fails
// says why instead of failing the whole answer.
func (s *Server) analyticsSummary(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	q, err := analyticsQuery(r, connectors.ReportTraffic)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	ws := accessFrom(r.Context()).WorkspaceID
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	all, err := s.analyticsSources(r.Context(), ws, sel)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// A shared connection counts here only when the site it reads is the
	// brand's.
	sources := []analyticsSource{}
	for _, src := range all {
		in, err := s.siteInBrand(r.Context(), ws, src.ID, src.Site, sel)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if in {
			sources = append(sources, src)
		}
	}
	type item struct {
		analyticsSource
		Report string              `json:"report"`
		Totals *connectors.Metrics `json:"totals"`
		From   string              `json:"from,omitempty"`
		To     string              `json:"to,omitempty"`
		Notes  []string            `json:"notes"`
		Error  *string             `json:"error"`
	}
	out := make([]item, len(sources))
	fresh := r.URL.Query().Get("fresh") == "true"
	var wg sync.WaitGroup
	limit := make(chan struct{}, 4)
	for i, src := range sources {
		out[i] = item{analyticsSource: src, Notes: []string{}}
		// Search consoles have no visitors; their traffic report is
		// clicks and impressions, which is still the summary.
		wg.Add(1)
		go func(i int, src analyticsSource) {
			defer wg.Done()
			limit <- struct{}{}
			defer func() { <-limit }()
			fail := func(msg string) { out[i].Error = &msg }
			o, err := s.integrations.Open(r.Context(), s.pool, ws, src.ID)
			if err != nil {
				fail(err.Error())
				return
			}
			reader := o.Connector.(connectors.AnalyticsReader)
			rq := q
			if !connectors.Supports(reader, connectors.ReportTraffic) {
				fail(o.Connector.Definition().Name + " has no traffic report")
				return
			}
			ans, err := s.analyticsFetch(r.Context(), o, reader, rq, fresh)
			switch {
			case errors.Is(err, errPickSite):
				fail("Pick a site for this connection first: list its sites, then set one as the default.")
			case err != nil:
				if connectors.KindOf(err) == connectors.ErrAuth {
					_ = s.integrations.Checked(r.Context(), s.pool, ws, o.ID, nil, err)
				}
				fail(err.Error())
			default:
				out[i].Site, out[i].Report, out[i].Totals = ans.Site, ans.Report, &ans.Totals
				out[i].From, out[i].To, out[i].Notes = ans.From, ans.To, ans.Notes
			}
		}(i, src)
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]any{"from": q.From, "to": q.To, "data": out})
}
