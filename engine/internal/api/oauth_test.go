package api_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/oauth"
)

// fakeProvider is Google's token endpoint for "Continue with Google", and
// holds a service account key for tests that connect one.
type fakeProvider struct {
	t   *testing.T
	key *rsa.PrivateKey // the service account's key
}

func newFakeProvider(t *testing.T) (*fakeProvider, *httptest.Server) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeProvider{t: t, key: key}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeProvider) serviceAccount() string {
	der, _ := x509.MarshalPKCS8PrivateKey(f.key)
	b, _ := json.Marshal(map[string]string{
		"type": "service_account", "client_email": "sender@proj.iam.gserviceaccount.com", "client_id": "1234",
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"token_uri":   "https://oauth2.googleapis.com/token",
	})
	return string(b)
}

func idToken(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func (f *fakeProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.ParseForm() //nolint:errcheck
	form := r.PostForm
	reply := func(status int, v map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v) //nolint:errcheck
	}
	if r.URL.Path != "/google/token" || form.Get("grant_type") != "authorization_code" ||
		form.Get("redirect_uri") != "http://localhost:8080/v1/auth/google/callback" {
		reply(400, map[string]any{"error": "unsupported_grant_type"})
		return
	}
	// Sign-in codes are "login:<sub>:<email>"; no refresh token.
	parts := strings.SplitN(form.Get("code"), ":", 3)
	if len(parts) != 3 || parts[0] != "login" || form.Get("client_secret") != "gsecret" {
		reply(400, map[string]any{"error": "invalid_grant"})
		return
	}
	reply(200, map[string]any{"access_token": "ignored", "token_type": "Bearer", "expires_in": 3600,
		"id_token": idToken(map[string]any{"sub": parts[1], "email": parts[2], "email_verified": !strings.HasPrefix(parts[2], "unverified"), "name": "Signed In"})})
}

func (e *env) useFakeProvider(t *testing.T) *fakeProvider {
	f, srv := newFakeProvider(t)
	e.oauth.Endpoints = oauth.Endpoints{GoogleAuth: srv.URL + "/google/auth", GoogleToken: srv.URL + "/google/token"}
	e.oauth.Google = oauth.Client{ID: "gid", Secret: "gsecret"}
	return f
}

// follow sends a GET with c's session and returns the redirect target.
func follow(t *testing.T, c *client, path string) *url.URL {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body)
	}
	u, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return u
}
