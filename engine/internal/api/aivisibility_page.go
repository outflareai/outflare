package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/aivis"
	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// One page AI answers cite, in full: when the answers first and last cited
// it, which questions they answered, the site's numbers and the links to
// the page (from the workspace's own SEO connection, when read), who the
// answers named next to it, and what reading the page showed: whether it
// links to the brand and each competitor, or only names them. Pages are
// read by OutflareBot's polite, robots-aware page read, in a job: the most
// cited ones after each run, and any one on request.

const (
	// pageChecksBatch is how many of the most cited pages a run's job reads.
	pageChecksBatch = 25
	// pageCheckAgain is how old a read is before the run's job reads the
	// page again.
	pageCheckAgain = 14 * 24 * time.Hour
	pageDays       = 90
)

type aiVisPageQuestion struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	Answers int    `json:"answers"`
}

type aiVisPageFound struct {
	Name         string `json:"name"`
	CompetitorID string `json:"competitor_id,omitempty"`
	// How the page has them: link, mention (named, no link) or absent.
	How      string `json:"how"`
	Nofollow bool   `json:"nofollow"`
	Anchor   string `json:"anchor"`
}

type aiVisPageCheck struct {
	// none (never read), queued, done or unreachable.
	Status    string           `json:"status"`
	CheckedAt *time.Time       `json:"checked_at"`
	Found     []aiVisPageFound `json:"found"`
}

type aiVisPageLinks struct {
	TotalLinks       *float64  `json:"total_links"`
	ReferringDomains int       `json:"referring_domains"`
	ReadAt           time.Time `json:"read_at"`
}

type aiVisPage struct {
	URL        string                `json:"url"`
	Host       string                `json:"host"`
	Title      string                `json:"title"`
	SourceType string                `json:"source_type"`
	FirstSeen  *time.Time            `json:"first_seen_at"`
	LastSeen   *time.Time            `json:"last_seen_at"`
	Answers    int                   `json:"answers"`
	Named      int                   `json:"named"`
	Reads      int                   `json:"reads"`
	Questions  []aiVisPageQuestion   `json:"questions"`
	NamedWith  []aiVisPlacementBrand `json:"competitors"`
	// The site's numbers, from Presence's reads with the workspace's SEO
	// connection; null when never read.
	DomainRating  *float64        `json:"domain_rating"`
	URLRating     *float64        `json:"url_rating"`
	DomainTraffic *float64        `json:"domain_traffic"`
	Links         *aiVisPageLinks `json:"links"`
	Check         aiVisPageCheck  `json:"check"`
	Opportunity   *string         `json:"opportunity_id"`
}

// aiVisPageURL checks a page address from a request.
func aiVisPageURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if raw == "" || len(raw) > 2048 || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", false
	}
	return raw, true
}

