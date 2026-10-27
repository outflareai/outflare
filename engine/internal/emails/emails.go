// Package emails sends lifecycle and transactional email (receipts,
// password resets, sign-in codes, onboarding) to people who asked for it,
// through the workspace's own email provider: Amazon SES, Postmark or
// Resend. Cold outreach never goes this way; it stays on mailboxes.
//
// An email is a row with a state (queued, sent, delivered, bounced...). A
// job hands it to the provider after the policy check, and the provider's
// delivery events come back to a callback URL that moves it on. Every
// transactional send runs on its own job queue, so a big lifecycle send or
// a backlog of other work never delays a password reset.
package emails

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/leads"
	"github.com/outflareai/outflare/engine/internal/policy"
	"github.com/outflareai/outflare/engine/internal/secrets"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/tracking"
)

// Job kinds. JobSend runs on its own queue (see SendKinds).
const (
	JobSend        = "email.send"
	JobBroadcast   = "email.broadcast"
	JobDomainCheck = "email.domain_check"
	JobEventsSetup = "email.events_setup"
)

// SendKinds are the job kinds the dedicated send queue runs.
var SendKinds = []string{JobSend}

// Resolver looks up the DNS records a sending domain needs.
type Resolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
	LookupCNAME(ctx context.Context, host string) (string, error)
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
}

// Service sends email and keeps its state.
type Service struct {
	Pool      *pgxpool.Pool
	Store     *connectors.Store
	Box       *secrets.Box
	PublicURL string
	Log       *slog.Logger
	Resolver  Resolver // nil asks each domain's own nameservers (Authoritative)
}

// Who did something, for the audit log.
type Who struct {
	ActorID, ActorType, UserID, RequestID string
	MissionID                             string // the mission the request ran in, if any
}

// System is work the engine does on its own (jobs, provider callbacks).
var System = Who{ActorType: "system"}

func (s *Service) record(ctx context.Context, q jobs.Querier, who Who, ws, action, targetType, targetID string, data map[string]any) error {
	if who.ActorType == "" {
		who.ActorType = "system"
	}
	if who.MissionID != "" {
		if data == nil {
			data = map[string]any{}
		}
		data["mission_id"] = who.MissionID
	}
	return audit.Record(ctx, q, audit.Event{WorkspaceID: ws, ActorID: who.ActorID, ActorType: who.ActorType, UserID: who.UserID,
		RequestID: who.RequestID, Action: action, TargetType: targetType, TargetID: targetID, Data: data})
}

func (s *Service) resolver() Resolver {
	if s.Resolver != nil {
		return s.Resolver
	}
	return Authoritative{}
}

// Register adds the job handlers to q.
func (s *Service) Register(q *jobs.Queue) {
	q.Handle(JobSend, s.sendJob)
	q.Handle(JobBroadcast, s.broadcastJob)
	q.Handle(JobDomainCheck, s.domainJob)
	q.Handle(JobEventsSetup, s.eventsJob)
	q.Handle(JobDomainDNS, s.dnsJob)
}

// Attach makes connecting an email provider integration add it here.
func (s *Service) Attach(store *connectors.Store) {
	store.OnConnect = append(store.OnConnect, s.connected)
}

// NewID makes an email id, for content that must name it before Queue.
func NewID() string { return newID() }

func newID() string {
	var b [16]byte
	rand.Read(b[:]) //nolint:errcheck // never fails
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func randomToken() string {
	var b [24]byte
	rand.Read(b[:]) //nolint:errcheck // never fails
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// ---- Providers ----

// IsSender reports whether a connector sends email.
func (s *Service) IsSender(connector string) bool {
	d, ok := s.Store.Registry.Definition(connector)
	return ok && d.Has(connectors.CapSendEmail)
}

// connected adds a newly connected email provider and starts setting up
// its delivery events. It runs inside the connecting transaction.
func (s *Service) connected(ctx context.Context, tx pgx.Tx, i connectors.Integration) error {
	if !s.IsSender(i.Connector) {
		return nil
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM email_providers WHERE integration_id = $1)`, i.ID).Scan(&exists); err != nil || exists {
		return err
	}
	id := newID()
	sealed, err := s.seal(id, map[string]string{"token": randomToken()})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO email_providers (id, workspace_id, integration_id, connector, secrets, is_default)
		VALUES ($1, $2, $3, $4, $5,
		        NOT EXISTS (SELECT 1 FROM email_providers WHERE workspace_id = $2 AND channel = 'email' AND is_default))`,
		id, i.WorkspaceID, i.ID, i.Connector, sealed)
	if err != nil {
		return err
	}
	_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobEventsSetup, WorkspaceID: &i.WorkspaceID, Payload: map[string]string{"provider_id": id}, MaxAttempts: 5})
	return err
}

