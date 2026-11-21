import { MatchCheck } from "./ads/MatchCheck";
import { Holdouts } from "./ads/Holdouts";
import { CreativeResults } from "./ads/CreativeResults";
import { WholeBusiness } from "./ads/Business";
import { useCallback, useEffect, useState, type ReactNode } from "react";
import type { AdAccount, AdCampaign, AdChange, AdConnection, AdSummaryRow, AdTotals } from "@outflare/sdk";
import { useBrand, pickedBrandId } from "@/lib/brand";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { BrandBadge } from "@/components/brand-badge";
import { DataTable, type ColumnMeta, type TableDownload } from "@/components/data-table";
import { csvMoney, csvName, downloadCsv, toCsv } from "@/lib/csv";
import { api, errorMessage } from "../api";
import { track } from "@/lib/analytics";
import { adStatusWord, keepStatus, metricFooter, servingStatus, StatusFilterSelect, useStatusFilter } from "./ads/status";
import { ask, Badge, Button, Card, confirmAction, ErrorText, Input, Loading, Menu, PageHeader, Status, toast } from "../ui";
import { ago } from "../format";
import type { Tab } from "../App";
import { AdConversions } from "./AdConversions";
import { AdTrackingFix, AdTrackingHealthCard, AdTrackingTemplates } from "./AdTracking";
import { AdResults } from "./AdResults";
import { AdTests } from "./AdTests";
import { AdTrend, CampaignDetail, ConversionActions } from "./AdDrilldown";
import { AdsPanels } from "@/components/insight-panels";
import { PickBrand } from "@/components/pick-brand";
import { customerBuckets, isAlert, isOpen, isShadow, statusOf, useBrandAds, windows, type AdEconomics, type AdPerformanceDay, type AdPerformanceRow, type AdSetup, type Customers as CustomerBucket, type Finding } from "./ads/api";
import { BrandSplit, CostChart, Funnel, NextActions } from "./ads/Overview";
import { CreativePage, creativeItem, creativePages } from "./ads/Creatives";
import { Hero, setBudgetCeiling, setPayerTarget } from "./ads/Hero";
import { Inbox } from "./ads/Inbox";
import { SendLog } from "./ads/SendLog";
import { ActionPage } from "./ads/Action";
import { Results } from "./ads/Results";
import { Customers } from "./ads/Customers";
import { Audiences } from "./ads/Audiences";
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
  { id: "chatgpt_ads", name: "ChatGPT Ads" },
  { id: "zeropark_ads", name: "Zeropark" },
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

// The date ranges, in one menu: today, yesterday, the last 7, 28, 30 or 90
// days, this month, last month, or any from and to.
const day = (d: Date) => d.toISOString().slice(0, 10);
const presets = [
  { id: "today", label: "Today" },
  { id: "yesterday", label: "Yesterday" },
  { id: "7", label: "Last 7 days" },
  { id: "28", label: "Last 28 days" },
  { id: "30", label: "Last 30 days" },
  { id: "90", label: "Last 90 days" },
  { id: "this_month", label: "This month" },
  { id: "last_month", label: "Last month" },
] as const;
type Preset = (typeof presets)[number]["id"];
function presetRange(p: Preset, now = new Date()): { from: string; to: string } {
  const back = (n: number) => day(new Date(now.getTime() - n * 86_400_000));
  const today = day(now);
  if (p === "today") return { from: today, to: today };
  if (p === "yesterday") return { from: back(1), to: back(1) };
  if (p === "this_month") return { from: `${today.slice(0, 8)}01`, to: today };
  if (p === "last_month") {
    const end = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 0));
    return { from: `${day(end).slice(0, 8)}01`, to: day(end) };
  }
  return { from: back(Number(p) - 1), to: today };
}
const spanDays = (from: string, to: string) => Math.round((Date.parse(`${to}T00:00:00Z`) - Date.parse(`${from}T00:00:00Z`)) / 86_400_000) + 1;

// reportDownload is a Paid ads report's "Download CSV": its file is named
// after the report and its dates, and each download is recorded.
export const reportDownload = (report: string, ...parts: string[]): TableDownload => ({
  file: csvName("outflare-ads", report.replace(/_/g, "-"), ...parts),
  onDownload: (rows) => track("ui.ads_report_downloaded", { report, rows }),
});

// Money arrives in micros (millionths) of the account's currency. Every
// amount shows the currency's own decimals ($20.00, $1,250.00, ¥2,000),
// as Google Ads writes them, so a column of amounts lines up.
export const money = (micros: number | null | undefined, currency: string) => {
  if (micros === null || micros === undefined) return "—";
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency }).format(micros / 1e6);
  } catch {
    return `${(micros / 1e6).toFixed(2)} ${currency}`;
  }
};
export const n = (v: number | null | undefined) => (v === null || v === undefined ? "—" : Math.round(v).toLocaleString());
const pct = (v: number | null | undefined) => (v === null || v === undefined ? "—" : `${(v * 100).toFixed(2)}%`);
const share = (v: number | null | undefined) => (v === null || v === undefined ? "—" : `${(v * 100).toFixed(1)}%`);
const roas = (v: number | null | undefined) => (v === null || v === undefined ? "—" : `${v.toFixed(2)}×`);

// The platforms whose campaigns Outflare can change (connectors.AdsManager).
const manageable = new Set(["google_ads", "meta_ads", "microsoft_ads", "linkedin_ads", "tiktok_ads", "reddit_ads", "x_ads", "chatgpt_ads", "zeropark_ads", "taboola_ads", "outbrain_ads"]);

