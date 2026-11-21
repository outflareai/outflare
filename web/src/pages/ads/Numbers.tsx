// The Overview's numbers: each one against the period it is compared with
// (the change and the old number), against the brand's own target (a
// coloured word and bar), with the range drawn small under it. A brand
// chooses which numbers show and in what order, and sets the targets, in
// one panel with Save and Cancel.

import { useState } from "react";
import { ArrowDown, ArrowUp, GripVertical } from "lucide-react";
import { api, errorMessage } from "../../api";
import { track } from "../../lib/analytics";
import { Button, toast } from "../../ui";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { money, n } from "../Ads";
import { roasText, type AdEconomics, type AdPerformanceDay } from "./api";

export type Totals = { spend: number; signups: number; payers: number; revenue: number; clicks: number; platform: number };
export const noTotals = (): Totals => ({ spend: 0, signups: 0, payers: 0, revenue: 0, clicks: 0, platform: 0 });

export type NumberId = AdEconomics["overview_numbers"][number];
export const usualNumbers: NumberId[] = ["cost_per_payer", "cost_per_signup", "new_payers", "spend", "revenue", "roas"];
const mostNumbers = 8;

const over = (a: number, b: number) => (b > 0 ? a / b : null);
type Tone = "green" | "amber" | "red";
type Judged = { tone?: Tone; word: string; target: number | null; text: string; note?: string };

type Def = {
  id: NumberId;
  label: string;
  // What the number is, for the tile's title and the panel.
  help: string;
  value: (t: Totals) => number | null;
  text: (v: number | null, currency: string) => string;
  // Which way is good news: a cost going down, customers going up. Spend
  // going up or down is neither.
  better: "lower" | "higher" | null;
  // The brand's target for it, as stored, and how the panel asks for it.
  target?: { field: keyof AdEconomics; unit: "money" | "count" | "times"; monthly: boolean; ask: string };
};

const count = (v: number | null) => n(v);
const cash = (v: number | null, currency: string) => money(v === null ? null : Math.round(v), currency);

export const numberDefs: Def[] = [
  { id: "cost_per_payer", label: "Cost per new customer", help: "Spend divided by the new paying customers an ad click brought.", value: (t) => over(t.spend, t.payers), text: cash, better: "lower",
    target: { field: "target_cost_per_payer_micros", unit: "money", monthly: false, ask: "What a new customer may cost" } },
  { id: "cost_per_signup", label: "Cost per sign-up", help: "Spend divided by the sign-ups an ad click brought.", value: (t) => over(t.spend, t.signups), text: cash, better: "lower",
    target: { field: "target_cost_per_signup_micros", unit: "money", monthly: false, ask: "What a sign-up may cost" } },
  { id: "new_payers", label: "New customers", help: "People whose first payment came within the window after an ad click.", value: (t) => t.payers, text: count, better: "higher",
    target: { field: "target_new_payers_monthly", unit: "count", monthly: true, ask: "New customers a month" } },
  { id: "spend", label: "Spend", help: "What the ad networks charged.", value: (t) => t.spend, text: cash, better: null,
    target: { field: "monthly_budget_micros", unit: "money", monthly: true, ask: "Budget a month" } },
  { id: "revenue", label: "Revenue", help: "What customers the ads brought paid in the range, first and later payments.", value: (t) => t.revenue, text: cash, better: "higher",
    target: { field: "target_revenue_monthly_micros", unit: "money", monthly: true, ask: "Revenue a month" } },
  { id: "roas", label: "Return on ad spend", help: "Revenue divided by spend. 3.0× means every unit spent brought three back.", value: (t) => over(t.revenue, t.spend), text: (v) => roasText(v), better: "higher",
    target: { field: "target_roas", unit: "times", monthly: false, ask: "Return on ad spend to aim for" } },
  { id: "signups", label: "Sign-ups", help: "Sign-ups within the window after an ad click.", value: (t) => t.signups, text: count, better: "higher" },
  { id: "clicks", label: "Clicks", help: "Clicks the ad networks counted.", value: (t) => t.clicks, text: count, better: "higher" },
  { id: "cost_per_click", label: "Cost per click", help: "Spend divided by clicks.", value: (t) => over(t.spend, t.clicks), text: cash, better: "lower" },
  { id: "signup_rate", label: "Click to sign-up rate", help: "Sign-ups divided by clicks.", value: (t) => over(t.signups, t.clicks), text: (v) => (v === null ? "—" : `${(v * 100).toFixed(1)}%`), better: "higher" },
  { id: "platform_conversions", label: "Conversions the networks claim", help: "The ad networks' own count, with their own attribution. Shown to compare, never added to yours.", value: (t) => t.platform, text: count, better: "higher" },
];
const defOf = (id: NumberId) => numberDefs.find((d) => d.id === id)!;

