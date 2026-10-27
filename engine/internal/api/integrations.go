package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/logos"
	"github.com/outflareai/outflare/engine/internal/inbox"
)

func (s *Server) routesIntegrations(mux *http.ServeMux) {
	read, manage := auth.ScopeIntegrationsRead, auth.ScopeIntegrationsManage
	mux.HandleFunc("GET /v1/connectors", s.connectorCatalog)
	mux.HandleFunc("GET /v1/connectors/{id}/logo.svg", s.connectorLogo)
	mux.HandleFunc("GET /v1/workspaces/{ws}/connectors", s.inBrand(read, s.workspaceConnectors))
	mux.HandleFunc("GET /v1/workspaces/{ws}/integrations", s.inBrand(read, s.listIntegrations))
	mux.HandleFunc("POST /v1/workspaces/{ws}/integrations", s.inWorkspace(manage, s.createIntegration))
	mux.HandleFunc("GET /v1/workspaces/{ws}/integrations/{id}", s.inBrand(read, s.getIntegration))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/integrations/{id}", s.inWorkspace(manage, s.updateIntegration))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/integrations/{id}", s.inWorkspace(manage, s.deleteIntegration))
	mux.HandleFunc("POST /v1/workspaces/{ws}/integrations/{id}/test", s.inWorkspace(manage, s.testIntegration))
	mux.HandleFunc("GET /v1/workspaces/{ws}/integrations/oauth/{connector}/authorize", s.inWorkspace(manage, s.integrationAuthorize))
	mux.HandleFunc("GET /v1/workspaces/{ws}/integrations/{id}/sign-in", s.inWorkspace(manage, s.integrationSignIn))
	mux.HandleFunc("GET /v1/integrations/oauth/callback", s.integrationCallback)
	mux.HandleFunc("GET /v1/integrations/oauth/clients/{file}", s.integrationClientMetadata)
}

func (s *Server) needIntegrations(w http.ResponseWriter, r *http.Request) bool {
	if s.integrations == nil {
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "connectors are not configured on this server")
		return false
	}
	return true
}

// catalogEntry is a connector definition plus what this server and
// workspace know about it.
type catalogEntry struct {
	connectors.Definition
	// Configured: at least one way to connect works on this server.
	Configured bool `json:"configured"`
	// AuthOptions: each way to connect, recommended first, and whether
	// this server can use it (OAuth needs an app configured).
	AuthOptions []authOption `json:"auth_options"`
	Connected   int          `json:"connected"`
	// The company's logo as an SVG, or a monogram when we have none.
	LogoURL     string `json:"logo_url"`
	LogoDarkURL string `json:"logo_dark_url"`
}

// logoRev changes a logo's address once, so browsers drop the copies
// they kept for a day before logos were revalidated. Logos are checked
// against their ETag on every load now, so it never needs to change again.
const logoRev = "2"

func (s *Server) logoURL(id string, dark bool) string {
	u := s.publicURL + "/v1/connectors/" + url.PathEscape(id) + "/logo.svg?v=" + logoRev
	if dark {
		u += "&theme=dark"
	}
	return u
}

type authOption struct {
	Method      string `json:"method"`
	Available   bool   `json:"available"`
	Recommended bool   `json:"recommended"`
	// Why it is not available, and what the server's owner can do.
	Reason string `json:"reason,omitempty"`
}

// authOptions lists a connector's ways to connect on this server. The
// recommended one is the first that works.
func (s *Server) authOptions(d connectors.Definition) ([]authOption, bool) {
	out := []authOption{}
	any := false
	for _, m := range d.Methods() {
		o := authOption{Method: m, Available: d.Status == connectors.Available}
		switch {
		case !o.Available:
			o.Reason = "planned: cannot be connected yet"
		case m == connectors.AuthOAuth2:
			if _, ok := s.integrations.OAuthApps[d.ID]; !ok || d.OAuth == nil {
				o.Available = false
				o.Reason = "needs an OAuth app on this server: " + connectors.OAuthHint(d)
			}
		}
		if o.Available && !any {
			o.Recommended, any = true, true
		}
		out = append(out, o)
	}
	return out, any
}

