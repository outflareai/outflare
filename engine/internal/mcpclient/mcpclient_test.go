package mcpclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var pixel = []byte("\x89PNG\r\n\x1a\nnot-a-real-image")

func testServer(t *testing.T) (*httptest.Server, *string) {
	t.Helper()
	gotAuth := new(string)
	s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	s.AddTool(&mcp.Tool{Name: "screenshot", Description: "Take a screenshot",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"url": map[string]any{"type": "string"}}},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var args struct {
				URL string `json:"url"`
			}
			_ = json.Unmarshal(req.Params.Arguments, &args)
			return &mcp.CallToolResult{Content: []mcp.Content{
				&mcp.TextContent{Text: "Shot of " + args.URL},
				&mcp.ImageContent{MIMEType: "image/png", Data: pixel},
			}}, nil
		})
	s.AddTool(&mcp.Tool{Name: "fail", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "nope"}}}, nil
		})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotAuth = r.Header.Get("Authorization")
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, gotAuth
}

func TestListAndCall(t *testing.T) {
	srv, gotAuth := testServer(t)
	ctx := context.Background()
	s, err := Connect(ctx, Options{Endpoint: srv.URL, Internal: true, Headers: map[string]string{"Authorization": "Bearer oft_test_not_a_real_token"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if *gotAuth != "Bearer oft_test_not_a_real_token" {
		t.Fatalf("auth header: %q", *gotAuth)
	}
	tools, err := s.Tools(ctx)
	if err != nil || len(tools) != 2 {
		t.Fatalf("tools: %v %+v", err, tools)
	}
	var shot Tool
	for _, tl := range tools {
		if tl.Name == "screenshot" {
			shot = tl
		}
	}
	if !shot.ReadOnly || shot.Schema["type"] != "object" || shot.Description != "Take a screenshot" {
		t.Fatalf("screenshot tool: %+v", shot)
	}
	res, err := s.Call(ctx, "screenshot", json.RawMessage(`{"url":"https://example.com"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Shot of https://example.com" || len(res.Images) != 1 || string(res.Images[0].Data) != string(pixel) || res.IsError {
		t.Fatalf("result: %+v", res)
	}
	bad, err := s.Call(ctx, "fail", nil)
	if err != nil || !bad.IsError || bad.Text != "nope" {
		t.Fatalf("error result: %v %+v", err, bad)
	}
	if _, err := s.Call(ctx, "screenshot", json.RawMessage(`{not json`)); err == nil {
		t.Fatal("bad arguments accepted")
	}
}

func TestOnlyPublicServers(t *testing.T) {
	srv, _ := testServer(t)
	ctx := context.Background()
	// A workspace's server is https on the public internet.
	if _, err := Connect(ctx, Options{Endpoint: srv.URL}); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("http accepted: %v", err)
	}
	tls := httptest.NewTLSServer(http.NotFoundHandler())
	defer tls.Close()
	if _, err := Connect(ctx, Options{Endpoint: tls.URL}); err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("loopback accepted: %v", err)
	}
	if _, err := Connect(ctx, Options{Endpoint: "not a url"}); err == nil {
		t.Fatal("bad url accepted")
	}
}
