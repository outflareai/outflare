package oauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/outflareai/outflare/engine/internal/mailbox"
)

// Configured reports whether the consent flow is set up for a provider.
func (t *Tokens) Configured(provider string) bool {
	switch provider {
	case mailbox.ProviderGoogle:
		return t.Google.ID != ""
	case mailbox.ProviderMicrosoft:
		return t.Microsoft.ID != ""
	}
	return false
}

// AuthURL is where to send a person to approve access to their mailbox.
func (t *Tokens) AuthURL(provider, redirectURI, state string) string {
	q := url.Values{"response_type": {"code"}, "redirect_uri": {redirectURI}, "state": {state}}
	switch provider {
	case mailbox.ProviderGoogle:
		q.Set("client_id", t.Google.ID)
		q.Set("scope", "openid email "+GoogleMailScope)
		q.Set("access_type", "offline")
		q.Set("prompt", "consent") // always return a refresh token
		return t.Endpoints.GoogleAuth + "?" + q.Encode()
	default:
		q.Set("client_id", t.Microsoft.ID)
		q.Set("scope", MicrosoftUserScopes)
		q.Set("prompt", "select_account")
		return t.Endpoints.MicrosoftLogin + "/common/oauth2/v2.0/authorize?" + q.Encode()
	}
}

// Grant is the result of an approved consent screen.
type Grant struct {
	Email        string
	Name         string
	RefreshToken string
}

// Exchange trades an authorisation code for a refresh token and the
// mailbox's address.
func (t *Tokens) Exchange(ctx context.Context, provider, code, redirectURI string) (*Grant, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI}}
	endpoint := t.Endpoints.GoogleToken
	if provider == mailbox.ProviderGoogle {
		form.Set("client_id", t.Google.ID)
		form.Set("client_secret", t.Google.Secret)
	} else {
		endpoint = t.Endpoints.MicrosoftLogin + "/common/oauth2/v2.0/token"
		form.Set("client_id", t.Microsoft.ID)
		form.Set("client_secret", t.Microsoft.Secret)
		form.Set("scope", MicrosoftUserScopes)
	}
	r, err := t.post(ctx, endpoint, form)
	if err != nil {
		return nil, err
	}
	if r.RefreshToken == "" {
		return nil, errors.New("the provider did not return a refresh token; remove Outflare's access in your account settings and connect again")
	}
	// The ID token comes straight from the token endpoint over TLS, so its
	// claims can be read without checking the signature (OpenID Connect
	// Core 3.1.3.7).
	claims, err := idClaims(r.IDToken)
	if err != nil {
		return nil, err
	}
	email := claims.Email
	if email == "" {
		email = claims.PreferredUsername
	}
	if !strings.Contains(email, "@") {
		return nil, errors.New("the provider did not say which mailbox was approved")
	}
	if provider == mailbox.ProviderGoogle && !claims.EmailVerified {
		return nil, errors.New("Google has not verified this account's email address")
	}
	return &Grant{Email: strings.ToLower(email), Name: claims.Name, RefreshToken: r.RefreshToken}, nil
}

type claims struct {
	Subject           string `json:"sub"`
	Email             string `json:"email"`
	EmailVerified     bool   `json:"email_verified"`
	PreferredUsername string `json:"preferred_username"`
	Name              string `json:"name"`
}

func idClaims(idToken string) (*claims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return nil, errors.New("the provider returned no ID token")
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("the provider returned an unreadable ID token")
	}
	var c claims
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, errors.New("the provider returned an unreadable ID token")
	}
	return &c, nil
}

// LoginURL is Google's sign-in screen for signing in to Outflare itself:
// only openid, email and profile, which need no Google app verification,
// unlike the Gmail scope that connecting a mailbox asks for.
func (t *Tokens) LoginURL(redirectURI, state string) string {
	q := url.Values{
		"response_type": {"code"}, "redirect_uri": {redirectURI}, "state": {state},
		"client_id": {t.Google.ID}, "scope": {"openid email profile"}, "prompt": {"select_account"},
	}
	return t.Endpoints.GoogleAuth + "?" + q.Encode()
}

// Identity is a person Google has signed in.
type Identity struct {
	Subject string // Google's stable account id
	Email   string
	Name    string
}

// ExchangeLogin trades a sign-in code for the Google account behind it. The
// email must be verified by Google.
func (t *Tokens) ExchangeLogin(ctx context.Context, code, redirectURI string) (*Identity, error) {
	r, err := t.post(ctx, t.Endpoints.GoogleToken, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI},
		"client_id": {t.Google.ID}, "client_secret": {t.Google.Secret},
	})
	if err != nil {
		return nil, err
	}
	c, err := idClaims(r.IDToken)
	if err != nil {
		return nil, err
	}
	if c.Subject == "" || !strings.Contains(c.Email, "@") {
		return nil, errors.New("Google did not say which account signed in")
	}
	if !c.EmailVerified {
		return nil, errors.New("Google has not verified this account's email address")
	}
	return &Identity{Subject: c.Subject, Email: strings.ToLower(c.Email), Name: c.Name}, nil
}
