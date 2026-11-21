// Inside a campaign: its numbers by day and its conversions by type, its
// ad sets (Google: ad groups) and its ads, each with what it spent, what the
// platform says it brought and what Outflare's own attribution credits to
// it, and for Google its keywords and the searches they matched. Platforms
// Outflare doesn't read below campaigns show the campaign's trend only.

import { SearchAdPreview } from "./ads/AdPreview";
import { useEffect, useState } from "react";
import type { AdCampaign, AdConversionAction, AdItem, AdKeyword, AdSearchTerm, AdSummaryRow, AttributionAdRow } from "@outflare/sdk";
import { DataTable, type ColumnMeta } from "@/components/data-table";
import { csvMoney, csvName, downloadCsv, toCsv } from "@/lib/csv";
import { track } from "@/lib/analytics";
import { api, errorMessage } from "../api";
import { ask, Badge, Button, Card, confirmAction, ErrorText, Menu, Stat, Stats, toast } from "../ui";
import { adStatusWord, keepStatus, metricFooter, StatusFilterSelect, useStatusFilter } from "./ads/status";
import { deliveryColumns, metricColumns, money, n, platformName, reportDownload, statusColumn } from "./Ads";
import { CampaignTargeting } from "./AdTargeting";
import { CampaignSignals } from "./ads/Signals";
import { pickedBrandId } from "@/lib/brand";

const metrics = [
  { id: "spend", label: "Spend", value: (r: AdSummaryRow) => r.totals.spend_micros, format: (v: number, c: string) => money(v, c) },
  { id: "clicks", label: "Clicks", value: (r: AdSummaryRow) => r.totals.clicks, format: (v: number) => n(v) },
  { id: "impressions", label: "Impressions", value: (r: AdSummaryRow) => r.totals.impressions, format: (v: number) => n(v) },
  { id: "conversions", label: "Platform conversions", value: (r: AdSummaryRow) => r.totals.conversions ?? 0, format: (v: number) => n(v) },
] as const;

// AdTrend draws one number per day from a summary grouped by day, in one
// currency, with the days that have no row at zero.
export function AdTrend({ rows, from, to }: { rows: AdSummaryRow[]; from: string; to: string }) {
  const [metric, setMetric] = useState<(typeof metrics)[number]["id"]>("spend");
  const [hover, setHover] = useState<number | null>(null);
  const currencies = [...new Set(rows.map((r) => r.currency))];
  const [currency, setCurrency] = useState("");
  const cur = currencies.includes(currency) ? currency : currencies[0] ?? "USD";
  const m = metrics.find((x) => x.id === metric)!;
  const byDay = new Map(rows.filter((r) => r.currency === cur).map((r) => [r.key, m.value(r)]));
  const days: string[] = [];
  for (let d = new Date(`${from}T00:00:00Z`); d <= new Date(`${to}T00:00:00Z`); d = new Date(d.getTime() + 86_400_000)) days.push(d.toISOString().slice(0, 10));
  const values = days.map((d) => byDay.get(d) ?? 0);
  const max = Math.max(1, ...values);
  const total = values.reduce((s, v) => s + v, 0);
  const W = 600, H = 120, bw = W / Math.max(days.length, 1);

  return (
    <div>
      <div className="mb-2 flex flex-wrap items-center gap-2">
        <div className="flex rounded-md border" role="group" aria-label="Number">
          {metrics.map((x) => (
            <button key={x.id} type="button" className={`px-2.5 py-1 text-[12px] ${x.id === metric ? "bg-muted font-medium" : "text-muted-foreground"}`} onClick={() => setMetric(x.id)}>{x.label}</button>
          ))}
        </div>
        {currencies.length > 1 && (
          <select className="h-7 rounded-md border bg-background px-2 text-[12px]" value={cur} onChange={(e) => setCurrency(e.target.value)} aria-label="Currency">
            {currencies.map((c) => <option key={c}>{c}</option>)}
          </select>
        )}
        <span className="ml-auto font-mono text-[12px] text-muted-foreground tabular-nums">{m.format(total, cur)} in all</span>
      </div>
      {rows.length === 0 ? (
        <p className="text-[13px] text-muted-foreground">Nothing in this range yet.</p>
      ) : (
        <div className="relative">
          <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" className="h-28 w-full" role="img" aria-label={`${m.label} per day, ${m.format(total, cur)} in all`}
            onMouseLeave={() => setHover(null)}
            onMouseMove={(e) => {
              const r = e.currentTarget.getBoundingClientRect();
              setHover(Math.min(days.length - 1, Math.max(0, Math.floor(((e.clientX - r.left) / r.width) * days.length))));
            }}>
            <line x1={0} x2={W} y1={H} y2={H} className="stroke-border" strokeWidth={1} vectorEffect="non-scaling-stroke" />
            {values.map((v, i) => (
              <rect key={days[i]} x={i * bw + bw * 0.15} width={bw * 0.7} y={H - (v / max) * (H - 4)} height={(v / max) * (H - 4)} rx={1}
                style={{ fill: "var(--series-1)", opacity: hover === null || hover === i ? 1 : 0.45 }} />
            ))}
          </svg>
          {hover !== null && (
            <div className="pointer-events-none absolute top-0 z-10 rounded-md border bg-popover px-2 py-1 text-[12px] text-popover-foreground shadow-md"
              style={{ left: `${((hover + 0.5) / days.length) * 100}%`, transform: `translateX(${hover > days.length / 2 ? "-105%" : "5%"})` }}>
              <div className="font-medium">{days[hover]}</div>
              <div className="font-mono tabular-nums">{m.format(values[hover], cur)}</div>
            </div>
          )}
          <div className="mt-1 flex justify-between font-mono text-[11px] text-muted-foreground">
            <span>{days[0]}</span>
            <span>{days[days.length - 1]}</span>
          </div>
        </div>
      )}
    </div>
  );
}

// cents formats Outflare's own money, which is in cents, one entry per
// currency.
const cents = (list: { amount_cents: number; currency: string }[] | undefined) =>
  list && list.length > 0 ? list.map((m) => money(m.amount_cents * 10_000, m.currency)).join(" · ") : "—";

// outflareColumns are what Outflare's own attribution credits to each row,
// found by the platform's id in the visits its tracking template marked.
export function outflareColumns<T extends { external_id: string }>(by: Record<string, AttributionAdRow>) {
  const mono = (s: string) => <span className="font-mono tabular-nums">{s}</span>;
  return [
    { id: "of_visits", header: "Visits", meta: { align: "right" as const }, accessorFn: (r: T) => by[r.external_id]?.visits ?? 0, cell: ({ row: { original: r } }: { row: { original: T } }) => mono(n(by[r.external_id]?.visits ?? 0)) },
    { id: "of_conversions", header: "Outflare conv.", meta: { align: "right" as const }, accessorFn: (r: T) => by[r.external_id]?.conversions ?? 0, cell: ({ row: { original: r } }: { row: { original: T } }) => mono(n(by[r.external_id]?.conversions ?? 0)) },
    { id: "of_revenue", header: "Outflare revenue", meta: { align: "right" as const }, accessorFn: (r: T) => by[r.external_id]?.revenue[0]?.amount_cents ?? 0, cell: ({ row: { original: r } }: { row: { original: T } }) => mono(cents(by[r.external_id]?.revenue)) },
  ];
}

