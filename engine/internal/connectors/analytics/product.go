package analytics

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Product analytics: Mixpanel and Amplitude. They count events rather
// than visits, so traffic is read from their page view events.

// ---- Mixpanel ----

// Mixpanel reads the Query API with a service account.
type Mixpanel struct{}

func (Mixpanel) Definition() c.Definition {
	return def("mixpanel", "Mixpanel", "https://mixpanel.com", "https://developer.mixpanel.com/reference/query-api", "https://mixpanel.com/settings/org#serviceaccounts",
		"Page views, pages, referrers and event counts from Mixpanel.",
		"Uses a service account. Traffic comes from Mixpanel's page view event ($mp_web_page_view unless you name another). Mixpanel allows about 60 report queries an hour, so Outflare keeps each answer for an hour.",
		"Free tier", []c.Field{
			{Name: "username", Label: "Service account username", Help: "Organization settings > Service accounts > Add. The Consumer role is enough."},
			{Name: "secret", Label: "Service account secret", Secret: true},
			{Name: "project_id", Label: "Project id", Placeholder: "1234567", Help: "Project settings > Overview > Project ID."},
			{Name: "region", Label: "Data residency", Optional: true, Placeholder: "US", Help: "US (default), EU or IN."},
			{Name: "page_view_event", Label: "Page view event", Optional: true, Placeholder: "$mp_web_page_view"},
		})
}

func (Mixpanel) Reports() []string {
	return []string{c.ReportTraffic, c.ReportPages, c.ReportSources, c.ReportEvents}
}

func mixpanelBase(cl *c.Client, creds c.Creds) string {
	host := "https://mixpanel.com"
	switch strings.ToUpper(strings.TrimSpace(creds["region"])) {
	case "EU":
		host = "https://eu.mixpanel.com"
	case "IN":
		host = "https://in.mixpanel.com"
	}
	return cl.Base("mixpanel", host)
}

func (Mixpanel) get(ctx context.Context, cl *c.Client, creds c.Creds, path string, q url.Values, out any) error {
	q.Set("project_id", creds["project_id"])
	return newCall(cl, "Mixpanel").basic(creds["username"], creds["secret"]).get(ctx, mixpanelBase(cl, creds)+path+"?"+q.Encode(), out)
}

func (m Mixpanel) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	if !digits.MatchString(creds["project_id"]) {
		return c.Account{}, c.Errf(c.ErrInvalid, "the project id is a number, from Project settings > Overview")
	}
	var names []string
	if err := m.get(ctx, cl, creds, "/api/query/events/names", url.Values{"type": {"general"}, "limit": {"5"}}, &names); err != nil {
		return c.Account{}, err
	}
	return c.Account{ID: creds["project_id"], Name: "Mixpanel project " + creds["project_id"]}, nil
}

func (Mixpanel) Sites(_ context.Context, _ *c.Client, creds c.Creds) ([]c.AnalyticsSite, error) {
	return single(creds["project_id"], "Mixpanel project "+creds["project_id"]), nil
}

// segmentation returns counts by key (a date, or a property value with on
// set) summed over the range, and per day when byDay.
type mixpanelSeg struct {
	Data struct {
		Series []string                      `json:"series"`
		Values map[string]map[string]float64 `json:"values"`
	} `json:"data"`
}

func (m Mixpanel) segment(ctx context.Context, cl *c.Client, creds c.Creds, q c.AnalyticsQuery, event, kind, on string) (mixpanelSeg, error) {
	p := url.Values{"event": {event}, "from_date": {q.From}, "to_date": {q.To}, "unit": {"day"}, "type": {kind}}
	if on != "" {
		p.Set("on", on)
		p.Set("limit", itoa(q.Limit))
	}
	var seg mixpanelSeg
	err := m.get(ctx, cl, creds, "/api/query/segmentation", p, &seg)
	return seg, err
}

