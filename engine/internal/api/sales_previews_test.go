package api_test

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSalesQueuePreviewsAndFilters(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	company := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Northpeak Example", "attributes": map[string]any{"website": "https://northpeak.example"}})
	person := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana Example", "attributes": map[string]any{"companies": []string{company["id"].(string)}, "job_title": "Product lead", "linkedin_url": "https://social.example/ana"}, "identities": []map[string]any{{"namespace": "email", "value": "ana@customer.example"}}})
	brand := person["brand_id"].(string)
	root := "/v1/brands/" + brand + "/sales"
	user := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	integration := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "emailbison", "credentials": map[string]string{"base_url": "https://send.acme.test", "api_key": "eb-replies"}})["id"].(string)
	at := time.Now().UTC().Add(-time.Hour)
	message := func(id, direction, body string, offset time.Duration) map[string]any {
		from, to := "ana@customer.example", "ana@send-acme.test"
		if direction == "outgoing" {
			from, to = to, from
		}
		return map[string]any{"direction": direction, "from": from, "to": []string{to}, "cc": []string{}, "subject": "Team plan", "body": body, "message_id": "<" + id + "@customer.example>", "provider_id": id, "occurred_at": at.Add(offset).Format(time.RFC3339)}
	}
	lead := owner.must(201, "POST", root+"/bridge", map[string]any{"source": "test_importer", "source_id": "preview", "record_id": person["id"], "integration_id": integration, "sender_id": "1", "reply_id": "501", "messages": []map[string]any{message("501", "incoming", "Original interest", 0), message("502", "incoming", strings.Repeat("é", 1205), time.Minute), message("503", "outgoing", "Here are the plan details.", 2*time.Minute)}})
	id := lead["id"].(string)
	read := func(path string) []any { return owner.must(200, "GET", root+"/leads"+path, nil)["data"].([]any) }
	rows := read("?queue=all")
	if len(rows) != 1 {
		t.Fatal("missing imported conversation", rows)
	}
	got := rows[0].(map[string]any)
	if got["company"] != "Northpeak Example" || got["job_title"] != "Product lead" || got["website"] != "https://northpeak.example" || got["linkedin_url"] != "https://social.example/ana" {
		t.Fatal("canonical source fields missing", got)
	}
	if got["latest_message"].(map[string]any)["body"] != "Here are the plan details." || got["latest_reply"].(map[string]any)["body"] != strings.Repeat("é", 1200) {
		t.Fatal("latest message/reply or Unicode excerpt is wrong", got)
	}
	if got["last_activity"] == nil {
		t.Fatal("manager activity missing")
	}
	if _, err := e.pool.Exec(context.Background(), `UPDATE sales_leads SET state='active',owner_id=$4,ae_id=$4 WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, ws, brand, id, user); err != nil {
		t.Fatal(err)
	}
	if len(read("?queue=all&person="+user)) != 1 || len(read("?queue=all&state=new&person="+user)) != 0 {
		t.Fatal("teammate and state filters do not combine")
	}
	owner.must(400, "GET", root+"/leads?queue=all&person=not-a-uuid", nil)

	inv := owner.must(201, "POST", base+"/invites", map[string]any{"email": "other@acme.example", "role": "operator"})
	other := &client{t: t, h: e.h}
	other.must(200, "POST", "/v1/invites/accept", map[string]any{"token": inv["token"], "name": "Other Rep", "password": "not-real-test-password"})
	otherID := other.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	owner.must(200, "PUT", root+"/team/"+otherID, map[string]any{"role": "ae"})
	other.must(403, "GET", root+"/leads?queue=all", nil)
	for _, queue := range []string{"new", "mine", "handoffs"} {
		if len(other.must(200, "GET", root+"/leads?queue="+queue+"&person="+user, nil)["data"].([]any)) != 0 {
			t.Fatal("teammate filter widened queue access", queue)
		}
	}
	otherBrand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Other Brand", "handle": "preview-other"})["id"].(string)
	if len(owner.must(200, "GET", "/v1/brands/"+otherBrand+"/sales/leads?queue=all", nil)["data"].([]any)) != 0 {
		t.Fatal("preview crossed brand")
	}
	// Re-opening the shared queue exposes its conversation excerpt, but never
	// the manager activity feed to a representative.
	if _, err := e.pool.Exec(context.Background(), `UPDATE sales_leads SET state='new',owner_id=NULL,ae_id=NULL WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, ws, brand, id); err != nil {
		t.Fatal(err)
	}
	shared := other.must(200, "GET", root+"/leads?queue=new", nil)["data"].([]any)[0].(map[string]any)
	if shared["latest_reply"] == nil || shared["last_activity"] != nil {
		t.Fatal("representative preview or activity boundary is wrong", shared)
	}
	// Reconciliation can supersede a provider copy; a queue must not show it.
	detail := owner.must(200, "GET", root+"/leads/"+id, nil)
	messages := detail["messages"].([]any)
	if _, err := e.pool.Exec(context.Background(), `UPDATE sales_messages SET superseded_by=$4 WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, ws, brand, messages[1].(map[string]any)["id"], messages[0].(map[string]any)["id"]); err != nil {
		t.Fatal(err)
	}
	got = read("?queue=all")[0].(map[string]any)
	if got["latest_reply"].(map[string]any)["body"] != "Original interest" {
		t.Fatal("superseded message is still in the queue preview")
	}
}
