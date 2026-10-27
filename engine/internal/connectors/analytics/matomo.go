package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Matomo reads a Matomo (cloud or self-hosted) through its Reporting API.
type Matomo struct{}

func (Matomo) Definition() c.Definition {
	return def("matomo", "Matomo", "https://matomo.org", "https://developer.matomo.org/api-reference/reporting-api", "",
		"Visitors, pages, referrers, events and goal conversions from Matomo, cloud or self-hosted.",
		"Reads every site the token can view. The token is sent in the request body, never the address.",
		"Free (self-hosted)", []c.Field{
			{Name: "url", Label: "Matomo address", Placeholder: "https://analytics.example.com", Help: "The address you open Matomo at. Matomo Cloud: https://yourname.matomo.cloud."},
			{Name: "token", Label: "Auth token", Secret: true, Help: "Matomo > Administration > Personal > Security > Auth tokens. A user with view access is enough."},
		})
}

func (Matomo) Reports() []string {
	return []string{c.ReportTraffic, c.ReportPages, c.ReportSources, c.ReportEvents}
}

// call runs one Reporting API method. Matomo answers errors with HTTP 200
// and {"result":"error"}.
func (Matomo) call(ctx context.Context, cl *c.Client, creds c.Creds, method string, params url.Values, out any) error {
	if creds["url"] == "" {
		return c.Errf(c.ErrInvalid, "the Matomo address is required")
	}
	base, err := hostURL(cl, "matomo", creds["url"], "")
	if err != nil {
		return err
	}
	q := url.Values{"module": {"API"}, "method": {method}, "format": {"JSON"}}
	for k, v := range params {
		q[k] = v
	}
	var raw json.RawMessage
	form := url.Values{"token_auth": {creds["token"]}}
	if err := newCall(cl, "Matomo").post(ctx, strings.TrimSuffix(base, "/index.php")+"/index.php?"+q.Encode(), form, &raw); err != nil {
		return err
	}
	var e struct {
		Result  string `json:"result"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Result == "error" {
		msg := strings.ToLower(e.Message)
		if strings.Contains(msg, "token") || strings.Contains(msg, "access") || strings.Contains(msg, "authenticat") {
			return c.Errf(c.ErrAuth, "Matomo refused the token: %s", e.Message)
		}
		return c.Errf(c.ErrInvalid, "Matomo: %s", e.Message)
	}
	if strings.TrimSpace(string(raw)) == "[]" {
		return nil // no data: Matomo sends an empty list even for one-object methods
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return c.Errf(c.ErrUnavailable, "Matomo returned an answer Outflare does not understand for %s", method)
	}
	return nil
}

func (m Matomo) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	sites, err := m.Sites(ctx, cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	name := creds["url"]
	if u, err := url.Parse(name); err == nil && u.Host != "" {
		name = u.Host
	}
	return c.Account{Name: name, Details: map[string]string{"sites": itoa(len(sites))}}, nil
}

func (m Matomo) Sites(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AnalyticsSite, error) {
	var sites []map[string]any
	if err := m.call(ctx, cl, creds, "SitesManager.getSitesWithAtLeastViewAccess", nil, &sites); err != nil {
		return nil, err
	}
	out := []c.AnalyticsSite{}
	for _, s := range sites {
		out = append(out, c.AnalyticsSite{ID: str(s["idsite"]), Name: str(s["name"]), URL: str(s["main_url"])})
	}
	return out, nil
}

func matomoMetrics(r map[string]any) c.Metrics {
	return c.Metrics{
		Visitors: first(num(r["nb_uniq_visitors"]), num(r["sum_daily_nb_uniq_visitors"])), Sessions: num(r["nb_visits"]),
		Pageviews: first(num(r["nb_pageviews"]), num(r["nb_hits"])), BounceRate: num(r["bounce_rate"]),
		AvgDuration: num(r["avg_time_on_site"]), Events: num(r["nb_events"]), Conversions: num(r["nb_conversions"]),
	}
}

func (m Matomo) Report(ctx context.Context, cl *c.Client, creds c.Creds, q c.AnalyticsQuery) (c.AnalyticsReport, error) {
	if err := checkSite("Matomo", q.Site, digits); err != nil {
		return c.AnalyticsReport{}, err
	}
	rep := c.AnalyticsReport{Report: q.Report, Site: q.Site, From: q.From, To: q.To}
	span := q.From + "," + q.To
	rng := url.Values{"idSite": {q.Site}, "period": {"range"}, "date": {span}}
	rows := func(method string, extra url.Values) ([]c.AnalyticsRow, error) {
		p := url.Values{"flat": {"1"}, "filter_limit": {fmt.Sprint(q.Limit)}}
		for k, v := range rng {
			p[k] = v
		}
		for k, v := range extra {
			p[k] = v
		}
		var res []map[string]any
		if err := m.call(ctx, cl, creds, method, p, &res); err != nil {
			return nil, err
		}
		out := []c.AnalyticsRow{}
		for _, r := range res {
			out = append(out, c.AnalyticsRow{Key: str(r["label"]), Metrics: matomoMetrics(r)})
		}
		return out, nil
	}
	var err error
	switch q.Report {
	case c.ReportTraffic:
		var visits, actions map[string]any
		if err := m.call(ctx, cl, creds, "VisitsSummary.get", rng, &visits); err != nil {
			return rep, err
		}
		if err := m.call(ctx, cl, creds, "Actions.get", rng, &actions); err != nil {
			return rep, err
		}
		rep.Totals = matomoMetrics(visits)
		rep.Totals.Pageviews = num(actions["nb_pageviews"])
		// Per day: Matomo keys each day's numbers by date, and sends an
		// empty list for a day with no visits.
		daily := url.Values{"idSite": {q.Site}, "period": {"day"}, "date": {span}}
		var vd, ad map[string]json.RawMessage
		if err := m.call(ctx, cl, creds, "VisitsSummary.get", daily, &vd); err != nil {
			return rep, err
		}
		if err := m.call(ctx, cl, creds, "Actions.get", daily, &ad); err != nil {
			return rep, err
		}
		got := map[string]c.Metrics{}
		for d, raw := range vd {
			var v, a map[string]any
			_ = json.Unmarshal(raw, &v)
			_ = json.Unmarshal(ad[d], &a)
			mm := matomoMetrics(v)
			mm.Pageviews = num(a["nb_pageviews"])
			if v == nil {
				mm = c.Metrics{Visitors: c.Num(0), Sessions: c.Num(0), Pageviews: c.Num(0)}
			}
			got[d] = mm
		}
		rep.Series = fillDays(q, got)
		return rep, nil
	case c.ReportPages:
		rep.Rows, err = rows("Actions.getPageUrls", url.Values{"filter_sort_column": {"nb_hits"}, "filter_sort_order": {"desc"}})
	case c.ReportSources:
		rep.Rows, err = rows("Referrers.getAll", url.Values{"filter_sort_column": {"nb_visits"}, "filter_sort_order": {"desc"}})
	case c.ReportEvents:
		rep.Rows, err = rows("Events.getName", url.Values{"filter_sort_column": {"nb_events"}, "filter_sort_order": {"desc"}})
		if err == nil {
			var goals map[string]any
			if gerr := m.call(ctx, cl, creds, "Goals.get", rng, &goals); gerr == nil {
				rep.Totals.Conversions = num(goals["nb_conversions"])
				rep.Notes = append(rep.Notes, "The conversions total is every goal conversion in the range.")
			}
		}
	default:
		return rep, c.NotSupported("Matomo", q.Report)
	}
	return rep, err
}

// ---- Umami ----

const umamiCloud = "https://api.umami.is/v1"

// Umami reads Umami Cloud with an API key, or a self-hosted Umami by
// signing in.
type Umami struct{}

func (Umami) Definition() c.Definition {
	return def("umami", "Umami", "https://umami.is", "https://umami.is/docs/api", "https://cloud.umami.is/settings/api-keys",
		"Visitors, pages, referrers and events from Umami, cloud or self-hosted.",
		"Umami Cloud: paste an API key. Self-hosted: give the address and a user that can view the websites; Outflare signs in for each report.",
		"Free (self-hosted)", []c.Field{
			{Name: "api_key", Label: "API key (Umami Cloud)", Secret: true, Optional: true, Help: "Umami Cloud > Settings > API keys."},
			{Name: "host", Label: "Address (self-hosted)", Optional: true, Placeholder: "https://umami.example.com"},
			{Name: "username", Label: "Username (self-hosted)", Optional: true},
			{Name: "password", Label: "Password (self-hosted)", Secret: true, Optional: true},
		})
}

func (Umami) Reports() []string {
	return []string{c.ReportTraffic, c.ReportPages, c.ReportSources, c.ReportEvents}
}

func (Umami) open(ctx context.Context, cl *c.Client, creds c.Creds) (*call, string, error) {
	if creds["api_key"] != "" {
		return newCall(cl, "Umami").set("x-umami-api-key", creds["api_key"]), cl.Base("umami", umamiCloud), nil
	}
	if creds["host"] == "" || creds["username"] == "" || creds["password"] == "" {
		return nil, "", c.Errf(c.ErrInvalid, "give an Umami Cloud API key, or the address, username and password of your own Umami")
	}
	host, err := hostURL(cl, "umami", creds["host"], "")
	if err != nil {
		return nil, "", err
	}
	var login struct {
		Token string `json:"token"`
	}
	if err := newCall(cl, "Umami").post(ctx, host+"/api/auth/login", map[string]string{"username": creds["username"], "password": creds["password"]}, &login); err != nil {
		if c.KindOf(err) == c.ErrInvalid {
			return nil, "", c.Errf(c.ErrAuth, "Umami did not accept that username and password")
		}
		return nil, "", err
	}
	return newCall(cl, "Umami").bearer(login.Token), host + "/api", nil
}

func (u Umami) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	sites, err := u.Sites(ctx, cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	name := "Umami Cloud"
	if creds["api_key"] == "" {
		name = creds["username"] + " on " + creds["host"]
	}
	return c.Account{Name: name, Details: map[string]string{"websites": itoa(len(sites))}}, nil
}

func (u Umami) Sites(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AnalyticsSite, error) {
	k, base, err := u.open(ctx, cl, creds)
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if err := k.get(ctx, base+"/websites?pageSize=200", &raw); err != nil {
		return nil, err
	}
	type site struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Domain string `json:"domain"`
	}
	var list []site
	var paged struct {
		Data []site `json:"data"`
	}
	if json.Unmarshal(raw, &paged) == nil && paged.Data != nil {
		list = paged.Data
	} else {
		_ = json.Unmarshal(raw, &list)
	}
	out := []c.AnalyticsSite{}
	for _, s := range list {
		out = append(out, c.AnalyticsSite{ID: s.ID, Name: s.Name, URL: s.Domain})
	}
	return out, nil
}

// umamiValue reads a stat that older versions send as {"value": n} and
// newer ones as a plain number.
func umamiValue(raw json.RawMessage) *float64 {
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		return c.Num(n)
	}
	var v struct {
		Value *float64 `json:"value"`
	}
	if json.Unmarshal(raw, &v) == nil {
		return v.Value
	}
	return nil
}

func (u Umami) Report(ctx context.Context, cl *c.Client, creds c.Creds, q c.AnalyticsQuery) (c.AnalyticsReport, error) {
	if err := checkSite("Umami", q.Site, safeID); err != nil {
		return c.AnalyticsReport{}, err
	}
	k, base, err := u.open(ctx, cl, creds)
	if err != nil {
		return c.AnalyticsReport{}, err
	}
	rep := c.AnalyticsReport{Report: q.Report, Site: q.Site, From: q.From, To: q.To}
	site := base + "/websites/" + url.PathEscape(q.Site)
	span := url.Values{"startAt": {fmt.Sprint(millis(q.From))}, "endAt": {fmt.Sprint(millis(dayAfter(q.To)) - 1)}}
	enc := func(extra ...string) string {
		v := url.Values{}
		for x, y := range span {
			v[x] = y
		}
		for i := 0; i+1 < len(extra); i += 2 {
			v.Set(extra[i], extra[i+1])
		}
		return v.Encode()
	}
	metrics := func(kind string, metric func(float64) c.Metrics) error {
		type row struct {
			X string  `json:"x"`
			Y float64 `json:"y"`
		}
		var res []row
		err := k.get(ctx, site+"/metrics?"+enc("type", kind, "limit", fmt.Sprint(q.Limit)), &res)
		if err != nil && kind == "url" && c.KindOf(err) == c.ErrInvalid {
			// Umami 3 renamed the url breakdown to path.
			err = k.get(ctx, site+"/metrics?"+enc("type", "path", "limit", fmt.Sprint(q.Limit)), &res)
		}
		if err != nil {
			return err
		}
		rep.Rows = []c.AnalyticsRow{}
		for _, r := range res {
			key := r.X
			if key == "" {
				key = "(none)"
			}
			rep.Rows = append(rep.Rows, c.AnalyticsRow{Key: key, Metrics: metric(r.Y)})
		}
		return nil
	}
	switch q.Report {
	case c.ReportTraffic:
		var stats map[string]json.RawMessage
		if err := k.get(ctx, site+"/stats?"+enc(), &stats); err != nil {
			return rep, err
		}
		visits := umamiValue(stats["visits"])
		rep.Totals = c.Metrics{
			Pageviews: umamiValue(stats["pageviews"]), Visitors: umamiValue(stats["visitors"]), Sessions: visits,
			BounceRate: ratio(umamiValue(stats["bounces"]), visits), AvgDuration: ratio(umamiValue(stats["totaltime"]), visits),
		}
		var series struct {
			Pageviews []struct {
				X string  `json:"x"`
				Y float64 `json:"y"`
			} `json:"pageviews"`
			Sessions []struct {
				X string  `json:"x"`
				Y float64 `json:"y"`
			} `json:"sessions"`
		}
		if err := k.get(ctx, site+"/pageviews?"+enc("unit", "day", "timezone", "UTC"), &series); err != nil {
			return rep, err
		}
		got := map[string]c.Metrics{}
		for _, p := range series.Pageviews {
			d := expand(p.X)
			m := got[d]
			m.Pageviews = c.Num(p.Y)
			got[d] = m
		}
		for _, p := range series.Sessions {
			d := expand(p.X)
			m := got[d]
			m.Visitors = c.Num(p.Y)
			got[d] = m
		}
		rep.Series = fillDays(q, got)
		return rep, nil
	case c.ReportPages:
		err = metrics("url", func(y float64) c.Metrics { return c.Metrics{Pageviews: c.Num(y)} })
	case c.ReportSources:
		err = metrics("referrer", func(y float64) c.Metrics { return c.Metrics{Visitors: c.Num(y)} })
	case c.ReportEvents:
		err = metrics("event", func(y float64) c.Metrics { return c.Metrics{Events: c.Num(y)} })
	default:
		return rep, c.NotSupported("Umami", q.Report)
	}
	return rep, err
}
