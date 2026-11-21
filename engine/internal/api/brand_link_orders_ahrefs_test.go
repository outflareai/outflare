package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/outflareai/outflare/engine/internal/coverage/linkorders"
)

// Catalog rows say whether a site is worth a link; an Ahrefs check reads
// only the few numbers that decide it, on the brand's own Ahrefs.
func TestLinkOrderAhrefsCheck(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	root := "/v1/brands/" + brand + "/link-orders/catalog"
	if code, out := owner.raw("POST", root+"/import", "text/csv", intermediaryCSV); code != 200 || out["created"] != 2.0 {
		t.Fatalf("import %d: %v", code, out)
	}
	rows := map[string]map[string]any{}
	ids := []string{}
	for _, d := range owner.must(200, "GET", root, nil)["data"].([]any) {
		m := d.(map[string]any)
		rows[m["domain"].(string)] = m
		ids = append(ids, m["id"].(string))
	}
	// From the intermediary's numbers alone: news.example (DR 54, 12,500
	// visits) links out to 250 domains for 1,200 referring ones (0.21,
	// fine); blog.example has 800 visits (under 5,000) and links out to 30
	// for 90 (0.33, over 0.3).
	if q := rows["news.example"]["quality"].(map[string]any); q["verdict"] != "ok" || q["grade"] != "normal" || q["out_in"] != 0.21 {
		t.Fatalf("news quality: %v", q)
	}
	if q := rows["blog.example"]["quality"].(map[string]any); q["verdict"] != "skip" || len(q["reasons"].([]any)) != 2 {
		t.Fatalf("blog quality: %v", q)
	}

	// No Ahrefs connection: a clear refusal.
	if c := owner.must(409, "POST", root+"/ahrefs-check", map[string]any{"ids": ids})["error"].(map[string]any)["code"]; c != "connector_needed" {
		t.Fatalf("no connection: %v", c)
	}

	var calls atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if strings.Contains(r.URL.RawQuery, "country=") {
			http.Error(w, "a donor's traffic is worldwide", http.StatusBadRequest)
			return
		}
		var body any
		switch r.URL.Path {
		case "/subscription-info/limits-and-usage":
			body = map[string]any{"limits_and_usage": map[string]any{"subscription": "Advanced", "units_limit_api_key": 100000, "units_usage_api_key": 0}}
		case "/site-explorer/domain-rating":
			body = map[string]any{"domain_rating": map[string]any{"domain_rating": 58}}
		case "/site-explorer/metrics":
			body = map[string]any{"metrics": map[string]any{"org_traffic": 20000}}
		case "/site-explorer/backlinks-stats":
			body = map[string]any{"metrics": map[string]any{"live_refdomains": 1500}}
		case "/site-explorer/outlinks-stats":
			body = map[string]any{"metrics": map[string]any{"linked_domains": 150}}
		case "/site-explorer/top-pages":
			body = map[string]any{"pages": []any{
				map[string]any{"url": "https://news.example/", "sum_traffic": 10000},
				map[string]any{"url": "https://news.example/a", "sum_traffic": 2000},
				map[string]any{"url": "https://news.example/b", "sum_traffic": 1000},
			}}
		default:
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(fake.Close)
	if e.integrations.Client.BaseURLs == nil {
		e.integrations.Client.BaseURLs = map[string]string{}
	}
	e.integrations.Client.BaseURLs["ahrefs"] = fake.URL
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "ahrefs", "credentials": map[string]string{"api_key": "ahrefs-test-not-a-real-key"}})
	if _, err := e.pool.Exec(ctx, `DELETE FROM jobs WHERE kind = 'seo.sync'`); err != nil {
		t.Fatal(err)
	}

	news := rows["news.example"]["id"].(string)
	out := owner.must(202, "POST", root+"/ahrefs-check", map[string]any{"ids": []string{news}})
	if out["checks"] != 1.0 || out["estimated_units"] != float64(linkorders.AhrefsCheckUnits) {
		t.Fatalf("queue: %v", out)
	}
	if again := owner.must(202, "POST", root+"/ahrefs-check", map[string]any{"ids": []string{news}}); again["checks"] != 0.0 {
		t.Fatalf("again today: %v", again)
	}
	before := calls.Load()
	if err := (&linkorders.Checker{Pool: e.pool, Store: e.integrations}).CheckOne(ctx, news, ""); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load() - before; n != 5 {
		t.Fatalf("Ahrefs requests: %d", n)
	}
	var got map[string]any
	for _, d := range owner.must(200, "GET", root+"?q=news.example", nil)["data"].([]any) {
		got = d.(map[string]any)
	}
	m := got["metrics"].(map[string]any)
	if got["domain_rating"] != 58.0 || got["traffic"] != 20000.0 || got["metrics_source"] != "ahrefs" || m["referring_domains"] != 1500.0 ||
		m["linked_domains_live"] != 150.0 || m["top_page_url"] != "https://news.example/" || m["top3_traffic"] != 13000.0 || m["home_traffic"] != 10000.0 {
		t.Fatalf("stored: %v", got)
	}
	if c := got["ahrefs_check"].(map[string]any); c["status"] != "checked" {
		t.Fatalf("check: %v", c)
	}
	// Half the traffic on the home page: skip, with the reason.
	q := got["quality"].(map[string]any)
	if q["verdict"] != "skip" || q["home_share"] != 0.5 || q["grade"] != "premium" {
		t.Fatalf("quality after the check: %v", q)
	}
	// Checked: asking again within 30 days uses the stored numbers and
	// spends nothing.
	if again := owner.must(202, "POST", root+"/ahrefs-check", map[string]any{"ids": []string{news}}); again["checks"] != 0.0 || again["fresh"] != 1.0 || again["estimated_units"] != 0.0 {
		t.Fatalf("again within 30 days: %v", again)
	}
	// Older than 30 days, it is checked again.
	if _, err := e.pool.Exec(ctx, `UPDATE coverage_donor_profiles SET ahrefs_checked_at = now() - interval '31 days' WHERE id = $1`, news); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `DELETE FROM jobs WHERE kind = $1`, linkorders.JobAhrefsCheck); err != nil {
		t.Fatal(err)
	}
	if stale := owner.must(202, "POST", root+"/ahrefs-check", map[string]any{"ids": []string{news}}); stale["checks"] != 1.0 || stale["fresh"] != 0.0 {
		t.Fatalf("after 31 days: %v", stale)
	}
	// The spend is booked against the connection.
	var spent int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM spend_entries WHERE workspace_id = $1 AND domain = 'news.example'`, ws).Scan(&spent); err != nil || spent != 1 {
		t.Fatalf("spend entries: %d %v", spent, err)
	}
}
