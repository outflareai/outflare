// Package seosync reads SEO providers on a budget and keeps a weekly
// snapshot of each domain, so SEO numbers line up with the analytics tools
// by domain and can be compared over time.
//
// SEO providers charge per call, so every call goes through Fetch: it
// answers from a day-old copy when it can, refuses a call that would take
// the connection over its monthly budget, and records what each call cost
// in the Outcomes ledger.
package seosync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/analyticsync"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/outcomes"
	"github.com/outflareai/outflare/engine/internal/retry"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/usagelimits"
)

const (
	JobSync = "seo.sync"
	// Every domain is snapshotted this often.
	interval = 7 * 24 * time.Hour
	// Answers are kept this long, so asking twice does not pay twice.
	cacheFor = 24 * time.Hour
	// Keywords stored per domain and snapshot.
	keywordRows = 50
	// Domains followed from analytics when none are set.
	maxAutoDomains = 5
)

// Units for budgets and spend.
const (
	UnitUSD    = "USD"
	UnitAPIUse = "API unit"
)

// Settings are an SEO connection's choices, kept in its config.
type Settings struct {
	// Domains to snapshot. Unset follows the domains the analytics tools
	// already line up (at most five).
	Domains []string `json:"domains,omitempty"`
	// Country for keywords and traffic, a two-letter code. Default US.
	Country string `json:"country,omitempty"`
	// MonthlyBudget caps spend per calendar month, in Unit. Unset is the
	// default budget; 0 stops every call.
	MonthlyBudget *float64 `json:"monthly_budget,omitempty"`
	// Sync is "weekly" (default) or "off".
	Sync string `json:"sync,omitempty"`
	// LinkRows is how many links each weekly read asks for per SEO site.
	// Unset is DefaultLinkRows.
	LinkRows *int `json:"link_rows,omitempty"`
	// LinkTraffic adds the linking pages' and domains' traffic to each
	// weekly links read. Ahrefs charges about 25 more API units a link for
	// it; DataForSEO has no traffic for links.
	LinkTraffic bool `json:"link_traffic,omitempty"`
	// GapRows is how many keywords and linking domains each weekly read
	// takes of a tracked competitor, for the gaps. Unset is
	// DefaultGapRows; 0 turns gaps off.
	GapRows *int `json:"gap_rows,omitempty"`
}

// SettingsOf reads a connection's settings with the defaults filled in.
func SettingsOf(config json.RawMessage) Settings {
	var s Settings
	_ = json.Unmarshal(config, &s)
	if s.Country == "" {
		s.Country = "US"
	}
	if s.Sync == "" {
		s.Sync = "weekly"
	}
	return s
}

// Unit is what a reader's spend is counted in.
func Unit(r connectors.SEOReader) string {
	if r.Estimate(connectors.SEOQuery{Report: connectors.SEOOverview, Limit: 1}).USD > 0 {
		return UnitUSD
	}
	return UnitAPIUse
}

// DefaultBudget is the monthly budget until someone sets one: $10 of
// DataForSEO, or 10,000 Ahrefs API units (usagelimits.Starting).
func DefaultBudget(unit string) float64 {
	if unit == UnitUSD {
		return 10
	}
	return 10000
}

// Budget is a connection's monthly budget and what it has spent. It is
// the connection's monthly usage limit (usagelimits), which the
// connection's page under Connectors also sets, with a daily one.
type Budget struct {
	Unit  string  `json:"unit"`
	Limit float64 `json:"limit"`
	Spent float64 `json:"spent"`
	// Default: nobody set a budget, so the default applies.
	Default bool `json:"default"`
	// Unlimited: the connection has no monthly limit (Limit is 0).
	Unlimited bool `json:"unlimited"`
}

// Left is what can still be spent this month.
func (b Budget) Left() float64 {
	if b.Unlimited {
		return usagelimits.Max
	}
	return math.Max(0, b.Limit-b.Spent)
}

// BudgetOf works out a connection's budget for this calendar month (UTC).
func BudgetOf(ctx context.Context, q jobs.Querier, o *connectors.Opened, r connectors.SEOReader) (Budget, error) {
	return BudgetIn(ctx, q, o.WorkspaceID, o.ID, Unit(r))
}

