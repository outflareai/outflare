// Paid link orders: the catalog, orders, and a budget summary. Approve and
// pay are person-only on the API and are not offered as tools.

import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import type { MissionClient } from "@outflare/sdk";
import { z } from "zod";

type Call = (fn: () => Promise<{ data?: unknown; error?: unknown; response: Response }>) => Promise<CallToolResult>;

const brand = z.string().uuid().describe("Brand ID. Link orders are always limited to one brand.");
const read = { readOnlyHint: true, openWorldHint: false } as const;
const write = { readOnlyHint: false, destructiveHint: false, openWorldHint: false } as const;

export function registerLinkOrderTools(
  server: McpServer,
  gtm: MissionClient,
  readOnly: boolean,
  call: Call,
): void {
  server.registerTool(
    "list_link_order_catalog",
    {
      title: "List the link-order catalog",
      description: "Donor profiles for paid link orders: domain, niches, PBN flag, and current published prices in minor units (the donor price only). Page with cursor=next_cursor.",
      inputSchema: {
        brand,
        cursor: z.string().optional().describe("next_cursor from the previous page."),
        limit: z.number().int().min(1).max(200).optional(),
        q: z.string().optional().describe("Filter by domain."),
        pbn: z.boolean().optional(),
        niche: z.string().optional(),
        archived: z.enum(["true", "false", "all"]).optional().describe("Default omits archived rows."),
      },
      annotations: read,
    },
    ({ brand, ...query }) =>
      call(() =>
        gtm.GET("/v1/brands/{brand}/link-orders/catalog", { params: { path: { brand }, query } }),
      ),
  );

  server.registerTool(
    "list_link_orders",
    {
      title: "List link orders",
      description: "Paid link orders for a brand, newest first. Page with cursor=next_cursor. Amounts are the donor price only; USD and EUR are never summed.",
      inputSchema: {
        brand,
        cursor: z.string().optional().describe("next_cursor from the previous page."),
        limit: z.number().int().min(1).max(200).optional(),
        status: z.enum(["requested", "quoted", "negotiating", "approved", "published", "paid", "verified", "lost"]).optional(),
        donor_profile_id: z.string().uuid().optional(),
        q: z.string().optional().describe("Filter by domain, target URL, or published URL."),
      },
      annotations: read,
    },
    ({ brand, ...query }) =>
      call(() =>
        gtm.GET("/v1/brands/{brand}/link-orders", { params: { path: { brand }, query } }),
      ),
  );

  server.registerTool(
    "get_link_order",
    {
      title: "Get a link order",
      description: "One paid link order, including its status, frozen donor amount, and last live-link check.",
      inputSchema: { brand, id: z.string().uuid().describe("The order id.") },
      annotations: read,
    },
    ({ brand, id }) =>
      call(() =>
        gtm.GET("/v1/brands/{brand}/link-orders/{id}", { params: { path: { brand, id } } }),
      ),
  );

  server.registerTool(
    "get_link_order_summary",
    {
      title: "Summarize link orders",
      description: "Counts by status, and donor amounts by currency. Committed is approved plus published not yet paid; spent is paid and verified. USD and EUR are never summed.",
      inputSchema: { brand },
      annotations: read,
    },
    ({ brand }) =>
      call(() =>
        gtm.GET("/v1/brands/{brand}/link-orders/summary", { params: { path: { brand } } }),
      ),
  );

  if (readOnly) {
    return;
  }

  server.registerTool(
    "create_link_order",
    {
      title: "Request a link order",
      description: "Open a requested paid-link order against a catalog donor. The guarantee window defaults to the catalog row, then to 30 days. This does not approve or pay; those stay with the brand's designated approver.",
      inputSchema: {
        brand,
        donor_profile_id: z.string().uuid(),
        target_url: z.string().min(1),
        slot_type: z.string().optional(),
        rel_requested: z.string().optional(),
        guarantee_days: z.number().int().min(0).optional(),
        notes: z.string().optional(),
        owner_user_id: z.string().uuid().optional(),
        replaces_order_id: z.string().uuid().optional().describe("The lost order this one replaces, if any."),
      },
      annotations: write,
    },
    ({ brand, ...body }) =>
      call(() =>
        gtm.POST("/v1/brands/{brand}/link-orders", { params: { path: { brand } }, body }),
      ),
  );

  server.registerTool(
    "import_link_order_catalog",
    {
      title: "Import the link-order catalog from CSV",
      description: "The first row is the header and must include domain. A repeated domain updates the existing profile. A bad row is listed in errors and does not abort the rest of the file. Amounts are the donor price only, in minor units.",
      inputSchema: {
        brand,
        csv: z.string().min(1).describe("The CSV file contents, including the header row."),
      },
      annotations: write,
    },
    ({ brand, csv }) =>
      call(() =>
        gtm.POST("/v1/brands/{brand}/link-orders/catalog/import", { params: { path: { brand } }, body: { csv } }),
      ),
  );
}
