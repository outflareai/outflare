package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/oauth"
	"github.com/prometheus/client_golang/prometheus"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestSalesWorkflowRecordingAndIsolation(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	person := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana Example", "identities": []map[string]any{{"namespace": "email", "value": "ana@customer.example"}}})
	brand := person["brand_id"].(string)
	root := "/v1/brands/" + brand + "/sales"
	me := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)
	user := me["id"].(string)
	owner.must(200, "PUT", root+"/team/"+user, map[string]any{"role": "ae"})
	integration := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "emailbison", "credentials": map[string]string{"base_url": "https://send.acme.test", "api_key": "eb-replies"}})["id"].(string)
	input := map[string]any{"source": "portal", "source_id": "test-source-lead", "record_id": person["id"], "integration_id": integration, "sender_id": "1", "reply_id": "501", "messages": []map[string]any{{"direction": "incoming", "from": "ana@customer.example", "to": []string{"ana@send-acme.test"}, "cc": []string{}, "subject": "Test conversation", "body": "Can we talk?", "message_id": "<test-root@customer.example>", "provider_id": "501", "occurred_at": time.Now().UTC().Format(time.RFC3339)}}}
	lead := owner.must(201, "POST", root+"/bridge", input)
	id := lead["id"].(string)
	leadPath := root + "/leads/" + id
	repeated := owner.must(201, "POST", root+"/bridge", input)
	if repeated["id"] != id {
		t.Fatal("bridge retry created a second lead")
	}
	owner.must(400, "POST", leadPath+"/actions", map[string]any{"action": "take", "version": lead["version"]})
	// A fixture represents this person's own separately authorized Sales grant.
	var connection string
	if err := e.pool.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&connection); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"grant": map[string]string{"email": me["email"].(string), "subject": "test-subject", "refresh_token": "test-not-real-refresh"}})
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO sales_mail_connections(id,workspace_id,brand_id,owner_id,credentials_sealed)VALUES($1,$2,$3,$4,$5)`, connection, ws, brand, user, e.box.Seal(raw, []byte(connection))); err != nil {
		t.Fatal(err)
	}
	lead = owner.must(200, "POST", leadPath+"/actions", map[string]any{"action": "take", "version": lead["version"]})
	if lead["owner_id"] != user || lead["ae_id"] != user || lead["sdr_id"] != nil || lead["state"] != "active" {
		t.Fatalf("direct AE claim: %v", lead)
	}
	owner.must(409, "POST", leadPath+"/actions", map[string]any{"action": "take", "version": 1})
	var count int
	e.pool.QueryRow(context.Background(), `SELECT count(*) FROM sales_messages WHERE lead_id=$1 AND kind='introduction'`, id).Scan(&count)
	if count != 1 {
		t.Fatal("claim must queue one introduction", count)
	}
	template := owner.must(201, "POST", root+"/templates", map[string]any{"name": "Pricing", "body": "Hi {first_name},\nHere are the details. — {my_name}"})
	draft := owner.must(200, "PUT", leadPath+"/draft", map[string]any{"revision": 0, "body": "", "template_id": template["id"]})
	originalVersion := draft["template_version_id"]
	owner.must(200, "PUT", root+"/templates/"+template["id"].(string), map[string]any{"version": 1, "name": "Pricing changed", "body": "New wording", "archived": true})
	draft = owner.must(200, "PUT", leadPath+"/draft", map[string]any{"revision": draft["revision"], "body": "Hi Ana, here is a personal volume estimate."})
	if draft["template_version_id"] != originalVersion {
		t.Fatal("editing draft rewrote template attribution")
	}
	if len(owner.must(200, "GET", root+"/templates", nil)["data"].([]any)) != 0 {
		t.Fatal("archived template still in picker")
	}
	// A provider acceptance fixture frees the introduction lane. No real mail.
	if _, err := e.pool.Exec(context.Background(), `UPDATE sales_messages SET status='accepted' WHERE workspace_id=$1 AND brand_id=$2 AND lead_id=$3 AND kind='introduction'`, ws, brand, id); err != nil {
		t.Fatal(err)
	}
	sent := owner.must(200, "POST", leadPath+"/send", map[string]any{"draft_id": draft["id"]})
	retry := owner.must(200, "POST", leadPath+"/send", map[string]any{"draft_id": draft["id"]})
	if retry["message_id"] != sent["message_id"] {
		t.Fatal("send retry created another provider operation")
	}
	lead = owner.must(200, "POST", leadPath+"/actions", map[string]any{"action": "details", "version": lead["version"], "note": "Shared across the handoff", "amount_minor": nil})
	lead = owner.must(200, "POST", leadPath+"/actions", map[string]any{"action": "confirm_customer", "version": lead["version"]})
	if lead["state"] != "customer" || lead["amount_minor"] != nil {
		t.Fatal("confirmation required money", lead)
	}
	lead = owner.must(200, "POST", leadPath+"/actions", map[string]any{"action": "reopen", "version": lead["version"]})
	lead = owner.must(200, "POST", leadPath+"/actions", map[string]any{"action": "archive", "version": lead["version"]})
	lead = owner.must(200, "POST", leadPath+"/actions", map[string]any{"action": "reopen", "version": lead["version"]})
	if lead["note"] != "Shared across the handoff" || lead["state"] != "active" {
		t.Fatal("reopen lost details", lead)
	}
	inv := owner.must(201, "POST", base+"/invites", map[string]any{"email": "other@acme.example", "role": "operator"})
	other := &client{t: t, h: e.h}
	other.must(200, "POST", "/v1/invites/accept", map[string]any{"token": inv["token"], "name": "Other Rep", "password": "not-real-test-password"})
	otherID := other.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	owner.must(200, "PUT", root+"/team/"+otherID, map[string]any{"role": "ae"})
	other.must(404, "GET", leadPath, nil)
	other.must(404, "PUT", leadPath+"/draft", map[string]any{"revision": 0, "body": "Not mine"})
	other.must(404, "PUT", root+"/templates/"+template["id"].(string), map[string]any{"version": 2, "name": "Not mine", "body": "No"})
	otherBrand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Other Brand", "handle": "other"})["id"].(string)
	owner.must(404, "GET", "/v1/brands/"+otherBrand+"/sales/leads/"+id, nil)
	detail := owner.must(200, "GET", leadPath, nil)
	if len(detail["messages"].([]any)) != 3 {
		t.Fatal("conversation copies duplicated", detail["messages"])
	}
	// A definite rejection can be retried without creating another draft or
	// message. An overlapping retry still cannot enqueue a second attempt.
	if _, err := e.pool.Exec(context.Background(), `UPDATE sales_messages SET status='failed' WHERE id=$1`, sent["message_id"]); err != nil {
		t.Fatal(err)
	}
	registry := prometheus.NewRegistry()
	registry.MustRegister(&metrics.Postgres{Pool: e.pool})
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	attentionReported := false
	for _, family := range families {
		if family.GetName() != "outflare_sales_messages" {
			continue
		}
		for _, point := range family.GetMetric() {
			if len(point.Label) == 1 && point.Label[0].GetName() == "status" && point.Label[0].GetValue() == "failed" && point.GetGauge().GetValue() == 1 {
				attentionReported = true
			}
		}
	}
	if !attentionReported {
		t.Fatal("failed Sales send is missing from the operational backlog metric")
	}
	if _, err := e.pool.Exec(context.Background(), `UPDATE jobs SET status='succeeded' WHERE workspace_id=$1 AND kind='sales.send' AND payload->>'id'=$2`, ws, sent["message_id"]); err != nil {
		t.Fatal(err)
	}
	retried := owner.must(200, "POST", leadPath+"/actions", map[string]any{"action": "retry_send", "version": detail["lead"].(map[string]any)["version"], "message_id": sent["message_id"]})
	owner.must(409, "POST", leadPath+"/actions", map[string]any{"action": "retry_send", "version": retried["version"], "message_id": sent["message_id"]})
	e.pool.QueryRow(context.Background(), `SELECT count(*) FROM sales_template_versions WHERE workspace_id=$1 AND brand_id=$2`, ws, brand).Scan(&count)
	if count != 2 {
		t.Fatal("template versions lost", count)
	}
	e.pool.QueryRow(context.Background(), `SELECT count(*) FROM attribute_changes WHERE workspace_id=$1 AND record_id=$2 AND field_key='sales_workflow'`, ws, person["id"]).Scan(&count)
	if count < 8 {
		t.Fatal("record history missing Sales changes", count)
	}

	// The provider accepts the email but the HTTP response is lost. A worker
	// restart/retry finds its RFC id in Sent and must not call send again.
	var mu sync.Mutex
	sends := 0
	acceptedRFC := ""
	returnReceipt := false
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			json.NewEncoder(w).Encode(map[string]any{"access_token": "test-not-real-access", "scope": oauth.GoogleReadMailScope + " " + oauth.GoogleSendMailScope})
		case "/gmail/users/me/messages":
			mu.Lock()
			accepted := acceptedRFC
			mu.Unlock()
			if accepted != "" && strings.Contains(r.URL.Query().Get("q"), strings.Trim(accepted, "<> ")) {
				json.NewEncoder(w).Encode(map[string]any{"messages": []map[string]string{{"id": "test-sent-provider-id", "threadId": "test-own-thread"}}})
			} else {
				json.NewEncoder(w).Encode(map[string]any{"messages": []any{}})
			}
		case "/gmail/users/me/messages/send":
			var payload struct {
				Raw string `json:"raw"`
			}
			json.NewDecoder(r.Body).Decode(&payload)
			raw, err := base64.RawURLEncoding.DecodeString(payload.Raw)
			if err != nil {
				t.Error(err)
			}
			mail, e := mail.ReadMessage(strings.NewReader(string(raw)))
			if e != nil {
				t.Error(e)
				w.WriteHeader(400)
				return
			}
			if mail.Header.Get("In-Reply-To") == "" || mail.Header.Get("References") == "" {
				t.Error("reply lost thread headers")
			}
			if !strings.Contains(mail.Header.Get("Cc"), "ana@send-acme.test") {
				t.Error("personal reply dropped the original Bison sender from CC")
			}
			mu.Lock()
			sends++
			acceptedRFC = mail.Header.Get("Message-ID")
			receipt := returnReceipt
			mu.Unlock()
			if receipt {
				if !strings.Contains(mail.Header.Get("Cc"), "other@acme.example") {
					t.Error("handoff dropped the introduced account manager from CC")
				}
				json.NewEncoder(w).Encode(map[string]string{"id": "test-handoff-provider-id", "threadId": "test-own-thread"})
				return
			}
			w.WriteHeader(500)
		default:
			w.WriteHeader(404)
		}
	}))
	defer fake.Close()
	e.oauth.Google = oauth.Client{ID: "test-sales-client", Secret: "test-not-real-secret"}
	e.oauth.Endpoints.GoogleToken = fake.URL + "/token"
	e.oauth.Endpoints.GoogleMail = fake.URL + "/gmail"
	e.oauth.HTTP = fake.Client()
	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 10 * time.Millisecond
	q.Only = []string{"sales.send"}
	e.srv.RegisterJobs(q)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = q.Run(ctx, 1) }()
	defer func() { cancel(); <-done }()
	waitStatus := func(want string) {
		t.Helper()
		deadline := time.Now().Add(45 * time.Second)
		for {
			var state string
			e.pool.QueryRow(context.Background(), `SELECT status FROM sales_messages WHERE id=$1`, sent["message_id"]).Scan(&state)
			if state == want {
				return
			}
			if time.Now().After(deadline) {
				var last string
				e.pool.QueryRow(context.Background(), `SELECT COALESCE(last_error,'') FROM jobs WHERE kind='sales.send' ORDER BY created_at DESC LIMIT 1`).Scan(&last)
				t.Fatalf("send state %s, wanted %s: %s", state, want, last)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitStatus("uncertain")
	detail = owner.must(200, "GET", leadPath, nil)
	owner.must(200, "POST", leadPath+"/actions", map[string]any{"action": "archive", "version": detail["lead"].(map[string]any)["version"]})
	_, err = jobs.Enqueue(context.Background(), e.pool, jobs.NewJob{Kind: "sales.send", WorkspaceID: &ws, Payload: map[string]any{"id": sent["message_id"], "brand_id": brand}, MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	waitStatus("accepted")
	detail = owner.must(200, "GET", leadPath, nil)
	owner.must(200, "POST", leadPath+"/actions", map[string]any{"action": "reopen", "version": detail["lead"].(map[string]any)["version"]})
	mu.Lock()
	calls := sends
	mu.Unlock()
	if calls != 1 {
		t.Fatalf("ambiguous send was repeated %d times", calls)
	}

	// A role change followed by an SDR handoff retains the introducing person.
	// The account manager sees the shared evidence, but never another rep's draft.
	owner.must(200, "PUT", root+"/team/"+user, map[string]any{"role": "sdr"})
	if err := e.pool.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&connection); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(map[string]any{"grant": map[string]string{"email": "other@acme.example", "subject": "test-other-subject", "refresh_token": "test-not-real-refresh"}})
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO sales_mail_connections(id,workspace_id,brand_id,owner_id,credentials_sealed)VALUES($1,$2,$3,$4,$5)`, connection, ws, brand, otherID, e.box.Seal(raw, []byte(connection))); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	returnReceipt = true
	mu.Unlock()
	detail = owner.must(200, "GET", leadPath, nil)
	lead = owner.must(200, "POST", leadPath+"/actions", map[string]any{"action": "handoff", "version": detail["lead"].(map[string]any)["version"], "assignee_id": otherID})
	if lead["owner_id"] != user || lead["sdr_id"] != user {
		t.Fatal("handoff changed ownership before provider acceptance or lost SDR", lead)
	}
	var handoffID string
	if err := e.pool.QueryRow(context.Background(), `SELECT id::text FROM sales_messages WHERE workspace_id=$1 AND brand_id=$2 AND lead_id=$3 AND kind='handoff'`, ws, brand, id).Scan(&handoffID); err != nil {
		t.Fatal(err)
	}
	sent = map[string]any{"message_id": handoffID}
	waitStatus("accepted")
	detail = other.must(200, "GET", leadPath, nil)
	lead = detail["lead"].(map[string]any)
	if lead["owner_id"] != otherID || lead["ae_id"] != otherID || lead["sdr_id"] != user || lead["note"] != "Shared across the handoff" {
		t.Fatal("handoff lost ownership or shared notes", lead)
	}
	if len(detail["messages"].([]any)) != 4 || detail["draft"] != nil {
		t.Fatal("handoff lost conversation or exposed a personal draft", detail)
	}
	owner.must(403, "POST", leadPath+"/actions", map[string]any{"action": "confirm_customer", "version": lead["version"]})
	if len(named(events(), "sales.customer_confirmed")) != 1 {
		t.Fatal("customer confirmation event missing")
	}
	owner.must(200, "DELETE", base+"/records/"+person["id"].(string), nil)
	owner.must(404, "GET", leadPath, nil)
	if err := e.pool.QueryRow(context.Background(), `SELECT
	 (SELECT count(*) FROM sales_leads WHERE workspace_id=$1 AND brand_id=$2)+
	 (SELECT count(*) FROM sales_drafts WHERE workspace_id=$1 AND brand_id=$2)+
	 (SELECT count(*) FROM sales_messages WHERE workspace_id=$1 AND brand_id=$2)+
	 (SELECT count(*) FROM sales_message_copies WHERE workspace_id=$1 AND brand_id=$2)+
	 (SELECT count(*) FROM sales_events WHERE workspace_id=$1 AND brand_id=$2)`, ws, brand).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("erasing a person retained Sales evidence", count)
	}
}
