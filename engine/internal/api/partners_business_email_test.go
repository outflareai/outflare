package api_test

import (
	"context"
	"net/http/httptest"
	"testing"
)

// TestPartnerBusinessEmailOnly: with business_email_only, the portal
// refuses applications from free email addresses and takes work ones.
func TestPartnerBusinessEmailOnly(t *testing.T) {
	e := freshEnv(t)
	fake := &fakeResend{}
	rs := httptest.NewServer(fake)
	defer rs.Close()
	e.integrations.Client.BaseURLs["resend"] = rs.URL
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "resend", "credentials": map[string]any{"api_key": "re_test_not_a_real_key"}})
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO email_domains (workspace_id, provider_id, domain, status)
		SELECT workspace_id, id, 'northpeak.example', 'verified' FROM email_providers WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	id := owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate"})["id"].(string)
	settings := map[string]any{"email_from": "Partners <partners@northpeak.example>", "signup_open": true}
	owner.must(200, "PUT", base+"/partner-programs/"+id+"/portal", map[string]any{"enabled": true, "settings": settings})
	portal := &portalClient{t: t, h: e.h, base: "/p/" + id + "/"}

	// Off by default: a free address may apply.
	portal.must(201, "POST", "api/signup", map[string]any{"email": "first.applicant@gmail.com", "name": "First"})

	settings["business_email_only"] = true
	saved := owner.must(200, "PUT", base+"/partner-programs/"+id+"/portal", map[string]any{"settings": settings})
	if saved["portal"].(map[string]any)["business_email_only"] != true {
		t.Fatalf("portal = %v", saved["portal"])
	}
	if code, out, _ := portal.do("POST", "api/signup", map[string]any{"email": "second.applicant@gmail.com", "name": "Second"}); code != 400 ||
		out["error"].(map[string]any)["code"] != "personal_email" {
		t.Fatalf("free address = %d %v", code, out)
	}
	portal.must(201, "POST", "api/signup", map[string]any{"email": "ana@acme.example", "name": "Ana"})
}
