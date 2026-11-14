package oauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSalesGoogleRequiresExplicitReadAndSendConsent(t *testing.T) {
	scope := "openid email " + GoogleReadMailScope
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := json.Marshal(map[string]any{"sub": "test-subject", "email": "rep@acme.example", "email_verified": true, "nonce": "test-nonce", "aud": "test-client", "iss": "https://accounts.google.com", "exp": time.Now().Add(time.Hour).Unix()})
		json.NewEncoder(w).Encode(map[string]any{"access_token": "test-not-real-access", "refresh_token": "test-not-real-refresh", "scope": scope, "id_token": "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".test"})
	}))
	defer fake.Close()
	c := &Tokens{Google: Client{ID: "test-client", Secret: "test-not-real-secret"}, Endpoints: Endpoints{GoogleToken: fake.URL, GoogleAuth: fake.URL + "/authorize"}, HTTP: fake.Client()}
	exchange := func() error {
		_, err := c.ExchangeSalesGoogle(context.Background(), "test-code", "https://app.example/callback", "test-nonce", "test-verifier")
		return err
	}
	if exchange() == nil {
		t.Fatal("read-only grant authorized sending")
	}
	scope += " " + GoogleSendMailScope
	if err := exchange(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ExchangePersonalGoogle(context.Background(), "test-code", "https://app.example/callback", "test-nonce", "test-verifier"); err == nil {
		t.Fatal("Sales grant accepted by read-only capture")
	}
	scope += " https://www.googleapis.com/auth/gmail.modify"
	if exchange() == nil {
		t.Fatal("unrequested modify grant accepted")
	}
	u, _ := url.Parse(c.SalesGoogleURL("https://app.example/callback", "test-state", "test-nonce", "test-verifier", "rep@acme.example"))
	if !strings.Contains(u.Query().Get("scope"), GoogleSendMailScope) || u.Query().Get("include_granted_scopes") != "false" || u.Query().Get("code_challenge_method") != "S256" {
		t.Fatal("consent URL is not explicit and bound")
	}
}
