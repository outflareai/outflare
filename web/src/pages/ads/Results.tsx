// Campaigns: every campaign (or ad group, or ad) of the brand's ad accounts
// over the range, scored in Outflare's own count: sign-ups and new paying
// customers, what each cost against the target, the revenue they brought
// and keep bringing beside the spend with the return on it (ROAS), and
// payback. The network's own conversion count sits
// at the end, greyed: what Google says, never the judge. Each row has one
// visible button (Pause or Resume) and the rest under More; ticking
// campaigns shows one bar that pauses or re-budgets them together.

import { useEffect, useState } from "react";
import type { AdCampaign } from "@outflare/sdk";
import type { Tab } from "../../App";
import { api, errorMessage } from "../../api";
import { track } from "@/lib/analytics";
import { DataTable, type ColumnMeta, type TableFooterRow } from "@/components/data-table";
import { keepStatus, servingStatus, StatusFilterSelect, useStatusFilter } from "./status";
import { csvMoney } from "@/lib/csv";
import { Badge, Button, Card, ErrorText, Loading, Menu, Status, field } from "../../ui";
import { Against } from "./Overview";
import { money, n, platformName, reportDownload, scopeQuery, type AdScope } from "../Ads";
import { minorList, rangeOf, roasHelp, roasText, type AdPerformanceRow, type Customers, type Level } from "./api";

const levels: { id: Level; label: string }[] = [
  { id: "campaign", label: "Campaigns" },
  { id: "ad_group", label: "Ad groups" },
  { id: "ad", label: "Ads" },
];
// How the table can count: the last ad click the page judges on, or one of
// the brand's attribution models beside it.
const lastPaidClick = "last_paid_click";
const builtInModels: { id: string; label: string; help: string }[] = [
  { id: "last_non_direct", label: "Last non-direct touch", help: "the last visit that wasn't direct gets all the credit" },
  { id: "first_touch", label: "First touch", help: "the first visit gets all the credit" },
  { id: "linear", label: "Linear", help: "every visit gets an equal share" },
  { id: "position", label: "40/20/40", help: "40% to the first visit, 40% to the last, 20% shared by the rest" },
  { id: "time_decay", label: "Time decay", help: "shared, with visits closer to the conversion getting more" },
];
const modelKey = (brand: string) => `outflare.ads.model.${brand}`;
function savedModel(brand: string): string {
  try {
    return window.localStorage.getItem(modelKey(brand)) || lastPaidClick;
  } catch {
    return lastPaidClick;
  }
}

const targetWord = { over: "Over", under: "Under", on_target: "On target", no_target: "", no_spend: "" } as const;
const trendWord = { scaling_up: "Scaling up", scaling_down: "Scaling down", steady: "Steady", new: "New", stopped: "Stopped", idle: "" } as const;
const trendTone = { scaling_up: "green", scaling_down: "amber", steady: "grey", new: "blue", stopped: "grey", idle: "grey" } as const;

// A share of sign-ups: whole when it is, else to one decimal place.
const share = (v: number) => (Number.isInteger(v) ? n(v) : v.toLocaleString(undefined, { minimumFractionDigits: 1, maximumFractionDigits: 1 }));

const mono = (s: string, muted = false) => <span className={`font-mono tabular-nums ${muted ? "text-muted-foreground" : ""}`}>{s}</span>;

// Manage are the campaign actions the old Campaigns table had, kept here
// behind one menu per row: the shell owns them, because each is queued and
// read back from the network.
export type Manage = {
  campaigns: AdCampaign[];
  busy: boolean;
  pause: (c: AdCampaign) => void;
  resume: (c: AdCampaign) => void;
  budget: (c: AdCampaign) => void;
  target: (c: AdCampaign) => void;
  // remove removes a Google campaign for good, after asking.
  remove: (c: AdCampaign) => void;
  manageable: (connector: string) => boolean;
  // link is the campaign's own page in its ad network, when there is one.
  link: (c: AdCampaign) => string | null;
  // pauseMany, resumeMany, removeMany and budgetMany change several
  // campaigns after one confirmation.
  pauseMany: (cs: AdCampaign[]) => Promise<void>;
  resumeMany: (cs: AdCampaign[]) => Promise<void>;
  removeMany: (cs: AdCampaign[]) => Promise<void>;
  budgetMany: (cs: AdCampaign[]) => Promise<void>;
};


