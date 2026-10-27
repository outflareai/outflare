package api_test

import (
	"net/http/httptest"
	"testing"
)

func TestSecurityHeaders(t *testing.T) {
	e := freshEnv(t)
	req := httptest.NewRequest("GET", "/v1/meta", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	for k, v := range map[string]string{"X-Content-Type-Options": "nosniff", "Cache-Control": "no-store", "X-Frame-Options": "DENY", "Content-Security-Policy": "frame-ancestors 'none'"} {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s: %q, want %q", k, got, v)
		}
	}
}
