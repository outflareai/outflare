package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/fixauto"
)

// Fix autonomy: how far agents may go on their own with the fixes list.
// An agent takes a fix (POST .../fixes/{fix}/start) before it changes the
// site; in a mission, the gate runs that or holds it for a person by the
// workspace's or the site's setting (package fixauto). Changing the
// setting from inside a mission always waits for a person, so an agent
// can't give itself more room.

func (s *Server) routesFixAutonomy(mux *http.ServeMux) {
	read, manage := auth.ScopeSEORead, auth.ScopeSEOManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/fix-autonomy", s.inWorkspace(read, s.getFixAutonomy))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/fix-autonomy", s.inWorkspace(manage, s.putFixAutonomy))
	mux.HandleFunc("GET /v1/workspaces/{ws}/seo-sites/{site}/fix-autonomy", s.inBrand(read, s.getFixAutonomy))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/seo-sites/{site}/fix-autonomy", s.inBrand(manage, s.putFixAutonomy))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/seo-sites/{site}/fix-autonomy", s.inBrand(manage, s.resetFixAutonomy))
	mux.HandleFunc("POST /v1/workspaces/{ws}/fixes/{fix}/start", s.inBrand(manage, s.startFix))
}

type fixAutonomyView struct {
	fixauto.Settings
	SiteID       *string  `json:"site_id"`
	AlwaysPerson []string `json:"always_person"`
	Categories   []string `json:"categories"`
}

// autonomySite is the {site} path value's site, "" for the workspace's
// own setting, or false after writing a 404 or 403.
func (s *Server) autonomySite(w http.ResponseWriter, r *http.Request, a *access, write bool) (string, bool) {
	if r.PathValue("site") == "" {
		return "", true
	}
	site, ok := s.seoSiteFor(w, r, a)
	if !ok {
		return "", false
	}
	if write && !a.writes(site.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return "", false
	}
	return site.ID, true
}

func (s *Server) fixAutonomyView(ctx context.Context, ws, site string) (fixAutonomyView, error) {
	set, err := fixauto.Load(ctx, s.pool, ws, site)
	v := fixAutonomyView{Settings: set, AlwaysPerson: fixauto.AlwaysPersonRules(), Categories: fixauto.Categories()}
	if site != "" {
		v.SiteID = &site
	}
	return v, err
}

