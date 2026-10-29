package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/verify"
)

// Connect requests let an agent connect a service without ever holding the
// secret. The agent asks for a connector and gets a one-time link; the
// person opens it, pastes the key or approves the OAuth sign-in, and the
// agent sees the request turn connected. The link is single use, expires,
// can only connect that one connector, and opens only for a signed-in
// member of the workspace who may connect services there: a link passed to
// a stranger asks for nothing, so it can't be used to talk someone outside
// the workspace into handing over their accounts.

const (
	prefixConnect  = "ofc_"
	connectTTL     = 24 * time.Hour
	connectTTLMax  = 7 * 24 * time.Hour
	connectTTLMin  = 10 * time.Minute
	maxReasonChars = 500
)

var errRequestUsed = errors.New("connect request already used, cancelled or expired")

func (s *Server) routesConnectRequests(mux *http.ServeMux) {
	read, manage := auth.ScopeIntegrationsRead, auth.ScopeIntegrationsManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/connect-requests", s.inWorkspace(read, s.listConnectRequests))
	mux.HandleFunc("POST /v1/workspaces/{ws}/connect-requests", s.inWorkspace(manage, s.createConnectRequest))
	mux.HandleFunc("GET /v1/workspaces/{ws}/connect-requests/{id}", s.inWorkspace(read, s.getConnectRequest))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/connect-requests/{id}", s.inWorkspace(manage, s.cancelConnectRequest))
	// Used by the link's page, by a signed-in member: the token in the body
	// says which request.
	mux.HandleFunc("POST /v1/connect/lookup", s.requireUser(s.connectLookup))
	mux.HandleFunc("POST /v1/connect/complete", s.requireUser(s.connectComplete))
	mux.HandleFunc("POST /v1/connect/oauth", s.requireUser(s.connectOAuth))
}

type connectRequest struct {
	ID                 string `json:"id"`
	Connector          string `json:"connector"`
	ConnectorName      string `json:"connector_name"`
	Name               string `json:"name"`
	Reason             string `json:"reason"`
	UseForVerification bool   `json:"use_for_verification"`
	// pending, connected, cancelled or expired.
	Status        string     `json:"status"`
	IntegrationID *string    `json:"integration_id"`
	CreatedByType string     `json:"created_by_type"`
	ExpiresAt     time.Time  `json:"expires_at"`
	CompletedAt   *time.Time `json:"completed_at"`
	CreatedAt     time.Time  `json:"created_at"`
	// Only in the answer to POST: the link to give the person.
	URL string `json:"url,omitempty"`
}

const connectCols = `id, connector, name, reason, use_for_verification, status, integration_id, created_by_type, expires_at, completed_at, created_at`

func (s *Server) scanConnect(row pgx.Row) (connectRequest, error) {
	var c connectRequest
	err := row.Scan(&c.ID, &c.Connector, &c.Name, &c.Reason, &c.UseForVerification, &c.Status, &c.IntegrationID, &c.CreatedByType, &c.ExpiresAt, &c.CompletedAt, &c.CreatedAt)
	if err != nil {
		return c, err
	}
	if c.Status == "pending" && time.Now().After(c.ExpiresAt) {
		c.Status = "expired"
	}
	c.ConnectorName = c.Connector
	if s.integrations != nil {
		if d, ok := s.integrations.Registry.Definition(c.Connector); ok {
			c.ConnectorName = d.Name
		}
	}
	return c, nil
}

