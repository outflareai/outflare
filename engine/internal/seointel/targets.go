package seointel

import (
	"context"
	"errors"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/fixes"
)

// FixSource is the fixes list's source for search intelligence: what a
// page's keyword target, Search Console and rankings say needs doing.
const FixSource = "seo_keywords"

// Rules is search intelligence's catalog. Ids are a promise: never rename
// or remove one, add new ones.
var Rules = []crawl.Rule{
	{ID: "target_not_in_title", Severity: crawl.SevWarning, Category: "keywords", Title: "Target keyword not in the title",
		Why: "The title is the strongest on-page sign of what a page is about, and it is what searchers see in the results.",
		Fix: "Work the page's target keyword into its <title>, near the start, in a way that still reads naturally."},
	{ID: "target_not_in_h1", Severity: crawl.SevNotice, Category: "keywords", Title: "Target keyword not in the H1",
		Why: "The main heading tells visitors and search engines what the page covers.",
		Fix: "Use the target keyword, or a close variant, in the page's H1."},
	{ID: "target_not_in_description", Severity: crawl.SevNotice, Category: "keywords", Title: "Target keyword not in the description",
		Why: "Search engines bold the searched words in the snippet, so a description with the keyword draws more clicks.",
		Fix: "Write a meta description that uses the target keyword and says why the page answers it."},
	{ID: "keyword_cannibalised", Severity: crawl.SevWarning, Category: "keywords", Title: "Pages compete for one search",
		Why: "Google shows several of the site's pages for this search and splits the clicks between them, so none of them ranks as well as one strong page would.",
		Fix: "Choose the page that should rank for the search and make it the best answer. Merge the others into it and redirect them (301), or change their titles and headings to target other searches and link from them to the chosen page."},
	{ID: "target_shared", Severity: crawl.SevWarning, Category: "keywords", Title: "Pages target the same keyword",
		Why: "Two or more pages are set to rank for one keyword, so they compete with each other instead of with other sites.",
		Fix: "Keep the keyword on the page that answers it best, and give the others their own target keyword."},
	{ID: "internal_link_opportunity", Severity: crawl.SevNotice, Category: "keywords", Title: "Pages about this keyword don't link to its page",
		Why: "Links between pages on the same topic tell search engines which page is the one for it, and pass on some of their own strength.",
		Fix: "Add a link to the target page from each suggested page, with the keyword or a close variant as the link text, where it helps the reader."},
}

var ruleByID = func() map[string]crawl.Rule {
	m := make(map[string]crawl.Rule, len(Rules))
	for _, r := range Rules {
		m[r.ID] = r
	}
	return m
}()

// RuleFor looks a rule up by id.
func RuleFor(id string) (crawl.Rule, bool) { r, ok := ruleByID[id]; return r, ok }

// targetRules are the rules Refresh checks.
var targetRules = []string{"target_not_in_title", "target_not_in_h1", "target_not_in_description"}

// MaxKeyword is the longest keyword a target takes.
const MaxKeyword = 200

// Keyword is a keyword as targets keep it: lowercase, single spaces.
func Keyword(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// words splits text into lowercase words, dropping punctuation.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
}

// singular is a word without its plural ending: proxies is proxy, boxes
// is box, plans is plan.
func singular(w string) string {
	switch {
	case len(w) > 4 && strings.HasSuffix(w, "ies"):
		return w[:len(w)-3] + "y"
	case len(w) > 3 && (strings.HasSuffix(w, "ses") || strings.HasSuffix(w, "xes") || strings.HasSuffix(w, "zes") ||
		strings.HasSuffix(w, "ches") || strings.HasSuffix(w, "shes")):
		return w[:len(w)-2]
	case len(w) > 2 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss"):
		return w[:len(w)-1]
	}
	return w
}

// sameWord reports whether two words are the same, ignoring plurals.
func sameWord(a, b string) bool { return a == b || singular(a) == singular(b) }

