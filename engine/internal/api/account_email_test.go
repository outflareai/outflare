package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/sysmail"
	"github.com/outflareai/outflare/engine/internal/track"
)

// outbox is a sysmail.Sender that keeps what it is given.
type outbox struct {
	mu   sync.Mutex
	sent []sysmail.Message
}

func (*outbox) Name() string { return "test" }

func (o *outbox) Send(_ context.Context, m sysmail.Message) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sent = append(o.sent, m)
	return nil
}

func (o *outbox) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.sent)
}

var linkRe = regexp.MustCompile(`http://localhost:8080/(verify-email|reset-password)#(of[vr]_[A-Za-z0-9_-]+)`)

// last returns the last email's recipient and the secret in its link.
func (o *outbox) last(t *testing.T, page string) (to, secret string) {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.sent) == 0 {
		t.Fatal("no email sent")
	}
	m := o.sent[len(o.sent)-1]
	g := linkRe.FindStringSubmatch(m.Text)
	if g == nil || g[1] != page {
		t.Fatalf("no %s link in %q", page, m.Text)
	}
	return m.To, g[2]
}

func TestEmailConfirmation(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	if m := owner.must(200, "GET", "/v1/meta", nil); m["account_email"] != "test" {
		t.Fatalf("meta: %v", m["account_email"])
	}
	to, first := e.outbox.last(t, "verify-email")
	if to != "jack@example.com" {
		t.Fatalf("sent to %s", to)
	}
	me := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)
	if me["email_verified"] != false || me["has_password"] != true {
		t.Fatalf("me: %v", me)
	}

	// A link just went out, so asking again waits for the cooldown.
	if me["email_resend_after"].(float64) < 50 {
		t.Fatalf("resend after: %v", me["email_resend_after"])
	}
	n0 := e.outbox.count()
	st, body := owner.do("POST", "/v1/me/email-verification", nil)
	if st != 429 || !strings.Contains(errText(body), "seconds") || e.outbox.count() != n0 {
		t.Fatalf("resend during cooldown: %d %s", st, body)
	}
	ageLinks(t, e, "2 minutes")

	// Asking again replaces the first link.
	if out := owner.must(202, "POST", "/v1/me/email-verification", nil); out["resend_after"] != float64(60) {
		t.Fatalf("resend: %v", out)
	}
	_, second := e.outbox.last(t, "verify-email")
	anon := &client{t: t, h: e.h}
	anon.must(404, "POST", "/v1/auth/verify-email", map[string]string{"token": first})
	if out := anon.must(200, "POST", "/v1/auth/verify-email", map[string]string{"token": second}); out["email"] != "jack@example.com" {
		t.Fatalf("verify: %v", out)
	}
	anon.must(404, "POST", "/v1/auth/verify-email", map[string]string{"token": second})
	if me := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any); me["email_verified"] != true {
		t.Fatalf("me after confirming: %v", me)
	}
	n := e.outbox.count()
	owner.must(200, "POST", "/v1/me/email-verification", nil) // already confirmed: nothing sent
	if e.outbox.count() != n {
		t.Fatal("sent a confirmation to a confirmed address")
	}

	// Someone joining by invite with a password is asked to confirm too.
	inv := owner.must(201, "POST", "/v1/workspaces/"+ws+"/invites", map[string]string{"email": "viv@example.com", "role": "viewer"})
	(&client{t: t, h: e.h}).must(200, "POST", "/v1/invites/accept", map[string]string{"token": inv["token"].(string), "name": "Viv", "password": "viv long password"})
	if to, _ := e.outbox.last(t, "verify-email"); to != "viv@example.com" {
		t.Fatalf("invitee confirmation sent to %s", to)
	}

	// Hosts such as Cloud send it through Accounts, and never to a
	// confirmed address.
	accounts := &api.Accounts{Pool: e.pool, PublicURL: "http://localhost:8080/", Mailer: e.outbox, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	var vivID, jackID string
	e.pool.QueryRow(context.Background(), `SELECT id FROM users WHERE email = 'viv@example.com'`).Scan(&vivID)   //nolint:errcheck
	e.pool.QueryRow(context.Background(), `SELECT id FROM users WHERE email = 'jack@example.com'`).Scan(&jackID) //nolint:errcheck
	ageLinks(t, e, "2 minutes")
	if err := accounts.SendVerification(context.Background(), vivID); err != nil {
		t.Fatal(err)
	}
	if to, _ := e.outbox.last(t, "verify-email"); to != "viv@example.com" {
		t.Fatalf("Accounts confirmation sent to %s", to)
	}
	n = e.outbox.count()
	if err := accounts.SendVerification(context.Background(), jackID); err != nil || e.outbox.count() != n {
		t.Fatalf("sent to a confirmed address: %v", err)
	}
}

