import { useCallback, useEffect, useState } from "react";
import type { AdAccount, AdCampaign, AdChange, AdConnection, AdSummaryRow, AdTotals } from "@outflare/sdk";
import { useBrand, pickedBrandId } from "@/lib/brand";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { BrandBadge } from "@/components/brand-badge";
import { DataTable, type ColumnMeta, type TableDownload } from "@/components/data-table";
import { csvMoney, csvName, downloadCsv, toCsv } from "@/lib/csv";
import { api, errorMessage } from "../api";
import { track } from "@/lib/analytics";
import { ask, Badge, Button, Card, confirmAction, ErrorText, Input, Loading, Menu, PageHeader, Stat, Stats, toast } from "../ui";
import { ago } from "../format";
import type { Tab } from "../App";
import { AdConversions } from "./AdConversions";
import { AdTrackingFix, AdTrackingHealthCard, AdTrackingTemplates } from "./AdTracking";
import { AdResults } from "./AdResults";
import { AdTests } from "./AdTests";
import { AdTrend, CampaignDetail, ConversionActions } from "./AdDrilldown";
import { AdsPanels } from "@/components/insight-panels";
import { PickBrand } from "@/components/pick-brand";
import { customerBuckets, useBrandAds, windows, type Customers as CustomerBucket } from "./ads/api";
import { Hero } from "./ads/Hero";
import { Inbox } from "./ads/Inbox";
import { Results } from "./ads/Results";
import { Customers } from "./ads/Customers";
import { Verdicts } from "./ads/Verdicts";
import { SetUp } from "./ads/SetUp";
import { BiddingStrategyReport, GoogleAccountTools, GoogleConversionActions } from "./ads/Google";

// Paid ads: a brand's ad accounts managed toward cost per new paying
// customer, measured on Outflare's own attribution (plans/paid-ads/plan.md).
// The screens under web/src/pages/ads/ are the number, the decision inbox,
// results, customers, verdicts and set-up; this file is the shell, the
// header's switches, the campaign actions and the older tabs (changes,
// tracking, conversions, accounts, tests). Spend and the networks' own
// conversion counts are read from the workspace's own ad accounts; the
// networks' claims are never added across platforms.

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

// The date ranges: the last 7, 28, 30 or 90 days, or any from and to.
const ranges = [7, 28, 30, 90] as const;
const day = (d: Date) => d.toISOString().slice(0, 10);
const spanDays = (from: string, to: string) => Math.round((Date.parse(`${to}T00:00:00Z`) - Date.parse(`${from}T00:00:00Z`)) / 86_400_000) + 1;

// reportDownload is a Paid ads report's "Download CSV": its file is named
// after the report and its dates, and each download is recorded.
export const reportDownload = (report: string, ...parts: string[]): TableDownload => ({
  file: csvName("outflare-ads", report.replace(/_/g, "-"), ...parts),
  onDownload: (rows) => track("ui.ads_report_downloaded", { report, rows }),
});

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
const manageable = new Set(["google_ads", "meta_ads", "microsoft_ads", "linkedin_ads", "tiktok_ads", "reddit_ads", "x_ads"]);

// The page's screens, as the item in its URL (the overview has none): the
// six of plans/paid-ads first, then the older tabs in a second row.
const adsTabs = ["overview", "inbox", "results", "customers", "verdicts", "setup", "changes", "tracking", "conversions", "accounts", "tests"] as const;
type AdsTab = (typeof adsTabs)[number];
const primaryTabs: { id: AdsTab; label: string }[] = [
  { id: "overview", label: "The number" },
  { id: "inbox", label: "Needs you" },
  { id: "results", label: "Results" },
  { id: "customers", label: "Customers" },
  { id: "verdicts", label: "Verdicts" },
  { id: "setup", label: "Set up" },
];
const secondaryTabs: { id: AdsTab; label: string }[] = [
  { id: "changes", label: "Changes" },
  { id: "tracking", label: "Tracking" },
  { id: "conversions", label: "Send conversions" },
  { id: "accounts", label: "Accounts" },
  { id: "tests", label: "Tests" },
];
// The screens that need one brand picked: its number, decisions, results,
// customers, verdicts and set-up are one brand's.
const brandNeeded: readonly AdsTab[] = ["inbox", "results", "customers", "verdicts", "setup"];
// Older links: the suggestions tab is now the inbox, the campaigns tab the
// results.
const legacyTabs: Record<string, AdsTab> = { suggestions: "inbox", campaigns: "results" };

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

