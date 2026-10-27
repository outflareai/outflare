package emailsend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Postmark sends through a Postmark server's token. Its account token is
// optional: with it, Outflare adds sending domains for you; without it,
// add the domain in Postmark and Outflare only sends.
type Postmark struct{}

const postmarkAPI = "https://api.postmarkapp.com"

func (Postmark) Definition() c.Definition {
	return c.Definition{
		ID: "postmark", Name: "Postmark", Category: c.CategoryEmailSending,
		Summary: "Send transactional email through Postmark, known for fast, reliable delivery of receipts and password resets.",
		Description: "Outflare sends through one of your Postmark servers and registers a webhook on it, so deliveries, bounces and spam complaints come back automatically. " +
			"Add the account token too and Outflare also adds your sending domain with its DKIM and return-path records.",
		Website: "https://postmarkapp.com", DocsURL: "https://postmarkapp.com/developer/api/email-api",
		KeyURL: "https://account.postmarkapp.com/servers",
		Auth:   c.AuthAPIKey,
		Fields: []c.Field{
			{Name: "server_token", Label: "Server API token", Secret: true, Help: "In Postmark: Servers, your server, API Tokens."},
			{Name: "account_token", Label: "Account API token", Secret: true, Optional: true, Help: "Optional. In Postmark: Account, API Tokens. Lets Outflare add sending domains for you."},
		},
		Capabilities: []string{c.CapSendEmail},
		Pricing:      "Pay Postmark directly; 100 test emails a month free",
	}
}

func (Postmark) call(ctx context.Context, cl *c.Client, token, header, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(ctx, method, cl.Base("postmark", postmarkAPI)+path, r)
	req.Header.Set(header, token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := cl.Do(req)
	if err != nil {
		return c.Errf(c.ErrUnavailable, "could not reach Postmark: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out != nil {
			if err := json.Unmarshal(raw, out); err != nil {
				return c.Errf(c.ErrUnavailable, "Postmark returned something that is not JSON")
			}
		}
		return nil
	}
	var e struct {
		ErrorCode int    `json:"ErrorCode"`
		Message   string `json:"Message"`
	}
	_ = json.Unmarshal(raw, &e)
	return postmarkError(resp.StatusCode, e.ErrorCode, e.Message)
}

// postmarkError explains Postmark's error codes
// (postmarkapp.com/developer/api/overview#error-codes).
func postmarkError(status, code int, msg string) error {
	switch {
	case status == 401 || code == 10:
		return c.Errf(c.ErrAuth, "Postmark refused the token: %s", firstNonEmpty(msg, "check it in Postmark's API Tokens"))
	case status == 429:
		return c.Errf(c.ErrQuota, "Postmark is rate limiting: %s", msg)
	case status >= 500:
		return c.Errf(c.ErrUnavailable, "Postmark is not responding properly (HTTP %d). Try again in a few minutes.", status)
	case code == 406:
		return &c.InactiveRecipient{Message: "Postmark has this address marked inactive after an earlier hard bounce or spam complaint: " + msg}
	case code == 412:
		return c.Errf(c.ErrInvalid, "Postmark's account is still pending approval, so it only sends to addresses on your own domain: %s", msg)
	}
	return c.Errf(c.ErrInvalid, "Postmark refused the request: %s", firstNonEmpty(msg, "HTTP "+strconv.Itoa(status)))
}

func (p Postmark) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var srv struct {
		ID    int    `json:"ID"`
		Name  string `json:"Name"`
		Color string `json:"Color"`
	}
	if err := p.call(ctx, cl, creds["server_token"], "X-Postmark-Server-Token", http.MethodGet, "/server", nil, &srv); err != nil {
		return c.Account{}, err
	}
	details := map[string]string{"domains": "manual"}
	if t := creds["account_token"]; t != "" {
		if err := p.call(ctx, cl, t, "X-Postmark-Account-Token", http.MethodGet, "/domains?count=1&offset=0", nil, nil); err != nil {
			return c.Account{}, fmt.Errorf("the server token works, but the account token does not: %w", err)
		}
		details["domains"] = "automatic"
	}
	return c.Account{ID: strconv.Itoa(srv.ID), Name: srv.Name, Details: details}, nil
}