// ConversionActions lists the conversions the platforms counted, by their
// own action: all campaigns, or the one campaign, ad set or ad entity.
// With card, it is a card of its own, shown only when there is something.
export function ConversionActions({ ws, from, to, brand, entity, card, download }: { ws: string; from: string; to: string; brand?: string; entity?: string; card?: boolean; download?: boolean }) {
  const [rows, setRows] = useState<AdConversionAction[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    api.GET("/v1/brands/{brand}/ads/conversion-actions", { params: { path: { brand: pickedBrandId() }, query: { from, to, ...(entity ? { entity_id: entity } : {}) } } })
      .then(({ data, error }) => {
        setError(error ? errorMessage(error) : "");
        setRows(data?.data ?? []);
      });
  }, [ws, from, to, brand, entity]);
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!rows || rows.length === 0) return null;
  const label = (a: string) => (a.startsWith("custom.") ? `Custom conversion ${a.slice(7)}` : a.replace(/_/g, " ").replace(/^./, (c) => c.toUpperCase()));
  const list = (
    <div className="divide-y rounded-md border text-[13px]">
      {rows.map((r) => (
        <div key={`${r.connector}|${r.action}|${r.currency}`} className="flex flex-wrap items-baseline justify-between gap-x-3 px-3 py-1.5">
          <span className="min-w-0 truncate">{label(r.action)} <span className="text-muted-foreground">· {platformName(r.connector)}</span></span>
          <span className="font-mono tabular-nums">{n(r.conversions)}{r.value_micros !== null && r.value_micros > 0 && <span className="text-muted-foreground"> · {money(r.value_micros, r.currency)}</span>}</span>
        </div>
      ))}
    </div>
  );
  if (!card) return list;
  const save = () => {
    downloadCsv(csvName("outflare-ads", "conversion-actions", from, to), toCsv(["Conversion action", "Platform", "Conversions", "Value"],
      rows.map((r) => [label(r.action), platformName(r.connector), Number(r.conversions.toFixed(2)), csvMoney(r.value_micros, r.currency)])));
    track("ui.ads_report_downloaded", { report: "conversion_actions", rows: rows.length });
  };
  return (
    <Card title="Conversions by type" description="What each platform counted, by its own action: purchases, leads, sign-ups and the rest. Meta reports a purchase several ways; each kind counts once here."
      action={download ? <Button variant="ghost" onClick={save}>Download CSV</Button> : undefined}>
      {list}
    </Card>
  );
}

// matchWord is how a search matched its keyword, in Google's words.
const matchWord = (m: string) => ({ exact: "Exact", near_exact: "Exact (close variant)", phrase: "Phrase", near_phrase: "Phrase (close variant)", broad: "Broad" } as Record<string, string>)[m] ?? (m || "—");

// bidColumns are Google's estimates of what a keyword must bid to show on
// the first page, at the top and first, and its quality score.
function bidColumns() {
  const mono = (s: string) => <span className="font-mono tabular-nums">{s}</span>;
  const bid = (id: string, header: string, f: (k: AdKeyword) => number | null, title: string) => {
    // Google's estimate is in the ad account's currency; shown in the brand's.
    const shown = (k: AdKeyword) => { const v = f(k); return v === null ? null : Math.round(v * (k.exchange_rate || 1)); };
    return {
    id, header, accessorFn: (k: AdKeyword) => f(k) ?? -1, meta: { align: "right", csv: (k: AdKeyword) => csvMoney(shown(k), k.report_currency) } satisfies ColumnMeta<AdKeyword>,
    cell: ({ row: { original: k } }: { row: { original: AdKeyword } }) => <span title={title}>{mono(money(shown(k), k.report_currency))}</span>,
    };
  };
  return [
    bid("first_page_bid", "First page bid", (k) => k.first_page_cpc_micros, "Google's estimate of the bid to show on the first page of results"),
    bid("top_of_page_bid", "Top of page bid", (k) => k.top_of_page_cpc_micros, "Google's estimate of the bid to show above the results"),
    bid("first_position_bid", "First position bid", (k) => k.first_position_cpc_micros, "Google's estimate of the bid to show first, above the results"),
    { id: "quality_score", header: "Quality score", accessorFn: (k: AdKeyword) => k.quality_score ?? -1, meta: { csv: (k: AdKeyword) => k.quality_score ?? "" } satisfies ColumnMeta<AdKeyword>,
      cell: ({ row: { original: k } }: { row: { original: AdKeyword } }) => mono(k.quality_score == null ? "—" : `${k.quality_score}/10`) },
  ];
}

// qualityColumns are Outflare's own count of who a keyword's clicks
// brought: sign-ups, how many of them have paid, people who paid for the
// first time, and what each of those cost.
function qualityColumns() {
  const mono = (s: string) => <span className="font-mono tabular-nums">{s}</span>;
  type Col = { row: { original: AdKeyword } };
  return [
    { id: "signups", header: "Sign-ups", accessorFn: (k: AdKeyword) => k.signups, meta: { align: "right" } satisfies ColumnMeta<AdKeyword>,
      cell: ({ row: { original: k } }: Col) => <span title="People who signed up in this range after a click on this keyword, counted by Outflare">{mono(n(k.signups))}</span> },
    { id: "paid_share", header: "Sign-ups that paid", accessorFn: (k: AdKeyword) => k.paid_share ?? -1,
      meta: { align: "right", csv: (k: AdKeyword) => k.paid_share == null ? "" : Number(k.paid_share.toFixed(4)) } satisfies ColumnMeta<AdKeyword>,
      cell: ({ row: { original: k } }: Col) => k.paid_share == null ? mono("—")
        : <span title={`${k.signups_paid} of ${k.signups} sign-ups have paid since`} className={k.signups >= 5 && k.signups_paid === 0 ? "text-amber-700 dark:text-amber-400" : undefined}>{mono(`${Math.round(k.paid_share * 100)}%`)}</span> },
    { id: "new_payers", header: "New paying customers", accessorFn: (k: AdKeyword) => k.new_payers, meta: { align: "right" } satisfies ColumnMeta<AdKeyword>,
      cell: ({ row: { original: k } }: Col) => <span title="People this keyword brought whose first payment fell in this range, counted by Outflare">{mono(n(k.new_payers))}</span> },
    { id: "cost_per_new_payer", header: "Cost per new paying customer", accessorFn: (k: AdKeyword) => k.cost_per_new_payer_micros ?? -1,
      meta: { align: "right", csv: (k: AdKeyword) => csvMoney(k.cost_per_new_payer_micros, k.report_currency) } satisfies ColumnMeta<AdKeyword>,
      cell: ({ row: { original: k } }: Col) => mono(money(k.cost_per_new_payer_micros, k.report_currency)) },
  ];
}

function status(s: AdItem["status"], word: string) {
  if (s === "active") return null;
  return <Badge tone={s === "removed" ? "red" : "grey"} title={word}>{s === "other" ? word || "Other" : adStatusWord(s)}</Badge>;
}

// LandingRow is every ad that sends people to one page, added up.
type LandingRow = { page: string; url: string; ads: number; spend: number; clicks: number; conversions: number; matched: number; currency: string };

const host = (u: string) => {
  try {
    return new URL(u).host + new URL(u).pathname.replace(/\/$/, "");
  } catch {
    return u;
  }
};

// last30 is the range of the last 30 days, for what a confirmation says
// each ticked row spent.
function last30() {
  const day = (d: number) => new Date(Date.now() - d * 86400000).toISOString().slice(0, 10);
  return { from: day(29), to: day(0) };
}

