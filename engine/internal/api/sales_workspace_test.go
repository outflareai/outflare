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

// Sales uses the company's Google Workspace connection (ADR 15): a rep the
// CRM email rollout allows connects without signing in to Google, Sales
// reads their mailbox through it, and taking them off the rollout stops it.
func TestSalesMailThroughWorkspace(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana", "identities": []map[string]any{{"namespace": "email", "value": "ana@customer.example"}}})
	brand := ana["brand_id"].(string)
	crm, sales := "/v1/brands/"+brand+"/crm/mail", "/v1/brands/"+brand+"/sales"
	ownerEmail := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["email"].(string)
	inv := owner.must(201, "POST", base+"/invites", map[string]any{"email": "rep@northpeak.example", "role": "operator"})
	rep := &client{t: t, h: e.h}
	rep.must(200, "POST", "/v1/invites/accept", map[string]any{"token": inv["token"], "name": "Rep", "password": "not-real-test-password"})
	repID := rep.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	owner.must(200, "PUT", sales+"/team/"+repID, map[string]any{"role": "sdr"})

	var mu sync.Mutex
	acted := map[string]bool{}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/sa" {
			_ = r.ParseForm()
			parts := strings.Split(r.PostForm.Get("assertion"), ".")
			raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
			var claims struct{ Sub, Scope string }
			_ = json.Unmarshal(raw, &claims)
			acted[claims.Sub+" "+claims.Scope] = true
			reply(map[string]any{"access_token": "test-workspace-token:" + claims.Sub, "expires_in": 3600, "token_type": "Bearer"})
			return
		}
		user := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer test-workspace-token:")
		switch r.URL.Path {
		case "/gmail/users/me/profile":
			reply(map[string]any{"emailAddress": user})
		default:
			reply(map[string]any{"messages": []map[string]string{}})
		}
	}))
	defer fake.Close()
	e.integrations.Client.BaseURLs["google.token"] = fake.URL + "/sa"
	e.integrations.Client.BaseURLs["gmail"] = fake.URL + "/gmail"
	e.oauth.Endpoints.GoogleMail = fake.URL + "/gmail"

	conn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "google_workspace_mail", "credentials": map[string]string{"service_account_json": newServiceAccount(t), "admin_email": ownerEmail}})
	connID := conn["id"].(string)

	// Before the rollout names the connection, Workspace isn't offered.
	if me := rep.must(200, "GET", sales+"/me", nil); me["workspace_available"] != false || me["mail_via"] != "none" {
		t.Fatalf("before rollout: %v", me)
	}
	rep.must(403, "POST", sales+"/mail/workspace", nil)

	owner.must(200, "PUT", crm+"/policy", map[string]any{"internal_domains": []string{"northpeak.example"}, "blocked_addresses": []string{}, "blocked_domains": []string{}, "version": 0})
	owner.must(200, "PUT", crm+"/rollout", map[string]any{"enabled": true, "allowed_user_ids": []string{repID}, "excluded_user_ids": []string{}, "workspace_integration_id": connID, "version": 0})
	if me := rep.must(200, "GET", sales+"/me", nil); me["workspace_available"] != true {
		t.Fatalf("after rollout: %v", me)
	}
	// Only the person, in their own session; a manager without a Sales role can't.
	owner.must(403, "POST", sales+"/mail/workspace", nil)
	rep.must(200, "POST", sales+"/mail/workspace", nil)
	if me := rep.must(200, "GET", sales+"/me", nil); me["connected"] != true || me["mail_via"] != "workspace" {
		t.Fatalf("connected: %v", me)
	}

	// The catch-up reads the rep's own mailbox through the service account.
	ctx, cancel := context.WithCancel(context.Background())
	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 10 * time.Millisecond
	q.Only = []string{"sales.sync"}
	e.srv.RegisterJobs(q)
	done := make(chan struct{})
	go func() { defer close(done); _ = q.Run(ctx, 1) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(45 * time.Second)
	for {
		mu.Lock()
		ok := acted["rep@northpeak.example https://www.googleapis.com/auth/gmail.readonly"]
		mu.Unlock()
		if ok {
			break
		}
		if time.Now().After(deadline) {
			var last string
			_ = e.pool.QueryRow(context.Background(), `SELECT COALESCE(last_error,'') FROM jobs WHERE kind='sales.sync' ORDER BY id DESC LIMIT 1`).Scan(&last)
			t.Fatalf("sync never read the rep's mailbox: %v %s", acted, last)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Excluding the rep by address disconnects Sales too.
	owner.must(200, "PUT", crm+"/rollout", map[string]any{"enabled": true, "allowed_user_ids": []string{repID}, "excluded_user_ids": []string{},
		"excluded_emails": []string{"rep@northpeak.example"}, "workspace_integration_id": connID, "version": 1})
	if me := rep.must(200, "GET", sales+"/me", nil); me["connected"] != false || me["mail_via"] != "none" || me["workspace_available"] != false {
		t.Fatalf("still connected after exclusion: %v", me)
	}
	rep.must(403, "POST", sales+"/mail/workspace", nil)
}
