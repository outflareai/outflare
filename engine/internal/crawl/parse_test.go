package crawl

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"
)

func TestRobots(t *testing.T) {
	body := `# a comment
User-agent: Googlebot
Disallow: /

User-agent: *
Disallow: /private/
Allow: /private/press/
Disallow: /*.pdf$
Disallow: /search?q=*
Crawl-delay: 5

User-agent: OutflareBot
User-agent: Other
Disallow: /admin
Allow: /admin/public
Disallow: /tmp/*/cache
Crawl-delay: 2.5

User-agent: outflarebot
Disallow: /drafts

Sitemap: https://example.com/sitemap.xml
`
	r := ParseRobots([]byte(body), Token)
	if r.Delay != 2500*time.Millisecond {
		t.Errorf("delay %v", r.Delay)
	}
	if !slices.Equal(r.Sitemaps, []string{"https://example.com/sitemap.xml"}) {
		t.Errorf("sitemaps %v", r.Sitemaps)
	}
	for path, want := range map[string]bool{
		"/":                  true,
		"/private/x":         true, // our group has no such rule; the * group does not apply
		"/admin":             false,
		"/admin/users":       false,
		"/admin/public/page": true, // longer Allow wins
		"/tmp/a/cache":       false,
		"/tmp/a/b/cache/x":   false,
		"/tmp/cache":         true,
		"/drafts/1":          false, // groups for the same agent merge
		"/robots.txt":        true,
	} {
		if got := r.Allowed(path); got != want {
			t.Errorf("Allowed(%q) = %v, want %v", path, got, want)
		}
	}

	// Without our own group, the * group applies.
	star := ParseRobots([]byte("User-agent: *\nDisallow: /private/\nAllow: /private/press/\nDisallow: /*.pdf$\nDisallow: /search?q=\nDisallow:\n"), Token)
	for path, want := range map[string]bool{
		"/private/a":       false,
		"/private/press/a": true,
		"/files/a.pdf":     false,
		"/files/a.pdf?x=1": true, // $ anchors the end
		"/search?q=shoes":  false,
		"/search":          true,
	} {
		if got := star.Allowed(path); got != want {
			t.Errorf("star Allowed(%q) = %v, want %v", path, got, want)
		}
	}
	// An empty file and an empty Disallow allow everything.
	if !ParseRobots(nil, Token).Allowed("/x") || !ParseRobots([]byte("User-agent: *\nDisallow:\n"), Token).Allowed("/x") {
		t.Error("empty robots.txt should allow all")
	}
	if !AllowAll().Allowed("/x") || DisallowAll().Allowed("/x") {
		t.Error("AllowAll / DisallowAll")
	}
}

func TestSitemap(t *testing.T) {
	set := `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc> https://example.com/ </loc><lastmod>2026-01-01</lastmod></url>
  <url><loc>https://example.com/pricing</loc></url>
</urlset>`
	pages, maps, err := ParseSitemap([]byte(set))
	if err != nil || !slices.Equal(pages, []string{"https://example.com/", "https://example.com/pricing"}) || len(maps) != 0 {
		t.Fatalf("urlset: %v %v %v", pages, maps, err)
	}
	index := `<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><sitemap><loc>https://example.com/a.xml.gz</loc></sitemap></sitemapindex>`
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte(index))
	zw.Close()
	pages, maps, err = ParseSitemap(gz.Bytes())
	if err != nil || len(pages) != 0 || !slices.Equal(maps, []string{"https://example.com/a.xml.gz"}) {
		t.Fatalf("gzipped index: %v %v %v", pages, maps, err)
	}
	pages, _, err = ParseSitemap([]byte("https://example.com/a\nnot a url\nhttps://example.com/b\n"))
	if err != nil || len(pages) != 2 {
		t.Fatalf("text sitemap: %v %v", pages, err)
	}
}

