package api

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/allocation"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/features"
	"github.com/outflareai/outflare/engine/internal/oauth"
	"github.com/outflareai/outflare/engine/internal/sysmail"
	"github.com/outflareai/outflare/engine/internal/track"
)

func (s *Server) routesAccess(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/setup", s.setupStatus)
	mux.HandleFunc("POST /v1/setup", s.setup)
	mux.HandleFunc("POST /v1/auth/login", s.login)
	mux.HandleFunc("POST /v1/auth/logout", s.logout)
	mux.HandleFunc("POST /v1/invites/accept", s.acceptInvite)

	mux.HandleFunc("GET /v1/me", s.requireUser(s.me))
	mux.HandleFunc("POST /v1/workspaces", s.requireUser(s.createWorkspace))

	mux.HandleFunc("GET /v1/workspaces/{ws}", s.inBrand(auth.ScopeWorkspaceRead, s.getWorkspace))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}", s.inWorkspace(auth.ScopeWorkspaceManage, s.updateWorkspace))
	mux.HandleFunc("GET /v1/workspaces/{ws}/slug-availability", s.inWorkspace(auth.ScopeWorkspaceManage, s.workspaceSlugAvailable))
	mux.HandleFunc("GET /v1/workspaces/{ws}/links/{kind}/{ref}", s.inBrand(auth.ScopeWorkspaceRead, s.itemLink))
	mux.HandleFunc("GET /v1/workspaces/{ws}/members", s.inBrand(auth.ScopeMembersRead, s.listMembers))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/members/{user}", s.inWorkspace(auth.ScopeMembersManage, s.updateMember))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/members/{user}", s.inWorkspace(auth.ScopeMembersRemove, s.removeMember))
	mux.HandleFunc("POST /v1/workspaces/{ws}/leave", s.inBrand(auth.ScopeWorkspaceRead, s.leaveWorkspace))
	mux.HandleFunc("GET /v1/workspaces/{ws}/invites", s.inWorkspace(auth.ScopeMembersManage, s.listInvites))
	mux.HandleFunc("POST /v1/workspaces/{ws}/invites", s.inWorkspace(auth.ScopeMembersManage, s.createInvite))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/invites/{id}", s.inWorkspace(auth.ScopeMembersManage, s.revokeInvite))
	mux.HandleFunc("GET /v1/workspaces/{ws}/api-tokens", s.inWorkspace(auth.ScopeTokensManage, s.listTokens))
	mux.HandleFunc("POST /v1/workspaces/{ws}/api-tokens", s.inWorkspace(auth.ScopeTokensManage, s.createToken))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/api-tokens/{id}", s.inWorkspace(auth.ScopeTokensManage, s.updateTokenLimits))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/api-tokens/{id}", s.inWorkspace(auth.ScopeTokensManage, s.revokeToken))
	mux.HandleFunc("GET /v1/workspaces/{ws}/audit-events", s.inWorkspace(auth.ScopeAuditRead, s.listAudit))
}

// ---- Setup, login, logout ----

func (s *Server) setupStatus(w http.ResponseWriter, r *http.Request) {
	var anyUser bool
	if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM users)`).Scan(&anyUser); err != nil {
		s.internal(w, r, err)
		return
	}
	// The first-run form tells the owner about usage stats before they
	// start, and lets them turn it off there.
	off, err := s.telemetry.Off(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var by *string
	if off != "" {
		by = &off
	}
	writeJSON(w, http.StatusOK, map[string]any{"needed": !anyUser, "telemetry": map[string]any{"enabled": off == "", "disabled_by": by}})
}

type setupRequest struct {
	Email         string `json:"email"`
	Name          string `json:"name"`
	Password      string `json:"password"`
	WorkspaceName string `json:"workspace_name"`
	// Telemetry false turns off anonymous usage stats from the start.
	Telemetry *bool `json:"telemetry"`
}

// setup creates the first user and workspace. It only works while no user exists.
func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	var req setupRequest
	if !decode(w, r, &req) {
		return
	}
	email, ok := validEmail(req.Email)
	name := strings.TrimSpace(req.Name)
	wsName := strings.TrimSpace(req.WorkspaceName)
	switch {
	case !ok:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a valid email is required")
		return
	case name == "" || wsName == "":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name and workspace_name are required")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	ctx := r.Context()
	var userID, wsID, sessionSecret string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Serialise concurrent setup attempts.
		if _, err := tx.Exec(ctx, `LOCK TABLE users IN EXCLUSIVE MODE`); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users)`).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return errSetupDone
		}
		var actorID string
		if userID, actorID, err = createUser(ctx, tx, email, name, hash); err != nil {
			return err
		}
		if wsID, err = createWorkspace(ctx, tx, wsName, userID); err != nil {
			return err
		}
		if sessionSecret, err = createSession(ctx, tx, userID, r); err != nil {
			return err
		}
		if req.Telemetry != nil && !*req.Telemetry {
			if _, err := tx.Exec(ctx, `UPDATE telemetry_state SET enabled = false, changed_by = $1, changed_at = now()`, userID); err != nil {
				return err
			}
		}
		p := &principal{ActorID: actorID, ActorType: "human", UserID: userID}
		return record(ctx, tx, p, wsID, "workspace.created", "workspace", wsID, map[string]any{"name": wsName, "via": "setup"})
	})
	if errors.Is(err, errSetupDone) {
		writeError(w, r, http.StatusConflict, "already_set_up", "this instance already has users; sign in instead")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.sendVerification(ctx, userID, email) //nolint:errcheck // logged; they can ask again from the app
	s.setSessionCookie(w, sessionSecret)
	writeJSON(w, http.StatusCreated, map[string]string{"user_id": userID, "workspace_id": wsID})
}

var errSetupDone = errors.New("setup already done")

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decode(w, r, &req) {
		return
	}
	key := strings.ToLower(strings.TrimSpace(req.Email))
	if !s.passwordAttemptAllowed(w, r, key, "password") {
		return
	}
	ctx := r.Context()
	var userID, actorID, hash string
	err := s.pool.QueryRow(ctx,
		`SELECT id, actor_id, password_hash FROM users WHERE lower(email) = $1 AND disabled_at IS NULL`, key,
	).Scan(&userID, &actorID, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		auth.BurnPasswordCheck(req.Password)
		s.passwordFailed(r, key, "password")
		writeError(w, r, http.StatusUnauthorized, "invalid_credentials", "email or password is wrong")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !auth.CheckPassword(hash, req.Password) {
		s.passwordFailed(r, key, "password")
		writeError(w, r, http.StatusUnauthorized, "invalid_credentials", "email or password is wrong")
		return
	}
	s.limiter.reset(r.Context(), key)

	var secret string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if secret, err = createSession(ctx, tx, userID, r); err != nil {
			return err
		}
		p := &principal{ActorID: actorID, ActorType: "human", UserID: userID}
		return record(ctx, tx, p, "", "user.login", "user", userID, nil)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.setSessionCookie(w, secret)
	writeJSON(w, http.StatusOK, map[string]string{"user_id": userID})
}