func (s *Server) entry(d connectors.Definition, connected int) catalogEntry {
	// OAuth-only connectors have no fields; clients expect lists, not null.
	if d.Fields == nil {
		d.Fields = []connectors.Field{}
	}
	if d.Capabilities == nil {
		d.Capabilities = []string{}
	}
	opts, configured := s.authOptions(d)
	return catalogEntry{Definition: d, Configured: configured, AuthOptions: opts, Connected: connected, LogoURL: s.logoURL(d.ID, false), LogoDarkURL: s.logoURL(d.ID, true)}
}

func (s *Server) catalog(counts map[string]int) []catalogEntry {
	defs := s.integrations.Registry.All()
	out := make([]catalogEntry, len(defs))
	for i, d := range defs {
		out[i] = s.entry(d, counts[d.ID])
	}
	return out
}

// connectorCatalog is public: the website and agents read it without a key.
func (s *Server) connectorCatalog(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": s.catalog(nil)})
}

// connectorLogo is public and cacheable, so <img> tags, the website and
// agents can use it without a key. ?theme=dark picks the variant for dark
// backgrounds.
func (s *Server) connectorLogo(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	id := r.PathValue("id")
	d, ok := s.integrations.Registry.Definition(id)
	if !ok {
		writeError(w, r, http.StatusNotFound, "not_found", "unknown connector "+id+"; GET /v1/connectors lists them")
		return
	}
	b, _ := logos.Get(d.ID, d.Name, r.URL.Query().Get("theme") == "dark")
	h := w.Header()
	// Revalidated on every load (a 304 when it hasn't changed), so a new
	// logo shows as soon as it ships instead of a day later.
	sum := sha256.Sum256(b)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	h.Set("ETag", etag)
	h.Set("Cache-Control", "public, no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", "image/svg+xml")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Access-Control-Allow-Origin", "*")
	w.Write(b)
}

