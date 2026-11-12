package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/jobs"
)

// An admin reads the team's work mail through the company's own Google
// Workspace connection (a service account with domain-wide delegation):
// enabled people's mailboxes sync without them signing in.
func TestCRMMailThroughWorkspace(t *testing.T) {
	e := freshEnv(t)
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
	queries, users := []string{}, map[string]bool{}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/sa" {
			// The JWT bearer grant: who the service account acts as.
			_ = r.ParseForm()
			parts := strings.Split(r.PostForm.Get("assertion"), ".")
			raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
			var claims struct{ Sub, Scope string }
			_ = json.Unmarshal(raw, &claims)
			if claims.Scope != "https://www.googleapis.com/auth/gmail.readonly" || claims.Sub == "" {
				w.WriteHeader(400)
				reply(map[string]any{"error": "unauthorized_client"})
				return
			}
			users[claims.Sub] = true
			reply(map[string]any{"access_token": "test-workspace-token:" + claims.Sub, "expires_in": 3600, "token_type": "Bearer"})
			return
		}
		user := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer test-workspace-token:")
		switch {
		case r.URL.Path == "/gmail/users/me/profile":
			reply(map[string]any{"emailAddress": user})
		case r.URL.Path == "/gmail/users/me/messages":
			queries = append(queries, r.URL.Query().Get("q"))
			reply(map[string]any{"messages": []map[string]string{{"id": "w1"}}})
		default:
			headers := []map[string]string{{"name": "From", "value": "ana@customer.example"}, {"name": "To", "value": user}, {"name": "Subject", "value": "Fictional order"}}
			if r.URL.Query().Get("format") == "metadata" {
				reply(map[string]any{"labelIds": []string{"INBOX"}, "payload": map[string]any{"headers": headers}})
				return
			}
			reply(map[string]any{"internalDate": "1762000000000", "labelIds": []string{"INBOX"}, "payload": map[string]any{"mimeType": "text/plain", "headers": headers,
				"body": map[string]any{"data": base64.RawURLEncoding.EncodeToString([]byte("A fictional order."))}}})
		}
	}))
	defer fake.Close()
	e.integrations.Client.BaseURLs["google.token"] = fake.URL + "/sa"
	e.integrations.Client.BaseURLs["gmail"] = fake.URL + "/gmail"
	e.oauth.Endpoints.GoogleMail = fake.URL + "/gmail"

	// The connection: the company's key, tested as one of its users.
	conn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "google_workspace", "credentials": map[string]string{"service_account_json": newServiceAccount(t), "admin_email": ownerEmail}})
	if conn["account"].(map[string]any)["name"] != "northpeak.example" && !users[strings.ToLower(ownerEmail)] {
		t.Fatalf("connection: %v", conn)
	}
	connID := conn["id"].(string)

	owner.must(200, "PUT", root+"/policy", map[string]any{"internal_domains": []string{"northpeak.example"}, "blocked_addresses": []string{}, "blocked_domains": []string{}, "capture_all_contacts": true, "version": 0})
	owner.must(400, "PUT", root+"/rollout", map[string]any{"enabled": true, "allowed_user_ids": []string{employeeID}, "excluded_user_ids": []string{}, "workspace_integration_id": anaID, "version": 0})
	rollout := owner.must(200, "PUT", root+"/rollout", map[string]any{"enabled": true, "allowed_user_ids": []string{employeeID}, "excluded_user_ids": []string{}, "workspace_integration_id": connID, "version": 0})
	if rollout["workspace_integration_id"] != connID || rollout["history_days"] != 0.0 {
		t.Fatalf("rollout: %v", rollout)
	}
	// The employee never signed in; their mailbox is read through Workspace.
	if c := employee.must(200, "GET", root+"/connection", nil); c["connected"] != true || c["via"] != "workspace" {
		t.Fatalf("employee connection: %v", c)
	}
	if c := owner.must(200, "GET", root+"/connection", nil); c["connected"] != false {
		t.Fatalf("someone not enabled got a mailbox: %v", c)
	}

	ctx, cancel := context.WithCancel(context.Background())
	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 10 * time.Millisecond
	q.Only = []string{"crm_mail.google_sync"}
	e.srv.RegisterJobs(q)
	done := make(chan struct{})
	go func() { defer close(done); _ = q.Run(ctx, 1) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(45 * time.Second)
	for employee.must(200, "GET", root+"/connection", nil)["last_synced_at"] == nil {
		if time.Now().After(deadline) {
			var last string
			_ = e.pool.QueryRow(context.Background(), `SELECT COALESCE(last_error,'') FROM jobs WHERE kind='crm_mail.google_sync' ORDER BY id DESC LIMIT 1`).Scan(&last)
			t.Fatalf("no sync: %s", last)
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	if !users["employee@northpeak.example"] || len(queries) == 0 || strings.Contains(queries[0], "after:") {
		t.Errorf("acted as %v, searched %q (whole history wanted)", users, queries)
	}
	mu.Unlock()
	got := employee.must(200, "GET", root+"/records/"+anaID+"/messages", nil)["data"].([]any)
	if len(got) != 1 || got[0].(map[string]any)["subject"] != "Fictional order" {
		t.Fatalf("captured: %v", got)
	}

	// The person can't switch it off themselves; the admin does.
	employee.must(400, "DELETE", root+"/connection", nil)
	owner.must(200, "PUT", root+"/rollout", map[string]any{"enabled": true, "allowed_user_ids": []string{employeeID}, "excluded_user_ids": []string{}, "workspace_integration_id": nil, "version": 1})
	if c := employee.must(200, "GET", root+"/connection", nil); c["connected"] != false {
		t.Fatalf("still read after the admin stopped it: %v", c)
	}
}