// passwordAttemptAllowed answers 429 when an address, or the caller's
// network, has failed to sign in too often lately. Every place that checks
// a password goes through it, so none is a way around the other.
func (s *Server) passwordAttemptAllowed(w http.ResponseWriter, r *http.Request, email, via string) bool {
	limit := ""
	switch {
	case s.limiter.blocked(r.Context(), email):
		limit = "address"
	case s.limiter.over(r.Context(), "login-ip:"+networkKey(clientIP(r)), loginNetworkFailures):
		limit = "network"
	default:
		return true
	}
	emitSignInFailed(r, "rate_limited", map[string]any{"via": via, "limit": limit})
	retryLater(w, r, loginWindow, "too many failed attempts; try again in 15 minutes")
	return false
}

// passwordFailed counts a wrong password (or unknown address) against both
// limits.
func (s *Server) passwordFailed(r *http.Request, email, via string) {
	s.limiter.fail(r.Context(), email)
	s.limiter.fail(r.Context(), "login-ip:"+networkKey(clientIP(r)))
	emitSignInFailed(r, "invalid_credentials", map[string]any{"via": via})
}

func emitSignInFailed(r *http.Request, code string, props map[string]any) {
	track.Emit(r.Context(), track.Event{Name: "user.sign_in_failed", ActorType: "human", Outcome: track.Failure, ErrorCode: code,
		RequestID: RequestID(r.Context()), Props: props})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		var userID string
		err := s.pool.QueryRow(r.Context(), `DELETE FROM sessions WHERE token_hash = $1 RETURNING user_id`, auth.HashToken(c.Value)).Scan(&userID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.internal(w, r, err)
			return
		}
		if userID != "" {
			emitUser(r.Context(), "user.signed_out", userID, nil)
		}
	}
	if c, err := r.Cookie(viewCookie); err == nil {
		// Signing out ends a support view in progress too.
		if _, err := s.pool.Exec(r.Context(), `UPDATE support_views SET ended_at = now() WHERE token_hash = $1 AND ended_at IS NULL`, auth.HashToken(c.Value)); err != nil {
			s.internal(w, r, err)
			return
		}
		s.setViewCookie(w, "", 0)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteLaxMode})
	s.setSignedInHint(w, false)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setSessionCookie(w http.ResponseWriter, secret string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    secret,
		Path:     "/",
		MaxAge:   int(sessionLifetime.Seconds()),
		HttpOnly: true,
		Secure:   s.secureCookies,
		SameSite: http.SameSiteLaxMode,
	})
	s.setSignedInHint(w, true)
}

// setSignedInHint tells the operator's website, on a parent domain of this
// server (OUTFLARE_SIGNED_IN_COOKIE_DOMAIN), that this browser is signed in, so its
// header can link to the app in place of Sign in. The cookie holds only
// "1": the session stays in the HttpOnly cookie that only this host gets.
// It is readable by scripts on purpose, which is why it carries nothing else.
func (s *Server) setSignedInHint(w http.ResponseWriter, on bool) {
	if s.signedInDomain == "" {
		return
	}
	c := &http.Cookie{Name: signedInCookie, Value: "1", Domain: s.signedInDomain, Path: "/",
		MaxAge: int(sessionLifetime.Seconds()), Secure: s.secureCookies, SameSite: http.SameSiteLaxMode}
	if !on {
		c.Value, c.MaxAge = "", -1
	}
	http.SetCookie(w, c)
}

// syncSignedInHint keeps the hint in step with the session in the browser:
// it is added for a session made before the hint was turned on, and removed
// once the session has expired or was signed out somewhere else.
func (s *Server) syncSignedInHint(w http.ResponseWriter, r *http.Request, p *principal) {
	if s.signedInDomain == "" || r.Header.Get("Authorization") != "" {
		return
	}
	_, err := r.Cookie(signedInCookie)
	hinted := err == nil
	switch signedIn := p != nil && p.SessionID != ""; {
	case signedIn && !hinted:
		s.setSignedInHint(w, true)
	case !signedIn && hinted:
		s.setSignedInHint(w, false)
	}
}

// ---- Me and workspaces ----

type workspaceView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
	Role string `json:"role,omitempty"`
	// The workspace's own role the caller is on, or null for a starting role.
	RoleID *string `json:"role_id,omitempty"`
	// What the caller may do here; the app shows only what these allow.
	Scopes []string `json:"scopes,omitempty"`
	// The brands the caller is kept to; empty for every brand.
	BrandIDs []string `json:"brand_ids"`
	// A test workspace: its mailboxes are simulated, so nothing it sends
	// reaches anyone.
	Test bool `json:"test"`
	// Parts of Outflare switched on for this workspace (package
	// features). Left out when none is on, and in lists other than /v1/me.
	Features []string `json:"features,omitempty"`
}

