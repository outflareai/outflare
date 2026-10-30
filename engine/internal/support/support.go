// Package support is the support inbox: conversations with customers who
// write in, answered by the team. Mail reaches it from a connected mailbox
// marked for support (package inbox hands it over instead of storing it in
// the sales inbox) or from a forwarding address; replies go out in a job,
// through the mailbox the conversation came in on.
package support

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/mailbox"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/policy"
	"github.com/outflareai/outflare/engine/internal/sending"
)

// Mailbox purposes (mailboxes.purpose).
const (
	PurposeOutreach = "outreach"
	PurposeSupport  = "support"
)

// Conversation statuses.
const (
	StatusOpen    = "open"
	StatusPending = "pending"
	StatusClosed  = "closed"
)

// Statuses lists every conversation status.
var Statuses = []string{StatusOpen, StatusPending, StatusClosed}

// JobSend sends one reply.
const JobSend = "support.send"

// threadFor is how long a new email with the same sender and subject joins
// an earlier conversation when its headers don't say which it answers.
const threadFor = 30 * 24 * time.Hour

// Email is one email a customer sent to support.
type Email struct {
	WorkspaceID string
	BrandID     *string
	// The mailbox it arrived in, and its place there so a sync never
	// stores it twice. UID is 0 for mail that came another way.
	MailboxID        string
	UIDValidity, UID int64
	// Or the support address it was forwarded to (inbound email), the
	// forwarded message, and the business's own address the customer
	// wrote to, which replies go out from.
	InboundAddressID, InboundEmailID string
	ReplyFrom                        string
	MessageID                        string
	InReplyTo                        string
	References                       []string
	From, FromName                   string
	To                               string
	Subject, Body                    string
	Received                         time.Time
	AutoReply                        bool
}

// Received is what Intake did with an email.
type Received struct {
	MessageID, ConversationID string
	// New is false when the email was stored before.
	New bool
	// Started is true when the email began a conversation.
	Started bool
}

func cleanID(id string) string { return strings.Trim(strings.TrimSpace(id), "<>") }

// Subject without the Re: and Fwd: in front.
func baseSubject(s string) string {
	for {
		t := strings.TrimSpace(s)
		l := strings.ToLower(t)
		switch {
		case strings.HasPrefix(l, "re:"):
			s = t[3:]
		case strings.HasPrefix(l, "fw:"):
			s = t[3:]
		case strings.HasPrefix(l, "fwd:"):
			s = t[4:]
		default:
			return t
		}
	}
}

