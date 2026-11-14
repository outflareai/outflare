package googleworkspace

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// All values are fictional. Nothing here calls Google.
const (
	testSA     = "workspace-reader@northpeak-demo.iam.gserviceaccount.com"
	testAdmin  = "admin@northpeak.example"
	testClient = "test-only-client.apps.googleusercontent.com"
	testSecret = "test-only-not-a-real-secret"
	testSigner = "ops@northpeak.example"
	privateTxt = "test-only-private-provider-text"
)

// fakeGoogle stands in for Google's token endpoint, IAM Credentials and the
// Directory API. Modes make each stage fail the way Google does.
type fakeGoogle struct {
	t   *testing.T
	srv *httptest.Server
	mu  sync.Mutex

	refreshes, signs, exchanges, reads atomic.Int32
	revoked                            atomic.Int32
	// dirMode makes Directory reads fail: "403" or "401".
	dirMode string
	issued  atomic.Int32

	signerMode, signMode, exchangeMode, codeMode string
	// blockSign makes signJwt wait until the request is cancelled.
	blockSign bool
	// lastClaims are the claims of the last assertion signed.
	lastClaims map[string]any
	// lastCodeForm is the last authorization_code request.
	lastCodeForm url.Values
}

func idToken(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"RS256"}`)) + "." + enc(b) + ".test-only-signature"
}

func newFake(t *testing.T) *fakeGoogle {
	f := &fakeGoogle{t: t}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGoogle) client() *c.Client {
	return &c.Client{BaseURLs: map[string]string{
		ID: f.srv.URL, ID + ".token": f.srv.URL + "/token", ID + ".iam": f.srv.URL + "/iam", ID + ".accounts": f.srv.URL + "/auth", ID + ".revoke": f.srv.URL + "/revoke",
	}}
}

func (f *fakeGoogle) serve(w http.ResponseWriter, r *http.Request) {
	t := f.t
	switch {
	case r.URL.Path == "/revoke":
		_ = r.ParseForm()
		if strings.HasPrefix(r.Form.Get("token"), "test-only-refresh") {
			f.revoked.Add(1)
		}
	case r.URL.Path == "/token":
		if r.Method != http.MethodPost {
			t.Error("token endpoint: wrong verb")
		}
		_ = r.ParseForm()
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			f.mu.Lock()
			f.lastCodeForm = r.Form
			f.mu.Unlock()
			if r.Form.Get("client_id") != testClient || r.Form.Get("client_secret") != testSecret {
				t.Error("sign-in used a client other than the workspace's own")
			}
			reply := map[string]any{"access_token": "test-only-signer-token", "refresh_token": "test-only-refresh-new", "expires_in": 3600,
				"scope": "openid https://www.googleapis.com/auth/userinfo.email " + iamScope, "id_token": idToken(map[string]any{"email": testSigner, "email_verified": true, "iss": "https://accounts.google.com", "aud": testClient})}
			switch f.codeMode {
			case "no_scope":
				reply["scope"] = "openid email"
			case "unverified":
				reply["id_token"] = idToken(map[string]any{"email": testSigner, "email_verified": false, "iss": "https://accounts.google.com", "aud": testClient})
			case "other_audience":
				reply["id_token"] = idToken(map[string]any{"email": testSigner, "email_verified": true, "iss": "https://accounts.google.com", "aud": "another-client.apps.googleusercontent.com"})
			case "no_refresh":
				delete(reply, "refresh_token")
			case "refused":
				w.WriteHeader(400)
				fmt.Fprintf(w, `{"error":"invalid_grant","error_description":%q}`, privateTxt)
				return
			}
			_ = json.NewEncoder(w).Encode(reply)
		case "refresh_token":
			f.refreshes.Add(1)
			if r.Form.Get("client_id") != testClient || r.Form.Get("client_secret") != testSecret {
				t.Error("renewal used a client other than the workspace's own")
			}
			switch f.signerMode {
			case "revoked":
				w.WriteHeader(400)
				fmt.Fprintf(w, `{"error":"invalid_grant","error_description":%q}`, privateTxt)
				return
			case "bad_client":
				w.WriteHeader(401)
				fmt.Fprintf(w, `{"error":"invalid_client","error_description":%q}`, privateTxt)
				return
			}
			fmt.Fprintf(w, `{"access_token":"test-only-signer-token-%s","expires_in":3600,"scope":%q}`, r.Form.Get("refresh_token"), iamScope)
		case "urn:ietf:params:oauth:grant-type:jwt-bearer":
			f.exchanges.Add(1)
			if !strings.HasPrefix(r.Form.Get("assertion"), "test-only.signed.") {
				t.Error("exchanged something other than the signed assertion")
			}
			if f.exchangeMode == "denied" {
				w.WriteHeader(401)
				fmt.Fprintf(w, `{"error":"unauthorized_client","error_description":%q}`, privateTxt)
				return
			}
			n := f.issued.Add(1)
			fmt.Fprintf(w, `{"access_token":"test-only-delegated-%d","expires_in":3600}`, n)
		default:
			t.Errorf("unexpected grant %q", r.Form.Get("grant_type"))
		}
	case strings.HasPrefix(r.URL.Path, "/iam/v1/projects/-/serviceAccounts/"):
		f.signs.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/iam/v1/projects/-/serviceAccounts/"+testSA+":signJwt" {
			t.Errorf("wrong signJwt call %s %s", r.Method, r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer test-only-signer-token") {
			t.Error("signJwt not called with the connection's signer token")
		}
		// Read the body first: the server notices a client hanging up only
		// once the body is consumed.
		raw, _ := io.ReadAll(r.Body)
		if f.blockSign {
			select {
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
				t.Error("signJwt request was never cancelled")
			}
			return
		}
		var body struct {
			Payload string `json:"payload"`
		}
		if json.Unmarshal(raw, &body) != nil {
			t.Fatal("unreadable signJwt body")
		}
		var claims map[string]any
		if err := json.Unmarshal([]byte(body.Payload), &claims); err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		f.lastClaims = claims
		f.mu.Unlock()
		switch f.signMode {
		case "denied":
			w.WriteHeader(403)
			fmt.Fprintf(w, `{"error":{"code":403,"status":"PERMISSION_DENIED","message":%q}}`, privateTxt)
			return
		case "disabled":
			w.WriteHeader(403)
			fmt.Fprintf(w, `{"error":{"code":403,"status":"PERMISSION_DENIED","message":%q,"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"SERVICE_DISABLED"}]}}`, privateTxt)
			return
		case "unauthorized":
			w.WriteHeader(401)
			fmt.Fprintf(w, `{"error":{"code":401,"status":"UNAUTHENTICATED","message":%q}}`, privateTxt)
			return
		case "missing":
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"error":{"code":404,"status":"NOT_FOUND","message":%q}}`, privateTxt)
			return
		}
		fmt.Fprintf(w, `{"keyId":"test-only-key","signedJwt":"test-only.signed.%d"}`, f.signs.Load())
	case strings.HasPrefix(r.URL.Path, "/admin/directory/v1/"):
		f.reads.Add(1)
		if r.Method != http.MethodGet || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer test-only-delegated-") {
			t.Error("directory read without a delegated token")
		}
		switch f.dirMode {
		case "403":
			w.WriteHeader(403)
			return
		case "401":
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/admin/directory/v1/customers/Ctest123":
			w.Write([]byte(`{"id":"Ctest123"}`))
		case "/admin/directory/v1/customer/Ctest123/domains/northpeak.example":
			w.Write([]byte(`{"domainName":"northpeak.example","verified":true,"isPrimary":true}`))
		default:
			t.Errorf("unexpected directory path %s", r.URL.Path)
		}
	default:
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
	}
}

// keylessCreds are a signed-in keyless connection's credentials.
func keylessCreds(refresh string) c.Creds {
	return c.Creds{"delegated_admin": testAdmin, "customer_id": "Ctest123", "service_account_email": testSA,
		"oauth_client_id": testClient, "oauth_client_secret": testSecret, "refresh_token": refresh, "signer_email": testSigner}
}

func resetCache(t *testing.T) {
	t.Helper()
	tokens.Range(func(k, _ any) bool { tokens.Delete(k); return true })
	old := now
	t.Cleanup(func() {
		now = old
		tokens.Range(func(k, _ any) bool { tokens.Delete(k); return true })
	})
}

func codeOf(err error) string {
	var ce *c.CodedError
	if errors.As(err, &ce) {
		return ce.Code()
	}
	return ""
}

func TestKeylessSignInSignsExchangesAndReads(t *testing.T) {
	resetCache(t)
	f := newFake(t)
	cl := f.client()
	g := Connector{}
	saved := keylessCreds("")
	delete(saved, "refresh_token")
	delete(saved, "signer_email")

	link := g.SignInURLPKCE(cl, saved, "https://outflare.example/v1/integrations/oauth/callback", "state-1", "verifier-1")
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("client_id") != testClient || q.Get("code_challenge") != c.PKCEChallenge("verifier-1") || q.Get("access_type") != "offline" ||
		q.Get("scope") != "openid email "+iamScope || q.Get("state") != "state-1" {
		t.Fatalf("sign-in link: %s", link)
	}

	creds, acct, err := g.SignInPKCE(context.Background(), cl, saved, "test-only-code", "https://outflare.example/v1/integrations/oauth/callback", "verifier-1")
	if err != nil {
		t.Fatalf("sign-in: %v", err)
	}
	if f.lastCodeForm.Get("code_verifier") != "verifier-1" {
		t.Fatal("PKCE verifier not sent")
	}
	if acct.ID != "Ctest123" || acct.Email != testSigner || creds["refresh_token"] != "test-only-refresh-new" {
		t.Fatalf("account %+v creds %v", acct, creds)
	}
	for _, k := range []string{"access_token", "expires_at", "id_token", "service_account_json"} {
		if creds[k] != "" {
			t.Fatalf("stored %s", k)
		}
	}
	// The sign-in itself proved the path: one sign, one exchange, one read.
	if f.signs.Load() != 1 || f.exchanges.Load() != 1 || f.reads.Load() != 1 || f.refreshes.Load() != 0 {
		t.Fatalf("sign-in calls: sign %d exchange %d read %d refresh %d", f.signs.Load(), f.exchanges.Load(), f.reads.Load(), f.refreshes.Load())
	}
	claims := f.lastClaims
	iat, _ := claims["iat"].(float64)
	exp, _ := claims["exp"].(float64)
	if claims["iss"] != testSA || claims["sub"] != testAdmin || claims["aud"] != "https://oauth2.googleapis.com/token" || exp-iat != 300 ||
		claims["scope"] != "https://www.googleapis.com/auth/admin.directory.customer.readonly https://www.googleapis.com/auth/admin.directory.domain.readonly" {
		t.Fatalf("assertion claims %v", claims)
	}

	d, err := g.InspectWorkspaceDomain(context.Background(), cl, creds, "Ctest123", "northpeak.example")
	if err != nil || !d.Verified || !d.Primary || d.Domain != "northpeak.example" {
		t.Fatalf("inspect: %+v %v", d, err)
	}
	// The delegated token is reused: no new signing.
	if f.signs.Load() != 1 || f.exchanges.Load() != 1 {
		t.Fatal("delegated token not reused")
	}
	a, err := g.Test(context.Background(), cl, creds)
	if err != nil || a.ID != "Ctest123" || a.Details["signer"] != "signed_in" {
		t.Fatalf("test: %+v %v", a, err)
	}
}

func TestKeylessBeforeSignInNeedsSigner(t *testing.T) {
	resetCache(t)
	f := newFake(t)
	cl := f.client()
	cr := keylessCreds("")
	a, err := Connector{}.Test(context.Background(), cl, cr)
	if err != nil || a.ID != "" || a.Details["signer"] != "sign_in_required" {
		t.Fatalf("saved before sign-in: %+v %v", a, err)
	}
	_, err = Connector{}.InspectWorkspaceDomain(context.Background(), cl, cr, "Ctest123", "northpeak.example")
	if codeOf(err) != c.CodeWorkspaceSignerNeeded || c.KindOf(err) != c.ErrAuth {
		t.Fatalf("want signer needed, got %v (%s)", err, codeOf(err))
	}
	if n := f.refreshes.Load() + f.signs.Load() + f.exchanges.Load() + f.reads.Load(); n != 0 {
		t.Fatalf("called Google %d times without a sign-in", n)
	}
}

func TestKeylessFailuresAreCodedAndRedacted(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*fakeGoogle)
		code  string
		kind  string
	}{
		{"signer revoked", func(f *fakeGoogle) { f.signerMode = "revoked" }, c.CodeWorkspaceSignerNeeded, c.ErrAuth},
		{"own client refused", func(f *fakeGoogle) { f.signerMode = "bad_client" }, c.CodeWorkspaceSignerNeeded, c.ErrAuth},
		{"missing signJwt permission", func(f *fakeGoogle) { f.signMode = "denied" }, c.CodeWorkspaceSignerDenied, c.ErrAuth},
		{"credentials API off", func(f *fakeGoogle) { f.signMode = "disabled" }, c.CodeWorkspaceSignerDenied, c.ErrAuth},
		{"service account missing", func(f *fakeGoogle) { f.signMode = "missing" }, c.CodeWorkspaceSignerDenied, c.ErrAuth},
		{"delegation refused", func(f *fakeGoogle) { f.exchangeMode = "denied" }, c.CodeWorkspaceDelegationDenied, c.ErrAuth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetCache(t)
			f := newFake(t)
			tc.setup(f)
			cr := keylessCreds("test-only-refresh-a")
			_, err := Connector{}.InspectWorkspaceDomain(context.Background(), f.client(), cr, "Ctest123", "northpeak.example")
			if err == nil {
				t.Fatal("no error")
			}
			if codeOf(err) != tc.code || c.KindOf(err) != tc.kind {
				t.Fatalf("got %q/%q: %v", codeOf(err), c.KindOf(err), err)
			}
			for _, leak := range []string{privateTxt, "test-only-signer-token", "test-only-delegated", "test-only.signed", "test-only-refresh", testSecret} {
				if strings.Contains(err.Error(), leak) {
					t.Fatalf("error leaks %q: %v", leak, err)
				}
			}
			if f.reads.Load() != 0 {
				t.Fatal("read the directory without a delegated token")
			}
			// A failed stage caches no delegated token.
			f.signerMode, f.signMode, f.exchangeMode = "", "", ""
			if _, err := (Connector{}).InspectWorkspaceDomain(context.Background(), f.client(), cr, "Ctest123", "northpeak.example"); err != nil {
				t.Fatalf("after fixing: %v", err)
			}
		})
	}
	if s := keylessCreds("test-only-refresh-a").String(); strings.Contains(s, testSecret) || strings.Contains(s, "test-only-refresh") {
		t.Fatalf("creds print secrets: %s", s)
	}
}

func TestKeylessTokenExpiry(t *testing.T) {
	resetCache(t)
	f := newFake(t)
	cl := f.client()
	cr := keylessCreds("test-only-refresh-a")
	base := time.Now()
	now = func() time.Time { return base }
	inspect := func() {
		t.Helper()
		if _, err := (Connector{}).InspectWorkspaceDomain(context.Background(), cl, cr, "Ctest123", "northpeak.example"); err != nil {
			t.Fatal(err)
		}
	}
	inspect()
	inspect()
	if f.refreshes.Load() != 1 || f.signs.Load() != 1 || f.exchanges.Load() != 1 {
		t.Fatalf("first hour: refresh %d sign %d exchange %d", f.refreshes.Load(), f.signs.Load(), f.exchanges.Load())
	}
	// 54 minutes on, the hour-long tokens still have six minutes: reused.
	now = func() time.Time { return base.Add(54 * time.Minute) }
	inspect()
	if f.signs.Load() != 1 {
		t.Fatal("renewed a token with six minutes left")
	}
	// Past 55 minutes they have under five minutes left: renewed.
	now = func() time.Time { return base.Add(55*time.Minute + time.Second) }
	inspect()
	if f.refreshes.Load() != 2 || f.signs.Load() != 2 || f.exchanges.Load() != 2 {
		t.Fatalf("after expiry: refresh %d sign %d exchange %d", f.refreshes.Load(), f.signs.Load(), f.exchanges.Load())
	}
	// The new assertion is dated by the moved clock and lives five minutes.
	if iat, _ := f.lastClaims["iat"].(float64); int64(iat) != now().Unix() {
		t.Fatalf("assertion iat %v", f.lastClaims["iat"])
	}
}

func TestKeylessCancellation(t *testing.T) {
	resetCache(t)
	f := newFake(t)
	f.blockSign = true
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := Connector{}.InspectWorkspaceDomain(ctx, f.client(), keylessCreds("test-only-refresh-a"), "Ctest123", "northpeak.example")
		done <- err
	}()
	for f.signs.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want cancellation, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not stop the signing call")
	}
	if f.exchanges.Load() != 0 || f.reads.Load() != 0 {
		t.Fatal("went on after cancellation")
	}
}

func TestKeylessCacheIsolation(t *testing.T) {
	resetCache(t)
	f := newFake(t)
	cl := f.client()
	ctx := context.Background()
	a := keylessCreds("test-only-refresh-a")
	b := keylessCreds("test-only-refresh-b") // another connection's sign-in
	otherAdmin := keylessCreds("test-only-refresh-a")
	otherAdmin["delegated_admin"] = "second-admin@northpeak.example"
	for _, cr := range []c.Creds{a, b, a, b} {
		if _, _, err := token(ctx, cl, cr); err != nil {
			t.Fatal(err)
		}
	}
	if f.signs.Load() != 2 || f.exchanges.Load() != 2 || f.refreshes.Load() != 2 {
		t.Fatalf("two connections: sign %d exchange %d refresh %d", f.signs.Load(), f.exchanges.Load(), f.refreshes.Load())
	}
	ta, _, _ := token(ctx, cl, a)
	tb, _, _ := token(ctx, cl, b)
	if ta == tb {
		t.Fatal("two connections share a delegated token")
	}
	// The same sign-in for another administrator signs again; it may reuse
	// the signer's own token but never the delegated one.
	to, _, err := token(ctx, cl, otherAdmin)
	if err != nil || to == ta || f.signs.Load() != 3 || f.refreshes.Load() != 2 {
		t.Fatalf("other administrator: %v sign %d refresh %d", err, f.signs.Load(), f.refreshes.Load())
	}
	if f.lastClaims["sub"] != "second-admin@northpeak.example" {
		t.Fatalf("signed for %v", f.lastClaims["sub"])
	}
	// Changing the service account invalidates the sign-in's cache entry too.
	otherSA := keylessCreds("test-only-refresh-a")
	otherSA["service_account_email"] = "other-reader@northpeak-demo.iam.gserviceaccount.com"
	if k1, k2 := cacheKey("delegated", testClient, "test-only-refresh-a", testSA, testAdmin, strings.Join(scopes, " ")), cacheKey("delegated", testClient, "test-only-refresh-a", otherSA["service_account_email"], testAdmin, strings.Join(scopes, " ")); k1 == k2 {
		t.Fatal("service account not part of the cache key")
	}
}

func TestKeylessSignInRefusals(t *testing.T) {
	saved := keylessCreds("")
	delete(saved, "refresh_token")
	delete(saved, "signer_email")
	for _, mode := range []string{"no_scope", "unverified", "no_refresh", "refused", "other_audience"} {
		t.Run(mode, func(t *testing.T) {
			resetCache(t)
			f := newFake(t)
			f.codeMode = mode
			_, _, err := Connector{}.SignInPKCE(context.Background(), f.client(), saved, "test-only-code", "https://outflare.example/cb", "v")
			if codeOf(err) != c.CodeWorkspaceSignerNeeded || strings.Contains(err.Error(), privateTxt) {
				t.Fatalf("got %v", err)
			}
			if f.signs.Load() != 0 {
				t.Fatal("signed with an unusable sign-in")
			}
		})
	}
	t.Run("permission missing stores nothing", func(t *testing.T) {
		resetCache(t)
		f := newFake(t)
		f.signMode = "denied"
		_, _, err := Connector{}.SignInPKCE(context.Background(), f.client(), saved, "test-only-code", "https://outflare.example/cb", "v")
		if codeOf(err) != c.CodeWorkspaceSignerDenied {
			t.Fatalf("got %v", err)
		}
		n := 0
		tokens.Range(func(_, _ any) bool { n++; return true })
		if n != 0 {
			t.Fatalf("%d tokens cached for a refused sign-in", n)
		}
		if f.revoked.Load() != 1 {
			t.Fatal("unproven sign-in not revoked")
		}
	})
	t.Run("key connection needs no sign-in", func(t *testing.T) {
		_, _, err := Connector{}.SignInPKCE(context.Background(), &c.Client{}, c.Creds{"service_account_json": `{"type":"service_account"}`, "delegated_admin": testAdmin, "customer_id": "Ctest123"}, "x", "y", "z")
		if c.KindOf(err) != c.ErrInvalid {
			t.Fatalf("got %v", err)
		}
	})
}

func TestModeValidation(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	cl := f.client()
	bad := map[string]c.Creds{
		"both ways":             {"service_account_json": `{"type":"service_account"}`, "service_account_email": testSA, "oauth_client_id": testClient, "oauth_client_secret": testSecret, "delegated_admin": testAdmin, "customer_id": "Ctest123"},
		"neither":               {"delegated_admin": testAdmin, "customer_id": "Ctest123"},
		"no client secret":      {"service_account_email": testSA, "oauth_client_id": testClient, "delegated_admin": testAdmin, "customer_id": "Ctest123"},
		"not a service account": {"service_account_email": "someone@northpeak.example", "oauth_client_id": testClient, "oauth_client_secret": testSecret, "delegated_admin": testAdmin, "customer_id": "Ctest123"},
		"not a Google client":   {"service_account_email": testSA, "oauth_client_id": "client.northpeak.example", "oauth_client_secret": testSecret, "delegated_admin": testAdmin, "customer_id": "Ctest123"},
		"bad tenant":            {"service_account_email": testSA, "oauth_client_id": testClient, "oauth_client_secret": testSecret, "delegated_admin": testAdmin, "customer_id": "123"},
		"bad administrator":     {"service_account_email": testSA, "oauth_client_id": testClient, "oauth_client_secret": testSecret, "delegated_admin": "Admin <admin@northpeak.example>", "customer_id": "Ctest123"},
	}
	for name, cr := range bad {
		if _, err := (Connector{}).Test(ctx, cl, cr); c.KindOf(err) != c.ErrInvalid {
			t.Errorf("%s: got %v", name, err)
		}
	}
	if n := f.refreshes.Load() + f.signs.Load() + f.exchanges.Load() + f.reads.Load(); n != 0 {
		t.Fatalf("invalid connections called Google %d times", n)
	}
	// The catalog no longer makes the key mandatory.
	for _, fl := range (Connector{}).Definition().Fields {
		if fl.Name == "service_account_json" && !fl.Optional {
			t.Fatal("key still required")
		}
	}
}

func TestKeylessSignInAgainPreservesNewGrantAndEvictsOldCache(t *testing.T) {
	resetCache(t)
	f := newFake(t)
	old := keylessCreds("test-only-refresh-old")
	if _, _, err := customer(context.Background(), f.client(), old); err != nil {
		t.Fatal(err)
	}
	oldSigner := cacheKey("signer", old["oauth_client_id"], old["refresh_token"])
	oldDelegated := cacheKey("delegated", old["oauth_client_id"], old["refresh_token"], strings.ToLower(old["service_account_email"]), strings.ToLower(old["delegated_admin"]), strings.Join(scopes, " "))
	if _, _, err := (Connector{}).SignInPKCE(context.Background(), f.client(), old, "test-only-code", "https://outflare.example/cb", "v"); err != nil {
		t.Fatal(err)
	}
	if f.revoked.Load() != 0 {
		t.Fatal("replacement revoked the Google grant")
	}
	for _, key := range []string{oldSigner, oldDelegated} {
		if _, ok := tokens.Load(key); ok {
			t.Fatal("old sign-in remains cached")
		}
	}
}

func TestKeylessRevocationEvictsCachedTokens(t *testing.T) {
	resetCache(t)
	f := newFake(t)
	cl := f.client()
	cr := keylessCreds("test-only-refresh-a")
	ctx := context.Background()
	if _, err := (Connector{}).InspectWorkspaceDomain(ctx, cl, cr, "Ctest123", "northpeak.example"); err != nil {
		t.Fatal(err)
	}
	// Delegation narrowed: the Directory says 403, and the next call signs again.
	f.dirMode = "403"
	if _, err := (Connector{}).InspectWorkspaceDomain(ctx, cl, cr, "Ctest123", "northpeak.example"); err == nil {
		t.Fatal("403 accepted")
	}
	f.dirMode = ""
	if _, err := (Connector{}).InspectWorkspaceDomain(ctx, cl, cr, "Ctest123", "northpeak.example"); err != nil || f.signs.Load() != 2 {
		t.Fatalf("delegated token reused after 403: %v signs %d", err, f.signs.Load())
	}
	// The signer token refused by IAM Credentials is renewed, not reused.
	resetCache(t)
	f.signMode = "unauthorized"
	_, err := Connector{}.InspectWorkspaceDomain(ctx, cl, cr, "Ctest123", "northpeak.example")
	if codeOf(err) != c.CodeWorkspaceSignerNeeded {
		t.Fatalf("got %v", err)
	}
	before := f.refreshes.Load()
	f.signMode = ""
	if _, err := (Connector{}).InspectWorkspaceDomain(ctx, cl, cr, "Ctest123", "northpeak.example"); err != nil || f.refreshes.Load() != before+1 {
		t.Fatalf("refused signer token reused: %v", err)
	}
}

func TestKeylessFailedSignInAgainPreservesExistingGrant(t *testing.T) {
	resetCache(t)
	f := newFake(t)
	f.signMode = "denied"
	old := keylessCreds("test-only-refresh-old")
	if _, _, err := (Connector{}).SignInPKCE(context.Background(), f.client(), old, "test-only-code", "https://outflare.example/cb", "v"); codeOf(err) != c.CodeWorkspaceSignerDenied {
		t.Fatalf("got %v", err)
	}
	if f.revoked.Load() != 0 {
		t.Fatal("failed replacement revoked the existing Google grant")
	}
}
