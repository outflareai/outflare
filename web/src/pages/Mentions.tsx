import { useCallback, useEffect, useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import type { Mention, MentionsOverview } from "@outflare/sdk";
import { PickBrand } from "@/components/pick-brand";
import { DataTable } from "@/components/data-table";
import { PlatformIcon } from "@/components/platform-icon";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useBrand } from "@/lib/brand";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, CopyButton, Empty, ErrorText, field, Loading, PageHeader, Stat, Stats, Status, toast, type tones } from "../ui";
import { ago } from "../format";
import type { Tab } from "../App";

// Mentions: who names the brand or its tracked competitors in videos,
// podcasts, posts, forums and news. Three views (the mentions, the people
// behind them, and where it listens), and a page per mention.

type Props = { ws: string; canManage: boolean; item?: string; onOpen: (id: string) => void; go: (tab: Tab, item?: string) => void };
type Tone = keyof typeof tones;
type View = "mentions" | "creators" | "sources";
type MentionStatus = Mention["status"];
type Source = Mention["source"];

const sourceName: Record<Source, string> = {
  youtube: "YouTube", podcast: "Podcast", x: "X", reddit: "Reddit", hacker_news: "Hacker News", bluesky: "Bluesky", news: "News", web: "Forum or community",
};
const statusName: Record<MentionStatus, string> = { new: "New", contacted: "Contacted", replied: "Replied", on_board: "On board", not_a_fit: "Not a fit" };
const statusTone: Record<MentionStatus, Tone> = { new: "blue", contacted: "amber", replied: "green", on_board: "green", not_a_fit: "grey" };
const audience = (s: Source) => (s === "podcast" ? "listeners" : s === "youtube" ? "views" : "followers");

// Letter tiles for sources with no logo of their own.
const tiles: Partial<Record<string, [string, string]>> = {
  x: ["X", "bg-black text-white"], reddit: ["R", "bg-[#FF4500] text-white"], podcast: ["P", "bg-purple-600 text-white"],
  hacker_news: ["Y", "bg-[#FF6600] text-white"], bluesky: ["B", "bg-[#1185FE] text-white"], news: ["N", "bg-slate-600 text-white"], web: ["W", "bg-slate-500 text-white"],
};
function SourceIcon({ source }: { source: string }) {
  const t = tiles[source];
  if (!t) return <PlatformIcon kind={source} />;
  return <span aria-hidden className={`inline-flex size-5 shrink-0 items-center justify-center rounded-[5px] text-[10px] font-bold ${t[1]}`}>{t[0]}</span>;
}

const short = (v: number) => (v >= 1_000_000 ? `${(v / 1_000_000).toFixed(1)}M` : v >= 1000 ? `${(v / 1000).toFixed(v >= 10_000 ? 0 : 1)}k` : String(v));
const at = (s: number) => {
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = s % 60;
  return (h ? `${h}:${String(m).padStart(2, "0")}` : String(m)) + ":" + String(sec).padStart(2, "0");
};
const who = (m: Mention) => m.author.name || m.author.handle || "Unknown";
const about = (m: Mention, brandName: string) => (m.competitor ? m.competitor.name : brandName);
// A YouTube mention's video id, for its thumbnail and player.
const videoId = (m: Mention) => {
  if (m.source !== "youtube") return "";
  if (/^[A-Za-z0-9_-]{11}$/.test(m.external_id)) return m.external_id;
  try {
    return new URL(m.url).searchParams.get("v") ?? "";
  } catch {
    return "";
  }
};
// A title without the " - YouTube" a search result adds.
const cleanTitle = (t: string) => t.replace(/\s+-\s+YouTube$/, "").trim();
// Who said it, or what the source still has to tell: a site's host isn't a person.
const isHost = (name: string) => /^[a-z0-9.-]+\.[a-z]{2,}$/.test(name);
const channelName = (m: Mention) => (m.author.name && !isHost(m.author.name) ? m.author.name : m.author.handle || "");

// Thumb is a video's still, or the source's tile.
function Thumb({ m, big }: { m: Mention; big?: boolean }) {
  const v = videoId(m);
  const size = big ? "h-[68px] w-[120px]" : "h-[45px] w-[80px]";
  if (v) {
    return (
      <span className={`relative block shrink-0 overflow-hidden rounded-md bg-muted ${size}`}>
        <img src={`https://i.ytimg.com/vi/${v}/mqdefault.jpg`} alt="" loading="lazy" className="size-full object-cover" />
        {m.at_seconds != null && <span className="absolute right-1 bottom-1 rounded bg-black/80 px-1 font-mono text-[10px] text-white">{at(m.at_seconds)}</span>}
      </span>
    );
  }
  return <span className={`flex shrink-0 items-center justify-center rounded-md bg-muted ${size}`}><SourceIcon source={m.source} /></span>;
}

