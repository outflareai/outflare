package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/seosync"
	"github.com/outflareai/outflare/engine/internal/usagelimits"
)

// SEO: DataForSEO and Ahrefs read about a domain from the outside. Every
// live call costs money, so answers are kept for a day, each connection
// has a monthly budget, and the cost of every call lands in Outcomes.
func (s *Server) routesSEO(mux *http.ServeMux) {
	read, manage := auth.ScopeAnalyticsRead, auth.ScopeIntegrationsManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/seo", s.inWorkspace(read, s.listSEO))
	mux.HandleFunc("GET /v1/workspaces/{ws}/seo/history", s.inWorkspace(read, s.seoHistory))
	mux.HandleFunc("GET /v1/workspaces/{ws}/seo/overview", s.inBrand(read, s.seoOverview))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/seo/{id}/settings", s.inWorkspace(manage, s.setSEOSettings))
	mux.HandleFunc("GET /v1/workspaces/{ws}/seo/{id}/reports/{report}", s.inWorkspace(read, s.seoReport))
	mux.HandleFunc("POST /v1/workspaces/{ws}/seo/{id}/sync", s.inWorkspace(manage, s.seoSync))
}

type seoDomain struct {
	Host      string     `json:"host"`
	LastRunAt *time.Time `json:"last_run_at"`
	LastOKAt  *time.Time `json:"last_ok_at"`
	LastError *string    `json:"last_error"`
	LastCost  *float64   `json:"last_cost"`
}

type seoSource struct {
	ID            string             `json:"id"`
	Name          string             `json:"name"`
	Connector     string             `json:"connector"`
	ConnectorName string             `json:"connector_name"`
	Status        string             `json:"status"`
	LastError     *string            `json:"last_error"`
	Account       connectors.Account `json:"account"`
	Reports       []string           `json:"reports"`
	Country       string             `json:"country"`
	Sync          string             `json:"sync"`
	// LinkRows is how many links a weekly read asks for per SEO site;
	// zero for a connection that does not read links.
	LinkRows int `json:"link_rows"`
	// GapRows is how many keywords and linking domains a weekly read takes
	// of each tracked competitor; 0 is off.
	GapRows int `json:"gap_rows"`
	// DomainsSet: the domains were chosen; otherwise they follow the
	// analytics tools.
	DomainsSet bool           `json:"domains_set"`
	Domains    []seoDomain    `json:"domains"`
	Budget     seosync.Budget `json:"budget"`
}

func (s *Server) seoSources(ctx context.Context, ws string) ([]seoSource, error) {
	list, err := s.integrations.List(ctx, s.pool, ws, "")
	if err != nil {
		return nil, err
	}
	out := []seoSource{}
	for _, i := range list {
		c, ok := s.integrations.Registry.Get(i.Connector)
		if !ok {
			continue
		}
		r, ok := c.(connectors.SEOReader)
		if !ok {
			continue
		}
		set := seosync.SettingsOf(i.Config)
		src := seoSource{ID: i.ID, Name: i.Name, Connector: i.Connector, ConnectorName: c.Definition().Name, Status: i.Status,
			LastError: i.LastError, Account: i.Account, Reports: r.SEOReports(), Country: set.Country, Sync: set.Sync, Domains: []seoDomain{}}
		src.GapRows = set.GapRowsOrDefault()
		if connectors.SupportsSEO(r, connectors.SEOLinks) {
			src.LinkRows = set.LinkRowsOrDefault()
		}
		var hosts []string
		if hosts, src.DomainsSet, err = seosync.Domains(ctx, s.pool, i); err != nil {
			return nil, err
		}
		for _, h := range hosts {
			d := seoDomain{Host: h}
			err := s.pool.QueryRow(ctx, `SELECT last_run_at, last_ok_at, last_error, last_cost FROM seo_syncs WHERE integration_id = $1 AND host = $2`, i.ID, h).
				Scan(&d.LastRunAt, &d.LastOKAt, &d.LastError, &d.LastCost)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return nil, err
			}
			src.Domains = append(src.Domains, d)
		}
		if src.Budget, err = seosync.BudgetIn(ctx, s.pool, ws, i.ID, seosync.Unit(r)); err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, nil
}

