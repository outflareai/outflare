package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Signing AI apps in to the hosted MCP server (/mcp) with OAuth 2.1, the way
// the MCP authorization spec asks: the app finds this server from the
// protected resource metadata, registers itself (RFC 7591), sends the
// person to /oauth/authorize with PKCE, and swaps the code for an access
// token. The person picks a workspace and how much the app may do on the
// consent screen; the app gets an ordinary API token (oft_) that lasts an
// hour, and a refresh token that renews it. Revoking the token, here or on
// the API tokens page, disconnects the app.
//
// The OAuth endpoints answer in RFC 6749's shape ({"error",
// "error_description"}), not the API's, because OAuth clients expect it.

const (
	agentCodeTTL    = 10 * time.Minute
	agentAccessTTL  = time.Hour
	agentRefreshTTL = 90 * 24 * time.Hour
	// An app that retries a refresh (a dropped response) may send the
	// refresh token it just swapped once more within this window.
	agentRefreshGrace = 2 * time.Minute

	prefixOAuthCode    = "ofc_"
	prefixOAuthRefresh = "ofo_"
)

// agentWithheld are scopes a connected app never gets from the consent
// screen's "Run my marketing" choice: they manage people, tokens and the
// workspace itself, or approve what an agent asked for. Spending money
// (infrastructure.approve) is a separate opt-in.
var agentWithheld = []string{auth.ScopeTokensManage, auth.ScopeMembersManage, auth.ScopeMembersRemove, auth.ScopeRolesManage, auth.ScopeWorkspaceManage, auth.ScopeMissionsApprove,
	auth.ScopeInfraApprove, auth.ScopeSuppressionsManage, auth.ScopeWebhooksManage, auth.ScopeSocialPost}

// agentScopes is what a connected app may do for someone with role: "full"
// is everything the role allows except agentWithheld (plus spending when
// spend is set), "read" only the role's .read scopes.
func agentScopes(roleScopes []string, access string, spend bool) []string {
	var out []string
	for _, sc := range roleScopes {
		switch {
		case access == "read":
			if strings.HasSuffix(sc, ".read") {
				out = append(out, sc)
			}
		case sc == auth.ScopeInfraApprove && spend:
			out = append(out, sc)
		case !slices.Contains(agentWithheld, sc):
			out = append(out, sc)
		}
	}
	return out
}

func (s *Server) routesAgentOAuth(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.protectedResourceMetadata)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", s.protectedResourceMetadata)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.authorizationServerMetadata)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server/mcp", s.authorizationServerMetadata)
	mux.HandleFunc("GET /.well-known/openid-configuration", s.authorizationServerMetadata)
	mux.HandleFunc("POST /oauth/register", s.registerOAuthClient)
	mux.HandleFunc("POST /oauth/token", s.oauthToken)
	mux.HandleFunc("POST /oauth/revoke", s.oauthRevoke)
	for _, p := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-authorization-server",
		"/.well-known/oauth-authorization-server/mcp", "/.well-known/openid-configuration", "/oauth/register", "/oauth/token", "/oauth/revoke"} {
		mux.HandleFunc("OPTIONS "+p, preflight)
	}
	mux.HandleFunc("GET /oauth/authorize", s.oauthAuthorizePage)

	mux.HandleFunc("GET /v1/agent-apps/authorize", s.requireUser(s.getAgentAuthorization))
	mux.HandleFunc("POST /v1/agent-apps/authorize", s.requireUser(s.approveAgentAuthorization))
	mux.HandleFunc("GET /v1/workspaces/{ws}/agent-apps", s.inWorkspace(auth.ScopeWorkspaceRead, s.listAgentApps))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/agent-apps/{id}", s.inWorkspace(auth.ScopeWorkspaceRead, s.updateAgentApp))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/agent-apps/{id}", s.inWorkspace(auth.ScopeWorkspaceRead, s.disconnectAgentApp))
}

// The OAuth endpoints and metadata carry no cookies, so any origin may call
// them (browser-based MCP clients such as the MCP Inspector do).
func allowAnyOrigin(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Mcp-Session-Id, Mcp-Protocol-Version, Last-Event-ID")
	h.Set("Access-Control-Expose-Headers", "Mcp-Session-Id, WWW-Authenticate")
	h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	h.Set("Access-Control-Max-Age", "86400")
}

func preflight(w http.ResponseWriter, r *http.Request) {
	allowAnyOrigin(w)
	w.WriteHeader(http.StatusNoContent)
}

// mcpURL is the hosted MCP server's address, the resource tokens are for.
func (s *Server) mcpURL() string { return s.publicURL + "/mcp" }

func (s *Server) protectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	allowAnyOrigin(w)
	meta := map[string]any{
		"resource":                 s.mcpURL(),
		"resource_name":            "Outflare",
		"authorization_servers":    []string{s.publicURL},
		"bearer_methods_supported": []string{"header"},
		"resource_documentation":   "https://outflare.ai/docs/agents/mcp/",
	}
	s.addPolicies(meta, "resource_policy_uri", "resource_tos_uri")
	writeJSON(w, http.StatusOK, meta)
}

func (s *Server) authorizationServerMetadata(w http.ResponseWriter, r *http.Request) {
	allowAnyOrigin(w)
	meta := map[string]any{
		"issuer":                                     s.publicURL,
		"authorization_endpoint":                     s.publicURL + "/oauth/authorize",
		"token_endpoint":                             s.publicURL + "/oauth/token",
		"registration_endpoint":                      s.publicURL + "/oauth/register",
		"revocation_endpoint":                        s.publicURL + "/oauth/revoke",
		"response_types_supported":                   []string{"code"},
		"grant_types_supported":                      []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":           []string{"S256"},
		"token_endpoint_auth_methods_supported":      []string{"none"},
		"revocation_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                           []string{"outflare", "offline_access"},
		"service_documentation":                      "https://outflare.ai/docs/agents/mcp/",
	}
	s.addPolicies(meta, "op_policy_uri", "op_tos_uri")
	writeJSON(w, http.StatusOK, meta)
}

// addPolicies links the operator's privacy policy and terms, when set, under
// the given metadata keys (RFC 9728 for the resource, RFC 8414 for the
// authorization server).
func (s *Server) addPolicies(meta map[string]any, policy, tos string) {
	if s.privacyURL != "" {
		meta[policy] = s.privacyURL
	}
	if s.termsURL != "" {
		meta[tos] = s.termsURL
	}
}

