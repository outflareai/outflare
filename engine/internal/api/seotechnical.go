package api

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/outflareai/outflare/engine/internal/crawl"
)

// Technical SEO at a glance for a brand: what its latest finished crawl
// of each site found, counted in unique pages (not occurrences) and in
// templates with the language part taken out (so one template error is
// one line, not one per language), set against the crawl before it (new
// and fixed pages), weighted by the Search Console clicks of the pages it
// touches, with robots.txt changes, sitemaps, indexing and Core Web Vitals
// from real visits. Reads only what is stored.

const (
	techRulesShown  = 200
	techTopPages    = 3
	vitalsMinVisits = 20
	vitalsPages     = 30
)

type techRule struct {
	Rule      string `json:"rule"`
	Title     string `json:"title"`
	Category  string `json:"category"`
	Severity  string `json:"severity"`
	Pages     int    `json:"pages"`
	Previous  int    `json:"previous_pages"`
	New       int    `json:"new_pages"`
	Fixed     int    `json:"fixed_pages"`
	Templates int    `json:"templates"`
	// Search Console clicks over the last 28 days of the pages it touches.
	Clicks   float64    `json:"clicks"`
	TopPages []techPage `json:"top_pages"`
}

type techPage struct {
	URL    string  `json:"url"`
	Clicks float64 `json:"clicks"`
}

type techSite struct {
	SiteID        string     `json:"site_id"`
	Host          string     `json:"host"`
	CrawlID       *string    `json:"crawl_id"`
	CrawledAt     *time.Time `json:"crawled_at"`
	Pages         int        `json:"pages"`
	PreviousID    *string    `json:"previous_crawl_id"`
	PreviousAt    *time.Time `json:"previous_crawled_at"`
	RobotsChanged []string   `json:"robots_changed"`
	Indexable     *int       `json:"indexable"`
	Indexed       *int       `json:"indexed"`
	NotIndexed    *int       `json:"not_indexed"`
	PrevIndexed   *int       `json:"previous_indexed"`
	IndexedAt     *string    `json:"indexed_day"`
}

type techSitemap struct {
	Host      string     `json:"host"`
	Path      string     `json:"path"`
	Submitted int        `json:"submitted"`
	Indexed   int        `json:"indexed"`
	Errors    int        `json:"errors"`
	Warnings  int        `json:"warnings"`
	LastRead  *time.Time `json:"last_read"`
}

type techVitals struct {
	Path   string   `json:"path"`
	Device string   `json:"device"`
	Visits int      `json:"visits"`
	LCP    *float64 `json:"lcp_ms"`
	INP    *float64 `json:"inp_ms"`
	CLS    *float64 `json:"cls"`
	// The metrics past Google's poor line: lcp (4 s), inp (500 ms), cls (0.25).
	Poor []string `json:"poor"`
}

