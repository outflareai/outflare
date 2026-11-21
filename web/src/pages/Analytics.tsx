import { PickBrand } from "@/components/pick-brand";
import { pickedBrandId } from "@/lib/brand";
import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import type { AnalyticsMetrics, AnalyticsOverviewSite, AnalyticsPoint, AnalyticsReport, AnalyticsReportName, AnalyticsSitePlan, AnalyticsSource, AnalyticsSummaryItem, AnalyticsSync, SEOSource, SiteAnalyticsReport } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, CopyButton, Empty, ErrorText, field, Loading, PageHeader, Stat, Stats, toast } from "../ui";
import { ago } from "../format";
import { Icon } from "../icons";
import { DataTable } from "@/components/data-table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import type { Tab } from "../App";
import { SEOStrip } from "./SEO";
import { ConnectorLogo } from "@/components/connector-logo";
import { SiteSetup, SiteSetupDialog } from "@/components/analytics-sites";
import { useSiteTracking } from "./SiteKeys";

type Metric = keyof AnalyticsMetrics;

// Each metric in display order, with how to show it.
const metrics: { key: Metric; label: string; format: (v: number) => string; count?: boolean }[] = [
  { key: "visitors", label: "Visitors", format: (v) => whole(v), count: true },
  { key: "pageviews", label: "Page views", format: (v) => whole(v), count: true },
  { key: "sessions", label: "Visits", format: (v) => whole(v), count: true },
  { key: "bounce_rate", label: "Bounce rate", format: (v) => percent(v) },
  { key: "avg_duration", label: "Avg. visit", format: (v) => duration(v) },
  { key: "clicks", label: "Search clicks", format: (v) => whole(v), count: true },
  { key: "impressions", label: "Impressions", format: (v) => whole(v), count: true },
  { key: "ctr", label: "Click-through", format: (v) => percent(v) },
  { key: "position", label: "Avg. position", format: (v) => v.toFixed(1) },
  { key: "events", label: "Events", format: (v) => whole(v), count: true },
  { key: "conversions", label: "Conversions", format: (v) => whole(v), count: true },
];

const breakdowns: { id: AnalyticsReportName; label: string; key: string }[] = [
  { id: "pages", label: "Pages", key: "Page" },
  { id: "sources", label: "Sources", key: "Source" },
  { id: "search_queries", label: "Search queries", key: "Query" },
  { id: "events", label: "Events", key: "Event" },
];

const periods = [
  { days: 7, label: "Last 7 days" },
  { days: 28, label: "Last 28 days" },
  { days: 90, label: "Last 90 days" },
  { days: 365, label: "Last 12 months" },
];

function whole(v: number) {
  return Math.round(v).toLocaleString();
}
function percent(v: number) {
  return `${(v * 100).toFixed(v >= 0.1 ? 0 : 1)}%`;
}
function duration(s: number) {
  const m = Math.floor(s / 60);
  return m > 0 ? `${m}m ${Math.round(s % 60)}s` : `${Math.round(s)}s`;
}
function show(m: AnalyticsMetrics | null | undefined, k: Metric) {
  const v = m?.[k];
  return v === undefined || v === null ? "–" : metrics.find((x) => x.key === k)!.format(v);
}
function day(d: string) {
  return new Date(d + "T00:00:00Z").toLocaleDateString(undefined, { month: "short", day: "numeric", timeZone: "UTC" });
}
function range(days: number) {
  const to = new Date();
  const from = new Date(to.getTime() - (days - 1) * 86400000);
  const iso = (d: Date) => d.toISOString().slice(0, 10);
  return { from: iso(from), to: iso(to) };
}

// Stored history ends yesterday: today is still being counted.
function storedRange(days: number) {
  const to = new Date(Date.now() - 86400000);
  const from = new Date(to.getTime() - (days - 1) * 86400000);
  const iso = (d: Date) => d.toISOString().slice(0, 10);
  return { from: iso(from), to: iso(to) };
}

const remember = (key: string, value?: string) => {
  try {
    if (value === undefined) return localStorage.getItem(key) ?? "";
    localStorage.setItem(key, value);
  } catch {
    // Storage can be refused; the choice just isn't remembered.
  }
  return "";
};

// Visitors, pages, sources, searches and conversions from the analytics
// tools a workspace already uses. The sync stores each tool's daily numbers
// so websites can be compared across tools; nothing changes at the provider.
// Analytics belong to one brand (plans/brands/README.md): with all brands
// picked the page asks for one, and switching brands opens it afresh.
export function Analytics(props: { ws: string; canManage: boolean; go: (t: Tab, item?: string) => void; item?: string }) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="Analytics" brands={active.length} />;
  return <AnalyticsPage key={brand.id} {...props} />;
}

// The page's tabs, in order. Each shows one thing from one source, named
// at its top, so nobody has to guess where a number comes from.
type View = "overview" | OwnBreakdown | "search" | "tools";
const views: { id: View; label: string }[] = [
  { id: "overview", label: "Overview" },
  { id: "pages", label: "Pages" },
  { id: "sources", label: "Sources" },
  { id: "websites", label: "Websites" },
  { id: "countries", label: "Countries" },
  { id: "devices", label: "Devices" },
  { id: "search", label: "Search" },
  { id: "tools", label: "Connected tools" },
];

// A search engine's tool can narrow its reports to one page or one search
// query, so each row of its Pages and Search queries tables opens a page
// of its own: /analytics/search/page/{url} or /analytics/search/query/{q}.
// The value is one path segment, so a page's slashes and trailing slash
// survive the trip through the URL.
type Drill = { kind: "page" | "query"; value: string };
const narrows = (connector: string) => connector === "google_search_console" || connector === "bing_webmaster";
const drillItem = (d: Drill) => `search/${d.kind}/${encodeURIComponent(d.value)}`;
function parseDrill(item?: string): Drill | null {
  const m = item?.match(/^search\/(page|query)\/(.+)$/);
  if (!m) return null;
  try {
    return { kind: m[1] as Drill["kind"], value: decodeURIComponent(m[2]) };
  } catch {
    return null;
  }
}

// The open page or search, for the browser tab's title.
export const searchDrillName = (item?: string) => parseDrill(item)?.value;

// Where a tab's numbers come from.
function DataSource({ logo, name, children }: { logo?: string; name: string; children: ReactNode }) {
  return (
    <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[13px] text-muted-foreground" data-testid="data-source">
      <span className="font-mono text-[11px] tracking-wide uppercase">Source</span>
      <span className="inline-flex items-center gap-1.5 font-medium text-foreground">{logo && <ConnectorLogo id={logo} size="sm" />}{name}</span>
      <span>· {children}</span>
    </p>
  );
}

