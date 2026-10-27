package analytics

import (
	"context"
	"net/url"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

const plausibleCloud = "https://plausible.io"

// Plausible reads the Plausible Stats API (v2).
type Plausible struct{}

func (Plausible) Definition() c.Definition {
	return def("plausible", "Plausible", "https://plausible.io", "https://plausible.io/docs/stats-api", "https://plausible.io/settings/api-keys",
		"Visitors, pages, sources and goal conversions from Plausible, cloud or self-hosted.",
		"Reads one site with a Stats API key. Goals count as conversions.",
		"Paid plans", []c.Field{
			{Name: "api_key", Label: "Stats API key", Secret: true, Help: "Plausible > Account settings > API keys > New API key > Stats API."},
			{Name: "site_id", Label: "Site domain", Placeholder: "example.com", Help: "The domain exactly as it appears in your Plausible dashboard."},
			{Name: "host", Label: "Plausible address", Optional: true, Placeholder: plausibleCloud, Help: "Only for self-hosted Plausible."},
		})
}

func (Plausible) Reports() []string {
	return []string{c.ReportTraffic, c.ReportPages, c.ReportSources, c.ReportEvents}
}

type plausibleResp struct {
	Results []struct {
		Dimensions []any `json:"dimensions"`
		Metrics    []any `json:"metrics"`
	} `json:"results"`
}

func (Plausible) query(ctx context.Context, cl *c.Client, creds c.Creds, body map[string]any) (plausibleResp, error) {
	var resp plausibleResp
	base, err := hostURL(cl, "plausible", creds["host"], plausibleCloud)
	if err != nil {
		return resp, err
	}
	body["site_id"] = creds["site_id"]
	err = newCall(cl, "Plausible").bearer(creds["api_key"]).post(ctx, base+"/api/v2/query", body, &resp)
	return resp, err
}

func (p Plausible) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	today := time.Now().UTC().Format(time.DateOnly)
	if _, err := p.query(ctx, cl, creds, map[string]any{"metrics": []string{"visitors"}, "date_range": []string{today, today}}); err != nil {
		return c.Account{}, err
	}
	return c.Account{ID: creds["site_id"], Name: creds["site_id"]}, nil
}

func (Plausible) Sites(_ context.Context, _ *c.Client, creds c.Creds) ([]c.AnalyticsSite, error) {
	s := creds["site_id"]
	return []c.AnalyticsSite{{ID: s, Name: s, URL: "https://" + strings.TrimPrefix(s, "https://")}}, nil
}

func plausibleMetrics(names []string, vals []any) c.Metrics {
	var m c.Metrics
	for i, n := range names {
		if i >= len(vals) {
			break
		}
		v := num(vals[i])
		switch n {
		case "visitors":
			m.Visitors = v
		case "pageviews":
			m.Pageviews = v
		case "visits":
			m.Sessions = v
		case "bounce_rate":
			m.BounceRate = pct(v)
		case "visit_duration":
			m.AvgDuration = v
		case "events":
			m.Events, m.Conversions = v, v
		}
	}
	return m
}

func (p Plausible) Report(ctx context.Context, cl *c.Client, creds c.Creds, q c.AnalyticsQuery) (c.AnalyticsReport, error) {
	rep := c.AnalyticsReport{Report: q.Report, Site: creds["site_id"], From: q.From, To: q.To}
	dates := []string{q.From, q.To}
	var metrics []string
	var dim, order string
	switch q.Report {
	case c.ReportTraffic:
		metrics, dim = []string{"visitors", "pageviews", "visits", "bounce_rate", "visit_duration"}, "time:day"
	case c.ReportPages:
		metrics, dim, order = []string{"pageviews", "visitors"}, "event:page", "pageviews"
	case c.ReportSources:
		metrics, dim, order = []string{"visits", "visitors"}, "visit:source", "visits"
	case c.ReportEvents:
		metrics, dim, order = []string{"events", "visitors"}, "event:goal", "events"
		rep.Notes = append(rep.Notes, "These are your Plausible goals; each completion counts as a conversion.")
	default:
		return rep, c.NotSupported("Plausible", q.Report)
	}
	total, err := p.query(ctx, cl, creds, map[string]any{"metrics": metrics, "date_range": dates})
	if err != nil {
		return rep, err
	}
	if len(total.Results) > 0 {
		rep.Totals = plausibleMetrics(metrics, total.Results[0].Metrics)
	}
	body := map[string]any{"metrics": metrics, "date_range": dates, "dimensions": []string{dim}}
	if order != "" {
		body["order_by"] = [][]string{{order, "desc"}}
		body["pagination"] = map[string]int{"limit": q.Limit, "offset": 0}
	}
	resp, err := p.query(ctx, cl, creds, body)
	if err != nil {
		return rep, err
	}
	if q.Report == c.ReportTraffic {
		got := map[string]c.Metrics{}
		for _, r := range resp.Results {
			if len(r.Dimensions) > 0 {
				got[expand(str(r.Dimensions[0]))] = plausibleMetrics(metrics, r.Metrics)
			}
		}
		rep.Series = fillDays(q, got)
		return rep, nil
	}
	rep.Rows = []c.AnalyticsRow{}
	for _, r := range resp.Results {
		if len(r.Dimensions) > 0 {
			rep.Rows = append(rep.Rows, c.AnalyticsRow{Key: str(r.Dimensions[0]), Metrics: plausibleMetrics(metrics, r.Metrics)})
		}
	}
	return rep, nil
}