func (s *Server) getAIVisibilityPage(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	page, ok := aiVisPageURL(r.URL.Query().Get("page"))
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "page is the address of a page AI answers cite")
		return
	}
	ctx := r.Context()
	from := time.Now().UTC().AddDate(0, 0, -pageDays)
	ws, brand := a.WorkspaceID, a.Brand
	p := aiVisPage{URL: page, Questions: []aiVisPageQuestion{}, NamedWith: []aiVisPlacementBrand{}, Check: aiVisPageCheck{Status: "none", Found: []aiVisPageFound{}}}
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(max(c.host), ''), COALESCE(max(c.title), ''), COALESCE(max(c.source_type), ''), min(c.sampled_at), max(c.sampled_at), count(DISTINCT c.sample_id),
		       count(DISTINCT c.sample_id) FILTER (WHERE EXISTS (SELECT 1 FROM ai_mentions m WHERE m.sample_id = c.sample_id AND m.entity = 'brand'))
		FROM ai_citations c JOIN ai_samples s ON s.id = c.sample_id
		WHERE c.workspace_id = $1 AND s.brand_id = $2 AND c.url = $3 AND s.status = 'ok' AND s.sampled_at >= $4`, ws, brand, page, from).
		Scan(&p.Host, &p.Title, &p.SourceType, &p.FirstSeen, &p.LastSeen, &p.Answers, &p.Named)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var readFirst, readLast *time.Time
	var readHost, readTitle string
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(max(r.host), ''), COALESCE(max(r.title), ''), min(r.sampled_at), max(r.sampled_at), count(DISTINCT r.sample_id)
		FROM ai_pages_read r JOIN ai_samples s ON s.id = r.sample_id
		WHERE r.workspace_id = $1 AND s.brand_id = $2 AND r.url = $3 AND NOT r.cited AND s.status = 'ok' AND s.sampled_at >= $4`, ws, brand, page, from).
		Scan(&readHost, &readTitle, &readFirst, &readLast, &p.Reads); err != nil {
		s.internal(w, r, err)
		return
	}
	if p.Answers == 0 && p.Reads == 0 {
		writeError(w, r, http.StatusNotFound, "not_found", "no answer cited or read this page in the last 90 days")
		return
	}
	if p.Host == "" {
		p.Host, p.Title = readHost, readTitle
	}
	if p.FirstSeen == nil || readFirst != nil && readFirst.Before(*p.FirstSeen) {
		p.FirstSeen = readFirst
	}
	if p.LastSeen == nil || readLast != nil && readLast.After(*p.LastSeen) {
		p.LastSeen = readLast
	}
	// The questions the answers citing or reading it answered.
	rows, err := s.pool.Query(ctx, `
		SELECT q.id::text, q.text, count(DISTINCT x.sample_id) FROM (
			SELECT c.sample_id FROM ai_citations c WHERE c.workspace_id = $1 AND c.url = $3
			UNION SELECT r.sample_id FROM ai_pages_read r WHERE r.workspace_id = $1 AND r.url = $3) x
		JOIN ai_samples s ON s.id = x.sample_id JOIN ai_prompts q ON q.id = s.prompt_id
		WHERE s.workspace_id = $1 AND s.brand_id = $2 AND s.status = 'ok' AND s.sampled_at >= $4
		GROUP BY 1, 2 ORDER BY 3 DESC, 2 LIMIT 20`, ws, brand, page, from)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var q aiVisPageQuestion
	if _, err := pgx.ForEachRow(rows, []any{&q.ID, &q.Text, &q.Answers}, func() error {
		p.Questions = append(p.Questions, q)
		return nil
	}); err != nil {
		s.internal(w, r, err)
		return
	}
	// The competitors the answers citing it named.
	rows, err = s.pool.Query(ctx, `
		SELECT m.name, count(DISTINCT m.sample_id)
		FROM ai_citations c JOIN ai_samples s ON s.id = c.sample_id JOIN ai_mentions m ON m.sample_id = s.id AND m.entity = 'competitor'
		WHERE c.workspace_id = $1 AND s.brand_id = $2 AND c.url = $3 AND s.status = 'ok' AND s.sampled_at >= $4
		GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT 20`, ws, brand, page, from)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var b aiVisPlacementBrand
	if _, err := pgx.ForEachRow(rows, []any{&b.Name, &b.Answers}, func() error {
		p.NamedWith = append(p.NamedWith, b)
		return nil
	}); err != nil {
		s.internal(w, r, err)
		return
	}
	// The site's numbers, from Presence's reads.
	if err := s.pool.QueryRow(ctx, `
		SELECT max(domain_rating), max(url_rating) FILTER (WHERE url = $3), max(domain_traffic)
		FROM coverage_competitor_placements WHERE workspace_id = $1 AND brand_id = $2 AND host = $4`, ws, brand, page, strings.TrimPrefix(strings.ToLower(p.Host), "www.")).
		Scan(&p.DomainRating, &p.URLRating, &p.DomainTraffic); err != nil {
		s.internal(w, r, err)
		return
	}
	// The links to the page, from the latest link read of it.
	target := page
	if norm, ok := pageURL(page); ok {
		target = norm
	}
	var l aiVisPageLinks
	err = s.pool.QueryRow(ctx, `SELECT total_links, referring_domains, created_at FROM link_reads
		WHERE workspace_id = $1 AND target = ANY($2) AND scope = 'page' AND status = 'done' ORDER BY created_at DESC LIMIT 1`, ws, []string{target, page}).
		Scan(&l.TotalLinks, &l.ReferringDomains, &l.ReadAt)
	if err == nil {
		p.Links = &l
	} else if !errors.Is(err, pgx.ErrNoRows) {
		s.internal(w, r, err)
		return
	}
	// What reading the page showed.
	var found []byte
	err = s.pool.QueryRow(ctx, `SELECT status, checked_at, found FROM ai_page_checks WHERE workspace_id = $1 AND brand_id = $2 AND url = $3`, ws, brand, page).
		Scan(&p.Check.Status, &p.Check.CheckedAt, &found)
	if err == nil {
		if err := json.Unmarshal(found, &p.Check.Found); err != nil || p.Check.Found == nil {
			p.Check.Found = []aiVisPageFound{}
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		s.internal(w, r, err)
		return
	}
	var opp string
	err = s.pool.QueryRow(ctx, `SELECT id::text FROM placement_opportunities WHERE workspace_id = $1 AND brand_id = $2 AND url = $3
		ORDER BY state IN ('open', 'picked') DESC, updated_at DESC LIMIT 1`, ws, brand, page).Scan(&opp)
	if err == nil {
		p.Opportunity = &opp
	} else if !errors.Is(err, pgx.ErrNoRows) {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// checkAIVisibilityPage queues a read of one cited page.
func (s *Server) checkAIVisibilityPage(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		URL string `json:"url"`
	}
	if !decode(w, r, &in) {
		return
	}
	page, ok := aiVisPageURL(in.URL)
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "url is the address of a page AI answers cite")
		return
	}
	ctx := r.Context()
	var cited bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ai_citations c JOIN ai_samples s ON s.id = c.sample_id WHERE c.workspace_id = $1 AND s.brand_id = $2 AND c.url = $3)
		OR EXISTS (SELECT 1 FROM ai_pages_read WHERE workspace_id = $1 AND brand_id = $2 AND url = $3)`, a.WorkspaceID, a.Brand, page).Scan(&cited); err != nil {
		s.internal(w, r, err)
		return
	}
	if !cited {
		writeError(w, r, http.StatusNotFound, "not_found", "no answer cited or read this page")
		return
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO ai_page_checks (workspace_id, brand_id, url) VALUES ($1, $2, $3)
			ON CONFLICT (brand_id, url) DO UPDATE SET status = CASE WHEN ai_page_checks.status = 'queued' THEN 'queued' ELSE ai_page_checks.status END, requested_at = now()`,
			a.WorkspaceID, a.Brand, page); err != nil {
			return err
		}
		if err := aivis.EnqueuePageChecks(ctx, tx, a.WorkspaceID, a.Brand, page, page+":"+time.Now().UTC().Format("2006-01-02T15")); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "ai_visibility.page_check_requested", "brand", a.Brand, nil, map[string]any{"brand_id": a.Brand})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