// shownNumbers is the brand's choice, or the usual six.
export const shownNumbers = (economics: AdEconomics | null): NumberId[] => {
  const picked = (economics?.overview_numbers ?? []).filter((id) => numberDefs.some((d) => d.id === id));
  return picked.length > 0 ? picked : usualNumbers;
};

// judge compares a number with the brand's target for it. A monthly target
// counts for the share of a month (30 days) the range covers. A cost within
// a tenth of its target is on target, under it there is room to spend more,
// over it costs too much; a count or a return within a tenth is on target
// and below that is under; spend is inside its budget or over it.
export function judge(def: Def, value: number | null, economics: AdEconomics | null, days: number, currency: string): Judged {
  const stored = def.target ? (economics?.[def.target.field] as number | null | undefined) : undefined;
  if (!def.target || stored === null || stored === undefined || stored <= 0) return { word: "", target: null, text: "" };
  const target = def.target.monthly ? (stored * days) / 30 : stored;
  const shown = def.target.unit === "money" ? cash(target, currency) : def.target.unit === "times" ? roasText(target) : n(target);
  const note = def.target.monthly && days !== 30 ? `${def.target.unit === "money" ? cash(stored, currency) : n(stored)} a month, so ${shown} for these ${days} days` : undefined;
  if (value === null) return { word: "", target, text: shown, note };
  const r = value / target;
  if (def.id === "spend") return r > 1 ? { tone: "red", word: "Over budget", target, text: shown, note } : { tone: "green", word: "Inside budget", target, text: shown, note };
  if (def.better === "lower") return { ...(r > 1.1 ? { tone: "red" as const, word: "Over target" } : r < 0.9 ? { tone: "amber" as const, word: "Under target" } : { tone: "green" as const, word: "On target" }), target, text: shown, note };
  return { ...(r < 0.9 ? { tone: "amber" as const, word: "Under target" } : { tone: "green" as const, word: "On target" }), target, text: shown, note };
}

const toneText = { green: "text-green-600 dark:text-green-400", amber: "text-amber-600 dark:text-amber-400", red: "text-red-600 dark:text-red-400" };
const toneFill = { green: "bg-green-500", amber: "bg-amber-500", red: "bg-red-500" };
const toneLine = { green: "#22c55e", red: "#ef4444", grey: "#9ca3af" };

// The range drawn small: one point per day, each covering the seven days
// up to it so a ratio doesn't jump on a day with one customer.
function series(def: Def, daily: AdPerformanceDay[], from: string, to: string): number[] {
  const by = new Map(daily.map((d) => [d.day, d]));
  const days: string[] = [];
  for (let t = Date.parse(`${from}T00:00:00Z`); t <= Date.parse(`${to}T00:00:00Z`); t += 86400000) days.push(new Date(t).toISOString().slice(0, 10));
  const out: number[] = [];
  for (let i = 0; i < days.length; i++) {
    const t = noTotals();
    for (const day of days.slice(Math.max(0, i - 6), i + 1)) {
      const d = by.get(day);
      if (!d) continue;
      t.spend += d.spend_micros; t.signups += d.signups; t.payers += d.new_payers;
      t.revenue += d.revenue_micros; t.clicks += d.clicks; t.platform += d.platform_conversions;
    }
    // On a range of two weeks or more the first six points cover fewer
    // than seven days, so they are left out.
    if (days.length >= 14 && i < 6) continue;
    const v = def.value(t);
    if (v !== null) out.push(v);
  }
  return out;
}

