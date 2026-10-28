// Package news is the news watch: saved searches a workspace runs on a
// schedule through its own web search connection, the articles they find,
// and the articles about a brand, kept as its coverage. The searches and
// the jobs that run them live in package api; this package holds what
// decides which results are kept.
package news

import (
	"net/url"
	"slices"
	"strings"
	"unicode"

	"github.com/outflareai/outflare/engine/internal/customers"
)

// The values a search and a coverage item take.
var (
	Purposes       = []string{"topic", "coverage"}
	TimeRanges     = []string{"day", "week", "month"}
	SearchStatuses = []string{"active", "paused"}
	CoverageStates = []string{"new", "confirmed", "dismissed"}
)

const (
	// MaxResults is how many results a run keeps from one search.
	MaxResults = 50
	// MaxSearches is how many searches a workspace may save.
	MaxSearches = 200
	// MinHours and MaxHours bound how often a search runs.
	MinHours, MaxHours = 6, 168
)

// trackingParams are query parameters that only say where a click came
// from, dropped so one article is kept once.
var trackingParams = []string{"fbclid", "gclid", "mc_cid", "mc_eid", "ocid", "cmpid", "ref", "ref_src", "guccounter"}

// ArticleURL is an article's address as kept (no fragment or tracking
// parameters, lowercase scheme and host) and its website's domain, or ""
// when it isn't a web page.
func ArticleURL(raw string) (string, string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return "", ""
	}
	d := customers.CompanyDomain(u.Host)
	if d == "" {
		return "", ""
	}
	u.Fragment, u.RawFragment = "", ""
	u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	if u.Path == "" {
		u.Path = "/"
	}
	if u.RawQuery != "" {
		q := u.Query()
		for k := range q {
			lk := strings.ToLower(k)
			if strings.HasPrefix(lk, "utm_") || slices.Contains(trackingParams, lk) {
				q.Del(k)
			}
		}
		u.RawQuery = q.Encode()
	}
	out := u.String()
	if len(out) > 2048 {
		return "", ""
	}
	return out, d
}

// OwnSite reports whether domain is the brand's own website or one of its
// subdomains, which is never coverage.
func OwnSite(website, domain string) bool {
	own := customers.CompanyDomain(website)
	return own != "" && (domain == own || strings.HasSuffix(domain, "."+own))
}

// Mentions reports whether a result's title or snippet names what a
// coverage search looks for: every word of the query, in any case,
// ignoring quotes and search operators. A search engine also returns pages
// that only match a word in their address or links, which aren't coverage.
func Mentions(query, title, snippet string) bool {
	text := " " + fold(title+" "+snippet) + " "
	words := Words(query)
	if len(words) == 0 {
		return false
	}
	for _, w := range words {
		if !strings.Contains(text, " "+w+" ") {
			return false
		}
	}
	return true
}

// Words are a query's words, lowercase, without quotes, operators (OR,
// AND, site:...) or excluded words (-word).
func Words(query string) []string {
	var out []string
	for _, f := range strings.Fields(query) {
		if f == "OR" || f == "AND" || strings.HasPrefix(f, "-") || strings.Contains(f, ":") {
			continue
		}
		for _, w := range strings.Fields(fold(f)) {
			out = append(out, w)
		}
	}
	return out
}

// fold lowercases s and turns everything but letters and digits into
// spaces.
func fold(s string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}), " ")
}