func (s *Server) getFixAutonomy(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	site, ok := s.autonomySite(w, r, a, false)
	if !ok {
		return
	}
	v, err := s.fixAutonomyView(r.Context(), a.WorkspaceID, site)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) putFixAutonomy(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Mode           *string   `json:"mode"`
		Allowed        *[]string `json:"allowed"`
		VerifyOnDeploy *bool     `json:"verify_on_deploy"`
	}
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	site, ok := s.autonomySite(w, r, a, true)
	if !ok {
		return
	}
	// A site's first setting starts from what it inherited.
	set, err := fixauto.Load(ctx, s.pool, a.WorkspaceID, site)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if req.Mode != nil {
		set.Mode = *req.Mode
	}
	if req.Allowed != nil {
		set.Allowed = *req.Allowed
		if set.Allowed == nil {
			set.Allowed = []string{}
		}
	}
	if req.VerifyOnDeploy != nil {
		set.VerifyOnDeploy = *req.VerifyOnDeploy
	}
	if err := set.Check(); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	conflict := `(workspace_id) WHERE site_id IS NULL`
	if site != "" {
		conflict = `(site_id) WHERE site_id IS NOT NULL`
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO fix_autonomy (workspace_id, site_id, mode, allowed, verify_on_deploy, updated_by)
			VALUES ($1, NULLIF($2, '')::uuid, $3, $4, $5, $6)
			ON CONFLICT `+conflict+` DO UPDATE SET mode = EXCLUDED.mode, allowed = EXCLUDED.allowed,
				verify_on_deploy = EXCLUDED.verify_on_deploy, updated_by = EXCLUDED.updated_by, updated_at = now()
			RETURNING id::text`, a.WorkspaceID, site, set.Mode, set.Allowed, set.VerifyOnDeploy, a.ActorID).Scan(&id); err != nil {
			return err
		}
		data := map[string]any{"mode": set.Mode, "allowed": len(set.Allowed), "verify_on_deploy": set.VerifyOnDeploy}
		if site != "" {
			data["site_id"] = site
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "fix_autonomy.updated", "fix_autonomy", id, data)
	})
	if s.allocError(w, r, err) {
		return
	}
	v, err := s.fixAutonomyView(ctx, a.WorkspaceID, site)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// resetFixAutonomy puts a site back on the workspace's setting.
func (s *Server) resetFixAutonomy(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	site, ok := s.autonomySite(w, r, a, true)
	if !ok {
		return
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `DELETE FROM fix_autonomy WHERE workspace_id = $1 AND site_id = $2 RETURNING id::text`, a.WorkspaceID, site).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "fix_autonomy.reset", "fix_autonomy", id, map[string]any{"site_id": site})
	})
	if s.allocError(w, r, err) {
		return
	}
	v, err := s.fixAutonomyView(ctx, a.WorkspaceID, site)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// startFix takes a fix: it is in progress, assigned to the caller, and the
// answer carries the prompt to work from. In a mission, the gate has
// already run or held this call by the fix autonomy setting.
func (s *Server) startFix(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Note *string `json:"note"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	fix, ok := s.fixFor(ctx, w, r, a)
	if !ok {
		return
	}
	if !a.writes(fix.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	if req.Note != nil && len(*req.Note) > 2000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "note is at most 2000 characters")
		return
	}
	if fix.Status != "open" && fix.Status != "in_progress" {
		writeError(w, r, http.StatusConflict, "conflict", "the fix is "+fix.Status+"; reopen it first (PATCH .../fixes/{fix} with status open)")
		return
	}
	decision, _, err := fixauto.ForFix(ctx, s.pool, a.WorkspaceID, fix.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE fixes SET status = 'in_progress', assignee_id = $3, note = COALESCE($4, note), updated_at = now()
			WHERE id = $1 AND workspace_id = $2`, fix.ID, a.WorkspaceID, a.ActorID, req.Note); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "fix.started", "fix", fix.ID, map[string]any{
			"source": fix.Source, "rule": fix.Rule.ID, "subject_type": fix.Subject.Type, "subject_id": fix.Subject.ID, "autonomy": decision.Reason})
	})
	if s.allocError(w, r, err) {
		return
	}
	v, ok := s.fixFor(ctx, w, r, a)
	if !ok {
		return
	}
	p, err := s.buildFixPrompt(ctx, a.WorkspaceID, v)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"fix": v, "prompt": p, "autonomy": decision})
}

var (
	fixStartPath    = regexp.MustCompile(`^/v1/workspaces/[^/]+/fixes/([0-9a-fA-F-]{36})/start/?$`)
	fixAutonomyPath = regexp.MustCompile(`^/v1/workspaces/[^/]+/(seo-sites/[^/]+/)?fix-autonomy/?$`)
)

// fixHold says whether a write the mission would run waits for a person
// by fix autonomy, and why.
func (s *Server) fixHold(ctx context.Context, ws string, r *http.Request) (string, error) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return "", nil
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	if fixAutonomyPath.MatchString(path) {
		return "changing fix autonomy always needs a person", nil
	}
	m := fixStartPath.FindStringSubmatch(path)
	if m == nil || r.Method != http.MethodPost {
		return "", nil
	}
	d, ok, err := fixauto.ForFix(ctx, s.pool, ws, m[1])
	if err != nil || !ok || d.Run {
		return "", err
	}
	return map[string]string{
		"always_person": "this fix changes redirects, canonicals, indexing or URLs, so a person always approves it",
		"not_allowed":   "fix autonomy is rules, and this fix's rule isn't allowed to run on its own",
		"copilot":       "fix autonomy is copilot, so every fix waits for a person",
	}[d.Reason], nil
}
