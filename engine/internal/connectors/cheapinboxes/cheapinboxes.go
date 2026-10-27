// Package cheapinboxes connects Cheap Inboxes (cheapinboxes.com), a
// supplier of Google Workspace and Microsoft 365 mailboxes. It implements
// connectors.MailboxVendor, AppPasswordVendor and WebhookVendor, and adds
// the ordering calls (search, quote, checkout, cancel) that the
// infrastructure planner uses.
//
// API reference: https://api.cheapinboxes.com/docs (OpenAPI at
// /v1/openapi.json). Every call is a Bearer ci_live_ key; the limit is 120
// requests a minute per key.
package cheapinboxes

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// ID is the connector id, also used as the mailbox source.
const ID = "cheapinboxes"

const name = "Cheap Inboxes"

// Connector is the Cheap Inboxes connector.
type Connector struct{}

var (
	_ c.AppPasswordVendor = Connector{}
	_ c.WebhookVendor     = Connector{}
)

func (Connector) Definition() c.Definition {
	return c.Definition{
		ID: ID, Name: name, Category: c.CategoryMailbox,
		Summary: "Import your Google and Microsoft mailboxes from Cheap Inboxes and keep them in sync.",
		Description: "Connect your Cheap Inboxes account with an API key. Outflare lists every mailbox, imports the ones you pick " +
			"(or all of them, automatically) with their passwords, checks SMTP, IMAP and DNS, and picks up new and cancelled " +
			"mailboxes on its own. Google mailboxes get an app password from Cheap Inboxes, since Google refuses plain passwords.",
		Website: "https://cheapinboxes.com", DocsURL: "https://api.cheapinboxes.com/docs",
		KeyURL: "https://app.cheapinboxes.com/integrations", Auth: c.AuthAPIKey,
		Fields: []c.Field{{
			Name: "api_key", Label: "API key", Secret: true, Placeholder: "ci_live_…",
			Help: "Cheap Inboxes → Integrations → API. One key covers your whole organization.",
		}},
		Capabilities: []string{c.CapVendorMailboxes, c.CapImportMailboxes},
		Pricing:      "You pay Cheap Inboxes directly",
	}
}

func base(cl *c.Client) string { return cl.Base(ID, "https://api.cheapinboxes.com") }

// call sends one request and decodes the JSON answer into out. It waits
// out a rate limit (429) up to three times, using X-RateLimit-Reset.
func call(ctx context.Context, cl *c.Client, cr c.Creds, method, path string, in, out any) error {
	return send(ctx, cl, cr, method, path, in, out, nil, 3)
}

