package api_test

import (
	"context"
	"math"
	"net/http/httptest"
	"net/url"
	"regexp"
	"testing"
	"time"
)

// Network cost (plans/ad-tracker.md, Z2): a Zeropark campaign's spend per
// target and day is shared out over the tracker's clicks from that
// campaign and target, by each click's own {visit_cost}; spend on a
// target no click named goes over the clicks that named no reported
// target. The clicks, the record and the report all add up to Zeropark's
// figure exactly, and a later re-read corrects them.
func TestNetworkCosts(t *testing.T) {
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
	host := ownTrackingDomain(t, e, ws, brand, "go.northpeak.example")

	ts := owner.must(201, "POST", base+"/traffic-sources", map[string]any{"template": "zeropark"})
	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Zeropark push", "channel": "paid_ads", "source": "zeropark",
		"redirect_links": true, "traffic_source": ts["id"]})
	l := owner.must(201, "POST", base+"/tracking-campaigns/"+c["code"].(string)+"/links", map[string]any{"url": "https://offers.northpeak.example/o1"})
	code := regexp.MustCompile(`/l/([a-z0-9]{8})$`).FindStringSubmatch(l["redirect_url"].(string))[1]
	const cid = "11111111-1111-4111-8111-111111111111"
	click := func(target, cost string) {
		t.Helper()
		q := url.Values{"cid": {"zp-click"}, "campaign_id": {cid}, "cost": {cost}}
		if target != "" {
			q.Set("target", target)
		}
		req := httptest.NewRequest("GET", "/l/"+code+"?"+q.Encode(), nil)
		req.Host = host
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code/100 != 3 {
			t.Fatalf("click: %d", rec.Code)
		}
	}
	click("pub-a", "0.002")
	click("pub-a", "0.001")
	click("pub-x", "0.002") // a target Zeropark doesn't report
	click("", "0")          // no target at all
	if err := e.srv.FlushClicks(ctx); err != nil {
		t.Fatal(err)
	}

	// Not connected yet: nothing happens.
	if n, err := e.srv.SyncNetworkCosts(ctx); err != nil || n != 0 {
		t.Fatalf("unconnected sync: %d %v", n, err)
	}
	owner.must(201, "POST", "/v1/workspaces/"+ws+"/integrations", map[string]any{"connector": "zeropark_ads", "credentials": map[string]any{"api_token": "test-good"}})
	zeroparkFake.set(cid, []map[string]any{
		{"id": "aaaaaaaa-0000-4000-8000-000000000001", "target": "pub-a", "address": "pub-a", "stats": map[string]any{"spent": 0.006, "redirects": 2}},
		{"id": "aaaaaaaa-0000-4000-8000-000000000003", "target": "pub-c", "address": "pub-c", "stats": map[string]any{"spent": 0.5, "redirects": 250}},
	})
	if n, err := e.srv.SyncNetworkCosts(ctx); err != nil || n < 1 {
		t.Fatalf("sync: %d %v", n, err)
	}
	costs := func() map[string]int64 {
		t.Helper()
		rows, err := e.pool.Query(ctx, `SELECT COALESCE(fields->>'1', ''), network_cost_micros FROM tracking_link_clicks WHERE brand_id = $1`, brand)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]int64{}
		for rows.Next() {
			var target string
			var v *int64
			if err := rows.Scan(&target, &v); err != nil {
				t.Fatal(err)
			}
			if v == nil {
				t.Fatalf("click %q not reconciled", target)
			}
			out[target] += *v
		}
		return out
	}
	// pub-a's 0.006 goes 2:1 by visit cost; pub-c's 0.5 has no click of
	// its own, so it goes on the clicks that named no reported target, by
	// their visit cost (the one without a cost gets none).
	if got := costs(); got["pub-a"] != 6000 || got["pub-x"] != 500_000 || got[""] != 0 {
		t.Fatalf("click costs: %v", got)
	}
	var lo, hi int64
	if err := e.pool.QueryRow(ctx, `SELECT min(network_cost_micros), max(network_cost_micros) FROM tracking_link_clicks WHERE brand_id = $1 AND fields->>'1' = 'pub-a'`,
		brand).Scan(&lo, &hi); err != nil || lo != 2000 || hi != 4000 {
		t.Fatalf("pub-a split: %d %d %v", lo, hi, err)
	}

	// The record says what Zeropark reported and where it went.
	days := owner.must(200, "GET", base+"/network-costs", nil)["data"].([]any)
	if len(days) != 1 {
		t.Fatalf("network cost days: %v", days)
	}
	d := days[0].(map[string]any)
	if d["network"] != "zeropark" || d["campaign_ref"] != cid || d["spend"] != "0.506" || d["matched"] != "0.006" || d["spread"] != "0.5" ||
		d["unattributed"] != "0" || d["clicks"] != float64(4) || d["targets"] != float64(2) || d["source_id"] != ts["id"] {
		t.Fatalf("network cost day: %v", d)
	}

	// The report's cost is Zeropark's spend, to the micro.
	today := time.Now().UTC().Format("2006-01-02")
	report := func() float64 {
		t.Helper()
		r := owner.must(200, "GET", base+"/drilldown?source=tracker&from="+today+"&to="+today+"&timezone=UTC", nil)["totals"].(map[string]any)
		return r["cost"].(float64)
	}
	if got := report(); math.Abs(got-0.506) > 1e-9 {
		t.Fatalf("report cost %v, want 0.506", got)
	}
	// The click log shows each click's share beside its own cost.
	log := owner.must(200, "GET", base+"/tracker-clicks", nil)["data"].([]any)
	if len(log) != 4 || log[0].(map[string]any)["network_cost"] == nil {
		t.Fatalf("click log: %v", log)
	}

	// Zeropark settles the day higher; the next pass corrects every click.
	zeroparkFake.set(cid, []map[string]any{
		{"id": "aaaaaaaa-0000-4000-8000-000000000001", "target": "pub-a", "stats": map[string]any{"spent": 0.009, "redirects": 3}},
	})
	if _, err := e.srv.SyncNetworkCosts(ctx); err != nil {
		t.Fatal(err)
	}
	if got := costs(); got["pub-a"] != 9000 || got["pub-x"] != 0 {
		t.Fatalf("after the re-read: %v", got)
	}
	if got := report(); math.Abs(got-0.009) > 1e-9 {
		t.Fatalf("report cost after the re-read %v", got)
	}
}