// ageLinks makes every account email link look older, as if time passed.
func ageLinks(t *testing.T, e *env, by string) {
	t.Helper()
	if _, err := e.pool.Exec(context.Background(), `UPDATE account_tokens SET created_at = created_at - $1::interval`, by); err != nil {
		t.Fatal(err)
	}
}

func errText(body map[string]any) string {
	m, _ := body["error"].(map[string]any)
	s, _ := m["message"].(string)
	return s
}

// A reported bug: "Send the link again" sent an email every click.
func TestConfirmationEmailLimits(t *testing.T) {
	e := freshEnv(t)
	owner, _ := setupOwner(t, e.h)
	events := track.Capture(t)
	sent := e.outbox.count() // the link from setup
	// Clicking fast sends nothing more, however many times.
	for range 5 {
		owner.must(429, "POST", "/v1/me/email-verification", nil)
	}
	if e.outbox.count() != sent {
		t.Fatal("sent during the cooldown")
	}
	if ev := named(events(), "account_email.throttled"); len(ev) != 5 || ev[0].ErrorCode != "rate_limited" || ev[0].Props["limit"] != "cooldown" {
		t.Fatalf("throttled events: %+v", ev)
	}
	// Once a minute is allowed, up to five an hour.
	for range 4 {
		ageLinks(t, e, "61 seconds")
		owner.must(202, "POST", "/v1/me/email-verification", nil)
	}
	ageLinks(t, e, "61 seconds")
	st, body := owner.do("POST", "/v1/me/email-verification", nil)
	if st != 429 || !strings.Contains(errText(body), "minutes") || e.outbox.count() != sent+4 {
		t.Fatalf("sixth in an hour: %d %s", st, body)
	}
	// An hour on, more are allowed, up to ten a day.
	ageLinks(t, e, "1 hour")
	for range 5 {
		owner.must(202, "POST", "/v1/me/email-verification", nil)
		ageLinks(t, e, "61 seconds")
	}
	ageLinks(t, e, "1 hour")
	if st, body := owner.do("POST", "/v1/me/email-verification", nil); st != 429 || !strings.Contains(errText(body), "tomorrow") {
		t.Fatalf("eleventh in a day: %d %s", st, body)
	}
}

