// Package inboxkit connects a customer's InboxKit workspace. Its mailbox
// inventory feeds the existing supplier import and synchronization jobs.
// API reference: https://docs.inboxkit.com/list-mailboxes-18118368e0.
package inboxkit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

const ID = "inboxkit"

type Connector struct{}

var _ c.MailboxVendor = Connector{}

func (Connector) Definition() c.Definition {
	return c.Definition{
		ID: ID, Name: "InboxKit", Category: c.CategoryMailbox, Auth: c.AuthAPIKey,
		Summary: "Bring your InboxKit mailboxes into Outflare and keep their status in sync.",
		Description: "Connect your own InboxKit account and choose its workspace. Select existing mailboxes to import, " +
			"or enable automatic import. InboxKit bills your account directly. Import uses the supplier's app passwords " +
			"for Google mailboxes; accounts still being configured remain pending.",
		Website: "https://www.inboxkit.com", DocsURL: "https://docs.inboxkit.com",
		KeyURL: "https://app.inboxkit.com/settings/api",
		Fields: []c.Field{
			{Name: "api_key", Label: "API key", Secret: true, Help: "InboxKit Settings → API & MCP → API Keys."},
			{Name: "workspace_id", Label: "InboxKit workspace ID", Help: "The UID from InboxKit's List workspaces API. Connect each InboxKit workspace separately."},
		},
		Capabilities: []string{c.CapVendorMailboxes, c.CapImportMailboxes},
		Pricing:      "You pay InboxKit directly",
	}
}

