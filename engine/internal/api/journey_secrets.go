package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/journeys"
)

// Journey webhook keys. Every webhook step's call is signed with the
// workspace's signing secret; a step whose receiver wants its own key
// (auth header, basic or query) has it set here, sealed, and it is never
// shown again. Keys never travel in a journey's steps, so reading a
// journey never shows one.

func (s *Server) routesJourneySecrets(mux *http.ServeMux) {
	read, manage := auth.ScopeEmailsRead, auth.ScopeEmailsManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/journeys/signing-secret", s.inWorkspace(manage, s.journeySigningSecret))
	mux.HandleFunc("POST /v1/workspaces/{ws}/journeys/signing-secret/rotate", s.inWorkspace(manage, s.rotateJourneySigningSecret))
	mux.HandleFunc("GET /v1/workspaces/{ws}/journeys/{id}/step-secrets", s.inWorkspace(read, s.listJourneyStepSecrets))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/journeys/{id}/step-secrets/{step}", s.inWorkspace(manage, s.setJourneyStepSecret))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/journeys/{id}/step-secrets/{step}", s.inWorkspace(manage, s.deleteJourneyStepSecret))
}

// journeySigningSecret shows the workspace's signing secret, making it
// the first time. Showing it is recorded.
func (s *Server) journeySigningSecret(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var info journeys.SigningSecretInfo
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if info, err = journeys.SigningSecret(ctx, tx, s.box, a.WorkspaceID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "journey_signing_secret.revealed", "workspace", a.WorkspaceID, nil)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// rotateJourneySigningSecret makes a new signing secret. The old one keeps
// signing alongside it for a day, so receivers can switch without missing
// a call; expire_now: true drops it at once (for a leaked secret).
func (s *Server) rotateJourneySigningSecret(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		ExpireNow bool `json:"expire_now"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	var info journeys.SigningSecretInfo
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := journeys.RotateSigningSecret(ctx, tx, s.box, a.WorkspaceID, req.ExpireNow); err != nil {
			return err
		}
		var err error
		if info, err = journeys.SigningSecret(ctx, tx, s.box, a.WorkspaceID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "journey_signing_secret.rotated", "workspace", a.WorkspaceID,
			map[string]any{"expire_now": req.ExpireNow})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

type journeyStepSecretView struct {
	Step      string    `json:"step"`
	UpdatedAt time.Time `json:"updated_at"`
}

// listJourneyStepSecrets lists which steps have a key set, never the keys.
func (s *Server) listJourneyStepSecrets(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findJourney(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT step, updated_at FROM journey_step_secrets WHERE workspace_id = $1 AND journey_id = $2 ORDER BY step`,
		a.WorkspaceID, cur.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (journeyStepSecretView, error) {
		var v journeyStepSecretView
		return v, row.Scan(&v.Step, &v.UpdatedAt)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []journeyStepSecretView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// journeyStep finds a webhook step that sends a key, for setting it.
func (s *Server) journeyKeyStep(w http.ResponseWriter, r *http.Request) (*journeyView, *journeys.Step, bool) {
	a := accessFrom(r.Context())
	cur, ok := s.findJourney(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return nil, nil, false
	}
	id := r.PathValue("step")
	for i := range cur.Steps {
		if cur.Steps[i].ID == id {
			return cur, &cur.Steps[i], true
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "the journey has no step "+id)
	return nil, nil, false
}

// setJourneyStepSecret sets the key a webhook step sends, in place of any
// earlier one. Set the step's auth first: the key is the header's value,
// the password (basic) or the URL parameter's value.
func (s *Server) setJourneyStepSecret(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, st, ok := s.journeyKeyStep(w, r)
	if !ok {
		return
	}
	if !journeys.NeedsSecret(*st) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "step "+st.ID+" sends no key: set its auth type to header, basic or query first")
		return
	}
	var req struct {
		Value *string `json:"value"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Value == nil || strings.TrimSpace(*req.Value) == "" || len(*req.Value) > 4000 || strings.ContainsAny(*req.Value, "\r\n") {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "value is the key: one line, up to 4,000 characters")
		return
	}
	var by *string
	if a.ActorID != "" && a.ActorType == "human" {
		by = &a.ActorID
	}
	ctx := r.Context()
	var v journeyStepSecretView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		at, err := journeys.SetStepSecret(ctx, tx, s.box, a.WorkspaceID, cur.ID, st.ID, *req.Value, by)
		if err != nil {
			return err
		}
		v = journeyStepSecretView{Step: st.ID, UpdatedAt: at}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "journey.step_secret_set", "journey", cur.ID,
			map[string]any{"step": st.ID, "auth": st.Auth.Type})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) deleteJourneyStepSecret(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findJourney(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	step := r.PathValue("step")
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM journey_step_secrets WHERE workspace_id = $1 AND journey_id = $2 AND step = $3`, a.WorkspaceID, cur.ID, step)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "journey.step_secret_deleted", "journey", cur.ID, map[string]any{"step": step})
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "step "+step+" has no key")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// checkJourneyKeys checks, to go live, that every webhook step that sends
// a key has one.
func (s *Server) checkJourneyKeys(r *http.Request, ws, journeyID string, steps []journeys.Step) error {
	for _, st := range steps {
		if !journeys.NeedsSecret(st) {
			continue
		}
		var ok bool
		if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM journey_step_secrets WHERE workspace_id = $1 AND journey_id = $2 AND step = $3)`,
			ws, journeyID, st.ID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errInvalid("step " + st.ID + ": set its key first (PUT .../step-secrets/" + st.ID + ")")
		}
	}
	return nil
}
