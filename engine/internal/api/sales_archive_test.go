package api_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestSalesQueueArchiveAuditAndUndo(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	person := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana Example", "identities": []map[string]any{{"namespace": "email", "value": "ana@customer.example"}}})
	brand := person["brand_id"].(string)
	root := "/v1/brands/" + brand + "/sales"
	invite := func(email string) (*client, string) {
		t.Helper()
		inv := owner.must(201, "POST", base+"/invites", map[string]any{"email": email, "role": "operator"})
		rep := &client{t: t, h: e.h}
		rep.must(200, "POST", "/v1/invites/accept", map[string]any{"token": inv["token"], "name": "Example Rep", "password": "test-not-real-password"})
		id := rep.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
		owner.must(200, "PUT", root+"/team/"+id, map[string]any{"role": "sdr"})
		return rep, id
	}
	rep, user := invite("rep@northpeak.example")
	other, otherUser := invite("other@northpeak.example")
	integration := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "emailbison", "credentials": map[string]string{"base_url": "https://send.acme.test", "api_key": "eb-replies"}})["id"].(string)
	lead := owner.must(201, "POST", root+"/bridge", map[string]any{"source": "test", "source_id": "archive", "record_id": person["id"], "integration_id": integration, "sender_id": "1", "reply_id": "501", "messages": []map[string]any{{"direction": "incoming", "from": "ana@customer.example", "to": []string{"sender@northpeak.example"}, "cc": []string{}, "subject": "Example conversation", "body": "Private conversation fixture", "message_id": "<archive@customer.example>", "provider_id": "501", "occurred_at": time.Now().UTC().Format(time.RFC3339)}}})
	id := lead["id"].(string)
	path := root + "/leads/" + id
	lead = rep.must(200, "POST", path+"/actions", map[string]any{"action": "details", "version": lead["version"], "note": "Private shared note fixture"})
	archive := func(c *client) {
		t.Helper()
		lead = c.must(200, "POST", path+"/actions", map[string]any{"action": "archive", "version": lead["version"]})
		if lead["state"] != "archived" || lead["owner_id"] != nil || lead["note"] != "Private shared note fixture" {
			t.Fatal("archive took ownership or discarded shared details", lead)
		}
		if rows := c.must(200, "GET", root+"/leads?queue=new", nil)["data"].([]any); len(rows) != 0 {
			t.Fatal("archived lead remains in New", rows)
		}
	}
	archive(rep)
	// A rep can undo their own archive without a mailbox grant, but that
	// does not let other reps reopen or read an archived conversation.
	rep.must(404, "GET", path, nil)
	other.must(404, "POST", path+"/actions", map[string]any{"action": "reopen", "version": lead["version"]})
	otherBrand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Other", "handle": "archive-other"})["id"].(string)
	owner.must(404, "POST", "/v1/brands/"+otherBrand+"/sales/leads/"+id+"/actions", map[string]any{"action": "reopen", "version": lead["version"]})
	archivedVersion := lead["version"]
	lead = rep.must(200, "POST", path+"/actions", map[string]any{"action": "reopen", "version": archivedVersion})
	if lead["state"] != "new" || lead["owner_id"] != nil {
		t.Fatal("undo did not restore the unassigned queue")
	}
	rep.must(409, "POST", path+"/actions", map[string]any{"action": "reopen", "version": archivedVersion})
	archive(other)
	rep.must(404, "POST", path+"/actions", map[string]any{"action": "reopen", "version": lead["version"]})
	owner.must(200, "PUT", root+"/team/"+otherUser, map[string]any{"role": "none"})
	other.must(404, "POST", path+"/actions", map[string]any{"action": "reopen", "version": lead["version"]})
	owner.must(200, "PUT", root+"/team/"+otherUser, map[string]any{"role": "ae"})
	lead = other.must(200, "POST", path+"/actions", map[string]any{"action": "reopen", "version": lead["version"]})
	detail := rep.must(200, "GET", path, nil)
	if len(detail["messages"].([]any)) != 1 || len(detail["events"].([]any)) != 0 {
		t.Fatal("archive changed the conversation or exposed manager logs")
	}
	var sends int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE workspace_id=$1 AND kind='sales.send'`, ws).Scan(&sends); err != nil || sends != 0 {
		t.Fatalf("queue cleanup must not send email: %d, %v", sends, err)
	}

	// Verify all three durable records point to the same action, actor,
	// request and timestamp. Undo adds evidence; it never erases the archive.
	rows, err := e.pool.Query(ctx, `SELECT id::text,actor_id::text,occurred_at,action,content_sealed FROM sales_events
	 WHERE workspace_id=$1 AND brand_id=$2 AND lead_id=$3 AND action IN ('lead_archived','lead_reopened') ORDER BY occurred_at,id`, ws, brand, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var eventID, actor, action string
		var at time.Time
		var sealed []byte
		if err := rows.Scan(&eventID, &actor, &at, &action, &sealed); err != nil {
			t.Fatal(err)
		}
		if at.IsZero() || (actor != user && actor != otherUser) {
			t.Fatal("missing action attribution")
		}
		raw, err := e.box.Open(sealed, []byte(eventID))
		if err != nil {
			t.Fatal(err)
		}
		var evidence struct {
			Before struct {
				State string `json:"state"`
			} `json:"before"`
			After struct {
				Lead struct {
					State string `json:"state"`
				} `json:"lead"`
			} `json:"after"`
			RequestID string `json:"request_id"`
		}
		if err := json.Unmarshal(raw, &evidence); err != nil {
			t.Fatal(err)
		}
		before, after := "new", "archived"
		if action == "lead_reopened" {
			before, after = "archived", "new"
		}
		if evidence.Before.State != before || evidence.After.Lead.State != after || evidence.RequestID == "" {
			t.Fatal("missing state transition or request", evidence)
		}
		var linked int
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events a JOIN users u ON u.actor_id=a.actor_id
		 JOIN attribute_changes h ON h.workspace_id=a.workspace_id AND h.brand_id=a.brand_id AND h.record_id=$3 AND h.field_key='sales_workflow'
		 AND h.new_value->>'event_id'=$4 AND h.actor_id=a.actor_id AND h.request_id=a.request_id
		 WHERE a.workspace_id=$1 AND a.brand_id=$2 AND a.target_id=$5 AND a.action=$6 AND a.data->>'event_id'=$4 AND u.id=$7 AND a.request_id=$8`, ws, brand, person["id"], eventID, id, "sales."+action, actor, evidence.RequestID).Scan(&linked); err != nil || linked != 1 {
			t.Fatalf("missing linked audit/history: %d, %v", linked, err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("expected two archives and two reopens, got %d", count)
	}
	for _, name := range []string{"sales.lead_archived", "sales.lead_reopened"} {
		got := named(events(), name)
		if len(got) != 2 {
			t.Fatalf("%s events: %d", name, len(got))
		}
		for _, event := range got {
			if event.UserID == "" || event.WorkspaceID != ws || event.Props["brand_id"] != brand || event.Outcome != track.Success || event.RequestID == "" {
				t.Fatalf("missing product attribution: %+v", event)
			}
			payload, _ := json.Marshal(event.Props)
			if strings.Contains(string(payload), "Private") || strings.Contains(string(payload), "customer.example") {
				t.Fatal("conversation content leaked into analytics")
			}
		}
	}
	refused := 0
	for _, event := range named(events(), "api.called") {
		if event.Outcome == track.Failure && (event.ErrorCode == "not_found" || event.ErrorCode == "record_changed") && event.RequestID != "" {
			refused++
		}
	}
	if refused != 6 {
		t.Fatalf("expected six refusal events, got %d", refused)
	}
}
