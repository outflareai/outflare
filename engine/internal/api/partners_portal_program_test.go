package api_test

import (
	"context"
	"fmt"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/track"
	"testing"
)

func TestPartnerPortalBrandHelpAndHandoff(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	prog := owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Northpeak partners", "kind": "affiliate", "landing_url": "https://northpeak.example/", "auto_accept": true, "min_payout_cents": 100, "terms_url": "https://northpeak.example/terms"})
	id := prog["id"].(string)
	admin := base + "/partner-programs/" + id
	events := track.Capture(t)
	settings := map[string]any{"home_url": "https://northpeak.example/", "support_email": "partners@northpeak.example", "header_links": []any{map[string]any{"label": "Website", "url": "https://northpeak.example/"}}, "footer_links": []any{map[string]any{"label": "Privacy", "url": "https://northpeak.example/privacy"}}, "landing_pages": []any{map[string]any{"label": "Signup", "url": "https://northpeak.example/signup"}}, "terms_revision": "2026-11", "payout_terms": "We pay approved balances monthly.", "help_articles": []any{map[string]any{"id": "payment_dates", "title": "Payment dates", "category": "payouts", "body": "We process payments monthly. <script>plain text</script>"}}}
	owner.must(200, "PUT", admin+"/portal", map[string]any{"enabled": true, "settings": settings})
	for _, bad := range []map[string]any{{"home_url": "javascript:alert(1)"}, {"support_email": "bad"}, {"header_links": []any{map[string]any{"label": "Bad", "url": "http://northpeak.example"}}}, {"landing_pages": []any{map[string]any{"label": "External", "url": "https://other.example/"}}}, {"help_articles": []any{map[string]any{"id": "bad", "title": "Title", "category": "oops", "body": "x"}}}} {
		owner.must(400, "PUT", admin+"/portal", map[string]any{"settings": bad})
	}
	portal := &portalClient{t: t, h: e.h, base: "https://outflare.test/p/" + id + "/"}
	info := portal.must(200, "GET", "api/program", nil)
	if info["home_url"] != "https://northpeak.example/" || info["support_email"] != "partners@northpeak.example" || len(info["help_articles"].([]any)) != 1 {
		t.Fatalf("public branding=%v", info)
	}
	owner.must(400, "POST", admin+"/portal-sessions", map[string]any{"email": "maya@northpeak.example"})
	sess := owner.must(201, "POST", admin+"/portal-sessions", map[string]any{"customer_id": "maya-user", "email": "maya@northpeak.example", "name": "Maya"})
	url := sess["sign_in_url"].(string)
	m := signInLink.FindStringSubmatch(url)
	if m == nil || sess["expires_in_seconds"] != float64(60) {
		t.Fatalf("handoff=%v", sess)
	}
	otherProg := owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Other", "kind": "affiliate"})["id"].(string)
	wrong := &portalClient{t: t, h: e.h, base: "https://outflare.test/p/" + otherProg + "/"}
	wrong.must(404, "POST", "api/session", map[string]any{"token": m[1]})
	portal.must(200, "POST", "api/session", map[string]any{"token": m[1]})
	portal.must(404, "POST", "api/session", map[string]any{"token": m[1]})
	me := portal.must(200, "GET", "api/me", nil)
	if me["requires_terms"] != true {
		t.Fatal("new provisioned partner bypassed terms")
	}
	portal.must(403, "PATCH", "api/me", map[string]any{"payout_method": "PayPal"})
	portal.must(409, "POST", "api/accept-terms", map[string]any{"accept": true, "version": "old"})
	version := me["program"].(map[string]any)["terms_version"]
	portal.must(200, "POST", "api/accept-terms", map[string]any{"accept": true, "version": version})
	portal.must(200, "PATCH", "api/me", map[string]any{"payout_method": "PayPal: maya@northpeak.example"})
	// Stable customer id survives email changes and uses the same embedded membership.
	again := owner.must(201, "POST", admin+"/widget-sessions", map[string]any{"customer_id": "maya-user", "email": "new@northpeak.example"})
	if again["partner_id"] != sess["partner_id"] {
		t.Fatal("customer identity duplicated")
	}
	// An expired handoff cannot be exchanged.
	expired := owner.must(201, "POST", admin+"/portal-sessions", map[string]any{"customer_id": "maya-user"})
	token := signInLink.FindStringSubmatch(expired["sign_in_url"].(string))[1]
	if _, err := e.pool.Exec(context.Background(), `UPDATE partner_sign_in_links SET expires_at=now()-interval '1 second' WHERE token_hash=$1`, auth.HashToken(token)); err != nil {
		t.Fatal(err)
	}
	portal.must(404, "POST", "api/session", map[string]any{"token": token})
	// Editing a help article does not invalidate consent. A new terms revision does.
	settings["payout_terms"] = "We pay twice monthly."
	owner.must(200, "PUT", admin+"/portal", map[string]any{"settings": settings})
	if portal.must(200, "GET", "api/me", nil)["requires_terms"] != false {
		t.Fatal("ordinary edit invalidated terms")
	}
	settings["terms_revision"] = "2026-12"
	owner.must(200, "PUT", admin+"/portal", map[string]any{"settings": settings})
	me = portal.must(200, "GET", "api/me", nil)
	if me["requires_terms"] != true {
		t.Fatal("changed terms did not require consent")
	}
	portal.must(403, "POST", "api/payouts", map[string]any{})
	portal.must(409, "POST", "api/accept-terms", map[string]any{"accept": true, "version": version})
	portal.must(200, "POST", "api/accept-terms", map[string]any{"accept": true, "version": me["program"].(map[string]any)["terms_version"]})
	// A request allocates eligible cash once; it never moves money.
	owner.must(201, "POST", base+"/partners/"+sess["partner_id"].(string)+"/fees", map[string]any{"amount_cents": 500, "note": "Tutorial"})
	payout := portal.must(201, "POST", "api/payouts", map[string]any{})
	if payout["status"] != "due" {
		t.Fatal(payout)
	}
	portal.must(400, "POST", "api/payouts", map[string]any{})
	payouts := owner.must(200, "GET", base+"/partner-payouts", nil)["data"].([]any)
	if len(payouts) != 1 || payouts[0].(map[string]any)["amount_cents"] != float64(500) {
		t.Fatal(payouts)
	}
	if me = portal.must(200, "GET", "api/me", nil); me["stats"].(map[string]any)["balance_cents"] != float64(0) {
		t.Fatal("allocated cash still available")
	}
	found := map[string]bool{}
	for _, event := range events() {
		found[event.Name] = true
	}
	for _, name := range []string{"partner.portal_handoff_created", "partner.terms_accepted", "partner_payout.created"} {
		if !found[name] {
			t.Errorf("missing event %s", name)
		}
	}
	// Closed portals cannot create hosted handoffs.
	owner.must(200, "PUT", admin+"/portal", map[string]any{"enabled": false})
	owner.must(400, "POST", admin+"/portal-sessions", map[string]any{"customer_id": "maya-user"})
}

