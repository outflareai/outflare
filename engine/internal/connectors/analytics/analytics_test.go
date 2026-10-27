package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// fake serves canned answers by "METHOD path" and records request bodies.
func fake(t *testing.T, routes map[string]any) (*c.Client, *[]string) {
	t.Helper()
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(b))
		bodies = append(bodies, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery+" "+string(b))
		if r.Header.Get("Authorization") == "Bearer bad" {
			w.WriteHeader(401)
			return
		}
		v, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if fn, ok := v.(func(*http.Request) any); ok {
			v = fn(r)
		}
		json.NewEncoder(w).Encode(v)
	}))
	t.Cleanup(srv.Close)
	ids := []string{"google_analytics", "google_analytics.admin", "google.userinfo", "google_search_console", "bing_webmaster", "posthog", "plausible",
		"fathom", "matomo", "umami", "mixpanel", "amplitude", "cloudflare_web_analytics", "simple_analytics", "microsoft_clarity", "vercel_analytics"}
	cl := &c.Client{BaseURLs: map[string]string{}}
	for _, id := range ids {
		cl.BaseURLs[id] = srv.URL
	}
	return cl, &bodies
}

var ctx = context.Background()

func query(report string) c.AnalyticsQuery {
	q, _ := c.CheckAnalyticsQuery(c.AnalyticsQuery{Report: report, From: "2026-10-01", To: "2026-10-03", Limit: 10}, time.Now())
	return q
}

func val(p *float64) float64 {
	if p == nil {
		return -1
	}
	return *p
}

func TestEveryConnectorIsAnAnalyticsReader(t *testing.T) {
	seen := map[string]bool{}
	for _, x := range All() {
		r, ok := x.(c.AnalyticsReader)
		d := x.Definition()
		if !ok || d.Category != c.CategoryAnalytics || !d.Has(c.CapAnalytics) || len(r.Reports()) == 0 || seen[d.ID] {
			t.Fatalf("%s: reader %v, def %+v", d.ID, ok, d)
		}
		seen[d.ID] = true
		// A sign-in that needs its own registered app must keep a pasted
		// key too, so servers without that app can still connect.
		if d.Supports(c.AuthOAuth2) && !c.IsGoogle(d) && !d.OAuth.ClientMetadata && !d.Supports(c.AuthAPIKey) {
			t.Fatalf("%s: a sign-in needs the server's Google app, a hosted client document, or an API key beside it", d.ID)
		}
		if d.Supports(c.AuthAPIKey) && len(d.Fields) == 0 {
			t.Fatalf("%s: an API key method needs fields", d.ID)
		}
	}
}

