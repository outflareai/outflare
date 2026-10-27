import { useEffect, useMemo, useState } from "react";
import type { SEOCompetitor, SEOCompetitorEvent, SEOKeywordGap, SEOLinkGap, SEOSite } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { DataTable } from "@/components/data-table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
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
const providerName: Record<string, string> = { ahrefs: "Ahrefs", dataforseo: "DataForSEO" };
const num = (v: number | null | undefined) => (v === null || v === undefined ? "–" : v.toLocaleString(undefined, { maximumFractionDigits: 0 }));
const path = (u: string) => {
  try {
    return new URL(u).pathname;
  } catch {
    return u;
  }
};

export function Competitors({ ws, go }: Props) {
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
  const brandKey = useBrand(ws).query.brand ?? "";
  const brand = brandKey ? { brand: brandKey } : {};

  useEffect(() => {
    setSites(null);
    api.GET("/v1/workspaces/{ws}/seo-sites", { params: { path: { ws }, query: brand } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setError("");
      setSites(data.data);
      setSite((s) => (data.data.some((x) => x.id === s) ? s : (data.data[0]?.id ?? "")));
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brandKey]);

  useEffect(() => {
    if (!site) return;
    api.GET("/v1/workspaces/{ws}/seo-competitors", { params: { path: { ws }, query: { ...brand, site_id: site, limit: 500 } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setList(data.data);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brandKey, site, reload]);

  useEffect(() => {
    if (!site || section !== "changes") return;
    setEvents(null);
    api.GET("/v1/workspaces/{ws}/seo-competitors/events", { params: { path: { ws }, query: { ...brand, site_id: site, limit: 500 } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setEvents(data.data);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brandKey, site, section]);

  useEffect(() => {
    if (!site || (section !== "keywords" && section !== "links")) return;
    const kind = section;
    (kind === "keywords" ? setKeywordGaps : setLinkGaps)(null);
    api.GET("/v1/workspaces/{ws}/seo-competitors/gaps", { params: { path: { ws }, query: { site_id: site, kind, limit: 500 } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      const base = { total: data.total, competitors: data.competitors };
      if (kind === "keywords") setKeywordGaps({ ...base, data: data.data as SEOKeywordGap[], mine: data.your_keywords ?? 0 });
      else setLinkGaps({ ...base, data: data.data as SEOLinkGap[], mine: data.your_domains ?? 0 });
    });
  }, [ws, site, section]);

  const add = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!host.trim()) return;
    setBusy("add");
    const { error } = await api.POST("/v1/workspaces/{ws}/seo-competitors", { params: { path: { ws } }, body: { site_id: site, host: host.trim() } });
    setBusy("");
    if (error) return setError(errorMessage(error));
    setError("");
    setHost("");
    toast("Tracking it. OutflareBot takes a first look now; numbers come with the next SEO snapshot.");
    setReload((n) => n + 1);
  };
  const setStatus = async (c: SEOCompetitor, status: "tracked" | "dismissed") => {
    setBusy(c.id);
    const { error } = await api.PATCH("/v1/workspaces/{ws}/seo-competitors/{competitor}", { params: { path: { ws, competitor: c.id } }, body: { status } });
    setBusy("");
    if (error) return setError(errorMessage(error));
    setError("");
    setReload((n) => n + 1);
  };
  const check = async (c: SEOCompetitor) => {
    const { error } = await api.POST("/v1/workspaces/{ws}/seo-competitors/{competitor}/check", { params: { path: { ws, competitor: c.id } } });
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
    const { error } = await api.PATCH("/v1/workspaces/{ws}/seo-competitors/{competitor}", { params: { path: { ws, competitor: c.id } }, body: { name, aliases: aliases.slice(0, 10) } });
    if (error) return setError(errorMessage(error));
    setError("");
    setReload((n) => n + 1);
  };
  const remove = async (c: SEOCompetitor) => {
    if (!(await confirmAction(`Remove ${c.host}?`, "Its pages and changes go with it. Your SEO connection may suggest it again; dismiss it instead to keep it away.", "Remove"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/seo-competitors/{competitor}", { params: { path: { ws, competitor: c.id } } });
    if (error) return setError(errorMessage(error));
    setReload((n) => n + 1);
  };

  const current = sites?.find((s) => s.id === site);
  const header = (
    <PageHeader
      title="Competitors"
      description="Who you compete with in search. Your SEO connection suggests them; track the ones that matter to follow their numbers and what they publish. Agents use list_seo_competitors and list_competitor_events."
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
  const shown = list.filter((c) => (show === "dismissed" ? c.status === "dismissed" : c.status !== "dismissed"));
  const dismissed = list.length - list.filter((c) => c.status !== "dismissed").length;
  return (
    <div className="space-y-6">
      {header}
      {error && <ErrorText>{error}</ErrorText>}
      <form onSubmit={add} className="flex flex-wrap items-center gap-2">
        <input className={`${field} w-64 max-w-full`} aria-label="Competitor's domain" placeholder="rival.example" value={host} onChange={(e) => setHost(e.target.value)} maxLength={300} />
        <Button type="submit" disabled={!host.trim() || busy === "add" || tracked >= 20}>Track competitor</Button>
        <span className="text-[12px] text-muted-foreground">{tracked} of 20 tracked for {current.host}</span>
      </form>
      {list.length === 0 ? (
        <Empty title="No competitors yet" action={<Button variant="ghost" onClick={() => go("connectors")}>Connect Ahrefs or DataForSEO</Button>}>
          Add one above, or connect Ahrefs or DataForSEO with your own account: each week it suggests the domains that rank for the same keywords as {current.host}.
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
                  <option value="active">Tracked and suggested</option>
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
          {section === "keywords" && <KeywordGaps gaps={keywordGaps} host={current.host} />}
          {section === "links" && <LinkGaps gaps={linkGaps} host={current.host} />}
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

function KeywordGaps({ gaps, host }: { gaps: Gaps<SEOKeywordGap> | null; host: string }) {
  if (!gaps) return <Loading />;
  if (gaps.data.length === 0) return noGaps(gaps, "keyword gaps");
  const rank = (p: number | null | undefined) => (p == null ? "–" : `#${num(p)}`);
  return (
    <div className="space-y-2">
      <GapNote gaps={gaps} what="keywords" />
      <DataTable
        data={gaps.data}
        rowId={(g) => g.keyword}
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

function LinkGaps({ gaps, host }: { gaps: Gaps<SEOLinkGap> | null; host: string }) {
  if (!gaps) return <Loading />;
  if (gaps.data.length === 0) return noGaps(gaps, "link gaps");
  return (
    <div className="space-y-2">
      <GapNote gaps={gaps} what="linking domains" />
      <DataTable
        data={gaps.data}
        rowId={(g) => g.domain}
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
              <a className="font-medium whitespace-nowrap underline-offset-2 hover:underline" href={`https://${row.original.domain}/`} target="_blank" rel="noreferrer noopener nofollow">{row.original.domain}</a>
            ),
          },
          { id: "rating", header: "Rating", accessorFn: (g) => g.rating ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => num(row.original.rating) },
          { id: "them", header: "Links to", accessorFn: (g) => g.competitors.length, cell: ({ row }) => <span className="text-[12px]">{row.original.competitors.join(", ")}</span> },
        ]}
      />
    </div>
  );
}
