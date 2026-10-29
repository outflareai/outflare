package ads

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// appleCreds makes credentials around a P-256 key generated for the test.
func appleCreds(t *testing.T) (c.Creds, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	block := string(pem.EncodeToMemory(&pem.Block{Type: "EC " + "PRIVATE KEY", Bytes: der}))
	return c.Creds{"client_id": "SEARCHADS.test-client", "team_id": "SEARCHADS.test-team", "key_id": "test-key-id", "private_key": block}, key
}

// checkClientSecret verifies the client secret JWT Apple would get.
func checkClientSecret(t *testing.T, jwt string, pub *ecdsa.PublicKey) {
	t.Helper()
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("client secret is not a JWT: %q", jwt)
	}
	var head map[string]string
	var claims map[string]any
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if json.Unmarshal(hb, &head) != nil || json.Unmarshal(cb, &claims) != nil {
		t.Fatalf("unreadable JWT %q %q", hb, cb)
	}
	if head["alg"] != "ES256" || head["kid"] != "test-key-id" {
		t.Fatalf("header %v", head)
	}
	now := float64(time.Now().Unix())
	iat, _ := claims["iat"].(float64)
	exp, _ := claims["exp"].(float64)
	if claims["sub"] != "SEARCHADS.test-client" || claims["iss"] != "SEARCHADS.test-team" || claims["aud"] != "https://appleid.apple.com" ||
		iat > now+5 || iat < now-60 || exp <= now || exp-iat > 180*86400 {
		t.Fatalf("claims %v", claims)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if len(sig) != 64 {
		t.Fatalf("ES256 signature is r||s, 64 bytes; got %d", len(sig))
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("client secret signature does not verify with the public key")
	}
}

