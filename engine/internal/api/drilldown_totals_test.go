package api_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The report's totals are the same whatever it is grouped by. Three things
// broke that: a first level with an empty value (a click with no offer)
// shared the totals' key, so its numbers were counted twice and its row
// was missing; at conversion time the day was still the click's; and money
// in a currency with no exchange rate vanished without a word.
func TestDrilldownTotalsHoldForEveryGrouping(t *testing.T) {
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
	offer := "50000000-0000-0000-0000-00000000000a"
	day1 := time.Date(2026, 10, 5, 23, 30, 0, 0, time.UTC)
	click := func(key, country string, offer any, at time.Time) int64 {
		t.Helper()
		var id int64
		if err := e.pool.QueryRow(ctx, `INSERT INTO tracking_link_clicks (workspace_id, brand_id, link_id, click_key, clicked_at, country, offer_id, cost_micros, ip_hash)
			VALUES ($1, $2, $3, $4, $5, $6, $7::uuid, 100000, $4) RETURNING id`, ws, brand, l["id"], key, at, country, offer).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// A conversion an hour after its click, so the late click's lands on
	// the next day.
	convert := func(clickID int64, key string, payout int64, currency string, at time.Time) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `INSERT INTO tracker_conversions (workspace_id, brand_id, source_id, click_id, click_key, campaign_id, link_id, clicked_at,
				transaction_id, status, payout_micros, currency, converted_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8::timestamptz, $5::text||'-tx', 'approved', $9, $10, $8::timestamptz + interval '1 hour')`,
			ws, brand, src["id"], clickID, key, c["id"], l["id"], at, payout, currency); err != nil {
			t.Fatal(err)
		}
	}
	k1 := click("bbbbbbbbbbbbbbb1", "US", offer, day1)
	click("bbbbbbbbbbbbbbb2", "US", offer, day1)
	k3 := click("bbbbbbbbbbbbbbb3", "US", nil, day1) // straight to a page: no offer
	convert(k1, "bbbbbbbbbbbbbbb1", 2_500000, "USD", day1)
	convert(k3, "bbbbbbbbbbbbbbb3", 4_000000, "USD", day1)

	type row = map[string]any
	rowsOf := func(r row) map[string]row {
		out := map[string]row{}
		for _, x := range r["rows"].([]any) {
			out[x.(row)["keys"].([]any)[0].(string)] = x.(row)
		}
		return out
	}
	check := func(what string, m any, visits, conversions, revenue, cost float64) {
		t.Helper()
		got := m.(row)
		if got["visits"] != visits || got["conversions"] != conversions || got["revenue"] != revenue || got["cost"] != cost {
			t.Fatalf("%s: want %v visits, %v conversions, %v revenue, %v cost; got %v", what, visits, conversions, revenue, cost, got)
		}
	}
	q := base + "/drilldown?source=tracker&from=2026-10-01&to=2026-10-10&timezone=UTC"

	// By campaign nothing is empty: the truth.
	check("totals by campaign", owner.must(200, "GET", q+"&group_by=tracking_campaign", nil)["totals"], 3, 2, 6.5, 0.3)
	// A country code filters in any case: they are stored in capitals.
	check("a lower-case country filter", owner.must(200, "GET", q+"&group_by=country&filter=country:us", nil)["totals"], 3, 2, 6.5, 0.3)
	// The report names what it keys by id, for its rows and its filters,
	// and the CSV writes the names.
	named := owner.must(200, "GET", q+"&group_by=tracking_campaign,traffic_source&filter=tracking_campaign:"+c["id"].(string), nil)["names"].(row)
	if named[c["id"].(string)] != "Push US" || named[ts["id"].(string)] != "Push network" || len(named) != 2 {
		t.Fatalf("names: %v", named)
	}
	req := httptest.NewRequest("GET", q+"&group_by=tracking_campaign,country&format=csv", nil)
	req.AddCookie(owner.cookie)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if body := rec.Body.String(); rec.Code != 200 || !strings.Contains(body, "\nPush US,US,3,") || strings.Contains(body, c["id"].(string)) {
		t.Fatalf("csv by campaign: %d %s", rec.Code, body)
	}
	// By offer, the click with no offer is a row of its own and is counted
	// once.
	r := owner.must(200, "GET", q+"&group_by=offer", nil)
	check("totals by offer", r["totals"], 3, 2, 6.5, 0.3)
	rows := rowsOf(r)
	if len(rows) != 2 {
		t.Fatalf("by offer: want the offer and the empty row, got %v", r["rows"])
	}
	check("the offer", rows[offer]["metrics"], 2, 1, 2.5, 0.2)
	check("no offer", rows[""]["metrics"], 1, 1, 4, 0.1)
	// A level nothing has: one empty row holding everything, not double
	// totals over no rows.
	r = owner.must(200, "GET", q+"&group_by=field3", nil)
	check("totals by an unused field", r["totals"], 3, 2, 6.5, 0.3)
	check("the empty field row", rowsOf(r)[""]["metrics"], 3, 2, 6.5, 0.3)
	// Two levels: the empty row keeps its children.
	r = owner.must(200, "GET", q+"&group_by=offer,country", nil)
	check("totals by offer and country", r["totals"], 3, 2, 6.5, 0.3)
	none := rowsOf(r)[""]
	kids := none["rows"].([]any)
	if len(r["rows"].([]any)) != 2 || len(kids) != 1 || kids[0].(row)["keys"].([]any)[1] != "US" {
		t.Fatalf("by offer and country: %v", r["rows"])
	}
	check("no offer, US", kids[0].(row)["metrics"], 1, 1, 4, 0.1)
	if u := r["unconverted_currencies"].([]any); len(u) != 0 {
		t.Fatalf("everything is in the brand's currency: %v", u)
	}

	// At conversion time, a report for the day after the click holds that
	// day's conversions under that day, and a filter on the day finds them.
	next := base + "/drilldown?source=tracker&from=2026-10-06&to=2026-10-06&timezone=UTC&conversion_time=conversion&group_by=day"
	r = owner.must(200, "GET", next, nil)
	rows = rowsOf(r)
	if len(rows) != 1 || rows["2026-10-06"] == nil || rows["2026-10-06"]["metrics"].(row)["conversions"] != float64(2) {
		t.Fatalf("by day at conversion time: %v", r["rows"])
	}
	r = owner.must(200, "GET", next+"&filter=day:2026-10-06", nil)
	if r["totals"].(row)["conversions"] != float64(2) {
		t.Fatalf("a filter on the conversion's day: %v", r["totals"])
	}
	// At click time (the default) the same conversions sit on the click's day.
	r = owner.must(200, "GET", base+"/drilldown?source=tracker&from=2026-10-05&to=2026-10-06&timezone=UTC&group_by=day", nil)
	if rows = rowsOf(r); len(rows) != 1 || rows["2026-10-05"]["metrics"].(row)["conversions"] != float64(2) {
		t.Fatalf("by day at click time: %v", r["rows"])
	}

	// Money in a currency with no exchange rate is named, not just left out.
	if _, err := e.pool.Exec(ctx, `DELETE FROM exchange_rates WHERE currency = 'ISK'`); err != nil {
		t.Fatal(err)
	}
	k4 := click("bbbbbbbbbbbbbbb4", "IS", offer, day1)
	convert(k4, "bbbbbbbbbbbbbbb4", 900_000000, "ISK", day1)
	r = owner.must(200, "GET", q+"&group_by=country", nil)
	check("totals with an unconverted sale", r["totals"], 4, 3, 6.5, 0.4)
	if u := r["unconverted_currencies"].([]any); len(u) != 1 || u[0] != "ISK" {
		t.Fatalf("unconverted currencies: %v", u)
	}

	// With no to, the range ends on the report zone's own today. East of
	// UTC that is already tomorrow for part of the day, and from=today
	// must not be refused as "after to"; a day that hasn't come still is.
	ahead, err := time.LoadLocation("Pacific/Kiritimati")
	if err != nil {
		t.Fatal(err)
	}
	owner.must(200, "GET", base+"/drilldown?source=tracker&timezone=Pacific/Kiritimati&from="+time.Now().In(ahead).Format(time.DateOnly), nil)
	owner.must(400, "GET", base+"/drilldown?source=tracker&timezone=Pacific/Kiritimati&from="+time.Now().In(ahead).AddDate(0, 0, 2).Format(time.DateOnly), nil)
}
