package crawl

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// A site's pages are found through its sitemap (and an index), only its
// own and never files or lists; read once each, politely, with the words
// kept in paragraphs and the menus left out.
func TestSitePages(t *testing.T) {
	f := &mapFetcher{pages: map[string]string{
		"/robots.txt":  "User-agent: *\nDisallow: /private/\nCrawl-delay: 2\n",
		"/sitemap.xml": `<sitemapindex><sitemap><loc>https://northpeak.example/pages.xml</loc></sitemap></sitemapindex>`,
		"/pages.xml": `<urlset><url><loc>https://northpeak.example/help/refunds</loc></url><url><loc>https://www.northpeak.example/pricing?x=1</loc></url>` +
			`<url><loc>https://northpeak.example/private/notes</loc></url><url><loc>https://northpeak.example/guide.pdf</loc></url>` +
			`<url><loc>https://northpeak.example/tag/news</loc></url><url><loc>https://elsewhere.example/a</loc></url>` +
			`<url><loc>https://northpeak.example/hidden</loc></url><url><loc>https://northpeak.example/old</loc></url></urlset>`,
		"/help/refunds": `<html><head><title>Refunds</title></head><body><nav><a href="/">Home</a> Menu</nav>` +
			`<main><h1>Refunds</h1><p>We refund any plan within 30 days.</p><ul><li>Open Settings</li><li>Choose Billing</li><li>Click Cancel</li></ul>` +
			`<script>track()</script></main><footer>Copyright</footer></body></html>`,
		"/pricing": `<html><head><title>Pricing</title></head><body><p>Starter is $49 a month.</p></body></html>`,
		"/hidden":  `<html><head><title>Hidden</title><meta name="robots" content="noindex"></head><body><p>Secret</p></body></html>`,
	}}
	var waits []time.Duration
	wait := func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	urls, err := SitePages(context.Background(), f, wait, "https://northpeak.example/", 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://northpeak.example/help/refunds", "https://www.northpeak.example/pricing", "https://northpeak.example/private/notes",
		"https://northpeak.example/hidden", "https://northpeak.example/old"}
	if !slices.Equal(urls, want) {
		t.Fatalf("pages: %v", urls)
	}
	if two, _ := SitePages(context.Background(), f, wait, "https://northpeak.example/", 2); len(two) != 2 {
		t.Fatalf("max: %v", two)
	}

	pages, err := ReadSitePages(context.Background(), f, wait, urls, 1000)
	if err != nil {
		t.Fatal(err)
	}
	// The private page is disallowed, the hidden one is noindex, the old one
	// is missing.
	if len(pages) != 2 || pages[0].Title != "Refunds" || pages[1].URL != "https://www.northpeak.example/pricing" {
		t.Fatalf("read: %+v", pages)
	}
	if got := pages[0].Text; got != "# Refunds\n\nWe refund any plan within 30 days.\n\n- Open Settings\n- Choose Billing\n- Click Cancel" {
		t.Fatalf("text: %q", got)
	}
	if strings.Contains(strings.Join(f.got, " "), "/private/notes") {
		t.Fatalf("read a disallowed page: %v", f.got)
	}
	for _, d := range waits {
		if d < 2*time.Second {
			t.Fatalf("didn't keep the crawl delay: %v", waits)
		}
	}
}

// Without a sitemap, the home page's links are the pages.
func TestSitePagesFromLinks(t *testing.T) {
	f := &mapFetcher{pages: map[string]string{
		"/robots.txt": "User-agent: *\nAllow: /\n",
		"/":           `<html><body><a href="/faq">FAQ</a> <a href="https://elsewhere.example/">Out</a> <a href="/logo.png">Logo</a></body></html>`,
		"/faq":        `<html><body><a href="/faq/billing">Billing</a></body></html>`,
	}}
	urls, err := SitePages(context.Background(), f, func(context.Context, time.Duration) error { return nil }, "https://northpeak.example", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(urls, []string{"https://northpeak.example/", "https://northpeak.example/faq", "https://northpeak.example/faq/billing"}) {
		t.Fatalf("pages: %v", urls)
	}
}