func TestAppleAds(t *testing.T) {
	creds, key := appleCreds(t)
	tokens := 0
	var reportBodies []map[string]any
	cl, reqs, _ := fake(t, map[string]any{
		"POST /auth/oauth2/token": func(r *http.Request) any {
			tokens++
			if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
				t.Errorf("token content type %q", r.Header.Get("Content-Type"))
			}
			_ = r.ParseForm()
			if r.PostForm.Get("grant_type") != "client_credentials" || r.PostForm.Get("scope") != "searchadsorg" || r.PostForm.Get("client_id") != "SEARCHADS.test-client" {
				t.Errorf("token form %v", r.PostForm)
			}
			checkClientSecret(t, r.PostForm.Get("client_secret"), &key.PublicKey)
			return map[string]any{"access_token": "apple-tok", "token_type": "Bearer", "expires_in": 3600, "scope": "searchadsorg"}
		},
		"GET /v1/me":            map[string]any{"result": map[string]any{"userId": 3962840, "orgId": 27154130}},
		"GET /v1/orgs/27154130": map[string]any{"result": map[string]any{"id": 27154130, "name": "Northpeak"}},
		"GET /v1/acls": map[string]any{"result": map[string]any{"acls": []any{
			map[string]any{"adAccount": map[string]any{"id": 111, "name": "Northpeak Apps", "orgId": 27154130}, "roles": []any{"API Account Read Only"}},
			map[string]any{"adAccount": map[string]any{"id": 222, "name": "Northpeak Maps", "orgId": 27154130}, "roles": []any{"API Account Read Only"}},
		}}},
		"GET /v1/ad-accounts/111": map[string]any{"result": map[string]any{"id": 111, "name": "Northpeak Apps", "currency": "eur", "timezone": "Europe/Berlin", "productFeatures": []any{"APPSTORE_APP_MANUAL"}}},
		"GET /v1/ad-accounts/222": map[string]any{"result": map[string]any{"id": 222, "name": "Northpeak Maps", "currency": "USD", "timezone": "America/New_York", "productFeatures": []any{"BUSINESS_BRAND_MANUAL"}}},
		"POST /v1/campaigns/query": func(r *http.Request) any {
			var q struct {
				Pagination struct {
					Offset int `json:"offset"`
				} `json:"pagination"`
			}
			_ = json.NewDecoder(r.Body).Decode(&q)
			if q.Pagination.Offset == 0 {
				return map[string]any{"result": []any{map[string]any{"id": 501, "name": "Brand", "status": "ENABLED", "displayStatus": "RUNNING", "deleted": false,
					"dailyBudget": map[string]any{"value": map[string]any{"amount": "100.00", "currency": "EUR"}}, "bidStrategy": map[string]any{"bidStrategyType": "MAX_CONVERSIONS", "bidStrategyGoal": "INSTALL"}}},
					"pagination": map[string]any{"offset": 0, "pageSize": 1, "totalCount": 2}}
			}
			return map[string]any{"result": []any{map[string]any{"id": 502, "name": "Old", "status": "PAUSED", "displayStatus": "PAUSED"}},
				"pagination": map[string]any{"offset": 1, "pageSize": 1, "totalCount": 2}}
		},
		"POST /v1/reports/apps/campaigns/query": func(r *http.Request) any {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			reportBodies = append(reportBodies, body)
			tr := body["timeRange"].(map[string]any)
			meta := map[string]any{"id": 501, "name": "Brand"}
			if tr["granularity"] == nil {
				return map[string]any{"result": map[string]any{"rows": []any{map[string]any{"metadata": meta,
					"totalMetrics": map[string]any{"localSpend": map[string]any{"amount": "2.50", "currency": "EUR"}, "impressions": 40, "taps": 4, "totalInstalls": 1}}}},
					"pagination": map[string]any{"offset": 0, "pageSize": 1, "totalCount": 1}}
			}
			return map[string]any{"result": map[string]any{"rows": []any{map[string]any{"metadata": meta,
				"totalMetrics": map[string]any{"localSpend": map[string]any{"amount": "18.40", "currency": "EUR"}},
				"granularMetrics": []any{
					map[string]any{"date": "2026-07-01", "localSpend": map[string]any{"amount": "18.40", "currency": "EUR"}, "impressions": 1850, "taps": 92, "tapInstalls": 22, "totalInstalls": 25},
					map[string]any{"date": "2026-07-02", "localSpend": map[string]any{"amount": "0", "currency": "EUR"}, "impressions": 0, "taps": 0},
				}}}}, "pagination": map[string]any{"offset": 0, "pageSize": 1, "totalCount": 1}}
		},
		"POST /v1/reports/business-brands/campaigns/query": map[string]any{"result": map[string]any{"rows": []any{map[string]any{"metadata": map[string]any{"id": 601, "name": "Stores"},
			"granularMetrics": []any{map[string]any{"date": "2026-07-01", "localSpend": map[string]any{"amount": "3.00", "currency": "USD"}, "impressions": 300, "taps": 9}}}}}},
	})
	ap := AppleAds{}
	acct0, err := ap.Test(ctx, cl, creds)
	if err != nil || acct0.ID != "27154130" || acct0.Name != "Northpeak" || acct0.Details["accounts"] != "2" {
		t.Fatalf("test %+v %v", acct0, err)
	}
	accts, err := ap.AdAccounts(ctx, cl, creds)
	if err != nil || len(accts) != 2 || accts[0].ID != "111" || accts[0].Currency != "EUR" || accts[0].TimeZone != "Europe/Berlin" ||
		accts[0].Meta["reports"] != "apps" || accts[0].Meta["org_id"] != "27154130" || accts[1].Meta["reports"] != "business-brands" {
		t.Fatalf("accounts %+v %v", accts, err)
	}
	camps, err := ap.AdCampaigns(ctx, cl, creds, accts[0])
	if err != nil || len(camps) != 2 || camps[0].ID != "501" || camps[0].Status != c.AdActive || camps[0].ProviderStatus != "RUNNING" ||
		*camps[0].DailyBudgetMicros != 100_000_000 || camps[0].Objective != "install" || camps[1].Status != c.AdPaused {
		t.Fatalf("campaigns %+v %v", camps, err)
	}

	// 91 days: a DAILY window of 90, then the last day alone, whose
	// numbers come in the row's totals.
	days, err := ap.AdDays(ctx, cl, creds, accts[0], "2026-07-01", "2026-09-29")
	if err != nil || len(days) != 2 {
		t.Fatalf("days %+v %v", days, err)
	}
	if d := days[0]; d.CampaignID != "501" || d.CampaignName != "Brand" || d.Date != "2026-07-01" || d.SpendMicros != 18_400_000 || d.Impressions != 1850 || d.Clicks != 92 || *d.Conversions != 25 {
		t.Fatalf("day %+v", d)
	}
	if d := days[1]; d.Date != "2026-09-29" || d.SpendMicros != 2_500_000 || d.Clicks != 4 || *d.Conversions != 1 {
		t.Fatalf("single day %+v", d)
	}
	if len(reportBodies) != 2 {
		t.Fatalf("report calls %d", len(reportBodies))
	}
	w0 := reportBodies[0]["timeRange"].(map[string]any)
	w1 := reportBodies[1]["timeRange"].(map[string]any)
	if w0["start"] != "2026-07-01" || w0["end"] != "2026-09-28" || w0["granularity"] != "DAILY" || w0["timeZone"] != "ORTZ" ||
		w1["start"] != "2026-09-29" || w1["end"] != "2026-09-29" || w1["granularity"] != nil {
		t.Fatalf("report windows %v %v", w0, w1)
	}

	// Apple Maps accounts read the brands report and have no installs.
	maps, err := ap.AdDays(ctx, cl, creds, accts[1], "2026-07-01", "2026-07-02")
	if err != nil || len(maps) != 1 || maps[0].SpendMicros != 3_000_000 || maps[0].Clicks != 9 || maps[0].Conversions != nil {
		t.Fatalf("maps days %+v %v", maps, err)
	}

	// One token for the whole run, sent on every call; the account goes
	// in X-AP-Context except on the calls Apple scopes to the org.
	if tokens != 1 {
		t.Fatalf("token requested %d times, want 1 (cached)", tokens)
	}
	for _, r := range *reqs {
		if r.path == "/auth/oauth2/token" {
			continue
		}
		if r.header.Get("Authorization") != "Bearer apple-tok" {
			t.Fatalf("%s without the token", r.path)
		}
		ctxh := r.header.Get("X-AP-Context")
		switch {
		case r.path == "/v1/me" || r.path == "/v1/acls" || strings.HasPrefix(r.path, "/v1/orgs/"):
			if ctxh != "" {
				t.Fatalf("%s should not carry X-AP-Context: %q", r.path, ctxh)
			}
		case r.path == "/v1/ad-accounts/222" || strings.Contains(r.path, "business-brands"):
			if ctxh != "adAccountId=222" {
				t.Fatalf("%s context %q", r.path, ctxh)
			}
		default:
			if ctxh != "adAccountId=111" {
				t.Fatalf("%s context %q", r.path, ctxh)
			}
		}
	}
}

