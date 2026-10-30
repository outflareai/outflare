package api

import (
	"net/http"
	"strings"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/emailfind"
)

// The email finder proposes a likely work address for a person from the
// workspace's own learned patterns and first-party history, and checks an
// address through the verification waterfall (free native tiers first, the
// workspace's own verifier only when it must). Everything here reads and
// writes the one workspace's data; nothing is shared between workspaces.
func (s *Server) routesEmailFind(mux *http.ServeMux) {
	read, manage := auth.ScopeLeadsRead, auth.ScopeLeadsManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/email-finder/pattern", s.inWorkspace(read, s.emailFinderPattern))
	mux.HandleFunc("POST /v1/workspaces/{ws}/email-finder/find", s.inWorkspace(read, s.emailFinderFind))
	mux.HandleFunc("POST /v1/workspaces/{ws}/email-finder/verify", s.inWorkspace(manage, s.emailFinderVerify))
}

// emailFinderUnset reports whether the finder is wired. It is nil when the
// engine started without it (for example in a bare test server).
func (s *Server) emailFinderUnset(w http.ResponseWriter, r *http.Request) bool {
	if s.emailFinder == nil {
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "email finding is not configured on this server")
		return true
	}
	return false
}

// emailFinderPattern returns a domain's learned address shape for the
// workspace, building it on first use. Free.
func (s *Server) emailFinderPattern(w http.ResponseWriter, r *http.Request) {
	if s.emailFinderUnset(w, r) {
		return
	}
	domain := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("domain")))
	if domain == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "domain is required")
		return
	}
	pat, code, err := emailfind.FindCompanyPattern(r.Context(), s.emailFinder.Pool, s.emailFinder.Resolver, accessFrom(r.Context()).WorkspaceID, domain)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": pat, "code": code})
}

// emailFinderFind proposes an address for each person in the batch. Free: it
// spends nothing, using only the learned pattern and first-party history.
func (s *Server) emailFinderFind(w http.ResponseWriter, r *http.Request) {
	if s.emailFinderUnset(w, r) {
		return
	}
	var req emailfind.FindRequest
	if !decode(w, r, &req) {
		return
	}
	if len(req.Items) == 0 || len(req.Items) > emailfind.MaxBatch {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "items must hold 1 to 50 people")
		return
	}
	out, err := s.emailFinder.Find(r.Context(), accessFrom(r.Context()).WorkspaceID, req)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// emailFinderVerify checks a batch of addresses through the waterfall. It may
// spend on the workspace's own connected verifier at the last tier, billed to
// that workspace, never to Outflare. A slow check answers pending and runs as
// its own short job; re-call to collect it.
func (s *Server) emailFinderVerify(w http.ResponseWriter, r *http.Request) {
	if s.emailFinderUnset(w, r) {
		return
	}
	var req emailfind.VerifyRequest
	if !decode(w, r, &req) {
		return
	}
	if len(req.Items) == 0 || len(req.Items) > emailfind.MaxBatch {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "items must hold 1 to 50 addresses")
		return
	}
	out, err := s.emailFinder.VerifyEmails(r.Context(), accessFrom(r.Context()).WorkspaceID, req)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}
