package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/mailbox"
	"github.com/outflareai/outflare/engine/internal/oauth"
)

func (s *Server) routesOAuth(mux *http.ServeMux) {
	read, manage := auth.ScopeMailboxesRead, auth.ScopeMailboxesManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/oauth-connections", s.inWorkspace(read, s.listConnections))
	mux.HandleFunc("POST /v1/workspaces/{ws}/oauth-connections", s.inWorkspace(manage, s.createConnection))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/oauth-connections/{id}", s.inWorkspace(manage, s.deleteConnection))
	mux.HandleFunc("POST /v1/workspaces/{ws}/oauth-connections/{id}/mailboxes", s.inWorkspace(manage, s.addConnectionMailboxes))
	mux.HandleFunc("GET /v1/workspaces/{ws}/oauth/{provider}/authorize", s.inWorkspace(manage, s.authorize))
	mux.HandleFunc("GET /v1/oauth/callback", s.oauthCallback)
}

// oauthProviders lists the providers whose consent screen is configured.
func (s *Server) oauthProviders() []string {
	out := []string{}
	if s.oauth == nil {
		return out
	}
	for _, p := range []string{mailbox.ProviderGoogle, mailbox.ProviderMicrosoft} {
		if s.oauth.Configured(p) {
			out = append(out, p)
		}
	}
	return out
}

func (s *Server) needOAuth(w http.ResponseWriter, r *http.Request) bool {
	if s.oauth == nil || s.mail == nil {
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "OAuth is not configured on this server")
		return false
	}
	return true
}

// ---- Admin connections ----

type connectionView struct {
	ID           string            `json:"id"`
	Provider     string            `json:"provider"`
	Name         string            `json:"name"`
	Details      map[string]string `json:"details"`
	MailboxCount int               `json:"mailbox_count"`
	CreatedAt    time.Time         `json:"created_at"`
}

// The secret column is never selected here.
const connectionSelect = `
	SELECT c.id, c.provider, c.name, c.details,
	       (SELECT count(*) FROM mailboxes m WHERE m.oauth_connection_id = c.id)::int, c.created_at
	FROM oauth_connections c`

func (s *Server) listConnections(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), connectionSelect+` WHERE c.workspace_id = $1 ORDER BY c.created_at`, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[connectionView])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) createConnection(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !s.needOAuth(w, r) {
		return
	}
	var req struct {
		Provider       string          `json:"provider"`
		Name           string          `json:"name"`
		ServiceAccount json.RawMessage `json:"service_account"`
		TenantID       string          `json:"tenant_id"`
		ClientID       string          `json:"client_id"`
		ClientSecret   string          `json:"client_secret"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	ctx := r.Context()
	id := newID()
	var sealed []byte
	var details map[string]string
	var err error
	switch req.Provider {
	case mailbox.ProviderGoogle:
		raw := []byte(req.ServiceAccount)
		// The key file may be sent as an object or as its text.
		var text string
		if json.Unmarshal(raw, &text) == nil {
			raw = []byte(text)
		}
		sa, perr := oauth.ParseServiceAccount(raw)
		if perr != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "service_account: "+perr.Error())
			return
		}
		details = map[string]string{"client_email": sa.ClientEmail, "client_id": sa.ClientID}
		if req.Name == "" {
			req.Name = sa.ClientEmail
		}
		sealed, err = oauth.SealConnection(s.oauth.Box, id, sa, "", "", "")
	case mailbox.ProviderMicrosoft:
		req.TenantID, req.ClientID = strings.TrimSpace(req.TenantID), strings.TrimSpace(req.ClientID)
		if req.TenantID == "" || req.ClientID == "" || req.ClientSecret == "" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "tenant_id, client_id and client_secret are required")
			return
		}
		// Getting a token proves the app and secret are right before any
		// mailbox depends on them.
		if _, terr := s.oauth.AppToken(ctx, req.TenantID, req.ClientID, req.ClientSecret); terr != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", terr.Error())
			return
		}
		details = map[string]string{"tenant_id": req.TenantID, "client_id": req.ClientID}
		if req.Name == "" {
			req.Name = req.TenantID
		}
		sealed, err = oauth.SealConnection(s.oauth.Box, id, nil, req.TenantID, req.ClientID, req.ClientSecret)
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "provider must be google or microsoft")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO oauth_connections (id, workspace_id, provider, name, details, secret) VALUES ($1, $2, $3, $4, $5, $6)`,
			id, a.WorkspaceID, req.Provider, req.Name, details, sealed); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "oauth_connection.created", "oauth_connection", id,
			map[string]any{"provider": req.Provider, "name": req.Name})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, connectionView{ID: id, Provider: req.Provider, Name: req.Name, Details: details, CreatedAt: time.Now().UTC()})
}

