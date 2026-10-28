// Package indexability works out which of a site's pages Google has
// indexed. No one source is right on its own: the crawl knows which pages
// exist and which can be indexed, the sitemaps say which the site wants
// indexed, Search Console's URL Inspection says what Google decided when it
// last looked (and can be weeks old), impressions prove Google showed a page
// in the last 28 days, and an SEO tool (Ahrefs, DataForSEO) sees pages
// ranking even without Search Console. The report puts them side by side
// per page, gives each page a verdict with the evidence for it, and names
// the problems worth fixing. Snapshots keep the counts per day.
package indexability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/seointel"
)

// JobSnapshot takes a site's snapshot. The crawl queues it when a crawl
// ends, and the Search Console sync when it has inspected pages.
const JobSnapshot = "indexability.snapshot"

// Enqueue queues a snapshot of a site, once per key (such as a crawl's id).
func Enqueue(ctx context.Context, q jobs.Querier, ws, siteID, key string) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobSnapshot, WorkspaceID: &ws, MaxAttempts: 3,
		Payload: map[string]string{"site_id": siteID}, IdempotencyKey: siteID + ":" + key})
	return err
}

// AfterCrawl is the crawl's hook (crawl.Service.AfterCrawl): a crawl that
// read pages queues a snapshot of the report.
func AfterCrawl(ctx context.Context, tx pgx.Tx, ws, siteID, runID string) error {
	return Enqueue(ctx, tx, ws, siteID, "crawl:"+runID)
}

// Handler runs JobSnapshot jobs. A site deleted since is skipped.
func Handler(pool *pgxpool.Pool) jobs.Handler {
	return func(ctx context.Context, job jobs.Job) error {
		var p struct {
			SiteID string `json:"site_id"`
		}
		if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
			return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
		}
		site := Site{ID: p.SiteID, WorkspaceID: *job.WorkspaceID}
		err := pool.QueryRow(ctx, `SELECT host FROM seo_sites WHERE id = $1 AND workspace_id = $2`, site.ID, site.WorkspaceID).Scan(&site.Host)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		_, err = Snapshot(ctx, pool, site)
		return err
	}
}

// Verdicts, strongest evidence first.
const (
	// Indexed: Search Console showed it in results in the last 28 days, or
	// URL Inspection says it is on Google.
	Indexed = "indexed"
	// LikelyIndexed: an SEO tool sees it ranking, and Search Console has
	// nothing to say either way.
	LikelyIndexed = "likely_indexed"
	// NotIndexed: URL Inspection says Google hasn't indexed it, and nothing
	// shows it in results.
	NotIndexed = "not_indexed"
	// Unknown: no source has looked at it yet.
	Unknown = "unknown"
)

// Verdicts lists them in order.
var Verdicts = []string{Indexed, LikelyIndexed, NotIndexed, Unknown}

// Issue is a problem the report can find on a page.
type Issue struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Why   string `json:"why"`
	Fix   string `json:"fix"`
	// error, warning or notice, as in the site audit.
	Severity string `json:"severity"`
}

// Issues lists every issue, worst first.
var Issues = []Issue{
	{ID: "indexable_not_indexed", Severity: "error", Title: "Can be indexed, but Google hasn't",
		Why: "The page answers, isn't noindex and points its canonical at itself, yet Google's URL Inspection says it isn't indexed and it got no impressions. It can't bring any search traffic.",
		Fix: "Open the inspection in Search Console to see Google's reason. Usually: link to it from more pages, make it more useful than similar pages, list it in the sitemap, then ask Google to index it."},
	{ID: "sitemap_not_indexable", Severity: "error", Title: "In the sitemap, but can't be indexed",
		Why: "The sitemap asks Google to index a page that redirects, is broken, is noindex or points its canonical elsewhere. Google trusts sitemaps with mistakes less.",
		Fix: "Take it out of the sitemap, or fix the page if it should be indexed."},
	{ID: "indexed_not_indexable", Severity: "warning", Title: "Indexed, but now can't be",
		Why: "Google still shows a page that is now noindex, redirects, is broken or points its canonical elsewhere. It drops out soon, with whatever traffic it brings.",
		Fix: "If the change was meant, nothing to do. If not, undo it before Google drops the page."},
	{ID: "google_canonical_differs", Severity: "warning", Title: "Google chose another canonical",
		Why: "Google indexes a different URL than the one the page names as canonical, so this page's signals go to the other one.",
		Fix: "Make the canonical, internal links and sitemap agree on one URL, or merge the duplicate content."},
	{ID: "not_found_by_crawl", Severity: "warning", Title: "Google knows it, the crawl didn't find it",
		Why: "Google has the page but no link on the site leads to it (an orphan), or it is an old address. Orphans get crawled less and lose rankings.",
		Fix: "Link to it from a related page, or redirect it if it's gone."},
	{ID: "sources_disagree", Severity: "notice", Title: "Sources disagree",
		Why: "URL Inspection says it isn't indexed, but Search Console showed it in results in the last 28 days. The inspection is likely out of date.",
		Fix: "Nothing to do: the next inspection usually agrees. Check it again if it keeps disagreeing."},
	{ID: "missing_from_sitemap", Severity: "notice", Title: "Can be indexed, not in the sitemap",
		Why: "The sitemap tells Google which pages matter. Pages left out are found more slowly.",
		Fix: "Add it to the sitemap, or make it noindex if it shouldn't be found."},
}

