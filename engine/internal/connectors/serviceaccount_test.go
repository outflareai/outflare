package connectors

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A Google service account key signs a JWT, trades it for a token once,
// and reuses the token until it nearly expires.
func TestServiceAccountCreds(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		r.ParseForm()
		parts := strings.Split(r.PostForm.Get("assertion"), ".")
		if r.PostForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || len(parts) != 3 {
			w.WriteHeader(400)
			return
		}
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig) != nil {
			w.WriteHeader(401)
			return
		}
		claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var c map[string]any
		json.Unmarshal(claims, &c)
		if c["iss"] != "sa@proj.iam.gserviceaccount.com" || c["scope"] != "https://www.googleapis.com/auth/analytics.readonly" {
			w.WriteHeader(403)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "ya29.sa", "expires_in": 3600})
	}))
	t.Cleanup(srv.Close)
	j, _ := json.Marshal(map[string]string{"type": "service_account", "client_email": "sa@proj.iam.gserviceaccount.com", "private_key": pemKey, "token_uri": srv.URL})
	d := Definition{ID: "ga", OAuth: &OAuth{AuthURL: "https://accounts.google.com/x", Scopes: []string{"openid", "https://www.googleapis.com/auth/analytics.readonly"}}}
	cl := &Client{}
	for i := 0; i < 2; i++ {
		got, err := ServiceAccountCreds(context.Background(), cl, d, Creds{"service_account_json": string(j)})
		if err != nil || got["access_token"] != "ya29.sa" || got["service_account_email"] != "sa@proj.iam.gserviceaccount.com" {
			t.Fatalf("creds: %v %v", got, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("token calls: %d, want 1 (cached)", calls.Load())
	}
	if _, err := ServiceAccountCreds(context.Background(), cl, d, Creds{"service_account_json": `{"type":"authorized_user"}`}); KindOf(err) != ErrInvalid {
		t.Fatalf("not a service account: %v", err)
	}
}

func TestRegistryResolvesOldIDs(t *testing.T) {
	r := NewRegistry([]Connector{fakeConn{Definition{ID: "new", Auth: AuthOAuth2, AuthMethods: Both, Replaces: []string{"old"}}}}, nil)
	d, ok := r.Definition("old")
	if !ok || d.ID != "new" || !d.Supports(AuthAPIKey) || r.Resolve("old") != "new" {
		t.Fatalf("old id: %+v %v", d, ok)
	}
	if _, ok := r.Get("old"); !ok {
		t.Fatal("Get should resolve old ids")
	}
	if got := (Definition{Auth: AuthAPIKey}).normal().AuthMethods; len(got) != 1 || got[0] != AuthAPIKey {
		t.Fatalf("single method: %v", got)
	}
}

type fakeConn struct{ d Definition }

func (f fakeConn) Definition() Definition { return f.d }
func (fakeConn) Test(context.Context, *Client, Creds) (Account, error) {
	return Account{}, nil
}
