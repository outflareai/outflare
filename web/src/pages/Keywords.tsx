import { useEffect, useState } from "react";
import type { InternalLinkTarget, PageConversion, PageTarget, SEORegression, TrackedKeyword, SEOSite, SearchCompetition, SearchRow, SearchSitemap, SearchSource, URLInspection } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { DataTable } from "@/components/data-table";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api, errorMessage } from "../api";
import { ask, Badge, Button, Empty, ErrorText, field, Loading, PageHeader, Stat, Stats, toast } from "../ui";
import { ago, plural } from "../format";
import type { Tab } from "../App";

// Keywords: what Google Search Console says about each page of a site,
// from what the analytics sync stored (so opening it costs nothing):
// clicks and positions by page and by query, the keyword each page
// targets, how Google indexed the pages, and the sitemaps it read.

type Props = { ws: string; go: (tab: Tab, item?: string) => void };
type Section = "pages" | "queries" | "alerts" | "targets" | "rankings" | "conversions" | "links" | "cannibalisation" | "indexing" | "sitemaps";
const sections: Record<Section, string> = { pages: "Pages", queries: "Queries", alerts: "Drops", targets: "Targets", rankings: "Rankings", conversions: "Conversions", links: "Internal links", cannibalisation: "Cannibalisation", indexing: "Indexing", sitemaps: "Sitemaps" };

const num = (v: number) => v.toLocaleString();
const pct = (v: number) => `${(v * 100).toLocaleString(undefined, { maximumFractionDigits: 1 })}%`;
const pos = (v: number) => (v > 0 ? v.toLocaleString(undefined, { maximumFractionDigits: 1 }) : "–");
const day = (d: string) => new Date(d + "T00:00:00Z").toLocaleDateString(undefined, { month: "short", day: "numeric", timeZone: "UTC" });

export function pathOf(url: string) {
  try {
    const u = new URL(url);
    return u.pathname + u.search;
  } catch {
    return url;
  }
}

export function SitePicker({ sites, site, onChange }: { sites: SEOSite[]; site: string; onChange: (id: string) => void }) {
  if (sites.length < 2) return null;
  return (
    <select className={field} aria-label="Site" value={site} onChange={(e) => onChange(e.target.value)}>
      {sites.map((s) => <option key={s.id} value={s.id}>{s.host}</option>)}
    </select>
  );
}

export function Keywords({ ws, go }: Props) {
  const [sites, setSites] = useState<SEOSite[] | null>(null);
  const [site, setSite] = useState("");
  const [section, setSection] = useState<Section>("pages");
  const [error, setError] = useState("");
  const brandKey = useBrand(ws).query.brand ?? "";

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/seo-sites", { params: { path: { ws }, query: brandKey ? { brand: brandKey } : {} } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setSites(data.data);
      setSite((s) => (data.data.some((x) => x.id === s) ? s : (data.data[0]?.id ?? "")));
    });
  }, [ws, brandKey]);

  const header = (
    <PageHeader
      title="Keywords"
      description="What Google shows each page for and how often people click, from your own Search Console, with how Google indexed the pages. Agents use get_site_search and get_site_indexing."
      action={sites && <SitePicker sites={sites} site={site} onChange={setSite} />}
    />
  );
  if (error) return <div className="space-y-6">{header}<ErrorText>{error}</ErrorText></div>;
  if (!sites) return <div className="space-y-6">{header}<Loading /></div>;
  if (sites.length === 0)
    return (
      <div className="space-y-6">
        {header}
        <Empty title="Add your website first" action={<Button onClick={() => go("audits")}>Add a site</Button>}>
          Keywords are shown for the sites OutflareBot audits. Add your website, then connect Google Search Console.
        </Empty>
      </div>
    );
  return (
    <div className="space-y-6">
      {header}
      <Tabs value={section} onValueChange={(v) => setSection(v as Section)}>
        <TabsList className="h-8">
          {(Object.keys(sections) as Section[]).map((s) => <TabsTrigger key={s} value={s}>{sections[s]}</TabsTrigger>)}
        </TabsList>
      </Tabs>
      {site && section === "pages" && <Performance key={site + section} ws={ws} site={site} group="page" go={go} />}
      {site && section === "queries" && <Performance key={site + section} ws={ws} site={site} group="query" go={go} />}
      {site && section === "targets" && <Targets key={site} ws={ws} site={site} host={sites?.find((x) => x.id === site)?.host ?? ""} go={go} />}
      {site && section === "rankings" && <Rankings key={site} ws={ws} site={site} go={go} />}
      {site && section === "alerts" && <Drops key={site} ws={ws} site={site} go={go} />}
      {site && section === "conversions" && <Conversions key={site} ws={ws} site={site} go={go} />}
      {site && section === "links" && <InternalLinks key={site} ws={ws} site={site} go={go} onTargets={() => setSection("targets")} />}
      {site && section === "cannibalisation" && <Cannibalisation key={site} ws={ws} site={site} go={go} />}
      {site && section === "indexing" && <Indexing key={site} ws={ws} site={site} go={go} />}
      {site && section === "sitemaps" && <Sitemaps key={site} ws={ws} site={site} go={go} />}
    </div>
  );
}

// The rank tracker as seen from the other sections: which keywords the
// site already tracks, and a button to track one more in one click.
const kwKey = (k: string) => k.trim().toLowerCase().replace(/\s+/g, " ");

type Tracker = { tracked: Set<string>; ready: boolean; connected: boolean; add: (keyword: string) => Promise<void> };

function useTracker(ws: string, site: string): Tracker {
  const [tracked, setTracked] = useState<Set<string>>(new Set());
  const [ready, setReady] = useState(false);
  const [connected, setConnected] = useState(false);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/seo-sites/{site}/rankings", { params: { path: { ws, site } } }).then(({ data }) => {
      if (!data) return;
      setTracked(new Set(data.data.map((t) => kwKey(t.keyword))));
      setConnected(data.connections.length > 0);
      setReady(true);
    });
  }, [ws, site]);
  const add = async (keyword: string) => {
    const { data, error } = await api.POST("/v1/workspaces/{ws}/seo-sites/{site}/rankings", { params: { path: { ws, site } }, body: { keywords: [keyword] } });
    if (error) return toast(errorMessage(error), "error");
    setTracked((t) => new Set(t).add(kwKey(keyword)));
    toast(data?.added ? `Tracking "${keyword}". The first check has started.` : `"${keyword}" was already tracked.`);
  };
  return { tracked, ready, connected, add };
}

