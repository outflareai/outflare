package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/adsuggest"
)

// Per-target results (plans/ad-tracker.md, B8): each target of a Zeropark
// campaign with the tracker's clicks, cost, conversions, revenue and ROI,
// the campaign as Paid ads knows it, and the target's state as it is
// paused and resumed through the exclusions endpoint.
func TestNetworkTargets(t *testing.T) {
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
	const cid = "11111111-1111-4111-8111-111111111111"

	ts := owner.must(201, "POST", base+"/traffic-sources", map[string]any{"template": "zeropark"})
	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Zeropark push", "channel": "paid_ads", "source": "zeropark",
		"redirect_links": true, "traffic_source": ts["id"]})
	l := owner.must(201, "POST", base+"/tracking-campaigns/"+c["code"].(string)+"/links", map[string]any{"url": "https://offers.northpeak.example/o1"})
	code := regexp.MustCompile(`/l/([a-z0-9]{8})$`).FindStringSubmatch(l["redirect_url"].(string))[1]
	src := owner.must(201, "POST", base+"/postback-sources", map[string]any{"name": "Acme Network"})
	token := strings.TrimPrefix(strings.SplitN(src["postback_url"].(string), "?", 2)[0], "https://"+host)
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Host = host
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	click := func(target, cost string) string {
		t.Helper()
		q := url.Values{"cid": {"zp-click"}, "campaign_id": {cid}, "cost": {cost}, "target": {target}}
		loc, _ := url.Parse(get("/l/" + code + "?" + q.Encode()).Header().Get("Location"))
		return loc.Query().Get("ofclk")
	}
	// pub-a: two clicks at 0.02 and a 1.50 sale; pub-b: one click at 0.05
	// and nothing back.
	k := click("pub-a", "0.02")
	click("pub-a", "0.02")
	click("pub-b", "0.05")
	if err := e.srv.FlushClicks(ctx); err != nil {
		t.Fatal(err)
	}
	if rec := get(token + "?cid=" + k + "&payout=1.5&txid=T-1"); rec.Code != 200 {
		t.Fatalf("postback: %d %s", rec.Code, rec.Body)
	}

	rows := func() map[string]map[string]any {
		t.Helper()
		out := map[string]map[string]any{}
		for _, r := range owner.must(200, "GET", base+"/network-targets", nil)["data"].([]any) {
			out[r.(map[string]any)["target"].(string)] = r.(map[string]any)
		}
		return out
	}
	// Before Zeropark is connected: the tracker's own numbers, the cost
	// from each click's token, and no campaign to change.
	got := rows()
	a, b := got["pub-a"], got["pub-b"]
	if len(got) != 2 || a["clicks"] != float64(2) || a["conversions"] != float64(1) || a["cost"] != "0.04" || a["revenue"] != "1.5" || a["profit"] != "1.46" ||
		a["roi"].(float64) < 36.4 || a["roi"].(float64) > 36.6 || a["ad_campaign_id"] != nil || a["state"] != "active" || a["campaign_ref"] != cid {
		t.Fatalf("pub-a: %v", a)
	}
	if b["clicks"] != float64(1) || b["conversions"] != float64(0) || b["cost"] != "0.05" || b["profit"] != "-0.05" || b["roi"] != float64(-1) {
		t.Fatalf("pub-b: %v", b)
	}
	// Most costly first.
	if first := owner.must(200, "GET", base+"/network-targets", nil)["data"].([]any)[0].(map[string]any); first["target"] != "pub-b" {
		t.Fatalf("order: %v", first)
	}
	owner.must(400, "GET", base+"/network-targets?from=yesterday", nil)
	if n := len(owner.must(200, "GET", base+"/network-targets?campaign_ref=other", nil)["data"].([]any)); n != 0 {
		t.Fatalf("another campaign: %d rows", n)
	}

	// Connected and synced: the row names the campaign, and the target's
	// state follows its exclusion changes.
	zeroparkFake.campaign(cid, "Push US")
	i := owner.must(201, "POST", "/v1/workspaces/"+ws+"/integrations", map[string]any{"connector": "zeropark_ads", "credentials": map[string]string{"api_token": "test-good"}})
	if _, err := e.ads.SyncOnce(ctx, ws, i["id"].(string)); err != nil {
		t.Fatal(err)
	}
	b = rows()["pub-b"]
	camp, _ := b["ad_campaign_id"].(string)
	if camp == "" || b["campaign_name"] != "Push US" {
		t.Fatalf("the campaign in Paid ads: %v", b)
	}
	run := &adsuggest.Runner{Pool: e.pool, Store: e.integrations, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	change := func(action string) {
		t.Helper()
		res := owner.must(202, "POST", base+"/ads/campaigns/"+camp+"/exclusions", map[string]any{"kind": "placement", "value": "pub-b", "action": action})
		want := map[string]string{"add": "pausing", "remove": "resuming"}[action]
		if st := rows()["pub-b"]["state"]; st != want {
			t.Fatalf("after asking to %s: %v", action, st)
		}
		if err := run.ApplyFindingChange(ctx, ws, res["change"].(map[string]any)["id"].(string), false); err != nil {
			t.Fatal(err)
		}
	}
	change("add")
	if got := rows(); got["pub-b"]["state"] != "paused" || got["pub-a"]["state"] != "active" {
		t.Fatalf("after the pause: %v / %v", got["pub-b"]["state"], got["pub-a"]["state"])
	}
	change("remove")
	if st := rows()["pub-b"]["state"]; st != "active" {
		t.Fatalf("after the resume: %v", st)
	}
	// Nothing read from the network yet: no bid, and no time it was seen.
	if b := rows()["pub-b"]; b["bid"] != nil || b["seen_at"] != nil || b["auto_bid"] != false || b["bid_queued"] != nil {
		t.Fatalf("before the network was read: %v", b)
	}

	// The cost sync also reads each target's state and bid. pub-a goes by
	// its address there; pub-b was paused in Zeropark's own panel, and
	// Zeropark holds pub-c back.
	click("pub-c", "0.01")
	if err := e.srv.FlushClicks(ctx); err != nil {
		t.Fatal(err)
	}
	zeroparkFake.set(cid, []map[string]any{
		{"id": "aaaaaaaa-0000-4000-8000-000000000001", "target": "site-a", "address": "pub-a", "stats": map[string]any{"spent": 0.04, "redirects": 2}},
		{"id": "aaaaaaaa-0000-4000-8000-000000000002", "target": "pub-b", "stats": map[string]any{"spent": 0.05, "redirects": 1}},
		{"id": "aaaaaaaa-0000-4000-8000-000000000003", "target": "pub-c", "stats": map[string]any{"spent": 0.01, "redirects": 1}},
	})
	zeroparkFake.setBid(cid, "site-a", 0.02)
	zeroparkFake.setState(cid, "pub-b", "PAUSED")
	zeroparkFake.setState(cid, "pub-c", "OVER_BUDGET")
	sync := func() {
		t.Helper()
		if _, err := e.srv.SyncNetworkCosts(ctx); err != nil {
			t.Fatal(err)
		}
	}
	sync()
	got = rows()
	a, b = got["pub-a"], got["pub-b"]
	if a["state"] != "active" || a["bid"] != "0.02" || a["min_bid"] != "0.0005" || a["auto_bid"] != false || a["seen_at"] == nil {
		t.Fatalf("pub-a as Zeropark lists it: %v", a)
	}
	if b["state"] != "paused" || b["bid"] != "0.01" {
		t.Fatalf("pub-b, paused in Zeropark's panel: %v", b)
	}
	if c := got["pub-c"]; c["state"] != "stopped" || c["state_detail"] != "over_budget" {
		t.Fatalf("pub-c, held back by Zeropark: %v", c)
	}
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM tracker_network_targets WHERE brand_id = $1 AND campaign_ref = $2`, brand, cid).Scan(&n); err != nil || n != 3 {
		t.Fatalf("stored targets: %d %v", n, err)
	}

	// A bid change shows as queued, then as the bid, before the network is
	// read again; after that the network's own figure shows.
	res := owner.must(202, "POST", base+"/ads/campaigns/"+camp+"/bid", map[string]any{"bid_micros": 15000, "placement": "pub-a"})
	if a := rows()["pub-a"]; a["bid"] != "0.02" || a["bid_queued"] != "0.015" {
		t.Fatalf("a bid change that waits: %v", a)
	}
	if err := run.ApplyFindingChange(ctx, ws, res["change"].(map[string]any)["id"].(string), false); err != nil {
		t.Fatal(err)
	}
	if a := rows()["pub-a"]; a["bid"] != "0.015" || a["bid_queued"] != nil {
		t.Fatalf("an applied bid change: %v", a)
	}
	zeroparkFake.setBid(cid, "site-a", 0.03) // someone raised it in the panel
	sync()
	if a := rows()["pub-a"]; a["bid"] != "0.03" {
		t.Fatalf("the network's bid after it was read again: %v", a)
	}
	// Resuming the target paused in the panel: resuming, then active; and
	// the next read agrees.
	change("remove")
	if st := rows()["pub-b"]["state"]; st != "active" {
		t.Fatalf("after resuming the target paused in the panel: %v", st)
	}
	sync()
	if st := rows()["pub-b"]["state"]; st != "active" {
		t.Fatalf("after the next read: %v", st)
	}
	// A target the network stops listing keeps its row for 90 days.
	if _, err := e.pool.Exec(ctx, `UPDATE tracker_network_targets SET seen_at = now() - interval '91 days' WHERE brand_id = $1 AND 'pub-c' = ANY(keys)`, brand); err != nil {
		t.Fatal(err)
	}
	zeroparkFake.set(cid, nil)
	sync()
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM tracker_network_targets WHERE brand_id = $1`, brand).Scan(&n); err != nil || n != 2 {
		t.Fatalf("after the old target was dropped: %d %v", n, err)
	}
}
