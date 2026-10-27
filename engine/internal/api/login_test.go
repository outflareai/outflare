package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
)

// googleSignIn runs "Continue with Google" for c in one browser: start,
// then Google's callback with code, carrying the nonce cookie across. It
// returns where the person lands.
func googleSignIn(t *testing.T, c *client, returnTo, code string) *url.URL {
	t.Helper()
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/auth/google/start?return="+url.QueryEscape(returnTo), nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	to, _ := url.Parse(rec.Header().Get("Location"))
	if to.Query().Get("scope") != "openid email profile" || to.Query().Get("redirect_uri") != "http://localhost:8080/v1/auth/google/callback" {
		t.Fatalf("auth url: %s", to)
	}
	var nonce *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "outflare_login" {
			nonce = ck
		}
	}
	req := httptest.NewRequest("GET", "/v1/auth/google/callback?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(to.Query().Get("state")), nil)
	if nonce != nil {
		req.AddCookie(nonce)
	}
	rec = httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("callback: %d %s", rec.Code, rec.Body)
	}
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "outflare_session" {
			c.cookie = ck
		}
	}
	back, _ := url.Parse(rec.Header().Get("Location"))
	return back
}

func TestGoogleSignIn(t *testing.T) {
	e := freshEnv(t)
	anon := &client{t: t, h: e.h}
	anon.must(404, "GET", "/v1/auth/google/start", nil) // not configured yet
	if m := anon.must(200, "GET", "/v1/meta", nil)["login_methods"].([]any); len(m) != 1 {
		t.Fatalf("login methods: %v", m)
	}
	e.useFakeProvider(t)
	if m := anon.must(200, "GET", "/v1/meta", nil)["login_methods"].([]any); len(m) != 2 || m[1] != "google" {
		t.Fatalf("login methods: %v", m)
	}
	owner, ws := setupOwner(t, e.h)
	if _, err := e.pool.Exec(context.Background(), `UPDATE users SET email_verified_at = now()`); err != nil {
		t.Fatal(err)
	}

	// A Google account with the owner's verified email signs in to them and
	// is linked from then on.
	jack := &client{t: t, h: e.h}
	if back := googleSignIn(t, jack, "/", "login:g-jack:Jack@Example.com"); back.Path != "/" || back.RawQuery != "" {
		t.Fatalf("jack lands: %s", back)
	}
	if me := jack.must(200, "GET", "/v1/me", nil); me["user"].(map[string]any)["email"] != "jack@example.com" {
		t.Fatalf("me: %v", me)
	}

	// Unverified emails and strangers get no session.
	if back := googleSignIn(t, &client{t: t, h: e.h}, "/", "login:g-x:unverified@example.com"); !strings.Contains(back.Query().Get("login_error"), "verified") {
		t.Fatalf("unverified: %s", back)
	}
	stranger := &client{t: t, h: e.h}
	back := googleSignIn(t, stranger, "/", "login:g-ann:ann@example.com")
	if stranger.cookie != nil || back.Query().Get("identity") == "" || back.Query().Get("identity_email") != "ann@example.com" {
		t.Fatalf("stranger: %s", back)
	}
	stranger.must(401, "GET", "/v1/me", nil)

	// A callback without this browser's nonce cookie is refused.
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/auth/google/start", nil))
	to, _ := url.Parse(rec.Header().Get("Location"))
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/auth/google/callback?code=login:g-jack:jack@example.com&state="+url.QueryEscape(to.Query().Get("state")), nil))
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "login_error") || len(rec.Result().Cookies()) > 1 {
		t.Fatalf("no nonce: %s", loc)
	}

	// An invite accepted with the Google sign-in for its email: no password.
	inv := owner.must(201, "POST", "/v1/workspaces/"+ws+"/invites", map[string]string{"email": "ann@example.com", "role": "operator"})
	back = googleSignIn(t, stranger, "/invite#"+inv["token"].(string), "login:g-ann:ann@example.com")
	if back.Path != "/invite" || back.Fragment != inv["token"] || back.Query().Get("identity") == "" {
		t.Fatalf("invite return: %s", back)
	}
	other := googleSignIn(t, &client{t: t, h: e.h}, "/", "login:g-bob:bob@example.com").Query().Get("identity")
	stranger.must(400, "POST", "/v1/invites/accept", map[string]string{"token": inv["token"].(string), "identity": other})
	stranger.must(200, "POST", "/v1/invites/accept", map[string]string{"token": inv["token"].(string), "identity": back.Query().Get("identity")})
	if me := stranger.must(200, "GET", "/v1/me", nil); me["user"].(map[string]any)["name"] != "Signed In" {
		t.Fatalf("ann: %v", me)
	}
	// Ann has no password, so password sign-in never matches; Google does.
	anon.must(401, "POST", "/v1/auth/login", map[string]string{"email": "ann@example.com", "password": ""})
	if googleSignIn(t, &client{t: t, h: e.h}, "/", "login:g-ann:ann@example.com").Query().Get("identity") != "" {
		t.Fatal("ann should now sign in")
	}

	// A signed-in existing user accepts an invite without retyping a password.
	ws2 := owner.must(201, "POST", "/v1/workspaces", map[string]string{"name": "Second"})["id"].(string)
	inv2 := owner.must(201, "POST", "/v1/workspaces/"+ws2+"/invites", map[string]string{"email": "ann@example.com", "role": "viewer"})
	stranger.must(200, "POST", "/v1/invites/accept", map[string]string{"token": inv2["token"].(string)})

	// Hosted builds create accounts straight from a Google sign-in.
	acc := &api.Accounts{Pool: e.pool, PublicURL: "http://localhost:8080", Box: e.oauth.Box}
	cid := googleSignIn(t, &client{t: t, h: e.h}, "/signup?code=x", "login:g-cy:cy@example.com").Query().Get("identity")
	rec = httptest.NewRecorder()
	if _, _, err := acc.CreateAccount(context.Background(), nil, rec, httptest.NewRequest("POST", "/", nil), api.NewAccount{Identity: cid, WorkspaceName: "Cy Co"}); err != nil {
		t.Fatal(err)
	}
	if googleSignIn(t, &client{t: t, h: e.h}, "/", "login:g-cy:cy@example.com").Query().Get("identity") != "" {
		t.Fatal("cy should sign in after sign-up")
	}
	// Someone signs up with another person's address and a password, and
	// waits. When the owner of the address signs in with Google, the
	// squatter's password, sessions and tokens stop working.
	squat := httptest.NewRecorder()
	if _, _, err := acc.CreateAccount(context.Background(), nil, squat, httptest.NewRequest("POST", "/", nil),
		api.NewAccount{Email: "dee@example.com", Name: "Not Dee", Password: "squatter password", WorkspaceName: "Trap"}); err != nil {
		t.Fatal(err)
	}
	squatter := &client{t: t, h: e.h}
	for _, ck := range squat.Result().Cookies() {
		squatter.cookie = ck
	}
	squatter.must(200, "GET", "/v1/me", nil)
	dee := &client{t: t, h: e.h}
	googleSignIn(t, dee, "/", "login:g-dee:dee@example.com")
	if me := dee.must(200, "GET", "/v1/me", nil)["user"].(map[string]any); me["email_verified"] != true || me["has_password"] != false {
		t.Fatalf("dee: %v", me)
	}
	squatter.must(401, "GET", "/v1/me", nil)
	anon.must(401, "POST", "/v1/auth/login", map[string]string{"email": "dee@example.com", "password": "squatter password"})
	// A confirmed account keeps its password when Google is linked later.
	anon.must(200, "POST", "/v1/auth/login", map[string]string{"email": "jack@example.com", "password": "long enough password"})

	var inputErr *api.InputError
	if _, _, err := acc.CreateAccount(context.Background(), nil, httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil), api.NewAccount{Identity: "junk", WorkspaceName: "X"}); err == nil || !asInput(err, &inputErr) {
		t.Fatalf("junk identity: %v", err)
	}
}

func asInput(err error, target **api.InputError) bool {
	e, ok := err.(*api.InputError)
	*target = e
	return ok
}
