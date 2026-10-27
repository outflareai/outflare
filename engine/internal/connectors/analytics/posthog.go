package analytics

import (
	"context"
	"fmt"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

const posthogCloud = "https://us.posthog.com"

const posthogOAuth = "https://oauth.posthog.com"

// PostHog reads web and product analytics with HogQL queries on a
// project's events. It signs in to PostHog Cloud (US or EU) with OAuth, or
// takes a personal API key, which also works for self-hosted PostHog.
// PostHog reads Outflare's OAuth client from a metadata document the
// server hosts, so nobody registers an app; it needs the server on a
// public https address.
type PostHog struct{}

func (PostHog) Definition() c.Definition {
	d := def("posthog", "PostHog", "https://posthog.com", "https://posthog.com/docs/api/queries", "https://us.posthog.com/settings/user-api-keys",
		"Visitors, pages, referrers and custom events from PostHog. Sign in with PostHog Cloud, US or EU, or use a personal API key.",
		"Signs in with PostHog and reads the projects you allow, with read-only access to queries. A personal API key works too, and is the way for self-hosted PostHog. Visitors are distinct people with a $pageview; sources are the referring domain.",
		"Free tier", []c.Field{
			{Name: "api_key", Label: "Personal API key", Secret: true, Placeholder: "phx_…", Help: "PostHog > Settings > Personal API keys. Give it the Query read and Project read scopes."},
			{Name: "host", Label: "PostHog address", Optional: true, Placeholder: posthogCloud, Help: "EU cloud: https://eu.posthog.com. Self-hosted: your own address."},
		})
	d.Auth, d.AuthMethods = c.AuthOAuth2, c.Both
	d.OAuth = &c.OAuth{
		AuthURL:        posthogOAuth + "/oauth/authorize/",
		TokenURL:       posthogOAuth + "/oauth/token/",
		Scopes:         []string{"openid", "user:read", "project:read", "query:read"},
		ClientMetadata: true,
	}
	d.Replaces = []string{"posthog_cloud"}
	return d
}

func (PostHog) Reports() []string {
	return []string{c.ReportTraffic, c.ReportPages, c.ReportSources, c.ReportEvents}
}

func (PostHog) open(cl *c.Client, creds c.Creds) (*call, string, error) {
	base, err := hostURL(cl, "posthog", creds["host"], posthogCloud)
	return newCall(cl, "PostHog").bearer(first2(creds["api_key"], creds["access_token"])), base, err
}

func (p PostHog) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	if creds["access_token"] != "" && creds["host"] == "" {
		return p.findRegion(ctx, cl, creds)
	}
	k, base, err := p.open(cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	var me struct {
		UUID         string `json:"uuid"`
		Email        string `json:"email"`
		FirstName    string `json:"first_name"`
		Organization struct {
			Name string `json:"name"`
		} `json:"organization"`
	}
	if err := k.get(ctx, base+"/api/users/@me/", &me); err != nil {
		return c.Account{}, err
	}
	return c.Account{ID: me.UUID, Name: me.Organization.Name, Email: me.Email}, nil
}

func (p PostHog) Sites(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AnalyticsSite, error) {
	k, base, err := p.open(cl, creds)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Results []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"results"`
	}
	if err := k.get(ctx, base+"/api/projects/?limit=200", &resp); err != nil {
		return nil, err
	}
	out := []c.AnalyticsSite{}
	for _, r := range resp.Results {
		out = append(out, c.AnalyticsSite{ID: fmt.Sprint(r.ID), Name: r.Name})
	}
	return out, nil
}

// hogql runs one query. Every value placed in SQL is a checked date or a
// number, never text from the caller.
func (p PostHog) hogql(ctx context.Context, cl *c.Client, creds c.Creds, project, sql string) ([][]any, error) {
	k, base, err := p.open(cl, creds)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Results [][]any `json:"results"`
	}
	body := map[string]any{"query": map[string]any{"kind": "HogQLQuery", "query": sql}, "name": "outflare analytics"}
	err = k.post(ctx, base+"/api/projects/"+project+"/query/", body, &resp)
	return resp.Results, err
}

func (p PostHog) Report(ctx context.Context, cl *c.Client, creds c.Creds, q c.AnalyticsQuery) (c.AnalyticsReport, error) {
	if err := checkSite("PostHog", q.Site, digits); err != nil {
		return c.AnalyticsReport{}, err
	}
	rep := c.AnalyticsReport{Report: q.Report, Site: q.Site, From: q.From, To: q.To}
	when := fmt.Sprintf("timestamp >= toDateTime('%s 00:00:00') AND timestamp < toDateTime('%s 00:00:00')", q.From, dayAfter(q.To))
	pageviews := "event = '$pageview' AND " + when
	run := func(sql string) ([][]any, error) { return p.hogql(ctx, cl, creds, q.Site, sql) }
	rows := func(res [][]any, m func([]any) c.Metrics) []c.AnalyticsRow {
		out := []c.AnalyticsRow{}
		for _, r := range res {
			if len(r) > 0 {
				key := str(r[0])
				if key == "" {
					key = "(none)"
				}
				out = append(out, c.AnalyticsRow{Key: key, Metrics: m(r)})
			}
		}
		return out
	}
	at := func(r []any, i int) *float64 {
		if i < len(r) {
			return num(r[i])
		}
		return nil
	}
	switch q.Report {
	case c.ReportTraffic:
		cols := "count(DISTINCT person_id), count(), count(DISTINCT properties.$session_id)"
		m := func(r []any, i int) c.Metrics {
			return c.Metrics{Visitors: at(r, i), Pageviews: at(r, i+1), Sessions: at(r, i+2)}
		}
		tot, err := run("SELECT " + cols + " FROM events WHERE " + pageviews)
		if err != nil {
			return rep, err
		}
		if len(tot) > 0 {
			rep.Totals = m(tot[0], 0)
		}
		res, err := run("SELECT toDate(timestamp) AS day, " + cols + " FROM events WHERE " + pageviews + " GROUP BY day ORDER BY day LIMIT 10000")
		if err != nil {
			return rep, err
		}
		got := map[string]c.Metrics{}
		for _, r := range res {
			if len(r) > 0 {
				got[expand(str(r[0]))] = m(r, 1)
			}
		}
		rep.Series = fillDays(q, got)
		return rep, nil
	case c.ReportPages:
		res, err := run(fmt.Sprintf("SELECT properties.$pathname AS path, count() AS views, count(DISTINCT person_id) FROM events WHERE %s GROUP BY path ORDER BY views DESC LIMIT %d", pageviews, q.Limit))
		if err != nil {
			return rep, err
		}
		rep.Rows = rows(res, func(r []any) c.Metrics { return c.Metrics{Pageviews: at(r, 1), Visitors: at(r, 2)} })
	case c.ReportSources:
		res, err := run(fmt.Sprintf("SELECT properties.$referring_domain AS source, count(DISTINCT properties.$session_id) AS visits, count(DISTINCT person_id) FROM events WHERE %s GROUP BY source ORDER BY visits DESC LIMIT %d", pageviews, q.Limit))
		if err != nil {
			return rep, err
		}
		rep.Rows = rows(res, func(r []any) c.Metrics { return c.Metrics{Sessions: at(r, 1), Visitors: at(r, 2)} })
		rep.Notes = append(rep.Notes, "$direct means no referrer: typed in, bookmarked or from an app.")
	case c.ReportEvents:
		res, err := run(fmt.Sprintf("SELECT event, count() AS n, count(DISTINCT person_id) FROM events WHERE %s AND event NOT IN ('$pageview', '$pageleave', '$autocapture', '$feature_flag_called', '$set', '$identify', '$groupidentify', '$web_vitals', '$rageclick', '$dead_click', '$exception', '$snapshot', '$opt_in') GROUP BY event ORDER BY n DESC LIMIT %d", when, q.Limit))
		if err != nil {
			return rep, err
		}
		rep.Rows = rows(res, func(r []any) c.Metrics { return c.Metrics{Events: at(r, 1), Visitors: at(r, 2)} })
		rep.Notes = append(rep.Notes, "PostHog's own events (page views, autocapture, flags) are left out so your custom events stand out.")
	default:
		return rep, c.NotSupported("PostHog", q.Report)
	}
	return rep, nil
}

// searchReferrer matches the referring domain of a search engine's
// results page.
const searchReferrer = `(^|[.])(google|bing|duckduckgo|yahoo|yandex|baidu|ecosia|naver|search[.]brave)[.]`

// PageConversions counts each visit (a session with a page view) under
// the page it started on, and whether it went on to one of the events.
// Event names are checked by ValidEventName, so quoting them is safe.
func (p PostHog) PageConversions(ctx context.Context, cl *c.Client, creds c.Creds, q c.PageConversionQuery) ([]c.PageConversionRow, error) {
	if err := checkSite("PostHog", q.Site, digits); err != nil {
		return nil, err
	}
	converted, filter := "0", "event = '$pageview'"
	if len(q.Events) > 0 {
		quoted := make([]string, 0, len(q.Events))
		for _, e := range q.Events {
			if !c.ValidEventName(e) {
				return nil, c.Errf(c.ErrInvalid, "PostHog: %q cannot be a conversion event", e)
			}
			quoted = append(quoted, "'"+e+"'")
		}
		in := "event IN (" + strings.Join(quoted, ", ") + ")"
		converted, filter = "countIf("+in+") > 0", "(event = '$pageview' OR "+in+")"
	}
	limit := q.Limit
	if limit <= 0 || limit > 50000 {
		limit = 50000
	}
	when := fmt.Sprintf("timestamp >= toDateTime('%s 00:00:00') AND timestamp < toDateTime('%s 00:00:00')", q.From, dayAfter(q.To))
	sql := fmt.Sprintf(`SELECT toDate(start) AS day, host, path, count() AS sessions, count(DISTINCT person) AS visitors,
	countIf(converted) AS conversions, countIf(search) AS search_sessions, countIf(search AND converted) AS search_conversions
FROM (
	SELECT properties.$session_id AS sid, min(timestamp) AS start,
		argMinIf(properties.$host, timestamp, event = '$pageview') AS host,
		argMinIf(properties.$pathname, timestamp, event = '$pageview') AS path,
		match(coalesce(argMinIf(properties.$referring_domain, timestamp, event = '$pageview'), ''), '%s') AS search,
		any(person_id) AS person, %s AS converted
	FROM events
	WHERE %s AND %s AND properties.$session_id IS NOT NULL
	GROUP BY sid
	HAVING countIf(event = '$pageview') > 0
)
GROUP BY day, host, path ORDER BY sessions DESC LIMIT %d`, searchReferrer, converted, when, filter, limit)
	res, err := p.hogql(ctx, cl, creds, q.Site, sql)
	if err != nil {
		return nil, err
	}
	n := func(r []any, i int) int {
		if v := num(r[i]); v != nil {
			return int(*v)
		}
		return 0
	}
	out := make([]c.PageConversionRow, 0, len(res))
	for _, r := range res {
		if len(r) < 8 {
			continue
		}
		out = append(out, c.PageConversionRow{Day: expand(str(r[0])), Host: strings.ToLower(str(r[1])), Path: str(r[2]),
			Sessions: n(r, 3), Visitors: n(r, 4), Conversions: n(r, 5), SearchSessions: n(r, 6), SearchConversions: n(r, 7)})
	}
	return out, nil
}

// findRegion finds a signed-in account's region: a token from the
// region-agnostic sign-in works on US or EU Cloud. It stores the answer in
// creds["host"].
func (p PostHog) findRegion(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var last error
	for _, region := range []struct{ id, url string }{{"us", posthogCloud}, {"eu", "https://eu.posthog.com"}} {
		try := c.Creds{"access_token": creds["access_token"], "host": cl.Base("posthog."+region.id, region.url)}
		acct, err := p.Test(ctx, cl, try)
		if err == nil {
			creds["host"] = try["host"]
			return acct, nil
		}
		last = err
	}
	return c.Account{}, last
}
