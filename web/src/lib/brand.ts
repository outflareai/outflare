// The brand the app is narrowed to, for companies that market several
// brands in one workspace. Like a project in PostHog, one brand is shown at
// a time: a workspace with brands opens on the one picked last, or its
// first. "" is all brands, which someone picks on purpose. The choice is
// kept per person and workspace, and in the URL (?brand=repocket, or
// ?brand=all) so a shared link opens on the same brand. Pages read it with
// useBrand() and pass it to list calls.

import { useEffect, useSyncExternalStore } from "react";
import type { Brand } from "@outflare/sdk";
import { api } from "../api";
import { track } from "./analytics";

type State = { ws: string; brand: string; brands: Brand[]; loaded: boolean };

let state: State = { ws: "", brand: "", brands: [], loaded: false };
const listeners = new Set<() => void>();

function set(next: Partial<State>) {
  state = { ...state, ...next };
  listeners.forEach((l) => l());
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => listeners.delete(l);
}

const storageKey = (ws: string) => `outflare.brand.${ws}`;
// Kept for someone who picked all brands, so the next visit doesn't narrow.
const allBrands = "all";

// remembered is the brand picked last: a handle, allBrands, or null when
// nothing was picked yet.
function remembered(ws: string): string | null {
  const fromURL = new URLSearchParams(location.search).get("brand");
  if (fromURL !== null) return fromURL || allBrands;
  try {
    return localStorage.getItem(storageKey(ws));
  } catch {
    return null;
  }
}

let picked: string | null = null;

// brandSearch is the query string that keeps the brand in the URL.
export function brandSearch(): string {
  if (state.brand) return `?brand=${encodeURIComponent(state.brand)}`;
  return state.brands.some((b) => b.status === "active") ? `?brand=${allBrands}` : "";
}

// syncURL keeps ?brand= in step and leaves a page's other parameters (the
// Logs page's filters) as they are. All brands is ?brand=all when the
// workspace has brands, so the link doesn't open on one.
function syncURL() {
  const p = new URLSearchParams(location.search);
  if (state.brand) p.set("brand", state.brand);
  else if (state.brands.some((b) => b.status === "active")) p.set("brand", allBrands);
  else p.delete("brand");
  const qs = p.toString();
  const url = location.pathname + (qs ? `?${qs}` : "") + location.hash;
  if (location.pathname + location.search + location.hash !== url) history.replaceState(history.state, "", url);
}

// loadBrands fetches the workspace's brands. With nothing picked yet, or a
// picked brand that is gone, it opens on the first active brand.
export async function loadBrands(ws: string) {
  if (state.ws !== ws) {
    picked = remembered(ws);
    set({ ws, brand: picked && picked !== allBrands ? picked : "", brands: [], loaded: false });
  }
  const { data } = await api.GET("/v1/workspaces/{ws}/brands", { params: { path: { ws }, query: { status: "all" } } });
  if (state.ws !== ws) return;
  const brands = data?.data ?? [];
  const active = brands.filter((b) => b.status === "active");
  let brand = active.some((b) => b.handle === state.brand) ? state.brand : "";
  if (!brand && picked !== allBrands && active.length > 0) brand = active[0].handle;
  set({ brands, brand, loaded: true });
  syncURL();
}

export function setBrand(handle: string) {
  if (handle === state.brand) return;
  set({ brand: handle });
  track("ui.brand_switched", { scope: handle ? "brand" : "all", brand_id: state.brands.find((b) => b.handle === handle)?.id });
  picked = handle || allBrands;
  try {
    localStorage.setItem(storageKey(state.ws), picked);
  } catch {
    // Private windows may refuse storage; the URL still holds the choice.
  }
  syncURL();
}

// useBrand gives the selected brand (undefined for all brands), every
// brand, and a lookup by id for badges.
export function useBrand(ws?: string) {
  const s = useSyncExternalStore(subscribe, () => state);
  useEffect(() => {
    if (ws && ws !== state.ws) void loadBrands(ws);
  }, [ws]);
  const current = s.brands.find((b) => b.handle === s.brand);
  return {
    brand: current,
    // The query to add to list calls: { brand: "repocket" } or {}.
    query: (current ? { brand: current.handle } : {}) as { brand?: string },
    brands: s.brands,
    active: s.brands.filter((b) => b.status === "active"),
    byId: (id?: string | null) => (id ? s.brands.find((b) => b.id === id) : undefined),
    loaded: s.loaded,
    reload: () => loadBrands(s.ws),
  };
}
