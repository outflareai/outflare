package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/deploys"
)

// Deploys: when a website changed. A workspace watches a repository or
// project on a code or deploy connection (GitHub, Vercel), tied to the SEO
// site it serves, and Outflare records each production deploy (package
// deploys). Fix re-checks and experiments start from the deploy.

func (s *Server) routesDeploys(mux *http.ServeMux) {
	read, manage := auth.ScopeIntegrationsRead, auth.ScopeIntegrationsManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/integrations/{id}/deploy-options", s.inWorkspace(read, s.listDeployOptions))
	mux.HandleFunc("GET /v1/workspaces/{ws}/deploy-targets", s.inBrand(read, s.listDeployTargets))
	mux.HandleFunc("POST /v1/workspaces/{ws}/deploy-targets", s.inBrand(manage, s.createDeployTarget))
	mux.HandleFunc("GET /v1/workspaces/{ws}/deploy-targets/{target}", s.inBrand(read, s.getDeployTarget))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/deploy-targets/{target}", s.inBrand(manage, s.updateDeployTarget))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/deploy-targets/{target}", s.inBrand(manage, s.deleteDeployTarget))
	mux.HandleFunc("POST /v1/workspaces/{ws}/deploy-targets/{target}/check", s.inBrand(manage, s.checkDeployTarget))
	mux.HandleFunc("GET /v1/workspaces/{ws}/deploys", s.inBrand(read, s.listDeploys))
}

