package seointel

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Internal links worth adding: for each page with a target keyword, the
// site's other pages that are about the same thing but do not link to it.
// A page is about the keyword when Google shows it for that search (in the
// 28 days to the newest day Search Console sent) or its title or H1 has
// every word of it. Only pages the latest crawl read, that search engines
// may index, count, the most linked to first: a link from a page with
// links of its own passes on more.

// maxLinkSuggestions is how many pages a target gets suggested.
const maxLinkSuggestions = 5

// LinkFrom is a page that could link to a target.
type LinkFrom struct {
	URL string `json:"url"`
	// Why it fits: ranks (Google shows it for the keyword) or mentions
	// (its title or H1 has the keyword).
	Reason      string `json:"reason"`
	Impressions int    `json:"impressions"`
	// LinksIn is how many pages of the site link to it.
	LinksIn int `json:"links_in"`
}

// LinkTarget is a target page and the pages that could link to it.
type LinkTarget struct {
	URL     string     `json:"url"`
	Keyword string     `json:"keyword"`
	LinksIn int        `json:"links_in"`
	From    []LinkFrom `json:"from"`
}

// slashTwin is a URL with or without its trailing slash.
func slashTwin(u string) string {
	if strings.HasSuffix(u, "/") {
		return strings.TrimSuffix(u, "/")
	}
	return u + "/"
}

// LinkSuggestions finds internal links worth adding to a site's target
// pages from a crawl (the latest finished one when crawlID is ""). Targets
// the crawl did not read are left out; skipped lists them.
func LinkSuggestions(ctx context.Context, q Querier, ws, siteID, siteHost, crawlID string, src *Source) (list []LinkTarget, skipped []string, err error) {
	list = []LinkTarget{}
	if crawlID == "" {
		if crawlID, err = LatestCrawl(ctx, q, ws, siteID); err != nil {
			return nil, nil, err
		}
	}
	rows, err := q.Query(ctx, `SELECT url, keyword FROM page_targets WHERE workspace_id = $1 AND site_id = $2 AND status = 'set' AND keyword <> '' ORDER BY url`, ws, siteID)
	if err != nil {
		return nil, nil, err
	}
	type target struct{ url, keyword string }
	targets, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (target, error) {
		var t target
		return t, r.Scan(&t.url, &t.keyword)
	})
	if err != nil || len(targets) == 0 {
		return list, nil, err
	}
	if crawlID == "" {
		for _, t := range targets {
			skipped = append(skipped, t.url)
		}
		return list, skipped, nil
	}

	// The pages the crawl read that may be indexed, with what they say.
	type page struct {
		id      int64
		title   string
		h1      []string
		linksIn int
	}
	rows, err = q.Query(ctx, `SELECT id, url, title, h1, links_in FROM crawl_pages
		WHERE workspace_id = $1 AND run_id = $2 AND status = 'fetched' AND NOT noindex AND http_status = 200
		  AND content_type LIKE 'text/html%'`, ws, crawlID)
	if err != nil {
		return nil, nil, err
	}
	pages := map[string]page{}
	var u string
	var p page
	if _, err := pgx.ForEachRow(rows, []any{&p.id, &u, &p.title, &p.h1, &p.linksIn}, func() error {
		pages[u] = p
		return nil
	}); err != nil {
		return nil, nil, err
	}

	// Which pages already link where.
	rows, err = q.Query(ctx, `SELECT p.url, l.to_url FROM crawl_links l JOIN crawl_pages p ON p.id = l.from_page
		WHERE l.workspace_id = $1 AND l.run_id = $2 AND l.internal`, ws, crawlID)
	if err != nil {
		return nil, nil, err
	}
	linked := map[[2]string]bool{}
	var from, to string
	if _, err := pgx.ForEachRow(rows, []any{&from, &to}, func() error {
		linked[[2]string{from, to}] = true
		return nil
	}); err != nil {
		return nil, nil, err
	}
	links := func(from, to string) bool {
		return linked[[2]string{from, to}] || linked[[2]string{from, slashTwin(to)}]
	}

	// Which pages Google shows for each target's keyword.
	ranks := map[string]map[string]int{}
	if src != nil && src.NewestDay != nil {
		kws := make([]string, len(targets))
		for i, t := range targets {
			kws[i] = t.keyword
		}
		rows, err := q.Query(ctx, `SELECT d.query, d.page, sum(d.impressions)::int FROM search_daily d
			WHERE d.workspace_id = $1 AND d.integration_id = $2 AND d.site = $3 AND `+PageHostSQL("d.page")+` = ANY($4)
			  AND d.day > $5::date - 28 AND d.day <= $5::date AND d.query = ANY($6)
			GROUP BY d.query, d.page`, ws, src.IntegrationID, src.Property, PageHosts(siteHost), *src.NewestDay, kws)
		if err != nil {
			return nil, nil, err
		}
		var kw string
		var n int
		if _, err := pgx.ForEachRow(rows, []any{&kw, &u, &n}, func() error {
			if ranks[kw] == nil {
				ranks[kw] = map[string]int{}
			}
			ranks[kw][u] = n
			return nil
		}); err != nil {
			return nil, nil, err
		}
	}

	for _, t := range targets {
		tp, ok := pages[t.url]
		if !ok {
			skipped = append(skipped, t.url)
			continue
		}
		lt := LinkTarget{URL: t.url, Keyword: t.keyword, LinksIn: tp.linksIn, From: []LinkFrom{}}
		for u, p := range pages {
			if u == t.url || u == slashTwin(t.url) || links(u, t.url) {
				continue
			}
			f := LinkFrom{URL: u, Impressions: ranks[t.keyword][u], LinksIn: p.linksIn}
			mentions := Mentions(p.title, t.keyword)
			for _, h := range p.h1 {
				mentions = mentions || Mentions(h, t.keyword)
			}
			switch {
			case f.Impressions > 0:
				f.Reason = "ranks"
			case mentions:
				f.Reason = "mentions"
			default:
				continue
			}
			lt.From = append(lt.From, f)
		}
		sortFrom(lt.From)
		if len(lt.From) > maxLinkSuggestions {
			lt.From = lt.From[:maxLinkSuggestions]
		}
		if len(lt.From) > 0 {
			list = append(list, lt)
		}
	}
	return list, skipped, nil
}

// sortFrom puts the pages that pass on the most first: linked to most,
// then shown most for the keyword, then by URL.
func sortFrom(f []LinkFrom) {
	slices.SortFunc(f, func(a, b LinkFrom) int {
		return cmp.Or(cmp.Compare(b.LinksIn, a.LinksIn), cmp.Compare(b.Impressions, a.Impressions), cmp.Compare(a.URL, b.URL))
	})
}
