// Package inboundemail receives email for workspaces: each workspace gets an
// address per purpose (AI receipts, support) at the server's inbound domain,
// people forward mail to it, and the engine keeps every address, message
// and result. It is off unless the server's operator turns it on through
// an extension (outflare.Host.EnableInboundEmail), which receives the mail
// at its edge and delivers each message, signed, to POST /v1/inbound/email.
// Outflare Cloud does; a self-hosted server shows it as a Cloud feature.
//
// Processing a message checks its DKIM signatures, works out who
// forwarded it, keeps Gmail's forwarding confirmations for a person to
// click, and hands the rest to the purpose's handler (Register). Each
// purpose says who may forward to it: only members (their own address, or
// a personal one they linked), or anyone, for a support address the
// business's own support@ forwards to. Message content is data, never
// instructions.
package inboundemail

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// Purposes an address can have. Add new ones to the migration's check.
const (
	PurposeAIReceipts = "ai_receipts"
	PurposeSupport    = "support"
)

// Purpose is how one purpose's mail is handled, set with Register.
type Purpose struct {
	Handler Handler
	// AnyForwarder accepts mail whoever forwarded it. Otherwise only a
	// member's own address, or a personal one they linked, may.
	AnyForwarder bool
	// Suffix makes the purpose's addresses readable, one per brand: the
	// brand's handle (or the workspace's slug) and Suffix, such as
	// northpeak-support, and a person may rename them. Without it an
	// address is one per workspace, with letters nobody can guess.
	Suffix string
	// On reports whether the purpose is on for a workspace; nil is always.
	On func(ctx context.Context, q Querier, workspaceID string) (bool, error)
}

// JobProcess processes one delivered message.
const JobProcess = "inbound.process"

// MaxBytes is the largest message accepted.
const MaxBytes = 25 << 20

// RawKeptFor is how long a message's raw content is kept.
const RawKeptFor = 30 * 24 * time.Hour

var (
	ErrOff            = errors.New("inbound email isn't available on this server")
	ErrUnknownAddress = errors.New("no live inbound address")
	ErrTooLarge       = errors.New("message too large")
	ErrBadSignature   = errors.New("bad signature")
	ErrDuplicate      = errors.New("message already received")
)

// Email is a processed message, as a purpose's handler sees it.
type Email struct {
	ID          string
	WorkspaceID string
	Purpose     string
	// The address it was delivered to, and that address's brand.
	AddressID  string
	To         string
	BrandID    *string
	ReceivedAt time.Time
	MessageID  string
	From       string // the original sender's address
	Subject    string
	// ForwardedBy is the address that forwarded it and ForwarderID the
	// member it belongs to.
	ForwardedBy string
	ForwarderID string
	// DKIM lists the domains whose signature on the message checked out.
	DKIM []string
	Raw  []byte
}

// SignedBy reports whether a valid DKIM signature is from domain or one
// of its subdomains.
func (e Email) SignedBy(domain string) bool {
	return slices.ContainsFunc(e.DKIM, func(d string) bool { return d == domain || strings.HasSuffix(d, "."+domain) })
}

// Handler processes a message for its purpose, in tx. It returns the
// status (processed or ignored, or rejected with a reason); an error
// retries the job.
type Handler func(ctx context.Context, tx pgx.Tx, e Email) (status, reason string, err error)

// Service is the engine's inbound email.
type Service struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger

	domain   string
	secret   []byte
	purposes map[string]Purpose
	// lookupTXT resolves DKIM keys; nil uses DNS. Tests set it.
	lookupTXT func(string) ([]string, error)
}

// Enable turns inbound email on for the server: addresses are
// local@domain, and deliveries are signed with secret.
func (s *Service) Enable(domain string, secret []byte) {
	s.domain, s.secret = strings.ToLower(strings.TrimSpace(domain)), secret
}

// Enabled reports whether the operator turned inbound email on.
func (s *Service) Enabled() bool { return s != nil && s.domain != "" && len(s.secret) > 0 }

// Domain is the inbound domain, such as in.outflare.ai.
func (s *Service) Domain() string { return s.domain }

// Register sets how a purpose's messages are handled. Only registered
// purposes get addresses.
func (s *Service) Register(name string, p Purpose) {
	if s.purposes == nil {
		s.purposes = map[string]Purpose{}
	}
	s.purposes[name] = p
}