func (s *Server) deleteConnection(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "connection not found")
		return
	}
	ctx := r.Context()
	var inUse int
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var name string
		err := tx.QueryRow(ctx, `SELECT name FROM oauth_connections WHERE id = $1 AND workspace_id = $2 FOR UPDATE`, id, a.WorkspaceID).Scan(&name)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM mailboxes WHERE oauth_connection_id = $1`, id).Scan(&inUse); err != nil || inUse > 0 {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM oauth_connections WHERE id = $1`, id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "oauth_connection.deleted", "oauth_connection", id, map[string]any{"name": name})
	})
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "connection not found")
	case err != nil:
		s.internal(w, r, err)
	case inUse > 0:
		writeError(w, r, http.StatusConflict, "conflict", "remove the mailboxes that use this connection first")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// maxConnectionMailboxes bounds one bulk add.
const maxConnectionMailboxes = 5000

func (s *Server) addConnectionMailboxes(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var req struct {
		Mailboxes []struct {
			Email       string `json:"email"`
			DisplayName string `json:"display_name"`
		} `json:"mailboxes"`
		Pool       string `json:"pool"`
		DailyLimit int    `json:"daily_limit"`
	}
	if !decodeLarge(w, r, &req, maxImportBytes) {
		return
	}
	if len(req.Mailboxes) == 0 || len(req.Mailboxes) > maxConnectionMailboxes {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send 1 to 5000 mailboxes")
		return
	}
	if req.DailyLimit != 0 && (req.DailyLimit < 1 || req.DailyLimit > 500) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "daily_limit must be 1 to 500")
		return
	}
	var provider string
	err := errNotFound
	if validUUID(id) {
		err = s.pool.QueryRow(r.Context(), `SELECT provider FROM oauth_connections WHERE id = $1 AND workspace_id = $2`, id, a.WorkspaceID).Scan(&provider)
	}
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "connection not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var rows []mailbox.Row
	var bad []mailbox.RowError
	for i, m := range req.Mailboxes {
		email, ok := validEmail(m.Email)
		if !ok {
			bad = append(bad, mailbox.RowError{Line: i + 1, Email: m.Email, Error: "invalid email"})
			continue
		}
		rows = append(rows, mailbox.Row{
			Line: i + 1, Email: strings.ToLower(email), DisplayName: strings.TrimSpace(m.DisplayName), Provider: provider,
			SMTPUsername: strings.ToLower(email), IMAPUsername: strings.ToLower(email), Pool: strings.TrimSpace(req.Pool), DailyLimit: req.DailyLimit,
			ConnectionID: &id,
		})
	}
	s.addMailboxes(w, r, a, "oauth_connection", rows, bad, func(res mailbox.ImportResult) {
		writeJSON(w, http.StatusOK, res)
	})
}

// ---- Consent screen, one mailbox at a time ----

const statePurpose = "oauth-state"

type oauthState struct {
	Workspace string `json:"w"`
	User      string `json:"u"`
	Provider  string `json:"p"`
	Pool      string `json:"o,omitempty"`
	Expires   int64  `json:"e"`
	Nonce     string `json:"n"`
}

