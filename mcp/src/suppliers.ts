// Tools for mailbox suppliers (Cheap Inboxes and any other supplier
// connector): see what a connected supplier account holds, import its
// mailboxes, and keep them in sync. Connecting the account itself is the
// connectors' job (POST /v1/workspaces/{ws}/integrations).

import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import type { MissionClient } from "@outflare/sdk";
import { z } from "zod";

type Call = (fn: () => Promise<{ data?: unknown; error?: unknown; response: Response }>) => Promise<CallToolResult>;

const id = z.string().uuid();
const read = { readOnlyHint: true, openWorldHint: false } as const;
const external = { readOnlyHint: false, destructiveHint: false, openWorldHint: true } as const;

// workspace is read on every call: with an agency, use_client changes it.
export function registerSupplierTools(server: McpServer, gtm: MissionClient, workspace: () => string, readOnly: boolean, call: Call): void {
  const supplier = id.describe("The supplier integration's id, from the integrations list (connector cheapinboxes).");

  server.registerTool(
    "list_supplier_mailboxes",
    {
      title: "List a supplier's mailboxes",
      description:
        "Every mailbox a connected mailbox supplier (e.g. Cheap Inboxes) holds, imported or not: status at the supplier, other platforms it is " +
        "connected to, and import_state (available, queued, waiting, app_password, imported, failed) with the Outflare mailbox once imported. " +
        "counts summarises the whole account. synced is false until the first sync finishes.",
      inputSchema: {
        integration_id: supplier,
        import_state: z.enum(["available", "queued", "waiting", "app_password", "imported", "failed"]).optional(),
        status: z.enum(["provisioning", "active", "cancelling", "cancelled", "failed"]).optional(),
        q: z.string().optional().describe("Part of the email address."),
        limit: z.number().int().min(1).max(1000).optional(),
        offset: z.number().int().min(0).optional(),
      },
      annotations: read,
    },
    ({ integration_id, ...query }) =>
      call(() => gtm.GET("/v1/workspaces/{ws}/integrations/{id}/mailboxes", { params: { path: { ws: workspace(), id: integration_id }, query } })),
  );

  if (readOnly) return;

  server.registerTool(
    "import_supplier_mailboxes",
    {
      title: "Import mailboxes from a supplier",
      description:
        "Import mailboxes from a connected supplier: all not imported yet, or the listed ones (by email or external_id). Passwords come from " +
        "the supplier; each mailbox gets SMTP, IMAP and DNS checks like a CSV import. Google mailboxes first get an app password from the " +
        "supplier, about 20 an hour, so large Google imports take hours: follow progress with list_supplier_mailboxes. Mailboxes still being " +
        "set up import when they turn active.",
      inputSchema: {
        integration_id: supplier,
        all: z.boolean().optional(),
        emails: z.array(z.string()).max(5000).optional(),
        external_ids: z.array(z.string()).max(5000).optional(),
      },
      annotations: external,
    },
    ({ integration_id, ...body }) =>
      call(() => gtm.POST("/v1/workspaces/{ws}/integrations/{id}/mailboxes/import", { params: { path: { ws: workspace(), id: integration_id } }, body })),
  );

  server.registerTool(
    "push_supplier_mailboxes",
    {
      title: "Have a supplier push mailboxes in",
      description:
        "Ask the supplier to send mailboxes into Outflare through its push connection (turn it on first with update_supplier_sync push: true). " +
        "all: every active mailbox not in Outflare yet; or the listed external_ids. Pushed mailboxes are checked like any import. " +
        "The counts arrive in push.last_result of list_supplier_mailboxes.",
      inputSchema: {
        integration_id: supplier,
        all: z.boolean().optional(),
        external_ids: z.array(z.string()).max(5000).optional(),
      },
      annotations: external,
    },
    ({ integration_id, ...body }) =>
      call(() => gtm.POST("/v1/workspaces/{ws}/integrations/{id}/mailboxes/push", { params: { path: { ws: workspace(), id: integration_id } }, body })),
  );

  server.registerTool(
    "sync_supplier_mailboxes",
    {
      title: "Refresh a supplier's mailboxes",
      description: "Read the supplier's mailboxes again now, for example right after ordering more. Mailboxes the supplier cancelled are paused. Runs in the background.",
      inputSchema: { integration_id: supplier },
      annotations: { ...external, idempotentHint: true },
    },
    ({ integration_id }) =>
      call(() => gtm.POST("/v1/workspaces/{ws}/integrations/{id}/mailboxes/sync", { params: { path: { ws: workspace(), id: integration_id } } })),
  );

  server.registerTool(
    "update_supplier_sync",
    {
      title: "Change how a supplier is synced",
      description:
        "auto_import: import every active mailbox now and new ones as they appear. app_passwords: get Google app passwords from the supplier " +
        "before importing Google mailboxes (on by default; Google refuses plain passwords). daily_limit: daily cap for imported mailboxes, null for the supplier's. " +
        "push: let the supplier push mailboxes in, the way it does to a sequencer (Outflare adds itself there as an EmailBison connection with " +
        "a token that can only read and add mailboxes; needs integrations.manage and push.available). Connecting runs in the background: follow push.state.",
      inputSchema: {
        integration_id: supplier,
        auto_import: z.boolean().optional(),
        app_passwords: z.boolean().optional(),
        daily_limit: z.number().int().min(1).max(500).nullable().optional(),
        push: z.boolean().optional(),
      },
      annotations: { readOnlyHint: false, destructiveHint: false, openWorldHint: false, idempotentHint: true },
    },
    ({ integration_id, ...body }) =>
      call(() => gtm.PATCH("/v1/workspaces/{ws}/integrations/{id}/mailbox-sync", { params: { path: { ws: workspace(), id: integration_id } }, body })),
  );
}
