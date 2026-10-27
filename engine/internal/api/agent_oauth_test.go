package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/track"
)

// form posts an OAuth form request and decodes the JSON answer.
func form(t *testing.T, h http.Handler, path string, v url.Values) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func pkce(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// agentServer is the test server with a web app to serve the consent
// screen from and a stand-in for the hosted MCP server behind /mcp.
func agentServer(t *testing.T) (http.Handler, *env, chan *http.Request) {
	t.Helper()
	e := freshEnv(t)
	if _, err := e.pool.Exec(context.Background(), `TRUNCATE oauth_clients CASCADE`); err != nil {
		t.Fatal(err)
	}
	web := t.TempDir()
	os.WriteFile(filepath.Join(web, "index.html"), []byte("<!doctype html><title>Outflare</title>"), 0o644) //nolint:errcheck
	seen := make(chan *http.Request, 10)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Clone(context.Background())
		if r.Header.Get("Mcp-Session-Id") == "" && r.Method == "POST" {
			w.Header().Set("Mcp-Session-Id", "sess-1")
			w.Header().Set("X-Outflare-Agent", "claude-ai")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{}}`) //nolint:errcheck
	}))
	t.Cleanup(upstream.Close)
	u, _ := url.Parse(upstream.URL)
	srv := api.NewServer(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)), api.Options{WebDir: web, Box: e.box,
		MCP: &api.MCPUpstream{URL: u, Secret: "proxy-secret"}})
	return handler(t, srv), e, seen
}

func TestConnectAnAIAppWithOAuth(t *testing.T) {
	got := track.Capture(t)
	h, e, seen := agentServer(t)
	owner, ws := setupOwner(t, h)
	anon := &client{t: t, h: h}

	// An app finds where to sign in from /mcp's 401.
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 || !strings.Contains(rec.Header().Get("WWW-Authenticate"), `resource_metadata="http://localhost:8080/.well-known/oauth-protected-resource/mcp"`) {
		t.Fatalf("401: %d %q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
	// A session cookie is no way in: a web page must not reach the tools.
	req = httptest.NewRequest("POST", "/mcp", strings.NewReader(`{}`))
	req.AddCookie(owner.cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("cookie on /mcp: %d", rec.Code)
	}
	meta := anon.must(200, "GET", "/.well-known/oauth-protected-resource/mcp", nil)
	if meta["resource"] != "http://localhost:8080/mcp" {
		t.Fatalf("resource metadata: %v", meta)
	}
	as := anon.must(200, "GET", "/.well-known/oauth-authorization-server", nil)
	if as["registration_endpoint"] != "http://localhost:8080/oauth/register" || !slices.Contains(as["code_challenge_methods_supported"].([]any), any("S256")) {
		t.Fatalf("server metadata: %v", as)
	}

	// It registers itself. Only https, loopback or an app's own scheme.
	if code, out := anon.do("POST", "/oauth/register", map[string]any{"redirect_uris": []string{"http://evil.example/cb"}, "client_name": "Claude"}); code != 400 || out["error"] != "invalid_redirect_uri" {
		t.Fatalf("http redirect: %d %v", code, out)
	}
	if code, _ := anon.do("POST", "/oauth/register", map[string]any{"redirect_uris": []string{"javascript:alert(1)"}}); code != 400 {
		t.Fatalf("javascript redirect: %d", code)
	}
	const redirect = "https://claude.ai/api/mcp/auth_callback"
	reg := anon.must(201, "POST", "/oauth/register", map[string]any{"redirect_uris": []string{redirect}, "client_name": "Claude"})
	clientID := reg["client_id"].(string)
	if reg["token_endpoint_auth_method"] != "none" {
		t.Fatalf("registration: %v", reg)
	}

	// The person lands on the consent screen; a redirect URI the app did
	// not register gets a page, never a redirect.
	verifier := strings.Repeat("v", 50)
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect}, "state": {"st"},
		"code_challenge": {pkce(verifier)}, "code_challenge_method": {"S256"}, "resource": {"http://localhost:8080/mcp"}}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/oauth/authorize?"+q.Encode(), nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<title>Outflare") {
		t.Fatalf("authorize page: %d %s", rec.Code, rec.Body.String())
	}
	bad := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {"https://evil.example/cb"}}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/oauth/authorize?"+bad.Encode(), nil))
	if rec.Code != 400 || rec.Header().Get("Location") != "" {
		t.Fatalf("bad redirect: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	noPKCE := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect}, "state": {"st"}}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/oauth/authorize?"+noPKCE.Encode(), nil))
	if loc := rec.Header().Get("Location"); rec.Code != 302 || !strings.Contains(loc, "error=invalid_request") || !strings.Contains(loc, "state=st") {
		t.Fatalf("no PKCE: %d %q", rec.Code, loc)
	}

	info := owner.must(200, "GET", "/v1/agent-apps/authorize?"+url.Values{"client_id": {clientID}, "redirect_uri": {redirect}}.Encode(), nil)
	if info["app"] != "claude" || info["redirect_host"] != "claude.ai" {
		t.Fatalf("authorization info: %v", info)
	}
	wsInfo := info["workspaces"].([]any)[0].(map[string]any)
	full := wsInfo["full_scopes"].([]any)
	if wsInfo["id"] != ws || slices.Contains(full, any("tokens.manage")) || slices.Contains(full, any("missions.approve")) || !slices.Contains(full, any("leads.manage")) {
		t.Fatalf("full scopes: %v", full)
	}

	// Declining sends the app access_denied.
	no := owner.must(200, "POST", "/v1/agent-apps/authorize", map[string]any{"client_id": clientID, "redirect_uri": redirect, "state": "st", "approve": false})
	if !strings.Contains(no["redirect_url"].(string), "error=access_denied") {
		t.Fatalf("decline: %v", no)
	}
	if d := named(got(), "agent_app.connect_denied"); len(d) != 1 || d[0].Props["app"] != "claude" {
		t.Fatalf("connect_denied: %+v", d)
	}

	approve := func() string {
		t.Helper()
		yes := owner.must(200, "POST", "/v1/agent-apps/authorize", map[string]any{"client_id": clientID, "redirect_uri": redirect, "state": "st",
			"code_challenge": pkce(verifier), "code_challenge_method": "S256", "workspace_id": ws, "access": "full", "approve": true})
		u, _ := url.Parse(yes["redirect_url"].(string))
		if u.Host != "claude.ai" || u.Query().Get("state") != "st" || u.Query().Get("code") == "" {
			t.Fatalf("approve: %v", yes)
		}
		return u.Query().Get("code")
	}
	code := approve()
	exchange := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID}, "redirect_uri": {redirect}, "code_verifier": {strings.Repeat("w", 50)}}
	if c, out := form(t, h, "/oauth/token", exchange); c != 400 || out["error"] != "invalid_grant" {
		t.Fatalf("wrong verifier: %d %v", c, out)
	}
	exchange.Set("code_verifier", verifier)
	c, tok := form(t, h, "/oauth/token", exchange)
	if c != 200 || !strings.HasPrefix(tok["access_token"].(string), "oft_") || !strings.HasPrefix(tok["refresh_token"].(string), "ofo_") || tok["expires_in"].(float64) != 3600 {
		t.Fatalf("token: %d %v", c, tok)
	}
	if c, out := form(t, h, "/oauth/token", exchange); c != 400 || out["error"] != "invalid_grant" {
		t.Fatalf("code used twice: %d %v", c, out)
	}
	connected := named(got(), "agent_app.connected")
	if len(connected) != 1 || connected[0].WorkspaceID != ws || connected[0].UserID == "" {
		t.Fatalf("agent_app.connected: %+v", connected)
	}

	// The app is an ordinary API token, without what an app never gets.
	app := &client{t: t, h: h, bearer: tok["access_token"].(string)}
	who := app.must(200, "GET", "/v1/whoami", nil)
	if sc := who["token"].(map[string]any)["scopes"].([]any); slices.Contains(sc, any("tokens.manage")) || !slices.Contains(sc, any("sequences.manage")) {
		t.Fatalf("token scopes: %v", sc)
	}
	app.must(403, "POST", "/v1/workspaces/"+ws+"/api-tokens", map[string]any{"name": "x", "scopes": []string{"leads.read"}})

	// /mcp goes through to the MCP server with the proxy secret, never a cookie.
	req = httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	req.Header.Set("Authorization", "Bearer "+app.bearer)
	req.AddCookie(owner.cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	up := <-seen
	if rec.Code != 200 || up.Header.Get("X-Outflare-Proxy-Secret") != "proxy-secret" || up.Header.Get("Cookie") != "" || rec.Header().Get("X-Outflare-Agent") != "" {
		t.Fatalf("proxy: %d %v", rec.Code, up.Header)
	}
	if s := named(got(), "mcp.session_started"); len(s) != 1 || s[0].Props["agent"] != "claude-ai" || s[0].WorkspaceID != ws {
		t.Fatalf("mcp.session_started: %+v", s)
	}

	// Refreshing rotates both tokens; the old access token stops working.
	refresh := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok["refresh_token"].(string)}, "client_id": {clientID}}
	c, tok2 := form(t, h, "/oauth/token", refresh)
	if c != 200 || tok2["access_token"] == tok["access_token"] || tok2["refresh_token"] == tok["refresh_token"] {
		t.Fatalf("refresh: %d %v", c, tok2)
	}
	app.must(401, "GET", "/v1/whoami", nil)
	app.bearer = tok2["access_token"].(string)
	app.must(200, "GET", "/v1/whoami", nil)
	// An old refresh token used again after the grace period disconnects
	// the app: someone may have copied it.
	if _, err := e.pool.Exec(context.Background(), `UPDATE oauth_grants SET rotated_at = now() - interval '10 minutes'`); err != nil {
		t.Fatal(err)
	}
	if c, out := form(t, h, "/oauth/token", refresh); c != 400 || out["error"] != "invalid_grant" {
		t.Fatalf("reused refresh: %d %v", c, out)
	}
	app.must(401, "GET", "/v1/whoami", nil)
	if d := named(got(), "agent_app.disconnected"); len(d) != 1 {
		t.Fatalf("disconnected after reuse: %+v", d)
	}

	// Connected apps are listed and disconnect in one click.
	exchange.Set("code", approve())
	_, tok3 := form(t, h, "/oauth/token", exchange)
	list := owner.must(200, "GET", "/v1/workspaces/"+ws+"/agent-apps", nil)["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["app"] != "claude" || list[0].(map[string]any)["name"] != "Claude" {
		t.Fatalf("agent apps: %v", list)
	}
	owner.must(204, "DELETE", "/v1/workspaces/"+ws+"/agent-apps/"+list[0].(map[string]any)["id"].(string), nil)
	(&client{t: t, h: h, bearer: tok3["access_token"].(string)}).must(401, "GET", "/v1/whoami", nil)
	if c, out := form(t, h, "/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok3["refresh_token"].(string)}}); c != 400 || out["error"] != "invalid_grant" {
		t.Fatalf("refresh after disconnect: %d %v", c, out)
	}
	if d := named(got(), "agent_app.disconnected"); len(d) != 2 {
		t.Fatalf("disconnected: %+v", d)
	}
	if f := named(got(), "agent_app.refresh_failed"); len(f) < 2 || f[0].ErrorCode != "invalid_grant" {
		t.Fatalf("refresh_failed: %+v", f)
	}

	// An app can disconnect itself.
	exchange.Set("code", approve())
	_, tok4 := form(t, h, "/oauth/token", exchange)
	if c, _ := form(t, h, "/oauth/revoke", url.Values{"token": {tok4["refresh_token"].(string)}}); c != 200 {
		t.Fatalf("revoke: %d", c)
	}
	(&client{t: t, h: h, bearer: tok4["access_token"].(string)}).must(401, "GET", "/v1/whoami", nil)
}

func TestRemovedMemberAppStopsRefreshing(t *testing.T) {
	h, e, _ := agentServer(t)
	owner, ws := setupOwner(t, h)
	anon := &client{t: t, h: h}
	// Claude Code listens on a loopback port it picks each time.
	reg := anon.must(201, "POST", "/oauth/register", map[string]any{"redirect_uris": []string{"http://localhost:33418/callback"}, "client_name": "claude-code"})
	clientID := reg["client_id"].(string)
	inv := owner.must(201, "POST", "/v1/workspaces/"+ws+"/invites", map[string]string{"email": "op@example.com", "role": "operator"})
	op := &client{t: t, h: h}
	op.must(200, "POST", "/v1/invites/accept", map[string]string{"token": inv["token"].(string), "name": "Op", "password": "ops long password"})
	verifier := strings.Repeat("x", 64)
	info := op.must(200, "GET", "/v1/agent-apps/authorize?"+url.Values{"client_id": {clientID}, "redirect_uri": {"http://localhost:51234/callback"}}.Encode(), nil)
	if info["app"] != "local" {
		t.Fatalf("loopback app: %v", info)
	}
	yes := op.must(200, "POST", "/v1/agent-apps/authorize", map[string]any{"client_id": clientID, "redirect_uri": "http://localhost:51234/callback",
		"code_challenge": pkce(verifier), "code_challenge_method": "S256", "workspace_id": ws, "access": "read", "approve": true})
	u, _ := url.Parse(yes["redirect_url"].(string))
	_, tok := form(t, h, "/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {u.Query().Get("code")}, "client_id": {clientID},
		"redirect_uri": {"http://localhost:51234/callback"}, "code_verifier": {verifier}})
	if scope := tok["scope"].(string); strings.Contains(scope, ".manage") || !strings.Contains(scope, "leads.read") {
		t.Fatalf("read access: %v", tok)
	}
	if _, err := e.pool.Exec(context.Background(), `DELETE FROM memberships WHERE role = 'operator'`); err != nil {
		t.Fatal(err)
	}
	if c, out := form(t, h, "/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok["refresh_token"].(string)}}); c != 400 || out["error"] != "invalid_grant" {
		t.Fatalf("refresh after removal: %d %v", c, out)
	}
}

func TestBrandLimitedPersonConnectsAnAppForOneBrand(t *testing.T) {
	h, e, _ := agentServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws
	geo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Geonode", "handle": "geonode"})["id"].(string)
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "handle": "repocket"})
	anon := &client{t: t, h: h}
	redirect := "http://localhost:51234/callback"
	clientID := anon.must(201, "POST", "/oauth/register", map[string]any{"redirect_uris": []string{redirect}, "client_name": "claude-code"})["client_id"].(string)
	inv := owner.must(201, "POST", base+"/invites", map[string]any{"email": "op@example.com", "role": "operator", "brand_ids": []string{"geonode"}})
	op := &client{t: t, h: h}
	op.must(200, "POST", "/v1/invites/accept", map[string]string{"token": inv["token"].(string), "name": "Op", "password": "ops long password"})

	info := op.must(200, "GET", "/v1/agent-apps/authorize?"+url.Values{"client_id": {clientID}, "redirect_uri": {redirect}}.Encode(), nil)
	brands := info["workspaces"].([]any)[0].(map[string]any)["brands"].([]any)
	if len(brands) != 1 || brands[0].(map[string]any)["handle"] != "geonode" {
		t.Fatalf("offered brands: %v", brands)
	}
	verifier := strings.Repeat("y", 64)
	body := map[string]any{"client_id": clientID, "redirect_uri": redirect, "code_challenge": pkce(verifier), "code_challenge_method": "S256",
		"workspace_id": ws, "access": "full", "approve": true}
	// An app for every brand would see more than the person does.
	op.must(400, "POST", "/v1/agent-apps/authorize", body)
	body["brand"] = "repocket"
	op.must(400, "POST", "/v1/agent-apps/authorize", body)
	body["brand"] = "geonode"
	u, _ := url.Parse(op.must(200, "POST", "/v1/agent-apps/authorize", body)["redirect_url"].(string))
	_, tok := form(t, h, "/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {u.Query().Get("code")}, "client_id": {clientID},
		"redirect_uri": {redirect}, "code_verifier": {verifier}})
	agent := &client{t: t, h: h, bearer: tok["access_token"].(string)}
	if b := agent.must(200, "GET", "/v1/whoami", nil)["token"].(map[string]any)["brand"].(map[string]any); b["id"] != geo {
		t.Fatalf("app's brand: %v", b)
	}

	// Moved off the brand: the app stops refreshing.
	if _, err := e.pool.Exec(context.Background(), `UPDATE memberships SET brand_ids = '{}'::uuid[] || (SELECT id FROM brands WHERE handle = 'repocket') WHERE role = 'operator'`); err != nil {
		t.Fatal(err)
	}
	if c, out := form(t, h, "/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok["refresh_token"].(string)}}); c != 400 || out["error"] != "invalid_grant" {
		t.Fatalf("refresh after brand change: %d %v", c, out)
	}
}

func TestConsentTogglesPickTheAppsScopes(t *testing.T) {
	h, _, _ := agentServer(t)
	owner, ws := setupOwner(t, h)
	anon := &client{t: t, h: h}
	redirect := "http://localhost:51234/callback"
	clientID := anon.must(201, "POST", "/oauth/register", map[string]any{"redirect_uris": []string{redirect}, "client_name": "claude-code"})["client_id"].(string)
	verifier := strings.Repeat("y", 64)
	approve := func(scopes []string) (int, map[string]any) {
		return owner.do("POST", "/v1/agent-apps/authorize", map[string]any{"client_id": clientID, "redirect_uri": redirect,
			"code_challenge": pkce(verifier), "code_challenge_method": "S256", "workspace_id": ws, "scopes": scopes, "approve": true})
	}

	// A scope no app may have is refused, whatever the role.
	if code, out := approve([]string{"leads.manage", "members.manage"}); code != 400 {
		t.Fatalf("withheld scope: %d %v", code, out)
	}
	code, out := approve([]string{"leads.manage", "infrastructure.approve"})
	if code != 200 {
		t.Fatalf("approve: %d %v", code, out)
	}
	u, _ := url.Parse(out["redirect_url"].(string))
	_, tok := form(t, h, "/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {u.Query().Get("code")}, "client_id": {clientID},
		"redirect_uri": {redirect}, "code_verifier": {verifier}})
	got := strings.Fields(tok["scope"].(string))
	for _, want := range []string{"leads.manage", "infrastructure.approve", "leads.read", "sequences.read"} {
		if !slices.Contains(got, want) {
			t.Fatalf("missing %s: %v", want, got)
		}
	}
	for _, off := range []string{"sequences.manage", "context.manage", "members.manage"} {
		if slices.Contains(got, off) {
			t.Fatalf("toggled off %s: %v", off, got)
		}
	}
}

// connectApp runs the consent screen and the token exchange for one app,
// with the write scopes left on, and returns the app as a client.
func connectApp(t *testing.T, h http.Handler, owner *client, ws, clientID, redirect string, scopes []string) *client {
	t.Helper()
	verifier := strings.Repeat("z", 64)
	out := owner.must(200, "POST", "/v1/agent-apps/authorize", map[string]any{"client_id": clientID, "redirect_uri": redirect,
		"code_challenge": pkce(verifier), "code_challenge_method": "S256", "workspace_id": ws, "scopes": scopes, "approve": true})
	u, _ := url.Parse(out["redirect_url"].(string))
	code, tok := form(t, h, "/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {u.Query().Get("code")}, "client_id": {clientID},
		"redirect_uri": {redirect}, "code_verifier": {verifier}})
	if code != 200 {
		t.Fatalf("token: %d %v", code, tok)
	}
	return &client{t: t, h: h, bearer: tok["access_token"].(string)}
}

func TestChangeWhatAConnectedAppMayDo(t *testing.T) {
	got := track.Capture(t)
	h, _, _ := agentServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws
	anon := &client{t: t, h: h}
	const redirect = "https://chatgpt.com/connector_platform_oauth_redirect"
	clientID := anon.must(201, "POST", "/oauth/register", map[string]any{"redirect_uris": []string{redirect}, "client_name": "ChatGPT"})["client_id"].(string)
	app := connectApp(t, h, owner, ws, clientID, redirect, []string{"leads.manage"})

	// Refused, the app is told where its person turns the permission on.
	code, out := app.do("POST", base+"/seo-sites", map[string]any{"url": "https://example.com"})
	msg, _ := out["error"].(map[string]any)["message"].(string)
	if code != 403 || !strings.Contains(msg, "Connect your AI") || !strings.Contains(msg, "/w/"+ws+"/ai/") {
		t.Fatalf("refusal: %d %v", code, out)
	}
	// Something no app may do says so, rather than sending them looking.
	_, out = app.do("POST", base+"/api-tokens", map[string]any{"name": "x", "scopes": []string{"leads.read"}})
	if msg := out["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "can never") {
		t.Fatalf("withheld refusal: %v", out)
	}

	apps := owner.must(200, "GET", base+"/agent-apps", nil)["data"].([]any)
	a := apps[0].(map[string]any)
	id, tokenID := a["id"].(string), a["token_id"].(string)
	avail := a["available_scopes"].([]any)
	if !slices.Contains(avail, any("seo.manage")) || slices.Contains(avail, any("members.manage")) || slices.Contains(avail, any("leads.read")) {
		t.Fatalf("available scopes: %v", avail)
	}

	// The app's token is not an API token to edit or list.
	for _, tk := range owner.must(200, "GET", base+"/api-tokens", nil)["data"].([]any) {
		if tk.(map[string]any)["id"] == tokenID {
			t.Fatal("the app's token is listed under API tokens")
		}
	}
	owner.must(400, "PATCH", base+"/api-tokens/"+tokenID, map[string]any{"scopes": []string{"leads.read", "seo.manage"}})

	// The app can never change its own access, even to narrow it.
	app.must(403, "PATCH", base+"/agent-apps/"+id, map[string]any{"scopes": []string{"leads.manage", "seo.manage"}})
	owner.must(400, "PATCH", base+"/agent-apps/"+id, map[string]any{"scopes": []string{"members.manage"}})

	// The person turns SEO on; the app can use it on its next call.
	upd := owner.must(200, "PATCH", base+"/agent-apps/"+id, map[string]any{"scopes": []string{"leads.manage", "seo.manage"}})
	sc := upd["scopes"].([]any)
	if !slices.Contains(sc, any("seo.manage")) || !slices.Contains(sc, any("leads.read")) || slices.Contains(sc, any("sequences.manage")) {
		t.Fatalf("updated scopes: %v", sc)
	}
	// It makes changes only inside a mission until the person says otherwise,
	// and without Missions on it is told where to change that.
	code, out = app.do("POST", base+"/seo-sites", map[string]any{"url": "https://example.com"})
	if e := out["error"].(map[string]any); code != 403 || e["code"] != "mission_required" || !strings.Contains(e["message"].(string), "/w/"+ws+"/ai/") {
		t.Fatalf("outside a mission: %d %v", code, out)
	}
	if a["require_mission"] != true || a["verified"] != true || a["client_id"] != clientID {
		t.Fatalf("app view: %v", a)
	}
	if upd := owner.must(200, "PATCH", base+"/agent-apps/"+id, map[string]any{"require_mission": false}); upd["require_mission"] != false || !slices.Contains(upd["scopes"].([]any), any("seo.manage")) {
		t.Fatalf("mission off: %v", upd)
	}
	if code, out := app.do("POST", base+"/seo-sites", map[string]any{"url": "https://example.com"}); code == 403 {
		t.Fatalf("still refused: %v", out)
	}
	// Read only takes every change away and keeps the reads.
	sc = owner.must(200, "PATCH", base+"/agent-apps/"+id, map[string]any{"scopes": []string{}})["scopes"].([]any)
	if slices.ContainsFunc(sc, func(s any) bool { return !strings.HasSuffix(s.(string), ".read") }) || len(sc) == 0 {
		t.Fatalf("read only: %v", sc)
	}
	if !slices.ContainsFunc(got(), func(e track.Event) bool { return e.Name == "agent_app.updated" && e.Outcome == track.Success }) {
		t.Fatal("no agent_app.updated event")
	}

	// Connecting again goes through the approval screen once more and
	// replaces the old connection, even under a new client registration.
	again := anon.must(201, "POST", "/oauth/register", map[string]any{"redirect_uris": []string{redirect}, "client_name": "ChatGPT"})["client_id"].(string)
	connectApp(t, h, owner, ws, again, redirect, []string{"leads.manage", "seo.manage"})
	if apps := owner.must(200, "GET", base+"/agent-apps", nil)["data"].([]any); len(apps) != 1 || apps[0].(map[string]any)["id"] == id {
		t.Fatalf("after reconnecting: %v", apps)
	}
	app.must(401, "GET", base+"/stats", nil)
}

func TestConnectedAppChangesThingsOnlyInMissions(t *testing.T) {
	h, _, _ := agentServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws
	anon := &client{t: t, h: h}
	const redirect = "https://claude.ai/api/mcp/auth_callback"
	clientID := anon.must(201, "POST", "/oauth/register", map[string]any{"redirect_uris": []string{redirect}, "client_name": "Claude"})["client_id"].(string)
	app := connectApp(t, h, owner, ws, clientID, redirect, []string{"missions.run", "sequences.manage"})
	seq := map[string]any{"name": "Mission sequence", "steps": []map[string]any{{"subject": "Hi", "body": "Hello"}}}

	// Outside a mission it reads, but its changes are refused with the way in.
	app.must(200, "GET", base+"/sequences", nil)
	code, out := app.do("POST", base+"/sequences", seq)
	if e := out["error"].(map[string]any); code != 403 || e["code"] != "mission_required" || !strings.Contains(e["message"].(string), "create_mission") {
		t.Fatalf("outside a mission: %d %v", code, out)
	}
	// Starting a mission is allowed, and inside it the change follows the
	// mission's mode: a draft only simulates it.
	m := app.must(201, "POST", base+"/missions", map[string]any{"name": "Fintech CFOs", "goal": "Book 10 meetings"})
	if code, res := app.inMission(m["id"].(string), "POST", base+"/sequences", seq); code != 202 || res["dry_run"] != true {
		t.Fatalf("in a draft mission: %d %v", code, res)
	}
	if n := len(owner.must(200, "GET", base+"/sequences", nil)["data"].([]any)); n != 0 {
		t.Fatalf("created %d sequences", n)
	}
}

func TestUnverifiedAppOnlyReads(t *testing.T) {
	got := track.Capture(t)
	h, e, _ := agentServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws
	anon := &client{t: t, h: h}
	const redirect = "https://agent.example/callback"
	clientID := anon.must(201, "POST", "/oauth/register", map[string]any{"redirect_uris": []string{redirect}, "client_name": "Claude"})["client_id"].(string)
	info := owner.must(200, "GET", "/v1/agent-apps/authorize?"+url.Values{"client_id": {clientID}, "redirect_uri": {redirect}}.Encode(), nil)
	if info["app"] != "unknown" || info["verified"] != false {
		t.Fatalf("unknown app: %v", info)
	}

	// Asked for everything, it gets reading.
	verifier := strings.Repeat("v", 64)
	yes := owner.must(200, "POST", "/v1/agent-apps/authorize", map[string]any{"client_id": clientID, "redirect_uri": redirect,
		"code_challenge": pkce(verifier), "code_challenge_method": "S256", "workspace_id": ws, "access": "full", "approve": true})
	u, _ := url.Parse(yes["redirect_url"].(string))
	_, tok := form(t, h, "/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {u.Query().Get("code")}, "client_id": {clientID},
		"redirect_uri": {redirect}, "code_verifier": {verifier}})
	if scope := tok["scope"].(string); strings.Contains(scope, ".manage") || strings.Contains(scope, ".run") || !strings.Contains(scope, "leads.read") {
		t.Fatalf("unverified scopes: %v", tok)
	}
	a := owner.must(200, "GET", base+"/agent-apps", nil)["data"].([]any)[0].(map[string]any)
	id := a["id"].(string)
	if a["verified"] != false || len(a["available_scopes"].([]any)) != 0 || a["client_id"] != clientID {
		t.Fatalf("unverified app: %v", a)
	}
	owner.must(403, "PATCH", base+"/agent-apps/"+id, map[string]any{"scopes": []string{"leads.manage"}})
	owner.must(200, "PATCH", base+"/agent-apps/"+id, map[string]any{"require_mission": true})

	// The operator checks it: people can then give it more.
	acc := &api.Accounts{Pool: e.pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if ok, err := acc.VerifyAgentApp(context.Background(), clientID, true); !ok || err != nil {
		t.Fatalf("verify: %v %v", ok, err)
	}
	if ok, _ := acc.VerifyAgentApp(context.Background(), "00000000-0000-0000-0000-000000000000", true); ok {
		t.Fatal("verified an app that doesn't exist")
	}
	a = owner.must(200, "GET", base+"/agent-apps", nil)["data"].([]any)[0].(map[string]any)
	if a["verified"] != true || len(a["available_scopes"].([]any)) == 0 {
		t.Fatalf("verified app: %v", a)
	}
	if sc := owner.must(200, "PATCH", base+"/agent-apps/"+id, map[string]any{"scopes": []string{"leads.manage"}})["scopes"].([]any); !slices.Contains(sc, any("leads.manage")) {
		t.Fatalf("after verifying: %v", sc)
	}

	// Taking it back drops the app to reading on its next refresh.
	if ok, err := acc.VerifyAgentApp(context.Background(), clientID, false); !ok || err != nil {
		t.Fatalf("unverify: %v %v", ok, err)
	}
	c, out := form(t, h, "/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok["refresh_token"].(string)}})
	if scope, _ := out["scope"].(string); c != 200 || strings.Contains(scope, ".manage") || !strings.Contains(scope, "leads.read") {
		t.Fatalf("refresh after unverifying: %d %v", c, out)
	}
	if len(named(got(), "oauth_client.verified")) != 1 || len(named(got(), "oauth_client.unverified")) != 1 {
		t.Fatal("no verify events")
	}
}
