package api_test

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Rows that share a timestamp are all reached when a list is paged (one
// call's cost updates, an upload's conversions), and a later cost update
// for the same days wins whatever order the jobs run in.
func TestTrackerListsPageEveryRowAndCostUpdatesKeepOrder(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	ts := owner.must(201, "POST", base+"/traffic-sources", map[string]any{"name": "Acme Push"})
	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Push", "channel": "paid_ads", "source": "acme", "redirect_links": true, "traffic_source": ts["id"]})
	l := owner.must(201, "POST", base+"/tracking-campaigns/"+c["id"].(string)+"/links", map[string]any{"url": "https://offers.northpeak.example/o1"})
	src := owner.must(201, "POST", base+"/postback-sources", map[string]any{"name": "Network"})
	now := time.Now().UTC()
	var clickID int64
	for i := range 4 {
		if err := e.pool.QueryRow(ctx, `INSERT INTO tracking_link_clicks (workspace_id, brand_id, link_id, click_key, clicked_at, ip_hash)
			VALUES ($1, $2, $3, $4, $5, $4) RETURNING id`, ws, brand, l["id"], fmt.Sprintf("dddddddddddddd%02d", i), now.Add(-time.Minute)).Scan(&clickID); err != nil {
			t.Fatal(err)
		}
	}
	today := now.Format(time.DateOnly)
	path := base + "/tracker-cost-updates"
	update := func(from, to, amount string) map[string]any {
		return map[string]any{"campaign_id": c["id"], "from": from, "to": to, "amount": amount}
	}
	status := func(id string) (st, why string) {
		t.Helper()
		if err := e.pool.QueryRow(ctx, `SELECT status, error FROM tracker_cost_updates WHERE id = $1`, id).Scan(&st, &why); err != nil {
			t.Fatal(err)
		}
		return st, why
	}
	// apply runs an update's job here (a worker may have run it already).
	apply := func(id string) {
		t.Helper()
		if err := e.srv.RunCostUpdate(ctx, ws, id); err != nil {
			t.Fatalf("cost update job: %v", err)
		}
	}
	cost := func() (sum int64) {
		t.Helper()
		if err := e.pool.QueryRow(ctx, `SELECT COALESCE(sum(network_cost_micros), 0) FROM tracking_link_clicks WHERE workspace_id = $1 AND link_id = $2`, ws, l["id"]).Scan(&sum); err != nil {
			t.Fatal(err)
		}
		return sum
	}

	// One call can't name a campaign's day twice: there is no order to
	// apply the two amounts in.
	owner.must(400, "POST", path, map[string]any{"updates": []any{update(today, today, "1"), update(today, today, "2")}})
	yesterday := now.AddDate(0, 0, -1).Format(time.DateOnly)
	owner.must(400, "POST", path, map[string]any{"updates": []any{update(yesterday, today, "1"), update(today, today, "2")}})

	// A correction after a mistake: the later one stands, also when the
	// earlier one's job runs last.
	first := owner.must(202, "POST", path, map[string]any{"updates": []any{update(today, today, "1")}})["data"].([]any)[0].(map[string]any)["id"].(string)
	apply(first)
	second := owner.must(202, "POST", path, map[string]any{"updates": []any{update(today, today, "2")}})["data"].([]any)[0].(map[string]any)["id"].(string)
	apply(second)
	if got := cost(); got != 2_000000 {
		t.Fatalf("cost after the correction = %d", got)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE tracker_cost_updates SET status = 'queued', applied_at = NULL WHERE id = $1`, first); err != nil {
		t.Fatal(err)
	}
	apply(first)
	if st, why := status(first); st != "failed" || why != "a later update for the same days replaced this one before it was applied" {
		t.Fatalf("the earlier update, run late: %s %q", st, why)
	}
	if got := cost(); got != 2_000000 {
		t.Fatalf("an earlier update overwrote a later one: %d", got)
	}

	// One call's updates share a created_at; paging two at a time still
	// reaches each of them once.
	var many []any
	for i := 2; i <= 6; i++ {
		day := now.AddDate(0, 0, -i).Format(time.DateOnly)
		many = append(many, update(day, day, "1"))
	}
	owner.must(202, "POST", path, map[string]any{"updates": many})
	seen := map[string]int{}
	cursor, pages := "", 0
	for {
		r := owner.must(200, "GET", path+"?limit=2"+cursor, nil)
		for _, u := range r["data"].([]any) {
			seen[u.(map[string]any)["id"].(string)]++
		}
		pages++
		next, _ := r["next_cursor"].(string)
		if next == "" || pages > 10 {
			break
		}
		cursor = "&cursor=" + next
	}
	if len(seen) != 7 || pages != 4 {
		t.Fatalf("cost updates reached = %d over %d pages, want 7 over 4: %v", len(seen), pages, seen)
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("update %s listed %d times", id, n)
		}
	}

	// 130 conversions with one converted_at (an upload): the second page
	// holds the 30 the first couldn't.
	if _, err := e.pool.Exec(ctx, `INSERT INTO tracker_conversions (workspace_id, brand_id, source_id, click_id, click_key, campaign_id, link_id, clicked_at,
			transaction_id, status, payout_micros, currency, converted_at)
		SELECT $1, $2, $3, $4, 'dddddddddddddd03', $5, $6, $7::timestamptz, 'tx-' || g, 'approved', 1000000, 'USD', $7::timestamptz
		FROM generate_series(1, 130) g`, ws, brand, src["id"], clickID, c["id"], l["id"], now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	page1 := owner.must(200, "GET", base+"/tracker-conversions", nil)
	next, _ := page1["next_cursor"].(string)
	if len(page1["data"].([]any)) != 100 || next == "" {
		t.Fatalf("first page: %d rows, cursor %q", len(page1["data"].([]any)), next)
	}
	page2 := owner.must(200, "GET", base+"/tracker-conversions?cursor="+next, nil)
	ids := map[string]bool{}
	for _, p := range []map[string]any{page1, page2} {
		for _, x := range p["data"].([]any) {
			ids[x.(map[string]any)["id"].(string)] = true
		}
	}
	if len(page2["data"].([]any)) != 30 || page2["next_cursor"] != nil || len(ids) != 130 {
		t.Fatalf("second page: %d rows, %d distinct in all, cursor %v", len(page2["data"].([]any)), len(ids), page2["next_cursor"])
	}
	// The older ?before= still answers, by time alone.
	owner.must(200, "GET", base+"/tracker-conversions?before="+now.Format(time.RFC3339), nil)
	owner.must(400, "GET", base+"/tracker-conversions?cursor=nonsense", nil)
}
