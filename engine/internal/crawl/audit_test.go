package crawl

import (
	"slices"
	"testing"
)

func TestTemplates(t *testing.T) {
	var pages []AuditPage
	for _, u := range []string{"/", "/pricing", "/blog/", "/blog/one", "/blog/two", "/blog/three",
		"/shop/42/reviews", "/shop/43/reviews", "/about/team", "/docs/a/b"} {
		pages = append(pages, AuditPage{URL: "https://example.com" + u})
	}
	got := Templates(pages)
	for u, want := range map[string]string{
		"/": "/", "/pricing": "/pricing", "/blog/one": "/blog/*", "/blog/three": "/blog/*", "/blog/": "/blog",
		"/shop/42/reviews": "/shop/*/reviews", "/about/team": "/about/team", "/docs/a/b": "/docs/a/b",
	} {
		if g := got["https://example.com"+u]; g != want {
			t.Errorf("%s: %q, want %q", u, g, want)
		}
	}
}

func TestRunAudit(t *testing.T) {
	ok, nf, moved := 200, 404, 301
	page := func(id int64, path string, mod func(*AuditPage)) AuditPage {
		p := AuditPage{ID: id, URL: "https://example.com" + path, Source: "link", Status: "fetched", ContentType: "text/html",
			HTTPStatus: &ok, Title: "A good page title", TitleCount: 1, Description: "A description of " + path, H1Count: 1,
			WordCount: 500, Lang: "en", Viewport: true, LinksIn: 1, ContentHash: "h" + path}
		if mod != nil {
			mod(&p)
		}
		return p
	}
	pages := []AuditPage{
		page(1, "/", nil),
		page(2, "/gone", func(p *AuditPage) { p.HTTPStatus = &nf }),
		page(3, "/old", func(p *AuditPage) { p.HTTPStatus = &moved; p.RedirectTo = "https://example.com/older" }),
		page(4, "/older", func(p *AuditPage) { p.HTTPStatus = &moved; p.RedirectTo = "https://example.com/" }),
		page(5, "/a", func(p *AuditPage) { p.Title = ""; p.H1Count = 2; p.WordCount = 80 }),
		page(6, "/b", func(p *AuditPage) { p.Description = "A description of /c"; p.ContentHash = "same" }),
		page(7, "/c", func(p *AuditPage) { p.ContentHash = "same"; p.Source = "sitemap"; p.LinksIn = 0; p.Noindex = false }),
		page(8, "/d", func(p *AuditPage) { p.JSShell = true; p.Canonical = "https://example.com/gone" }),
		page(9, "/e", func(p *AuditPage) { p.Noindex = true; p.Source = "sitemap"; p.Viewport = false; p.Lang = "" }),
		{ID: 10, URL: "https://example.com/private", Source: "sitemap", Status: "blocked", Reason: "robots"},
	}
	links := []AuditLink{{From: 1, To: "https://example.com/gone", Anchor: "Gone"}, {From: 1, To: "https://example.com/gone", Anchor: "Gone"}, {From: 1, To: "https://example.com/old", Anchor: "Old"}}
	a := RunAudit(pages, links)
	got := map[string][]int64{}
	for _, is := range a.Issues {
		got[is.Rule] = append(got[is.Rule], is.PageID)
	}
	for rule, want := range map[string][]int64{
		"page_broken": {2}, "redirect_chain": {3}, "link_broken": {1}, "link_to_redirect": {1},
		"title_missing": {5}, "h1_multiple": {5}, "content_thin": {5}, "description_duplicate": {6, 7},
		"content_duplicate": {6, 7}, "orphan_page": {7}, "js_only_content": {8}, "canonical_broken": {8},
		"noindex": {9}, "noindex_in_sitemap": {9}, "viewport_missing": {9}, "lang_missing": {9}, "blocked_in_sitemap": {10},
	} {
		g := got[rule]
		slices.Sort(g)
		if !slices.Equal(g, want) {
			t.Errorf("%s: pages %v, want %v", rule, g, want)
		}
	}
	if len(got["title_duplicate"]) == 0 {
		t.Error("pages 1, 3 and 4 share a title")
	}
	// Errors on pages 1 (broken link), 2, 5 and 8, of the 7 pages that are
	// HTML or broken (redirects and blocked pages do not count).
	if a.Score == nil || *a.Score != 100*(7-4)/7 {
		t.Errorf("score %v", *a.Score)
	}
	for _, r := range Rules {
		if r.Why == "" || r.Fix == "" || r.Title == "" || issueSev(r.Severity) {
			t.Errorf("rule %s is incomplete", r.ID)
		}
	}
}

