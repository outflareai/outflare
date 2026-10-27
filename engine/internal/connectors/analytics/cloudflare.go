package analytics

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

const cloudflareAPI = "https://api.cloudflare.com/client/v4"

// Cloudflare reads Cloudflare Web Analytics (the free, cookie-less
// beacon) through the GraphQL Analytics API.
type Cloudflare struct{}

func (Cloudflare) Definition() c.Definition {
	d := def("cloudflare_web_analytics", "Cloudflare Web Analytics", "https://www.cloudflare.com/web-analytics/",
		"https://developers.cloudflare.com/analytics/graphql-api/", "https://dash.cloudflare.com/profile/api-tokens",
		"Page views, visits, top pages and referrers from Cloudflare's free Web Analytics.",
		"Reads every Web Analytics site on one Cloudflare account. Cloudflare counts visits and page views but not unique visitors.",
		"Free", []c.Field{
			{Name: "api_token", Label: "API token", Secret: true, Help: "My Profile > API Tokens > Create token, with Account > Account Analytics > Read. To see AI crawlers in your request logs too, add Zone > Zone > Read and Zone > Analytics > Read."},
			{Name: "account_id", Label: "Account id", Help: "The id in your dashboard's address: dash.cloudflare.com/<account id>."},
		})
	// Cloudflare OAuth clients (2026). Scope names follow API token
	// permission names; CONNECTOR_CLOUDFLARE_WEB_ANALYTICS_SCOPES replaces
	// them if the client was set up with others.
	d.Capabilities = append(d.Capabilities, c.CapBotLogs)
	d.Auth, d.AuthMethods = c.AuthOAuth2, c.Both
	d.OAuth = &c.OAuth{
		AuthURL: "https://dash.cloudflare.com/oauth2/auth", TokenURL: "https://dash.cloudflare.com/oauth2/token",
		Scopes: []string{"offline_access", "account.read", "account_analytics.read"},
	}
	return d
}

func cfToken(creds c.Creds) string { return first2(creds["access_token"], creds["api_token"]) }

