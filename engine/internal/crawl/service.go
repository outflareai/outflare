// Package crawl is OutflareBot, the crawler behind SEO audits. It reads a
// site's pages one at a time as a public, self-identified crawler: it obeys
// robots.txt and Crawl-delay (an owner who proved a site may turn robots.txt
// off for that site), slows down on 429, 503, Retry-After, errors and slow
// answers, never dials a non-public address, and skips hosts on the
// install's opt-out list. outflare.ai/bot/ promises all of this to site
// owners; keep the two in step.
//
// A crawl run is a chain of jobs: each works for a while, records what it
// fetched in crawl_pages, and queues the next step, so a large site never
// holds a worker and a restart picks up where the run left off.
package crawl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// JobRun works one step of a crawl run.
const JobRun = "crawl.run"

const (
	// stepFor is how long one job works before queueing the next step.
	stepFor = 90 * time.Second
	// maxCrawlDelay caps robots.txt's Crawl-delay, so a typo of 86400
	// does not stall a run for a day.
	maxCrawlDelay = 30 * time.Second
	// maxDelay caps the backed-off pause between requests.
	maxDelay = 60 * time.Second
	// maxRetryAfter caps how long a Retry-After is waited for within a run.
	maxRetryAfter = 2 * time.Minute
	// slowAnswer is how long an answer may take before the crawler slows.
	slowAnswer = 5 * time.Second
	// maxErrors in a row ends a run: the site is down or refusing us.
	maxErrors = 10
	// robotsFor is how long a fetched robots.txt is trusted.
	robotsFor = 24 * time.Hour
	// reverifyAfter is how often a verified site's proof is checked again.
	reverifyAfter = 30 * 24 * time.Hour
	// maxSitemaps is how many sitemap files a run reads.
	maxSitemaps = 10
	// frontierFactor caps the URLs a run queues at this many times its
	// page limit.
	frontierFactor = 3
)

// Limits are what a site may ask of the crawler. Proving ownership raises
// them: an owner may crawl their own site harder than anyone else may.
type Limits struct {
	MaxPages        int
	MinDelayMS      int
	MayIgnoreRobots bool
}

// LimitsFor is the limits of a verified or unverified site.
func LimitsFor(verified bool) Limits {
	if verified {
		return Limits{MaxPages: 10000, MinDelayMS: 250, MayIgnoreRobots: true}
	}
	return Limits{MaxPages: 100, MinDelayMS: 2000}
}

// Errors StartRun returns.
var (
	ErrRunning   = errors.New("a crawl of this site is already running")
	ErrOptedOut  = errors.New("this site's owner asked not to be crawled")
	ErrNoSite    = errors.New("site not found")
	ErrSitePause = errors.New("the site is paused")
)

// Service runs crawls.
type Service struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	// Fetcher reaches real sites; nil builds an HTTPFetcher.
	Fetcher Fetcher
	// Fallback reads blocked pages of proved sites through a connector;
	// nil never does.
	Fallback Fallback
	// Resolver looks up DNS proofs; nil skips them.
	Resolver TXTResolver
	// AllowPrivate lets crawls reach private addresses (development).
	AllowPrivate bool
	// StepFor overrides how long a step works, for tests.
	StepFor time.Duration
	// Wait overrides the pause between requests, for tests.
	Wait func(ctx context.Context, d time.Duration) error
	// AfterAudit runs in the transaction that stores a crawl's audit, so
	// other checks of the site's pages (keyword targets) see the crawl.
	AfterAudit func(ctx context.Context, tx pgx.Tx, ws, siteID, runID string) error
}

// Register adds the crawl job handler.
func (s *Service) Register(q *jobs.Queue) {
	q.Handle(JobRun, s.step)
	q.Handle(JobGlance, s.glance)
}

func (s *Service) fetcher(test bool) Fetcher {
	if test {
		return Simulated{}
	}
	if s.Fetcher == nil {
		s.Fetcher = NewHTTPFetcher(s.AllowPrivate)
	}
	return s.Fetcher
}

