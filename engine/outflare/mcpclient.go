package outflare

import (
	"context"

	"github.com/outflareai/outflare/engine/internal/mcpclient"
)

// Calling tools on MCP servers: one a workspace connected with its own
// credentials, or a server Outflare runs. Every call that changes
// something goes through the action layer first (ADR 13).
type (
	MCPOptions = mcpclient.Options
	MCPSession = mcpclient.Session
	MCPTool    = mcpclient.Tool
	MCPResult  = mcpclient.Result
)

// ConnectMCP opens a session with an MCP server over streamable HTTP.
// Only public https addresses, unless o.Internal names a server Outflare
// runs itself.
func ConnectMCP(ctx context.Context, o MCPOptions) (*MCPSession, error) {
	return mcpclient.Connect(ctx, o)
}
