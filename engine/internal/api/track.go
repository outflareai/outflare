package api

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"

	spec "github.com/outflareai/outflare/api"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/missions"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Every request is logged and, unless it is the web app reading a page's
// data, recorded as an api.called product event. The events its handler
// recorded (audit actions, sign-ins) are sent once it succeeds.

// Clients name themselves in this header ("web", "mcp/0.1.0",
// "sdk-ts/0.1.0", "cli/1.2.0"); the MCP server also sends the tool and the
// agent's app it runs in.
const (
	clientHeader = "Outflare-Client"
	toolHeader   = "Outflare-Tool"
	agentHeader  = "Outflare-Agent"
)

// reqInfo is filled in as a request is authenticated, for its log line and
// event.
type reqInfo struct {
	p           *principal
	workspaceID string
	injected    bool   // an approved mission action, run inside the server
	path        string // the URL path, which says how someone signed in or up
	brandID     string // the brand and project the request was about, for its log row
	projectID   string
}

type ctxReqInfoKey struct{}

func reqInfoFrom(ctx context.Context) *reqInfo {
	i, _ := ctx.Value(ctxReqInfoKey{}).(*reqInfo)
	return i
}

func notePrincipal(r *http.Request, p *principal) {
	if i := reqInfoFrom(r.Context()); i != nil && i.p == nil {
		i.p = p
	}
}

func noteWorkspace(r *http.Request, ws string) {
	if i := reqInfoFrom(r.Context()); i != nil && i.workspaceID == "" {
		i.workspaceID = ws
	}
}

// trackRecorder notes the status and keeps the start of an error body, to
// read its code whoever wrote it (writeError, or an extension's own).
type trackRecorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (t *trackRecorder) WriteHeader(code int) {
	if t.status == 0 {
		t.status = code
	}
	t.ResponseWriter.WriteHeader(code)
}

func (t *trackRecorder) Write(b []byte) (int, error) {
	if t.status == 0 {
		t.status = http.StatusOK
	}
	if t.status >= 400 && t.body.Len() < 4096 {
		t.body.Write(b[:min(len(b), 4096-t.body.Len())])
	}
	return t.ResponseWriter.Write(b)
}

