// Package oauth gets access tokens for mailboxes that sign in to SMTP and
// IMAP with XOAUTH2 instead of a password, from the connection each one
// signs in through (the google_mail and outlook_mail connectors), and
// signs people in to Outflare itself with Google (flow.go).
package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/mailbox"
	"github.com/outflareai/outflare/engine/internal/secrets"
)

// Endpoints are Google's OAuth URLs for signing in to Outflare. Tests
// point them at a fake.
type Endpoints struct {
	GoogleAuth  string
	GoogleToken string
	GoogleMail  string // Gmail API base; empty uses the provider default
}

// DefaultEndpoints are the real provider.
var DefaultEndpoints = Endpoints{
	GoogleAuth:  "https://accounts.google.com/o/oauth2/v2/auth",
	GoogleToken: "https://oauth2.googleapis.com/token",
}

// Client is an OAuth app registered with a provider. Empty means it is
// not configured.
type Client struct {
	ID, Secret string
}

// Tokens signs mailboxes in through their connections, and people in to
// Outflare with Google.
type Tokens struct {
	Box       *secrets.Box
	Endpoints Endpoints
	// Google is the app behind "Continue with Google".
	Google Client
	HTTP   *http.Client
	// Integrations opens the connections mailboxes sign in through (the
	// google_mail and outlook_mail connectors). Nil until the connectors
	// are set up.
	Integrations *connectors.Store
}

func (t *Tokens) http() *http.Client {
	if t.HTTP != nil {
		return t.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// Token implements mailbox.TokenSource: it signs a mailbox in through its
// connection. Opening the connection refreshes a sign-in's token and
// stores the new one.
func (t *Tokens) Token(ctx context.Context, req mailbox.TokenRequest) (string, error) {
	if req.IntegrationID == nil {
		return "", lost{errors.New("this mailbox has no connection to sign in through; connect it again")}
	}
	if t.Integrations == nil {
		return "", errors.New("connectors are not configured on this server")
	}
	o, err := t.Integrations.Open(ctx, t.Integrations.Pool, req.WorkspaceID, *req.IntegrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		return "", lost{errors.New("the mailbox's connection was removed; connect the mailbox again")}
	}
	if err != nil {
		return "", signInErr(err)
	}
	ms, ok := o.Connector.(connectors.MailSignIn)
	if !ok {
		return "", fmt.Errorf("the %s connection cannot sign mailboxes in", o.Integration.Connector)
	}
	tok, err := ms.MailToken(ctx, o.Client, o.Integration.AuthMethod, o.Creds, req.Email)
	return tok, signInErr(err)
}

// signInErr marks a refusal of the connection's credentials as permanent
// (mailbox.SignInLost): trying again won't help until someone connects it
// again. Outages and timeouts stay temporary.
func signInErr(err error) error {
	if err != nil && connectors.KindOf(err) == connectors.ErrAuth {
		return lost{err}
	}
	return err
}

// lost is a sign-in refused for good.
type lost struct{ err error }

func (e lost) Error() string { return e.err.Error() }
func (e lost) Unwrap() error { return e.err }
func (lost) Permanent() bool { return true }

type tokenResponse struct {
	Scope        string `json:"scope"`
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
		return "OAuth sign-in refused: " + e.Code + ": " + e.Description
	}
	return "OAuth sign-in refused: " + e.Code
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
