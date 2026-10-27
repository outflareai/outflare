package inbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/mailbox"
)

// Simulated mail for test workspaces: what a lead's reply, an auto-reply,
// an opt-out or a bounce would look like, delivered into a simulated
// mailbox and handled by the same code as real mail, so sequences stop,
// suppressions are added and webhooks fire just as they would live.

// Kinds of simulated inbound mail.
const (
	SimReply     = "reply"
	SimAutoReply = "auto_reply"
	SimOptOut    = "opt_out"
	SimBounce    = "bounce"
)

// SimKinds lists the kinds Simulate makes.
var SimKinds = []string{SimReply, SimAutoReply, SimOptOut, SimBounce}

// Sent is the email a simulated message answers.
type Sent struct {
	MessageID string // without angle brackets
	From      string // the mailbox
	To        string // the lead
	Subject   string
}

// ErrNotSimulated means the mailbox is a real one, whose inbox only real
// mail reaches.
var ErrNotSimulated = errors.New("not a simulated mailbox")

// Simulate writes the raw message of a kind answering sent. body replaces
// the default text, except for a bounce.
func Simulate(kind string, sent Sent, body string) ([]byte, error) {
	id := simID()
	date := time.Now().UTC().Format(time.RFC1123Z)
	var b strings.Builder
	header := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	header("Date", date)
	header("Message-ID", "<"+id+">")
	header("MIME-Version", "1.0")
	subject := strings.TrimSpace(sent.Subject)
	switch kind {
	case SimReply, SimAutoReply, SimOptOut:
		if body == "" {
			body = map[string]string{
				SimReply:     "Thanks for reaching out. This looks interesting: can we talk next week?",
				SimAutoReply: "I'm out of the office until Monday with limited access to email.",
				SimOptOut:    "Please remove me from your list.",
			}[kind]
		}
		header("From", sent.To)
		header("To", sent.From)
		if kind == SimAutoReply {
			header("Subject", "Automatic reply: "+subject)
			header("Auto-Submitted", "auto-replied")
		} else {
			header("Subject", "Re: "+subject)
		}
		header("In-Reply-To", "<"+sent.MessageID+">")
		header("References", "<"+sent.MessageID+">")
		header("Content-Type", "text/plain; charset=utf-8")
		b.WriteString("\r\n")
		b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
		b.WriteString("\r\n")
	case SimBounce:
		boundary := "bounce-" + id[:12]
		header("From", "Mail Delivery System <mailer-daemon@"+mailbox.SimulatedHost+">")
		header("To", sent.From)
		header("Subject", "Undelivered Mail Returned to Sender")
		header("Content-Type", `multipart/report; report-type=delivery-status; boundary="`+boundary+`"`)
		b.WriteString("\r\n--" + boundary + "\r\n")
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
		b.WriteString("Your message to " + sent.To + " could not be delivered: 550 5.1.1 the email account does not exist (simulated).\r\n")
		b.WriteString("\r\n--" + boundary + "\r\n")
		b.WriteString("Content-Type: message/delivery-status\r\n\r\n")
		b.WriteString("Reporting-MTA: dns; " + mailbox.SimulatedHost + "\r\n\r\n")
		b.WriteString("Final-Recipient: rfc822; " + sent.To + "\r\nAction: failed\r\nStatus: 5.1.1\r\n")
		b.WriteString("Diagnostic-Code: smtp; 550 5.1.1 the email account does not exist\r\n")
		b.WriteString("\r\n--" + boundary + "\r\n")
		b.WriteString("Content-Type: text/rfc822-headers\r\n\r\n")
		b.WriteString("Message-ID: <" + sent.MessageID + ">\r\nFrom: " + sent.From + "\r\nTo: " + sent.To + "\r\nSubject: " + subject + "\r\n")
		b.WriteString("\r\n--" + boundary + "--\r\n")
	default:
		return nil, fmt.Errorf("kind must be one of %s", strings.Join(SimKinds, ", "))
	}
	return []byte(b.String()), nil
}

// Deliver stores raw as mail arriving in a simulated mailbox's inbox and
// acts on it as a sync would. It answers the stored message's id.
func (s *Syncer) Deliver(ctx context.Context, workspaceID, mailboxID string, raw []byte) (string, error) {
	acct, err := s.Mail.Load(ctx, s.Pool, workspaceID, mailboxID)
	if err != nil {
		return "", err
	}
	if !mailbox.Simulated(acct.IMAP) {
		return "", ErrNotSimulated
	}
	p, err := Parse(raw)
	if err != nil {
		return "", err
	}
	var id string
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		// Simulated mail gets the next UID, as a real inbox would give it.
		var uid int64
		if err := tx.QueryRow(ctx, `
			UPDATE mailboxes SET imap_uidvalidity = 1, imap_last_uid = imap_last_uid + 1, last_synced_at = now()
			WHERE id = $1 RETURNING imap_last_uid`, acct.ID).Scan(&uid); err != nil {
			return err
		}
		if _, err := s.store(ctx, tx, acct, 1, uid, time.Now(), p); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT id::text FROM inbound_messages WHERE mailbox_id = $1 AND uidvalidity = 1 AND uid = $2`,
			acct.ID, uid).Scan(&id)
	})
	return id, err
}

func simID() string {
	b := make([]byte, 16)
	rand.Read(b) //nolint:errcheck // crypto/rand does not fail
	return hex.EncodeToString(b) + "@" + mailbox.SimulatedHost
}
