package seointel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/fixes"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Regressions: what got worse for a site since the numbers before. Each
// check compares what the syncs stored, so it costs nothing, and runs
// after every sync that can change it (Search Console, rank checks,
// conversions, crawls). What it finds is a fix on the fixes list and a row
// in seo_regressions; what recovers is confirmed and resolved.

// Regression kinds, which are also fix rules.
const (
	RegressionClicks      = "search_clicks_dropped"
	RegressionRank        = "rank_dropped"
	RegressionDeindexed   = "page_deindexed"
	RegressionConversions = "conversions_dropped"
	RegressionHealth      = "health_dropped"
)

// Thresholds. A page's week is compared with its weekly average over the
// four weeks before.
const (
	minWeeklyClicks      = 20  // clicks a week before, for a drop to count
	clicksDrop           = 0.4 // at least 40% fewer
	minWeeklyConversions = 5
	conversionsDrop      = 0.5
	rankWatchedTop       = 20 // keywords that ranked this well or better
	rankDrop             = 5  // positions lost
	healthDrop           = 10 // health score points lost
	maxRegressions       = 50 // per kind and site
)

var regressionRules = []crawl.Rule{
	{ID: RegressionClicks, Severity: crawl.SevWarning, Category: "regressions", Title: "Search clicks dropped",
		Why: "The page got far fewer clicks from Google this week than its average over the four weeks before, so it lost rankings, its snippet stopped drawing clicks, or demand fell.",
		Fix: "Check the page's queries and positions on the Keywords page: if positions fell, compare the page with the results now above it and with what changed on it recently (title, content, links, a redirect); if positions held, rewrite the title and description to win the click back."},
	{ID: RegressionRank, Severity: crawl.SevWarning, Category: "regressions", Title: "Tracked keyword dropped",
		Why: "A keyword the site ranked well for fell five or more places, or out of the top 100, at the latest check.",
		Fix: "Open the keyword's history to see which pages now outrank the site and whether another of its own pages took over. Make sure the target page still answers the search best, is indexed and is linked from related pages."},
	{ID: RegressionDeindexed, Severity: crawl.SevError, Category: "regressions", Title: "Page dropped out of Google's index",
		Why: "Google's URL Inspection had the page indexed, and now it does not, so it cannot rank at all.",
		Fix: "Read Google's reason on the Keywords page (Indexing). Remove a noindex or a robots.txt block, fix a canonical that points elsewhere or a broken response, then ask Google to index the page again in Search Console."},
	{ID: RegressionConversions, Severity: crawl.SevWarning, Category: "regressions", Title: "Conversions from a page dropped",
		Why: "Visits that started on the page converted half as often or less this week than their weekly average over the four weeks before.",
		Fix: "Check whether visits fell (then it is a traffic problem: see its search clicks) or only conversions did (then something on the page or the step after it broke: a form, a button, a price, a tracking event)."},
	{ID: RegressionHealth, Severity: crawl.SevWarning, Category: "regressions", Title: "Site health dropped",
		Why: "The latest crawl's health score fell ten or more points since the crawl before, so many pages got new errors.",
		Fix: "Open the site audit and sort issues by what is new: a deploy that broke templates, links or redirects usually explains it."},
}

func init() {
	Rules = append(Rules, regressionRules...)
	for _, r := range regressionRules {
		ruleByID[r.ID] = r
	}
}

// Regression is one thing that got worse.
type Regression struct {
	Kind    string
	Subject string
	Before  *float64
	After   *float64
	Detail  string
}

func f64(v float64) *float64 { return &v }

// count writes a weekly number with at most one decimal.
func count(v float64) string { return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64) }

// weekly compares each page's latest 7 days with its weekly average over
// the 28 days before, from a table of daily rows.
func weekly(ctx context.Context, q Querier, sql string, args ...any) ([]Regression, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	var out []Regression
	var page string
	var before, after float64
	_, err = pgx.ForEachRow(rows, []any{&page, &before, &after}, func() error {
		out = append(out, Regression{Subject: page, Before: f64(before), After: f64(after)})
		return nil
	})
	return out, err
}