function AnalyticsPage({ ws, canManage, go, item }: { ws: string; canManage: boolean; go: (t: Tab, item?: string) => void; item?: string }) {
  const [sources, setSources] = useState<AnalyticsSource[] | null>(null);
  const [seo, setSEO] = useState<SEOSource[] | null>(null);
  const [error, setError] = useState("");
  const [selected, setSelected] = useState(() => remember(`analytics:${ws}`));
  // The tool whose reports are open on Connected tools; none shows them all.
  const [tool, setTool] = useState("");
  const [days, setDays] = useState(28);
  const tracking = useSiteTracking(ws);
  const drill = parseDrill(item);
  const view: View = drill ? "search" : views.find((v) => v.id === item)?.id ?? "overview";
  // The Search tab's site and table, kept while a page or query is open so
  // going back finds them as they were.
  const [searchSite, setSearchSite] = useState("");
  const [searchTab, setSearchTab] = useState<AnalyticsReportName | undefined>();
  const own = view !== "search" && view !== "tools";

  // Outflare's own numbers, read once for every tab that shows them.
  // website narrows everything to one host; sites are the hosts seen with
  // no filter, so the picker keeps every choice while one is picked.
  const [data, setData] = useState<SiteAnalyticsReport | null>(null);
  const [ownError, setOwnError] = useState("");
  const [website, setWebsite] = useState("");
  const [sites, setSites] = useState<string[]>([]);
  useEffect(() => {
    if (!tracking) return;
    setData(null);
    api.GET("/v1/brands/{brand}/analytics/tracking", { params: { path: { brand: pickedBrandId() }, query: { ...range(days), ...(website ? { website } : {}) } } }).then(({ data, error }) => {
      if (error || !data) return setOwnError(errorMessage(error));
      setOwnError("");
      setData(data);
      if (!website) setSites(data.websites.map((w) => w.key).filter((k) => k !== "(none)"));
    });
  }, [days, website, tracking]);

  const load = useCallback(async () => {
    const seoReq = api.GET("/v1/brands/{brand}/seo", { params: { path: { brand: pickedBrandId() } } });
    const { data, error } = await api.GET("/v1/brands/{brand}/analytics", { params: { path: { brand: pickedBrandId() } } });
    if (error || !data) return setError(errorMessage(error));
    setSources(data.data);
    setSEO((await seoReq).data?.data ?? []);
  }, [ws]);

  useEffect(() => {
    load();
  }, [load]);

  // Search engines' own tools (Search Console, Bing) have a tab to
  // themselves; the rest are on Connected tools.
  const searchSources = (sources ?? []).filter((s) => s.reports.includes("search_queries"));
  const search = searchSources.find((s) => s.id === selected) ?? searchSources[0];
  const opened = sources?.find((s) => s.id === tool);
  const pick = (id: string) => {
    setSelected(id);
    remember(`analytics:${ws}`, id);
  };
  const connect = canManage ? <Button onClick={() => go("connectors", "analytics")}>Choose a tool</Button> : <p className="text-[13px] text-muted-foreground">Ask a workspace admin to connect one.</p>;

  return (
    <div className="space-y-5">
      <PageHeader
        title="Website analytics"
        description="Who visits your website, what they read and where they come from. Each tab shows one thing, and says at the top where its numbers come from."
        action={canManage ? <Button variant="ghost" onClick={() => go("connectors", "analytics")}><Icon name="plus" className="size-3.5" /> Connect a tool</Button> : undefined}
      />
      <div className="-mt-2 flex flex-wrap items-center gap-x-3 gap-y-2 border-b">
        <Tabs value={view} onValueChange={(v) => go("analytics", v === "overview" ? undefined : v)} className="min-w-0">
          <TabsList variant="line" className="max-w-full justify-start overflow-x-auto">
            {views.map((v) => <TabsTrigger key={v.id} value={v.id}>{v.label}</TabsTrigger>)}
          </TabsList>
        </Tabs>
        <span className="flex-1" />
        <div className="flex flex-wrap items-center gap-2 pb-1.5">
          {own && (sites.length > 1 || website) && (
            <select aria-label="Website" className={field} value={website} onChange={(e) => setWebsite(e.target.value)}>
              <option value="">All websites</option>
              {sites.map((h) => <option key={h} value={h}>{h}</option>)}
            </select>
          )}
          <select aria-label="Period" className={field} value={days} onChange={(e) => setDays(Number(e.target.value))}>
            {periods.map((p) => <option key={p.days} value={p.days}>{p.label}</option>)}
          </select>
        </div>
      </div>
      <ErrorText>{error}</ErrorText>

      {own && (
        <section className="space-y-4" aria-label={views.find((v) => v.id === view)!.label}>
          <DataSource name="Outflare Analytics">
            every visit counted by the Outflare snippet on your website, live.{" "}
            <button className="underline underline-offset-2 hover:text-foreground" onClick={() => go("visitors")}>See who's visiting</button>
          </DataSource>
          {tracking === false && (
            <Card
              title="Add Outflare's snippet to your website"
              description="One script tag in your site's <head> counts every visit with where it came from, so sign-ups and revenue are credited to the channel that brought them. It works alongside any other analytics tool."
            >
              <Button onClick={() => go("tracking")}>Get the snippet</Button>
            </Card>
          )}
          <ErrorText>{ownError}</ErrorText>
          {tracking && !data && !ownError && <Loading />}
          {tracking && data && view === "overview" && (
            <Traffic report={{ ...data, report: "traffic", site: "", integration_id: "", connector: "outflare", cached: false, fetched_at: "", rows: undefined }} />
          )}
          {tracking && data && view !== "overview" && <OwnTable data={data} tab={view as OwnBreakdown} setWebsite={setWebsite} />}
        </section>
      )}

      {view === "search" && sources && search && drill && narrows(search.connector) && (
        <SearchDrill key={drill.kind + drill.value + days + search.id} ws={ws} source={search} site={searchSite} days={days} drill={drill} go={go} />
      )}
      {view === "search" && sources && !(search && drill && narrows(search.connector)) && (search ? (
        <SourceView key={search.id + days} ws={ws} source={search} sources={searchSources} days={days} onPick={pick} canManage={canManage} onChanged={load} go={go}
          initialSite={searchSite} onSite={setSearchSite} initialTab={searchTab} onTab={setSearchTab}
          about="what people searched for before they clicked through to your website. Search engines report it two to three days late." />
      ) : (
        <Empty title="Connect Google Search Console to see your searches" action={connect}>
          Search Console and Bing Webmaster Tools say which searches show your website, how often people click and where you rank. They connect with a sign-in to your own account.
        </Empty>
      ))}
      {view === "search" && !sources && !error && <Loading />}

      {view === "tools" && (!sources || !seo) && !error && <Loading />}
      {view === "tools" && sources && seo && sources.length + seo.length === 0 && (
        <Empty title="Connect the analytics you already use" action={connect}>
          Google Analytics, Search Console, Bing Webmaster Tools, PostHog, Plausible, Fathom, Matomo, Umami, Mixpanel, Amplitude, Cloudflare, Simple Analytics, Clarity or Vercel. Google tools connect with a sign-in; the rest take an API key. For rankings and backlinks, add DataForSEO or Ahrefs from the SEO connectors. Nothing else in Outflare needs this.
        </Empty>
      )}
      {view === "tools" && sources && seo && sources.length + seo.length > 0 && !opened && (
        <section className="space-y-4" aria-label="Connected tools">
          <DataSource name="Your connected tools">
            each tool's own totals for the same website, side by side, copied into Outflare every six hours. Pick a tool to open its reports.
          </DataSource>
          <Websites ws={ws} days={days} sources={sources} seo={seo} onPick={setTool} canManage={canManage} />
          {seo.length > 0 && (
            <p className="text-[13px] text-muted-foreground">
              SEO tools, their budgets and every keyword are on the <button className="text-foreground underline underline-offset-2 hover:text-brand-strong dark:hover:text-brand" onClick={() => go("seo")}>SEO page</button>.
            </p>
          )}
        </section>
      )}
      {view === "tools" && sources && opened && (
        <section className="space-y-4" aria-label={opened.name}>
          <button onClick={() => setTool("")} className="font-mono text-[12px] text-muted-foreground hover:text-foreground">← All connected tools</button>
          <SourceView key={opened.id + days} ws={ws} source={opened} sources={[opened]} days={days} onPick={setTool} canManage={canManage} onChanged={load} go={go}
            about="this tool's own numbers, asked for live." />
        </section>
      )}
    </div>
  );
}

