package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

const GoogleSendMailScope = "https://www.googleapis.com/auth/gmail.send"

var ErrSalesConsent = errors.New("Google Sales send and receive consent is required")

// Sales consent is separate from read-only personal CRM capture. A stored
// read-only grant is never upgraded or used to send mail implicitly.
func (t *Tokens) SalesGoogleURL(redirect, state, nonce, verifier, email string) string {
	u, _ := url.Parse(t.ReadonlyGoogleURL(redirect, state, nonce, verifier, email))
	q := u.Query()
	q.Set("scope", "openid email "+GoogleReadMailScope+" "+GoogleSendMailScope)
	u.RawQuery = q.Encode()
	return u.String()
}
func salesScopes(scope string) bool {
	all := strings.Fields(scope)
	if !slices.Contains(all, GoogleReadMailScope) || !slices.Contains(all, GoogleSendMailScope) {
		return false
	}
	return readonlyScopes(strings.Join(slices.DeleteFunc(all, func(s string) bool { return s == GoogleSendMailScope }), " "))
}
func (t *Tokens) ExchangeSalesGoogle(ctx context.Context, code, redirect, nonce, verifier string) (PersonalGoogleGrant, error) {
	return t.exchangePersonalGoogle(ctx, code, redirect, nonce, verifier, salesScopes)
}
func (t *Tokens) RefreshSalesGoogle(ctx context.Context, refresh string) (string, string, error) {
	r, err := t.post(ctx, t.Endpoints.GoogleToken, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {t.Google.ID}, "client_secret": {t.Google.Secret}})
	if err != nil {
		return "", "", err
	}
	if r.Scope != "" && !salesScopes(r.Scope) {
		return "", "", ErrSalesConsent
	}
	return r.AccessToken, r.RefreshToken, nil
}

// GoogleSendRejected is a definitive validation/permission refusal. Transport
// errors and server errors are ambiguous and must be reconciled, never replayed.
type GoogleSendRejected struct{ Status int }

func (e GoogleSendRejected) Error() string {
	return fmt.Sprintf("Google refused Sales send (%d)", e.Status)
}
func (t *Tokens) SendSalesGoogle(ctx context.Context, token, raw, thread string) (string, string, error) {
	base := t.Endpoints.GoogleMail
	if base == "" {
		base = "https://gmail.googleapis.com/gmail/v1"
	}
	payload := map[string]string{"raw": raw}
	if thread != "" {
		payload["threadId"] = thread
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(base, "/")+"/users/me/messages/send", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := t.http().Do(req)
	if err != nil {
		return "", "", errors.New("Google send result is uncertain")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		if res.StatusCode >= 400 && res.StatusCode < 500 && res.StatusCode != 408 {
			return "", "", GoogleSendRejected{res.StatusCode}
		}
		return "", "", errors.New("Google send result is uncertain")
	}
	var out struct {
		ID     string `json:"id"`
		Thread string `json:"threadId"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out) != nil || out.ID == "" {
		return "", "", errors.New("Google send receipt is uncertain")
	}
	return out.ID, out.Thread, nil
}
