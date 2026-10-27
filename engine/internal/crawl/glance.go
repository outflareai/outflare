package crawl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// JobGlance takes a light look at a competitor's site.
const JobGlance = "crawl.glance"

const (
	// A glance reads robots.txt, at most glanceSitemaps sitemap files and
	// the home page, never more: competitors get a public look, not a
	// crawl. Their numbers come from the workspace's SEO connections.
	glanceSitemaps = 10
	glancePages    = 5000
	// glanceDelay is the least pause between a glance's requests; a longer
	// Crawl-delay wins.
	glanceDelay = time.Second
	// glanceEvery is how often a tracked competitor is looked at.
	glanceEvery = 167 * time.Hour
	// sampleURLs is how many added or removed pages an event lists.
	sampleURLs = 10
)

// ErrBlocked is a glance robots.txt forbids or could not be read for.
var ErrBlocked = errors.New("robots.txt does not allow OutflareBot")

// Look is what a glance saw.
type Look struct {
	Title       string
	Description string
	Pages       []SitemapPage
	// Sitemaps is how many sitemap files were read.
	Sitemaps int
}

// Glance looks at a site's robots.txt, its sitemaps and its home page,
// pausing between requests and obeying robots.txt for each of them. It
// never follows links or reads other pages.
func Glance(ctx context.Context, f Fetcher, wait func(context.Context, time.Duration) error, host string) (*Look, error) {
	base := "https://" + host
	status, body := readRobots(ctx, f, base+"/robots.txt")
	var rb *Robots
	switch {
	case status >= 200 && status < 300:
		rb = ParseRobots([]byte(body), Token)
	case status >= 400 && status < 500:
		rb = AllowAll()
	default:
		return nil, fmt.Errorf("%w: robots.txt could not be read", ErrBlocked)
	}
	delay := max(glanceDelay, min(rb.Delay, maxCrawlDelay))
	own := func(raw string) (*url.URL, bool) {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
			return nil, false
		}
		h := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
		return u, h == strings.TrimPrefix(host, "www.")
	}
	allowed := func(u *url.URL) bool {
		p := u.EscapedPath()
		if p == "" {
			p = "/"
		}
		if u.RawQuery != "" {
			p += "?" + u.RawQuery
		}
		return rb.Allowed(p)
	}
	look := &Look{}
	home, _ := url.Parse(base + "/")
	if !allowed(home) {
		return nil, ErrBlocked
	}
	// The home page, following up to three redirects on the same site.
	raw := home.String()
	for range 4 {
		if err := wait(ctx, delay); err != nil {
			return nil, err
		}
		resp, err := f.Fetch(ctx, raw)
		if err != nil {
			return nil, err
		}
		if resp.Status >= 300 && resp.Status < 400 {
			u, _ := url.Parse(raw)
			next, ok := Normalize(u, resp.Header.Get("Location"))
			nu, mine := own(next)
			if !ok || !mine || !allowed(nu) {
				break
			}
			raw = next
			continue
		}
		if resp.Status == 200 && readable(resp.ContentType()) {
			u, _ := url.Parse(raw)
			pg := ParsePage(u, resp.Body)
			look.Title, look.Description = clip(pg.Title, 300), clip(pg.Description, 500)
		}
		break
	}
	maps := rb.Sitemaps
	if len(maps) == 0 {
		maps = []string{base + "/sitemap.xml"}
	}
	seen := map[string]bool{}
	for len(maps) > 0 && look.Sitemaps < glanceSitemaps && len(look.Pages) < glancePages {
		m := maps[0]
		maps = maps[1:]
		u, mine := own(m)
		if !mine || seen[m] || !allowed(u) {
			continue
		}
		seen[m] = true
		if err := wait(ctx, delay); err != nil {
			return nil, err
		}
		resp, err := f.Fetch(ctx, m)
		look.Sitemaps++
		if err != nil || resp.Status != 200 {
			continue
		}
		pages, more, _ := ParseSitemapEntries(resp.Body)
		for _, p := range pages {
			if _, mine := own(p.URL); mine && len(look.Pages) < glancePages {
				p.URL = clip(p.URL, 2000)
				p.Lastmod = clip(p.Lastmod, 40)
				look.Pages = append(look.Pages, p)
			}
		}
		maps = append(maps, more...)
	}
	return look, nil
}

type glancePayload struct {
	CompetitorID string `json:"competitor_id"`
}

// StartGlance queues a look at a competitor, at most once an hour (the
// schedule passes a day, so at most once a day).
func StartGlance(ctx context.Context, q jobs.Querier, ws, competitorID string, now time.Time) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobGlance, Payload: glancePayload{CompetitorID: competitorID},
		WorkspaceID: &ws, MaxAttempts: 3, IdempotencyKey: "glance:" + competitorID + ":" + now.UTC().Format("2006-01-02T15")})
	return err
}

