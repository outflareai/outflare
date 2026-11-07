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

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/rdap"
	"github.com/outflareai/outflare/engine/internal/retry"
	"github.com/outflareai/outflare/engine/internal/sociallinks"
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
	// maxRemovedEvents is how many competitor.page_removed events one look
	// records; the feed counts them all.
	maxRemovedEvents = 50
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
	// Links are the social profiles and review pages the home page links
	// to.
	Links []sociallinks.Link
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
			look.Links = sociallinks.Find(string(resp.Body), raw)
		}
		break
	}
	maps := rb.Sitemaps
	if len(maps) == 0 {
		maps = []string{base + "/sitemap.xml"}
	}
	seen := map[string]bool{}
	// A page listed twice (in two sitemaps, twice in one, or two long URLs
	// that clip to the same one) is one page, and counts once toward the cap.
	listed := map[string]bool{}
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
				if listed[p.URL] {
					continue
				}
				listed[p.URL] = true
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
		// The owner's opt-out and robots.txt are answers, not failures to
		// retry; anything else is tried again soon.
		retryable := code != "crawl_opted_out" && code != "robots_disallowed"
		_, uerr := s.Pool.Exec(ctx, `UPDATE seo_competitors SET checked_at = now(), check_error = $3, updated_at = now(),
			check_failures = CASE WHEN $4 THEN check_failures + 1 ELSE 0 END
			WHERE id = $1 AND workspace_id = $2`, p.CompetitorID, ws, reason, retryable)
		ev.Name = "seo_competitor.check_failed"
		if code == "internal" {
			track.Fail(ctx, s.Log, ev, code, err)
		} else {
			ev.Outcome, ev.ErrorCode = track.Failure, code
			track.Emit(ctx, ev)
		}
		return uerr
	}
	// How old its domain (and the brand's own) is, from the public
	// registration record; it doesn't touch their site.
	if !test {
		s.readRegistered(ctx, ws, p.CompetitorID, siteID, host)
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
			SELECT DISTINCT ON (u) $1, $2, u, m FROM unnest($3::text[], $4::text[]) AS t(u, m)
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
			       checked_at = now(), check_error = NULL, check_failures = 0, updated_at = now()
			WHERE id = $1 AND workspace_id = $2`, p.CompetitorID, ws, look.Title, look.Description); err != nil {
			return err
		}
		// The profiles its home page links to are suggested, for a person
		// to say yes or no to; only confirmed ones are followed.
		if suggested, err := suggestCompetitorLinks(ctx, tx, ws, p.CompetitorID, look.Links); err != nil {
			return err
		} else if suggested > 0 {
			ev.Props["profiles_suggested"] = suggested
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
		// Each page gone from a competitor's sitemaps is a webhook event,
		// at most maxRemovedEvents a look: links to it may be worth asking
		// for instead.
		for _, u := range removed[:min(len(removed), maxRemovedEvents)] {
			if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "competitor.page_removed",
				TargetType: "seo_competitor", TargetID: p.CompetitorID, Data: map[string]any{
					"site_id": siteID, "competitor_id": p.CompetitorID, "url": u, "removed": len(removed)},
				Props: map[string]any{"site_id": siteID, "competitor_id": p.CompetitorID, "removed": len(removed)}}); err != nil {
				return err
			}
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
		if ctx.Err() != nil {
			return err
		}
		// A save that fails is recorded on the competitor and tried again
		// soon, like a site that didn't answer, never a bare job error.
		return fail("internal", "Saving the site's pages failed. It's tried again soon.", err)
	}
	ev.Props["pages"], ev.Props["sitemaps"], ev.Props["baseline"] = len(look.Pages), look.Sitemaps, baseline
	ev.Outcome = track.Success
	track.Emit(ctx, ev)
	return nil
}

// ScheduleGlances queues a look at each tracked competitor not looked at
// for a week.
func (s *Service) ScheduleGlances(ctx context.Context) error {
	// A look that failed for a reason that can pass (the site didn't answer)
	// is tried again soon (retry.After), not a week later.
	rows, err := s.Pool.Query(ctx, `
		SELECT id, workspace_id, check_failures > 0 FROM seo_competitors
		WHERE status = 'tracked' AND (checked_at IS NULL OR checked_at < now() - make_interval(secs => $1)
		   OR (check_failures > 0 AND checked_at < now() - `+retry.SQL("check_failures")+`))
		ORDER BY checked_at NULLS FIRST LIMIT 200`, glanceEvery.Seconds())
	if err != nil {
		return err
	}
	type due struct {
		id, ws string
		retry  bool
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (due, error) {
		var d due
		return d, row.Scan(&d.id, &d.ws, &d.retry)
	})
	if err != nil {
		return err
	}
	now := time.Now()
	for _, d := range list {
		// A retry gets an hourly key, so it isn't held to one look a day.
		at := now.Truncate(24 * time.Hour)
		if d.retry {
			at = now
		}
		if err := StartGlance(ctx, s.Pool, d.ws, d.id, at); err != nil {
			return err
		}
	}
	return nil
}

// suggestCompetitorLinks adds the followable links of a competitor's home
// page to its profiles as suggestions, and reports how many are new.
func suggestCompetitorLinks(ctx context.Context, tx pgx.Tx, ws, competitorID string, found []sociallinks.Link) (int, error) {
	if len(found) == 0 {
		return 0, nil
	}
	var list []sociallinks.CompetitorLink
	if err := tx.QueryRow(ctx, `SELECT links FROM seo_competitors WHERE id = $1 AND workspace_id = $2 FOR UPDATE`, competitorID, ws).Scan(&list); err != nil {
		return 0, err
	}
	list, added := sociallinks.Suggest(list, found)
	if added == 0 {
		return 0, nil
	}
	_, err := tx.Exec(ctx, `UPDATE seo_competitors SET links = $3 WHERE id = $1 AND workspace_id = $2`, competitorID, ws, list)
	return added, err
}

// registeredEvery is how often a domain's registration date is read again:
// it only changes when a domain lapses and is registered anew.
const registeredEvery = 180 * 24 * time.Hour

// registeredRetry is how soon a registry that didn't answer (or rate
// limited the lookup) is asked again.
const registeredRetry = 6 * time.Hour

// registeredPerTick is how many registration dates ScheduleRegistered
// reads a minute, one after another, so the public RDAP service isn't
// flooded when many competitors are tracked at once.
const registeredPerTick = 5

// registeredTables are the tables with a registration date; the names are
// constants, never input.
var registeredTables = []string{"seo_competitors", "seo_sites"}

// readRegistered reads when a competitor's domain, and its SEO site's,
// was registered, if not read in the last half year or due a retry.
func (s *Service) readRegistered(ctx context.Context, ws, competitorID, siteID, host string) {
	if s.RDAP == nil {
		return
	}
	targets := []struct{ table, id string }{{"seo_competitors", competitorID}, {"seo_sites", siteID}}
	for _, t := range targets {
		var h string
		err := s.Pool.QueryRow(ctx, `UPDATE `+t.table+` SET registered_checked_at = now() - $3::interval + $4::interval
			WHERE id = $1 AND workspace_id = $2 AND (registered_checked_at IS NULL OR registered_checked_at < now() - $3::interval)
			RETURNING host`, t.id, ws, registeredEvery.String(), registeredRetry.String()).Scan(&h)
		if err != nil {
			continue // not due, or claimed by another look
		}
		s.saveRegistered(ctx, t.table, ws, t.id, h)
	}
}

// saveRegistered looks one domain up and keeps its date. The row was
// claimed with a retry time first, so a lookup that fails is tried again
// in registeredRetry, not at the next weekly look.
func (s *Service) saveRegistered(ctx context.Context, table, ws, id, host string) {
	at, err := s.RDAP.Registered(ctx, host)
	var on *time.Time
	switch {
	case err == nil:
		on = &at
	case errors.Is(err, rdap.ErrNotFound):
	default:
		s.Log.Info("rdap read failed, retrying later", "table", table, "id", id, "err", err)
		return
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE `+table+` SET registered_on = $3, registered_checked_at = now()
		WHERE id = $1 AND workspace_id = $2`, id, ws, on); err != nil {
		s.Log.Warn("saving registration date", "table", table, "err", err)
	}
}

