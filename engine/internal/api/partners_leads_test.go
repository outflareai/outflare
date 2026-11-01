package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// TestPartnerLeadSubmissions: with lead_submissions on, partners submit
// people they referred on the portal; the team approves them into
// referrals (via manual) or turns them down, the first partner keeps a
// customer, and auto mode takes leads at once.
func TestPartnerLeadSubmissions(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	prog := owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate"})
	id := prog["id"].(string)
	portalURL := base + "/partner-programs/" + id + "/portal"
	owner.must(200, "PUT", portalURL, map[string]any{"enabled": true})
	ana := owner.must(201, "POST", base+"/partners", map[string]any{"program": "affiliates", "email": "ana@acme.example"})
	bo := owner.must(201, "POST", base+"/partners", map[string]any{"program": "affiliates", "email": "bo@acme.example"})

	signIn := func(partner map[string]any) *portalClient {
		t.Helper()
		var rec string
		if err := e.pool.QueryRow(ctx, `SELECT record_id::text FROM partner_memberships WHERE id = $1`, partner["id"]).Scan(&rec); err != nil {
			t.Fatal(err)
		}
		token, hash := auth.NewToken(auth.PrefixPartnerLink)
		if _, err := e.pool.Exec(ctx, `INSERT INTO partner_sign_in_links (workspace_id, program_id, record_id, token_hash, expires_at)
			VALUES ($1, $2, $3, $4, $5)`, ws, id, rec, hash, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		p := &portalClient{t: t, h: e.h, base: "/p/" + id + "/"}
		p.must(200, "POST", "api/session", map[string]any{"token": token})
		return p
	}
	pa, pb := signIn(ana), signIn(bo)

	// Off by default.
	if info := pa.must(200, "GET", "api/program", nil); info["lead_submissions"] != false {
		t.Fatalf("program = %v", info)
	}
	pa.must(403, "POST", "api/leads", map[string]any{"email": "cara@northpeak.example"})
	owner.must(400, "PUT", portalURL, map[string]any{"settings": map[string]any{"lead_submissions": "maybe"}})
	owner.must(200, "PUT", portalURL, map[string]any{"settings": map[string]any{"lead_submissions": "review"}})

	lead := pa.must(201, "POST", "api/leads", map[string]any{"email": "Cara@northpeak.example", "note": "Met at a meetup"})
	if lead["status"] != "pending" || lead["label"] == "" || lead["email"] != nil {
		t.Fatalf("lead = %v", lead)
	}
	if code, out, _ := pa.do("POST", "api/leads", map[string]any{"email": "cara@northpeak.example"}); code != 409 || out["error"].(map[string]any)["code"] != "already_submitted" {
		t.Fatalf("again = %d %v", code, out)
	}
	if code, out, _ := pa.do("POST", "api/leads", map[string]any{"email": "ana@acme.example"}); code != 409 || out["error"].(map[string]any)["code"] != "self_referral" {
		t.Fatalf("self = %d %v", code, out)
	}
	pa.must(400, "POST", "api/leads", map[string]any{"email": "not an email"})

	// The team sees who it is, and approves it into a referral.
	pending := owner.must(200, "GET", base+"/partner-leads?status=pending&program=affiliates", nil)["data"].([]any)
	if len(pending) != 1 {
		t.Fatalf("pending = %v", pending)
	}
	p0 := pending[0].(map[string]any)
	if p0["email"] != "cara@northpeak.example" || p0["partner_id"] != ana["id"] || p0["note"] != "Met at a meetup" {
		t.Fatalf("lead for the team = %v", p0)
	}
	got := owner.must(200, "POST", base+"/partner-leads/"+p0["id"].(string)+"/approve", map[string]any{})
	if got["status"] != "approved" || got["referral_id"] == nil {
		t.Fatalf("approved = %v", got)
	}
	owner.must(409, "POST", base+"/partner-leads/"+p0["id"].(string)+"/approve", map[string]any{})
	refs := owner.must(200, "GET", base+"/partner-referrals?partner_id="+ana["id"].(string), nil)["data"].([]any)
	if len(refs) != 1 || refs[0].(map[string]any)["via"] != "manual" {
		t.Fatalf("referrals = %v", refs)
	}
	var signups int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM customer_events WHERE workspace_id = $1 AND record_id = $2 AND name = 'signed_up'`,
		ws, p0["record_id"]).Scan(&signups); err != nil || signups != 0 {
		t.Fatalf("a submitted lead recorded a sign-up: %d %v", signups, err)
	}
	if mine := pa.must(200, "GET", "api/leads", nil)["data"].([]any); len(mine) != 1 || mine[0].(map[string]any)["status"] != "approved" {
		t.Fatalf("portal leads = %v", mine)
	}
	// Cara is Ana's now.
	if code, out, _ := pb.do("POST", "api/leads", map[string]any{"email": "cara@northpeak.example"}); code != 409 || out["error"].(map[string]any)["code"] != "already_referred" {
		t.Fatalf("Bo submitting Cara = %d %v", code, out)
	}

	// Turned down, with a reason the partner sees.
	dee := pa.must(201, "POST", "api/leads", map[string]any{"email": "dee@northpeak.example"})
	if r := owner.must(200, "POST", base+"/partner-leads/"+dee["id"].(string)+"/reject", map[string]any{"reason": "Came in through an ad"}); r["status"] != "rejected" {
		t.Fatalf("rejected = %v", r)
	}
	for _, l := range pa.must(200, "GET", "api/leads", nil)["data"].([]any) {
		if m := l.(map[string]any); m["id"] == dee["id"] && (m["status"] != "rejected" || m["reason"] != "Came in through an ad") {
			t.Fatalf("Dee on the portal = %v", m)
		}
	}

	// Someone Bo referred while Ana's lead waited stays Bo's.
	fay := pa.must(201, "POST", "api/leads", map[string]any{"email": "fay@northpeak.example"})
	owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "signup", "email": "fay@northpeak.example", "ref": bo["code"]})
	if r := owner.must(200, "POST", base+"/partner-leads/"+fay["id"].(string)+"/approve", map[string]any{}); r["status"] != "rejected" || r["reason"] == "" {
		t.Fatalf("Fay = %v", r)
	}

	// Auto: taken at once.
	owner.must(200, "PUT", portalURL, map[string]any{"settings": map[string]any{"lead_submissions": "auto"}})
	if l := pa.must(201, "POST", "api/leads", map[string]any{"email": "gus@northpeak.example"}); l["status"] != "approved" {
		t.Fatalf("auto = %v", l)
	}
	owner.must(400, "GET", base+"/partner-leads?status=lost", nil)
	owner.must(404, "POST", base+"/partner-leads/00000000-0000-4000-8000-000000000000/approve", map[string]any{})
}
