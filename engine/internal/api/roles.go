package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// Workspace roles. Every workspace starts with Owner, Admin, Member
// (stored as operator) and Viewer. Owner is fixed; the others can be
// changed, and a workspace can add its own. Two rules keep this safe:
// nobody gives a permission they don't hold, and nobody changes someone
// who can do more than they can.

func (s *Server) routesRoles(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/workspaces/{ws}/roles", s.inBrand(auth.ScopeMembersRead, s.listRoles))
	mux.HandleFunc("POST /v1/workspaces/{ws}/roles", s.inWorkspace(auth.ScopeRolesManage, s.createRole))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/roles/{role}", s.inWorkspace(auth.ScopeRolesManage, s.updateRole))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/roles/{role}", s.inWorkspace(auth.ScopeRolesManage, s.deleteRole))
}

// builtinRoles in display order, with their names and one-line promises.
var builtinRoles = []struct{ key, name, description string }{
	{auth.RoleOwner, "Owner", "Everything, including removing people and managing owners."},
	{auth.RoleAdmin, "Admin", "Everything except removing people or reducing their access."},
	{auth.RoleOperator, "Member", "All the marketing work, and connecting tools. No settings, people, tokens or audit log."},
	{auth.RoleViewer, "Viewer", "Sees everything, changes nothing."},
}