type Props = {
  brand: string;
  days: number;
  // end is a custom range's last day; without it the range ends today.
  end?: string;
  windowDays: number;
  customers: Customers;
  scope: AdScope;
  canManage: boolean;
  manage: Manage;
  go: (tab: Tab, item?: string) => void;
  version?: number;
};

// budgetOf is a campaign's daily budget in the currency its numbers are
// shown in: the budget itself is in the ad account's.
export function budgetOf(c: AdCampaign | undefined): number | null {
  if (!c || c.daily_budget_micros == null) return null;
  return Math.round(c.daily_budget_micros * (c.exchange_rate || 1));
}

export function Results({ brand, days, end, windowDays, customers, scope, canManage, manage, go, version = 0 }: Props) {
  const [level, setLevel] = useState<Level>("campaign");
  const [rows, setRows] = useState<AdPerformanceRow[] | null>(null);
  const [target, setTarget] = useState<number | null>(null);
  const [notes, setNotes] = useState<string[]>([]);
  const [error, setError] = useState("");
  const [picked, setPicked] = useState<string[]>([]);
  const [statusFilter, setStatusFilter] = useStatusFilter();
  // The attribution model the rows are also counted under, remembered per
  // brand in this browser; the brand's own models come after the built-in.
  const [model, setModel] = useState(() => savedModel(brand));
  const [models, setModels] = useState(builtInModels);

  useEffect(() => {
    let live = true;
    setModel(savedModel(brand));
    api.GET("/v1/brands/{brand}/attribution/models", { params: { path: { brand } } }).then(({ data }) => {
      if (!live) return;
      setModels([...builtInModels, ...(data?.data ?? []).filter((m) => !m.archived_at).map((m) => ({ id: m.key, label: m.name, help: "one of your own models" }))]);
    });
    return () => {
      live = false;
    };
  }, [brand]);
  const pickModel = (id: string) => {
    setModel(id);
    track("ui.ads_model_picked", { model: builtInModels.some((m) => m.id === id) || id === lastPaidClick ? id : "custom" });
    try {
      window.localStorage.setItem(modelKey(brand), id);
    } catch {
      // The choice then lasts until the page is left.
    }
  };

  useEffect(() => {
    let live = true;
    setRows(null);
    setPicked([]);
    const range = rangeOf(days, end ? new Date(`${end}T12:00:00Z`) : undefined);
    const query = { from: range.from, to: range.to, level, window_days: windowDays, customers, idle: true, ...scopeQuery(scope) };
    const read = (m: string) => api.GET("/v1/brands/{brand}/ads/performance", { params: { path: { brand }, query: m === lastPaidClick ? query : { ...query, model: m } } });
    read(model).then(async (res) => {
      // A remembered model that is gone (archived, or another brand's):
      // read again on the last ad click and forget it.
      if (res.error && model !== lastPaidClick && res.response.status === 400) {
        res = await read(lastPaidClick);
        if (live) pickModel(lastPaidClick);
      }
      if (!live) return;
      const { data, error } = res;
      setError(error ? errorMessage(error) : "");
      setRows(data?.data ?? []);
      setTarget(data?.target_cost_per_payer_micros ?? null);
      setNotes(data?.notes ?? []);
    });
    return () => {
      live = false;
    };
  }, [brand, days, end, windowDays, customers, level, scope.network, scope.account, version, model]);

  const open = (r: AdPerformanceRow) => {
    if (r.level === "campaign") return go("ads", r.id);
    if (r.level === "ad_group") return go("ads", `${r.campaign_id ?? r.id}/${r.id}`);
    if (r.campaign_id) go("ads", r.campaign_id);
  };
  const campaignOf = (r: AdPerformanceRow) => {
    const id = r.level === "campaign" ? r.id : r.campaign_id;
    return manage.campaigns.find((c) => c.id === id || (r.level === "campaign" && c.external_id === r.external_id));
  };

  // A campaign's finer status and Optimization score come with the
  // campaign; ad groups and ads show the plain status here.
  const serving = (r: AdPerformanceRow) => { const c = r.level === "campaign" ? campaignOf(r) : undefined; return servingStatus(r.status, c?.primary_status, c?.primary_status_reasons); };
  // Room to grow: the searches a campaign missed to its budget and to
  // rank over the last 7 days, in whole percent; null when Google gives none.
  const room = (r: AdPerformanceRow) => {
    const c = r.level === "campaign" ? campaignOf(r) : undefined;
    if (!c || c.budget_lost_share == null) return null;
    return { budget: Math.round(c.budget_lost_share * 100), rank: Math.round((c.rank_lost_share ?? 0) * 100) };
  };
  const score = (r: AdPerformanceRow) => { const v = r.level === "campaign" ? campaignOf(r)?.optimization_score ?? null : null; return v === null ? null : Math.round(v * 1000) / 10; };

  const switcher = (
    <div className="flex rounded-md border" role="group" aria-label="View by">
      {levels.map((l) => (
        <button key={l.id} type="button" aria-pressed={l.id === level} className={`px-2.5 py-1 text-[13px] ${l.id === level ? "bg-muted font-medium" : "text-muted-foreground"}`} onClick={() => setLevel(l.id)}>{l.label}</button>
      ))}
    </div>
  );

  // The range the rows are for, as the download and description name it.
  const shown = rangeOf(days, end ? new Date(`${end}T12:00:00Z`) : undefined);
  if (error && !rows) return <ErrorText>{error}</ErrorText>;
  if (!rows) return <Loading />;

  const cost = (r: AdPerformanceRow) => <span className="font-mono tabular-nums">{money(r.cost_per_new_payer_micros, r.currency)}</span>;

  // Ticked campaigns, for the bar that changes several at once.
  const pickable = canManage && level === "campaign";
  const chosen = picked.map((id) => rows.find((r) => r.id === id)).map((r) => r && campaignOf(r)).filter((c): c is AdCampaign => !!c && manage.manageable(c.connector) && c.status !== "removed");
  const bulk = async (run: (cs: AdCampaign[]) => Promise<void>) => { await run(chosen); setPicked([]); };

  // What the status filter leaves, and the two total rows under them, as
  // in Google Ads: the rows shown, then every row. Money adds up in the
  // currency that spent the most, so it matches the number on Overview.
  const kept = rows.filter((r) => keepStatus(statusFilter, r.status));
  const byCurrency = new Map<string, number>();
  for (const r of rows) byCurrency.set(r.currency, (byCurrency.get(r.currency) ?? 0) + r.spend_micros);
  const mainCurrency = [...byCurrency.entries()].sort((a, b) => b[1] - a[1])[0]?.[0];
  const what = levels.find((l) => l.id === level)!.label.toLowerCase();
  const totalRow = (label: string, testId: string, of: AdPerformanceRow[]): TableFooterRow => {
    const main = of.filter((r) => r.currency === mainCurrency);
    const cur = mainCurrency ?? "USD";
    const sum = (f: (r: AdPerformanceRow) => number | null | undefined) => main.reduce((a, r) => a + (f(r) ?? 0), 0);
    const spend = sum((r) => r.spend_micros), signups = sum((r) => r.signups), payers = sum((r) => r.new_payers);
    const per = (k: number) => (k > 0 ? Math.round(spend / k) : null);
    // The budget row adds what the enabled campaigns may spend a day.
    const budget = level !== "campaign" ? null : main.reduce((a, r) => { const c = campaignOf(r); return a + (c && c.status === "active" ? budgetOf(c) ?? 0 : 0); }, 0);
    return {
      label: byCurrency.size > 1 ? `${label} (${cur} only)` : label, testId,
      cells: {
        budget: budget === null ? null : <span title="What the enabled campaigns may spend a day">{mono(`${money(budget, cur)}/day`)}</span>,
        spend: mono(money(spend, cur)),
        clicks: mono(n(sum((r) => r.clicks))),
        impressions: mono(n(sum((r) => r.impressions))),
        signups: mono(n(signups)),
        cps: mono(money(per(signups), cur)),
        payers: mono(n(payers)),
        cpp: mono(money(per(payers), cur)),
        against: <Against cost={per(payers)} payers={payers} target={target} />,
        platform_conversions: mono(n(sum((r) => r.platform_conversions))),
        platform_all_conversions: mono(n(sum((r) => r.platform_all_conversions))),
      },
    };
  };

  // The model picked beside the last ad click, and what it adds.
  const picked_model = model === lastPaidClick ? null : models.find((m) => m.id === model) ?? { id: model, label: model, help: "one of your own models" };
  const modelNote = picked_model
    ? `The "${picked_model.label}" columns count the same sign-ups and revenue another way: ${picked_model.help}, so a campaign gets a share of each. Targets and actions still use the last ad click.`
    : "Network says is the ad network's own count, for comparison.";

  return (
    <Card
      title="Campaigns"
      description={`${end ? `From ${shown.from} to ${shown.to}` : `The last ${days} days`}. A sign-up or first payment counts for the last ad click within ${windowDays} days before it. ${target ? `Cost per customer is judged against your ${money(target, mainCurrency ?? "USD")} target.` : "Set a target and each campaign is judged against it."} Open a campaign for its ad groups, ads, keywords and searches. Source: ad network spend and clicks, Outflare sign-ups and payments. ${modelNote}`}
      action={switcher}
    >
      {error && <ErrorText>{error}</ErrorText>}
      <DataTable
        data={kept}
        footer={(shown) => [totalRow(`Total: filtered ${what}`, "campaigns-total", shown), totalRow("Total: account", "account-total", rows)]}
        rowId={(r) => r.id}
        selection={pickable ? { ids: picked, onChange: setPicked, label: (r) => r.name || r.external_id } : undefined}
        onRowClick={open}
        searchPlaceholder={`Search ${levels.find((l) => l.id === level)!.label.toLowerCase()}`}
        empty={rows.length === 0 && notes.length > 0 ? notes[0] : rows.length > 0 ? `No enabled ${what} in this range. Change Status to see the ${rows.length} paused or removed.` : `Nothing spent in the last ${days} days at this level. Numbers arrive with the next sync.`}
        initialSort={[{ id: "spend", desc: true }]}
        view={`ads-${level}`}
        hidden={["account", "new_revenue", "returning", "roas", "cps", "lifetime", "payback", "trend", "platform_all_conversions", "clicks", "impressions"]}
        toolbar={
          <>
            <StatusFilterSelect value={statusFilter} onChange={setStatusFilter} hidden={rows.length - kept.length} />
            <label className="flex items-center gap-1.5 text-[13px] text-muted-foreground">
              Also count by
              <select className={field} aria-label="Attribution model" data-testid="ads-model" value={model} onChange={(e) => pickModel(e.target.value)}>
                <option value={lastPaidClick}>Last ad click only</option>
                {models.map((m) => <option key={m.id} value={m.id}>{m.label}</option>)}
              </select>
            </label>
            {pickable && picked.length > 0 && (
              <div className="flex flex-wrap items-center gap-2 text-[13px]" role="group" aria-label="Change the ticked campaigns">
                <span className="font-medium">{picked.length} selected</span>
                <Button variant="ghost" disabled={manage.busy || chosen.every((c) => c.status !== "active")} onClick={() => bulk(manage.pauseMany)}>Pause</Button>
                <Button variant="ghost" disabled={manage.busy || chosen.every((c) => c.status !== "paused")} onClick={() => bulk(manage.resumeMany)}>Resume</Button>
                <Button variant="ghost" disabled={manage.busy || chosen.length === 0} onClick={() => bulk(manage.budgetMany)}>Change budget</Button>
                <Button variant="ghost" className="text-destructive" disabled={manage.busy || chosen.every((c) => c.connector !== "google_ads")} onClick={() => bulk(manage.removeMany)}>Remove</Button>
                <Button variant="ghost" onClick={() => setPicked([])}>Clear</Button>
                <span className="text-[12px] text-muted-foreground">One confirmation covers all of them.</span>
              </div>
            )}
          </>
        }
        download={reportDownload(`results_${level}`, shown.from, shown.to)}
        mobile={(r) => (
          <div className="space-y-1">
            <div className="flex items-center justify-between gap-2"><span className="font-medium">{r.name || r.external_id}</span><span className="flex items-center gap-1.5">{cost(r)}<Against cost={r.cost_per_new_payer_micros} payers={r.new_payers} target={target} /></span></div>
            <div className="text-[12px] text-muted-foreground">{money(r.spend_micros, r.currency)} spent · {r.roas === null ? "" : `${roasText(r.roas)} ROAS · `}{n(r.new_payers)} new {r.new_payers === 1 ? "customer" : "customers"} · {n(r.signups)} sign-ups</div>
          </div>
        )}
        columns={[
          {
            id: "name",
            header: levels.find((l) => l.id === level)!.label.replace(/s$/, ""),
            accessorFn: (r) => `${r.name} ${r.account_name}`,
            meta: { csv: (r: AdPerformanceRow) => r.name || r.external_id } satisfies ColumnMeta<AdPerformanceRow>,
            cell: ({ row: { original: r } }) => (
              <div className="min-w-48">
                <div className="font-medium text-blue-700 underline-offset-2 hover:underline dark:text-blue-400">{r.name || r.external_id}</div>
                <div className="text-[12px] text-muted-foreground">{platformName(r.connector)} · {r.account_name}</div>
              </div>
            ),
          },
          { id: "account", header: "Network", accessorFn: (r) => `${platformName(r.connector)} · ${r.account_name}`, cell: ({ row: { original: r } }) => <span className="text-[12px] text-muted-foreground">{platformName(r.connector)} · {r.account_name}</span> },
          {
            id: "status", header: "Status", accessorFn: (r) => serving(r).word,
            cell: ({ row: { original: r } }) => { const s = serving(r); return <span title={s.title}><Status tone={s.tone}>{s.word}</Status></span>; },
          },
          ...(level === "campaign" && manage.campaigns.some((c) => c.optimization_score != null) ? [{
            id: "optimization_score", header: "Optimization score", meta: { align: "right", csv: (r: AdPerformanceRow) => score(r) ?? "" } satisfies ColumnMeta<AdPerformanceRow>, accessorFn: (r: AdPerformanceRow) => score(r) ?? -1,
            cell: ({ row: { original: r } }: { row: { original: AdPerformanceRow } }) => { const v = score(r); return <span title="Google's estimate of how well the campaign is set up, out of 100%.">{mono(v === null ? "—" : `${v.toFixed(1)}%`)}</span>; },
          }] : []),
          ...(level === "campaign" && manage.campaigns.some((c) => c.budget_lost_share != null) ? [{
            id: "room", header: "Room to grow", meta: { align: "right", csv: (r: AdPerformanceRow) => { const g = room(r); return g ? `${g.budget}% budget, ${g.rank}% rank` : ""; } } satisfies ColumnMeta<AdPerformanceRow>, accessorFn: (r: AdPerformanceRow) => room(r)?.budget ?? -1,
            cell: ({ row: { original: r } }: { row: { original: AdPerformanceRow } }) => {
              const g = room(r);
              if (!g) return mono("—");
              return (
                <span className="whitespace-nowrap" data-testid="room" title={`Over the last 7 days this campaign missed ${g.budget}% of the searches it could have shown for because its budget ran out, and ${g.rank}% because other advertisers ranked higher. A raise can win back the first; better ads or bids the second. Source: Google Ads.`}>
                  <span className={`font-mono tabular-nums ${g.budget >= 10 ? "font-medium text-emerald-700 dark:text-emerald-400" : ""}`}>{g.budget}% budget</span>
                  <span className="text-muted-foreground"> · {g.rank}% rank</span>
                </span>
              );
            },
          }] : []),
          {
            id: "budget", header: "Budget", meta: { align: "right", csv: (r: AdPerformanceRow) => csvMoney(budgetOf(campaignOf(r)), r.currency) }, accessorFn: (r) => budgetOf(campaignOf(r)) ?? -1,
            cell: ({ row: { original: r } }) => {
              const c = r.level === "campaign" ? campaignOf(r) : undefined;
              const b = budgetOf(c);
              if (!c || b === null) return mono("—");
              // Set in the ad account's currency; shown in the brand's like the rest.
              return <span className="font-mono tabular-nums" title={c.report_currency !== c.currency ? `${money(c.daily_budget_micros, c.currency)} a day in the ad account` : undefined}>{money(b, c.report_currency)}/day</span>;
            },
          },
          { id: "spend", header: "Spend", meta: { align: "right", csv: (r: AdPerformanceRow) => csvMoney(r.spend_micros, r.currency) }, accessorFn: (r) => r.spend_micros, cell: ({ row: { original: r } }) => mono(money(r.spend_micros, r.currency)) },
          { id: "new_revenue", header: "New revenue", meta: { align: "right", csv: (r: AdPerformanceRow) => minorList(r.new_revenue, csvMoney) }, accessorFn: (r) => r.new_revenue.reduce((s, m) => s + m.amount_minor, 0), cell: ({ row: { original: r } }) => <span title="First payments in the range, less their refunds">{mono(minorList(r.new_revenue, money))}</span> },
          { id: "returning", header: "Returning revenue", meta: { align: "right", csv: (r: AdPerformanceRow) => minorList(r.returning_revenue, csvMoney) }, accessorFn: (r) => r.returning_revenue.reduce((s, m) => s + m.amount_minor, 0), cell: ({ row: { original: r } }) => mono(minorList(r.returning_revenue, money)) },
          { id: "roas", header: "ROAS", meta: { align: "right", csv: (r: AdPerformanceRow) => (r.roas === null ? "" : Number(r.roas.toFixed(2))) }, accessorFn: (r) => r.roas ?? -1, cell: ({ row: { original: r } }) => <span title={roasHelp} data-testid="roas">{mono(roasText(r.roas))}</span> },
          { id: "clicks", header: "Clicks", meta: { align: "right" }, accessorFn: (r) => r.clicks, cell: ({ row: { original: r } }) => mono(n(r.clicks)) },
          { id: "impressions", header: "Impressions", meta: { align: "right" }, accessorFn: (r) => r.impressions, cell: ({ row: { original: r } }) => mono(n(r.impressions)) },
          { id: "signups", header: "Sign-ups", meta: { align: "right" }, accessorFn: (r) => r.signups, cell: ({ row: { original: r } }) => mono(n(r.signups)) },
          { id: "cps", header: "Cost / sign-up", meta: { align: "right", csv: (r: AdPerformanceRow) => csvMoney(r.cost_per_signup_micros, r.currency) }, accessorFn: (r) => r.cost_per_signup_micros ?? -1, cell: ({ row: { original: r } }) => mono(money(r.cost_per_signup_micros, r.currency)) },
          { id: "payers", header: "New customers", meta: { align: "right" }, accessorFn: (r) => r.new_payers, cell: ({ row: { original: r } }) => mono(n(r.new_payers)) },
          { id: "cpp", header: "Cost per customer", meta: { align: "right", csv: (r: AdPerformanceRow) => csvMoney(r.cost_per_new_payer_micros, r.currency) }, accessorFn: (r) => r.cost_per_new_payer_micros ?? -1, cell: ({ row: { original: r } }) => cost(r) },
          { id: "against", header: "Against target", enableSorting: false, meta: { csv: (r: AdPerformanceRow) => targetWord[r.target_status] } satisfies ColumnMeta<AdPerformanceRow>, cell: ({ row: { original: r } }) => <Against cost={r.cost_per_new_payer_micros} payers={r.new_payers} target={target} /> },
          { id: "lifetime", header: "Lifetime revenue", meta: { align: "right", csv: (r: AdPerformanceRow) => minorList(r.lifetime_revenue, csvMoney) }, accessorFn: (r) => r.lifetime_revenue.reduce((s, m) => s + m.amount_minor, 0), cell: ({ row: { original: r } }) => mono(minorList(r.lifetime_revenue, money)) },
          { id: "payback", header: "Payback", meta: { align: "right", csv: (r: AdPerformanceRow) => (r.payback_months === null ? "" : Number(r.payback_months.toFixed(1))) }, accessorFn: (r) => r.payback_months ?? 999, cell: ({ row: { original: r } }) => mono(r.payback_months === null ? "—" : `${r.payback_months.toFixed(1)} mo`) },
          ...(picked_model ? [
            { id: "model_signups", header: `Sign-ups (${picked_model.label})`, meta: { align: "right", csv: (r: AdPerformanceRow) => r.model_signups ?? "" } satisfies ColumnMeta<AdPerformanceRow>, accessorFn: (r: AdPerformanceRow) => r.model_signups ?? -1, cell: ({ row: { original: r } }: { row: { original: AdPerformanceRow } }) => <span data-testid="model-signups" title={`Sign-ups in the range, counted by ${picked_model.label}: ${picked_model.help}. A campaign gets a share of each, so this isn't a whole number.`}>{mono(r.model_signups == null ? "—" : share(r.model_signups))}</span> },
            { id: "model_cps", header: `Cost / sign-up (${picked_model.label})`, meta: { align: "right", csv: (r: AdPerformanceRow) => csvMoney(r.model_cost_per_signup_micros, r.currency) } satisfies ColumnMeta<AdPerformanceRow>, accessorFn: (r: AdPerformanceRow) => r.model_cost_per_signup_micros ?? -1, cell: ({ row: { original: r } }: { row: { original: AdPerformanceRow } }) => mono(money(r.model_cost_per_signup_micros, r.currency)) },
            { id: "model_revenue", header: `Revenue (${picked_model.label})`, meta: { align: "right", csv: (r: AdPerformanceRow) => minorList(r.model_revenue, csvMoney) } satisfies ColumnMeta<AdPerformanceRow>, accessorFn: (r: AdPerformanceRow) => r.model_revenue.reduce((s, m) => s + m.amount_minor, 0), cell: ({ row: { original: r } }: { row: { original: AdPerformanceRow } }) => <span title={`The share of every payment in the range, less refunds, that ${picked_model.label} credits to this row.`}>{mono(minorList(r.model_revenue, money))}</span> },
            { id: "model_roas", header: `ROAS (${picked_model.label})`, meta: { align: "right", csv: (r: AdPerformanceRow) => (r.model_roas == null ? "" : Number(r.model_roas.toFixed(2))) } satisfies ColumnMeta<AdPerformanceRow>, accessorFn: (r: AdPerformanceRow) => r.model_roas ?? -1, cell: ({ row: { original: r } }: { row: { original: AdPerformanceRow } }) => mono(roasText(r.model_roas)) },
          ] : []),
          { id: "platform_conversions", header: "Network says", meta: { align: "right", csv: (r: AdPerformanceRow) => r.platform_conversions ?? "" }, accessorFn: (r) => r.platform_conversions ?? -1, cell: ({ row: { original: r } }) => <span title={`What ${platformName(r.connector)} says it brought, with its own attribution window`}>{mono(n(r.platform_conversions), true)}</span> },
          { id: "platform_all_conversions", header: "All conv.", meta: { align: "right", csv: (r: AdPerformanceRow) => r.platform_all_conversions ?? "" }, accessorFn: (r) => r.platform_all_conversions ?? -1, cell: ({ row: { original: r } }) => <span title={`Every conversion ${platformName(r.connector)} counted, its secondary conversion actions included`}>{mono(n(r.platform_all_conversions), true)}</span> },
          {
            id: "trend", header: "Trend", accessorFn: (r) => r.spend_change ?? (r.trend === "new" ? 99 : r.trend === "stopped" ? -99 : 0), meta: { csv: (r: AdPerformanceRow) => trendWord[r.trend] },
            cell: ({ row: { original: r } }) => trendWord[r.trend] && (
              <span title={`Spend against the ${days} days before: ${money(r.previous_spend_micros, r.currency)}`}>
                <Badge tone={trendTone[r.trend]}>{r.trend === "scaling_up" ? "↑ " : r.trend === "scaling_down" ? "↓ " : ""}{trendWord[r.trend]}{r.spend_change != null && ` ${r.spend_change > 0 ? "+" : ""}${Math.round(r.spend_change * 100)}%`}</Badge>
              </span>
            ),
          },
          {
            id: "manage", header: "", enableSorting: false, meta: { align: "right", csv: false } satisfies ColumnMeta<AdPerformanceRow>,
            cell: ({ row: { original: r } }) => {
              const c = campaignOf(r);
              if (!c) return null;
              const can = canManage && manage.manageable(c.connector) && c.status !== "removed";
              const link = manage.link(c);
              const items = [
                can && { label: "Change budget", onClick: () => manage.budget(c) },
                canManage && { label: "Set a target", onClick: () => manage.target(c) },
                link !== null && { label: `Open in ${platformName(c.connector)}`, onClick: () => { window.open(link!, "_blank", "noopener"); } },
                can && c.connector === "google_ads" && { label: "Remove", onClick: () => manage.remove(c), danger: true },
              ];
              if (!can && items.every((i) => !i)) return null;
              return (
                <div className="flex justify-end gap-1.5" onClick={(e) => e.stopPropagation()}>
                  {can && r.level === "campaign" && (c.status === "active"
                    ? <Button variant="ghost" disabled={manage.busy} onClick={() => manage.pause(c)} aria-label={`Pause ${c.name}`}>Pause</Button>
                    : <Button variant="ghost" disabled={manage.busy} onClick={() => manage.resume(c)} aria-label={`Resume ${c.name}`}>Resume</Button>)}
                  <Menu aria={`More for ${c.name}`} items={items} />
                </div>
              );
            },
          },
        ]}
      />
      {notes.length > 0 && rows.length > 0 && <ul className="mt-3 space-y-1 text-[12px] text-muted-foreground">{notes.map((x) => <li key={x}>{x}</li>)}</ul>}
      {!canManage && <p className="sr-only">Ask an admin to manage campaigns.</p>}
      {rows.length === 0 && <div className="mt-3"><Button variant="ghost" onClick={() => go("ads", "setup")}>Check the set-up</Button></div>}
    </Card>
  );
}
