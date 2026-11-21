package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/go-sourcemap/sourcemap"
	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// Error tracking. A brand's website script (with the site key's "Capture
// errors" switch, or outflare.captureException) and its servers send each
// error as a track event, Error Caught, with type, message, stack and a
// fingerprint. Nothing else is stored: an issue is the events that share a
// fingerprint, and error_issues only remembers what a person decided about
// it (resolved, ignored or archived). Deleting an issue deletes its
// events.

func (s *Server) routesSiteErrors(mux *http.ServeMux) {
	read, manage := auth.ScopeAnalyticsRead, auth.ScopeAnalyticsManage
	mux.HandleFunc("GET /v1/brands/{brand}/errors", s.onBrand(read, s.listErrorIssues))
	mux.HandleFunc("GET /v1/brands/{brand}/errors/{fingerprint}", s.onBrand(read, s.getErrorIssue))
	mux.HandleFunc("PATCH /v1/brands/{brand}/errors/{fingerprint}", s.onBrand(manage, s.updateErrorIssue))
	mux.HandleFunc("DELETE /v1/brands/{brand}/errors/{fingerprint}", s.onBrand(manage, s.deleteErrorIssue))
	mux.HandleFunc("POST /v1/brands/{brand}/errors/bulk", s.onBrand(manage, s.bulkErrorIssues))
	mux.HandleFunc("POST /v1/brands/{brand}/errors/merge", s.onBrand(manage, s.mergeErrorIssues))
	mux.HandleFunc("GET /v1/brands/{brand}/errors/{fingerprint}/occurrences", s.onBrand(read, s.listErrorOccurrences))
	mux.HandleFunc("GET /v1/brands/{brand}/errors/{fingerprint}/occurrences/{id}/before", s.onBrand(read, s.errorBefore))
	mux.HandleFunc("DELETE /v1/brands/{brand}/errors/{fingerprint}/merged/{merged}", s.onBrand(manage, s.splitErrorIssue))
}

// errorStatuses are what a person can decide about an issue: open again,
// resolved (fixed: back again if it happens again), ignored (stays out of
// the way and never alerts) or archived (out of the way until it happens
// again, when it opens again without counting as back).
var errorStatuses = []string{"open", "resolved", "ignored", "archived"}

// errorEvent is the catalog name of Error Caught.
const errorEvent = "error_caught"

// errorHistory is how far back an issue's first sighting is looked for.
const errorHistory = 90 * 24 * time.Hour

var errorFingerprintRe = regexp.MustCompile(`^[a-z0-9]{1,64}$`)

type errorIssue struct {
	Fingerprint string `json:"fingerprint"`
	Type        string `json:"type"`
	Message     string `json:"message"`
	// The full message of a minified React or Redux error, or "".
	MessageFull string `json:"message_full"`
	// open, resolved, ignored or archived. A resolved or archived issue
	// seen again since is open.
	Status string `json:"status"`
	// When a person last set the status, and who; null for an issue
	// nobody touched.
	StatusAt *time.Time `json:"status_at"`
	StatusBy *string    `json:"status_by"`
	// Resolved once, and seen again since.
	Regressed bool `json:"regressed"`
	// First seen inside the period, when capture ran before it.
	New       bool      `json:"new"`
	Count     int       `json:"count"`
	Visitors  int       `json:"visitors"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	LastPath  string    `json:"last_path"`
	// The site (host) of the latest error, and how many sites it
	// happened on in the period.
	Host  string `json:"host"`
	Hosts int    `json:"hosts"`
	// Where the latest error came from: browser (the website) or server.
	Source string `json:"source"`
	// Whether the latest error was caught by the site's own code.
	Handled bool `json:"handled"`
	// How many different pages it happened on in the period.
	Pages int `json:"pages"`
	// The release (version or commit) of its first and latest error that
	// carried one, in the last 90 days; "" when none did.
	FirstRelease string `json:"first_release"`
	LastRelease  string `json:"last_release"`
	// Why every error of it in the period is filtered: a built-in filter
	// (extension, link_scanner, injected, local_file, ip_host) or rule;
	// "" when it isn't.
	Filtered string `json:"filtered"`
	// The share of the brand's site visitors it happened to in the
	// period (0 to 1), and the page where the share was highest; null
	// when the site has no visits to compare with.
	VisitorShare *float64        `json:"visitor_share"`
	WorstPage    *errorPageShare `json:"worst_page"`
	// Errors per day of the period, oldest first.
	Trend []int `json:"trend"`
}

// errorPageShare is how many of a page's visitors an error happened to.
type errorPageShare struct {
	Path string `json:"path"`
	// Visitors it happened to on the page, and visitors who viewed it.
	Visitors     int     `json:"visitors"`
	PageVisitors int     `json:"page_visitors"`
	Share        float64 `json:"share"`
}

type errorDecision struct {
	status string
	at     time.Time
	by     *string
}

func (s *Server) errorDecisions(ctx context.Context, ws, brand string) (map[string]errorDecision, error) {
	rows, err := s.pool.Query(ctx, `SELECT i.fingerprint, i.status, i.status_at, a.display_name FROM error_issues i LEFT JOIN actors a ON a.id = i.updated_by
		WHERE i.workspace_id = $1 AND i.brand_id = $2`, ws, brand)
	if err != nil {
		return nil, err
	}
	out := map[string]errorDecision{}
	var fp string
	var d errorDecision
	_, err = pgx.ForEachRow(rows, []any{&fp, &d.status, &d.at, &d.by}, func() error { out[fp] = d; return nil })
	return out, err
}

// decide sets an issue's status from what a person decided about it.
func (i *errorIssue) decide(d errorDecision, known bool) {
	i.Status = "open"
	if !known {
		return
	}
	at := d.at
	i.StatusAt, i.StatusBy = &at, d.by
	switch {
	case d.status == "resolved" && i.LastSeen.After(d.at):
		i.Regressed = true
	case d.status == "archived" && i.LastSeen.After(d.at):
	default:
		i.Status = d.status
	}
}

// errorDays are the days of a period, as errors per day are keyed.
func errorDays(from, now time.Time) []string {
	var out []string
	for t := from; !t.After(now); t = t.AddDate(0, 0, 1) {
		out = append(out, t.Format(time.DateOnly))
	}
	return out
}

// siteVisitors is how many visitors the brand's site had since from, on
// host when it isn't "".
func (s *Server) siteVisitors(ctx context.Context, ws, brand string, from time.Time, host string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(DISTINCT anonymous_id) FROM site_sessions WHERE workspace_id = $1 AND brand_id = $2 AND last_seen_at >= $3
		AND ($4 = '' OR `+siteSessionHost+` = $4)`, ws, brand, from, host).Scan(&n)
	return n, err
}

