// Where the app is lives in the URL, /{workspace}/{tab}, so a reload or a
// shared link opens the same page. {workspace} is the workspace's slug, its
// name in URLs (set in Workspace settings). A third segment opens one item,
// e.g. /acme/sequences/{id} or /acme/domains/acme-com.example.
//
// Links from before slugs, /w/{workspace id}/{tab}, still open and are
// rewritten to the slug.

export const appTabs = ["overview", "get-started", "setup", "ai", "ai-usage", "tools", "reports", "analytics", "tracking", "flags", "links", "seo", "seo-overview", "audits", "fixes", "experiments", "keywords", "ai-visibility", "backlinks", "competitors", "reddit", "social", "reviews", "ads", "visitors", "inbox", "missions", "playbooks", "outcomes", "projects", "sequences", "infrastructure", "mailboxes", "domains", "warmup", "servers", "allocation", "lifecycle", "emails", "broadcasts", "journeys", "templates", "topics", "providers", "leads", "lists", "lead-finder", "suppression", "company", "brands", "ideal-customers", "workspace", "members", "alerts", "digests", "senders", "connectors", "webhooks", "tokens", "logs", "audit", "agency", "instance", "account"] as const;
export type Tab = (typeof appTabs)[number];

const isTab = (t?: string): t is Tab => !!t && (appTabs as readonly string[]).includes(t);

export type Place = { ws?: string; tab?: Tab; item?: string; legacy?: boolean };

// parsePath reads a workspace (slug or, in old links, ID), tab and item.
// Paths that are not app pages, such as /invite, give {}.
export function parsePath(path: string): Place {
  const parts = path.split("/").slice(1);
  const legacy = parts[0] === "w";
  if (legacy) parts.shift();
  const [ws, tab, ...rest] = parts;
  if (!ws || (!legacy && tab !== undefined && !isTab(tab))) return {};
  if (!legacy && tab === undefined && ws.includes(".")) return {};
  const item = rest.length ? rest.map(decodeURIComponent).join("/") : undefined;
  return { ws: decodeURIComponent(ws), tab: isTab(tab) ? tab : undefined, item, legacy };
}

// Slugs of the signed-in person's workspaces by ID, so any page can link
// to another page without passing slugs around.
const slugs = new Map<string, string>();

export function rememberWorkspaces(list: { id: string; slug: string }[]) {
  slugs.clear();
  for (const w of list) slugs.set(w.id, w.slug);
}

// appPath is the URL of a page: appPath(ws, "connectors", "claude") is
// /acme/connectors/claude. ws is the workspace ID or slug.
export function appPath(ws: string, tab: Tab, item?: string): string {
  const slug = slugs.get(ws) ?? ws;
  return `/${encodeURIComponent(slug)}/${tab}${item ? `/${encodeURIComponent(item)}` : ""}`;
}

// The same path with the workspace and item hidden, for product analytics:
// /:ws/sequences/:item. Other paths come back unchanged.
export function anonymousPath(path: string): string {
  const p = parsePath(path);
  if (!p.ws || (!p.tab && !p.legacy)) return path; // /invite and the like
  return `/:ws${p.tab ? `/${p.tab}` : ""}${p.item ? "/:item" : ""}`;
}

// Item pages whose URLs name the item by a slug made from its name
// (/acme/sequences/q3-founders) rather than its ID. A connection's slug is
// its connector (/acme/connectors/posthog, then posthog-2). GET
// .../links/{kind}/{ref} turns either into the other; old ID links still open.
export type ItemKind = "sequence" | "project" | "list" | "mission" | "server" | "plan" | "connection" | "token" | "webhook";
export const itemKinds: Partial<Record<Tab, ItemKind>> = { sequences: "sequence", projects: "project", lists: "list", missions: "mission", servers: "server", infrastructure: "plan", connectors: "connection", tokens: "token", webhooks: "webhook" };

// Tabs whose item part can also name something that is not an item, so a
// slug that finds nothing goes to the page as it is: /acme/connectors/posthog
// opens the PostHog connection, or the dialog to connect PostHog when there
// is none, and /acme/connectors/analytics a category.
export const itemOrPage: Partial<Record<Tab, true>> = { connectors: true };

export const isID = (s?: string) => !!s && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(s);

// An item part of a URL that names an item by slug, as opposed to an ID or
// a page of the app's own such as "new".
export const isItemSlug = (tab: Tab, item?: string) => !!item && !!itemKinds[tab] && !isID(item) && item !== "new";
