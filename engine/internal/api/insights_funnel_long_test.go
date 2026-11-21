package api_test

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// A long journey across several systems (an ad visit, a download, an
// install, a sign-up, a first use) fits in one funnel of up to 20 steps,
// and each step still shows its own drop-off.
func TestFunnelLongJourney(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand

	const steps = 20
	day := 24 * time.Hour
	t0 := time.Now().UTC().Truncate(day).Add(-5*day + time.Hour)
	// Person i gets through the first steps-i steps: the first does all
	// 20, the last only step 1.
	for i := 0; i < steps; i++ {
		id := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": fmt.Sprintf("Person %d", i)})["id"].(string)
		for s := 0; s < steps-i; s++ {
			if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, brand_id, record_id, kind, name, occurred_at, properties)
				VALUES ($1, $2, $3, 'track', $4, $5, '{}')`, ws, brand, id, fmt.Sprintf("stage_%d", s+1), t0.Add(time.Duration(s)*time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
	}
	funnel := func(n int) map[string]any {
		list := make([]map[string]any, n)
		for s := range list {
			list[s] = map[string]any{"kind": "event", "event": fmt.Sprintf("stage_%d", s+1)}
		}
		return map[string]any{
			"date_range": map[string]any{"from": t0.Add(-day).Format(time.DateOnly), "to": time.Now().UTC().Format(time.DateOnly)},
			"steps":      list,
			"window":     map[string]any{"value": 14, "unit": "day"},
		}
	}

	res := owner.must(200, "POST", base+"/insights/funnel", funnel(steps))
	got := res["steps"].([]any)
	if len(got) != steps {
		t.Fatalf("want %d steps, got %d", steps, len(got))
	}
	for s, raw := range got {
		st := raw.(map[string]any)
		if want := float64(steps - s); st["count"] != want {
			t.Fatalf("step %d: count %v, want %v", s+1, st["count"], want)
		}
		if s > 0 && st["dropped"] != 1.0 {
			t.Fatalf("step %d: dropped %v, want 1", s+1, st["dropped"])
		}
	}

	// The people behind the last step and the ones who stopped just before.
	people := owner.must(200, "POST", base+"/insights/funnel/people", map[string]any{"funnel": funnel(steps), "step": steps, "dropped": true})
	if n := len(people["data"].([]any)); n != 1 {
		t.Fatalf("dropped before the last step: %d people, want 1", n)
	}

	// 21 is one too many, and the message says the limit.
	bad := owner.must(400, "POST", base+"/insights/funnel", funnel(steps+1))
	if msg := bad["error"].(map[string]any)["message"]; msg != "steps has 2 to 20 steps" {
		t.Fatalf("message: %v", msg)
	}
}
