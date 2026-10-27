package api_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/adsync"
	"github.com/outflareai/outflare/engine/internal/analyticsync"
	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/classify"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/catalog"
	"github.com/outflareai/outflare/engine/internal/connectors/verifiers"
	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/db"
	"github.com/outflareai/outflare/engine/internal/domainconnect"
	"github.com/outflareai/outflare/engine/internal/imports"
	"github.com/outflareai/outflare/engine/internal/inbox"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/mailbox"
	"github.com/outflareai/outflare/engine/internal/oauth"
	"github.com/outflareai/outflare/engine/internal/platforms"
	"github.com/outflareai/outflare/engine/internal/research"
	"github.com/outflareai/outflare/engine/internal/secrets"
	"github.com/outflareai/outflare/engine/internal/sending"
	"github.com/outflareai/outflare/engine/internal/seointel"
	"github.com/outflareai/outflare/engine/internal/seosync"
	"github.com/outflareai/outflare/engine/internal/verify"
)

// freshServer returns a handler on an empty database.
func freshServer(t *testing.T) http.Handler {
	t.Helper()
	return freshEnv(t).h
}

// env is a server on an empty database, with the pieces tests reach into.
type env struct {
	h     http.Handler
	srv   *api.Server
	crawl *crawl.Service
	pool  *pgxpool.Pool
	mail  *mailbox.Service
	dns   *fakeDNS
	send  *sending.Engine
	sync  *inbox.Syncer
	oauth *oauth.Tokens
	// Connectors call fake services; zerobounce answers from fakeZeroBounce.
	integrations *connectors.Store
	verify       *verify.Service
	box          *secrets.Box
	imports      *imports.Service
	platforms    *platforms.Service
	outbox       *outbox // account emails the server sent
	ads          *adsync.Syncer
}

func freshEnv(t *testing.T) *env {
	t.Helper()
	return freshEnvWith(t, nil)
}

