package oauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

type claims struct {
	Subject           string `json:"sub"`
	Email             string `json:"email"`
	EmailVerified     bool   `json:"email_verified"`
	PreferredUsername string `json:"preferred_username"`
	Name              string `json:"name"`
}

// idClaims reads an ID token. It comes straight from the token endpoint
// over TLS, so its claims can be read without checking the signature
// (OpenID Connect Core 3.1.3.7).
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
