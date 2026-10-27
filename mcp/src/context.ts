// What Outflare remembers about the business, and what is stopping it.
// get_context is the first call of every conversation: the company context,
// pinned memories, projects and blockers in one answer, so the person never
// repeats themselves and the agent knows where things stand. Memories are
// short lasting facts, never transcripts; the client keeps the chat.

import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import type { MissionClient } from "@outflare/sdk";
import { z } from "zod";

type Call = (fn: () => Promise<{ data?: unknown; error?: unknown; response: Response }>) => Promise<CallToolResult>;

const id = z.string().uuid();
const read = { readOnlyHint: true, openWorldHint: false } as const;
const write = { readOnlyHint: false, destructiveHint: false, openWorldHint: false } as const;
const kind = z.enum(["preference", "decision", "fact", "feedback"]);

// GUIDE is the map of Outflare an agent needs before its first call: what
// each thing is, how they connect and where to look when something is
// wrong. It is the resource outflare://guide and part of get_context.
export const GUIDE = `# Outflare, in one page

Outflare is the brain of a company's go-to-market. It sends cold email from the company's own mailboxes, connects out to every other tool (data, verification, CRMs, calendars, analytics, SEO, mailbox suppliers) and lets agents run all of it within limits people set.

## How things connect
- Workspace: one company (or one agency client). Everything below belongs to exactly one workspace. Members are people with a role (owner, admin, operator, viewer); API tokens are machines with scopes, optionally held to one project.
- Company context and memories: what Outflare knows about the business (get_context). Read them before writing anything; save what the person tells you with update_context and remember.
- Brands: a company can market several brands, each with its own website (list_brands). Connectors, members, tokens and leads are shared; each brand has its own context, memories, projects, domains and mailboxes. Pass brand to get_context to read the company's context with that brand's on top; a brand's mailboxes only send for its sequences. Records without a brand are shared.
- Project: one motion, an offer sold to one ideal customer in one market. Its brief (get_project_brief) holds offer, audience, market and ICP filters. Domains, mailboxes, lead lists and sequences can belong to a project; the rest is shared.
- Sending: domains hold mailboxes; mailboxes may sign in through a sending server. A mailbox sends cold email only when active and in the ready stage (new ones warm up first). Domain health (SPF, DKIM, DMARC, blocklists, bounces) decides whether mail lands.
- Leads and lists: leads are people; lead lists group them, and can be built from a data connector. The suppression list is never emailed.
- Sequences: steps of email. Enroll leads, then activate the sequence; mailboxes are allocated to it automatically. Replies land in the inbox.
- Connectors: optional. Nothing needs connecting to run. list_connectors shows what can connect; request_connection gives the person a link to connect it, so keys never pass through chat.
- Missions: an agent working toward a goal within limits. Writes are dry runs or wait for a person until the mission allows them. An agent can never approve its own writes.
- Outcomes and reports: meetings, deals and spend give cost per meeting; reports and analytics show what is working.
- Events: every change is in the audit log and events feed (list_events), and can go to webhooks.

## When something is wrong
- get_status lists every blocker, worst first, with the tool that fixes it and whether you or a person must act.
- A failed call returns a code and a next step. forbidden means you aren't allowed to do that: give the person the link in the message and say which permission to turn on; do not retry.
- Never ask for API keys or passwords in chat. Never approve spending or a mission's held writes yourself.
`;

