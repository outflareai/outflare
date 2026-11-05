package api_test

import (
	"context"
	"strings"
	"testing"
)

func TestJobsListAndGet(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `INSERT INTO jobs (workspace_id, kind, status, attempts, last_error) VALUES
		($1, 'test.ok', 'succeeded', 1, NULL),
		($1, 'test.bad', 'dead', 10, 'smtp: 535 bad credentials'),
		($1, 'test.ok', 'queued', 0, NULL),
		(NULL, 'test.global', 'queued', 0, NULL)`, ws); err != nil {
		t.Fatal(err)
	}
	base := "/v1/workspaces/" + ws + "/jobs"
	all := owner.must(200, "GET", base, nil)["data"].([]any)
	// Setup may queue jobs of its own; only test.* matter here, and jobs
	// without a workspace never show.
	var mine []map[string]any
	for _, j := range all {
		m := j.(map[string]any)
		if k := m["kind"].(string); len(k) > 5 && k[:5] == "test." {
			mine = append(mine, m)
		}
		if m["kind"] == "test.global" {
			t.Fatal("a job outside the workspace was listed")
		}
	}
	if len(mine) != 3 || mine[0]["status"] != "queued" || mine[1]["status"] != "failed" || mine[1]["error"] != "smtp: 535 bad credentials" {
		t.Fatalf("jobs %v", mine)
	}
	failed := owner.must(200, "GET", base+"?status=failed", nil)["data"].([]any)
	if len(failed) != 1 || failed[0].(map[string]any)["kind"] != "test.bad" {
		t.Fatalf("failed %v", failed)
	}
	ok := owner.must(200, "GET", base+"?kind=test.ok&limit=1", nil)
	if len(ok["data"].([]any)) != 1 || ok["next_cursor"] == nil {
		t.Fatalf("page %v", ok)
	}
	rest := owner.must(200, "GET", base+"?kind=test.ok&limit=1&cursor="+ok["next_cursor"].(string), nil)["data"].([]any)
	if len(rest) != 1 || rest[0].(map[string]any)["status"] != "succeeded" {
		t.Fatalf("second page %v", rest)
	}
	id := mine[1]["id"].(string)
	if got := owner.must(200, "GET", base+"/"+id, nil); got["kind"] != "test.bad" || got["attempts"] != float64(10) {
		t.Fatalf("get %v", got)
	}
	owner.must(404, "GET", base+"/job_999999999", nil)
	owner.must(404, "GET", base+"/nope", nil)
	owner.must(400, "GET", base+"?status=dead", nil)

	// A 202 that queues one job points at it.
	var cal string
	if err := e.pool.QueryRow(ctx, `INSERT INTO integrations (workspace_id, connector, name, credentials)
		VALUES ($1, 'cal_com', 'Cal.com', '\x00') RETURNING id`, ws).Scan(&cal); err != nil {
		t.Fatal(err)
	}
	queued := owner.must(202, "POST", "/v1/workspaces/"+ws+"/outcomes/syncs/"+cal+"/run", nil)
	ref := queued["job"].(map[string]any)
	if job := owner.must(200, "GET", ref["url"].(string), nil); job["id"] != ref["id"] || job["kind"] == "" {
		t.Fatalf("queued %v, job %v", queued, job)
	}

	// Finished jobs go after 14 days. A key keeps a job, except for the
	// kinds that run once per event or batch of events.
	if _, err := e.pool.Exec(ctx, `INSERT INTO jobs (workspace_id, kind, status, idempotency_key) VALUES
		($1, 'test.keyed', 'succeeded', 'test:1'),
		($1, 'events.process', 'succeeded', 'batch:1'),
		($1, 'metric.event', 'dead', 'metric-event:1'),
		($1, 'events.process', 'queued', 'batch:2')`, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE jobs SET updated_at = now() - interval '15 days'
		WHERE kind LIKE 'test.%' OR idempotency_key IN ('batch:1', 'batch:2', 'metric-event:1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO jobs (workspace_id, kind, status, idempotency_key) VALUES ($1, 'events.process', 'succeeded', 'batch:3')`, ws); err != nil {
		t.Fatal(err)
	}
	n, err := e.srv.PruneJobs(ctx)
	if err != nil || n != 4 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	var left []string
	rows, err := e.pool.Query(ctx, `SELECT idempotency_key FROM jobs WHERE idempotency_key IN ('test:1', 'batch:1', 'metric-event:1', 'batch:2', 'batch:3') ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		left = append(left, k)
	}
	if strings.Join(left, ",") != "batch:2,batch:3,test:1" {
		t.Fatalf("left %v", left)
	}
}
