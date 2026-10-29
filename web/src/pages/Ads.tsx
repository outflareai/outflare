import { useCallback, useEffect, useState } from "react";
import type { AdAccount, AdCampaign, AdConnection, AdSummaryRow, AdTotals } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { BrandBadge } from "@/components/brand-badge";
import { DataTable } from "@/components/data-table";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, ErrorText, Input, Loading, PageHeader, Stat, Stats, toast } from "../ui";
import { ago } from "../format";
import type { Tab } from "../App";
import { AdConversions } from "./AdConversions";
import { AdTrackingHealthCard, AdTrackingTemplates } from "./AdTracking";
import { AdResults } from "./AdResults";

// Paid ads: spend, clicks and what each platform says it brought in, read
// every six hours from the workspace's own ad accounts. Conversions and ROAS
// are the platform's own claims, so they are never added across platforms.

type Props = { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void };

const platforms: { id: string; name: string }[] = [
  { id: "google_ads", name: "Google Ads" },
  { id: "meta_ads", name: "Meta Ads" },
  { id: "microsoft_ads", name: "Microsoft Advertising" },
  { id: "reddit_ads", name: "Reddit Ads" },
  { id: "x_ads", name: "X Ads" },
  { id: "linkedin_ads", name: "LinkedIn Ads" },
  { id: "tiktok_ads", name: "TikTok Ads" },
  { id: "snapchat_ads", name: "Snapchat Ads" },
  { id: "taboola_ads", name: "Taboola Ads" },
  { id: "outbrain_ads", name: "Outbrain Ads" },
  { id: "apple_ads", name: "Apple Ads" },
  { id: "amazon_ads", name: "Amazon Ads" },
  { id: "pinterest_ads", name: "Pinterest Ads" },
];
const platformName = (id: string) => platforms.find((p) => p.id === id)?.name ?? id;

const ranges = [7, 28, 90] as const;
const day = (d: Date) => d.toISOString().slice(0, 10);

// Money arrives in micros (millionths) of the account's currency.
const money = (micros: number | null | undefined, currency: string) => {
  if (micros === null || micros === undefined) return "—";
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency, minimumFractionDigits: micros >= 10_000_000 ? 0 : 2, maximumFractionDigits: micros >= 10_000_000 ? 0 : 2 }).format(micros / 1e6);
  } catch {
    return `${(micros / 1e6).toFixed(2)} ${currency}`;
  }
};
const n = (v: number | null | undefined) => (v === null || v === undefined ? "—" : Math.round(v).toLocaleString());
const pct = (v: number | null | undefined) => (v === null || v === undefined ? "—" : `${(v * 100).toFixed(2)}%`);
const roas = (v: number | null | undefined) => (v === null || v === undefined ? "—" : `${v.toFixed(2)}×`);

