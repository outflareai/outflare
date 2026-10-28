// A/B tests: which version of a feature flag does best at a goal. Agents
// set one up on a flag, start it, read how it is going, and end it with
// the version the team chose.

import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import type { MissionClient } from "@outflare/sdk";
import { z } from "zod";

type Call = (fn: () => Promise<{ data?: unknown; error?: unknown; response: Response }>) => Promise<CallToolResult>;

const read = { readOnlyHint: true, openWorldHint: false } as const;
const write = { readOnlyHint: false, destructiveHint: false, openWorldHint: false } as const;
const testRef = z.string().uuid().describe("The test's id, from list_ab_tests.");

const goal = z.object({
  kind: z.enum(["event", "page"]),
  name: z.string().max(100).optional().describe("For an event goal: the product event's catalog name, such as signup_started."),
  path: z.string().max(500).optional().describe("For a page goal: a path such as /thanks, or /docs/* for every page under it."),
});
const fields = {
  name: z.string().max(200).optional().describe("Default: the flag's name."),
  hypothesis: z.string().max(2000).optional().describe("What the team expects and why."),
  control: z.string().optional().describe('The flag version the others are compared with. Default: "control" if the flag has one, else its first.'),
  goals: z.array(goal).min(1).max(5).optional().describe("What counts as success; the first is the one the test is judged on. Fixed once it starts."),
  min_effect: z
    .number()
    .min(0.01)
    .max(1)
    .optional()
    .describe("The smallest relative change worth detecting: 0.1 (+10%) for a busy site, 0.25 (+25%, the default), 0.5 (+50%) for a bold change. Smaller finds small changes but needs more visitors. Fixed once it starts."),
};

export function registerABTestTools(
  server: McpServer,
  gtm: MissionClient,
  workspace: () => string,
  readOnly: boolean,
  call: Call,
  withBrand: <T extends { brand?: string }>(q: T) => T,
): void {
  const path = () => ({ ws: workspace() });

  server.registerTool(
    "list_ab_tests",
    {
      title: "List A/B tests",
      description: "A/B tests of feature flags: draft, running or done, their control, goals and the version chosen when they ended.",
      inputSchema: {
        brand: z.string().optional().describe("Only this brand's tests and those of shared flags: a handle or id from list_brands."),
        status: z.enum(["draft", "running", "done"]).optional(),
        flag: z.string().optional().describe("Only the tests of this flag, by id or key."),
      },
      annotations: read,
    },
    (q) => call(() => gtm.GET("/v1/workspaces/{ws}/ab-tests", { params: { path: path(), query: withBrand(q) } })),
  );

  server.registerTool(
    "get_ab_test",
    { title: "Get an A/B test", description: "One A/B test.", inputSchema: { test: testRef }, annotations: read },
    ({ test }) => call(() => gtm.GET("/v1/workspaces/{ws}/ab-tests/{test}", { params: { path: { ...path(), test } } })),
  );

  server.registerTool(
    "get_ab_test_results",
    {
      title: "Get an A/B test's results",
      description:
        "Visitors and conversions per version for each goal, each version's chance to beat the control, its lift with a 95% range, an always-valid p_value (below 0.05 it is safe to stop, however often you looked), a sample-ratio warning, and the winner if one is clear. Live while running; as it stood when it ended once done.",
      inputSchema: { test: testRef },
      annotations: read,
    },
    ({ test }) => call(() => gtm.GET("/v1/workspaces/{ws}/ab-tests/{test}/results", { params: { path: { ...path(), test } } })),
  );

  if (readOnly) return;

  server.registerTool(
    "create_ab_test",
    {
      title: "Add an A/B test",
      description:
        "Test a feature flag with two or more versions (create_flag) against goals. It starts as a draft; start_ab_test starts measuring. Visitors join when a page shows them the flag's version (the website script sends Experiment Viewed).",
      inputSchema: { flag: z.string().min(1).describe("The flag's id or key."), ...fields, goals: fields.goals.unwrap() },
      annotations: write,
    },
    (body) => call(() => gtm.POST("/v1/workspaces/{ws}/ab-tests", { params: { path: path() }, body: body as never })),
  );

  server.registerTool(
    "update_ab_test",
    {
      title: "Change an A/B test",
      description: "Its name or hypothesis at any time; its control, goals and min_effect only while it is a draft.",
      inputSchema: { test: testRef, ...fields },
      annotations: write,
    },
    ({ test, ...body }) => call(() => gtm.PATCH("/v1/workspaces/{ws}/ab-tests/{test}", { params: { path: { ...path(), test } }, body: body as never })),
  );

  server.registerTool(
    "start_ab_test",
    {
      title: "Start an A/B test",
      description: "Start measuring from now. Turns the flag on if it is off. A flag runs one test at a time.",
      inputSchema: { test: testRef },
      annotations: write,
    },
    ({ test }) => call(() => gtm.POST("/v1/workspaces/{ws}/ab-tests/{test}/start", { params: { path: { ...path(), test } } })),
  );

  server.registerTool(
    "end_ab_test",
    {
      title: "End an A/B test",
      description: 'Stop measuring and keep the results, with the version the team chose (decision; "" for none). The flag is left as it is: ship the winner with update_flag.',
      inputSchema: { test: testRef, decision: z.string().describe('The version to keep, or "" for none.') },
      annotations: { readOnlyHint: false, destructiveHint: true, openWorldHint: false },
    },
    ({ test, decision }) => call(() => gtm.POST("/v1/workspaces/{ws}/ab-tests/{test}/end", { params: { path: { ...path(), test } }, body: { decision } })),
  );
}
