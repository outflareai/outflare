package api

import (
	"errors"
	"net/http"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/classify"
)

func (s *Server) routesClassify(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/workspaces/{ws}/classifications", s.inWorkspace(auth.ScopeLeadsRead, s.classify))
	mux.HandleFunc("GET /v1/workspaces/{ws}/email-domains/{domain}", s.inWorkspace(auth.ScopeLeadsRead, s.getEmailDomain))
	// The same lookup under a name that says what it is: email-domains is
	// also what people call Lifecycle sending domains.
	mux.HandleFunc("GET /v1/workspaces/{ws}/domain-lookups/{domain}", s.inWorkspace(auth.ScopeLeadsRead, s.getEmailDomain))
}

// classify says what kind of email infrastructure an address or domain
// is. It changes nothing, so it is safe to retry and needs no
// Idempotency-Key.
func (s *Server) classify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email  string `json:"email"`
		Domain string `json:"domain"`
	}
	if !decode(w, r, &req) {
		return
	}
	s.writeClassification(w, r, classify.Input{Email: req.Email, Domain: req.Domain})
}

func (s *Server) getEmailDomain(w http.ResponseWriter, r *http.Request) {
	s.writeClassification(w, r, classify.Input{Domain: r.PathValue("domain")})
}

func (s *Server) writeClassification(w http.ResponseWriter, r *http.Request, in classify.Input) {
	res, err := s.classifier.Classify(r.Context(), in)
	if err == nil {
		// An unknown domain is the long tail: add this workspace's own
		// research, or ask for it.
		err = s.withResearch(r, r.PathValue("ws"), &res)
	}
	switch {
	case errors.Is(err, classify.ErrInput), errors.Is(err, classify.ErrInvalidEmail),
		errors.Is(err, classify.ErrInvalidDomain), errors.Is(err, classify.ErrPublicSuffix):
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
	case err != nil:
		s.internal(w, r, err)
	default:
		writeJSON(w, http.StatusOK, res)
	}
}
