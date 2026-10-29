package api_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/seosync"
)

// An SEO site's weekly competitor read that failed is kept, tried again
// soon by a job of its own, and cleared once a read works.
func TestFailedSiteReadsRetrySoon(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	t.Cleanup(func() { dfsRivalsDown.Store(false); setDFSRivals() })

	id := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})["id"].(string)
	owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.example.com/"})
	dfsRivalsDown.Store(true)
	runs := owner.must(200, "POST", base+"/seo/"+id+"/sync", nil)["competitors"].([]any)
	if len(runs) != 1 || runs[0].(map[string]any)["error"] == nil {
		t.Fatalf("competitors: %v", runs)
	}
	failed := func() (n int) {
		t.Helper()
		e.pool.QueryRow(ctx, `SELECT COALESCE(sum(failures), 0) FROM seo_site_read_failures WHERE workspace_id = $1 AND kind = 'competitors'`, ws).Scan(&n)
		return n
	}
	if n := failed(); n != 1 {
		t.Fatalf("failures kept: %d", n)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	seo := &seosync.Syncer{Pool: e.pool, Store: e.integrations, Log: log}
	retries := func() (n int) {
		t.Helper()
		if err := seo.QueueDue(ctx); err != nil {
			t.Fatal(err)
		}
		e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE workspace_id = $1 AND idempotency_key LIKE '%:site_reads:%'`, ws).Scan(&n)
		return n
	}
	if n := retries(); n != 0 {
		t.Fatalf("retried before the first wait: %d", n)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE seo_site_read_failures SET last_failed_at = now() - interval '20 minutes' WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	if n := retries(); n != 1 {
		t.Fatalf("retries: %d", n)
	}
	if n := retries(); n != 1 {
		t.Fatalf("retried twice: %d", n)
	}

	// The provider is back: the retry reads it and clears the failure.
	dfsRivalsDown.Store(false)
	setDFSRivals(map[string]any{"domain": "rival.example", "intersections": 120})
	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	seo.Register(q)
	qctx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(qctx, 1); close(done) }() //nolint:errcheck
	defer func() { stop(); <-done }()
	deadline := time.Now().Add(10 * time.Second)
	for failed() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the failed site read was not tried again")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n := owner.must(200, "GET", base+"/seo-competitors", nil)["total"]; n != 1.0 {
		t.Fatalf("suggested after the retry: %v", n)
	}
}

// A competitor whose weekly look failed for a reason that can pass is
// looked at again soon, not a week later.
func TestFailedGlancesRetrySoon(t *testing.T) {
	e := freshEnv(t)
	_, ws := setupOwner(t, e.h)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	var site, rival string
	if err := e.pool.QueryRow(ctx, `INSERT INTO seo_sites (workspace_id, host, start_url, verify_token) VALUES ($1, 'acme.example', 'https://acme.example/', 'tok') RETURNING id`, ws).Scan(&site); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO seo_competitors (workspace_id, site_id, host, status, checked_at) VALUES ($1, $2, 'rival.example', 'tracked', now() - interval '20 minutes') RETURNING id`,
		ws, site).Scan(&rival); err != nil {
		t.Fatal(err)
	}
	glances := func() (n int) {
		t.Helper()
		if err := e.crawl.ScheduleGlances(ctx); err != nil {
			t.Fatal(err)
		}
		e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'crawl.glance' AND workspace_id = $1`, ws).Scan(&n)
		return n
	}
	exec(`DELETE FROM jobs WHERE kind = 'crawl.glance'`)
	if n := glances(); n != 0 {
		t.Fatalf("a healthy look was repeated: %d", n)
	}
	exec(`UPDATE seo_competitors SET check_failures = 1, check_error = 'crawl_failed' WHERE id = $1`, rival)
	if n := glances(); n != 1 {
		t.Fatalf("retries after a failure: %d", n)
	}
	// After a second failure the next look waits an hour.
	exec(`DELETE FROM jobs WHERE kind = 'crawl.glance'`)
	exec(`UPDATE seo_competitors SET check_failures = 2 WHERE id = $1`, rival)
	if n := glances(); n != 0 {
		t.Fatalf("retried before the second wait: %d", n)
	}
}

// A lead list whose scheduled refresh failed is refreshed again soon,
// without moving its next scheduled refresh, and stops after a few tries.
func TestFailedListRefreshRetriesSoon(t *testing.T) {
	e := freshListEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	apollo := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "apollo", "name": "Apollo", "credentials": map[string]string{"api_key": "k"}})
	list := owner.must(201, "POST", base+"/lead-lists", map[string]any{"name": "Weekly", "search_integration_id": apollo["id"],
		"filters": map[string]any{"titles": []string{"sales"}}, "refresh_every_days": 7, "build": false})["id"].(string)
	exec(`DELETE FROM list_runs WHERE list_id = $1`, list)
	exec(`UPDATE lead_lists SET next_refresh_at = now() + interval '6 days' WHERE id = $1`, list)
	failRun := func(ago string) {
		t.Helper()
		exec(`INSERT INTO list_runs (workspace_id, list_id, kind, status, created_at, finished_at) VALUES ($1, $2, 'refresh', 'failed', now() - $3::interval, now() - $3::interval)`, ws, list, ago)
	}
	queued := func() (n int) {
		t.Helper()
		if err := e.lists.RefreshDue(ctx); err != nil {
			t.Fatal(err)
		}
		e.pool.QueryRow(ctx, `SELECT count(*) FROM list_runs WHERE list_id = $1 AND status = 'queued'`, list).Scan(&n)
		return n
	}
	if n := queued(); n != 0 {
		t.Fatalf("a list that never failed was refreshed early: %d", n)
	}
	failRun("20 minutes")
	if n := queued(); n != 1 {
		t.Fatalf("retries after a failure: %d", n)
	}
	var later bool
	e.pool.QueryRow(ctx, `SELECT next_refresh_at > now() + interval '5 days' FROM lead_lists WHERE id = $1`, list).Scan(&later)
	if !later {
		t.Fatal("a retry moved the next scheduled refresh")
	}

	// That retry failed too: the next waits an hour.
	exec(`UPDATE list_runs SET status = 'failed', created_at = now() - interval '19 minutes', finished_at = now() - interval '19 minutes' WHERE list_id = $1 AND status = 'queued'`, list)
	if n := queued(); n != 0 {
		t.Fatalf("retried before the second wait: %d", n)
	}
	// After refreshRetries failures in a row it waits for its schedule.
	for range 3 {
		failRun("10 minutes")
	}
	exec(`UPDATE list_runs SET finished_at = now() - interval '5 hours' WHERE list_id = $1`, list)
	if n := queued(); n != 0 {
		t.Fatalf("retried after five failures: %d", n)
	}
}