export function Ads({ ws, canManage, go }: Props) {
  const brand = useBrand(ws);
  const brandKey = brand.query.brand ?? "";
  const [days, setDays] = useState<(typeof ranges)[number]>(28);
  const [conns, setConns] = useState<AdConnection[] | null>(null);
  const [accounts, setAccounts] = useState<AdAccount[]>([]);
  const [campaigns, setCampaigns] = useState<AdCampaign[]>([]);
  const [byPlatform, setByPlatform] = useState<AdSummaryRow[]>([]);
  const [totals, setTotals] = useState<AdSummaryRow[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    const path = { ws };
    const query = brandKey ? { brand: brandKey } : {};
    const to = new Date();
    const range = { from: day(new Date(to.getTime() - (days - 1) * 86_400_000)), to: day(to) };
    const [c, a, cp, s] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/ads/connections", { params: { path } }),
      api.GET("/v1/workspaces/{ws}/ads/accounts", { params: { path, query } }),
      api.GET("/v1/workspaces/{ws}/ads/campaigns", { params: { path, query: { ...query, ...range, limit: 200 } } }),
      api.GET("/v1/workspaces/{ws}/ads/summary", { params: { path, query: { ...query, ...range, group: "connector" } } }),
    ]);
    if (c.error || !c.data) return setError(errorMessage(c.error));
    setError(a.error || cp.error || s.error ? errorMessage(a.error ?? cp.error ?? s.error) : "");
    setConns(c.data.data);
    setAccounts(a.data?.data ?? []);
    setCampaigns(cp.data?.data ?? []);
    setByPlatform(s.data?.data ?? []);
    setTotals(s.data?.totals ?? []);
  }, [ws, brandKey, days]);
  useEffect(() => {
    load();
  }, [load]);

  async function syncNow() {
    setBusy(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/ads/sync", { params: { path: { ws } } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast("Syncing your ad accounts now. Numbers update in a minute or two.");
    setTimeout(load, 8000);
  }

  async function update(a: AdAccount, body: { brand?: string; sync_on?: boolean }) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/ads/accounts/{id}", { params: { path: { ws, id: a.id } }, body });
    if (error) return toast(errorMessage(error), "error");
    load();
  }

  const header = (
    <PageHeader
      title="Paid ads"
      description="Spend, clicks and the conversions each platform reports, from your own ad accounts, by brand. Outflare never changes your campaigns; it only sends conversions back when you add a rule. Agents use the ads tools."
      action={
        <div className="flex items-center gap-2">
          <div className="flex rounded-md border" role="group" aria-label="Date range">
            {ranges.map((r) => (
              <button key={r} type="button" className={`px-2.5 py-1 text-[13px] ${r === days ? "bg-muted font-medium" : "text-muted-foreground"}`} onClick={() => setDays(r)}>{r} days</button>
            ))}
          </div>
          {canManage && accounts.length > 0 && <Button variant="ghost" disabled={busy} onClick={syncNow}>Sync now</Button>}
        </div>
      }
    />
  );
  if (error && !conns) return <div className="space-y-6">{header}<ErrorText>{error}</ErrorText></div>;
  if (!conns) return <div className="space-y-6">{header}<Loading /></div>;

  if (conns.length === 0)
    return (
      <div className="space-y-6">
        {header}
        <Card title="Connect an ad account" description="Google Ads, Meta Ads, Microsoft Advertising, Reddit Ads, X Ads, LinkedIn Ads, TikTok Ads, Snapchat Ads, Taboola Ads, Outbrain Ads, Apple Ads, Amazon Ads and Pinterest Ads. Each one is your own account: Outflare never provides one, and nothing is read until you connect it.">
          {canManage ? <Button onClick={() => go("connectors", "ads")}>Connect an ad platform</Button> : <p className="text-[13px] text-muted-foreground">Ask an admin to connect your ad accounts.</p>}
        </Card>
        <AdTrackingTemplates ws={ws} />
      </div>
    );

  return (
    <div className="space-y-6">
      {header}
      {error && <ErrorText>{error}</ErrorText>}

      {conns.filter((c) => c.setup_needed.length > 0).map((c) => <Setup key={c.id} ws={ws} conn={c} canManage={canManage} done={load} />)}

      {totals.length === 0 ? null : totals.map((t) => (
        <Stats key={t.currency} cols={5}>
          <Stat label={totals.length > 1 ? `Spend, ${t.currency}` : `Spend, ${days} days`} value={money(t.totals.spend_micros, t.currency)} />
          <Stat label="Clicks" value={n(t.totals.clicks)} sub={`${n(t.totals.impressions)} impressions`} />
          <Stat label="CTR" value={pct(t.totals.ctr)} />
          <Stat label="Cost per click" value={money(t.totals.cpc_micros, t.currency)} />
          <Stat label="Platform conversions" value={n(t.totals.conversions)} sub="as each platform counts them" />
        </Stats>
      ))}

      <Card title="By platform" description={`The last ${days} days. Conversions and ROAS are what each platform reports, with its own attribution window, so they are not added up across platforms.`}>
        <DataTable
          data={byPlatform}
          rowId={(r) => `${r.key}-${r.currency}`}
          columnsMenu={false}
          empty="Nothing spent in this range yet."
          initialSort={[{ id: "spend", desc: true }]}
          columns={[
            { id: "platform", header: "Platform", accessorFn: (r) => platformName(r.key), cell: ({ row: { original: r } }) => <span className="font-medium">{platformName(r.key)}</span> },
            ...metricColumns<AdSummaryRow>((r) => r.totals, (r) => r.currency),
          ]}
        />
      </Card>

      <Card title="Campaigns" description={`Ranked by spend over the last ${days} days.`}>
        <DataTable
          data={campaigns}
          rowId={(c) => c.id}
          searchPlaceholder="Search campaigns"
          empty="Campaigns appear after the first sync."
          initialSort={[{ id: "spend", desc: true }]}
          columns={[
            {
              id: "campaign",
              header: "Campaign",
              accessorFn: (c) => `${c.name} ${c.account_name}`,
              cell: ({ row: { original: c } }) => (
                <div className="space-y-0.5">
                  <div className="font-medium">{c.name}</div>
                  <div className="text-[12px] text-muted-foreground">
                    {platformName(c.connector)} · {c.account_name}
                    {c.status !== "active" && <> · <Badge tone={c.status === "removed" ? "red" : "grey"} title={c.provider_status}>{c.status === "paused" ? "Paused" : c.status === "removed" ? "Removed" : c.provider_status || "Other"}</Badge></>}
                  </div>
                </div>
              ),
            },
            { id: "budget", header: "Daily budget", accessorFn: (c) => c.daily_budget_micros ?? -1, cell: ({ row: { original: c } }) => <span className="font-mono tabular-nums">{money(c.daily_budget_micros, c.currency)}</span> },
            ...metricColumns<AdCampaign>((c) => c.totals, (c) => c.currency),
            { id: "brand", header: "Brand", accessorFn: (c) => brand.byId(c.brand_id)?.name ?? "", cell: ({ row: { original: c } }) => <BrandBadge id={c.brand_id} /> },
          ]}
        />
      </Card>

      <AdResults ws={ws} from={day(new Date(Date.now() - (days - 1) * 86_400_000))} to={day(new Date())} brand={brandKey} go={go} />

      <AdConversions ws={ws} canManage={canManage} accounts={accounts} />

      <Card title="Ad accounts" description="Every account your connections can see. Give each a brand so its spend counts there, in the costs ledger too; switch one off to stop syncing it.">
        <DataTable
          data={accounts}
          rowId={(a) => a.id}
          searchPlaceholder="Search accounts"
          empty="Accounts appear after the first sync."
          columns={[
            {
              id: "account",
              header: "Account",
              accessorFn: (a) => `${a.name} ${a.external_id}`,
              cell: ({ row: { original: a } }) => (
                <div className="space-y-0.5">
                  <div className="font-medium">{a.name || a.external_id}</div>
                  <div className="text-[12px] text-muted-foreground">
                    {platformName(a.connector)} · {a.currency} · {a.time_zone}
                    {a.status === "gone" && <> · <Badge tone="grey" title="The sign-in no longer sees this account. Its history is kept.">Gone</Badge></>}
                  </div>
                </div>
              ),
            },
            {
              id: "brand",
              header: "Brand",
              accessorFn: (a) => brand.byId(a.brand_id)?.name ?? "",
              cell: ({ row: { original: a } }) =>
                canManage && brand.active.length > 0 ? (
                  <select className="h-8 rounded-md border bg-background px-2 text-[13px]" value={a.brand_id ?? ""} onChange={(e) => update(a, { brand: e.target.value || "none" })} aria-label="Brand">
                    <option value="">Every brand</option>
                    {brand.active.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
                  </select>
                ) : (
                  <BrandBadge id={a.brand_id} />
                ),
            },
            { id: "synced", header: "Synced", accessorFn: (a) => a.last_synced_at ?? "", cell: ({ row: { original: a } }) => <span className={a.last_error ? "text-red-600 dark:text-red-400" : "text-muted-foreground"} title={a.last_error ?? undefined}>{a.sync_on ? (a.last_synced_at ? ago(a.last_synced_at) : "Not yet") : "Off"}</span> },
            {
              id: "actions",
              header: "",
              enableSorting: false,
              cell: ({ row: { original: a } }) => canManage && a.status !== "gone" && <Button variant="ghost" onClick={() => update(a, { sync_on: !a.sync_on })}>{a.sync_on ? "Stop syncing" : "Sync"}</Button>,
            },
          ]}
        />
      </Card>

      <AdTrackingHealthCard ws={ws} brand={brandKey || undefined} />
      <AdTrackingTemplates ws={ws} />

      <Card title="Connections" description="Your ad platform sign-ins. Add or remove one under Connectors.">
        <div className="space-y-2 text-[13px]">
          {conns.map((c) => (
            <div key={c.id} className="flex flex-wrap items-center justify-between gap-2">
              <div>
                <span className="font-medium">{platformName(c.connector)}</span> <span className="text-muted-foreground">{c.name !== platformName(c.connector) && `· ${c.name} `}· {c.accounts === 1 ? "1 account" : `${c.accounts} accounts`}</span>
              </div>
              <div className={c.last_error ? "text-red-600 dark:text-red-400" : "text-muted-foreground"} title={c.last_error ?? undefined}>
                {c.setup_needed.length > 0 ? "Waiting for setup" : c.last_error ? "Last sync failed" : c.last_ok_at ? `Synced ${ago(c.last_ok_at)}` : "Not synced yet"}
              </div>
            </div>
          ))}
          {canManage && <Button variant="ghost" onClick={() => go("connectors", "ads")}>Connect another</Button>}
        </div>
      </Card>
    </div>
  );
}