// ---- Fathom ----

const fathomAPI = "https://api.usefathom.com/v1"

// Fathom reads Fathom Analytics aggregations.
type Fathom struct{}

func (Fathom) Definition() c.Definition {
	return def("fathom", "Fathom Analytics", "https://usefathom.com", "https://usefathom.com/api", "https://app.usefathom.com/api",
		"Visitors, pages, referrers and event conversions from Fathom.",
		"Reads every site on the account with an API key. Events count as conversions.",
		"Paid plans", []c.Field{{Name: "api_key", Label: "API key", Secret: true, Help: "Fathom > Settings > API > Create new. Read-only (\"Site read\") is enough."}})
}

func (Fathom) Reports() []string {
	return []string{c.ReportTraffic, c.ReportPages, c.ReportSources, c.ReportEvents}
}

func (f Fathom) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var acct struct {
		ID    any    `json:"id"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := newCall(cl, "Fathom").bearer(creds["api_key"]).get(ctx, cl.Base("fathom", fathomAPI)+"/account", &acct); err != nil {
		// Site-scoped keys cannot read the account; listing sites still
		// proves the key works.
		if c.KindOf(err) != c.ErrAuth {
			return c.Account{}, err
		}
		sites, err2 := f.Sites(ctx, cl, creds)
		if err2 != nil {
			return c.Account{}, err2
		}
		return c.Account{Name: "Fathom", Details: map[string]string{"sites": itoa(len(sites))}}, nil
	}
	return c.Account{ID: str(acct.ID), Name: acct.Name, Email: acct.Email}, nil
}

func (Fathom) Sites(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AnalyticsSite, error) {
	var resp struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := newCall(cl, "Fathom").bearer(creds["api_key"]).get(ctx, cl.Base("fathom", fathomAPI)+"/sites?limit=100", &resp); err != nil {
		return nil, err
	}
	out := []c.AnalyticsSite{}
	for _, s := range resp.Data {
		out = append(out, c.AnalyticsSite{ID: s.ID, Name: s.Name})
	}
	return out, nil
}

func (Fathom) aggregate(ctx context.Context, cl *c.Client, creds c.Creds, q url.Values) ([]map[string]any, error) {
	var out []map[string]any
	err := newCall(cl, "Fathom").bearer(creds["api_key"]).get(ctx, cl.Base("fathom", fathomAPI)+"/aggregations?"+q.Encode(), &out)
	return out, err
}

func fathomMetrics(r map[string]any) c.Metrics {
	return c.Metrics{
		Visitors: num(r["uniques"]), Sessions: num(r["visits"]), Pageviews: num(r["pageviews"]),
		AvgDuration: num(r["avg_duration"]), BounceRate: num(r["bounce_rate"]),
		Conversions: num(r["conversions"]),
	}
}

func (f Fathom) Report(ctx context.Context, cl *c.Client, creds c.Creds, q c.AnalyticsQuery) (c.AnalyticsReport, error) {
	if err := checkSite("Fathom", q.Site, safeID); err != nil {
		return c.AnalyticsReport{}, err
	}
	rep := c.AnalyticsReport{Report: q.Report, Site: q.Site, From: q.From, To: q.To}
	base := url.Values{
		"entity": {"pageview"}, "entity_id": {q.Site},
		"aggregates": {"visits,uniques,pageviews,avg_duration,bounce_rate"},
		"date_from":  {q.From + " 00:00:00"}, "date_to": {q.To + " 23:59:59"},
	}
	with := func(kv ...string) url.Values {
		v := url.Values{}
		for k, x := range base {
			v[k] = x
		}
		for i := 0; i+1 < len(kv); i += 2 {
			v.Set(kv[i], kv[i+1])
		}
		return v
	}
	rowsBy := func(field string, res []map[string]any) []c.AnalyticsRow {
		out := []c.AnalyticsRow{}
		for _, r := range res {
			key := str(r[field])
			if key == "" {
				key = "(none)"
			}
			out = append(out, c.AnalyticsRow{Key: key, Metrics: fathomMetrics(r)})
		}
		return out
	}
	switch q.Report {
	case c.ReportTraffic:
		tot, err := f.aggregate(ctx, cl, creds, with())
		if err != nil {
			return rep, err
		}
		if len(tot) > 0 {
			rep.Totals = fathomMetrics(tot[0])
		}
		res, err := f.aggregate(ctx, cl, creds, with("date_grouping", "day"))
		if err != nil {
			return rep, err
		}
		got := map[string]c.Metrics{}
		for _, r := range res {
			got[expand(str(r["date"]))] = fathomMetrics(r)
		}
		rep.Series = fillDays(q, got)
	case c.ReportPages:
		res, err := f.aggregate(ctx, cl, creds, with("field_grouping", "pathname", "sort_by", "pageviews:desc", "limit", itoa(q.Limit)))
		if err != nil {
			return rep, err
		}
		rep.Rows = rowsBy("pathname", res)
	case c.ReportSources:
		res, err := f.aggregate(ctx, cl, creds, with("field_grouping", "referrer_hostname", "sort_by", "visits:desc", "limit", itoa(q.Limit)))
		if err != nil {
			return rep, err
		}
		rep.Rows = rowsBy("referrer_hostname", res)
		rep.Notes = append(rep.Notes, "(none) means no referrer: typed in, bookmarked or from an app.")
	case c.ReportEvents:
		var events struct {
			Data []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"data"`
		}
		if err := newCall(cl, "Fathom").bearer(creds["api_key"]).get(ctx, cl.Base("fathom", fathomAPI)+"/sites/"+url.PathEscape(q.Site)+"/events?limit=100", &events); err != nil {
			return rep, err
		}
		// One call per event; the busiest 25 are plenty for a report.
		rows := []c.AnalyticsRow{}
		for i, e := range events.Data {
			if i >= 25 {
				rep.Notes = append(rep.Notes, "Only the first 25 events are counted.")
				break
			}
			res, err := f.aggregate(ctx, cl, creds, url.Values{
				"entity": {"event"}, "entity_id": {q.Site}, "entity_name": {e.Name},
				"aggregates": {"conversions,unique_conversions"},
				"date_from":  {q.From + " 00:00:00"}, "date_to": {q.To + " 23:59:59"},
			})
			if err != nil {
				return rep, err
			}
			m := c.Metrics{Conversions: c.Num(0), Events: c.Num(0)}
			if len(res) > 0 {
				m.Conversions, m.Events, m.Visitors = num(res[0]["conversions"]), num(res[0]["conversions"]), num(res[0]["unique_conversions"])
			}
			rows = append(rows, c.AnalyticsRow{Key: e.Name, Metrics: m})
		}
		rep.Totals = sumRows(rows)
		rep.Totals.Visitors = nil
		rep.Rows = sortRows(rows, q.Limit, byEvents)
	default:
		return rep, c.NotSupported("Fathom", q.Report)
	}
	return rep, nil
}

