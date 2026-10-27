package api

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

func (s *Server) routesPayments(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/workspaces/{ws}/integrations/{id}/payment-sync", s.inWorkspace(auth.ScopeIntegrationsRead, s.getPaymentSync))
	mux.HandleFunc("POST /v1/workspaces/{ws}/integrations/{id}/payment-sync", s.inWorkspace(auth.ScopeIntegrationsManage, s.runPaymentSync))
}

type paymentSyncView struct {
	IntegrationID string        `json:"integration_id"`
	Connector     string        `json:"connector"`
	Phase         string        `json:"phase"`
	Totals        paymentTotals `json:"totals"`
	LastRunAt     *time.Time    `json:"last_run_at"`
	LastSuccessAt *time.Time    `json:"last_success_at"`
	LastError     *string       `json:"last_error"`
	BackfilledAt  *time.Time    `json:"backfilled_at"`
}

// paymentIntegration loads a payment connection, or answers 404.
func (s *Server) paymentIntegration(w http.ResponseWriter, r *http.Request) (connectors.Integration, bool) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "integration not found")
		return connectors.Integration{}, false
	}
	i, err := s.integrations.Get(r.Context(), s.pool, a.WorkspaceID, id)
	if errors.Is(err, connectors.ErrNotFound) || (err == nil && !slices.Contains(s.paymentConnectors(), i.Connector)) {
		writeError(w, r, http.StatusNotFound, "not_found", "payment connection not found: this integration does not read payments")
		return i, false
	}
	if err != nil {
		s.internal(w, r, err)
		return i, false
	}
	return i, true
}

func (s *Server) getPaymentSync(w http.ResponseWriter, r *http.Request) {
	i, ok := s.paymentIntegration(w, r)
	if !ok {
		return
	}
	v := paymentSyncView{IntegrationID: i.ID, Connector: i.Connector, Phase: "backfill"}
	err := s.pool.QueryRow(r.Context(), `SELECT phase, totals, last_run_at, last_success_at, last_error, backfilled_at
		FROM payment_syncs WHERE integration_id = $1 AND workspace_id = $2`, i.ID, i.WorkspaceID).
		Scan(&v.Phase, &v.Totals, &v.LastRunAt, &v.LastSuccessAt, &v.LastError, &v.BackfilledAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// runPaymentSync queues a sync now, instead of waiting for the next one.
func (s *Server) runPaymentSync(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	i, ok := s.paymentIntegration(w, r)
	if !ok {
		return
	}
	if i.Status != connectors.StatusConnected {
		writeError(w, r, http.StatusConflict, "conflict", "this connection is not working: test it or connect it again first")
		return
	}
	ctx := r.Context()
	if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.queuePaymentSync(ctx, tx, a.WorkspaceID, i.ID, fmt.Sprintf("%s:now:%d", i.ID, time.Now().Unix()/10)); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "payments.sync_requested", "integration", i.ID, map[string]any{"connector": i.Connector})
	}); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"queued": true})
}