func scanWorkspace(row pgx.Row, v *workspaceView, withRole bool) error {
	dest := []any{&v.ID, &v.Name, &v.Slug}
	if withRole {
		dest = append(dest, &v.Role, &v.BrandIDs, &v.RoleID, &v.Scopes)
	}
	dest = append(dest, &v.Test)
	if err := row.Scan(dest...); err != nil {
		return err
	}
	if withRole {
		v.Scopes = effectiveScopes(v.Role, v.Scopes)
	}
	if v.BrandIDs == nil || (withRole && !auth.BrandLimitable(v.Scopes)) {
		v.BrandIDs = []string{}
	}
	return nil
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	var user struct {
		ID            string `json:"id"`
		Email         string `json:"email"`
		Name          string `json:"name"`
		EmailVerified bool   `json:"email_verified"`
		HasPassword   bool   `json:"has_password"`
		// Seconds until another confirmation email can be sent.
		EmailResendAfter int `json:"email_resend_after"`
	}
	ctx := r.Context()
	if err := s.pool.QueryRow(ctx, `SELECT id, email, name, email_verified_at IS NOT NULL, password_hash <> '' FROM users WHERE id = $1`, p.UserID).
		Scan(&user.ID, &user.Email, &user.Name, &user.EmailVerified, &user.HasPassword); err != nil {
		s.internal(w, r, err)
		return
	}
	if !user.EmailVerified {
		wait, _, err := linkWait(ctx, s.pool, p.UserID, "verify_email")
		if err != nil {
			s.internal(w, r, err)
			return
		}
		user.EmailResendAfter = seconds(wait)
	}
	// Workspaces the person is a member of, with their role.
	rows, err := s.pool.Query(ctx, `
		SELECT w.id, w.name, w.slug, m.role, m.brand_ids::text[], m.role_id::text, `+memberRoleScopes+`, w.test
		FROM memberships m JOIN workspaces w ON w.id = m.workspace_id
		WHERE m.user_id = $1
		ORDER BY w.created_at`, p.UserID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	defer rows.Close()
	wss := []workspaceView{}
	for rows.Next() {
		var v workspaceView
		if err := scanWorkspace(rows, &v, true); err != nil {
			s.internal(w, r, err)
			return
		}
		if p.ViewID != "" {
			v.Scopes = viewScopes(v.Scopes)
		}
		wss = append(wss, v)
	}
	if err := rows.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	rows.Close()
	for i := range wss {
		if wss[i].Features, err = features.List(ctx, s.pool, wss[i].ID); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	view, err := s.supportViewOf(ctx, p)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	links := s.menuLinksFor(ctx, p.UserID)
	if view != nil {
		links = []MenuLink{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user, "workspaces": wss, "menu_links": links, "support_view": view})
}

func (s *Server) createWorkspace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		// A test workspace: simulated mailboxes, nothing really sent.
		Test bool `json:"test"`
		// Sample mailboxes and leads to start from (test workspaces only).
		SampleData bool `json:"sample_data"`
	}
	if !decode(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	if req.SampleData && !req.Test {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "sample_data needs test: true; sample data only goes in test workspaces")
		return
	}
	p := principalFrom(r.Context())
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if id, err = createWorkspace(ctx, tx, name, p.UserID); err != nil {
			return err
		}
		if req.Test {
			if _, err := tx.Exec(ctx, `UPDATE workspaces SET test = true WHERE id = $1`, id); err != nil {
				return err
			}
		}
		return record(ctx, tx, p, id, "workspace.created", "workspace", id, map[string]any{"name": name, "test": req.Test})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if req.SampleData {
		if err := s.addSampleData(ctx, &access{principal: *p, WorkspaceID: id, Role: auth.RoleOwner, Scopes: auth.RoleScopes(auth.RoleOwner), Test: true}); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	s.writeWorkspace(w, r, id, auth.RoleOwner, http.StatusCreated)
}

func (s *Server) getWorkspace(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	s.writeWorkspace(w, r, a.WorkspaceID, a.Role, http.StatusOK)
}

func (s *Server) writeWorkspace(w http.ResponseWriter, r *http.Request, id, role string, status int) {
	v := workspaceView{Role: role}
	if role != "" {
		v.Scopes = effectiveScopes(role, nil)
	}
	row := s.pool.QueryRow(r.Context(), `SELECT w.id, w.name, w.slug, w.test FROM workspaces w WHERE w.id = $1`, id)
	if err := scanWorkspace(row, &v, false); err != nil {
		s.internal(w, r, err)
		return
	}
	var err error
	if v.Features, err = features.List(r.Context(), s.pool, id); err != nil {
		s.internal(w, r, err)
		return
	}
	if a := accessFrom(r.Context()); a != nil {
		if a.Brands != nil {
			v.BrandIDs = a.Brands
		}
		v.RoleID, v.Scopes = a.RoleID, a.Scopes
	}
	writeJSON(w, status, v)
}

// ---- Members ----

type memberView struct {
	UserID    string    `json:"user_id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	// The brands the member works on; empty for every brand.
	BrandIDs []string `json:"brand_ids"`
	// The workspace's own role they are on, or null for a starting role.
	RoleID *string `json:"role_id"`
	// Their role's name as the workspace shows it.
	RoleName string `json:"role_name"`
}

func (s *Server) listMembers(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	roles, err := loadRoles(ctx, s.pool, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	rows, err := s.pool.Query(ctx, `
		SELECT u.id, u.email, u.name, m.role, m.created_at, m.brand_ids::text[], m.role_id::text FROM memberships m JOIN users u ON u.id = m.user_id
		WHERE m.workspace_id = $1 ORDER BY m.created_at`, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (memberView, error) {
		var v memberView
		err := row.Scan(&v.UserID, &v.Email, &v.Name, &v.Role, &v.CreatedAt, &v.BrandIDs, &v.RoleID)
		v.RoleName = roleOf(roles, v.Role, v.RoleID).Name
		return v, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) updateMember(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		// A starting role (owner, admin, operator or member, viewer).
		Role string `json:"role"`
		// One of the workspace's own roles, instead of role.
		RoleID string `json:"role_id"`
		// Brand ids or handles; empty for every brand.
		BrandIDs *[]string `json:"brand_ids"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Role == "" && req.RoleID == "" && req.BrandIDs == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send role, role_id, brand_ids or more than one")
		return
	}
	if req.Role != "" && req.RoleID != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send role or role_id, not both")
		return
	}
	if req.Role != "" && !auth.ValidRole(req.Role) && req.Role != "member" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "role must be owner, admin, operator (member) or viewer; use role_id for the workspace's own roles")
		return
	}
	var brands []string
	if req.BrandIDs != nil {
		var err error
		if brands, err = s.brandIDsFor(r.Context(), a.WorkspaceID, *req.BrandIDs); s.allocError(w, r, err) {
			return
		}
	}
	ref := req.Role
	if req.RoleID != "" {
		ref = req.RoleID
	}
	s.changeMember(w, r, a, r.PathValue("user"), memberChange{role: ref, brands: brands, setBrands: req.BrandIDs != nil})
}

func (s *Server) removeMember(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	s.changeMember(w, r, a, r.PathValue("user"), memberChange{remove: true})
}

// memberChange is a new role, a new brand list, or both; or a removal.
type memberChange struct {
	role      string // a role's id or starting key; empty keeps the role
	brands    []string
	setBrands bool
	remove    bool
}

