// What a role allows, grouped the way people think about it: areas that
// are None, View or Edit, and switches for actions that need their own say.
// Scopes not listed here (a new feature's) show under "Other" as switches.
// API tokens and connected apps use the simpler areas in ./permissions.

export type RoleArea = { label: string; help: string; read: string; manage: string };
export type RoleToggle = { label: string; help: string; scope: string };
export type RoleGroup = { label: string; areas: RoleArea[]; toggles: RoleToggle[] };

export const roleGroups: RoleGroup[] = [
  {
    label: "The marketing work",
    areas: [
      { label: "CRM and customer records", help: "People, companies and their records", read: "customers.read", manage: "customers.manage" },
      { label: "Leads and lists", help: "Lead lists and finding leads", read: "leads.read", manage: "leads.manage" },
      { label: "Outreach", help: "Sequences and enrolling leads", read: "sequences.read", manage: "sequences.manage" },
      { label: "Mailboxes and domains", help: "Sending mailboxes and their set-up", read: "mailboxes.read", manage: "mailboxes.manage" },
      { label: "Inbox", help: "Replies and conversations", read: "inbox.read", manage: "inbox.manage" },
      { label: "Lifecycle email", help: "Templates, broadcasts and journeys", read: "emails.read", manage: "emails.manage" },
      { label: "SEO", help: "Sites, crawls and experiments", read: "seo.read", manage: "seo.manage" },
      { label: "Social", help: "Watches and drafted posts", read: "social.read", manage: "social.manage" },
      { label: "Partners", help: "Referral, affiliate and influencer programs, rewards and payouts", read: "partners.read", manage: "partners.manage" },
      { label: "Analytics", help: "Reports and campaign links", read: "analytics.read", manage: "analytics.manage" },
      { label: "Outcomes", help: "Meetings, deals and spend", read: "outcomes.read", manage: "outcomes.manage" },
      { label: "Customer profiles", help: "Ideal customer profiles, and projects until they are removed", read: "projects.read", manage: "projects.manage" },
      { label: "AI missions", help: "Run Outflare's agent", read: "missions.read", manage: "missions.run" },
      { label: "Infrastructure", help: "Plans for domains and mailboxes", read: "infrastructure.read", manage: "infrastructure.manage" },
    ],
    toggles: [
      { label: "Edit company and brand context", help: "What Outflare knows about the business", scope: "context.manage" },
      { label: "Send email", help: "Send lifecycle and transactional email", scope: "emails.send" },
      { label: "Post to social accounts", help: "Approve a drafted post so it goes out", scope: "social.post" },
      { label: "Report partner conversions", help: "Send sales, leads and refunds from your backend", scope: "partners.conversions" },
      { label: "Approve AI missions", help: "Start missions and approve what they hold", scope: "missions.approve" },
      { label: "Approve spending", help: "Buy domains and mailboxes", scope: "infrastructure.approve" },
    ],
  },
  {
    label: "Workspace",
    areas: [
      { label: "Connected tools", help: "Connect and disconnect accounts", read: "integrations.read", manage: "integrations.manage" },
      { label: "Webhooks", help: "URLs that receive workspace events", read: "webhooks.read", manage: "webhooks.manage" },
    ],
    toggles: [
      { label: "Workspace settings", help: "Name, address and brand rules", scope: "workspace.manage" },
      { label: "Unsubscribe list", help: "Remove someone from it, so they can be emailed again", scope: "suppressions.manage" },
      { label: "Shared fields", help: "Record types, fields and option lists", scope: "catalog.manage" },
      { label: "API tokens", help: "Create and revoke tokens", scope: "tokens.manage" },
      { label: "Audit log and request logs", help: "See everything that happened", scope: "audit.read" },
    ],
  },
  {
    label: "People",
    areas: [],
    toggles: [
      { label: "See people", help: "Who is in the workspace", scope: "members.read" },
      { label: "Invite people and change roles", help: "Never past what they can do themselves", scope: "members.manage" },
      { label: "Remove people", help: "Remove someone, or reduce what they can do", scope: "members.remove" },
      { label: "Edit roles", help: "Change what roles allow, and add new ones", scope: "roles.manage" },
    ],
  },
];

// Always on: without it nothing in the workspace opens.
export const baseScope = "workspace.read";

const known = new Set<string>([baseScope, ...roleGroups.flatMap((g) => [...g.areas.flatMap((a) => [a.read, a.manage]), ...g.toggles.map((t) => t.scope)])]);

// otherScopes are scopes this page doesn't group yet.
export function otherScopes(all: string[]): string[] {
  return all.filter((s) => !known.has(s));
}

export type RoleLevel = "none" | "view" | "edit";

export function roleLevel(scopes: string[], a: RoleArea): RoleLevel {
  if (scopes.includes(a.manage)) return "edit";
  if (scopes.includes(a.read)) return "view";
  return "none";
}

export function setRoleLevel(scopes: string[], a: RoleArea, level: RoleLevel): string[] {
  const rest = scopes.filter((s) => s !== a.read && s !== a.manage);
  if (level === "view") return [...rest, a.read];
  if (level === "edit") return [...rest, a.read, a.manage];
  return rest;
}

export function within(inner: readonly string[], outer: readonly string[]): boolean {
  return inner.every((s) => outer.includes(s));
}