function Spark({ points, colour }: { points: number[]; colour: string }) {
  if (points.length < 3) return <div className="mt-2 h-[26px]" />;
  const w = 150, h = 26, lo = Math.min(...points), hi = Math.max(...points);
  const d = points.map((p, i) => `${i === 0 ? "M" : "L"}${((i * w) / (points.length - 1)).toFixed(1)},${(h - 3 - ((p - lo) / (hi - lo || 1)) * (h - 6)).toFixed(1)}`).join(" ");
  return (
    <svg aria-hidden="true" className="mt-2 block h-[26px] w-full max-w-[150px]" viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none">
      <path d={d} fill="none" stroke={colour} strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" vectorEffect="non-scaling-stroke" />
    </svg>
  );
}

type TileProps = {
  id: NumberId;
  now: Totals;
  before: Totals | null;
  // "the 28 days before" or "the same days a year before".
  beforeWords: string;
  economics: AdEconomics | null;
  days: number;
  currency: string;
  suffix?: string;
  daily: AdPerformanceDay[];
  from: string;
  to: string;
  // Opens the panel where a target is set; absent for someone who can't.
  setTarget?: () => void;
};

export function NumberTile({ id, now, before, beforeWords, economics, days, currency, suffix = "", daily, from, to, setTarget }: TileProps) {
  const def = defOf(id);
  const value = def.value(now);
  const was = before ? def.value(before) : null;
  const moved = value !== null && was !== null && was !== 0 ? (value - was) / was : null;
  // Under a twentieth of a percent reads as no change.
  const change = moved !== null && Math.abs(moved) < 0.0005 ? null : moved;
  const good = change === null || def.better === null ? null : (change < 0) === (def.better === "lower");
  const judged = judge(def, value, economics, days, currency);
  const Arrow = change !== null && change < 0 ? ArrowDown : ArrowUp;
  return (
    <div className="relative min-w-0 bg-card px-4 py-3" data-testid={`number-${id}`} title={def.help}>
      {judged.tone && <span aria-hidden="true" className={`absolute inset-y-0 left-0 w-[3px] ${toneFill[judged.tone]}`} />}
      <div className="truncate font-mono text-[11px] tracking-wide text-muted-foreground uppercase">{def.label}{suffix}</div>
      <div className="mt-1 font-mono text-2xl font-medium tracking-tight tabular-nums" data-testid={`number-${id}-value`}>{def.text(value, currency)}</div>
      <div className="mt-0.5 flex flex-wrap items-center gap-x-1.5 text-[12px]" data-testid={`number-${id}-change`}>
        {change !== null ? (
          <>
            <span className={`inline-flex items-center gap-0.5 font-semibold ${good === null ? "" : good ? toneText.green : toneText.red}`}>
              <Arrow aria-hidden="true" className="size-3" />
              <span className="sr-only">{change < 0 ? "down" : "up"}</span>
              {Math.abs(change * 100).toFixed(1)}%
            </span>
            <span className="text-muted-foreground" title={`${def.text(was, currency)} in ${beforeWords}`}>was {def.text(was, currency)}</span>
          </>
        ) : (
          <span className="text-muted-foreground">{was === null || was === 0 ? `Nothing in ${beforeWords}` : "No change"}</span>
        )}
      </div>
      <div className="mt-1.5 flex items-center gap-1.5 text-[11.5px] text-muted-foreground" data-testid={`number-${id}-target`} title={judged.note}>
        {judged.target !== null ? (
          <>
            <span aria-hidden="true" className={`size-2 shrink-0 rounded-full ${judged.tone ? toneFill[judged.tone] : "bg-muted-foreground/50"}`} />
            <span className="truncate">{judged.word ? <span className={judged.tone ? toneText[judged.tone] : ""}>{judged.word}</span> : "Target"} · {judged.text}</span>
          </>
        ) : def.target ? (
          <>
            <span aria-hidden="true" className="size-2 shrink-0 rounded-full bg-muted-foreground/40" />
            <span className="truncate">No target set{setTarget && <> · <button type="button" className="underline underline-offset-2 hover:text-foreground" onClick={setTarget}>Set one</button></>}</span>
          </>
        ) : (
          <span>&nbsp;</span>
        )}
      </div>
      <Spark points={series(def, daily, from, to)} colour={good === null ? toneLine.grey : good ? toneLine.green : toneLine.red} />
    </div>
  );
}

