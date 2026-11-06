import { useCallback, useEffect, useState } from "react";
import type { AdAccount, AdCampaign, AdChange, AdConnection, AdSummaryRow, AdTotals, AttributionAdRow } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { BrandBadge } from "@/components/brand-badge";
import { DataTable } from "@/components/data-table";
import { api, errorMessage } from "../api";
import { track } from "@/lib/analytics";
import { ask, Badge, Button, Card, confirmAction, ErrorText, Input, Loading, PageHeader, Stat, Stats, toast } from "../ui";
import { ago } from "../format";
import type { Tab } from "../App";
import { AdConversions } from "./AdConversions";
import { AdTrackingFix, AdTrackingHealthCard, AdTrackingTemplates } from "./AdTracking";
import { AdResults } from "./AdResults";
import { AdSuggestions } from "./AdSuggestions";
import { AdDrafts } from "./AdDrafts";
import { AdTests } from "./AdTests";
import { AdTrend, CampaignDetail, ConversionActions, outflareColumns } from "./AdDrilldown";
import { AdsPanels } from "@/components/insight-panels";

// Paid ads: spend, clicks and what each platform says it brought in, read
// every six hours from the workspace's own ad accounts. Conversions and ROAS
// are the platform's own claims, so they are never added across platforms.

// item opens one campaign ({campaign id}) or one of its ad groups
// ({campaign id}/{ad group id}) as a page of its own.
type Props = { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void; item?: string };

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
export const platformName = (id: string) => platforms.find((p) => p.id === id)?.name ?? id;

// An account's id as its platform shows it (Google: 123-456-7890), and a
// link that opens it there, when the platform has one.
export const accountId = (connector: string, id: string) => {
  const digits = id.replace(/\D/g, "");
  return connector === "google_ads" && digits.length === 10 ? `${digits.slice(0, 3)}-${digits.slice(3, 6)}-${digits.slice(6)}` : id;
};
export const accountLink = (connector: string, id: string) => {
  const digits = id.replace(/\D/g, "");
  if (!digits) return null;
  if (connector === "google_ads") return `https://ads.google.com/aw/overview?__e=${digits}`;
  if (connector === "meta_ads") return `https://adsmanager.facebook.com/adsmanager/manage/campaigns?act=${digits}`;
  return null;
};

// A platform's own word (performance_max, maximize_conversions) as words.
const words = (v: string) => (v && v !== "unknown" && v !== "unspecified" ? v.charAt(0).toUpperCase() + v.slice(1).replace(/_/g, " ") : "");

const targetTone = { over: "red", under: "green", on_target: "blue", no_target: "grey", no_spend: "grey" } as const;
const targetWord = { over: "Over target", under: "Under target", on_target: "On target", no_target: "", no_spend: "No spend" } as const;
const trendWord = { scaling_up: "Scaling up", scaling_down: "Scaling down", steady: "Steady", new: "New", stopped: "Stopped", idle: "" } as const;
const trendTone = { scaling_up: "green", scaling_down: "amber", steady: "grey", new: "blue", stopped: "grey", idle: "grey" } as const;

function AccountName({ a }: { a: AdAccount }) {
  const link = accountLink(a.connector, a.external_id);
  return (
    <div className="space-y-0.5">
      <div className="font-medium">{a.name || accountId(a.connector, a.external_id)}</div>
      <div className="text-[12px] text-muted-foreground">
        {platformName(a.connector)} · <span className="font-mono">{accountId(a.connector, a.external_id)}</span> · {a.currency}
        {a.time_zone && <> · {a.time_zone}</>}
        {link && <> · <a className="underline underline-offset-2 hover:text-foreground" href={link} target="_blank" rel="noreferrer" onClick={(e) => e.stopPropagation()}>Open in {platformName(a.connector)}</a></>}
        {a.status === "gone" && <> · <Badge tone="grey" title="The sign-in no longer sees this account. Its history is kept.">Gone</Badge></>}
      </div>
    </div>
  );
}

const ranges = [7, 28, 90] as const;
const day = (d: Date) => d.toISOString().slice(0, 10);

// Money arrives in micros (millionths) of the account's currency.
export const money = (micros: number | null | undefined, currency: string) => {
  if (micros === null || micros === undefined) return "—";
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency, minimumFractionDigits: micros >= 10_000_000 ? 0 : 2, maximumFractionDigits: micros >= 10_000_000 ? 0 : 2 }).format(micros / 1e6);
  } catch {
    return `${(micros / 1e6).toFixed(2)} ${currency}`;
  }
};
export const n = (v: number | null | undefined) => (v === null || v === undefined ? "—" : Math.round(v).toLocaleString());
const pct = (v: number | null | undefined) => (v === null || v === undefined ? "—" : `${(v * 100).toFixed(2)}%`);
const roas = (v: number | null | undefined) => (v === null || v === undefined ? "—" : `${v.toFixed(2)}×`);

