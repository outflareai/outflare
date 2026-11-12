// What the Paid ads screens share: the words and money helpers that score
// everything in cost per new paying customer (plans/paid-ads/plan.md,
// "Vocabulary"), the finding type the decision inbox reads, and the two
// calls whose routes another workstream is adding to the API.

import { useCallback, useEffect, useState } from "react";
import type { AdSuggestion, Schemas } from "@outflare/sdk";

// The paid ads types the SDK does not yet export by name.
export type AdEconomics = Schemas["AdEconomics"];
export type AdPerformanceRow = Schemas["AdPerformanceRow"];
export type AdAcquisition = Schemas["AdAcquisition"];
export type AdSetup = Schemas["AdSetup"];
export type BrandKnowledgeSheet = Schemas["BrandKnowledgeSheet"];
import { api, errorMessage } from "../../api";

export type MinorMoney = Schemas["MinorMoney"];
export type Customers = "all" | "new" | "returning";
export type Level = AdPerformanceRow["level"];

export const windows = [7, 30, 90] as const;
export const customerBuckets: { id: Customers; label: string }[] = [
  { id: "all", label: "All" },
  { id: "new", label: "New" },
  { id: "returning", label: "Returning" },
];

// The day as the API wants it, and the range of the last `days` days ending
// today, with the range of as many days before it.
export const day = (d: Date) => d.toISOString().slice(0, 10);
export function rangeOf(days: number, endingAt = new Date()) {
  const to = endingAt;
  const from = new Date(to.getTime() - (days - 1) * 86_400_000);
  const prevTo = new Date(from.getTime() - 86_400_000);
  const prevFrom = new Date(prevTo.getTime() - (days - 1) * 86_400_000);
  return { from: day(from), to: day(to), previous: { from: day(prevFrom), to: day(prevTo) } };
}

// Outflare's own money is in minor units (cents), one entry per currency;
// the networks' spend is in micros. Both are shown per currency and never
// added across currencies.
export const minorToMicros = (m: MinorMoney) => m.amount_minor * 10_000;
export const minorList = (list: MinorMoney[] | undefined, money: (micros: number | null | undefined, currency: string) => string) =>
  list && list.length > 0 ? list.map((m) => money(minorToMicros(m), m.currency)).join(" · ") : "—";

// A finding is one row in the decision queue: a proposed change, an alert,
// or an automatic change that was made. The queue gains these fields from
// the verdicts workstream (plans/paid-ads/plan.md, migration 0499); they
// are optional here so the page compiles before and after the SDK has them.
export type Autonomy = "auto" | "approve" | "alert";
export type Finding = AdSuggestion & {
  autonomy?: Autonomy;
  impact_micros?: number | null;
  unit?: "payers" | "signups" | "platform_conversions";
  shadow?: boolean;
  applied_auto?: boolean;
  reverted_at?: string | null;
  rule?: string;
};
// The status a finding is in, read as text because the queue has statuses
// the SDK's enum does not list yet (applied, acknowledged, shadow).
export const statusOf = (f: Finding): string => f.status as string;
export const isAlert = (f: Finding) => f.autonomy === "alert" || String(f.kind).startsWith("alert.");
export const isShadow = (f: Finding) => f.shadow === true || statusOf(f) === "shadow";
export const isDone = (f: Finding) => f.applied_auto === true && !f.reverted_at && !isShadow(f) && ["applied", "measuring", "judged"].includes(statusOf(f));
export const isOpen = (f: Finding) => statusOf(f) === "open" && !isShadow(f) && !isAlert(f) && !f.applied_auto;
export const isJudged = (f: Finding) => statusOf(f) === "judged";
export const isMeasuring = (f: Finding) => (statusOf(f) === "measuring" || statusOf(f) === "testing") && !f.applied_auto;

// Every lever waits for a person; levers that can only spend less may
// apply on their own only where Outflare Cloud allows it. Anything that
// can spend more is confirmed here before it is sent (plans/paid-ads/plan.md,
// "Lever catalogue").
export const spendsMore = (kind: string) => ["budget_up", "resume", "keyword_add", "target_loosen", "bidding_target", "draft_ad", "draft_ad_group", "draft_campaign", "experiment_start"].includes(kind);

// What each lever is called on screen. Kinds the catalogue adds later fall
// back to their own name in words.
const leverWords: Record<string, string> = {
  budget_up: "Raise the budget", budget_down: "Lower the budget", pause: "Pause the campaign", resume: "Resume the campaign",
  negative_add: "Add a negative keyword", keyword_add: "Add a keyword", keyword_pause: "Pause a keyword", keyword_split: "Give a keyword its own ad group",
  ad_pause: "Pause an ad", target_tighten: "Tighten the target", target_loosen: "Loosen the target", bidding_target: "Bid to a target",
  exclusion_add: "Add an exclusion", consolidate: "Merge ad groups", draft_ad: "New ad", draft_ad_group: "New ad group", draft_campaign: "New campaign",
  experiment_start: "Start a test", overspend_pause: "Paused for overspending",
};
export const leverWord = (kind: string) => leverWords[kind] ?? (kind.startsWith("alert.") ? "Alert" : kind.charAt(0).toUpperCase() + kind.slice(1).replace(/_/g, " "));

// The word for what a finding counts in, in the unit its evidence is in.
export const unitWord = (unit: Finding["unit"], count: number) =>
  unit === "payers" ? (count === 1 ? "new customer" : "new customers") : unit === "signups" ? (count === 1 ? "sign-up" : "sign-ups") : count === 1 ? "platform conversion" : "platform conversions";

// The brand's economics and set-up path, loaded once for the page and read
// by the hero, the inbox and the set-up screen.
export function useBrandAds(brand: string) {
  const [economics, setEconomics] = useState<AdEconomics | null>(null);
  const [setup, setSetup] = useState<AdSetup | null>(null);
  const [error, setError] = useState("");
  const [loaded, setLoaded] = useState(false);
  const reload = useCallback(async () => {
    if (!brand) return;
    const path = { params: { path: { brand } } };
    const [e, s] = await Promise.all([api.GET("/v1/brands/{brand}/ads/economics", path), api.GET("/v1/brands/{brand}/ads/setup", path)]);
    setError(e.error || s.error ? errorMessage(e.error ?? s.error) : "");
    if (e.data) setEconomics(e.data);
    if (s.data) setSetup(s.data);
    setLoaded(true);
  }, [brand]);
  useEffect(() => {
    setLoaded(false);
    setEconomics(null);
    setSetup(null);
    reload();
  }, [reload]);
  return { economics, setup, error, loaded, reload };
}

// Closing an alert and undoing an applied finding (docs/paid-media.md, "Findings and decisions").
export async function acknowledgeFinding(brand: string, id: string) {
  return api.POST("/v1/brands/{brand}/ads/findings/{id}/acknowledge", { params: { path: { brand, id } } });
}
export async function revertFinding(brand: string, id: string) {
  return api.POST("/v1/brands/{brand}/ads/findings/{id}/revert", { params: { path: { brand, id } } });
}
