package api_test

import (
	"context"
	"slices"
	"testing"
	"time"
)

// The relationship check on a card: what the brand already has with its
// company, from the company itself, a partnership pointing at it, or one
// of its people.
func TestRecordRelationshipCheck(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme", "identities": []map[string]any{{"namespace": "domain", "value": "acme.example"}}})
	brand, acmeID := acme["brand_id"].(string), acme["id"].(string)
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana", "identities": []map[string]any{{"namespace": "email", "value": "ana@acme.example"}}})["id"].(string)
	root := "/v1/brands/" + brand
	owner.must(201, "POST", root+"/records/"+ana+"/relationships", map[string]any{"to": acmeID, "kind": "member_of"})
	check := func(c *client, rec string) map[string]any {
		t.Helper()
		return c.must(200, "GET", root+"/records/"+rec+"/relationship", nil)
	}
	flags := func(v map[string]any) []string {
		var out []string
		for _, f := range v["flags"].([]any) {
			out = append(out, f.(string))
		}
		return out
	}

	// Nothing yet: a person's company is found, and there are no flags.
	got := check(owner, ana)
	if got["company_id"] != acmeID || got["people"] != 1.0 || len(flags(got)) != 0 || got["contact"].(map[string]any)["last_at"] != nil {
		t.Fatalf("empty check: %v", got)
	}

	// Two partnership pipelines for the same company.
	owner.must(200, "POST", root+"/kits/partnerships-seo/apply", nil)
	owner.must(200, "POST", root+"/kits/partnerships-affiliates/apply", nil)
	seo := owner.must(201, "POST", root+"/partnerships", map[string]any{"type": "link_partnership", "website": "acme.example"})["record"].(map[string]any)["id"].(string)
	aff := owner.must(201, "POST", root+"/partnerships", map[string]any{"type": "affiliate_partnership", "website": "acme.example"})["record"].(map[string]any)["id"].(string)
	got = check(owner, seo)
	pipes := got["pipelines"].([]any)
	if got["company_id"] != acmeID || len(pipes) != 1 || pipes[0].(map[string]any)["id"] != aff || pipes[0].(map[string]any)["type"] != "affiliate_partnership" || !slices.Contains(flags(got), "in_other_pipeline") {
		t.Fatalf("pipelines: %v", got)
	}

	// A paying customer, an open deal, and a meeting three months ago.
	if _, err := e.pool.Exec(ctx, `INSERT INTO lifecycle_overall (record_id, workspace_id, brand_id, status, active_products) VALUES ($1, $2, $3, 'active', 1)`, acmeID, ws, brand); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO deals (workspace_id, brand_id, record_id, source, state, name) VALUES ($1, $2, $3, 'manual', 'open', 'Fictional renewal')`, ws, brand, ana); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO meetings (workspace_id, brand_id, record_id, source, starts_at) VALUES ($1, $2, $3, 'manual', $4)`, ws, brand, ana, time.Now().Add(-90*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got = check(owner, seo)
	for _, f := range []string{"already_customer", "open_deal", "stale_thread", "in_other_pipeline"} {
		if !slices.Contains(flags(got), f) {
			t.Errorf("missing %s: %v", f, got)
		}
	}
	if got["customer"].(map[string]any)["active_products"] != 1.0 || got["deals"].(map[string]any)["open"] != 1.0 || got["contact"].(map[string]any)["stale"] != true {
		t.Fatalf("customer, deal, stale: %v", got)
	}

	// A colleague's own email with Ana: hidden from others until shared.
	inv := owner.must(201, "POST", base+"/invites", map[string]any{"email": "employee@northpeak.example", "role": "operator"})
	employee := &client{t: t, h: e.h}
	employee.must(200, "POST", "/v1/invites/accept", map[string]any{"token": inv["token"], "name": "Employee", "password": "not-real-test-password"})
	mail := root + "/crm/mail"
	owner.must(200, "PUT", mail+"/policy", map[string]any{"internal_domains": []string{"northpeak.example"}, "blocked_addresses": []string{}, "blocked_domains": []string{}, "version": 0})
	raw := "From: ana@acme.example\r\nTo: employee@northpeak.example\r\nSubject: Integration call\r\nMessage-ID: <call@acme.example>\r\n\r\nNext week works."
	employee.must(200, "POST", mail+"/records/"+ana+"/messages", map[string]any{"raw": raw})
	got = check(owner, acmeID)
	if len(got["team"].([]any)) != 0 || slices.Contains(flags(got), "warm_intro") {
		t.Fatalf("private mail showed: %v", got)
	}
	// Its owner sees their own, which is no warm intro to themselves.
	mine := check(employee, acmeID)
	if len(mine["team"].([]any)) != 1 || slices.Contains(flags(mine), "warm_intro") || mine["contact"].(map[string]any)["last_email_at"] == nil {
		t.Fatalf("owner's view: %v", mine)
	}
	employee.must(200, "PUT", mail+"/records/"+ana+"/sharing", map[string]any{"visibility": "metadata", "audience": "brand", "viewer_ids": []string{}, "version": 0})
	got = check(owner, acmeID)
	team := got["team"].([]any)
	if len(team) != 1 || team[0].(map[string]any)["name"] != "Employee" || team[0].(map[string]any)["messages"] != 1.0 || !slices.Contains(flags(got), "warm_intro") {
		t.Fatalf("warm intro: %v", got)
	}
	if _, ok := team[0].(map[string]any)["subject"]; ok {
		t.Fatal("the check carried email content")
	}

	// Another brand's route doesn't show this brand's card.
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Bravo"})["id"].(string)
	owner.must(404, "GET", "/v1/brands/"+other+"/records/"+acmeID+"/relationship", nil)
}
