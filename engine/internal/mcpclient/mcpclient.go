// Package mcpclient lets Outflare call tools on an MCP server: one a
// workspace connected (with its own credentials), or a browser automation
// server Outflare runs. It speaks the streamable HTTP transport through
// the official Go SDK, reaches only public addresses unless the server is
// Outflare's own, and turns tools and their results into the model
// layer's types (engine/internal/llm), so an agent can offer them to a
// model and hand back what they return, images included.
//
// It never decides whether a call may run: the caller puts every call that
// changes something through the action layer first (ADR 13).
package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/outflareai/outflare/engine/internal/llm"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/netguard"
)

// Options open a session.
type Options struct {
	// Endpoint is the server's streamable HTTP URL (https, or http for a
	// server on Outflare's own network).
	Endpoint string
	// Headers go on every request, such as the workspace's own
	// Authorization for the server. Never a key Outflare holds.
	Headers map[string]string
	// Internal allows private addresses: only for a server Outflare runs
	// itself, such as its browser workers. A workspace's server is always
	// on the public internet.
	Internal bool
	// HTTP replaces the client (tests).
	HTTP *http.Client
	// Timeout bounds each call; 2 minutes when zero.
	Timeout time.Duration
}

// Session is an open connection to one MCP server.
type Session struct {
	cs      *mcp.ClientSession
	timeout time.Duration
}

// Tool is a tool the server offers.
type Tool struct {
	llm.Tool
	// ReadOnly is the server's hint that the tool changes nothing. A hint
	// only: the caller still decides what needs the action layer.
	ReadOnly bool
}

// Result is what a tool returned.
type Result struct {
	Text    string
	Images  []llm.Image
	IsError bool
}

// headerTransport adds fixed headers to every request.
type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range t.headers {
		r.Header.Set(k, v)
	}
	return t.base.RoundTrip(r)
}

// Connect opens a session and initialises it.
func Connect(ctx context.Context, o Options) (*Session, error) {
	u, err := url.Parse(o.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && o.Internal)) {
		return nil, errors.New("an MCP server's address is an https URL")
	}
	if o.Timeout <= 0 {
		o.Timeout = 2 * time.Minute
	}
	h := o.HTTP
	if h == nil {
		if o.Internal {
			h = &http.Client{Timeout: o.Timeout}
		} else {
			h = netguard.Client(o.Timeout)
		}
	}
	if len(o.Headers) > 0 {
		base := h.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		c := *h
		c.Transport = headerTransport{base: base, headers: o.Headers}
		h = &c
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "outflare", Version: "1"}, nil)
	start := time.Now()
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: o.Endpoint, HTTPClient: h, MaxRetries: 2, DisableStandaloneSSE: true}, nil)
	metrics.MCPClientCalled("connect", outcome(err), time.Since(start))
	if err != nil {
		return nil, fmt.Errorf("could not connect to the MCP server: %w", err)
	}
	return &Session{cs: cs, timeout: o.Timeout}, nil
}

// Close ends the session.
func (s *Session) Close() error { return s.cs.Close() }

// Tools lists every tool the server offers, following pages.
func (s *Session) Tools(ctx context.Context) ([]Tool, error) {
	var out []Tool
	cursor := ""
	for range 50 {
		start := time.Now()
		res, err := s.cs.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		metrics.MCPClientCalled("list_tools", outcome(err), time.Since(start))
		if err != nil {
			return nil, fmt.Errorf("could not list the MCP server's tools: %w", err)
		}
		for _, t := range res.Tools {
			schema := map[string]any{"type": "object"}
			if b, err := json.Marshal(t.InputSchema); err == nil {
				var m map[string]any
				if json.Unmarshal(b, &m) == nil && m != nil {
					schema = m
				}
			}
			out = append(out, Tool{
				Tool:     llm.Tool{Name: t.Name, Description: t.Description, Schema: schema},
				ReadOnly: t.Annotations != nil && t.Annotations.ReadOnlyHint,
			})
		}
		if res.NextCursor == "" {
			return out, nil
		}
		cursor = res.NextCursor
	}
	return out, nil
}

// Call runs one tool with the model's arguments.
func (s *Session) Call(ctx context.Context, name string, args json.RawMessage) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var a any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return Result{}, fmt.Errorf("the arguments for %s are not JSON", name)
		}
	}
	start := time.Now()
	res, err := s.cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: a})
	metrics.MCPClientCalled("call_tool", outcome(err), time.Since(start))
	if err != nil {
		return Result{}, fmt.Errorf("the MCP tool %s failed: %w", name, err)
	}
	return convert(res), nil
}

// convert turns a tool's result into text and images for the model.
func convert(res *mcp.CallToolResult) Result {
	out := Result{IsError: res.IsError}
	var text []string
	for _, c := range res.Content {
		switch v := c.(type) {
		case *mcp.TextContent:
			text = append(text, v.Text)
		case *mcp.ImageContent:
			img := llm.Image{MediaType: v.MIMEType, Data: v.Data}
			if llm.CheckImage(img) == nil {
				out.Images = append(out.Images, img)
			} else {
				text = append(text, "[an image the model can't read was left out]")
			}
		case *mcp.EmbeddedResource:
			if v.Resource != nil && v.Resource.Text != "" {
				text = append(text, v.Resource.Text)
			}
		case *mcp.ResourceLink:
			text = append(text, "Resource: "+v.URI)
		}
	}
	if len(text) == 0 && res.StructuredContent != nil {
		if b, err := json.Marshal(res.StructuredContent); err == nil {
			text = append(text, string(b))
		}
	}
	out.Text = strings.Join(text, "\n")
	return out
}

func outcome(err error) string {
	if err != nil {
		return "error"
	}
	return "success"
}