// brandIDsFor turns brand ids or handles into ids, without repeats. An
// empty list means every brand.
func (s *Server) brandIDsFor(ctx context.Context, ws string, refs []string) ([]string, error) {
	if len(refs) > 100 {
		return nil, errInvalid("brand_ids holds at most 100 brands")
	}
	out := []string{}
	for _, ref := range refs {
		id, err := s.brandIDFor(ctx, ws, ref)
		if errors.Is(err, errNotFound) {
			return nil, errInvalid("brand not found: " + truncate(ref, 60))
		}
		if err != nil {
			return nil, err
		}
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out, nil
}

// errBrandsForAdmins: a role with workspace-wide permissions sees every brand.
var errBrandsForAdmins = errInvalid("this role has workspace-wide permissions (settings, people, roles, API tokens or the audit log), so it always works on every brand; brand_ids is only for roles without them")

// changeMember sets a member's role or brands, or removes them.
func (s *Server) changeMember(w http.ResponseWriter, r *http.Request, a *access, userID string, c memberChange) {
	if !validUUID(userID) {
		writeError(w, r, http.StatusNotFound, "not_found", "member not found")
		return
	}
	ctx := r.Context()
	var target roleView
	var brands []string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var current string
		var curID *string
		var curBrands []string
		err := tx.QueryRow(ctx, `SELECT role, role_id::text, brand_ids::text[] FROM memberships WHERE workspace_id = $1 AND user_id = $2 FOR UPDATE`, a.WorkspaceID, userID).Scan(&current, &curID, &curBrands)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		roles, err := loadRoles(ctx, tx, a.WorkspaceID)
		if err != nil {
			return err
		}
		cur := roleOf(roles, current, curID)
		target = cur
		if c.role != "" {
			var ok bool
			if target, ok = findRole(roles, c.role); !ok {
				return errInvalid("role not found: " + truncate(c.role, 60))
			}
		}
		brands = curBrands
		if c.setBrands {
			brands = c.brands
		}
		// A role with workspace-wide permissions sees every brand:
		// moving someone to one clears their brands.
		if !target.BrandLimitable {
			if c.setBrands && len(brands) > 0 {
				return errBrandsForAdmins
			}
			brands = []string{}
		}
		var newScopes []string
		newRole := ""
		if !c.remove {
			newScopes, newRole = target.Scopes, target.base
		}
		if err := roleCheck(a, current, cur.Scopes, newRole, newScopes, !c.remove && narrowed(curBrands, brands)); err != nil {
			return err
		}
		if current == auth.RoleOwner && (c.remove || newRole != auth.RoleOwner) {
			// Lock every owner row so two concurrent demotions cannot both pass the check.
			rows, err := tx.Query(ctx, `SELECT user_id FROM memberships WHERE workspace_id = $1 AND role = 'owner' FOR UPDATE`, a.WorkspaceID)
			if err != nil {
				return err
			}
			owners, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return err
			}
			if len(owners) <= 1 {
				return errLastOwner
			}
		}
		if c.remove {
			if _, err := tx.Exec(ctx, `DELETE FROM memberships WHERE workspace_id = $1 AND user_id = $2`, a.WorkspaceID, userID); err != nil {
				return err
			}
			if err := record(ctx, tx, &a.principal, a.WorkspaceID, "member.removed", "user", userID, map[string]any{"role": current, "role_id": curID}); err != nil {
				return err
			}
			return limitMemberTokens(ctx, tx, &a.principal, a.WorkspaceID, userID, nil, nil)
		}
		newID := roleIDOf(target)
		if _, err := tx.Exec(ctx, `UPDATE memberships SET role = $3, role_id = $4, brand_ids = $5::uuid[] WHERE workspace_id = $1 AND user_id = $2`, a.WorkspaceID, userID, newRole, newID, brands); err != nil {
			return err
		}
		if err := limitMemberTokens(ctx, tx, &a.principal, a.WorkspaceID, userID, newScopes, brands); err != nil {
			return err
		}
		if target.ID != cur.ID {
			if err := record(ctx, tx, &a.principal, a.WorkspaceID, "member.role_changed", "user", userID, map[string]any{
				"from": current, "to": newRole, "from_role_id": curID, "to_role_id": newID}); err != nil {
				return err
			}
		}
		if !slices.Equal(brands, curBrands) {
			return record(ctx, tx, &a.principal, a.WorkspaceID, "member.brands_changed", "user", userID, map[string]any{"from": curBrands, "to": brands})
		}
		return nil
	})
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "member not found")
	case errors.Is(err, errLastOwner):
		writeError(w, r, http.StatusConflict, "last_owner", "a workspace must keep at least one owner")
	case s.allocError(w, r, err):
	case c.remove:
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"user_id": userID, "role": target.base, "role_id": roleIDOf(target), "role_name": target.Name, "brand_ids": brands})
	}
}