// Ads belong to one brand.
export function Ads(props: Props) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="Ads" brands={active.length} />;
  return <AdsPage key={brand.id} {...props} />;
}

function AdsPage({ ws, canManage, go, item }: Props) {
  // The item is a tab, or a campaign's id (and its ad group's) for its
  // own page.
  const tab: AdsTab = (adsTabs as readonly string[]).includes(item ?? "") ? (item as AdsTab) : legacyTabs[item ?? ""] ?? "overview";
  const [openId, openGroup] = tab === "overview" && item && !legacyTabs[item] ? item.split("/") : [];
  const brand = useBrand(ws);
  const brandKey = brand.query.brand ?? "";
  const brandId = brand.brand?.id ?? "";
  // The range: the last days, or a custom from and to (end is its last
  // day, which the brand screens count back from).
  const [lastDays, setLastDays] = useState<number>(28);
  const [custom, setCustom] = useState<{ from: string; to: string } | null>(null);
  const [picking, setPicking] = useState(false);
  const days = custom ? spanDays(custom.from, custom.to) : lastDays;
  const end = custom?.to;
  const rangeWord = custom ? `${custom.from} to ${custom.to}` : `the last ${lastDays} days`;
  // The acquisition window (days after an ad click a sign-up or first
  // payment still counts for it) and which customers' revenue the numbers
  // keep, for the number, the results and the customers.
  const [windowDays, setWindowDays] = useState<(typeof windows)[number]>(30);
  const [customers, setCustomers] = useState<CustomerBucket>("all");
  // Bumped after a sync so the brand screens read again.
  const [version, setVersion] = useState(0);
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
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const brandAds = useBrandAds(brandId);
  const windowFromBrand = brandAds.economics?.acquisition_window_days;
  useEffect(() => {
    // The brand's own window is the default until the person switches it.
    if (windowFromBrand && (windows as readonly number[]).includes(windowFromBrand)) setWindowDays(windowFromBrand as (typeof windows)[number]);
  }, [windowFromBrand]);
  const openScreen = (v: string) => {
    if (!v) return;
    track("ui.ads_screen_opened", { screen: v });
    go("ads", v === "overview" ? undefined : v);
  };

  const load = useCallback(async () => {
    const path = { brand: brandId };
    const narrow = scopeQuery(scope);
    const to = new Date();
    const range = custom ?? { from: day(new Date(to.getTime() - (days - 1) * 86_400_000)), to: day(to) };
    // Every brand's accounts and those with none yet, for assigning.
    const all = await api.GET("/v1/workspaces/{ws}/ads/accounts", { params: { path: { ws }, query: { brand: "all" } } });
    const [c, a, cp, s, d, ba] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/ads/connections", { params: { path: { ws } } }),
      api.GET("/v1/brands/{brand}/ads/accounts", { params: { path } }),
      api.GET("/v1/brands/{brand}/ads/campaigns", { params: { path, query: { ...narrow, ...range, limit: 200 } } }),
      api.GET("/v1/brands/{brand}/ads/summary", { params: { path, query: { ...narrow, ...range, group: "connector" } } }),
      api.GET("/v1/brands/{brand}/ads/summary", { params: { path, query: { ...narrow, ...range, group: "day" } } }),
      api.GET("/v1/brands/{brand}/ads/summary", { params: { path, query: { ...narrow, ...range, group: "account" } } }),
    ]);
    if (c.error || !c.data) return setError(errorMessage(c.error));
    setError(a.error || cp.error || s.error ? errorMessage(a.error ?? cp.error ?? s.error) : "");
    setConns(c.data.data);
    setAccounts(a.data?.data ?? []);
    setAllAccounts(all.data?.data ?? []);
    setCampaigns(cp.data?.data ?? []);
    setByPlatform(s.data?.data ?? []);
    setTotals(s.data?.totals ?? []);
    setByDay(d.data?.data ?? []);
    setByAccount(ba.data?.data ?? []);
  }, [ws, brandKey, days, custom?.from, custom?.to, scope.network, scope.account]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    load();
  }, [load]);

  async function syncNow() {
    setBusy(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/ads/sync", { params: { path: { ws } } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast("Syncing your ad accounts now. Numbers update in a minute or two.");
    setTimeout(() => {
      load();
      setVersion((v) => v + 1);
    }, 8000);
  }

  // Managing campaigns: each change is queued and read back from the
  // platform; the list follows the platform's answer at the next load.
  async function change(c: AdCampaign, kind: "pause" | "resume" | "budget", budget?: number) {
    setBusy(true);
    const p = { params: { path: { brand: brandId, id: c.id } } };
    const { error } =
      kind === "pause" ? await api.POST("/v1/brands/{brand}/ads/campaigns/{id}/pause", p)
      : kind === "resume" ? await api.POST("/v1/brands/{brand}/ads/campaigns/{id}/resume", p)
      : await api.PATCH("/v1/brands/{brand}/ads/campaigns/{id}", { ...p, body: { daily_budget_micros: budget ?? 0 } });
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
    const { error } =
      kind === "campaigns"
        ? await api.PUT("/v1/brands/{brand}/ads/campaigns/{id}/target", { params: { path: { brand: brandId, id } }, body })
        : await api.PUT("/v1/workspaces/{ws}/ads/accounts/{id}/target", { params: { path: { ws, id } }, body });
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

  const range = custom ?? { from: day(new Date(Date.now() - (days - 1) * 86_400_000)), to: day(new Date()) };

  // Removing can't be undone in Google Ads, so it asks first, naming what
  // goes. Only a person's own session can remove (the server refuses an
  // agent or token).
  async function removeCampaign(c: AdCampaign) {
    if (!(await confirmAction(`Remove ${c.name}?`, <>
      <p>Google Ads can't undo a removal: the campaign stops for good and can't be turned on again. Its ad groups, ads and keywords go with it.</p>
      <p>Its numbers stay here and in Google Ads. To stop it for now, pause it instead.</p>
    </>, "Remove campaign"))) return;
    setBusy(true);
    const { error } = await api.POST("/v1/brands/{brand}/ads/campaigns/{id}/remove", { params: { path: { brand: brandId, id: c.id } }, body: { confirm: true } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast("Removal sent to Google Ads. The campaign shows as removed once Google confirms it.");
    setTimeout(load, 3000);
  }
  function pickRange(r: number | "custom") {
    if (r === "custom") return setPicking((p) => !p);
    setCustom(null);
    setPicking(false);
    setLastDays(r);
    track("ui.ads_range_changed", { days: r });
  }
  function applyCustom(from: string, to: string) {
    if (!from || !to || from > to) return toast("Pick a from date on or before the to date.", "error");
    if (spanDays(from, to) > 3 * 366) return toast("A range is at most three years.", "error");
    setCustom({ from, to });
    setPicking(false);
    track("ui.ads_range_changed", { days: spanDays(from, to), custom: true });
  }

  const header = (
    <PageHeader
      title="Paid ads"
      description="What a new paying customer costs you, measured on your own sign-ups and payments, and what to do about it. Every change waits for you to approve it; where Outflare Cloud allows it, savings can apply on their own."
      action={
        <div className="flex flex-wrap items-center gap-2">
          <div className="flex rounded-md border" role="group" aria-label="Date range">
            {ranges.map((r) => (
              <button key={r} type="button" aria-pressed={!custom && r === lastDays} className={`px-2.5 py-1 text-[13px] ${!custom && r === lastDays ? "bg-muted font-medium" : "text-muted-foreground"}`} onClick={() => pickRange(r)}>{r} days</button>
            ))}
            <button type="button" aria-pressed={!!custom} aria-expanded={picking} className={`px-2.5 py-1 text-[13px] ${custom ? "bg-muted font-medium" : "text-muted-foreground"}`} onClick={() => pickRange("custom")}>{custom ? `${custom.from} – ${custom.to}` : "Custom"}</button>
          </div>
          {picking && <CustomRange from={range.from} to={range.to} apply={applyCustom} />}
          {brand.brand && (
            <>
              <div className="flex rounded-md border" role="group" aria-label="Attribution window" title="Days after an ad click a sign-up or first payment still counts for it">
                {windows.map((w) => (
                  <button key={w} type="button" aria-pressed={w === windowDays} className={`px-2.5 py-1 text-[13px] ${w === windowDays ? "bg-muted font-medium" : "text-muted-foreground"}`} onClick={() => { setWindowDays(w); track("ui.ads_window_changed", { days: w }); }}>{w}-day window</button>
                ))}
              </div>
              <div className="flex rounded-md border" role="group" aria-label="Customers" title="New: a person's first ever payment. Returning: any later payment.">
                {customerBuckets.map((c) => (
                  <button key={c.id} type="button" aria-pressed={c.id === customers} className={`px-2.5 py-1 text-[13px] ${c.id === customers ? "bg-muted font-medium" : "text-muted-foreground"}`} onClick={() => { setCustomers(c.id); track("ui.ads_customers_filter_changed", { customers: c.id }); }}>{c.label}</button>
                ))}
              </div>
            </>
          )}
          {canManage && conns && conns.length > 0 && <Button variant="ghost" disabled={busy} onClick={syncNow}>Sync now</Button>}
          {canManage && brand.brand && campaigns.some((c) => c.status === "active") && <Button variant="danger" disabled={busy} onClick={pauseAll}>Pause all ads</Button>}
        </div>
      }
    />
  );
  // X Ads signs in through the workspace's own X app (OAuth 1.0a).
  async function signInX(c: AdConnection) {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/integrations/{id}/sign-in", { params: { path: { ws, id: c.id }, query: { format: "json", return: "ads" } } });
    if (error || !data) return toast(errorMessage(error), "error");
    location.assign((data as { url: string }).url);
  }

  if (error && !conns) return <div className="space-y-6">{header}<ErrorText>{error}</ErrorText></div>;
  if (!conns) return <div className="space-y-6">{header}<Loading /></div>;

  // One campaign, or one of its ad groups, as a page of its own.
  if (openId) {
    const c = campaigns.find((x) => x.id === openId);
    return (
      <div className="space-y-6">
        {header}
        {c ? (
          <CampaignDetail key={c.id} ws={ws} campaign={c} groupId={openGroup || undefined} from={range.from} to={range.to} canManage={canManage}
            open={(g) => go("ads", g ? `${c.id}/${g}` : c.id)} back={() => go("ads", "results")} />
        ) : (
          <Card title="Campaign not found" description={`It didn't spend or run from ${range.from} to ${range.to}, or belongs to another brand.`}>
            <Button variant="ghost" onClick={() => go("ads", "results")}>All results</Button>
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
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <Tabs value={primaryTabs.some((t) => t.id === tab) ? tab : ""} onValueChange={openScreen}>
          <TabsList className="h-auto! flex-wrap justify-start" aria-label="Paid ads screens">
            {primaryTabs.map((t) => <TabsTrigger key={t.id} value={t.id} className="px-3 text-[12px]">{t.label}</TabsTrigger>)}
          </TabsList>
        </Tabs>
        <nav aria-label="More on paid ads" className="flex flex-wrap items-center gap-1 text-[12px]">
          <span className="text-muted-foreground">More:</span>
          {secondaryTabs.map((t) => (
            <button key={t.id} type="button" aria-current={tab === t.id ? "page" : undefined} className={`rounded px-1.5 py-0.5 hover:bg-muted ${tab === t.id ? "bg-muted font-medium" : "text-muted-foreground"}`} onClick={() => openScreen(t.id)}>{t.label}</button>
          ))}
        </nav>
      </div>
      <Levels scope={scope} setScope={setScope} accounts={allAccounts} />
      {error && <ErrorText>{error}</ErrorText>}
      {brandAds.error && brandId && <ErrorText>{brandAds.error}</ErrorText>}

      {conns.filter((c) => c.setup_needed.length > 0).map((c) => <Setup key={c.id} ws={ws} conn={c} canManage={canManage} done={load} />)}

      {brandNeeded.includes(tab) && !brand.brand && <PickBrand what="A brand's ad numbers, decisions and customers" brands={brand.active.length} />}

      {tab === "overview" && (
        <>
      {brand.brand && <Hero brand={brand.brand.id} days={days} end={end} windowDays={windowDays} customers={customers} economics={brandAds.economics} setup={brandAds.setup} canManage={canManage} reload={brandAds.reload} go={go} version={version} />}

      {totals.length === 0 ? null : totals.map((t) => (
        <Stats key={t.currency} cols={5}>
          <Stat label={totals.length > 1 ? `Spend, ${t.currency}` : custom ? "Spend" : `Spend, ${days} days`} value={money(t.totals.spend_micros, t.currency)} sub={custom ? rangeWord : undefined} />
          <Stat label="Clicks" value={n(t.totals.clicks)} sub={`${n(t.totals.impressions)} impressions`} />
          <Stat label="CTR" value={pct(t.totals.ctr)} />
          <Stat label="Cost per click" value={money(t.totals.cpc_micros, t.currency)} />
          <Stat label="Platform conversions" value={n(t.totals.conversions)} sub={t.totals.all_conversions != null ? `${n(t.totals.all_conversions)} all conversions` : "as each platform counts them"} />
        </Stats>
      ))}

      {brand.brand && <AdsPanels brand={brand.brand.id} days={days} go={go} />}

      <Card title="By day" description="Every platform's spend, clicks, impressions and conversions per day, as each reports them."
        action={byDay.length > 0 ? <Button variant="ghost" onClick={() => downloadByDay(byDay, range.from, range.to)}>Download CSV</Button> : undefined}>
        <AdTrend rows={byDay} from={range.from} to={range.to} />
      </Card>

      <Card title="By platform" description={`From ${range.from} to ${range.to}. Conversions and ROAS are what each platform reports, with its own attribution window, so they are not added up across platforms. All conversions also count each platform's secondary conversion actions.`}>
        <DataTable
          data={byPlatform}
          rowId={(r) => `${r.key}-${r.currency}`}
          onRowClick={scope.network ? undefined : (r) => setScope({ network: r.key })}
          columnsMenu={false}
          hidden={deliveryColumns}
          download={reportDownload("by_platform", range.from, range.to)}
          empty="Nothing spent in this range yet."
          initialSort={[{ id: "spend", desc: true }]}
          columns={[
            { id: "platform", header: "Platform", accessorFn: (r) => platformName(r.key), cell: ({ row: { original: r } }) => <span className="font-medium">{platformName(r.key)}</span> },
            { id: "currency", header: "Currency", accessorFn: (r) => r.currency, cell: ({ row: { original: r } }) => <span className="text-muted-foreground">{r.currency}</span> },
            ...metricColumns<AdSummaryRow>((r) => r.totals, (r) => r.currency),
          ]}
        />
      </Card>

      {!scope.account && byAccount.length > 0 && (
        <Card title="By account" description={`From ${range.from} to ${range.to}, each ad account on its own, as each platform reports it. Open one to see only its campaigns, changes and suggestions.`}>
          <DataTable
            data={byAccount}
            rowId={(r) => `${r.key}-${r.currency}`}
            columnsMenu={false}
            hidden={deliveryColumns}
            download={reportDownload("by_account", range.from, range.to)}
            empty="Nothing spent in this range yet."
            initialSort={[{ id: "spend", desc: true }]}
            onRowClick={(r) => {
              const acc = allAccounts.find((x) => x.id === r.key);
              setScope({ network: acc?.connector ?? scope.network, account: r.key });
            }}
            columns={[
              { id: "account", header: "Account", accessorFn: (r) => allAccounts.find((x) => x.id === r.key)?.name ?? r.key, meta: { csv: (r: AdSummaryRow) => {
                const acc = allAccounts.find((x) => x.id === r.key);
                return acc ? `${acc.name} (${platformName(acc.connector)} ${accountId(acc.connector, acc.external_id)})` : r.key;
              } }, cell: ({ row: { original: r } }) => {
                const acc = allAccounts.find((x) => x.id === r.key);
                return acc ? <AccountName a={acc} /> : <span className="font-mono">{r.key}</span>;
              } },
              ...metricColumns<AdSummaryRow>((r) => r.totals, (r) => r.currency),
            ]}
          />
        </Card>
      )}

      <ConversionActions ws={ws} from={range.from} to={range.to} brand={brandKey || undefined} card download />

        </>
      )}

      {tab === "results" && brand.brand && (
        <>
          <Results brand={brand.brand.id} days={days} end={end} windowDays={windowDays} customers={customers} scope={scope} canManage={canManage} go={go} version={version}
            manage={{ campaigns, busy, manageable: (c) => manageable.has(c), pause: (c) => change(c, "pause"), resume: (c) => change(c, "resume"), budget: setBudget, target: (c) => setTarget("campaigns", c.id, c.name, c.currency, c.target_cost_per_conversion_micros, c.target_from === "campaign"), remove: removeCampaign }} />
          <CampaignReport campaigns={campaigns} from={range.from} to={range.to} canManage={canManage} busy={busy} open={(c) => go("ads", c.id)}
            manage={{ pause: (c) => change(c, "pause"), resume: (c) => change(c, "resume"), budget: setBudget, remove: removeCampaign }} />
          <BiddingStrategyReport brand={brand.brand.id} from={range.from} to={range.to} account={scope.account} />
          <AdResults ws={ws} from={range.from} to={range.to} brand={brandId} go={go} />
        </>
      )}

      {tab === "inbox" && brand.brand && <Inbox ws={ws} brand={brand.brand.id} scope={scope} canManage={canManage} economics={brandAds.economics} reloadBrand={brandAds.reload} />}
      {tab === "customers" && brand.brand && <Customers brand={brand.brand.id} days={days} end={end} customers={customers} campaigns={campaigns} go={go} version={version} />}
      {tab === "verdicts" && brand.brand && <Verdicts brand={brand.brand.id} scope={scope} />}
      {tab === "setup" && brand.brand && <SetUp brand={brand.brand.id} setup={brandAds.setup} economics={brandAds.economics} error={brandAds.error} canManage={canManage} reload={brandAds.reload} go={go} />}

      {tab === "tests" && <AdTests ws={ws} brand={brandKey} scope={scope} campaigns={campaigns} canManage={canManage} />}
      {tab === "changes" && <AdChanges ws={ws} brand={brandKey} scope={scope} />}

      {tab === "tracking" && (
        <>
          <AdTrackingFix ws={ws} accounts={accounts} canManage={canManage} />
      <AdTrackingHealthCard ws={ws} brand={brandKey || undefined} />
      <AdTrackingTemplates ws={ws} />

        </>
      )}

      {tab === "conversions" && (
        <>
          {brand.brand && accounts.filter((a) => a.connector === "google_ads" && a.status === "active").map((a) => <GoogleConversionActions key={a.id} brand={brandId} account={a} canManage={canManage} />)}
          <AdConversions ws={ws} canManage={canManage} accounts={accounts} />
        </>
      )}

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
                {canManage && c.connector === "x_ads" && (
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <span className="text-[12px] text-muted-foreground">{c.accounts === 0 ? "Sign in with the X account that runs your ads, so Outflare can read and manage them." : "Signed in to X."}</span>
                    <Button variant={c.accounts === 0 ? "primary" : "ghost"} onClick={() => signInX(c)}>{c.accounts === 0 ? "Sign in to X" : "Sign in again"}</Button>
                  </div>
                )}
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
      {brand.brand && <GoogleAccountTools brand={brandId} accounts={accounts} canManage={canManage} />}
        </>
      )}
    </div>
  );
}

// The spend, clicks and platform-reported results columns, shared by the
// platform and campaign tables.
export function metricColumns<T>(totals: (r: T) => AdTotals, currency: (r: T) => string) {
  const mono = (s: string) => <span className="font-mono tabular-nums">{s}</span>;
  type Cell = { row: { original: T } };
  // In a download, money is a decimal with its currency's code and a
  // missing number is empty.
  const money$ = (f: (t: AdTotals) => number | null | undefined) => ({ csv: (r: T) => csvMoney(f(totals(r)), currency(r)) }) satisfies ColumnMeta<T>;
  const num$ = (f: (t: AdTotals) => number | null | undefined, digits = 2) => ({ csv: (r: T) => { const v = f(totals(r)); return v == null ? "" : Number(v.toFixed(digits)); } }) satisfies ColumnMeta<T>;
  return [
    { id: "spend", header: "Spend", meta: money$((t) => t.spend_micros), accessorFn: (r: T) => totals(r).spend_micros, cell: ({ row: { original: r } }: Cell) => mono(money(totals(r).spend_micros, currency(r))) },
    { id: "clicks", header: "Clicks", accessorFn: (r: T) => totals(r).clicks, cell: ({ row: { original: r } }: Cell) => mono(n(totals(r).clicks)) },
    { id: "impressions", header: "Impressions", accessorFn: (r: T) => totals(r).impressions, cell: ({ row: { original: r } }: Cell) => mono(n(totals(r).impressions)) },
    { id: "ctr", header: "CTR", meta: num$((t) => t.ctr, 4), accessorFn: (r: T) => totals(r).ctr ?? -1, cell: ({ row: { original: r } }: Cell) => mono(pct(totals(r).ctr)) },
    { id: "cpc", header: "CPC", meta: money$((t) => t.cpc_micros), accessorFn: (r: T) => totals(r).cpc_micros ?? -1, cell: ({ row: { original: r } }: Cell) => mono(money(totals(r).cpc_micros, currency(r))) },
    { id: "conversions", header: "Conv.", meta: num$((t) => t.conversions), accessorFn: (r: T) => totals(r).conversions ?? -1, cell: ({ row: { original: r } }: Cell) => mono(n(totals(r).conversions)) },
    { id: "all_conversions", header: "All conv.", meta: num$((t) => t.all_conversions), accessorFn: (r: T) => totals(r).all_conversions ?? -1, cell: ({ row: { original: r } }: Cell) => <span title="Every conversion the platform counted, its secondary conversion actions included">{mono(n(totals(r).all_conversions))}</span> },
    { id: "cpa", header: "Cost / conv.", meta: money$((t) => t.cost_per_conversion_micros), accessorFn: (r: T) => totals(r).cost_per_conversion_micros ?? -1, cell: ({ row: { original: r } }: Cell) => mono(money(totals(r).cost_per_conversion_micros, currency(r))) },
    { id: "roas", header: "Platform ROAS", meta: num$((t) => t.platform_roas), accessorFn: (r: T) => totals(r).platform_roas ?? -1, cell: ({ row: { original: r } }: Cell) => mono(roas(totals(r).platform_roas)) },
    { id: "link_clicks", header: "Link clicks", meta: num$((t) => t.link_clicks, 0), accessorFn: (r: T) => totals(r).link_clicks ?? -1, cell: ({ row: { original: r } }: Cell) => mono(n(totals(r).link_clicks)) },
    { id: "landing_page_views", header: "Page views", meta: num$((t) => t.landing_page_views, 0), accessorFn: (r: T) => totals(r).landing_page_views ?? -1, cell: ({ row: { original: r } }: Cell) => mono(n(totals(r).landing_page_views)) },
    { id: "video_views", header: "Video views", meta: num$((t) => t.video_views, 0), accessorFn: (r: T) => totals(r).video_views ?? -1, cell: ({ row: { original: r } }: Cell) => mono(n(totals(r).video_views)) },
    { id: "frequency", header: "Frequency", meta: num$((t) => t.frequency), accessorFn: (r: T) => totals(r).frequency ?? -1, cell: ({ row: { original: r } }: Cell) => mono(totals(r).frequency == null ? "—" : totals(r).frequency!.toFixed(2)) },
  ];
}

// deliveryColumns are metricColumns' Meta delivery numbers, hidden until
// the viewer turns them on where most rows won't have them. Clicks, cost,
// impressions, conversions and all conversions show by default: Google's
// Required Minimum Functionality wants them on its account and campaign
// reports (docs/paid-media.md).
export const deliveryColumns = ["link_clicks", "landing_page_views", "video_views", "frequency"];

// statusColumn shows whether a campaign, ad group, ad or keyword is active,
// paused or removed, by default wherever a report lists them all.
export function statusColumn<T extends { status: string }>() {
  const tone = { active: "green", paused: "grey", removed: "red", other: "grey" } as const;
  return {
    id: "status",
    header: "Status",
    accessorFn: (r: T) => r.status.charAt(0).toUpperCase() + r.status.slice(1),
    cell: ({ row: { original: r } }: { row: { original: T } }) => <Badge tone={tone[r.status as keyof typeof tone] ?? "grey"}>{r.status.charAt(0).toUpperCase() + r.status.slice(1)}</Badge>,
  };
}

// downloadByDay saves the by-day numbers, one row per day and currency.
function downloadByDay(rows: AdSummaryRow[], from: string, to: string) {
  const sorted = [...rows].sort((a, b) => a.key.localeCompare(b.key) || a.currency.localeCompare(b.currency));
  downloadCsv(csvName("outflare-ads", "by-day", from, to), toCsv(
    ["Date", "Currency", "Spend", "Clicks", "Impressions", "Conv.", "All conv.", "Cost / conv."],
    sorted.map((r) => [r.key, r.currency, csvMoney(r.totals.spend_micros, r.currency), r.totals.clicks, r.totals.impressions, r.totals.conversions ?? "", r.totals.all_conversions ?? "",
      csvMoney(r.totals.cost_per_conversion_micros, r.currency)]),
  ));
  track("ui.ads_report_downloaded", { report: "by_day", rows: sorted.length });
}

// CustomRange picks any from and to.
function CustomRange({ from, to, apply }: { from: string; to: string; apply: (from: string, to: string) => void }) {
  const [f, setF] = useState(from);
  const [t, setT] = useState(to);
  return (
    <form className="flex flex-wrap items-center gap-1.5 text-[13px]" aria-label="Custom date range" onSubmit={(e) => { e.preventDefault(); apply(f, t); }}>
      <label className="flex items-center gap-1">From <input type="date" className="h-7 rounded-md border bg-background px-1.5" value={f} max={t} onChange={(e) => setF(e.target.value)} aria-label="From" required /></label>
      <label className="flex items-center gap-1">to <input type="date" className="h-7 rounded-md border bg-background px-1.5" value={t} min={f} onChange={(e) => setT(e.target.value)} aria-label="To" required /></label>
      <Button type="submit" variant="ghost">Apply</Button>
    </form>
  );
}

// CampaignReport is every campaign as its network reports it: status,
// clicks, cost, impressions, conversions and all conversions by default
// (Google's campaign report), with the actions a person can take.
function CampaignReport({ campaigns, from, to, canManage, busy, open, manage }: {
  campaigns: AdCampaign[]; from: string; to: string; canManage: boolean; busy: boolean; open: (c: AdCampaign) => void;
  manage: { pause: (c: AdCampaign) => void; resume: (c: AdCampaign) => void; budget: (c: AdCampaign) => void; remove: (c: AdCampaign) => void };
}) {
  if (campaigns.length === 0) return null;
  return (
    <Card title="Campaigns as each network reports them" description={`From ${from} to ${to}: what each platform says every campaign spent and got, active, paused and removed alike. Conversions are the platform's own claim.`}>
      <DataTable
        data={campaigns}
        rowId={(c) => c.id}
        onRowClick={open}
        searchPlaceholder="Search campaigns"
        hidden={[...deliveryColumns, "roas", "ctr"]}
        initialSort={[{ id: "spend", desc: true }]}
        download={reportDownload("campaigns", from, to)}
        empty="No campaigns in this range."
        mobile={(c) => (
          <span className="block min-w-0">
            <span className="block truncate font-medium">{c.name}</span>
            <span className="block truncate text-[12px] text-muted-foreground">{c.status} · {money(c.totals.spend_micros, c.currency)} · {n(c.totals.clicks)} clicks · {n(c.totals.conversions)} conv.</span>
          </span>
        )}
        columns={[
          {
            id: "campaign", header: "Campaign", accessorFn: (c) => c.name,
            cell: ({ row: { original: c } }) => <div className="min-w-40 font-medium">{c.name}</div>,
          },
          { id: "network", header: "Network", accessorFn: (c) => `${platformName(c.connector)} · ${c.account_name}`, cell: ({ row: { original: c } }) => <span className="text-[12px] text-muted-foreground">{platformName(c.connector)} · {c.account_name}</span> },
          statusColumn<AdCampaign>(),
          ...metricColumns<AdCampaign>((c) => c.totals, (c) => c.currency),
          {
            id: "manage", header: "", enableSorting: false,
            cell: ({ row: { original: c } }) => {
              if (!canManage || c.status === "removed" || !manageable.has(c.connector)) return null;
              return (
                <div onClick={(e) => e.stopPropagation()}>
                  <Menu label="Manage" aria={`Manage ${c.name}`} items={[
                    !busy && { label: "Daily budget", onClick: () => manage.budget(c) },
                    !busy && (c.status === "active" ? { label: "Pause", onClick: () => manage.pause(c) } : { label: "Resume", onClick: () => manage.resume(c) }),
                    !busy && c.connector === "google_ads" && { label: "Remove", onClick: () => manage.remove(c), danger: true },
                  ]} />
                </div>
              );
            },
          },
        ]}
      />
    </Card>
  );
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

const changeWord: Record<AdChange["kind"], string> = {
  pause: "Paused", resume: "Resumed", budget: "Daily budget", keyword_add: "Keyword added", negative_add: "Negative keyword added", keyword_split: "Keyword given its own ad group",
  bidding: "Bidding", audience_add: "Audience added", audience_remove: "Audience taken out", country_add: "Country added", country_remove: "Country taken out",
  keyword_pause: "Keyword paused", keyword_resume: "Keyword resumed", ad_pause: "Ad paused", ad_resume: "Ad resumed", negative_remove: "Negative keyword taken out",
  target: "Target moved", exclusion_add: "Exclusion added", exclusion_remove: "Exclusion taken out",
  campaign_remove: "Campaign removed", ad_remove: "Ad removed", keyword_remove: "Keyword removed",
};

// changeDetail is what a change names: a keyword, an audience, a country or a bidding target.
function changeDetail(r: AdChange) {
  const a = r.after;
  if (a.keyword) return <span className="font-mono">[{a.keyword}]</span>;
  if (r.kind === "ad_remove") return <span>{a.name || a.ad_id}</span>;
  if (a.country) return <span>{a.country}</span>;
  if (a.audience_id) return <span>{a.audience_name || a.audience_id}</span>;
  if (a.bidding) {
    const b = a.bidding;
    const target = b.target_cpa_micros != null ? ` to ${(b.target_cpa_micros / 1e6).toFixed(2)} a conversion` : b.target_roas != null ? ` to ${b.target_roas}× return` : "";
    if (b.strategy === "portfolio") return <span>Portfolio strategy {b.bidding_strategy_id}</span>;
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
    api.GET("/v1/brands/{brand}/ads/changes", { params: { path: { brand: pickedBrandId() }, query: { ...(brand ? { brand } : {}), ...scopeQuery(scope), limit: 200 } } }).then(({ data, error }) => {
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