// BulkTable lists the rows a bulk change covers: name, status and what
// each spent in the last 30 days.
function BulkTable({ rows }: { rows: { id: string; name: string; status: string; spend: number; currency: string }[] }) {
  return (
    <table className="mt-1 w-full text-[12px]">
      <thead><tr className="text-left text-muted-foreground"><th className="font-normal">Name</th><th className="font-normal">Status</th><th className="text-right font-normal">Spent, 30 days</th></tr></thead>
      <tbody>{rows.map((r) => (
        <tr key={r.id}><td className="max-w-[18rem] truncate pr-2">{r.name}</td><td className="pr-2">{adStatusWord(r.status)}</td><td className="text-right font-mono tabular-nums">{money(r.spend, r.currency)}</td></tr>
      ))}</tbody>
    </table>
  );
}

// BulkBar is the bar over a table with ticked rows: how many, the buttons
// that change all of them after one confirmation, and Clear.
function BulkBar({ count, what, actions, clear }: { count: number; what: string; actions: { label: string; run: () => void; disabled?: boolean; danger?: boolean }[]; clear: () => void }) {
  if (count === 0) return null;
  return (
    <div className="flex flex-wrap items-center gap-2 text-[13px]" role="group" aria-label={`Change the ticked ${what}`}>
      <span className="font-medium">{count} selected</span>
      {actions.map((a) => <Button key={a.label} variant="ghost" className={a.danger ? "text-destructive" : undefined} disabled={a.disabled} onClick={a.run}>{a.label}</Button>)}
      <Button variant="ghost" onClick={clear}>Clear</Button>
    </div>
  );
}

