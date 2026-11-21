import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Replayer } from "@rrweb/replay";
import "@rrweb/replay/dist/style.css";
import type { Schemas } from "@outflare/sdk";
import { PickBrand } from "@/components/pick-brand";
import { DataTable } from "@/components/data-table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Switch } from "@/components/ui/switch";
import { pickedBrandId, useBrand } from "@/lib/brand";
import { api, errorMessage } from "../api";
import { Button, Card, Empty, ErrorText, field, Loading, PageHeader, Stat, Stats, confirmAction, toast } from "../ui";
import { ago, stamp } from "../format";
import type { Tab } from "../App";

// Session recordings: watch a visit to the brand's website back, as the
// visitor saw it. The list is a table; a row opens the visit full screen.
// Nothing a visitor typed is ever in a recording.

export type Recording = Schemas["Recording"];
export type ReplaySettings = Schemas["ReplaySettings"];
type RecordedEvent = { type: number; timestamp: number; data?: unknown };

const brandPath = () => ({ path: { brand: pickedBrandId() } });

export function clock(seconds: number) {
  const s = Math.max(0, Math.round(seconds));
  const m = Math.floor(s / 60);
  return m >= 60 ? `${Math.floor(m / 60)}h ${m % 60}m` : `${m}:${String(s % 60).padStart(2, "0")}`;
}
const deviceName: Record<string, string> = { desktop: "Desktop", tablet: "Tablet", mobile: "Phone" };
const regionNames = (() => {
  try {
    return new Intl.DisplayNames(["en"], { type: "region" });
  } catch {
    return null;
  }
})();
const title = (s: string) => (s ? s[0].toUpperCase() + s.slice(1) : "");

export function Recordings(props: { ws: string; canManage: boolean; go: (t: Tab, item?: string) => void; item?: string }) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="Recordings" brands={active.length} />;
  if (props.item && props.item !== "settings") return <Watch key={props.item} id={props.item} canManage={props.canManage} go={props.go} />;
  return <RecordingList key={brand.id} {...props} />;
}

// excludePages adds pages to the brand's "Never these pages", after the
// person agrees. True when they were added.
export async function excludePages(settings: ReplaySettings, save: (c: Partial<Omit<ReplaySettings, "storage">>) => Promise<boolean>, pages: string[], also: string): Promise<boolean> {
  const add = [...new Set(pages.map((p) => (p.length > 1 ? p.replace(/\/+$/, "") : p)))].filter((p) => p.startsWith("/") && !settings.exclude_paths.includes(p));
  if (add.length === 0) {
    toast("Those pages are already left out of tracking");
    return false;
  }
  if (settings.exclude_paths.length + add.length > 50) {
    toast("At most 50 pages can be left out. Use * in Settings to cover many at once, such as /account/*.", "error");
    return false;
  }
  const shown = add.slice(0, 8).join(", ") + (add.length > 8 ? ` and ${add.length - 8} more` : "");
  if (!(await confirmAction(`Stop tracking ${add.length === 1 ? "this page" : `these ${add.length} pages`}?`, `Recordings and heatmaps leave out ${shown} from now on. ${also} You can bring a page back under Settings, Never these pages.`, "Exclude from tracking"))) return false;
  if (!(await save({ exclude_paths: [...settings.exclude_paths, ...add] }))) return false;
  toast(`${add.length} page${add.length === 1 ? "" : "s"} left out of tracking`);
  return true;
}

// useReplaySettings reads the brand's settings and saves changes to them.
export function useReplaySettings() {
  const [settings, setSettings] = useState<ReplaySettings | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    api.GET("/v1/brands/{brand}/replay/settings", { params: brandPath() }).then(({ data, error }) => (error || !data ? setError(errorMessage(error)) : setSettings(data)));
  }, []);
  const save = useCallback(async (change: Partial<Omit<ReplaySettings, "storage">>) => {
    const { data, error } = await api.PATCH("/v1/brands/{brand}/replay/settings", { params: brandPath(), body: change });
    if (error || !data || !("recordings" in data)) {
      toast(errorMessage(error), "error");
      return false;
    }
    setSettings(data);
    return true;
  }, []);
  return { settings, error, save };
}

