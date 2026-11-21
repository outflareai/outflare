package api_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

// A holdout compares sign-ups from the countries the ads were kept out of
// with everywhere else, before and during, and says what the ads added.
func TestPaidAdsHoldouts(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand + "/ads/holdouts"
	day := 24 * time.Hour
	today := time.Now().UTC().Truncate(day)
	date := func(d int) string { return today.Add(time.Duration(d) * day).Format(time.DateOnly) }

	// people signs up n people from a country at a time; ads of them are
	// credited to an ad, and their visits arrived from a paid click.
	n := 0
	people := func(count, ads int, country string, at time.Time) {
		t.Helper()
		for i := range count {
			n++
			var rec string
			if err := e.pool.QueryRow(ctx, `INSERT INTO records (brand_id, workspace_id, record_type, name) VALUES ($1, $2, 'person', $3) RETURNING id::text`, brand, ws, fmt.Sprintf("Person %d", n)).Scan(&rec); err != nil {
				t.Fatal(err)
			}
			clicks := "{}"
			if i < ads {
				clicks = `{"gclid": "Cj0test"}`
			}
			if _, err := e.pool.Exec(ctx, `INSERT INTO site_sessions (brand_id, workspace_id, record_id, anonymous_id, started_at, last_seen_at, country, click_ids) VALUES ($1, $2, $3, $4, $5, $5, $6, $7)`,
				brand, ws, rec, fmt.Sprintf("anon-%d", n), at.Add(-time.Hour), country, clicks); err != nil {
				t.Fatal(err)
			}
			if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (brand_id, workspace_id, record_id, kind, name, occurred_at, message_id) VALUES ($1, $2, $3, 'track', 'signed_up', $4, gen_random_uuid()::text)`, brand, ws, rec, at); err != nil {
				t.Fatal(err)
			}
			if i < ads {
				if _, err := e.pool.Exec(ctx, `INSERT INTO ad_acquisitions (workspace_id, brand_id, record_id, connector, clicked_at, window_days, signed_up_at) VALUES ($1, $2, $3, 'google_ads', $4, 30, $4)`, ws, brand, rec, at); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	// The holdout started 10 days ago in Portugal. Before: 100 sign-ups
	// there (50 credited to ads) and 200 elsewhere. During: 60 there with
	// no paid click, and 200 elsewhere again, so 100 were expected: the ads
	// were adding 40, or 80% of the 50 they were credited with.
	before, during := today.Add(-15*day), today.Add(-5*day)
	people(100, 50, "PT", before)
	people(200, 30, "ES", before)
	people(60, 0, "PT", during)
	people(200, 30, "ES", during)

	// Bad input is refused.
	for _, bad := range []map[string]any{
		{"name": "", "countries": []string{"PT"}, "starts_on": date(-10), "ends_on": date(10)},
		{"name": "x", "countries": []string{}, "starts_on": date(-10), "ends_on": date(10)},
		{"name": "x", "countries": []string{"Portugal"}, "starts_on": date(-10), "ends_on": date(10)},
		{"name": "x", "countries": []string{"PT"}, "starts_on": date(0), "ends_on": date(2)},
		{"name": "x", "countries": []string{"PT"}, "starts_on": date(0), "ends_on": date(200)},
		{"name": "x", "countries": []string{"PT"}, "starts_on": date(-10), "ends_on": date(10), "connector": "nope"},
	} {
		owner.must(400, "POST", base, bad)
	}

	events := track.Capture(t)
	h := owner.must(201, "POST", base, map[string]any{"name": "Portugal off", "countries": []string{"pt"}, "starts_on": date(-9), "ends_on": date(10)})
	id := h["id"].(string)
	res := h["result"].(map[string]any)
	if h["status"] != "running" || h["countries"].([]any)[0] != "PT" || res["days"] != 10.0 || res["verdict"] != "ads_add" {
		t.Fatalf("created: %v", h)
	}
	side := func(k string) map[string]any { return res[k].(map[string]any) }
	if side("held_before")["signups"] != 100.0 || side("held_before")["ad_signups"] != 50.0 || side("held_before")["paid_visits"] != 50.0 ||
		side("held_during")["signups"] != 60.0 || side("held_during")["paid_visits"] != 0.0 ||
		side("rest_before")["signups"] != 200.0 || side("rest_during")["signups"] != 200.0 {
		t.Fatalf("sides: %v", res)
	}
	if res["expected_signups"] != 100.0 || res["lift_signups"] != 40.0 || res["incrementality"] != 0.8 || res["leaked"] != false {
		t.Fatalf("result: %v", res)
	}
	if made := named(events(), "ad_holdout.created"); len(made) != 1 || made[0].Props["countries"] != 1 {
		t.Fatalf("ad_holdout.created: %+v", made)
	}

	// A holdout that hasn't started is scheduled, and can't be ended.
	later := owner.must(201, "POST", base, map[string]any{"name": "Spain next", "countries": []string{"ES"}, "starts_on": date(5), "ends_on": date(20), "connector": "google_ads"})
	if later["status"] != "scheduled" || later["result"].(map[string]any)["verdict"] != "scheduled" {
		t.Fatalf("scheduled: %v", later)
	}
	owner.must(409, "POST", base+"/"+later["id"].(string)+"/end", nil)

	list := owner.must(200, "GET", base, nil)["data"].([]any)
	if len(list) != 2 || list[0].(map[string]any)["name"] != "Spain next" {
		t.Fatalf("list: %v", list)
	}

	// Ending it sets its last day to today; deleting removes it.
	ended := owner.must(200, "POST", base+"/"+id+"/end", nil)
	if ended["ends_on"] != date(0) || ended["status"] != "running" {
		t.Fatalf("ended: %v", ended)
	}
	owner.must(409, "POST", base+"/"+id+"/end", nil)
	owner.must(204, "DELETE", base+"/"+id, nil)
	owner.must(404, "DELETE", base+"/"+id, nil)
	owner.must(404, "POST", base+"/"+id+"/end", nil)
}
