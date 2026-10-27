package crawl

import (
	"bytes"
	"context"
	"net/http"
	"time"

	"github.com/outflareai/outflare/engine/internal/connectors"
)

// A crawl of a site the workspace has proved it owns can read a page
// through a scraping connector (Geonode) when the site's firewall or a bot
// challenge blocks OutflareBot. Only pages robots.txt allows are ever
// asked for: a fallback gets past a technical block, never the site's
// rules.

// Fallback finds a workspace's page fetcher for blocked pages.
type Fallback interface {
	// For returns the fetcher and its connector id, or nil when the
	// workspace has none connected.
	For(ctx context.Context, ws string) (*FallbackFetcher, error)
}

// FallbackFetcher reads pages through one connection.
type FallbackFetcher struct {
	Connector string
	open      *connectors.Opened
	fetch     connectors.PageFetcher
}

// Fetch reads a page; Tokens is what it cost on the provider's plan.
func (f *FallbackFetcher) Fetch(ctx context.Context, raw string, renderJS bool) (*Response, float64, error) {
	start := time.Now()
	p, err := f.fetch.FetchPage(ctx, f.open.Client, f.open.Creds, raw, renderJS)
	if err != nil {
		return nil, p.Cost.Units, err
	}
	body := []byte(p.HTML)
	out := &Response{URL: raw, Status: p.Status, Header: http.Header{"Content-Type": {"text/html"}}, Body: body,
		Duration: time.Since(start)}
	if len(out.Body) > maxBodyBytes {
		out.Body, out.Truncated = out.Body[:maxBodyBytes], true
	}
	return out, p.Cost.Units, nil
}

// ConnectorFallback finds the workspace's connected PageFetcher.
type ConnectorFallback struct {
	Store *connectors.Store
	Pool  connectors.Querier
}

func (c *ConnectorFallback) For(ctx context.Context, ws string) (*FallbackFetcher, error) {
	list, err := c.Store.List(ctx, c.Pool, ws, "")
	if err != nil {
		return nil, err
	}
	for _, i := range list {
		if i.Status != connectors.StatusConnected {
			continue
		}
		d, ok := c.Store.Registry.Definition(i.Connector)
		if !ok || !d.Has(connectors.CapCrawlFetch) {
			continue
		}
		o, err := c.Store.Open(ctx, c.Pool, ws, i.ID)
		if err != nil {
			return nil, err
		}
		pf, ok := o.Connector.(connectors.PageFetcher)
		if !ok {
			continue
		}
		return &FallbackFetcher{Connector: i.Connector, open: o, fetch: pf}, nil
	}
	return nil, nil
}

// challengeMarks are what firewalls and bot challenges put in the page
// they answer with instead of the site's own.
var challengeMarks = [][]byte{
	[]byte("cf-browser-verification"), []byte("challenge-platform"), []byte("<title>just a moment...</title>"),
	[]byte("attention required! | cloudflare"), []byte("_incapsula_resource"), []byte("px-captcha"),
	[]byte("captcha-delivery.com"), []byte("/_sec/cp_challenge/"), []byte("<title>access denied</title>"),
}

// technicalBlock reports whether an answer is a firewall's or a bot
// challenge's rather than the site's.
func technicalBlock(resp *Response) bool {
	if resp.Header.Get("Cf-Mitigated") == "challenge" {
		return true
	}
	if resp.Status == http.StatusForbidden {
		return true
	}
	switch resp.Status {
	case http.StatusOK, http.StatusAccepted, http.StatusTooManyRequests, http.StatusServiceUnavailable:
	default:
		return false
	}
	head := resp.Body
	if len(head) > 64<<10 {
		head = head[:64<<10]
	}
	head = bytes.ToLower(head)
	for _, m := range challengeMarks {
		if bytes.Contains(head, m) {
			return true
		}
	}
	return false
}
