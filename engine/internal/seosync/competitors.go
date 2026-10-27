package seosync

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Competitors: each SEO snapshot also asks the connection which domains
// compete with every SEO site in search, and keeps them as suggestions
// (people track or dismiss them, or add their own). Each tracked
// competitor's overview is snapshotted too, and a move in its domain
// rating, traffic, keywords or referring domains is a competitor event.
// Their site itself is only ever glanced at by OutflareBot (crawl.Glance).

const (
	// competitorRows is how many competitors a site's read suggests.
	competitorRows = 10
	// MaxTrackedNumbers is how many tracked competitors per site get an
	// overview each week, oldest first, so one site can't spend the budget.
	MaxTrackedNumbers = 10
	// MaxGapCompetitors is how many of them also get their keywords and
	// linking domains read for the gaps.
	MaxGapCompetitors = 5
	// DefaultGapRows is how many keywords and linking domains that read
	// takes of each.
	DefaultGapRows = 50
)

// GapRowsOrDefault is how many rows a gap read takes; 0 is off.
func (s Settings) GapRowsOrDefault() int {
	if s.GapRows != nil {
		return max(0, *s.GapRows)
	}
	return DefaultGapRows
}

// Moves that make an event: a domain rating point, a tenth of traffic or
// keywords, a twentieth of referring domains.
var moves = []struct {
	kind   string
	metric func(connectors.SEOMetrics) *float64
	share  float64
	points float64
}{
	{"rating_changed", func(m connectors.SEOMetrics) *float64 { return m.DomainRating }, 0, 1},
	{"traffic_changed", func(m connectors.SEOMetrics) *float64 { return m.OrganicTraffic }, 0.10, 0},
	{"keywords_changed", func(m connectors.SEOMetrics) *float64 { return m.OrganicKeywords }, 0.10, 0},
	{"links_changed", func(m connectors.SEOMetrics) *float64 { return m.ReferringDomains }, 0.05, 0},
}

// CompetitorRun is what a connection's read of one site's competitors did.
type CompetitorRun struct {
	SiteID    string  `json:"site_id"`
	Host      string  `json:"host"`
	Suggested int     `json:"suggested"`
	Checked   int     `json:"checked"`
	Gaps      int     `json:"gaps"`
	Events    int     `json:"events"`
	Cost      float64 `json:"cost"`
	Error     *string `json:"error"`
}