function RecordingList({ canManage, go, item }: { canManage: boolean; go: (t: Tab, item?: string) => void; item?: string }) {
  const { settings, error: settingsError, save } = useReplaySettings();
  const [rows, setRows] = useState<Recording[] | null>(null);
  const [error, setError] = useState("");
  const [days, setDays] = useState(30);
  const [device, setDevice] = useState("");
  const [minSeconds, setMinSeconds] = useState(0);
  const [path, setPath] = useState("");
  const [host, setHost] = useState("");
  const [who, setWho] = useState("");
  const [sites, setSites] = useState<string[]>([]);
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [reload, setReload] = useState(0);
  const view = item === "settings" ? "settings" : "list";
  const toggle = (id: string, on: boolean) => setPicked((old) => {
    const next = new Set(old);
    if (on) next.add(id); else next.delete(id);
    return next;
  });
  // Deletes the ticked recordings, or every one older than a number of days.
  const remove = async (older?: number) => {
    const ids = [...picked];
    const what = older === undefined ? `${ids.length} recording${ids.length === 1 ? "" : "s"}` : older === 0 ? "every recording" : `every recording older than ${older} days`;
    if (!(await confirmAction(`Delete ${what}?`, "They are gone for good, for everyone in the workspace.", "Delete"))) return;
    const { data, error } = await api.POST("/v1/brands/{brand}/recordings/delete", { params: brandPath(), body: older === undefined ? { ids } : { older_than_days: older } });
    if (error || !data || !("deleted" in data)) return toast(errorMessage(error), "error");
    toast(`${data.deleted.toLocaleString()} recording${data.deleted === 1 ? "" : "s"} deleted`);
    setPicked(new Set());
    setReload((n) => n + 1);
  };

  useEffect(() => {
    if (view !== "list") return;
    const t = setTimeout(() => {
      api.GET("/v1/brands/{brand}/recordings", { params: { ...brandPath(), query: { days, limit: 200, ...(device ? { device: device as Recording["device"] } : {}), ...(minSeconds ? { min_seconds: minSeconds } : {}), ...(path.startsWith("/") ? { path } : {}), ...(host ? { host } : {}), ...(who ? { visitor: who as "known" | "anonymous" } : {}) } } }).then(({ data, error }) => {
        if (error || !data) return setError(errorMessage(error));
        setError("");
        setRows(data.data);
        setSites(data.sites);
      });
    }, path ? 300 : 0);
    return () => clearTimeout(t);
  }, [days, device, minSeconds, path, host, who, view, reload]);

  const on = settings?.recordings && settings.storage;
  return (
    <div className="space-y-5">
      <PageHeader
        title="Recordings"
        description="Watch a visit to your website back, as the visitor saw it: where they moved, clicked and got stuck. Nothing a visitor types is ever recorded."
        action={settings ? (
          <div className="flex items-center gap-3">
            <label className="flex items-center gap-2 text-[13px] font-medium" title={settings.storage ? undefined : "This server has no file storage, which recordings need."}>
              <Switch aria-label="Recordings on or off" checked={!!on} disabled={!canManage || !settings.storage} onCheckedChange={(v) => save({ recordings: v })} />
            {on ? "On" : "Off"}
            </label>
            {canManage && view === "list" && (rows?.length ?? 0) > 0 && <Button variant="ghost" onClick={() => remove(0)}>Delete all</Button>}
          </div>
        ) : undefined}
      />
      <div className="-mt-2 border-b">
        <Tabs value={view} onValueChange={(v) => go("recordings", v === "settings" ? "settings" : undefined)}>
          <TabsList variant="line">
            <TabsTrigger value="list">Recordings</TabsTrigger>
            <TabsTrigger value="settings">Settings</TabsTrigger>
          </TabsList>
        </Tabs>
      </div>
      <ErrorText>{settingsError || error}</ErrorText>
      {!settings && !settingsError && <Loading />}
      {settings && view === "settings" && <ReplaySettingsForm settings={settings} save={save} canManage={canManage} go={go} />}
      {settings && view === "list" && !on && (rows?.length ?? 0) === 0 && (
        <Empty
          title={settings.storage ? "Recordings are off for this brand" : "Recordings need file storage on this server"}
          action={settings.storage ? (canManage ? <Button onClick={() => save({ recordings: true })}>Turn on recordings</Button> : <p className="text-[13px] text-muted-foreground">Ask a workspace admin to turn them on.</p>) : undefined}
        >
          {settings.storage
            ? "Once on, the Outflare snippet on your website records visits from then on. Everything typed into a field is hidden before it leaves the visitor's browser, and where consent is needed only visitors who allow analytics are recorded."
            : "Recordings are kept as files, and this server has no object storage set up. Whoever runs it adds an S3-compatible bucket (OUTFLARE_S3_ENDPOINT, OUTFLARE_S3_BUCKET and its keys). Heatmaps work without it."}
        </Empty>
      )}
      {settings && view === "list" && (on || (rows?.length ?? 0) > 0) && (
        <>
          {!rows && !error && <Loading />}
          {rows && (
            <DataTable
              data={rows}
              rowId={(r: Recording) => r.id}
              columnsMenu={false}
              onRowClick={(r: Recording) => go("recordings", r.id)}
              empty={on ? "No recordings match yet. New visits show up here a few seconds after they start." : "No recordings match."}
              toolbar={
                <div className="flex flex-wrap items-center gap-2">
                  <input aria-label="Page" className={`${field} w-44`} placeholder="Page, such as /pricing" value={path} onChange={(e) => setPath(e.target.value.trim())} />
                  {(sites.length > 1 || host !== "") && (
                    <select aria-label="Website" className={field} value={host} onChange={(e) => setHost(e.target.value)}>
                      <option value="">Every website</option>
                      {sites.map((s) => <option key={s} value={s}>{s}</option>)}
                      {host !== "" && !sites.includes(host) && <option value={host}>{host}</option>}
                    </select>
                  )}
                  <select aria-label="Visitor" className={field} value={who} onChange={(e) => setWho(e.target.value)}>
                    <option value="">Everyone</option>
                    <option value="known">Known people</option>
                    <option value="anonymous">Anonymous visitors</option>
                  </select>
                  <select aria-label="Device" className={field} value={device} onChange={(e) => setDevice(e.target.value)}>
                    <option value="">Every device</option>
                    <option value="desktop">Desktop</option>
                    <option value="tablet">Tablet</option>
                    <option value="mobile">Phone</option>
                  </select>
                  <select aria-label="Length" className={field} value={minSeconds} onChange={(e) => setMinSeconds(Number(e.target.value))}>
                    <option value={0}>Any length</option>
                    <option value={10}>Over 10 seconds</option>
                    <option value={60}>Over 1 minute</option>
                    <option value={300}>Over 5 minutes</option>
                  </select>
                  <select aria-label="Period" className={field} value={days} onChange={(e) => setDays(Number(e.target.value))}>
                    <option value={1}>Last 24 hours</option>
                    <option value={7}>Last 7 days</option>
                    <option value={30}>Last 30 days</option>
                    <option value={90}>Last 90 days</option>
                  </select>
                  {canManage && picked.size > 0 && <Button variant="danger" onClick={() => remove()}>Delete {picked.size} selected</Button>}
                  {canManage && picked.size > 0 && settings && <Button variant="ghost" title="Stop recording and counting the pages these visits were on" onClick={async () => { if (await excludePages(settings, save, rows.filter((r) => picked.has(r.id)).flatMap((r) => r.paths), "The recordings you ticked stay until you delete them.")) setPicked(new Set()); }}>Exclude their pages from tracking</Button>}
                  {canManage && picked.size === 0 && rows.length > 0 && (
                    <select aria-label="Delete old recordings" className={field} value="" onChange={(e) => e.target.value !== "" && remove(Number(e.target.value))}>
                      <option value="">Delete old recordings…</option>
                      <option value={7}>Older than 7 days</option>
                      <option value={30}>Older than 30 days</option>
                      <option value={60}>Older than 60 days</option>
                      <option value={0}>Every recording</option>
                    </select>
                  )}
                </div>
              }
              columns={[
                ...(canManage ? [{ id: "pick", enableSorting: false, header: () => (
                    <input type="checkbox" aria-label="Select every recording shown" className="size-3.5 accent-[var(--color-brand-strong)]" checked={rows.length > 0 && rows.every((r) => picked.has(r.id))} onChange={(e) => setPicked(e.target.checked ? new Set(rows.map((r) => r.id)) : new Set())} />
                  ), cell: ({ row: { original: r } }: { row: { original: Recording } }) => (
                    <input type="checkbox" aria-label="Select this recording" className="size-3.5 accent-[var(--color-brand-strong)]" checked={picked.has(r.id)} onChange={(e) => toggle(r.id, e.target.checked)} />
                  ) }] : []),
                { id: "status", header: "", accessorFn: (r: Recording) => (r.viewed_at ? 1 : 0), cell: ({ row: { original: r } }: { row: { original: Recording } }) => (
                    <span className={`inline-block size-2 rounded-full ${r.viewed_at ? "bg-muted-foreground/30" : "bg-brand"}`} title={r.viewed_at ? `Watched ${ago(r.viewed_at)}` : "Not watched yet"} />
                  ) },
                { id: "visitor", header: "Visitor", accessorFn: (r: Recording) => r.anonymous_id, cell: ({ row: { original: r } }: { row: { original: Recording } }) => (
                    r.record_id
                      ? <button className="block max-w-[16rem] truncate font-medium text-brand-strong hover:underline dark:text-brand" title="Open this person" onClick={() => go("people", r.record_id!)}>{r.record_name || "Unnamed person"}</button>
                      : <span className="font-medium" title="Outflare doesn't know who this is yet. They get a name once they sign up, sign in or click a link in one of your emails.">Anonymous visitor <span className="font-mono text-[12px] text-muted-foreground">{r.anonymous_id.slice(0, 6)}</span></span>
                  ) },
                ...(sites.length > 1 ? [{ id: "site", header: "Website", accessorFn: (r: Recording) => r.host, cell: ({ row: { original: r } }: { row: { original: Recording } }) => <span className="text-muted-foreground">{r.host || "–"}</span> }] : []),
                { id: "started", header: "Started", accessorFn: (r: Recording) => r.started_at, cell: ({ row: { original: r } }: { row: { original: Recording } }) => <span title={stamp(r.started_at)}>{ago(r.started_at)}</span> },
                { id: "length", header: "Length", accessorFn: (r: Recording) => r.seconds, meta: { align: "right" as const, mono: true }, cell: ({ row: { original: r } }: { row: { original: Recording } }) => clock(r.seconds) },
                { id: "pages", header: "Pages", accessorFn: (r: Recording) => r.paths.join(" "), cell: ({ row: { original: r } }: { row: { original: Recording } }) => (
                    <span className="block max-w-[22rem] truncate" title={r.paths.join("\n")}><span className="font-mono text-[12px]">{r.paths[0] ?? "/"}</span>{r.paths.length > 1 && <span className="text-muted-foreground"> +{r.paths.length - 1} more</span>}</span>
                  ) },
                { id: "clicks", header: "Clicks", accessorFn: (r: Recording) => r.clicks, meta: { align: "right" as const, mono: true } },
                { id: "device", header: "Device", accessorFn: (r: Recording) => r.device, cell: ({ row: { original: r } }: { row: { original: Recording } }) => <span>{deviceName[r.device]}<span className="text-muted-foreground">{r.browser ? ` · ${title(r.browser)}` : ""}</span></span> },
                { id: "country", header: "Country", accessorFn: (r: Recording) => r.country, cell: ({ row: { original: r } }: { row: { original: Recording } }) => (r.country ? regionNames?.of(r.country) ?? r.country : <span className="text-muted-foreground">Unknown</span>) },
              ]}
            />
          )}
        </>
      )}
    </div>
  );
}

