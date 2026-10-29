package api_test

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// Deleting a person erases them: the record, the records merged into it,
// their leads and product events, and with suppress=true their emails go
// on the opt-out list. Deals stay, and a company's people stay.
func TestDeleteRecord(t *testing.T) {
	got := track.Capture(t)
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws

	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme",
		"identities": []map[string]any{{"namespace": "domain", "value": "acme.example"}}})["id"].(string)
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "origin": "first_party",
		"attributes": map[string]any{"first_name": "Ana", "companies": []string{acme}},
		"identities": []map[string]any{{"namespace": "user_id", "value": "u_ana"}, {"namespace": "email", "value": "ana@acme.example", "verified": true}}})
	id := ana["id"].(string)
	ben := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ben",
		"attributes": map[string]any{"companies": []string{acme}}})["id"].(string)
	// A lead with her email, a product event, and an older copy merged in.
	owner.must(201, "POST", base+"/leads", map[string]any{"email": "Ana@acme.example", "first_name": "Ana"})
	owner.must(200, "POST", base+"/customer-events", map[string]any{"type": "track", "userId": "u_ana", "event": "Report Created"})
	dup := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana D",
		"identities": []map[string]any{{"namespace": "email", "value": "ana.old@acme.example"}}})["id"].(string)
	owner.must(200, "POST", base+"/records/"+id+"/merge", map[string]any{"from": dup})
	v := int(owner.must(200, "GET", base+"/records/"+id, nil)["version"].(float64))

	// An agent must say which version it read; a stale one is refused.
	agent := agentToken(t, owner, base, "Agent", "customers.read", "customers.manage")
	agent.header = map[string]string{"Outflare-Client": "mcp/0.1.0", "Outflare-Tool": "delete_record"}
	if code, out := agent.do("DELETE", base+"/records/"+id, nil); code != 400 || errCode(out) != "version_required" {
		t.Fatalf("agent without version: %d %v", code, out)
	}
	if v < 2 {
		t.Fatalf("the merge should have moved the version: %d", v)
	}
	if code, out := owner.do("DELETE", base+"/records/"+id+"?version=1", nil); code != 409 || errCode(out) != "record_changed" {
		t.Fatalf("stale version: %d %v", code, out)
	}
	// A reader can't.
	viewer := agentToken(t, owner, base, "Viewer", "customers.read")
	viewer.must(403, "DELETE", base+"/records/"+id, nil)

	out := agent.must(200, "DELETE", base+"/records/"+id+"?suppress=true&version="+strconv.Itoa(v), nil)
	erased := out["erased"].(map[string]any)
	if erased["records"] != 2.0 || erased["leads"] != 1.0 || erased["customer_events"] != 1.0 || out["suppressed"] != 2.0 {
		t.Fatalf("erased: %v", out)
	}
	owner.must(404, "GET", base+"/records/"+id, nil)
	owner.must(404, "GET", base+"/records/"+dup, nil)
	if n := len(owner.must(200, "GET", base+"/leads?q=ana", nil)["data"].([]any)); n != 0 {
		t.Fatalf("leads left: %d", n)
	}
	if l := owner.must(200, "GET", base+"/records/lookup?namespace=user_id&value=u_ana", nil); len(l["data"].([]any)) != 0 {
		t.Fatalf("identity left: %v", l)
	}
	sup := owner.must(200, "GET", base+"/suppressions?source=record_deleted", nil)["data"].([]any)
	if len(sup) != 2 {
		t.Fatalf("suppressions: %v", sup)
	}
	// Her company and its other people stay.
	owner.must(200, "GET", base+"/records/"+acme, nil)
	owner.must(200, "GET", base+"/records/"+ben, nil)

	// Deleting a company keeps its people, unlinked.
	owner.must(200, "DELETE", base+"/records/"+acme, nil)
	if rels := owner.must(200, "GET", base+"/records/"+ben+"/relationships", nil)["data"].([]any); len(rels) != 0 {
		t.Fatalf("links left: %v", rels)
	}
	owner.must(200, "GET", base+"/records/"+ben, nil)
	owner.must(404, "DELETE", base+"/records/"+acme, nil)

	if len(named(got(), "record.deleted")) != 2 {
		t.Fatal("no record.deleted events")
	}
}

