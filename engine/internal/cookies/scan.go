package cookies

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/net/html"
	"golang.org/x/net/publicsuffix"

	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// JobScan scans a site key's site for cookies and scripts.
const JobScan = "cookies.scan"

const (
	// maxScanPages is how many pages one scan reads: the home page of each
	// of the key's sites, then the pages visits most often land on.
	maxScanPages = 20
	// scanEvery is how often a site with the banner on is scanned.
	scanEvery = 7 * 24 * time.Hour
	// pause between two requests to the same site.
	scanPause = time.Second
)

// Service runs cookie scans.
type Service struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	// Fetcher reaches real sites; nil builds a crawl.HTTPFetcher.
	Fetcher crawl.Fetcher
	// AllowPrivate lets scans reach private addresses (development, tests).
	AllowPrivate bool
	// Wait overrides the pause between requests, for tests.
	Wait func(ctx context.Context, d time.Duration) error
}

// Register adds the scan job's handler.
func (s *Service) Register(q *jobs.Queue) { q.Handle(JobScan, s.scan) }

type scanPayload struct {
	ScanID string `json:"scan_id"`
}

// StartScan queues a scan of the key's site, unless one is already
// waiting or running, and returns its id.
func StartScan(ctx context.Context, tx pgx.Tx, ws, keyID, trigger, actorID string) (id string, started bool, err error) {
	err = tx.QueryRow(ctx, `SELECT id::text FROM cookie_scans WHERE site_key_id = $1 AND status IN ('queued', 'running') LIMIT 1`, keyID).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	var actor *string
	if actorID != "" {
		actor = &actorID
	}
	if err := tx.QueryRow(ctx, `INSERT INTO cookie_scans (workspace_id, site_key_id, trigger, created_by) VALUES ($1, $2, $3, $4) RETURNING id::text`,
		ws, keyID, trigger, actor).Scan(&id); err != nil {
		return "", false, err
	}
	_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobScan, WorkspaceID: &ws, Payload: scanPayload{ScanID: id}, MaxAttempts: 3,
		IdempotencyKey: JobScan + ":" + id})
	return id, err == nil, err
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

func (s *Service) fetcher() crawl.Fetcher {
	if s.Fetcher == nil {
		s.Fetcher = crawl.NewHTTPFetcher(s.AllowPrivate)
	}
	return s.Fetcher
}

// scan reads the site's pages and records what they hold: cookies their
// servers set, and the third-party scripts, embeds and pixels in their
// HTML (inline snippets included, by the addresses they load). Scripts a
// tag manager adds later, and cookies scripts set, come from visitors'
// browsers instead (Report).
func (s *Service) scan(ctx context.Context, job jobs.Job) error {
	start := time.Now()
	var p scanPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
	}
	var ws, keyID, name string
	var origins []string
	var website *string
	err := s.Pool.QueryRow(ctx, `UPDATE cookie_scans c SET status = 'running', started_at = now()
		FROM site_keys k LEFT JOIN brands b ON b.id = k.brand_id
		WHERE c.id = $1 AND k.id = c.site_key_id AND k.revoked_at IS NULL
		RETURNING c.workspace_id::text, k.id::text, k.name, k.origins, b.website`, p.ScanID).Scan(&ws, &keyID, &name, &origins, &website)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	starts := startPages(origins, website)
	if len(starts) == 0 {
		return s.finish(ctx, p.ScanID, ws, keyID, nil, 0, "the site key has no domain to scan: add the site's domain to its origins", start)
	}
	more, err := landingPages(ctx, s.Pool, keyID, starts)
	if err != nil {
		return err
	}
	pages := append(starts, more...)
	if len(pages) > maxScanPages {
		pages = pages[:maxScanPages]
	}
	f := s.fetcher()
	robots := map[string]*crawl.Robots{}
	var seen []Seen
	read := 0
	for i, page := range pages {
		u, err := url.Parse(page)
		if err != nil {
			continue
		}
		if crawl.CheckURL(page, s.AllowPrivate) != nil {
			continue
		}
		r, ok := robots[u.Host]
		if !ok {
			r = crawl.AllowAll()
			if resp, err := f.Fetch(ctx, u.Scheme+"://"+u.Host+"/robots.txt"); err == nil && resp.Status == http.StatusOK {
				r = crawl.ParseRobots(resp.Body, crawl.Token)
			}
			robots[u.Host] = r
		}
		if !r.Allowed(u.EscapedPath()) {
			continue
		}
		if i > 0 {
			if err := s.wait(ctx, scanPause); err != nil {
				return err
			}
		}
		found, ok := s.read(ctx, f, page)
		if !ok {
			continue
		}
		read++
		seen = append(seen, found...)
	}
	if read == 0 {
		return s.finish(ctx, p.ScanID, ws, keyID, nil, 0, "no page of the site could be read: check that it is up and that robots.txt lets OutflareBot in", start)
	}
	return s.finish(ctx, p.ScanID, ws, keyID, seen, read, "", start)
}