// pageVisitors is how many visitors viewed each of paths since from, and
// how many times: [visitors, views].
func (s *Server) pageVisitors(ctx context.Context, ws, brand string, from time.Time, host string, paths []string) (map[string][2]int, error) {
	out := map[string][2]int{}
	if len(paths) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT context->'page'->>'path', count(DISTINCT anonymous_id), count(*) FROM customer_events
		WHERE workspace_id = $1 AND brand_id = $2 AND name = 'page.viewed' AND occurred_at >= $3 AND context->'page'->>'path' = ANY($4)
			AND ($5 = '' OR `+sitePageHost+` = $5)
		GROUP BY 1`, ws, brand, from, paths, host)
	if err != nil {
		return nil, err
	}
	var p string
	var n, views int
	_, err = pgx.ForEachRow(rows, []any{&p, &n, &views}, func() error { out[p] = [2]int{n, views}; return nil })
	return out, err
}

// errorSetup is what the brand's website errors in the period carry, so
// the page can say what is still missing to make them easy to fix.
type errorSetup struct {
	// Source maps the brand has uploaded.
	SourceMaps int `json:"source_maps"`
	// Errors from the website in the period (filtered ones left out), and
	// how many of them came with a release and with React's components.
	BrowserErrors      int `json:"browser_errors"`
	WithRelease        int `json:"with_release"`
	WithComponentStack int `json:"with_component_stack"`
}

// errorBuckets are the tabs of the list: a status, or filtered.
var errorBuckets = []string{"open", "resolved", "ignored", "archived", "filtered"}

func (s *Server) listErrorIssues(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	days, ok := intParam(w, r, "days", 7, 1, 90)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "open"
	}
	if status != "all" && !slices.Contains(errorBuckets, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is open, resolved, ignored, archived, filtered or all")
		return
	}
	host := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("host")))
	if len(host) > 253 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "host is a site's host name, such as app.example.com")
		return
	}
	now := time.Now().UTC()
	from := now.AddDate(0, 0, -days)
	decided, err := s.errorDecisions(ctx, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// One pass over the brand's errors: each with its issue, filter and
	// site worked out once.
	const errs = `WITH e AS (
			SELECT ` + errorFingerprint + ` AS fp, occurred_at, anonymous_id, properties, context, source,
				` + errorFilter + ` AS filtered, ` + errorHost + ` AS host, coalesce(context->'page'->>'path', '') AS path
			FROM customer_events WHERE workspace_id = $1 AND brand_id = $2 AND name = $3 AND occurred_at >= $4),
		f AS (SELECT * FROM e WHERE $6 = '' OR host = $6) `
	args := []any{a.WorkspaceID, a.Brand, errorEvent, now.Add(-errorHistory), from, host}
	rows, err := s.pool.Query(ctx, errs+`
		SELECT fp, count(*) FILTER (WHERE occurred_at >= $5), count(DISTINCT anonymous_id) FILTER (WHERE occurred_at >= $5),
			min(occurred_at), max(occurred_at),
			coalesce((array_agg(left(properties->>'type', 200) ORDER BY occurred_at DESC))[1], ''),
			coalesce((array_agg(left(properties->>'message', 500) ORDER BY occurred_at DESC))[1], ''),
			coalesce((array_agg(left(properties->>'message_full', 1000) ORDER BY occurred_at DESC))[1], ''),
			coalesce((array_agg(left(path, 500) ORDER BY occurred_at DESC))[1], ''),
			coalesce((array_agg(host ORDER BY occurred_at DESC))[1], ''),
			count(DISTINCT host) FILTER (WHERE occurred_at >= $5 AND host <> ''),
			coalesce((array_agg(source ORDER BY occurred_at DESC))[1], ''),
			coalesce((array_agg(properties->>'handled' ORDER BY occurred_at DESC))[1], '') = 'true',
			count(DISTINCT path) FILTER (WHERE occurred_at >= $5),
			coalesce((array_agg(left(properties->>'release', 100) ORDER BY occurred_at) FILTER (WHERE coalesce(properties->>'release', '') <> ''))[1], ''),
			coalesce((array_agg(left(properties->>'release', 100) ORDER BY occurred_at DESC) FILTER (WHERE coalesce(properties->>'release', '') <> ''))[1], ''),
			CASE WHEN bool_and(filtered <> '') FILTER (WHERE occurred_at >= $5) THEN (array_agg(filtered ORDER BY occurred_at DESC))[1] ELSE '' END
		FROM f GROUP BY fp HAVING count(*) FILTER (WHERE occurred_at >= $5) > 0
		ORDER BY 2 DESC, 5 DESC LIMIT 1000`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list := []errorIssue{}
	counts := map[string]int{"open": 0, "resolved": 0, "ignored": 0, "archived": 0, "filtered": 0}
	total := 0
	var i errorIssue
	_, err = pgx.ForEachRow(rows, []any{&i.Fingerprint, &i.Count, &i.Visitors, &i.FirstSeen, &i.LastSeen, &i.Type, &i.Message, &i.MessageFull, &i.LastPath,
		&i.Host, &i.Hosts, &i.Source, &i.Handled, &i.Pages, &i.FirstRelease, &i.LastRelease, &i.Filtered}, func() error {
		v := i
		d, known := decided[v.Fingerprint]
		v.decide(d, known)
		v.New = !v.FirstSeen.Before(from)
		bucket := v.Status
		if v.Filtered != "" {
			bucket = "filtered"
		}
		counts[bucket]++
		if status == "all" || status == bucket {
			total += v.Count
			if len(list) < 200 {
				list = append(list, v)
			}
		}
		return nil
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// When capture began: before then nothing can be told apart as new.
	var since *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT min(occurred_at) FROM customer_events WHERE workspace_id = $1 AND brand_id = $2 AND name = $3 AND occurred_at >= $4`,
		a.WorkspaceID, a.Brand, errorEvent, now.Add(-errorHistory)).Scan(&since); err != nil {
		s.internal(w, r, err)
		return
	}
	if since != nil && !since.Before(from) {
		for k := range list {
			list[k].New = false
		}
	}
	if err := s.errorRates(ctx, errs, args, a.WorkspaceID, a.Brand, from, now, host, list); err != nil {
		s.internal(w, r, err)
		return
	}
	hosts, err := s.errorHosts(ctx, a.WorkspaceID, a.Brand, from)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// The site's visitors in the period, how many of them any error that
	// isn't filtered happened to, and what the errors carry.
	visitors, err := s.siteVisitors(ctx, a.WorkspaceID, a.Brand, from, host)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var affected int
	var setup errorSetup
	if err := s.pool.QueryRow(ctx, errs+`SELECT count(DISTINCT anonymous_id), count(*) FILTER (WHERE source = 'browser'),
			count(*) FILTER (WHERE source = 'browser' AND coalesce(properties->>'release', '') <> ''),
			count(*) FILTER (WHERE source = 'browser' AND coalesce(properties->>'component_stack', '') <> '')
		FROM f WHERE occurred_at >= $5 AND filtered = ''`, args...).Scan(&affected, &setup.BrowserErrors, &setup.WithRelease, &setup.WithComponentStack); err != nil {
		s.internal(w, r, err)
		return
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM source_maps WHERE workspace_id = $1 AND brand_id = $2`, a.WorkspaceID, a.Brand).Scan(&setup.SourceMaps); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": now, "days": days, "errors": total, "counts": counts, "capture_since": since,
		"hosts": hosts, "visitors": visitors, "affected_visitors": affected, "setup": setup, "data": list})
}

// errorRates fills in the listed issues' trend, visitor share and worst
// page.
func (s *Server) errorRates(ctx context.Context, errs string, args []any, ws, brand string, from, now time.Time, host string, list []errorIssue) error {
	days := errorDays(from, now)
	at := map[string]int{}
	for k, d := range days {
		at[d] = k
	}
	idx := map[string]int{}
	fps := make([]string, len(list))
	for k := range list {
		idx[list[k].Fingerprint] = k
		fps[k] = list[k].Fingerprint
		list[k].Trend = make([]int, len(days))
	}
	if len(list) == 0 {
		return nil
	}
	rows, err := s.pool.Query(ctx, errs+`SELECT fp, to_char(date_trunc('day', occurred_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD'), count(*)
		FROM f WHERE occurred_at >= $5 AND fp = ANY($7) GROUP BY 1, 2`, append(args, fps)...)
	if err != nil {
		return err
	}
	var fp, day, path string
	var n int
	if _, err := pgx.ForEachRow(rows, []any{&fp, &day, &n}, func() error {
		if k, ok := idx[fp]; ok {
			if d, ok := at[day]; ok {
				list[k].Trend[d] = n
			}
		}
		return nil
	}); err != nil {
		return err
	}
	visitors, err := s.siteVisitors(ctx, ws, brand, from, host)
	if err != nil || visitors == 0 {
		return err
	}
	// Visitors each issue happened to on each page.
	rows, err = s.pool.Query(ctx, errs+`SELECT fp, path, count(DISTINCT anonymous_id) FROM f
		WHERE occurred_at >= $5 AND fp = ANY($7) AND path <> '' AND anonymous_id IS NOT NULL GROUP BY 1, 2`, append(args, fps)...)
	if err != nil {
		return err
	}
	type hit struct {
		fp, path string
		n        int
	}
	var hits []hit
	paths := map[string]bool{}
	if _, err := pgx.ForEachRow(rows, []any{&fp, &path, &n}, func() error {
		hits = append(hits, hit{fp, path, n})
		paths[path] = true
		return nil
	}); err != nil {
		return err
	}
	keys := make([]string, 0, len(paths))
	for p := range paths {
		keys = append(keys, p)
	}
	slices.Sort(keys)
	if len(keys) > 500 {
		keys = keys[:500]
	}
	viewed, err := s.pageVisitors(ctx, ws, brand, from, host, keys)
	if err != nil {
		return err
	}
	for k := range list {
		share := min(1, float64(list[k].Visitors)/float64(visitors))
		list[k].VisitorShare = &share
	}
	for _, h := range hits {
		pv := viewed[h.path][0]
		if pv == 0 {
			continue
		}
		share := min(1, float64(h.n)/float64(pv))
		i := &list[idx[h.fp]]
		if w := i.WorstPage; w == nil || share > w.Share || (share == w.Share && h.n > w.Visitors) {
			i.WorstPage = &errorPageShare{Path: h.path, Visitors: h.n, PageVisitors: pv, Share: share}
		}
	}
	return nil
}

// errorHosts are the sites the brand's errors came from in the period,
// most errors first, for the site filter.
func (s *Server) errorHosts(ctx context.Context, ws, brand string, from time.Time) ([]map[string]any, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+errorHost+` AS h, count(*) FROM customer_events
		WHERE workspace_id = $1 AND brand_id = $2 AND name = $3 AND occurred_at >= $4 GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT 50`, ws, brand, errorEvent, from)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	var h string
	var n int
	_, err = pgx.ForEachRow(rows, []any{&h, &n}, func() error {
		if h != "" {
			out = append(out, map[string]any{"host": h, "count": n})
		}
		return nil
	})
	return out, err
}

type errorOccurrence struct {
	ID      string    `json:"id"`
	At      time.Time `json:"at"`
	Type    string    `json:"type"`
	Message string    `json:"message"`
	// The full message of a minified React or Redux error, or "".
	MessageFull string `json:"message_full"`
	Stack       string `json:"stack"`
	// The stack read through the brand's source maps, or "".
	StackOriginal string `json:"stack_original"`
	// The React components it happened in, when the page sent them.
	ComponentStack string  `json:"component_stack"`
	Handled        bool    `json:"handled"`
	Path           string  `json:"path"`
	URL            string  `json:"url"`
	Host           string  `json:"host"`
	Source         string  `json:"source"`
	AnonymousID    *string `json:"anonymous_id"`
	// Read from the visitor's browser: desktop, mobile or tablet, the
	// browser and the operating system; "" when unknown.
	Device         string `json:"device"`
	Browser        string `json:"browser"`
	BrowserVersion string `json:"browser_version"`
	OS             string `json:"os"`
	OSVersion      string `json:"os_version"`
	UserAgent      string `json:"user_agent"`
	// The browser's language, the visit's country (ISO 3166-1 alpha-2),
	// the window's size (such as 1440x900) and the visitor's settings for
	// colours (light or dark) and motion (reduce or no-preference).
	Language      string `json:"language"`
	Country       string `json:"country"`
	Viewport      string `json:"viewport"`
	ColorScheme   string `json:"color_scheme"`
	ReducedMotion string `json:"reduced_motion"`
	Release       string `json:"release"`
	// Why it is filtered (a built-in filter or rule), or "".
	Filtered string `json:"filtered"`
	// The person the visitor is known as, to open their record.
	RecordID *string `json:"record_id"`
	// The recording of the visit it happened in, when there is one.
	RecordingID *string `json:"recording_id"`
}

type errorDay struct {
	Day   string `json:"day"`
	Count int    `json:"count"`
}

type errorPage struct {
	Path  string `json:"path"`
	Count int    `json:"count"`
	// Visitors it happened to on the page, visitors who viewed the page
	// in the period, and the share; 0 when the page has no views.
	Visitors     int     `json:"visitors"`
	PageVisitors int     `json:"page_visitors"`
	Share        float64 `json:"share"`
	// Times the page was viewed in the period, and errors over views.
	PageViews int     `json:"page_views"`
	ViewShare float64 `json:"view_share"`
}

// errorStatusChange is one time someone set an issue's status.
type errorStatusChange struct {
	Status string    `json:"status"`
	At     time.Time `json:"at"`
	By     *string   `json:"by"`
	Bulk   bool      `json:"bulk"`
}

// errorTarget is the issue a fingerprint now belongs to: itself, or what
// it was merged into.
func (s *Server) errorTarget(ctx context.Context, brand, fp string) (string, error) {
	var into string
	err := s.pool.QueryRow(ctx, `SELECT into_fingerprint FROM error_issue_merges WHERE brand_id = $1 AND fingerprint = $2`, brand, fp).Scan(&into)
	if errors.Is(err, pgx.ErrNoRows) {
		return fp, nil
	}
	return into, err
}

func (s *Server) getErrorIssue(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	fp := r.PathValue("fingerprint")
	if !errorFingerprintRe.MatchString(fp) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such error")
		return
	}
	days, ok := intParam(w, r, "days", 7, 1, 90)
	if !ok {
		return
	}
	ctx := r.Context()
	// A link to an issue that was merged opens the one it is part of.
	fp, err := s.errorTarget(ctx, a.Brand, fp)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	now := time.Now().UTC()
	from := now.AddDate(0, 0, -days)
	const where = ` FROM customer_events WHERE workspace_id = $1 AND brand_id = $2 AND name = $3 AND occurred_at >= $4 AND ` + errorFingerprint + ` = $5`
	i := errorIssue{Fingerprint: fp}
	var first, last *time.Time
	err = s.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE occurred_at >= $6), count(DISTINCT anonymous_id) FILTER (WHERE occurred_at >= $6), min(occurred_at), max(occurred_at),
		count(DISTINCT context->'page'->>'path') FILTER (WHERE occurred_at >= $6),
		count(DISTINCT `+errorHost+`) FILTER (WHERE occurred_at >= $6 AND `+errorHost+` <> ''),
		coalesce((array_agg(left(properties->>'release', 100) ORDER BY occurred_at) FILTER (WHERE coalesce(properties->>'release', '') <> ''))[1], ''),
		coalesce((array_agg(left(properties->>'release', 100) ORDER BY occurred_at DESC) FILTER (WHERE coalesce(properties->>'release', '') <> ''))[1], ''),
		CASE WHEN bool_and(`+errorFilter+` <> '') FILTER (WHERE occurred_at >= $6) THEN (array_agg(`+errorFilter+` ORDER BY occurred_at DESC))[1] ELSE '' END`+where,
		a.WorkspaceID, a.Brand, errorEvent, now.Add(-errorHistory), fp, from).Scan(&i.Count, &i.Visitors, &first, &last, &i.Pages, &i.Hosts, &i.FirstRelease, &i.LastRelease, &i.Filtered)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if first == nil {
		writeError(w, r, http.StatusNotFound, "not_found", "no such error in the last 90 days")
		return
	}
	i.FirstSeen, i.LastSeen = *first, *last
	i.New = !i.FirstSeen.Before(from)
	decided, err := s.errorDecisions(ctx, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	d, known := decided[fp]
	i.decide(d, known)

	seen, next, err := s.errorOccurrences(ctx, a.WorkspaceID, a.Brand, fp, time.Time{}, "", 20)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if len(seen) > 0 {
		i.Type, i.Message, i.MessageFull, i.LastPath, i.Host, i.Source, i.Handled = seen[0].Type, cutUTF8(seen[0].Message, 500), seen[0].MessageFull, seen[0].Path, seen[0].Host, seen[0].Source, seen[0].Handled
	}

	rows, err := s.pool.Query(ctx, `SELECT to_char(date_trunc('day', occurred_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD'), count(*)`+where+` AND occurred_at >= $6 GROUP BY 1 ORDER BY 1`,
		a.WorkspaceID, a.Brand, errorEvent, now.Add(-errorHistory), fp, from)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	byDay := map[string]int{}
	var day string
	var n int
	if _, err = pgx.ForEachRow(rows, []any{&day, &n}, func() error { byDay[day] = n; return nil }); err != nil {
		s.internal(w, r, err)
		return
	}
	series := []errorDay{}
	for _, k := range errorDays(from, now) {
		series = append(series, errorDay{Day: k, Count: byDay[k]})
		i.Trend = append(i.Trend, byDay[k])
	}

	rows, err = s.pool.Query(ctx, `SELECT left(coalesce(context->'page'->>'path', ''), 500), count(*), count(DISTINCT anonymous_id)`+where+` AND occurred_at >= $6 GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT 10`,
		a.WorkspaceID, a.Brand, errorEvent, now.Add(-errorHistory), fp, from)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	pages, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (errorPage, error) {
		var p errorPage
		return p, row.Scan(&p.Path, &p.Count, &p.Visitors)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if pages == nil {
		pages = []errorPage{}
	}
	paths := []string{}
	for _, p := range pages {
		if p.Path != "" {
			paths = append(paths, p.Path)
		}
	}
	viewed, err := s.pageVisitors(ctx, a.WorkspaceID, a.Brand, from, "", paths)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for k := range pages {
		if v := viewed[pages[k].Path]; v[0] > 0 {
			pv := v[0]
			pages[k].PageVisitors, pages[k].Share = pv, min(1, float64(pages[k].Visitors)/float64(pv))
			pages[k].PageViews, pages[k].ViewShare = v[1], min(1, float64(pages[k].Count)/float64(max(1, v[1])))
			if w := i.WorstPage; w == nil || pages[k].Share > w.Share {
				i.WorstPage = &errorPageShare{Path: pages[k].Path, Visitors: pages[k].Visitors, PageVisitors: pv, Share: pages[k].Share}
			}
		}
	}
	if visitors, err := s.siteVisitors(ctx, a.WorkspaceID, a.Brand, from, ""); err != nil {
		s.internal(w, r, err)
		return
	} else if visitors > 0 {
		share := min(1, float64(i.Visitors)/float64(visitors))
		i.VisitorShare = &share
	}
	history, err := s.errorStatusHistory(ctx, a.WorkspaceID, a.Brand, fp)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	merged, err := s.errorMergedFrom(ctx, a.Brand, fp)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"issue": i, "from": from, "to": now, "days": series, "pages": pages, "occurrences": seen, "next_cursor": nullableString(next),
		"history": history, "merged_from": merged})
}

// errorOccurrences are an issue's errors, newest first: limit of them
// from before the error (at, id), or the newest when at is zero. next is
// the cursor of the page after, or "".
func (s *Server) errorOccurrences(ctx context.Context, ws, brand, fp string, at time.Time, id string, limit int) ([]errorOccurrence, string, error) {
	cursor := ""
	if !at.IsZero() {
		cursor = "AND (e.occurred_at, e.id) < ($6, $7::uuid)"
	} else {
		cursor = "AND $6::timestamptz IS NULL AND $7::text IS NULL"
	}
	var atArg, idArg any
	if !at.IsZero() {
		atArg, idArg = at, id
	}
	rows, err := s.pool.Query(ctx, `SELECT e.id::text, e.occurred_at, left(coalesce(e.properties->>'type', ''), 200), left(coalesce(e.properties->>'message', ''), 2000),
			left(coalesce(e.properties->>'message_full', ''), 2000), left(coalesce(e.properties->>'stack', ''), 8000), left(coalesce(e.properties->>'component_stack', ''), 4000),
			coalesce(e.properties->>'handled', '') = 'true', left(coalesce(e.context->'page'->>'path', ''), 500),
			left(coalesce(e.context->'page'->>'url', ''), 1000), `+strings.ReplaceAll(errorHost, "context", "e.context")+`, e.source, e.anonymous_id, e.record_id::text,
			left(coalesce(e.context->>'userAgent', ''), 500), left(coalesce(e.properties->>'release', ''), 100),
			left(coalesce(e.context->>'locale', ''), 35), left(coalesce(e.properties->>'viewport', ''), 20), left(coalesce(e.properties->>'color_scheme', ''), 20),
			left(coalesce(e.properties->>'reduced_motion', ''), 20), coalesce(v.country, ''), coalesce(v.browser_version, ''), coalesce(v.os_version, ''),
			(SELECT rec.id::text FROM session_recordings rec WHERE rec.workspace_id = $1 AND rec.brand_id = $2 AND e.anonymous_id IS NOT NULL
				AND rec.anonymous_id = e.anonymous_id AND rec.started_at <= e.occurred_at + interval '1 minute' AND rec.last_at >= e.occurred_at - interval '1 minute'
				ORDER BY rec.started_at DESC LIMIT 1),
			`+strings.NewReplacer("properties", "e.properties", "context", "e.context").Replace(errorFilter)+`
		FROM customer_events e
		LEFT JOIN site_sessions v ON v.workspace_id = $1 AND e.context->>'sessionId' ~ '^[0-9a-f-]{36}$' AND v.id = (e.context->>'sessionId')::uuid
		WHERE e.workspace_id = $1 AND e.brand_id = $2 AND e.name = $3 AND e.occurred_at >= $4
			AND `+strings.NewReplacer("properties", "e.properties").Replace(errorFingerprint)+` = $5 `+cursor+`
		ORDER BY e.occurred_at DESC, e.id DESC LIMIT $8`,
		ws, brand, errorEvent, time.Now().Add(-errorHistory), fp, atArg, idArg, limit+1)
	if err != nil {
		return nil, "", err
	}
	seen, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (errorOccurrence, error) {
		var o errorOccurrence
		var lang string
		err := row.Scan(&o.ID, &o.At, &o.Type, &o.Message, &o.MessageFull, &o.Stack, &o.ComponentStack, &o.Handled, &o.Path, &o.URL, &o.Host, &o.Source, &o.AnonymousID,
			&o.RecordID, &o.UserAgent, &o.Release, &lang, &o.Viewport, &o.ColorScheme, &o.ReducedMotion, &o.Country, &o.BrowserVersion, &o.OSVersion,
			&o.RecordingID, &o.Filtered)
		o.Language = lang
		if o.UserAgent != "" {
			o.Device, o.Browser, o.OS = parseUA(o.UserAgent)
			// The error's own browser says its versions; the visit's are
			// only for an error that came without one.
			d := parseDevice(o.UserAgent, o.OS, o.Browser)
			if d.browserVersion != "" {
				o.BrowserVersion = cutUTF8(d.browserVersion, 20)
			}
			if d.osVersion != "" {
				o.OSVersion = cutUTF8(d.osVersion, 20)
			}
		}
		return o, err
	})
	if err != nil {
		return nil, "", err
	}
	if seen == nil {
		seen = []errorOccurrence{}
	}
	next := ""
	if len(seen) > limit {
		seen = seen[:limit]
		last := seen[len(seen)-1]
		next = last.At.UTC().Format(time.RFC3339Nano) + "_" + last.ID
	}
	maps := map[string]*sourcemap.Consumer{}
	for k := range seen {
		seen[k].StackOriginal = s.originalStack(ctx, ws, brand, seen[k].Stack, maps)
	}
	return seen, next, nil
}

