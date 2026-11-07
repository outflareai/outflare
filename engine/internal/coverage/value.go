package coverage

import (
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"unicode"
)

// How a link is got: the first question when deciding which of a
// competitor's links to go after.
const (
	// AccessCantBuy is an editorial link on a site that doesn't sell them:
	// news, a university or government site, Wikipedia, or a strong site
	// with real readers and no sign of payment.
	AccessCantBuy = "cant_buy"
	// AccessEarned is a page a third party chose to write: a review, a
	// comparison, a roundup, an article or interview.
	AccessEarned = "earned"
	// AccessPaid has evidence of a deal: sponsored, affiliate.
	AccessPaid = "paid"
	// AccessFree is anything anyone can make for themselves: a profile, a
	// listing, a forum post, their own upload to a platform.
	AccessFree = "free"
	// AccessUnknown is a page the address and numbers say nothing about.
	AccessUnknown = "unknown"
)

// Accesses are the access values, strongest first.
var Accesses = []string{AccessCantBuy, AccessEarned, AccessPaid, AccessFree, AccessUnknown}

// Link is what is known about one placement: where it is, what kind of
// page it is, and the site's and page's numbers when an SEO connection
// read them.
type Link struct {
	URL, Title, Anchor string
	// Kind is a placement type (Classify); "" when unknown.
	Kind string
	// Paid: there is evidence of a deal (arrangement likely_paid).
	Paid          bool
	Nofollow      *bool
	DomainRating  *float64
	URLRating     *float64
	DomainTraffic *float64
	// Competitors is how many of the brand's competitors this page features.
	Competitors int
	// Topic is the brand's topic (NewTopic); nil scores no relevance.
	Topic *Topic
}

// Valuation is a link's value to the brand, 0 to 100, how it's got and
// the reasons, as fixed short phrases a person can read.
type Valuation struct {
	Value   float64  `json:"value"`
	Access  string   `json:"access"`
	Reasons []string `json:"reasons"`
}

// platforms are sites anyone can post to or list themselves on: their
// domain rating says nothing about one page on them.
var platforms = []string{
	"google.com", "youtube.com", "youtu.be", "facebook.com", "instagram.com", "x.com", "twitter.com", "tiktok.com",
	"linkedin.com", "pinterest.com", "reddit.com", "quora.com", "medium.com", "github.com", "gitlab.com", "bitbucket.org",
	"stackoverflow.com", "stackexchange.com", "wordpress.com", "blogspot.com", "tumblr.com", "about.me", "linktr.ee",
	"crunchbase.com", "producthunt.com", "trustpilot.com", "g2.com", "capterra.com", "getapp.com", "softwareadvice.com",
	"trustradius.com", "alternativeto.net", "saashub.com", "slideshare.net", "scribd.com", "issuu.com", "soundcloud.com",
	"vimeo.com", "dailymotion.com", "twitch.tv", "discord.com", "telegram.me", "t.me", "threads.net", "bsky.app",
	"substack.com", "dev.to", "hashnode.dev", "hackernoon.com", "npmjs.com", "pypi.org", "docker.com", "hub.docker.com",
	"apple.com", "microsoft.com", "amazon.com", "yelp.com", "bbb.org", "glassdoor.com", "indeed.com", "wellfound.com",
	"angel.co", "f6s.com", "behance.net", "dribbble.com", "gravatar.com", "disqus.com", "archive.org", "sites.google.com",
	"blogger.com", "weebly.com", "wixsite.com", "notion.site", "gitbook.io", "readthedocs.io", "huggingface.co",
	"chromewebstore.google.com", "addons.mozilla.org", "apps.apple.com", "play.google.com",
}

// cantBuy are sites that don't sell links.
var cantBuy = []string{"wikipedia.org", "wikimedia.org", "nytimes.com", "theguardian.com", "bbc.co.uk", "bbc.com", "reuters.com",
	"apnews.com", "forbes.com", "techcrunch.com", "theverge.com", "wired.com", "arstechnica.com", "bloomberg.com", "wsj.com", "ft.com",
	"cnbc.com", "zdnet.com", "venturebeat.com", "nature.com", "sciencedirect.com", "arxiv.org", "acm.org", "ieee.org"}

