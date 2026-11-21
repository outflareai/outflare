// The numbers: what a new paying customer costs the brand over the range,
// with cost per sign-up, new customers, spend, the revenue the ads brought
// and the return on the spend beside it (or the numbers the brand chose),
// each against the period before and its target, under one plain sentence. The API converts every account's money into
// the brand's currency, so there is one row; an account whose currency has
// no rate keeps its own and gets a row of its own.

import { useEffect, useState } from "react";
import type { Tab } from "../../App";
import { api, errorMessage } from "../../api";
import { track } from "../../lib/analytics";
import { ask, Button, ErrorText, toast } from "../../ui";
import { Skeleton } from "@/components/ui/skeleton";
import { money, n, scopeQuery, type AdScope } from "../Ads";
import { ChooseNumbers, noTotals, NumberTile, numbersGrid, shownNumbers, type Totals } from "./Numbers";
import { day, minorToMicros, rangeOf, type AdEconomics, type AdPerformanceDay, type AdPerformanceRow, type AdSetup, type Customers } from "./api";

type Summary = { currency: string; now: Totals; before: Totals | null };

// sum adds the rows up per currency (one, the brand's, once the API has
// converted). Revenue counts only amounts in that currency, as the API's
// roas does.
function sum(rows: AdPerformanceRow[]) {
  const by = new Map<string, Totals>();
  for (const r of rows) {
    const t = by.get(r.currency) ?? noTotals();
    t.spend += r.spend_micros;
    t.signups += r.signups;
    t.payers += r.new_payers;
    t.clicks += r.clicks;
    t.platform += r.platform_conversions ?? 0;
    for (const m of [...r.new_revenue, ...r.returning_revenue]) if (m.currency === r.currency) t.revenue += minorToMicros(m);
    by.set(r.currency, t);
  }
  return by;
}
const per = (spend: number, count: number) => (count > 0 ? Math.round(spend / count) : null);

// What the range is compared with: as many days straight before it, or the
// same days a year earlier. The choice is kept per brand in this browser.
export type Compare = "previous" | "year";
const compareKey = (brand: string) => `outflare.ads.compare.${brand}`;
const savedCompare = (brand: string): Compare => {
  try {
    return localStorage.getItem(compareKey(brand)) === "year" ? "year" : "previous";
  } catch {
    return "previous";
  }
};
const yearBefore = (date: string) => {
  const d = new Date(`${date}T12:00:00Z`);
  d.setUTCFullYear(d.getUTCFullYear() - 1);
  return day(d);
};

// Within a tenth of the target is on target; under it there is room to
// spend more; over it costs too much.
export function targetState(cost: number | null, target: number | null): { tone?: "green" | "amber" | "red"; word: string } {
  if (cost === null || target === null || target <= 0) return { word: "" };
  const r = cost / target;
  if (r > 1.1) return { tone: "red", word: "Over target" };
  if (r < 0.9) return { tone: "amber", word: "Under target" };
  return { tone: "green", word: "On target" };
}

// The target cost per new paying customer, set from the page. Nothing
// changes in the ad network: Outflare compares against it.
export async function setPayerTarget(brand: string, currency: string, current: number | null, done: () => void) {
  const r = await ask({
    title: "What may a new paying customer cost?",
    body: `In ${currency}. Every finding, verdict and report on this page is judged against it: a campaign within a tenth of it is on target. Nothing changes in your ad accounts.`,
    input: { label: "Target cost per new paying customer", type: "number", value: current ? (current / 1e6).toFixed(2) : "", placeholder: "45.00" },
    choices: [
      { label: "Cancel", value: "cancel", variant: "ghost" },
      ...(current ? [{ label: "Clear", value: "clear", variant: "ghost" as const }] : []),
      { label: "Set the target", value: "ok", variant: "primary" },
    ],
  });
  if (!r || r.choice === "cancel") return;
  const v = Number(r.value);
  if (r.choice === "ok" && !(v > 0)) return toast("Enter an amount above zero.", "error");
  const { error } = await api.PUT("/v1/brands/{brand}/ads/economics", {
    params: { path: { brand } },
    body: { currency, target_cost_per_payer_micros: r.choice === "clear" ? null : Math.round(v * 1e6) },
  });
  if (error) return toast(errorMessage(error), "error");
  toast(r.choice === "clear" ? "Target cleared." : "Target set. Every number on this page is judged against it from now on.");
  done();
}

