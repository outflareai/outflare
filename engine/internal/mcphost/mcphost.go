// Package mcphost runs the hosted MCP server next to the engine. The MCP
// tools are written in TypeScript (mcp/), so the engine starts that server
// with Node on a loopback port and proxies /mcp to it (api.Server, after
// checking the caller's token). It restarts the server if it exits.
package mcphost

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

// Host is one MCP server process.
type Host struct {
	Node   string // the node binary
	Script string // the bundled server, mcp/dist/outflare-mcp.mjs
	// EngineURL is how the server reaches the engine's own API, such as
	// http://127.0.0.1:8080.
	EngineURL string
	Log       *slog.Logger

	// Upstream is where the server listens; Secret goes with every
	// proxied request, so only the engine can use it. Both are set by New.
	Upstream *url.URL
	Secret   string
}

// Check reports why the server can't run here: the script or node is
// missing. The engine then serves without /mcp instead of restarting a
// server that can never start.
func Check(node, script string) error {
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("mcp server script: %w", err)
	}
	if _, err := exec.LookPath(node); err != nil {
		return fmt.Errorf("node for the mcp server: %w", err)
	}
	return nil
}

// New picks a free loopback port and a secret. Run starts the process.
func New(node, script, engineURL string, log *slog.Logger) (*Host, error) {
	if _, err := os.Stat(script); err != nil {
		return nil, fmt.Errorf("OUTFLARE_MCP_SERVER: %w", err)
	}
	if _, err := exec.LookPath(node); err != nil {
		return nil, fmt.Errorf("OUTFLARE_NODE: %w", err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	addr := l.Addr().String()
	l.Close()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return &Host{Node: node, Script: script, EngineURL: engineURL, Log: log,
		Upstream: &url.URL{Scheme: "http", Host: addr}, Secret: hex.EncodeToString(b)}, nil
}

// EngineURL turns HTTP_ADDR (":8080", "0.0.0.0:8080") into a loopback URL.
func EngineURL(httpAddr string) string {
	host, port, err := net.SplitHostPort(httpAddr)
	if err != nil {
		return "http://127.0.0.1:8080"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// Run keeps the server running until ctx ends.
func (h *Host) Run(ctx context.Context) {
	wait := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := h.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		track.Fail(ctx, h.Log, track.Event{Name: "mcp.server_exited", ActorType: "system", Surface: "system",
			Props: map[string]any{"uptime_s": int(time.Since(start).Seconds())}}, "internal", fmt.Errorf("hosted MCP server exited: %w", err))
		if time.Since(start) > time.Minute {
			wait = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, 30*time.Second)
	}
}

func (h *Host) runOnce(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, h.Node, h.Script)
	cmd.Env = append(os.Environ(),
		"OUTFLARE_MCP_HTTP="+h.Upstream.Host,
		"OUTFLARE_URL="+h.EngineURL,
		"OUTFLARE_MCP_PROXY_SECRET="+h.Secret,
	)
	// The server's own settings must not leak in from the engine's
	// environment: each session's token comes from its request.
	cmd.Env = filter(cmd.Env, "OUTFLARE_TOKEN=", "OUTFLARE_WORKSPACE=", "OUTFLARE_AGENCY=", "OUTFLARE_MISSION=", "OUTFLARE_BRAND=", "OUTFLARE_SECRET_KEY", "DATABASE_URL=")
	out, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		return err
	}
	h.Log.Info("hosted MCP server started", "addr", h.Upstream.Host, "pid", cmd.Process.Pid)
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		h.Log.Info("mcp", "line", sc.Text())
	}
	return cmd.Wait()
}

func filter(env []string, drop ...string) []string {
	out := env[:0:0]
	for _, kv := range env {
		keep := true
		for _, d := range drop {
			if strings.HasPrefix(kv, d) {
				keep = false
			}
		}
		if keep {
			out = append(out, kv)
		}
	}
	return out
}
