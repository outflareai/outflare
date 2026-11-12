// The number: what a new paying customer costs the brand over the range,
// against its target, with cost per sign-up, new customers and spend beside
// it and one plain sentence under it. Money is per currency and never added
// across currencies, so a brand with accounts in two currencies gets a row
// for each.

import { useEffect, useState } from "react";
import type { Tab } from "../../App";
import { api, errorMessage } from "../../api";
import { ask, Button, ErrorText, Stat, toast } from "../../ui";
import { Skeleton } from "@/components/ui/skeleton";
import { money, n } from "../Ads";
import { rangeOf, type AdEconomics, type AdPerformanceRow, type AdSetup, type Customers } from "./api";

type Summary = {
  currency: string;
  spend: number;
  signups: number;
  payers: number;
  costPerPayer: number | null;
  costPerSignup: number | null;
  previousCostPerPayer: number | null;
  previousPayers: number;
};

function sum(rows: AdPerformanceRow[]) {
  const by = new Map<string, { spend: number; signups: number; payers: number }>();
  for (const r of rows) {
    const t = by.get(r.currency) ?? { spend: 0, signups: 0, payers: 0 };
    t.spend += r.spend_micros;
    t.signups += r.signups;
    t.payers += r.new_payers;
    by.set(r.currency, t);
  }
  return by;
}
const per = (spend: number, count: number) => (count > 0 ? Math.round(spend / count) : null);

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

const signed = (v: number) => `${v > 0 ? "+" : ""}${Math.round(v * 100)}%`;

type Props = {
  brand: string;
  days: number;
  // end is a custom range's last day; without it the range ends today.
  end?: string;
  windowDays: number;
  customers: Customers;
  economics: AdEconomics | null;
  setup: AdSetup | null;
  canManage: boolean;
  reload: () => void;
  go: (tab: Tab, item?: string) => void;
  // Bumped by the shell after a sync, so the number is read again.
  version?: number;
};

