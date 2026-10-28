package api_test

import (
	"context"
	"testing"
	"time"
)

// A program that takes sales from Stripe credits the synced payments of a
// customer its partner referred, and makes promo codes there.
func TestPartnerBillingSource(t *testing.T) {
	e := freshEnv(t)
	e.integrations.Client.BaseURLs["stripe"] = fakeStripe(t).URL
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	owner.must(400, "POST", base+"/partner-programs", map[string]any{"name": "Bad", "kind": "affiliate", "sales_source": "resend"})
	prog := owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate",
		"landing_url": "https://northpeak.example/", "sales_source": "stripe"})
	if prog["sales_source"] != "stripe" {
		t.Fatalf("program = %v", prog)
	}
	gil := owner.must(201, "POST", base+"/partners", map[string]any{"program": "affiliates", "email": "gil@example.com", "code": "gil"})
	// Ann signed up through Gil's link; the sign-up binds her.
	owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "signup", "customer_id": "u_ann", "email": "ann@acme.example", "ref": "gil"})

	// A promo code with a coupon is made in Stripe by a job.
	owner.must(409, "POST", base+"/partners/"+gil["id"].(string)+"/promo-codes", map[string]any{"code": "GIL10", "coupon": "co_partner"})
	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "stripe", "credentials": map[string]string{"api_key": "rk_test_key"}})
	p := owner.must(201, "POST", base+"/partners/"+gil["id"].(string)+"/promo-codes", map[string]any{"code": "GIL10", "coupon": "co_partner"})
	if c := p["promo_codes"].([]any)[0].(map[string]any); c["status"] != "pending" || c["coupon"] != "co_partner" {
		t.Fatalf("promo code = %v", c)
	}
	owner.must(201, "POST", base+"/partners/"+gil["id"].(string)+"/promo-codes", map[string]any{"code": "GILBAD", "coupon": "co_missing"})
	e.runCRMImports(t)

	if err := e.srv.SyncPayments(ctx, ws, i["id"].(string)); err != nil {
		t.Fatal(err)
	}
	var sales, refunds int
	for range 250 {
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE type = 'sale'), count(*) FILTER (WHERE type = 'refund')
			FROM partner_conversions WHERE workspace_id = $1 AND source = 'billing'`, ws).Scan(&sales, &refunds); err != nil {
			t.Fatal(err)
		}
		if sales == 2 && refunds == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if sales != 2 || refunds != 2 {
		t.Fatalf("billing conversions: %d sales, %d refunds", sales, refunds)
	}
	// No second payment.succeeded event for the same payments.
	var events int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM customer_events WHERE workspace_id = $1 AND name = 'payment.succeeded'`, ws).Scan(&events); err != nil || events != 3 {
		t.Fatalf("payment events = %d, %v", events, err)
	}
	st := owner.must(200, "GET", base+"/partners/"+gil["id"].(string), nil)["stats"].(map[string]any)
	// 20% of two 49.00 payments, less a 10.00 partial refund of one and the
	// full refund of the other.
	if st["customers"] != float64(1) || st["approved_cents"] != float64(780) || st["sales_cents"] != float64(3900) {
		t.Fatalf("stats = %v", st)
	}

	codes := map[string]map[string]any{}
	for range 250 {
		for _, c := range owner.must(200, "GET", base+"/partners/"+gil["id"].(string), nil)["promo_codes"].([]any) {
			codes[c.(map[string]any)["code"].(string)] = c.(map[string]any)
		}
		if codes["GIL10"]["status"] != "pending" && codes["GILBAD"]["status"] != "pending" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if codes["GIL10"]["status"] != "created" || codes["GILBAD"]["status"] != "failed" || codes["GILBAD"]["error"] == "" {
		t.Fatalf("codes = %v", codes)
	}
}
