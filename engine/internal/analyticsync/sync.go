// Package analyticsync copies what each connected analytics tool reports
// into Postgres, one row per site, report, day and key, so people and
// agents can query history, compare tools for the same website and add
// numbers up without calling the providers each time.
//
// A connection's first run stores the last 90 days of traffic and the
// last 7 days of top pages, sources, search queries and events. Later runs
// re-read the last few days, because providers keep revising recent
// numbers (Search Console lags by two to three days). Runs are spaced so
// each tool stays well inside its rate limits.
package analyticsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

const (
	JobSync = "analytics.sync"
	// Every connection syncs this often, or once a day for tools whose
	// API allows only a few calls a day.
	interval      = 6 * time.Hour
	slowInterval  = 24 * time.Hour
	backfillDays  = 90 // traffic on the first run
	breakdownDays = 7  // pages, sources, queries and events on the first run
	recentDays    = 3  // re-read on every later run
	rowsPerDay    = 50
	// MaxSites is how many of a login's sites sync without being switched
	// on one by one; the rest are listed but off.
	MaxSites = 20
)

// Syncer runs analytics syncs as jobs.
type Syncer struct {
	Pool  *pgxpool.Pool
	Store *connectors.Store
	Log   *slog.Logger
	// Now is the clock; tests set it.
	Now func() time.Time
}

type payload struct {
	Integration string `json:"integration_id"`
}

// Register adds the job handler.
func (s *Syncer) Register(q *jobs.Queue) { q.Handle(JobSync, s.job) }

func (s *Syncer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Run schedules syncs until ctx ends.
func (s *Syncer) Run(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		if err := s.tick(ctx); err != nil && ctx.Err() == nil {
			s.Log.Error("analytics: schedule syncs", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Syncer) readers() (fast, slow []string) {
	for _, d := range s.Store.Registry.All() {
		c, ok := s.Store.Registry.Get(d.ID)
		if !ok || d.Status != connectors.Available {
			continue
		}
		r, ok := c.(connectors.AnalyticsReader)
		if !ok {
			continue
		}
		if ca, ok := r.(connectors.AnalyticsCacher); ok && ca.CacheFor() >= 6*time.Hour {
			slow = append(slow, d.ID)
		} else {
			fast = append(fast, d.ID)
		}
	}
	return fast, slow
}

func (s *Syncer) tick(ctx context.Context) error {
	fast, slow := s.readers()
	for _, group := range []struct {
		ids   []string
		every time.Duration
	}{{fast, interval}, {slow, slowInterval}} {
		if len(group.ids) == 0 {
			continue
		}
		rows, err := s.Pool.Query(ctx, `
			SELECT i.id, i.workspace_id FROM integrations i
			WHERE i.connector = ANY($1) AND i.status = 'connected'
			  AND NOT COALESCE((i.config->>'sync_paused')::boolean, false)
			  AND COALESCE((SELECT max(a.last_run_at) FROM analytics_syncs a WHERE a.integration_id = i.id), '-infinity') < now() - $2::interval`,
			group.ids, fmt.Sprintf("%d seconds", int(group.every.Seconds())))
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
		slot := s.now().Truncate(group.every).Unix()
		for _, d := range list {
			if _, err := Enqueue(ctx, s.Pool, d.ws, d.id, fmt.Sprintf("%s:%d", d.id, slot)); err != nil {
				return err
			}
		}
	}
	return nil
}

// Enqueue asks for a sync of one connection. Key de-duplicates; "" is a
// sync someone asked for now.
func Enqueue(ctx context.Context, q jobs.Querier, ws, integrationID, key string) (int64, error) {
	if key == "" {
		key = fmt.Sprintf("%s:now:%d", integrationID, time.Now().Unix()/30)
	}
	return jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobSync, WorkspaceID: &ws, Payload: payload{Integration: integrationID},
		IdempotencyKey: key, MaxAttempts: 3})
}

func (s *Syncer) job(ctx context.Context, job jobs.Job) error {
	var p payload
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return nil
	}
	_, err := s.SyncOnce(ctx, *job.WorkspaceID, p.Integration)
	if errors.Is(err, connectors.ErrNotFound) {
		return nil
	}
	if err != nil && connectors.KindOf(err) != connectors.ErrUnavailable {
		// A refused key or a missing site will not fix itself; the error
		// shows on the sync status.
		return nil
	}
	return err
}

// Result is what one run stored.
type Result struct {
	Rows  int `json:"rows"`
	Calls int `json:"calls"`
}

