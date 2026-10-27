import { useEffect, useMemo, useState } from "react";
import type { Backlink, BacklinkEvent, BacklinkSiteSummary } from "@outflare/sdk";
import { useBrand } from "@/lib/brand";
import { DataTable } from "@/components/data-table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api, errorMessage } from "../api";
import { Badge, Button, Empty, ErrorText, field, Loading, Menu, PageHeader, Stat, Stats, toast, type tones } from "../ui";
import { ago } from "../format";
import type { Tab } from "../App";

// Backlinks: the links other sites have to the workspace's SEO sites,
// merged from Ahrefs and DataForSEO, and the feed of links gained, lost and
// regained. It reads only what the weekly SEO snapshot stored, so opening
// it costs nothing.

type Props = { ws: string; go: (tab: Tab, item?: string) => void };
type Tone = keyof typeof tones;
type Section = "links" | "changes";
type Kind = BacklinkEvent["kind"];
type Spam = "" | "toxic" | "suspect" | "clean";
type Review = "ok" | "disavow" | "none";

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
const rating = (v: number | null) => (v === null ? "–" : v.toLocaleString(undefined, { maximumFractionDigits: 0 }));
const path = (u: string) => {
  try {
    const x = new URL(u);
    return x.pathname + x.search;
  } catch {
    return u;
  }
};

