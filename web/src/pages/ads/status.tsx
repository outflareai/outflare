import { useState, type ReactNode } from "react";
import type { TableFooterRow } from "@/components/data-table";
import type { Schemas } from "@outflare/sdk";
import { track } from "@/lib/analytics";

// What the ad tables share with Google Ads' own: its words for a status
// (Enabled, Paused, Removed), its three status filters with "All enabled"
// to start with, and its two total rows under a table, one for the rows
// the filter and search leave and one for everything.

type AdTotals = Schemas["AdTotals"];

export type StatusFilter = "enabled" | "not_removed" | "all";

const filters: { id: StatusFilter; label: string }[] = [
  { id: "enabled", label: "All enabled" },
  { id: "not_removed", label: "All but removed" },
  { id: "all", label: "All" },
];
const key = "outflare:ads-status-filter";

// adStatusWord is the status in Google Ads' words.
export function adStatusWord(status: string): string {
  return status === "active" ? "Enabled" : status === "paused" ? "Paused" : status === "removed" ? "Removed" : status ? status.charAt(0).toUpperCase() + status.slice(1) : "";
}
export const adStatusTone = (status: string) => (status === "active" ? "green" : status === "removed" ? "red" : "grey") as "green" | "red" | "grey";

// servingStatus is whether something enabled is really showing ads, in
// Google Ads' words (Eligible, Limited by budget, Learning), with the
// reasons to hover over. primary is the network's finer status and
// reasons why; both are empty when the network gives none, and the plain
// status is what shows then, as it does for anything paused or removed.
export function servingStatus(status: string, primary?: string, reasons?: string[] | string): { word: string; tone: "green" | "red" | "grey" | "amber" | "blue"; title?: string } {
  const plain = { word: adStatusWord(status), tone: adStatusTone(status) };
  if (status !== "active" || !primary) return plain;
  const why = (Array.isArray(reasons) ? reasons : (reasons ?? "").split(",")).map((r) => r.trim()).filter(Boolean);
  const say = (r: string) => reasonWords[r] ?? (r.charAt(0).toUpperCase() + r.slice(1)).replace(/_/g, " ");
  const title = why.length ? why.map(say).join(". ") + "." : undefined;
  switch (primary) {
    case "eligible": return { word: "Eligible", tone: "green", title };
    case "limited": return { word: why.includes("budget_constrained") ? "Limited by budget" : why.some((r) => r.startsWith("bidding_strategy")) ? "Limited by bidding" : why.some((r) => r.includes("policy") || r.includes("disapproved")) ? "Limited by policy" : "Limited", tone: "amber", title };
    case "learning": return { word: "Learning", tone: "blue", title: title ?? "Google is still learning how to bid. Results settle in a few days." };
    case "misconfigured": return { word: "Misconfigured", tone: "red", title };
    case "not_eligible": return { word: "Not eligible", tone: "red", title };
    case "pending": return { word: "Pending", tone: "grey", title };
    case "ended": return { word: "Ended", tone: "grey", title };
    case "paused": return { word: why.length ? say(why[0]) : "Not running", tone: "grey", title };
    default: return plain;
  }
}

const reasonWords: Record<string, string> = {
  budget_constrained: "The daily budget runs out before the day does",
  bidding_strategy_learning: "The bid strategy is still learning",
  bidding_strategy_limited: "The bid strategy is limited",
  bidding_strategy_constrained: "The bid target holds it back",
  bidding_strategy_misconfigured: "The bid strategy is set up wrong",
  budget_misconfigured: "The budget is set up wrong",
  campaign_paused: "Campaign paused",
  campaign_ended: "Campaign ended",
  campaign_pending: "Campaign hasn't started",
  campaign_removed: "Campaign removed",
  ad_group_paused: "Ad group paused",
  ad_groups_paused: "All ad groups paused",
  ad_group_ads_paused: "All ads paused",
  keywords_paused: "All keywords paused",
  no_keywords: "No keywords",
  no_ad_groups: "No ad groups",
  no_ad_group_ads: "No ads",
  has_ads_disapproved: "Some ads are disapproved",
  has_ads_limited_by_policy: "Some ads are limited by policy",
  most_ads_under_review: "Most ads are under review",
  ad_group_ad_disapproved: "Disapproved",
  ad_group_ad_approved_labeled: "Approved with limits",
  ad_group_ad_under_review: "Under review",
  ad_group_ad_poor_quality: "Poor ad strength",
};