// SiteConfig is one site's settings on a connection: the domain that
// lines it up with other tools when the tool shows none, whether the
// sync leaves it out, and the events that count as conversions.
type SiteConfig struct {
	ID               string   `json:"id"`
	Domain           string   `json:"domain,omitempty"`
	Off              bool     `json:"off,omitempty"`
	ConversionEvents []string `json:"conversion_events,omitempty"`
}

// Config is the part of a connection's config about sites. Site is the
// default for live reports; Domain is its domain from before connections
// read several sites.
type Config struct {
	Site   string       `json:"site"`
	Domain string       `json:"domain"`
	Sites  []SiteConfig `json:"sites"`
}

// ParseConfig reads the site settings from a connection's config.
func ParseConfig(raw []byte) Config {
	var c Config
	_ = json.Unmarshal(raw, &c)
	return c
}

// SitePlan is one site a connection can read, the domain it counts
// under, and whether the sync stores it.
type SitePlan struct {
	connectors.AnalyticsSite
	// Host is the domain the site lines up under; "" when unknown.
	Host string `json:"host"`
	// Domain is the domain someone set, overriding the tool's own.
	Domain string `json:"domain"`
	Sync   bool   `json:"sync"`
	// ConversionEvents are the events that count as a conversion when
	// visits are counted by landing page.
	ConversionEvents []string `json:"conversion_events"`
}

// Plan lines up the sites a tool lists with the connection's settings.
// Every site syncs unless switched off, up to MaxSites; sites past that
// sync only when switched on.
func Plan(cfg Config, sites []connectors.AnalyticsSite) []SitePlan {
	set := map[string]SiteConfig{}
	for _, x := range cfg.Sites {
		set[x.ID] = x
	}
	if len(sites) == 0 && cfg.Site != "" {
		// Some tools cannot list sites; the one someone named still syncs.
		sites = []connectors.AnalyticsSite{{ID: cfg.Site}}
	}
	out := make([]SitePlan, 0, len(sites))
	on := 0
	for _, x := range sites {
		p := SitePlan{AnalyticsSite: x, ConversionEvents: []string{}}
		c, named := set[x.ID]
		if c.ConversionEvents != nil {
			p.ConversionEvents = c.ConversionEvents
		}
		p.Domain = Host(c.Domain)
		if p.Domain == "" && x.ID == cfg.Site {
			p.Domain = Host(cfg.Domain)
		}
		if p.Host = p.Domain; p.Host == "" {
			p.Host = HostOf(x)
		}
		if named {
			p.Sync = !c.Off
		} else {
			p.Sync = on < MaxSites
		}
		if p.Sync {
			on++
		}
		out = append(out, p)
	}
	return out
}

// SyncOnce syncs every site of one connection that is switched on.
func (s *Syncer) SyncOnce(ctx context.Context, ws, integrationID string) (Result, error) {
	var res Result
	o, err := s.Store.Open(ctx, s.Pool, ws, integrationID)
	if err != nil {
		if connectors.KindOf(err) == connectors.ErrAuth {
			s.finish(ctx, ws, integrationID, "", "", "", res, err)
		}
		return res, err
	}
	reader, ok := o.Connector.(connectors.AnalyticsReader)
	if !ok {
		return res, connectors.Errf(connectors.ErrInvalid, "%s is not an analytics connection", o.Name)
	}
	res.Calls++
	sites, err := reader.Sites(ctx, o.Client, o.Creds)
	if err != nil {
		s.finish(ctx, ws, integrationID, "", "", "", res, err)
		return res, err
	}
	var firstErr error
	cfg := ParseConfig(o.Config)
	plan := Plan(cfg, sites)
	// A site someone switched off while a run was storing it can keep that
	// run's rows; each run clears what switched-off sites left.
	off := []string{}
	for _, x := range cfg.Sites {
		if x.Off {
			off = append(off, x.ID)
		}
	}
	for _, q := range []string{
		`DELETE FROM analytics_daily WHERE integration_id = $1 AND site = ANY($2)`,
		`DELETE FROM analytics_syncs WHERE integration_id = $1 AND site = ANY($2)`,
		`DELETE FROM search_daily WHERE integration_id = $1 AND site = ANY($2)`,
		`DELETE FROM search_syncs WHERE integration_id = $1 AND site = ANY($2)`,
		`DELETE FROM url_inspections WHERE integration_id = $1 AND site = ANY($2)`,
		`DELETE FROM search_sitemaps WHERE integration_id = $1 AND site = ANY($2)`,
		`DELETE FROM page_conversions WHERE integration_id = $1 AND site = ANY($2)`,
		`DELETE FROM page_conversion_syncs WHERE integration_id = $1 AND site = ANY($2)`,
	} {
		if _, err := s.Pool.Exec(ctx, q, integrationID, off); err != nil {
			return res, err
		}
	}
	for _, p := range plan {
		if !p.Sync {
			continue
		}
		err := s.syncSite(ctx, o, reader, p, &res)
		if sc, ok := reader.(connectors.SearchConsoleReader); ok && err == nil {
			_, err = s.syncSearch(ctx, o, sc, p, &res)
		}
		if pc, ok := reader.(connectors.PageConversionReader); ok && err == nil {
			_, err = s.syncConversions(ctx, o, pc, p, &res)
		}
		if err == nil {
			continue
		}
		if firstErr == nil {
			firstErr = err
		}
		if connectors.KindOf(err) == connectors.ErrAuth {
			_ = s.Store.Checked(ctx, s.Pool, ws, integrationID, nil, err)
			break
		}
		var db dbError
		if errors.As(err, &db) {
			return res, db.error
		}
	}
	if br, ok := reader.(connectors.BotLogReader); ok && firstErr == nil {
		if err := s.syncBots(ctx, o, br, &res); err != nil {
			var db dbError
			if errors.As(err, &db) {
				return res, db.error
			}
		}
	}
	return res, firstErr
}

