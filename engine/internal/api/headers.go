package api

import (
	"net/http"
	"strings"
)

// securityHeaders sets the headers every answer should carry. API answers
// hold workspace data, so nothing caches them unless a handler says so;
// no page of the app may be framed by another site; and once the server
// is on https, browsers are told to stay there.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	https := strings.HasPrefix(s.publicURL, "https://")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Content-Security-Policy", "frame-ancestors 'none'")
		h.Set("X-Frame-Options", "DENY")
		if https {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}
