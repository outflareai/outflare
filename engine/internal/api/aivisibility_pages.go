package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/crawl"
)

// Your pages: how ready a brand's own pages are for AI answers, in one
// place. It joins what the latest audit of each of the brand's sites
// found (the AI rules, robots.txt for AI crawlers, llms.txt, the page's
// AI signals) with which of those pages the AI answers already cite, so
// a person fixes the pages that matter first. Everything comes from data
// Outflare already has: no request to the site and no AI call.

const (
	maxAIPages           = 200
	maxAICitedElsewhere  = 50
	maxAICompetitorPages = 20
)

// aiPageRules are the audit rules about AI answers: the ai_visibility
// category, plus structured data that doesn't match the page and pages
// only scripts fill in, which AI crawlers don't run.
func aiPageRules() []crawl.Rule {
	var out []crawl.Rule
	for _, r := range crawl.Rules {
		if r.Category == "ai_visibility" || r.ID == "schema_mismatch" || r.ID == "js_only_content" {
			out = append(out, r)
		}
	}
	return out
}

type aiPagesSite struct {
	SiteID      string    `json:"site_id"`
	Host        string    `json:"host"`
	CrawlID     string    `json:"crawl_id"`
	CrawledAt   time.Time `json:"crawled_at"`
	Pages       int       `json:"pages"`
	LLMsTxt     bool      `json:"llms_txt"`
	BlockedBots []string  `json:"blocked_bots"`
	Signals     []string  `json:"usage_signals"`
}

type aiPagesRule struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Severity string `json:"severity"`
	Why      string `json:"why"`
	Fix      string `json:"fix"`
	Pages    int    `json:"pages"`
}

type aiPage struct {
	URL       string          `json:"url"`
	Title     string          `json:"title"`
	SiteID    string          `json:"site_id"`
	CrawlID   string          `json:"crawl_id"`
	Answers   int             `json:"answers"`
	Questions int             `json:"questions"`
	Issues    []string        `json:"issues"`
	Signals   crawl.AISignals `json:"signals"`
}

// aiCompetitorPage is a competitor's page the answers cite.
type aiCompetitorPage struct {
	URL        string `json:"url"`
	Title      string `json:"title"`
	Competitor string `json:"competitor"`
	Answers    int    `json:"answers"`
	Questions  int    `json:"questions"`
	// NamedYou: those answers that named the brand as well.
	NamedYou int `json:"named_you"`
}

type aiCitedPage struct {
	URL       string `json:"url"`
	Title     string `json:"title"`
	Answers   int    `json:"answers"`
	Questions int    `json:"questions"`
}

