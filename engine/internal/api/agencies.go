package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// Agencies run outbound for many clients. Each client is an ordinary
// workspace; agency members get their agency role in every client, and
// agency tokens reach every client, so one person or agent can run them all.

func (s *Server) routesAgencies(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/agencies", s.requireUser(s.createAgency))
	mux.HandleFunc("GET /v1/agencies", s.requireUser(s.listAgencies))
	mux.HandleFunc("GET /v1/agencies/{ag}", s.inAgency(auth.RoleViewer, true, s.getAgency))
	mux.HandleFunc("PATCH /v1/agencies/{ag}", s.inAgency(auth.RoleAdmin, false, s.updateAgency))
	mux.HandleFunc("GET /v1/agencies/{ag}/clients", s.inAgency(auth.RoleViewer, true, s.listClients))
	mux.HandleFunc("POST /v1/agencies/{ag}/clients", s.inAgency(auth.RoleAdmin, true, s.addClient))
	mux.HandleFunc("DELETE /v1/agencies/{ag}/clients/{ws}", s.inAgency(auth.RoleOwner, false, s.removeClient))
	mux.HandleFunc("GET /v1/agencies/{ag}/members", s.inAgency(auth.RoleViewer, false, s.listAgencyMembers))
	mux.HandleFunc("POST /v1/agencies/{ag}/members", s.inAgency(auth.RoleAdmin, false, s.addAgencyMember))
	mux.HandleFunc("PATCH /v1/agencies/{ag}/members/{user}", s.inAgency(auth.RoleAdmin, false, s.updateAgencyMember))
	mux.HandleFunc("DELETE /v1/agencies/{ag}/members/{user}", s.inAgency(auth.RoleAdmin, false, s.removeAgencyMember))
	mux.HandleFunc("GET /v1/agencies/{ag}/api-tokens", s.inAgency(auth.RoleAdmin, false, s.listAgencyTokens))
	mux.HandleFunc("POST /v1/agencies/{ag}/api-tokens", s.inAgency(auth.RoleAdmin, false, s.createAgencyToken))
	mux.HandleFunc("DELETE /v1/agencies/{ag}/api-tokens/{id}", s.inAgency(auth.RoleAdmin, false, s.revokeAgencyToken))
	mux.HandleFunc("GET /v1/agencies/{ag}/events", s.inAgency(auth.RoleAdmin, false, s.listAgencyEvents))
}

type agencyAccess struct {
	principal
	AgencyID string
	Role     string // the person's agency role; empty for tokens
	// For agency tokens.
	CanCreateClients bool
}

type ctxAgencyKey struct{}

func agencyFrom(ctx context.Context) *agencyAccess {
	a, _ := ctx.Value(ctxAgencyKey{}).(*agencyAccess)
	return a
}

// inAgency lets through agency members with at least minRole and, when
// tokens is set, that agency's own tokens. Everyone else sees a 404 so
// agency ids cannot be probed.
func (s *Server) inAgency(minRole string, tokens bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		noteAccess(r, routeAccess{AgencyRole: minRole, AgencyTokens: tokens})
		p, ok := s.authenticated(w, r)
		if !ok {
			return
		}
		ag := r.PathValue("ag")
		if !validUUID(ag) {
			writeError(w, r, http.StatusNotFound, "not_found", "agency not found")
			return
		}
		a := &agencyAccess{principal: *p, AgencyID: ag}
		switch {
		case p.TokenAgencyID != "":
			if p.TokenAgencyID != ag {
				writeError(w, r, http.StatusNotFound, "not_found", "agency not found")
				return
			}
			if !tokens {
				writeError(w, r, http.StatusForbidden, "forbidden", "this needs a signed-in agency admin, not an API token")
				return
			}
			if err := s.pool.QueryRow(r.Context(), `SELECT can_create_clients FROM agency_tokens WHERE id = $1`, p.TokenID).Scan(&a.CanCreateClients); err != nil {
				s.internal(w, r, err)
				return
			}
		case p.UserID != "":
			err := s.pool.QueryRow(r.Context(), `SELECT role FROM agency_members WHERE agency_id = $1 AND user_id = $2`, ag, p.UserID).Scan(&a.Role)
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, r, http.StatusNotFound, "not_found", "agency not found")
				return
			}
			if err != nil {
				s.internal(w, r, err)
				return
			}
			if auth.RoleRank(a.Role) < auth.RoleRank(minRole) {
				writeError(w, r, http.StatusForbidden, "forbidden", "needs the agency "+minRole+" role")
				return
			}
		default:
			writeError(w, r, http.StatusNotFound, "not_found", "agency not found")
			return
		}
		ctx := context.WithValue(r.Context(), ctxPrincipalKey{}, p)
		next(w, r.WithContext(context.WithValue(ctx, ctxAgencyKey{}, a)))
	}
}

