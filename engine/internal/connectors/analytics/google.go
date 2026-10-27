package analytics

import (
	"context"
	"net/url"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

const (
	gaData       = "https://analyticsdata.googleapis.com"
	gaAdmin      = "https://analyticsadmin.googleapis.com"
	googleUser   = "https://openidconnect.googleapis.com"
	searchSearch = "https://searchconsole.googleapis.com"
)

// googleAccount is the signed-in Google account, or the service account
// when connected with a key.
func googleAccount(ctx context.Context, cl *c.Client, name string, creds c.Creds) (c.Account, error) {
	if sa := creds["service_account_email"]; sa != "" {
		return c.Account{ID: sa, Name: "Service account", Email: sa, Details: map[string]string{}}, nil
	}
	token := creds["access_token"]
	var me struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := newCall(cl, name).bearer(token).get(ctx, cl.Base("google.userinfo", googleUser)+"/v1/userinfo", &me); err != nil {
		return c.Account{}, err
	}
	return c.Account{ID: me.Sub, Name: me.Name, Email: me.Email, Details: map[string]string{}}, nil
}

// serviceAccount is the key field, with where to give the account access.
func serviceAccount(grant string) c.Field {
	f := c.ServiceAccountField
	f.Help = "For servers without Google sign-in. In Google Cloud: IAM & Admin, Service accounts, Keys, Add key, JSON. " + grant
	return f
}

// ---- Google Analytics 4 ----

// GA4 reads Google Analytics 4 properties through the Data API.
type GA4 struct{}

func (GA4) Definition() c.Definition {
	d := def("google_analytics", "Google Analytics", "https://analytics.google.com",
		"https://developers.google.com/analytics/devguides/reporting/data/v1", "",
		"Visitors, top pages, traffic sources and key events from Google Analytics 4.",
		"Signs in with Google (or uses a service account key) and reads any GA4 property the account can see, with read-only access. Pick the property once, or name it on each call. Conversions are GA4 key events. The same sign-in also connects Search Console.",
		"Free", []c.Field{})
	d.Auth, d.AuthMethods, d.OAuth, d.AlsoConnects = c.AuthOAuth2, c.Both, googleOAuth(), []string{"google_search_console"}
	d.Fields = []c.Field{serviceAccount("In Google Analytics: Admin, Property access management, add the service account's email as a Viewer.")}
	return d
}

func (GA4) Reports() []string {
	return []string{c.ReportTraffic, c.ReportPages, c.ReportSources, c.ReportEvents}
}

func (g GA4) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	acct, err := googleAccount(ctx, cl, "Google Analytics", creds)
	if err != nil {
		return acct, err
	}
	sites, err := g.Sites(ctx, cl, creds)
	if err != nil {
		return acct, err
	}
	acct.Details["properties"] = itoa(len(sites))
	return acct, nil
}

func (GA4) Sites(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AnalyticsSite, error) {
	k := newCall(cl, "Google Analytics").bearer(creds["access_token"])
	out := []c.AnalyticsSite{}
	page := ""
	for i := 0; i < 20; i++ {
		var resp struct {
			AccountSummaries []struct {
				DisplayName       string `json:"displayName"`
				PropertySummaries []struct {
					Property    string `json:"property"`
					DisplayName string `json:"displayName"`
				} `json:"propertySummaries"`
			} `json:"accountSummaries"`
			NextPageToken string `json:"nextPageToken"`
		}
		q := url.Values{"pageSize": {"200"}}
		if page != "" {
			q.Set("pageToken", page)
		}
		if err := k.get(ctx, cl.Base("google_analytics.admin", gaAdmin)+"/v1beta/accountSummaries?"+q.Encode(), &resp); err != nil {
			return nil, err
		}
		for _, a := range resp.AccountSummaries {
			for _, p := range a.PropertySummaries {
				name := p.DisplayName
				if a.DisplayName != "" {
					name += " (" + a.DisplayName + ")"
				}
				out = append(out, c.AnalyticsSite{ID: strings.TrimPrefix(p.Property, "properties/"), Name: name})
			}
		}
		if page = resp.NextPageToken; page == "" {
			break
		}
	}
	return out, nil
}

