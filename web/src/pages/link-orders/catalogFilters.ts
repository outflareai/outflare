import { pbnSignals } from "./pbnSignals";
import type { LinkOrderDonor, LinkOrderDonorMetrics } from "./shared";

// The catalog's filters, as one pure function so the table, the counts and
// the export all agree. Number fields are kept as the typed text; an empty
// field is no filter. A site with no value for a metric fails a minimum or
// maximum on it, so "Min referring domains 100" never shows sites the
// intermediary sent no numbers for.

export type Filters = {
  q: string;
  intermediary: string;
  country: string;
  geo: string;
  lang: string;
  niche: string;
  category: string;
  subcategory: string;
  articleType: string;
  drMin: string;
  drMax: string;
  priceMin: string;
  priceMax: string;
  trafficMin: string;
  trafficMax: string;
  valueMax: string;
  rdMin: string;
  kwMin: string;
  outgoingMax: string;
  addedSince: string;
  rel: string;
  duration: string;
  casino: string;
  pbn: "no" | "yes" | "any";
  direct: "any" | "yes" | "no";
  best: "any" | "yes";
  /** Signs of a PBN in the stored metrics: likely, none, or either. */
  pbnHint: "any" | "likely" | "none";
  /** The team's skip rules (traffic on few pages, too many links out). */
  verdict: "any" | "keep" | "skip" | "unknown";
};

export const defaultFilters: Filters = {
  q: "", intermediary: "", country: "", geo: "", lang: "", niche: "", category: "", subcategory: "", articleType: "",
  drMin: "", drMax: "", priceMin: "", priceMax: "", trafficMin: "", trafficMax: "", valueMax: "", rdMin: "", kwMin: "",
  outgoingMax: "", addedSince: "", rel: "", duration: "", casino: "", pbn: "no", direct: "any", best: "any", pbnHint: "any", verdict: "keep",
};

/** How many filters differ from the defaults, for the "Reset" button and the "More filters" label. */
export function activeFilterCount(f: Filters, keys: (keyof Filters)[] = Object.keys(defaultFilters) as (keyof Filters)[]): number {
  return keys.filter((k) => f[k] !== defaultFilters[k]).length;
}

/** The "More filters" fields, so their count can show on the closed section. */
export const moreFilterKeys: (keyof Filters)[] = [
  "geo", "subcategory", "articleType", "drMax", "priceMin", "trafficMax", "valueMax", "rdMin", "kwMin", "outgoingMax", "addedSince", "rel", "duration", "casino", "direct", "best", "pbnHint",
];

/** The cheapest price in dollars or euros (the catalog doesn't convert), or null. */
export function lowestPrice(d: Pick<LinkOrderDonor, "prices">): number | null {
  return d.prices.length ? Math.min(...d.prices.map((p) => p.amount_minor)) / 100 : null;
}

/** Price per 1,000 monthly organic visits: lower is better value. Null without a price or traffic. */
export function pricePerThousandVisits(d: Pick<LinkOrderDonor, "prices" | "traffic">): number | null {
  const price = lowestPrice(d);
  if (price == null || !d.traffic) return null;
  return price / (d.traffic / 1000);
}

function num(s: string): number | null {
  if (s.trim() === "") return null;
  const n = Number(s.replace(/[\s,]/g, ""));
  return Number.isFinite(n) ? n : null;
}

function metricNumber(d: LinkOrderDonor, key: keyof LinkOrderDonorMetrics): number | null {
  const v = d.metrics?.[key];
  return typeof v === "number" ? v : null;
}

function atLeast(v: number | null, min: number | null): boolean {
  return min == null || (v != null && v >= min);
}

function atMost(v: number | null, max: number | null): boolean {
  return max == null || (v != null && v <= max);
}

