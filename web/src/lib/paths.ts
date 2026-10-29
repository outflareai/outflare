// Where the app is lives in the URL, /{workspace}/{brand}/{tab}, so a
// reload or a shared link opens the same page on the same brand.
// {workspace} is the workspace's slug, its name in URLs (set in Workspace
// settings). {brand} is the brand's handle, or "all" for every brand; a
// workspace without brands, and the pages that belong to the whole
// workspace (Members, Tokens and the like), leave it out. A last segment
// opens one item, e.g. /acme/repocket/sequences/{id}.
//
// Older links still open: /w/{workspace id}/{tab} is rewritten to the slug,
// and /{workspace}/{tab}?brand=repocket to /{workspace}/repocket/{tab}.

export const appTabs = ["overview", "get-started", "setup", "ai", "ai-usage", "tools", "reports", "channels", "analytics", "tracking", "flags", "ab-tests", "links", "seo", "seo-overview", "audits", "fixes", "experiments", "keywords", "ai-visibility", "backlinks", "competitors", "reddit", "social", "reviews", "ads", "partners", "visitors", "inbox", "missions", "playbooks", "outcomes", "projects", "sequences", "link-building", "news", "infrastructure", "mailboxes", "domains", "warmup", "servers", "allocation", "lifecycle", "emails", "broadcasts", "journeys", "rules", "templates", "topics", "providers", "people", "leads", "lists", "lead-finder", "email-finder", "suppression", "knowledge", "company", "brands", "ideal-customers", "workspace", "members", "alerts", "digests", "senders", "connectors", "webhooks", "tokens", "logs", "audit", "instance", "account"] as const;
export type Tab = (typeof appTabs)[number];

export const isTab = (t?: string): t is Tab => !!t && (appTabs as readonly string[]).includes(t);

// Pages that belong to the whole workspace, not to a brand: their URLs
// have no brand in them.
export const workspaceTabs: readonly Tab[] = ["get-started", "tools", "ai", "ai-usage", "workspace", "members", "digests", "tokens", "webhooks", "instance", "account"];

// The brand part of URLs: a handle, "all", or "" for a workspace without
// brands. Set by lib/brand.ts as the brand picked changes.
let brandPart = "";
export function setBrandPart(part: string) {
  brandPart = part;
}

export type Place = { ws?: string; brand?: string; tab?: Tab; item?: string; legacy?: boolean };

// parsePath reads a workspace (slug or, in old links, ID), brand, tab and
// item. A brand is the segment after the workspace when it is not a page's
// name; brand handles can't be one. Paths that are not app pages, such as
// /invite, give {}.
export function parsePath(path: string): Place {
  const parts = path.split("/").slice(1);
  const legacy = parts[0] === "w";
  if (legacy) parts.shift();
  if (parts.length && parts[parts.length - 1] === "") parts.pop();
  const ws = parts.shift();
  if (!ws) return {};
  if (!legacy && !parts.length && ws.includes(".")) return {};
  let brand: string | undefined;
  if (!legacy && parts.length && !isTab(parts[0])) {
    brand = decodeURIComponent(parts.shift()!).toLowerCase();
    if (!/^[a-z0-9][a-z0-9-]*$/.test(brand)) return {};
  }
  const [tab, ...rest] = parts;
  if (tab !== undefined && !isTab(tab)) return {};
  const item = rest.length ? rest.map(decodeURIComponent).join("/") : undefined;
  return { ws: decodeURIComponent(ws), brand, tab, item, legacy };
}

// Slugs of the signed-in person's workspaces by ID, so any page can link
// to another page without passing slugs around.
const slugs = new Map<string, string>();

export function rememberWorkspaces(list: { id: string; slug: string }[]) {
  slugs.clear();
  for (const w of list) slugs.set(w.id, w.slug);
}

// appPath is the URL of a page on the brand picked: appPath(ws,
// "sequences", "q3-founders") is /acme/repocket/sequences/q3-founders, and
// appPath(ws, "connectors", "claude") /acme/connectors/claude. ws is the
// workspace ID or slug.
export function appPath(ws: string, tab: Tab, item?: string): string {
  const slug = slugs.get(ws) ?? ws;
  const brand = brandPart && !workspaceTabs.includes(tab) ? `/${encodeURIComponent(brandPart)}` : "";
  return `/${encodeURIComponent(slug)}${brand}/${tab}${item ? `/${encodeURIComponent(item)}` : ""}`;
}

// The same path with the workspace, brand and item hidden, for product
// analytics: /:ws/:brand/sequences/:item. Other paths come back unchanged.
export function anonymousPath(path: string): string {
  const p = parsePath(path);
  if (!p.ws || (!p.tab && !p.legacy && !p.brand)) return path; // /invite and the like
  return `/:ws${p.brand ? "/:brand" : ""}${p.tab ? `/${p.tab}` : ""}${p.item ? "/:item" : ""}`;
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