// BudgetIn works out a connection's budget for this calendar month (UTC),
// counted in unit. Every paid read of the connection (SEO reports, rank
// checks) shares it.
func BudgetIn(ctx context.Context, q jobs.Querier, ws, id, unit string) (Budget, error) {
	l, err := usagelimits.Load(ctx, q, ws, id)
	if err != nil && !errors.Is(err, usagelimits.ErrNotFound) {
		return Budget{}, err
	}
	b := Budget{Unit: unit, Default: l.Default}
	switch {
	case l.Unit == unit && l.Monthly != nil && l.Stop:
		b.Limit = *l.Monthly
	case l.Default:
		b.Limit = DefaultBudget(unit)
	default:
		// Limits in another unit, none for the month, or ones that only
		// warn: nothing caps the month here.
		b.Unlimited = true
	}
	err = q.QueryRow(ctx, `SELECT COALESCE(sum(units), 0)::float8 FROM spend_entries
		WHERE workspace_id = $1 AND integration_id = $2 AND unit_name = $3 AND occurred_at >= date_trunc('month', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'`,
		ws, id, b.Unit).Scan(&b.Spent)
	return b, err
}

// BudgetError means a call would take the connection over a usage limit.
type BudgetError struct {
	Name   string
	Budget Budget
	Cost   float64
	// Limit is the limit it would pass.
	Limit *usagelimits.Error
}

func (e *BudgetError) Error() string {
	if e.Limit != nil {
		return e.Limit.Error()
	}
	return fmt.Sprintf("%s's monthly budget is %s and %s is spent; this call would cost about %s. Raise the budget in the connection's SEO settings, or wait for next month.",
		e.Name, Amount(e.Budget.Unit, e.Budget.Limit), Amount(e.Budget.Unit, e.Budget.Spent), Amount(e.Budget.Unit, e.Cost))
}

func (e *BudgetError) Unwrap() error {
	if e.Limit == nil {
		return nil
	}
	return e.Limit
}

// CheckBudget reports whether a call costing about cost (in b's unit) may
// run on the connection: a *BudgetError when it would pass the daily or
// monthly limit.
func CheckBudget(ctx context.Context, q jobs.Querier, ws, id, name string, b Budget, cost float64) error {
	err := usagelimits.Check(ctx, q, ws, id, b.Unit, cost)
	var le *usagelimits.Error
	if errors.As(err, &le) {
		return &BudgetError{Name: name, Budget: b, Cost: cost, Limit: le}
	}
	return err
}

// Amount writes a spend for people: $1.23 or 1,500 API units.
func Amount(unit string, v float64) string {
	if unit == UnitUSD {
		return "$" + strconv.FormatFloat(math.Round(v*100)/100, 'f', 2, 64)
	}
	s := strconv.FormatFloat(math.Round(v), 'f', 0, 64)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s + " API units"
}

func amountOf(unit string, c connectors.SEOCost) float64 {
	if unit == UnitUSD {
		return c.USD
	}
	return c.Units
}

// Answer is a report with where it came from.
type Answer struct {
	connectors.SEOReport
	Integration string    `json:"integration_id"`
	Connector   string    `json:"connector"`
	Cached      bool      `json:"cached"`
	FetchedAt   time.Time `json:"fetched_at"`
	Budget      Budget    `json:"budget"`
}

// Options say who asked and whether a kept copy will do.
type Options struct {
	Fresh   bool
	ActorID *string
}

