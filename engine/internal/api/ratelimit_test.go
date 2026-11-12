package api_test

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
)

func TestRateLimitPerToken(t *testing.T) {
	e := freshEnv(t)
	h := handler(t, api.NewServer(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)), api.Options{Box: e.box, RateLimit: 3}))
	owner, ws := setupOwner(t, h)
	secret := owner.must(201, "POST", "/v1/workspaces/"+ws+"/api-tokens", map[string]any{"name": "bot", "scopes": []string{"leads.read"}})["token"].(string)
	get := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/v1/workspaces/"+ws+"/leads", nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for i := 0; i < 3; i++ {
		if rec := get(); rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("RateLimit"), `"token";r=`) || rec.Header().Get("RateLimit-Policy") != `"token";q=3;w=60` {
			t.Fatalf("request %d: %d %v", i, rec.Code, rec.Header())
		}
	}
	rec := get()
	if rec.Code != 429 || rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), "rate_limited") {
		t.Fatalf("over the limit: %d %s", rec.Code, rec.Body)
	}
	// People in the web app are not limited by the token's limit.
	for i := 0; i < 5; i++ {
		owner.must(200, "GET", "/v1/workspaces/"+ws+"/leads", nil)
	}
}

// A signed-in session has a limit of its own, four times a token's, so one
// session can't flood the server.
func TestRateLimitPerSession(t *testing.T) {
	e := freshEnv(t)
	h := handler(t, api.NewServer(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)), api.Options{Box: e.box, RateLimit: 5}))
	owner, ws := setupOwner(t, h)
	// Setting up used some of the 20 already.
	for i := 0; i < 21; i++ {
		code, body := owner.do("GET", "/v1/workspaces/"+ws+"/leads", nil)
		if code == 200 {
			continue
		}
		msg, _ := body["error"].(map[string]any)["message"].(string)
		if code != 429 || !strings.Contains(msg, "this session made more than 20 requests") {
			t.Fatalf("request %d: %d %v", i, code, body)
		}
		return
	}
	t.Fatal("a session was never limited")
}
