package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// TestPartnerPayoutTerms: a program's payout term holds rewards until the
// month they were earned in has ended plus N days, and partners choose
// their payout method and token from the program's list on the portal.
func TestPartnerPayoutTerms(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	prog := owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate"})
	if prog["payout_term_days"] != nil || len(prog["payout_methods"].([]any)) != 0 || len(prog["payout_tokens"].([]any)) != 0 {
		t.Fatalf("defaults = %v %v %v", prog["payout_term_days"], prog["payout_methods"], prog["payout_tokens"])
	}
	if p := owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Referrals", "kind": "referral", "payout_term_days": 15,
		"payout_methods": []string{"wise"}}); p["payout_term_days"] != 15.0 || p["payout_methods"].([]any)[0] != "wise" {
		t.Fatalf("created with a term = %v %v", p["payout_term_days"], p["payout_methods"])
	}
	owner.must(400, "PATCH", base+"/partner-programs/affiliates", map[string]any{"payout_term_days": 91})
	owner.must(400, "PATCH", base+"/partner-programs/affiliates", map[string]any{"payout_methods": []string{"cash"}})
	owner.must(400, "PATCH", base+"/partner-programs/affiliates", map[string]any{"payout_tokens": []string{""}})
	prog = owner.must(200, "PATCH", base+"/partner-programs/affiliates", map[string]any{"min_payout_cents": 0, "payout_term_days": 30,
		"payout_methods": []string{"paypal", "crypto", "paypal"}, "payout_tokens": []string{" USDC on Base "}})
	if prog["payout_term_days"] != 30.0 || len(prog["payout_methods"].([]any)) != 2 || prog["payout_tokens"].([]any)[0] != "USDC on Base" {
		t.Fatalf("program = %v %v %v", prog["payout_term_days"], prog["payout_methods"], prog["payout_tokens"])
	}

	// Net 30: a fee earned this month waits; one earned two months ago is paid.
	ana := owner.must(201, "POST", base+"/partners", map[string]any{"program": "affiliates", "email": "ana@acme.example",
		"payout_method": "ana@acme.example", "payout_type": "paypal"})
	if ana["payout_type"] != "paypal" || ana["payout_token"] != nil {
		t.Fatalf("ana = %v", ana)
	}
	id := ana["id"].(string)
	owner.must(201, "POST", base+"/partners/"+id+"/fees", map[string]any{"amount_cents": 1000, "note": "This month"})
	owner.must(201, "POST", base+"/partners/"+id+"/fees", map[string]any{"amount_cents": 500, "note": "Earlier"})
	if _, err := e.pool.Exec(ctx, `UPDATE partner_rewards SET created_at = now() - interval '2 months' WHERE membership_id = $1 AND note = 'Earlier'`, id); err != nil {
		t.Fatal(err)
	}
	res := owner.must(200, "POST", base+"/partner-payouts", map[string]any{"program": "affiliates"})
	if list := res["data"].([]any); len(list) != 1 || list[0].(map[string]any)["amount_cents"] != 500.0 {
		t.Fatalf("payouts with Net 30 = %v", res)
	}
	// Without a term, the rest is paid once approved.
	if p := owner.must(200, "PATCH", base+"/partner-programs/affiliates", map[string]any{"payout_term_days": nil}); p["payout_term_days"] != nil {
		t.Fatalf("term cleared = %v", p["payout_term_days"])
	}
	if list := owner.must(200, "POST", base+"/partner-payouts", map[string]any{"program": "affiliates"})["data"].([]any); len(list) != 1 ||
		list[0].(map[string]any)["amount_cents"] != 1000.0 {
		t.Fatalf("payouts without a term = %v", list)
	}

	// On the portal, Ana picks from the program's methods and tokens.
	owner.must(200, "PUT", base+"/partner-programs/affiliates/portal", map[string]any{"enabled": true})
	var rec string
	if err := e.pool.QueryRow(ctx, `SELECT record_id::text FROM partner_memberships WHERE id = $1`, id).Scan(&rec); err != nil {
		t.Fatal(err)
	}
	token, hash := auth.NewToken(auth.PrefixPartnerLink)
	if _, err := e.pool.Exec(ctx, `INSERT INTO partner_sign_in_links (workspace_id, program_id, record_id, token_hash, expires_at)
		VALUES ($1, $2, $3, $4, $5)`, ws, prog["id"], rec, hash, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	portal := &portalClient{t: t, h: e.h, base: "/p/" + prog["id"].(string) + "/"}
	portal.must(200, "POST", "api/session", map[string]any{"token": token})
	me := portal.must(200, "GET", "api/me", nil)
	if me["payout_type"] != "paypal" || len(me["payout_methods"].([]any)) != 2 || me["payout_term_days"] != nil {
		t.Fatalf("me = %v", me)
	}
	portal.must(400, "PATCH", "api/me", map[string]any{"payout_type": "wise"})
	portal.must(400, "PATCH", "api/me", map[string]any{"payout_type": "crypto", "payout_token": "DOGE"})
	portal.must(400, "PATCH", "api/me", map[string]any{"payout_type": "paypal", "payout_token": "USDC on Base"})
	me = portal.must(200, "PATCH", "api/me", map[string]any{"payout_type": "crypto", "payout_token": "USDC on Base", "payout_method": "0xexample"})
	if me["payout_type"] != "crypto" || me["payout_token"] != "USDC on Base" || me["payout_method"] != "0xexample" {
		t.Fatalf("crypto = %v", me)
	}
	// Switching away from crypto drops the token.
	if me = portal.must(200, "PATCH", "api/me", map[string]any{"payout_type": "paypal"}); me["payout_type"] != "paypal" || me["payout_token"] != nil {
		t.Fatalf("paypal = %v", me)
	}
}
