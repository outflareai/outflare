package api

import (
	"errors"
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// crmPreviewLimit is how far each count goes before it stops and says so,
// so the preview answers in seconds even for a large CRM.
const crmPreviewLimit = 5000

// Moving from another CRM: see what a connected CRM holds, then import it
// once into customer records (crm_import_run.go).
func (s *Server) routesCRMImport(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/workspaces/{ws}/integrations/{id}/crm-import-preview", s.inWorkspace(auth.ScopeIntegrationsRead, s.crmImportPreview))
	mux.HandleFunc("POST /v1/workspaces/{ws}/integrations/{id}/crm-imports", s.inWorkspace(auth.ScopeCatalogManage, s.startCRMImport))
	mux.HandleFunc("GET /v1/workspaces/{ws}/crm-imports", s.inWorkspace(auth.ScopeCustomersRead, s.listCRMImports))
	mux.HandleFunc("GET /v1/workspaces/{ws}/crm-imports/{import}", s.inWorkspace(auth.ScopeCustomersRead, s.getCRMImportHandler))
}

// crmImporter opens the {id} connection as a CRM, or writes the error.
func (s *Server) crmImporter(w http.ResponseWriter, r *http.Request) (*connectors.Opened, connectors.CRMImporter, bool) {
	if !s.needIntegrations(w, r) {
		return nil, nil, false
	}
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "integration not found")
		return nil, nil, false
	}
	o, err := s.integrations.Open(r.Context(), s.pool, a.WorkspaceID, id)
	if errors.Is(err, connectors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "integration not found")
		return nil, nil, false
	}
	if err != nil {
		s.internal(w, r, err)
		return nil, nil, false
	}
	imp, ok := o.Connector.(connectors.CRMImporter)
	if !ok || !o.Connector.Definition().Has(connectors.CapImportCRM) {
		writeError(w, r, http.StatusBadRequest, "not_crm_importer", o.Name+" cannot import a CRM")
		return nil, nil, false
	}
	return o, imp, true
}

func (s *Server) crmImportPreview(w http.ResponseWriter, r *http.Request) {
	o, imp, ok := s.crmImporter(w, r)
	if !ok {
		return
	}
	p, err := imp.CRMPreview(r.Context(), o.Client, o.Creds, crmPreviewLimit)
	if err != nil {
		connectorError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Connector string `json:"connector"`
		Limit     int    `json:"limit"`
		connectors.CRMPreview
	}{o.Connector.Definition().ID, crmPreviewLimit, p})
}

func (s *Server) startCRMImport(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Objects []string `json:"objects"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !a.has(auth.ScopeCustomersManage) {
		writeError(w, r, http.StatusForbidden, "forbidden", "importing a CRM adds customer records, so it needs customers.manage as well as catalog.manage")
		return
	}
	if len(req.Objects) > 100 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "at most 100 objects")
		return
	}
	objects := []string{}
	for _, o := range req.Objects {
		if o == "" || len(o) > 100 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "objects are slugs from the preview, such as people or deals")
			return
		}
		if !slices.Contains(objects, o) {
			objects = append(objects, o)
		}
	}
	o, _, ok := s.crmImporter(w, r)
	if !ok {
		return
	}
	var view crmImportView
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		view, err = startCRMImport(r.Context(), tx, a, o.ID, o.Connector.Definition().ID, objects)
		return err
	})
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, view)
}

func (s *Server) listCRMImports(w http.ResponseWriter, r *http.Request) {
	rows, err := s.pool.Query(r.Context(), `SELECT `+crmImportCols+` FROM crm_imports WHERE workspace_id = $1 ORDER BY created_at DESC LIMIT 50`,
		accessFrom(r.Context()).WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (crmImportView, error) { return scanCRMImport(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) getCRMImportHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("import")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "import not found")
		return
	}
	v, err := getCRMImport(r.Context(), s.pool, accessFrom(r.Context()).WorkspaceID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "import not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
