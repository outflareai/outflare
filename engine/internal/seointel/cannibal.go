package seointel

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/fixes"
)

// Cannibalisation: several pages of a site that Google shows for the same
// search, splitting its clicks. Over the 28 days to the newest day Search
// Console sent, a search shown at least minCannibalImpressions times
// competes when two or more pages each had at least a tenth of its
// impressions, at least 10 of them, at an average position of 30 or
// better. Searches naming the site's brand are left out: a brand's home
// page and its other pages showing for its name is how it should be.

const (
	minCannibalImpressions = 100
	minCannibalShare       = 0.1
	maxCannibalPosition    = 30
	// maxCannibalFixes is how many searches a site's fixes list holds, the
	// most shown first.
	maxCannibalFixes = 50
)

// CompetingPage is one page Google shows for a search.
type CompetingPage struct {
	URL         string  `json:"url"`
	Clicks      int     `json:"clicks"`
	Impressions int     `json:"impressions"`
	Position    float64 `json:"position"`
	// Share is the page's part of the search's impressions, 0 to 1.
	Share float64 `json:"share"`
	// Target is whether the search is the page's target keyword.
	Target bool `json:"target"`
}

// Competition is a search the site's pages compete for.
type Competition struct {
	Query       string          `json:"query"`
	Clicks      int             `json:"clicks"`
	Impressions int             `json:"impressions"`
	Pages       []CompetingPage `json:"pages"`
	// Keep is the page to keep for the search: the one that targets it,
	// otherwise the one with the most clicks.
	Keep string `json:"keep"`
}