// listErrorOccurrences pages through an issue's errors.
func (s *Server) listErrorOccurrences(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	fp := r.PathValue("fingerprint")
	if !errorFingerprintRe.MatchString(fp) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such error")
		return
	}
	limit, ok := intParam(w, r, "limit", 20, 1, 100)
	if !ok {
		return
	}
	var at time.Time
	var id string
	if c := r.URL.Query().Get("cursor"); c != "" {
		t, rest, found := strings.Cut(c, "_")
		var err error
		at, err = time.Parse(time.RFC3339Nano, t)
		if !found || err != nil || !validUUID(rest) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "cursor is the next_cursor of the page before")
			return
		}
		id = rest
	}
	ctx := r.Context()
	fp, err := s.errorTarget(ctx, a.Brand, fp)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	seen, next, err := s.errorOccurrences(ctx, a.WorkspaceID, a.Brand, fp, at, id, limit)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"fingerprint": fp, "data": seen, "next_cursor": nullableString(next)})
}

// errorBeforeEvent is something the visitor did before an error.
type errorBeforeEvent struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
	Name string    `json:"name"`
	Path string    `json:"path"`
}

// errorBefore is what the visitor did in the 30 minutes before one error:
// at most 30 page views and events, newest first, and the recording of
// the visit when there is one.
func (s *Server) errorBefore(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such error")
		return
	}
	ctx := r.Context()
	var anon *string
	var at time.Time
	err := s.pool.QueryRow(ctx, `SELECT anonymous_id, occurred_at FROM customer_events WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 AND name = $4`,
		id, a.WorkspaceID, a.Brand, errorEvent).Scan(&anon, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such error")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list := []errorBeforeEvent{}
	if anon != nil {
		rows, err := s.pool.Query(ctx, `SELECT occurred_at, kind, name, left(coalesce(context->'page'->>'path', ''), 500) FROM customer_events
			WHERE workspace_id = $1 AND brand_id = $2 AND anonymous_id = $3 AND occurred_at BETWEEN $4 AND $5 AND id <> $6
			ORDER BY occurred_at DESC LIMIT 30`, a.WorkspaceID, a.Brand, *anon, at.Add(-30*time.Minute), at, id)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if list, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (errorBeforeEvent, error) {
			var v errorBeforeEvent
			return v, row.Scan(&v.At, &v.Kind, &v.Name, &v.Path)
		}); err != nil {
			s.internal(w, r, err)
			return
		}
		if list == nil {
			list = []errorBeforeEvent{}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"at": at, "data": list})
}