function TrackButton({ tracker, keyword, go }: { tracker: Tracker; keyword: string; go: Props["go"] }) {
  const [busy, setBusy] = useState(false);
  if (!tracker.ready || !keyword) return null;
  if (tracker.tracked.has(kwKey(keyword))) return <Badge title="Rank tracking follows this keyword (Rankings)">Tracked</Badge>;
  if (!tracker.connected)
    return (
      <Button variant="ghost" className="-my-1 h-6 px-2" title="Rank tracking needs a DataForSEO or Geonode connection" onClick={() => go("connectors", "geonode")}>
        Track
      </Button>
    );
  return (
    <Button
      variant="ghost"
      className="-my-1 h-6 px-2"
      disabled={busy}
      title="Add to rank tracking: checked weekly on Google, US desktop"
      onClick={async () => {
        setBusy(true);
        await tracker.add(keyword);
        setBusy(false);
      }}
    >
      Track
    </Button>
  );
}

function NoSource({ go }: { go: Props["go"] }) {
  return (
    <Empty title="Connect Google Search Console" action={<Button onClick={() => go("connectors", "google_search_console")}>Connect Search Console</Button>}>
      Sign in with the Google account that sees this site in Search Console. Outflare stores 16 months of clicks and impressions by page and query, and checks how Google indexed each page. It only reads.
    </Empty>
  );
}

function SourceLine({ src }: { src: SearchSource }) {
  return (
    <p className="text-[12px] text-muted-foreground">
      From Search Console property <span className="font-mono">{src.property}</span>
      {src.oldest_day && src.newest_day && <> · {day(src.oldest_day)} to {day(src.newest_day)} stored</>}
      {src.last_run_at && <> · synced {ago(src.last_run_at)}</>}
      {src.last_error && <span className="text-destructive"> · last sync failed: {src.last_error}</span>}
    </p>
  );
}

function Performance({ ws, site, group, go }: { ws: string; site: string; group: "page" | "query"; go: Props["go"] }) {
  const [rows, setRows] = useState<SearchRow[] | null>(null);
  const [totals, setTotals] = useState<SearchRow | null>(null);
  const [src, setSrc] = useState<SearchSource | null | undefined>(undefined);
  const [range, setRange] = useState({ from: "", to: "" });
  const [picked, setPicked] = useState<SearchRow | null>(null);
  const [error, setError] = useState("");
  const tracker = useTracker(ws, site);

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/seo-sites/{site}/search", { params: { path: { ws, site }, query: { group, limit: 500 } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setRows(data.data);
      setTotals(data.totals);
      setSrc(data.source);
      setRange({ from: data.from, to: data.to });
    });
  }, [ws, site, group]);

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!rows || src === undefined) return <Loading />;
  if (!src) return <NoSource go={go} />;
  const key = (r: SearchRow) => (group === "page" ? (r.page ?? "") : (r.query ?? ""));
  return (
    <div className="space-y-4">
      {totals && (
        <Stats cols={4}>
          <Stat label="Clicks" value={num(totals.clicks)} sub={`${day(range.from)} to ${day(range.to)}`} />
          <Stat label="Impressions" value={num(totals.impressions)} />
          <Stat label="Click-through rate" value={pct(totals.ctr)} />
          <Stat label="Average position" value={pos(totals.position)} />
        </Stats>
      )}
      <DataTable
        data={rows}
        rowId={key}
        onRowClick={setPicked}
        searchPlaceholder={group === "page" ? "Search pages" : "Search queries"}
        empty={rows.length === 0 ? "Search Console has no clicks or impressions for this site in these 28 days yet." : "Nothing matches."}
        initialSort={[{ id: "clicks", desc: true }]}
        mobile={(r) => (
          <div className="space-y-1">
            <div className="font-mono text-[12px] break-all">{group === "page" ? pathOf(key(r)) : key(r)}</div>
            <div className="text-xs text-muted-foreground">{plural(r.clicks, "click")} · {plural(r.impressions, "impression")} · position {pos(r.position)}</div>
            {group === "query" && <TrackButton tracker={tracker} keyword={key(r)} go={go} />}
          </div>
        )}
        columns={[
          {
            id: "key",
            header: group === "page" ? "Page" : "Query",
            accessorFn: key,
            cell: ({ row }) => <span className={group === "page" ? "font-mono text-[12px] break-all" : ""}>{group === "page" ? pathOf(key(row.original)) : key(row.original)}</span>,
          },
          { id: "clicks", header: "Clicks", accessorFn: (r) => r.clicks, meta: { align: "right", mono: true }, cell: ({ row }) => num(row.original.clicks) },
          { id: "impressions", header: "Impressions", accessorFn: (r) => r.impressions, meta: { align: "right", mono: true }, cell: ({ row }) => num(row.original.impressions) },
          { id: "ctr", header: "CTR", accessorFn: (r) => r.ctr, meta: { align: "right", mono: true }, cell: ({ row }) => pct(row.original.ctr) },
          { id: "position", header: "Position", accessorFn: (r) => r.position, meta: { align: "right", mono: true }, cell: ({ row }) => pos(row.original.position) },
          ...(group === "query"
            ? [{ id: "track", header: "", enableSorting: false, meta: { align: "right" as const }, cell: ({ row }: { row: { original: SearchRow } }) => <TrackButton tracker={tracker} keyword={key(row.original)} go={go} /> }]
            : []),
        ]}
      />
      <SourceLine src={src} />
      {picked && <Breakdown ws={ws} site={site} row={picked} group={group} tracker={tracker} go={go} onClose={() => setPicked(null)} />}
    </div>
  );
}

