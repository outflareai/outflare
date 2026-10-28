package analyticsync

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/indexability"
	"github.com/outflareai/outflare/engine/internal/seointel"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The page by page Search Console sync: clicks and impressions for every
// page and query of every day (search_daily), how Google indexed the
// site's pages (url_inspections) and its sitemaps (search_sitemaps). It
// runs with each Search Console connection's analytics sync, on the same
// sign-in.

const (
	// searchFirstDays are read on a property's first run; later runs
	// re-read searchRecentDays and go searchBackDays further back, until
	// searchKeepDays (16 months, all Search Console keeps). Older rows go.
	searchFirstDays  = 90
	searchRecentDays = 4
	searchBackDays   = 14
	searchKeepDays   = 486
	// searchRowsPerDay caps the page and query rows kept for one day.
	searchRowsPerDay = 5000
	// URL Inspection allows about 2,000 calls a day per property; runs
	// are six hours apart, so each run inspects at most inspectPerRun and
	// a URL is inspected again after reinspectAfter.
	inspectPerRun  = 100
	reinspectAfter = 7 * 24 * time.Hour
	sitemapsEvery  = 24 * time.Hour
)

// SearchResult is what one property's page by page run did.
type SearchResult struct {
	Days      int `json:"days"`
	Rows      int `json:"rows"`
	Inspected int `json:"inspected"`
	Sitemaps  int `json:"sitemaps"`
}

type searchState struct {
	oldest, newest *time.Time
	backfilled     bool
	sitemapsAt     *time.Time
}

// searchDays are the days a run reads, newest first.
func searchDays(st searchState, yesterday time.Time) []time.Time {
	var out []time.Time
	if st.oldest == nil {
		for i := range searchFirstDays {
			out = append(out, yesterday.AddDate(0, 0, -i))
		}
		return out
	}
	for i := range searchRecentDays {
		out = append(out, yesterday.AddDate(0, 0, -i))
	}
	floor := yesterday.AddDate(0, 0, -(searchKeepDays - 1))
	if !st.backfilled {
		for i := 1; i <= searchBackDays; i++ {
			d := st.oldest.AddDate(0, 0, -i)
			if d.Before(floor) {
				break
			}
			out = append(out, d)
		}
	}
	return out
}

