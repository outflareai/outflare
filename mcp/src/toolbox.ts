// The toolbox: how an app reaches every Outflare tool while seeing only a
// few. Outflare has well over a thousand tools, and apps cap or choke on long
// lists (ChatGPT keeps the first 200 and drops the rest). So by default the
// server lists a small core set plus find_tools, which searches every tool,
// and call_tool, which runs any of them. ?tools=all on /mcp (or
// OUTFLARE_MCP_TOOLS=all) lists every tool instead, for apps that cope.

import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import { z } from "zod";

export type ToolMode = "core" | "all";

/** toolMode reads a ?tools= value or OUTFLARE_MCP_TOOLS: "all" lists every tool; anything else the core set. */
export function toolMode(v: string | null | undefined): ToolMode {
  return (v ?? "").trim().toLowerCase() === "all" ? "all" : "core";
}

// The core set: what people ask for most, listed in every app. Keep it
// under MAX_CORE (a test checks), so it stays far below any app's cap as
// Outflare grows. Read-only tokens see the read tools among these.
export const CORE = new Set([
  // Start here.
  "get_context", "get_status", "update_context", "remember", "list_memories", "find_knowledge",
  "list_brands", "get_overview", "search_workspace",
  // People, companies, leads and lists.
  "list_records", "get_record", "lookup_record",
  "list_leads", "get_lead", "create_lead", "import_leads_csv", "list_lead_lists", "add_leads_to_list", "check_recipients",
  // Cold email.
  "list_sequences", "get_sequence", "create_sequence", "update_sequence", "preview_sequence", "enroll_leads",
  "list_inbox", "get_inbox_message", "reply_to_inbox_message",
  "list_mailboxes", "list_domains", "get_infrastructure",
  // Lifecycle email.
  "list_email_templates", "create_email_template", "send_email", "list_emails",
  "list_email_broadcasts", "create_email_broadcast", "send_email_broadcast", "list_journeys",
  // Results.
  "get_analytics_summary", "get_outcomes_report", "get_seo_overview",
  // Connectors and agents.
  "list_connectors", "request_connection", "list_integrations",
  "list_playbooks", "run_playbook", "list_actions", "get_action",
  // The way to everything else.
  "find_tools", "call_tool",
]);
export const MAX_CORE = 60;

// Every area Outflare covers, so an app knows to search before it says
// Outflare can't do something. find_tools' description and get_context
// carry it.
export const AREAS = [
  "brands, context and memories",
  "leads, lists and email finding",
  "customer records, CRM, events and consent",
  "cold email sequences, inbox and replies",
  "mailboxes, domains, warm-up and deliverability",
  "lifecycle email: templates, send_email, broadcasts, topics, journeys and email providers",
  "analytics, insights, funnels, dashboards and A/B tests",
  "SEO, crawls, rankings, backlinks, competitors and AI visibility",
  "ads, attribution, tracking links and conversions",
  "social, reviews, Reddit and digital PR",
  "partners, affiliates and payouts",
  "support, knowledge and help centres",
  "website visitors, consent banner and feature flags",
  "connectors, integrations and AI settings",
  "agents, playbooks, automations, rules and approvals",
  "spend, deals, meetings, webhooks and logs",
];

export const TOOLS_HINT =
  "Your app lists Outflare's core tools. Outflare has many more, covering: " + AREAS.join("; ") + ". " +
  "Before saying Outflare can't do something, call find_tools with what you want to do, then run the tool it finds with call_tool.";

type Entry = { name: string; config: ToolConfig; cb: (...a: unknown[]) => unknown; words: Words };
type ToolConfig = { title?: string; description?: string; inputSchema?: unknown; annotations?: { readOnlyHint?: boolean } };
type Words = { verb: string; name: Set<string>; title: Set<string>; text: Set<string> };