// Breakdown shows the queries of one page, or the pages of one query.
function Breakdown({ ws, site, row, group, tracker, go, onClose }: { ws: string; site: string; row: SearchRow; group: "page" | "query"; tracker: Tracker; go: Props["go"]; onClose: () => void }) {
  const [rows, setRows] = useState<SearchRow[] | null>(null);
  useEffect(() => {
    const query = group === "page" ? { page: row.page ?? "" } : { keyword: row.query ?? "" };
    api.GET("/v1/workspaces/{ws}/seo-sites/{site}/search", { params: { path: { ws, site }, query: { group: "page_query", limit: 100, ...query } } }).then(({ data }) => setRows(data?.data ?? []));
  }, [ws, site, row, group]);
  const title = group === "page" ? pathOf(row.page ?? "") : (row.query ?? "");
  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-lg">
        <SheetHeader>
          <SheetTitle className="break-all">{title}</SheetTitle>
          <SheetDescription>
            {plural(row.clicks, "click")} · {plural(row.impressions, "impression")} · position {pos(row.position)}
          </SheetDescription>
          {group === "query" && row.query && <div><TrackButton tracker={tracker} keyword={row.query} go={go} /></div>}
        </SheetHeader>
        <div className="space-y-3 px-4 pb-6 text-[13px]">
          <div className="flex items-center justify-between">
            <span className="font-medium">{group === "page" ? "Queries Google shows it for" : "Pages Google shows for it"}</span>
            {group === "page" && row.page && <a className="text-xs text-muted-foreground underline-offset-2 hover:underline" href={row.page} target="_blank" rel="noreferrer noopener">Open the page</a>}
          </div>
          {!rows && <Loading />}
          {rows && rows.length === 0 && <p className="text-muted-foreground">Nothing stored.</p>}
          {rows && rows.length > 0 && (
            <ul className="divide-y">
              {rows.map((r) => (
                <li key={`${r.page}|${r.query}`} className="flex items-center justify-between gap-3 py-1.5">
                  <span className={group === "page" ? "" : "font-mono text-[12px] break-all"}>{group === "page" ? r.query : pathOf(r.page ?? "")}</span>
                  <span className="flex shrink-0 items-center gap-2">
                    <span className="font-mono text-[12px] text-muted-foreground">{num(r.clicks)} · {num(r.impressions)} · {pos(r.position)}</span>
                    {group === "page" && <TrackButton tracker={tracker} keyword={r.query ?? ""} go={go} />}
                  </span>
                </li>
              ))}
            </ul>
          )}
          <p className="text-[12px] text-muted-foreground">Clicks · impressions · average position, over the same 28 days.</p>
        </div>
      </SheetContent>
    </Sheet>
  );
}

const stateLabel: Record<string, string> = { "": "Every page", not_indexed: "Not indexed", canonical_differs: "Google chose another canonical", changed: "Changed this week", indexed: "Indexed" };

function Indexing({ ws, site, go }: { ws: string; site: string; go: Props["go"] }) {
  const [rows, setRows] = useState<URLInspection[] | null>(null);
  const [summary, setSummary] = useState<Record<string, number> | null>(null);
  const [src, setSrc] = useState<SearchSource | null | undefined>(undefined);
  const [state, setState] = useState("");
  const [error, setError] = useState("");

  useEffect(() => {
    const query = state ? { state: state as "indexed" | "not_indexed" | "canonical_differs" | "changed" } : {};
    api.GET("/v1/workspaces/{ws}/seo-sites/{site}/indexing", { params: { path: { ws, site }, query } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setRows(data.data);
      setSummary(data.summary);
      setSrc(data.source);
    });
  }, [ws, site, state]);

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!rows || !summary || src === undefined) return <Loading />;
  if (!src) return <NoSource go={go} />;
  if (summary.inspected === 0)
    return <Empty title="No pages inspected yet">Each Search Console sync asks Google how up to 100 of the site's pages are indexed, starting with the pages OutflareBot crawled and the pages Google shows. The first results come with the next sync.</Empty>;
  return (
    <div className="space-y-4">
      <Stats cols={4}>
        <Stat label="Inspected" value={num(summary.inspected)} />
        <Stat label="Not indexed" value={num(summary.not_indexed)} tone={summary.not_indexed > 0 ? "amber" : undefined} />
        <Stat label="Other canonical" value={num(summary.canonical_differs)} tone={summary.canonical_differs > 0 ? "amber" : undefined} />
        <Stat label="Changed this week" value={num(summary.changed)} />
      </Stats>
      <DataTable
        data={rows}
        rowId={(r) => r.url}
        searchPlaceholder="Search pages"
        toolbar={
          <select className={field} aria-label="Show" value={state} onChange={(e) => setState(e.target.value)}>
            {Object.entries(stateLabel).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
          </select>
        }
        empty="Nothing matches."
        mobile={(r) => (
          <div className="space-y-1">
            <div className="font-mono text-[12px] break-all">{pathOf(r.url)}</div>
            <div className="text-xs text-muted-foreground">{r.coverage || r.verdict}</div>
          </div>
        )}
        columns={[
          { id: "url", header: "Page", accessorFn: (r) => r.url, cell: ({ row }) => <a className="font-mono text-[12px] break-all underline-offset-2 hover:underline" href={row.original.link || row.original.url} target="_blank" rel="noreferrer noopener">{pathOf(row.original.url)}</a> },
          { id: "indexed", header: "Indexed", accessorFn: (r) => (r.indexed ? 1 : 0), cell: ({ row }) => <Badge tone={row.original.indexed ? "green" : "amber"}>{row.original.indexed ? "Indexed" : "Not indexed"}</Badge> },
          { id: "coverage", header: "Google says", accessorFn: (r) => r.coverage, cell: ({ row }) => <span className="text-muted-foreground">{row.original.coverage || "–"}</span> },
          {
            id: "canonical",
            header: "Canonical",
            accessorFn: (r) => (r.canonical_differs ? 1 : 0),
            cell: ({ row }) => (row.original.canonical_differs ? <span className="font-mono text-[12px] break-all" title="Google chose this canonical, not the page's own">{pathOf(row.original.google_canonical)}</span> : <span className="text-muted-foreground">Its own</span>),
          },
          { id: "crawled", header: "Last crawled", accessorFn: (r) => r.last_crawl ?? "", cell: ({ row }) => <span className="text-muted-foreground">{row.original.last_crawl ? ago(row.original.last_crawl) : "Never"}</span> },
        ]}
      />
      <SourceLine src={src} />
    </div>
  );
}

function Sitemaps({ ws, site, go }: { ws: string; site: string; go: Props["go"] }) {
  const [rows, setRows] = useState<SearchSitemap[] | null>(null);
  const [src, setSrc] = useState<SearchSource | null | undefined>(undefined);
  const [error, setError] = useState("");
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/seo-sites/{site}/sitemaps", { params: { path: { ws, site } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setRows(data.data);
      setSrc(data.source);
    });
  }, [ws, site]);
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!rows || src === undefined) return <Loading />;
  if (!src) return <NoSource go={go} />;
  return (
    <div className="space-y-4">
      <DataTable
        data={rows}
        rowId={(r) => r.path}
        empty="No sitemaps submitted in Search Console for this site. Submit yours there so Google reads every page."
        columns={[
          { id: "path", header: "Sitemap", accessorFn: (r) => r.path, cell: ({ row }) => <span className="font-mono text-[12px] break-all">{row.original.path}</span> },
          { id: "indexed", header: "Indexed", accessorFn: (r) => r.indexed, meta: { align: "right", mono: true }, cell: ({ row }) => (row.original.submitted ? `${num(row.original.indexed)} of ${num(row.original.submitted)}` : "–") },
          { id: "errors", header: "Problems", accessorFn: (r) => r.errors * 1000 + r.warnings, cell: ({ row }) => (row.original.errors || row.original.warnings ? <Badge tone={row.original.errors ? "red" : "amber"}>{[row.original.errors && plural(row.original.errors, "error"), row.original.warnings && plural(row.original.warnings, "warning")].filter(Boolean).join(", ")}</Badge> : <span className="text-muted-foreground">None</span>) },
          { id: "read", header: "Last read", accessorFn: (r) => r.last_read ?? "", cell: ({ row }) => <span className="text-muted-foreground">{row.original.pending ? "Waiting" : row.original.last_read ? ago(row.original.last_read) : "Never"}</span> },
        ]}
      />
      <SourceLine src={src} />
    </div>
  );
}

