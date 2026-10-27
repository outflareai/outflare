package emailsend

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Resend sends with a Resend API key that has full access, which also
// lets Outflare add domains and register the webhook.
type Resend struct{}

const resendAPI = "https://api.resend.com"

func (Resend) Definition() c.Definition {
	return c.Definition{
		ID: "resend", Name: "Resend", Category: c.CategoryEmailSending,
		Summary:     "Send transactional and lifecycle email through Resend, the developer-first email API.",
		Description: "Outflare sends through your Resend account, adds your sending domain with its DKIM, SPF and MX records, and registers a signed webhook so deliveries, bounces and complaints come back automatically.",
		Website:     "https://resend.com", DocsURL: "https://resend.com/docs/api-reference/emails/send-email",
		KeyURL:       "https://resend.com/api-keys",
		Auth:         c.AuthAPIKey,
		Fields:       []c.Field{{Name: "api_key", Label: "API key", Secret: true, Placeholder: "re_…", Help: "In Resend: API Keys, Create API Key, with Full access (sending-only keys cannot add domains)."}},
		Capabilities: []string{c.CapSendEmail},
		Pricing:      "Pay Resend directly; 3,000 emails a month free",
	}
}

func (Resend) call(ctx context.Context, cl *c.Client, creds c.Creds, method, path string, body, out any, headers map[string]string) error {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(ctx, method, cl.Base("resend", resendAPI)+path, r)
	req.Header.Set("Authorization", "Bearer "+creds["api_key"])
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := cl.Do(req)
	if err != nil {
		return c.Errf(c.ErrUnavailable, "could not reach Resend: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out != nil && len(raw) > 0 {
			if err := json.Unmarshal(raw, out); err != nil {
				return c.Errf(c.ErrUnavailable, "Resend returned something that is not JSON")
			}
		}
		return nil
	}
	var e struct {
		Name    string `json:"name"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &e)
	switch {
	case e.Name == "restricted_api_key":
		return c.Errf(c.ErrAuth, "this Resend key can only send. Create one with Full access so Outflare can add domains and the webhook")
	case resp.StatusCode == 401 || resp.StatusCode == 403 || e.Name == "invalid_api_key" || e.Name == "missing_api_key":
		return c.Errf(c.ErrAuth, "Resend refused the API key: %s", firstNonEmpty(e.Message, "check it under API Keys"))
	case resp.StatusCode == 429:
		return c.Errf(c.ErrQuota, "Resend is rate limiting or over the plan's quota: %s", e.Message)
	case resp.StatusCode >= 500:
		return c.Errf(c.ErrUnavailable, "Resend is not responding properly (HTTP %d). Try again in a few minutes.", resp.StatusCode)
	}
	return c.Errf(c.ErrInvalid, "Resend refused the request: %s", firstNonEmpty(e.Message, e.Name, "HTTP "+strconv.Itoa(resp.StatusCode)))
}

func (r Resend) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var out struct {
		Data []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := r.call(ctx, cl, creds, http.MethodGet, "/domains", nil, &out, nil); err != nil {
		return c.Account{}, err
	}
	key := creds["api_key"]
	if len(key) > 8 {
		key = key[:7] + "…"
	}
	return c.Account{ID: key, Details: map[string]string{"domains": strconv.Itoa(len(out.Data))}}, nil
}

func (r Resend) Send(ctx context.Context, cl *c.Client, creds c.Creds, m c.OutboundEmail) (string, error) {
	req := map[string]any{"from": m.From, "to": m.To, "subject": m.Subject,
		"tags": []map[string]string{{"name": "outflare_id", "value": strings.ReplaceAll(m.ID, "-", "_")}}}
	if m.HTML != "" {
		req["html"] = m.HTML
	}
	if m.Text != "" {
		req["text"] = m.Text
	}
	if len(m.Cc) > 0 {
		req["cc"] = m.Cc
	}
	if len(m.Bcc) > 0 {
		req["bcc"] = m.Bcc
	}
	if len(m.ReplyTo) > 0 {
		req["reply_to"] = m.ReplyTo
	}
	if len(m.Headers) > 0 {
		req["headers"] = m.Headers
	}
	var out struct {
		ID string `json:"id"`
	}
	// Resend keeps idempotency keys for 24 hours, so a retried job never
	// sends twice.
	if err := r.call(ctx, cl, creds, http.MethodPost, "/emails", req, &out, map[string]string{"Idempotency-Key": "outflare-" + m.ID}); err != nil {
		return "", err
	}
	return out.ID, nil
}

type resendDomain struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"` // not_started, pending, verified, failed, temporary_failure
	Records []struct {
		Record   string `json:"record"` // SPF, DKIM, MX
		Name     string `json:"name"`
		Type     string `json:"type"`
		Value    string `json:"value"`
		Priority int    `json:"priority"`
		Status   string `json:"status"`
	} `json:"records"`
}

func (d resendDomain) setup(domain string) c.DomainSetup {
	s := c.DomainSetup{Ref: d.ID, Verified: d.Status == "verified", Records: []c.DomainRecord{}}
	for _, rec := range d.Records {
		name := rec.Name
		// Resend gives names relative to the domain ("send", "resend._domainkey").
		if name == "" || name == "@" {
			name = domain
		} else if !strings.HasSuffix(strings.TrimSuffix(name, "."), domain) {
			name = name + "." + domain
		}
		purpose := map[string]string{"DKIM": "dkim", "SPF": "spf", "MX": "return_path"}[strings.ToUpper(rec.Record)]
		if purpose == "" {
			purpose = "verification"
		}
		if strings.ToUpper(rec.Type) == "MX" {
			purpose = "return_path"
		}
		s.Records = append(s.Records, c.DomainRecord{Purpose: purpose, Type: strings.ToUpper(rec.Type), Name: name, Value: rec.Value, Priority: rec.Priority, Required: true})
	}
	switch d.Status {
	case "failed":
		s.Reason = "Resend could not find the records within 72 hours. Check them, then check again."
	case "temporary_failure":
		s.Reason = "Resend could not reach your DNS just now. It keeps checking."
	case "verified":
	default:
		s.Reason = "Waiting for Resend to see the records."
	}
	return s
}

func (r Resend) AddDomain(ctx context.Context, cl *c.Client, creds c.Creds, domain string) (c.DomainSetup, error) {
	var d resendDomain
	err := r.call(ctx, cl, creds, http.MethodPost, "/domains", map[string]string{"name": domain}, &d, nil)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "already") {
		var list struct {
			Data []resendDomain `json:"data"`
		}
		if lerr := r.call(ctx, cl, creds, http.MethodGet, "/domains", nil, &list, nil); lerr != nil {
			return c.DomainSetup{}, lerr
		}
		for _, x := range list.Data {
			if strings.EqualFold(x.Name, domain) {
				return r.CheckDomain(ctx, cl, creds, domain, x.ID)
			}
		}
	}
	if err != nil {
		return c.DomainSetup{}, err
	}
	return d.setup(domain), nil
}