// The brand's recording and heatmap settings, each saved as it changes.
export function ReplaySettingsForm({ settings, save, canManage, go }: { settings: ReplaySettings; save: (c: Partial<Omit<ReplaySettings, "storage">>) => Promise<boolean>; canManage: boolean; go: (t: Tab, item?: string) => void }) {
  const row = "flex flex-wrap items-center justify-between gap-3 py-3";
  const toggle = (label: string, hint: string, on: boolean, change: (v: boolean) => void, disabled = false) => (
    <label className={row}>
      <span className="min-w-0 max-w-xl text-[13px]"><span className="block font-medium">{label}</span><span className="text-muted-foreground">{hint}</span></span>
      <input type="checkbox" className="size-4 accent-[var(--color-brand-strong)]" checked={on} disabled={!canManage || disabled} onChange={(e) => change(e.target.checked)} />
    </label>
  );
  return (
    <Card title="What your website records" description="These apply to every site that carries this brand's Outflare snippet. Sites pick a change up within a minute.">
      <div className="divide-y">
        {toggle("Record visits", settings.storage ? "Keep a recording of visits to watch back. Visitors who must give consent are recorded only after they allow analytics." : "This server has no file storage, which recordings need. Whoever runs it adds an S3-compatible bucket.", settings.recordings && settings.storage, (v) => save({ recordings: v }), !settings.storage)}
        {toggle("Heatmaps", "Count where each page is clicked and how far down it is scrolled. Counts only: no visitor is named.", settings.heatmaps, (v) => save({ heatmaps: v }))}
        <label className={row}>
          <span className="min-w-0 max-w-xl text-[13px]"><span className="block font-medium">Share of visits recorded</span><span className="text-muted-foreground">Record every visit, or a random share of them on a busy site.</span></span>
          <select aria-label="Share of visits recorded" className={field} value={settings.sample_percent} disabled={!canManage} onChange={(e) => save({ sample_percent: Number(e.target.value) })}>
            {[100, 50, 25, 10, 5, 1].map((p) => <option key={p} value={p}>{p === 100 ? "Every visit" : `${p}% of visits`}</option>)}
            {![100, 50, 25, 10, 5, 1].includes(settings.sample_percent) && <option value={settings.sample_percent}>{settings.sample_percent}% of visits</option>}
          </select>
        </label>
        {toggle("Hide all text", "What visitors type is always hidden. Turn this on to hide every piece of text on the page too, for sites that show personal details. To hide one part, add data-of-mask to it in your page; data-of-block leaves it out altogether.", settings.mask_text, (v) => save({ mask_text: v }))}
        <label className={row}>
          <span className="min-w-0 max-w-xl text-[13px]"><span className="block font-medium">Keep recordings for</span><span className="text-muted-foreground">Older recordings are deleted for good.</span></span>
          <select aria-label="Keep recordings for" className={field} value={settings.keep_days} disabled={!canManage} onChange={(e) => save({ keep_days: Number(e.target.value) })}>
            {[7, 14, 30, 60, 90].map((d) => <option key={d} value={d}>{d} days</option>)}
            {![7, 14, 30, 60, 90].includes(settings.keep_days) && <option value={settings.keep_days}>{settings.keep_days} days</option>}
          </select>
        </label>
        <label className={row}>
          <span className="min-w-0 max-w-xl text-[13px]"><span className="block font-medium">Shortest visit kept</span><span className="text-muted-foreground">Recordings of visits shorter than this are deleted soon after they end, so the list holds visits worth watching.</span></span>
          <select aria-label="Shortest visit kept" className={field} value={settings.min_seconds} disabled={!canManage} onChange={(e) => save({ min_seconds: Number(e.target.value) })}>
            {[0, 5, 10, 30, 60].map((n) => <option key={n} value={n}>{n === 0 ? "Keep every visit" : `${n} seconds`}</option>)}
            {![0, 5, 10, 30, 60].includes(settings.min_seconds) && <option value={settings.min_seconds}>{settings.min_seconds} seconds</option>}
          </select>
        </label>
        <label className={row}>
          <span className="min-w-0 max-w-xl text-[13px]"><span className="block font-medium">Most recordings a day</span><span className="text-muted-foreground">Once this many visits are recorded in a day, the rest of that day's visits aren't. Heatmaps keep counting.</span></span>
          <select aria-label="Most recordings a day" className={field} value={settings.daily_limit} disabled={!canManage} onChange={(e) => save({ daily_limit: Number(e.target.value) })}>
            {[0, 100, 500, 1000, 5000, 10000].map((n) => <option key={n} value={n}>{n === 0 ? "No limit" : n.toLocaleString()}</option>)}
            {![0, 100, 500, 1000, 5000, 10000].includes(settings.daily_limit) && <option value={settings.daily_limit}>{settings.daily_limit.toLocaleString()}</option>}
          </select>
        </label>
        <KeyShapes value={settings.hide_shapes} disabled={!canManage} save={(v) => save({ hide_shapes: v })} />
        <PathList label="Only these pages" hint="Record and count only the pages you list, one per line. Leave empty for every page. Use * for anything: /blog/* is every page under /blog." value={settings.include_paths} disabled={!canManage} save={(v) => save({ include_paths: v })} />
        <PathList label="Never these pages" hint="Pages left out of recordings and heatmaps, such as /account/* or /checkout. A visit that passes through one is still recorded on its other pages." value={settings.exclude_paths} disabled={!canManage} save={(v) => save({ exclude_paths: v })} />
      </div>
      <p className="mt-3 text-[13px] text-muted-foreground">
        Recordings and heatmaps come from the Outflare snippet. <button className="underline underline-offset-2 hover:text-foreground" onClick={() => go("tracking")}>Check it is on your website</button>.
      </p>
    </Card>
  );
}

