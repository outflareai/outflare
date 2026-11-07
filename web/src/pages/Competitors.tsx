import { PickBrand } from "@/components/pick-brand";
import { useEffect, useMemo, useState } from "react";
import type { SEOCompetitor, SEOCompetitorEvent, SEOKeywordGap, SEOKeywordResults, SEOLinkGap, SEOLinkGapPages, SEOSite } from "@outflare/sdk";
import { pickedBrandId, useBrand } from "@/lib/brand";
import { DataTable } from "@/components/data-table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { api, errorMessage } from "../api";
import { ask, Badge, Button, confirmAction, Empty, ErrorText, field, Loading, Menu, PageHeader, toast, type tones } from "../ui";
import { ago } from "../format";
import type { Tab } from "../App";

// Competitors: the sites each SEO site competes with in search. The SEO
// connection suggests them each week; tracked ones get numbers from that
// connection and a light weekly look from OutflareBot (robots.txt, sitemaps
// and home page only), and what changed is the feed under Changes. Gaps
// compares the stored keywords and linking domains of up to five tracked
// competitors with the site's own, as ideas only: nothing reaches out.

type Props = { ws: string; go: (tab: Tab, item?: string) => void };
type Tone = keyof typeof tones;
type Section = "competitors" | "changes" | "keywords" | "links";
type Gaps<T> = { data: T[]; total: number; competitors: string[]; mine: number };
type Kind = SEOCompetitorEvent["kind"];
type Show = "active" | "dismissed";

const kindLabel: Record<Kind, string> = {
  pages_added: "New pages",
  pages_removed: "Pages removed",
  title_changed: "New title",
  description_changed: "New description",
  rating_changed: "Domain rating",
  traffic_changed: "Traffic",
  keywords_changed: "Keywords",
  links_changed: "Referring domains",
};
const kindTone: Partial<Record<Kind, Tone>> = { pages_added: "blue", pages_removed: "amber" };
const statusTone: Record<SEOCompetitor["status"], Tone> = { tracked: "green", suggested: "blue", dismissed: "grey" };
const statusLabel: Record<SEOCompetitor["status"], string> = { tracked: "Tracked", suggested: "Suggested", dismissed: "Dismissed" };
const providerName: Record<string, string> = { ahrefs: "Ahrefs", dataforseo: "DataForSEO", seranking: "SE Ranking" };
const num = (v: number | null | undefined) => (v === null || v === undefined ? "–" : v.toLocaleString(undefined, { maximumFractionDigits: 0 }));
const path = (u: string) => {
  try {
    return new URL(u).pathname;
  } catch {
    return u;
  }
};

// Competitors belong to one brand.
export function Competitors(props: Props) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="Competitors" brands={active.length} />;
  return <CompetitorsPage key={brand.id} {...props} />;
}

