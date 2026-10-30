package api

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/inboundemail"
)

// Personal addresses a member links to forward AI receipts from, such as
// their own Gmail, besides their work address (package inboundemail).
// Gmail's forwarding confirmation for one is kept here for them to click.

func (s *Server) routesInboundForwarders(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/workspaces/{ws}/inbound-forwarders", s.inBrand(auth.ScopeMyAIAccountsRead, s.listInboundForwarders))
	mux.HandleFunc("POST /v1/workspaces/{ws}/inbound-forwarders", s.inBrand(auth.ScopeMyAIAccountsManage, s.linkInboundForwarder))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/inbound-forwarders/{email}", s.inBrand(auth.ScopeMyAIAccountsManage, s.unlinkInboundForwarder))
}

func (s *Server) listInboundForwarders(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	user := a.personID()
	if r.URL.Query().Get("everyone") == "true" && auth.Grants(a.Scopes, auth.ScopeAIAccountsRead) {
		user = ""
	} else if user == "" {
		writeJSON(w, http.StatusOK, map[string]any{"data": []inboundemail.Forwarder{}})
		return
	}
	list, err := inboundemail.Forwarders(r.Context(), s.pool, a.WorkspaceID, user)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// linkInboundForwarder lets the caller forward from a personal address,
// such as their own Gmail, before Gmail asks to confirm it.
func (s *Server) linkInboundForwarder(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Email string `json:"email"`
	}
	if !decode(w, r, &req) {
		return
	}
	if a.personID() == "" {
		writeError(w, r, http.StatusForbidden, "forbidden", "only a person links their own address")
		return
	}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if err := inboundemail.LinkForwarder(r.Context(), tx, a.WorkspaceID, a.personID(), req.Email); err != nil {
			return forwarderInvalid{err}
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "inbound_forwarder.linked", "user", a.personID(), nil)
	})
	var bad forwarderInvalid
	if errors.As(err, &bad) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", bad.Error())
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := inboundemail.Forwarders(r.Context(), s.pool, a.WorkspaceID, a.personID())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"data": list})
}

func (s *Server) unlinkInboundForwarder(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	user := a.personID()
	if auth.Grants(a.Scopes, auth.ScopeAIAccountsManage) {
		user = ""
	}
	var gone bool
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		if gone, err = inboundemail.UnlinkForwarder(r.Context(), tx, a.WorkspaceID, user, r.PathValue("email")); err != nil || !gone {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "inbound_forwarder.unlinked", "user", a.personID(), nil)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !gone {
		writeError(w, r, http.StatusNotFound, "not_found", "no linked address like that")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// forwarderInvalid marks a refusal to link an address, answered with 400.
type forwarderInvalid struct{ error }
