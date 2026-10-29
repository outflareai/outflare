package seo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Fetchers returns the SEO connectors that read pages for the crawler.
func Fetchers() []c.Connector { return []c.Connector{Geonode{}} }

const geonodeAPI = "https://scraper.geonode.io"

// Geonode is Geonode's Scraper API. OutflareBot uses it as a fallback for
// a site the workspace has proved it owns, when the site's firewall or a
// bot challenge blocks the crawler (never for what robots.txt disallows).
// Geonode charges in tokens per page, which each answer reports.
type Geonode struct{}

func (Geonode) Definition() c.Definition {
	d := def("geonode", "Geonode", "https://geonode.com", "https://docs.geonode.com/docs/scraper-api",
		"https://app.geonode.com/",
		"Searches the web, tracks where your pages rank on Google, finds your brands' social links, reads public X follower counts, and reads your own site's pages when a firewall blocks OutflareBot.",
		"Runs web searches for finding companies and pages, through Geonode's Search API. "+
			"Checks where your pages rank on Google for the keywords you track, through Geonode's Google Search Light API (one credit per results page; a check reads 10 pages), within the monthly budget you set on the connection. "+
			"Reads each brand's homepage to suggest its social profiles and app store listings, and the public X profile in each brand's links once a day for its follower count. "+
			"For sites you have proved you own, and only for pages robots.txt allows: when a page answers with a firewall block or a challenge, "+
			"OutflareBot reads it through Geonode's Scraper API instead. Searches and pages cost Geonode tokens on your account.",
		"Tokens per search and per page, and a credit per Google results page, on your Geonode plan", []c.Field{
			{Name: "api_key", Label: "API key", Secret: true, Help: "Geonode dashboard > Scraper API > API keys."},
		})
	d.Capabilities = []string{c.CapCrawlFetch, c.CapWebSearch, c.CapSERP, c.CapHooks}
	return d
}

func (Geonode) header(creds c.Creds) http.Header {
	return http.Header{"X-Api-Key": {creds["api_key"]}}
}

// Test reads the key's concurrency limit, which spends nothing.
func (g Geonode) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var resp struct {
		InUse int `json:"work_concurrency_in_use"`
		Limit int `json:"work_concurrency_limit"`
	}
	if err := get(ctx, cl, "Geonode", cl.Base("geonode", geonodeAPI)+"/v1/usage/concurrency", g.header(creds), &resp); err != nil {
		return c.Account{}, err
	}
	return c.Account{Name: "Geonode Scraper API", Details: map[string]string{"concurrency": strconv.Itoa(resp.Limit)}}, nil
}

// geonodePageWait is how long Geonode may take to read one page. Pages read
// in a browser behind a bot check (Trustpilot, Capterra) take 25 seconds or
// more; Geonode asked for 60. Outflare waits a little longer than Geonode,
// so Geonode's own answer, read or failed, always arrives, and a page is
// never sent again because Outflare gave up first.
const (
	geonodePageWait = 60 * time.Second
	geonodeHTTPWait = geonodePageWait + 15*time.Second
)

