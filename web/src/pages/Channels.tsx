// Channels: the CMO view. Visits, conversions, revenue and cost for every
// channel, under one attribution model the whole workspace shares (last
// non-direct touch unless changed), with a switch to compare the others.

import { useCallback, useEffect, useState } from "react";
import type { AttributionCampaignRow, AttributionChannels, AttributionSettings } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { DataTable, type ColumnDef } from "@/components/data-table";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { api, errorMessage } from "../api";
import { Button, Card, ErrorText, field, Loading, PageHeader, Stat, Stats, toast } from "../ui";
import { AttributionModels, ConversionDefinitions } from "./AttributionSetup";
import type { Tab } from "../App";

type Props = { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void };
type Model = AttributionSettings["model"];
type Row = AttributionChannels["data"][number];
type Money = Row["revenue"][number];

const builtIn: { id: Model; label: string; help: string }[] = [
  { id: "last_non_direct", label: "Last non-direct touch", help: "The last visit that wasn't direct gets all the credit." },
  { id: "first_touch", label: "First touch", help: "The first visit gets all the credit." },
  { id: "linear", label: "Linear", help: "Every visit gets an equal share." },
  { id: "position", label: "40/20/40", help: "40% to the first visit, 40% to the last, 20% shared by the rest." },
  { id: "time_decay", label: "Time decay", help: "Shared, with visits closer to the conversion getting more." },
];
const ranges = [7, 28, 90] as const;
const day = (d: Date) => d.toISOString().slice(0, 10);

export const money = (list: Money[]) =>
  list.length === 0
    ? "—"
    : list
        .map((m) => {
          try {
            return new Intl.NumberFormat(undefined, { style: "currency", currency: m.currency, maximumFractionDigits: Math.abs(m.amount_cents) < 10000 ? 2 : 0 }).format(m.amount_cents / 100);
          } catch {
            return `${(m.amount_cents / 100).toFixed(2)} ${m.currency}`;
          }
        })
        .join(" + ");
export const num = (v: number) => (Number.isInteger(v) ? v.toLocaleString() : v.toLocaleString(undefined, { maximumFractionDigits: 1 }));

// costPer is cost per conversion, and ret revenue over cost, when both are
// in one currency.
function one(list: Money[]): Money | null {
  return list.length === 1 ? list[0] : null;
}
function costPer(r: Row): string {
  const c = one(r.cost);
  if (!c || r.conversions <= 0) return "—";
  return money([{ currency: c.currency, amount_cents: Math.round(c.amount_cents / r.conversions) }]);
}
function ret(r: Row): string {
  const c = one(r.cost), v = one(r.revenue);
  if (!c || !v || c.currency !== v.currency || c.amount_cents <= 0) return "—";
  return `${(v.amount_cents / c.amount_cents).toFixed(1)}×`;
}

// totals adds up every channel: cost includes spend not tied to one.
export function totals(report: AttributionChannels) {
  const rows = report.data;
  const sum = (lists: Money[][]) => {
    const by: Record<string, number> = {};
    for (const list of lists) for (const m of list) by[m.currency] = (by[m.currency] ?? 0) + m.amount_cents;
    return Object.entries(by).map(([currency, amount_cents]) => ({ currency, amount_cents }));
  };
  return {
    visits: rows.reduce((s, r) => s + r.visits, 0),
    conversions: rows.reduce((s, r) => s + r.conversions, 0),
    revenue: sum(rows.map((r) => r.revenue)),
    cost: sum([...rows.map((r) => r.cost), report.unallocated_cost]),
  };
}

