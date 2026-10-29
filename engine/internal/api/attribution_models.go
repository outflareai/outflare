package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/attribution"
	"github.com/outflareai/outflare/engine/internal/auth"
)

// A workspace's own attribution models, on top of the five built in. Each
// edit is a new version, so reports and modules can say which one they
// used; a module that must never move (an affiliate program) pins one
// (attribution.Pin).

func (s *Server) routesAttributionModels(mux *http.ServeMux) {
	read, manage := auth.ScopeAnalyticsRead, auth.ScopeAnalyticsManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/attribution/models", s.inWorkspace(read, s.listAttributionModels))
	mux.HandleFunc("POST /v1/workspaces/{ws}/attribution/models", s.inWorkspace(manage, s.createAttributionModel))
	mux.HandleFunc("GET /v1/workspaces/{ws}/attribution/models/{key}", s.inWorkspace(read, s.getAttributionModel))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/attribution/models/{key}", s.inWorkspace(manage, s.updateAttributionModel))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/attribution/models/{key}", s.inWorkspace(manage, s.archiveAttributionModel))
}

type builtInModelView struct {
	ID   string           `json:"id"`
	Spec attribution.Spec `json:"spec"`
}

func (s *Server) listAttributionModels(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	list, err := attribution.LatestModels(r.Context(), s.pool, a.WorkspaceID, r.URL.Query().Get("archived") == "true")
	if err != nil {
		s.internal(w, r, err)
		return
	}
	builtIn := make([]builtInModelView, 0, len(attribution.Models))
	for _, m := range attribution.Models {
		builtIn = append(builtIn, builtInModelView{ID: m, Spec: attribution.Spec{Base: m}})
	}
	writeJSON(w, http.StatusOK, map[string]any{"built_in": builtIn, "data": list})
}

func (s *Server) getAttributionModel(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	versions, err := attribution.Versions(r.Context(), s.pool, a.WorkspaceID, r.PathValue("key"))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if len(versions) == 0 {
		writeError(w, r, http.StatusNotFound, "not_found", "no model with that key")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": versions})
}

type attributionModelInput struct {
	Key  string            `json:"key"`
	Name *string           `json:"name"`
	Spec *attribution.Spec `json:"spec"`
}

// checkModelInput validates a name and a spec, writing the error.
func checkModelInput(w http.ResponseWriter, r *http.Request, in attributionModelInput) bool {
	if in.Name != nil {
		if n := strings.TrimSpace(*in.Name); n == "" || len([]rune(n)) > 80 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "name is 1 to 80 characters")
			return false
		}
	}
	if in.Spec != nil {
		if err := in.Spec.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "spec: "+err.Error())
			return false
		}
	}
	return true
}

// saveAttributionModel stores a model's next version and credits every
// conversion again, so its credits cover the past too.
func (s *Server) saveAttributionModel(ctx context.Context, a *access, key, name string, spec attribution.Spec, action string) (attribution.Model, error) {
	if _, err := s.loadAttributionSettings(ctx, a.WorkspaceID); err != nil {
		return attribution.Model{}, err
	}
	var m attribution.Model
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if m, err = attribution.SaveVersion(ctx, tx, a.WorkspaceID, key, name, spec, nullIfEmpty(a.ActorID)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE attribution_settings SET backfilled_at = NULL, updated_at = now() WHERE workspace_id = $1`, a.WorkspaceID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "attribution_model", m.ID,
			map[string]any{"version": m.Version, "base": spec.Base, "excluded_channels": len(spec.ExcludeChannels), "channel_windows": len(spec.ChannelLookbackDays)})
	})
	return m, err
}

func isUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

func (s *Server) createAttributionModel(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in attributionModelInput
	if !decode(w, r, &in) || !checkModelInput(w, r, in) {
		return
	}
	if !attribution.KeyRE.MatchString(in.Key) || slices.Contains(attribution.Models, in.Key) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "key is a lowercase letter, then up to 39 letters, digits or hyphens, and not a built-in model's id")
		return
	}
	if in.Name == nil || in.Spec == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name and spec are required")
		return
	}
	var exists bool
	if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM attribution_models WHERE workspace_id = $1 AND key = $2)`, a.WorkspaceID, in.Key).Scan(&exists); err != nil {
		s.internal(w, r, err)
		return
	}
	if exists {
		writeError(w, r, http.StatusConflict, "conflict", "a model with that key exists: change it with PATCH .../attribution/models/"+in.Key)
		return
	}
	m, err := s.saveAttributionModel(r.Context(), a, in.Key, strings.TrimSpace(*in.Name), *in.Spec, "attribution.model_created")
	if isUniqueViolation(err) {
		writeError(w, r, http.StatusConflict, "conflict", "a model with that key was just made")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) updateAttributionModel(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in attributionModelInput
	if !decode(w, r, &in) || !checkModelInput(w, r, in) {
		return
	}
	key := r.PathValue("key")
	versions, err := attribution.Versions(r.Context(), s.pool, a.WorkspaceID, key)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if len(versions) == 0 {
		writeError(w, r, http.StatusNotFound, "not_found", "no model with that key")
		return
	}
	cur := versions[0]
	if cur.ArchivedAt != nil {
		writeError(w, r, http.StatusConflict, "conflict", "the model is archived")
		return
	}
	name := cur.Name
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
	}
	if in.Spec == nil || specEqual(*in.Spec, cur.Spec) {
		// A new name alone keeps the version: every version takes it.
		if name != cur.Name {
			err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
				if _, err := tx.Exec(r.Context(), `UPDATE attribution_models SET name = $3 WHERE workspace_id = $1 AND key = $2`, a.WorkspaceID, key, name); err != nil {
					return err
				}
				return record(r.Context(), tx, &a.principal, a.WorkspaceID, "attribution.model_renamed", "attribution_model", cur.ID, map[string]any{"version": cur.Version})
			})
			if err != nil {
				s.internal(w, r, err)
				return
			}
			cur.Name = name
		}
		writeJSON(w, http.StatusOK, cur)
		return
	}
	m, err := s.saveAttributionModel(r.Context(), a, key, name, *in.Spec, "attribution.model_updated")
	if isUniqueViolation(err) {
		writeError(w, r, http.StatusConflict, "conflict", "the model was just changed: read it again")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if name != cur.Name {
		if _, err := s.pool.Exec(r.Context(), `UPDATE attribution_models SET name = $3 WHERE workspace_id = $1 AND key = $2`, a.WorkspaceID, key, name); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, m)
}

func specEqual(a, b attribution.Spec) bool {
	return string(mustJSON(a)) == string(mustJSON(b))
}

func (s *Server) archiveAttributionModel(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	key := r.PathValue("key")
	settings, err := s.loadAttributionSettings(r.Context(), a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if settings.Model == key {
		writeError(w, r, http.StatusConflict, "conflict", "it is the workspace's default model: pick another in PATCH .../attribution/settings first")
		return
	}
	var id string
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(r.Context(), `UPDATE attribution_models SET archived_at = now() WHERE workspace_id = $1 AND key = $2 AND archived_at IS NULL
			RETURNING id::text`, a.WorkspaceID, key).Scan(&id)
		if err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "attribution.model_archived", "attribution_model", id, nil)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "no model with that key that isn't archived")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
