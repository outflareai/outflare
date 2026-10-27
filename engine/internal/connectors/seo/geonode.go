package seo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

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
		"Searches the web, finds your brands' social links, reads public X follower counts, and reads your own site's pages when a firewall blocks OutflareBot.",
		"Runs web searches for finding companies and pages, through Geonode's Search API. "+
			"Reads each brand's homepage to suggest its social profiles and app store listings, and the public X profile in each brand's links once a day for its follower count. "+
			"For sites you have proved you own, and only for pages robots.txt allows: when a page answers with a firewall block or a challenge, "+
			"OutflareBot reads it through Geonode's Scraper API instead. Searches and pages cost Geonode tokens on your account.",
		"Tokens per search and per page, on your Geonode plan", []c.Field{
			{Name: "api_key", Label: "API key", Secret: true, Help: "Geonode dashboard > Scraper API > API keys."},
		})
	d.Capabilities = []string{c.CapCrawlFetch, c.CapWebSearch, c.CapHooks}
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

// FetchPage reads one page with POST /v1/extract, waiting for the answer.
func (g Geonode) FetchPage(ctx context.Context, cl *c.Client, creds c.Creds, url string, renderJS bool) (c.FetchedPage, error) {
	status, raw, err := g.post(ctx, cl, creds, "/v1/extract", map[string]any{
		"url": url, "formats": []string{"html"}, "processing_mode": "sync", "render_js": renderJS, "timeout": 40000,
	})
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
	resp, err := cl.Do(req)
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