// limitMemberTokens keeps the API tokens a member made within what they may
// do now, since a token never holds more than its maker. Removed (allowed
// nil), every one stops working. Given less, a token loses the scopes the
// role lacks, and stops working when none are left. Kept to some brands, a
// token that reaches other brands stops working. Connected apps check this
// themselves each time they refresh (stillGranted).
func limitMemberTokens(ctx context.Context, tx pgx.Tx, p *principal, ws, userID string, allowed []string, brands []string) error {
	rows, err := tx.Query(ctx, `
		SELECT t.id, t.scopes, t.brand_id::text FROM api_tokens t
		WHERE t.workspace_id = $1 AND t.created_by = $2 AND t.revoked_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM oauth_grants g WHERE g.api_token_id = t.id)
		FOR UPDATE OF t`, ws, userID)
	if err != nil {
		return err
	}
	type token struct {
		id     string
		scopes []string
		brand  *string
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (token, error) {
		var t token
		err := r.Scan(&t.id, &t.scopes, &t.brand)
		return t, err
	})
	if err != nil {
		return err
	}
	for _, t := range list {
		keep := slices.DeleteFunc(slices.Clone(t.scopes), func(sc string) bool { return !slices.Contains(allowed, sc) })
		reason := ""
		switch {
		case allowed == nil:
			reason = "member_removed"
		case len(keep) == 0:
			reason = "role_changed"
		case len(brands) > 0 && (t.brand == nil || !slices.Contains(brands, *t.brand)):
			reason = "brands_changed"
		}
		if reason != "" {
			if _, err := tx.Exec(ctx, `UPDATE api_tokens SET revoked_at = now() WHERE id = $1`, t.id); err != nil {
				return err
			}
			if err := record(ctx, tx, p, ws, "token.revoked", "api_token", t.id, map[string]any{"reason": reason, "user_id": userID}); err != nil {
				return err
			}
			continue
		}
		if len(keep) == len(t.scopes) {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE api_tokens SET scopes = $2 WHERE id = $1`, t.id, keep); err != nil {
			return err
		}
		removed := slices.DeleteFunc(slices.Clone(t.scopes), func(sc string) bool { return slices.Contains(keep, sc) })
		if err := record(ctx, tx, p, ws, "token.updated", "api_token", t.id, map[string]any{
			"renamed": false, "scopes": keep, "scopes_added": []string{}, "scopes_removed": removed, "reason": "role_changed"}); err != nil {
			return err
		}
	}
	return nil
}

var (
	errNotFound  = errors.New("not found")
	errForbidden = errors.New("forbidden")
	errLastOwner = errors.New("last owner")
)

// ---- Invites ----

const inviteLifetime = 7 * 24 * time.Hour

type inviteView struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	BrandIDs  []string  `json:"brand_ids"`
	// The workspace's own role the invite gives, or null for a starting role.
	RoleID   *string `json:"role_id"`
	RoleName string  `json:"role_name"`
}

func (s *Server) listInvites(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	roles, err := loadRoles(r.Context(), s.pool, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT id, email, role, created_at, expires_at, brand_ids::text[], role_id::text FROM invites
		WHERE workspace_id = $1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()
		ORDER BY created_at DESC`, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (inviteView, error) {
		var v inviteView
		err := row.Scan(&v.ID, &v.Email, &v.Role, &v.CreatedAt, &v.ExpiresAt, &v.BrandIDs, &v.RoleID)
		v.RoleName = roleOf(roles, v.Role, v.RoleID).Name
		return v, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) createInvite(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Email string `json:"email"`
		// A starting role (owner, admin, operator or member, viewer).
		Role string `json:"role"`
		// One of the workspace's own roles, instead of role.
		RoleID string `json:"role_id"`
		// Brand ids or handles the person will work on; empty for every
		// brand.
		BrandIDs []string `json:"brand_ids"`
	}
	if !decode(w, r, &req) {
		return
	}
	email, ok := validEmail(req.Email)
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a valid email is required")
		return
	}
	ref := req.Role
	if req.RoleID != "" {
		if req.Role != "" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "send role or role_id, not both")
			return
		}
		ref = req.RoleID
	}
	if ref == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "role or role_id is required")
		return
	}
	roles, err := loadRoles(r.Context(), s.pool, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	role, ok := findRole(roles, ref)
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "role not found: "+truncate(ref, 60))
		return
	}
	brands, err := s.brandIDsFor(r.Context(), a.WorkspaceID, req.BrandIDs)
	if err == nil && len(brands) > 0 && !role.BrandLimitable {
		err = errBrandsForAdmins
	}
	if err == nil {
		err = roleCheck(a, "", nil, role.base, role.Scopes, false)
	}
	if s.allocError(w, r, err) {
		return
	}
	roleID := roleIDOf(role)
	secret, hash := auth.NewToken(auth.PrefixInvite)
	ctx := r.Context()
	var invitedBy *string
	if a.UserID != "" {
		invitedBy = &a.UserID
	}
	var v inviteView
	var wsName, inviter string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var member bool
		err := tx.QueryRow(ctx, `
			SELECT w.name, coalesce((SELECT name FROM users WHERE id = $2), ''),
				EXISTS (SELECT 1 FROM memberships m JOIN users u ON u.id = m.user_id WHERE m.workspace_id = w.id AND lower(u.email) = lower($3))
			FROM workspaces w WHERE w.id = $1`, a.WorkspaceID, invitedBy, email).Scan(&wsName, &inviter, &member)
		if err != nil {
			return err
		}
		if member {
			return errAlreadyMember
		}
		// A new invite replaces any pending one for the same email, so only
		// the latest link works.
		if _, err := tx.Exec(ctx, `UPDATE invites SET revoked_at = now() WHERE workspace_id = $1 AND lower(email) = lower($2) AND accepted_at IS NULL AND revoked_at IS NULL`, a.WorkspaceID, email); err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO invites (workspace_id, email, role, token_hash, invited_by, expires_at, brand_ids, role_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7::uuid[], $8)
			RETURNING id, email, role, created_at, expires_at, brand_ids::text[], role_id::text`,
			a.WorkspaceID, email, role.base, hash, invitedBy, time.Now().Add(inviteLifetime), brands, roleID,
		).Scan(&v.ID, &v.Email, &v.Role, &v.CreatedAt, &v.ExpiresAt, &v.BrandIDs, &v.RoleID)
		if err != nil {
			return err
		}
		v.RoleName = role.Name
		return record(ctx, tx, &a.principal, a.WorkspaceID, "invite.created", "invite", v.ID, map[string]any{"email": email, "role": role.base, "role_id": roleID, "brand_ids": brands})
	})
	if errors.Is(err, errAlreadyMember) {
		writeError(w, r, http.StatusConflict, "conflict", email+" is already a member of this workspace")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// The link goes to the invited person by email, and back to the inviter
	// once, to share another way. The email sends in the background so the
	// answer doesn't wait on the email service; a server with none only
	// logs it.
	url := s.publicURL + "/invite#" + secret
	msg := inviteMessage(email, wsName, inviter, url)
	go func() {
		if err := s.sendAccountEmail(ctx, msg); err != nil {
			track.Fail(context.WithoutCancel(ctx), s.log, track.Event{Name: "invite.email_failed", WorkspaceID: a.WorkspaceID, Props: map[string]any{"provider": s.mailer.Name()}}, "email_failed", err)
		}
	}()
	emailed := s.mailer.Name() != "log"
	writeJSON(w, http.StatusCreated, map[string]any{
		"invite":     v,
		"token":      secret,
		"invite_url": url,
		"emailed":    emailed,
	})
}

var errAlreadyMember = errors.New("already a member")

func inviteMessage(email, workspace, inviter, url string) sysmail.Message {
	line := "You've been invited to work in the " + workspace + " workspace on Outflare."
	if inviter != "" {
		line = inviter + " invited you to work in the " + workspace + " workspace on Outflare."
	}
	return sysmail.Email{
		To:      email,
		Subject: "Join " + workspace + " on Outflare",
		Preview: line,
		Heading: "Join " + workspace,
		Body:    []string{line},
		Button:  "Accept invite",
		URL:     url,
		Note:    "The link works once, for 7 days. If you weren't expecting this, you can ignore this email.",
	}.Message()
}

func (s *Server) revokeInvite(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "invite not found")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE invites SET revoked_at = now() WHERE id = $1 AND workspace_id = $2 AND accepted_at IS NULL AND revoked_at IS NULL`, id, a.WorkspaceID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "invite.revoked", "invite", id, nil)
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "invite not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type acceptRequest struct {
	Token    string `json:"token"`
	Name     string `json:"name"`
	Password string `json:"password"`
	// Identity is a Google sign-in for the invited email (from
	// /v1/auth/google/callback), used instead of a password.
	Identity string `json:"identity"`
}