// Where is the video, episode or post, with the channel or show it is on.
function Where({ m }: { m: Mention }) {
  const name = channelName(m);
  const title = cleanTitle(m.title) || m.url;
  return (
    <div className="flex min-w-0 items-center gap-3">
      <Thumb m={m} />
      <div className="min-w-0 max-w-[340px]">
        <a className="line-clamp-2 text-[13px] font-medium leading-5 underline-offset-2 hover:underline" href={m.url} target="_blank" rel="noreferrer noopener" onClick={(e) => e.stopPropagation()} title={title}>
          {title}
        </a>
        <div className="mt-0.5 flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground">
          <SourceIcon source={m.source} />
          {name ? (
            m.author.url ? (
              <a className="truncate underline-offset-2 hover:underline" href={m.author.url} target="_blank" rel="noreferrer noopener" onClick={(e) => e.stopPropagation()}>{name}</a>
            ) : <span className="truncate">{name}</span>
          ) : <span className="truncate italic">{m.source === "youtube" ? "Channel not read yet" : sourceName[m.source]}</span>}
          {m.reach ? <span className="shrink-0">· {short(m.reach)} {audience(m.source)}</span> : null}
        </div>
      </div>
    </div>
  );
}

function Who({ m }: { m: Mention }) {
  const handle = m.author.handle && m.author.handle !== m.author.name ? m.author.handle : "";
  return (
    <div className="flex items-center gap-2">
      <SourceIcon source={m.source} />
      <div className="min-w-0">
        <div className="truncate font-medium">{who(m)}</div>
        <div className="truncate text-xs text-muted-foreground">
          {handle}
          {handle && m.reach ? " · " : ""}
          {m.reach ? `${short(m.reach)} ${audience(m.source)}` : ""}
          {!handle && !m.reach ? sourceName[m.source] : ""}
        </div>
      </div>
    </div>
  );
}

// Quote marks the name that matched.
function Quote({ text, matched }: { text: string; matched: string }) {
  const i = matched ? text.toLowerCase().indexOf(matched.toLowerCase()) : -1;
  if (i < 0) return <>“{text}”</>;
  return (
    <>
      “{text.slice(0, i)}
      <mark className="rounded bg-amber-400/25 px-0.5 text-foreground">{text.slice(i, i + matched.length)}</mark>
      {text.slice(i + matched.length)}”
    </>
  );
}

export function Mentions(props: Props) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="Mentions" brands={active.length} />;
  return <MentionsPage key={brand.id} brandId={brand.id} brandName={brand.name} {...props} />;
}

