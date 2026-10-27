package connectors

import (
	"context"
	"net/url"
	"strings"
)

// Web search: connectors that run a general web search and return ranked
// results, for finding companies and pages to read (the lead finder,
// research). Unlike SERP reads they are not for rank tracking: the engine
// that answers may rotate. Each answer says what it cost so the caller
// records it on the connection's budget.

// CapWebSearch marks a connector that implements WebSearcher.
const CapWebSearch = "web.search"

// WebQuery asks for one page of web results.
type WebQuery struct {
	Query string `json:"query"`
	// Locale is a language code (en), a language-region tag (en-US) or
	// empty for the provider's default.
	Locale string `json:"locale,omitempty"`
	// Page counts from 1.
	Page int `json:"page,omitempty"`
	// TimeRange limits results to the last day, week, month or year.
	TimeRange string `json:"time_range,omitempty"`
	// SafeSearch is off, moderate or strict.
	SafeSearch string `json:"safe_search,omitempty"`
}

// WebTimeRanges and WebSafeSearch are the values WebQuery accepts.
var (
	WebTimeRanges = []string{"day", "week", "month", "year"}
	WebSafeSearch = []string{"off", "moderate", "strict"}
)

// WebResult is one result, best first.
type WebResult struct {
	Position int    `json:"position"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	Domain   string `json:"domain"`
	Snippet  string `json:"snippet,omitempty"`
}

// WebResults is one page of results.
type WebResults struct {
	// Engine is the search engine that answered, when the provider says.
	Engine      string      `json:"engine,omitempty"`
	Results     []WebResult `json:"results"`
	Suggestions []string    `json:"suggestions,omitempty"`
	// Correction is the spelling the provider searched for instead.
	Correction string  `json:"correction,omitempty"`
	Cost       SEOCost `json:"cost"`
}

// WebSearcher runs web searches.
type WebSearcher interface {
	Connector
	// EstimateWebSearch is what a query will cost, from the price list.
	EstimateWebSearch(q WebQuery) SEOCost
	WebSearch(ctx context.Context, c *Client, creds Creds, q WebQuery) (WebResults, error)
}

// ResultDomain is a result URL's host without "www.", lowercase, or "".
func ResultDomain(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}
