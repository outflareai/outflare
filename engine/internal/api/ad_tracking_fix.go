package api

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/adsync"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Adding the tracking template to an ad account's ads for the workspace:
// GET reads the account's URL suffixes from the platform and answers with
// the changes it would make; POST queues the job that makes them
// (adsync.JobTracking), since the platform call is the side effect.

func (s *Server) routesAdTrackingFix(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/workspaces/{ws}/ads/accounts/{id}/tracking", s.inWorkspace(auth.ScopeAnalyticsRead, s.planAdTracking))
	mux.HandleFunc("POST /v1/workspaces/{ws}/ads/accounts/{id}/tracking", s.inWorkspace(auth.ScopeAdsManage, s.addAdTracking))
}

// adTrackingAccount reads the account the caller names, answering the
// error itself when it can't be used.
func (s *Server) adTrackingAccount(w http.ResponseWriter, r *http.Request, write bool) (*adsync.Tracking, bool) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !isUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "ad account not found")
		return nil, false
	}
	var brand *string
	err := s.pool.QueryRow(r.Context(), `SELECT brand_id FROM ad_accounts WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, id).Scan(&brand)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.sees(brand)) {
		writeError(w, r, http.StatusNotFound, "not_found", "ad account not found")
		return nil, false
	}
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	if write && !a.writes(brand) {
		writeError(w, r, http.StatusForbidden, "forbidden", "you can only change ad accounts of your own brands")
		return nil, false
	}
	t, err := s.adSync.OpenTracking(r.Context(), a.WorkspaceID, id)
	switch {
	case errors.Is(err, adsync.ErrNoTracker):
		writeError(w, r, http.StatusUnprocessableEntity, "connector_invalid", "Outflare can't add the tracking template on this ad platform yet: paste it in by hand (Tracking templates)")
		return nil, false
	case errors.Is(err, connectors.ErrNotFound):
		writeError(w, r, http.StatusConflict, "not_active", "the ad account's connection no longer sees it")
		return nil, false
	case err != nil:
		s.connectorFailed(w, r, err)
		return nil, false
	}
	return t, true
}

func (s *Server) planAdTracking(w http.ResponseWriter, r *http.Request) {
	t, ok := s.adTrackingAccount(w, r, false)
	if !ok {
		return
	}
	plan, err := t.Plan(r.Context())
	if err != nil {
		s.connectorFailed(w, r, err)
		return
	}
	if plan == nil {
		plan = []connectors.TrackingChange{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"connector": t.Connector, "template": t.Template, "changes": plan})
}

func (s *Server) addAdTracking(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	t, ok := s.adTrackingAccount(w, r, true)
	if !ok {
		return
	}
	id := r.PathValue("id")
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if err := adsync.EnqueueTracking(r.Context(), tx, a.WorkspaceID, id, a.ActorID, RequestID(r.Context())); err != nil {
			return err
		}
		return recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "ad_account.tracking_requested", "ad_account", id,
			map[string]any{"ad_account_id": id, "connector": t.Connector, "template": t.Template, "brand_id": t.Brand},
			map[string]any{"connector": t.Connector})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}
