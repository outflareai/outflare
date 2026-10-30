package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/mailbox"
	"github.com/outflareai/outflare/engine/internal/missions"
	"github.com/outflareai/outflare/engine/internal/track"
)

const (
	sessionCookie   = "outflare_session"
	signedInCookie  = "outflare_signed_in"
	sessionLifetime = 30 * 24 * time.Hour
	// viewCookie holds a support view's secret beside the staff member's
	// own session cookie; a version without support views ignores it.
	viewCookie = "outflare_view"
)

// principal is who is making a request.
type principal struct {
	ActorID   string
	ActorType string // "human" or "api_token"

	// Human sessions.
	UserID    string
	SessionID string

	// API tokens are bound to one workspace and carry explicit scopes.
	TokenID          string
	TokenWorkspaceID string
	TokenScopes      []string
	TokenOwnerID     string // the user who made the token, if they still exist
	// Set when the token is bound to one brand.
	TokenBrandID *string
	// Set when the token is a connected app's (an OAuth grant): its
	// permissions are changed under Connect your AI, not API tokens.
	TokenGrantID string
	// The token makes changes only inside a mission (Outflare-Mission).
	TokenRequiresMission bool

	// A support view: a staff member (ViewerID, signed in with SessionID)
	// sees UserID's account read-only, without inbox or email content.
	ViewID   string
	ViewerID string
}

// access is a principal's resolved rights in one workspace.
type access struct {
	principal
	WorkspaceID string
	Role        string // empty for API tokens; the starting role beside a custom one
	// RoleID is set when the person is on a role the workspace made.
	RoleID *string
	Scopes []string
	// Brands is set for a brand-limited caller: an operator or viewer kept
	// to some brands, or a brand token. It sees those brands' records and
	// the shared ones, changes only its brands' records, and is let in
	// only on routes registered with inBrand. Nil means every
	// brand.
	Brands []string
	// Test is true in a test workspace: mailboxes are simulated and
	// purchases are refused.
	Test bool
}

// sees reports whether the caller may see a record owned by brand (nil for
// shared).
func (a *access) sees(brand *string) bool {
	return a.Brands == nil || brand == nil || slices.Contains(a.Brands, *brand)
}

// writes reports whether the caller may change a record owned by brand. A
// brand-limited caller only reads shared records, since changing them would
// change every brand.
func (a *access) writes(brand *string) bool {
	return a.Brands == nil || (brand != nil && slices.Contains(a.Brands, *brand))
}

// brandLimitedMessage is the 403 a brand-limited caller gets on a route that
// needs every brand.
const brandLimitedMessage = "this endpoint needs access to every brand; you are limited to some brands"

func (a *access) has(scope string) bool { return auth.Grants(a.Scopes, scope) }

// missingScope says what is missing and how to get it, so a 403 is
// actionable without reading the docs. A connected app gets the page where
// its person turns the permission on.
func (s *Server) missingScope(a *access, scope string) string {
	switch {
	case a.Role != "":
		return "missing scope " + scope + ": your role does not include it; a workspace admin can change your role, or what it allows under Settings → Members → Roles"
	case a.TokenGrantID != "" && slices.Contains(agentWithheld, scope) && scope != auth.ScopeInfraApprove:
		return "missing scope " + scope + ": connected apps can never do this; a person does it in Outflare"
	case a.TokenGrantID != "":
		return "missing scope " + scope + ": this app was not allowed to do this; the person who connected it can turn it on under Connect your AI, " +
			s.publicURL + "/w/" + a.WorkspaceID + "/ai/" + a.TokenGrantID
	}
	return "missing scope " + scope + ": this token was not given it; add it to the token under API tokens, " + s.publicURL + "/w/" + a.WorkspaceID + "/tokens/" + a.TokenID
}

type ctxAccessKey struct{}
type ctxPrincipalKey struct{}

func accessFrom(ctx context.Context) *access {
	a, _ := ctx.Value(ctxAccessKey{}).(*access)
	return a
}

func principalFrom(ctx context.Context) *principal {
	p, _ := ctx.Value(ctxPrincipalKey{}).(*principal)
	return p
}

