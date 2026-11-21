// Query and search: read-only SQL over the workspace's own data, and
// search over records and their history, by words or by meaning. One query answers what
// would otherwise take many list calls ("win rate by source this quarter",
// "people who visited pricing and have no deal"), computed exactly. It sees
// only what this connection may see through the API: the workspace, its
// brands and the read scopes it holds.

import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import type { MissionClient } from "@outflare/sdk";
import { z } from "zod";
import { brandCaller, brandRef } from "./records.js";

type Call = (fn: () => Promise<{ data?: unknown; error?: unknown; response: Response }>) => Promise<CallToolResult>;

const read = { readOnlyHint: true, openWorldHint: false } as const;

const kinds = z.enum(["record", "message", "meeting", "deal", "email"]);

export function registerQueryTools(server: McpServer, gtm: MissionClient, workspace: () => string, readOnly: boolean, call: Call): void {
  const path = () => ({ ws: workspace() });
  const onBrand = brandCaller(gtm, workspace);

  server.registerTool(
    "describe_brand_data",
    {
      title: "Describe the brand's data",
      description:
        "The data catalog: what data the brand has and what it means, before you write SQL. Without table, every table run_query reads, " +
        "grouped by area (customers, revenue, ads, analytics, outreach, email, seo, coverage, social), with what a row is, where its rows come " +
        "from, whether you may read it, and, read live, how many rows it holds and the first and last date (how fresh it is), plus example " +
        "questions with SQL. With table, every column of that table explained: meaning, unit (amounts ending _minor or _cents are minor units of " +
        "the row's currency; times are UTC), what an id joins to, whether it is personal data, how often it is filled and its common values.",
      inputSchema: {
        brand: brandRef,
        table: z.string().optional().describe("One table's name, from the list without table."),
        fresh: z.boolean().optional().describe("Read the numbers again instead of the copy kept for up to five minutes."),
        numbers: z.boolean().optional().describe("false answers at once with the descriptions and no row counts, dates or values: use it when you only need what the tables and columns mean."),
      },
      annotations: read,
    },
    ({ brand, table, fresh, numbers }) =>
      table
        ? call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/data/catalog/{table}", { params: { path: { brand: b, table }, query: { fresh, numbers } } })))
        : call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/data/catalog", { params: { path: { brand: b }, query: { fresh, numbers } } }))),
  );

  server.registerTool(
    "list_query_tables",
    {
      title: "List the tables run_query can read",
      description:
        "The tables run_query reads (people, companies, customer_events, deals, messages, replies, emails, visits and more), their columns, and " +
        "whether this connection holds the read scope each needs. A query is for one brand: its customer tables hold that brand's rows only, and " +
        "each of its record types (such as deal or subscription) is a table of its own, with a column per field. describe_brand_data explains " +
        "each table and column and says how much data each holds.",
      inputSchema: { brand: brandRef },
      annotations: read,
    },
    ({ brand }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/query/tables", { params: { path: { brand: b } } }))),
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
        "attributes are read with ->> (attributes->>'plan'). It reads one brand's data: nothing in the SQL reaches another brand's rows.",
      inputSchema: {
        brand: brandRef,
        sql: z.string().min(1).max(20000),
        limit: z.number().int().min(1).max(1000).optional(),
      },
      annotations: read,
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/query", { params: { path: { brand: b } }, body }))),
  );

  server.registerTool(
    "export_query_csv",
    {
      title: "Export a query's results as CSV",
      description:
        "The same read-only SELECT as run_query, answered as CSV with a header row, for up to 10,000 rows (limit; default 10,000, at most " +
        "16 MB): for a list too long for run_query's 1,000 rows, to save as a file or hand to another tool. Aggregate with run_query instead " +
        "when you only need numbers. Needs customers.read and the read scope of each table; the download is recorded in the activity log, " +
        "and an agent kept to missions exports only inside one. CSV holds personal data when the query does: never paste it anywhere public.",
      inputSchema: {
        brand: brandRef,
        sql: z.string().min(1).max(20000),
        limit: z.number().int().min(1).max(10000).optional(),
      },
      annotations: read,
    },
    ({ brand, ...body }) =>
      call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/query/csv", { params: { path: { brand: b } }, body, parseAs: "text" }))),
  );

  server.registerTool(
    "list_saved_queries",
    {
      title: "List a brand's saved queries",
      description:
        "Queries people saved in Data → Queries for one brand: your own private ones and those shared with the brand, newest first, each with " +
        "its current SQL and chart. Look here before writing SQL for a common question; run one with run_saved_query.",
      inputSchema: {
        brand: brandRef,
        q: z.string().max(200).optional().describe("Only queries whose name or description contains this."),
        visibility: z.enum(["private", "brand"]).optional().describe("private: only yours; brand: only shared ones."),
      },
      annotations: read,
    },
    ({ brand, q, visibility }) =>
      call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/queries", { params: { path: { brand: b }, query: { q, visibility } } }))),
  );

  server.registerTool(
    "get_saved_query",
    {
      title: "Read a saved query",
      description: "One saved query with its current SQL and chart. versions: true also lists its versions, newest first.",
      inputSchema: {
        brand: brandRef,
        query_id: z.string().uuid(),
        versions: z.boolean().optional(),
      },
      annotations: read,
    },
    ({ brand, query_id, versions }) =>
      versions
        ? call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/queries/{saved_query}/versions", { params: { path: { brand: b, saved_query: query_id } } })))
        : call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/queries/{saved_query}", { params: { path: { brand: b, saved_query: query_id } } }))),
  );

  server.registerTool(
    "list_query_endpoints",
    {
      title: "List published query endpoints",
      description:
        "A brand's published query endpoints: saved queries owners and admins published for other systems to call with a brand API token, " +
        "with their slug, parameters, row cap, rate limit and calls in the last 24 hours.",
      inputSchema: { brand: brandRef },
      annotations: read,
    },
    ({ brand }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/query-endpoints", { params: { path: { brand: b } } }))),
  );

  server.registerTool(
    "describe_query_endpoint",
    {
      title: "Describe a query endpoint in plain English",
      description:
        "Writes a plain-English description of what an endpoint for a saved query returns: the table it reads, its filters, the parameters " +
        "callers send, its order and its row limit. Written by rules, not AI, and nothing is saved; publishing without a description uses it.",
      inputSchema: {
        brand: brandRef,
        saved_query_id: z.string().uuid().describe("The saved query, from list_saved_queries."),
        version: z.number().int().min(1).optional().describe("The saved query's version; its current one when left out."),
        params: z
          .array(z.object({ name: z.string(), type: z.enum(["text", "integer", "number", "boolean", "date", "timestamp", "uuid"]), required: z.boolean().optional() }))
          .max(20)
          .optional()
          .describe("The parameters in order ($1, $2, …), as the endpoint will declare them."),
        row_cap: z.number().int().min(1).max(10000).optional().describe("The most rows a call returns."),
      },
      annotations: read,
    },
    ({ brand, saved_query_id, version, params, row_cap }) =>
      call(() =>
        onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/query-endpoints/describe", { params: { path: { brand: b } }, body: { saved_query_id, version, params, row_cap } })),
      ),
  );

  server.registerTool(
    "run_query_endpoint",
    {
      title: "Call a published query endpoint",
      description:
        "Runs a published query endpoint by slug or id with its parameters and answers its rows (up to its row cap). Needs a token holding " +
        "query.endpoints made for this brand only; each call is logged and rate limited.",
      inputSchema: {
        brand: brandRef,
        endpoint: z.string().describe("The endpoint's slug or id, from list_query_endpoints."),
        params: z.record(z.string(), z.unknown()).optional().describe("The endpoint's parameters by name."),
      },
      annotations: read,
    },
    ({ brand, endpoint, params }) =>
      call(() =>
        onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/query-endpoints/{endpoint}/run", { params: { path: { brand: b, endpoint } }, body: { params } })),
      ),
  );

  server.registerTool(
    "run_saved_query",
    {
      title: "Run a saved query",
      description:
        "Runs a saved query's current version (or version) exactly as run_query runs SQL: the brand's rows only, up to limit rows (default 100, " +
        "at most 1000), 15 seconds at most. It never changes anything, so it needs no mission.",
      inputSchema: {
        brand: brandRef,
        query_id: z.string().uuid(),
        version: z.number().int().min(1).optional(),
        limit: z.number().int().min(1).max(1000).optional(),
      },
      annotations: read,
    },
    ({ brand, query_id, ...body }) =>
      call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/queries/{saved_query}/run", { params: { path: { brand: b, saved_query: query_id } }, body }))),
  );

  server.registerTool(
    "search_workspace",
    {
      title: "Search records and their history",
      description:
        "Find people, companies and other records by name, attribute text, email, phone or id, and emails sent and received, meetings, deals " +
        "and lifecycle email by their words, in one ranked list. Every word must match, each as the start of a word. Each hit says whose it " +
        "is (record_id, record_name); a reply or meeting has a snippet of the text that matched. Use run_query instead to count or sum. " +
        'mode "meaning" finds what means the same in other words ("unhappy with price" finds "too expensive for us") over records, ' +
        "replies, meetings and deals; use it for objections, intent and themes. It needs the workspace's meaning-based search " +
        "(get_semantic_search) and makes one small AI call on the workspace's own key.",
      inputSchema: {
        q: z.string().min(1).max(500),
        kinds: z.array(kinds).optional().describe("Only these kinds. Default: every kind this connection may read."),
        limit: z.number().int().min(1).max(100).optional(),
        mode: z.enum(["words", "meaning"]).optional().describe("words (default) matches the words; meaning matches what they mean."),
        brand: z.string().optional().describe("Only this brand's records: a handle or id from list_brands, none for records shared by every brand, or all."),
      },
      annotations: read,
    },
    ({ q, kinds, limit, mode, brand }) =>
      call(() =>
        gtm.GET("/v1/workspaces/{ws}/search", { params: { path: path(), query: { q, kinds: kinds?.join(","), limit, mode, brand } } }),
      ),
  );

  server.registerTool(
    "get_semantic_search",
    {
      title: "Read whether meaning-based search is on",
      description:
        "Whether meaning-based search (search_workspace with mode meaning) is on, which provider and model make its vectors, how many " +
        "items are ready and waiting, and problem when it can't run (no pgvector in the database, or no OpenAI, Gemini, Mistral or " +
        "OpenRouter connection: Claude has no embeddings).",
      inputSchema: {},
      annotations: read,
    },
    () => call(() => gtm.GET("/v1/workspaces/{ws}/semantic-search", { params: { path: path() } })),
  );

  if (readOnly) return;
  server.registerTool(
    "save_query",
    {
      title: "Save a query for a brand",
      description:
        "Saves SQL (check it with run_query first) as a query people can open in Data → Queries. Without query_id it makes a new one, private to " +
        "the person this connection belongs to unless visibility is brand (shared with everyone who can query the brand). With query_id it " +
        "changes that query: new sql or chart makes a new version, and the old one stays in its history. Needs customers.manage.",
      inputSchema: {
        brand: brandRef,
        query_id: z.string().uuid().optional(),
        name: z.string().min(1).max(200).optional().describe("Required for a new query."),
        description: z.string().max(2000).optional(),
        sql: z.string().min(1).max(20000).optional().describe("Required for a new query."),
        chart: z
          .object({ type: z.enum(["bar", "line", "area"]), x: z.string().max(63), y: z.array(z.string().max(63)).min(1).max(8) })
          .nullable()
          .optional()
          .describe("How to draw the results: one x column and up to 8 y columns. null shows a table."),
        visibility: z.enum(["private", "brand"]).optional(),
        note: z.string().max(500).optional().describe("What this version is, shown in its history."),
      },
      annotations: { readOnlyHint: false, destructiveHint: false, openWorldHint: false },
    },
    ({ brand, query_id, ...body }) => {
      if (query_id) {
        return call(() => onBrand(brand, (b) => gtm.PATCH("/v1/brands/{brand}/queries/{saved_query}", { params: { path: { brand: b, saved_query: query_id } }, body })));
      }
      const { name, sql } = body;
      if (!name || !sql) {
        return Promise.resolve({ isError: true, content: [{ type: "text" as const, text: "A new query needs name and sql (or pass query_id to change one)." }] });
      }
      return call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/queries", { params: { path: { brand: b } }, body: { ...body, name, sql } })));
    },
  );
  server.registerTool(
    "set_semantic_search",
    {
      title: "Turn meaning-based search on or off",
      description:
        "It is on unless an admin turned it off. Off stops its AI calls and deletes the workspace's vectors; word search keeps working. " +
        "Only change it when the person asks.",
      inputSchema: { enabled: z.boolean() },
      annotations: { readOnlyHint: false, destructiveHint: true, idempotentHint: true, openWorldHint: false },
    },
    (body) => call(() => gtm.PUT("/v1/workspaces/{ws}/semantic-search", { params: { path: path() }, body })),
  );
}
