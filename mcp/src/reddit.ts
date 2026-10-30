// Reddit campaigns through Engain: an agent gets a brand and a budget,
// reads the guide, and places comments, replies, posts, thread groups and
// upvotes as experiments, then reads what stayed live and what it cost.
// Outflare checks the brand's limits before anything spends credits.

import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import type { MissionClient } from "@outflare/sdk";
import { z } from "zod";

type Call = (fn: () => Promise<{ data?: unknown; error?: unknown; response: Response }>) => Promise<CallToolResult>;

const read = { readOnlyHint: true, openWorldHint: false } as const;
const write = { readOnlyHint: false, destructiveHint: false, openWorldHint: true } as const;
const brand = z.string().min(1).describe("The brand's handle or id, from list_brands.");
const actionRef = z.string().uuid().describe("The action's id, from list_reddit_actions.");
const experimentRef = z.string().uuid().describe("The experiment's id, from list_reddit_experiments.");

// REDDIT_GUIDE is what an agent reads before running a campaign: what the
// supplier can do, what it costs, the rules, and how to experiment.
export const REDDIT_GUIDE = `# Reddit campaigns through Outflare

You run Reddit marketing for one brand. Outflare places content through
Engain, which posts from its own pool of aged Reddit accounts. You never call
Engain yourself: every action goes through Outflare, which checks the brand's
budget, logs the action and follows it on Reddit until it is removed.

## The goal
Brand mentions and links that stay live, at the lowest cost per mention,
within the budget. A removed mention counts for nothing.

## What you can do
| Action | Tool | Cost (Engain credits) | Notes |
|---|---|---|---|
| Comment on a thread | place_reddit_action kind comment | about 5 | target_url: the thread |
| Reply to any comment | place_reddit_action kind reply | about 5 | target_url: the comment's permalink, ours included |
| New post | place_reddit_action kind post | about 10 | target_url: https://www.reddit.com/r/<name>, plus title |
| Thread group | place_reddit_action kind thread | about 10 + 5 a reply | a post, comment or reply plus replies from up to 10 other pool accounts (children: account 1-10, delay_minutes) |
| Upvotes | place_reddit_action kind upvotes | about 0.1 each | on a thread or comment; parent_action_id upvotes one of ours once live; 1-10 a day |
| Upvote plan with a task | upvotes_goal + upvotes_per_day on a comment, reply or post | about 0.1 each | upvotes_start: immediately, after_3_days, after_5_days, after_7_days |
| Preflight drafts | preflight_reddit_drafts | free, 5 a minute | verdict, rules at risk, removal risk, reads-as-AI risk, edits |
| How strict a subreddit is | get_reddit_subreddit | free | Engain's removal rates across all its customers, and ours |
| Threads worth answering | find_reddit_threads | free | type new (fresh) or seo (ranking on Google) |
| Results | get_reddit_report, list_reddit_actions, get_reddit_action | free | mentions, links, cost, lifespan |
| Budget | get_reddit_budget | free | left this month and today |

Not available: downvotes, choosing a specific account, editing or deleting
after posting.

## Rules
- No tracking parameters on links (no UTM): they are refused. Posts must read
  as organic.
- Often mention the brand with no link at all; that counts as a mention.
- Answer the thread's question first. Name the brand the way a real user
  would, beside other options, not as an ad.
- Preflight every draft; rewrite what fails or reads as AI.
- Never reuse text across actions.
- Every action belongs to an experiment. Create the experiment first.
- Describe each action honestly: strategy, link_type, brand_mentioned, tone.
  The report compares them.
- Over a limit, the action is kept as a draft and you get
  reddit_budget_reached or reddit_limit_reached. Don't try to get round a
  limit; tell the person.

## How to work
1. get_reddit_budget and get_reddit_report (group_by strategy, then
   subreddit). Read closed experiments' conclusions: don't re-test what is
   settled.
2. Choose subreddits with get_reddit_subreddit; avoid those removing over half.
3. create_reddit_experiment: one hypothesis, one variable changed. Aim for
   10 to 20 actions per arm before concluding.
4. Draft, preflight, place. Spread actions over days and subreddits.
5. Come back after 1, 7 and 30 days (get_reddit_report with experiment_id).
   close_reddit_experiment with a conclusion: what you tested, the numbers,
   what to do next.

## Experiment ideas
- Placement: top-level comment vs a reply under the current top comment.
- Self-reply: a comment, then another pool account replies naming the brand
  (a thread group).
- Post then comment: a post asking the question, a comment answering it.
- Link vs no link vs brand name only.
- Link target: the brand's site vs a review site vs a docs page.
- Thread age: under a day old vs threads already ranking on Google.
- Upvotes: none vs 10 vs 30, at once vs after 3 days.
- Tone: direct recommendation vs comparison vs personal story.
- Subreddit strictness: the same approach in a strict and a lenient one.
- Timing: US mornings vs evenings.

## Report back
End each session with: actions placed, credits spent, mentions and links live
now, median lifespan, and the one thing you would change next.
`;

