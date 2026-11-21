package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A request its caller closed is client_closed, never a server error; any
// other failure, or a deadline that ran out, stays internal.
func TestInternalWhenTheClientIsGone(t *testing.T) {
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	late, stop := context.WithTimeout(context.Background(), 0)
	defer stop()
	for name, c := range map[string]struct {
		ctx  context.Context
		err  error
		want int
	}{
		"closed by the client":     {gone, fmt.Errorf("query: %w", context.Canceled), statusClientClosed},
		"client gone, other error": {gone, errors.New("boom"), http.StatusInternalServerError},
		"deadline":                 {late, context.DeadlineExceeded, http.StatusInternalServerError},
		"still there":              {context.Background(), context.Canceled, http.StatusInternalServerError},
	} {
		w := httptest.NewRecorder()
		s.internal(w, httptest.NewRequest("GET", "/v1/me", nil).WithContext(c.ctx), c.err)
		if w.Code != c.want {
			t.Fatalf("%s: %d", name, w.Code)
		}
	}
}
