// Paid ads, Creatives, Results: every ad that ran in the range with a big
// preview, a grade for each step from being shown to a first payment, one
// verdict, a line on why, and one main button. It is the first page of the
// Creatives tab (creativePages in ./Creatives).

import { useCallback, useEffect, useMemo, useState } from "react";
import type { components } from "@outflare/sdk";
import { Film, ImageIcon, LayoutGrid, List } from "lucide-react";
import { DataTable } from "@/components/data-table";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { api, errorMessage } from "../../api";
import { track } from "../../lib/analytics";
import { ask, Button, ErrorText, Loading, tones, toast } from "../../ui";
import { money, n, platformName } from "../Ads";
import { roasText } from "./api";

type Result = components["schemas"]["AdCreativeResult"];
type Step = Result["steps"][number];
type Verdict = Result["verdict"];
type Tone = keyof typeof tones;

const verdicts: Record<Verdict, { word: string; tone: Tone; filter: string }> = {
  scale: { word: "★ Scale it", tone: "green", filter: "Scale it" },
  keep: { word: "Keep running", tone: "blue", filter: "Keep running" },
  fix: { word: "Fix it", tone: "amber", filter: "Fix" },
  stop: { word: "Stop it", tone: "red", filter: "Stop it" },
  too_early: { word: "Too early", tone: "grey", filter: "Too early" },
};
const order: Verdict[] = ["scale", "keep", "fix", "stop", "too_early"];
const stepName: Record<Step["id"], string> = { hook: "Hook", hold: "Hold", click: "Click", sign_up: "Sign up", pay: "Pay" };
const fixWord: Record<Step["id"], string> = { hook: "Fix the opening", hold: "Fix the middle", click: "Fix the message", sign_up: "Fix the page", pay: "Fix the offer" };
const gradeColour: Record<string, string> = { A: "text-green-600 dark:text-green-400", B: "text-green-600 dark:text-green-400", C: "text-amber-600 dark:text-amber-400", D: "text-red-600 dark:text-red-400" };
const gradeBar: Record<string, string> = { A: "bg-green-500 w-[92%]", B: "bg-green-500 w-[68%]", C: "bg-amber-500 w-[42%]", D: "bg-red-500 w-[18%]" };

const pct = (v: number | null | undefined) => (v == null ? "—" : `${(v * 100).toFixed(v < 0.1 ? 1 : 0)}%`);
const word = (r: Result) => (r.verdict === "fix" && r.weakest_step ? fixWord[r.weakest_step] : verdicts[r.verdict].word);

// why says in one or two sentences what the verdict rests on.
export function why(r: Result, target: number | null | undefined): string {
  const cur = r.currency;
  const cost = r.cost_per_new_payer_micros;
  const weak = r.steps.find((s) => s.id === r.weakest_step);
  const weakLine = weak
    ? { hook: `Only ${pct(weak.rate)} watch past the opening; your ads usually get ${pct(weak.usual)}. A new first few seconds should lift it.`,
        hold: `People start watching but only ${pct(weak.rate)} stay to the end; usual is ${pct(weak.usual)}. Tighten the middle.`,
        click: `It is seen but only ${pct(weak.rate)} click; your ads usually get ${pct(weak.usual)}. The promise isn't landing.`,
        sign_up: `People click but only ${pct(weak.rate)} sign up; usual is ${pct(weak.usual)}. The page may not match the ad's promise.`,
        pay: `People sign up but only ${pct(weak.rate)} pay; usual is ${pct(weak.usual)}. It may be drawing the wrong people.` }[weak.id]
    : "";
  switch (r.verdict) {
    case "scale":
      return `${n(r.new_payers)} new customers at ${money(cost, cur)} each, under your ${money(target, cur)} target, with no weak step. It can take more budget.`;
    case "stop":
      return `${r.new_payers === 0 ? `${money(r.spend_micros, cur)} spent and no new customer` : `New customers cost ${money(cost, cur)} each`}, against your ${money(target, cur)} target.${weakLine ? ` ${weakLine}` : ""}`;
    case "fix":
      return weakLine;
    case "too_early":
      return target ? `Not enough people have seen it to judge. A verdict comes after about ${money(2 * target, cur)} of spend, or two new customers.` : "Not enough people have seen it to judge yet.";
    default:
      return target ? "About as good as your other ads at every step. Nothing to change." : "About as good as your other ads at every step. Set a target cost per new customer in Settings to see which ads to scale or stop.";
  }
}