// syncSearch runs the page by page sync of one Search Console property.
func (s *Syncer) syncSearch(ctx context.Context, o *connectors.Opened, r connectors.SearchConsoleReader, p SitePlan, res *Result) (SearchResult, error) {
	var out SearchResult
	ws, id, site, host := o.WorkspaceID, o.ID, p.ID, p.Host
	var st searchState
	err := s.Pool.QueryRow(ctx, `SELECT oldest_day, newest_day, backfilled_at IS NOT NULL, sitemaps_at FROM search_syncs WHERE integration_id = $1 AND site = $2`, id, site).
		Scan(&st.oldest, &st.newest, &st.backfilled, &st.sitemapsAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, dbError{err}
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE search_daily SET host = $3 WHERE integration_id = $1 AND site = $2 AND host <> $3`, id, site, host); err != nil {
		return out, dbError{err}
	}
	today := s.now().UTC().Truncate(24 * time.Hour)
	yesterday := today.AddDate(0, 0, -1)
	floor := yesterday.AddDate(0, 0, -(searchKeepDays - 1))
	var runErr error
	oldest, newest := st.oldest, st.newest
	for _, d := range searchDays(st, yesterday) {
		res.Calls++
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		rows, err := r.SearchRows(cctx, o.Client, o.Creds, site, day(d), searchRowsPerDay)
		cancel()
		if err != nil {
			runErr = err
			break
		}
		n, err := s.storeSearchDay(ctx, ws, id, site, host, d, rows)
		if err != nil {
			return out, err
		}
		out.Days++
		out.Rows += n
		res.Rows += n
		if oldest == nil || d.Before(*oldest) {
			dd := d
			oldest = &dd
		}
		if newest == nil || d.After(*newest) {
			dd := d
			newest = &dd
		}
	}
	backfilled := st.backfilled || (oldest != nil && !oldest.After(floor))
	if _, err := s.Pool.Exec(ctx, `DELETE FROM search_daily WHERE integration_id = $1 AND site = $2 AND day < $3`, id, site, floor); err != nil {
		return out, dbError{err}
	}
	sitemapsAt := st.sitemapsAt
	if runErr == nil && (sitemapsAt == nil || s.now().Sub(*sitemapsAt) >= sitemapsEvery) {
		res.Calls++
		n, err := s.syncSitemaps(ctx, o, r, ws, id, site, host)
		if err != nil {
			var db dbError
			if errors.As(err, &db) {
				return out, err
			}
			runErr = err
		} else {
			now := s.now()
			sitemapsAt, out.Sitemaps = &now, n
		}
	}
	if runErr == nil {
		n, err := s.inspect(ctx, o, r, ws, id, site, host, res)
		out.Inspected = n
		if err != nil {
			var db dbError
			if errors.As(err, &db) {
				return out, err
			}
			runErr = err
		}
	}
	var msg *string
	if runErr != nil {
		m := runErr.Error()
		msg = &m
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO search_syncs (integration_id, site, workspace_id, host, oldest_day, newest_day, backfilled_at, sitemaps_at, inspected_at, last_run_at, last_error)
		VALUES ($1, $2, $3, $4, $5, $6, CASE WHEN $7 THEN now() END, $8, CASE WHEN $9 > 0 THEN now() END, now(), $10)
		ON CONFLICT (integration_id, site) DO UPDATE SET host = EXCLUDED.host, oldest_day = EXCLUDED.oldest_day, newest_day = EXCLUDED.newest_day,
			backfilled_at = COALESCE(search_syncs.backfilled_at, EXCLUDED.backfilled_at), sitemaps_at = EXCLUDED.sitemaps_at,
			inspected_at = COALESCE(EXCLUDED.inspected_at, search_syncs.inspected_at), last_run_at = now(), last_error = EXCLUDED.last_error`,
		id, site, ws, host, oldest, newest, backfilled, sitemapsAt, out.Inspected, msg)
	if err != nil {
		return out, dbError{err}
	}
	ev := track.Event{Name: "search_console.synced", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"integration_id": id, "days": out.Days, "rows": out.Rows, "inspected": out.Inspected, "sitemaps": out.Sitemaps, "backfilled": backfilled}}
	// The indexability report of the property's SEO sites takes today's
	// numbers once a day.
	if out.Days > 0 || out.Inspected > 0 {
		if err := s.queueIndexability(ctx, ws, host, today); err != nil {
			return out, dbError{err}
		}
	}
	// New days can change which pages compete for a search: check the
	// property's SEO sites again. A failure here leaves the sync done.
	if out.Days > 0 {
		ch, err := seointel.RefreshSearch(ctx, s.Pool, ws, host)
		if err != nil {
			track.Fail(ctx, s.Log, track.Event{Name: "search_insights.failed", WorkspaceID: ws, ActorType: "system", Surface: "job",
				Props: map[string]any{"integration_id": id}}, "internal", fmt.Errorf("search insights %s: %w", id, err))
		} else {
			ev.Props["fixes_new"], ev.Props["fixes_reopened"], ev.Props["fixes_confirmed"] = ch.New, ch.Reopened, ch.Confirmed
		}
	}
	if runErr != nil {
		ev.Outcome = track.Failure
		if connectors.KindOf(runErr) == connectors.ErrUnavailable {
			ev.Outcome = track.Error
		}
		track.Fail(ctx, s.Log, ev, "connector_"+connectors.KindOf(runErr), fmt.Errorf("search console %s: %w", id, runErr))
	} else {
		track.Emit(ctx, ev)
	}
	return out, runErr
}

