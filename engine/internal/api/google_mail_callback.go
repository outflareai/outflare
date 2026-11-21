package api

import (
	"context"
	"net/http"
	"strings"
)

// Gmail consent (CRM email capture and Sales "My email") uses the same Google
// OAuth client as "Continue with Google", and returns to the same redirect
// URI, /v1/auth/google/callback, which every server that offers Google
// sign-in already registers. Each flow's state starts with its own prefix, so
// the sign-in callback hands it to the right handler. The old per-feature
// callbacks stay registered so a consent started before the switch can still
// finish, with the redirect URI it started with.
const (
	salesStatePrefix    = "sales."
	crmStatePrefix      = "crm."
	salesLegacyCallback = "/v1/sales/google/callback"
	crmLegacyCallback   = "/v1/crm-mail/google/callback"
)

// mailConsentHandler returns the handler for a Gmail consent state, or nil
// for a sign-in state.
func (s *Server) mailConsentHandler(state string) http.HandlerFunc {
	switch {
	case strings.HasPrefix(state, salesStatePrefix):
		return s.consentUser(s.finishSalesGoogle)
	case strings.HasPrefix(state, crmStatePrefix):
		return s.consentUser(s.finishCRMGoogle)
	}
	return nil
}

// consentUser is requireUser for a consent arriving on the sign-in callback.
// The route itself stays public (it signs people in), so it doesn't note
// signed-in access for the API contract; the consent still needs the session
// of the person who started it.
func (s *Server) consentUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.authenticated(w, r)
		if !ok {
			return
		}
		if p.UserID == "" {
			writeError(w, r, http.StatusForbidden, "forbidden", "this endpoint needs a signed-in user, not an API token")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxPrincipalKey{}, p)))
	}
}

// mailRedirectURI is the redirect URI a consent started with, which the code
// exchange must repeat exactly.
func (s *Server) mailRedirectURI(r *http.Request, legacy string) string {
	if r.URL.Path == legacy {
		return s.publicURL + legacy
	}
	return s.loginRedirectURI()
}