// Intake stores an email a customer sent and files it in its
// conversation: the one its headers answer, else a recent one from the
// same address with the same subject, else a new one. The customer gets a
// record if they have none. Run it in the transaction that marks the email
// as read from its source.
func Intake(ctx context.Context, tx pgx.Tx, e Email) (Received, error) {
	var out Received
	ws := e.WorkspaceID
	from := strings.ToLower(strings.TrimSpace(e.From))
	if e.UID != 0 {
		err := tx.QueryRow(ctx, `SELECT id::text, conversation_id::text FROM support_messages
			WHERE mailbox_id = $1 AND uidvalidity = $2 AND uid = $3`, e.MailboxID, e.UIDValidity, e.UID).Scan(&out.MessageID, &out.ConversationID)
		if err == nil {
			return out, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return out, err
		}
	}
	if e.Received.IsZero() {
		e.Received = time.Now()
	}
	var ids []string
	for _, id := range append([]string{e.InReplyTo}, e.References...) {
		if id = cleanID(id); id != "" {
			ids = append(ids, id)
		}
	}
	// The conversation the email answers.
	var conv string
	if len(ids) > 0 {
		err := tx.QueryRow(ctx, `SELECT conversation_id::text FROM support_messages
			WHERE workspace_id = $1 AND message_id = ANY($2) ORDER BY created_at DESC LIMIT 1`, ws, ids).Scan(&conv)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return out, err
		}
	}
	if conv == "" && from != "" {
		err := tx.QueryRow(ctx, `SELECT id::text FROM support_conversations
			WHERE workspace_id = $1 AND mailbox_id IS NOT DISTINCT FROM $2::uuid AND inbound_address_id IS NOT DISTINCT FROM $6::uuid
			  AND contact_email = $3 AND lower(subject) = lower($4)
			  AND last_message_at > $5 ORDER BY last_message_at DESC LIMIT 1`,
			ws, nilIfEmpty(e.MailboxID), from, baseSubject(e.Subject), e.Received.Add(-threadFor), nilIfEmpty(e.InboundAddressID)).Scan(&conv)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return out, err
		}
	}
	var record *string
	if from != "" {
		ids, err := customers.EnsurePeople(ctx, tx, ws, "first_party", "support", []customers.Person{{Email: from, Name: e.FromName}})
		if err != nil {
			return out, err
		}
		if id, ok := ids[from]; ok {
			record = &id
		}
	}
	if conv == "" {
		out.Started = true
		err := tx.QueryRow(ctx, `
			INSERT INTO support_conversations (workspace_id, brand_id, record_id, mailbox_id, subject, contact_email,
				first_message_at, last_message_at, inbound_address_id, reply_from)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $7, $8, $9) RETURNING id::text`,
			ws, e.BrandID, record, nilIfEmpty(e.MailboxID), truncate(baseSubject(e.Subject), 1000), from, e.Received,
			nilIfEmpty(e.InboundAddressID), nilIfEmpty(truncate(strings.ToLower(e.ReplyFrom), 320))).Scan(&conv)
		if err != nil {
			return out, err
		}
	} else {
		// A person writing opens it again; an automatic answer doesn't.
		if _, err := tx.Exec(ctx, `
			UPDATE support_conversations SET last_message_at = GREATEST(last_message_at, $2), last_from = 'customer', unread = true,
				status = CASE WHEN $3 THEN status ELSE 'open' END,
				record_id = COALESCE(record_id, $4), reply_from = COALESCE(reply_from, $5), updated_at = now()
			WHERE id = $1`, conv, e.Received, e.AutoReply, record, nilIfEmpty(truncate(strings.ToLower(e.ReplyFrom), 320))); err != nil {
			return out, err
		}
	}
	var uv, uid *int64
	if e.UID != 0 {
		uv, uid = &e.UIDValidity, &e.UID
	}
	err := tx.QueryRow(ctx, `
		INSERT INTO support_messages (workspace_id, conversation_id, kind, record_id, from_email, from_name, to_email, subject, body,
			message_id, in_reply_to, references_ids, auto_reply, status, mailbox_id, uidvalidity, uid, created_at, inbound_email_id)
		VALUES ($1, $2, 'in', $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 'received', $13, $14, $15, $16, $17) RETURNING id::text`,
		ws, conv, record, from, truncate(e.FromName, 300), strings.ToLower(e.To), truncate(e.Subject, 1000), truncate(e.Body, 200000),
		cleanID(e.MessageID), cleanID(e.InReplyTo), strings.Join(ids, " "), e.AutoReply, nilIfEmpty(e.MailboxID), uv, uid, e.Received,
		nilIfEmpty(e.InboundEmailID),
	).Scan(&out.MessageID)
	if err != nil {
		return out, err
	}
	out.ConversationID, out.New = conv, true
	action := "support.message_received"
	if out.Started {
		action = "support.conversation_started"
	}
	metrics.SupportMessage("in", "success")
	via := "mailbox"
	if e.InboundAddressID != "" {
		via = "forwarding"
	}
	return out, audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: action,
		TargetType: "support_conversation", TargetID: conv,
		Data:  map[string]any{"message_id": out.MessageID, "auto_reply": e.AutoReply, "channel": "email", "via": via},
		Props: map[string]any{"auto_reply": e.AutoReply, "channel": "email", "via": via}})
}

