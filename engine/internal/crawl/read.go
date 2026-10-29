package crawl

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Reading one page of someone else's site, for link building: whether it
// is up, what it is, who wrote it, where it links and which addresses it
// shows. Like a glance, it obeys robots.txt, pauses before each request and
// never follows links on its own; checking a page's outbound links reads
// each linked URL once, obeying each site's robots.txt.

// ErrNotPage is a read of a URL that is not an http(s) page.
var ErrNotPage = errors.New("not a web page")

// Read is what reading one page saw.
type Read struct {
	URL string
	// FinalURL is where same-site redirects led; Status is its HTTP status
	// (0 when it never answered). Redirect is where it pointed off the
	// site, if it did.
	FinalURL    string
	Status      int
	Redirect    string
	Title       string
	Description string
	Author      string
	// Published is when the page says it was published, as written
	// (article:published_time, or its JSON-LD article), or "".
	Published string
	H1        []string
	Lang      string
	Noindex   bool
	WordCount int
	Links     []Link
	// Emails are the addresses its mailto links show, lowercase.
	Emails []string
	// Text is the start of its visible text, for the AI page reader.
	Text string
}

// readText is how much of a page's text a read keeps.
const readText = 6000

var mailto = regexp.MustCompile(`(?i)href\s*=\s*["']mailto:([^"'?\s]+)`)
var emailShape = regexp.MustCompile(`^[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}$`)
var tags = regexp.MustCompile(`(?is)<(script|style|noscript|svg)[^>]*>.*?</(script|style|noscript|svg)>|<[^>]+>`)
var spaces = regexp.MustCompile(`\s+`)

// politeSite reads a host's robots.txt once and tells whether a URL there
// may be read, and how long to pause first.
type politeSite struct {
	rb    *Robots
	delay time.Duration
}

func readSite(ctx context.Context, f Fetcher, u *url.URL) (*politeSite, error) {
	status, body := readRobots(ctx, f, u.Scheme+"://"+u.Host+"/robots.txt")
	var rb *Robots
	switch {
	case status >= 200 && status < 300:
		rb = ParseRobots([]byte(body), Token)
	case status >= 400 && status < 500:
		rb = AllowAll()
	default:
		return nil, fmt.Errorf("%w: robots.txt could not be read", ErrBlocked)
	}
	return &politeSite{rb: rb, delay: max(glanceDelay, min(rb.Delay, maxCrawlDelay))}, nil
}

func (p *politeSite) allowed(u *url.URL) bool {
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	return p.rb.Allowed(path)
}

func sameSite(a, b *url.URL) bool {
	return strings.TrimPrefix(strings.ToLower(a.Hostname()), "www.") == strings.TrimPrefix(strings.ToLower(b.Hostname()), "www.")
}

// ReadPage reads one page politely: robots.txt first (ErrBlocked when it
// forbids the page), then the page, following up to three redirects on the
// same site. A redirect elsewhere is reported, not followed.
func ReadPage(ctx context.Context, f Fetcher, wait func(context.Context, time.Duration) error, raw string) (*Read, error) {
	start, err := url.Parse(raw)
	if err != nil || (start.Scheme != "http" && start.Scheme != "https") || start.Host == "" {
		return nil, ErrNotPage
	}
	site, err := readSite(ctx, f, start)
	if err != nil {
		return nil, err
	}
	read := &Read{URL: raw, FinalURL: raw}
	cur := start
	for range 4 {
		if !site.allowed(cur) {
			return nil, ErrBlocked
		}
		if err := wait(ctx, site.delay); err != nil {
			return nil, err
		}
		resp, err := f.Fetch(ctx, cur.String())
		if err != nil {
			return nil, err
		}
		read.FinalURL, read.Status = cur.String(), resp.Status
		if resp.Status >= 300 && resp.Status < 400 {
			next, ok := Normalize(cur, resp.Header.Get("Location"))
			nu, err := url.Parse(next)
			if !ok || err != nil {
				return read, nil
			}
			if !sameSite(start, nu) {
				read.Redirect = next
				return read, nil
			}
			cur = nu
			continue
		}
		if resp.Status == 200 && readable(resp.ContentType()) {
			pg := ParsePage(cur, resp.Body)
			read.Title, read.Description, read.Author = clip(pg.Title, 300), clip(pg.Description, 500), clip(pg.Meta["author"], 200)
			// News sites name the author in other tags; a profile URL isn't a
			// name.
			for _, a := range []string{pg.Meta["article:author"], pg.Meta["parsely-author"], pg.Meta["sailthru.author"], pg.Meta["dc.creator"], pg.ArticleAuthor} {
				if read.Author != "" {
					break
				}
				if a = strings.TrimSpace(a); !strings.Contains(a, "://") {
					read.Author = clip(a, 200)
				}
			}
			read.Published = clip(strings.TrimSpace(pg.Meta["article:published_time"]), 40)
			if read.Published == "" {
				read.Published = pg.ArticlePublished
			}
			read.H1, read.Lang, read.Noindex, read.WordCount, read.Links = pg.H1, pg.Lang, pg.Noindex, pg.WordCount, pg.Links
			seen := map[string]bool{}
			for _, m := range mailto.FindAllSubmatch(resp.Body, -1) {
				e := strings.ToLower(strings.TrimSpace(string(m[1])))
				if emailShape.MatchString(e) && !seen[e] && len(read.Emails) < 20 {
					seen[e] = true
					read.Emails = append(read.Emails, e)
				}
			}
			text := strings.TrimSpace(spaces.ReplaceAllString(tags.ReplaceAllString(string(resp.Body), " "), " "))
			read.Text = clip(text, readText)
		}
		return read, nil
	}
	return read, nil
}