func (t *trackRecorder) Flush() {
	if f, ok := t.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (t *trackRecorder) Unwrap() http.ResponseWriter { return t.ResponseWriter }

var missingScope = regexp.MustCompile(`^missing scope ([a-z_]+\.[a-z_]+)`)

func (t *trackRecorder) errorCode() (code, param string) {
	if t.status < 400 {
		return "", ""
	}
	// The API's {"error": {"code", "param"}}, or a JSON-RPC error from the
	// hosted MCP server, whose code is a number and which puts the API's
	// code and param in its data.
	var body struct {
		Error struct {
			Code    json.RawMessage `json:"code"`
			Param   string          `json:"param"`
			Message string          `json:"message"`
			Data    struct {
				Code  string `json:"code"`
				Param string `json:"param"`
			} `json:"data"`
		} `json:"error"`
	}
	if json.Unmarshal(t.body.Bytes(), &body) == nil {
		var code string
		if json.Unmarshal(body.Error.Code, &code) != nil || code == "" {
			code, body.Error.Param = body.Error.Data.Code, body.Error.Data.Param
		}
		if code == "" {
			return "http_" + strconv.Itoa(t.status), ""
		}
		// A refusal for a missing scope keeps the scope's name, so the
		// Logs page can say what to grant.
		if m := missingScope.FindStringSubmatch(body.Error.Message); body.Error.Param == "" && code == "forbidden" && m != nil {
			body.Error.Param = m[1]
		}
		return code, body.Error.Param[:min(len(body.Error.Param), 100)]
	}
	return "http_" + strconv.Itoa(t.status), ""
}

// logRequests logs every request and records it as a product event.
// pattern names the route that served it.
func (s *Server) logRequests(pattern func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		observe := metrics.RequestStarted()
		info := &reqInfo{path: r.URL.Path}
		_, info.injected = r.Context().Value(ctxInjectedKey{}).(*principal)
		ctx, release := track.Hold(context.WithValue(r.Context(), ctxReqInfoKey{}, info))
		rec := &trackRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r.WithContext(ctx))
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		code, param := rec.errorCode()
		route := pattern(r)
		observe(route, rec.status, time.Since(start))
		e := track.Event{
			Name:        "api.called",
			WorkspaceID: info.workspaceID,
			Surface:     surface(r, info),
			ErrorCode:   code,
			RequestID:   RequestID(r.Context()),
		}
		if p := info.p; p != nil {
			e.ActorID, e.ActorType, e.UserID = p.ActorID, p.ActorType, p.personID()
		}
		release(rec.status < 400, e)

		dur := time.Since(start).Milliseconds()
		attrs := []any{"method", r.Method, "path", r.URL.Path, "status", rec.status, "duration_ms", dur, "request_id", e.RequestID}
		if code != "" {
			attrs = append(attrs, "error_code", code, "route", route, "surface", e.Surface)
		}
		s.log.Info("http", attrs...)

		if info.workspaceID != "" && route != "" && logged(r, route) {
			s.logs.add(requestLog{
				WorkspaceID: info.workspaceID, RequestID: e.RequestID, Method: r.Method, Route: route,
				Operation: operationFor(r.Method, route), Path: r.URL.Path[:min(len(r.URL.Path), 500)], QueryKeys: queryKeys(r),
				Status: rec.status, DurationMS: dur, ErrorCode: code, ErrorParam: param, Surface: e.Surface,
				Client: clean(r.Header.Get(clientHeader)), MCPTool: clean(r.Header.Get(toolHeader)), Agent: clean(r.Header.Get(agentHeader)),
				UserAgent: r.UserAgent()[:min(len(r.UserAgent()), 200)], MissionID: missionIDOf(r), ActorID: e.ActorID, ActorType: e.ActorType,
				TokenID: tokenIDOf(info.p), BrandID: info.brandID, ProjectID: info.projectID, At: start,
			})
		}

		if !tracked(r, rec.status, route) {
			return
		}
		switch {
		case rec.status >= 500:
			e.Outcome = track.Error
		case rec.status >= 400:
			e.Outcome = track.Failure
		}
		e.Props = map[string]any{
			"operation":   operationFor(r.Method, route),
			"route":       route,
			"method":      r.Method,
			"status":      rec.status,
			"duration_ms": dur,
		}
		if v := clean(r.Header.Get(clientHeader)); v != "" {
			e.Props["client"] = v
		}
		if v := clean(r.Header.Get(toolHeader)); v != "" {
			e.Props["mcp_tool"] = v
		}
		if v := clean(r.Header.Get(agentHeader)); v != "" {
			e.Props["agent"] = v
		}
		if m := missionIDOf(r); m != "" {
			e.Props["mission_id"] = m
		}
		if p := info.p; p != nil && p.TokenProjectID != nil {
			e.Props["project_token"] = true
		}
		track.Emit(r.Context(), e)
	})
}

func missionIDOf(r *http.Request) string {
	if m := r.Header.Get(missions.Header); validUUID(m) {
		return m
	}
	return ""
}

func tokenIDOf(p *principal) string {
	if p == nil {
		return ""
	}
	return p.TokenID
}

// tracked is whether a request is an api.called event: everything except
// health checks, the analytics proxy itself, pages and files served outside
// /v1/ that loaded fine, and the web app's own successful reads (its
// pageviews say what people looked at).
func tracked(r *http.Request, status int, route string) bool {
	if route == "" || route == "/" || strings.HasPrefix(route, "/ingest/") {
		return false
	}
	switch route {
	case "GET /healthz", "GET /readyz", "GET /v1/meta", "GET /.well-known/outflare.json":
		return false
	}
	// Websites send a request per page view: one in a hundred that went
	// fine is enough to see the endpoint is used; every failure counts.
	if status < 400 {
		switch route {
		case "OPTIONS /v1/site/events":
			return false
		case "POST /v1/site/events":
			return rand.IntN(100) == 0
		}
	}
	if status < 400 && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		if _, path, _ := strings.Cut(route, " "); !strings.HasPrefix(path, "/v1/") {
			return false
		}
		if _, err := r.Cookie(sessionCookie); err == nil && r.Header.Get("Authorization") == "" {
			return false
		}
	}
	return true
}