func (s *Service) wait(ctx context.Context, d time.Duration) error {
	if s.Wait != nil {
		return s.Wait(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Check looks for a proof that the workspace owns a site. A test
// workspace's sites always pass, as method "test", without a lookup.
func (s *Service) Check(ctx context.Context, test bool, startURL, host, token string) (string, error) {
	if test {
		return "test", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return Verify(ctx, s.fetcher(false), s.Resolver, startURL, host, token)
}

// CheckSite checks a site's address can be crawled from this install.
func (s *Service) CheckSite(raw string) error { return CheckURL(raw, s.AllowPrivate) }

// OptedOut reports whether a host (or a domain above it) is on the opt-out
// list.
func OptedOut(ctx context.Context, q jobs.Querier, host string) (bool, error) {
	h := strings.ToLower(host)
	if i := strings.LastIndexByte(h, ':'); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	var out bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crawl_optouts WHERE $1 = host OR $1 LIKE '%.' || host)`, h).Scan(&out)
	return out, err
}

type payload struct {
	RunID string `json:"run_id"`
	Step  int    `json:"step"`
}

// StartRun queues a crawl of a site, with the limits the site has now.
// trigger is manual or schedule.
func StartRun(ctx context.Context, tx pgx.Tx, ws, siteID, trigger string, actorID *string) (string, error) {
	var (
		host, status    string
		maxPages, delay int
		obey            bool
		verifiedAt      *time.Time
	)
	err := tx.QueryRow(ctx, `
		SELECT host, status, max_pages, crawl_delay_ms, obey_robots, verified_at
		FROM seo_sites WHERE id = $1 AND workspace_id = $2 FOR UPDATE`, siteID, ws).
		Scan(&host, &status, &maxPages, &delay, &obey, &verifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoSite
	}
	if err != nil {
		return "", err
	}
	if status != "active" {
		return "", ErrSitePause
	}
	if out, err := OptedOut(ctx, tx, host); err != nil {
		return "", err
	} else if out {
		return "", ErrOptedOut
	}
	lim := LimitsFor(verifiedAt != nil)
	maxPages, delay = min(maxPages, lim.MaxPages), max(delay, lim.MinDelayMS)
	obey = obey || !lim.MayIgnoreRobots
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO crawl_runs (workspace_id, site_id, host, trigger, max_pages, delay_ms, obey_robots, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		ws, siteID, host, trigger, maxPages, delay, obey, actorID).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return "", ErrRunning
	}
	if err != nil {
		return "", err
	}
	_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobRun, Payload: payload{RunID: id}, WorkspaceID: &ws,
		IdempotencyKey: "crawl:" + id + ":0"})
	return id, err
}

// run is a crawl run's state while a step works.
type run struct {
	id, ws, siteID, host, startURL string
	status                         string
	step, maxPages, delayMS        int
	obey, test                     bool
	errors, fetched, found         int
	token                          string
	verifiedAt, checkedAt          *time.Time
	robots                         map[string]*Robots
	fetch                          Fetcher
	// The workspace's fallback fetcher, looked up once per step, and how
	// many of its reads failed in a row.
	fb       *FallbackFetcher
	fbLoaded bool
	fbFails  int
}

func (s *Service) step(ctx context.Context, job jobs.Job) error {
	var p payload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
	}
	r := &run{id: p.RunID, robots: map[string]*Robots{}}
	err := s.Pool.QueryRow(ctx, `
		SELECT r.workspace_id, r.site_id, r.host, r.status, r.step, r.max_pages, r.delay_ms, r.obey_robots,
		       r.consecutive_errors, r.pages_fetched, r.pages_found, s.start_url, s.verify_token, s.verified_at,
		       s.verify_checked_at, w.test
		FROM crawl_runs r JOIN seo_sites s ON s.id = r.site_id JOIN workspaces w ON w.id = r.workspace_id
		WHERE r.id = $1`, p.RunID).
		Scan(&r.ws, &r.siteID, &r.host, &r.status, &r.step, &r.maxPages, &r.delayMS, &r.obey, &r.errors,
			&r.fetched, &r.found, &r.startURL, &r.token, &r.verifiedAt, &r.checkedAt, &r.test)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // the site was deleted
	}
	if err != nil {
		return err
	}
	if (r.status != "queued" && r.status != "running") || r.step != p.Step {
		return nil // stopped, finished, or a stale copy of a step
	}
	budget := s.StepFor
	if budget == 0 {
		budget = stepFor
	}
	// Take the run, unless another run on the same host is working now:
	// one crawl at a time per site, across every workspace.
	tag, err := s.Pool.Exec(ctx, `
		UPDATE crawl_runs r SET status = 'running', started_at = COALESCE(started_at, now()),
			lease_until = now() + $3::interval
		WHERE r.id = $1 AND r.step = $2 AND r.status IN ('queued', 'running')
		  AND (r.lease_until IS NULL OR r.lease_until < now() OR $4)
		  AND NOT EXISTS (SELECT 1 FROM crawl_runs o WHERE o.host = r.host AND o.id <> r.id
		                  AND o.status = 'running' AND o.lease_until > now())`,
		r.id, r.step, (budget + 2*time.Minute).String(), job.Attempt > 1)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		_, err := jobs.Enqueue(ctx, s.Pool, jobs.NewJob{Kind: JobRun, Payload: p, WorkspaceID: &r.ws, RunAt: time.Now().Add(time.Minute)})
		return err
	}
	r.fetch = s.fetcher(r.test)
	ev := track.Event{Name: "crawl.finished", WorkspaceID: r.ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"run_id": r.id, "site_id": r.siteID, "step": r.step}}

	if r.step == 0 {
		if done, err := s.begin(ctx, r); err != nil || done {
			return s.failed(ctx, r, ev, err)
		}
	}
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		var status string
		if err := s.Pool.QueryRow(ctx, `SELECT status FROM crawl_runs WHERE id = $1`, r.id).Scan(&status); err != nil {
			return err
		}
		if status != "running" {
			return nil // stopped
		}
		if r.fetched >= r.maxPages {
			return s.finish(ctx, r, ev, "done", "limit", "")
		}
		var (
			pageID int64
			raw    string
			depth  int
		)
		err := s.Pool.QueryRow(ctx, `
			SELECT id, url, depth FROM crawl_pages WHERE run_id = $1 AND status = 'queued'
			ORDER BY depth, id LIMIT 1`, r.id).Scan(&pageID, &raw, &depth)
		if errors.Is(err, pgx.ErrNoRows) {
			return s.finish(ctx, r, ev, "done", "done", "")
		}
		if err != nil {
			return err
		}
		pause, err := s.page(ctx, r, pageID, raw, depth)
		if err != nil {
			return s.failed(ctx, r, ev, err)
		}
		if r.errors >= maxErrors {
			return s.finish(ctx, r, ev, "failed", "site_errors", "site_errors")
		}
		if pause > 0 && !r.test {
			if err := s.wait(ctx, pause); err != nil {
				return err
			}
		}
	}
	// Hand over to the next step, which a restart would pick up too.
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE crawl_runs SET step = step + 1, lease_until = NULL WHERE id = $1 AND status = 'running'`, r.id); err != nil {
			return err
		}
		_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobRun, Payload: payload{RunID: r.id, Step: r.step + 1}, WorkspaceID: &r.ws,
			IdempotencyKey: fmt.Sprintf("crawl:%s:%d", r.id, r.step+1)})
		return err
	})
}

