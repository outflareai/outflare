package api

import (
	"net/http"
	"time"
)

// whoami tells a caller who it is: the person signed in, or the API token
// and what it reaches. A token alone is enough to find its workspace, so
// clients (the CLI, MCP, scripts) need nothing else to get started.
func (s *Server) whoami(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticated(w, r)
	if !ok {
		return
	}
	noteAccess(r, routeAccess{Authenticated: true})
	ctx := r.Context()
	type brandRef struct {
		ID     string `json:"id"`
		Handle string `json:"handle"`
		Name   string `json:"name"`
	}
	type wsRef struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
		Name string `json:"name"`
		Test bool   `json:"test"`
	}
	type tokenView struct {
		ID          string     `json:"id"`
		Name        string     `json:"name"`
		WorkspaceID *string    `json:"workspace_id"`
		Workspace   *wsRef     `json:"workspace"`
		BrandID     *string    `json:"brand_id"`
		Brand       *brandRef  `json:"brand"`
		Scopes      []string   `json:"scopes"`
		ExpiresAt   *time.Time `json:"expires_at"`
	}
	type userView struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	out := struct {
		Actor struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"actor"`
		Token *tokenView `json:"token"`
		User  *userView  `json:"user"`
	}{}
	out.Actor.ID, out.Actor.Type = p.ActorID, p.ActorType
	switch {
	case p.TokenID != "":
		t := &tokenView{ID: p.TokenID, WorkspaceID: &p.TokenWorkspaceID, Scopes: p.TokenScopes,
			Workspace: &wsRef{ID: p.TokenWorkspaceID}, BrandID: p.TokenBrandID}
		var handle, name *string
		if err := s.pool.QueryRow(ctx, `SELECT t.name, t.expires_at, w.slug, w.name, w.test, b.handle, b.name FROM api_tokens t JOIN workspaces w ON w.id = t.workspace_id
			LEFT JOIN brands b ON b.id = t.brand_id WHERE t.id = $1`, p.TokenID).Scan(&t.Name, &t.ExpiresAt, &t.Workspace.Slug, &t.Workspace.Name, &t.Workspace.Test, &handle, &name); err != nil {
			s.internal(w, r, err)
			return
		}
		if t.BrandID != nil && handle != nil {
			t.Brand = &brandRef{ID: *t.BrandID, Handle: *handle, Name: *name}
		}
		out.Token = t
	default:
		u := &userView{ID: p.UserID}
		if err := s.pool.QueryRow(ctx, `SELECT email, name FROM users WHERE id = $1`, p.UserID).Scan(&u.Email, &u.Name); err != nil {
			s.internal(w, r, err)
			return
		}
		out.User = u
	}
	writeJSON(w, http.StatusOK, out)
}
