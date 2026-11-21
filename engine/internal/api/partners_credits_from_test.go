package api_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestPartnerCreditsFrom(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	brand := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Northpeak"})["id"].(string)
	base := "/v1/brands/" + brand
	ctx := context.Background()
	cutoff := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Microsecond)
	stamp := cutoff.Format(time.RFC3339Nano)
	prog := owner.must(201, "POST", base+"/partner-programs", map[string]any{
		"name": "Affiliates", "kind": "affiliate", "credits_from": stamp, "auto_approve": true,
		"rules": []map[string]any{
			{"trigger": "signup", "kind": "fixed", "amount_cents": 100},
			{"trigger": "sale", "kind": "percent", "percent_bp": 2000},
			{"trigger": "trial_started", "kind": "fixed", "amount_cents": 100},
		},
	})
	programURL := base + "/partner-programs/affiliates"
	for _, p := range []map[string]any{
		prog,
		owner.must(200, "GET", programURL, nil),
		owner.must(200, "GET", base+"/partner-programs", nil)["data"].([]any)[0].(map[string]any),
		owner.must(200, "PATCH", programURL, map[string]any{"hold_days": 0}),
	} {
		value, _ := p["credits_from"].(string)
		got, err := time.Parse(time.RFC3339Nano, value)
		if err != nil || !got.Equal(cutoff) {
			t.Fatalf("credits_from = %v, want %s", p["credits_from"], stamp)
		}
	}
	for _, bad := range []any{"yesterday", "2026-11-01", "2026-11-01T00:00:00", "", 42} {
		owner.must(400, "PATCH", programURL, map[string]any{"credits_from": bad})
		owner.must(400, "POST", base+"/partner-programs", map[string]any{"name": "Invalid", "kind": "affiliate", "credits_from": bad})
	}
	other := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Acme"})["id"].(string)
	owner.must(404, "PATCH", "/v1/brands/"+other+"/partner-programs/"+prog["id"].(string), map[string]any{"credits_from": nil})
	partner := owner.must(201, "POST", base+"/partners", map[string]any{"program": "affiliates", "email": "ana@example.com", "code": "ana"})
	owner.must(201, "POST", base+"/partners/"+partner["id"].(string)+"/promo-codes", map[string]any{"code": "ANA10"})
	customer := func(id string) {
		t.Helper()
		owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "identities": []map[string]any{
			{"namespace": "user_id", "value": id, "verified": true},
		}})
	}

	// The same gate applies with an explicit program, an inferred program,
	// and a promo code. No early conversion may bind the customer.
	for _, typ := range []string{"sale", "signup", "lead", "custom"} {
		for _, when := range []struct {
			name string
			at   time.Time
		}{{"before", cutoff.Add(-time.Microsecond)}, {"at", cutoff}, {"after", cutoff.Add(time.Microsecond)}} {
			t.Run(typ+"/"+when.name, func(t *testing.T) {
				body := map[string]any{"type": typ, "customer_id": typ + "-" + when.name, "external_id": typ + "-" + when.name,
					"ref": "ana", "occurred_at": when.at.Format(time.RFC3339Nano)}
				customer(body["customer_id"].(string))
				if typ == "sale" {
					body["amount_cents"], body["promo_code"] = 1000, "ANA10"
					delete(body, "ref")
				}
				if typ == "custom" {
					body["event_name"], body["program"] = "trial_started", prog["id"]
				}
				metricType := typ
				if typ == "lead" {
					metricType = "signup"
				}
				labels := map[string]string{"type": metricType, "source": "api", "outcome": "not_credited"}
				beforeMetric := metricValue(t, "outflare_partners_conversions_total", labels)
				beforeRewards := len(named(events(), "partner_reward.created"))
				beforeReferrals := len(named(events(), "partner_referral.created"))
				status := 201
				if when.name == "before" {
					status = 200
				}
				res := owner.must(status, "POST", base+"/partner-conversions", body)
				if when.name == "before" {
					if res["credited"] != false || res["reason"] != "before_credits_from" || res["conversion"] != nil || res["referral_id"] != nil || len(res["rewards"].([]any)) != 0 {
						t.Fatalf("early conversion = %v", res)
					}
					if len(named(events(), "partner_reward.created")) != beforeRewards || len(named(events(), "partner_referral.created")) != beforeReferrals {
						t.Fatal("early conversion emitted a referral or reward event")
					}
					if delta := metricValue(t, "outflare_partners_conversions_total", labels) - beforeMetric; delta != 1 {
						t.Fatalf("not_credited metric increased by %v", delta)
					}
					var n int
					if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM partner_referrals f JOIN record_identities i ON i.record_id = f.record_id
						WHERE f.workspace_id = $1 AND f.brand_id = $2 AND i.namespace = 'user_id' AND i.value = $3`, ws, brand, body["customer_id"]).Scan(&n); err != nil || n != 0 {
						t.Fatalf("early referral count = %d, %v", n, err)
					}
				} else if res["credited"] != true || len(res["rewards"].([]any)) != 1 {
					t.Fatalf("qualifying conversion = %v", res)
				}
			})
		}
	}
	log := owner.must(200, "GET", base+"/partner-conversion-log?outcome=not_credited", nil)["data"].([]any)
	if len(log) != 4 {
		t.Fatalf("uncredited calls = %d, want 4", len(log))
	}
	for _, entry := range log {
		if entry.(map[string]any)["reason"] != "before_credits_from" {
			t.Fatalf("conversion log = %v", entry)
		}
	}

	// A sale that was not credited has nothing to reverse. Moving the start
	// time forward leaves past rewards intact, and refunds still reverse them.
	if res := owner.must(200, "POST", base+"/partner-conversions", map[string]any{"type": "refund", "program": prog["id"], "refund_of": "sale-before"}); res["reason"] != "sale_not_found" {
		t.Fatalf("refund of uncredited sale = %v", res)
	}
	owner.must(200, "PATCH", programURL, map[string]any{"credits_from": time.Now().UTC().Format(time.RFC3339Nano)})
	res := owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "refund", "program": prog["id"], "refund_of": "sale-at", "occurred_at": stamp})
	if res["credited"] != true || res["rewards"].([]any)[0].(map[string]any)["amount_cents"] != -200.0 {
		t.Fatalf("refund before new start time = %v", res)
	}
	if p := owner.must(200, "PATCH", programURL, map[string]any{"credits_from": nil}); p["credits_from"] != nil {
		t.Fatalf("cleared credits_from = %v", p["credits_from"])
	}
	if res := owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "sale", "ref": "ana", "customer_id": "sale-before", "external_id": "sale-before", "amount_cents": 1000, "occurred_at": cutoff.Add(-time.Microsecond).Format(time.RFC3339Nano)}); res["credited"] != true {
		t.Fatalf("cleared cutoff = %v", res)
	}
	for _, slug := range []string{"omitted", "null"} {
		body := map[string]any{"name": slug, "kind": "affiliate"}
		if slug == "null" {
			body["credits_from"] = nil
		}
		p := owner.must(201, "POST", base+"/partner-programs", body)
		if p["credits_from"] != nil {
			t.Fatalf("default credits_from = %v", p["credits_from"])
		}
		owner.must(201, "POST", base+"/partners", map[string]any{"program": slug, "email": slug + "@example.com", "code": slug})
		customer("old-" + slug)
		if res := owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "sale", "ref": slug, "customer_id": "old-" + slug, "amount_cents": 1000, "occurred_at": stamp}); res["credited"] != true {
			t.Fatalf("default program = %v", res)
		}
	}
}

// Wait for the real workers, so a missing reward cannot pass merely because
// the payment or event has not been processed yet.
func waitPartnerCreditJobs(t *testing.T, e *env, ws string, minimum int) {
	t.Helper()
	var total, unfinished, dead int
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); {
		if err := e.pool.QueryRow(context.Background(), `SELECT count(*), count(*) FILTER (WHERE status <> 'succeeded'), count(*) FILTER (WHERE status = 'dead')
			FROM jobs WHERE workspace_id = $1 AND kind IN ('partners.billing_conversion', 'partners.event_conversion')`, ws).Scan(&total, &unfinished, &dead); err != nil {
			t.Fatal(err)
		}
		if dead > 0 {
			t.Fatal("a partner conversion job failed permanently")
		}
		if total >= minimum && unfinished == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("partner conversion jobs did not finish: total=%d, want at least %d, unfinished=%d", total, minimum, unfinished)
}

func TestPartnerCreditsFromEventsAndTransactions(t *testing.T) {
	events := track.Capture(t)
	te := newTrailEnv(t)
	e, owner, ws, base := te.e, te.owner, te.ws, te.base
	cutoff := time.Now().UTC().Add(-time.Hour).Truncate(time.Second).Add(123456 * time.Microsecond)
	stamp, before := cutoff.Format(time.RFC3339Nano), cutoff.Add(-time.Microsecond).Format(time.RFC3339Nano)
	owner.must(200, "PATCH", base+"/partner-programs/affiliates", map[string]any{"credits_from": stamp, "sales_source": "transactions"})
	owner.must(200, "PUT", base+"/partner-programs/affiliates/rules", map[string]any{"rules": []map[string]any{
		{"trigger": "sale", "kind": "percent", "percent_bp": 2000}, {"trigger": "signup", "kind": "fixed", "amount_cents": 100},
		{"trigger": "trial_started", "kind": "fixed", "amount_cents": 100},
	}})
	te.partner("ana", "Ana")
	for _, user := range []string{"bound", "late"} {
		owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "identities": []map[string]any{
			{"namespace": "user_id", "value": user, "verified": true},
		}})
	}
	owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "signup", "customer_id": "bound", "ref": "ana", "occurred_at": stamp})
	beforeRewards := len(named(events(), "partner_reward.created"))
	pay := func(user, id, at string) {
		t.Helper()
		owner.must(201, "POST", base+"/transactions", map[string]any{"kind": "payment", "user_id": user, "source_id": id, "amount_minor": 1000, "currency": "USD", "occurred_at": at})
	}
	pay("bound", "old-transaction", before)
	// The early sign-up leaves the record unbound; its payment arrives before
	// its qualifying sign-up. The requeued payment is still before the cutoff.
	owner.must(200, "POST", base+"/partner-conversions", map[string]any{"type": "signup", "customer_id": "late", "ref": "ana", "occurred_at": before})
	pay("late", "old-requeued", before)
	e.runCRMImports(t)
	waitPartnerCreditJobs(t, e, ws, 2)
	owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "signup", "customer_id": "late", "ref": "ana", "occurred_at": stamp})
	for _, input := range []map[string]any{
		{"type": "track", "userId": "bound", "event": "Order Completed", "properties": map[string]any{"order_id": "old-order", "revenue": 20, "currency": "USD"}},
		{"type": "track", "userId": "old-signup", "event": "Signed Up", "properties": map[string]any{"ref": "ana"}},
		{"type": "identify", "userId": "old-identify", "properties": map[string]any{"ref": "ana"}, "traits": map[string]any{"created_at": before}},
		{"type": "track", "userId": "bound", "event": "Trial Started"},
	} {
		input["timestamp"] = before
		owner.must(202, "POST", base+"/events", input)
	}
	waitPartnerCreditJobs(t, e, ws, 7)
	var sales, referrals int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE type = 'sale') FROM partner_conversions WHERE workspace_id = $1 AND program_id = $2`, ws, te.program).Scan(&sales); err != nil || sales != 0 {
		t.Fatalf("early sales = %d, %v", sales, err)
	}
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM partner_referrals WHERE workspace_id = $1 AND program_id = $2`, ws, te.program).Scan(&referrals); err != nil || referrals != 2 {
		t.Fatalf("referrals = %d, want only the two qualifying sign-ups: %v", referrals, err)
	}
	if n := len(named(events(), "partner_reward.created")); n != beforeRewards+1 {
		t.Fatalf("rewards = %d, want only qualifying sign-ups", n)
	}
	// The fractional-second boundary survives both job payload paths.
	pay("bound", "at-cutoff", stamp)
	owner.must(202, "POST", base+"/events", map[string]any{"type": "track", "userId": "at-signup", "event": "Signed Up", "timestamp": stamp, "properties": map[string]any{"ref": "ana"}})
	waitPartnerCreditJobs(t, e, ws, 9)
	if n := len(named(events(), "partner_reward.created")); n != beforeRewards+3 {
		t.Fatalf("boundary rewards = %d, want %d", n, beforeRewards+3)
	}
}

func TestPartnerCreditsFromBrowserAndBilling(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	e.integrations.Client.BaseURLs["stripe"] = fakeStripe(t).URL
	owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate", "landing_url": "https://northpeak.example/", "sales_source": "stripe"})
	owner.must(201, "POST", base+"/partners", map[string]any{"program": "affiliates", "email": "ana@example.com", "code": "ana"})
	owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "signup", "customer_id": "u_ann", "email": "ann@acme.example", "ref": "ana"})
	owner.must(200, "PUT", base+"/partner-programs/affiliates/rules", map[string]any{"rules": []map[string]any{
		{"trigger": "signup", "kind": "fixed", "amount_cents": 100}, {"trigger": "sale", "kind": "percent", "percent_bp": 2000},
	}})
	owner.must(200, "PATCH", base+"/partner-programs/affiliates", map[string]any{"credits_from": time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), "pay_browser_signups": true})
	key := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "northpeak.example", "origins": []string{"https://northpeak.example"}})["key"]
	if _, err := e.pool.Exec(ctx, `INSERT INTO site_sessions (workspace_id, anonymous_id, started_at, last_seen_at, ref, landing_url)
		VALUES ($1, 'before-start-visitor', now() - interval '1 minute', now(), 'ana', 'https://northpeak.example/')`, ws); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"key": key, "anonymousId": "before-start-visitor", "email": "browser@example.com"})
	req := httptest.NewRequest("POST", "/v1/site/signup", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Origin", "https://northpeak.example")
	res := httptest.NewRecorder()
	e.h.ServeHTTP(res, req)
	if res.Code != 200 {
		t.Fatalf("browser signup = %d", res.Code)
	}
	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "stripe", "credentials": map[string]string{"api_key": "rk_test_key"}})
	e.runCRMImports(t)
	if err := e.srv.SyncPayments(ctx, ws, i["id"].(string)); err != nil {
		t.Fatal(err)
	}
	waitPartnerCreditJobs(t, e, ws, 4)
	var conversions, referrals int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM partner_conversions WHERE workspace_id = $1 AND source IN ('browser', 'billing')`, ws).Scan(&conversions); err != nil || conversions != 0 {
		t.Fatalf("early browser/billing conversions = %d, %v", conversions, err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM partner_referrals WHERE workspace_id = $1`, ws).Scan(&referrals); err != nil || referrals != 1 {
		t.Fatalf("referrals = %d, want only the original referral: %v", referrals, err)
	}
	if n := len(named(events(), "partner_reward.created")); n != 0 {
		t.Fatalf("early browser/billing reward events = %d", n)
	}
}
