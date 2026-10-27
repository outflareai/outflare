// Package oauth gets access tokens for Google Workspace and Microsoft 365
// mailboxes, so they can sign in to SMTP and IMAP with XOAUTH2 instead of a
// password. Tokens come from one of three places:
//
//   - a Google service account with domain-wide delegation (admin level:
//     one key signs in as any mailbox in the organisation),
//   - a Microsoft Entra app with application access to Exchange Online
//     (admin level: one secret for the whole tenant),
//   - a refresh token from one person approving the consent screen.
package oauth

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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/mailbox"
	"github.com/outflareai/outflare/engine/internal/secrets"
)

// Scopes requested for mail.
const (
	GoogleMailScope = "https://mail.google.com/"
	// Application access to Exchange Online IMAP and SMTP.
	MicrosoftAppScope = "https://outlook.office365.com/.default"
	// Delegated access for one person's mailbox.
	MicrosoftUserScopes = "offline_access openid email https://outlook.office.com/IMAP.AccessAsUser.All https://outlook.office.com/SMTP.Send"
)

// Endpoints are the providers' OAuth URLs. Tests point them at a fake.
type Endpoints struct {
	GoogleAuth     string
	GoogleToken    string
	MicrosoftLogin string // base; /{tenant}/oauth2/v2.0/{authorize,token} is appended
}

// DefaultEndpoints are the real providers.
var DefaultEndpoints = Endpoints{
	GoogleAuth:     "https://accounts.google.com/o/oauth2/v2/auth",
	GoogleToken:    "https://oauth2.googleapis.com/token",
	MicrosoftLogin: "https://login.microsoftonline.com",
}

// Client is an OAuth app registered with a provider, used for the consent
// screen. Empty means that provider's consent flow is not configured.
type Client struct {
	ID, Secret string
}

// Tokens hands out access tokens and caches them until shortly before
// they expire.
type Tokens struct {
	Pool      *pgxpool.Pool
	Box       *secrets.Box
	Endpoints Endpoints
	Google    Client
	Microsoft Client
	HTTP      *http.Client

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	token   string
	expires time.Time
}