// Reply is an answer the team writes to a conversation.
type Reply struct {
	WorkspaceID, ConversationID string
	AuthorID                    string // the user, or "" for a token
	Body                        string
	// Status to set the conversation to once the reply is queued:
	// pending (the default) or closed.
	Status string
	// Provider sends replies to conversations that came in by forwarding,
	// through the workspace's email provider; Who is who asked.
	Provider Provider
	Who      emails.Who
}

// Provider queues email through the workspace's email provider (package
// emails).
type Provider interface {
	Compose(ctx context.Context, q jobs.Querier, d emails.Draft) (emails.NewEmail, error)
	Queue(ctx context.Context, tx pgx.Tx, who emails.Who, e emails.NewEmail) (string, error)
}

// ErrNotFound means no such conversation in the workspace.
var ErrNotFound = errors.New("conversation not found")

// ErrBlocked means the reply can't be sent, and says why.
type ErrBlocked struct{ Reason string }

func (e ErrBlocked) Error() string { return e.Reason }

// ErrProvider is an email provider's refusal to take a reply, with the
// API's status and code (no provider, an unverified sending domain).
type ErrProvider struct{ *emails.ComposeError }

// Blocked reasons, for the API's error codes.
const (
	BlockedNoMailbox = "the mailbox this conversation came in on was removed or no longer answers support: connect it again, or reply from your own email"
	BlockedInactive  = "the support mailbox is not active: reconnect it under Settings, Mailboxes"
	BlockedNoReplyTo = "Outflare doesn't know which of your addresses this customer wrote to, so it can't reply from it"
)