function Check({ ok, label }: { ok: boolean | null; label: string }) {
  if (ok === null) return null;
  return <Badge tone={ok ? "green" : "amber"} title={ok ? `The keyword is in the ${label}` : `The keyword is not in the ${label}`}>{label}</Badge>;
}

function Targets({ ws, site, host, go }: { ws: string; site: string; host: string; go: Props["go"] }) {
  const [rows, setRows] = useState<PageTarget[] | null>(null);
  const [src, setSrc] = useState<SearchSource | null | undefined>(undefined);
  const [suggested, setSuggested] = useState(0);
  const [error, setError] = useState("");
  const [url, setURL] = useState("");
  const [keyword, setKeyword] = useState("");
  const [busy, setBusy] = useState(false);
  const [reload, setReload] = useState(0);
  const path = { ws, site };

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/seo-sites/{site}/targets", { params: { path } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setRows(data.data);
      setSrc(data.source);
      setSuggested(data.suggested);
    });
  }, [ws, site, reload]); // eslint-disable-line react-hooks/exhaustive-deps

  const done = (error: unknown, ok: string) => {
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast(ok);
    setReload((n) => n + 1);
  };
  const set = async (body: { url: string; keyword?: string; status?: "set" | "dismissed" }, ok: string) => {
    setBusy(true);
    const { error } = await api.PUT("/v1/workspaces/{ws}/seo-sites/{site}/targets", { params: { path }, body });
    done(error, ok);
    return !error;
  };
  const accept = async (urls?: string[]) => {
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/seo-sites/{site}/targets/accept", { params: { path }, body: urls ? { urls } : {} });
    done(error, data ? plural(data.accepted, "target") + " set" : "");
  };
  const edit = async (t: PageTarget) => {
    const r = await ask({ title: "Change the target keyword", body: <span className="font-mono text-[12px] break-all">{pathOf(t.url)}</span>, input: { label: "Keyword", value: t.keyword, maxLength: 200 }, choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Save", value: "ok" }] });
    if (r?.choice !== "ok" || !t.id) return;
    setBusy(true);
    const { error } = await api.PATCH("/v1/workspaces/{ws}/seo-sites/{site}/targets/{target}", { params: { path: { ...path, target: t.id } }, body: { keyword: r.value } });
    done(error, "Target changed");
  };
  const remove = async (t: PageTarget) => {
    if (!t.id) return;
    setBusy(true);
    const { error } = await api.DELETE("/v1/workspaces/{ws}/seo-sites/{site}/targets/{target}", { params: { path: { ...path, target: t.id } } });
    done(error, t.status === "dismissed" ? "Suggestions are back for this page" : "Target removed");
  };

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!rows || src === undefined) return <Loading />;
  const setCount = rows.filter((r) => r.status === "set").length;
  const missing = rows.filter((r) => r.status === "set" && (r.checks.in_title === false || r.checks.in_h1 === false || r.checks.in_description === false)).length;
  return (
    <div className="space-y-4">
      <Stats cols={3}>
        <Stat label="Pages with a target" value={num(setCount)} />
        <Stat label="Keyword missing on the page" value={num(missing)} tone={missing > 0 ? "amber" : undefined} sub={missing > 0 ? <button className="underline-offset-2 hover:underline" onClick={() => go("fixes")}>On the fixes list</button> : undefined} />
        <Stat label="Suggested" value={num(suggested)} />
      </Stats>
      <form
        className="flex flex-wrap items-center gap-2"
        onSubmit={async (e) => {
          e.preventDefault();
          if (await set({ url: url.startsWith("http") ? url : `https://${host}${url.startsWith("/") ? "" : "/"}${url}`, keyword }, "Target set")) {
            setURL("");
            setKeyword("");
          }
        }}
      >
        <input className={`${field} w-64`} aria-label="Page" placeholder={`Page, like https://${host}/pricing`} value={url} onChange={(e) => setURL(e.target.value)} required />
        <input className={`${field} w-56`} aria-label="Target keyword" placeholder="Target keyword" maxLength={200} value={keyword} onChange={(e) => setKeyword(e.target.value)} required />
        <Button type="submit" disabled={busy}>Set target</Button>
        {suggested > 0 && <Button type="button" variant="ghost" disabled={busy} onClick={() => accept()}>Accept {plural(suggested, "suggestion")}</Button>}
      </form>
      {!src && (
        <p className="text-xs text-muted-foreground">
          Connect <button className="underline underline-offset-2" onClick={() => go("connectors", "google_search_console")}>Google Search Console</button> to get a suggested keyword for every page (the query that brings it the most clicks) and each target's clicks and position.
        </p>
      )}
      <DataTable
        data={rows}
        rowId={(r) => r.url}
        searchPlaceholder="Search pages and keywords"
        empty={src ? "No targets yet, and nothing to suggest: Search Console has no queries with enough impressions for this site's pages." : "No targets yet. Set the keyword a page should rank for above."}
        mobile={(r) => (
          <div className="space-y-1">
            <div className="font-mono text-[12px] break-all">{pathOf(r.url)}</div>
            <div className="text-xs">{r.keyword || "–"} {r.status !== "set" && <Badge>{r.status === "suggested" ? "Suggested" : "Dismissed"}</Badge>}</div>
          </div>
        )}
        columns={[
          { id: "url", header: "Page", accessorFn: (r) => r.url, cell: ({ row }) => <span className="font-mono text-[12px] break-all">{pathOf(row.original.url)}</span> },
          {
            id: "keyword",
            header: "Keyword",
            accessorFn: (r) => r.keyword,
            cell: ({ row }) => (
              <span className="flex flex-wrap items-center gap-1.5">
                <span className={row.original.status === "dismissed" ? "text-muted-foreground" : ""}>{row.original.keyword || "–"}</span>
                {row.original.status === "suggested" && <Badge tone="blue">Suggested</Badge>}
                {row.original.status === "dismissed" && <Badge>Dismissed</Badge>}
              </span>
            ),
          },
          {
            id: "on_page",
            header: "On the page",
            enableSorting: false,
            accessorFn: (r) => r.status,
            cell: ({ row }) => {
              const c = row.original.checks;
              if (row.original.status !== "set") return null;
              if (c.in_title === null) return <span className="text-muted-foreground text-xs">Not in the latest crawl</span>;
              return (
                <span className="flex gap-1">
                  <Check ok={c.in_title} label="Title" />
                  <Check ok={c.in_h1} label="H1" />
                  <Check ok={c.in_description} label="Description" />
                </span>
              );
            },
          },
          { id: "clicks", header: "Clicks", accessorFn: (r) => r.search?.clicks ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => (row.original.search ? num(row.original.search.clicks) : "–") },
          { id: "position", header: "Position", accessorFn: (r) => r.search?.position ?? 0, meta: { align: "right", mono: true }, cell: ({ row }) => (row.original.search ? pos(row.original.search.position) : "–") },
          {
            id: "actions",
            header: "",
            enableSorting: false,
            cell: ({ row }) => {
              const t = row.original;
              return (
                <span className="flex justify-end gap-1">
                  {t.status === "suggested" && (
                    <>
                      <Button variant="ghost" disabled={busy} onClick={() => accept([t.url])}>Accept</Button>
                      <Button variant="ghost" disabled={busy} onClick={() => set({ url: t.url, status: "dismissed" }, "Dismissed")}>Dismiss</Button>
                    </>
                  )}
                  {t.status === "set" && (
                    <>
                      <Button variant="ghost" disabled={busy} onClick={() => edit(t)}>Change</Button>
                      <Button variant="ghost" disabled={busy} onClick={() => remove(t)}>Remove</Button>
                    </>
                  )}
                  {t.status === "dismissed" && <Button variant="ghost" disabled={busy} onClick={() => remove(t)}>Undo</Button>}
                </span>
              );
            },
          },
        ]}
      />
      {src && <SourceLine src={src} />}
    </div>
  );
}