func (t *Tokens) http() *http.Client {
	if t.HTTP != nil {
		return t.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (t *Tokens) cached(key string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c, ok := t.cache[key]
	if !ok || time.Now().After(c.expires) {
		return "", false
	}
	return c.token, true
}

func (t *Tokens) store(key string, r *tokenResponse) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cache == nil {
		t.cache = map[string]cached{}
	}
	ttl := time.Duration(r.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	// Renew a few minutes early so a token never expires mid-session.
	t.cache[key] = cached{r.AccessToken, time.Now().Add(ttl - 5*time.Minute)}
}

// Token implements mailbox.TokenSource.
func (t *Tokens) Token(ctx context.Context, req mailbox.TokenRequest) (string, string, error) {
	if req.ConnectionID != nil {
		tok, err := t.connectionToken(ctx, req.WorkspaceID, *req.ConnectionID, req.Email)
		return tok, "", err
	}
	if req.RefreshToken == "" {
		return "", "", &Error{"invalid_grant", "this mailbox has no OAuth connection or refresh token; connect it again"}
	}
	key := "refresh:" + req.MailboxID
	if tok, ok := t.cached(key); ok {
		return tok, "", nil
	}
	var r *tokenResponse
	var err error
	switch req.Provider {
	case mailbox.ProviderGoogle:
		r, err = t.post(ctx, t.Endpoints.GoogleToken, url.Values{
			"grant_type": {"refresh_token"}, "refresh_token": {req.RefreshToken},
			"client_id": {t.Google.ID}, "client_secret": {t.Google.Secret},
		})
	case mailbox.ProviderMicrosoft:
		r, err = t.post(ctx, t.Endpoints.MicrosoftLogin+"/common/oauth2/v2.0/token", url.Values{
			"grant_type": {"refresh_token"}, "refresh_token": {req.RefreshToken}, "scope": {MicrosoftUserScopes},
			"client_id": {t.Microsoft.ID}, "client_secret": {t.Microsoft.Secret},
		})
	default:
		return "", "", fmt.Errorf("OAuth is not supported for provider %q", req.Provider)
	}
	if err != nil {
		return "", "", err
	}
	t.store(key, r)
	rotated := ""
	if r.RefreshToken != "" && r.RefreshToken != req.RefreshToken {
		rotated = r.RefreshToken
	}
	return r.AccessToken, rotated, nil
}

// Connection secrets, sealed in oauth_connections.secret.
type connectionSecret struct {
	// Google: the service account key file.
	ServiceAccount *ServiceAccount `json:"service_account,omitempty"`
	// Microsoft: the Entra app.
	TenantID     string `json:"tenant_id,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
}

// ServiceAccount is the part of a Google service account key file used.
type ServiceAccount struct {
	Type        string `json:"type"`
	ClientEmail string `json:"client_email"`
	ClientID    string `json:"client_id"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

// ParseServiceAccount reads a Google service account key file.
func ParseServiceAccount(b []byte) (*ServiceAccount, error) {
	var sa ServiceAccount
	if err := json.Unmarshal(b, &sa); err != nil {
		return nil, errors.New("not a JSON service account key file")
	}
	if sa.Type != "service_account" || sa.ClientEmail == "" || sa.PrivateKey == "" {
		return nil, errors.New("the key file must be a service account key with client_email and private_key")
	}
	if _, err := sa.key(); err != nil {
		return nil, err
	}
	return &sa, nil
}

func (sa *ServiceAccount) key() (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return nil, errors.New("the service account private_key is not PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		if k1, err1 := x509.ParsePKCS1PrivateKey(block.Bytes); err1 == nil {
			return k1, nil
		}
		return nil, fmt.Errorf("the service account private_key cannot be read: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("the service account private_key is not an RSA key")
	}
	return rk, nil
}

// SealConnection encrypts a connection's secret, bound to its id.
func SealConnection(box *secrets.Box, id string, sa *ServiceAccount, tenantID, clientID, clientSecret string) ([]byte, error) {
	b, err := json.Marshal(connectionSecret{ServiceAccount: sa, TenantID: tenantID, ClientID: clientID, ClientSecret: clientSecret})
	if err != nil {
		return nil, err
	}
	return box.Seal(b, []byte(id)), nil
}

func (t *Tokens) connectionToken(ctx context.Context, ws, connID, email string) (string, error) {
	var provider string
	var sealed []byte
	err := t.Pool.QueryRow(ctx, `SELECT provider, secret FROM oauth_connections WHERE id = $1 AND workspace_id = $2`, connID, ws).Scan(&provider, &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errors.New("the mailbox's OAuth connection was removed")
	}
	if err != nil {
		return "", err
	}
	plain, err := t.Box.Open(sealed, []byte(connID))
	if err != nil {
		return "", fmt.Errorf("decrypt OAuth connection %s: %w", connID, err)
	}
	var sec connectionSecret
	if err := json.Unmarshal(plain, &sec); err != nil {
		return "", err
	}
	switch provider {
	case mailbox.ProviderGoogle:
		return t.ServiceAccountToken(ctx, sec.ServiceAccount, email)
	case mailbox.ProviderMicrosoft:
		return t.AppToken(ctx, sec.TenantID, sec.ClientID, sec.ClientSecret)
	}
	return "", fmt.Errorf("unknown provider %q", provider)
}

// ServiceAccountToken signs in as one user through domain-wide delegation.
func (t *Tokens) ServiceAccountToken(ctx context.Context, sa *ServiceAccount, email string) (string, error) {
	if sa == nil {
		return "", errors.New("connection has no service account key")
	}
	key := "sa:" + sa.ClientEmail + ":" + strings.ToLower(email)
	if tok, ok := t.cached(key); ok {
		return tok, nil
	}
	aud := t.Endpoints.GoogleToken
	assertion, err := sa.assertion(email, aud, time.Now())
	if err != nil {
		return "", err
	}
	r, err := t.post(ctx, aud, url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}})
	if err != nil {
		return "", err
	}
	t.store(key, r)
	return r.AccessToken, nil
}

// assertion is the signed JWT a service account exchanges for a token
// acting as sub (RFC 7523).
func (sa *ServiceAccount) assertion(sub, aud string, now time.Time) (string, error) {
	k, err := sa.key()
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"iss": sa.ClientEmail, "sub": sub, "scope": GoogleMailScope, "aud": aud,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})
	signing := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// AppToken gets an application token for every mailbox in a Microsoft
// tenant (client credentials grant).
func (t *Tokens) AppToken(ctx context.Context, tenant, clientID, secret string) (string, error) {
	key := "app:" + tenant + ":" + clientID
	if tok, ok := t.cached(key); ok {
		return tok, nil
	}
	r, err := t.post(ctx, t.Endpoints.MicrosoftLogin+"/"+url.PathEscape(tenant)+"/oauth2/v2.0/token", url.Values{
		"grant_type": {"client_credentials"}, "client_id": {clientID}, "client_secret": {secret}, "scope": {MicrosoftAppScope},
	})
	if err != nil {
		return "", err
	}
	t.store(key, r)
	return r.AccessToken, nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

// Error is a refusal from the provider's token endpoint.
type Error struct {
	Code, Description string
}

func (e *Error) Error() string {
	if e.Description != "" {
		// Microsoft descriptions end with trace and correlation ids.
		d, _, _ := strings.Cut(e.Description, "\r\n")
		d, _, _ = strings.Cut(d, " Trace ID:")
		return "OAuth sign-in refused: " + e.Code + ": " + d
	}
	return "OAuth sign-in refused: " + e.Code
}

// Permanent reports whether the provider refused the grant itself, so
// asking again gives the same answer until the person signs in again.
func (e *Error) Permanent() bool {
	switch e.Code {
	case "invalid_grant", "unauthorized_client", "invalid_client", "access_denied", "consent_required", "interaction_required":
		return true
	}
	return false
}

func (t *Tokens) post(ctx context.Context, endpoint string, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := t.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("OAuth token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var r tokenResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("OAuth token endpoint answered %d with no JSON", resp.StatusCode)
	}
	if r.Error != "" || resp.StatusCode != http.StatusOK || r.AccessToken == "" {
		if r.Error == "" {
			r.Error = resp.Status
		}
		return nil, &Error{r.Error, r.Description}
	}
	return &r, nil
}
