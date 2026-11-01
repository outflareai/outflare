import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import type { MissionClient } from "@outflare/sdk";
import { z } from "zod";
type Call = (
  fn: () => Promise<{ data?: unknown; error?: unknown; response: Response }>,
) => Promise<CallToolResult>;
const read = { readOnlyHint: true, openWorldHint: false } as const;
const write = { readOnlyHint: false, openWorldHint: false } as const;
const address = { brand: z.string().uuid(), site: z.string().uuid() };
const rule = z.object({
  operator: z.enum(["prefix", "contains", "regex", "equals"]),
  value: z.string().min(1).max(500),
});
export function registerSearchInsightTools(
  server: McpServer,
  gtm: MissionClient,
  readOnly: boolean,
  call: Call,
) {
  server.registerTool(
    "get_search_insights",
    {
      title: "Analyse search opportunities and performance",
      description:
        "Stored SEO reports: opportunities estimates click gains using CTR benchmarks; ctr shows the position curve; decay compares complete calendar weeks/months; segments reads country/device rows; groups aggregates saved topic and content rules; forecast models future clicks; completeness measures omitted/capped query traffic; changes adds sustained breaks, Google updates and deploys; attribution joins GA4 landing pages with estimated query allocation; ai-referrals measures assistant referrers; migrations checks recovery and crawl redirect evidence; vitals reads CrUX history. Estimates are not observed conversions or guaranteed growth. Dates default to settled GSC days. Brand and site must match.",
      inputSchema: {
        ...address,
        report: z
          .enum([
            "opportunities",
            "ctr",
            "decay",
            "segments",
            "groups",
            "forecast",
            "completeness",
            "changes",
            "attribution",
            "ai-referrals",
            "migrations",
            "vitals",
          ])
          .optional(),
        from: z.string().optional(),
        to: z.string().optional(),
        country: z.string().length(3).optional(),
        device: z.enum(["MOBILE", "DESKTOP", "TABLET"]).optional(),
        group_id: z.string().uuid().optional(),
        bucket: z.enum(["week", "month"]).optional(),
        threshold: z.number().min(0).max(1).optional(),
        weeks: z.number().int().min(1).max(52).optional(),
        growth: z.number().min(-0.9).max(5).optional(),
        conversion_rate: z.number().min(0).max(1).optional(),
        currency: z.string().length(3).optional(),
        order_value: z.number().min(0).max(1000000).optional(),
        migration_id: z.string().uuid().optional(),
        integration_id: z.string().uuid().optional(),
      },
      annotations: read,
    },
    ({ brand, site, ...query }) =>
      call(() =>
        gtm.GET("/v1/brands/{brand}/seo-sites/{site}/insights", {
          params: { path: { brand, site }, query },
        }),
      ),
  );
  server.registerTool(
    "list_search_groups",
    {
      title: "List saved search groups",
      description:
        "Topic rules match queries; content rules match URLs or paths. Rules are ORed; groups can overlap, so their totals must not be added together.",
      inputSchema: address,
      annotations: read,
    },
    (path) =>
      call(() =>
        gtm.GET("/v1/brands/{brand}/seo-sites/{site}/search-groups", {
          params: { path },
        }),
      ),
  );
  server.registerTool(
    "list_search_migrations",
    {
      title: "List saved site migrations",
      description:
        "Saved go-live dates, target sites and old-to-new URL mappings. Both sites belong to the same brand.",
      inputSchema: address,
      annotations: read,
    },
    (path) =>
      call(() =>
        gtm.GET("/v1/brands/{brand}/seo-sites/{site}/search-migrations", {
          params: { path },
        }),
      ),
  );
  server.registerTool(
    "get_search_workspace",
    {
      title: "Read the search work queue and run history",
      description:
        "Saved findings with tags, statuses and linked fixes, saved report definitions, collection schedules and recent activity. Snapshot results are fetched separately.",
      inputSchema: address,
      annotations: read,
    },
    (path) =>
      call(() =>
        gtm.GET("/v1/brands/{brand}/seo-sites/{site}/insights/workspace", {
          params: { path },
        }),
      ),
  );
  server.registerTool(
    "get_search_report_run",
    {
      title: "Read a saved report snapshot",
      description:
        "Read one immutable snapshot, including its original filters, data window, results or failure.",
      inputSchema: { ...address, item: z.string().uuid() },
      annotations: read,
    },
    (path) =>
      call(() =>
        gtm.GET(
          "/v1/brands/{brand}/seo-sites/{site}/insights/report-runs/{item}",
          { params: { path } },
        ),
      ),
  );
  if (readOnly) return;
  const filters = z.object({
    report: z.string(),
    from: z.string().optional(),
    to: z.string().optional(),
    country: z.string().optional(),
    device: z.string().optional(),
    group_id: z.string().uuid().optional(),
    migration_id: z.string().uuid().optional(),
    integration_id: z.string().uuid().optional(),
    bucket: z.string().optional(),
    threshold: z.number().optional(),
    weeks: z.number().optional(),
    growth: z.number().optional(),
    conversion_rate: z.number().optional(),
    order_value: z.number().optional(),
    currency: z.string().optional(),
  });
  server.registerTool(
    "save_search_work",
    {
      title: "Save or update a search finding",
      description:
        "Save a finding from a report with tags, notes and work status. New findings are verified against server-side evidence; submitting invented evidence is not supported.",
      inputSchema: {
        ...address,
        report: z.string(),
        key: z.string(),
        tags: z.array(z.string().max(40)).max(20).optional(),
        status: z.enum(["open", "in_progress", "done", "dismissed"]).optional(),
        note: z.string().max(2000).optional(),
        filters: filters.optional(),
      },
      annotations: write,
    },
    ({ brand, site, ...body }) =>
      call(() =>
        gtm.POST("/v1/brands/{brand}/seo-sites/{site}/insights/items", {
          params: { path: { brand, site } },
          body,
        }),
      ),
  );
  server.registerTool(
    "create_search_work_fix",
    {
      title: "Create an implementation fix for search work",
      description:
        "Create or return the shared Fixes ticket for a saved finding. Includes the original evidence and recommended next step; does not deploy anything.",
      inputSchema: { ...address, item: z.string().uuid() },
      annotations: write,
    },
    (path) =>
      call(() =>
        gtm.POST(
          "/v1/brands/{brand}/seo-sites/{site}/insights/items/{item}/fix",
          { params: { path } },
        ),
      ),
  );
  server.registerTool(
    "save_search_report",
    {
      title: "Save or edit a report definition",
      description:
        "Save filters and tags for reuse. Explicit dates remain fixed; omitted dates use the latest settled period when run. Editing a definition leaves snapshots unchanged.",
      inputSchema: {
        ...address,
        id: z.string().uuid().optional(),
        name: z.string().min(1).max(120),
        tags: z.array(z.string().max(40)).max(20).optional(),
        filters,
      },
      annotations: write,
    },
    ({ brand, site, ...body }) =>
      call(() =>
        gtm.POST("/v1/brands/{brand}/seo-sites/{site}/insights/reports", {
          params: { path: { brand, site } },
          body,
        }),
      ),
  );
  server.registerTool(
    "run_search_report",
    {
      title: "Generate an immutable report snapshot",
      description:
        "Run a saved report against stored data and retain its data period, results or failure. This does not collect new provider data.",
      inputSchema: { ...address, item: z.string().uuid() },
      annotations: write,
    },
    (path) =>
      call(() =>
        gtm.POST(
          "/v1/brands/{brand}/seo-sites/{site}/insights/reports/{item}/run",
          { params: { path } },
        ),
      ),
  );
  server.registerTool(
    "save_search_schedule",
    {
      title: "Set up, edit or pause search data collection",
      description:
        "Explicit daily or weekly collection in a named timezone. Without a connection collects official Google updates; with the brand's own CrUX connection collects vitals. Pausing or editing invalidates old queued work. Provider failures do not stop later collections.",
      inputSchema: {
        ...address,
        id: z.string().uuid().optional(),
        name: z.string().min(1).max(120),
        integration_id: z.string().uuid().optional(),
        page: z.string().url().optional(),
        device: z.enum(["PHONE", "DESKTOP", "TABLET"]).optional(),
        frequency: z.enum(["off", "daily", "weekly"]),
        weekday: z.number().int().min(0).max(6),
        hour: z.number().int().min(0).max(23),
        timezone: z.string(),
      },
      annotations: write,
    },
    ({ brand, site, ...body }) =>
      call(() =>
        gtm.POST("/v1/brands/{brand}/seo-sites/{site}/insights/schedules", {
          params: { path: { brand, site } },
          body,
        }),
      ),
  );
  server.registerTool(
    "delete_search_report",
    {
      title: "Delete a report and its snapshots",
      description:
        "Permanently delete a saved report definition and its snapshot history. Confirm the exact report with the user before deleting.",
      inputSchema: { ...address, item: z.string().uuid() },
      annotations: { ...write, destructiveHint: true },
    },
    (path) =>
      call(() =>
        gtm.DELETE(
          "/v1/brands/{brand}/seo-sites/{site}/insights/reports/{item}",
          { params: { path } },
        ),
      ),
  );
  server.registerTool(
    "delete_search_schedule",
    {
      title: "Delete a collection schedule",
      description:
        "Delete one schedule and invalidate queued runs from it. Existing collection history remains.",
      inputSchema: { ...address, item: z.string().uuid() },
      annotations: { ...write, destructiveHint: true },
    },
    (path) =>
      call(() =>
        gtm.DELETE(
          "/v1/brands/{brand}/seo-sites/{site}/insights/schedules/{item}",
          { params: { path } },
        ),
      ),
  );

  server.registerTool(
    "save_search_group",
    {
      title: "Save a topic or content group",
      description:
        "Create a named group, replace the same named group, or update by id. Go RE2 regex; path rules begin with /. Topic matching ignores case.",
      inputSchema: {
        ...address,
        id: z.string().uuid().optional(),
        name: z.string().min(1).max(120),
        kind: z.enum(["content", "topic"]),
        rules: z.array(rule).min(1).max(100),
      },
      annotations: write,
    },
    ({ brand, site, ...body }) =>
      call(() =>
        gtm.POST("/v1/brands/{brand}/seo-sites/{site}/search-groups", {
          params: { path: { brand, site } },
          body,
        }),
      ),
  );
  server.registerTool(
    "delete_search_group",
    {
      title: "Delete a search group",
      description: "Delete a saved group, preserving source performance data.",
      inputSchema: { ...address, item: z.string().uuid() },
      annotations: write,
    },
    (path) =>
      call(() =>
        gtm.DELETE("/v1/brands/{brand}/seo-sites/{site}/search-groups/{item}", {
          params: { path },
        }),
      ),
  );
  server.registerTool(
    "save_search_migration",
    {
      title: "Save a site migration",
      description:
        "Save a go-live date and source-to-target mappings for two sites of this brand. The report uses stored Search Console data and crawl evidence; it does not fetch arbitrary mapped URLs.",
      inputSchema: {
        ...address,
        id: z.string().uuid().optional(),
        name: z.string().min(1).max(120),
        target_site_id: z.string().uuid(),
        go_live: z.string(),
        mappings: z
          .array(
            z.object({ source: z.string().url(), target: z.string().url() }),
          )
          .max(1000),
      },
      annotations: write,
    },
    ({ brand, site, ...body }) =>
      call(() =>
        gtm.POST("/v1/brands/{brand}/seo-sites/{site}/search-migrations", {
          params: { path: { brand, site } },
          body,
        }),
      ),
  );
  server.registerTool(
    "delete_search_migration",
    {
      title: "Delete a site migration",
      description:
        "Delete the migration configuration, preserving crawls and search history.",
      inputSchema: { ...address, item: z.string().uuid() },
      annotations: write,
    },
    (path) =>
      call(() =>
        gtm.DELETE(
          "/v1/brands/{brand}/seo-sites/{site}/search-migrations/{item}",
          { params: { path } },
        ),
      ),
  );
  server.registerTool(
    "refresh_search_insights",
    {
      title: "Collect Core Web Vitals history",
      description:
        "Queue the official Google update feed, optionally also CrUX history with the brand's own google_crux connection. One-time collection; use save_search_schedule for recurring collection. Field data may be unavailable for low-traffic URLs.",
      inputSchema: {
        ...address,
        integration_id: z.string().uuid().optional(),
        page: z.string().url().optional(),
        device: z.enum(["PHONE", "DESKTOP", "TABLET"]).optional(),
      },
      annotations: { ...write, openWorldHint: true },
    },
    ({ brand, site, ...body }) =>
      call(() =>
        gtm.POST("/v1/brands/{brand}/seo-sites/{site}/insights/refresh", {
          params: { path: { brand, site } },
          body,
        }),
      ),
  );
}
