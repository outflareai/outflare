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
	// Partial: the engine stopped before the depth asked, so a site not in
	// Items may still rank below them.
	Partial bool `json:"partial,omitempty"`
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

// CapTrackedRanks marks a connector that implements TrackedRankReader.
const CapTrackedRanks = "ranks.tracked"

// TrackedRankQuery asks for the positions an account already tracks in its
// own rank tracker for one site.
type TrackedRankQuery struct {
	// Hosts are the site's hosts (www and bare); the tracker's project for
	// any of them answers.
	Hosts    []string
	From, To string // YYYY-MM-DD, both included
}

// TrackedDay is one day's check: the site's position (nil when the tracker
// did not find it) and the page that ranked.
type TrackedDay struct {
	Day      string
	Position *int
	URL      string
}

// TrackedRank is one tracked keyword on one engine, region and device.
type TrackedRank struct {
	Keyword string
	// Country is a two-letter code; Device desktop or mobile; Engine
	// google, bing, ...
	Country, Device, Engine string
	Days                    []TrackedDay
}

// TrackedRanks is what the tracker holds for a site.
type TrackedRanks struct {
	// Project names the tracker's project that matched, "" when none did.
	Project string
	Ranks   []TrackedRank
	// Skipped says what could not be mapped, such as an engine whose
	// country is unknown.
	Skipped []string
}

// TrackedRankReader reads positions an account already checks in its own
// rank tracker, so Outflare keeps them without paying for its own checks.
type TrackedRankReader interface {
	Connector
	TrackedRanks(ctx context.Context, c *Client, creds Creds, q TrackedRankQuery) (TrackedRanks, error)
}