// Fetch answers one checked query: from a copy under a day old unless
// Fresh, otherwise from the provider within the budget, recording the cost.
func Fetch(ctx context.Context, pool *pgxpool.Pool, o *connectors.Opened, r connectors.SEOReader, q connectors.SEOQuery, opt Options) (Answer, error) {
	out := Answer{Integration: o.ID, Connector: o.Connector.Definition().ID}
	if !connectors.SupportsSEO(r, q.Report) {
		return out, connectors.Errf(connectors.ErrInvalid, "%s has no %s report; it answers %s", o.Connector.Definition().Name, q.Report, strings.Join(r.SEOReports(), ", "))
	}
	b, err := BudgetOf(ctx, pool, o, r)
	if err != nil {
		return out, err
	}
	out.Budget = b
	key := "seo|" + strings.Join([]string{q.Report, q.Target, q.Country, strconv.Itoa(q.Limit)}, "|")
	if q.Traffic {
		key += "|traffic"
	}
	if q.Page {
		key += "|page"
	}
	if !opt.Fresh {
		var raw []byte
		var at time.Time
		err := pool.QueryRow(ctx, `SELECT report, fetched_at FROM analytics_cache
			WHERE workspace_id = $1 AND integration_id = $2 AND key = $3 AND fetched_at > now() - make_interval(secs => $4)`,
			o.WorkspaceID, o.ID, key, cacheFor.Seconds()).Scan(&raw, &at)
		if err == nil && json.Unmarshal(raw, &out.SEOReport) == nil {
			out.Cached, out.FetchedAt = true, at
			out.Cost = connectors.SEOCost{}
			return out, nil
		}
	}
	est := amountOf(b.Unit, r.Estimate(q))
	if err := CheckBudget(ctx, pool, o.WorkspaceID, o.ID, o.Name, b, est); err != nil {
		return out, err
	}
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	rep, callErr := r.SEOReport(cctx, o.Client, o.Creds, q)
	if spent := amountOf(b.Unit, rep.Cost); spent > 0 {
		if err := record(ctx, pool, o, b.Unit, q, rep.Cost, opt.ActorID); err != nil {
			return out, err
		}
		out.Budget.Spent += spent
	}
	if callErr != nil {
		return out, callErr
	}
	if rep.Notes == nil {
		rep.Notes = []string{}
	}
	if rep.Rows == nil && q.Report != connectors.SEOOverview {
		rep.Rows = []connectors.SEORow{}
	}
	if raw, err := json.Marshal(rep); err == nil {
		_, _ = pool.Exec(ctx, `INSERT INTO analytics_cache (workspace_id, integration_id, key, report) VALUES ($1, $2, $3, $4)
			ON CONFLICT (workspace_id, integration_id, key) DO UPDATE SET report = EXCLUDED.report, fetched_at = now()`, o.WorkspaceID, o.ID, key, raw)
	}
	out.SEOReport, out.FetchedAt = rep, time.Now().UTC()
	return out, nil
}

// record puts one call's cost in the Outcomes ledger, counted against the
// connection's budget.
func record(ctx context.Context, pool *pgxpool.Pool, o *connectors.Opened, unit string, q connectors.SEOQuery, cost connectors.SEOCost, actor *string) error {
	units := amountOf(unit, cost)
	id := o.ID
	e := outcomes.Entry{
		WorkspaceID: o.WorkspaceID, Category: outcomes.CategoryData, Provider: o.Connector.Definition().ID,
		IntegrationID: &id, Units: &units, UnitName: unit, OccurredAt: time.Now(), Source: outcomes.SourceAuto,
		Domain: q.Target, Note: fmt.Sprintf("SEO %s for %s (%s)", q.Report, q.Target, q.Country), ActorID: actor,
	}
	if cost.Estimated {
		e.Note += ", estimated from the price list"
	}
	if unit == UnitUSD {
		cents := int64(math.Round(cost.USD * 100))
		e.AmountCents, e.Currency = &cents, "USD"
	}
	_, _, err := outcomes.Record(ctx, pool, e)
	return err
}

// ---- Weekly snapshots ----

// Syncer runs SEO snapshots as jobs.
type Syncer struct {
	Pool  *pgxpool.Pool
	Store *connectors.Store
	Log   *slog.Logger
}

type payload struct {
	Integration string `json:"integration_id"`
	// NewOnly reads only the domains the connection has never read, such
	// as a brand's website added after this week's snapshot.
	NewOnly bool `json:"new_only,omitempty"`
	// Retry also reads again the domains whose last read failed, after
	// settings changed (a raised budget, say).
	Retry bool `json:"retry,omitempty"`
	// DeadPages only reads again who links to competitors' dead pages
	// whose weekly read failed, and nothing else.
	DeadPages bool `json:"dead_pages,omitempty"`
	// SiteReads only reads again the SEO sites' competitors and backlinks
	// whose weekly read failed, and nothing else.
	SiteReads bool `json:"site_reads,omitempty"`
}

// Register adds the job handler.
func (s *Syncer) Register(q *jobs.Queue) { q.Handle(JobSync, s.job) }

