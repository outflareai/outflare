// Package sysmail sends Outflare's own account emails: email confirmation
// and password reset. It is not how campaigns send (that is the sending
// package, through your mailboxes); these go through a transactional email
// service, or only to the log when none is set up.
package sysmail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/mail"
	"os"
	"slices"
	"strings"
	"time"
)

// A Message is one account email to one person.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string // optional; sent alongside Text when set
}

// A Sender delivers account emails.
type Sender interface {
	// Name is the provider, as shown in /v1/meta: log, postmark, resend or cloudflare.
	Name() string
	Send(ctx context.Context, m Message) error
}

// FromEnv picks the sender from the environment:
//
//	EMAIL_FROM                 who account emails come from, such as "Outflare <login@example.com>"
//	POSTMARK_SERVER_TOKEN      send with Postmark
//	RESEND_API_KEY             send with Resend
//	CLOUDFLARE_EMAIL_API_TOKEN send with Cloudflare Email Service, with CLOUDFLARE_ACCOUNT_ID
//
// With no key, or without EMAIL_FROM, emails are only written to the log,
// which is enough to try Outflare on one machine.
func FromEnv(log *slog.Logger) (Sender, error) {
	from := strings.TrimSpace(os.Getenv("EMAIL_FROM"))
	postmark, resend := os.Getenv("POSTMARK_SERVER_TOKEN"), os.Getenv("RESEND_API_KEY")
	cloudflare, account := os.Getenv("CLOUDFLARE_EMAIL_API_TOKEN"), strings.TrimSpace(os.Getenv("CLOUDFLARE_ACCOUNT_ID"))
	set := 0
	for _, k := range []string{postmark, resend, cloudflare} {
		if k != "" {
			set++
		}
	}
	switch {
	case set == 0:
		return Log{Logger: log}, nil
	case set > 1:
		return nil, fmt.Errorf("set only one of POSTMARK_SERVER_TOKEN, RESEND_API_KEY and CLOUDFLARE_EMAIL_API_TOKEN")
	case from == "":
		return nil, fmt.Errorf("EMAIL_FROM is required to send account emails")
	case postmark != "":
		return &Postmark{Token: postmark, From: from}, nil
	case resend != "":
		return &Resend{Key: resend, From: from}, nil
	case account == "":
		return nil, fmt.Errorf("CLOUDFLARE_ACCOUNT_ID is required with CLOUDFLARE_EMAIL_API_TOKEN")
	default:
		return &Cloudflare{Token: cloudflare, AccountID: account, From: from}, nil
	}
}

// Log writes emails to the log instead of sending them.
type Log struct{ Logger *slog.Logger }

func (Log) Name() string { return "log" }

func (l Log) Send(_ context.Context, m Message) error {
	l.Logger.Warn("account email not sent: no email service set up (POSTMARK_SERVER_TOKEN, RESEND_API_KEY or CLOUDFLARE_EMAIL_API_TOKEN); here it is",
		"to", m.To, "subject", m.Subject, "body", m.Text)
	return nil
}

var client = &http.Client{Timeout: 15 * time.Second}

// Postmark sends through Postmark's email API.
type Postmark struct {
	Token, From string
	BaseURL     string // default https://api.postmarkapp.com
}

func (*Postmark) Name() string { return "postmark" }

func (p *Postmark) Send(ctx context.Context, m Message) error {
	base := p.BaseURL
	if base == "" {
		base = "https://api.postmarkapp.com"
	}
	body := map[string]string{"From": p.From, "To": m.To, "Subject": m.Subject, "TextBody": m.Text, "MessageStream": "outbound"}
	if m.HTML != "" {
		body["HtmlBody"] = m.HTML
	}
	return post(ctx, base+"/email", map[string]string{"X-Postmark-Server-Token": p.Token}, body)
}

// Resend sends through Resend's email API.
type Resend struct {
	Key, From string
	BaseURL   string // default https://api.resend.com
}

func (*Resend) Name() string { return "resend" }

func (s *Resend) Send(ctx context.Context, m Message) error {
	base := s.BaseURL
	if base == "" {
		base = "https://api.resend.com"
	}
	body := map[string]any{"from": s.From, "to": []string{m.To}, "subject": m.Subject, "text": m.Text}
	if m.HTML != "" {
		body["html"] = m.HTML
	}
	return post(ctx, base+"/emails", map[string]string{"Authorization": "Bearer " + s.Key}, body)
}

// Cloudflare sends through Cloudflare Email Service's REST API. The account
// needs Email Sending (Workers Paid) and the sender's domain onboarded.
type Cloudflare struct {
	Token, AccountID, From string
	BaseURL                string // default https://api.cloudflare.com/client/v4
}

func (*Cloudflare) Name() string { return "cloudflare" }

func (c *Cloudflare) Send(ctx context.Context, m Message) error {
	base := c.BaseURL
	if base == "" {
		base = "https://api.cloudflare.com/client/v4"
	}
	// The API takes the sender as {address, name}, not "Name <address>".
	var from any = c.From
	if a, err := mail.ParseAddress(c.From); err == nil {
		from = map[string]string{"address": a.Address, "name": a.Name}
	}
	body := map[string]any{"from": from, "to": m.To, "subject": m.Subject, "text": m.Text}
	if m.HTML != "" {
		body["html"] = m.HTML
	}
	var out struct {
		Result struct {
			PermanentBounces []string `json:"permanent_bounces"`
		} `json:"result"`
	}
	if err := postJSON(ctx, base+"/accounts/"+c.AccountID+"/email/sending/send",
		map[string]string{"Authorization": "Bearer " + c.Token}, body, &out); err != nil {
		return err
	}
	if slices.ContainsFunc(out.Result.PermanentBounces, func(a string) bool { return strings.EqualFold(a, m.To) }) {
		return fmt.Errorf("cloudflare: %s bounced permanently", m.To)
	}
	return nil
}

func post(ctx context.Context, url string, headers map[string]string, body any) error {
	return postJSON(ctx, url, headers, body, nil)
}

// postJSON sends body as JSON and, when out is set, decodes the reply into it.
func postJSON(ctx context.Context, url string, headers map[string]string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: %s: %s", url, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
	}
	return nil
}