// QueueReply checks a reply, stores it as sending and queues what sends
// it, in tx: the support job for a conversation from a mailbox, or an
// email through the workspace's provider for one that was forwarded. It
// answers the new message's id.
func QueueReply(ctx context.Context, tx pgx.Tx, r Reply) (string, error) {
	var mailboxID, inboundID, replyFrom, brandID *string
	var contact, subject string
	var mbStatus, mbPurpose, mbEmail, mbName *string
	err := tx.QueryRow(ctx, `
		SELECT c.mailbox_id::text, c.inbound_address_id::text, c.reply_from, c.brand_id::text, c.contact_email, c.subject,
			m.status, m.purpose, m.email, m.display_name
		FROM support_conversations c LEFT JOIN mailboxes m ON m.id = c.mailbox_id
		WHERE c.id = $1 AND c.workspace_id = $2 FOR UPDATE OF c`, r.ConversationID, r.WorkspaceID,
	).Scan(&mailboxID, &inboundID, &replyFrom, &brandID, &contact, &subject, &mbStatus, &mbPurpose, &mbEmail, &mbName)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	forwarded := mailboxID == nil && inboundID != nil
	switch {
	case forwarded:
		if replyFrom == nil {
			return "", ErrBlocked{BlockedNoReplyTo}
		}
		if r.Provider == nil {
			return "", ErrBlocked{"replies through your email provider aren't available here"}
		}
	case mailboxID == nil || mbPurpose == nil || *mbPurpose != PurposeSupport:
		return "", ErrBlocked{BlockedNoMailbox}
	case *mbStatus != "active":
		return "", ErrBlocked{BlockedInactive}
	}
	// Answering someone who wrote in is mail they asked for: an unsubscribe
	// from marketing doesn't stop it, a bounce or a block does.
	d, err := policy.CheckPurpose(ctx, tx, r.WorkspaceID, policy.PurposeTransactional, []string{contact})
	if err != nil {
		return "", err
	}
	if !d[0].Allowed {
		return "", ErrBlocked{"this address can't be emailed: " + strings.Join(d[0].Reasons, ", ")}
	}
	// Thread it under the customer's latest email.
	var inReplyTo, refs string
	if err := tx.QueryRow(ctx, `
		SELECT message_id, references_ids FROM support_messages
		WHERE conversation_id = $1 AND kind IN ('in', 'out') AND message_id <> '' ORDER BY created_at DESC LIMIT 1`,
		r.ConversationID).Scan(&inReplyTo, &refs); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if inReplyTo != "" {
		refs = strings.TrimSpace(refs + " " + inReplyTo)
	}
	subj := "Re: " + subject
	if subject == "" {
		subj = "Re: your message"
	}

	var id, from, fromName, msgID string
	var emailID, mbID *string
	if forwarded {
		from = *replyFrom
		if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT name FROM brands WHERE id = $2), (SELECT name FROM workspaces WHERE id = $1))`,
			r.WorkspaceID, brandID).Scan(&fromName); err != nil {
			return "", err
		}
		e, err := r.Provider.Compose(ctx, tx, emails.Draft{
			NewEmail: emails.NewEmail{WorkspaceID: r.WorkspaceID, Purpose: policy.PurposeTransactional, BrandID: brandID,
				From: mailAddress(fromName, from), To: []string{contact}, ReplyTo: []string{from},
				Trigger: "system", TriggerRef: "support:" + r.ConversationID, InReplyTo: inReplyTo, References: refs},
			// The text goes in as data, so nothing in it is read as a
			// template.
			Subject: ptr("{{subject}}"), Text: ptr("{{{body}}}"),
			Data: map[string]any{"subject": subj, "body": r.Body},
		})
		var ce *emails.ComposeError
		if errors.As(err, &ce) {
			return "", ErrProvider{ce}
		}
		if err != nil {
			return "", err
		}
		eid, err := r.Provider.Queue(ctx, tx, r.Who, e)
		if err != nil {
			return "", err
		}
		emailID = &eid
	} else {
		from, fromName, mbID = *mbEmail, *mbName, mailboxID
		msgID = newID() + "@" + from[strings.LastIndexByte(from, '@')+1:]
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO support_messages (workspace_id, conversation_id, kind, record_id, author_id, from_email, from_name, to_email,
			subject, body, message_id, in_reply_to, references_ids, status, mailbox_id, email_id)
		SELECT $1, $2, 'out', c.record_id, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'sending', $12, $13
		FROM support_conversations c WHERE c.id = $2 RETURNING id::text`,
		r.WorkspaceID, r.ConversationID, nilIfEmpty(r.AuthorID), from, fromName, contact, subj, r.Body, msgID, inReplyTo, refs, mbID, emailID,
	).Scan(&id)
	if err != nil {
		return "", err
	}
	next := r.Status
	if next == "" {
		next = StatusPending
	}
	if _, err := tx.Exec(ctx, `
		UPDATE support_conversations SET last_message_at = now(), last_from = 'team', unread = false, status = $2,
			first_reply_at = COALESCE(first_reply_at, now()),
			closed_at = CASE WHEN $2 = 'closed' THEN now() ELSE closed_at END, updated_at = now()
		WHERE id = $1`, r.ConversationID, next); err != nil {
		return "", err
	}
	if forwarded {
		// The provider sends it; its status is the email's.
		return id, nil
	}
	_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobSend, WorkspaceID: &r.WorkspaceID,
		Payload: map[string]string{"message_id": id}, IdempotencyKey: "support-send:" + id, MaxAttempts: 5})
	return id, err
}

// mailAddress is "Name <address>", quoting the name when it needs it.
func mailAddress(name, addr string) string {
	if name = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ", "\"", "", "<", "", ">", "").Replace(name)); name == "" {
		return addr
	}
	return (&mail.Address{Name: name, Address: addr}).String()
}

func ptr(s string) *string { return &s }

// Sender runs JobSend.
type Sender struct {
	Pool *pgxpool.Pool
	Mail *mailbox.Service
	Log  *slog.Logger
	Now  func() time.Time
}

// Register adds the send job to the queue.
func (s *Sender) Register(q *jobs.Queue) { q.Handle(JobSend, s.job) }