// Mentions reports whether text has every word of keyword, in any order,
// plurals counting.
func Mentions(text, keyword string) bool {
	have := words(text)
	want := words(keyword)
	if len(want) == 0 {
		return true
	}
	for _, w := range want {
		found := false
		for _, h := range have {
			if sameWord(w, h) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Page is what a crawl read of a target's page.
type Page struct {
	Title       string   `json:"title"`
	H1          []string `json:"h1"`
	Description string   `json:"description"`
}

// Checks is how a page carries its target keyword. nil fields mean the
// latest crawl did not read the page.
type Checks struct {
	InTitle       *bool `json:"in_title"`
	InH1          *bool `json:"in_h1"`
	InDescription *bool `json:"in_description"`
}

// Check reads a page against a keyword.
func Check(p *Page, keyword string) Checks {
	if p == nil {
		return Checks{}
	}
	t, d := Mentions(p.Title, keyword), Mentions(p.Description, keyword)
	h := false
	for _, x := range p.H1 {
		if Mentions(x, keyword) {
			h = true
			break
		}
	}
	return Checks{InTitle: &t, InH1: &h, InDescription: &d}
}

// LatestCrawl is the site's latest finished crawl, or "".
func LatestCrawl(ctx context.Context, q Querier, ws, siteID string) (string, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT id::text FROM crawl_runs WHERE workspace_id = $1 AND site_id = $2 AND status = 'done'
		ORDER BY created_at DESC LIMIT 1`, ws, siteID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// CrawledPages reads what a crawl fetched of some URLs.
func CrawledPages(ctx context.Context, q Querier, ws, runID string, urls []string) (map[string]*Page, error) {
	out := map[string]*Page{}
	if runID == "" || len(urls) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT url, title, h1, meta_description FROM crawl_pages
		WHERE workspace_id = $1 AND run_id = $2 AND url = ANY($3) AND status = 'fetched'`, ws, runID, urls)
	if err != nil {
		return nil, err
	}
	var u string
	var p Page
	_, err = pgx.ForEachRow(rows, []any{&u, &p.Title, &p.H1, &p.Description}, func() error {
		c := p
		out[u] = &c
		return nil
	})
	return out, err
}

// Trigger is what made Refresh run. Each kind of evidence reopens only the
// fixes it checks: a crawl the target fixes, a Search Console sync the
// cannibalisation fixes. Other passes only add and confirm, so marking a
// fix done holds until new evidence says otherwise.
type Trigger string

const (
	TriggerCrawl  Trigger = "crawl"
	TriggerSearch Trigger = "search"
	TriggerChange Trigger = "change"
)

// Refresh puts what search intelligence finds on a site on the fixes list:
// keyword targets checked against a crawl (runID, or the latest finished
// one when it is ""), pages that target the same keyword, internal links
// worth adding to target pages, and pages that compete for one query in
// Search Console.
func Refresh(ctx context.Context, tx pgx.Tx, ws, siteID, runID string, t Trigger) (fixes.Changes, error) {
	var brand *string
	var host, brandName string
	if err := tx.QueryRow(ctx, `SELECT s.brand_id::text, s.host, COALESCE(b.name, '') FROM seo_sites s LEFT JOIN brands b ON b.id = s.brand_id
		WHERE s.id = $1 AND s.workspace_id = $2`, siteID, ws).Scan(&brand, &host, &brandName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fixes.Changes{}, nil
		}
		return fixes.Changes{}, err
	}
	crawlID := runID
	if crawlID == "" {
		var err error
		if crawlID, err = LatestCrawl(ctx, tx, ws, siteID); err != nil {
			return fixes.Changes{}, err
		}
	}
	rows, err := tx.Query(ctx, `SELECT url, keyword FROM page_targets WHERE workspace_id = $1 AND site_id = $2 AND status = 'set' AND keyword <> ''`, ws, siteID)
	if err != nil {
		return fixes.Changes{}, err
	}
	targets := map[string]string{}
	var urls []string
	var u, kw string
	if _, err := pgx.ForEachRow(rows, []any{&u, &kw}, func() error {
		targets[u] = kw
		urls = append(urls, u)
		return nil
	}); err != nil {
		return fixes.Changes{}, err
	}
	pages, err := CrawledPages(ctx, tx, ws, crawlID, urls)
	if err != nil {
		return fixes.Changes{}, err
	}
	var found []fixes.Found
	var keep []string
	for _, u := range urls {
		p := pages[u]
		if p == nil {
			keep = append(keep, u)
			continue
		}
		c := Check(p, targets[u])
		for rule, ok := range map[string]bool{"target_not_in_title": *c.InTitle, "target_not_in_h1": *c.InH1, "target_not_in_description": *c.InDescription} {
			if !ok {
				found = append(found, fixes.Found{Rule: rule, Severity: ruleByID[rule].Severity, Group: u, Count: 1, Unit: "pages"})
			}
		}
	}
	pass := runID
	if pass == "" {
		if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&pass); err != nil {
			return fixes.Changes{}, err
		}
	}
	run := fixes.Run{Workspace: ws, Source: FixSource, SubjectType: "seo_site", SubjectID: siteID, BrandID: brand, RunID: pass}
	tr := run
	tr.Rules, tr.Keep, tr.Reopen = targetRules, keep, t == TriggerCrawl
	ch, err := fixes.Sync(ctx, tx, tr, found)
	if err != nil {
		return ch, err
	}
	add := func(c fixes.Changes) {
		ch.New, ch.Reopened, ch.Confirmed = ch.New+c.New, ch.Reopened+c.Reopened, ch.Confirmed+c.Confirmed
	}

	// Pages that target the same keyword.
	shared, err := SharedTargets(ctx, tx, ws, siteID)
	if err != nil {
		return ch, err
	}
	found = nil
	for _, g := range shared {
		found = append(found, fixes.Found{Rule: "target_shared", Severity: ruleByID["target_shared"].Severity, Group: g.Keyword, Count: len(g.URLs), Unit: "pages"})
	}
	sr := run
	sr.Rules = []string{"target_shared"}
	c, err := fixes.Sync(ctx, tx, sr, found)
	if err != nil {
		return ch, err
	}
	add(c)

	src, err := SourceFor(ctx, tx, ws, host)
	if err != nil {
		return ch, err
	}

	// Internal links worth adding to target pages, from the crawl.
	sug, skipped, err := LinkSuggestions(ctx, tx, ws, siteID, host, crawlID, src)
	if err != nil {
		return ch, err
	}
	found = nil
	for _, l := range sug {
		found = append(found, fixes.Found{Rule: "internal_link_opportunity", Severity: ruleByID["internal_link_opportunity"].Severity, Group: l.URL, Count: len(l.From), Unit: "pages"})
	}
	lr := run
	lr.Rules, lr.Keep, lr.Reopen = []string{"internal_link_opportunity"}, skipped, t == TriggerCrawl
	c, err = fixes.Sync(ctx, tx, lr, found)
	if err != nil {
		return ch, err
	}
	add(c)

	// Pages that compete for one query. With no Search Console property the
	// fixes stay as they are: no data is not the same as fixed.
	if src == nil {
		return ch, nil
	}
	comp, err := Cannibalised(ctx, tx, ws, siteID, host, brandName, src, maxCannibalFixes)
	if err != nil {
		return ch, err
	}
	found = nil
	for _, q := range comp {
		found = append(found, fixes.Found{Rule: "keyword_cannibalised", Severity: ruleByID["keyword_cannibalised"].Severity, Group: q.Query, Count: len(q.Pages), Unit: "pages"})
	}
	cr := run
	cr.Rules, cr.Reopen = []string{"keyword_cannibalised"}, t == TriggerSearch
	c, err = fixes.Sync(ctx, tx, cr, found)
	add(c)
	return ch, err
}

