package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

// fakeOrganicPostHog answers PostHog query with first-touch organic
// sessions, signups and payment events for www.example.com. Every other
// query returns no rows, so the existing page-conversions sync still runs.
type fakeOrganicPostHog struct {
	mu      sync.Mutex
	queries []string
}

func (f *fakeOrganicPostHog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	enc := json.NewEncoder(w)
	switch {
	case r.URL.Path == "/api/users/@me/":
		enc.Encode(map[string]any{"uuid": "u1", "email": "jo@example.com", "organization": map[string]any{"name": "Acme"}})
	case r.URL.Path == "/api/projects/":
		enc.Encode(map[string]any{"results": []any{map[string]any{"id": 1, "name": "Web"}}})
	case r.URL.Path == "/api/projects/1/query/":
		var body struct {
			Query struct {
				Query string `json:"query"`
			} `json:"query"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		sql := body.Query.Query
		f.mu.Lock()
		f.queries = append(f.queries, sql)
		f.mu.Unlock()
		day := time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly)
		results := [][]any{}
		switch {
		case strings.Contains(sql, "argMinIf(properties.$pathname"):
			// Visits for the existing conversions sync; values are unused here.
			results = [][]any{{day, "www.example.com", "/pricing", 20, 18, 0, 12, 0}}
		case strings.Contains(sql, "FROM sessions") || strings.Contains(sql, "$channel_type"):
			results = [][]any{
				{day, "www.example.com", "/pricing", 40},
				{day, "www.example.com", "/fr/pricing", 8},
				{day, "www.example.com", "/blog/intro", 30},
				{day, "www.example.com", "/about", 12},
			}
		case strings.Contains(sql, "user_signed_up"):
			results = [][]any{
				{day, "www.example.com", "/pricing", 3, 3},
				{day, "www.example.com", "/blog/intro", 2, 2},
				{day, "www.example.com", "/about", 1, 1},
			}
		case strings.Contains(sql, "event IN ('payment_succeeded'"):
			// day, host, path, person, event, amount, currency, trial, wallet, tx
			results = [][]any{
				{day, "www.example.com", "/pricing", "p1", "payment_succeeded", 49.0, "usd", false, false, "tx-1"},
				{day, "www.example.com", "/pricing", "p1", "service_purchased", 49.0, "USD", false, false, ""},
				{day, "www.example.com", "/pricing", "p2", "service_purchased", 19.0, "usd", true, false, ""},
				{day, "www.example.com", "/pricing", "p3", "service_purchased", 10.0, "usd", false, true, ""},
				{day, "www.example.com", "/pricing", "p4", "service_purchased", 99.0, "usd", false, false, ""},
				{day, "www.example.com", "/blog/intro", "p5", "payment_succeeded", 30.0, "usd", false, false, "tx-5"},
				{day, "www.example.com", "/about", "p6", "payment_succeeded", 20.0, "eur", false, false, "tx-6"},
			}
		case strings.Contains(sql, "min(toDate(timestamp))") && strings.Contains(sql, "payment_succeeded"):
			results = [][]any{{day}}
		}
		enc.Encode(map[string]any{"results": results})
	default:
		http.NotFound(w, r)
	}
}

func TestOrganicConversions(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "website": "https://www.example.com"})["id"].(string)
	site := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.example.com/", "brand_id": brand})["id"].(string)
	root := "/v1/brands/" + brand + "/seo-sites/" + site

	if c := owner.must(200, "GET", root+"/organic-conversions", nil); c["source"] != nil {
		t.Fatalf("nothing counted yet: %v", c)
	}

	owner.must(200, "POST", root+"/search-groups", map[string]any{"name": "Pricing", "kind": "content", "rules": []any{map[string]any{"operator": "prefix", "value": "/pricing"}}})
	owner.must(200, "POST", root+"/search-groups", map[string]any{"name": "Blog", "kind": "content", "rules": []any{map[string]any{"operator": "prefix", "value": "/blog/"}}})
	// Overlaps Pricing, to prove group totals must not be added together.
	owner.must(200, "POST", root+"/search-groups", map[string]any{"name": "All pricing URLs", "kind": "content", "rules": []any{map[string]any{"operator": "contains", "value": "pricing"}}})

	f := &fakeOrganicPostHog{}
	ph := httptest.NewServer(f)
	defer ph.Close()
	e.integrations.Client.BaseURLs["posthog"] = ph.URL
	id := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "posthog", "credentials": map[string]string{"api_key": "phx_test_not_a_real_key"}})["id"].(string)
	owner.must(200, "POST", base+"/analytics/"+id+"/sync", nil)

	c := owner.must(200, "GET", root+"/organic-conversions?sort=revenue", nil)
	src := c["source"].(map[string]any)
	if src["integration_id"] != id || src["site"] != "1" || src["attribution"] != "first_touch_organic" || src["currency"] != "USD" {
		t.Fatalf("source: %v", src)
	}
	pages := map[string]map[string]any{}
	for _, row := range c["data"].([]any) {
		m := row.(map[string]any)
		pages[m["path"].(string)] = m
	}
	pricing := pages["/pricing"]
	if pricing == nil || pricing["payments"].(float64) != 2 || pricing["revenue_usd"].(float64) != 148 || pricing["signups"].(float64) != 3 {
		t.Fatalf("pricing page should count payment_succeeded $49 plus unmatched service_purchased $99, not the duplicate or trials: %v", pricing)
	}
	if pricing["canonical_path"] != "/pricing" {
		t.Fatalf("canonical: %v", pricing)
	}
	fr := pages["/fr/pricing"]
	if fr == nil || fr["canonical_path"] != "/pricing" || fr["organic_sessions"].(float64) != 8 {
		t.Fatalf("localized path: %v", fr)
	}
	// /fr/pricing maps to the Pricing groups.
	names := []string{}
	for _, g := range fr["groups"].([]any) {
		names = append(names, g.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "All pricing URLs,Pricing" && strings.Join(names, ",") != "Pricing,All pricing URLs" {
		// order follows group name sort in attach (API iterates load order, name sort)
		if !containsAll(names, "Pricing", "All pricing URLs") {
			t.Fatalf("fr groups: %v", names)
		}
	}
	if pages["/about"]["revenue_usd"].(float64) != 0 {
		t.Fatalf("EUR payment must be skipped: %v", pages["/about"])
	}
	tot := c["totals"].(map[string]any)
	// Unique: pricing 148 + blog 30 = 178; sessions 40+8+30+12=90; signups 6
	if tot["revenue_usd"].(float64) != 178 || tot["payments"].(float64) != 3 || tot["organic_sessions"].(float64) != 90 || tot["signups"].(float64) != 6 {
		t.Fatalf("unique totals: %v", tot)
	}

	cl := owner.must(200, "GET", root+"/organic-conversions?group=cluster&sort=revenue", nil)
	clusters := map[string]map[string]any{}
	for _, row := range cl["data"].([]any) {
		m := row.(map[string]any)
		clusters[m["name"].(string)] = m
	}
	if clusters["Pricing"] == nil || clusters["Pricing"]["revenue_usd"].(float64) != 148 || clusters["All pricing URLs"]["revenue_usd"].(float64) != 148 {
		t.Fatalf("overlapping groups must each show pricing revenue, not a split: %v", cl["data"])
	}
	if clusters["Blog"]["revenue_usd"].(float64) != 30 || clusters["Ungrouped"]["revenue_usd"].(float64) != 0 || clusters["Ungrouped"]["signups"].(float64) != 1 {
		t.Fatalf("blog and ungrouped: %v", cl["data"])
	}
	// Overlapping group revenues must not be mistaken for the site total.
	if cl["totals"].(map[string]any)["revenue_usd"].(float64) != 178 {
		t.Fatalf("cluster totals stay unique: %v", cl["totals"])
	}

	owner.must(400, "GET", root+"/organic-conversions?sort=views", nil)
	owner.must(400, "GET", root+"/organic-conversions?group=topic", nil)

	other := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Other"})["id"].(string)
	owner.must(404, "GET", "/v1/workspaces/"+other+"/seo-sites/"+site+"/organic-conversions", nil)

	qbase := "/v1/brands/" + brand + "/query"
	q := owner.must(200, "POST", qbase, map[string]any{"sql": "select path, sum(revenue_usd) from seo_organic_conversions_by_page group by 1 order by 1"})
	rows := queryRows(t, q)
	found := map[string]float64{}
	for _, r := range rows {
		found[r[0].(string)] = r[1].(float64)
	}
	if found["/pricing"] != 148 || found["/blog/intro"] != 30 {
		t.Fatalf("query by page: %#v from %v", found, q)
	}
	q = owner.must(200, "POST", qbase, map[string]any{"sql": "select group_name, sum(revenue_usd) from seo_organic_conversions_by_cluster group by 1 order by 1"})
	rows = queryRows(t, q)
	byName := map[string]float64{}
	for _, r := range rows {
		byName[r[0].(string)] = r[1].(float64)
	}
	if byName["Pricing"] != 148 || byName["Blog"] != 30 {
		t.Fatalf("query by cluster: %#v from %v", byName, q)
	}

	// Switching the project off deletes what it counted.
	owner.must(200, "PATCH", base+"/analytics/"+id+"/sites/1", map[string]any{"sync": false})
	if c := owner.must(200, "GET", root+"/organic-conversions", nil); c["source"] != nil {
		t.Fatalf("switched off: %v", c)
	}

	var synced, backfilled int
	for _, ev := range got() {
		if ev.Name == "organic_conversions.synced" && ev.WorkspaceID == ws && ev.Outcome == track.Success {
			synced++
			if ev.Props["backfilled"] == true {
				backfilled++
			}
		}
	}
	if synced != 1 || backfilled != 1 {
		t.Fatalf("organic_conversions.synced: %d, backfilled %d", synced, backfilled)
	}
}
