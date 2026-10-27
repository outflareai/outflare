package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestProductEventSink(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var user string
	if err := e.pool.QueryRow(ctx, `SELECT id::text FROM users`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	if _, err := e.srv.ProductEventSink(ctx, "00000000-0000-0000-0000-000000000000"); err == nil {
		t.Fatal("a sink for a missing workspace was made")
	}
	sink, err := e.srv.ProductEventSink(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	before := len(got())
	at := time.Now().UTC().Add(-time.Minute)
	sink.Send(ctx, []track.Event{
		{Name: "mailbox.connected", UserID: user, WorkspaceID: "w-1", Surface: "web", Outcome: track.Success, At: at, Props: map[string]any{"provider": "google"}},
		{Name: "api.called", UserID: user, Surface: "mcp", Outcome: track.Failure, ErrorCode: "forbidden", At: at},
		{Name: "job.dead", WorkspaceID: "w-1", Outcome: track.Error, ErrorCode: "internal", At: at},
	})
	if n := len(got()); n != before {
		t.Fatalf("storing product events made %d events of its own", n-before)
	}

	base := "/v1/workspaces/" + ws
	list := owner.must(200, "GET", base+"/customer-events?user_id="+user, nil)["data"].([]any)
	if len(list) != 3 {
		t.Fatalf("events for the person: %v", list)
	}
	var called map[string]any
	for _, v := range list {
		if m := v.(map[string]any); m["name"] == "api.called" {
			called = m
		}
	}
	p := called["properties"].(map[string]any)
	if called["source"] != "outflare" || p["outcome"] != "failure" || p["error_code"] != "forbidden" || p["surface"] != "mcp" || called["record_id"] == nil {
		t.Fatalf("api.called: %v", called)
	}
	// The person is a record with their account's email and name.
	rec := owner.must(200, "GET", base+"/records/"+called["record_id"].(string), nil)
	if rec["name"] != "Jack" {
		t.Fatalf("record: %v", rec)
	}
	ids := owner.must(200, "GET", base+"/records/"+called["record_id"].(string)+"/identities", nil)
	if !hasIdentity(ids, "email", "jack@example.com") {
		t.Fatalf("identities: %v", ids)
	}
	// System work waits unmatched, and Outflare's own names need no approval.
	if l := owner.must(200, "GET", base+"/customer-events?anonymous_id=workspace:w-1", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("system event: %v", l)
	}
	for _, v := range owner.must(200, "GET", base+"/catalog/events", nil)["data"].([]any) {
		if d := v.(map[string]any); d["status"] != "approved" {
			t.Fatalf("catalog: %v", d)
		}
	}

	// A person is identified once per process.
	sink.Send(ctx, []track.Event{{Name: "api.called", UserID: user, At: at}})
	if l := owner.must(200, "GET", base+"/customer-events?kind=identify", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("identify calls: %d", len(l))
	}
}

func hasIdentity(body map[string]any, namespace, value string) bool {
	for _, v := range body["data"].([]any) {
		if m := v.(map[string]any); m["namespace"] == namespace && m["value"] == value {
			return true
		}
	}
	return false
}
