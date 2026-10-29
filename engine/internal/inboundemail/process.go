package inboundemail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/mail"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-msgauth/dkim"
	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Process handles a JobProcess job.
func (s *Service) Process(ctx context.Context, j jobs.Job) error {
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(j.Payload, &p); err != nil {
		return err
	}
	return s.process(ctx, p.ID)
}

func (s *Service) process(ctx context.Context, id string) error {
	var e Email
	var status string
	err := s.Pool.QueryRow(ctx, `SELECT m.id::text, m.workspace_id::text, m.purpose, m.received_at, COALESCE(m.message_id, ''), m.status, COALESCE(m.raw, ''),
			a.id::text, a.brand_id::text, a.local_part
		FROM inbound_emails m JOIN inbound_addresses a ON a.id = m.address_id WHERE m.id::text = $1`, id).
		Scan(&e.ID, &e.WorkspaceID, &e.Purpose, &e.ReceivedAt, &e.MessageID, &status, &e.Raw, &e.AddressID, &e.BrandID, &e.To)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != "received" && status != "failed") {
		return nil
	}
	if err != nil {
		return err
	}
	msg, err := mail.ReadMessage(bytes.NewReader(e.Raw))
	if err != nil {
		return s.finish(ctx, e, "rejected", "unreadable", nil)
	}
	e.Subject = decodeHeader(msg.Header.Get("Subject"))
	if a, err := mail.ParseAddress(msg.Header.Get("From")); err == nil {
		e.From = strings.ToLower(a.Address)
	}
	e.DKIM = s.verifyDKIM(e.Raw)
	e.ForwardedBy = forwardedBy(msg.Header)

	e.To += "@" + s.domain
	p, registered := s.purposes[e.Purpose]

	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE inbound_addresses SET last_email_at = now() WHERE id = $1`, e.AddressID); err != nil {
			return err
		}
		// Gmail asks the address to confirm before it forwards to it.
		if e.From == "forwarding-noreply@google.com" && e.SignedBy("google.com") {
			confirm := s.gmailConfirmation
			if p.AnyForwarder {
				confirm = s.addressConfirmation
			}
			st, reason, err := confirm(ctx, tx, &e, msg)
			if err != nil {
				return err
			}
			return s.save(ctx, tx, e, st, reason)
		}
		if !p.AnyForwarder {
			ok, err := s.forwarder(ctx, tx, &e, msg.Header)
			if err != nil {
				return err
			}
			if !ok {
				return s.save(ctx, tx, e, "rejected", "unknown_forwarder")
			}
		}
		h := p.Handler
		if !registered || h == nil {
			return s.save(ctx, tx, e, "ignored", "no_handler")
		}
		st, reason, err := h(ctx, tx, e)
		if err != nil {
			return err
		}
		return s.save(ctx, tx, e, st, reason)
	})
	if err != nil {
		track.Fail(ctx, s.Log, track.Event{Name: "inbound_email.process_failed", Surface: "job", WorkspaceID: e.WorkspaceID,
			Props: map[string]any{"purpose": e.Purpose}}, "internal", err)
		_, _ = s.Pool.Exec(ctx, `UPDATE inbound_emails SET status = 'failed', reason = 'internal' WHERE id::text = $1 AND status = 'received'`, e.ID)
		observe(e.Purpose, "failed")
	}
	return err
}

// finish saves a result outside a transaction of its own.
func (s *Service) finish(ctx context.Context, e Email, status, reason string, _ error) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error { return s.save(ctx, tx, e, status, reason) })
}

func (s *Service) save(ctx context.Context, tx pgx.Tx, e Email, status, reason string) error {
	var r *string
	if reason != "" {
		r = &reason
	}
	clip := func(v string) *string {
		if v == "" {
			return nil
		}
		if len(v) > 254 {
			v = v[:254]
		}
		return &v
	}
	subject := e.Subject
	if len(subject) > 998 {
		subject = subject[:998]
	}
	if _, err := tx.Exec(ctx, `UPDATE inbound_emails SET status = $2, reason = $3, from_address = $4, forwarded_by = $5, subject = $6,
		dkim_domains = $7, processed_at = now() WHERE id::text = $1`,
		e.ID, status, r, clip(e.From), clip(e.ForwardedBy), subject, e.DKIM); err != nil {
		return err
	}
	if err := recordProcessed(ctx, tx, e, status, reason); err != nil {
		return err
	}
	observe(e.Purpose, status)
	return nil
}

// verifyDKIM returns the domains of the message's valid DKIM signatures.
func (s *Service) verifyDKIM(raw []byte) []string {
	opts := &dkim.VerifyOptions{LookupTXT: s.lookupTXT}
	if opts.LookupTXT == nil {
		opts.LookupTXT = net.LookupTXT
	}
	vs, err := dkim.VerifyWithOptions(bytes.NewReader(raw), opts)
	out := []string{}
	if err != nil {
		return out
	}
	for _, v := range vs {
		d := strings.ToLower(v.Domain)
		if v.Err == nil && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	return out
}

// forwardedBy is the address a message was forwarded from: Gmail and
// Outlook name it in X-Forwarded-For ("from@example.com to@..."), others
// in Resent-From.
func forwardedBy(h mail.Header) string {
	if f := strings.Fields(h.Get("X-Forwarded-For")); len(f) > 0 {
		return strings.ToLower(strings.Trim(f[0], "<>,"))
	}
	if a, err := mail.ParseAddress(h.Get("Resent-From")); err == nil {
		return strings.ToLower(a.Address)
	}
	return ""
}

// forwarder works out which member a message came through: the address
// that forwarded it, or, for mail a Google Workspace rule copied, a
// member's address among its recipients. It must be a member's own
// address or one they linked.
func (s *Service) forwarder(ctx context.Context, tx pgx.Tx, e *Email, h mail.Header) (bool, error) {
	candidates := []string{}
	if e.ForwardedBy != "" {
		candidates = append(candidates, e.ForwardedBy)
	}
	for _, field := range []string{"Delivered-To", "To", "Cc"} {
		if list, err := h.AddressList(field); err == nil {
			for _, a := range list {
				candidates = append(candidates, strings.ToLower(a.Address))
			}
		}
	}
	for _, c := range candidates {
		var userID string
		err := tx.QueryRow(ctx, `
			SELECT u.id::text FROM memberships m JOIN users u ON u.id = m.user_id WHERE m.workspace_id = $1 AND lower(u.email) = $2
			UNION ALL
			SELECT f.user_id::text FROM inbound_forwarders f WHERE f.workspace_id = $1 AND f.email = $2
			LIMIT 1`, e.WorkspaceID, c).Scan(&userID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, err
		}
		e.ForwardedBy, e.ForwarderID = c, userID
		_, err = tx.Exec(ctx, `UPDATE inbound_forwarders SET last_email_at = now() WHERE workspace_id = $1 AND email = $2`, e.WorkspaceID, c)
		return true, err
	}
	return false, nil
}

var (
	gmailFrom = regexp.MustCompile(`(?i)receive mail from\s+<?([^\s<>]+@[^\s<>]+)>?`)
	gmailCode = regexp.MustCompile(`\(#(\d{6,12})\)|(?i)confirmation code:\s*(\d{6,12})`)
	gmailURL  = regexp.MustCompile(`https://mail(?:-settings)?\.google\.com/mail/[^\s"'<>]+`)
)