// Run schedules snapshots until ctx ends.
func (s *Syncer) Run(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if err := s.QueueDue(ctx); err != nil && ctx.Err() == nil {
			s.Log.Error("seo: schedule snapshots", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Syncer) readers() []string {
	var ids []string
	for _, d := range s.Store.Registry.All() {
		if c, ok := s.Store.Registry.Get(d.ID); ok && d.Status == connectors.Available {
			if _, ok := c.(connectors.SEOReader); ok {
				ids = append(ids, d.ID)
			}
		}
	}
	return ids
}

// QueueDue queues a sync of each connection that is due, or whose last
// run failed and is due a retry (retry.After).
func (s *Syncer) QueueDue(ctx context.Context) error {
	ids := s.readers()
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT i.id, i.workspace_id FROM integrations i
		WHERE i.connector = ANY($1) AND i.status = 'connected' AND COALESCE(i.config->>'sync', 'weekly') <> 'off'
		  AND NOT EXISTS (SELECT 1 FROM seo_syncs y WHERE y.integration_id = i.id AND y.last_run_at > now() - $2::interval)`,
		ids, fmt.Sprintf("%d seconds", int(interval.Seconds())))
	if err != nil {
		return err
	}
	type due struct{ id, ws string }
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.ws); err != nil {
			rows.Close()
			return err
		}
		list = append(list, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	slot := time.Now().Truncate(interval).Unix()
	for _, d := range list {
		if _, err := Enqueue(ctx, s.Pool, d.ws, d.id, fmt.Sprintf("%s:%d", d.id, slot)); err != nil {
			return err
		}
	}
	return s.tickNew(ctx, ids)
}

// tickNew reads, within the hour, a domain a connection follows but has
// never read, and soon again one whose last read failed (a brand's website added since its weekly snapshot), so new
// websites don't wait a week. That includes a connection that has never
// read anything because it had no domains when this week's snapshot ran:
// the weekly job's key is already used for the week.
func (s *Syncer) tickNew(ctx context.Context, ids []string) error {
	rows, err := s.Pool.Query(ctx, `SELECT i.id, i.workspace_id FROM integrations i
		WHERE i.connector = ANY($1) AND i.status = 'connected' AND COALESCE(i.config->>'sync', 'weekly') <> 'off'`, ids)
	if err != nil {
		return err
	}
	type conn struct{ id, ws string }
	var list []conn
	for rows.Next() {
		var c conn
		if err := rows.Scan(&c.id, &c.ws); err != nil {
			rows.Close()
			return err
		}
		list = append(list, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	slot := time.Now().Truncate(time.Hour).Unix()
	for _, c := range list {
		id := c.id
		i, err := s.Store.Get(ctx, s.Pool, c.ws, id)
		if err != nil {
			return err
		}
		domains, _, err := Domains(ctx, s.Pool, i)
		if err != nil {
			return err
		}
		var unread bool
		if err := s.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM unnest($2::text[]) h
			WHERE NOT EXISTS (SELECT 1 FROM seo_syncs y WHERE y.integration_id = $1 AND y.host = h))`, id, domains).Scan(&unread); err != nil {
			return err
		}
		if unread {
			if _, err := enqueue(ctx, s.Pool, i.WorkspaceID, payload{Integration: id, NewOnly: true}, fmt.Sprintf("%s:new:%d", id, slot)); err != nil {
				return err
			}
		}
		// A domain whose snapshot failed is read again soon (retry.After),
		// not at next week's snapshot; each retry is a job of its own.
		var failedAt int64
		if err := s.Pool.QueryRow(ctx, `SELECT COALESCE(max(extract(epoch FROM y.last_run_at))::bigint, 0) FROM seo_syncs y
			WHERE y.integration_id = $1 AND y.host = ANY($2) AND y.failures > 0
			  AND y.last_run_at < now() - `+retry.SQL("y.failures"), id, domains).Scan(&failedAt); err != nil {
			return err
		}
		if failedAt > 0 {
			if _, err := enqueue(ctx, s.Pool, i.WorkspaceID, payload{Integration: id, NewOnly: true, Retry: true}, fmt.Sprintf("%s:failed:%d", id, failedAt)); err != nil {
				return err
			}
		}
		// So is a competitor's dead page whose weekly read of who links to
		// it failed and that no read has got since, while it is still
		// fresh (ReadDeadPages reads pages gone in the last 8 days).
		var deadFailedAt int64
		if err := s.Pool.QueryRow(ctx, `SELECT COALESCE(max(extract(epoch FROM t.at))::bigint, 0) FROM (
				SELECT x.target, count(*) AS n, max(x.created_at) AS at FROM link_reads x
				WHERE x.integration_id = $1 AND x.reason = 'dead_page' AND x.status = 'failed' AND x.created_at > now() - interval '8 days'
				  AND NOT EXISTS (SELECT 1 FROM link_reads d WHERE d.workspace_id = x.workspace_id AND d.target = x.target AND d.scope = 'page' AND d.status = 'done')
				GROUP BY x.target) t
			WHERE t.at < now() - `+retry.SQL("t.n"), id).Scan(&deadFailedAt); err != nil {
			return err
		}
		if deadFailedAt > 0 {
			if _, err := enqueue(ctx, s.Pool, i.WorkspaceID, payload{Integration: id, DeadPages: true}, fmt.Sprintf("%s:dead_pages:%d", id, deadFailedAt)); err != nil {
				return err
			}
		}
		// So is an SEO site's competitor or backlink read that failed this
		// week; next week's snapshot reads it anyway.
		var siteFailedAt int64
		if err := s.Pool.QueryRow(ctx, `SELECT COALESCE(max(extract(epoch FROM f.last_failed_at))::bigint, 0) FROM seo_site_read_failures f
			WHERE f.integration_id = $1 AND f.last_failed_at > now() - interval '7 days'
			  AND f.last_failed_at < now() - `+retry.SQL("f.failures"), id).Scan(&siteFailedAt); err != nil {
			return err
		}
		if siteFailedAt > 0 {
			if _, err := enqueue(ctx, s.Pool, i.WorkspaceID, payload{Integration: id, SiteReads: true}, fmt.Sprintf("%s:site_reads:%d", id, siteFailedAt)); err != nil {
				return err
			}
		}
	}
	return nil
}

