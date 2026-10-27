package api

import (
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/inbound"
)

// Live updates: a connection's own webhook address, so a connector tells
// Outflare when something happens instead of being asked.
func (s *Server) routesConnectionHooks(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/hooks/{token}", s.receiveConnectionHook)
	read, manage := auth.ScopeIntegrationsRead, auth.ScopeIntegrationsManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/integrations/{id}/hook", s.inWorkspace(read, s.getConnectionHook))
	mux.HandleFunc("POST /v1/workspaces/{ws}/integrations/{id}/hook", s.inWorkspace(manage, s.setUpConnectionHook))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/integrations/{id}/hook", s.inWorkspace(manage, s.removeConnectionHook))
	mux.HandleFunc("POST /v1/workspaces/{ws}/integrations/{id}/hook/rotate", s.inWorkspace(manage, s.rotateConnectionHook))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/integrations/{id}/hook/signing-secret", s.inWorkspace(manage, s.setConnectionHookSecret))
	mux.HandleFunc("GET /v1/workspaces/{ws}/hook-events", s.inWorkspace(read, s.listHookEvents))
	mux.HandleFunc("POST /v1/workspaces/{ws}/hook-events/{event}/replay", s.inWorkspace(manage, s.replayHookEvent))
}

// receiveConnectionHook is where connectors post their webhooks. It answers
// at once; the events are handled by workers.
func (s *Server) receiveConnectionHook(w http.ResponseWriter, r *http.Request) {
	if s.inbound == nil {
		writeError(w, r, http.StatusNotFound, "not_found", "no such webhook address")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "the body is limited to 1 MiB")
		return
	}
	got, err := s.inbound.Receive(r.Context(), r.PathValue("token"), r.Header, body)
	switch {
	case errors.Is(err, inbound.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "no such webhook address: it may have been replaced or turned off")
	case errors.Is(err, inbound.ErrBadSignature):
		writeError(w, r, http.StatusUnauthorized, "bad_signature", "the signature does not match")
	case err != nil && connectors.KindOf(err) == connectors.ErrInvalid:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "the delivery could not be read: "+err.Error())
	case err != nil:
		s.internal(w, r, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"received": got.Events, "duplicates": got.Duplicates})
	}
}

// hookIntegration loads a connection that can send webhooks, or answers.
func (s *Server) hookIntegration(w http.ResponseWriter, r *http.Request) (connectors.Integration, bool) {
	a := accessFrom(r.Context())
	if s.inbound == nil || s.integrations == nil {
		writeError(w, r, http.StatusNotFound, "not_found", "live updates are not available on this server")
		return connectors.Integration{}, false
	}
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "integration not found")
		return connectors.Integration{}, false
	}
	i, err := s.integrations.Get(r.Context(), s.pool, a.WorkspaceID, id)
	if errors.Is(err, connectors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "integration not found")
		return i, false
	}
	if err != nil {
		s.internal(w, r, err)
		return i, false
	}
	if _, ok := s.inbound.Hooked(i.Connector); !ok {
		writeError(w, r, http.StatusNotFound, "not_found", "this connection can't send live updates")
		return i, false
	}
	return i, true
}

func (s *Server) writeConnectionHook(w http.ResponseWriter, r *http.Request, i connectors.Integration, status int) {
	v, err := s.inbound.Get(r.Context(), s.pool, i.WorkspaceID, i)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, status, v)
}

func (s *Server) getConnectionHook(w http.ResponseWriter, r *http.Request) {
	if i, ok := s.hookIntegration(w, r); ok {
		s.writeConnectionHook(w, r, i, http.StatusOK)
	}
}

