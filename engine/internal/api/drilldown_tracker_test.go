package api_test

import (
	"context"
	"testing"
	"time"
)

// The tracker as a source of the drill-down report (plans/ad-tracker.md,
// B5d): clicks without likely bots, the click ledger's conversions without
// declined ones, and the cost the traffic source reported on each click,
// grouped by the tracker's own dimensions.
func TestDrilldownTrackerSource(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE brands SET currency = 'USD' WHERE id = $1`, brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	ts := owner.must(201, "POST", base+"/traffic-sources", map[string]any{"name": "Push network", "currency": "USD", "click_id": map[string]any{"param": "cid", "token": "{cid}"}})
	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Push US", "channel": "paid_ads", "source": "push", "redirect_links": true, "traffic_source": ts["id"]})
	l := owner.must(201, "POST", base+"/tracking-campaigns/"+c["id"].(string)+"/links", map[string]any{"url": "https://offers.northpeak.example/o1"})
	src := owner.must(201, "POST", base+"/postback-sources", map[string]any{"name": "Network"})
	offerA, offerB := "50000000-0000-0000-0000-00000000000a", "50000000-0000-0000-0000-00000000000b"
	day1 := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	day2 := day1.AddDate(0, 0, 1)
	click := func(key, country, offer string, bot bool, cost int64, at time.Time) int64 {
		t.Helper()
		var id int64
		if err := e.pool.QueryRow(ctx, `INSERT INTO tracking_link_clicks (workspace_id, brand_id, link_id, click_key, likely_bot, clicked_at, country, offer_id, cost_micros, ip_hash)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8::uuid, $9, $4) RETURNING id`, ws, brand, l["id"], key, bot, at, country, offer, cost).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	convert := func(clickID int64, key, status string, payout int64, at time.Time) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `INSERT INTO tracker_conversions (workspace_id, brand_id, source_id, click_id, click_key, campaign_id, link_id, clicked_at,
				transaction_id, status, payout_micros, currency, converted_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8::timestamptz, $5::text||'-tx', $9, $10, 'USD', $8::timestamptz + interval '1 hour')`,
			ws, brand, src["id"], clickID, key, c["id"], l["id"], at, status, payout); err != nil {
			t.Fatal(err)
		}
	}
	k1 := click("aaaaaaaaaaaaaaa1", "US", offerA, false, 100000, day1)
	k2 := click("aaaaaaaaaaaaaaa2", "US", offerB, false, 100000, day1)
	click("aaaaaaaaaaaaaaa3", "US", offerA, true, 100000, day1)
	k4 := click("aaaaaaaaaaaaaaa4", "DE", offerA, false, 0, day2)
	convert(k1, "aaaaaaaaaaaaaaa1", "approved", 2_500000, day1)
	convert(k2, "aaaaaaaaaaaaaaa2", "declined", 9_000000, day1)
	convert(k4, "aaaaaaaaaaaaaaa4", "pending", 1_000000, day2)

	metric := func(m any, name string) any { return m.(map[string]any)[name] }
	r := owner.must(200, "GET", base+"/drilldown?source=tracker&from=2026-10-01&to=2026-10-10&timezone=UTC&group_by=country", nil)
	if r["source"] != "tracker" || r["cost_available"] != true {
		t.Fatalf("report: %v", r)
	}
	rows := map[string]any{}
	for _, row := range r["rows"].([]any) {
		rows[row.(map[string]any)["keys"].([]any)[0].(string)] = row.(map[string]any)["metrics"]
	}
	// US: two clicks (the bot left out), one conversion (the declined one
	// left out), 2.5 revenue, 0.2 cost; DE: one click, a pending conversion.
	us, de := rows["US"], rows["DE"]
	if metric(us, "visits") != float64(2) || metric(us, "conversions") != float64(1) || metric(us, "revenue") != 2.5 || metric(us, "cost") != 0.2 || metric(us, "profit") != 2.3 {
		t.Fatalf("US: %v", us)
	}
	if metric(de, "visits") != float64(1) || metric(de, "conversions") != float64(1) || metric(de, "revenue") != float64(1) || metric(de, "cost") != float64(0) {
		t.Fatalf("DE: %v", de)
	}
	if tot := r["totals"]; metric(tot, "visits") != float64(3) || metric(tot, "revenue") != 3.5 {
		t.Fatalf("totals: %v", tot)
	}
	// The tracker's own levels: campaign, then offer.
	r = owner.must(200, "GET", base+"/drilldown?source=tracker&from=2026-10-01&to=2026-10-10&timezone=UTC&group_by=tracking_campaign,offer&filter=suspect:no", nil)
	camp := r["rows"].([]any)[0].(map[string]any)
	if camp["keys"].([]any)[0] != c["id"] || len(camp["rows"].([]any)) != 2 {
		t.Fatalf("by campaign and offer: %v", camp)
	}
	for _, o := range camp["rows"].([]any) {
		om := o.(map[string]any)
		if om["keys"].([]any)[1] == offerA && metric(om["metrics"], "visits") != float64(2) {
			t.Fatalf("offer A: %v", om)
		}
	}
	// The traffic source is a level too; conversions by their own time.
	r = owner.must(200, "GET", base+"/drilldown?source=tracker&from=2026-10-01&to=2026-10-10&timezone=UTC&group_by=traffic_source&conversion_time=conversion", nil)
	if r["rows"].([]any)[0].(map[string]any)["keys"].([]any)[0] != ts["id"] {
		t.Fatalf("by traffic source: %v", r["rows"])
	}
	// No cost reported in the range: cost is unknown, and the note says why.
	r = owner.must(200, "GET", base+"/drilldown?source=tracker&from=2026-10-06&to=2026-10-06&timezone=UTC&group_by=country", nil)
	if r["cost_available"] != false || r["cost_note"] == nil {
		t.Fatalf("no cost: %v", r)
	}
	// A saved view keeps the source.
	v := owner.must(201, "POST", base+"/report-views", map[string]any{"name": "Tracker by offer", "spec": map[string]any{"source": "tracker", "group_by": []string{"offer"},
		"date_range": map[string]any{"from": "2026-10-01", "to": "2026-10-10"}}})
	if r := owner.must(200, "GET", base+"/drilldown?view="+v["id"].(string), nil); r["source"] != "tracker" || r["group_by"].([]any)[0] != "offer" {
		t.Fatalf("view: %v", r)
	}
	// Site dimensions aren't the tracker's, and the reverse.
	for _, bad := range []string{"source=tracker&group_by=channel", "source=tracker&filter=utm_source:x", "group_by=offer", "source=ads"} {
		owner.must(400, "GET", base+"/drilldown?from=2026-10-01&to=2026-10-10&"+bad, nil)
	}
	owner.must(400, "POST", base+"/report-views", map[string]any{"name": "Bad", "spec": map[string]any{"source": "tracker", "group_by": []string{"channel"}}})
}
