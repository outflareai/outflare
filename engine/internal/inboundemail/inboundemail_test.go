package inboundemail

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-msgauth/dkim"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/db"
)

const domain = "in.example.test"

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := db.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := db.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

// setup makes a workspace with one member, ana@acme.example, and a
// service with a Google DKIM key it trusts.
func setup(t *testing.T) (*Service, string, string, func(string, []byte) []byte) {
	t.Helper()
	pool := testPool(t)
	ctx := context.Background()
	var ws, user string
	tag := strings.ReplaceAll(fmt.Sprint(time.Now().UnixNano()), "-", "")
	if err := pool.QueryRow(ctx, `INSERT INTO workspaces (name, slug) VALUES ('Acme', 'acme-' || $1) RETURNING id::text`, tag).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		WITH a AS (INSERT INTO actors (type, display_name) VALUES ('human', 'Ana') RETURNING id)
		INSERT INTO users (actor_id, email, name, password_hash) SELECT id, $1, 'Ana', '' FROM a RETURNING id::text`,
		"ana-"+tag+"@acme.example").Scan(&user); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO memberships (workspace_id, user_id, role) VALUES ($1, $2, 'operator')`, ws, user); err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	s := &Service{Pool: pool}
	s.Enable(domain, []byte("test-secret-not-real"))
	s.SetLookupTXT(func(name string) ([]string, error) {
		if name == "t._domainkey.google.com" {
			return []string{"v=DKIM1; k=ed25519; p=" + base64.StdEncoding.EncodeToString(pub)}, nil
		}
		return nil, errors.New("no such record")
	})
	sign := func(d string, raw []byte) []byte {
		var out bytes.Buffer
		if err := dkim.Sign(&out, bytes.NewReader(raw), &dkim.SignOptions{Domain: d, Selector: "t", Signer: priv}); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	return s, ws, user, sign
}

