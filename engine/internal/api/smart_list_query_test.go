package api_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/smartlists"
)

// TestQueryListsAndCopies covers the owner's decision of 2026-11-02: a list
// can be built from a saved query (its record_id column, refreshed like any
// list, keeping its members when the query fails), and any list of people
// can be saved as a static copy.
func TestQueryListsAndCopies(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana"})["id"].(string)
	bo := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Bo"})["id"].(string)
	owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Cy"})

	log := slog.New(slog.DiscardHandler)
	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	(&smartlists.Service{Pool: e.pool, Log: log, Query: e.srv.SmartListQueryIDs}).Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	defer func() { cancel(); <-done }()
	get := func(id string) map[string]any { return owner.must(200, "GET", base+"/smart-lists/"+id, nil) }
	wait := func(id string, ok func(l map[string]any) bool) map[string]any {
		t.Helper()
		for range 200 {
			if l := get(id); ok(l) {
				return l
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("list %s never settled: %v", id, get(id))
		return nil
	}

	sq := owner.must(201, "POST", base+"/queries", map[string]any{"name": "A names",
		"sql": "SELECT id AS record_id, name FROM people WHERE name IN ('Ana', 'Bo')"})["id"].(string)
	owner.must(400, "POST", base+"/smart-lists", map[string]any{"name": "No query", "kind": "query"})
	owner.must(400, "POST", base+"/smart-lists", map[string]any{"name": "Bad", "kind": "query", "saved_query_id": sq,
		"rules": []map[string]any{{"attribute": "plan", "op": "eq", "value": "pro"}}})
	owner.must(400, "POST", base+"/smart-lists", map[string]any{"name": "Smart with query", "saved_query_id": sq})
	l := owner.must(201, "POST", base+"/smart-lists", map[string]any{"name": "From the query", "kind": "query", "saved_query_id": sq})
	id := l["id"].(string)
	if l["kind"] != "query" || l["saved_query_id"] != sq {
		t.Fatalf("query list: %v", l)
	}
	wait(id, func(l map[string]any) bool { return l["members"] == 2.0 })
	if h := owner.must(200, "GET", base+"/smart-lists/"+id+"/changes", nil)["data"].([]any); len(h) != 2 {
		t.Fatalf("joins in history: %v", h)
	}

	// The query changes: the list follows it at the next refresh.
	owner.must(200, "PATCH", base+"/queries/"+sq, map[string]any{"sql": "SELECT id FROM people WHERE name = 'Ana'"})
	owner.must(202, "POST", base+"/smart-lists/"+id+"/refresh", nil)
	wait(id, func(l map[string]any) bool { return l["members"] == 1.0 })
	if left := owner.must(200, "GET", base+"/smart-lists/"+id+"/changes?change=leave", nil)["data"].([]any); len(left) != 1 || left[0].(map[string]any)["record_id"] != bo {
		t.Fatalf("leave: %v", left)
	}

	// A query with no record column, then an archived one: the list keeps
	// Ana and says why.
	owner.must(200, "PATCH", base+"/queries/"+sq, map[string]any{"sql": "SELECT name FROM people"})
	owner.must(202, "POST", base+"/smart-lists/"+id+"/refresh", map[string]any{})
	if l := wait(id, func(l map[string]any) bool { return l["error_code"] != nil }); l["error_code"] != "no_record_id" || l["members"] != 1.0 {
		t.Fatalf("no record column: %v", l)
	}
	owner.must(200, "POST", base+"/queries/"+sq+"/archive", nil)
	if _, err := e.pool.Exec(ctx, `UPDATE smart_lists SET error_code = NULL WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := smartlists.Enqueue(ctx, e.pool, ws, id, false); err != nil {
		t.Fatal(err)
	}
	if l := wait(id, func(l map[string]any) bool { return l["error_code"] != nil }); l["error_code"] != "query_missing" || l["members"] != 1.0 {
		t.Fatalf("archived query: %v", l)
	}
	if m := owner.must(200, "GET", base+"/smart-lists/"+id+"/members", nil)["data"].([]any); len(m) != 1 || m[0].(map[string]any)["id"] != ana {
		t.Fatalf("members kept: %v", m)
	}
	owner.must(400, "PATCH", base+"/smart-lists/"+id, map[string]any{"saved_query_id": sq})

	// Save a copy: a static list with the same people, and the history
	// says how they joined.
	c := owner.must(201, "POST", base+"/smart-lists/"+id+"/copy", nil)
	if c["kind"] != "static" || c["members"] != 1.0 || c["name"] == "From the query" {
		t.Fatalf("copy: %v", c)
	}
	named := owner.must(201, "POST", base+"/smart-lists/"+id+"/copy", map[string]any{"name": "Snapshot"})
	if named["name"] != "Snapshot" {
		t.Fatalf("named copy: %v", named)
	}
	owner.must(409, "POST", base+"/smart-lists/"+id+"/copy", map[string]any{"name": "Snapshot"})
	if h := owner.must(200, "GET", base+"/smart-lists/"+c["id"].(string)+"/changes", nil)["data"].([]any); len(h) != 1 || h[0].(map[string]any)["record_id"] != ana {
		t.Fatalf("copy history: %v", h)
	}
	var audited int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'smart_list.copied'`, ws).Scan(&audited); err != nil || audited != 2 {
		t.Fatalf("audit: %d %v", audited, err)
	}
	acc := owner.must(201, "POST", base+"/smart-lists", map[string]any{"name": "Companies", "record_type": "account",
		"rules": []map[string]any{{"attribute": "segment", "op": "eq", "value": "software"}}})
	owner.must(400, "POST", base+"/smart-lists/"+acc["id"].(string)+"/copy", nil)
}