// ClicksDropped finds pages whose Search Console clicks fell. nil, false
// when the property has under five weeks stored.
func ClicksDropped(ctx context.Context, q Querier, ws, siteHost string, src *Source) ([]Regression, bool, error) {
	if src == nil || src.NewestDay == nil || src.OldestDay == nil {
		return nil, false, nil
	}
	newest, err1 := time.Parse(time.DateOnly, *src.NewestDay)
	oldest, err2 := time.Parse(time.DateOnly, *src.OldestDay)
	if err1 != nil || err2 != nil || oldest.After(newest.AddDate(0, 0, -34)) {
		return nil, false, nil
	}
	list, err := weekly(ctx, q, `
		SELECT page, base, recent FROM (
			SELECT sd.page, COALESCE(sum(sd.clicks) FILTER (WHERE sd.day <= $4::date - 7), 0) / 4.0 AS base,
			       COALESCE(sum(sd.clicks) FILTER (WHERE sd.day > $4::date - 7), 0)::float8 AS recent
			FROM search_daily sd
			WHERE sd.workspace_id = $1 AND sd.integration_id = $2 AND sd.site = $3 AND sd.day BETWEEN $4::date - 34 AND $4::date
			  AND `+PageHostSQL("sd.page")+` = ANY($5)
			GROUP BY sd.page) g
		WHERE base >= $6::float8 AND recent <= base * (1 - $7::float8)
		ORDER BY base - recent DESC, page LIMIT $8`,
		ws, src.IntegrationID, src.Property, newest, PageHosts(siteHost), minWeeklyClicks, clicksDrop, maxRegressions)
	for i := range list {
		r := &list[i]
		r.Kind = RegressionClicks
		r.Detail = fmt.Sprintf("%s search clicks in the week to %s, down from %s a week before", count(*r.After), *src.NewestDay, count(*r.Before))
	}
	return list, true, err
}

// Deindexed finds pages Google had indexed at the inspection before and
// not at the latest one.
func Deindexed(ctx context.Context, q Querier, ws, siteHost string, src *Source) ([]Regression, bool, error) {
	if src == nil {
		return nil, false, nil
	}
	rows, err := q.Query(ctx, `
		SELECT url, coverage FROM url_inspections
		WHERE workspace_id = $1 AND integration_id = $2 AND site = $3 AND `+PageHostSQL("url")+` = ANY($4)
		  AND previous_verdict = 'PASS' AND verdict NOT IN ('PASS', '')
		ORDER BY verdict_changed_at DESC NULLS LAST, url LIMIT $5`, ws, src.IntegrationID, src.Property, PageHosts(siteHost), maxRegressions)
	if err != nil {
		return nil, true, err
	}
	var out []Regression
	var u, coverage string
	_, err = pgx.ForEachRow(rows, []any{&u, &coverage}, func() error {
		d := "Google no longer indexes the page"
		if coverage != "" {
			d += ": " + coverage
		}
		out = append(out, Regression{Kind: RegressionDeindexed, Subject: u, Detail: d})
		return nil
	})
	return out, true, err
}

// RankDropped finds tracked keywords that fell at their latest check.
func RankDropped(ctx context.Context, q Querier, ws, siteID string) ([]Regression, error) {
	rows, err := q.Query(ctx, `
		SELECT t.keyword || ' (' || t.country || ', ' || t.device || ')', p.position, c.position, c.day::text
		FROM tracked_keywords t
		JOIN LATERAL (SELECT position, day FROM rank_observations WHERE tracked_id = t.id ORDER BY day DESC LIMIT 1) c ON true
		JOIN LATERAL (SELECT position FROM rank_observations WHERE tracked_id = t.id AND day < c.day ORDER BY day DESC LIMIT 1) p ON true
		WHERE t.workspace_id = $1 AND t.site_id = $2 AND t.status = 'active'
		  AND p.position IS NOT NULL AND p.position <= $3 AND (c.position IS NULL OR c.position - p.position >= $4)
		ORDER BY COALESCE(c.position, 101) - p.position DESC, 1 LIMIT $5`, ws, siteID, rankWatchedTop, rankDrop, maxRegressions)
	if err != nil {
		return nil, err
	}
	var out []Regression
	var kw, day string
	var before int
	var after *int
	_, err = pgx.ForEachRow(rows, []any{&kw, &before, &after, &day}, func() error {
		r := Regression{Kind: RegressionRank, Subject: kw, Before: f64(float64(before))}
		if after == nil {
			r.Detail = fmt.Sprintf("out of the top 100 on %s, from position %d", day, before)
		} else {
			r.After = f64(float64(*after))
			r.Detail = fmt.Sprintf("position %d on %s, from %d", *after, day, before)
		}
		out = append(out, r)
		return nil
	})
	return out, err
}