func (p Postmark) Send(ctx context.Context, cl *c.Client, creds c.Creds, m c.OutboundEmail) (string, error) {
	type header struct {
		Name  string `json:"Name"`
		Value string `json:"Value"`
	}
	req := map[string]any{
		"From": m.From, "To": strings.Join(m.To, ","), "Subject": m.Subject,
		"MessageStream": "outbound", "Metadata": map[string]string{"outflare_id": m.ID},
	}
	if m.HTML != "" {
		req["HtmlBody"] = m.HTML
	}
	if m.Text != "" {
		req["TextBody"] = m.Text
	}
	if len(m.Cc) > 0 {
		req["Cc"] = strings.Join(m.Cc, ",")
	}
	if len(m.Bcc) > 0 {
		req["Bcc"] = strings.Join(m.Bcc, ",")
	}
	if len(m.ReplyTo) > 0 {
		req["ReplyTo"] = strings.Join(m.ReplyTo, ",")
	}
	if len(m.Tags) > 0 {
		req["Tag"] = m.Tags[0] // Postmark allows one
	}
	if len(m.Headers) > 0 {
		var hs []header
		for k, v := range m.Headers {
			hs = append(hs, header{k, v})
		}
		req["Headers"] = hs
	}
	var out struct {
		MessageID string `json:"MessageID"`
		ErrorCode int    `json:"ErrorCode"`
		Message   string `json:"Message"`
	}
	if err := p.call(ctx, cl, creds["server_token"], "X-Postmark-Server-Token", http.MethodPost, "/email", req, &out); err != nil {
		return "", err
	}
	if out.ErrorCode != 0 {
		return "", postmarkError(422, out.ErrorCode, out.Message)
	}
	return out.MessageID, nil
}

type postmarkDomain struct {
	ID                         int    `json:"ID"`
	Name                       string `json:"Name"`
	DKIMVerified               bool   `json:"DKIMVerified"`
	DKIMHost                   string `json:"DKIMHost"`
	DKIMTextValue              string `json:"DKIMTextValue"`
	DKIMPendingHost            string `json:"DKIMPendingHost"`
	DKIMPendingTextValue       string `json:"DKIMPendingTextValue"`
	ReturnPathDomain           string `json:"ReturnPathDomain"`
	ReturnPathDomainVerified   bool   `json:"ReturnPathDomainVerified"`
	ReturnPathDomainCNAMEValue string `json:"ReturnPathDomainCNAMEValue"`
}

func (d postmarkDomain) setup() c.DomainSetup {
	host, value := firstNonEmpty(d.DKIMPendingHost, d.DKIMHost), firstNonEmpty(d.DKIMPendingTextValue, d.DKIMTextValue)
	recs := []c.DomainRecord{}
	if host != "" {
		recs = append(recs, c.DomainRecord{Purpose: "dkim", Type: "TXT", Name: host, Value: value, Required: true})
	}
	if d.ReturnPathDomain != "" {
		recs = append(recs, c.DomainRecord{Purpose: "return_path", Type: "CNAME", Name: d.ReturnPathDomain, Value: firstNonEmpty(d.ReturnPathDomainCNAMEValue, "pm.mtasv.net"), Required: true})
	}
	s := c.DomainSetup{Ref: strconv.Itoa(d.ID), Records: recs, Verified: d.DKIMVerified}
	if !d.DKIMVerified {
		s.Reason = "Waiting for Postmark to see the DKIM record."
	}
	return s
}

func (p Postmark) accountToken(creds c.Creds) (string, error) {
	t := creds["account_token"]
	if t == "" {
		return "", c.Errf(c.ErrInvalid, "Outflare needs Postmark's account API token to add domains. Add it to the connection, or add the domain in Postmark under Sender Signatures, Domains")
	}
	return t, nil
}

func (p Postmark) AddDomain(ctx context.Context, cl *c.Client, creds c.Creds, domain string) (c.DomainSetup, error) {
	t, err := p.accountToken(creds)
	if err != nil {
		return c.DomainSetup{}, err
	}
	var d postmarkDomain
	err = p.call(ctx, cl, t, "X-Postmark-Account-Token", http.MethodPost, "/domains", map[string]string{"Name": domain, "ReturnPathDomain": "pm-bounces." + domain}, &d)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "already") {
		// It is there already: find it.
		var list struct {
			Domains []postmarkDomain `json:"Domains"`
		}
		if lerr := p.call(ctx, cl, t, "X-Postmark-Account-Token", http.MethodGet, "/domains?count=500&offset=0", nil, &list); lerr != nil {
			return c.DomainSetup{}, lerr
		}
		for _, x := range list.Domains {
			if strings.EqualFold(x.Name, domain) {
				return p.CheckDomain(ctx, cl, creds, domain, strconv.Itoa(x.ID))
			}
		}
	}
	if err != nil {
		return c.DomainSetup{}, err
	}
	return d.setup(), nil
}