func (s *Server) workspaceConnectors(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), `SELECT connector, count(*)::int FROM integrations WHERE workspace_id = $1 GROUP BY 1`, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	counts := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			rows.Close()
			s.internal(w, r, err)
			return
		}
		counts[id] = n
	}
	rows.Close()
	category := r.URL.Query().Get("category")
	out := []catalogEntry{}
	for _, e := range s.catalog(counts) {
		if category == "" || e.Category == category {
			out = append(out, e)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// integrationView adds connector display fields to an integration.
type integrationView struct {
	connectors.Integration
	ConnectorName string   `json:"connector_name"`
	Category      string   `json:"category"`
	Capabilities  []string `json:"capabilities"`
}

func (s *Server) view(i connectors.Integration) integrationView {
	v := integrationView{Integration: i, ConnectorName: i.Connector, Capabilities: []string{}}
	if d, ok := s.integrations.Registry.Definition(i.Connector); ok {
		v.ConnectorName, v.Category, v.Capabilities = d.Name, d.Category, d.Capabilities
	}
	return v
}

func (s *Server) listIntegrations(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	list, err := s.integrations.List(r.Context(), s.pool, a.WorkspaceID, "")
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list = integrationsFor(list, sel)
	connector := r.URL.Query().Get("connector")
	if connector != "" {
		connector = s.integrations.Registry.Resolve(connector)
	}
	category := r.URL.Query().Get("category")
	out := []integrationView{}
	for _, i := range list {
		if v := s.view(i); (category == "" || v.Category == category) && (connector == "" || i.Connector == connector) {
			out = append(out, v)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// integrationsFor keeps the connections a brand filter's brand uses: its
// own and the workspace's shared with it (connectors.Integration.Serves).
// "none" keeps the workspace connections every brand shares. A
// brand-limited caller sees only connections one of its brands uses.
func integrationsFor(list []connectors.Integration, sel brandSel) []connectors.Integration {
	all := list
	switch sel.id {
	case "":
	case "none":
		list = slices.DeleteFunc(slices.Clone(list), func(i connectors.Integration) bool { return i.BrandID != nil || len(i.BrandIDs) > 0 })
	default:
		list = connectors.ForBrand(list, sel.id)
	}
	if sel.in != nil {
		list = slices.DeleteFunc(slices.Clone(list), func(i connectors.Integration) bool {
			return !slices.ContainsFunc(sel.in, func(b string) bool { return i.Serves(b, all) })
		})
	}
	return list
}

func (s *Server) loadIntegration(w http.ResponseWriter, r *http.Request) (connectors.Integration, bool) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "integration not found")
		return connectors.Integration{}, false
	}
	i, err := s.integrations.Get(r.Context(), s.pool, a.WorkspaceID, id)
	if err == nil && a.Brands != nil {
		// A brand-limited caller sees only connections its brands use.
		var serves bool
		err = s.pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM integrations i, unnest($3::uuid[]) b
			WHERE i.workspace_id = $1 AND i.id = $2 AND integration_serves(i, b))`, a.WorkspaceID, i.ID, a.Brands).Scan(&serves)
		if err == nil && !serves {
			err = connectors.ErrNotFound
		}
	}
	if errors.Is(err, connectors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "integration not found")
		return i, false
	}
	if err != nil {
		s.internal(w, r, err)
		return i, false
	}
	if i.BrandID != nil {
		noteBrand(r.Context(), *i.BrandID)
	}
	return i, true
}

// integrationShare is who uses a connection: one brand's own (BrandID), or
// the workspace's shared with BrandIDs (empty: every brand).
type integrationShare struct {
	BrandID  *string
	BrandIDs []string
}

// errOwnAndShared: a brand's own connection is not shared with others.
var errOwnAndShared = errInvalid("a brand's own connection is used by that brand only; leave brand_ids empty, or set brand_id to null to share it from the workspace")

// integrationShareFrom resolves brand_id and brand_ids (ids or handles)
// from a request body.
func (s *Server) integrationShareFrom(ctx context.Context, ws string, brand *string, brands []string) (integrationShare, error) {
	var out integrationShare
	var err error
	if out.BrandID, err = s.brandRef(ctx, ws, brand); err != nil {
		return out, err
	}
	if out.BrandIDs, err = s.brandIDsFor(ctx, ws, brands); err != nil {
		return out, err
	}
	if out.BrandID != nil && len(out.BrandIDs) > 0 {
		return out, errOwnAndShared
	}
	return out, nil
}

// setShare saves who uses a connection, in tx.
func setShare(ctx context.Context, tx pgx.Tx, ws, id string, sh integrationShare) error {
	ids := sh.BrandIDs
	if ids == nil {
		ids = []string{}
	}
	_, err := tx.Exec(ctx, `UPDATE integrations SET brand_id = $3, brand_ids = $4::uuid[], updated_at = now() WHERE workspace_id = $1 AND id = $2`, ws, id, sh.BrandID, ids)
	return err
}

func (s *Server) getIntegration(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	if i, ok := s.loadIntegration(w, r); ok {
		writeJSON(w, http.StatusOK, s.view(i))
	}
}

// connectorError answers a failed connector call with its explanation.
func connectorError(w http.ResponseWriter, r *http.Request, err error) {
	code, status := "connector_"+connectors.KindOf(err), http.StatusBadGateway
	switch connectors.KindOf(err) {
	case connectors.ErrAuth, connectors.ErrInvalid:
		status = http.StatusUnprocessableEntity
	case connectors.ErrQuota:
		status = http.StatusTooManyRequests
	}
	writeError(w, r, status, code, err.Error())
}

func cleanName(n string) string {
	n = strings.TrimSpace(n)
	if len(n) > 100 {
		n = n[:100]
	}
	return n
}

func (s *Server) createIntegration(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	var req struct {
		Connector   string            `json:"connector"`
		Name        string            `json:"name"`
		Credentials map[string]string `json:"credentials"`
		BrandID     *string           `json:"brand_id"`
		BrandIDs    []string          `json:"brand_ids"`
	}
	if !decode(w, r, &req) {
		return
	}
	share, err := s.integrationShareFrom(r.Context(), a.WorkspaceID, req.BrandID, req.BrandIDs)
	if s.allocError(w, r, err) {
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
	case !def.Supports(connectors.AuthAPIKey):
		writeError(w, r, http.StatusBadRequest, "use_oauth", def.Name+" connects through its sign-in page: open GET /v1/workspaces/{ws}/integrations/oauth/"+def.ID+"/authorize in a browser")
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
	name := cleanName(req.Name)
	if name == "" {
		name = s.defaultName(r, a.WorkspaceID, def, account)
	}
	out, err := s.saveIntegration(r.Context(), &a.principal, a.WorkspaceID, def, connectors.AuthAPIKey, name, creds, account, "api", "", share)
	if isUnique(err) {
		writeError(w, r, http.StatusConflict, "conflict", "another connection is already named "+name)
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.view(out))
}

// defaultName is the connector's name, numbered when the workspace already
// has one: "ZeroBounce", "ZeroBounce 2", or the account's address.
func (s *Server) defaultName(r *http.Request, ws string, d connectors.Definition, acct connectors.Account) string {
	base := d.Name
	if acct.Email != "" {
		base = d.Name + " (" + acct.Email + ")"
	} else if acct.Name != "" {
		base = d.Name + " (" + acct.Name + ")"
	}
	name := base
	for n := 2; n < 100; n++ {
		var taken bool
		if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM integrations WHERE workspace_id = $1 AND lower(name) = lower($2))`, ws, name).Scan(&taken); err != nil || !taken {
			return name
		}
		name = base + " " + itoa(n)
	}
	return name
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func (s *Server) updateIntegration(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	i, ok := s.loadIntegration(w, r)
	if !ok {
		return
	}
	var req struct {
		Name        *string           `json:"name"`
		Credentials map[string]string `json:"credentials"`
		Config      json.RawMessage   `json:"config"`
		BrandID     optional[string]  `json:"brand_id"`
		BrandIDs    *[]string         `json:"brand_ids"`
	}
	if !decode(w, r, &req) {
		return
	}
	var share *integrationShare
	if req.BrandID.Set || req.BrandIDs != nil {
		// Unchanged parts keep their value; moving a connection to a brand
		// stops sharing it.
		brand, brands := i.BrandID, i.BrandIDs
		if req.BrandID.Set {
			brand = req.BrandID.Value
			if brand != nil && req.BrandIDs == nil {
				brands = nil
			}
		}
		if req.BrandIDs != nil {
			brands = *req.BrandIDs
		}
		sh, err := s.integrationShareFrom(r.Context(), a.WorkspaceID, brand, brands)
		if s.allocError(w, r, err) {
			return
		}
		share = &sh
	}
	def, _ := s.integrations.Registry.Definition(i.Connector)
	var creds connectors.Creds
	var account connectors.Account
	if req.Credentials != nil {
		if !def.Supports(connectors.AuthAPIKey) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "this connection signs in through "+def.Name+"; connect it again to renew it")
			return
		}
		var err error
		if creds, err = connectors.CheckFields(def, req.Credentials); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if account, err = s.integrations.Test(r.Context(), i.Connector, creds); err != nil {
			connectorError(w, r, err)
			return
		}
	}
	if req.Config != nil && (len(req.Config) > 16<<10 || !json.Valid(req.Config) || req.Config[0] != '{') {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "config must be a JSON object under 16 KB")
		return
	}
	if req.Config != nil {
		if msg := checkWarmupTags(req.Config); msg != "" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
			return
		}
	}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		changed := []string{}
		if req.Name != nil {
			name := cleanName(*req.Name)
			if name == "" {
				return errBadName
			}
			if _, err := tx.Exec(r.Context(), `UPDATE integrations SET name = $3, updated_at = now() WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, i.ID, name); err != nil {
				return err
			}
			changed = append(changed, "name")
		}
		if creds != nil {
			// A pasted key replaces a sign-in, if the connection had one.
			if err := s.integrations.SetCreds(r.Context(), tx, a.WorkspaceID, i.ID, connectors.AuthAPIKey, creds); err != nil {
				return err
			}
			if err := s.integrations.Checked(r.Context(), tx, a.WorkspaceID, i.ID, &account, nil); err != nil {
				return err
			}
			changed = append(changed, "credentials")
		}
		if req.Config != nil {
			if _, err := tx.Exec(r.Context(), `UPDATE integrations SET config = $3, updated_at = now() WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, i.ID, req.Config); err != nil {
				return err
			}
			changed = append(changed, "config")
		}
		data := map[string]any{"changed": changed}
		if share != nil {
			if err := setShare(r.Context(), tx, a.WorkspaceID, i.ID, *share); err != nil {
				return err
			}
			changed = append(changed, "brands")
			data = map[string]any{"changed": changed, "brand_id": share.BrandID, "brand_ids": len(share.BrandIDs)}
			if share.BrandID != nil {
				noteBrand(r.Context(), *share.BrandID)
			}
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "integration.updated", "integration", i.ID, data)
	})
	switch {
	case errors.Is(err, errBadName):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name cannot be empty")
		return
	case isUnique(err):
		writeError(w, r, http.StatusConflict, "conflict", "another connection already has that name")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	s.getIntegration(w, r)
}

