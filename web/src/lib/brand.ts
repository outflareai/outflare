// The brand the app is narrowed to, for companies that market several
// brands in one workspace. Like a project in PostHog, one brand is shown at
// a time: a workspace with brands opens on the one picked last, or its
// first. "" is all brands, which someone picks on purpose. The choice is
// kept per person and workspace, and in the URL (/acme/repocket/seo, or
// /acme/all/seo) so a shared link opens on the same brand.
//
// Every workspace read the app sends carries the brand picked (api.ts), so
// a page can't show another brand's records by forgetting it; a call that
// needs every brand says brand: "all". Pages still read it with useBrand()
// to reload when it changes and to show brand badges.

import { useEffect, useSyncExternalStore } from "react";
import type { Brand } from "@outflare/sdk";
import { api, setRequestBrand } from "../api";
import { track } from "./analytics";
import { parsePath, setBrandPart } from "./paths";

type State = { ws: string; brand: string; brands: Brand[]; loaded: boolean };

let state: State = { ws: "", brand: "", brands: [], loaded: false };
const listeners = new Set<() => void>();

function set(next: Partial<State>) {
  state = { ...state, ...next };
  setRequestBrand(state.loaded ? state.brand : "");
  setBrandPart(state.brand || (state.brands.some((b) => b.status === "active") ? allBrands : ""));
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
// nothing was picked yet. The URL's brand comes first, then an old link's
// ?brand=, then this browser's last pick.
function remembered(ws: string): string | null {
  const fromPath = parsePath(location.pathname).brand;
  if (fromPath) return fromPath;
  const fromURL = new URLSearchParams(location.search).get("brand");
  if (fromURL !== null) return fromURL || allBrands;
  try {
    return localStorage.getItem(storageKey(ws));
  } catch {
    return null;
  }
}

let picked: string | null = null;

// syncURL drops an old link's ?brand=, which the path now holds, and
// leaves a page's other parameters (the Logs page's filters) as they are.
// The shell puts the brand in the path (appPath).
function syncURL() {
  const p = new URLSearchParams(location.search);
  if (!p.has("brand")) return;
  p.delete("brand");
  const qs = p.toString();
  history.replaceState(history.state, "", location.pathname + (qs ? `?${qs}` : "") + location.hash);
}

// loadBrands fetches the workspace's brands. With nothing picked yet, or a
// picked brand that is gone, it opens on the first active brand.
export async function loadBrands(ws: string) {
  if (state.ws !== ws) {
    picked = remembered(ws);
    set({ ws, brand: picked && picked !== allBrands ? picked : "", brands: [], loaded: false });
  }
  let brands: Brand[] = [];
  try {
    const { data } = await api.GET("/v1/workspaces/{ws}/brands", { params: { path: { ws }, query: { status: "all" } } });
    brands = data?.data ?? [];
  } catch {
    // Offline: carry on with no brands rather than wait forever.
  }
  if (state.ws !== ws) return;
  const active = brands.filter((b) => b.status === "active");
  let brand = active.some((b) => b.handle === state.brand) ? state.brand : "";
  if (!brand && picked !== allBrands && active.length > 0) brand = active[0].handle;
  set({ brands, brand, loaded: true });
  syncURL();
}

// setBrand picks a brand ("" for all brands). from says how: the switcher,
// or a link (Back and Forward) that names another brand.
export function setBrand(handle: string, from: "switcher" | "link" = "switcher") {
  if (handle === allBrands) handle = "";
  if (handle === state.brand) return;
  if (from === "link" && handle && !state.brands.some((b) => b.handle === handle && b.status === "active")) return;
  set({ brand: handle });
  track("ui.brand_switched", { scope: handle ? "brand" : "all", brand_id: state.brands.find((b) => b.handle === handle)?.id, from });
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
