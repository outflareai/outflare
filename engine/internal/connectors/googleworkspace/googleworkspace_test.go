package googleworkspace

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

func TestDelegatedDomainInspectionAndTenantGuards(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"type": "service_account", "client_email": "service@northpeak.example", "private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), "token_uri": "https://oauth2.googleapis.com/token"})
	cr := c.Creds{"service_account_json": string(raw), "customer_id": "Ctest123", "delegated_admin": "admin@northpeak.example"}
	calls := 0
	responseMode := "ok"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/token" {
			if r.Method != "POST" {
				t.Error("wrong token verb")
			}
			r.ParseForm()
			parts := strings.Split(r.Form.Get("assertion"), ".")
			if len(parts) != 3 {
				t.Fatal("missing assertion")
			}
			b, e := base64.RawURLEncoding.DecodeString(parts[1])
			if e != nil {
				t.Fatal(e)
			}
			var claims map[string]any
			json.Unmarshal(b, &claims)
			if claims["sub"] != "admin@northpeak.example" || claims["scope"] != strings.Join(scopes, " ") {
				t.Errorf("wrong delegation claims")
			}
			w.Write([]byte(`{"access_token":"test-only-not-a-real-token","expires_in":3600}`))
			return
		}
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer test-only-not-a-real-token" {
			t.Error("wrong directory authentication")
		}
		if responseMode == "error" {
			w.WriteHeader(403)
			w.Write([]byte(`{"error":"test-only-private-provider-response"}`))
			return
		}
		if strings.Contains(r.URL.Path, "/customers/") {
			if responseMode == "wrong_customer" {
				w.Write([]byte(`{"id":"Cother"}`))
			} else {
				w.Write([]byte(`{"id":"Ctest123"}`))
			}
			return
		}
		if r.URL.Path != "/admin/directory/v1/customer/Ctest123/domains/northpeak.example" {
			t.Errorf("wrong scoped path %s", r.URL.Path)
		}
		switch responseMode {
		case "missing":
			w.Write([]byte(`{"domainName":"northpeak.example"}`))
		case "unverified":
			w.Write([]byte(`{"domainName":"northpeak.example","verified":false,"isPrimary":false}`))
		default:
			w.Write([]byte(`{"domainName":"northpeak.example","verified":true,"isPrimary":true}`))
		}
	}))
	defer srv.Close()
	cl := &c.Client{BaseURLs: map[string]string{ID: srv.URL, "google.token": srv.URL + "/token"}}
	g := Connector{}
	ctx := context.Background()
	d, err := g.InspectWorkspaceDomain(ctx, cl, cr, "Ctest123", "Northpeak.Example")
	if err != nil || !d.Verified || d.CustomerID != "Ctest123" {
		t.Fatalf("inspection: %+v %v", d, err)
	}
	if calls != 3 {
		t.Fatalf("unexpected calls %d", calls)
	}
	for _, domain := range []string{"bad/../../example", "a..example", "-bad.example", "bad.example?x=1"} {
		before := calls
		if _, err = g.InspectWorkspaceDomain(ctx, cl, cr, "Ctest123", domain); err == nil || calls != before {
			t.Fatalf("invalid domain requested: %s", domain)
		}
	}
	before := calls
	if _, err = g.InspectWorkspaceDomain(ctx, cl, cr, "Cother", "northpeak.example"); err == nil || calls != before {
		t.Fatal("wrong tenant requested")
	}
	responseMode = "wrong_customer"
	before = calls
	if _, err = g.InspectWorkspaceDomain(ctx, cl, cr, "Ctest123", "northpeak.example"); err == nil || calls != before+1 {
		t.Fatal("queried domain under wrong customer")
	}
	responseMode = "missing"
	if _, err = g.InspectWorkspaceDomain(ctx, cl, cr, "Ctest123", "northpeak.example"); err == nil {
		t.Fatal("missing evidence accepted")
	}
	responseMode = "unverified"
	d, err = g.InspectWorkspaceDomain(ctx, cl, cr, "Ctest123", "northpeak.example")
	if err != nil || d.Verified {
		t.Fatal("unverified domain marked verified")
	}
	responseMode = "error"
	_, err = g.InspectWorkspaceDomain(ctx, cl, cr, "Ctest123", "northpeak.example")
	if err == nil || strings.Contains(err.Error(), "test-only-private") {
		t.Fatalf("provider text leaked: %v", err)
	}
}