// setBudgetCeiling asks for the most the brand's active daily budgets may
// add up to. A raise past it is refused until a person says to go ahead.
export async function setBudgetCeiling(brand: string, currency: string, current: number | null, done: () => void) {
  const r = await ask({
    title: "What is the most your ads may spend in a day?",
    body: `In ${currency}. The daily budgets of your running campaigns together can't be raised past it without you confirming a second time. Nothing changes in your ad accounts.`,
    input: { label: "Total daily budget ceiling", type: "number", value: current ? (current / 1e6).toFixed(2) : "", placeholder: "150.00" },
    choices: [
      { label: "Cancel", value: "cancel", variant: "ghost" },
      ...(current ? [{ label: "Remove the ceiling", value: "clear", variant: "ghost" as const }] : []),
      { label: "Set the ceiling", value: "ok", variant: "primary" },
    ],
  });
  if (!r || r.choice === "cancel") return;
  const v = Number(r.value);
  if (r.choice === "ok" && !(v > 0)) return toast("Enter an amount above zero.", "error");
  const { error } = await api.PUT("/v1/brands/{brand}/ads/economics", {
    params: { path: { brand } },
    body: { currency, daily_budget_ceiling_micros: r.choice === "clear" ? null : Math.round(v * 1e6) },
  });
  if (error) return toast(errorMessage(error), "error");
  toast(r.choice === "clear" ? "Ceiling removed." : "Ceiling set.");
  done();
}

type Props = {
  brand: string;
  days: number;
  // end is a custom range's last day; without it the range ends today.
  end?: string;
  windowDays: number;
  customers: Customers;
  // The network or account picked at the top of the page: every number
  // here is that one's alone.
  scope?: AdScope;
  economics: AdEconomics | null;
  setup: AdSetup | null;
  canManage: boolean;
  reload: () => void;
  go: (tab: Tab, item?: string) => void;
  // Bumped by the shell after a sync, so the number is read again.
  version?: number;
  // The range's campaign rows, handed up so Overview's other tables read
  // the same numbers; and how many actions wait, for the link to them.
  // previous is the compared period's days, moved onto the range's own
  // dates so the two can be drawn over each other.
  onRows?: (rows: AdPerformanceRow[], daily?: AdPerformanceDay[], previous?: AdPerformanceDay[]) => void;
  toDo?: number;
};

