package api_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
)

func TestAccountsCreateAndRecognise(t *testing.T) {
	e := freshEnv(t)
	acc := &api.Accounts{Pool: e.pool, PublicURL: "https://app.example.com"}
	ctx := context.Background()

	rec := httptest.NewRecorder()
	in := api.NewAccount{Email: "ada@example.com", Name: "Ada", Password: "correct horse battery", WorkspaceName: "Acme"}
	userID, wsID, err := acc.CreateAccount(ctx, nil, rec, httptest.NewRequest("POST", "/", nil), in)
	if err != nil || userID == "" || wsID == "" {
		t.Fatalf("create: %q %q %v", userID, wsID, err)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly {
		t.Fatalf("session cookie: %+v", cookies)
	}

	// The new session works against the engine's own API, as an owner.
	c := &client{t: t, h: e.h, cookie: cookies[0]}
	if code, out := c.do("GET", "/v1/me", nil); code != 200 || out["workspaces"].([]any)[0].(map[string]any)["role"] != "owner" {
		t.Fatalf("me: %d %v", code, out)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookies[0])
	if id, email, err := acc.SessionUser(req); err != nil || id != userID || email != "ada@example.com" {
		t.Fatalf("session user: %q %q %v", id, email, err)
	}
	if id, _, err := acc.SessionUser(httptest.NewRequest("GET", "/", nil)); err != nil || id != "" {
		t.Fatalf("anonymous: %q %v", id, err)
	}

	in.Email = "ADA@example.com"
	if _, _, err := acc.CreateAccount(ctx, nil, httptest.NewRecorder(), req, in); !errors.Is(err, api.ErrEmailTaken) {
		t.Fatalf("duplicate email: %v", err)
	}
	in.Email, in.Password = "bob@example.com", "short"
	var inputErr *api.InputError
	if _, _, err := acc.CreateAccount(ctx, nil, httptest.NewRecorder(), req, in); !errors.As(err, &inputErr) {
		t.Fatalf("weak password: %v", err)
	}
}

// Open sign-up answers the same for a new and an existing address, makes
// the account only from the emailed link, and uses the form's password
// only in the browser that sent the form.
func TestSignupConfirmedFirst(t *testing.T) {
	e := freshEnv(t)
	setupOwner(t, e.h) // jack@example.com has an account
	acc := &api.Accounts{Pool: e.pool, PublicURL: "http://localhost:8080", Mailer: e.outbox, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx := context.Background()
	start := func(email string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		if err := acc.StartSignup(ctx, rec, httptest.NewRequest("POST", "/", nil), api.NewAccount{Email: email, Name: "N", Password: "long enough password", WorkspaceName: "W"}); err != nil {
			t.Fatal(err)
		}
		return rec
	}
	signupLink := regexp.MustCompile(`/signup/confirm#(ofv_[A-Za-z0-9_-]+)`)

	// An existing address: the same answer, no account, and its owner hears about it.
	n := e.outbox.count()
	if rec := start("JACK@example.com"); len(rec.Result().Cookies()) != 0 {
		t.Fatal("an existing address got a sign-up cookie")
	}
	if to, _ := e.outbox.last(t, "reset-password"); to != "jack@example.com" || e.outbox.count() != n+1 {
		t.Fatalf("reminder: %s", to)
	}

	// A new address: nothing exists until the link is opened.
	rec := start("new@example.com")
	var exists bool
	e.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE email = 'new@example.com')`).Scan(&exists) //nolint:errcheck
	if exists {
		t.Fatal("account made before confirming")
	}
	m := e.outbox.sent[len(e.outbox.sent)-1]
	g := signupLink.FindStringSubmatch(m.Text)
	if m.To != "new@example.com" || g == nil {
		t.Fatalf("confirmation: %s %q", m.To, m.Text)
	}
	// Asking again at once sends nothing more.
	n = e.outbox.count()
	start("new@example.com")
	if e.outbox.count() != n {
		t.Fatal("sent a second link within a minute")
	}

	// Opened in another browser, it asks for a password; the link still works.
	other := httptest.NewRequest("POST", "/", nil)
	if _, _, err := acc.FinishSignup(ctx, nil, httptest.NewRecorder(), other, g[1], ""); !errors.Is(err, api.ErrChoosePassword) {
		t.Fatalf("other browser: %v", err)
	}
	// In the browser that sent the form, it signs in with the form's password.
	same := httptest.NewRequest("POST", "/", nil)
	for _, c := range rec.Result().Cookies() {
		same.AddCookie(c)
	}
	out := httptest.NewRecorder()
	userID, wsID, err := acc.FinishSignup(ctx, nil, out, same, g[1], "")
	if err != nil || userID == "" || wsID == "" {
		t.Fatalf("finish: %v", err)
	}
	browser := &client{t: t, h: e.h}
	for _, c := range out.Result().Cookies() {
		if c.Name == "outflare_session" {
			browser.cookie = c
		}
	}
	if me := browser.must(200, "GET", "/v1/me", nil)["user"].(map[string]any); me["email_verified"] != true {
		t.Fatalf("me: %v", me)
	}
	(&client{t: t, h: e.h}).must(200, "POST", "/v1/auth/login", map[string]string{"email": "new@example.com", "password": "long enough password"})
	if _, _, err := acc.FinishSignup(ctx, nil, httptest.NewRecorder(), same, g[1], ""); !errors.Is(err, api.ErrBadSignupLink) {
		t.Fatalf("used twice: %v", err)
	}

	// Opened elsewhere with a password of the person's own choosing, the
	// form's password is ignored.
	start("third@example.com")
	g = signupLink.FindStringSubmatch(e.outbox.sent[len(e.outbox.sent)-1].Text)
	if _, _, err := acc.FinishSignup(ctx, nil, httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil), g[1], "my own password here"); err != nil {
		t.Fatal(err)
	}
	(&client{t: t, h: e.h}).must(401, "POST", "/v1/auth/login", map[string]string{"email": "third@example.com", "password": "long enough password"})
	(&client{t: t, h: e.h}).must(200, "POST", "/v1/auth/login", map[string]string{"email": "third@example.com", "password": "my own password here"})
}