func TestGA4(t *testing.T) {
	cl, bodies := fake(t, map[string]any{
		"GET /v1/userinfo": map[string]string{"sub": "1", "email": "jack@example.com"},
		"GET /v1beta/accountSummaries": map[string]any{"accountSummaries": []any{map[string]any{"displayName": "Acme",
			"propertySummaries": []any{map[string]string{"property": "properties/123", "displayName": "acme-com.example"}}}}},
		"POST /v1beta/properties/123:runReport": map[string]any{
			"metricHeaders": []any{map[string]string{"name": "totalUsers"}, map[string]string{"name": "screenPageViews"}, map[string]string{"name": "bounceRate"}},
			"rows":          []any{map[string]any{"dimensionValues": []any{map[string]string{"value": "20261002"}}, "metricValues": []any{map[string]string{"value": "5"}, map[string]string{"value": "9"}, map[string]string{"value": "0.4"}}}},
			"totals":        []any{map[string]any{"metricValues": []any{map[string]string{"value": "12"}, map[string]string{"value": "30"}, map[string]string{"value": "0.5"}}}},
		},
	})
	creds := c.Creds{"access_token": "tok"}
	acct, err := GA4{}.Test(ctx, cl, creds)
	if err != nil || acct.Email != "jack@example.com" || acct.Details["properties"] != "1" {
		t.Fatalf("test: %+v %v", acct, err)
	}
	sites, _ := GA4{}.Sites(ctx, cl, creds)
	if len(sites) != 1 || sites[0].ID != "123" || sites[0].Name != "acme-com.example (Acme)" {
		t.Fatalf("sites: %+v", sites)
	}
	q := query(c.ReportTraffic)
	q.Site = "123"
	rep, err := GA4{}.Report(ctx, cl, creds, q)
	if err != nil || val(rep.Totals.Visitors) != 12 || val(rep.Totals.BounceRate) != 0.5 || len(rep.Series) != 3 || val(rep.Series[1].Pageviews) != 9 || val(rep.Series[0].Pageviews) != 0 {
		t.Fatalf("traffic: %+v %v", rep, err)
	}
	if !strings.Contains((*bodies)[len(*bodies)-1], `"startDate":"2026-10-01"`) {
		t.Fatalf("body: %s", (*bodies)[len(*bodies)-1])
	}
	q.Site = "123/../x"
	if _, err := (GA4{}).Report(ctx, cl, creds, q); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("bad property id: %v", err)
	}
	if _, err := (GA4{}).Test(ctx, cl, c.Creds{"access_token": "bad"}); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("bad token: %v", err)
	}
}

func TestSearchConsole(t *testing.T) {
	cl, _ := fake(t, map[string]any{
		"GET /webmasters/v3/sites": map[string]any{"siteEntry": []any{
			map[string]string{"siteUrl": "sc-domain:acme-com.example", "permissionLevel": "siteOwner"},
			map[string]string{"siteUrl": "https://other-com.example/", "permissionLevel": "siteUnverifiedUser"},
		}},
		"POST /webmasters/v3/sites/sc-domain:acme-com.example/searchAnalytics/query": func(r *http.Request) any {
			var b struct {
				Dimensions []string `json:"dimensions"`
			}
			json.NewDecoder(r.Body).Decode(&b)
			if len(b.Dimensions) == 0 {
				return map[string]any{"rows": []any{map[string]any{"clicks": 10, "impressions": 200, "ctr": 0.05, "position": 7.5}}}
			}
			return map[string]any{"rows": []any{map[string]any{"keys": []string{"outflare"}, "clicks": 8, "impressions": 50, "ctr": 0.16, "position": 2.1}}}
		},
	})
	creds := c.Creds{"access_token": "tok"}
	sites, _ := SearchConsole{}.Sites(ctx, cl, creds)
	if len(sites) != 1 || sites[0].Name != "acme-com.example (whole domain)" {
		t.Fatalf("sites: %+v", sites)
	}
	q := query(c.ReportSearchQueries)
	q.Site = "sc-domain:acme-com.example"
	rep, err := SearchConsole{}.Report(ctx, cl, creds, q)
	if err != nil || val(rep.Totals.Clicks) != 10 || len(rep.Rows) != 1 || rep.Rows[0].Key != "outflare" || val(rep.Rows[0].Position) != 2.1 {
		t.Fatalf("queries: %+v %v", rep, err)
	}
}

func TestBingSumsWeeksInRange(t *testing.T) {
	cl, _ := fake(t, map[string]any{
		"GET /GetQueryStats": map[string]any{"d": []any{
			map[string]any{"Query": "cold email", "Date": "/Date(1759363200000-0700)/", "Clicks": 3, "Impressions": 30, "AvgImpressionPosition": 4}, // 2025-10-02
			map[string]any{"Query": "cold email", "Date": "/Date(1790899200000)/", "Clicks": 2, "Impressions": 10, "AvgImpressionPosition": 8},      // 2026-10-02
			map[string]any{"Query": "cold email", "Date": "/Date(1790985600000)/", "Clicks": 1, "Impressions": 10, "AvgImpressionPosition": 4},      // 2026-10-03
			map[string]any{"Query": "outflare", "Date": "/Date(1790899200000)/", "Clicks": 5, "Impressions": 20, "AvgImpressionPosition": 1},
		}},
	})
	q := query(c.ReportSearchQueries)
	q.Site = "https://acme-com.example/"
	rep, err := Bing{}.Report(ctx, cl, c.Creds{"api_key": "k"}, q)
	if err != nil || len(rep.Rows) != 2 || rep.Rows[0].Key != "outflare" || val(rep.Rows[1].Clicks) != 3 || val(rep.Rows[1].Position) != 6 || val(rep.Totals.Clicks) != 8 {
		t.Fatalf("bing: %+v %v", rep, err)
	}
}