type roleView struct {
	// The starting role's key (owner, admin, operator, viewer), or the id
	// of a role the workspace made.
	ID string `json:"id"`
	// The starting role this is, or null for a workspace's own role.
	Builtin     *string  `json:"builtin"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Scopes      []string `json:"scopes"`
	// Owner can't be changed.
	Locked bool `json:"locked"`
	// A starting role the workspace changed from its defaults.
	Edited bool `json:"edited"`
	// Whether people on this role can be kept to some brands: only roles
	// without workspace-wide permissions.
	BrandLimitable bool `json:"brand_limitable"`
	Members        int  `json:"members"`
	Invites        int  `json:"invites"`
	// The starting role stored beside a custom role, which older versions
	// read; the role's own key for a starting role.
	base string
}

// effectiveScopes is what a role allows: everything for owners, the stored
// list for a changed or custom role, otherwise the starting role's
// defaults.
func effectiveScopes(role string, stored []string) []string {
	if role == auth.RoleOwner {
		return auth.AllScopes
	}
	if stored != nil {
		return auth.CleanScopes(stored)
	}
	return auth.RoleScopes(role)
}

// baseRole is the starting role stored beside a custom role: the
// strongest whose defaults it fully includes, so a version without custom
// roles gives its people no more than the role does where it can.
func baseRole(scopes []string) string {
	for _, k := range []string{auth.RoleAdmin, auth.RoleOperator, auth.RoleViewer} {
		if auth.Within(auth.RoleScopes(k), scopes) {
			return k
		}
	}
	return auth.RoleViewer
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// loadRoles lists a workspace's roles: the four starting ones (with the
// workspace's changes), then its own, oldest first, each with how many
// people and pending invites use it.
func loadRoles(ctx context.Context, q querier, ws string) ([]roleView, error) {
	rows, err := q.Query(ctx, `
		SELECT r.id::text, r.builtin, r.name, r.description, r.scopes,
			(SELECT count(*)::int FROM memberships m WHERE m.workspace_id = r.workspace_id AND (m.role_id = r.id OR (r.builtin IS NOT NULL AND m.role_id IS NULL AND m.role = r.builtin))),
			(SELECT count(*)::int FROM invites i WHERE i.workspace_id = r.workspace_id AND i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > now()
				AND (i.role_id = r.id OR (r.builtin IS NOT NULL AND i.role_id IS NULL AND i.role = r.builtin)))
		FROM workspace_roles r WHERE r.workspace_id = $1 ORDER BY r.created_at, r.id`, ws)
	if err != nil {
		return nil, err
	}
	type row struct {
		id               string
		builtin          *string
		name, desc       string
		scopes           []string
		members, invites int
	}
	stored, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var v row
		err := r.Scan(&v.id, &v.builtin, &v.name, &v.desc, &v.scopes, &v.members, &v.invites)
		return v, err
	})
	if err != nil {
		return nil, err
	}
	// Starting roles without a row of their own: count their people here.
	counts := map[string][2]int{}
	crow, err := q.Query(ctx, `
		SELECT role, count(*)::int, 0 FROM memberships WHERE workspace_id = $1 AND role_id IS NULL GROUP BY role
		UNION ALL
		SELECT role, 0, count(*)::int FROM invites WHERE workspace_id = $1 AND role_id IS NULL AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now() GROUP BY role`, ws)
	if err != nil {
		return nil, err
	}
	for crow.Next() {
		var k string
		var m, i int
		if err := crow.Scan(&k, &m, &i); err != nil {
			crow.Close()
			return nil, err
		}
		c := counts[k]
		counts[k] = [2]int{c[0] + m, c[1] + i}
	}
	if err := crow.Err(); err != nil {
		return nil, err
	}
	out := []roleView{}
	for _, b := range builtinRoles {
		key := b.key
		v := roleView{ID: key, Builtin: &key, Name: b.name, Description: b.description, Scopes: effectiveScopes(key, nil),
			Locked: key == auth.RoleOwner, Members: counts[key][0], Invites: counts[key][1], base: key}
		if i := slices.IndexFunc(stored, func(r row) bool { return r.builtin != nil && *r.builtin == key }); i >= 0 {
			r := stored[i]
			v.Name, v.Description, v.Scopes, v.Edited = r.name, r.desc, effectiveScopes(key, r.scopes), true
			v.Members, v.Invites = r.members, r.invites
		}
		v.BrandLimitable = auth.BrandLimitable(v.Scopes)
		out = append(out, v)
	}
	for _, r := range stored {
		if r.builtin != nil {
			continue
		}
		sc := auth.CleanScopes(r.scopes)
		out = append(out, roleView{ID: r.id, Name: r.name, Description: r.desc, Scopes: sc, BrandLimitable: auth.BrandLimitable(sc),
			Members: r.members, Invites: r.invites, base: baseRole(sc)})
	}
	return out, nil
}

// findRole picks a role by its id, its starting key, or "member" for the
// operator role.
func findRole(list []roleView, ref string) (roleView, bool) {
	ref = strings.TrimSpace(ref)
	if strings.EqualFold(ref, "member") {
		ref = auth.RoleOperator
	}
	for _, v := range list {
		if v.ID == ref {
			return v, true
		}
	}
	return roleView{}, false
}

// roleOf is the role a member or invite holds, from its stored starting
// role and custom role id.
func roleOf(list []roleView, base string, roleID *string) roleView {
	if roleID != nil {
		if v, ok := findRole(list, *roleID); ok {
			return v
		}
	}
	v, _ := findRole(list, base)
	return v
}

func (s *Server) listRoles(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	list, err := loadRoles(r.Context(), s.pool, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

type roleRequest struct {
	Name        *string   `json:"name"`
	Description *string   `json:"description"`
	Scopes      *[]string `json:"scopes"`
}

// checkRoleRequest validates a name, description and scopes, and that the
// caller holds every scope they give.
func checkRoleRequest(a *access, list []roleView, self string, req roleRequest) error {
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		if n == "" || len([]rune(n)) > 60 {
			return errInvalid("name must be 1 to 60 characters")
		}
		if slices.ContainsFunc(list, func(v roleView) bool { return v.ID != self && strings.EqualFold(v.Name, n) }) {
			return errRoleName
		}
		*req.Name = n
	}
	if req.Description != nil {
		d := strings.TrimSpace(*req.Description)
		if len([]rune(d)) > 300 {
			return errInvalid("description must be at most 300 characters")
		}
		*req.Description = d
	}
	if req.Scopes != nil {
		*req.Scopes = auth.Rename(*req.Scopes)
		for _, sc := range *req.Scopes {
			if !auth.ValidScope(sc) {
				return errInvalid("unknown scope " + truncate(sc, 60))
			}
			if !slices.Contains(a.Scopes, sc) {
				return errDenied("you can't give a permission you don't have yourself: " + sc)
			}
		}
		*req.Scopes = auth.CleanScopes(*req.Scopes)
	}
	return nil
}

var errRoleName = errors.New("role name taken")

func (s *Server) createRole(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req roleRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Name == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	if req.Scopes == nil {
		req.Scopes = &[]string{}
	}
	if req.Description == nil {
		req.Description = new(string)
	}
	ctx := r.Context()
	var v roleView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockRoles(ctx, tx, a.WorkspaceID); err != nil {
			return err
		}
		list, err := loadRoles(ctx, tx, a.WorkspaceID)
		if err != nil {
			return err
		}
		if err := checkRoleRequest(a, list, "", req); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO workspace_roles (workspace_id, name, description, scopes) VALUES ($1, $2, $3, $4) RETURNING id::text`,
			a.WorkspaceID, *req.Name, *req.Description, *req.Scopes).Scan(&v.ID); err != nil {
			return err
		}
		v.Name, v.Description, v.Scopes = *req.Name, *req.Description, *req.Scopes
		v.BrandLimitable = auth.BrandLimitable(v.Scopes)
		return record(ctx, tx, &a.principal, a.WorkspaceID, "role.created", "role", v.ID, map[string]any{"scopes": v.Scopes})
	})
	if s.roleError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// lockRoles serialises role changes in a workspace, so two edits can't
