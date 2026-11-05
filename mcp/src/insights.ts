// Insights: product analytics questions over everything Outflare knows
// about a brand's people, such as a funnel from an ad click to a first
// payment. Agents run one, list the people behind a step or a drop-off,
// and save the ones worth keeping.

import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import type { MissionClient } from "@outflare/sdk";
import { z } from "zod";
import { brandCaller, brandRef } from "./records.js";

type Call = (fn: () => Promise<{ data?: unknown; error?: unknown; response: Response }>) => Promise<CallToolResult>;

const read = { readOnlyHint: true, openWorldHint: false } as const;
const write = { readOnlyHint: false, destructiveHint: false, openWorldHint: false } as const;
const destroy = { readOnlyHint: false, destructiveHint: true, openWorldHint: false } as const;
const insightRef = z.string().uuid().describe("The saved insight's id, from list_insights.");

const filter = z.object({
  property: z
    .string()
    .describe(
      "What to filter on: for visits network, channel, utm_source, utm_campaign, landing_path, device or country; for payments amount or currency; " +
        "for events and pages path, url or referrer, param:<name> for a query parameter, or any property of the event (property:plan or plan). See get_insights_catalog.",
    ),
  op: z.enum(["eq", "neq", "gt", "gte", "lt", "lte", "in", "contains", "not_contains", "exists", "not_exists"]),
  value: z.union([z.string(), z.number(), z.boolean(), z.array(z.string())]).optional().describe("Not for exists and not_exists; a list for in."),
});
const window = z.object({
  value: z.number().int().min(1),
  unit: z.enum(["minute", "hour", "day", "week"]),
});
const step = z.object({
  kind: z
    .enum(["visit", "event", "page", "conversion", "payment"])
    .describe("visit: a website visit (with its source and ad click). event: a product event. page: a page view. conversion: a conversion definition. payment: a successful payment."),
  event: z.string().optional().describe("For an event step: the event's name, such as signed_up."),
  conversion: z.string().optional().describe("For a conversion step: the conversion definition's id or key."),
  filters: z.array(filter).max(10).optional().describe("All must match."),
  first: z.boolean().optional().describe("Only the person's first ever one, such as a first payment."),
  within: window
    .extend({ from: z.number().int().min(1).optional().describe("Counted from this earlier step (1-based). Default: the step before.") })
    .optional()
    .describe("The time limit for this step, such as within 3 days of signing up."),
  label: z.string().max(80).optional(),
});
const funnel = {
  steps: z.array(step).min(2).max(10).describe("In order. Only people who did step 1 are counted in later steps, so each step shows its drop-off."),
  date_range: z
    .object({
      preset: z.enum(["today", "yesterday", "last_7_days", "last_14_days", "last_30_days", "last_90_days", "last_180_days", "last_365_days", "this_month", "last_month", "this_year"]).optional(),
      from: z.string().optional().describe("YYYY-MM-DD"),
      to: z.string().optional().describe("YYYY-MM-DD"),
    })
    .optional()
    .describe("When people did step 1. Default: the last 30 days."),
  window: window.optional().describe("The time limit for the whole funnel, from step 1."),
  breakdown: z.string().optional().describe("Split every step by a property of step 1, such as utm_campaign or network."),
  interval: z.enum(["day", "week", "month"]).optional().describe("Also show conversion over time by when people entered."),
  ad_clicks: z
    .object({ network: z.enum(["google", "meta", "microsoft", "linkedin", "reddit", "x", "tiktok"]).optional(), campaign: z.string().optional() })
    .optional()
    .describe("Show the ad network's own click count for the same dates in front of step 1."),
  exclusions: z
    .array(
      z.object({
        kind: z.enum(["visit", "event", "page", "conversion", "payment"]),
        event: z.string().optional(),
        conversion: z.string().optional(),
        filters: z.array(filter).max(10).optional(),
        from: z.number().int().min(1).describe("A step number, 1-based."),
        to: z.number().int().min(2).describe("A later step number."),
      }),
    )
    .max(5)
    .optional()
    .describe("Drop from the whole funnel everyone who did this between their step from and their step to, such as contacting support between signing up and paying."),
  by: z.enum(["person", "company"]).optional().describe("company counts each company once through the people linked to it; a company reaches a step when any of its people does. Default: person."),
  order: z
    .enum(["strict", "any"])
    .optional()
    .describe("strict (default): people do the steps in this order. any: in any order, so step 1 is whichever they did first and step N means N of the steps done within the window; no exclusions or per-step limits."),
};