// authenticate resolves the request's credentials. A nil principal with a nil
// error means the request is anonymous. A support view in progress makes the
// staff member's session the viewed person's, read-only.
func (s *Server) authenticate(r *http.Request) (*principal, error) {
	p, err := s.authenticateSession(r)
	if err != nil || p == nil || p.SessionID == "" {
		return p, err
	}
	c, err := r.Cookie(viewCookie)
	if err != nil || !strings.HasPrefix(c.Value, auth.PrefixSupportView) {
		return p, nil
	}
	return s.supportViewFor(r.Context(), p, c.Value)
}

// authenticateSession is authenticate without support views: always the
// person who signed in.
func (s *Server) authenticateSession(r *http.Request) (*principal, error) {
	var secret string
	fromCookie := false
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		secret = strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	} else if c, err := r.Cookie(sessionCookie); err == nil {
		secret = c.Value
		fromCookie = true
	}
	if secret == "" {
		return nil, nil
	}
	ctx := r.Context()
	hash := auth.HashToken(secret)

	switch {
	case strings.HasPrefix(secret, auth.PrefixAPIToken) && !fromCookie:
		p := &principal{ActorType: "api_token"}
		err := s.pool.QueryRow(ctx, `
			UPDATE api_tokens SET last_used_at = now()
			WHERE token_hash = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())
			  -- A disabled account's tokens stop with it.
			  AND NOT EXISTS (SELECT 1 FROM users u WHERE u.id = api_tokens.created_by AND u.disabled_at IS NOT NULL)
			RETURNING id, actor_id, workspace_id, scopes, brand_id::text, coalesce(created_by::text, ''),
				coalesce((SELECT g.id::text FROM oauth_grants g WHERE g.api_token_id = api_tokens.id), ''), require_mission`, hash,
		).Scan(&p.TokenID, &p.ActorID, &p.TokenWorkspaceID, &p.TokenScopes, &p.TokenBrandID, &p.TokenOwnerID, &p.TokenGrantID, &p.TokenRequiresMission)
		p.TokenScopes = auth.Rename(p.TokenScopes)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return p, err

	case strings.HasPrefix(secret, auth.PrefixSession):
		p := &principal{ActorType: "human"}
		err := s.pool.QueryRow(ctx, `
			UPDATE sessions s SET last_seen_at = now()
			FROM users u
			WHERE s.token_hash = $1 AND s.expires_at > now() AND u.id = s.user_id AND u.disabled_at IS NULL
			RETURNING s.id, u.id, u.actor_id`, hash,
		).Scan(&p.SessionID, &p.UserID, &p.ActorID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return p, err
	}
	return nil, nil
}

// routeAccess is who a route lets in, as api/openapi.yaml states it in each
// operation's x-outflare-access. The wrappers below note it on the request
// when the package's tests ask, so the tests can check the document.
type routeAccess struct {
	Scope         string `yaml:"scope,omitempty"`
	BrandLimited  bool   `yaml:"brand_limited,omitempty"`
	SignedIn      bool   `yaml:"signed_in,omitempty"`
	Authenticated bool   `yaml:"authenticated,omitempty"` // any caller: a session or any token
}

type ctxRouteAccessKey struct{}

func noteAccess(r *http.Request, a routeAccess) {
	if n, ok := r.Context().Value(ctxRouteAccessKey{}).(*routeAccess); ok {
		*n = a
	}
}

// requireUser runs next only for a signed-in human.
func (s *Server) requireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		noteAccess(r, routeAccess{SignedIn: true})
		p, ok := s.authenticated(w, r)
		if !ok {
			return
		}
		if p.UserID == "" {
			writeError(w, r, http.StatusForbidden, "forbidden", "this endpoint needs a signed-in user, not an API token")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxPrincipalKey{}, p)))
	}
}

// inWorkspace runs next only when the caller has scope in the {ws} workspace.
// Brand-limited callers are refused: the route has not been made
// brand-aware.
func (s *Server) inWorkspace(scope string, next http.HandlerFunc) http.HandlerFunc {
	return s.workspaceRoute(scope, false, next)
}

