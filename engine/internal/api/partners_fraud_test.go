package api_test

import (
	"context"
	"net/url"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// The partner fraud review queue (plans/ad-tracker.md, B7d): a credited
// conversion with a partner signal is flagged, and its rewards wait for a
// person even past their hold and in a program that approves on its own.
// Clearing every flag lets them go on; confirming rejects them and can
// suspend the partner.
func TestPartnerFraudReview(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT id::text FROM brands WHERE workspace_id = $1 AND handle = 'northpeak'`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Northpeak affiliates", "kind": "affiliate", "brand": "northpeak",
		"landing_url": "https://northpeak.example/pricing", "hold_days": 0, "auto_approve": true})
	// The partner is paid to the address the customer later signs up with.
	ana := owner.must(201, "POST", base+"/partners", map[string]any{"program": "northpeak-affiliates", "email": "ana@example.com",
		"payout_method": "PayPal: bo@example.com"})
	ben := owner.must(201, "POST", base+"/partners", map[string]any{"program": "northpeak-affiliates", "email": "ben@example.com"})
	click := func(code string) string {
		t.Helper()
		_, loc := owner.rawUA("GET", "/r/"+ws+"/"+code, "Mozilla/5.0")
		u, _ := url.Parse(loc)
		return u.Query().Get("ref_click")
	}
	conv := base + "/partner-conversions?brand=northpeak"
	flags := "/v1/brands/" + brand + "/partner-flags"

	// A sale seconds after the click, by a customer whose address is in the
	// partner's payout details: two flags, and the reward waits though the
	// program approves at once.
	sale := owner.must(201, "POST", conv, map[string]any{"type": "sale", "customer_id": "u-1", "email": "bo@example.com", "amount_cents": 10000,
		"click_id": click(ana["code"].(string))})
	rw := sale["rewards"].([]any)[0].(map[string]any)
	if sale["credited"] != true || rw["status"] != "pending" {
		t.Fatalf("flagged sale: %v", sale)
	}
	open := owner.must(200, "GET", flags+"?status=open", nil)["data"].([]any)
	signals := map[string]string{}
	for _, f := range open {
		f := f.(map[string]any)
		signals[f["signal"].(string)] = f["id"].(string)
		if f["held_cents"] != float64(2000) || f["partner_id"] != ana["id"] {
			t.Fatalf("flag: %v", f)
		}
	}
	if len(signals) != 2 || signals["payout_matches_customer"] == "" || signals["fast_conversion"] == "" {
		t.Fatalf("signals: %v", open)
	}
	if err := e.srv.ApproveDuePartnerRewards(ctx); err != nil {
		t.Fatal(err)
	}
	reward := func(id string) string {
		t.Helper()
		var st string
		if err := e.pool.QueryRow(ctx, `SELECT status FROM partner_rewards WHERE id = $1`, id).Scan(&st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	if st := reward(rw["id"].(string)); st != "pending" {
		t.Fatalf("a flagged reward was approved: %s", st)
	}
	// Clearing one flag isn't enough; clearing both lets it go on.
	owner.must(200, "POST", flags+"/"+signals["fast_conversion"]+"/clear", map[string]any{"note": "a returning customer"})
	if err := e.srv.ApproveDuePartnerRewards(ctx); err != nil {
		t.Fatal(err)
	}
	if st := reward(rw["id"].(string)); st != "pending" {
		t.Fatalf("approved with a flag still open: %s", st)
	}
	owner.must(400, "POST", flags+"/"+signals["fast_conversion"]+"/clear", map[string]any{})
	cleared := owner.must(200, "POST", flags+"/"+signals["payout_matches_customer"]+"/clear", map[string]any{})
	if cleared["status"] != "cleared" || cleared["resolved_at"] == nil {
		t.Fatalf("cleared: %v", cleared)
	}
	if err := e.srv.ApproveDuePartnerRewards(ctx); err != nil {
		t.Fatal(err)
	}
	if st := reward(rw["id"].(string)); st != "approved" {
		t.Fatalf("after clearing every flag: %s", st)
	}

	// Confirming rejects the pending reward and can suspend the partner.
	sale2 := owner.must(201, "POST", conv, map[string]any{"type": "sale", "customer_id": "u-2", "email": "cy@example.com", "amount_cents": 5000,
		"click_id": click(ben["code"].(string))})
	rw2 := sale2["rewards"].([]any)[0].(map[string]any)["id"].(string)
	list := owner.must(200, "GET", flags+"?status=open&partner_id="+ben["id"].(string), nil)["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["signal"] != "fast_conversion" {
		t.Fatalf("ben's flags: %v", list)
	}
	confirmed := owner.must(200, "POST", flags+"/"+list[0].(map[string]any)["id"].(string)+"/confirm", map[string]any{"note": "a bot", "suspend_partner": true})
	if confirmed["status"] != "confirmed" || confirmed["held_cents"] != float64(0) {
		t.Fatalf("confirmed: %v", confirmed)
	}
	if st := reward(rw2); st != "rejected" {
		t.Fatalf("a confirmed sale's reward: %s", st)
	}
	var status string
	if err := e.pool.QueryRow(ctx, `SELECT status FROM partner_memberships WHERE id = $1`, ben["id"]).Scan(&status); err != nil || status != "suspended" {
		t.Fatalf("ben: %s %v", status, err)
	}

	// The queue is the brand's own; filters are checked.
	owner.must(400, "GET", flags+"?status=maybe", nil)
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "handle": "acme"})
	if n := len(owner.must(200, "GET", "/v1/brands/"+other["id"].(string)+"/partner-flags", nil)["data"].([]any)); n != 0 {
		t.Fatalf("another brand sees %d flags", n)
	}
	owner.must(404, "POST", "/v1/brands/"+other["id"].(string)+"/partner-flags/"+signals["fast_conversion"]+"/clear", map[string]any{})

	names := map[string]int{}
	for _, ev := range got() {
		names[ev.Name]++
	}
	if names["partner_flag.raised"] != 3 || names["partner_flag.cleared"] != 2 || names["partner_flag.confirmed"] != 1 {
		t.Fatalf("events: raised %d, cleared %d, confirmed %d", names["partner_flag.raised"], names["partner_flag.cleared"], names["partner_flag.confirmed"])
	}
}