type gaReport struct {
	MetricHeaders []struct {
		Name string `json:"name"`
	} `json:"metricHeaders"`
	Rows []struct {
		DimensionValues []struct {
			Value string `json:"value"`
		} `json:"dimensionValues"`
		MetricValues []struct {
			Value string `json:"value"`
		} `json:"metricValues"`
	} `json:"rows"`
	Totals []struct {
		MetricValues []struct {
			Value string `json:"value"`
		} `json:"metricValues"`
	} `json:"totals"`
}

func gaMetrics(names []string, values []string) c.Metrics {
	var m c.Metrics
	for i, n := range names {
		if i >= len(values) {
			break
		}
		v := c.ParseNum(values[i])
		switch n {
		case "totalUsers":
			m.Visitors = v
		case "screenPageViews":
			m.Pageviews = v
		case "sessions":
			m.Sessions = v
		case "bounceRate":
			m.BounceRate = v
		case "averageSessionDuration":
			m.AvgDuration = v
		case "eventCount":
			m.Events = v
		case "keyEvents":
			m.Conversions = v
		}
	}
	return m
}

func (GA4) Report(ctx context.Context, cl *c.Client, creds c.Creds, q c.AnalyticsQuery) (c.AnalyticsReport, error) {
	if err := checkSite("Google Analytics", q.Site, digits); err != nil {
		return c.AnalyticsReport{}, err
	}
	metric := func(names ...string) []map[string]string {
		out := []map[string]string{}
		for _, n := range names {
			out = append(out, map[string]string{"name": n})
		}
		return out
	}
	body := map[string]any{
		"dateRanges":         []map[string]string{{"startDate": q.From, "endDate": q.To}},
		"metricAggregations": []string{"TOTAL"},
		"limit":              q.Limit,
	}
	var dim, order string
	switch q.Report {
	case c.ReportTraffic:
		dim = "date"
		body["metrics"] = metric("totalUsers", "screenPageViews", "sessions", "bounceRate", "averageSessionDuration", "keyEvents")
		body["orderBys"] = []any{map[string]any{"dimension": map[string]string{"dimensionName": "date"}}}
		body["limit"] = 10000
	case c.ReportPages:
		dim, order = "pagePath", "screenPageViews"
		body["metrics"] = metric("screenPageViews", "totalUsers", "sessions")
	case c.ReportSources:
		dim, order = "sessionSourceMedium", "sessions"
		body["metrics"] = metric("sessions", "totalUsers", "keyEvents")
	case c.ReportEvents:
		dim, order = "eventName", "eventCount"
		body["metrics"] = metric("eventCount", "totalUsers", "keyEvents")
	default:
		return c.AnalyticsReport{}, c.NotSupported("Google Analytics", q.Report)
	}
	body["dimensions"] = []map[string]string{{"name": dim}}
	if order != "" {
		body["orderBys"] = []any{map[string]any{"metric": map[string]string{"metricName": order}, "desc": true}}
	}
	var resp gaReport
	u := cl.Base("google_analytics", gaData) + "/v1beta/properties/" + q.Site + ":runReport"
	if err := newCall(cl, "Google Analytics").bearer(creds["access_token"]).post(ctx, u, body, &resp); err != nil {
		return c.AnalyticsReport{}, err
	}
	names := make([]string, len(resp.MetricHeaders))
	for i, h := range resp.MetricHeaders {
		names[i] = h.Name
	}
	values := func(vs []struct {
		Value string `json:"value"`
	}) []string {
		out := make([]string, len(vs))
		for i, v := range vs {
			out[i] = v.Value
		}
		return out
	}
	rep := c.AnalyticsReport{Report: q.Report, Site: q.Site, From: q.From, To: q.To}
	if len(resp.Totals) > 0 {
		rep.Totals = gaMetrics(names, values(resp.Totals[0].MetricValues))
	}
	if q.Report == c.ReportTraffic {
		got := map[string]c.Metrics{}
		for _, r := range resp.Rows {
			if len(r.DimensionValues) > 0 {
				got[expand(r.DimensionValues[0].Value)] = gaMetrics(names, values(r.MetricValues))
			}
		}
		rep.Series = fillDays(q, got)
		return rep, nil
	}
	rep.Rows = []c.AnalyticsRow{}
	for _, r := range resp.Rows {
		if len(r.DimensionValues) > 0 {
			rep.Rows = append(rep.Rows, c.AnalyticsRow{Key: r.DimensionValues[0].Value, Metrics: gaMetrics(names, values(r.MetricValues))})
		}
	}
	if q.Report == c.ReportEvents {
		rep.Notes = append(rep.Notes, "Conversions are the events marked as key events in Google Analytics.")
	}
	return rep, nil
}

