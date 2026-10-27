package crawl

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"
)

type mapFetcher struct {
	pages map[string]string
	got   []string
}

func (m *mapFetcher) Fetch(_ context.Context, raw string) (*Response, error) {
	m.got = append(m.got, raw)
	u, _ := url.Parse(raw)
	h := http.Header{}
	body, ok := m.pages[u.Path]
	if !ok {
		h.Set("Content-Type", "text/html")
		return &Response{URL: raw, Status: 404, Header: h}, nil
	}
	h.Set("Content-Type", "text/html")
	return &Response{URL: raw, Status: 200, Header: h, Body: []byte(body)}, nil
}

func TestGlance(t *testing.T) {
	f := &mapFetcher{pages: map[string]string{
		"/robots.txt": "User-agent: *\nDisallow: /private/\nCrawl-delay: 3\nSitemap: https://rival.example/sitemap.xml\nSitemap: https://other.example/sitemap.xml\n",
		"/sitemap.xml": `<sitemapindex><sitemap><loc>https://rival.example/blog.xml</loc></sitemap>` +
			`<sitemap><loc>https://rival.example/private/hidden.xml</loc></sitemap></sitemapindex>`,
		"/blog.xml": `<urlset><url><loc>https://rival.example/blog/a</loc><lastmod>2026-10-01</lastmod></url>` +
			`<url><loc>https://www.rival.example/blog/b</loc></url><url><loc>https://elsewhere.example/x</loc></url></urlset>`,
		"/": `<html><head><title>Rival</title><meta name="description" content="We sell things"></head><body><a href="/pricing">Pricing</a></body></html>`,
	}}
	var waits []time.Duration
	wait := func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	look, err := Glance(context.Background(), f, wait, "rival.example")
	if err != nil {
		t.Fatal(err)
	}
	if look.Title != "Rival" || look.Description != "We sell things" {
		t.Fatalf("home = %q %q", look.Title, look.Description)
	}
	var urls []string
	for _, p := range look.Pages {
		urls = append(urls, p.URL)
	}
	if !slices.Equal(urls, []string{"https://rival.example/blog/a", "https://www.rival.example/blog/b"}) || look.Pages[0].Lastmod != "2026-10-01" {
		t.Fatalf("pages = %+v", look.Pages)
	}
	// robots.txt, the home page and two sitemaps; never the other host's
	// sitemap, the disallowed one or a linked page.
	want := []string{"https://rival.example/robots.txt", "https://rival.example/", "https://rival.example/sitemap.xml", "https://rival.example/blog.xml"}
	if !slices.Equal(f.got, want) {
		t.Fatalf("fetched %v", f.got)
	}
	for _, d := range waits {
		if d != 3*time.Second {
			t.Fatalf("waited %v, want the Crawl-delay", d)
		}
	}

	f = &mapFetcher{pages: map[string]string{"/robots.txt": "User-agent: OutflareBot\nDisallow: /\n"}}
	if _, err := Glance(context.Background(), f, wait, "rival.example"); !errors.Is(err, ErrBlocked) || len(f.got) != 1 {
		t.Fatalf("blocked: err %v, fetched %v", err, f.got)
	}
}