func onAny(host string, list []string) bool {
	for _, d := range list {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

func hostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}

// Platform reports whether the page is on a site anyone can post to.
func Platform(raw string) bool { return onAny(hostOf(raw), platforms) }

// institutional is a government or university site.
func institutional(host string) bool {
	for _, l := range strings.Split(host, ".") {
		if l == "gov" || l == "edu" || l == "mil" || l == "ac" {
			return true
		}
	}
	return false
}

// AccessOf says how a link is got, from its address, kind and numbers.
func AccessOf(l Link) string {
	host := hostOf(l.URL)
	kind := l.Kind
	if kind == "" {
		kind = Classify(l.URL)
	}
	switch {
	case l.Paid:
		return AccessPaid
	case onAny(host, cantBuy) || institutional(host):
		return AccessCantBuy
	case kind == "youtube" || kind == "video":
		// Someone else's review is earned; an upload of their own isn't.
		if reviewish(l.Title) {
			return AccessEarned
		}
		return AccessFree
	case onAny(host, platforms):
		if kind == "review" {
			// Reviews on a review site are customers' words, but the
			// listing is free to make.
			return AccessFree
		}
		if (kind == "blog" || kind == "newsletter") && reviewish(l.Title) {
			return AccessEarned
		}
		if kind == "influencer" && postPath(l.URL) {
			// Someone's post about them, not their own profile.
			return AccessEarned
		}
		return AccessFree
	}
	switch kind {
	case "directory", "mcp_directory", "app_listing", "forum", "integration", "partner":
		return AccessFree
	case "news":
		return AccessCantBuy
	case "comparison", "roundup", "review", "blog", "interview", "case_study", "podcast", "newsletter", "influencer":
		if strong(l) {
			return AccessCantBuy
		}
		return AccessEarned
	}
	if l.DomainRating != nil || l.DomainTraffic != nil {
		// A page on someone else's site that links to them, with no sign of
		// payment: editorial unless the numbers say otherwise.
		if strong(l) {
			return AccessCantBuy
		}
		return AccessEarned
	}
	return AccessUnknown
}

// strong is a site with real authority and real readers, followed: the
// kind that turns down paid links.
func strong(l Link) bool {
	return l.DomainRating != nil && *l.DomainRating >= 75 && l.DomainTraffic != nil && *l.DomainTraffic >= 100_000 &&
		(l.Nofollow == nil || !*l.Nofollow)
}

// postPath is an address deeper than a profile: x.com/name/status/1, not
// x.com/name.
func postPath(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return len(strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })) >= 2
}

func reviewish(title string) bool {
	t := strings.ToLower(title)
	for _, w := range []string{"review", " vs ", " vs.", "versus", "best ", "top ", "alternative", "compar", "tested", "honest"} {
		if strings.Contains(t, w) {
			return true
		}
	}
	return false
}

// accessWeight is how much each way of getting a link keeps of its value.
var accessWeight = map[string]float64{AccessCantBuy: 1, AccessEarned: 0.9, AccessUnknown: 0.7, AccessPaid: 0.6, AccessFree: 0.35}

// Value scores a link's worth to the brand from 0 to 100: authority of
// the page (not a platform's domain), real readers, trust signals,
// relevance to the brand's topic and how many competitors the page
// features, weighted by how the link is got.
func Value(l Link) Valuation {
	access := AccessOf(l)
	platform := Platform(l.URL)
	var reasons []string
	kind := l.Kind
	if kind == "" {
		kind = Classify(l.URL)
	}
	if kind != "" {
		reasons = append(reasons, strings.ReplaceAll(kind, "_", " "))
	}

	// Authority, up to 40: the page's own rating, and the site's unless
	// anyone can post on it.
	authority := 0.0
	switch {
	case platform:
		if l.URLRating != nil {
			authority = clamp(*l.URLRating) / 100 * 30
		}
		reasons = append(reasons, "a platform anyone can post on")
	case l.DomainRating != nil && l.URLRating != nil:
		authority = clamp(*l.DomainRating)/100*25 + clamp(*l.URLRating)/100*15
		reasons = append(reasons, fmt.Sprintf("DR %.0f", *l.DomainRating))
	case l.DomainRating != nil:
		authority = clamp(*l.DomainRating) / 100 * 30
		reasons = append(reasons, fmt.Sprintf("DR %.0f", *l.DomainRating))
	case l.URLRating != nil:
		authority = clamp(*l.URLRating) / 100 * 30
	}

	// Readers, up to 15: search visits to the site, not counted on a
	// platform whose visitors never see this page.
	readers := 0.0
	if !platform && l.DomainTraffic != nil && *l.DomainTraffic > 0 {
		readers = math.Min(15, math.Log10(*l.DomainTraffic+1)*2.5)
		reasons = append(reasons, visits(*l.DomainTraffic))
	}

	// Trust, up to 15: a followed link, no sign of payment, and readers to
	// match its rating (a high rating with no visits is a link farm's mark).
	trust := 7.0
	switch {
	case l.Nofollow == nil:
		trust += 4
	case !*l.Nofollow:
		trust += 8
	default:
		reasons = append(reasons, "nofollow")
	}
	if l.Paid {
		trust -= 7
	}
	if !platform && l.DomainRating != nil && *l.DomainRating >= 40 && l.DomainTraffic != nil && *l.DomainTraffic < 500 {
		trust -= 12
		reasons = append(reasons, "few visits for its rating")
	}
	trust = math.Max(0, trust)

	// Relevance, up to 20: the page is about the brand's topic, and it
	// features several of the brand's competitors.
	relevance := 0.0
	if l.Topic != nil {
		if m := l.Topic.Match(l.URL, l.Title, l.Anchor); m > 0 {
			relevance += 14 * math.Min(1, float64(m)/2)
			reasons = append(reasons, "on your topic")
		}
	}
	if l.Competitors > 1 {
		relevance += math.Min(6, float64(l.Competitors)*1.5)
		reasons = append(reasons, fmt.Sprintf("features %d competitors", l.Competitors))
	}

	raw := authority + readers + trust + relevance
	v := math.Min(100, raw/90*100*accessWeight[access])
	return Valuation{Value: math.Round(v*10) / 10, Access: access, Reasons: reasons}
}