func (s *Server) getAIVisibilityPages(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	days := 28
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 365 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "days is 1 to 365")
			return
		}
		days = n
	}
	from := time.Now().UTC().AddDate(0, 0, -days)
	out, err := s.aiVisibilityPages(r.Context(), a.WorkspaceID, a.Brand, from)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out["days"] = days
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) aiVisibilityPages(ctx context.Context, ws, brand string, from time.Time) (map[string]any, error) {
	// The latest audited crawl of each of the brand's sites.
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (r.site_id) r.site_id, s.host, r.id, COALESCE(r.finished_at, r.created_at), COALESCE(r.llms_txt_status = 200, false)
		FROM crawl_runs r JOIN seo_sites s ON s.id = r.site_id AND s.workspace_id = r.workspace_id
		WHERE r.workspace_id = $1 AND s.brand_id = $2 AND r.status IN ('done', 'stopped') AND r.health_score IS NOT NULL
		ORDER BY r.site_id, r.finished_at DESC NULLS LAST`, ws, brand)
	if err != nil {
		return nil, err
	}
	sites := []*aiPagesSite{}
	var runs []string
	byRun := map[string]*aiPagesSite{}
	var st aiPagesSite
	if _, err := pgx.ForEachRow(rows, []any{&st.SiteID, &st.Host, &st.CrawlID, &st.CrawledAt, &st.LLMsTxt}, func() error {
		c := st
		c.BlockedBots, c.Signals = []string{}, []string{}
		sites = append(sites, &c)
		runs = append(runs, c.CrawlID)
		byRun[c.CrawlID] = &c
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Slice(sites, func(i, j int) bool { return sites[i].Host < sites[j].Host })

	// AI search and user crawlers robots.txt keeps out, and how the site
	// says its content may be used.
	purpose := map[string]string{}
	for _, b := range crawl.AIBots {
		purpose[b.Token] = b.Purpose
	}
	if len(runs) > 0 {
		rows, err = s.pool.Query(ctx, `SELECT run_id, token FROM crawl_ai_bots
			WHERE workspace_id = $1 AND run_id = ANY($2) AND (NOT root_allowed OR blocked_pages > 0) ORDER BY token`, ws, runs)
		if err != nil {
			return nil, err
		}
		var run, token string
		if _, err := pgx.ForEachRow(rows, []any{&run, &token}, func() error {
			if p := purpose[token]; p == "search" || p == "user" {
				byRun[run].BlockedBots = append(byRun[run].BlockedBots, token)
			}
			return nil
		}); err != nil {
			return nil, err
		}
		rows, err = s.pool.Query(ctx, `SELECT run_id, body FROM crawl_robots WHERE run_id = ANY($1) ORDER BY host`, runs)
		if err != nil {
			return nil, err
		}
		var body string
		if _, err := pgx.ForEachRow(rows, []any{&run, &body}, func() error {
			byRun[run].Signals = append(byRun[run].Signals, crawl.UsageSignals(body)...)
			return nil
		}); err != nil {
			return nil, err
		}
	}

	// Which of the brand's pages the answers cite.
	cited := map[string]*aiCitedPage{}
	rows, err = s.pool.Query(ctx, `
		SELECT c.url, max(c.title), count(DISTINCT c.sample_id), count(DISTINCT s.prompt_id)
		FROM ai_citations c JOIN ai_samples s ON s.id = c.sample_id
		WHERE c.workspace_id = $1 AND s.brand_id = $2 AND c.owner = 'brand' AND s.status = 'ok' AND s.sampled_at >= $3
		GROUP BY c.url`, ws, brand, from)
	if err != nil {
		return nil, err
	}
	var cp aiCitedPage
	if _, err := pgx.ForEachRow(rows, []any{&cp.URL, &cp.Title, &cp.Answers, &cp.Questions}, func() error {
		c := cp
		cited[c.URL] = &c
		return nil
	}); err != nil {
		return nil, err
	}

	// The audit's AI rules, page by page.
	catalog := aiPageRules()
	ruleIDs := make([]string, len(catalog))
	for i, rl := range catalog {
		ruleIDs[i] = rl.ID
	}
	pages := []*aiPage{}
	ruleCount := map[string]int{}
	checked := 0
	if len(runs) > 0 {
		rows, err = s.pool.Query(ctx, `
			SELECT p.id, p.run_id, p.url, p.title, p.ai_signals,
			       COALESCE(array_agg(DISTINCT i.rule) FILTER (WHERE i.rule IS NOT NULL), '{}')
			FROM crawl_pages p LEFT JOIN crawl_issues i ON i.page_id = p.id AND i.rule = ANY($3)
			WHERE p.workspace_id = $1 AND p.run_id = ANY($2) AND p.status = 'fetched' AND p.content_type LIKE 'text/html%'
			GROUP BY p.id`, ws, runs, ruleIDs)
		if err != nil {
			return nil, err
		}
		var id int64
		var run, url, title string
		var raw []byte
		var issues []string
		if _, err := pgx.ForEachRow(rows, []any{&id, &run, &url, &title, &raw, &issues}, func() error {
			checked++
			site := byRun[run]
			site.Pages++
			pg := &aiPage{URL: url, Title: title, SiteID: site.SiteID, CrawlID: run, Issues: issues}
			_ = json.Unmarshal(raw, &pg.Signals)
			for _, is := range issues {
				ruleCount[is]++
			}
			if c := cited[url]; c != nil {
				pg.Answers, pg.Questions = c.Answers, c.Questions
				delete(cited, url)
			}
			if len(issues) > 0 || pg.Answers > 0 {
				pages = append(pages, pg)
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	// Cited pages first, then those with the most problems.
	sort.SliceStable(pages, func(i, j int) bool {
		a, b := pages[i], pages[j]
		if a.Answers != b.Answers {
			return a.Answers > b.Answers
		}
		if len(a.Issues) != len(b.Issues) {
			return len(a.Issues) > len(b.Issues)
		}
		return a.URL < b.URL
	})
	withIssues := 0
	for _, p := range pages {
		if len(p.Issues) > 0 {
			withIssues++
		}
	}
	if len(pages) > maxAIPages {
		pages = pages[:maxAIPages]
	}
	rules := []aiPagesRule{}
	for _, rl := range catalog {
		if n := ruleCount[rl.ID]; n > 0 {
			rules = append(rules, aiPagesRule{ID: rl.ID, Title: rl.Title, Severity: rl.Severity, Why: rl.Why, Fix: rl.Fix, Pages: n})
		}
	}
	// Cited pages the latest crawls didn't read.
	elsewhere := []aiCitedPage{}
	for _, c := range cited {
		elsewhere = append(elsewhere, *c)
	}
	sort.Slice(elsewhere, func(i, j int) bool {
		return elsewhere[i].Answers > elsewhere[j].Answers || elsewhere[i].Answers == elsewhere[j].Answers && elsewhere[i].URL < elsewhere[j].URL
	})
	if len(elsewhere) > maxAICitedElsewhere {
		elsewhere = elsewhere[:maxAICitedElsewhere]
	}
	// Competitors' pages the answers cite, to compare with.
	rows, err = s.pool.Query(ctx, `
		SELECT c.url, max(c.title), max(c.competitor), count(DISTINCT c.sample_id), count(DISTINCT s.prompt_id),
		       count(DISTINCT c.sample_id) FILTER (WHERE EXISTS (SELECT 1 FROM ai_mentions m WHERE m.sample_id = s.id AND m.entity = 'brand'))
		FROM ai_citations c JOIN ai_samples s ON s.id = c.sample_id
		WHERE c.workspace_id = $1 AND s.brand_id = $2 AND c.owner = 'competitor' AND s.status = 'ok' AND s.sampled_at >= $3
		GROUP BY c.url ORDER BY 4 DESC, 1 LIMIT $4`, ws, brand, from, maxAICompetitorPages)
	if err != nil {
		return nil, err
	}
	theirs := []aiCompetitorPage{}
	var tp aiCompetitorPage
	if _, err := pgx.ForEachRow(rows, []any{&tp.URL, &tp.Title, &tp.Competitor, &tp.Answers, &tp.Questions, &tp.NamedYou}, func() error {
		theirs = append(theirs, tp)
		return nil
	}); err != nil {
		return nil, err
	}
	reviews, err := s.aiPageReviews(ctx, ws, brand)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"reviews":           reviews,
		"competitor_pages":  theirs,
		"from":              from.Format(time.RFC3339),
		"sites":             sites,
		"pages_checked":     checked,
		"pages_with_issues": withIssues,
		"rules":             rules,
		"pages":             pages,
		"cited_not_crawled": elsewhere,
	}, nil
}