// storeSearchDay replaces one day's rows of a property.
func (s *Syncer) storeSearchDay(ctx context.Context, ws, id, site, host string, d time.Time, rows []connectors.SearchRow) (int, error) {
	type key struct{ page, query string }
	seen := make(map[key]bool, len(rows))
	copyRows := make([][]any, 0, len(rows))
	for _, x := range rows {
		k := key{truncate(x.Page, 1000), truncate(x.Query, 500)}
		if k.page == "" || seen[k] {
			continue
		}
		seen[k] = true
		copyRows = append(copyRows, []any{ws, id, site, host, d, k.page, k.query, int(x.Clicks), int(x.Impressions), x.CTR, x.Position})
	}
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM search_daily WHERE integration_id = $1 AND site = $2 AND day = $3`, id, site, d); err != nil {
			return err
		}
		_, err := tx.CopyFrom(ctx, pgx.Identifier{"search_daily"},
			[]string{"workspace_id", "integration_id", "site", "host", "day", "page", "query", "clicks", "impressions", "ctr", "position"},
			pgx.CopyFromRows(copyRows))
		return err
	})
	if err != nil {
		return 0, dbError{err}
	}
	return len(copyRows), nil
}

func (s *Syncer) syncSitemaps(ctx context.Context, o *connectors.Opened, r connectors.SearchConsoleReader, ws, id, site, host string) (int, error) {
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	list, err := r.Sitemaps(cctx, o.Client, o.Creds, site)
	cancel()
	if err != nil {
		return 0, err
	}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM search_sitemaps WHERE integration_id = $1 AND site = $2`, id, site); err != nil {
			return err
		}
		for _, m := range list {
			if m.Path == "" {
				continue
			}
			_, err := tx.Exec(ctx, `
				INSERT INTO search_sitemaps (workspace_id, integration_id, site, host, path, last_submitted, last_read, pending, is_index, errors, warnings, submitted, indexed)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) ON CONFLICT DO NOTHING`,
				ws, id, site, host, truncate(m.Path, 1000), parseTime(m.LastSubmitted), parseTime(m.LastRead), m.Pending, m.Index,
				m.Errors, m.Warnings, m.Submitted, m.Indexed)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, dbError{err}
	}
	return len(list), nil
}

