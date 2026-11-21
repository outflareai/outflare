package connectors

import "context"

// Search consoles: analytics connectors that can also say how a search
// engine sees a site page by page. They answer the same reports as any
// AnalyticsReader, and three more things the SEO module stores: clicks and
// impressions for each page and query together, how the engine indexed a
// URL, and the sitemaps it read.

// SearchRow is one page and query on one day.
type SearchRow struct {
	Page        string  `json:"page"`
	Query       string  `json:"query"`
	Clicks      float64 `json:"clicks"`
	Impressions float64 `json:"impressions"`
	CTR         float64 `json:"ctr"`
	Position    float64 `json:"position"`
}

// URLInspection is how the search engine sees one URL: whether it is
// indexed, why not, and the canonical it chose. Verdicts are the
// provider's own words (Google: PASS, PARTIAL, FAIL, NEUTRAL).
type URLInspection struct {
	URL     string `json:"url"`
	Verdict string `json:"verdict"`
	// Coverage is the engine's reason in words: "Submitted and indexed",
	// "Crawled - currently not indexed".
	Coverage        string   `json:"coverage"`
	Indexing        string   `json:"indexing"`
	RobotsTxt       string   `json:"robots_txt"`
	PageFetch       string   `json:"page_fetch"`
	GoogleCanonical string   `json:"google_canonical"`
	UserCanonical   string   `json:"user_canonical"`
	LastCrawl       string   `json:"last_crawl"`
	CrawledAs       string   `json:"crawled_as"`
	Sitemaps        []string `json:"sitemaps"`
	Mobile          string   `json:"mobile"`
	RichResults     string   `json:"rich_results"`
	// Link opens the URL's report in the provider's own tool.
	Link string `json:"link"`
}

// SitemapStatus is one sitemap the engine knows about.
type SitemapStatus struct {
	Path          string `json:"path"`
	LastSubmitted string `json:"last_submitted"`
	LastRead      string `json:"last_read"`
	Pending       bool   `json:"pending"`
	Index         bool   `json:"index"`
	Errors        int    `json:"errors"`
	Warnings      int    `json:"warnings"`
	// Submitted and Indexed count the URLs it lists and those indexed,
	// when the engine says.
	Submitted int `json:"submitted"`
	Indexed   int `json:"indexed"`
}

// SearchConsoleReader is an AnalyticsReader that also reads page by page
// search data. It must not change anything at the provider.
type SearchConsoleReader interface {
	AnalyticsReader
	// SearchRows returns a day's rows for every page and query, the most
	// clicked first, at most limit of them.
	SearchRows(ctx context.Context, c *Client, creds Creds, site, day string, limit int) ([]SearchRow, error)
	// InspectURL asks how one URL of the site is indexed.
	InspectURL(ctx context.Context, c *Client, creds Creds, site, url string) (URLInspection, error)
	// Sitemaps lists the site's sitemaps.
	Sitemaps(ctx context.Context, c *Client, creds Creds, site string) ([]SitemapStatus, error)
}

// SearchDetailsReader provides independent totals and page/country/device rows.
// Totals are property-wide; they must not be labelled as host-specific totals.
type SearchDetailsReader interface {
	SearchDetails(context.Context, *Client, Creds, string, string, int) (SearchDetails, error)
}
type SearchSegment struct {
	Page        string
	Country     string
	Device      string
	Clicks      float64
	Impressions float64
	Position    float64
}
type SearchDetails struct {
	Clicks      float64
	Impressions float64
	Segments    []SearchSegment
}

// SearchAnalyticsFilter narrows a Search Analytics query to rows whose
// dimension (query, page, country, device or searchAppearance) matches:
// operator is equals, notEquals, contains, notContains, includingRegex or
// excludingRegex (RE2), as in Search Console's own filters.
type SearchAnalyticsFilter struct {
	Dimension  string `json:"dimension"`
	Operator   string `json:"operator"`
	Expression string `json:"expression"`
}

// SearchAnalyticsQuery is one Search Analytics request, as Search
// Console's Performance report asks it: a search type (web, image, video,
// news, discover, googleNews), the dimensions to group by (none for the
// totals), filters that all apply, and how many rows.
type SearchAnalyticsQuery struct {
	Site       string
	From, To   string
	Type       string
	Dimensions []string
	Filters    []SearchAnalyticsFilter
	Limit      int
	// Hourly asks for hourly data (dataState hourly_all), which the hour
	// dimension needs; Search Console keeps it for the last few days.
	Hourly bool
}

// SearchAnalyticsRow is one row: the dimension values in the order asked,
// and the metrics for them.
type SearchAnalyticsRow struct {
	Keys        []string `json:"keys"`
	Clicks      float64  `json:"clicks"`
	Impressions float64  `json:"impressions"`
	CTR         float64  `json:"ctr"`
	Position    float64  `json:"position"`
}

// SearchAnalyticsReader answers any Search Analytics query live, so a
// page can show exactly what the search console's own report shows for
// any mix of filters.
type SearchAnalyticsReader interface {
	SearchAnalytics(ctx context.Context, cl *Client, creds Creds, q SearchAnalyticsQuery) ([]SearchAnalyticsRow, error)
}