// both pass their checks against the same old state.
func lockRoles(ctx context.Context, tx pgx.Tx, ws string) error {
	_, err := tx.Exec(ctx, `SELECT 1 FROM workspaces WHERE id = $1 FOR UPDATE`, ws)
	return err
}

func (s *Server) updateRole(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req roleRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Name == nil && req.Description == nil && req.Scopes == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send name, description, scopes or more than one")
		return
	}
	ctx := r.Context()
	var out roleView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockRoles(ctx, tx, a.WorkspaceID); err != nil {
			return err
		}
		list, err := loadRoles(ctx, tx, a.WorkspaceID)
		if err != nil {
			return err
		}
		v, ok := findRole(list, r.PathValue("role"))
		if !ok {
			return errNotFound
		}
		if err := s.checkRoleChange(a, v); err != nil {
			return err
		}
		if err := checkRoleRequest(a, list, v.ID, req); err != nil {
			return err
		}
		name, desc, scopes := v.Name, v.Description, v.Scopes
		if req.Name != nil {
			name = *req.Name
		}
		if req.Description != nil {
			desc = *req.Description
		}
		if req.Scopes != nil {
			scopes = *req.Scopes
		}
		if err := s.applyRole(ctx, tx, a, v, name, desc, scopes); err != nil {
			return err
		}
		v.Name, v.Description, v.Scopes, v.BrandLimitable = name, desc, scopes, auth.BrandLimitable(scopes)
		v.Edited = v.Builtin != nil || v.Edited
		out = v
		return nil
	})
	if s.roleError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// checkRoleChange: Owner is fixed, and a role that allows more than the
// caller can't be changed by them.
func (s *Server) checkRoleChange(a *access, v roleView) error {
	if v.Locked {
		return errDenied("the Owner role always has every permission and can't be changed")
	}
	if !auth.Within(v.Scopes, a.Scopes) {
		return errDenied("this role can do things you can't, so only someone who can do all of them can change it")
	}
	return nil
}

