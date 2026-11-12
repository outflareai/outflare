package api_test

import (
	"context"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// New event names are usable as they arrive; the name check asks only
// when one looks like a typo of a name the brand already has, and the
// answer merges it or keeps both.
func TestEventNameCheck(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	brand := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	catalog := "/v1/brands/" + brand + "/catalog/events/"
	send := func(names ...string) {
		t.Helper()
		var batch []map[string]any
		for _, n := range names {
			batch = append(batch, map[string]any{"type": "track", "userId": "u1", "event": n})
		}
		owner.must(200, "POST", base+"/customer-events", map[string]any{"batch": batch})
	}
	check := func() {
		t.Helper()
		if more, err := e.srv.CheckEventNames(ctx, ws, brand); err != nil || more {
			t.Fatalf("check: more %v, %v", more, err)
		}
	}
	def := func(name string) map[string]any {
		t.Helper()
		for _, d := range owner.must(200, "GET", "/v1/brands/"+brand+"/catalog/events", nil)["data"].([]any) {
			if d := d.(map[string]any); d["name"] == name {
				return d
			}
		}
		t.Fatalf("%s isn't in the catalog", name)
		return nil
	}
	count := func(name string) int {
		t.Helper()
		return len(owner.must(200, "GET", base+"/customer-events?name="+name, nil)["data"].([]any))
	}

	// A first name has nothing to look like. Sending queues the check.
	send("Signed Up", "Signed Up", "Order Completed", "Step 1")
	var queued int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'event_names.check' AND workspace_id = $1`, ws).Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("queued checks: %d, %v", queued, err)
	}
	check()
	if d := def("signed_up"); d["status"] != "approved" || d["name_check"] != "ok" || d["looks_like"] != nil {
		t.Fatalf("signed_up: %v", d)
	}

	// No AI connected: compared letter by letter. A typo is asked about;
	// a related but different event and another step are not.
	send("Sign Up", "Signed In", "Step 2", "Order Complete")
	if count("sign_up") != 1 {
		t.Fatal("a new name's event isn't stored straight away")
	}
	check()
	if d := def("sign_up"); d["status"] != "approved" || d["name_check"] != "asked" || d["looks_like"] != "signed_up" || d["check_reason"] != "two letters apart" {
		t.Fatalf("sign_up: %v", d)
	}
	for _, n := range []string{"signed_in", "step_2"} {
		if d := def(n); d["name_check"] != "ok" {
			t.Fatalf("%s: %v", n, d)
		}
	}
	if d := def("order_complete"); d["name_check"] != "asked" || d["looks_like"] != "order_completed" {
		t.Fatalf("order_complete: %v", d)
	}

	// The question shows on the Events page and in the status.
	checks := owner.must(200, "GET", "/v1/brands/"+brand+"/events/summary", nil)["name_checks"].([]any)
	if len(checks) != 2 {
		t.Fatalf("name checks: %v", checks)
	}
	blocked := false
	for _, b := range owner.must(200, "GET", base+"/status", nil)["blockers"].([]any) {
		if b := b.(map[string]any); b["code"] == "event_names_to_check" && b["count"] == 2.0 {
			blocked = true
		}
	}
	if !blocked {
		t.Fatal("the status doesn't ask about the names")
	}

	// No: both stay.
	owner.must(400, "POST", catalog+"order_complete/name-check", map[string]any{})
	if d := owner.must(200, "POST", catalog+"order_complete/name-check", map[string]any{"same": false}); d["name_check"] != "ok" || d["looks_like"] != nil || d["status"] != "approved" {
		t.Fatalf("kept: %v", d)
	}
	owner.must(409, "POST", catalog+"order_complete/name-check", map[string]any{"same": false})
	owner.must(409, "POST", catalog+"signed_up/name-check", map[string]any{"same": true})
	owner.must(404, "POST", catalog+"never_sent/name-check", map[string]any{"same": true})

	// Yes: its events move to the name it looks like, and later ones are
	// stored there too.
	if d := owner.must(200, "POST", catalog+"sign_up/name-check", map[string]any{"same": true}); d["status"] != "deprecated" || d["same_as"] != "signed_up" || d["name_check"] != "ok" {
		t.Fatalf("merged: %v", d)
	}
	if count("sign_up") != 0 || count("signed_up") != 3 {
		t.Fatalf("after the merge: sign_up %d, signed_up %d", count("sign_up"), count("signed_up"))
	}
	send("Sign Up")
	if count("sign_up") != 0 || count("signed_up") != 4 {
		t.Fatalf("sent after the merge: sign_up %d, signed_up %d", count("sign_up"), count("signed_up"))
	}
	if len(owner.must(200, "GET", "/v1/brands/"+brand+"/events/summary", nil)["name_checks"].([]any)) != 0 {
		t.Fatal("answered questions still show")
	}

	// With the workspace's own AI model, its answer counts: it can see a
	// duplicate no spelling shows, and clear one the spelling would ask.
	connectFakeOpenAI(t, e, owner, ws)
	send("Checkout Finished", "Order Completd")
	check()
	if d := def("checkout_finished"); d["name_check"] != "asked" || d["looks_like"] != "order_completed" || d["check_reason"] != "same event, other words" {
		t.Fatalf("checkout_finished: %v", d)
	}
	if d := def("order_completd"); d["name_check"] != "ok" {
		t.Fatalf("order_completd: %v", d)
	}

	methods := map[string]int{}
	for _, ev := range named(got(), "event_names.checked") {
		methods[ev.Props["method"].(string)]++
	}
	if methods["spelling"] != 2 || methods["ai"] != 1 {
		t.Fatalf("event_names.checked: %v", methods)
	}
	if len(named(got(), "event_definition.merged")) != 1 || len(named(got(), "event_definition.name_kept")) != 1 {
		t.Fatal("the answers aren't events")
	}
}