// Suggestion is the keyword Search Console suggests for a page: the query
// that brought it the most clicks.
type Suggestion struct {
	URL         string  `json:"url"`
	Keyword     string  `json:"keyword"`
	Clicks      int     `json:"clicks"`
	Impressions int     `json:"impressions"`
	Position    float64 `json:"position"`
}

// minSuggestImpressions is how often a query must have shown a page to be
// suggested as its target.
const minSuggestImpressions = 10

// Suggestions are the targets Search Console suggests for a site's pages
// that have none (and were not dismissed), over the 28 days to src's
// newest day, most clicks first. urls narrows them to some pages.
func Suggestions(ctx context.Context, q Querier, ws, siteID, siteHost string, src *Source, urls []string, limit int) ([]Suggestion, error) {
	out := []Suggestion{}
	if src == nil || src.NewestDay == nil {
		return out, nil
	}
	rows, err := q.Query(ctx, `
		WITH agg AS (
			SELECT d.page, d.query, sum(d.clicks)::int AS clicks, sum(d.impressions)::int AS impressions,
				sum(d.position * d.impressions) / NULLIF(sum(d.impressions), 0) AS position
			FROM search_daily d
			WHERE d.workspace_id = $1 AND d.integration_id = $2 AND d.site = $3 AND `+PageHostSQL("d.page")+` = ANY($4)
			  AND d.day > $5::date - 28 AND d.day <= $5::date AND d.query <> ''
			  AND ($8::text[] IS NULL OR d.page = ANY($8))
			  AND NOT EXISTS (SELECT 1 FROM page_targets t WHERE t.workspace_id = $1 AND t.site_id = $6 AND t.url = d.page)
			GROUP BY d.page, d.query
			HAVING sum(d.impressions) >= $7
		), ranked AS (
			SELECT *, row_number() OVER (PARTITION BY page ORDER BY clicks DESC, impressions DESC, query) AS n FROM agg
		)
		SELECT page, query, clicks, impressions, COALESCE(position, 0) FROM ranked WHERE n = 1
		ORDER BY clicks DESC, impressions DESC, page LIMIT $9`,
		ws, src.IntegrationID, src.Property, PageHosts(siteHost), *src.NewestDay, siteID, minSuggestImpressions, urls, limit)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Suggestion])
	if err != nil || list == nil {
		return out, err
	}
	return list, nil
}

// AfterAudit is the crawl's hook (crawl.Service.AfterAudit): a finished
// crawl checks the site's keyword targets again.
func AfterAudit(ctx context.Context, tx pgx.Tx, ws, siteID, runID string) error {
	if _, err := Refresh(ctx, tx, ws, siteID, runID, TriggerCrawl); err != nil {
		return err
	}
	_, err := RefreshRegressions(ctx, tx, ws, siteID, runID)
	return err
}