// applyRole saves a role's name, description and scopes, updates the API
// tokens its people made, and records the change. A starting role gets
// its own row the first time it changes.
func (s *Server) applyRole(ctx context.Context, tx pgx.Tx, a *access, v roleView, name, desc string, scopes []string) error {
	// Taking a permission off a role takes it from everyone on it, which is
	// the same as lowering each of them: it needs Remove people. Starting
	// roles always count as in use.
	if (v.Builtin != nil || v.Members > 0 || v.Invites > 0) && !auth.Within(v.Scopes, scopes) && !a.has(auth.ScopeMembersRemove) {
		return errDenied("taking permissions off " + v.Name + " takes them from the people on it, which needs the Remove people permission")
	}
	if !auth.BrandLimitable(scopes) {
		var limited int
		if err := tx.QueryRow(ctx, `SELECT count(*)::int FROM memberships WHERE workspace_id = $1 AND cardinality(brand_ids) > 0 AND `+holdsRole(v), a.WorkspaceID, v.ID).Scan(&limited); err != nil {
			return err
		}
		if limited > 0 {
			return errRoleBrands
		}
	}
	var err error
	if v.Builtin != nil {
		_, err = tx.Exec(ctx, `
			INSERT INTO workspace_roles (workspace_id, builtin, name, description, scopes) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (workspace_id, builtin) WHERE builtin IS NOT NULL DO UPDATE SET name = $3, description = $4, scopes = $5, updated_at = now()`,
			a.WorkspaceID, *v.Builtin, name, desc, scopes)
	} else {
		_, err = tx.Exec(ctx, `UPDATE workspace_roles SET name = $3, description = $4, scopes = $5, updated_at = now() WHERE workspace_id = $1 AND id = $2`,
			a.WorkspaceID, v.ID, name, desc, scopes)
	}
	if err != nil {
		return err
	}
	if v.Builtin == nil {
		// Older versions read the starting role stored beside it.
		if _, err := tx.Exec(ctx, `UPDATE memberships SET role = $3 WHERE workspace_id = $1 AND role_id = $2`, a.WorkspaceID, v.ID, baseRole(scopes)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE invites SET role = $3 WHERE workspace_id = $1 AND role_id = $2`, a.WorkspaceID, v.ID, baseRole(scopes)); err != nil {
			return err
		}
	}
	if err := s.limitRoleTokens(ctx, tx, a, v, scopes); err != nil {
		return err
	}
	added := slices.DeleteFunc(slices.Clone(scopes), func(sc string) bool { return slices.Contains(v.Scopes, sc) })
	removed := slices.DeleteFunc(slices.Clone(v.Scopes), func(sc string) bool { return slices.Contains(scopes, sc) })
	return record(ctx, tx, &a.principal, a.WorkspaceID, "role.updated", "role", v.ID, map[string]any{
		"builtin": v.Builtin, "scopes": scopes, "scopes_added": added, "scopes_removed": removed, "renamed": name != v.Name})
}

// holdsRole is a memberships filter for the people on role v ($2).
func holdsRole(v roleView) string {
	if v.Builtin != nil {
		return `role_id IS NULL AND role = $2`
	}
	return `role_id = $2::uuid`
}

// limitRoleTokens keeps the tokens of everyone on a role within its new
// scopes.
func (s *Server) limitRoleTokens(ctx context.Context, tx pgx.Tx, a *access, v roleView, scopes []string) error {
	rows, err := tx.Query(ctx, `SELECT user_id::text, brand_ids::text[] FROM memberships WHERE workspace_id = $1 AND `+holdsRole(v), a.WorkspaceID, v.ID)
	if err != nil {
		return err
	}
	type holder struct {
		id     string
		brands []string
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (holder, error) {
		var h holder
		err := r.Scan(&h.id, &h.brands)
		return h, err
	})
	if err != nil {
		return err
	}
	for _, h := range list {
		if err := limitMemberTokens(ctx, tx, &a.principal, a.WorkspaceID, h.id, scopes, h.brands); err != nil {
			return err
		}
	}
	return nil
}

var errRoleBrands = errInvalid("some people on this role are kept to some brands, and a role with workspace-wide permissions (settings, people, roles, API tokens or the audit log) covers every brand; give them every brand first")

// deleteRole deletes a workspace's own role, moving its people and
// pending invites to move_to, or puts a starting role back to its
// defaults.
func (s *Server) deleteRole(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	moveTo := r.URL.Query().Get("move_to")
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockRoles(ctx, tx, a.WorkspaceID); err != nil {
			return err
		}
		list, err := loadRoles(ctx, tx, a.WorkspaceID)
		if err != nil {
			return err
		}
		v, ok := findRole(list, r.PathValue("role"))
		if !ok {
			return errNotFound
		}
		if err := s.checkRoleChange(a, v); err != nil {
			return err
		}
		if v.Builtin != nil {
			// Reset: the defaults must be within the caller too.
			def := auth.RoleScopes(*v.Builtin)
			if !auth.Within(def, a.Scopes) {
				return errDenied("the default for this role includes things you can't do, so you can't put it back")
			}
			if !v.Edited {
				return nil
			}
			if err := s.applyRole(ctx, tx, a, v, builtinName(*v.Builtin), builtinDescription(*v.Builtin), def); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `DELETE FROM workspace_roles WHERE workspace_id = $1 AND builtin = $2`, a.WorkspaceID, *v.Builtin)
			return err
		}
		if v.Members > 0 || v.Invites > 0 {
			if moveTo == "" {
				return errRoleInUse
			}
			to, ok := findRole(list, moveTo)
			if !ok || to.ID == v.ID {
				return errInvalid("move_to must be another role in this workspace")
			}
			if to.Builtin != nil && *to.Builtin == auth.RoleOwner {
				return errInvalid("people can't be moved to Owner this way; make each one an owner instead")
			}
			// Moving people is changing their role: the usual rules apply.
			if !auth.Within(to.Scopes, a.Scopes) {
				return errDenied("you can't move people to a role that can do things you can't")
			}
			if !auth.Within(v.Scopes, to.Scopes) && !a.has(auth.ScopeMembersRemove) {
				return errDenied("moving these people to " + to.Name + " takes access away from them, which needs the Remove people permission")
			}
			if err := s.moveRole(ctx, tx, a, v, to); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM workspace_roles WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, v.ID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "role.deleted", "role", v.ID, map[string]any{"moved_to": moveTo, "members": v.Members, "invites": v.Invites})
	})
	if s.roleError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var errRoleInUse = errors.New("role in use")

// moveRole moves everyone and every pending invite on role from to role to.
func (s *Server) moveRole(ctx context.Context, tx pgx.Tx, a *access, from, to roleView) error {
	toID := roleIDOf(to)
	rows, err := tx.Query(ctx, `SELECT user_id::text, role, brand_ids::text[] FROM memberships WHERE workspace_id = $1 AND role_id = $2 FOR UPDATE`, a.WorkspaceID, from.ID)
	if err != nil {
		return err
	}
	type holder struct {
		id, role string
		brands   []string
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (holder, error) {
		var h holder
		err := r.Scan(&h.id, &h.role, &h.brands)
		return h, err
	})
	if err != nil {
		return err
	}
	for _, h := range list {
		brands := h.brands
		if !to.BrandLimitable {
			brands = []string{}
		}
		if _, err := tx.Exec(ctx, `UPDATE memberships SET role = $3, role_id = $4, brand_ids = $5::uuid[] WHERE workspace_id = $1 AND user_id = $2`,
			a.WorkspaceID, h.id, to.base, toID, brands); err != nil {
			return err
		}
		if err := limitMemberTokens(ctx, tx, &a.principal, a.WorkspaceID, h.id, to.Scopes, brands); err != nil {
			return err
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "member.role_changed", "user", h.id, map[string]any{
			"from": h.role, "to": to.base, "from_role_id": from.ID, "to_role_id": to.ID}); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE invites SET role = $3, role_id = $4, brand_ids = CASE WHEN $5 THEN brand_ids ELSE '{}' END WHERE workspace_id = $1 AND role_id = $2`,
		a.WorkspaceID, from.ID, to.base, toID, to.BrandLimitable)
	return err
}

// roleIDOf is the id stored in role_id: null for a starting role.
func roleIDOf(v roleView) *string {
	if v.Builtin != nil {
		return nil
	}
	return &v.ID
}

func builtinName(key string) string {
	for _, b := range builtinRoles {
		if b.key == key {
			return b.name
		}
	}
	return key
}

func builtinDescription(key string) string {
	for _, b := range builtinRoles {
		if b.key == key {
			return b.description
		}
	}
	return ""
}

func (s *Server) roleError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, errRoleName):
		writeError(w, r, http.StatusConflict, "conflict", "another role already has this name")
	case errors.Is(err, errRoleInUse):
		writeError(w, r, http.StatusConflict, "conflict", "people or pending invites use this role: send move_to with the role they should move to")
	default:
		return s.allocError(w, r, err)
	}
	return true
}