// dbError is Outflare's own database failing, which stops the run.
type dbError struct{ error }

// syncSite stores one site's numbers.
func (s *Syncer) syncSite(ctx context.Context, o *connectors.Opened, reader connectors.AnalyticsReader, p SitePlan, res *Result) error {
	ws, integrationID := o.WorkspaceID, o.ID
	site, host := p.ID, p.Host
	calls := res.Calls
	var backfilled bool
	err := s.Pool.QueryRow(ctx, `SELECT backfilled FROM analytics_syncs WHERE integration_id = $1 AND site = $2`, integrationID, site).Scan(&backfilled)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return dbError{err}
	}
	// Days stored before the site's domain was known, or by a run that
	// read the old one, follow its domain now, as a domain change does.
	if _, err := s.Pool.Exec(ctx, `UPDATE analytics_daily SET host = $3 WHERE integration_id = $1 AND site = $2 AND host <> $3`, integrationID, site, host); err != nil {
		return dbError{err}
	}
	today := s.now().UTC().Truncate(24 * time.Hour)
	yesterday := today.AddDate(0, 0, -1)
	trafficFrom, breakdownFrom := yesterday.AddDate(0, 0, -recentDays), yesterday.AddDate(0, 0, -(recentDays-1))
	if !backfilled {
		trafficFrom, breakdownFrom = yesterday.AddDate(0, 0, -(backfillDays-1)), yesterday.AddDate(0, 0, -(breakdownDays-1))
	}
	slow := false
	if ca, ok := reader.(connectors.AnalyticsCacher); ok && ca.CacheFor() >= 6*time.Hour {
		// Tools with a few calls a day (Clarity) only store totals.
		slow = true
	}
	stored := 0
	store := func(report string, day string, key string, m connectors.Metrics) error {
		_, err := s.Pool.Exec(ctx, `
			INSERT INTO analytics_daily (workspace_id, integration_id, connector, site, host, report, day, key,
				visitors, pageviews, sessions, bounce_rate, avg_duration, clicks, impressions, ctr, position, events, conversions, synced_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, now())
			ON CONFLICT (integration_id, site, report, day, key) DO UPDATE SET host = EXCLUDED.host,
				visitors = EXCLUDED.visitors, pageviews = EXCLUDED.pageviews, sessions = EXCLUDED.sessions,
				bounce_rate = EXCLUDED.bounce_rate, avg_duration = EXCLUDED.avg_duration, clicks = EXCLUDED.clicks,
				impressions = EXCLUDED.impressions, ctr = EXCLUDED.ctr, position = EXCLUDED.position,
				events = EXCLUDED.events, conversions = EXCLUDED.conversions, synced_at = now()`,
			ws, integrationID, o.Connector.Definition().ID, site, host, report, day, truncate(key, 500),
			m.Visitors, m.Pageviews, m.Sessions, m.BounceRate, m.AvgDuration, m.Clicks, m.Impressions, m.CTR, m.Position, m.Events, m.Conversions)
		if err != nil {
			return dbError{err}
		}
		res.Rows++
		stored++
		return nil
	}
	var firstErr error
	if connectors.Supports(reader, connectors.ReportTraffic) {
		q := connectors.AnalyticsQuery{Report: connectors.ReportTraffic, Site: site, From: day(trafficFrom), To: day(yesterday)}
		rep, err := s.report(ctx, o, reader, q, res)
		if err != nil {
			firstErr = err
		} else {
			for _, p := range rep.Series {
				if err := store(connectors.ReportTraffic, p.Date, "", p.Metrics); err != nil {
					return err
				}
			}
			if len(rep.Series) == 0 && rep.From == rep.To {
				if err := store(connectors.ReportTraffic, rep.From, "", rep.Totals); err != nil {
					return err
				}
			}
		}
	}
	if !slow && firstErr == nil {
		for _, report := range []string{connectors.ReportPages, connectors.ReportSources, connectors.ReportSearchQueries, connectors.ReportEvents} {
			if !connectors.Supports(reader, report) {
				continue
			}
			for d := breakdownFrom; !d.After(yesterday); d = d.AddDate(0, 0, 1) {
				q := connectors.AnalyticsQuery{Report: report, Site: site, From: day(d), To: day(d), Limit: rowsPerDay}
				rep, err := s.report(ctx, o, reader, q, res)
				if err != nil {
					if firstErr == nil {
						firstErr = err
					}
					break
				}
				for _, row := range rep.Rows {
					if err := store(report, day(d), row.Key, row.Metrics); err != nil {
						return err
					}
				}
			}
		}
	}
	s.finish(ctx, ws, integrationID, site, host, p.Name, Result{Rows: stored, Calls: res.Calls - calls}, firstErr)
	return firstErr
}