export function Hero({ brand, days, end, windowDays, customers, economics, setup, canManage, reload, go, version = 0 }: Props) {
  const [rows, setRows] = useState<Summary[] | null>(null);
  const [notes, setNotes] = useState<string[]>([]);
  const [target, setTarget] = useState<{ payer: number | null; signup: number | null; currency: string }>({ payer: null, signup: null, currency: "" });
  const [error, setError] = useState("");

  useEffect(() => {
    let live = true;
    setRows(null);
    const range = rangeOf(days, end ? new Date(`${end}T12:00:00Z`) : undefined);
    const base = { level: "campaign" as const, window_days: windowDays, customers };
    Promise.all([
      api.GET("/v1/brands/{brand}/ads/performance", { params: { path: { brand }, query: { ...base, from: range.from, to: range.to } } }),
      api.GET("/v1/brands/{brand}/ads/performance", { params: { path: { brand }, query: { ...base, from: range.previous.from, to: range.previous.to } } }),
    ]).then(([now, before]) => {
      if (!live) return;
      if (now.error || !now.data) {
        setError(errorMessage(now.error));
        setRows([]);
        return;
      }
      setError("");
      setNotes(now.data.notes ?? []);
      setTarget({ payer: now.data.target_cost_per_payer_micros, signup: now.data.target_cost_per_signup_micros, currency: now.data.currency });
      const cur = sum(now.data.data);
      const prev = sum(before.data?.data ?? []);
      const out: Summary[] = [];
      for (const [currency, t] of cur) {
        const p = prev.get(currency);
        out.push({
          currency, spend: t.spend, signups: t.signups, payers: t.payers,
          costPerPayer: per(t.spend, t.payers), costPerSignup: per(t.spend, t.signups),
          previousCostPerPayer: p ? per(p.spend, p.payers) : null, previousPayers: p?.payers ?? 0,
        });
      }
      // The brand's own currency first, then the biggest spender.
      out.sort((a, b) => (a.currency === now.data!.currency ? -1 : b.currency === now.data!.currency ? 1 : b.spend - a.spend));
      setRows(out);
    });
    return () => {
      live = false;
    };
  }, [brand, days, end, windowDays, customers, version]);

  const targetPayer = economics?.target_cost_per_payer_micros ?? target.payer;
  const targetSignup = economics?.target_cost_per_signup_micros ?? target.signup;
  const currency = economics?.currency || target.currency || rows?.[0]?.currency || "USD";
  const stepsLeft = setup ? setup.steps.filter((s) => !s.done) : [];

  if (error && (!rows || rows.length === 0)) return <ErrorText>{error}</ErrorText>;
  if (!rows)
    return (
      <div aria-live="polite" className="grid gap-px overflow-hidden rounded-lg border bg-border shadow-xs sm:grid-cols-2 lg:grid-cols-[1.6fr_1fr_1fr_1fr]">
        <span className="sr-only">Loading…</span>
        {[0, 1, 2, 3].map((i) => <div key={i} className="bg-card p-4"><Skeleton className="h-3 w-28" /><Skeleton className={`mt-3 ${i === 0 ? "h-10 w-40" : "h-7 w-24"}`} /></div>)}
      </div>
    );

  const setTargetButton = canManage && (
    <Button variant={targetPayer ? "ghost" : "primary"} onClick={() => setPayerTarget(brand, currency, targetPayer, reload)}>{targetPayer ? "Change the target" : "Set your target"}</Button>
  );

  // Nothing spent: say which set-up step is missing, or that the range is
  // empty, rather than a row of dashes.
  if (rows.length === 0 || rows.every((r) => r.spend === 0 && r.payers === 0 && r.signups === 0)) {
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

  return (
    <div className="space-y-3">
      {error && <ErrorText>{error}</ErrorText>}
      {rows.map((r) => {
        const state = targetState(r.costPerPayer, targetPayer);
        const delta = r.costPerPayer !== null && r.previousCostPerPayer ? (r.costPerPayer - r.previousCostPerPayer) / r.previousCostPerPayer : null;
        const signupState = targetState(r.costPerSignup, targetSignup);
        const against = targetPayer ? `, ${state.tone === "green" ? "on" : state.tone === "amber" ? "under" : "over"} your ${money(targetPayer, r.currency)} target` : ", no target set yet";
        const sentence =
          r.payers > 0 && r.costPerPayer !== null
            ? `${n(r.payers)} new ${r.payers === 1 ? "customer" : "customers"} at ${money(r.costPerPayer, r.currency)} each${against}.`
            : stepsLeft.length > 0
              ? `No new customers yet: ${stepsLeft.length} of ${setup?.total ?? stepsLeft.length} set-up steps left (next: ${stepsLeft[0].title.toLowerCase()}).`
              : r.signups > 0
                ? `${n(r.signups)} sign-ups and no first payment yet within ${windowDays} days of a click.`
                : `${money(r.spend, r.currency)} spent and no sign-up tied to an ad click yet.`;
        return (
          <section key={r.currency} aria-label={`Cost per new paying customer${rows.length > 1 ? `, ${r.currency}` : ""}`} className="overflow-hidden rounded-lg border bg-card shadow-xs">
            <div className="grid gap-px bg-border sm:grid-cols-2 lg:grid-cols-[1.6fr_1fr_1fr_1fr]">
              <div className="min-w-0 bg-card px-4 py-3 sm:col-span-2 lg:col-span-1">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div className="truncate font-mono text-[11px] tracking-wide text-muted-foreground uppercase">Cost per new paying customer{rows.length > 1 && ` · ${r.currency}`}</div>
                  {state.word && (
                    <span className={`inline-flex items-center gap-1 font-mono text-[11px] ${state.tone === "red" ? "text-red-600 dark:text-red-400" : state.tone === "amber" ? "text-amber-600 dark:text-amber-400" : "text-green-600 dark:text-green-400"}`}>
                      <span aria-hidden="true">{state.tone === "red" ? "▲" : state.tone === "amber" ? "▼" : "●"}</span>{state.word}
                    </span>
                  )}
                </div>
                <div className={`mt-1 font-mono text-4xl font-medium tracking-tight tabular-nums ${state.tone === "red" ? "text-red-600 dark:text-red-400" : state.tone === "amber" ? "text-amber-600 dark:text-amber-400" : state.tone === "green" ? "text-green-600 dark:text-green-400" : ""}`} data-testid="cost-per-payer">
                  {money(r.costPerPayer, r.currency)}
                </div>
                <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-[12px] text-muted-foreground">
                  {targetPayer ? <span>Target {money(targetPayer, r.currency)}</span> : <span>No target yet</span>}
                  {delta !== null && (
                    <span className={delta < 0 ? "text-green-600 dark:text-green-400" : delta > 0 ? "text-red-600 dark:text-red-400" : ""} title={`${money(r.previousCostPerPayer, r.currency)} in the ${days} days before`}>
                      {signed(delta)} vs the {days} days before
                    </span>
                  )}
                  {delta === null && r.previousPayers === 0 && r.payers > 0 && <span>No new customers in the {days} days before</span>}
                </div>
              </div>
              <Stat label="Cost per sign-up" value={money(r.costPerSignup, r.currency)} tone={signupState.tone} sub={targetSignup ? `Target ${money(targetSignup, r.currency)}` : `${n(r.signups)} sign-ups`} />
              <Stat label="New customers" value={n(r.payers)} sub={`first payments within ${windowDays} days of a click`} />
              <Stat label="Spend" value={money(r.spend, r.currency)} sub={`last ${days} days`} />
            </div>
            <div className="flex flex-wrap items-center justify-between gap-2 border-t px-4 py-2.5">
              <p className="text-[13px]" data-testid="hero-sentence">{sentence}</p>
              <div className="flex flex-wrap gap-2">
                {stepsLeft.length > 0 && r.payers === 0 && <Button variant="ghost" onClick={() => go("ads", "setup")}>Continue set-up</Button>}
                {setTargetButton}
              </div>
            </div>
          </section>
        );
      })}
      {notes.map((x) => <p key={x} className="text-[12px] text-muted-foreground">{x}</p>)}
    </div>
  );
}
