package api_test

import (
	"context"
	"net/http/httptest"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

// Cost entered by hand (plans/ad-tracker.md, B6): an amount for a campaign
// and a period is shared out evenly over the campaign's clicks in it, to
// the millionth; a campaign with a cost model, or whose network reports
// its cost, is refused.
func TestTrackerCostUpdates(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	host := ownTrackingDomain(t, e, ws, brand, "go.northpeak.example")
	ts := owner.must(201, "POST", base+"/traffic-sources", map[string]any{"name": "Acme Push"})
	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Push", "channel": "paid_ads", "source": "acme", "redirect_links": true, "traffic_source": ts["id"]})
	l := owner.must(201, "POST", base+"/tracking-campaigns/"+c["code"].(string)+"/links", map[string]any{"url": "https://offers.northpeak.example/o1"})
	code := regexp.MustCompile(`/l/([a-z0-9]{8})$`).FindStringSubmatch(l["redirect_url"].(string))[1]
	// Another campaign's click never carries this one's cost.
	c2 := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Other", "channel": "paid_ads", "source": "acme", "redirect_links": true})
	l2 := owner.must(201, "POST", base+"/tracking-campaigns/"+c2["code"].(string)+"/links", map[string]any{"url": "https://offers.northpeak.example/o2"})
	code2 := regexp.MustCompile(`/l/([a-z0-9]{8})$`).FindStringSubmatch(l2["redirect_url"].(string))[1]
	click := func(code string) {
		t.Helper()
		req := httptest.NewRequest("GET", "/l/"+code, nil)
		req.Host = host
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if loc, err := url.Parse(rec.Header().Get("Location")); err != nil || loc.Host == "" {
			t.Fatalf("click: %d %v", rec.Code, rec.Header())
		}
	}
	for range 3 {
		click(code)
	}
	click(code2)
	if err := e.srv.FlushClicks(ctx); err != nil {
		t.Fatal(err)
	}
	e.runCRMImports(t)
	today := time.Now().UTC().Format(time.DateOnly)
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	path := base + "/tracker-cost-updates"
	one := func(u map[string]any) map[string]any { return map[string]any{"updates": []any{u}} }
	// wait polls an update until its job has run.
	wait := func(id string) map[string]any {
		t.Helper()
		for range 250 {
			for _, u := range owner.must(200, "GET", path, nil)["data"].([]any) {
				if u := u.(map[string]any); u["id"] == id && u["status"] != "queued" {
					return u
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("update %s never ran: %v", id, owner.must(200, "GET", path, nil))
		return nil
	}
	costs := func() []int64 {
		t.Helper()
		rows, err := e.pool.Query(ctx, `SELECT COALESCE(k.network_cost_micros, -1) FROM tracking_link_clicks k JOIN tracking_links l ON l.id = k.link_id
			WHERE l.campaign_id = $1 ORDER BY k.id`, c["id"])
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []int64
		for rows.Next() {
			var v int64
			if err := rows.Scan(&v); err != nil {
				t.Fatal(err)
			}
			out = append(out, v)
		}
		return out
	}

	// One unit over three clicks: a third each, and the millionth left over
	// on the first, so the total is exact.
	got := owner.must(202, "POST", path, one(map[string]any{"campaign_id": c["id"], "from": today, "to": today, "amount": "1"}))
	list := got["data"].([]any)
	if got["queued"] != true || len(list) != 1 || list[0].(map[string]any)["status"] != "queued" || list[0].(map[string]any)["amount"] != "1" {
		t.Fatalf("queued: %v", got)
	}
	done := wait(list[0].(map[string]any)["id"].(string))
	if done["status"] != "applied" || done["clicks"] != 3.0 || done["applied_at"] == nil || done["error"] != "" {
		t.Fatalf("applied: %v", done)
	}
	if v := costs(); len(v) != 3 || v[0] != 333_334 || v[1] != 333_333 || v[2] != 333_333 {
		t.Fatalf("shares: %v", v)
	}
	var other *int64
	if err := e.pool.QueryRow(ctx, `SELECT k.network_cost_micros FROM tracking_link_clicks k JOIN tracking_links l ON l.id = k.link_id WHERE l.campaign_id = $1`, c2["id"]).Scan(&other); err != nil || other != nil {
		t.Fatalf("another campaign's click got cost: %v %v", other, err)
	}
	// A later update for the same days replaces it; a number works as well
	// as a string.
	second := owner.must(202, "POST", path, one(map[string]any{"campaign_id": c["id"], "from": yesterday, "to": today, "amount": 0.3}))["data"].([]any)[0].(map[string]any)
	if u := wait(second["id"].(string)); u["status"] != "applied" || u["clicks"] != 3.0 {
		t.Fatalf("second: %v", u)
	}
	if v := costs(); v[0] != 100_000 || v[1] != 100_000 || v[2] != 100_000 {
		t.Fatalf("replaced shares: %v", v)
	}
	// Days with no clicks have nothing to carry the cost: the update fails
	// and says why.
	empty := owner.must(202, "POST", path, one(map[string]any{"campaign_id": c["id"], "from": yesterday, "to": yesterday, "amount": "5"}))["data"].([]any)[0].(map[string]any)
	if u := wait(empty["id"].(string)); u["status"] != "failed" || u["error"] == "" || u["clicks"] != 0.0 {
		t.Fatalf("no clicks: %v", u)
	}
	if v := costs(); v[0] != 100_000 {
		t.Fatalf("a failed update changed the clicks: %v", v)
	}
	// Clear puts the clicks back on the cost they carried themselves.
	clear := owner.must(202, "POST", path, one(map[string]any{"campaign_id": c["id"], "from": today, "to": today, "mode": "clear"}))["data"].([]any)[0].(map[string]any)
	if u := wait(clear["id"].(string)); u["status"] != "applied" || u["mode"] != "clear" {
		t.Fatalf("clear: %v", u)
	}
	if v := costs(); v[0] != -1 || v[1] != -1 || v[2] != -1 {
		t.Fatalf("cleared shares: %v", v)
	}

	// The list filters by campaign and status.
	if n := len(owner.must(200, "GET", path+"?status=failed", nil)["data"].([]any)); n != 1 {
		t.Fatalf("failed updates: %d", n)
	}
	if n := len(owner.must(200, "GET", path+"?campaign="+c2["id"].(string), nil)["data"].([]any)); n != 0 {
		t.Fatalf("the other campaign's updates: %d", n)
	}
	owner.must(400, "GET", path+"?status=maybe", nil)
	owner.must(400, "GET", path+"?campaign=nope", nil)

	// What is refused: nothing is queued when one update of a call is bad.
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format(time.DateOnly)
	long := time.Now().UTC().AddDate(0, 0, -31).Format(time.DateOnly)
	good := map[string]any{"campaign_id": c["id"], "from": today, "to": today, "amount": "1"}
	for name, bad := range map[string]map[string]any{
		"no campaign":     {"from": today, "to": today, "amount": "1"},
		"unknown":         {"campaign_id": "00000000-0000-0000-0000-000000000000", "from": today, "to": today, "amount": "1"},
		"bad day":         {"campaign_id": c["id"], "from": "yesterday", "to": today, "amount": "1"},
		"backwards":       {"campaign_id": c["id"], "from": today, "to": yesterday, "amount": "1"},
		"future":          {"campaign_id": c["id"], "from": today, "to": tomorrow, "amount": "1"},
		"too long":        {"campaign_id": c["id"], "from": long, "to": today, "amount": "1"},
		"no amount":       {"campaign_id": c["id"], "from": today, "to": today},
		"negative":        {"campaign_id": c["id"], "from": today, "to": today, "amount": "-1"},
		"mode":            {"campaign_id": c["id"], "from": today, "to": today, "amount": "1", "mode": "double"},
		"clear with cost": {"campaign_id": c["id"], "from": today, "to": today, "amount": "1", "mode": "clear"},
	} {
		if st, body := owner.do("POST", path, map[string]any{"updates": []any{good, bad}}); st != 400 {
			t.Errorf("%s: %d %v, want 400", name, st, body)
		}
	}
	owner.must(400, "POST", path, map[string]any{"updates": []any{}})
	before := len(owner.must(200, "GET", path, nil)["data"].([]any))
	// Another brand's campaign isn't this brand's.
	otherBrand := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Summitly"})
	owner.must(400, "POST", "/v1/brands/"+otherBrand["id"].(string)+"/tracker-cost-updates", one(good))
	// A campaign whose cost comes from a cost model, and one whose network
	// reports its cost for the days, are refused: never both.
	owner.must(200, "PATCH", base+"/tracking-campaigns/"+c2["code"].(string), map[string]any{"cost": map[string]any{"model": "cpc", "amount": "0.05"}})
	owner.must(409, "POST", path, one(map[string]any{"campaign_id": c2["id"], "from": today, "to": today, "amount": "1"}))
	if _, err := e.pool.Exec(ctx, `INSERT INTO tracker_network_costs (workspace_id, brand_id, source_id, network, campaign_ref, day, spend_micros)
		VALUES ($1, $2, $3, 'zeropark', 'net-1', $4, 1000000)`, ws, brand, ts["id"], today); err != nil {
		t.Fatal(err)
	}
	owner.must(409, "POST", path, one(good))
	if n := len(owner.must(200, "GET", path, nil)["data"].([]any)); n != before {
		t.Fatalf("refused updates were stored: %d, want %d", n, before)
	}

	var updated, applied, failed int
	for _, ev := range events() {
		switch ev.Name {
		case "tracking_campaign.cost_updated":
			updated++
		case "tracking_campaign.cost_update_applied":
			applied++
		case "tracking_campaign.cost_update_failed":
			failed++
		}
	}
	if updated != 4 || applied != 3 || failed != 1 {
		t.Errorf("events: %d updated, %d applied, %d failed; want 4, 3, 1", updated, applied, failed)
	}
}