// KeyShapes is the brand's own key shapes. A person types a made-up key
// that looks like theirs; only its shape leaves this box.
function KeyShapes({ value, disabled, save }: { value: string[]; disabled: boolean; save: (v: string[]) => Promise<boolean> }) {
  const [text, setText] = useState("");
  const example = text.trim();
  const shape = example.replace(/[A-Za-z0-9]/g, "x");
  const bad = !example ? "" : !/^[x\-_.:+/=~]+$/.test(shape) ? "Use only letters, digits and - _ . : + / = ~, with no spaces." : (shape.match(/x/g) ?? []).length < 8 ? "Too short to tell from ordinary words: at least 8 letters or digits." : shape.length > 100 ? "At most 100 characters." : "";
  const add = async () => {
    if (!example || bad) return;
    if (value.includes(shape) || (await save([...value, shape]))) setText("");
  };
  return (
    <div className="flex flex-wrap items-start justify-between gap-3 py-3">
      <span className="min-w-0 max-w-xl text-[13px]">
        <span className="block font-medium">Hide your own keys</span>
        <span className="text-muted-foreground">Common key formats, passwords and long mixed codes are always hidden. If your product shows its users keys of its own, type a made-up key that looks like one (never a real one) and add it. Outflare keeps only the shape and hides everything that matches it, in recordings and heatmap pictures.</span>
      </span>
      <div className="w-full max-w-xs">
        <div className="flex gap-2">
          <input aria-label="A made-up key that looks like yours" className={`${field} min-w-0 flex-1 font-mono text-[12px]`} placeholder="ab12cd34-ef56-7890" value={text} disabled={disabled} autoComplete="off" spellCheck={false} onChange={(e) => setText(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); void add(); } }} />
          <Button variant="ghost" disabled={disabled || !example || !!bad} onClick={() => void add()}>Add</Button>
        </div>
        {bad && <p className="mt-1 text-[12px] text-destructive">{bad}</p>}
        {example && !bad && <p className="mt-1 break-all font-mono text-[12px] text-muted-foreground">Kept as {shape}</p>}
        {value.length === 0 ? <p className="mt-2 text-[12px] text-muted-foreground">No shapes of your own yet.</p> : (
          <ul className="mt-2 space-y-1">
            {value.map((s) => (
              <li key={s} className="flex items-center justify-between gap-2 rounded-md border px-2 py-1">
                <span className="min-w-0 break-all font-mono text-[12px]">{s}</span>
                <button type="button" aria-label={`Stop hiding ${s}`} className="shrink-0 text-[12px] text-muted-foreground hover:text-foreground disabled:opacity-50" disabled={disabled} onClick={() => void save(value.filter((x) => x !== s))}>Remove</button>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

// PathList is a list of page paths, one per line, saved when the box is left.
function PathList({ label, hint, value, disabled, save }: { label: string; hint: string; value: string[]; disabled: boolean; save: (v: string[]) => Promise<boolean> }) {
  const saved = value.join("\n");
  const [text, setText] = useState(saved);
  useEffect(() => setText(saved), [saved]);
  const lines = text.split("\n").map((l) => l.trim()).filter(Boolean);
  const bad = lines.find((l) => !l.startsWith("/") || /[?#\s]/.test(l));
  const done = async () => {
    if (bad || lines.join("\n") === saved) return;
    if (!(await save(lines))) setText(saved);
  };
  return (
    <div className="flex flex-wrap items-start justify-between gap-3 py-3">
      <span className="min-w-0 max-w-xl text-[13px]"><span className="block font-medium">{label}</span><span className="text-muted-foreground">{hint}</span></span>
      <div className="w-full max-w-xs">
        <textarea aria-label={label} className={`${field} h-auto min-h-[72px] w-full py-1.5 font-mono text-[12px]`} rows={3} placeholder={"/pricing\n/blog/*"} value={text} disabled={disabled} onChange={(e) => setText(e.target.value)} onBlur={done} />
        {bad && <p className="mt-1 text-[12px] text-destructive">A page starts with / and has no ? or spaces: {bad}</p>}
      </div>
    </div>
  );
}

// loadParts reads a recording's parts in order and returns every event.
export async function loadParts(id: string, parts: number, whole = false): Promise<RecordedEvent[]> {
  const out: RecordedEvent[] = [];
  for (let p = 0; p < parts; p += 6) {
    const got = await Promise.all(Array.from({ length: Math.min(6, parts - p) }, (_, i) =>
      api.GET("/v1/brands/{brand}/recordings/{recording}/parts/{part}", { params: { path: { brand: pickedBrandId(), recording: id, part: p + i } } })));
    for (const g of got) {
      if (g.error || !g.data) throw new Error(errorMessage(g.error));
      out.push(...(g.data.events as RecordedEvent[]));
    }
  }
  const events = out.filter((e) => e && typeof e.timestamp === "number").sort((a, b) => a.timestamp - b.timestamp);
  if (whole) return events;
  // Playing starts at the first whole page (and the note of its size just before it).
  const full = events.findIndex((e) => e.type === 2);
  return full < 0 ? [] : events.slice(full > 0 && events[full - 1].type === 4 ? full - 1 : full);
}

function Watch({ id, canManage, go }: { id: string; canManage: boolean; go: (t: Tab, item?: string) => void }) {
  const [rec, setRec] = useState<Recording | null>(null);
  const [events, setEvents] = useState<RecordedEvent[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    api.GET("/v1/brands/{brand}/recordings/{recording}", { params: { path: { brand: pickedBrandId(), recording: id } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setRec(data);
      loadParts(id, data.parts).then(setEvents, (e) => setError(String(e.message ?? e)));
    });
  }, [id]);
  async function remove() {
    if (!(await confirmAction("Delete this recording?", "It is gone for good, for everyone in the workspace.", "Delete recording"))) return;
    const { error } = await api.DELETE("/v1/brands/{brand}/recordings/{recording}", { params: { path: { brand: pickedBrandId(), recording: id } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Recording deleted.");
    go("recordings");
  }
  const back = { label: "Recordings", onClick: () => go("recordings") };
  if (error) return <div className="space-y-5"><PageHeader title="Recording" back={back} /><ErrorText>{error}</ErrorText></div>;
  if (!rec) return <div className="space-y-5"><PageHeader title="Recording" back={back} /><Loading /></div>;
  return (
    <div className="space-y-5">
      <PageHeader
        back={back}
        title={<>Visit to <span className="font-mono text-lg">{rec.paths[0] ?? "/"}</span></>}
        description={`${stamp(rec.started_at)} · ${deviceName[rec.device]}${rec.browser ? `, ${title(rec.browser)}` : ""}${rec.country ? ` · ${regionNames?.of(rec.country) ?? rec.country}` : ""}`}
        action={
          <>
            {rec.record_id && <Button variant="ghost" onClick={() => go("people", rec.record_id!)}>Open {rec.record_name || "this person"}</Button>}
            {canManage && <Button variant="ghost" onClick={remove}>Delete recording</Button>}
          </>
        }
      />
      <Stats cols={4}>
        <Stat label="Length" value={clock(rec.seconds)} />
        <Stat label="Pages" value={rec.paths.length} />
        <Stat label="Clicks" value={rec.clicks} />
        <Stat label="Screen" value={rec.width ? `${rec.width} × ${rec.height}` : "Unknown"} />
      </Stats>
      {!events && <Loading />}
      {events && events.length < 2 && <Empty title="This visit was too short to play">The visitor left before the page finished recording.</Empty>}
      {events && events.length >= 2 && <Player events={events} />}
    </div>
  );
}

const speeds = [1, 2, 4, 8];

// What happened in a visit, read from its recorded events, to mark on the
// player's progress bar: clicks, typing, new pages and pauses.
type Mark = { at: number; kind: "click" | "type" | "page"; x?: number; y?: number; path?: string; until?: number };
type Step = { at: number; path: string; seconds: number; clicks: number; typed: number };
const idleGap = 5000;

function pagePath(href?: string) {
  try {
    const u = new URL(href ?? "");
    return u.pathname + u.hash;
  } catch {
    return href || "/";
  }
}

export function readVisit(events: RecordedEvent[]) {
  const start = events[0].timestamp;
  const total = Math.max(1, events[events.length - 1].timestamp - start);
  const marks: Mark[] = [];
  const pauses: [number, number][] = [];
  let lastPath = "";
  let typing: Mark | null = null;
  for (let i = 0; i < events.length; i++) {
    const e = events[i];
    const at = e.timestamp - start;
    if (i > 0 && e.timestamp - events[i - 1].timestamp >= idleGap) pauses.push([events[i - 1].timestamp - start, at]);
    const d = (e.data ?? {}) as { source?: number; type?: number; x?: number; y?: number; href?: string };
    if (e.type === 4) {
      const path = pagePath(d.href);
      if (path !== lastPath) marks.push({ at, kind: "page", path });
      lastPath = path;
    } else if (e.type === 3 && d.source === 2 && (d.type === 2 || d.type === 4)) {
      marks.push({ at, kind: "click", x: d.x, y: d.y });
    } else if (e.type === 3 && d.source === 5) {
      // Keystrokes close together are one stretch of typing.
      if (typing && at - (typing.until ?? typing.at) < 2000) typing.until = at;
      else marks.push((typing = { at, kind: "type", until: at }));
    }
  }
  // The visit page by page: when each was opened and what was done on it.
  const steps: Step[] = [];
  for (const m of marks) {
    if (m.kind === "page") {
      const last = steps[steps.length - 1];
      if (last) last.seconds = Math.round((m.at - last.at) / 1000);
      steps.push({ at: m.at, path: m.path ?? "/", seconds: 0, clicks: 0, typed: 0 });
    } else if (steps.length) {
      if (m.kind === "click") steps[steps.length - 1].clicks++;
      else steps[steps.length - 1].typed++;
    }
  }
  if (steps.length) steps[steps.length - 1].seconds = Math.round((total - steps[steps.length - 1].at) / 1000);
  return { total, marks, pauses, steps };
}

// Player plays a recording, scaled to fit, with its own plain controls.
// It starts by itself, and its progress bar shows what happened when.
function Player({ events }: { events: RecordedEvent[] }) {
  const stage = useRef<HTMLDivElement>(null);
  const ripples = useRef<HTMLDivElement>(null);
  const player = useRef<Replayer | null>(null);
  const [playing, setPlaying] = useState(false);
  const [now, setNow] = useState(0);
  const [speed, setSpeed] = useState(1);
  const [skip, setSkip] = useState(true);
  const [size, setSize] = useState({ width: 1280, height: 720 });
  const [box, setBox] = useState(0);
  const { total, marks, pauses, steps } = useMemo(() => readVisit(events), [events]);

  useEffect(() => {
    const root = stage.current;
    if (!root) return;
    // The recorded events are the library's own format.
    const r = new Replayer(events as never, { root, speed: 1, skipInactive: true, mouseTail: { strokeStyle: "#16a34a" } });
    player.current = r;
    r.on("resize", (d) => setSize(d as { width: number; height: number }));
    r.on("finish", () => setPlaying(false));
    // Opening a recording plays it.
    r.play(0);
    setPlaying(true);
    let before = 0;
    const tick = setInterval(() => {
      const t = Math.min(total, Math.max(0, r.getCurrentTime()));
      // Each click the playback just passed is shown as a ring where it landed.
      if (t > before && t - before < 3000 && ripples.current) {
        for (const m of marks) {
          if (m.kind !== "click" || m.at <= before || m.at > t || m.x == null || m.y == null) continue;
          const ring = document.createElement("span");
          ring.className = "of-click-ring";
          ring.style.left = `${m.x}px`;
          ring.style.top = `${m.y}px`;
          ripples.current.appendChild(ring);
          setTimeout(() => ring.remove(), 900);
        }
      }
      before = t;
      setNow(t);
    }, 100);
    const fit = () => setBox(root.parentElement?.parentElement?.clientWidth ?? 0);
    fit();
    window.addEventListener("resize", fit);
    return () => {
      clearInterval(tick);
      window.removeEventListener("resize", fit);
      r.destroy();
      player.current = null;
    };
  }, [events, total, marks]);
  useEffect(() => {
    player.current?.setConfig({ speed, skipInactive: skip });
  }, [speed, skip]);

  const scale = box ? Math.min(1, (box - 26) / size.width, 640 / size.height) : 1;
  const toggle = () => {
    const r = player.current;
    if (!r) return;
    if (playing) r.pause();
    else r.play(now >= total - 50 ? 0 : now);
    setPlaying(!playing);
  };
  const seek = (t: number) => {
    const r = player.current;
    if (!r) return;
    setNow(t);
    if (playing) r.play(t);
    else r.pause(t);
  };
  const pct = (t: number) => `${Math.min(100, Math.max(0, (t / total) * 100))}%`;
  // What the visitor is doing at this moment, said in words on the picture.
  const doing = useMemo(() => {
    for (let i = marks.length - 1; i >= 0; i--) {
      const m = marks[i];
      if (m.at > now) continue;
      if (now - (m.until ?? m.at) > 2500) break;
      return m.kind === "click" ? "Click" : m.kind === "type" ? "Typing" : `Opened ${m.path}`;
    }
    const idle = pauses.find(([a, b]) => now >= a && now < b);
    return idle ? `No activity for ${clock((idle[1] - idle[0]) / 1000)}` : "";
  }, [marks, pauses, now]);
  const counts = { click: marks.filter((m) => m.kind === "click").length, type: marks.filter((m) => m.kind === "type").length };
  return (
    <>
      <section aria-label="Recording" className="overflow-hidden rounded-lg border bg-card shadow-xs">
        <div className="flex justify-center bg-muted/40 p-3">
          <div style={{ width: size.width * scale, height: size.height * scale }} className="relative overflow-hidden rounded-md border bg-white shadow-sm">
            <div ref={stage} style={{ width: size.width, height: size.height, transform: `scale(${scale})`, transformOrigin: "top left" }} />
            <div ref={ripples} className="pointer-events-none absolute left-0 top-0" style={{ width: size.width, height: size.height, transform: `scale(${scale})`, transformOrigin: "top left" }} />
            {doing && <span className="pointer-events-none absolute bottom-2 left-2 rounded-md bg-slate-900/85 px-2 py-1 text-[12px] font-medium text-white" data-testid="recording-doing">{doing}</span>}
          </div>
        </div>
        <div className="space-y-2 border-t px-4 py-3">
          <div className="relative h-7" data-testid="recording-timeline">
            <div className="absolute inset-x-0 top-3 h-1.5 overflow-hidden rounded-full bg-muted">
              {pauses.map(([a, b]) => <span key={a} className="absolute inset-y-0 bg-slate-300 dark:bg-slate-600" style={{ left: pct(a), width: pct(b - a) }} />)}
              <span className="absolute inset-y-0 left-0 bg-brand/70" style={{ width: pct(now) }} />
            </div>
            <input type="range" aria-label="Position" className="of-timeline absolute inset-x-0 top-0 h-7 w-full cursor-pointer" min={0} max={total} step={100} value={now} onChange={(e) => seek(Number(e.target.value))} />
            {marks.map((m, i) =>
              m.kind === "page" ? (
                <button key={i} type="button" title={`${clock(m.at / 1000)} · Opened ${m.path}`} aria-label={`Opened ${m.path} at ${clock(m.at / 1000)}`} onClick={() => seek(m.at)} className="absolute top-1 h-5 w-[3px] -translate-x-1/2 rounded-sm bg-slate-700 hover:scale-125 dark:bg-slate-200" style={{ left: pct(m.at) }} />
              ) : m.kind === "type" ? (
                <button key={i} type="button" title={`${clock(m.at / 1000)} · Typing`} aria-label={`Typing at ${clock(m.at / 1000)}`} onClick={() => seek(m.at)} className="absolute top-[9px] h-2.5 min-w-1.5 rounded-sm bg-amber-500 hover:brightness-110" style={{ left: pct(m.at), width: pct((m.until ?? m.at) - m.at) }} />
              ) : (
                <button key={i} type="button" title={`${clock(m.at / 1000)} · Click`} aria-label={`Click at ${clock(m.at / 1000)}`} onClick={() => seek(Math.max(0, m.at - 500))} className="absolute top-[9px] size-2.5 -translate-x-1/2 rounded-full border border-white bg-rose-500 hover:scale-150 dark:border-slate-900" style={{ left: pct(m.at) }} />
              ),
            )}
          </div>
          <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-[12px] text-muted-foreground">
            <span className="flex items-center gap-1.5"><span className="size-2.5 rounded-full bg-rose-500" /> Click ({counts.click})</span>
            <span className="flex items-center gap-1.5"><span className="h-2.5 w-3 rounded-sm bg-amber-500" /> Typing ({counts.type})</span>
            <span className="flex items-center gap-1.5"><span className="h-3.5 w-[3px] rounded-sm bg-slate-700 dark:bg-slate-200" /> New page</span>
            <span className="flex items-center gap-1.5"><span className="h-1.5 w-4 rounded-full bg-slate-300 dark:bg-slate-600" /> Pause</span>
            <span>What was typed is never recorded.</span>
          </div>
          <div className="flex flex-wrap items-center gap-3">
            <Button onClick={toggle} aria-label={playing ? "Pause" : "Play"}>{playing ? "Pause" : now >= total - 50 ? "Play again" : "Play"}</Button>
            <span className="font-mono text-[12px] tabular-nums text-muted-foreground">{clock(now / 1000)} / {clock(total / 1000)}</span>
            <span className="flex-1" />
            <select aria-label="Speed" className={field} value={speed} onChange={(e) => setSpeed(Number(e.target.value))}>
              {speeds.map((s) => <option key={s} value={s}>{s}× speed</option>)}
            </select>
            <label className="flex items-center gap-1.5 text-[13px]">
              <input type="checkbox" className="size-4 accent-[var(--color-brand-strong)]" checked={skip} onChange={(e) => setSkip(e.target.checked)} /> Skip pauses
            </label>
          </div>
        </div>
      </section>
      {steps.length > 0 && (
        <Card title="The visit, page by page" description="Pick a page to jump to where the visitor opened it.">
          <table className="w-full text-[13px]" data-testid="recording-steps">
            <thead>
              <tr className="text-left text-[12px] text-muted-foreground">
                <th className="py-1 pr-3 font-medium">At</th>
                <th className="py-1 pr-3 font-medium">Page</th>
                <th className="py-1 pr-3 text-right font-medium">Time on page</th>
                <th className="py-1 pr-3 text-right font-medium">Clicks</th>
                <th className="py-1 text-right font-medium">Typed in fields</th>
              </tr>
            </thead>
            <tbody>
              {steps.map((st, i) => {
                const here = now >= st.at && (i === steps.length - 1 || now < steps[i + 1].at);
                return (
                  <tr key={i} onClick={() => seek(st.at)} className={`cursor-pointer border-t hover:bg-muted/50 ${here ? "bg-muted/60 font-medium" : ""}`}>
                    <td className="py-1.5 pr-3 font-mono text-[12px] tabular-nums text-muted-foreground">{clock(st.at / 1000)}</td>
                    <td className="max-w-[28rem] truncate py-1.5 pr-3 font-mono text-[12px]" title={st.path}>{st.path}</td>
                    <td className="py-1.5 pr-3 text-right font-mono text-[12px] tabular-nums">{clock(st.seconds)}</td>
                    <td className="py-1.5 pr-3 text-right font-mono text-[12px] tabular-nums">{st.clicks}</td>
                    <td className="py-1.5 text-right font-mono text-[12px] tabular-nums">{st.typed ? `${st.typed}×` : "–"}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </Card>
      )}
    </>
  );
}