// ConversionSource is the analytics connection and site that counts a
// host's conversions by landing page: the one with the most visits to it
// lately.
type ConversionSource struct {
	IntegrationID string     `json:"integration_id"`
	Name          string     `json:"name"`
	Connector     string     `json:"connector"`
	Site          string     `json:"site"`
	Events        []string   `json:"events"`
	NewestDay     *string    `json:"newest_day"`
	OldestDay     *string    `json:"-"`
	LastRunAt     *time.Time `json:"last_run_at"`
	LastError     *string    `json:"last_error"`
}

// ConversionSourceFor finds it; nil when none counted visits to the host.
func ConversionSourceFor(ctx context.Context, q Querier, ws, siteHost string) (*ConversionSource, error) {
	var src ConversionSource
	err := q.QueryRow(ctx, `
		SELECT pc.integration_id::text, i.name, i.connector, pc.site, COALESCE(y.events, '{}'), y.newest_day::text, y.oldest_day::text, y.last_run_at, y.last_error
		FROM page_conversions pc
		JOIN integrations i ON i.id = pc.integration_id AND i.workspace_id = pc.workspace_id AND i.status = 'connected'
		LEFT JOIN page_conversion_syncs y ON y.integration_id = pc.integration_id AND y.site = pc.site
		WHERE pc.workspace_id = $1 AND lower(pc.host) = ANY($2)
		  AND pc.day >= (SELECT max(day) - 35 FROM page_conversions WHERE workspace_id = $1 AND lower(host) = ANY($2))
		GROUP BY 1, 2, 3, 4, 5, 6, 7, 8, 9
		ORDER BY sum(pc.sessions) DESC, 1, 4 LIMIT 1`, ws, PageHosts(siteHost)).Scan(&src.IntegrationID, &src.Name, &src.Connector, &src.Site, &src.Events,
		&src.NewestDay, &src.OldestDay, &src.LastRunAt, &src.LastError)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &src, nil
}

// ConversionsDropped finds landing pages whose conversions fell. false
// when no events count, or under five weeks are stored.
func ConversionsDropped(ctx context.Context, q Querier, ws, siteHost string, src *ConversionSource) ([]Regression, bool, error) {
	if src == nil || len(src.Events) == 0 || src.NewestDay == nil || src.OldestDay == nil {
		return nil, false, nil
	}
	newest, err1 := time.Parse(time.DateOnly, *src.NewestDay)
	oldest, err2 := time.Parse(time.DateOnly, *src.OldestDay)
	if err1 != nil || err2 != nil || oldest.After(newest.AddDate(0, 0, -34)) {
		return nil, false, nil
	}
	list, err := weekly(ctx, q, `
		SELECT url, base, recent FROM (
			SELECT 'https://' || lower(host) || path AS url, COALESCE(sum(conversions) FILTER (WHERE day <= $4::date - 7), 0) / 4.0 AS base,
			       COALESCE(sum(conversions) FILTER (WHERE day > $4::date - 7), 0)::float8 AS recent
			FROM page_conversions
			WHERE workspace_id = $1 AND integration_id = $2 AND site = $3 AND day BETWEEN $4::date - 34 AND $4::date AND lower(host) = ANY($5)
			GROUP BY 1) g
		WHERE base >= $6::float8 AND recent <= base * (1 - $7::float8)
		ORDER BY base - recent DESC, url LIMIT $8`,
		ws, src.IntegrationID, src.Site, newest, PageHosts(siteHost), minWeeklyConversions, conversionsDrop, maxRegressions)
	for i := range list {
		r := &list[i]
		r.Kind = RegressionConversions
		r.Detail = fmt.Sprintf("%s conversions from visits that started here in the week to %s, down from %s a week before", count(*r.After), *src.NewestDay, count(*r.Before))
	}
	return list, true, err
}