const pathPoint = z.object({ kind: z.enum(["page", "event"]), name: z.string().describe("The page's path (/pricing) or the event's name.") });
const paths = {
  date_range: z
    .object({
      preset: z.enum(["today", "yesterday", "last_7_days", "last_14_days", "last_30_days", "last_90_days", "last_180_days", "last_365_days", "this_month", "last_month", "this_year"]).optional(),
      from: z.string().optional().describe("YYYY-MM-DD"),
      to: z.string().optional().describe("YYYY-MM-DD"),
    })
    .optional()
    .describe("When the paths start (or, towards an end, end). Default: the last 30 days."),
  start: pathPoint.optional().describe("Where every path begins. Not with end."),
  end: pathPoint.optional().describe("Where every path ends. Not with start. With neither, paths begin at each person's first page or event in the dates."),
  steps: z.number().int().min(2).max(6).optional().describe("How many steps to show, start or end included. Default 4."),
  kinds: z.array(z.enum(["page", "event"])).optional().describe("What counts as a step. Default: both."),
  window: window.optional().describe("How long a whole path may take from its first step. Default: a day."),
  top: z.number().int().min(1).max(20).optional().describe("Pages or events shown per step before the rest fold into other. Default 8."),
  exclude: z.array(pathPoint).max(20).optional().describe("Pages or events to ignore as if they never happened."),
};

const dateRange = z
  .object({
    preset: z.enum(["today", "yesterday", "last_7_days", "last_14_days", "last_30_days", "last_90_days", "last_180_days", "last_365_days", "this_month", "last_month", "this_year"]).optional(),
    from: z.string().optional().describe("YYYY-MM-DD"),
    to: z.string().optional().describe("YYYY-MM-DD"),
  })
  .describe("A preset moves with today; or fixed from and to dates. Default: the last 30 days.");
