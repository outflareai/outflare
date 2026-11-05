package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/conversions"
)

// Conversion definitions: what a workspace counts as a conversion, on the
// customer events stream (engine/internal/conversions). Attribution
// credits channels with them; ad send-back and partner programs read them
// too. Every change credits conversions again.

func (s *Server) routesConversionDefinitions(mux *http.ServeMux) {
	read, manage := auth.ScopeAnalyticsRead, auth.ScopeAnalyticsManage
	for _, t := range s.twins("conversion-definitions") {
		mux.HandleFunc("GET "+t.path, t.on(s.inWorkspace)(read, s.listConversionDefinitions))
		mux.HandleFunc("POST "+t.path, t.on(s.inWorkspace)(manage, s.createConversionDefinition))
		mux.HandleFunc("GET "+t.path+"/presets", t.on(s.inWorkspace)(read, s.listConversionPresets))
		mux.HandleFunc("POST "+t.path+"/presets/{preset}", t.on(s.inWorkspace)(manage, s.addConversionPreset))
		mux.HandleFunc("GET "+t.path+"/{key}", t.on(s.inWorkspace)(read, s.getConversionDefinition))
		mux.HandleFunc("PATCH "+t.path+"/{key}", t.on(s.inWorkspace)(manage, s.updateConversionDefinition))
		mux.HandleFunc("DELETE "+t.path+"/{key}", t.on(s.inWorkspace)(manage, s.archiveConversionDefinition))
	}
}

// brandDefinition is the route's brand's definition with key (on a
// workspace route, the first brand's, else another brand's the caller
// sees): one in another brand is ErrNotFound, as if it didn't exist.
func (s *Server) brandDefinition(ctx context.Context, a *access, key string) (conversions.Definition, error) {
	d, err := conversions.Get(ctx, s.pool, a.WorkspaceID, a.Brand, key)
	if err == nil && !a.sees(nullIfEmpty(d.BrandID)) {
		return conversions.Definition{}, conversions.ErrNotFound
	}
	return d, err
}

func (s *Server) listConversionDefinitions(w http.ResponseWriter, r *http.Request) {
	sel, ok := s.dataBrand(w, r)
	if !ok {
		return
	}
	list, err := conversions.List(r.Context(), s.pool, accessFrom(r.Context()).WorkspaceID, groupBrand(sel), r.URL.Query().Get("archived") == "true")
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) listConversionPresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"data": conversions.Presets})
}

func (s *Server) getConversionDefinition(w http.ResponseWriter, r *http.Request) {
	d, err := s.brandDefinition(r.Context(), accessFrom(r.Context()), r.PathValue("key"))
	if errors.Is(err, conversions.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", err.Error())
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// conversionsChanged records a change and credits brand's conversions
// again ("": the workspace's first brand's), in the change's transaction.
func (s *Server) conversionsChanged(ctx context.Context, tx pgx.Tx, a *access, brand, action, id string, data map[string]any) error {
	if _, err := s.loadAttributionSettings(ctx, a.WorkspaceID, brand); err != nil {
		return err
	}
	if err := recreditBrand(ctx, tx, a.WorkspaceID, brand); err != nil {
		return err
	}
	return record(ctx, tx, &a.principal, a.WorkspaceID, action, "conversion_definition", id, data)
}

func definitionProps(d conversions.Definition) map[string]any {
	return map[string]any{"value_mode": d.ValueMode, "filters": len(d.Filters), "networks": len(d.NetworkEvents), "attribution": d.Attribution}
}

func (s *Server) createConversionDefinition(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	in := conversions.Definition{Attribution: true}
	if !decode(w, r, &in) {
		return
	}
	if err := in.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	brand, err := s.rowBrand(r.Context(), a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	in.BrandID = brand
	var out conversions.Definition
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		if out, err = conversions.Create(r.Context(), tx, a.WorkspaceID, in, nullIfEmpty(a.ActorID)); err != nil {
			return err
		}
		return s.conversionsChanged(r.Context(), tx, a, out.BrandID, "conversion_definition.created", out.ID, definitionProps(out))
	})
	if errors.Is(err, conversions.ErrExists) {
		writeError(w, r, http.StatusConflict, "conflict", "a conversion definition with that key exists: change it with PATCH .../conversion-definitions/"+in.Key)
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) updateConversionDefinition(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, err := s.brandDefinition(r.Context(), a, r.PathValue("key"))
	if errors.Is(err, conversions.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", err.Error())
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if cur.ArchivedAt != nil {
		writeError(w, r, http.StatusConflict, "conflict", "the conversion definition is archived")
		return
	}
	// Fields left out keep their values; the key never changes.
	next := cur
	if !decode(w, r, &next) {
		return
	}
	next.Key, next.ID = cur.Key, cur.ID
	if err := next.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var out conversions.Definition
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		if out, err = conversions.Update(r.Context(), tx, a.WorkspaceID, next); err != nil {
			return err
		}
		return s.conversionsChanged(r.Context(), tx, a, out.BrandID, "conversion_definition.updated", out.ID, definitionProps(out))
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) archiveConversionDefinition(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, err := s.brandDefinition(r.Context(), a, r.PathValue("key"))
	if err == nil {
		err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
			d, err := conversions.Archive(r.Context(), tx, a.WorkspaceID, cur.ID)
			if err != nil {
				return err
			}
			return s.conversionsChanged(r.Context(), tx, a, d.BrandID, "conversion_definition.archived", d.ID, nil)
		})
	}
	if errors.Is(err, conversions.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "no conversion definition with that key that isn't archived")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// addConversionPreset adds a preset's definitions the workspace doesn't
// have yet (by key), so adding one twice changes nothing.
func (s *Server) addConversionPreset(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	p, ok := conversions.PresetOf(r.PathValue("preset"))
	if !ok {
		writeError(w, r, http.StatusNotFound, "not_found", "no such preset: see GET .../conversion-definitions/presets")
		return
	}
	brand, err := s.rowBrand(r.Context(), a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	added, skipped := []conversions.Definition{}, []string{}
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		for _, d := range p.Definitions {
			d.BrandID = brand
			if err := d.Validate(); err != nil {
				return err
			}
			made, ok, err := conversions.CreateIfMissing(r.Context(), tx, a.WorkspaceID, d, nullIfEmpty(a.ActorID))
			if err != nil {
				return err
			}
			if !ok {
				skipped = append(skipped, d.Key)
				continue
			}
			added = append(added, made)
		}
		if len(added) == 0 {
			return nil
		}
		return s.conversionsChanged(r.Context(), tx, a, brand, "conversion_definitions.preset_added", a.WorkspaceID,
			map[string]any{"preset": p.ID, "added": len(added), "skipped": len(skipped)})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": added, "skipped": skipped})
}