// read fetches a page, following a few redirects on the same site, and
// lists what it holds.
func (s *Service) read(ctx context.Context, f crawl.Fetcher, page string) ([]Seen, bool) {
	var seen []Seen
	for hop := 0; hop < 4; hop++ {
		resp, err := f.Fetch(ctx, page)
		if err != nil {
			return seen, false
		}
		u, _ := url.Parse(resp.URL)
		if u == nil {
			u, _ = url.Parse(page)
		}
		path := cleanPath(u.EscapedPath())
		seen = append(seen, setCookies(resp.Header, u.Hostname(), path)...)
		if resp.Status >= 300 && resp.Status < 400 {
			next, err := u.Parse(resp.Header.Get("Location"))
			if err != nil || registrable(next.Hostname()) != registrable(u.Hostname()) {
				return seen, false
			}
			page = next.String()
			continue
		}
		if resp.Status != http.StatusOK || !strings.Contains(resp.ContentType(), "html") {
			return seen, false
		}
		return append(seen, inPage(u, resp.Body, path)...), true
	}
	return seen, false
}

// setCookies lists the cookies a response sets, with how long each is
// kept.
func setCookies(h http.Header, host, page string) []Seen {
	var out []Seen
	for _, c := range (&http.Response{Header: h}).Cookies() {
		if c.Name == "" {
			continue
		}
		domain := strings.TrimPrefix(c.Domain, ".")
		if domain == "" {
			domain = host
		}
		out = append(out, Seen{Kind: KindCookie, Name: clip(c.Name), Host: domain, Source: "crawl", Page: page, Retention: retention(c)})
	}
	return out
}

func retention(c *http.Cookie) string {
	d := time.Duration(c.MaxAge) * time.Second
	if c.MaxAge <= 0 && !c.Expires.IsZero() {
		d = time.Until(c.Expires)
	}
	switch {
	case c.MaxAge < 0:
		return ""
	case d <= 0:
		return "Session"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes()+0.5)) + " minutes"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours()+0.5)) + " hours"
	case d < 60*24*time.Hour:
		return strconv.Itoa(int(d.Hours()/24+0.5)) + " days"
	case d < 700*24*time.Hour:
		return strconv.Itoa(int(d.Hours()/24/30.4+0.5)) + " months"
	}
	return strconv.Itoa(int(d.Hours()/24/365+0.5)) + " years"
}

// urlInScript finds addresses an inline snippet loads, such as
// "https://connect.facebook.net/en_US/fbevents.js" or "//bat.bing.com/bat.js".
var urlInScript = regexp.MustCompile(`(?:https?:)?//[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}/[^\s"'\\)]*`)

// inPage lists the third-party scripts, embeds and pixels in a page's
// HTML. The site's own files are left out: what they set shows up as
// cookies.
func inPage(base *url.URL, body []byte, page string) []Seen {
	var out []Seen
	own := registrable(base.Hostname())
	add := func(kind, raw string, outflare bool) {
		u, err := base.Parse(strings.TrimSpace(raw))
		if err != nil {
			return
		}
		name := ScriptName(u.String())
		if name == "" {
			return
		}
		if outflare {
			out = append(out, Seen{Kind: KindScript, Name: name, Host: u.Hostname(), Source: "crawl", Page: page, Vendor: "outflare"})
			return
		}
		if registrable(u.Hostname()) == own && Lookup(kind, name).Vendor == "" {
			return
		}
		out = append(out, Seen{Kind: kind, Name: name, Host: u.Hostname(), Source: "crawl", Page: page})
	}
	z := html.NewTokenizer(bytes.NewReader(body))
	inScript := false
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return out
		case html.TextToken:
			if inScript {
				for _, m := range urlInScript.FindAllString(string(z.Text()), 20) {
					if strings.HasPrefix(m, "//") {
						m = "https:" + m
					}
					add(KindScript, m, false)
				}
			}
		case html.EndTagToken:
			if name, _ := z.TagName(); string(name) == "script" {
				inScript = false
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			attrs := map[string]string{}
			for hasAttr {
				var k, v []byte
				k, v, hasAttr = z.TagAttr()
				attrs[string(k)] = string(v)
			}
			switch string(name) {
			case "script":
				inScript = tt == html.StartTagToken && attrs["src"] == ""
				if src := attrs["src"]; src != "" {
					// The website script, wherever it is served from.
					add(KindScript, src, strings.HasPrefix(attrs["data-key"], "ofk_"))
				}
			case "iframe":
				if src := attrs["src"]; src != "" {
					add(KindIframe, src, false)
				} else if src := attrs["data-src"]; src != "" {
					add(KindIframe, src, false)
				}
			case "img":
				if src := attrs["src"]; src != "" {
					if u, err := base.Parse(src); err == nil && Lookup(KindPixel, ScriptName(u.String())).Vendor != "" {
						add(KindPixel, src, false)
					}
				}
			}
		}
	}
}