// The grid the tiles sit in: one row on a wide screen for up to six.
export const numbersGrid = (count: number) =>
  `grid grid-cols-2 gap-px bg-border md:grid-cols-3 ${count >= 6 ? "xl:grid-cols-6" : count === 5 ? "xl:grid-cols-5" : count === 4 ? "xl:grid-cols-4" : ""}`;

// A target as typed: money and counts in whole units, a return as 3.0.
const typed = (def: Def, economics: AdEconomics | null) => {
  const v = def.target ? (economics?.[def.target.field] as number | null | undefined) : null;
  if (v === null || v === undefined) return "";
  return def.target!.unit === "money" ? (v / 1e6).toFixed(2) : String(v);
};

// ChooseNumbers is the panel under the button: tick the numbers to show,
// drag them into order (or use the arrows), type a target beside the ones
// that take one, then Save. Nothing changes until Save.
export function ChooseNumbers({ brand, economics, currency, open, setOpen, saved, label = "Choose numbers" }: { brand: string; economics: AdEconomics | null; currency: string; open: boolean; setOpen: (open: boolean) => void; saved: () => void; label?: string }) {
  const start = () => shownNumbers(economics);
  const startTargets = () => Object.fromEntries(numberDefs.filter((d) => d.target).map((d) => [d.id, typed(d, economics)])) as Record<string, string>;
  const [shown, setShown] = useState<NumberId[]>(start);
  const [targets, setTargets] = useState<Record<string, string>>(startTargets);
  const [dragging, setDragging] = useState<NumberId | null>(null);
  const [busy, setBusy] = useState(false);
  const change = (next: boolean) => {
    if (next) {
      setShown(start());
      setTargets(startTargets());
    }
    setOpen(next);
  };
  const move = (id: NumberId, to: number) => {
    const rest = shown.filter((x) => x !== id);
    rest.splice(Math.max(0, Math.min(rest.length, to)), 0, id);
    setShown(rest);
  };
  const save = async () => {
    const body: Record<string, unknown> = { overview_numbers: shown.join() === usualNumbers.join() ? [] : shown };
    for (const d of numberDefs) {
      if (!d.target || targets[d.id] === typed(d, economics)) continue;
      const text = targets[d.id].trim();
      if (text === "") {
        body[d.target.field] = null;
        continue;
      }
      const v = Number(text.replace(/,/g, ""));
      if (!(v > 0)) return toast(`${d.target.ask}: enter a number above zero, or leave it empty for no target.`, "error");
      body[d.target.field] = d.target.unit === "money" ? Math.round(v * 1e6) : d.target.unit === "count" ? Math.round(v) : v;
    }
    setBusy(true);
    const { error } = await api.PUT("/v1/brands/{brand}/ads/economics", { params: { path: { brand } }, body: { currency, ...body } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    track("ui.ads_numbers_saved", { numbers: shown.length, targets: Object.keys(body).length - 1 });
    toast("Saved. The Overview shows your numbers.");
    setOpen(false);
    saved();
  };
  const hidden = numberDefs.filter((d) => !shown.includes(d.id));
  const row = (d: Def, on: boolean, at: number) => (
    <li
      key={d.id} draggable={on} data-testid={`choose-${d.id}`}
      onDragStart={() => setDragging(d.id)} onDragEnd={() => setDragging(null)}
      onDragOver={(e) => { if (on && dragging && dragging !== d.id) { e.preventDefault(); move(dragging, at); } }}
      className={`flex items-center gap-2 rounded px-1 py-1 text-[12.5px] ${dragging === d.id ? "bg-muted" : ""}`}
    >
      <GripVertical aria-hidden="true" className={`size-3.5 shrink-0 ${on ? "cursor-grab text-muted-foreground" : "invisible"}`} />
      <Checkbox
        id={`choose-number-${d.id}`} checked={on} aria-label={`Show ${d.label}`}
        disabled={(on && shown.length === 1) || (!on && shown.length >= mostNumbers)}
        onCheckedChange={(v) => setShown(v ? [...shown, d.id] : shown.filter((x) => x !== d.id))}
      />
      <label htmlFor={`choose-number-${d.id}`} className="min-w-0 flex-1 truncate" title={d.help}>{d.label}</label>
      {on && (
        <span className="flex shrink-0">
          <button type="button" className="rounded p-0.5 text-muted-foreground hover:text-foreground disabled:opacity-30" aria-label={`Move ${d.label} up`} disabled={at === 0} onClick={() => move(d.id, at - 1)}><ArrowUp className="size-3.5" /></button>
          <button type="button" className="rounded p-0.5 text-muted-foreground hover:text-foreground disabled:opacity-30" aria-label={`Move ${d.label} down`} disabled={at === shown.length - 1} onClick={() => move(d.id, at + 1)}><ArrowDown className="size-3.5" /></button>
        </span>
      )}
      {d.target && (
        <Input
          className="h-7 w-24 shrink-0 px-2 text-right font-mono text-[12px]" inputMode="decimal" aria-label={`${d.target.ask}${d.target.unit === "money" ? `, in ${currency}` : ""}`}
          title={`${d.target.ask}${d.target.unit === "money" ? `, in ${currency}` : d.target.unit === "times" ? ": 3 means three times the spend" : ""}. Leave empty for no target.`}
          placeholder={d.target.unit === "times" ? "3.0" : "Set target"} value={targets[d.id] ?? ""} onChange={(e) => setTargets({ ...targets, [d.id]: e.target.value })}
        />
      )}
    </li>
  );
  return (
    <Popover open={open} onOpenChange={change}>
      <PopoverTrigger asChild><Button variant="ghost">{label}</Button></PopoverTrigger>
      <PopoverContent align="end" className="w-[23rem] p-0" aria-label="Choose your numbers">
        <div className="flex items-baseline justify-between border-b px-3.5 py-2.5">
          <span className="text-[13px] font-semibold">Choose your numbers</span>
          <span className="text-[12px] text-muted-foreground">{shown.length} of {mostNumbers} shown</span>
        </div>
        <div className="max-h-[60vh] overflow-y-auto px-2.5 py-2">
          <div className="px-1 pt-1 pb-0.5 font-mono text-[10.5px] tracking-wide text-muted-foreground uppercase">Shown, drag to reorder · target on the right</div>
          <ul>{shown.map((id, i) => row(defOf(id), true, i))}</ul>
          {hidden.length > 0 && (
            <>
              <div className="px-1 pt-2.5 pb-0.5 font-mono text-[10.5px] tracking-wide text-muted-foreground uppercase">More numbers</div>
              <ul>{hidden.map((d) => row(d, false, 0))}</ul>
            </>
          )}
          <p className="px-1 pt-2 text-[11.5px] text-muted-foreground">Targets are in {currency}. Monthly ones are judged by the share of a month your date range covers. Nothing changes in your ad accounts.</p>
        </div>
        <div className="flex gap-2 border-t px-3.5 py-2.5">
          <Button variant="primary" disabled={busy} onClick={save}>Save</Button>
          <Button variant="ghost" disabled={busy} onClick={() => setOpen(false)}>Cancel</Button>
          <span className="flex-1" />
          <Button variant="ghost" disabled={busy} onClick={() => setShown(usualNumbers)} title="Back to the usual six numbers. Your targets stay.">Reset</Button>
        </div>
      </PopoverContent>
    </Popover>
  );
}
