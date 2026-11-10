package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestCRMMailPrivacy(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	rec := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana", "identities": []map[string]any{{"namespace": "email", "value": "ana@customer.example"}, {"namespace": "email", "value": "finance@customer.example"}}})
	brand := rec["brand_id"].(string)
	id := rec["id"].(string)
	root := "/v1/brands/" + brand + "/crm/mail"
	messages := root + "/records/" + id + "/messages"
	sharing := root + "/records/" + id + "/sharing"
	inv := owner.must(201, "POST", base+"/invites", map[string]any{"email": "employee@northpeak.example", "role": "operator"})
	employee := &client{t: t, h: e.h}
	employee.must(200, "POST", "/v1/invites/accept", map[string]any{"token": inv["token"], "name": "Employee", "password": "not-real-test-password"})
	owner.must(200, "PUT", root+"/policy", map[string]any{"internal_domains": []string{"northpeak.example"}, "blocked_addresses": []string{}, "blocked_domains": []string{}, "version": 0})
	raw := "From: ana@customer.example\r\nTo: employee@northpeak.example\r\nBcc: finance@customer.example\r\nSubject: Private renewal\r\nMessage-ID: <renewal@customer.example>\r\n\r\nConfidential customer discussion."
	if got := employee.must(200, "POST", messages, map[string]any{"raw": raw}); got["stored"] != true {
		t.Fatal(got)
	}
	if got := employee.must(200, "POST", messages, map[string]any{"raw": raw}); got["stored"] != false {
		t.Fatal("duplicate import", got)
	}
	own := employee.must(200, "GET", messages, nil)["data"].([]any)
	if len(own) != 1 || own[0].(map[string]any)["body"] != "Confidential customer discussion." {
		t.Fatal("own mail not readable", own)
	}
	messageID := own[0].(map[string]any)["id"].(string)
	// Broad customer permissions on an agent token do not open personal mail.
	token := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "CRM helper", "scopes": []string{"customers.read", "customers.manage"}})["token"].(string)
	bot := &client{t: t, h: e.h, bearer: token}
	bot.must(403, "GET", messages, nil)
	bot.must(403, "GET", sharing, nil)

	if got := owner.must(200, "GET", messages, nil); len(got["data"].([]any)) != 0 || got["next_cursor"] != nil {
		t.Fatal("admin saw private row or paging state", got)
	}
	owner.must(404, "DELETE", root+"/messages/"+messageID, nil)
	me := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	employee.must(200, "PUT", sharing, map[string]any{"visibility": "metadata", "audience": "selected", "viewer_ids": []string{me}, "version": 0})
	row := owner.must(200, "GET", messages, nil)["data"].([]any)[0].(map[string]any)
	if row["subject"] != nil || row["body"] != nil || row["participants"] != nil || row["visibility"] != "metadata" {
		t.Fatal("metadata leaked content", row)
	}
	employee.must(409, "PUT", sharing, map[string]any{"visibility": "full", "audience": "brand", "viewer_ids": []string{}, "version": 0})
	employee.must(200, "PUT", sharing, map[string]any{"visibility": "full", "audience": "selected", "viewer_ids": []string{me}, "version": 1})
	row = owner.must(200, "GET", messages, nil)["data"].([]any)[0].(map[string]any)
	if row["subject"] != "Private renewal" {
		t.Fatal("explicit share did not work", row)
	}
	for _, p := range row["participants"].([]any) {
		if p == "finance@customer.example" {
			t.Fatal("full sharing exposed a blind-copy recipient")
		}
	}
	// Disabling an owner also removes previously shared correspondence.
	employeeID := own[0].(map[string]any)["owner_id"].(string)
	if _, err := e.pool.Exec(context.Background(), `UPDATE users SET disabled_at=now() WHERE id=$1`, employeeID); err != nil {
		t.Fatal(err)
	}
	if len(owner.must(200, "GET", messages, nil)["data"].([]any)) != 0 {
		t.Fatal("disabled owner's shared mail remained visible")
	}
	if _, err := e.pool.Exec(context.Background(), `UPDATE users SET disabled_at=NULL WHERE id=$1`, employeeID); err != nil {
		t.Fatal(err)
	}
	employee.must(403, "PUT", root+"/policy", map[string]any{"internal_domains": []string{"northpeak.example"}, "blocked_addresses": []string{}, "blocked_domains": []string{}, "version": 0})

	mixed := "From: ana@customer.example\r\nTo: employee@northpeak.example\r\nCc: hr@northpeak.example\r\nSubject: Internal sensitive detail\r\n\r\nDo not capture."
	employee.must(400, "POST", messages, map[string]any{"raw": mixed})
	employee.must(200, "PUT", root+"/preferences", map[string]any{"blocked_addresses": []string{"ana@customer.example"}, "blocked_domains": []string{}, "version": 0})
	if len(owner.must(200, "GET", messages, nil)["data"].([]any)) != 0 {
		t.Fatal("new exclusion did not revoke historical share")
	}
	employee.must(400, "POST", messages, map[string]any{"raw": raw})
	employee.must(200, "PUT", root+"/preferences", map[string]any{"blocked_addresses": []string{}, "blocked_domains": []string{}, "version": 1})
	employee.must(200, "PUT", sharing, map[string]any{"visibility": "private", "audience": "selected", "viewer_ids": []string{}, "version": 2})
	if len(owner.must(200, "GET", messages, nil)["data"].([]any)) != 0 {
		t.Fatal("revocation failed")
	}
	// Neither the shared inbox nor the record's general activity contains it.
	if got := owner.must(200, "GET", base+"/inbox?kind=all", nil); len(got["data"].([]any)) != 0 {
		t.Fatal("personal mail entered shared inbox")
	}
	timeline := owner.must(200, "GET", base+"/records/"+id+"/timeline", nil)
	timelineJSON, err := json.Marshal(timeline)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(timelineJSON, []byte("Private renewal")) || bytes.Contains(timelineJSON, []byte("Confidential customer discussion.")) {
		t.Fatal("personal mail leaked into the general record timeline")
	}
	var stored []byte
	if err := e.pool.QueryRow(context.Background(), `SELECT content_sealed FROM crm_mail_messages WHERE id=$1`, messageID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) == 0 || bytes.Contains(stored, []byte("Confidential customer discussion.")) || bytes.Contains(stored, []byte("Private renewal")) {
		t.Fatal("missing sealed content")
	}
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Other", "handle": "other"})["id"].(string)
	owner.must(404, "GET", fmt.Sprintf("/v1/brands/%s/crm/mail/records/%s/messages", other, id), nil)

	// Merging into a fully shared destination never declassifies private mail.
	target := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana duplicate"})["id"].(string)
	employee.must(200, "PUT", root+"/records/"+target+"/sharing", map[string]any{"visibility": "full", "audience": "brand", "viewer_ids": []string{}, "version": 0})
	owner.must(200, "POST", base+"/records/"+target+"/merge", map[string]any{"from": id})
	mergedMessages := root + "/records/" + target + "/messages"
	if len(owner.must(200, "GET", mergedMessages, nil)["data"].([]any)) != 0 {
		t.Fatal("merge exposed formerly private mail")
	}
	if len(employee.must(200, "GET", mergedMessages, nil)["data"].([]any)) != 1 {
		t.Fatal("merge lost the owner's mail")
	}
	if count := owner.must(200, "DELETE", root+"/messages", nil)["deleted_count"].(float64); count != 0 {
		t.Fatal("admin cleared employee mail", count)
	}
	employee.must(200, "DELETE", root+"/messages/"+messageID, nil)
	employee.must(200, "POST", mergedMessages, map[string]any{"raw": raw})
	// Hidden copies can still be removed by their owner without unblocking them.
	employee.must(200, "PUT", root+"/preferences", map[string]any{"blocked_addresses": []string{"ana@customer.example"}, "blocked_domains": []string{}, "version": 2})
	if count := employee.must(200, "DELETE", root+"/messages", nil)["deleted_count"].(float64); count != 1 {
		t.Fatal("owner could not clear hidden copies", count)
	}

	seen := map[string]bool{}
	for _, ev := range events() {
		seen[ev.Name] = true
	}
	for _, name := range []string{"crm_mail.imported", "crm_mail.privacy_updated", "crm_mail.deleted", "crm_mail.cleared"} {
		if !seen[name] {
			t.Error("missing event", name)
		}
	}
}