// acceptInvite joins the invited workspace. A new email creates an account; an
// existing account must prove its password.
func (s *Server) acceptInvite(w http.ResponseWriter, r *http.Request) {
	var req acceptRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	var inviteID, wsID, email, role string
	err := s.pool.QueryRow(ctx, `
		SELECT id, workspace_id, email, role FROM invites
		WHERE token_hash = $1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()`,
		auth.HashToken(req.Token)).Scan(&inviteID, &wsID, &email, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "invalid_invite", "this invite is invalid, used or expired")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}

	var userID, actorID, existingHash string
	err = s.pool.QueryRow(ctx, `SELECT id, actor_id, password_hash FROM users WHERE lower(email) = lower($1) AND disabled_at IS NULL`, email).
		Scan(&userID, &actorID, &existingHash)
	newUser := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !newUser {
		s.internal(w, r, err)
		return
	}
	// Proof that the person owns the invited email: a Google sign-in for it,
	// being signed in as it already, or (for an existing account) its password.
	var id *oauth.Identity
	if req.Identity != "" {
		if id, err = openIdentity(s.box, req.Identity); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if !strings.EqualFold(id.Email, email) {
			writeError(w, r, http.StatusBadRequest, "wrong_account",
				"this invite is for "+email+", but you signed in with Google as "+id.Email+"; use that account, or ask for an invite to "+id.Email)
			return
		}
	}
	signedIn := false
	if !newUser && id == nil {
		if p, err := s.authenticateSession(r); err == nil && p != nil && p.UserID == userID {
			signedIn = true
		}
	}
	var hash string
	name := strings.TrimSpace(req.Name)
	if newUser {
		if name == "" && id != nil {
			name = id.Name
		}
		if name == "" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required")
			return
		}
		if id == nil {
			if hash, err = auth.HashPassword(req.Password); err != nil {
				writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
				return
			}
		}
	} else if id == nil && !signedIn {
		key := strings.ToLower(email)
		if !s.passwordAttemptAllowed(w, r, key, "invite") {
			return
		}
		if !auth.CheckPassword(existingHash, req.Password) {
			s.passwordFailed(r, key, "invite")
			writeError(w, r, http.StatusUnauthorized, "invalid_credentials", "an account with this email exists; enter its password")
			return
		}
	}

	var secret string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE invites SET accepted_at = now() WHERE id = $1 AND accepted_at IS NULL AND revoked_at IS NULL`, inviteID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound // raced with another accept or a revoke
		}
		if newUser {
			if userID, actorID, err = createUser(ctx, tx, email, name, hash); err != nil {
				return err
			}
		}
		if id != nil {
			if _, err := tx.Exec(ctx, `
				INSERT INTO user_identities (provider, subject, user_id, email) VALUES ('google', $1, $2, $3)
				ON CONFLICT (provider, subject) DO NOTHING`, id.Subject, userID, id.Email); err != nil {
				return err
			}
			if err := markVerified(ctx, tx, userID, id.Email); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO memberships (workspace_id, user_id, role, brand_ids, role_id)
			SELECT $1, $2, $3, brand_ids, role_id FROM invites WHERE id = $4
			ON CONFLICT (workspace_id, user_id) DO NOTHING`, wsID, userID, role, inviteID); err != nil {
			return err
		}
		if secret, err = createSession(ctx, tx, userID, r); err != nil {
			return err
		}
		p := &principal{ActorID: actorID, ActorType: "human", UserID: userID}
		return record(ctx, tx, p, wsID, "invite.accepted", "invite", inviteID, map[string]any{"role": role})
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "invalid_invite", "this invite is invalid, used or expired")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if newUser && id == nil {
		s.sendVerification(ctx, userID, email) //nolint:errcheck // logged; they can ask again from the app
	}
	s.setSessionCookie(w, secret)
	writeJSON(w, http.StatusOK, map[string]string{"user_id": userID, "workspace_id": wsID})
}

// ---- API tokens ----

type tokenView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	// Set when the token only sees one brand's records and the shared ones,
	// and changes only that brand's.
	BrandID *string `json:"brand_id"`
	tokenLimitsView
}

// tokenLimitsView is what a token may do to mailbox allocation and sending.
// The same rules apply to people and agents; agents just get them here.
type tokenLimitsView struct {
	DailySendBudget  *int    `json:"daily_send_budget"`
	MaxPriority      *string `json:"max_priority"`
	MayTakeMailboxes bool    `json:"may_take_mailboxes"`
	DefaultTeamID    *string `json:"default_team_id"`
}

const tokenColumns = `id, name, prefix, scopes, created_at, expires_at, last_used_at, brand_id::text,
	daily_send_budget, CASE max_priority WHEN 0 THEN 'low' WHEN 1 THEN 'normal' WHEN 2 THEN 'high' END, may_take_mailboxes, default_team_id::text`

func scanToken(row pgx.Row) (tokenView, error) {
	var v tokenView
	err := row.Scan(&v.ID, &v.Name, &v.Prefix, &v.Scopes, &v.CreatedAt, &v.ExpiresAt, &v.LastUsedAt, &v.BrandID,
		&v.DailySendBudget, &v.MaxPriority, &v.MayTakeMailboxes, &v.DefaultTeamID)
	return v, err
}

type tokenLimits struct {
	DailySendBudget  optional[int]    `json:"daily_send_budget"`
	MaxPriority      optional[string] `json:"max_priority"`
	MayTakeMailboxes *bool            `json:"may_take_mailboxes"`
	DefaultTeamID    optional[string] `json:"default_team_id"`
}