func recordAgency(ctx context.Context, tx pgx.Tx, a *agencyAccess, action, target string, data map[string]any) error {
	if data == nil {
		data = map[string]any{}
	}
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO agency_events (agency_id, actor_id, action, target, data) VALUES ($1, $2, $3, $4, $5)`,
		a.AgencyID, a.ActorID, action, target, b)
	return err
}

// ---- The agency ----

type agencyView struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	BrandName    string    `json:"brand_name"`
	LogoURL      string    `json:"logo_url"`
	AccentColor  string    `json:"accent_color"`
	SupportEmail string    `json:"support_email"`
	Clients      int       `json:"clients"`
	Role         string    `json:"role,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

const agencySelect = `
	SELECT a.id, a.name, a.brand_name, a.logo_url, a.accent_color, a.support_email,
	       (SELECT count(*)::int FROM workspaces w WHERE w.agency_id = a.id), '', a.created_at
	FROM agencies a`

func (s *Server) findAgency(ctx context.Context, id string) (agencyView, error) {
	rows, err := s.pool.Query(ctx, agencySelect+` WHERE a.id = $1`, id)
	if err != nil {
		return agencyView{}, err
	}
	return pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[agencyView])
}

func (s *Server) createAgency(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	var req struct {
		Name         string   `json:"name"`
		WorkspaceIDs []string `json:"workspace_ids"`
	}
	if !decode(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 100 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name must be 1 to 100 characters")
		return
	}
	for _, id := range req.WorkspaceIDs {
		if !validUUID(id) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "workspace_ids must be workspace ids")
			return
		}
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO agencies (name) VALUES ($1) RETURNING id`, name).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO agency_members (agency_id, user_id, role) VALUES ($1, $2, 'owner')`, id, p.UserID); err != nil {
			return err
		}
		a := &agencyAccess{principal: *p, AgencyID: id, Role: auth.RoleOwner}
		for _, ws := range req.WorkspaceIDs {
			if err := attachClient(ctx, tx, a, ws); err != nil {
				return err
			}
		}
		return recordAgency(ctx, tx, a, "agency.created", id, map[string]any{"name": name})
	})
	if !s.agencyErr(w, r, err) {
		return
	}
	v, err := s.findAgency(ctx, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	v.Role = auth.RoleOwner
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) listAgencies(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), `
		SELECT a.id, a.name, a.brand_name, a.logo_url, a.accent_color, a.support_email,
		       (SELECT count(*)::int FROM workspaces w WHERE w.agency_id = a.id), m.role, a.created_at
		FROM agencies a JOIN agency_members m ON m.agency_id = a.id
		WHERE m.user_id = $1 ORDER BY a.created_at`, p.UserID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[agencyView])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) getAgency(w http.ResponseWriter, r *http.Request) {
	a := agencyFrom(r.Context())
	v, err := s.findAgency(r.Context(), a.AgencyID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	v.Role = a.Role
	writeJSON(w, http.StatusOK, v)
}

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func (s *Server) updateAgency(w http.ResponseWriter, r *http.Request) {
	a := agencyFrom(r.Context())
	var req struct {
		Name         *string `json:"name"`
		BrandName    *string `json:"brand_name"`
		LogoURL      *string `json:"logo_url"`
		AccentColor  *string `json:"accent_color"`
		SupportEmail *string `json:"support_email"`
	}
	if !decode(w, r, &req) {
		return
	}
	trim := func(p *string) {
		if p != nil {
			*p = strings.TrimSpace(*p)
		}
	}
	trim(req.Name)
	trim(req.BrandName)
	trim(req.LogoURL)
	trim(req.AccentColor)
	trim(req.SupportEmail)
	switch {
	case req.Name != nil && (*req.Name == "" || len(*req.Name) > 100):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name must be 1 to 100 characters")
		return
	case req.BrandName != nil && len(*req.BrandName) > 60:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "brand_name must be at most 60 characters")
		return
	case req.LogoURL != nil && *req.LogoURL != "" && !httpsURL(*req.LogoURL):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "logo_url must be an https URL")
		return
	case req.AccentColor != nil && *req.AccentColor != "" && !hexColor.MatchString(*req.AccentColor):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "accent_color must look like #2563eb")
		return
	case req.SupportEmail != nil && *req.SupportEmail != "" && !isEmail(*req.SupportEmail):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "support_email must be an email address")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE agencies SET name = COALESCE($2, name), brand_name = COALESCE($3, brand_name), logo_url = COALESCE($4, logo_url),
				accent_color = COALESCE($5, accent_color), support_email = COALESCE($6, support_email), updated_at = now()
			WHERE id = $1`, a.AgencyID, req.Name, req.BrandName, req.LogoURL, req.AccentColor, req.SupportEmail); err != nil {
			return err
		}
		return recordAgency(ctx, tx, a, "agency.updated", a.AgencyID, map[string]any{
			"name": req.Name, "brand_name": req.BrandName, "logo_url": req.LogoURL, "accent_color": req.AccentColor, "support_email": req.SupportEmail,
		})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.getAgency(w, r)
}

func httpsURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && len(s) <= 500
}

func isEmail(s string) bool {
	_, ok := validEmail(s)
	return ok
}

// ---- Clients ----

type clientView struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Slug            string    `json:"slug"`
	MailboxesActive int       `json:"mailboxes_active"`
	MailboxesPaused int       `json:"mailboxes_paused"`
	ActiveSequences int       `json:"active_sequences"`
	Sent7d          int       `json:"sent_7d"`
	Replies7d       int       `json:"replies_7d"`
	Bounced7d       int       `json:"bounced_7d"`
	BounceRate7d    *float64  `json:"bounce_rate_7d"`
	ReplyRate7d     *float64  `json:"reply_rate_7d"`
	OpenReplies     int       `json:"open_replies"`
	CreatedAt       time.Time `json:"created_at"`
}

// listClients is the agency's cross-client view: how each client's
// outbound is doing this week, worst bounce rate first so problems surface.
func (s *Server) listClients(w http.ResponseWriter, r *http.Request) {
	a := agencyFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), `
		SELECT w.id, w.name, w.slug,
		       (SELECT count(*)::int FROM mailboxes m WHERE m.workspace_id = w.id AND m.status = 'active'),
		       (SELECT count(*)::int FROM mailboxes m WHERE m.workspace_id = w.id AND m.status = 'paused'),
		       (SELECT count(*)::int FROM sequences q WHERE q.workspace_id = w.id AND q.status = 'active'),
		       s.sent, s.replies, s.bounced,
		       CASE WHEN s.sent > 0 THEN round(100.0 * s.bounced / s.sent, 1)::float8 END,
		       CASE WHEN s.contacted > 0 THEN round(100.0 * s.replied / s.contacted, 1)::float8 END,
		       (SELECT count(*)::int FROM inbound_messages i WHERE i.workspace_id = w.id AND i.kind = 'reply' AND NOT i.done
		          AND (i.snoozed_until IS NULL OR i.snoozed_until <= now())),
		       w.created_at
		FROM workspaces w
		CROSS JOIN LATERAL (SELECT
		       (SELECT count(*)::int FROM messages x WHERE x.workspace_id = w.id AND x.status = 'sent' AND x.sent_at >= now() - interval '7 days') AS sent,
		       (SELECT count(*)::int FROM inbound_messages i WHERE i.workspace_id = w.id AND i.kind = 'reply' AND i.received_at >= now() - interval '7 days') AS replies,
		       (SELECT count(*)::int FROM enrollments e WHERE e.workspace_id = w.id AND e.status = 'bounced' AND e.updated_at >= now() - interval '7 days') AS bounced,
		       -- The reply rate is leads who replied over leads emailed, as everywhere else.
		       (SELECT count(DISTINCT x.lead_id)::int FROM messages x WHERE x.workspace_id = w.id AND x.status = 'sent' AND x.sent_at >= now() - interval '7 days') AS contacted,
		       (SELECT count(DISTINCT i.lead_id)::int FROM inbound_messages i WHERE i.workspace_id = w.id AND i.kind = 'reply' AND i.received_at >= now() - interval '7 days') AS replied
		) s
		WHERE w.agency_id = $1
		ORDER BY CASE WHEN s.sent > 0 THEN s.bounced::float8 / s.sent END DESC NULLS LAST, w.name`, a.AgencyID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[clientView])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// attachClient moves an existing workspace into the agency. Only a direct
// owner of the workspace can hand it to an agency.
func attachClient(ctx context.Context, tx pgx.Tx, a *agencyAccess, ws string) error {
	var owner bool
	var current *string
	err := tx.QueryRow(ctx, `
		SELECT w.agency_id, EXISTS (SELECT 1 FROM memberships m WHERE m.workspace_id = w.id AND m.user_id = $2 AND m.role = 'owner')
		FROM workspaces w WHERE w.id = $1 FOR UPDATE`, ws, a.UserID).Scan(&current, &owner)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !owner {
		return errInvalid("you can only add workspaces you own")
	}
	if err != nil {
		return err
	}
	if current != nil && *current != a.AgencyID {
		return errInvalid("that workspace already belongs to another agency")
	}
	if _, err := tx.Exec(ctx, `UPDATE workspaces SET agency_id = $2 WHERE id = $1`, ws, a.AgencyID); err != nil {
		return err
	}
	return recordAgency(ctx, tx, a, "agency.client_added", ws, map[string]any{"existing": true})
}

func (s *Server) addClient(w http.ResponseWriter, r *http.Request) {
	a := agencyFrom(r.Context())
	var req struct {
		Name        string `json:"name"`
		WorkspaceID string `json:"workspace_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if (name == "") == (req.WorkspaceID == "") {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send name to create a client workspace, or workspace_id to add one you own")
		return
	}
	if a.TokenID != "" && (req.WorkspaceID != "" || !a.CanCreateClients) {
		writeError(w, r, http.StatusForbidden, "forbidden", "this token may not create client workspaces")
		return
	}
	if len(name) > 100 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name must be at most 100 characters")
		return
	}
	if req.WorkspaceID != "" && !validUUID(req.WorkspaceID) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "workspace_id must be a workspace id")
		return
	}
	ctx := r.Context()
	id := req.WorkspaceID
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if id != "" {
			return attachClient(ctx, tx, a, id)
		}
		// A new client has no direct members: the agency runs it, and its
		// people get in through their agency role.
		var err error
		if id, err = createWorkspace(ctx, tx, name, ""); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE workspaces SET agency_id = $2 WHERE id = $1`, id, a.AgencyID); err != nil {
			return err
		}
		if err := record(ctx, tx, &a.principal, id, "workspace.created", "workspace", id, map[string]any{"name": name, "agency_id": a.AgencyID}); err != nil {
			return err
		}
		return recordAgency(ctx, tx, a, "agency.client_added", id, map[string]any{"name": name})
	})
	if !s.agencyErr(w, r, err) {
		return
	}
	v := workspaceView{BrandIDs: []string{}}
	if err := s.pool.QueryRow(ctx, `SELECT id, name, slug FROM workspaces WHERE id = $1`, id).Scan(&v.ID, &v.Name, &v.Slug); err != nil {
		s.internal(w, r, err)
		return
	}
	v.Role = a.Role
	writeJSON(w, http.StatusCreated, v)
}

// removeClient takes a workspace out of the agency. The workspace and its
// data stay; agency people lose access unless they are direct members. If
// nobody would own it, the person removing it becomes its owner.
func (s *Server) removeClient(w http.ResponseWriter, r *http.Request) {
	a := agencyFrom(r.Context())
	ws := r.PathValue("ws")
	if !validUUID(ws) {
		writeError(w, r, http.StatusNotFound, "not_found", "client not found")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE workspaces SET agency_id = NULL WHERE id = $1 AND agency_id = $2`, ws, a.AgencyID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO memberships (workspace_id, user_id, role)
			SELECT $1, $2, 'owner' WHERE NOT EXISTS (SELECT 1 FROM memberships WHERE workspace_id = $1 AND role = 'owner')
			ON CONFLICT (workspace_id, user_id) DO UPDATE SET role = 'owner'`, ws, a.UserID); err != nil {
			return err
		}
		return recordAgency(ctx, tx, a, "agency.client_removed", ws, nil)
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "client not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// agencyErr writes the response for a failed agency change and reports
// whether the request may carry on.
func (s *Server) agencyErr(w http.ResponseWriter, r *http.Request, err error) bool {
	var inv errInvalid
	switch {
	case err == nil:
		return true
	case errors.As(err, &inv):
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
	default:
		s.internal(w, r, err)
	}
	return false
}

// ---- Members ----

func (s *Server) listAgencyMembers(w http.ResponseWriter, r *http.Request) {
	a := agencyFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), `
		SELECT u.id, u.email, u.name, m.role, m.created_at FROM agency_members m JOIN users u ON u.id = m.user_id
		WHERE m.agency_id = $1 ORDER BY m.created_at`, a.AgencyID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	type agencyMember struct {
		UserID    string    `json:"user_id"`
		Email     string    `json:"email"`
		Name      string    `json:"name"`
		Role      string    `json:"role"`
		CreatedAt time.Time `json:"created_at"`
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[agencyMember])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// canManageAgency mirrors canManage for workspaces: nobody acts above their
// own rank, and only owners make or touch owners.
func canManageAgency(a *agencyAccess, target, newRole string) bool {
	rank := auth.RoleRank(a.Role)
	if auth.RoleRank(target) > rank || (newRole != "" && auth.RoleRank(newRole) > rank) {
		return false
	}
	return !((target == auth.RoleOwner || newRole == auth.RoleOwner) && rank < auth.RoleRank(auth.RoleOwner))
}

var roles = []string{auth.RoleOwner, auth.RoleAdmin, auth.RoleOperator, auth.RoleViewer}

func (s *Server) addAgencyMember(w http.ResponseWriter, r *http.Request) {
	a := agencyFrom(r.Context())
	var req struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if !decode(w, r, &req) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if !slices.Contains(roles, req.Role) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "role must be owner, admin, operator or viewer")
		return
	}
	if !canManageAgency(a, "", req.Role) {
		writeError(w, r, http.StatusForbidden, "forbidden", "you cannot give someone a role above your own")
		return
	}
	ctx := r.Context()
	var userID string
	err := s.pool.QueryRow(ctx, `SELECT id FROM users WHERE lower(email) = $1 AND disabled_at IS NULL`, email).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "no_account", "no one has an account with that email yet; invite them to any workspace first, then add them here")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO agency_members (agency_id, user_id, role) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, a.AgencyID, userID, req.Role)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errInvalid("they are already in this agency")
		}
		return recordAgency(ctx, tx, a, "agency.member_added", userID, map[string]any{"email": email, "role": req.Role})
	})
	if !s.agencyErr(w, r, err) {
		return
	}
	s.listAgencyMembers(w, r)
}

func (s *Server) agencyMemberRole(ctx context.Context, w http.ResponseWriter, r *http.Request, a *agencyAccess) (string, string, bool) {
	user := r.PathValue("user")
	var role string
	err := errNotFound
	if validUUID(user) {
		err = s.pool.QueryRow(ctx, `SELECT role FROM agency_members WHERE agency_id = $1 AND user_id = $2`, a.AgencyID, user).Scan(&role)
	}
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "member not found")
		return "", "", false
	}
	if err != nil {
		s.internal(w, r, err)
		return "", "", false
	}
	return user, role, true
}

// lastOwner reports whether removing or demoting user would leave the agency
// without an owner.
func lastOwner(ctx context.Context, tx pgx.Tx, agency, user string) (bool, error) {
	var others int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM agency_members WHERE agency_id = $1 AND role = 'owner' AND user_id <> $2`, agency, user).Scan(&others)
	return others == 0, err
}