func TestPartnerTaskProfileErasure(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	id := owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Partners", "kind": "affiliate", "auto_accept": true})["id"].(string)
	admin := base + "/partner-programs/" + id
	sess := owner.must(201, "POST", admin+"/widget-sessions", map[string]any{"customer_id": "partner_to_erase"})
	partner := &client{t: t, h: e.h, bearer: sess["token"].(string)}
	portal := "/p/" + id + "/api"
	partner.must(200, "POST", portal+"/audiences", map[string]any{"url": "https://maya.example/", "kind": "website", "role": "owner"})
	task := owner.must(200, "POST", admin+"/tasks", map[string]any{"title": "Write a guide", "description": "Publish a guide.", "reward_kind": "cash", "amount_cents": 100})["id"].(string)
	sub := partner.must(200, "POST", portal+"/tasks/"+task, map[string]any{"action": "apply"})
	partner.must(200, "POST", portal+"/tasks/"+task, map[string]any{"action": "submit", "evidence_url": "https://maya.example/guide"})
	owner.must(200, "POST", admin+"/task-submissions/"+sub["id"].(string), map[string]any{"action": "approve"})
	rec := owner.must(200, "GET", base+"/partners/"+sess["partner_id"].(string), nil)["record_id"].(string)
	v := owner.must(200, "GET", base+"/records/"+rec, nil)["version"]
	owner.must(200, "DELETE", base+"/records/"+rec+"?version="+fmt.Sprint(v), nil)
	for _, table := range []string{"partner_audiences", "partner_task_submissions"} {
		var n int
		if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table+` WHERE workspace_id=$1`, ws).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s personal data remained: %d %v", table, n, err)
		}
	}
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM partner_rewards WHERE workspace_id=$1 AND amount_cents=100`, ws).Scan(&n); err != nil || n != 1 {
		t.Fatalf("business ledger lost: %d %v", n, err)
	}
}
