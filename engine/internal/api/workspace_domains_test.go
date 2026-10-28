package api_test

import (
	"context"
	"fmt"
	"testing"
)

// Every domain the workspace has set up shows once, with each thing it is
// used for, and the connections list joins integrations and mailbox
// sign-ins.
func TestOneListOfDomainsAndConnections(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	acme := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "website": "acme.example", "domains": []string{"acme.example"}})["id"].(string)
	owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "Website", "brand_id": acme, "origins": []string{"https://www.acme.example"}})
	owner.must(201, "POST", base+"/tracking-domains", map[string]any{"domain": "links.acme.example"})
	if _, err := e.pool.Exec(ctx, `INSERT INTO sending_domains (workspace_id, domain, spf, dkim, dmarc, brand_id) VALUES
		($1, 'acme.example', 'pass', 'pass', 'pass', $2), ($1, 'acme-mail.example', 'missing', 'pass', 'pass', NULL)`, ws, acme); err != nil {
		t.Fatal(err)
	}
	company := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme",
		"identities": []any{map[string]any{"namespace": "domain", "value": "acme.example"}}})["id"].(string)

	list := owner.must(200, "GET", base+"/domains", nil)["data"].([]any)
	if len(list) != 2 {
		t.Fatalf("domains: %v", list)
	}
	a, b := list[0].(map[string]any), list[1].(map[string]any)
	if a["domain"] != "acme-mail.example" || fmt.Sprint(a["purposes"]) != "[cold_email]" || fmt.Sprint(a["needs_attention"]) != "[cold_email]" {
		t.Fatalf("second sending domain: %v", a)
	}
	if b["domain"] != "acme.example" || fmt.Sprint(b["purposes"]) != "[website site_tracking cold_email tracking_links]" ||
		fmt.Sprint(b["needs_attention"]) != "[tracking_links]" || b["record_id"] != company || fmt.Sprint(b["brand_ids"]) != "["+acme+"]" {
		t.Fatalf("acme.example: %v", b)
	}
	hosts := []string{}
	for _, u := range b["uses"].([]any) {
		hosts = append(hosts, u.(map[string]any)["host"].(string))
	}
	if fmt.Sprint(hosts) != "[acme.example www.acme.example acme.example links.acme.example]" {
		t.Fatalf("hosts: %v", hosts)
	}
	if n := len(owner.must(200, "GET", base+"/domains?purpose=tracking_links", nil)["data"].([]any)); n != 1 {
		t.Fatalf("filtered: %d", n)
	}

	// The classifier's lookup has a name of its own too.
	if got := owner.must(200, "GET", base+"/domain-lookups/gmail.com", nil); got["normalized_domain"] != "gmail.com" {
		t.Fatalf("lookup: %v", got)
	}

	hub := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})["id"].(string)
	var google string
	if err := e.pool.QueryRow(ctx, `INSERT INTO oauth_connections (workspace_id, provider, name, secret) VALUES ($1, 'google', 'Acme Google', '\x00') RETURNING id::text`, ws).Scan(&google); err != nil {
		t.Fatal(err)
	}
	conns := owner.must(200, "GET", base+"/connections", nil)["data"].([]any)
	if len(conns) != 2 || conns[0].(map[string]any)["id"] != hub || conns[0].(map[string]any)["kind"] != "integration" ||
		conns[1].(map[string]any)["id"] != google || conns[1].(map[string]any)["kind"] != "mailbox_oauth" {
		t.Fatalf("connections: %v", conns)
	}
	if n := len(owner.must(200, "GET", base+"/connections?category=mailboxes", nil)["data"].([]any)); n != 1 {
		t.Fatalf("mailbox connections: %d", n)
	}
}