// Enqueue asks for a snapshot of one connection. Key de-duplicates; "" is
// a snapshot someone asked for now.
func Enqueue(ctx context.Context, q jobs.Querier, ws, integrationID, key string) (int64, error) {
	if key == "" {
		key = fmt.Sprintf("%s:now:%d", integrationID, time.Now().Unix()/30)
	}
	return enqueue(ctx, q, ws, payload{Integration: integrationID}, key)
}

// EnqueueRetry asks for a read of the domains a connection has never read
// or failed to read last time, such as after its budget was raised. It
// costs nothing for domains that read fine.
func EnqueueRetry(ctx context.Context, q jobs.Querier, ws, integrationID string) (int64, error) {
	return enqueue(ctx, q, ws, payload{Integration: integrationID, NewOnly: true, Retry: true}, fmt.Sprintf("%s:retry:%d", integrationID, time.Now().Unix()/30))
}

func enqueue(ctx context.Context, q jobs.Querier, ws string, p payload, key string) (int64, error) {
	return jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobSync, WorkspaceID: &ws, Payload: p, IdempotencyKey: key, MaxAttempts: 2})
}

// Watch reads a new SEO connection straight away, so its numbers show
// minutes after connecting, not at the next weekly snapshot.
func (s *Syncer) Watch() {
	s.Store.OnConnect = append(s.Store.OnConnect, func(ctx context.Context, tx pgx.Tx, i connectors.Integration) error {
		c, ok := s.Store.Registry.Get(i.Connector)
		if !ok {
			return nil
		}
		if _, ok := c.(connectors.SEOReader); !ok {
			return nil
		}
		_, err := Enqueue(ctx, tx, i.WorkspaceID, i.ID, i.ID+":connected")
		return err
	})
}

func (s *Syncer) job(ctx context.Context, job jobs.Job) error {
	var p payload
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return nil
	}
	if p.DeadPages {
		return s.retryDeadPages(ctx, *job.WorkspaceID, p.Integration)
	}
	if p.SiteReads {
		return s.retrySiteReads(ctx, *job.WorkspaceID, p.Integration)
	}
	res, err := s.sync(ctx, *job.WorkspaceID, p.Integration, p.NewOnly, p.Retry)
	s.report(ctx, *job.WorkspaceID, p.Integration, res, err)
	if err != nil && connectors.KindOf(err) == connectors.ErrUnavailable && !errors.Is(err, connectors.ErrNotFound) {
		return err
	}
	// Budgets, refused keys and bad domains will not fix themselves; the
	// error shows on the sync status.
	return nil
}

