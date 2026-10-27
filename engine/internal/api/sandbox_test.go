package api_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestTestWorkspaceRunsWithoutTheOutsideWorld(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	e.runJobs(t)
	owner, live := setupOwner(t, e.h)
	ctx := context.Background()

	owner.must(400, "POST", "/v1/workspaces", map[string]any{"name": "Nope", "sample_data": true})
	ws := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Sandbox", "test": true, "sample_data": true})
	if ws["test"] != true {
		t.Fatalf("workspace: %v", ws)
	}
	id := ws["id"].(string)
	base := "/v1/workspaces/" + id

	// Every answer from a test workspace says so; a live one's does not.
	for path, want := range map[string]string{base: "true", "/v1/workspaces/" + live: ""} {
		req := httptest.NewRequest("GET", path, nil)
		req.AddCookie(owner.cookie)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if got := rec.Header().Get("Outflare-Test-Workspace"); got != want {
			t.Errorf("%s: header %q, want %q", path, got, want)
		}
	}

	// The sample mailboxes connect at once and their domain looks healthy.
	boxes := owner.must(200, "GET", base+"/mailboxes", nil)["data"].([]any)
	if len(boxes) != 3 {
		t.Fatalf("sample mailboxes: %v", boxes)
	}
	for _, m := range boxes {
		got := waitFor(t, owner, base+"/mailboxes/"+m.(map[string]any)["id"].(string), func(m map[string]any) bool { return m["status"] == "active" })
		if got["provider"] != "simulated" {
			t.Fatalf("mailbox: %v", got)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var spf *string
		if err := e.pool.QueryRow(ctx, `SELECT spf FROM sending_domains WHERE workspace_id = $1 AND domain = 'outreach.example'`, id).Scan(&spf); err != nil {
			t.Fatal(err)
		}
		if spf != nil && *spf == "pass" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("domain check: %v", spf)
		}
		time.Sleep(25 * time.Millisecond)
	}
	leadList := owner.must(200, "GET", base+"/leads?limit=100", nil)["data"].([]any)
	if len(leadList) != 10 {
		t.Fatalf("sample leads: %d", len(leadList))
	}

	// A real mailbox is turned into a simulated one; a simulated one is
	// refused in a live workspace.
	if code, res := owner.raw("POST", base+"/mailboxes/import", "text/csv",
		"email,password,smtp_host,smtp_port,imap_host,imap_port\nreal@acme.test,pw,smtp.acme.test,465,imap.acme.test,993\n"); code != 200 || res["created"] != 1.0 {
		t.Fatalf("import: %d %v", code, res)
	}
	if code, res := owner.raw("POST", "/v1/workspaces/"+live+"/mailboxes/import", "text/csv",
		"email,password,smtp_host,smtp_port,imap_host,imap_port\nfake@acme.test,pw,simulated.outflare.invalid,465,simulated.outflare.invalid,993\n"); code != 200 || res["created"] != 0.0 {
		t.Fatalf("live import of a simulated mailbox: %d %v", code, res)
	}

	// Verification is made up from the address, and costs nothing.
	res, err := e.verify.Verify(ctx, id, []string{"ana@northwind.example", "risky@tailspin.example", "undeliverable@contoso.example"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Status != "deliverable" || res[1].Status != "risky" || !res[1].CatchAll || res[2].Status != "undeliverable" || res[0].Provider != "simulated" {
		t.Fatalf("verification: %+v", res)
	}

	// The outside-world endpoints only work in a test workspace.
	if out := owner.must(409, "POST", "/v1/workspaces/"+live+"/test/send-now", nil); out["error"].(map[string]any)["code"] != "live_workspace" {
		t.Fatalf("live send-now: %v", out)
	}
	owner.must(404, "POST", base+"/test/inbound", map[string]any{"lead_id": leadList[0].(map[string]any)["id"]})
	owner.must(400, "POST", base+"/test/inbound", map[string]any{})
	owner.must(400, "POST", base+"/test/inbound", map[string]any{"lead_id": leadList[0].(map[string]any)["id"], "kind": "angry"})

	// A sequence runs in a minute: send-now skips the delays and the window.
	owner.must(200, "PUT", base+"/verification-settings", map[string]any{"verify_before_send": false})
	seq := owner.must(201, "POST", base+"/sequences", map[string]any{
		"name": "Try it", "target_daily": 1000, "send_days": []int{1, 2, 3, 4, 5, 6, 7},
		"steps": []map[string]any{{"subject": "Hello {{first_name}}", "body": "Hi {{first_name}}"}, {"delay_days": 3, "body": "Following up"}},
	})
	seqID := seq["id"].(string)
	byEmail := map[string]string{}
	var ids []string
	for _, l := range leadList {
		l := l.(map[string]any)
		if strings.HasPrefix(l["email"].(string), "risky@") || strings.HasPrefix(l["email"].(string), "undeliverable@") {
			continue
		}
		byEmail[l["email"].(string)] = l["id"].(string)
		ids = append(ids, l["id"].(string))
	}
	owner.must(200, "POST", base+"/sequences/"+seqID+"/enrollments", map[string]any{"lead_ids": ids})
	owner.must(200, "PATCH", base+"/sequences/"+seqID, map[string]any{"status": "active"})
	sendNow := func() {
		t.Helper()
		out := owner.must(202, "POST", base+"/test/send-now", map[string]any{"sequence_id": seqID})
		if out["queued"].(float64) == 0 {
			t.Fatalf("send-now queued nothing")
		}
		e.waitSends(t)
	}
	sendNow()
	var sent int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM messages WHERE workspace_id = $1 AND status = 'sent'`, id).Scan(&sent); err != nil {
		t.Fatal(err)
	}
	if sent != 8 {
		t.Fatalf("sent %d", sent)
	}

	// Each kind of answer is handled as real mail would be.
	for email, kind := range map[string]string{"ana@northwind.example": "reply", "ben@northwind.example": "opt_out",
		"chloe@contoso.example": "bounce", "dev@contoso.example": "auto_reply"} {
		in := owner.must(201, "POST", base+"/test/inbound", map[string]any{"lead_id": byEmail[email], "kind": kind})
		if in["kind"] != map[string]string{"opt_out": "reply"}[kind] && in["kind"] != kind {
			t.Errorf("%s: inbound %v", kind, in)
		}
	}
	ens := enrollments(t, owner, base, seqID)
	for email, status := range map[string]string{"ana@northwind.example": "replied", "ben@northwind.example": "unsubscribed",
		"chloe@contoso.example": "bounced", "dev@contoso.example": "active", "emma@fabrikam.example": "active"} {
		if ens[email]["status"] != status {
			t.Errorf("%s: %v, want %s", email, ens[email]["status"], status)
		}
	}
	if inbox := owner.must(200, "GET", base+"/inbox", nil); inbox["unread_replies"] != 1.0 {
		t.Fatalf("inbox: %v", inbox)
	}
	ev := got()
	if sim := named(ev, "inbound.simulated"); len(sim) != 4 || len(named(ev, "sequence.sent_now")) != 1 {
		t.Fatalf("events: %d simulated, %d sent now", len(sim), len(named(ev, "sequence.sent_now")))
	}

	// The follow-up goes now too, only to the leads still active.
	sendNow()
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM messages WHERE workspace_id = $1 AND status = 'sent'`, id).Scan(&sent); err != nil {
		t.Fatal(err)
	}
	if sent != 8+5 {
		t.Fatalf("after follow-ups: %d sent", sent)
	}

	// Nothing can be bought for a test workspace.
	plan := owner.must(201, "POST", base+"/infra-plans", map[string]any{"name": "Q4", "domain_count": 1, "mailboxes_per_domain": 2, "brand": "Sandbox"})
	planID := plan["id"].(string)
	if out := owner.must(409, "POST", base+"/infra-plans/"+planID+"/approve", nil); out["error"].(map[string]any)["code"] != "test_workspace" {
		t.Fatalf("approve: %v", out)
	}
}

// waitSends waits for the send jobs already queued.
func (e *env) waitSends(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var pending int
		if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE kind = 'sequence.send' AND status IN ('queued', 'running')`).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if pending == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d send jobs still pending", pending)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