func address(t *testing.T, s *Service, ws string, replace bool) Address {
	t.Helper()
	var a Address
	if err := pgx.BeginFunc(context.Background(), s.Pool, func(tx pgx.Tx) error {
		var err error
		a, _, err = s.Ensure(context.Background(), tx, Want{WorkspaceID: ws, Purpose: PurposeAIReceipts, Replace: replace})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return a
}

func message(id, from, fwd, subject, body string) []byte {
	h := fmt.Sprintf("From: %s\r\nTo: receipts@example.test\r\nSubject: %s\r\nMessage-ID: <%s>\r\nDate: Thu, 29 Oct 2026 10:00:00 +0000\r\n", from, subject, id)
	if fwd != "" {
		h += "X-Forwarded-For: " + fwd + " receipts@" + domain + "\r\n"
	}
	return []byte(h + "\r\n" + body + "\r\n")
}

func statusOf(t *testing.T, s *Service, id string) (string, string) {
	t.Helper()
	var status string
	var reason *string
	if err := s.Pool.QueryRow(context.Background(), `SELECT status, reason FROM inbound_emails WHERE id::text = $1`, id).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if reason == nil {
		return status, ""
	}
	return status, *reason
}

func TestSignature(t *testing.T) {
	s := &Service{}
	s.Enable(domain, []byte("k"))
	body, now := []byte("raw"), time.Now()
	sig := Sign([]byte("k"), now.Unix(), body)
	if err := s.Verify(fmt.Sprint(now.Unix()), "sha256="+sig, body, now); err != nil {
		t.Fatal(err)
	}
	if s.Verify(fmt.Sprint(now.Unix()), sig, []byte("other"), now) == nil {
		t.Error("changed body accepted")
	}
	if s.Verify(fmt.Sprint(now.Add(-10*time.Minute).Unix()), Sign([]byte("k"), now.Add(-10*time.Minute).Unix(), body), body, now) == nil {
		t.Error("old signature accepted")
	}
	if (&Service{}).Enabled() || !s.Enabled() {
		t.Error("Enabled")
	}
}

func TestLocalPart(t *testing.T) {
	a, b := newLocalPart("Acme Outbound!"), newLocalPart("acme-outbound")
	if !strings.HasPrefix(a, "acme-outbound-") || a == b || len(a) != len("acme-outbound-")+10 {
		t.Errorf("local parts %q %q", a, b)
	}
	if !strings.HasPrefix(newLocalPart(""), "ws-") {
		t.Error("empty slug")
	}
}

func TestDeliverAndProcess(t *testing.T) {
	s, ws, user, sign := setup(t)
	ctx := context.Background()
	var anaEmail string
	if err := s.Pool.QueryRow(ctx, `SELECT email FROM users WHERE id::text = $1`, user).Scan(&anaEmail); err != nil {
		t.Fatal(err)
	}
	s.Register(PurposeAIReceipts, Purpose{})
	a := address(t, s, ws, false)
	if again := address(t, s, ws, false); again.ID != a.ID || !strings.HasSuffix(a.Email, "@"+domain) {
		t.Fatalf("address not kept: %v %v", a, again)
	}

	var got []Email
	s.Register(PurposeAIReceipts, Purpose{Handler: func(ctx context.Context, tx pgx.Tx, e Email) (string, string, error) {
		got = append(got, e)
		return "processed", "", nil
	}})

	// Forwarded by a member's own address: handed to the handler.
	id, purpose, err := s.Deliver(ctx, strings.ToUpper(a.Email), message("m1@x", "Anthropic <receipts@anthropic.example>", anaEmail, "Your receipt", "Total $200.00"))
	if err != nil || purpose != PurposeAIReceipts {
		t.Fatal(err, purpose)
	}
	if err := s.process(ctx, id); err != nil {
		t.Fatal(err)
	}
	if st, _ := statusOf(t, s, id); st != "processed" || len(got) != 1 || got[0].ForwarderID != user || got[0].From != "receipts@anthropic.example" || got[0].Subject != "Your receipt" {
		t.Fatalf("processed: %s %+v", st, got)
	}

	// The same message again is a duplicate; unknown addresses are refused.
	if _, _, err := s.Deliver(ctx, a.Email, message("m1@x", "x@example.test", anaEmail, "s", "b")); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate: %v", err)
	}
	for _, to := range []string{"nobody-1234567890@" + domain, strings.Replace(a.Email, domain, "elsewhere.test", 1), "no-at-sign"} {
		if _, _, err := s.Deliver(ctx, to, message("m2@x", "x@example.test", "", "s", "b")); !errors.Is(err, ErrUnknownAddress) {
			t.Errorf("%s: %v", to, err)
		}
	}

	// Forwarded from an address nobody linked: rejected.
	id, _, _ = s.Deliver(ctx, a.Email, message("m3@x", "x@example.test", "stranger@example.test", "s", "b"))
	if err := s.process(ctx, id); err != nil {
		t.Fatal(err)
	}
	if st, reason := statusOf(t, s, id); st != "rejected" || reason != "unknown_forwarder" || len(got) != 1 {
		t.Fatalf("stranger: %s %s", st, reason)
	}

	// A personal address Ana linked: Gmail's confirmation, signed by
	// google.com, is kept for her; then her forwards are accepted.
	if err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error { return LinkForwarder(ctx, tx, ws, user, " Ana.Personal@Gmail.example ") }); err != nil {
		t.Fatal(err)
	}
	confirm := sign("google.com", message("c1@google", "Gmail Team <forwarding-noreply@google.com>", "",
		"(#123456789) Gmail Forwarding Confirmation - Receive Mail from ana.personal@gmail.example",
		"ana.personal@gmail.example has requested to automatically forward mail to your email address.\r\nConfirmation code: 123456789\r\nTo allow, click the link below:\r\nhttps://mail-settings.google.com/mail/vf-%5BANGjdJ-example%5D-abc"))
	id, _, _ = s.Deliver(ctx, a.Email, confirm)
	if err := s.process(ctx, id); err != nil {
		t.Fatal(err)
	}
	fs, err := Forwarders(ctx, s.Pool, ws, user)
	if err != nil || len(fs) != 1 || fs[0].ConfirmationCode == nil || *fs[0].ConfirmationCode != "123456789" ||
		fs[0].ConfirmationURL == nil || !strings.HasPrefix(*fs[0].ConfirmationURL, "https://mail-settings.google.com/mail/vf-") {
		t.Fatalf("confirmation: %+v %v", fs, err)
	}
	if st, reason := statusOf(t, s, id); st != "processed" || reason != "forwarding_confirmation" {
		t.Fatalf("confirmation status: %s %s", st, reason)
	}
	// An unsigned look-alike is not a confirmation.
	fake := message("c2@x", "forwarding-noreply@google.com", "", "(#1) Gmail Forwarding Confirmation - Receive Mail from evil@example.test", "https://mail-settings.google.com/mail/vf-evil")
	id, _, _ = s.Deliver(ctx, a.Email, fake)
	_ = s.process(ctx, id)
	if st, reason := statusOf(t, s, id); st != "rejected" || reason != "unknown_forwarder" {
		t.Fatalf("unsigned confirmation: %s %s", st, reason)
	}

	id, _, _ = s.Deliver(ctx, a.Email, message("m4@x", "receipts@openai.example", "ana.personal@gmail.example", "Receipt", "b"))
	if err := s.process(ctx, id); err != nil {
		t.Fatal(err)
	}
	if st, _ := statusOf(t, s, id); st != "processed" || len(got) != 2 || got[1].ForwardedBy != "ana.personal@gmail.example" {
		t.Fatalf("personal forward: %s %+v", st, got)
	}

	// Another member can't take Ana's address.
	if err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		return LinkForwarder(ctx, tx, ws, "00000000-0000-0000-0000-000000000000", "ana.personal@gmail.example")
	}); err == nil {
		t.Error("someone else linked Ana's address")
	}

	// Replacing the address refuses mail to the old one.
	b := address(t, s, ws, true)
	if b.Email == a.Email {
		t.Fatal("not replaced")
	}
	if _, _, err := s.Deliver(ctx, a.Email, message("m5@x", "x@example.test", "", "s", "b")); !errors.Is(err, ErrUnknownAddress) {
		t.Errorf("old address: %v", err)
	}

	// Raw messages go after 30 days; their details stay.
	if _, err := s.Pool.Exec(ctx, `UPDATE inbound_emails SET received_at = now() - interval '31 days' WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Purge(ctx); err != nil || n < 5 {
		t.Fatalf("purged %d %v", n, err)
	}
	var left int
	_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM inbound_emails WHERE workspace_id = $1 AND raw IS NOT NULL`, ws).Scan(&left)
	if left != 0 {
		t.Errorf("%d raw messages left", left)
	}
}