func (r Resend) CheckDomain(ctx context.Context, cl *c.Client, creds c.Creds, domain, ref string) (c.DomainSetup, error) {
	// Ask Resend to look again; it checks in the background.
	_ = r.call(ctx, cl, creds, http.MethodPost, "/domains/"+ref+"/verify", nil, nil, nil)
	var d resendDomain
	if err := r.call(ctx, cl, creds, http.MethodGet, "/domains/"+ref, nil, &d, nil); err != nil {
		return c.DomainSetup{}, err
	}
	return d.setup(domain), nil
}

var resendEvents = []string{"email.sent", "email.delivered", "email.delivery_delayed", "email.bounced", "email.complained", "email.failed"}

func (r Resend) SetUpEvents(ctx context.Context, cl *c.Client, creds c.Creds, callback string) (map[string]string, error) {
	var out struct {
		ID            string `json:"id"`
		SigningSecret string `json:"signing_secret"`
	}
	if err := r.call(ctx, cl, creds, http.MethodPost, "/webhooks", map[string]any{"endpoint": callback, "events": resendEvents}, &out, nil); err != nil {
		if c.KindOf(err) == c.ErrInvalid {
			// Older accounts have no webhook API: a person adds it.
			return nil, c.ErrEventsManual
		}
		return nil, err
	}
	if out.SigningSecret == "" {
		return nil, c.ErrEventsManual
	}
	return map[string]string{"webhook_id": out.ID, "signing_secret": out.SigningSecret}, nil
}