const STOP = new Set("a an the to for of and or in on at my our your with from by it its is are be me i we you how can do does please want need would like some any all this that using use via into".split(" "));
// Words people use for what Outflare calls something else.
const SYNONYMS: Record<string, string[]> = {
  make: ["create"], new: ["create"], add: ["create", "add"], build: ["create"], write: ["create", "draft", "write"], draft: ["draft", "create"],
  edit: ["update"], change: ["update"], modify: ["update"], rename: ["update"], set: ["set", "update"],
  remove: ["delete", "remove"], erase: ["delete"],
  show: ["get", "list"], see: ["get", "list"], view: ["get", "list"], read: ["get", "list", "read"], find: ["find", "list", "search"], fetch: ["get"],
  newsletter: ["broadcast"], blast: ["broadcast"], announcement: ["broadcast"],
  campaign: ["sequence", "broadcast", "campaign"], cadence: ["sequence"], outreach: ["sequence"],
  mail: ["email"], emails: ["email"], message: ["email", "inbox", "message"], transactional: ["email", "send"],
  lifecycle: ["email", "journey", "lifecycle"], drip: ["journey"], flow: ["journey", "flow"], workflow: ["journey", "playbook"], automation: ["journey", "rule", "automation"],
  contact: ["record", "lead"], person: ["record", "lead"], people: ["record", "lead"], customer: ["record", "customer"], company: ["record"], account: ["record", "account"], prospect: ["lead"],
  reply: ["reply", "inbox"], replies: ["reply", "inbox"],
  sender: ["mailbox", "sender"], inboxes: ["mailbox"], warmup: ["warmup"], deliverability: ["domain", "mailbox", "health"],
  stats: ["report", "overview", "summary"], metrics: ["report", "metric", "analytic"], traffic: ["analytic"], ranking: ["rank"],
  unsubscribe: ["suppression", "consent", "topic"], subscription: ["topic", "consent"],
  provider: ["provider"], ses: ["provider"], postmark: ["provider"], resend: ["provider"],
};

function stem(w: string): string {
  if (w.length > 4 && w.endsWith("ies")) return w.slice(0, -3) + "y";
  if (w.length > 3 && w.endsWith("s") && !w.endsWith("ss")) return w.slice(0, -1);
  return w;
}

function words(s: string | undefined): string[] {
  return (s ?? "").toLowerCase().split(/[^a-z0-9]+/).filter((w) => w && !STOP.has(w)).map(stem);
}

// A tool's name is a verb and what it acts on (create_email_template). The
// verb counts for less, so "list" in "send a newsletter to a list" doesn't
// pull in every list_ tool.
function index(name: string, config: ToolConfig): Words {
  const [verb, ...rest] = name.split("_");
  return { verb: stem(verb), name: new Set(words(rest.join(" "))), title: new Set(words(config.title)), text: new Set(words(config.description)) };
}

// score ranks a tool for the query: a word in the tool's name counts most,
// then its title, then its description. A word's synonyms count a little
// less than the word itself.
function score(e: Entry, query: string[], exact: string): number {
  let total = 0;
  let inName = 0;
  for (const q of query) {
    const alts = [q, ...(SYNONYMS[q] ?? []).map(stem)];
    let best = 0;
    let named = false;
    for (const [i, w] of alts.entries()) {
      const k = i === 0 ? 1 : 0.8;
      const inN = e.words.name.has(w) || e.words.verb === w;
      const s = e.words.name.has(w) ? 6 : e.words.verb === w ? 4 : e.words.title.has(w) ? 3 : e.words.text.has(w) ? 1 : 0;
      if (s * k > best) best = s * k;
      if (inN) named = true;
    }
    if (named) inName++;
    total += best;
  }
  // A tool whose name holds every word asked for is almost surely the one.
  if (query.length > 1 && inName === query.length) total += 4;
  // Searching a tool's own name finds it first.
  if (exact === e.name) total += 10;
  return total;
}

export class Toolbox {
  readonly tools = new Map<string, Entry>();

  add(name: string, config: ToolConfig, cb: (...a: unknown[]) => unknown) {
    this.tools.set(name, { name, config, cb, words: index(name, config) });
  }

  search(query: string, limit: number): Entry[] {
    const q = words(query);
    if (!q.length) return [];
    const exact = query.trim().toLowerCase().replace(/[\s-]+/g, "_");
    return [...this.tools.values()]
      .map((e) => ({ e, s: score(e, q, exact) }))
      .filter((r) => r.s > 0)
      .sort((a, b) => b.s - a.s || a.e.name.length - b.e.name.length || a.e.name.localeCompare(b.e.name))
      .slice(0, limit)
      .map((r) => r.e);
  }

