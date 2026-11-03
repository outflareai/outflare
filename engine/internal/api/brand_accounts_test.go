package api_test

import (
	"context"
	"strings"
	"testing"
)

// A brand's external accounts: recorded unverified, kept healthy or at
// needs attention by the attempts tools record, with incidents that
// repeat, chain to an upstream identity and resolve; secrets in the vault
// are never read back, and an agent asking to set one waits for a person.
func TestBrandAccounts(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	path := "/v1/brands/" + brand + "/accounts"

	st, res := owner.do("POST", path, map[string]any{"platform": "g2", "label": "G2 profile"})
	if st != 400 {
		t.Fatalf("sign_in required: %d %v", st, res)
	}
	google := owner.must(201, "POST", path, map[string]any{"platform": "google", "label": "The brand's Google account", "identity": "ops@acme.example", "sign_in": "local_password"})
	gid := google["id"].(string)
	if google["status"] != "unverified" || len(google["secrets"].([]any)) != 0 {
		t.Fatalf("new account: %v", google)
	}
	g2 := owner.must(201, "POST", path, map[string]any{"platform": "g2", "label": "G2 profile", "sign_in": "social_sign_in", "depends_on": gid, "url": "https://www.g2.com/products/acme"})
	g2id := g2["id"].(string)
	st, _ = owner.do("POST", path, map[string]any{"platform": "x", "label": "X", "sign_in": "social_sign_in", "depends_on": "00000000-0000-0000-0000-000000000000"})
	if st != 400 {
		t.Fatalf("depends_on another brand's account: %d", st)
	}

	// The vault: set, listed by kind only, never returned anywhere.
	owner.must(200, "PUT", path+"/"+gid+"/secrets/password", map[string]any{"value": "correct horse battery staple"})
	got := owner.must(200, "GET", path+"/"+gid, nil)
	if s := got["secrets"].([]any); len(s) != 1 || s[0] != "password" {
		t.Fatalf("secrets: %v", s)
	}
	if strings.Contains(jsonText(got), "correct horse") {
		t.Fatal("the secret's value came back")
	}
	var sealed []byte
	if err := e.pool.QueryRow(ctx, `SELECT sealed FROM brand_account_secrets WHERE account_id = $1 AND kind = 'password'`, gid).Scan(&sealed); err != nil || strings.Contains(string(sealed), "correct horse") {
		t.Fatalf("the vault stores the value sealed: %v", err)
	}
	st, _ = owner.do("PUT", path+"/"+gid+"/secrets/pin", map[string]any{"value": "1234"})
	if st != 400 {
		t.Fatalf("unknown secret kind: %d", st)
	}

	// A use that works makes the account healthy; the list shows its
	// dependents.
	r := owner.must(201, "POST", path+"/"+gid+"/attempts", map[string]any{"purpose": "sign_in", "outcome": "authenticated", "evidence": map[string]any{"page_title": "Google Account"}})
	if acc := r["account"].(map[string]any); acc["status"] != "healthy" || acc["last_verified_at"] == nil || acc["dependents"] != 1.0 || r["incident_id"] != nil {
		t.Fatalf("after authenticating: %v", r)
	}
	// A refused credential opens a critical incident and needs attention;
	// a repeat counts up on the same incident.
	r = owner.must(201, "POST", path+"/"+gid+"/attempts", map[string]any{"purpose": "sign_in", "outcome": "credential_failed", "evidence": map[string]any{"error_code": "invalid_password"}})
	inc := r["incident_id"].(string)
	if acc := r["account"].(map[string]any); acc["status"] != "needs_attention" || acc["open_incidents"] != 1.0 {
		t.Fatalf("after a refused credential: %v", r)
	}
	r = owner.must(201, "POST", path+"/"+gid+"/attempts", map[string]any{"purpose": "sign_in", "outcome": "credential_failed"})
	if r["incident_id"] != inc {
		t.Fatalf("a repeat opens no second incident: %v", r)
	}
	incidents := owner.must(200, "GET", "/v1/brands/"+brand+"/account-incidents", nil)["data"].([]any)
	if len(incidents) != 1 || incidents[0].(map[string]any)["repeats"] != 2.0 || incidents[0].(map[string]any)["severity"] != "critical" || incidents[0].(map[string]any)["kind"] != "credential" {
		t.Fatalf("incidents: %v", incidents)
	}
	// The account that signs in through it fails too: its incident chains
	// to the identity's as the root.
	r = owner.must(201, "POST", path+"/"+g2id+"/attempts", map[string]any{"purpose": "sign_in", "outcome": "session_expired"})
	chained := owner.must(200, "GET", "/v1/brands/"+brand+"/account-incidents", nil)["data"].([]any)
	var rooted bool
	for _, x := range chained {
		if m := x.(map[string]any); m["account_id"] == g2id && m["root_id"] == inc {
			rooted = true
		}
	}
	if !rooted || len(chained) != 2 {
		t.Fatalf("chained incidents: %v", chained)
	}
	if l := owner.must(200, "GET", path+"?view=needs_attention", nil)["data"].([]any); len(l) != 2 {
		t.Fatalf("needs attention: %v", l)
	}
	if l := owner.must(200, "GET", path+"?view=dependencies", nil)["data"].([]any); len(l) != 1 || l[0].(map[string]any)["id"] != gid {
		t.Fatalf("dependencies: %v", l)
	}

	// Resolving the last incident puts the account back to unverified; an
	// authenticated use then resolves the dependent's incident itself.
	st, _ = owner.do("PATCH", "/v1/brands/"+brand+"/account-incidents/"+inc, map[string]any{"status": "snoozed"})
	if st != 400 {
		t.Fatalf("snoozing needs next_retry_at: %d", st)
	}
	owner.must(200, "PATCH", "/v1/brands/"+brand+"/account-incidents/"+inc, map[string]any{"status": "resolved", "resolution": "Password rotated"})
	if acc := owner.must(200, "GET", path+"/"+gid, nil); acc["status"] != "unverified" || acc["open_incidents"] != 0.0 {
		t.Fatalf("after resolving: %v", acc)
	}
	owner.must(201, "POST", path+"/"+g2id+"/attempts", map[string]any{"purpose": "session_use", "outcome": "authenticated"})
	if l := owner.must(200, "GET", "/v1/brands/"+brand+"/account-incidents", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("incidents after recovery: %v", l)
	}

	// A wrong identity blocks the account, and stays blocked through an
	// authenticated use until a person puts it back.
	r = owner.must(201, "POST", path+"/"+g2id+"/attempts", map[string]any{"purpose": "session_use", "outcome": "wrong_identity"})
	if acc := r["account"].(map[string]any); acc["status"] != "blocked" {
		t.Fatalf("wrong identity: %v", acc)
	}
	owner.must(201, "POST", path+"/"+g2id+"/attempts", map[string]any{"purpose": "session_use", "outcome": "authenticated"})
	if acc := owner.must(200, "GET", path+"/"+g2id, nil); acc["status"] != "blocked" {
		t.Fatalf("blocked stays a person's call: %v", acc)
	}
	owner.must(200, "PATCH", path+"/"+g2id, map[string]any{"status": "unverified"})
	owner.must(200, "PATCH", path+"/"+g2id, map[string]any{"status": "archived"})
	if l := owner.must(200, "GET", path, nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("archived accounts leave the list: %v", l)
	}
	owner.must(204, "DELETE", path+"/"+gid+"/secrets/password", nil)
	owner.must(404, "DELETE", path+"/"+gid+"/secrets/password", nil)

	// Another brand's path never sees them.
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	owner.must(404, "GET", "/v1/brands/"+other+"/accounts/"+gid, nil)
}

// An agent records attempts within its grants, but setting a secret
// always waits for a person.
func TestBrandAccountSecretsAlwaysNeedAPerson(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	path := "/v1/brands/" + brand + "/accounts"
	acc := owner.must(201, "POST", path, map[string]any{"platform": "producthunt", "label": "Product Hunt", "sign_in": "local_password"})
	agent, agentID := gatedAgent(t, owner, base, "Accounts agent", "accounts.manage")
	owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "brands/*/accounts/**", "mode": "run"})
	st, res := agent.do("POST", path+"/"+acc["id"].(string)+"/attempts", map[string]any{"purpose": "verify", "outcome": "unresolved"})
	if st != 201 {
		t.Fatalf("an attempt within the grant runs: %d %v", st, res)
	}
	st, res = agent.do("PUT", path+"/"+acc["id"].(string)+"/secrets/password", map[string]any{"value": "hunter2"})
	if st != 202 || res["queued"] == true {
		t.Fatalf("a secret always waits for a person: %d %v", st, res)
	}
	if got := owner.must(200, "GET", path+"/"+acc["id"].(string), nil); len(got["secrets"].([]any)) != 0 {
		t.Fatalf("the held secret must not be in the vault: %v", got["secrets"])
	}
}