// Purposes lists the registered purposes.
func (s *Service) Purposes() []string {
	out := make([]string, 0, len(s.purposes))
	for k := range s.purposes {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// PurposeOf returns a registered purpose's rules.
func (s *Service) PurposeOf(name string) (Purpose, bool) {
	p, ok := s.purposes[name]
	return p, ok
}

// SetLookupTXT replaces the DNS lookup for DKIM keys (tests).
func (s *Service) SetLookupTXT(fn func(string) ([]string, error)) { s.lookupTXT = fn }

// ---- Addresses ----

// Address is a workspace's inbound address for a purpose.
type Address struct {
	ID        string    `json:"id"`
	Purpose   string    `json:"purpose"`
	BrandID   *string   `json:"brand_id"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
	// Gmail's request to confirm forwarding from a mailbox, for purposes
	// anyone may forward to: who asked, and the code and link to confirm.
	Confirmation *Confirmation `json:"confirmation"`
	LastEmailAt  *time.Time    `json:"last_email_at"`
}

// Confirmation is Gmail's request to confirm forwarding to an address.
type Confirmation struct {
	From string     `json:"from"`
	Code *string    `json:"code"`
	URL  *string    `json:"url"`
	At   *time.Time `json:"at"`
}

// Querier is satisfied by *pgxpool.Pool, *pgx.Conn and pgx.Tx.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

const addressSelect = `SELECT id::text, purpose, brand_id::text, local_part, created_at,
	confirmation_from, confirmation_code, confirmation_url, confirmation_at, last_email_at FROM inbound_addresses`

func (s *Service) scanAddress(row pgx.CollectableRow) (Address, error) {
	var a Address
	var local string
	var from *string
	var c Confirmation
	err := row.Scan(&a.ID, &a.Purpose, &a.BrandID, &local, &a.CreatedAt, &from, &c.Code, &c.URL, &c.At, &a.LastEmailAt)
	a.Email = local + "@" + s.domain
	if from != nil {
		c.From = *from
		a.Confirmation = &c
	}
	return a, err
}

// List returns the workspace's live addresses.
func (s *Service) List(ctx context.Context, q Querier, workspaceID string) ([]Address, error) {
	rows, err := q.Query(ctx, addressSelect+` WHERE workspace_id = $1 AND disabled_at IS NULL ORDER BY purpose, created_at`, workspaceID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, s.scanAddress)
}

var notSlug = regexp.MustCompile(`[^a-z0-9]+`)

// newLocalPart is the workspace's slug, shortened, then random letters,
// such as geonode-7q4m2x9k: easy to recognise, impossible to guess.
func newLocalPart(slug string) string {
	base := strings.Trim(notSlug.ReplaceAllString(strings.ToLower(slug), "-"), "-")
	if len(base) > 24 {
		base = strings.Trim(base[:24], "-")
	}
	if base == "" {
		base = "ws"
	}
	const letters = "abcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return base + "-" + string(b)
}

// Current returns the live address for a purpose and brand, if any.
func (s *Service) Current(ctx context.Context, q Querier, workspaceID, purpose string, brandID *string) (*Address, error) {
	rows, err := q.Query(ctx, addressSelect+` WHERE workspace_id = $1 AND purpose = $2 AND brand_id IS NOT DISTINCT FROM $3 AND disabled_at IS NULL`,
		workspaceID, purpose, brandID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, s.scanAddress)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// ErrNameTaken means another address already has that name.
var ErrNameTaken = errors.New("that address is taken")

// ErrBadName means a name isn't usable as an address.
var ErrBadName = errors.New("an address is 4 to 64 lowercase letters, digits and dashes, starting and ending with a letter or digit")

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,62}[a-z0-9]$`)

// reserved are names a mail server keeps for itself.
var reserved = []string{"abuse", "admin", "administrator", "hostmaster", "mailer-daemon", "noreply", "no-reply", "postmaster", "root", "security", "webmaster"}

// Want says which address Ensure finds or makes.
type Want struct {
	WorkspaceID, Purpose string
	// BrandID is the brand of a readable purpose's address; nil for the
	// workspace's own.
	BrandID *string
	// Name is a readable purpose's local part; empty picks one from the
	// brand's handle or the workspace's slug.
	Name string
	// Replace disables the live address and makes another, so mail to the
	// old one is refused. A Name always does.
	Replace bool
	By      string // the user making it, if any
}

// Ensure returns the live address for w, making one when there is none.
// It reports whether it made one.
func (s *Service) Ensure(ctx context.Context, tx pgx.Tx, w Want) (Address, bool, error) {
	p, ok := s.purposes[w.Purpose]
	if !ok {
		return Address{}, false, fmt.Errorf("unknown purpose %q", w.Purpose)
	}
	if p.Suffix == "" {
		w.BrandID, w.Name = nil, ""
	}
	if w.Name != "" {
		// Naming it replaces it.
		w.Replace = true
	}
	if !w.Replace {
		cur, err := s.Current(ctx, tx, w.WorkspaceID, w.Purpose, w.BrandID)
		if err != nil || cur != nil {
			return deref(cur), false, err
		}
	}
	var local string
	if p.Suffix == "" {
		var slug string
		if err := tx.QueryRow(ctx, `SELECT slug FROM workspaces WHERE id = $1`, w.WorkspaceID).Scan(&slug); err != nil {
			return Address{}, false, err
		}
		local = newLocalPart(slug)
	} else if w.Name != "" {
		local = strings.ToLower(strings.TrimSpace(w.Name))
		if !nameRe.MatchString(local) || strings.Contains(local, "--") || slices.Contains(reserved, local) {
			return Address{}, false, ErrBadName
		}
	} else {
		var err error
		if local, err = s.readableName(ctx, tx, w, p.Suffix); err != nil {
			return Address{}, false, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE inbound_addresses SET disabled_at = now()
		WHERE workspace_id = $1 AND purpose = $2 AND brand_id IS NOT DISTINCT FROM $3 AND disabled_at IS NULL`,
		w.WorkspaceID, w.Purpose, w.BrandID); err != nil {
		return Address{}, false, err
	}
	var by *string
	if w.By != "" {
		by = &w.By
	}
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO inbound_addresses (workspace_id, purpose, brand_id, local_part, created_by) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (local_part) DO NOTHING RETURNING id::text`, w.WorkspaceID, w.Purpose, w.BrandID, local, by).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		// A name this address had before comes back to it.
		err = tx.QueryRow(ctx, `UPDATE inbound_addresses SET disabled_at = NULL
			WHERE local_part = $1 AND workspace_id = $2 AND purpose = $3 AND brand_id IS NOT DISTINCT FROM $4 AND disabled_at IS NOT NULL
			RETURNING id::text`, local, w.WorkspaceID, w.Purpose, w.BrandID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return Address{}, false, ErrNameTaken
		}
	}
	if err != nil {
		return Address{}, false, err
	}
	rows, err := tx.Query(ctx, addressSelect+` WHERE id = $1`, id)
	if err != nil {
		return Address{}, false, err
	}
	a, err := pgx.CollectExactlyOneRow(rows, s.scanAddress)
	return a, true, err
}

// readableName is the brand's handle (or the workspace's slug) and the
// purpose's suffix, with a number after it when that is taken.
func (s *Service) readableName(ctx context.Context, tx pgx.Tx, w Want, suffix string) (string, error) {
	var base string
	err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT handle FROM brands WHERE id = $2 AND workspace_id = $1), (SELECT slug FROM workspaces WHERE id = $1))`,
		w.WorkspaceID, w.BrandID).Scan(&base)
	if err != nil {
		return "", err
	}
	base = strings.Trim(notSlug.ReplaceAllString(strings.ToLower(base), "-"), "-")
	if max := 63 - len(suffix) - 4; len(base) > max {
		base = strings.Trim(base[:max], "-")
	}
	if base == "" {
		base = "team"
	}
	name := base + "-" + suffix
	for n := 2; ; n++ {
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM inbound_addresses WHERE local_part = $1)`, name).Scan(&taken); err != nil {
			return "", err
		}
		if !taken {
			return name, nil
		}
		name = fmt.Sprintf("%s-%s-%d", base, suffix, n)
	}
}

