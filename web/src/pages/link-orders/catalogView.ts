import { pricePerThousandVisits } from "./catalogFilters";
import { metricFields, type LinkOrderDonor, type LinkOrderDonorMetrics } from "./shared";

// How the catalog table is shown: which intermediary metrics have a
// column, how rows are sorted, and how many rows are drawn at once. A
// broker's full database is 11,000+ sites, so the table draws a page of
// rows and grows on request instead of rendering every row and column.

export type MetricKey = keyof LinkOrderDonorMetrics;

/** Rows drawn at first and added by "Show more". Filters and sorting apply to the whole catalog. */
export const CATALOG_PAGE = 200;

/** Every intermediary metric has a column by default; people hide the ones they don't need. */
export const defaultMetricColumns: MetricKey[] = metricFields.map((m) => m.key);

const storageKey = "outflare.link-orders.catalog-columns";

/** The metric columns this browser last chose, or every metric. Storage can be missing or blocked. */
export function loadMetricColumns(): MetricKey[] {
  try {
    const raw = window.localStorage.getItem(storageKey);
    if (!raw) return defaultMetricColumns;
    const keys = JSON.parse(raw);
    if (!Array.isArray(keys)) return defaultMetricColumns;
    const known = new Set<string>(defaultMetricColumns);
    return defaultMetricColumns.filter((k) => keys.includes(k) && known.has(k));
  } catch {
    return defaultMetricColumns;
  }
}

export function saveMetricColumns(keys: MetricKey[]) {
  try {
    window.localStorage.setItem(storageKey, JSON.stringify(keys));
  } catch {
    // A per-browser convenience only.
  }
}

export type SortKey = "site" | "dr" | "traffic" | "price" | "value" | MetricKey;
export type Sort = { key: SortKey; dir: 1 | -1 } | null;

function sortValue(d: LinkOrderDonor, key: SortKey): number | string | null {
  switch (key) {
    case "site":
      return d.domain;
    case "dr":
      return d.domain_rating ?? null;
    case "traffic":
      return d.traffic ?? null;
    case "price":
      return d.prices.length ? Math.min(...d.prices.map((p) => p.amount_minor)) : null;
    case "value":
      return pricePerThousandVisits(d);
    default: {
      const v = d.metrics?.[key];
      return v == null || v === "" ? null : (v as number | string);
    }
  }
}

/** Sorted copy of the rows. Rows with no value always go last, whichever way the column is sorted. */
export function sortDonors(list: LinkOrderDonor[], sort: Sort): LinkOrderDonor[] {
  // With no sort chosen, sites labelled Best link come first, in catalog order.
  if (!sort) return [...list.filter((d) => d.best_link), ...list.filter((d) => !d.best_link)];
  const { key, dir } = sort;
  return [...list].sort((a, b) => {
    const x = sortValue(a, key);
    const y = sortValue(b, key);
    if (x == null && y == null) return 0;
    if (x == null) return 1;
    if (y == null) return -1;
    if (typeof x === "number" && typeof y === "number") return (x - y) * dir;
    return String(x).localeCompare(String(y), undefined, { numeric: true }) * dir;
  });
}

/** Numbers and higher-is-interesting columns sort high to low first; text sorts A to Z first. */
export function nextSort(cur: Sort, key: SortKey, textual: boolean): Sort {
  const first: 1 | -1 = textual ? 1 : -1;
  if (!cur || cur.key !== key) return { key, dir: first };
  if (cur.dir === first) return { key, dir: (first * -1) as 1 | -1 };
  return null;
}

/** A guess at the intermediary from an export's file name ("AcmeLinks_export.csv" is AcmeLinks). */
export function intermediaryFromFileName(name: string): string {
  const base = name.replace(/\.[^.]+$/, "").split(/[_\s]+/)[0] ?? "";
  return /^[\p{L}\p{N}][\p{L}\p{N} .&'-]{1,60}$/u.test(base) && !/^(export|catalog|link-order-catalog|sites|data)$/i.test(base) ? base : "";
}

/** The sorts offered in the "Sort by" list, besides clicking a column heading. */
export const sortChoices: { label: string; sort: NonNullable<Sort> }[] = [
  { label: "Best value: lowest price per 1,000 visits", sort: { key: "value", dir: 1 } },
  { label: "Price: low to high", sort: { key: "price", dir: 1 } },
  { label: "Price: high to low", sort: { key: "price", dir: -1 } },
  { label: "DR: high to low", sort: { key: "dr", dir: -1 } },
  { label: "DR: low to high", sort: { key: "dr", dir: 1 } },
  { label: "Traffic: high to low", sort: { key: "traffic", dir: -1 } },
  { label: "Referring domains: most first", sort: { key: "referring_domains", dir: -1 } },
  { label: "Total keywords: most first", sort: { key: "total_keywords", dir: -1 } },
  { label: "Outgoing domains: fewest first", sort: { key: "outgoing_domains_followed", dir: 1 } },
  { label: "Newest added first", sort: { key: "date_added", dir: -1 } },
  { label: "Site: A to Z", sort: { key: "site", dir: 1 } },
];

/** The "Sort by" option that matches the current sort, or "" (none, or a column sort with no named option). */
export function sortChoiceIndex(sort: Sort): string {
  if (!sort) return "";
  const i = sortChoices.findIndex((c) => c.sort.key === sort.key && c.sort.dir === sort.dir);
  return i < 0 ? "custom" : String(i);
}
