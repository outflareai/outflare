// Package googleworkspace reads Google Workspace tenant/domain state through
// a service account the tenant's administrator authorized for domain-wide
// delegation of two read-only Directory scopes.
//
// It connects two ways, and each connection uses exactly one:
//
//   - Keyless (recommended): the connection holds the service account's
//     email and the workspace's own Google OAuth client. A person signs the
//     connection in to a Google account that may sign as the service account
//     (Service Account Token Creator on that account only). Each delegated
//     token comes from the IAM Credentials signJwt call made with that
//     sign-in, then Google's JWT bearer grant. No private key exists outside
//     Google.
//   - Key: a downloaded service account key, as before.
//
// It never uses an operator key, the server's own OAuth apps, ambient
// credentials such as gcloud's, or another connection's sign-in, and never
// changes the scopes of the shared Google OAuth application. Tokens and
// signed assertions stay in memory and never appear in errors or logs.
package googleworkspace

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

const ID = "google_workspace"

const (
	tokenURL = "https://oauth2.googleapis.com/token"
	authURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	// iamScope is the narrowest scope that lets the signed-in account call
	// signJwt; the account's IAM role still limits it to one service account.
	iamScope = "https://www.googleapis.com/auth/iam"
	// assertionLife is how long a signed assertion is valid. Google's token
	// endpoint accepts at most an hour; five minutes is plenty for one trade.
	assertionLife = 5 * time.Minute
	// reuseMargin: a cached token is used only while it has longer than
	// this left.
	reuseMargin = 5 * time.Minute
)

