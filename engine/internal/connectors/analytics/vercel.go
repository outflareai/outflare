package analytics

import (
	"context"
	"net/url"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

const vercelAPI = "https://api.vercel.com"

// Vercel reads Vercel Web Analytics through the Web Analytics query API.
// Each Vercel project is a site.
type Vercel struct{}

func (Vercel) Definition() c.Definition {
	return def("vercel_analytics", "Vercel Web Analytics", "https://vercel.com/analytics",
		"https://vercel.com/docs/analytics/web-analytics-api", "https://vercel.com/account/settings/tokens",
		"Page views, visitors, top pages, referrers and custom events from Vercel Web Analytics.",
		"Reads every project on one Vercel account or team. Only production traffic counts, as on Vercel's own dashboard. Projects need Web Analytics turned on.",
		"Free on Hobby, more events on Pro", []c.Field{
			{Name: "api_token", Label: "Access token", Secret: true, Help: "Vercel > Account Settings > Tokens > Create, scoped to the team that owns your projects."},
			{Name: "team_id", Label: "Team id", Optional: true, Placeholder: "team_…", Help: "Team > Settings > General > Team ID. Leave empty for projects on your personal account."},
		})
}

func (Vercel) Reports() []string {
	return []string{c.ReportTraffic, c.ReportPages, c.ReportSources, c.ReportEvents}
}

func (v Vercel) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	sites, err := v.Sites(ctx, cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	name := "Vercel account"
	if creds["team_id"] != "" {
		name = "Vercel team " + creds["team_id"]
	}
	return c.Account{ID: first2(creds["team_id"], "personal"), Name: name, Details: map[string]string{"projects": itoa(len(sites))}}, nil
}

func (Vercel) scope(creds c.Creds, q url.Values) (url.Values, error) {
	if t := creds["team_id"]; t != "" {
		if !safeID.MatchString(t) {
			return nil, c.Errf(c.ErrInvalid, "the team id looks like team_ followed by letters and numbers")
		}
		q.Set("teamId", t)
	}
	return q, nil
}

func (v Vercel) Sites(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AnalyticsSite, error) {
	out := []c.AnalyticsSite{}
	from := ""
	for page := 0; page < 10; page++ {
		q, err := v.scope(creds, url.Values{"limit": {"100"}})
		if err != nil {
			return nil, err
		}
		if from != "" {
			q.Set("from", from)
		}
		var resp struct {
			Projects []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"projects"`
			Pagination struct {
				Next any `json:"next"`
			} `json:"pagination"`
		}
		if err := newCall(cl, "Vercel").bearer(creds["api_token"]).get(ctx, cl.Base("vercel_analytics", vercelAPI)+"/v9/projects?"+q.Encode(), &resp); err != nil {
			return nil, err
		}
		for _, p := range resp.Projects {
			out = append(out, c.AnalyticsSite{ID: p.ID, Name: p.Name})
		}
		from = str(resp.Pagination.Next)
		if from == "" || len(resp.Projects) == 0 {
			break
		}
	}
	return out, nil
}

// vercelRow reads one result row: the grouped dimension plus pageviews (or
// count, for events) and visitors.
func vercelRow(row map[string]any) c.Metrics {
	m := c.Metrics{Visitors: num(row["visitors"])}
	if v, ok := row["pageviews"]; ok {
		m.Pageviews = num(v)
	} else if v, ok := row["count"]; ok {
		m.Pageviews = num(v)
	}
	return m
}

func (v Vercel) get(ctx context.Context, cl *c.Client, creds c.Creds, path string, q url.Values, out any) error {
	q, err := v.scope(creds, q)
	if err != nil {
		return err
	}
	err = newCall(cl, "Vercel").bearer(creds["api_token"]).get(ctx, cl.Base("vercel_analytics", vercelAPI)+path+"?"+q.Encode(), out)
	if err != nil && strings.Contains(err.Error(), "web_analytics_not_enabled") {
		return c.Errf(c.ErrInvalid, "Web Analytics is not turned on for this Vercel project. Turn it on under the project's Analytics tab, or pick another project.")
	}
	return err
}

func (v Vercel) aggregate(ctx context.Context, cl *c.Client, creds c.Creds, q c.AnalyticsQuery, kind, by string, limit int) ([]map[string]any, error) {
	params := url.Values{"projectId": {q.Site}, "by": {by}, "since": {q.From}, "until": {q.To}}
	if limit > 0 {
		params.Set("limit", itoa(limit))
	}
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	err := v.get(ctx, cl, creds, "/v1/query/web-analytics/"+kind+"/aggregate", params, &resp)
	return resp.Data, err
}

func (v Vercel) Report(ctx context.Context, cl *c.Client, creds c.Creds, q c.AnalyticsQuery) (c.AnalyticsReport, error) {
	if err := checkSite("Vercel", q.Site, safeID); err != nil {
		return c.AnalyticsReport{}, err
	}
	rep := c.AnalyticsReport{Report: q.Report, Site: q.Site, From: q.From, To: q.To}
	var dim, kind string
	switch q.Report {
	case c.ReportTraffic:
		var tot struct {
			Data map[string]any `json:"data"`
		}
		if err := v.get(ctx, cl, creds, "/v1/query/web-analytics/visits/count", url.Values{"projectId": {q.Site}, "since": {q.From}, "until": {q.To}}, &tot); err != nil {
			return rep, err
		}
		rep.Totals = vercelRow(tot.Data)
		rows, err := v.aggregate(ctx, cl, creds, q, "visits", "day", 0)
		if err != nil {
			return rep, err
		}
		got := map[string]c.Metrics{}
		for _, r := range rows {
			d := first2(str(r["timestamp"]), str(r["day"]), str(r["date"]))
			if len(d) >= 10 {
				got[d[:10]] = vercelRow(r)
			}
		}
		rep.Series = fillDays(q, got)
		return rep, nil
	case c.ReportPages:
		dim, kind = "requestPath", "visits"
	case c.ReportSources:
		dim, kind = "referrerHostname", "visits"
		rep.Notes = append(rep.Notes, "(none) means no referrer: typed in, bookmarked or from an app.")
	case c.ReportEvents:
		dim, kind = "eventName", "events"
		rep.Notes = append(rep.Notes, "Custom events sent with track() from @vercel/analytics.")
	default:
		return rep, c.NotSupported("Vercel Web Analytics", q.Report)
	}
	rows, err := v.aggregate(ctx, cl, creds, q, kind, dim, min(q.Limit, 100))
	if err != nil {
		return rep, err
	}
	rep.Rows = []c.AnalyticsRow{}
	for _, r := range rows {
		k := str(r[dim])
		if k == "Others" {
			// Vercel folds everything past the limit into one "Others" group.
			continue
		}
		if k == "" {
			k = "(none)"
		}
		m := vercelRow(r)
		if kind == "events" {
			m = c.Metrics{Events: m.Pageviews, Visitors: m.Visitors}
		}
		rep.Rows = append(rep.Rows, c.AnalyticsRow{Key: k, Metrics: m})
	}
	if kind == "events" {
		// Visitors overlap between events, so only the event count adds up.
		rep.Totals = c.Metrics{Events: sumRows(rep.Rows).Events}
		return rep, nil
	}
	var tot struct {
		Data map[string]any `json:"data"`
	}
	if err := v.get(ctx, cl, creds, "/v1/query/web-analytics/visits/count", url.Values{"projectId": {q.Site}, "since": {q.From}, "until": {q.To}}, &tot); err != nil {
		return rep, err
	}
	rep.Totals = vercelRow(tot.Data)
	return rep, nil
}