var errBadName = errors.New("bad name")

func (s *Server) deleteIntegration(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	i, ok := s.loadIntegration(w, r)
	if !ok {
		return
	}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if err := s.integrations.Disconnecting(r.Context(), tx, i); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `DELETE FROM integrations WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, i.ID); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "integration.disconnected", "integration", i.ID,
			map[string]any{"connector": i.Connector, "name": i.Name})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// testIntegration signs in again with the stored credentials. It reads
// the account only; nothing changes at the provider.
func (s *Server) testIntegration(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	i, ok := s.loadIntegration(w, r)
	if !ok {
		return
	}
	o, err := s.integrations.Open(r.Context(), s.pool, a.WorkspaceID, i.ID)
	if err != nil {
		if connectors.KindOf(err) != connectors.ErrUnavailable {
			connectorError(w, r, err)
			return
		}
		s.internal(w, r, err)
		return
	}
	acct, callErr := o.Connector.Test(r.Context(), o.Client, o.Creds)
	if err := s.integrations.Checked(r.Context(), s.pool, a.WorkspaceID, i.ID, &acct, callErr); err != nil {
		s.internal(w, r, err)
		return
	}
	s.getIntegration(w, r)
}

// ---- OAuth ----

const integrationStatePurpose = "integration-oauth-state"

type integrationState struct {
	Workspace string `json:"w"`
	Actor     string `json:"a"`
	ActorType string `json:"t,omitempty"`
	// Set when the sign-in started from a connect request link.
	Request string `json:"r,omitempty"`
	// The app page to come back to: empty for Connectors, get-started or setup.
	Return    string `json:"p,omitempty"`
	Connector string `json:"c"`
	// Set when signing an existing connection in to an account through
	// the workspace's own app (connectors.AccountSignIn).
	Integration string `json:"i,omitempty"`
	Name        string `json:"n,omitempty"`
	// Set when the connection is a brand's own (?brand=).
	Brand    string `json:"b,omitempty"`
	Verifier string `json:"v"`
	Expires  int64  `json:"e"`
}

// share is who uses the connection the sign-in makes.
func (st integrationState) share() integrationShare {
	if st.Brand == "" {
		return integrationShare{}
	}
	return integrationShare{BrandID: &st.Brand}
}

func (s *Server) integrationRedirectURI() string {
	return s.publicURL + "/v1/integrations/oauth/callback"
}

// integrationAuthorize sends the browser to the connector's consent screen.
// Agents can read the Location header and hand the link to a person.
func (s *Server) integrationAuthorize(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	def, ok := s.integrations.Registry.Definition(r.PathValue("connector"))
	if !ok || !def.Supports(connectors.AuthOAuth2) || def.Status != connectors.Available || def.OAuth == nil {
		writeError(w, r, http.StatusNotFound, "not_found", "no OAuth connector by that id")
		return
	}
	app, ok := s.integrations.OAuthApps[def.ID]
	if !ok {
		msg := def.Name + " needs an OAuth app on this server: " + connectors.OAuthHint(def)
		if def.Supports(connectors.AuthAPIKey) {
			msg += ". Or connect it with an API key: POST /v1/workspaces/{ws}/integrations"
		}
		writeError(w, r, http.StatusBadRequest, "not_configured", msg)
		return
	}
	// Only pages of the app's own are allowed, so the link can't send the
	// browser anywhere else.
	back := r.URL.Query().Get("return")
	if !slices.Contains([]string{"get-started", "setup", "tracking", "providers", "audits", "domains"}, back) {
		back = ""
	}
	var brand string
	if ref := r.URL.Query().Get("brand"); ref != "" {
		id, err := s.brandIDFor(r.Context(), a.WorkspaceID, ref)
		if errors.Is(err, errNotFound) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "brand not found: brand is a brand's id or handle, for a connection only that brand uses")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		brand = id
	}
	st, _ := json.Marshal(integrationState{
		Workspace: a.WorkspaceID, Actor: a.ActorID, ActorType: a.ActorType, Connector: def.ID, Name: cleanName(r.URL.Query().Get("name")), Return: back, Brand: brand,
		Verifier: newID() + newID(), Expires: time.Now().Add(15 * time.Minute).Unix(),
	})
	// The verifier rides inside the signed state; the state is sealed so the
	// verifier is not readable in browser history.
	sealed := base64.RawURLEncoding.EncodeToString(s.box.Seal(st, []byte(integrationStatePurpose)))
	var parsed integrationState
	_ = json.Unmarshal(st, &parsed)
	link := connectors.AuthURL(def, app, s.integrationRedirectURI(), sealed, parsed.Verifier)
	if r.URL.Query().Get("format") == "json" {
		writeJSON(w, http.StatusOK, map[string]string{"url": link})
		return
	}
	http.Redirect(w, r, link, http.StatusFound)
}

// integrationClientMetadata serves the OAuth client metadata document for
// providers that read the client from a URL (PostHog), so signing in needs
// no app registered with them. The document's URL is the client id.
func (s *Server) integrationClientMetadata(w http.ResponseWriter, r *http.Request) {
	id, ok := strings.CutSuffix(r.PathValue("file"), ".json")
	var def connectors.Definition
	if ok && s.integrations != nil {
		def, ok = s.integrations.Registry.Definition(id)
	}
	if !ok || def.OAuth == nil || !def.OAuth.ClientMetadata {
		writeError(w, r, http.StatusNotFound, "not_found", "no client metadata for that connector")
		return
	}
	scopes := []string{}
	for _, sc := range def.OAuth.Scopes {
		if strings.Contains(sc, ":") {
			scopes = append(scopes, sc)
		}
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, map[string]any{
		"client_id":                  s.publicURL + connectors.MetadataClientPath(def.ID),
		"client_name":                "Outflare",
		"client_uri":                 s.publicURL,
		"redirect_uris":              []string{s.integrationRedirectURI()},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
		"com.posthog":                map[string]any{"scopes": scopes},
	})
}

func (s *Server) integrationCallback(w http.ResponseWriter, r *http.Request) {
	page := "/"
	done := func(params url.Values) {
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, s.publicURL+page+"?"+params.Encode(), http.StatusFound)
	}
	fail := func(msg string) { done(url.Values{"connect_error": {msg}}) }
	if s.integrations == nil || s.box == nil {
		fail("connectors are not configured on this server")
		return
	}
	q := r.URL.Query()
	raw, err := base64.RawURLEncoding.DecodeString(q.Get("state"))
	var st integrationState
	if err == nil {
		var plain []byte
		if plain, err = s.box.Open(raw, []byte(integrationStatePurpose)); err == nil {
			err = json.Unmarshal(plain, &st)
		}
	}
	if err != nil {
		fail("the sign-in link is not valid; start again from Connectors")
		return
	}
	page = s.appPath(r.Context(), st.Workspace, "connectors")
	if st.Return != "" {
		page = s.appPath(r.Context(), st.Workspace, st.Return)
	}
	if st.Request != "" {
		// Whoever opened a connect link may not have a login.
		page = "/connect"
	}
	if time.Now().Unix() > st.Expires {
		fail("the sign-in took too long; start again")
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
	if st.Integration != "" {
		s.finishAccountSignIn(w, r, st, done, fail)
		return
	}
	def, ok := s.integrations.Registry.Definition(st.Connector)
	app, configured := s.integrations.OAuthApps[st.Connector]
	if !ok || !configured || def.OAuth == nil {
		fail("this connector is no longer available")
		return
	}
	creds, err := connectors.Exchange(r.Context(), s.integrations.Client, def, app, q.Get("code"), s.integrationRedirectURI(), st.Verifier)
	if err != nil {
		fail(truncate(err.Error(), 300))
		return
	}
	account, err := s.integrations.Test(r.Context(), def.ID, creds)
	if err != nil {
		fail(truncate(err.Error(), 300))
		return
	}
	name := st.Name
	if name == "" {
		name = s.defaultName(r, st.Workspace, def, account)
	}
	actorType := st.ActorType
	if actorType == "" {
		actorType = "human"
	}
	out, err := s.saveIntegration(r.Context(), &principal{ActorID: st.Actor, ActorType: actorType}, st.Workspace, def, connectors.AuthOAuth2, name, creds, account, "oauth", st.Request, st.share())
	if errors.Is(err, errRequestUsed) {
		fail("this link was already used or cancelled")
		return
	}
	if isUnique(err) {
		fail("another connection is already named " + name)
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	params := url.Values{"integration": {out.ID}}
	if also := s.connectAlso(r, st, def, creds, actorType); len(also) > 0 {
		params["also"] = also
	}
	done(params)
}

// connectAlso saves the connectors a sign-in also covers (one Google
// sign-in for Analytics and Search Console), skipping any the workspace
// already has and any the person did not grant. It returns their names.
func (s *Server) connectAlso(r *http.Request, st integrationState, def connectors.Definition, creds connectors.Creds, actorType string) []string {
	var names []string
	for _, id := range def.AlsoConnects {
		other, ok := s.integrations.Registry.Definition(id)
		if _, configured := s.integrations.OAuthApps[id]; !ok || !configured || other.Status != connectors.Available {
			continue
		}
		var have bool
		if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM integrations WHERE workspace_id = $1 AND connector = $2 AND brand_id IS NOT DISTINCT FROM NULLIF($3, '')::uuid)`, st.Workspace, id, st.Brand).Scan(&have); err != nil || have {
			continue
		}
		account, err := s.integrations.Test(r.Context(), id, creds)
		if err != nil {
			// Not granted: the main connection stands on its own.
			continue
		}
		if ar, ok := s.integrations.Registry.Get(id); ok {
			// An analytics tool with no sites on this account adds nothing.
			if reader, ok := ar.(connectors.AnalyticsReader); ok {
				if sites, err := reader.Sites(r.Context(), s.integrations.Client, creds); err != nil || len(sites) == 0 {
					continue
				}
			}
		}
		name := s.defaultName(r, st.Workspace, other, account)
		if _, err := s.saveIntegration(r.Context(), &principal{ActorID: st.Actor, ActorType: actorType}, st.Workspace, other, connectors.AuthOAuth2, name, creds, account, "oauth", "", st.share()); err == nil {
			names = append(names, other.Name)
		}
	}
	return names
}

