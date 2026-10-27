package connectors

import (
	"context"
	"strings"
	"time"
)

// SEO: connectors that read how a domain does in search from the outside:
// its link strength, the keywords it ranks for, who links to it, who it
// competes with and what a site audit found. Unlike analytics tools, SEO
// providers charge per call, so every SEO read has a cost the caller
// records and caps. They only read.

// CategorySEO groups SEO data providers.
const CategorySEO = "seo"

// CapSEO marks a connector that implements SEOReader.
const CapSEO = "seo.read"

// CapCrawlFetch marks a connector that implements PageFetcher: it fetches
// a page for OutflareBot when the site's firewall blocks the crawler.
const CapCrawlFetch = "crawl.fetch"

// FetchedPage is a page a PageFetcher read.
type FetchedPage struct {
	// URL is where the page ended up; Status the site's HTTP answer.
	URL    string
	Status int
	HTML   string
	Cost   SEOCost
}

// PageFetcher reads one page through the provider's own network, for a
// site whose firewall or bot challenge blocks the crawler. The crawler
// only asks for pages robots.txt allows.
type PageFetcher interface {
	Connector
	FetchPage(ctx context.Context, c *Client, creds Creds, url string, renderJS bool) (FetchedPage, error)
}

// Reports an SEOReader can answer.
const (
	SEOOverview    = "overview"    // domain metrics: rating, backlinks, keywords, estimated traffic
	SEOKeywords    = "keywords"    // keywords the domain ranks for, with position and volume
	SEOBacklinks   = "backlinks"   // domains that link to it
	SEOCompetitors = "competitors" // domains that rank for the same keywords
	SEOAudit       = "audit"       // issues a site audit found
	SEOLinks       = "links"       // single links to it, live and recently lost
)

// AllSEOReports lists every SEO report, in display order.
var AllSEOReports = []string{SEOOverview, SEOKeywords, SEOBacklinks, SEOLinks, SEOCompetitors, SEOAudit}

// SEOMetrics are the numbers an SEO report can carry. A nil field means the
// provider does not measure it.
type SEOMetrics struct {
	// DomainRating is link strength from 0 to 100 (Ahrefs DR, DataForSEO
	// rank scaled to 100).
	DomainRating *float64 `json:"domain_rating,omitempty"`
	// Backlinks and ReferringDomains are live links and the domains they
	// come from.
	Backlinks        *float64 `json:"backlinks,omitempty"`
	ReferringDomains *float64 `json:"referring_domains,omitempty"`
	// OrganicKeywords is how many keywords rank in the top 100, Top3 how
	// many in the top 3.
	OrganicKeywords *float64 `json:"organic_keywords,omitempty"`
	Top3            *float64 `json:"top3,omitempty"`
	// OrganicTraffic is the provider's estimate of monthly visits from
	// search, TrafficValue what that traffic would cost in ads, in USD.
	OrganicTraffic *float64 `json:"organic_traffic,omitempty"`
	TrafficValue   *float64 `json:"traffic_value,omitempty"`
	// Row metrics: a keyword's position, monthly searches and the traffic
	// it brings; how many keywords a competitor shares.
	Position       *float64 `json:"position,omitempty"`
	Volume         *float64 `json:"volume,omitempty"`
	Traffic        *float64 `json:"traffic,omitempty"`
	CommonKeywords *float64 `json:"common_keywords,omitempty"`
	// Audit: the site's health score (0 to 100) and the pages an issue
	// affects.
	HealthScore *float64 `json:"health_score,omitempty"`
	Pages       *float64 `json:"pages,omitempty"`
}

// SEORow is one keyword, linking domain, competitor or audit issue.
type SEORow struct {
	Key string `json:"key"`
	// URL is the ranking page of a keyword.
	URL string `json:"url,omitempty"`
	// Kind is an audit issue's importance: error, warning or notice.
	Kind string `json:"kind,omitempty"`
	// Link is one backlink, in the links report.
	Link *SEOLink `json:"link,omitempty"`
	SEOMetrics
}