// The platforms whose campaigns Outflare can change (connectors.AdsManager).
const manageable = new Set(["google_ads", "meta_ads", "microsoft_ads", "linkedin_ads", "tiktok_ads"]);

// The page's tabs, as the item in its URL (the overview has none).
const adsTabs = ["overview", "suggestions", "tests", "campaigns", "changes", "tracking", "conversions", "accounts"] as const;

// The level the page shows: every network, one network, or one account
// of it. The same page narrows; the breadcrumb widens it again. It is
// kept for the tab's session, so opening a campaign and coming back
// keeps it.
export type AdScope = { network?: string; account?: string };
const scopeKey = (ws: string) => `outflare.ads.scope.${ws}`;
function readScope(ws: string): AdScope {
  try {
    const v = JSON.parse(sessionStorage.getItem(scopeKey(ws)) ?? "{}");
    return { network: typeof v.network === "string" ? v.network : undefined, account: typeof v.account === "string" ? v.account : undefined };
  } catch {
    return {};
  }
}
// The query a level adds to every ads request.
export const scopeQuery = (s: AdScope) => ({ ...(s.network ? { connector: s.network } : {}), ...(s.account ? { account_id: s.account } : {}) });
type AdsTab = (typeof adsTabs)[number];

export function Ads({ ws, canManage, go, item }: Props) {
  // The item is a tab, or a campaign's id (and its ad group's) for its
  // own page.
  const tab: AdsTab = (adsTabs as readonly string[]).includes(item ?? "") ? (item as AdsTab) : "overview";
  const [openId, openGroup] = tab === "overview" && item ? item.split("/") : [];
  const brand = useBrand(ws);
  const brandKey = brand.query.brand ?? "";
  const brandId = brand.brand?.id ?? "";
  const [days, setDays] = useState<(typeof ranges)[number]>(28);
  const [scope, setScopeState] = useState<AdScope>(() => readScope(ws));
  const setScope = (next: AdScope) => {
    setScopeState(next);
    try {
      sessionStorage.setItem(scopeKey(ws), JSON.stringify(next));
    } catch {
      // Private windows: the level just isn't kept.
    }
    track("ui.ads_level_opened", { level: next.account ? "account" : next.network ? "network" : "all", ...(next.network ? { connector: next.network } : {}) });
  };
  const [byAccount, setByAccount] = useState<AdSummaryRow[]>([]);
  const [conns, setConns] = useState<AdConnection[] | null>(null);
  const [accounts, setAccounts] = useState<AdAccount[]>([]);
  // Every account, whatever its brand, so the accounts list can give one a
  // brand and each connection can say what it sees.
  const [allAccounts, setAllAccounts] = useState<AdAccount[]>([]);
  const [campaigns, setCampaigns] = useState<AdCampaign[]>([]);
  const [byPlatform, setByPlatform] = useState<AdSummaryRow[]>([]);
  const [totals, setTotals] = useState<AdSummaryRow[]>([]);
  const [byDay, setByDay] = useState<AdSummaryRow[]>([]);
  // What Outflare's own attribution credits each campaign, by its id.
  const [credited, setCredited] = useState<Record<string, AttributionAdRow>>({});
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    const path = { ws };
    const query = brandKey ? { brand: brandKey } : {};
    const narrow = { ...query, ...scopeQuery(scope) };
    const to = new Date();
    const range = { from: day(new Date(to.getTime() - (days - 1) * 86_400_000)), to: day(to) };
    const [c, a, all, cp, s, d, at, ba] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/ads/connections", { params: { path } }),
      api.GET("/v1/workspaces/{ws}/ads/accounts", { params: { path, query } }),
      brandKey ? api.GET("/v1/workspaces/{ws}/ads/accounts", { params: { path, query: { brand: "all" } } }) : null,
      api.GET("/v1/workspaces/{ws}/ads/campaigns", { params: { path, query: { ...narrow, ...range, limit: 200 } } }),
      api.GET("/v1/workspaces/{ws}/ads/summary", { params: { path, query: { ...narrow, ...range, group: "connector" } } }),
      api.GET("/v1/workspaces/{ws}/ads/summary", { params: { path, query: { ...narrow, ...range, group: "day" } } }),
      // What Outflare credits is a brand's, so only with one picked.
      brandId ? api.GET("/v1/brands/{brand}/attribution/ads", { params: { path: { brand: brandId }, query: range } }) : null,
      api.GET("/v1/workspaces/{ws}/ads/summary", { params: { path, query: { ...narrow, ...range, group: "account" } } }),
    ]);
    if (c.error || !c.data) return setError(errorMessage(c.error));
    setError(a.error || cp.error || s.error ? errorMessage(a.error ?? cp.error ?? s.error) : "");
    setConns(c.data.data);
    setAccounts(a.data?.data ?? []);
    setAllAccounts((all ? all.data?.data : a.data?.data) ?? []);
    setCampaigns(cp.data?.data ?? []);
    setByPlatform(s.data?.data ?? []);
    setTotals(s.data?.totals ?? []);
    setByDay(d.data?.data ?? []);
    setByAccount(ba.data?.data ?? []);
    setCredited(Object.fromEntries((at?.data?.data ?? []).map((r) => [r.id, r])));
  }, [ws, brandKey, days, scope.network, scope.account]); // eslint-disable-line react-hooks/exhaustive-deps
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

  // Managing campaigns: each change is queued and read back from the
  // platform; the list follows the platform's answer at the next load.
  async function change(c: AdCampaign, kind: "pause" | "resume" | "budget", budget?: number) {
    setBusy(true);
    const p = { params: { path: { ws, id: c.id } } };
    const { error } =
      kind === "pause" ? await api.POST("/v1/workspaces/{ws}/ads/campaigns/{id}/pause", p)
      : kind === "resume" ? await api.POST("/v1/workspaces/{ws}/ads/campaigns/{id}/resume", p)
      : await api.PATCH("/v1/workspaces/{ws}/ads/campaigns/{id}", { ...p, body: { daily_budget_micros: budget ?? 0 } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast(kind === "budget" ? "Budget change sent to the platform. It shows here once the platform confirms it." : `${kind === "pause" ? "Pause" : "Resume"} sent to the platform. It shows here once the platform confirms it.`);
    setTimeout(load, 3000);
  }
  // The brand's own target cost per conversion, for a campaign or for an
  // account's campaigns without one. Nothing changes on the platform.
  async function setTarget(kind: "campaigns" | "accounts", id: string, name: string, currency: string, current: number | null, own: boolean) {
    const r = await ask({
      title: `Target cost per conversion for ${name}`,
      body:
        kind === "campaigns"
          ? `In ${currency}. Outflare compares the campaign's cost per conversion with it and says whether it is over or under. Nothing changes in the ad platform.`
          : `In ${currency}, for every campaign of this account without a target of its own. Nothing changes in the ad platform.`,
      input: { label: "Target cost per conversion", type: "number", value: current ? (current / 1e6).toFixed(2) : "", placeholder: "25.00" },
      choices: [
        { label: "Cancel", value: "cancel", variant: "ghost" },
        ...(own ? [{ label: "Clear", value: "clear", variant: "ghost" as const }] : []),
        { label: "Set the target", value: "ok", variant: "primary" },
      ],
    });
    if (!r || r.choice === "cancel") return;
    const v = Number(r.value);
    if (r.choice === "ok" && !(v > 0)) return toast("Enter an amount above zero.", "error");
    const body = { cost_per_conversion_micros: r.choice === "clear" ? null : Math.round(v * 1e6) };
    const path = { ws, id };
    const { error } =
      kind === "campaigns"
        ? await api.PUT("/v1/workspaces/{ws}/ads/campaigns/{id}/target", { params: { path }, body })
        : await api.PUT("/v1/workspaces/{ws}/ads/accounts/{id}/target", { params: { path }, body });
    if (error) return toast(errorMessage(error), "error");
    toast(r.choice === "clear" ? "Target cleared." : "Target set.");
    load();
  }

  async function setBudget(c: AdCampaign) {
    const current = c.daily_budget_micros ? (c.daily_budget_micros / 1e6).toFixed(2) : "";
    const r = await ask({
      title: `Daily budget for ${c.name}`,
      body: `In ${c.currency}, as the platform shows it. The change is read back from the platform before it shows here.`,
      input: { label: "Daily budget", type: "number", value: current, placeholder: "25.00" },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Set the budget", value: "ok", variant: "primary" }],
    });
    const v = Number(r?.value);
    if (r?.choice !== "ok" || !(v > 0)) return;
    await change(c, "budget", Math.round(v * 1e6));
  }
  async function pauseAll() {
    const b = brand.brand;
    if (!b) return;
    if (!(await confirmAction(`Pause every campaign of ${b.name}?`, "Every active campaign on the brand's ad accounts is paused at once, on each platform Outflare can change. You can resume them one by one.", "Pause all ads"))) return;
    setBusy(true);
    const { data, error } = await api.POST("/v1/brands/{brand}/ads/pause-all", { params: { path: { brand: b.id } } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    const n = data && "changes" in data ? data.changes.length : 0;
    toast(n ? `Pausing ${n} campaign${n === 1 ? "" : "s"}.` : "No active campaign to pause on a platform Outflare can change.");
    setTimeout(load, 3000);
  }
  async function update(a: AdAccount, body: { brand?: string; sync_on?: boolean }) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/ads/accounts/{id}", { params: { path: { ws, id: a.id } }, body });
    if (error) return toast(errorMessage(error), "error");
    load();
  }

  const range = { from: day(new Date(Date.now() - (days - 1) * 86_400_000)), to: day(new Date()) };

  const header = (
    <PageHeader
      title="Paid ads"
      description="Spend, clicks and the conversions each platform reports, from your own ad accounts, by brand. Outflare changes a campaign only when you, or an agent you allow, ask it to."
      action={
        <div className="flex items-center gap-2">
          <div className="flex rounded-md border" role="group" aria-label="Date range">
            {ranges.map((r) => (
              <button key={r} type="button" className={`px-2.5 py-1 text-[13px] ${r === days ? "bg-muted font-medium" : "text-muted-foreground"}`} onClick={() => setDays(r)}>{r} days</button>
            ))}
          </div>
          {canManage && conns && conns.length > 0 && <Button variant="ghost" disabled={busy} onClick={syncNow}>Sync now</Button>}
          {canManage && brand.brand && campaigns.some((c) => c.status === "active") && <Button variant="danger" disabled={busy} onClick={pauseAll}>Pause all ads</Button>}
        </div>
      }
    />
  );
  if (error && !conns) return <div className="space-y-6">{header}<ErrorText>{error}</ErrorText></div>;
  if (!conns) return <div className="space-y-6">{header}<Loading /></div>;

  // One campaign, or one of its ad groups, as a page of its own.
  if (openId) {
    const c = campaigns.find((x) => x.id === openId);
    return (
      <div className="space-y-6">
        {header}
        {c ? (
          <CampaignDetail key={c.id} ws={ws} campaign={c} groupId={openGroup || undefined} from={range.from} to={range.to}
            open={(g) => go("ads", g ? `${c.id}/${g}` : c.id)} back={() => go("ads", "campaigns")} />
        ) : (
          <Card title="Campaign not found" description={`It didn't spend or run in the last ${days} days, or belongs to another brand.`}>
            <Button variant="ghost" onClick={() => go("ads", "campaigns")}>All campaigns</Button>
          </Card>
        )}
      </div>
    );
  }

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
      <Tabs value={tab} onValueChange={(v) => go("ads", v === "overview" ? undefined : v)}>
        <TabsList className="h-8">
          <TabsTrigger value="overview" className="px-3 text-[12px]">Overview</TabsTrigger>
          <TabsTrigger value="suggestions" className="px-3 text-[12px]">Suggestions</TabsTrigger>
          <TabsTrigger value="tests" className="px-3 text-[12px]">Tests</TabsTrigger>
          <TabsTrigger value="campaigns" className="px-3 text-[12px]">Campaigns</TabsTrigger>
          <TabsTrigger value="changes" className="px-3 text-[12px]">Changes</TabsTrigger>
          <TabsTrigger value="tracking" className="px-3 text-[12px]">Tracking</TabsTrigger>
          <TabsTrigger value="conversions" className="px-3 text-[12px]">Send conversions</TabsTrigger>
          <TabsTrigger value="accounts" className="px-3 text-[12px]">Accounts</TabsTrigger>
        </TabsList>
      </Tabs>
      <Levels scope={scope} setScope={setScope} accounts={allAccounts} />
      {error && <ErrorText>{error}</ErrorText>}

      {conns.filter((c) => c.setup_needed.length > 0).map((c) => <Setup key={c.id} ws={ws} conn={c} canManage={canManage} done={load} />)}

      {tab === "overview" && (
        <>
      {totals.length === 0 ? null : totals.map((t) => (
        <Stats key={t.currency} cols={5}>
          <Stat label={totals.length > 1 ? `Spend, ${t.currency}` : `Spend, ${days} days`} value={money(t.totals.spend_micros, t.currency)} />
          <Stat label="Clicks" value={n(t.totals.clicks)} sub={`${n(t.totals.impressions)} impressions`} />
          <Stat label="CTR" value={pct(t.totals.ctr)} />
          <Stat label="Cost per click" value={money(t.totals.cpc_micros, t.currency)} />
          <Stat label="Platform conversions" value={n(t.totals.conversions)} sub="as each platform counts them" />
        </Stats>
      ))}

      {brand.brand && <AdsPanels brand={brand.brand.id} days={days} go={go} />}

      <Card title="By day" description="Every platform's spend, clicks, impressions and conversions per day, as each reports them.">
        <AdTrend rows={byDay} from={range.from} to={range.to} />
      </Card>

      <Card title="By platform" description={`The last ${days} days. Conversions and ROAS are what each platform reports, with its own attribution window, so they are not added up across platforms.`}>
        <DataTable
          data={byPlatform}
          rowId={(r) => `${r.key}-${r.currency}`}
          onRowClick={scope.network ? undefined : (r) => setScope({ network: r.key })}
          columnsMenu={false}
          hidden={deliveryColumns}
          empty="Nothing spent in this range yet."
          initialSort={[{ id: "spend", desc: true }]}
          columns={[
            { id: "platform", header: "Platform", accessorFn: (r) => platformName(r.key), cell: ({ row: { original: r } }) => <span className="font-medium">{platformName(r.key)}</span> },
            ...metricColumns<AdSummaryRow>((r) => r.totals, (r) => r.currency),
          ]}
        />
      </Card>

      {!scope.account && byAccount.length > 0 && (
        <Card title="By account" description={`The last ${days} days, each ad account on its own. Open one to see only its campaigns, changes and suggestions.`}>
          <DataTable
            data={byAccount}
            rowId={(r) => `${r.key}-${r.currency}`}
            columnsMenu={false}
            hidden={deliveryColumns}
            empty="Nothing spent in this range yet."
            initialSort={[{ id: "spend", desc: true }]}
            onRowClick={(r) => {
              const acc = allAccounts.find((x) => x.id === r.key);
              setScope({ network: acc?.connector ?? scope.network, account: r.key });
            }}
            columns={[
              { id: "account", header: "Account", accessorFn: (r) => allAccounts.find((x) => x.id === r.key)?.name ?? r.key, cell: ({ row: { original: r } }) => {
                const acc = allAccounts.find((x) => x.id === r.key);
                return acc ? <AccountName a={acc} /> : <span className="font-mono">{r.key}</span>;
              } },
              ...metricColumns<AdSummaryRow>((r) => r.totals, (r) => r.currency),
            ]}
          />
        </Card>
      )}

      <ConversionActions ws={ws} from={range.from} to={range.to} brand={brandKey || undefined} card />

        </>
      )}

      {tab === "campaigns" && (
        <>
      <Card title="Campaigns" description={`Ranked by spend over the last ${days} days. Open a campaign for its own page: its numbers by day, its ad groups or ad sets and its ads (Google and Meta), each of which opens in turn. Pause, resume or set the daily budget of a Google Ads, Meta, Microsoft, LinkedIn or TikTok campaign; each change is read back from the platform.`}>
        <DataTable
          data={campaigns}
          rowId={(c) => c.id}
          onRowClick={(c) => go("ads", c.id)}
          searchPlaceholder="Search campaigns"
          hidden={deliveryColumns}
          empty="Campaigns appear after the first sync."
          initialSort={[{ id: "spend", desc: true }]}
          columns={[
            {
              id: "campaign",
              header: "Campaign",
              accessorFn: (c) => `${c.name} ${c.account_name}`,
              cell: ({ row: { original: c } }) => (
                <div className="min-w-56 space-y-0.5">
                  <div className="font-medium text-blue-700 underline-offset-2 hover:underline dark:text-blue-400">{c.name || c.external_id}</div>
                  <div className="text-[12px] text-muted-foreground">
                    {platformName(c.connector)}{allAccounts.length > 1 && <> · {c.account_name}</>}
                    {words(c.objective) && <> · {words(c.objective)}</>}
                    {words(c.bidding_strategy) && <> · <span title="How the platform bids">{words(c.bidding_strategy)}</span></>}
                    {c.status !== "active" && <> · <Badge tone={c.status === "removed" ? "red" : "grey"} title={c.provider_status}>{c.status === "paused" ? "Paused" : c.status === "removed" ? "Removed" : c.provider_status || "Other"}</Badge></>}
                  </div>
                </div>
              ),
            },
            {
              id: "target",
              header: "Target / conv.",
              accessorFn: (c) => c.target_cost_per_conversion_micros ?? -1,
              cell: ({ row: { original: c } }) => (
                <div className="space-y-0.5">
                  <div className="flex items-center gap-1.5">
                    <span className="font-mono tabular-nums">{money(c.target_cost_per_conversion_micros, c.currency)}</span>
                    {targetWord[c.target_status] && <Badge tone={targetTone[c.target_status]} title="Cost per conversion over this range against the target, within a tenth either way is on target">{targetWord[c.target_status]}</Badge>}
                  </div>
                  {c.target_from === "account" && <div className="text-[11px] text-muted-foreground">The account's target</div>}
                  {c.platform_target_cpa_micros != null && <div className="text-[11px] text-muted-foreground">{platformName(c.connector)} bids to {money(c.platform_target_cpa_micros, c.currency)}</div>}
                  {c.platform_target_roas != null && <div className="text-[11px] text-muted-foreground">{platformName(c.connector)} bids to {Math.round(c.platform_target_roas * 100)}% return</div>}
                </div>
              ),
            },
            {
              id: "trend",
              header: "Trend",
              accessorFn: (c) => c.spend_change ?? (c.trend === "new" ? 99 : c.trend === "stopped" ? -99 : 0),
              cell: ({ row: { original: c } }) =>
                trendWord[c.trend] && (
                  <span title={`Spend against the ${days} days before: ${money(c.previous_totals.spend_micros, c.currency)}, ${n(c.previous_totals.conversions)} conversions`}>
                    <Badge tone={trendTone[c.trend]}>
                      {c.trend === "scaling_up" ? "↑ " : c.trend === "scaling_down" ? "↓ " : ""}
                      {trendWord[c.trend]}
                      {c.spend_change != null && ` ${c.spend_change > 0 ? "+" : ""}${Math.round(c.spend_change * 100)}%`}
                    </Badge>
                  </span>
                ),
            },
            { id: "budget", header: "Daily budget", accessorFn: (c) => c.daily_budget_micros ?? -1, cell: ({ row: { original: c } }) => <span className="font-mono tabular-nums">{money(c.daily_budget_micros, c.currency)}</span> },
            ...metricColumns<AdCampaign>((c) => c.totals, (c) => c.currency),
            ...outflareColumns<AdCampaign>(credited),
            { id: "brand", header: "Brand", accessorFn: (c) => brand.byId(c.brand_id)?.name ?? "", cell: ({ row: { original: c } }) => <BrandBadge id={c.brand_id} /> },
            {
              id: "manage",
              header: "",
              enableSorting: false,
              cell: ({ row: { original: c } }) =>
                canManage && (
                  <div className="flex gap-1" onClick={(e) => e.stopPropagation()}>
                    <Button variant="ghost" disabled={busy} onClick={() => setTarget("campaigns", c.id, c.name, c.currency, c.target_cost_per_conversion_micros, c.target_from === "campaign")}>Target</Button>
                    {manageable.has(c.connector) && c.status !== "removed" && (
                      <>
                        {c.status === "active" ? <Button variant="ghost" disabled={busy} onClick={() => change(c, "pause")}>Pause</Button> : <Button variant="ghost" disabled={busy} onClick={() => change(c, "resume")}>Resume</Button>}
                        <Button variant="ghost" disabled={busy} onClick={() => setBudget(c)}>Budget</Button>
                      </>
                    )}
                  </div>
                ),
            },
          ]}
        />
      </Card>

      <AdResults ws={ws} from={range.from} to={range.to} brand={brandId} go={go} />

        </>
      )}

      {tab === "suggestions" && <AdSuggestions ws={ws} brand={brandKey} scope={scope} canManage={canManage} />}
      {tab === "suggestions" && (brand.brand ? <AdDrafts key={brand.brand.id} brand={brand.brand.id} canManage={canManage} />
        : <Card title="New ads"><p className="text-[13px] text-muted-foreground">Pick a brand to see the new ads its agent wrote.</p></Card>)}
      {tab === "tests" && <AdTests ws={ws} brand={brandKey} scope={scope} campaigns={campaigns} canManage={canManage} />}
      {tab === "changes" && <AdChanges ws={ws} brand={brandKey} scope={scope} />}

      {tab === "tracking" && (
        <>
          <AdTrackingFix ws={ws} accounts={accounts} canManage={canManage} />
      <AdTrackingHealthCard ws={ws} brand={brandKey || undefined} />
      <AdTrackingTemplates ws={ws} />

        </>
      )}

      {tab === "conversions" && <AdConversions ws={ws} canManage={canManage} accounts={accounts} />}

      {tab === "accounts" && (
        <>
      <Card title="Ad accounts" description="Every account your connections can see, in every brand. Give each a brand so its campaigns, spend and clicks show under it, in the costs ledger too; switch one off to stop syncing it.">
        <DataTable
          data={allAccounts}
          rowId={(a) => a.id}
          searchPlaceholder="Search accounts"
          empty={conns.some((c) => c.accounts > 0) ? "Your connections see accounts, but none are saved yet. Press Sync now." : "Accounts appear after the first sync."}
          columns={[
            {
              id: "account",
              header: "Account",
              accessorFn: (a) => `${a.name} ${a.external_id} ${accountId(a.connector, a.external_id)}`,
              cell: ({ row: { original: a } }) => <AccountName a={a} />,
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
            {
              id: "target",
              header: "Target / conv.",
              accessorFn: (a) => a.target_cost_per_conversion_micros ?? -1,
              cell: ({ row: { original: a } }) =>
                canManage ? (
                  <Button variant="ghost" onClick={() => setTarget("accounts", a.id, a.name || accountId(a.connector, a.external_id), a.currency, a.target_cost_per_conversion_micros, a.target_cost_per_conversion_micros != null)}>
                    {a.target_cost_per_conversion_micros != null ? money(a.target_cost_per_conversion_micros, a.currency) : "Set target"}
                  </Button>
                ) : (
                  <span className="font-mono tabular-nums">{money(a.target_cost_per_conversion_micros, a.currency)}</span>
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

      <Card title="Connections" description="Your ad platform sign-ins. Add or remove one under Connectors.">
        <div className="space-y-2 text-[13px]">
          {conns.map((c) => {
            const seen = allAccounts.filter((a) => a.integration_id === c.id);
            return (
              <div key={c.id} className="space-y-1.5 rounded-md border p-3">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div>
                    <span className="font-medium">{platformName(c.connector)}</span> <span className="text-muted-foreground">{c.name !== platformName(c.connector) && `· ${c.name} `}· {c.accounts === 1 ? "1 account" : `${c.accounts} accounts`}</span>
                  </div>
                  <div className={c.last_error ? "text-red-600 dark:text-red-400" : "text-muted-foreground"} title={c.last_error ?? undefined}>
                    {c.setup_needed.length > 0 ? "Waiting for setup" : c.last_error ? "Last sync failed" : c.last_ok_at ? `Synced ${ago(c.last_ok_at)}` : "Not synced yet"}
                  </div>
                </div>
                {c.last_error && <p className="text-[12px] text-red-600 dark:text-red-400">{c.last_error}</p>}
                {seen.map((a) => (
                  <div key={a.id} className="flex flex-wrap items-center justify-between gap-2 border-t pt-1.5">
                    <AccountName a={a} />
                    <div className="flex items-center gap-2">
                      <BrandBadge id={a.brand_id} />
                      <span className="text-[12px] text-muted-foreground">{a.sync_on ? "Syncing" : "Not syncing"}</span>
                    </div>
                  </div>
                ))}
              </div>
            );
          })}
          {canManage && <Button variant="ghost" onClick={() => go("connectors", "ads")}>Connect another</Button>}
        </div>
      </Card>
        </>
      )}
    </div>
  );
}

// The spend, clicks and platform-reported results columns, shared by the
// platform and campaign tables.
export function metricColumns<T>(totals: (r: T) => AdTotals, currency: (r: T) => string) {
  const mono = (s: string) => <span className="font-mono tabular-nums">{s}</span>;
  return [
    { id: "spend", header: "Spend", accessorFn: (r: T) => totals(r).spend_micros, cell: ({ row: { original: r } }: { row: { original: T } }) => mono(money(totals(r).spend_micros, currency(r))) },
    { id: "clicks", header: "Clicks", accessorFn: (r: T) => totals(r).clicks, cell: ({ row: { original: r } }: { row: { original: T } }) => mono(n(totals(r).clicks)) },
    { id: "ctr", header: "CTR", accessorFn: (r: T) => totals(r).ctr ?? -1, cell: ({ row: { original: r } }: { row: { original: T } }) => mono(pct(totals(r).ctr)) },
    { id: "cpc", header: "CPC", accessorFn: (r: T) => totals(r).cpc_micros ?? -1, cell: ({ row: { original: r } }: { row: { original: T } }) => mono(money(totals(r).cpc_micros, currency(r))) },
    { id: "conversions", header: "Conv.", accessorFn: (r: T) => totals(r).conversions ?? -1, cell: ({ row: { original: r } }: { row: { original: T } }) => mono(n(totals(r).conversions)) },
    { id: "cpa", header: "Cost / conv.", accessorFn: (r: T) => totals(r).cost_per_conversion_micros ?? -1, cell: ({ row: { original: r } }: { row: { original: T } }) => mono(money(totals(r).cost_per_conversion_micros, currency(r))) },
    { id: "roas", header: "Platform ROAS", accessorFn: (r: T) => totals(r).platform_roas ?? -1, cell: ({ row: { original: r } }: { row: { original: T } }) => mono(roas(totals(r).platform_roas)) },
    { id: "impressions", header: "Impressions", accessorFn: (r: T) => totals(r).impressions, cell: ({ row: { original: r } }: { row: { original: T } }) => mono(n(totals(r).impressions)) },
    { id: "link_clicks", header: "Link clicks", accessorFn: (r: T) => totals(r).link_clicks ?? -1, cell: ({ row: { original: r } }: { row: { original: T } }) => mono(n(totals(r).link_clicks)) },
    { id: "landing_page_views", header: "Page views", accessorFn: (r: T) => totals(r).landing_page_views ?? -1, cell: ({ row: { original: r } }: { row: { original: T } }) => mono(n(totals(r).landing_page_views)) },
    { id: "video_views", header: "Video views", accessorFn: (r: T) => totals(r).video_views ?? -1, cell: ({ row: { original: r } }: { row: { original: T } }) => mono(n(totals(r).video_views)) },
    { id: "frequency", header: "Frequency", accessorFn: (r: T) => totals(r).frequency ?? -1, cell: ({ row: { original: r } }: { row: { original: T } }) => mono(totals(r).frequency == null ? "—" : totals(r).frequency!.toFixed(2)) },
  ];
}

// deliveryColumns are metricColumns' Meta delivery numbers, hidden until
// the viewer turns them on where most rows won't have them.
export const deliveryColumns = ["impressions", "link_clicks", "landing_page_views", "video_views", "frequency"];

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

// The level the page shows, as a breadcrumb: every network › one network ›
// one account. Each step back widens it again.
function Levels({ scope, setScope, accounts }: { scope: AdScope; setScope: (s: AdScope) => void; accounts: AdAccount[] }) {
  const networks = [...new Set(accounts.map((a) => a.connector))];
  const inNetwork = accounts.filter((a) => a.connector === scope.network);
  const account = accounts.find((a) => a.id === scope.account);
  const crumb = "rounded px-1.5 py-0.5 hover:bg-muted";
  return (
    <nav aria-label="Level" className="flex flex-wrap items-center gap-1 text-[13px]">
      <button type="button" className={`${crumb} ${scope.network ? "text-muted-foreground" : "font-medium"}`} onClick={() => setScope({})}>All networks</button>
      <span className="text-muted-foreground">›</span>
      <select aria-label="Network" className="rounded border bg-transparent px-1.5 py-0.5" value={scope.network ?? ""} onChange={(e) => setScope(e.target.value ? { network: e.target.value } : {})}>
        <option value="">Every network</option>
        {networks.map((c) => <option key={c} value={c}>{platformName(c)}</option>)}
      </select>
      {scope.network && (
        <>
          <span className="text-muted-foreground">›</span>
          <select aria-label="Account" className="rounded border bg-transparent px-1.5 py-0.5" value={scope.account ?? ""} onChange={(e) => setScope({ network: scope.network, account: e.target.value || undefined })}>
            <option value="">Every account</option>
            {inNetwork.map((a) => <option key={a.id} value={a.id}>{a.name || accountId(a.connector, a.external_id)}</option>)}
          </select>
        </>
      )}
      {account && <span className="text-muted-foreground">· {account.currency}</span>}
    </nav>
  );
}

const changeWord = {
  pause: "Paused", resume: "Resumed", budget: "Daily budget", keyword_add: "Keyword added", negative_add: "Negative keyword added", keyword_split: "Keyword given its own ad group",
  bidding: "Bidding", audience_add: "Audience added", audience_remove: "Audience taken out", country_add: "Country added", country_remove: "Country taken out",
} as const;

// changeDetail is what a change names: a keyword, an audience, a country or a bidding target.
function changeDetail(r: AdChange) {
  const a = r.after;
  if (a.keyword) return <span className="font-mono">[{a.keyword}]</span>;
  if (a.country) return <span>{a.country}</span>;
  if (a.audience_id) return <span>{a.audience_name || a.audience_id}</span>;
  if (a.bidding) {
    const b = a.bidding;
    const target = b.target_cpa_micros != null ? ` to ${(b.target_cpa_micros / 1e6).toFixed(2)} a conversion` : b.target_roas != null ? ` to ${b.target_roas}× return` : "";
    return <span>{b.strategy === "maximize_conversion_value" ? "Maximise value" : "Maximise conversions"}{target}</span>;
  }
  return null;
}

// Every change Outflare made, or is making, at the level the page shows.
function AdChanges({ ws, brand, scope }: { ws: string; brand: string; scope: AdScope }) {
  const [rows, setRows] = useState<AdChange[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    let live = true;
    api.GET("/v1/workspaces/{ws}/ads/changes", { params: { path: { ws }, query: { ...(brand ? { brand } : {}), ...scopeQuery(scope), limit: 200 } } }).then(({ data, error }) => {
      if (!live) return;
      if (error) setError(errorMessage(error));
      setRows(data?.data ?? []);
    });
    return () => {
      live = false;
    };
  }, [ws, brand, scope.network, scope.account]); // eslint-disable-line react-hooks/exhaustive-deps
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!rows) return <Loading />;
  return (
    <Card title="Changes" description="Every pause, resume, budget, keyword, bidding, audience and country change asked for here, newest first, and whether the platform shows it.">
      <DataTable
        data={rows}
        rowId={(r) => r.id}
        columnsMenu={false}
        empty="No changes yet."
        columns={[
          { id: "when", header: "When", accessorFn: (r) => r.created_at, cell: ({ row: { original: r } }) => <span className="text-muted-foreground">{ago(r.created_at)}</span> },
          { id: "campaign", header: "Campaign", accessorFn: (r) => r.campaign_name, cell: ({ row: { original: r } }) => (
            <div><div className="font-medium">{r.campaign_name}</div><div className="text-[12px] text-muted-foreground">{platformName(r.connector)} · {r.account_name}</div></div>
          ) },
          { id: "change", header: "Change", accessorFn: (r) => r.kind, cell: ({ row: { original: r } }) => (
            <span>{changeWord[r.kind]} {changeDetail(r)}{r.kind === "budget" && <> {r.before.daily_budget_micros != null ? `${(r.before.daily_budget_micros / 1e6).toFixed(2)} → ` : ""}{r.after.daily_budget_micros != null ? (r.after.daily_budget_micros / 1e6).toFixed(2) : ""}</>}</span>
          ) },
          { id: "status", header: "Status", accessorFn: (r) => r.status, cell: ({ row: { original: r } }) => (
            <Badge tone={r.status === "failed" ? "red" : r.status === "applied" ? (r.verified_at ? "green" : "blue") : "grey"} title={r.error || r.verify_note || undefined}>
              {r.status === "failed" ? "Failed" : r.status === "queued" ? "Queued" : r.verified_at ? "Applied and checked" : "Applied"}
            </Badge>
          ) },
        ]}
      />
    </Card>
  );
}
