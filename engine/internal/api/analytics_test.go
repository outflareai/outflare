package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/connectors"
)

var plausibleCalls atomic.Int64

// fakePlausible answers the Stats API for example.com with the key "pk".
type fakePlausible struct{}

func (fakePlausible) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	plausibleCalls.Add(1)
	if r.Header.Get("Authorization") != "Bearer pk" {
		w.WriteHeader(401)
		return
	}
	var q struct {
		SiteID     string   `json:"site_id"`
		Dimensions []string `json:"dimensions"`
		DateRange  []string `json:"date_range"`
	}
	json.NewDecoder(r.Body).Decode(&q)
	if q.SiteID != "example.com" {
		w.WriteHeader(400)
		w.Write([]byte(`{"error":"site not found"}`))
		return
	}
	res := []any{}
	switch {
	case len(q.Dimensions) == 0:
		res = append(res, map[string]any{"dimensions": []any{}, "metrics": []any{30, 90, 40, 25, 61}})
	case q.Dimensions[0] == "time:day":
		res = append(res, map[string]any{"dimensions": []any{q.DateRange[0]}, "metrics": []any{10, 30, 12, 20, 50}})
	case q.Dimensions[0] == "event:page":
		res = append(res, map[string]any{"dimensions": []any{"/pricing"}, "metrics": []any{50, 20}}, map[string]any{"dimensions": []any{"/"}, "metrics": []any{40, 25}})
	}
	json.NewEncoder(w).Encode(map[string]any{"results": res})
}

func TestAnalyticsReports(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	// Nothing connected is not an error.
	if got := owner.must(200, "GET", base+"/analytics", nil)["data"].([]any); len(got) != 0 {
		t.Fatalf("empty: %v", got)
	}
	cat := owner.must(200, "GET", base+"/connectors?category=analytics", nil)["data"].([]any)
	if len(cat) < 13 {
		t.Fatalf("analytics catalog: %d", len(cat))
	}

	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})
	id := i["id"].(string)
	list := owner.must(200, "GET", base+"/analytics", nil)["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["connector"] != "plausible" {
		t.Fatalf("list: %v", list)
	}

	rep := owner.must(200, "GET", base+"/analytics/"+id+"/reports/traffic?from=2026-10-01&to=2026-10-03", nil)
	totals := rep["totals"].(map[string]any)
	series := rep["series"].([]any)
	if rep["site"] != "example.com" || totals["visitors"] != 30.0 || totals["bounce_rate"] != 0.25 || len(series) != 3 || rep["cached"] != false {
		t.Fatalf("traffic: %v", rep)
	}
	if day := series[1].(map[string]any); day["visitors"] != 0.0 {
		t.Fatalf("missing day should be zero: %v", day)
	}
	before := plausibleCalls.Load()
	again := owner.must(200, "GET", base+"/analytics/"+id+"/reports/traffic?from=2026-10-01&to=2026-10-03", nil)
	if again["cached"] != true || plausibleCalls.Load() != before {
		t.Fatalf("second read should come from the cache: %v", again["cached"])
	}

	pages := owner.must(200, "GET", base+"/analytics/"+id+"/reports/pages?from=2026-10-01&to=2026-10-03&limit=5", nil)
	rows := pages["rows"].([]any)
	if len(rows) != 2 || rows[0].(map[string]any)["key"] != "/pricing" || rows[0].(map[string]any)["pageviews"] != 50.0 {
		t.Fatalf("pages: %v", pages)
	}

	// Plausible has no search queries; bad input is explained.
	owner.must(422, "GET", base+"/analytics/"+id+"/reports/search_queries", nil)
	owner.must(404, "GET", base+"/analytics/"+id+"/reports/nope", nil)
	owner.must(400, "GET", base+"/analytics/"+id+"/reports/traffic?from=yesterday", nil)
	owner.must(400, "GET", base+"/analytics/"+id+"/reports/traffic?from=2026-10-05&to=2026-10-01", nil)

	sites := owner.must(200, "GET", base+"/analytics/"+id+"/sites", nil)["data"].([]any)
	if len(sites) != 1 {
		t.Fatalf("sites: %v", sites)
	}
	owner.must(400, "PUT", base+"/analytics/"+id+"/site", map[string]string{"site": "other-com.example"})
	owner.must(200, "PUT", base+"/analytics/"+id+"/site", map[string]string{"site": "example.com"})

	sum := owner.must(200, "GET", base+"/analytics/summary?from=2026-10-01&to=2026-10-03", nil)["data"].([]any)
	if len(sum) != 1 || sum[0].(map[string]any)["error"] != nil || sum[0].(map[string]any)["totals"].(map[string]any)["pageviews"] != 90.0 {
		t.Fatalf("summary: %v", sum)
	}

	// Other connections are not analytics.
	zb := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "zerobounce", "credentials": map[string]string{"api_key": "good-key"}})
	if code, out := owner.do("GET", base+"/analytics/"+zb["id"].(string)+"/reports/traffic", nil); code != 400 || !strings.Contains(out["error"].(map[string]any)["message"].(string), "not an analytics") {
		t.Fatalf("zerobounce: %d %v", code, out)
	}

	// A viewer token without analytics.read is refused.
	agentToken(t, owner, base, "no analytics", "workspace.read").must(403, "GET", base+"/analytics", nil)
	agentToken(t, owner, base, "analytics", "analytics.read").must(200, "GET", base+"/analytics/"+id+"/reports/pages", nil)
}