// surface is where a request came from: the client's own name for itself,
// else what its credentials and user agent say.
func surface(r *http.Request, info *reqInfo) string {
	if info.injected {
		return "mission"
	}
	if c := clean(r.Header.Get(clientHeader)); c != "" {
		name, _, _ := strings.Cut(c, "/")
		switch {
		case name == "web" || name == "mcp" || name == "cli":
			return name
		case strings.HasPrefix(name, "sdk"):
			return "sdk"
		}
	}
	if strings.HasPrefix(r.UserAgent(), "outflare-cli/") {
		return "cli"
	}
	if r.Header.Get("Authorization") != "" {
		return "api"
	}
	if _, err := r.Cookie(sessionCookie); err == nil {
		return "web"
	}
	return "api"
}

var unsafeChars = regexp.MustCompile(`[^a-zA-Z0-9._/@+-]`)

// clean keeps a client-sent label short and plain.
func clean(v string) string {
	v = unsafeChars.ReplaceAllString(strings.TrimSpace(v), "")
	return v[:min(len(v), 64)]
}

// operations maps "METHOD /path" to the operationId in api/openapi.yaml.
var operations = func() map[string]string {
	var doc struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	m := map[string]string{}
	if yaml.Unmarshal(spec.OpenAPI, &doc) != nil {
		return m
	}
	for path, item := range doc.Paths {
		for method, op := range item {
			if op, ok := op.(map[string]any); ok {
				if id, ok := op["operationId"].(string); ok {
					m[strings.ToUpper(method)+" "+path] = id
				}
			}
		}
	}
	return m
}()

// operationFor names a route by its operationId, else by the route itself
// (an extension's own endpoint).
func operationFor(method, route string) string {
	if _, path, ok := strings.Cut(route, " "); ok {
		if id, ok := operations[method+" "+path]; ok {
			return id
		}
	}
	return route
}

// personID is the person behind a principal: the signed-in user, or the
// one who made the API token.
func (p *principal) personID() string {
	if p.UserID != "" {
		return p.UserID
	}
	return p.TokenOwnerID
}

// emit records an event about the request's caller.
func emit(r *http.Request, name, workspaceID string, props map[string]any) {
	e := track.Event{Name: name, WorkspaceID: workspaceID, RequestID: RequestID(r.Context()), Props: props}
	if p := principalFrom(r.Context()); p != nil {
		e.ActorID, e.ActorType, e.UserID = p.ActorID, p.ActorType, p.personID()
	} else if a := accessFrom(r.Context()); a != nil {
		e.ActorID, e.ActorType, e.UserID = a.ActorID, a.ActorType, a.personID()
	}
	track.Emit(r.Context(), e)
}

// accountVia is the flow that signed someone in or made their account,
// from the request's path.
func accountVia(ctx context.Context) string {
	i := reqInfoFrom(ctx)
	if i == nil {
		return "other"
	}
	switch i.path {
	case "/v1/setup":
		return "setup"
	case "/v1/auth/login":
		return "password"
	case "/v1/auth/google/callback":
		return "google"
	case "/v1/auth/passkey":
		return "passkey"
	case "/v1/auth/password-reset/confirm":
		return "password_reset"
	case "/v1/invites/accept":
		return "invite"
	case "/v1/auth/verify-email":
		return "email_link"
	}
	if strings.Contains(i.path, "signup") {
		return "signup"
	}
	return "other"
}

// emitUser records an account event (signed up, signed in, verified).
func emitUser(ctx context.Context, name, userID string, props map[string]any) {
	track.Emit(ctx, track.Event{Name: name, UserID: userID, ActorType: "human", RequestID: RequestID(ctx), Props: props})
}