// errFinished is begin's way of saying it already ended the run.
var errFinished = errors.New("run finished")

// failed ends a run on an unexpected error, or does nothing when begin
// already ended it.
func (s *Service) failed(ctx context.Context, r *run, ev track.Event, err error) error {
	if err == nil || errors.Is(err, errFinished) {
		return nil
	}
	if ctx.Err() != nil {
		return err // shutting down: the job is retried
	}
	if ferr := s.finish(ctx, r, ev, "failed", "error", "internal"); ferr != nil {
		return errors.Join(err, ferr)
	}
	s.Log.Error("crawl failed", "run_id", r.id, "err", err)
	return nil
}

// begin starts a run: checks the opt-out list and a stale proof, reads
// robots.txt and the sitemaps, and queues the first pages.
func (s *Service) begin(ctx context.Context, r *run) (bool, error) {
	ev := track.Event{Name: "crawl.finished", WorkspaceID: r.ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"run_id": r.id, "site_id": r.siteID, "step": 0}}
	if out, err := OptedOut(ctx, s.Pool, r.host); err != nil {
		return false, err
	} else if out {
		return true, s.finish(ctx, r, ev, "stopped", "opted_out", "crawl_opted_out")
	}
	// A proof can be taken down; check it again now and then, and crawl
	// as anyone else would when it is gone.
	if r.verifiedAt != nil && (r.checkedAt == nil || time.Since(*r.checkedAt) > reverifyAfter) {
		method, err := s.Check(ctx, r.test, r.startURL, r.host, r.token)
		if err == nil {
			_, err = s.Pool.Exec(ctx, `UPDATE seo_sites SET verify_checked_at = now(), verify_method = $2 WHERE id = $1`, r.siteID, method)
		} else {
			lim := LimitsFor(false)
			r.maxPages, r.delayMS, r.obey = min(r.maxPages, lim.MaxPages), max(r.delayMS, lim.MinDelayMS), true
			err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `UPDATE seo_sites SET verified_at = NULL, verify_method = '', verify_checked_at = now(),
					max_pages = LEAST(max_pages, $2), crawl_delay_ms = GREATEST(crawl_delay_ms, $3), obey_robots = true, updated_at = now()
					WHERE id = $1`, r.siteID, lim.MaxPages, lim.MinDelayMS); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE crawl_runs SET max_pages = $2, delay_ms = $3, obey_robots = true WHERE id = $1`,
					r.id, r.maxPages, r.delayMS); err != nil {
					return err
				}
				return audit.Record(ctx, tx, audit.Event{WorkspaceID: r.ws, ActorType: "system", Action: "seo_site.unverified",
					TargetType: "seo_site", TargetID: r.siteID, Data: map[string]any{"reason": "proof_missing"}})
			})
		}
		if err != nil {
			return false, err
		}
	}
	robots, err := s.robotsFor(ctx, r, r.startURL)
	if err != nil {
		return false, err
	}
	if robots.all != nil && !*robots.all && r.obey {
		return true, s.finish(ctx, r, ev, "failed", "robots_unreachable", "site_unreachable")
	}
	if _, err := s.queue(ctx, r, []string{r.startURL}, 0, "start", ""); err != nil {
		return false, err
	}
	start, _ := url.Parse(r.startURL)
	if err := s.readLLMSTxt(ctx, r, start.Scheme+"://"+start.Host+"/llms.txt"); err != nil {
		return false, err
	}
	// Sitemaps: those robots.txt lists, else the usual place.
	maps := []string{}
	for _, m := range robots.Sitemaps {
		if u, ok := Normalize(start, m); ok && SameSite(hostOfURL(u), r.host) {
			maps = append(maps, u)
		}
	}
	if len(maps) == 0 {
		maps = append(maps, start.Scheme+"://"+start.Host+"/sitemap.xml")
	}
	seen := map[string]bool{}
	for read := 0; len(maps) > 0 && read < maxSitemaps; read++ {
		m := maps[0]
		maps = maps[1:]
		if seen[m] {
			continue
		}
		seen[m] = true
		if ok, err := s.allowed(ctx, r, m); err != nil || !ok {
			if err != nil {
				return false, err
			}
			continue
		}
		resp, err := r.fetch.Fetch(ctx, m)
		if !r.test {
			if err := s.wait(ctx, r.delay(nil)); err != nil {
				return false, err
			}
		}
		if err != nil || resp.Status != 200 {
			continue
		}
		pages, more, err := ParseSitemap(resp.Body)
		if err != nil {
			continue
		}
		var own []string
		for _, p := range pages {
			if u, ok := Normalize(start, p); ok && SameSite(hostOfURL(u), r.host) {
				own = append(own, u)
			}
		}
		if _, err := s.queue(ctx, r, own, 1, "sitemap", m); err != nil {
			return false, err
		}
		maps = append(maps, more...)
	}
	return false, nil
}

// delay is the pause before the next request: the run's (backed off when
// the site struggled) or robots.txt's Crawl-delay, whichever is longer.
func (r *run) delay(robots *Robots) time.Duration {
	d := time.Duration(r.delayMS) * time.Millisecond
	if robots == nil {
		robots = r.robots[r.host]
	}
	if robots != nil && robots.Delay > d {
		d = min(robots.Delay, maxCrawlDelay)
	}
	return d
}

// robotsFor returns the robots.txt that governs a URL's host, reading it
// when this run has none for the host or has had it a day.
func (s *Service) robotsFor(ctx context.Context, r *run, raw string) (*Robots, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return DisallowAll(), nil
	}
	host := strings.ToLower(u.Host)
	if rb, ok := r.robots[host]; ok {
		return rb, nil
	}
	var (
		status  int
		body    string
		fetched time.Time
	)
	err = s.Pool.QueryRow(ctx, `SELECT status, body, fetched_at FROM crawl_robots WHERE run_id = $1 AND host = $2`, r.id, host).
		Scan(&status, &body, &fetched)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && time.Since(fetched) > robotsFor) {
		status, body = s.readRobots(ctx, r, u.Scheme+"://"+u.Host+"/robots.txt")
		_, err = s.Pool.Exec(ctx, `
			INSERT INTO crawl_robots (run_id, host, status, body) VALUES ($1, $2, $3, $4)
			ON CONFLICT (run_id, host) DO UPDATE SET status = EXCLUDED.status, body = EXCLUDED.body, fetched_at = now()`,
			r.id, host, status, body)
	}
	if err != nil {
		return nil, err
	}
	rb := robotsFrom(status, body, Token)
	r.robots[host] = rb
	return rb, nil
}

// readLLMSTxt checks the site's /llms.txt, for the audit's AI checks. An
// HTML page at that address (a site that answers every path) counts as no
// file.
func (s *Service) readLLMSTxt(ctx context.Context, r *run, raw string) error {
	if ok, err := s.allowed(ctx, r, raw); err != nil || !ok {
		return err
	}
	status, size := 0, 0
	resp, err := r.fetch.Fetch(ctx, raw)
	if err == nil {
		status, size = resp.Status, min(len(resp.Body), maxLLMSTxtBytes)
		if status == 200 && strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "html") {
			status, size = 404, 0
		}
	}
	if !r.test {
		if err := s.wait(ctx, r.delay(nil)); err != nil {
			return err
		}
	}
	_, err = s.Pool.Exec(ctx, `UPDATE crawl_runs SET llms_txt_status = $2, llms_txt_bytes = $3 WHERE id = $1`, r.id, status, size)
	return err
}

// robotsFrom is the rules a fetched robots.txt sets for an agent token.
func robotsFrom(status int, body, token string) *Robots {
	switch {
	case status >= 200 && status < 300:
		return ParseRobots([]byte(body), token)
	case status >= 400 && status < 500:
		return AllowAll() // RFC 9309: no robots.txt, no rules
	default:
		return DisallowAll() // 5xx or no answer: assume nothing is allowed
	}
}

// readRobots fetches a robots.txt, following up to five redirects as RFC
// 9309 asks. It returns the status (0 for no answer) and the body.
func (s *Service) readRobots(ctx context.Context, r *run, raw string) (int, string) {
	return readRobots(ctx, r.fetch, raw)
}

func readRobots(ctx context.Context, f Fetcher, raw string) (int, string) {
	for range 5 {
		resp, err := f.Fetch(ctx, raw)
		if err != nil {
			return 0, ""
		}
		if resp.Status >= 300 && resp.Status < 400 {
			base, _ := url.Parse(raw)
			next, ok := Normalize(base, resp.Header.Get("Location"))
			if !ok {
				return 404, ""
			}
			raw = next
			continue
		}
		body := resp.Body
		if len(body) > maxRobotsBytes {
			body = body[:maxRobotsBytes]
		}
		return resp.Status, strings.ToValidUTF8(string(body), "")
	}
	return 404, "" // too many redirects counts as no file
}

// allowed reports whether robots.txt lets the run fetch a URL, or whether
// the run ignores it (an owner's choice for their own site).
func (s *Service) allowed(ctx context.Context, r *run, raw string) (bool, error) {
	if !r.obey {
		return true, nil
	}
	rb, err := s.robotsFor(ctx, r, raw)
	if err != nil {
		return false, err
	}
	u, _ := url.Parse(raw)
	path := u.EscapedPath()
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	return rb.Allowed(path), nil
}

// queue adds URLs to the run's queue, up to its cap, and reports how many
// were new.
func (s *Service) queue(ctx context.Context, r *run, urls []string, depth int, source, foundOn string) (int, error) {
	room := r.maxPages*frontierFactor - r.found
	if room <= 0 || len(urls) == 0 {
		return 0, nil
	}
	if len(urls) > room {
		urls = urls[:room]
	}
	tag, err := s.Pool.Exec(ctx, `
		INSERT INTO crawl_pages (workspace_id, run_id, url, depth, source, found_on)
		SELECT $1, $2, u, $4, $5, $6 FROM unnest($3::text[]) u
		ON CONFLICT (run_id, url) DO NOTHING`, r.ws, r.id, urls, depth, source, foundOn)
	if err != nil {
		return 0, err
	}
	n := int(tag.RowsAffected())
	r.found += n
	_, err = s.Pool.Exec(ctx, `UPDATE crawl_runs SET pages_found = pages_found + $2 WHERE id = $1`, r.id, n)
	return n, err
}

// page fetches one queued URL and records the result. It returns how long
// to pause before the next request.
func (s *Service) page(ctx context.Context, r *run, id int64, raw string, depth int) (time.Duration, error) {
	set := func(status, reason string, resp *Response, extra string, args ...any) error {
		var code *int
		ct, n, ms := "", 0, 0
		if resp != nil {
			code, ct, n, ms = &resp.Status, resp.ContentType(), len(resp.Body), int(resp.Duration/time.Millisecond)
		}
		a := append([]any{id, status, reason, code, ct, n, ms}, args...)
		_, err := s.Pool.Exec(ctx, `
			UPDATE crawl_pages SET status = $2, reason = $3, http_status = $4, content_type = $5, bytes = $6,
				duration_ms = $7, fetched_at = now()`+extra+` WHERE id = $1`, a...)
		return err
	}
	count := func(col string, bytes int) error {
		_, err := s.Pool.Exec(ctx, `UPDATE crawl_runs SET `+col+` = `+col+` + 1, bytes = bytes + $2, consecutive_errors = $3 WHERE id = $1`,
			r.id, bytes, r.errors)
		return err
	}
	ok, err := s.allowed(ctx, r, raw)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, errors.Join(set("blocked", "robots", nil, ""), count("pages_blocked", 0))
	}
	resp, err := r.fetch.Fetch(ctx, raw)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		reason := "network"
		if errors.Is(err, ErrPrivate) {
			reason = "private_address"
		}
		r.errors++
		return r.delay(nil), errors.Join(set("failed", reason, nil, ""), count("pages_failed", 0))
	}
	via := ""
	if technicalBlock(resp) && !r.test {
		hint := ""
		switch {
		case r.verifiedAt == nil:
			hint = "verify_site"
		case s.Fallback != nil:
			if fr := s.viaFallback(ctx, r, raw, false); fr != nil {
				resp, via = fr, r.fb.Connector
			} else if r.fb == nil {
				hint = "connect_geonode"
			}
		}
		// Only the workspace's own connection reads these pages; without
		// one the page stays blocked and the crawl says what to add.
		if _, err := s.Pool.Exec(ctx, `UPDATE crawl_runs SET pages_firewalled = pages_firewalled + 1,
			fallback_hint = CASE WHEN $2 = '' THEN fallback_hint ELSE $2 END WHERE id = $1`, r.id, hint); err != nil {
			return 0, err
		}
	}
	r.fetched++
	pause := r.delay(nil)
	switch {
	case resp.Status == http.StatusTooManyRequests || resp.Status == http.StatusServiceUnavailable:
		// The site asked us to slow down: wait as long as it says, then
		// go at half the speed for the rest of the run. The page is tried
		// again later; a run of these ends the run.
		r.errors++
		r.fetched--
		r.delayMS = min(r.delayMS*2, int(maxDelay/time.Millisecond))
		if _, err := s.Pool.Exec(ctx, `UPDATE crawl_runs SET delay_ms = $2, consecutive_errors = $3 WHERE id = $1`, r.id, r.delayMS, r.errors); err != nil {
			return 0, err
		}
		return max(retryAfter(resp.Header.Get("Retry-After")), r.delay(nil)), nil
	case resp.Status >= 500:
		r.errors++
	default:
		r.errors = 0
	}
	if resp.Duration > slowAnswer {
		r.delayMS = min(r.delayMS*2, int(maxDelay/time.Millisecond))
		if _, err := s.Pool.Exec(ctx, `UPDATE crawl_runs SET delay_ms = $2 WHERE id = $1`, r.id, r.delayMS); err != nil {
			return 0, err
		}
	}
	base, _ := url.Parse(raw)
	status := "fetched"
	if resp.Status >= 500 {
		status = "failed"
	}
	var (
		target            string
		noindex, nofollow bool
		linksOut          int
	)
	if resp.Status >= 300 && resp.Status < 400 {
		if loc, ok := Normalize(base, resp.Header.Get("Location")); ok {
			target = loc
			if SameSite(hostOfURL(loc), r.host) {
				if _, err := s.queue(ctx, r, []string{loc}, depth, "redirect", raw); err != nil {
					return 0, err
				}
			}
		}
	}
	xrt := strings.TrimSpace(strings.Join(resp.Header.Values("X-Robots-Tag"), ", "))
	noindex, nofollow = RobotsDirectives(xrt)
	var pg *Page
	jsShell := false
	if resp.Status >= 200 && resp.Status < 300 && strings.Contains(resp.ContentType(), "html") {
		parsed := ParsePage(base, resp.Body)
		pg = &parsed
		// As served, the page is an empty app shell: crawlers that do not
		// run scripts see little of it, whoever reads it next.
		jsShell = !noindex && !pg.Noindex && looksLikeShell(pg, resp.Body)
		// A page that its scripts build is read through the workspace's
		// own scraping connector, which runs them, when the site is proved.
		// Links in the served HTML stay links.
		if jsShell && via == "" && r.verifiedAt != nil && !r.test && s.Fallback != nil {
			if fr := s.viaFallback(ctx, r, raw, true); fr != nil && fr.Status >= 200 && fr.Status < 300 && strings.Contains(fr.ContentType(), "html") {
				built := ParsePage(base, fr.Body)
				have := map[string]bool{}
				for _, l := range built.Links {
					have[l.URL] = true
				}
				for _, l := range parsed.Links {
					if !have[l.URL] {
						built.Links = append(built.Links, l)
					}
				}
				pg, resp, via = &built, fr, r.fb.Connector
			}
		}
		noindex, nofollow, linksOut = noindex || pg.Noindex, nofollow || pg.Nofollow, len(pg.Links)
		// A meta refresh is a redirect too.
		if pg.MetaRefreshTo != "" {
			if loc, ok := Normalize(base, pg.MetaRefreshTo); ok {
				target = loc
				if SameSite(hostOfURL(loc), r.host) {
					if _, err := s.queue(ctx, r, []string{loc}, depth, "redirect", raw); err != nil {
						return 0, err
					}
				}
			}
		}
		if !nofollow {
			var own []string
			for _, l := range pg.Links {
				if SameSite(hostOfURL(l.URL), r.host) && !l.Nofollow {
					own = append(own, l.URL)
				}
			}
			if _, err := s.queue(ctx, r, own, depth+1, "link", raw); err != nil {
				return 0, err
			}
		}
	}
	reason := ""
	if resp.Truncated {
		reason = "truncated"
	}
	col := "pages_fetched"
	if status == "failed" {
		col, reason = "pages_failed", "server_error"
	}
	err = errors.Join(set(status, reason, resp, `, redirect_to = $8, noindex = $9, nofollow = $10, links_out = $11, x_robots_tag = $12,
		fetched_via = $13, js_shell = $14`, target, noindex, nofollow, linksOut, clip(xrt, 500), via, jsShell), count(col, len(resp.Body)))
	if err == nil && pg != nil {
		err = s.keepPage(ctx, r, id, pg)
	}
	return pause, err
}

// maxFallbackFails is how many fallback reads in a row may fail before a
// step stops asking (out of tokens, or the provider is blocked too).
const maxFallbackFails = 3

// viaFallback reads a blocked page (or, with js, a page its scripts
// build) through the workspace's fallback
// connector. It returns nil when there is none or it failed; the failure
// is an event and a log line, and the page keeps the site's answer.
func (s *Service) viaFallback(ctx context.Context, r *run, raw string, js bool) *Response {
	if !r.fbLoaded {
		r.fbLoaded = true
		fb, err := s.Fallback.For(ctx, r.ws)
		if err != nil {
			track.Fail(ctx, s.Log, track.Event{Name: "crawl.page_fallback", WorkspaceID: r.ws, ActorType: "system", Surface: "job",
				Props: map[string]any{"run_id": r.id, "site_id": r.siteID}}, "fallback_unavailable", fmt.Errorf("crawl %s fallback: %w", r.id, err))
		}
		r.fb = fb
	}
	if r.fb == nil || r.fbFails >= maxFallbackFails {
		return nil
	}
	ev := track.Event{Name: "crawl.page_fallback", WorkspaceID: r.ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"run_id": r.id, "site_id": r.siteID, "connector": r.fb.Connector, "render_js": js}}
	resp, tokens, err := r.fb.Fetch(ctx, raw, js)
	ev.Props["tokens"] = tokens
	if err != nil {
		r.fbFails++
		if ctx.Err() == nil {
			track.Fail(ctx, s.Log, ev, "fallback_failed", fmt.Errorf("crawl %s fallback via %s: %w", r.id, r.fb.Connector, err))
		}
		return nil
	}
	r.fbFails = 0
	ev.Props["http_status"], ev.Props["duration_ms"] = resp.Status, resp.Duration.Milliseconds()
	track.Emit(ctx, ev)
	if _, err := s.Pool.Exec(ctx, `UPDATE crawl_runs SET pages_fallback = pages_fallback + 1 WHERE id = $1`, r.id); err != nil {
		s.Log.Error("count fallback page", "run_id", r.id, "err", err)
	}
	return resp
}

// shellMarks are the empty mount points of common script frameworks.
var shellMarks = []string{`id="root"`, `id="app"`, `id="__next"`, `id="__nuxt"`, `id="___gatsby"`, `ng-version`, `data-reactroot`, `id="svelte"`}

func looksLikeShell(p *Page, body []byte) bool {
	if p.WordCount >= 150 {
		return false
	}
	lower := bytes.ToLower(body)
	for _, m := range shellMarks {
		if bytes.Contains(lower, []byte(m)) {
			return true
		}
	}
	// Few words and no links, but scripts: the content comes later.
	return len(p.Links) <= 1 && bytes.Count(lower, []byte("<script")) >= 1
}

// maxLinksKept is how many of a page's links are stored.
const maxLinksKept = 500

// keepPage stores what the audit needs of an HTML page: its on-page facts
// and its links.
func (s *Service) keepPage(ctx context.Context, r *run, id int64, pg *Page) error {
	hreflang, _ := json.Marshal(pg.Hreflang)
	if pg.Hreflang == nil {
		hreflang = []byte("[]")
	}
	h1, types := pg.H1, pg.SchemaTypes
	if h1 == nil {
		h1 = []string{}
	}
	if types == nil {
		types = []string{}
	}
	_, err := s.Pool.Exec(ctx, `
		UPDATE crawl_pages SET title = $2, title_count = $3, meta_description = $4, canonical = $5, canonical_count = $6,
			h1 = $7, lang = $8, hreflang = $9, schema_types = $10, word_count = $11, images = $12, images_no_alt = $13,
			content_hash = $14, viewport = $15, og_title = $16, og_image = $17, insecure_resources = $18
		WHERE id = $1`, id, pg.Title, pg.TitleCount, pg.Description, pg.Canonical, pg.CanonicalCount, h1, pg.Lang,
		hreflang, types, pg.WordCount, pg.Images, pg.ImagesNoAlt, pg.ContentHash, !pg.ViewportMissing, pg.OGTitle, pg.OGImage, pg.InsecureResources)
	if err != nil || len(pg.Links) == 0 {
		return err
	}
	links := pg.Links
	if len(links) > maxLinksKept {
		links = links[:maxLinksKept]
	}
	urls, anchors := make([]string, len(links)), make([]string, len(links))
	internal, nofollow := make([]bool, len(links)), make([]bool, len(links))
	for i, l := range links {
		urls[i], anchors[i], internal[i], nofollow[i] = l.URL, l.Anchor, SameSite(hostOfURL(l.URL), r.host), l.Nofollow
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO crawl_links (workspace_id, run_id, from_page, to_url, anchor, internal, nofollow)
		SELECT $1, $2, $3, u, a, i, n FROM unnest($4::text[], $5::text[], $6::bool[], $7::bool[]) AS t(u, a, i, n)`,
		r.ws, r.id, id, urls, anchors, internal, nofollow)
	return err
}

