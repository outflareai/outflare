// Package seointel is the SEO module's search intelligence: what Search
// Console says about each page of a site, the keyword each page targets,
// pages that compete for the same query, internal links worth adding,
// where the site ranks, which pages bring conversions, and what got worse.
// It reads what the syncs stored (search_daily, url_inspections, crawls)
// and puts what needs doing on the workspace's fixes list.
package seointel

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Querier is what reads need: a pool or a transaction.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Source is the Search Console property an SEO site's search data comes
// from.
type Source struct {
	IntegrationID string `json:"integration_id"`
	Property      string `json:"property"`
	Host          string `json:"host"`
	// The first and last day stored, YYYY-MM-DD.
	OldestDay *string    `json:"oldest_day"`
	NewestDay *string    `json:"newest_day"`
	LastRunAt *time.Time `json:"last_run_at"`
	LastError *string    `json:"last_error"`
}

// Bare is a site host without its port and www.
func Bare(host string) string {
	h := strings.ToLower(host)
	if i := strings.IndexByte(h, ':'); i >= 0 {
		h = h[:i]
	}
	return strings.TrimPrefix(h, "www.")
}

// PageHosts are the hosts a site's pages may have in Search Console:
// the host and its www. twin.
func PageHosts(siteHost string) []string {
	b := Bare(siteHost)
	return []string{b, "www." + b}
}

// PageHostSQL is the SQL for a page URL's host, lowercase.
func PageHostSQL(col string) string {
	return `lower(substring(` + col + ` from '^[A-Za-z]+://([^/?#:]+)'))`
}

// SourceFor finds the property that covers a site: a domain property
// first, then the closest URL-prefix property, then the freshest. nil
// means no Search Console property covers it.
func SourceFor(ctx context.Context, q Querier, ws, siteHost string) (*Source, error) {
	bare := Bare(siteHost)
	var s Source
	err := q.QueryRow(ctx, `
		SELECT y.integration_id::text, y.site, y.host, y.oldest_day::text, y.newest_day::text, y.last_run_at, y.last_error
		FROM search_syncs y JOIN integrations i ON i.id = y.integration_id AND i.status = 'connected'
		WHERE y.workspace_id = $1 AND y.host <> '' AND ($2 = y.host OR $2 LIKE '%.' || y.host)
		ORDER BY (y.site LIKE 'sc-domain:%') DESC, length(y.host) DESC, y.newest_day DESC NULLS LAST, y.integration_id
		LIMIT 1`, ws, bare).Scan(&s.IntegrationID, &s.Property, &s.Host, &s.OldestDay, &s.NewestDay, &s.LastRunAt, &s.LastError)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}