function CompetitorsPage({ ws, go }: Props) {
  const [sites, setSites] = useState<SEOSite[] | null>(null);
  const [site, setSite] = useState("");
  const [section, setSection] = useState<Section>("competitors");
  const [show, setShow] = useState<Show>("active");
  const [list, setList] = useState<SEOCompetitor[] | null>(null);
  const [events, setEvents] = useState<SEOCompetitorEvent[] | null>(null);
  const [keywordGaps, setKeywordGaps] = useState<Gaps<SEOKeywordGap> | null>(null);
  const [linkGaps, setLinkGaps] = useState<Gaps<SEOLinkGap> | null>(null);
  const [host, setHost] = useState("");
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);
  const [opened, setOpened] = useState("");
  const [openedDomain, setOpenedDomain] = useState("");
  const brandState = useBrand(ws);
  const brandKey = brandState.query.brand ?? "";
  const brand = brandKey ? { brand: brandKey } : {};

  useEffect(() => {
    setSites(null);
    api.GET("/v1/brands/{brand}/seo-sites", { params: { path: { brand: pickedBrandId() } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setError("");
      setSites(data.data);
      setSite((s) => (data.data.some((x) => x.id === s) ? s : (data.data[0]?.id ?? "")));
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brandKey]);

  useEffect(() => {
    if (!site) return;
    api.GET("/v1/brands/{brand}/seo-competitors", { params: { path: { brand: pickedBrandId() }, query: { ...brand, site_id: site, limit: 500 } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setList(data.data);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brandKey, site, reload]);

  useEffect(() => {
    if (!site || section !== "changes") return;
    setEvents(null);
    api.GET("/v1/brands/{brand}/seo-competitors/events", { params: { path: { brand: pickedBrandId() }, query: { ...brand, site_id: site, limit: 500 } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setEvents(data.data);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brandKey, site, section]);

  useEffect(() => {
    if (!site || (section !== "keywords" && section !== "links")) return;
    const kind = section;
    (kind === "keywords" ? setKeywordGaps : setLinkGaps)(null);
    api.GET("/v1/brands/{brand}/seo-competitors/gaps", { params: { path: { brand: pickedBrandId() }, query: { site_id: site, kind, limit: 500 } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      const base = { total: data.total, competitors: data.competitors };
      if (kind === "keywords") setKeywordGaps({ ...base, data: data.data as SEOKeywordGap[], mine: data.your_keywords ?? 0 });
      else setLinkGaps({ ...base, data: data.data as SEOLinkGap[], mine: data.your_domains ?? 0 });
    });
  }, [ws, site, section]);

  // Add one or several domains at once (separated by commas, spaces or new lines).
  const add = async (e: React.FormEvent) => {
    e.preventDefault();
    const hosts = [...new Set(host.split(/[\s,;]+/).map((h) => h.trim()).filter(Boolean))];
    if (!hosts.length) return;
    setBusy("add");
    let added = 0;
    const failed: string[] = [];
    let lastError = "";
    for (const h of hosts) {
      const { error } = await api.POST("/v1/brands/{brand}/seo-competitors", { params: { path: { brand: pickedBrandId() } }, body: { site_id: site, host: h } });
      if (error) {
        failed.push(h);
        lastError = errorMessage(error);
      } else added++;
    }
    setBusy("");
    setError(failed.length ? `${failed.join(", ")}: ${lastError}` : "");
    setHost(failed.join(", "));
    if (added) toast(added === 1 ? "Tracking it. OutflareBot takes a first look now; numbers come with the next SEO snapshot." : `Tracking ${added} competitors.`);
    setReload((n) => n + 1);
  };
  const setStatus = async (c: SEOCompetitor, status: "tracked" | "dismissed") => {
    setList((l) => l?.map((x) => (x.id === c.id ? { ...x, status } : x)) ?? l);
    const { error } = await api.PATCH("/v1/brands/{brand}/seo-competitors/{competitor}", { params: { path: { brand: pickedBrandId(), competitor: c.id } }, body: { status } });
    if (error) {
      setError(errorMessage(error));
      return setReload((n) => n + 1);
    }
    setError("");
  };
  const trackAll = async (list: SEOCompetitor[]) => {
    setBusy("all");
    for (const c of list) await setStatus(c, "tracked");
    setBusy("");
    toast(`Tracking ${list.length} competitors.`);
    setReload((n) => n + 1);
  };
  const check = async (c: SEOCompetitor) => {
    const { error } = await api.POST("/v1/brands/{brand}/seo-competitors/{competitor}/check", { params: { path: { brand: pickedBrandId(), competitor: c.id } } });
    if (error) return setError(errorMessage(error));
    toast(`Looking at ${c.host} now.`);
  };
  const rename = async (c: SEOCompetitor) => {
    const r = await ask({
      title: `Name ${c.host}`,
      body: "Its name first, then any other names it goes by, separated by commas. AI answers often use a short name.",
      input: { label: "Names", placeholder: "Summit Gear, Summit", value: [c.name, ...c.aliases].filter(Boolean).join(", "), maxLength: 1100, optional: true },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Save", value: "ok" }],
    });
    if (r?.choice !== "ok") return;
    const [name = "", ...aliases] = r.value.split(",").map((v) => v.trim()).filter(Boolean);
    const { error } = await api.PATCH("/v1/brands/{brand}/seo-competitors/{competitor}", { params: { path: { brand: pickedBrandId(), competitor: c.id } }, body: { name, aliases: aliases.slice(0, 10) } });
    if (error) return setError(errorMessage(error));
    setError("");
    setReload((n) => n + 1);
  };
  // A website AI visibility only guessed is wrong: say No to that website
  // and drop the name, so the brand waits for its real website instead.
  const wrongSite = async (c: SEOCompetitor) => {
    const { error } = await api.PATCH("/v1/brands/{brand}/seo-competitors/{competitor}", { params: { path: { brand: pickedBrandId(), competitor: c.id } }, body: { status: "dismissed", name: "" } });
    if (error) return setError(errorMessage(error));
    setError("");
    toast(`${c.host} won't be suggested again.`);
    setReload((n) => n + 1);
  };
  const remove = async (c: SEOCompetitor) => {
    if (!(await confirmAction(`Remove ${c.host}?`, "Its pages and changes go with it. Your SEO connection may suggest it again; dismiss it instead to keep it away.", "Remove"))) return;
    const { error } = await api.DELETE("/v1/brands/{brand}/seo-competitors/{competitor}", { params: { path: { brand: pickedBrandId(), competitor: c.id } } });
    if (error) return setError(errorMessage(error));
    setReload((n) => n + 1);
  };

  // Find more competitors: read the results pages of the top keyword gaps
  // (or of one), and suggest the domains that rank across several.
  const discover = async (keywords?: string[]) => {
    const brandID = current?.brand_id ?? pickedBrandId();
    if (!brandID) return;
    setBusy(keywords ? "read" : "discover");
    const { data, error } = await api.POST("/v1/brands/{brand}/competition/discover", { params: { path: { brand: brandID } }, body: keywords ? { keywords } : {} });
    setBusy("");
    if (error || !data) return setError(errorMessage(error));
    setError("");
    if (keywords) return toast("Reading its results now. They show here in a few seconds.");
    toast("Reading who ranks for your top keyword gaps. New suggestions show up here in a minute or two.");
    for (const ms of [45000, 120000]) setTimeout(() => setReload((n) => n + 1), ms);
  };
  const track = async (host: string) => {
    const { error } = await api.POST("/v1/brands/{brand}/seo-competitors", { params: { path: { brand: pickedBrandId() } }, body: { site_id: site, host } });
    if (error) return setError(errorMessage(error));
    setError("");
    toast(`Tracking ${host}.`);
    setReload((n) => n + 1);
  };

  const current = sites?.find((s) => s.id === site);
  const header = (
    <PageHeader
      title="Competitors"
      description="Your brand's competitors, one list used everywhere: Presence scans them, AI visibility counts them and agents read them. New ones are suggested every day; say yes or no. Agents use list_seo_competitors and list_competitor_events."
      action={
        sites && sites.length > 1 ? (
          <select className={field} aria-label="Site" value={site} onChange={(e) => (setList(null), setSite(e.target.value))}>
            {sites.map((s) => <option key={s.id} value={s.id}>{s.host}</option>)}
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
          Competitors are kept for the websites you audit. Add yours under Site audits, then connect Ahrefs or DataForSEO for suggestions.
        </Empty>
      </div>
    );
  if (!current || !list) return <div className="space-y-6">{header}<Loading /></div>;

  const tracked = list.filter((c) => c.status === "tracked").length;
  const suggested = list.filter((c) => c.status === "suggested");
  const shown = list.filter((c) => c.status === (show === "dismissed" ? "dismissed" : "tracked"));
  const dismissed = list.filter((c) => c.status === "dismissed").length;
  return (
    <div className="space-y-6">
      {header}
      <LinkGapPages ws={ws} site={site} domain={openedDomain} onClose={() => setOpenedDomain("")} />
      <KeywordResults
        ws={ws}
        site={site}
        keyword={opened}
        reload={reload}
        busy={busy}
        onClose={() => setOpened("")}
        onRead={(k) => discover([k]).then(() => setTimeout(() => setReload((n) => n + 1), 15000))}
        onTrack={track}
        onStatus={(id, status) => {
          const c = list.find((x) => x.id === id);
          if (c) setStatus(c, status).then(() => setReload((n) => n + 1));
        }}
      />
      {error && <ErrorText>{error}</ErrorText>}
      <form onSubmit={add} className="flex flex-wrap items-center gap-2">
        <input className={`${field} w-96 max-w-full`} aria-label="Competitors' domains" placeholder="rival.example, other.example" value={host} onChange={(e) => setHost(e.target.value)} maxLength={5000} />
        <Button type="submit" disabled={!host.trim() || busy === "add"}>{busy === "add" ? "Adding…" : "Add"}</Button>
        <span className="text-[12px] text-muted-foreground">{tracked} tracked for {current.host}. Add one or several, separated by commas.</span>
        {tracked > 0 && (
          <Button variant="ghost" className="ml-auto" disabled={busy === "discover"} onClick={() => discover()} title="Reads who ranks for your top keyword gaps on your own SERP connection, and suggests the sites that rank for several of them">
            {busy === "discover" ? "Looking…" : "Find more competitors"}
          </Button>
        )}
      </form>
      <Suggestions list={suggested} busy={busy} onStatus={setStatus} onTrackAll={() => trackAll(suggested)} onWrongSite={wrongSite} />
      {list.length === 0 ? (
        <Empty title="No competitors yet" action={<Button variant="ghost" onClick={() => go("connectors")}>Connect Ahrefs or DataForSEO</Button>}>
          Add some above, or connect Ahrefs or DataForSEO with your own account: each day it suggests the domains that rank for the same keywords as {current.host}.
        </Empty>
      ) : (
        <>
          <Tabs value={section} onValueChange={(v) => setSection(v as Section)}>
            <TabsList className="h-8">
              <TabsTrigger value="competitors" className="px-3 text-[12px]">Competitors</TabsTrigger>
              <TabsTrigger value="changes" className="px-3 text-[12px]">Changes</TabsTrigger>
              <TabsTrigger value="keywords" className="px-3 text-[12px]">Keyword gaps</TabsTrigger>
              <TabsTrigger value="links" className="px-3 text-[12px]">Link gaps</TabsTrigger>
            </TabsList>
          </Tabs>
          {section === "competitors" && (
            <CompetitorTable
              list={shown}
              busy={busy}
              toolbar={
                <select className={field} aria-label="Show" value={show} onChange={(e) => setShow(e.target.value as Show)}>
                  <option value="active">Tracked ({tracked})</option>
                  <option value="dismissed">Dismissed ({dismissed})</option>
                </select>
              }
              onStatus={setStatus}
              onCheck={check}
              onRename={rename}
              onRemove={remove}
            />
          )}
          {section === "changes" && <Changes events={events} />}
          {section === "keywords" && <KeywordGaps gaps={keywordGaps} host={current.host} onOpen={setOpened} />}
          {section === "links" && <LinkGaps gaps={linkGaps} host={current.host} onOpen={setOpenedDomain} />}
        </>
      )}
    </div>
  );
}

function CompetitorTable({ list, busy, toolbar, onStatus, onCheck, onRename, onRemove }: {
  list: SEOCompetitor[];
  busy: string;
  toolbar: React.ReactNode;
  onStatus: (c: SEOCompetitor, s: "tracked" | "dismissed") => void;
  onCheck: (c: SEOCompetitor) => void;
  onRename: (c: SEOCompetitor) => void;
  onRemove: (c: SEOCompetitor) => void;
}) {
  const actions = (c: SEOCompetitor) => (
    <div className="flex items-center justify-end gap-1">
      {c.status !== "tracked" && <Button variant="ghost" disabled={busy === c.id} onClick={() => onStatus(c, "tracked")}>Track</Button>}
      <Menu
        aria={`More for ${c.host}`}
        items={[
          c.status === "tracked" && { label: "Look at the site now", onClick: () => onCheck(c) },
          { label: c.name ? "Rename" : "Name it", onClick: () => onRename(c) },
          c.status !== "dismissed" && { label: "Dismiss", onClick: () => onStatus(c, "dismissed") },
          { label: "Remove", onClick: () => onRemove(c), danger: true },
        ]}
      />
    </div>
  );
  const m = (c: SEOCompetitor) => c.numbers?.metrics;
  return (
    <DataTable
      data={list}
      rowId={(c) => c.id}
      searchPlaceholder="Search competitors"
      toolbar={toolbar}
      empty="Nothing here."
      mobile={(c) => (
        <div className="space-y-1">
          <div className="flex items-start justify-between gap-2">
            <span className="font-medium break-all">{c.name || c.host}</span>
            <Badge tone={statusTone[c.status]}>{statusLabel[c.status]}</Badge>
          </div>
          <div className="text-xs text-muted-foreground">
            {[
              c.common_keywords !== null && `${num(c.common_keywords)} shared keywords`,
              m(c)?.domain_rating != null && `rating ${num(m(c)?.domain_rating)}`,
              m(c)?.organic_traffic != null && `traffic ${num(m(c)?.organic_traffic)}`,
            ].filter(Boolean).join(" · ")}
          </div>
          {actions(c)}
        </div>
      )}
      columns={[
        {
          id: "host",
          header: "Competitor",
          accessorFn: (c) => `${c.host} ${c.name} ${c.aliases.join(" ")} ${c.title}`,
          cell: ({ row: { original: c } }) => (
            <div className="min-w-0 space-y-0.5">
              {c.name && <div className="font-medium" title={c.aliases.length ? `Also ${c.aliases.join(", ")}` : undefined}>{c.name}</div>}
              <a className="font-medium whitespace-nowrap underline-offset-2 hover:underline" href={`https://${c.host}/`} target="_blank" rel="noreferrer noopener nofollow">{c.host}</a>
              {c.title && <div className="max-w-64 truncate text-xs text-muted-foreground" title={c.title}>{c.title}</div>}
              {c.check_error && <div className="text-xs text-amber-700 dark:text-amber-400">{c.check_error}</div>}
            </div>
          ),
        },
        { id: "status", header: "Status", accessorFn: (c) => c.status, cell: ({ row: { original: c } }) => <Badge tone={statusTone[c.status]}>{statusLabel[c.status]}</Badge> },
        { id: "shared", header: "Shared keywords", accessorFn: (c) => c.common_keywords ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => num(row.original.common_keywords) },
        {
          id: "rating",
          header: "Rating",
          accessorFn: (c) => m(c)?.domain_rating ?? -1,
          meta: { align: "right", mono: true },
          cell: ({ row: { original: c } }) => <span title={c.numbers ? `${providerName[c.numbers.connector] ?? c.numbers.connector}, ${c.numbers.day}` : undefined}>{num(m(c)?.domain_rating)}</span>,
        },
        { id: "traffic", header: "Traffic", accessorFn: (c) => m(c)?.organic_traffic ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => num(m(row.original)?.organic_traffic) },
        { id: "keywords", header: "Keywords", accessorFn: (c) => m(c)?.organic_keywords ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => num(m(row.original)?.organic_keywords) },
        { id: "domains", header: "Domains", accessorFn: (c) => m(c)?.referring_domains ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => num(m(row.original)?.referring_domains) },
        { id: "pages", header: "Pages", accessorFn: (c) => c.pages, meta: { align: "right", mono: true }, cell: ({ row: { original: c } }) => (c.checked_at ? num(c.pages) : "–") },
        { id: "actions", header: () => <span className="sr-only">Actions</span>, enableSorting: false, meta: { align: "right" }, cell: ({ row }) => actions(row.original) },
      ]}
    />
  );
}

function change(e: SEOCompetitorEvent) {
  const d = e.data;
  if (e.kind === "pages_added" || e.kind === "pages_removed") return `${num(d.count)} page${d.count === 1 ? "" : "s"}`;
  if (typeof d.before === "number" && typeof d.after === "number") {
    const diff = d.after - d.before;
    return `${num(d.before)} → ${num(d.after)} (${diff > 0 ? "+" : "−"}${num(Math.abs(diff))})`;
  }
  return `"${d.after ?? ""}"`;
}

function Changes({ events }: { events: SEOCompetitorEvent[] | null }) {
  const byDay = useMemo(() => {
    const out: [string, SEOCompetitorEvent[]][] = [];
    for (const e of events ?? []) {
      const d = new Date(e.at).toLocaleDateString(undefined, { weekday: "short", month: "short", day: "numeric" });
      if (out.at(-1)?.[0] === d) out.at(-1)![1].push(e);
      else out.push([d, [e]]);
    }
    return out;
  }, [events]);
  if (!events) return <Loading />;
  if (events.length === 0) return <p className="text-[13px] text-muted-foreground">Nothing changed at your tracked competitors in the last 90 days. The first look and the first snapshot are the baseline, so changes show from the next week on.</p>;
  return (
    <div className="space-y-4">
      {byDay.map(([d, list]) => (
        <section key={d} className="space-y-1.5">
          <h3 className="font-mono text-[11px] tracking-wide text-muted-foreground uppercase">{d}</h3>
          <ul className="divide-y rounded-lg border bg-card">
            {list.map((e) => (
              <li key={e.id} className="space-y-1 px-3 py-2 text-[13px]">
                <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
                  <Badge tone={kindTone[e.kind] ?? "grey"}>{kindLabel[e.kind]}</Badge>
                  <span className="font-medium break-all">{e.host}</span>
                  <span className="text-muted-foreground">{change(e)}</span>
                  {e.connector && <span className="text-[12px] text-muted-foreground">{providerName[e.connector] ?? e.connector}</span>}
                  <span className="text-[12px] text-muted-foreground">{ago(e.at)}</span>
                </div>
                {e.data.urls && e.data.urls.length > 0 && (
                  <ul className="space-y-0.5 pl-1">
                    {e.data.urls.map((u) => (
                      <li key={u} className="font-mono text-[12px] break-all text-muted-foreground">
                        {e.kind === "pages_added" ? <a className="underline-offset-2 hover:underline" href={u} target="_blank" rel="noreferrer noopener nofollow">{path(u)}</a> : path(u)}
                      </li>
                    ))}
                  </ul>
                )}
              </li>
            ))}
          </ul>
        </section>
      ))}
    </div>
  );
}

function GapNote({ gaps, what }: { gaps: Gaps<unknown>; what: string }) {
  return (
    <p className="text-[12px] text-muted-foreground">
      {gaps.total.toLocaleString()} {what} from {gaps.competitors.join(", ")}, compared with the {gaps.mine.toLocaleString()} your SEO connection read for you. Ideas only: Outflare never contacts anyone for you.
      {what === "keywords" && " Click a keyword to see everyone who ranks for it."}
    </p>
  );
}

function noGaps(gaps: Gaps<unknown>, what: string) {
  if (gaps.competitors.length > 0) return <p className="text-[13px] text-muted-foreground">No {what}: you already have what your tracked competitors have.</p>;
  return (
    <p className="text-[13px] text-muted-foreground">
      Track a competitor to see gaps. The next weekly SEO snapshot reads the keywords and linking domains of your first five tracked competitors.
    </p>
  );
}

function KeywordGaps({ gaps, host, onOpen }: { gaps: Gaps<SEOKeywordGap> | null; host: string; onOpen: (keyword: string) => void }) {
  if (!gaps) return <Loading />;
  if (gaps.data.length === 0) return noGaps(gaps, "keyword gaps");
  const rank = (p: number | null | undefined) => (p == null ? "–" : `#${num(p)}`);
  return (
    <div className="space-y-2">
      <GapNote gaps={gaps} what="keywords" />
      <DataTable
        data={gaps.data}
        rowId={(g) => g.keyword}
        onRowClick={(g) => onOpen(g.keyword)}
        searchPlaceholder="Search keywords"
        empty="Nothing here."
        mobile={(g) => (
          <div className="space-y-1">
            <div className="flex items-start justify-between gap-2">
              <span className="font-medium break-all">{g.keyword}</span>
              <Badge tone={g.kind === "missing" ? "blue" : "amber"}>{g.kind === "missing" ? "Missing" : "Behind"}</Badge>
            </div>
            <div className="text-xs text-muted-foreground">
              {[`${num(g.volume)} searches`, `best ${rank(g.best_position)}`, g.you && `${host} ${rank(g.you.position)}`].filter(Boolean).join(" · ")}
            </div>
          </div>
        )}
        columns={[
          { id: "keyword", header: "Keyword", accessorFn: (g) => g.keyword, cell: ({ row }) => <span className="font-medium">{row.original.keyword}</span> },
          {
            id: "kind",
            header: "Gap",
            accessorFn: (g) => g.kind,
            cell: ({ row: { original: g } }) => <Badge tone={g.kind === "missing" ? "blue" : "amber"}>{g.kind === "missing" ? "Missing" : "Behind"}</Badge>,
          },
          { id: "volume", header: "Searches", accessorFn: (g) => g.volume ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => num(row.original.volume) },
          {
            id: "them",
            header: "Competitors",
            accessorFn: (g) => g.competitors.length,
            cell: ({ row: { original: g } }) => (
              <div className="space-y-0.5 text-[12px]">
                {g.competitors.map((c) => (
                  <div key={c.host} className="whitespace-nowrap">
                    <span className="font-mono">{rank(c.position)}</span>{" "}
                    <a className="underline-offset-2 hover:underline" href={c.url} target="_blank" rel="noreferrer noopener nofollow" title={c.url}>{c.host}</a>
                  </div>
                ))}
              </div>
            ),
          },
          { id: "you", header: "You", accessorFn: (g) => g.you?.position ?? 999, meta: { align: "right", mono: true }, cell: ({ row }) => rank(row.original.you?.position) },
        ]}
      />
    </div>
  );
}

function LinkGaps({ gaps, host, onOpen }: { gaps: Gaps<SEOLinkGap> | null; host: string; onOpen: (domain: string) => void }) {
  if (!gaps) return <Loading />;
  if (gaps.data.length === 0) return noGaps(gaps, "link gaps");
  return (
    <div className="space-y-2">
      <GapNote gaps={gaps} what="linking domains" />
      <DataTable
        data={gaps.data}
        rowId={(g) => g.domain}
        onRowClick={(g) => onOpen(g.domain)}
        searchPlaceholder="Search domains"
        empty="Nothing here."
        mobile={(g) => (
          <div className="space-y-1">
            <span className="font-medium break-all">{g.domain}</span>
            <div className="text-xs text-muted-foreground">
              rating {num(g.rating)} · links to {g.competitors.join(", ")}, not {host}
            </div>
          </div>
        )}
        columns={[
          {
            id: "domain",
            header: "Linking domain",
            accessorFn: (g) => g.domain,
            cell: ({ row }) => (
              <span className="font-medium whitespace-nowrap">{row.original.domain}</span>
            ),
          },
          { id: "rating", header: "Rating", accessorFn: (g) => g.rating ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => num(row.original.rating) },
          { id: "them", header: "Links to", accessorFn: (g) => g.competitors.length, cell: ({ row }) => <span className="text-[12px]">{row.original.competitors.join(", ")}</span> },
        ]}
      />
    </div>
  );
}

// Suggestions to say yes or no to: from the SEO connection (sharing
// keywords), from rankings (ranking beside the site) or from AI
// visibility (named next to the brand in AI answers).
function Suggestions({ list, busy, onStatus, onTrackAll, onWrongSite }: {
  list: SEOCompetitor[];
  busy: string;
  onStatus: (c: SEOCompetitor, s: "tracked" | "dismissed") => void;
  onTrackAll: () => void;
  // onWrongSite takes off a suggestion whose website AI visibility only
  // guessed, so the brand waits on the AI competitors page for its real one.
  onWrongSite?: (c: SEOCompetitor) => void;
}) {
  if (!list.length) return null;
  const why = (c: SEOCompetitor) =>
    c.source === "ai_visibility"
      ? "Named next to you in AI answers"
      : c.source === "ai_visibility_unsure"
      ? `Named next to you in AI answers. Is ${c.host} their website?`
      : c.source === "rankings"
      ? `Ranks beside you for ${num(c.common_keywords)} of your keywords`
      : c.source === "keyword_gaps"
        ? `Ranks for ${num(c.common_keywords)} of your keyword gaps`
      : c.common_keywords !== null
        ? `Shares ${num(c.common_keywords)} keywords with you${providerName[c.source] ? ` (${providerName[c.source]})` : ""}`
        : "Suggested";
  return (
    <section className="max-w-3xl space-y-2 rounded-xl border p-4">
      <div className="flex flex-wrap items-center gap-3">
        <h2 className="font-medium">Are these competitors? · {list.length}</h2>
        <Button variant="ghost" disabled={busy === "all"} onClick={onTrackAll}>Yes to all</Button>
      </div>
      <div className="divide-y text-sm">
        {list.map((c) => (
          <div key={c.id} className="flex items-center gap-3 py-2">
            <div className="flex shrink-0 gap-1">
              <Button disabled={busy === "all"} onClick={() => onStatus(c, "tracked")} aria-label={`Yes, track ${c.host}`}>Yes</Button>
              <Button variant="ghost" disabled={busy === "all"} onClick={() => onStatus(c, "dismissed")} aria-label={`No, ${c.host} is not a competitor`}>No</Button>
              {c.source === "ai_visibility_unsure" && onWrongSite && (
                <Button variant="ghost" disabled={busy === "all"} onClick={() => onWrongSite(c)} aria-label={`${c.host} is not ${c.name || "their"} website`}>Wrong website</Button>
              )}
            </div>
            <div className="min-w-0 flex-1">
              <a className="font-medium underline-offset-2 hover:underline" href={`https://${c.host}/`} target="_blank" rel="noreferrer noopener nofollow">{c.name || c.host}</a>
              {c.name && <span className="text-muted-foreground"> {c.host}</span>}
              <div className="text-xs text-muted-foreground">
                {[why(c), c.numbers?.metrics?.domain_rating != null && `rating ${num(c.numbers.metrics.domain_rating)}`, c.title].filter(Boolean).join(" · ")}
              </div>
            </div>
          </div>
        ))}
      </div>
    </section>
  );
}

// KeywordResults: one keyword's stored results page, everyone who ranks,
// competitor or not, with Yes/No for suggestions and Track for the rest.
// LinkGapPages: one link gap opened. The exact pages on that site linking
// to each competitor, from links already read, or read now on the
// workspace's own SEO connection after showing what it costs.
function LinkGapPages({ ws, site, domain, onClose }: { ws: string; site: string; domain: string; onClose: () => void }) {
  const [data, setData] = useState<SEOLinkGapPages | null>(null);
  const [error, setError] = useState("");
  const [cost, setCost] = useState<{ estimate: number; unit: string; left: number | null } | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    setData(null);
    setCost(null);
    setError("");
    if (!domain) return;
    api.GET("/v1/brands/{brand}/seo-competitors/gaps/links", { params: { path: { brand: pickedBrandId() }, query: { site_id: site, domain } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setData(data);
    });
  }, [ws, site, domain]);
  const estimate = async () => {
    setBusy(true);
    const { data, error } = await api.POST("/v1/brands/{brand}/seo-competitors/gaps/links", { params: { path: { brand: pickedBrandId() } }, body: { site_id: site, domain, estimate_only: true } });
    setBusy(false);
    if (error || !data || !("estimate" in data)) return setError(errorMessage(error));
    setError("");
    const b = data.budget;
    setCost({ estimate: data.estimate, unit: b.unit, left: b.unlimited || !b.limit ? null : Math.max(0, b.limit - b.spent) });
  };
  const read = async () => {
    setBusy(true);
    const { data, error } = await api.POST("/v1/brands/{brand}/seo-competitors/gaps/links", { params: { path: { brand: pickedBrandId() } }, body: { site_id: site, domain } });
    setBusy(false);
    setCost(null);
    if (error || !data || !("site_id" in data)) return setError(errorMessage(error));
    setError("");
    setData(data);
  };
  const amount = (v: number, unit: string) => (unit === "USD" ? `US$${v.toFixed(2)}` : `${num(v)} API units`);
  const unread = data?.competitors.filter((c) => !c.read_at).length ?? 0;
  return (
    <Sheet open={!!domain} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>{domain}</SheetTitle>
          <SheetDescription>The pages on this site that link to each competitor.</SheetDescription>
        </SheetHeader>
        <div className="space-y-4 px-4 pb-6">
          {error && <ErrorText>{error}</ErrorText>}
          {!data && !error && <Loading />}
          {data?.competitors.map((c) => (
            <section key={c.competitor_id} className="space-y-1">
              <h3 className="text-[13px] font-medium">{c.host} · {c.pages.length} {c.pages.length === 1 ? "page" : "pages"}</h3>
              {c.pages.length === 0 && (
                <p className="text-[12px] text-muted-foreground">{c.read_at ? "No live links from this site were found." : "Not read yet: only the site is known."}</p>
              )}
              <ul className="divide-y text-[13px]">
                {c.pages.map((p) => (
                  <li key={`${p.source_url}>${p.target_url}`} className="space-y-0.5 py-2">
                    <a className="font-medium break-all underline-offset-2 hover:underline" href={p.source_url} target="_blank" rel="noreferrer noopener nofollow">{p.title || p.source_url}</a>
                    <div className="break-all text-xs text-muted-foreground">{p.source_url}</div>
                    <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                      <span className="break-all">to {path(p.target_url)}</span>
                      {p.anchor && <span>· “{p.anchor}”</span>}
                      {p.nofollow && <Badge>nofollow</Badge>}
                    </div>
                  </li>
                ))}
              </ul>
            </section>
          ))}
          {data && (
            <div className="space-y-2 border-t pt-3 text-[13px]">
              {!cost && (
                <Button variant={unread ? "primary" : "ghost"} disabled={busy} onClick={estimate}>
                  {busy ? "Checking…" : unread ? "Read their links from this site" : "Read again"}
                </Button>
              )}
              {cost && (
                <div className="space-y-2 rounded-md border p-3">
                  <p>
                    Reads up to 100 links from {domain} to {data.competitors.length === 1 ? "this competitor" : `each of these ${data.competitors.length} competitors`} on your own SEO connection, for about {amount(cost.estimate, cost.unit)}
                    {cost.left !== null ? ` (${amount(cost.left, cost.unit)} left this month)` : ""}. A copy under a day old costs nothing.
                  </p>
                  <div className="flex gap-2">
                    <Button disabled={busy} onClick={read}>{busy ? "Reading…" : "Read now"}</Button>
                    <Button variant="ghost" disabled={busy} onClick={() => setCost(null)}>Cancel</Button>
                  </div>
                </div>
              )}
            </div>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}

function KeywordResults({ ws, site, keyword, reload, busy, onClose, onRead, onTrack, onStatus }: {
  ws: string;
  site: string;
  keyword: string;
  reload: number;
  busy: string;
  onClose: () => void;
  onRead: (keyword: string) => void;
  onTrack: (host: string) => void;
  onStatus: (id: string, status: "tracked" | "dismissed") => void;
}) {
  const [page, setPage] = useState<SEOKeywordResults | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!keyword) return;
    api.GET("/v1/brands/{brand}/seo-competitors/results", { params: { path: { brand: pickedBrandId() }, query: { site_id: site, keyword } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setError("");
      setPage(data);
    });
  }, [ws, site, keyword, reload]);
  useEffect(() => setPage(null), [keyword]);
  const label: Record<string, string> = { tracked: "Tracked", suggested: "Suggested", dismissed: "Not a competitor" };
  return (
    <Sheet open={!!keyword} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>{keyword}</SheetTitle>
          <SheetDescription>
            {page?.read_at
              ? `Who ranks on ${page.engine === "google" ? "Google" : page.engine} (${page.country}), read ${ago(page.read_at)}${page.source === "rank_tracking" ? " by rank tracking, top 10" : ", top 20"}.`
              : "Who ranks for it, competitor or not."}
          </SheetDescription>
        </SheetHeader>
        <div className="space-y-3 px-4 pb-6">
          {error && <ErrorText>{error}</ErrorText>}
          {!page && !error && <Loading />}
          {page && !page.read_at && (
            <div className="space-y-2 text-[13px] text-muted-foreground">
              <p>Not read yet. The top keyword gaps are read once a day on your own SERP connection (DataForSEO or Geonode); this one can be read now.</p>
              <Button disabled={busy === "read"} onClick={() => onRead(keyword)}>{busy === "read" ? "Reading…" : "Read its results now"}</Button>
            </div>
          )}
          {page?.read_at && (
            <ol className="divide-y text-[13px]">
              {page.items.map((it) => (
                <li key={`${it.position}-${it.url}`} className="flex items-start gap-3 py-2">
                  <span className="w-7 shrink-0 pt-0.5 text-right font-mono text-muted-foreground">#{it.position}</span>
                  <div className="min-w-0 flex-1">
                    <a className="font-medium break-words underline-offset-2 hover:underline" href={it.url} target="_blank" rel="noreferrer noopener nofollow">{it.title || it.url}</a>
                    <div className="truncate text-xs text-muted-foreground" title={it.url}>{it.domain}{path(it.url) !== "/" ? path(it.url) : ""}</div>
                  </div>
                  <div className="flex shrink-0 items-center gap-1">
                    {it.you ? (
                      <Badge tone="green">You</Badge>
                    ) : it.competitor?.status === "suggested" ? (
                      <>
                        <Button onClick={() => onStatus(it.competitor!.id, "tracked")} aria-label={`Yes, track ${it.domain}`}>Yes</Button>
                        <Button variant="ghost" onClick={() => onStatus(it.competitor!.id, "dismissed")} aria-label={`No, ${it.domain} is not a competitor`}>No</Button>
                      </>
                    ) : it.competitor ? (
                      <Badge tone={it.competitor.status === "tracked" ? "blue" : "grey"}>{label[it.competitor.status]}</Badge>
                    ) : (
                      <Button variant="ghost" onClick={() => onTrack(it.domain)} aria-label={`Track ${it.domain}`}>Track</Button>
                    )}
                  </div>
                </li>
              ))}
            </ol>
          )}
          {page?.stale && <p className="text-[12px] text-muted-foreground">Some results were Google redirect links and are left out. Read again for the full page.</p>}
          {page?.read_at && (
            <Button variant="ghost" disabled={busy === "read"} onClick={() => onRead(keyword)}>{busy === "read" ? "Reading…" : "Read again"}</Button>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}