function Preview({ r, big }: { r: Result; big?: boolean }) {
  const [broken, setBroken] = useState(false);
  const box = big ? "h-64" : "size-14 shrink-0 rounded-md";
  if (r.thumbnail_url && !broken)
    return <img src={r.thumbnail_url} alt="" loading="lazy" referrerPolicy="no-referrer" onError={() => setBroken(true)} className={`${box} w-full bg-muted object-contain`} />;
  const Icon = r.video ? Film : ImageIcon;
  return (
    <div className={`${box} flex w-full flex-col items-center justify-center gap-2 bg-muted px-4 text-center text-muted-foreground`}>
      <Icon className={big ? "size-8" : "size-5"} />
      {big && <div className="line-clamp-3 text-[13px]">{r.headline || r.text || "The network gave no preview for this ad."}</div>}
    </div>
  );
}

function Steps({ r }: { r: Result }) {
  return (
    <div className="flex gap-1.5" data-testid={`creative-steps-${r.external_id}`}>
      {r.steps.map((s) => (
        <div key={s.id} className="min-w-0 flex-1 rounded-md border px-1.5 py-1.5" title={s.usual == null ? undefined : `${n(s.count)} of ${n(s.base)}. Your ads on ${platformName(r.connector)} usually get ${pct(s.usual)}.`}>
          <div className="truncate font-mono text-[10px] uppercase tracking-wide text-muted-foreground">{stepName[s.id]}</div>
          <div className="flex items-baseline gap-1">
            <b className={`text-[16px] ${gradeColour[s.grade] ?? "text-muted-foreground"}`}>{s.grade || "–"}</b>
            <span className="truncate font-mono text-[11px] tabular-nums">{s.grade ? pct(s.rate) : n(s.count)}</span>
          </div>
          <div className="mt-1 h-[3px] overflow-hidden rounded bg-muted"><div className={`h-full ${gradeBar[s.grade] ?? "w-0"}`} /></div>
        </div>
      ))}
    </div>
  );
}

function VerdictChip({ r }: { r: Result }) {
  return <span className={`inline-flex whitespace-nowrap rounded-md border px-2 py-0.5 text-[12px] font-semibold ${tones[verdicts[r.verdict].tone]}`}>{word(r)}</span>;
}