export function registerRedditTools(
  server: McpServer,
  gtm: MissionClient,
  workspace: () => string,
  readOnly: boolean,
  call: Call,
): void {
  const path = () => ({ ws: workspace() });
  const text = (t: string): CallToolResult => ({ content: [{ type: "text", text: t }] });

  server.registerResource(
    "reddit-guide",
    "outflare://reddit-guide",
    { title: "Reddit campaigns guide", description: "What Engain can do through Outflare, what it costs, the rules, and how to run Reddit experiments.", mimeType: "text/markdown" },
    async (uri) => ({ contents: [{ uri: uri.href, mimeType: "text/markdown", text: REDDIT_GUIDE }] }),
  );

  server.registerPrompt(
    "run_reddit_campaign",
    {
      title: "Run a Reddit campaign",
      description: "Brand mentions on Reddit through Engain for one brand, as experiments, within a budget.",
      argsSchema: {
        brand: z.string().describe("The brand's handle, such as geonode."),
        budget: z.string().optional().describe("What to spend at most, in Engain credits, such as 300."),
        period: z.string().optional().describe("Over what time, such as this week."),
        goal: z.string().optional().describe("What to aim for, such as mentions in threads that rank on Google for 'residential proxies'."),
      },
    },
    ({ brand, budget, period, goal }) => ({
      messages: [
        {
          role: "user",
          content: {
            type: "text",
            text:
              `Run Reddit marketing for the brand ${brand}${budget ? `, spending at most ${budget} Engain credits` : ""}${period ? ` ${period}` : ""}.\n` +
              (goal ? `Goal: ${goal}.\n` : "") +
              "1. Call get_reddit_guide and follow it.\n" +
              "2. Read get_reddit_budget and get_reddit_report, and the conclusions of closed experiments.\n" +
              "3. Plan one or two experiments, tell me the plan in a few lines, then carry it out.\n" +
              "4. End with the report the guide asks for.",
          },
        },
      ],
    }),
  );

  server.registerTool(
    "get_reddit_guide",
    { title: "Read the Reddit campaigns guide", description: "Read this first: what can be done through Engain, costs, rules and experiment ideas.", inputSchema: {}, annotations: read },
    async () => text(REDDIT_GUIDE),
  );

  server.registerTool(
    "get_reddit_budget",
    {
      title: "Get a brand's Reddit budget",
      description: "Credits spent this month and today (UTC) against the brand's limits, what is left, and Engain's balance across every brand.",
      inputSchema: { brand },
      annotations: read,
    },
    ({ brand }) => call(() => gtm.GET("/v1/workspaces/{ws}/reddit/budget", { params: { path: path(), query: { brand } } })),
  );

  server.registerTool(
    "list_reddit_settings",
    {
      title: "List Reddit campaign settings",
      description: "Each brand's Engain connection and project, its limits (credits a month and a day, actions per subreddit a day, upvotes per item, whether a person approves each action) and its spend.",
      inputSchema: { brand: z.string().optional().describe("Only this brand.") },
      annotations: read,
    },
    (q) => call(() => gtm.GET("/v1/workspaces/{ws}/reddit/settings", { params: { path: path(), query: q } })),
  );

  server.registerTool(
    "list_reddit_experiments",
    {
      title: "List Reddit experiments",
      description: "Experiments, newest first, with hypothesis, strategy, status, conclusion and how many actions each has.",
      inputSchema: {
        brand: z.string().optional(),
        status: z.enum(["open", "closed"]).optional(),
        limit: z.number().int().min(1).max(200).optional(),
      },
      annotations: read,
    },
    (q) => call(() => gtm.GET("/v1/workspaces/{ws}/reddit/experiments", { params: { path: path(), query: q } })),
  );

  server.registerTool(
    "get_reddit_experiment",
    { title: "Get a Reddit experiment", description: "One experiment.", inputSchema: { experiment: experimentRef }, annotations: read },
    ({ experiment }) => call(() => gtm.GET("/v1/workspaces/{ws}/reddit/experiments/{id}", { params: { path: { ...path(), id: experiment } } })),
  );

  server.registerTool(
    "list_reddit_actions",
    {
      title: "List Reddit actions",
      description: "Every comment, reply, post, thread group and upvote order, newest first: status (live, removed, gone...), Reddit link, score, days live, cost. next_before pages back.",
      inputSchema: {
        brand: z.string().optional(),
        status: z.string().optional().describe("One or more, comma-separated: draft, approved, sending, submitted, live, frozen, removed, hidden, gone, failed, cancelled, done."),
        kind: z.enum(["comment", "reply", "post", "thread", "upvotes"]).optional(),
        subreddit: z.string().optional(),
        experiment_id: z.string().uuid().optional(),
        before: z.string().optional().describe("next_before from the previous page."),
        limit: z.number().int().min(1).max(200).optional(),
      },
      annotations: read,
    },
    (q) => call(() => gtm.GET("/v1/workspaces/{ws}/reddit/actions", { params: { path: path(), query: q } })),
  );

  server.registerTool(
    "get_reddit_action",
    {
      title: "Get a Reddit action",
      description: "One action with its timeline (sent, live, removed, restored), every check of Reddit, and Engain's last answer.",
      inputSchema: { action: actionRef },
      annotations: read,
    },
    ({ action }) => call(() => gtm.GET("/v1/workspaces/{ws}/reddit/actions/{id}", { params: { path: { ...path(), id: action } } })),
  );

  server.registerTool(
    "get_reddit_report",
    {
      title: "Get the Reddit results",
      description:
        "Mentions and links placed and live now, credits and cost, cost per mention, median and mean days live, and the share that survived 1, 7 and 30 days. group_by compares tactics.",
      inputSchema: {
        brand,
        experiment_id: z.string().uuid().optional(),
        subreddit: z.string().optional(),
        from: z.string().optional().describe("Only actions since this date, such as 2026-10-01."),
        group_by: z.enum(["subreddit", "strategy", "kind", "link_type", "account_tier", "experiment", "tone", "author"]).optional(),
      },
      annotations: read,
    },
    (q) => call(() => gtm.GET("/v1/workspaces/{ws}/reddit/report", { params: { path: path(), query: q } })),
  );

  server.registerTool(
    "get_reddit_subreddit",
    {
      title: "How strict is a subreddit",
      description: "Engain's historical removal rates in a subreddit across all its customers, beside this brand's own record there.",
      inputSchema: { brand, subreddit: z.string().min(2).describe("The name, such as proxies.") },
      annotations: { ...read, openWorldHint: true },
    },
    ({ brand, subreddit }) =>
      call(() => gtm.GET("/v1/workspaces/{ws}/reddit/subreddits/{name}", { params: { path: { ...path(), name: subreddit.replace(/^r\//, "") }, query: { brand } } })),
  );

  server.registerTool(
    "find_reddit_threads",
    {
      title: "Find Reddit threads to act on",
      description: "Threads Engain found for the brand's project: type new (fresh threads on its topics) or seo (threads that rank on Google). Pass engain_cursor back for more.",
      inputSchema: {
        brand,
        type: z.enum(["new", "seo"]).optional(),
        engain_cursor: z.string().optional(),
        limit: z.number().int().min(1).max(100).optional(),
      },
      annotations: { ...read, openWorldHint: true },
    },
    (q) => call(() => gtm.GET("/v1/workspaces/{ws}/reddit/threads", { params: { path: path(), query: q } })),
  );

  server.registerTool(
    "preflight_reddit_drafts",
    {
      title: "Preflight Reddit drafts",
      description:
        "Engain's rule check on up to 25 drafts for one subreddit. Publishes nothing and spends nothing; 5 calls a minute, so check a whole thread or several versions in one call. Each item: key, taskType (post, comment or reply), content, title for a post, and for a reply context.parentCommentUrl.",
      inputSchema: {
        brand,
        subreddit: z.string().min(2),
        items: z
          .array(
            z.object({
              key: z.string(),
              taskType: z.enum(["post", "comment", "reply"]),
              content: z.string(),
              title: z.string().optional(),
              context: z.object({ parentCommentUrl: z.string().optional(), postUrl: z.string().optional() }).optional(),
            }),
          )
          .min(1)
          .max(25),
      },
      annotations: { ...read, openWorldHint: true },
    },
    (body) => call(() => gtm.POST("/v1/workspaces/{ws}/reddit/preflight", { params: { path: path() }, body: body as never })),
  );

  if (readOnly) return;

  server.registerTool(
    "set_reddit_project",
    {
      title: "Choose a brand's Engain project or connection",
      description: "Which Engain project (and connection) the brand's actions go to. Budgets and approval are set by a person in Outflare, not here.",
      inputSchema: {
        brand,
        engain_project_id: z.string().optional(),
        integration_id: z.string().optional().describe("An Engain connection's id, from list_integrations."),
      },
      annotations: { ...write, openWorldHint: false },
    },
    ({ brand, ...body }) => call(() => gtm.PUT("/v1/workspaces/{ws}/reddit/settings/{brand}", { params: { path: { ...path(), brand } }, body: body as never })),
  );

  server.registerTool(
    "create_reddit_experiment",
    {
      title: "Start a Reddit experiment",
      description: "One hypothesis and the strategy it tests. Place actions in it with experiment_id; the report compares experiments.",
      inputSchema: {
        brand,
        name: z.string().min(1).max(200),
        hypothesis: z.string().max(2000).optional(),
        strategy: z.string().max(100).optional().describe("Such as top_comment_on_ranking_thread, reply_under_top_comment, self_reply_chain, post_then_self_comment, answer_without_link."),
      },
      annotations: { ...write, openWorldHint: false },
    },
    (body) => call(() => gtm.POST("/v1/workspaces/{ws}/reddit/experiments", { params: { path: path() }, body: body as never })),
  );

  server.registerTool(
    "close_reddit_experiment",
    {
      title: "Close a Reddit experiment",
      description: "End an experiment with its conclusion: what was tested, the numbers, what to do next. Future agents read it before testing again.",
      inputSchema: { experiment: experimentRef, conclusion: z.string().min(1).max(5000) },
      annotations: { ...write, openWorldHint: false },
    },
    ({ experiment, conclusion }) =>
      call(() => gtm.PATCH("/v1/workspaces/{ws}/reddit/experiments/{id}", { params: { path: { ...path(), id: experiment } }, body: { status: "closed", conclusion } as never })),
  );

  server.registerTool(
    "place_reddit_action",
    {
      title: "Place a comment, reply, post, thread or upvotes on Reddit",
      description:
        "Logs the action and sends it to Engain, spending credits, after the brand's limits are checked. Over a limit, or when the brand needs a person's approval, it is kept as a draft. Links must have no tracking parameters. Read get_reddit_guide first.",
      inputSchema: {
        brand,
        kind: z.enum(["comment", "reply", "post", "thread", "upvotes"]),
        experiment_id: z.string().uuid().optional(),
        target_url: z.string().optional().describe("The thread (comment), the comment's permalink (reply), https://www.reddit.com/r/<name> (post), any of those (thread), a thread or comment (upvotes)."),
        title: z.string().max(300).optional(),
        content: z.string().max(40000).optional(),
        children: z
          .array(
            z.object({
              id: z.string().max(64).optional(),
              parent_id: z.string().nullable().optional().describe("An earlier child's id; null replies to the parent."),
              account: z.number().int().min(1).max(10).describe("Which pool account replies, 1 to 10."),
              content: z.string(),
              delay_minutes: z.number().int().min(1).optional(),
            }),
          )
          .max(20)
          .optional()
          .describe("Thread groups only: the replies, in order."),
        link_url: z.string().optional(),
        link_type: z.enum(["none", "brand_site", "review_site", "docs", "third_party", "other"]).optional(),
        brand_mentioned: z.boolean().optional(),
        competitors: z.array(z.string()).max(20).optional(),
        strategy: z.string().max(100).optional(),
        tone: z.string().max(50).optional().describe("Such as recommendation, comparison, story, question."),
        account_tier: z.enum(["shared", "dedicated"]).optional(),
        upvotes_goal: z.number().int().min(0).max(2000).optional(),
        upvotes_per_day: z.number().int().min(0).max(10).optional(),
        upvotes_start: z.enum(["immediately", "after_3_days", "after_5_days", "after_7_days"]).optional(),
        parent_action_id: z.string().uuid().optional().describe("Upvotes on one of ours: its action id, once live."),
        scheduled_at: z.string().optional().describe("When to post, an RFC 3339 time."),
        notes: z.string().max(5000).optional(),
        preflight: z.unknown().optional().describe("The preflight verdict for this draft, kept with it."),
        submit: z.boolean().optional().describe("false keeps it a draft."),
      },
      annotations: write,
    },
    (body) => call(() => gtm.POST("/v1/workspaces/{ws}/reddit/actions", { params: { path: path() }, body: body as never })),
  );

  server.registerTool(
    "approve_reddit_action",
    {
      title: "Send a draft Reddit action",
      description: "Checks the brand's limits and sends a draft to Engain. When the brand needs a person's approval, only a person can.",
      inputSchema: { action: actionRef },
      annotations: write,
    },
    ({ action }) => call(() => gtm.POST("/v1/workspaces/{ws}/reddit/actions/{id}/approve", { params: { path: { ...path(), id: action } } })),
  );

  server.registerTool(
    "cancel_reddit_action",
    {
      title: "Withdraw a Reddit action",
      description: "A draft, or an approved action not yet sent to Engain.",
      inputSchema: { action: actionRef },
      annotations: { ...write, openWorldHint: false },
    },
    ({ action }) => call(() => gtm.POST("/v1/workspaces/{ws}/reddit/actions/{id}/cancel", { params: { path: { ...path(), id: action } } })),
  );

  server.registerTool(
    "check_reddit_action",
    {
      title: "Check a Reddit action now",
      description: "Look at a sent action on Engain and Reddit straight away instead of waiting for the next scheduled check.",
      inputSchema: { action: actionRef },
      annotations: write,
    },
    ({ action }) => call(() => gtm.POST("/v1/workspaces/{ws}/reddit/actions/{id}/check", { params: { path: { ...path(), id: action } } })),
  );
}
