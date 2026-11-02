package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

var ErrPersonalGoogleScope = errors.New("Google grant is not restricted to read-only mail")

const GoogleReadMailScope = "https://www.googleapis.com/auth/gmail.readonly"

// ReadonlyGoogleURL is deliberately separate from the SMTP/IMAP consent flow.
// It requests neither sending nor modifying/deleting messages.
func (t *Tokens) ReadonlyGoogleURL(redirect, state, nonce, verifier, email string) string {
	challenge := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {t.Google.ID}, "response_type": {"code"}, "redirect_uri": {redirect}, "state": {state}, "nonce": {nonce}, "scope": {"openid email " + GoogleReadMailScope}, "access_type": {"offline"}, "prompt": {"consent"}, "include_granted_scopes": {"false"}, "login_hint": {email}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}}
	return t.Endpoints.GoogleAuth + "?" + q.Encode()
}

type PersonalGoogleGrant struct {
	Email        string `json:"email"`
	Subject      string `json:"subject"`
	RefreshToken string `json:"refresh_token"`
	AccessToken  string `json:"-"`
}

func readonlyScopes(scope string) bool {
	scopes := strings.Fields(scope)
	if !slices.Contains(scopes, GoogleReadMailScope) {
		return false
	}
	for _, s := range scopes {
		if !slices.Contains([]string{"openid", "email", "profile", "https://www.googleapis.com/auth/userinfo.profile", "https://www.googleapis.com/auth/userinfo.email", GoogleReadMailScope}, s) {
			return false
		}
	}
	return true
}
func (t *Tokens) ExchangePersonalGoogle(ctx context.Context, code, redirect, nonce, verifier string) (PersonalGoogleGrant, error) {
	var g PersonalGoogleGrant
	r, err := t.post(ctx, t.Endpoints.GoogleToken, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "client_id": {t.Google.ID}, "client_secret": {t.Google.Secret}, "code_verifier": {verifier}})
	if err != nil {
		return g, err
	}
	if r.RefreshToken == "" || !readonlyScopes(r.Scope) {
		return g, errors.New("Google must grant only read-only Gmail access and a refresh token")
	}
	parts := strings.Split(r.IDToken, ".")
	if len(parts) != 3 {
		return g, errors.New("missing Google identity")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return g, errors.New("invalid Google identity")
	}
	var c struct {
		Subject  string `json:"sub"`
		Email    string `json:"email"`
		Verified bool   `json:"email_verified"`
		Nonce    string `json:"nonce"`
		Audience string `json:"aud"`
		Issuer   string `json:"iss"`
		Expires  int64  `json:"exp"`
	}
	// The ID token is obtained directly from the provider token endpoint over
	// TLS, as in ExchangeLogin; bind its identity to this PKCE/nonce flow.
	if json.Unmarshal(raw, &c) != nil || c.Subject == "" || !c.Verified || !strings.Contains(c.Email, "@") || c.Nonce != nonce || c.Audience != t.Google.ID || !slices.Contains([]string{"accounts.google.com", "https://accounts.google.com"}, c.Issuer) || c.Expires <= time.Now().Unix() {
		return g, errors.New("Google identity does not match this consent flow")
	}
	return PersonalGoogleGrant{Email: strings.ToLower(c.Email), Subject: c.Subject, RefreshToken: r.RefreshToken, AccessToken: r.AccessToken}, nil
}
func (t *Tokens) RefreshPersonalGoogle(ctx context.Context, refresh string) (string, string, error) {
	r, err := t.post(ctx, t.Endpoints.GoogleToken, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {t.Google.ID}, "client_secret": {t.Google.Secret}})
	if err != nil {
		return "", "", err
	}
	if r.Scope != "" && !readonlyScopes(r.Scope) {
		return "", "", ErrPersonalGoogleScope
	}
	return r.AccessToken, r.RefreshToken, nil
}

// ReadPersonalGoogle uses only this user's grant; never service accounts or
// domain-wide delegation. Responses are bounded and provider bodies are not
// copied into errors/logs.
func (t *Tokens) ReadPersonalGoogle(ctx context.Context, token, path string, q url.Values, out any) error {
	base := t.Endpoints.GoogleMail
	if base == "" {
		base = "https://gmail.googleapis.com/gmail/v1"
	}
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(base, "/")+"/users/me/"+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := t.http().Do(req)
	if err != nil {
		return errors.New("Google mail request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("Google mail request refused (%d)", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20+1))
	if err != nil || len(raw) > 4<<20 {
		return errors.New("Google mail response exceeded its limit")
	}
	if json.Unmarshal(raw, out) != nil {
		return errors.New("Google mail response was unreadable")
	}
	return nil
}
