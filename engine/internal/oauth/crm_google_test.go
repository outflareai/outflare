package oauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPersonalGoogleConsentBinding(t *testing.T) {
	claims := map[string]any{"sub": "google-test-subject", "email": "employee@northpeak.example", "email_verified": true, "nonce": "test-nonce", "aud": "test-client", "iss": "https://accounts.google.com", "exp": time.Now().Add(time.Hour).Unix()}
	scope := "openid email " + GoogleReadMailScope
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := json.Marshal(claims)
		token := "e30." + base64.RawURLEncoding.EncodeToString(raw) + ".test-signature"
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "test-not-real-access", "refresh_token": "test-not-real-refresh", "id_token": token, "scope": scope})
	}))
	defer server.Close()
	client := &Tokens{Google: Client{ID: "test-client", Secret: "test-not-real-secret"}, Endpoints: Endpoints{GoogleToken: server.URL}, HTTP: server.Client()}
	exchange := func() error {
		_, err := client.ExchangePersonalGoogle(context.Background(), "test-code", "https://app.example/v1/crm-mail/google/callback", "test-nonce", "test-pkce-verifier")
		return err
	}
	if err := exchange(); err != nil {
		t.Fatal(err)
	}
	for key, bad := range map[string]any{"nonce": "other-nonce", "aud": "other-client", "iss": "https://other.example", "email_verified": false, "exp": time.Now().Add(-time.Hour).Unix()} {
		old := claims[key]
		claims[key] = bad
		if err := exchange(); err == nil {
			t.Error("invalid identity accepted", key)
		}
		claims[key] = old
	}
	for _, bad := range []string{"https://mail.google.com/", GoogleReadMailScope + " https://www.googleapis.com/auth/gmail.send", GoogleReadMailScope + " https://www.googleapis.com/auth/gmail.modify", "openid email"} {
		scope = bad
		if err := exchange(); err == nil {
			t.Error("broad or missing mail scope accepted", bad)
		}
	}
	rawURL := client.ReadonlyGoogleURL("https://app.example/callback", "test-state", "test-nonce", "test-verifier", "employee@northpeak.example")
	if strings.Contains(rawURL, "mail.google.com") || !strings.Contains(rawURL, "code_challenge_method=S256") || !strings.Contains(rawURL, "include_granted_scopes=false") {
		t.Fatal("unsafe consent URL")
	}
}