// SyncSiteCompetitors suggests a site's competitors and snapshots the
// tracked ones' numbers.
func (s *Syncer) SyncSiteCompetitors(ctx context.Context, o *connectors.Opened, r connectors.SEOReader, site LinkSite, country string, now time.Time) (CompetitorRun, error) {
	ws := o.WorkspaceID
	run := CompetitorRun{SiteID: site.ID, Host: site.Host}
	unit := Unit(r)
	target := LinkTarget(site.Host)
	conn := o.Connector.Definition().ID
	fail := func(err error) (CompetitorRun, error) {
		msg := err.Error()
		run.Error = &msg
		return run, err
	}
	if connectors.SupportsSEO(r, connectors.SEOCompetitors) {
		q, err := connectors.CheckSEOQuery(connectors.SEOQuery{Report: connectors.SEOCompetitors, Target: target, Country: country, Limit: competitorRows})
		if err != nil {
			return fail(err)
		}
		a, err := Fetch(ctx, s.Pool, o, r, q, Options{Fresh: true})
		run.Cost += amountOf(unit, a.Cost)
		if err != nil {
			return fail(err)
		}
		for _, row := range a.Rows {
			host := CompetitorHost(row.Key)
			if host == "" || host == target || strings.HasSuffix(host, "."+target) {
				continue
			}
			tag, err := s.Pool.Exec(ctx, `
				INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, status, source, common_keywords)
				VALUES ($1, $2, $3, $4, 'suggested', $5, $6)
				ON CONFLICT (site_id, host) DO UPDATE SET common_keywords = EXCLUDED.common_keywords, updated_at = now()`,
				ws, site.ID, site.BrandID, host, conn, row.CommonKeywords)
			if err != nil {
				return fail(err)
			}
			run.Suggested += int(tag.RowsAffected())
		}
		if err := LinkCompetitorRecords(ctx, s.Pool, ws, site.ID); err != nil {
			return fail(err)
		}
	}
	rows, err := s.Pool.Query(ctx, `SELECT id::text, host FROM seo_competitors
		WHERE workspace_id = $1 AND site_id = $2 AND status = 'tracked' ORDER BY created_at, host LIMIT $3`, ws, site.ID, MaxTrackedNumbers)
	if err != nil {
		return fail(err)
	}
	type tracked struct{ id, host string }
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (tracked, error) {
		var t tracked
		return t, row.Scan(&t.id, &t.host)
	})
	if err != nil {
		return fail(err)
	}
	day := now.UTC().Format(time.DateOnly)
	gapRows := SettingsOf(o.Config).GapRowsOrDefault()
	for i, c := range list {
		q, err := connectors.CheckSEOQuery(connectors.SEOQuery{Report: connectors.SEOOverview, Target: c.host, Country: country})
		if err != nil {
			continue
		}
		var before *connectors.SEOMetrics
		var raw []byte
		err = s.Pool.QueryRow(ctx, `SELECT metrics FROM seo_snapshots
			WHERE integration_id = $1 AND host = $2 AND report = 'overview' AND key = '' AND day < $3
			ORDER BY day DESC LIMIT 1`, o.ID, c.host, day).Scan(&raw)
		if err == nil {
			var m connectors.SEOMetrics
			if json.Unmarshal(raw, &m) == nil {
				before = &m
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return fail(err)
		}
		a, err := Fetch(ctx, s.Pool, o, r, q, Options{Fresh: true})
		run.Cost += amountOf(unit, a.Cost)
		if err != nil {
			return fail(err)
		}
		if _, err := s.store(ctx, ws, o, c.host, day, a.SEOReport); err != nil {
			return fail(err)
		}
		run.Checked++
		// The first few also get their top keywords and linking domains,
		// for the keyword and link gaps.
		if i < MaxGapCompetitors && gapRows > 0 {
			for _, report := range []string{connectors.SEOKeywords, connectors.SEOBacklinks} {
				if !connectors.SupportsSEO(r, report) {
					continue
				}
				q, err := connectors.CheckSEOQuery(connectors.SEOQuery{Report: report, Target: c.host, Country: country, Limit: gapRows})
				if err != nil {
					continue
				}
				a, err := Fetch(ctx, s.Pool, o, r, q, Options{Fresh: true})
				run.Cost += amountOf(unit, a.Cost)
				if err != nil {
					return fail(err)
				}
				if _, err := s.store(ctx, ws, o, c.host, day, a.SEOReport); err != nil {
					return fail(err)
				}
			}
			run.Gaps++
		}
		if before == nil {
			continue // the first snapshot is a baseline
		}
		for _, m := range moves {
			was, now := m.metric(*before), m.metric(a.Totals)
			if was == nil || now == nil || !moved(*was, *now, m.share, m.points) {
				continue
			}
			if _, err := s.Pool.Exec(ctx, `INSERT INTO competitor_events (workspace_id, site_id, competitor_id, kind, connector, data)
				VALUES ($1, $2, $3, $4, $5, $6)`, ws, site.ID, c.id, m.kind, conn, map[string]any{"before": *was, "after": *now}); err != nil {
				return fail(err)
			}
			run.Events++
		}
	}
	return run, nil
}

// moved reports whether a number moved enough to tell anyone: by points,
// or by a share of what it was.
func moved(was, now, share, points float64) bool {
	d := math.Abs(now - was)
	if points > 0 {
		return d >= points
	}
	if was == 0 {
		return now >= 10
	}
	return d/was >= share
}

// CompetitorHost normalises a competitor's domain: lowercase, without a
// scheme, path, port or www. It is "" when it is not a domain.
func CompetitorHost(raw string) string {
	h := strings.ToLower(strings.TrimSpace(raw))
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	h, _, _ = strings.Cut(h, "/")
	h, _, _ = strings.Cut(h, ":")
	h = strings.TrimSuffix(strings.TrimPrefix(h, "www."), ".")
	if len(h) < 3 || len(h) > 253 || !strings.Contains(h, ".") {
		return ""
	}
	for _, c := range h {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.') {
			return ""
		}
	}
	return h
}

// LinkCompetitorRecords points a site's competitors at the company
// record with their domain, when the workspace has one.
func LinkCompetitorRecords(ctx context.Context, pool *pgxpool.Pool, ws, siteID string) error {
	_, err := pool.Exec(ctx, `
		UPDATE seo_competitors c SET record_id = (
			SELECT ri.record_id FROM record_identities ri JOIN records r ON r.id = ri.record_id
			WHERE ri.workspace_id = c.workspace_id AND ri.namespace = 'domain' AND ri.value = c.host
			  AND ri.valid_to IS NULL AND r.merged_into IS NULL
			ORDER BY ri.verified DESC, ri.valid_from LIMIT 1)
		WHERE c.workspace_id = $1 AND c.site_id = $2`, ws, siteID)
	return err
}
