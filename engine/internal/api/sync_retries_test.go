package api_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/outflareai/outflare/engine/internal/adsync"
	"github.com/outflareai/outflare/engine/internal/analyticsync"
	"github.com/outflareai/outflare/engine/internal/seosync"
)

// A scheduled sync whose last run failed is tried again soon, not after
// its whole interval: analytics and ads every 6 hours, SEO snapshots every
// week. Each retry is a job of its own; a healthy sync waits its interval.
func TestFailedSyncsRetrySoon(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	analytics := &analyticsync.Syncer{Pool: e.pool, Store: e.integrations, Log: log}
	ads := &adsync.Syncer{Pool: e.pool, Store: e.integrations, Log: log}
	seo := &seosync.Syncer{Pool: e.pool, Store: e.integrations, Log: log}

	pl := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})["id"].(string)
	meta := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})["id"].(string)
	dfs := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})["id"].(string)

	// Each ran 20 minutes ago: healthy, so not due again yet.
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`DELETE FROM jobs WHERE workspace_id = $1`, ws)
	exec(`DELETE FROM analytics_syncs WHERE integration_id = $1`, pl)
	exec(`INSERT INTO analytics_syncs (integration_id, workspace_id, site, host, last_run_at, last_ok_at) VALUES ($1, $2, 'example.com', 'example.com', now() - interval '20 minutes', now() - interval '20 minutes')`, pl, ws)
	exec(`INSERT INTO ad_syncs (integration_id, workspace_id, last_run_at, last_ok_at) VALUES ($1, $2, now() - interval '20 minutes', now() - interval '20 minutes')
		ON CONFLICT (integration_id) DO UPDATE SET last_run_at = EXCLUDED.last_run_at, last_error = NULL, failures = 0`, meta, ws)
	exec(`INSERT INTO seo_syncs (integration_id, host, workspace_id, last_run_at, last_ok_at) VALUES ($1, 'example.com', $2, now() - interval '20 minutes', now() - interval '20 minutes')
		ON CONFLICT (integration_id, host) DO UPDATE SET last_run_at = EXCLUDED.last_run_at, last_error = NULL, failures = 0`, dfs, ws)
	queue := func() {
		t.Helper()
		for _, f := range []func(context.Context) error{analytics.QueueDue, ads.QueueDue, seo.QueueDue} {
			if err := f(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	retries := func() map[string]int {
		t.Helper()
		rows, err := e.pool.Query(ctx, `SELECT kind, count(*) FROM jobs WHERE workspace_id = $1 AND (idempotency_key LIKE '%:retry:%' OR idempotency_key LIKE '%:failed:%') GROUP BY 1`, ws)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]int{}
		for rows.Next() {
			var k string
			var n int
			rows.Scan(&k, &n)
			out[k] = n
		}
		return out
	}
	queue()
	if r := retries(); len(r) != 0 {
		t.Fatalf("healthy syncs retried: %v", r)
	}

	// The same runs failed: 15 minutes later each is tried again, once.
	exec(`UPDATE analytics_syncs SET failures = 1, last_error = 'Plausible is down', last_error_code = 'connector_unavailable' WHERE integration_id = $1`, pl)
	exec(`UPDATE ad_syncs SET failures = 1, last_error = 'Meta is down', last_error_code = 'connector_unavailable' WHERE integration_id = $1`, meta)
	exec(`UPDATE seo_syncs SET failures = 1, last_error = 'DataForSEO is down', last_error_code = 'connector_unavailable' WHERE integration_id = $1`, dfs)
	queue()
	queue()
	if r := retries(); r[analyticsync.JobSync] != 1 || r[adsync.JobSync] != 1 || r[seosync.JobSync] != 1 {
		t.Fatalf("retries after a failure: %v", r)
	}

	// A competitor's dead page whose weekly link read failed is read again
	// soon, on its own, not at next week's snapshot; once a read of it
	// works, it isn't.
	deadRetries := func() int {
		t.Helper()
		var n int
		e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE workspace_id = $1 AND idempotency_key LIKE '%:dead_pages:%'`, ws).Scan(&n)
		return n
	}
	exec(`INSERT INTO link_reads (workspace_id, integration_id, connector, target, scope, reason, status, error_code, created_at)
		VALUES ($1, $2, 'dataforseo', 'https://rival.example/gone', 'page', 'dead_page', 'failed', 'provider_error', now() - interval '20 minutes')`, ws, dfs)
	queue()
	queue()
	if n := deadRetries(); n != 1 {
		t.Fatalf("dead page retries: %d", n)
	}
	exec(`INSERT INTO link_reads (workspace_id, integration_id, connector, target, scope, reason, status, error_code, created_at)
		VALUES ($1, $2, 'dataforseo', 'https://rival.example/gone', 'page', 'dead_page', 'failed', 'provider_error', now() - interval '20 minutes')`, ws, dfs)
	queue()
	if n := deadRetries(); n != 1 {
		t.Fatalf("dead page retried before the second wait: %d", n)
	}
	exec(`UPDATE link_reads SET created_at = now() - interval '2 hours' WHERE workspace_id = $1 AND reason = 'dead_page'`, ws)
	exec(`INSERT INTO link_reads (workspace_id, integration_id, connector, target, scope, reason, status) VALUES ($1, $2, 'dataforseo', 'https://rival.example/gone', 'page', 'dead_page', 'done')`, ws, dfs)
	queue()
	if n := deadRetries(); n != 1 {
		t.Fatalf("dead page retried after a read worked: %d", n)
	}

	// After a second failure the next try waits an hour, not 15 minutes.
	exec(`UPDATE analytics_syncs SET failures = 2, last_run_at = now() - interval '20 minutes' WHERE integration_id = $1`, pl)
	queue()
	if r := retries(); r[analyticsync.JobSync] != 1 {
		t.Fatalf("retried before the second wait: %v", r)
	}
}