export function CreativeResults({ brand, from, to, target, canManage, openCampaign }: { brand: string; from: string; to: string; target: number | null | undefined; canManage: boolean; openCampaign: (id: string) => void }) {
  const [rows, setRows] = useState<Result[] | null>(null);
  const [error, setError] = useState("");
  const [only, setOnly] = useState<Verdict | "all">("all");
  const [network, setNetwork] = useState("");
  const viewKey = `outflare.ads.creatives.view.${brand}`;
  const [view, setView] = useState<"gallery" | "list">(() => { try { return localStorage.getItem(viewKey) === "list" ? "list" : "gallery"; } catch { return "gallery"; } });
  const pick = (v: "gallery" | "list") => { setView(v); try { localStorage.setItem(viewKey, v); } catch { /* private window */ } track("ui.ads_creatives_view_changed", { view: v }); };
  const load = useCallback(() => {
    setRows(null);
    api.GET("/v1/brands/{brand}/ads/creatives/results", { params: { path: { brand }, query: { from, to } } }).then(({ data, error }) => (error ? setError(errorMessage(error)) : (setError(""), setRows(data.data))));
  }, [brand, from, to]);
  useEffect(load, [load]);
  const networks = useMemo(() => [...new Set((rows ?? []).map((r) => r.connector))], [rows]);
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!rows) return <Loading />;
  const inNetwork = rows.filter((r) => !network || r.connector === network);
  const count = (v: Verdict) => inNetwork.filter((r) => r.verdict === v).length;
  const shown = inNetwork.filter((r) => only === "all" || r.verdict === only);
  const part = (c: number, w: string) => (c > 0 ? `${c} ${w}` : "");
  const lead = [part(count("scale"), "to scale"), part(count("fix"), "to fix"), part(count("stop"), "to stop")].filter(Boolean);
  const headline = rows.length === 0 ? "No ad ran in these days." : lead.length === 0 ? `Nothing to change: ${inNetwork.length} ${inNetwork.length === 1 ? "ad" : "ads"} running.` : `${lead.join(", ").replace(/, ([^,]*)$/, " and $1")}, out of ${inNetwork.length} running.`;
  const pause = async (r: Result) => {
    const a = await ask({ title: `Pause "${r.name}"?`, body: `It stops showing on ${platformName(r.connector)} within a few minutes. You can resume it from its campaign's page.`, choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Pause the ad", value: "ok", variant: "primary" }] });
    if (a?.choice !== "ok") return;
    const { error } = await api.POST("/v1/brands/{brand}/ads/items/{id}/pause", { params: { path: { brand, id: r.id } } });
    if (error) return toast(errorMessage(error), "error");
    track("ui.ads_creative_paused", { verdict: r.verdict });
    toast("Pausing the ad. The network shows it within a few minutes.");
    load();
  };
  const buttons = (r: Result) => {
    const campaign = r.campaign_id;
    const live = r.status === "active" && canManage;
    return (
      <div className="flex flex-wrap gap-2">
        {campaign && <Button variant={r.verdict === "scale" || r.verdict === "fix" ? "primary" : "ghost"} onClick={() => openCampaign(campaign)}>{r.verdict === "scale" ? "Raise its budget" : "Open campaign"}</Button>}
        {live && <Button variant={r.verdict === "stop" ? "primary" : "ghost"} onClick={() => pause(r)}>Pause</Button>}
      </div>
    );
  };
  const chip = (id: Verdict | "all", label: string, c: number) => (
    <button key={id} type="button" aria-pressed={only === id} onClick={() => setOnly(id)} className={`whitespace-nowrap rounded-md border px-2.5 py-1 text-[12.5px] ${only === id ? "border-foreground bg-foreground font-semibold text-background" : "text-muted-foreground hover:text-foreground"}`}>
      {label} <span className="ml-1 opacity-60">{c}</span>
    </button>
  );
  return (
    <div className="space-y-3" data-testid="creative-results">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="text-[16px] font-semibold" data-testid="creative-headline">{headline}</h2>
          <p className="text-[12.5px] text-muted-foreground">Every ad that ran in your ad accounts, graded on each step from being shown to a first payment, against your other ads on the same network over the same days.</p>
        </div>
        <Popover>
          <PopoverTrigger asChild><Button variant="ghost">How scores work</Button></PopoverTrigger>
          <PopoverContent align="end" className="w-96 space-y-2 text-[12.5px]">
            <p><b>Steps.</b> Hook: watched past the opening, of everyone shown a video. Hold: watched to the end, of those. Click: clicked, of everyone shown the ad. Sign up: signed up, of those who clicked. Pay: made a first payment, of those who signed up.</p>
            <p><b>Grades.</b> Each step is compared with the same step across all your ads on that network. A is well above your usual (1.3 times or more), B about usual, C below (under 0.9 times), D well below (under 0.6 times). A dash means too few people so far.</p>
            <p><b>Verdict.</b> Scale it: at or under your target cost per new customer, with no D. Stop it: twice your target spent with no new customer, or over 1.5 times your target. Fix: a step graded C or D. Sign-ups and payments are Outflare's own count, not the network's.</p>
          </PopoverContent>
        </Popover>
      </div>
      <div className="flex flex-wrap items-center gap-1.5">
        {chip("all", "All", inNetwork.length)}
        {order.map((v) => chip(v, verdicts[v].filter, count(v)))}
        <div className="ml-auto flex items-center gap-1.5">
          {networks.length > 1 && (
            <select aria-label="Network" className="h-8 rounded-md border bg-transparent px-2 text-[12.5px]" value={network} onChange={(e) => setNetwork(e.target.value)}>
              <option value="">Every network</option>
              {networks.map((c) => <option key={c} value={c}>{platformName(c)}</option>)}
            </select>
          )}
          <div className="flex rounded-md border p-0.5" role="group" aria-label="View">
            {([["list", "List", List], ["gallery", "Gallery", LayoutGrid]] as const).map(([id, label, Icon]) => (
              <button key={id} type="button" aria-pressed={view === id} onClick={() => pick(id)} className={`inline-flex items-center gap-1 rounded px-2 py-1 text-[12.5px] ${view === id ? "bg-foreground font-semibold text-background" : "text-muted-foreground"}`}><Icon className="size-3.5" />{label}</button>
            ))}
          </div>
        </div>
      </div>
      {shown.length === 0 && <p className="rounded-md border p-6 text-center text-[13px] text-muted-foreground">{rows.length === 0 ? "Ads show here once they have run. Sync your ad accounts under Settings if you expected some." : "No ad with this verdict."}</p>}
      {view === "gallery" && shown.length > 0 && (
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
          {shown.map((r) => (
            <article key={r.id} className="overflow-hidden rounded-lg border bg-card" data-testid={`creative-${r.external_id}`}>
              <div className="relative border-b">
                <Preview r={r} big />
                <span className="absolute left-2 top-2 rounded border bg-background/90 px-1.5 py-0.5 text-[11px]">{r.status === "active" ? "Live" : r.status === "paused" ? "Paused" : "Ended"}</span>
                <span className="absolute right-2 top-2 rounded-md bg-background/90"><VerdictChip r={r} /></span>
              </div>
              <div className="space-y-2.5 p-3">
                <div>
                  <div className="truncate text-[14px] font-semibold" title={r.name}>{r.name || r.external_id}</div>
                  <div className="truncate text-[12px] text-muted-foreground">{platformName(r.connector)}{r.campaign_name ? ` · ${r.campaign_name}` : ""}</div>
                </div>
                <Steps r={r} />
                <div className="grid grid-cols-3 gap-2 rounded-md border px-2 py-1.5">
                  <div><div className="text-[11px] text-muted-foreground">Spend</div><div className="font-mono text-[13px] tabular-nums">{money(r.spend_micros, r.currency)}</div></div>
                  <div><div className="text-[11px] text-muted-foreground">Per new customer</div><div className="font-mono text-[13px] tabular-nums">{money(r.cost_per_new_payer_micros, r.currency)}</div></div>
                  <div><div className="text-[11px] text-muted-foreground">Return</div><div className="font-mono text-[13px] tabular-nums">{roasText(r.roas)}</div></div>
                </div>
                <p className="min-h-[36px] text-[12px] text-muted-foreground">{why(r, target)}</p>
                {buttons(r)}
              </div>
            </article>
          ))}
        </div>
      )}
      {view === "list" && shown.length > 0 && (
        <DataTable
          data={shown}
          rowId={(r) => r.id}
          columnsMenu={false}
          columns={[
            { id: "ad", header: "Ad", accessorFn: (r) => r.name, cell: ({ row: { original: r } }) => (
              <div className="flex items-center gap-3">
                <div className="w-14"><Preview r={r} /></div>
                <div className="min-w-0"><div className="truncate font-medium">{r.name || r.external_id}</div><div className="truncate text-[12px] text-muted-foreground">{platformName(r.connector)}{r.campaign_name ? ` · ${r.campaign_name}` : ""}</div></div>
              </div>
            ) },
            { id: "verdict", header: "Verdict", accessorFn: (r) => order.indexOf(r.verdict), cell: ({ row: { original: r } }) => <VerdictChip r={r} /> },
            { id: "steps", header: "Steps", enableSorting: false, cell: ({ row: { original: r } }) => <div className="w-72"><Steps r={r} /></div> },
            { id: "spend", header: "Spend", accessorFn: (r) => r.spend_micros, cell: ({ row: { original: r } }) => <span className="font-mono tabular-nums">{money(r.spend_micros, r.currency)}</span> },
            { id: "cost", header: "Per new customer", accessorFn: (r) => r.cost_per_new_payer_micros ?? Infinity, cell: ({ row: { original: r } }) => <span className="font-mono tabular-nums">{money(r.cost_per_new_payer_micros, r.currency)}</span> },
            { id: "roas", header: "Return", accessorFn: (r) => r.roas ?? -1, cell: ({ row: { original: r } }) => <span className="font-mono tabular-nums">{roasText(r.roas)}</span> },
            { id: "why", header: "Why", enableSorting: false, cell: ({ row: { original: r } }) => <div className="max-w-sm whitespace-normal text-[12px] text-muted-foreground">{why(r, target)}</div> },
            { id: "do", header: "", enableSorting: false, cell: ({ row: { original: r } }) => buttons(r) },
          ]}
        />
      )}
      <p className="text-[11.5px] text-muted-foreground">Source: the ad networks' delivery (impressions, video plays, clicks, spend) and Outflare's own count of sign-ups and first payments.</p>
    </div>
  );
}