export function Hero({ brand, days, end, windowDays, customers, scope, economics, setup, canManage, reload, go, version = 0, onRows, toDo = 0 }: Props) {
  const [rows, setRows] = useState<Summary[] | null>(null);
  const [notes, setNotes] = useState<string[]>([]);
  const [target, setTarget] = useState<{ payer: number | null; signup: number | null; currency: string }>({ payer: null, signup: null, currency: "" });
  const [error, setError] = useState("");
  const [daily, setDaily] = useState<AdPerformanceDay[]>([]);
  const [compare, setCompare] = useState<Compare>(() => savedCompare(brand));
  const [choosing, setChoosing] = useState(false);
  const range = rangeOf(days, end ? new Date(`${end}T12:00:00Z`) : undefined);
  const before = compare === "year" ? { from: yearBefore(range.from), to: yearBefore(range.to) } : range.previous;
  const beforeWords = compare === "year" ? "the same days a year before" : `the ${days} days before`;
  const pickCompare = (v: Compare) => {
    setCompare(v);
    try {
      localStorage.setItem(compareKey(brand), v);
    } catch {
      // The choice lasts for this visit.
    }
    track("ui.ads_compare_changed", { compare: v });
  };

  useEffect(() => setCompare(savedCompare(brand)), [brand]);

  useEffect(() => {
    let live = true;
    setRows(null);
    const base = { level: "campaign" as const, window_days: windowDays, customers, ...scopeQuery(scope ?? {}) };
    Promise.all([
      api.GET("/v1/brands/{brand}/ads/performance", { params: { path: { brand }, query: { ...base, from: range.from, to: range.to } } }),
      api.GET("/v1/brands/{brand}/ads/performance", { params: { path: { brand }, query: { ...base, from: before.from, to: before.to } } }),
    ]).then(([now, was]) => {
      if (!live) return;
      if (now.error || !now.data) {
        setError(errorMessage(now.error));
        setRows([]);
        onRows?.([]);
        return;
      }
      setError("");
      setNotes(now.data.notes ?? []);
      // The compared days, on the range's own dates.
      const shift = Date.parse(`${range.from}T00:00:00Z`) - Date.parse(`${before.from}T00:00:00Z`);
      const moved = (was.data?.daily ?? []).map((d) => ({ ...d, day: day(new Date(Date.parse(`${d.day}T00:00:00Z`) + shift)) }));
      onRows?.(now.data.data, now.data.daily, moved);
      setDaily(now.data.daily ?? []);
      setTarget({ payer: now.data.target_cost_per_payer_micros, signup: now.data.target_cost_per_signup_micros, currency: now.data.currency });
      const cur = sum(now.data.data);
      const prev = was.data ? sum(was.data.data) : null;
      const out: Summary[] = [];
      for (const [currency, t] of cur) out.push({ currency, now: t, before: prev ? (prev.get(currency) ?? noTotals()) : null });
      // The brand's own currency first, then the biggest spender.
      out.sort((a, b) => (a.currency === now.data!.currency ? -1 : b.currency === now.data!.currency ? 1 : b.now.spend - a.now.spend));
      setRows(out);
    });
    return () => {
      live = false;
    };
  }, [brand, days, end, windowDays, customers, version, scope?.network, scope?.account, compare]); // eslint-disable-line react-hooks/exhaustive-deps

  const targetPayer = economics?.target_cost_per_payer_micros ?? target.payer;
  const currency = economics?.currency || target.currency || rows?.[0]?.currency || "USD";
  const stepsLeft = setup ? setup.steps.filter((s) => !s.done) : [];

  if (error && (!rows || rows.length === 0)) return <ErrorText>{error}</ErrorText>;
  if (!rows)
    return (
      <div aria-live="polite" className={`overflow-hidden rounded-lg border shadow-xs ${numbersGrid(6)}`}>
        <span className="sr-only">Loading…</span>
        {[0, 1, 2, 3, 4, 5].map((i) => <div key={i} className="bg-card p-4"><Skeleton className="h-3 w-28" /><Skeleton className="mt-3 h-7 w-24" /><Skeleton className="mt-3 h-3 w-32" /></div>)}
      </div>
    );

  const setTargetButton = canManage && (
    <Button variant={targetPayer ? "ghost" : "primary"} onClick={() => setPayerTarget(brand, currency, targetPayer, reload)}>{targetPayer ? "Change the target" : "Set your target"}</Button>
  );

  // Nothing spent: say which set-up step is missing, or that the range is
  // empty, rather than a row of dashes.
  if (rows.length === 0 || rows.every((r) => r.now.spend === 0 && r.now.payers === 0 && r.now.signups === 0)) {
    const next = stepsLeft[0];
    return (
      <section aria-label="Cost per new paying customer" className="rounded-lg border bg-card p-4 shadow-xs">
        <div className="font-mono text-[11px] tracking-wide text-muted-foreground uppercase">Cost per new paying customer</div>
        <div className="mt-1 font-mono text-3xl font-medium tracking-tight text-muted-foreground">—</div>
        <p className="mt-2 text-[13px]">
          {next
            ? <>No ad spend to measure yet: {stepsLeft.length} of {setup?.total ?? stepsLeft.length} set-up steps left. Next: <span className="font-medium">{next.title}</span>.</>
            : <>Nothing spent in the last {days} days. Numbers arrive with the next sync of your ad accounts.</>}
        </p>
        <div className="mt-3 flex flex-wrap gap-2">
          {next && <Button onClick={() => go("ads", "setup")}>Continue set-up</Button>}
          {!targetPayer && setTargetButton}
        </div>
        {notes.map((x) => <p key={x} className="mt-2 text-[12px] text-muted-foreground">{x}</p>)}
      </section>
    );
  }

  const shown = shownNumbers(economics);
  const chooser = canManage && <ChooseNumbers brand={brand} economics={economics} currency={currency} open={choosing} setOpen={setChoosing} saved={reload} />;
  return (
    <div className="space-y-3">
      {error && <ErrorText>{error}</ErrorText>}
      {rows.map((r, at) => {
        const cost = per(r.now.spend, r.now.payers);
        const state = targetState(cost, targetPayer);
        const against = targetPayer ? `, ${state.tone === "green" ? "on" : state.tone === "amber" ? "under" : "over"} your ${money(targetPayer, r.currency)} target` : ", no target set yet";
        const sentence =
          r.now.payers > 0 && cost !== null
            ? `${n(r.now.payers)} new ${r.now.payers === 1 ? "customer" : "customers"} at ${money(cost, r.currency)} each${against}.`
            : stepsLeft.length > 0
              ? `No new customers yet: ${stepsLeft.length} of ${setup?.total ?? stepsLeft.length} set-up steps left (next: ${stepsLeft[0].title.toLowerCase()}).`
              : r.now.signups > 0
                ? `${n(r.now.signups)} sign-ups and no first payment yet within ${windowDays} days of a click.`
                : `${money(r.now.spend, r.currency)} spent and no sign-up tied to an ad click yet.`;
        return (
          <section key={r.currency} aria-label={`Cost per new paying customer${rows.length > 1 ? `, ${r.currency}` : ""}`} className="overflow-hidden rounded-lg border bg-card shadow-xs">
            <div className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-3">
              <h2 className="text-[15px] font-semibold" data-testid="hero-sentence">{sentence}</h2>
              <div className="flex flex-wrap items-center gap-2">
                {toDo > 0 && <Button onClick={() => go("ads", "actions")}>See the {toDo} recommended {toDo === 1 ? "action" : "actions"}</Button>}
                {stepsLeft.length > 0 && r.now.payers === 0 && <Button variant="ghost" onClick={() => go("ads", "setup")}>Continue set-up</Button>}
                {at === 0 && (
                  <label className="flex items-center gap-1.5 text-[12px] text-muted-foreground">
                    Compared with
                    <select className="h-8 rounded-md border bg-background px-2 text-[12.5px] text-foreground" aria-label="Compared with" data-testid="ads-compare" value={compare} onChange={(e) => pickCompare(e.target.value as Compare)}>
                      <option value="previous">the {days} days before</option>
                      <option value="year">the same days a year before</option>
                    </select>
                  </label>
                )}
                {!targetPayer && setTargetButton}
                {at === 0 && chooser}
              </div>
            </div>
            <div className={numbersGrid(shown.length)}>
              {shown.map((id) => (
                <NumberTile
                  key={id} id={id} now={r.now} before={r.before} beforeWords={beforeWords} economics={economics} days={days} currency={r.currency}
                  suffix={rows.length > 1 ? ` · ${r.currency}` : ""} daily={daily.filter((d) => d.currency === r.currency)} from={range.from} to={range.to}
                  setTarget={canManage && at === 0 ? () => setChoosing(true) : undefined}
                />
              ))}
            </div>
          </section>
        );
      })}
      {notes.map((x) => <p key={x} className="text-[12px] text-muted-foreground">{x}</p>)}
    </div>
  );
}