// hookChange runs one change to a connection's live updates with its
// audit event, and answers the result.
func (s *Server) hookChange(w http.ResponseWriter, r *http.Request, action string, change func(tx pgx.Tx, i connectors.Integration) error) {
	a := accessFrom(r.Context())
	i, ok := s.hookIntegration(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := change(tx, i); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "integration", i.ID, map[string]any{"connector": i.Connector})
	})
	switch {
	case errors.Is(err, inbound.ErrUnreachable):
		writeError(w, r, http.StatusConflict, "conflict", "this server can't be reached from the internet (PUBLIC_URL is not a public https address), so connectors can't send it webhooks")
	case errors.Is(err, inbound.ErrNotSetUp):
		writeError(w, r, http.StatusConflict, "conflict", "live updates are not set up for this connection: turn them on first")
	case err != nil:
		s.internal(w, r, err)
	case action == "connection_hook.removed":
		w.WriteHeader(http.StatusNoContent)
	default:
		s.writeConnectionHook(w, r, i, http.StatusOK)
	}
}

func (s *Server) setUpConnectionHook(w http.ResponseWriter, r *http.Request) {
	s.hookChange(w, r, "connection_hook.set_up", func(tx pgx.Tx, i connectors.Integration) error {
		return s.inbound.SetUp(r.Context(), tx, i.WorkspaceID, i.ID)
	})
}

func (s *Server) rotateConnectionHook(w http.ResponseWriter, r *http.Request) {
	s.hookChange(w, r, "connection_hook.rotated", func(tx pgx.Tx, i connectors.Integration) error {
		return s.inbound.Rotate(r.Context(), tx, i.WorkspaceID, i.ID)
	})
}

func (s *Server) removeConnectionHook(w http.ResponseWriter, r *http.Request) {
	s.hookChange(w, r, "connection_hook.removed", func(tx pgx.Tx, i connectors.Integration) error {
		return s.inbound.Remove(r.Context(), tx, i.WorkspaceID, i.ID)
	})
}

func (s *Server) setConnectionHookSecret(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Secret *string `json:"secret"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Secret == nil || len(*body.Secret) > 500 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "secret is required: the signing secret the connector shows for this address, or \"\" to remove it")
		return
	}
	secret := strings.TrimSpace(*body.Secret)
	s.hookChange(w, r, "connection_hook.secret_set", func(tx pgx.Tx, i connectors.Integration) error {
		return s.inbound.SetSecret(r.Context(), tx, i.WorkspaceID, i.ID, secret)
	})
}

var hookEventStatuses = []string{inbound.EventQueued, inbound.EventHandled, inbound.EventIgnored, inbound.EventFailed}

func (s *Server) listHookEvents(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if s.inbound == nil {
		writeJSON(w, http.StatusOK, map[string]any{"data": []any{}, "next_before": nil})
		return
	}
	q := r.URL.Query()
	f := inbound.Filter{IntegrationID: q.Get("integration_id"), Status: q.Get("status")}
	if f.IntegrationID != "" && !validUUID(f.IntegrationID) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "integration_id must be a connection id")
		return
	}
	if f.Status != "" && !slices.Contains(hookEventStatuses, f.Status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is "+strings.Join(hookEventStatuses, ", "))
		return
	}
	var ok bool
	if f.Before, ok = beforeTime(w, r); !ok {
		return
	}
	if f.Limit, ok = pageLimit(w, r, 50, 200); !ok {
		return
	}
	list, err := s.inbound.Events(r.Context(), s.pool, a.WorkspaceID, f)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *string
	if len(list) == f.Limit {
		t := list[len(list)-1].ReceivedAt.Format("2006-01-02T15:04:05.999999999Z07:00")
		next = &t
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}

func (s *Server) replayHookEvent(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("event")
	if s.inbound == nil || !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "event not found")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.inbound.Replay(ctx, tx, a.WorkspaceID, id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "connection_hook.event_replayed", "hook_event", id, nil)
	})
	switch {
	case errors.Is(err, inbound.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "event not found")
	case err != nil:
		s.internal(w, r, err)
	default:
		writeJSON(w, http.StatusAccepted, map[string]bool{"queued": true})
	}
}