// send is call with extra headers and a limit on rate-limit retries; 0
// sends the request exactly once.
func send(ctx context.Context, cl *c.Client, cr c.Creds, method, path string, in, out any, hdr http.Header, retries int) error {
	key := cr["api_key"]
	if key == "" {
		return c.Errf(c.ErrAuth, "the Cheap Inboxes API key is missing")
	}
	var payload []byte
	if in != nil {
		var err error
		if payload, err = json.Marshal(in); err != nil {
			return err
		}
	}
	for attempt := 0; ; attempt++ {
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, base(cl)+path, body)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Accept", "application/json")
		for k, v := range hdr {
			req.Header[k] = v
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := cl.Do(req)
		if err != nil {
			return c.Errf(c.ErrUnavailable, "could not reach Cheap Inboxes: %v", err)
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil {
			return c.Errf(c.ErrUnavailable, "reading the Cheap Inboxes response: %v", err)
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt < retries {
			wait := 5 * time.Second
			if n, err := strconv.Atoi(resp.Header.Get("X-RateLimit-Reset")); err == nil && n > 0 && n <= 60 {
				wait = time.Duration(n)*time.Second + 250*time.Millisecond
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return apiError(resp.StatusCode, raw)
		}
		if out != nil && len(raw) > 0 {
			if err := json.Unmarshal(raw, out); err != nil {
				return c.Errf(c.ErrUnavailable, "Cheap Inboxes returned something that is not JSON")
			}
		}
		return nil
	}
}

// noSuchItem starts the error for a 404.
const noSuchItem = "Cheap Inboxes has no such item"

// apiError reads Cheap Inboxes' {"error": {"code", "message"}} envelope.
func apiError(status int, raw []byte) error {
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	switch {
	case status == 404:
		return c.Errf(c.ErrInvalid, noSuchItem+" (%s)", orDefault(e.Error.Message, "not found"))
	case e.Error.Message != "" && status >= 400 && status < 500 && status != 401 && status != 403 && status != 429:
		return c.Errf(c.ErrInvalid, "Cheap Inboxes refused the request: %s", e.Error.Message)
	}
	return c.HTTPError(name, status, e.Error.Message)
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// Test reads the organization, which changes nothing.
func (Connector) Test(ctx context.Context, cl *c.Client, cr c.Creds) (c.Account, error) {
	var r struct {
		Organization struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			BillingEmail string `json:"billing_email"`
			Status       string `json:"status"`
		} `json:"organization"`
		Role string `json:"role"`
	}
	if err := call(ctx, cl, cr, http.MethodGet, "/v1/org", nil, &r); err != nil {
		return c.Account{}, err
	}
	acct := c.Account{ID: r.Organization.ID, Name: r.Organization.Name, Email: r.Organization.BillingEmail,
		Details: map[string]string{}}
	if r.Role != "" {
		acct.Details["role"] = r.Role
	}
	if r.Organization.Status != "" {
		acct.Details["status"] = r.Organization.Status
	}
	// The mailbox count, cheaply: one row and the total.
	var m mailboxList
	if err := call(ctx, cl, cr, http.MethodGet, "/v1/mailboxes?limit=1", nil, &m); err == nil {
		acct.Details["mailboxes"] = strconv.Itoa(m.Pagination.Total)
	}
	return acct, nil
}

// mailbox is the API's mailbox object, the fields we read.
type mailbox struct {
	ID                 string     `json:"id"`
	FullEmail          string     `json:"full_email"`
	FirstName          string     `json:"first_name"`
	LastName           string     `json:"last_name"`
	Status             string     `json:"status"`
	SourceProvider     string     `json:"source_provider"`
	Provider           string     `json:"provider"`
	DailyLimit         int        `json:"daily_limit"`
	Tags               []string   `json:"tags"`
	CancellationStatus *string    `json:"cancellation_status"`
	AppPasswordStatus  *string    `json:"app_password_status"`
	EmailBisonID       any        `json:"emailbison_id"`
	InstantlyID        any        `json:"instantly_id"`
	SmartleadID        any        `json:"smartlead_id"`
	PlusVibeID         any        `json:"plusvibe_id"`
	ApolloID           any        `json:"apollo_id"`
	CreatedAt          *time.Time `json:"created_at"`
	ActivatedAt        *time.Time `json:"activated_at"`
}

type mailboxList struct {
	Mailboxes  []mailbox `json:"mailboxes"`
	Pagination struct {
		Total   int   `json:"total"`
		Limit   int   `json:"limit"`
		Offset  int   `json:"offset"`
		HasMore *bool `json:"has_more"`
	} `json:"pagination"`
}

func present(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return x != ""
	}
	return true
}

func (m mailbox) neutral() c.VendorMailbox {
	v := c.VendorMailbox{
		ExternalID: m.ID, Email: strings.ToLower(strings.TrimSpace(m.FullEmail)),
		FirstName: m.FirstName, LastName: m.LastName, VendorStatus: m.Status,
		DailyLimit: m.DailyLimit, Tags: m.Tags, CreatedAt: m.CreatedAt, ActivatedAt: m.ActivatedAt,
		ConnectedTo: []string{},
	}
	if v.Tags == nil {
		v.Tags = []string{}
	}
	switch p := strings.ToLower(orDefault(m.Provider, m.SourceProvider)); p {
	case "google", "microsoft":
		v.Provider = p
	default:
		v.Provider = "other"
	}
	v.Status = neutralStatus(m.Status, m.CancellationStatus)
	v.AppPassword = neutralAppPassword(m.AppPasswordStatus)
	for platform, id := range map[string]any{"emailbison": m.EmailBisonID, "instantly": m.InstantlyID,
		"smartlead": m.SmartleadID, "plusvibe": m.PlusVibeID, "apollo": m.ApolloID} {
		if present(id) {
			v.ConnectedTo = append(v.ConnectedTo, platform)
		}
	}
	sortStrings(v.ConnectedTo)
	return v
}

func neutralStatus(s string, cancellation *string) string {
	if cancellation != nil {
		switch strings.ToLower(*cancellation) {
		case "scheduled", "pending", "requested":
			if strings.EqualFold(s, "active") {
				return c.VendorCancelling
			}
		case "completed", "cancelled", "canceled":
			return c.VendorCancelled
		}
	}
	switch strings.ToLower(s) {
	case "active":
		return c.VendorActive
	case "cancelled", "canceled", "deleted", "deprovisioned", "suspended", "expired":
		return c.VendorCancelled
	case "failed", "error":
		return c.VendorFailed
	}
	return c.VendorProvisioning
}

func neutralAppPassword(s *string) string {
	if s == nil {
		return c.AppPasswordNone
	}
	switch strings.ToLower(*s) {
	case "ready":
		return c.AppPasswordReady
	case "queued", "running", "pending":
		return c.AppPasswordPending
	case "failed", "error":
		return c.AppPasswordFailed
	}
	return c.AppPasswordNone
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// pageSize is the largest page the API serves.
const pageSize = 200

func (Connector) ListMailboxes(ctx context.Context, cl *c.Client, cr c.Creds, page c.VendorPage) ([]c.VendorMailbox, string, error) {
	offset, _ := strconv.Atoi(page.Cursor)
	limit := page.Limit
	if limit <= 0 || limit > pageSize {
		limit = pageSize
	}
	var r mailboxList
	q := url.Values{"limit": {strconv.Itoa(limit)}, "offset": {strconv.Itoa(offset)}}
	if err := call(ctx, cl, cr, http.MethodGet, "/v1/mailboxes?"+q.Encode(), nil, &r); err != nil {
		return nil, "", err
	}
	out := make([]c.VendorMailbox, 0, len(r.Mailboxes))
	for _, m := range r.Mailboxes {
		if m.ID == "" || m.FullEmail == "" {
			continue
		}
		out = append(out, m.neutral())
	}
	more := offset+len(r.Mailboxes) < r.Pagination.Total
	if r.Pagination.HasMore != nil {
		more = *r.Pagination.HasMore
	}
	if !more || len(r.Mailboxes) == 0 {
		return out, "", nil
	}
	return out, strconv.Itoa(offset + len(r.Mailboxes)), nil
}

func (Connector) GetMailbox(ctx context.Context, cl *c.Client, cr c.Creds, id string) (c.VendorMailbox, error) {
	var r struct {
		Mailbox mailbox `json:"mailbox"`
	}
	if err := call(ctx, cl, cr, http.MethodGet, "/v1/mailboxes/"+url.PathEscape(id), nil, &r); err != nil {
		return c.VendorMailbox{}, err
	}
	return r.Mailbox.neutral(), nil
}

func (Connector) MailboxCredentials(ctx context.Context, cl *c.Client, cr c.Creds, id string) (c.VendorCredentials, error) {
	// The reference documents snake_case keys; the live API answers in
	// camelCase. Read both.
	var r struct {
		Credentials struct {
			Email        string `json:"email"`
			Username     string `json:"username"`
			Password     string `json:"password"`
			AppPassword  string `json:"app_password"`
			AppPassword2 string `json:"appPassword"`
			IMAPHost     string `json:"imap_host"`
			IMAPPort     int    `json:"imap_port"`
			SMTPHost     string `json:"smtp_host"`
			SMTPPort     int    `json:"smtp_port"`
			IMAPHost2    string `json:"imapHost"`
			IMAPPort2    int    `json:"imapPort"`
			SMTPHost2    string `json:"smtpHost"`
			SMTPPort2    int    `json:"smtpPort"`
		} `json:"credentials"`
	}
	if err := call(ctx, cl, cr, http.MethodGet, "/v1/mailboxes/"+url.PathEscape(id)+"/credentials", nil, &r); err != nil {
		return c.VendorCredentials{}, err
	}
	k := r.Credentials
	out := c.VendorCredentials{
		Email: strings.ToLower(k.Email), Username: k.Username, Password: k.Password,
		AppPassword: orDefault(k.AppPassword, k.AppPassword2),
		SMTPHost:    orDefault(k.SMTPHost, k.SMTPHost2), SMTPPort: firstNonZero(k.SMTPPort, k.SMTPPort2),
		IMAPHost: orDefault(k.IMAPHost, k.IMAPHost2), IMAPPort: firstNonZero(k.IMAPPort, k.IMAPPort2),
	}
	if out.Password == "" && out.AppPassword == "" {
		return out, c.Errf(c.ErrInvalid, "Cheap Inboxes has no password for this mailbox yet; it is probably still being set up")
	}
	return out, nil
}

func firstNonZero(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

// AppPasswordRate: Cheap Inboxes accepts 20 app password requests an hour
// per organization, three running at once.
func (Connector) AppPasswordRate() int { return 20 }

func (Connector) RequestAppPassword(ctx context.Context, cl *c.Client, cr c.Creds, id string) error {
	return call(ctx, cl, cr, http.MethodPost, "/v1/mailboxes/"+url.PathEscape(id)+"/app-password",
		map[string]any{"appName": "Outflare"}, nil)
}

func (Connector) AppPasswordStatus(ctx context.Context, cl *c.Client, cr c.Creds, id string) (string, string, string, error) {
	var r struct {
		Status       string  `json:"status"`
		AppPassword  string  `json:"appPassword"`
		AppPassword2 string  `json:"app_password"`
		Error        *string `json:"error"`
	}
	if err := call(ctx, cl, cr, http.MethodGet, "/v1/mailboxes/"+url.PathEscape(id)+"/app-password", nil, &r); err != nil {
		return "", "", "", err
	}
	reason := ""
	if r.Error != nil {
		reason = *r.Error
	}
	st := neutralAppPassword(&r.Status)
	return st, orDefault(r.AppPassword, r.AppPassword2), reason, nil
}

// webhookEvents are the events Outflare subscribes to.
var webhookEvents = []string{"mailbox.active", "mailbox.credentials_ready", "domain.provisioned", "order.completed"}

func (Connector) RegisterWebhook(ctx context.Context, cl *c.Client, cr c.Creds, hookURL string) (string, string, error) {
	var r struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	if err := call(ctx, cl, cr, http.MethodPost, "/v1/webhooks", map[string]any{"url": hookURL, "events": webhookEvents}, &r); err != nil {
		return "", "", err
	}
	if r.ID == "" || r.Secret == "" {
		return "", "", c.Errf(c.ErrUnavailable, "Cheap Inboxes did not return a webhook id and secret")
	}
	return r.ID, r.Secret, nil
}

func (Connector) DeleteWebhook(ctx context.Context, cl *c.Client, cr c.Creds, id string) error {
	err := call(ctx, cl, cr, http.MethodDelete, "/v1/webhooks/"+url.PathEscape(id), nil, nil)
	if err != nil && strings.Contains(err.Error(), "no such item") {
		return nil // already gone
	}
	return err
}

// ParseWebhook checks X-Webhook-Signature, the lowercase hex HMAC-SHA256
// of the raw body keyed with the secret, then decodes the delivery.
func (Connector) ParseWebhook(secret string, h http.Header, body []byte) (c.VendorEvent, error) {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	got := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h.Get("X-Webhook-Signature"), "sha256=")))
	if secret == "" || !hmac.Equal([]byte(want), []byte(got)) {
		return c.VendorEvent{}, c.Errf(c.ErrAuth, "the webhook signature does not match")
	}
	var d struct {
		ID      string          `json:"id"`
		Event   string          `json:"event"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(body, &d); err != nil || d.ID == "" {
		return c.VendorEvent{}, c.Errf(c.ErrInvalid, "the webhook body is not a Cheap Inboxes event")
	}
	ev := c.VendorEvent{ID: d.ID, Type: c.EventOther}
	var p struct {
		MailboxID  string `json:"mailbox_id"`
		MailboxID2 string `json:"mailboxId"`
		ID         string `json:"id"`
		Mailbox    struct {
			ID string `json:"id"`
		} `json:"mailbox"`
	}
	_ = json.Unmarshal(d.Payload, &p)
	switch d.Event {
	case "mailbox.active":
		ev.Type = c.EventMailboxActive
	case "mailbox.credentials_ready":
		ev.Type = c.EventMailboxCredentials
	default:
		ev.Resync = strings.HasPrefix(d.Event, "domain.") || strings.HasPrefix(d.Event, "order.")
		if strings.HasPrefix(d.Event, "mailbox.") {
			ev.Type = c.EventMailboxChanged
		}
	}
	if strings.HasPrefix(d.Event, "mailbox.") {
		ev.MailboxExternalID = orDefault(orDefault(p.MailboxID, p.MailboxID2), orDefault(p.Mailbox.ID, p.ID))
		if ev.MailboxExternalID == "" {
			ev.Resync = true
		}
	}
	return ev, nil
}