func TestPostHogQueriesOnlyCheckedValues(t *testing.T) {
	cl, bodies := fake(t, map[string]any{
		"POST /api/projects/42/query/": map[string]any{"results": [][]any{{"/pricing", 30, 12}}},
	})
	q := query(c.ReportPages)
	q.Site = "42"
	rep, err := PostHog{}.Report(ctx, cl, c.Creds{"api_key": "phx"}, q)
	if err != nil || len(rep.Rows) != 1 || val(rep.Rows[0].Pageviews) != 30 {
		t.Fatalf("pages: %+v %v", rep, err)
	}
	if b := (*bodies)[0]; !strings.Contains(b, "2026-10-04 00:00:00") || !strings.Contains(b, "LIMIT 10") {
		t.Fatalf("sql: %s", b)
	}
	q.Site = "42' OR 1=1"
	if _, err := (PostHog{}).Report(ctx, cl, c.Creds{"api_key": "phx"}, q); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("injection: %v", err)
	}
}

func TestMatomoErrorsAndDays(t *testing.T) {
	cl, _ := fake(t, map[string]any{
		"POST /index.php": func(r *http.Request) any {
			r.ParseForm()
			if r.PostForm.Get("token_auth") != "tok" {
				return map[string]string{"result": "error", "message": "You must be logged in to access this functionality. token_auth is invalid"}
			}
			switch r.URL.Query().Get("method") + "/" + r.URL.Query().Get("period") {
			case "VisitsSummary.get/range":
				return map[string]any{"nb_visits": 20, "nb_uniq_visitors": 15, "bounce_rate": "40%"}
			case "Actions.get/range":
				return map[string]any{"nb_pageviews": 60}
			case "VisitsSummary.get/day":
				return map[string]any{"2026-10-01": map[string]any{"nb_visits": 20, "nb_uniq_visitors": 15}, "2026-10-02": []any{}, "2026-10-03": []any{}}
			case "Actions.get/day":
				return map[string]any{"2026-10-01": map[string]any{"nb_pageviews": 60}, "2026-10-02": []any{}, "2026-10-03": []any{}}
			}
			return []any{}
		},
	})
	q := query(c.ReportTraffic)
	q.Site = "1"
	rep, err := Matomo{}.Report(ctx, cl, c.Creds{"url": "https://m.example.com", "token": "tok"}, q)
	if err != nil || val(rep.Totals.Pageviews) != 60 || val(rep.Totals.BounceRate) != 0.4 || val(rep.Series[0].Visitors) != 15 || val(rep.Series[1].Sessions) != 0 {
		t.Fatalf("traffic: %+v %v", rep, err)
	}
	if _, err := (Matomo{}).Report(ctx, cl, c.Creds{"url": "https://m.example.com", "token": "nope"}, q); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("bad token: %v", err)
	}
}