// One Google sign-in for Google Analytics also connects Search Console.
func TestGoogleSignInConnectsBoth(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	g := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/": // the token URL is replaced whole
			json.NewEncoder(w).Encode(map[string]any{"access_token": "gat", "refresh_token": "grt", "expires_in": 3600, "token_type": "Bearer"})
		case "/v1/userinfo":
			json.NewEncoder(w).Encode(map[string]any{"sub": "1", "email": "jo@example.com"})
		case "/v1beta/accountSummaries":
			json.NewEncoder(w).Encode(map[string]any{"accountSummaries": []any{map[string]any{"displayName": "Acme", "propertySummaries": []any{map[string]any{"property": "properties/9", "displayName": "Site"}, map[string]any{"property": "properties/10", "displayName": "Shop"}}}}})
		case "/v1beta/properties/9:runReport", "/v1beta/properties/10:runReport":
			json.NewEncoder(w).Encode(map[string]any{"rows": []any{}})
		case "/webmasters/v3/sites":
			json.NewEncoder(w).Encode(map[string]any{"siteEntry": []any{map[string]any{"siteUrl": "sc-domain:example.com", "permissionLevel": "siteOwner"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.Close)
	for _, id := range []string{"google_analytics", "google_analytics.token", "google_analytics.admin", "google.userinfo", "google_search_console"} {
		e.integrations.Client.BaseURLs[id] = g.URL
	}
	app := connectors.OAuthApp{ClientID: "cid", ClientSecret: "secret"}
	e.integrations.OAuthApps = map[string]connectors.OAuthApp{"google_analytics": app, "google_search_console": app}

	link := owner.must(200, "GET", base+"/integrations/oauth/google_analytics/authorize?format=json", nil)["url"].(string)
	u, _ := url.Parse(link)
	if s := u.Query().Get("scope"); !strings.Contains(s, "analytics.readonly") || !strings.Contains(s, "webmasters.readonly") {
		t.Fatalf("scopes: %s", s)
	}
	cb := follow(t, owner, "/v1/integrations/oauth/callback?code=abc&state="+url.QueryEscape(u.Query().Get("state")))
	if cb.Query().Get("integration") == "" || cb.Query().Get("also") != "Google Search Console" {
		t.Fatalf("callback: %s", cb)
	}
	got := owner.must(200, "GET", base+"/analytics", nil)["data"].([]any)
	if len(got) != 2 {
		t.Fatalf("want both Google tools connected: %v", got)
	}

	// Both properties sync, and reports read the first one that does
	// until a default is set.
	var ga string
	for _, x := range got {
		if x.(map[string]any)["connector"] == "google_analytics" {
			ga = x.(map[string]any)["id"].(string)
		}
	}
	sites := owner.must(200, "GET", base+"/analytics/"+ga+"/sites", nil)["data"].([]any)
	if len(sites) != 2 || sites[0].(map[string]any)["sync"] != true || sites[1].(map[string]any)["sync"] != true {
		t.Fatalf("sites: %v", sites)
	}
	if rep := owner.must(200, "GET", base+"/analytics/"+ga+"/reports/traffic", nil); rep["site"] != "9" {
		t.Fatalf("first synced site: %v", rep["site"])
	}
	owner.must(200, "PATCH", base+"/analytics/"+ga+"/sites/9", map[string]any{"sync": false})
	if rep := owner.must(200, "GET", base+"/analytics/"+ga+"/reports/traffic", nil); rep["site"] != "10" {
		t.Fatalf("after switching 9 off: %v", rep["site"])
	}

	// Signing in again does not add a second Search Console.
	link = owner.must(200, "GET", base+"/integrations/oauth/google_search_console/authorize?format=json", nil)["url"].(string)
	u, _ = url.Parse(link)
	cb = follow(t, owner, "/v1/integrations/oauth/callback?code=abc&state="+url.QueryEscape(u.Query().Get("state")))
	if cb.Query().Get("also") != "" {
		t.Fatalf("second sign-in: %s", cb)
	}
}

// PostHog signs in with a client document the server hosts, and the
// connection remembers the region the account lives in.
func TestPostHogSignIn(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	doc := owner.must(200, "GET", "/v1/integrations/oauth/clients/posthog.json", nil)
	if doc["client_id"] != "http://localhost:8080/v1/integrations/oauth/clients/posthog.json" || doc["token_endpoint_auth_method"] != "none" ||
		!strings.Contains(fmt.Sprint(doc["com.posthog"]), "query:read") || !strings.Contains(fmt.Sprint(doc["redirect_uris"]), "/v1/integrations/oauth/callback") {
		t.Fatalf("client document: %v", doc)
	}
	owner.must(404, "GET", "/v1/integrations/oauth/clients/plausible.json", nil)

	var tokenForm url.Values
	ph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			r.ParseForm()
			tokenForm = r.PostForm
			json.NewEncoder(w).Encode(map[string]any{"access_token": "pha_1", "refresh_token": "phr_1", "expires_in": 3600})
		case "/api/users/@me/":
			json.NewEncoder(w).Encode(map[string]any{"uuid": "u1", "email": "jo@example.com", "organization": map[string]any{"name": "Acme"}})
		case "/api/projects/":
			json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"id": 7, "name": "Web"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ph.Close)
	e.integrations.Client.BaseURLs["posthog.token"] = ph.URL
	e.integrations.Client.BaseURLs["posthog.us"] = ph.URL
	e.integrations.OAuthApps = map[string]connectors.OAuthApp{"posthog": {ClientID: doc["client_id"].(string)}}

	link := owner.must(200, "GET", base+"/integrations/oauth/posthog/authorize?format=json", nil)["url"].(string)
	u, _ := url.Parse(link)
	if u.Query().Get("client_id") != doc["client_id"] || u.Query().Get("code_challenge") == "" {
		t.Fatalf("authorize: %s", link)
	}
	cb := follow(t, owner, "/v1/integrations/oauth/callback?code=abc&state="+url.QueryEscape(u.Query().Get("state")))
	if cb.Query().Get("integration") == "" {
		t.Fatalf("callback: %s", cb)
	}
	if _, sent := tokenForm["client_secret"]; sent || tokenForm.Get("code_verifier") == "" {
		t.Fatalf("token form: %v", tokenForm)
	}
	sites := owner.must(200, "GET", base+"/analytics/"+cb.Query().Get("integration")+"/sites", nil)["data"].([]any)
	if len(sites) != 1 {
		t.Fatalf("sites: %v", sites)
	}
	if got := owner.must(200, "GET", base+"/integrations/"+cb.Query().Get("integration"), nil); got["auth_method"] != "oauth2" || got["connector"] != "posthog" {
		t.Fatalf("connection: %v", got)
	}
}

// The sync stores each tool's numbers, and history, overview and compare
// read them back without calling the tool.
func TestAnalyticsSyncAndHistory(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})
	id := i["id"].(string)

	got := owner.must(200, "POST", base+"/analytics/"+id+"/sync", nil)
	if got["rows"].(float64) < 3 || got["calls"].(float64) < 3 {
		t.Fatalf("sync: %v", got)
	}
	before := plausibleCalls.Load()

	yesterday := time.Now().UTC().AddDate(0, 0, -1)
	from := yesterday.AddDate(0, 0, -89).Format(time.DateOnly)
	hist := owner.must(200, "GET", base+"/analytics/history?report=traffic&from="+from, nil)
	if rows := hist["data"].([]any); len(rows) != 90 {
		t.Fatalf("traffic history should cover the 90-day backfill: %d", len(rows))
	}
	pages := owner.must(200, "GET", base+"/analytics/history?report=pages&from="+yesterday.AddDate(0, 0, -6).Format(time.DateOnly), nil)["data"].([]any)
	top := pages[0].(map[string]any)
	if len(pages) != 2 || top["key"] != "/pricing" || top["pageviews"].(float64) != 7*50 {
		t.Fatalf("pages summed over 7 days: %v", pages)
	}

	ov := owner.must(200, "GET", base+"/analytics/overview?from="+from, nil)
	sites := ov["data"].([]any)
	if len(sites) != 1 || sites[0].(map[string]any)["host"] != "example.com" {
		t.Fatalf("overview: %v", ov)
	}
	if tool := sites[0].(map[string]any)["tools"].([]any)[0].(map[string]any); tool["site_name"] == "" {
		t.Fatalf("overview tool has no site name: %v", tool)
	}
	cmp := owner.must(200, "GET", base+"/analytics/compare?host=www.example.com", nil)["data"].([]any)
	if len(cmp) != 1 || len(cmp[0].(map[string]any)["series"].([]any)) != 28 {
		t.Fatalf("compare: %v", cmp)
	}
	owner.must(400, "GET", base+"/analytics/compare", nil)
	if plausibleCalls.Load() != before {
		t.Fatal("history reads must not call the tool")
	}

	// A second run re-reads only the recent days.
	again := owner.must(200, "POST", base+"/analytics/"+id+"/sync", nil)
	if again["calls"].(float64) >= got["calls"].(float64) {
		t.Fatalf("second run should be smaller: %v vs %v", again, got)
	}
	syncs := owner.must(200, "GET", base+"/analytics/syncs", nil)["data"].([]any)
	if s := syncs[0].(map[string]any); s["host"] != "example.com" || s["last_error"] != nil || s["first_day"] != from {
		t.Fatalf("syncs: %v", s)
	}

	// Days stored under another host follow the site's on its next run,
	// though that run only re-reads the recent days.
	if _, err := e.pool.Exec(context.Background(), `UPDATE analytics_daily SET host = '' WHERE integration_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	owner.must(200, "POST", base+"/analytics/"+id+"/sync", nil)
	if ov := owner.must(200, "GET", base+"/analytics/overview", nil)["data"].([]any); len(ov) != 1 || ov[0].(map[string]any)["host"] != "example.com" {
		t.Fatalf("old days keep their host: %v", ov)
	}

	// A domain set by hand moves the stored rows with it.
	owner.must(200, "PUT", base+"/analytics/"+id+"/site", map[string]any{"site": "example.com", "domain": "https://Shop.Example.org/"})
	if ov := owner.must(200, "GET", base+"/analytics/overview", nil)["data"].([]any); ov[0].(map[string]any)["host"] != "shop.example.org" {
		t.Fatalf("domain: %v", ov)
	}
	owner.must(400, "PUT", base+"/analytics/"+id+"/site", map[string]any{"site": "example.com", "domain": "not a domain"})
	owner.must(202, "POST", base+"/analytics/sync", nil)

	// Each site has its own domain and can be switched off.
	list := owner.must(200, "GET", base+"/analytics/"+id+"/sites", nil)["data"].([]any)
	if s := list[0].(map[string]any); s["id"] != "example.com" || s["host"] != "shop.example.org" || s["sync"] != true {
		t.Fatalf("sites: %v", s)
	}
	site := base + "/analytics/" + id + "/sites/" + url.PathEscape("example.com")
	if got := owner.must(200, "PATCH", site, map[string]any{"domain": "example.net"}); got["host"] != "example.net" || got["sync"] != true {
		t.Fatalf("patch domain: %v", got)
	}
	if ov := owner.must(200, "GET", base+"/analytics/overview", nil)["data"].([]any); ov[0].(map[string]any)["host"] != "example.net" {
		t.Fatalf("overview after domain: %v", ov)
	}
	if got := owner.must(200, "PATCH", site, map[string]any{"sync": false}); got["sync"] != false || got["host"] != "example.net" {
		t.Fatalf("patch off: %v", got)
	}
	if ov := owner.must(200, "GET", base+"/analytics/overview", nil)["data"].([]any); len(ov) != 0 {
		t.Fatalf("a site switched off leaves the overview: %v", ov)
	}
	if s := owner.must(200, "GET", base+"/analytics/"+id+"/sites", nil)["data"].([]any)[0].(map[string]any); s["sync"] != false {
		t.Fatalf("sites after off: %v", s)
	}
	// Rows a run stored after the site was switched off go on the next run.
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO analytics_daily (workspace_id, integration_id, connector, site, host, report, day, key, visitors)
		VALUES ($1, $2, 'plausible', 'example.com', 'example.net', 'traffic', $3, '', 5)`, ws, id, time.Now().UTC().AddDate(0, 0, -2).Format(time.DateOnly)); err != nil {
		t.Fatal(err)
	}
	owner.must(200, "POST", base+"/analytics/"+id+"/sync", nil)
	if ov := owner.must(200, "GET", base+"/analytics/overview", nil)["data"].([]any); len(ov) != 0 {
		t.Fatalf("a switched-off site's late rows stay: %v", ov)
	}
	owner.must(404, "PATCH", base+"/analytics/"+id+"/sites/other-com.example", map[string]any{"sync": true})
	owner.must(400, "PATCH", site, map[string]any{"domain": "not a domain"})
}
