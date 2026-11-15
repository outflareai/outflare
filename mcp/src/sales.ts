import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import type { MissionClient } from "@outflare/sdk";
import { z } from "zod";
import { brandCaller, brandRef } from "./records.js";

type Call = (
  fn: () => Promise<{ data?: unknown; error?: unknown; response: Response }>,
) => Promise<CallToolResult>;
export function registerSalesTools(
  server: McpServer,
  client: MissionClient,
  workspace: () => string,
  readOnly: boolean,
  call: Call,
) {
  const onBrand = brandCaller(client, workspace);
  const reportInput = {
    brand: brandRef,
    period: z.enum(["24h", "48h", "7d", "30d", "365d"]).optional(),
    integration: z.string().uuid().optional(),
    from: z.string().optional(),
    to: z.string().optional(),
  };
  server.registerTool(
    "get_sales_reporting",
    {
      title: "Read Sales reporting",
      description:
        "Compare Sales milestones, original uploader performance, submission quality, costs and capacity. Requires a Sales manager or workspace owner. Portal data requires a brand-owned Lead Portal connection; native outcomes come from Outflare.",
      inputSchema: reportInput,
      annotations: {
        readOnlyHint: true,
        destructiveHint: false,
        openWorldHint: true,
      },
    },
    ({ brand, ...query }) =>
      call(() =>
        onBrand(brand, (id) =>
          client.GET("/v1/brands/{brand}/sales/reporting", {
            params: { path: { brand: id }, query },
          }),
        ),
      ),
  );
  server.registerTool(
    "get_sales_reporting_leads",
    {
      title: "Inspect Sales milestones",
      description:
        "List the companies behind selected milestones in a bounded interval. Each stage has its own count and 50-row page, including original uploader attribution when recorded. Comma-separated stages: discovered, researched, verified, emailed, replied, sdr, closed.",
      inputSchema: {
        ...reportInput,
        stages: z.string().min(1),
        offset: z.number().int().min(0).max(1000000).optional(),
      },
      annotations: {
        readOnlyHint: true,
        destructiveHint: false,
        openWorldHint: true,
      },
    },
    ({ brand, ...query }) =>
      call(() =>
        onBrand(brand, (id) =>
          client.GET("/v1/brands/{brand}/sales/reporting/leads", {
            params: { path: { brand: id }, query },
          }),
        ),
      ),
  );
  if (readOnly) return;
  server.registerTool(
    "import_sales_lead",
    {
      title: "Import a Sales conversation",
      description:
        "Import a qualified Sales reply from an external source. Create/find the person and company using the CRM record tools first. Preserve stable source ids, the original workspace-owned EmailBison connection, sender and reply ids, and actual RFC headers. Replays append provider copies without creating another lead. This tool does not send mail. Personal templates, replies and mailbox consent require each person's own signed-in Sales session.",
      inputSchema: {
        brand: brandRef,
        source: z.string().min(1).max(100),
        source_id: z.string().min(1).max(200),
        record_id: z.string().uuid(),
        integration_id: z.string().uuid(),
        sender_id: z.string().min(1),
        reply_id: z.string().min(1),
        messages: z
          .array(
            z.object({
              direction: z.enum(["incoming", "outgoing"]),
              from: z.string(),
              to: z.array(z.string()),
              cc: z.array(z.string()).optional(),
              subject: z.string(),
              body: z.string(),
              occurred_at: z.string(),
              provider_id: z.string().min(1),
              message_id: z.string().optional(),
              in_reply_to: z.string().optional(),
              references: z.string().optional(),
              thread_id: z.string().optional(),
            }),
          )
          .min(1)
          .max(100),
      },
      annotations: {
        readOnlyHint: false,
        destructiveHint: false,
        openWorldHint: false,
      },
    },
    ({ brand, ...body }) =>
      call(() =>
        onBrand(brand, (id) =>
          client.POST("/v1/brands/{brand}/sales/bridge", {
            params: { path: { brand: id } },
            body,
          }),
        ),
      ),
  );
}
