// Results: every campaign (or ad group, or ad) of the brand's ad accounts
// over the range, scored in Outflare's own count: sign-ups and new paying
// customers, what each cost against the target, the revenue they brought
// and keep bringing, and payback. The network's own conversion count sits
// at the end, greyed: what Google says, never the judge.

import { useEffect, useState } from "react";
import type { AdCampaign } from "@outflare/sdk";
import type { Tab } from "../../App";
import { api, errorMessage } from "../../api";
import { DataTable, type ColumnMeta } from "@/components/data-table";
import { csvMoney } from "@/lib/csv";
import { Badge, Button, Card, ErrorText, Loading, Menu } from "../../ui";
import { money, n, platformName, reportDownload, scopeQuery, statusColumn, type AdScope } from "../Ads";
import { minorList, rangeOf, type AdPerformanceRow, type Customers, type Level } from "./api";

const levels: { id: Level; label: string }[] = [
  { id: "campaign", label: "Campaigns" },
  { id: "ad_group", label: "Ad groups" },
  { id: "ad", label: "Ads" },
];
const targetTone = { over: "red", under: "amber", on_target: "green", no_target: "grey", no_spend: "grey" } as const;
const targetWord = { over: "Over", under: "Under", on_target: "On target", no_target: "", no_spend: "" } as const;
const trendWord = { scaling_up: "Scaling up", scaling_down: "Scaling down", steady: "Steady", new: "New", stopped: "Stopped", idle: "" } as const;
const trendTone = { scaling_up: "green", scaling_down: "amber", steady: "grey", new: "blue", stopped: "grey", idle: "grey" } as const;

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

