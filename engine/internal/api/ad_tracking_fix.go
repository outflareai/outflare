package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/adsync"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// Adding the tracking template to an ad account's ads for the workspace:
// GET reads the account's URL suffixes from the platform and answers with
// the changes it would make; POST queues the job that makes them
// (adsync.JobTracking), since the platform call is the side effect.

func (s *Server) routesAdTrackingFix(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/workspaces/{ws}/ads/accounts/{id}/tracking", s.inWorkspace(auth.ScopeAnalyticsRead, s.planAdTracking))
	mux.HandleFunc("POST /v1/workspaces/{ws}/ads/accounts/{id}/tracking", s.inWorkspace(auth.ScopeAdsManage, s.addAdTracking))
	mux.HandleFunc("GET /v1/workspaces/{ws}/ads/accounts/{id}/tracking/run", s.inWorkspace(auth.ScopeAnalyticsRead, s.getAdTrackingRun))
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

// adTrackingRun is the latest request to add the template to an account,
// as the page shows it while it runs and after.
type adTrackingRun struct {
	Status      string                      `json:"status"`
	RequestedAt time.Time                   `json:"requested_at"`
	FinishedAt  *time.Time                  `json:"finished_at"`
	Changed     int                         `json:"changed"`
	Failed      int                         `json:"failed"`
	Errors      []connectors.TrackingChange `json:"errors"`
	Error       string                      `json:"error"`
}

// adTrackingRunErrorsMax is how many refused changes the run lists.
const adTrackingRunErrorsMax = 50

func (s *Server) getAdTrackingRun(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	id := r.PathValue("id")
	if !isUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "ad account not found")
		return
	}
	var brand *string
	err := s.pool.QueryRow(ctx, `SELECT brand_id FROM ad_accounts WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, id).Scan(&brand)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.sees(brand)) {
		writeError(w, r, http.StatusNotFound, "not_found", "ad account not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var (
		status    string
		attempts  int
		lastError *string
		created   time.Time
		updated   time.Time
	)
	err = s.pool.QueryRow(ctx, `SELECT status, attempts, last_error, created_at, updated_at FROM jobs
		WHERE kind = $3 AND idempotency_key LIKE $2 AND workspace_id = $1 ORDER BY id DESC LIMIT 1`,
		a.WorkspaceID, "ads.tracking:"+id+":%", adsync.JobTracking).Scan(&status, &attempts, &lastError, &created, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]any{"run": nil})
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	run := adTrackingRun{Status: status, RequestedAt: created, Errors: []connectors.TrackingChange{}}
	jobError := func() string {
		if lastError == nil {
			return ""
		}
		e := strings.TrimSpace(strings.TrimPrefix(*lastError, jobs.ErrPermanent.Error()))
		if len(e) > 500 {
			e = e[:500]
		}
		return e
	}
	switch status {
	case "queued":
		if attempts > 0 {
			run.Status, run.Error = "retrying", jobError()
		}
	case "dead":
		run.Status, run.Error, run.FinishedAt = "failed", jobError(), &updated
	case "succeeded":
		run.Status, run.FinishedAt = "done", &updated
		var data []byte
		err := s.pool.QueryRow(ctx, `SELECT data FROM audit_events WHERE workspace_id = $1 AND action = 'ad_account.tracking_added'
			AND target_id = $2 AND created_at >= $3 ORDER BY id DESC LIMIT 1`, a.WorkspaceID, id, created).Scan(&data)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.internal(w, r, err)
			return
		}
		var got struct {
			Changed int                         `json:"changed"`
			Failed  int                         `json:"failed"`
			Changes []connectors.TrackingChange `json:"changes"`
		}
		_ = json.Unmarshal(data, &got)
		run.Changed, run.Failed = got.Changed, got.Failed
		for _, ch := range got.Changes {
			if ch.Error != "" && len(run.Errors) < adTrackingRunErrorsMax {
				run.Errors = append(run.Errors, ch)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run})
}