// The spend, clicks and platform-reported results columns, shared by the
// platform and campaign tables.
function metricColumns<T>(totals: (r: T) => AdTotals, currency: (r: T) => string) {
  const mono = (s: string) => <span className="font-mono tabular-nums">{s}</span>;
  return [
    { id: "spend", header: "Spend", accessorFn: (r: T) => totals(r).spend_micros, cell: ({ row: { original: r } }: { row: { original: T } }) => mono(money(totals(r).spend_micros, currency(r))) },
    { id: "clicks", header: "Clicks", accessorFn: (r: T) => totals(r).clicks, cell: ({ row: { original: r } }: { row: { original: T } }) => mono(n(totals(r).clicks)) },
    { id: "ctr", header: "CTR", accessorFn: (r: T) => totals(r).ctr ?? -1, cell: ({ row: { original: r } }: { row: { original: T } }) => mono(pct(totals(r).ctr)) },
    { id: "cpc", header: "CPC", accessorFn: (r: T) => totals(r).cpc_micros ?? -1, cell: ({ row: { original: r } }: { row: { original: T } }) => mono(money(totals(r).cpc_micros, currency(r))) },
    { id: "conversions", header: "Conv.", accessorFn: (r: T) => totals(r).conversions ?? -1, cell: ({ row: { original: r } }: { row: { original: T } }) => mono(n(totals(r).conversions)) },
    { id: "cpa", header: "Cost / conv.", accessorFn: (r: T) => totals(r).cost_per_conversion_micros ?? -1, cell: ({ row: { original: r } }: { row: { original: T } }) => mono(money(totals(r).cost_per_conversion_micros, currency(r))) },
    { id: "roas", header: "Platform ROAS", accessorFn: (r: T) => totals(r).platform_roas ?? -1, cell: ({ row: { original: r } }: { row: { original: T } }) => mono(roas(totals(r).platform_roas)) },
  ];
}