func (s *Syncer) report(ctx context.Context, o *connectors.Opened, reader connectors.AnalyticsReader, q connectors.AnalyticsQuery, res *Result) (connectors.AnalyticsReport, error) {
	q, err := connectors.CheckAnalyticsQuery(q, s.now())
	if err != nil {
		return connectors.AnalyticsReport{}, err
	}
	res.Calls++
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	return reader.Report(cctx, o.Client, o.Creds, q)
}

func (s *Syncer) finish(ctx context.Context, ws, id, site, host, name string, res Result, runErr error) {
	var msg *string
	if runErr != nil {
		m := runErr.Error()
		msg = &m
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO analytics_syncs (integration_id, workspace_id, site, host, site_name, first_day, last_day, backfilled, last_run_at, last_ok_at, last_error, last_calls)
		SELECT $1, $2, $3, $4, $7, d.first, d.last, $5::text IS NULL AND $3 <> '', now(), CASE WHEN $5::text IS NULL THEN now() END, $5, $6
		FROM (SELECT min(day) AS first, max(day) AS last FROM analytics_daily WHERE integration_id = $1 AND site = $3) d
		ON CONFLICT (integration_id, site) DO UPDATE SET
			host = $4, site_name = $7,
			first_day = COALESCE(EXCLUDED.first_day, analytics_syncs.first_day),
			last_day = COALESCE(EXCLUDED.last_day, analytics_syncs.last_day),
			backfilled = analytics_syncs.backfilled OR EXCLUDED.backfilled,
			last_run_at = now(),
			last_ok_at = COALESCE(EXCLUDED.last_ok_at, analytics_syncs.last_ok_at),
			last_error = $5, last_calls = $6`,
		id, ws, site, host, msg, res.Calls, name)
	if err == nil && site != "" && runErr == nil {
		// A site read fine, so an earlier failure to list sites is over.
		_, err = s.Pool.Exec(ctx, `DELETE FROM analytics_syncs WHERE integration_id = $1 AND site = ''`, id)
	}
	if err != nil {
		s.Log.Warn("analytics: record sync", "integration", id, "err", err)
	}
}

func day(t time.Time) string { return t.Format(time.DateOnly) }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

var hostRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// Host turns a site address into the domain that lines it up across
// tools: "https://www.Example.com/" and "sc-domain:example.com" are both
// example.com. It returns "" when the text is not a domain.
func Host(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.TrimPrefix(s, "sc-domain:")
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil {
			s = u.Hostname()
		}
	}
	if i := strings.IndexAny(s, "/?#: "); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimPrefix(s, "www.")
	if !hostRe.MatchString(s) {
		return ""
	}
	return s
}

// HostOf finds the domain of a tool's site from its address, id or name.
func HostOf(site connectors.AnalyticsSite) string {
	for _, v := range []string{site.URL, site.ID, site.Name} {
		if h := Host(v); h != "" {
			return h
		}
	}
	return ""
}