func TestAppleAdsKeys(t *testing.T) {
	creds, key := appleCreds(t)
	// A single-line field drops the line breaks.
	oneLine := strings.ReplaceAll(creds["private_key"], "\n", "")
	if k, err := appleKey(oneLine); err != nil || !k.Equal(key) {
		t.Fatalf("one-line key: %v", err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	pkcs8 := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE " + "KEY", Bytes: der}))
	if k, err := appleKey(pkcs8); err != nil || !k.Equal(key) {
		t.Fatalf("PKCS #8 key: %v", err)
	}
	// Armour around bytes that are not a key, built at run time.
	notDER := string(pem.EncodeToMemory(&pem.Block{Type: "EC " + "PRIVATE KEY", Bytes: []byte{0, 0, 0}}))
	for _, bad := range []string{"", "not a key", notDER} {
		if _, err := appleKey(bad); c.KindOf(err) != c.ErrInvalid {
			t.Fatalf("%q: want invalid, got %v", bad, err)
		}
	}
	other, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	der, _ = x509.MarshalECPrivateKey(other)
	if _, err := appleKey(string(pem.EncodeToMemory(&pem.Block{Type: "EC " + "PRIVATE KEY", Bytes: der}))); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("P-384 key: want invalid, got %v", err)
	}
	if _, err := (AppleAds{}).AdAccounts(ctx, &c.Client{}, c.Creds{"client_id": "x"}); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("missing fields: want invalid, got %v", err)
	}
}

func TestAppleAdsErrors(t *testing.T) {
	serve := func(t *testing.T, token, api func(w http.ResponseWriter)) *c.Client {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/auth/oauth2/token" {
				token(w)
				return
			}
			api(w)
		}))
		t.Cleanup(srv.Close)
		return &c.Client{BaseURLs: map[string]string{"apple_ads": srv.URL, "apple_ads.token": srv.URL}}
	}
	ok := func(w http.ResponseWriter) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "t", "expires_in": 3600})
	}
	status := func(code int, body string) func(http.ResponseWriter) {
		return func(w http.ResponseWriter) { w.WriteHeader(code); _, _ = w.Write([]byte(body)) }
	}
	cases := []struct {
		name       string
		token, api func(http.ResponseWriter)
		want       string
	}{
		{"wrong credentials", status(400, `{"error":"invalid_client"}`), ok, c.ErrAuth},
		{"apple down", status(503, ""), ok, c.ErrUnavailable},
		{"token revoked", ok, status(401, `{"error":{"code":"UNAUTHORIZED"}}`), c.ErrAuth},
		{"role too low", ok, status(403, `{"error":{"code":"FORBIDDEN"}}`), c.ErrAuth},
		{"rate limited", ok, status(429, `{"error":{"code":"RATE_LIMIT_EXCEEDED"}}`), c.ErrQuota},
		{"error in the envelope", ok, status(200, `{"result":null,"error":{"code":"VALIDATION_ERROR","message":"Validation errors found"}}`), c.ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			creds, _ := appleCreds(t) // a new key each time, so no cached token
			_, err := AppleAds{}.AdAccounts(ctx, serve(t, tc.token, tc.api), creds)
			if c.KindOf(err) != tc.want {
				t.Fatalf("want %s, got %v", tc.want, err)
			}
		})
	}
}

// The token call carries no credential in the URL.
func TestAppleAdsTokenInBody(t *testing.T) {
	creds, _ := appleCreds(t)
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "t", "expires_in": 3600})
	}))
	defer srv.Close()
	if _, err := (AppleAds{}).token(ctx, &c.Client{BaseURLs: map[string]string{"apple_ads.token": srv.URL}}, creds); err != nil {
		t.Fatal(err)
	}
	if v, _ := url.ParseQuery(query); v.Get("client_secret") != "" {
		t.Fatalf("client secret in the URL: %q", query)
	}
}