function Cannibalisation({ ws, site, go }: { ws: string; site: string; go: Props["go"] }) {
  const [rows, setRows] = useState<SearchCompetition[] | null>(null);
  const [src, setSrc] = useState<SearchSource | null | undefined>(undefined);
  const [error, setError] = useState("");
  const tracker = useTracker(ws, site);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/seo-sites/{site}/cannibalisation", { params: { path: { ws, site }, query: { limit: 200 } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setRows(data.data);
      setSrc(data.source);
    });
  }, [ws, site]);
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!rows || src === undefined) return <Loading />;
  if (!src) return <NoSource go={go} />;
  return (
    <div className="space-y-4">
      <p className="text-sm text-muted-foreground">
        Searches where Google shows more than one of your pages and splits the clicks between them. Keep one page for each search and point the others to it. The 50 most shown are on the <button className="underline underline-offset-2" onClick={() => go("fixes")}>fixes list</button>.
      </p>
      <DataTable
        data={rows}
        rowId={(r) => r.query}
        searchPlaceholder="Search queries"
        empty="No pages compete for the same search. Searches for your brand name are left out."
        mobile={(r) => (
          <div className="space-y-1">
            <div>{r.query}</div>
            <div className="text-xs text-muted-foreground">{plural(r.pages.length, "page")}, {num(r.impressions)} impressions</div>
            <TrackButton tracker={tracker} keyword={r.query} go={go} />
          </div>
        )}
        columns={[
          { id: "query", header: "Search", accessorFn: (r) => r.query },
          { id: "impressions", header: "Impressions", accessorFn: (r) => r.impressions, meta: { align: "right", mono: true }, cell: ({ row }) => num(row.original.impressions) },
          { id: "clicks", header: "Clicks", accessorFn: (r) => r.clicks, meta: { align: "right", mono: true }, cell: ({ row }) => num(row.original.clicks) },
          {
            id: "pages",
            header: "Pages",
            enableSorting: false,
            cell: ({ row }) => (
              <ul className="space-y-1">
                {row.original.pages.map((p) => (
                  <li key={p.url} className="flex flex-wrap items-center gap-1.5">
                    <span className="font-mono text-[12px] break-all">{pathOf(p.url)}</span>
                    <span className="text-xs text-muted-foreground">{pct(p.share)} · position {pos(p.position)}</span>
                    {p.url === row.original.keep && <Badge tone="green" title={p.target ? "This page targets the search" : "The page with the most clicks"}>Keep</Badge>}
                  </li>
                ))}
              </ul>
            ),
          },
          { id: "track", header: "", enableSorting: false, meta: { align: "right" }, cell: ({ row }) => <TrackButton tracker={tracker} keyword={row.original.query} go={go} /> },
        ]}
      />
      <SourceLine src={src} />
    </div>
  );
}

function InternalLinks({ ws, site, go, onTargets }: { ws: string; site: string; go: Props["go"]; onTargets: () => void }) {
  const [rows, setRows] = useState<InternalLinkTarget[] | null>(null);
  const [notCrawled, setNotCrawled] = useState(0);
  const [error, setError] = useState("");
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/seo-sites/{site}/internal-links", { params: { path: { ws, site } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setRows(data.data);
      setNotCrawled(data.not_crawled.length);
    });
  }, [ws, site]);
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!rows) return <Loading />;
  return (
    <div className="space-y-4">
      <p className="text-sm text-muted-foreground">
        Pages about a target keyword that don't link to its page yet, from the latest crawl. A link from each, with the keyword as its text, tells Google which page is the one for it. Each page here is on the <button className="underline underline-offset-2" onClick={() => go("fixes")}>fixes list</button>, and the next crawl checks it.
        {notCrawled > 0 && ` ${plural(notCrawled, "target page")} weren't in the latest crawl.`}
      </p>
      <DataTable
        data={rows}
        rowId={(r) => r.url}
        searchPlaceholder="Search pages and keywords"
        empty={<>No links to suggest. Suggestions come from pages with a target keyword: <button className="underline underline-offset-2" onClick={onTargets}>set targets</button>.</>}
        mobile={(r) => (
          <div className="space-y-1">
            <div className="font-mono text-[12px] break-all">{pathOf(r.url)}</div>
            <div className="text-xs text-muted-foreground">{r.keyword} · {plural(r.from.length, "page")} to link from</div>
          </div>
        )}
        columns={[
          { id: "url", header: "Target page", accessorFn: (r) => r.url, cell: ({ row }) => <span className="font-mono text-[12px] break-all">{pathOf(row.original.url)}</span> },
          { id: "keyword", header: "Keyword", accessorFn: (r) => r.keyword },
          { id: "links_in", header: "Linked from", accessorFn: (r) => r.links_in, meta: { align: "right", mono: true }, cell: ({ row }) => plural(row.original.links_in, "page") },
          {
            id: "from",
            header: "Add a link from",
            enableSorting: false,
            cell: ({ row }) => (
              <ul className="space-y-1">
                {row.original.from.map((f) => (
                  <li key={f.url} className="flex flex-wrap items-center gap-1.5">
                    <span className="font-mono text-[12px] break-all">{pathOf(f.url)}</span>
                    <span className="text-xs text-muted-foreground">{f.reason === "ranks" ? `shown for it ${num(f.impressions)} times` : "title or H1 has it"}</span>
                  </li>
                ))}
              </ul>
            ),
          },
        ]}
      />
    </div>
  );
}

