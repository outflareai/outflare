#!/usr/bin/env node
// outflare-mcp: runs the Outflare MCP server over stdio.
//
//   OUTFLARE_URL=https://outflare.example.com OUTFLARE_TOKEN=oft_... outflare-mcp
//
// The token says which workspace (or agency) it belongs to; set
// OUTFLARE_WORKSPACE only to pick an agency client at startup.
// OUTFLARE_MISSION=<uuid> makes every call act inside that agent mission.
// OUTFLARE_BRAND=<handle> makes get_context and the list tools default to
// one brand; the agent can still ask for others.
// With an agency token the agent picks a client with use_client.
//
// OUTFLARE_MCP_HTTP=127.0.0.1:<port> serves Streamable HTTP instead, for the
// engine to proxy at /mcp (see http.ts); it then needs
// OUTFLARE_MCP_PROXY_SECRET and takes each session's token from the request.

import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { createClient } from "@outflare/sdk";
import { createServer, VERSION } from "./server.js";
import { serveHttp } from "./http.js";

// opt reads the optional setting OUTFLARE_<name>.
function opt(name: string): string | undefined {
  return process.env[`OUTFLARE_${name}`]?.trim() || undefined;
}

function env(name: string): string {
  const v = opt(name);
  if (!v) {
    // stdout belongs to the protocol, so problems go to stderr.
    console.error(`outflare-mcp: set OUTFLARE_${name}. See https://github.com/outflareai/outflare/tree/main/mcp#configure`);
    process.exit(1);
  }
  return v;
}

async function main() {
  const listen = opt("MCP_HTTP");
  if (listen) {
    serveHttp({ listen, baseUrl: env("URL"), secret: env("MCP_PROXY_SECRET") });
    return;
  }
  const client = createClient({ baseUrl: env("URL"), token: env("TOKEN"), missions: true, clientName: `mcp/${VERSION}` });
  let agency = opt("AGENCY");
  let workspace = opt("WORKSPACE");
  if (!agency && !workspace) {
    const { data, error } = await client.GET("/v1/whoami");
    if (error || !data?.token) {
      console.error("outflare-mcp: the server refused OUTFLARE_TOKEN, or it is not an API token:", error ?? "no token");
      process.exit(1);
    }
    agency = data.token.agency_id ?? undefined;
    workspace = data.token.workspace_id ?? undefined;
  }
  const server = createServer({
    client,
    ...(agency ? { agency, workspace } : { workspace: workspace ?? env("WORKSPACE") }),
    readOnly: ["1", "true", "yes"].includes((opt("MCP_READ_ONLY") ?? "").toLowerCase()),
    mission: opt("MISSION"),
    brand: opt("BRAND"),
  });
  await server.connect(new StdioServerTransport());
}

main().catch((e) => {
  console.error("outflare-mcp:", e);
  process.exit(1);
});