func (s *Server) seoTechnical(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a := accessFrom(ctx)
	ws, brand := a.WorkspaceID, a.Brand
	sites, err := s.techSites(ctx, ws, brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var latest, previous []string
	var hosts []string
	for _, st := range sites {
		hosts = append(hosts, strings.TrimPrefix(strings.ToLower(st.Host), "www."))
		if st.CrawlID != nil {
			latest = append(latest, *st.CrawlID)
		}
		if st.PreviousID != nil {
			previous = append(previous, *st.PreviousID)
		}
	}
	rules, err := s.techRules(ctx, ws, brand, latest, previous)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	sitemaps, err := s.techSitemaps(ctx, ws, hosts)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	vitals, err := s.techVitals(ctx, ws, brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	sum := map[string]int{"errors": 0, "warnings": 0, "notices": 0, "new_pages": 0, "fixed_pages": 0, "robots_changed": 0, "poor_vitals": 0, "sitemap_errors": 0}
	for _, x := range rules {
		switch x.Severity {
		case "error":
			sum["errors"] += x.Pages
		case "warning":
			sum["warnings"] += x.Pages
		default:
			sum["notices"] += x.Pages
		}
		if x.Severity != "notice" {
			sum["new_pages"] += x.New
			sum["fixed_pages"] += x.Fixed
		}
	}
	for _, st := range sites {
		sum["robots_changed"] += len(st.RobotsChanged)
	}
	for _, v := range vitals {
		if len(v.Poor) > 0 {
			sum["poor_vitals"]++
		}
	}
	for _, m := range sitemaps {
		sum["sitemap_errors"] += m.Errors
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sites": sites, "rules": rules, "sitemaps": sitemaps, "vitals": vitals, "summary": sum,
		"notes": []string{
			"Issues are from each site's latest finished crawl against the one before, counted in unique pages. New pages have an issue the crawl before did not; fixed pages had it and no longer do. A crawl that stopped at its page limit sees only part of the site, so a page it did not reach is not counted as fixed or new.",
			"Templates count the page templates an issue is on, with the language part of the path taken out, so one template error across ten languages is one template.",
			"Clicks are the Search Console clicks of the pages an issue touches over the last 28 days stored: fix the issues with the most clicks first.",
			"Core Web Vitals come from real visits recorded by the Outflare tag over the last 28 days, the 75th percentile per page and device, for pages with at least 20 visits; poor means past Google's lines (LCP 4 s, INP 500 ms, CLS 0.25).",
		},
	})
}

// techSites reads each brand site's two latest finished crawls, its
// robots.txt changes between them and its indexing snapshots.
func (s *Server) techSites(ctx context.Context, ws, brand string) ([]techSite, error) {
	rows, err := s.pool.Query(ctx, `
		WITH r AS (
			SELECT c.id, c.site_id, c.finished_at, c.pages_fetched,
				row_number() OVER (PARTITION BY c.site_id ORDER BY c.finished_at DESC NULLS LAST, c.created_at DESC) AS n
			FROM crawl_runs c WHERE c.workspace_id = $1 AND c.status = 'done'
		)
		SELECT s.id::text, s.host, r1.id::text, r1.finished_at, COALESCE(r1.pages_fetched, 0), r2.id::text, r2.finished_at
		FROM seo_sites s
		LEFT JOIN r r1 ON r1.site_id = s.id AND r1.n = 1
		LEFT JOIN r r2 ON r2.site_id = s.id AND r2.n = 2
		WHERE s.workspace_id = $1 AND s.brand_id = $2
		ORDER BY s.host`, ws, brand)
	if err != nil {
		return nil, err
	}
	out := []techSite{}
	for rows.Next() {
		var t techSite
		if err := rows.Scan(&t.SiteID, &t.Host, &t.CrawlID, &t.CrawledAt, &t.Pages, &t.PreviousID, &t.PreviousAt); err != nil {
			rows.Close()
			return nil, err
		}
		t.RobotsChanged = []string{}
		out = append(out, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		t := &out[i]
		if t.CrawlID != nil && t.PreviousID != nil {
			rows, err := s.pool.Query(ctx, `
				SELECT a.host FROM crawl_robots a JOIN crawl_robots b ON b.host = a.host AND b.run_id = $2
				JOIN crawl_runs c ON c.id = a.run_id AND c.workspace_id = $3
				WHERE a.run_id = $1 AND (a.status IS DISTINCT FROM b.status OR a.body IS DISTINCT FROM b.body)
				ORDER BY a.host`, *t.CrawlID, *t.PreviousID, ws)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var h string
				if err := rows.Scan(&h); err != nil {
					rows.Close()
					return nil, err
				}
				t.RobotsChanged = append(t.RobotsChanged, h)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, err
			}
		}
		rows, err := s.pool.Query(ctx, `
			SELECT to_char(day, 'YYYY-MM-DD'), indexable, indexed, not_indexed FROM indexability_snapshots
			WHERE workspace_id = $1 AND site_id = $2 ORDER BY day DESC LIMIT 2`, ws, t.SiteID)
		if err != nil {
			return nil, err
		}
		n := 0
		for rows.Next() {
			var d string
			var able, idx, not int
			if err := rows.Scan(&d, &able, &idx, &not); err != nil {
				rows.Close()
				return nil, err
			}
			if n == 0 {
				t.IndexedAt, t.Indexable, t.Indexed, t.NotIndexed = &d, &able, &idx, &not
			} else {
				t.PrevIndexed = &idx
			}
			n++
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// techRules counts each audit rule's unique pages in the latest crawls
// and the crawls before, its templates and the clicks of its pages.
func (s *Server) techRules(ctx context.Context, ws, brand string, latest, previous []string) ([]techRule, error) {
	out := []techRule{}
	if len(latest) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		WITH iss AS (
			SELECT i.rule, i.severity, p.url, i.template, i.run_id = ANY($2::uuid[]) AS cur
			FROM crawl_issues i JOIN crawl_pages p ON p.id = i.page_id
			WHERE i.workspace_id = $1 AND (i.run_id = ANY($2::uuid[]) OR i.run_id = ANY($3::uuid[]))
		),
		seen AS (
			-- The pages each crawl reached, so a page the latest crawl did
			-- not reach is neither new nor fixed.
			SELECT p.url, bool_or(p.run_id = ANY($2::uuid[])) AS in_cur, bool_or(p.run_id = ANY($3::uuid[])) AS in_prev
			FROM crawl_pages p
			WHERE p.workspace_id = $1 AND (p.run_id = ANY($2::uuid[]) OR p.run_id = ANY($3::uuid[]))
			GROUP BY p.url
		),
		per AS (
			SELECT iss.rule, iss.url, max(iss.severity) AS severity, bool_or(iss.cur) AS has_now, bool_or(NOT iss.cur) AS had_before,
				max(regexp_replace(iss.template, '^/[a-z]{2}(-[a-z]{2})?(/|$)', '/')) FILTER (WHERE iss.cur) AS tpl
			FROM iss GROUP BY iss.rule, iss.url
		)
		SELECT per.rule, max(per.severity),
			count(*) FILTER (WHERE has_now), count(*) FILTER (WHERE had_before),
			count(*) FILTER (WHERE has_now AND NOT had_before AND COALESCE(seen.in_prev, false)),
			count(*) FILTER (WHERE had_before AND NOT has_now AND COALESCE(seen.in_cur, false)),
			count(DISTINCT CASE WHEN has_now THEN COALESCE(NULLIF(tpl, ''), per.url) END)
		FROM per LEFT JOIN seen USING (url)
		GROUP BY per.rule`, ws, latest, previous)
	if err != nil {
		return nil, err
	}
	by := map[string]*techRule{}
	for rows.Next() {
		var t techRule
		if err := rows.Scan(&t.Rule, &t.Severity, &t.Pages, &t.Previous, &t.New, &t.Fixed, &t.Templates); err != nil {
			rows.Close()
			return nil, err
		}
		if t.Pages == 0 && t.Fixed == 0 {
			continue
		}
		if rule, ok := crawl.RuleFor(t.Rule); ok {
			t.Title, t.Category = rule.Title, rule.Category
		} else {
			t.Title = strings.ReplaceAll(t.Rule, "_", " ")
		}
		t.TopPages = []techPage{}
		v := t
		by[t.Rule] = &v
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Clicks of the pages each rule touches, over the 28 days stored last.
	rows, err = s.pool.Query(ctx, `
		WITH newest AS (SELECT max(day) AS d FROM search_segments WHERE workspace_id = $1 AND brand_id = $3),
		clicks AS (
			SELECT page, sum(c) AS c FROM (
				SELECT page, day, max(c) AS c FROM (
					SELECT integration_id, page, day, sum(clicks)::float8 AS c FROM search_segments, newest
					WHERE workspace_id = $1 AND brand_id = $3 AND day > newest.d - 28 AND day <= newest.d
					GROUP BY integration_id, page, day
				) x GROUP BY page, day
			) y GROUP BY page
		),
		cur AS (
			SELECT DISTINCT i.rule, p.url FROM crawl_issues i JOIN crawl_pages p ON p.id = i.page_id
			WHERE i.workspace_id = $1 AND i.run_id = ANY($2::uuid[])
		),
		ranked AS (
			SELECT cur.rule, cur.url, clicks.c, row_number() OVER (PARTITION BY cur.rule ORDER BY clicks.c DESC, cur.url) AS n,
				sum(clicks.c) OVER (PARTITION BY cur.rule) AS total
			FROM cur JOIN clicks ON clicks.page = cur.url WHERE clicks.c > 0
		)
		SELECT rule, url, c, total FROM ranked WHERE n <= $4`, ws, latest, brand, techTopPages)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var rule, url string
		var c, total float64
		if err := rows.Scan(&rule, &url, &c, &total); err != nil {
			return nil, err
		}
		if t := by[rule]; t != nil {
			t.Clicks = total
			t.TopPages = append(t.TopPages, techPage{URL: url, Clicks: c})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sev := map[string]int{"error": 0, "warning": 1, "notice": 2}
	for _, t := range by {
		out = append(out, *t)
	}
	slices.SortFunc(out, func(x, y techRule) int {
		switch {
		case x.Clicks != y.Clicks:
			if x.Clicks > y.Clicks {
				return -1
			}
			return 1
		case sev[x.Severity] != sev[y.Severity]:
			return sev[x.Severity] - sev[y.Severity]
		case x.Pages != y.Pages:
			return y.Pages - x.Pages
		}
		return strings.Compare(x.Rule, y.Rule)
	})
	if len(out) > techRulesShown {
		out = out[:techRulesShown]
	}
	return out, nil
}

func (s *Server) techSitemaps(ctx context.Context, ws string, hosts []string) ([]techSitemap, error) {
	out := []techSitemap{}
	if len(hosts) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (host, path) host, path, submitted, indexed, errors, warnings, last_read
		FROM search_sitemaps
		WHERE workspace_id = $1 AND regexp_replace(lower(host), '^www\.', '') = ANY($2)
		ORDER BY host, path, checked_at DESC`, ws, hosts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var m techSitemap
		if err := rows.Scan(&m.Host, &m.Path, &m.Submitted, &m.Indexed, &m.Errors, &m.Warnings, &m.LastRead); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// techVitals is each page's 75th percentile Core Web Vitals by device
// over 28 days of real visits, worst first.
func (s *Server) techVitals(ctx context.Context, ws, brand string) ([]techVitals, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT path, device, count(*)::int,
			percentile_cont(0.75) WITHIN GROUP (ORDER BY lcp_ms) FILTER (WHERE lcp_ms IS NOT NULL),
			percentile_cont(0.75) WITHIN GROUP (ORDER BY inp_ms) FILTER (WHERE inp_ms IS NOT NULL),
			percentile_cont(0.75) WITHIN GROUP (ORDER BY cls) FILTER (WHERE cls IS NOT NULL)
		FROM page_vitals
		WHERE workspace_id = $1 AND brand_id = $2 AND occurred_at > now() - interval '28 days'
		GROUP BY path, device HAVING count(*) >= $3`, ws, brand, vitalsMinVisits)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []techVitals{}
	for rows.Next() {
		var v techVitals
		if err := rows.Scan(&v.Path, &v.Device, &v.Visits, &v.LCP, &v.INP, &v.CLS); err != nil {
			return nil, err
		}
		v.Poor = []string{}
		if v.LCP != nil && *v.LCP > 4000 {
			v.Poor = append(v.Poor, "lcp")
		}
		if v.INP != nil && *v.INP > 500 {
			v.Poor = append(v.Poor, "inp")
		}
		if v.CLS != nil && *v.CLS > 0.25 {
			v.Poor = append(v.Poor, "cls")
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(x, y techVitals) int {
		if len(x.Poor) != len(y.Poor) {
			return len(y.Poor) - len(x.Poor)
		}
		lx, ly := 0.0, 0.0
		if x.LCP != nil {
			lx = *x.LCP
		}
		if y.LCP != nil {
			ly = *y.LCP
		}
		switch {
		case lx > ly:
			return -1
		case lx < ly:
			return 1
		}
		return strings.Compare(x.Path+x.Device, y.Path+y.Device)
	})
	if len(out) > vitalsPages {
		out = out[:vitalsPages]
	}
	return out, nil
}