// Cannibalised finds the searches a site's pages compete for, the most
// shown first.
func Cannibalised(ctx context.Context, q Querier, ws, siteID, siteHost, brand string, src *Source, limit int) ([]Competition, error) {
	out := []Competition{}
	if src == nil || src.NewestDay == nil {
		return out, nil
	}
	rows, err := q.Query(ctx, `
		WITH pq AS (
			SELECT d.query, d.page, sum(d.clicks)::int AS c, sum(d.impressions)::int AS i,
				COALESCE(sum(d.position * d.impressions) / NULLIF(sum(d.impressions), 0), 0) AS p
			FROM search_daily d
			WHERE d.workspace_id = $1 AND d.integration_id = $2 AND d.site = $3 AND `+PageHostSQL("d.page")+` = ANY($4)
			  AND d.day > $5::date - 28 AND d.day <= $5::date AND d.query <> ''
			GROUP BY d.query, d.page
		), q AS (
			SELECT query, sum(i)::int AS ti, sum(c)::int AS tc FROM pq GROUP BY query HAVING sum(i) >= $6
		), comp AS (
			SELECT pq.*, q.ti, q.tc, pq.i::float8 / q.ti AS share FROM pq JOIN q USING (query)
			WHERE pq.i >= 10 AND pq.i::float8 / q.ti >= $7 AND pq.p <= $8
		), multi AS (
			SELECT query FROM comp GROUP BY query HAVING count(*) >= 2
		)
		SELECT comp.query, comp.ti, comp.tc, comp.page, comp.c, comp.i, comp.p, comp.share,
			EXISTS (SELECT 1 FROM page_targets t WHERE t.workspace_id = $1 AND t.site_id = $9 AND t.url = comp.page AND t.keyword = comp.query AND t.status = 'set')
		FROM comp JOIN multi USING (query)
		ORDER BY comp.ti DESC, comp.query, comp.i DESC, comp.page`,
		ws, src.IntegrationID, src.Property, PageHosts(siteHost), *src.NewestDay, minCannibalImpressions, minCannibalShare, maxCannibalPosition, siteID)
	if err != nil {
		return nil, err
	}
	var query string
	var ti, tc int
	var p CompetingPage
	_, err = pgx.ForEachRow(rows, []any{&query, &ti, &tc, &p.URL, &p.Clicks, &p.Impressions, &p.Position, &p.Share, &p.Target}, func() error {
		if n := len(out); n == 0 || out[n-1].Query != query {
			out = append(out, Competition{Query: query, Clicks: tc, Impressions: ti})
		}
		c := &out[len(out)-1]
		c.Pages = append(c.Pages, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	list := out[:0]
	for _, c := range out {
		if brand != "" && Mentions(c.Query, brand) {
			continue
		}
		most := -1
		for _, pg := range c.Pages {
			if pg.Target {
				c.Keep = pg.URL
				break
			}
			if pg.Clicks > most {
				most, c.Keep = pg.Clicks, pg.URL
			}
		}
		list = append(list, c)
		if len(list) == limit {
			break
		}
	}
	return list, nil
}

// Shared is a keyword that more than one page targets.
type Shared struct {
	Keyword string   `json:"keyword"`
	URLs    []string `json:"urls"`
}

// SharedTargets are the keywords two or more of a site's pages target.
func SharedTargets(ctx context.Context, q Querier, ws, siteID string) ([]Shared, error) {
	rows, err := q.Query(ctx, `SELECT keyword, array_agg(url ORDER BY url) FROM page_targets
		WHERE workspace_id = $1 AND site_id = $2 AND status = 'set' AND keyword <> ''
		GROUP BY keyword HAVING count(*) > 1 ORDER BY keyword`, ws, siteID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Shared])
}

// FixPage is a page a fix is about, with a short fact for a prompt.
type FixPage struct {
	URL    string `json:"url"`
	Detail string `json:"detail"`
}

// FixContext says where a search intelligence fix is, for its prompt:
// "the page ..., whose target keyword is ..." and the pages it covers.
func FixContext(ctx context.Context, q Querier, ws, siteID, rule, group string) (string, []FixPage, error) {
	pages := []FixPage{}
	switch rule {
	case "keyword_cannibalised":
		var host, brand string
		if err := q.QueryRow(ctx, `SELECT s.host, COALESCE(b.name, '') FROM seo_sites s LEFT JOIN brands b ON b.id = s.brand_id
			WHERE s.id = $1 AND s.workspace_id = $2`, siteID, ws).Scan(&host, &brand); err != nil {
			return "", pages, err
		}
		src, err := SourceFor(ctx, q, ws, host)
		if err != nil {
			return "", pages, err
		}
		comp, err := Cannibalised(ctx, q, ws, siteID, host, brand, src, 1000)
		if err != nil {
			return "", pages, err
		}
		for _, c := range comp {
			if c.Query != group {
				continue
			}
			for _, p := range c.Pages {
				d := fmt.Sprintf("%d%% of impressions, %d clicks, position %.1f", int(p.Share*100+0.5), p.Clicks, p.Position)
				if p.URL == c.Keep {
					d += ", the page to keep"
				}
				pages = append(pages, FixPage{URL: p.URL, Detail: d})
			}
		}
		return fmt.Sprintf("the pages Google shows for the search [%s] in the last 28 days of Search Console", group), pages, nil
	case "target_shared":
		var urls []string
		if err := q.QueryRow(ctx, `SELECT COALESCE(array_agg(url ORDER BY url), '{}') FROM page_targets
			WHERE workspace_id = $1 AND site_id = $2 AND keyword = $3 AND status = 'set'`, ws, siteID, group).Scan(&urls); err != nil {
			return "", pages, err
		}
		for _, u := range urls {
			pages = append(pages, FixPage{URL: u, Detail: "target keyword: " + group})
		}
		return fmt.Sprintf("the pages that all target the keyword [%s]", group), pages, nil
	case "internal_link_opportunity":
		var host string
		if err := q.QueryRow(ctx, `SELECT host FROM seo_sites WHERE id = $1 AND workspace_id = $2`, siteID, ws).Scan(&host); err != nil {
			return "", pages, err
		}
		src, err := SourceFor(ctx, q, ws, host)
		if err != nil {
			return "", pages, err
		}
		sug, _, err := LinkSuggestions(ctx, q, ws, siteID, host, "", src)
		if err != nil {
			return "", pages, err
		}
		keyword := ""
		for _, l := range sug {
			if l.URL != group {
				continue
			}
			keyword = l.Keyword
			for _, f := range l.From {
				d := "its title or H1 has the keyword"
				if f.Reason == "ranks" {
					d = fmt.Sprintf("Google shows it for the keyword (%d impressions in 28 days)", f.Impressions)
				}
				pages = append(pages, FixPage{URL: f.URL, Detail: d + "; add a link to " + group})
			}
		}
		return fmt.Sprintf("the page %s, whose target keyword is [%s]; the pages below are about the same keyword and don't link to it", group, keyword), pages, nil
	case RegressionClicks, RegressionRank, RegressionDeindexed, RegressionConversions, RegressionHealth:
		var subject, detail, kind string
		err := q.QueryRow(ctx, `SELECT r.subject, r.detail, r.kind FROM seo_regressions r JOIN seo_sites s ON s.id = r.site_id
			WHERE r.workspace_id = $1 AND r.site_id = $2 AND r.kind = $3 AND r.status = 'open' AND (r.subject = $4 OR (r.subject = '' AND s.host = $4))
			ORDER BY r.detected_at DESC LIMIT 1`, ws, siteID, rule, group).Scan(&subject, &detail, &kind)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", pages, err
		}
		switch {
		case kind == RegressionRank:
			return fmt.Sprintf("the tracked keyword [%s]: %s", group, detail), pages, nil
		case kind == RegressionHealth || subject == "":
			return fmt.Sprintf("the site %s: %s", group, detail), pages, nil
		}
		pages = append(pages, FixPage{URL: subject, Detail: detail})
		return "the page " + subject + ": " + detail, pages, nil
	}
	var keyword string
	err := q.QueryRow(ctx, `SELECT keyword FROM page_targets WHERE workspace_id = $1 AND site_id = $2 AND url = $3 AND status = 'set'`,
		ws, siteID, group).Scan(&keyword)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", pages, err
	}
	where := "the page " + group
	if keyword != "" {
		where += ", whose target keyword is [" + keyword + "]"
		pages = append(pages, FixPage{URL: group, Detail: "target keyword: " + keyword})
	}
	return where, pages, nil
}

// RefreshSearch checks again the SEO sites a Search Console property
// covers, after its sync stored new days: a domain property's host and
// its subdomains, a URL-prefix property's host and its www. twin.
func RefreshSearch(ctx context.Context, pool *pgxpool.Pool, ws, propertyHost string) (fixes.Changes, error) {
	var total fixes.Changes
	h := Bare(propertyHost)
	if h == "" {
		return total, nil
	}
	rows, err := pool.Query(ctx, `SELECT id::text FROM seo_sites WHERE workspace_id = $1
		AND (regexp_replace(lower(host), '^www\.', '') = $2 OR lower(host) LIKE '%.' || $2)`, ws, h)
	if err != nil {
		return total, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return total, err
	}
	for _, id := range ids {
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			c, err := Refresh(ctx, tx, ws, id, "", TriggerSearch)
			total.New, total.Reopened, total.Confirmed = total.New+c.New, total.Reopened+c.Reopened, total.Confirmed+c.Confirmed
			if err != nil {
				return err
			}
			// New days can show a drop in clicks or a page out of the index.
			r, err := RefreshRegressions(ctx, tx, ws, id, "")
			total.New, total.Reopened, total.Confirmed = total.New+r.New, total.Reopened+r.Reopened, total.Confirmed+r.Confirmed
			return err
		})
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
