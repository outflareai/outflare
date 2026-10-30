// Feature flags: switches and splits the website script and servers read
// to decide what a visitor sees. Agents set up a test, turn it on and off,
// and check what a given visitor gets.

import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import type { MissionClient } from "@outflare/sdk";
import { z } from "zod";

type Call = (fn: () => Promise<{ data?: unknown; error?: unknown; response: Response }>) => Promise<CallToolResult>;

const read = { readOnlyHint: true, openWorldHint: false } as const;
const write = { readOnlyHint: false, destructiveHint: false, openWorldHint: false } as const;
const flagRef = z.string().min(1).describe("The flag's id or key.");
const key = z.string().regex(/^[a-z0-9][a-z0-9_-]{0,63}$/);

const variant = z.object({
  key: key.describe("The version's name, such as control or faster."),
  weight: z.number().int().min(0).max(100).describe("Its share is its weight over the total of all weights."),
  payload: z.unknown().optional().describe("Any JSON the page reads with this version, such as {\"headline\": \"Ship faster\"}."),
});
const rule = z.object({
  attribute: z.string().describe("host, path, url, referrer, device (desktop, mobile, tablet), language, signed_in, query.<name> (a parameter of the page's address) or property.<name> (sent to evaluate_flags)."),
  op: z.enum(["is", "is_not", "contains", "not_contains", "starts_with", "ends_with", "exists", "not_exists"]),
  values: z.array(z.string().max(500)).max(50).optional().describe("Any of these; none for exists and not_exists."),
});
const fields = {
  name: z.string().max(100).optional(),
  description: z.string().max(1000).optional(),
  brand_id: z.string().nullable().optional().describe("Only this brand's site gets it (an id from list_brands); null for every brand."),
  status: z.enum(["on", "off"]).optional().describe("A new flag is off unless this says on."),
  variants: z.array(variant).min(1).max(10).optional().describe('Leave out for a plain switch (one version, "on"). Two or more split visitors between them.'),
  rollout: z.number().int().min(0).max(100).optional().describe("Percent of visitors who pass the rules that get it (default 100)."),
  rules: z.array(rule).max(20).optional().describe("Conditions every one of which must hold."),
  on_site: z.boolean().optional().describe("Send it to the website script (default true). false keeps a flag only servers read out of the page."),
  exclusion_group: z.string().max(64).optional().describe('Join an exclusion group (such as homepage), so no visitor is in this flag\'s A/B test and another flag\'s in the same group; "" leaves it. Fixed while its test runs.'),
  group_share: z.number().int().min(1).max(100).optional().describe("Percent of the group's visitors this flag takes (default 50); it is off for the rest."),
};

export function registerFlagTools(
  server: McpServer,
  gtm: MissionClient,
  workspace: () => string,
  readOnly: boolean,
  call: Call,
  withBrand: <T extends { brand?: string }>(q: T) => T,
): void {
  const path = () => ({ ws: workspace() });

  server.registerTool(
    "list_flags",
    {
      title: "List feature flags",
      description: "Switches and splits (A/B tests) the website script and servers read: on or off, versions and weights, who gets them.",
      inputSchema: {
        brand: z.string().optional().describe("Only this brand's flags and the shared ones: a handle or id from list_brands."),
        status: z.enum(["on", "off", "archived", "all"]).optional().describe("Default: every flag but the archived ones."),
      },
      annotations: read,
    },
    (q) => call(() => gtm.GET("/v1/workspaces/{ws}/flags", { params: { path: path(), query: withBrand(q) } })),
  );

  server.registerTool(
    "get_flag",
    { title: "Get a feature flag", description: "One flag, by id or key.", inputSchema: { flag: flagRef }, annotations: read },
    ({ flag }) => call(() => gtm.GET("/v1/workspaces/{ws}/flags/{flag}", { params: { path: { ...path(), flag } } })),
  );

  server.registerTool(
    "evaluate_flags",
    {
      title: "Check which version a visitor gets",
      description: "The version a visitor gets of every flag that is on, and why (match, rules, rollout). Pass the anonymous_id the website script gave them so the answer matches the site. Nothing is stored.",
      inputSchema: {
        anonymous_id: z.string().max(200).optional(),
        user_id: z.string().max(200).optional(),
        url: z.string().optional().describe("The page they are on, for host, path and query rules."),
        user_agent: z.string().optional(),
        language: z.string().optional(),
        properties: z.record(z.string(), z.unknown()).optional(),
        keys: z.array(z.string()).max(200).optional().describe("Only these flags."),
      },
      annotations: read,
    },
    (body) => call(() => gtm.POST("/v1/workspaces/{ws}/flags/evaluate", { params: { path: path() }, body })),
  );

  if (readOnly) return;

  server.registerTool(
    "create_flag",
    {
      title: "Add a feature flag",
      description: "A switch, or a split between versions to test which works better. It starts off unless status is on. The site reads it with outflare.flag(key) when its script tag has data-flags.",
      inputSchema: { key: key.describe("What code asks for, such as pricing-headline. It can't change later."), ...fields },
      annotations: write,
    },
    (body) => call(() => gtm.POST("/v1/workspaces/{ws}/flags", { params: { path: path() }, body: body as never })),
  );

  server.registerTool(
    "update_flag",
    {
      title: "Change a feature flag",
      description: "Turn it on or off, or change its versions, rollout, rules or brand. Visitors get the change within 10 seconds; changing the weights of a running split moves some visitors to another version.",
      inputSchema: { flag: flagRef, ...fields },
      annotations: write,
    },
    ({ flag, ...body }) => call(() => gtm.PATCH("/v1/workspaces/{ws}/flags/{flag}", { params: { path: { ...path(), flag } }, body: body as never })),
  );

  server.registerTool(
    "archive_flag",
    {
      title: "Archive a feature flag",
      description: "Turn a flag off for good and free its key. Code that still asks for it gets false.",
      inputSchema: { flag: flagRef },
      annotations: { readOnlyHint: false, destructiveHint: true, openWorldHint: false },
    },
    ({ flag }) => call(() => gtm.DELETE("/v1/workspaces/{ws}/flags/{flag}", { params: { path: { ...path(), flag } } })),
  );
}