// CampaignDetail is a campaign's own page, or one of its ad groups' when
// groupId is set: the level's numbers, its trend by day, and the level
// below it, each row opening the next level the way the ad platforms do.
export function CampaignDetail({ ws, campaign, groupId, from, to, open, back, canManage = false }: { ws: string; campaign: AdCampaign; groupId?: string; from: string; to: string; open: (groupId?: string) => void; back: () => void; canManage?: boolean }) {
  const [statusFilter, setStatusFilter] = useStatusFilter();
  const [trend, setTrend] = useState<AdSummaryRow[] | null>(null);
  const [groups, setGroups] = useState<AdItem[]>([]);
  const [ads, setAds] = useState<AdItem[]>([]);
  const [setsBy, setSetsBy] = useState<Record<string, AttributionAdRow>>({});
  const [adsBy, setAdsBy] = useState<Record<string, AttributionAdRow>>({});
  const [keywords, setKeywords] = useState<AdKeyword[]>([]);
  const [terms, setTerms] = useState<AdSearchTerm[]>([]);
  const [search, setSearch] = useState<"keywords" | "terms">("keywords");
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);
  const [pickedGroups, setPickedGroups] = useState<string[]>([]);
  const [pickedAds, setPickedAds] = useState<string[]>([]);
  const [pickedKeywords, setPickedKeywords] = useState<string[]>([]);
  const [bulkBusy, setBulkBusy] = useState(false);
  const google = campaign.connector === "google_ads";
  // Networks with keywords and search terms: Google and Microsoft.
  const searchAds = google || campaign.connector === "microsoft_ads";
  // Removing an ad or a keyword can't be undone in Google Ads: it asks
  // first, naming what goes, and only a person's own session can do it.
  async function remove(kind: "ad" | "keyword", id: string, name: string) {
    const what = kind === "ad" ? "ad" : "keyword";
    if (!(await confirmAction(`Remove ${kind === "ad" ? "the ad" : "the keyword"} ${name}?`, <>
      <p>Google Ads can't undo a removal: the {what} stops for good and can't be turned on again. To stop it for now, pause it instead.</p>
      <p>Its numbers stay here and in Google Ads.</p>
    </>, `Remove ${what}`))) return;
    const path = { brand: pickedBrandId(), id };
    const { error } = kind === "ad"
      ? await api.POST("/v1/brands/{brand}/ads/items/{id}/remove", { params: { path }, body: { confirm: true } })
      : await api.POST("/v1/brands/{brand}/ads/keywords/{id}/remove", { params: { path }, body: { confirm: true } });
    if (error) return toast(errorMessage(error), "error");
    toast(`Removal sent to Google Ads. The ${what} shows as removed once Google confirms it.`);
    setTimeout(() => setReload((v) => v + 1), 3000);
  }
  // Excluding a search adds it as a negative keyword, so the campaign (or
  // one ad group) stops paying for it. It asks first, and warns when the
  // search or a keyword it would block brought conversions.
  async function excludeSearch(t: AdSearchTerm, match: "exact" | "phrase", groupOnly: boolean) {
    const term = t.term.trim().toLowerCase();
    const padded = ` ${term} `;
    const blocked = keywords.filter((k) => k.status === "active" && (!groupOnly || k.ad_group_id === t.ad_group_id) && (k.totals.conversions ?? 0) > 0
      && (match === "exact" ? k.text.trim().toLowerCase() === term : ` ${k.text.trim().toLowerCase()} `.includes(padded)));
    const got = t.totals.conversions ?? 0;
    const ok = await confirmAction(`Exclude "${t.term}"?`, <>
      <p>{match === "exact" ? "Only this exact search is excluded" : "Every search that contains these words is excluded"}, in {groupOnly ? `the ad group ${t.ad_group_name}` : `the campaign ${campaign.name}`}. It is added as a negative keyword in Google Ads and can only spend less.</p>
      {got > 0 && <p className="text-amber-700 dark:text-amber-400">This search brought {n(got)} {got === 1 ? "conversion" : "conversions"} in this range.</p>}
      {blocked.length > 0 && <p className="text-amber-700 dark:text-amber-400">It would also block {blocked.length === 1 ? "a keyword" : "keywords"} that brought conversions: {blocked.slice(0, 5).map((k) => k.text).join(", ")}.</p>}
      <p>The change is listed under Settings, Change history.</p>
    </>, "Exclude", false);
    if (!ok) return;
    const { error } = await api.POST("/v1/brands/{brand}/ads/campaigns/{id}/negative-keywords", { params: { path: { brand: pickedBrandId(), id: campaign.id } }, body: { keyword: t.term, match_type: match, ...(groupOnly ? { ad_group_id: t.ad_group_id } : {}) } });
    if (error) return toast(errorMessage(error), "error");
    toast("Sent to Google Ads. The search stops showing your ads once Google confirms it.");
  }
  // Adding a search as a keyword makes the ad group's ads show for it.
  // It can spend more, so it asks first.
  async function addSearchAsKeyword(t: AdSearchTerm, match: "exact" | "phrase") {
    const ok = await confirmAction(`Add "${t.term}" as a keyword?`, <>
      <p>{match === "exact" ? "Your ads show for this exact search" : "Your ads show for every search that contains these words"}, in the ad group {t.ad_group_name || "it matched"}. It is added as a keyword in Google Ads.</p>
      <p>A new keyword can spend more, within the campaign's daily budget.</p>
      <p>The change is listed under Settings, Change history.</p>
    </>, "Add keyword", false);
    if (!ok) return;
    const { error } = await api.POST("/v1/brands/{brand}/ads/campaigns/{id}/keywords", { params: { path: { brand: pickedBrandId(), id: campaign.id } }, body: { keyword: t.term, match_type: match, ad_group_id: t.ad_group_id } });
    if (error) return toast(errorMessage(error), "error");
    toast("Sent to Google Ads. The keyword shows here after the next sync.");
  }
  // One ad group or ad paused or resumed, or an ad group's own daily
  // budget set: each is queued and shows here once the platform took it.
  async function changeItem(item: AdItem, kind: "pause" | "resume" | "budget", budget?: number) {
    const p = { params: { path: { brand: pickedBrandId(), id: item.id } } };
    const { error } =
      kind === "pause" ? await api.POST("/v1/brands/{brand}/ads/items/{id}/pause", p)
      : kind === "resume" ? await api.POST("/v1/brands/{brand}/ads/items/{id}/resume", p)
      : await api.PATCH("/v1/brands/{brand}/ads/items/{id}", { ...p, body: { daily_budget_micros: budget ?? 0 } });
    if (error) return toast(errorMessage(error), "error");
    toast(`${kind === "budget" ? "Budget change" : kind === "pause" ? "Pause" : "Resume"} sent to ${platformName(campaign.connector)}. It shows here once the platform took it.`);
    setTimeout(() => setReload((v) => v + 1), 3000);
  }
  async function setItemBudget(g: AdItem) {
    const r = await ask({
      title: `Daily budget for ${g.name || g.external_id}`,
      body: `In ${g.currency}, as ${platformName(campaign.connector)} shows it. Only this ${groupWord.toLowerCase().slice(0, -1)} changes.`,
      input: { label: "Daily budget", type: "number", value: g.daily_budget_micros ? (g.daily_budget_micros / 1e6).toFixed(2) : "", placeholder: "25.00" },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Set the budget", value: "ok", variant: "primary" }],
    });
    const v = Number(r?.value);
    if (r?.choice !== "ok") return;
    if (!(v > 0)) return toast("Enter an amount above zero.", "error");
    await changeItem(g, "budget", Math.round(v * 1e6));
  }
  // A bid made by hand, on an ad group or one keyword: only where the
  // campaign bids by hand (manual CPC), since a campaign that bids
  // automatically ignores it. What it replaces shows on the Changes tab.
  const manualBids = google && campaign.bidding_strategy === "manual_cpc";
  async function setUnitBid(kind: "items" | "keywords", id: string, name: string) {
    const r = await ask({
      title: `Bid for ${name}`,
      body: `The most to pay for a click, in ${campaign.currency}. ${kind === "keywords" ? "A keyword without a bid of its own uses its ad group's." : "Keywords with a bid of their own keep it."}`,
      input: { label: "Bid per click", type: "number", placeholder: "1.25" },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Set the bid", value: "ok", variant: "primary" }],
    });
    if (r?.choice !== "ok") return;
    const v = Number(r.value);
    if (!(v > 0)) return toast("Enter an amount above zero.", "error");
    const p = { params: { path: { brand: pickedBrandId(), id } }, body: { bid_micros: Math.round(v * 1e6) } };
    const { error } = kind === "items" ? await api.POST("/v1/brands/{brand}/ads/items/{id}/bid", p) : await api.POST("/v1/brands/{brand}/ads/keywords/{id}/bid", p);
    if (error) return toast(errorMessage(error), "error");
    toast("Bid change sent to Google Ads. The Changes tab shows the bid it replaced.");
  }
  // Several ad groups, ads or keywords at once: one confirmation listing
  // each with its status and what it spent in the last 30 days, then each
  // change is queued like a single one. Removing follows the permission
  // to remove; anything refused says why.
  const itemChanges = (level: AdItem["level"]) => ["meta_ads", "google_ads", "tiktok_ads", "reddit_ads", "taboola_ads", "outbrain_ads"].includes(campaign.connector)
    || (level === "ad_group" && ["microsoft_ads", "x_ads"].includes(campaign.connector));
  async function spentItems(level: "ad_group" | "ad") {
    const { data } = await api.GET("/v1/brands/{brand}/ads/items", { params: { path: { brand: pickedBrandId() }, query: { ...last30(), level, campaign_id: campaign.id, limit: 1000, idle: true } } });
    const by = new Map((data?.data ?? []).map((i) => [i.id, i.totals.spend_micros]));
    return (id: string) => by.get(id) ?? 0;
  }
  async function bulkItems(kind: "pause" | "resume" | "remove", level: "ad_group" | "ad", list: AdItem[]) {
    const take = list.filter((i) => (kind === "pause" ? i.status === "active" : kind === "resume" ? i.status === "paused" : i.status !== "removed"));
    if (take.length === 0) return;
    const what = level === "ad" ? (take.length === 1 ? "ad" : "ads") : groupWord.toLowerCase().slice(0, take.length === 1 ? -1 : undefined);
    setBulkBusy(true);
    const spend = await spentItems(level);
    setBulkBusy(false);
    const rows = take.map((i) => ({ id: i.id, name: i.name || i.details.headline || i.external_id, status: i.status, spend: spend(i.id), currency: i.currency }));
    const verb = kind === "pause" ? "Pause" : kind === "resume" ? "Resume" : "Remove";
    const ok = await confirmAction(`${verb} ${take.length} ${what}${kind === "remove" ? " for good" : ""}?`, <>
      <BulkTable rows={rows} />
      {kind === "pause" && <p>They stop showing once {platformName(campaign.connector)} confirms. You can resume them later.</p>}
      {kind === "resume" && <p>This change can spend more, within the campaign's budget. They show again once {platformName(campaign.connector)} confirms.</p>}
      {kind === "remove" && <p>Google Ads can't undo a removal: each stops for good. Their numbers stay here and in Google Ads. To stop them for now, pause them instead.</p>}
    </>, kind === "remove" ? `Remove ${take.length} ${what}` : verb, kind === "remove");
    if (!ok) return;
    setBulkBusy(true);
    const failed: string[] = [];
    for (const i of take) {
      const p = { params: { path: { brand: pickedBrandId(), id: i.id } } };
      const r = kind === "pause" ? await api.POST("/v1/brands/{brand}/ads/items/{id}/pause", p)
        : kind === "resume" ? await api.POST("/v1/brands/{brand}/ads/items/{id}/resume", p)
        : await api.POST("/v1/brands/{brand}/ads/items/{id}/remove", { ...p, body: { confirm: true } });
      if (r.error) failed.push(`${i.name || i.external_id} (${errorMessage(r.error)})`);
    }
    setBulkBusy(false);
    track("ui.ads_bulk_changed", { kind, level, campaigns: 0, items: take.length, failed: failed.length });
    if (failed.length) toast(`Could not ${kind} ${failed.length} of ${take.length}: ${failed.join("; ")}.`, "error");
    else toast(`${verb === "Remove" ? "Removal" : verb} sent for ${take.length} ${what}. They show here once ${platformName(campaign.connector)} confirms.`);
    (level === "ad" ? setPickedAds : setPickedGroups)([]);
    setTimeout(() => setReload((v) => v + 1), 3000);
  }
  async function bulkRemoveKeywords(list: AdKeyword[]) {
    const take = list.filter((k) => k.status !== "removed");
    if (take.length === 0) return;
    setBulkBusy(true);
    const { data } = await api.GET("/v1/brands/{brand}/ads/keywords", { params: { path: { brand: pickedBrandId() }, query: { ...last30(), campaign_id: campaign.id, limit: 500 } } });
    setBulkBusy(false);
    const by = new Map((data?.data ?? []).map((k) => [k.id, k.totals.spend_micros]));
    const rows = take.map((k) => ({ id: k.id, name: `${k.text} (${k.match_type})`, status: k.status, spend: by.get(k.id) ?? 0, currency: k.currency }));
    const what = take.length === 1 ? "keyword" : "keywords";
    if (!(await confirmAction(`Remove ${take.length} ${what} for good?`, <>
      <BulkTable rows={rows} />
      <p>Google Ads can't undo a removal: each keyword stops for good. Their numbers stay here and in Google Ads.</p>
    </>, `Remove ${take.length} ${what}`))) return;
    setBulkBusy(true);
    const failed: string[] = [];
    for (const k of take) {
      const { error } = await api.POST("/v1/brands/{brand}/ads/keywords/{id}/remove", { params: { path: { brand: pickedBrandId(), id: k.id } }, body: { confirm: true } });
      if (error) failed.push(`${k.text} (${errorMessage(error)})`);
    }
    setBulkBusy(false);
    track("ui.ads_bulk_changed", { kind: "remove", level: "keyword", campaigns: 0, items: take.length, failed: failed.length });
    if (failed.length) toast(`Could not remove ${failed.length} of ${take.length}: ${failed.join("; ")}.`, "error");
    else toast(`Removal sent for ${take.length} ${what}. They show as removed once Google confirms.`);
    setPickedKeywords([]);
    setTimeout(() => setReload((v) => v + 1), 3000);
  }
  const pickedOf = <T extends { id: string }>(list: T[], ids: string[]) => list.filter((x) => ids.includes(x.id));
  // What can be done to an ad group or an ad: pause or resume it, set an
  // ad group's own budget (Meta, when the ad set holds one), and remove
  // an ad (Google).
  function itemMenu(item: AdItem, name: string) {
    if (!canManage || item.status === "removed") return null;
    const items: { label: string; onClick: () => void; danger?: boolean }[] = [];
    // Ad groups and ads are changed where Outflare has the platform's call
    // for it; on the others they are read only. Pause or Resume is a
    // button on the row; the rest is under More.
    // On Microsoft and X only the ad group (X's line item) is changed.
    const changes = ["meta_ads", "google_ads", "tiktok_ads", "reddit_ads", "taboola_ads", "outbrain_ads"].includes(campaign.connector)
      || (item.level === "ad_group" && ["microsoft_ads", "x_ads"].includes(campaign.connector));
    const toggle = !changes ? null : item.status === "active" ? "pause" as const : item.status === "paused" ? "resume" as const : null;
    if (item.level === "ad_group" && (campaign.connector === "meta_ads" || campaign.connector === "tiktok_ads") && item.daily_budget_micros !== null) items.push({ label: "Set daily budget", onClick: () => setItemBudget(item) });
    if (item.level === "ad_group" && manualBids) items.push({ label: "Set bid", onClick: () => setUnitBid("items", item.id, name) });
    if (item.level === "ad" && google) items.push({ label: "Remove", onClick: () => remove("ad", item.id, name), danger: true });
    if (!toggle && items.length === 0) return null;
    return (
      <div className="flex justify-end gap-1.5" onClick={(e) => e.stopPropagation()}>
        {toggle && <Button variant="ghost" aria-label={`${toggle === "pause" ? "Pause" : "Resume"} ${name}`} onClick={() => changeItem(item, toggle)}>{toggle === "pause" ? "Pause" : "Resume"}</Button>}
        {items.length > 0 && <Menu aria={`More for ${name}`} items={items} />}
      </div>
    );
  }
  // Meta's delivery numbers show where Meta has them.
  const hidden = campaign.connector === "meta_ads" ? [] : deliveryColumns;
  // Taboola, Outbrain and LinkedIn have no ad groups: their ads sit in the
  // campaign, so there is no level to show between.
  const adsOnly = ["taboola_ads", "outbrain_ads", "linkedin_ads"].includes(campaign.connector);
  const below = adsOnly || ["meta_ads", "google_ads", "tiktok_ads", "snapchat_ads", "microsoft_ads", "pinterest_ads", "reddit_ads", "x_ads"].includes(campaign.connector);
  // The ads in view, added up by the page they send people to.
  const landing: LandingRow[] = Object.values((groupId ? ads.filter((a) => a.parent_id === groupId) : ads).reduce<Record<string, LandingRow>>((acc, a) => {
    if (!a.details.url) return acc;
    const page = host(a.details.url);
    const row = (acc[page] ??= { page, url: a.details.url, ads: 0, spend: 0, clicks: 0, conversions: 0, matched: 0, currency: a.report_currency });
    row.ads += 1;
    row.spend += a.totals.spend_micros;
    row.clicks += a.totals.clicks;
    row.conversions += a.totals.conversions ?? 0;
    row.matched += adsBy[a.external_id]?.conversions ?? 0;
    return acc;
  }, {}));
  const groupWord = campaign.connector === "meta_ads" ? "Ad sets" : campaign.connector === "snapchat_ads" ? "Ad squads" : "Ad groups";
  const group = groupId ? groups.find((g) => g.id === groupId) ?? null : null;
  useEffect(() => {
    window.scrollTo({ top: 0 });
  }, [campaign.id, groupId]);

  // The trend of the level open: the ad group's, else the campaign's.
  useEffect(() => {
    setTrend(null);
    api.GET("/v1/brands/{brand}/ads/summary", { params: { path: { brand: pickedBrandId() }, query: { from, to, group: "day", entity_id: groupId ?? campaign.id } } }).then(({ data }) => setTrend(data?.data ?? []));
  }, [ws, campaign.id, groupId, from, to]);

  useEffect(() => {
    const path = { brand: pickedBrandId() };
    const range = { from, to };
    const byId = (rows: AttributionAdRow[] | undefined) => Object.fromEntries((rows ?? []).map((r) => [r.id, r]));
    Promise.all([
      below && !adsOnly ? api.GET("/v1/brands/{brand}/ads/items", { params: { path, query: { ...range, level: "ad_group", campaign_id: campaign.id, limit: 500, idle: true } } }) : null,
      below ? api.GET("/v1/brands/{brand}/ads/items", { params: { path, query: { ...range, level: "ad", campaign_id: campaign.id, limit: 1000, idle: true } } }) : null,
      // What Outflare credits is the campaign's brand's.
      below && !adsOnly && campaign.brand_id ? api.GET("/v1/brands/{brand}/attribution/ads", { params: { path: { brand: campaign.brand_id }, query: { ...range, level: "adset" } } }) : null,
      below && campaign.brand_id ? api.GET("/v1/brands/{brand}/attribution/ads", { params: { path: { brand: campaign.brand_id }, query: { ...range, level: "ad" } } }) : null,
      searchAds ? api.GET("/v1/brands/{brand}/ads/keywords", { params: { path, query: { ...range, campaign_id: campaign.id, limit: 500 } } }) : null,
      searchAds ? api.GET("/v1/brands/{brand}/ads/search-terms", { params: { path, query: { ...range, campaign_id: campaign.id, limit: 1000 } } }) : null,
    ]).then(([g, a, os, oa, k, t]) => {
      const err = g?.error ?? a?.error ?? os?.error ?? oa?.error ?? k?.error ?? t?.error;
      setError(err ? errorMessage(err) : "");
      setGroups(g?.data?.data ?? []);
      setAds(a?.data?.data ?? []);
      setSetsBy(byId(os?.data?.data));
      setAdsBy(byId(oa?.data?.data));
      setKeywords(k?.data?.data ?? []);
      setTerms(t?.data?.data ?? []);
    });
  }, [ws, campaign.id, from, to, below, adsOnly, searchAds, reload]);

  const shown = groupId ? ads.filter((a) => a.parent_id === groupId) : ads;
  const scopedKeywords = groupId ? keywords.filter((k) => k.ad_group_id === groupId) : keywords;
  const level = group ?? campaign;
  const t = level.totals;
  const of = group ? setsBy[group.external_id] : undefined;
  const crumb = "text-blue-700 underline-offset-2 hover:underline dark:text-blue-400";
  return (
    <div className="space-y-6">
      <div className="space-y-1">
        <nav aria-label="Where you are" className="flex flex-wrap items-center gap-1.5 text-[13px] text-muted-foreground">
          <button type="button" className={crumb} onClick={back}>All campaigns</button>
          <span aria-hidden>›</span>
          {groupId ? <button type="button" className={crumb} onClick={() => open()}>{campaign.name}</button> : <span className="text-foreground">{campaign.name}</span>}
          {groupId && (
            <>
              <span aria-hidden>›</span>
              <span className="text-foreground">{group ? group.name || group.external_id : "…"}</span>
            </>
          )}
        </nav>
        <h2 className="text-lg font-semibold">{group ? group.name || group.external_id : campaign.name}</h2>
        <p className="text-[13px] text-muted-foreground">
          {group ? `${groupWord.slice(0, -1)} in ${campaign.name}` : `${platformName(campaign.connector)} · ${campaign.account_name}`}
          {!group && campaign.objective && ` · ${campaign.objective.replace(/_/g, " ")}`}
          {!group && campaign.bidding_strategy && campaign.bidding_strategy !== "unknown" && ` · bids ${campaign.bidding_strategy.replace(/_/g, " ")}`}
          {group && " "}{group && status(group.status, group.provider_status)}
        </p>
      </div>
      <ErrorText>{error}</ErrorText>
      <Stats cols={5}>
        <Stat label="Spend" value={money(t.spend_micros, level.report_currency)} sub={!group && campaign.spend_change != null ? `${campaign.spend_change > 0 ? "+" : ""}${Math.round(campaign.spend_change * 100)}% on the period before` : undefined} />
        <Stat label="Clicks" value={n(t.clicks)} sub={`${n(t.impressions)} impressions`} />
        <Stat label="Conversions" value={n(t.conversions)} sub={of ? `${n(of.conversions)} by Outflare's count` : "as the platform counts them"} />
        <Stat
          label="Cost per conversion"
          value={money(t.cost_per_conversion_micros, level.report_currency)}
          sub={!group && campaign.target_cost_per_conversion_micros != null ? `target ${money(Math.round(campaign.target_cost_per_conversion_micros * (campaign.exchange_rate || 1)), campaign.report_currency)}` : undefined}
          tone={!group ? (campaign.target_status === "over" ? "red" : campaign.target_status === "under" ? "green" : undefined) : undefined}
        />
        <Stat label="Cost per click" value={money(t.cpc_micros, level.report_currency)} sub={`CTR ${t.ctr == null ? "—" : `${(t.ctr * 100).toFixed(2)}%`}`} />
      </Stats>

      <Card title="By day" description={`${groupId ? `This ${groupWord.toLowerCase().slice(0, -1)}'s` : "This campaign's"} numbers each day, as ${platformName(campaign.connector)} reports them.`}>
        {trend && <AdTrend rows={trend} from={from} to={to} />}
      </Card>

      <ConversionActions ws={ws} from={from} to={to} entity={groupId ?? campaign.id} card />

      {!groupId && google && <CampaignSignals campaign={campaign} from={from} to={to} />}

      {!groupId && (campaign.connector === "google_ads" || campaign.connector === "meta_ads") && <CampaignTargeting ws={ws} campaign={campaign} />}

      {below && !adsOnly && !groupId && (
        <Card title={groupWord} description={`Open one for its ads${searchAds ? ", keywords and searches" : ""}.`}>
          <DataTable
            data={groups.filter((g) => keepStatus(statusFilter, g.status))}
            toolbar={<>
              <StatusFilterSelect value={statusFilter} onChange={setStatusFilter} hidden={groups.filter((g) => !keepStatus(statusFilter, g.status)).length} />
              <BulkBar count={pickedGroups.length} what={groupWord.toLowerCase()} clear={() => setPickedGroups([])} actions={[
                { label: "Pause", disabled: bulkBusy || pickedOf(groups, pickedGroups).every((g) => g.status !== "active"), run: () => bulkItems("pause", "ad_group", pickedOf(groups, pickedGroups)) },
                { label: "Resume", disabled: bulkBusy || pickedOf(groups, pickedGroups).every((g) => g.status !== "paused"), run: () => bulkItems("resume", "ad_group", pickedOf(groups, pickedGroups)) },
              ]} />
            </>}
            footer={metricFooter<AdItem>(groupWord.toLowerCase(), groups, (g) => g.totals, (g) => g.report_currency, { money, n }, "campaign")}
            rowId={(g) => g.id}
            selection={canManage && itemChanges("ad_group") ? { ids: pickedGroups, onChange: setPickedGroups, label: (g) => g.name || g.external_id } : undefined}
            onRowClick={(g) => open(g.id)}
            hidden={hidden}
            searchPlaceholder={`Search ${groupWord.toLowerCase()}`}
            empty={`No ${groupWord.toLowerCase()} spent or ran in this range.`}
            initialSort={[{ id: "spend", desc: true }]}
            download={reportDownload("ad_groups", campaign.name, from, to)}
            columns={[
              {
                id: "name",
                header: groupWord.slice(0, -1),
                accessorFn: (g) => g.name,
                meta: { csv: (g: AdItem) => g.name || g.external_id } satisfies ColumnMeta<AdItem>,
                cell: ({ row: { original: g } }) => (
                  <div className="space-y-0.5">
                    <div className="font-medium text-blue-700 underline-offset-2 hover:underline dark:text-blue-400">{g.name || g.external_id}</div>
                    <div className="text-[12px] text-muted-foreground">
                      {((k) => (k === 1 ? "1 ad" : `${k} ads`))(ads.filter((a) => a.parent_id === g.id).length)}{g.daily_budget_micros !== null && <> · {money(Math.round(g.daily_budget_micros * (g.exchange_rate || 1)), g.report_currency)} a day</>} {status(g.status, g.provider_status)}
                    </div>
                  </div>
                ),
              },
              statusColumn<AdItem>(),
              ...metricColumns<AdItem>((g) => g.totals, (g) => g.report_currency),
              ...outflareColumns<AdItem>(setsBy),
              {
                id: "manage", header: "", enableSorting: false,
                cell: ({ row: { original: g } }) => <span onClick={(e) => e.stopPropagation()}>{itemMenu(g, g.name || g.external_id)}</span>,
              },
            ]}
          />
        </Card>
      )}

      {below && (
        <Card title={groupId ? "Ads" : "Every ad in this campaign"}>
              <DataTable
                data={shown.filter((a) => keepStatus(statusFilter, a.status))}
                toolbar={<>
                  <StatusFilterSelect value={statusFilter} onChange={setStatusFilter} hidden={shown.filter((a) => !keepStatus(statusFilter, a.status)).length} />
                  <BulkBar count={pickedAds.length} what="ads" clear={() => setPickedAds([])} actions={[
                    { label: "Pause", disabled: bulkBusy || pickedOf(shown, pickedAds).every((a) => a.status !== "active"), run: () => bulkItems("pause", "ad", pickedOf(shown, pickedAds)) },
                    { label: "Resume", disabled: bulkBusy || pickedOf(shown, pickedAds).every((a) => a.status !== "paused"), run: () => bulkItems("resume", "ad", pickedOf(shown, pickedAds)) },
                    ...(google ? [{ label: "Remove", danger: true, disabled: bulkBusy, run: () => bulkItems("remove", "ad", pickedOf(shown, pickedAds)) }] : []),
                  ]} />
                </>}
                footer={metricFooter<AdItem>("ads", shown, (a) => a.totals, (a) => a.report_currency, { money, n }, groupId ? groupWord.slice(0, -1).toLowerCase() : "campaign")}
                rowId={(a) => a.id}
                selection={canManage && itemChanges("ad") ? { ids: pickedAds, onChange: setPickedAds, label: (a) => a.name || a.details.headline || a.external_id } : undefined}
                searchPlaceholder="Search ads"
                hidden={[...hidden, "all_conversions"]}
                empty="No ads spent or ran in this range."
                initialSort={[{ id: "spend", desc: true }]}
                download={reportDownload("ads", campaign.name, from, to)}
                columns={[
                  {
                    id: "ad",
                    header: "Ad",
                    accessorFn: (a) => `${a.name} ${a.details.headline ?? ""} ${a.external_id}`,
                    meta: { csv: (a: AdItem) => a.name || a.details.headline || a.external_id } satisfies ColumnMeta<AdItem>,
                    cell: ({ row: { original: a } }) => (
                      <div className={`flex gap-3 ${a.details.headlines ? "w-[440px] max-w-[60vw] whitespace-normal" : "max-w-md"}`}>
                        {a.details.thumbnail_url && <img src={a.details.thumbnail_url} alt="" className="size-10 shrink-0 rounded object-cover" loading="lazy" referrerPolicy="no-referrer" />}
                        <div className="min-w-0 space-y-0.5">
                          {a.details.headlines && a.details.descriptions ? (
                            <SearchAdPreview where="campaign" open={false} ad={{ final_url: a.details.url, path1: a.details.path1, path2: a.details.path2, headlines: a.details.headlines.split("\n"), descriptions: a.details.descriptions.split("\n") }} />
                          ) : (
                            <>
                              <div className="truncate font-medium">{a.name || a.details.headline || a.external_id}</div>
                              {a.name && a.details.headline && a.details.headline !== a.name && <div className="truncate text-[12px]">{a.details.headline}</div>}
                            </>
                          )}
                          <div className="truncate text-[12px] text-muted-foreground">
                            {!groupId && !adsOnly && <>{a.parent_name} · </>}
                            {a.details.url ? <a href={a.details.url} target="_blank" rel="noreferrer" className="underline">{host(a.details.url)}</a> : `id ${a.external_id}`} {status(a.status, a.provider_status)}
                          </div>
                          {a.details.policy_status === "disapproved" && (
                            <Badge tone="red" title="The network stopped showing this ad. Fix what it names, in the ad or on its landing page, and it is reviewed again.">
                              Disapproved{a.details.policy_reasons ? `: ${a.details.policy_reasons}` : ""}
                            </Badge>
                          )}
                          {a.details.policy_status === "approved_limited" && a.details.policy_reasons && <Badge tone="amber" title="The network shows this ad to fewer people.">Limited: {a.details.policy_reasons}</Badge>}
                          {a.details.templated === "false" && <Badge tone="amber" title="Its links don't carry Outflare's tracking template, so its clicks can't be tied to it. Add the template under Tracking templates.">No tracking template</Badge>}
                        </div>
                      </div>
                    ),
                  },
                  statusColumn<AdItem>(),
                  ...metricColumns<AdItem>((a) => a.totals, (a) => a.report_currency),
                  ...outflareColumns<AdItem>(adsBy),
                  {
                    id: "manage", header: "", enableSorting: false,
                    cell: ({ row: { original: a } }) => itemMenu(a, a.name || a.details.headline || a.external_id),
                  },
                ]}
                mobile={(a) => (
                  <span className="block min-w-0">
                    <span className="block truncate text-[13px] font-medium">{a.name || a.details.headline || a.external_id}</span>
                    <span className="block truncate text-[12px] text-muted-foreground">{money(a.totals.spend_micros, a.report_currency)} · {n(a.totals.clicks)} clicks · {n(a.totals.conversions)} conversions</span>
                  </span>
                )}
              />
        </Card>
      )}

      {below && landing.length > 1 && (
        <Card title="Landing pages" description="The same ads, added up by the page they send people to. Source: the ad network, with sign-ups and payments Outflare matched to each ad.">
          <DataTable
            data={landing}
            rowId={(l) => l.page}
            searchPlaceholder="Search pages"
            empty="No ad with a landing page ran in this range."
            initialSort={[{ id: "spend", desc: true }]}
            download={reportDownload("landing_pages", campaign.name, from, to)}
            columns={[
              { id: "page", header: "Landing page", accessorFn: (l) => l.page, cell: ({ row: { original: l } }) => <a href={l.url} target="_blank" rel="noreferrer" className="block max-w-md truncate underline">{l.page}</a> },
              { id: "ads", header: "Ads", accessorFn: (l) => l.ads, meta: { align: "right" } satisfies ColumnMeta<LandingRow>, cell: ({ row: { original: l } }) => <span className="font-mono tabular-nums">{n(l.ads)}</span> },
              { id: "spend", header: "Spend", accessorFn: (l) => l.spend, meta: { align: "right" } satisfies ColumnMeta<LandingRow>, cell: ({ row: { original: l } }) => <span className="font-mono tabular-nums">{money(l.spend, l.currency)}</span> },
              { id: "clicks", header: "Clicks", accessorFn: (l) => l.clicks, meta: { align: "right" } satisfies ColumnMeta<LandingRow>, cell: ({ row: { original: l } }) => <span className="font-mono tabular-nums">{n(l.clicks)}</span> },
              { id: "conversions", header: "Conversions", accessorFn: (l) => l.conversions, meta: { align: "right" } satisfies ColumnMeta<LandingRow>, cell: ({ row: { original: l } }) => <span className="font-mono tabular-nums" title="What the ad network counted">{n(l.conversions)}</span> },
              { id: "matched", header: "Outflare conv.", accessorFn: (l) => l.matched, meta: { align: "right" } satisfies ColumnMeta<LandingRow>, cell: ({ row: { original: l } }) => <span className="font-mono tabular-nums" title="Sign-ups and payments Outflare matched to these ads">{n(l.matched)}</span> },
              { id: "cost", header: "Cost per conversion", accessorFn: (l) => (l.conversions > 0 ? l.spend / l.conversions : Infinity), meta: { align: "right" } satisfies ColumnMeta<LandingRow>, cell: ({ row: { original: l } }) => <span className="font-mono tabular-nums">{l.conversions > 0 ? money(l.spend / l.conversions, l.currency) : "—"}</span> },
            ]}
          />
        </Card>
      )}

      {searchAds && (
        <Card title="Search">
              <div>
                <div className="mb-2 flex rounded-md border w-fit" role="group" aria-label="Search">
                  {(["keywords", "terms"] as const).map((k) => (
                    <button key={k} type="button" className={`px-2.5 py-1 text-[12px] ${k === search ? "bg-muted font-medium" : "text-muted-foreground"}`} onClick={() => setSearch(k)}>
                      {k === "keywords" ? "Keywords" : "Search terms"}
                    </button>
                  ))}
                </div>
                {search === "keywords" ? (
                  <DataTable
                    data={scopedKeywords.filter((k) => keepStatus(statusFilter, k.status))}
                    toolbar={<>
                      <StatusFilterSelect value={statusFilter} onChange={setStatusFilter} hidden={scopedKeywords.filter((k) => !keepStatus(statusFilter, k.status)).length} />
                      <BulkBar count={pickedKeywords.length} what="keywords" clear={() => setPickedKeywords([])} actions={[
                        { label: "Remove", danger: true, disabled: bulkBusy, run: () => bulkRemoveKeywords(pickedOf(scopedKeywords, pickedKeywords)) },
                      ]} />
                    </>}
                    footer={metricFooter<AdKeyword>("keywords", scopedKeywords, (k) => k.totals, (k) => k.report_currency, { money, n }, groupId ? groupWord.slice(0, -1).toLowerCase() : "campaign")}
                    rowId={(k) => k.id}
                    selection={canManage && google ? { ids: pickedKeywords, onChange: setPickedKeywords, label: (k) => k.text } : undefined}
                    searchPlaceholder="Search keywords"
                    hidden={[...deliveryColumns, "all_conversions", "roas", "quality_score"]}
                    empty="No keyword showed an ad in this range."
                    initialSort={[{ id: "spend", desc: true }]}
                    download={reportDownload("keywords", campaign.name, from, to)}
                    columns={[
                      {
                        id: "keyword",
                        header: "Keyword",
                        accessorFn: (k) => k.text,
                        cell: ({ row: { original: k } }) => (
                          <div className="space-y-0.5">
                            <div className="font-medium">{k.match_type === "exact" ? `[${k.text}]` : k.match_type === "phrase" ? `"${k.text}"` : k.text}</div>
                            <div className="text-[12px] text-muted-foreground">{k.match_type} · {k.ad_group_name} {status(k.status, "")}</div>
                          </div>
                        ),
                      },
                      { id: "match_type", header: "Match type", accessorFn: (k) => k.match_type },
                      { id: "ad_group", header: "Ad group", accessorFn: (k) => k.ad_group_name },
                      statusColumn<AdKeyword>(),
                      ...metricColumns<AdKeyword>((k) => k.totals, (k) => k.report_currency),
                      ...qualityColumns(),
                      ...bidColumns(),
                      {
                        id: "manage", header: "", enableSorting: false,
                        cell: ({ row: { original: k } }) => canManage && k.status !== "removed" && (
                          <div className="flex justify-end gap-1.5">
                            {manualBids && <Button variant="ghost" aria-label={`Set bid for ${k.text}`} onClick={() => setUnitBid("keywords", k.id, k.text)}>Set bid</Button>}
                            {google && <Menu aria={`More for ${k.text}`} items={[{ label: "Remove", onClick: () => remove("keyword", k.id, k.text), danger: true }]} />}
                          </div>
                        ),
                      },
                    ]}
                  />
                ) : (
                  <DataTable
                    data={groupId ? terms.filter((t) => t.ad_group_id === groupId) : terms}
                    rowId={(t) => `${t.ad_group_id}|${t.term}|${t.keyword_text}|${t.keyword_match_type}`}
                    searchPlaceholder="Search what people searched"
                    hidden={[...deliveryColumns, "all_conversions", "roas"]}
                    empty={`No searches in this range. ${google ? "Google" : "Microsoft"} leaves out searches too few people made.`}
                    initialSort={[{ id: "spend", desc: true }]}
                    download={reportDownload("search_terms", campaign.name, from, to)}
                    columns={[
                      {
                        id: "term",
                        header: "Search",
                        accessorFn: (t) => `${t.term} ${t.keyword_text}`,
                        meta: { csv: (t: AdSearchTerm) => t.term } satisfies ColumnMeta<AdSearchTerm>,
                        cell: ({ row: { original: t } }) => (
                          <div className="space-y-0.5">
                            <div className="font-medium">{t.term}</div>
                            <div className="text-[12px] text-muted-foreground">{t.keyword_text ? `matched ${t.keyword_text} (${t.keyword_match_type})` : "keyword not reported"} · {t.ad_group_name}</div>
                          </div>
                        ),
                      },
                      { id: "match_type", header: "Match type", accessorFn: (t) => matchWord(t.match_type), cell: ({ row: { original: t } }) => <span title="How the search matched the keyword">{matchWord(t.match_type)}</span> },
                      { id: "keyword", header: "Keyword", accessorFn: (t) => t.keyword_text ? `${t.keyword_text} (${t.keyword_match_type})` : "" },
                      ...metricColumns<AdSearchTerm>((t) => t.totals, (t) => t.report_currency),
                      { id: "keyword_paid_share", header: "Its keyword's sign-ups that paid", accessorFn: (t) => t.keyword_paid_share ?? -1,
                        meta: { align: "right", csv: (t: AdSearchTerm) => t.keyword_paid_share == null ? "" : Number(t.keyword_paid_share.toFixed(4)) } satisfies ColumnMeta<AdSearchTerm>,
                        cell: ({ row: { original: t } }) => t.keyword_paid_share == null ? <span className="font-mono tabular-nums">—</span>
                          : <span className={`font-mono tabular-nums ${t.keyword_signups >= 5 && t.keyword_signups_paid === 0 ? "text-amber-700 dark:text-amber-400" : ""}`} title={`${t.keyword_signups_paid} of ${t.keyword_signups} people who signed up after a click on ${t.keyword_text} have paid. A network doesn't say which search a click came from, so this is the keyword's number.`}>{Math.round(t.keyword_paid_share * 100)}% of {n(t.keyword_signups)}</span> },
                      {
                        id: "manage", header: "", enableSorting: false, meta: { align: "right", csv: false } satisfies ColumnMeta<AdSearchTerm>,
                        cell: ({ row: { original: t } }) => canManage && campaign.status !== "removed" && (
                          <span className="inline-flex items-center gap-1">
                            {t.ad_group_id && !keywords.some((k) => k.status !== "removed" && k.text.trim().toLowerCase() === t.term.trim().toLowerCase()) && (
                              <Menu label="Add as keyword" aria={`Add ${t.term} as a keyword`} items={[
                                { label: "This exact search", onClick: () => addSearchAsKeyword(t, "exact") },
                                { label: "Every search that contains it", onClick: () => addSearchAsKeyword(t, "phrase") },
                              ]} />
                            )}
                          <Menu label="Exclude" aria={`Exclude ${t.term}`} items={[
                            { label: "This exact search, in this campaign", onClick: () => excludeSearch(t, "exact", false) },
                            { label: "Every search that contains it, in this campaign", onClick: () => excludeSearch(t, "phrase", false) },
                            { label: `This exact search, in ${t.ad_group_name || "its ad group"} only`, onClick: () => excludeSearch(t, "exact", true) },
                          ]} />
                          </span>
                        ),
                      },
                    ]}
                  />
                )}
              </div>
        </Card>
      )}
    </div>
  );
}