func (s *Server) createConnectRequest(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	var req struct {
		Connector          string `json:"connector"`
		Name               string `json:"name"`
		Reason             string `json:"reason"`
		UseForVerification *bool  `json:"use_for_verification"`
		ExpiresInMinutes   int    `json:"expires_in_minutes"`
	}
	if !decode(w, r, &req) {
		return
	}
	def, ok := s.integrations.Registry.Definition(req.Connector)
	switch {
	case !ok:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "unknown connector "+req.Connector+"; GET /v1/connectors lists them")
		return
	case def.Status != connectors.Available:
		writeError(w, r, http.StatusBadRequest, "not_available", def.Name+" is planned but cannot be connected yet")
		return
	case def.Auth == connectors.AuthNone:
		writeError(w, r, http.StatusBadRequest, "built_in", def.Name+" is built in and needs no connection")
		return
	}
	if _, ok := s.authOptions(def); !ok {
		writeError(w, r, http.StatusBadRequest, "not_configured", def.Name+" needs an OAuth app on this server: "+connectors.OAuthHint(def))
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if len([]rune(reason)) > maxReasonChars {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "reason must be at most 500 characters")
		return
	}
	ttl := connectTTL
	if req.ExpiresInMinutes != 0 {
		ttl = time.Duration(req.ExpiresInMinutes) * time.Minute
		if ttl < connectTTLMin || ttl > connectTTLMax {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "expires_in_minutes must be between 10 and 10080 (7 days)")
			return
		}
	}
	// Verification connectors become the verification service unless told not to.
	useVerify := def.Has(connectors.CapVerify)
	if req.UseForVerification != nil {
		useVerify = *req.UseForVerification && useVerify
	}
	secret, hash := auth.NewToken(prefixConnect)
	var out connectRequest
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = s.scanConnect(tx.QueryRow(r.Context(), `
			INSERT INTO connect_requests (workspace_id, connector, name, reason, use_for_verification, token_hash, created_by, created_by_type, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING `+connectCols,
			a.WorkspaceID, def.ID, cleanName(req.Name), reason, useVerify, hash, a.ActorID, a.ActorType, time.Now().Add(ttl)))
		if err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "connect_request.created", "connect_request", out.ID,
			map[string]any{"connector": def.ID, "reason": reason})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// The secret rides in the fragment so it never reaches server logs.
	out.URL = s.publicURL + "/connect#" + secret
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) listConnectRequests(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), `SELECT `+connectCols+` FROM connect_requests WHERE workspace_id = $1 ORDER BY created_at DESC LIMIT 100`, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	defer rows.Close()
	list := []connectRequest{}
	for rows.Next() {
		c, err := s.scanConnect(rows)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		list = append(list, c)
	}
	if err := rows.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) getConnectRequest(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "connect request not found")
		return
	}
	c, err := s.scanConnect(s.pool.QueryRow(r.Context(), `SELECT `+connectCols+` FROM connect_requests WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "connect request not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) cancelConnectRequest(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "connect request not found")
		return
	}
	var found bool
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), `UPDATE connect_requests SET status = 'cancelled', completed_at = now() WHERE workspace_id = $1 AND id = $2 AND status = 'pending'`, a.WorkspaceID, id)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		found = true
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "connect_request.cancelled", "connect_request", id, nil)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !found {
		writeError(w, r, http.StatusNotFound, "not_found", "no pending connect request with that id")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pendingRequest is the request behind a link's secret, if it can still be used.
type pendingRequest struct {
	ID, Workspace, WorkspaceName, Connector, Name, Reason, CreatedBy, CreatedByType string
	ExpiresAt                                                                       time.Time
	// The account email of the person who asked, or whose token or app
	// asked; empty when there is none. Only members see it, and members
	// already see each other's addresses.
	RequesterEmail string
}

// openLink finds the request behind a link and checks the signed-in person
// may complete it: a member of its workspace whose role connects services.

func (s *Server) openLink(w http.ResponseWriter, r *http.Request, token string) (pendingRequest, connectors.Definition, bool) {
	var p pendingRequest
	if s.integrations == nil || !strings.HasPrefix(token, prefixConnect) {
		writeError(w, r, http.StatusNotFound, "not_found", "this link is not valid")
		return p, connectors.Definition{}, false
	}
	var status string
	var createdBy *string
	err := s.pool.QueryRow(r.Context(), `
		SELECT c.id, c.workspace_id, w.name, c.connector, c.name, c.reason, c.created_by, c.created_by_type, c.status, c.expires_at
		FROM connect_requests c JOIN workspaces w ON w.id = c.workspace_id WHERE c.token_hash = $1`, auth.HashToken(token)).
		Scan(&p.ID, &p.Workspace, &p.WorkspaceName, &p.Connector, &p.Name, &p.Reason, &createdBy, &p.CreatedByType, &status, &p.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "this link is not valid")
		return p, connectors.Definition{}, false
	}
	if err != nil {
		s.internal(w, r, err)
		return p, connectors.Definition{}, false
	}
	if createdBy != nil {
		p.CreatedBy = *createdBy
	}
	// Members only, and the same answer for every other account, so a link
	// says nothing about the workspace to someone outside it.
	var role string
	var stored []string
	err = s.pool.QueryRow(r.Context(), `SELECT role, scopes FROM (`+workspaceRoles+`) r ORDER BY rank DESC LIMIT 1`, p.Workspace, principalFrom(r.Context()).UserID).Scan(&role, &stored)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusForbidden, "not_a_member", "this link is for members of the workspace that asked for it: sign in with the account you use there, or ask a workspace admin to invite you")
		return p, connectors.Definition{}, false
	}
	if err != nil {
		s.internal(w, r, err)
		return p, connectors.Definition{}, false
	}
	if !auth.Grants(effectiveScopes(role, stored), auth.ScopeIntegrationsManage) {
		writeError(w, r, http.StatusForbidden, "forbidden", "your role in this workspace can't connect services: ask a workspace admin to open the link")
		return p, connectors.Definition{}, false
	}
	if createdBy != nil {
		// A person, or the person behind the token or app that asked.
		if err := s.pool.QueryRow(r.Context(), `
			SELECT coalesce((SELECT u.email FROM users u WHERE u.disabled_at IS NULL AND (u.actor_id = $1
				OR u.id = (SELECT t.created_by FROM api_tokens t WHERE t.actor_id = $1 LIMIT 1)) LIMIT 1), '')`, *createdBy).Scan(&p.RequesterEmail); err != nil {
			s.internal(w, r, err)
			return p, connectors.Definition{}, false
		}
	}
	switch {
	case status == "connected":
		writeError(w, r, http.StatusGone, "used", "this link was already used; the service is connected")
		return p, connectors.Definition{}, false
	case status == "cancelled":
		writeError(w, r, http.StatusGone, "cancelled", "this link was cancelled; ask for a new one")
		return p, connectors.Definition{}, false
	case time.Now().After(p.ExpiresAt):
		writeError(w, r, http.StatusGone, "expired", "this link has expired; ask for a new one")
		return p, connectors.Definition{}, false
	}
	def, ok := s.integrations.Registry.Definition(p.Connector)
	if !ok || def.Status != connectors.Available {
		writeError(w, r, http.StatusGone, "not_available", "this connector is no longer available")
		return p, connectors.Definition{}, false
	}
	return p, def, true
}

func (s *Server) connectLookup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &req) {
		return
	}
	p, def, ok := s.openLink(w, r, req.Token)
	if !ok {
		return
	}
	entry := s.entry(def, 0)
	writeJSON(w, http.StatusOK, map[string]any{
		"workspace_name":     p.WorkspaceName,
		"connector":          entry,
		"name":               p.Name,
		"reason":             p.Reason,
		"requested_by":       map[bool]string{true: "agent", false: "person"}[p.CreatedByType == "api_token"],
		"requested_by_email": nullIfEmpty(p.RequesterEmail),
		"expires_at":         p.ExpiresAt,
	})
}

func (s *Server) connectComplete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token       string            `json:"token"`
		Credentials map[string]string `json:"credentials"`
	}
	if !decode(w, r, &req) {
		return
	}
	p, def, ok := s.openLink(w, r, req.Token)
	if !ok {
		return
	}
	if !def.Supports(connectors.AuthAPIKey) {
		writeError(w, r, http.StatusBadRequest, "use_oauth", def.Name+" connects through its sign-in page")
		return
	}
	creds, err := connectors.CheckFields(def, req.Credentials)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	account, err := s.integrations.Test(r.Context(), def.ID, creds)
	if err != nil {
		connectorError(w, r, err)
		return
	}
	name := p.Name
	if name == "" {
		name = s.defaultName(r, p.Workspace, def, account)
	}
	out, err := s.saveIntegration(r.Context(), &principal{ActorID: p.CreatedBy, ActorType: p.CreatedByType}, p.Workspace, def, connectors.AuthAPIKey, name, creds, account, "connect_link", p.ID, integrationShare{})
	switch {
	case errors.Is(err, errRequestUsed):
		writeError(w, r, http.StatusGone, "used", "this link was already used or cancelled")
	case isUnique(err):
		writeError(w, r, http.StatusConflict, "conflict", "another connection is already named "+name)
	case err != nil:
		s.internal(w, r, err)
	default:
		writeJSON(w, http.StatusCreated, map[string]any{"connector_name": def.Name, "name": out.Name, "account": out.Account, "workspace_name": p.WorkspaceName})
	}
}

// connectOAuth starts the provider's sign-in for a link. The OAuth state
// carries the request, so the callback marks it connected.
func (s *Server) connectOAuth(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &req) {
		return
	}
	p, def, ok := s.openLink(w, r, req.Token)
	if !ok {
		return
	}
	app, configured := s.integrations.OAuthApps[def.ID]
	if !def.Supports(connectors.AuthOAuth2) || def.OAuth == nil || !configured || s.box == nil {
		writeError(w, r, http.StatusBadRequest, "not_configured", def.Name+" cannot sign in through OAuth on this server")
		return
	}
	st, _ := json.Marshal(integrationState{
		Workspace: p.Workspace, Actor: p.CreatedBy, ActorType: p.CreatedByType, Request: p.ID, Connector: def.ID, Name: p.Name,
		Verifier: newID() + newID(), Expires: time.Now().Add(15 * time.Minute).Unix(),
	})
	var parsed integrationState
	_ = json.Unmarshal(st, &parsed)
	sealed := base64.RawURLEncoding.EncodeToString(s.box.Seal(st, []byte(integrationStatePurpose)))
	writeJSON(w, http.StatusOK, map[string]string{"url": connectors.AuthURL(def, app, s.integrationRedirectURI(), sealed, parsed.Verifier)})
}

// completeConnectRequest marks a pending request connected, inside the
// transaction that saved the integration, and applies what it asked for.
func completeConnectRequest(ctx context.Context, tx pgx.Tx, ws, id, integrationID string, def connectors.Definition) error {
	var useVerify bool
	err := tx.QueryRow(ctx, `
		UPDATE connect_requests SET status = 'connected', integration_id = $3, completed_at = now()
		WHERE workspace_id = $1 AND id = $2 AND status = 'pending' AND expires_at > now()
		RETURNING use_for_verification`, ws, id, integrationID).Scan(&useVerify)
	if errors.Is(err, pgx.ErrNoRows) {
		return errRequestUsed
	}
	if err != nil || !useVerify || !def.Has(connectors.CapVerify) {
		return err
	}
	set, err := verify.LoadSettings(ctx, tx, ws)
	if err != nil {
		return err
	}
	set.IntegrationID = &integrationID
	return verify.SaveSettings(ctx, tx, ws, set)
}
