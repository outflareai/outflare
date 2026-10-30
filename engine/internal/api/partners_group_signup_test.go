package api_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPartnerGroupSignup: each group has a sign-up link on the portal;
// partners who apply through it join that group, and a link to a group
// since deleted joins the default group.
func TestPartnerGroupSignup(t *testing.T) {
	e := freshEnv(t)
	fake := &fakeResend{}
	rs := httptest.NewServer(fake)
	defer rs.Close()
	e.integrations.Client.BaseURLs["resend"] = rs.URL
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "resend", "credentials": map[string]any{"api_key": "re_test_not_a_real_key"}})
	if _, err := e.pool.Exec(ctx, `INSERT INTO email_domains (workspace_id, provider_id, domain, status)
		SELECT workspace_id, id, 'northpeak.example', 'verified' FROM email_providers WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	id := owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate"})["id"].(string)
	owner.must(200, "PUT", base+"/partner-programs/"+id+"/portal", map[string]any{"enabled": true,
		"settings": map[string]any{"email_from": "Partners <partners@northpeak.example>", "signup_open": true}})
	std := owner.must(201, "POST", base+"/partner-programs/"+id+"/groups", map[string]any{"name": "Standard", "default": true})
	gold := owner.must(201, "POST", base+"/partner-programs/"+id+"/groups", map[string]any{"name": "Gold"})
	link, _ := gold["signup_url"].(string)
	if !strings.HasSuffix(link, "/p/"+id+"/?group="+gold["id"].(string)) {
		t.Fatalf("signup_url = %q", link)
	}
	prog := owner.must(200, "GET", base+"/partner-programs/"+id, nil)
	for _, g := range prog["groups"].([]any) {
		if m := g.(map[string]any); m["signup_url"] == "" {
			t.Fatalf("group = %v", m)
		}
	}

	portal := &portalClient{t: t, h: e.h, base: "/p/" + id + "/"}
	portal.must(201, "POST", "api/signup", map[string]any{"email": "ana@acme.example", "name": "Ana", "group": gold["id"]})
	portal.must(201, "POST", "api/signup", map[string]any{"email": "bo@acme.example", "name": "Bo"})
	portal.must(201, "POST", "api/signup", map[string]any{"email": "cy@acme.example", "name": "Cy", "group": "00000000-0000-0000-0000-000000000000"})
	groupOf := func(email string) string {
		var g string
		if err := e.pool.QueryRow(ctx, `SELECT COALESCE(m.group_id::text, '') FROM partner_memberships m
			JOIN record_identities i ON i.record_id = m.record_id AND i.namespace = 'email' WHERE i.value = $1`, email).Scan(&g); err != nil {
			t.Fatal(err)
		}
		return g
	}
	if groupOf("ana@acme.example") != gold["id"] || groupOf("bo@acme.example") != std["id"] || groupOf("cy@acme.example") != std["id"] {
		t.Fatalf("groups: ana %s, bo %s, cy %s", groupOf("ana@acme.example"), groupOf("bo@acme.example"), groupOf("cy@acme.example"))
	}
}