// oauthError answers an OAuth endpoint in RFC 6749's error shape.
func oauthError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

// ---- Client registration (RFC 7591) ----

type oauthClient struct {
	ID           string
	Name         string
	URI          string
	RedirectURIs []string
	VerifiedAt   *time.Time
}

var badSchemes = []string{"javascript", "data", "file", "vbscript", "blob", "about", "ftp", "ws", "wss", "mailto", "tel"}
var schemeShape = regexp.MustCompile(`^[a-z][a-z0-9+.-]*$`)

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// redirectProblem says why u cannot be a redirect URI, or "" when it can:
// https anywhere, http only back to this computer (apps such as Claude Code
// listen on a loopback port), or an app's own scheme (cursor://...).
func redirectProblem(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || len(raw) > 2000 {
		return "redirect_uris must be absolute URLs"
	}
	if u.Fragment != "" {
		return "redirect_uris must not have a fragment"
	}
	switch u.Scheme {
	case "https":
		if u.Host == "" {
			return "redirect_uris must name a host"
		}
	case "http":
		if !isLoopback(u.Hostname()) {
			return "http redirect_uris must point at this computer (localhost or 127.0.0.1); use https"
		}
	default:
		if !schemeShape.MatchString(u.Scheme) || slices.Contains(badSchemes, u.Scheme) {
			return "redirect_uris must use https, a loopback http address or an app's own scheme"
		}
	}
	return ""
}

// redirectMatches reports whether given is one of the registered URIs. A
// loopback address may use any port (RFC 8252), since apps pick a free one.
func redirectMatches(registered []string, given string) bool {
	if slices.Contains(registered, given) {
		return true
	}
	g, err := url.Parse(given)
	if err != nil || g.Scheme != "http" || !isLoopback(g.Hostname()) {
		return false
	}
	for _, r := range registered {
		u, err := url.Parse(r)
		if err == nil && u.Scheme == "http" && u.Hostname() == g.Hostname() && u.Path == g.Path && u.RawQuery == g.RawQuery {
			return true
		}
	}
	return false
}

// appDisplayName keeps a display name to printable characters, 80 at most.
func appDisplayName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '‮' || r == '‭' {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80])
	}
	return s
}