// ---- Google Search Console ----

// SearchConsole reads Search Console performance: clicks, impressions,
// click-through rate and position, by day, page and query.
type SearchConsole struct{}

func (SearchConsole) Definition() c.Definition {
	d := def("google_search_console", "Google Search Console", "https://search.google.com/search-console",
		"https://developers.google.com/webmaster-tools/v1/searchanalytics/query", "",
		"What people search on Google before they click through to your site, and how you rank.",
		"Signs in with Google (or uses a service account key) and reads every Search Console property the account can see, with read-only access. Domain properties show as sc-domain:example.com. The same sign-in also connects Google Analytics.",
		"Free", []c.Field{})
	d.Auth, d.AuthMethods, d.OAuth, d.AlsoConnects = c.AuthOAuth2, c.Both, googleOAuth(), []string{"google_analytics"}
	d.Fields = []c.Field{serviceAccount("In Search Console: Settings, Users and permissions, add the service account's email with Restricted access.")}
	return d
}

func (SearchConsole) Reports() []string {
	return []string{c.ReportTraffic, c.ReportPages, c.ReportSearchQueries}
}

func (s SearchConsole) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	acct, err := googleAccount(ctx, cl, "Google Search Console", creds)
	if err != nil {
		return acct, err
	}
	sites, err := s.Sites(ctx, cl, creds)
	if err != nil {
		return acct, err
	}
	acct.Details["properties"] = itoa(len(sites))
	return acct, nil
}

func (SearchConsole) Sites(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AnalyticsSite, error) {
	var resp struct {
		SiteEntry []struct {
			SiteURL         string `json:"siteUrl"`
			PermissionLevel string `json:"permissionLevel"`
		} `json:"siteEntry"`
	}
	if err := newCall(cl, "Google Search Console").bearer(creds["access_token"]).get(ctx, cl.Base("google_search_console", searchSearch)+"/webmasters/v3/sites", &resp); err != nil {
		return nil, err
	}
	out := []c.AnalyticsSite{}
	for _, e := range resp.SiteEntry {
		if e.PermissionLevel == "siteUnverifiedUser" {
			continue
		}
		name := e.SiteURL
		if d, ok := strings.CutPrefix(e.SiteURL, "sc-domain:"); ok {
			name = d + " (whole domain)"
		}
		out = append(out, c.AnalyticsSite{ID: e.SiteURL, Name: name, URL: strings.TrimPrefix(e.SiteURL, "sc-domain:")})
	}
	return out, nil
}

type gscRow struct {
	Keys        []string `json:"keys"`
	Clicks      float64  `json:"clicks"`
	Impressions float64  `json:"impressions"`
	CTR         float64  `json:"ctr"`
	Position    float64  `json:"position"`
}

func (r gscRow) metrics() c.Metrics {
	return c.Metrics{Clicks: c.Num(r.Clicks), Impressions: c.Num(r.Impressions), CTR: c.Num(r.CTR), Position: c.Num(r.Position)}
}

