package api_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// helpSite is a business's website: a sitemap with a long help article, a
// thin page, a page robots.txt keeps out, and one that changes.
type helpSite struct {
	mu      sync.Mutex
	refunds string
	// noExport takes the export page off the site.
	noExport bool
}

func (h *helpSite) Fetch(_ context.Context, raw string) (*crawl.Response, error) {
	u, _ := url.Parse(raw)
	h.mu.Lock()
	refunds, noExport := h.refunds, h.noExport
	h.mu.Unlock()
	pages := map[string]string{
		"/robots.txt":  "User-agent: *\nDisallow: /internal/\n",
		"/sitemap.xml": `<urlset><url><loc>https://northpeak.example/help/refunds</loc></url><url><loc>https://northpeak.example/contact</loc></url><url><loc>https://northpeak.example/internal/runbook</loc></url><url><loc>https://northpeak.example/help/export</loc></url></urlset>`,
		"/help/refunds": `<html><head><title>Refunds and cancellations</title></head><body><nav>Home Pricing</nav><main><h1>Refunds</h1><p>` + refunds +
			`</p><p>Annual plans are refunded pro rata after 30 days. Refunds go back to the card that paid and take five to ten working days to arrive.</p></main></body></html>`,
		"/help/export": `<html><head><title>Export a chart</title></head><body><article><h1>Export a chart</h1><p>Open the chart, then choose More and Export CSV. ` +
			`The file has one row per day and one column per series, and it downloads straight away. Exports include every filter you set on the chart.</p></article></body></html>`,
		"/contact":          `<html><head><title>Contact</title></head><body><p>Email us.</p></body></html>`,
		"/internal/runbook": `<html><head><title>Runbook</title></head><body><p>Private notes that are long enough to import if anyone read them, which nobody should.</p></body></html>`,
	}
	h2 := http.Header{"Content-Type": []string{"text/html"}}
	if noExport {
		delete(pages, "/help/export")
		pages["/sitemap.xml"] = strings.Replace(pages["/sitemap.xml"], `<url><loc>https://northpeak.example/help/export</loc></url>`, "", 1)
	}
	body, ok := pages[u.Path]
	if !ok {
		return &crawl.Response{URL: raw, Status: 404, Header: h2}, nil
	}
	return &crawl.Response{URL: raw, Status: 200, Header: h2, Body: []byte(body)}, nil
}

