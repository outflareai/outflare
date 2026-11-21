import { useEffect, useSyncExternalStore } from "react";
import type { components } from "@outflare/sdk";
import { api } from "@/api";

// How a brand's People are set up (who it sells to, whether its product has
// customer accounts), shared by the menu, the People list and CRM settings,
// so a change in settings shows everywhere at once.
export type PeopleSetup = components["schemas"]["PeopleSetup"];

const cache = new Map<string, PeopleSetup>();
const loading = new Set<string>();
const listeners = new Set<() => void>();
let version = 0;
const changed = () => {
  version++;
  listeners.forEach((l) => l());
};

async function load(brand: string) {
  if (loading.has(brand)) return;
  loading.add(brand);
  try {
    const { data } = await api.GET("/v1/brands/{brand}/people-setup", { params: { path: { brand } } });
    if (data) {
      cache.set(brand, data);
      changed();
    }
  } finally {
    loading.delete(brand);
  }
}

// setPeopleSetup stores what a save returned.
export function setPeopleSetup(brand: string, setup: PeopleSetup) {
  cache.set(brand, setup);
  changed();
}

// usePeopleSetup is the brand's setup, undefined until it loads.
export function usePeopleSetup(brand?: string): PeopleSetup | undefined {
  useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => version,
  );
  useEffect(() => {
    if (brand && !cache.has(brand)) void load(brand);
  }, [brand]);
  return brand ? cache.get(brand) : undefined;
}
