package api

import (
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/outflareai/outflare/engine/internal/track"
)

// The hosted MCP server lives at /mcp: apps connect with this one URL and
// sign in with OAuth (agent_oauth.go), or send an API token. The engine
// checks the token, then proxies to the MCP server it runs on a loopback
// port (package mcphost). Session cookies are never accepted here, so a web
// page cannot ride a signed-in person's session into the tools.

// MCPUpstream is where the hosted MCP server listens, and the secret that
// proves a request came through the engine.
type MCPUpstream struct {
	URL    *url.URL
	Secret string
}

func (s *Server) routesMCP(mux *http.ServeMux) {
	for _, m := range []string{"GET", "POST", "DELETE", "OPTIONS"} {
		mux.HandleFunc(m+" /mcp", s.mcpEndpoint)
	}
}

func (s *Server) mcpEndpoint(w http.ResponseWriter, r *http.Request) {
	allowAnyOrigin(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if s.mcp == nil {
		writeError(w, r, http.StatusNotFound, "not_configured", "this server does not host the MCP server; run it on your computer instead: https://outflare.ai/docs/agents/mcp/")
		return
	}
	r.Header.Del("Cookie")
	sentToken := r.Header.Get("Authorization") != ""
	p, err := s.authenticate(r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if p == nil || p.TokenID == "" {
		// This header is how an app finds out where to sign in.
		challenge := `Bearer resource_metadata="` + s.publicURL + `/.well-known/oauth-protected-resource/mcp"`
		if sentToken {
			challenge += `, error="invalid_token", error_description="The access token is not valid or has expired"`
		}
		w.Header().Set("WWW-Authenticate", challenge)
		writeError(w, r, http.StatusUnauthorized, "unauthenticated", "connect with OAuth, or send an API token as Authorization: Bearer oft_...")
		return
	}
	notePrincipal(r, p)
	if p.TokenWorkspaceID != "" {
		noteWorkspace(r, p.TokenWorkspaceID)
	}
	if !s.rateLimit.allow(w, r, p.TokenID) {
		return
	}
	opening := r.Method == http.MethodPost && r.Header.Get("Mcp-Session-Id") == ""
	closing := r.Method == http.MethodDelete
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(s.mcp.URL)
			pr.Out.URL.Path = "/mcp"
			pr.Out.Header.Set("X-Outflare-Proxy-Secret", s.mcp.Secret)
		},
		FlushInterval: -1, // stream server-sent events as they come
		ModifyResponse: func(res *http.Response) error {
			agent := res.Header.Get("X-Outflare-Agent")
			res.Header.Del("X-Outflare-Agent")
			ev := track.Event{WorkspaceID: p.TokenWorkspaceID, UserID: p.personID(), ActorID: p.ActorID, ActorType: p.ActorType, Surface: "mcp",
				RequestID: RequestID(r.Context()), Props: map[string]any{"token_id": p.TokenID, "hosted": true}}
			switch {
			case opening && res.StatusCode < 300 && res.Header.Get("Mcp-Session-Id") != "":
				ev.Name = "mcp.session_started"
				if agent != "" {
					ev.Props["agent"] = agent
				}
				track.Emit(r.Context(), ev)
			case closing && res.StatusCode < 300:
				ev.Name = "mcp.session_ended"
				track.Emit(r.Context(), ev)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			track.Fail(r.Context(), s.log, track.Event{Name: "mcp.proxy_failed", WorkspaceID: p.TokenWorkspaceID, Surface: "mcp", RequestID: RequestID(r.Context())}, "unavailable", err)
			writeError(w, r, http.StatusServiceUnavailable, "unavailable", "the MCP server is starting or restarting; try again in a few seconds")
		},
	}
	proxy.ServeHTTP(w, r)
}
