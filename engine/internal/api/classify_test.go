package api_test

import (
	"context"
	"net"
	"testing"
)

func TestClassify(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	e.dns.mx["acme-widgets.test"] = []*net.MX{{Host: "acme-widgets-test.mail.protection.outlook.com.", Pref: 0}}

	global := func(body map[string]any) map[string]any { return body["global"].(map[string]any) }
	effective := func(body map[string]any) map[string]any { return body["effective"].(map[string]any) }

	body := owner.must(200, "POST", base+"/classifications", map[string]any{"email": "Jo@Proton.me"})
	if body["scope"] != "email" || body["normalized_domain"] != "proton.me" || global(body)["category"] != "private_mailbox" ||
		effective(body)["action"] != "allow" || global(body)["confidence"] != nil {
		t.Fatalf("proton: %v", body)
	}

	body = owner.must(200, "POST", base+"/classifications", map[string]any{"email": "x@mailinator.com"})
	attrs := global(body)["attributes"].(map[string]any)
	if global(body)["category"] != "disposable" || attrs["is_disposable"] != true || effective(body)["action"] != "block" {
		t.Fatalf("mailinator: %v", body)
	}

	// An unknown business domain: mail servers are known, the category is not.
	body = owner.must(200, "GET", base+"/email-domains/acme-widgets.test", nil)
	attrs = global(body)["attributes"].(map[string]any)
	if body["scope"] != "domain" || global(body)["category"] != "unknown" || attrs["has_mx"] != true ||
		attrs["mx_provider_id"] != "microsoft" || attrs["is_disposable"] != "unknown" || effective(body)["action"] != "verify" {
		t.Fatalf("unknown: %v", body)
	}
	if _, ok := attrs["is_role_address"]; ok {
		t.Fatal("a domain lookup has no address attributes")
	}
	if a := body["analysis"].(map[string]any); a["provisional"] != true {
		t.Fatalf("analysis: %v", a)
	}

	// A domain that takes no mail.
	e.dns.mx["parked.test"] = []*net.MX{{Host: ".", Pref: 0}}
	body = owner.must(200, "POST", base+"/classifications", map[string]any{"domain": "parked.test"})
	if effective(body)["matched_policy_id"] != "default_no_mail" {
		t.Fatalf("parked: %v", body)
	}

	// Research the workspace paid for is added to the answer, without
	// changing what the global data says.
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO domain_research
		(workspace_id, domain, category, subcategory, confidence_status, signals, attributes, note, model)
		VALUES ($1, 'acme-widgets.test', 'disposable', 'short_lived_mailbox', 'inferred',
			'["site_offers_temporary_addresses"]', '{"is_disposable":"true"}', 'Hands out inboxes that expire.', 'claude-haiku-5-5')`,
		ws); err != nil {
		t.Fatal(err)
	}
	body = owner.must(200, "GET", base+"/email-domains/acme-widgets.test", nil)
	r, ok := body["research"].(map[string]any)
	if !ok || r["category"] != "disposable" || r["model"] != "claude-haiku-5-5" {
		t.Fatalf("research: %v", body)
	}
	if global(body)["category"] != "unknown" {
		t.Error("research must not change the global category")
	}
	if effective(body)["category"] != "disposable" || effective(body)["action"] != "block" ||
		effective(body)["matched_policy_id"] != "default_disposable" {
		t.Fatalf("effective: %v", effective(body))
	}
	if a := body["analysis"].(map[string]any); a["status"] != "researched" {
		t.Fatalf("analysis: %v", a)
	}

	for _, req := range []map[string]any{{}, {"email": "a@b.test", "domain": "b.test"}, {"email": "not an address"}, {"domain": "co.uk"}} {
		status, body := owner.do("POST", base+"/classifications", req)
		if status != 400 || body["error"].(map[string]any)["code"] != "invalid_request" {
			t.Errorf("%v: %d %v", req, status, body)
		}
	}
	if status, _ := owner.do("GET", base+"/email-domains/not_a_domain", nil); status != 400 {
		t.Errorf("bad domain: %d", status)
	}
}