func (s *Server) updateAgencyMember(w http.ResponseWriter, r *http.Request) {
	a := agencyFrom(r.Context())
	var req struct {
		Role string `json:"role"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !slices.Contains(roles, req.Role) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "role must be owner, admin, operator or viewer")
		return
	}
	ctx := r.Context()
	user, current, ok := s.agencyMemberRole(ctx, w, r, a)
	if !ok {
		return
	}
	if !canManageAgency(a, current, req.Role) {
		writeError(w, r, http.StatusForbidden, "forbidden", "you cannot change someone at or above your rank to that role")
		return
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if current == auth.RoleOwner && req.Role != auth.RoleOwner {
			if last, err := lastOwner(ctx, tx, a.AgencyID, user); err != nil || last {
				if err == nil {
					err = errInvalid("an agency needs at least one owner")
				}
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE agency_members SET role = $3 WHERE agency_id = $1 AND user_id = $2`, a.AgencyID, user, req.Role); err != nil {
			return err
		}
		if err := recordAgency(ctx, tx, a, "agency.member_updated", user, map[string]any{"role": req.Role}); err != nil {
			return err
		}
		return limitAgencyMemberTokens(ctx, tx, a, user, req.Role)
	})
	if !s.agencyErr(w, r, err) {
		return
	}
	s.listAgencyMembers(w, r)
}