// inBrand is inWorkspace for routes that also serve brand-limited callers
// (access.Brands). The handler must keep them to their brands: filter
// lists, return 404 for other brands' records, and refuse changes outside
// them (access.sees, access.writes, brandFilter).
func (s *Server) inBrand(scope string, next http.HandlerFunc) http.HandlerFunc {
	return s.workspaceRoute(scope, true, next)
}

func (s *Server) workspaceRoute(scope string, brandAware bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		noteAccess(r, routeAccess{Scope: scope, BrandLimited: brandAware})
		p, ok := s.authenticated(w, r)
		if !ok {
			return
		}
		// {ws} is the workspace ID or its slug.
		ws, err := s.workspaceIDFor(r.Context(), r.PathValue("ws"))
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if ws == "" {
			writeError(w, r, http.StatusNotFound, "not_found", "workspace not found")
			return
		}
		noteWorkspace(r, ws)
		a := &access{principal: *p, WorkspaceID: ws}
		if p.TokenID != "" {
			if p.TokenWorkspaceID != ws {
				writeError(w, r, http.StatusNotFound, "not_found", "workspace not found")
				return
			}
			a.Scopes = p.TokenScopes
			if p.TokenBrandID != nil {
				a.Brands = []string{*p.TokenBrandID}
			}
		} else {
			var brands, stored []string
			err := s.pool.QueryRow(r.Context(), `SELECT role, brand_ids::text[], role_id::text, scopes FROM (`+workspaceRoles+`) r ORDER BY rank DESC, cardinality(brand_ids) LIMIT 1`, ws, p.UserID).Scan(&a.Role, &brands, &a.RoleID, &stored)
			if errors.Is(err, pgx.ErrNoRows) {
				// Same answer as a missing workspace, so ids cannot be probed.
				writeError(w, r, http.StatusNotFound, "not_found", "workspace not found")
				return
			}
			if err != nil {
				s.internal(w, r, err)
				return
			}
			a.Scopes = effectiveScopes(a.Role, stored)
			if len(brands) > 0 && auth.BrandLimitable(a.Scopes) {
				a.Brands = brands
			}
		}
		if a.Brands != nil && !brandAware {
			writeError(w, r, http.StatusForbidden, "forbidden", brandLimitedMessage)
			return
		}
		if p.ViewID != "" {
			a.Scopes = viewScopes(a.Scopes)
			if scope != "" && !a.has(scope) {
				writeError(w, r, http.StatusForbidden, "support_view_hidden", "this is hidden while staff view this account")
				return
			}
		}
		// No scope: the handler checks what the caller may read itself.
		if scope != "" && !a.has(scope) {
			writeError(w, r, http.StatusForbidden, "forbidden", s.missingScope(a, scope))
			return
		}
		if a.Test, err = mailbox.IsTestWorkspace(r.Context(), s.pool, ws); err != nil {
			s.internal(w, r, err)
			return
		}
		if a.Test {
			// Every answer from a test workspace says so, so a client can
			// tell test data from live data without another call.
			w.Header().Set("Outflare-Test-Workspace", "true")
		}
		noteAccessScope(r, a)
		r = r.WithContext(context.WithValue(r.Context(), ctxAccessKey{}, a))
		m := r.Header.Get(missions.Header)
		if p.TokenRequiresMission && m == "" && r.Method != http.MethodGet && r.Method != http.MethodHead && !isMissionRoute(r) && !readsOnly(r) {
			// Its changes wait for a mission a person approved, where they
			// follow the mission's mode: dry run, approvals or running.
			msg := "this app makes changes only inside a mission: start one (POST /v1/workspaces/{ws}/missions, or create_mission), and once a person approves it send its id in the " + missions.Header + " header"
			if !a.has(auth.ScopeMissionsRun) {
				msg = "this app makes changes only inside a mission, and it may not run missions; the person who connected it can turn on Missions, or turn off Only inside missions, under Connect your AI"
				if a.TokenGrantID != "" {
					msg += ", " + s.publicURL + "/w/" + a.WorkspaceID + "/ai/" + a.TokenGrantID
				}
			}
			writeError(w, r, http.StatusForbidden, "mission_required", msg)
			return
		}
		if m != "" && !isMissionRoute(r) && !readsOnly(r) {
			s.idempotent(w, r, a, func(w http.ResponseWriter, r *http.Request) { s.missionGate(w, r, a, m, next) })
			return
		}
		s.idempotent(w, r, a, next)
	}
}

