// AI Accounts: the AI subscriptions and API accounts the workspace's people
// pay for (Claude, ChatGPT, Cursor, Copilot, the Anthropic and OpenAI APIs).
// Agents list them, see which one has room or renews soon, and keep their
// plans, prices and renewals up to date. A workspace feature: the tools
// answer not found until it is switched on.

import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import type { MissionClient } from "@outflare/sdk";
import { z } from "zod";

type Call = (fn: () => Promise<{ data?: unknown; error?: unknown; response: Response }>) => Promise<CallToolResult>;

const read = { readOnlyHint: true, openWorldHint: false } as const;
const write = { readOnlyHint: false, destructiveHint: false, openWorldHint: false } as const;
const provider = z.enum(["claude", "chatgpt", "cursor", "copilot", "anthropic_api", "openai_api"]);
const id = z.string().uuid().describe("The AI account's id, from list_ai_accounts.");

const fields = {
  name: z.string().min(1).max(100).optional().describe("What people call it, such as Claude Max — Design."),
  account_email: z.string().optional().describe("The email the account is on at the AI company. Empty clears it."),
  plan: z.string().max(100).optional().describe("Such as Max 20x, Plus, Team or Pro. Empty clears it."),
  seats: z.number().int().min(1).max(100000).optional(),
  paid_by: z.enum(["company", "person"]).optional().describe("Who pays. Only company-paid accounts count in spend."),
  billing_cycle: z.enum(["monthly", "yearly", "prepaid", "usage", ""]).optional(),
  renews_on: z.string().optional().describe("The next renewal day, YYYY-MM-DD. Empty clears it."),
  price_cents: z.number().int().min(0).optional().describe("The price per billing cycle in US cents."),
  resets_available: z.number().int().min(0).max(1000).optional().describe("Banked resets the AI company lets it use (Codex resets, Claude reset grants)."),
  cancelled: z.boolean().optional(),
  members: z
    .array(z.object({ user_id: z.string().uuid(), role: z.enum(["owner", "user"]).optional() }))
    .optional()
    .describe("The people it belongs to (ids from list_members), replacing the list. Without ai_accounts.manage you must stay on it."),
};

export function registerAIAccountTools(server: McpServer, gtm: MissionClient, workspace: () => string, readOnly: boolean, call: Call): void {
  const path = () => ({ ws: workspace() });

  server.registerTool(
    "list_ai_accounts",
    {
      title: "List AI accounts",
      description:
        "The AI subscriptions and API accounts people pay for: the tool, the email it is on, plan, seats, price, renewal day, who pays, resets available, and who it belongs to (owner_left when they left). Only your own unless you can see everyone's.",
      inputSchema: { provider: provider.optional(), mine: z.boolean().optional().describe("Only the accounts you belong to.") },
      annotations: read,
    },
    (q) => call(() => gtm.GET("/v1/workspaces/{ws}/ai-accounts", { params: { path: path(), query: q } })),
  );

  server.registerTool(
    "get_ai_account",
    { title: "Get an AI account", description: "One AI account, by id.", inputSchema: { id }, annotations: read },
    ({ id }) => call(() => gtm.GET("/v1/workspaces/{ws}/ai-accounts/{id}", { params: { path: { ...path(), id } } })),
  );

  server.registerTool(
    "get_outflare_ai_account",
    {
      title: "What Outflare's own AI spent",
      description: "What Outflare's agent and tools spent this month on the workspace's own AI keys, by provider. get_ai_usage has the detail.",
      inputSchema: {},
      annotations: read,
    },
    () => call(() => gtm.GET("/v1/workspaces/{ws}/ai-accounts/outflare", { params: { path: path() } })),
  );

  server.registerTool(
    "list_ai_receipts",
    {
      title: "List AI receipts",
      description:
        "Receipts people forwarded (Outflare Cloud): vendor, amount, plan, seats, period, the account each matched and how. needs_review lists those waiting for someone to assign or ignore.",
      inputSchema: { status: z.enum(["matched", "needs_review", "ignored"]).optional() },
      annotations: read,
    },
    (q) => call(() => gtm.GET("/v1/workspaces/{ws}/ai-accounts/receipts", { params: { path: path(), query: q } })),
  );

  server.registerTool(
    "get_receipt_forwarding",
    {
      title: "Where to forward AI receipts",
      description: "The workspace's inbound addresses (available on Outflare Cloud), such as the one AI receipts are forwarded to, and your linked personal addresses with any Gmail confirmation to open.",
      inputSchema: {},
      annotations: read,
    },
    async () => {
      const [addresses, forwarders] = await Promise.all([
        gtm.GET("/v1/workspaces/{ws}/inbound-addresses", { params: { path: path() } }),
        gtm.GET("/v1/workspaces/{ws}/inbound-forwarders", { params: { path: path() } }),
      ]);
      return call(async () => (addresses.error ? addresses : { ...addresses, data: { ...addresses.data, forwarders: forwarders.data?.data ?? [] } }));
    },
  );

  if (readOnly) return;

  server.registerTool(
    "assign_ai_receipt",
    {
      title: "Assign a receipt to an AI account",
      description: "For a receipt that needs review: the account's renewal, plan, seats and price follow it, and the next one from the same account matches by itself.",
      inputSchema: { id: z.string().uuid().describe("The receipt's id, from list_ai_receipts."), account_id: z.string().uuid().describe("The AI account, from list_ai_accounts.") },
      annotations: write,
    },
    ({ id, account_id }) => call(() => gtm.POST("/v1/workspaces/{ws}/ai-accounts/receipts/{id}/assign", { params: { path: { ...path(), id } }, body: { account_id } })),
  );

  server.registerTool(
    "ignore_ai_receipt",
    {
      title: "Ignore a receipt",
      description: "A receipt that needs review but isn't for an account to track.",
      inputSchema: { id: z.string().uuid() },
      annotations: write,
    },
    ({ id }) => call(() => gtm.POST("/v1/workspaces/{ws}/ai-accounts/receipts/{id}/ignore", { params: { path: { ...path(), id } } })),
  );

  server.registerTool(
    "create_ai_account",
    {
      title: "Add an AI account",
      description: "Add an AI subscription or API account. You are its owner unless members says otherwise.",
      inputSchema: { provider, ...fields, name: z.string().min(1).max(100) },
      annotations: write,
    },
    (body) => call(() => gtm.POST("/v1/workspaces/{ws}/ai-accounts", { params: { path: path() }, body: body as never })),
  );

  server.registerTool(
    "update_ai_account",
    {
      title: "Change an AI account",
      description: "Change an AI account's plan, price, renewal, who pays, resets available or people. Send only what changes.",
      inputSchema: { id, provider: provider.optional(), ...fields },
      annotations: write,
    },
    ({ id, ...body }) => call(() => gtm.PATCH("/v1/workspaces/{ws}/ai-accounts/{id}", { params: { path: { ...path(), id } }, body: body as never })),
  );

  server.registerTool(
    "delete_ai_account",
    {
      title: "Remove an AI account",
      description: "Remove an AI account added by mistake. An account that ended should stay, with cancelled true.",
      inputSchema: { id },
      annotations: { readOnlyHint: false, destructiveHint: true, openWorldHint: false },
    },
    ({ id }) => call(() => gtm.DELETE("/v1/workspaces/{ws}/ai-accounts/{id}", { params: { path: { ...path(), id } } })),
  );
}