// The page's screens, as the item in its URL (the overview has none): the
// six of plans/paid-ads first, then the older tabs in a second row.
// Six tabs, one job each. Settings (the "setup" item in links) also holds five pages of its own (tracking,
// sending conversions, the send log, accounts and the change history),
// each one click away on the row of pages under the tabs.
const adsTabs = ["overview", "business", "actions", "campaigns", "creatives", "customers", "audiences", "experiments", "setup", "changes", "tracking", "conversions", "sends", "accounts"] as const;
type AdsTab = (typeof adsTabs)[number];
const mainTabs: { id: AdsTab; label: string }[] = [
  { id: "overview", label: "Overview" },
  { id: "business", label: "Whole business" },
  { id: "actions", label: "Actions" },
  { id: "campaigns", label: "Campaigns" },
  { id: "creatives", label: "Creatives" },
  { id: "customers", label: "Customers" },
  { id: "audiences", label: "Audiences" },
  { id: "experiments", label: "Experiments" },
  { id: "setup", label: "Settings" },
];
const setupPages: Partial<Record<AdsTab, string>> = { tracking: "Tracking", conversions: "Send conversions", sends: "Send log", accounts: "Ad accounts", changes: "Change history" };
// The screens that need one brand picked.
const brandNeeded: readonly AdsTab[] = ["business", "actions", "campaigns", "creatives", "customers", "audiences", "experiments", "setup"];
// Older links keep working: the inbox and suggestions are Actions, results
// are Campaigns, verdicts and tests are Experiments.
const legacyTabs: Record<string, AdsTab> = { suggestions: "actions", inbox: "actions", results: "campaigns", verdicts: "experiments", tests: "experiments" };

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
  // "actions/<id>" is one action's own page.
  const actionId = item?.startsWith("actions/") ? item.slice("actions/".length) : "";
  // "audiences/new" and "audiences/<id>" are an audience's own page.
  const audienceItem = item?.startsWith("audiences/") ? item.slice("audiences/".length) : "";
  // "<creatives page>" and "<creatives page>/<id>" are the Creatives tab's
  // pages; "creatives" itself is the first of them, Results.
  const creative = creativeItem(item);
  const tab: AdsTab = creative ? "creatives" : actionId ? "actions" : audienceItem ? "audiences" : (adsTabs as readonly string[]).includes(item ?? "") ? (item as AdsTab) : legacyTabs[item ?? ""] ?? "overview";
  const [openId, openGroup] = tab === "overview" && item && !legacyTabs[item] ? item.split("/") : [];
  const brand = useBrand(ws);
  const brandKey = brand.query.brand ?? "";
  const brandId = brand.brand?.id ?? "";
  // The range: the last days, or a custom from and to (end is its last
  // day, which the brand screens count back from).
  const [preset, setPreset] = useState<Preset>("28");
  const [custom, setCustom] = useState<{ from: string; to: string } | null>(null);
  const [picking, setPicking] = useState(false);
  const range = custom ?? presetRange(preset);
  const days = spanDays(range.from, range.to);
  const end = range.to === day(new Date()) ? undefined : range.to;
  const rangeLabel = custom ? `${custom.from} – ${custom.to}` : presets.find((p) => p.id === preset)!.label;
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
  const [byDay, setByDay] = useState<AdSummaryRow[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  // The brand's findings, for the count on Actions and "Do this next", and
  // the campaign rows behind the number, which Overview's tables reuse.
  const [findings, setFindings] = useState<Finding[]>([]);
  const [perf, setPerf] = useState<AdPerformanceRow[]>([]);
  const [perfDays, setPerfDays] = useState<AdPerformanceDay[]>([]);
  const [perfBefore, setPerfBefore] = useState<AdPerformanceDay[]>([]);
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
    // Every brand's accounts and those with none yet, for assigning.
    const all = await api.GET("/v1/workspaces/{ws}/ads/accounts", { params: { path: { ws }, query: { brand: "all" } } });
    const [c, a, cp, s, d, ba, f] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/ads/connections", { params: { path: { ws } } }),
      api.GET("/v1/brands/{brand}/ads/accounts", { params: { path } }),
      // idle: every campaign, also a paused one that never spent, so the
      // status filter, not the spend, decides what the table shows.
      api.GET("/v1/brands/{brand}/ads/campaigns", { params: { path, query: { ...narrow, ...range, limit: 1000, idle: true } } }),
      api.GET("/v1/brands/{brand}/ads/summary", { params: { path, query: { ...narrow, ...range, group: "connector" } } }),
      api.GET("/v1/brands/{brand}/ads/summary", { params: { path, query: { ...narrow, ...range, group: "day" } } }),
      api.GET("/v1/brands/{brand}/ads/summary", { params: { path, query: { ...narrow, ...range, group: "account" } } }),
      api.GET("/v1/brands/{brand}/ads/findings", { params: { path, query: { ...narrow, limit: 300 } } }),
    ]);
    if (c.error || !c.data) return setError(errorMessage(c.error));
    setError(a.error || cp.error || s.error ? errorMessage(a.error ?? cp.error ?? s.error) : "");
    setConns(c.data.data);
    setAccounts(a.data?.data ?? []);
    setAllAccounts(all.data?.data ?? []);
    setCampaigns(cp.data?.data ?? []);
    setByPlatform(s.data?.data ?? []);
    setByDay(d.data?.data ?? []);
    setByAccount(ba.data?.data ?? []);
    setFindings((f.data?.data ?? []) as Finding[]);
  }, [ws, brandKey, range.from, range.to, scope.network, scope.account]); // eslint-disable-line react-hooks/exhaustive-deps
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
    // Over the brand's daily ceiling: say so and ask a second time.
    if (kind !== "pause" && error && (error as { error?: { code?: string } }).error?.code === "ad_budget_ceiling") {
      const ok = await confirmAction("This goes over your daily budget ceiling", <>
        <p>{kind === "budget"
          ? <>Setting {c.name} to {money(budget ?? 0, c.currency)} a day</>
          : <>Resuming {c.name}{c.daily_budget_micros != null ? <> ({money(c.daily_budget_micros, c.currency)} a day)</> : null}</>} takes your running campaigns' budgets together over the ceiling you set under Settings, Limits.</p>
        <p>Go ahead anyway? The ceiling stays as it is.</p>
      </>, kind === "budget" ? "Raise it anyway" : "Resume anyway", false);
      if (!ok) return;
      setBusy(true);
      const again = kind === "budget"
        ? await api.PATCH("/v1/brands/{brand}/ads/campaigns/{id}", { ...p, body: { daily_budget_micros: budget ?? 0, over_ceiling: true } })
        : await api.POST("/v1/brands/{brand}/ads/campaigns/{id}/resume", { ...p, body: { over_ceiling: true } });
      setBusy(false);
      if (again.error) return toast(errorMessage(again.error), "error");
      toast(`${kind === "budget" ? "Budget change" : "Resume"} sent to the platform. It shows here once the platform confirms it.`);
      setTimeout(load, 3000);
      return;
    }
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

  // Several campaigns at once: one confirmation that lists each one with
  // its status and what it spent in the last 30 days, then each change is
  // queued like a single one.
  async function spent30() {
    const day = (d: number) => new Date(Date.now() - d * 86400000).toISOString().slice(0, 10);
    const { data } = await api.GET("/v1/brands/{brand}/ads/campaigns", { params: { path: { brand: brandId }, query: { from: day(29), to: day(0), limit: 1000, idle: true } } });
    const by = new Map((data?.data ?? []).map((c) => [c.id, c.totals.spend_micros]));
    return (c: AdCampaign) => by.get(c.id) ?? 0;
  }
  function bulkList(cs: AdCampaign[], spend: (c: AdCampaign) => number, extra?: (c: AdCampaign) => ReactNode) {
    return (
      <table className="mt-1 w-full text-[12px]">
        <thead><tr className="text-left text-muted-foreground"><th className="font-normal">Campaign</th><th className="font-normal">Status</th><th className="text-right font-normal">Spent, 30 days</th>{extra && <th className="text-right font-normal">Change</th>}</tr></thead>
        <tbody>{cs.map((c) => (
          <tr key={c.id}><td className="max-w-[16rem] truncate pr-2">{c.name}</td><td className="pr-2">{adStatusWord(c.status)}</td><td className="text-right font-mono tabular-nums">{money(spend(c), c.currency)}</td>{extra && <td className="text-right font-mono tabular-nums">{extra(c)}</td>}</tr>
        ))}</tbody>
      </table>
    );
  }
  async function bulkSend(kind: "pause" | "resume" | "remove", cs: AdCampaign[]) {
    setBusy(true);
    const failed: string[] = [];
    let held = 0;
    for (const c of cs) {
      const p = { params: { path: { brand: brandId, id: c.id } } };
      const r = kind === "pause" ? await api.POST("/v1/brands/{brand}/ads/campaigns/{id}/pause", p)
        : kind === "resume" ? await api.POST("/v1/brands/{brand}/ads/campaigns/{id}/resume", p)
        : await api.POST("/v1/brands/{brand}/ads/campaigns/{id}/remove", { ...p, body: { confirm: true } });
      if (r.error) failed.push(`${c.name} (${errorMessage(r.error)})`);
      else if ((r.data as { action?: unknown } | undefined)?.action) held++;
    }
    setBusy(false);
    track("ui.ads_bulk_changed", { kind, campaigns: cs.length, failed: failed.length });
    const word = kind === "pause" ? "Pause" : kind === "resume" ? "Resume" : "Removal";
    const sent = cs.length - failed.length;
    if (failed.length) toast(`${sent > 0 ? `${word} sent for ${sent}. ` : ""}Could not ${kind}: ${failed.join("; ")}.`, "error");
    else toast(`${word} sent for ${cs.length} ${cs.length === 1 ? "campaign" : "campaigns"}${held ? `, ${held} waiting for approval under Approvals` : ""}. They show here once the platform confirms.`);
    setTimeout(load, 3000);
  }
  async function pauseMany(cs: AdCampaign[]) {
    const live = cs.filter((c) => c.status === "active");
    if (live.length === 0) return;
    const spend = await spent30();
    if (!(await confirmAction(`Pause ${live.length} ${live.length === 1 ? "campaign" : "campaigns"}?`, <>{bulkList(live, spend)}<p>They stop spending once the platform confirms. You can resume them later.</p></>, "Pause", false))) return;
    await bulkSend("pause", live);
  }
  async function resumeMany(cs: AdCampaign[]) {
    const stopped = cs.filter((c) => c.status === "paused");
    if (stopped.length === 0) return;
    const spend = await spent30();
    const daily = stopped.reduce((a, c) => a + (c.daily_budget_micros ?? 0), 0);
    if (!(await confirmAction(`Resume ${stopped.length} ${stopped.length === 1 ? "campaign" : "campaigns"}?`, <>
      {bulkList(stopped, spend, (c) => (c.daily_budget_micros != null ? `${money(c.daily_budget_micros, c.currency)}/day` : "shared"))}
      <p>This change can spend more{daily > 0 ? <>: up to {money(daily, stopped[0].currency)} a day together</> : null}. They start spending once the platform confirms.</p>
    </>, "Resume", false))) return;
    await bulkSend("resume", stopped);
  }
  async function removeMany(cs: AdCampaign[]) {
    const google = cs.filter((c) => c.connector === "google_ads" && c.status !== "removed");
    const other = cs.length - google.length;
    if (google.length === 0) return toast("Only Google Ads campaigns can be removed from here.", "error");
    const spend = await spent30();
    if (!(await confirmAction(`Remove ${google.length} ${google.length === 1 ? "campaign" : "campaigns"} for good?`, <>
      {bulkList(google, spend)}
      <p>Google Ads can't undo a removal: each campaign stops for good with its ad groups, ads and keywords. Their numbers stay here and in Google Ads. To stop them for now, pause them instead.</p>
      {other > 0 && <p>{other} of the ticked {other === 1 ? "campaign is" : "campaigns are"} left out: only Google Ads campaigns that aren't removed yet can be removed here.</p>}
    </>, `Remove ${google.length === 1 ? "campaign" : `${google.length} campaigns`}`))) return;
    await bulkSend("remove", google);
  }
  async function budgetMany(cs: AdCampaign[]) {
    const withBudget = cs.filter((c) => (c.daily_budget_micros ?? 0) > 0);
    if (withBudget.length === 0) return toast("None of the ticked campaigns has a daily budget of its own.", "error");
    const r = await ask({
      title: `Change the budget of ${withBudget.length} ${withBudget.length === 1 ? "campaign" : "campaigns"}`,
      body: "By how many percent: 20 raises each daily budget by a fifth, -20 lowers it by a fifth.",
      input: { label: "Change in percent", type: "number", value: "20", placeholder: "20" },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Continue", value: "ok", variant: "primary" }],
    });
    const pct = Number(r?.value);
    if (r?.choice !== "ok" || !Number.isFinite(pct) || pct === 0 || pct <= -100) return;
    const next = (c: AdCampaign) => Math.max(10_000, Math.round(((c.daily_budget_micros ?? 0) * (1 + pct / 100)) / 10_000) * 10_000);
    const currency = withBudget[0].currency;
    const before = withBudget.reduce((a, c) => a + (c.daily_budget_micros ?? 0), 0);
    const after = withBudget.reduce((a, c) => a + next(c), 0);
    const spend = await spent30();
    const ok = await confirmAction(`${pct > 0 ? "Raise" : "Lower"} ${withBudget.length} ${withBudget.length === 1 ? "budget" : "budgets"} by ${Math.abs(pct)}%?`, <>
      {pct > 0 && <p>This change can spend more.</p>}
      <p>Daily budgets together: {money(before, currency)} → {money(after, currency)}.</p>
      {pct > 0 && <p>The most it can add: {money(Math.round((after - before) * 30.4), currency)} a month.</p>}
      {bulkList(withBudget, spend, (c) => `${money(c.daily_budget_micros, c.currency)} → ${money(next(c), c.currency)}`)}
    </>, pct > 0 ? "Raise the budgets" : "Lower the budgets", false);
    if (!ok) return;
    setBusy(true);
    const failed: string[] = [];
    for (const c of withBudget) {
      const { error } = await api.PATCH("/v1/brands/{brand}/ads/campaigns/{id}", { params: { path: { brand: brandId, id: c.id } }, body: { daily_budget_micros: next(c) } });
      if (error) failed.push(c.name);
    }
    setBusy(false);
    track("ui.ads_bulk_changed", { kind: pct > 0 ? "budget_up" : "budget_down", campaigns: withBudget.length, failed: failed.length });
    toast(failed.length ? `Could not change: ${failed.join(", ")}.` : `Budget change sent for ${withBudget.length} ${withBudget.length === 1 ? "campaign" : "campaigns"}. They show here once the platform confirms.`, failed.length ? "error" : undefined);
    setTimeout(load, 3000);
  }
  // A campaign's own page in its ad network.
  const campaignLink = (c: AdCampaign) => {
    const acc = accounts.find((a) => a.id === c.ad_account_id);
    const digits = acc?.external_id.replace(/\D/g, "") ?? "";
    return c.connector === "google_ads" && digits && /^\d+$/.test(c.external_id) ? `https://ads.google.com/aw/adgroups?campaignId=${c.external_id}&__e=${digits}` : null;
  };

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
  function pickRange(p: Preset) {
    setCustom(null);
    setPicking(false);
    setPreset(p);
    const r = presetRange(p);
    track("ui.ads_range_changed", { days: spanDays(r.from, r.to) });
  }
  function applyCustom(from: string, to: string) {
    if (!from || !to || from > to) return toast("Pick a from date on or before the to date.", "error");
    if (spanDays(from, to) > 3 * 366) return toast("A range is at most three years.", "error");
    setCustom({ from, to });
    setPicking(false);
    track("ui.ads_range_changed", { days: spanDays(from, to), custom: true });
  }

  // A campaign opened by its link that the list leaves out (paused, with no
  // spend in the range) is read on its own: undefined while it loads, null
  // when this brand has no such campaign.
  const [opened, setOpened] = useState<AdCampaign | null | undefined>(undefined);
  const listed = !!openId && campaigns.some((x) => x.id === openId);
  useEffect(() => {
    if (!openId || listed || !conns) return;
    let live = true;
    setOpened(undefined);
    api.GET("/v1/brands/{brand}/ads/campaigns", { params: { path: { brand: brandId }, query: { ...range, id: openId } } }).then(({ data }) => {
      if (live) setOpened(data?.data?.[0] ?? null);
    });
    return () => { live = false; };
  }, [openId, listed, !conns, brandId, range.from, range.to]); // eslint-disable-line react-hooks/exhaustive-deps

  const only = allAccounts.length === 1 ? allAccounts[0] : null;
  // What waits for a decision: open proposals and open alerts.
  const toDo = findings.filter((f) => !isShadow(f) && (isOpen(f) || (isAlert(f) && statusOf(f) === "open"))).length;
  const header = (
    <PageHeader
      title="Paid ads"
      description="Get more paying customers at or under your target cost. Nothing changes or spends without your OK."
      action={
        <div className="flex flex-wrap items-center gap-2">
          <Menu label={rangeLabel} aria="Date range" items={[
            ...presets.map((p) => ({ label: p.label, onClick: () => pickRange(p.id) })),
            { label: "Custom range", onClick: () => setPicking(true) },
          ]} />
          {picking && <CustomRange from={range.from} to={range.to} apply={applyCustom} cancel={() => setPicking(false)} />}
          {canManage && conns && conns.length > 0 && <Button variant="ghost" disabled={busy} onClick={syncNow}>Sync now</Button>}
          {canManage && brand.brand && campaigns.some((c) => c.status === "active") && (
            <Menu label="More" aria="More actions" items={[{ label: "Pause all ads", onClick: pauseAll, danger: true }]} />
          )}
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
    const c = campaigns.find((x) => x.id === openId) ?? (opened?.id === openId ? opened : undefined);
    if (!c && opened === undefined) return <div className="space-y-6">{header}<Loading /></div>;
    return (
      <div className="space-y-6">
        {header}
        {c ? (
          <CampaignDetail key={c.id} ws={ws} campaign={c} groupId={openGroup || undefined} from={range.from} to={range.to} canManage={canManage}
            open={(g) => go("ads", g ? `${c.id}/${g}` : c.id)} back={() => go("ads", "campaigns")} />
        ) : (
          <Card title="Campaign not found" description="This brand has no campaign with this link. It may belong to another brand.">
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
      <Levels scope={scope} setScope={setScope} accounts={allAccounts} synced={only?.last_synced_at ?? null} shownIn={brandAds.economics?.currency || "USD"} />
      <Tabs value={setupPages[tab] ? "setup" : tab} onValueChange={openScreen}>
        <TabsList className="h-auto! flex-wrap justify-start" aria-label="Paid ads screens">
          {mainTabs.map((t) => (
            <TabsTrigger key={t.id} value={t.id} className="px-3 text-[13px]" onClick={t.id === "setup" && setupPages[tab] ? () => openScreen("setup") : undefined}>
              {t.label}
              {t.id === "actions" && toDo > 0 && <span className="ml-1.5 rounded-full bg-foreground px-1.5 font-mono text-[11px] text-background" aria-label={`${toDo} to do`}>{toDo}</span>}
            </TabsTrigger>
          ))}
        </TabsList>
      </Tabs>
      {creative && (
        <nav aria-label="Creatives pages" className="flex flex-wrap gap-1 border-b text-[13px]">
          {creativePages.map(({ id, label }) => (
            <button
              key={id} type="button" aria-current={creative.page === id ? "page" : undefined} onClick={() => openScreen(id)}
              className={`-mb-px border-b-2 px-3 py-2 ${creative.page === id ? "border-foreground font-medium" : "border-transparent text-muted-foreground hover:text-foreground"}`}
            >{label}</button>
          ))}
        </nav>
      )}
      {(tab === "setup" || setupPages[tab]) && (
        // Settings' own pages, always in view, so any of them is one click
        // from any other and there is nothing to go back out of.
        <nav aria-label="Settings pages" className="flex flex-wrap gap-1 border-b text-[13px]">
          {([["setup", "General"], ...Object.entries(setupPages)] as [string, string][]).map(([id, label]) => (
            <button
              key={id} type="button" aria-current={tab === id ? "page" : undefined} onClick={() => openScreen(id)}
              className={`-mb-px border-b-2 px-3 py-2 ${tab === id ? "border-foreground font-medium" : "border-transparent text-muted-foreground hover:text-foreground"}`}
            >{label}</button>
          ))}
        </nav>
      )}
      {error && <ErrorText>{error}</ErrorText>}
      {brandAds.error && brandId && <ErrorText>{brandAds.error}</ErrorText>}

      {conns.filter((c) => c.setup_needed.length > 0).map((c) => <Setup key={c.id} ws={ws} conn={c} canManage={canManage} done={load} />)}

      {brandNeeded.includes(tab) && !brand.brand && <PickBrand what="A brand's ad numbers, decisions and customers" brands={brand.active.length} />}

      {tab === "overview" && brand.brand && (
        <>
          <Hero brand={brand.brand.id} days={days} end={end} windowDays={windowDays} customers={customers} scope={scope} economics={brandAds.economics} setup={brandAds.setup} canManage={canManage} reload={brandAds.reload} go={go} version={version} onRows={(rows, daily, previous) => { setPerf(rows); setPerfDays(daily ?? []); setPerfBefore(previous ?? []); }} toDo={toDo} />
          <CostChart daily={perfDays} previous={perfBefore} from={range.from} to={range.to} target={brandAds.economics?.target_cost_per_payer_micros ?? null} />
          <BrandSplit rows={perf} brandName={brand.brand.name} target={brandAds.economics?.target_cost_per_payer_micros ?? null} open={() => openScreen("campaigns")} />
          <NextActions findings={findings} shadowMode={brandAds.economics?.shadow_mode === true} open={() => openScreen("actions")} />
          <Funnel rows={perf} />
          <AdsPanels brand={brand.brand.id} days={days} go={go} />
        </>
      )}

      {tab === "campaigns" && brand.brand && (
        <>
          <Results brand={brand.brand.id} days={days} end={end} windowDays={windowDays} customers={customers} scope={scope} canManage={canManage} go={go} version={version}
            manage={{ campaigns, busy, link: campaignLink, pauseMany, resumeMany, removeMany, budgetMany, manageable: (c) => manageable.has(c), pause: (c) => change(c, "pause"), resume: (c) => change(c, "resume"), budget: setBudget, target: (c) => setTarget("campaigns", c.id, c.name, c.currency, c.target_cost_per_conversion_micros, c.target_from === "campaign"), remove: removeCampaign }} />
          <CampaignReport campaigns={campaigns} from={range.from} to={range.to} canManage={canManage} busy={busy} open={(c) => go("ads", c.id)}
            manage={{ pause: (c) => change(c, "pause"), resume: (c) => change(c, "resume"), budget: setBudget, remove: removeCampaign }} />
          <BiddingStrategyReport brand={brand.brand.id} from={range.from} to={range.to} account={scope.account} />
          <AdResults ws={ws} from={range.from} to={range.to} brand={brandId} go={go} />

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
          mobile={(r) => (
            <span className="block min-w-0">
              <span className="block truncate font-medium">{platformName(r.key)}</span>
              <span className="block truncate text-[12px] text-muted-foreground">{money(r.totals.spend_micros, r.currency)} · {n(r.totals.clicks)} clicks · {n(r.totals.conversions)} conv.</span>
            </span>
          )}
          empty="Nothing spent in this range yet."
          initialSort={[{ id: "spend", desc: true }]}
          columns={[
            { id: "platform", header: "Platform", accessorFn: (r) => platformName(r.key), cell: ({ row: { original: r } }) => <span className="font-medium">{platformName(r.key)}</span> },
            { id: "currency", header: "Currency", accessorFn: (r) => r.currency, cell: ({ row: { original: r } }) => <span className="text-muted-foreground">{r.currency}</span> },
            ...metricColumns<AdSummaryRow>((r) => r.totals, (r) => r.currency),
          ]}
        />
      </Card>

      {!scope.account && byAccount.length > 1 && (
        <Card title="By account" description={`From ${range.from} to ${range.to}, each ad account on its own, as each platform reports it. Open one to see only its campaigns, changes and suggestions.`}>
          <DataTable
            data={byAccount}
            rowId={(r) => `${r.key}-${r.currency}`}
            columnsMenu={false}
            hidden={deliveryColumns}
            download={reportDownload("by_account", range.from, range.to)}
            mobile={(r) => (
              <span className="block min-w-0">
                <span className="block truncate font-medium">{allAccounts.find((x) => x.id === r.key)?.name ?? r.key}</span>
                <span className="block truncate text-[12px] text-muted-foreground">{money(r.totals.spend_micros, r.currency)} · {n(r.totals.clicks)} clicks · {n(r.totals.conversions)} conv.</span>
              </span>
            )}
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
        </>
      )}

      {tab === "actions" && brand.brand && actionId && <ActionPage key={actionId} brand={brand.brand.id} id={actionId} canManage={canManage} go={go} changed={load} />}
      {tab === "actions" && brand.brand && !actionId && <Inbox ws={ws} brand={brand.brand.id} scope={scope} canManage={canManage} economics={brandAds.economics} reloadBrand={brandAds.reload} go={go} changed={load} />}
      {creative && creative.page !== "creatives" && brand.brand && (
        <CreativePage
          ws={ws} brand={brand.brand} canManage={canManage} page={creative.page} id={creative.id}
          open={(v) => go("ads", v)} onConnectors={() => go("connectors")} brandChanged={brand.reload} openProfile={() => go("brand-profile")}
        />
      )}
      {tab === "audiences" && brand.brand && <Audiences brand={brand.brand.id} canManage={canManage} go={go} item={audienceItem} />}
      {tab === "customers" && brand.brand && <Customers brand={brand.brand.id} days={days} end={end} customers={customers} campaigns={campaigns} go={go} version={version} />}
      {tab === "business" && brand.brand && (
        <WholeBusiness brand={brand.brand.id} from={range.from} to={range.to} economics={brandAds.economics} canManage={canManage} reload={brandAds.reload} beforeWords={`the ${days} days before`} />
      )}
      {creative?.page === "creatives" && brand.brand && (
        <CreativeResults brand={brand.brand.id} from={range.from} to={range.to} target={brandAds.economics?.target_cost_per_payer_micros} canManage={canManage} openCampaign={(id) => go("ads", id)} />
      )}
      {tab === "experiments" && brand.brand && (
        <>
          <AdTests ws={ws} brand={brandKey} scope={scope} campaigns={campaigns} canManage={canManage} />
          <Holdouts brand={brand.brand.id} connectors={[...new Set(accounts.map((x) => x.connector))]} canManage={canManage} />
          <Verdicts brand={brand.brand.id} scope={scope} />
        </>
      )}
      {tab === "setup" && brand.brand && (
        <>
          <SetupLines
            ws={ws} accounts={accounts} conns={conns} shadowMode={brandAds.economics?.shadow_mode === true}
            setup={brandAds.setup} economics={brandAds.economics}
            changeTarget={canManage ? () => setPayerTarget(brand.brand!.id, brandAds.economics?.currency || "USD", brandAds.economics?.target_cost_per_payer_micros ?? null, brandAds.reload) : undefined}
                        changeCeiling={canManage ? () => setBudgetCeiling(brand.brand!.id, brandAds.economics?.currency || "USD", brandAds.economics?.daily_budget_ceiling_micros ?? null, brandAds.reload) : undefined}
            windowDays={windowDays} setWindowDays={(w) => { setWindowDays(w); track("ui.ads_window_changed", { days: w }); }}
            customers={customers} setCustomers={(c) => { setCustomers(c); track("ui.ads_customers_filter_changed", { customers: c }); }}
            open={openScreen}
          />
          <SetUp brand={brand.brand.id} setup={brandAds.setup} economics={brandAds.economics} error={brandAds.error} canManage={canManage} reload={brandAds.reload} go={go} />
        </>
      )}

      {tab === "changes" && <AdChanges ws={ws} brand={brandKey} scope={scope} />}

      {tab === "tracking" && (
        <>
          <AdTrackingFix ws={ws} accounts={accounts} canManage={canManage} />
      <AdTrackingHealthCard ws={ws} brand={brandKey || undefined} />
      <AdTrackingTemplates ws={ws} />
          {brand.brand && <MatchCheck brand={brandId} />}
        </>
      )}

      {tab === "conversions" && (
        <>
          {brand.brand && accounts.filter((a) => a.connector === "google_ads" && a.status === "active").map((a) => <GoogleConversionActions key={a.id} brand={brandId} account={a} canManage={canManage} />)}
          <AdConversions ws={ws} canManage={canManage} accounts={accounts} openLog={() => openScreen("sends")} />
          <ConversionActions ws={ws} from={range.from} to={range.to} brand={brandKey || undefined} card download />
        </>
      )}

      {tab === "sends" && <SendLog ws={ws} brand={brandId} accounts={accounts} go={go} />}

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
                {c.conversion_fields.length > 0 && <ConversionSetup ws={ws} conn={c} canManage={canManage} done={load} />}
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
  // Numbers sit to the right, under their headings, so a column reads down.
  const cols = [
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
    { id: "video_thruplays", header: "Watched", meta: num$((t) => t.video_thruplays, 0), accessorFn: (r: T) => totals(r).video_thruplays ?? -1, cell: ({ row: { original: r } }: Cell) => <span title="Video plays to the end or long enough to count as watched: Meta's ThruPlays (15 seconds or the whole video), TikTok's plays of 6 seconds or more">{mono(n(totals(r).video_thruplays))}</span> },
    { id: "hook_rate", header: "Hook rate", meta: num$((t) => t.hook_rate), accessorFn: (r: T) => totals(r).hook_rate ?? -1, cell: ({ row: { original: r } }: Cell) => <span title="Video views over impressions: how often the opening stops the scroll">{mono(share(totals(r).hook_rate))}</span> },
    { id: "hold_rate", header: "Hold rate", meta: num$((t) => t.hold_rate), accessorFn: (r: T) => totals(r).hold_rate ?? -1, cell: ({ row: { original: r } }: Cell) => <span title="Watched over video views: how many of those who stopped kept watching">{mono(share(totals(r).hold_rate))}</span> },
    { id: "video_p100", header: "Watched to the end", meta: num$((t) => t.video_p100, 0), accessorFn: (r: T) => totals(r).video_p100 ?? -1, cell: ({ row: { original: r } }: Cell) => <span title={`A quarter: ${n(totals(r).video_p25)} · half: ${n(totals(r).video_p50)} · three quarters: ${n(totals(r).video_p75)}`}>{mono(n(totals(r).video_p100))}</span> },
    { id: "frequency", header: "Frequency", meta: num$((t) => t.frequency), accessorFn: (r: T) => totals(r).frequency ?? -1, cell: ({ row: { original: r } }: Cell) => mono(totals(r).frequency == null ? "—" : totals(r).frequency!.toFixed(2)) },
  ];
  return cols.map((c) => ({ ...c, meta: { ...("meta" in c ? c.meta : {}), align: "right" as const } }));
}

// deliveryColumns are metricColumns' Meta delivery numbers, hidden until
// the viewer turns them on where most rows won't have them. Clicks, cost,
// impressions, conversions and all conversions show by default: Google's
// Required Minimum Functionality wants them on its account and campaign
// reports (docs/paid-media.md).
export const deliveryColumns = ["link_clicks", "landing_page_views", "video_views", "video_thruplays", "hook_rate", "hold_rate", "video_p100", "frequency"];

// statusColumn shows whether a campaign, ad group, ad or keyword is
// enabled, paused or removed, in Google Ads' words, and for one that is
// enabled whether it is really serving (Eligible, Limited by budget,
// Learning) when the network says.
export function statusColumn<T extends { status: string; primary_status?: string; primary_status_reasons?: string[]; details?: { primary_status?: string; primary_status_reasons?: string } }>() {
  const of = (r: T) => servingStatus(r.status, r.primary_status ?? r.details?.primary_status, r.primary_status_reasons ?? r.details?.primary_status_reasons);
  return {
    id: "status",
    header: "Status",
    accessorFn: (r: T) => of(r).word,
    cell: ({ row: { original: r } }: { row: { original: T } }) => { const s = of(r); return <span title={s.title}><Status tone={s.tone}>{s.word}</Status></span>; },
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
function CustomRange({ from, to, apply, cancel }: { from: string; to: string; apply: (from: string, to: string) => void; cancel: () => void }) {
  const [f, setF] = useState(from);
  const [t, setT] = useState(to);
  return (
    <form className="flex flex-wrap items-center gap-1.5 text-[13px]" aria-label="Custom date range" onSubmit={(e) => { e.preventDefault(); apply(f, t); }}>
      <label className="flex items-center gap-1">From <input type="date" className="h-7 rounded-md border bg-background px-1.5" value={f} max={t} onChange={(e) => setF(e.target.value)} aria-label="From" required /></label>
      <label className="flex items-center gap-1">to <input type="date" className="h-7 rounded-md border bg-background px-1.5" value={t} min={f} onChange={(e) => setT(e.target.value)} aria-label="To" required /></label>
      <Button type="submit">Apply</Button>
      <Button type="button" variant="ghost" onClick={cancel}>Cancel</Button>
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
  const [statusFilter, setStatusFilter] = useStatusFilter();
  if (campaigns.length === 0) return null;
  const kept = campaigns.filter((c) => keepStatus(statusFilter, c.status));
  return (
    <Card title="Campaigns as each network reports them" description={`From ${from} to ${to}: what each platform says every campaign spent and got. Conversions are the platform's own claim.`}>
      <DataTable
        data={kept}
        toolbar={<StatusFilterSelect value={statusFilter} onChange={setStatusFilter} hidden={campaigns.length - kept.length} />}
        footer={metricFooter<AdCampaign>("campaigns", campaigns, (c) => c.totals, (c) => c.report_currency, { money, n })}
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
            <span className="block truncate text-[12px] text-muted-foreground">{adStatusWord(c.status)} · {money(c.totals.spend_micros, c.report_currency)} · {n(c.totals.clicks)} clicks · {n(c.totals.conversions)} conv.</span>
          </span>
        )}
        columns={[
          {
            id: "campaign", header: "Campaign", accessorFn: (c) => c.name,
            cell: ({ row: { original: c } }) => <div className="min-w-40 font-medium">{c.name}</div>,
          },
          { id: "network", header: "Network", accessorFn: (c) => `${platformName(c.connector)} · ${c.account_name}`, cell: ({ row: { original: c } }) => <span className="text-[12px] text-muted-foreground">{platformName(c.connector)} · {c.account_name}</span> },
          statusColumn<AdCampaign>(),
          ...metricColumns<AdCampaign>((c) => c.totals, (c) => c.report_currency),
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

// What a platform takes to send conversions back (a pixel ID, a
// conversions key), on the connection itself: whether it is set up, and a
// form to add or change it. A saved secret is never shown; typing a new one
// replaces it, and a field left empty keeps what it has.
function ConversionSetup({ ws, conn, canManage, done }: { ws: string; conn: AdConnection; canManage: boolean; done: () => void }) {
  const [open, setOpen] = useState(false);
  const [values, setValues] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const missing = conn.conversion_fields.filter((f) => !conn.conversion_fields_set.includes(f.name));
  const ready = missing.length === 0;

  async function save() {
    const sent = Object.fromEntries(Object.entries(values).filter(([, v]) => v.trim()));
    if (Object.keys(sent).length === 0) return setOpen(false);
    setBusy(true);
    const { error } = await api.PUT("/v1/workspaces/{ws}/ads/connections/{id}/setup", { params: { path: { ws, id: conn.id } }, body: { values: sent } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast(`Saved. ${platformName(conn.connector)} can now take conversions: add a rule under Send conversions.`);
    setValues({});
    setOpen(false);
    done();
  }

  return (
    <div className="space-y-2 border-t pt-1.5">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <span>Sending conversions</span>
          <Badge tone={ready ? "green" : "amber"}>{ready ? "Set up" : "Not set up"}</Badge>
          {!ready && <span className="text-[12px] text-muted-foreground">Needs {missing.map((f) => f.label).join(", ")}</span>}
        </div>
        {canManage && !open && <Button variant={ready ? "ghost" : "primary"} onClick={() => setOpen(true)}>{ready ? "Edit" : "Set up"}</Button>}
      </div>
      {open && (
        <form className="max-w-xl space-y-3" onSubmit={(e) => { e.preventDefault(); save(); }}>
          {conn.conversion_fields.map((f) => {
            const saved = conn.conversion_fields_set.includes(f.name);
            return (
              <Input key={f.name} label={f.label} hint={saved ? `Saved. Type a new one to replace it. ${f.help ?? ""}` : f.help} placeholder={saved ? "Saved" : undefined}
                type={f.secret ? "password" : "text"} autoComplete="off" value={values[f.name] ?? ""} onChange={(e) => setValues((v) => ({ ...v, [f.name]: e.target.value }))} />
            );
          })}
          <div className="flex gap-2">
            <Button type="submit" disabled={busy}>Save</Button>
            <Button type="button" variant="ghost" onClick={() => { setOpen(false); setValues({}); }}>Cancel</Button>
          </div>
        </form>
      )}
    </div>
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
function Levels({ scope, setScope, accounts, synced, shownIn }: { scope: AdScope; setScope: (s: AdScope) => void; accounts: AdAccount[]; synced: string | null; shownIn: string }) {
  // An account billed in another currency is converted, and says so.
  const billed = (c: string) => (c && c !== shownIn ? `billed in ${c}, shown in ${shownIn}` : c);
  const networks = [...new Set(accounts.map((a) => a.connector))];
  const inNetwork = accounts.filter((a) => a.connector === scope.network);
  const account = accounts.find((a) => a.id === scope.account);
  // With one account there is nothing to pick: the page is that account's.
  // With one network, or one account in the picked network, the choice is
  // made for the person instead of asked for.
  const only = accounts.length === 1 ? accounts[0] : null;
  const onlyInNetwork = inNetwork.length === 1 ? inNetwork[0] : null;
  if (only)
    return (
      <p className="text-[13px] text-muted-foreground">
        {platformName(only.connector)} · {only.name || accountId(only.connector, only.external_id)} · {billed(only.currency)}
        {synced && <> · synced {ago(synced)}</>}
      </p>
    );
  const pickNetwork = (c: string) => {
    if (!c) return setScope({});
    const its = accounts.filter((a) => a.connector === c);
    setScope(its.length === 1 ? { network: c, account: its[0].id } : { network: c });
  };
  const crumb = "rounded px-1.5 py-0.5 hover:bg-muted";
  return (
    <nav aria-label="Level" className="flex flex-wrap items-center gap-1 text-[13px]">
      <button type="button" className={`${crumb} ${scope.network ? "text-muted-foreground" : "font-medium"}`} onClick={() => setScope({})}>All networks</button>
      <span className="text-muted-foreground">›</span>
      <select aria-label="Network" className="rounded border bg-transparent px-1.5 py-0.5" value={scope.network ?? ""} onChange={(e) => pickNetwork(e.target.value)}>
        <option value="">Every network</option>
        {networks.map((c) => <option key={c} value={c}>{platformName(c)}</option>)}
      </select>
      {scope.network && (
        <>
          <span className="text-muted-foreground">›</span>
          {onlyInNetwork ? (
            <span>{onlyInNetwork.name || accountId(onlyInNetwork.connector, onlyInNetwork.external_id)}</span>
          ) : (
            <select aria-label="Account" className="rounded border bg-transparent px-1.5 py-0.5" value={scope.account ?? ""} onChange={(e) => setScope({ network: scope.network, account: e.target.value || undefined })}>
              <option value="">Every account</option>
              {inNetwork.map((a) => <option key={a.id} value={a.id}>{a.name || accountId(a.connector, a.external_id)}</option>)}
            </select>
          )}
        </>
      )}
      {account && <span className="text-muted-foreground">· {billed(account.currency)}</span>}
    </nav>
  );
}

// Settings' own lines: what is connected and counted, each with its status
// and the page or choice behind it.
function SetupLines({ ws, accounts, conns, shadowMode, setup, economics, changeTarget, changeCeiling, windowDays, setWindowDays, customers, setCustomers, open }: {
  ws: string; accounts: AdAccount[]; conns: AdConnection[]; shadowMode: boolean;
  setup: AdSetup | null; economics: AdEconomics | null; changeTarget?: () => void; changeCeiling?: () => void;
  windowDays: (typeof windows)[number]; setWindowDays: (w: (typeof windows)[number]) => void;
  customers: CustomerBucket; setCustomers: (c: CustomerBucket) => void; open: (screen: string) => void;
}) {
  const failing = conns.find((c) => c.last_error);
  const synced = accounts.map((a) => a.last_synced_at).filter((x): x is string => !!x).sort()[0];
  const select = "h-8 rounded-md border bg-background px-2 text-[13px]";
  // A line's status comes from the setup checks behind it: all done reads
  // Working, any not done reads Fix with what is missing.
  const check = (...ids: string[]) => {
    const steps = (setup?.steps ?? []).filter((x) => ids.includes(x.step));
    const open = steps.filter((x) => !x.done);
    if (steps.length === 0) return null;
    return open.length ? { tone: "red" as const, status: "Fix", now: open.map((x) => x.detail || x.title).join(" ") } : { tone: "green" as const, status: "Working", now: steps.map((x) => x.detail || x.title).join(" ") };
  };
  // Conversions the networks refused in the last week.
  const [failed, setFailed] = useState<number | null>(null);
  useEffect(() => {
    let live = true;
    api.GET("/v1/workspaces/{ws}/ads/conversion-sends", { params: { path: { ws }, query: { status: "failed", limit: 100 } } }).then(({ data }) => {
      if (!live || !data) return;
      const week = Date.now() - 7 * 86400000;
      setFailed(data.data.filter((x) => Date.parse(x.created_at) >= week).length);
    });
    return () => {
      live = false;
    };
  }, [ws]);
  const conversions = check("conversion_feed", "consent");
  const tracking = check("site_script", "events", "tracking_template");
  const target = economics?.target_cost_per_payer_micros ?? null;
  const signupTarget = economics?.target_cost_per_signup_micros ?? null;
  const ceiling = economics?.daily_budget_ceiling_micros ?? null;
  const lines: { id: string; tone: "green" | "grey" | "amber" | "red"; status: string; setting: string; now: ReactNode; action: ReactNode }[] = [
    {
      id: "accounts", tone: failing ? "red" : accounts.length ? "green" : "amber", status: failing ? "Fix" : accounts.length ? "Connected" : "To do", setting: "Ad accounts",
      now: failing ? `The last sync of ${platformName(failing.connector)} failed.` : accounts.length ? `${accounts.map((a) => `${platformName(a.connector)} · ${a.name || accountId(a.connector, a.external_id)}`).join(", ")}${synced ? ` · synced ${ago(synced)}` : ""}` : "No ad account belongs to this brand yet.",
      action: <Button variant="ghost" onClick={() => open("accounts")}>Manage</Button>,
    },
    {
      id: "recommendations", tone: shadowMode ? "grey" : "green", status: shadowMode ? "Off" : "On", setting: "Recommendations",
      now: shadowMode ? "Outflare reviews the account but keeps what it finds to itself. Switch them on under Actions." : "Outflare reviews the account daily and lists actions for your OK.",
      action: <Button variant="ghost" onClick={() => open("actions")}>Open Actions</Button>,
    },
    {
      id: "target", tone: target === null ? "red" : "green", status: target === null ? "Fix" : "Set", setting: "Target cost per new customer",
      now: target === null ? "No target yet, so nothing can be judged as cheap or expensive." : `${money(target, economics?.currency ?? "USD")}${signupTarget !== null ? ` · sign-up target ${money(signupTarget, economics?.currency ?? "USD")}` : ""}`,
      action: changeTarget ? <Button variant="ghost" onClick={changeTarget}>{target === null ? "Set a target" : "Change"}</Button> : null,
    },
    {
      id: "limits", tone: ceiling === null ? "amber" : "green", status: ceiling === null ? "To do" : "Set", setting: "Limits",
      now: `${ceiling === null ? "No ceiling on your total daily budget yet" : `Total daily budget never above ${money(ceiling, economics?.currency ?? "USD")} without asking you twice`} · a suggested budget moves a fifth at a time · no new suggestion on a campaign changed in the last 7 days`,
      action: changeCeiling ? <Button variant="ghost" onClick={changeCeiling}>{ceiling === null ? "Set a ceiling" : "Change"}</Button> : null,
    },
    {
      id: "conversions", tone: conversions?.tone ?? "grey", status: conversions?.status ?? "Open", setting: "Conversions sent to the ad networks",
      now: conversions?.now ?? "Which sign-ups and payments go back to each network, and who they are sent for.", action: <Button variant="ghost" onClick={() => open("conversions")}>Open</Button>,
    },
    {
      id: "sends", tone: failed === null ? "grey" : failed > 0 ? "red" : "green", status: failed === null ? "Open" : failed > 0 ? "Fix" : "Working", setting: "Send log",
      now: failed === null ? "Every conversion a rule matched: sent, held back or failed, and why." : failed > 0 ? `${failed >= 100 ? "100 or more" : n(failed)} ${failed === 1 ? "conversion was" : "conversions were"} refused in the last 7 days. Open the log to see why.` : "No conversion was refused in the last 7 days.",
      action: <Button variant="ghost" onClick={() => open("sends")}>Open</Button>,
    },
    {
      id: "tracking", tone: tracking?.tone ?? "grey", status: tracking?.status ?? "Open", setting: "Tracking",
      now: tracking?.now ?? "Whether ad clicks arrive with a click id, and the tracking template on each campaign.", action: <Button variant="ghost" onClick={() => open("tracking")}>Open</Button>,
    },
    {
      id: "window", tone: "green", status: "Set", setting: "Counting window", now: `A sign-up or first payment counts for an ad click for ${windowDays} days.`,
      action: (
        <select className={select} aria-label="Counting window" value={windowDays} onChange={(e) => setWindowDays(Number(e.target.value) as (typeof windows)[number])}>
          {windows.map((w) => <option key={w} value={w}>{w} days</option>)}
        </select>
      ),
    },
    {
      id: "customers", tone: "green", status: "Set", setting: "Revenue counted", now: "New: a person's first ever payment. Returning: any later payment.",
      action: (
        <select className={select} aria-label="Revenue counted" value={customers} onChange={(e) => setCustomers(e.target.value as CustomerBucket)}>
          {customerBuckets.map((c) => <option key={c.id} value={c.id}>{c.label} customers</option>)}
        </select>
      ),
    },
    { id: "changes", tone: "grey", status: "Open", setting: "Change history", now: "Every pause, budget, keyword and bidding change made from here, and whether the network shows it.", action: <Button variant="ghost" onClick={() => open("changes")}>Open</Button> },
  ];
  // Problems first, then the rest in their own order.
  const rank = { red: 0, amber: 1, green: 2, grey: 2 } as const;
  const ordered = lines.map((l, i) => ({ l, i })).sort((a, b) => rank[a.l.tone] - rank[b.l.tone] || a.i - b.i).map((x) => x.l);
  return (
    <Card title="Settings" description="Everything that has to be right for the numbers and recommendations to be trusted. Problems are listed first.">
      <DataTable
        data={ordered}
        rowId={(l) => l.id}
        columnsMenu={false}
        empty=""
        columns={[
          { id: "status", header: "Status", enableSorting: false, accessorFn: (l) => l.status, cell: ({ row: { original: l } }) => <Status tone={l.tone}>{l.status}</Status> },
          { id: "setting", header: "Setting", enableSorting: false, accessorFn: (l) => l.setting, cell: ({ row: { original: l } }) => <span className="font-medium">{l.setting}</span> },
          { id: "now", header: "Now", enableSorting: false, cell: ({ row: { original: l } }) => <div className="max-w-xl whitespace-normal text-muted-foreground">{l.now}</div> },
          { id: "action", header: "", enableSorting: false, meta: { align: "right" }, cell: ({ row: { original: l } }) => l.action },
        ]}
      />
    </Card>
  );
}

const changeWord: Record<AdChange["kind"], string> = {
  pause: "Paused", resume: "Resumed", budget: "Daily budget", keyword_add: "Keyword added", negative_add: "Negative keyword added", keyword_split: "Keyword given its own ad group",
  bidding: "Bidding", audience_add: "Audience added", audience_remove: "Audience taken out", country_add: "Country added", country_remove: "Country taken out",
  keyword_pause: "Keyword paused", keyword_resume: "Keyword resumed", ad_pause: "Ad paused", ad_resume: "Ad resumed", negative_remove: "Negative keyword taken out",
  target: "Target moved", exclusion_add: "Exclusion added", exclusion_remove: "Exclusion taken out",
  ad_group_pause: "Ad group paused", ad_group_resume: "Ad group resumed", ad_group_budget: "Ad group budget",
  locations: "Places set", rename: "Renamed",
  campaign_remove: "Campaign removed", ad_remove: "Ad removed", keyword_remove: "Keyword removed", bid: "Bid",
};

// changeDetail is what a change names: a keyword, an audience, a country or a bidding target.
function changeDetail(r: AdChange) {
  const a = r.after;
  // A bid by hand on a keyword or an ad group names it before the figures.
  const unit = a.bid && (a.keyword_id || a.ad_group_id) ? (a.keyword ? <span className="font-mono">[{a.keyword}] </span> : <span>{a.name || a.ad_group_id}: </span>) : null;
  if (a.keyword && !a.bid) return <span className="font-mono">[{a.keyword}]</span>;
  if (r.kind === "ad_remove") return <span>{a.name || a.ad_id}</span>;
  // A change to one ad group or ad names it; a budget shows both figures.
  if (r.kind === "ad_group_pause" || r.kind === "ad_group_resume") return <span>{a.name || a.ad_group_id}</span>;
  if (r.kind === "ad_pause" || r.kind === "ad_resume") return <span>{a.name || a.ad_id}</span>;
  if (r.kind === "ad_group_budget") {
    const fig = (v?: number | null) => (v != null ? (v / 1e6).toFixed(2) : "");
    return <span>{a.name || a.ad_group_id}: {r.before.daily_budget_micros != null ? `${fig(r.before.daily_budget_micros)} → ` : ""}{fig(a.daily_budget_micros)}</span>;
  }
  if (r.kind === "rename") return <span>{r.before.name ? `${r.before.name} → ` : ""}{a.name}</span>;
  if (a.locations) {
    // Each place added or taken out, by name, so the row says what moved.
    const l = a.locations;
    const names = (list?: { name: string; geo_target_constant: string }[]) => (list ?? []).map((x) => x.name || x.geo_target_constant).join(", ");
    const parts = [
      l.add_included?.length ? `shows in ${names(l.add_included)}` : "",
      l.remove_included?.length ? `no longer shows in ${names(l.remove_included)}` : "",
      l.add_excluded?.length ? `kept out of ${names(l.add_excluded)}` : "",
      l.remove_excluded?.length ? `no longer kept out of ${names(l.remove_excluded)}` : "",
    ].filter(Boolean);
    return <span className="whitespace-normal">{l.target_all_locations && l.remove_included?.length ? "Everywhere it isn't kept out of. " : ""}{parts.join("; ")}</span>;
  }
  if (a.country) return <span>{a.country}</span>;
  if (a.exclusion) return <span className="font-mono">{a.exclusion.label || a.exclusion.value}</span>;
  if (a.bid) {
    // What it was before (once the job has read it), then what was asked.
    const was = r.before.bid ? (r.before.bid.auto ? "auto" : (r.before.bid.micros / 1e6).toString()) : "";
    return <span>{unit}{a.bid.placement ? <span className="font-mono">{a.bid.placement}: </span> : null}{was ? `${was} → ` : ""}{(a.bid.micros / 1e6).toString()}{a.bid.only_lower ? " (only lower)" : ""}</span>;
  }
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
    <Card title="Changes" description="Every pause, resume, budget, keyword, bidding, audience and country change asked for here, on a campaign, an ad group or an ad, newest first, and whether the platform shows it.">
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