const moved = (t: TrackedKeyword) => {
  if (t.position === null || t.previous_position === null) return null;
  const d = t.previous_position - t.position;
  if (d === 0) return null;
  return <span className={d > 0 ? "text-emerald-600 dark:text-emerald-400" : "text-red-600 dark:text-red-400"}>{d > 0 ? `▲${d}` : `▼${-d}`}</span>;
};

// Rankings: where the site ranks on Google for the keywords it tracks,
// checked through the workspace's own DataForSEO or Geonode connection.
type RankConnection = { integration_id: string; name: string; connector: string };

function Rankings({ ws, site, go }: { ws: string; site: string; go: Props["go"] }) {
  const [rows, setRows] = useState<TrackedKeyword[] | null>(null);
  const [conn, setConn] = useState<RankConnection | null | undefined>(undefined);
  const [conns, setConns] = useState<RankConnection[]>([]);
  const [source, setSource] = useState("");
  const [error, setError] = useState("");
  const [keywords, setKeywords] = useState("");
  const [device, setDevice] = useState<"desktop" | "mobile">("desktop");
  const [busy, setBusy] = useState(false);
  const [reload, setReload] = useState(0);
  const path = { ws, site };

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/seo-sites/{site}/rankings", { params: { path } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setRows(data.data);
      setConn(data.connection);
      setConns(data.connections);
    });
  }, [ws, site, reload]); // eslint-disable-line react-hooks/exhaustive-deps

  const done = (error: unknown, ok: string) => {
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    if (ok) toast(ok);
    setReload((n) => n + 1);
  };
  const track = async (body: { keywords?: string[]; from_targets?: boolean }) => {
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/seo-sites/{site}/rankings", { params: { path }, body: { ...body, device, ...(source ? { integration_id: source } : {}) } });
    done(error, data ? `${plural(data.added, "keyword")} tracked${data.skipped ? `, ${data.skipped} already were` : ""}. The first check has started.` : "");
    return !error;
  };
  const check = async () => {
    setBusy(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/seo-sites/{site}/rankings/check", { params: { path } });
    done(error, "Checking now. Positions update in a minute or two.");
  };
  const toggle = async (t: TrackedKeyword) => {
    setBusy(true);
    const { error } = await api.PATCH("/v1/workspaces/{ws}/seo-sites/{site}/rankings/{tracked}", { params: { path: { ...path, tracked: t.id } }, body: { status: t.status === "active" ? "paused" : "active" } });
    done(error, t.status === "active" ? "Paused" : "Resumed");
  };
  const often = async (t: TrackedKeyword) => {
    setBusy(true);
    const { error } = await api.PATCH("/v1/workspaces/{ws}/seo-sites/{site}/rankings/{tracked}", { params: { path: { ...path, tracked: t.id } }, body: { frequency: t.frequency === "daily" ? "weekly" : "daily" } });
    done(error, t.frequency === "daily" ? "Checked weekly" : "Checked daily");
  };
  const switchSource = async (t: TrackedKeyword, id: string) => {
    setBusy(true);
    const { error } = await api.PATCH("/v1/workspaces/{ws}/seo-sites/{site}/rankings/{tracked}", { params: { path: { ...path, tracked: t.id } }, body: { integration_id: id } });
    done(error, `Checked through ${conns.find((c) => c.integration_id === id)?.name ?? "that connection"}`);
  };
  const sourceOf = (t: TrackedKeyword) => conns.find((c) => c.integration_id === t.integration_id) ?? conn;
  const remove = async (t: TrackedKeyword) => {
    setBusy(true);
    const { error } = await api.DELETE("/v1/workspaces/{ws}/seo-sites/{site}/rankings/{tracked}", { params: { path: { ...path, tracked: t.id } } });
    done(error, "No longer tracked");
  };

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!rows || conn === undefined) return <Loading />;
  if (!conn && rows.length === 0)
    return (
      <Empty
        title="Connect DataForSEO or Geonode to track rankings"
        action={
          <div className="flex gap-2">
            <Button onClick={() => go("connectors", "dataforseo")}>Connect DataForSEO</Button>
            <Button variant="ghost" onClick={() => go("connectors", "geonode")}>Connect Geonode</Button>
          </div>
        }
      >
        Outflare checks where your pages rank on Google for the keywords you choose, daily or weekly, with your own DataForSEO or Geonode account. Each check costs about $0.02 with DataForSEO or $0.005 with Geonode, and stops at the monthly budget you set on the connection.
      </Empty>
    );
  const ranked = rows.filter((r) => r.position !== null);
  const top10 = ranked.filter((r) => (r.position ?? 101) <= 10).length;
  const up = rows.filter((r) => r.position !== null && r.previous_position !== null && r.position < r.previous_position).length;
  const down = rows.filter((r) => r.previous_position !== null && (r.position === null || r.position > r.previous_position)).length;
  return (
    <div className="space-y-4">
      <Stats cols={4}>
        <Stat label="Tracked" value={num(rows.length)} />
        <Stat label="In the top 10" value={num(top10)} />
        <Stat label="Moved up" value={num(up)} />
        <Stat label="Moved down" value={num(down)} tone={down > 0 ? "amber" : undefined} />
      </Stats>
      <form
        className="flex flex-wrap items-center gap-2"
        onSubmit={async (e) => {
          e.preventDefault();
          const list = keywords.split(/[\n,]/).map((k) => k.trim()).filter(Boolean);
          if (list.length && (await track({ keywords: list }))) setKeywords("");
        }}
      >
        <input className={`${field} w-72`} aria-label="Keywords" placeholder="Keywords, separated by commas" value={keywords} onChange={(e) => setKeywords(e.target.value)} />
        <select className={field} aria-label="Device" value={device} onChange={(e) => setDevice(e.target.value as "desktop" | "mobile")}>
          <option value="desktop">Desktop</option>
          <option value="mobile">Mobile</option>
        </select>
        {conns.length > 1 && (
          <select className={field} aria-label="Check with" value={source} onChange={(e) => setSource(e.target.value)}>
            <option value="">{`Check with ${conns[0].name}`}</option>
            {conns.slice(1).map((c) => <option key={c.integration_id} value={c.integration_id}>{`Check with ${c.name}`}</option>)}
          </select>
        )}
        <Button type="submit" disabled={busy || !keywords.trim()}>Track</Button>
        <Button type="button" variant="ghost" disabled={busy} onClick={() => track({ from_targets: true })}>Track every target keyword</Button>
        {rows.length > 0 && <Button type="button" variant="ghost" disabled={busy} onClick={check}>Check now</Button>}
      </form>
      <p className="text-xs text-muted-foreground">
        {conn
          ? `Checked on Google (US unless you choose) through ${conns.length > 1 ? "the connection each keyword names" : conn.name}, within its monthly budget. Each check is on Outcomes.`
          : "The rank tracking connection was removed, so checks have stopped."} Agents use list_rankings and track_keywords.
      </p>
      <DataTable
        data={rows}
        rowId={(r) => r.id}
        searchPlaceholder="Search keywords"
        empty="No keywords tracked yet. Add some above, or track every page's target keyword."
        mobile={(r) => (
          <div className="space-y-1">
            <div className="text-[13px]">{r.keyword} <span className="text-xs text-muted-foreground">{r.country} · {r.device}</span></div>
            <div className="text-xs text-muted-foreground">{r.position !== null ? `#${r.position}` : r.last_checked_at ? "Not in the top 100" : "Not checked yet"} {moved(r)}</div>
          </div>
        )}
        columns={[
          {
            id: "keyword",
            header: "Keyword",
            accessorFn: (r) => r.keyword,
            cell: ({ row }) => (
              <div>
                <div>{row.original.keyword} {row.original.status === "paused" && <Badge>Paused</Badge>}</div>
                <div className="text-xs text-muted-foreground">
                  {row.original.country} · {row.original.device} · {row.original.frequency}
                  {conns.length > 1 && (
                    <>
                      {" · "}
                      <select
                        className="bg-transparent text-xs"
                        aria-label={`Check ${row.original.keyword} with`}
                        disabled={busy}
                        value={sourceOf(row.original)?.integration_id ?? ""}
                        onChange={(e) => switchSource(row.original, e.target.value)}
                      >
                        {conns.map((c) => <option key={c.integration_id} value={c.integration_id}>{c.name}</option>)}
                      </select>
                    </>
                  )}
                </div>
              </div>
            ),
          },
          {
            id: "position",
            header: "Position",
            accessorFn: (r) => r.position ?? 999,
            meta: { align: "right", mono: true },
            cell: ({ row }) => (row.original.position !== null ? <span>{row.original.position} {moved(row.original)}</span> : <span className="text-muted-foreground">{row.original.last_checked_at ? "100+" : "–"}</span>),
          },
          { id: "best", header: "Best", accessorFn: (r) => r.best_position ?? 999, meta: { align: "right", mono: true }, cell: ({ row }) => row.original.best_position ?? "–" },
          {
            id: "url",
            header: "Ranking page",
            accessorFn: (r) => r.url,
            cell: ({ row }) => {
              const r = row.original;
              const wrong = r.target_url && r.url && r.url !== r.target_url;
              return (
                <div>
                  <span className="font-mono text-[12px] break-all">{r.url ? pathOf(r.url) : "–"}</span>
                  {wrong && <div className="text-xs text-amber-700 dark:text-amber-400">Target is {pathOf(r.target_url!)}</div>}
                </div>
              );
            },
          },
          {
            id: "checked",
            header: "Checked",
            accessorFn: (r) => r.last_checked_at ?? "",
            cell: ({ row }) => (
              <div>
                <div className="text-xs text-muted-foreground">{row.original.last_checked_at ? ago(row.original.last_checked_at) : "Not yet"}</div>
                {row.original.last_error && <div className="text-xs text-red-600 dark:text-red-400">{row.original.last_error}</div>}
              </div>
            ),
          },
          {
            id: "actions",
            header: "",
            enableSorting: false,
            cell: ({ row }) => (
              <div className="flex justify-end gap-1">
                <Button variant="ghost" disabled={busy} onClick={() => often(row.original)}>{row.original.frequency === "daily" ? "Weekly" : "Daily"}</Button>
                <Button variant="ghost" disabled={busy} onClick={() => toggle(row.original)}>{row.original.status === "active" ? "Pause" : "Resume"}</Button>
                <Button variant="ghost" disabled={busy} onClick={() => remove(row.original)}>Remove</Button>
              </div>
            ),
          },
        ]}
      />
    </div>
  );
}

