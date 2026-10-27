package api_test

import (
	"context"
	"slices"
	"testing"

	"github.com/outflareai/outflare/engine/internal/policy"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Every person says what may go out to them and why, and the workspace
// chooses whether people who never said yes or no get marketing email.
// Third-party leads never do.
func TestContactabilityAndRules(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	if r := owner.must(200, "GET", base+"/contact-rules", nil); r["customer_marketing"] != "inferred" {
		t.Fatalf("default rule = %v", r)
	}
	owner.must(400, "PATCH", base+"/contact-rules", map[string]any{"customer_marketing": "everyone"})
	owner.must(400, "POST", base+"/records", map[string]any{"type": "person", "acquired_via": "Apollo IO"})

	person := func(origin, via, email string) map[string]any {
		t.Helper()
		r := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "origin": origin, "acquired_via": via,
			"identities": []any{map[string]any{"namespace": "email", "value": email}}})
		return r
	}
	contact := func(id string) map[string]any {
		t.Helper()
		return owner.must(200, "GET", base+"/records/"+id, nil)["contactability"].(map[string]any)
	}
	allowed := func(c map[string]any) [3]bool {
		a := c["allowed"].(map[string]any)
		return [3]bool{a["transactional"].(bool), a["cold_outreach"].(bool), a["marketing"].(bool)}
	}
	lead := person("prospect", "apollo", "lee@acme.example")["id"].(string)
	customer := person("first_party", "site", "cam@northpeak.example")["id"].(string)
	crm := person("crm_sync", "", "cy@northpeak.example")["id"].(string)

	c := contact(lead)
	if c["basis"] != "third_party" || c["acquired_via"] != "apollo" || allowed(c) != [3]bool{true, true, false} {
		t.Fatalf("lead = %v", c)
	}
	if c := contact(customer); c["basis"] != "customer" || allowed(c) != [3]bool{true, true, true} {
		t.Fatalf("customer = %v", c)
	}
	if c := contact(crm); c["basis"] != "unknown" || allowed(c) != [3]bool{true, true, true} {
		t.Fatalf("crm = %v", c)
	}

	// Only people who agreed: the customer and the CRM contact are out too.
	owner.must(200, "PATCH", base+"/contact-rules", map[string]any{"customer_marketing": "opt_in"})
	if c := contact(customer); allowed(c) != [3]bool{true, true, false} || c["why"] == "" {
		t.Fatalf("customer, opt-in only = %v", c)
	}
	if c := contact(crm); allowed(c)[2] {
		t.Fatalf("crm, opt-in only = %v", c)
	}
	// The same answer for many people at once, with a reason code each.
	check := func(purpose string, emails ...string) []string {
		t.Helper()
		var out []string
		for _, d := range owner.must(200, "POST", base+"/recipients/check", map[string]any{"emails": emails, "purpose": purpose})["data"].([]any) {
			d := d.(map[string]any)
			why := "ok"
			if rs := d["reasons"].([]any); len(rs) > 0 {
				why = rs[0].(string)
			}
			out = append(out, why)
		}
		return out
	}
	if got := check("lifecycle", "lee@acme.example", "cam@northpeak.example", "new@northpeak.example"); !slices.Equal(got, []string{"third_party_no_consent", "no_consent", "no_consent"}) {
		t.Fatalf("lifecycle check = %v", got)
	}
	if got := check("cold", "lee@acme.example", "cam@northpeak.example"); !slices.Equal(got, []string{"ok", "ok"}) {
		t.Fatalf("cold check = %v", got)
	}
	owner.must(400, "POST", base+"/recipients/check", map[string]any{"emails": []string{"lee@acme.example"}, "purpose": "newsletter"})
	owner.must(201, "POST", base+"/records/"+customer+"/consents", map[string]any{"channel": "email", "purpose": "marketing", "status": "granted"})
	if c := contact(customer); c["basis"] != "opted_in" || !allowed(c)[2] {
		t.Fatalf("customer who agreed = %v", c)
	}

	// A competitor's domain keeps everyone there out of cold and marketing
	// email; a customer entry only out of cold email.
	r := owner.must(200, "POST", base+"/suppressions", map[string]any{"values": []string{"acme.example"}, "reason": "competitor", "source": "hubspot_export"})
	if r["added"] != 1.0 {
		t.Fatalf("add = %v", r)
	}
	owner.must(400, "POST", base+"/suppressions", map[string]any{"values": []string{"x.example"}, "source": "Not OK!"})
	owner.must(400, "POST", base+"/suppressions", map[string]any{"values": []string{"x.example"}, "method": "guessed"})
	owner.must(200, "POST", base+"/suppressions", map[string]any{"values": []string{"cam@northpeak.example"}, "reason": "customer"})
	if c := contact(lead); allowed(c) != [3]bool{true, false, false} || !slices.Contains(c["suppressed"].([]any), any("competitor")) {
		t.Fatalf("competitor = %v", c)
	}
	if c := contact(customer); allowed(c) != [3]bool{true, false, true} {
		t.Fatalf("customer entry = %v", c)
	}
	list := owner.must(200, "GET", base+"/suppressions?reason=competitor", nil)["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("list = %v", list)
	}
	entry := list[0].(map[string]any)
	if entry["method"] != "manual" || entry["source"] != "hubspot_export" || entry["added_by"] == nil || entry["added_by_type"] != "human" {
		t.Fatalf("entry = %v", entry)
	}
	if n := len(owner.must(200, "GET", base+"/suppressions?method=automated", nil)["data"].([]any)); n != 0 {
		t.Fatalf("automated = %d", n)
	}

	// Whole companies, before anyone's address is known.
	ds, err := policy.CheckDomains(context.Background(), e.pool, ws, policy.PurposeCold, []string{"Acme.example", "northpeak.example"})
	if err != nil || ds[0].Allowed || ds[0].Reasons[0] != "suppressed_domain:competitor" || !ds[1].Allowed {
		t.Fatalf("domains = %+v, %v", ds, err)
	}

	// Cold email checks every entry.
	d := owner.must(200, "POST", base+"/recipients/check", map[string]any{"emails": []string{"lee@acme.example"}})["data"].([]any)[0].(map[string]any)
	if d["allowed"] != false {
		t.Fatalf("check = %v", d)
	}

	if r := owner.must(200, "GET", base+"/suppression-syncs", nil)["data"].([]any); len(r) != 0 {
		t.Fatalf("syncs = %v", r)
	}
	var names []string
	for _, ev := range got() {
		names = append(names, ev.Name)
	}
	if !slices.Contains(names, "contact_rules.updated") || !slices.Contains(names, "suppressions.added") {
		t.Fatalf("events = %v", names)
	}
}