// HealthDropped compares a crawl's health score (the latest finished one
// when crawlID is "") with the crawl before it.
func HealthDropped(ctx context.Context, q Querier, ws, siteID, crawlID string) ([]Regression, bool, error) {
	var cur, prev *int
	err := q.QueryRow(ctx, `
		WITH c AS (SELECT id, health_score, created_at FROM crawl_runs
			WHERE workspace_id = $1 AND site_id = $2 AND health_score IS NOT NULL AND (id::text = $3 OR ($3 = '' AND status = 'done'))
			ORDER BY created_at DESC LIMIT 1)
		SELECT c.health_score, (SELECT r.health_score FROM crawl_runs r WHERE r.workspace_id = $1 AND r.site_id = $2 AND r.status = 'done'
			AND r.health_score IS NOT NULL AND r.id <> c.id AND r.created_at < c.created_at ORDER BY r.created_at DESC LIMIT 1)
		FROM c`, ws, siteID, crawlID).Scan(&cur, &prev)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil || cur == nil || prev == nil {
		return nil, err == nil && cur != nil, err
	}
	if *prev-*cur < healthDrop {
		return nil, true, nil
	}
	return []Regression{{Kind: RegressionHealth, Before: f64(float64(*prev)), After: f64(float64(*cur)),
		Detail: fmt.Sprintf("health score %d at the latest crawl, from %d at the crawl before", *cur, *prev)}}, true, nil
}

// RegressionChanges is what a check changed.
type RegressionChanges struct {
	fixes.Changes
	Found, Resolved int
}

// RefreshRegressions checks a site for regressions in tx: it records new
// ones, updates the ones still there and resolves the ones that recovered,
// and keeps their fixes in step. Kinds it cannot check (no Search Console,
// no conversion events, a single crawl) are left as they are. crawlID is
// the crawl being audited, or "".
func RefreshRegressions(ctx context.Context, tx pgx.Tx, ws, siteID, crawlID string) (RegressionChanges, error) {
	var ch RegressionChanges
	var brand *string
	var host string
	if err := tx.QueryRow(ctx, `SELECT brand_id::text, host FROM seo_sites WHERE id = $1 AND workspace_id = $2`, siteID, ws).Scan(&brand, &host); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ch, nil
		}
		return ch, err
	}
	src, err := SourceFor(ctx, tx, ws, host)
	if err != nil {
		return ch, err
	}
	csrc, err := ConversionSourceFor(ctx, tx, ws, host)
	if err != nil {
		return ch, err
	}
	var all []Regression
	var checked []string
	add := func(list []Regression, ok bool, err error, kind string) error {
		if err != nil {
			return err
		}
		if ok {
			checked = append(checked, kind)
			all = append(all, list...)
		}
		return nil
	}
	l, ok, err := ClicksDropped(ctx, tx, ws, host, src)
	if err := add(l, ok, err, RegressionClicks); err != nil {
		return ch, err
	}
	l, ok, err = Deindexed(ctx, tx, ws, host, src)
	if err := add(l, ok, err, RegressionDeindexed); err != nil {
		return ch, err
	}
	l, err = RankDropped(ctx, tx, ws, siteID)
	if err := add(l, true, err, RegressionRank); err != nil {
		return ch, err
	}
	l, ok, err = ConversionsDropped(ctx, tx, ws, host, csrc)
	if err := add(l, ok, err, RegressionConversions); err != nil {
		return ch, err
	}
	l, ok, err = HealthDropped(ctx, tx, ws, siteID, crawlID)
	if err := add(l, ok, err, RegressionHealth); err != nil {
		return ch, err
	}
	if len(checked) == 0 {
		return ch, nil
	}
	kinds, subjects, details := make([]string, len(all)), make([]string, len(all)), make([]string, len(all))
	befores, afters := make([]*float64, len(all)), make([]*float64, len(all))
	found := make([]fixes.Found, 0, len(all))
	for i, r := range all {
		kinds[i], subjects[i], details[i], befores[i], afters[i] = r.Kind, r.Subject, r.Detail, r.Before, r.After
		group, unit := r.Subject, "pages"
		switch r.Kind {
		case RegressionRank:
			unit = "keywords"
		case RegressionHealth:
			group, unit = host, "sites"
		}
		found = append(found, fixes.Found{Rule: r.Kind, Severity: ruleByID[r.Kind].Severity, Group: group, Count: 1, Unit: unit})
	}
	if err := tx.QueryRow(ctx, `
		WITH f AS (SELECT * FROM unnest($3::text[], $4::text[], $5::float8[], $6::float8[], $7::text[]) AS f(kind, subject, before, after, detail)),
		up AS (
			INSERT INTO seo_regressions (workspace_id, site_id, kind, subject, before, after, detail)
			SELECT $1, $2, kind, subject, before, after, detail FROM f
			ON CONFLICT (site_id, kind, subject) WHERE status = 'open' DO UPDATE SET
				after = EXCLUDED.after, detail = EXCLUDED.detail, last_seen_at = now()
			RETURNING (xmax = 0) AS inserted
		),
		gone AS (
			UPDATE seo_regressions r SET status = 'resolved', resolved_at = now()
			WHERE r.workspace_id = $1 AND r.site_id = $2 AND r.status = 'open' AND r.kind = ANY($8)
			  AND NOT EXISTS (SELECT 1 FROM f WHERE f.kind = r.kind AND f.subject = r.subject)
			RETURNING 1
		)
		SELECT (SELECT count(*) FROM up WHERE inserted)::int, (SELECT count(*) FROM gone)::int`,
		ws, siteID, kinds, subjects, befores, afters, details, checked).Scan(&ch.Found, &ch.Resolved); err != nil {
		return ch, err
	}
	var pass string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&pass); err != nil {
		return ch, err
	}
	fc, err := fixes.Sync(ctx, tx, fixes.Run{Workspace: ws, Source: FixSource, SubjectType: "seo_site", SubjectID: siteID, BrandID: brand,
		RunID: pass, Rules: checked, Reopen: true}, found)
	ch.Changes = fc
	return ch, err
}