func (s *Server) removeAgencyMember(w http.ResponseWriter, r *http.Request) {
	a := agencyFrom(r.Context())
	ctx := r.Context()
	user, current, ok := s.agencyMemberRole(ctx, w, r, a)
	if !ok {
		return
	}
	if user != a.UserID && !canManageAgency(a, current, "") {
		writeError(w, r, http.StatusForbidden, "forbidden", "you cannot remove someone above your rank")
		return
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if current == auth.RoleOwner {
			if last, err := lastOwner(ctx, tx, a.AgencyID, user); err != nil || last {
				if err == nil {
					err = errInvalid("an agency needs at least one owner")
				}
				return err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM agency_members WHERE agency_id = $1 AND user_id = $2`, a.AgencyID, user); err != nil {
			return err
		}
		if err := recordAgency(ctx, tx, a, "agency.member_removed", user, nil); err != nil {
			return err
		}
		return limitAgencyMemberTokens(ctx, tx, a, user, "")
	})
	if !s.agencyErr(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Tokens ----

// limitAgencyMemberTokens is limitMemberTokens for agency tokens, which
// reach every client: removed (role ""), a member's tokens stop working;
// given a lower role, they lose the scopes it lacks, and stop working when
// none are left.
func limitAgencyMemberTokens(ctx context.Context, tx pgx.Tx, a *agencyAccess, userID, role string) error {
	rows, err := tx.Query(ctx, `SELECT id, scopes FROM agency_tokens WHERE agency_id = $1 AND created_by = $2 AND revoked_at IS NULL FOR UPDATE`, a.AgencyID, userID)
	if err != nil {
		return err
	}
	type token struct {
		id     string
		scopes []string
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (token, error) {
		var t token
		err := r.Scan(&t.id, &t.scopes)
		return t, err
	})
	if err != nil {
		return err
	}
	allowed := auth.RoleScopes(role)
	for _, t := range list {
		keep := slices.DeleteFunc(slices.Clone(t.scopes), func(sc string) bool { return !slices.Contains(allowed, sc) })
		switch {
		case len(keep) == 0:
			reason := "role_changed"
			if role == "" {
				reason = "member_removed"
			}
			if _, err := tx.Exec(ctx, `UPDATE agency_tokens SET revoked_at = now() WHERE id = $1`, t.id); err != nil {
				return err
			}
			if err := recordAgency(ctx, tx, a, "agency.token_revoked", t.id, map[string]any{"reason": reason, "user_id": userID}); err != nil {
				return err
			}
		case len(keep) < len(t.scopes):
			if _, err := tx.Exec(ctx, `UPDATE agency_tokens SET scopes = $2 WHERE id = $1`, t.id, keep); err != nil {
				return err
			}
			if err := recordAgency(ctx, tx, a, "agency.token_updated", t.id, map[string]any{"reason": "role_changed", "user_id": userID, "scopes": keep}); err != nil {
				return err
			}
		}
	}
	return nil
}

type agencyTokenView struct {
	tokenView
	CanCreateClients bool `json:"can_create_clients"`
}

func (s *Server) listAgencyTokens(w http.ResponseWriter, r *http.Request) {
	a := agencyFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), `
		SELECT id, name, prefix, scopes, created_at, expires_at, last_used_at, can_create_clients FROM agency_tokens
		WHERE agency_id = $1 AND revoked_at IS NULL ORDER BY created_at DESC`, a.AgencyID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list := []agencyTokenView{}
	for rows.Next() {
		var v agencyTokenView
		if err := rows.Scan(&v.ID, &v.Name, &v.Prefix, &v.Scopes, &v.CreatedAt, &v.ExpiresAt, &v.LastUsedAt, &v.CanCreateClients); err != nil {
			s.internal(w, r, err)
			return
		}
		list = append(list, v)
	}
	if err := rows.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) createAgencyToken(w http.ResponseWriter, r *http.Request) {
	a := agencyFrom(r.Context())
	var req struct {
		Name             string   `json:"name"`
		Scopes           []string `json:"scopes"`
		CanCreateClients bool     `json:"can_create_clients"`
		ExpiresInDays    *int     `json:"expires_in_days"`
	}
	if !decode(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 100 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name must be 1 to 100 characters")
		return
	}
	if len(req.Scopes) == 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "scopes must list at least one scope")
		return
	}
	// A token never holds more than its creator's agency role allows.
	mine := auth.RoleScopes(a.Role)
	for _, sc := range req.Scopes {
		if !auth.ValidScope(sc) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "unknown scope "+sc)
			return
		}
		if !slices.Contains(mine, sc) {
			writeError(w, r, http.StatusForbidden, "forbidden", "you cannot grant scope "+sc)
			return
		}
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
	secret, hash := auth.NewToken(auth.PrefixAgencyToken)
	prefix := secret[:len(auth.PrefixAgencyToken)+6]
	ctx := r.Context()
	var v agencyTokenView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var actorID string
		if err := tx.QueryRow(ctx, `INSERT INTO actors (type, display_name) VALUES ('api_token', $1) RETURNING id`, name).Scan(&actorID); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO agency_tokens (agency_id, actor_id, name, prefix, token_hash, scopes, can_create_clients, created_by, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			RETURNING id, name, prefix, scopes, created_at, expires_at, last_used_at, can_create_clients`,
			a.AgencyID, actorID, name, prefix, hash, req.Scopes, req.CanCreateClients, a.UserID, expiresAt,
		).Scan(&v.ID, &v.Name, &v.Prefix, &v.Scopes, &v.CreatedAt, &v.ExpiresAt, &v.LastUsedAt, &v.CanCreateClients)
		if err != nil {
			return err
		}
		return recordAgency(ctx, tx, a, "agency.token_created", v.ID, map[string]any{"name": name, "scopes": req.Scopes, "can_create_clients": req.CanCreateClients})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"api_token": v, "token": secret})
}

func (s *Server) revokeAgencyToken(w http.ResponseWriter, r *http.Request) {
	a := agencyFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "token not found")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE agency_tokens SET revoked_at = now() WHERE id = $1 AND agency_id = $2 AND revoked_at IS NULL`, id, a.AgencyID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return recordAgency(ctx, tx, a, "agency.token_revoked", id, nil)
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

func (s *Server) listAgencyEvents(w http.ResponseWriter, r *http.Request) {
	a := agencyFrom(r.Context())
	type event struct {
		ID        string          `json:"id"`
		Actor     *string         `json:"actor"`
		Action    string          `json:"action"`
		Target    string          `json:"target"`
		Data      json.RawMessage `json:"data"`
		CreatedAt time.Time       `json:"created_at"`
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT e.id, ac.display_name, e.action, e.target, e.data, e.created_at
		FROM agency_events e LEFT JOIN actors ac ON ac.id = e.actor_id
		WHERE e.agency_id = $1 ORDER BY e.created_at DESC LIMIT 200`, a.AgencyID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[event])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}
