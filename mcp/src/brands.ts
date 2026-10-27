// Brands: the businesses one company markets, each with its own website,
// such as Geonode and Repocket. Connectors, members, tokens and leads are
// the workspace's and shared; a brand owns its context, memories, projects
// and sending setup. get_context with a brand layers its context on the
// company's.

import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import type { MissionClient } from "@outflare/sdk";
import { z } from "zod";

type Call = (fn: () => Promise<{ data?: unknown; error?: unknown; response: Response }>) => Promise<CallToolResult>;

const id = z.string().uuid();
const read = { readOnlyHint: true, openWorldHint: false } as const;
const write = { readOnlyHint: false, destructiveHint: false, openWorldHint: false } as const;
const brandRef = z.string().min(1).describe("The brand's handle (such as repocket) or id, from list_brands.");
const platforms = z.array(z.enum(["web", "ios", "android", "tv", "desktop", "console", "extension", "api"]));
const links = z
  .array(
    z.object({
      url: z.string().min(1).max(2000),
      kind: z
        .enum(["app_store", "google_play", "tv_app", "chrome_web_store", "microsoft_store", "steam", "docs", "help", "blog", "status", "x", "linkedin", "youtube", "instagram", "tiktok", "facebook", "discord", "reddit", "github", "bluesky", "threads", "mastodon", "dev_to", "docker_hub", "other"])
        .optional()
        .describe("Guessed from the address when left out."),
      label: z.string().max(60).optional().describe("Such as Fire TV or Support docs."),
    }),
  )
  .max(50)
  .describe("Where else the brand lives: app store listings, socials, docs. Replaces the brand's links.");

const records = {
  project_ids: z.array(id).max(10000).optional().describe("A project brings everything in it."),
  domains: z.array(z.string()).max(10000).optional(),
  mailbox_ids: z.array(id).max(10000).optional(),
  lead_list_ids: z.array(id).max(10000).optional(),
  sequence_ids: z.array(id).max(10000).optional(),
  mission_ids: z.array(id).max(10000).optional(),
  with_mailboxes: z.boolean().optional().describe("Whether a domain's mailboxes move with it. Default true."),
};

