// Package workspace connects a company's whole Google Workspace with its
// own service account key and domain-wide delegation, so the CRM can read
// the work mail of the people an admin enables without each of them
// signing in. The key is the company's own (bring your own keys) and the
// scopes are read-only.
package workspace

import (
	"context"
	"net/http"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// ID is the connector's id.
const ID = "google_workspace"

// Scopes are what the service account must be allowed in the Google
// Admin console's domain-wide delegation.
const (
	MailScope     = "https://www.googleapis.com/auth/gmail.readonly"
	SendScope     = "https://www.googleapis.com/auth/gmail.send"
	CalendarScope = "https://www.googleapis.com/auth/calendar.readonly"
)

// Scopes are what the connection may be allowed, in the order it reports
// them; reading mail is required, the others are optional.
var Scopes = []string{MailScope, SendScope, CalendarScope}

// CapCRMMail: the connection reads its Workspace members' mail into the
// CRM (Token).
const CapCRMMail = "crm.mail_workspace"

const gmailAPI = "https://gmail.googleapis.com/gmail/v1"

// Connector is the Google Workspace connection.
type Connector struct{}

func (Connector) Definition() c.Definition {
	return c.Definition{
		ID: ID, Name: "Google Workspace", Category: c.CategoryCRM,
		Summary:     "Read your team's work email into the CRM with your Workspace's own service account, without each person signing in.",
		Description: "Connect a service account key from your own Google Cloud project with domain-wide delegation for " + MailScope + ", and " + CalendarScope + " to read calendars and " + SendScope + " to send as a person. An admin then picks which people's mail the CRM reads (CRM email settings), under the same filters and privacy rules as Gmail people connect themselves. Nothing is sent unless a person sends it, and nothing is changed in Google.",
		Website:     "https://workspace.google.com", DocsURL: "https://support.google.com/a/answer/162106",
		KeyURL: "https://console.cloud.google.com/iam-admin/serviceaccounts",
		Auth:   c.AuthAPIKey,
		Fields: []c.Field{
			{
				Name: "service_account_json", Label: "Service account key (JSON)", Secret: true,
				Placeholder: `{"type": "service_account", ...}`,
				Help: "In Google Cloud: IAM & Admin, Service accounts, Keys, Add key, JSON. In the Google Admin console (Security, API controls, Domain-wide delegation), " +
					"add the service account's client id with " + MailScope + ", plus " + CalendarScope + " to read calendars and " + SendScope + " to send as a person.",
			},
			{
				Name: "admin_email", Label: "A Workspace user to test with",
				Placeholder: "you@yourcompany.example",
				Help:        "Any address in your Workspace. Outflare checks it can read that mailbox's profile, nothing else.",
			},
		},
		Capabilities: []string{CapCRMMail}, Pricing: "Free",
	}
}

// Test signs in as the test user through domain-wide delegation and reads
// only their mailbox's address.
func (Connector) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	user := strings.ToLower(strings.TrimSpace(creds["admin_email"]))
	if !strings.Contains(user, "@") {
		return c.Account{}, c.Errf(c.ErrInvalid, "enter an address in your Google Workspace to test with")
	}
	tok, err := Token(ctx, cl, creds, user)
	if err != nil {
		return c.Account{}, err
	}
	req, _ := http.NewRequest(http.MethodGet, cl.Base("gmail", gmailAPI)+"/users/me/profile", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	var profile struct {
		Email string `json:"emailAddress"`
	}
	if _, err := c.DoJSON(ctx, cl, "Gmail", req, &profile); err != nil {
		return c.Account{}, err
	}
	_, domain, _ := strings.Cut(strings.ToLower(profile.Email), "@")
	key, _ := c.ServiceAccountToken(ctx, cl, creds["service_account_json"], []string{MailScope}, user)
	// Which of the optional scopes Google lets it use, so modules offer
	// only what the delegation allows (sending, calendars).
	granted := []string{MailScope}
	for _, scope := range Scopes[1:] {
		if _, err := c.ServiceAccountToken(ctx, cl, creds["service_account_json"], []string{scope}, user); err == nil {
			granted = append(granted, scope)
		}
	}
	return c.Account{ID: key.ClientEmail, Name: domain, Details: map[string]string{"domain": domain, "service_account": key.ClientEmail, "scopes": strings.Join(granted, " ")}}, nil
}

// Token is a read-only Gmail access token for one Workspace user.
func Token(ctx context.Context, cl *c.Client, creds c.Creds, user string) (string, error) {
	t, err := c.ServiceAccountToken(ctx, cl, creds["service_account_json"], []string{MailScope}, user)
	return t.AccessToken, err
}

// SendToken is a token that may only send mail as one Workspace user.
func SendToken(ctx context.Context, cl *c.Client, creds c.Creds, user string) (string, error) {
	t, err := c.ServiceAccountToken(ctx, cl, creds["service_account_json"], []string{SendScope}, user)
	return t.AccessToken, err
}

// Allows reports whether a connection's account says Google lets it use
// scope (read when it was connected or last checked).
func Allows(account c.Account, scope string) bool {
	for _, s := range strings.Fields(account.Details["scopes"]) {
		if s == scope {
			return true
		}
	}
	return false
}

// CalendarToken is a read-only Calendar access token for one Workspace user.
func CalendarToken(ctx context.Context, cl *c.Client, creds c.Creds, user string) (string, error) {
	t, err := c.ServiceAccountToken(ctx, cl, creds["service_account_json"], []string{CalendarScope}, user)
	return t.AccessToken, err
}
