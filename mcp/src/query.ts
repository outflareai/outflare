// Query and search: read-only SQL over the workspace's own data, and
// full-text search over records and their history. One query answers what
// would otherwise take many list calls ("win rate by source this quarter",
// "people who visited pricing and have no deal"), computed exactly. It sees
// only what this connection may see through the API: the workspace, its
// brands and the read scopes it holds.

import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import type { MissionClient } from "@outflare/sdk";
import { z } from "zod";

type Call = (fn: () => Promise<{ data?: unknown; error?: unknown; response: Response }>) => Promise<CallToolResult>;

const read = { readOnlyHint: true, openWorldHint: false } as const;

const kinds = z.enum(["record", "message", "meeting", "deal", "email"]);

export function registerQueryTools(server: McpServer, gtm: MissionClient, workspace: () => string, call: Call): void {
  const path = () => ({ ws: workspace() });

  server.registerTool(
    "list_query_tables",
    {
      title: "List the tables run_query can read",
      description:
        "The tables run_query reads (people, companies, customer_events, deals, messages, replies, emails, visits and more), their columns, and " +
        "whether this connection holds the read scope each needs. Read it before writing SQL.",
      inputSchema: {},
      annotations: read,
    },
    () => call(() => gtm.GET("/v1/workspaces/{ws}/query/tables", { params: { path: path() } })),
  );

  server.registerTool(
    "run_query",
    {
      title: "Run a read-only SQL query",
      description:
        "One Postgres SELECT (or WITH ... SELECT) over the tables list_query_tables lists, such as " +
        "select stage, count(*), sum(amount_cents) from deals group by stage. Prefer it to paging through list tools when the answer is a " +
        "count, sum, rate or join. It never changes anything, so it needs no mission. Up to limit rows come back (default 100, at most 1000; " +
        "truncated says there were more); it stops after 15 seconds (query_timeout), so aggregate rather than list. jsonb columns such as " +
        "attributes are read with ->> (attributes->>'plan').",
      inputSchema: {
        sql: z.string().min(1).max(20000),
        limit: z.number().int().min(1).max(1000).optional(),
      },
      annotations: read,
    },
    (body) => call(() => gtm.POST("/v1/workspaces/{ws}/query", { params: { path: path() }, body })),
  );

  server.registerTool(
    "search_workspace",
    {
      title: "Search records and their history",
      description:
        "Find people, companies and other records by name, attribute text, email, phone or id, and emails sent and received, meetings, deals " +
        "and lifecycle email by their words, in one ranked list. Every word must match, each as the start of a word. Each hit says whose it " +
        "is (record_id, record_name); a reply or meeting has a snippet of the text that matched. Use run_query instead to count or sum.",
      inputSchema: {
        q: z.string().min(1).max(500),
        kinds: z.array(kinds).optional().describe("Only these kinds. Default: every kind this connection may read."),
        limit: z.number().int().min(1).max(100).optional(),
      },
      annotations: read,
    },
    ({ q, kinds, limit }) =>
      call(() =>
        gtm.GET("/v1/workspaces/{ws}/search", { params: { path: path(), query: { q, kinds: kinds?.join(","), limit } } }),
      ),
  );
}