// pageEntity is the brand or a competitor to look for on a page.
type pageEntity struct {
	name, competitor string
	hosts            []string
	names            []*regexp.Regexp
}

func pageWordRe(name string) *regexp.Regexp {
	name = strings.ToLower(strings.TrimSpace(name))
	if len(name) < 2 {
		return nil
	}
	return regexp.MustCompile(`(^|[^\pL\pN])` + regexp.QuoteMeta(name) + `($|[^\pL\pN])`)
}

// pageFinds says how a read page has each entity. A page that didn't load
// says nothing.
func pageFinds(rd *crawl.Read, list []pageEntity) []aiVisPageFound {
	text := strings.ToLower(rd.Title + "\n" + rd.Text)
	out := []aiVisPageFound{}
	for _, e := range list {
		f := aiVisPageFound{Name: e.name, CompetitorID: e.competitor, How: "absent"}
		for _, l := range rd.Links {
			u, err := url.Parse(l.URL)
			if err != nil || !coverageHostIn(u.Hostname(), e.hosts) {
				continue
			}
			// A link in the text says more than one in the navigation.
			if f.How != "link" || f.Anchor == "" && l.Anchor != "" || !l.Boilerplate && f.Nofollow && !l.Nofollow {
				f.How, f.Nofollow, f.Anchor = "link", l.Nofollow, clipText(strings.TrimSpace(l.Anchor), 200)
			}
		}
		if f.How == "absent" {
			for _, h := range e.hosts {
				if h != "" && strings.Contains(text, h) {
					f.How = "mention"
				}
			}
			for _, re := range e.names {
				if re != nil && re.MatchString(text) {
					f.How = "mention"
				}
			}
		}
		// The brand is always listed; a competitor only when the page has it.
		if e.competitor == "" || f.How != "absent" {
			out = append(out, f)
		}
	}
	return out
}