// call never retries. Reads may be retried by their owning synchronization
// job; future paid operations must reconcile uncertain outcomes first.
// Provider response text can contain passwords or reflected input, so it is
// deliberately not included in errors returned to logs or callers.
func call(ctx context.Context, cl *c.Client, cr c.Creds, method, path string, input, output any) error {
	if strings.TrimSpace(cr["api_key"]) == "" || strings.TrimSpace(cr["workspace_id"]) == "" {
		return c.Errf(c.ErrAuth, "add your InboxKit API key and workspace ID")
	}
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, cl.Base(ID, "https://api.inboxkit.com")+path, body)
	if err != nil {
		return c.Errf(c.ErrInvalid, "invalid InboxKit request")
	}
	req.Header.Set("Authorization", "Bearer "+cr["api_key"])
	req.Header.Set("X-Workspace-Id", cr["workspace_id"])
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := cl.Do(req)
	if err != nil {
		return c.Errf(c.ErrUnavailable, "could not reach InboxKit")
	}
	defer resp.Body.Close()
	const maxResponse = 8 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil || len(raw) > maxResponse {
		return c.Errf(c.ErrUnavailable, "could not read the InboxKit response")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return c.HTTPError("InboxKit", resp.StatusCode, "")
	}
	var envelope struct {
		Error bool `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return c.Errf(c.ErrUnavailable, "InboxKit returned an invalid response")
	}
	if envelope.Error {
		return c.Errf(c.ErrInvalid, "InboxKit could not complete the request; check the supplier account")
	}
	if output != nil && json.Unmarshal(raw, output) != nil {
		return c.Errf(c.ErrUnavailable, "InboxKit returned an invalid response")
	}
	return nil
}

// Test checks both team access and membership of the chosen workspace. An
// account-level read alone would accept another team's workspace ID.
func (Connector) Test(ctx context.Context, cl *c.Client, cr c.Creds) (c.Account, error) {
	var account struct {
		UID        string `json:"uid"`
		Team       string `json:"team"`
		AdminEmail string `json:"admin_email"`
	}
	if err := call(ctx, cl, cr, http.MethodGet, "/v1/api/account", nil, &account); err != nil {
		return c.Account{}, err
	}
	if account.UID == "" {
		return c.Account{}, c.Errf(c.ErrUnavailable, "InboxKit returned an account without an ID")
	}
	var list struct {
		Workspaces []struct {
			UID  string `json:"uid"`
			Name string `json:"name"`
		} `json:"workspaces"`
	}
	if err := call(ctx, cl, cr, http.MethodGet, "/v1/api/workspaces/list", nil, &list); err != nil {
		return c.Account{}, err
	}
	for _, ws := range list.Workspaces {
		if ws.UID == cr["workspace_id"] {
			return c.Account{ID: account.UID, Name: account.Team, Email: account.AdminEmail,
				Details: map[string]string{"workspace_id": ws.UID, "workspace_name": ws.Name}}, nil
		}
	}
	return c.Account{}, c.Errf(c.ErrAuth, "the InboxKit workspace does not belong to this connected account")
}

type mailbox struct {
	UID        string     `json:"uid"`
	Email      string     `json:"email"`
	Username   string     `json:"username"`
	DomainName string     `json:"domain_name"`
	Domain     string     `json:"domain"`
	FirstName  string     `json:"first_name"`
	LastName   string     `json:"last_name"`
	Platform   string     `json:"platform"`
	Status     string     `json:"status"`
	Tags       []string   `json:"tags"`
	CreatedAt  *time.Time `json:"createdAt"`
}

func (m mailbox) neutral() (c.VendorMailbox, error) {
	email := strings.ToLower(strings.TrimSpace(m.Email))
	if email == "" {
		domain := m.DomainName
		if domain == "" {
			domain = m.Domain
		}
		email = strings.ToLower(strings.TrimSpace(m.Username) + "@" + strings.TrimSpace(domain))
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email || m.UID == "" {
		return c.VendorMailbox{}, c.Errf(c.ErrUnavailable, "InboxKit returned a mailbox without a valid address or ID")
	}
	provider := "other"
	switch strings.ToUpper(m.Platform) {
	case "GOOGLE":
		provider = "google"
	case "MICROSOFT", "MS_OUTLOOK", "AZURE":
		provider = "microsoft"
	}
	status := c.VendorProvisioning
	switch strings.ToLower(m.Status) {
	case "active":
		status = c.VendorActive
	case "scheduled_for_cancellation":
		status = c.VendorCancelling
	case "cancelled", "canceled", "deleted", "archived", "suspended", "inactive":
		status = c.VendorCancelled
	case "failed", "renewal_failed":
		status = c.VendorFailed
	}
	tags := m.Tags
	if tags == nil {
		tags = []string{}
	}
	return c.VendorMailbox{ExternalID: m.UID, Email: email, FirstName: m.FirstName, LastName: m.LastName,
		Provider: provider, Status: status, VendorStatus: m.Status, Tags: tags, ConnectedTo: []string{},
		CreatedAt: m.CreatedAt, AppPassword: c.AppPasswordNone}, nil
}

func (Connector) ListMailboxes(ctx context.Context, cl *c.Client, cr c.Creds, p c.VendorPage) ([]c.VendorMailbox, string, error) {
	page := 1
	if p.Cursor != "" {
		n, err := strconv.Atoi(p.Cursor)
		if err != nil || n < 1 || n > 1_000_000 {
			return nil, "", c.Errf(c.ErrInvalid, "invalid InboxKit mailbox page")
		}
		page = n
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 100 {
		limit = 100
	}
	var result struct {
		Mailboxes   []mailbox `json:"mailboxes"`
		Total       *int      `json:"total"`
		Pages       *int      `json:"pages"`
		CurrentPage int       `json:"current_page"`
		Limit       int       `json:"limit"`
	}
	if err := call(ctx, cl, cr, http.MethodPost, "/v1/api/mailboxes/list", map[string]int{"page": page, "limit": limit}, &result); err != nil {
		return nil, "", err
	}
	if result.Mailboxes == nil || (result.CurrentPage != 0 && result.CurrentPage != page) {
		return nil, "", c.Errf(c.ErrUnavailable, "InboxKit returned an incomplete mailbox page")
	}
	if (result.Total != nil && *result.Total < 0) || (result.Pages != nil && *result.Pages < 0) || result.Limit < 0 {
		return nil, "", c.Errf(c.ErrUnavailable, "InboxKit returned invalid mailbox pagination")
	}
	out := make([]c.VendorMailbox, 0, len(result.Mailboxes))
	for _, m := range result.Mailboxes {
		v, err := m.neutral()
		if err != nil {
			return nil, "", err
		}
		out = append(out, v)
	}
	// Prefer the provider's page count: its effective page size may differ
	// from our request. Without metadata, a full page merits one more read.
	more := len(out) == limit
	if result.Pages != nil {
		more = page < *result.Pages
	} else if result.Total != nil {
		effective := result.Limit
		if effective <= 0 {
			effective = limit
		}
		more = page*effective < *result.Total
	}
	if more && len(out) == 0 {
		return nil, "", c.Errf(c.ErrUnavailable, "InboxKit returned an empty page before the end of its inventory")
	}
	next := ""
	if more {
		next = strconv.Itoa(page + 1)
	}
	return out, next, nil
}

func (Connector) GetMailbox(ctx context.Context, cl *c.Client, cr c.Creds, id string) (c.VendorMailbox, error) {
	if id == "" {
		return c.VendorMailbox{}, c.Errf(c.ErrInvalid, "mailbox ID is required")
	}
	var result struct {
		Mailbox mailbox `json:"mailbox"`
	}
	if err := call(ctx, cl, cr, http.MethodGet, "/v1/api/mailboxes/details?uid="+url.QueryEscape(id), nil, &result); err != nil {
		return c.VendorMailbox{}, err
	}
	if result.Mailbox.UID != id {
		return c.VendorMailbox{}, c.Errf(c.ErrUnavailable, "InboxKit returned another mailbox")
	}
	return result.Mailbox.neutral()
}

func (ci Connector) MailboxCredentials(ctx context.Context, cl *c.Client, cr c.Creds, id string) (c.VendorCredentials, error) {
	m, err := ci.GetMailbox(ctx, cl, cr, id)
	if err != nil {
		return c.VendorCredentials{}, err
	}
	if m.Status != c.VendorActive && m.Status != c.VendorCancelling {
		return c.VendorCredentials{}, c.Errf(c.ErrInvalid, "this InboxKit mailbox is not ready to connect")
	}
	var result struct {
		Password    string `json:"password"`
		AppPassword string `json:"app_password"`
	}
	if err := call(ctx, cl, cr, http.MethodGet, "/v1/api/mailboxes/show-credentials?uid="+url.QueryEscape(id), nil, &result); err != nil {
		return c.VendorCredentials{}, err
	}
	// A Google login password is not an SMTP/IMAP app password. Import must
	// wait rather than repeatedly attempting the wrong authentication.
	if (m.Provider == "google" && result.AppPassword == "") || (result.Password == "" && result.AppPassword == "") {
		return c.VendorCredentials{}, c.Errf(c.ErrInvalid, "InboxKit has not provided the mailbox sign-in credentials required for import")
	}
	return c.VendorCredentials{Email: m.Email, Username: m.Email, Password: result.Password, AppPassword: result.AppPassword}, nil
}