func (s *Service) seal(id string, v map[string]string) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return s.Box.Seal(b, []byte(id)), nil
}

func (s *Service) unseal(id string, sealed []byte) (map[string]string, error) {
	plain, err := s.Box.Open(sealed, []byte(id))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	return out, json.Unmarshal(plain, &out)
}

// provider is an email_providers row with what sending needs.
type provider struct {
	ID, WorkspaceID, IntegrationID, Connector, Status string
	Secrets                                           map[string]string
}

func (s *Service) loadProvider(ctx context.Context, q connectors.Querier, ws, id string) (*provider, error) {
	var p provider
	var sealed []byte
	err := q.QueryRow(ctx, `
		SELECT id, workspace_id, integration_id, connector, status, secrets FROM email_providers
		WHERE id = $1 AND ($2 = '' OR workspace_id::text = $2)`, id, ws).
		Scan(&p.ID, &p.WorkspaceID, &p.IntegrationID, &p.Connector, &p.Status, &sealed)
	if err != nil {
		return nil, err
	}
	if p.Secrets, err = s.unseal(p.ID, sealed); err != nil {
		return nil, err
	}
	return &p, nil
}

// CallbackURL is where the provider sends delivery events.
func (s *Service) CallbackURL(providerID, token string) string {
	return strings.TrimRight(s.PublicURL, "/") + "/v1/email-events/" + providerID + "/" + token
}

// ProviderCallback returns a provider's callback URL, for people who
// connect events by hand.
func (s *Service) ProviderCallback(ctx context.Context, ws, id string) (string, error) {
	p, err := s.loadProvider(ctx, s.Pool, ws, id)
	if err != nil {
		return "", err
	}
	return s.CallbackURL(p.ID, p.Secrets["token"]), nil
}

// open returns the provider's sender, ready to call.
func (s *Service) open(ctx context.Context, p *provider) (*connectors.Opened, connectors.EmailSender, error) {
	op, err := s.Store.Open(ctx, s.Pool, p.WorkspaceID, p.IntegrationID)
	if err != nil {
		return nil, nil, err
	}
	sender, ok := op.Connector.(connectors.EmailSender)
	if !ok {
		return nil, nil, connectors.Errf(connectors.ErrInvalid, "%s does not send email", op.Connector.Definition().Name)
	}
	return op, sender, nil
}

// SetSigningSecret saves a webhook signing secret a person copied from the
// provider, for providers whose events are connected by hand.
func (s *Service) SetSigningSecret(ctx context.Context, tx pgx.Tx, ws, id, secret string) error {
	p, err := s.loadProvider(ctx, tx, ws, id)
	if err != nil {
		return err
	}
	p.Secrets["signing_secret"] = secret
	sealed, err := s.seal(p.ID, p.Secrets)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE email_providers SET secrets = $2, events_status = 'manual', events_error = NULL, updated_at = now() WHERE id = $1`, p.ID, sealed)
	return err
}

func (s *Service) eventsJob(ctx context.Context, job jobs.Job) error {
	var in struct {
		ProviderID string `json:"provider_id"`
	}
	if err := json.Unmarshal(job.Payload, &in); err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
	}
	err := s.SetUpEvents(ctx, in.ProviderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // removed meanwhile
	}
	if err != nil && connectors.KindOf(err) == connectors.ErrUnavailable && job.Attempt < job.MaxAttempts {
		return err
	}
	return nil
}

// SetUpEvents asks the provider to send delivery events to Outflare, and
// records how that went on the provider.
func (s *Service) SetUpEvents(ctx context.Context, id string) error {
	p, err := s.loadProvider(ctx, s.Pool, "", id)
	if err != nil {
		return err
	}
	status, msg := "active", ""
	var config map[string]string
	base := strings.TrimRight(s.PublicURL, "/")
	switch {
	case !strings.HasPrefix(base, "https://") || strings.Contains(base, "localhost") || strings.Contains(base, "127.0.0.1"):
		status, msg = "manual", "This server's PUBLIC_URL is not a public https address, so the provider cannot send delivery events to it. Bounces still show when the provider refuses a send."
	default:
		_, _ = s.Pool.Exec(ctx, `UPDATE email_providers SET events_status = 'setting_up', updated_at = now() WHERE id = $1`, id)
		op, sender, oerr := s.open(ctx, p)
		if oerr == nil {
			config, oerr = sender.SetUpEvents(ctx, op.Client, op.Creds, s.CallbackURL(p.ID, p.Secrets["token"]))
		}
		switch {
		case errors.Is(oerr, connectors.ErrEventsManual):
			status, msg = "manual", ""
		case oerr != nil:
			status, msg, err = "failed", oerr.Error(), oerr
		}
	}
	for k, v := range config {
		p.Secrets[k] = v
	}
	sealed, serr := s.seal(p.ID, p.Secrets)
	if serr != nil {
		return serr
	}
	txErr := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE email_providers SET secrets = $2, events_status = $3, events_error = NULLIF($4, ''), updated_at = now() WHERE id = $1`,
			p.ID, sealed, status, msg); err != nil {
			return err
		}
		action := map[string]string{"active": "email_provider.events_connected", "manual": "email_provider.events_manual", "failed": "email_provider.events_failed"}[status]
		return s.record(ctx, tx, System, p.WorkspaceID, action, "email_provider", p.ID, map[string]any{"connector": p.Connector})
	})
	if txErr != nil {
		return txErr
	}
	if status == "failed" {
		track.Fail(ctx, s.Log, track.Event{Name: "email_provider.events_failed", WorkspaceID: p.WorkspaceID, Surface: "job",
			Props: map[string]any{"connector": p.Connector, "kind": connectors.KindOf(err)}}, "provider_error", err)
	}
	return err
}