type OwnRow = Row & { website?: string };

// One breakdown of Outflare's own numbers: the tab's only table.
function OwnTable({ data, tab, setWebsite }: { data: SiteAnalyticsReport; tab: OwnBreakdown; setWebsite: (h: string) => void }) {
  const rows: OwnRow[] = data[tab] ?? [];
  const shown = ownBreakdowns.find((b) => b.id === tab)!;
  return (
    <DataTable
      data={rows}
      rowId={(r: OwnRow) => `${r.website ?? ""}${r.key}`}
      columnsMenu={false}
      searchPlaceholder={rows.length > 10 ? `Filter ${shown.label.toLowerCase()}…` : undefined}
      empty={`No ${shown.label.toLowerCase()} in this period.`}
      columns={[
        ...(tab === "pages"
          ? [{ id: "website", header: "Website", accessorFn: (r: OwnRow) => r.website ?? "", cell: ({ row: { original: r } }: { row: { original: OwnRow } }) => (
              <button type="button" className="text-muted-foreground hover:text-foreground hover:underline" title={`Only ${r.website}`} onClick={() => r.website && r.website !== "(none)" && setWebsite(r.website)}>{r.website === "(none)" ? "Unknown" : r.website}</button>
            ) }]
          : []),
        { id: "key", header: shown.key, accessorFn: (r: OwnRow) => r.key, cell: ({ row: { original: r } }: { row: { original: OwnRow } }) => <span className="block max-w-[28rem] truncate font-medium" title={r.key}>{shown.show ? shown.show(r.key) : r.key}</span> },
        ...metrics.filter((m) => rows.some((r) => r[m.key] !== undefined && r[m.key] !== null)).map((m) => ({
          id: m.key,
          header: m.label,
          accessorFn: (r: OwnRow) => r[m.key] ?? -1,
          meta: { align: "right" as const, mono: true },
          cell: ({ row: { original: r } }: { row: { original: OwnRow } }) => show(r, m.key),
        })),
      ]}
    />
  );
}

type OwnBreakdown = "pages" | "sources" | "websites" | "countries" | "devices";
const regionNames = (() => {
  try {
    return new Intl.DisplayNames(["en"], { type: "region" });
  } catch {
    return null;
  }
})();
const ownBreakdowns: { id: OwnBreakdown; label: string; key: string; show?: (k: string) => string }[] = [
  { id: "pages", label: "Pages", key: "Page" },
  { id: "sources", label: "Sources", key: "Source", show: (k) => (k === "(direct)" ? "Direct" : k.replaceAll("_", " ")) },
  { id: "websites", label: "Websites", key: "Website" },
  { id: "countries", label: "Countries", key: "Country", show: (k) => (k === "(none)" ? "Unknown" : regionNames?.of(k) ?? k) },
  { id: "devices", label: "Devices", key: "Device", show: (k) => (k === "(none)" ? "Unknown" : k[0].toUpperCase() + k.slice(1)) },
];

const syncedAgo = (s?: AnalyticsSync) => (s?.last_ok_at ? `Synced ${ago(s.last_ok_at)}` : s?.last_run_at ? "Sync failed" : "Not synced yet");

