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

// fakePostHog is one PostHog project, "Web" (id 1), whose events count
// visits by landing page. It answers every other query with no rows.
type fakePostHog struct {
	mu      sync.Mutex
	queries []string
}

func (f *fakePostHog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
		results := [][]any{}
		if strings.Contains(sql, "argMinIf(properties.$pathname") {
			f.mu.Lock()
			f.queries = append(f.queries, sql)
			f.mu.Unlock()
			conv := strings.Contains(sql, "'signed_up'")
			n := func(v int) int {
				if conv {
					return v
				}
				return 0
			}
			day := time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly)
			results = [][]any{
				{day, "www.example.com", "/pricing", 40, 35, n(8), 20, n(6)},
				{day, "www.example.com", "/blog/proxies", 100, 90, n(1), 80, n(1)},
				{day, "docs.example.com", "/start", 50, 50, n(5), 10, n(1)},
				{"2020-01-01", "www.example.com", "/old", 9, 9, 9, 9, 9},
			}
		}
		enc.Encode(map[string]any{"results": results})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakePostHog) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queries) == 0 {
		return ""
	}
	return f.queries[len(f.queries)-1]
}

func TestPageConversions(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	site := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.example.com/"})["id"].(string)
	sb := base + "/seo-sites/" + site

	if c := owner.must(200, "GET", sb+"/conversions", nil); c["source"] != nil || len(c["data"].([]any)) != 0 {
		t.Fatalf("nothing counted yet: %v", c)
	}

	f := &fakePostHog{}
	ph := httptest.NewServer(f)
	defer ph.Close()
	e.integrations.Client.BaseURLs["posthog"] = ph.URL
	id := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "posthog", "credentials": map[string]string{"api_key": "phx_test_not_a_real_key"}})["id"].(string)
	owner.must(200, "POST", base+"/analytics/"+id+"/sync", nil)
	if q := f.last(); q == "" || strings.Contains(q, "event IN") {
		t.Fatalf("visits only without conversion events: %q", q)
	}

	// Visits by landing page, before any event counts as a conversion.
	c := owner.must(200, "GET", sb+"/conversions?sort=sessions", nil)
	src := c["source"].(map[string]any)
	if src["integration_id"] != id || src["site"] != "1" || len(src["events"].([]any)) != 0 {
		t.Fatalf("source: %v", src)
	}
	data := c["data"].([]any)
	if len(data) != 2 || data[0].(map[string]any)["url"] != "https://www.example.com/blog/proxies" || data[0].(map[string]any)["conversions"].(float64) != 0 {
		t.Fatalf("visits: %v", data)
	}

	// Only PostHog-safe event names.
	owner.must(400, "PATCH", base+"/analytics/"+id+"/sites/1", map[string]any{"conversion_events": []string{"bad'name"}})
	p := owner.must(200, "PATCH", base+"/analytics/"+id+"/sites/1", map[string]any{"conversion_events": []string{"signed_up", "signed_up"}})
	if ev := p["conversion_events"].([]any); len(ev) != 1 || ev[0] != "signed_up" {
		t.Fatalf("events: %v", p)
	}
	owner.must(200, "POST", base+"/analytics/"+id+"/sync", nil)
	ninety := time.Now().UTC().AddDate(0, 0, -90).Format(time.DateOnly)
	if q := f.last(); !strings.Contains(q, "event IN ('signed_up')") || !strings.Contains(q, ninety) {
		t.Fatalf("new events read the 90 days again: %q", q)
	}

	owner.must(200, "PUT", sb+"/targets", map[string]any{"url": "https://www.example.com/pricing", "keyword": "proxy pricing"})
	c = owner.must(200, "GET", sb+"/conversions", nil)
	top := c["data"].([]any)[0].(map[string]any)
	if top["url"] != "https://www.example.com/pricing" || top["conversions"].(float64) != 8 || top["conversion_rate"].(float64) != 0.2 ||
		top["search_conversions"].(float64) != 6 || top["keyword"] != "proxy pricing" || top["clicks"] != nil {
		t.Fatalf("pricing: %v", top)
	}
	// docs.example.com is another site, and old days are outside the window.
	if tot := c["totals"].(map[string]any); tot["sessions"].(float64) != 140 || tot["conversions"].(float64) != 9 {
		t.Fatalf("totals: %v", tot)
	}
	owner.must(400, "GET", sb+"/conversions?sort=views", nil)

	// Another workspace sees none of it.
	other := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Other"})["id"].(string)
	owner.must(404, "GET", "/v1/workspaces/"+other+"/seo-sites/"+site+"/conversions", nil)

	// Switching the project off deletes what it counted.
	owner.must(200, "PATCH", base+"/analytics/"+id+"/sites/1", map[string]any{"sync": false})
	if c := owner.must(200, "GET", sb+"/conversions", nil); c["source"] != nil {
		t.Fatalf("switched off: %v", c)
	}

	var synced, backfilled, regressions int
	for _, ev := range got() {
		if ev.Name == "seo_regressions.checked" && ev.WorkspaceID == ws && ev.Props["trigger"] == "conversions" {
			regressions++
		}
		if ev.Name == "page_conversions.synced" && ev.WorkspaceID == ws && ev.Outcome == track.Success {
			synced++
			if ev.Props["backfilled"] == true {
				backfilled++
			}
		}
	}
	if synced != 2 || backfilled != 2 || regressions != 2 {
		t.Fatalf("page_conversions.synced: %d, backfilled %d; seo_regressions.checked %d", synced, backfilled, regressions)
	}
}
