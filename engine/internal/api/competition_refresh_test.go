package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/rdap"
	"github.com/outflareai/outflare/engine/internal/seosync"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Refresh all competitors: one click reads every tracked competitor now,
// on the workspace's own SEO connection (only the ones not read today),
// OutflareBot's look with the domain's registration date, and their
// followed profiles. The overview then shows each one's Domain Rating,
// keywords, domain age and pages.
func TestCompetitionRefresh(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Trailcast", "handle": "trailcast"})["id"].(string)
	exec := func(q string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, q+` RETURNING id::text`, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	site := exec(`INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token) VALUES ($1, $2, 'northpeak.example', 'https://northpeak.example/', 'tok')`, ws, brand)
	rival := exec(`INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, name, status) VALUES ($1, $2, $3, 'rival.example', 'Rival', 'tracked')`, ws, site, brand)
	exec(`INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, name, status) VALUES ($1, $2, $3, 'small.example', 'Small', 'tracked')`, ws, site, brand)
	root := "/v1/brands/" + brand + "/competition"

	// Nothing tracked for the other brand: nothing to read.
	owner.must(409, "POST", "/v1/brands/"+other+"/competition/refresh", nil)

	// No SEO connection yet: their sites and profiles are still looked at.
	got := owner.must(202, "POST", root+"/refresh", nil)
	if got["competitors"] != 2.0 || got["seo_integration_id"] != nil || got["page_reader"] != false {
		t.Fatalf("refresh: %v", got)
	}
	seo := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})["id"].(string)
	if _, err := e.pool.Exec(ctx, `DELETE FROM jobs WHERE kind = 'seo.sync'`); err != nil {
		t.Fatal(err)
	}
	got = owner.must(202, "POST", root+"/refresh", nil)
	if got["seo_integration_id"] != seo {
		t.Fatalf("refresh with SEO: %v", got)
	}
	var glances, refreshes int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE kind = 'crawl.glance'), count(*) FILTER (WHERE kind = 'seo.sync' AND payload->>'refresh_brand' = $1)
		FROM jobs WHERE workspace_id = $2`, brand, ws).Scan(&glances, &refreshes); err != nil || glances != 2 || refreshes != 1 {
		t.Fatalf("queued: %d glances, %d refreshes, %v", glances, refreshes, err)
	}

	// The jobs: SEO numbers for both, and both domains' (and the brand's)
	// registration dates.
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/domain/rival.example":
			_, _ = w.Write([]byte(`{"events":[{"eventAction":"registration","eventDate":"2011-05-09T14:00:00Z"}]}`))
		case "/domain/northpeak.example":
			_, _ = w.Write([]byte(`{"events":[{"eventAction":"registration","eventDate":"2019-02-01T00:00:00Z"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(registry.Close)
	e.crawl.RDAP = &rdap.Client{BaseURL: registry.URL}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	(&seosync.Syncer{Pool: e.pool, Store: e.integrations, Log: log}).Register(q)
	e.crawl.Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(15 * time.Second)
	for {
		var left int
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE workspace_id = $1 AND kind IN ('seo.sync', 'crawl.glance') AND status IN ('queued', 'running')`, ws).Scan(&left); err != nil {
			t.Fatal(err)
		}
		if left == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d jobs still waiting", left)
		}
		time.Sleep(25 * time.Millisecond)
	}

	comp := owner.must(200, "GET", root, nil)
	byID := map[string]map[string]any{}
	for _, en := range comp["entries"].([]any) {
		m := en.(map[string]any)
		byID[m["id"].(string)] = m
	}
	r, you := byID[rival], byID["brand"]
	if r["domain_rating"] != 40.0 || r["organic_keywords"] != 1200.0 || r["seo_read_on"] != time.Now().UTC().Format(time.DateOnly) || r["registered_on"] != "2011-05-09" || r["looked_at"] == nil {
		t.Fatalf("rival: %v", r)
	}
	if you["registered_on"] != "2019-02-01" || you["pages"] != nil {
		t.Fatalf("brand: %v", you)
	}
	var small *string
	var checked *time.Time
	if err := e.pool.QueryRow(ctx, `SELECT to_char(registered_on, 'YYYY-MM-DD'), registered_checked_at FROM seo_competitors WHERE host = 'small.example' AND workspace_id = $1`, ws).Scan(&small, &checked); err != nil || small != nil || checked == nil {
		t.Fatalf("a domain the registry doesn't know is checked, with no date: %v %v %v", small, checked, err)
	}

	// Again the same day: nothing is read twice.
	calls := dataForSEOCalls.Load()
	owner.must(202, "POST", root+"/refresh", nil)
	deadline = time.Now().Add(10 * time.Second)
	for {
		var left int
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE workspace_id = $1 AND kind = 'seo.sync' AND status IN ('queued', 'running')`, ws).Scan(&left); err != nil {
			t.Fatal(err)
		}
		if left == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("refresh still waiting")
		}
		time.Sleep(25 * time.Millisecond)
	}
	if dataForSEOCalls.Load() != calls {
		t.Fatalf("read again the same day: %d calls", dataForSEOCalls.Load()-calls)
	}

	var requested, synced bool
	for _, ev := range events() {
		switch ev.Name {
		case "competition.refresh_requested":
			requested = true
		case "seo_competitors.synced":
			synced = synced || ev.Props["refresh"] == true
		}
	}
	if !requested || !synced {
		t.Fatalf("events: requested %v, synced %v", requested, synced)
	}
}