func TestUmamiReadsOldAndNewStats(t *testing.T) {
	cl, _ := fake(t, map[string]any{
		"GET /websites/abc/stats": map[string]any{"pageviews": map[string]any{"value": 40}, "visitors": 10, "visits": 20, "bounces": 5, "totaltime": 600},
		"GET /websites/abc/pageviews": map[string]any{"pageviews": []any{map[string]any{"x": "2026-10-02 00:00:00", "y": 40}},
			"sessions": []any{map[string]any{"x": "2026-10-02T00:00:00Z", "y": 10}}},
	})
	q := query(c.ReportTraffic)
	q.Site = "abc"
	rep, err := Umami{}.Report(ctx, cl, c.Creds{"api_key": "k"}, q)
	if err != nil || val(rep.Totals.Pageviews) != 40 || val(rep.Totals.Visitors) != 10 || val(rep.Totals.BounceRate) != 0.25 || val(rep.Totals.AvgDuration) != 30 || val(rep.Series[1].Visitors) != 10 {
		t.Fatalf("umami: %+v %v", rep, err)
	}
	if _, err := (Umami{}).Report(ctx, cl, c.Creds{}, q); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("no creds: %v", err)
	}
}

func TestProductAnalytics(t *testing.T) {
	cl, _ := fake(t, map[string]any{
		"GET /api/query/segmentation": func(r *http.Request) any {
			n := 7.0
			if r.URL.Query().Get("type") == "unique" {
				n = 3
			}
			return map[string]any{"data": map[string]any{"series": []string{"2026-10-01"}, "values": map[string]any{"$mp_web_page_view": map[string]float64{"2026-10-01": n}}}}
		},
		"GET /api/2/events/segmentation": map[string]any{"data": map[string]any{"series": [][]float64{{4, 0, 2}}, "xValues": []string{"2026-10-01", "2026-10-02", "2026-10-03"},
			"seriesCollapsed": [][]map[string]float64{{{"value": 5}}}}},
	})
	mp, err := Mixpanel{}.Report(ctx, cl, c.Creds{"username": "u", "secret": "s", "project_id": "1"}, query(c.ReportTraffic))
	if err != nil || val(mp.Series[0].Pageviews) != 7 || val(mp.Series[0].Visitors) != 3 || mp.Totals.Visitors != nil {
		t.Fatalf("mixpanel: %+v %v", mp, err)
	}
	am, err := Amplitude{}.Report(ctx, cl, c.Creds{"api_key": "a", "secret_key": "s"}, query(c.ReportTraffic))
	if err != nil || val(am.Totals.Visitors) != 5 || val(am.Series[2].Visitors) != 2 {
		t.Fatalf("amplitude: %+v %v", am, err)
	}
}

func TestCloudflareGraphQL(t *testing.T) {
	cl, bodies := fake(t, map[string]any{
		"POST /graphql": func(r *http.Request) any {
			b, _ := io.ReadAll(r.Body)
			if strings.Contains(string(b), "requestPath") {
				return map[string]any{"data": map[string]any{"viewer": map[string]any{"accounts": []any{map[string]any{"g": []any{
					map[string]any{"count": 9, "sum": map[string]any{"visits": 4}, "dimensions": map[string]string{"requestPath": "/blog"}}}}}}}}
			}
			return map[string]any{"data": map[string]any{"viewer": map[string]any{"accounts": []any{map[string]any{"g": []any{map[string]any{"count": 20, "sum": map[string]any{"visits": 8}}}}}}}}
		},
	})
	q := query(c.ReportPages)
	q.Site = "tag1"
	rep, err := Cloudflare{}.Report(ctx, cl, c.Creds{"api_token": "t", "account_id": "acct"}, q)
	if err != nil || val(rep.Totals.Pageviews) != 20 || len(rep.Rows) != 1 || rep.Rows[0].Key != "/blog" || val(rep.Rows[0].Sessions) != 4 {
		t.Fatalf("cloudflare: %+v %v %v", rep, err, *bodies)
	}
}

