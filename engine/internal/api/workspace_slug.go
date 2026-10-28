package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// A workspace's slug is its name in web app URLs (/{slug}/connectors) and an
// alias for its ID in the API (/v1/workspaces/{slug}/...). The ID stays the
// stable reference: a slug can change.
var slugShape = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$`)

// reservedSlugs are first path segments the server, the web app or the
// hosted Cloud use, or may want later, so no workspace can take them.
// Migration 0053 keeps the same list.
var reservedSlugs = []string{
	"about", "account", "accounts", "admin", "agency", "agencies", "api", "app", "assets", "auth",
	"billing", "blog", "callback", "cloud", "connect", "dashboard", "docs", "download", "emailbison",
	"favicon", "help", "home", "healthz", "ingest", "invite", "invites", "login", "logout", "new",
	"oauth", "outflare", "pricing", "privacy", "readyz", "register", "reset-password", "security",
	"settings", "setup", "sign-in", "sign-up", "signin", "signout", "signup", "static", "status",
	"support", "terms", "user", "users", "verify-email", "workspaces", "www",
}

// slugProblem says why s cannot be a workspace slug, or "" when it can.
func slugProblem(s string) string {
	switch {
	case len(s) < 3 || len(s) > 40:
		return "slug must be 3 to 40 characters"
	case !slugShape.MatchString(s) || validUUID(s):
		return "slug must use only lowercase letters, numbers and dashes, and start and end with a letter or number"
	case slices.Contains(reservedSlugs, s):
		return "slug must not be a reserved word: " + s + " is used by the app"
	}
	return ""
}

// slugFrom makes a slug from a workspace name: "Acme & Co." becomes "acme-co".
func slugFrom(name string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	switch {
	case s == "":
		return "workspace"
	case slugProblem(s) != "":
		// Too short or reserved: keep it recognisable but valid.
		return s + "-workspace"
	}
	return s
}

// workspaceIDFor turns the {ws} path value into a workspace ID: an ID as it
// is, or a slug looked up. It returns "" for anything that is neither.
func (s *Server) workspaceIDFor(ctx context.Context, ws string) (string, error) {
	if validUUID(ws) {
		return ws, nil
	}
	ws = strings.ToLower(ws)
	if !slugShape.MatchString(ws) {
		return "", nil
	}
	// A current slug, or one the workspace had before.
	var id string
	err := s.pool.QueryRow(ctx, `
		SELECT id FROM workspaces WHERE slug = $1
		UNION ALL
		SELECT workspace_id FROM workspace_slug_history WHERE slug = $1
		LIMIT 1`, ws).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// updateWorkspace renames a workspace or changes its slug.
func (s *Server) updateWorkspace(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Name *string `json:"name"`
		Slug *string `json:"slug"`
	}
	if !decode(w, r, &req) {
		return
	}
	changes := map[string]any{}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" || len(name) > 100 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "name must be 1 to 100 characters")
			return
		}
		changes["name"] = name
	}
	if req.Slug != nil {
		slug := strings.ToLower(strings.TrimSpace(*req.Slug))
		if p := slugProblem(slug); p != "" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", p)
			return
		}
		changes["slug"] = slug
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var oldName, oldSlug string
		if err := tx.QueryRow(ctx, `SELECT name, slug FROM workspaces WHERE id = $1 FOR UPDATE`, a.WorkspaceID).Scan(&oldName, &oldSlug); err != nil {
			return err
		}
		if n, ok := changes["name"]; ok && n == oldName {
			delete(changes, "name")
		}
		if sl, ok := changes["slug"]; ok && sl == oldSlug {
			delete(changes, "slug")
		}
		if len(changes) == 0 {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE workspaces SET name = COALESCE($2, name), slug = COALESCE($3, slug) WHERE id = $1`,
			a.WorkspaceID, changes["name"], changes["slug"]); err != nil {
			return err
		}
		if sl, ok := changes["slug"]; ok {
			// The old slug keeps leading here until another workspace takes
			// it; the new one stops leading anywhere else.
			if _, err := tx.Exec(ctx, `DELETE FROM workspace_slug_history WHERE slug = $1`, sl); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO workspace_slug_history (slug, workspace_id) VALUES ($1, $2)
				ON CONFLICT (slug) DO UPDATE SET workspace_id = EXCLUDED.workspace_id, retired_at = now()`, oldSlug, a.WorkspaceID); err != nil {
				return err
			}
		}
		data := map[string]any{}
		if _, ok := changes["name"]; ok {
			data["name"], data["previous_name"] = changes["name"], oldName
		}
		if _, ok := changes["slug"]; ok {
			data["slug"], data["previous_slug"] = changes["slug"], oldSlug
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "workspace.updated", "workspace", a.WorkspaceID, data)
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		writeError(w, r, http.StatusConflict, "slug_taken", "slug is already taken")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.writeWorkspace(w, r, a.WorkspaceID, a.Role, http.StatusOK)
}

// workspaceSlugAvailable answers whether a slug is free, for the settings
// form to check as someone types.
func (s *Server) workspaceSlugAvailable(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	slug := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("slug")))
	if p := slugProblem(slug); p != "" {
		writeJSON(w, http.StatusOK, map[string]any{"slug": slug, "available": false, "reason": p})
		return
	}
	var owner string
	err := s.pool.QueryRow(r.Context(), `SELECT id FROM workspaces WHERE slug = $1`, slug).Scan(&owner)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.internal(w, r, err)
		return
	}
	out := map[string]any{"slug": slug, "available": owner == "" || owner == a.WorkspaceID, "reason": nil}
	if owner != "" && owner != a.WorkspaceID {
		out["reason"] = "slug is already taken"
	}
	writeJSON(w, http.StatusOK, out)
}

// appPath is a web app page in a workspace, such as /acme/mailboxes.
// Old /w/{id}/... links still work, so an ID stands in if the lookup fails.
func (s *Server) appPath(ctx context.Context, workspaceID, page string) string {
	var slug string
	if err := s.pool.QueryRow(ctx, `SELECT slug FROM workspaces WHERE id = $1`, workspaceID).Scan(&slug); err != nil {
		return "/w/" + workspaceID + "/" + page
	}
	return "/" + slug + "/" + page
}

// brandAppPath is a page's path in the web app on one brand
// (/acme/repocket/seo), or on none in particular when brandID is "".
func (s *Server) brandAppPath(ctx context.Context, workspaceID, brandID, page string) string {
	var handle string
	if brandID != "" {
		_ = s.pool.QueryRow(ctx, `SELECT handle FROM brands WHERE workspace_id = $1 AND id = $2`, workspaceID, brandID).Scan(&handle)
	}
	if handle == "" {
		return s.appPath(ctx, workspaceID, page)
	}
	return s.appPath(ctx, workspaceID, handle+"/"+page)
}
