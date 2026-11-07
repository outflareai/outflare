package crawl

import (
	"net/url"
	"slices"
	"testing"
	"time"
)

func TestAISignals(t *testing.T) {
	base, _ := url.Parse("https://acme.example/guide")
	body := `<html><head>
<title>Proxy guide</title>
<meta name="robots" content="max-snippet:20, max-image-preview:large">
<meta name="googlebot" content="nosnippet">
<meta name="bingbot" content="noarchive">
<meta name="author" content="Ana Example">
<meta property="article:modified_time" content="2026-09-01T10:00:00Z">
<link rel="alternate" type="text/markdown" href="/guide.md">
<script type="application/ld+json">{"@context":"https://schema.org","@graph":[
 {"@type":"BlogPosting","headline":"The proxy guide","datePublished":"2025-01-02"},
 {"@type":"Product","name":"Residential proxies","offers":{"@type":"Offer","price":"49.00"}},
 {"@type":"FAQPage","mainEntity":[{"@type":"Question","name":"Is it legal?"},{"@type":"Question","name":"Do you offer refunds?"}]}]}</script>
</head><body><main>
<h1>The proxy guide</h1>
<p>Residential proxies cost $49 a month.</p>
<h2>What is a proxy?</h2>
<p>A proxy is a server that sends your requests on to websites for you.</p>
<h2>How fast is it?</h2>
<p>Fast.</p>
<h3>Pricing</h3>
<h2>Is it legal?</h2>
<p data-nosnippet>Short.</p>
<p>Yes, using a proxy is legal in most countries for most purposes.</p>
<h2>Can I cancel?</h2>
</main></body></html>`
	p := ParsePage(base, []byte(body))
	a := p.AI
	if !a.NoSnippet || a.MaxSnippet == nil || *a.MaxSnippet != 20 || !a.NoArchive || a.DataNoSnippet != 1 {
		t.Fatalf("snippet rules: %+v", a)
	}
	if a.Headings != 5 || a.QuestionHeadings != 4 {
		t.Fatalf("headings %d, questions %d", a.Headings, a.QuestionHeadings)
	}
	// "How fast" has only a one-word answer and "Can I cancel?" none;
	// "Is it legal?" is answered by its second paragraph.
	if !slices.Equal(a.Unanswered, []string{"How fast is it?", "Can I cancel?"}) {
		t.Fatalf("unanswered: %v", a.Unanswered)
	}
	if !a.Article || a.Author != "Ana Example" || a.Published != "2025-01-02" || a.Modified != "2026-09-01T10:00:00Z" {
		t.Fatalf("article: %+v", a)
	}
	if a.Markdown != "https://acme.example/guide.md" {
		t.Fatalf("markdown: %q", a.Markdown)
	}
	// The headline, name, price and first FAQ are on the page; the
	// refunds question isn't.
	if !slices.Equal(a.SchemaMismatch, []string{"Product name Residential proxies", "FAQ question Do you offer refunds?"}) {
		// "Residential proxies" is on the page too, lowercase or not.
		if !slices.Equal(a.SchemaMismatch, []string{"FAQ question Do you offer refunds?"}) {
			t.Fatalf("mismatch: %v", a.SchemaMismatch)
		}
	}

	// X-Robots-Tag counts too, but not for another agent.
	var s AISignals
	s.snippetDirectives("otherbot: nosnippet, max-snippet:-1")
	if s.NoSnippet || s.MaxSnippet != nil {
		t.Fatalf("other agent: %+v", s)
	}
	s.snippetDirectives("googlebot: nosnippet")
	if !s.NoSnippet {
		t.Fatal("googlebot: nosnippet")
	}

	for h, want := range map[string]bool{"What is a proxy?": true, "How it works": true, "Pricing": false, "Why": false, "Plans and prices?": true} {
		if isQuestion(h) != want {
			t.Errorf("isQuestion(%q) = %v", h, !want)
		}
	}
}