type ConversionSource = { integration_id: string; name: string; site: string; events: string[]; newest_day: string | null; last_error: string | null };

// Conversions: visits by the page they started on, and how many went on to
// a conversion event, from the workspace's own PostHog, beside Search
// Console's clicks for the page.
function Conversions({ ws, site, go }: { ws: string; site: string; go: Props["go"] }) {
  const [rows, setRows] = useState<PageConversion[] | null>(null);
  const [src, setSrc] = useState<ConversionSource | null>(null);
  const [totals, setTotals] = useState<{ sessions: number; conversions: number; conversion_rate: number; search_sessions: number; search_conversions: number; search_conversion_rate: number } | null>(null);
  const [error, setError] = useState("");
  const [events, setEvents] = useState("");
  const [busy, setBusy] = useState(false);
  const [reload, setReload] = useState(0);

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/seo-sites/{site}/conversions", { params: { path: { ws, site } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setRows(data.data);
      setSrc(data.source);
      setTotals(data.totals);
      setEvents(data.source?.events.join(", ") ?? "");
    });
  }, [ws, site, reload]);

  const save = async () => {
    if (!src) return;
    setBusy(true);
    const list = events.split(",").map((e) => e.trim()).filter(Boolean);
    const { error } = await api.PATCH("/v1/workspaces/{ws}/analytics/{id}/sites/{site}", { params: { path: { ws, id: src.integration_id, site: src.site } }, body: { conversion_events: list } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast(list.length ? "Saved. The last 90 days are being counted again." : "Saved. Visits are counted without conversions.");
    setReload((n) => n + 1);
  };

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!rows || !totals) return <Loading />;
  if (!src)
    return (
      <Empty title="Count conversions with PostHog" action={<Button onClick={() => go("connectors", "posthog")}>Connect PostHog</Button>}>
        Outflare counts the visits that start on each page and how many go on to a conversion, such as a sign-up, from your own PostHog. Once PostHog has page views for this site, they show here within a sync.
      </Empty>
    );
  return (
    <div className="space-y-4">
      <Stats cols={4}>
        <Stat label="Visits" value={num(totals.sessions)} />
        <Stat label="Conversions" value={num(totals.conversions)} sub={src.events.length ? pct(totals.conversion_rate) : undefined} />
        <Stat label="Visits from search" value={num(totals.search_sessions)} />
        <Stat label="Conversions from search" value={num(totals.search_conversions)} sub={src.events.length ? pct(totals.search_conversion_rate) : undefined} />
      </Stats>
      <form
        className="flex flex-wrap items-center gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          save();
        }}
      >
        <input className={`${field} w-80`} aria-label="Conversion events" placeholder="Events that count, like signed_up, purchase" value={events} onChange={(e) => setEvents(e.target.value)} />
        <Button type="submit" disabled={busy}>Save events</Button>
      </form>
      <p className="text-xs text-muted-foreground">
        From {src.name}{src.newest_day ? `, to ${day(src.newest_day)}` : ""}. A visit counts under the page it started on, and as a conversion when it went on to one of these events.
        {!src.events.length && " Choose the events that count to see conversions."}
        {src.last_error && ` The last sync failed: ${src.last_error}`} Agents use get_site_conversions.
      </p>
      <DataTable
        data={rows}
        rowId={(r) => r.url}
        searchPlaceholder="Search pages"
        empty="No visits counted for this site's pages yet."
        mobile={(r) => (
          <div className="space-y-1">
            <div className="font-mono text-[12px] break-all">{pathOf(r.url)}</div>
            <div className="text-xs text-muted-foreground">{num(r.sessions)} visits · {num(r.conversions)} conversions · {num(r.search_sessions)} from search</div>
          </div>
        )}
        columns={[
          {
            id: "url",
            header: "Landing page",
            accessorFn: (r) => r.url,
            cell: ({ row }) => (
              <div>
                <span className="font-mono text-[12px] break-all">{pathOf(row.original.url)}</span>
                {row.original.keyword && <div className="text-xs text-muted-foreground">Target: {row.original.keyword}</div>}
              </div>
            ),
          },
          { id: "sessions", header: "Visits", accessorFn: (r) => r.sessions, meta: { align: "right", mono: true }, cell: ({ row }) => num(row.original.sessions) },
          { id: "conversions", header: "Conversions", accessorFn: (r) => r.conversions, meta: { align: "right", mono: true }, cell: ({ row }) => `${num(row.original.conversions)} (${pct(row.original.conversion_rate)})` },
          { id: "search_sessions", header: "From search", accessorFn: (r) => r.search_sessions, meta: { align: "right", mono: true }, cell: ({ row }) => num(row.original.search_sessions) },
          { id: "search_conversions", header: "Search conversions", accessorFn: (r) => r.search_conversions, meta: { align: "right", mono: true }, cell: ({ row }) => `${num(row.original.search_conversions)} (${pct(row.original.search_conversion_rate)})` },
          { id: "clicks", header: "Search clicks", accessorFn: (r) => r.clicks ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => (row.original.clicks === null ? "–" : num(row.original.clicks)) },
          { id: "position", header: "Position", accessorFn: (r) => r.position ?? 999, meta: { align: "right", mono: true }, cell: ({ row }) => (row.original.position === null ? "–" : pos(row.original.position)) },
        ]}
      />
    </div>
  );
}