func (p Postmark) CheckDomain(ctx context.Context, cl *c.Client, creds c.Creds, domain, ref string) (c.DomainSetup, error) {
	t, err := p.accountToken(creds)
	if err != nil {
		return c.DomainSetup{}, err
	}
	var d postmarkDomain
	// Ask Postmark to look again, then read the result.
	if err := p.call(ctx, cl, t, "X-Postmark-Account-Token", http.MethodPut, "/domains/"+ref+"/verifyDkim", nil, &d); err != nil {
		return c.DomainSetup{}, err
	}
	_ = p.call(ctx, cl, t, "X-Postmark-Account-Token", http.MethodPut, "/domains/"+ref+"/verifyReturnPath", nil, nil)
	if err := p.call(ctx, cl, t, "X-Postmark-Account-Token", http.MethodGet, "/domains/"+ref, nil, &d); err != nil {
		return c.DomainSetup{}, err
	}
	return d.setup(), nil
}

func (p Postmark) SetUpEvents(ctx context.Context, cl *c.Client, creds c.Creds, callback string) (map[string]string, error) {
	var out struct {
		ID int `json:"ID"`
	}
	err := p.call(ctx, cl, creds["server_token"], "X-Postmark-Server-Token", http.MethodPost, "/webhooks", map[string]any{
		"Url": callback, "MessageStream": "outbound",
		"Triggers": map[string]any{
			"Delivery":      map[string]bool{"Enabled": true},
			"Bounce":        map[string]bool{"Enabled": true, "IncludeContent": false},
			"SpamComplaint": map[string]bool{"Enabled": true, "IncludeContent": false},
		},
	}, &out)
	if err != nil {
		return nil, err
	}
	return map[string]string{"webhook_id": strconv.Itoa(out.ID)}, nil
}

// postmarkHard are the bounce types that mean the address will never
// work.
var postmarkHard = map[string]bool{"HardBounce": true, "BadEmailAddress": true, "ManuallyDeactivated": true, "SpamComplaint": true, "Blocked": true}

// postmarkSoft are temporary failures worth showing; the rest
// (auto-replies, subscribe requests) are not delivery problems.
var postmarkSoft = map[string]bool{"SoftBounce": true, "Transient": true, "DnsError": true, "Unknown": true, "DMARCPolicy": true}

func (Postmark) ParseEvents(_ context.Context, _ *c.Client, _ *http.Request, body []byte, _ map[string]string) ([]c.ProviderEvent, error) {
	var e struct {
		RecordType  string    `json:"RecordType"`
		ID          int64     `json:"ID"`
		MessageID   string    `json:"MessageID"`
		Recipient   string    `json:"Recipient"`
		Email       string    `json:"Email"`
		Type        string    `json:"Type"`
		Description string    `json:"Description"`
		Details     string    `json:"Details"`
		DeliveredAt time.Time `json:"DeliveredAt"`
		BouncedAt   time.Time `json:"BouncedAt"`
	}
	if err := json.Unmarshal(body, &e); err != nil || e.RecordType == "" {
		return nil, c.Errf(c.ErrInvalid, "not a Postmark webhook")
	}
	ev := c.ProviderEvent{MessageID: e.MessageID, Recipient: firstNonEmpty(e.Recipient, e.Email)}
	switch e.RecordType {
	case "Delivery":
		ev.Type, ev.At, ev.Reason = c.EventDelivered, e.DeliveredAt, e.Details
		ev.EventID = "delivery:" + e.MessageID + ":" + ev.Recipient
	case "Bounce":
		ev.Type, ev.At, ev.Reason = c.EventBounced, e.BouncedAt, firstNonEmpty(e.Details, e.Description)
		ev.EventID = "bounce:" + strconv.FormatInt(e.ID, 10)
		switch {
		case postmarkHard[e.Type]:
			ev.BounceType = "hard"
		case postmarkSoft[e.Type]:
			ev.BounceType = "soft"
		default:
			return nil, nil
		}
	case "SpamComplaint":
		ev.Type, ev.At = c.EventComplaint, e.BouncedAt
		ev.EventID = "complaint:" + strconv.FormatInt(e.ID, 10)
	default:
		return nil, nil
	}
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	return []c.ProviderEvent{ev}, nil
}
