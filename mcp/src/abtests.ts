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
  kind: z.enum(["event", "page", "deal"]).describe("event: a product event. page: a view of a page. deal: won deals (US dollars) of the person the visitor's browser belongs to."),
  name: z.string().max(100).optional().describe("For an event goal: the product event's catalog name, such as signup_started."),
  path: z.string().max(500).optional().describe("For a page goal: a path such as /thanks, or /docs/* for every page under it."),
  metric: z
    .enum(["conversion", "count", "value"])
    .optional()
    .describe("conversion (default): whether each visitor reached it. count: how many times. value: the total of property (or a deal's value) per visitor, such as revenue per visitor."),
  property: z.string().max(64).optional().describe("For a value goal on an event: the property with the number, such as revenue."),
  window_hours: z.number().int().min(0).max(8784).optional().describe("Count only what happens within this many hours of first seeing a version."),
  cap: z.number().min(0).optional().describe("For count and value goals: the most one visitor can add, so a few huge orders can't swing the result."),
});
const fields = {
  name: z.string().max(200).optional().describe("Default: the flag's name."),
  hypothesis: z.string().max(2000).optional().describe("What the team expects and why."),
  control: z.string().optional().describe('The flag version the others are compared with. Default: "control" if the flag has one, else its first.'),
  goals: z.array(goal).min(1).max(5).optional().describe("What counts as success; the first is the one the test is judged on. Fixed once it starts."),
  guardrails: z
    .array(goal.extend({ better: z.enum(["higher", "lower"]).optional().describe("Which way is good: higher (default) or lower, such as for errors or refunds.") }))
    .max(5)
    .optional()
    .describe("Metrics a winner mustn't hurt, such as errors or refunds; flagged when a version clearly makes one worse, never deciding the winner. Fixed once it starts."),
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
        "Visitors and conversions (or the mean count or value per visitor) per version for each goal, each version's chance to beat the control, its lift with a 95% range, an always-valid p_value (below 0.05 it is safe to stop, however often you looked; corrected when several versions compete), a sample-ratio warning, and the winner if one is clear. Goals after the first are exploratory and never decide. Live while running; as it stood when it ended once done.",
      inputSchema: { test: testRef },
      annotations: read,
    },
    ({ test }) => call(() => gtm.GET("/v1/workspaces/{ws}/ab-tests/{test}/results", { params: { path: { ...path(), test } } })),
  );

  server.registerTool(
    "get_ab_test_breakdown",
    {
      title: "Break an A/B test's results down",
      description:
        "The first goal's results within groups of visitors, by the visit they first saw their version in: device, source (utm_source, else the referring site, else direct), country, or visitor (new or returning). The largest 8 groups, the rest as other; every comparison is corrected for every version in every group. For learning where a change works, not for picking a winner.",
      inputSchema: { test: testRef, by: z.enum(["device", "source", "country", "visitor"]).describe("What to group visitors by.") },
      annotations: read,
    },
    ({ test, by }) => call(() => gtm.GET("/v1/workspaces/{ws}/ab-tests/{test}/breakdown", { params: { path: { ...path(), test }, query: { by } } })),
  );

  server.registerTool(
    "plan_ab_test",
    {
      title: "Plan an A/B test",
      description:
        "How long a test of a flag is likely to take before it starts: from the last 28 days of traffic (on one page if given) and the goal's rate. Gives visitors per version and weeks for true lifts of +5% to +50%, the smallest lift findable in 4 and 8 weeks, and a warning when it won't finish in 8 weeks. Nothing is stored.",
      inputSchema: {
        flag: z.string().min(1).describe("The flag's id or key."),
        goal: goal.describe("The goal the test would be judged on."),
        page: z.string().max(500).optional().describe("The page the test changes, such as /pricing, or /docs/* for a section."),
        min_effect: z.number().min(0.01).max(1).optional().describe("Default 0.25 (+25%)."),
      },
      annotations: read,
    },
    (body) => call(() => gtm.POST("/v1/workspaces/{ws}/ab-tests/plan", { params: { path: path() }, body: body as never })),
  );

  server.registerTool(
    "get_ab_test_ai_run",
    {
      title: "Get an A/B test AI run",
      description: "A run from suggest_ab_tests, write_ab_test_versions or summarize_ab_test. Poll until status is ready (output holds the answer) or failed (error says why).",
      inputSchema: { run: z.string().uuid() },
      annotations: read,
    },
    ({ run }) => call(() => gtm.GET("/v1/workspaces/{ws}/ab-test-ai-runs/{run}", { params: { path: { ...path(), run } } })),
  );

  if (readOnly) return;

  server.registerTool(
    "suggest_ab_tests",
    {
      title: "Suggest A/B tests with AI",
      description:
        "The workspace's own AI model proposes tests from the site's busiest pages, the goals visitors reach, the brand's context and past tests, each with the engine's plan (weeks, findable lift, warning). Returns a queued run: read it with get_ab_test_ai_run. Nothing is created.",
      inputSchema: {
        brand_id: z.string().nullable().optional().describe("Use this brand's context (an id from list_brands)."),
        page: z.string().max(500).optional().describe("Only pages under this path."),
        goal: goal.optional().describe("Judge every suggestion on this goal."),
        count: z.number().int().min(1).max(5).optional(),
        notes: z.string().max(2000).optional().describe("What to focus on."),
      },
      annotations: { ...write, openWorldHint: true },
    },
    (body) => call(() => gtm.POST("/v1/workspaces/{ws}/ab-tests/suggestions", { params: { path: path() }, body: body as never })),
  );

  server.registerTool(
    "write_ab_test_versions",
    {
      title: "Write test versions with AI",
      description:
        "The workspace's own AI model writes new versions of a flag's content from a prompt, in the brand's voice. Returns a queued run (get_ab_test_ai_run); the versions are proposals: add the ones the person approves to the flag with update_flag.",
      inputSchema: {
        flag: z.string().min(1).describe("The flag's id or key."),
        prompt: z.string().min(1).max(2000).describe('What to change and for whom, such as "a shorter, more urgent headline for agencies".'),
        count: z.number().int().min(1).max(4).optional(),
        fields: z.array(z.string().regex(/^[a-z][a-z0-9_]{0,39}$/)).max(8).optional().describe("Content fields to write, such as headline and cta. Default: those the flag's versions carry."),
      },
      annotations: { ...write, openWorldHint: true },
    },
    (body) => call(() => gtm.POST("/v1/workspaces/{ws}/ab-tests/versions", { params: { path: path() }, body: body as never })),
  );

  server.registerTool(
    "summarize_ab_test",
    {
      title: "Summarise a test with AI",
      description:
        "The workspace's own AI model puts a running or finished test's results into plain English with next steps. The recommendation (ship, keep_control, fix_split, extend, stop, wait) comes from the engine. Returns a queued run: read it with get_ab_test_ai_run.",
      inputSchema: { test: testRef },
      annotations: { ...write, openWorldHint: true },
    },
    ({ test }) => call(() => gtm.POST("/v1/workspaces/{ws}/ab-tests/{test}/summary", { params: { path: { ...path(), test } } })),
  );

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
      description: "Its name or hypothesis at any time; its control, goals, guardrails and min_effect only while it is a draft.",
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

  server.registerTool(
    "freeze_ab_test",
    {
      title: "Freeze or unfreeze an A/B test",
      description:
        "frozen true stops new visitors joining a running test while those already in keep counting, so it can finish on the visitors it has (such as before a launch that would change the traffic); false lets them join again.",
      inputSchema: { test: testRef, frozen: z.boolean() },
      annotations: write,
    },
    ({ test, frozen }) => call(() => gtm.POST("/v1/workspaces/{ws}/ab-tests/{test}/freeze", { params: { path: { ...path(), test } }, body: { frozen } })),
  );

  server.registerTool(
    "duplicate_ab_test",
    {
      title: "Duplicate an A/B test",
      description: "A new draft with the same flag, control, goals, guardrails and hypothesis, to run a test again or try a variation.",
      inputSchema: { test: testRef },
      annotations: write,
    },
    ({ test }) => call(() => gtm.POST("/v1/workspaces/{ws}/ab-tests/{test}/duplicate", { params: { path: { ...path(), test } } })),
  );

  server.registerTool(
    "get_ab_test_checklist",
    {
      title: "Check an A/B test before it starts",
      description:
        "What would stop a test measuring anything useful: the flag, another running test of it, whether pages showed its versions lately, whether visitors reach the goal, how long it will take, and guardrails. ready is false while any check fails.",
      inputSchema: { test: testRef },
      annotations: read,
    },
    ({ test }) => call(() => gtm.GET("/v1/workspaces/{ws}/ab-tests/{test}/checklist", { params: { path: { ...path(), test } } })),
  );

  server.registerTool(
    "ship_ab_test",
    {
      title: "Ship a version of an A/B test",
      description:
        "Show the chosen version to everyone: its flag gives it all the traffic at full rollout, and a running test ends with it as the decision. Confirm with the person first: visitors see the change at once.",
      inputSchema: { test: testRef, variant: z.string().min(1).describe("The version to ship, usually the winner from get_ab_test_results.") },
      annotations: { readOnlyHint: false, destructiveHint: true, openWorldHint: false },
    },
    ({ test, variant }) => call(() => gtm.POST("/v1/workspaces/{ws}/ab-tests/{test}/ship", { params: { path: { ...path(), test } }, body: { variant } })),
  );
}