export function registerContextTools(
  server: McpServer,
  gtm: MissionClient,
  workspace: () => string,
  readOnly: boolean,
  call: Call,
  withBrand: <T extends { brand?: string }>(q: T) => T,
): void {
  const path = () => ({ ws: workspace() });

  server.registerTool(
    "get_context",
    {
      title: "Start here: what Outflare knows and what is blocked",
      description:
        "Call this first in every conversation. Returns the company context (company, website, about, goals, voice, rules, notes), " +
        "pinned and recent memories, the projects and whether each has a brief, the workspace status: a one-line summary, the set-up checklist " +
        "and blockers with the tool that fixes each, and onboarding (see get_onboarding). If context.ask_for_missing is true, ask the person for context.missing in one short message " +
        "(or offer to read their website) and save the answers with update_context. Never ask for anything already here. " +
        "If brands lists several, the company markets several brands: pass brand (a handle) to add that brand's context and memories, which win over the company's where set " +
        "(rules add up). A project token always gets its project's brand.",
      inputSchema: { brand: z.string().optional().describe("A brand's handle or id from brands, or all.") },
      annotations: read,
    },
    async (q) => {
      const [ctx, status, onboarding] = await Promise.all([
        call(() => gtm.GET("/v1/workspaces/{ws}/context", { params: { path: path(), query: withBrand(q) } })),
        call(() => gtm.GET("/v1/workspaces/{ws}/status", { params: { path: path() } })),
        call(() => gtm.GET("/v1/workspaces/{ws}/onboarding", { params: { path: path() } })),
      ]);
      if (ctx.isError) return ctx;
      const data = {
        ...(ctx.structuredContent ?? {}),
        status: status.isError ? { error: (status.content[0] as { text: string }).text } : status.structuredContent,
        // Project tokens can't read it; then it is simply left out.
        ...(onboarding.isError ? {} : { onboarding: onboarding.structuredContent }),
      };
      return { content: [{ type: "text", text: JSON.stringify(data) }], structuredContent: data };
    },
  );

  server.registerTool(
    "get_status",
    {
      title: "What is blocked, and what to do",
      description:
        "Everything stopping the workspace, worst first: mailboxes that can't sign in or aren't ready, domains with critical problems, paused sending servers, " +
        "broken connections, mission changes waiting for a person, paused missions, sequences short of mailboxes, plans awaiting approval, unread replies, " +
        "missing company context. Each blocker has fix.tool (call it next), fix.page (the dashboard page) and fix.who: agent means you can resolve it, " +
        "person means tell the person what to do there. Also can_send and the set-up checklist.",
      annotations: read,
    },
    () => call(() => gtm.GET("/v1/workspaces/{ws}/status", { params: { path: path() } })),
  );

  server.registerTool(
    "get_onboarding",
    {
      title: "Getting-started steps",
      description:
        "The steps a new workspace is walked through: add its website (brand), pick what to do first (goal), connect an AI, connect the tools that goal needs, start a first mission; then invite the team. " +
        "Each is done once the workspace has it (however it was added) or skipped by a person; none is required. step.tool is the tool that does it " +
        "(empty: a person must, at step.page). When steps are left and not skipped, offer once to help with next_step; if the person declines, skip it with update_onboarding.",
      annotations: read,
    },
    () => call(() => gtm.GET("/v1/workspaces/{ws}/onboarding", { params: { path: path() } })),
  );

  if (!readOnly) {
    const steps = ["brand", "goal", "ai", "connect", "mission", "team"] as const;
    server.registerTool(
      "update_onboarding",
      {
        title: "Update getting-started steps",
        description:
          "Record what the person wants to do first (goal), skip steps they want to leave for later, bring skipped ones back, or close the walk-through in the app (dismissed). " +
          "Skipping turns nothing off: a skipped step still counts as done once the workspace has it. Needs workspace.manage.",
        inputSchema: {
          skip: z.array(z.enum(steps)).optional().describe("Steps to leave for later."),
          unskip: z.array(z.enum(steps)).optional().describe("Skipped steps to bring back."),
          goal: z.enum(["outreach", "search", "reports", "nurture"]).optional().describe("What to do first: find and email new customers, grow search traffic, report on what works, or nurture existing customers."),
          dismissed: z.boolean().optional().describe("True closes the walk-through in the app; false opens it again."),
        },
        annotations: write,
      },
      (body) => call(() => gtm.PATCH("/v1/workspaces/{ws}/onboarding", { params: { path: path() }, body })),
    );
  }

  server.registerTool(
    "list_memories",
    {
      title: "Search memories",
      description:
        "Lasting facts, preferences and decisions people and agents saved, pinned first. Search with q before asking the person something they may have told you before.",
      inputSchema: {
        q: z.string().max(200).optional().describe("Words the memory contains."),
        kind: kind.optional(),
        project_id: z.string().optional().describe("A project's id, or none for workspace-wide memories."),
        brand: z.string().optional().describe("A brand's handle or id, or none for memories true for the whole company."),
        pinned: z.boolean().optional(),
        limit: z.number().int().min(1).max(200).optional(),
        cursor: z.string().optional().describe("next_cursor from the previous answer, for the next page."),
      },
      annotations: read,
    },
    (q) => call(() => gtm.GET("/v1/workspaces/{ws}/memories", { params: { path: path(), query: q } })),
  );

  server.registerResource(
    "guide",
    "outflare://guide",
    { title: "Outflare guide", description: "What Outflare is, how workspaces, projects, mailboxes, sequences, connectors and missions connect, and where to look when something is wrong.", mimeType: "text/markdown" },
    async (uri) => ({ contents: [{ uri: uri.href, mimeType: "text/markdown", text: GUIDE }] }),
  );

  server.registerResource(
    "context",
    "outflare://context",
    { title: "Company context", description: "What Outflare knows about the company and its pinned memories, as Markdown. Same as get_context's markdown.", mimeType: "text/markdown" },
    async (uri) => {
      const res = await call(() => gtm.GET("/v1/workspaces/{ws}/context", { params: { path: path() } }));
      const sc = res.structuredContent as { markdown?: string } | undefined;
      return { contents: [{ uri: uri.href, mimeType: "text/markdown", text: sc?.markdown ?? (res.content[0] as { text: string }).text }] };
    },
  );

  server.registerResource(
    "status",
    "outflare://status",
    { title: "Workspace status", description: "Blockers, worst first, with the tool that fixes each. Same as get_status.", mimeType: "application/json" },
    async (uri) => {
      const res = await call(() => gtm.GET("/v1/workspaces/{ws}/status", { params: { path: path() } }));
      return { contents: [{ uri: uri.href, mimeType: "application/json", text: (res.content[0] as { text: string }).text }] };
    },
  );

  server.registerPrompt(
    "set_up_outflare",
    {
      title: "Set up Outflare",
      description: "A short interview that tells Outflare about the company once, so no agent or chat asks again, then the first steps to sending.",
      argsSchema: { website: z.string().max(253).optional().describe("The company's website, to read instead of asking.") },
    },
    ({ website }) => ({
      messages: [
        {
          role: "user",
          content: {
            type: "text",
            text:
              "Set up Outflare for my company.\n" +
              "1. Call get_context. Keep everything it already knows; never ask for it again.\n" +
              `2. ${website ? `Read ${website} if you can browse, and draft` : "For"} what is missing (company, what we do, goals, voice), ` +
              "ask me in ONE short message, with your draft for me to confirm or correct. Rules and notes are optional.\n" +
              "3. Save the answers with update_context. Save any lasting preference I mention with remember.\n" +
              "4. If there are no projects, suggest one (offer, audience, market) and create it with create_project when I agree.\n" +
              "5. Read status.setup and the blockers, and tell me the next one or two steps in plain words, with the dashboard page for anything only I can do.\n" +
              "Keep each message short. Never ask me for API keys.",
          },
        },
      ],
    }),
  );

  server.registerPrompt(
    "whats_blocked",
    {
      title: "What's blocked?",
      description: "Everything stopping outreach right now, what you can fix yourself, and what needs the person.",
      argsSchema: {},
    },
    () => ({
      messages: [
        {
          role: "user",
          content: {
            type: "text",
            text:
              "What is stopping Outflare right now?\n" +
              "1. Call get_status. Lead with its summary.\n" +
              "2. For each blocker, worst first: one line on what it means. If fix.who is agent, call fix.tool to look closer and fix it if I have asked you to run things. " +
              "If fix.who is person, say exactly what I need to do and link fix.page.\n" +
              "3. End with whether we can send today (can_send) and the next set-up step that is not done.\n" +
              "Keep it under 200 words.",
          },
        },
      ],
    }),
  );

  if (readOnly) return;

  server.registerTool(
    "update_context",
    {
      title: "Save what you learned about the company",
      description:
        "Save the company context: who they are, what they do, goals, voice and rules. Send only the fields to change, in the person's own words where possible; " +
        "every agent and teammate reads it from then on. Pass the version you read to avoid overwriting someone's edit (409 context_changed means read again). " +
        "Offer, audience, market and ICP belong on a project (update_project), not here. onboarding_dismissed true stops asking for missing fields.",
      inputSchema: {
        company: z.string().max(200).optional(),
        website: z.string().max(253).optional(),
        about: z.string().max(4000).optional().describe("What the company does, in a few sentences."),
        goals: z.string().max(4000).optional().describe("What the person wants from Outflare, with numbers and dates where given."),
        voice: z.string().max(4000).optional().describe("How emails should sound: tone, length, sign-off, words to avoid."),
        rules: z.string().max(4000).optional().describe("Things never to do."),
        notes: z.string().max(100000).optional().describe("Everything else, in Markdown: proof points, competitors, past results."),
        brands: z.string().max(4000).optional().describe("How the company's brands relate to each other. Each brand's own context goes in update_brand_context."),
        version: z.number().int().optional().describe("context.version from get_context."),
        onboarding_dismissed: z.boolean().optional().describe("True when the person does not want to be asked for the missing fields."),
      },
      annotations: { ...write, idempotentHint: true },
    },
    (body) => call(() => gtm.PATCH("/v1/workspaces/{ws}/context", { params: { path: path() }, body })),
  );

  server.registerTool(
    "remember",
    {
      title: "Remember something",
      description:
        "Save one lasting fact, preference or decision in a sentence, so no one has to say it again: \"Never mention pricing in a first email\", " +
        "\"Only target Series A and later\", \"Paused acme-io.example on 25 Oct after bounces\". Do it when the person states something that should outlive this chat, " +
        "and tell them in one line so they can correct it. Never save transcripts, secrets or personal data about leads. Pin what every conversation should know.",
      inputSchema: {
        text: z.string().min(1).max(500),
        kind: kind.optional().describe("preference (how they like things), decision (what was decided), fact (about the business), feedback (what worked or didn't). Default fact."),
        project_id: id.nullable().optional().describe("The project it is about; leave out for the whole workspace."),
        brand_id: z.string().nullable().optional().describe("The brand it is true for (a handle or id), when it is not true for every brand. Leave out with project_id."),
        pinned: z.boolean().optional(),
      },
      annotations: { ...write, idempotentHint: true },
    },
    (body) => call(() => gtm.POST("/v1/workspaces/{ws}/memories", { params: { path: path() }, body })),
  );

  server.registerTool(
    "update_memory",
    {
      title: "Correct a memory",
      description: "Change a memory's text or kind, pin or unpin it, or move it to a project or brand (null for the whole workspace). For when the person corrects you.",
      inputSchema: {
        memory_id: id,
        text: z.string().min(1).max(500).optional(),
        kind: kind.optional(),
        pinned: z.boolean().optional(),
        project_id: id.nullable().optional(),
        brand_id: z.string().nullable().optional().describe("A brand's handle or id; null for the whole company."),
      },
      annotations: { ...write, idempotentHint: true },
    },
    ({ memory_id, ...body }) => call(() => gtm.PATCH("/v1/workspaces/{ws}/memories/{id}", { params: { path: { ws: workspace(), id: memory_id } }, body })),
  );

  server.registerTool(
    "forget",
    {
      title: "Forget a memory",
      description: "Delete a memory that is wrong or out of date, or that the person asks you to forget.",
      inputSchema: { memory_id: id },
      annotations: { readOnlyHint: false, destructiveHint: true, idempotentHint: true, openWorldHint: false },
    },
    ({ memory_id }) => call(() => gtm.DELETE("/v1/workspaces/{ws}/memories/{id}", { params: { path: { ws: workspace(), id: memory_id } } })),
  );
}