func (s *Server) aiPageChecksJob(ctx context.Context, j jobs.Job) error {
	var req aivis.PageChecksPayload
	if err := json.Unmarshal(j.Payload, &req); err != nil || j.WorkspaceID == nil || !validUUID(req.BrandID) {
		return fmt.Errorf("%w: invalid page checks job", jobs.ErrPermanent)
	}
	ws, brand := *j.WorkspaceID, req.BrandID
	var name, website string
	var domains []string
	var test bool
	err := s.pool.QueryRow(ctx, `SELECT b.name, b.website, b.domains, w.test FROM brands b JOIN workspaces w ON w.id = b.workspace_id WHERE b.workspace_id = $1 AND b.id = $2`, ws, brand).
		Scan(&name, &website, &domains, &test)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	list := []pageEntity{{name: name, hosts: coverageBrandHosts("", website, domains), names: []*regexp.Regexp{pageWordRe(name)}}}
	rows, err := s.pool.Query(ctx, `SELECT id::text, host, COALESCE(name, ''), aliases FROM seo_competitors
		WHERE workspace_id = $1 AND brand_id = $2 AND status = 'tracked' ORDER BY created_at, host LIMIT 300`, ws, brand)
	if err != nil {
		return err
	}
	var id, host, cname string
	var aliases []string
	if _, err := pgx.ForEachRow(rows, []any{&id, &host, &cname, &aliases}, func() error {
		e := pageEntity{name: cname, competitor: id, hosts: []string{strings.TrimPrefix(strings.ToLower(host), "www.")}}
		if e.name == "" {
			e.name = host
		}
		for _, n := range append([]string{cname}, aliases...) {
			if re := pageWordRe(n); re != nil {
				e.names = append(e.names, re)
			}
		}
		list = append(list, e)
		return nil
	}); err != nil {
		return err
	}
	// The pages: the one asked for, or the most cited ones not read lately.
	var pages []string
	if req.URL != "" {
		pages = []string{req.URL}
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT c.url FROM ai_citations c JOIN ai_samples s ON s.id = c.sample_id
			WHERE c.workspace_id = $1 AND s.brand_id = $2 AND c.owner = 'other' AND s.status = 'ok' AND s.sampled_at >= now() - interval '28 days' AND `+aivis.NotAssetSQL("c.host")+`
			  AND NOT EXISTS (SELECT 1 FROM ai_page_checks k WHERE k.workspace_id = $1 AND k.brand_id = $2 AND k.url = c.url AND k.checked_at > $3)
			GROUP BY c.url ORDER BY count(DISTINCT c.sample_id) DESC, c.url LIMIT $4`, ws, brand, time.Now().Add(-pageCheckAgain), pageChecksBatch)
		if err != nil {
			return err
		}
		if pages, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return err
		}
	}
	if len(pages) == 0 {
		return nil
	}
	var fetch crawl.Fetcher
	var wait func(context.Context, time.Duration) error
	if s.crawler != nil {
		fetch, wait = s.crawler.ReadFetcher(test)
	}
	type result struct {
		status, title string
		found         []aiVisPageFound
	}
	results := make([]result, len(pages))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, pg := range pages {
		results[i] = result{status: "unreachable", found: []aiVisPageFound{}}
		u, err := url.Parse(pg)
		if fetch == nil || err != nil || crawl.CheckURL(pg, false) != nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if opted, _ := crawl.OptedOut(ctx, s.pool, u.Hostname()); opted {
				metrics.CoverageCheck("blocked")
				return
			}
			rd, err := crawl.ReadPage(ctx, fetch, wait, pg)
			if err != nil || rd == nil || rd.Status < 200 || rd.Status >= 300 {
				metrics.CoverageCheck("unreachable")
				return
			}
			metrics.CoverageCheck("up")
			results[i] = result{status: "done", title: clipText(rd.Title, 500), found: pageFinds(rd, list)}
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	counts := map[string]int{}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for i, pg := range pages {
			res := results[i]
			body, err := json.Marshal(res.found)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO ai_page_checks (workspace_id, brand_id, url, status, title, found, checked_at) VALUES ($1, $2, $3, $4, $5, $6, now())
				ON CONFLICT (brand_id, url) DO UPDATE SET status = EXCLUDED.status, title = EXCLUDED.title, found = EXCLUDED.found, checked_at = now()`,
				ws, brand, pg, res.status, res.title, body); err != nil {
				return err
			}
			counts[res.status]++
			if res.status == "done" && len(res.found) > 0 {
				switch res.found[0].How {
				case "link":
					counts["brand_linked"]++
				case "mention":
					counts["brand_mentioned"]++
				}
			}
		}
		return recordWithProps(ctx, tx, nil, ws, "ai_visibility.pages_checked", "brand", brand, nil, map[string]any{
			"brand_id": brand, "pages": len(pages), "read": counts["done"], "unreachable": counts["unreachable"],
			"brand_linked": counts["brand_linked"], "brand_mentioned": counts["brand_mentioned"], "requested": req.URL != ""})
	})
}

// clipText cuts s to at most n bytes without splitting a character.
func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
