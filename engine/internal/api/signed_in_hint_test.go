package api_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
)

// hint is the outflare_signed_in cookie a response set, if any.
func hint(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == "outflare_signed_in" {
			return c
		}
	}
	return nil
}

func TestSignedInHint(t *testing.T) {
	e := freshEnv(t)
	h := handler(t, api.NewServer(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)), api.Options{Box: e.box,
		PublicURL: "https://app.example.com", SignedInCookieDomain: "example.com"}))
	setupOwner(t, h)

	send := func(method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	rec := send("POST", "/v1/auth/login", `{"email":"jack@example.com","password":"long enough password"}`)
	c := hint(rec)
	if rec.Code != 200 || c == nil || c.Value != "1" || c.Domain != "example.com" || c.HttpOnly || !c.Secure || c.MaxAge <= 0 {
		t.Fatalf("sign-in should set a readable hint holding only 1 on the parent domain: %d %+v", rec.Code, c)
	}
	var session *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "outflare_session" {
			session = ck
		}
	}
	if session == nil || session.Domain != "" || !session.HttpOnly {
		t.Fatalf("the session must stay a host-only HttpOnly cookie: %+v", session)
	}
	present := &http.Cookie{Name: "outflare_signed_in", Value: "1"}

	// A session from before the hint gets it on its next request; one that
	// has it already is left alone.
	if c := hint(send("GET", "/v1/me", "", session)); c == nil || c.Value != "1" {
		t.Fatalf("missing hint not restored: %+v", c)
	}
	if c := hint(send("GET", "/v1/me", "", session, present)); c != nil {
		t.Fatalf("hint rewritten on every request: %+v", c)
	}

	// Signing out removes it, and a dead session's hint goes on the next visit.
	rec = send("POST", "/v1/auth/logout", "", session, present)
	if c := hint(rec); rec.Code != 204 || c == nil || c.MaxAge >= 0 || c.Domain != "example.com" {
		t.Fatalf("sign-out should clear the hint: %d %+v", rec.Code, c)
	}
	rec = send("GET", "/v1/me", "", session, present)
	if c := hint(rec); rec.Code != 401 || c == nil || c.MaxAge >= 0 {
		t.Fatalf("a signed-out session's hint should be cleared: %d %+v", rec.Code, c)
	}
	// Anonymous visitors without the hint get no cookie at all.
	if c := hint(send("GET", "/v1/me", "")); c != nil {
		t.Fatalf("anonymous request set %+v", c)
	}
}

func TestSignedInHintOffByDefault(t *testing.T) {
	h := freshServer(t)
	setupOwner(t, h)
	req := httptest.NewRequest("POST", "/v1/auth/login", strings.NewReader(`{"email":"jack@example.com","password":"long enough password"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || hint(rec) != nil {
		t.Fatalf("no hint without SignedInCookieDomain: %d %+v", rec.Code, hint(rec))
	}
}