export function Backlinks({ ws, go }: Props) {
  const [sites, setSites] = useState<BacklinkSiteSummary[] | null>(null);
  const [site, setSite] = useState("");
  const [section, setSection] = useState<Section>("links");
  const [status, setStatus] = useState<"live" | "lost" | "">("live");
  const [spam, setSpam] = useState<Spam>("");
  const [reload, setReload] = useState(0);
  const [links, setLinks] = useState<Backlink[] | null>(null);
  const [total, setTotal] = useState(0);
  const [events, setEvents] = useState<BacklinkEvent[] | null>(null);
  const [error, setError] = useState("");
  const brandKey = useBrand(ws).query.brand ?? "";
  const brand = brandKey ? { brand: brandKey } : {};

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

  useEffect(() => {
    if (!site) return;
    setLinks(null);
    const query = { ...brand, site_id: site, limit: 500, ...(status ? { status } : {}), ...(spam ? { spam, order: "spam" as const } : {}) };
    api.GET("/v1/workspaces/{ws}/backlinks", { params: { path: { ws }, query } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setLinks(data.data);
      setTotal(data.total);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brandKey, site, status, spam, reload]);

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
      description="The links other sites have to yours, merged from Ahrefs and DataForSEO on your own accounts, and which ones you gained, lost and got back. Agents use list_backlinks and list_backlink_events."
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
          {section === "links" && <LinkTable links={links} total={total} status={status} setStatus={setStatus} spam={spam} setSpam={setSpam} onReview={review} />}
          {section === "changes" && <Changes events={events} />}
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

function LinkTable({ links, total, status, setStatus, spam, setSpam, onReview }: {
  links: Backlink[] | null;
  total: number;
  status: string;
  setStatus: (s: "live" | "lost" | "") => void;
  spam: Spam;
  setSpam: (s: Spam) => void;
  onReview: (l: Backlink, r: Review, domain?: boolean) => void;
}) {
  const toolbar = (
    <>
      <select className={field} aria-label="Status" value={status} onChange={(e) => setStatus(e.target.value as "live" | "lost" | "")}>
        <option value="live">Live</option>
        <option value="lost">Lost</option>
        <option value="">Live and lost</option>
      </select>
      <select className={field} aria-label="Spam" value={spam} onChange={(e) => setSpam(e.target.value as Spam)}>
        <option value="">Any spam score</option>
        <option value="toxic">Toxic</option>
        <option value="suspect">Suspect</option>
        <option value="clean">Clean</option>
      </select>
    </>
  );
  const spamCell = (l: Backlink) =>
    l.spam_level ? (
      <span className="flex flex-wrap items-center gap-1" title={l.spam_factors.length ? `Because of ${l.spam_factors.map((f) => factorLabel[f.code] ?? f.code).join(", ")}` : "Nothing looks like spam"}>
        <Badge tone={spamTone[l.spam_level]}>{l.spam_score}</Badge>
        {l.review === "disavow" && <Badge tone="red">Disavow</Badge>}
        {l.review === "ok" && <Badge tone="green">Kept</Badge>}
      </span>
    ) : (
      "–"
    );
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
  if (!links) return <Loading />;
  return (
    <div className="space-y-2">
      <DataTable
        data={links}
        rowId={(l) => l.id}
        searchPlaceholder="Search links"
        toolbar={toolbar}
        empty={status === "lost" ? "No lost links. Nice." : "No links here."}
        mobile={(l) => (
          <div className="space-y-1">
            <div className="flex items-start justify-between gap-2">
              <span className="font-medium break-all">{l.source_domain}</span>
              <span className="font-mono text-xs tabular-nums">{rating(l.source_rating)}</span>
            </div>
            <div className="text-xs break-all text-muted-foreground">to {path(l.target_url)}{l.anchor && ` · "${l.anchor}"`}</div>
            <div className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
              <span className="flex items-center gap-1">Spam {spamCell(l)}</span>
              {actions(l)}
            </div>
          </div>
        )}
        columns={[
          {
            id: "from",
            header: "From",
            accessorFn: (l) => `${l.source_domain} ${l.source_url} ${l.anchor}`,
            cell: ({ row: { original: l } }) => (
              <div className="min-w-0 space-y-0.5">
                <a className="font-medium break-all underline-offset-2 hover:underline" href={l.source_url} target="_blank" rel="noreferrer noopener nofollow">{l.source_domain}</a>
                <div className="text-xs break-all text-muted-foreground">{l.anchor ? `"${l.anchor}"` : "No anchor text"}</div>
              </div>
            ),
          },
          { id: "to", header: "To", accessorFn: (l) => l.target_url, cell: ({ row }) => <span className="font-mono text-[12px] break-all">{path(row.original.target_url)}</span> },
          { id: "rating", header: "Rating", accessorFn: (l) => l.source_rating ?? -1, meta: { align: "right", mono: true }, cell: ({ row }) => rating(row.original.source_rating) },
          {
            id: "type",
            header: "Type",
            accessorFn: (l) => (l.nofollow ? 1 : 0),
            cell: ({ row: { original: l } }) => (
              <div className="flex flex-wrap gap-1">
                <Badge tone={l.nofollow ? "grey" : "green"}>{l.nofollow ? "Nofollow" : "Followed"}</Badge>
                {l.sponsored && <Badge>Sponsored</Badge>}
                {l.ugc && <Badge>UGC</Badge>}
              </div>
            ),
          },
          {
            id: "status",
            header: "Status",
            accessorFn: (l) => l.status,
            cell: ({ row: { original: l } }) =>
              l.status === "lost" ? <Badge tone="red">Lost {l.lost_at ? ago(l.lost_at) : ""}</Badge> : <span className="text-muted-foreground">{l.first_seen_at ? `Since ${new Date(l.first_seen_at).toLocaleDateString(undefined, { month: "short", year: "numeric" })}` : "Live"}</span>,
          },
          { id: "spam", header: "Spam", accessorFn: (l) => l.spam_score ?? -1, meta: { align: "right" }, cell: ({ row }) => spamCell(row.original) },
          { id: "seen", header: "Seen by", accessorFn: (l) => Object.keys(l.providers).join(" "), cell: ({ row }) => <span className="text-muted-foreground">{Object.keys(row.original.providers).map((p) => providerName[p] ?? p).join(", ")}</span> },
          { id: "actions", header: () => <span className="sr-only">Actions</span>, enableSorting: false, meta: { align: "right" }, cell: ({ row }) => actions(row.original) },
        ]}
      />
      {total > links.length && <p className="text-[12px] text-muted-foreground">Showing the strongest {links.length.toLocaleString()} of {total.toLocaleString()}. Agents read every one with list_backlinks.</p>}
    </div>
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
              <li key={e.id} className="flex flex-wrap items-baseline gap-x-3 gap-y-1 px-3 py-2 text-[13px]">
                <Badge tone={kindTone[e.kind]}>{kindLabel[e.kind]}</Badge>
                <a className="font-medium break-all underline-offset-2 hover:underline" href={e.backlink.source_url} target="_blank" rel="noreferrer noopener nofollow">{e.backlink.source_domain}</a>
                <span className="text-muted-foreground">to <span className="font-mono text-[12px]">{path(e.backlink.target_url)}</span></span>
                {e.source_rating !== null && <span className="font-mono text-[12px] text-muted-foreground tabular-nums">rating {rating(e.source_rating)}</span>}
                {e.reason && <span className="text-[12px] text-muted-foreground">{reasonLabel[e.reason] ?? e.reason}</span>}
              </li>
            ))}
          </ul>
        </section>
      ))}
    </div>
  );
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