// gmailConfirmation keeps Gmail's forwarding confirmation for the member
// whose address asked for it, who confirms it with one click. Outflare
// never follows the link itself.
func (s *Service) gmailConfirmation(ctx context.Context, tx pgx.Tx, e *Email, msg *mail.Message) (string, string, error) {
	from, code, link, ok := readGmailConfirmation(e, msg)
	if !ok {
		return "rejected", "unreadable_confirmation", nil
	}
	e.ForwardedBy = from
	// The address must be a member's own or one a member linked.
	var userID string
	err := tx.QueryRow(ctx, `
		SELECT f.user_id::text FROM inbound_forwarders f WHERE f.workspace_id = $1 AND f.email = $2
		UNION ALL
		SELECT u.id::text FROM memberships m JOIN users u ON u.id = m.user_id WHERE m.workspace_id = $1 AND lower(u.email) = $2
		LIMIT 1`, e.WorkspaceID, from).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "rejected", "unknown_forwarder", nil
	}
	if err != nil {
		return "", "", err
	}
	e.ForwarderID = userID
	_, err = tx.Exec(ctx, `
		INSERT INTO inbound_forwarders (workspace_id, email, user_id, confirmation_code, confirmation_url, confirmation_at)
		VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), now())
		ON CONFLICT (workspace_id, email) DO UPDATE SET confirmation_code = EXCLUDED.confirmation_code,
			confirmation_url = EXCLUDED.confirmation_url, confirmation_at = now()`, e.WorkspaceID, from, userID, code, link)
	return "processed", "forwarding_confirmation", err
}

// addressConfirmation keeps Gmail's forwarding confirmation on the
// address itself, for purposes anyone may forward to (a support mailbox
// forwarding to its support address): an admin confirms it with one
// click. Outflare never follows the link itself.
func (s *Service) addressConfirmation(ctx context.Context, tx pgx.Tx, e *Email, msg *mail.Message) (string, string, error) {
	from, code, link, ok := readGmailConfirmation(e, msg)
	if !ok {
		return "rejected", "unreadable_confirmation", nil
	}
	e.ForwardedBy = from
	_, err := tx.Exec(ctx, `UPDATE inbound_addresses SET confirmation_from = $2, confirmation_code = NULLIF($3, ''),
		confirmation_url = NULLIF($4, ''), confirmation_at = now() WHERE id = $1`, e.AddressID, from, code, link)
	return "processed", "forwarding_confirmation", err
}

// readGmailConfirmation reads who asked to forward, the code and the link
// from Gmail's confirmation.
func readGmailConfirmation(e *Email, msg *mail.Message) (from, code, link string, ok bool) {
	body, _ := io.ReadAll(io.LimitReader(msg.Body, 256<<10))
	text := e.Subject + "\n" + string(body)
	m := gmailFrom.FindStringSubmatch(text)
	if m == nil {
		return "", "", "", false
	}
	from = strings.ToLower(strings.TrimRight(m[1], ".,"))
	if len(from) > 254 {
		return "", "", "", false
	}
	if c := gmailCode.FindStringSubmatch(text); c != nil {
		code = c[1] + c[2]
	}
	link = gmailURL.FindString(text)
	if len(link) > 2048 {
		link = ""
	}
	return from, code, link, true
}

func decodeHeader(v string) string {
	if d, err := new(mime.WordDecoder).DecodeHeader(v); err == nil {
		return d
	}
	return v
}

// RunPurge deletes raw messages older than RawKeptFor, every period,
// until ctx ends. Their details stay.
func (s *Service) RunPurge(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := s.Purge(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.Log, track.Event{Name: "inbound_email.purge_failed", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Purge deletes the raw content of messages older than RawKeptFor and
// returns how many.
func (s *Service) Purge(ctx context.Context) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `UPDATE inbound_emails SET raw = NULL WHERE raw IS NOT NULL AND received_at < now() - make_interval(secs => $1)`,
		RawKeptFor.Seconds())
	if err != nil {
		return 0, err
	}
	metrics.InboundPurged(tag.RowsAffected())
	return tag.RowsAffected(), nil
}