func clamp(v float64) float64 { return math.Max(0, math.Min(100, v)) }

func visits(n float64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM visits a month", n/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.0fk visits a month", n/1_000)
	}
	return fmt.Sprintf("%.0f visits a month", n)
}

// Topic is what the brand is about, as words from the searches it ranks
// for, so a page can be judged on topic or not.
type Topic struct {
	words map[string]bool
}

var stopwords = map[string]bool{"the": true, "and": true, "for": true, "with": true, "best": true, "free": true, "what": true,
	"how": true, "your": true, "from": true, "that": true, "this": true, "are": true, "can": true, "you": true, "top": true,
	"vs": true, "com": true, "www": true, "http": true, "https": true, "html": true, "near": true, "online": true, "buy": true,
	"cheap": true, "review": true, "reviews": true, "price": true, "pricing": true, "2024": true, "2025": true, "2026": true}

func words(s string) []string {
	f := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	out := f[:0]
	for _, w := range f {
		if len([]rune(w)) >= 4 && !stopwords[w] {
			out = append(out, stem(w))
		}
	}
	return out
}

// stem drops a plural s, so "proxies" and "proxy" meet.
func stem(w string) string {
	switch {
	case strings.HasSuffix(w, "ies") && len(w) > 4:
		return w[:len(w)-3] + "y"
	case strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && len(w) > 4:
		return w[:len(w)-1]
	}
	return w
}

// NewTopic builds a topic from phrases (the brand's keywords), leaving
// out the names given (brand and competitor names say nothing about a
// page's subject). The 40 commonest words make the topic.
func NewTopic(phrases []string, names []string) *Topic {
	skip := map[string]bool{}
	for _, n := range names {
		for _, w := range words(n) {
			skip[w] = true
		}
	}
	count := map[string]int{}
	for _, p := range phrases {
		seen := map[string]bool{}
		for _, w := range words(p) {
			if !skip[w] && !seen[w] {
				seen[w] = true
				count[w]++
			}
		}
	}
	list := make([]string, 0, len(count))
	for w := range count {
		list = append(list, w)
	}
	sort.Slice(list, func(i, j int) bool {
		if count[list[i]] != count[list[j]] {
			return count[list[i]] > count[list[j]]
		}
		return list[i] < list[j]
	})
	if len(list) > 40 {
		list = list[:40]
	}
	if len(list) == 0 {
		return nil
	}
	t := &Topic{words: map[string]bool{}}
	for _, w := range list {
		t.words[w] = true
	}
	return t
}

// Match is how many of the topic's words a page's address, title and
// anchor use.
func (t *Topic) Match(texts ...string) int {
	if t == nil {
		return 0
	}
	seen := map[string]bool{}
	for _, s := range texts {
		for _, w := range words(s) {
			if t.words[w] {
				seen[w] = true
			}
		}
	}
	return len(seen)
}