// Erasing a partner keeps what they earned and were paid, on an archived
// record with no name or identities, and erasing a customer they referred
// keeps the partner's reward.
func TestDeleteRecordKeepsPartnerLedger(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	// Payout notes are cleared on erasure by default.
	if v := owner.must(200, "GET", base+"/partner-settings", nil); v["erase_payout_notes"] != true {
		t.Fatalf("partner settings = %v", v)
	}
	owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate",
		"landing_url": "https://northpeak.example/", "min_payout_cents": 0, "hold_days": 0})
	ana := owner.must(201, "POST", base+"/partners", map[string]any{"program": "affiliates", "email": "ana@example.com", "name": "Ana Lima", "code": "ana",
		"payout_method": "PayPal: ana@example.com", "note": "Met at a conference"})
	member, anaRec := ana["id"].(string), ana["record_id"].(string)
	cy := owner.must(201, "POST", base+"/partners", map[string]any{"program": "affiliates", "email": "cy@example.com", "name": "Cy Moss", "code": "cy"})
	sell := func(ref, customer, order string) string {
		sale := owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "sale", "customer_id": customer,
			"email": customer + "@example.com", "ref": ref, "amount_cents": 10000, "external_id": order})
		rewards := sale["rewards"].([]any)
		if sale["credited"] != true || len(rewards) == 0 {
			t.Fatalf("sale = %v", sale)
		}
		rw := rewards[0].(map[string]any)
		if rw["status"] == "pending" {
			owner.must(200, "POST", base+"/partner-rewards/"+rw["id"].(string)+"/approve", map[string]any{})
		}
		return rw["id"].(string)
	}
	reward := sell("ana", "u-1", "ch_1")
	sell("cy", "u-2", "ch_2")
	payouts := map[string]string{}
	for _, made := range owner.must(200, "POST", base+"/partner-payouts", map[string]any{"program": "affiliates"})["data"].([]any) {
		o := made.(map[string]any)
		payouts[o["partner_id"].(string)] = o["id"].(string)
		owner.must(200, "POST", base+"/partner-payouts/"+o["id"].(string)+"/paid", map[string]any{"note": "Paid by PayPal", "reference": "PP-1001"})
	}
	if len(payouts) != 2 {
		t.Fatalf("payouts = %v", payouts)
	}
	payoutAfter := func(partner string) (note, ref string) {
		t.Helper()
		var status string
		var cents int64
		if err := e.pool.QueryRow(ctx, `SELECT note, reference, status, amount_cents FROM partner_payouts WHERE id = $1`, payouts[partner]).
			Scan(&note, &ref, &status, &cents); err != nil || status != "paid" || cents == 0 {
			t.Fatalf("payout after erasing: %s %d, %v", status, cents, err)
		}
		return note, ref
	}

	owner.must(200, "DELETE", base+"/records/"+anaRec+"?suppress=true", nil)
	owner.must(404, "GET", base+"/records/"+anaRec, nil)
	var standIn, name string
	var archived bool
	var identities, kept int
	if err := e.pool.QueryRow(ctx, `SELECT m.record_id::text, r.name, r.archived_at IS NOT NULL,
			(SELECT count(*) FROM record_identities i WHERE i.record_id = r.id),
			(SELECT count(*) FROM partner_rewards w WHERE w.id = $2 AND w.recipient_id = r.id)
		FROM partner_memberships m JOIN records r ON r.id = m.record_id WHERE m.id = $1`, member, reward).
		Scan(&standIn, &name, &archived, &identities, &kept); err != nil {
		t.Fatalf("membership after erasing the partner: %v", err)
	}
	if standIn == anaRec || name != "Erased partner" || !archived || identities != 0 || kept != 1 {
		t.Fatalf("stand-in %s %q archived=%v identities=%d reward kept=%d", standIn, name, archived, identities, kept)
	}

	if p := owner.must(200, "GET", base+"/partners/"+member, nil); p["name"] != "Erased partner" || p["email"] != "" || p["status"] != "suspended" || p["payout_method"] != "" || p["note"] != "" {
		t.Fatalf("partner after erasing = %v", p)
	}
	owner.must(200, "GET", base+"/partners", nil)
	owner.must(200, "GET", base+"/partner-rewards", nil)

	// The payout stays without its note and reference.
	if note, ref := payoutAfter(member); note != "" || ref != "" {
		t.Fatalf("payout note %q reference %q, want both cleared", note, ref)
	}
	// With the setting off, the next erasure keeps them.
	owner.must(400, "PATCH", base+"/partner-settings", map[string]any{})
	owner.must(200, "PATCH", base+"/partner-settings", map[string]any{"erase_payout_notes": false})
	owner.must(200, "DELETE", base+"/records/"+cy["record_id"].(string), nil)
	if note, ref := payoutAfter(cy["id"].(string)); note != "Paid by PayPal" || ref != "PP-1001" {
		t.Fatalf("payout note %q reference %q, want both kept", note, ref)
	}

	// Their link still leads to the site, without credit.
	anon := &client{t: t, h: e.h}
	if code, loc := anon.rawUA("GET", "/r/"+ws+"/ana", "Mozilla/5.0"); code != http.StatusFound || loc != "https://northpeak.example/" {
		t.Fatalf("erased partner's link = %d %q", code, loc)
	}

	// The customer they referred is erased; the partner's reward stays.
	var bo string
	if err := e.pool.QueryRow(ctx, `SELECT record_id::text FROM partner_referrals WHERE workspace_id = $1`, ws).Scan(&bo); err != nil {
		t.Fatalf("referred customer: %v", err)
	}
	owner.must(200, "DELETE", base+"/records/"+bo, nil)
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM partner_rewards WHERE id = $1`, reward).Scan(&kept); err != nil || kept != 1 {
		t.Fatalf("reward after erasing the customer: %d, %v", kept, err)
	}
}