export function Results({ brand, days, end, windowDays, customers, scope, canManage, manage, go, version = 0 }: Props) {
  const [level, setLevel] = useState<Level>("campaign");
  const [rows, setRows] = useState<AdPerformanceRow[] | null>(null);
  const [target, setTarget] = useState<number | null>(null);
  const [notes, setNotes] = useState<string[]>([]);
  const [error, setError] = useState("");

  useEffect(() => {
    let live = true;
    setRows(null);
    const range = rangeOf(days, end ? new Date(`${end}T12:00:00Z`) : undefined);
    api.GET("/v1/brands/{brand}/ads/performance", { params: { path: { brand }, query: { from: range.from, to: range.to, level, window_days: windowDays, customers, ...scopeQuery(scope) } } }).then(({ data, error }) => {
      if (!live) return;
      setError(error ? errorMessage(error) : "");
      setRows(data?.data ?? []);
      setTarget(data?.target_cost_per_payer_micros ?? null);
      setNotes(data?.notes ?? []);
    });
    return () => {
      live = false;
    };
  }, [brand, days, end, windowDays, customers, level, scope.network, scope.account, version]);

  const open = (r: AdPerformanceRow) => {
    if (r.level === "campaign") return go("ads", r.id);
    if (r.level === "ad_group") return go("ads", `${r.campaign_id ?? r.id}/${r.id}`);
    if (r.campaign_id) go("ads", r.campaign_id);
  };
  const campaignOf = (r: AdPerformanceRow) => {
    const id = r.level === "campaign" ? r.id : r.campaign_id;
    return manage.campaigns.find((c) => c.id === id || (r.level === "campaign" && c.external_id === r.external_id));
  };

  const switcher = (
    <div className="flex rounded-md border" role="group" aria-label="Level">
      {levels.map((l) => (
        <button key={l.id} type="button" aria-pressed={l.id === level} className={`px-2.5 py-1 text-[13px] ${l.id === level ? "bg-muted font-medium" : "text-muted-foreground"}`} onClick={() => setLevel(l.id)}>{l.label}</button>
      ))}
    </div>
  );

  // The range the rows are for, as the download and description name it.
  const shown = rangeOf(days, end ? new Date(`${end}T12:00:00Z`) : undefined);
  if (error && !rows) return <ErrorText>{error}</ErrorText>;
  if (!rows) return <Loading />;

  const cost = (r: AdPerformanceRow) => (
    <div className="flex items-center justify-end gap-1.5">
      <span className={`font-mono tabular-nums ${r.target_status === "over" ? "text-red-600 dark:text-red-400" : r.target_status === "under" ? "text-amber-600 dark:text-amber-400" : r.target_status === "on_target" ? "text-green-600 dark:text-green-400" : ""}`}>{money(r.cost_per_new_payer_micros, r.currency)}</span>
      {targetWord[r.target_status] && <Badge tone={targetTone[r.target_status]} title={target ? `Against the brand's ${money(target, r.currency)} target, within a tenth is on target` : undefined}>{targetWord[r.target_status]}</Badge>}
    </div>
  );

  return (
    <Card
      title="Results"
      description={`${end ? `From ${shown.from} to ${shown.to}` : `The last ${days} days`}, in Outflare's own count: a sign-up or first payment counts for the last ad click within ${windowDays} days before it. ${target ? "Cost per new customer is coloured against your target." : "Set a target and cost per new customer is coloured against it."} Open a row for its own page.`}
      action={switcher}
    >
      {error && <ErrorText>{error}</ErrorText>}
      <DataTable
        data={rows}
        rowId={(r) => r.id}
        onRowClick={open}
        searchPlaceholder={`Search ${levels.find((l) => l.id === level)!.label.toLowerCase()}`}
        empty={rows.length === 0 && notes.length > 0 ? notes[0] : `Nothing spent in the last ${days} days at this level. Numbers arrive with the next sync.`}
        initialSort={[{ id: "spend", desc: true }]}
        hidden={["platform_conversions", "platform_all_conversions", "clicks", "impressions"]}
        download={reportDownload(`results_${level}`, shown.from, shown.to)}
        mobile={(r) => (
          <div className="space-y-1">
            <div className="flex items-center justify-between gap-2"><span className="font-medium">{r.name || r.external_id}</span>{cost(r)}</div>
            <div className="text-[12px] text-muted-foreground">{money(r.spend_micros, r.currency)} spent · {n(r.new_payers)} new {r.new_payers === 1 ? "customer" : "customers"} · {n(r.signups)} sign-ups</div>
          </div>
        )}
        columns={[
          {
            id: "name",
            header: levels.find((l) => l.id === level)!.label.replace(/s$/, ""),
            accessorFn: (r) => `${r.name} ${r.account_name}`,
            meta: { csv: (r: AdPerformanceRow) => r.name || r.external_id } satisfies ColumnMeta<AdPerformanceRow>,
            cell: ({ row: { original: r } }) => (
              <div className="min-w-48 font-medium text-blue-700 underline-offset-2 hover:underline dark:text-blue-400">{r.name || r.external_id}</div>
            ),
          },
          { id: "account", header: "Network", accessorFn: (r) => `${platformName(r.connector)} · ${r.account_name}`, cell: ({ row: { original: r } }) => <span className="text-[12px] text-muted-foreground">{platformName(r.connector)} · {r.account_name}</span> },
          statusColumn<AdPerformanceRow>(),
          { id: "spend", header: "Spend", meta: { align: "right", csv: (r: AdPerformanceRow) => csvMoney(r.spend_micros, r.currency) }, accessorFn: (r) => r.spend_micros, cell: ({ row: { original: r } }) => mono(money(r.spend_micros, r.currency)) },
          { id: "clicks", header: "Clicks", meta: { align: "right" }, accessorFn: (r) => r.clicks, cell: ({ row: { original: r } }) => mono(n(r.clicks)) },
          { id: "impressions", header: "Impressions", meta: { align: "right" }, accessorFn: (r) => r.impressions, cell: ({ row: { original: r } }) => mono(n(r.impressions)) },
          { id: "signups", header: "Sign-ups", meta: { align: "right" }, accessorFn: (r) => r.signups, cell: ({ row: { original: r } }) => mono(n(r.signups)) },
          { id: "cps", header: "Cost / sign-up", meta: { align: "right", csv: (r: AdPerformanceRow) => csvMoney(r.cost_per_signup_micros, r.currency) }, accessorFn: (r) => r.cost_per_signup_micros ?? -1, cell: ({ row: { original: r } }) => mono(money(r.cost_per_signup_micros, r.currency)) },
          { id: "payers", header: "New customers", meta: { align: "right" }, accessorFn: (r) => r.new_payers, cell: ({ row: { original: r } }) => mono(n(r.new_payers)) },
          { id: "cpp", header: "Cost / new customer", meta: { align: "right", csv: (r: AdPerformanceRow) => csvMoney(r.cost_per_new_payer_micros, r.currency) }, accessorFn: (r) => r.cost_per_new_payer_micros ?? -1, cell: ({ row: { original: r } }) => cost(r) },
          { id: "returning", header: "Returning revenue", meta: { align: "right", csv: (r: AdPerformanceRow) => minorList(r.returning_revenue, csvMoney) }, accessorFn: (r) => r.returning_revenue.reduce((s, m) => s + m.amount_minor, 0), cell: ({ row: { original: r } }) => mono(minorList(r.returning_revenue, money)) },
          { id: "lifetime", header: "Lifetime revenue", meta: { align: "right", csv: (r: AdPerformanceRow) => minorList(r.lifetime_revenue, csvMoney) }, accessorFn: (r) => r.lifetime_revenue.reduce((s, m) => s + m.amount_minor, 0), cell: ({ row: { original: r } }) => mono(minorList(r.lifetime_revenue, money)) },
          { id: "payback", header: "Payback", meta: { align: "right", csv: (r: AdPerformanceRow) => (r.payback_months === null ? "" : Number(r.payback_months.toFixed(1))) }, accessorFn: (r) => r.payback_months ?? 999, cell: ({ row: { original: r } }) => mono(r.payback_months === null ? "—" : `${r.payback_months.toFixed(1)} mo`) },
          { id: "platform_conversions", header: "Platform conv.", meta: { align: "right", csv: (r: AdPerformanceRow) => r.platform_conversions ?? "" }, accessorFn: (r) => r.platform_conversions ?? -1, cell: ({ row: { original: r } }) => <span title={`What ${platformName(r.connector)} says it brought, with its own attribution window`}>{mono(n(r.platform_conversions), true)}</span> },
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
            id: "manage", header: "", enableSorting: false,
            cell: ({ row: { original: r } }) => {
              const c = campaignOf(r);
              if (!canManage || !c) return null;
              const can = manage.manageable(c.connector) && c.status !== "removed";
              return (
                <div onClick={(e) => e.stopPropagation()}>
                  <Menu label="Manage" aria={`Manage ${c.name}`} items={[
                    { label: "Target cost per conversion", onClick: () => manage.target(c) },
                    can && { label: "Daily budget", onClick: () => manage.budget(c) },
                    can && (c.status === "active" ? { label: "Pause", onClick: () => manage.pause(c) } : { label: "Resume", onClick: () => manage.resume(c) }),
                    can && c.connector === "google_ads" && { label: "Remove", onClick: () => manage.remove(c), danger: true },
                  ]} />
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