// RegressionsForSites checks each site in its own transaction.
func RegressionsForSites(ctx context.Context, pool *pgxpool.Pool, ws string, siteIDs []string) (RegressionChanges, error) {
	var total RegressionChanges
	for _, id := range siteIDs {
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			c, err := RefreshRegressions(ctx, tx, ws, id, "")
			total.Found, total.Resolved = total.Found+c.Found, total.Resolved+c.Resolved
			total.New, total.Reopened, total.Confirmed = total.New+c.New, total.Reopened+c.Reopened, total.Confirmed+c.Confirmed
			return err
		})
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// CheckRegressions checks sites after a sync that is not a crawl or a
// Search Console sync (rank checks, conversions): seo_regressions.checked
// with what it found, or seo_regressions.failed and a log line. A failure
// never fails the sync.
func CheckRegressions(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, ws, trigger string, siteIDs []string) {
	if len(siteIDs) == 0 {
		return
	}
	ev := track.Event{Name: "seo_regressions.checked", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"trigger": trigger, "sites": len(siteIDs)}}
	ch, err := RegressionsForSites(ctx, pool, ws, siteIDs)
	if err != nil {
		ev.Name = "seo_regressions.failed"
		track.Fail(ctx, log, ev, "internal", fmt.Errorf("seo regressions after %s: %w", trigger, err))
		return
	}
	ev.Props["found"], ev.Props["resolved"], ev.Props["fixes_new"], ev.Props["fixes_reopened"], ev.Props["fixes_confirmed"] = ch.Found, ch.Resolved, ch.New, ch.Reopened, ch.Confirmed
	track.Emit(ctx, ev)
}

// SitesOnHosts are the workspace's SEO sites on any of the hosts or their
// www. twins.
func SitesOnHosts(ctx context.Context, q Querier, ws string, hosts []string) ([]string, error) {
	bare := make([]string, 0, len(hosts))
	for _, h := range hosts {
		if b := Bare(h); b != "" {
			bare = append(bare, b)
		}
	}
	rows, err := q.Query(ctx, `SELECT id::text FROM seo_sites WHERE workspace_id = $1 AND regexp_replace(lower(host), '^www\.', '') = ANY($2) ORDER BY id`, ws, bare)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