func (s *Server) registerOAuthClient(w http.ResponseWriter, r *http.Request) {
	allowAnyOrigin(w)
	ctx := r.Context()
	fail := func(status int, code, msg string) {
		track.Emit(ctx, track.Event{Name: "oauth_client.register_failed", Surface: "api", Outcome: track.Failure, ErrorCode: code, RequestID: RequestID(ctx)})
		oauthError(w, status, code, msg)
	}
	if !s.registerLimit.allow(w, r, clientIP(r)) {
		return
	}
	var req struct {
		RedirectURIs            []string `json:"redirect_uris"`
		ClientName              string   `json:"client_name"`
		ClientURI               string   `json:"client_uri"`
		GrantTypes              []string `json:"grant_types"`
		ResponseTypes           []string `json:"response_types"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		fail(http.StatusBadRequest, "invalid_client_metadata", "send the client metadata as a JSON object")
		return
	}
	if len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > 10 {
		fail(http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris must list 1 to 10 URLs")
		return
	}
	for _, u := range req.RedirectURIs {
		if p := redirectProblem(u); p != "" {
			fail(http.StatusBadRequest, "invalid_redirect_uri", p)
			return
		}
	}
	for _, g := range req.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			fail(http.StatusBadRequest, "invalid_client_metadata", "grant_types may only be authorization_code and refresh_token")
			return
		}
	}
	name := appDisplayName(req.ClientName)
	if name == "" {
		name = "AI app"
	}
	uri := ""
	if u, err := url.Parse(req.ClientURI); err == nil && u.Scheme == "https" && u.Host != "" && len(req.ClientURI) <= 500 {
		uri = req.ClientURI
	}
	var id string
	var created time.Time
	err := s.pool.QueryRow(ctx, `INSERT INTO oauth_clients (name, uri, redirect_uris) VALUES ($1, $2, $3) RETURNING id, created_at`,
		name, uri, req.RedirectURIs).Scan(&id, &created)
	if err != nil {
		track.Fail(ctx, s.log, track.Event{Name: "oauth_client.register_failed", Surface: "api", RequestID: RequestID(ctx)}, "internal", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "could not register the client")
		return
	}
	track.Emit(ctx, track.Event{Name: "oauth_client.registered", Surface: "api", RequestID: RequestID(ctx),
		Props: map[string]any{"client_id": id, "app": appKind(req.RedirectURIs[0]), "redirect_uris": len(req.RedirectURIs)}})
	w.Header().Set("Cache-Control", "no-store")
	out := map[string]any{
		"client_id":                  id,
		"client_id_issued_at":        created.Unix(),
		"client_name":                name,
		"redirect_uris":              req.RedirectURIs,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	}
	// Clients validate URLs they get back, so an empty one is left out.
	if uri != "" {
		out["client_uri"] = uri
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) loadOAuthClient(ctx context.Context, id string) (*oauthClient, error) {
	if !validUUID(id) {
		return nil, nil
	}
	c := &oauthClient{ID: id}
	err := s.pool.QueryRow(ctx, `SELECT name, uri, redirect_uris, verified_at FROM oauth_clients WHERE id = $1`, id).Scan(&c.Name, &c.URI, &c.RedirectURIs, &c.VerifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

// appKind names the app a redirect URI belongs to, when it is one we know.
// The consent screen marks other apps as unverified: anyone can register
// a client called "Claude".
func appKind(redirect string) string {
	u, err := url.Parse(redirect)
	if err != nil {
		return "unknown"
	}
	host := strings.ToLower(u.Hostname())
	under := func(domains ...string) bool {
		return slices.ContainsFunc(domains, func(d string) bool { return host == d || strings.HasSuffix(host, "."+d) })
	}
	switch {
	case u.Scheme == "https" && under("claude.ai", "claude.com", "anthropic.com"):
		return "claude"
	case u.Scheme == "https" && under("chatgpt.com", "openai.com"):
		return "chatgpt"
	case u.Scheme == "cursor":
		return "cursor"
	case u.Scheme == "vscode" || u.Scheme == "vscode-insiders":
		return "vscode"
	case u.Scheme == "http" && isLoopback(host):
		return "local"
	}
	return "unknown"
}

// appVerified reports whether an app may be given more than reading: one
// Outflare recognises by its redirect address, or one the server's operator
// checked (Accounts.VerifyAgentApp). Anyone can register an app, so any
// other app only reads, whatever the person picks.
func appVerified(kind string, verifiedAt *time.Time) bool {
	return kind != "unknown" || verifiedAt != nil
}

// onlyReads keeps the .read scopes.
func onlyReads(scopes []string) []string {
	return slices.DeleteFunc(slices.Clone(scopes), func(sc string) bool { return !strings.HasSuffix(sc, ".read") })
}

// VerifyAgentApp marks a self-registered app as checked by the operator,
// so people can give it more than reading; verified false takes that back
// (its connections drop to reading on their next refresh). It answers
// false when there is no such app.
func (a *Accounts) VerifyAgentApp(ctx context.Context, clientID string, verified bool) (bool, error) {
	if !validUUID(clientID) {
		return false, nil
	}
	tag, err := a.Pool.Exec(ctx, `UPDATE oauth_clients SET verified_at = CASE WHEN $2 THEN coalesce(verified_at, now()) END WHERE id = $1`, clientID, verified)
	if err != nil {
		track.Fail(ctx, a.Log, track.Event{Name: "oauth_client.verify_failed", Surface: "system"}, "internal", err)
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	name := "oauth_client.verified"
	if !verified {
		name = "oauth_client.unverified"
	}
	track.Emit(ctx, track.Event{Name: name, Surface: "system", Props: map[string]any{"client_id": clientID}})
	return true, nil
}

// ---- Authorization ----

// oauthAuthorizePage is where apps send the person. Problems the app can
// hear about go back to its redirect URI; a bad client or redirect URI must
// not redirect anywhere, so those get a page of their own. Otherwise the web
// app shows the consent screen.
func (s *Server) oauthAuthorizePage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c, err := s.loadOAuthClient(r.Context(), q.Get("client_id"))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	redirect := q.Get("redirect_uri")
	if c == nil || !redirectMatches(c.RedirectURIs, redirect) {
		track.Emit(r.Context(), track.Event{Name: "agent_app.connect_failed", Surface: "web", Outcome: track.Failure, ErrorCode: "invalid_client", RequestID: RequestID(r.Context())})
		authorizeProblem(w, "This connection link isn't valid", "The app that sent you here isn't registered with Outflare, or it asked to send you somewhere it didn't register. Start connecting again from the app.")
		return
	}
	back := func(code, desc string) {
		track.Emit(r.Context(), track.Event{Name: "agent_app.connect_failed", Surface: "web", Outcome: track.Failure, ErrorCode: code, RequestID: RequestID(r.Context()),
			Props: map[string]any{"client_id": c.ID, "app": appKind(redirect)}})
		http.Redirect(w, r, withQuery(redirect, url.Values{"error": {code}, "error_description": {desc}, "state": {q.Get("state")}}), http.StatusFound)
	}
	switch {
	case q.Get("response_type") != "code":
		back("unsupported_response_type", "response_type must be code")
	case q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256":
		back("invalid_request", "PKCE with code_challenge_method S256 is required")
	case q.Get("resource") != "" && !s.isOwnResource(q.Get("resource")):
		back("invalid_target", "resource must be "+s.mcpURL())
	case s.webDir == "":
		authorizeProblem(w, "Approve on the web app", "This server has no web app to show the consent screen on. Set WEB_DIR.")
	default:
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(w, r, filepath.Join(s.webDir, "index.html"))
	}
}

// isOwnResource accepts this server's MCP address, or the server itself.
func (s *Server) isOwnResource(res string) bool {
	res = strings.TrimRight(res, "/")
	return res == s.mcpURL() || res == s.publicURL
}

func withQuery(base string, v url.Values) string {
	for k, vs := range v {
		if len(vs) == 0 || vs[0] == "" {
			delete(v, k)
		}
	}
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	q := u.Query()
	for k, vs := range v {
		q[k] = vs
	}
	u.RawQuery = q.Encode()
	return u.String()
}

var problemPage = template.Must(template.New("p").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}} · Outflare</title><style>body{font:16px/1.5 system-ui,sans-serif;max-width:32rem;margin:15vh auto;padding:0 1rem;color:#111}@media(prefers-color-scheme:dark){body{background:#0a0a0a;color:#eee}}h1{font-size:1.25rem}</style>
<h1>{{.Title}}</h1><p>{{.Body}}</p><p><a href="/">Go to Outflare</a></p></html>`))

func authorizeProblem(w http.ResponseWriter, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusBadRequest)
	problemPage.Execute(w, map[string]string{"Title": title, "Body": body}) //nolint:errcheck
}

type agentWorkspace struct {
	ID         string   `json:"id"`
	Slug       string   `json:"slug"`
	Name       string   `json:"name"`
	Role       string   `json:"role"`
	FullScopes []string `json:"full_scopes"`
	ReadScopes []string `json:"read_scopes"`
	CanSpend   bool     `json:"can_spend"`
	// Brands is set when the person is kept to some brands: the app is
	// kept to one of them, which they pick.
	Brands []agentBrand `json:"brands"`
	scopes []string
}

type agentBrand struct {
	ID     string `json:"id"`
	Handle string `json:"handle"`
	Name   string `json:"name"`
}

// limitedBrands is the brands a person is kept to in a workspace, or nil
// for every brand. A role with workspace-wide permissions always sees
// every brand (as in authn).
func limitedBrands(scopes []string, brands []string) []string {
	if len(brands) == 0 || !auth.BrandLimitable(scopes) {
		return nil
	}
	return brands
}