  describe(e: Entry) {
    return {
      name: e.name,
      title: e.config.title,
      description: e.config.description,
      read_only: e.config.annotations?.readOnlyHint === true,
      input_schema: jsonSchema(e.config.inputSchema),
    };
  }
}

function schemaOf(input: unknown): z.ZodType | undefined {
  if (input === undefined) return undefined;
  if (input && typeof (input as z.ZodType).safeParse === "function") return input as z.ZodType;
  return z.object(input as z.ZodRawShape);
}

function jsonSchema(input: unknown): unknown {
  const s = schemaOf(input);
  if (!s) return { type: "object", properties: {} };
  try {
    const j = z.toJSONSchema(s, { io: "input", unrepresentable: "any" }) as Record<string, unknown>;
    delete j.$schema;
    return j;
  } catch {
    return { type: "object" };
  }
}

function text(t: string, isError = false): CallToolResult {
  return { content: [{ type: "text", text: t }], ...(isError ? { isError: true } : {}) };
}

/** registerToolbox adds find_tools and call_tool, which reach every tool in the box. */
export function registerToolbox(server: McpServer, box: Toolbox, readOnly: boolean) {
  server.registerTool(
    "find_tools",
    {
      title: "Find any Outflare tool",
      description:
        "Search all of Outflare's tools (over a thousand) by what you want to do, such as \"create an email template\", \"send a broadcast\" or \"check rankings\". " +
        "Your app lists only the core tools; this finds the rest. Outflare covers: " + AREAS.join("; ") + ". " +
        "Always search here before telling the person Outflare can't do something. Returns each match's name, description and input_schema; run one with call_tool.",
      inputSchema: {
        query: z.string().min(1).describe("What you want to do, in plain words."),
        limit: z.number().int().min(1).max(25).optional().describe("How many matches to return. Default 8."),
      },
      annotations: { readOnlyHint: true, openWorldHint: false },
    },
    ({ query, limit }: { query: string; limit?: number }) => {
      const found = box.search(query, limit ?? 8).map((e) => box.describe(e));
      const data = {
        tools: found,
        next_step: found.length
          ? "Run the tool that fits with call_tool, passing its name and arguments that match its input_schema."
          : "Nothing matched. Try other words, such as the thing (template, broadcast, sequence, record) and the action (create, list, send).",
      };
      return { content: [{ type: "text", text: JSON.stringify(data) }], structuredContent: data };
    },
  );

  server.registerTool(
    "call_tool",
    {
      title: "Run any Outflare tool",
      description:
        "Run any Outflare tool by name, including the ones your app doesn't list. Find the name and its input_schema with find_tools first. " +
        "The tool runs exactly as if it were called directly: same permissions, send policy, approvals and audit log.",
      inputSchema: {
        name: z.string().min(1).describe("The tool's name, from find_tools."),
        arguments: z.record(z.string(), z.unknown()).optional().describe("The tool's arguments, matching its input_schema."),
      },
      annotations: readOnly ? { readOnlyHint: true, openWorldHint: false } : { readOnlyHint: false, destructiveHint: true, openWorldHint: true },
    },
    async ({ name, arguments: args }: { name: string; arguments?: Record<string, unknown> }, extra: unknown) => {
      const e = box.tools.get(name.trim());
      if (!e || e.name === "call_tool") {
        const near = box.search(name.replace(/_/g, " "), 5).map((t) => t.name).filter((n) => n !== "call_tool");
        const why = readOnly ? " This connection can only read, so tools that change things are left out." : "";
        return text(`No tool is called ${JSON.stringify(name)}.${why}` + (near.length ? ` Did you mean: ${near.join(", ")}? ` : " ") + "Search with find_tools.", true);
      }
      const schema = schemaOf(e.config.inputSchema);
      if (!schema) return (await e.cb(extra)) as CallToolResult;
      const parsed = schema.safeParse(args ?? {});
      if (!parsed.success) {
        const problems = parsed.error.issues.map((i) => `${i.path.join(".") || "arguments"}: ${i.message}`).join("; ");
        return text(`The arguments for ${e.name} don't fit: ${problems}\nIts input_schema: ${JSON.stringify(jsonSchema(e.config.inputSchema))}`, true);
      }
      return (await e.cb(parsed.data, extra)) as CallToolResult;
    },
  );
}
