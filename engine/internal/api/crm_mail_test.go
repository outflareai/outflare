package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

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