export function filterDonors(donors: LinkOrderDonor[], f: Filters): LinkOrderDonor[] {
  const q = f.q.trim().toLowerCase();
  const drMin = num(f.drMin), drMax = num(f.drMax);
  const priceMin = num(f.priceMin), priceMax = num(f.priceMax);
  const trafficMin = num(f.trafficMin), trafficMax = num(f.trafficMax);
  const valueMax = num(f.valueMax), rdMin = num(f.rdMin), kwMin = num(f.kwMin), outgoingMax = num(f.outgoingMax);
  return donors.filter((d) => {
    const m = d.metrics;
    if (q && !`${d.domain} ${d.niches.join(" ")} ${d.intermediary} ${d.theme} ${m?.main_category ?? ""} ${m?.subcategory ?? ""}`.toLowerCase().includes(q)) return false;
    if (f.intermediary && d.intermediary.toLowerCase() !== f.intermediary.toLowerCase()) return false;
    if (f.country && d.country !== f.country) return false;
    if (f.geo && m?.geo_top1 !== f.geo && m?.geo_top2 !== f.geo) return false;
    if (f.lang && !d.languages.includes(f.lang)) return false;
    if (f.niche && !d.niches.includes(f.niche)) return false;
    if (f.category && m?.main_category !== f.category) return false;
    if (f.subcategory && m?.subcategory !== f.subcategory) return false;
    if (f.articleType && (m?.article_type || "") !== (f.articleType === "-" ? "" : f.articleType)) return false;
    if (!atLeast(d.domain_rating ?? null, drMin) || !atMost(d.domain_rating ?? null, drMax)) return false;
    // A price filter passes when any of the site's prices is in range.
    if (priceMin != null || priceMax != null) {
      const ok = d.prices.some((p) => atLeast(p.amount_minor / 100, priceMin) && atMost(p.amount_minor / 100, priceMax));
      if (!ok) return false;
    }
    if (!atLeast(d.traffic ?? null, trafficMin) || !atMost(d.traffic ?? null, trafficMax)) return false;
    if (!atMost(pricePerThousandVisits(d), valueMax)) return false;
    if (!atLeast(metricNumber(d, "referring_domains"), rdMin)) return false;
    if (kwMin != null && !atLeast(metricNumber(d, "total_keywords") ?? metricNumber(d, "organic_keywords"), kwMin)) return false;
    if (!atMost(metricNumber(d, "outgoing_domains_followed"), outgoingMax)) return false;
    if (f.addedSince && !((m?.date_added ?? "") >= f.addedSince && /^\d{4}-\d{2}-\d{2}/.test(m?.date_added ?? ""))) return false;
    if (f.rel && !d.rel_offered.includes(f.rel)) return false;
    if (f.duration && d.duration !== f.duration) return false;
    if (f.casino === "yes" && !d.casino_allowed) return false;
    if (f.casino === "no" && d.casino_allowed) return false;
    if (f.pbn === "no" && d.pbn) return false;
    if (f.pbn === "yes" && !d.pbn) return false;
    if (f.direct === "yes" && !d.direct_contact.recommended) return false;
    if (f.direct === "no" && d.direct_contact.recommended) return false;
    if (f.best === "yes" && !d.best_link) return false;
    if (f.pbnHint !== "any" && pbnSignals(d).likely !== (f.pbnHint === "likely")) return false;
    if (f.verdict !== "any") {
      const v = d.quality?.verdict ?? "unknown";
      if (f.verdict === "keep" ? v === "skip" : v !== f.verdict) return false;
    }
    return true;
  });
}

/** The distinct values of a text metric, most common first, with their counts, for a filter's options. */
export function metricOptions(donors: LinkOrderDonor[], key: "main_category" | "subcategory" | "article_type" | "geo_top1", within?: (d: LinkOrderDonor) => boolean): { value: string; n: number }[] {
  const counts = new Map<string, number>();
  for (const d of donors) {
    if (within && !within(d)) continue;
    const v = d.metrics?.[key];
    if (typeof v === "string" && v) counts.set(v, (counts.get(v) ?? 0) + 1);
    if (key === "geo_top1") {
      const v2 = d.metrics?.geo_top2;
      if (typeof v2 === "string" && v2 && v2 !== v) counts.set(v2, (counts.get(v2) ?? 0) + 1);
    }
  }
  return [...counts.entries()].map(([value, n]) => ({ value, n })).sort((a, b) => b.n - a.n || a.value.localeCompare(b.value));
}