// saveIntegration stores a tested connection, runs the connect hooks and
// writes the audit event, all in one transaction. With a connect request
// id it also marks that request done, and fails with errRequestUsed if it
// was used or cancelled meanwhile.
func (s *Server) saveIntegration(ctx context.Context, p *principal, ws string, def connectors.Definition, method, name string, creds connectors.Creds, account connectors.Account, via, requestID string, share integrationShare) (connectors.Integration, error) {
	var out connectors.Integration
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if out, err = s.integrations.Insert(ctx, tx, ws, def.ID, method, name, creds, account, p.ActorID); err != nil {
			return err
		}
		if share.BrandID != nil || len(share.BrandIDs) > 0 {
			if err := setShare(ctx, tx, ws, out.ID, share); err != nil {
				return err
			}
			out.BrandID, out.BrandIDs = share.BrandID, share.BrandIDs
			if share.BrandID != nil {
				noteBrand(ctx, *share.BrandID)
			}
		}
		if err := s.integrations.Connected(ctx, tx, out); err != nil {
			return err
		}
		data := map[string]any{"connector": def.ID, "auth_method": method, "name": name, "via": via, "brand_id": share.BrandID}
		if requestID != "" {
			data["connect_request_id"] = requestID
			if err := completeConnectRequest(ctx, tx, ws, requestID, out.ID, def); err != nil {
				return err
			}
		}
		return record(ctx, tx, p, ws, "integration.connected", "integration", out.ID, data)
	})
	return out, err
}

// isUnique reports a unique-constraint violation.
func isUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// checkWarmupTags checks config.warmup_tags, the tags a sequencer puts in
// its warm-up mail, which the inbox skips. It returns what is wrong, or "".
func checkWarmupTags(config json.RawMessage) string {
	var c struct {
		Tags json.RawMessage `json:"warmup_tags"`
	}
	if json.Unmarshal(config, &c) != nil || c.Tags == nil {
		return ""
	}
	var tags []string
	if err := json.Unmarshal(c.Tags, &tags); err != nil || len(tags) > 20 {
		return "config.warmup_tags must be a list of at most 20 strings"
	}
	for _, t := range tags {
		if n := len(strings.TrimSpace(t)); n < inbox.MinWarmupTag || n > 100 {
			return fmt.Sprintf("config.warmup_tags: each tag must be %d to 100 characters, so it cannot match real mail", inbox.MinWarmupTag)
		}
	}
	return ""
}