// inspect asks how Google indexed the pages that matter most and were
// not inspected lately: pages OutflareBot crawled on the property's
// SEO sites and pages Google showed, those never inspected first, then by
// impressions.
func (s *Syncer) inspect(ctx context.Context, o *connectors.Opened, r connectors.SearchConsoleReader, ws, id, site, host string, res *Result) (int, error) {
	if host == "" {
		return 0, nil
	}
	rows, err := s.Pool.Query(ctx, `
		WITH crawled AS (
			SELECT DISTINCT p.url FROM seo_sites ss
			JOIN LATERAL (SELECT r.id FROM crawl_runs r WHERE r.site_id = ss.id AND r.status = 'done' ORDER BY r.created_at DESC LIMIT 1) last ON true
			JOIN crawl_pages p ON p.run_id = last.id AND p.status = 'fetched' AND p.http_status = 200 AND NOT p.noindex
			WHERE ss.workspace_id = $1 AND regexp_replace(lower(ss.host), '^www\.', '') = $3
		), shown AS (
			SELECT page AS url, sum(impressions) AS imp FROM search_daily
			WHERE integration_id = $2 AND site = $4 AND day > current_date - 28 GROUP BY page
		), urls AS (
			SELECT COALESCE(c.url, sh.url) AS url, COALESCE(sh.imp, 0) AS imp FROM crawled c FULL JOIN shown sh ON sh.url = c.url
		)
		SELECT u.url FROM urls u
		LEFT JOIN url_inspections i ON i.integration_id = $2 AND i.site = $4 AND i.url = u.url
		WHERE i.url IS NULL OR i.inspected_at < now() - make_interval(secs => $5)
		ORDER BY (i.url IS NULL) DESC, u.imp DESC, u.url
		LIMIT $6`, ws, id, host, site, reinspectAfter.Seconds(), inspectPerRun*2)
	if err != nil {
		return 0, dbError{err}
	}
	urls, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, dbError{err}
	}
	n := 0
	for _, u := range urls {
		if n >= inspectPerRun {
			break
		}
		if !inProperty(site, u) {
			continue
		}
		res.Calls++
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		ins, err := r.InspectURL(cctx, o.Client, o.Creds, site, u)
		cancel()
		if err != nil {
			if connectors.KindOf(err) == connectors.ErrInvalid {
				continue // a URL outside the property; the next may do
			}
			return n, err
		}
		_, err = s.Pool.Exec(ctx, `
			INSERT INTO url_inspections (workspace_id, integration_id, site, host, url, verdict, coverage, indexing, robots_txt, page_fetch,
				google_canonical, user_canonical, last_crawl, crawled_as, sitemaps, mobile, rich_results, link, inspected_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, now())
			ON CONFLICT (integration_id, site, url) DO UPDATE SET host = EXCLUDED.host,
				previous_verdict = url_inspections.verdict,
				verdict_changed_at = CASE WHEN url_inspections.verdict <> EXCLUDED.verdict THEN now() ELSE url_inspections.verdict_changed_at END,
				verdict = EXCLUDED.verdict, coverage = EXCLUDED.coverage, indexing = EXCLUDED.indexing, robots_txt = EXCLUDED.robots_txt,
				page_fetch = EXCLUDED.page_fetch, google_canonical = EXCLUDED.google_canonical, user_canonical = EXCLUDED.user_canonical,
				last_crawl = EXCLUDED.last_crawl, crawled_as = EXCLUDED.crawled_as, sitemaps = EXCLUDED.sitemaps, mobile = EXCLUDED.mobile,
				rich_results = EXCLUDED.rich_results, link = EXCLUDED.link, inspected_at = now()`,
			ws, id, site, host, truncate(u, 1000), ins.Verdict, truncate(ins.Coverage, 200), ins.Indexing, ins.RobotsTxt, ins.PageFetch,
			truncate(ins.GoogleCanonical, 1000), truncate(ins.UserCanonical, 1000), parseTime(ins.LastCrawl), ins.CrawledAs, ins.Sitemaps,
			ins.Mobile, ins.RichResults, truncate(ins.Link, 1000))
		if err != nil {
			return n, dbError{err}
		}
		n++
	}
	return n, nil
}

// inProperty reports whether a URL belongs to a Search Console property:
// a domain property covers the domain and its subdomains, a URL-prefix
// property the URLs under it.
func inProperty(site, raw string) bool {
	if d, ok := strings.CutPrefix(site, "sc-domain:"); ok {
		u, err := url.Parse(raw)
		if err != nil {
			return false
		}
		h := strings.ToLower(u.Hostname())
		d = strings.ToLower(d)
		return h == d || strings.HasSuffix(h, "."+d)
	}
	return strings.HasPrefix(raw, site)
}

func parseTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, time.DateOnly} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}

// queueIndexability queues today's indexability snapshot of each SEO site
// the property covers: the host and, for a domain property, its subdomains.
func (s *Syncer) queueIndexability(ctx context.Context, ws, host string, today time.Time) error {
	rows, err := s.Pool.Query(ctx, `
		SELECT id::text FROM (SELECT id, regexp_replace(split_part(lower(host), ':', 1), '^www\.', '') AS bare FROM seo_sites WHERE workspace_id = $1) x
		WHERE bare = $2 OR bare LIKE '%.' || $2`, ws, host)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := indexability.Enqueue(ctx, s.Pool, ws, id, "search:"+day(today)); err != nil {
			return err
		}
	}
	return nil
}