// errorStatusHistory is every time someone set an issue's status, newest
// first, at most 50.
func (s *Server) errorStatusHistory(ctx context.Context, ws, brand, fp string) ([]errorStatusChange, error) {
	rows, err := s.pool.Query(ctx, `SELECT e.data->>'status', e.created_at, a.display_name, coalesce(e.data->>'bulk', '') = 'true'
		FROM audit_events e LEFT JOIN actors a ON a.id = e.actor_id
		WHERE e.workspace_id = $1 AND e.action = 'error_issue.updated' AND e.data->>'brand_id' = $2 AND e.data->>'fingerprint' = $3
		ORDER BY e.created_at DESC, e.id DESC LIMIT 50`, ws, brand, fp)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (errorStatusChange, error) {
		var c errorStatusChange
		return c, row.Scan(&c.Status, &c.At, &c.By, &c.Bulk)
	})
	if out == nil {
		out = []errorStatusChange{}
	}
	return out, err
}

func (s *Server) updateErrorIssue(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	fp := r.PathValue("fingerprint")
	if !errorFingerprintRe.MatchString(fp) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such error")
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !slices.Contains(errorStatuses, in.Status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is open, resolved, ignored or archived")
		return
	}
	ctx := r.Context()
	fp, err := s.errorTarget(ctx, a.Brand, fp)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	known, err := s.knownErrorIssues(ctx, a.WorkspaceID, a.Brand, []string{fp})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if len(known) == 0 {
		writeError(w, r, http.StatusNotFound, "not_found", "no such error in the last 90 days")
		return
	}
	var at time.Time
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		at, err = setErrorStatus(ctx, tx, &a.principal, a.WorkspaceID, a.Brand, fp, in.Status, "")
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"fingerprint": fp, "status": in.Status, "status_at": at})
}

