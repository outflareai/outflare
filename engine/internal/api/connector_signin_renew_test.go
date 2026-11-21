package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/track"
)

// A Google access token refused before it was due to expire renews once
// with the refresh token instead of marking the connection broken; when
// the refresh token is dead too, the connection says the sign-in expired
// (not "refused the API key"), and Sign in again mends it even though its
// old tokens can't be renewed.
func TestRefusedSignInRenewsBeforeBreaking(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	events := track.Capture(t)
	has := func(name string) bool {
		for _, ev := range events() {
			if ev.Name == name {
				return true
			}
		}
		return false
	}

	var mu sync.Mutex
	valid := map[string]bool{}
	refreshWorks := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/" && r.Method == "POST":
			r.ParseForm()
			var tok string
			switch r.Form.Get("grant_type") {
			case "authorization_code":
				tok = map[string]string{"abc": "gsc_first", "def": "gsc_again"}[r.Form.Get("code")]
			case "refresh_token":
				if refreshWorks {
					tok = "gsc_renewed"
				}
			}
			if tok == "" {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant", "error_description": "Token has been expired or revoked."})
				return
			}
			valid[tok] = true
			json.NewEncoder(w).Encode(map[string]any{"access_token": tok, "refresh_token": tok + "_r", "expires_in": 3600})
		case !valid[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]:
			w.WriteHeader(401)
		case r.URL.Path == "/v1/userinfo":
			json.NewEncoder(w).Encode(map[string]any{"sub": "7", "email": "ana@acme.example", "name": "Ana"})
		case r.URL.Path == "/webmasters/v3/sites":
			json.NewEncoder(w).Encode(map[string]any{"siteEntry": []map[string]any{{"siteUrl": "sc-domain:acme.example", "permissionLevel": "siteOwner"}}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	for _, k := range []string{"google_search_console", "google_search_console.token", "google.userinfo"} {
		e.integrations.Client.BaseURLs[k] = srv.URL
	}
	e.integrations.OAuthApps = map[string]connectors.OAuthApp{"google_search_console": {ClientID: "gsc-id", ClientSecret: "gsc-secret"}}

	signIn := func(code, query string) url.Values {
		t.Helper()
		link := owner.must(200, "GET", base+"/integrations/oauth/google_search_console/authorize?format=json"+query, nil)["url"].(string)
		u, _ := url.Parse(link)
		return follow(t, owner, "/v1/integrations/oauth/callback?code="+code+"&state="+url.QueryEscape(u.Query().Get("state"))).Query()
	}
	id := signIn("abc", "").Get("integration")
	if id == "" {
		t.Fatal("no connection")
	}
	revoke := func() {
		mu.Lock()
		defer mu.Unlock()
		valid = map[string]bool{}
	}

	// Google stops honouring the access token an hour early: a check renews
	// it and the connection stays connected.
	revoke()
	got := owner.must(200, "POST", base+"/integrations/"+id+"/test", nil)
	if got["status"] != "connected" || got["last_error"] != nil {
		t.Fatalf("after a renewable refusal: %v %v", got["status"], got["last_error"])
	}
	o, err := e.integrations.Open(context.Background(), e.pool, ws, id)
	if err != nil || o.Creds["access_token"] != "gsc_renewed" {
		t.Fatalf("tokens after renewing: %v", err)
	}
	if !has("integration.sign_in_renewed") {
		t.Fatal("no integration.sign_in_renewed event")
	}

	// The refresh token is dead too (an app in testing, a revoked grant):
	// broken, with words that say what to do.
	revoke()
	mu.Lock()
	refreshWorks = false
	mu.Unlock()
	got = owner.must(200, "POST", base+"/integrations/"+id+"/test", nil)
	msg, _ := got["last_error"].(string)
	if got["status"] != "error" || !strings.Contains(msg, "expired or was revoked") || strings.Contains(msg, "API key") {
		t.Fatalf("after a dead refresh token: %v %q", got["status"], msg)
	}
	if !has("integration.sign_in_expired") {
		t.Fatal("no integration.sign_in_expired event")
	}

	// Sign in again mends it, though its old tokens can't be renewed.
	again := signIn("def", "&integration="+id)
	if again.Get("integration") != id || again.Get("signed_in") != "1" {
		t.Fatalf("sign in again: %v", again)
	}
	got = owner.must(200, "GET", base+"/integrations/"+id, nil)
	if got["status"] != "connected" {
		t.Fatalf("after signing in again: %v", got["status"])
	}
}

// Signing in to an account that has two connections already (an older
// "… 2" copy) renews both, so neither stays broken.
func TestSignInRenewsEveryCopyOfTheAccount(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/" && r.Method == "POST":
			r.ParseForm()
			tok := map[string]string{"abc": "gsc_first", "def": "gsc_second"}[r.Form.Get("code")]
			json.NewEncoder(w).Encode(map[string]any{"access_token": tok, "refresh_token": tok + "_r", "expires_in": 3600})
		case r.URL.Path == "/v1/userinfo":
			json.NewEncoder(w).Encode(map[string]any{"sub": "7", "email": "ana@acme.example", "name": "Ana"})
		case r.URL.Path == "/webmasters/v3/sites":
			json.NewEncoder(w).Encode(map[string]any{"siteEntry": []map[string]any{}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	for _, k := range []string{"google_search_console", "google_search_console.token", "google.userinfo"} {
		e.integrations.Client.BaseURLs[k] = srv.URL
	}
	e.integrations.OAuthApps = map[string]connectors.OAuthApp{"google_search_console": {ClientID: "gsc-id", ClientSecret: "gsc-secret"}}
	signIn := func(code string) url.Values {
		t.Helper()
		link := owner.must(200, "GET", base+"/integrations/oauth/google_search_console/authorize?format=json", nil)["url"].(string)
		u, _ := url.Parse(link)
		return follow(t, owner, "/v1/integrations/oauth/callback?code="+code+"&state="+url.QueryEscape(u.Query().Get("state"))).Query()
	}
	first := signIn("abc").Get("integration")
	ctx := context.Background()
	// A copy made before sign-ins renewed the same account, now broken.
	var copyID string
	if err := e.pool.QueryRow(ctx, `INSERT INTO integrations (workspace_id, connector, auth_method, name, status, account, config, credentials, last_error)
		SELECT workspace_id, connector, auth_method, name || ' 2', 'error', account, config, '\x00'::bytea, 'refused' FROM integrations WHERE id = $1 RETURNING id::text`, first).Scan(&copyID); err != nil {
		t.Fatal(err)
	}
	if err := e.integrations.UpdateCreds(ctx, e.pool, ws, copyID, connectors.Creds{"access_token": "dead", "refresh_token": "dead_r"}); err != nil {
		t.Fatal(err)
	}
	if got := signIn("def"); got.Get("renewed") != "true" {
		t.Fatalf("second sign-in: %v", got)
	}
	for _, id := range []string{first, copyID} {
		o, err := e.integrations.Open(ctx, e.pool, ws, id)
		if err != nil || o.Creds["access_token"] != "gsc_second" || o.Status != "connected" {
			t.Fatalf("%s after signing in: %v", id, err)
		}
	}
}
