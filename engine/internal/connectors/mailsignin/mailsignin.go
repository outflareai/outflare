// Package mailsignin is the Gmail and Outlook connectors: connections that
// sign mailboxes in to SMTP and IMAP with OAuth (XOAUTH2) instead of a
// password. Each connector connects two ways:
//
//   - oauth2: one person signs in to their own mailbox on the provider's
//     consent screen. The connection holds that mailbox's refresh token.
//   - api_key: an admin connection for a whole organisation, a Google
//     service account with domain-wide delegation or a Microsoft Entra app
//     with application access to Exchange Online. It signs in any mailbox
//     of the organisation, so mailboxes are added to it by address.
//
// The OAuth apps are the operator's (GOOGLE_OAUTH_CLIENT_ID,
// MICROSOFT_OAUTH_CLIENT_ID or CONNECTOR_<ID>_CLIENT_ID); the tokens and
// keys belong to the workspace's own accounts.
package mailsignin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Connector ids.
const (
	GoogleID    = "google_mail"
	MicrosoftID = "outlook_mail"
)

// Scopes.
const (
	googleMailScope = "https://mail.google.com/"
	// Application access to Exchange Online IMAP and SMTP.
	microsoftAppScope = "https://outlook.office365.com/.default"
)

// All returns the mail sign-in connectors.
func All() []c.Connector { return []c.Connector{Google{}, Microsoft{}} }

// Google signs in Gmail and Google Workspace mailboxes.
type Google struct{}

func (Google) Definition() c.Definition {
	return c.Definition{
		ID: GoogleID, Name: "Gmail", Category: c.CategoryMailbox,
		Summary: "Add Gmail and Google Workspace mailboxes without passwords.",
		Description: "Sign in to one mailbox, or connect a service account with domain-wide delegation for https://mail.google.com/ " +
			"to add any mailbox of your Google Workspace by its address. Mailboxes send and read mail over SMTP and IMAP with OAuth.",
		Website: "https://workspace.google.com/products/gmail/", DocsURL: "https://developers.google.com/gmail/imap/xoauth2-protocol",
		KeyURL: "https://console.cloud.google.com/iam-admin/serviceaccounts",
		Auth:   c.AuthOAuth2, AuthMethods: c.Both,
		Fields: []c.Field{{
			Name: "service_account_json", Label: "Service account key (JSON)", Secret: true,
			Placeholder: `{"type": "service_account", ...}`,
			Help: "In Google Cloud: IAM & Admin, Service accounts, Keys, Add key, JSON. In the Google Admin console, give the service account's client id " +
				"domain-wide delegation for https://mail.google.com/.",
		}},
		OAuth: &c.OAuth{
			AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL: "https://oauth2.googleapis.com/token",
			Scopes:   []string{"openid", "email", googleMailScope},
			// Always return a refresh token.
			Params:  map[string]string{"access_type": "offline", "prompt": "consent"},
			IDToken: true,
		},
		Capabilities: []string{c.CapMailboxSignIn}, Pricing: "Free",
	}
}

func (Google) MailProvider() string { return "google" }

func (Google) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	if sa := creds["service_account_email"]; sa != "" {
		// The store already got a token for the key, which proves it works.
		return c.Account{ID: sa, Name: sa, Details: map[string]string{"kind": "service_account"}}, nil
	}
	id, err := idClaims(creds["id_token"])
	if err != nil {
		return c.Account{}, err
	}
	if !strings.Contains(id.Email, "@") {
		return c.Account{}, c.Errf(c.ErrAuth, "Google did not say which mailbox was approved; sign in again")
	}
	if !id.EmailVerified {
		return c.Account{}, c.Errf(c.ErrAuth, "Google has not verified this account's email address")
	}
	if creds["refresh_token"] == "" {
		return c.Account{}, c.Errf(c.ErrAuth, "Google did not return a refresh token; remove Outflare's access in your Google account settings and sign in again")
	}
	email := strings.ToLower(id.Email)
	return c.Account{ID: email, Name: id.Name, Email: email}, nil
}

func (Google) MailToken(ctx context.Context, cl *c.Client, method string, creds c.Creds, email string) (string, error) {
	if method == c.AuthAPIKey {
		t, err := c.ServiceAccountToken(ctx, cl, creds["service_account_json"], []string{googleMailScope}, email)
		return t.AccessToken, err
	}
	return oneMailbox(creds, email)
}

// Microsoft signs in Outlook.com and Microsoft 365 mailboxes.
type Microsoft struct{}

func (Microsoft) Definition() c.Definition {
	return c.Definition{
		ID: MicrosoftID, Name: "Outlook", Category: c.CategoryMailbox,
		Summary: "Add Outlook and Microsoft 365 mailboxes without passwords.",
		Description: "Sign in to one mailbox, or connect an Entra app with the Exchange Online application permissions IMAP.AccessAsApp " +
			"and SMTP.SendAsApp to add any mailbox of your tenant by its address. Mailboxes send and read mail over SMTP and IMAP with OAuth.",
		Website: "https://www.microsoft.com/microsoft-365/outlook", DocsURL: "https://learn.microsoft.com/exchange/client-developer/legacy-protocols/how-to-authenticate-an-imap-pop-smtp-application-by-using-oauth",
		KeyURL: "https://entra.microsoft.com/#view/Microsoft_AAD_RegisteredApps/ApplicationsListBlade",
		Auth:   c.AuthOAuth2, AuthMethods: c.Both,
		Fields: []c.Field{
			{Name: "tenant_id", Label: "Directory (tenant) id"},
			{Name: "client_id", Label: "Application (client) id"},
			{Name: "client_secret", Label: "Client secret", Secret: true},
		},
		OAuth: &c.OAuth{
			AuthURL:  "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
			TokenURL: "https://login.microsoftonline.com/common/oauth2/v2.0/token",
			Scopes:   []string{"offline_access", "openid", "email", "https://outlook.office.com/IMAP.AccessAsUser.All", "https://outlook.office.com/SMTP.Send"},
			Params:   map[string]string{"prompt": "select_account"},
			IDToken:  true,
		},
		Capabilities: []string{c.CapMailboxSignIn}, Pricing: "Free",
	}
}