// retryAfter reads a Retry-After header, in seconds or as a date, capped.
func retryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return min(time.Duration(n)*time.Second, maxRetryAfter)
	}
	if t, err := http.ParseTime(v); err == nil {
		return min(max(time.Until(t), 0), maxRetryAfter)
	}
	return 0
}

// finish ends a run. status is done, stopped or failed; reason says why;
// code is the error code of a run that did not finish well.
func (s *Service) finish(ctx context.Context, r *run, ev track.Event, status, reason, code string) error {
	var fetched, blocked, failed, found, viaFallback int
	var result *Audit
	var fixes FixChanges
	var bytes int64
	var started *time.Time
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			UPDATE crawl_runs SET status = $2, reason = $3, error_code = $4, finished_at = now(), lease_until = NULL
			WHERE id = $1 AND status IN ('queued', 'running')
			RETURNING pages_fetched, pages_blocked, pages_failed, pages_found, pages_fallback, bytes, started_at`, r.id, status, reason, code).
			Scan(&fetched, &blocked, &failed, &found, &viaFallback, &bytes, &started)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // stopped meanwhile
		}
		if err != nil {
			return err
		}
		// What was never reached stays visible, marked as skipped.
		if _, err := tx.Exec(ctx, `UPDATE crawl_pages SET status = 'skipped', reason = $2 WHERE run_id = $1 AND status = 'queued'`, r.id, reason); err != nil {
			return err
		}
		// For webhooks: crawl.finished is the product event with the full
		// numbers, sent after this commits.
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: r.ws, ActorType: "system", Action: "crawl.done", TargetType: "crawl", TargetID: r.id,
			Data: map[string]any{"site_id": r.siteID, "status": status, "reason": reason, "pages_fetched": fetched, "pages_failed": failed}}); err != nil {
			return err
		}
		// How many pages link to each page, for orphans and weak pages.
		_, err = tx.Exec(ctx, `
			UPDATE crawl_pages p SET links_in = c.n
			FROM (SELECT to_url, count(DISTINCT from_page)::int AS n FROM crawl_links
			      WHERE run_id = $1 AND internal GROUP BY to_url) c
			WHERE p.run_id = $1 AND p.url = c.to_url`, r.id)
		if err != nil || fetched == 0 {
			return err
		}
		// The audit. A failure leaves the crawl finished without issues,
		// rather than not finished.
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		res, err := storeAudit(ctx, sp, r.ws, r.id)
		if err == nil {
			fixes, err = syncFixes(ctx, sp, r.ws, r.siteID, r.id, status == "done" && reason == "done")
		}
		if err == nil && s.AfterAudit != nil {
			err = s.AfterAudit(ctx, sp, r.ws, r.siteID, r.id)
		}
		if err != nil {
			_ = sp.Rollback(ctx)
			track.Fail(ctx, s.Log, track.Event{Name: "crawl.audit_failed", WorkspaceID: r.ws, ActorType: "system", Surface: "job",
				Props: map[string]any{"run_id": r.id, "site_id": r.siteID}}, "internal", fmt.Errorf("audit crawl %s: %w", r.id, err))
		} else {
			result = &res
			return sp.Commit(ctx)
		}
		return nil
	})
	if err != nil {
		return err
	}
	ev.Props["status"] = status
	ev.Props["reason"] = reason
	ev.Props["pages_fetched"] = fetched
	ev.Props["pages_blocked"] = blocked
	ev.Props["pages_failed"] = failed
	ev.Props["pages_found"] = found
	ev.Props["pages_fallback"] = viaFallback
	if result != nil {
		ev.Props["fixes_new"], ev.Props["fixes_reopened"], ev.Props["fixes_confirmed"] = fixes.New, fixes.Reopened, fixes.Confirmed
		ev.Props["issues_errors"], ev.Props["issues_warnings"], ev.Props["issues_notices"] = result.Errors, result.Warnings, result.Notices
		if result.Score != nil {
			ev.Props["health_score"] = *result.Score
		}
	}
	ev.Props["bytes"] = bytes
	ev.Props["obey_robots"] = r.obey
	ev.Props["verified"] = r.verifiedAt != nil
	if started != nil {
		ev.Props["duration_ms"] = time.Since(*started).Milliseconds()
	}
	switch {
	case code == "":
		track.Emit(ctx, ev)
	case code == "internal" || code == "site_errors":
		track.Fail(ctx, s.Log, ev, code, fmt.Errorf("crawl %s ended: %s", r.id, reason))
	default:
		ev.Outcome = track.Failure
		ev.ErrorCode = code
		track.Emit(ctx, ev)
	}
	return nil
}

// Run starts scheduled crawls until ctx ends: daily and weekly sites whose
// last run is that old, and the weekly look at each tracked competitor.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := s.Schedule(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.Log, track.Event{Name: "crawl.schedule_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		if err := s.scheduleGlances(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.Log, track.Event{Name: "seo_competitor.schedule_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
	}
}

// Schedule starts the crawls that are due now.
func (s *Service) Schedule(ctx context.Context) error {
	rows, err := s.Pool.Query(ctx, `
		SELECT s.id, s.workspace_id FROM seo_sites s
		WHERE s.schedule <> 'off' AND s.status = 'active'
		  AND NOT EXISTS (SELECT 1 FROM crawl_runs r WHERE r.site_id = s.id
		      AND (r.status IN ('queued', 'running')
		           OR r.created_at > now() - CASE s.schedule WHEN 'daily' THEN interval '23 hours' ELSE interval '167 hours' END))
		ORDER BY s.created_at LIMIT 100`)
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
	for _, d := range list {
		err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			id, err := StartRun(ctx, tx, d.ws, d.id, "schedule", nil)
			if errors.Is(err, ErrOptedOut) {
				// Kept as a stopped run, so the schedule waits a period
				// before asking again.
				_, err = tx.Exec(ctx, `
					INSERT INTO crawl_runs (workspace_id, site_id, host, status, trigger, reason, error_code, max_pages, delay_ms, obey_robots, finished_at)
					SELECT workspace_id, id, host, 'stopped', 'schedule', 'opted_out', 'crawl_opted_out', max_pages, crawl_delay_ms, true, now()
					FROM seo_sites WHERE id = $1`, d.id)
				return err
			}
			if errors.Is(err, ErrRunning) {
				return nil
			}
			if err != nil {
				return err
			}
			return audit.Record(ctx, tx, audit.Event{WorkspaceID: d.ws, ActorType: "system", Action: "crawl.started",
				TargetType: "crawl", TargetID: id, Data: map[string]any{"site_id": d.id, "trigger": "schedule"}})
		})
		if err != nil {
			track.Fail(ctx, s.Log, track.Event{Name: "crawl.schedule_failed", WorkspaceID: d.ws, ActorType: "system", Surface: "system",
				Props: map[string]any{"site_id": d.id}}, "internal", err)
		}
	}
	return nil
}