// A connection that still needs a field before it can sync, such as the
// workspace's own Google Ads or Microsoft Advertising developer token. The
// value is checked with the platform, then stored sealed; it is never shown
// again.
function Setup({ ws, conn, canManage, done }: { ws: string; conn: AdConnection; canManage: boolean; done: () => void }) {
  const [values, setValues] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);

  async function save() {
    setBusy(true);
    const { error } = await api.PUT("/v1/workspaces/{ws}/ads/connections/{id}/setup", { params: { path: { ws, id: conn.id } }, body: { values } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast(`${platformName(conn.connector)} is set up. The first sync reads 90 days.`);
    done();
  }

  return (
    <Card title={`Finish setting up ${platformName(conn.connector)}`} description="This platform needs one more thing from your own account before Outflare can read it. Outflare checks it with the platform and stores it encrypted.">
      {canManage ? (
        <form className="space-y-3" onSubmit={(e) => { e.preventDefault(); save(); }}>
          {conn.setup_needed.map((f) => (
            <Input key={f.name} label={f.label} hint={f.help} type={f.secret ? "password" : "text"} autoComplete="off" required={!f.optional} value={values[f.name] ?? ""} onChange={(e) => setValues((v) => ({ ...v, [f.name]: e.target.value }))} />
          ))}
          <Button type="submit" disabled={busy}>Save and sync</Button>
        </form>
      ) : (
        <p className="text-[13px] text-muted-foreground">Ask an admin to finish setting it up.</p>
      )}
    </Card>
  );
}
