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
      description: "Donor profiles for paid link orders: domain, niches, PBN flag, intermediary, theme, site age, the intermediary's metrics (referring domains, backlinks, keywords, outgoing links and more), current published prices in minor units (the donor price only), and direct_contact: whether the site is recommended for writing to directly, not only through the intermediary, with the rules it misses. A recommendation only; use find_link_order_donor_emails and send_link_order_donor_to_sequence to write. Page with cursor=next_cursor.",
      inputSchema: {
        brand,
        cursor: z.string().optional().describe("next_cursor from the previous page."),
        limit: z.number().int().min(1).max(200).optional(),
        q: z.string().optional().describe("Search the domain, intermediary and theme."),
        pbn: z.boolean().optional(),
        niche: z.string().optional(),
        intermediary: z.string().optional().describe("Only sites bought through this intermediary (exact name, ignoring case)."),
        direct_contact: z.boolean().optional().describe("true: only sites recommended for direct contact (not a PBN, DR or traffic stored, and a stored, unsuppressed address at the site's own domain); false: the rest."),
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

  server.registerTool(
    "list_link_order_candidates",
    {
      title: "List link-order candidates",
      description:
        "Referring domains from the brand's already-saved competitor link gap (stored snapshots and live backlinks only). This does not call a paid SEO provider. Traffic is always null. Status is not_in_catalog, in_catalog, or ordered. Page with cursor=next_cursor.",
      inputSchema: {
        brand,
        site_id: z.string().uuid().optional().describe("Limit the gap to one SEO site."),
        cursor: z.string().optional().describe("next_cursor from the previous page."),
        limit: z.number().int().min(1).max(500).optional(),
      },
      annotations: read,
    },
    ({ brand, ...query }) =>
      call(() =>
        gtm.GET("/v1/brands/{brand}/link-orders/candidates", { params: { path: { brand }, query } }),
      ),
  );

  server.registerTool(
    "get_link_order_equivalents",
    {
      title: "List equivalent donors for a link order",
      description:
        "Catalog donors in the same domain-rating band and niche as the order's donor, not archived, preferring non-PBN when the source is not PBN. The current donor is first when it still qualifies. Amounts are the donor price only.",
      inputSchema: { brand, id: z.string().uuid().describe("The order id.") },
      annotations: read,
    },
    ({ brand, id }) =>
      call(() =>
        gtm.GET("/v1/brands/{brand}/link-orders/{id}/equivalents", { params: { path: { brand, id } } }),
      ),
  );

  server.registerTool(
    "list_link_order_targets",
    {
      title: "List saved link-order targets",
      description:
        "Pages saved as link targets for paid link orders: URL, kind (article or product), title, and whether the page is ticked (selected). A ticked page is offered when an order is requested.",
      inputSchema: {
        brand,
        kind: z.enum(["article", "product"]).optional(),
        selected: z.enum(["true", "false"]).optional().describe("Omit to list every saved page."),
      },
      annotations: read,
    },
    ({ brand, ...query }) =>
      call(() =>
        gtm.GET("/v1/brands/{brand}/link-orders/targets", { params: { path: { brand }, query } }),
      ),
  );

  server.registerTool(
    "suggest_link_order_targets",
    {
      title: "Suggest pages to build links to",
      description:
        "Brand article and product pages ranked from already-stored data only: keyword gaps against tracked competitors (where a competitor ranks top 20 and the brand is behind), and the pages competitor backlinks point to, mapped to the brand page that ranks for the same keywords. " +
        "Each suggestion lists its reasons and anchor options (primary from SE Ranking when stored; second anchor by type: brand, url, or keyword). This does not call a paid SEO provider.",
      inputSchema: {
        brand,
        kind: z.enum(["article", "product"]).optional(),
        limit: z.number().int().min(1).max(200).optional(),
      },
      annotations: read,
    },
    ({ brand, ...query }) =>
      call(() =>
        gtm.GET("/v1/brands/{brand}/link-orders/targets/suggestions", { params: { path: { brand }, query } }),
      ),
  );

  server.registerTool(
    "get_link_order_target_anchors",
    {
      title: "Anchor options for a page",
      description:
        "Anchor options for one brand page from stored data: primary anchors (SE Ranking keywords first, then the page's saved target keyword, then other stored keywords), and second-anchor options by type (brand, url, keyword). This does not call a paid SEO provider.",
      inputSchema: { brand, url: z.string().min(1).describe("The brand page URL.") },
      annotations: read,
    },
    ({ brand, url }) =>
      call(() =>
        gtm.GET("/v1/brands/{brand}/link-orders/targets/anchors", { params: { path: { brand }, query: { page: url } } }),
      ),
  );

  server.registerTool(
    "find_link_order_donor_emails",
    {
      title: "Find a catalog donor's stored emails",
      description: "The email addresses already stored for a catalog donor's site: people at its company record, contacts a link-building check found on its pages, the brand's Outreach leads at the domain, and addresses in the notes (such as the intermediary's). Reads stored data only: no web lookup and no paid provider, so an empty list means nothing is stored yet. Each address says whether it is at the site's domain, suppressed, or already contacted.",
      inputSchema: { brand, id: z.string().uuid().describe("The catalog row (donor profile) id.") },
      annotations: read,
    },
    ({ brand, id }) =>
      call(() =>
        gtm.GET("/v1/brands/{brand}/link-orders/catalog/items/{id}/contacts", { params: { path: { brand, id } } }),
      ),
  );

  if (readOnly) {
    return;
  }

  server.registerTool(
    "send_link_order_donor_to_sequence",
    {
      title: "Send a catalog donor to an Outreach sequence",
      description: "Make an email address a lead of the brand (the donor's domain as its company) and add it to one of the brand's Outreach sequences, with the same rules as adding leads there: suppressed, excluded and already-enrolled addresses are skipped, and so is one active in another sequence. The sequence sends on its own schedule from its own mailboxes; a draft or paused one sends nothing until it is started. Needs coverage.manage and sequences.manage.",
      inputSchema: {
        brand,
        id: z.string().uuid().describe("The catalog row (donor profile) id."),
        sequence_id: z.string().uuid().describe("One of the brand's sequences."),
        email: z.string().email().max(320),
        name: z.string().max(200).optional().describe("The person's name, when known."),
      },
      annotations: write,
    },
    ({ brand, id, ...body }) =>
      call(() =>
        gtm.POST("/v1/brands/{brand}/link-orders/catalog/items/{id}/outreach", { params: { path: { brand, id } }, body }),
      ),
  );

  server.registerTool(
    "create_link_order",
    {
      title: "Request a link order",
      description: "Open a requested paid-link order against a catalog donor. The guarantee window defaults to the catalog row, then to 30 days. This does not approve or pay; those stay with the brand's designated approver.",
      inputSchema: {
        brand,
        donor_profile_id: z.string().uuid(),
        target_url: z.string().min(1).optional().describe("The brand page. Optional when target_id is set."),
        target_id: z.string().uuid().optional().describe("A ticked saved target from list_link_order_targets."),
        target_kind: z.enum(["article", "product"]).optional(),
        target_choice: z.enum(["manual", "suggested"]).optional(),
        anchor_primary: z.string().max(500).optional(),
        anchor_primary_source: z.enum(["manual", "se_ranking", "page_target", "seo_keywords"]).optional(),
        anchor_secondary: z.string().max(500).optional(),
        anchor_secondary_type: z.enum(["brand", "url", "keyword"]).optional().describe("Required with anchor_secondary."),
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
      description: "Import a CSV string only; a Google Sheet is not imported. The first row is the header and must include a domain column. Headers are matched ignoring case and punctuation (\"Traffic / Organic\" is traffic), and an intermediary's export is read in full: intermediary, theme, site age, and metrics such as referring domains, backlinks, keywords and outgoing links. A repeated domain updates the existing profile. A bad row is listed in errors and does not abort the rest of the file; ignored_columns lists headers that were not read. Up to 32 MB per call; send a larger file as several calls that each repeat the header. price is in dollars or euros, amount_minor in cents; amounts are the donor price only.",
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

  server.registerTool(
    "add_link_order_candidate_to_catalog",
    {
      title: "Add a saved-gap candidate to the catalog",
      description:
        "Find-or-create a donor profile for a domain that is already in the saved link gap, with source=competitor_gap. A domain that is not in the saved gap fails. This does not call a paid SEO provider.",
      inputSchema: {
        brand,
        domain: z.string().min(1).describe("The referring domain from the saved link gap."),
        site_id: z.string().uuid().optional().describe("Limit the gap check to one SEO site."),
      },
      annotations: write,
    },
    ({ brand, domain, site_id }) =>
      call(() =>
        gtm.POST("/v1/brands/{brand}/link-orders/candidates/{domain}/add-to-catalog", {
          params: { path: { brand, domain }, query: site_id ? { site_id } : undefined },
        }),
      ),
  );
  server.registerTool(
    "get_link_roi",
    {
      title: "Read cost per published link against stored page changes",
      description:
        "For each published, paid or verified link order: the donor price, the referring domain and its stored rating, and already-saved Search Console, ranking and referring-domain figures on the target page 30, 60 and 90 days around publication. " +
        "This does not call a paid SEO provider or claim that the link caused a change. Amounts stay per currency. " +
        "Pages rolls the same orders up by target URL. Orders without a publication date are counted in totals only.",
      inputSchema: { brand },
      annotations: read,
    },
    ({ brand }) => call(() => gtm.GET("/v1/brands/{brand}/link-roi", { params: { path: { brand } } })),
  );

}