const series = z.object({
  kind: z
    .enum(["event", "page", "visit", "conversion", "payment", "ad"])
    .describe("event: a product event. page: page views. visit: website visits. conversion: a conversion definition. payment: successful payments. ad: the ad networks' own clicks, impressions, spend or conversions."),
  event: z.string().optional().describe("For an event series: the event's name, such as signed_up."),
  conversion: z.string().optional().describe("For a conversion series: the conversion definition's id or key."),
  filters: z.array(filter).max(10).optional().describe("All must match."),
  first: z.boolean().optional().describe("Only each person's first ever event or payment."),
  measure: z
    .enum(["count", "people", "sum", "average", "clicks", "impressions", "spend", "conversions"])
    .optional()
    .describe("count (default), people, sum or average; for an ad series clicks (default), impressions, spend or conversions."),
  property: z.string().optional().describe("What sum and average add up: amount for payments (default), or a property of the event."),
  network: z.enum(["google", "meta", "microsoft", "linkedin", "reddit", "x", "tiktok"]).optional().describe("For an ad series: one network, or all when left out."),
  campaign: z.string().optional().describe("For an ad series: only campaigns whose name contains this."),
  label: z.string().max(80).optional(),
});
const trend = {
  series: z.array(series).min(1).max(6).describe("A, B, C … in a formula."),
  date_range: dateRange.optional(),
  interval: z.enum(["day", "week", "month"]).optional().describe("Default: day."),
  breakdown: z.string().optional().describe("Split every series by a property, such as utm_campaign, network or country; the 10 largest groups are shown."),
  formula: z.string().max(200).optional().describe("A sum between series, such as A / B for cost per sign-up when A is ad spend and B is sign-ups: + - * / and brackets."),
  compare: z.boolean().optional().describe("Also run the period before, the same length, as previous."),
  display: z.enum(["line", "bar", "number", "table"]).optional().describe("How the team sees it: line (default), bar, number (totals only) or table."),
};
const retentionStep = step.omit({ within: true });
const retention = {
  start: retentionStep.describe("What puts a person in a cohort: the period of their first one in the dates."),
  return: retentionStep.optional().describe("What counts as coming back; the start again when left out."),
  date_range: dateRange.optional().describe("When the cohorts start. Default: the last 30 days; the last 90 suits weeks."),
  interval: z.enum(["day", "week", "month"]).optional().describe("The cohort and return period. Default: week."),
  periods: z.number().int().min(1).max(24).optional().describe("How many periods after the start to follow. Default: 14 days, 8 weeks or 6 months."),
};
const lifecycle = {
  step: retentionStep.describe("What counts as being active: a visit, a page, an event, a conversion or a payment, with filters."),
  date_range: dateRange.optional().describe("The periods shown. Default: the last 30 days; the last 90 suits weeks."),
  interval: z.enum(["day", "week", "month"]).optional().describe("The period. Default: week."),
};
const dashboardRef = z.string().uuid().describe("The dashboard's id, from list_dashboards.");
const tilePlace = {
  x: z.number().int().min(0).max(11).optional().describe("Column, 0 to 11 on a 12-column grid."),
  y: z.number().int().min(0).optional().describe("Row; new tiles go below everything when left out."),
  w: z.number().int().min(1).max(12).optional().describe("Width in columns; x + w is at most 12. Default 12 for a funnel, 6 otherwise."),
  h: z.number().int().min(1).max(24).optional().describe("Height in rows. Default 6 for a funnel, 4 otherwise."),
};