export const keepStatus = (f: StatusFilter, status: string) => f === "all" || (f === "enabled" ? status === "active" : status !== "removed");

// useStatusFilter is the filter every ad table starts with: All enabled,
// or what was last picked in this browser.
export function useStatusFilter(): [StatusFilter, (f: StatusFilter) => void] {
  const [f, setF] = useState<StatusFilter>(() => {
    try {
      const v = localStorage.getItem(key);
      return filters.some((x) => x.id === v) ? (v as StatusFilter) : "enabled";
    } catch {
      return "enabled";
    }
  });
  return [
    f,
    (next) => {
      setF(next);
      try {
        localStorage.setItem(key, next);
      } catch {
        // Private windows: the choice lasts until the page is left.
      }
      track("ui.ads_status_filter_changed", { filter: next });
    },
  ];
}

// StatusFilterSelect is the dropdown beside a table's search box. hidden
// says how many rows the filter leaves out, so nobody wonders where a
// paused campaign went.
export function StatusFilterSelect({ value, onChange, hidden }: { value: StatusFilter; onChange: (f: StatusFilter) => void; hidden: number }) {
  return (
    <label className="flex items-center gap-1.5 text-[13px]">
      <span className="text-muted-foreground">Status</span>
      <select aria-label="Status" value={value} onChange={(e) => onChange(e.target.value as StatusFilter)} className="h-8 rounded-md border border-input bg-transparent px-2 text-[13px] shadow-xs dark:bg-input/30">
        {filters.map((x) => <option key={x.id} value={x.id}>{x.label}</option>)}
      </select>
      {hidden > 0 && <span className="text-[12px] text-muted-foreground">{hidden} not shown</span>}
    </label>
  );
}

const mono = (s: string) => <span className="font-mono tabular-nums">{s}</span>;

// metricFooter is the two total rows for a table of metricColumns: the
// rows shown, and all of them. Rows in another currency than the one that
// spent the most are left out of the money, and the label says so.
export function metricFooter<T>(what: string, all: T[], totals: (r: T) => AdTotals, currency: (r: T) => string, fmt: { money: (v: number | null, currency: string) => string; n: (v: number | null) => string }, whole = "account") {
  const spendBy = new Map<string, number>();
  for (const r of all) spendBy.set(currency(r), (spendBy.get(currency(r)) ?? 0) + totals(r).spend_micros);
  const main = [...spendBy.entries()].sort((a, b) => b[1] - a[1])[0]?.[0] ?? "USD";
  const mixed = spendBy.size > 1;
  const row = (label: string, rows: T[], testId: string): TableFooterRow => {
    const of = rows.filter((r) => currency(r) === main);
    const sum = (f: (t: AdTotals) => number | null | undefined) => of.reduce((a, r) => a + (f(totals(r)) ?? 0), 0);
    const spend = sum((t) => t.spend_micros), clicks = sum((t) => t.clicks), impressions = sum((t) => t.impressions), conv = sum((t) => t.conversions), allConv = sum((t) => t.all_conversions);
    const cells: Record<string, ReactNode> = {
      spend: mono(fmt.money(spend, main)),
      clicks: mono(fmt.n(clicks)),
      impressions: mono(fmt.n(impressions)),
      ctr: mono(impressions > 0 ? `${((clicks / impressions) * 100).toFixed(2)}%` : "—"),
      cpc: mono(clicks > 0 ? fmt.money(Math.round(spend / clicks), main) : "—"),
      conversions: mono(fmt.n(conv)),
      all_conversions: mono(fmt.n(allConv)),
      cpa: mono(conv > 0 ? fmt.money(Math.round(spend / conv), main) : "—"),
    };
    return { label: mixed ? `${label} (${main} only)` : label, testId, cells };
  };
  return (shown: T[]): TableFooterRow[] => [row(`Total: filtered ${what}`, shown, "total-filtered"), row(`Total: ${whole}`, all, "total-all")];
}
