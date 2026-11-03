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

// Human takeover: short-lived, exclusive, started and handed back by a
// person, with the identity checked on handback; and offboarding, which
// a person does once and that leaves the owner a list.
func TestBrandAccountTakeoversAndOffboarding(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	path := "/v1/brands/" + brand + "/accounts"
	acc := owner.must(201, "POST", path, map[string]any{"platform": "producthunt", "label": "Product Hunt", "identity": "ops@acme.example", "sign_in": "local_password"})
	id := acc["id"].(string)
	owner.must(200, "PUT", path+"/"+id+"/secrets/password", map[string]any{"value": "correct horse battery staple"})
	agent, agentID := gatedAgent(t, owner, base, "Accounts agent", "accounts.manage", "accounts.read")
	owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "brands/*/accounts/**", "mode": "run"})
	owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "brands/*/account-takeovers/**", "mode": "run"})

	// The tool meets MFA: an incident, then a request for a person.
	r := agent.must(201, "POST", path+"/"+id+"/attempts", map[string]any{"purpose": "sign_in", "outcome": "mfa_required"})
	inc := r["incident_id"].(string)
	st, res := agent.do("POST", path+"/"+id+"/takeovers", map[string]any{"reason": "mfa", "note": "Approve the sign-in on the phone", "incident_id": inc})
	if st != 201 || res["status"] != "requested" {
		t.Fatalf("request: %d %v", st, res)
	}
	tk := res["id"].(string)
	tpath := "/v1/brands/" + brand + "/account-takeovers/"
	if st, _ := agent.do("POST", path+"/"+id+"/takeovers", map[string]any{"reason": "captcha"}); st != 409 {
		t.Fatalf("exclusive: a second request is %d", st)
	}
	if st, _ := agent.do("POST", tpath+tk+"/start", nil); st != 403 {
		t.Fatalf("only a person starts a takeover: %d", st)
	}
	if st, _ := owner.do("POST", tpath+tk+"/start", map[string]any{"minutes": 120}); st != 400 {
		t.Fatalf("a takeover is short: %d", st)
	}
	started := owner.must(200, "POST", tpath+tk+"/start", nil)
	if started["status"] != "active" || started["holder_id"] == nil || started["expires_at"] == nil {
		t.Fatalf("started: %v", started)
	}
	if l := agent.must(200, "GET", tpath[:len(tpath)-1], nil)["data"].([]any); len(l) != 1 || l[0].(map[string]any)["status"] != "active" {
		t.Fatalf("under way: %v", l)
	}
	// Handing back with the brand's identity: the account is healthy and
	// the MFA incident is resolved.
	hb := owner.must(200, "POST", tpath+tk+"/handback", map[string]any{"identity": "Ops@acme.example", "note": "Approved on the phone"})
	if hb["status"] != "handed_back" || hb["handback_outcome"] != "authenticated" {
		t.Fatalf("handback: %v", hb)
	}
	if got := owner.must(200, "GET", path+"/"+id, nil); got["status"] != "healthy" || got["open_incidents"] != 0.0 {
		t.Fatalf("after handback: %v", got)
	}
	if st, _ := owner.do("POST", tpath+tk+"/handback", nil); st != 409 {
		t.Fatalf("handing back twice: %d", st)
	}
	// A takeover left running expires on its own; a wrong identity on
	// handback blocks the account.
	tk2 := agent.must(201, "POST", path+"/"+id+"/takeovers", map[string]any{"reason": "captcha"})["id"].(string)
	owner.must(200, "POST", tpath+tk2+"/start", map[string]any{"minutes": 1})
	if _, err := e.pool.Exec(ctx, `UPDATE brand_account_takeovers SET expires_at = now() - interval '1 minute' WHERE id = $1`, tk2); err != nil {
		t.Fatal(err)
	}
	if got := owner.must(200, "GET", tpath+tk2, nil); got["status"] != "expired" {
		t.Fatalf("expired: %v", got)
	}
	tk3 := agent.must(201, "POST", path+"/"+id+"/takeovers", map[string]any{"reason": "consent"})["id"].(string)
	owner.must(200, "POST", tpath+tk3+"/start", nil)
	hb = owner.must(200, "POST", tpath+tk3+"/handback", map[string]any{"identity": "someone@example.com"})
	if hb["handback_outcome"] != "wrong_identity" {
		t.Fatalf("wrong identity on handback: %v", hb)
	}
	if got := owner.must(200, "GET", path+"/"+id, nil); got["status"] != "blocked" {
		t.Fatalf("a wrong identity blocks: %v", got)
	}
	// An agent may withdraw its own waiting request.
	tk4 := agent.must(201, "POST", path+"/"+id+"/takeovers", map[string]any{"reason": "other"})["id"].(string)
	if c := agent.must(200, "POST", tpath+tk4+"/cancel", nil); c["status"] != "cancelled" {
		t.Fatalf("cancel: %v", c)
	}
	if l := owner.must(200, "GET", tpath[:len(tpath)-1]+"?status=all", nil)["data"].([]any); len(l) != 4 {
		t.Fatalf("history: %d", len(l))
	}

	// Offboarding: an agent's ask waits for a person; a person's archives
	// everything, empties the vault, revokes the brand's grants and lists
	// the owner's work.
	bound := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Brand-bound agent", "scopes": []string{"accounts.manage"}, "brand": brand, "action_gate": true})["api_token"].(map[string]any)["id"].(string)
	owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": bound, "path": "brands/**", "mode": "run", "brand": brand})
	agent.must(201, "POST", path+"/"+id+"/takeovers", map[string]any{"reason": "mfa"})
	st, res = agent.do("POST", path+"/offboard", map[string]any{"note": "Brand sold"})
	if st != 202 || res["queued"] == true {
		t.Fatalf("offboarding always waits for a person: %d %v", st, res)
	}
	rep := owner.must(200, "POST", path+"/offboard", map[string]any{"note": "Brand sold"})
	if rep["archived"] != 1.0 || rep["secrets_removed"] != 1.0 || rep["takeovers_cancelled"] != 1.0 || rep["incidents_resolved"] != 1.0 || rep["grants_revoked"] != 1.0 {
		t.Fatalf("report: %v", rep)
	}
	needs := rep["needs_owner"].([]any)
	if len(needs) != 1 || needs[0].(map[string]any)["action"] != "change_password" {
		t.Fatalf("needs owner: %v", needs)
	}
	if got := owner.must(200, "GET", path+"/"+id, nil); got["status"] != "archived" || len(got["secrets"].([]any)) != 0 {
		t.Fatalf("after offboarding: %v", got)
	}
	var open int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM action_grants WHERE workspace_id = $1 AND revoked_at IS NULL`, ws).Scan(&open); err != nil || open != 2 {
		t.Fatalf("the brand's grant is revoked, the workspace-wide ones stay: %d %v", open, err)
	}
	if st, _ := agent.do("POST", path+"/"+id+"/takeovers", map[string]any{"reason": "mfa"}); st != 409 {
		t.Fatalf("an archived account takes no takeover: %d", st)
	}
}
