package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/verifiers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/seosync"
	"github.com/outflareai/outflare/engine/internal/sysmail"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/usagelimits"
)

// Usage limits on paid connections: what each connection that costs money
// per call (SEO data, lead data, email verification, page reads) used today
// and this month, and the daily and monthly limits that stop Outflare from
// making more of those calls. The calls run on the workspace's own account
// with the provider; usagelimits has the counting and the checks.

const connectionUsageDays = 30

func (s *Server) routesConnectionUsage(mux *http.ServeMux) {
	read, manage := auth.ScopeIntegrationsRead, auth.ScopeIntegrationsManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/connection-usage", s.inWorkspace(read, s.listConnectionUsage))
	mux.HandleFunc("GET /v1/workspaces/{ws}/integrations/{id}/usage", s.inWorkspace(read, s.getConnectionUsage))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/integrations/{id}/usage/limits", s.inWorkspace(manage, s.putConnectionLimits))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/integrations/{id}/usage/limits", s.inWorkspace(manage, s.resetConnectionLimits))
}

// usageUnits are the units a connection's usage can be counted in, its
// usual one first, or none when its calls cost nothing per call (or are AI
// calls, which have their own limits under Settings → AI usage).
func (s *Server) usageUnits(ctx context.Context, i connectors.Integration) ([]string, error) {
	c, ok := s.integrations.Registry.Get(i.Connector)
	if !ok {
		return nil, nil
	}
	d := c.Definition()
	if d.Category == connectors.CategoryModel {
		return nil, nil
	}
	var units []string
	switch {
	case d.Has(connectors.CapSEO):
		if r, ok := c.(connectors.SEOReader); ok {
			units = append(units, seosync.Unit(r))
		}
	case d.Has(connectors.CapSERP):
		units = append(units, usagelimits.USD)
	case d.Has(connectors.CapCrawlFetch):
		units = append(units, "token")
	case d.Has(connectors.CapVerify) && d.ID != verifiers.BuiltinID,
		d.Has(connectors.CapSearchPeople), d.Has(connectors.CapEnrichPerson), d.Has(connectors.CapFindEmail):
		units = append(units, "credit")
	case d.Has(connectors.CapWebSearch):
		units = append(units, usagelimits.USD)
	}
	// And whatever its calls were booked in lately.
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT CASE WHEN units IS NULL AND currency = 'USD' THEN 'USD' ELSE unit_name END
		FROM spend_entries WHERE workspace_id = $1 AND integration_id = $2 AND occurred_at > now() - interval '90 days'
		  AND (units IS NOT NULL OR amount_cents IS NOT NULL)`, i.WorkspaceID, i.ID)
	if err != nil {
		return nil, err
	}
	booked, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	slices.Sort(booked)
	for _, u := range booked {
		if usagelimits.ValidUnit(u) && !slices.Contains(units, u) {
			units = append(units, u)
		}
	}
	return units, nil
}

type connectionUsage struct {
	IntegrationID string             `json:"integration_id"`
	Name          string             `json:"name"`
	Connector     string             `json:"connector"`
	Units         []string           `json:"units"`
	Limits        usagelimits.Limits `json:"limits"`
	Used          usagelimits.Used   `json:"used"`
	// Estimated: the provider doesn't say what each call cost, so the
	// usage is worked out from its price list.
	Estimated bool                   `json:"estimated"`
	Days      []usagelimits.DayUsage `json:"days,omitempty"`
	Alerts    []connectionAlert      `json:"alerts,omitempty"`
}

type connectionAlert struct {
	Period      string    `json:"period"`
	PeriodStart string    `json:"period_start"`
	Level       int       `json:"level"`
	Unit        string    `json:"unit"`
	Limit       float64   `json:"limit"`
	Used        float64   `json:"used"`
	SentAt      time.Time `json:"sent_at"`
}

// connectionUsageOf is one paid connection's usage, or ok false when its
// calls cost nothing per call.
func (s *Server) connectionUsageOf(ctx context.Context, i connectors.Integration, detail bool) (connectionUsage, bool, error) {
	units, err := s.usageUnits(ctx, i)
	if err != nil || len(units) == 0 {
		return connectionUsage{}, false, err
	}
	l, err := usagelimits.Load(ctx, s.pool, i.WorkspaceID, i.ID)
	if err != nil {
		return connectionUsage{}, false, err
	}
	if l.Unit == "" {
		l.Unit = units[0]
	}
	if !slices.Contains(units, l.Unit) {
		units = append(units, l.Unit)
	}
	out := connectionUsage{IntegrationID: i.ID, Name: i.Name, Connector: i.Connector, Units: units, Limits: l,
		Estimated: i.Connector == "ahrefs" || l.Unit == "credit"}
	if out.Used, err = usagelimits.Spent(ctx, s.pool, i.WorkspaceID, i.ID, l.Unit, time.Now()); err != nil {
		return out, false, err
	}
	if !detail {
		return out, true, nil
	}
	day, _ := usagelimits.Starts(time.Now())
	if out.Days, err = usagelimits.Days(ctx, s.pool, i.WorkspaceID, i.ID, l.Unit, day.AddDate(0, 0, -(connectionUsageDays-1))); err != nil {
		return out, false, err
	}
	rows, err := s.pool.Query(ctx, `SELECT period, to_char(period_start, 'YYYY-MM-DD'), level, unit, limit_value::float8, used::float8, sent_at
		FROM connection_limit_alerts WHERE workspace_id = $1 AND integration_id = $2 ORDER BY sent_at DESC, level DESC LIMIT 10`, i.WorkspaceID, i.ID)
	if err != nil {
		return out, false, err
	}
	out.Alerts, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (connectionAlert, error) {
		var a connectionAlert
		return a, r.Scan(&a.Period, &a.PeriodStart, &a.Level, &a.Unit, &a.Limit, &a.Used, &a.SentAt)
	})
	return out, true, err
}

// listConnectionUsage is every paid connection's usage today and this
// month, with its limits.
func (s *Server) listConnectionUsage(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	list, err := s.integrations.List(r.Context(), s.pool, a.WorkspaceID, "")
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := []connectionUsage{}
	for _, i := range list {
		u, ok, err := s.connectionUsageOf(r.Context(), i, false)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if ok {
			out = append(out, u)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// usageIntegration loads a paid connection, or answers.
func (s *Server) usageIntegration(w http.ResponseWriter, r *http.Request) (connectors.Integration, connectionUsage, bool) {
	if !s.needIntegrations(w, r) {
		return connectors.Integration{}, connectionUsage{}, false
	}
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "integration not found")
		return connectors.Integration{}, connectionUsage{}, false
	}
	i, err := s.integrations.Get(r.Context(), s.pool, a.WorkspaceID, id)
	if errors.Is(err, connectors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "integration not found")
		return i, connectionUsage{}, false
	}
	if err != nil {
		s.internal(w, r, err)
		return i, connectionUsage{}, false
	}
	u, ok, err := s.connectionUsageOf(r.Context(), i, true)
	if err != nil {
		s.internal(w, r, err)
		return i, u, false
	}
	if !ok {
		writeError(w, r, http.StatusNotFound, "not_found", "this connection doesn't cost anything per call, so it has no usage limits")
		return i, u, false
	}
	return i, u, true
}

func (s *Server) getConnectionUsage(w http.ResponseWriter, r *http.Request) {
	if _, u, ok := s.usageIntegration(w, r); ok {
		writeJSON(w, http.StatusOK, u)
	}
}

// putConnectionLimits sets a connection's daily and monthly limits.
func (s *Server) putConnectionLimits(w http.ResponseWriter, r *http.Request) {
	i, u, ok := s.usageIntegration(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	var req struct {
		Unit     *string  `json:"unit"`
		Daily    *float64 `json:"daily_limit"`
		Monthly  *float64 `json:"monthly_limit"`
		HardStop *bool    `json:"hard_stop"`
	}
	if !decode(w, r, &req) {
		return
	}
	bad := func(msg string) { writeError(w, r, http.StatusBadRequest, "invalid_request", msg) }
	next := usagelimits.Limits{Unit: u.Limits.Unit, Daily: req.Daily, Monthly: req.Monthly, Stop: true}
	if req.Unit != nil {
		if !slices.Contains(u.Units, *req.Unit) {
			bad(fmt.Sprintf("unit is one of %q for this connection", u.Units))
			return
		}
		next.Unit = *req.Unit
	}
	if req.HardStop != nil {
		next.Stop = *req.HardStop
	}
	for name, v := range map[string]*float64{"daily_limit": next.Daily, "monthly_limit": next.Monthly} {
		if v != nil && (*v < 0 || *v > usagelimits.Max || math.IsNaN(*v)) {
			bad(name + " is 0 or more (0 stops every call), or null for no limit")
			return
		}
	}
	if next.Daily != nil && next.Monthly != nil && *next.Daily > *next.Monthly {
		bad("daily_limit is more than monthly_limit, so it would never apply")
		return
	}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		actor := a.principal.ActorID
		if err := usagelimits.Save(r.Context(), tx, a.WorkspaceID, i.ID, next, &actor); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "connection_limits.updated", "integration", i.ID, map[string]any{
			"connector": i.Connector, "unit": next.Unit, "daily_limit": next.Daily, "monthly_limit": next.Monthly, "hard_stop": next.Stop})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.getConnectionUsage(w, r)
}

// resetConnectionLimits removes a connection's own limits: the connector's
// starting limit (Ahrefs and DataForSEO have one) or none applies again.
func (s *Server) resetConnectionLimits(w http.ResponseWriter, r *http.Request) {
	i, _, ok := s.usageIntegration(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if err := usagelimits.Reset(r.Context(), tx, a.WorkspaceID, i.ID); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "connection_limits.reset", "integration", i.ID, map[string]any{"connector": i.Connector})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.getConnectionUsage(w, r)
}

// connectionLimitAlertJob emails one owner or admin that a connection used
// most of a limit, or reached it. It is Outflare's own account email
// (sysmail): it names the workspace and the connection, and quotes nothing
// else from the workspace's data.
func (s *Server) connectionLimitAlertJob(ctx context.Context, job jobs.Job) error {
	var p struct {
		UserID        string `json:"user_id"`
		IntegrationID string `json:"integration_id"`
		Period        string `json:"period"`
		PeriodStart   string `json:"period_start"`
		Level         int    `json:"level"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || !validUUID(p.UserID) || !validUUID(p.IntegrationID) {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	start, err := time.Parse(time.DateOnly, p.PeriodStart)
	if err != nil {
		return fmt.Errorf("%w: bad period start", jobs.ErrPermanent)
	}
	var email, workspace, name, unit string
	var limit, used float64
	var stop *bool
	err = s.pool.QueryRow(ctx, `
		SELECT u.email, w.name, i.name, a.unit, a.limit_value::float8, a.used::float8, l.hard_stop
		FROM connection_limit_alerts a
		JOIN workspaces w ON w.id = a.workspace_id
		JOIN integrations i ON i.id = a.integration_id AND i.workspace_id = a.workspace_id
		LEFT JOIN connection_limits l ON l.integration_id = a.integration_id AND l.workspace_id = a.workspace_id
		JOIN memberships m ON m.workspace_id = a.workspace_id AND m.role IN ('owner', 'admin')
		JOIN users u ON u.id = m.user_id AND u.email_verified_at IS NOT NULL
		WHERE a.workspace_id = $1 AND a.integration_id = $2 AND a.period = $3 AND a.period_start = $4 AND a.level = $5 AND u.id = $6`,
		ws, p.IntegrationID, p.Period, start, p.Level, p.UserID).Scan(&email, &workspace, &name, &unit, &limit, &used, &stop)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // no longer an admin, or the connection is gone
	}
	if err != nil {
		return err
	}
	ev := track.Event{Name: "connection_limit.emailed", WorkspaceID: ws, UserID: p.UserID, ActorType: "system", Surface: "job",
		Props: map[string]any{"level": p.Level, "period": p.Period}}
	url := s.publicURL + "/" + s.workspaceSlug(ctx, ws) + "/connectors/" + p.IntegrationID
	if err := s.sendAccountEmail(ctx, connectionLimitMessage(url, email, workspace, name, p.Period, unit, p.Level, limit, used, stop == nil || *stop)); err != nil {
		track.Fail(ctx, s.log, ev, "email_failed", err)
		return err
	}
	track.Emit(ctx, ev)
	return nil
}

