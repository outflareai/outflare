package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/outflareai/outflare/engine/internal/analyticsync"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Stored analytics: what the sync copied from each tool, for history,
// cross-tool comparison and totals without calling the providers.

func (s *Server) routesAnalyticsHistory(mux *http.ServeMux) {
	read := auth.ScopeAnalyticsRead
	mux.HandleFunc("GET /v1/workspaces/{ws}/analytics/history", s.inBrand(read, s.analyticsHistory))
	mux.HandleFunc("GET /v1/workspaces/{ws}/analytics/overview", s.inBrand(read, s.analyticsOverview))
	mux.HandleFunc("GET /v1/workspaces/{ws}/analytics/compare", s.inBrand(read, s.analyticsCompare))
	mux.HandleFunc("GET /v1/workspaces/{ws}/analytics/syncs", s.inWorkspace(read, s.analyticsSyncs))
	mux.HandleFunc("POST /v1/workspaces/{ws}/analytics/sync", s.inWorkspace(read, s.analyticsSyncAll))
	mux.HandleFunc("POST /v1/workspaces/{ws}/analytics/{id}/sync", s.inWorkspace(read, s.analyticsSyncOne))
}

// metricCols adds metrics up over days and tools: counts are summed,
// click-through rate and position are weighted by impressions, the rest
// averaged. A metric no row measures stays null.
const metricCols = `sum(visitors), sum(pageviews), sum(sessions),
	CASE WHEN sum(sessions) > 0 THEN sum(bounce_rate * sessions) / sum(sessions) ELSE avg(bounce_rate) END,
	avg(avg_duration), sum(clicks), sum(impressions),
	CASE WHEN sum(impressions) > 0 THEN sum(clicks) / sum(impressions) ELSE avg(ctr) END,
	CASE WHEN sum(impressions) > 0 THEN sum(position * impressions) / sum(impressions) ELSE avg(position) END,
	sum(events), sum(conversions)`

func scanMetrics(m *connectors.Metrics) []any {
	return []any{&m.Visitors, &m.Pageviews, &m.Sessions, &m.BounceRate, &m.AvgDuration, &m.Clicks, &m.Impressions, &m.CTR, &m.Position, &m.Events, &m.Conversions}
}

type historyFilter struct {
	report, from, to, integration, host string
	// brand keeps the rows to sites of a brand, or with none.
	brand brandSel
}

// historyQuery reads the shared filters. Dates default to the last 28
// days, ending yesterday.
func historyQuery(w http.ResponseWriter, r *http.Request, defReport string) (historyFilter, bool) {
	q := r.URL.Query()
	f := historyFilter{report: q.Get("report"), integration: q.Get("integration"), host: analyticsync.Host(q.Get("host"))}
	if f.report == "" {
		f.report = defReport
	}
	if !validReport(f.report) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "report is one of "+strings.Join(connectors.AllReports, ", "))
		return f, false
	}
	if q.Get("host") != "" && f.host == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "host is a domain, like example.com")
		return f, false
	}
	if f.integration != "" && !isUUID(f.integration) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "integration is an analytics connection id")
		return f, false
	}
	to := time.Now().UTC().AddDate(0, 0, -1)
	f.to, f.from = to.Format(time.DateOnly), to.AddDate(0, 0, -27).Format(time.DateOnly)
	for name, dst := range map[string]*string{"from": &f.from, "to": &f.to} {
		if v := q.Get(name); v != "" {
			if dateOnly(v) == "" {
				writeError(w, r, http.StatusBadRequest, "invalid_request", name+" is a date, YYYY-MM-DD")
				return f, false
			}
			*dst = dateOnly(v)
		}
	}
	if f.from > f.to {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "from is after to")
		return f, false
	}
	return f, true
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

// where builds the shared WHERE clause; $1 is the workspace.
func (f historyFilter) where(ws string) (string, []any) {
	sql := `d.workspace_id = $1 AND d.report = $2 AND d.day BETWEEN $3 AND $4`
	args := []any{ws, f.report, f.from, f.to}
	if f.integration != "" {
		args = append(args, f.integration)
		sql += ` AND d.integration_id = $` + strconv.Itoa(len(args))
	}
	if f.host != "" {
		args = append(args, f.host)
		sql += ` AND d.host = $` + strconv.Itoa(len(args))
	}
	sql += f.brand.sql(`analytics_site_brand(d.workspace_id, d.integration_id, d.site, d.host)`, &args)
	return sql, args
}

type historyRow struct {
	Day         string `json:"day,omitempty"`
	Key         string `json:"key,omitempty"`
	Integration string `json:"integration_id,omitempty"`
	Connector   string `json:"connector,omitempty"`
	Host        string `json:"host,omitempty"`
	connectors.Metrics
}

