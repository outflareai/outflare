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
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/oauth"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestCRMGoogleConsentAndCapture(t *testing.T) {
	captured := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	person := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana", "identities": []map[string]any{{"namespace": "email", "value": "ana@customer.example"}}})
	id := person["id"].(string)
	brand := person["brand_id"].(string)
	root := "/v1/brands/" + brand + "/crm/mail"
	messages := root + "/records/" + id + "/messages"
	target := root + "/records/" + id + "/capture"
	me := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)
	user := me["id"].(string)
	email := me["email"].(string)
	inv := owner.must(201, "POST", base+"/invites", map[string]any{"email": "finance@northpeak.example", "role": "operator"})
	finance := &client{t: t, h: e.h}
	finance.must(200, "POST", "/v1/invites/accept", map[string]any{"token": inv["token"], "name": "Finance colleague", "password": "not-real-test-password"})
	financeID := finance.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	var revoke atomic.Bool
	var mu sync.Mutex
	nonce, challenge := "", ""
	googleEmail := email
	fullCalls := map[string]int{}
	tokenCalls := 0
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		if r.URL.Path == "/token" {
			_ = r.ParseForm()
			mu.Lock()
			defer mu.Unlock()
			tokenCalls++
			if r.Form.Get("grant_type") == "authorization_code" {
				hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
				if base64.RawURLEncoding.EncodeToString(hash[:]) != challenge {
					t.Error("missing PKCE binding")
				}
				reply(map[string]any{"access_token": "test-access-not-a-real-token", "refresh_token": "test-refresh-not-a-real-token", "scope": "openid email " + oauth.GoogleReadMailScope, "id_token": idToken(map[string]any{"sub": "test-google-subject", "email": googleEmail, "email_verified": true, "aud": "test-crm-client", "iss": "https://accounts.google.com", "exp": time.Now().Add(time.Hour).Unix(), "nonce": nonce})})
				return
			}
			reply(map[string]any{"access_token": "test-access-not-a-real-token", "scope": "openid email " + oauth.GoogleReadMailScope})
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-access-not-a-real-token" {
			t.Error("wrong personal token")
		}
		if r.URL.Path == "/gmail/users/me/messages" {
			if !strings.Contains(r.URL.Query().Get("q"), "ana@customer.example") || !strings.Contains(r.URL.Query().Get("q"), "-in:drafts") {
				t.Error("missing capture search restriction")
			}
			list := []map[string]string{{"id": "a1"}, {"id": "a2"}, {"id": "a3"}, {"id": "a4"}}
			if revoke.Load() {
				list = append(list, map[string]string{"id": "a5"})
			}
			reply(map[string]any{"messages": list})
			return
		}
		msg := strings.TrimPrefix(r.URL.Path, "/gmail/users/me/messages/")
		headers := []map[string]string{{"name": "From", "value": "ana@customer.example"}, {"name": "To", "value": email}, {"name": "Subject", "value": "Customer renewal"}}
		labels := []string{"INBOX"}
		if msg == "a2" {
			headers = append(headers, map[string]string{"name": "Cc", "value": "hr@northpeak.example"})
		}
		if msg == "a3" {
			headers[0]["value"] = "unrelated@other.example"
		}
		if msg == "a4" {
			labels = []string{"DRAFT"}
		}
		if r.URL.Query().Get("format") == "metadata" {
			if r.URL.Query().Get("fields") != "labelIds,payload(headers)" {
				t.Error("metadata request included unnecessary content fields")
			}
			reply(map[string]any{"labelIds": labels, "payload": map[string]any{"headers": headers}})
			return
		}
		if msg == "a5" && revoke.Load() {
			err := pgx.BeginFunc(r.Context(), e.pool, func(tx pgx.Tx) error {
				if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "crm-rollout:"+brand); err != nil {
					return err
				}
				if _, err := tx.Exec(r.Context(), `UPDATE crm_mail_rollout SET config=jsonb_set(config,'{enabled}','false'::jsonb) WHERE workspace_id=$1 AND brand_id=$2`, ws, brand); err != nil {
					return err
				}
				_, err := tx.Exec(r.Context(), `DELETE FROM crm_google_connections WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3`, ws, brand, user)
				return err
			})
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
		}
		mu.Lock()
		fullCalls[msg]++
		mu.Unlock()
		reply(map[string]any{"internalDate": "1762000000000", "labelIds": labels, "payload": map[string]any{"mimeType": "multipart/mixed", "headers": headers, "parts": []any{map[string]any{"mimeType": "text/plain", "body": map[string]any{"data": base64.RawURLEncoding.EncodeToString([]byte("A fictional customer renewal."))}}, map[string]any{"mimeType": "application/pdf", "filename": "contract.pdf", "body": map[string]any{"attachmentId": "must-not-be-fetched"}}}}})
	}))
	defer fake.Close()
	e.oauth.Google = oauth.Client{ID: "test-crm-client", Secret: "test-client-secret-not-real"}
	e.oauth.Endpoints.GoogleAuth = fake.URL + "/authorize"
	e.oauth.Endpoints.GoogleToken = fake.URL + "/token"
	e.oauth.Endpoints.GoogleMail = fake.URL + "/gmail"
	e.oauth.HTTP = fake.Client()
	if got := owner.must(200, "GET", root+"/connection", nil); got["eligible"] != false {
		t.Fatal("default rollout was open", got)
	}
	owner.must(403, "POST", root+"/google/authorize", nil)
	owner.must(200, "PUT", root+"/policy", map[string]any{"internal_domains": []string{"northpeak.example"}, "blocked_addresses": []string{}, "blocked_domains": []string{}, "version": 0})
	finance.must(403, "PUT", root+"/rollout", map[string]any{"enabled": true, "allowed_user_ids": []string{financeID}, "excluded_user_ids": []string{}, "version": 0})
	owner.must(200, "PUT", root+"/rollout", map[string]any{"enabled": true, "allowed_user_ids": []string{user, financeID}, "excluded_user_ids": []string{financeID}, "version": 0})
	finance.must(403, "POST", root+"/google/authorize", nil)
	provider := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Gmail", "identities": []map[string]any{{"namespace": "domain", "value": "gmail.com"}}})["id"].(string)
	owner.must(400, "PUT", root+"/records/"+provider+"/capture", map[string]any{"enabled": true, "version": 0})

	// A different Google account cannot be attached, even by an eligible admin.
	wrong := owner.must(200, "POST", root+"/google/authorize", nil)
	wrongURL, err := url.Parse(wrong["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	nonce, challenge = wrongURL.Query().Get("nonce"), wrongURL.Query().Get("code_challenge")
	googleEmail = "other@unrelated.example"
	mu.Unlock()
	owner.must(302, "GET", "/v1/crm-mail/google/callback?state="+url.QueryEscape(wrongURL.Query().Get("state"))+"&code=test-code-not-real", nil)
	if owner.must(200, "GET", root+"/connection", nil)["connected"] != false {
		t.Fatal("wrong Google account was connected")
	}
	mu.Lock()
	googleEmail = email
	mu.Unlock()
	start := owner.must(200, "POST", root+"/google/authorize", nil)
	authURL, err := url.Parse(start["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if authURL.Query().Get("scope") != "openid email "+oauth.GoogleReadMailScope || authURL.Query().Get("include_granted_scopes") != "false" {
		t.Fatal("personal grant requested broad access", authURL.Query().Get("scope"))
	}
	mu.Lock()
	nonce, challenge = authURL.Query().Get("nonce"), authURL.Query().Get("code_challenge")
	mu.Unlock()
	callback := "/v1/crm-mail/google/callback?state=" + url.QueryEscape(authURL.Query().Get("state")) + "&code=test-code-not-real"
	finance.must(400, "GET", callback, nil) // wrong person cannot consume the flow
	owner.must(302, "GET", callback, nil)
	owner.must(400, "GET", callback, nil)
	if got := owner.must(200, "GET", root+"/connection", nil); got["connected"] != true || got["capture_count"] != float64(0) {
		t.Fatal(got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 10 * time.Millisecond
	q.Only = []string{"crm_mail.google_sync"}
	e.srv.RegisterJobs(q)
	done := make(chan struct{})
	go func() { defer close(done); _ = q.Run(ctx, 1) }()
	defer func() { cancel(); <-done }()
	wait := func(what string, check func() bool) {
		t.Helper()
		deadline := time.Now().Add(45 * time.Second)
		for !check() {
			if time.Now().After(deadline) {
				var last string
				_ = e.pool.QueryRow(context.Background(), `SELECT COALESCE(last_error,'') FROM jobs WHERE kind='crm_mail.google_sync' ORDER BY id DESC LIMIT 1`).Scan(&last)
				t.Fatalf("waited for %s: %s", what, last)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	finished := func() int {
		var count int
		_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE workspace_id=$1 AND kind='crm_mail.google_sync' AND status='succeeded'`, ws).Scan(&count)
		return count
	}
	wait("empty capture selection", func() bool { return finished() >= 1 })
	mu.Lock()
	calls := tokenCalls
	mu.Unlock()
	if calls != 2 {
		t.Fatalf("mailbox was read before records were selected: token calls %d", calls)
	}
	owner.must(200, "PUT", target, map[string]any{"enabled": true, "version": 0})
	wait("selected-record capture", func() bool { return owner.must(200, "GET", root+"/connection", nil)["last_synced_at"] != nil })
	rows := owner.must(200, "GET", messages, nil)["data"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["body"] != "A fictional customer renewal." {
		t.Fatal("capture result", rows)
	}
	if len(finance.must(200, "GET", messages, nil)["data"].([]any)) != 0 {
		t.Fatal("private captured email was shared")
	}
	mu.Lock()
	if fullCalls["a2"] != 0 || fullCalls["a3"] != 0 || fullCalls["a4"] != 0 {
		t.Error("sensitive/unmatched/draft body was fetched", fullCalls)
	}
	mu.Unlock()
	wait("capture job completion", func() bool { return finished() >= 2 })
	baseline := finished()
	owner.must(200, "POST", root+"/sync", nil)
	wait("idempotent capture", func() bool { return finished() > baseline })
	if len(owner.must(200, "GET", messages, nil)["data"].([]any)) != 1 {
		t.Fatal("duplicate capture")
	}
	owner.must(200, "DELETE", root+"/messages/"+rows[0].(map[string]any)["id"].(string), nil)
	baseline = finished()
	owner.must(200, "POST", root+"/sync", nil)
	wait("capture after deletion", func() bool { return finished() > baseline })
	if len(owner.must(200, "GET", messages, nil)["data"].([]any)) != 0 {
		t.Fatal("deleted source email reappeared")
	}
	mu.Lock()
	if fullCalls["a1"] != 1 {
		t.Error("known or deleted message bodies were fetched again", fullCalls["a1"])
	}
	mu.Unlock()
	// An administrator revokes the grant while a matching body is in flight.
	// The final transaction must not store it after the provider responds.
	revoke.Store(true)
	baseline = finished()
	owner.must(200, "POST", root+"/sync", nil)
	wait("revocation during provider read", func() bool { return finished() > baseline })
	if len(owner.must(200, "GET", messages, nil)["data"].([]any)) != 0 {
		t.Fatal("in-flight capture ignored revocation")
	}
	owner.must(200, "PUT", root+"/rollout", map[string]any{"enabled": true, "allowed_user_ids": []string{user}, "excluded_user_ids": []string{user}, "version": 1})
	if got := owner.must(200, "GET", root+"/connection", nil); got["connected"] != false || got["eligible"] != false {
		t.Fatal("eligibility removal retained grant", got)
	}
	owner.must(403, "POST", root+"/sync", nil)
	// Read-only members must still be able to revoke their own stored grant.
	// This fixture represents a previously connected member whose role changed.
	if _, err := e.pool.Exec(context.Background(), `UPDATE memberships SET role='viewer',role_id=NULL WHERE workspace_id=$1 AND user_id=$2`, ws, financeID); err != nil {
		t.Fatal(err)
	}
	placeholder := e.box.Seal([]byte("{}"), []byte(id))
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO crm_google_connections(id,workspace_id,brand_id,owner_id,credentials_sealed)VALUES($1,$2,$3,$4,$5)`, id, ws, brand, financeID, placeholder); err != nil {
		t.Fatal(err)
	}
	finance.must(200, "DELETE", root+"/connection", nil)
	if finance.must(200, "GET", root+"/connection", nil)["connected"] != false {
		t.Fatal("read-only owner could not revoke their grant")
	}
	// Offboarding is enforced by the membership lifecycle, not just by the
	// Google settings page. Stale roster entries and grants cannot survive it.
	owner.must(200, "PUT", root+"/rollout", map[string]any{"enabled": true, "allowed_user_ids": []string{user, financeID}, "excluded_user_ids": []string{user, financeID}, "version": 2})
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO crm_google_connections(id,workspace_id,brand_id,owner_id,credentials_sealed)VALUES($1,$2,$3,$4,$5)`, id, ws, brand, financeID, placeholder); err != nil {
		t.Fatal(err)
	}
	owner.must(204, "DELETE", base+"/members/"+financeID, nil)
	var remaining int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM crm_google_connections WHERE workspace_id=$1 AND owner_id=$2`, ws, financeID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("offboarding kept a Google grant", remaining, err)
	}
	roster := owner.must(200, "GET", root+"/rollout", nil)
	for _, key := range []string{"allowed_user_ids", "excluded_user_ids"} {
		for _, member := range roster[key].([]any) {
			if member == financeID {
				t.Fatal("offboarding kept a stale roster entry", key)
			}
		}
	}
	seen := map[string]bool{}
	for _, event := range captured() {
		seen[event.Name] = true
	}
	for _, name := range []string{"crm_mail.rollout_updated", "crm_mail.connected", "crm_mail.captured", "crm_mail.synced", "crm_mail.capture_updated"} {
		if !seen[name] {
			t.Error("missing event", name)
		}
	}
}