// ---- Simple Analytics ----

const simpleAPI = "https://simpleanalytics.com"

// SimpleAnalytics reads the Simple Analytics Stats API.
type SimpleAnalytics struct{}

func (SimpleAnalytics) Definition() c.Definition {
	return def("simple_analytics", "Simple Analytics", "https://www.simpleanalytics.com", "https://docs.simpleanalytics.com/api/stats", "https://dashboard.simpleanalytics.com/account",
		"Visitors, pages and referrers from Simple Analytics.",
		"Reads one website. Public dashboards need only the domain; private ones need an API key and your user id.",
		"Paid plans", []c.Field{
			{Name: "hostname", Label: "Website", Placeholder: "example.com", Help: "The domain as it appears in Simple Analytics."},
			{Name: "api_key", Label: "API key", Secret: true, Optional: true, Placeholder: "sa_api_key_…", Help: "Account settings > API keys. Needed for private dashboards."},
			{Name: "user_id", Label: "User id", Optional: true, Placeholder: "sa_user_id_…", Help: "Shown next to your API keys."},
		})
}

func (SimpleAnalytics) Reports() []string {
	return []string{c.ReportTraffic, c.ReportPages, c.ReportSources}
}

type simpleRow struct {
	Value     string   `json:"value"`
	Date      string   `json:"date"`
	Pageviews *float64 `json:"pageviews"`
	Visitors  *float64 `json:"visitors"`
}

