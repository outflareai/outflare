import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import type { HelpCentre as HelpCentreView, HelpCentreSettings, HelpCentreStats, KnowledgeCollection } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, confirmAction, ErrorText, field, Loading, PageHeader, Status, toast } from "../ui";
import { Icon } from "../icons";
import { cn } from "@/lib/utils";
import { useBrand } from "@/lib/brand";
import { track } from "@/lib/analytics";
import { Switch } from "@/components/ui/switch";

// The help centre: a public site of the knowledge marked for the help
// centre, one per brand. This page sets it up (one click, from the
// brand's look), shows how readers use it, gives it a domain, and holds
// the Configure and style editor: every setting beside a live preview of
// the real page, saved together.

type Settings = HelpCentreSettings;
type PreviewPage = "home" | "collection" | "article" | "search";
type Link = { label: string; url: string };

// Templates set the look in one go; everything stays editable after.
const templates: { id: NonNullable<Settings["template"]>; name: string; blurb: string; set: Settings }[] = [
  {
    id: "classic",
    name: "Classic",
    blurb: "A header and hero in your colour, collections as cards",
    set: { template: "classic", header_color: "", background: "#ffffff", radius: "medium", font: "system", collections_layout: "grid", columns: 3, footer_layout: "simple" },
  },
  {
    id: "minimal",
    name: "Minimal",
    blurb: "Light and plain, collections as a list",
    set: { template: "minimal", header_color: "#ffffff", background: "#ffffff", radius: "small", font: "inter", collections_layout: "list", columns: 1, footer_layout: "simple" },
  },
  {
    id: "bold",
    name: "Bold",
    blurb: "A large hero, rounder corners, a footer in columns",
    set: { template: "bold", header_color: "", background: "#ffffff", radius: "large", font: "dm-sans", collections_layout: "grid", columns: 2, footer_layout: "columns" },
  },
];

const fonts: { id: NonNullable<Settings["font"]>; name: string }[] = [
  { id: "system", name: "System (fastest)" },
  { id: "inter", name: "Inter" },
  { id: "roboto", name: "Roboto" },
  { id: "open-sans", name: "Open Sans" },
  { id: "lato", name: "Lato" },
  { id: "source-sans", name: "Source Sans" },
  { id: "ibm-plex-sans", name: "IBM Plex Sans" },
  { id: "dm-sans", name: "DM Sans" },
  { id: "nunito", name: "Nunito" },
  { id: "merriweather", name: "Merriweather (serif)" },
  { id: "lora", name: "Lora (serif)" },
];

const socials: { id: "x" | "linkedin" | "youtube" | "facebook" | "instagram" | "github"; name: string }[] = [
  { id: "x", name: "X" },
  { id: "linkedin", name: "LinkedIn" },
  { id: "youtube", name: "YouTube" },
  { id: "facebook", name: "Facebook" },
  { id: "instagram", name: "Instagram" },
  { id: "github", name: "GitHub" },
];

export const folderIcons = ["book", "rocket", "card", "shield", "settings", "users", "chat", "bolt", "star", "globe", "help", "tag", "box", "chart", "plug", "lock"] as const;

export function HelpCentre({ ws, canManage, go }: { ws: string; canManage: boolean; go?: (tab: "knowledge" | "domains") => void }) {
  const { brand, query: brandQuery } = useBrand(ws);
  const [list, setList] = useState<HelpCentreView[] | null>(null);
  const [error, setError] = useState("");
  const [editing, setEditing] = useState(false);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/help-centres", { params: { path: { ws }, query: brandQuery } });
    if (error) return setError(errorMessage(error));
    setList(data.data);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brand?.id]);
  useEffect(() => void load(), [load]);

  // The help centre for the brand picked (or the workspace's own).
  const hc = list?.find((h) => (brand ? h.brand_id === brand.id : h.brand_id === null)) ?? null;

  async function create() {
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/help-centres", { params: { path: { ws } }, body: brand ? { brand_id: brand.id } : {} });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    setList((l) => [...(l ?? []), data]);
    setEditing(true);
  }

  async function patch(body: { enabled?: boolean; domain_id?: string | null; name?: string; slug?: string; settings?: Settings }, done?: string) {
    if (!hc) return false;
    const { data, error } = await api.PATCH("/v1/workspaces/{ws}/help-centres/{id}", { params: { path: { ws, id: hc.id } }, body });
    if (error) {
      toast(errorMessage(error), "error");
      return false;
    }
    setList((l) => l?.map((x) => (x.id === data.id ? data : x)) ?? null);
    if (done) toast(done);
    return true;
  }

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!list) return <Loading />;
  if (hc && editing) return <Designer ws={ws} hc={hc} canManage={canManage} onClose={() => setEditing(false)} onSaved={(v) => setList((l) => l?.map((x) => (x.id === v.id ? v : x)) ?? null)} />;

  return (
    <div className="space-y-5">
      <PageHeader
        title={
          <>
            Help centre
            {hc && (hc.enabled ? <span className="text-[13px] font-normal"><Status tone="green">Live</Status></span> : <span className="text-[13px] font-normal"><Status tone="grey">Off</Status></span>)}
          </>
        }
        description="A public site where customers read and search what you've marked for the help centre in Knowledge. Each brand can have its own."
        action={
          hc && (
            <div className="flex gap-2">
              <a href={hc.url} target="_blank" rel="noreferrer" className="inline-flex"><Button variant="ghost"><Icon name="external" className="size-3.5" /> {hc.enabled ? "Open" : "Open (off)"}</Button></a>
              {canManage && <Button variant="ghost" onClick={() => setEditing(true)}>Configure and style</Button>}
              {canManage &&
                (hc.enabled ? (
                  <Button variant="ghost" onClick={async () => (await confirmAction("Turn the help centre off?", "Its pages stop answering until you turn it on again. Nothing is deleted.", "Turn off")) && patch({ enabled: false }, "The help centre is off.")}>Turn off</Button>
                ) : (
                  <Button onClick={() => patch({ enabled: true }, "Your help centre is live.")} disabled={hc.articles === 0} title={hc.articles === 0 ? "Turn on Help centre for at least one article first" : undefined}>Publish</Button>
                ))}
            </div>
          )
        }
      />
      {!hc ? (
        <GetStarted brandName={brand?.name} canManage={canManage} busy={busy} onCreate={create} />
      ) : (
        <Dashboard ws={ws} hc={hc} canManage={canManage} onPatch={patch} onEdit={() => setEditing(true)} go={go} />
      )}
    </div>
  );
}

