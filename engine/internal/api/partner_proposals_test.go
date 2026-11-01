package api_test

import (
	"context"
	"fmt"
	"github.com/outflareai/outflare/engine/internal/track"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func proposalFixture() map[string]any {
	return map[string]any{"title": "Northpeak launch collaboration", "brief": "Publish a video and a website mention.", "partner_site_name": "Creator Journal", "partner_site_url": "https://creator.example/", "partner_site_icon_url": "https://creator.example/icon.png", "reply_to": "partners@northpeak.example", "deliverables": []any{map[string]any{"id": "video", "title": "Hands-on video", "channel": "Any video platform", "live_days": 30}, map[string]any{"id": "website", "title": "Website mention", "channel": "Website"}}, "milestones": []any{map[string]any{"id": "first", "title": "Publication approved", "deliverable_id": "video", "amount_cents": 5000, "benefit": "One month Pro"}, map[string]any{"id": "remainder", "title": "30-day placement completed", "deliverable_id": "video", "amount_cents": 5000, "wait_days": 30}}}
}
func TestPartnerProposals(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	events := track.Capture(t)
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak"})
	id := owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Partners", "kind": "affiliate", "brand": "northpeak", "auto_accept": true})["id"].(string)
	admin := base + "/partner-programs/" + id
	fake := httptest.NewServer(&fakeResend{})
	defer fake.Close()
	e.integrations.Client.BaseURLs["resend"] = fake.URL
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "resend", "credentials": map[string]any{"api_key": "re_test_not_a_real_key"}})
	if _, err := e.pool.Exec(ctx, `INSERT INTO email_domains(workspace_id,provider_id,domain,status) SELECT workspace_id,id,'northpeak.example','verified' FROM email_providers WHERE workspace_id=$1`, ws); err != nil {
		t.Fatal(err)
	}
	owner.must(200, "PUT", admin+"/portal", map[string]any{"enabled": true, "settings": map[string]any{"email_from": "partners@northpeak.example"}})
	invalid := proposalFixture()
	invalid["partner_site_url"] = "javascript:alert(1)"
	owner.must(400, "POST", admin+"/proposals", map[string]any{"email": "maya@creator.example", "config": invalid})
	invalid = proposalFixture()
	invalid["partner_site_icon_url"] = "http://creator.example/favicon.ico"
	owner.must(400, "POST", admin+"/proposals", map[string]any{"email": "maya@creator.example", "config": invalid})
	config := proposalFixture()
	p := owner.must(200, "POST", admin+"/proposals", map[string]any{"email": "maya@creator.example", "name": "Maya", "config": config})
	pid := p["id"].(string)
	owner.must(400, "PATCH", admin, map[string]any{"currency": "EUR"})
	// Reusing an existing email does not create a duplicate membership.
	owner.must(200, "POST", admin+"/proposals", map[string]any{"email": "maya@creator.example", "config": proposalFixture()})
	sess := owner.must(201, "POST", admin+"/widget-sessions", map[string]any{"customer_id": "maya", "email": "maya@creator.example"})
	partner := &client{t: t, h: e.h, bearer: sess["token"].(string)}
	otherSess := owner.must(201, "POST", admin+"/widget-sessions", map[string]any{"customer_id": "someone_else"})
	other := &client{t: t, h: e.h, bearer: otherSess["token"].(string)}
	portal := "/p/" + id + "/api"
	if n := len(partner.must(200, "GET", portal+"/proposals", nil)["data"].([]any)); n != 0 {
		t.Fatal("draft leaked into portal")
	}
	invited := owner.must(200, "POST", admin+"/proposals/"+pid, map[string]any{"action": "invite", "version": 1})
	u, _ := url.Parse(invited["invitation_url"].(string))
	token := strings.TrimPrefix(u.Fragment, "invite=")
	public := &client{t: t, h: e.h}
	preview := public.must(200, "POST", portal+"/proposal-preview", map[string]any{"token": token})
	if preview["site_name"] != "Creator Journal" || preview["site_url"] != "https://creator.example/" {
		t.Fatalf("teaser website missing: %v", preview)
	}
	for _, key := range []string{"config", "title", "brief", "milestones", "deliverables", "terms", "currency", "reply_to", "recipient", "record_id", "progress"} {
		if _, exposed := preview[key]; exposed {
			t.Errorf("private proposal field exposed before sign-in: %s", key)
		}
	}
	if len(preview) != 4 {
		t.Fatalf("teaser allowlist changed: %v", preview)
	}
	if strings.Contains(jsonString(preview), "maya@creator.example") {
		t.Fatal("recipient address exposed in preview")
	}
	public.must(200, "POST", portal+"/proposal-sign-in", map[string]any{"token": token})
	var text string
	if err := e.pool.QueryRow(ctx, `SELECT text_body FROM emails WHERE workspace_id=$1 AND 'maya@creator.example'=ANY(to_addresses) ORDER BY created_at DESC LIMIT 1`, ws).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "?proposal="+pid+"#signin=") {
		t.Fatal("email did not preserve proposal destination")
	}
	owner.must(200, "POST", admin+"/proposals/"+pid, map[string]any{"action": "revise", "version": 1, "config": config})
	public.must(404, "POST", portal+"/proposal-preview", map[string]any{"token": token})
	owner.must(200, "POST", admin+"/proposals/"+pid, map[string]any{"action": "invite", "version": 2})
	other.must(404, "POST", portal+"/proposals/"+pid, map[string]any{"action": "accept", "version": 2})
	if len(other.must(200, "GET", portal+"/proposals", nil)["data"].([]any)) != 0 {
		t.Fatal("another partner saw proposal")
	}
	partner.must(400, "POST", portal+"/proposals/"+pid, map[string]any{"action": "accept", "version": 1})
	partner.must(200, "POST", portal+"/proposals/"+pid, map[string]any{"action": "accept", "version": 2})
	details := partner.must(200, "GET", portal+"/proposals", nil)["data"].([]any)
	fullOffer := false
	for _, row := range details {
		offer := row.(map[string]any)
		if offer["id"] == pid {
			c := offer["config"].(map[string]any)
			fullOffer = c["title"] == config["title"] && len(c["milestones"].([]any)) == 2
		}
	}
	if !fullOffer {
		t.Fatal("authenticated recipient did not receive the full offer")
	}
	owner.must(400, "POST", admin+"/proposals/"+pid, map[string]any{"action": "revise", "version": 2, "config": config})
	partner.must(200, "POST", portal+"/proposals/"+pid, map[string]any{"action": "billing", "version": 2, "note": "Invoice reference INV-EXAMPLE"})
	owner.must(400, "POST", admin+"/proposals/"+pid, map[string]any{"action": "approve", "version": 2, "milestone_id": "first"})
	partner.must(200, "POST", portal+"/proposals/"+pid, map[string]any{"action": "submit", "version": 2, "deliverable_id": "video", "url": "https://video.example/demo"})
	owner.must(200, "POST", admin+"/proposals/"+pid, map[string]any{"action": "verify", "version": 2, "deliverable_id": "video"})
	owner.must(200, "POST", admin+"/proposals/"+pid, map[string]any{"action": "approve", "version": 2, "milestone_id": "first"})
	owner.must(400, "POST", admin+"/proposals/"+pid, map[string]any{"action": "approve", "version": 2, "milestone_id": "first"})
	owner.must(400, "POST", admin+"/proposals/"+pid, map[string]any{"action": "approve", "version": 2, "milestone_id": "remainder"})
	var count int
	var cash int64
	if err := e.pool.QueryRow(ctx, `SELECT count(*),COALESCE(sum(amount_cents),0) FROM partner_rewards WHERE workspace_id=$1 AND membership_id=$2`, ws, sess["partner_id"]).Scan(&count, &cash); err != nil || count != 2 || cash != 5000 {
		t.Fatalf("rewards=%d cash=%d err=%v", count, cash, err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE partner_proposals SET progress=jsonb_set(progress,'{deliverables,video,verified_at}',to_jsonb($3::text)) WHERE workspace_id=$1 AND id=$2`, ws, pid, time.Now().Add(-31*24*time.Hour).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	owner.must(200, "POST", admin+"/proposals/"+pid, map[string]any{"action": "approve", "version": 2, "milestone_id": "remainder"})
	owner.must(400, "POST", admin+"/proposals/"+pid, map[string]any{"action": "complete", "version": 2})
	partner.must(200, "POST", portal+"/proposals/"+pid, map[string]any{"action": "submit", "version": 2, "deliverable_id": "website", "url": "https://creator.example/mention"})
	owner.must(200, "POST", admin+"/proposals/"+pid, map[string]any{"action": "verify", "version": 2, "deliverable_id": "website"})
	owner.must(200, "POST", admin+"/proposals/"+pid, map[string]any{"action": "complete", "version": 2})
	// Brand-scoped access is checked by the parent program, not by guessing ids.
	tokenRead := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Read only", "scopes": []string{"partners.read"}})["token"].(string)
	reader := &client{t: t, h: e.h, bearer: tokenRead}
	reader.must(403, "POST", admin+"/proposals", map[string]any{"email": "other@creator.example", "config": config})
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Other brand"})
	restrictedToken := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Other brand team", "brand": "other-brand", "scopes": []string{"partners.read", "partners.manage"}})["token"].(string)
	restricted := &client{t: t, h: e.h, bearer: restrictedToken}
	restricted.must(404, "GET", admin+"/proposals", nil)
	restricted.must(404, "POST", admin+"/proposals/"+pid, map[string]any{"action": "invite", "version": 2})
	record := p["record_id"].(string)
	version := owner.must(200, "GET", base+"/records/"+record, nil)["version"]
	owner.must(200, "DELETE", base+"/records/"+record+"?version="+fmt.Sprint(version), nil)
	var remaining int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM partner_proposals WHERE workspace_id=$1`, ws).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("private agreements remained after erasure: %d %v", remaining, err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM partner_rewards WHERE workspace_id=$1`, ws).Scan(&remaining); err != nil || remaining != 3 {
		t.Fatalf("financial ledger lost after erasure: %d %v", remaining, err)
	}
	found := false
	for _, event := range events() {
		if event.Name == "partner_proposal.updated" {
			found = true
		}
	}
	if !found {
		t.Fatal("proposal event missing")
	}
}
func TestPartnerProposalQuote(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	id := owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Partners", "kind": "affiliate"})["id"].(string)
	admin := base + "/partner-programs/" + id
	owner.must(200, "PUT", admin+"/portal", map[string]any{"enabled": true})
	config := proposalFixture()
	config["request_quote"] = true
	p := owner.must(200, "POST", admin+"/proposals", map[string]any{"email": "quote@creator.example", "config": config})
	pid := p["id"].(string)
	owner.must(200, "POST", admin+"/proposals/"+pid, map[string]any{"action": "invite", "version": 1})
	sess := owner.must(201, "POST", admin+"/widget-sessions", map[string]any{"email": "quote@creator.example"})
	partner := &client{t: t, h: e.h, bearer: sess["token"].(string)}
	portal := "/p/" + id + "/api/proposals/" + pid
	partner.must(400, "POST", portal, map[string]any{"action": "accept", "version": 1})
	partner.must(200, "POST", portal, map[string]any{"action": "quote", "version": 1, "amount_cents": 12000, "note": "My rate for the brief."})
	config["request_quote"] = false
	owner.must(200, "POST", admin+"/proposals/"+pid, map[string]any{"action": "revise", "version": 1, "config": config})
	owner.must(200, "POST", admin+"/proposals/"+pid, map[string]any{"action": "invite", "version": 2})
	partner.must(200, "POST", portal, map[string]any{"action": "decline", "version": 2})
}
