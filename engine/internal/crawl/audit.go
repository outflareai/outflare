package crawl

import (
	"context"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// The audit turns what a crawl stored into issues: one for each rule a
// page breaks. It runs when the crawl ends, from the database alone, so a
// rule can be added and older crawls keep the issues they had.

// Severities, worst first.
const (
	SevError   = "error"
	SevWarning = "warning"
	SevNotice  = "notice"
)

// Rule is one check the audit makes.
type Rule struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	// Category groups rules: indexability, links, content, metadata,
	// performance, security, social.
	Category string `json:"category"`
	Title    string `json:"title"`
	// Why it matters and how to fix it, in plain words.
	Why string `json:"why"`
	Fix string `json:"fix"`
}

// Rules is the catalog, in the order a report lists them. Ids are a
// promise: never rename or remove one, add new ones.
var Rules = []Rule{
	{"page_broken", SevError, "links", "Page not found", "The page answers 4xx, so visitors and search engines that follow a link to it reach nothing.", "Restore the page, redirect it (301) to its closest match, or remove the links to it."},
	{"page_server_error", SevError, "indexability", "Server error", "The page answers 5xx or not at all. Search engines drop pages that keep failing.", "Check the server's logs for this URL."},
	{"link_broken", SevError, "links", "Links to a broken page", "The page links to a page of the site that is not found or fails.", "Point the link at a working page, or remove it."},
	{"redirect_chain", SevWarning, "links", "Redirect chain", "The redirect leads to another redirect. Each hop is slower and loses some of the link's value.", "Redirect straight to the final page."},
	{"link_to_redirect", SevNotice, "links", "Links to a redirect", "The page links to a URL that redirects.", "Link to the final URL instead."},
	{"canonical_broken", SevError, "indexability", "Canonical points to a broken page", "The page names a canonical URL that is not found, fails or redirects, so search engines may ignore it.", "Set the canonical to the page's working final URL."},
	{"canonical_multiple", SevWarning, "indexability", "More than one canonical", "With several rel=canonical links, search engines may ignore all of them.", "Keep one canonical link in the head."},
	{"canonical_elsewhere", SevNotice, "indexability", "Canonical points to another page", "The page asks search engines to index another URL instead. Fine for duplicates, a problem if it is not one.", "Check this page is meant to be a copy of its canonical."},
	{"noindex", SevNotice, "indexability", "Not indexed (noindex)", "The page asks search engines not to index it.", "Remove noindex if the page should be found in search."},
	{"noindex_in_sitemap", SevWarning, "indexability", "Noindex page in the sitemap", "The sitemap asks search engines to index a page that says not to.", "Remove the page from the sitemap, or remove noindex."},
	{"blocked_in_sitemap", SevWarning, "indexability", "Sitemap page blocked by robots.txt", "The sitemap lists a page robots.txt keeps crawlers away from.", "Remove it from the sitemap, or allow it in robots.txt."},
	{"js_only_content", SevWarning, "indexability", "Content only appears with JavaScript", "Most of the page's words are added by scripts. Crawlers that do not run JavaScript, and AI assistants among them, see little.", "Render the content on the server, or pre-render the page."},
	{"title_missing", SevError, "metadata", "Missing title", "The title is the headline search results show. Without one, search engines make one up.", "Add a unique <title> that says what the page is."},
	{"title_multiple", SevWarning, "metadata", "More than one title", "The page has several <title> tags; search engines pick one.", "Keep one <title> in the head."},
	{"title_duplicate", SevWarning, "metadata", "Duplicate title", "Several pages share this title, so search results cannot tell them apart.", "Give each page its own title."},
	{"title_too_long", SevNotice, "metadata", "Title too long", "Titles over about 60 characters are cut off in search results.", "Shorten the title, with the important words first."},
	{"title_too_short", SevNotice, "metadata", "Title too short", "A title under 10 characters says little about the page.", "Describe the page in the title."},
	{"description_missing", SevWarning, "metadata", "Missing meta description", "Search results show the description under the title; without one, a snippet is taken from the page.", "Add a meta description that makes people want to click."},
	{"description_duplicate", SevNotice, "metadata", "Duplicate meta description", "Several pages share this description.", "Write a description for each page."},
	{"description_too_long", SevNotice, "metadata", "Meta description too long", "Descriptions over about 160 characters are cut off.", "Shorten it to about 155 characters."},
	{"h1_missing", SevWarning, "content", "Missing h1", "The h1 is the page's main heading, for readers and search engines.", "Add one h1 that says what the page is about."},
	{"h1_multiple", SevNotice, "content", "More than one h1", "Several h1 headings blur what the page is about.", "Keep one h1 and make the others h2."},
	{"content_thin", SevWarning, "content", "Thin content", "The page has fewer than 200 words, which rarely ranks or answers a question.", "Add useful text, or merge the page with a related one."},
	{"content_duplicate", SevWarning, "content", "Duplicate content", "Another page has the same words, so they compete in search.", "Merge the pages, or set a canonical on the copy."},
	{"images_no_alt", SevNotice, "content", "Images without alt text", "Screen readers and image search read the alt text.", "Describe each image in its alt attribute (alt=\"\" for decoration)."},
	{"lang_missing", SevNotice, "content", "No language set", "The html element has no lang, so browsers and search engines guess the language.", "Add lang to the html element, like lang=\"en\"."},
	{"viewport_missing", SevWarning, "performance", "Not mobile friendly", "Without a viewport meta tag, phones show the desktop page shrunk.", "Add <meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">."},
	{"orphan_page", SevWarning, "links", "Orphan page", "The page is in the sitemap but no page of the site links to it, so it gets no link value.", "Link to it from related pages."},
	{"deep_page", SevNotice, "links", "Deep page", "The page is more than 3 clicks from the start page, which search engines treat as less important.", "Link to it from higher up."},
	{"page_slow", SevNotice, "performance", "Slow page", "The server took over 1.5 seconds to send the page.", "Cache the page or speed up the server."},
	{"page_large", SevNotice, "performance", "Large page", "The page's HTML is over 2 MB.", "Remove what the page does not need in its HTML."},
	// Added later, grouped as above.
	{"redirect_loop", SevError, "links", "Redirect loop", "The redirects lead back to a page already passed, so the page never loads.", "Point the redirect at the final page so the chain ends."},
	{"redirect_to_broken", SevError, "links", "Redirect to a broken page", "The redirect ends on a page that is not found or fails.", "Redirect to a working page, or restore the target."},
	{"redirect_temporary", SevNotice, "links", "Temporary redirect", "A 302, 303 or 307 redirect tells search engines the move is temporary, so they keep the old URL.", "Use a 301 (or 308) for a page that has moved for good."},
	{"link_internal_nofollow", SevNotice, "links", "Nofollow on internal links", "The page marks links to its own site nofollow, so they pass no value to those pages.", "Remove rel=\"nofollow\" from links to your own pages."},
	{"link_no_text", SevNotice, "links", "Links without text", "Links with no text or image alt tell readers, screen readers and search engines nothing about where they go.", "Give each link words that describe its page, or alt text on its image."},
	{"link_to_http", SevWarning, "links", "Links to an insecure page", "A secure page links to a page of the site on plain http.", "Link to the https URL."},
	{"links_out_none", SevNotice, "links", "No links out", "The page links nowhere, a dead end for readers and crawlers.", "Link to related pages."},
	{"links_out_many", SevNotice, "links", "Too many links", "The page has over 300 links, which spreads its value thin and buries the important ones.", "Keep the links that matter."},
	{"single_link_in", SevNotice, "links", "Only one link to the page", "Only one page of the site links here, so it is easy to miss.", "Link to it from more related pages."},
	{"url_too_long", SevNotice, "links", "Long URL", "URLs over 115 characters are hard to read and share, and get cut off in search results.", "Use a shorter path."},
	{"url_uppercase", SevNotice, "links", "Capital letters in the URL", "Paths are case sensitive, so capitals invite duplicates and broken links.", "Use lowercase paths, and redirect the old ones."},
	{"url_underscores", SevNotice, "links", "Underscores in the URL", "Search engines read hyphens as word breaks but not underscores.", "Use hyphens between words."},
	{"url_parameters", SevNotice, "links", "Indexable URL with parameters", "A page reachable with query parameters can be indexed many times over.", "Link to the clean URL, and set a canonical on the variants."},
	{"nofollow_page", SevNotice, "indexability", "Links not followed (nofollow)", "The page asks search engines not to follow any of its links.", "Remove nofollow from the robots meta tag unless that is intended."},
	{"canonical_missing", SevNotice, "indexability", "No canonical", "Without a canonical, search engines guess which of a page's URLs to index.", "Add <link rel=\"canonical\"> with the page's own URL."},
	{"canonical_to_noindex", SevWarning, "indexability", "Canonical points to a noindex page", "The page sends its value to a page that says not to index it, so neither may be found.", "Point the canonical at an indexable page, or remove noindex there."},
	{"sitemap_redirect", SevWarning, "indexability", "Sitemap page redirects", "The sitemap lists a URL that redirects, which wastes crawls.", "List the final URL in the sitemap."},
	{"sitemap_broken", SevWarning, "indexability", "Broken page in the sitemap", "The sitemap lists a page that is not found or fails.", "Remove it from the sitemap, or restore the page."},
	{"sitemap_non_canonical", SevWarning, "indexability", "Sitemap page is not canonical", "The sitemap lists a page whose canonical points to another URL.", "List only canonical URLs in the sitemap."},
	{"hreflang_invalid", SevWarning, "indexability", "Invalid hreflang code", "Search engines ignore an hreflang whose language or region code is wrong.", "Use a language code (en), optionally with a region (en-gb), or x-default."},
	{"hreflang_no_self", SevNotice, "indexability", "Hreflang without the page itself", "A page with hreflang alternates should list itself too.", "Add an hreflang for the page's own language and URL."},
	{"hreflang_broken", SevWarning, "indexability", "Hreflang points to a broken page", "An alternate language version is not found, fails or redirects.", "Point the hreflang at the working final URL."},
	{"hreflang_no_return", SevWarning, "indexability", "Hreflang not confirmed", "The alternate page does not link back, so search engines may ignore the pair.", "List each page in the other's hreflang tags."},
	{"description_too_short", SevNotice, "metadata", "Meta description too short", "A description under 50 characters says little in search results.", "Describe the page in about 120 to 155 characters."},
	{"h1_empty", SevWarning, "content", "Empty h1", "The page's main heading has no text.", "Put the page's main topic in the h1."},
	{"schema_missing", SevNotice, "content", "No structured data", "Structured data (JSON-LD) helps search engines and AI assistants understand the page and show rich results.", "Add schema.org JSON-LD that fits the page, such as Article, Product or Organization."},
	{"http_page", SevWarning, "security", "Page not on HTTPS", "The page is served over plain http. Browsers mark it not secure and search engines prefer https.", "Serve the site on https and redirect http to it."},
	{"mixed_content", SevWarning, "security", "Insecure resources on a secure page", "A https page loads scripts, styles or images over http, which browsers block or warn about.", "Load every resource over https."},
	{"og_missing", SevNotice, "social", "No Open Graph tags", "Without og:title and og:image, links to the page shared in chats and social apps show a bare URL.", "Add og:title, og:description and og:image meta tags."},
	{"ai_search_blocked", SevWarning, "ai_visibility", "Hidden from AI search", "robots.txt keeps AI search crawlers (such as OAI-SearchBot, Claude-SearchBot or PerplexityBot) from the page, so AI answers can't read or cite it.", "Allow these crawlers in robots.txt. Training crawlers such as GPTBot and ClaudeBot are separate tokens and can stay blocked."},
	{"llms_txt_missing", SevNotice, "ai_visibility", "No llms.txt", "An llms.txt at the site's root gives AI assistants a short, plain guide to the site and its key pages.", "Publish /llms.txt: a Markdown file with the site's name, a one-line summary and links to its most important pages (see llmstxt.org)."},
	{"og_image_missing", SevNotice, "social", "No share image", "The page has Open Graph tags but no og:image, so shared links show no picture.", "Add an og:image of at least 1200 by 630 pixels."},
}

var ruleByID = func() map[string]Rule {
	m := map[string]Rule{}
	for _, r := range Rules {
		m[r.ID] = r
	}
	return m
}()

// RuleFor returns a rule by id.
func RuleFor(id string) (Rule, bool) { r, ok := ruleByID[id]; return r, ok }

// AuditPage is what the audit reads of a crawled page.
type AuditPage struct {
	ID                               int64
	URL, Source, Status, ContentType string
	Reason                           string
	HTTPStatus                       *int
	Depth, Bytes, DurationMS         int
	RedirectTo                       string
	Noindex                          bool
	Title                            string
	TitleCount                       int
	Description, Canonical, Lang     string
	CanonicalCount, H1Count          int
	WordCount, ImagesNoAlt           int
	ContentHash                      string
	Viewport                         bool
	LinksIn                          int
	JSShell                          bool
	Nofollow                         bool
	LinksOut                         int
	H1                               []string
	Hreflang                         []Hreflang
	SchemaTypes                      int
	OGTitle, OGImage                 string
	InsecureResources                int
	// AIBlocked are the AI search and user crawlers robots.txt keeps from
	// the page; NoLLMSTxt marks the start page of a site without llms.txt.
	AIBlocked []string
	NoLLMSTxt bool
}

// AuditLink is an internal link between crawled pages.
type AuditLink struct {
	From     int64
	To       string
	Anchor   string
	Nofollow bool
}

// Issue is one rule one page breaks.
type Issue struct {
	PageID   int64
	Rule     string
	Template string
	Detail   string
}

// Audit is a crawl's result.
type Audit struct {
	Issues []Issue
	// Score is the share of HTML pages with no error, 0 to 100; nil when
	// the crawl read no HTML page.
	Score                     *int
	Errors, Warnings, Notices int
}

func (p *AuditPage) html() bool {
	return p.Status == "fetched" && p.HTTPStatus != nil && *p.HTTPStatus >= 200 && *p.HTTPStatus < 300 &&
		strings.Contains(p.ContentType, "html")
}

func (p *AuditPage) code() int {
	if p.HTTPStatus == nil {
		return 0
	}
	return *p.HTTPStatus
}

// RunAudit checks every page. Templates are worked out from all the URLs.
func RunAudit(pages []AuditPage, links []AuditLink) Audit {
	byURL := make(map[string]*AuditPage, len(pages))
	for i := range pages {
		byURL[pages[i].URL] = &pages[i]
	}
	templates := Templates(pages)
	var a Audit
	add := func(p *AuditPage, rule, detail string) {
		a.Issues = append(a.Issues, Issue{PageID: p.ID, Rule: rule, Template: templates[p.URL], Detail: clip(detail, 500)})
	}
	// Pages that are a problem to link to: broken, failing, or a redirect.
	bad := func(u string) (string, bool) {
		t := byURL[u]
		if t == nil || t.Status == "queued" || t.Status == "skipped" || t.Status == "blocked" {
			return "", false
		}
		switch c := t.code(); {
		case t.Status == "failed" || c >= 500:
			return "server_error", true
		case c >= 400:
			return "broken", true
		case c >= 300:
			return "redirect", true
		}
		return "", false
	}
	// follow walks a redirect to where it ends, and says whether it loops.
	follow := func(p *AuditPage) (string, bool) {
		seen := map[string]bool{p.URL: true}
		u := p.RedirectTo
		for range 20 {
			t := byURL[u]
			if t == nil || t.code() < 300 || t.code() >= 400 || t.RedirectTo == "" {
				return u, false
			}
			if seen[u] {
				return u, true
			}
			seen[u] = true
			u = t.RedirectTo
		}
		return u, false
	}
	titles, descs, hashes := map[string][]*AuditPage{}, map[string][]*AuditPage{}, map[string][]*AuditPage{}
	for i := range pages {
		p := &pages[i]
		c := p.code()
		switch {
		case p.Status == "blocked" && p.Reason == "robots" && p.Source == "sitemap":
			add(p, "blocked_in_sitemap", "")
		case p.Status == "failed" || c >= 500:
			add(p, "page_server_error", strconv.Itoa(c))
			if p.Source == "sitemap" {
				add(p, "sitemap_broken", strconv.Itoa(c))
			}
		case c >= 400:
			add(p, "page_broken", strconv.Itoa(c))
			if p.Source == "sitemap" {
				add(p, "sitemap_broken", strconv.Itoa(c))
			}
		case c >= 300 && c < 400 && p.RedirectTo != "":
			if c == 302 || c == 303 || c == 307 {
				add(p, "redirect_temporary", strconv.Itoa(c))
			}
			if p.Source == "sitemap" {
				add(p, "sitemap_redirect", p.RedirectTo)
			}
			end, loop := follow(p)
			switch {
			case loop:
				add(p, "redirect_loop", p.RedirectTo)
			default:
				if k, ok := bad(p.RedirectTo); ok && k == "redirect" {
					add(p, "redirect_chain", p.RedirectTo)
				}
				if k, ok := bad(end); ok && k != "redirect" {
					add(p, "redirect_to_broken", end)
				}
			}
		}
		if p.Status == "fetched" && p.DurationMS > 1500 {
			add(p, "page_slow", strconv.Itoa(p.DurationMS)+" ms")
		}
		if !p.html() {
			continue
		}
		if p.Bytes > 2<<20 {
			add(p, "page_large", strconv.Itoa(p.Bytes/1024)+" KB")
		}
		if strings.HasPrefix(p.URL, "http://") {
			add(p, "http_page", "")
		}
		if p.InsecureResources > 0 {
			add(p, "mixed_content", strconv.Itoa(p.InsecureResources))
		}
		if p.Nofollow {
			add(p, "nofollow_page", "")
		}
		if u, err := url.Parse(p.URL); err == nil {
			if len(p.URL) > 115 {
				add(p, "url_too_long", strconv.Itoa(len(p.URL))+" characters")
			}
			if u.Path != strings.ToLower(u.Path) {
				add(p, "url_uppercase", "")
			}
			if strings.Contains(u.Path, "_") {
				add(p, "url_underscores", "")
			}
			if u.RawQuery != "" && !p.Noindex && (p.Canonical == "" || p.Canonical == p.URL) {
				add(p, "url_parameters", "")
			}
		}
		if p.Noindex {
			add(p, "noindex", "")
			if p.Source == "sitemap" {
				add(p, "noindex_in_sitemap", "")
			}
		}
		switch {
		case p.CanonicalCount > 1:
			add(p, "canonical_multiple", strconv.Itoa(p.CanonicalCount))
		case p.Canonical != "" && p.Canonical != p.URL:
			if _, ok := bad(p.Canonical); ok {
				add(p, "canonical_broken", p.Canonical)
			} else if t := byURL[p.Canonical]; t != nil && t.html() && t.Noindex {
				add(p, "canonical_to_noindex", p.Canonical)
			} else {
				add(p, "canonical_elsewhere", p.Canonical)
			}
			if p.Source == "sitemap" {
				add(p, "sitemap_non_canonical", p.Canonical)
			}
		case p.CanonicalCount == 0 && !p.Noindex:
			add(p, "canonical_missing", "")
		}
		if len(p.Hreflang) > 0 {
			self := false
			for _, h := range p.Hreflang {
				if h.URL == p.URL {
					self = true
				}
				if !validHreflang.MatchString(h.Lang) {
					add(p, "hreflang_invalid", h.Lang)
				}
				if h.URL == "" || h.URL == p.URL {
					continue
				}
				if _, ok := bad(h.URL); ok {
					add(p, "hreflang_broken", h.URL)
				} else if t := byURL[h.URL]; t != nil && t.html() && !slices.ContainsFunc(t.Hreflang, func(b Hreflang) bool { return b.URL == p.URL }) {
					add(p, "hreflang_no_return", h.URL)
				}
			}
			if !self {
				add(p, "hreflang_no_self", "")
			}
		}
		if p.JSShell {
			add(p, "js_only_content", "")
		}
		if len(p.AIBlocked) > 0 && !p.Noindex {
			add(p, "ai_search_blocked", strings.Join(p.AIBlocked, ", "))
		}
		if p.NoLLMSTxt {
			add(p, "llms_txt_missing", "")
		}
		n := utf8.RuneCountInString(p.Title)
		switch {
		case p.Title == "":
			add(p, "title_missing", "")
		case n > 60:
			add(p, "title_too_long", strconv.Itoa(n)+" characters")
		case n < 10:
			add(p, "title_too_short", strconv.Itoa(n)+" characters")
		}
		if p.TitleCount > 1 {
			add(p, "title_multiple", strconv.Itoa(p.TitleCount))
		}
		if d := utf8.RuneCountInString(p.Description); p.Description == "" {
			add(p, "description_missing", "")
		} else if d > 160 {
			add(p, "description_too_long", strconv.Itoa(d)+" characters")
		} else if d < 50 {
			add(p, "description_too_short", strconv.Itoa(d)+" characters")
		}
		switch {
		case p.H1Count == 0:
			add(p, "h1_missing", "")
		case p.H1Count > 1:
			add(p, "h1_multiple", strconv.Itoa(p.H1Count))
		}
		if slices.Contains(p.H1, "") {
			add(p, "h1_empty", "")
		}
		if p.SchemaTypes == 0 && !p.Noindex {
			add(p, "schema_missing", "")
		}
		switch {
		case p.OGTitle == "" && p.OGImage == "":
			add(p, "og_missing", "")
		case p.OGImage == "":
			add(p, "og_image_missing", "")
		}
		switch {
		case p.LinksOut == 0:
			add(p, "links_out_none", "")
		case p.LinksOut > 300:
			add(p, "links_out_many", strconv.Itoa(p.LinksOut)+" links")
		}
		if p.LinksIn == 1 && p.Depth > 0 {
			add(p, "single_link_in", "")
		}
		if p.WordCount < 200 && !p.Noindex {
			add(p, "content_thin", strconv.Itoa(p.WordCount)+" words")
		}
		if p.ImagesNoAlt > 0 {
			add(p, "images_no_alt", strconv.Itoa(p.ImagesNoAlt))
		}
		if p.Lang == "" {
			add(p, "lang_missing", "")
		}
		if !p.Viewport {
			add(p, "viewport_missing", "")
		}
		if p.LinksIn == 0 && p.Source == "sitemap" {
			add(p, "orphan_page", "")
		}
		if p.Depth > 3 {
			add(p, "deep_page", strconv.Itoa(p.Depth)+" clicks")
		}
		// Duplicates count only among pages meant to be indexed, and a page
		// pointing its canonical elsewhere is a declared copy.
		if !p.Noindex && (p.Canonical == "" || p.Canonical == p.URL) {
			if p.Title != "" {
				titles[strings.ToLower(p.Title)] = append(titles[strings.ToLower(p.Title)], p)
			}
			if p.Description != "" {
				descs[strings.ToLower(p.Description)] = append(descs[strings.ToLower(p.Description)], p)
			}
			if p.ContentHash != "" && p.WordCount >= 50 {
				hashes[p.ContentHash] = append(hashes[p.ContentHash], p)
			}
		}
	}
	dupes := func(groups map[string][]*AuditPage, rule string) {
		for _, g := range groups {
			if len(g) < 2 {
				continue
			}
			for _, p := range g {
				// Name another page with the same, so the fix is clear.
				other := g[0]
				if other == p {
					other = g[1]
				}
				add(p, rule, other.URL)
			}
		}
	}
	dupes(titles, "title_duplicate")
	dupes(descs, "description_duplicate")
	dupes(hashes, "content_duplicate")

	// Links: to broken pages and to redirects, once per target.
	byID := make(map[int64]*AuditPage, len(pages))
	for i := range pages {
		byID[pages[i].ID] = &pages[i]
	}
	seen := map[[2]string]bool{}
	nofollow, noText := map[*AuditPage]int{}, map[*AuditPage]int{}
	for _, l := range links {
		from := byID[l.From]
		if from == nil {
			continue
		}
		if from.html() {
			if l.Nofollow {
				nofollow[from]++
			}
			if l.Anchor == "" {
				noText[from]++
			}
			if strings.HasPrefix(from.URL, "https://") && strings.HasPrefix(l.To, "http://") {
				if key := [2]string{"http:" + strconv.FormatInt(l.From, 10), l.To}; !seen[key] {
					seen[key] = true
					add(from, "link_to_http", l.To)
				}
			}
		}
		k, ok := bad(l.To)
		if !ok {
			continue
		}
		rule := "link_broken"
		if k == "redirect" {
			rule = "link_to_redirect"
		}
		key := [2]string{strconv.FormatInt(l.From, 10), l.To}
		if seen[key] {
			continue
		}
		seen[key] = true
		add(from, rule, l.To)
	}

	for p, n := range nofollow {
		add(p, "link_internal_nofollow", strconv.Itoa(n)+" links")
	}
	for p, n := range noText {
		add(p, "link_no_text", strconv.Itoa(n)+" links")
	}

	sort.SliceStable(a.Issues, func(i, j int) bool { return a.Issues[i].PageID < a.Issues[j].PageID })
	withError := map[int64]bool{}
	htmlPages := 0
	for i := range pages {
		if pages[i].html() || pages[i].code() >= 400 || pages[i].Status == "failed" {
			htmlPages++
		}
	}
	for _, is := range a.Issues {
		switch ruleByID[is.Rule].Severity {
		case SevError:
			a.Errors++
			withError[is.PageID] = true
		case SevWarning:
			a.Warnings++
		default:
			a.Notices++
		}
	}
	if htmlPages > 0 {
		s := 100 * (htmlPages - min(len(withError), htmlPages)) / htmlPages
		a.Score = &s
	}
	return a
}

// validHreflang is a language code, optionally with a script and a
// region, or x-default.
var validHreflang = regexp.MustCompile(`^(x-default|[a-z]{2,3}(-[a-z]{4})?(-([a-z]{2}|[0-9]{3}))?)$`)

var idLike = regexp.MustCompile(`^(\d+|[0-9a-f]{8,}|[0-9a-f-]{20,})$`)

// Templates names each page's URL template: the path with the parts that
// vary between many sibling pages replaced by *, like /blog/* or
// /products/*/reviews. Pages that share no pattern keep their own path.
func Templates(pages []AuditPage) map[string]string {
	type parsed struct {
		raw  string
		segs []string
	}
	var all []parsed
	// How many distinct values follow each prefix, at each depth.
	children := map[string]map[string]bool{}
	for _, p := range pages {
		u, err := url.Parse(p.URL)
		if err != nil {
			continue
		}
		segs := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(segs) == 1 && segs[0] == "" {
			segs = nil
		}
		all = append(all, parsed{p.URL, segs})
		for i := range segs {
			prefix := strconv.Itoa(len(segs)) + ":" + strings.Join(segs[:i], "/")
			if children[prefix] == nil {
				children[prefix] = map[string]bool{}
			}
			children[prefix][segs[i]] = true
		}
	}
	out := make(map[string]string, len(all))
	for _, p := range all {
		if len(p.segs) == 0 {
			out[p.raw] = "/"
			continue
		}
		t := make([]string, len(p.segs))
		for i, s := range p.segs {
			prefix := strconv.Itoa(len(p.segs)) + ":" + strings.Join(p.segs[:i], "/")
			// A part varies when it looks like an id, or when at least 3
			// sibling pages differ only there (the first part never does).
			if idLike.MatchString(s) || (i > 0 && len(children[prefix]) >= 3) {
				t[i] = "*"
			} else {
				t[i] = s
			}
		}
		out[p.raw] = "/" + strings.Join(t, "/")
	}
	return out
}

// storeAudit runs the audit of a finished crawl and stores its issues and
// score in tx.
func storeAudit(ctx context.Context, tx pgx.Tx, ws, runID string) (Audit, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, url, source, status, reason, content_type, http_status, depth, bytes, duration_ms, redirect_to, noindex,
		       title, title_count, meta_description, canonical, lang, canonical_count, coalesce(array_length(h1, 1), 0),
		       word_count, images_no_alt, content_hash, viewport, links_in, js_shell,
		       nofollow, links_out, h1, hreflang, coalesce(array_length(schema_types, 1), 0), og_title, og_image, insecure_resources
		FROM crawl_pages WHERE run_id = $1 AND workspace_id = $2 ORDER BY id`, runID, ws)
	if err != nil {
		return Audit{}, err
	}
	pages, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (AuditPage, error) {
		var p AuditPage
		err := row.Scan(&p.ID, &p.URL, &p.Source, &p.Status, &p.Reason, &p.ContentType, &p.HTTPStatus, &p.Depth, &p.Bytes, &p.DurationMS,
			&p.RedirectTo, &p.Noindex, &p.Title, &p.TitleCount, &p.Description, &p.Canonical, &p.Lang, &p.CanonicalCount,
			&p.H1Count, &p.WordCount, &p.ImagesNoAlt, &p.ContentHash, &p.Viewport, &p.LinksIn, &p.JSShell,
			&p.Nofollow, &p.LinksOut, &p.H1, &p.Hreflang, &p.SchemaTypes, &p.OGTitle, &p.OGImage, &p.InsecureResources)
		return p, err
	})
	if err != nil {
		return Audit{}, err
	}
	rows, err = tx.Query(ctx, `SELECT from_page, to_url, anchor, nofollow FROM crawl_links WHERE run_id = $1 AND internal`, runID)
	if err != nil {
		return Audit{}, err
	}
	links, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (AuditLink, error) {
		var l AuditLink
		return l, row.Scan(&l.From, &l.To, &l.Anchor, &l.Nofollow)
	})
	if err != nil {
		return Audit{}, err
	}
	// AI crawlers: what robots.txt lets each read, and llms.txt.
	files, err := loadRobotsFiles(ctx, tx, runID)
	if err != nil {
		return Audit{}, err
	}
	var start string
	var llms *int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(s.start_url, 'https://' || r.host || '/'), r.llms_txt_status
		FROM crawl_runs r LEFT JOIN seo_sites s ON s.id = r.site_id WHERE r.id = $1`, runID).Scan(&start, &llms); err != nil {
		return Audit{}, err
	}
	blocked, bots := aiAccess(pages, files, start)
	for i := range pages {
		pages[i].AIBlocked = blocked[pages[i].ID]
		pages[i].NoLLMSTxt = pages[i].Source == "start" && pages[i].html() && llms != nil && *llms != 200
	}
	for _, b := range bots {
		if _, err := tx.Exec(ctx, `
			INSERT INTO crawl_ai_bots (run_id, workspace_id, token, purpose, root_allowed, pages, blocked_pages)
			VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (run_id, token) DO UPDATE SET
				root_allowed = EXCLUDED.root_allowed, pages = EXCLUDED.pages, blocked_pages = EXCLUDED.blocked_pages`,
			runID, ws, b.Token, b.Purpose, b.RootAllowed, b.Pages, b.Blocked); err != nil {
			return Audit{}, err
		}
	}
	a := RunAudit(pages, links)
	if len(a.Issues) > 0 {
		ids, rules, sev, tpl, det := make([]int64, len(a.Issues)), make([]string, len(a.Issues)), make([]string, len(a.Issues)),
			make([]string, len(a.Issues)), make([]string, len(a.Issues))
		for i, is := range a.Issues {
			ids[i], rules[i], sev[i], tpl[i], det[i] = is.PageID, is.Rule, ruleByID[is.Rule].Severity, is.Template, is.Detail
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO crawl_issues (workspace_id, run_id, page_id, rule, severity, template, detail)
			SELECT $1, $2, p, r, s, t, d FROM unnest($3::bigint[], $4::text[], $5::text[], $6::text[], $7::text[]) AS x(p, r, s, t, d)
			ON CONFLICT DO NOTHING`, ws, runID, ids, rules, sev, tpl, det); err != nil {
			return Audit{}, err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE crawl_runs SET health_score = $2, issues_errors = $3, issues_warnings = $4, issues_notices = $5 WHERE id = $1`,
		runID, a.Score, a.Errors, a.Warnings, a.Notices)
	return a, err
}