func (SearchConsole) query(ctx context.Context, cl *c.Client, creds c.Creds, q c.AnalyticsQuery, dim string, limit int) ([]gscRow, error) {
	body := map[string]any{"startDate": q.From, "endDate": q.To, "rowLimit": limit, "dataState": "all"}
	if dim != "" {
		body["dimensions"] = []string{dim}
	}
	var resp struct {
		Rows []gscRow `json:"rows"`
	}
	u := cl.Base("google_search_console", searchSearch) + "/webmasters/v3/sites/" + url.PathEscape(q.Site) + "/searchAnalytics/query"
	err := newCall(cl, "Google Search Console").bearer(creds["access_token"]).post(ctx, u, body, &resp)
	return resp.Rows, err
}

func (s SearchConsole) Report(ctx context.Context, cl *c.Client, creds c.Creds, q c.AnalyticsQuery) (c.AnalyticsReport, error) {
	if q.Site == "" || len(q.Site) > 300 {
		return c.AnalyticsReport{}, c.Errf(c.ErrInvalid, "name a Search Console property, like https://example.com/ or sc-domain:example.com")
	}
	rep := c.AnalyticsReport{Report: q.Report, Site: q.Site, From: q.From, To: q.To,
		Notes: []string{"Search Console data is usually two to three days behind; the latest days may still change."}}
	total, err := s.query(ctx, cl, creds, q, "", 1)
	if err != nil {
		return rep, err
	}
	if len(total) > 0 {
		rep.Totals = total[0].metrics()
	} else {
		rep.Totals = c.Metrics{Clicks: c.Num(0), Impressions: c.Num(0)}
	}
	var dim string
	switch q.Report {
	case c.ReportTraffic:
		rows, err := s.query(ctx, cl, creds, q, "date", 5000)
		if err != nil {
			return rep, err
		}
		got := map[string]c.Metrics{}
		for _, r := range rows {
			if len(r.Keys) > 0 {
				got[r.Keys[0]] = r.metrics()
			}
		}
		rep.Series = fillDays(q, got)
		return rep, nil
	case c.ReportPages:
		dim = "page"
	case c.ReportSearchQueries:
		dim = "query"
		rep.Notes = append(rep.Notes, "Google hides rare queries for privacy, so the queries add up to less than the total.")
	default:
		return rep, c.NotSupported("Google Search Console", q.Report)
	}
	rows, err := s.query(ctx, cl, creds, q, dim, q.Limit)
	if err != nil {
		return rep, err
	}
	rep.Rows = []c.AnalyticsRow{}
	for _, r := range rows {
		if len(r.Keys) > 0 {
			rep.Rows = append(rep.Rows, c.AnalyticsRow{Key: r.Keys[0], Metrics: r.metrics()})
		}
	}
	return rep, nil
}

func itoa(n int) string { return str(float64(n)) }

// searchPage is the most rows Search Console sends in one answer.
const searchPage = 25000

// SearchRows reads one day by page and query, a page of rows at a time.
func (s SearchConsole) SearchRows(ctx context.Context, cl *c.Client, creds c.Creds, site, day string, limit int) ([]c.SearchRow, error) {
	if site == "" || len(site) > 300 {
		return nil, c.Errf(c.ErrInvalid, "name a Search Console property, like https://example.com/ or sc-domain:example.com")
	}
	u := cl.Base("google_search_console", searchSearch) + "/webmasters/v3/sites/" + url.PathEscape(site) + "/searchAnalytics/query"
	out := []c.SearchRow{}
	for len(out) < limit {
		n := min(searchPage, limit-len(out))
		body := map[string]any{"startDate": day, "endDate": day, "dimensions": []string{"page", "query"},
			"rowLimit": n, "startRow": len(out), "dataState": "all"}
		var resp struct {
			Rows []gscRow `json:"rows"`
		}
		if err := newCall(cl, "Google Search Console").bearer(creds["access_token"]).post(ctx, u, body, &resp); err != nil {
			return out, err
		}
		for _, r := range resp.Rows {
			if len(r.Keys) == 2 {
				out = append(out, c.SearchRow{Page: r.Keys[0], Query: r.Keys[1], Clicks: r.Clicks, Impressions: r.Impressions, CTR: r.CTR, Position: r.Position})
			}
		}
		if len(resp.Rows) < n {
			break
		}
	}
	return out, nil
}