// startPages are the home pages of a key's sites: each origin's, or the
// brand's website when the key takes any origin.
func startPages(origins []string, website *string) []string {
	var out []string
	for _, o := range origins {
		if scheme, host, ok := strings.Cut(o, "://*."); ok {
			out = append(out, scheme+"://"+host+"/")
		} else {
			out = append(out, strings.TrimRight(o, "/")+"/")
		}
	}
	if len(out) == 0 && website != nil && *website != "" {
		out = append(out, "https://"+*website+"/")
	}
	return out
}

// landingPages are the pages visits to the key's sites landed on most in
// the last 30 days, other than the home pages.
func landingPages(ctx context.Context, q *pgxpool.Pool, keyID string, starts []string) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT split_part(split_part(landing_url, '#', 1), '?', 1) AS u FROM site_sessions
		WHERE key_id = $1 AND started_at > now() - interval '30 days' AND landing_url LIKE 'http%'
		GROUP BY 1 ORDER BY count(*) DESC LIMIT $2`, keyID, maxScanPages)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	var out []string
	for _, u := range list {
		if !contains(starts, u) && !contains(out, u) {
			out = append(out, u)
		}
	}
	return out, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// finish records a scan's findings and outcome, and publishes the policy.
func (s *Service) finish(ctx context.Context, scanID, ws, keyID string, seen []Seen, pages int, problem string, start time.Time) error {
	outcome := "ok"
	if problem != "" {
		outcome = "failed"
	}
	var added int
	var pub Published
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		if added, err = Observe(ctx, tx, ws, keyID, seen); err != nil {
			return err
		}
		status := "done"
		if problem != "" {
			status = "failed"
		}
		if _, err := tx.Exec(ctx, `UPDATE cookie_scans SET status = $2, pages = $3, found = $4, added = $5, error = $6, finished_at = now() WHERE id = $1`,
			scanID, status, pages, len(seen), added, problem); err != nil {
			return err
		}
		if problem == "" {
			if pub, err = Publish(ctx, tx, ws, keyID, Who{ActorType: "system"}, time.Now()); err != nil {
				return err
			}
		}
		return nil
	})
	metrics.CookieScanned(outcome, pages, len(seen), time.Since(start))
	if err != nil {
		return err
	}
	ev := track.Event{Name: "cookie_scan.finished", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"pages": pages, "found": len(seen), "added": added, "policy_version": pub.Version, "reasked": pub.Reasked}}
	if problem != "" {
		track.Fail(ctx, s.Log, ev, "scan_failed", errors.New(problem))
		return nil
	}
	track.Emit(ctx, ev)
	return nil
}

// Run queues a scan every week for each site key whose consent banner is
// on, until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if err := s.Schedule(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.Log, track.Event{Name: "cookie_scan.schedule_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Schedule queues the scans that are due.
func (s *Service) Schedule(ctx context.Context) error {
	rows, err := s.Pool.Query(ctx, `
		SELECT b.workspace_id::text, b.site_key_id::text FROM consent_banners b JOIN site_keys k ON k.id = b.site_key_id AND k.revoked_at IS NULL
		WHERE (b.config->>'enabled')::boolean
		  AND NOT EXISTS (SELECT 1 FROM cookie_scans c WHERE c.site_key_id = b.site_key_id
		      AND (c.status IN ('queued', 'running') OR c.created_at > now() - $1::interval))
		LIMIT 100`, fmt.Sprintf("%d seconds", int(scanEvery.Seconds())))
	if err != nil {
		return err
	}
	type due struct{ ws, key string }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (due, error) {
		var d due
		return d, r.Scan(&d.ws, &d.key)
	})
	if err != nil {
		return err
	}
	for _, d := range list {
		if err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			_, _, err := StartScan(ctx, tx, d.ws, d.key, "schedule", "")
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

// registrable is a host's registrable domain (www.northpeak.example is
// northpeak.example), or the host itself when it has none.
func registrable(host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if d, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		return d
	}
	return host
}

func clip(s string) string {
	if len(s) > 300 {
		return s[:300]
	}
	return s
}