export function Channels({ ws, canManage, go }: Props) {
  const brand = useBrand(ws);
  const brandKey = brand.query.brand ?? "";
  const [days, setDays] = useState<(typeof ranges)[number]>(28);
  const [settings, setSettings] = useState<AttributionSettings | null>(null);
  const [model, setModel] = useState<Model | "">("");
  // The workspace's own models (GET .../attribution/models) after the
  // built-in ones.
  const [custom, setCustom] = useState<{ id: Model; label: string; help: string }[]>([]);
  const models = [...builtIn, ...custom];
  const [report, setReport] = useState<AttributionChannels | null>(null);
  const [open, setOpen] = useState<Row | null>(null);
  const [error, setError] = useState("");

  const range = useCallback(() => {
    const to = new Date();
    return { from: day(new Date(to.getTime() - (days - 1) * 86_400_000)), to: day(to) };
  }, [days]);

  const load = useCallback(async () => {
    const query = { ...range(), ...(brandKey ? { brand: brandKey } : {}), ...(model ? { model } : {}) };
    const [s, r, m] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/attribution/settings", { params: { path: { ws } } }),
      api.GET("/v1/workspaces/{ws}/attribution/channels", { params: { path: { ws }, query } }),
      api.GET("/v1/workspaces/{ws}/attribution/models", { params: { path: { ws } } }),
    ]);
    setError(s.error || r.error ? errorMessage(s.error ?? r.error) : "");
    if (s.data) setSettings(s.data);
    if (r.data) setReport(r.data);
    if (m.data) setCustom(m.data.data.map((x) => ({ id: x.key, label: x.name, help: `Your own model, version ${x.version}, starting from ${builtIn.find((b) => b.id === x.spec.base)?.label ?? x.spec.base}.` })));
  }, [ws, brandKey, model, range]);
  useEffect(() => {
    load();
  }, [load]);

  async function makeDefault() {
    if (!model) return;
    const { error } = await api.PATCH("/v1/workspaces/{ws}/attribution/settings", { params: { path: { ws } }, body: { model } });
    if (error) return toast(errorMessage(error), "error");
    toast(`${models.find((m) => m.id === model)?.label} is now the default for every report.`);
    setModel("");
    load();
  }

  const shown = model || settings?.model || "last_non_direct";
  const header = (
    <PageHeader
      title="Channels"
      description="Where your visits, sign-ups and revenue come from, and what each channel costs. Every report, channel and ad uses the same credit, so the numbers add up the same way everywhere. Agents use the attribution tools."
      action={
        <div className="flex flex-wrap items-center gap-2">
          <select aria-label="Attribution model" className={field} value={shown} onChange={(e) => setModel(e.target.value === settings?.model ? "" : (e.target.value as Model))}>
            {models.map((m) => <option key={m.id} value={m.id}>{m.label}{m.id === settings?.model ? " (default)" : ""}</option>)}
          </select>
          <div className="flex rounded-md border" role="group" aria-label="Date range">
            {ranges.map((r) => (
              <button key={r} type="button" className={`px-2.5 py-1 text-[13px] ${r === days ? "bg-muted font-medium" : "text-muted-foreground"}`} onClick={() => setDays(r)}>{r} days</button>
            ))}
          </div>
        </div>
      }
    />
  );
  if (!report) return <div className="space-y-6">{header}{error ? <ErrorText>{error}</ErrorText> : <Loading />}</div>;

  const rows = report.data;
  const { visits, conversions, revenue, cost } = totals(report);
  const help = models.find((m) => m.id === shown)?.help ?? "";

  const columns: ColumnDef<Row>[] = [
    { id: "channel", header: "Channel", accessorFn: (r) => r.label, cell: ({ row: { original: r } }) => <span className="font-medium">{r.label}</span> },
    { id: "visits", header: "Visits", accessorFn: (r) => r.visits, cell: ({ row: { original: r } }) => num(r.visits) },
    { id: "conversions", header: "Conversions", accessorFn: (r) => r.conversions, cell: ({ row: { original: r } }) => num(r.conversions) },
    { id: "revenue", header: "Revenue", accessorFn: (r) => r.revenue[0]?.amount_cents ?? 0, cell: ({ row: { original: r } }) => money(r.revenue) },
    { id: "cost", header: "Cost", accessorFn: (r) => r.cost[0]?.amount_cents ?? 0, cell: ({ row: { original: r } }) => money(r.cost) },
    { id: "cpa", header: "Cost per conversion", enableSorting: false, cell: ({ row: { original: r } }) => costPer(r) },
    { id: "return", header: "Return", enableSorting: false, cell: ({ row: { original: r } }) => ret(r) },
  ];

  return (
    <div className="space-y-6">
      {header}
      {error && <ErrorText>{error}</ErrorText>}
      {model && canManage && (
        <Card title={`Comparing: ${models.find((m) => m.id === model)?.label}`} description={`${help} Your default is still ${models.find((m) => m.id === settings?.model)?.label}.`}>
          <div className="flex gap-2">
            <Button onClick={makeDefault}>Make it the default</Button>
            <Button variant="ghost" onClick={() => setModel("")}>Back to the default</Button>
          </div>
        </Card>
      )}

      {visits === 0 && conversions === 0 ? (
        <Card title="No visits yet" description="Channels are worked out from your website's visits. Add the tracking snippet to your site, then tag the links you share with campaign links, and visits, sign-ups and payments show up here within minutes.">
          <div className="flex gap-2">
            <Button onClick={() => go("tracking")}>Set up website tracking</Button>
            <Button variant="ghost" onClick={() => go("links")}>Campaign links</Button>
          </div>
        </Card>
      ) : (
        <>
          <Stats cols={4}>
            <Stat label={`Visits, ${days} days`} value={num(visits)} sub={report.pending_visits > 0 ? `${num(report.pending_visits)} more being sorted` : undefined} />
            <Stat label="Conversions" value={num(conversions)} sub={report.conversion_events.join(", ")} />
            <Stat label="Revenue" value={money(revenue)} sub="net of refunds" />
            <Stat label="Cost" value={money(cost)} sub={report.unallocated_cost.length > 0 ? `${money(report.unallocated_cost)} not tied to a channel` : undefined} />
          </Stats>
          <Card title="By channel" description={`${help} Visits up to ${report.lookback_days} days before a conversion can get credit. Select a channel to see its sources and campaigns.`}>
            <DataTable
              columns={columns}
              data={rows}
              rowId={(r) => r.channel}
              onRowClick={(r) => setOpen(r)}
              empty="No channels."
              mobile={(r) => (
                <span className="block min-w-0">
                  <span className="block truncate text-[13px] font-medium">{r.label}</span>
                  <span className="block truncate text-[12px] text-muted-foreground">{num(r.visits)} visits · {num(r.conversions)} conversions · {money(r.revenue)}</span>
                </span>
              )}
            />
          </Card>
        </>
      )}
      <AttributionModels ws={ws} canManage={canManage} defaultModel={settings?.model} onChanged={load} />
      <ConversionDefinitions ws={ws} canManage={canManage} onChanged={load} />
      <Drilldown ws={ws} row={open} query={{ ...range(), ...(brandKey ? { brand: brandKey } : {}), model: shown }} onClose={() => setOpen(null)} />
    </div>
  );
}