// InspectURL calls the URL Inspection API, which allows about 2,000
// inspections a day for each property.
func (SearchConsole) InspectURL(ctx context.Context, cl *c.Client, creds c.Creds, site, page string) (c.URLInspection, error) {
	var resp struct {
		InspectionResult struct {
			Link  string `json:"inspectionResultLink"`
			Index struct {
				Verdict         string   `json:"verdict"`
				CoverageState   string   `json:"coverageState"`
				RobotsTxtState  string   `json:"robotsTxtState"`
				IndexingState   string   `json:"indexingState"`
				LastCrawlTime   string   `json:"lastCrawlTime"`
				PageFetchState  string   `json:"pageFetchState"`
				GoogleCanonical string   `json:"googleCanonical"`
				UserCanonical   string   `json:"userCanonical"`
				Sitemap         []string `json:"sitemap"`
				CrawledAs       string   `json:"crawledAs"`
			} `json:"indexStatusResult"`
			Mobile struct {
				Verdict string `json:"verdict"`
			} `json:"mobileUsabilityResult"`
			Rich struct {
				Verdict string `json:"verdict"`
			} `json:"richResultsResult"`
		} `json:"inspectionResult"`
	}
	body := map[string]any{"inspectionUrl": page, "siteUrl": site}
	err := newCall(cl, "Google Search Console").bearer(creds["access_token"]).
		post(ctx, cl.Base("google_search_console", searchSearch)+"/v1/urlInspection/index:inspect", body, &resp)
	if err != nil {
		return c.URLInspection{}, err
	}
	r, ix := resp.InspectionResult, resp.InspectionResult.Index
	sitemaps := ix.Sitemap
	if sitemaps == nil {
		sitemaps = []string{}
	}
	return c.URLInspection{URL: page, Verdict: ix.Verdict, Coverage: ix.CoverageState, Indexing: ix.IndexingState,
		RobotsTxt: ix.RobotsTxtState, PageFetch: ix.PageFetchState, GoogleCanonical: ix.GoogleCanonical,
		UserCanonical: ix.UserCanonical, LastCrawl: ix.LastCrawlTime, CrawledAs: ix.CrawledAs, Sitemaps: sitemaps,
		Mobile: r.Mobile.Verdict, RichResults: r.Rich.Verdict, Link: r.Link}, nil
}

// Sitemaps lists what the property's sitemaps report.
func (SearchConsole) Sitemaps(ctx context.Context, cl *c.Client, creds c.Creds, site string) ([]c.SitemapStatus, error) {
	var resp struct {
		Sitemap []struct {
			Path            string `json:"path"`
			LastSubmitted   string `json:"lastSubmitted"`
			LastDownloaded  string `json:"lastDownloaded"`
			IsPending       bool   `json:"isPending"`
			IsSitemapsIndex bool   `json:"isSitemapsIndex"`
			Errors          any    `json:"errors"`
			Warnings        any    `json:"warnings"`
			Contents        []struct {
				Submitted any `json:"submitted"`
				Indexed   any `json:"indexed"`
			} `json:"contents"`
		} `json:"sitemap"`
	}
	u := cl.Base("google_search_console", searchSearch) + "/webmasters/v3/sites/" + url.PathEscape(site) + "/sitemaps"
	if err := newCall(cl, "Google Search Console").bearer(creds["access_token"]).get(ctx, u, &resp); err != nil {
		return nil, err
	}
	n := func(v any) int {
		if f := num(v); f != nil {
			return int(*f)
		}
		return 0
	}
	out := []c.SitemapStatus{}
	for _, m := range resp.Sitemap {
		x := c.SitemapStatus{Path: m.Path, LastSubmitted: m.LastSubmitted, LastRead: m.LastDownloaded, Pending: m.IsPending,
			Index: m.IsSitemapsIndex, Errors: n(m.Errors), Warnings: n(m.Warnings)}
		for _, ct := range m.Contents {
			x.Submitted += n(ct.Submitted)
			x.Indexed += n(ct.Indexed)
		}
		out = append(out, x)
	}
	return out, nil
}