func TestParsePageAndNormalize(t *testing.T) {
	base, _ := url.Parse("https://Example.com/blog/post")
	p := ParsePage(base, []byte(`<html><head>
<meta name="ROBOTS" content="NoIndex">
<meta name="outflarebot" content="nofollow">
<meta name="outflare-site-verification" content="tok">
</head><body>
<a href="/a#top">A</a> <a href="b">B</a> <a href="/a">A again</a>
<a href="mailto:x@example.com">mail</a> <a href="javascript:void(0)">js</a> <a href="#x">frag</a>
<a href="https://other.example:443/x">other</a> <a href="//cdn.example.com">cdn</a>
</body></html>`))
	if !p.Noindex || !p.Nofollow || p.Meta["outflare-site-verification"] != "tok" {
		t.Fatalf("meta: %+v", p)
	}
	want := []string{"https://example.com/a", "https://example.com/blog/b", "https://other.example/x", "https://cdn.example.com/"}
	if !slices.Equal(linkURLs(p), want) {
		t.Fatalf("links %v", p.Links)
	}
	// <base> changes where relative links point.
	p = ParsePage(base, []byte(`<base href="https://example.com/docs/"><a href="x">x</a>`))
	if !slices.Equal(linkURLs(p), []string{"https://example.com/docs/x"}) {
		t.Fatalf("base: %v", p.Links)
	}
}

func TestCheckURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://example.com/":        true,
		"http://example.com/":         true,
		"https://localhost/":          false,
		"https://10.0.0.1/":           false,
		"https://169.254.169.254/":    false,
		"https://[::1]/":              false,
		"https://db.internal/":        false,
		"https://example.com:8443/":   false,
		"https://u:p@example.com/":    false,
		"ftp://example.com/":          false,
		"https://intranet/":           false,
		"https://93.184.215.14/page":  true,
		"https://printer.local/admin": false,
	} {
		if err := CheckURL(raw, false); (err == nil) != ok {
			t.Errorf("CheckURL(%q) = %v", raw, err)
		}
	}
	if err := CheckURL("http://127.0.0.1:8080/", true); err != nil {
		t.Errorf("private allowed: %v", err)
	}
}

func TestRetryAfter(t *testing.T) {
	if retryAfter("30") != 30*time.Second || retryAfter("99999") != maxRetryAfter || retryAfter("soon") != 0 {
		t.Error("retryAfter")
	}
}

func linkURLs(p Page) []string {
	var out []string
	for _, l := range p.Links {
		out = append(out, l.URL)
	}
	return out
}

func TestPageExtraction(t *testing.T) {
	base, _ := url.Parse("https://www.example.com/en/pricing")
	p := ParsePage(base, []byte(`<!doctype html><html lang="en-AU"><head>
<title>  Pricing |
 Example </title>
<meta name="description" content="Plans from $9.">
<meta name="viewport" content="width=device-width">
<link rel="canonical" href="/en/pricing?ref=x">
<link rel="alternate" hreflang="de" href="https://www.example.com/de/preise">
<link rel="alternate" hreflang="x-default" href="/">
<meta http-equiv="refresh" content="5; URL='/en/plans'">
<script type="application/ld+json">{"@context":"https://schema.org","@graph":[{"@type":"Organization"},{"@type":["Product","Thing"],"offers":{"@type":"Offer"}}]}</script>
<script>var title = "<title>not this</title>";</script>
<style>h1 { color: red }</style>
</head><body>
<svg><title>icon</title></svg>
<h1>Simple <b>pricing</b></h1>
<p>Pay monthly, cancel any time.</p>
<img src="a.png"> <img src="b.png" alt=""> <img src="c.png" alt="Chart">
<a href="/en/plans" rel="nofollow">See <em>plans</em></a>
<a href="https://partner.example/"><img src="p.png" alt="Partner"></a>
</body></html>`))
	if p.Title != "Pricing | Example" || p.TitleCount != 1 || p.Description != "Plans from $9." || p.Lang != "en-AU" {
		t.Errorf("head: %q %d %q %q", p.Title, p.TitleCount, p.Description, p.Lang)
	}
	if p.Canonical != "https://www.example.com/en/pricing?ref=x" || p.CanonicalCount != 1 || p.ViewportMissing {
		t.Errorf("canonical/viewport: %q %v", p.Canonical, p.ViewportMissing)
	}
	if len(p.Hreflang) != 2 || p.Hreflang[0] != (Hreflang{"de", "https://www.example.com/de/preise"}) || p.Hreflang[1].URL != "https://www.example.com/" {
		t.Errorf("hreflang: %v", p.Hreflang)
	}
	if p.MetaRefreshTo != "/en/plans" {
		t.Errorf("refresh: %q", p.MetaRefreshTo)
	}
	if !slices.Equal(p.SchemaTypes, []string{"Organization", "Product", "Thing", "Offer"}) {
		t.Errorf("schema: %v", p.SchemaTypes)
	}
	if !slices.Equal(p.H1, []string{"Simple pricing"}) || p.Images != 4 || p.ImagesNoAlt != 1 {
		t.Errorf("h1/images: %v %d %d", p.H1, p.Images, p.ImagesNoAlt)
	}
	if len(p.Links) != 2 || p.Links[0] != (Link{"https://www.example.com/en/plans", "See plans", true}) || p.Links[1].Anchor != "Partner" {
		t.Errorf("links: %+v", p.Links)
	}
	// Script and style text is not counted; the same words hash the same.
	if p.WordCount != 9 || p.ContentHash == "" {
		t.Logf("words: %v", p.WordCount)
		t.Errorf("words %d hash %q", p.WordCount, p.ContentHash)
	}
	q := ParsePage(base, []byte(`<title>Other</title><h1>Simple pricing</h1><p>Pay monthly; cancel ANY time.</p><a href="/x">See plans!</a>`))
	if q.ContentHash != p.ContentHash {
		t.Errorf("hash differs: %d words %q", q.WordCount, q.ContentHash)
	}
	if n, f := RobotsDirectives("otherbot: noindex, outflarebot: nofollow"); n || !f {
		t.Errorf("X-Robots-Tag: %v %v", n, f)
	}
	if clip("héllo", 2) != "h" {
		t.Errorf("clip: %q", clip("héllo", 2))
	}
}