// check validates the limits. A token creating or changing another token
// cannot grant more than it has itself.
func (l *tokenLimits) check(ctx context.Context, tx pgx.Tx, a *access) (prio *int, err error) {
	if b := l.DailySendBudget.Value; b != nil && (*b < 1 || *b > 1000000) {
		return nil, errInvalid("daily_send_budget must be 1 to 1000000, or null for no budget")
	}
	if v := l.MaxPriority.Value; v != nil {
		i := slices.Index(priorityNames, *v)
		if i < 0 {
			return nil, errInvalid("max_priority is low, normal or high, or null for no limit")
		}
		prio = &i
	}
	if t := l.DefaultTeamID.Value; t != nil {
		var ok bool
		if validUUID(*t) {
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM teams WHERE id = $1 AND workspace_id = $2)`, *t, a.WorkspaceID).Scan(&ok); err != nil {
				return nil, err
			}
		}
		if !ok {
			return nil, errInvalid("team not found")
		}
	}
	if a.ActorType != "api_token" {
		return prio, nil
	}
	var budget *int
	var maxPrio *int16
	var mayTake bool
	if err := tx.QueryRow(ctx, `SELECT daily_send_budget, max_priority, may_take_mailboxes FROM api_tokens WHERE id = $1`, a.TokenID).Scan(&budget, &maxPrio, &mayTake); err != nil {
		return nil, err
	}
	if budget != nil && (l.DailySendBudget.Value == nil || *l.DailySendBudget.Value > *budget) {
		return nil, errDenied("a token cannot grant a larger daily_send_budget than its own")
	}
	if maxPrio != nil && (prio == nil || *prio > int(*maxPrio)) {
		return nil, errDenied("a token cannot grant a higher max_priority than its own")
	}
	if l.MayTakeMailboxes != nil && *l.MayTakeMailboxes && !mayTake {
		return nil, errDenied("a token cannot grant may_take_mailboxes without having it")
	}
	return prio, nil
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), `
		SELECT `+tokenColumns+` FROM api_tokens
		WHERE workspace_id = $1 AND revoked_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM oauth_grants g WHERE g.api_token_id = api_tokens.id) -- connected apps are under Connect your AI
		ORDER BY created_at DESC`, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (tokenView, error) { return scanToken(r) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Name          string   `json:"name"`
		Scopes        []string `json:"scopes"`
		ExpiresInDays *int     `json:"expires_in_days"`
		// Refused: projects were removed. Ignoring it would make a token
		// that reaches more than the caller asked for.
		ProjectID *string `json:"project_id"`
		// Binds the token to one brand, by id or handle. Fixed for the
		// token's life.
		Brand *string `json:"brand"`
		tokenLimits
	}
	if !decode(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	if len(req.Scopes) == 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "scopes must list at least one scope")
		return
	}
	for _, sc := range req.Scopes {
		if !auth.ValidScope(sc) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "unknown scope "+sc)
			return
		}
		if slices.Contains(auth.PersonOnly, sc) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "an API token can't hold "+sc+": only a signed-in person removes people or changes roles")
			return
		}
		// A token can never hold more than the person or token creating it.
		if !a.has(sc) {
			writeError(w, r, http.StatusForbidden, "forbidden", "you cannot grant scope "+sc)
			return
		}
	}
	if req.ProjectID != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "project_id: projects were removed; limit the token to a brand with brand instead")
		return
	}
	var brand *string
	if req.Brand != nil {
		id, err := s.brandIDFor(r.Context(), a.WorkspaceID, *req.Brand)
		if errors.Is(err, errNotFound) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "brand not found: brand is a brand's id or handle")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		brand = &id
	}
	var expiresAt *time.Time
	if req.ExpiresInDays != nil {
		if *req.ExpiresInDays < 1 || *req.ExpiresInDays > 3650 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "expires_in_days must be between 1 and 3650")
			return
		}
		t := time.Now().Add(time.Duration(*req.ExpiresInDays) * 24 * time.Hour)
		expiresAt = &t
	}

	secret, hash := auth.NewToken(auth.PrefixAPIToken)
	prefix := secret[:len(auth.PrefixAPIToken)+6]
	ctx := r.Context()
	var createdBy *string
	if a.UserID != "" {
		createdBy = &a.UserID
	}
	var v tokenView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		prio, err := req.check(ctx, tx, a)
		if err != nil {
			return err
		}
		var actorID string
		if err := tx.QueryRow(ctx, `INSERT INTO actors (workspace_id, type, display_name) VALUES ($1, 'api_token', $2) RETURNING id`, a.WorkspaceID, name).Scan(&actorID); err != nil {
			return err
		}
		v, err = scanToken(tx.QueryRow(ctx, `
			INSERT INTO api_tokens (workspace_id, actor_id, name, prefix, token_hash, scopes, created_by, expires_at,
			                        daily_send_budget, max_priority, may_take_mailboxes, default_team_id, brand_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, COALESCE($11, false), $12, $13)
			RETURNING `+tokenColumns,
			a.WorkspaceID, actorID, name, prefix, hash, req.Scopes, createdBy, expiresAt,
			req.DailySendBudget.Value, prio, req.MayTakeMailboxes, req.DefaultTeamID.Value, brand))
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "token.created", "api_token", v.ID, map[string]any{"name": name, "scopes": req.Scopes, "brand_id": brand})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"api_token": v, "token": secret})
}

// updateTokenLimits changes a token's name, permissions, and allocation and
// sending limits. Only the fields sent change. A connected app's token is
// changed under Connect your AI (updateAgentApp) instead.
func (s *Server) updateTokenLimits(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "token not found")
		return
	}
	if id == a.TokenID {
		writeError(w, r, http.StatusForbidden, "forbidden", "a token cannot change itself")
		return
	}
	var req struct {
		Name   *string  `json:"name"`
		Scopes []string `json:"scopes"`
		tokenLimits
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name can't be empty")
		return
	}
	if req.Scopes != nil && len(req.Scopes) == 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "scopes must list at least one scope; revoke the token to take everything away")
		return
	}
	for _, sc := range req.Scopes {
		if !auth.ValidScope(sc) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "unknown scope "+sc)
			return
		}
		if slices.Contains(auth.PersonOnly, sc) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "an API token can't hold "+sc+": only a signed-in person removes people or changes roles")
			return
		}
	}
	limits := req.DailySendBudget.Set || req.MaxPriority.Set || req.MayTakeMailboxes != nil || req.DefaultTeamID.Set
	ctx := r.Context()
	var v tokenView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var cur tokenLimitsView
		var name, actorID string
		var scopes []string
		var app bool
		err := tx.QueryRow(ctx, `SELECT name, actor_id, scopes, EXISTS (SELECT 1 FROM oauth_grants g WHERE g.api_token_id = api_tokens.id),
			daily_send_budget, CASE max_priority WHEN 0 THEN 'low' WHEN 1 THEN 'normal' WHEN 2 THEN 'high' END, may_take_mailboxes, default_team_id::text
			FROM api_tokens WHERE id = $1 AND workspace_id = $2 AND revoked_at IS NULL FOR UPDATE`, id, a.WorkspaceID).
			Scan(&name, &actorID, &scopes, &app, &cur.DailySendBudget, &cur.MaxPriority, &cur.MayTakeMailboxes, &cur.DefaultTeamID)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if app && (req.Scopes != nil || req.Name != nil) {
			return errInvalid("this token belongs to a connected app: change what it may do under Connect your AI")
		}
		// Taking a permission away is always allowed; adding one needs the
		// person or token making the change to have it.
		for _, sc := range req.Scopes {
			if !slices.Contains(scopes, sc) && !a.has(sc) {
				return errDenied("you cannot grant scope " + sc)
			}
		}
		// Check the limits the token will end up with, not just the fields sent.
		next := tokenLimits{DailySendBudget: req.DailySendBudget, MaxPriority: req.MaxPriority, MayTakeMailboxes: req.MayTakeMailboxes, DefaultTeamID: req.DefaultTeamID}
		if !next.DailySendBudget.Set {
			next.DailySendBudget.Value = cur.DailySendBudget
		}
		if !next.MaxPriority.Set {
			next.MaxPriority.Value = cur.MaxPriority
		}
		if next.MayTakeMailboxes == nil {
			next.MayTakeMailboxes = &cur.MayTakeMailboxes
		}
		if !next.DefaultTeamID.Set {
			next.DefaultTeamID.Value = cur.DefaultTeamID
		}
		var prio *int
		if limits {
			if prio, err = next.check(ctx, tx, a); err != nil {
				return err
			}
		} else if cur.MaxPriority != nil {
			i := slices.Index(priorityNames, *cur.MaxPriority)
			prio = &i
		}
		newName, newScopes := name, scopes
		if req.Name != nil {
			newName = strings.TrimSpace(*req.Name)
		}
		if req.Scopes != nil {
			newScopes = slices.Compact(slices.Sorted(slices.Values(req.Scopes)))
		}
		v, err = scanToken(tx.QueryRow(ctx, `
			UPDATE api_tokens SET name = $3, scopes = $4, daily_send_budget = $5, max_priority = $6, may_take_mailboxes = $7, default_team_id = $8
			WHERE id = $1 AND workspace_id = $2 RETURNING `+tokenColumns,
			id, a.WorkspaceID, newName, newScopes, next.DailySendBudget.Value, prio, *next.MayTakeMailboxes, next.DefaultTeamID.Value))
		if err != nil {
			return err
		}
		if newName != name {
			// The audit log shows the token's actor by name.
			if _, err := tx.Exec(ctx, `UPDATE actors SET display_name = $2 WHERE id = $1`, actorID, newName); err != nil {
				return err
			}
		}
		if newName != name || !slices.Equal(slices.Sorted(slices.Values(scopes)), newScopes) {
			added := slices.DeleteFunc(slices.Clone(newScopes), func(sc string) bool { return slices.Contains(scopes, sc) })
			removed := slices.DeleteFunc(slices.Clone(scopes), func(sc string) bool { return slices.Contains(newScopes, sc) })
			if err := record(ctx, tx, &a.principal, a.WorkspaceID, "token.updated", "api_token", id, map[string]any{
				"renamed": newName != name, "scopes": newScopes, "scopes_added": added, "scopes_removed": removed}); err != nil {
				return err
			}
		}
		if !limits {
			return nil
		}
		if err := allocation.Request(ctx, tx, a.WorkspaceID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "token.limits_updated", "api_token", id, map[string]any{
			"daily_send_budget": v.DailySendBudget, "max_priority": v.MaxPriority, "may_take_mailboxes": v.MayTakeMailboxes, "default_team_id": v.DefaultTeamID})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "token not found")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE api_tokens SET revoked_at = now() WHERE id = $1 AND workspace_id = $2 AND revoked_at IS NULL`, id, a.WorkspaceID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "token.revoked", "api_token", id, nil)
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "token not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Audit log ----

