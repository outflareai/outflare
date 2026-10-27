package api

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/imports"
)

// Switching from another sequencer: preview what a connected Instantly or
// Smartlead account holds, then import it. Imported campaigns are drafts,
// so nothing sends until someone starts them.
func (s *Server) routesImports(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/workspaces/{ws}/integrations/{id}/import-preview", s.inWorkspace(auth.ScopeIntegrationsRead, s.importPreview))
	mux.HandleFunc("POST /v1/workspaces/{ws}/integrations/{id}/imports", s.inWorkspace(auth.ScopeSequencesManage, s.startImport))
	mux.HandleFunc("GET /v1/workspaces/{ws}/imports", s.inWorkspace(auth.ScopeSequencesRead, s.listImports))
	mux.HandleFunc("GET /v1/workspaces/{ws}/imports/{id}", s.inWorkspace(auth.ScopeSequencesRead, s.getImport))
}

// importTarget checks the path's integration can import, writing the error if not.
func (s *Server) importTarget(w http.ResponseWriter, r *http.Request) (connectors.Integration, bool) {
	if !s.needIntegrations(w, r) || s.imports == nil {
		if s.integrations != nil {
			writeError(w, r, http.StatusServiceUnavailable, "unavailable", "imports are not configured on this server")
		}
		return connectors.Integration{}, false
	}
	a := accessFrom(r.Context())
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
	if d, ok := s.integrations.Registry.Definition(i.Connector); !ok || !d.Has(connectors.CapImportCampaigns) {
		writeError(w, r, http.StatusBadRequest, "not_importer", i.Name+" cannot import campaigns")
		return i, false
	}
	return i, true
}

func (s *Server) importPreview(w http.ResponseWriter, r *http.Request) {
	i, ok := s.importTarget(w, r)
	if !ok {
		return
	}
	p, err := s.imports.Preview(r.Context(), accessFrom(r.Context()).WorkspaceID, i.ID)
	if err != nil {
		if connectors.KindOf(err) != "" {
			connectorError(w, r, err)
		} else {
			s.internal(w, r, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) startImport(w http.ResponseWriter, r *http.Request) {
	i, ok := s.importTarget(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	var req struct {
		CampaignIDs []string `json:"campaign_ids"`
		Leads       *bool    `json:"leads"`
		Mailboxes   *bool    `json:"mailboxes"`
	}
	if !decode(w, r, &req) {
		return
	}
	opts := imports.Options{CampaignIDs: req.CampaignIDs, Leads: req.Leads == nil || *req.Leads, Mailboxes: req.Mailboxes == nil || *req.Mailboxes}
	if opts.Leads && !a.has(auth.ScopeLeadsManage) {
		writeError(w, r, http.StatusForbidden, "forbidden", "importing leads needs the leads.manage scope; send leads: false to import campaigns only")
		return
	}
	var run imports.Run
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		if run, err = imports.Start(r.Context(), tx, a.WorkspaceID, i.ID, i.Connector, a.ActorID, a.ActorType, opts); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "import.started", "import_run", run.ID,
			map[string]any{"integration_id": i.ID, "connector": i.Connector, "campaigns": len(opts.CampaignIDs), "leads": opts.Leads})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

func (s *Server) listImports(w http.ResponseWriter, r *http.Request) {
	list, err := imports.List(r.Context(), s.pool, accessFrom(r.Context()).WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) getImport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "import not found")
		return
	}
	run, err := imports.Get(r.Context(), s.pool, accessFrom(r.Context()).WorkspaceID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "import not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}