function Drilldown({ ws, row, query, onClose }: { ws: string; row: Row | null; query: { from: string; to: string; brand?: string; model: Model }; onClose: () => void }) {
  const [list, setList] = useState<AttributionCampaignRow[] | null>(null);
  const [error, setError] = useState("");
  const channel = row?.channel;
  const key = JSON.stringify(query);
  useEffect(() => {
    if (!channel) return;
    setList(null);
    api.GET("/v1/workspaces/{ws}/attribution/campaigns", { params: { path: { ws }, query: { ...JSON.parse(key), channel } } }).then(({ data, error }) => {
      setError(error ? errorMessage(error) : "");
      setList(data?.data ?? []);
    });
  }, [ws, channel, key]);

  const columns: ColumnDef<AttributionCampaignRow>[] = [
    { id: "source", header: "Source", accessorFn: (r) => r.source, cell: ({ row: { original: r } }) => <span className="font-mono text-[12px]">{r.source}</span> },
    {
      id: "campaign", header: "Campaign", accessorFn: (r) => r.campaign_name || r.campaign,
      cell: ({ row: { original: r } }) => r.campaign_name || r.campaign || <span className="text-muted-foreground">None</span>,
    },
    { id: "visits", header: "Visits", accessorFn: (r) => r.visits, cell: ({ row: { original: r } }) => num(r.visits) },
    { id: "conversions", header: "Conversions", accessorFn: (r) => r.conversions, cell: ({ row: { original: r } }) => num(r.conversions) },
    { id: "revenue", header: "Revenue", accessorFn: (r) => r.revenue[0]?.amount_cents ?? 0, cell: ({ row: { original: r } }) => money(r.revenue) },
  ];
  return (
    <Sheet open={!!row} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full sm:max-w-2xl">
        <SheetHeader>
          <SheetTitle>{row?.label}</SheetTitle>
          <SheetDescription>Sources and campaigns in this channel, most conversions first.</SheetDescription>
        </SheetHeader>
        <div className="mt-4 space-y-3">
          <ErrorText>{error}</ErrorText>
          {!list ? <Loading /> : (
            <DataTable
              columns={columns}
              data={list}
              rowId={(r) => `${r.source}|${r.campaign}|${r.tracking_campaign_id ?? ""}`}
              searchPlaceholder="Search sources and campaigns"
              empty="Nothing in this channel for these dates."
              mobile={(r) => (
                <span className="block min-w-0">
                  <span className="block truncate text-[13px] font-medium">{r.campaign_name || r.campaign || r.source}</span>
                  <span className="block truncate text-[12px] text-muted-foreground">{r.source} · {num(r.visits)} visits · {num(r.conversions)} conversions</span>
                </span>
              )}
            />
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}