// setSEOBudget makes budget the connection's monthly usage limit, keeping
// its daily one; nil goes back to the default.
func setSEOBudget(ctx context.Context, tx pgx.Tx, a *access, id, unit string, budget *float64) error {
	l, err := usagelimits.Load(ctx, tx, a.WorkspaceID, id)
	if err != nil {
		return err
	}
	kept := !l.Default && l.Unit == unit && (l.Daily != nil || !l.Stop)
	if budget == nil && !kept {
		return usagelimits.Reset(ctx, tx, a.WorkspaceID, id)
	}
	next := usagelimits.Limits{Unit: unit, Monthly: budget, Stop: true}
	if budget == nil {
		def := seosync.DefaultBudget(unit)
		next.Monthly = &def
	}
	if kept {
		next.Daily, next.Stop = l.Daily, l.Stop
	}
	actor := a.principal.ActorID
	return usagelimits.Save(ctx, tx, a.WorkspaceID, id, next, &actor)
}

func (s *Server) listSEO(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	out, err := s.seoSources(r.Context(), accessFrom(r.Context()).WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// openSEO loads an SEO integration with its credentials.
func (s *Server) openSEO(w http.ResponseWriter, r *http.Request) (*connectors.Opened, connectors.SEOReader, bool) {
	if !s.needIntegrations(w, r) {
		return nil, nil, false
	}
	i, ok := s.loadIntegration(w, r)
	if !ok {
		return nil, nil, false
	}
	c, _ := s.integrations.Registry.Get(i.Connector)
	reader, isReader := c.(connectors.SEOReader)
	if !isReader {
		writeError(w, r, http.StatusBadRequest, "not_seo", i.Name+" is not an SEO connection; GET /v1/workspaces/{ws}/seo lists them")
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

// seoFailed answers a failed SEO call: a budget stop, a refused request,
// or a provider error that also marks the connection.
func (s *Server) seoFailed(w http.ResponseWriter, r *http.Request, o *connectors.Opened, err error) {
	var be *seosync.BudgetError
	switch {
	case errors.As(err, &be):
		writeError(w, r, http.StatusPaymentRequired, "budget_reached", err.Error())
	case connectors.KindOf(err) == connectors.ErrInvalid:
		connectorError(w, r, err)
	default:
		s.analyticsFailed(w, r, o, err)
	}
}

func validSEOReport(name string) bool {
	for _, x := range connectors.AllSEOReports {
		if x == name {
			return true
		}
	}
	return false
}

func (s *Server) seoReport(w http.ResponseWriter, r *http.Request) {
	report := r.PathValue("report")
	if !validSEOReport(report) {
		writeError(w, r, http.StatusNotFound, "not_found", "no report "+report+"; SEO reports are "+strings.Join(connectors.AllSEOReports, ", "))
		return
	}
	o, reader, ok := s.openSEO(w, r)
	if !ok {
		return
	}
	v := r.URL.Query()
	set := seosync.SettingsOf(o.Config)
	target := seosync.Host(v.Get("target"))
	if target == "" && v.Get("target") == "" {
		if hosts, _, _ := seosync.Domains(r.Context(), s.pool, o.Integration); len(hosts) == 1 {
			target = hosts[0]
		}
	}
	if target == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "target is the domain to look at, like example.com")
		return
	}
	q := connectors.SEOQuery{Report: report, Target: target, Country: v.Get("country"), Limit: 0, Since: v.Get("since")}
	if q.Country == "" {
		q.Country = set.Country
	}
	if l := v.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit should be a number from 1 to 1000")
			return
		}
		q.Limit = n
	}
	q, err := connectors.CheckSEOQuery(q)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	a := accessFrom(r.Context())
	actor := a.principal.ActorID
	out, err := seosync.Fetch(r.Context(), s.pool, o, reader, q, seosync.Options{Fresh: v.Get("fresh") == "true", ActorID: &actor})
	if err != nil {
		s.seoFailed(w, r, o, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) setSEOSettings(w http.ResponseWriter, r *http.Request) {
	o, reader, ok := s.openSEO(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	var req struct {
		Domains       optional[[]string] `json:"domains"`
		Country       *string            `json:"country"`
		MonthlyBudget optional[float64]  `json:"monthly_budget"`
		Sync          *string            `json:"sync"`
		LinkRows      optional[int]      `json:"link_rows"`
		GapRows       optional[int]      `json:"gap_rows"`
	}
	if !decode(w, r, &req) {
		return
	}
	set := seosync.SettingsOf(o.Config)
	if req.Domains.Set {
		if req.Domains.Value == nil {
			set.Domains = nil
		} else {
			if len(*req.Domains.Value) > 25 {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "domains may only hold 25 per connection")
				return
			}
			seen := map[string]bool{}
			set.Domains = []string{}
			for _, d := range *req.Domains.Value {
				h := seosync.Host(d)
				if h == "" {
					writeError(w, r, http.StatusBadRequest, "invalid_request", "domains are like example.com, and "+strconv.Quote(d)+" is not one")
					return
				}
				if !seen[h] {
					seen[h] = true
					set.Domains = append(set.Domains, h)
				}
			}
		}
	}
	if req.Country != nil {
		c := strings.ToUpper(strings.TrimSpace(*req.Country))
		if len(c) != 2 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "country is a two-letter code, like US or AU")
			return
		}
		set.Country = c
	}
	if req.MonthlyBudget.Set {
		if b := req.MonthlyBudget.Value; b != nil && *b < 0 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "monthly_budget is 0 or more, or null for the default")
			return
		}
		set.MonthlyBudget = req.MonthlyBudget.Value
	}
	if req.Sync != nil {
		if *req.Sync != "weekly" && *req.Sync != "off" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "sync is weekly or off")
			return
		}
		set.Sync = *req.Sync
	}
	if req.LinkRows.Set {
		if n := req.LinkRows.Value; n != nil && (*n < 1 || *n > 1000) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "link_rows is 1 to 1000, or null for the default")
			return
		}
		set.LinkRows = req.LinkRows.Value
	}
	if req.GapRows.Set {
		if n := req.GapRows.Value; n != nil && (*n < 0 || *n > 500) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "gap_rows is 0 to 500, or null for the default")
			return
		}
		set.GapRows = req.GapRows.Value
	}
	patch, _ := json.Marshal(set)
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		// Settings keys are replaced whole; unset ones are removed so the
		// defaults apply.
		if _, err := tx.Exec(r.Context(), `UPDATE integrations SET config = (config - 'domains' - 'country' - 'monthly_budget' - 'sync' - 'link_rows' - 'gap_rows') || $3::jsonb, updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, o.ID, patch); err != nil {
			return err
		}
		// The budget is the connection's monthly usage limit.
		if req.MonthlyBudget.Set {
			if err := setSEOBudget(r.Context(), tx, a, o.ID, seosync.Unit(reader), set.MonthlyBudget); err != nil {
				return err
			}
		}
		// New settings (a raised budget, new domains) can fix a failed read:
		// try again now rather than at next week's snapshot.
		if _, err := seosync.EnqueueRetry(r.Context(), tx, a.WorkspaceID, o.ID); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "seo.settings_set", "integration", o.ID, map[string]any{"domains": set.Domains, "country": set.Country, "monthly_budget": set.MonthlyBudget, "sync": set.Sync, "link_rows": set.LinkRows, "gap_rows": set.GapRows})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	srcs, err := s.seoSources(r.Context(), a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for _, src := range srcs {
		if src.ID == o.ID {
			writeJSON(w, http.StatusOK, src)
			return
		}
	}
	writeJSON(w, http.StatusOK, set)
}

func (s *Server) seoSync(w http.ResponseWriter, r *http.Request) {
	if s.seoSyncer == nil {
		writeError(w, r, http.StatusServiceUnavailable, "not_configured", "SEO snapshots are not running on this server")
		return
	}
	o, _, ok := s.openSEO(w, r)
	if !ok {
		return
	}
	res, err := s.seoSyncer.SyncOnce(r.Context(), o.WorkspaceID, o.ID)
	if err != nil {
		var be *seosync.BudgetError
		if errors.As(err, &be) {
			writeError(w, r, http.StatusPaymentRequired, "budget_reached", err.Error())
			return
		}
		connectorError(w, r, err)
		return
	}
	if len(res.Domains) == 0 && len(res.Links) == 0 {
		writeError(w, r, http.StatusBadRequest, "no_domains", "this connection has no domains to snapshot: add some with PUT .../seo/"+o.ID+"/settings, or connect an analytics tool so its domains are followed")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type seoHistoryRow struct {
	Day         string `json:"day"`
	Integration string `json:"integration_id"`
	Connector   string `json:"connector"`
	Host        string `json:"host"`
	Country     string `json:"country"`
	Key         string `json:"key,omitempty"`
	URL         string `json:"url,omitempty"`
	Kind        string `json:"kind,omitempty"`
	connectors.SEOMetrics
	// Change since the snapshot before: position for keywords (negative
	// is better), the metric for an overview.
	Previous *connectors.SEOMetrics `json:"previous,omitempty"`
}

// seoHistory reads stored snapshots. overview gives one row per snapshot;
// keywords gives the latest snapshot's keywords with their previous
// position, unless from/to name a range.
func (s *Server) seoHistory(w http.ResponseWriter, r *http.Request) {
	ws := accessFrom(r.Context()).WorkspaceID
	v := r.URL.Query()
	report := v.Get("report")
	if report == "" {
		report = connectors.SEOOverview
	}
	if !validSEOReport(report) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "report is one of "+strings.Join(connectors.AllSEOReports, ", "))
		return
	}
	host := seosync.Host(v.Get("host"))
	if v.Get("host") != "" && host == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "host is a domain, like example.com")
		return
	}
	limit := 200
	if l := v.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 || n > 5000 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit should be a number from 1 to 5000")
			return
		}
		limit = n
	}
	from, to := dateOnly(v.Get("from")), dateOnly(v.Get("to"))
	for name, d := range map[string]string{"from": from, "to": to} {
		if d != "" {
			if _, err := time.Parse(time.DateOnly, d); err != nil {
				writeError(w, r, http.StatusBadRequest, "invalid_request", name+" is a date like 2026-10-24")
				return
			}
		}
	}
	args := []any{ws, report, host, from, to, v.Get("integration"), limit}
	if id := v.Get("integration"); id != "" && !isUUID(id) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "integration is a connection id from GET .../seo")
		return
	}
	// Lists (keywords and the rest) default to each connection's latest
	// snapshot, with the snapshot before it to compare.
	latest := report != connectors.SEOOverview && from == "" && to == ""
	rows, err := s.pool.Query(r.Context(), `
		WITH days AS (
			SELECT integration_id, host, day, dense_rank() OVER (PARTITION BY integration_id, host ORDER BY day DESC) AS n
			FROM (SELECT DISTINCT integration_id, host, day FROM seo_snapshots
				WHERE workspace_id = $1 AND report = $2 AND ($3 = '' OR host = $3) AND ($6 = '' OR integration_id::text = $6)) x
		)
		SELECT to_char(s.day, 'YYYY-MM-DD'), s.integration_id::text, s.connector, s.host, s.country, s.key, s.url, s.kind, s.metrics,
			(SELECT p.metrics FROM seo_snapshots p JOIN days pd ON pd.integration_id = p.integration_id AND pd.host = p.host AND pd.day = p.day
			 WHERE p.integration_id = s.integration_id AND p.host = s.host AND p.report = s.report AND p.key = s.key AND p.day < s.day
			 ORDER BY p.day DESC LIMIT 1)
		FROM seo_snapshots s JOIN days d ON d.integration_id = s.integration_id AND d.host = s.host AND d.day = s.day
		WHERE s.workspace_id = $1 AND s.report = $2
		  AND ($4 = '' OR s.day >= $4::date) AND ($5 = '' OR s.day <= $5::date)
		  AND (`+strconv.FormatBool(latest)+` = false OR d.n = 1)
		ORDER BY s.host, s.day DESC, s.integration_id, COALESCE((s.metrics->>'traffic')::float8, 0) DESC, s.key
		LIMIT $7`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	defer rows.Close()
	out := []seoHistoryRow{}
	for rows.Next() {
		var row seoHistoryRow
		var raw, prev []byte
		if err := rows.Scan(&row.Day, &row.Integration, &row.Connector, &row.Host, &row.Country, &row.Key, &row.URL, &row.Kind, &raw, &prev); err != nil {
			s.internal(w, r, err)
			return
		}
		_ = json.Unmarshal(raw, &row.SEOMetrics)
		if prev != nil {
			var p connectors.SEOMetrics
			if json.Unmarshal(prev, &p) == nil {
				row.Previous = &p
			}
		}
		out = append(out, row)
	}
	if rows.Err() != nil {
		s.internal(w, r, rows.Err())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"report": report, "data": out})
}

// seoLatest is each SEO connection's latest overview of host, for the
// analytics overview.
type seoLatest struct {
	Integration string `json:"integration_id"`
	Name        string `json:"name"`
	Connector   string `json:"connector"`
	Day         string `json:"day"`
	connectors.SEOMetrics
}

func (s *Server) seoLatestByHost(ctx context.Context, ws string) (map[string][]seoLatest, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (s.integration_id, s.host) s.host, s.integration_id::text, i.name, s.connector, to_char(s.day, 'YYYY-MM-DD'), s.metrics
		FROM seo_snapshots s JOIN integrations i ON i.id = s.integration_id
		WHERE s.workspace_id = $1 AND s.report = 'overview' AND s.key = ''
		ORDER BY s.integration_id, s.host, s.day DESC`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]seoLatest{}
	for rows.Next() {
		var host string
		var l seoLatest
		var raw []byte
		if err := rows.Scan(&host, &l.Integration, &l.Name, &l.Connector, &l.Day, &raw); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &l.SEOMetrics)
		out[host] = append(out[host], l)
	}
	return out, rows.Err()
}
