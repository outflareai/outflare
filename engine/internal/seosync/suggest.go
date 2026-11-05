package seosync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The daily look for new competitors. Each day every SEO connection is
// asked which domains share the most keywords with each SEO site (one read
// a site, on the workspace's own account and within its budget), and the
// domains that keep ranking beside the site in its tracked keywords' top
// results are suggested too, at no cost. New ones arrive as suggestions to
// say yes or no to; tracked and dismissed ones keep their status.

// rankingSuggestions is how many domains from a site's rankings are
// suggested a day, and rankingMinKeywords how many of its tracked keywords
// a domain must rank for in the last week to count.
const (
	rankingSuggestions = 10
	rankingMinKeywords = 2
)

// platforms rank for nearly everything without competing with anyone, so
// they are never suggested from rankings.
var platforms = map[string]bool{
	"wikipedia.org": true, "youtube.com": true, "reddit.com": true, "quora.com": true, "medium.com": true,
	"linkedin.com": true, "facebook.com": true, "instagram.com": true, "x.com": true, "twitter.com": true,
	"tiktok.com": true, "pinterest.com": true, "github.com": true, "amazon.com": true, "google.com": true,
	"apple.com": true, "microsoft.com": true, "stackoverflow.com": true, "g2.com": true, "capterra.com": true,
	"trustpilot.com": true,
}

func platform(host string) bool {
	for h := host; h != ""; {
		if platforms[h] {
			return true
		}
		_, rest, ok := strings.Cut(h, ".")
		if !ok || !strings.Contains(rest, ".") {
			return false
		}
		h = rest
	}
	return false
}

// queueSuggest queues today's competitor suggestions for each connected
// SEO connection whose snapshots are on.
func (s *Syncer) queueSuggest(ctx context.Context, ids []string) error {
	rows, err := s.Pool.Query(ctx, `SELECT i.id::text, i.workspace_id::text FROM integrations i
		WHERE i.connector = ANY($1) AND i.status = 'connected' AND COALESCE(i.config->>'sync', 'weekly') <> 'off'
		  AND EXISTS (SELECT 1 FROM seo_sites t WHERE t.workspace_id = i.workspace_id AND t.status = 'active')`, ids)
	if err != nil {
		return err
	}
	type conn struct{ id, ws string }
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (conn, error) {
		var c conn
		return c, row.Scan(&c.id, &c.ws)
	})
	if err != nil {
		return err
	}
	day := time.Now().UTC().Format(time.DateOnly)
	for _, c := range list {
		if _, err := enqueue(ctx, s.Pool, c.ws, payload{Integration: c.id, Suggest: true}, fmt.Sprintf("%s:suggest:%s", c.id, day)); err != nil {
			return err
		}
	}
	return nil
}

// suggestDaily asks one connection for each SEO site's competitors.
func (s *Syncer) suggestDaily(ctx context.Context, ws, integrationID string) error {
	o, err := s.Store.Open(ctx, s.Pool, ws, integrationID)
	if err != nil {
		return nil
	}
	r, ok := o.Connector.(connectors.SEOReader)
	if !ok || !connectors.SupportsSEO(r, connectors.SEOCompetitors) {
		return nil
	}
	sites, err := LinkSites(ctx, s.Pool, ws)
	if err != nil {
		return err
	}
	country := SettingsOf(o.Config).Country
	conn := o.Connector.Definition().ID
	for _, site := range sites {
		n, _, err := s.SuggestSiteCompetitors(ctx, o, r, site, country)
		if err != nil {
			var be *BudgetError
			code := "provider_error"
			if errors.As(err, &be) {
				code = "budget_reached"
			}
			metrics.CompetitorSuggest(conn, "error", 0)
			track.Fail(ctx, s.Log, track.Event{Name: "seo_competitors.suggest_failed", WorkspaceID: ws, Surface: "job",
				Props: map[string]any{"connector": conn, "site_id": site.ID, "kind": connectors.KindOf(err)}}, code, err)
			if errors.As(err, &be) || connectors.KindOf(err) == connectors.ErrAuth {
				return nil
			}
			continue
		}
		metrics.CompetitorSuggest(conn, "ok", n)
		track.Emit(ctx, track.Event{Name: "seo_competitors.suggested", WorkspaceID: ws, ActorType: "system", Surface: "job", Outcome: track.Success,
			Props: map[string]any{"source": conn, "site_id": site.ID, "suggested": n}})
	}
	return nil
}