// userWorkspaces lists the workspaces a person can connect an app to, with
// their role in each.
func (s *Server) userWorkspaces(ctx context.Context, userID string) ([]agentWorkspace, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT wsx.id, wsx.slug, wsx.name, r.role, r.brand_ids::text[], r.scopes FROM workspaces wsx
		CROSS JOIN LATERAL (SELECT role, brand_ids, scopes FROM (`+strings.ReplaceAll(strings.ReplaceAll(workspaceRoles, "$1", "wsx.id"), "$2", "$1")+`) x ORDER BY rank DESC, cardinality(brand_ids) LIMIT 1) r
		ORDER BY wsx.name`, userID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (agentWorkspace, error) {
		var v agentWorkspace
		var brands, stored []string
		err := row.Scan(&v.ID, &v.Slug, &v.Name, &v.Role, &brands, &stored)
		v.scopes = effectiveScopes(v.Role, stored)
		if ids := limitedBrands(v.scopes, brands); ids != nil {
			v.Brands = []agentBrand{}
			for _, id := range ids {
				v.Brands = append(v.Brands, agentBrand{ID: id})
			}
		}
		v.FullScopes, v.ReadScopes = agentScopes(v.scopes, "full", false), agentScopes(v.scopes, "read", false)
		v.CanSpend = slices.Contains(v.scopes, auth.ScopeInfraApprove)
		return v, err
	})
	if err != nil {
		return nil, err
	}
	for i := range list {
		for j, b := range list[i].Brands {
			if err := s.pool.QueryRow(ctx, `SELECT handle, name FROM brands WHERE id = $1 AND workspace_id = $2`, b.ID, list[i].ID).
				Scan(&list[i].Brands[j].Handle, &list[i].Brands[j].Name); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return nil, err
			}
		}
	}
	return list, nil
}

func (s *Server) getAgentAuthorization(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	q := r.URL.Query()
	c, err := s.loadOAuthClient(r.Context(), q.Get("client_id"))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	redirect := q.Get("redirect_uri")
	if c == nil || !redirectMatches(c.RedirectURIs, redirect) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such app, or it did not register that redirect_uri")
		return
	}
	list, err := s.userWorkspaces(r.Context(), p.UserID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	u, _ := url.Parse(redirect)
	writeJSON(w, http.StatusOK, map[string]any{
		"client":        map[string]any{"id": c.ID, "name": c.Name, "uri": c.URI},
		"app":           appKind(redirect),
		"verified":      appVerified(appKind(redirect), c.VerifiedAt),
		"redirect_host": u.Host,
		"resource":      s.mcpURL(),
		"workspaces":    list,
	})
}

func (s *Server) approveAgentAuthorization(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	ctx := r.Context()
	var req struct {
		ClientID            string   `json:"client_id"`
		RedirectURI         string   `json:"redirect_uri"`
		State               string   `json:"state"`
		CodeChallenge       string   `json:"code_challenge"`
		CodeChallengeMethod string   `json:"code_challenge_method"`
		WorkspaceID         string   `json:"workspace_id"`
		Access              string   `json:"access"`
		AllowSpending       bool     `json:"allow_spending"`
		Scopes              []string `json:"scopes"`
		Brand               string   `json:"brand"`
		Approve             bool     `json:"approve"`
	}
	if !decode(w, r, &req) {
		return
	}
	c, err := s.loadOAuthClient(ctx, req.ClientID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if c == nil || !redirectMatches(c.RedirectURIs, req.RedirectURI) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such app, or it did not register that redirect_uri")
		return
	}
	app := appKind(req.RedirectURI)
	if !req.Approve {
		track.Emit(ctx, track.Event{Name: "agent_app.connect_denied", UserID: p.UserID, ActorID: p.ActorID, ActorType: p.ActorType, Surface: "web",
			Outcome: track.Failure, ErrorCode: "access_denied", RequestID: RequestID(ctx), Props: map[string]any{"client_id": c.ID, "app": app}})
		writeJSON(w, http.StatusOK, map[string]any{"redirect_url": withQuery(req.RedirectURI, url.Values{"error": {"access_denied"},
			"error_description": {"The person declined to connect the app."}, "state": {req.State}})})
		return
	}
	if req.CodeChallenge == "" || req.CodeChallengeMethod != "S256" || len(req.CodeChallenge) > 128 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "code_challenge must be an S256 PKCE challenge")
		return
	}
	if req.Scopes == nil && req.Access != "full" && req.Access != "read" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "access must be full or read")
		return
	}
	list, err := s.userWorkspaces(ctx, p.UserID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	i := slices.IndexFunc(list, func(v agentWorkspace) bool { return v.ID == req.WorkspaceID || v.Slug == req.WorkspaceID })
	if i < 0 {
		writeError(w, r, http.StatusNotFound, "not_found", "workspace not found")
		return
	}
	ws := list[i]
	scopes := agentScopes(ws.scopes, req.Access, req.AllowSpending)
	// The consent screen's toggles send the exact scopes: the role's reads
	// always, plus whichever of its writes the person left on.
	if req.Scopes != nil {
		req.Scopes = auth.Rename(req.Scopes)
		allowed := agentScopes(ws.scopes, "full", true)
		for _, sc := range req.Scopes {
			if !slices.Contains(allowed, sc) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "an app can't be given "+sc+" in this workspace")
				return
			}
		}
		reads := agentScopes(ws.scopes, "read", false)
		scopes = slices.DeleteFunc(slices.Clone(allowed), func(sc string) bool { return !slices.Contains(reads, sc) && !slices.Contains(req.Scopes, sc) })
	}
	if !appVerified(app, c.VerifiedAt) {
		scopes = onlyReads(scopes)
	}
	if len(scopes) == 0 {
		writeError(w, r, http.StatusForbidden, "forbidden", "your role gives an app nothing to do in this workspace")
		return
	}
	// A person kept to some brands connects an app for one of them; the
	// app's token is kept to that brand.
	var brand *string
	if ws.Brands != nil {
		j := slices.IndexFunc(ws.Brands, func(b agentBrand) bool { return req.Brand != "" && (b.ID == req.Brand || b.Handle == req.Brand) })
		if j < 0 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "pick one of your brands: you are kept to some brands in this workspace")
			return
		}
		brand = &ws.Brands[j].ID
	}
	code, hash := auth.NewToken(prefixOAuthCode)
	_, err = s.pool.Exec(ctx, `
		INSERT INTO oauth_codes (code_hash, client_id, user_id, workspace_id, scopes, redirect_uri, code_challenge, brand_id, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, hash, c.ID, p.UserID, ws.ID, scopes, req.RedirectURI, req.CodeChallenge, brand, time.Now().Add(agentCodeTTL))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	noteWorkspace(r, ws.ID)
	writeJSON(w, http.StatusOK, map[string]any{"redirect_url": withQuery(req.RedirectURI, url.Values{"code": {code}, "state": {req.State},
		"iss": {s.publicURL}})})
}