// workspaceRoles lists a user's role in a workspace ($1) for user $2: their
// membership. Callers take the highest rank.
const workspaceRoles = `
	SELECT m.role, ` + roleRankSQL + ` AS rank, m.brand_ids, m.role_id, ` + memberRoleScopes + ` AS scopes FROM memberships m WHERE m.workspace_id = $1 AND m.user_id = $2`

const roleRankSQL = `CASE role WHEN 'owner' THEN 3 WHEN 'admin' THEN 2 WHEN 'operator' THEN 1 ELSE 0 END`

func (s *Server) authenticated(w http.ResponseWriter, r *http.Request) (*principal, bool) {
	// Browsers mark cross-site requests; refuse them so a session cookie can
	// never be ridden by another site (defence in depth on top of SameSite).
	// Same-site counts too when the request rides the cookie: a sibling
	// subdomain (the operator's website, a preview) is another site's code.
	switch site := r.Header.Get("Sec-Fetch-Site"); {
	case r.Method == http.MethodGet:
	case site == "cross-site", site == "same-site" && r.Header.Get("Authorization") == "":
		writeError(w, r, http.StatusForbidden, "forbidden", "cross-site request refused")
		return nil, false
	}
	// An approved mission action runs as the agent that asked for it.
	if p, ok := r.Context().Value(ctxInjectedKey{}).(*principal); ok {
		notePrincipal(r, p)
		return p, true
	}
	p, err := s.authenticate(r)
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	s.syncSignedInHint(w, r, p)
	if p == nil {
		writeError(w, r, http.StatusUnauthorized, "unauthenticated", "sign in or send a valid API token")
		return nil, false
	}
	notePrincipal(r, p)
	if p.ViewerID != "" {
		track.Credit(r.Context(), p.ViewerID)
	}
	if p.ViewID != "" && !viewAllows(r) {
		writeError(w, r, http.StatusForbidden, "support_view_read_only", "you are viewing this account as staff, which is read-only; end the view to make changes")
		return nil, false
	}
	if p.TokenID != "" && !s.rateLimit.allow(w, r, p.TokenID) {
		return nil, false
	}
	return p, true
}

// record writes an audit event for the request's actor.
func record(ctx context.Context, q pgx.Tx, p *principal, workspaceID, action, targetType, targetID string, data map[string]any) error {
	return recordWithProps(ctx, q, p, workspaceID, action, targetType, targetID, data, nil)
}

// recordWithProps is record with extra product event properties: ids,
// enums and counts only, never names or text.
func recordWithProps(ctx context.Context, q pgx.Tx, p *principal, workspaceID, action, targetType, targetID string, data, props map[string]any) error {
	e := audit.Event{
		WorkspaceID: workspaceID,
		Action:      action,
		TargetType:  targetType,
		TargetID:    targetID,
		RequestID:   RequestID(ctx),
		Data:        data,
		Props:       props,
		ActorType:   "system",
	}
	if p != nil {
		e.ActorID, e.ActorType, e.UserID = p.ActorID, p.ActorType, p.personID()
	}
	if m := missions.ID(ctx); m != "" {
		if e.Data == nil {
			e.Data = map[string]any{}
		}
		e.Data["mission_id"] = m
	}
	return audit.Record(ctx, q, e)
}

// decode reads a JSON body into v, rejecting unknown fields and bodies over 1 MiB.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeLarge(w, r, v, 1<<20)
}

// decodeLarge is decode with a custom size limit, for bulk endpoints.
func decodeLarge(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		writeError(w, r, http.StatusUnsupportedMediaType, "invalid_request", "send a JSON body with Content-Type: application/json")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid JSON: "+err.Error())
		return false
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "body must be a single JSON object")
		return false
	}
	return true
}

