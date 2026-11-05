package api

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"syscall"
	"time"

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
		Transport:     s.mcpTransport,
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
			ev := track.Event{WorkspaceID: p.TokenWorkspaceID, UserID: p.personID(), ActorID: p.ActorID, ActorType: p.ActorType, Surface: "mcp",
				RequestID: RequestID(r.Context()), Props: map[string]any{"token_id": p.TokenID, "hosted": true, "method": r.Method}}
			if r.Context().Err() != nil {
				// The app hung up first (a cancelled tool call, a closed
				// stream): nothing is wrong here and nobody reads the reply.
				ev.Name, ev.Outcome, ev.ErrorCode = "mcp.request_cancelled", track.Failure, "client_closed"
				track.Emit(r.Context(), ev)
				writeError(w, r, statusClientClosed, "client_closed", "the app closed the request before the MCP server answered")
				return
			}
			ev.Name = "mcp.proxy_failed"
			ev.Props["reason"] = mcpFailure(err)
			track.Fail(r.Context(), s.log, ev, "unavailable", err)
			writeError(w, r, http.StatusServiceUnavailable, "unavailable", "the MCP server is starting or restarting; try again in a few seconds")
		},
	}
	proxy.ServeHTTP(w, r)
}

// statusClientClosed is the status logged for a request whose client went
// away before the answer (nginx's 499): it is never actually sent.
const statusClientClosed = 499

// mcpReadyWait is how long a request waits for the MCP server to listen,
// while it starts with the engine or restarts after exiting, before the
// proxy gives up with 503.
var mcpReadyWait = 15 * time.Second

// newMCPTransport is the connection pool to the MCP server on loopback.
// Idle connections are dropped well before the server's own keep-alive
// timeout (Node's is 5s; mcp/src/http.ts raises it), so a request never
// lands on a connection the server is closing at that moment. A dial
// refused because the server isn't listening yet is retried until it is,
// for up to mcpReadyWait: nothing of the request has been sent then, so
// retrying is always safe.
func newMCPTransport() *http.Transport {
	d := &net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			deadline := time.Now().Add(mcpReadyWait)
			wait := 50 * time.Millisecond
			for {
				c, err := d.DialContext(ctx, network, addr)
				if err == nil || !errors.Is(err, syscall.ECONNREFUSED) || time.Now().Add(wait).After(deadline) {
					return c, err
				}
				select {
				case <-ctx.Done():
					return nil, err
				case <-time.After(wait):
				}
				wait = min(wait*2, 500*time.Millisecond)
			}
		},
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   100,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 0, // tool calls and streams take as long as they take
		ExpectContinueTimeout: time.Second,
	}
}

// mcpFailure names why the proxy couldn't reach the MCP server, for the
// event: an enum, never the error text.
func mcpFailure(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "refused"
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE):
		return "reset"
	case errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return "closed"
	}
	return "other"
}
