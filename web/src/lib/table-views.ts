// Saved table column views ("Save as my default" in a table's Columns
// menu). They are kept with the person's account (GET /v1/me/table-views),
// so they follow them to every browser, and mirrored in this browser's
// storage so a table can read its view the moment it draws.
import { api } from "@/api";

const prefix = "outflare:table-view:";
const syncedKey = "outflare:table-views:synced";

export const viewKey = (view: string) => prefix + view;

/** The columns hidden in a saved view, or null when the viewer saved none. */
export function savedView(view?: string): string[] | null {
  if (!view) return null;
  try {
    const v = JSON.parse(localStorage.getItem(viewKey(view)) || "null") as { hidden?: unknown } | null;
    return v && Array.isArray(v.hidden) ? v.hidden.filter((h): h is string => typeof h === "string") : null;
  } catch {
    return null;
  }
}

/**
 * Brings this browser in step with the account, once the person is signed
 * in. The account wins. Views saved in this browser before views were kept
 * with the account are sent up the first time, so nobody loses theirs.
 */
export function syncTableViews(): Promise<void> {
  ready ??= sync();
  return ready;
}

// One read of the account's views per page load.
let ready: Promise<void> | undefined;

async function sync(): Promise<void> {
  try {
    const { data } = await api.GET("/v1/me/table-views");
    if (!data) {
      // Not signed in yet: read again when asked next.
      ready = undefined;
      return;
    }
    const server = new Map(data.data.map((v) => [v.view, v.hidden]));
    const local: string[] = [];
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i);
      if (k?.startsWith(prefix)) local.push(k.slice(prefix.length));
    }
    const first = localStorage.getItem(syncedKey) === null;
    for (const view of local) {
      if (server.has(view)) continue;
      const hidden = savedView(view);
      if (first && hidden) await api.PUT("/v1/me/table-views/{view}", { params: { path: { view } }, body: { hidden } });
      else localStorage.removeItem(viewKey(view));
    }
    for (const [view, hidden] of server) localStorage.setItem(viewKey(view), JSON.stringify({ hidden }));
    localStorage.setItem(syncedKey, "1");
  } catch {
    // Storage is off or the request failed: tables use their standard columns.
  }
}

/** Saves a view here and on the account. False when the account didn't take it. */
export async function saveTableView(view: string, hidden: string[]): Promise<boolean> {
  try {
    localStorage.setItem(viewKey(view), JSON.stringify({ hidden }));
  } catch {
    // The account still keeps it.
  }
  const { error } = await api.PUT("/v1/me/table-views/{view}", { params: { path: { view } }, body: { hidden } });
  return !error;
}

/** Drops a saved view here and on the account. */
export async function dropTableView(view: string): Promise<void> {
  try {
    localStorage.removeItem(viewKey(view));
  } catch {
    // Nothing was stored here.
  }
  await api.DELETE("/v1/me/table-views/{view}", { params: { path: { view } } });
}