// ---- Delivery ----

// Sign is the signature an extension sends with a delivery: hex
// HMAC-SHA256 of "<unix seconds>.<raw message>".
func Sign(secret []byte, ts int64, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(strconv.FormatInt(ts, 10) + "."))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// Verify checks a delivery's signature, made within five minutes of now.
func (s *Service) Verify(ts string, sig string, body []byte, now time.Time) error {
	t, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || now.Sub(time.Unix(t, 0)).Abs() > 5*time.Minute {
		return ErrBadSignature
	}
	if !hmac.Equal([]byte(strings.TrimPrefix(sig, "sha256=")), []byte(Sign(s.secret, t, body))) {
		return ErrBadSignature
	}
	return nil
}

// Deliver stores a message sent to the address to and queues it to be
// processed. It returns the message's id, and the purpose of the address.
func (s *Service) Deliver(ctx context.Context, to string, raw []byte) (id, purpose string, err error) {
	if len(raw) > MaxBytes {
		return "", "", ErrTooLarge
	}
	local, domain, ok := strings.Cut(strings.ToLower(strings.TrimSpace(to)), "@")
	if !ok || domain != s.domain {
		return "", "", ErrUnknownAddress
	}
	msgID := ""
	if m, err := mail.ReadMessage(bytes.NewReader(raw)); err == nil {
		msgID = strings.Trim(strings.TrimSpace(m.Header.Get("Message-Id")), "<>")
		if len(msgID) > 998 {
			msgID = msgID[:998]
		}
	}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var addressID, workspaceID string
		err := tx.QueryRow(ctx, `SELECT id::text, workspace_id::text, purpose FROM inbound_addresses WHERE local_part = $1 AND disabled_at IS NULL`, local).
			Scan(&addressID, &workspaceID, &purpose)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUnknownAddress
		}
		if err != nil {
			return err
		}
		var mid *string
		if msgID != "" {
			mid = &msgID
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO inbound_emails (workspace_id, address_id, purpose, message_id, raw, size_bytes)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (address_id, message_id) WHERE message_id IS NOT NULL DO NOTHING
			RETURNING id::text`, workspaceID, addressID, purpose, mid, raw, len(raw)).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDuplicate
		}
		if err != nil {
			return err
		}
		_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobProcess, Payload: map[string]string{"id": id}, WorkspaceID: &workspaceID, IdempotencyKey: id})
		return err
	})
	return id, purpose, err
}

// ---- Forwarders ----

// Forwarder is an address allowed to forward to the workspace besides
// its members' own: a member's personal address.
type Forwarder struct {
	Email            string     `json:"email"`
	UserID           string     `json:"user_id"`
	ConfirmationCode *string    `json:"confirmation_code"`
	ConfirmationURL  *string    `json:"confirmation_url"`
	ConfirmationAt   *time.Time `json:"confirmation_at"`
	LastEmailAt      *time.Time `json:"last_email_at"`
	CreatedAt        time.Time  `json:"created_at"`
}

// LinkForwarder lets userID forward from email, in tx. Linking it again
// changes nothing; an address another member linked is refused.
func LinkForwarder(ctx context.Context, tx pgx.Tx, workspaceID, userID, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if a, err := mail.ParseAddress(email); err != nil || a.Address != email || len(email) > 254 {
		return errors.New("enter an email address")
	}
	var owner string
	err := tx.QueryRow(ctx, `INSERT INTO inbound_forwarders (workspace_id, email, user_id) VALUES ($1, $2, $3)
		ON CONFLICT (workspace_id, email) DO UPDATE SET email = EXCLUDED.email RETURNING user_id::text`, workspaceID, email, userID).Scan(&owner)
	if err != nil {
		return err
	}
	if owner != userID {
		return errors.New("someone else in the workspace already forwards from that address")
	}
	return nil
}

// Forwarders lists the addresses linked in the workspace, or one
// member's when userID is set.
func Forwarders(ctx context.Context, q Querier, workspaceID, userID string) ([]Forwarder, error) {
	rows, err := q.Query(ctx, `SELECT email, user_id::text, confirmation_code, confirmation_url, confirmation_at, last_email_at, created_at
		FROM inbound_forwarders WHERE workspace_id = $1 AND ($2 = '' OR user_id::text = $2) ORDER BY created_at`, workspaceID, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Forwarder, error) {
		var f Forwarder
		return f, row.Scan(&f.Email, &f.UserID, &f.ConfirmationCode, &f.ConfirmationURL, &f.ConfirmationAt, &f.LastEmailAt, &f.CreatedAt)
	})
}

// UnlinkForwarder removes a linked address of userID's (any member's when
// userID is empty).
func UnlinkForwarder(ctx context.Context, tx pgx.Tx, workspaceID, userID, email string) (bool, error) {
	tag, err := tx.Exec(ctx, `DELETE FROM inbound_forwarders WHERE workspace_id = $1 AND email = $2 AND ($3 = '' OR user_id::text = $3)`,
		workspaceID, strings.ToLower(strings.TrimSpace(email)), userID)
	return tag.RowsAffected() > 0, err
}

// recordProcessed writes the inbound_email.processed audit event.
func recordProcessed(ctx context.Context, tx pgx.Tx, e Email, status, reason string) error {
	props := map[string]any{"purpose": e.Purpose, "status": status}
	if reason != "" {
		props["reason"] = reason
	}
	return audit.Record(ctx, tx, audit.Event{WorkspaceID: e.WorkspaceID, ActorType: "system", Action: "inbound_email.processed",
		TargetType: "inbound_email", TargetID: e.ID, Data: props, Props: props})
}

func observe(purpose, status string) { metrics.InboundProcessed(purpose, status) }

func deref(a *Address) Address {
	if a == nil {
		return Address{}
	}
	return *a
}
