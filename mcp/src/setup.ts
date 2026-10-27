// Brand setup: the ordered checklist that gets one brand's website and
// tools working (website script, verifying the site, then analytics,
// Search Console, email, payments, ads, social and a CRM), the one-click
// installs through the workspace's own Tag Manager or Cloudflare, and
// install health: what is out of date or broken.

import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import type { MissionClient } from "@outflare/sdk";
import { z } from "zod";

type Call = (fn: () => Promise<{ data?: unknown; error?: unknown; response: Response }>) => Promise<CallToolResult>;

const read = { readOnlyHint: true, openWorldHint: false } as const;
const write = { readOnlyHint: false, destructiveHint: false, openWorldHint: false } as const;
const brandRef = z.string().min(1).describe("The brand's handle (such as repocket) or id, from list_brands.");
const steps = ["business_model", "site_script", "verify_site", "analytics", "search", "email", "payments", "ads", "social", "crm"] as const;
const installSteps = ["site_script", "verify_site"] as const;

export function registerSetupTools(server: McpServer, gtm: MissionClient, workspace: () => string, readOnly: boolean, call: Call): void {
  const path = (brand: string) => ({ ws: workspace(), brand });

  server.registerTool(
    "get_brand_setup",
    {
      title: "Brand setup checklist",
      description:
        "One brand's setup steps, most needed first: say what the brand does (business_model, set with update_brand from list_business_models), add the website script, verify the website, then connect analytics, Search Console, email, payments, ads, social and a CRM. " +
        "Each step is todo, waiting (Outflare is waiting for a first visit or for DNS), done (however it was done) or skipped. " +
        "The two install steps carry install: the snippet or DNS record, ai_prompt (a prompt for an AI coding tool: if you can edit the site's code, follow it yourself), " +
        "developer (a message the person can send), and whether a one-click install is ready (connections to Google Tag Manager or Cloudflare). " +
        "Other steps name connectors to suggest; request_connection asks the person to connect one. Offer once to help with next_step.",
      inputSchema: { brand: brandRef },
      annotations: read,
    },
    ({ brand }) => call(() => gtm.GET("/v1/workspaces/{ws}/brands/{brand}/setup", { params: { path: path(brand) } })),
  );

  server.registerTool(
    "list_business_models",
    {
      title: "List business models",
      description:
        "What a brand can say it does, in groups (B2B SaaS, online store, agency and so on), and the ways its customers pay. " +
        "Set a brand's with update_brand (business_model, revenue_models); it decides which reports and funnels suit it. Pick the closest; ask the person when unsure.",
      annotations: read,
    },
    () => call(() => gtm.GET("/v1/business-models")),
  );

  server.registerTool(
    "list_tag_containers",
    {
      title: "List Tag Manager containers",
      description: "The web containers a Google Tag Manager connection can see, the ones on the brand's website first (matches_website). Pick one for install_brand_setup.",
      inputSchema: { brand: brandRef, integration_id: z.string().uuid().describe("The Tag Manager connection, from the site_script step's install.connections.") },
      annotations: read,
    },
    ({ brand, integration_id }) => call(() => gtm.GET("/v1/workspaces/{ws}/brands/{brand}/setup/tag-containers", { params: { path: path(brand), query: { integration_id } } })),
  );

  server.registerTool(
    "get_install_health",
    {
      title: "Install health",
      description:
        "What is out of date or broken, worst first: a site that never sent a visit or stopped, an old copy of the website script, an unverified site, a connection that needs signing in again, " +
        "an SDK, CLI or MCP server below the supported version, or calls to a deprecated API operation. Each check has fix.tool or fix.page and fix.who.",
      inputSchema: { brand: z.string().optional().describe("Only this brand's checks (handle or id). Required for a token kept to some brands.") },
      annotations: read,
    },
    ({ brand }) => call(() => gtm.GET("/v1/workspaces/{ws}/install-health", { params: { path: { ws: workspace() }, query: brand ? { brand } : {} } })),
  );

  if (readOnly) return;

  server.registerTool(
    "update_brand_setup",
    {
      title: "Skip or close brand setup steps",
      description: "Leave setup steps for later (skip), bring them back (unskip), or close the checklist so Home stops showing it (dismissed). A skipped step still counts once it is done. Needs workspace.manage.",
      inputSchema: {
        brand: brandRef,
        skip: z.array(z.enum(steps)).optional(),
        unskip: z.array(z.enum(steps)).optional(),
        dismissed: z.boolean().optional(),
      },
      annotations: write,
    },
    ({ brand, ...body }) => call(() => gtm.PATCH("/v1/workspaces/{ws}/brands/{brand}/setup", { params: { path: path(brand) }, body })),
  );

  server.registerTool(
    "prepare_brand_setup",
    {
      title: "Make what a setup step installs",
      description: "For site_script, makes the brand's website script key; for verify_site, adds the brand's website so it has a DNS record to add. Safe to call again. Needs workspace.manage.",
      inputSchema: { brand: brandRef, step: z.enum(installSteps) },
      annotations: write,
    },
    ({ brand, step }) => call(() => gtm.POST("/v1/workspaces/{ws}/brands/{brand}/setup/prepare", { params: { path: path(brand) }, body: { step } })),
  );

  server.registerTool(
    "install_brand_setup",
    {
      title: "Do a setup step in one click",
      description:
        "With the workspace's own connection: site_script with gtm adds the website script to a Tag Manager container (list_tag_containers) and publishes it unless publish is false; " +
        "verify_site with dns adds the site's TXT record on Cloudflare and then checks for it every minute. Queued: read get_brand_setup for install.last. Changes the person's site, so confirm first. Needs workspace.manage.",
      inputSchema: {
        brand: brandRef,
        step: z.enum(installSteps),
        method: z.enum(["gtm", "dns"]),
        integration_id: z.string().uuid(),
        container: z.string().optional().describe("For gtm: a container id from list_tag_containers."),
        publish: z.boolean().optional().describe("For gtm: false leaves the change in Tag Manager for a person to publish."),
      },
      annotations: { ...write, openWorldHint: true },
    },
    ({ brand, ...body }) => call(() => gtm.POST("/v1/workspaces/{ws}/brands/{brand}/setup/install", { params: { path: path(brand) }, body })),
  );

  server.registerTool(
    "check_brand_setup",
    {
      title: "Check a site's verification now",
      description: "Looks for the verify_site DNS record (or meta tag or file) now, and keeps looking for three days if it isn't there yet. Needs workspace.manage.",
      inputSchema: { brand: brandRef },
      annotations: write,
    },
    ({ brand }) => call(() => gtm.POST("/v1/workspaces/{ws}/brands/{brand}/setup/check", { params: { path: path(brand) }, body: { step: "verify_site" } })),
  );
}