export function registerInsightTools(server: McpServer, gtm: MissionClient, workspace: () => string, readOnly: boolean, call: Call): void {
  const onBrand = brandCaller(gtm, workspace);

  server.registerTool(
    "get_insights_catalog",
    {
      title: "List what funnels can use",
      description: "The events, conversions, properties and breakdowns a brand's funnels can use, with the event names seen most recently first. Read it before building a funnel.",
      inputSchema: { brand: brandRef },
      annotations: read,
    },
    ({ brand }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/insights/catalog", { params: { path: { brand: b } } }))),
  );

  server.registerTool(
    "run_funnel",
    {
      title: "Run a funnel",
      description:
        "How many people got through each step and where they dropped off, such as Google ad visitors to sign-up to a first payment within 3 days. " +
        "Ad visits, website events, conversions and payments join on the person, including visits from before they signed up. " +
        "Each step has its count, conversion from step 1 and from the step before, the number dropped and the median time it took; with breakdown, the same per group; with interval, by when people entered.",
      inputSchema: { brand: brandRef, ...funnel },
      annotations: read,
    },
    ({ brand, ...spec }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/insights/funnel", { params: { path: { brand: b } }, body: spec as never }))),
  );

  server.registerTool(
    "run_trend",
    {
      title: "Run a trend",
      description:
        "Any number over time: visits, events, people, revenue, the ad networks' clicks and spend, by day, week or month, with a breakdown, a formula between series (A / B) and the period before. " +
        "Each series answers its total and its points; with breakdown, the same per group; with compare, the same for the previous period.",
      inputSchema: { brand: brandRef, ...trend },
      annotations: read,
    },
    ({ brand, ...spec }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/insights/trend", { params: { path: { brand: b } }, body: spec as never }))),
  );

  server.registerTool(
    "run_retention",
    {
      title: "Run a retention question",
      description:
        "Of the people who first did the start in each day, week or month (the cohort), how many came back and did the return in that period and each one after: " +
        "a triangle of cohorts (size and counts per period, null for a period that hasn't begun) and the average share per period across the cohorts that reached it.",
      inputSchema: { brand: brandRef, ...retention },
      annotations: read,
    },
    ({ brand, ...spec }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/insights/retention", { params: { path: { brand: b } }, body: spec as never }))),
  );

  server.registerTool(
    "run_lifecycle",
    {
      title: "Run a lifecycle question",
      description:
        "In each day, week or month, how many of the people who did the step were doing it for the first time ever (new), had done it the period before too (returning) or came back after a gap (resurrected), " +
        "and how many who did it the period before did not this time (dormant). New, returning and resurrected add up to the period's active people; dormant is the people lost.",
      inputSchema: { brand: brandRef, ...lifecycle },
      annotations: read,
    },
    ({ brand, ...spec }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/insights/lifecycle", { params: { path: { brand: b } }, body: spec as never }))),
  );

  server.registerTool(
    "list_funnel_people",
    {
      title: "List the people behind a funnel step",
      description:
        "The people who reached a step of a funnel, or with dropped the ones who reached the step before and not this one, with when they did each step. " +
        "A browser not yet tied to a person has anonymous_id and no record_id.",
      inputSchema: {
        brand: brandRef,
        funnel: z.object(funnel),
        step: z.number().int().min(1).max(10).describe("1-based."),
        dropped: z.boolean().optional().describe("The people who dropped off before this step (not for step 1)."),
        breakdown: z.string().optional().describe("Only this breakdown group's people."),
        limit: z.number().int().min(1).max(500).optional(),
        offset: z.number().int().min(0).optional(),
      },
      annotations: read,
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/insights/funnel/people", { params: { path: { brand: b } }, body: body as never }))),
  );

  server.registerTool(
    "run_funnel_correlation",
    {
      title: "What converters did differently",
      description:
        "Of everyone at step 1 of a funnel, compares the events, pages and person properties (as \"key: value\") of the people who reached a step (the last when unset) with those who didn't, between their step 1 and that step. " +
        "Each item has both groups' counts and rates and an odds ratio: over 1 goes with converting, under 1 with dropping off. Things fewer than 3 people did are skipped. Not for company funnels.",
      inputSchema: {
        brand: brandRef,
        funnel: z.object(funnel),
        step: z.number().int().min(2).max(10).optional().describe("The step reached; the last when left out."),
        limit: z.number().int().min(1).max(50).optional().describe("Items each way. Default 10."),
      },
      annotations: read,
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/insights/funnel/correlation", { params: { path: { brand: b } }, body: body as never }))),
  );

  server.registerTool(
    "run_paths",
    {
      title: "Run a paths question",
      description:
        "Where people went step by step: the pages they viewed and the events they caused, from a start, towards an end, or from each person's first page or event. " +
        "Answers a graph: nodes per step (page, event or other, with count and drop_off) and the links between steps with how many people took each.",
      inputSchema: { brand: brandRef, ...paths },
      annotations: read,
    },
    ({ brand, ...spec }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/insights/paths", { params: { path: { brand: b } }, body: spec as never }))),
  );

  server.registerTool(
    "list_annotations",
    {
      title: "List chart annotations",
      description: "The notes that mark a day on every chart of a brand (a launch, a campaign, an outage), between from and to (dates; the last year when left out).",
      inputSchema: { brand: brandRef, from: z.string().optional().describe("YYYY-MM-DD"), to: z.string().optional().describe("YYYY-MM-DD") },
      annotations: read,
    },
    ({ brand, ...q }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/annotations", { params: { path: { brand: b }, query: q } }))),
  );

  server.registerTool(
    "list_insights",
    {
      title: "List saved insights",
      description: "A brand's saved funnels, trends, retention, paths and lifecycle questions, newest change first, each with its kind and spec. Run one with run_funnel, run_trend, run_retention, run_paths or run_lifecycle and its spec. Page with cursor=next_cursor.",
      inputSchema: { brand: brandRef, kind: z.enum(["funnel", "trend", "retention", "paths", "lifecycle"]).optional(), limit: z.number().int().min(1).max(500).optional(), cursor: z.string().optional() },
      annotations: read,
    },
    ({ brand, ...q }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/insights", { params: { path: { brand: b }, query: q } }))),
  );

  server.registerTool(
    "get_insight",
    { title: "Get a saved insight", description: "One saved funnel, trend or retention question with its spec.", inputSchema: { brand: brandRef, insight: insightRef }, annotations: read },
    ({ brand, insight }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/insights/{insight}", { params: { path: { brand: b, insight } } }))),
  );

  server.registerTool(
    "list_dashboards",
    {
      title: "List dashboards",
      description: "A brand's dashboards, newest change first, each with its settings (the dates every tile shows) and how many tiles it has. Page with cursor=next_cursor.",
      inputSchema: { brand: brandRef, limit: z.number().int().min(1).max(500).optional(), cursor: z.string().optional() },
      annotations: read,
    },
    ({ brand, ...q }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/dashboards", { params: { path: { brand: b }, query: q } }))),
  );

  server.registerTool(
    "get_dashboard",
    {
      title: "Get a dashboard",
      description: "One dashboard with its tiles: each tile's saved insight (with its spec) or note, and where it sits on the 12-column grid. Run a tile with run_funnel or run_trend, with the dashboard's date_range in place of the spec's.",
      inputSchema: { brand: brandRef, dashboard: dashboardRef },
      annotations: read,
    },
    ({ brand, dashboard }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/dashboards/{dashboard}", { params: { path: { brand: b, dashboard } } }))),
  );

  if (readOnly) return;

  server.registerTool(
    "save_funnel_people",
    {
      title: "Save a funnel step's people as a list",
      description:
        "Puts the identified people who reached a step, or with dropped the ones who dropped off before it, into a new static list of the brand, to mail, score or compare. " +
        "Browsers nobody has identified have no record and are skipped (skipped_anonymous). Not for company funnels. Answers the list's id.",
      inputSchema: {
        brand: brandRef,
        funnel: z.object(funnel),
        step: z.number().int().min(1).max(10).describe("1-based."),
        dropped: z.boolean().optional().describe("The people who dropped off before this step (not for step 1)."),
        breakdown: z.string().optional().describe("Only this breakdown group's people."),
        name: z.string().min(1).max(200).describe("The new list's name."),
      },
      annotations: write,
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/insights/funnel/people/save", { params: { path: { brand: b } }, body: body as never }))),
  );

  server.registerTool(
    "create_annotation",
    {
      title: "Annotate a day on every chart",
      description: "Marks a day on every chart of the brand with a note, such as 'new pricing page live', so a jump or a dip has its reason beside it.",
      inputSchema: { brand: brandRef, day: z.string().describe("YYYY-MM-DD"), text: z.string().min(1).max(300) },
      annotations: write,
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/annotations", { params: { path: { brand: b } }, body }))),
  );

  server.registerTool(
    "delete_annotation",
    {
      title: "Delete an annotation",
      description: "Removes a note from every chart of the brand.",
      inputSchema: { brand: brandRef, annotation: z.string().uuid() },
      annotations: destroy,
    },
    ({ brand, annotation }) => call(() => onBrand(brand, (b) => gtm.DELETE("/v1/brands/{brand}/annotations/{annotation}", { params: { path: { brand: b, annotation } } }))),
  );


  server.registerTool(
    "save_insight",
    {
      title: "Save a funnel, a trend or a retention question",
      description: "Saves a funnel, a trend or a retention question for the brand, so the team sees it on its page and can put it on a dashboard. Use a preset date range so it stays current.",
      inputSchema: {
        brand: brandRef,
        kind: z.enum(["funnel", "trend", "retention", "paths", "lifecycle"]).optional().describe("Default: funnel."),
        name: z.string().min(1).max(120),
        description: z.string().max(1000).optional(),
        spec: z.union([z.object(funnel), z.object(trend), z.object(retention)]).describe("A funnel's steps, a trend's series or a retention question's start and return, by kind."),
      },
      annotations: write,
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/insights", { params: { path: { brand: b } }, body: { ...body, kind: body.kind ?? "funnel" } as never }))),
  );

  server.registerTool(
    "update_insight",
    {
      title: "Change a saved insight",
      description: "Renames a saved funnel, trend or retention question, or changes its description or spec; its kind stays. Only the fields given change.",
      inputSchema: {
        brand: brandRef,
        insight: insightRef,
        name: z.string().min(1).max(120).optional(),
        description: z.string().max(1000).optional(),
        spec: z.union([z.object(funnel), z.object(trend), z.object(retention)]).optional(),
      },
      annotations: write,
    },
    ({ brand, insight, ...body }) =>
      call(() => onBrand(brand, (b) => gtm.PATCH("/v1/brands/{brand}/insights/{insight}", { params: { path: { brand: b, insight } }, body: body as never }))),
  );

  server.registerTool(
    "delete_insight",
    { title: "Delete a saved insight", description: "Deletes a saved funnel, trend or retention question for everyone in the brand, and takes it off every dashboard.", inputSchema: { brand: brandRef, insight: insightRef }, annotations: destroy },
    ({ brand, insight }) => call(() => onBrand(brand, (b) => gtm.DELETE("/v1/brands/{brand}/insights/{insight}", { params: { path: { brand: b, insight } } }))),
  );
  server.registerTool(
    "create_dashboard",
    {
      title: "Create a dashboard",
      description: "A new dashboard for the brand. Add saved funnels and trends to it with add_dashboard_tile.",
      inputSchema: {
        brand: brandRef,
        name: z.string().min(1).max(120),
        description: z.string().max(1000).optional(),
        settings: z.object({ date_range: dateRange.optional(), compare: z.boolean().optional().describe("Trends also show the period before.") }).optional().describe("The dates every tile shows; without date_range each tile keeps its own."),
      },
      annotations: write,
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/dashboards", { params: { path: { brand: b } }, body: body as never }))),
  );

  server.registerTool(
    "update_dashboard",
    {
      title: "Change a dashboard",
      description: "Renames a dashboard, changes its description or settings, or moves and resizes tiles with layout. Only the fields given change.",
      inputSchema: {
        brand: brandRef,
        dashboard: dashboardRef,
        name: z.string().min(1).max(120).optional(),
        description: z.string().max(1000).optional(),
        settings: z.object({ date_range: dateRange.optional(), compare: z.boolean().optional() }).optional(),
        layout: z.array(z.object({ id: z.string().uuid().describe("The tile's id."), ...tilePlace })).max(100).optional().describe("New places for tiles."),
      },
      annotations: write,
    },
    ({ brand, dashboard, ...body }) =>
      call(() => onBrand(brand, (b) => gtm.PATCH("/v1/brands/{brand}/dashboards/{dashboard}", { params: { path: { brand: b, dashboard } }, body: body as never }))),
  );

  server.registerTool(
    "delete_dashboard",
    { title: "Delete a dashboard", description: "Deletes a dashboard for everyone in the brand. The insights on it stay saved.", inputSchema: { brand: brandRef, dashboard: dashboardRef }, annotations: destroy },
    ({ brand, dashboard }) => call(() => onBrand(brand, (b) => gtm.DELETE("/v1/brands/{brand}/dashboards/{dashboard}", { params: { path: { brand: b, dashboard } } }))),
  );

  server.registerTool(
    "add_dashboard_tile",
    {
      title: "Add a tile to a dashboard",
      description: "Puts a saved funnel, trend or retention question (insight_id), or a note, on a dashboard, below everything unless placed.",
      inputSchema: {
        brand: brandRef,
        dashboard: dashboardRef,
        insight_id: insightRef.optional(),
        note: z.string().max(4000).optional().describe("Markdown, for a note tile."),
        ...tilePlace,
      },
      annotations: write,
    },
    ({ brand, dashboard, ...body }) =>
      call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/dashboards/{dashboard}/tiles", { params: { path: { brand: b, dashboard } }, body: body as never }))),
  );

  server.registerTool(
    "remove_dashboard_tile",
    {
      title: "Take a tile off a dashboard",
      description: "Removes one tile. The saved insight stays.",
      inputSchema: { brand: brandRef, dashboard: dashboardRef, tile: z.string().uuid().describe("The tile's id, from get_dashboard.") },
      annotations: destroy,
    },
    ({ brand, dashboard, tile }) =>
      call(() => onBrand(brand, (b) => gtm.DELETE("/v1/brands/{brand}/dashboards/{dashboard}/tiles/{tile}", { params: { path: { brand: b, dashboard, tile } } }))),
  );

  server.registerTool(
    "share_dashboard",
    {
      title: "Share a dashboard by link",
      description:
        "Makes the dashboard's share link (replacing one it had): anyone with the link, and the password when one is set, sees the dashboard read-only with every tile live, and nothing else. The token is shown once; give the url to the person.",
      inputSchema: {
        brand: brandRef,
        dashboard: dashboardRef,
        password: z.string().min(8).max(200).optional().describe("Asked for before the dashboard opens. Leave out for a link that opens on its own."),
        expires_in_days: z.number().int().min(1).max(365).optional().describe("When the link stops working. Leave out for one that doesn't expire."),
      },
      annotations: write,
    },
    ({ brand, dashboard, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/dashboards/{dashboard}/share", { params: { path: { brand: b, dashboard } }, body }))),
  );

  server.registerTool(
    "unshare_dashboard",
    {
      title: "Take a dashboard's link back",
      description: "The share link stops working at once.",
      inputSchema: { brand: brandRef, dashboard: dashboardRef },
      annotations: destroy,
    },
    ({ brand, dashboard }) => call(() => onBrand(brand, (b) => gtm.DELETE("/v1/brands/{brand}/dashboards/{dashboard}/share", { params: { path: { brand: b, dashboard } } }))),
  );

  server.registerTool(
    "create_insight_alert",
    {
      title: "Alert on a number",
      description:
        "Watches a saved trend's number (its formula's total, else its first series' total, over the trend's own dates), checked every hour. An alert is raised once when it goes above or below the threshold, or rises_by or falls_by more than the threshold per cent against the period before, and again only after it came back. It reaches the app, the alert.raised webhook, email and Slack by the alerts settings.",
      inputSchema: {
        brand: brandRef,
        insight_id: z.string().uuid().describe("A saved trend's id, from list_insights."),
        condition: z.enum(["above", "below", "rises_by", "falls_by"]),
        threshold: z.number().describe("A value for above and below; a percentage over 0 for rises_by and falls_by."),
      },
      annotations: write,
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/insight-alerts", { params: { path: { brand: b } }, body }))),
  );

  server.registerTool(
    "list_insight_alerts",
    {
      title: "List alerts on numbers",
      description: "The brand's alerts on saved trends, with each one's state (firing while over the line) and last value.",
      inputSchema: { brand: brandRef, insight_id: z.string().uuid().optional().describe("Keep one trend's alerts.") },
      annotations: read,
    },
    ({ brand, insight_id }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/insight-alerts", { params: { path: { brand: b }, query: { insight_id } } }))),
  );

  server.registerTool(
    "delete_insight_alert",
    {
      title: "Delete an alert on a number",
      description: "Removes the alert; the trend stays.",
      inputSchema: { brand: brandRef, alert: z.string().uuid().describe("The alert's id, from list_insight_alerts.") },
      annotations: destroy,
    },
    ({ brand, alert }) => call(() => onBrand(brand, (b) => gtm.DELETE("/v1/brands/{brand}/insight-alerts/{alert}", { params: { path: { brand: b, alert } } }))),
  );
}