type auditView struct {
	ID         int64          `json:"id"`
	ActorID    *string        `json:"actor_id"`
	ActorType  string         `json:"actor_type"`
	ActorName  *string        `json:"actor_name"`
	Action     string         `json:"action"`
	TargetType *string        `json:"target_type"`
	TargetID   *string        `json:"target_id"`
	RequestID  *string        `json:"request_id"`
	Data       map[string]any `json:"data"`
	CreatedAt  time.Time      `json:"created_at"`
	// The brand of the changed record, when it has one.
	BrandID *string `json:"brand_id"`
}

// listAudit returns events newest first. Page with ?before=<id>&limit=<n>,
// and narrow to a brand with ?brand=.
func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit must be 1 to 200")
			return
		}
		limit = n
	}
	var before int64 = 1<<63 - 1
	if v := r.URL.Query().Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "before must be an event id")
			return
		}
		before = n
	}
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID, before, limit}
	rows, err := s.pool.Query(r.Context(), `
		SELECT e.id, e.actor_id::text, e.actor_type, a.display_name, e.action, e.target_type, e.target_id, e.request_id, e.data, e.created_at,
		       e.brand_id::text
		FROM audit_events e LEFT JOIN actors a ON a.id = e.actor_id
		WHERE e.workspace_id = $1 AND e.id < $2`+sel.sql("e.brand_id", &args)+`
		ORDER BY e.id DESC LIMIT $3`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[auditView])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *int64
	if len(list) == limit {
		next = &list[len(list)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}

// ---- Shared helpers ----

func validEmail(s string) (string, bool) {
	s = strings.TrimSpace(s)
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s || len(s) > 254 {
		return "", false
	}
	return s, true
}

// markVerified records that userID proved they read email (a link they
// opened, or Google vouching for it). It does nothing when email is no
// longer theirs.
func markVerified(ctx context.Context, tx pgx.Tx, userID, email string) error {
	tag, err := tx.Exec(ctx, `UPDATE users SET email_verified_at = now() WHERE id = $1 AND lower(email) = lower($2) AND email_verified_at IS NULL`, userID, email)
	if err == nil && tag.RowsAffected() > 0 {
		emitUser(ctx, "user.email_verified", userID, map[string]any{"via": accountVia(ctx)})
	}
	return err
}

func createUser(ctx context.Context, tx pgx.Tx, email, name, hash string) (userID, actorID string, err error) {
	if err = tx.QueryRow(ctx, `INSERT INTO actors (type, display_name) VALUES ('human', $1) RETURNING id`, name).Scan(&actorID); err != nil {
		return
	}
	err = tx.QueryRow(ctx, `INSERT INTO users (actor_id, email, name, password_hash) VALUES ($1, $2, $3, $4) RETURNING id`,
		actorID, email, name, hash).Scan(&userID)
	if err == nil {
		method := "password"
		if hash == "" {
			method = "google"
		}
		emitUser(ctx, "user.signed_up", userID, map[string]any{"via": accountVia(ctx), "method": method})
	}
	return
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func createWorkspace(ctx context.Context, tx pgx.Tx, name, ownerID string) (string, error) {
	base := slugFrom(name)
	var id string
	for i := 0; ; i++ {
		slug := base
		if i > 0 {
			suffix, _ := auth.NewToken("")
			slug = strings.Trim(base[:min(len(base), 33)], "-") + "-" + nonSlug.ReplaceAllString(strings.ToLower(suffix), "")[:6]
		}
		_, err := tx.Exec(ctx, `SAVEPOINT slug`)
		if err != nil {
			return "", err
		}
		err = tx.QueryRow(ctx, `INSERT INTO workspaces (name, slug) VALUES ($1, $2) RETURNING id`, name, slug).Scan(&id)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && i < 5 {
			if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT slug`); err != nil {
				return "", err
			}
			continue
		}
		if err != nil {
			return "", err
		}
		break
	}
	// New workspaces start with the Deal type, like Attio's Deals.
	if _, err := tx.Exec(ctx, `SELECT outflare_deal_type($1)`, id); err != nil {
		return "", err
	}
	// The slug may be one another workspace had; it now leads here.
	if _, err := tx.Exec(ctx, `DELETE FROM workspace_slug_history WHERE slug = (SELECT slug FROM workspaces WHERE id = $1)`, id); err != nil {
		return "", err
	}
	if ownerID == "" {
		return id, nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO memberships (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`, id, ownerID)
	return id, err
}

func createSession(ctx context.Context, tx pgx.Tx, userID string, r *http.Request) (string, error) {
	secret, hash := auth.NewToken(auth.PrefixSession)
	_, err := tx.Exec(ctx, `INSERT INTO sessions (token_hash, user_id, user_agent, ip, expires_at) VALUES ($1, $2, $3, $4, $5)`,
		hash, userID, truncate(r.UserAgent(), 512), clientIP(r), time.Now().Add(sessionLifetime))
	if err == nil {
		emitUser(ctx, "user.signed_in", userID, map[string]any{"via": accountVia(ctx)})
	}
	return secret, err
}

// clientIPHeader names the header a trusted proxy puts the visitor's
// address in (Options.ClientIPHeader); empty uses the connection's address.
var clientIPHeader string

// ClientIP is the visitor's address, for rate limits and session records.
// Behind a proxy (Fly.io, Cloudflare) every connection comes from the proxy,
// so set Options.ClientIPHeader to the header it sends, such as
// Fly-Client-IP or CF-Connecting-IP. Only do that behind such a proxy: a
// direct visitor could send the header themselves.
func ClientIP(r *http.Request) string { return clientIP(r) }

func clientIP(r *http.Request) string {
	if clientIPHeader != "" {
		if v := strings.TrimSpace(r.Header.Get(clientIPHeader)); v != "" {
			if i := strings.IndexByte(v, ','); i >= 0 {
				v = strings.TrimSpace(v[:i])
			}
			return truncate(v, 64)
		}
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