// The business's website becomes knowledge: one article per page worth
// reading, in a Website folder, used by AI answers; a second run updates
// what changed instead of adding it twice.
func TestKnowledgeWebsiteImport(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	q := jobs.New(e.pool, slog.New(slog.DiscardHandler))
	q.PollInterval = 20 * time.Millisecond
	e.srv.RegisterJobs(q)
	run, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = q.Run(run, 2); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	site := &helpSite{refunds: "We refund any plan within 30 days of payment, no questions asked."}
	e.crawl.Fetcher = site
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ib := base + "/knowledge/website-imports"

	if l := owner.must(200, "GET", ib, nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("imports before: %v", l)
	}
	owner.must(400, "POST", ib, map[string]any{}) // no website anywhere yet
	owner.must(400, "POST", ib, map[string]any{"url": "northpeak.example", "max_pages": 0})
	owner.must(400, "POST", ib, map[string]any{"url": "northpeak.example", "visibility": "everyone"})
	owner.must(404, "POST", ib, map[string]any{"url": "northpeak.example", "brand": "nobrand"})
	imp := owner.must(202, "POST", ib, map[string]any{"url": "northpeak.example/help"})
	if imp["url"] != "https://northpeak.example/" || imp["status"] != "queued" || imp["visibility"] != "customers_ai" || imp["publish"] != true || imp["collection_id"] == nil {
		t.Fatalf("started: %v", imp)
	}
	wait := func(id string) map[string]any {
		t.Helper()
		return waitFor(t, owner, ib+"/"+id, func(m map[string]any) bool { return m["status"] == "done" || m["status"] == "failed" })
	}
	if d := wait(imp["id"].(string)); d["status"] != "done" || d["pages_found"] != 4.0 || d["pages_added"] != 2.0 || d["pages_updated"] != 0.0 || d["pages_skipped"] != 2.0 {
		t.Fatalf("done: %v", d)
	}
	items := owner.must(200, "GET", base+"/knowledge/items?collection_id="+imp["collection_id"].(string), nil)["data"].([]any)
	if len(items) != 2 {
		t.Fatalf("items: %v", items)
	}
	var refunds map[string]any
	for _, it := range items {
		if it.(map[string]any)["title"] == "Refunds and cancellations" {
			refunds = owner.must(200, "GET", base+"/knowledge/items/"+it.(map[string]any)["id"].(string), nil)
		}
	}
	if refunds == nil || refunds["visibility"] != "customers_ai" || refunds["status"] != "published" ||
		!strings.Contains(refunds["body"].(string), "# Refunds\n\nWe refund any plan within 30 days") || strings.Contains(refunds["body"].(string), "Home Pricing") ||
		!strings.HasSuffix(refunds["body"].(string), "From https://northpeak.example/help/refunds") {
		t.Fatalf("refunds article: %v", refunds)
	}
	// AI answers find it.
	if res := owner.must(200, "GET", base+"/knowledge/passages?q=refund+annual+plan&audience=customers", nil); len(res["data"].([]any)) == 0 {
		t.Fatalf("not searchable: %v", res)
	}

	// The page changes; running again updates it and leaves the rest.
	site.mu.Lock()
	site.refunds = "We refund any plan within 60 days of payment, no questions asked."
	site.mu.Unlock()
	again := owner.must(202, "POST", ib, map[string]any{"url": "https://northpeak.example"})
	if d := wait(again["id"].(string)); d["pages_added"] != 0.0 || d["pages_updated"] != 1.0 || d["pages_skipped"] != 3.0 || again["collection_id"] != imp["collection_id"] {
		t.Fatalf("again: %v", d)
	}
	if b := owner.must(200, "GET", base+"/knowledge/items/"+refunds["id"].(string), nil); !strings.Contains(b["body"].(string), "60 days") || b["version"] != 2.0 {
		t.Fatalf("updated: %v", b)
	}
	if l := owner.must(200, "GET", ib, nil)["data"].([]any); len(l) != 2 {
		t.Fatalf("imports: %v", l)
	}
	owner.must(404, "GET", ib+"/00000000-0000-0000-0000-000000000000", nil)

	if len(named(got(), "knowledge.website_imported")) != 2 || len(named(got(), "knowledge.website_import_started")) != 2 {
		t.Fatalf("events: %d", len(got()))
	}

	// Every day the site is read again on its own. A page that left the
	// site unpublishes its article; it is published again when it's back.
	recheck := func() map[string]any {
		t.Helper()
		if _, err := e.pool.Exec(context.Background(), `UPDATE knowledge_website_imports SET finished_at = now() - interval '25 hours'`); err != nil {
			t.Fatal(err)
		}
		if n, err := e.srv.QueueWebsiteRechecks(context.Background()); err != nil || n != 1 {
			t.Fatalf("rechecks: %d %v", n, err)
		}
		if n, err := e.srv.QueueWebsiteRechecks(context.Background()); err != nil || n != 0 {
			t.Fatalf("rechecked twice: %d %v", n, err)
		}
		l := owner.must(200, "GET", ib, nil)["data"].([]any)
		return wait(l[0].(map[string]any)["id"].(string))
	}
	var export string
	for _, it := range items {
		if it.(map[string]any)["title"] == "Export a chart" {
			export = it.(map[string]any)["id"].(string)
		}
	}
	site.mu.Lock()
	site.noExport = true
	site.mu.Unlock()
	if d := recheck(); d["scheduled"] != true || d["pages_found"] != 3.0 || d["pages_removed"] != 1.0 || d["pages_updated"] != 0.0 {
		t.Fatalf("recheck: %v", d)
	}
	if it := owner.must(200, "GET", base+"/knowledge/items/"+export, nil); it["status"] != "draft" {
		t.Fatalf("gone page still published: %v", it)
	}
	site.mu.Lock()
	site.noExport = false
	site.mu.Unlock()
	if d := recheck(); d["pages_removed"] != 0.0 || d["pages_updated"] != 1.0 {
		t.Fatalf("back: %v", d)
	}
	if it := owner.must(200, "GET", base+"/knowledge/items/"+export, nil); it["status"] != "published" {
		t.Fatalf("returned page not published: %v", it)
	}
}

// The sitemaps robots.txt names are read too, and a list is complete only
// when every sitemap was read and nothing was cut short.
func TestListSiteSitemaps(t *testing.T) {
	site := siteMaps{}
	l, err := crawl.ListSite(context.Background(), site, func(context.Context, time.Duration) error { return nil }, "https://northpeak.example/", 10)
	if err != nil || len(l.Pages) != 3 || !l.Complete {
		t.Fatalf("list: %v %v", l, err)
	}
	l, _ = crawl.ListSite(context.Background(), site, func(context.Context, time.Duration) error { return nil }, "https://northpeak.example/", 2)
	if len(l.Pages) != 2 || l.Complete {
		t.Fatalf("cut short: %v", l)
	}
}

type siteMaps struct{}

func (siteMaps) Fetch(_ context.Context, raw string) (*crawl.Response, error) {
	u, _ := url.Parse(raw)
	pages := map[string]string{
		"/robots.txt":        "User-agent: *\nSitemap: https://northpeak.example/maps/index.xml\n",
		"/maps/index.xml":    `<sitemapindex><sitemap><loc>https://northpeak.example/maps/help.xml</loc></sitemap></sitemapindex>`,
		"/maps/help.xml":     `<urlset><url><loc>https://northpeak.example/help/a</loc></url><url><loc>https://northpeak.example/help/b</loc></url></urlset>`,
		"/sitemap_index.xml": `<urlset><url><loc>https://northpeak.example/pricing</loc></url></urlset>`,
	}
	body, ok := pages[u.Path]
	if !ok {
		return &crawl.Response{URL: raw, Status: 404}, nil
	}
	return &crawl.Response{URL: raw, Status: 200, Body: []byte(body)}, nil
}
