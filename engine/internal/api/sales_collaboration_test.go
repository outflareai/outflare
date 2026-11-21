package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestSalesAssignmentViewsAndFeedback(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	p := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Example Buyer", "attributes": map[string]any{"country": "GB", "city": "London"}, "identities": []map[string]string{{"namespace": "email", "value": "buyer@customer.example"}}})
	brand := p["brand_id"].(string)
	root := "/v1/brands/" + brand + "/sales"
	rep, rid := joinAs(t, owner, ws, "rep@northpeak.example", map[string]any{"role": "operator"})
	other, oid := joinAs(t, owner, ws, "other@northpeak.example", map[string]any{"role": "operator"})
	manager, mid := joinAs(t, owner, ws, "manager@northpeak.example", map[string]any{"role": "operator"})
	for id, role := range map[string]string{rid: "sdr", oid: "ae", mid: "manager"} {
		owner.must(200, "PUT", root+"/team/"+id, map[string]any{"role": role})
	}
	integration := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "emailbison", "credentials": map[string]string{"base_url": "https://send.acme.test", "api_key": "eb-replies"}})["id"].(string)
	lead := owner.must(201, "POST", root+"/bridge", map[string]any{"source": "test", "source_id": "collaboration", "record_id": p["id"], "integration_id": integration, "sender_id": "1", "reply_id": "501", "messages": []map[string]any{{"direction": "incoming", "from": "buyer@customer.example", "to": []string{"rep@northpeak.example"}, "cc": []string{}, "subject": "Example", "body": "Please send an overview.", "message_id": "<collaboration@customer.example>", "provider_id": "501", "occurred_at": time.Now().UTC().Format(time.RFC3339)}}})
	path := root + "/leads/" + lead["id"].(string)
	// Listing a queue is not evidence that someone opened this conversation.
	rows := rep.must(200, "GET", root+"/leads?queue=new&assignment=unassigned", nil)["data"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["viewed_by"] != nil {
		t.Fatal("queue marked the conversation viewed")
	}
	rep.must(200, "POST", path+"/actions", map[string]any{"action": "read", "version": lead["version"]})
	detail := other.must(200, "GET", path, nil)
	views := detail["lead"].(map[string]any)["viewed_by"].([]any)
	if len(views) != 1 || views[0].(map[string]any)["user_id"] != rid {
		t.Fatal("missing shared viewing status", views)
	}
	if detail["lead"].(map[string]any)["country"] != "GB" || detail["lead"].(map[string]any)["city"] != "London" {
		t.Fatal("contact location missing", detail["lead"])
	}
	oldVersion := lead["version"]
	lead = rep.must(200, "POST", path+"/actions", map[string]any{"action": "assign", "version": oldVersion, "assignee_id": oid})
	if lead["state"] != "new" || lead["owner_id"] != oid || lead["ae_id"] != oid {
		t.Fatal("assignment should reserve without sending", lead)
	}
	other.must(409, "POST", path+"/actions", map[string]any{"action": "assign", "version": oldVersion, "assignee_id": rid})
	lead = rep.must(200, "POST", path+"/actions", map[string]any{"action": "assign", "version": lead["version"], "assignee_id": rid})
	lead = other.must(200, "POST", path+"/actions", map[string]any{"action": "assign", "version": lead["version"], "assignee_id": oid})
	rep.must(403, "POST", path+"/actions", map[string]any{"action": "take", "version": lead["version"]})
	rep.must(403, "POST", path+"/actions", map[string]any{"action": "archive", "version": lead["version"]})
	mine := other.must(200, "GET", root+"/leads?queue=mine&state=open", nil)
	if len(mine["data"].([]any)) != 1 || mine["unread"] != true {
		t.Fatal("assigned new lead missing from open conversations or unread indicator", mine)
	}
	if len(rep.must(200, "GET", root+"/leads?queue=mine&state=open", nil)["data"].([]any)) != 0 {
		t.Fatal("reassignment retained lead in previous owner's queue")
	}
	lead = other.must(200, "POST", path+"/actions", map[string]any{"action": "archive", "version": lead["version"]})
	lead = other.must(200, "POST", path+"/actions", map[string]any{"action": "reopen", "version": lead["version"]})
	if lead["state"] != "new" {
		t.Fatal("reopening a reserved lead incorrectly started it")
	}
	if len(other.must(200, "GET", root+"/leads?queue=new&assignment=mine", nil)["data"].([]any)) != 1 {
		t.Fatal("assignee cannot find reserved lead")
	}
	rep.must(200, "POST", path+"/actions", map[string]any{"action": "feedback", "version": lead["version"], "feedback_category": "question", "body": "Can someone verify the requested use case?"})
	detail = other.must(200, "GET", path, nil)
	feedback := detail["feedback"].([]any)
	if len(feedback) != 1 || feedback[0].(map[string]any)["body"] != "Can someone verify the requested use case?" || feedback[0].(map[string]any)["author_id"] != rid {
		t.Fatal("feedback lost author or comment", feedback)
	}
	if detail["lead"].(map[string]any)["owner_id"] != oid || detail["lead"].(map[string]any)["feedback_count"] != float64(1) {
		t.Fatal("feedback changed assignment")
	}
	lead = manager.must(200, "POST", path+"/actions", map[string]any{"action": "assign", "version": lead["version"], "assignee_id": rid})
	lead = other.must(200, "POST", path+"/actions", map[string]any{"action": "release", "version": lead["version"]})
	if lead["owner_id"] != nil || lead["sdr_id"] != nil || lead["ae_id"] != nil {
		t.Fatal("release retained assignment")
	}
	rep.must(400, "POST", path+"/actions", map[string]any{"action": "assign", "version": lead["version"], "assignee_id": mid})
	rep.must(400, "POST", path+"/actions", map[string]any{"action": "feedback", "version": lead["version"], "feedback_category": "question", "body": " "})
	var sends int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE workspace_id=$1 AND kind='sales.send'`, ws).Scan(&sends); err != nil || sends != 0 {
		t.Fatal("collaboration sent email", sends, err)
	}
	if len(named(events(), "sales.feedback_added")) != 1 || len(named(events(), "sales.lead_assigned")) != 4 || len(named(events(), "sales.lead_seen")) != 1 {
		t.Fatal("missing collaboration audit events")
	}
	if _, err := e.pool.Exec(context.Background(), `UPDATE sales_leads SET state='active',owner_id=$3,sdr_id=$3 WHERE workspace_id=$1 AND brand_id=$2 AND id=$4`, ws, brand, rid, lead["id"]); err != nil {
		t.Fatal(err)
	}
	// A taken conversation cannot be released, but its owner can pass it on.
	rep.must(403, "POST", path+"/actions", map[string]any{"action": "release", "version": lead["version"]})
	lead = rep.must(200, "POST", path+"/actions", map[string]any{"action": "assign", "version": lead["version"], "assignee_id": oid})
	if lead["state"] != "active" || lead["owner_id"] != oid {
		t.Fatal("owner could not reassign an active conversation", lead)
	}
	otherBrand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Other", "handle": "other-collaboration"})["id"].(string)
	rep.must(404, "GET", "/v1/brands/"+otherBrand+"/sales/leads/"+lead["id"].(string), nil)
}

func TestSalesPersonalTemplateFavorites(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	person := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Example Buyer"})
	brand := person["brand_id"].(string)
	root := "/v1/brands/" + brand + "/sales"
	rep, rid := joinAs(t, owner, ws, "rep@northpeak.example", map[string]any{"role": "operator"})
	other, oid := joinAs(t, owner, ws, "other@northpeak.example", map[string]any{"role": "operator"})
	for _, id := range []string{rid, oid} {
		owner.must(200, "PUT", root+"/team/"+id, map[string]any{"role": "sdr"})
	}
	template := rep.must(201, "POST", root+"/templates", map[string]any{"name": "Overview", "body": "Hello {first_name}"})
	path := root + "/templates/" + template["id"].(string)
	other.must(404, "PUT", path+"/favorite", map[string]any{"favorite": true})
	rep.must(400, "PUT", path+"/favorite", map[string]any{})
	favorite := rep.must(200, "PUT", path+"/favorite", map[string]any{"favorite": true})
	if favorite["favorite"] != true || favorite["version_id"] != template["version_id"] || favorite["version"] != template["version"] {
		t.Fatal("favorite changed content version", favorite)
	}
	listed := rep.must(200, "GET", root+"/templates", nil)["data"].([]any)
	if len(listed) != 1 || listed[0].(map[string]any)["favorite"] != true {
		t.Fatal("favorite did not persist")
	}
	if len(other.must(200, "GET", root+"/templates", nil)["data"].([]any)) != 0 {
		t.Fatal("templates leaked between teammates")
	}
	updated := rep.must(200, "PUT", path, map[string]any{"version": template["version"], "name": "Overview", "body": "Updated text"})
	if updated["favorite"] != true {
		t.Fatal("editing cleared favorite")
	}
	otherBrand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Other", "handle": "favorite-isolation"})["id"].(string)
	rep.must(404, "PUT", "/v1/brands/"+otherBrand+"/sales/templates/"+template["id"].(string)+"/favorite", map[string]any{"favorite": true})
	unfavorite := rep.must(200, "PUT", path+"/favorite", map[string]any{"favorite": false})
	if unfavorite["favorite"] != false {
		t.Fatal("unfavorite failed")
	}
	rep.must(200, "PUT", path, map[string]any{"version": updated["version"], "name": "Overview", "body": "Updated text", "archived": true})
	rep.must(404, "PUT", path+"/favorite", map[string]any{"favorite": true})
	if len(named(events(), "sales.template_favorite_updated")) != 2 {
		t.Fatal("missing favorite audit events")
	}
}