var customerID = regexp.MustCompile(`^C[a-zA-Z0-9]{1,99}$`)
var domainLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// serviceAccount is a user-managed service account's address: an account id
// and a project id of 6 to 30 characters.
var serviceAccount = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]@[a-z][a-z0-9-]{4,28}[a-z0-9]\.iam\.gserviceaccount\.com$`)
var oauthClient = regexp.MustCompile(`^[A-Za-z0-9-]{1,200}\.apps\.googleusercontent\.com$`)

// scopes are the only scopes a delegated token is ever requested with.
var scopes = []string{"https://www.googleapis.com/auth/admin.directory.customer.readonly", "https://www.googleapis.com/auth/admin.directory.domain.readonly"}

// now is the clock; tests move it.
var now = time.Now

type cached struct {
	token   string
	expires time.Time
}

// tokens holds signer and delegated access tokens in memory only, keyed by a
// hash of everything the token depends on: the connection's own sign-in
// (refresh token) and OAuth client, the service account, the administrator
// and the scopes. A different connection, signer, service account,
// administrator or scope set never shares an entry.
var tokens sync.Map

func cacheKey(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func fromCache(key string) (string, bool) {
	if v, ok := tokens.Load(key); ok {
		t := v.(cached)
		if t.expires.Sub(now()) > reuseMargin {
			return t.token, true
		}
		// Only this stale entry: another call may have stored a fresh one.
		tokens.CompareAndDelete(key, v)
	}
	return "", false
}

type Connector struct{}

var (
	_ c.WorkspaceInspector = Connector{}
	_ c.AccountSignIn      = Connector{}
	_ c.AccountSignInPKCE  = Connector{}
	_ c.Connector          = Connector{}
)

func (Connector) Definition() c.Definition {
	keyField := c.ServiceAccountField
	keyField.Optional = true
	keyField.Label = "Service account key (JSON), only instead of signing in"
	keyField.Help = "Leave empty to connect without a key (recommended). Only if you cannot sign in: a key downloaded from Google Cloud for the same service account."
	return c.Definition{ID: ID, Name: "Google Workspace", Category: c.CategoryMailbox, Auth: c.AuthAPIKey,
		Summary: "Inspect the tenant and domain with your administrator's delegated service account, without a key.",
		Description: "Read-only tenant/domain checks. In your Google Cloud project: enable Admin SDK API and IAM Service Account Credentials API, " +
			"create a service account, and grant a dedicated Google account (not a project owner) Service Account Token Creator on that service account only; you will sign in with it. " +
			"Create an OAuth client (web application) with this server's redirect URI. In Google Admin, Security, API controls, Domain-wide delegation, " +
			"authorize the service account's numeric client ID for exactly the Directory customer.readonly and domain.readonly scopes. " +
			"Then connect with the service account's email and your OAuth client, and sign in. No key is downloaded. " +
			"With profile changes allowed (and the Directory user and Gmail settings basic scopes authorized too), it can change existing users' names, photos and signatures. " +
			"It cannot create or delete users, enroll two-factor authentication or retrieve passwords or login codes.",
		Website: "https://workspace.google.com", DocsURL: "https://developers.google.com/workspace/admin/directory/v1/guides/manage-domains",
		KeyURL: "https://console.cloud.google.com/iam-admin/serviceaccounts",
		Fields: []c.Field{
			{Name: "delegated_admin", Label: "Delegated Workspace administrator", Help: "Administrator email in this tenant; Google must authorize domain-wide delegation."},
			{Name: "customer_id", Label: "Google customer ID", Help: "The tenant's immutable customer ID beginning with C, supplied by the administrator or reseller."},
			{Name: "service_account_email", Label: "Service account email", Optional: true, Placeholder: "workspace-reader@your-project.iam.gserviceaccount.com",
				Help: "For connecting without a key: the service account the tenant authorized for domain-wide delegation."},
			{Name: "oauth_client_id", Label: "Your OAuth client ID", Optional: true, Placeholder: "….apps.googleusercontent.com",
				Help: "For connecting without a key: an OAuth client (web application) in your own Google Cloud project, with this server's redirect URI. After connecting, sign in."},
			{Name: "oauth_client_secret", Label: "Your OAuth client secret", Secret: true, Optional: true},
			{Name: "manage_users", Label: "Allow profile changes", Optional: true, Placeholder: "yes",
				Help: "Type yes to let Outflare change existing users' names, photos and Gmail signatures. Also authorize the service account for the Directory user and Gmail settings (basic) scopes. It never creates or deletes users or sees passwords."},
			keyField,
		},
		Capabilities: []string{c.CapWorkspaceInspect, c.CapWorkspaceManageUsers}, Pricing: "Uses your Google Workspace account"}
}

// keyless reports whether creds use the sign-in path instead of a key.
func keyless(cr c.Creds) bool {
	return strings.TrimSpace(cr["service_account_json"]) == ""
}

// checkCreds validates what every call needs and returns the administrator.
// A connection uses exactly one way: a key, or a service account email with
// the workspace's own OAuth client.
func checkCreds(cr c.Creds) (string, error) {
	admin := strings.TrimSpace(cr["delegated_admin"])
	parsed, parseErr := mail.ParseAddress(admin)
	if parseErr != nil || parsed.Address != admin || strings.ContainsAny(admin, "\r\n") || strings.Count(admin, "@") != 1 || !customerID.MatchString(cr["customer_id"]) {
		return "", c.Errf(c.ErrInvalid, "provide the delegated administrator and Google customer ID")
	}
	if m := strings.TrimSpace(cr["manage_users"]); m != "" && m != "yes" {
		return "", c.Errf(c.ErrInvalid, "leave profile changes empty, or type yes to allow them")
	}
	signIn := cr["service_account_email"] != "" || cr["oauth_client_id"] != "" || cr["oauth_client_secret"] != ""
	if !keyless(cr) {
		if signIn {
			return "", c.Errf(c.ErrInvalid, "connect either without a key (service account email and your OAuth client) or with a key, not both")
		}
		var key struct {
			TokenURI string `json:"token_uri"`
		}
		if json.Unmarshal([]byte(cr["service_account_json"]), &key) != nil || (key.TokenURI != "" && key.TokenURI != tokenURL) {
			return "", c.Errf(c.ErrInvalid, "use the service account key downloaded from Google Cloud")
		}
		return admin, nil
	}
	if !serviceAccount.MatchString(cr["service_account_email"]) || !oauthClient.MatchString(cr["oauth_client_id"]) || cr["oauth_client_secret"] == "" || len(cr["oauth_client_secret"]) > 512 {
		return "", c.Errf(c.ErrInvalid, "to connect without a key, provide the service account email (…@project.iam.gserviceaccount.com) and your own OAuth client ID and secret")
	}
	return admin, nil
}

// token returns a delegated Directory token for the connection and the key
// it is cached under.
func token(ctx context.Context, cl *c.Client, cr c.Creds) (string, string, error) {
	admin, err := checkCreds(cr)
	if err != nil {
		return "", "", err
	}
	return delegated(ctx, cl, cr, scopes, admin)
}

// delegated returns a token for subject with exactly the given scopes, and
// the key it is cached under.
func delegated(ctx context.Context, cl *c.Client, cr c.Creds, scopes []string, admin string) (string, string, error) {
	if !keyless(cr) {
		t, err := c.ServiceAccountToken(ctx, cl, cr["service_account_json"], scopes, admin)
		if err != nil {
			kind := c.KindOf(err)
			if kind == "" || kind == c.ErrInvalid {
				kind = c.ErrAuth
			}
			return "", "", c.Errf(kind, "Google Workspace delegation failed; check the key, administrator and authorized read-only Directory scopes")
		}
		return t.AccessToken, "", nil
	}
	if cr["refresh_token"] == "" {
		return "", "", c.Codedf(c.CodeWorkspaceSignerNeeded, c.ErrAuth, "sign this Google Workspace connection in to the Google account allowed to sign as its service account (Connectors, Google Workspace, Sign in)")
	}
	key := cacheKey("delegated", cr["oauth_client_id"], cr["refresh_token"], strings.ToLower(cr["service_account_email"]), strings.ToLower(admin), strings.Join(scopes, " "))
	if t, ok := fromCache(key); ok {
		return t, key, nil
	}
	signer, err := signerToken(ctx, cl, cr)
	if err != nil {
		return "", "", err
	}
	assertion, err := signJWT(ctx, cl, signer, cr["service_account_email"], admin, scopes)
	if err != nil {
		if codeOfErr(err) == c.CodeWorkspaceSignerNeeded {
			// The signer token was refused: never reuse it.
			tokens.Delete(cacheKey("signer", cr["oauth_client_id"], cr["refresh_token"]))
		}
		return "", "", err
	}
	t, life, err := exchange(ctx, cl, assertion)
	if err != nil {
		return "", "", err
	}
	tokens.Store(key, cached{token: t, expires: now().Add(life)})
	return t, key, nil
}

// googleError is the parts of a Google error body this package reads: codes
// and enums only, never messages, which can quote private details.
type googleError struct {
	OAuthError string `json:"-"`
	Status     string
	Reasons    []string
}

func readError(resp *http.Response) googleError {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var g struct {
		Error json.RawMessage `json:"error"`
	}
	out := googleError{}
	if json.Unmarshal(raw, &g) != nil || len(g.Error) == 0 {
		return out
	}
	if json.Unmarshal(g.Error, &out.OAuthError) == nil {
		return out
	}
	var e struct {
		Status  string `json:"status"`
		Details []struct {
			Reason string `json:"reason"`
		} `json:"details"`
	}
	if json.Unmarshal(g.Error, &e) == nil {
		out.Status = e.Status
		for _, d := range e.Details {
			out.Reasons = append(out.Reasons, d.Reason)
		}
	}
	return out
}

type tokenReply struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

// postForm calls Google's token endpoint. Errors carry only the OAuth error
// code's meaning, never the response.
func postForm(ctx context.Context, cl *c.Client, form url.Values) (tokenReply, int, googleError, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cl.Base(ID+".token", tokenURL), strings.NewReader(form.Encode()))
	if err != nil {
		return tokenReply{}, 0, googleError{}, c.Errf(c.ErrInvalid, "invalid Google sign-in request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return tokenReply{}, 0, googleError{}, ctx.Err()
		}
		return tokenReply{}, 0, googleError{}, c.Errf(c.ErrUnavailable, "could not reach Google")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return tokenReply{}, resp.StatusCode, readError(resp), nil
	}
	var t tokenReply
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 || json.Unmarshal(raw, &t) != nil || t.AccessToken == "" {
		return tokenReply{}, 0, googleError{}, c.Errf(c.ErrUnavailable, "Google returned an unreadable token response")
	}
	return t, http.StatusOK, googleError{}, nil
}

func lifetime(expiresIn int64) time.Duration {
	if expiresIn <= 0 || expiresIn > 24*3600 {
		return time.Hour
	}
	return time.Duration(expiresIn) * time.Second
}

func hasScope(granted, want string) bool {
	for _, s := range strings.Fields(granted) {
		if s == want {
			return true
		}
	}
	return false
}

// signerToken renews the signed-in account's access token with the
// connection's own OAuth client and refresh token.
func signerToken(ctx context.Context, cl *c.Client, cr c.Creds) (string, error) {
	key := cacheKey("signer", cr["oauth_client_id"], cr["refresh_token"])
	if t, ok := fromCache(key); ok {
		return t, nil
	}
	t, status, g, err := postForm(ctx, cl, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {cr["refresh_token"]},
		"client_id": {cr["oauth_client_id"]}, "client_secret": {cr["oauth_client_secret"]},
	})
	if err != nil {
		return "", err
	}
	switch {
	case status == http.StatusOK:
	case status >= 500 || status == 0:
		return "", c.Errf(c.ErrUnavailable, "Google is not responding properly (HTTP %d). Try again in a few minutes.", status)
	case status == http.StatusTooManyRequests:
		return "", c.Errf(c.ErrQuota, "Google is rate limiting sign-in renewals (HTTP 429)")
	case g.OAuthError == "invalid_client" || g.OAuthError == "unauthorized_client":
		return "", c.Codedf(c.CodeWorkspaceSignerNeeded, c.ErrAuth, "Google refused this connection's OAuth client; check its ID and secret, then sign in again")
	default:
		return "", c.Codedf(c.CodeWorkspaceSignerNeeded, c.ErrAuth, "the Google sign-in for this Workspace connection expired or was revoked; sign in again")
	}
	if t.Scope != "" && !hasScope(t.Scope, iamScope) {
		return "", c.Codedf(c.CodeWorkspaceSignerNeeded, c.ErrAuth, "the Google sign-in did not grant permission to sign as the service account; sign in again and allow it")
	}
	tokens.Store(key, cached{token: t.AccessToken, expires: now().Add(lifetime(t.ExpiresIn))})
	return t.AccessToken, nil
}

// signJWT asks IAM Credentials to sign the delegation assertion as the
// service account, with the signed-in account's token.
func signJWT(ctx context.Context, cl *c.Client, signer, sa, admin string, scopes []string) (string, error) {
	iat := now().Unix()
	claims, _ := json.Marshal(map[string]any{
		"iss": sa, "sub": admin, "aud": tokenURL, "iat": iat, "exp": iat + int64(assertionLife/time.Second),
		"scope": strings.Join(scopes, " "),
	})
	body, _ := json.Marshal(map[string]string{"payload": string(claims)})
	endpoint := cl.Base(ID+".iam", "https://iamcredentials.googleapis.com") + "/v1/projects/-/serviceAccounts/" + url.PathEscape(sa) + ":signJwt"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return "", c.Errf(c.ErrInvalid, "invalid signing request")
	}
	req.Header.Set("Authorization", "Bearer "+signer)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", c.Errf(c.ErrUnavailable, "could not reach Google IAM Credentials")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		g := readError(resp)
		for _, r := range g.Reasons {
			switch r {
			case "SERVICE_DISABLED":
				return "", c.Codedf(c.CodeWorkspaceSignerDenied, c.ErrAuth, "enable the IAM Service Account Credentials API in the Google Cloud project of your OAuth client and of the service account")
			case "ACCESS_TOKEN_SCOPE_INSUFFICIENT":
				return "", c.Codedf(c.CodeWorkspaceSignerNeeded, c.ErrAuth, "the Google sign-in did not grant permission to sign as the service account; sign in again and allow it")
			}
		}
		switch {
		case resp.StatusCode == http.StatusUnauthorized:
			return "", c.Codedf(c.CodeWorkspaceSignerNeeded, c.ErrAuth, "Google no longer accepts this connection's sign-in; sign in again")
		case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound:
			return "", c.Codedf(c.CodeWorkspaceSignerDenied, c.ErrAuth, "the signed-in Google account may not sign as the service account: grant it Service Account Token Creator on that service account, and check the service account exists")
		case resp.StatusCode == http.StatusTooManyRequests:
			return "", c.Errf(c.ErrQuota, "Google IAM Credentials is rate limiting (HTTP 429)")
		case resp.StatusCode >= 500:
			return "", c.Errf(c.ErrUnavailable, "Google IAM Credentials is not responding properly (HTTP %d). Try again in a few minutes.", resp.StatusCode)
		}
		return "", c.Errf(c.ErrInvalid, "Google IAM Credentials refused the signing request (HTTP %d)", resp.StatusCode)
	}
	var out struct {
		SignedJWT string `json:"signedJwt"`
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 || json.Unmarshal(raw, &out) != nil || strings.Count(out.SignedJWT, ".") != 2 {
		return "", c.Errf(c.ErrUnavailable, "Google IAM Credentials returned an unreadable response")
	}
	return out.SignedJWT, nil
}

// exchange trades a signed assertion for a delegated access token.
func exchange(ctx context.Context, cl *c.Client, assertion string) (string, time.Duration, error) {
	t, status, g, err := postForm(ctx, cl, url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}})
	if err != nil {
		return "", 0, err
	}
	switch {
	case status == http.StatusOK:
		return t.AccessToken, lifetime(t.ExpiresIn), nil
	case status >= 500 || status == 0:
		return "", 0, c.Errf(c.ErrUnavailable, "Google is not responding properly (HTTP %d). Try again in a few minutes.", status)
	case status == http.StatusTooManyRequests:
		return "", 0, c.Errf(c.ErrQuota, "Google is rate limiting token requests (HTTP 429)")
	}
	if g.OAuthError == "invalid_grant" {
		// A bad assertion (often a clock off by minutes) or an
		// administrator Google doesn't know.
		return "", 0, c.Codedf(c.CodeWorkspaceDelegationDenied, c.ErrAuth, "Google refused the signed request: check the delegated administrator is a user of this tenant, then try again (a server clock far off also causes this)")
	}
	return "", 0, c.Codedf(c.CodeWorkspaceDelegationDenied, c.ErrAuth, "Google refused domain-wide delegation: in Google Admin, authorize the service account's client ID for exactly the two read-only Directory scopes, and check the administrator belongs to the tenant")
}

func read(ctx context.Context, cl *c.Client, bearer, cacheKey, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cl.Base(ID, "https://admin.googleapis.com")+"/admin/directory/v1/"+path, nil)
	if err != nil {
		return c.Errf(c.ErrInvalid, "invalid Google Workspace request")
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return c.Errf(c.ErrUnavailable, "could not reach Google Workspace")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) && cacheKey != "" {
			// Revoked or narrowed delegation: never reuse the token.
			tokens.Delete(cacheKey)
		}
		return c.HTTPError("Google Workspace", resp.StatusCode, "")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 || json.Unmarshal(raw, out) != nil {
		return c.Errf(c.ErrUnavailable, "Google Workspace returned an unreadable response")
	}
	return nil
}

func customer(ctx context.Context, cl *c.Client, cr c.Creds) (string, string, error) {
	bearer, key, err := token(ctx, cl, cr)
	if err != nil {
		return "", key, err
	}
	var account struct {
		ID string `json:"id"`
	}
	if err = read(ctx, cl, bearer, key, "customers/"+url.PathEscape(cr["customer_id"])+"?fields=id", &account); err != nil {
		return "", key, err
	}
	if account.ID != cr["customer_id"] {
		return "", key, c.Errf(c.ErrInvalid, "the Google customer ID does not match this connection")
	}
	return bearer, key, nil
}

func (Connector) Test(ctx context.Context, cl *c.Client, cr c.Creds) (c.Account, error) {
	if _, err := checkCreds(cr); err != nil {
		return c.Account{}, err
	}
	if keyless(cr) && cr["refresh_token"] == "" {
		// Saved, but not usable until a person signs it in. The account has
		// no id, so nothing selects it for a tenant before then.
		return c.Account{Name: "Google Workspace", Details: map[string]string{"signer": "sign_in_required"}}, nil
	}
	if _, _, err := customer(ctx, cl, cr); err != nil {
		return c.Account{}, err
	}
	a := c.Account{ID: cr["customer_id"], Name: "Google Workspace"}
	if keyless(cr) {
		a.Email = cr["signer_email"]
		a.Details = map[string]string{"signer": "signed_in"}
	}
	return a, nil
}

func (Connector) InspectWorkspaceDomain(ctx context.Context, cl *c.Client, cr c.Creds, expected, domain string) (c.WorkspaceDomain, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if !customerID.MatchString(expected) || expected != cr["customer_id"] || !validDomain(domain) {
		return c.WorkspaceDomain{}, c.Errf(c.ErrInvalid, "provide the connected Google customer ID and a valid domain")
	}
	bearer, key, err := customer(ctx, cl, cr)
	if err != nil {
		return c.WorkspaceDomain{}, err
	}
	var d struct {
		Domain   string `json:"domainName"`
		Verified *bool  `json:"verified"`
		Primary  *bool  `json:"isPrimary"`
	}
	if err = read(ctx, cl, bearer, key, "customer/"+url.PathEscape(expected)+"/domains/"+url.PathEscape(domain)+"?fields=domainName,verified,isPrimary", &d); err != nil {
		return c.WorkspaceDomain{}, err
	}
	if strings.ToLower(d.Domain) != domain || d.Verified == nil || d.Primary == nil {
		return c.WorkspaceDomain{}, c.Errf(c.ErrUnavailable, "Google Workspace returned incomplete domain evidence")
	}
	return c.WorkspaceDomain{CustomerID: expected, Domain: domain, Verified: *d.Verified, Primary: *d.Primary}, nil
}

// ---- Signing the connection in (keyless) ----

// SignInURL is the consent page of the workspace's own OAuth client. The
// server always uses SignInURLPKCE; this exists for AccountSignIn.
func (Connector) SignInURL(cl *c.Client, cr c.Creds, redirectURI, state string) string {
	return signInURL(cl, cr, redirectURI, state, "")
}

func (Connector) SignInURLPKCE(cl *c.Client, cr c.Creds, redirectURI, state, verifier string) string {
	return signInURL(cl, cr, redirectURI, state, c.PKCEChallenge(verifier))
}

func signInURL(cl *c.Client, cr c.Creds, redirectURI, state, challenge string) string {
	q := url.Values{
		"client_id": {cr["oauth_client_id"]}, "redirect_uri": {redirectURI}, "response_type": {"code"},
		"scope": {"openid email " + iamScope}, "access_type": {"offline"}, "prompt": {"consent"}, "state": {state},
	}
	if challenge != "" {
		q.Set("code_challenge", challenge)
		q.Set("code_challenge_method", "S256")
	}
	return cl.Base(ID+".accounts", authURL) + "?" + q.Encode()
}

func (g Connector) SignIn(ctx context.Context, cl *c.Client, cr c.Creds, code, redirectURI string) (c.Creds, c.Account, error) {
	return g.SignInPKCE(ctx, cl, cr, code, redirectURI, "")
}

// SignInPKCE trades the code with the workspace's own OAuth client, then
// proves the whole path (sign, delegate, read the tenant) before anything is
// stored, so a connection is never marked signed in when it cannot work.
func (Connector) SignInPKCE(ctx context.Context, cl *c.Client, cr c.Creds, code, redirectURI, verifier string) (c.Creds, c.Account, error) {
	if !keyless(cr) {
		return nil, c.Account{}, c.Errf(c.ErrInvalid, "this connection uses a service account key and needs no sign-in")
	}
	if _, err := checkCreds(cr); err != nil {
		return nil, c.Account{}, err
	}
	if code == "" || len(code) > 2048 {
		return nil, c.Account{}, c.Codedf(c.CodeWorkspaceSignerNeeded, c.ErrAuth, "Google did not complete the sign-in; try again")
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI},
		"client_id": {cr["oauth_client_id"]}, "client_secret": {cr["oauth_client_secret"]}}
	if verifier != "" {
		form.Set("code_verifier", verifier)
	}
	t, status, _, err := postForm(ctx, cl, form)
	if err != nil {
		return nil, c.Account{}, err
	}
	if status != http.StatusOK {
		if status >= 500 || status == 0 {
			return nil, c.Account{}, c.Errf(c.ErrUnavailable, "Google is not responding properly (HTTP %d). Try again in a few minutes.", status)
		}
		return nil, c.Account{}, c.Codedf(c.CodeWorkspaceSignerNeeded, c.ErrAuth, "Google did not accept the sign-in; check your OAuth client's redirect URI and secret, then try again")
	}
	if t.RefreshToken == "" {
		return nil, c.Account{}, c.Codedf(c.CodeWorkspaceSignerNeeded, c.ErrAuth, "Google gave no lasting sign-in; try again")
	}
	if !hasScope(t.Scope, iamScope) {
		revokeUnproven(cl, cr, t.RefreshToken)
		return nil, c.Account{}, c.Codedf(c.CodeWorkspaceSignerNeeded, c.ErrAuth, "the sign-in did not allow signing as the service account; sign in again and allow it")
	}
	email := verifiedEmail(t.IDToken, cr["oauth_client_id"])
	if email == "" {
		revokeUnproven(cl, cr, t.RefreshToken)
		return nil, c.Account{}, c.Codedf(c.CodeWorkspaceSignerNeeded, c.ErrAuth, "Google did not confirm the signed-in account's email; try again")
	}
	out := c.Creds{}
	for _, f := range []string{"delegated_admin", "customer_id", "service_account_email", "oauth_client_id", "oauth_client_secret"} {
		out[f] = cr[f]
	}
	out["refresh_token"] = t.RefreshToken
	out["signer_email"] = email
	// Reuse the token this sign-in just returned instead of renewing it.
	signerKey := cacheKey("signer", out["oauth_client_id"], out["refresh_token"])
	tokens.Store(signerKey, cached{token: t.AccessToken, expires: now().Add(lifetime(t.ExpiresIn))})
	if _, key, err := customer(ctx, cl, out); err != nil {
		// Nothing is stored for a sign-in that cannot work.
		tokens.Delete(signerKey)
		if key != "" {
			tokens.Delete(key)
		}
		revokeUnproven(cl, cr, out["refresh_token"])
		return nil, c.Account{}, err
	}
	if old := cr["refresh_token"]; old != "" && old != out["refresh_token"] {
		// Google's revocation endpoint can revoke the whole client grant,
		// including the new refresh token. Drop old cached access locally;
		// do not revoke the grant during a successful replacement.
		tokens.Delete(cacheKey("signer", cr["oauth_client_id"], old))
		tokens.Delete(cacheKey("delegated", cr["oauth_client_id"], old, strings.ToLower(cr["service_account_email"]), strings.ToLower(cr["delegated_admin"]), strings.Join(scopes, " ")))
	}
	return out, c.Account{ID: out["customer_id"], Name: "Google Workspace", Email: email, Details: map[string]string{"signer": "signed_in"}}, nil
}

// verifiedEmail reads the email of an ID token that came straight from
// Google's token endpoint over TLS (OpenID Connect Core 3.1.3.7 lets the
// client skip the signature then). Only a verified email counts.
func verifiedEmail(idToken, clientID string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Email    string `json:"email"`
		Verified any    `json:"email_verified"`
		Issuer   string `json:"iss"`
		Audience any    `json:"aud"`
	}
	if json.Unmarshal(raw, &claims) != nil {
		return ""
	}
	// Issued by Google for this connection's own client.
	if claims.Issuer != "https://accounts.google.com" && claims.Issuer != "accounts.google.com" {
		return ""
	}
	if aud, ok := claims.Audience.(string); !ok || aud != clientID {
		return ""
	}
	verified := claims.Verified == true
	if s, ok := claims.Verified.(string); ok {
		verified, _ = strconv.ParseBool(s)
	}
	a, err := mail.ParseAddress(claims.Email)
	if !verified || err != nil || a.Address != claims.Email || len(claims.Email) > 254 {
		return ""
	}
	return claims.Email
}

func validDomain(domain string) bool {
	if len(domain) > 253 || !strings.Contains(domain, ".") {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if !domainLabel.MatchString(label) {
			return false
		}
	}
	return true
}

// revokeUnproven must preserve an existing sign-in if reauthorization fails:
// revoking the candidate can also revoke the existing client grant.
func revokeUnproven(cl *c.Client, previous c.Creds, candidate string) {
	if previous["refresh_token"] == "" {
		revoke(cl, candidate)
	}
}

// revoke asks Google to end a sign-in that was never proven to work.
// Google may revoke the whole client grant, not just the supplied token.
// Best effort: it runs detached from the request with its own short timeout,
// and a failure changes nothing for the person.
func revoke(cl *c.Client, refreshToken string) {
	if refreshToken == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cl.Base(ID+".revoke", "https://oauth2.googleapis.com/revoke"), strings.NewReader(url.Values{"token": {refreshToken}}.Encode()))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if resp, err := cl.Do(req); err == nil {
		resp.Body.Close()
	}
}

func codeOfErr(err error) string {
	var ce *c.CodedError
	if errors.As(err, &ce) {
		return ce.Code()
	}
	return ""
}