func TestRunAuditMoreRules(t *testing.T) {
	ok, found, temp, gone := 200, 301, 302, 404
	page := func(id int64, u string, mod func(*AuditPage)) AuditPage {
		p := AuditPage{ID: id, URL: u, Source: "link", Status: "fetched", ContentType: "text/html", HTTPStatus: &ok, Depth: 1,
			Title: "A good page title", TitleCount: 1, Description: "A description of the page that is long enough to read well", H1Count: 1, H1: []string{"Heading"},
			WordCount: 500, Lang: "en", Viewport: true, LinksIn: 2, LinksOut: 5, ContentHash: "h" + u, Canonical: u, CanonicalCount: 1,
			SchemaTypes: 1, OGTitle: "Title", OGImage: "https://example.com/og.png"}
		if mod != nil {
			mod(&p)
		}
		return p
	}
	en, de := "https://example.com/en", "https://example.com/de"
	pages := []AuditPage{
		page(1, "https://example.com/", nil),
		page(2, "https://example.com/loop-a", func(p *AuditPage) {
			p.HTTPStatus = &found
			p.RedirectTo = "https://example.com/loop-b"
			p.Source = "sitemap"
		}),
		page(3, "https://example.com/loop-b", func(p *AuditPage) { p.HTTPStatus = &temp; p.RedirectTo = "https://example.com/loop-a" }),
		page(4, "https://example.com/to-gone", func(p *AuditPage) { p.HTTPStatus = &found; p.RedirectTo = "https://example.com/gone" }),
		page(5, "https://example.com/gone", func(p *AuditPage) { p.HTTPStatus = &gone; p.Source = "sitemap" }),
		page(6, "http://example.com/Plain_Page?x=1", func(p *AuditPage) { p.Canonical = ""; p.CanonicalCount = 0 }),
		page(7, "https://example.com/mixed", func(p *AuditPage) {
			p.InsecureResources = 2
			p.Nofollow = true
			p.H1 = []string{""}
			p.SchemaTypes = 0
			p.OGTitle, p.OGImage = "", ""
			p.LinksOut = 0
			p.LinksIn = 1
			p.Description = "Short"
		}),
		page(8, en, func(p *AuditPage) { p.Hreflang = []Hreflang{{"en", en}, {"de", de}, {"EN_GB", en}}; p.OGImage = "" }),
		page(9, de, func(p *AuditPage) { p.Hreflang = []Hreflang{{"de", de}} }),
		page(10, "https://example.com/copy", func(p *AuditPage) { p.Canonical = "https://example.com/hidden"; p.Source = "sitemap" }),
		page(11, "https://example.com/hidden", func(p *AuditPage) { p.Noindex = true }),
	}
	links := []AuditLink{
		{From: 1, To: "http://example.com/Plain_Page?x=1", Anchor: "Plain"},
		{From: 1, To: "https://example.com/mixed", Anchor: "", Nofollow: true},
	}
	a := RunAudit(pages, links)
	got := map[string][]int64{}
	for _, is := range a.Issues {
		got[is.Rule] = append(got[is.Rule], is.PageID)
	}
	for rule, want := range map[string][]int64{
		"redirect_loop": {2, 3}, "redirect_temporary": {3}, "redirect_to_broken": {4}, "sitemap_redirect": {2}, "sitemap_broken": {5},
		"http_page": {6}, "url_uppercase": {6}, "url_underscores": {6}, "url_parameters": {6}, "canonical_missing": {6},
		"link_to_http": {1}, "link_internal_nofollow": {1}, "link_no_text": {1},
		"mixed_content": {7}, "nofollow_page": {7}, "h1_empty": {7}, "schema_missing": {7}, "og_missing": {7}, "links_out_none": {7},
		"single_link_in": {7}, "description_too_short": {7}, "og_image_missing": {8},
		"hreflang_invalid": {8}, "hreflang_no_return": {8}, "hreflang_no_self": {}, "canonical_to_noindex": {10}, "sitemap_non_canonical": {10},
	} {
		g := got[rule]
		slices.Sort(g)
		if !slices.Equal(g, want) && !(len(want) == 0 && len(g) == 0) {
			t.Errorf("%s: pages %v, want %v", rule, g, want)
		}
	}
	if len(got["redirect_chain"]) != 0 {
		t.Errorf("a loop is not also a chain: %v", got["redirect_chain"])
	}
	if len(Rules) < 60 {
		t.Errorf("%d rules", len(Rules))
	}
	ids := map[string]bool{}
	for _, r := range Rules {
		if ids[r.ID] {
			t.Errorf("rule %s twice", r.ID)
		}
		ids[r.ID] = true
	}
}

func issueSev(s string) bool { return s != SevError && s != SevWarning && s != SevNotice }
