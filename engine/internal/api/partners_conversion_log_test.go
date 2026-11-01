package api_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
)

// TestPartnerConversionLog: every conversion call, by the API or a
// postback URL, is logged with what it sent, the answer and the outcome;
// the log finds a call by its ids or email without keeping the email, and
// forgets calls after 30 days.
func TestPartnerConversionLog(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate"})
	ana := owner.must(201, "POST", base+"/partners", map[string]any{"program": "affiliates", "email": "ana@acme.example"})
	conv := base + "/partner-conversions"

	sale := map[string]any{"type": "sale", "ref": ana["code"], "email": "bo@northpeak.example", "external_id": "ord-1", "amount_cents": 4900}
	owner.must(201, "POST", conv, sale)
	owner.must(200, "POST", conv, sale) // a retry
	owner.must(200, "POST", conv, map[string]any{"type": "signup", "customer_id": "u-2"})
	owner.must(400, "POST", conv, map[string]any{"type": "sale", "customer_id": "u-3"}) // no amount
	pb := owner.must(201, "POST", base+"/partner-programs/affiliates/postback", map[string]any{})
	pbURL, _ := url.Parse(pb["url"].(string))
	anon := &client{t: t, h: e.h}
	anon.must(201, "GET", pbURL.Path+"?type=sale&ref="+ana["code"].(string)+"&user_id=u-4&amount=10&order_id=ord-2", nil)

	logURL := base + "/partner-conversion-log"
	list := func(query string) []map[string]any {
		t.Helper()
		var out []map[string]any
		for _, v := range owner.must(200, "GET", logURL+query, nil)["data"].([]any) {
			out = append(out, v.(map[string]any))
		}
		return out
	}
	all := list("")
	if len(all) != 5 {
		t.Fatalf("log = %d entries: %v", len(all), all)
	}
	// Newest first: the postback, the rejected sale, the uncredited sign-up,
	// the retry and the sale.
	want := []struct{ source, outcome, reason string }{
		{"postback", "credited", ""}, {"api", "rejected", "invalid_request"}, {"api", "not_credited", "no_partner"},
		{"api", "duplicate", ""}, {"api", "credited", ""},
	}
	for i, w := range want {
		if all[i]["source"] != w.source || all[i]["outcome"] != w.outcome || all[i]["reason"] != w.reason {
			t.Errorf("entry %d = %v %v %v, want %v", i, all[i]["source"], all[i]["outcome"], all[i]["reason"], w)
		}
	}
	first := all[4]
	if first["conversion_id"] == nil || first["record_id"] == nil || first["program_id"] == nil || first["status"] != 201.0 {
		t.Errorf("credited entry = %v", first)
	}
	if req := first["request"].(map[string]any); req["email"] != "(sent)" || req["external_id"] != "ord-1" || req["recurring"] != nil {
		t.Errorf("request = %v", req)
	}
	if first["response"].(map[string]any)["credited"] != true {
		t.Errorf("response = %v", first["response"])
	}
	if all[2]["record_id"] == nil || all[1]["record_id"] != nil {
		t.Errorf("records: not credited %v, rejected %v", all[2]["record_id"], all[1]["record_id"])
	}

	// Search by id, email (never stored) and outcome.
	if got := list("?q=ord-1"); len(got) != 2 {
		t.Errorf("q=ord-1: %d", len(got))
	}
	if got := list("?q=" + url.QueryEscape("BO@northpeak.example")); len(got) != 2 {
		t.Errorf("q=email: %d", len(got))
	}
	if got := list("?outcome=rejected&source=api"); len(got) != 1 {
		t.Errorf("rejected: %d", len(got))
	}
	if got := list("?source=postback&q=ord-2"); len(got) != 1 {
		t.Errorf("postback: %d", len(got))
	}
	owner.must(400, "GET", logURL+"?outcome=lost", nil)
	var stored string
	if err := e.pool.QueryRow(ctx, `SELECT string_agg(request::text || response::text, ' ') FROM partner_conversion_log WHERE workspace_id = $1`, ws).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "bo@northpeak.example") {
		t.Errorf("the log kept an email: %s", stored)
	}

	// After 30 days a call is forgotten.
	if _, err := e.pool.Exec(ctx, `UPDATE partner_conversion_log SET created_at = now() - interval '31 days' WHERE workspace_id = $1 AND source = 'postback'`, ws); err != nil {
		t.Fatal(err)
	}
	if n, err := e.srv.PrunePartnerConversionLog(ctx); err != nil || n != 1 {
		t.Fatalf("prune = %d %v", n, err)
	}
	if got := list(""); len(got) != 4 {
		t.Errorf("after prune: %d", len(got))
	}
}
