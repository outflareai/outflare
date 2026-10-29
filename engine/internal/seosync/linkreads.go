package seosync

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Link reads: who links to a page or domain that isn't the workspace's
// own, from its own SEO connection's links report. Each read is paid, within
// the connection's budget, and kept, so reports over reads (dead pages that
// still have links, the most linked pages) cost nothing. The weekly SEO
// snapshot also reads the pages that just went from tracked competitors'
// sitemaps, within a share of the budget the workspace sets.

// LinkSettings are a workspace's link building settings.
type LinkSettings struct {
	PitchCooldownDays   int       `json:"pitch_cooldown_days"`
	WeeklyReads         bool      `json:"weekly_reads"`
	WeeklyBudgetPercent int       `json:"weekly_budget_percent"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// LoadLinkSettings reads a workspace's settings, making the row with the
// defaults on first use.
func LoadLinkSettings(ctx context.Context, q jobs.Querier, ws string) (LinkSettings, error) {
	var v LinkSettings
	err := q.QueryRow(ctx, `
		WITH ins AS (INSERT INTO link_building_settings (workspace_id) VALUES ($1) ON CONFLICT DO NOTHING
			RETURNING pitch_cooldown_days, weekly_reads, weekly_budget_percent, updated_at)
		SELECT * FROM ins
		UNION ALL SELECT pitch_cooldown_days, weekly_reads, weekly_budget_percent, updated_at FROM link_building_settings WHERE workspace_id = $1
		LIMIT 1`, ws).Scan(&v.PitchCooldownDays, &v.WeeklyReads, &v.WeeklyBudgetPercent, &v.UpdatedAt)
	return v, err
}

// Link read limits: rows kept per read by default and at most, and pages
// the weekly read looks at per site.
const (
	LinkReadRows    = 100
	MaxLinkReadRows = 1000
	WeeklyReadPages = 20
)

// LinkReadRequest asks who links to Target: a page URL when Page, else a
// domain.
type LinkReadRequest struct {
	Target       string
	Page         bool
	Limit        int
	SiteID       *string
	BrandID      *string
	Reason       string // requested or dead_page
	CompetitorID *string
	// Fresh reads again even when a copy under a day old exists.
	Fresh   bool
	ActorID *string
	// Rec records link_read.done (SystemRecorder for jobs).
	Rec Recorder
}

// LinkRead is one stored read.
type LinkRead struct {
	ID               string
	Links            int
	ReferringDomains int
	TotalLinks       *float64
	Cost             float64
	Unit             string
	Cached           bool
}

// LinkQuery is the provider query for a read.
func LinkQuery(req LinkReadRequest) (connectors.SEOQuery, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = LinkReadRows
	}
	return connectors.CheckSEOQuery(connectors.SEOQuery{Report: connectors.SEOLinks, Target: req.Target, Page: req.Page, Limit: min(limit, MaxLinkReadRows)})
}

// EstimateLinkRead is about what a read would cost on the connection, in
// its budget's unit, and the budget.
func EstimateLinkRead(ctx context.Context, pool *pgxpool.Pool, o *connectors.Opened, r connectors.SEOReader, req LinkReadRequest) (float64, Budget, error) {
	q, err := LinkQuery(req)
	if err != nil {
		return 0, Budget{}, err
	}
	b, err := BudgetOf(ctx, pool, o, r)
	return amountOf(b.Unit, r.Estimate(q)), b, err
}

// ReadLinks reads who links to the target within the connection's budget
// and stores what it found. A failed read is stored too, with its error
// code, and counted in metrics; the caller records the failure event.
func ReadLinks(ctx context.Context, pool *pgxpool.Pool, o *connectors.Opened, r connectors.SEOReader, ws string, req LinkReadRequest) (LinkRead, error) {
	out := LinkRead{Unit: Unit(r)}
	q, err := LinkQuery(req)
	if err != nil {
		return out, err
	}
	scope := "domain"
	if req.Page {
		scope = "page"
	}
	connector := o.Connector.Definition().ID
	a, err := Fetch(ctx, pool, o, r, q, Options{Fresh: req.Fresh, ActorID: req.ActorID})
	out.Cost, out.Cached = amountOf(out.Unit, a.Cost), a.Cached
	if err != nil {
		code := LinkReadErrorCode(err)
		metrics.LinkRead(req.Reason, code)
		_, _ = pool.Exec(ctx, `INSERT INTO link_reads (workspace_id, site_id, brand_id, integration_id, connector, target, scope, reason, competitor_id,
				status, error_code, cost, unit, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'failed', $10, $11, $12, $13)`,
			ws, req.SiteID, req.BrandID, o.ID, connector, q.Target, scope, req.Reason, req.CompetitorID, code, out.Cost, out.Unit, req.ActorID)
		return out, err
	}
	out.TotalLinks = a.Totals.Backlinks
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO link_reads (workspace_id, site_id, brand_id, integration_id, connector, target, scope, reason,
				competitor_id, status, total_links, cost, unit, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'done', $10, $11, $12, $13) RETURNING id::text`,
			ws, req.SiteID, req.BrandID, o.ID, connector, q.Target, scope, req.Reason, req.CompetitorID, out.TotalLinks, out.Cost, out.Unit,
			req.ActorID).Scan(&out.ID); err != nil {
			return err
		}
		domains := map[string]bool{}
		for _, row := range a.Rows {
			l := row.Link
			if l == nil {
				continue
			}
			from, to := NormalLinkURL(l.From), NormalLinkURL(l.To)
			if from == "" || to == "" {
				continue
			}
			domain := strings.TrimPrefix(strings.ToLower(l.SourceDomain), "www.")
			if domain == "" {
				_, domain = ProspectPage(from)
			}
			var spam *int
			if l.Spam != nil {
				s := int(*l.Spam)
				spam = &s
			}
			tag, err := tx.Exec(ctx, `INSERT INTO link_read_rows (read_id, workspace_id, source_url, source_domain, target_url, anchor, nofollow, lost,
					source_rating, url_rating, domain_traffic, spam_score, title, first_seen)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) ON CONFLICT DO NOTHING`,
				out.ID, ws, from, trunc(domain, 253), to, trunc(l.Anchor, 500), l.Nofollow, l.Lost, l.SourceRating, l.URLRating, l.DomainTraffic,
				spam, trunc(l.Title, 300), l.FirstSeen)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 1 {
				out.Links++
				domains[domain] = true
			}
		}
		out.ReferringDomains = len(domains)
		if _, err := tx.Exec(ctx, `UPDATE link_reads SET links = $2, referring_domains = $3 WHERE id = $1`, out.ID, out.Links, out.ReferringDomains); err != nil {
			return err
		}
		data := map[string]any{"target": q.Target, "scope": scope, "reason": req.Reason, "site_id": req.SiteID, "brand_id": req.BrandID,
			"competitor_id": req.CompetitorID, "connector": connector, "links": out.Links, "referring_domains": out.ReferringDomains,
			"cost": out.Cost, "unit": out.Unit, "cached": out.Cached}
		props := map[string]any{"scope": scope, "reason": req.Reason, "connector": connector, "links": out.Links, "cached": out.Cached}
		return req.Rec(ctx, tx, "link_read.done", out.ID, data, props)
	})
	if err != nil {
		metrics.LinkRead(req.Reason, "internal")
		return out, err
	}
	metrics.LinkRead(req.Reason, "success")
	return out, nil
}