// Result is what one snapshot stored and cost.
type Result struct {
	Domains []string `json:"domains"`
	Rows    int      `json:"rows"`
	Cost    float64  `json:"cost"`
	Unit    string   `json:"unit"`
	// Links is each SEO site's links read, for connections that read them.
	Links []LinkRun `json:"links"`
	// Competitors is each SEO site's competitors read.
	Competitors []CompetitorRun `json:"competitors"`
	// DeadPages is each SEO site's weekly read of who links to its
	// competitors' dead pages.
	DeadPages []DeadPageRun `json:"dead_pages"`
}

// Domains is what a connection snapshots: its own list, or else the
// websites of the workspace it serves, at most five. Brands' domains come
// first (they are what the SEO overview shows), then the sites being
// audited, then the domains the analytics tools line up. A subdomain of a
// domain already followed is left out: the providers read subdomains with
// their domain.
func Domains(ctx context.Context, q *pgxpool.Pool, i connectors.Integration) ([]string, bool, error) {
	set := SettingsOf(i.Config)
	if set.Domains != nil {
		return set.Domains, true, nil
	}
	rows, err := q.Query(ctx, `
		SELECT d, 0, b.created_at FROM integrations i, brands b, unnest(b.domains || ARRAY[b.website]) d
		WHERE i.id = $2 AND b.workspace_id = $1 AND b.status = 'active' AND d <> '' AND integration_serves(i, b.id)
		UNION ALL
		SELECT s.host, 1, s.created_at FROM integrations i, seo_sites s
		WHERE i.id = $2 AND s.workspace_id = $1 AND (s.brand_id IS NULL OR integration_serves(i, s.brand_id))
		UNION ALL
		SELECT a.host, 2, NULL FROM integrations i, analytics_syncs a
		WHERE i.id = $2 AND a.workspace_id = $1 AND a.host <> ''
		  AND (analytics_site_brand($1, a.integration_id, a.site, a.host) IS NULL OR integration_serves(i, analytics_site_brand($1, a.integration_id, a.site, a.host)))
		ORDER BY 2, 3, 1`, i.WorkspaceID, i.ID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var raw string
		var rank int
		var at *time.Time
		if err := rows.Scan(&raw, &rank, &at); err != nil {
			return nil, false, err
		}
		if h := Host(raw); h != "" && !covered(out, h) && len(out) < maxAutoDomains {
			out = append(out, h)
		}
	}
	return out, false, rows.Err()
}