// knownErrorIssues is which of fps have an error in the last 90 days.
func (s *Server) knownErrorIssues(ctx context.Context, ws, brand string, fps []string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT `+errorFingerprint+` FROM customer_events WHERE workspace_id = $1 AND brand_id = $2 AND name = $3 AND occurred_at >= $4 AND `+errorFingerprint+` = ANY($5)`,
		ws, brand, errorEvent, time.Now().Add(-errorHistory), fps)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// setErrorStatus records what a person decided about an issue, with its
// audit event; bulk is the bulk change it was part of, or "".
func setErrorStatus(ctx context.Context, tx pgx.Tx, p *principal, ws, brand, fp, status, bulk string) (time.Time, error) {
	var id string
	var at time.Time
	if err := tx.QueryRow(ctx, `INSERT INTO error_issues (workspace_id, brand_id, fingerprint, status, updated_by) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (brand_id, fingerprint) DO UPDATE SET status = EXCLUDED.status, status_at = now(), updated_by = EXCLUDED.updated_by
		RETURNING id::text, status_at`, ws, brand, fp, status, nullableString(p.ActorID)).Scan(&id, &at); err != nil {
		return at, err
	}
	data := map[string]any{"brand_id": brand, "fingerprint": fp, "status": status, "bulk": bulk != ""}
	props := map[string]any{"status": status, "bulk": bulk != ""}
	return at, recordWithProps(ctx, tx, p, ws, "error_issue.updated", "error_issue", id, data, props)
}

// deleteErrorIssues deletes the issues' errors (the Error Caught events
// with their fingerprints, any age) and what anyone decided about them,
// with an audit event each. If one happens again it is a new issue.
func deleteErrorIssues(ctx context.Context, tx pgx.Tx, p *principal, ws, brand string, fps []string, bulk bool) (int64, error) {
	tag, err := tx.Exec(ctx, `DELETE FROM customer_events WHERE workspace_id = $1 AND brand_id = $2 AND name = $3 AND `+errorFingerprint+` = ANY($4)`, ws, brand, errorEvent, fps)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM error_issues WHERE workspace_id = $1 AND brand_id = $2 AND fingerprint = ANY($3)`, ws, brand, fps); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM error_issue_merges WHERE workspace_id = $1 AND brand_id = $2 AND (fingerprint = ANY($3) OR into_fingerprint = ANY($3))`, ws, brand, fps); err != nil {
		return 0, err
	}
	for _, fp := range fps {
		if err := recordWithProps(ctx, tx, p, ws, "error_issue.deleted", "error_issue", fp, map[string]any{"brand_id": brand, "fingerprint": fp},
			map[string]any{"bulk": bulk}); err != nil {
			return 0, err
		}
	}
	return tag.RowsAffected(), nil
}

func (s *Server) deleteErrorIssue(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	fp := r.PathValue("fingerprint")
	if !errorFingerprintRe.MatchString(fp) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such error")
		return
	}
	ctx := r.Context()
	fp, err := s.errorTarget(ctx, a.Brand, fp)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	known, err := s.knownErrorIssues(ctx, a.WorkspaceID, a.Brand, []string{fp})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if len(known) == 0 {
		writeError(w, r, http.StatusNotFound, "not_found", "no such error in the last 90 days")
		return
	}
	var n int64
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		n, err = deleteErrorIssues(ctx, tx, &a.principal, a.WorkspaceID, a.Brand, known, false)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": 1, "errors_deleted": n})
}

// bulkErrorIssues does one thing to many issues at once: set their
// status, or delete them. Fingerprints with no error in the last 90 days
// are skipped and counted.
func (s *Server) bulkErrorIssues(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Fingerprints []string `json:"fingerprints"`
		Action       string   `json:"action"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Action != "delete" && !slices.Contains(errorStatuses, in.Action) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "action is open, resolved, ignored, archived or delete")
		return
	}
	if len(in.Fingerprints) == 0 || len(in.Fingerprints) > 200 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "fingerprints lists 1 to 200 issues")
		return
	}
	for _, fp := range in.Fingerprints {
		if !errorFingerprintRe.MatchString(fp) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "a fingerprint is 1 to 64 lowercase letters and digits")
			return
		}
	}
	slices.Sort(in.Fingerprints)
	fps := slices.Compact(in.Fingerprints)
	ctx := r.Context()
	known, err := s.knownErrorIssues(ctx, a.WorkspaceID, a.Brand, fps)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	slices.Sort(known)
	var errs int64
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if len(known) == 0 {
			return nil
		}
		if in.Action == "delete" {
			var err error
			errs, err = deleteErrorIssues(ctx, tx, &a.principal, a.WorkspaceID, a.Brand, known, true)
			return err
		}
		for _, fp := range known {
			if _, err := setErrorStatus(ctx, tx, &a.principal, a.WorkspaceID, a.Brand, fp, in.Action, "bulk"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := map[string]any{"action": in.Action, "changed": len(known), "skipped": len(fps) - len(known), "fingerprints": known}
	if in.Action == "delete" {
		out["errors_deleted"] = errs
	}
	writeJSON(w, http.StatusOK, out)
}