// Team sharing: an owner switches it on, every member is alerted, and it
// starts after the notice period; then a contact's mail whose owner made no
// choice is readable by the brand, a contact made private stays private, and
// each read of a colleague's mail is recorded. Switching it off is at once.
func TestCRMTeamMailSharing(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	rec := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana", "identities": []map[string]any{{"namespace": "email", "value": "ana@customer.example"}}})
	brand := rec["brand_id"].(string)
	id := rec["id"].(string)
	root := "/v1/brands/" + brand + "/crm/mail"
	messages := root + "/records/" + id + "/messages"
	inv := owner.must(201, "POST", base+"/invites", map[string]any{"email": "employee@northpeak.example", "role": "operator"})
	employee := &client{t: t, h: e.h}
	employee.must(200, "POST", "/v1/invites/accept", map[string]any{"token": inv["token"], "name": "Employee", "password": "not-real-test-password"})
	policy := owner.must(200, "PUT", root+"/policy", map[string]any{"internal_domains": []string{"northpeak.example"}, "blocked_addresses": []string{}, "blocked_domains": []string{}, "version": 0})
	// Labels never set keep the defaults (Personal, HR, Legal, Finance, Medical).
	if policy["private_labels"] != nil {
		t.Fatalf("default labels: %v", policy)
	}
	raw := "From: ana@customer.example\r\nTo: employee@northpeak.example\r\nSubject: Reseller pricing\r\nMessage-ID: <pricing@customer.example>\r\n\r\n10 TB a month."
	employee.must(200, "POST", messages, map[string]any{"raw": raw})
	if len(owner.must(200, "GET", messages, nil)["data"].([]any)) != 0 {
		t.Fatal("shared before team sharing")
	}

	// Only the brand policy switches it on, and the server says when it starts.
	if p := employee.must(200, "PUT", root+"/preferences", map[string]any{"blocked_addresses": []string{}, "blocked_domains": []string{}, "team_sharing": true, "version": 0}); p["team_sharing"] != false {
		t.Fatalf("a person switched team sharing on: %v", p)
	}
	on := owner.must(200, "PUT", root+"/policy", map[string]any{"internal_domains": []string{"northpeak.example"}, "blocked_addresses": []string{}, "blocked_domains": []string{},
		"team_sharing": true, "team_sharing_from": "2000-01-01T00:00:00Z", "version": 1})
	from, err := time.Parse(time.RFC3339, on["team_sharing_from"].(string))
	if err != nil || from.Before(time.Now().Add(47*time.Hour)) {
		t.Fatalf("starts: %v %v", on, err)
	}
	var alerted int
	if err := e.pool.QueryRow(ctx, `SELECT count(DISTINCT owner_id) FROM alerts WHERE workspace_id=$1 AND kind='crm_mail.team_sharing'`, ws).Scan(&alerted); err != nil || alerted != 2 {
		t.Fatalf("alerted %d %v", alerted, err)
	}
	if len(owner.must(200, "GET", messages, nil)["data"].([]any)) != 0 {
		t.Fatal("shared during the notice period")
	}
	// Every member sees it coming in their own settings.
	if p := employee.must(200, "GET", root+"/preferences", nil); p["team_sharing"] != true || p["team_sharing_from"] != on["team_sharing_from"] {
		t.Fatalf("member's view: %v", p)
	}
	// Saving the policy again keeps the start; the notice period passes.
	again := owner.must(200, "PUT", root+"/policy", map[string]any{"internal_domains": []string{"northpeak.example"}, "blocked_addresses": []string{}, "blocked_domains": []string{}, "team_sharing": true, "version": 2})
	if again["team_sharing_from"] != on["team_sharing_from"] {
		t.Fatalf("start moved: %v", again)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE crm_mail_policy SET config = jsonb_set(config, '{team_sharing_from}', to_jsonb(now() - interval '1 minute')) WHERE brand_id=$1`, brand); err != nil {
		t.Fatal(err)
	}
	// A private-looking email from the same contact.
	offer := "From: ana@customer.example\r\nTo: employee@northpeak.example\r\nSubject: Offer letter\r\nMessage-ID: <offer@customer.example>\r\n\r\nWe'd like to offer you the role."
	employee.must(200, "POST", messages, map[string]any{"raw": offer})
	// Nothing is shown before the AI check has read it.
	if got := owner.must(200, "GET", messages, nil)["data"].([]any); len(got) != 0 {
		t.Fatalf("unchecked mail shown: %v", got)
	}
	var queued int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind='crm_mail.privacy_check' AND payload->>'brand_id'=$1`, brand).Scan(&queued); err != nil || queued == 0 {
		t.Fatalf("check not queued: %d %v", queued, err)
	}
	// Without an AI connection nothing is checked, so nothing is shown.
	if err := e.srv.RunMailPrivacyCheck(ctx, ws, brand); err != nil {
		t.Fatal(err)
	}
	if got := owner.must(200, "GET", messages, nil)["data"].([]any); len(got) != 0 {
		t.Fatalf("shown without a check: %v", got)
	}
	connectFakePrivacyModel(t, e, owner, ws)
	if err := e.srv.RunMailPrivacyCheck(ctx, ws, brand); err != nil {
		t.Fatal(err)
	}
	got := owner.must(200, "GET", messages, nil)["data"].([]any)
	if len(got) != 1 || got[0].(map[string]any)["subject"] != "Reseller pricing" || got[0].(map[string]any)["visibility"] != "full" {
		t.Fatalf("team read: %v", got)
	}
	// Its owner still sees both.
	if mine := employee.must(200, "GET", messages, nil)["data"].([]any); len(mine) != 2 {
		t.Fatalf("owner's view: %v", mine)
	}
	var spent int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM ai_calls WHERE workspace_id=$1 AND task='mail_privacy'`, ws).Scan(&spent); err != nil || spent != 2 {
		t.Fatalf("ai calls %d %v", spent, err)
	}
	var viewed int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id=$1 AND action='crm_mail.viewed' AND target_id=$2`, ws, id).Scan(&viewed); err != nil || viewed != 1 {
		t.Fatalf("viewed %d %v", viewed, err)
	}
	// Reading your own mail isn't recorded as a colleague's.
	employee.must(200, "GET", messages, nil)
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id=$1 AND action='crm_mail.viewed'`, ws).Scan(&viewed); err != nil || viewed != 1 {
		t.Fatalf("own read recorded: %d %v", viewed, err)
	}

	// The contact's sharing says what really happens: shared with the brand.
	if sh := employee.must(200, "GET", root+"/records/"+id+"/sharing", nil); sh["visibility"] != "full" || sh["audience"] != "brand" || sh["version"] != 0.0 {
		t.Fatalf("effective sharing: %v", sh)
	}
	// The owner makes the contact private: it stays theirs.
	employee.must(200, "PUT", root+"/records/"+id+"/sharing", map[string]any{"visibility": "private", "audience": "selected", "viewer_ids": []string{}, "version": 0})
	if len(owner.must(200, "GET", messages, nil)["data"].([]any)) != 0 {
		t.Fatal("private contact shared")
	}
	employee.must(200, "PUT", root+"/records/"+id+"/sharing", map[string]any{"visibility": "full", "audience": "brand", "viewer_ids": []string{}, "version": 1})

	// Switching it off is at once, and tells everyone.
	off := owner.must(200, "PUT", root+"/policy", map[string]any{"internal_domains": []string{"northpeak.example"}, "blocked_addresses": []string{}, "blocked_domains": []string{}, "team_sharing": false, "version": 3})
	if off["team_sharing_from"] != nil {
		t.Fatalf("off: %v", off)
	}
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM alerts WHERE workspace_id=$1 AND kind='crm_mail.team_sharing'`, ws).Scan(&alerted); err != nil || alerted != 4 {
		t.Fatalf("alerts after off %d %v", alerted, err)
	}
	owner.must(400, "PUT", root+"/policy", map[string]any{"internal_domains": []string{}, "blocked_addresses": []string{}, "blocked_domains": []string{}, "private_labels": []string{`"quoted"`}, "version": 4})
}

// connectFakePrivacyModel connects a fake OpenAI that calls an email about
// a job offer private and anything else work.
func connectFakePrivacyModel(t *testing.T, e *env, owner *client, ws string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			io.WriteString(w, `{"data":[{"id":"gpt-test"}]}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		verdict := "work"
		if strings.Contains(string(body), "Offer letter") {
			verdict = "private"
		}
		json.NewEncoder(w).Encode(map[string]any{"model": "gpt-test", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant",
			"content": `{"verdict":"` + verdict + `"}`}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 300, "completion_tokens": 5}})
	}))
	t.Cleanup(srv.Close)
	e.integrations.Client.BaseURLs["openai"] = srv.URL
	conn := owner.must(201, "POST", "/v1/workspaces/"+ws+"/integrations", map[string]any{"connector": "openai", "credentials": map[string]any{"api_key": openAIKey}})
	owner.must(200, "PUT", "/v1/workspaces/"+ws+"/ai-settings", map[string]any{"integration_id": conn["id"], "model": "gpt-test",
		"input_cents_per_million": 10, "output_cents_per_million": 50, "monthly_limit_cents": 5000})
}