func (m Mixpanel) Report(ctx context.Context, cl *c.Client, creds c.Creds, q c.AnalyticsQuery) (c.AnalyticsReport, error) {
	rep := c.AnalyticsReport{Report: q.Report, Site: creds["project_id"], From: q.From, To: q.To}
	pv := strings.TrimSpace(creds["page_view_event"])
	if pv == "" {
		pv = "$mp_web_page_view"
	}
	byKey := func(seg mixpanelSeg, metric func(float64) c.Metrics) []c.AnalyticsRow {
		rows := []c.AnalyticsRow{}
		for k, days := range seg.Data.Values {
			var n float64
			for _, v := range days {
				n += v
			}
			if k == "undefined" {
				k = "(none)"
			}
			rows = append(rows, c.AnalyticsRow{Key: k, Metrics: metric(n)})
		}
		return rows
	}
	switch q.Report {
	case c.ReportTraffic:
		views, err := m.segment(ctx, cl, creds, q, pv, "general", "")
		if err != nil {
			return rep, err
		}
		people, err := m.segment(ctx, cl, creds, q, pv, "unique", "")
		if err != nil {
			return rep, err
		}
		got := map[string]c.Metrics{}
		for d, n := range views.Data.Values[pv] {
			mm := got[d]
			mm.Pageviews = c.Num(n)
			got[d] = mm
		}
		for d, n := range people.Data.Values[pv] {
			mm := got[d]
			mm.Visitors = c.Num(n)
			got[d] = mm
		}
		rep.Series = fillDays(q, got)
		rep.Totals = c.Metrics{Pageviews: sumPoints(rep.Series).Pageviews}
		rep.Notes = append(rep.Notes, "Mixpanel gives unique visitors per day only, so there is no visitors total for the range.")
	case c.ReportPages:
		seg, err := m.segment(ctx, cl, creds, q, pv, "general", `properties["$current_url"]`)
		if err != nil {
			return rep, err
		}
		rep.Rows = sortRows(byKey(seg, func(n float64) c.Metrics { return c.Metrics{Pageviews: c.Num(n)} }), q.Limit, byPageviews)
	case c.ReportSources:
		seg, err := m.segment(ctx, cl, creds, q, pv, "general", `properties["$referring_domain"]`)
		if err != nil {
			return rep, err
		}
		rep.Rows = sortRows(byKey(seg, func(n float64) c.Metrics { return c.Metrics{Pageviews: c.Num(n)} }), q.Limit, byPageviews)
		rep.Notes = append(rep.Notes, "Counted in page views from each referring domain; $direct means no referrer.")
	case c.ReportEvents:
		var names []string
		if err := m.get(ctx, cl, creds, "/api/query/events/names", url.Values{"type": {"general"}, "limit": {itoa(q.Limit)}}, &names); err != nil {
			return rep, err
		}
		if len(names) == 0 {
			rep.Rows = []c.AnalyticsRow{}
			return rep, nil
		}
		ev, _ := json.Marshal(names)
		var seg mixpanelSeg
		if err := m.get(ctx, cl, creds, "/api/query/events", url.Values{"event": {string(ev)}, "type": {"general"}, "unit": {"day"}, "from_date": {q.From}, "to_date": {q.To}}, &seg); err != nil {
			return rep, err
		}
		rep.Rows = sortRows(byKey(seg, func(n float64) c.Metrics { return c.Metrics{Events: c.Num(n)} }), q.Limit, byEvents)
		rep.Notes = append(rep.Notes, "The events are the ones Mixpanel saw most in the last 31 days, counted over your range.")
	default:
		return rep, c.NotSupported("Mixpanel", q.Report)
	}
	return rep, nil
}

// ---- Amplitude ----

// Amplitude reads the Dashboard REST API with a project's API and secret
// keys.
type Amplitude struct{}

const amplitudePageView = "[Amplitude] Page Viewed"

func (Amplitude) Definition() c.Definition {
	return def("amplitude", "Amplitude", "https://amplitude.com", "https://amplitude.com/docs/apis/analytics/dashboard-rest", "https://app.amplitude.com/analytics/settings/projects",
		"Active users, page views, top pages and event counts from Amplitude.",
		"Reads one project with its API key and secret key. Traffic uses active users and the [Amplitude] Page Viewed event from the browser SDK.",
		"Free tier", []c.Field{
			{Name: "api_key", Label: "API key", Help: "Organization settings > Projects > your project > General."},
			{Name: "secret_key", Label: "Secret key", Secret: true},
			{Name: "region", Label: "Data region", Optional: true, Placeholder: "US", Help: "US (default) or EU."},
		})
}

func (Amplitude) Reports() []string {
	return []string{c.ReportTraffic, c.ReportPages, c.ReportEvents}
}

func (Amplitude) get(ctx context.Context, cl *c.Client, creds c.Creds, path string, q url.Values, out any) error {
	host := "https://amplitude.com"
	if strings.EqualFold(strings.TrimSpace(creds["region"]), "EU") {
		host = "https://analytics.eu.amplitude.com"
	}
	u := cl.Base("amplitude", host) + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return newCall(cl, "Amplitude").basic(creds["api_key"], creds["secret_key"]).get(ctx, u, out)
}

type amplitudeEvent struct {
	Name   string  `json:"name"`
	Totals float64 `json:"totals"`
	Hidden bool    `json:"hidden"`
}

func (a Amplitude) events(ctx context.Context, cl *c.Client, creds c.Creds) ([]amplitudeEvent, error) {
	var resp struct {
		Data []amplitudeEvent `json:"data"`
	}
	err := a.get(ctx, cl, creds, "/api/2/events/list", nil, &resp)
	return resp.Data, err
}

func (a Amplitude) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	ev, err := a.events(ctx, cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	return c.Account{Name: "Amplitude project", Details: map[string]string{"event_types": itoa(len(ev))}}, nil
}

func (Amplitude) Sites(context.Context, *c.Client, c.Creds) ([]c.AnalyticsSite, error) {
	return single("project", "Amplitude project"), nil
}

type amplitudeSeg struct {
	Data struct {
		Series          [][]float64 `json:"series"`
		XValues         []string    `json:"xValues"`
		SeriesLabels    []any       `json:"seriesLabels"`
		SeriesCollapsed [][]struct {
			Value float64 `json:"value"`
		} `json:"seriesCollapsed"`
	} `json:"data"`
}