func (Microsoft) MailProvider() string { return "microsoft" }

func (Microsoft) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	if tenant := strings.TrimSpace(creds["tenant_id"]); tenant != "" {
		// Getting a token proves the app and secret are right before any
		// mailbox depends on them.
		if _, err := appToken(ctx, cl, creds); err != nil {
			return c.Account{}, err
		}
		return c.Account{ID: tenant, Name: tenant, Details: map[string]string{"kind": "entra_app", "client_id": strings.TrimSpace(creds["client_id"])}}, nil
	}
	id, err := idClaims(creds["id_token"])
	if err != nil {
		return c.Account{}, err
	}
	email := id.Email
	if email == "" {
		email = id.PreferredUsername
	}
	if !strings.Contains(email, "@") {
		return c.Account{}, c.Errf(c.ErrAuth, "Microsoft did not say which mailbox was approved; sign in again")
	}
	if creds["refresh_token"] == "" {
		return c.Account{}, c.Errf(c.ErrAuth, "Microsoft did not return a refresh token; sign in again")
	}
	email = strings.ToLower(email)
	return c.Account{ID: email, Name: id.Name, Email: email}, nil
}

func (Microsoft) MailToken(ctx context.Context, cl *c.Client, method string, creds c.Creds, email string) (string, error) {
	if method == c.AuthAPIKey {
		return appToken(ctx, cl, creds)
	}
	return oneMailbox(creds, email)
}

// oneMailbox is a person's own sign-in: it only signs in their mailbox.
func oneMailbox(creds c.Creds, email string) (string, error) {
	if id, err := idClaims(creds["id_token"]); err == nil {
		signed := id.Email
		if signed == "" {
			signed = id.PreferredUsername
		}
		if signed != "" && !strings.EqualFold(signed, email) {
			return "", c.Errf(c.ErrInvalid, "this connection signs in %s, not %s", strings.ToLower(signed), email)
		}
	}
	if creds["access_token"] == "" {
		return "", c.Errf(c.ErrAuth, "the connection has no access token; sign in again")
	}
	return creds["access_token"], nil
}

type cached struct {
	token   string
	expires time.Time
}

var appTokens sync.Map // tenant + client id + secret -> cached

// appToken gets an application token for every mailbox in a Microsoft
// tenant (the client credentials grant), cached until shortly before it
// expires.
func appToken(ctx context.Context, cl *c.Client, creds c.Creds) (string, error) {
	tenant, id, secret := strings.TrimSpace(creds["tenant_id"]), strings.TrimSpace(creds["client_id"]), creds["client_secret"]
	if tenant == "" || id == "" || secret == "" {
		return "", c.Errf(c.ErrInvalid, "tenant_id, client_id and client_secret are required")
	}
	key := tenant + "\n" + id + "\n" + secret
	if t, ok := appTokens.Load(key); ok && time.Until(t.(cached).expires) > 5*time.Minute {
		return t.(cached).token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {id}, "client_secret": {secret}, "scope": {microsoftAppScope}}
	base := cl.Base(MicrosoftID+".login", "https://login.microsoftonline.com")
	req, _ := http.NewRequest(http.MethodPost, base+"/"+url.PathEscape(tenant)+"/oauth2/v2.0/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if _, err := c.DoJSON(ctx, cl, "Microsoft", req, &out); err != nil {
		if k := c.KindOf(err); k == c.ErrAuth || k == c.ErrInvalid {
			return "", c.Errf(c.ErrAuth, "Microsoft refused the Entra app: check the tenant id, client id and secret, and that the secret has not expired")
		}
		return "", err
	}
	if out.AccessToken == "" {
		return "", c.Errf(c.ErrAuth, "Microsoft did not return a token for the Entra app")
	}
	life := time.Duration(out.ExpiresIn) * time.Second
	if life <= 0 {
		life = time.Hour
	}
	appTokens.Store(key, cached{out.AccessToken, time.Now().Add(life)})
	return out.AccessToken, nil
}

type claims struct {
	Email             string `json:"email"`
	EmailVerified     bool   `json:"email_verified"`
	PreferredUsername string `json:"preferred_username"`
	Name              string `json:"name"`
}

// idClaims reads an ID token. It comes straight from the token endpoint
// over TLS, so its claims can be read without checking the signature
// (OpenID Connect Core 3.1.3.7).
func idClaims(idToken string) (claims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return claims{}, c.Errf(c.ErrAuth, "the provider returned no ID token; sign in again")
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	var out claims
	if err != nil || json.Unmarshal(b, &out) != nil {
		return claims{}, c.Errf(c.ErrAuth, "the provider returned an unreadable ID token; sign in again")
	}
	return out, nil
}