// memberRoleScopes is the SQL for the scopes stored for membership m's
// role: its own role, or the workspace's version of its starting role;
// null when the starting role's defaults apply.
const memberRoleScopes = `(SELECT wr.scopes FROM workspace_roles wr WHERE wr.workspace_id = m.workspace_id
	AND (wr.id = m.role_id OR (m.role_id IS NULL AND wr.builtin = m.role)))`

// roleCheck is what a change to someone's role needs: they hold
// curScopes now (nil when they are new), and get newScopes (nil when
// removed). It returns why the caller may not, or nil.
func roleCheck(a *access, curRole string, curScopes []string, newRole string, newScopes []string, narrowsBrands bool) error {
	if curRole == auth.RoleOwner || newRole == auth.RoleOwner {
		if a.Role != auth.RoleOwner || a.TokenID != "" {
			return errDenied("only an owner can make, change or remove owners")
		}
	}
	if curScopes != nil && !auth.Within(curScopes, a.Scopes) {
		return errDenied("this person can do things you can't, so you can't change their access")
	}
	if newScopes != nil && !auth.Within(newScopes, a.Scopes) {
		return errDenied("you can't give someone a role that can do things you can't")
	}
	takesAway := curScopes != nil && (newScopes == nil || !auth.Within(curScopes, newScopes) || narrowsBrands)
	if takesAway && !a.has(auth.ScopeMembersRemove) {
		return errDenied("removing someone or reducing their access needs the Remove people permission, which owners have")
	}
	return nil
}