// ---- Sending ----

// NewEmail is an email to queue. Content is final: templates are filled
// in before it gets here.
type NewEmail struct {
	// ID is optional: set it when the content needs the id first, for the
	// unsubscribe link. NewID makes one.
	ID                               string
	WorkspaceID, ProviderID, Purpose string
	BrandID                          *string
	From                             string
	To, Cc, Bcc, ReplyTo             []string
	Subject, HTML, Text              string
	TemplateID                       *string
	TemplateVersion                  *int
	Tags                             []string
	Metadata                         map[string]any
	SendAt                           *time.Time
	Test                             bool
	// What asked for it (api, test, event, journey, broadcast, system), and
	// the event, journey step or broadcast behind it.
	Trigger, TriggerRef string
	// Where the API call came from (web, api, mcp, cli, sdk, mission) and
	// the API token it used, if any.
	Surface, TokenID string
	// Count opens with a tracking pixel and clicks through tracked links.
	TrackOpens, TrackClicks bool
	// The subscription topic of a lifecycle email.
	TopicID *string
	// The broadcast it is part of.
	BroadcastID string
}

// Queue inserts an email and its send job in tx, and returns its id. who
// is recorded as asking for it.
func (s *Service) Queue(ctx context.Context, tx pgx.Tx, who Who, e NewEmail) (string, error) {
	id := e.ID
	if id == "" {
		id = newID()
	}
	status := "queued"
	runAt := time.Time{}
	if e.SendAt != nil && e.SendAt.After(time.Now()) {
		status, runAt = "scheduled", *e.SendAt
	}
	meta, _ := json.Marshal(e.Metadata)
	if e.Metadata == nil {
		meta = []byte(`{}`)
	}
	var createdBy *string
	if who.ActorID != "" {
		createdBy = &who.ActorID
	}
	trigger := e.Trigger
	if trigger == "" {
		trigger = "api"
		if e.Test {
			trigger = "test"
		}
	}
	recipients := []string{}
	for _, a := range append(append(append([]string{}, e.To...), e.Cc...), e.Bcc...) {
		if a = strings.ToLower(a); !slices.Contains(recipients, a) {
			recipients = append(recipients, a)
		}
	}
	if !e.Test && (e.Trigger == "broadcast" || e.Trigger == "journey") && e.TriggerRef != "" {
		var err error
		if e.HTML, e.Text, err = s.tagOwnLinks(ctx, tx, e.WorkspaceID, e.BrandID, e.Trigger, e.TriggerRef, e.HTML, e.Text); err != nil {
			return "", err
		}
	}
	var tracked map[string]string
	if e.TrackOpens || e.TrackClicks {
		base, err := tracking.BaseURL(ctx, tx, e.WorkspaceID, s.PublicURL)
		if err != nil {
			return "", err
		}
		e.HTML, e.Text, tracked = s.addTracking(base, id, e.HTML, e.Text, e.TrackOpens, e.TrackClicks)
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO emails (id, workspace_id, brand_id, provider_id, purpose, from_address, to_addresses, cc, bcc, reply_to,
			subject, html, text_body, template_id, template_version, tags, metadata, status, test, send_at, created_by,
			trigger_kind, trigger_ref, request_id, surface, token_id, mission_id, track_opens, track_clicks, recipients, topic_id, broadcast_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21,
			$22, NULLIF($23, ''), NULLIF($24, ''), NULLIF($25, ''), NULLIF($26, '')::uuid, NULLIF($27, '')::uuid, $28, $29, $30, $31, NULLIF($32, '')::uuid)`,
		id, e.WorkspaceID, e.BrandID, e.ProviderID, e.Purpose, e.From, e.To, nonNil(e.Cc), nonNil(e.Bcc), nonNil(e.ReplyTo),
		e.Subject, e.HTML, e.Text, e.TemplateID, e.TemplateVersion, nonNil(e.Tags), meta, status, e.Test, e.SendAt, createdBy,
		trigger, e.TriggerRef, who.RequestID, e.Surface, e.TokenID, who.MissionID, e.TrackOpens, e.TrackClicks, recipients, e.TopicID, e.BroadcastID)
	if err != nil {
		return "", err
	}
	if err := saveLinks(ctx, tx, e.WorkspaceID, id, tracked); err != nil {
		return "", err
	}
	if err := addEvent(ctx, tx, e.WorkspaceID, id, status, "", "", ""); err != nil {
		return "", err
	}
	if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobSend, WorkspaceID: &e.WorkspaceID, Payload: map[string]string{"email_id": id},
		RunAt: runAt, IdempotencyKey: "email:" + id, MaxAttempts: 8}); err != nil {
		return "", err
	}
	data := map[string]any{"purpose": e.Purpose, "recipients": len(e.To) + len(e.Cc) + len(e.Bcc), "test": e.Test, "trigger": trigger,
		"track_opens": e.TrackOpens, "track_clicks": e.TrackClicks}
	if e.TemplateID != nil {
		data["template_id"] = *e.TemplateID
	}
	if e.TopicID != nil {
		data["topic_id"] = *e.TopicID
	}
	return id, s.record(ctx, tx, who, e.WorkspaceID, "email.queued", "email", id, data)
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func addEvent(ctx context.Context, q jobs.Querier, ws, emailID, typ, recipient, detail, providerEventID string) error {
	_, err := q.Exec(ctx, `
		INSERT INTO email_events (workspace_id, email_id, type, recipient, detail, provider_event_id)
		VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), NULLIF($6, ''))`, ws, emailID, typ, recipient, detail, providerEventID)
	return err
}

func (s *Service) sendJob(ctx context.Context, job jobs.Job) error {
	var in struct {
		EmailID string `json:"email_id"`
	}
	if err := json.Unmarshal(job.Payload, &in); err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
	}
	return s.Send(ctx, in.EmailID, job.Attempt >= job.MaxAttempts)
}

type queued struct {
	WorkspaceID, Purpose, Status, From, Subject, HTML, Text string
	ProviderID, IntegrationID, Connector, ProviderStatus    *string
	TopicID                                                 *string
	To, Cc, Bcc, ReplyTo, Tags                              []string
	Sealed                                                  []byte
}

// Send hands a queued email to its provider. last is true on the final
// attempt, when a temporary failure becomes a permanent one. An error
// means try again later.
func (s *Service) Send(ctx context.Context, id string, last bool) error {
	var q queued
	var p *provider
	var skip bool
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT e.workspace_id, e.purpose, e.status, e.from_address, e.subject, e.html, e.text_body,
			       e.to_addresses, e.cc, e.bcc, e.reply_to, e.tags,
			       p.id, p.integration_id, p.connector, p.status, p.secrets, e.topic_id
			FROM emails e LEFT JOIN email_providers p ON p.id = e.provider_id
			WHERE e.id = $1 FOR UPDATE OF e`, id).
			Scan(&q.WorkspaceID, &q.Purpose, &q.Status, &q.From, &q.Subject, &q.HTML, &q.Text,
				&q.To, &q.Cc, &q.Bcc, &q.ReplyTo, &q.Tags,
				&q.ProviderID, &q.IntegrationID, &q.Connector, &q.ProviderStatus, &q.Sealed, &q.TopicID)
		if errors.Is(err, pgx.ErrNoRows) {
			skip = true
			return nil
		}
		if err != nil {
			return err
		}
		switch q.Status {
		case "queued", "scheduled":
		case "sending":
			// An earlier attempt stopped after handing it over, or just
			// before. Resend drops repeats of the same send; with the
			// others, sending again could deliver it twice.
			if q.Connector == nil || *q.Connector != "resend" {
				skip = true
				_, err := tx.Exec(ctx, `UPDATE emails SET status = 'sent', status_reason = $2, updated_at = now() WHERE id = $1`, id,
					"Outflare lost track of this send after handing it to the provider, so it was probably sent. Delivery events will confirm it.")
				return err
			}
		default:
			skip = true // sent, cancelled or finished already
			return nil
		}
		if q.ProviderID == nil {
			skip = true
			return s.finish(ctx, tx, q.WorkspaceID, id, "failed", "The email provider it was queued for was removed.", "provider_missing")
		}
		if *q.ProviderStatus == "paused" {
			return errPaused
		}
		p = &provider{ID: *q.ProviderID, WorkspaceID: q.WorkspaceID, IntegrationID: *q.IntegrationID, Connector: *q.Connector}
		if p.Secrets, err = s.unseal(p.ID, q.Sealed); err != nil {
			return err
		}
		// The policy check, at the moment of sending: an unsubscribe or a
		// bounce since the email was queued still stops it.
		all := append(append(append([]string{}, q.To...), q.Cc...), q.Bcc...)
		ds, err := policy.CheckPurpose(ctx, tx, q.WorkspaceID, q.Purpose, all)
		if err != nil {
			return err
		}
		blocked := map[string]string{}
		for _, d := range ds {
			if !d.Allowed {
				blocked[strings.ToLower(d.Email)] = strings.Join(d.Reasons, ", ")
			}
		}
		// What each person chose on their customer record: the email's
		// topic, or all marketing email.
		chosen, err := topicBlocks(ctx, tx, q.WorkspaceID, q.Purpose, q.TopicID, all)
		if err != nil {
			return err
		}
		for addr, why := range chosen {
			if _, ok := blocked[addr]; !ok {
				blocked[addr] = why
			}
		}
		keep := func(in []string) []string {
			out := []string{}
			for _, a := range in {
				if _, ok := blocked[strings.ToLower(a)]; !ok {
					out = append(out, a)
				}
			}
			return out
		}
		q.To, q.Cc, q.Bcc = keep(q.To), keep(q.Cc), keep(q.Bcc)
		for addr, why := range blocked {
			if err := addEvent(ctx, tx, q.WorkspaceID, id, "suppressed", addr, why, ""); err != nil {
				return err
			}
			if err := onTimeline(ctx, tx, q.WorkspaceID, id, "rejected", []string{addr}, map[string]any{"status": "suppressed", "reason": why}, time.Now()); err != nil {
				return err
			}
		}
		if len(q.To) == 0 {
			skip = true
			reasons := []string{}
			for _, why := range blocked {
				reasons = append(reasons, why)
			}
			return s.finish(ctx, tx, q.WorkspaceID, id, "suppressed", "No recipient can get it: "+strings.Join(reasons, "; "), "")
		}
		_, err = tx.Exec(ctx, `UPDATE emails SET status = 'sending', attempts = attempts + 1, updated_at = now() WHERE id = $1`, id)
		return err
	})
	if errors.Is(err, errPaused) {
		if last {
			return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
				return s.finish(ctx, tx, q.WorkspaceID, id, "failed", "The email provider stayed paused, so it was never sent.", "provider_paused")
			})
		}
		return err
	}
	if err != nil || skip {
		return err
	}

	m := connectors.OutboundEmail{ID: id, From: q.From, To: q.To, Cc: q.Cc, Bcc: q.Bcc, ReplyTo: q.ReplyTo,
		Subject: q.Subject, HTML: q.HTML, Text: q.Text, Tags: q.Tags, Config: p.Secrets,
		Headers: map[string]string{"X-Outflare-Email-ID": id}}
	if q.Purpose == policy.PurposeLifecycle {
		// One-click unsubscribe (RFC 8058), which Gmail and Yahoo require
		// of bulk senders.
		m.Headers["List-Unsubscribe"] = "<" + s.UnsubscribeURL(id) + ">"
		m.Headers["List-Unsubscribe-Post"] = "List-Unsubscribe=One-Click"
	}
	var msgID string
	op, sender, err := s.open(ctx, p)
	if err == nil {
		msgID, err = sender.Send(ctx, op.Client, op.Creds, m)
	}
	var again error
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var inactive *connectors.InactiveRecipient
		kind := connectors.KindOf(err)
		switch {
		case err == nil:
			if _, err := tx.Exec(ctx, `
				UPDATE emails SET status = 'sent', provider_message_id = $2, sent_at = now(), status_reason = NULL, updated_at = now()
				WHERE id = $1 AND status = 'sending'`, id, msgID); err != nil {
				return err
			}
			if err := addEvent(ctx, tx, q.WorkspaceID, id, "sent", "", "", "outflare:sent"); err != nil {
				return err
			}
			if err := onTimeline(ctx, tx, q.WorkspaceID, id, "sent", q.To, nil, time.Now()); err != nil {
				return err
			}
			return s.record(ctx, tx, System, q.WorkspaceID, "email.sent", "email", id, map[string]any{"provider_id": p.ID, "connector": p.Connector, "purpose": q.Purpose})
		case errors.As(err, &inactive):
			if len(q.To) == 1 && len(q.Cc)+len(q.Bcc) == 0 {
				if _, err := leads.AddSuppressions(ctx, tx, q.WorkspaceID, "bounced", "email_provider", p.Connector+" marked the address inactive", []leads.Suppression{{Kind: "email", Value: strings.ToLower(q.To[0])}}); err != nil {
					return err
				}
			}
			return s.finish(ctx, tx, q.WorkspaceID, id, "suppressed", inactive.Message, "")
		case !last && (kind == connectors.ErrUnavailable || kind == connectors.ErrQuota) && !errors.Is(err, connectors.ErrNotFound):
			// The provider did not take it: try again with backoff.
			if _, uerr := tx.Exec(ctx, `UPDATE emails SET status = 'queued', status_reason = $2, updated_at = now() WHERE id = $1`, id, "Retrying: "+err.Error()); uerr != nil {
				return uerr
			}
			if aerr := addEvent(ctx, tx, q.WorkspaceID, id, "retrying", "", err.Error(), ""); aerr != nil {
				return aerr
			}
			again = &retry{err}
			return nil
		default:
			msg := err.Error()
			if errors.Is(err, connectors.ErrNotFound) {
				msg = "The email provider's connection was removed."
			}
			return s.finish(ctx, tx, q.WorkspaceID, id, "failed", msg, "provider_"+kind)
		}
	})
	if err != nil {
		return err
	}
	return again
}

