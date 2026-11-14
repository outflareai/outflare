package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Sending as a member (ADR 15): the policy check, a job that sends from
// the person's own mailbox through the Workspace connection, never twice,
// and the sent email kept on the record.
func TestCRMMailSendAsMember(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana", "identities": []map[string]any{{"namespace": "email", "value": "ana@customer.example"}}})
	brand, anaID := ana["brand_id"].(string), ana["id"].(string)
	root := "/v1/brands/" + brand + "/crm/mail"
	ownerEmail := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["email"].(string)
	inv := owner.must(201, "POST", base+"/invites", map[string]any{"email": "employee@northpeak.example", "role": "operator"})
	employee := &client{t: t, h: e.h}
	employee.must(200, "POST", "/v1/invites/accept", map[string]any{"token": inv["token"], "name": "Employee", "password": "not-real-test-password"})
	employeeID := employee.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)

	var mu sync.Mutex
	allowSend := false
	var sent []string
	inSent := map[string]bool{}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/sa":
			_ = r.ParseForm()
			parts := strings.Split(r.PostForm.Get("assertion"), ".")
			raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
			var claims struct{ Sub, Scope string }
			_ = json.Unmarshal(raw, &claims)
			if strings.HasSuffix(claims.Scope, "gmail.send") && !allowSend {
				w.WriteHeader(401)
				reply(map[string]any{"error": "unauthorized_client"})
				return
			}
			reply(map[string]any{"access_token": "test-token:" + claims.Scope, "expires_in": 3600})
		case r.URL.Path == "/gmail/users/me/profile":
			reply(map[string]any{"emailAddress": ownerEmail})
		case r.URL.Path == "/gmail/users/me/messages/send":
			if !strings.HasSuffix(r.Header.Get("Authorization"), "gmail.send") {
				t.Error("sent without the send scope")
			}
			body, _ := io.ReadAll(r.Body)
			var in struct{ Raw string }
			_ = json.Unmarshal(body, &in)
			raw, _ := base64.RawURLEncoding.DecodeString(in.Raw)
			sent = append(sent, string(raw))
			for _, line := range strings.Split(string(raw), "\r\n") {
				if strings.HasPrefix(line, "Message-ID: ") {
					inSent[strings.Trim(strings.TrimPrefix(line, "Message-ID: "), "<>")] = true
				}
			}
			reply(map[string]any{"id": "gmail-sent-1", "threadId": "t1"})
		case r.URL.Path == "/gmail/users/me/messages":
			q := strings.TrimPrefix(r.URL.Query().Get("q"), "rfc822msgid:")
			if inSent[q] {
				reply(map[string]any{"messages": []map[string]string{{"id": "gmail-sent-1"}}})
				return
			}
			reply(map[string]any{"messages": []any{}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer fake.Close()
	e.integrations.Client.BaseURLs["google.token"] = fake.URL + "/sa"
	e.integrations.Client.BaseURLs["gmail"] = fake.URL + "/gmail"
	e.oauth.Endpoints.GoogleMail = fake.URL + "/gmail"

	// The connection says what Google lets it do: not sending yet.
	conn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "google_workspace", "credentials": map[string]string{"service_account_json": newServiceAccount(t), "admin_email": ownerEmail}})
	if scopes := conn["account"].(map[string]any)["details"].(map[string]any)["scopes"].(string); strings.Contains(scopes, "gmail.send") || !strings.Contains(scopes, "gmail.readonly") {
		t.Fatalf("scopes %q", scopes)
	}
	owner.must(200, "PUT", root+"/policy", map[string]any{"internal_domains": []string{"northpeak.example"}, "blocked_addresses": []string{}, "blocked_domains": []string{}, "version": 0})
	send := map[string]any{"record_id": anaID, "to": []string{"Ana <ana@customer.example>"}, "subject": "Fictional proposal", "body": "Here it is.\nThanks"}
	// Not enabled for CRM email yet.
	employee.must(403, "POST", root+"/sends", send)
	owner.must(200, "PUT", root+"/rollout", map[string]any{"enabled": true, "allowed_user_ids": []string{employeeID}, "excluded_user_ids": []string{}, "workspace_integration_id": conn["id"], "version": 0})

	// The policy check refuses a suppressed recipient before anything is queued.
	owner.must(200, "POST", base+"/suppressions", map[string]any{"values": []string{"blocked@customer.example"}, "reason": "do_not_contact"})
	blocked := employee.must(409, "POST", root+"/sends", map[string]any{"record_id": anaID, "to": []string{"blocked@customer.example"}, "subject": "Hi", "body": "Hi"})
	if blocked["error"].(map[string]any)["code"] != "blocked" {
		t.Fatalf("blocked: %v", blocked)
	}

	// Without gmail.send delegated, the send fails and says why.
	queued := employee.must(202, "POST", root+"/sends", send)
	if queued["status"] != "queued" {
		t.Fatalf("queued: %v", queued)
	}
	if err := e.srv.RunCRMMailSend(ctx, ws, queued["id"].(string)); err != nil {
		t.Fatal(err)
	}
	if got := employee.must(200, "GET", root+"/sends/"+queued["id"].(string), nil); got["status"] != "failed" || got["error_code"] != "send_not_allowed" {
		t.Fatalf("no send scope: %v", got)
	}

	// With it, the email goes out once and is kept on Ana.
	mu.Lock()
	allowSend = true
	mu.Unlock()
	ok := employee.must(202, "POST", root+"/sends", send)
	id := ok["id"].(string)
	if err := e.srv.RunCRMMailSend(ctx, ws, id); err != nil {
		t.Fatal(err)
	}
	got := employee.must(200, "GET", root+"/sends/"+id, nil)
	if got["status"] != "sent" || got["crm_mail_message_id"] == nil {
		t.Fatalf("sent: %v", got)
	}
	mu.Lock()
	if len(sent) != 1 || !strings.Contains(sent[0], "To: ana@customer.example") || !strings.Contains(sent[0], "From: employee@northpeak.example") {
		t.Errorf("sent %q", sent)
	}
	mu.Unlock()
	mail := employee.must(200, "GET", root+"/records/"+anaID+"/messages", nil)["data"].([]any)
	if len(mail) != 1 || mail[0].(map[string]any)["subject"] != "Fictional proposal" || mail[0].(map[string]any)["body"] != "Here it is.\nThanks" {
		t.Fatalf("kept on the record: %v", mail)
	}

	// A retry after an unknown outcome finds it in Sent and doesn't send again.
	if _, err := e.pool.Exec(ctx, `UPDATE crm_mail_sends SET status='sending' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.RunCRMMailSend(ctx, ws, id); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(sent) != 1 {
		t.Errorf("sent twice: %d", len(sent))
	}
	mu.Unlock()
	if n := len(employee.must(200, "GET", root+"/records/"+anaID+"/messages", nil)["data"].([]any)); n != 1 {
		t.Fatalf("kept twice: %d", n)
	}
	// Someone else's send isn't theirs to read.
	owner.must(404, "GET", root+"/sends/"+id, nil)
	var audited int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id=$1 AND action IN ('crm_mail.send_queued','crm_mail.sent','crm_mail.send_failed')`, ws).Scan(&audited); err != nil || audited < 4 {
		t.Fatalf("audit %d %v", audited, err)
	}
}