export function registerBrandTools(server: McpServer, gtm: MissionClient, workspace: () => string, readOnly: boolean, call: Call): void {
  const path = () => ({ ws: workspace() });
  const brandPath = (brand: string) => ({ ws: workspace(), brand });

  server.registerTool(
    "list_brands",
    {
      title: "List brands",
      description:
        "The businesses this company markets, each with its own website (such as Geonode and Repocket), with their domains, platforms and what each owns. " +
        "Empty for a company with one brand. When there are several, work out which brand a task is for (from its project or sequence, or ask) " +
        "and pass brand to get_context and the list tools.",
      inputSchema: { status: z.enum(["active", "archived", "all"]).optional().describe("Default: active brands.") },
      annotations: read,
    },
    (q) => call(() => gtm.GET("/v1/workspaces/{ws}/brands", { params: { path: path(), query: q } })),
  );

  server.registerTool(
    "get_brand_context",
    {
      title: "Read a brand's own context",
      description: "What is true for one brand only: about, audience, goals, voice, rules and notes. get_context with brand gives this layered on the company's, which is usually what you want.",
      inputSchema: { brand: brandRef },
      annotations: read,
    },
    ({ brand }) => call(() => gtm.GET("/v1/workspaces/{ws}/brands/{brand}/context", { params: { path: brandPath(brand) } })),
  );

  server.registerTool(
    "get_brand_rules",
    {
      title: "Rules between brands",
      description:
        "The workspace's cross-brand contact rule: with cross_brand_days set, no person gets a first email from one brand within that many days of an email from another " +
        "(the send policy blocks it as other_brand:<handle>). Null: off.",
      inputSchema: {},
      annotations: read,
    },
    () => call(() => gtm.GET("/v1/workspaces/{ws}/brand-rules", { params: { path: path() } })),
  );

  server.registerTool(
    "list_brand_link_suggestions",
    {
      title: "Links found for a brand",
      description:
        "The social profiles and app store listings found on a brand's website (find_brand_links), waiting to be added or dismissed, " +
        "and gaps: the common places (x, linkedin, youtube, instagram, tiktok, facebook) with neither a link nor a suggestion. " +
        "can_discover false means the brand has no website or the workspace has no Geonode connection to read it with.",
      inputSchema: { brand: brandRef, status: z.enum(["suggested", "added", "dismissed", "all"]).optional().describe("Default: suggested.") },
      annotations: read,
    },
    ({ brand, status }) => call(() => gtm.GET("/v1/workspaces/{ws}/brands/{brand}/link-suggestions", { params: { path: brandPath(brand), query: { status } } })),
  );

  if (readOnly) return;

  server.registerTool(
    "create_brand",
    {
      title: "Add a brand",
      description:
        "Adds a brand to the workspace: a business with its own website. Connectors, members and tokens are shared, so nothing needs reconnecting. " +
        "Then save what you know about it with update_brand_context (offer to read its website) and move its projects or domains in with add_to_brand.",
      inputSchema: {
        name: z.string().min(1).max(100),
        website: z.string().max(253).optional().describe("Its main site, such as repocket.com."),
        domains: z.array(z.string()).max(100).optional().describe("Its other domains. The website is always included."),
        platforms: platforms.optional().describe("Where it ships."),
        links: links.optional(),
        handle: z.string().min(2).max(40).optional().describe("Short name for URLs and calls. Made from the name when left out."),
      },
      annotations: write,
    },
    (body) => call(() => gtm.POST("/v1/workspaces/{ws}/brands", { params: { path: path() }, body })),
  );

  server.registerTool(
    "update_brand",
    {
      title: "Edit or archive a brand",
      description: "Changes a brand's name, handle, website, domains, platforms, links (app store listings, socials, docs), business model or how its customers pay. status archived hides it from pickers and keeps its records; brands are never deleted.",
      inputSchema: {
        brand: brandRef,
        name: z.string().min(1).max(100).optional(),
        handle: z.string().min(2).max(40).optional(),
        website: z.string().max(253).optional(),
        domains: z.array(z.string()).max(100).optional(),
        platforms: platforms.optional(),
        links: links.optional(),
        status: z.enum(["active", "archived"]).optional(),
        business_model: z.string().max(60).optional().describe("What the brand does: a model id from list_business_models; empty clears it."),
        revenue_models: z.array(z.string()).max(10).optional().describe("How its customers pay: ids from list_business_models. Replaces the list."),
      },
      annotations: { ...write, idempotentHint: true },
    },
    ({ brand, ...body }) => call(() => gtm.PATCH("/v1/workspaces/{ws}/brands/{brand}", { params: { path: brandPath(brand) }, body })),
  );

  server.registerTool(
    "find_brand_links",
    {
      title: "Find a brand's social links",
      description:
        "Reads the brand's website through the workspace's own Geonode connection and suggests the social profiles and app store listings it links to. " +
        "Runs in the background; then list_brand_link_suggestions. Nothing is added until add_brand_link. Needs a website and a Geonode connection (one page read, a token or two).",
      inputSchema: { brand: brandRef },
      annotations: write,
    },
    ({ brand }) => call(() => gtm.POST("/v1/workspaces/{ws}/brands/{brand}/links/discover", { params: { path: brandPath(brand) } })),
  );

  server.registerTool(
    "add_brand_link",
    {
      title: "Add a found link to a brand",
      description:
        "Adds a suggestion from list_brand_link_suggestions to the brand's links, with the address corrected if url is given. Ask the person first unless they said to add them. " +
        "An X link is then read every day for its followers through Geonode. To add a link nobody found, use update_brand with links.",
      inputSchema: {
        brand: brandRef,
        suggestion_id: id,
        url: z.string().min(1).max(2000).optional().describe("The corrected address, when the found one is wrong."),
        kind: links.element.shape.kind,
        label: z.string().max(60).optional(),
      },
      annotations: write,
    },
    ({ brand, suggestion_id, ...body }) =>
      call(() => gtm.POST("/v1/workspaces/{ws}/brands/{brand}/link-suggestions/{suggestion}/add", { params: { path: { ...brandPath(brand), suggestion: suggestion_id } }, body })),
  );

  server.registerTool(
    "dismiss_brand_link",
    {
      title: "Dismiss a found link",
      description: "Marks a suggestion as not the brand's. It is not suggested again.",
      inputSchema: { brand: brandRef, suggestion_id: id },
      annotations: { ...write, idempotentHint: true },
    },
    ({ brand, suggestion_id }) =>
      call(() => gtm.POST("/v1/workspaces/{ws}/brands/{brand}/link-suggestions/{suggestion}/dismiss", { params: { path: { ...brandPath(brand), suggestion: suggestion_id } } })),
  );

  server.registerTool(
    "update_brand_context",
    {
      title: "Save what you learned about a brand",
      description:
        "Saves what is true for one brand, in the person's words where possible. Rules add to the company's rules; the other fields win over the company's where set. " +
        "What is true for every brand belongs in update_context; how the brands relate goes in update_context's brands field. " +
        "Pass the version you read (409 context_changed means read again).",
      inputSchema: {
        brand: brandRef,
        about: z.string().max(4000).optional().describe("What the brand is and does."),
        audience: z.string().max(4000).optional().describe("Who it is for."),
        goals: z.string().max(4000).optional(),
        voice: z.string().max(4000).optional().describe("How its emails and posts sound."),
        rules: z.string().max(4000).optional().describe("Things never to do for this brand."),
        notes: z.string().max(100000).optional().describe("Everything else, in Markdown."),
        version: z.number().int().optional().describe("version from get_brand_context, or brand.version from get_context."),
      },
      annotations: { ...write, idempotentHint: true },
    },
    ({ brand, ...body }) => call(() => gtm.PATCH("/v1/workspaces/{ws}/brands/{brand}/context", { params: { path: brandPath(brand) }, body })),
  );

  server.registerTool(
    "add_to_brand",
    {
      title: "Move records into a brand",
      description:
        "Moves projects (with everything in them), domains (with their mailboxes), mailboxes, lead lists, sequences or missions into a brand. " +
        "A record inside a project keeps the project's brand (counted in in_project): move the project instead. A brand's mailboxes then only send for that brand's sequences.",
      inputSchema: { brand: brandRef, ...records },
      annotations: write,
    },
    ({ brand, ...body }) => call(() => gtm.POST("/v1/workspaces/{ws}/brands/{brand}/add", { params: { path: brandPath(brand) }, body })),
  );

  server.registerTool(
    "set_brand_rules",
    {
      title: "Turn the cross-brand contact rule on or off",
      description: "Sets how many days (1 to 365) a person is kept from a first email by a second brand after another brand emailed them, or null to turn the rule off.",
      inputSchema: { cross_brand_days: z.number().int().min(1).max(365).nullable() },
      annotations: write,
    },
    (body) => call(() => gtm.PATCH("/v1/workspaces/{ws}/brand-rules", { params: { path: path() }, body })),
  );

  server.registerTool(
    "remove_from_brand",
    {
      title: "Make a brand's records shared",
      description: "Takes projects, domains, mailboxes, lead lists, sequences or missions out of a brand so every brand shares them again.",
      inputSchema: { brand: brandRef, ...records },
      annotations: write,
    },
    ({ brand, ...body }) => call(() => gtm.POST("/v1/workspaces/{ws}/brands/{brand}/remove", { params: { path: brandPath(brand) }, body })),
  );
}