// Inspection is what Search Console's URL Inspection said last.
type Inspection struct {
	Verdict         string     `json:"verdict"`
	Coverage        string     `json:"coverage"`
	GoogleCanonical string     `json:"google_canonical"`
	UserCanonical   string     `json:"user_canonical"`
	LastCrawl       *time.Time `json:"last_crawl"`
	InspectedAt     time.Time  `json:"inspected_at"`
}

// Page is one URL with what every source says about it.
type Page struct {
	URL string `json:"url"`
	// What the crawl found; Crawled is false for pages only Google or the
	// SEO tool know.
	Crawled    bool   `json:"crawled"`
	Status     string `json:"crawl_status"`
	HTTPStatus *int   `json:"http_status"`
	InSitemap  bool   `json:"in_sitemap"`
	Depth      *int   `json:"depth"`
	LinksIn    int    `json:"links_in"`
	// Indexable is null when the crawl didn't read the page; when false,
	// Reason says why: noindex, canonical, redirect, error, blocked or failed.
	Indexable *bool  `json:"indexable"`
	Reason    string `json:"not_indexable_reason"`
	// Search Console over the last 28 days, and its last URL Inspection.
	Clicks      int         `json:"clicks"`
	Impressions int         `json:"impressions"`
	Inspection  *Inspection `json:"inspection"`
	// The SEO tool's latest keywords snapshot: keywords it ranks for and
	// their estimated monthly traffic.
	SEOKeywords int     `json:"seo_keywords"`
	SEOTraffic  float64 `json:"seo_traffic"`
	Verdict     string  `json:"verdict"`
	// Evidence for the verdict: search_console (impressions),
	// url_inspection, seo_tool.
	Evidence []string `json:"evidence"`
	Issues   []string `json:"issues"`
}

// Sources says what the report could use and how fresh it is.
type Sources struct {
	Crawl struct {
		RunID      *string    `json:"run_id"`
		FinishedAt *time.Time `json:"finished_at"`
		// Complete: the crawl read every page it found, so a page it didn't
		// find really isn't linked.
		Complete  bool `json:"complete"`
		Sitemaps  bool `json:"sitemaps"`
		Pages     int  `json:"pages"`
		InSitemap int  `json:"in_sitemap"`
	} `json:"crawl"`
	SearchConsole struct {
		Connected   bool       `json:"connected"`
		NewestDay   *string    `json:"newest_day"`
		Inspected   int        `json:"inspected"`
		InspectedAt *time.Time `json:"inspected_at"`
	} `json:"search_console"`
	SEOTool struct {
		Connector *string `json:"connector"`
		Day       *string `json:"day"`
		Pages     int     `json:"pages"`
	} `json:"seo_tool"`
}

// Summary counts a report's pages.
type Summary struct {
	Pages         int            `json:"pages"`
	Crawled       int            `json:"crawled"`
	Indexable     int            `json:"indexable"`
	InSitemap     int            `json:"in_sitemap"`
	Indexed       int            `json:"indexed"`
	LikelyIndexed int            `json:"likely_indexed"`
	NotIndexed    int            `json:"not_indexed"`
	Unknown       int            `json:"unknown"`
	Issues        map[string]int `json:"issues"`
}

// Report is a site's indexability now.
type Report struct {
	Summary Summary `json:"summary"`
	Sources Sources `json:"sources"`
	Pages   []Page  `json:"-"`
}

// Site is the site a report is for.
type Site struct {
	ID, WorkspaceID, Host string
}

// maxRows caps each source, so one huge property can't stall a report.
const maxRows = 50000

