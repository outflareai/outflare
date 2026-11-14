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
  if (readOnly) return;
  const onBrand = brandCaller(client, workspace);
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