// ---- Tokens ----

func (s *Server) oauthToken(w http.ResponseWriter, r *http.Request) {
	allowAnyOrigin(w)
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "send the request as application/x-www-form-urlencoded")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		s.exchangeCode(w, r)
	case "refresh_token":
		s.refreshAgentToken(w, r)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "grant_type must be authorization_code or refresh_token")
	}
}

// errOAuth is a token request refused with an OAuth error code.
type errOAuth struct{ code, desc string }

func (e *errOAuth) Error() string { return e.code + ": " + e.desc }

func pkceOK(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(challenge)) == 1
}

// tokenFailed answers a refused or failed token request and records why.
func (s *Server) tokenFailed(w http.ResponseWriter, r *http.Request, event, workspaceID string, err error) {
	ctx := r.Context()
	e := track.Event{Name: event, WorkspaceID: workspaceID, Surface: "api", RequestID: RequestID(ctx)}
	var oe *errOAuth
	if errors.As(err, &oe) {
		e.Outcome, e.ErrorCode = track.Failure, oe.code
		track.Emit(ctx, e)
		oauthError(w, http.StatusBadRequest, oe.code, oe.desc)
		return
	}
	track.Fail(ctx, s.log, e, "internal", err)
	oauthError(w, http.StatusInternalServerError, "server_error", "something went wrong; try again")
}