func TestTechnicalBlock(t *testing.T) {
	page := func(status int, body string, h ...string) *Response {
		r := &Response{Status: status, Header: http.Header{}, Body: []byte(body)}
		for i := 0; i+1 < len(h); i += 2 {
			r.Header.Set(h[i], h[i+1])
		}
		return r
	}
	for name, c := range map[string]struct {
		r    *Response
		want bool
	}{
		"403":                {page(403, "Forbidden"), true},
		"cloudflare 503":     {page(503, `<html><head><title>Just a moment...</title>`), true},
		"challenge header":   {page(200, "", "Cf-Mitigated", "challenge"), true},
		"datadome 200":       {page(200, `<script src="https://ct.captcha-delivery.com/c.js">`), true},
		"plain 503":          {page(503, "Service Unavailable"), false},
		"404":                {page(404, "Just a moment..."), false},
		"ordinary page":      {page(200, "<title>Pricing</title>"), false},
		"rate limited (429)": {page(429, "slow down"), false},
	} {
		if got := technicalBlock(c.r); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestParsePageSocialAndMixedContent(t *testing.T) {
	base, _ := url.Parse("https://example.com/")
	p := ParsePage(base, []byte(`<html><head>
<meta property="og:title" content=" Pricing ">
<meta property="og:image" content="https://example.com/og.png">
<link rel="stylesheet" href="http://cdn.example.com/a.css">
<script src="http://cdn.example.com/a.js"></script>
<script src="https://cdn.example.com/b.js"></script>
</head><body><img src="http://example.com/x.png" alt="x"><noscript><img src="http://example.com/y.png"></noscript></body></html>`))
	if p.OGTitle != "Pricing" || p.OGImage != "https://example.com/og.png" || p.InsecureResources != 3 {
		t.Errorf("og %q %q, insecure %d", p.OGTitle, p.OGImage, p.InsecureResources)
	}
	plain, _ := url.Parse("http://example.com/")
	if q := ParsePage(plain, []byte(`<img src="http://example.com/x.png">`)); q.InsecureResources != 0 {
		t.Errorf("an http page has no mixed content: %d", q.InsecureResources)
	}
}

func TestSitemapLastmod(t *testing.T) {
	pages, _, err := ParseSitemapEntries([]byte(`<urlset><url><loc>https://a.example/x</loc><lastmod>2026-10-01</lastmod></url>` +
		`<url><lastmod>2026-09-01</lastmod><loc>https://a.example/y</loc></url><url><loc>https://a.example/z</loc></url></urlset>`))
	if err != nil || len(pages) != 3 || pages[0].Lastmod != "2026-10-01" || pages[1].Lastmod != "2026-09-01" || pages[2].Lastmod != "" {
		t.Fatalf("%v %+v", err, pages)
	}
}