// SEOLink is one link from another site's page to the target, as a
// provider last saw it.
type SEOLink struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Anchor string `json:"anchor"`
	// SourceDomain is the linking site's host.
	SourceDomain string `json:"source_domain"`
	// Nofollow, Sponsored and UGC are the link's rel attributes.
	Nofollow  bool `json:"nofollow"`
	Sponsored bool `json:"sponsored"`
	UGC       bool `json:"ugc"`
	// Lost: the provider no longer finds the link, since LastSeen, for
	// LostReason (removed, page gone, redirected, noindex, ...).
	Lost       bool   `json:"lost"`
	LostReason string `json:"lost_reason,omitempty"`
	// Dates the provider first and last found the link (YYYY-MM-DD).
	FirstSeen string `json:"first_seen,omitempty"`
	LastSeen  string `json:"last_seen,omitempty"`
	// SourceRating is the linking domain's strength, 0 to 100.
	SourceRating *float64 `json:"source_rating,omitempty"`
	// Spam is the provider's own judgement, 0 (clean) to 100 (spam):
	// DataForSEO's spam score, or 100 when Ahrefs lists the domain as spam.
	Spam *float64 `json:"spam,omitempty"`
	// ExternalLinks is how many links to other sites the linking page has.
	ExternalLinks *float64 `json:"external_links,omitempty"`
	// Title is the linking page's title.
	Title string `json:"title,omitempty"`
}

// SEOCost is what a call cost: money for pay-as-you-go providers
// (DataForSEO), API units for subscription ones (Ahrefs).
type SEOCost struct {
	USD   float64 `json:"usd,omitempty"`
	Units float64 `json:"units,omitempty"`
	// Estimated: the provider did not say, so this is worked out from its
	// price list.
	Estimated bool `json:"estimated,omitempty"`
}

// Add sums two costs.
func (c SEOCost) Add(d SEOCost) SEOCost {
	return SEOCost{USD: c.USD + d.USD, Units: c.Units + d.Units, Estimated: c.Estimated || d.Estimated}
}

// SEOQuery asks for one report about one domain.
type SEOQuery struct {
	Report string `json:"report"`
	// Target is a domain, like example.com.
	Target string `json:"target"`
	// Country is a two-letter code for keyword and traffic reports.
	Country string `json:"country"`
	Limit   int    `json:"limit"`
	// Since (YYYY-MM-DD) adds links lost on or after it to the links
	// report; empty is live links only.
	Since string `json:"since,omitempty"`
}

// SEOReport is a provider's answer, in provider-neutral terms.
type SEOReport struct {
	Report  string     `json:"report"`
	Target  string     `json:"target"`
	Country string     `json:"country"`
	Totals  SEOMetrics `json:"totals"`
	Rows    []SEORow   `json:"rows,omitempty"`
	Notes   []string   `json:"notes"`
	Cost    SEOCost    `json:"cost"`
}

// SEOReader reads SEO data about a domain. Each Report call may cost
// money; callers check Estimate against the connection's budget first.
type SEOReader interface {
	Connector
	// SEOReports returns the reports the provider can answer.
	SEOReports() []string
	// Estimate is the most a query should cost, for budget checks.
	Estimate(q SEOQuery) SEOCost
	// Report answers one query. The caller has checked it with CheckSEOQuery.
	SEOReport(ctx context.Context, c *Client, creds Creds, q SEOQuery) (SEOReport, error)
}

// SupportsSEO reports whether r answers report.
func SupportsSEO(r SEOReader, report string) bool {
	for _, x := range r.SEOReports() {
		if x == report {
			return true
		}
	}
	return false
}

// CheckSEOQuery validates a query and fills in the defaults: the US and
// 50 rows.
func CheckSEOQuery(q SEOQuery) (SEOQuery, error) {
	q.Country = strings.ToUpper(strings.TrimSpace(q.Country))
	if q.Country == "" {
		q.Country = "US"
	}
	if len(q.Country) != 2 {
		return q, Errf(ErrInvalid, "country should be a two-letter code, like US or AU")
	}
	if q.Target == "" {
		return q, Errf(ErrInvalid, "name a domain, like example.com")
	}
	if q.Since != "" {
		if _, err := time.Parse(time.DateOnly, q.Since); err != nil {
			return q, Errf(ErrInvalid, "since is a date, like 2026-01-31")
		}
	}
	switch {
	case q.Limit <= 0:
		q.Limit = 50
	case q.Limit > 1000:
		q.Limit = 1000
	}
	return q, nil
}