func (Resend) ParseEvents(_ context.Context, _ *c.Client, req *http.Request, body []byte, config map[string]string) ([]c.ProviderEvent, error) {
	if err := verifySvix(req.Header, body, config["signing_secret"], time.Now()); err != nil {
		return nil, err
	}
	var e struct {
		Type      string    `json:"type"`
		CreatedAt time.Time `json:"created_at"`
		Data      struct {
			EmailID string   `json:"email_id"`
			To      []string `json:"to"`
			Bounce  struct {
				Type    string `json:"type"` // Permanent, Transient, Undetermined
				Message string `json:"message"`
			} `json:"bounce"`
			Failed struct {
				Reason string `json:"reason"`
			} `json:"failed"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &e); err != nil || e.Type == "" {
		return nil, c.Errf(c.ErrInvalid, "not a Resend webhook")
	}
	ev := c.ProviderEvent{MessageID: e.Data.EmailID, EventID: req.Header.Get("svix-id"), At: e.CreatedAt}
	if len(e.Data.To) > 0 {
		ev.Recipient = e.Data.To[0]
	}
	switch e.Type {
	case "email.sent":
		ev.Type = c.EventSent
	case "email.delivered":
		ev.Type = c.EventDelivered
	case "email.delivery_delayed":
		ev.Type = c.EventDelayed
	case "email.bounced":
		ev.Type, ev.Reason, ev.BounceType = c.EventBounced, e.Data.Bounce.Message, "soft"
		if e.Data.Bounce.Type == "Permanent" {
			ev.BounceType = "hard"
		}
	case "email.complained":
		ev.Type = c.EventComplaint
	case "email.failed":
		ev.Type, ev.Reason = c.EventFailed, e.Data.Failed.Reason
	case "email.opened":
		ev.Type = c.EventOpened
	case "email.clicked":
		ev.Type = c.EventClicked
	default:
		return nil, nil
	}
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	return []c.ProviderEvent{ev}, nil
}

// verifySvix checks a Standard Webhooks (Svix) signature: base64
// HMAC-SHA256 of "id.timestamp.body" with the secret after its whsec_
// prefix, within five minutes of now.
func verifySvix(h http.Header, body []byte, secret string, now time.Time) error {
	if secret == "" {
		return c.Errf(c.ErrAuth, "no signing secret is saved for this provider's webhook")
	}
	id, ts, sigs := h.Get("svix-id"), h.Get("svix-timestamp"), h.Get("svix-signature")
	if id == "" {
		id, ts, sigs = h.Get("webhook-id"), h.Get("webhook-timestamp"), h.Get("webhook-signature")
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || id == "" {
		return c.Errf(c.ErrAuth, "the webhook has no signature headers")
	}
	if d := now.Sub(time.Unix(sec, 0)); d > 5*time.Minute || d < -5*time.Minute {
		return c.Errf(c.ErrAuth, "the webhook's timestamp is too old or in the future")
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil {
		return c.Errf(c.ErrAuth, "the saved signing secret is not a whsec_ secret")
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(id + "." + ts + "." + string(body)))
	want := base64.StdEncoding.EncodeToString(m.Sum(nil))
	for _, s := range strings.Fields(sigs) {
		if v, sig, ok := strings.Cut(s, ","); ok && v == "v1" && hmac.Equal([]byte(sig), []byte(want)) {
			return nil
		}
	}
	return c.Errf(c.ErrAuth, "the webhook signature does not match")
}

// All returns the email providers.
func All() []c.Connector { return []c.Connector{SES{}, Postmark{}, Resend{}} }
