// A/B tests: which version of a feature flag does best at a goal. Agents
// set one up on a flag, start it, read how it is going, and end it with
// the version the team chose.

import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import type { MissionClient } from "@outflare/sdk";
import { z } from "zod";
import { brandCaller, brandRef } from "./records.js";

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
  factors: z
    .array(z.object({ flag: z.string().min(1).describe("Another flag's id or key."), control: z.string().optional().describe('Its control version. Default: "control" if it has one, else its first.') }))
    .max(2)
    .optional()
    .describe("Makes it a multivariate test: up to two other flags (outside this flag's exclusion group), so every combination of versions is measured (get_ab_test_factorial). Fixed once it starts."),
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
  // Flags and A/B tests belong to one brand: calls go to its routes.
  const onBrand = brandCaller(gtm, workspace);

  server.registerTool(
    "list_ab_tests",
    {
      title: "List A/B tests",
      description: "A/B tests of feature flags: draft, running or done, their control, goals and the version chosen when they ended.",
      inputSchema: {
        brand: brandRef,
        status: z.enum(["draft", "running", "done"]).optional(),
        flag: z.string().optional().describe("Only the tests of this flag, by id or key."),
      },
      annotations: read,
    },
    ({ brand, ...q }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/ab-tests", { params: { path: { brand: b }, query: q } }))),
  );

  server.registerTool(
    "get_ab_test",
    { title: "Get an A/B test", description: "One A/B test.", inputSchema: { brand: brandRef, test: testRef }, annotations: read },
    ({ brand, test }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/ab-tests/{test}", { params: { path: { brand: b, test } } }))),
  );

  server.registerTool(
    "get_ab_test_results",
    {
      title: "Get an A/B test's results",
      description:
        "Visitors and conversions (or the mean count or value per visitor) per version for each goal, each version's chance to beat the control, its lift with a 95% range, an always-valid p_value (below 0.05 it is safe to stop, however often you looked; corrected when several versions compete), a sample-ratio warning, and the winner if one is clear. Goals after the first are exploratory and never decide. Live while running; as it stood when it ended once done.",
      inputSchema: { brand: brandRef, test: testRef },
      annotations: read,
    },
    ({ brand, test }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/ab-tests/{test}/results", { params: { path: { brand: b, test } } }))),
  );

  server.registerTool(
    "get_ab_test_breakdown",
    {
      title: "Break an A/B test's results down",
      description:
        "The first goal's results within groups of visitors, by the visit they first saw their version in: device, source (utm_source, else the referring site, else direct), country, or visitor (new or returning). The largest 8 groups, the rest as other; every comparison is corrected for every version in every group. For learning where a change works, not for picking a winner.",
      inputSchema: { brand: brandRef, test: testRef, by: z.enum(["device", "source", "country", "visitor"]).describe("What to group visitors by.") },
      annotations: read,
    },
    ({ brand, test, by }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/ab-tests/{test}/breakdown", { params: { path: { brand: b, test }, query: { by } } }))),
  );

  server.registerTool(
    "get_ab_test_factorial",
    {
      title: "Read a multivariate A/B test",
      description:
        "A multivariate test's first goal for every combination of versions (cell), each against the all-controls cell (corrected for every cell), the best combination, each flag's own effect pooled over the others, and whether the other flags change this flag's effect (interacts).",
      inputSchema: { brand: brandRef, test: testRef },
      annotations: read,
    },
    ({ brand, test }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/ab-tests/{test}/factorial", { params: { path: { brand: b, test } } }))),
  );

  server.registerTool(
    "get_ab_test_interactions",
    {
      title: "Check an A/B test for interactions",
      description:
        "For each other test of another flag that ran at the same time (outside this flag's exclusion group): how many visitors saw both, and whether each version's effect on the first goal differs by the other test's version (interacts, at p < 0.01). An interaction means one change alters the other's effect; read the effects before shipping either.",
      inputSchema: { brand: brandRef, test: testRef },
      annotations: read,
    },
    ({ brand, test }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/ab-tests/{test}/interactions", { params: { path: { brand: b, test } } }))),
  );

  server.registerTool(
    "plan_ab_test",
    {
      title: "Plan an A/B test",
      description:
        "How long a test of a flag is likely to take before it starts: from the last 28 days of traffic (on one page if given) and the goal's rate. Gives visitors per version and weeks for true lifts of +5% to +50%, the smallest lift findable in 4 and 8 weeks, and a warning when it won't finish in 8 weeks. Nothing is stored.",
      inputSchema: {
        brand: brandRef,
        flag: z.string().min(1).describe("The flag's id or key."),
        goal: goal.describe("The goal the test would be judged on."),
        page: z.string().max(500).optional().describe("The page the test changes, such as /pricing, or /docs/* for a section."),
        min_effect: z.number().min(0.01).max(1).optional().describe("Default 0.25 (+25%)."),
      },
      annotations: read,
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/ab-tests/plan", { params: { path: { brand: b } }, body: body as never }))),
  );

  server.registerTool(
    "get_ab_test_ai_run",
    {
      title: "Get an A/B test AI run",
      description: "A run from suggest_ab_tests, write_ab_test_versions or summarize_ab_test. Poll until status is ready (output holds the answer) or failed (error says why).",
      inputSchema: { brand: brandRef, run: z.string().uuid() },
      annotations: read,
    },
    ({ brand, run }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/ab-test-ai-runs/{run}", { params: { path: { brand: b, run } } }))),
  );

  if (readOnly) return;

  server.registerTool(
    "suggest_ab_tests",
    {
      title: "Suggest A/B tests with AI",
      description:
        "The workspace's own AI model proposes tests from the site's busiest pages, the goals visitors reach, the brand's context and past tests, each with the engine's plan (weeks, findable lift, warning). Returns a queued run: read it with get_ab_test_ai_run. Nothing is created.",
      inputSchema: {
        brand: brandRef,
        brand_id: z.string().nullable().optional().describe("Use this brand's context (an id from list_brands)."),
        page: z.string().max(500).optional().describe("Only pages under this path."),
        goal: goal.optional().describe("Judge every suggestion on this goal."),
        count: z.number().int().min(1).max(5).optional(),
        notes: z.string().max(2000).optional().describe("What to focus on."),
      },
      annotations: { ...write, openWorldHint: true },
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/ab-tests/suggestions", { params: { path: { brand: b } }, body: body as never }))),
  );

  server.registerTool(
    "write_ab_test_versions",
    {
      title: "Write test versions with AI",
      description:
        "The workspace's own AI model writes new versions of a flag's content from a prompt, in the brand's voice. Returns a queued run (get_ab_test_ai_run); the versions are proposals: add the ones the person approves to the flag with update_flag.",
      inputSchema: {
        brand: brandRef,
        flag: z.string().min(1).describe("The flag's id or key."),
        prompt: z.string().min(1).max(2000).describe('What to change and for whom, such as "a shorter, more urgent headline for agencies".'),
        count: z.number().int().min(1).max(4).optional(),
        fields: z.array(z.string().regex(/^[a-z][a-z0-9_]{0,39}$/)).max(8).optional().describe("Content fields to write, such as headline and cta. Default: those the flag's versions carry."),
      },
      annotations: { ...write, openWorldHint: true },
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/ab-tests/versions", { params: { path: { brand: b } }, body: body as never }))),
  );

  server.registerTool(
    "summarize_ab_test",
    {
      title: "Summarise a test with AI",
      description:
        "The workspace's own AI model puts a running or finished test's results into plain English with next steps. The recommendation (ship, keep_control, fix_split, extend, stop, wait) comes from the engine. Returns a queued run: read it with get_ab_test_ai_run.",
      inputSchema: { brand: brandRef, test: testRef },
      annotations: { ...write, openWorldHint: true },
    },
    ({ brand, test }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/ab-tests/{test}/summary", { params: { path: { brand: b, test } } }))),
  );

  server.registerTool(
    "create_ab_test",
    {
      title: "Add an A/B test",
      description:
        "Test a feature flag with two or more versions (create_flag) against goals, or send page instead of flag to test a page with no code: " +
        "Outflare makes a flag shown only there, split between the control and page.versions new versions (b, c, …), whose changes are made " +
        "in the visual editor (a person opens it from the test's page) or set with set_ab_test_version. It starts as a draft; start_ab_test starts " +
        "measuring. Visitors join when a page shows them the flag's version (the website script sends Experiment Viewed).",
      inputSchema: {
        brand: brandRef,
        flag: z.string().min(1).optional().describe("The flag's id or key. Leave out when sending page."),
        page: z.object({
          url: z.string().url().max(2000).describe("The page, such as https://northpeak.example/pricing; end with * for every page under it."),
          versions: z.number().int().min(1).max(4).optional().describe("New versions besides the control. Default 1."),
          redirects: z.array(z.string().max(2000)).min(1).max(4).optional().describe("A split URL test: one page per new version that its visitors are sent to, such as /pricing-b (only the brand's own domains)."),
        }).optional(),
        ...fields,
        goals: fields.goals.unwrap(),
      },
      annotations: write,
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/ab-tests", { params: { path: { brand: b } }, body: body as never }))),
  );

  server.registerTool(
    "get_ab_test_version",
    {
      title: "Read a test version's changes",
      description: "A version's visual changes, CSS and redirect, and whether visitors can see it now (live).",
      inputSchema: { brand: brandRef, test: testRef, version: z.string().min(1).describe("One of the test's versions, such as b.") },
      annotations: { readOnlyHint: true },
    },
    ({ brand, test, version }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/ab-tests/{test}/versions/{version}", { params: { path: { brand: b, test, version } } }))),
  );

  server.registerTool(
    "set_ab_test_version",
    {
      title: "Set a test version's changes",
      description:
        "Build a version of a no-code test, as the visual editor saves it. changes replace the version's visual changes: each has a CSS selector " +
        "and one action: text, rich (**bold**, *italic*, [link](/page)), hide, href, src and alt, style ({\"color\": \"#0f766e\"}; looks only), " +
        "move (before, after, start or end of target) or insert ({where, kind: text|button|link|image, text, href, src, alt}), optionally device " +
        "mobile or desktop. css adds the version's own CSS (no url() or @import); redirect sends its visitors to another page on the brand's domains " +
        "(a split URL test). No scripts or HTML. The control can't change. While the test is a draft this runs on your grants; once visitors can see " +
        "the version it waits for a person. Check each version with get_ab_test_previews before asking a person to start the test.",
      inputSchema: {
        brand: brandRef,
        test: testRef,
        version: z.string().min(1).describe("One of the test's versions besides the control, such as b."),
        changes: z.array(z.record(z.string(), z.unknown())).max(50).describe("The version's visual changes."),
        css: z.string().max(8000).optional().describe("The version's own CSS; \"\" removes it."),
        redirect: z.string().max(2000).optional().describe("A page to send the version's visitors to, such as /pricing-b; \"\" removes it."),
      },
      annotations: write,
    },
    ({ brand, test, version, ...body }) => call(() => onBrand(brand, (b) => gtm.PUT("/v1/brands/{brand}/ab-tests/{test}/versions/{version}", { params: { path: { brand: b, test, version } }, body: body as never }))),
  );

  server.registerTool(
    "update_ab_test",
    {
      title: "Change an A/B test",
      description: "Its name or hypothesis at any time; its control, goals, guardrails and min_effect only while it is a draft.",
      inputSchema: { brand: brandRef, test: testRef, ...fields },
      annotations: write,
    },
    ({ brand, test, ...body }) => call(() => onBrand(brand, (b) => gtm.PATCH("/v1/brands/{brand}/ab-tests/{test}", { params: { path: { brand: b, test } }, body: body as never }))),
  );

  server.registerTool(
    "start_ab_test",
    {
      title: "Start an A/B test",
      description: "Start measuring from now. Turns the flag on if it is off. A flag runs one test at a time. From an agent this always waits for a person to approve it.",
      inputSchema: { brand: brandRef, test: testRef },
      annotations: write,
    },
    ({ brand, test }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/ab-tests/{test}/start", { params: { path: { brand: b, test } } }))),
  );

  server.registerTool(
    "end_ab_test",
    {
      title: "End an A/B test",
      description: 'Stop measuring and keep the results, with the version the team chose (decision; "" for none). The flag is left as it is: ship the winner with update_flag.',
      inputSchema: { brand: brandRef, test: testRef, decision: z.string().describe('The version to keep, or "" for none.') },
      annotations: { readOnlyHint: false, destructiveHint: true, openWorldHint: false },
    },
    ({ brand, test, decision }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/ab-tests/{test}/end", { params: { path: { brand: b, test } }, body: { decision } }))),
  );

  server.registerTool(
    "freeze_ab_test",
    {
      title: "Freeze or unfreeze an A/B test",
      description:
        "frozen true stops new visitors joining a running test while those already in keep counting, so it can finish on the visitors it has (such as before a launch that would change the traffic); false lets them join again.",
      inputSchema: { brand: brandRef, test: testRef, frozen: z.boolean() },
      annotations: write,
    },
    ({ brand, test, frozen }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/ab-tests/{test}/freeze", { params: { path: { brand: b, test } }, body: { frozen } }))),
  );

  server.registerTool(
    "duplicate_ab_test",
    {
      title: "Duplicate an A/B test",
      description: "A new draft with the same flag, control, goals, guardrails and hypothesis, to run a test again or try a variation.",
      inputSchema: { brand: brandRef, test: testRef },
      annotations: write,
    },
    ({ brand, test }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/ab-tests/{test}/duplicate", { params: { path: { brand: b, test } } }))),
  );

  server.registerTool(
    "get_ab_test_checklist",
    {
      title: "Check an A/B test before it starts",
      description:
        "What would stop a test measuring anything useful: the flag, another running test of it, whether pages showed its versions lately, whether visitors reach the goal, how long it will take, and guardrails. ready is false while any check fails.",
      inputSchema: { brand: brandRef, test: testRef },
      annotations: read,
    },
    ({ brand, test }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/ab-tests/{test}/checklist", { params: { path: { brand: b, test } } }))),
  );

  server.registerTool(
    "get_ab_test_previews",
    {
      title: "Preview an A/B test's versions",
      description:
        "Links that show each version of a test to whoever opens them, on the pages that showed its flag most lately (or on page), without counting them; and whether a person has checked each version. Give the links to the person to look at: an agent can't judge how a page renders.",
      inputSchema: { brand: brandRef, test: testRef, page: z.string().url().optional().describe("A page of the site to give links for.") },
      annotations: read,
    },
    ({ brand, test, page }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/ab-tests/{test}/previews", { params: { path: { brand: b, test }, query: { page } } }))),
  );

  server.registerTool(
    "check_ab_test_version",
    {
      title: "Record a check of an A/B test version",
      description:
        "Record what the person saw when they previewed a version: ok when it renders right, broken when it doesn't (a broken version stops the test passing its checklist). Only record what the person told you.",
      inputSchema: {
        brand: brandRef,
        test: testRef,
        variant: z.string().min(1),
        status: z.enum(["ok", "broken"]),
        note: z.string().max(1000).optional().describe("What was wrong, or anything worth knowing."),
      },
      annotations: write,
    },
    ({ brand, test, variant, status, note }) =>
      call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/ab-tests/{test}/checks", { params: { path: { brand: b, test } }, body: { variant, status, note } }))),
  );

  server.registerTool(
    "ship_ab_test",
    {
      title: "Ship a version of an A/B test",
      description:
        "Show the chosen version to everyone: its flag gives it all the traffic at full rollout, and a running test ends with it as the decision. Confirm with the person first: visitors see the change at once. From an agent this always waits for a person to approve it.",
      inputSchema: { brand: brandRef, test: testRef, variant: z.string().min(1).describe("The version to ship, usually the winner from get_ab_test_results.") },
      annotations: { readOnlyHint: false, destructiveHint: true, openWorldHint: false },
    },
    ({ brand, test, variant }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/ab-tests/{test}/ship", { params: { path: { brand: b, test } }, body: { variant } }))),
  );

  server.registerTool(
    "list_ab_test_learnings",
    {
      title: "List what past A/B tests taught",
      description:
        "Finished A/B tests, newest first: the hypothesis, the goal, how each came out in a line, the version kept, and the learning the workspace's AI wrote when it ended. q finds tests whose name, hypothesis, flag or learning mention it. Read this before suggesting a new test, so it doesn't repeat one.",
      inputSchema: { brand: brandRef, q: z.string().max(200).optional().describe("Words to look for, such as headline or pricing.") },
      annotations: read,
    },
    ({ brand, q }) => call(() => onBrand(brand, (b) => gtm.GET("/v1/brands/{brand}/ab-tests/learnings", { params: { path: { brand: b }, query: q ? { q } : {} } }))),
  );

  server.registerTool(
    "ask_ab_tests",
    {
      title: "Ask about past A/B tests",
      description:
        "The workspace's own AI model answers a question over the most recent 40 finished tests, citing the tests it drew on. Returns a queued run: read it with get_ab_test_ai_run.",
      inputSchema: {
        brand: brandRef,
        question: z.string().min(1).max(2000).describe('Such as "What have we learned about headlines on the pricing page?"'),
        brand_id: z.string().uuid().optional().describe("Only this brand's tests and those of shared flags."),
      },
      annotations: { readOnlyHint: false, destructiveHint: false, openWorldHint: true },
    },
    ({ brand, ...body }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/ab-tests/ask", { params: { path: { brand: b } }, body }))),
  );

  server.registerTool(
    "write_ab_test_learning",
    {
      title: "Write a finished A/B test's learning",
      description:
        "The workspace's own AI model writes (or rewrites) what a finished test taught, kept on the test for the library. It is written on its own when a test ends, if the workspace has an AI model. Returns a queued run.",
      inputSchema: { brand: brandRef, test: testRef },
      annotations: { readOnlyHint: false, destructiveHint: false, openWorldHint: true },
    },
    ({ brand, test }) => call(() => onBrand(brand, (b) => gtm.POST("/v1/brands/{brand}/ab-tests/{test}/learning", { params: { path: { brand: b, test } } }))),
  );
}