func (s *Server) analyticsHistory(w http.ResponseWriter, r *http.Request) {
	ws := accessFrom(r.Context()).WorkspaceID
	f, ok := historyQuery(w, r, connectors.ReportTraffic)
	if !ok {
		return
	}
	if f.brand, ok = s.brandFilter(w, r); !ok {
		return
	}
	group := r.URL.Query().Get("group")
	if group == "" {
		group = "day"
		if f.report != connectors.ReportTraffic {
			group = "key"
		}
	}
	limit := 100
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = min(v, 5000)
	}
	where, args := f.where(ws)
	var sel, by, order string
	switch group {
	case "day":
		// One row per day per tool.
		sel, by, order = `to_char(d.day, 'YYYY-MM-DD'), '', d.integration_id::text, d.connector, d.host`, `d.day, d.integration_id, d.connector, d.host`, `d.day, d.connector`
	case "tool":
		sel, by, order = `'', '', d.integration_id::text, d.connector, d.host`, `d.integration_id, d.connector, d.host`, `d.connector`
	case "key":
		// Added up across the tools picked.
		sel, by, order = `'', d.key, '', '', ''`, `d.key`, `sum(coalesce(d.pageviews, d.clicks, d.sessions, d.visitors, d.events, 0)) DESC, d.key`
	case "tool_key":
		sel, by, order = `'', d.key, d.integration_id::text, d.connector, d.host`, `d.key, d.integration_id, d.connector, d.host`, `sum(coalesce(d.pageviews, d.clicks, d.sessions, d.visitors, d.events, 0)) DESC, d.key`
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "group is day, tool, key or tool_key")
		return
	}
	keyFilter := ` AND d.key = ''`
	if group == "key" || group == "tool_key" {
		keyFilter = ` AND d.key <> ''`
	}
	args = append(args, limit)
	rows, err := s.pool.Query(r.Context(), `SELECT `+sel+`, `+metricCols+` FROM analytics_daily d WHERE `+where+keyFilter+
		` GROUP BY `+by+` ORDER BY `+order+` LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := []historyRow{}
	for rows.Next() {
		var h historyRow
		if err := rows.Scan(append([]any{&h.Day, &h.Key, &h.Integration, &h.Connector, &h.Host}, scanMetrics(&h.Metrics)...)...); err != nil {
			rows.Close()
			s.internal(w, r, err)
			return
		}
		out = append(out, h)
	}
	rows.Close()
	if rows.Err() != nil {
		s.internal(w, r, rows.Err())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"report": f.report, "from": f.from, "to": f.to, "group": group, "data": out,
		"notes": historyNotes(f.report, group)})
}

func historyNotes(report, group string) []string {
	notes := []string{}
	if group != "day" {
		notes = append(notes, "Visitors are added up by day, so a person who came on two days counts twice.")
	}
	if group == "key" && report != connectors.ReportTraffic {
		notes = append(notes, "Each key is added up across the tools picked; pass integration to read one tool.")
	}
	return notes
}

type overviewTool struct {
	Integration string             `json:"integration_id"`
	Name        string             `json:"name"`
	Connector   string             `json:"connector"`
	Site        string             `json:"site"`
	SiteName    string             `json:"site_name"`
	Totals      connectors.Metrics `json:"totals"`
	FirstDay    *string            `json:"first_day"`
	LastDay     *string            `json:"last_day"`
	LastSynced  *time.Time         `json:"last_synced_at"`
}

type overviewSite struct {
	Host  string         `json:"host"`
	Tools []overviewTool `json:"tools"`
	// Search adds up clicks and impressions from every search console
	// (Google and Bing are different engines, so their totals add up).
	Search connectors.Metrics `json:"search"`
	// SEO is each SEO connection's latest snapshot of the domain.
	SEO []seoLatest `json:"seo"`
}

func (s *Server) analyticsOverview(w http.ResponseWriter, r *http.Request) {
	ws := accessFrom(r.Context()).WorkspaceID
	f, ok := historyQuery(w, r, connectors.ReportTraffic)
	if !ok {
		return
	}
	if f.brand, ok = s.brandFilter(w, r); !ok {
		return
	}
	f.report = connectors.ReportTraffic
	where, args := f.where(ws)
	rows, err := s.pool.Query(r.Context(), `
		SELECT d.host, d.integration_id::text, i.name, d.connector, d.site, COALESCE(a.site_name, ''),
			to_char(a.first_day, 'YYYY-MM-DD'), to_char(a.last_day, 'YYYY-MM-DD'), a.last_ok_at, `+metricCols+`
		FROM analytics_daily d
		JOIN integrations i ON i.id = d.integration_id
		LEFT JOIN analytics_syncs a ON a.integration_id = d.integration_id AND a.site = d.site
		WHERE `+where+` AND d.key = ''
		GROUP BY d.host, d.integration_id, i.name, d.connector, d.site, a.site_name, a.first_day, a.last_day, a.last_ok_at
		ORDER BY d.host = '', d.host, i.name`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := []*overviewSite{}
	byHost := map[string]*overviewSite{}
	for rows.Next() {
		var host string
		var t overviewTool
		if err := rows.Scan(append([]any{&host, &t.Integration, &t.Name, &t.Connector, &t.Site, &t.SiteName, &t.FirstDay, &t.LastDay, &t.LastSynced}, scanMetrics(&t.Totals)...)...); err != nil {
			rows.Close()
			s.internal(w, r, err)
			return
		}
		site := byHost[host]
		if site == nil {
			site = &overviewSite{Host: host, Tools: []overviewTool{}}
			byHost[host] = site
			out = append(out, site)
		}
		site.Tools = append(site.Tools, t)
		if t.Totals.Impressions != nil {
			site.Search = site.Search.Add(connectors.Metrics{Clicks: t.Totals.Clicks, Impressions: t.Totals.Impressions})
		}
	}
	rows.Close()
	if rows.Err() != nil {
		s.internal(w, r, rows.Err())
		return
	}
	for _, site := range out {
		if site.Search.Clicks != nil && site.Search.Impressions != nil && *site.Search.Impressions > 0 {
			site.Search.CTR = connectors.Num(*site.Search.Clicks / *site.Search.Impressions)
		}
	}
	// SEO snapshots join on the same domain; a domain only SEO follows
	// still gets a row.
	seo, err := s.seoLatestByHost(r.Context(), ws, f.brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	inBrand, err := s.hostBrandFilter(r.Context(), ws, f.brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for _, site := range out {
		if inBrand(site.Host) {
			site.SEO = seo[site.Host]
		}
		delete(seo, site.Host)
	}
	for host, l := range seo {
		if (f.host == "" || f.host == host) && inBrand(host) {
			out = append(out, &overviewSite{Host: host, Tools: []overviewTool{}, SEO: l})
		}
	}
	for _, site := range out {
		if site.SEO == nil {
			site.SEO = []seoLatest{}
		}
	}
	syncs, err := s.syncStatuses(r.Context(), ws, f.brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": f.from, "to": f.to, "data": out, "syncs": syncs,
		"notes": []string{"Sites line up across tools by domain. Set a connection's domain with PUT .../analytics/{id}/site when its tool does not show one."}})
}

type compareTool struct {
	Integration string                      `json:"integration_id"`
	Name        string                      `json:"name"`
	Connector   string                      `json:"connector"`
	Series      []connectors.AnalyticsPoint `json:"series"`
}

func (s *Server) analyticsCompare(w http.ResponseWriter, r *http.Request) {
	ws := accessFrom(r.Context()).WorkspaceID
	f, ok := historyQuery(w, r, connectors.ReportTraffic)
	if !ok {
		return
	}
	if f.brand, ok = s.brandFilter(w, r); !ok {
		return
	}
	if f.host == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "host is the website to compare, like example.com; GET .../analytics/overview lists them")
		return
	}
	if !s.needHostInBrand(w, r, ws, f.host, f.brand) {
		return
	}
	f.report = connectors.ReportTraffic
	where, args := f.where(ws)
	rows, err := s.pool.Query(r.Context(), `
		SELECT d.integration_id::text, i.name, d.connector, to_char(d.day, 'YYYY-MM-DD'), `+metricCols+`
		FROM analytics_daily d JOIN integrations i ON i.id = d.integration_id
		WHERE `+where+` AND d.key = ''
		GROUP BY d.integration_id, i.name, d.connector, d.day ORDER BY i.name, d.day`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := []*compareTool{}
	byID := map[string]*compareTool{}
	for rows.Next() {
		var id, name, conn string
		var p connectors.AnalyticsPoint
		if err := rows.Scan(append([]any{&id, &name, &conn, &p.Date}, scanMetrics(&p.Metrics)...)...); err != nil {
			rows.Close()
			s.internal(w, r, err)
			return
		}
		t := byID[id]
		if t == nil {
			t = &compareTool{Integration: id, Name: name, Connector: conn, Series: []connectors.AnalyticsPoint{}}
			byID[id] = t
			out = append(out, t)
		}
		t.Series = append(t.Series, p)
	}
	rows.Close()
	if rows.Err() != nil {
		s.internal(w, r, rows.Err())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"host": f.host, "from": f.from, "to": f.to, "data": out,
		"notes": []string{"Tools count differently (cookies, bots, consent banners), so expect their numbers to differ."}})
}

type syncStatus struct {
	Integration string     `json:"integration_id"`
	Name        string     `json:"name"`
	Connector   string     `json:"connector"`
	Site        string     `json:"site"`
	Host        string     `json:"host"`
	FirstDay    *string    `json:"first_day"`
	LastDay     *string    `json:"last_day"`
	LastRun     *time.Time `json:"last_run_at"`
	LastOK      *time.Time `json:"last_ok_at"`
	LastError   *string    `json:"last_error"`
	Paused      bool       `json:"paused"`
}

// syncStatuses lists each analytics connection's sites and how their
// syncs went, for the sites a brand filter lets through.
func (s *Server) syncStatuses(ctx context.Context, ws string, sel brandSel) ([]syncStatus, error) {
	sources, err := s.analyticsSources(ctx, ws, sel)
	if err != nil {
		return nil, err
	}
	out := []syncStatus{}
	for _, src := range sources {
		// One row per site the sync has run for, or one for the connection
		// before its first run.
		args := []any{src.ID}
		inBrand := sel.sql(`analytics_site_brand(i.workspace_id, i.id, a.site, a.host)`, &args)
		rows, err := s.pool.Query(ctx, `
			SELECT a.site, a.host, to_char(a.first_day, 'YYYY-MM-DD'), to_char(a.last_day, 'YYYY-MM-DD'), a.last_run_at, a.last_ok_at, a.last_error,
				COALESCE((i.config->>'sync_paused')::boolean, false)
			FROM integrations i LEFT JOIN analytics_syncs a ON a.integration_id = i.id`+inBrand+` WHERE i.id = $1
			ORDER BY a.site`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			st := syncStatus{Integration: src.ID, Name: src.Name, Connector: src.Connector}
			if err := rows.Scan(nullable(&st.Site), nullable(&st.Host), &st.FirstDay, &st.LastDay, &st.LastRun, &st.LastOK, &st.LastError, &st.Paused); err != nil {
				rows.Close()
				return nil, err
			}
			if st.Site == "" {
				st.Site = src.Site
			}
			out = append(out, st)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// nullable scans a NULL text as "" without overwriting a value already set.
type nullText struct{ dst *string }

func (n nullText) Scan(v any) error {
	if s, ok := v.(string); ok && s != "" {
		*n.dst = s
	}
	return nil
}

func nullable(dst *string) nullText { return nullText{dst} }

func (s *Server) analyticsSyncs(w http.ResponseWriter, r *http.Request) {
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	out, err := s.syncStatuses(r.Context(), accessFrom(r.Context()).WorkspaceID, sel)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (s *Server) needSync(w http.ResponseWriter, r *http.Request) bool {
	if s.analyticsSync == nil || !s.needIntegrations(w, r) {
		if s.analyticsSync == nil {
			writeError(w, r, http.StatusServiceUnavailable, "not_configured", "analytics sync is not running on this server")
		}
		return false
	}
	return true
}

// analyticsSyncAll queues a sync of every analytics connection.
func (s *Server) analyticsSyncAll(w http.ResponseWriter, r *http.Request) {
	if !s.needSync(w, r) {
		return
	}
	ws := accessFrom(r.Context()).WorkspaceID
	sources, err := s.analyticsSources(r.Context(), ws, brandSel{})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	queued := []string{}
	for _, src := range sources {
		if src.Status != "connected" {
			continue
		}
		if _, err := analyticsync.Enqueue(r.Context(), s.pool, ws, src.ID, ""); err != nil {
			s.internal(w, r, err)
			return
		}
		queued = append(queued, src.ID)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": queued})
}

// analyticsSyncOne syncs one connection now and says what it stored.
func (s *Server) analyticsSyncOne(w http.ResponseWriter, r *http.Request) {
	if !s.needSync(w, r) {
		return
	}
	i, ok := s.loadIntegration(w, r)
	if !ok {
		return
	}
	if c, _ := s.integrations.Registry.Get(i.Connector); c == nil {
		writeError(w, r, http.StatusBadRequest, "not_analytics", i.Name+" is not an analytics connection")
		return
	} else if _, ok := c.(connectors.AnalyticsReader); !ok {
		writeError(w, r, http.StatusBadRequest, "not_analytics", i.Name+" is not an analytics connection")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	res, err := s.analyticsSync.SyncOnce(ctx, i.WorkspaceID, i.ID)
	if err != nil {
		connectorError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"integration_id": i.ID, "rows": res.Rows, "calls": res.Calls})
}