func TestPasswordReset(t *testing.T) {
	e := freshEnv(t)
	owner, _ := setupOwner(t, e.h)
	anon := &client{t: t, h: e.h}
	n := e.outbox.count()

	// Same answer for an unknown address, and nothing is sent.
	anon.must(400, "POST", "/v1/auth/password-reset", map[string]string{"email": "nope"})
	anon.must(202, "POST", "/v1/auth/password-reset", map[string]string{"email": "nobody@example.com"})
	if e.outbox.count() != n {
		t.Fatal("sent a reset to an unknown address")
	}
	anon.must(202, "POST", "/v1/auth/password-reset", map[string]string{"email": "JACK@example.com"})
	to, secret := e.outbox.last(t, "reset-password")
	if to != "jack@example.com" {
		t.Fatalf("reset sent to %s", to)
	}

	browser := &client{t: t, h: e.h}
	browser.must(400, "POST", "/v1/auth/password-reset/confirm", map[string]string{"token": secret, "password": "short"})
	browser.must(404, "POST", "/v1/auth/password-reset/confirm", map[string]string{"token": "ofr_wrong", "password": "a brand new password"})
	browser.must(200, "POST", "/v1/auth/password-reset/confirm", map[string]string{"token": secret, "password": "a brand new password"})
	browser.must(200, "GET", "/v1/me", nil)
	owner.must(401, "GET", "/v1/me", nil) // every other session is signed out
	browser.must(404, "POST", "/v1/auth/password-reset/confirm", map[string]string{"token": secret, "password": "yet another password"})

	anon.must(401, "POST", "/v1/auth/login", map[string]string{"email": "jack@example.com", "password": "long enough password"})
	anon.must(200, "POST", "/v1/auth/login", map[string]string{"email": "jack@example.com", "password": "a brand new password"})
	if me := anon.must(200, "GET", "/v1/me", nil)["user"].(map[string]any); me["email_verified"] != true {
		t.Fatalf("a reset should confirm the address: %v", me)
	}

	// Expired links don't work.
	anon.must(202, "POST", "/v1/auth/password-reset", map[string]string{"email": "jack@example.com"})
	_, secret = e.outbox.last(t, "reset-password")
	if _, err := e.pool.Exec(context.Background(), `UPDATE account_tokens SET expires_at = now() - interval '1 minute' WHERE used_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	anon.must(404, "POST", "/v1/auth/password-reset/confirm", map[string]string{"token": secret, "password": "a brand new password 2"})

	// Asking again within a minute answers the same but sends nothing.
	ageLinks(t, e, "1 hour")
	anon.must(202, "POST", "/v1/auth/password-reset", map[string]string{"email": "jack@example.com"})
	n = e.outbox.count()
	anon.must(202, "POST", "/v1/auth/password-reset", map[string]string{"email": "jack@example.com"})
	if e.outbox.count() != n {
		t.Fatal("sent a second reset link within a minute")
	}

	// Too many requests for one address are refused.
	limited := false
	for i := 0; i < 12 && !limited; i++ {
		st, _ := anon.do("POST", "/v1/auth/password-reset", map[string]string{"email": "jack@example.com"})
		limited = st == 429
	}
	if !limited {
		t.Fatal("reset requests were never rate limited")
	}
}

func TestClientIPBehindAProxy(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:5000"
	r.Header.Set("Fly-Client-IP", "203.0.113.9")
	api.NewServer(nil, log, api.Options{})
	if ip := api.ClientIP(r); ip != "10.0.0.1" {
		t.Fatalf("without a proxy header: %s", ip)
	}
	api.NewServer(nil, log, api.Options{ClientIPHeader: "Fly-Client-IP"})
	defer api.NewServer(nil, log, api.Options{})
	if ip := api.ClientIP(r); ip != "203.0.113.9" {
		t.Fatalf("behind the proxy: %s", ip)
	}
}

// Passwords can't be guessed faster through an invite than through
// sign-in, nor one password tried across many addresses.
func TestPasswordGuessingLimits(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	events := track.Capture(t)
	// Anyone can invite any address to their own workspace, which used to
	// make the invite's password check an unlimited way to guess.
	first := owner.must(201, "POST", "/v1/workspaces/"+ws+"/invites", map[string]string{"email": "sam@example.com", "role": "viewer"})
	sam := &client{t: t, h: e.h}
	sam.must(200, "POST", "/v1/invites/accept", map[string]string{"token": first["token"].(string), "name": "Sam", "password": "sams long password"})
	samWS := sam.must(201, "POST", "/v1/workspaces", map[string]string{"name": "Sam's"})["id"].(string)
	inv := sam.must(201, "POST", "/v1/workspaces/"+samWS+"/invites", map[string]string{"email": "jack@example.com", "role": "viewer"})
	anon := &client{t: t, h: e.h}
	for range 10 {
		anon.must(401, "POST", "/v1/invites/accept", map[string]string{"token": inv["token"].(string), "password": "a wrong guess"})
	}
	anon.must(429, "POST", "/v1/invites/accept", map[string]string{"token": inv["token"].(string), "password": "long enough password"})
	anon.must(429, "POST", "/v1/auth/login", map[string]string{"email": "jack@example.com", "password": "long enough password"})
	// The count lives in the database, so a restart or another server
	// doesn't forget it.
	restarted := &client{t: t, h: handler(t, api.NewServer(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)), api.Options{}))}
	restarted.must(429, "POST", "/v1/auth/login", map[string]string{"email": "jack@example.com", "password": "long enough password"})
	if ev := named(events(), "user.sign_in_failed"); len(ev) != 13 || ev[0].Props["via"] != "invite" || ev[12].ErrorCode != "rate_limited" {
		t.Fatalf("sign-in failures: %d", len(ev))
	}

	// One network trying many addresses is stopped too.
	for i := range 90 {
		anon.must(401, "POST", "/v1/auth/login", map[string]string{"email": "user" + strconv.Itoa(i) + "@example.com", "password": "guess"})
	}
	if st, _ := anon.do("POST", "/v1/auth/login", map[string]string{"email": "fresh@example.com", "password": "guess"}); st != 429 {
		t.Fatalf("network limit: %d", st)
	}
}
