package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/inbox"
	"github.com/outflareai/outflare/engine/internal/track"
)

// A hot reply is written into the workspace's own HubSpot: the contact, a
// note with the reply, and a task to follow up for the lead's owner.
func TestRepliesToHubSpot(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, base, _ := sendingSetup(t, e, &fakeClock{t: time.Now()})
	ws := strings.TrimPrefix(base, "/v1/workspaces/")
	ctx := context.Background()
	mb := mailboxIDs(t, owner, base)["ana@acme.test"]

	var mu sync.Mutex
	calls := map[string][]map[string]any{}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		calls[r.URL.Path] = append(calls[r.URL.Path], body)
		mu.Unlock()
		switch r.URL.Path {
		case "/account-info/v3/details":
			w.Write([]byte(`{"portalId": 42}`))
		case "/crm/v3/objects/contacts/batch/upsert":
			w.Write([]byte(`{"results": [{"id": "c1"}]}`))
		case "/crm/v3/objects/notes":
			w.Write([]byte(`{"id": "n1"}`))
		case "/crm/v3/objects/tasks":
			w.Write([]byte(`{"id": "t1"}`))
		case "/crm/v3/owners":
			w.Write([]byte(`{"results": [{"id": "o7"}]}`))
		default:
			w.Write([]byte(`{"results": []}`))
		}
	}))
	defer hs.Close()
	e.integrations.Client.BaseURLs["hubspot"] = hs.URL
	h := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "hubspot", "credentials": map[string]string{"api_key": "pat-test-not-a-real-token"}})["id"].(string)
	owner.must(400, "PATCH", base+"/integrations/"+h, map[string]any{"config": map[string]any{"push_replies": []string{"warm"}}})
	owner.must(200, "PATCH", base+"/integrations/"+h, map[string]any{"config": map[string]any{"push_replies": []string{"hot"}, "reply_tasks": true}})

	owner.raw("POST", base+"/leads/import?list=Pilot", "text/csv", "email,first_name,last_name,company\nkim@prospect.test,Kim,Lee,Prospect\n")
	label := func(cat string) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, `
			INSERT INTO inbound_messages (workspace_id, mailbox_id, uidvalidity, uid, from_email, kind, body, lead_id, category, received_at, assigned_to)
			SELECT $1, $2, 0, nextval('inbound_external_uid_seq'), l.email, 'reply', 'Yes, <b>keen</b> to talk', l.id, $3, now(),
				(SELECT user_id FROM memberships WHERE workspace_id = $1 LIMIT 1)
			FROM leads l WHERE l.workspace_id = $1 AND l.email = 'kim@prospect.test' RETURNING id`, ws, mb, cat).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if err := inbox.Classified(ctx, e.pool, inbox.Classification{WorkspaceID: ws, InboundID: id, MailboxID: mb,
			Source: "mailbox", Category: &cat, By: "rules", Confidence: 1}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	label("not_interested") // not a label this connection asked for
	hot := label("interested")
	for i := 0; i < 200; i++ {
		mu.Lock()
		n := len(calls["/crm/v3/objects/tasks"])
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	mu.Lock()
	notes, tasks := calls["/crm/v3/objects/notes"], calls["/crm/v3/objects/tasks"]
	mu.Unlock()
	if n := len(notes); n != 1 {
		t.Fatalf("notes: %d", n)
	}
	note := notes[0]["properties"].(map[string]any)["hs_note_body"].(string)
	if !strings.Contains(note, "Interested") || !strings.Contains(note, "&lt;b&gt;keen") || !strings.Contains(note, "/inbox/"+hot) {
		t.Fatalf("note: %s", note)
	}
	task := tasks[0]["properties"].(map[string]any)
	if task["hubspot_owner_id"] != "o7" || !strings.Contains(task["hs_task_subject"].(string), "Kim Lee") {
		t.Fatalf("task: %v", task)
	}
	var pushed int
	for i := 0; i < 200 && pushed == 0; i++ {
		e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'crm.reply_pushed'`, ws).Scan(&pushed)
		time.Sleep(10 * time.Millisecond)
	}
	if pushed != 1 {
		t.Fatalf("crm.reply_pushed: %d", pushed)
	}
	ev := got()
	if n := len(named(ev, "crm.reply_pushed")); n != 1 {
		t.Fatalf("event: %d", n)
	}
}
