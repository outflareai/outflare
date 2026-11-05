package api_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/track"
)

// mcpProxy is the test server with /mcp pointing at addr, and an API token
// for it.
func mcpProxy(t *testing.T, addr string) (http.Handler, string) {
	t.Helper()
	e := freshEnv(t)
	srv := api.NewServer(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)), api.Options{Box: e.box,
		MCP: &api.MCPUpstream{URL: &url.URL{Scheme: "http", Host: addr}, Secret: "proxy-secret"}})
	h := handler(t, srv)
	owner, ws := setupOwner(t, h)
	out := owner.must(201, "POST", "/v1/workspaces/"+ws+"/api-tokens", map[string]any{"name": "Agent app", "scopes": []string{"workspace.read"}})
	return h, out["token"].(string)
}

func mcpPost(ctx context.Context, h http.Handler, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(ctx, "POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Right after the engine starts, or while the MCP server restarts, nothing
// listens on its port yet. A request waits for it instead of failing.
func TestMCPProxyWaitsForTheServerToListen(t *testing.T) {
	got := track.Capture(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	h, token := mcpProxy(t, addr)

	go func() {
		time.Sleep(400 * time.Millisecond)
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{}}`) //nolint:errcheck
		})}
		t.Cleanup(func() { srv.Close() })
		srv.Serve(l) //nolint:errcheck
	}()

	if rec := mcpPost(context.Background(), h, token); rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if f := named(got(), "mcp.proxy_failed"); len(f) != 0 {
		t.Fatalf("mcp.proxy_failed: %+v", f)
	}
}

// An app that hangs up before the answer is not an outage: no 503, no
// mcp.proxy_failed, and the event says the app closed it.
func TestMCPProxyClientClosedIsNotAnOutage(t *testing.T) {
	got := track.Capture(t)
	started := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body) //nolint:errcheck
		close(started)
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(upstream.Close)
	h, token := mcpProxy(t, strings.TrimPrefix(upstream.URL, "http://"))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()
	rec := mcpPost(ctx, h, token)
	if rec.Code == http.StatusServiceUnavailable {
		t.Fatalf("status 503 for a request the app closed")
	}
	events := got()
	if f := named(events, "mcp.proxy_failed"); len(f) != 0 {
		t.Fatalf("mcp.proxy_failed: %+v", f)
	}
	c := named(events, "mcp.request_cancelled")
	if len(c) != 1 || c[0].ErrorCode != "client_closed" || c[0].Outcome != track.Failure || c[0].Props["token_id"] == "" {
		t.Fatalf("mcp.request_cancelled: %+v", c)
	}
}

// When the MCP server really is down, the app gets 503 and the event says
// why, as an enum.
func TestMCPProxyServerDown(t *testing.T) {
	got := track.Capture(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, _ := w.(http.Hijacker)
		c, _, _ := hj.Hijack()
		c.Close()
	}))
	t.Cleanup(upstream.Close)
	h, token := mcpProxy(t, strings.TrimPrefix(upstream.URL, "http://"))
	if rec := mcpPost(context.Background(), h, token); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", rec.Code)
	}
	f := named(got(), "mcp.proxy_failed")
	if len(f) != 1 || f[0].ErrorCode != "unavailable" || f[0].Props["reason"] == "" || f[0].Props["reason"] == nil {
		t.Fatalf("mcp.proxy_failed: %+v", f)
	}
}
