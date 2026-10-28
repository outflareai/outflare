import { useEffect, useMemo, useState, type ReactNode } from "react";
import type { Backlink, BacklinkEvent, BacklinkSiteSummary, paths } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api, errorMessage } from "../api";
import { Badge, Button, Empty, ErrorText, field, Loading, Menu, PageHeader, Stat, Stats, toast, type tones } from "../ui";
import { ago } from "../format";
import type { Tab } from "../App";

// Backlinks: the links other sites have to the workspace's SEO sites,
// merged from Ahrefs and DataForSEO, and the feed of links gained, lost and
// regained. It reads only what the weekly SEO snapshot stored, so opening
// it and filtering it costs nothing. The table follows Ahrefs' backlinks
// report: one link per domain, All, New and Lost over a history range, and
// the full linking and linked URLs.

type Props = { ws: string; go: (tab: Tab, item?: string) => void };
type Tone = keyof typeof tones;
type Section = "links" | "changes";
type Kind = BacklinkEvent["kind"];
type Review = "ok" | "disavow" | "none";
type ListQuery = NonNullable<paths["/v1/workspaces/{ws}/backlinks"]["get"]["parameters"]["query"]>;

const kindLabel: Record<Kind, string> = { new: "New", lost: "Lost", regained: "Regained" };
const kindTone: Record<Kind, Tone> = { new: "green", lost: "red", regained: "blue" };
const providerName: Record<string, string> = { ahrefs: "Ahrefs", dataforseo: "DataForSEO" };
const spamTone: Record<string, Tone> = { toxic: "red", suspect: "amber", clean: "grey" };
const factorLabel: Record<string, string> = {
  provider_spam: "the SEO tool's own spam score",
  weak_domain: "a very weak linking domain",
  spam_tld: "a domain ending spam networks favour",
  spam_anchor: "spam words in the anchor",
  link_farm: "a page with hundreds of outgoing links",
  many_outlinks: "a page with many outgoing links",
  sitewide: "the same domain linking 100+ times",
};
const linkTypeLabel: Record<string, string> = {
  content: "In content",
  text: "Text",
  image: "Image",
  redirect: "Redirect",
  canonical: "Canonical",
  alternate: "Alternate",
  frame: "Frame",
  form: "Form",
  rss: "RSS",
};
// History ranges, in days back from today; 0 is all time.
const histories: [number, string][] = [[7, "Last 7 days"], [30, "Last month"], [90, "Last 3 months"], [180, "Last 6 months"], [365, "Last year"], [0, "All time"]];
const orders: [string, string][] = [
  ["rating", "DR"],
  ["url_rating", "UR"],
  ["domain_traffic", "Domain traffic"],
  ["page_traffic", "Page traffic"],
  ["keywords", "Keywords"],
  ["newest", "First seen, newest"],
  ["oldest", "First seen, oldest"],
  ["lost", "Lost, latest"],
  ["spam", "Spam score"],
];

