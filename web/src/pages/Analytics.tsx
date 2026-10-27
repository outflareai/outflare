import { useCallback, useEffect, useMemo, useState } from "react";
import type { AnalyticsMetrics, AnalyticsOverviewSite, AnalyticsPoint, AnalyticsReport, AnalyticsReportName, AnalyticsSitePlan, AnalyticsSource, AnalyticsSummaryItem, AnalyticsSync, SEOSource } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, Empty, ErrorText, field, Loading, PageHeader, Stat, Stats, toast } from "../ui";
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
export function Analytics({ ws, canManage, go }: { ws: string; canManage: boolean; go: (t: Tab, item?: string) => void }) {
  const [sources, setSources] = useState<AnalyticsSource[] | null>(null);
  const [seo, setSEO] = useState<SEOSource[] | null>(null);
  const [error, setError] = useState("");
  const [selected, setSelected] = useState(() => remember(`analytics:${ws}`));
  const [days, setDays] = useState(28);
  const tracking = useSiteTracking(ws);

  const load = useCallback(async () => {
    const seoReq = api.GET("/v1/workspaces/{ws}/seo", { params: { path: { ws } } });
    const { data, error } = await api.GET("/v1/workspaces/{ws}/analytics", { params: { path: { ws } } });
    if (error || !data) return setError(errorMessage(error));
    setSources(data.data);
    setSEO((await seoReq).data?.data ?? []);
  }, [ws]);

  useEffect(() => {
    load();
  }, [load]);

  const source = sources?.find((s) => s.id === selected) ?? sources?.[0];
  const pick = (id: string) => {
    setSelected(id);
    remember(`analytics:${ws}`, id);
  };

  const header = (
    <PageHeader
      title="Website analytics"
      description="Visitors, top pages, traffic sources, search queries and conversions from the analytics tools already on your website, stored every six hours so you can compare tools and look back, plus a line from your SEO tools for each site."
      action={sources && seo && sources.length + seo.length > 0 && canManage ? <Button variant="ghost" onClick={() => go("connectors", "analytics")}><Icon name="plus" className="size-3.5" /> Connect another</Button> : undefined}
    />
  );
  // Until a site sends Outflare a visit, the way to add the snippet comes first.
  const snippet = tracking === false && (
    <Card
      title="Add Outflare's snippet to your website"
      description="One script tag in your site's <head> records every visit with where it came from, so sign-ups and revenue are credited to the channel that brought them. It works alongside the tools below."
    >
      <Button onClick={() => go("tracking")}>Get the snippet</Button>
    </Card>
  );
  if (error) return <div className="space-y-6">{header}<ErrorText>{error}</ErrorText></div>;
  if (!sources || !seo) return <div className="space-y-6">{header}<Loading /></div>;
  if (sources.length === 0 && seo.length === 0) {
    return (
      <div className="space-y-6">
        {header}
        {snippet}
        <Empty
          title="Connect the analytics you already use"
          action={canManage ? <Button onClick={() => go("connectors", "analytics")}>Choose a tool</Button> : <p className="text-[13px] text-muted-foreground">Ask a workspace admin to connect one.</p>}
        >
          Google Analytics, Search Console, Bing Webmaster Tools, PostHog, Plausible, Fathom, Matomo, Umami, Mixpanel, Amplitude, Cloudflare, Simple Analytics, Clarity or Vercel. Google tools connect with a sign-in; the rest take an API key. For rankings and backlinks, add DataForSEO or Ahrefs from the SEO connectors. Nothing else in Outflare needs this.
        </Empty>
      </div>
    );
  }
  return (
    <div className="space-y-6">
      {header}
      {snippet}
      <Websites ws={ws} days={days} sources={sources} seo={seo} selected={source?.id} onPick={pick} canManage={canManage} />
      {seo.length > 0 && (
        <p className="text-[13px] text-muted-foreground">
          SEO tools, their budgets and every keyword are on the <button className="text-foreground underline underline-offset-2 hover:text-brand-strong dark:hover:text-brand" onClick={() => go("seo")}>SEO page</button>.
        </p>
      )}
      {source && <SourceView key={source.id} ws={ws} source={source} sources={sources} days={days} setDays={setDays} onPick={pick} canManage={canManage} onChanged={load} go={go} />}
    </div>
  );
}

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
  useEffect(() => {
    if (!canManage) return;
    api.GET("/v1/workspaces/{ws}/analytics/site-brands", { params: { path: { ws } } }).then(({ data }) => {
      const out: Record<string, number> = {};
      for (const b of data?.data ?? []) if (brands.length ? b.source === "none" : !b.host) out[b.integration_id] = (out[b.integration_id] ?? 0) + 1;
      setUnmapped(out);
    });
  }, [ws, sources, reload, canManage, brands.length]);
  const setup = (id: string) => setSettingUp(sources.find((s) => s.id === id) ?? null);
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
  const dialog = settingUp && <SiteSetupDialog ws={ws} source={settingUp} from="analytics" onClose={(saved) => { setSettingUp(null); if (saved) setReload((n) => n + 1); }} />;
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/analytics/overview", { params: { path: { ws }, query: { ...storedRange(days), ...(brand ? { brand } : {}) } } }).then(({ data, error }) => {
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
  const searchTools = site.tools.filter((t) => t.totals?.clicks !== undefined && t.totals?.clicks !== null).length;
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
                <tr key={t.integration_id + t.site} className={`border-b last:border-0 ${t.integration_id === selected ? "bg-muted/40" : ""}`}>
                  <td className="px-4 py-2">
                    <button className="flex items-center gap-2 text-left font-medium hover:underline" onClick={() => onPick(t.integration_id)} title="Open this tool's reports below"><ConnectorLogo id={t.connector} size="sm" />{t.name}</button>
                    {t.site_name && <div className="truncate pl-6 text-[12px] text-muted-foreground">{t.site_name}</div>}
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
    api.GET("/v1/workspaces/{ws}/analytics/compare", { params: { path: { ws }, query: { host, ...storedRange(days) } } }).then(({ data, error }) => {
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
    api.GET("/v1/workspaces/{ws}/analytics/summary", { params: { path: { ws }, query: range(days) } }).then(({ data }) => setItems(data?.data ?? []));
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

function SourceView({ ws, source, sources, days, setDays, onPick, canManage, onChanged, go }: {
  ws: string; source: AnalyticsSource; sources: AnalyticsSource[]; days: number; setDays: (d: number) => void; onPick: (id: string) => void; canManage: boolean; onChanged: () => void; go: (t: Tab, item?: string) => void;
}) {
  const [sites, setSites] = useState<AnalyticsSitePlan[] | null>(null);
  const [site, setSite] = useState(source.site);
  const [needSite, setNeedSite] = useState(false);
  const [traffic, setTraffic] = useState<AnalyticsReport | null>(null);
  const [error, setError] = useState("");
  const [fresh, setFresh] = useState(0);
  const tabs = breakdowns.filter((b) => source.reports.includes(b.id));
  const [tab, setTab] = useState<AnalyticsReportName | undefined>(tabs[0]?.id);

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/analytics/{id}/sites", { params: { path: { ws, id: source.id } } }).then(({ data }) => setSites(data?.data ?? []));
  }, [ws, source.id]);

  // With no default, show the first site that syncs rather than asking:
  // the site list below is where sites are chosen.
  useEffect(() => {
    if (!needSite || !sites?.length) return;
    setSite((sites.find((s) => s.sync) ?? sites[0]).id);
  }, [needSite, sites]);

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
  }, [ws, source.id, source.reports, days, site, fresh]);

  async function saveSite(id: string) {
    setSite(id);
    if (!canManage) return;
    const { error } = await api.PUT("/v1/workspaces/{ws}/analytics/{id}/site", { params: { path: { ws, id: source.id } }, body: { site: id } });
    if (error) toast(errorMessage(error), "error");
    else onChanged();
  }

  const siteName = sites?.find((s) => s.id === (traffic?.site ?? site))?.name;
  return (
    <div className="space-y-3">
      <h2 className="text-[13px] font-medium">Reports <span className="font-normal text-muted-foreground">· pages, sources and searches, live from one tool</span></h2>
      <div className="space-y-6">
        <div className="flex flex-wrap items-center gap-2">
          {sources.length > 1 && (
            <select aria-label="Analytics tool" className={`${field} max-w-64`} value={source.id} onChange={(e) => onPick(e.target.value)}>
              {sources.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
            </select>
          )}
          {sites && sites.length > 1 && (
            <select aria-label="Site" className={`${field} max-w-72`} value={traffic?.site ?? site} onChange={(e) => saveSite(e.target.value)}>
              {!(traffic?.site ?? site) && <option value="">Choose a site…</option>}
              {sites.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
            </select>
          )}
          <select aria-label="Period" className={field} value={days} onChange={(e) => setDays(Number(e.target.value))}>
            {periods.map((p) => <option key={p.days} value={p.days}>{p.label}</option>)}
          </select>
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
        {needSite && sites?.length === 0 ? (
          <Empty title={`${source.name} can't see any sites`}>The account you connected has no properties it can read. Give it access in the provider, or connect a different account.</Empty>
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
                <Breakdown key={tab + site + days + fresh} ws={ws} source={source} report={tab} site={traffic?.site ?? site} days={days} fresh={fresh > 0} keyLabel={breakdowns.find((b) => b.id === tab)!.key} />
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

function Breakdown({ ws, source, report, site, days, fresh, keyLabel }: { ws: string; source: AnalyticsSource; report: AnalyticsReportName; site: string; days: number; fresh: boolean; keyLabel: string }) {
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
      <DataTable
        data={rows}
        rowId={(r: Row) => r.key}
        columnsMenu={false}
        searchPlaceholder={rows.length > 10 ? `Filter ${keyLabel.toLowerCase()}s…` : undefined}
        empty={`No ${keyLabel.toLowerCase()}s in this period.`}
        columns={[
          { id: "key", header: keyLabel, accessorFn: (r: Row) => r.key, cell: ({ row: { original: r } }: { row: { original: Row } }) => <span className="block max-w-[28rem] truncate font-medium" title={r.key}>{r.key}</span> },
          ...cols.map((m) => ({
            id: m.key,
            header: m.label,
            accessorFn: (r: Row) => r[m.key] ?? -1,
            meta: { align: "right" as const, mono: true },
            cell: ({ row: { original: r } }: { row: { original: Row } }) => show(r, m.key),
          })),
        ]}
      />
      {data.notes.length > 0 && (
        <ul className="space-y-1 text-[12px] text-muted-foreground">
          {data.notes.filter((n) => !n.startsWith("Search Console data is usually")).map((n) => <li key={n}>{n}</li>)}
        </ul>
      )}
      {report === "events" && rows.some((r) => r.conversions !== undefined) && <p className="text-[12px] text-muted-foreground"><Badge>Conversions</Badge> are the events your tool counts as conversions or goals.</p>}
    </div>
  );
}