type simpleResp struct {
	Pageviews *float64    `json:"pageviews"`
	Visitors  *float64    `json:"visitors"`
	Histogram []simpleRow `json:"histogram"`
	Pages     []simpleRow `json:"pages"`
	Referrers []simpleRow `json:"referrers"`
}

func (SimpleAnalytics) stats(ctx context.Context, cl *c.Client, creds c.Creds, q url.Values) (simpleResp, error) {
	var resp simpleResp
	host := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(creds["hostname"]), "https://"), "http://")
	host = strings.TrimRight(host, "/")
	if !hostname.MatchString(host) {
		return resp, c.Errf(c.ErrInvalid, "the website should be a domain like example.com")
	}
	k := newCall(cl, "Simple Analytics")
	if creds["api_key"] != "" {
		k.set("Api-Key", creds["api_key"])
	}
	if creds["user_id"] != "" {
		k.set("User-Id", creds["user_id"])
	}
	q.Set("version", "6")
	q.Set("timezone", "UTC")
	err := k.get(ctx, cl.Base("simple_analytics", simpleAPI)+"/"+host+".json?"+q.Encode(), &resp)
	return resp, err
}

func (s SimpleAnalytics) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	today := time.Now().UTC().Format(time.DateOnly)
	if _, err := s.stats(ctx, cl, creds, url.Values{"fields": {"pageviews"}, "start": {today}, "end": {today}}); err != nil {
		return c.Account{}, err
	}
	return c.Account{ID: creds["hostname"], Name: creds["hostname"]}, nil
}

func (SimpleAnalytics) Sites(_ context.Context, _ *c.Client, creds c.Creds) ([]c.AnalyticsSite, error) {
	return single(creds["hostname"], creds["hostname"]), nil
}

func (s SimpleAnalytics) Report(ctx context.Context, cl *c.Client, creds c.Creds, q c.AnalyticsQuery) (c.AnalyticsReport, error) {
	rep := c.AnalyticsReport{Report: q.Report, Site: creds["hostname"], From: q.From, To: q.To}
	fields := map[string]string{c.ReportTraffic: "pageviews,visitors,histogram", c.ReportPages: "pageviews,visitors,pages", c.ReportSources: "pageviews,visitors,referrers"}[q.Report]
	if fields == "" {
		return rep, c.NotSupported("Simple Analytics", q.Report)
	}
	resp, err := s.stats(ctx, cl, creds, url.Values{"fields": {fields}, "start": {q.From}, "end": {q.To}, "limit": {itoa(q.Limit)}})
	if err != nil {
		return rep, err
	}
	rep.Totals = c.Metrics{Pageviews: resp.Pageviews, Visitors: resp.Visitors}
	rows := func(rs []simpleRow) []c.AnalyticsRow {
		out := []c.AnalyticsRow{}
		for _, r := range rs {
			out = append(out, c.AnalyticsRow{Key: r.Value, Metrics: c.Metrics{Pageviews: r.Pageviews, Visitors: r.Visitors}})
		}
		return out
	}
	switch q.Report {
	case c.ReportTraffic:
		got := map[string]c.Metrics{}
		for _, r := range resp.Histogram {
			got[expand(r.Date)] = c.Metrics{Pageviews: r.Pageviews, Visitors: r.Visitors}
		}
		rep.Series = fillDays(q, got)
	case c.ReportPages:
		rep.Rows = sortRows(rows(resp.Pages), q.Limit, byPageviews)
	case c.ReportSources:
		rep.Rows = sortRows(rows(resp.Referrers), q.Limit, byPageviews)
	}
	return rep, nil
}