func TestFathomSimpleAndClarity(t *testing.T) {
	cl, _ := fake(t, map[string]any{
		"GET /aggregations":          []any{map[string]any{"pathname": "/", "pageviews": "12", "uniques": "5", "visits": "6"}},
		"GET /acme-com.example.json": map[string]any{"pageviews": 50, "visitors": 20, "referrers": []any{map[string]any{"value": "news.ycombinator.com", "pageviews": 30, "visitors": 12}}},
		"GET /project-live-insights": []any{
			map[string]any{"metricName": "Traffic", "information": []any{map[string]any{"totalSessionCount": "100", "distinctUserCount": "80", "pagesPerSessionPercentage": 1.5}}},
			map[string]any{"metricName": "RageClickCount", "information": []any{map[string]any{"sessionsWithMetricPercentage": 2.5}}},
		},
	})
	q := query(c.ReportPages)
	q.Site = "ABCDEF"
	fa, err := Fathom{}.Report(ctx, cl, c.Creds{"api_key": "k"}, q)
	if err != nil || len(fa.Rows) != 1 || val(fa.Rows[0].Pageviews) != 12 {
		t.Fatalf("fathom: %+v %v", fa, err)
	}
	sa, err := SimpleAnalytics{}.Report(ctx, cl, c.Creds{"hostname": "https://acme-com.example/"}, query(c.ReportSources))
	if err != nil || val(sa.Totals.Visitors) != 20 || sa.Rows[0].Key != "news.ycombinator.com" {
		t.Fatalf("simple: %+v %v", sa, err)
	}
	cy, err := Clarity{}.Report(ctx, cl, c.Creds{"api_token": "t"}, query(c.ReportTraffic))
	if err != nil || val(cy.Totals.Sessions) != 100 || val(cy.Totals.Pageviews) != 150 || len(cy.Notes) != 2 || !strings.Contains(cy.Notes[1], "Rage clicks: 2.5%") {
		t.Fatalf("clarity: %+v %v", cy, err)
	}
}