// label reads a series label, which Amplitude sends as a string or as
// [index, "value"].
func (s amplitudeSeg) label(i int) string {
	if i >= len(s.Data.SeriesLabels) {
		return ""
	}
	switch l := s.Data.SeriesLabels[i].(type) {
	case []any:
		if len(l) > 0 {
			return str(l[len(l)-1])
		}
	default:
		return str(l)
	}
	return ""
}

func (s amplitudeSeg) total(i int) *float64 {
	if i < len(s.Data.SeriesCollapsed) && len(s.Data.SeriesCollapsed[i]) > 0 {
		return c.Num(s.Data.SeriesCollapsed[i][0].Value)
	}
	return nil
}

func (a Amplitude) segment(ctx context.Context, cl *c.Client, creds c.Creds, q c.AnalyticsQuery, event map[string]any, metric string, limit int) (amplitudeSeg, error) {
	e, _ := json.Marshal(event)
	p := url.Values{"e": {string(e)}, "m": {metric}, "start": {compact(q.From)}, "end": {compact(q.To)}, "i": {"1"}}
	if limit > 0 {
		p.Set("limit", itoa(limit))
	}
	var seg amplitudeSeg
	err := a.get(ctx, cl, creds, "/api/2/events/segmentation", p, &seg)
	return seg, err
}

func (a Amplitude) Report(ctx context.Context, cl *c.Client, creds c.Creds, q c.AnalyticsQuery) (c.AnalyticsReport, error) {
	rep := c.AnalyticsReport{Report: q.Report, Site: "project", From: q.From, To: q.To}
	switch q.Report {
	case c.ReportTraffic:
		users, err := a.segment(ctx, cl, creds, q, map[string]any{"event_type": "_active"}, "uniques", 0)
		if err != nil {
			return rep, err
		}
		views, err := a.segment(ctx, cl, creds, q, map[string]any{"event_type": amplitudePageView}, "totals", 0)
		if err != nil {
			return rep, err
		}
		got := map[string]c.Metrics{}
		put := func(seg amplitudeSeg, set func(*c.Metrics, float64)) {
			if len(seg.Data.Series) == 0 {
				return
			}
			for i, d := range seg.Data.XValues {
				if i < len(seg.Data.Series[0]) {
					m := got[expand(d)]
					set(&m, seg.Data.Series[0][i])
					got[expand(d)] = m
				}
			}
		}
		put(users, func(m *c.Metrics, v float64) { m.Visitors = c.Num(v) })
		put(views, func(m *c.Metrics, v float64) { m.Pageviews = c.Num(v) })
		rep.Series = fillDays(q, got)
		rep.Totals = c.Metrics{Visitors: users.total(0), Pageviews: views.total(0)}
		rep.Notes = append(rep.Notes, "Visitors are Amplitude active users; page views are the [Amplitude] Page Viewed event.")
	case c.ReportPages:
		seg, err := a.segment(ctx, cl, creds, q, map[string]any{"event_type": amplitudePageView,
			"group_by": []map[string]string{{"type": "event", "value": "[Amplitude] Page Path"}}}, "totals", q.Limit)
		if err != nil {
			return rep, err
		}
		rows := []c.AnalyticsRow{}
		for i := range seg.Data.SeriesLabels {
			rows = append(rows, c.AnalyticsRow{Key: seg.label(i), Metrics: c.Metrics{Pageviews: seg.total(i)}})
		}
		rep.Rows = sortRows(rows, q.Limit, byPageviews)
	case c.ReportEvents:
		ev, err := a.events(ctx, cl, creds)
		if err != nil {
			return rep, err
		}
		// Count the busiest custom events (by this week's totals) over the
		// range, one call each.
		sort.Slice(ev, func(i, j int) bool { return ev[i].Totals > ev[j].Totals })
		rows := []c.AnalyticsRow{}
		for _, e := range ev {
			if len(rows) >= min(q.Limit, 10) {
				break
			}
			if e.Hidden || strings.HasPrefix(e.Name, "[Amplitude]") || strings.HasPrefix(e.Name, "_") {
				continue
			}
			seg, err := a.segment(ctx, cl, creds, q, map[string]any{"event_type": e.Name}, "totals", 0)
			if err != nil {
				return rep, err
			}
			rows = append(rows, c.AnalyticsRow{Key: e.Name, Metrics: c.Metrics{Events: seg.total(0)}})
		}
		rep.Rows = sortRows(rows, q.Limit, byEvents)
		rep.Notes = append(rep.Notes, "Shows up to 10 of your busiest custom events; Amplitude's own events are left out.")
	default:
		return rep, c.NotSupported("Amplitude", q.Report)
	}
	return rep, nil
}

// CacheFor keeps answers an hour: Mixpanel allows about 60 queries an hour.
func (Mixpanel) CacheFor() time.Duration { return time.Hour }

// CacheFor keeps answers an hour: Amplitude limits concurrent and hourly
// queries, and the events report costs a call per event.
func (Amplitude) CacheFor() time.Duration { return time.Hour }