const num = (v: number | null | undefined) => {
  if (v === null || v === undefined) return "–";
  if (v >= 1e6) return `${(v / 1e6).toFixed(v >= 1e7 ? 0 : 1)}M`;
  if (v >= 1e4) return `${(v / 1e3).toFixed(v >= 1e5 ? 0 : 1)}K`;
  return v.toLocaleString(undefined, { maximumFractionDigits: v < 10 ? 1 : 0 });
};
const date = (iso: string | null | undefined) => (iso ? new Date(iso).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" }) : "");
const dayISO = (daysBack: number) => new Date(Date.now() - daysBack * 864e5).toISOString().slice(0, 10);
// The last part of a page type or category path: /Article/Listicle is Listicle.
const leaf = (p: string) => p.split("/").filter(Boolean).pop()?.replaceAll("_", " ") ?? p;

// Filters live in the URL, so a filtered view can be shared and survives a
// reload.
type Filters = {
  group: "domain" | "page" | "all";
  change: "" | "new" | "lost";
  history: number;
  follow: "" | "dofollow" | "nofollow" | "ugc" | "sponsored";
  type: string;
  dr: number;
  traffic: number;
  pageType: string;
  q: string;
  qIn: "" | "source_url" | "target_url" | "anchor" | "title";
  order: string;
  spam: "" | "toxic" | "suspect" | "clean";
};
const defaults: Filters = { group: "domain", change: "", history: 30, follow: "", type: "", dr: 0, traffic: 0, pageType: "", q: "", qIn: "", order: "rating", spam: "" };
const keys: Record<keyof Filters, string> = { group: "group", change: "show", history: "history", follow: "follow", type: "type", dr: "dr", traffic: "traffic", pageType: "page_type", q: "q", qIn: "in", order: "order", spam: "spam" };

function readFilters(): Filters {
  const p = new URLSearchParams(location.search);
  const f = { ...defaults } as Record<keyof Filters, string | number>;
  for (const k of Object.keys(keys) as (keyof Filters)[]) {
    const v = p.get(keys[k]);
    if (v === null) continue;
    f[k] = typeof defaults[k] === "number" ? Number(v) || 0 : v;
  }
  return f as Filters;
}

function writeFilters(f: Filters) {
  const p = new URLSearchParams(location.search);
  for (const k of Object.keys(keys) as (keyof Filters)[]) {
    if (f[k] === defaults[k]) p.delete(keys[k]);
    else p.set(keys[k], String(f[k]));
  }
  const qs = p.toString();
  history.replaceState(history.state, "", location.pathname + (qs ? `?${qs}` : ""));
}

function toQuery(f: Filters, site: string, brand: object): ListQuery {
  const q: ListQuery = { ...brand, site_id: site, order: f.order as ListQuery["order"], limit: 100 };
  if (f.group !== "all") q.group = f.group;
  if (f.history > 0) q.since = dayISO(f.history);
  if (f.change) {
    q.change = f.change;
    if (!q.since) q.since = "2000-01-01";
  } else if (!q.since) {
    q.status = "live";
  }
  if (f.follow) q.follow = f.follow;
  if (f.type) q.link_type = f.type as ListQuery["link_type"];
  if (f.dr) q.dr_min = f.dr;
  if (f.traffic) q.domain_traffic_min = f.traffic;
  if (f.pageType) q.page_type = f.pageType;
  if (f.q.trim()) q.q = f.q.trim();
  if (f.q.trim() && f.qIn) q.q_in = f.qIn;
  if (f.spam) q.spam = f.spam;
  return q;
}

export function Backlinks({ ws, go }: Props) {
  const [sites, setSites] = useState<BacklinkSiteSummary[] | null>(null);
  const [site, setSite] = useState("");
  const [section, setSection] = useState<Section>("links");
  const [filters, setFiltersState] = useState<Filters>(readFilters);
  const [search, setSearch] = useState(filters.q);
  const [reload, setReload] = useState(0);
  const [links, setLinks] = useState<Backlink[] | null>(null);
  const [total, setTotal] = useState(0);
  const [next, setNext] = useState<number | null>(null);
  const [more, setMore] = useState(false);
  const [events, setEvents] = useState<BacklinkEvent[] | null>(null);
  const [error, setError] = useState("");
  const brandKey = useBrand(ws).query.brand ?? "";
  const brand = brandKey ? { brand: brandKey } : {};
  const setFilters = (patch: Partial<Filters>) =>
    setFiltersState((f) => {
      const n = { ...f, ...patch };
      writeFilters(n);
      return n;
    });

  // Typing searches after a pause, not on every key.
  useEffect(() => {
    const t = setTimeout(() => search !== filters.q && setFilters({ q: search }), 350);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [search]);

  useEffect(() => {
    setSites(null);
    api.GET("/v1/workspaces/{ws}/backlinks/summary", { params: { path: { ws }, query: brand } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setError("");
      setSites(data.data);
      setSite((s) => (data.data.some((x) => x.site.id === s) ? s : (data.data[0]?.site.id ?? "")));
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brandKey]);

  const query = useMemo(() => (site ? toQuery(filters, site, brand) : null), [filters, site, brandKey]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (!query) return;
    setLinks(null);
    api.GET("/v1/workspaces/{ws}/backlinks", { params: { path: { ws }, query } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setError("");
      setLinks(data.data);
      setTotal(data.total);
      setNext(data.next_offset);
    });
  }, [ws, query, reload]);

  const loadMore = async () => {
    if (!query || next === null) return;
    setMore(true);
    const { data, error } = await api.GET("/v1/workspaces/{ws}/backlinks", { params: { path: { ws }, query: { ...query, offset: next } } });
    setMore(false);
    if (error || !data) return setError(errorMessage(error));
    setLinks((l) => [...(l ?? []), ...data.data]);
    setNext(data.next_offset);
  };

  const review = async (l: Backlink, review: Review, domain = false) => {
    const { data, error } = await api.PATCH("/v1/workspaces/{ws}/backlinks/{backlink}", { params: { path: { ws, backlink: l.id } }, body: { review, domain } });
    if (error || !data) return setError(errorMessage(error));
    setError("");
    if (domain) toast(`${data.updated.toLocaleString()} links from ${l.source_domain} marked.`);
    setReload((n) => n + 1);
    setSites((list) => list && list.map((x) => (x.site.id === site ? { ...x, spam: { ...x.spam, disavowed: x.spam.disavowed + (review === "disavow" ? data.updated : 0) } } : x)));
  };
  const download = async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/backlinks/disavow", { params: { path: { ws }, query: { site_id: site } }, parseAs: "text" });
    if (error || data === undefined) return setError(errorMessage(error));
    const a = document.createElement("a");
    a.href = URL.createObjectURL(new Blob([data], { type: "text/plain" }));
    a.download = `disavow-${current?.site.host ?? "site"}.txt`;
    a.click();
    URL.revokeObjectURL(a.href);
  };

  useEffect(() => {
    if (!site || section !== "changes") return;
    setEvents(null);
    api.GET("/v1/workspaces/{ws}/backlinks/events", { params: { path: { ws }, query: { ...brand, site_id: site, limit: 500 } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setEvents(data.data);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brandKey, site, section]);

  const current = sites?.find((s) => s.site.id === site);
  const header = (
    <PageHeader
      title="Backlinks"
      description="The links other sites have to yours, merged from Ahrefs and DataForSEO on your own accounts, and which ones you gained, lost and got back. Filtering reads what is stored, so it costs nothing. Agents use list_backlinks and list_backlink_events."
      action={
        sites && sites.length > 1 ? (
          <select className={field} aria-label="Site" value={site} onChange={(e) => setSite(e.target.value)}>
            {sites.map((s) => <option key={s.site.id} value={s.site.id}>{s.site.host}</option>)}
          </select>
        ) : undefined
      }
    />
  );
  if (error && !sites) return <div className="space-y-6">{header}<ErrorText>{error}</ErrorText></div>;
  if (!sites) return <div className="space-y-6">{header}<Loading /></div>;
  if (sites.length === 0)
    return (
      <div className="space-y-6">
        {header}
        <Empty title="No website yet" action={<Button onClick={() => go("audits")}>Add your website</Button>}>
          Backlinks are read for the websites you audit. Add yours under Site audits, then connect Ahrefs or DataForSEO.
        </Empty>
      </div>
    );
  if (!current) return <div className="space-y-6">{header}<Loading /></div>;

  const read = current.providers.length > 0;
  const ahrefs = current.providers.some((p) => p.connector === "ahrefs");
  return (
    <div className="space-y-6">
      {header}
      {error && <ErrorText>{error}</ErrorText>}
      {!read && (
        <Empty title="No links read yet" action={<Button onClick={() => go("connectors")}>Connect Ahrefs or DataForSEO</Button>}>
          Connect Ahrefs or DataForSEO with your own account. Every week Outflare reads the links to {current.site.host} (the strongest 200 by default, within the connection's budget) and keeps their history here. To read them now, take a snapshot on Rankings & backlinks.
        </Empty>
      )}
      {read && (
        <>
          <Stats cols={5}>
            <Stat label="Live links" value={current.live.toLocaleString()} sub={`${current.followed.toLocaleString()} followed`} />
            <Stat label="Referring domains" value={current.referring_domains.toLocaleString()} />
            <Stat label="Gained, 30 days" value={current.last_30_days.new + current.last_30_days.regained ? `+${(current.last_30_days.new + current.last_30_days.regained).toLocaleString()}` : "0"} sub={current.last_30_days.regained ? `${current.last_30_days.regained.toLocaleString()} came back` : undefined} tone={current.last_30_days.new + current.last_30_days.regained > 0 ? "green" : undefined} />
            <Stat label="Lost, 30 days" value={current.last_30_days.lost ? `−${current.last_30_days.lost.toLocaleString()}` : "0"} tone={current.last_30_days.lost > 0 ? "red" : undefined} />
            <Stat label="Toxic links" value={current.spam.toxic.toLocaleString()} sub={current.spam.unreviewed ? `${current.spam.unreviewed.toLocaleString()} to review` : `${current.spam.suspect.toLocaleString()} suspect`} tone={current.spam.unreviewed > 0 ? "amber" : undefined} />
          </Stats>
          <Weeks weeks={current.weeks} />
          <p className="flex flex-wrap gap-x-4 gap-y-1 text-[12px] text-muted-foreground">
            {current.providers.map((p) => (
              <span key={p.connector}>
                {providerName[p.connector] ?? p.connector}: {p.status === "failed" ? <span className="text-red-700 dark:text-red-400">last read failed ({p.error})</span> : `${p.links_seen.toLocaleString()} links read ${ago(p.last_run_at)}`}
              </span>
            ))}
          </p>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <Tabs value={section} onValueChange={(v) => setSection(v as Section)}>
              <TabsList className="h-8">
                <TabsTrigger value="links" className="px-3 text-[12px]">Links</TabsTrigger>
                <TabsTrigger value="changes" className="px-3 text-[12px]">Changes</TabsTrigger>
              </TabsList>
            </Tabs>
            {current.spam.disavowed > 0 && <Button variant="ghost" onClick={download}>Download disavow file ({current.spam.disavowed.toLocaleString()})</Button>}
          </div>
          {section === "links" && (
            <div className="space-y-3">
              <FilterBar summary={current} filters={filters} setFilters={setFilters} search={search} setSearch={setSearch} />
              {!current.traffic && (
                <p className="text-[12px] text-muted-foreground">
                  {ahrefs ? (
                    <>Traffic columns fill in once the Ahrefs connection reads link traffic (about 25 more Ahrefs units a link a week). <button className="underline underline-offset-2 hover:text-foreground" onClick={() => go("seo")}>Turn it on in Rankings & backlinks settings</button>.</>
                  ) : (
                    "Traffic for linking pages comes from Ahrefs; DataForSEO does not give it."
                  )}
                </p>
              )}
              <LinkTable ws={ws} brand={brand} links={links} total={total} filters={filters} onReview={review} />
              {next !== null && links && (
                <div className="flex justify-center">
                  <Button variant="ghost" onClick={loadMore} disabled={more}>{more ? "Loading…" : `Show more (${(total - links.length).toLocaleString()} left)`}</Button>
                </div>
              )}
            </div>
          )}
          {section === "changes" && <Changes events={events} />}
        </>
      )}
    </div>
  );
}

// Segmented is a row of buttons where one is chosen, like Ahrefs' All /
// Dofollow / Nofollow.
function Segmented<T extends string>({ value, options, onChange, label }: { value: T; options: [T, string][]; onChange: (v: T) => void; label: string }) {
  return (
    <div role="radiogroup" aria-label={label} className="inline-flex h-8 overflow-hidden rounded-md border text-[13px] shadow-xs">
      {options.map(([v, text]) => (
        <button
          key={v}
          type="button"
          role="radio"
          aria-checked={value === v}
          onClick={() => onChange(v)}
          className={`border-r px-2.5 last:border-r-0 ${value === v ? "bg-brand/15 font-medium text-foreground" : "text-muted-foreground hover:bg-muted hover:text-foreground"}`}
        >
          {text}
        </button>
      ))}
    </div>
  );
}

function FilterBar({ summary, filters: f, setFilters, search, setSearch }: {
  summary: BacklinkSiteSummary;
  filters: Filters;
  setFilters: (p: Partial<Filters>) => void;
  search: string;
  setSearch: (s: string) => void;
}) {
  const changed = (Object.keys(defaults) as (keyof Filters)[]).some((k) => f[k] !== defaults[k]);
  return (
    <div className="space-y-2 rounded-lg border bg-card p-3">
      <div className="flex flex-wrap items-center gap-2">
        <Segmented label="Link attribute" value={f.follow} onChange={(follow) => setFilters({ follow })} options={[["", "All"], ["dofollow", "Dofollow"], ["nofollow", "Nofollow"], ["ugc", "UGC"], ["sponsored", "Sponsored"]]} />
        <select className={field} aria-label="Backlink type" value={f.type} onChange={(e) => setFilters({ type: e.target.value })}>
          <option value="">Any backlink type</option>
          <option value="content">In content</option>
          {Object.keys(linkTypeLabel).filter((t) => t !== "content").map((t) => {
            const n = summary.link_types.find((x) => x.value === t)?.links;
            return <option key={t} value={t}>{linkTypeLabel[t]}{n ? ` (${n.toLocaleString()})` : ""}</option>;
          })}
        </select>
        <select className={field} aria-label="DR" value={f.dr} onChange={(e) => setFilters({ dr: Number(e.target.value) })}>
          <option value={0}>Any DR</option>
          {[10, 20, 30, 40, 50, 60, 70, 80].map((n) => <option key={n} value={n}>DR {n}+</option>)}
        </select>
        <select className={field} aria-label="Domain traffic" value={f.traffic} onChange={(e) => setFilters({ traffic: Number(e.target.value) })} disabled={!summary.traffic && !f.traffic} title={summary.traffic ? undefined : "No traffic read yet"}>
          <option value={0}>Any domain traffic</option>
          {[100, 1000, 10000, 100000, 1000000].map((n) => <option key={n} value={n}>Domain traffic {num(n)}+</option>)}
        </select>
        {(summary.page_types.length > 0 || f.pageType) && (
          <select className={field} aria-label="Page type" value={f.pageType} onChange={(e) => setFilters({ pageType: e.target.value })}>
            <option value="">Any page type</option>
            {summary.page_types.map((p) => <option key={p.value} value={p.value}>{p.value.replaceAll("_", " ")} ({p.links.toLocaleString()})</option>)}
          </select>
        )}
        <select className={field} aria-label="Spam" value={f.spam} onChange={(e) => setFilters({ spam: e.target.value as Filters["spam"] })}>
          <option value="">Any spam score</option>
          <option value="toxic">Toxic</option>
          <option value="suspect">Suspect</option>
          <option value="clean">Clean</option>
        </select>
        <div className="flex min-w-0 grow sm:grow-0">
          <input className={`${field} min-w-0 grow rounded-r-none sm:w-56`} type="search" placeholder="Word or phrase" aria-label="Search links" value={search} onChange={(e) => setSearch(e.target.value)} />
          <select className={`${field} -ml-px rounded-l-none`} aria-label="Search in" value={f.qIn} onChange={(e) => setFilters({ qIn: e.target.value as Filters["qIn"] })}>
            <option value="">Everywhere</option>
            <option value="source_url">Ref. page URL</option>
            <option value="title">Ref. page title</option>
            <option value="anchor">Anchor</option>
            <option value="target_url">Target URL</option>
          </select>
        </div>
        {changed && <Button variant="ghost" onClick={() => { setSearch(""); setFilters(defaults); }}>Reset</Button>}
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <select className={field} aria-label="Grouping" value={f.group} onChange={(e) => setFilters({ group: e.target.value as Filters["group"] })}>
          <option value="domain">One link per domain</option>
          <option value="page">One link per page</option>
          <option value="all">All links</option>
        </select>
        <Segmented label="Show" value={f.change} onChange={(change) => setFilters({ change })} options={[["", "All"], ["new", "New"], ["lost", "Lost"]]} />
        <select className={field} aria-label="History" value={f.history} onChange={(e) => setFilters({ history: Number(e.target.value) })}>
          {histories.map(([d, label]) => <option key={d} value={d}>{f.change ? label : `Show history: ${label.toLowerCase()}`}</option>)}
        </select>
        <select className={field} aria-label="Sort" value={f.order} onChange={(e) => setFilters({ order: e.target.value })}>
          {orders.map(([v, label]) => <option key={v} value={v}>Sort: {label}</option>)}
        </select>
      </div>
    </div>
  );
}

// Url shows a whole URL as a link, its host in bold, so it can be read and
// opened.
function Url({ href, className = "" }: { href: string; className?: string }) {
  let scheme = "", host = href, rest = "";
  try {
    const u = new URL(href);
    scheme = `${u.protocol}//`;
    host = u.host;
    rest = href.slice(scheme.length + u.host.length);
  } catch {
    /* shown as it is */
  }
  return (
    <a className={`break-all text-emerald-700 underline-offset-2 hover:underline dark:text-emerald-400 ${className}`} href={href} target="_blank" rel="noreferrer noopener nofollow">
      {scheme}<span className="font-semibold">{host}</span>{rest}
    </a>
  );
}

function Tag({ children, tone = "grey" }: { children: ReactNode; tone?: Tone }) {
  return <Badge tone={tone}>{children}</Badge>;
}

const reasonLabel: Record<string, string> = {
  removedfromhtml: "link removed from the page",
  notfound: "page gone",
  pageerror: "page failing",
  pageredirected: "page redirects",
  noindex: "page set to noindex",
  notcanonical: "page no longer canonical",
  lostredirect: "redirect removed",
};

function lostReason(l: Backlink) {
  for (const v of Object.values(l.providers) as { lost_reason?: string }[]) if (v.lost_reason) return reasonLabel[v.lost_reason] ?? v.lost_reason;
  return "";
}

function LinkTable({ ws, brand, links, total, filters, onReview }: {
  ws: string;
  brand: object;
  links: Backlink[] | null;
  total: number;
  filters: Filters;
  onReview: (l: Backlink, r: Review, domain?: boolean) => void;
}) {
  const [open, setOpen] = useState<Record<string, Backlink[] | "loading">>({});
  useEffect(() => setOpen({}), [links]);
  if (!links) return <Loading />;
  const since = filters.history > 0 ? dayISO(filters.history) : "";
  const noun = filters.group === "domain" ? "domains" : filters.group === "page" ? "pages" : "links";
  // Expanding a group lists the other links from that domain or page.
  const expand = async (l: Backlink) => {
    if (open[l.id]) return setOpen(({ [l.id]: _, ...rest }) => rest);
    setOpen((o) => ({ ...o, [l.id]: "loading" }));
    const q = { ...toQuery({ ...filters, group: "all", order: "url_rating" }, l.site_id, brand), limit: 200, source_domain: l.source_domain, ...(filters.group === "page" ? { q: l.source_url, q_in: "source_url" as const } : {}) };
    const { data } = await api.GET("/v1/workspaces/{ws}/backlinks", { params: { path: { ws }, query: q } });
    setOpen((o) => ({ ...o, [l.id]: (data?.data ?? []).filter((x) => x.id !== l.id) }));
  };

  const spam = (l: Backlink) =>
    l.spam_level ? (
      <span className="flex flex-wrap items-center gap-1" title={l.spam_factors.length ? `Spam score ${l.spam_score}, because of ${l.spam_factors.map((f) => factorLabel[f.code] ?? f.code).join(", ")}` : "Nothing looks like spam"}>
        {l.spam_level !== "clean" && <Badge tone={spamTone[l.spam_level]}>Spam {l.spam_score}</Badge>}
        {l.review === "disavow" && <Badge tone="red">Disavow</Badge>}
        {l.review === "ok" && <Badge tone="green">Kept</Badge>}
      </span>
    ) : null;
  const actions = (l: Backlink) => (
    <Menu
      label="Review"
      aria={`Review the link from ${l.source_domain}`}
      items={[
        l.review !== "ok" && { label: "Keep: it's a real link", onClick: () => onReview(l, "ok") },
        l.review !== "disavow" && { label: "Disavow this link", onClick: () => onReview(l, "disavow") },
        { label: `Disavow every link from ${l.source_domain}`, onClick: () => onReview(l, "disavow", true) },
        l.review !== "" && { label: "Clear the review", onClick: () => onReview(l, "none") },
      ]}
    />
  );
  const referring = (l: Backlink) => (
    <div className="min-w-0 space-y-1">
      {l.title && <a className="block break-words text-blue-700 underline-offset-2 hover:underline dark:text-blue-400" href={l.source_url} target="_blank" rel="noreferrer noopener nofollow">{l.title}</a>}
      <Url href={l.source_url} className="block text-[12px]" />
      <div className="flex flex-wrap gap-1">
        {l.languages.slice(0, 2).map((x) => <Tag key={x}>{x.toUpperCase()}</Tag>)}
        {l.platforms.slice(0, 2).map((x) => <Tag key={x}>{x}</Tag>)}
        {spam(l)}
      </div>
      {l.author && <div className="text-[12px] text-muted-foreground">By {l.author}</div>}
    </div>
  );
  const kind = (l: Backlink) => (
    <div className="flex flex-wrap gap-1">
      {l.page_types.slice(0, 2).map((p) => <Tag key={p} tone="amber">{leaf(p)}</Tag>)}
      {l.page_categories.slice(0, 2).map((p) => <Tag key={p}>{leaf(p)}</Tag>)}
      {l.page_types.length + l.page_categories.length === 0 && <span className="text-muted-foreground">–</span>}
    </div>
  );
  const target = (l: Backlink) => {
    const isNew = l.first_seen_at && since && l.first_seen_at.slice(0, 10) >= since;
    return (
      <div className="min-w-0 space-y-1">
        {l.anchor ? <div className="break-words">{l.anchor}</div> : <div className="text-muted-foreground italic">No anchor text</div>}
        <div className="flex flex-wrap gap-1">
          {l.link_type && l.link_type !== "text" && <Tag>{linkTypeLabel[l.link_type]?.toUpperCase() ?? l.link_type.toUpperCase()}</Tag>}
          {l.in_content && <Tag>CONTENT</Tag>}
          {l.nofollow && <Tag>NOFOLLOW</Tag>}
          {l.ugc && <Tag>UGC</Tag>}
          {l.sponsored && <Tag>SPONSORED</Tag>}
        </div>
        <Url href={l.target_url} className="block text-[12px]" />
        {l.hops.map((h, i) => (
          <div key={i} className="flex flex-wrap items-baseline gap-1 pl-3 text-[12px]">
            <span className="text-muted-foreground" aria-hidden>↳</span>
            <Tag tone="amber">{h.kind === "canonical" ? "CANONICAL" : h.kind === "redirect" ? "REDIRECT" : h.kind}</Tag>
            <span className="text-muted-foreground" aria-hidden>→</span>
            <Url href={h.url} />
          </div>
        ))}
        {l.status === "lost" ? (
          <Tag tone="red">Lost{lostReason(l) ? `: ${lostReason(l)}` : ""}</Tag>
        ) : isNew ? (
          <Tag tone="green">New: link added</Tag>
        ) : null}
      </div>
    );
  };
  const seen = (l: Backlink) => (
    <div className="space-y-0.5 whitespace-nowrap">
      <div title="First seen">{date(l.first_seen_at) || "–"}</div>
      <div className="text-muted-foreground" title="Last seen">{l.status === "live" ? ago(l.last_seen_at ?? l.checked_at) : date(l.last_seen_at)}</div>
      {l.status === "lost" && <div className="text-red-700 dark:text-red-400" title="Lost">{date(l.lost_at)}</div>}
    </div>
  );
  const group = (l: Backlink) =>
    l.group_links > 1 ? (
      <button className={`${field} inline-flex items-center gap-1 tabular-nums`} aria-expanded={!!open[l.id]} onClick={() => expand(l)} title={`Show the other links from this ${filters.group}`}>
        {l.group_links} <span aria-hidden>{open[l.id] ? "▴" : "▾"}</span>
      </button>
    ) : null;
  const n = (v: number | null) => <span className="tabular-nums">{num(v)}</span>;

  const row = (l: Backlink, child = false) => (
    <tr key={l.id} className={`align-top ${child ? "bg-muted/40" : ""}`}>
      <td className={`max-w-80 py-3 pr-3 ${child ? "pl-6" : "pl-3"}`}>{referring(l)}</td>
      <td className="max-w-48 py-3 pr-3">{kind(l)}</td>
      <td className="py-3 pr-3 text-right">{n(l.source_rating)}</td>
      <td className="py-3 pr-3 text-right">{n(l.url_rating)}</td>
      <td className="py-3 pr-3 text-right">{n(l.domain_traffic)}</td>
      <td className="py-3 pr-3 text-right">{n(l.page_referring_domains)}</td>
      <td className="py-3 pr-3 text-right">{n(l.linked_domains)}</td>
      <td className="py-3 pr-3 text-right">{n(l.external_links)}</td>
      <td className="py-3 pr-3 text-right">{n(l.page_traffic)}</td>
      <td className="py-3 pr-3 text-right">{n(l.page_keywords)}</td>
      <td className="max-w-80 py-3 pr-3">{target(l)}</td>
      <td className="py-3 pr-3">{seen(l)}</td>
      <td className="py-3 pr-3 text-right">{!child && group(l)}</td>
      <td className="py-3 pr-3 text-right">{actions(l)}</td>
    </tr>
  );
  const head = (label: ReactNode, title?: string, right = true) => <th scope="col" title={title} className={`py-2 pr-3 align-bottom font-medium ${right ? "text-right" : "text-left"}`}>{label}</th>;

  return (
    <div className="space-y-2">
      <p className="text-[13px]">
        <span className="font-semibold">{total.toLocaleString()}</span>{" "}
        {filters.group === "all" ? (total === 1 ? "link" : "links") : `${total === 1 ? noun.slice(0, -1) : noun}, one link each`}
      </p>
      {links.length === 0 ? (
        <p className="rounded-lg border bg-card px-4 py-6 text-center text-[13px] text-muted-foreground">
          {filters.change === "lost" ? "No links lost in this range. Nice." : filters.change === "new" ? "No new links in this range yet." : "No links match these filters."}
        </p>
      ) : (
        <>
          {/* Phones get one card per link. */}
          <ul className="space-y-2 md:hidden">
            {links.map((l) => (
              <li key={l.id} className="space-y-2 rounded-lg border bg-card p-3 text-[13px]">
                {referring(l)}
                <div className="flex flex-wrap gap-x-3 gap-y-1 text-[12px] text-muted-foreground tabular-nums">
                  <span>DR {num(l.source_rating)}</span>
                  <span>UR {num(l.url_rating)}</span>
                  {l.domain_traffic !== null && <span>Traffic {num(l.domain_traffic)}</span>}
                  {l.page_keywords !== null && <span>Kw {num(l.page_keywords)}</span>}
                </div>
                {target(l)}
                <div className="flex items-end justify-between gap-2 text-[12px]">
                  {seen(l)}
                  <div className="flex items-center gap-2">{group(l)}{actions(l)}</div>
                </div>
                {Array.isArray(open[l.id]) && (open[l.id] as Backlink[]).map((c) => (
                  <div key={c.id} className="space-y-1 border-t pt-2">{target(c)}<Url href={c.source_url} className="text-[12px]" /></div>
                ))}
              </li>
            ))}
          </ul>
          <div className="hidden overflow-x-auto rounded-lg border bg-card md:block">
            <table className="w-full text-[13px]">
              <thead className="border-b text-[12px] text-muted-foreground">
                <tr>
                  <th scope="col" className="py-2 pr-3 pl-3 text-left align-bottom font-medium">Referring page</th>
                  {head("Page type", "What kind of page links, and its topics", false)}
                  {head("DR", "Domain Rating: how strong the linking domain is, 0 to 100")}
                  {head("UR", "URL Rating: how strong the linking page is, 0 to 100")}
                  {head(<>Domain<br />traffic</>, "The linking domain's estimated monthly organic visits")}
                  {head(<>Ref.<br />domains</>, "Domains linking to the linking page")}
                  {head(<>Linked<br />domains</>, "Domains the linking page links to")}
                  {head("Ext.", "Links from the linking page to other sites")}
                  {head(<>Page<br />traffic</>, "The linking page's estimated monthly organic visits")}
                  {head("Kw.", "Keywords the linking page ranks for in the top 100")}
                  {head("Anchor and target URL", undefined, false)}
                  {head(<>First seen<br />Last seen</>, undefined, false)}
                  {head("Links", "Links in this group")}
                  <th scope="col" className="py-2 pr-3"><span className="sr-only">Review</span></th>
                </tr>
              </thead>
              <tbody className="divide-y">
                {links.map((l) => [
                  row(l),
                  open[l.id] === "loading" && <tr key={`${l.id}-loading`}><td colSpan={14} className="px-6 py-2 text-[12px] text-muted-foreground">Loading the other links…</td></tr>,
                  ...(Array.isArray(open[l.id]) ? (open[l.id] as Backlink[]).map((c) => row(c, true)) : []),
                ])}
              </tbody>
            </table>
          </div>
        </>
      )}
    </div>
  );
}

// Weeks charts links gained (up) and lost (down) in each of the last 12 weeks.
function Weeks({ weeks }: { weeks: BacklinkSiteSummary["weeks"] }) {
  const max = Math.max(1, ...weeks.map((w) => Math.max(w.new + w.regained, w.lost)));
  if (weeks.every((w) => w.new + w.regained + w.lost === 0)) return null;
  const W = 600, H = 80, mid = H / 2, bw = W / weeks.length;
  return (
    <figure className="space-y-1">
      <svg viewBox={`0 0 ${W} ${H}`} className="h-20 w-full" role="img" aria-label="Links gained and lost by week">
        <line x1={0} x2={W} y1={mid} y2={mid} stroke="var(--border)" />
        {weeks.map((w, i) => {
          const up = ((w.new + w.regained) / max) * (mid - 2), down = (w.lost / max) * (mid - 2);
          return (
            <g key={w.week}>
              <title>{`Week of ${w.week}: +${w.new + w.regained}, −${w.lost}`}</title>
              {up > 0 && <rect x={i * bw + bw * 0.2} width={bw * 0.6} y={mid - up} height={up} rx={2} fill="var(--series-3)" />}
              {down > 0 && <rect x={i * bw + bw * 0.2} width={bw * 0.6} y={mid} height={down} rx={2} fill="var(--series-2)" />}
            </g>
          );
        })}
      </svg>
      <figcaption className="text-[12px] text-muted-foreground">Links gained (above) and lost (below), each of the last 12 weeks.</figcaption>
    </figure>
  );
}

function Changes({ events }: { events: BacklinkEvent[] | null }) {
  const byDay = useMemo(() => {
    const out: [string, BacklinkEvent[]][] = [];
    for (const e of events ?? []) {
      const d = new Date(e.at).toLocaleDateString(undefined, { weekday: "short", month: "short", day: "numeric" });
      if (out.at(-1)?.[0] === d) out.at(-1)![1].push(e);
      else out.push([d, [e]]);
    }
    return out;
  }, [events]);
  if (!events) return <Loading />;
  if (events.length === 0) return <p className="text-[13px] text-muted-foreground">No links gained or lost in the last 90 days. The first read of a site is the baseline, so changes show from the next week on.</p>;
  return (
    <div className="space-y-4">
      {byDay.map(([d, list]) => (
        <section key={d} className="space-y-1.5">
          <h3 className="font-mono text-[11px] tracking-wide text-muted-foreground uppercase">{d}</h3>
          <ul className="divide-y rounded-lg border bg-card">
            {list.map((e) => (
              <li key={e.id} className="space-y-1 px-3 py-2 text-[13px]">
                <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
                  <Badge tone={kindTone[e.kind]}>{kindLabel[e.kind]}</Badge>
                  <Url href={e.backlink.source_url} />
                  {e.source_rating !== null && <span className="font-mono text-[12px] text-muted-foreground tabular-nums">DR {num(e.source_rating)}</span>}
                  {e.reason && <span className="text-[12px] text-muted-foreground">{reasonLabel[e.reason] ?? e.reason}</span>}
                </div>
                <div className="flex flex-wrap items-baseline gap-x-1 text-[12px] text-muted-foreground">
                  to <Url href={e.backlink.target_url} />
                </div>
              </li>
            ))}
          </ul>
        </section>
      ))}
    </div>
  );
}