func (s *Server) internal(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("internal error", "err", err, "request_id", RequestID(r.Context()))
	writeError(w, r, http.StatusInternalServerError, "internal", "internal error")
}

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
				return false
			}
		}
	}
	return true
}

// loginLimiter slows password guessing and email floods: after max
// entries for a key within loginWindow, further attempts are refused until
// the window passes. Entries are rows in auth_attempts, so limits hold
// across restarts, deploys and replicas; keys are stored hashed, since they
// hold email and IP addresses. Without a database (some tests) it counts in
// memory.
type loginLimiter struct {
	pool *pgxpool.Pool
	name string // keeps the sign-in and email limits apart
	log  *slog.Logger

	mu       sync.Mutex
	failures map[string][]time.Time
}

const (
	loginMaxFailures = 10
	loginWindow      = 15 * time.Minute
	// Failed sign-ins from one network across every address, so nobody
	// can try one password on many accounts. High enough for an office.
	loginNetworkFailures = 100

	// Account emails asked for from one network in loginWindow.
	accountEmailsPerNetwork = 20
	accountEmailWindow      = loginWindow
)

func (l *loginLimiter) blocked(ctx context.Context, key string) bool {
	return l.over(ctx, key, loginMaxFailures)
}

func (l *loginLimiter) bucket(key string) []byte {
	h := sha256.Sum256([]byte(l.name + "\x00" + key))
	return h[:]
}

// over reports whether key has max or more entries in the window. A
// database failure lets the attempt through (and is logged): the limit is
// a brake, and the database being down stops sign-in anyway.
func (l *loginLimiter) over(ctx context.Context, key string, max int) bool {
	if l.pool == nil {
		l.mu.Lock()
		defer l.mu.Unlock()
		return len(l.recent(key)) >= max
	}
	var n int
	err := l.pool.QueryRow(ctx, `SELECT count(*) FROM auth_attempts WHERE bucket = $1 AND at > now() - make_interval(secs => $2)`,
		l.bucket(key), loginWindow.Seconds()).Scan(&n)
	if err != nil {
		l.logErr(ctx, err)
		return false
	}
	return n >= max
}

func (l *loginLimiter) fail(ctx context.Context, key string) {
	if l.pool == nil {
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.failures == nil {
			l.failures = map[string][]time.Time{}
		}
		l.failures[key] = append(l.recent(key), time.Now())
		return
	}
	// Each write also clears a few rows nobody needs any more.
	_, err := l.pool.Exec(ctx, `
		WITH old AS (DELETE FROM auth_attempts WHERE ctid IN (SELECT ctid FROM auth_attempts WHERE at < now() - interval '1 hour' LIMIT 100))
		INSERT INTO auth_attempts (bucket) VALUES ($1)`, l.bucket(key))
	if err != nil {
		l.logErr(ctx, err)
	}
}

func (l *loginLimiter) reset(ctx context.Context, key string) {
	if l.pool == nil {
		l.mu.Lock()
		defer l.mu.Unlock()
		delete(l.failures, key)
		return
	}
	if _, err := l.pool.Exec(ctx, `DELETE FROM auth_attempts WHERE bucket = $1`, l.bucket(key)); err != nil {
		l.logErr(ctx, err)
	}
}

func (l *loginLimiter) logErr(ctx context.Context, err error) {
	track.Fail(ctx, l.log, track.Event{Name: "auth.limit_unavailable", ActorType: "system", RequestID: RequestID(ctx),
		Props: map[string]any{"limit": l.name}}, "internal", err)
}

// recent must be called with mu held.
func (l *loginLimiter) recent(key string) []time.Time {
	cut := time.Now().Add(-loginWindow)
	ts := l.failures[key]
	i := 0
	for i < len(ts) && ts[i].Before(cut) {
		i++
	}
	return ts[i:]
}

// networkKey groups an IPv6 address by its /64, which one household or
// server usually holds whole, so rotating addresses inside it doesn't reset
// a limit. IPv4 addresses stay as they are.
func networkKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	if a = a.Unmap(); !a.Is6() {
		return a.String()
	}
	p, err := a.Prefix(64)
	if err != nil {
		return ip
	}
	return p.String()
}