// narrowed reports whether a brand list takes brands away: from every
// brand (empty) to some, or dropping one.
func narrowed(from, to []string) bool {
	if len(to) == 0 {
		return false
	}
	return len(from) == 0 || slices.ContainsFunc(from, func(b string) bool { return !slices.Contains(to, b) })
}

// leaveWorkspace takes the caller out of a workspace. Anyone can leave,
// except its last owner.
func (s *Server) leaveWorkspace(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if a.UserID == "" || a.TokenID != "" {
		writeError(w, r, http.StatusForbidden, "forbidden", "only a signed-in person can leave a workspace")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var role string
		err := tx.QueryRow(ctx, `SELECT role FROM memberships WHERE workspace_id = $1 AND user_id = $2 FOR UPDATE`, a.WorkspaceID, a.UserID).Scan(&role)
		if errors.Is(err, pgx.ErrNoRows) {
			return errInvalid("you're not a member of this workspace")
		}
		if err != nil {
			return err
		}
		if role == auth.RoleOwner {
			var others int
			if err := tx.QueryRow(ctx, `SELECT count(*)::int FROM (SELECT 1 FROM memberships WHERE workspace_id = $1 AND role = 'owner' AND user_id <> $2 FOR UPDATE) o`, a.WorkspaceID, a.UserID).Scan(&others); err != nil {
				return err
			}
			if others == 0 {
				return errLastOwner
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM memberships WHERE workspace_id = $1 AND user_id = $2`, a.WorkspaceID, a.UserID); err != nil {
			return err
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "member.left", "user", a.UserID, map[string]any{"role": role}); err != nil {
			return err
		}
		return limitMemberTokens(ctx, tx, &a.principal, a.WorkspaceID, a.UserID, nil, nil)
	})
	if errors.Is(err, errLastOwner) {
		writeError(w, r, http.StatusConflict, "last_owner", "you're the last owner: make someone else an owner before you leave")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