// LinkReadErrorCode is the error code for a failed read.
func LinkReadErrorCode(err error) string {
	var be *BudgetError
	switch {
	case errors.As(err, &be):
		return "budget_reached"
	case connectors.KindOf(err) == connectors.ErrAuth:
		return "connector_auth"
	case connectors.KindOf(err) == connectors.ErrInvalid:
		return "invalid_request"
	}
	return "provider_error"
}

// DeadPageRun is what the weekly read of competitors' dead pages did for
// one site.
type DeadPageRun struct {
	SiteID string  `json:"site_id"`
	Read   int     `json:"read"`
	Cost   float64 `json:"cost"`
	// Capped: pages were left for next week, because the share of the
	// budget for weekly reads was spent.
	Capped bool `json:"capped"`
}

// ReadDeadPages reads who links to the pages that went from a site's
// tracked competitors' sitemaps in the last 8 days and were never read, at
// most WeeklyReadPages a site, while the month's weekly reads stay within
// the workspace's share of the connection's budget. It does nothing when
// the workspace turned weekly reads off.
func ReadDeadPages(ctx context.Context, pool *pgxpool.Pool, o *connectors.Opened, r connectors.SEOReader, site LinkSite) (DeadPageRun, error) {
	run := DeadPageRun{SiteID: site.ID}
	set, err := LoadLinkSettings(ctx, pool, o.WorkspaceID)
	if err != nil || !set.WeeklyReads {
		return run, err
	}
	rows, err := pool.Query(ctx, `
		SELECT p.url, c.id::text FROM competitor_pages p JOIN seo_competitors c ON c.id = p.competitor_id
		WHERE c.workspace_id = $1 AND c.site_id = $2 AND c.status = 'tracked' AND p.gone_at > now() - interval '8 days'
		  AND NOT EXISTS (SELECT 1 FROM link_reads x WHERE x.workspace_id = $1 AND x.target = p.url AND x.scope = 'page' AND x.status = 'done')
		ORDER BY p.gone_at DESC, p.url LIMIT $3`, o.WorkspaceID, site.ID, WeeklyReadPages)
	if err != nil {
		return run, err
	}
	type page struct{ url, competitor string }
	pages, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (page, error) {
		var p page
		return p, row.Scan(&p.url, &p.competitor)
	})
	if err != nil || len(pages) == 0 {
		return run, err
	}
	b, err := BudgetOf(ctx, pool, o, r)
	if err != nil {
		return run, err
	}
	var spent float64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(sum(cost), 0) FROM link_reads
		WHERE integration_id = $1 AND reason = 'dead_page' AND created_at >= date_trunc('month', now())`, o.ID).Scan(&spent); err != nil {
		return run, err
	}
	share := b.Limit * float64(set.WeeklyBudgetPercent) / 100
	for _, p := range pages {
		req := LinkReadRequest{Target: p.url, Page: true, SiteID: &site.ID, BrandID: site.BrandID, Reason: "dead_page",
			CompetitorID: &p.competitor, Rec: SystemRecorder(o.WorkspaceID)}
		est, _, err := EstimateLinkRead(ctx, pool, o, r, req)
		if err != nil {
			return run, err
		}
		if !b.Unlimited && spent+est > share {
			run.Capped = true
			break
		}
		read, err := ReadLinks(ctx, pool, o, r, o.WorkspaceID, req)
		spent += read.Cost
		run.Cost += read.Cost
		if err != nil {
			return run, err
		}
		run.Read++
	}
	return run, nil
}

// readDeadPages runs ReadDeadPages for each site and records what it did.
func (s *Syncer) readDeadPages(ctx context.Context, o *connectors.Opened, r connectors.SEOReader, sites []LinkSite) []DeadPageRun {
	runs := []DeadPageRun{}
	connector := o.Connector.Definition().ID
	for _, site := range sites {
		run, err := ReadDeadPages(ctx, s.Pool, o, r, site)
		runs = append(runs, run)
		props := map[string]any{"connector": connector, "site_id": site.ID, "read": run.Read, "capped": run.Capped}
		if err != nil {
			track.Fail(ctx, s.Log, track.Event{Name: "link_reads.weekly_failed", WorkspaceID: o.WorkspaceID, Surface: "job", Props: props},
				LinkReadErrorCode(err), err)
			var be *BudgetError
			if errors.As(err, &be) || connectors.KindOf(err) == connectors.ErrAuth {
				break
			}
			continue
		}
		if run.Read > 0 || run.Capped {
			track.Emit(ctx, track.Event{Name: "link_reads.weekly_done", WorkspaceID: o.WorkspaceID, ActorType: "system", Surface: "job",
				Outcome: track.Success, Props: props})
		}
	}
	return runs
}

// retryDeadPages reads again who links to competitors' dead pages whose
// weekly read failed, for every SEO site, without a whole snapshot. Pages
// that read fine are left out by ReadDeadPages itself.
func (s *Syncer) retryDeadPages(ctx context.Context, ws, integrationID string) error {
	o, err := s.Store.Open(ctx, s.Pool, ws, integrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	r, ok := o.Connector.(connectors.SEOReader)
	if !ok || !connectors.SupportsSEO(r, connectors.SEOLinks) {
		return nil
	}
	sites, err := LinkSites(ctx, s.Pool, ws)
	if err != nil || len(sites) == 0 {
		return err
	}
	s.readDeadPages(ctx, o, r, sites)
	return nil
}