// ScheduleRegistered reads the registration dates that are due (never
// read, half a year old, or a failed lookup's retry), a few each minute,
// so every tracked competitor gets its domain age without waiting for its
// weekly look.
func (s *Service) ScheduleRegistered(ctx context.Context) error {
	if s.RDAP == nil {
		return nil
	}
	for _, table := range registeredTables {
		live := `status = 'tracked'`
		if table == "seo_sites" {
			live = `status = 'active'`
		}
		rows, err := s.Pool.Query(ctx, `UPDATE `+table+` SET registered_checked_at = now() - $1::interval + $2::interval
			WHERE id IN (SELECT id FROM `+table+` WHERE `+live+`
				AND (registered_checked_at IS NULL OR registered_checked_at < now() - $1::interval)
				ORDER BY registered_checked_at NULLS FIRST LIMIT $3 FOR UPDATE SKIP LOCKED)
			RETURNING id::text, workspace_id::text, host`, registeredEvery.String(), registeredRetry.String(), registeredPerTick)
		if err != nil {
			return err
		}
		type due struct{ id, ws, host string }
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (due, error) {
			var d due
			return d, row.Scan(&d.id, &d.ws, &d.host)
		})
		if err != nil {
			return err
		}
		for _, d := range list {
			if ctx.Err() != nil {
				return nil
			}
			s.saveRegistered(ctx, table, d.ws, d.id, d.host)
		}
	}
	return nil
}