// Build reads every source for a site and works out each page.
func Build(ctx context.Context, pool *pgxpool.Pool, site Site) (Report, error) {
	var rep Report
	host := seointel.Bare(site.Host)
	pages := map[string]*Page{}
	get := func(u string) *Page {
		u = clean(u)
		p := pages[u]
		if p == nil {
			p = &Page{URL: u, Evidence: []string{}, Issues: []string{}}
			pages[u] = p
		}
		return p
	}
	onSite := func(u string) bool {
		pu, err := url.Parse(u)
		return err == nil && seointel.Bare(pu.Hostname()) == host
	}

	// The latest finished crawl.
	src := &rep.Sources
	var reason *string
	err := pool.QueryRow(ctx, `SELECT id::text, finished_at, reason FROM crawl_runs
		WHERE workspace_id = $1 AND site_id = $2 AND status = 'done' ORDER BY created_at DESC LIMIT 1`,
		site.WorkspaceID, site.ID).Scan(&src.Crawl.RunID, &src.Crawl.FinishedAt, &reason)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return rep, err
	}
	src.Crawl.Complete = reason != nil && *reason == "done"
	if src.Crawl.RunID != nil {
		rows, err := pool.Query(ctx, `SELECT url, status, http_status, depth, links_in, noindex, canonical, redirect_to,
				in_sitemap OR source = 'sitemap'
			FROM crawl_pages WHERE run_id = $1 AND workspace_id = $2 AND status <> 'queued' ORDER BY id LIMIT $3`,
			*src.Crawl.RunID, site.WorkspaceID, maxRows)
		if err != nil {
			return rep, err
		}
		for rows.Next() {
			var u, status, canonical, redirect string
			var code *int
			var depth, linksIn int
			var noindex, inMap bool
			if err := rows.Scan(&u, &status, &code, &depth, &linksIn, &noindex, &canonical, &redirect, &inMap); err != nil {
				rows.Close()
				return rep, err
			}
			p := get(u)
			p.Crawled, p.Status, p.HTTPStatus, p.Depth, p.LinksIn, p.InSitemap = true, status, code, &depth, linksIn, inMap
			ok, why := indexable(p.URL, status, code, noindex, canonical, redirect)
			if status != "skipped" {
				p.Indexable, p.Reason = &ok, why
			}
			src.Crawl.Pages++
			if inMap {
				src.Crawl.InSitemap++
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return rep, err
		}
		src.Crawl.Sitemaps = src.Crawl.InSitemap > 0
	}

	// Search Console: the property that covers the site (as the rest of the
	// SEO module picks it), what it showed over the 28 days up to its newest
	// day, and its last inspection of each page.
	sc, err := seointel.SourceFor(ctx, pool, site.WorkspaceID, site.Host)
	if err != nil {
		return rep, err
	}
	if sc != nil {
		src.SearchConsole.Connected, src.SearchConsole.NewestDay = true, sc.NewestDay
		args := []any{site.WorkspaceID, sc.IntegrationID, sc.Property, seointel.PageHosts(site.Host), maxRows}
		where := `workspace_id = $1 AND integration_id = $2 AND site = $3 AND ` + seointel.PageHostSQL("%s") + ` = ANY($4)`
		if sc.NewestDay != nil {
			rows, err := pool.Query(ctx, `SELECT page, sum(clicks)::int, sum(impressions)::int FROM search_daily
				WHERE `+fmt.Sprintf(where, "page")+` AND day > $6::date - 28 GROUP BY page LIMIT $5`, append(args, *sc.NewestDay)...)
			if err != nil {
				return rep, err
			}
			for rows.Next() {
				var u string
				var clicks, imp int
				if err := rows.Scan(&u, &clicks, &imp); err != nil {
					rows.Close()
					return rep, err
				}
				p := get(u)
				p.Clicks += clicks
				p.Impressions += imp
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return rep, err
			}
		}
		rows, err := pool.Query(ctx, `SELECT url, verdict, coverage, google_canonical, user_canonical, last_crawl, inspected_at
			FROM url_inspections WHERE `+fmt.Sprintf(where, "url")+` ORDER BY url LIMIT $5`, args...)
		if err != nil {
			return rep, err
		}
		for rows.Next() {
			var u string
			var in Inspection
			if err := rows.Scan(&u, &in.Verdict, &in.Coverage, &in.GoogleCanonical, &in.UserCanonical, &in.LastCrawl, &in.InspectedAt); err != nil {
				rows.Close()
				return rep, err
			}
			get(u).Inspection = &in
			src.SearchConsole.Inspected++
			if src.SearchConsole.InspectedAt == nil || in.InspectedAt.After(*src.SearchConsole.InspectedAt) {
				t := in.InspectedAt
				src.SearchConsole.InspectedAt = &t
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return rep, err
		}
	}

	// The SEO tool's latest keywords snapshot for the host.
	rows, err := pool.Query(ctx, `
		WITH latest AS (
			SELECT integration_id, connector, max(day) AS day FROM seo_snapshots
			WHERE workspace_id = $1 AND report = 'keywords' AND regexp_replace(lower(host), '^www\.', '') = $2
			GROUP BY integration_id, connector ORDER BY max(day) DESC LIMIT 1
		)
		SELECT l.connector, to_char(l.day, 'YYYY-MM-DD'), s.url, count(*)::int, COALESCE(sum((s.metrics->>'traffic')::float8), 0)
		FROM latest l JOIN seo_snapshots s ON s.integration_id = l.integration_id AND s.report = 'keywords' AND s.day = l.day
		WHERE s.workspace_id = $1 AND s.url <> '' GROUP BY l.connector, l.day, s.url LIMIT $3`, site.WorkspaceID, host, maxRows)
	if err != nil {
		return rep, err
	}
	for rows.Next() {
		var conn, day, u string
		var n int
		var traffic float64
		if err := rows.Scan(&conn, &day, &u, &n, &traffic); err != nil {
			rows.Close()
			return rep, err
		}
		src.SEOTool.Connector, src.SEOTool.Day = &conn, &day
		if !onSite(u) {
			continue
		}
		p := get(u)
		p.SEOKeywords += n
		p.SEOTraffic += traffic
		src.SEOTool.Pages++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return rep, err
	}

	for _, p := range pages {
		judge(p, src)
		rep.Pages = append(rep.Pages, *p)
	}
	sort.Slice(rep.Pages, func(i, j int) bool {
		a, b := rep.Pages[i], rep.Pages[j]
		if wa, wb := worst(a.Issues), worst(b.Issues); wa != wb {
			return wa < wb
		}
		if a.Impressions != b.Impressions {
			return a.Impressions > b.Impressions
		}
		return a.URL < b.URL
	})
	rep.Summary = summarise(rep.Pages)
	return rep, nil
}

// worst ranks a page by its worst issue: 0 for an error, 1 a warning, 2 a
// notice, 3 none.
func worst(ids []string) int {
	w := 3
	for _, id := range ids {
		for _, i := range Issues {
			if i.ID == id {
				w = min(w, map[string]int{"error": 0, "warning": 1, "notice": 2}[i.Severity])
			}
		}
	}
	return w
}

// clean drops a URL's fragment, which no source treats as a page.
func clean(u string) string {
	if i := strings.IndexByte(u, '#'); i >= 0 {
		u = u[:i]
	}
	return u
}

// indexable says whether a crawled page can be indexed, and why not.
func indexable(u, status string, code *int, noindex bool, canonical, redirect string) (bool, string) {
	switch {
	case status == "blocked":
		return false, "blocked"
	case status == "failed":
		return false, "failed"
	case redirect != "" || (code != nil && *code >= 300 && *code < 400):
		return false, "redirect"
	case code != nil && *code >= 400:
		return false, "error"
	case noindex:
		return false, "noindex"
	case canonical != "" && !sameURL(canonical, u):
		return false, "canonical"
	}
	return true, ""
}

// sameURL compares two URLs, ignoring a trailing slash and the scheme's
// case, as canonicals are written loosely.
func sameURL(a, b string) bool {
	norm := func(s string) string { return strings.TrimSuffix(strings.ToLower(clean(s)), "/") }
	return norm(a) == norm(b)
}

// judge sets a page's verdict, evidence and issues.
func judge(p *Page, src *Sources) {
	in := p.Inspection
	inspectedIn := in != nil && in.Verdict == "PASS"
	inspectedOut := in != nil && (in.Verdict == "FAIL" || in.Verdict == "NEUTRAL" || in.Verdict == "PARTIAL") && !inspectedIn
	if p.Impressions > 0 {
		p.Evidence = append(p.Evidence, "search_console")
	}
	if in != nil {
		p.Evidence = append(p.Evidence, "url_inspection")
	}
	if p.SEOKeywords > 0 {
		p.Evidence = append(p.Evidence, "seo_tool")
	}
	switch {
	case p.Impressions > 0 || inspectedIn:
		p.Verdict = Indexed
	case p.SEOKeywords > 0 && !inspectedOut:
		p.Verdict = LikelyIndexed
	case inspectedOut:
		p.Verdict = NotIndexed
	default:
		p.Verdict = Unknown
	}
	add := func(id string) { p.Issues = append(p.Issues, id) }
	can := p.Indexable != nil && *p.Indexable
	cannot := p.Indexable != nil && !*p.Indexable
	if can && p.Verdict == NotIndexed {
		add("indexable_not_indexed")
	}
	if cannot && p.InSitemap {
		add("sitemap_not_indexable")
	}
	if cannot && p.Verdict == Indexed && p.Reason != "blocked" && p.Reason != "failed" {
		add("indexed_not_indexable")
	}
	if in != nil && in.GoogleCanonical != "" && !sameURL(in.GoogleCanonical, p.URL) &&
		(in.UserCanonical == "" || !sameURL(in.GoogleCanonical, in.UserCanonical)) {
		add("google_canonical_differs")
	}
	if !p.Crawled && src.Crawl.RunID != nil && src.Crawl.Complete && (p.Verdict == Indexed || p.Verdict == LikelyIndexed) {
		add("not_found_by_crawl")
	}
	if p.Impressions > 0 && inspectedOut {
		add("sources_disagree")
	}
	if can && !p.InSitemap && src.Crawl.Sitemaps {
		add("missing_from_sitemap")
	}
}

func summarise(pages []Page) Summary {
	s := Summary{Issues: map[string]int{}}
	for _, is := range Issues {
		s.Issues[is.ID] = 0
	}
	for _, p := range pages {
		s.Pages++
		if p.Crawled {
			s.Crawled++
		}
		if p.Indexable != nil && *p.Indexable {
			s.Indexable++
		}
		if p.InSitemap {
			s.InSitemap++
		}
		switch p.Verdict {
		case Indexed:
			s.Indexed++
		case LikelyIndexed:
			s.LikelyIndexed++
		case NotIndexed:
			s.NotIndexed++
		default:
			s.Unknown++
		}
		for _, id := range p.Issues {
			s.Issues[id]++
		}
	}
	return s
}

// Snapshot stores today's counts for a site, replacing an earlier one of
// the same day.
func Snapshot(ctx context.Context, pool *pgxpool.Pool, site Site) (Summary, error) {
	rep, err := Build(ctx, pool, site)
	if err != nil {
		return Summary{}, err
	}
	s := rep.Summary
	issues, _ := json.Marshal(s.Issues)
	_, err = pool.Exec(ctx, `
		INSERT INTO indexability_snapshots (workspace_id, site_id, day, pages, crawled, indexable, in_sitemap, indexed, likely_indexed, not_indexed, unknown, issues)
		VALUES ($1, $2, current_date, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (site_id, day) DO UPDATE SET pages = EXCLUDED.pages, crawled = EXCLUDED.crawled, indexable = EXCLUDED.indexable,
			in_sitemap = EXCLUDED.in_sitemap, indexed = EXCLUDED.indexed, likely_indexed = EXCLUDED.likely_indexed,
			not_indexed = EXCLUDED.not_indexed, unknown = EXCLUDED.unknown, issues = EXCLUDED.issues, taken_at = now()`,
		site.WorkspaceID, site.ID, s.Pages, s.Crawled, s.Indexable, s.InSitemap, s.Indexed, s.LikelyIndexed, s.NotIndexed, s.Unknown, issues)
	return s, err
}

// Day is one snapshot.
type Day struct {
	Day string `json:"day"`
	Summary
}

// History is a site's snapshots over the last days, oldest first.
func History(ctx context.Context, pool *pgxpool.Pool, site Site, days int) ([]Day, error) {
	rows, err := pool.Query(ctx, `SELECT to_char(day, 'YYYY-MM-DD'), pages, crawled, indexable, in_sitemap, indexed, likely_indexed, not_indexed, unknown, issues
		FROM indexability_snapshots WHERE workspace_id = $1 AND site_id = $2 AND day > current_date - $3::int ORDER BY day`,
		site.WorkspaceID, site.ID, days)
	if err != nil {
		return nil, err
	}
	out := []Day{}
	for rows.Next() {
		var d Day
		if err := rows.Scan(&d.Day, &d.Pages, &d.Crawled, &d.Indexable, &d.InSitemap, &d.Indexed, &d.LikelyIndexed, &d.NotIndexed, &d.Unknown, &d.Issues); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, d)
	}
	rows.Close()
	return out, rows.Err()
}
