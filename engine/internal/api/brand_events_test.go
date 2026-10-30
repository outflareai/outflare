package api_test

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Events, kept (plans/brands/README.md): a brand's events are saved first
// and answered with receipts, then processed by a job; each keeps its
// status and what to fix, and rejected and held ones can be replayed.
func TestBrandEvents(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	acme := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	rp := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "website": "repocket.com"})["id"].(string)
	events := "/v1/brands/" + acme + "/events"

	// Sending answers at once with a receipt per event, before any job
	// runs: nothing is refused for its content.
	sent := owner.must(202, "POST", events, map[string]any{"source": "segment", "context": map[string]any{"app": map[string]any{"version": "2.1"}}, "batch": []any{
		map[string]any{"type": "identify", "userId": "u1", "messageId": "m1", "traits": map[string]any{"email": "ada@example.com", "email_verified": true, "firstName": "Ada"}},
		map[string]any{"type": "track", "userId": "u1", "event": "Order Completed", "messageId": "m2", "properties": map[string]any{"total": 12}},
		map[string]any{"type": "track", "event": "No One", "messageId": "m3"},
		map[string]any{"type": "page", "anonymousId": "anon-1", "name": "Pricing", "messageId": "m4"},
		map[string]any{"type": "launch", "userId": "u2", "messageId": "m6"},
		map[string]any{"type": "identify", "userId": "u1", "messageId": "m1"},
	}})
	batch := sent["batch_id"].(string)
	receipts := sent["events"].([]any)
	if sent["received"] != 6.0 || len(receipts) != 6 || receipts[4].(map[string]any)["index"] != 4.0 {
		t.Fatalf("receipts: %v", sent)
	}
	id := func(i int) string { return receipts[i].(map[string]any)["id"].(string) }
	if ev := owner.must(200, "GET", events+"/"+id(0), nil); ev["status"] != "pending" || ev["batch_id"] != batch ||
		ev["raw"].(map[string]any)["context"].(map[string]any)["app"] == nil {
		t.Fatalf("pending with the batch's context merged: %v", ev)
	}
	// Only the envelope can be refused.
	owner.must(400, "POST", events, map[string]any{"batch": []any{}})
	owner.must(400, "POST", events, map[string]any{"type": "track", "source": "Not A Label"})
	owner.must(400, "POST", events, map[string]any{"type": "track", "source": "browser"})

	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 20 * time.Millisecond
	e.srv.RegisterJobs(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	t.Cleanup(func() { cancel(); <-done })

	settled := func(path string, ids ...string) []map[string]any {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for {
			var out []map[string]any
			waiting := false
			for _, id := range ids {
				ev := owner.must(200, "GET", path+"/"+id, nil)
				waiting = waiting || ev["status"] == "pending"
				out = append(out, ev)
			}
			if !waiting {
				return out
			}
			if time.Now().After(deadline) {
				t.Fatalf("events still pending: %v", out)
			}
			time.Sleep(30 * time.Millisecond)
		}
	}
	got6 := settled(events, id(0), id(1), id(2), id(3), id(4), id(5))
	status := func(ev map[string]any) string { s, _ := ev["code"].(string); return ev["status"].(string) + ":" + s }
	want := []string{"accepted:", "held:name_not_approved", "rejected:missing_id", "accepted:", "rejected:invalid_type", "duplicate:"}
	for i, w := range want {
		if status(got6[i]) != w {
			t.Fatalf("event %d is %s, want %s: %v", i, status(got6[i]), w, got6[i])
		}
	}
	ada := got6[0]["record_id"].(string)
	if got6[0]["customer_event_id"] == nil || got6[0]["name"] != "profile.identified" || got6[1]["name"] != "order_completed" {
		t.Fatalf("accepted: %v / held: %v", got6[0], got6[1])
	}
	if r, _ := got6[2]["reason"].(string); !strings.Contains(r, "userId or anonymousId") {
		t.Fatalf("a rejection says what to fix: %v", got6[2])
	}
	// Accepted rows go after 30 days, rejected and held after 90.
	days := func(ev map[string]any) int {
		rec, _ := time.Parse(time.RFC3339Nano, ev["received_at"].(string))
		exp, _ := time.Parse(time.RFC3339Nano, ev["expires_at"].(string))
		return int(exp.Sub(rec).Hours() / 24)
	}
	if days(got6[0]) != 30 || days(got6[1]) != 90 || days(got6[2]) != 90 || days(got6[5]) != 30 {
		t.Fatalf("retention: %d %d %d %d", days(got6[0]), days(got6[1]), days(got6[2]), days(got6[5]))
	}
	// The person belongs to the route's brand.
	if rec := owner.must(200, "GET", base+"/records/"+ada, nil); rec["brand_id"] != acme || rec["attributes"].(map[string]any)["first_name"] != "Ada" {
		t.Fatalf("person: %v", rec)
	}
	// A held event changed nothing: it isn't a product event yet.
	if l := owner.must(200, "GET", base+"/customer-events?name=order_completed", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("a held event drives nothing: %v", l)
	}

	// Approving the name and replaying the held events processes them.
	owner.must(200, "PATCH", base+"/catalog/events/order_completed", map[string]any{"status": "approved",
		"properties": []map[string]any{{"key": "total", "value_type": "number"}}})
	owner.must(400, "POST", events+"/replay", map[string]any{})
	owner.must(400, "POST", events+"/replay", map[string]any{"status": "accepted"})
	if r := owner.must(202, "POST", events+"/replay", map[string]any{"status": "held"}); r["replayed"] != 1.0 {
		t.Fatalf("replay held: %v", r)
	}
	if ev := settled(events, id(1))[0]; ev["status"] != "accepted" || ev["record_id"] != ada || ev["code"] != nil {
		t.Fatalf("replayed held event: %v", ev)
	}
	if l := owner.must(200, "GET", base+"/customer-events?name=order_completed", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("the replayed event is stored: %v", l)
	}

	// A property of the wrong type is rejected with what to fix; fixing
	// the catalog and replaying it by id accepts it.
	bad := owner.must(202, "POST", events, map[string]any{"type": "track", "userId": "u1", "event": "Order Completed", "messageId": "m5",
		"properties": map[string]any{"total": "12.50"}})["events"].([]any)[0].(map[string]any)["id"].(string)
	ev := settled(events, bad)[0]
	if ev["status"] != "rejected" || ev["code"] != "property_type" || !strings.Contains(ev["reason"].(string), "property total must be a number") {
		t.Fatalf("property type: %v", ev)
	}
	owner.must(200, "PATCH", base+"/catalog/events/order_completed", map[string]any{"properties": []map[string]any{{"key": "total", "value_type": "text"}}})
	if r := owner.must(202, "POST", events+"/replay", map[string]any{"ids": []string{bad, id(0)}}); r["replayed"] != 1.0 || r["skipped"] != 1.0 {
		t.Fatalf("replay by ids: %v", r)
	}
	if ev := settled(events, bad)[0]; ev["status"] != "accepted" {
		t.Fatalf("fixed and replayed: %v", ev)
	}

	// A person of another brand is never touched from this one.
	other := owner.must(202, "POST", "/v1/brands/"+rp+"/events", map[string]any{"type": "identify", "userId": "u1", "traits": map[string]any{"plan": "pro"}})
	oid := other["events"].([]any)[0].(map[string]any)["id"].(string)
	if ev := settled("/v1/brands/"+rp+"/events", oid)[0]; ev["status"] != "rejected" || ev["code"] != "other_brand" {
		t.Fatalf("another brand's person: %v", ev)
	}
	// And a brand's events are only its own.
	owner.must(404, "GET", events+"/"+oid, nil)

	// A token for one brand can't send to or read another's.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Repocket app", "scopes": []string{"customers.manage", "customers.read"}, "brand": "repocket"})
	app := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	app.must(404, "POST", events, map[string]any{"type": "track", "userId": "u9", "event": "Signed Up"})
	app.must(404, "GET", events, nil)
	app.must(202, "POST", "/v1/brands/"+rp+"/events", map[string]any{"type": "page", "anonymousId": "anon-9"})

	// Lists filter by status, code, source and batch.
	count := func(query string) int {
		t.Helper()
		return len(owner.must(200, "GET", events+"?"+query, nil)["data"].([]any))
	}
	if n := count(""); n != 7 {
		t.Fatalf("all: %d", n)
	}
	if count("status=rejected") != 2 || count("code=missing_id") != 1 || count("status=accepted") != 4 || count("source=segment") != 6 ||
		count("batch_id="+batch) != 6 || count("status=held") != 0 || count("source=api&status=accepted") != 1 {
		t.Fatalf("filters: rejected %d, missing_id %d, accepted %d, segment %d", count("status=rejected"), count("code=missing_id"), count("status=accepted"), count("source=segment"))
	}
	owner.must(400, "GET", events+"?status=lost", nil)
	page := owner.must(200, "GET", events+"?limit=4", nil)
	if len(page["data"].([]any)) != 4 || page["next_cursor"] == nil {
		t.Fatalf("page: %v", page)
	}
	if rest := owner.must(200, "GET", events+"?limit=4&cursor="+page["next_cursor"].(string), nil)["data"].([]any); len(rest) != 3 {
		t.Fatalf("second page: %d", len(rest))
	}

	// The summary counts them and lists the rejections still kept.
	sum := owner.must(200, "GET", events+"/summary", nil)
	day := sum["last_24h"].(map[string]any)
	if day["accepted"] != 4.0 || day["rejected"] != 2.0 || day["duplicate"] != 1.0 || day["held"] != 0.0 || sum["pending"] != 0.0 {
		t.Fatalf("summary: %v", sum)
	}
	codes := sum["rejection_codes"].([]any)
	if len(codes) != 2 || codes[0].(map[string]any)["expires_at"] == nil {
		t.Fatalf("codes: %v", codes)
	}

	// The sweep deletes only rows past their retention.
	if _, err := e.pool.Exec(ctx, `UPDATE event_inbox SET purge_after = now() - interval '1 minute' WHERE id = $1`, id(2)); err != nil {
		t.Fatal(err)
	}
	if n, err := e.srv.PruneEventInbox(ctx); err != nil || n != 1 {
		t.Fatalf("purged %d: %v", n, err)
	}
	owner.must(404, "GET", events+"/"+id(2), nil)
	if n := count(""); n != 6 {
		t.Fatalf("after the purge: %d", n)
	}

	// Erasing the person takes their events from the inbox too, even the
	// rejected ones that never got a record, but not another brand's.
	wrong := owner.must(202, "POST", events, map[string]any{"type": "track", "userId": "u1", "event": "Order Completed",
		"properties": map[string]any{"total": 5}})["events"].([]any)[0].(map[string]any)["id"].(string)
	if ev := settled(events, wrong)[0]; ev["status"] != "rejected" || ev["record_id"] != nil {
		t.Fatalf("rejected without a record: %v", ev)
	}
	rec := owner.must(200, "GET", base+"/records/"+ada, nil)
	if out := owner.must(200, "DELETE", base+"/records/"+ada+"?version="+strconv.Itoa(int(rec["version"].(float64))), nil); out["erased"].(map[string]any)["event_inbox"] == nil {
		t.Fatalf("erased: %v", out)
	}
	owner.must(404, "GET", events+"/"+wrong, nil)
	owner.must(404, "GET", events+"/"+id(0), nil)
	owner.must(200, "GET", "/v1/brands/"+rp+"/events/"+oid, nil)

	ingested := named(got(), "customer_events.ingested")
	if len(ingested) < 4 || len(named(got(), "customer_events.replayed")) != 2 {
		t.Fatalf("events: %d ingested, %d replayed", len(ingested), len(named(got(), "customer_events.replayed")))
	}
}