var errPaused = errors.New("the email provider is paused")

// retry carries a temporary failure out of the transaction that recorded
// it, so the job tries again.
type retry struct{ err error }

func (r *retry) Error() string { return r.err.Error() }
func (r *retry) Unwrap() error { return r.err }

// finish ends an email as failed or suppressed, with its event and audit.
func (s *Service) finish(ctx context.Context, tx pgx.Tx, ws, id, status, reason, code string) error {
	if _, err := tx.Exec(ctx, `UPDATE emails SET status = $2, status_reason = $3, updated_at = now() WHERE id = $1`, id, status, reason); err != nil {
		return err
	}
	if err := addEvent(ctx, tx, ws, id, status, "", reason, ""); err != nil {
		return err
	}
	// Recipients already marked suppressed one by one keep that entry.
	if err := onTimeline(ctx, tx, ws, id, "rejected", nil, map[string]any{"status": status, "reason": reason}, time.Now()); err != nil {
		return err
	}
	data := map[string]any{"status": status}
	if code != "" {
		data["error_code"] = code
	}
	return s.record(ctx, tx, System, ws, "email."+status, "email", id, data)
}

// Cancel stops a queued or scheduled email that has not been sent.
func (s *Service) Cancel(ctx context.Context, tx pgx.Tx, who Who, ws, id string) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE emails SET status = 'cancelled', status_reason = 'Cancelled before sending', updated_at = now()
		WHERE workspace_id = $1 AND id = $2 AND status IN ('queued', 'scheduled')`, ws, id)
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	if err := addEvent(ctx, tx, ws, id, "cancelled", "", "", ""); err != nil {
		return false, err
	}
	return true, s.record(ctx, tx, who, ws, "email.cancelled", "email", id, nil)
}

// ---- Delivery events ----

// rank orders states so a late or repeated event never moves an email
// backwards: a delivery after a bounce leaves it bounced.
var rank = map[string]int{"queued": 0, "scheduled": 0, "sending": 0, "sent": 1, "delayed": 2, "delivered": 3, "failed": 4, "bounced": 4, "suppressed": 4, "complained": 5}

// ErrBadCallback means the callback URL's token does not match.
var ErrBadCallback = errors.New("unknown callback")

// HandleCallback applies a provider's delivery events. It returns how
// many events were new.
func (s *Service) HandleCallback(ctx context.Context, providerID, token string, r *http.Request, body []byte) (int, error) {
	p, err := s.loadProvider(ctx, s.Pool, "", providerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrBadCallback
	}
	if err != nil {
		return 0, err
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(p.Secrets["token"])) != 1 {
		return 0, ErrBadCallback
	}
	c, ok := s.Store.Registry.Get(p.Connector)
	sender, isSender := c.(connectors.EmailSender)
	if !ok || !isSender {
		return 0, ErrBadCallback
	}
	events, err := sender.ParseEvents(ctx, s.Store.Client, r, body, p.Secrets)
	if err != nil {
		track.Fail(ctx, s.Log, track.Event{Name: "email_provider.callback_failed", WorkspaceID: p.WorkspaceID, Surface: "system",
			Props: map[string]any{"connector": p.Connector, "kind": connectors.KindOf(err)}}, "provider_callback_invalid", err)
		return 0, err
	}
	n := 0
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		for _, ev := range events {
			added, err := s.apply(ctx, tx, p, ev)
			if err != nil {
				return err
			}
			if added {
				n++
			}
		}
		// Events arriving means they work, whichever way they were set up.
		_, err := tx.Exec(ctx, `UPDATE email_providers SET last_event_at = now(),
			events_status = CASE WHEN events_status IN ('manual', 'failed', 'not_set_up', 'setting_up') AND $2 THEN 'active' ELSE events_status END,
			events_error = CASE WHEN $2 THEN NULL ELSE events_error END
			WHERE id = $1`, p.ID, len(events) > 0)
		return err
	})
	return n, err
}

// apply records one delivery event on its email.
func (s *Service) apply(ctx context.Context, tx pgx.Tx, p *provider, ev connectors.ProviderEvent) (bool, error) {
	if ev.MessageID == "" {
		return false, nil
	}
	var id, status string
	var to []string
	var ownOpens, ownClicks bool
	err := tx.QueryRow(ctx, `SELECT id, status, to_addresses, track_opens, track_clicks FROM emails WHERE provider_id = $1 AND provider_message_id = $2 FOR UPDATE`,
		p.ID, ev.MessageID).Scan(&id, &status, &to, &ownOpens, &ownClicks)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // sent by something else on the same account
	}
	if err != nil {
		return false, err
	}
	if ev.EventID == "" {
		ev.EventID = ev.Type + ":" + ev.Recipient + ":" + ev.At.UTC().Format(time.RFC3339Nano)
	}
	detail := ev.Reason
	if ev.Type == connectors.EventBounced && ev.BounceType != "" {
		detail = strings.TrimSpace(ev.BounceType + " bounce. " + ev.Reason)
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO email_events (workspace_id, email_id, type, recipient, detail, provider_event_id, occurred_at)
		VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), $6,
			-- Providers stamp to the second or worse: never before what the
			-- timeline already has.
			GREATEST(COALESCE($7, now()), (SELECT max(occurred_at) FROM email_events WHERE email_id = $2)))
		ON CONFLICT (email_id, provider_event_id) WHERE provider_event_id IS NOT NULL DO NOTHING`,
		p.WorkspaceID, id, ev.Type, ev.Recipient, detail, ev.EventID, nullTime(ev.At))
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	next, known := map[string]string{
		connectors.EventSent: "sent", connectors.EventDelivered: "delivered", connectors.EventDelayed: "delayed",
		connectors.EventBounced: "bounced", connectors.EventComplaint: "complained", connectors.EventFailed: "failed",
	}[ev.Type]
	if !known {
		// Opens and clicks the provider tracked go on the timeline, and
		// count unless Outflare tracks them itself.
		if (ev.Type == connectors.EventOpened && !ownOpens) || (ev.Type == connectors.EventClicked && !ownClicks) {
			at := ev.At
			if at.IsZero() {
				at = time.Now()
			}
			return true, s.count(ctx, tx, p.WorkspaceID, id, ev.Type, at)
		}
		return true, nil
	}
	// A soft bounce is a temporary failure while the provider keeps trying.
	if ev.Type == connectors.EventBounced && ev.BounceType == "soft" {
		next = "delayed"
	}
	moved := rank[next] > rank[status]
	if moved {
		var bounceType *string
		if ev.Type == connectors.EventBounced {
			bounceType = &ev.BounceType
		}
		if _, err := tx.Exec(ctx, `
			UPDATE emails SET status = $2, status_reason = NULLIF($3, ''), bounce_type = COALESCE($4, bounce_type),
				delivered_at = CASE WHEN $2 = 'delivered' THEN COALESCE($5, now()) ELSE delivered_at END, updated_at = now()
			WHERE id = $1`, id, next, detail, bounceType, nullTime(ev.At)); err != nil {
			return false, err
		}
	}
	recipient := strings.ToLower(ev.Recipient)
	if recipient == "" && len(to) == 1 {
		recipient = strings.ToLower(to[0])
	}
	if state := timelineState(ev); state != "" {
		var who []string
		if recipient != "" {
			who = []string{recipient}
		}
		at := ev.At
		if at.IsZero() {
			at = time.Now()
		}
		if err := onTimeline(ctx, tx, p.WorkspaceID, id, state, who, map[string]any{"reason": detail}, at); err != nil {
			return false, err
		}
	}
	data := map[string]any{"provider_id": p.ID, "connector": p.Connector}
	switch {
	case ev.Type == connectors.EventBounced && ev.BounceType == "hard":
		if recipient != "" {
			if _, err := leads.AddSuppressions(ctx, tx, p.WorkspaceID, "bounced", "email_provider", p.Connector+" hard bounce", []leads.Suppression{{Kind: "email", Value: recipient}}); err != nil {
				return false, err
			}
		}
		data["bounce_type"] = "hard"
		return true, s.record(ctx, tx, System, p.WorkspaceID, "email.bounced", "email", id, data)
	case ev.Type == connectors.EventComplaint:
		if recipient != "" {
			if _, err := leads.AddSuppressions(ctx, tx, p.WorkspaceID, "complained", "email_provider", p.Connector+" spam complaint", []leads.Suppression{{Kind: "email", Value: recipient}}); err != nil {
				return false, err
			}
		}
		return true, s.record(ctx, tx, System, p.WorkspaceID, "email.complained", "email", id, data)
	case moved && (next == "delivered" || next == "failed"):
		return true, s.record(ctx, tx, System, p.WorkspaceID, "email."+next, "email", id, data)
	}
	return true, nil
}