func (s *Server) redirectURI() string { return s.publicURL + "/v1/oauth/callback" }

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !s.needOAuth(w, r) {
		return
	}
	if a.UserID == "" {
		writeError(w, r, http.StatusForbidden, "forbidden", "connecting a mailbox needs a signed-in user, not an API token")
		return
	}
	provider := r.PathValue("provider")
	if provider != mailbox.ProviderGoogle && provider != mailbox.ProviderMicrosoft {
		writeError(w, r, http.StatusNotFound, "not_found", "provider must be google or microsoft")
		return
	}
	if !s.oauth.Configured(provider) {
		writeError(w, r, http.StatusBadRequest, "not_configured", "the "+provider+" OAuth app is not configured on this server")
		return
	}
	st, _ := json.Marshal(oauthState{
		Workspace: a.WorkspaceID, User: a.UserID, Provider: provider, Pool: strings.TrimSpace(r.URL.Query().Get("pool")),
		Expires: time.Now().Add(15 * time.Minute).Unix(), Nonce: newID(),
	})
	msg := base64.RawURLEncoding.EncodeToString(st)
	state := msg + "." + s.oauth.Box.Sign(statePurpose, msg)
	http.Redirect(w, r, s.oauth.AuthURL(provider, s.redirectURI(), state), http.StatusFound)
}

// oauthCallback finishes the consent screen. It answers with a redirect to
// the web app, which shows the outcome.
func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	page := "/" // the workspace's Mailboxes page once the state is known
	done := func(params url.Values) {
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, s.publicURL+page+"?"+params.Encode(), http.StatusFound)
	}
	fail := func(msg string) { done(url.Values{"oauth_error": {msg}}) }
	if s.oauth == nil || s.mail == nil {
		fail("OAuth is not configured on this server")
		return
	}
	q := r.URL.Query()
	msg, sig, _ := strings.Cut(q.Get("state"), ".")
	var st oauthState
	raw, err := base64.RawURLEncoding.DecodeString(msg)
	if err != nil || !s.oauth.Box.Verify(statePurpose, msg, sig) || json.Unmarshal(raw, &st) != nil {
		fail("the sign-in link is not valid; start again from Mailboxes")
		return
	}
	page = s.appPath(r.Context(), st.Workspace, "mailboxes")
	if time.Now().Unix() > st.Expires {
		fail("the sign-in took too long; start again from Mailboxes")
		return
	}
	if e := q.Get("error"); e != "" {
		if e == "access_denied" {
			fail("access was not granted")
		} else {
			fail(truncate("the provider refused: "+e+" "+q.Get("error_description"), 300))
		}
		return
	}

	// The person finishing must be the person who started, and still allowed
	// to manage mailboxes there.
	p, err := s.authenticate(r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if p == nil || p.UserID != st.User {
		fail("sign in to Outflare as the person who started connecting, then try again")
		return
	}
	a := &access{principal: *p, WorkspaceID: st.Workspace}
	var brands int
	var stored []string
	err = s.pool.QueryRow(r.Context(), `SELECT m.role, cardinality(m.brand_ids), `+memberRoleScopes+` FROM memberships m WHERE m.workspace_id = $1 AND m.user_id = $2`, st.Workspace, p.UserID).Scan(&a.Role, &brands, &stored)
	if errors.Is(err, pgx.ErrNoRows) {
		fail("you are no longer a member of that workspace")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	a.Scopes = effectiveScopes(a.Role, stored)
	if a.Test, err = mailbox.IsTestWorkspace(r.Context(), s.pool, st.Workspace); err != nil {
		s.internal(w, r, err)
		return
	}
	if !a.has(auth.ScopeMailboxesManage) {
		fail("your role cannot add mailboxes")
		return
	}
	if brands > 0 && auth.BrandLimitable(a.Scopes) {
		fail("adding mailboxes needs access to every brand")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	g, err := s.oauth.Exchange(ctx, st.Provider, q.Get("code"), s.redirectURI())
	if err != nil {
		fail(truncate(err.Error(), 300))
		return
	}
	row := mailbox.Row{Email: g.Email, DisplayName: g.Name, Provider: st.Provider, SMTPUsername: g.Email, IMAPUsername: g.Email,
		Pool: st.Pool, RefreshToken: g.RefreshToken}
	res, err := s.importRows(ctx, a, "oauth", []mailbox.Row{row}, nil)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if len(res.Errors) > 0 {
		fail(res.Errors[0].Error)
		return
	}
	done(url.Values{"ws": {st.Workspace}, "connected": {g.Email}})
}

func newID() string {
	var b [16]byte
	rand.Read(b[:]) //nolint:errcheck // never fails
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
