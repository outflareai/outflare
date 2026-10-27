package emailbison

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Replies are read from GET /api/replies?folder=inbox (newest first) and
// answered with POST /api/replies/{id}/reply. The reply object's fields
// are read tolerantly, since the reference doesn't pin all of them down.

// reply is one EmailBison reply, with the names its API uses and the
// likely alternatives.
type reply struct {
	ID             id     `json:"id"`
	SenderEmailID  id     `json:"sender_email_id"`
	FromEmail      string `json:"from_email_address"`
	FromEmailAlt   string `json:"from_email"`
	FromName       string `json:"from_name"`
	To             string `json:"primary_to_email_address"`
	Subject        string `json:"subject"`
	TextBody       string `json:"text_body"`
	HTMLBody       string `json:"html_body"`
	MessageID      string `json:"raw_message_id"`
	MessageIDAlt   string `json:"message_id"`
	DateReceived   string `json:"date_received"`
	CreatedAt      string `json:"created_at"`
	AutomatedReply *bool  `json:"automated_reply"`
	Folder         string `json:"folder"`
	SenderEmail    *struct {
		ID    id     `json:"id"`
		Email string `json:"email"`
	} `json:"sender_email"`
}

// maxReplyPages bounds one read: 50 pages of replies is far more than a
// few minutes bring.
const maxReplyPages = 50

func (EmailBison) ListReplies(ctx context.Context, cl *c.Client, creds c.Creds, since time.Time) ([]c.Reply, error) {
	a, err := open(cl, creds)
	if err != nil {
		return nil, err
	}
	var out []c.Reply
	for n := 1; n <= maxReplyPages; n++ {
		var p page[json.RawMessage]
		if err := a.do(ctx, http.MethodGet, "/api/replies?folder=inbox&page="+strconv.Itoa(n), nil, &p); err != nil {
			return nil, err
		}
		older := false
		for _, raw := range p.Data {
			var r reply
			if json.Unmarshal(raw, &r) != nil || r.ID == "" {
				continue
			}
			got := toReply(r)
			if got.ReceivedAt.Before(since) {
				older = true
				continue
			}
			if got.FromEmail == "" || (r.Folder != "" && !strings.EqualFold(r.Folder, "inbox")) {
				continue
			}
			out = append(out, got)
		}
		if older || len(p.Data) == 0 || p.Links.Next == nil && (p.Meta.LastPage == 0 || n >= p.Meta.LastPage) {
			break
		}
	}
	return out, nil
}

func toReply(r reply) c.Reply {
	out := c.Reply{
		ExternalID: string(r.ID), MailboxExternalID: string(r.SenderEmailID),
		FromEmail: strings.ToLower(strings.TrimSpace(first(r.FromEmail, r.FromEmailAlt))), FromName: strings.TrimSpace(r.FromName),
		MailboxEmail: strings.ToLower(strings.TrimSpace(r.To)), Subject: r.Subject,
		Body:      strings.TrimSpace(r.TextBody),
		MessageID: strings.Trim(first(r.MessageID, r.MessageIDAlt), "<> "),
	}
	if r.SenderEmail != nil {
		if out.MailboxExternalID == "" {
			out.MailboxExternalID = string(r.SenderEmail.ID)
		}
		if r.SenderEmail.Email != "" {
			out.MailboxEmail = strings.ToLower(strings.TrimSpace(r.SenderEmail.Email))
		}
	}
	if out.Body == "" {
		out.HTML = r.HTMLBody
	}
	if r.AutomatedReply != nil {
		out.Automated = *r.AutomatedReply
	}
	for _, s := range []string{r.DateReceived, r.CreatedAt} {
		if t, ok := parseTime(s); ok {
			out.ReceivedAt = t
			break
		}
	}
	if out.ReceivedAt.IsZero() {
		out.ReceivedAt = time.Now()
	}
	return out
}

func first(s ...string) string {
	for _, v := range s {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000000Z", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func (EmailBison) SendReply(ctx context.Context, cl *c.Client, creds c.Creds, r c.ReplyOut) error {
	a, err := open(cl, creds)
	if err != nil {
		return err
	}
	sender, err := strconv.Atoi(r.MailboxExternalID)
	if err != nil {
		return c.Errf(c.ErrInvalid, "EmailBison sender email id %q is not a number", r.MailboxExternalID)
	}
	return a.do(ctx, http.MethodPost, "/api/replies/"+url.PathEscape(r.ReplyExternalID)+"/reply", map[string]any{
		"message":                    r.Body,
		"sender_email_id":            sender,
		"to_emails":                  []map[string]string{{"name": r.ToName, "email_address": r.ToEmail}},
		"inject_previous_email_body": true,
	}, nil)
}

var _ c.ReplySource = EmailBison{}