func (s *Sender) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Sender) job(ctx context.Context, j jobs.Job) error {
	var p struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(j.Payload, &p); err != nil || j.WorkspaceID == nil {
		return nil
	}
	ws := *j.WorkspaceID
	var conv, status, mailboxID, from, fromName, to, subject, body, msgID, inReplyTo, refs string
	err := s.Pool.QueryRow(ctx, `
		SELECT conversation_id::text, status, COALESCE(mailbox_id::text, ''), from_email, from_name, to_email, subject, body,
			message_id, in_reply_to, references_ids
		FROM support_messages WHERE id = $1 AND workspace_id = $2 AND kind = 'out'`, p.MessageID, ws,
	).Scan(&conv, &status, &mailboxID, &from, &fromName, &to, &subject, &body, &msgID, &inReplyTo, &refs)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && status != "sending" {
		return nil // deleted with its workspace, or already done
	}
	if err != nil {
		return err
	}
	start := s.now()
	sendErr := func() error {
		if mailboxID == "" {
			return ErrBlocked{BlockedNoMailbox}
		}
		// The address may have bounced since the reply was written.
		d, err := policy.CheckPurpose(ctx, s.Pool, ws, policy.PurposeTransactional, []string{to})
		if err != nil {
			return err
		}
		if !d[0].Allowed {
			return ErrBlocked{"this address can't be emailed: " + strings.Join(d[0].Reasons, ", ")}
		}
		acct, err := s.Mail.Load(ctx, s.Pool, ws, mailboxID)
		if err != nil {
			return err
		}
		if acct.Status != "active" {
			return ErrBlocked{BlockedInactive}
		}
		var references []string
		for _, id := range strings.Fields(refs) {
			references = append(references, "<"+id+">")
		}
		email := sending.Email{FromName: fromName, From: from, To: to, Subject: subject, Body: body,
			MessageID: msgID, InReplyTo: inReplyTo, References: strings.Join(references, " "), Date: s.now()}
		_, err = s.Mail.Dialer.Send(ctx, acct.SMTP, acct.SMTPCred, from, []string{to}, strings.NewReader(string(email.Bytes())))
		return err
	}()
	var blocked ErrBlocked
	var ce *mailbox.ConnError
	retry := sendErr != nil && !errors.As(sendErr, &blocked) && !errors.Is(sendErr, mailbox.ErrNotFound) && j.Attempt < j.MaxAttempts
	if retry && !errors.As(sendErr, &ce) {
		// A database error: try again without marking it.
		return sendErr
	}
	if retry {
		metrics.SupportSent("retry", s.now().Sub(start))
		return sendErr
	}
	outcome, errText := "sent", ""
	if sendErr != nil {
		s.Log.Warn("support reply not sent", "workspace", ws, "message", p.MessageID, "err", sendErr)
		outcome, errText = "failed", sendErr.Error()
		if errors.Is(sendErr, mailbox.ErrNotFound) {
			errText = BlockedNoMailbox
		}
	}
	metrics.SupportSent(outcome, s.now().Sub(start))
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE support_messages SET status = $2, error = NULLIF($3, ''),
			sent_at = CASE WHEN $2 = 'sent' THEN now() END WHERE id = $1`, p.MessageID, outcome, truncate(errText, 1000)); err != nil {
			return err
		}
		if sendErr == nil {
			return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "support.reply_sent",
				TargetType: "support_conversation", TargetID: conv, Data: map[string]any{"message_id": p.MessageID}})
		}
		// Not sent: the customer is waiting on the team again.
		if _, err := tx.Exec(ctx, `UPDATE support_conversations SET status = 'open', updated_at = now() WHERE id = $1`, conv); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "support.reply_failed",
			TargetType: "support_conversation", TargetID: conv, Data: map[string]any{"message_id": p.MessageID}})
	})
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8Start(s[n]) {
		n--
	}
	return s[:n]
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

func newID() string {
	var b [16]byte
	rand.Read(b[:]) //nolint:errcheck // never fails
	return fmt.Sprintf("%x", b)
}
