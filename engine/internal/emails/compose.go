package emails

import (
	"context"
	"encoding/json"
	"errors"
	"net/mail"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/jobs"
)

// Compose turns a send request into an email ready to queue: the content
// (a template's published version, the one asked for, its draft, or what
// was given), the provider, a verified sender, and the content filled in
// with an unsubscribe link for lifecycle email. A send from the API, a
// broadcast and a journey all come through here.

// Draft is what to send. Exactly one of TemplateID or Subject is set.
type Draft struct {
	NewEmail
	TemplateID string
	// The template version to use, or the draft; else the published one.
	Version    *int
	UseDraft   bool
	Subject    *string
	HTML, Text *string
	Data       map[string]any
	// The topic, overriding the template's, when TopicSet.
	TopicSet bool
	// Overrides the template's tracking settings.
	TrackOpens, TrackClicks *bool
}

// ComposeError is a request that cannot be sent as it is, with the HTTP
// status and error code the API answers with.
type ComposeError struct {
	Status  int
	Code    string
	Message string
}

func (e *ComposeError) Error() string { return e.Message }

func refuse(status int, code, msg string) error {
	return &ComposeError{Status: status, Code: code, Message: msg}
}

func invalid(msg string) error { return refuse(400, "invalid_request", msg) }

// Variable defaults and the reserved names every email fills in.
const (
	VarUnsubscribe = "unsubscribe_url"
	VarEmailID     = "email_id"
)