// covered reports whether host is one of hosts or a subdomain of one.
func covered(hosts []string, host string) bool {
	for _, h := range hosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// report records what a snapshot job did, or why it failed.
func (s *Syncer) report(ctx context.Context, ws, integrationID string, res Result, err error) {
	props := map[string]any{"integration_id": integrationID, "domains": len(res.Domains), "rows": res.Rows, "cost": res.Cost, "unit": res.Unit}
	if err == nil {
		track.Emit(ctx, track.Event{Name: "seo.synced", WorkspaceID: ws, ActorType: "system", Surface: "job", Outcome: track.Success, Props: props})
		return
	}
	code := "provider_error"
	var be *BudgetError
	switch {
	case errors.As(err, &be):
		code = "budget_reached"
	case connectors.KindOf(err) == connectors.ErrAuth:
		code = "connector_auth"
	}
	props["kind"] = connectors.KindOf(err)
	track.Fail(ctx, s.Log, track.Event{Name: "seo.sync_failed", WorkspaceID: ws, Surface: "job", Props: props}, code, err)
}

// SyncOnce snapshots every domain of one connection now: its overview and
// top keywords.
func (s *Syncer) SyncOnce(ctx context.Context, ws, integrationID string) (Result, error) {
	return s.sync(ctx, ws, integrationID, false, false)
}

// sync snapshots a connection's domains; newOnly reads only the ones it
// has never read, and nothing else, and retry adds the ones whose last
// read failed.
func (s *Syncer) sync(ctx context.Context, ws, integrationID string, newOnly, retry bool) (Result, error) {
	o, err := s.Store.Open(ctx, s.Pool, ws, integrationID)
	if err != nil {
		return Result{}, err
	}
	r, ok := o.Connector.(connectors.SEOReader)
	if !ok {
		return Result{}, connectors.Errf(connectors.ErrInvalid, "%s is not an SEO connection", o.Name)
	}
	res := Result{Domains: []string{}, Unit: Unit(r), Links: []LinkRun{}, Competitors: []CompetitorRun{}, DeadPages: []DeadPageRun{}}
	domains, _, err := Domains(ctx, s.Pool, o.Integration)
	if err != nil {
		return res, err
	}
	if newOnly {
		if err := s.Pool.QueryRow(ctx, `SELECT COALESCE(array_agg(h ORDER BY n), '{}') FROM unnest($2::text[]) WITH ORDINALITY AS u(h, n)
			WHERE NOT EXISTS (SELECT 1 FROM seo_syncs y WHERE y.integration_id = $1 AND y.host = h AND ($3 IS FALSE OR y.last_error IS NULL))`, integrationID, domains, retry).Scan(&domains); err != nil {
			return res, err
		}
	}
	set := SettingsOf(o.Config)
	day := time.Now().UTC().Format(time.DateOnly)
	var firstErr error
	for n, host := range domains {
		res.Domains = append(res.Domains, host)
		var hostErr error
		var cost float64
		for _, report := range []string{connectors.SEOOverview, connectors.SEOKeywords} {
			q, err := connectors.CheckSEOQuery(connectors.SEOQuery{Report: report, Target: host, Country: set.Country, Limit: keywordRows})
			if err == nil {
				var a Answer
				a, err = Fetch(ctx, s.Pool, o, r, q, Options{Fresh: true})
				cost += amountOf(res.Unit, a.Cost)
				if err == nil {
					var n int
					n, err = s.store(ctx, ws, o, host, day, a.SEOReport)
					res.Rows += n
				}
			}
			if err != nil {
				hostErr = err
				break
			}
		}
		res.Cost += cost
		s.finish(ctx, ws, integrationID, host, cost, hostErr)
		if hostErr != nil && firstErr == nil {
			firstErr = hostErr
		}
		var be *BudgetError
		if errors.As(hostErr, &be) || connectors.KindOf(hostErr) == connectors.ErrAuth {
			// The rest are not read either, for the same reason; saying so
			// shows why their numbers are missing.
			for _, rest := range domains[n+1:] {
				s.finish(ctx, ws, integrationID, rest, 0, hostErr)
			}
			break
		}
	}
	var be *BudgetError
	// Each SEO site's competitors, then its links, while the budget lasts.
	var sites []LinkSite
	if !newOnly && !errors.As(firstErr, &be) && connectors.KindOf(firstErr) != connectors.ErrAuth {
		if sites, err = LinkSites(ctx, s.Pool, ws); err != nil {
			return res, err
		}
	}
	for _, site := range sites {
		run, err := s.SyncSiteCompetitors(ctx, o, r, site, set.Country, time.Now())
		res.Competitors = append(res.Competitors, run)
		res.Cost += run.Cost
		s.noteSiteRead(ctx, ws, o.ID, site.ID, "competitors", err)
		if err != nil {
			code := "provider_error"
			if errors.As(err, &be) {
				code = "budget_reached"
			}
			track.Fail(ctx, s.Log, track.Event{Name: "seo_competitors.sync_failed", WorkspaceID: ws, Surface: "job",
				Props: map[string]any{"connector": o.Connector.Definition().ID, "site_id": site.ID, "kind": connectors.KindOf(err)}}, code, err)
		} else {
			track.Emit(ctx, track.Event{Name: "seo_competitors.synced", WorkspaceID: ws, ActorType: "system", Surface: "job", Outcome: track.Success,
				Props: map[string]any{"connector": o.Connector.Definition().ID, "site_id": site.ID, "suggested": run.Suggested, "checked": run.Checked, "gaps": run.Gaps, "events": run.Events}})
		}
		if connectors.KindOf(err) == connectors.ErrAuth && firstErr == nil {
			firstErr = err
		}
		if errors.As(err, &be) || connectors.KindOf(err) == connectors.ErrAuth {
			sites = nil
			break
		}
	}
	if connectors.SupportsSEO(r, connectors.SEOLinks) {
		for _, site := range sites {
			run, err := SyncSiteLinks(ctx, s.Pool, o, r, site, time.Now())
			res.Links = append(res.Links, run)
			res.Cost += run.Cost
			s.noteSiteRead(ctx, ws, o.ID, site.ID, "backlinks", err)
			if err != nil {
				code := "provider_error"
				if errors.As(err, &be) {
					code = "budget_reached"
				}
				track.Fail(ctx, s.Log, track.Event{Name: "backlinks.sync_failed", WorkspaceID: ws, Surface: "job",
					Props: map[string]any{"connector": o.Connector.Definition().ID, "site_id": site.ID, "kind": connectors.KindOf(err)}}, code, err)
			}
			// A failed read is on its run; only a refused key fails the
			// whole snapshot.
			if connectors.KindOf(err) == connectors.ErrAuth && firstErr == nil {
				firstErr = err
			}
			if errors.As(err, &be) || connectors.KindOf(err) == connectors.ErrAuth {
				break
			}
		}
	}
	// Who links to competitors' pages that just went, within the share
	// of the budget the workspace allows.
	if connectors.SupportsSEO(r, connectors.SEOLinks) && len(sites) > 0 && connectors.KindOf(firstErr) != connectors.ErrAuth {
		res.DeadPages = s.readDeadPages(ctx, o, r, sites)
		for _, d := range res.DeadPages {
			res.Cost += d.Cost
		}
	}
	if connectors.KindOf(firstErr) == connectors.ErrAuth {
		_ = s.Store.Checked(ctx, s.Pool, ws, integrationID, nil, firstErr)
	}
	return res, firstErr
}

func (s *Syncer) store(ctx context.Context, ws string, o *connectors.Opened, host, day string, rep connectors.SEOReport) (int, error) {
	put := func(key, url, kind string, m connectors.SEOMetrics) error {
		raw, _ := json.Marshal(m)
		_, err := s.Pool.Exec(ctx, `
			INSERT INTO seo_snapshots (workspace_id, integration_id, connector, host, report, day, country, key, url, kind, metrics)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			ON CONFLICT (integration_id, host, report, day, key) DO UPDATE SET country = EXCLUDED.country, url = EXCLUDED.url,
				kind = EXCLUDED.kind, metrics = EXCLUDED.metrics, synced_at = now()`,
			ws, o.ID, o.Connector.Definition().ID, host, rep.Report, day, rep.Country, trunc(key, 500), trunc(url, 1000), kind, raw)
		return err
	}
	n := 0
	if rep.Report == connectors.SEOOverview {
		if err := put("", "", "", rep.Totals); err != nil {
			return n, err
		}
		return 1, nil
	}
	// A new snapshot of a list replaces the day's old one.
	if _, err := s.Pool.Exec(ctx, `DELETE FROM seo_snapshots WHERE integration_id = $1 AND host = $2 AND report = $3 AND day = $4`, o.ID, host, rep.Report, day); err != nil {
		return n, err
	}
	for _, row := range rep.Rows {
		if row.Key == "" {
			continue
		}
		if err := put(row.Key, row.URL, row.Kind, row.SEOMetrics); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (s *Syncer) finish(ctx context.Context, ws, id, host string, cost float64, runErr error) {
	var msg, code *string
	outcome := "success"
	if runErr != nil {
		m, c := runErr.Error(), "connector_"+connectors.KindOf(runErr)
		var be *BudgetError
		if errors.As(runErr, &be) {
			c = "budget_reached"
		}
		msg, code, outcome = &m, &c, c
	}
	metrics.SyncRun("seo", outcome)
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO seo_syncs (integration_id, host, workspace_id, last_run_at, last_ok_at, last_error, last_cost, failures, last_error_code)
		VALUES ($1, $2, $3, now(), CASE WHEN $4::text IS NULL THEN now() END, $4, $5, CASE WHEN $4::text IS NULL THEN 0 ELSE 1 END, $6)
		ON CONFLICT (integration_id, host) DO UPDATE SET last_run_at = now(),
			last_ok_at = COALESCE(EXCLUDED.last_ok_at, seo_syncs.last_ok_at), last_error = $4, last_cost = $5, last_error_code = $6,
			failures = CASE WHEN $4::text IS NULL THEN 0 ELSE seo_syncs.failures + 1 END`,
		id, host, ws, msg, cost, code)
	if err != nil {
		s.Log.Warn("seo: record snapshot", "integration", id, "host", host, "err", err)
	}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Cut on a character boundary, so the text stays valid UTF-8.
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Host normalises a domain the way the analytics tools line up.
func Host(raw string) string { return analyticsync.Host(raw) }