// SuggestFromRankings suggests, for each active SEO site with rankings
// checked in the last day, the domains that ranked in the top results for
// at least two of its tracked keywords in the last week. It reads only
// what rank tracking already stored, so it costs nothing.
func SuggestFromRankings(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	rows, err := pool.Query(ctx, `SELECT t.workspace_id::text, t.id::text, t.host, t.brand_id::text FROM seo_sites t
		WHERE t.status = 'active' AND EXISTS (SELECT 1 FROM tracked_keywords k JOIN rank_observations o ON o.tracked_id = k.id
			WHERE k.site_id = t.id AND o.workspace_id = t.workspace_id AND o.checked_at > now() - interval '1 hour')`)
	if err != nil {
		return err
	}
	type site struct {
		ws string
		LinkSite
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (site, error) {
		var v site
		return v, row.Scan(&v.ws, &v.ID, &v.Host, &v.BrandID)
	})
	if err != nil {
		return err
	}
	for _, v := range list {
		n, err := suggestSiteFromRankings(ctx, pool, v.ws, v.LinkSite)
		if err != nil {
			metrics.CompetitorSuggest("rankings", "error", 0)
			track.Fail(ctx, log, track.Event{Name: "seo_competitors.suggest_failed", WorkspaceID: v.ws, Surface: "job",
				Props: map[string]any{"connector": "rankings", "site_id": v.ID}}, "internal", err)
			continue
		}
		metrics.CompetitorSuggest("rankings", "ok", n)
		if n > 0 {
			track.Emit(ctx, track.Event{Name: "seo_competitors.suggested", WorkspaceID: v.ws, ActorType: "system", Surface: "job", Outcome: track.Success,
				Props: map[string]any{"source": "rankings", "site_id": v.ID, "suggested": n}})
		}
	}
	return nil
}

func suggestSiteFromRankings(ctx context.Context, pool *pgxpool.Pool, ws string, site LinkSite) (int, error) {
	rows, err := pool.Query(ctx, `SELECT r->>'domain', count(DISTINCT k.id)::int
		FROM tracked_keywords k JOIN rank_observations o ON o.tracked_id = k.id AND o.workspace_id = k.workspace_id
		CROSS JOIN LATERAL jsonb_array_elements(o.top) r
		WHERE k.workspace_id = $1 AND k.site_id = $2 AND o.day > current_date - 7 AND coalesce(r->>'domain', '') <> ''
		GROUP BY 1 HAVING count(DISTINCT k.id) >= $3 ORDER BY 2 DESC, 1 LIMIT 100`, ws, site.ID, rankingMinKeywords)
	if err != nil {
		return 0, err
	}
	type hit struct {
		host string
		n    int
	}
	hits, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (hit, error) {
		var h hit
		return h, row.Scan(&h.host, &h.n)
	})
	if err != nil {
		return 0, err
	}
	target := LinkTarget(site.Host)
	added, seen := 0, map[string]bool{}
	for _, h := range hits {
		host := CompetitorHost(h.host)
		if host == "" || seen[host] || host == target || strings.HasSuffix(host, "."+target) || platform(host) {
			continue
		}
		seen[host] = true
		tag, err := pool.Exec(ctx, `INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, status, source, common_keywords)
			VALUES ($1, $2, $3, $4, 'suggested', 'rankings', $5) ON CONFLICT (site_id, host) DO NOTHING`, ws, site.ID, site.BrandID, host, float64(h.n))
		if err != nil {
			return added, err
		}
		added += int(tag.RowsAffected())
		if added >= rankingSuggestions {
			break
		}
	}
	if added > 0 {
		return added, LinkCompetitorRecords(ctx, pool, ws, site.ID)
	}
	return 0, nil
}
