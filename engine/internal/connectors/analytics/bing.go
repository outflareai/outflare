package analytics

import (
	"context"
	"net/url"
	"regexp"
	"strconv"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

const bingAPI = "https://ssl.bing.com/webmaster/api.svc/json"

// Bing reads Bing Webmaster Tools: clicks and impressions in Bing search,
// by day, page and query.
type Bing struct{}

func (Bing) Definition() c.Definition {
	return def("bing_webmaster", "Bing Webmaster Tools", "https://www.bing.com/webmasters",
		"https://learn.microsoft.com/en-us/dotnet/api/microsoft.bing.webmaster.api.interfaces.iwebmasterapi", "https://www.bing.com/webmasters/settings/api",
		"Clicks, impressions and the search queries people used on Bing before visiting your site.",
		"Reads every verified site in your Bing Webmaster Tools account. Bing keeps about six months of search data.",
		"Free", []c.Field{{Name: "api_key", Label: "API key", Secret: true, Help: "Bing Webmaster Tools > Settings > API access > API key."}})
}

func (Bing) Reports() []string {
	return []string{c.ReportTraffic, c.ReportPages, c.ReportSearchQueries}
}

func (b Bing) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	sites, err := b.Sites(ctx, cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	acct := c.Account{Name: "Bing Webmaster Tools", Details: map[string]string{"sites": itoa(len(sites))}}
	if len(sites) > 0 {
		acct.Name = sites[0].Name
	}
	return acct, nil
}

func (Bing) get(ctx context.Context, cl *c.Client, creds c.Creds, method string, q url.Values, out any) error {
	q.Set("apikey", creds["api_key"])
	return newCall(cl, "Bing Webmaster Tools").get(ctx, cl.Base("bing_webmaster", bingAPI)+"/"+method+"?"+q.Encode(), out)
}

func (b Bing) Sites(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AnalyticsSite, error) {
	var resp struct {
		D []struct {
			URL        string `json:"Url"`
			IsVerified bool   `json:"IsVerified"`
		} `json:"d"`
	}
	if err := b.get(ctx, cl, creds, "GetUserSites", url.Values{}, &resp); err != nil {
		return nil, err
	}
	out := []c.AnalyticsSite{}
	for _, s := range resp.D {
		if s.IsVerified {
			out = append(out, c.AnalyticsSite{ID: s.URL, Name: s.URL, URL: s.URL})
		}
	}
	return out, nil
}

var bingDate = regexp.MustCompile(`/Date\((-?\d+)`)

// bingDay turns "/Date(1729728000000-0700)/" into 2024-10-24.
func bingDay(s string) string {
	m := bingDate.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	ms, _ := strconv.ParseInt(m[1], 10, 64)
	return time.UnixMilli(ms).UTC().Format(time.DateOnly)
}

type bingStat struct {
	Query                 string  `json:"Query"`
	Date                  string  `json:"Date"`
	Clicks                float64 `json:"Clicks"`
	Impressions           float64 `json:"Impressions"`
	AvgImpressionPosition float64 `json:"AvgImpressionPosition"`
}

func (b Bing) Report(ctx context.Context, cl *c.Client, creds c.Creds, q c.AnalyticsQuery) (c.AnalyticsReport, error) {
	if q.Site == "" || len(q.Site) > 300 {
		return c.AnalyticsReport{}, c.Errf(c.ErrInvalid, "name a Bing site, like https://example.com/")
	}
	rep := c.AnalyticsReport{Report: q.Report, Site: q.Site, From: q.From, To: q.To,
		Notes: []string{"Bing keeps about six months of search data and reports queries and pages by week."}}
	in := func(d string) bool { return d != "" && d >= q.From && d <= q.To }
	if q.Narrowed() {
		return b.narrowed(ctx, cl, creds, q, rep, in)
	}
	var method string
	switch q.Report {
	case c.ReportTraffic:
		var resp struct {
			D []bingStat `json:"d"`
		}
		if err := b.get(ctx, cl, creds, "GetRankAndTrafficStats", url.Values{"siteUrl": {q.Site}}, &resp); err != nil {
			return rep, err
		}
		got := map[string]c.Metrics{}
		for _, s := range resp.D {
			if d := bingDay(s.Date); in(d) {
				got[d] = got[d].Add(c.Metrics{Clicks: c.Num(s.Clicks), Impressions: c.Num(s.Impressions)})
			}
		}
		rep.Series = fillDays(q, got)
		for i := range rep.Series {
			rep.Series[i].Metrics = search(rep.Series[i].Metrics)
		}
		rep.Totals = search(sumPoints(rep.Series))
		return rep, nil
	case c.ReportPages:
		method = "GetPageStats"
	case c.ReportSearchQueries:
		method = "GetQueryStats"
	default:
		return rep, c.NotSupported("Bing Webmaster Tools", q.Report)
	}
	var resp struct {
		D []bingStat `json:"d"`
	}
	if err := b.get(ctx, cl, creds, method, url.Values{"siteUrl": {q.Site}}, &resp); err != nil {
		return rep, err
	}
	// Sum each query or page over the weeks in range.
	sums := bingSums{}
	for _, s := range resp.D {
		if in(bingDay(s.Date)) {
			sums.add(s.Query, s)
		}
	}
	rows := sums.rows()
	rep.Totals = search(sumRows(rows))
	rep.Rows = sortRows(rows, q.Limit, byClicks)
	return rep, nil
}

// NarrowsSearch: Bing lists the queries of one page and the pages of one
// query.
func (Bing) NarrowsSearch() bool { return true }

// narrowed answers a report narrowed to one page or one search query. Bing
// lists the queries that showed one page (GetPageQueryStats) and the pages
// one query showed (GetQueryPageStats), by week, with the other side in
// Query.
func (b Bing) narrowed(ctx context.Context, cl *c.Client, creds c.Creds, q c.AnalyticsQuery, rep c.AnalyticsReport, in func(string) bool) (c.AnalyticsReport, error) {
	method, args := "GetPageQueryStats", url.Values{"siteUrl": {q.Site}, "page": {q.Page}}
	if q.Page == "" {
		method, args = "GetQueryPageStats", url.Values{"siteUrl": {q.Site}, "query": {q.Keyword}}
	}
	var resp struct {
		D []bingStat `json:"d"`
	}
	if err := b.get(ctx, cl, creds, method, args, &resp); err != nil {
		return rep, err
	}
	sums := bingSums{}
	for _, s := range resp.D {
		day := bingDay(s.Date)
		if !in(day) {
			continue
		}
		page, query := q.Page, s.Query
		if q.Page == "" {
			page, query = s.Query, q.Keyword
		}
		if q.Keyword != "" && query != q.Keyword {
			continue
		}
		switch q.Report {
		case c.ReportTraffic:
			sums.add(day, s)
		case c.ReportPages:
			sums.add(page, s)
		case c.ReportSearchQueries:
			sums.add(query, s)
		default:
			return rep, c.NotSupported("Bing Webmaster Tools", q.Report)
		}
	}
	rows := sums.rows()
	if q.Report != c.ReportTraffic {
		rep.Totals = search(sumRows(rows))
		rep.Rows = sortRows(rows, q.Limit, byClicks)
		return rep, nil
	}
	got := map[string]c.Metrics{}
	for _, r := range rows {
		got[r.Key] = r.Metrics
	}
	rep.Series = fillDays(q, got)
	for i := range rep.Series {
		rep.Series[i].Metrics = search(rep.Series[i].Metrics)
	}
	rep.Totals = search(sumRows(rows))
	return rep, nil
}

// bingSums adds Bing's weekly rows up by key; position is the
// impression-weighted average.
type bingSums map[string]*struct{ clicks, impr, posW float64 }

func (b bingSums) add(key string, s bingStat) {
	a := b[key]
	if a == nil {
		a = &struct{ clicks, impr, posW float64 }{}
		b[key] = a
	}
	a.clicks += s.Clicks
	a.impr += s.Impressions
	if s.AvgImpressionPosition > 0 {
		a.posW += s.AvgImpressionPosition * s.Impressions
	}
}

func (b bingSums) rows() []c.AnalyticsRow {
	rows := []c.AnalyticsRow{}
	for k, a := range b {
		m := c.Metrics{Clicks: c.Num(a.clicks), Impressions: c.Num(a.impr)}
		if a.impr > 0 && a.posW > 0 {
			m.Position = c.Num(a.posW / a.impr)
		}
		rows = append(rows, c.AnalyticsRow{Key: k, Metrics: search(m)})
	}
	return rows
}
