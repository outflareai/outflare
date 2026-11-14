package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/oauth"
)

func TestSalesContinuousCatchupAndConsent(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	person := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana Example", "identities": []map[string]any{{"namespace": "email", "value": "ana@customer.example"}}})
	brand := person["brand_id"].(string)
	root := "/v1/brands/" + brand + "/sales"
	me := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)
	user, email := me["id"].(string), me["email"].(string)
	owner.must(200, "PUT", root+"/team/"+user, map[string]any{"role": "ae"})
	owner.must(200, "PUT", "/v1/brands/"+brand+"/crm/mail/policy", map[string]any{"internal_domains": []string{strings.Split(email, "@")[1]}, "blocked_addresses": []string{}, "blocked_domains": []string{}, "version": 0})
	integration := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "emailbison", "credentials": map[string]string{"base_url": "https://send.acme.test", "api_key": "eb-replies"}})["id"].(string)
	lead := owner.must(201, "POST", root+"/bridge", map[string]any{"source": "portal", "source_id": "catchup", "record_id": person["id"], "integration_id": integration, "sender_id": "1", "reply_id": "501", "messages": []map[string]any{{"direction": "incoming", "from": "ana@customer.example", "to": []string{"sender@outbound.example"}, "cc": []string{email}, "subject": "Conversation", "body": "Original", "message_id": "<root@customer.example>", "provider_id": "501", "occurred_at": time.Now().Add(-30 * 24 * time.Hour).UTC().Format(time.RFC3339)}}})
	id := lead["id"].(string)
	// A fixture represents an already introduced account manager. This test
	// exercises sync; provider introductions have their own connector test.
	_, err := e.pool.Exec(context.Background(), `UPDATE sales_leads SET owner_id=$2,ae_id=$2,state='active',integration_id=NULL WHERE id=$1`, id, user)
	if err != nil {
		t.Fatal(err)
	}
	var connection string
	e.pool.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&connection)
	raw, _ := json.Marshal(map[string]any{"grant": map[string]string{"email": email, "subject": "test-catchup", "refresh_token": "test_not_a_real_refresh"}})
	_, err = e.pool.Exec(context.Background(), `INSERT INTO sales_mail_connections(id,workspace_id,brand_id,owner_id,credentials_sealed)VALUES($1,$2,$3,$4,$5)`, connection, ws, brand, user, e.box.Seal(raw, []byte(connection)))
	if err != nil {
		t.Fatal(err)
	}
	var catchup atomic.Bool
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" {
			json.NewEncoder(w).Encode(map[string]any{"access_token": "test_not_a_real_token", "scope": oauth.GoogleReadMailScope + " " + oauth.GoogleSendMailScope})
			return
		}
		if r.URL.Path == "/gmail/users/me/messages" {
			if !strings.Contains(r.URL.Query().Get("q"), "after:") {
				t.Error("catch-up has no start")
			}
			if r.URL.Query().Get("pageToken") == "older" {
				json.NewEncoder(w).Encode(map[string]any{"messages": []map[string]string{{"id": "parent", "threadId": "own-thread"}, {"id": "unrelated", "threadId": "unrelated"}, {"id": "draft", "threadId": "own-thread"}}})
				return
			}
			list := []map[string]string{{"id": "child", "threadId": "own-thread"}}
			if catchup.Load() {
				list = append(list, map[string]string{"id": "missed", "threadId": "own-thread"})
			}
			json.NewEncoder(w).Encode(map[string]any{"messages": list, "nextPageToken": "older"})
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/gmail/users/me/messages/")
		parent := "<root@customer.example>"
		from, to := email, "ana@customer.example"
		millis := time.Now().Add(-25 * 24 * time.Hour).UnixMilli()
		if name == "child" || name == "missed" {
			parent = "<parent@customer.example>"
			from, to = to, from
			millis += 2000
		}
		if name == "unrelated" {
			parent = "<unrelated@other.example>"
		}
		labels := []string{"INBOX"}
		if name == "draft" {
			labels = []string{"DRAFT"}
		}
		headers := []map[string]string{{"name": "From", "value": from}, {"name": "To", "value": to}, {"name": "Cc", "value": "sender@outbound.example"}, {"name": "Subject", "value": "Conversation"}, {"name": "Message-ID", "value": "<" + name + "@customer.example>"}, {"name": "In-Reply-To", "value": parent}, {"name": "Date", "value": "Sat, 01 Nov 2025 12:00:00 +0000"}}
		json.NewEncoder(w).Encode(map[string]any{"labelIds": labels, "internalDate": fmt.Sprint(millis), "payload": map[string]any{"mimeType": "text/plain", "headers": headers, "body": map[string]any{"data": base64.RawURLEncoding.EncodeToString([]byte("Full fictional message " + name))}}})
	}))
	defer fake.Close()
	e.oauth.Google = oauth.Client{ID: "test-sales", Secret: "test_not_a_real_secret"}
	e.oauth.Endpoints.GoogleToken = fake.URL + "/token"
	e.oauth.Endpoints.GoogleMail = fake.URL + "/gmail"
	e.oauth.HTTP = fake.Client()
	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 10 * time.Millisecond
	q.Only = []string{"sales.sync"}
	e.srv.RegisterJobs(q)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = q.Run(ctx, 1) }()
	defer func() { cancel(); <-done }()
	run := func(want int) {
		t.Helper()
		j, err := jobs.Enqueue(context.Background(), e.pool, jobs.NewJob{Kind: "sales.sync", WorkspaceID: &ws, Payload: map[string]string{"id": connection, "brand_id": brand}, MaxAttempts: 1})
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(40 * time.Second)
		for {
			var status, last string
			e.pool.QueryRow(context.Background(), `SELECT status,COALESCE(last_error,'') FROM jobs WHERE id=$1`, j).Scan(&status, &last)
			if status == "succeeded" {
				break
			}
			if status == "dead" || time.Now().After(deadline) {
				t.Fatalf("sync %s: %s", status, last)
			}
			time.Sleep(20 * time.Millisecond)
		}
		detail := owner.must(200, "GET", root+"/leads/"+id, nil)
		if len(detail["messages"].([]any)) != want {
			t.Fatalf("catch-up count: %v", detail["messages"])
		}
	}
	run(3)
	run(3) // pagination, parent closure and replay deduplication
	owner.must(200, "DELETE", root+"/mail/connection", nil)
	catchup.Store(true)
	// Reconsent gives a new grant id; the historical start stays on the lead.
	e.pool.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&connection)
	_, err = e.pool.Exec(context.Background(), `INSERT INTO sales_mail_connections(id,workspace_id,brand_id,owner_id,credentials_sealed)VALUES($1,$2,$3,$4,$5)`, connection, ws, brand, user, e.box.Seal(raw, []byte(connection)))
	if err != nil {
		t.Fatal(err)
	}
	run(4)
	owner.must(200, "PUT", root+"/team/"+user, map[string]any{"role": "none"})
	owner.must(404, "PUT", root+"/leads/"+id+"/draft", map[string]any{"revision": 0, "body": "Role was removed"})
	var count int
	e.pool.QueryRow(context.Background(), `SELECT count(*) FROM sales_mail_connections WHERE workspace_id=$1 AND owner_id=$2`, ws, user).Scan(&count)
	if count != 0 {
		t.Fatal("role removal retained mail authorization")
	}
}
