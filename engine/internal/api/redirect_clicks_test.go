package api_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/quota"
)

// Redirect clicks are buffered and written in batches, so a redirect never
// waits on the database (plans/ad-tracker.md, T1).
func TestRedirectClickIngest(t *testing.T) {
	q := &fakeQuota{refuse: map[string]bool{}}
	e := freshEnvWith(t, func(o *api.Options) { o.Quota = q })
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{
		"name": "Winter push", "channel": "paid_ads", "source": "google", "landing_url": "https://northpeak.example/winter", "redirect_links": true,
	})
	link := owner.must(201, "POST", base+"/tracking-campaigns/"+c["code"].(string)+"/links", map[string]any{"content": "search-a"})
	code := regexp.MustCompile(`/l/([a-z0-9]{8})$`).FindStringSubmatch(link["redirect_url"].(string))[1]
	const person = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15"
	follow := func(ip string) (int, string, time.Duration) {
		req := httptest.NewRequest("GET", "/l/"+code, nil)
		req.Header.Set("User-Agent", person)
		req.RemoteAddr = ip + ":40000"
		rec := httptest.NewRecorder()
		start := time.Now()
		e.h.ServeHTTP(rec, req)
		took := time.Since(start)
		loc, _ := url.Parse(rec.Header().Get("Location"))
		key := ""
		if loc != nil {
			key = loc.Query().Get("ofclk")
		}
		return rec.Code, key, took
	}
	count := func() (n int) {
		t.Helper()
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM tracking_link_clicks WHERE workspace_id = $1`, ws).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// A click is answered at once and written by the next flush.
	status, key, _ := follow("192.0.2.10")
	if status != http.StatusFound || key == "" {
		t.Fatalf("redirect = %d, key %q", status, key)
	}
	if n := count(); n != 0 {
		t.Fatalf("written before the flush: %d", n)
	}

	// Its landing page reports it before it is written: the landing waits,
	// and the flush gives the click its visit.
	sk := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "Northpeak", "origins": []string{"https://northpeak.example"}})
	ev := map[string]any{"type": "page", "anonymousId": "early-visitor", "properties": map[string]any{"ofclk": key},
		"context": map[string]any{"page": map[string]any{"url": "https://northpeak.example/winter?ofclk=" + key, "path": "/winter"},
			"campaign": map[string]any{"source": "google", "medium": "cpc", "name": c["slug"]}}}
	if code, out := sendSite(t, e.h, "https://northpeak.example", sk["key"].(string), ev); code != 200 {
		t.Fatalf("site event = %d %v", code, out)
	}
	var waiting int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM tracking_click_claims WHERE click_key = $1`, key).Scan(&waiting); err != nil || waiting != 1 {
		t.Fatalf("waiting landings = %d, %v", waiting, err)
	}
	if err := e.srv.FlushClicks(ctx); err != nil {
		t.Fatal(err)
	}
	var landedBy, hash string
	if err := e.pool.QueryRow(ctx, `SELECT s.anonymous_id, k.ip_hash FROM tracking_link_clicks k JOIN site_sessions s ON s.id = k.session_id
		WHERE k.click_key = $1`, key).Scan(&landedBy, &hash); err != nil || landedBy != "early-visitor" {
		t.Fatalf("landing = %q, %v", landedBy, err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM tracking_click_claims`).Scan(&waiting); err != nil || waiting != 0 {
		t.Fatalf("landings left waiting = %d, %v", waiting, err)
	}
	if l := owner.must(200, "GET", base+"/tracking-campaigns/"+c["code"].(string)+"/links", nil)["data"].([]any)[0].(map[string]any); l["clicks"] != float64(1) || l["landed"] != float64(1) {
		t.Fatalf("link = %v", l)
	}

	// The address is kept as a keyed hash: the same visitor's clicks
	// match, another's don't, and the address isn't in it.
	if len(hash) < 16 || regexp.MustCompile(`192\.0\.2`).MatchString(hash) {
		t.Fatalf("hash = %q", hash)
	}
	follow("192.0.2.10")
	follow("192.0.2.11")
	if err := e.srv.FlushClicks(ctx); err != nil {
		t.Fatal(err)
	}
	var hashes []string
	rows, err := e.pool.Query(ctx, `SELECT ip_hash FROM tracking_link_clicks WHERE workspace_id = $1 ORDER BY id`, ws)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var h string
		rows.Scan(&h)
		hashes = append(hashes, h)
	}
	rows.Close()
	if len(hashes) != 3 || hashes[0] != hashes[1] || hashes[0] == hashes[2] {
		t.Fatalf("hashes = %v", hashes)
	}

	// Over the click quota the redirect still happens; the clicks aren't
	// stored.
	q.set(false, quota.TrackerClicks)
	if status, key, _ := follow("192.0.2.12"); status != http.StatusFound || key == "" {
		t.Fatalf("over quota = %d %q", status, key)
	}
	if err := e.srv.FlushClicks(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 3 || !slices.Contains(q.asked, quota.TrackerClicks) {
		t.Fatalf("over quota: %d clicks, asked %v", n, q.asked)
	}
	q.set(false)

	// Load: 2,000 clicks from 50 visitors at once. Each redirect answers
	// without waiting on the database, and every click is written once.
	const clicks, workers = 2000, 50
	var mu sync.Mutex
	var took []time.Duration
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range clicks / workers {
				status, _, d := follow(fmt.Sprintf("198.51.100.%d", w+1))
				if status != http.StatusFound {
					t.Errorf("redirect = %d", status)
					return
				}
				mu.Lock()
				took = append(took, d)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if err := e.srv.FlushClicks(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 3+clicks {
		t.Fatalf("after load: %d clicks, want %d", n, 3+clicks)
	}
	slices.Sort(took)
	p95 := took[len(took)*95/100]
	t.Logf("redirects: %d, median %v, p95 %v", len(took), took[len(took)/2], p95)
	if p95 > 50*time.Millisecond {
		t.Fatalf("p95 redirect = %v, want under 50ms", p95)
	}
}