func TestAIRules(t *testing.T) {
	defer func(f func() time.Time) { auditNow = f }(auditNow)
	auditNow = func() time.Time { return time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC) }
	ok := 200
	page := func(id int64, ai AISignals) AuditPage {
		return AuditPage{ID: id, URL: "https://acme.example/p" + string(rune('0'+id)), Status: "fetched", ContentType: "text/html",
			HTTPStatus: &ok, Title: "A page about proxies", Description: "A page about proxies for teams and people.", H1Count: 1,
			H1: []string{"Proxies"}, WordCount: 500, Viewport: true, Canonical: "https://acme.example/p", CanonicalCount: 1, AI: ai}
	}
	twenty := 20
	pages := []AuditPage{
		page(1, AISignals{NoSnippet: true, MaxSnippet: &twenty}),
		page(2, AISignals{MaxSnippet: &twenty, NoArchive: true, Unanswered: []string{"Can I cancel?"}}),
		page(3, AISignals{Article: true}),
		page(4, AISignals{Article: true, Author: "Ana", Published: "2023-05-01", Modified: "2024-01-10"}),
		page(5, AISignals{Article: true, Author: "Ana", Modified: "2026-10-01T09:00:00Z", SchemaMismatch: []string{"Product price 49"}}),
		page(6, AISignals{NoSnippet: true}),
	}
	pages[5].Noindex = true
	got := map[string][]int64{}
	for _, is := range RunAudit(pages, nil).Issues {
		got[is.Rule] = append(got[is.Rule], is.PageID)
	}
	for rule, want := range map[string][]int64{
		"ai_snippet_blocked": {1}, "ai_snippet_short": {2}, "ai_copilot_limited": {2}, "ai_question_unanswered": {2},
		"ai_no_author": {3}, "ai_no_date": {3}, "ai_stale": {4}, "schema_mismatch": {5},
	} {
		if !slices.Equal(got[rule], want) {
			t.Errorf("%s: pages %v, want %v", rule, got[rule], want)
		}
	}
	for _, id := range []string{"ai_snippet_blocked", "ai_snippet_short", "ai_copilot_limited", "ai_question_unanswered", "ai_no_date", "ai_stale", "ai_no_author", "schema_mismatch"} {
		if _, ok := RuleFor(id); !ok {
			t.Errorf("rule %s not in the catalog", id)
		}
	}
}

func TestUsageSignals(t *testing.T) {
	body := "# Cloudflare managed\nUser-Agent: *\nContent-Signal: search=yes, ai-train=no # comment\nContent-Usage: train-ai=n\ncontent-signal: search=yes, ai-train=no\nDisallow:\n"
	got := UsageSignals(body)
	if !slices.Equal(got, []string{"Content-Signal: search=yes, ai-train=no", "Content-Usage: train-ai=n", "content-signal: search=yes, ai-train=no"}) {
		t.Fatalf("signals: %q", got)
	}
}

func TestAIBotCatalog(t *testing.T) {
	seen := map[string]bool{}
	for _, b := range AIBots {
		if seen[b.Token] {
			t.Errorf("%s twice", b.Token)
		}
		seen[b.Token] = true
		if b.Feeds == "" || (b.Purpose != "search" && b.Purpose != "user" && b.Purpose != "training") {
			t.Errorf("%s: %+v", b.Token, b)
		}
	}
	by := map[string]AIBot{}
	for _, b := range AIBots {
		by[b.Token] = b
	}
	// As each company documents them.
	if by["Amazonbot"].Purpose != "training" || by["Amzn-SearchBot"].Purpose != "search" || !by["Googlebot"].SearchEngine ||
		by["Google-Extended"].Purpose != "training" || !by["ChatGPT-User"].MayIgnoreRobots || by["meta-webindexer"].Purpose != "search" {
		t.Fatalf("catalog: %+v", by)
	}
}