// Every website, lined up by domain across tools, from stored history: each
// tool's totals side by side and a day-by-day comparison. Before the first
// sync finishes it falls back to asking the tools live.
function Websites({ ws, days, sources, seo, selected, onPick, canManage }: { ws: string; days: number; sources: AnalyticsSource[]; seo: SEOSource[]; selected?: string; onPick: (id: string) => void; canManage: boolean }) {
  const [data, setData] = useState<{ sites: AnalyticsOverviewSite[]; syncs: AnalyticsSync[]; notes: string[] } | null>(null);
  const [error, setError] = useState("");
  const [syncing, setSyncing] = useState(false);
  const [reload, setReload] = useState(0);
  const [open, setOpen] = useState<string | null>(null);
  // Narrowed to the brand picked in the sidebar.
  const { brand: picked, active: brands } = useBrand(ws);
  const brand = picked?.handle;
  // Sites with no brand yet (or no website, in a workspace without brands),
  // by connection: the set-up still to finish.
  const [unmapped, setUnmapped] = useState<Record<string, number>>({});
  const [settingUp, setSettingUp] = useState<AnalyticsSource | null>(null);
  // With one brand picked: whether any site counts for it, and the
  // connections it uses, to pick its sites from without signing in again.
  const [brandSites, setBrandSites] = useState<number | null>(null);
  const [usable, setUsable] = useState<string[]>([]);
  const [focused, setFocused] = useState(false);
  useEffect(() => {
    if (!canManage) return;
    // Every brand's sites, to count the ones still to place.
    api.GET("/v1/workspaces/{ws}/analytics/site-brands", { params: { path: { ws }, query: { brand: "all" } } }).then(({ data }) => {
      const out: Record<string, number> = {};
      for (const b of data?.data ?? []) if (brands.length ? (b.source === "default" || b.source === "none") : !b.host) out[b.integration_id] = (out[b.integration_id] ?? 0) + 1;
      setUnmapped(out);
      setBrandSites(picked ? (data?.data ?? []).filter((b) => b.brand_id === picked.id).length : null);
    });
  }, [ws, sources, reload, canManage, brands.length, picked?.id]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!picked || !canManage) return setUsable([]);
    api.GET("/v1/workspaces/{ws}/integrations", { params: { path: { ws }, query: { brand: picked.handle, category: "analytics" } } }).then(({ data }) => setUsable((data?.data ?? []).map((i) => i.id)));
  }, [ws, picked?.handle, canManage, sources]); // eslint-disable-line react-hooks/exhaustive-deps
  const setup = (id: string) => setSettingUp(sources.find((s) => s.id === id) ?? null);
  const pickFrom = picked && brandSites === 0 ? sources.filter((s) => usable.includes(s.id)) : [];
  const pick = pickFrom.length > 0 && picked && (
    <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border bg-card px-4 py-3 shadow-xs">
      <div className="min-w-0 text-[13px]">
        <div className="font-medium">{picked.name} has no website analytics yet</div>
        <div className="text-muted-foreground">Pick its sites from the {pickFrom.length === 1 ? "connection" : "connections"} you already have. No need to sign in again.</div>
      </div>
      <div className="flex flex-wrap gap-2">
        {pickFrom.map((s) => <Button key={s.id} onClick={() => { setFocused(true); setup(s.id); }}>{pickFrom.length > 1 ? `Pick from ${s.name}` : `Pick ${picked.name}'s sites`}</Button>)}
      </div>
    </div>
  );
  const todo = sources.filter((s) => unmapped[s.id]);
  const finish = todo.length > 0 && (
    <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-brand-strong/40 bg-brand-strong/5 px-4 py-3 dark:border-brand/40 dark:bg-brand/5">
      <div className="min-w-0 text-[13px]">
        <div className="font-medium">Finish setting up analytics</div>
        <div className="text-muted-foreground">
          {todo.map((s) => `${unmapped[s.id] === 1 ? "1 site" : `${unmapped[s.id]} sites`} in ${s.name}`).join(", ")} {Object.values(unmapped).reduce((a, b) => a + b, 0) === 1 ? "doesn't" : "don't"} have a {brands.length ? "brand" : "website"} yet, so {Object.values(unmapped).reduce((a, b) => a + b, 0) === 1 ? "it isn't" : "they aren't"} counted for one.
        </div>
      </div>
      <div className="flex flex-wrap gap-2">
        {todo.map((s) => <Button key={s.id} onClick={() => setup(s.id)}>{todo.length > 1 ? `Choose brands: ${s.connector_name}` : "Choose brands"}</Button>)}
      </div>
    </div>
  );
  const dialog = settingUp && <SiteSetupDialog ws={ws} source={settingUp} from={focused ? "brand" : "analytics"} focus={focused ? picked : undefined} onClose={(saved) => { setSettingUp(null); setFocused(false); if (saved) setReload((n) => n + 1); }} />;
  useEffect(() => {
    api.GET("/v1/brands/{brand}/analytics/overview", { params: { path: { brand: pickedBrandId() }, query: { ...storedRange(days), ...(brand ? { brand } : {}) } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setError("");
      setData({ sites: data.data, syncs: data.syncs, notes: data.notes });
    });
  }, [ws, days, sources, seo, reload, brand]);

  async function syncAll() {
    setSyncing(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/analytics/sync", { params: { path: { ws } } });
    setSyncing(false);
    if (error) return toast(errorMessage(error), "error");
    toast("Syncing every tool. New numbers appear in a minute or two.");
    setTimeout(() => setReload((n) => n + 1), 60000);
  }

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!data) return null;
  const stored = data.sites.some((s) => s.tools.some((t) => t.first_day) || s.seo.length > 0);
  const failed = data.syncs.filter((s) => s.last_error && !s.paused);
  const action = canManage && sources.length > 0 && (
    <Button variant="ghost" onClick={syncAll} disabled={syncing} title="Copy every tool's latest numbers into Outflare now">
      <Icon name="refresh" className="size-3.5" /> {syncing ? "Syncing…" : "Sync now"}
    </Button>
  );
  if (!stored) {
    if (sources.length === 0) return null;
    return (
      <div className="space-y-3">
        {finish}
        {pick}
        {dialog}
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border bg-card px-4 py-3 text-[13px] shadow-xs">
          <span>
            <span className="font-medium">Storing your history.</span>{" "}
            <span className="text-muted-foreground">The first sync copies 90 days of traffic from each tool, usually within a few minutes of connecting. Until then the numbers below come live from each tool.</span>
          </span>
          {action}
        </div>
        {sources.length > 1 && <Summary ws={ws} days={days} sources={sources} selected={selected} onPick={onPick} />}
      </div>
    );
  }
  return (
    <section aria-label="Websites" className="space-y-3">
      {finish}
      {pick}
      {dialog}
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-[13px] font-medium">Websites <span className="font-normal text-muted-foreground">· stored history, {periods.find((p) => p.days === days)?.label.toLowerCase()} to yesterday</span></h2>
        {action}
      </div>
      {failed.length > 0 && (
        <ul className="space-y-1 rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-[13px] text-amber-800 dark:text-amber-200">
          {failed.map((s) => <li key={s.integration_id + s.site}>{s.name} didn't sync{s.site ? ` ${s.site}` : ""}: {s.last_error}</li>)}
        </ul>
      )}
      {data.sites.map((site) => (
        <WebsiteCard key={site.host || "-"} ws={ws} site={site} syncs={data.syncs} days={days} selected={selected} onPick={onPick} canManage={canManage}
          open={open === site.host} onToggle={() => setOpen(open === site.host ? null : site.host)} onSetup={setup} />
      ))}
      {data.notes.length > 0 && (
        <ul className="space-y-1 text-[12px] text-muted-foreground">
          {data.notes.filter((n) => !n.includes("PUT ")).map((n) => <li key={n}>{n}</li>)}
        </ul>
      )}
    </section>
  );
}

function WebsiteCard({ ws, site, syncs, days, selected, onPick, canManage, open, onToggle, onSetup }: {
  ws: string; site: AnalyticsOverviewSite; syncs: AnalyticsSync[]; days: number; selected?: string; onPick: (id: string) => void; canManage: boolean; open: boolean; onToggle: () => void; onSetup: (integration: string) => void;
}) {
  const cols = (["visitors", "pageviews", "sessions", "clicks", "impressions"] as Metric[]).filter((k) => site.tools.some((t) => t.totals?.[k] !== undefined && t.totals?.[k] !== null));
  const searchTools = site.tools.filter((t) => !t.copy_of && t.totals?.clicks !== undefined && t.totals?.clicks !== null).length;
  const comparable = site.host !== "" && site.tools.filter((t) => t.first_day).length > 1;
  return (
    <div className="overflow-hidden rounded-lg border bg-card shadow-xs">
      <div className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-2.5">
        <div className="min-w-0">
          <div className="truncate font-medium">{site.host || "No domain yet"}</div>
          {!site.host && <div className="text-[12px] text-muted-foreground">Choose each site's brand so it lines up with the brand's other tools.</div>}
        </div>
        {comparable && <Button variant="ghost" onClick={onToggle} aria-expanded={open}>{open ? "Hide comparison" : "Compare tools day by day"}</Button>}
      </div>
      {site.tools.length > 0 && <div className="overflow-x-auto">
        <table className="w-full text-[13px]">
          <thead>
            <tr className="border-b text-left font-mono text-[11px] tracking-wide text-muted-foreground uppercase">
              <th className="px-4 py-2 font-medium">Tool</th>
              {cols.map((k) => <th key={k} className="px-4 py-2 text-right font-medium">{metrics.find((m) => m.key === k)!.label}</th>)}
              <th className="px-4 py-2 text-right font-medium">Stored</th>
            </tr>
          </thead>
          <tbody>
            {site.tools.map((t) => {
              const sync = syncs.find((s) => s.integration_id === t.integration_id && s.site === t.site);
              return (
                <tr key={t.integration_id + t.site} className={`border-b last:border-0 ${t.integration_id === selected ? "bg-muted/40" : ""} ${t.copy_of ? "text-muted-foreground" : ""}`}>
                  <td className="px-4 py-2">
                    <button className="flex items-center gap-2 text-left font-medium hover:underline" onClick={() => onPick(t.integration_id)} title="Open this tool's reports below"><ConnectorLogo id={t.connector} size="sm" />{t.name}</button>
                    {t.site_name && <div className="truncate pl-6 text-[12px] text-muted-foreground">{t.site_name}</div>}
                    {t.copy_of && (
                      <div className="pl-6 text-[12px] text-amber-700 dark:text-amber-300" title="Two connections read the same site, so it is counted once: from the one whose sync worked last.">
                        Same site as {t.copy_of.name}, so it isn't counted twice.
                      </div>
                    )}
                    {!site.host && canManage && <button className="mt-0.5 pl-6 text-[12px] text-brand-strong underline underline-offset-2 dark:text-brand" onClick={() => onSetup(t.integration_id)}>Choose its brand</button>}
                  </td>
                  {cols.map((k) => <td key={k} className="px-4 py-2 text-right font-mono tabular-nums">{show(t.totals, k)}</td>)}
                  <td className="px-4 py-2 text-right text-[12px] whitespace-nowrap text-muted-foreground" title={t.first_day ? `${day(t.first_day)} to ${day(t.last_day!)}` : undefined}>{syncedAgo(sync)}</td>
                </tr>
              );
            })}
            {searchTools > 1 && (
              <tr className="border-t bg-muted/30">
                <td className="px-4 py-2 font-medium">All search engines</td>
                {cols.map((k) => <td key={k} className="px-4 py-2 text-right font-mono tabular-nums">{k === "clicks" || k === "impressions" ? show(site.search, k) : ""}</td>)}
                <td />
              </tr>
            )}
          </tbody>
        </table>
      </div>}
      {open && <Compare ws={ws} host={site.host} days={days} />}
      {site.host && <SEOStrip ws={ws} host={site.host} seo={site.seo} />}
    </div>
  );
}

type Series = { integration_id: string; name: string; series: AnalyticsPoint[] };

// Each tool's daily numbers for one website on one chart. Tools count
// differently, so the lines rarely match; the gap is the point.
function Compare({ ws, host, days }: { ws: string; host: string; days: number }) {
  const [data, setData] = useState<Series[] | null>(null);
  const [error, setError] = useState("");
  const [metric, setMetric] = useState<Metric>("visitors");
  useEffect(() => {
    api.GET("/v1/brands/{brand}/analytics/compare", { params: { path: { brand: pickedBrandId() }, query: { host, ...storedRange(days) } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setData(data.data);
    });
  }, [ws, host, days]);
  if (error) return <div className="px-4 pb-4"><ErrorText>{error}</ErrorText></div>;
  if (!data) return <div className="px-4 pb-4"><Loading /></div>;
  const options = metrics.filter((m) => m.count && data.filter((s) => s.series.some((p) => p[m.key] !== undefined && p[m.key] !== null)).length > 0);
  const shown = options.find((m) => m.key === metric) ?? options[0];
  if (!shown) return <p className="px-4 pb-4 text-[13px] text-muted-foreground">Nothing stored for this period yet.</p>;
  const lines = data.filter((s) => s.series.some((p) => p[shown.key] !== undefined && p[shown.key] !== null)).slice(0, 6);
  return (
    <figure className="border-t p-4">
      <figcaption className="mb-3 flex flex-wrap items-center justify-between gap-2 text-[13px] font-medium">
        <span>{shown.label} per day, by tool</span>
        {options.length > 1 && (
          <Tabs value={shown.key} onValueChange={(v) => setMetric(v as Metric)}>
            <TabsList className="h-7">
              {options.map((m) => <TabsTrigger key={m.key} value={m.key} className="px-2 text-[11px]">{m.label}</TabsTrigger>)}
            </TabsList>
          </Tabs>
        )}
      </figcaption>
      <Lines lines={lines} metric={shown} />
    </figure>
  );
}

// One line per tool on a shared axis; hovering a day lists every tool's
// number for it.
function Lines({ lines, metric }: { lines: Series[]; metric: (typeof metrics)[number] }) {
  const [hover, setHover] = useState<number | null>(null);
  const daysList = useMemo(() => [...new Set(lines.flatMap((l) => l.series.map((p) => p.date)))].sort(), [lines]);
  const value = (l: Series, d: string) => l.series.find((p) => p.date === d)?.[metric.key] ?? null;
  const max = Math.max(1, ...lines.flatMap((l) => daysList.map((d) => value(l, d) ?? 0)));
  const W = 600, H = 160;
  const x = (i: number) => (daysList.length > 1 ? (i / (daysList.length - 1)) * W : W / 2);
  const y = (v: number) => H - (v / max) * (H - 8);
  if (daysList.length < 2) return <p className="text-[13px] text-muted-foreground">Not enough days stored to draw a line yet.</p>;
  return (
    <div>
      <ul className="mb-2 flex flex-wrap gap-x-4 gap-y-1 text-[12px]" aria-label="Tools">
        {lines.map((l, i) => {
          const total = daysList.reduce((s, d) => s + (value(l, d) ?? 0), 0);
          return (
            <li key={l.integration_id} className="flex items-center gap-1.5">
              <span className="inline-block h-0.5 w-3 rounded-full" style={{ background: `var(--series-${i + 1})` }} />
              <span>{l.name}</span>
              <span className="font-mono text-muted-foreground tabular-nums">{metric.format(total)}</span>
            </li>
          );
        })}
      </ul>
      <div className="relative">
        <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" className="h-40 w-full overflow-visible" role="img"
          aria-label={`${metric.label} per day for ${lines.map((l) => l.name).join(", ")}`}
          onMouseLeave={() => setHover(null)}
          onMouseMove={(e) => {
            const r = e.currentTarget.getBoundingClientRect();
            setHover(Math.round(((e.clientX - r.left) / r.width) * (daysList.length - 1)));
          }}>
          <line x1={0} x2={W} y1={H} y2={H} className="stroke-border" strokeWidth={1} vectorEffect="non-scaling-stroke" />
          {hover !== null && <line x1={x(hover)} x2={x(hover)} y1={0} y2={H} className="stroke-muted-foreground/40" strokeWidth={1} vectorEffect="non-scaling-stroke" />}
          {lines.map((l, i) => {
            const pts = daysList.map((d, j) => [j, value(l, d)] as const).filter(([, v]) => v !== null);
            return <polyline key={l.integration_id} fill="none" stroke={`var(--series-${i + 1})`} strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" vectorEffect="non-scaling-stroke"
              points={pts.map(([j, v]) => `${x(j)},${y(v!)}`).join(" ")} />;
          })}
        </svg>
        {hover !== null && (
          <div className="pointer-events-none absolute top-0 z-10 rounded-md border bg-popover px-2 py-1 text-[12px] text-popover-foreground shadow-md"
            style={{ left: `${(hover / (daysList.length - 1)) * 100}%`, transform: `translateX(${hover > daysList.length / 2 ? "-105%" : "5%"})` }}>
            <div className="font-medium">{day(daysList[hover])}</div>
            {lines.map((l, i) => {
              const v = value(l, daysList[hover]);
              return (
                <div key={l.integration_id} className="flex items-center gap-1.5 whitespace-nowrap">
                  <span className="inline-block size-2 rounded-full" style={{ background: `var(--series-${i + 1})` }} />
                  {l.name}: <span className="font-mono tabular-nums">{v === null ? "–" : metric.format(v)}</span>
                </div>
              );
            })}
          </div>
        )}
      </div>
      <div className="mt-1 flex justify-between font-mono text-[11px] text-muted-foreground">
        <span>{day(daysList[0])}</span>
        <span>{day(daysList[daysList.length - 1])}</span>
      </div>
    </div>
  );
}

// Every tool's headline numbers side by side, so you can see which one to
// open and whether they agree.
function Summary({ ws, days, sources, selected, onPick }: { ws: string; days: number; sources: AnalyticsSource[]; selected?: string; onPick: (id: string) => void }) {
  const [items, setItems] = useState<AnalyticsSummaryItem[] | null>(null);
  useEffect(() => {
    setItems(null);
    api.GET("/v1/brands/{brand}/analytics/summary", { params: { path: { brand: pickedBrandId() }, query: range(days) } }).then(({ data }) => setItems(data?.data ?? []));
  }, [ws, days, sources]);
  if (!items) return null;
  // Only the columns some tool measures: a site with no search console has
  // no search clicks to show.
  const cols = (["visitors", "pageviews", "sessions", "clicks", "impressions"] as Metric[]).filter((k) => items.some((i) => i.totals?.[k] !== undefined && i.totals?.[k] !== null));
  return (
    <section aria-label="All analytics tools" className="overflow-x-auto rounded-lg border bg-card shadow-xs">
      <table className="w-full text-[13px]">
        <thead>
          <tr className="border-b text-left font-mono text-[11px] tracking-wide text-muted-foreground uppercase">
            <th className="px-4 py-2 font-medium">Tool · {periods.find((p) => p.days === days)?.label.toLowerCase()}</th>
            {cols.map((k) => <th key={k} className="px-4 py-2 text-right font-medium">{metrics.find((m) => m.key === k)!.label}</th>)}
          </tr>
        </thead>
        <tbody>
          {items.map((i) => (
            <tr key={i.id} onClick={() => onPick(i.id)} className={`cursor-pointer border-b last:border-0 hover:bg-muted/50 ${i.id === selected ? "bg-muted/40" : ""}`}>
              <td className="px-4 py-2">
                <button className="flex items-center gap-2 text-left font-medium" onClick={() => onPick(i.id)}>{sources.find((s) => s.id === i.id) && <ConnectorLogo id={sources.find((s) => s.id === i.id)!.connector} size="sm" />}{i.name}</button>
                {i.error && <div className="text-[12px] text-amber-600 dark:text-amber-400">{i.error.charAt(0).toUpperCase() + i.error.slice(1)}</div>}
              </td>
              {cols.map((k) => <td key={k} className="px-4 py-2 text-right font-mono tabular-nums">{show(i.totals, k)}</td>)}
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}

function SourceView({ ws, source, sources, days, onPick, canManage, onChanged, go, about, initialSite, onSite, initialTab, onTab }: {
  ws: string; source: AnalyticsSource; sources: AnalyticsSource[]; days: number; onPick: (id: string) => void; canManage: boolean; onChanged: () => void; go: (t: Tab, item?: string) => void; about: string;
  initialSite?: string; onSite?: (site: string) => void; initialTab?: AnalyticsReportName; onTab?: (tab: AnalyticsReportName) => void;
}) {
  const [sites, setSites] = useState<AnalyticsSitePlan[] | null>(null);
  const [site, setSite] = useState(initialSite || source.site);
  const [needSite, setNeedSite] = useState(false);
  const [traffic, setTraffic] = useState<AnalyticsReport | null>(null);
  const [error, setError] = useState("");
  const [fresh, setFresh] = useState(0);
  const tabs = breakdowns.filter((b) => source.reports.includes(b.id));
  const [tab, setTabState] = useState<AnalyticsReportName | undefined>(tabs.find((t) => t.id === initialTab)?.id ?? tabs[0]?.id);
  const setTab = (t: AnalyticsReportName) => {
    setTabState(t);
    onTab?.(t);
  };
  // One sign-in reads every site of the account, each one brand's: the
  // picked brand's page offers only its own (plans/brands/README.md).
  const { brand: picked } = useBrand(ws);
  const [mine, setMine] = useState<Set<string> | null>(null);
  const [picking, setPicking] = useState(false);
  const [reload, setReload] = useState(0);

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/analytics/{id}/sites", { params: { path: { ws, id: source.id } } }).then(({ data }) => setSites(data?.data ?? []));
  }, [ws, source.id, reload]);
  useEffect(() => {
    if (!picked) return setMine(null);
    api.GET("/v1/workspaces/{ws}/analytics/site-brands", { params: { path: { ws }, query: { brand: "all" } } }).then(({ data }) =>
      setMine(new Set((data?.data ?? []).filter((b) => b.integration_id === source.id && b.brand_id === picked.id).map((b) => b.site))));
  }, [ws, source.id, picked?.id, reload]); // eslint-disable-line react-hooks/exhaustive-deps
  const options = useMemo(() => (sites && picked ? (mine ? sites.filter((s) => mine.has(s.id)) : null) : sites), [sites, picked, mine]);

  // With no default, show the brand's first site that syncs rather than
  // asking: the site list below is where sites are chosen.
  useEffect(() => {
    if (!needSite || !options?.length) return;
    setSite((options.find((s) => s.sync) ?? options[0]).id);
  }, [needSite, options]);

  useEffect(() => {
    setTraffic(null);
    setError("");
    if (!source.reports.includes("traffic")) return;
    api.GET("/v1/workspaces/{ws}/analytics/{id}/reports/{report}", { params: { path: { ws, id: source.id, report: "traffic" }, query: { ...range(days), ...(site ? { site } : {}), ...(fresh ? { fresh: true } : {}) } } }).then(({ data, error }) => {
      if (error || !data) {
        const code = (error as { error?: { code?: string } } | undefined)?.error?.code;
        if (code === "pick_site") return setNeedSite(true);
        return setError(errorMessage(error));
      }
      setNeedSite(false);
      setTraffic(data);
    });
  }, [ws, source.id, source.reports, days, site, fresh, reload]);

  async function saveSite(id: string) {
    setSite(id);
    if (!canManage) return;
    const { error } = await api.PUT("/v1/workspaces/{ws}/analytics/{id}/site", { params: { path: { ws, id: source.id } }, body: { site: id } });
    if (error) toast(errorMessage(error), "error");
    else onChanged();
  }

  const siteName = sites?.find((s) => s.id === (traffic?.site ?? site))?.name;
  const shownSite = traffic?.site ?? site;
  useEffect(() => {
    if (shownSite) onSite?.(shownSite);
  }, [shownSite]); // eslint-disable-line react-hooks/exhaustive-deps
  // Search engines' rows open the page's searches, or the search's pages.
  const openFor = (t: AnalyticsReportName) => narrows(source.connector) && (t === "pages" || t === "search_queries")
    ? (key: string) => go("analytics", drillItem({ kind: t === "pages" ? "page" : "query", value: key }))
    : undefined;
  return (
    <div className="space-y-3">
      <DataSource logo={source.connector} name={source.name}>{about}</DataSource>
      <div className="space-y-6">
        <div className="flex flex-wrap items-center gap-2">
          {sources.length > 1 && (
            <select aria-label="Analytics tool" className={`${field} max-w-64`} value={source.id} onChange={(e) => onPick(e.target.value)}>
              {sources.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
            </select>
          )}
          {options && options.length > 1 && (
            <select aria-label="Site" className={`${field} max-w-72`} value={traffic?.site ?? site} onChange={(e) => saveSite(e.target.value)}>
              {!(traffic?.site ?? site) && <option value="">Choose a site…</option>}
              {options.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
            </select>
          )}
          <span className="flex-1" />
          {traffic && (
            <span className="text-[12px] text-muted-foreground" title={new Date(traffic.fetched_at).toLocaleString()}>
              {traffic.cached ? `Updated ${ago(traffic.fetched_at)}` : "Just updated"}
            </span>
          )}
          <Button variant="ghost" onClick={() => setFresh((n) => n + 1)} aria-label="Refresh from the provider" title="Ask the provider again instead of using the last answer"><Icon name="refresh" className="size-3.5" /></Button>
        </div>

        {source.status === "error" && (
          <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-[13px] text-amber-800 dark:text-amber-200">
            <span>{source.connector_name} refused the last call{source.last_error ? `: ${source.last_error}` : "."}</span>
            {canManage && <Button variant="ghost" onClick={() => go("connectors")}>Fix in Connectors</Button>}
          </div>
        )}

        {sites && sites.length > 1 && <SiteList ws={ws} source={source} sites={sites} canManage={canManage} onChanged={onChanged} />}
        {picking && picked && <SiteSetupDialog ws={ws} source={source} from="brand" focus={picked} onClose={(saved) => { setPicking(false); if (saved) { setNeedSite(false); setSite(""); setReload((n) => n + 1); onChanged(); } }} />}
        {needSite && sites?.length === 0 ? (
          <Empty title={`${source.name} can't see any sites`}>The account you connected has no properties it can read. Give it access in the provider, or connect a different account.</Empty>
        ) : needSite && picked && options?.length === 0 ? (
          <Empty
            title={`${picked.name} has no site in ${source.name} yet`}
            action={canManage ? <Button onClick={() => setPicking(true)}>Pick {picked.name}'s {source.connector_name} site</Button> : <p className="text-[13px] text-muted-foreground">Ask a workspace admin to pick it.</p>}
          >
            This sign-in can read {sites?.length === 1 ? "1 site" : `${sites?.length ?? 0} sites`}, and each belongs to one brand. Pick the ones that are {picked.name}'s; other brands' sites never show here.
          </Empty>
        ) : (
          <>
            <ErrorText>{error}</ErrorText>
            {source.reports.includes("traffic") && !traffic && !error && <Loading />}
            {traffic && <Traffic report={traffic} siteName={siteName} />}
            {tab && (
              <section className="space-y-3">
                <Tabs value={tab} onValueChange={(v) => setTab(v as AnalyticsReportName)}>
                  <TabsList className="h-8">
                    {tabs.map((t) => <TabsTrigger key={t.id} value={t.id} className="px-2.5 text-[12px]">{t.label}</TabsTrigger>)}
                  </TabsList>
                </Tabs>
                {/* Every table loads at once and stays loaded, so switching
                    between them shows the answer straight away. */}
                {tabs.map((t) => (
                  <div key={t.id + site + days + fresh} hidden={t.id !== tab}>
                    <Breakdown ws={ws} source={source} report={t.id} site={traffic?.site ?? site} days={days} fresh={fresh > 0} keyLabel={t.key} onOpen={openFor(t.id)} />
                  </div>
                ))}
              </section>
            )}
          </>
        )}
      </div>
    </div>
  );
}

// Every site one sign-in can see (each GA4 property or PostHog project):
// whether the sync stores it and which brand it belongs to.
function SiteList({ ws, source, sites, canManage, onChanged }: {
  ws: string; source: AnalyticsSource; sites: AnalyticsSitePlan[]; canManage: boolean; onChanged: () => void;
}) {
  const [open, setOpen] = useState(() => sites.some((s) => s.sync && !s.host));
  const syncing = sites.filter((s) => s.sync).length;
  return (
    <section aria-label={`Sites in ${source.name}`} className="rounded-lg border bg-card shadow-xs">
      <button className="flex w-full items-center justify-between gap-3 px-4 py-2.5 text-left" onClick={() => setOpen(!open)} aria-expanded={open}>
        <span className="text-[13px]">
          <span className="font-medium">{sites.length} sites in {source.name}</span>{" "}
          <span className="text-muted-foreground">· {syncing} imported{sites.some((s) => s.sync && !s.host) ? ", some without a brand" : ""}</span>
        </span>
        <Icon name="chevron" className={`size-4 text-muted-foreground transition-transform ${open ? "rotate-90" : ""}`} />
      </button>
      {open && (
        <div className="border-t p-3">
          <SiteSetup ws={ws} source={source} canManage={canManage} onSaved={onChanged} />
        </div>
      )}
    </section>
  );
}

function Traffic({ report, siteName }: { report: AnalyticsReport; siteName?: string }) {
  const present = metrics.filter((m) => report.totals[m.key] !== undefined && report.totals[m.key] !== null);
  const series = report.series ?? [];
  const chartable = metrics.filter((m) => m.count && series.some((p) => p[m.key] !== undefined && p[m.key] !== null));
  const [chart, setChart] = useState<Metric | undefined>(chartable[0]?.key);
  const shown = chartable.find((m) => m.key === chart) ?? chartable[0];
  return (
    <>
      {present.length > 0 && (
        <Stats cols={present.length >= 6 ? 6 : present.length >= 5 ? 5 : present.length >= 4 ? 4 : present.length === 3 ? 3 : 2}>
          {present.slice(0, 6).map((m) => <Stat key={m.key} label={m.label} value={show(report.totals, m.key)} />)}
        </Stats>
      )}
      {shown && series.length > 1 && (
        <figure className="rounded-lg border bg-card p-4 shadow-xs">
          <figcaption className="mb-3 flex flex-wrap items-center justify-between gap-2 text-[13px] font-medium">
            <span>{shown.label} per day{siteName ? <span className="font-normal text-muted-foreground"> · {siteName}</span> : null}</span>
            {chartable.length > 1 && (
              <Tabs value={shown.key} onValueChange={(v) => setChart(v as Metric)}>
                <TabsList className="h-7">
                  {chartable.map((m) => <TabsTrigger key={m.key} value={m.key} className="px-2 text-[11px]">{m.label}</TabsTrigger>)}
                </TabsList>
              </Tabs>
            )}
          </figcaption>
          <DayBars points={series.map((p) => ({ date: p.date, value: p[shown.key] ?? 0 }))} label={shown.label} format={shown.format} />
        </figure>
      )}
      {report.notes.length > 0 && (
        <ul className="space-y-1 text-[12px] text-muted-foreground">
          {report.notes.map((n) => <li key={n}>{n}</li>)}
        </ul>
      )}
    </>
  );
}

// One bar per day; hover shows the day and its number. One series, so no
// legend.
function DayBars({ points, label, format }: { points: { date: string; value: number }[]; label: string; format: (v: number) => string }) {
  const max = Math.max(1, ...points.map((p) => p.value));
  const total = points.reduce((s, p) => s + p.value, 0);
  return (
    <>
      <div className="flex h-40 items-end gap-px" role="img" aria-label={`${label} per day, ${format(total)} in total, highest ${format(max)}`}>
        {points.map((p) => (
          <div key={p.date} className="group relative flex h-full min-w-0 flex-1 items-end">
            <div className="w-full rounded-t-[3px] bg-brand/80 transition-colors group-hover:bg-brand" style={{ height: `${Math.max(p.value ? 2 : 0, (p.value / max) * 100)}%` }} />
            <div className="pointer-events-none absolute bottom-full left-1/2 z-10 mb-1 hidden -translate-x-1/2 whitespace-nowrap rounded-md border bg-popover px-2 py-1 text-[12px] text-popover-foreground shadow-md group-hover:block">
              <div className="font-medium">{day(p.date)}</div>
              <div>{format(p.value)} {label.toLowerCase()}</div>
            </div>
          </div>
        ))}
      </div>
      <div className="mt-1 flex justify-between font-mono text-[11px] text-muted-foreground">
        <span>{day(points[0].date)}</span>
        <span>{day(points[points.length - 1].date)}</span>
      </div>
    </>
  );
}

type Row = NonNullable<AnalyticsReport["rows"]>[number];

function Breakdown({ ws, source, report, site, days, fresh, keyLabel, onOpen }: { ws: string; source: AnalyticsSource; report: AnalyticsReportName; site: string; days: number; fresh: boolean; keyLabel: string; onOpen?: (key: string) => void }) {
  const [data, setData] = useState<AnalyticsReport | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/analytics/{id}/reports/{report}", { params: { path: { ws, id: source.id, report }, query: { ...range(days), limit: 50, ...(site ? { site } : {}), ...(fresh ? { fresh: true } : {}) } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setData(data);
    });
  }, [ws, source.id, report, site, days, fresh]);
  const rows = useMemo(() => data?.rows ?? [], [data]);
  const cols = metrics.filter((m) => rows.some((r) => r[m.key] !== undefined && r[m.key] !== null));
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!data) return <Loading />;
  return (
    <div className="space-y-2">
      <SearchRows rows={rows} cols={cols} keyLabel={keyLabel} onOpen={onOpen} />
      {onOpen && rows.length > 0 && <p className="text-[12px] text-muted-foreground">Click a {keyLabel === "Page" ? "page to see the searches that showed it" : "search to see the pages it showed"}.</p>}
      {data.notes.length > 0 && (
        <ul className="space-y-1 text-[12px] text-muted-foreground">
          {data.notes.filter((n) => !n.startsWith("Search Console data is usually")).map((n) => <li key={n}>{n}</li>)}
        </ul>
      )}
      {report === "events" && rows.some((r) => r.conversions !== undefined) && <p className="text-[12px] text-muted-foreground"><Badge>Conversions</Badge> are the events your tool counts as conversions or goals.</p>}
    </div>
  );
}

// A table of report rows. With onOpen each row opens its own page, and the
// first cell says so.
function SearchRows({ rows, cols, keyLabel, onOpen }: { rows: Row[]; cols: typeof metrics; keyLabel: string; onOpen?: (key: string) => void }) {
  return (
    <DataTable
      data={rows}
      rowId={(r: Row) => r.key}
      columnsMenu={false}
      onRowClick={onOpen ? (r: Row) => onOpen(r.key) : undefined}
      searchPlaceholder={rows.length > 10 ? `Filter ${keyLabel.toLowerCase()}s…` : undefined}
      empty={`No ${keyLabel.toLowerCase()}s in this period.`}
      columns={[
        {
          id: "key", header: keyLabel, accessorFn: (r: Row) => r.key,
          cell: ({ row: { original: r } }: { row: { original: Row } }) => onOpen ? (
            <span className="flex max-w-[34rem] items-center gap-1.5">
              <span className="truncate font-medium group-hover:text-brand-strong group-hover:underline dark:group-hover:text-brand" title={r.key}>{r.key}</span>
              <Icon name="chevron" className="size-3.5 shrink-0 text-muted-foreground" />
            </span>
          ) : <span className="block max-w-[28rem] truncate font-medium" title={r.key}>{r.key}</span>,
        },
        ...cols.map((m) => ({
          id: m.key,
          header: m.label,
          accessorFn: (r: Row) => r[m.key] ?? -1,
          meta: { align: "right" as const, mono: true },
          cell: ({ row: { original: r } }: { row: { original: Row } }) => show(r, m.key),
        })),
      ]}
    />
  );
}

// One page's searches, or one search's pages, from Search Console or Bing:
// its totals and days over the same period as the Search tab, then a row
// per search or page, each opening its own page in turn.
function SearchDrill({ ws, source, site, days, drill, go }: { ws: string; source: AnalyticsSource; site: string; days: number; drill: Drill; go: (t: Tab, item?: string) => void }) {
  const [traffic, setTraffic] = useState<AnalyticsReport | null>(null);
  const [list, setList] = useState<AnalyticsReport | null>(null);
  const [error, setError] = useState("");
  const isPage = drill.kind === "page";
  const report: AnalyticsReportName = isPage ? "search_queries" : "pages";
  useEffect(() => {
    const query = { ...range(days), ...(site ? { site } : {}), ...(isPage ? { page: drill.value } : { keyword: drill.value }) };
    const path = { ws, id: source.id };
    Promise.all([
      api.GET("/v1/workspaces/{ws}/analytics/{id}/reports/{report}", { params: { path: { ...path, report: "traffic" }, query } }),
      api.GET("/v1/workspaces/{ws}/analytics/{id}/reports/{report}", { params: { path: { ...path, report }, query: { ...query, limit: 500 } } }),
    ]).then(([t, l]) => {
      if (t.error || !t.data) return setError(errorMessage(t.error));
      if (l.error || !l.data) return setError(errorMessage(l.error));
      setTraffic(t.data);
      setList(l.data);
    });
  }, [ws, source.id, site, days, drill.value, isPage, report]);
  const rows = useMemo(() => list?.rows ?? [], [list]);
  const cols = metrics.filter((m) => ["clicks", "impressions", "ctr", "position"].includes(m.key));
  const period = periods.find((p) => p.days === days)?.label.toLowerCase() ?? `last ${days} days`;
  const back = () => go("analytics", "search");
  const external = isPage && /^https?:\/\//.test(drill.value);
  return (
    <section className="space-y-4" aria-label={isPage ? "Page" : "Search query"}>
      <nav aria-label="Breadcrumb" className="flex items-center gap-1.5 text-[13px] text-muted-foreground">
        <button onClick={back} className="hover:text-foreground">← Search</button>
        <span>/</span>
        <span>{isPage ? "Page" : "Search query"}</span>
      </nav>
      <header className="flex flex-wrap items-start gap-2">
        <h2 className="min-w-0 text-lg font-semibold break-all">{isPage ? drill.value : `“${drill.value}”`}</h2>
        <CopyButton text={drill.value} label={isPage ? "Copy URL" : "Copy search"} className="mt-0.5" />
        {external && (
          <a href={drill.value} target="_blank" rel="noreferrer noopener" className="mt-0.5 inline-flex items-center gap-1 text-[13px] text-muted-foreground underline underline-offset-2 hover:text-foreground">Open page</a>
        )}
      </header>
      <DataSource logo={source.connector} name={source.name}>
        {isPage ? "the searches that showed this page" : "the pages this search showed"} in the {period}. Search engines report it two to three days late.
      </DataSource>
      <ErrorText>{error}</ErrorText>
      {!error && (!traffic || !list) && <Loading />}
      {traffic && <Traffic report={{ ...traffic, notes: [] }} />}
      {list && (
        <div className="space-y-2">
          <h3 className="text-[13px] font-medium">{isPage ? "Searches that showed this page" : "Pages this search showed"}</h3>
          <SearchRows rows={rows} cols={cols} keyLabel={isPage ? "Query" : "Page"}
            onOpen={(key) => go("analytics", drillItem({ kind: isPage ? "query" : "page", value: key }))} />
          {list.notes.length > 0 && (
            <ul className="space-y-1 text-[12px] text-muted-foreground">
              {list.notes.filter((n) => !n.startsWith("Search Console data is usually")).map((n) => <li key={n}>{n}</li>)}
            </ul>
          )}
        </div>
      )}
    </section>
  );
}
