// Brands: the businesses one company markets, each with its own website,
// such as Geonode and Repocket. Connectors, members, tokens and leads are
// the workspace's and shared; a brand owns its context, memories, lists,
// sequences and sending setup. get_context with a brand reads its context,
// which no other brand shares.

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
        "Empty for a company with one brand. When there are several, work out which brand a task is for (from its sequence or list, or ask) " +
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
      description: "What is true for one brand only: about, audience, goals, voice, rules and notes. get_context with brand gives this with the brand's memories and knowledge, which is usually what you want.",
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
        "Then save what you know about it with update_brand_context (offer to read its website) and move its domains and sequences in with add_to_brand.",
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
      description: "Changes a brand's name, handle, website, domains, platforms, links (app store listings, socials, docs), business model, how its customers pay, or its own logo and colours. status archived hides it from pickers and keeps its records; brands are never deleted.",
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
        logo_url: z.string().max(2000).optional().describe("The brand's own logo (https) where it differs from the company's; empty for the company's."),
        colors: z
          .object({ primary: z.string(), background: z.string(), text: z.string() })
          .optional()
          .describe("The brand's own colours, as hex; empty for the company's."),
      },
      annotations: { ...write, idempotentHint: true },
    },
    ({ brand, ...body }) => call(() => gtm.PATCH("/v1/workspaces/{ws}/brands/{brand}", { params: { path: brandPath(brand) }, body })),
  );

  server.registerTool(
    "read_brand_website",
    {
      title: "Read a brand's website for what it offers",
      description:
        "Reads the brand's website through the workspace's own Geonode connection (home page, sitemap, up to 40 pages about the offer) and has the workspace's own AI model write the brand's knowledge: " +
        "a summary, the industry and bullets for products, services, features, integrations, capabilities, who it serves and use cases, each with its page. " +
        "Runs in the background; then get_brand_context (knowledge, knowledge_read_at, knowledge_error). Runs on its own when a brand is created with a website. AI visibility drafts questions from it.",
      inputSchema: { brand: brandRef },
      annotations: write,
    },
    ({ brand }) => call(() => gtm.POST("/v1/workspaces/{ws}/brands/{brand}/knowledge/read", { params: { path: brandPath(brand) } })),
  );

  server.registerTool(
    "update_brand_knowledge",
    {
      title: "Edit what the brand's website says it offers",
      description:
        "Replaces the brand's knowledge (summary, industry, and the lists products, services, features, integrations, capabilities, audiences, use_cases, each bullet with the page it came from or ''). " +
        "Read it with get_brand_context first and send it back whole with your changes.",
      inputSchema: {
        brand: brandRef,
        summary: z.string().max(2000),
        industry: z.string().max(200),
        lists: z.record(z.string(), z.array(z.object({ text: z.string().max(300), url: z.string().max(2048) }))),
        pages: z.array(z.string()).optional(),
      },
      annotations: { ...write, idempotentHint: true },
    },
    ({ brand, ...body }) => call(() => gtm.PUT("/v1/workspaces/{ws}/brands/{brand}/knowledge", { params: { path: brandPath(brand) }, body: { ...body, pages: body.pages ?? [] } })),
  );

  // ---- Knowledge sheet review (paid ads) ----
  server.registerTool(
    "get_brand_knowledge",
    {
      title: "Read a brand's knowledge sheet and whether it is trusted",
      description:
        "The brand's knowledge (what its website says it offers) with how it stands: read_at and read_error (connector_needed without a page reader, ai_model_needed without an AI model, '' when the read worked), " +
        "reviewed_at and reviewed_by, changed_since_review (a read changed what it sells since), refresh_daily (on once an ad account is connected) and trusted. Ads are written only from a trusted sheet.",
      inputSchema: { brand: id.describe("The brand's id from list_brands.") },
      annotations: read,
    },
    ({ brand }) => call(() => gtm.GET("/v1/brands/{brand}/knowledge", { params: { path: { brand } } })),
  );

  server.registerTool(
    "review_brand_knowledge",
    {
      title: "Say the brand's knowledge sheet looks right",
      description:
        "A person's session only (an API token or agent gets 403): marks the brand's knowledge sheet reviewed, which lets ads be written from it (write_ad_draft). " +
        "Ask the person to check the sheet (get_brand_knowledge) first; never call it on their behalf. A later read that changes the brand's products, services, prices or summary clears the review.",
      inputSchema: { brand: id.describe("The brand's id from list_brands.") },
      annotations: { ...write, idempotentHint: true },
    },
    ({ brand }) => call(() => gtm.POST("/v1/brands/{brand}/knowledge/review", { params: { path: { brand } } })),
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
    "find_brand_look",
    {
      title: "Find a brand's logo and colour on its website",
      description:
        "Reads the brand's home page as OutflareBot (obeying robots.txt) for its logo and theme colour, and fills in the brand's logo_url and main colour only where they are empty. " +
        "Runs in the background; then list_brands. Never replaces a logo or colour someone set. Needs a website; no connection or key.",
      inputSchema: { brand: brandRef },
      annotations: write,
    },
    ({ brand }) => call(() => gtm.POST("/v1/workspaces/{ws}/brands/{brand}/look/find", { params: { path: brandPath(brand) } })),
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
        "Saves what is true for one brand, in the person's words where possible. Each brand's context is its own: nothing is shared between brands. " +
        "legal_name, tax_id and address are the business behind the brand: lifecycle email footers ({{brand_address}}), its help centre and cookie policy reuse them. " +
        "Pass the version you read (409 context_changed means read again).",
      inputSchema: {
        brand: brandRef,
        about: z.string().max(4000).optional().describe("What the brand is and does."),
        audience: z.string().max(4000).optional().describe("Who it is for."),
        goals: z.string().max(4000).optional(),
        voice: z.string().max(4000).optional().describe("How its emails and posts sound."),
        rules: z.string().max(10000).optional().describe("Things never to do for this brand."),
        notes: z.string().max(210000).optional().describe("Everything else, in Markdown."),
        legal_name: z.string().max(200).optional().describe("The registered business name, such as Northpeak Pty Ltd."),
        tax_id: z.string().max(60).optional().describe("Its tax or company number: ABN, VAT, EIN."),
        address: z
          .object({
            line1: z.string().max(200).describe("The street or PO box."),
            line2: z.string().max(200).default(""),
            city: z.string().max(200),
            region: z.string().max(200).default("").describe("State, province or county."),
            postal_code: z.string().max(200).default(""),
            country: z.string().max(200).describe("Two-letter ISO 3166 code, such as AU."),
          })
          .optional()
          .describe("The postal address. Replaces the whole address; all parts empty clears it."),
        onboarding_dismissed: z.boolean().optional().describe("True when the person does not want to be asked for the missing fields."),
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
        "Moves domains (with their mailboxes), mailboxes, lead lists, sequences or missions into a brand. " +
        "A brand's mailboxes then send for any of that brand's sequences.",
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
      title: "Take missions out of a brand",
      description: "Makes missions in a brand ones for every brand. Brands share nothing else: to move domains, mailboxes, lead lists or sequences, add them to another brand with add_to_brand.",
      inputSchema: { brand: brandRef, ...records },
      annotations: write,
    },
    ({ brand, ...body }) => call(() => gtm.POST("/v1/workspaces/{ws}/brands/{brand}/remove", { params: { path: brandPath(brand) }, body })),
  );
}