// Compose fills in d and returns the email to queue. d.ID is set if empty.
func (s *Service) Compose(ctx context.Context, q jobs.Querier, d Draft) (NewEmail, error) {
	e := d.NewEmail
	if e.ID == "" {
		e.ID = newID()
	}
	subject, html, text, from := deref(d.Subject), deref(d.HTML), deref(d.Text), strings.TrimSpace(e.From)
	var defs []Variable
	purpose := e.Purpose
	if d.TemplateID != "" {
		var t struct {
			handle, purpose, status, from, reply string
			subject, html, text                  string
			vars                                 []byte
			published                            *int
			trackOpens, trackClicks              bool
			topic, brand                         *string
		}
		err := q.QueryRow(ctx, `SELECT handle, purpose, status, from_address, reply_to, subject, html, text_body, variables,
			published_version, track_opens, track_clicks, topic_id, brand_id FROM email_templates WHERE workspace_id = $1 AND id = $2`,
			e.WorkspaceID, d.TemplateID).Scan(&t.handle, &t.purpose, &t.status, &t.from, &t.reply, &t.subject, &t.html, &t.text, &t.vars,
			&t.published, &t.trackOpens, &t.trackClicks, &t.topic, &t.brand)
		if errors.Is(err, pgx.ErrNoRows) {
			return e, refuse(404, "not_found", "email template not found")
		}
		if err != nil {
			return e, err
		}
		if t.status != "active" {
			return e, refuse(409, "conflict", "the template is archived; restore it to send with it")
		}
		if purpose != "" && purpose != t.purpose {
			return e, invalid("purpose must match the template's (" + t.purpose + "), or be left out")
		}
		purpose = t.purpose
		tFrom, tReply := t.from, t.reply
		if d.UseDraft {
			subject, html, text = t.subject, t.html, t.text
			_ = json.Unmarshal(t.vars, &defs)
		} else {
			version := t.published
			if d.Version != nil {
				version = d.Version
			}
			if version == nil {
				return e, refuse(409, "template_not_published", "the template "+t.handle+" has no published version yet: publish it, or send with draft: true to test it")
			}
			var vars []byte
			err := q.QueryRow(ctx, `SELECT subject, html, text_body, variables, from_address, reply_to FROM email_template_versions
				WHERE workspace_id = $1 AND template_id = $2 AND version = $3`, e.WorkspaceID, d.TemplateID, *version).Scan(&subject, &html, &text, &vars, &tFrom, &tReply)
			if errors.Is(err, pgx.ErrNoRows) {
				return e, refuse(404, "not_found", "the template has no version "+strconv.Itoa(*version))
			}
			if err != nil {
				return e, err
			}
			_ = json.Unmarshal(vars, &defs)
			e.TemplateVersion = version
		}
		e.TemplateID = &d.TemplateID
		e.TrackOpens, e.TrackClicks = t.trackOpens, t.trackClicks
		if !d.TopicSet {
			e.TopicID = t.topic
		}
		if from == "" {
			from = tFrom
		}
		if len(e.ReplyTo) == 0 && tReply != "" {
			e.ReplyTo = []string{tReply}
		}
		if e.BrandID == nil {
			e.BrandID = t.brand
		}
	}
	if purpose == "" {
		purpose = "transactional"
	}
	e.Purpose = purpose
	if e.TopicID != nil && purpose != "lifecycle" {
		return e, invalid("only lifecycle email has a topic: send with purpose lifecycle, or leave topic out")
	}
	if d.TrackOpens != nil {
		e.TrackOpens = *d.TrackOpens
	}
	if d.TrackClicks != nil {
		e.TrackClicks = *d.TrackClicks
	}
	if from == "" {
		return e, invalid("from is required (an address on a verified sending domain), or set a default sender on the template")
	}
	fromAddr, err := mail.ParseAddress(from)
	if err != nil || len(from) > 320 {
		return e, invalid("from must be an address such as billing@example.com or Example <billing@example.com>")
	}
	e.From = from

	// The provider: the one asked for, or the workspace's default.
	if e.ProviderID == "" {
		err := q.QueryRow(ctx, `SELECT id FROM email_providers WHERE workspace_id = $1 AND channel = 'email' ORDER BY is_default DESC, created_at LIMIT 1`,
			e.WorkspaceID).Scan(&e.ProviderID)
		if errors.Is(err, pgx.ErrNoRows) {
			return e, refuse(409, "no_email_provider", "connect an email provider (Amazon SES, Postmark or Resend) before sending; see Lifecycle, Get started")
		}
		if err != nil {
			return e, err
		}
	}
	var purposes []string
	err = q.QueryRow(ctx, `SELECT purposes FROM email_providers WHERE workspace_id = $1 AND id = $2`, e.WorkspaceID, e.ProviderID).Scan(&purposes)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, refuse(404, "not_found", "email provider not found")
	}
	if err != nil {
		return e, err
	}
	if !slices.Contains(purposes, purpose) {
		return e, refuse(409, "email_provider_purpose", "this email provider is not set to send "+purpose+" email; choose another provider or change its purposes")
	}

	// The sender's domain must be verified with that provider, or the
	// provider would refuse it (or worse, send it unsigned).
	domain := strings.ToLower(fromAddr.Address[strings.LastIndex(fromAddr.Address, "@")+1:])
	var verified bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM email_domains WHERE provider_id = $1 AND status = 'verified' AND ($2 = domain OR $2 LIKE '%.' || domain))`,
		e.ProviderID, domain).Scan(&verified); err != nil {
		return e, err
	}
	if !verified {
		return e, refuse(409, "email_domain_not_verified", domain+" is not a verified sending domain for this provider: add it under Lifecycle, Sending domains, and add its DNS records")
	}

	// Fill it in.
	data := map[string]any{}
	for k, v := range d.Data {
		data[k] = v
	}
	unsubscribe := s.UnsubscribeURL(e.ID)
	data[VarUnsubscribe], data[VarEmailID] = unsubscribe, e.ID
	out, err := Render(subject, html, text, defs, data)
	if err != nil {
		return e, invalid("the content has a mistake in " + err.Error())
	}
	if len(out.Missing) > 0 {
		return e, refuse(422, "template_variables_missing", "data is missing required variables: "+strings.Join(out.Missing, ", "))
	}
	if strings.TrimSpace(out.Subject) == "" {
		return e, invalid("the subject is empty once filled in")
	}
	// Lifecycle email always carries a way out, even if the template
	// forgot one.
	if purpose == "lifecycle" && !strings.Contains(subject+html+text, VarUnsubscribe) {
		if out.HTML != "" {
			out.HTML += `<p style="font-size:12px;color:#6b7280;margin-top:24px"><a href="` + unsubscribe + `" style="color:#6b7280">Unsubscribe</a></p>`
		}
		if out.Text != "" {
			out.Text += "\n\nUnsubscribe: " + unsubscribe
		}
	}
	if out.Text == "" && out.HTML != "" {
		out.Text = PlainText(out.HTML)
	}
	e.Subject, e.HTML, e.Text = out.Subject, out.HTML, out.Text
	return e, nil
}
