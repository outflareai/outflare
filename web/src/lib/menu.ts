// What a person pinned to their menu and what they opened last. Both are
// kept with their account (GET /v1/me/menu), so they follow them to every
// browser, and mirrored in this browser's storage so the menu can draw
// them at once.
//
// An entry is a ref: a page's id ("sequences"), or a path to one thing
// inside a page (/{workspace id}/{brand or -}/{page}/{item}). A ref never
// holds a title; the names of things are remembered in this browser only,
// as the person opens them.
import { api } from "@/api";
import { useSyncExternalStore } from "react";

export type Recent = { ref: string; at: string };
type Menu = { pins: string[]; recents: Recent[] };

const none: Menu = { pins: [], recents: [] };
const maxRecents = 50;
let user = "";
let menu: Menu = none;
const listeners = new Set<() => void>();
const key = () => `outflare:menu:${user}`;

const strings = (v: unknown): string[] => (Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : []);

function set(next: Menu) {
  menu = next;
  if (user) {
    try {
      localStorage.setItem(key(), JSON.stringify(next));
    } catch {
      // Not remembered here; the account still has it.
    }
  }
  listeners.forEach((l) => l());
}

/**
 * Called once the signed-in person is known. Shows what this browser
 * remembers, then brings it in step with the account. browserPins are the
 * pins this browser kept before pins were saved to the account: they are
 * sent up once, so nobody loses theirs.
 */
export function startMenu(id: string, browserPins: string[]) {
  if (id === user) return;
  user = id;
  let mirrored = false;
  menu = { pins: browserPins, recents: [] };
  try {
    const m = JSON.parse(localStorage.getItem(key()) ?? "null") as { pins?: unknown; recents?: unknown } | null;
    if (m && typeof m === "object") {
      mirrored = true;
      menu = { pins: strings(m.pins), recents: Array.isArray(m.recents) ? m.recents.filter((r): r is Recent => !!r && typeof r.ref === "string" && typeof r.at === "string") : [] };
    }
  } catch {
    // Storage is off or holds something old: the account's copy is read next.
  }
  listeners.forEach((l) => l());
  void sync(id, mirrored ? [] : browserPins);
}

async function sync(id: string, browserPins: string[]) {
  try {
    const { data } = await api.GET("/v1/me/menu");
    if (!data || id !== user) return;
    let pins = data.pins;
    if (!pins.length && browserPins.length) {
      const saved = await api.PUT("/v1/me/menu/pins", { body: { pins: browserPins } });
      if (id !== user) return;
      if (saved.data) pins = saved.data.pins;
    }
    set({ pins, recents: data.recents.map((r) => ({ ref: r.ref, at: r.opened_at })) });
  } catch {
    // Offline or the request failed: the menu shows what this browser remembers.
  }
}

/** On sign-out: the next person starts with their own menu. */
export function resetMenu() {
  user = "";
  menu = none;
  listeners.forEach((l) => l());
}

export function useMenu(): Menu {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => menu,
  );
}

export const isPinned = (ref: string) => menu.pins.includes(ref);

/** Pins or unpins a ref, here and on the account. True when it is now pinned. */
export function togglePinned(ref: string): boolean {
  const on = !menu.pins.includes(ref);
  const pins = on ? [...menu.pins, ref] : menu.pins.filter((p) => p !== ref);
  set({ ...menu, pins });
  void api.PUT("/v1/me/menu/pins", { body: { pins } });
  return on;
}

/** Notes that the person opened something just now. */
export function opened(ref: string) {
  if (!user || menu.recents[0]?.ref === ref) return;
  set({ ...menu, recents: [{ ref, at: new Date().toISOString() }, ...menu.recents.filter((r) => r.ref !== ref)].slice(0, maxRecents) });
  void api.POST("/v1/me/menu/recents", { body: { ref } });
}

export function clearRecents() {
  set({ ...menu, recents: [] });
  void api.DELETE("/v1/me/menu/recents");
}

// A ref to one thing inside a page. The parts are ids and slugs; whatever
// else an item holds is percent-encoded, so a ref is always URL-safe.
const part = (s: string) => encodeURIComponent(s).replace(/[!'()*]/g, (c) => `%${c.charCodeAt(0).toString(16).toUpperCase()}`);
export const itemRef = (ws: string, brand: string, tab: string, item: string) => `/${part(ws)}/${brand ? part(brand) : "-"}/${tab}/${item.split("/").map(part).join("/")}`;

export type RefPlace = { tab: string; ws?: string; brand?: string; item?: string };
export function parseRef(ref: string): RefPlace | undefined {
  if (!ref.startsWith("/")) return { tab: ref };
  const [, ws, brand, tab, ...rest] = ref.split("/");
  if (!ws || !brand || !tab || !rest.length) return undefined;
  try {
    return { ws: decodeURIComponent(ws), brand: brand === "-" ? "" : decodeURIComponent(brand), tab, item: rest.map(decodeURIComponent).join("/") };
  } catch {
    return undefined;
  }
}

// The names of things, as this browser saw them: a ref's label in the menu.
const labelsKey = "outflare:menu-labels";
const maxLabels = 300;
function labels(): Record<string, string> {
  try {
    const l = JSON.parse(localStorage.getItem(labelsKey) ?? "{}");
    return l && typeof l === "object" ? (l as Record<string, string>) : {};
  } catch {
    return {};
  }
}
export const labelOf = (ref: string): string | undefined => labels()[ref];
export function rememberLabel(ref: string, label: string) {
  const all = labels();
  if (all[ref] === label) return;
  delete all[ref];
  all[ref] = label;
  const keys = Object.keys(all);
  for (const k of keys.slice(0, Math.max(0, keys.length - maxLabels))) delete all[k];
  try {
    localStorage.setItem(labelsKey, JSON.stringify(all));
  } catch {
    // The menu shows the page's name and a short id instead.
  }
  listeners.forEach((l) => l());
}

/** "2h", "3d": how long ago, short enough for a menu row. */
export function ago(at: string, now = Date.now()): string {
  const m = Math.max(0, Math.round((now - new Date(at).getTime()) / 60000));
  if (m < 1) return "now";
  if (m < 60) return `${m}m`;
  if (m < 60 * 24) return `${Math.round(m / 60)}h`;
  return `${Math.round(m / 1440)}d`;
}