func (s *Server) exchangeCode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := r.PostForm
	if res := f.Get("resource"); res != "" && !s.isOwnResource(res) {
		oauthError(w, http.StatusBadRequest, "invalid_target", "resource must be "+s.mcpURL())
		return
	}
	var wsID string
	var out map[string]any
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var clientID, userID, redirect, challenge string
		var scopes []string
		var expires time.Time
		var used *time.Time
		var brand *string
		err := tx.QueryRow(ctx, `SELECT client_id, user_id, workspace_id, scopes, redirect_uri, code_challenge, brand_id::text, expires_at, used_at
			FROM oauth_codes WHERE code_hash = $1 FOR UPDATE`, auth.HashToken(f.Get("code"))).
			Scan(&clientID, &userID, &wsID, &scopes, &redirect, &challenge, &brand, &expires, &used)
		scopes = auth.Rename(scopes)
		if errors.Is(err, pgx.ErrNoRows) {
			return &errOAuth{"invalid_grant", "the code is not valid"}
		}
		if err != nil {
			return err
		}
		switch {
		case used != nil:
			return &errOAuth{"invalid_grant", "the code was already used"}
		case time.Now().After(expires):
			return &errOAuth{"invalid_grant", "the code expired; connect again"}
		case f.Get("client_id") != clientID:
			return &errOAuth{"invalid_grant", "the code was issued to another client"}
		case f.Get("redirect_uri") != redirect:
			return &errOAuth{"invalid_grant", "redirect_uri does not match the one the code was issued for"}
		case !pkceOK(f.Get("code_verifier"), challenge):
			return &errOAuth{"invalid_grant", "code_verifier does not match the code_challenge"}
		}
		if _, err := tx.Exec(ctx, `UPDATE oauth_codes SET used_at = now() WHERE code_hash = $1`, auth.HashToken(f.Get("code"))); err != nil {
			return err
		}
		// The person may have lost access since they approved.
		scopes, err = s.stillGranted(ctx, tx, wsID, userID, scopes, brand)
		if err != nil {
			return err
		}
		var name, actorID string
		var verifiedAt *time.Time
		if err := tx.QueryRow(ctx, `UPDATE oauth_clients SET last_used_at = now() WHERE id = $1 RETURNING name, verified_at`, clientID).Scan(&name, &verifiedAt); err != nil {
			return err
		}
		if scopes, err = verifiedScopes(appKind(redirect), verifiedAt, scopes); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO actors (workspace_id, type, display_name) VALUES ($1, 'api_token', $2) RETURNING id`, wsID, name).Scan(&actorID); err != nil {
			return err
		}
		access, accessHash := auth.NewToken(auth.PrefixAPIToken)
		refresh, refreshHash := auth.NewToken(prefixOAuthRefresh)
		var tokenID, grantID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO api_tokens (workspace_id, actor_id, name, prefix, token_hash, scopes, created_by, expires_at, brand_id, require_mission)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, true) RETURNING id`,
			wsID, actorID, name, access[:len(auth.PrefixAPIToken)+6], accessHash, scopes, userID, time.Now().Add(agentAccessTTL), brand).Scan(&tokenID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO oauth_grants (workspace_id, client_id, user_id, api_token_id, refresh_hash, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, wsID, clientID, userID, tokenID, refreshHash, time.Now().Add(agentRefreshTTL)).Scan(&grantID); err != nil {
			return err
		}
		person, err := s.personPrincipal(ctx, tx, userID)
		if err != nil {
			return err
		}
		if err := s.replaceEarlierGrants(ctx, tx, person, wsID, userID, clientID, grantID, appKind(redirect)); err != nil {
			return err
		}
		if err := record(ctx, tx, person, wsID, "agent_app.connected", "api_token", tokenID, map[string]any{
			"grant_id": grantID, "client_id": clientID, "app": appKind(redirect), "scopes": scopes}); err != nil {
			return err
		}
		out = map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": int(agentAccessTTL.Seconds()),
			"refresh_token": refresh, "scope": strings.Join(scopes, " ")}
		return nil
	})
	if err != nil {
		s.tokenFailed(w, r, "agent_app.connect_failed", wsID, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// replaceEarlierGrants disconnects the person's earlier connections of the
// same app in the workspace: connecting again is how someone goes through
// the approval screen once more, so it replaces the old connection rather
// than leaving two. A known app (Claude, ChatGPT...) may register a new
// client each time, so it matches by kind; any other app by client.
func (s *Server) replaceEarlierGrants(ctx context.Context, tx pgx.Tx, person *principal, wsID, userID, clientID, grantID, kind string) error {
	rows, err := tx.Query(ctx, `
		SELECT g.id, t.id, g.client_id, c.redirect_uris[1] FROM oauth_grants g
		JOIN api_tokens t ON t.id = g.api_token_id
		JOIN oauth_clients c ON c.id = g.client_id
		WHERE g.workspace_id = $1 AND g.user_id = $2 AND g.id <> $3 AND t.revoked_at IS NULL
		FOR UPDATE OF t`, wsID, userID, grantID)
	if err != nil {
		return err
	}
	type earlier struct{ grant, token string }
	var old []earlier
	for rows.Next() {
		var e earlier
		var client, redirect string
		if err := rows.Scan(&e.grant, &e.token, &client, &redirect); err != nil {
			rows.Close()
			return err
		}
		if client == clientID || (kind != "unknown" && kind != "local" && appKind(redirect) == kind) {
			old = append(old, e)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, e := range old {
		if _, err := tx.Exec(ctx, `UPDATE api_tokens SET revoked_at = now() WHERE id = $1`, e.token); err != nil {
			return err
		}
		if err := record(ctx, tx, person, wsID, "agent_app.disconnected", "api_token", e.token, map[string]any{"grant_id": e.grant, "reason": "replaced", "replaced_by": grantID}); err != nil {
			return err
		}
	}
	return nil
}

// stillGranted narrows scopes to what the person's role allows now, and
// refuses when they are no longer in the workspace.
func (s *Server) stillGranted(ctx context.Context, tx pgx.Tx, wsID, userID string, scopes []string, brand *string) ([]string, error) {
	var role string
	var brands []string
	var stored []string
	err := tx.QueryRow(ctx, `SELECT role, brand_ids::text[], scopes FROM (`+workspaceRoles+`) r ORDER BY rank DESC, cardinality(brand_ids) LIMIT 1`, wsID, userID).Scan(&role, &brands, &stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, &errOAuth{"invalid_grant", "the person who connected this app no longer has access to the workspace"}
	}
	if err != nil {
		return nil, err
	}
	var disabled bool
	if err := tx.QueryRow(ctx, `SELECT disabled_at IS NOT NULL FROM users WHERE id = $1`, userID).Scan(&disabled); err != nil {
		return nil, err
	}
	if disabled {
		return nil, &errOAuth{"invalid_grant", "the account that connected this app is disabled"}
	}
	// Kept to some brands since approving (or no longer to this one): the
	// app may not see more than the person.
	allowed := effectiveScopes(role, stored)
	if ids := limitedBrands(allowed, brands); ids != nil && (brand == nil || !slices.Contains(ids, *brand)) {
		return nil, &errOAuth{"invalid_grant", "the person who connected this app no longer works on its brand; connect it again"}
	}
	out := slices.DeleteFunc(slices.Clone(scopes), func(sc string) bool { return !slices.Contains(allowed, sc) })
	if len(out) == 0 {
		return nil, &errOAuth{"invalid_grant", "the person's role no longer allows any of what this app was given"}
	}
	return out, nil
}

// verifiedScopes keeps an unverified app to reading.
func verifiedScopes(kind string, verifiedAt *time.Time, scopes []string) ([]string, error) {
	if appVerified(kind, verifiedAt) {
		return scopes, nil
	}
	if scopes = onlyReads(scopes); len(scopes) == 0 {
		return nil, &errOAuth{"invalid_grant", "this app isn't verified, so it may only read, and it was given nothing to read"}
	}
	return scopes, nil
}

// personPrincipal is a user acting outside their own request: they approved
// the app, and the app's token request completes it.
func (s *Server) personPrincipal(ctx context.Context, tx pgx.Tx, userID string) (*principal, error) {
	p := &principal{ActorType: "human", UserID: userID}
	err := tx.QueryRow(ctx, `SELECT actor_id FROM users WHERE id = $1`, userID).Scan(&p.ActorID)
	return p, err
}

func (s *Server) refreshAgentToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := r.PostForm
	hash := auth.HashToken(f.Get("refresh_token"))
	var wsID string
	var out map[string]any
	var reused bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var grantID, clientID, userID, tokenID string
		var current []byte
		var rotated *time.Time
		var expires time.Time
		var revoked bool
		var scopes []string
		var brand *string
		var redirect string
		var verifiedAt *time.Time
		err := tx.QueryRow(ctx, `
			SELECT g.id, g.workspace_id, g.client_id, g.user_id, g.api_token_id, g.refresh_hash, g.rotated_at, g.expires_at, t.revoked_at IS NOT NULL, t.scopes, t.brand_id::text,
				c.redirect_uris[1], c.verified_at
			FROM oauth_grants g JOIN api_tokens t ON t.id = g.api_token_id JOIN oauth_clients c ON c.id = g.client_id
			WHERE g.refresh_hash = $1 OR g.previous_refresh_hash = $1 FOR UPDATE OF g`, hash).
			Scan(&grantID, &wsID, &clientID, &userID, &tokenID, &current, &rotated, &expires, &revoked, &scopes, &brand, &redirect, &verifiedAt)
		scopes = auth.Rename(scopes)
		if errors.Is(err, pgx.ErrNoRows) {
			return &errOAuth{"invalid_grant", "the refresh token is not valid; connect the app again"}
		}
		if err != nil {
			return err
		}
		switch {
		case revoked:
			return &errOAuth{"invalid_grant", "the app was disconnected; connect it again"}
		case f.Get("client_id") != "" && f.Get("client_id") != clientID:
			return &errOAuth{"invalid_grant", "the refresh token was issued to another client"}
		case time.Now().After(expires):
			return &errOAuth{"invalid_grant", "the connection expired; connect the app again"}
		case subtle.ConstantTimeCompare(current, hash) != 1 && (rotated == nil || time.Since(*rotated) > agentRefreshGrace):
			// An old refresh token used again: someone may have copied it.
			// Disconnect the app so neither copy works.
			reused = true
			if _, err := tx.Exec(ctx, `UPDATE api_tokens SET revoked_at = now() WHERE id = $1`, tokenID); err != nil {
				return err
			}
			return record(ctx, tx, nil, wsID, "agent_app.disconnected", "api_token", tokenID, map[string]any{"grant_id": grantID, "reason": "refresh_token_reused"})
		}
		if scopes, err = s.stillGranted(ctx, tx, wsID, userID, scopes, brand); err != nil {
			return err
		}
		if scopes, err = verifiedScopes(appKind(redirect), verifiedAt, scopes); err != nil {
			return err
		}
		access, accessHash := auth.NewToken(auth.PrefixAPIToken)
		refresh, refreshHash := auth.NewToken(prefixOAuthRefresh)
		if _, err := tx.Exec(ctx, `UPDATE api_tokens SET token_hash = $2, prefix = $3, scopes = $4, expires_at = $5 WHERE id = $1`,
			tokenID, accessHash, access[:len(auth.PrefixAPIToken)+6], scopes, time.Now().Add(agentAccessTTL)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE oauth_grants SET previous_refresh_hash = refresh_hash, refresh_hash = $2, rotated_at = now(), refreshed_at = now(),
			expires_at = $3 WHERE id = $1`, grantID, refreshHash, time.Now().Add(agentRefreshTTL)); err != nil {
			return err
		}
		out = map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": int(agentAccessTTL.Seconds()),
			"refresh_token": refresh, "scope": strings.Join(scopes, " ")}
		return nil
	})
	if err == nil && reused {
		// The request fails, so the audit action's event is not sent with
		// it; send the disconnect on its own.
		track.Emit(ctx, track.Event{Name: "agent_app.disconnected", WorkspaceID: wsID, ActorType: "system", Surface: "api", Outcome: track.Failure,
			ErrorCode: "invalid_grant", RequestID: RequestID(ctx), Props: map[string]any{"reason": "refresh_token_reused"}})
		err = &errOAuth{"invalid_grant", "this refresh token was already used, so the app was disconnected to be safe; connect it again"}
	}
	if err != nil {
		s.tokenFailed(w, r, "agent_app.refresh_failed", wsID, err)
		return
	}
	track.Emit(ctx, track.Event{Name: "agent_app.token_refreshed", WorkspaceID: wsID, Surface: "api", RequestID: RequestID(ctx)})
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// oauthRevoke (RFC 7009) lets an app disconnect itself with its access or
// refresh token. It answers 200 whether or not the token was valid.
func (s *Server) oauthRevoke(w http.ResponseWriter, r *http.Request) {
	allowAnyOrigin(w)
	ctx := r.Context()
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "send the request as application/x-www-form-urlencoded")
		return
	}
	hash := auth.HashToken(r.PostForm.Get("token"))
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var wsID, tokenID, grantID, userID string
		err := tx.QueryRow(ctx, `
			SELECT g.workspace_id, g.api_token_id, g.id, g.user_id FROM oauth_grants g JOIN api_tokens t ON t.id = g.api_token_id
			WHERE (g.refresh_hash = $1 OR t.token_hash = $1) AND t.revoked_at IS NULL FOR UPDATE OF t`, hash).Scan(&wsID, &tokenID, &grantID, &userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE api_tokens SET revoked_at = now() WHERE id = $1`, tokenID); err != nil {
			return err
		}
		return record(ctx, tx, nil, wsID, "agent_app.disconnected", "api_token", tokenID, map[string]any{"grant_id": grantID, "reason": "app_revoked"})
	})
	if err != nil {
		s.tokenFailed(w, r, "agent_app.disconnect_failed", "", err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// ---- Connected apps ----

type agentAppView struct {
	ID      string   `json:"id"`
	TokenID string   `json:"token_id"`
	Name    string   `json:"name"`
	App     string   `json:"app"`
	URI     string   `json:"uri"`
	Scopes  []string `json:"scopes"`
	// The write scopes the app could be given: the switches to show.
	AvailableScopes []string   `json:"available_scopes"`
	UserID          string     `json:"user_id"`
	UserName        string     `json:"user_name"`
	CreatedAt       time.Time  `json:"created_at"`
	LastUsedAt      *time.Time `json:"last_used_at"`
	// Recognised or checked by the operator; an unverified app only reads.
	// An operator verifies one by its client id (outflare verify-app).
	Verified bool   `json:"verified"`
	ClientID string `json:"client_id"`
	// Its changes wait for a mission a person approved.
	RequireMission bool `json:"require_mission"`
}

// listAgentApps lists the apps connected with OAuth. Members see the ones
// they connected; people who manage tokens see everyone's.
func (s *Server) listAgentApps(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	all := a.has(auth.ScopeTokensManage)
	rows, err := s.pool.Query(r.Context(), `
		SELECT g.id, t.id, c.name, c.redirect_uris[1], c.uri, t.scopes, g.user_id, COALESCE(NULLIF(u.name, ''), u.email), g.created_at, t.last_used_at,
			c.verified_at, t.require_mission, c.id
		FROM oauth_grants g
		JOIN api_tokens t ON t.id = g.api_token_id
		JOIN oauth_clients c ON c.id = g.client_id
		JOIN users u ON u.id = g.user_id
		WHERE g.workspace_id = $1 AND t.revoked_at IS NULL AND g.expires_at > now() AND ($2 OR g.user_id::text = $3)
		ORDER BY g.created_at DESC`, a.WorkspaceID, all, a.UserID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (agentAppView, error) {
		var v agentAppView
		var redirect string
		var verifiedAt *time.Time
		err := row.Scan(&v.ID, &v.TokenID, &v.Name, &redirect, &v.URI, &v.Scopes, &v.UserID, &v.UserName, &v.CreatedAt, &v.LastUsedAt, &verifiedAt, &v.RequireMission, &v.ClientID)
		v.Scopes = auth.Rename(v.Scopes)
		v.App = appKind(redirect)
		v.Verified = appVerified(v.App, verifiedAt)
		return v, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	roles := map[string][]string{}
	for i := range list {
		u := list[i].UserID
		if _, ok := roles[u]; !ok {
			if roles[u], err = s.memberRole(r.Context(), s.pool, a.WorkspaceID, u); err != nil {
				s.internal(w, r, err)
				return
			}
		}
		if list[i].Verified {
			list[i].AvailableScopes = agentWrites(roles[u])
		} else {
			list[i].AvailableScopes = []string{}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// agentWrites is the write scopes an app connected by someone whose role
// allows role may be given: everything the role allows but agentWithheld,
// and spending when the role may approve purchases. No role gives nothing.
func agentWrites(role []string) []string {
	return slices.DeleteFunc(agentScopes(role, "full", true), func(sc string) bool { return strings.HasSuffix(sc, ".read") })
}

// memberRole is what a person's highest role in a workspace allows, or
// nil when they are no longer in it.
func (s *Server) memberRole(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, wsID, userID string) ([]string, error) {
	var role string
	var stored []string
	err := q.QueryRow(ctx, `SELECT role, scopes FROM (`+workspaceRoles+`) r ORDER BY rank DESC LIMIT 1`, wsID, userID).Scan(&role, &stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return effectiveScopes(role, stored), err
}

// updateAgentApp changes what a connected app may do, without connecting it
// again. Only a person can: the app's own token must never widen itself.
func (s *Server) updateAgentApp(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "app not found")
		return
	}
	if a.ActorType != "human" {
		writeError(w, r, http.StatusForbidden, "forbidden", "only a person can change what a connected app may do, in Outflare under Connect your AI")
		return
	}
	var req struct {
		Scopes         []string `json:"scopes"`
		RequireMission *bool    `json:"require_mission"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Scopes == nil && req.RequireMission == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send scopes (the write scopes to allow, or [] for read only), require_mission, or both")
		return
	}
	ctx := r.Context()
	var v agentAppView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var tokenID, redirect string
		var scopes []string
		var verifiedAt *time.Time
		var requireMission bool
		err := tx.QueryRow(ctx, `
			SELECT t.id, t.scopes, g.user_id, c.name, c.redirect_uris[1], c.uri, COALESCE(NULLIF(u.name, ''), u.email), g.created_at, t.last_used_at,
				c.verified_at, t.require_mission, c.id
			FROM oauth_grants g
			JOIN api_tokens t ON t.id = g.api_token_id
			JOIN oauth_clients c ON c.id = g.client_id
			JOIN users u ON u.id = g.user_id
			WHERE g.id = $1 AND g.workspace_id = $2 AND t.revoked_at IS NULL AND g.expires_at > now()
			FOR UPDATE OF t`, id, a.WorkspaceID).Scan(&tokenID, &scopes, &v.UserID, &v.Name, &redirect, &v.URI, &v.UserName, &v.CreatedAt, &v.LastUsedAt, &verifiedAt, &requireMission, &v.ClientID)
		scopes = auth.Rename(scopes)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		// Someone else's app: only for people who manage tokens, and it
		// looks missing to the rest.
		mine := v.UserID == a.UserID
		if !mine && !a.has(auth.ScopeTokensManage) {
			return errNotFound
		}
		role, err := s.memberRole(ctx, tx, a.WorkspaceID, v.UserID)
		if err != nil {
			return err
		}
		verified := appVerified(appKind(redirect), verifiedAt)
		writes := agentWrites(role)
		if !verified {
			writes = []string{}
		}
		req.Scopes = auth.Rename(req.Scopes)
		if req.Scopes == nil {
			// Only require_mission changes: keep its writes (none, unverified).
			req.Scopes = []string{}
			if verified {
				req.Scopes = slices.DeleteFunc(slices.Clone(scopes), func(sc string) bool { return strings.HasSuffix(sc, ".read") })
			}
		}
		for _, sc := range req.Scopes {
			if !verified {
				return errDenied("this app isn't verified, so it may only read: an app Outflare doesn't recognise needs the server's operator to check it first")
			}
			if !slices.Contains(writes, sc) {
				return errInvalid("an app can't be given " + sc + " here")
			}
			// An admin widening someone else's app can't hand it more than
			// they have themselves.
			if !mine && !slices.Contains(scopes, sc) && !a.has(sc) {
				return errDenied("you cannot grant scope " + sc)
			}
		}
		next := agentScopes(role, "read", false)
		for _, sc := range writes {
			if slices.Contains(req.Scopes, sc) {
				next = append(next, sc)
			}
		}
		if len(next) == 0 {
			return errDenied("the role of the person who connected this app gives it nothing to do here")
		}
		if req.RequireMission != nil {
			requireMission = *req.RequireMission
		}
		if _, err := tx.Exec(ctx, `UPDATE api_tokens SET scopes = $2, require_mission = $3 WHERE id = $1`, tokenID, next, requireMission); err != nil {
			return err
		}
		v.ID, v.TokenID, v.Scopes, v.AvailableScopes, v.App = id, tokenID, next, writes, appKind(redirect)
		v.Verified, v.RequireMission = verified, requireMission
		added := slices.DeleteFunc(slices.Clone(next), func(sc string) bool { return slices.Contains(scopes, sc) })
		removed := slices.DeleteFunc(slices.Clone(scopes), func(sc string) bool { return slices.Contains(next, sc) })
		return record(ctx, tx, &a.principal, a.WorkspaceID, "agent_app.updated", "api_token", tokenID, map[string]any{
			"grant_id": id, "app": v.App, "scopes": next, "scopes_added": added, "scopes_removed": removed, "require_mission": requireMission})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) disconnectAgentApp(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "app not found")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var tokenID, userID string
		err := tx.QueryRow(ctx, `
			SELECT t.id, g.user_id FROM oauth_grants g JOIN api_tokens t ON t.id = g.api_token_id
			WHERE g.id = $1 AND g.workspace_id = $2 AND t.revoked_at IS NULL FOR UPDATE OF t`, id, a.WorkspaceID).Scan(&tokenID, &userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		// Someone else's app: only for people who manage tokens, and it
		// looks missing to the rest.
		if userID != a.UserID && !a.has(auth.ScopeTokensManage) {
			return errNotFound
		}
		if _, err := tx.Exec(ctx, `UPDATE api_tokens SET revoked_at = now() WHERE id = $1`, tokenID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "agent_app.disconnected", "api_token", tokenID, map[string]any{"grant_id": id, "reason": "disconnected"})
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "app not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PruneOAuthCodes deletes authorization codes an hour after they expire, and
// apps that registered but were never approved within a day.
func (s *Server) PruneOAuthCodes(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM oauth_codes WHERE expires_at < now() - interval '1 hour'`)
	if err != nil {
		return 0, err
	}
	n := tag.RowsAffected()
	tag, err = s.pool.Exec(ctx, `DELETE FROM oauth_clients c WHERE last_used_at IS NULL AND created_at < now() - interval '1 day'
		AND NOT EXISTS (SELECT 1 FROM oauth_codes o WHERE o.client_id = c.id)`)
	return n + tag.RowsAffected(), err
}
