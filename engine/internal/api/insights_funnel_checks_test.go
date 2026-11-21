package api_test

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// The funnel checks look at each step on its own: who did it at all, how
// it follows the step before with nothing else in the way, and whether
// its data still arrives and links back to the earlier steps.
func TestFunnelChecks(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	now := time.Now().UTC()
	event := func(name, anon string, at time.Time) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, brand_id, anonymous_id, kind, name, occurred_at, properties)
			VALUES ($1, $2, $3, 'track', $4, $5, '{}')`, ws, brand, anon, name, at); err != nil {
			t.Fatal(err)
		}
	}
	// 60 people a day for 20 days land, download and sign up. The
	// installer reports half of them under an id of its own, and stopped
	// reporting altogether three days ago.
	for d := 0; d < 20; d++ {
		for p := 0; p < 60; p++ {
			at := now.Add(-time.Duration(d)*24*time.Hour - time.Duration(p)*time.Minute - time.Hour)
			id := fmt.Sprintf("v-%d-%d", d, p)
			event("landed", id, at)
			event("downloaded", id, at.Add(time.Minute))
			if d >= 3 {
				installer := id
				if p%2 == 0 {
					installer = "device-" + id
				}
				event("installed", installer, at.Add(2*time.Minute))
			}
			event("signed_up", id, at.Add(3*time.Minute))
		}
	}
	steps := func(names ...string) []map[string]any {
		var list []map[string]any
		for _, n := range names {
			list = append(list, map[string]any{"kind": "event", "event": n})
		}
		return list
	}
	body := func(names ...string) map[string]any {
		return map[string]any{"funnel": map[string]any{"date_range": map[string]any{"preset": "last_30_days"}, "steps": steps(names...)}}
	}

	res := owner.must(200, "POST", base+"/insights/funnel/checks", body("landed", "downloaded", "installed", "signed_up", "never_sent"))
	data := res["steps"].([]any)
	if len(data) != 5 {
		t.Fatalf("steps: %v", data)
	}
	step := func(n int) map[string]any { return data[n-1].(map[string]any) }
	// Everyone downloaded: the pair on its own keeps them all.
	if step(2)["people"] != 1200.0 || step(2)["nothing_before"] != 0.0 {
		t.Fatalf("step 2: %v", step(2))
	}
	if pair := step(2)["from_previous"].(map[string]any); pair["started"] != 1200.0 || pair["reached"] != 1200.0 {
		t.Fatalf("step 1 to 2: %v", pair)
	}
	// 17 days of installs, half under the installer's own id.
	if step(3)["people"] != 1020.0 || step(3)["nothing_before"] != 510.0 {
		t.Fatalf("step 3: %v", step(3))
	}
	if pair := step(3)["from_previous"].(map[string]any); pair["started"] != 1200.0 || pair["reached"] != 510.0 {
		t.Fatalf("step 2 to 3: %v", pair)
	}
	// Sign-ups all arrive, though the funnel would count only those the
	// installer step let through.
	if step(4)["people"] != 1200.0 || step(4)["last_received_at"] == nil {
		t.Fatalf("step 4: %v", step(4))
	}
	if step(5)["people"] != 0.0 || step(5)["last_received_at"] != nil {
		t.Fatalf("step 5: %v", step(5))
	}
	if _, ok := step(1)["from_previous"]; ok {
		t.Fatalf("step 1 has no step before it: %v", step(1))
	}

	// What looks wrong, in step order.
	var got []string
	for _, raw := range res["data"].([]any) {
		c := raw.(map[string]any)
		got = append(got, fmt.Sprintf("%v:%v", c["step"], c["kind"]))
		switch c["kind"] {
		case "stopped":
			if c["last_received_at"] == nil || c["daily_before"] != 60.0 {
				t.Fatalf("stopped: %v", c)
			}
		case "unlinked":
			if c["people"] != 510.0 {
				t.Fatalf("unlinked: %v", c)
			}
		}
	}
	if want := "[3:stopped 3:unlinked 5:never_received]"; fmt.Sprint(got) != want {
		t.Fatalf("checks: %v, want %v", got, want)
	}

	// A funnel whose data is all there finds nothing.
	res = owner.must(200, "POST", base+"/insights/funnel/checks", body("landed", "downloaded", "signed_up"))
	if found := res["data"].([]any); len(found) != 0 {
		t.Fatalf("a healthy funnel: %v", found)
	}

	// Any order has no step before a step, so nothing to check.
	anyOrder := body("landed", "downloaded")
	anyOrder["funnel"].(map[string]any)["order"] = "any"
	owner.must(400, "POST", base+"/insights/funnel/checks", anyOrder)
}