// findAccount picks the account a sign-in reads: the first one with Web
// Analytics sites, else the first. It stores it in creds["account_id"].
func (cf Cloudflare) findAccount(ctx context.Context, cl *c.Client, creds c.Creds) error {
	var resp struct {
		Result []struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	if err := newCall(cl, "Cloudflare").bearer(cfToken(creds)).get(ctx, cl.Base("cloudflare_web_analytics", cloudflareAPI)+"/accounts?per_page=50", &resp); err != nil {
		return err
	}
	if len(resp.Result) == 0 {
		return c.Errf(c.ErrAuth, "the sign-in did not grant any Cloudflare account; connect again and pick one")
	}
	for _, a := range resp.Result {
		try := c.Creds{"access_token": creds["access_token"], "account_id": a.ID}
		if sites, err := cf.Sites(ctx, cl, try); err == nil && len(sites) > 0 {
			creds["account_id"] = a.ID
			return nil
		}
	}
	creds["account_id"] = resp.Result[0].ID
	return nil
}

func (Cloudflare) Reports() []string {
	return []string{c.ReportTraffic, c.ReportPages, c.ReportSources}
}

func (cf Cloudflare) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	if creds["access_token"] != "" && creds["account_id"] == "" {
		if err := cf.findAccount(ctx, cl, creds); err != nil {
			return c.Account{}, err
		}
	}
	sites, err := cf.Sites(ctx, cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	return c.Account{ID: creds["account_id"], Name: "Cloudflare account", Details: map[string]string{"sites": itoa(len(sites))}}, nil
}

func (Cloudflare) Sites(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AnalyticsSite, error) {
	if !safeID.MatchString(creds["account_id"]) {
		return nil, c.Errf(c.ErrInvalid, "the account id is the 32-character id in your dashboard's address")
	}
	var resp struct {
		Result []struct {
			SiteTag string `json:"site_tag"`
			Host    string `json:"host"`
			Ruleset struct {
				ZoneName string `json:"zone_name"`
			} `json:"ruleset"`
		} `json:"result"`
	}
	u := cl.Base("cloudflare_web_analytics", cloudflareAPI) + "/accounts/" + url.PathEscape(creds["account_id"]) + "/rum/site_info/list?per_page=100"
	if err := newCall(cl, "Cloudflare").bearer(cfToken(creds)).get(ctx, u, &resp); err != nil {
		return nil, err
	}
	out := []c.AnalyticsSite{}
	for _, s := range resp.Result {
		name := first2(s.Host, s.Ruleset.ZoneName, s.SiteTag)
		out = append(out, c.AnalyticsSite{ID: s.SiteTag, Name: name})
	}
	return out, nil
}

func first2(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

type cfGroup struct {
	Count float64 `json:"count"`
	Sum   struct {
		Visits float64 `json:"visits"`
	} `json:"sum"`
	Dimensions map[string]string `json:"dimensions"`
}

func (g cfGroup) metrics() c.Metrics {
	return c.Metrics{Pageviews: c.Num(g.Count), Sessions: c.Num(g.Sum.Visits)}
}

func (Cloudflare) groups(ctx context.Context, cl *c.Client, creds c.Creds, q c.AnalyticsQuery, dim, order string, limit int) ([]cfGroup, error) {
	dims := ""
	if dim != "" {
		dims = "dimensions { " + dim + " }"
	}
	query := fmt.Sprintf(`query R($acct: String!, $filter: AccountRumPageloadEventsAdaptiveGroupsFilter_InputObject) {
  viewer { accounts(filter: {accountTag: $acct}) {
    g: rumPageloadEventsAdaptiveGroups(filter: $filter, limit: %d%s) { count sum { visits } %s }
  } }
}`, limit, order, dims)
	from, _ := time.Parse(time.DateOnly, q.From)
	to, _ := time.Parse(time.DateOnly, dayAfter(q.To))
	vars := map[string]any{"acct": creds["account_id"], "filter": map[string]any{"AND": []map[string]any{
		{"datetime_geq": from.Format(time.RFC3339), "datetime_lt": to.Format(time.RFC3339)},
		{"siteTag": q.Site},
	}}}
	var resp struct {
		Data struct {
			Viewer struct {
				Accounts []struct {
					G []cfGroup `json:"g"`
				} `json:"accounts"`
			} `json:"viewer"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := newCall(cl, "Cloudflare").bearer(cfToken(creds)).post(ctx, cl.Base("cloudflare_web_analytics", cloudflareAPI)+"/graphql", map[string]any{"query": query, "variables": vars}, &resp); err != nil {
		return nil, err
	}
	if len(resp.Errors) > 0 {
		msg := resp.Errors[0].Message
		if strings.Contains(strings.ToLower(msg), "not authorized") || strings.Contains(strings.ToLower(msg), "permission") {
			return nil, c.Errf(c.ErrAuth, "Cloudflare refused: %s. The token needs Account Analytics > Read.", msg)
		}
		return nil, c.Errf(c.ErrInvalid, "Cloudflare: %s", msg)
	}
	if len(resp.Data.Viewer.Accounts) == 0 {
		return nil, c.Errf(c.ErrAuth, "the token cannot read Cloudflare account %s", creds["account_id"])
	}
	return resp.Data.Viewer.Accounts[0].G, nil
}

func (cf Cloudflare) Report(ctx context.Context, cl *c.Client, creds c.Creds, q c.AnalyticsQuery) (c.AnalyticsReport, error) {
	if err := checkSite("Cloudflare", q.Site, safeID); err != nil {
		return c.AnalyticsReport{}, err
	}
	rep := c.AnalyticsReport{Report: q.Report, Site: q.Site, From: q.From, To: q.To}
	tot, err := cf.groups(ctx, cl, creds, q, "", "", 1)
	if err != nil {
		return rep, err
	}
	if len(tot) > 0 {
		rep.Totals = tot[0].metrics()
	} else {
		rep.Totals = c.Metrics{Pageviews: c.Num(0), Sessions: c.Num(0)}
	}
	var dim, key string
	switch q.Report {
	case c.ReportTraffic:
		gs, err := cf.groups(ctx, cl, creds, q, "date", ", orderBy: [date_ASC]", 5000)
		if err != nil {
			return rep, err
		}
		got := map[string]c.Metrics{}
		for _, g := range gs {
			got[g.Dimensions["date"]] = g.metrics()
		}
		rep.Series = fillDays(q, got)
		return rep, nil
	case c.ReportPages:
		dim, key = "requestPath", "requestPath"
	case c.ReportSources:
		dim, key = "refererHost", "refererHost"
		rep.Notes = append(rep.Notes, "(none) means no referrer: typed in, bookmarked or from an app.")
	default:
		return rep, c.NotSupported("Cloudflare Web Analytics", q.Report)
	}
	gs, err := cf.groups(ctx, cl, creds, q, dim, ", orderBy: [count_DESC]", q.Limit)
	if err != nil {
		return rep, err
	}
	rep.Rows = []c.AnalyticsRow{}
	for _, g := range gs {
		k := g.Dimensions[key]
		if k == "" {
			k = "(none)"
		}
		rep.Rows = append(rep.Rows, c.AnalyticsRow{Key: k, Metrics: g.metrics()})
	}
	return rep, nil
}

// ---- Microsoft Clarity ----

const clarityAPI = "https://www.clarity.ms/export-data/api/v1"

// Clarity reads Microsoft Clarity's Data Export API, which only covers the
// last one to three days and allows ten calls a project a day.
type Clarity struct{}

func (Clarity) Definition() c.Definition {
	return def("microsoft_clarity", "Microsoft Clarity", "https://clarity.microsoft.com", "https://learn.microsoft.com/en-us/clarity/setup-and-installation/clarity-data-export-api", "",
		"Sessions, visitors and frustration signals (rage and dead clicks) from Clarity, for the last three days.",
		"Clarity's export API only covers the last one to three days and allows ten calls a day per project, so Outflare keeps each answer for six hours. Use it for a recent snapshot, not history.",
		"Free", []c.Field{{Name: "api_token", Label: "API token", Secret: true, Help: "Clarity > your project > Settings > Data Export > Generate new API token."}})
}

func (Clarity) Reports() []string { return []string{c.ReportTraffic, c.ReportPages} }

// CacheFor keeps Clarity answers long: the API allows ten calls a day.
func (Clarity) CacheFor() time.Duration { return 6 * time.Hour }

type clarityMetric struct {
	MetricName  string           `json:"metricName"`
	Information []map[string]any `json:"information"`
}

func (Clarity) insights(ctx context.Context, cl *c.Client, creds c.Creds, days int, dim string) ([]clarityMetric, error) {
	q := url.Values{"numOfDays": {itoa(days)}}
	if dim != "" {
		q.Set("dimension1", dim)
	}
	var out []clarityMetric
	err := newCall(cl, "Microsoft Clarity").bearer(creds["api_token"]).get(ctx, cl.Base("microsoft_clarity", clarityAPI)+"/project-live-insights?"+q.Encode(), &out)
	return out, err
}

func (cy Clarity) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	if _, err := cy.insights(ctx, cl, creds, 1, ""); err != nil {
		return c.Account{}, err
	}
	return c.Account{Name: "Clarity project"}, nil
}

func (Clarity) Sites(context.Context, *c.Client, c.Creds) ([]c.AnalyticsSite, error) {
	return single("project", "Clarity project"), nil
}

func (cy Clarity) Report(ctx context.Context, cl *c.Client, creds c.Creds, q c.AnalyticsQuery) (c.AnalyticsReport, error) {
	// Clarity counts back from now: one, two or three days.
	today := time.Now().UTC().Format(time.DateOnly)
	from, _ := time.Parse(time.DateOnly, q.From)
	days := int(time.Since(from).Hours()/24) + 1
	if days > 3 {
		days = 3
	}
	if days < 1 {
		days = 1
	}
	start := time.Now().UTC().AddDate(0, 0, -(days - 1)).Format(time.DateOnly)
	rep := c.AnalyticsReport{Report: q.Report, Site: "project", From: start, To: today,
		Notes: []string{"Clarity only exports the last three days, so this covers " + start + " to " + today + " whatever range you ask for."}}
	dim := ""
	if q.Report == c.ReportPages {
		dim = "URL"
	} else if q.Report != c.ReportTraffic {
		return rep, c.NotSupported("Microsoft Clarity", q.Report)
	}
	ms, err := cy.insights(ctx, cl, creds, days, dim)
	if err != nil {
		return rep, err
	}
	traffic := func(r map[string]any) c.Metrics {
		sessions := num(r["totalSessionCount"])
		m := c.Metrics{Sessions: sessions, Visitors: num(r["distinctUserCount"])}
		if pps := num(r["pagesPerSessionPercentage"]); pps != nil && sessions != nil {
			m.Pageviews = c.Num(float64(int64(*pps**sessions + 0.5)))
		}
		return m
	}
	rows := []c.AnalyticsRow{}
	for _, m := range ms {
		switch m.MetricName {
		case "Traffic":
			for _, r := range m.Information {
				if dim == "" {
					rep.Totals = rep.Totals.Add(traffic(r))
				} else {
					rows = append(rows, c.AnalyticsRow{Key: str(r["URL"]), Metrics: traffic(r)})
				}
			}
		case "RageClickCount", "DeadClickCount", "QuickbackClick", "ExcessiveScroll", "ScriptErrorCount", "ErrorClickCount":
			if dim == "" && len(m.Information) > 0 {
				if p := num(m.Information[0]["sessionsWithMetricPercentage"]); p != nil {
					rep.Notes = append(rep.Notes, fmt.Sprintf("%s: %.1f%% of sessions.", clarityLabel(m.MetricName), *p))
				}
			}
		}
	}
	if dim != "" {
		rep.Rows = sortRows(rows, q.Limit, bySessions)
	}
	return rep, nil
}

func clarityLabel(metric string) string {
	switch metric {
	case "RageClickCount":
		return "Rage clicks"
	case "DeadClickCount":
		return "Dead clicks"
	case "QuickbackClick":
		return "Quick backs"
	case "ExcessiveScroll":
		return "Excessive scrolling"
	case "ScriptErrorCount":
		return "Script errors"
	case "ErrorClickCount":
		return "Error clicks"
	}
	return metric
}

// maxBotZones is how many of an account's zones bot logs read.
const maxBotZones = 20

// BotHits reads one day of requests by the given crawlers from every zone
// on the account, through the GraphQL Analytics API's HTTP request groups.
// It needs Zone > Analytics > Read (and Zone > Zone > Read to list zones);
// without them it answers ErrAuth and the rest of the connection works on.
func (cf Cloudflare) BotHits(ctx context.Context, cl *c.Client, creds c.Creds, agents []string, day string) ([]c.BotHits, error) {
	if !safeID.MatchString(creds["account_id"]) {
		return nil, c.Errf(c.ErrInvalid, "the account id is the 32-character id in your dashboard's address")
	}
	from, err := time.Parse(time.DateOnly, day)
	if err != nil || len(agents) == 0 {
		return nil, c.Errf(c.ErrInvalid, "a day (YYYY-MM-DD) and at least one agent are needed")
	}
	base := cl.Base("cloudflare_web_analytics", cloudflareAPI)
	var zones struct {
		Result []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"result"`
	}
	if err := newCall(cl, "Cloudflare").bearer(cfToken(creds)).get(ctx, base+"/zones?per_page=50&account.id="+url.QueryEscape(creds["account_id"]), &zones); err != nil {
		if c.KindOf(err) == c.ErrAuth {
			return nil, c.Errf(c.ErrAuth, "Cloudflare refused to list zones. Bot logs need a token with Zone > Zone > Read and Zone > Analytics > Read.")
		}
		return nil, err
	}
	like := make([]map[string]any, 0, len(agents))
	for _, a := range agents {
		like = append(like, map[string]any{"userAgent_like": "%" + a + "%"})
	}
	const query = `query B($zone: String!, $filter: ZoneHttpRequestsAdaptiveGroupsFilter_InputObject) {
  viewer { zones(filter: {zoneTag: $zone}) {
    g: httpRequestsAdaptiveGroups(filter: $filter, limit: 5000, orderBy: [count_DESC]) { count dimensions { userAgent clientRequestHTTPHost } }
  } }
}`
	filter := map[string]any{"AND": []map[string]any{
		{"datetime_geq": from.Format(time.RFC3339), "datetime_lt": from.AddDate(0, 0, 1).Format(time.RFC3339)},
		{"OR": like},
	}}
	out := []c.BotHits{}
	for i, z := range zones.Result {
		if i >= maxBotZones {
			break
		}
		if !safeID.MatchString(z.ID) {
			continue
		}
		var resp struct {
			Data struct {
				Viewer struct {
					Zones []struct {
						G []struct {
							Count      int64 `json:"count"`
							Dimensions struct {
								UserAgent string `json:"userAgent"`
								Host      string `json:"clientRequestHTTPHost"`
							} `json:"dimensions"`
						} `json:"g"`
					} `json:"zones"`
				} `json:"viewer"`
			} `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		vars := map[string]any{"zone": z.ID, "filter": filter}
		if err := newCall(cl, "Cloudflare").bearer(cfToken(creds)).post(ctx, base+"/graphql", map[string]any{"query": query, "variables": vars}, &resp); err != nil {
			return nil, err
		}
		if len(resp.Errors) > 0 {
			msg := strings.ToLower(resp.Errors[0].Message)
			if strings.Contains(msg, "not authorized") || strings.Contains(msg, "permission") {
				return nil, c.Errf(c.ErrAuth, "Cloudflare refused the zone's request logs. Bot logs need Zone > Analytics > Read.")
			}
			// A zone whose plan keeps no data for the day, or lacks the
			// user-agent dimension, has nothing to give; the others may.
			continue
		}
		for _, zz := range resp.Data.Viewer.Zones {
			for _, g := range zz.G {
				host := strings.ToLower(first2(g.Dimensions.Host, z.Name))
				out = append(out, c.BotHits{Host: host, Day: day, UserAgent: g.Dimensions.UserAgent, Requests: g.Count})
			}
		}
	}
	return out, nil
}