function GetStarted({ brandName, canManage, busy, onCreate }: { brandName?: string; canManage: boolean; busy: boolean; onCreate: () => void }) {
  const points: [string, string][] = [
    ["Your articles, published", "Every article and quick answer you switch on for the Help centre, in your folders, nothing else."],
    ["Your look", "Starts from your logo and colour. Change the template, colours, fonts, header, hero and footer with a live preview."],
    ["Found by search engines and AI", "Pages are built for search, with a sitemap and an llms.txt for AI assistants."],
    ["Learns what's missing", "Searches that find nothing join your unanswered questions, so you know what to write next."],
  ];
  return (
    <div className="grid gap-6 rounded-lg border bg-card p-6 lg:grid-cols-[minmax(0,1fr)_360px]">
      <div className="space-y-4">
        <div>
          <h2 className="text-lg font-semibold">{brandName ? `A help centre for ${brandName}` : "Publish a help centre"}</h2>
          <p className="mt-1 text-[13px] text-muted-foreground">Let customers find answers themselves, from the same knowledge your team and AI use. It stays off until you publish it.</p>
        </div>
        <ul className="grid gap-3 sm:grid-cols-2">
          {points.map(([t, d]) => (
            <li key={t} className="flex gap-2.5">
              <Icon name="check" className="mt-0.5 size-4 shrink-0 text-green-600 dark:text-green-400" />
              <span>
                <span className="block text-[13px] font-medium">{t}</span>
                <span className="block text-xs text-muted-foreground">{d}</span>
              </span>
            </li>
          ))}
        </ul>
        {canManage ? <Button onClick={onCreate} disabled={busy}>{busy ? "Setting up…" : "Set up the help centre"}</Button> : <p className="text-xs text-muted-foreground">Ask someone who manages knowledge to set it up.</p>}
      </div>
      <MiniPreview />
    </div>
  );
}

// MiniPreview is a drawing of a help centre, for the empty page.
function MiniPreview() {
  return (
    <div aria-hidden="true" className="overflow-hidden rounded-lg border bg-white text-neutral-900 shadow-sm">
      <div className="bg-indigo-600 px-4 pt-3 pb-6 text-white">
        <div className="mb-4 flex items-center gap-2 text-[11px] font-semibold"><span className="size-3 rounded-sm bg-white/90" /> Your company</div>
        <p className="text-center text-sm font-bold">How can we help?</p>
        <div className="mx-auto mt-2 h-6 w-4/5 rounded-md bg-white" />
      </div>
      <div className="grid grid-cols-3 gap-2 p-3">
        {["Getting started", "Billing", "Account"].map((t) => (
          <div key={t} className="rounded-md border p-2">
            <div className="mb-1.5 size-4 rounded bg-indigo-100" />
            <p className="text-[10px] font-semibold">{t}</p>
            <p className="text-[9px] text-neutral-500">4 articles</p>
          </div>
        ))}
      </div>
    </div>
  );
}