// FetchPage reads one page with POST /v1/extract, waiting for the answer.
func (g Geonode) FetchPage(ctx context.Context, cl *c.Client, creds c.Creds, url string, renderJS bool) (c.FetchedPage, error) {
	status, raw, err := g.postWithin(ctx, cl, creds, "/v1/extract", map[string]any{
		"url": url, "formats": []string{"html"}, "processing_mode": "sync", "render_js": renderJS,
		"timeout": geonodePageWait.Milliseconds(),
	}, geonodeHTTPWait)
	if err != nil {
		return c.FetchedPage{}, err
	}
	var out struct {
		Data struct {
			HTML string `json:"html"`
		} `json:"data"`
		Metadata struct {
			HTTPStatus int    `json:"http_status"`
			URL        string `json:"url"`
		} `json:"metadata"`
		Tokens int `json:"tokens_charged"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &out)
	cost := c.SEOCost{Units: float64(out.Tokens)}
	switch {
	case status == http.StatusOK:
		if out.Metadata.URL == "" {
			out.Metadata.URL = url
		}
		return c.FetchedPage{URL: out.Metadata.URL, Status: out.Metadata.HTTPStatus, HTML: out.Data.HTML, Cost: cost}, nil
	case status == http.StatusUnprocessableEntity && out.Error != nil:
		// The page could not be read: Geonode was blocked too, or the site
		// failed. The code says which; the message may quote the site.
		return c.FetchedPage{Cost: cost}, c.Errf(geonodeKind(out.Error.Code), "Geonode could not read the page (%s)", out.Error.Code)
	}
	return c.FetchedPage{Cost: cost}, c.HTTPError("Geonode", status, string(raw))
}

// geonodeSearchUSD is Geonode's list price per search, used when an answer
// does not say what it charged.
const geonodeSearchUSD = 1.5 / 1000

func (Geonode) EstimateWebSearch(c.WebQuery) c.SEOCost {
	return c.SEOCost{USD: geonodeSearchUSD, Estimated: true}
}

// WebSearch runs one search with POST /v1/search, waiting for the answer.
// Geonode rotates across engines and falls back on failure; the answer
// names the one that served it.
func (g Geonode) WebSearch(ctx context.Context, cl *c.Client, creds c.Creds, q c.WebQuery) (c.WebResults, error) {
	q.Query = strings.TrimSpace(q.Query)
	if q.Query == "" || len(q.Query) > 1000 {
		return c.WebResults{}, c.Errf(c.ErrInvalid, "a search needs 1 to 1000 characters")
	}
	body := map[string]any{"query": q.Query}
	if q.Locale != "" {
		body["locale"] = q.Locale
	}
	if q.Page > 1 {
		body["page"] = q.Page
	}
	if q.TimeRange != "" {
		body["time_range"] = q.TimeRange
	}
	if q.SafeSearch != "" {
		body["safe_search"] = q.SafeSearch
	}
	status, raw, err := g.post(ctx, cl, creds, "/v1/search", body)
	if err != nil {
		return c.WebResults{}, err
	}
	// Geonode wraps answers in "data" on some endpoints and not others,
	// so read both shapes.
	type answer struct {
		Engine  string `json:"engine"`
		Results []struct {
			Position int    `json:"position"`
			Title    string `json:"title"`
			URL      string `json:"url"`
			Link     string `json:"link"`
			Snippet  string `json:"snippet"`
			Content  string `json:"content"`
		} `json:"results"`
		Suggestions []string `json:"suggestions"`
		Correction  string   `json:"correction"`
		Tokens      int      `json:"tokens_charged"`
	}
	var out struct {
		answer
		Data  *answer `json:"data"`
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &out)
	a := out.answer
	if out.Data != nil {
		a = *out.Data
		if a.Tokens == 0 {
			a.Tokens = out.Tokens
		}
	}
	cost := c.SEOCost{Units: float64(a.Tokens)}
	if a.Tokens == 0 {
		cost = g.EstimateWebSearch(q)
	}
	if status != http.StatusOK {
		if out.Error != nil {
			return c.WebResults{Cost: c.SEOCost{Units: float64(a.Tokens)}}, c.Errf(geonodeKind(out.Error.Code), "Geonode could not run the search (%s)", out.Error.Code)
		}
		return c.WebResults{}, c.HTTPError("Geonode", status, string(raw))
	}
	res := c.WebResults{Engine: a.Engine, Suggestions: a.Suggestions, Correction: a.Correction, Cost: cost, Results: []c.WebResult{}}
	for i, r := range a.Results {
		u := r.URL
		if u == "" {
			u = r.Link
		}
		d := c.ResultDomain(u)
		if d == "" {
			continue
		}
		snippet := r.Snippet
		if snippet == "" {
			snippet = r.Content
		}
		pos := r.Position
		if pos == 0 {
			pos = i + 1
		}
		res.Results = append(res.Results, c.WebResult{Position: pos, Title: strings.TrimSpace(r.Title), URL: u, Domain: d, Snippet: strings.TrimSpace(snippet)})
	}
	return res, nil
}

// geonodeSERPPageUSD is what one Google results page costs (one credit).
// Geonode's answers do not say, so every rank check's cost is worked out
// from it.
const geonodeSERPPageUSD = 0.5 / 1000

// geonodeCountries are Google's names for the countries rank tracking
// covers, for the uule that makes Google search from there. The language
// comes from the same list DataForSEO uses.
var geonodeCountries = map[string]string{
	"US": "United States", "GB": "United Kingdom", "AU": "Australia", "CA": "Canada", "NZ": "New Zealand",
	"IE": "Ireland", "IN": "India", "SG": "Singapore", "ZA": "South Africa", "DE": "Germany",
	"FR": "France", "ES": "Spain", "IT": "Italy", "NL": "Netherlands", "BR": "Brazil",
	"MX": "Mexico", "SE": "Sweden", "PL": "Poland", "JP": "Japan",
}

// uule is Google's location parameter for a place by its name: "w+CAIQICI",
// a character for the name's length, then the name in base64.
func uule(name string) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	return "w+CAIQICI" + string(alphabet[len(name)%len(alphabet)]) + base64.RawStdEncoding.EncodeToString([]byte(name))
}

// SERPEngines: Geonode's Google Search Light reads Google.
func (Geonode) SERPEngines() []string { return []string{"google"} }

func serpPages(depth int) int { return min((max(depth, 10)+9)/10, 10) }

// EstimateSERP is one credit for each 10 results read.
func (Geonode) EstimateSERP(q c.SERPQuery) c.SEOCost {
	return c.SEOCost{USD: geonodeSERPPageUSD * float64(serpPages(q.Depth)), Estimated: true}
}

// SERP reads Google's organic results with POST /v1/google/search/light,
// every page in one request. Geonode bills the pages it returns; when it
// stops early the answer is Partial.
func (g Geonode) SERP(ctx context.Context, cl *c.Client, creds c.Creds, engine string, q c.SERPQuery) (c.SERPResult, error) {
	out := c.SERPResult{Engine: "google", Items: []c.SERPItem{}}
	if engine != "" && engine != "google" {
		return out, c.Errf(c.ErrInvalid, "Geonode here reads Google only")
	}
	name, ok := geonodeCountries[q.Country]
	if !ok {
		keys := make([]string, 0, len(geonodeCountries))
		for k := range geonodeCountries {
			keys = append(keys, k)
		}
		return out, c.Errf(c.ErrInvalid, "Geonode here covers %s; ask for one of those countries", strings.Join(sorted(keys), ", "))
	}
	device := q.Device
	if device == "" {
		device = "desktop"
	}
	if device != "desktop" && device != "mobile" {
		return out, c.Errf(c.ErrInvalid, "device is desktop or mobile")
	}
	pages := serpPages(q.Depth)
	status, raw, err := g.post(ctx, cl, creds, "/v1/google/search/light", map[string]any{
		"query": q.Keyword, "pages": pages, "format": "json", "device": device,
		"locale": locations[q.Country].lang + "-" + q.Country, "uule": uule(name),
	})
	if err != nil {
		return out, err
	}
	var resp struct {
		PagesReturned int    `json:"pages_returned"`
		PagesStopped  string `json:"pages_stopped"`
		Pages         []struct {
			Results []struct {
				Title      string `json:"title"`
				URL        string `json:"url"`
				SourceHost string `json:"source_host"`
			} `json:"results"`
		} `json:"pages"`
		Code string `json:"code"`
	}
	_ = json.Unmarshal(raw, &resp)
	if status != http.StatusOK {
		// Nothing is billed when page 1 fails. No results is an answer:
		// the site is not on Google for it.
		if status == http.StatusUnprocessableEntity && resp.Code == "NO_RESULTS" {
			return out, nil
		}
		if resp.Code != "" {
			return out, c.Errf(geonodeKind(resp.Code), "Geonode could not read Google's results (%s)", resp.Code)
		}
		return out, c.HTTPError("Geonode", status, string(raw))
	}
	returned := resp.PagesReturned
	if returned == 0 {
		returned = len(resp.Pages)
	}
	out.Cost = c.SEOCost{USD: geonodeSERPPageUSD * float64(returned), Units: float64(returned), Estimated: true}
	out.Partial = resp.PagesStopped != "" && resp.PagesStopped != "no_results"
	// Positions count down the pages in order, so they do not depend on
	// how Geonode numbers a page after the first.
	for _, p := range resp.Pages {
		for _, r := range p.Results {
			d := c.ResultDomain(r.URL)
			if d == "" {
				continue
			}
			out.Items = append(out.Items, c.SERPItem{Position: len(out.Items) + 1, URL: r.URL, Domain: d, Title: strings.TrimSpace(r.Title)})
		}
	}
	return out, nil
}

// geonodeKind sorts Geonode's error codes into ours.
func geonodeKind(code string) string {
	switch code {
	case "INVALID_URL", "INVALID_REQUEST", "VALIDATION_ERROR", "AUTH_REQUIRED", "UNPROCESSABLE_CONTENT":
		return c.ErrInvalid
	case "UNAUTHORIZED", "INVALID_API_KEY":
		return c.ErrAuth
	case "PAYMENT_REQUIRED", "RATE_LIMITED", "WORK_CONCURRENCY_LIMITED":
		return c.ErrQuota
	}
	return c.ErrUnavailable
}

// post sends a JSON request and returns the status and body, up to 16 MB.
func (g Geonode) post(ctx context.Context, cl *c.Client, creds c.Creds, path string, payload map[string]any) (int, []byte, error) {
	return g.postWithin(ctx, cl, creds, path, payload, 0)
}

// postWithin is post, waiting up to wait for the answer (zero: the
// client's own timeout).
func (g Geonode) postWithin(ctx context.Context, cl *c.Client, creds c.Creds, path string, payload map[string]any, wait time.Duration) (int, []byte, error) {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cl.Base("geonode", geonodeAPI)+path, strings.NewReader(string(body)))
	if err != nil {
		return 0, nil, c.Errf(c.ErrInvalid, "bad Geonode address: %v", err)
	}
	for h, v := range g.header(creds) {
		req.Header[h] = v
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := cl.DoWithin(req, wait)
	if err != nil {
		return 0, nil, c.Errf(c.ErrUnavailable, "could not reach Geonode: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return 0, nil, c.Errf(c.ErrUnavailable, "reading the Geonode response: %v", err)
	}
	return resp.StatusCode, raw, nil
}
