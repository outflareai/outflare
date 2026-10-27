package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestPanicEventSaysWhere(t *testing.T) {
	got := track.Capture(t)
	s := &Server{log: slog.New(slog.DiscardHandler)}
	h := s.recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		var m map[string]int
		m["boom"] = 1 // a nil map write: the runtime panics inside runtime.* frames
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/anything", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.Code)
	}
	ev := got()
	if len(ev) != 1 || ev[0].Name != "server.panicked" {
		t.Fatalf("events: %+v", ev)
	}
	where, _ := ev[0].Props["where"].(string)
	if !strings.HasPrefix(where, "engine/internal/api/panic_internal_test.go:") || !strings.Contains(where, "TestPanicEventSaysWhere") {
		t.Fatalf("where = %q", where)
	}
}

func TestAbortedResponseIsNotACrash(t *testing.T) {
	got := track.Capture(t)
	s := &Server{log: slog.New(slog.DiscardHandler)}
	h := s.recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler) // what httputil.ReverseProxy does when a stream drops
	}))
	func() {
		defer func() {
			if v := recover(); v != http.ErrAbortHandler {
				t.Fatalf("recovered %v, want http.ErrAbortHandler passed on to net/http", v)
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/mcp", nil))
	}()
	if ev := got(); len(ev) != 0 {
		t.Fatalf("events: %+v", ev)
	}
}