// glance is the JobGlance handler: it looks at a tracked competitor and
// records what changed since the last look. The first look is a baseline.
func (s *Service) glance(ctx context.Context, job jobs.Job) error {
	var p glancePayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
	}
	var (
		ws, siteID, host, status, title, desc string
		checked                               *time.Time
		test                                  bool
	)
	err := s.Pool.QueryRow(ctx, `
		SELECT c.workspace_id, c.site_id, c.host, c.status, c.title, c.description, c.checked_at, w.test
		FROM seo_competitors c JOIN workspaces w ON w.id = c.workspace_id WHERE c.id = $1`, p.CompetitorID).
		Scan(&ws, &siteID, &host, &status, &title, &desc, &checked, &test)
	if errors.Is(err, pgx.ErrNoRows) || status != "tracked" {
		return nil
	}
	if err != nil {
		return err
	}
	ev := track.Event{Name: "seo_competitor.checked", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"competitor_id": p.CompetitorID, "site_id": siteID}}
	fail := func(code, reason string, err error) error {
		_, uerr := s.Pool.Exec(ctx, `UPDATE seo_competitors SET checked_at = now(), check_error = $3, updated_at = now()
			WHERE id = $1 AND workspace_id = $2`, p.CompetitorID, ws, reason)
		ev.Name = "seo_competitor.check_failed"
		if code == "internal" {
			track.Fail(ctx, s.Log, ev, code, err)
		} else {
			ev.Outcome, ev.ErrorCode = track.Failure, code
			track.Emit(ctx, ev)
		}
		return uerr
	}
	if out, err := OptedOut(ctx, s.Pool, host); err != nil {
		return err
	} else if out {
		return fail("crawl_opted_out", "The site's owner asked not to be crawled.", nil)
	}
	if !test {
		if err := CheckURL("https://"+host+"/", s.AllowPrivate); err != nil {
			return fail("site_unreachable", "The site can't be reached from here.", nil)
		}
	}
	look, err := Glance(ctx, s.fetcher(test), s.wait, host)
	if errors.Is(err, ErrBlocked) {
		return fail("robots_disallowed", "robots.txt doesn't allow OutflareBot, so only SEO connections' numbers are shown.", nil)
	}
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		return fail("site_unreachable", "The site didn't answer.", nil)
	}
	baseline := checked == nil
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var added, removed []string
		urls := make([]string, 0, len(look.Pages))
		mods := make([]string, 0, len(look.Pages))
		for _, pg := range look.Pages {
			urls = append(urls, pg.URL)
			mods = append(mods, pg.Lastmod)
		}
		rows, err := tx.Query(ctx, `
			INSERT INTO competitor_pages (competitor_id, workspace_id, url, lastmod)
			SELECT $1, $2, u, m FROM unnest($3::text[], $4::text[]) AS t(u, m)
			ON CONFLICT (competitor_id, url) DO UPDATE SET lastmod = EXCLUDED.lastmod, last_seen_at = now(), gone_at = NULL
			RETURNING url, (xmax = 0) AS inserted`, p.CompetitorID, ws, urls, mods)
		if err != nil {
			return err
		}
		for rows.Next() {
			var u string
			var ins bool
			if err := rows.Scan(&u, &ins); err != nil {
				rows.Close()
				return err
			}
			if ins {
				added = append(added, u)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		// Pages a look no longer finds are gone, unless the look read no
		// sitemap at all (a broken sitemap is not a site without pages).
		if len(look.Pages) > 0 {
			rows, err = tx.Query(ctx, `
				UPDATE competitor_pages SET gone_at = now()
				WHERE competitor_id = $1 AND workspace_id = $2 AND gone_at IS NULL AND NOT (url = ANY($3))
				RETURNING url`, p.CompetitorID, ws, urls)
			if err != nil {
				return err
			}
			removed, err = pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE seo_competitors SET title = $3, description = $4,
			       pages = (SELECT count(*) FROM competitor_pages WHERE competitor_id = $1 AND gone_at IS NULL),
			       checked_at = now(), check_error = NULL, updated_at = now()
			WHERE id = $1 AND workspace_id = $2`, p.CompetitorID, ws, look.Title, look.Description); err != nil {
			return err
		}
		if baseline {
			return nil
		}
		event := func(kind string, data map[string]any) error {
			_, err := tx.Exec(ctx, `INSERT INTO competitor_events (workspace_id, site_id, competitor_id, kind, data) VALUES ($1, $2, $3, $4, $5)`,
				ws, siteID, p.CompetitorID, kind, data)
			return err
		}
		pagesEvent := func(kind string, list []string) error {
			if len(list) == 0 {
				return nil
			}
			return event(kind, map[string]any{"count": len(list), "urls": list[:min(len(list), sampleURLs)]})
		}
		if err := pagesEvent("pages_added", added); err != nil {
			return err
		}
		if err := pagesEvent("pages_removed", removed); err != nil {
			return err
		}
		if look.Title != "" && look.Title != title {
			if err := event("title_changed", map[string]any{"before": title, "after": look.Title}); err != nil {
				return err
			}
		}
		if look.Description != "" && look.Description != desc {
			if err := event("description_changed", map[string]any{"before": desc, "after": look.Description}); err != nil {
				return err
			}
		}
		ev.Props["pages_added"], ev.Props["pages_removed"] = len(added), len(removed)
		return nil
	})
	if err != nil {
		return err
	}
	ev.Props["pages"], ev.Props["sitemaps"], ev.Props["baseline"] = len(look.Pages), look.Sitemaps, baseline
	ev.Outcome = track.Success
	track.Emit(ctx, ev)
	return nil
}

// scheduleGlances queues a look at each tracked competitor not looked at
// for a week.
func (s *Service) scheduleGlances(ctx context.Context) error {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, workspace_id FROM seo_competitors
		WHERE status = 'tracked' AND (checked_at IS NULL OR checked_at < now() - make_interval(secs => $1))
		ORDER BY checked_at NULLS FIRST LIMIT 200`, glanceEvery.Seconds())
	if err != nil {
		return err
	}
	type due struct{ id, ws string }
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (due, error) {
		var d due
		return d, row.Scan(&d.id, &d.ws)
	})
	if err != nil {
		return err
	}
	now := time.Now()
	for _, d := range list {
		if err := StartGlance(ctx, s.Pool, d.ws, d.id, now.Truncate(24*time.Hour)); err != nil {
			return err
		}
	}
	return nil
}
