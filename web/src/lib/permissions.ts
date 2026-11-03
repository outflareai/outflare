import type { Scope } from "@outflare/sdk";

// One permissions model for everything that acts for a workspace: the
// approval screen an AI app sends people to, a connected app's settings and
// API tokens. People see areas in plain words; scopes stay underneath.

export type Area = { id: string; label: string; help: string; read: Scope[]; write: Scope[] };

// Each area can be seen, or seen and changed. An area with no write scopes
// is read only.
export const areas: Area[] = [
  { id: "context", label: "Company context and memories", help: "What Outflare knows about your company.", read: ["workspace.read"], write: ["context.manage"] },
  { id: "leads", label: "Leads and lists", help: "Import leads, build lists and record outcomes.", read: ["leads.read", "outcomes.read"], write: ["leads.manage", "outcomes.manage"] },
  { id: "sequences", label: "Sequences and customer profiles", help: "Write, start and pause sequences, and describe who you sell to.", read: ["sequences.read", "profiles.read"], write: ["sequences.manage", "profiles.manage"] },
  { id: "replies", label: "Replies", help: "Answer and label replies in your inbox.", read: ["inbox.read"], write: ["inbox.manage"] },
  { id: "infra", label: "Mailboxes, domains and connectors", help: "Plan and set up sending infrastructure.", read: ["mailboxes.read", "infrastructure.read", "integrations.read"], write: ["mailboxes.manage", "infrastructure.manage", "integrations.manage"] },
  { id: "customers", label: "Customers", help: "People, companies and other records, their emails and ids, product events and consent, and the fields they share.", read: ["customers.read"], write: ["customers.manage", "catalog.manage"] },
  { id: "seo", label: "SEO", help: "Add sites, check ownership, run crawls and work through the fixes list.", read: ["seo.read"], write: ["seo.manage"] },
  { id: "social", label: "Social", help: "Watch Reddit for posts the brand can help with and draft replies. Only a person approves a reply, in Outflare.", read: ["social.read"], write: ["social.manage"] },
  { id: "partners", label: "Partners", help: "Referral, affiliate and influencer programs, partners, rewards and payouts. Reporting conversions from your backend is its own power below.", read: ["partners.read"], write: ["partners.manage"] },
  { id: "agents", label: "Agent runs", help: "Run playbooks with Outflare's agent on the action layer, follow its runs and write to them.", read: ["agents.read"], write: ["agents.run"] },
  { id: "missions", label: "Missions", help: "Work toward a goal on its own, within limits. Being replaced by agent runs.", read: ["missions.read"], write: ["missions.run"] },
  { id: "actions", label: "Agent changes", help: "See every change agents made, what waits for a person, and what each agent may do.", read: ["actions.read"], write: [] },
  { id: "analytics", label: "Analytics", help: "Reports from connected analytics tools, and the campaign links that tag your traffic.", read: ["analytics.read"], write: ["analytics.manage"] },
  { id: "ads", label: "Paid ads", help: "Pause or resume campaigns and set their daily budgets on your own ad accounts. Reading them is Analytics; this spends money.", read: ["analytics.read"], write: ["ads.manage"] },
  { id: "content", label: "Content", help: "Write posts and pages for the brand's website and publish them through its own CMS connection, as drafts or live.", read: ["content.read"], write: ["content.manage"] },
  { id: "accounts", label: "Accounts", help: "The external accounts a brand's agent may act through, their attempts and incidents. Secrets go in the vault and are never read back; an agent asking to set one waits for a person.", read: ["accounts.read"], write: ["accounts.manage"] },
  { id: "browser", label: "Browser tasks", help: "Bounded work a browser worker does for a brand on sites with no API: within the task's hosts, account and ceilings, with every step logged.", read: ["browser.read"], write: ["browser.manage"] },
  { id: "my_ai_accounts", label: "Your AI accounts", help: "The AI subscriptions you use: their usage, resets and renewals.", read: ["my_ai_accounts.read"], write: ["my_ai_accounts.manage"] },
  { id: "ai_accounts", label: "Everyone's AI accounts", help: "Every AI subscription people in the workspace use.", read: ["ai_accounts.read"], write: ["ai_accounts.manage"] },
  { id: "webhooks", label: "Webhooks", help: "Endpoints that hear about changes, and their signing secrets.", read: ["webhooks.read"], write: ["webhooks.manage"] },
  { id: "people", label: "Members and audit log", help: "Who is in the workspace and what everyone did.", read: ["members.read", "audit.read"], write: [] },
];

// Powers that need a deliberate yes, never part of "can make changes".
export const extras: { scope: Scope; label: string; help: string }[] = [
  { scope: "infrastructure.approve", label: "Spend money", help: "Buy domains and mailboxes without asking. Off means a person approves each purchase." },
  { scope: "missions.approve", label: "Approve missions", help: "Approve missions and their held changes." },
  { scope: "actions.approve", label: "Approve agents' changes", help: "Approve or reject changes agents hold for a person. Never their own." },
  { scope: "query.endpoints", label: "Call published query endpoints", help: "Read the rows of the brand's published query endpoints, and nothing else. All another system's token needs to pull a report." },
  { scope: "partners.conversions", label: "Report partner conversions", help: "Send sign-ups, sales and refunds from your backend, and mark action rewards done. All a server token for partners needs." },
  { scope: "suppressions.manage", label: "Remove from the suppression list", help: "Let mail reach people who were suppressed." },
  { scope: "members.manage", label: "Invite people and change roles", help: "Never past what the person who connected it can do. Removing people is only for a signed-in person." },
  { scope: "ai_spend.read", label: "See AI spend", help: "Prices, spend and receipts on everyone's AI accounts." },
  { scope: "tokens.manage", label: "Manage API tokens", help: "Create, change and revoke tokens." },
  { scope: "workspace.manage", label: "Workspace settings", help: "Rename the workspace and change its URL." },
];

export type Level = "none" | "read" | "write";

export function levelOf(a: Area, scopes: readonly string[]): Level {
  if (a.write.length > 0 && a.write.some((s) => scopes.includes(s))) return "write";
  return a.read.some((s) => scopes.includes(s)) ? "read" : "none";
}

// withLevel sets one area's level and leaves the rest of the scopes alone.
export function withLevel(scopes: Scope[], a: Area, level: Level): Scope[] {
  const own = new Set<Scope>([...a.read, ...a.write]);
  const rest = scopes.filter((s) => !own.has(s));
  const add = level === "none" ? [] : level === "read" ? a.read : [...a.read, ...a.write];
  return [...new Set([...rest, ...add])];
}

export const readOnly: Scope[] = [...new Set(areas.flatMap((a) => a.read))];
export const canChange: Scope[] = [...new Set(areas.flatMap((a) => [...a.read, ...a.write]))].filter((s) => s !== "webhooks.manage");

/** Two words for what a set of scopes allows. */
export function summary(scopes: readonly string[]): string {
  const writes = scopes.filter((s) => !s.endsWith(".read"));
  if (writes.length === 0) return "Read only";
  if (extras.some((e) => scopes.includes(e.scope))) return "Can make changes, and more";
  return "Can make changes";
}

/** The areas a set of scopes may change, by name, for a one-line summary. */
export function changes(scopes: readonly string[]): string[] {
  return areas.filter((a) => levelOf(a, scopes) === "write").map((a) => a.label);
}