function MentionsPage({ brandId, brandName, canManage, item, onOpen, go }: Props & { brandId: string; brandName: string }) {
  const [view, setView] = useState<View>("mentions");
  const [overview, setOverview] = useState<MentionsOverview | null>(null);
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/mentions/overview", { params: { path: { brand: brandId } } });
    if (error) setError(errorMessage(error));
    else setOverview(data);
  }, [brandId]);
  useEffect(() => void load(), [load]);

  const settings = async (body: { enabled?: boolean; sources?: MentionsOverview["sources"][number]["id"][]; scan_every_hours?: number }) => {
    setSaving(true);
    const { data, error } = await api.PATCH("/v1/brands/{brand}/mentions/settings", { params: { path: { brand: brandId } }, body });
    setSaving(false);
    if (error) return toast(errorMessage(error), "error");
    if (data && "enabled" in data) setOverview(data);
    if (body.enabled === true) toast("Mentions are on. The first scan is running now.");
  };
  const scan = async () => {
    const { error } = await api.POST("/v1/brands/{brand}/mentions/scan", { params: { path: { brand: brandId } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Scanning now. New mentions show up here in a minute.");
  };

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!overview) return <Loading />;
  if (item) return <MentionPage id={item} brandId={brandId} brandName={brandName} canManage={canManage} back={() => onOpen("")} />;

  const lastScan = overview.last_scan_at ? `Last scan ${ago(overview.last_scan_at)}.` : "No scan yet.";
  return (
    <div>
      <PageHeader
        title={<>Mentions <Badge tone={overview.enabled ? "green" : "grey"}>{overview.enabled ? "On" : "Off"}</Badge></>}
        description={`Who talks about ${brandName} and your competitors in videos, podcasts, posts, forums and news, what they said, and who said it. ${overview.enabled ? lastScan : "Off until you turn it on."} Agents use list_mentions.`}
        action={
          canManage ? (
            <>
              <label className="flex items-center gap-2 text-[13px]">
                <Switch checked={overview.enabled} disabled={saving} onCheckedChange={(on) => void settings({ enabled: on })} aria-label="Mentions on" /> Listening
              </label>
              {overview.enabled && <Button variant="ghost" onClick={() => void scan()}>Scan now</Button>}
            </>
          ) : undefined
        }
      />
      <Tabs value={view} onValueChange={(v) => setView(v as View)}>
        <TabsList className="mb-5">
          <TabsTrigger value="mentions">Mentions</TabsTrigger>
          <TabsTrigger value="creators">Creators</TabsTrigger>
          <TabsTrigger value="sources">Sources</TabsTrigger>
        </TabsList>
      </Tabs>
      {view === "mentions" && <List overview={overview} brandId={brandId} brandName={brandName} canManage={canManage} open={onOpen} turnOn={() => void settings({ enabled: true })} show={setView} />}
      {view === "creators" && <Creators brandId={brandId} brandName={brandName} open={onOpen} />}
      {view === "sources" && <Sources overview={overview} brandId={brandId} brandName={brandName} canManage={canManage} saving={saving} save={settings} saved={setOverview} go={go} />}
    </div>
  );
}

// useMentions loads a brand's mentions with the filters given, a page of
// 200 at a time.
function useMentions(brandId: string, query: { about?: "brand" | "competitor"; source?: Source; competitor_id?: string; status?: MentionStatus }) {
  const [rows, setRows] = useState<Mention[] | null>(null);
  const [next, setNext] = useState<string | null>(null);
  const [error, setError] = useState("");
  const key = JSON.stringify(query);
  useEffect(() => {
    let live = true;
    setRows(null);
    void api.GET("/v1/brands/{brand}/mentions", { params: { path: { brand: brandId }, query: { ...JSON.parse(key), limit: 200 } } }).then(({ data, error }) => {
      if (!live) return;
      if (error) setError(errorMessage(error));
      else {
        setRows(data.data);
        setNext(data.next_cursor ?? null);
      }
    });
    return () => {
      live = false;
    };
  }, [brandId, key]);
  const more = async () => {
    if (!next) return;
    const { data, error } = await api.GET("/v1/brands/{brand}/mentions", { params: { path: { brand: brandId }, query: { ...query, limit: 200, cursor: next } } });
    if (error) return setError(errorMessage(error));
    setRows((r) => [...(r ?? []), ...data.data]);
    setNext(data.next_cursor ?? null);
  };
  return { rows, error, more: next ? more : undefined };
}

function List({ overview, brandId, brandName, canManage, open, turnOn, show }: {
  overview: MentionsOverview; brandId: string; brandName: string; canManage: boolean; open: (id: string) => void; turnOn: () => void; show: (v: View) => void;
}) {
  const [aboutF, setAbout] = useState<"" | "brand" | "competitor">("");
  const [source, setSource] = useState<"" | Source>("");
  const [competitor, setCompetitor] = useState("");
  const [status, setStatus] = useState<"" | MentionStatus>("");
  const { rows, error, more } = useMentions(brandId, {
    about: aboutF || undefined, source: source || undefined, competitor_id: competitor || undefined, status: status || undefined,
  });
  const competitors = overview.listen_for.filter((l) => l.about === "competitor");
  const t = overview.totals;

  const columns: ColumnDef<Mention>[] = useMemo(() => [
    { id: "where", header: "Where", accessorFn: (m) => `${m.title} ${channelName(m)}`, cell: ({ row }) => <Where m={row.original} /> },
    {
      id: "said", header: "What they said", accessorFn: (m) => m.quote,
      cell: ({ row: { original: m } }) => {
        const same = cleanTitle(m.quote) === cleanTitle(m.title);
        return (
          <div className="max-w-[460px]">
            <div className="mb-1 flex flex-wrap items-center gap-1.5">
              <Badge tone={m.about === "brand" ? "green" : "blue"}>{about(m, brandName)}</Badge>
              {m.at_seconds != null && <span className="font-mono text-[11px] text-muted-foreground">said at {at(m.at_seconds)}</span>}
              {same && <span className="text-[11px] text-muted-foreground">in the title</span>}
              {m.label && <Badge>{m.label}</Badge>}
            </div>
            {!same && <div className="line-clamp-2 text-[13px]"><Quote text={m.quote} matched={m.matched} /></div>}
          </div>
        );
      },
    },
    { id: "when", header: "When", accessorFn: (m) => m.published_at ?? m.found_at, cell: ({ row }) => <span className="whitespace-nowrap text-muted-foreground">{ago(row.original.published_at ?? row.original.found_at)}</span> },
    {
      id: "score", header: "Score", accessorFn: (m) => m.score, meta: { align: "right", mono: true },
      cell: ({ row }) => <span className={row.original.score >= 70 ? "font-semibold text-green-600 dark:text-green-400" : ""}>{row.original.score}</span>,
    },
    { id: "status", header: "Status", accessorFn: (m) => m.status, cell: ({ row }) => <Badge tone={statusTone[row.original.status]}>{statusName[row.original.status]}</Badge> },
  ], [brandName]);

  if (!overview.enabled && t.total === 0) {
    return (
      <Empty
        title="Mentions are off"
        action={canManage ? <Button onClick={turnOn}>Turn on mentions</Button> : undefined}
      >
        Turn them on to find where other people name {brandName} or your competitors: YouTube videos, podcast episodes, Reddit posts, news and forums. Your own channels and your competitors' are left out.
      </Empty>
    );
  }
  if (error) return <ErrorText>{error}</ErrorText>;

  const notReady = overview.sources.filter((s) => s.on && !s.ready);
  return (
    <>
      <Stats cols={4}>
        <Stat label="New this week" value={t.new_this_week.toLocaleString()} sub={`${t.total.toLocaleString()} in all`} />
        <Stat label="People behind them" value={t.people.toLocaleString()} />
        <Stat label="Contacted" value={t.contacted.toLocaleString()} />
        <Stat label="Replied or on board" value={(t.replied + t.on_board).toLocaleString()} tone={t.replied + t.on_board > 0 ? "green" : undefined} />
      </Stats>
      {notReady.length > 0 && (
        <div className="mt-4 rounded-md border border-amber-500/40 bg-amber-500/5 p-3 text-[13px]">
          {listNames(notReady.map((s) => s.name))} {notReady.length === 1 ? "has" : "have"} nothing to read yet.{" "}
          <button type="button" className="underline underline-offset-2" onClick={() => show("sources")}>See what to set up</button>
        </div>
      )}
      <div className="mt-5">
        {rows === null ? <Loading /> : (
          <DataTable
            columns={columns}
            data={rows}
            rowId={(m) => m.id}
            initialSort={[{ id: "score", desc: true }]}
            onRowClick={(m) => open(m.id)}
            searchPlaceholder="Search titles, channels, quotes"
            empty={overview.last_scan_at ? "No mentions match yet. New ones show up after each scan." : "The first scan is running. Mentions show up here in a minute."}
            toolbar={
              <div className="flex flex-wrap items-center gap-2">
                <select className={field} value={aboutF} onChange={(e) => setAbout(e.target.value as typeof aboutF)} aria-label="About">
                  <option value="">Everyone named</option>
                  <option value="competitor">About competitors</option>
                  <option value="brand">About {brandName}</option>
                </select>
                <select className={field} value={source} onChange={(e) => setSource(e.target.value as typeof source)} aria-label="Source">
                  <option value="">Every source</option>
                  {(Object.keys(sourceName) as Source[]).map((id) => <option key={id} value={id}>{sourceName[id]}</option>)}
                </select>
                {competitors.length > 0 && (
                  <select className={field} value={competitor} onChange={(e) => setCompetitor(e.target.value)} aria-label="Competitor">
                    <option value="">Every competitor</option>
                    {competitors.map((c) => <option key={c.competitor_id} value={c.competitor_id ?? ""}>{c.name}</option>)}
                  </select>
                )}
                <select className={field} value={status} onChange={(e) => setStatus(e.target.value as typeof status)} aria-label="Status">
                  <option value="">Every status</option>
                  {(Object.keys(statusName) as MentionStatus[]).map((s) => <option key={s} value={s}>{statusName[s]}</option>)}
                </select>
              </div>
            }
          />
        )}
        {more && <div className="mt-3"><Button variant="ghost" onClick={() => void more()}>Load more</Button></div>}
      </div>
    </>
  );
}

type Creator = { key: string; first: Mention; mentions: number; talksAbout: string[]; reach: number | null; best: number; status: MentionStatus; latest: string };

function Creators({ brandId, brandName, open }: { brandId: string; brandName: string; open: (id: string) => void }) {
  const { rows, error, more } = useMentions(brandId, {});
  const creators = useMemo(() => {
    const by = new Map<string, Creator>();
    for (const m of rows ?? []) {
      const id = m.author.url || m.author.handle || m.author.name;
      if (!id) continue;
      const key = `${m.source}:${id}`;
      const c = by.get(key);
      const name = about(m, brandName);
      if (!c) {
        by.set(key, { key, first: m, mentions: 1, talksAbout: [name], reach: m.reach, best: m.score, status: m.status, latest: m.found_at });
        continue;
      }
      c.mentions++;
      if (!c.talksAbout.includes(name)) c.talksAbout.push(name);
      if (m.reach != null && (c.reach == null || m.reach > c.reach)) c.reach = m.reach;
      if (m.score > c.best) {
        c.best = m.score;
        c.first = m;
      }
      if (m.status !== "new") c.status = m.status;
      if (m.found_at > c.latest) c.latest = m.found_at;
    }
    return [...by.values()];
  }, [rows, brandName]);

  const columns: ColumnDef<Creator>[] = [
    { id: "best", header: "Best score", accessorFn: (c) => c.best, meta: { align: "right", mono: true } },
    { id: "who", header: "Who", accessorFn: (c) => who(c.first), cell: ({ row }) => <Who m={{ ...row.original.first, reach: row.original.reach }} /> },
    { id: "mentions", header: "Mentions", accessorFn: (c) => c.mentions, meta: { align: "right", mono: true } },
    { id: "talks", header: "Talks about", accessorFn: (c) => c.talksAbout.join(", "), cell: ({ row }) => <div className="flex flex-wrap gap-1">{row.original.talksAbout.map((x) => <Badge key={x} tone={x === brandName ? "green" : "blue"}>{x}</Badge>)}</div> },
    {
      id: "profile", header: "Profile", accessorFn: (c) => c.first.author.url,
      cell: ({ row }) => (row.original.first.author.url ? <a className="text-xs underline underline-offset-2" href={row.original.first.author.url} target="_blank" rel="noreferrer noopener" onClick={(e) => e.stopPropagation()}>Open profile</a> : <span className="text-xs text-muted-foreground">—</span>),
    },
    { id: "latest", header: "Last mention", accessorFn: (c) => c.latest, cell: ({ row }) => <span className="text-muted-foreground">{ago(row.original.latest)}</span> },
    { id: "status", header: "Status", accessorFn: (c) => c.status, cell: ({ row }) => <Badge tone={statusTone[row.original.status]}>{statusName[row.original.status]}</Badge> },
  ];
  if (error) return <ErrorText>{error}</ErrorText>;
  if (rows === null) return <Loading />;
  return (
    <>
      <p className="mb-4 max-w-3xl text-[13px] text-muted-foreground">
        Everyone behind a mention, once per channel, with how often and about whom they talk. Open one to see their best mention.
      </p>
      <DataTable columns={columns} data={creators} rowId={(c) => c.key} initialSort={[{ id: "best", desc: true }]} onRowClick={(c) => open(c.first.id)} searchPlaceholder="Search people" empty="Nobody yet. People show up here with their first mention." />
      {more && <div className="mt-3"><Button variant="ghost" onClick={() => void more()}>Load more mentions</Button></div>}
    </>
  );
}

type MentionSourceId = MentionsOverview["sources"][number]["id"];

// scanError says in plain words why the last scan, or its YouTube or
// podcast search, failed.
function scanError(code: string): string {
  switch (code) {
    case "connector_needed": return "YouTube or Podcasts is on but its connection is missing";
    case "usage_limit_reached": return "Geonode's usage limit is reached; YouTube waits for it";
    case "connector_auth": return "A connection refused its key; reconnect it under Connectors";
    case "connector_quota": return "A connection is out of tokens or rate limited";
    default: return "The last scan failed; it tries again";
  }
}

function Sources({ overview, brandId, brandName, canManage, saving, save, saved, go }: {
  overview: MentionsOverview; brandId: string; brandName: string; canManage: boolean; saving: boolean;
  save: (b: { sources?: MentionSourceId[]; scan_every_hours?: number }) => Promise<void>; saved: (o: MentionsOverview) => void; go: (tab: Tab) => void;
}) {
  const on = overview.sources.filter((s) => s.on).map((s) => s.id);
  const toggle = (id: MentionSourceId, v: boolean) => void save({ sources: v ? [...on, id] : on.filter((x) => x !== id) });
  return (
    <div className="grid gap-5">
      <Card title="Where we listen" description="Reddit, News and Presence read what your own watches, searches and competitor scans already found, at no extra cost. YouTube searches for videos and reads their captions on your own Geonode account, which uses Geonode tokens. Podcasts search every public podcast on your own free Podcast Index key.">
        <div className="divide-y">
          {overview.sources.map((s) => (
            <div key={s.id} className="flex flex-wrap items-center gap-3 py-3">
              <SourceIcon source={s.id} />
              <div className="min-w-[240px] flex-1">
                <div className="font-medium">{s.name}</div>
                <div className="text-xs text-muted-foreground">{s.ready ? "Ready" : s.setup_hint}</div>
              </div>
              <div className="w-56 text-xs">
                {s.ready ? <Status tone={s.on ? "green" : "grey"}>{s.on ? "Reading" : "Off"}</Status> : (
                  <Button variant="ghost" onClick={() => go(s.setup_tab as Tab)}>Set up</Button>
                )}
              </div>
              <Switch checked={s.on} disabled={!canManage || saving} onCheckedChange={(v) => toggle(s.id as MentionSourceId, v)} aria-label={`${s.name} on`} />
            </div>
          ))}
        </div>
        <div className="mt-3 flex flex-wrap items-center gap-2 text-[13px]">
          <span className="text-muted-foreground">Scan every</span>
          <select className={field} value={overview.scan_every_hours} disabled={!canManage || saving} onChange={(e) => void save({ scan_every_hours: Number(e.target.value) })} aria-label="Scan every">
            {[1, 3, 6, 12, 24, 48, 168].map((h) => <option key={h} value={h}>{h === 168 ? "week" : h === 1 ? "hour" : `${h} hours`}</option>)}
          </select>
          {overview.last_error_code && <Status tone="red">{scanError(overview.last_error_code)}</Status>}
        </div>
        <p className="mt-3 text-xs text-muted-foreground">X, Hacker News and Bluesky are coming next.</p>
      </Card>
      <ListenFor overview={overview} brandId={brandId} brandName={brandName} canManage={canManage} saved={saved} go={go} />
    </div>
  );
}

type Listen = MentionsOverview["listen_for"][number];

// ListenFor shows what scans listen for, one row for the brand and one per
// tracked competitor: each row on or off, its terms, and terms added or
// taken out by hand.
function ListenFor({ overview, brandId, brandName, canManage, saved, go }: {
  overview: MentionsOverview; brandId: string; brandName: string; canManage: boolean; saved: (o: MentionsOverview) => void; go: (tab: Tab) => void;
}) {
  const [busy, setBusy] = useState("");
  const change = async (l: Listen, body: { listening?: boolean; added_terms?: string[]; skipped_terms?: string[] }) => {
    setBusy(l.competitor_id ?? "brand");
    const { data, error } = await api.PATCH("/v1/brands/{brand}/mentions/listen-for", { params: { path: { brand: brandId } }, body: { competitor_id: l.competitor_id, ...body } });
    setBusy("");
    if (error) return toast(errorMessage(error), "error");
    if (data && "listen_for" in data) saved(data);
  };
  return (
    <Card
      title="What we listen for"
      description={`${brandName} and every competitor you track, by website and brand name only. Everyday words like "Web scraping API" are left out so mentions are about a brand. Turn a row off, take a term out, or add one, such as a product name.`}
      action={<Button variant="ghost" onClick={() => go("competitors")}>Competitors page</Button>}
    >
      <table className="w-full text-[13px]">
        <thead>
          <tr className="border-b text-left text-xs text-muted-foreground">
            <th className="w-14 py-2 font-normal">On</th>
            <th className="w-[220px] py-2 font-normal">Name</th>
            <th className="py-2 font-normal">Listens for</th>
          </tr>
        </thead>
        <tbody className="divide-y">
          {overview.listen_for.map((l) => (
            <ListenRow key={l.competitor_id ?? "brand"} l={l} brandName={brandName} canManage={canManage} busy={busy === (l.competitor_id ?? "brand")} change={change} />
          ))}
        </tbody>
      </table>
      {overview.listen_for.length === 1 && <div className="py-2.5 text-[13px] text-muted-foreground">No tracked competitors yet. Track them on the Competitors page.</div>}
    </Card>
  );
}

function ListenRow({ l, brandName, canManage, busy, change }: {
  l: Listen; brandName: string; canManage: boolean; busy: boolean; change: (l: Listen, b: { listening?: boolean; added_terms?: string[]; skipped_terms?: string[] }) => Promise<void>;
}) {
  const [adding, setAdding] = useState(false);
  const [term, setTerm] = useState("");
  const skipped = (t: string) => l.skipped_terms.some((x) => x.toLowerCase() === t.toLowerCase());
  const add = async () => {
    const t = term.trim();
    if (t.length < 3) return toast("A term needs at least 3 characters.", "error");
    await change(l, { added_terms: [...l.added_terms, t] });
    setTerm("");
    setAdding(false);
  };
  const off = !l.listening;
  return (
    <tr className={off ? "text-muted-foreground" : ""}>
      <td className="py-2.5 align-top">
        <Switch checked={l.listening} disabled={!canManage || busy} onCheckedChange={(v) => void change(l, { listening: v })} aria-label={`Listen for ${l.name}`} />
      </td>
      <td className="py-2.5 pr-3 align-top font-medium">{l.name}{l.about === "brand" ? ` (${brandName === l.name ? "you" : brandName})` : ""}</td>
      <td className="py-2 align-top">
        <div className="flex flex-wrap items-center gap-1.5">
          {l.auto_terms.map((t) => (
            <TermChip key={t} text={t} off={off || skipped(t)} title={skipped(t) ? "Taken out: not listened for" : "From the website or brand name"}
              action={canManage ? (skipped(t) ? "Put back" : "Take out") : undefined}
              onAction={() => void change(l, { skipped_terms: skipped(t) ? l.skipped_terms.filter((x) => x.toLowerCase() !== t.toLowerCase()) : [...l.skipped_terms, t] })} />
          ))}
          {l.added_terms.map((t) => (
            <TermChip key={`+${t}`} text={t} added off={off} title="Added by hand" action={canManage ? "Remove" : undefined}
              onAction={() => void change(l, { added_terms: l.added_terms.filter((x) => x !== t) })} />
          ))}
          {l.auto_terms.length === 0 && l.added_terms.length === 0 && <span className="text-xs text-muted-foreground">Nothing yet: add a term.</span>}
          {canManage && !off && (adding ? (
            <form className="flex items-center gap-1.5" onSubmit={(e) => { e.preventDefault(); void add(); }}>
              <input className={`${field} h-7 w-44`} autoFocus value={term} onChange={(e) => setTerm(e.target.value)} placeholder="Product or other name" aria-label={`Add a term for ${l.name}`} />
              <Button type="submit" disabled={busy}>Add</Button>
              <Button type="button" variant="ghost" onClick={() => { setAdding(false); setTerm(""); }}>Cancel</Button>
            </form>
          ) : (
            <button type="button" className="rounded-md border border-dashed px-2 py-0.5 text-xs text-muted-foreground hover:border-ring hover:text-foreground" onClick={() => setAdding(true)}>+ Add term</button>
          ))}
        </div>
      </td>
    </tr>
  );
}

function TermChip({ text, off, added, title, action, onAction }: { text: string; off?: boolean; added?: boolean; title: string; action?: string; onAction: () => void }) {
  return (
    <span title={title} className={`inline-flex items-center gap-1 rounded-md border px-2 py-0.5 font-mono text-xs ${off ? "border-dashed text-muted-foreground line-through" : added ? "border-green-600/40 bg-green-600/5" : "bg-muted/40"}`}>
      {text}
      {action && (
        <button type="button" className="ml-0.5 rounded px-0.5 font-sans text-[11px] text-muted-foreground no-underline hover:text-foreground" onClick={onAction} aria-label={`${action} ${text}`} title={action}>
          {action === "Put back" ? "↺" : "×"}
        </button>
      )}
    </span>
  );
}

const steps: { id: MentionStatus; label: string; hint: string }[] = [
  { id: "new", label: "New", hint: "Not looked at yet." },
  { id: "contacted", label: "Contacted", hint: "You reached out." },
  { id: "replied", label: "Replied", hint: "They answered." },
  { id: "on_board", label: "On board", hint: "Working with you." },
];

function MentionPage({ id, brandId, brandName, canManage, back }: { id: string; brandId: string; brandName: string; canManage: boolean; back: () => void }) {
  const [m, setM] = useState<Mention | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    void api.GET("/v1/brands/{brand}/mentions/{id}", { params: { path: { brand: brandId, id } } }).then(({ data, error }) => (error ? setError(errorMessage(error)) : setM(data)));
  }, [brandId, id]);
  const move = async (status: MentionStatus) => {
    const { data, error } = await api.PATCH("/v1/brands/{brand}/mentions/{id}", { params: { path: { brand: brandId, id } }, body: { status } });
    if (error) return toast(errorMessage(error), "error");
    if (data && "status" in data) setM(data);
  };
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!m) return <Loading />;

  const video = videoId(m);
  const name = channelName(m);
  const title = cleanTitle(m.title) || sourceName[m.source];
  const sameAsTitle = cleanTitle(m.quote) === cleanTitle(m.title);
  const when = m.published_at ? `published ${ago(m.published_at)}` : `found ${ago(m.found_at)}`;
  const line = [sourceName[m.source], name, m.reach ? `${m.reach.toLocaleString()} ${audience(m.source)}` : "", when].filter(Boolean).join(" · ");
  const stepAt = steps.findIndex((x) => x.id === m.status);
  const reasons: [string, string][] = [
    [m.about === "competitor" ? `Names ${m.competitor?.name}, a competitor` : `Names ${brandName}`, m.about === "competitor" ? "A chance to be the alternative." : "Someone who already talks about you."],
    [m.source === "youtube" || m.source === "podcast" ? `A ${m.source === "youtube" ? "video" : "podcast episode"}` : sourceName[m.source], m.source === "youtube" || m.source === "podcast" ? "Listened to whole, so it carries more weight." : "Read in passing."],
    ["Audience", m.reach ? `${m.reach.toLocaleString()} ${audience(m.source)}.` : "Not known yet."],
    ["Fresh", m.published_at ? `Published ${ago(m.published_at)}.` : `Found ${ago(m.found_at)}.`],
  ];
  return (
    <div>
      <PageHeader
        back={{ label: "Mentions", onClick: back }}
        title={<>{title} <Badge tone={m.about === "brand" ? "green" : "blue"}>{about(m, brandName)}</Badge>{m.label && <> <Badge>{m.label}</Badge></>}</>}
        description={line}
        action={<a href={m.url} target="_blank" rel="noreferrer noopener"><Button variant="ghost">{m.source === "podcast" ? "Open episode" : `Open on ${sourceName[m.source]}`}{m.at_seconds != null ? ` at ${at(m.at_seconds)}` : ""}</Button></a>}
      />
      <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_340px]">
        <div className="grid content-start gap-5">
          {video && (
            <div className="overflow-hidden rounded-lg border bg-black shadow-xs">
              <iframe
                className="aspect-video w-full"
                src={`https://www.youtube-nocookie.com/embed/${video}?start=${m.at_seconds ?? 0}&rel=0`}
                title={title}
                allow="encrypted-media; picture-in-picture"
                allowFullScreen
                referrerPolicy="strict-origin-when-cross-origin"
              />
            </div>
          )}
          <Card
            title={m.at_seconds != null ? `What they said at ${at(m.at_seconds)}` : sameAsTitle ? "Named in the title" : "What they said"}
            description={m.at_seconds != null ? "Their own words, from the captions or transcript." : sameAsTitle ? "The name is in the title. Its captions or transcript haven't named it yet, or haven't been read." : "Their own words, as the source shows them."}
            action={<CopyButton text={m.quote || m.title} label="Copy quote" />}
          >
            <blockquote className="border-l-2 border-amber-400 pl-4 text-[15px] leading-7"><Quote text={m.quote || m.title} matched={m.matched} /></blockquote>
            <div className="mt-3 text-xs text-muted-foreground">Matched “{m.matched}”{m.competitor ? `, a name of ${m.competitor.name}` : ""}.</div>
          </Card>
          <Card title={`Score ${m.score} of 100`} description="A first sort of what is worth a look, not a verdict.">
            <ul className="grid gap-2 text-[13px] sm:grid-cols-2">
              {reasons.map(([a, b]) => (
                <li key={a} className="rounded-md bg-muted/40 px-3 py-2"><div className="font-medium">{a}</div><div className="text-muted-foreground">{b}</div></li>
              ))}
            </ul>
          </Card>
        </div>
        <div className="grid content-start gap-5">
          <Card title={m.source === "youtube" ? "Channel" : m.source === "podcast" ? "Show" : "Who said it"}>
            <div className="flex items-center gap-3">
              <span className="flex size-10 shrink-0 items-center justify-center rounded-full bg-muted text-sm font-semibold">{(name || "?").slice(0, 1).toUpperCase()}</span>
              <div className="min-w-0">
                <div className="truncate font-medium">{name || (m.source === "youtube" ? "Channel not read yet" : "Unknown")}</div>
                <div className="truncate text-xs text-muted-foreground">{m.author.handle && m.author.handle !== name ? `${m.author.handle} · ` : ""}{m.reach ? `${short(m.reach)} ${audience(m.source)}` : "Audience not known yet"}</div>
              </div>
            </div>
            {m.author.url && (
              <div className="mt-3 flex gap-2">
                <a href={m.author.url} target="_blank" rel="noreferrer noopener"><Button variant="ghost">Open {m.source === "youtube" ? "channel" : m.source === "podcast" ? "show" : "profile"}</Button></a>
                <CopyButton text={m.author.url} label="Copy link" />
              </div>
            )}
            {!name && m.source === "youtube" && <p className="mt-3 text-xs text-muted-foreground">The channel shows up once the video is read, on the next scan with YouTube on.</p>}
          </Card>
          <Card title="Next step" description="Where this conversation is. Nothing here contacts anyone.">
            <ol className="grid gap-1.5">
              {steps.map((x, i) => {
                const done = m.status !== "not_a_fit" && i <= stepAt;
                const current = x.id === m.status;
                return (
                  <li key={x.id}>
                    <button
                      type="button"
                      disabled={!canManage || current}
                      onClick={() => void move(x.id)}
                      className={`flex w-full items-center gap-3 rounded-md border px-3 py-2 text-left text-[13px] transition-colors ${current ? "border-ring bg-muted/60" : "hover:bg-muted/40"} disabled:cursor-default`}
                    >
                      <span className={`flex size-5 shrink-0 items-center justify-center rounded-full text-[11px] font-semibold ${done ? "bg-green-600 text-white" : "border text-muted-foreground"}`}>{done ? "✓" : i + 1}</span>
                      <span className="flex-1"><span className="font-medium">{x.label}</span><span className="block text-xs text-muted-foreground">{x.hint}</span></span>
                      {current && <Badge tone={statusTone[x.id]}>Now</Badge>}
                    </button>
                  </li>
                );
              })}
            </ol>
            {canManage && (
              <div className="mt-3 border-t pt-3">
                {m.status === "not_a_fit" ? (
                  <Button variant="ghost" onClick={() => void move("new")}>Back to new</Button>
                ) : (
                  <Button variant="ghost" onClick={() => void move("not_a_fit")}>Not a fit</Button>
                )}
              </div>
            )}
          </Card>
        </div>
      </div>
    </div>
  );
}

function listNames(names: string[]): string {
  if (names.length <= 1) return names.join("");
  return `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}