const dropKinds: Record<SEORegression["kind"], string> = {
  search_clicks_dropped: "Search clicks",
  rank_dropped: "Ranking",
  page_deindexed: "Out of the index",
  conversions_dropped: "Conversions",
  health_dropped: "Site health",
};

// Drops: what got worse since the numbers before, checked after every
// sync. Open ones are on the fixes list.
function Drops({ ws, site, go }: { ws: string; site: string; go: Props["go"] }) {
  const [status, setStatus] = useState<"open" | "resolved">("open");
  const [rows, setRows] = useState<SEORegression[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    setRows(null);
    api.GET("/v1/workspaces/{ws}/seo-sites/{site}/regressions", { params: { path: { ws, site }, query: { status } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setRows(data.data);
    });
  }, [ws, site, status]);
  if (error) return <ErrorText>{error}</ErrorText>;
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-sm text-muted-foreground">
          Outflare compares the latest numbers with the ones before after every sync: a page's search clicks or conversions this week against its weekly average, tracked rankings, Google's index and the site's health score. Each drop is on the <button className="underline underline-offset-2" onClick={() => go("fixes")}>fixes list</button> until it recovers. Agents use get_site_regressions.
        </p>
        <Tabs value={status} onValueChange={(v) => setStatus(v as "open" | "resolved")}>
          <TabsList className="h-8">
            <TabsTrigger value="open">Open</TabsTrigger>
            <TabsTrigger value="resolved">Recovered</TabsTrigger>
          </TabsList>
        </Tabs>
      </div>
      {!rows ? (
        <Loading />
      ) : (
        <DataTable
          data={rows}
          rowId={(r) => r.id}
          searchPlaceholder="Search drops"
          empty={status === "open" ? "Nothing has dropped. Drops show here after the syncs have a few weeks of numbers to compare." : "Nothing has recovered yet."}
          mobile={(r) => (
            <div className="space-y-1">
              <div className="text-[13px]">{dropKinds[r.kind]} <span className="font-mono text-[12px] break-all">{r.subject.startsWith("http") ? pathOf(r.subject) : r.subject}</span></div>
              <div className="text-xs text-muted-foreground">{r.detail}</div>
            </div>
          )}
          columns={[
            { id: "kind", header: "What dropped", accessorFn: (r) => dropKinds[r.kind], cell: ({ row }) => <Badge tone={row.original.kind === "page_deindexed" ? "red" : "amber"}>{dropKinds[row.original.kind]}</Badge> },
            { id: "subject", header: "Page or keyword", accessorFn: (r) => r.subject, cell: ({ row }) => <span className="font-mono text-[12px] break-all">{row.original.subject.startsWith("http") ? pathOf(row.original.subject) : row.original.subject || "The whole site"}</span> },
            { id: "detail", header: "Change", accessorFn: (r) => r.detail, enableSorting: false },
            { id: "when", header: status === "open" ? "Found" : "Recovered", accessorFn: (r) => r.resolved_at ?? r.detected_at, cell: ({ row }) => <span className="text-xs text-muted-foreground">{ago(row.original.resolved_at ?? row.original.detected_at)}</span> },
          ]}
        />
      )}
    </div>
  );
}
