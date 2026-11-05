package api_test

import (
	"context"
	"testing"

	"github.com/outflareai/outflare/engine/internal/adconv"
)

// A site that sends the same conversion from the browser (site.js, stored
// as "browser:" + its messageId) and from its server with the same
// messageId has it sent to the ad platform once, the server's copy first.
func TestAdConversionsOneCopy(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	if _, err := e.ads.SyncOnce(ctx, ws, i["id"].(string)); err != nil {
		t.Fatal(err)
	}
	acct := owner.must(200, "GET", base+"/ads/accounts", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	rule := owner.must(201, "POST", base+"/ads/conversion-rules", map[string]any{"account_id": acct, "event_name": "signed_up",
		"target_id": "777", "event_type": "CompleteRegistration"})["id"].(string)
	// Events count only once they arrive after the rule was made.
	if _, err := e.pool.Exec(ctx, `UPDATE ad_conversion_rules SET created_at = now() - interval '1 hour' WHERE id = $1`, rule); err != nil {
		t.Fatal(err)
	}

	add := func(msg, age string) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, kind, name, anonymous_id, message_id, occurred_at, received_at)
			VALUES ($1, 'track', 'signed_up', 'anon-1', $2, now() - $3::interval, now() - $3::interval)`, ws, msg, age); err != nil {
			t.Fatal(err)
		}
	}
	add("signup-both", "0 minutes")          // the server's copy
	add("browser:signup-both", "20 minutes") // and the browser's, older
	add("browser:signup-solo", "20 minutes") // only the browser sent it
	add("browser:signup-young", "1 minute")  // still waiting for the server
	add("browser:signup-late", "20 minutes") // sent before the server's copy came

	if n, err := adconv.Match(ctx, e.pool, ws); err != nil || n != 3 {
		t.Fatalf("match: %d %v", n, err)
	}
	// The server's copy of signup-late arrives after its browser copy was
	// queued, and signup-young's after it waited: each is still one send.
	add("signup-late", "0 minutes")
	add("signup-young", "0 minutes")
	if n, err := adconv.Match(ctx, e.pool, ws); err != nil || n != 1 {
		t.Fatalf("second match: %d %v", n, err)
	}

	rows, err := e.pool.Query(ctx, `SELECT x.event_id, e.message_id FROM ad_conversion_sends x JOIN customer_events e ON e.id = x.event_row_id
		WHERE x.rule_id = $1 ORDER BY x.event_id`, rule)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var id, msg string
		if err := rows.Scan(&id, &msg); err != nil {
			t.Fatal(err)
		}
		if _, dup := got[id]; dup {
			t.Fatalf("%s queued twice", id)
		}
		got[id] = msg
	}
	// The platform gets the id without "browser:", so an ad pixel given the
	// same id is counted once too.
	want := map[string]string{"signup-both": "signup-both", "signup-solo": "browser:signup-solo",
		"signup-late": "browser:signup-late", "signup-young": "signup-young"}
	if len(got) != len(want) {
		t.Fatalf("sends: %v", got)
	}
	for id, msg := range want {
		if got[id] != msg {
			t.Fatalf("send %s came from %q, want %q (all: %v)", id, got[id], msg, got)
		}
	}
}

// A browser copy sent without a messageId (the developer forgot it) is
// matched to the server's copy on the event name, the person and the time,
// and Tracking health says the id is missing.
func TestAdConversionsOneCopyWithoutID(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	if _, err := e.ads.SyncOnce(ctx, ws, i["id"].(string)); err != nil {
		t.Fatal(err)
	}
	acct := owner.must(200, "GET", base+"/ads/accounts", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	rule := owner.must(201, "POST", base+"/ads/conversion-rules", map[string]any{"account_id": acct, "event_name": "signed_up",
		"target_id": "777", "event_type": "CompleteRegistration"})["id"].(string)
	if _, err := e.pool.Exec(ctx, `UPDATE ad_conversion_rules SET created_at = now() - interval '1 hour' WHERE id = $1`, rule); err != nil {
		t.Fatal(err)
	}

	add := func(source, anon, msg, at, age string) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, kind, name, anonymous_id, source, message_id, occurred_at, received_at)
			VALUES ($1, 'track', 'signed_up', $2, $3, NULLIF($4, ''), now() - $5::interval, now() - $6::interval)`, ws, anon, source, msg, at, age); err != nil {
			t.Fatal(err)
		}
	}
	add("api", "anon-a", "signup-a", "20 minutes", "0 minutes")                 // the server's copy
	add("browser", "anon-a", "browser:auto-anon-a", "22 minutes", "22 minutes") // the browser's, no id, 2 minutes apart
	add("browser", "anon-b", "browser:auto-anon-b", "20 minutes", "20 minutes") // only the browser sent it
	add("browser", "anon-c", "browser:auto-anon-c", "1 minute", "1 minute")     // still waiting for the server
	add("api", "anon-d", "signup-d", "2 hours", "0 minutes")                    // the server's copy, of one long before
	add("browser", "anon-d", "browser:auto-anon-d", "20 minutes", "20 minutes") // a second sign-up from the same browser, much later

	if n, err := adconv.Match(ctx, e.pool, ws); err != nil || n != 4 {
		t.Fatalf("match: %d %v", n, err)
	}
	// anon-c's server copy comes in time: the browser copy is dropped.
	add("api", "anon-c", "signup-c", "1 minute", "0 minutes")
	if n, err := adconv.Match(ctx, e.pool, ws); err != nil || n != 1 {
		t.Fatalf("second match: %d %v", n, err)
	}
	var browser, server int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE e.source = 'browser'), count(*) FILTER (WHERE e.source <> 'browser')
		FROM ad_conversion_sends x JOIN customer_events e ON e.id = x.event_row_id WHERE x.rule_id = $1`, rule).Scan(&browser, &server); err != nil {
		t.Fatal(err)
	}
	// Sent: signup-a, signup-c, signup-d (server); anon-b and anon-d's later one (browser).
	if browser != 2 || server != 3 {
		t.Fatalf("sends: %d from the browser, %d from the server", browser, server)
	}

	h := owner.must(200, "GET", base+"/ads/tracking-health", nil)
	found := false
	for _, x := range h["issues"].([]any) {
		is := x.(map[string]any)
		if is["code"] == "events_without_shared_id" && is["subject"] == "signed_up" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no warning: %v", h["issues"])
	}
}

// The website and server copies of an order with the same order_id are one
// conversion, even with no shared messageId, a different person on each
// copy, and hours apart.
func TestAdConversionsOneCopyByOrderID(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	if _, err := e.ads.SyncOnce(ctx, ws, i["id"].(string)); err != nil {
		t.Fatal(err)
	}
	acct := owner.must(200, "GET", base+"/ads/accounts", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	rule := owner.must(201, "POST", base+"/ads/conversion-rules", map[string]any{"account_id": acct, "event_name": "order_completed",
		"target_id": "777", "event_type": "Purchase"})["id"].(string)
	if _, err := e.pool.Exec(ctx, `UPDATE ad_conversion_rules SET created_at = now() - interval '1 day' WHERE id = $1`, rule); err != nil {
		t.Fatal(err)
	}
	add := func(source, anon, msg, order, age string) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, kind, name, anonymous_id, source, message_id, properties, occurred_at, received_at)
			VALUES ($1, 'track', 'order_completed', $2, $3, $4, jsonb_build_object('order_id', $5::text), now() - $6::interval, now() - $6::interval)`,
			ws, anon, source, msg, order, age); err != nil {
			t.Fatal(err)
		}
	}
	add("api", "anon-x", "srv-1", "in_1", "0 minutes")               // the server's copy
	add("browser", "anon-y", "browser:auto-1", "in_1", "3 hours")    // the website's, hours before, another browser
	add("browser", "anon-z", "browser:auto-2", "in_2", "20 minutes") // an order only the website sent

	if n, err := adconv.Match(ctx, e.pool, ws); err != nil || n != 2 {
		t.Fatalf("match: %d %v", n, err)
	}
	var fromBrowser int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM ad_conversion_sends x JOIN customer_events e ON e.id = x.event_row_id
		WHERE x.rule_id = $1 AND e.properties->>'order_id' = 'in_1' AND e.source = 'browser'`, rule).Scan(&fromBrowser); err != nil || fromBrowser != 0 {
		t.Fatalf("website copy of in_1 sent: %d %v", fromBrowser, err)
	}
	// Matched on order_id, so Tracking health has nothing to warn about.
	h := owner.must(200, "GET", base+"/ads/tracking-health", nil)
	for _, x := range h["issues"].([]any) {
		if x.(map[string]any)["code"] == "events_without_shared_id" {
			t.Fatalf("warned about an order matched on order_id: %v", x)
		}
	}
}
