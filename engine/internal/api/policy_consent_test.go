package api_test

import (
	"context"
	"testing"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/policy"
)

// A person who unsubscribed from all marketing email on the preference
// page gets no cold or lifecycle email either, though transactional email
// still reaches them.
func TestWithdrawnMarketingConsentBlocksSends(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	withdraw := func(addr string, topic, status string) {
		t.Helper()
		rec, err := customers.EnsurePerson(ctx, e.pool, ws, "email", addr, true, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := customers.RecordConsent(ctx, e.pool, ws, customers.ConsentChange{RecordID: rec, Channel: "email", Topic: topic, Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	withdraw("ana@example.org", "", customers.Withdrawn)
	withdraw("bo@example.org", "news", customers.Withdrawn) // one topic only
	withdraw("cy@example.org", "", customers.Withdrawn)
	withdraw("cy@example.org", "", customers.Granted) // changed their mind

	res := owner.must(200, "POST", "/v1/workspaces/"+ws+"/recipients/check", map[string]any{
		"emails": []string{"Ana@example.org", "bo@example.org", "cy@example.org", "dee@example.org"}})
	got := map[string]bool{}
	for _, d := range res["data"].([]any) {
		d := d.(map[string]any)
		got[d["email"].(string)] = d["allowed"].(bool)
		if d["email"] == "Ana@example.org" && d["reasons"].([]any)[0] != policy.MarketingWithdrawn {
			t.Fatalf("ana = %v", d)
		}
	}
	if got["Ana@example.org"] || !got["bo@example.org"] || !got["cy@example.org"] || !got["dee@example.org"] {
		t.Fatalf("allowed = %v", got)
	}
	for purpose, want := range map[string]bool{policy.PurposeCold: false, policy.PurposeLifecycle: false, policy.PurposeTransactional: true} {
		d, err := policy.CheckPurpose(ctx, e.pool, ws, purpose, []string{"ana@example.org"})
		if err != nil {
			t.Fatal(err)
		}
		if d[0].Allowed != want {
			t.Errorf("%s: allowed = %v, want %v", purpose, d[0].Allowed, want)
		}
	}
}