// Support addresses: readable, one per brand, renamed by a person, and
// any address may forward to them.
func TestSupportAddresses(t *testing.T) {
	s, ws, _, sign := setup(t)
	ctx := context.Background()
	var got []Email
	s.Register(PurposeSupport, Purpose{AnyForwarder: true, Suffix: "support", Handler: func(ctx context.Context, tx pgx.Tx, e Email) (string, string, error) {
		got = append(got, e)
		return "processed", "", nil
	}})
	var brand string
	tag := strings.ReplaceAll(fmt.Sprint(time.Now().UnixNano()), "-", "")[10:]
	if err := s.Pool.QueryRow(ctx, `INSERT INTO brands (workspace_id, name, handle) VALUES ($1, 'Repocket', 'repocket' || $2) RETURNING id::text`, ws, tag).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	ensure := func(w Want) (Address, error) {
		w.WorkspaceID, w.Purpose = ws, PurposeSupport
		var a Address
		err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			var err error
			a, _, err = s.Ensure(ctx, tx, w)
			return err
		})
		return a, err
	}
	shared, err := ensure(Want{})
	if err != nil || !strings.HasPrefix(shared.Email, "acme-") || !strings.HasSuffix(shared.Email, "-support@"+domain) || shared.BrandID != nil {
		t.Fatalf("workspace address: %+v %v", shared, err)
	}
	branded, err := ensure(Want{BrandID: &brand})
	if err != nil || branded.Email != "repocket"+tag+"-support@"+domain || branded.BrandID == nil {
		t.Fatalf("brand address: %+v %v", branded, err)
	}
	if again, _ := ensure(Want{BrandID: &brand}); again.ID != branded.ID {
		t.Fatal("brand address not kept")
	}

	// Names: checked, unique, and an old one comes back to its address.
	for _, bad := range []string{"ab", "-help", "Help Desk", "postmaster", "help--desk"} {
		if _, err := ensure(Want{BrandID: &brand, Name: bad}); !errors.Is(err, ErrBadName) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := ensure(Want{Name: strings.Split(branded.Email, "@")[0], Replace: true}); !errors.Is(err, ErrNameTaken) {
		t.Errorf("taken name: %v", err)
	}
	renamed, err := ensure(Want{BrandID: &brand, Name: "help-" + tag, Replace: true})
	if err != nil || renamed.Email != "help-"+tag+"@"+domain {
		t.Fatalf("rename: %+v %v", renamed, err)
	}
	if _, _, err := s.Deliver(ctx, branded.Email, message("s0@x", "x@example.test", "", "s", "b")); !errors.Is(err, ErrUnknownAddress) {
		t.Errorf("old name still takes mail: %v", err)
	}
	back, err := ensure(Want{BrandID: &brand, Name: strings.Split(branded.Email, "@")[0], Replace: true})
	if err != nil || back.ID != branded.ID {
		t.Fatalf("renamed back: %+v %v", back, err)
	}

	// A customer's email, forwarded by the business's own support mailbox
	// (nobody in the workspace), reaches the handler with its brand.
	id, _, err := s.Deliver(ctx, back.Email, message("s1@x", "Li Wei <li@globex.example>", "help@repocket.example", "Export?", "How do I export?"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.process(ctx, id); err != nil {
		t.Fatal(err)
	}
	if st, _ := statusOf(t, s, id); st != "processed" || len(got) != 1 || got[0].From != "li@globex.example" || got[0].BrandID == nil || *got[0].BrandID != brand ||
		got[0].To != back.Email || got[0].ForwardedBy != "help@repocket.example" {
		t.Fatalf("support mail: %s %+v", st, got)
	}

	// Gmail's confirmation for forwarding the support mailbox is kept on
	// the address, for an admin to click.
	confirm := sign("google.com", message("c9@google", "Gmail Team <forwarding-noreply@google.com>", "",
		"(#555666777) Gmail Forwarding Confirmation - Receive Mail from help@repocket.example",
		"help@repocket.example has requested to automatically forward mail to your email address.\r\nConfirmation code: 555666777\r\nhttps://mail-settings.google.com/mail/vf-%5Bexample%5D-x"))
	id, _, _ = s.Deliver(ctx, back.Email, confirm)
	if err := s.process(ctx, id); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx, s.Pool, ws)
	if err != nil {
		t.Fatal(err)
	}
	var c *Confirmation
	for _, a := range list {
		if a.ID == back.ID {
			c = a.Confirmation
		}
	}
	if c == nil || c.From != "help@repocket.example" || c.Code == nil || *c.Code != "555666777" || len(got) != 1 {
		t.Fatalf("confirmation: %+v %d", c, len(got))
	}
}