// LinkStatus is what checking one linked URL found.
type LinkStatus struct {
	URL string `json:"url"`
	// Status is the HTTP status after up to three redirects; 0 when the
	// site never answered (no DNS, refused, timed out).
	Status int `json:"status"`
	// Broken: gone for good (404 or 410) or the site does not answer.
	Broken bool `json:"broken"`
	// Skipped: robots.txt does not allow reading it, so it wasn't checked.
	Skipped bool `json:"skipped,omitempty"`
}

// CheckLinks reads each URL once, a few hosts at a time and one request at
// a time per host, obeying each host's robots.txt, and says which are
// broken. A site that does not answer at all counts as broken.
func CheckLinks(ctx context.Context, f Fetcher, wait func(context.Context, time.Duration) error, urls []string) []LinkStatus {
	out := make([]LinkStatus, len(urls))
	byHost := map[string][]int{}
	var hosts []string
	for i, raw := range urls {
		out[i].URL = raw
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			out[i].Skipped = true
			continue
		}
		h := strings.ToLower(u.Host)
		if _, ok := byHost[h]; !ok {
			hosts = append(hosts, h)
		}
		byHost[h] = append(byHost[h], i)
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, h := range hosts {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx []int) {
			defer wg.Done()
			defer func() { <-sem }()
			first, _ := url.Parse(urls[idx[0]])
			site, err := readSite(ctx, f, first)
			if errors.Is(err, ErrBlocked) && unreachable(ctx, f, first) {
				for _, i := range idx {
					out[i].Broken = true
				}
				return
			}
			for _, i := range idx {
				u, _ := url.Parse(urls[i])
				if err != nil || !site.allowed(u) {
					out[i].Skipped = true
					continue
				}
				if wait(ctx, site.delay) != nil {
					return
				}
				out[i].Status = finalStatus(ctx, f, u)
				out[i].Broken = out[i].Status == 0 || out[i].Status == 404 || out[i].Status == 410
			}
		}(byHost[h])
	}
	wg.Wait()
	return out
}

// unreachable reports whether a site does not answer at all.
func unreachable(ctx context.Context, f Fetcher, u *url.URL) bool {
	_, err := f.Fetch(ctx, u.Scheme+"://"+u.Host+"/")
	return err != nil && ctx.Err() == nil
}

// finalStatus follows up to three redirects and returns the last status,
// 0 when the site never answered.
func finalStatus(ctx context.Context, f Fetcher, u *url.URL) int {
	cur := u
	for range 4 {
		resp, err := f.Fetch(ctx, cur.String())
		if err != nil {
			return 0
		}
		if resp.Status < 300 || resp.Status >= 400 {
			return resp.Status
		}
		next, ok := Normalize(cur, resp.Header.Get("Location"))
		nu, err := url.Parse(next)
		if !ok || err != nil {
			return resp.Status
		}
		cur = nu
	}
	return 310
}

// ReadFetcher is the fetcher and pause a read uses: the simulated site for
// test workspaces, real sites otherwise.
func (s *Service) ReadFetcher(test bool) (Fetcher, func(context.Context, time.Duration) error) {
	return s.fetcher(test), s.wait
}
