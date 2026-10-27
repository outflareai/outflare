package connectors

import "context"

// SERP: connectors that read a search engine's results page for a keyword,
// for rank tracking. Like other SEO reads they charge per call, so each
// answer says what it cost and the caller records it on the connection's
// budget.

// CapSERP marks a connector that implements SERPReader.
const CapSERP = "serp.read"

// SERPQuery asks for one results page.
type SERPQuery struct {
	Keyword string `json:"keyword"`
	// Country is a two-letter code; Device desktop or mobile.
	Country string `json:"country"`
	Device  string `json:"device"`
	// Depth is how many results to read, 10 to 100.
	Depth int `json:"depth"`
}

// SERPItem is one organic result.
type SERPItem struct {
	Position int    `json:"position"`
	URL      string `json:"url"`
	Domain   string `json:"domain"`
	Title    string `json:"title"`
}

// SERPResult is a results page, organic results only, best first.
type SERPResult struct {
	// Engine is the search engine that answered: google, brave, ...
	Engine string     `json:"engine"`
	Items  []SERPItem `json:"items"`
	Cost   SEOCost    `json:"cost"`
}

// SERPReader reads search results pages.
type SERPReader interface {
	Connector
	// SERPEngines are the engines it can read, the default first.
	SERPEngines() []string
	// EstimateSERP is what a query will cost, from the price list.
	EstimateSERP(q SERPQuery) SEOCost
	SERP(ctx context.Context, c *Client, creds Creds, engine string, q SERPQuery) (SERPResult, error)
}
