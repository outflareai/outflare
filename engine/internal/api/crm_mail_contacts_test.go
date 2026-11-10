package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/oauth"
)

// With capture of every contact on, a person's Gmail is saved onto any CRM
// contact or company it belongs to, without picking records first.
func TestCRMMailCaptureEveryContact(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana", "identities": []map[string]any{{"namespace": "email", "value": "ana@customer.example"}}})
	brand := ana["brand_id"].(string)
	customer := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Customer", "identities": []map[string]any{{"namespace": "domain", "value": "customer.example"}}})["id"].(string)
	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme", "identities": []map[string]any{{"namespace": "domain", "value": "acme.example"}}})["id"].(string)
	root := "/v1/brands/" + brand + "/crm/mail"
	me := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)
	user, email := me["id"].(string), me["email"].(string)

	// Each message: who it's from and who else is on it.
	mail := map[string][2]string{
		"b1": {"ana@customer.example", ""},                 // Ana herself, though Customer's domain fits too
		"b2": {"bob@mail.acme.example", ""},                // a company by its domain
		"b3": {"stranger@unknown.example", ""},             // nobody in the CRM
		"b4": {"ana@customer.example", "bob@acme.example"}, // two companies
		"b5": {"carol@customer.example", ""},               // someone at Customer
	}
	var mu sync.Mutex
	nonce, challenge, query := "", "", ""
	full := map[string]int{}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/token" {
			_ = r.ParseForm()
			if r.Form.Get("grant_type") == "authorization_code" {
				hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
				if base64.RawURLEncoding.EncodeToString(hash[:]) != challenge {
					t.Error("missing PKCE binding")
				}
				reply(map[string]any{"access_token": "test-access-not-a-real-token", "refresh_token": "test-refresh-not-a-real-token", "scope": "openid email " + oauth.GoogleReadMailScope, "id_token": idToken(map[string]any{"sub": "test-google-subject", "email": email, "email_verified": true, "aud": "test-crm-client", "iss": "https://accounts.google.com", "exp": time.Now().Add(time.Hour).Unix(), "nonce": nonce})})
				return
			}
			reply(map[string]any{"access_token": "test-access-not-a-real-token", "scope": "openid email " + oauth.GoogleReadMailScope})
			return
		}
		if r.URL.Path == "/gmail/users/me/messages" {
			query = r.URL.Query().Get("q")
			reply(map[string]any{"messages": []map[string]string{{"id": "b1"}, {"id": "b2"}, {"id": "b3"}, {"id": "b4"}, {"id": "b5"}}})
			return
		}
		msg := strings.TrimPrefix(r.URL.Path, "/gmail/users/me/messages/")
		headers := []map[string]string{{"name": "From", "value": mail[msg][0]}, {"name": "To", "value": email}, {"name": "Subject", "value": "Fictional " + msg}}
		if mail[msg][1] != "" {
			headers = append(headers, map[string]string{"name": "Cc", "value": mail[msg][1]})
		}
		if r.URL.Query().Get("format") == "metadata" {
			reply(map[string]any{"labelIds": []string{"INBOX"}, "payload": map[string]any{"headers": headers}})
			return
		}
		full[msg]++
		reply(map[string]any{"internalDate": "1762000000000", "labelIds": []string{"INBOX"}, "payload": map[string]any{"mimeType": "text/plain", "headers": headers, "body": map[string]any{"data": base64.RawURLEncoding.EncodeToString([]byte("A fictional note."))}}})
	}))
	defer fake.Close()
	e.oauth.Google = oauth.Client{ID: "test-crm-client", Secret: "test-client-secret-not-real"}
	e.oauth.Endpoints.GoogleAuth = fake.URL + "/authorize"
	e.oauth.Endpoints.GoogleToken = fake.URL + "/token"
	e.oauth.Endpoints.GoogleMail = fake.URL + "/gmail"
	e.oauth.HTTP = fake.Client()

	owner.must(200, "PUT", root+"/policy", map[string]any{"internal_domains": []string{"northpeak.example"}, "blocked_addresses": []string{}, "blocked_domains": []string{}, "version": 0})
	owner.must(200, "PUT", root+"/rollout", map[string]any{"enabled": true, "allowed_user_ids": []string{user}, "excluded_user_ids": []string{}, "version": 0})
	start := owner.must(200, "POST", root+"/google/authorize", nil)
	authURL, err := url.Parse(start["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	nonce, challenge = authURL.Query().Get("nonce"), authURL.Query().Get("code_challenge")
	mu.Unlock()
	owner.must(302, "GET", "/v1/crm-mail/google/callback?state="+url.QueryEscape(authURL.Query().Get("state"))+"&code=test-code-not-real", nil)

	// A person can't switch it on for the brand from their own settings.
	prefs := owner.must(200, "PUT", root+"/preferences", map[string]any{"blocked_addresses": []string{}, "blocked_domains": []string{}, "capture_all_contacts": true, "version": 0})
	if prefs["capture_all_contacts"] != false {
		t.Fatalf("preferences set it: %v", prefs)
	}
	got := owner.must(200, "PUT", root+"/policy", map[string]any{"internal_domains": []string{"northpeak.example"}, "blocked_addresses": []string{}, "blocked_domains": []string{}, "capture_all_contacts": true, "version": 1})
	if got["capture_all_contacts"] != true {
		t.Fatalf("policy: %v", got)
	}
	if owner.must(200, "GET", root+"/preferences", nil)["capture_all_contacts"] != true {
		t.Fatal("members don't see it")
	}
	var audited int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE workspace_id=$1 AND action='crm_mail.capture_all_changed' AND (data->>'mailboxes')::int=1`, ws).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("audit %d %v", audited, err)
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
	for owner.must(200, "GET", root+"/connection", nil)["last_synced_at"] == nil {
		if time.Now().After(deadline) {
			var last string
			_ = e.pool.QueryRow(context.Background(), `SELECT COALESCE(last_error,'') FROM jobs WHERE kind='crm_mail.google_sync' ORDER BY id DESC LIMIT 1`).Scan(&last)
			t.Fatalf("no sync: %s", last)
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	if strings.Contains(query, "ana@customer.example") || !strings.Contains(query, "-category:promotions") || !strings.Contains(query, "-label:hr") {
		t.Errorf("query %q", query)
	}
	if full["b3"] != 0 || full["b4"] != 0 {
		t.Errorf("unmatched bodies fetched: %v", full)
	}
	mu.Unlock()
	subjects := func(rec string) []string {
		var out []string
		for _, m := range owner.must(200, "GET", root+"/records/"+rec+"/messages", nil)["data"].([]any) {
			out = append(out, m.(map[string]any)["subject"].(string))
		}
		return out
	}
	for rec, want := range map[string]string{ana["id"].(string): "Fictional b1", acme: "Fictional b2", customer: "Fictional b5"} {
		if got := subjects(rec); len(got) != 1 || got[0] != want {
			t.Errorf("record %s: %v, want %s", rec, got, want)
		}
	}
	var total int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM crm_mail_messages WHERE workspace_id=$1`, ws).Scan(&total); err != nil || total != 3 {
		t.Fatalf("saved %d %v", total, err)
	}
}
