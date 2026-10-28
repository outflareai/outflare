package crawl

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Simulated answers for test workspaces, which never reach a real site:
// every host is the same small site, with a page robots.txt hides, a
// redirect and a broken link, so a test crawl shows each kind of result.
type Simulated struct{}

var simPages = map[string]struct {
	status int
	body   string
	header string
}{
	"/robots.txt":  {200, "User-agent: *\nDisallow: /private/\nSitemap: {base}/sitemap.xml\n", "text/plain"},
	"/sitemap.xml": {200, `<?xml version="1.0"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>{base}/</loc></url><url><loc>{base}/pricing</loc></url><url><loc>{base}/blog/</loc></url></urlset>`, "application/xml"},
	"/": {200, `<html><head><title>Home</title></head><body><a href="/pricing">Pricing</a> <a href="/about">About</a> ` +
		`<a href="/blog/">Blog</a> <a href="/private/report">Report</a> <a href="/old-about">Old about</a> <a href="/missing">Missing</a> ` +
		`<a href="https://elsewhere.example/">Elsewhere</a></body></html>`, "text/html; charset=utf-8"},
	"/pricing":   {200, `<html><head><title>Pricing</title></head><body><a href="/">Home</a></body></html>`, "text/html"},
	"/about":     {200, `<html><head><title>About</title></head><body><a href="/">Home</a></body></html>`, "text/html"},
	"/blog/":     {200, `<html><head><title>Blog</title><meta name="robots" content="noindex"></head><body><a href="/">Home</a></body></html>`, "text/html"},
	"/old-about": {301, "", "text/html"},
	// For link building: a resource page that links to example.com, one
	// that doesn't, a broken link on both, and addresses to contact.
	"/resources": {200, `<html lang="en"><head><title>Hiking gear resources</title><meta name="author" content="Jo Rivers"></head><body>` +
		`<h1>Best hiking gear resources</h1><a href="https://www.example.com/guide">A guide</a> <a href="https://tools.example/old-tool">Old tool</a> ` +
		`<a href="/contact">Contact us</a> <a href="mailto:jo@{host}">Email Jo</a></body></html>`, "text/html"},
	"/links": {200, `<html lang="en"><head><title>Hiking pricing links</title></head><body><h1>Hiking pricing</h1>` +
		`<a href="https://tools.example/old-tool">Old tool</a> <a href="/contact">Contact us</a></body></html>`, "text/html"},
	"/contact": {200, `<html><head><title>Contact</title></head><body><a href="mailto:editor@{host}">Editor</a> <a href="mailto:someone@elsewhere.example">Elsewhere</a></body></html>`, "text/html"},
}

func (Simulated) Fetch(_ context.Context, raw string) (*Response, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	base := u.Scheme + "://" + u.Host
	h := http.Header{}
	p, ok := simPages[u.Path]
	if !ok {
		h.Set("Content-Type", "text/html")
		return &Response{URL: raw, Status: 404, Header: h, Body: []byte("<html><body>Not found</body></html>"), Duration: time.Millisecond}, nil
	}
	h.Set("Content-Type", p.header)
	if p.status == 301 {
		h.Set("Location", base+"/about")
	}
	body := []byte(strings.NewReplacer("{base}", base, "{host}", strings.TrimPrefix(u.Hostname(), "www.")).Replace(p.body))
	return &Response{URL: raw, Status: p.status, Header: h, Body: body, Duration: time.Millisecond}, nil
}