type deployTargetView struct {
	ID            string     `json:"id"`
	IntegrationID string     `json:"integration_id"`
	Connector     string     `json:"connector"`
	Target        string     `json:"target"`
	Name          string     `json:"name"`
	URL           string     `json:"url"`
	SiteID        *string    `json:"site_id"`
	SiteHost      *string    `json:"site_host"`
	BrandID       *string    `json:"brand_id"`
	Status        string     `json:"status"`
	LastCheckedAt *time.Time `json:"last_checked_at"`
	LastError     *string    `json:"last_error"`
	LastDeployAt  *time.Time `json:"last_deploy_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

const deployTargetCols = `t.id::text, t.integration_id::text, i.connector, t.target, t.name, t.url, t.site_id::text, s.host, t.brand_id::text,
	t.status, t.last_checked_at, t.last_error, t.last_deploy_at, t.created_at, t.updated_at`

const deployTargetFrom = ` FROM deploy_targets t JOIN integrations i ON i.id = t.integration_id LEFT JOIN seo_sites s ON s.id = t.site_id`

func scanDeployTarget(row pgx.Row) (deployTargetView, error) {
	var v deployTargetView
	err := row.Scan(&v.ID, &v.IntegrationID, &v.Connector, &v.Target, &v.Name, &v.URL, &v.SiteID, &v.SiteHost, &v.BrandID,
		&v.Status, &v.LastCheckedAt, &v.LastError, &v.LastDeployAt, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

// deployReader opens the {id} integration as a deploy connection.
func (s *Server) deployReader(w http.ResponseWriter, r *http.Request, id string) (*connectors.Opened, connectors.DeployReader, bool) {
	if !s.needIntegrations(w, r) {
		return nil, nil, false
	}
	a := accessFrom(r.Context())
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
		if connectors.KindOf(err) != connectors.ErrUnavailable {
			connectorError(w, r, err)
		} else {
			s.internal(w, r, err)
		}
		return nil, nil, false
	}
	reader, ok := o.Connector.(connectors.DeployReader)
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", o.Name+" can't report deploys; connect GitHub or Vercel")
		return nil, nil, false
	}
	return o, reader, true
}

func (s *Server) listDeployOptions(w http.ResponseWriter, r *http.Request) {
	o, reader, ok := s.deployReader(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	list, err := reader.DeployTargets(r.Context(), s.integrations.Client, o.Creds)
	if err != nil {
		s.analyticsFailed(w, r, o, err)
		return
	}
	// Which are watched already.
	rows, err := s.pool.Query(r.Context(), `SELECT target, id::text FROM deploy_targets WHERE integration_id = $1 AND workspace_id = $2`, o.ID, o.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	watched := map[string]string{}
	for rows.Next() {
		var t, id string
		if err := rows.Scan(&t, &id); err != nil {
			rows.Close()
			s.internal(w, r, err)
			return
		}
		watched[t] = id
	}
	rows.Close()
	type option struct {
		connectors.DeployTarget
		Domains   []string `json:"domains"`
		WatchedBy *string  `json:"watched_by"`
	}
	out := make([]option, 0, len(list))
	for _, t := range list {
		opt := option{DeployTarget: t, Domains: t.Domains}
		if opt.Domains == nil {
			opt.Domains = []string{}
		}
		if id, ok := watched[t.ID]; ok {
			opt.WatchedBy = &id
		}
		out = append(out, opt)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (s *Server) listDeployTargets(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	site := q.Get("site_id")
	if site != "" && !validUUID(site) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "site_id is an id")
		return
	}
	args := []any{a.WorkspaceID, site}
	rows, err := s.pool.Query(r.Context(), `SELECT `+deployTargetCols+deployTargetFrom+`
		WHERE t.workspace_id = $1 AND ($2 = '' OR t.site_id = NULLIF($2, '')::uuid)`+sel.sql("t.brand_id", &args)+`
		ORDER BY t.name, t.target`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (deployTargetView, error) { return scanDeployTarget(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []deployTargetView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) loadDeployTarget(ctx context.Context, ws, id string) (deployTargetView, error) {
	return scanDeployTarget(s.pool.QueryRow(ctx, `SELECT `+deployTargetCols+deployTargetFrom+` WHERE t.id = $1 AND t.workspace_id = $2`, id, ws))
}

// deployTargetFor loads the {target} path value's target the caller may
// see, or writes a 404.
func (s *Server) deployTargetFor(w http.ResponseWriter, r *http.Request, a *access) (*deployTargetView, bool) {
	id := r.PathValue("target")
	if validUUID(id) {
		v, err := s.loadDeployTarget(r.Context(), a.WorkspaceID, id)
		if err == nil && a.sees(nil, v.BrandID) {
			return &v, true
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.internal(w, r, err)
			return nil, false
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "deploy target not found")
	return nil, false
}

func (s *Server) getDeployTarget(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.deployTargetFor(w, r, accessFrom(r.Context())); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// siteBrand checks a site id the caller may write and returns its brand.
func (s *Server) siteBrand(ctx context.Context, a *access, site string) (*string, error) {
	if !validUUID(site) {
		return nil, errInvalid("site_id is an SEO site's id")
	}
	var brand *string
	err := s.pool.QueryRow(ctx, `SELECT brand_id::text FROM seo_sites WHERE id = $1 AND workspace_id = $2`, site, a.WorkspaceID).Scan(&brand)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.sees(nil, brand)) {
		return nil, errInvalid("site_id is an SEO site in this workspace; GET .../seo-sites lists them")
	}
	if err == nil && !a.writes(nil, brand) {
		return nil, errSharedRecord
	}
	return brand, err
}

// matchSite finds the site a target serves from its domains.
func (s *Server) matchSite(ctx context.Context, a *access, domains []string) (*string, *string, error) {
	if len(domains) == 0 {
		return nil, nil, nil
	}
	hosts := make([]string, 0, len(domains))
	for _, d := range domains {
		hosts = append(hosts, strings.TrimPrefix(strings.ToLower(d), "www."))
	}
	var id, brand *string
	err := s.pool.QueryRow(ctx, `SELECT id::text, brand_id::text FROM seo_sites WHERE workspace_id = $1 AND regexp_replace(host, '^www\.', '') = ANY($2)
		ORDER BY created_at LIMIT 1`, a.WorkspaceID, hosts).Scan(&id, &brand)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.writes(nil, brand)) {
		return nil, nil, nil
	}
	return id, brand, err
}

func (s *Server) createDeployTarget(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		IntegrationID string  `json:"integration_id"`
		Target        string  `json:"target"`
		SiteID        *string `json:"site_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Target = strings.TrimSpace(req.Target)
	if req.Target == "" || len(req.Target) > 200 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "target is a repository (owner/name) or project id from GET .../integrations/{id}/deploy-options")
		return
	}
	ctx := r.Context()
	o, reader, ok := s.deployReader(w, r, req.IntegrationID)
	if !ok {
		return
	}
	// The target must be one the connection can see, which also gives its
	// name and the domains it serves.
	options, err := reader.DeployTargets(ctx, s.integrations.Client, o.Creds)
	if err != nil {
		s.analyticsFailed(w, r, o, err)
		return
	}
	i := slices.IndexFunc(options, func(t connectors.DeployTarget) bool { return t.ID == req.Target })
	if i < 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", o.Name+" can't see "+req.Target+"; GET .../integrations/{id}/deploy-options lists what it can watch")
		return
	}
	opt := options[i]
	var site, brand *string
	if req.SiteID != nil {
		site = req.SiteID
		if brand, err = s.siteBrand(ctx, a, *site); s.allocError(w, r, err) {
			return
		}
	} else if site, brand, err = s.matchSite(ctx, a, opt.Domains); err != nil {
		s.internal(w, r, err)
		return
	}
	if !a.writes(nil, brand) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	var id string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO deploy_targets (workspace_id, integration_id, target, name, url, site_id, brand_id, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT (integration_id, target) DO NOTHING RETURNING id::text`,
			a.WorkspaceID, o.ID, opt.ID, clip(opt.Name, 200), clip(opt.URL, 1000), site, brand, a.ActorID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errConflict
		}
		if err != nil {
			return err
		}
		if _, err := deploys.Enqueue(ctx, tx, a.WorkspaceID, id, ""); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "deploy_target.added", "deploy_target", id,
			map[string]any{"integration_id": o.ID, "connector": o.Connector.Definition().ID, "site_id": site, "brand_id": brand})
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, http.StatusConflict, "conflict", opt.ID+" is watched already on this connection")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	v, err := s.loadDeployTarget(ctx, a.WorkspaceID, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) updateDeployTarget(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		SiteID optional[string] `json:"site_id"`
		Status *string          `json:"status"`
	}
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	t, ok := s.deployTargetFor(w, r, a)
	if !ok {
		return
	}
	if !a.writes(nil, t.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	if req.Status != nil && !slices.Contains(siteStatuses, *req.Status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is active or paused")
		return
	}
	site, brand := t.SiteID, t.BrandID
	if req.SiteID.Set {
		site, brand = req.SiteID.Value, nil
		if site != nil {
			var err error
			if brand, err = s.siteBrand(ctx, a, *site); s.allocError(w, r, err) {
				return
			}
		}
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE deploy_targets SET site_id = $3, brand_id = $4, status = COALESCE($5, status), updated_at = now()
			WHERE id = $1 AND workspace_id = $2`, t.ID, a.WorkspaceID, site, brand, req.Status); err != nil {
			return err
		}
		data := map[string]any{"site_id": site, "brand_id": brand}
		if req.Status != nil {
			data["status"] = *req.Status
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "deploy_target.updated", "deploy_target", t.ID, data)
	})
	if s.allocError(w, r, err) {
		return
	}
	v, err := s.loadDeployTarget(ctx, a.WorkspaceID, t.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) deleteDeployTarget(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	t, ok := s.deployTargetFor(w, r, a)
	if !ok {
		return
	}
	if !a.writes(nil, t.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM deploy_targets WHERE id = $1 AND workspace_id = $2`, t.ID, a.WorkspaceID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "deploy_target.removed", "deploy_target", t.ID,
			map[string]any{"integration_id": t.IntegrationID, "site_id": t.SiteID, "brand_id": t.BrandID})
	})
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) checkDeployTarget(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	t, ok := s.deployTargetFor(w, r, a)
	if !ok {
		return
	}
	if !a.writes(nil, t.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := deploys.Enqueue(ctx, tx, a.WorkspaceID, t.ID, ""); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "deploy_target.check_requested", "deploy_target", t.ID,
			map[string]any{"brand_id": t.BrandID})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

func (s *Server) listDeploys(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	target, site, state := q.Get("target_id"), q.Get("site_id"), q.Get("state")
	for name, v := range map[string]string{"target_id": target, "site_id": site} {
		if v != "" && !validUUID(v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", name+" is an id")
			return
		}
	}
	if state != "" && !slices.Contains([]string{connectors.DeployReady, connectors.DeployFailed, connectors.DeployBuilding}, state) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "state is ready, failed or building")
		return
	}
	args := []any{a.WorkspaceID, target, site, state}
	rows, err := s.pool.Query(r.Context(), `
		SELECT d.id::text, d.target_id::text, d.site_id::text, d.brand_id::text, d.external_id, d.kind, d.state, d.commit_sha, d.ref,
		       d.message, d.url, d.deployed_at, d.seen_at
		FROM deploys d
		WHERE d.workspace_id = $1 AND ($2 = '' OR d.target_id = NULLIF($2, '')::uuid) AND ($3 = '' OR d.site_id = NULLIF($3, '')::uuid)
		  AND ($4 = '' OR d.state = $4)`+sel.sql("d.brand_id", &args)+`
		ORDER BY d.deployed_at DESC LIMIT 100`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (deploys.Deploy, error) {
		var d deploys.Deploy
		err := row.Scan(&d.ID, &d.TargetID, &d.SiteID, &d.BrandID, &d.ExternalID, &d.Kind, &d.State, &d.Commit, &d.Ref,
			&d.Message, &d.URL, &d.DeployedAt, &d.SeenAt)
		return d, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []deploys.Deploy{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