function Dashboard({
  ws,
  hc,
  canManage,
  onPatch,
  onEdit,
  go,
}: {
  ws: string;
  hc: HelpCentreView;
  canManage: boolean;
  onPatch: (body: { domain_id?: string | null; slug?: string; name?: string }, done?: string) => Promise<boolean>;
  onEdit: () => void;
  go?: (tab: "knowledge" | "domains") => void;
}) {
  const [stats, setStats] = useState<HelpCentreStats | null>(null);
  useEffect(() => {
    void api.GET("/v1/workspaces/{ws}/help-centres/{id}/stats", { params: { path: { ws, id: hc.id } } }).then(({ data }) => data && setStats(data));
  }, [ws, hc.id]);
  const rated = (stats?.helpful ?? 0) + (stats?.unhelpful ?? 0);
  const max = Math.max(1, ...(stats?.by_day.map((d) => d.views) ?? [0]));
  return (
    <div className="space-y-6">
      {hc.articles === 0 && (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-amber-500/40 bg-amber-500/5 px-4 py-3 text-[13px]">
          <span>Nothing is on the help centre yet. Open an article in Knowledge and switch on <b>Help centre</b>.</span>
          {go && <Button variant="ghost" onClick={() => go("knowledge")}>Go to Knowledge</Button>}
        </div>
      )}

      <section className="grid gap-3 md:grid-cols-[minmax(0,1fr)_320px]">
        <div className="rounded-lg border bg-card p-4">
          <div className="flex items-baseline justify-between">
            <h2 className="text-sm font-semibold">Last 30 days</h2>
            <span className="text-xs text-muted-foreground">Views a day</span>
          </div>
          <div className="mt-3 grid grid-cols-4 gap-3">
            <Metric label="Page views" value={stats?.views} />
            <Metric label="Searches" value={stats?.searches} />
            <Metric label="Found nothing" value={stats?.empty_searches} hint="These join your unanswered questions" />
            <Metric label="Answered their question" value={rated ? `${Math.round(((stats?.helpful ?? 0) / rated) * 100)}%` : "–"} hint={rated ? `${rated} answers` : "No answers yet"} />
          </div>
          <div className="mt-4 flex h-16 items-end gap-[3px]" role="img" aria-label="Page views a day over the last 30 days">
            {(stats?.by_day ?? Array.from({ length: 30 }, () => ({ views: 0, day: "" }))).map((d, i) => (
              <span key={i} title={d.day ? `${d.day}: ${d.views}` : undefined} className="flex-1 rounded-sm bg-primary/70" style={{ height: `${Math.max(3, (d.views / max) * 100)}%`, opacity: d.views ? 1 : 0.25 }} />
            ))}
          </div>
        </div>
        <div className="space-y-3 rounded-lg border bg-card p-4 text-[13px]">
          <h2 className="text-sm font-semibold">Address</h2>
          <a href={hc.url} target="_blank" rel="noreferrer" className="block truncate font-mono text-xs underline underline-offset-2">{hc.url}</a>
          <Domain ws={ws} hc={hc} canManage={canManage} onPatch={onPatch} go={go} />
          <p className="text-xs text-muted-foreground">{hc.articles} {hc.articles === 1 ? "item" : "items"} published · {hc.settings.indexing ? "Search engines may list it" : "Hidden from search engines"}</p>
        </div>
      </section>

      <section className="grid gap-3 md:grid-cols-2">
        <div className="rounded-lg border bg-card">
          <h2 className="border-b px-4 py-2.5 text-sm font-semibold">Most read</h2>
          {!stats ? (
            <div className="p-4"><Loading /></div>
          ) : stats.articles.length === 0 ? (
            <p className="px-4 py-6 text-center text-[13px] text-muted-foreground">No reads yet. They show here once readers arrive.</p>
          ) : (
            <table className="w-full text-[13px]">
              <thead className="text-left text-xs text-muted-foreground">
                <tr><th className="w-full px-4 py-2 font-medium">Article</th><th className="px-3 py-2 text-right font-medium">Views</th><th className="px-4 py-2 text-right font-medium">Helpful</th></tr>
              </thead>
              <tbody className="divide-y">
                {stats.articles.slice(0, 8).map((a) => (
                  <tr key={a.item_id}>
                    <td className="max-w-0 truncate px-4 py-2">{a.title}</td>
                    <td className="px-3 py-2 text-right tabular-nums">{a.views}</td>
                    <td className="px-4 py-2 text-right whitespace-nowrap tabular-nums text-muted-foreground">{a.helpful + a.unhelpful ? `${a.helpful} 👍 ${a.unhelpful} 👎` : "–"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
        <Collections ws={ws} canManage={canManage} />
      </section>

      {canManage && (
        <section className="flex flex-wrap items-center justify-between gap-3 rounded-lg border bg-card px-4 py-3">
          <div>
            <h2 className="text-sm font-semibold">Look and layout</h2>
            <p className="text-xs text-muted-foreground">Template, colours, fonts, header, hero, sections, article page and footer, with a live preview.</p>
          </div>
          <Button onClick={onEdit}>Configure and style</Button>
        </section>
      )}
    </div>
  );
}

function Metric({ label, value, hint }: { label: string; value?: ReactNode; hint?: string }) {
  return (
    <div>
      <p className="text-xs text-muted-foreground">{label}</p>
      <p className="text-xl font-semibold tabular-nums">{value ?? "–"}</p>
      {hint && <p className="text-[11px] text-muted-foreground">{hint}</p>}
    </div>
  );
}

// Collections: the folders readers browse, with their icons.
function Collections({ ws, canManage }: { ws: string; canManage: boolean }) {
  const { brand, query: brandQuery } = useBrand(ws);
  const [folders, setFolders] = useState<KnowledgeCollection[] | null>(null);
  const [counts, setCounts] = useState<Record<string, number>>({});
  useEffect(() => {
    void api.GET("/v1/workspaces/{ws}/knowledge/collections", { params: { path: { ws }, query: brandQuery } }).then(({ data }) => data && setFolders(data.data));
    void api
      .GET("/v1/workspaces/{ws}/knowledge/items", { params: { path: { ws }, query: { ...brandQuery, visibility: "public", status: "published", limit: 200 } } })
      .then(({ data }) => {
        const c: Record<string, number> = {};
        for (const it of data?.data ?? []) c[it.collection_id ?? ""] = (c[it.collection_id ?? ""] ?? 0) + 1;
        setCounts(c);
      });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brand?.id]);

  async function setIcon(f: KnowledgeCollection, icon: string) {
    const { data, error } = await api.PATCH("/v1/workspaces/{ws}/knowledge/collections/{id}", { params: { path: { ws, id: f.id } }, body: { icon: icon as never } });
    if (error) return toast(errorMessage(error), "error");
    setFolders((l) => l?.map((x) => (x.id === data.id ? data : x)) ?? null);
  }

  const top = (folders ?? []).filter((f) => f.parent_id === null).sort((a, b) => a.position - b.position || a.name.localeCompare(b.name));
  return (
    <div className="rounded-lg border bg-card">
      <h2 className="border-b px-4 py-2.5 text-sm font-semibold">Collections</h2>
      {!folders ? (
        <div className="p-4"><Loading /></div>
      ) : top.length === 0 ? (
        <p className="px-4 py-6 text-center text-[13px] text-muted-foreground">Folders in Knowledge become collections here.</p>
      ) : (
        <ul className="divide-y text-[13px]">
          {top.map((f) => (
            <li key={f.id} className="flex items-center gap-3 px-4 py-2">
              <IconPicker value={f.icon || "book"} disabled={!canManage} onChange={(i) => setIcon(f, i)} label={`Icon for ${f.name}`} />
              <span className="min-w-0 flex-1 truncate font-medium">{f.name}</span>
              <span className="text-xs text-muted-foreground">{counts[f.id] ? `${counts[f.id]} published` : "Nothing published here"}</span>
            </li>
          ))}
        </ul>
      )}
      <p className="border-t px-4 py-2 text-[11px] text-muted-foreground">Subfolders show inside their collection. Order and rename folders in Knowledge.</p>
    </div>
  );
}

function IconPicker({ value, onChange, disabled, label }: { value: string; onChange: (v: string) => void; disabled?: boolean; label: string }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="relative">
      <button type="button" disabled={disabled} aria-label={label} aria-expanded={open} onClick={() => setOpen((o) => !o)} className="flex size-8 items-center justify-center rounded-md border bg-primary/10 text-primary hover:border-ring/60">
        <FolderIcon name={value} />
      </button>
      {open && (
        <div className="absolute top-9 left-0 z-20 grid w-56 grid-cols-6 gap-1 rounded-md border bg-popover p-1.5 shadow-md" role="listbox" aria-label={label}>
          {folderIcons.map((i) => (
            <button key={i} type="button" role="option" aria-selected={i === value} title={i} onClick={() => { onChange(i); setOpen(false); }} className={cn("flex size-8 items-center justify-center rounded hover:bg-accent", i === value && "bg-accent")}>
              <FolderIcon name={i} />
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

const folderIconPaths: Record<string, ReactNode> = {
  book: <path d="M4 19.5v-15A2.5 2.5 0 0 1 6.5 2H20v20H6.5a2.5 2.5 0 0 1 0-5H20" />,
  rocket: <><path d="M4.5 16.5c-1.5 1.26-2 5-2 5s3.74-.5 5-2c.71-.84.7-2.13-.09-2.91a2.18 2.18 0 0 0-2.91-.09z" /><path d="m12 15-3-3a22 22 0 0 1 2-3.95A12.88 12.88 0 0 1 22 2c0 2.72-.78 7.5-6 11a22.35 22.35 0 0 1-4 2z" /></>,
  card: <><rect width="20" height="14" x="2" y="5" rx="2" /><path d="M2 10h20" /></>,
  shield: <path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z" />,
  settings: <><circle cx="12" cy="12" r="3" /><path d="M12 2v3M12 19v3M2 12h3M19 12h3M4.9 4.9l2.1 2.1M17 17l2.1 2.1M4.9 19.1 7 17M17 7l2.1-2.1" /></>,
  users: <><path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2" /><circle cx="9" cy="7" r="4" /><path d="M22 21v-2a4 4 0 0 0-3-3.87" /></>,
  chat: <path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z" />,
  bolt: <path d="M13 2 3 14h9l-1 8 10-12h-9l1-8z" />,
  star: <path d="m12 2 3.09 6.26L22 9.27l-5 4.87 1.18 6.88L12 17.77l-6.18 3.25L7 14.14 2 9.27l6.91-1.01L12 2z" />,
  globe: <><circle cx="12" cy="12" r="10" /><path d="M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20" /><path d="M2 12h20" /></>,
  help: <><circle cx="12" cy="12" r="10" /><path d="M9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3" /><path d="M12 17h.01" /></>,
  tag: <><path d="M12 2H2v10l9.29 9.29c.94.94 2.48.94 3.42 0l6.58-6.58c.94-.94.94-2.48 0-3.42L12 2Z" /><path d="M7 7h.01" /></>,
  box: <><path d="M21 8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16Z" /><path d="m3.3 7 8.7 5 8.7-5" /><path d="M12 22V12" /></>,
  chart: <><path d="M3 3v18h18" /><path d="M7 16v-4" /><path d="M11 16V8" /><path d="M15 16v-6" /><path d="M19 16V5" /></>,
  plug: <><path d="M12 22v-5" /><path d="M9 8V2" /><path d="M15 8V2" /><path d="M18 8v5a4 4 0 0 1-4 4h-4a4 4 0 0 1-4-4V8Z" /></>,
  lock: <><rect width="18" height="11" x="3" y="11" rx="2" /><path d="M7 11V7a5 5 0 0 1 10 0v4" /></>,
};

export function FolderIcon({ name, className = "size-4" }: { name: string; className?: string }) {
  return (
    <svg viewBox="0 0 24 24" className={className} fill="none" stroke="currentColor" strokeWidth={1.75} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      {folderIconPaths[name] ?? folderIconPaths.book}
    </svg>
  );
}

type TrackingDomain = { id: string; domain: string; status: string; target: string; partner_program_id: string | null; help_centre_id: string | null; https_status?: string | null };

// Domain: the help centre on a domain of your own (a CNAME to Outflare).
function Domain({ ws, hc, canManage, onPatch, go }: { ws: string; hc: HelpCentreView; canManage: boolean; onPatch: (b: { domain_id: string | null }, done?: string) => Promise<boolean>; go?: (tab: "knowledge" | "domains") => void }) {
  const [domains, setDomains] = useState<TrackingDomain[] | null>(null);
  const [adding, setAdding] = useState(false);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/workspaces/{ws}/tracking-domains", { params: { path: { ws } } });
    if (data) setDomains(data.data as TrackingDomain[]);
  }, [ws]);
  useEffect(() => void load(), [load]);

  const mine = domains?.find((d) => d.id === hc.domain_id);
  const free = (domains ?? []).filter((d) => !d.partner_program_id && (!d.help_centre_id || d.help_centre_id === hc.id) && d.id !== hc.domain_id);

  async function add() {
    const domain = name.trim().toLowerCase();
    if (!domain) return;
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/tracking-domains", { params: { path: { ws } }, body: { domain } });
    if (error) {
      setBusy(false);
      return toast(errorMessage(error), "error");
    }
    await onPatch({ domain_id: data.id }, "Domain added. Point its CNAME at the target shown.");
    setBusy(false);
    setAdding(false);
    setName("");
    load();
  }

  if (mine) {
    const live = mine.status === "verified";
    return (
      <div className="space-y-1.5 rounded-md border px-3 py-2">
        <div className="flex items-center gap-2">
          <span className="min-w-0 flex-1 truncate font-medium">{mine.domain}</span>
          <Status tone={live ? "green" : mine.status === "failed" ? "red" : "amber"}>{live ? "Connected" : mine.status === "failed" ? "Not found" : "Waiting for DNS"}</Status>
        </div>
        {!live && (
          <p className="text-xs text-muted-foreground">
            Add a CNAME record for <code className="font-mono">{mine.domain}</code> pointing to <code className="font-mono">{mine.target}</code>. It's checked every few minutes{go && <>; <button type="button" className="underline underline-offset-2" onClick={() => go("domains")}>see it in Domains</button></>}.
          </p>
        )}
        {canManage && <button type="button" className="text-xs text-muted-foreground hover:text-foreground" onClick={() => onPatch({ domain_id: null }, "The help centre is back on its Outflare address.")}>Stop using this domain</button>}
      </div>
    );
  }
  if (!canManage) return <p className="text-xs text-muted-foreground">No domain of your own yet.</p>;
  if (!adding)
    return (
      <div className="flex flex-wrap gap-2">
        <Button variant="ghost" onClick={() => setAdding(true)}><Icon name="plus" className="size-3.5" /> Use your own domain</Button>
        {free.length > 0 && (
          <select aria-label="Use a domain you've added" className={field} value="" onChange={(e) => e.target.value && onPatch({ domain_id: e.target.value }, "Domain set.")}>
            <option value="">Or pick one you've added…</option>
            {free.map((d) => <option key={d.id} value={d.id}>{d.domain}</option>)}
          </select>
        )}
      </div>
    );
  return (
    <form className="flex gap-2" onSubmit={(e) => { e.preventDefault(); void add(); }}>
      <input autoFocus aria-label="Domain" className={cn(field, "min-w-0 flex-1")} placeholder="help.yourcompany.com" value={name} onChange={(e) => setName(e.target.value)} />
      <Button type="submit" disabled={busy || !name.trim()}>{busy ? "Adding…" : "Add"}</Button>
      <Button variant="ghost" type="button" onClick={() => setAdding(false)}>Cancel</Button>
    </form>
  );
}

// Designer is Configure and style: the settings, grouped like the page
// they change, beside a live preview of the real page.
function Designer({ ws, hc, canManage, onClose, onSaved }: { ws: string; hc: HelpCentreView; canManage: boolean; onClose: () => void; onSaved: (v: HelpCentreView) => void }) {
  const { byId } = useBrand(ws);
  const [set, setSet] = useState<Settings>(hc.settings);
  const [name, setName] = useState(hc.name);
  const [slug, setSlug] = useState(hc.slug);
  const [saved, setSaved] = useState(JSON.stringify({ s: hc.settings, n: hc.name, g: hc.slug }));
  const [page, setPage] = useState<PreviewPage>("home");
  const [device, setDevice] = useState<"desktop" | "mobile">("desktop");
  const [html, setHtml] = useState("");
  const [previewError, setPreviewError] = useState("");
  const [busy, setBusy] = useState(false);
  const [section, setSection] = useState("templates");
  const dirty = JSON.stringify({ s: set, n: name, g: slug }) !== saved;
  const seq = useRef(0);

  const put = <K extends keyof Settings>(k: K, v: Settings[K]) => setSet((s) => ({ ...s, [k]: v }));
  // What is sent: link rows still being typed are left out.
  const ready = useMemo(() => tidy(set), [set]);

  // The preview follows every change, a moment after typing stops.
  useEffect(() => {
    const n = ++seq.current;
    const t = setTimeout(async () => {
      const { data, error } = await api.POST("/v1/workspaces/{ws}/help-centres/{id}/preview", {
        params: { path: { ws, id: hc.id } },
        body: { settings: ready, name, page },
        parseAs: "text",
      });
      if (n !== seq.current) return;
      if (error) return setPreviewError(errorMessage(error));
      setPreviewError("");
      setHtml(data as unknown as string);
    }, 250);
    return () => clearTimeout(t);
  }, [ws, hc.id, ready, name, page]);

  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  async function save() {
    setBusy(true);
    const { data, error } = await api.PATCH("/v1/workspaces/{ws}/help-centres/{id}", {
      params: { path: { ws, id: hc.id } },
      body: { settings: ready, ...(name !== hc.name ? { name } : {}), ...(slug !== hc.slug ? { slug } : {}) },
    });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    onSaved(data);
    setSet(data.settings);
    setSaved(JSON.stringify({ s: data.settings, n: data.name, g: data.slug }));
    toast(hc.enabled ? "Saved. Your help centre shows it now." : "Saved.");
  }

  async function close() {
    if (dirty && !(await confirmAction("Leave without saving?", "Your changes to the help centre will be lost.", "Leave"))) return;
    onClose();
  }

  async function importBrand() {
    let logo = "";
    let colors: { primary?: string; background?: string; text?: string } = {};
    if (hc.brand_id) {
      const { data } = await api.GET("/v1/workspaces/{ws}/brands/{brand}", { params: { path: { ws, brand: hc.brand_id } } });
      logo = data?.logo_url ?? "";
      colors = data?.colors ?? {};
    }
    if (!logo || !colors.primary) {
      const { data } = await api.GET("/v1/workspaces/{ws}/context", { params: { path: { ws } } });
      logo ||= data?.context.logo_url ?? "";
      colors = { ...(data?.context.colors ?? {}), ...Object.fromEntries(Object.entries(colors).filter(([, v]) => v)) };
    }
    track("ui.help_centre_brand_imported", { found_logo: !!logo, found_color: !!colors.primary });
    if (!logo && !colors.primary) return toast("No logo or colours saved yet. Add them under Knowledge, About your company.", "error");
    setSet((s) => ({ ...s, ...(logo ? { logo_url: logo } : {}), ...(colors.primary ? { color: colors.primary } : {}) }));
    toast("Your brand's logo and colour are in. Save to keep them.");
  }

  const sections: { id: string; title: string; body: ReactNode }[] = [
    {
      id: "templates",
      title: "Template",
      body: (
        <div className="space-y-2.5">
          <div className="grid gap-2">
            {templates.map((t) => (
              <button
                key={t.id}
                type="button"
                aria-pressed={set.template === t.id}
                onClick={() => {
                  setSet((s) => ({ ...s, ...t.set }));
                  track("ui.help_centre_template_picked", { template: t.id });
                }}
                className={cn("flex items-center gap-3 rounded-md border p-2 text-left", set.template === t.id ? "border-ring bg-accent/50" : "hover:bg-accent/30")}
              >
                <TemplateThumb id={t.id} color={set.color ?? "#4f46e5"} />
                <span>
                  <span className="block text-[13px] font-medium">{t.name}</span>
                  <span className="block text-xs text-muted-foreground">{t.blurb}</span>
                </span>
              </button>
            ))}
          </div>
          <Button variant="ghost" onClick={importBrand}><Icon name="brands" className="size-3.5" /> Import your brand</Button>
          <p className="text-xs text-muted-foreground">Takes the logo and main colour from {hc.brand_id ? byId(hc.brand_id)?.name ?? "the brand" : "your company"}.</p>
        </div>
      ),
    },
    {
      id: "general",
      title: "General",
      body: (
        <div className="space-y-3">
          <Text label="Name" value={name} onChange={setName} max={100} hint="Shown in the header and browser tab." />
          <Text label="Address" value={slug} onChange={(v) => setSlug(v.toLowerCase().replace(/[^a-z0-9-]/g, "-"))} max={63} hint={`On Outflare: …/h/${slug}/`} />
          <Text label="Title in search results" value={set.title ?? ""} onChange={(v) => put("title", v)} max={100} placeholder={name} />
          <label className="block">
            <span className="mb-1 block text-[13px] font-medium">Language</span>
            <select className={cn(field, "w-full")} value={set.language ?? "en"} onChange={(e) => put("language", e.target.value)}>
              {[["en", "English"], ["en-gb", "English (UK)"], ["fr", "French"], ["de", "German"], ["es", "Spanish"], ["pt", "Portuguese"], ["it", "Italian"], ["nl", "Dutch"], ["sv", "Swedish"], ["da", "Danish"], ["pl", "Polish"], ["ja", "Japanese"]].map(([v, l]) => <option key={v} value={v}>{l}</option>)}
            </select>
          </label>
          <Toggle label="Let search engines list it" hint="Off hides it from Google and others; people with the link can still read it." checked={!!set.indexing} onChange={(v) => put("indexing", v)} />
          <Text label="Social image" value={set.social_image_url ?? ""} onChange={(v) => put("social_image_url", v)} placeholder="https://… 1200 × 630 image" hint="Shown when a page is shared." />
          <Text label="Favicon" value={set.favicon_url ?? ""} onChange={(v) => put("favicon_url", v)} placeholder="https://… .png or .ico" />
        </div>
      ),
    },
    {
      id: "shared",
      title: "Colours and fonts",
      body: (
        <div className="space-y-3">
          <Color label="Accent" hint="Buttons, links and the header" value={set.color ?? "#4f46e5"} onChange={(v) => put("color", v)} />
          <Color label="Page background" value={set.background ?? "#ffffff"} onChange={(v) => put("background", v)} />
          <label className="block">
            <span className="mb-1 block text-[13px] font-medium">Font</span>
            <select className={cn(field, "w-full")} value={set.font ?? "system"} onChange={(e) => put("font", e.target.value as Settings["font"])}>
              {fonts.map((f) => <option key={f.id} value={f.id}>{f.name}</option>)}
            </select>
          </label>
          <Segmented label="Corners" value={set.radius ?? "medium"} options={[["none", "Square"], ["small", "Small"], ["medium", "Medium"], ["large", "Round"]]} onChange={(v) => put("radius", v as Settings["radius"])} />
          <Toggle label="Show authors" hint="Each article's owner, as its author." checked={!!set.show_authors} onChange={(v) => put("show_authors", v)} />
        </div>
      ),
    },
    {
      id: "header",
      title: "Header",
      body: (
        <div className="space-y-3">
          <Text label="Logo" value={set.logo_url ?? ""} onChange={(v) => put("logo_url", v)} placeholder="https://…/logo.svg" />
          <Toggle label="Show the name beside the logo" checked={!!set.show_title} onChange={(v) => put("show_title", v)} />
          <Color label="Header colour" hint="Empty uses the accent" value={set.header_color ?? ""} empty onChange={(v) => put("header_color", v)} />
          <Text label="Website" value={set.website_url ?? ""} onChange={(v) => put("website_url", v)} placeholder="https://yourcompany.com" hint={`Adds "Go to website" to the header.`} />
          <Links label="Header links" max={4} value={set.header_links ?? []} onChange={(v) => put("header_links", v)} />
        </div>
      ),
    },
    {
      id: "hero",
      title: "Hero and search",
      body: (
        <div className="space-y-3">
          <Text label="Title" value={set.hero_title ?? ""} onChange={(v) => put("hero_title", v)} max={120} />
          <Text label="Subtitle" value={set.hero_subtitle ?? ""} onChange={(v) => put("hero_subtitle", v)} max={300} placeholder="Answers from the Northpeak team" />
          <Text label="Search placeholder" value={set.search_placeholder ?? ""} onChange={(v) => put("search_placeholder", v)} max={80} />
          <Text label="Background image" value={set.hero_image_url ?? ""} onChange={(v) => put("hero_image_url", v)} placeholder="https://… (optional)" hint="Shown behind the header colour." />
        </div>
      ),
    },
    {
      id: "layout",
      title: "Layout and sections",
      body: (
        <div className="space-y-3">
          <Segmented label="Collections" value={set.collections_layout ?? "grid"} options={[["grid", "Cards"], ["list", "List"]]} onChange={(v) => put("collections_layout", v as Settings["collections_layout"])} />
          {set.collections_layout !== "list" && <Segmented label="Cards per row" value={String(set.columns ?? 3)} options={[["1", "1"], ["2", "2"], ["3", "3"]]} onChange={(v) => put("columns", Number(v))} />}
          <Toggle label="Popular articles" hint="The most read, once readers arrive." checked={!!set.show_popular} onChange={(v) => put("show_popular", v)} />
          <Toggle label="Frequently asked questions" hint="Quick answers as questions and answers." checked={!!set.show_faq} onChange={(v) => put("show_faq", v)} />
          <div className="space-y-2 rounded-md border p-2.5">
            <p className="text-[13px] font-medium">Contact block</p>
            <Text label="Link" value={set.contact_url ?? ""} onChange={(v) => put("contact_url", v)} placeholder="mailto:support@… or https://…" hint="Empty hides the block." />
            {set.contact_url && (
              <>
                <Text label="Title" value={set.contact_title ?? ""} onChange={(v) => put("contact_title", v)} max={80} />
                <Text label="Text" value={set.contact_text ?? ""} onChange={(v) => put("contact_text", v)} max={300} />
                <Text label="Button" value={set.contact_label ?? ""} onChange={(v) => put("contact_label", v)} max={40} />
              </>
            )}
          </div>
        </div>
      ),
    },
    {
      id: "collection",
      title: "Collection pages",
      body: (
        <div className="space-y-3">
          <Toggle label="Descriptions" hint="Each folder's description, from Knowledge." checked={!!set.show_descriptions} onChange={(v) => put("show_descriptions", v)} />
          <Toggle label="Article excerpts" hint="The first lines of each article in lists." checked={!!set.show_excerpts} onChange={(v) => put("show_excerpts", v)} />
          <Toggle label="Article counts" checked={!!set.show_counts} onChange={(v) => put("show_counts", v)} />
        </div>
      ),
    },
    {
      id: "article",
      title: "Article pages",
      body: (
        <div className="space-y-3">
          <Toggle label="Table of contents" hint="The article's headings beside it." checked={!!set.table_of_contents} onChange={(v) => put("table_of_contents", v)} />
          <Toggle label="Related articles" hint="Others from the same collection." checked={!!set.related} onChange={(v) => put("related", v)} />
          <Toggle label="Copy for AI" hint="A button that copies the article as Markdown for ChatGPT, Claude and others." checked={!!set.copy_for_llm} onChange={(v) => put("copy_for_llm", v)} />
          <Toggle label="Did this answer your question?" hint="Readers' answers show in the numbers." checked={!!set.feedback} onChange={(v) => put("feedback", v)} />
          <Toggle label="Last updated date" checked={!!set.show_updated} onChange={(v) => put("show_updated", v)} />
        </div>
      ),
    },
    {
      id: "footer",
      title: "Footer",
      body: (
        <div className="space-y-3">
          <Segmented label="Layout" value={set.footer_layout ?? "simple"} options={[["simple", "Centred"], ["columns", "Columns"]]} onChange={(v) => put("footer_layout", v as Settings["footer_layout"])} />
          <Text label="Text" value={set.footer_text ?? ""} onChange={(v) => put("footer_text", v)} max={300} placeholder="© Northpeak" />
          <Color label="Footer colour" hint="Empty uses the page background" value={set.footer_color ?? ""} empty onChange={(v) => put("footer_color", v)} />
          <Links label="Links" max={12} value={set.footer_links ?? []} onChange={(v) => put("footer_links", v)} />
          <div className="space-y-2">
            <p className="text-[13px] font-medium">Social profiles</p>
            {socials.map((s) => (
              <label key={s.id} className="flex items-center gap-2">
                <span className="w-20 text-xs text-muted-foreground">{s.name}</span>
                <input className={cn(field, "min-w-0 flex-1")} value={set.social?.[s.id] ?? ""} placeholder="https://…" onChange={(e) => put("social", { ...(set.social ?? {}), [s.id]: e.target.value })} />
              </label>
            ))}
          </div>
        </div>
      ),
    },
  ];

  const pages: [PreviewPage, string][] = [["home", "Home"], ["collection", "Collection"], ["article", "Article"], ["search", "Search"]];
  return (
    <div className="space-y-4">
      <PageHeader
        back={{ label: "Help centre", onClick: close }}
        title="Configure and style"
        description="Every change shows in the preview straight away. Save to put it on your help centre."
        action={
          <div className="flex items-center gap-2">
            {dirty && <span className="text-xs text-amber-700 dark:text-amber-400">Unsaved changes</span>}
            {dirty && <Button variant="ghost" onClick={() => { const o = JSON.parse(saved); setSet(o.s); setName(o.n); setSlug(o.g); }}>Discard</Button>}
            {canManage && <Button onClick={save} disabled={busy || !dirty}>{busy ? "Saving…" : "Save"}</Button>}
          </div>
        }
      />
      <div className="grid gap-4 lg:grid-cols-[340px_minmax(0,1fr)]">
        <aside className="space-y-1.5 lg:max-h-[calc(100vh-200px)] lg:overflow-y-auto lg:pr-1">
          <fieldset disabled={!canManage} className="min-w-0 space-y-1.5">
            {sections.map((s) => (
              <div key={s.id} className="rounded-lg border bg-card">
                <button type="button" aria-expanded={section === s.id} onClick={() => setSection(section === s.id ? "" : s.id)} className="flex w-full items-center justify-between px-3 py-2.5 text-left text-[13px] font-medium">
                  {s.title}
                  <Icon name="chevron" className={cn("size-3.5 text-muted-foreground transition-transform", section === s.id && "rotate-90")} />
                </button>
                {section === s.id && <div className="border-t px-3 py-3">{s.body}</div>}
              </div>
            ))}
          </fieldset>
        </aside>
        <section aria-label="Preview" className="min-w-0 rounded-lg border bg-muted/40">
          <div className="flex flex-wrap items-center gap-2 border-b px-3 py-2">
            <div className="flex rounded-md border bg-background p-0.5" role="group" aria-label="Page">
              {pages.map(([p, l]) => (
                <button key={p} type="button" aria-pressed={page === p} onClick={() => setPage(p)} className={cn("rounded px-2.5 py-1 text-[12px]", page === p ? "bg-accent font-medium" : "text-muted-foreground")}>{l}</button>
              ))}
            </div>
            <div className="ml-auto flex rounded-md border bg-background p-0.5" role="group" aria-label="Screen">
              {(["desktop", "mobile"] as const).map((d) => (
                <button key={d} type="button" aria-pressed={device === d} onClick={() => setDevice(d)} className={cn("rounded px-2.5 py-1 text-[12px]", device === d ? "bg-accent font-medium" : "text-muted-foreground")}>{d === "desktop" ? "Desktop" : "Phone"}</button>
              ))}
            </div>
          </div>
          <div className="relative flex justify-center p-3">
            {previewError && (
              <p role="status" className="absolute z-10 mt-2 rounded-md border border-amber-500/40 bg-background px-3 py-1.5 text-xs text-amber-700 shadow-sm dark:text-amber-400">
                Preview paused: {previewError}
              </p>
            )}
            <PreviewFrame html={html} device={device} />
          </div>
        </section>
      </div>
    </div>
  );
}

// PreviewFrame shows the page at a real screen width (1280px for desktop,
// 390px for a phone), scaled down to fit, so columns and breakpoints look
// as they will.
function PreviewFrame({ html, device }: { html: string; device: "desktop" | "mobile" }) {
  const box = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setWidth(el.clientWidth));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  const real = device === "desktop" ? 1280 : 390;
  const scale = width ? Math.min(1, width / real) : 1;
  const height = Math.max(520, window.innerHeight - 260);
  return (
    <div ref={box} className="w-full" style={{ height }}>
      <div className="mx-auto overflow-hidden rounded-md border bg-white shadow-sm" style={{ width: real * scale, height }}>
        <iframe
          title="Help centre preview"
          sandbox=""
          srcDoc={html}
          style={{ width: real, height: height / scale, transform: `scale(${scale})`, transformOrigin: "0 0", border: 0 }}
        />
      </div>
    </div>
  );
}

// tidy leaves out link rows with nothing in them and social profiles left
// empty, so half-typed rows don't stop the preview or a save.
function tidy(s: Settings): Settings {
  const links = (l?: Link[]) => (l ?? []).filter((x) => x.label.trim() || x.url.trim());
  return {
    ...s,
    header_links: links(s.header_links),
    footer_links: links(s.footer_links),
    social: Object.fromEntries(Object.entries(s.social ?? {}).filter(([, v]) => v?.trim())),
  };
}

function TemplateThumb({ id, color }: { id: string; color: string }) {
  const header = id === "minimal" ? "#ffffff" : color;
  return (
    <span aria-hidden="true" className="block h-12 w-16 shrink-0 overflow-hidden rounded border bg-white">
      <span className="block" style={{ background: header, height: id === "bold" ? 26 : 18, borderBottom: id === "minimal" ? "1px solid #e5e7eb" : undefined }}>
        <span className="mx-auto block h-1.5 w-8 translate-y-2 rounded-full bg-white/90" style={{ background: id === "minimal" ? color : undefined }} />
      </span>
      <span className={cn("grid gap-0.5 p-1", id === "minimal" ? "grid-cols-1" : id === "bold" ? "grid-cols-2" : "grid-cols-3")}>
        {Array.from({ length: id === "minimal" ? 2 : id === "bold" ? 2 : 3 }, (_, i) => (
          <span key={i} className="block h-2.5 border border-neutral-200" style={{ borderRadius: id === "bold" ? 4 : id === "minimal" ? 1 : 2 }} />
        ))}
      </span>
    </span>
  );
}

function Text({ label, value, onChange, max, placeholder, hint }: { label: string; value: string; onChange: (v: string) => void; max?: number; placeholder?: string; hint?: string }) {
  return (
    <label className="block">
      <span className="mb-1 block text-[13px] font-medium">{label}</span>
      <input className={cn(field, "w-full")} value={value} maxLength={max} placeholder={placeholder} onChange={(e) => onChange(e.target.value)} />
      {hint && <span className="mt-1 block text-xs text-muted-foreground">{hint}</span>}
    </label>
  );
}

function Toggle({ label, hint, checked, onChange }: { label: string; hint?: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <div className="flex items-start justify-between gap-3">
      <span>
        <span className="block text-[13px] font-medium">{label}</span>
        {hint && <span className="block text-xs text-muted-foreground">{hint}</span>}
      </span>
      <Switch checked={checked} onCheckedChange={onChange} aria-label={label} />
    </div>
  );
}

function Segmented({ label, value, options, onChange }: { label: string; value: string; options: [string, string][]; onChange: (v: string) => void }) {
  return (
    <div>
      <p className="mb-1 text-[13px] font-medium">{label}</p>
      <div className="flex rounded-md border p-0.5" role="group" aria-label={label}>
        {options.map(([v, l]) => (
          <button key={v} type="button" aria-pressed={value === v} onClick={() => onChange(v)} className={cn("flex-1 rounded px-2 py-1 text-[12px]", value === v ? "bg-accent font-medium" : "text-muted-foreground")}>{l}</button>
        ))}
      </div>
    </div>
  );
}

function Color({ label, hint, value, onChange, empty }: { label: string; hint?: string; value: string; onChange: (v: string) => void; empty?: boolean }) {
  const valid = /^#([0-9a-f]{3}|[0-9a-f]{6})$/i.test(value);
  return (
    <div>
      <p className="mb-1 text-[13px] font-medium">{label}</p>
      <div className="flex items-center gap-2">
        <input type="color" aria-label={`${label} picker`} className="h-8 w-9 cursor-pointer rounded border bg-transparent p-0.5" value={valid && value.length === 7 ? value : "#ffffff"} onChange={(e) => onChange(e.target.value)} />
        <input aria-label={label} className={cn(field, "w-28 font-mono")} value={value} placeholder={empty ? "Automatic" : "#4f46e5"} onChange={(e) => onChange(e.target.value.trim())} />
        {empty && value && <button type="button" className="text-xs text-muted-foreground hover:text-foreground" onClick={() => onChange("")}>Automatic</button>}
      </div>
      {hint && <p className="mt-1 text-xs text-muted-foreground">{hint}</p>}
    </div>
  );
}

function Links({ label, value, onChange, max }: { label: string; value: Link[]; onChange: (v: Link[]) => void; max: number }) {
  const rows = useMemo(() => value, [value]);
  return (
    <div className="space-y-1.5">
      <p className="text-[13px] font-medium">{label}</p>
      {rows.map((l, i) => (
        <div key={i} className="flex gap-1.5">
          <input aria-label={`${label} ${i + 1} label`} className={cn(field, "w-24")} value={l.label} placeholder="Label" onChange={(e) => onChange(rows.map((x, j) => (j === i ? { ...x, label: e.target.value } : x)))} />
          <input aria-label={`${label} ${i + 1} address`} className={cn(field, "min-w-0 flex-1")} value={l.url} placeholder="https://…" onChange={(e) => onChange(rows.map((x, j) => (j === i ? { ...x, url: e.target.value } : x)))} />
          <button type="button" aria-label={`Remove ${l.label || "link"}`} className="px-1 text-muted-foreground hover:text-foreground" onClick={() => onChange(rows.filter((_, j) => j !== i))}>
            <Icon name="x" className="size-3.5" />
          </button>
        </div>
      ))}
      {rows.length < max && (
        <button type="button" className="text-xs text-muted-foreground hover:text-foreground" onClick={() => onChange([...rows, { label: "", url: "" }])}>+ Add a link</button>
      )}
    </div>
  );
}

