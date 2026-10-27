package sending

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/mailbox"
	"github.com/outflareai/outflare/engine/internal/policy"
)

// ErrInboundNotFound means no such received message in the workspace.
var ErrInboundNotFound = errors.New("message not found")

// Reply answers a received message from the mailbox it arrived at, in the
// same thread. It goes through the recipient check and needs an active
// mailbox. A person writing back to a prospect is not held to the
// sequence's window or the mailbox's pacing, but the send counts towards
// the mailbox's daily limit. A reply a sequencer received (EmailBison) is
// answered through that sequencer, after the same recipient check.
func (e *Engine) Reply(ctx context.Context, workspaceID, inboundID, body string) (string, error) {
	var email Email
	var acct *mailbox.Account
	var rowID string
	var via *viaPlatform
	err := pgx.BeginFunc(ctx, e.Pool, func(tx pgx.Tx) error {
		var mailboxID, from, fromName, subject, msgID, refs string
		var leadID, integrationID, externalID *string
		err := tx.QueryRow(ctx, `
			SELECT mailbox_id, from_email, from_name, subject, message_id, references_ids, lead_id, integration_id::text, external_id
			FROM inbound_messages WHERE id = $1 AND workspace_id = $2`, inboundID, workspaceID,
		).Scan(&mailboxID, &from, &fromName, &subject, &msgID, &refs, &leadID, &integrationID, &externalID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInboundNotFound
		}
		if err != nil {
			return err
		}
		d, err := policy.CheckRecipients(ctx, tx, workspaceID, []string{from})
		if err != nil {
			return err
		}
		if !d[0].Allowed {
			return ErrBlocked{strings.Join(d[0].Reasons, ", ")}
		}
		if externalID != nil {
			if integrationID == nil {
				return ErrBlocked{"the connection this reply came through was removed"}
			}
			via, err = e.platformReply(ctx, tx, workspaceID, inboundID, mailboxID, *integrationID, *externalID, from, fromName, subject, body, leadID)
			if via != nil {
				rowID = via.rowID
			}
			return err
		}
		if acct, err = e.Mail.Load(ctx, tx, workspaceID, mailboxID); err != nil {
			return err
		}
		if acct.Status != "active" {
			return ErrBlocked{policy.MailboxInactive}
		}
		var references []string
		for _, id := range append(strings.Fields(refs), msgID) {
			if id != "" {
				references = append(references, "<"+strings.Trim(id, "<>")+">")
			}
		}
		email = Email{
			FromName: acct.DisplayName, From: acct.Email, To: from,
			Subject:   "Re: " + strings.TrimSpace(trimRe(subject)),
			Body:      body,
			MessageID: newID() + "@" + acct.Email[strings.LastIndexByte(acct.Email, '@')+1:],
			InReplyTo: strings.Trim(msgID, "<>"), References: strings.Join(references, " "),
			Date: e.now(),
		}
		if msgID == "" {
			email.InReplyTo = ""
		}
		if _, err := tx.Exec(ctx, `UPDATE inbound_messages SET read = true WHERE id = $1`, inboundID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			INSERT INTO messages (workspace_id, lead_id, mailbox_id, from_email, to_email, subject, body, message_id,
				status, attempts, created_at, reply_to_inbound_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'sending', 1, $9, $10) RETURNING id`,
			workspaceID, leadID, mailboxID, email.From, email.To, email.Subject, body, email.MessageID, email.Date, inboundID,
		).Scan(&rowID)
	})
	if err != nil {
		return "", err
	}
	if via != nil {
		return rowID, e.sendViaPlatform(ctx, workspaceID, via)
	}
	_, sendErr := e.Mail.Dialer.Send(ctx, acct.SMTP, acct.SMTPCred, email.From, []string{email.To}, strings.NewReader(string(email.Bytes())))
	status, errText := "sent", ""
	if sendErr != nil {
		status, errText = "failed", sendErr.Error()
	}
	if _, err := e.Pool.Exec(ctx, `UPDATE messages SET status = $2, error = NULLIF($3, ''), sent_at = CASE WHEN $2 = 'sent' THEN $4::timestamptz END WHERE id = $1`,
		rowID, status, errText, e.now()); err != nil {
		return rowID, err
	}
	return rowID, sendErr
}

func trimRe(s string) string {
	for {
		t := strings.TrimSpace(s)
		if len(t) >= 3 && strings.EqualFold(t[:3], "re:") {
			s = t[3:]
			continue
		}
		return t
	}
}

// viaPlatform is a reply to send through the sequencer it came from.
type viaPlatform struct {
	rowID, integrationID string
	out                  connectors.ReplyOut
}

// platformReply checks the sender email and records the reply as
// sending, in the caller's transaction.
func (e *Engine) platformReply(ctx context.Context, tx pgx.Tx, ws, inboundID, mailboxID, integrationID, externalID,
	to, toName, subject, body string, leadID *string) (*viaPlatform, error) {
	if e.Platforms == nil {
		return nil, ErrBlocked{"answering through a sequencer is not available here"}
	}
	var from, sender, status string
	err := tx.QueryRow(ctx, `
		SELECT m.email, mp.external_id, m.status FROM mailboxes m
		JOIN mailbox_platforms mp ON mp.mailbox_id = m.id AND mp.integration_id = $3
		WHERE m.id = $1 AND m.workspace_id = $2`, mailboxID, ws, integrationID).Scan(&from, &sender, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrBlocked{"the sequencer no longer shows the sender email this reply arrived at"}
	}
	if err != nil {
		return nil, err
	}
	if status != "active" {
		return nil, ErrBlocked{policy.MailboxInactive}
	}
	if _, err := tx.Exec(ctx, `UPDATE inbound_messages SET read = true WHERE id = $1`, inboundID); err != nil {
		return nil, err
	}
	v := &viaPlatform{integrationID: integrationID, out: connectors.ReplyOut{ReplyExternalID: externalID,
		MailboxExternalID: sender, ToEmail: to, ToName: toName, Body: body}}
	err = tx.QueryRow(ctx, `
		INSERT INTO messages (workspace_id, lead_id, mailbox_id, from_email, to_email, subject, body, message_id,
			status, attempts, created_at, reply_to_inbound_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'sending', 1, $9, $10) RETURNING id`,
		ws, leadID, mailboxID, from, to, "Re: "+strings.TrimSpace(trimRe(subject)), body, "", e.now(), inboundID,
	).Scan(&v.rowID)
	return v, err
}

// sendViaPlatform hands the reply to the sequencer and records how it
// went.
func (e *Engine) sendViaPlatform(ctx context.Context, ws string, v *viaPlatform) error {
	sendErr := func() error {
		op, err := e.Platforms.Open(ctx, e.Pool, ws, v.integrationID)
		if err != nil {
			return err
		}
		src, ok := op.Connector.(connectors.ReplySource)
		if !ok {
			return connectors.Errf(connectors.ErrInvalid, "%s cannot send replies", op.Integration.Name)
		}
		return src.SendReply(ctx, op.Client, op.Creds, v.out)
	}()
	status, errText := "sent", ""
	if sendErr != nil {
		status, errText = "failed", sendErr.Error()
	}
	if _, err := e.Pool.Exec(ctx, `UPDATE messages SET status = $2, error = NULLIF($3, ''), sent_at = CASE WHEN $2 = 'sent' THEN $4::timestamptz END WHERE id = $1`,
		v.rowID, status, errText, e.now()); err != nil {
		return err
	}
	return sendErr
}
