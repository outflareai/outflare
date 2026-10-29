package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// fakeNewsSearch answers Geonode's /v1/search with the same results for
// every query, and keeps what it was asked.
type fakeNewsSearch struct {
	mu      sync.Mutex
	asked   []map[string]any
	results []map[string]any
	status  int
}

func (f *fakeNewsSearch) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/usage/concurrency" {
		json.NewEncoder(w).Encode(map[string]any{"work_concurrency_in_use": 0, "work_concurrency_limit": 5}) //nolint:errcheck
		return
	}
	if r.URL.Path != "/v1/search" {
		http.NotFound(w, r)
		return
	}
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
	f.mu.Lock()
	f.asked = append(f.asked, body)
	status := f.status
	f.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "WORK_CONCURRENCY_LIMITED"}}) //nolint:errcheck
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"engine": "google", "results": f.results, "tokens_charged": 2}) //nolint:errcheck
}

func TestNewsWatchFindsArticlesAndCoverage(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	fake := &fakeNewsSearch{results: []map[string]any{
		{"title": "Northpeak raises a seed round", "url": "https://news.example.org/2026/10/northpeak?utm_source=feed", "snippet": "The proxy firm..."},
		{"title": "Proxies explained", "url": "https://blog.example.net/proxies", "snippet": "Northpeak and others sell them."},
		{"title": "Northpeak's own blog", "url": "https://blog.northpeak.example/post", "snippet": "Northpeak news"},
		{"title": "Unrelated", "url": "https://other.example.com/x", "snippet": "nothing about it"},
		{"title": "Same article again", "url": "https://news.example.org/2026/10/northpeak#comments", "snippet": "Northpeak"},
	}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	e.integrations.Client.BaseURLs["geonode"] = srv.URL

	bid := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)

	// Checks on what a search takes.
	owner.must(400, "POST", base+"/news-searches", map[string]any{"query": "Northpeak", "purpose": "coverage"})
	owner.must(400, "POST", base+"/news-searches", map[string]any{"query": "", "purpose": "topic"})
	owner.must(400, "POST", base+"/news-searches", map[string]any{"query": "proxies", "check_every_hours": 1})
	owner.must(400, "POST", base+"/news-searches", map[string]any{"query": "proxies", "time_range": "year"})

	cov := owner.must(201, "POST", base+"/news-searches", map[string]any{"query": "Northpeak", "purpose": "coverage", "brand_id": "northpeak"})
	if cov["brand_id"] != bid || cov["name"] != "Northpeak" || cov["time_range"] != "week" || cov["check_every_hours"] != 24.0 || cov["status"] != "active" {
		t.Fatalf("created: %v", cov)
	}
	covID := cov["id"].(string)
	topic := owner.must(201, "POST", base+"/news-searches", map[string]any{"name": "Proxy news", "query": "residential proxies", "time_range": "day"})
	topicID := topic["id"].(string)

	// Without a web search connection a run can't start, and a scheduled
	// one says why.
	owner.must(409, "POST", base+"/news-searches/"+covID+"/run", nil)
	if err := e.srv.RunNewsSearch(ctx, ws, covID); err != nil {
		t.Fatal(err)
	}
	if s := owner.must(200, "GET", base+"/news-searches/"+covID, nil); s["last_error_code"] != "connector_needed" {
		t.Fatalf("no connection: %v", s)
	}

	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "geonode", "credentials": map[string]string{"api_key": "gn-key"}})
	owner.must(202, "POST", base+"/news-searches/"+covID+"/run", nil)
	if err := e.srv.RunNewsSearch(ctx, ws, covID); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.RunNewsSearch(ctx, ws, topicID); err != nil {
		t.Fatal(err)
	}
	if len(fake.asked) != 2 || fake.asked[0]["query"] != "Northpeak" || fake.asked[0]["time_range"] != "week" || fake.asked[1]["time_range"] != "day" {
		t.Fatalf("asked: %v", fake.asked)
	}

	// Four articles: the duplicate is kept once, without its tracking tags.
	arts := owner.must(200, "GET", base+"/news-articles", nil)
	if arts["total"] != 4.0 {
		t.Fatalf("articles: %v", arts)
	}
	if n := owner.must(200, "GET", base+"/news-articles?search_id="+topicID, nil)["total"]; n != 4.0 {
		t.Fatalf("topic articles: %v", n)
	}
	// Coverage: the two articles that name the brand, not its own blog or
	// the unrelated page.
	list := owner.must(200, "GET", base+"/coverage", nil)
	data := list["data"].([]any)
	if list["total"] != 2.0 {
		t.Fatalf("coverage: %v", list)
	}
	urls := map[string]map[string]any{}
	for _, c := range data {
		c := c.(map[string]any)
		urls[c["url"].(string)] = c
	}
	first := urls["https://news.example.org/2026/10/northpeak"]
	if first == nil || first["domain"] != "news.example.org" || first["status"] != "new" || first["source"] != "news_watch" ||
		first["search_id"] != covID || first["linked"] != nil || first["brand_id"] != bid || first["record_id"] == "" {
		t.Fatalf("coverage item: %v", urls)
	}
	var recordType string
	if err := e.pool.QueryRow(ctx, `SELECT record_type FROM records WHERE id = $1`, first["record_id"]).Scan(&recordType); err != nil || recordType != "account" {
		t.Fatalf("outlet record: %v %v", recordType, err)
	}
	// The search is booked in the spend ledger against the connection.
	var tokens float64
	if err := e.pool.QueryRow(ctx, `SELECT coalesce(sum(units), 0) FROM spend_entries WHERE workspace_id = $1 AND note LIKE 'News search%'`, ws).Scan(&tokens); err != nil || tokens != 4 {
		t.Fatalf("booked tokens: %v %v", tokens, err)
	}

	// Running again finds nothing new.
	if err := e.srv.RunNewsSearch(ctx, ws, covID); err != nil {
		t.Fatal(err)
	}
	if n := owner.must(200, "GET", base+"/coverage", nil)["total"]; n != 2.0 {
		t.Fatalf("coverage after a second run: %v", n)
	}

	// Confirm one, dismiss the other; dismissed items leave the default list.
	id := first["id"].(string)
	if c := owner.must(200, "PATCH", base+"/coverage/"+id, map[string]any{"status": "confirmed", "linked": false}); c["status"] != "confirmed" || c["linked"] != false {
		t.Fatalf("confirmed: %v", c)
	}
	other := urls["https://blog.example.net/proxies"]["id"].(string)
	owner.must(200, "PATCH", base+"/coverage/"+other, map[string]any{"status": "dismissed"})
	if n := owner.must(200, "GET", base+"/coverage", nil)["total"]; n != 1.0 {
		t.Fatalf("after dismissing: %v", n)
	}
	if n := owner.must(200, "GET", base+"/coverage?status=dismissed", nil)["total"]; n != 1.0 {
		t.Fatalf("dismissed: %v", n)
	}
	owner.must(400, "PATCH", base+"/coverage/"+id, map[string]any{"status": "great"})

	// A person adds an article the watch missed; one on the brand's own
	// site is refused.
	added := owner.must(200, "POST", base+"/coverage", map[string]any{"brand_id": bid, "url": "https://paper.example.com/story", "title": "Northpeak in print"})
	if added["source"] != "manual" || added["title"] != "Northpeak in print" || added["status"] != "new" {
		t.Fatalf("added: %v", added)
	}
	owner.must(400, "POST", base+"/coverage", map[string]any{"brand_id": bid, "url": "https://northpeak.example/press"})

	// A failed run is kept on the search, and retried when the provider
	// was busy.
	fake.status = http.StatusTooManyRequests
	if _, err := e.pool.Exec(ctx, `UPDATE news_searches SET next_check_at = now() + interval '6 hours' WHERE id = $1`, topicID); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.RunNewsSearch(ctx, ws, topicID); err == nil {
		t.Fatal("a busy provider is retried")
	}
	if s := owner.must(200, "GET", base+"/news-searches/"+topicID, nil); s["last_error_code"] != "connector_quota" {
		t.Fatalf("failed run: %v", s)
	}
	// It is checked again in 15 minutes, not at its next interval.
	var failures, wait int
	if err := e.pool.QueryRow(ctx, `SELECT failures, EXTRACT(epoch FROM next_check_at - now())::int FROM news_searches WHERE id = $1`, topicID).
		Scan(&failures, &wait); err != nil || failures != 1 || wait > 15*60 || wait < 14*60 {
		t.Fatalf("retry: failures %d, next check in %ds (%v)", failures, wait, err)
	}

	// Pausing and changing the schedule; deleting a search keeps coverage.
	if s := owner.must(200, "PATCH", base+"/news-searches/"+topicID, map[string]any{"status": "paused", "check_every_hours": 12}); s["status"] != "paused" ||
		s["check_every_hours"] != 12.0 {
		t.Fatalf("updated: %v", s)
	}
	owner.must(204, "DELETE", base+"/news-searches/"+covID, nil)
	if n := owner.must(200, "GET", base+"/coverage?status=new,confirmed,dismissed", nil)["total"]; n != 3.0 {
		t.Fatalf("coverage after deleting its search: %v", n)
	}
	if list := owner.must(200, "GET", base+"/news-searches", nil)["data"].([]any); len(list) != 1 {
		t.Fatalf("searches: %v", list)
	}

	names := map[string]int{}
	for _, ev := range got() {
		names[ev.Name]++
	}
	if names["mention.found"] != 3 || names["news_search.ran"] != 3 || names["news_search.run_failed"] != 2 || names["news_search.created"] != 2 ||
		names["coverage.confirmed"] != 1 || names["coverage.dismissed"] != 1 || names["news_search.deleted"] != 1 {
		t.Fatalf("events: %v", names)
	}
}

func TestNewsSearchesKeepToTheirBrand(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "handle": "acme", "website": "https://acme.example"})
	owner.must(201, "POST", base+"/news-searches", map[string]any{"query": "Northpeak", "purpose": "coverage", "brand_id": "northpeak"})
	owner.must(201, "POST", base+"/news-searches", map[string]any{"query": "Acme", "purpose": "coverage", "brand_id": "acme"})
	owner.must(201, "POST", base+"/news-searches", map[string]any{"query": "proxies"})
	if n := len(owner.must(200, "GET", base+"/news-searches?brand=acme", nil)["data"].([]any)); n != 1 {
		t.Fatalf("acme's searches: %d", n)
	}
	if n := len(owner.must(200, "GET", base+"/news-searches?brand=none", nil)["data"].([]any)); n != 1 {
		t.Fatalf("shared searches: %d", n)
	}
	if n := len(owner.must(200, "GET", base+"/news-searches?purpose=coverage", nil)["data"].([]any)); n != 2 {
		t.Fatalf("coverage searches: %d", n)
	}
}