// ---- Unsubscribe ----

// UnsubscribeURL is the one-click unsubscribe link for a lifecycle email.
// It shares /u/ with sequence emails; the e. prefix tells them apart.
func (s *Service) UnsubscribeURL(emailID string) string {
	return strings.TrimRight(s.PublicURL, "/") + "/u/e." + emailID + "." + s.Box.Sign("email_unsubscribe", emailID)
}

// ParseUnsubscribe returns the email id in a link token, if it is one of
// this package's and genuine.
func (s *Service) ParseUnsubscribe(token string) (string, bool) {
	rest, ok := strings.CutPrefix(token, "e.")
	if !ok {
		return "", false
	}
	id, sig, ok := strings.Cut(rest, ".")
	if !ok || len(id) != 36 || !s.Box.Verify("email_unsubscribe", id, sig) {
		return "", false
	}
	return id, true
}

// Unsubscribe acts on an email's unsubscribe link or its recipients' mail
// app button. An email with a topic withdraws them from that topic only;
// one without suppresses them for all lifecycle email.
func (s *Service) Unsubscribe(ctx context.Context, emailID, requestID string) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var ws string
		var to []string
		var topic *string
		err := tx.QueryRow(ctx, `SELECT e.workspace_id, e.to_addresses, tp.key FROM emails e
			LEFT JOIN email_topics tp ON tp.id = e.topic_id WHERE e.id = $1`, emailID).Scan(&ws, &to, &topic)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if topic != nil {
			changed, err := s.unsubscribeTopic(ctx, tx, ws, emailID, *topic, to, requestID)
			if err != nil || !changed {
				return err
			}
			if err := addEvent(ctx, tx, ws, emailID, "unsubscribed", "", "from the topic "+*topic, ""); err != nil {
				return err
			}
			return s.record(ctx, tx, System, ws, "email.unsubscribed", "email", emailID, map[string]any{"scope": "topic"})
		}
		// Also on their customer record, so every feature sees it.
		for _, a := range to {
			rec, err := customers.EnsurePerson(ctx, tx, ws, "email", a, true, requestID)
			if err != nil {
				return err
			}
			if err := customers.RecordConsent(ctx, tx, ws, customers.ConsentChange{RecordID: rec, Channel: "email", Status: customers.Withdrawn,
				Source: "unsubscribe_link", Evidence: map[string]any{"email_id": emailID}, RequestID: requestID}); err != nil {
				return err
			}
		}
		var entries []leads.Suppression
		for _, a := range to {
			entries = append(entries, leads.Suppression{Kind: "email", Value: strings.ToLower(a)})
		}
		n, err := leads.AddSuppressions(ctx, tx, ws, "unsubscribed", "email_link", "", entries)
		if err != nil || n == 0 {
			return err
		}
		if err := addEvent(ctx, tx, ws, emailID, "unsubscribed", "", "", "outflare:unsubscribed"); err != nil {
			return err
		}
		return s.record(ctx, tx, System, ws, "email.unsubscribed", "email", emailID, map[string]any{"scope": "all"})
	})
}