func connectionLimitMessage(url, email, workspace, name, period, unit string, level int, limit, used float64, stop bool) sysmail.Message {
	when, next := "daily", "tomorrow (UTC)"
	if period == usagelimits.Month {
		when, next = "monthly", "next month"
	}
	m := sysmail.Email{To: email, Button: "See usage and limits", URL: url,
		Note: "These calls run on your own account with the provider, and the provider bills you for them. Outflare doesn't pay for them, so set a spend limit with the provider too."}
	body := fmt.Sprintf("%s in %s has used %s of its %s limit of %s.", name, workspace, usagelimits.Amount(unit, used), when, usagelimits.Amount(unit, limit))
	switch {
	case level >= 100 && stop:
		m.Subject = "Outflare paused " + name + ": " + when + " limit reached"
		m.Heading = name + " reached its " + when + " limit"
		m.Preview = name + " reached its " + when + " usage limit."
		m.Body = []string{body, "Outflare makes no more calls through it until " + next + ", or until an owner or admin raises the limit on the connection's page."}
	case level >= 100:
		m.Subject = name + " reached its " + when + " limit in Outflare"
		m.Heading = name + " reached its " + when + " limit"
		m.Preview = name + " reached its " + when + " usage limit."
		m.Body = []string{body, "This limit only warns, so calls go on. An owner or admin can make it stop calls on the connection's page."}
	default:
		m.Subject = name + " is at 80% of its " + when + " limit in Outflare"
		m.Heading = name + " is at 80% of its " + when + " limit"
		m.Preview = name + " is close to its " + when + " usage limit."
		m.Body = []string{body, "When it reaches the limit, Outflare stops calls through it until " + next + ". An owner or admin can change the limit on the connection's page."}
	}
	return m.Message()
}
