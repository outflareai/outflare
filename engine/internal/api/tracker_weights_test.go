package api_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// Suggested weights (plans/ad-tracker.md, T9): each list of a link's paths
// gets the chance each lander, offer or page is the best and the weights
// that follow, from the window's clicks and conversions without likely
// bots, suspect ones or declined conversions; a person applies them through
// the weights endpoint, which changes only weights.
func TestTrackerWeights(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	ownTrackingDomain(t, e, ws, brand, "go.northpeak.example")

	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Push", "channel": "paid_ads", "source": "acme", "redirect_links": true})
	cpath := base + "/tracking-campaigns/" + c["code"].(string)
	link := owner.must(201, "POST", cpath+"/links", map[string]any{"url": "https://northpeak.example/own"})
	lpath := cpath + "/links/" + link["id"].(string)
	dests := owner.must(200, "PUT", lpath+"/destinations", map[string]any{"destinations": []any{
		map[string]any{"name": "A", "url": "https://northpeak.example/a", "weight": 50},
		map[string]any{"name": "B", "url": "https://northpeak.example/b", "weight": 50},
	}})["destinations"].([]any)
	a, b := dests[0].(map[string]any)["id"].(string), dests[1].(map[string]any)["id"].(string)
	lander := owner.must(201, "POST", base+"/landers", map[string]any{"name": "Quiz", "url": "https://quiz.northpeak.example/q"})
	paths := owner.must(200, "PUT", lpath+"/paths", map[string]any{"paths": []any{map[string]any{"name": "Mobile",
		"conditions": []any{map[string]any{"field": "device", "op": "is", "values": []any{"mobile"}}},
		"destinations": []any{
			map[string]any{"lander_id": lander["id"]},
			map[string]any{"name": "X", "url": "https://offers.northpeak.example/x"},
			map[string]any{"name": "Y", "url": "https://offers.northpeak.example/y"},
		}}}})["paths"].([]any)
	pd := paths[0].(map[string]any)["destinations"].([]any)
	ld, x, y := pd[0].(map[string]any)["id"].(string), pd[1].(map[string]any)["id"].(string), pd[2].(map[string]any)["id"].(string)
	src := owner.must(201, "POST", base+"/postback-sources", map[string]any{"name": "Acme Network"})
	if _, err := e.pool.Exec(ctx, `INSERT INTO exchange_rates (currency, day, usd_per_unit) VALUES ('EUR', current_date, 1.1) ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}

	// clicks adds n clicks on dest, ago days back, the first convs of
	// them converting for payout in currency with status; via sends each
	// on from the lander to that destination.
	batch := 0
	clicks := func(dest string, n int, bot bool, fraud []string, ago int, convs int, payout int64, currency, status, via string) {
		t.Helper()
		batch++
		if fraud == nil {
			fraud = []string{}
		}
		if _, err := e.pool.Exec(ctx, `WITH k AS (
				INSERT INTO tracking_link_clicks (workspace_id, brand_id, link_id, click_key, likely_bot, clicked_at, destination_id, fraud_reasons)
				SELECT $1, $2, $3, substr(md5($4 || '-' || g), 1, 16), $5, now() - make_interval(days => $6), $7, $8::text[]
				FROM generate_series(1, $9::int) g RETURNING id, click_key, clicked_at),
			lc AS (
				INSERT INTO tracker_lander_clicks (workspace_id, brand_id, lander_id, click_id, click_key, cta, destination_id, clicked_at)
				SELECT $1, $2, $15, id, click_key, 1, $14::uuid, clicked_at + interval '1 minute' FROM k WHERE $14::uuid IS NOT NULL)
			INSERT INTO tracker_conversions (workspace_id, brand_id, source_id, click_id, click_key, campaign_id, link_id, clicked_at,
				transaction_id, payout_micros, currency, converted_at, status)
			SELECT $1, $2, $10, id, click_key, $11, $3, clicked_at, 'T-' || click_key, $12, $13, clicked_at + interval '1 hour', $16
			FROM k ORDER BY id LIMIT $17`,
			ws, brand, link["id"], fmt.Sprint(batch), bot, ago, dest, fraud, n,
			src["id"], c["id"], payout, currency, nilIfEmpty(via), lander["id"], status, convs); err != nil {
			t.Fatal(err)
		}
	}
	const usd10 = 10_000_000
	clicks(a, 300, false, nil, 1, 3, usd10, "USD", "approved", "")
	clicks(a, 1, false, nil, 1, 1, usd10, "USD", "declined", "")  // declined: no conversion
	clicks(a, 5, false, nil, 20, 5, usd10, "USD", "approved", "") // before the window
	clicks(b, 300, false, nil, 1, 30, usd10, "USD", "approved", "")
	clicks(b, 1, false, nil, 1, 1, usd10, "EUR", "approved", "") // 10 EUR is 11 USD
	clicks(b, 20, true, nil, 1, 2, usd10, "USD", "approved", "") // likely bots
	clicks(b, 10, false, []string{"datacenter_network"}, 1, 10, usd10, "USD", "approved", "")
	clicks(ld, 100, false, nil, 1, 2, usd10, "USD", "approved", x) // through the lander to X
	clicks(ld, 100, false, nil, 1, 1, usd10, "USD", "approved", y)

	v := owner.must(200, "GET", lpath+"/weight-suggestions", nil)
	if v["metric"] != "revenue" || v["days"] != float64(14) || v["currency"] != "USD" || v["link_id"] != link["id"] || v["flow_id"] != nil {
		t.Fatalf("suggestions: %v", v)
	}
	lists := v["lists"].([]any)
	if len(lists) != 3 {
		t.Fatalf("lists: %v", lists)
	}
	def := lists[0].(map[string]any)
	items := def["items"].([]any)
	ia, ib := items[0].(map[string]any), items[1].(map[string]any)
	if def["path_id"] != nil || def["kind"] != "destinations" || def["ready"] != true || def["reason"] != nil {
		t.Fatalf("default list: %v", def)
	}
	if ia["clicks"] != float64(301) || ia["excluded_clicks"] != float64(0) || ia["conversions"] != float64(3) || ia["revenue"] != "30" ||
		ib["clicks"] != float64(301) || ib["excluded_clicks"] != float64(30) || ib["conversions"] != float64(31) || ib["revenue"] != "311" {
		t.Fatalf("numbers: %v %v", ia, ib)
	}
	if ib["probability_best"].(float64) < 0.99 || ia["suggested_weight"] != float64(5) || ib["suggested_weight"] != float64(95) || ia["weight"] != float64(50) {
		t.Fatalf("suggestion: %v %v", ia, ib)
	}
	landers, onward := lists[1].(map[string]any), lists[2].(map[string]any)
	if landers["kind"] != "landers" || landers["ready"] != false || landers["reason"] != "too_few_arms" || landers["path_name"] != "Mobile" {
		t.Fatalf("landers: %v", landers)
	}
	ox := onward["items"].([]any)[0].(map[string]any)
	if onward["kind"] != "destinations" || onward["reason"] != "not_enough_conversions" || ox["clicks"] != float64(100) || ox["conversions"] != float64(2) ||
		ox["suggested_weight"] != float64(100) || ox["probability_best"] != nil {
		t.Fatalf("after the lander: %v", onward)
	}
	owner.must(200, "GET", lpath+"/weight-suggestions?metric=conversions&days=30", nil)
	owner.must(400, "GET", lpath+"/weight-suggestions?metric=roi", nil)
	owner.must(400, "GET", lpath+"/weight-suggestions?days=91", nil)

	// Applying them changes only the weights.
	got := owner.must(200, "PATCH", lpath+"/weights", map[string]any{"source": "suggestion", "weights": []any{
		map[string]any{"destination_id": a, "weight": 5}, map[string]any{"destination_id": b, "weight": 95}}})
	ga, gb := got["destinations"].([]any)[0].(map[string]any), got["destinations"].([]any)[1].(map[string]any)
	if ga["weight"] != float64(5) || gb["weight"] != float64(95) || ga["url"] != "https://northpeak.example/a" || ga["id"] != a {
		t.Fatalf("applied: %v", got)
	}
	if ev := named(events(), "tracking_link.weights_updated"); len(ev) != 1 || ev[0].Props["source"] != "suggestion" || ev[0].Props["destinations"] != 2 {
		t.Fatalf("events: %v", ev)
	}
	for _, body := range []map[string]any{
		{"weights": []any{}},
		{"weights": []any{map[string]any{"destination_id": a, "weight": 10001}}},
		{"weights": []any{map[string]any{"destination_id": a, "weight": 1}, map[string]any{"destination_id": a, "weight": 2}}},
		{"weights": []any{map[string]any{"destination_id": x, "weight": 1}}}, // a path's: fine
		{"weights": []any{map[string]any{"destination_id": "00000000-0000-0000-0000-000000000000", "weight": 1}}},
		{"weights": []any{map[string]any{"destination_id": a, "weight": 1}}, "source": "robot"},
	} {
		want := 400
		if body["weights"] != nil && len(body["weights"].([]any)) == 1 && body["weights"].([]any)[0].(map[string]any)["destination_id"] == x {
			want = 200
		}
		if st, out := owner.do("PATCH", lpath+"/weights", body); st != want {
			t.Fatalf("PATCH %v = %d %v", body, st, out)
		}
	}

	// A link with a flow gets the flow's suggestions and sets the flow's
	// weights.
	f := owner.must(201, "POST", base+"/flows", map[string]any{"name": "Shared", "destinations": []any{
		map[string]any{"url": "https://northpeak.example/f1"}, map[string]any{"url": "https://northpeak.example/f2"}}})
	owner.must(200, "PUT", lpath+"/flow", map[string]any{"flow_id": f["id"]})
	fv := owner.must(200, "GET", lpath+"/weight-suggestions", nil)
	if fv["flow_id"] != f["id"] || len(fv["lists"].([]any)) != 1 || fv["lists"].([]any)[0].(map[string]any)["reason"] != "not_enough_clicks" {
		t.Fatalf("flow suggestions: %v", fv)
	}
	owner.must(409, "PATCH", lpath+"/weights", map[string]any{"weights": []any{map[string]any{"destination_id": a, "weight": 1}}})
	f1 := f["destinations"].([]any)[0].(map[string]any)["id"]
	fp := owner.must(200, "PATCH", base+"/flows/"+f["id"].(string)+"/weights", map[string]any{"weights": []any{map[string]any{"destination_id": f1, "weight": 300}}})
	if fp["destinations"].([]any)[0].(map[string]any)["weight"] != float64(300) {
		t.Fatalf("flow weights: %v", fp)
	}
	owner.must(200, "GET", base+"/flows/"+f["id"].(string)+"/weight-suggestions", nil)
	owner.must(400, "PATCH", base+"/flows/"+f["id"].(string)+"/weights", map[string]any{"weights": []any{map[string]any{"destination_id": a, "weight": 1}}})
	owner.must(404, "GET", base+"/flows/00000000-0000-0000-0000-000000000000/weight-suggestions", nil)
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