func TestCheckAnalyticsQuery(t *testing.T) {
	now := time.Date(2026, 10, 24, 5, 0, 0, 0, time.UTC)
	q, err := c.CheckAnalyticsQuery(c.AnalyticsQuery{}, now)
	if err != nil || q.From != "2026-09-27" || q.To != "2026-10-24" || q.Limit != 20 || len(q.Days()) != 28 {
		t.Fatalf("defaults: %+v %v", q, err)
	}
	for _, bad := range []c.AnalyticsQuery{{From: "x"}, {From: "2026-10-05", To: "2026-10-01"}, {From: "2010-01-01", To: "2026-01-01"}} {
		if _, err := c.CheckAnalyticsQuery(bad, now); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}

func TestVercel(t *testing.T) {
	cl, bodies := fake(t, map[string]any{
		"GET /v9/projects":                         map[string]any{"projects": []any{map[string]any{"id": "prj_1", "name": "site"}}, "pagination": map[string]any{"next": nil}},
		"GET /v1/query/web-analytics/visits/count": map[string]any{"data": map[string]any{"pageviews": 30, "visitors": 12}},
		"GET /v1/query/web-analytics/visits/aggregate": func(r *http.Request) any {
			if r.URL.Query().Get("by") == "day" {
				return map[string]any{"data": []any{map[string]any{"timestamp": "2026-10-02T00:00:00Z", "pageviews": 7, "visitors": 3}}}
			}
			return map[string]any{"data": []any{
				map[string]any{"requestPath": "/pricing", "pageviews": 20, "visitors": 9},
				map[string]any{"requestPath": "Others", "pageviews": 10, "visitors": 3},
			}}
		},
		"GET /v1/query/web-analytics/events/aggregate": map[string]any{"data": []any{map[string]any{"eventName": "signup", "count": 4, "visitors": 4}}},
	})
	creds := c.Creds{"api_token": "t", "team_id": "team_abc"}
	sites, err := Vercel{}.Sites(ctx, cl, creds)
	if err != nil || len(sites) != 1 || sites[0].ID != "prj_1" {
		t.Fatalf("sites: %+v %v", sites, err)
	}
	q := query(c.ReportPages)
	q.Site = "prj_1"
	rep, err := Vercel{}.Report(ctx, cl, creds, q)
	if err != nil || val(rep.Totals.Pageviews) != 30 || len(rep.Rows) != 1 || rep.Rows[0].Key != "/pricing" || val(rep.Rows[0].Visitors) != 9 {
		t.Fatalf("pages: %+v %v %v", rep, err, *bodies)
	}
	if !strings.Contains(strings.Join(*bodies, "\n"), "teamId=team_abc") {
		t.Fatalf("team id not sent: %v", *bodies)
	}
	q.Report = c.ReportTraffic
	rep, err = Vercel{}.Report(ctx, cl, creds, q)
	var day float64
	for _, p := range rep.Series {
		if p.Date == "2026-10-02" {
			day = val(p.Pageviews)
		}
	}
	if err != nil || val(rep.Totals.Visitors) != 12 || day != 7 {
		t.Fatalf("traffic: %+v %v", rep, err)
	}
	q.Report = c.ReportEvents
	rep, err = Vercel{}.Report(ctx, cl, creds, q)
	if err != nil || len(rep.Rows) != 1 || val(rep.Rows[0].Events) != 4 || val(rep.Totals.Events) != 4 || rep.Totals.Visitors != nil {
		t.Fatalf("events: %+v %v", rep, err)
	}
	q.Report = c.ReportSearchQueries
	if _, err := (Vercel{}).Report(ctx, cl, creds, q); err == nil {
		t.Fatal("search queries should not be supported")
	}
}

func TestPostHogSignInFindsRegion(t *testing.T) {
	us := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	t.Cleanup(us.Close)
	eu := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer pha_1" || r.URL.Path != "/api/users/@me/" {
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"uuid": "u1", "email": "jo@example.com", "organization": map[string]any{"name": "Acme"}})
	}))
	t.Cleanup(eu.Close)
	cl := &c.Client{BaseURLs: map[string]string{"posthog.us": us.URL, "posthog.eu": eu.URL}}
	creds := c.Creds{"access_token": "pha_1"}
	acct, err := PostHog{}.Test(ctx, cl, creds)
	if err != nil || acct.Email != "jo@example.com" || creds["host"] != eu.URL {
		t.Fatalf("region: %+v %v %v", acct, err, creds)
	}
	d := PostHog{}.Definition()
	if d.Auth != c.AuthOAuth2 || !d.Supports(c.AuthAPIKey) || !d.OAuth.ClientMetadata || !strings.Contains(strings.Join(d.OAuth.Scopes, " "), "query:read") {
		t.Fatalf("definition: %+v", d.OAuth)
	}
}

// A Cloudflare sign-in has no account id: Test picks the account that has
// Web Analytics sites and remembers it.
func TestCloudflareSignInFindsAccount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cf_oauth" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/accounts":
			json.NewEncoder(w).Encode(map[string]any{"result": []any{map[string]string{"id": strings.Repeat("a", 32)}, map[string]string{"id": strings.Repeat("b", 32)}}})
		case "/accounts/" + strings.Repeat("b", 32) + "/rum/site_info/list":
			json.NewEncoder(w).Encode(map[string]any{"result": []any{map[string]any{"site_tag": "t1", "host": "example.com"}}})
		default:
			json.NewEncoder(w).Encode(map[string]any{"result": []any{}})
		}
	}))
	t.Cleanup(srv.Close)
	cl := &c.Client{BaseURLs: map[string]string{"cloudflare_web_analytics": srv.URL}}
	creds := c.Creds{"access_token": "cf_oauth"}
	acct, err := Cloudflare{}.Test(ctx, cl, creds)
	if err != nil || creds["account_id"] != strings.Repeat("b", 32) || acct.Details["sites"] != "1" {
		t.Fatalf("account: %+v %v %v", acct, err, creds)
	}
}