// freshEnvWith is freshEnv with changes to the server's options.
func freshEnvWith(t *testing.T, change func(*api.Options)) *env {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE workspaces, actors, users, sessions, memberships, invites, api_tokens, audit_events, jobs, auth_attempts, crawl_optouts CASCADE`); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.NewBox(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dns := &fakeDNS{mx: map[string][]*net.MX{}, txt: map[string][]string{}, host: map[string][]string{}}
	tokens := &oauth.Tokens{Pool: pool, Box: box}
	mail := &mailbox.Service{Pool: pool, Box: box, Dialer: &mailbox.Dialer{Timeout: 5 * time.Second}, Resolver: dns, Tokens: tokens, Log: log}
	send := &sending.Engine{Pool: pool, Mail: mail, Box: box, PublicURL: "http://localhost:8080", Log: log}
	syncer := &inbox.Syncer{Pool: pool, Mail: mail, Log: log}
	zb := httptest.NewServer(fakeZeroBounce{})
	t.Cleanup(zb.Close)
	sl := httptest.NewServer(fakeSmartlead{})
	t.Cleanup(sl.Close)
	eb := httptest.NewServer(fakeEmailBison{})
	t.Cleanup(eb.Close)
	cl := httptest.NewServer(fakeClaude{})
	t.Cleanup(cl.Close)
	pl := httptest.NewServer(fakePlausible{})
	t.Cleanup(pl.Close)
	dfs := httptest.NewServer(fakeDataForSEO{})
	t.Cleanup(dfs.Close)
	gn := httptest.NewServer(fakeGeonode{})
	t.Cleanup(gn.Close)
	ma := httptest.NewServer(fakeMetaAds{})
	t.Cleanup(ma.Close)
	ga := httptest.NewServer(fakeGoogleAds{})
	t.Cleanup(ga.Close)
	integrations := &connectors.Store{Pool: pool, Box: box, Registry: catalog.New(),
		Client: &connectors.Client{BaseURLs: map[string]string{"zerobounce": zb.URL, "smartlead": sl.URL, "emailbison": eb.URL, "claude": cl.URL, "plausible": pl.URL, "dataforseo": dfs.URL, "geonode": gn.URL, "meta_ads": ma.URL, "google_ads": ga.URL}}}
	send.Platforms = integrations
	importer := &imports.Service{Pool: pool, Store: integrations, Log: log}
	verifier := &verify.Service{Pool: pool, Store: integrations, Builtin: verifiers.Builtin{Resolver: dns}, Log: log}
	plat := &platforms.Service{Pool: pool, Store: integrations, Log: log}
	plat.Register(jobs.New(pool, log))
	ob := &outbox{}
	async := &analyticsync.Syncer{Pool: pool, Store: integrations, Log: log}
	seo := &seosync.Syncer{Pool: pool, Store: integrations, Log: log}
	ads := &adsync.Syncer{Pool: pool, Store: integrations, Log: log}
	// Crawls reach httptest sites on 127.0.0.1 without pausing between requests.
	crawler := &crawl.Service{Pool: pool, Log: log, Resolver: dns, AllowPrivate: true, StepFor: 5 * time.Second,
		Wait: func(context.Context, time.Duration) error { return nil }, Fallback: &crawl.ConnectorFallback{Store: integrations, Pool: pool}, AfterAudit: seointel.AfterAudit}
	opts := api.Options{Mailboxes: mail, Sending: send, Inbox: syncer, OAuth: tokens,
		Box: box, Integrations: integrations, Verify: verifier, Classifier: &classify.Classifier{Resolver: dns}, Research: &research.Service{Pool: pool, Log: log}, Imports: importer, Platforms: plat, CNAME: dns, Mailer: ob, AnalyticsSync: async, SEOSync: seo, AdSync: ads, Crawl: crawler,
		DomainConnect: &domainconnect.Client{HTTP: http.DefaultClient, Resolver: dns}}
	if change != nil {
		change(&opts)
	}
	srv := api.NewServer(pool, log, opts)
	h := handler(t, srv)
	return &env{h: h, srv: srv, crawl: crawler, ads: ads, pool: pool, mail: mail, dns: dns, send: send, sync: syncer, oauth: tokens, integrations: integrations, verify: verifier, imports: importer, platforms: plat, box: box, outbox: ob}
}

// handler serves srv for one test. When the test ends it writes the
// request logs still queued, as the engine does when it shuts down, so a
// write due a second later doesn't fail on a closed pool and record its
// failure in the next test's events.
func handler(t *testing.T, srv *api.Server) http.Handler {
	t.Cleanup(func() { srv.FlushLogs(context.Background()) }) //nolint:errcheck
	return srv.Handler()
}

// raw sends a non-JSON body.
func (c *client) raw(method, path, contentType, body string) (int, map[string]any) {
	c.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// client sends requests as one caller: a session cookie or a bearer token.
type client struct {
	t      *testing.T
	h      http.Handler
	cookie *http.Cookie
	bearer string
	header map[string]string // sent with every request, such as Outflare-Client
}

func (c *client) do(method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	for k, v := range c.header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "outflare_session" {
			if ck.MaxAge < 0 {
				c.cookie = nil
			} else {
				c.cookie = ck
			}
		}
	}
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (c *client) must(want int, method, path string, body any) map[string]any {
	c.t.Helper()
	code, out := c.do(method, path, body)
	if code != want {
		c.t.Fatalf("%s %s: status %d, want %d: %v", method, path, code, want, out)
	}
	return out
}

func setupOwner(t *testing.T, h http.Handler) (*client, string) {
	t.Helper()
	owner := &client{t: t, h: h}
	out := owner.must(201, "POST", "/v1/setup", map[string]string{
		"email": "jack@example.com", "name": "Jack", "password": "long enough password", "workspace_name": "Acme Outbound",
	})
	return owner, out["workspace_id"].(string)
}

func TestSetupOnlyOnce(t *testing.T) {
	h := freshServer(t)
	anon := &client{t: t, h: h}
	if out := anon.must(200, "GET", "/v1/setup", nil); out["needed"] != true {
		t.Fatal("setup should be needed on an empty instance")
	}
	owner, ws := setupOwner(t, h)
	me := owner.must(200, "GET", "/v1/me", nil)
	wss := me["workspaces"].([]any)
	if len(wss) != 1 || wss[0].(map[string]any)["id"] != ws || wss[0].(map[string]any)["role"] != "owner" {
		t.Fatalf("me: %v", me)
	}
	if slug := wss[0].(map[string]any)["slug"]; slug != "acme-outbound" {
		t.Fatalf("slug %v", slug)
	}
	anon.must(409, "POST", "/v1/setup", map[string]string{
		"email": "evil@example.com", "name": "E", "password": "long enough password", "workspace_name": "X",
	})
}

func TestLoginLogoutAndRateLimit(t *testing.T) {
	h := freshServer(t)
	setupOwner(t, h)
	c := &client{t: t, h: h}
	c.must(401, "GET", "/v1/me", nil)
	c.must(401, "POST", "/v1/auth/login", map[string]string{"email": "jack@example.com", "password": "wrong password!!"})
	c.must(200, "POST", "/v1/auth/login", map[string]string{"email": "JACK@example.com", "password": "long enough password"})
	c.must(200, "GET", "/v1/me", nil)
	old := c.cookie
	c.must(204, "POST", "/v1/auth/logout", nil)
	c.cookie = old // a logged-out session must not work even if replayed
	c.must(401, "GET", "/v1/me", nil)

	for i := 0; i < 10; i++ {
		c.do("POST", "/v1/auth/login", map[string]string{"email": "jack@example.com", "password": "nope nope nope"})
	}
	c.must(429, "POST", "/v1/auth/login", map[string]string{"email": "jack@example.com", "password": "long enough password"})
}

func TestAPITokenScopesAndRevocation(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws

	owner.must(400, "POST", base+"/api-tokens", map[string]any{"name": "x", "scopes": []string{"bogus.scope"}})
	out := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Read-only bot", "scopes": []string{"workspace.read", "members.read"}})
	secret := out["token"].(string)
	if !strings.HasPrefix(secret, "oft_") {
		t.Fatalf("token %q", secret)
	}
	tokenID := out["api_token"].(map[string]any)["id"].(string)

	bot := &client{t: t, h: h, bearer: secret}
	bot.must(200, "GET", base, nil)
	bot.must(200, "GET", base+"/members", nil)
	bot.must(403, "GET", base+"/audit-events", nil) // scope not granted
	bot.must(403, "GET", "/v1/me", nil)             // tokens are not users
	bot.must(403, "POST", base+"/api-tokens", map[string]any{"name": "y", "scopes": []string{"workspace.read"}})

	list := owner.must(200, "GET", base+"/api-tokens", nil)["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["last_used_at"] == nil {
		t.Fatalf("token list: %v", list)
	}
	if _, leaked := list[0].(map[string]any)["token"]; leaked {
		t.Fatal("token secret returned in list")
	}

	owner.must(204, "DELETE", base+"/api-tokens/"+tokenID, nil)
	bot.must(401, "GET", base, nil)
}

func TestWorkspaceIsolation(t *testing.T) {
	h := freshServer(t)
	owner, ws1 := setupOwner(t, h)
	ws2 := owner.must(201, "POST", "/v1/workspaces", map[string]string{"name": "Second"})["id"].(string)

	out := owner.must(201, "POST", "/v1/workspaces/"+ws1+"/api-tokens", map[string]any{"name": "ws1 bot", "scopes": []string{"workspace.read"}})
	bot := &client{t: t, h: h, bearer: out["token"].(string)}
	bot.must(200, "GET", "/v1/workspaces/"+ws1, nil)
	bot.must(404, "GET", "/v1/workspaces/"+ws2, nil)

	// A stranger with their own account sees nothing of ws1.
	inv := owner.must(201, "POST", "/v1/workspaces/"+ws2+"/invites", map[string]string{"email": "sam@example.com", "role": "admin"})
	sam := &client{t: t, h: h}
	sam.must(200, "POST", "/v1/invites/accept", map[string]string{"token": inv["token"].(string), "name": "Sam", "password": "sams long password"})
	sam.must(404, "GET", "/v1/workspaces/"+ws1, nil)
	sam.must(404, "GET", "/v1/workspaces/"+ws1+"/members", nil)
	sam.must(200, "GET", "/v1/workspaces/"+ws2+"/members", nil)
}

func TestInvitesRolesAndLastOwner(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws

	owner.must(400, "POST", base+"/invites", map[string]string{"email": "a@example.com", "role": "chief"})
	inv := owner.must(201, "POST", base+"/invites", map[string]string{"email": "viv@example.com", "role": "viewer"})
	if !strings.Contains(inv["invite_url"].(string), "/invite#ofi_") {
		t.Fatalf("invite url %v", inv["invite_url"])
	}
	viewer := &client{t: t, h: h}
	viewer.must(400, "POST", "/v1/invites/accept", map[string]string{"token": inv["token"].(string), "name": "Viv", "password": "short"})
	out := viewer.must(200, "POST", "/v1/invites/accept", map[string]string{"token": inv["token"].(string), "name": "Viv", "password": "viv long password"})
	vivID := out["user_id"].(string)
	(&client{t: t, h: h}).must(404, "POST", "/v1/invites/accept", map[string]string{"token": inv["token"].(string), "name": "X", "password": "another long one"})

	// Viewers can read but not manage.
	viewer.must(200, "GET", base+"/members", nil)
	viewer.must(403, "POST", base+"/invites", map[string]string{"email": "b@example.com", "role": "viewer"})
	viewer.must(403, "GET", base+"/api-tokens", nil)
	viewer.must(403, "GET", base+"/audit-events", nil)

	// Promote to admin; an admin cannot touch the owner.
	owner.must(200, "PATCH", base+"/members/"+vivID, map[string]string{"role": "admin"})
	me := owner.must(200, "GET", "/v1/me", nil)
	jackID := me["user"].(map[string]any)["id"].(string)
	viewer.must(403, "PATCH", base+"/members/"+jackID, map[string]string{"role": "viewer"})
	viewer.must(403, "PATCH", base+"/members/"+vivID, map[string]string{"role": "owner"})

	// The only owner cannot leave or be demoted.
	owner.must(409, "PATCH", base+"/members/"+jackID, map[string]string{"role": "admin"})
	owner.must(409, "DELETE", base+"/members/"+jackID, nil)

	// With a second owner, the first can step down.
	owner.must(200, "PATCH", base+"/members/"+vivID, map[string]string{"role": "owner"})
	owner.must(200, "PATCH", base+"/members/"+jackID, map[string]string{"role": "admin"})

	// Every change is in the audit log, newest first.
	events := viewer.must(200, "GET", base+"/audit-events", nil)["data"].([]any)
	var actions []string
	for _, e := range events {
		actions = append(actions, e.(map[string]any)["action"].(string))
	}
	want := []string{"member.role_changed", "member.role_changed", "member.role_changed", "invite.accepted", "invite.created", "workspace.created"}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Fatalf("audit actions %v", actions)
	}
	if name := events[len(events)-1].(map[string]any)["actor_name"]; name != "Jack" {
		t.Fatalf("actor_name %v", name)
	}
}

func TestExistingUserAcceptsInviteWithPassword(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	first := owner.must(201, "POST", "/v1/workspaces/"+ws+"/invites", map[string]string{"email": "sam@example.com", "role": "viewer"})
	(&client{t: t, h: h}).must(200, "POST", "/v1/invites/accept", map[string]string{"token": first["token"].(string), "name": "Sam", "password": "long enough password"})
	ws2 := owner.must(201, "POST", "/v1/workspaces", map[string]string{"name": "Other"})["id"].(string)
	inv := owner.must(201, "POST", "/v1/workspaces/"+ws2+"/invites", map[string]string{"email": "sam@example.com", "role": "admin"})
	c := &client{t: t, h: h}
	c.must(401, "POST", "/v1/invites/accept", map[string]string{"token": inv["token"].(string), "password": "wrong password!!"})
	c.must(200, "POST", "/v1/invites/accept", map[string]string{"token": inv["token"].(string), "password": "long enough password"})
}

func TestCrossSiteAndContentTypeRefused(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)

	req := httptest.NewRequest("POST", "/v1/workspaces/"+ws+"/invites", strings.NewReader(`{"email":"x@example.com","role":"viewer"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.AddCookie(owner.cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site status %d", rec.Code)
	}

	// A sibling subdomain riding the cookie is refused too.
	req = httptest.NewRequest("POST", "/v1/workspaces/"+ws+"/invites", strings.NewReader(`{"email":"x@example.com","role":"viewer"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	req.AddCookie(owner.cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("same-site status %d", rec.Code)
	}

	req = httptest.NewRequest("POST", "/v1/workspaces/"+ws+"/invites", strings.NewReader(`email=x`))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(owner.cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("form post status %d", rec.Code)
	}
}

func TestChangeATokensNameAndPermissions(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	out := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "n8n", "scopes": []string{"leads.read"}})
	id := out["api_token"].(map[string]any)["id"].(string)
	bot := &client{t: t, h: e.h, bearer: out["token"].(string)}

	_, refused := bot.do("POST", base+"/lead-lists", map[string]any{"name": "Q3"})
	if msg := refused["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "/w/"+ws+"/tokens/"+id) {
		t.Fatalf("refusal names no fix: %v", refused)
	}
	upd := owner.must(200, "PATCH", base+"/api-tokens/"+id, map[string]any{"name": "n8n sync", "scopes": []string{"leads.read", "leads.manage"}})
	if upd["name"] != "n8n sync" || len(upd["scopes"].([]any)) != 2 {
		t.Fatalf("updated: %v", upd)
	}
	if code, out := bot.do("POST", base+"/lead-lists", map[string]any{"name": "Q3"}); code == 403 {
		t.Fatalf("still refused after adding leads.manage: %v", out)
	}
	owner.must(400, "PATCH", base+"/api-tokens/"+id, map[string]any{"scopes": []string{}})
	owner.must(400, "PATCH", base+"/api-tokens/"+id, map[string]any{"name": "  "})

	// A token that manages tokens can't hand another more than it has,
	// or change itself, but can take a scope away.
	mgrOut := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "mgr", "scopes": []string{"tokens.manage", "leads.read"}})
	mgr := &client{t: t, h: e.h, bearer: mgrOut["token"].(string)}
	mgr.must(403, "PATCH", base+"/api-tokens/"+id, map[string]any{"scopes": []string{"leads.read", "leads.manage", "sequences.manage"}})
	mgr.must(200, "PATCH", base+"/api-tokens/"+id, map[string]any{"scopes": []string{"leads.read", "leads.manage"}})
	mgr.must(200, "PATCH", base+"/api-tokens/"+id, map[string]any{"scopes": []string{"leads.read"}})
	mgr.must(403, "PATCH", base+"/api-tokens/"+mgrOut["api_token"].(map[string]any)["id"].(string), map[string]any{"name": "me"})

	audit := owner.must(200, "GET", base+"/audit-events", nil)["data"].([]any)
	if !slices.ContainsFunc(audit, func(a any) bool { return a.(map[string]any)["action"] == "token.updated" }) {
		t.Fatal("no token.updated audit event")
	}
}

// A token never holds more than its maker: removing them stops it, and a
// lower role takes away what the role lacks.
func TestMemberTokensFollowTheirMaker(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws

	join := func(email, role string) (*client, string) {
		inv := owner.must(201, "POST", base+"/invites", map[string]string{"email": email, "role": role})
		c := &client{t: t, h: h}
		out := c.must(200, "POST", "/v1/invites/accept", map[string]string{"token": inv["token"].(string), "name": "Member", "password": "a long enough password"})
		return c, out["user_id"].(string)
	}
	token := func(c *client, scopes ...string) *client {
		out := c.must(201, "POST", base+"/api-tokens", map[string]any{"name": "script", "scopes": scopes})
		return &client{t: t, h: h, bearer: out["token"].(string)}
	}

	// Removed: the admin's token stops working.
	admin, adminID := join("ada@example.com", "admin")
	adminBot := token(admin, "workspace.read", "members.manage", "tokens.manage")
	adminBot.must(200, "GET", base, nil)
	owner.must(204, "DELETE", base+"/members/"+adminID, nil)
	adminBot.must(401, "GET", base, nil)

	// Demoted to viewer: the token keeps only what a viewer may do.
	admin2, admin2ID := join("bo@example.com", "admin")
	bot := token(admin2, "workspace.read", "members.read", "members.manage", "tokens.manage")
	owner.must(200, "PATCH", base+"/members/"+admin2ID, map[string]string{"role": "viewer"})
	bot.must(200, "GET", base+"/members", nil)
	bot.must(403, "GET", base+"/api-tokens", nil)
	var scopes []any
	for _, tk := range owner.must(200, "GET", base+"/api-tokens", nil)["data"].([]any) {
		scopes = tk.(map[string]any)["scopes"].([]any)
	}
	if len(scopes) != 2 {
		t.Fatalf("scopes after demotion: %v", scopes)
	}

	// A role with none of the token's scopes left revokes it.
	admin3, admin3ID := join("cy@example.com", "admin")
	tokensOnly := token(admin3, "tokens.manage")
	owner.must(200, "PATCH", base+"/members/"+admin3ID, map[string]string{"role": "operator"})
	tokensOnly.must(401, "GET", base+"/api-tokens", nil)
}

func TestInviteIsEmailed(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	before := e.outbox.count()

	inv := owner.must(201, "POST", base+"/invites", map[string]any{"email": "ana@acme.example", "role": "operator", "brand_ids": []string{}})
	if inv["emailed"] != true {
		t.Fatalf("emailed: %v", inv["emailed"])
	}
	// It sends in the background.
	for i := 0; e.outbox.count() == before && i < 200; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if e.outbox.count() != before+1 {
		t.Fatalf("sent %d emails", e.outbox.count()-before)
	}
	e.outbox.mu.Lock()
	m := e.outbox.sent[len(e.outbox.sent)-1]
	e.outbox.mu.Unlock()
	if m.To != "ana@acme.example" || !strings.Contains(m.Text, inv["invite_url"].(string)) || !strings.HasPrefix(m.Subject, "Join ") {
		t.Fatalf("invite email: to %q subject %q text %q", m.To, m.Subject, m.Text)
	}

	// A second invite replaces the first: only the newest link works.
	again := owner.must(201, "POST", base+"/invites", map[string]any{"email": "Ana@acme.example", "role": "viewer"})
	if list := owner.must(200, "GET", base+"/invites", nil)["data"].([]any); len(list) != 1 {
		t.Fatalf("pending invites: %v", list)
	}
	anon := &client{t: t, h: e.h}
	anon.must(404, "POST", "/v1/invites/accept", map[string]string{"token": inv["token"].(string), "name": "Ana", "password": "ana long password"})
	anon.must(200, "POST", "/v1/invites/accept", map[string]string{"token": again["token"].(string), "name": "Ana", "password": "ana long password"})

	// Someone already in the workspace can't be invited again.
	owner.must(409, "POST", base+"/invites", map[string]any{"email": "ana@acme.example", "role": "viewer"})
	owner.must(409, "POST", base+"/invites", map[string]any{"email": "jack@example.com", "role": "admin"})
}
