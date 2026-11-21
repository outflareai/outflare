import { useEffect, useMemo, useRef, useState } from "react";
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
import { excludePages, loadParts, useReplaySettings } from "./Recordings";
import { stamp } from "../format";
import type { Tab } from "../App";

// Heatmaps: where visitors click on a page and how far down they scroll.
// The list is a table of pages; a row opens that page's heatmap full
// screen. A heatmap is counts only and names no visitor.

type Heatmap = Schemas["Heatmap"];
type Device = Heatmap["device"];
type HeatPage = { path: string; clicks: number; views: number; screens: number };
type Go = (t: Tab, item?: string) => void;

const brandPath = () => ({ brand: pickedBrandId() });
const number = (n: number) => n.toLocaleString();
// A page's path is one part of the address, so its slashes are escaped.
const toItem = (path: string) => encodeURIComponent(path);
const fromItem = (item: string) => {
  try {
    return decodeURIComponent(item);
  } catch {
    return item;
  }
};

function Pickers({ days, setDays, device, setDevice, every }: { days: number; setDays: (d: number) => void; device: string; setDevice: (d: string) => void; every?: boolean }) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      <select aria-label="Device" className={field} value={device} onChange={(e) => setDevice(e.target.value)}>
        {every && <option value="">Every device</option>}
        <option value="desktop">Desktop</option>
        <option value="tablet">Tablet</option>
        <option value="mobile">Phone</option>
      </select>
      <select aria-label="Period" className={field} value={days} onChange={(e) => setDays(Number(e.target.value))}>
        <option value={1}>Last 24 hours</option>
        <option value={7}>Last 7 days</option>
        <option value={30}>Last 30 days</option>
        <option value={90}>Last 90 days</option>
      </select>
    </div>
  );
}

export function Heatmaps(props: { ws: string; canManage: boolean; go: Go; item?: string }) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="Heatmaps" brands={active.length} />;
  if (props.item) return <PageHeat key={`${brand.id}:${props.item}`} path={fromItem(props.item)} go={props.go} canManage={props.canManage} />;
  return <HeatPages key={brand.id} canManage={props.canManage} go={props.go} />;
}

function HeatPages({ canManage, go }: { canManage: boolean; go: Go }) {
  const { settings, error: settingsError, save } = useReplaySettings();
  const [rows, setRows] = useState<HeatPage[] | null>(null);
  const [error, setError] = useState("");
  const [days, setDays] = useState(30);
  const [device, setDevice] = useState("");
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const toggle = (path: string, on: boolean) => setPicked((old) => {
    const next = new Set(old);
    if (on) next.add(path); else next.delete(path);
    return next;
  });
  // Leaves the ticked pages out of recordings and heatmaps from now on,
  // and deletes what was counted on them.
  const exclude = async () => {
    if (!settings) return;
    const pages = [...picked];
    if (!(await excludePages(settings, save, pages, "What was counted on them so far is deleted."))) return;
    for (const path of pages) await api.POST("/v1/brands/{brand}/heatmaps/clear", { params: { path: brandPath() }, body: { path } });
    setRows((old) => (old ?? []).filter((r) => !picked.has(r.path)));
    setPicked(new Set());
  };

  useEffect(() => {
    api.GET("/v1/brands/{brand}/heatmaps/pages", { params: { path: brandPath(), query: { days, ...(device ? { device: device as Device } : {}) } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setError("");
      setRows(data.data);
    });
  }, [days, device]);

  // Clears what has been counted for every page, to start again.
  const clearAll = async () => {
    if (!(await confirmAction("Clear every heatmap?", "The clicks and scrolling counted so far on every page are deleted for good. Counting carries on from now.", "Clear every heatmap"))) return;
    const { error } = await api.POST("/v1/brands/{brand}/heatmaps/clear", { params: { path: brandPath() }, body: {} });
    if (error) return toast(errorMessage(error), "error");
    toast("Every heatmap cleared");
    setRows([]);
  };
  const on = !!settings?.heatmaps;
  const some = (rows?.length ?? 0) > 0 || device !== "" || days !== 30;
  return (
    <div className="space-y-5">
      <PageHeader
        title="Heatmaps"
        description="Where visitors click on each page of your website, and how far down they scroll. Counts only: a heatmap never names a visitor."
        action={settings ? (
          <div className="flex items-center gap-3">
            <label className="flex items-center gap-2 text-[13px] font-medium">
              <Switch aria-label="Heatmaps on or off" checked={on} disabled={!canManage} onCheckedChange={(v) => save({ heatmaps: v })} />
              {on ? "On" : "Off"}
            </label>
            {canManage && (rows?.length ?? 0) > 0 && <Button variant="ghost" onClick={clearAll}>Clear all</Button>}
            {canManage && <Button variant="ghost" onClick={() => go("recordings", "settings")}>Settings</Button>}
          </div>
        ) : undefined}
      />
      <ErrorText>{settingsError || error}</ErrorText>
      {(!settings || !rows) && !settingsError && !error && <Loading />}
      {settings && rows && !on && !some && (
        <Empty
          title="Heatmaps are off for this brand"
          action={canManage ? <Button onClick={() => save({ heatmaps: true })}>Turn on heatmaps</Button> : <p className="text-[13px] text-muted-foreground">Ask a workspace admin to turn them on.</p>}
        >
          Once on, the Outflare snippet on your website counts where visitors click and how far they scroll, from then on. Where consent is needed, only visitors who allow analytics are counted.
        </Empty>
      )}
      {settings && rows && (on || some) && (
        <DataTable
          data={rows}
          rowId={(r: HeatPage) => r.path}
          columnsMenu={false}
          onRowClick={(r: HeatPage) => go("heatmaps", toItem(r.path))}
          empty={on ? "No clicks or scrolls counted yet. Pages show up here within a minute of their first visit." : "Nothing was counted in this period."}
          toolbar={<div className="flex flex-wrap items-center gap-2"><Pickers days={days} setDays={setDays} device={device} setDevice={setDevice} every />{canManage && picked.size > 0 && <Button variant="danger" onClick={exclude}>Exclude {picked.size} from tracking</Button>}</div>}
          columns={[
            ...(canManage ? [{ id: "pick", enableSorting: false, header: () => (
                <input type="checkbox" aria-label="Select every page shown" className="size-3.5 accent-[var(--color-brand-strong)]" checked={rows.length > 0 && rows.every((r) => picked.has(r.path))} onChange={(e) => setPicked(e.target.checked ? new Set(rows.map((r) => r.path)) : new Set())} />
              ), cell: ({ row: { original: r } }: { row: { original: HeatPage } }) => (
                <input type="checkbox" aria-label={`Select ${r.path}`} className="size-3.5 accent-[var(--color-brand-strong)]" checked={picked.has(r.path)} onClick={(e) => e.stopPropagation()} onChange={(e) => toggle(r.path, e.target.checked)} />
              ) }] : []),
            { id: "path", header: "Page", accessorFn: (r: HeatPage) => r.path, cell: ({ row: { original: r } }: { row: { original: HeatPage } }) => <span className="flex max-w-[32rem] items-center gap-2"><span className="truncate font-mono text-[12px] font-medium" title={r.path}>{r.path}</span>{r.screens > 1 && <span className="shrink-0 rounded-full border px-1.5 text-[11px] text-muted-foreground" title="This page changes its content without changing its address, so each step has its own heatmap.">{r.screens} steps</span>}</span> },
            { id: "views", header: "Page views", accessorFn: (r: HeatPage) => r.views, meta: { align: "right" as const, mono: true }, cell: ({ row: { original: r } }: { row: { original: HeatPage } }) => number(r.views) },
            { id: "clicks", header: "Clicks", accessorFn: (r: HeatPage) => r.clicks, meta: { align: "right" as const, mono: true }, cell: ({ row: { original: r } }: { row: { original: HeatPage } }) => number(r.clicks) },
            { id: "rate", header: "Clicks per view", accessorFn: (r: HeatPage) => (r.views ? r.clicks / r.views : 0), meta: { align: "right" as const, mono: true }, cell: ({ row: { original: r } }: { row: { original: HeatPage } }) => (r.views ? (r.clicks / r.views).toFixed(1) : "–") },
          ]}
        />
      )}
    </div>
  );
}

const widths: Record<Device, number> = { desktop: 1280, tablet: 820, mobile: 390 };

// What is drawn over the page: only its busiest areas, every click, the
// most clicked spots by number, or nothing.
type Overlay = "busy" | "all" | "spots" | "none";
// A spot is a small area of the page and the clicks that landed in it:
// x in percent across the page, y in pixels from its top.
type Spot = { x: number; y: number; clicks: number; share: number };

// topSpots groups clicks that landed close together and returns the ten
// areas with the most, so a busy heatmap reads as a short list.
function topSpots(heat: Heatmap): Spot[] {
  const wide = widths[heat.device];
  const reach = 48;
  const left = heat.clicks.slice(0, 3000).map(([x, y, n]) => ({ x: ((x + 0.5) / 100) * wide, y: y + 5, n, used: false }));
  const groups: Spot[] = [];
  for (const c of left) {
    if (c.used || groups.length >= 40) continue;
    let n = 0, sx = 0, sy = 0;
    for (const o of left) {
      if (o.used || Math.abs(o.x - c.x) > reach || Math.abs(o.y - c.y) > reach) continue;
      o.used = true;
      n += o.n;
      sx += o.x * o.n;
      sy += o.y * o.n;
    }
    groups.push({ x: (sx / n / wide) * 100, y: Math.round(sy / n), clicks: n, share: heat.total_clicks ? n / heat.total_clicks : 0 });
  }
  return groups.sort((a, b) => b.clicks - a.clicks).slice(0, 10);
}

function PageHeat({ path, go, canManage }: { path: string; go: Go; canManage: boolean }) {
  // Clears what has been counted for this page, such as after a redesign.
  const clear = async () => {
    if (!(await confirmAction("Clear this heatmap?", "The clicks and scrolling counted so far on this page, on every device, are deleted for good. Counting carries on from now.", "Clear heatmap"))) return;
    const { error } = await api.POST("/v1/brands/{brand}/heatmaps/clear", { params: { path: brandPath() }, body: { path } });
    if (error) return toast(errorMessage(error), "error");
    toast("Heatmap cleared");
    go("heatmaps");
  };
  const [heat, setHeat] = useState<Heatmap | null>(null);
  const [error, setError] = useState("");
  const [days, setDays] = useState(30);
  const [device, setDevice] = useState<string>("desktop");
  const [view, setView] = useState<"clicks" | "scroll">("clicks");
  const [overlay, setOverlay] = useState<Overlay>("busy");
  const [labels, setLabels] = useState<string[]>([]);
  const [picked, setPicked] = useState(-1);
  // The step shown, of a page with several; undefined is its busiest.
  const [screen, setScreen] = useState<string | undefined>(undefined);

  useEffect(() => {
    setHeat(null);
    setLabels([]);
    setPicked(-1);
    api.GET("/v1/brands/{brand}/heatmaps", { params: { path: brandPath(), query: { path, days, device: device as Device, ...(screen === undefined ? {} : { screen }) } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setError("");
      setHeat(data);
    });
  }, [path, days, device, screen]);

  // The deepest tenth of the page at least half of its page views reached.
  const half = useMemo(() => {
    if (!heat || !heat.views) return 0;
    return heat.scroll.filter((s) => s.views * 2 >= heat.views).reduce((d, s) => Math.max(d, s.depth), 0) * 10;
  }, [heat]);
  const spots = useMemo(() => (heat ? topSpots(heat) : []), [heat]);
  const empty = heat && heat.total_clicks === 0 && heat.views === 0;
  return (
    <div className="space-y-5">
      <PageHeader
        back={{ label: "Heatmaps", onClick: () => go("heatmaps") }}
        title={<>Heatmap of <span className="font-mono text-lg">{path}</span></>}
        description="Pages are laid out differently on each kind of device, so a heatmap shows one at a time."
        action={<div className="flex items-center gap-2"><Pickers days={days} setDays={setDays} device={device} setDevice={setDevice} />{canManage && <Button variant="ghost" onClick={clear}>Clear</Button>}</div>}
      />
      <ErrorText>{error}</ErrorText>
      {!heat && !error && <Loading />}
      {heat && (
        <>
          {heat.screens.length > 0 && (
            <div className="flex flex-wrap items-center gap-3 rounded-lg border bg-muted/40 px-3 py-2.5 text-[13px]">
              <span className="min-w-0 flex-1 text-muted-foreground"><span className="font-medium text-foreground">This page has {heat.screens.filter((s) => s.screen !== "").length} steps.</span> It changes what it shows without changing its address, so each step has its own heatmap and picture.</span>
              <label className="flex items-center gap-2 font-medium">
                Step
                <select aria-label="Step of the page" className={`${field} max-w-[22rem]`} value={heat.screen} onChange={(e) => setScreen(e.target.value)}>
                  {heat.screens.map((s) => <option key={s.screen} value={s.screen}>{s.screen === "" ? "Earlier clicks, before steps were told apart" : s.screen} · {number(s.clicks)} clicks</option>)}
                  {!heat.screens.some((s) => s.screen === heat.screen) && <option value={heat.screen}>{heat.screen || "Earlier clicks"}</option>}
                </select>
              </label>
            </div>
          )}
          <Stats cols={4}>
            <Stat label="Page views" value={number(heat.views)} />
            <Stat label="Clicks" value={number(heat.total_clicks)} />
            <Stat label="Half of views reach" value={heat.views ? `${half}%` : "–"} sub={heat.views ? "of the way down the page" : undefined} />
            <Stat label="Page height" value={heat.height ? `${number(heat.height)} px` : "Unknown"} />
          </Stats>
          <div className="flex flex-wrap items-end justify-between gap-3 border-b">
            <Tabs value={view} onValueChange={(v) => setView(v as "clicks" | "scroll")}>
              <TabsList variant="line">
                <TabsTrigger value="clicks">Clicks</TabsTrigger>
                <TabsTrigger value="scroll">Scrolling</TabsTrigger>
              </TabsList>
            </Tabs>
            {view === "clicks" && !empty && (
              <label className="mb-1.5 flex items-center gap-2 text-[13px] text-muted-foreground">
                Show
                <select aria-label="What to show over the page" className={field} value={overlay} onChange={(e) => { setOverlay(e.target.value as Overlay); setPicked(-1); }}>
                  <option value="busy">Busiest areas</option>
                  <option value="all">Every click</option>
                  <option value="spots">Top 10 spots, numbered</option>
                  <option value="none">The page only</option>
                </select>
              </label>
            )}
          </div>
          {empty && <Empty title="Nothing counted on this page yet">No visitor on this kind of device clicked or scrolled here in this period. Try another device or a longer period.</Empty>}
          {!empty && (
            <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_19rem]">
              <Picture heat={heat} view={view} overlay={overlay} spots={spots} picked={picked} onLabels={setLabels} go={go} />
              <div className="space-y-5 self-start lg:sticky lg:top-4">
                {view === "clicks" ? (
                  <>
                    <Card title="Most clicked" description="Clicks that landed close together, counted as one spot. Pick one to see it on the page.">
                      <ol className="space-y-1 text-[13px]" data-testid="heatmap-spots">
                        {spots.map((sp, i) => (
                          <li key={i}>
                            <button type="button" onClick={() => { setOverlay("spots"); setPicked(i); }} className={`grid w-full grid-cols-[1.5rem_minmax(0,1fr)_auto] items-center gap-2 rounded-md px-1.5 py-1.5 text-left hover:bg-muted ${picked === i ? "bg-muted" : ""}`}>
                              <span className="grid size-5 place-items-center rounded-full bg-slate-900 text-[11px] font-semibold text-white dark:bg-slate-100 dark:text-slate-900">{i + 1}</span>
                              <span className="min-w-0">
                                <span className="block truncate font-medium" title={labels[i] || undefined}>{labels[i] || spotPlace(sp, heat)}</span>
                                <span className="block text-[12px] text-muted-foreground">{number(sp.clicks)} {sp.clicks === 1 ? "click" : "clicks"}</span>
                              </span>
                              <span className="font-mono text-[12px] tabular-nums">{Math.round(sp.share * 100)}%</span>
                            </button>
                          </li>
                        ))}
                        {spots.length === 0 && <li className="text-muted-foreground">No clicks counted yet.</li>}
                      </ol>
                    </Card>
                    <Card title="What to look for">
                      <ul className="list-disc space-y-1.5 pl-4 text-[13px] text-muted-foreground">
                        <li>A busy spot that is <span className="font-medium text-foreground">not a link or button</span>: visitors expect it to do something. Make it a link, or make it look less like one.</li>
                        <li>Your main button low on this list: it is being missed. Move it up the page or make it stand out.</li>
                        <li>Most clicks on the menu or search: visitors didn't find what they came for on this page.</li>
                      </ul>
                    </Card>
                  </>
                ) : (
                  <>
                    <Card title="How far down visitors get" description="The share of page views that reached each part of the page.">
                      <ol className="space-y-1.5 text-[12px]">
                        {heat.scroll.map((s) => {
                          const share = heat.views ? Math.round((s.views / heat.views) * 100) : 0;
                          return (
                            <li key={s.depth} className="grid grid-cols-[3.5rem_minmax(0,1fr)_2.5rem] items-center gap-2">
                              <span className="font-mono text-muted-foreground">{s.depth === 1 ? "Top 10%" : `${s.depth * 10}%`}</span>
                              <span className="h-2 overflow-hidden rounded-full bg-muted"><span className="block h-full rounded-full bg-brand" style={{ width: `${share}%` }} /></span>
                              <span className="text-right font-mono tabular-nums">{share}%</span>
                            </li>
                          );
                        })}
                        {heat.scroll.length === 0 && <li className="text-muted-foreground">No scrolling counted yet.</li>}
                      </ol>
                    </Card>
                    <Card title="What to look for">
                      <ul className="list-disc space-y-1.5 pl-4 text-[13px] text-muted-foreground">
                        <li>Where the share drops sharply, visitors stop reading. Put what matters most above that point.</li>
                        <li>A sign-up or pricing section that fewer than half of visitors reach is worth moving up.</li>
                      </ul>
                    </Card>
                  </>
                )}
              </div>
            </div>
          )}
        </>
      )}
    </div>
  );
}

// Where a spot is, in words, for when the page's picture can't name it.
function spotPlace(sp: Spot, heat: Heatmap) {
  const tall = Math.max(heat.height, 600);
  const down = sp.y < tall / 3 ? "Top" : sp.y < (tall * 2) / 3 ? "Middle" : "Bottom";
  const across = sp.x < 34 ? "left" : sp.x < 67 ? "centre" : "right";
  return `${down} ${across} of the page`;
}

// From cold to hot, by how many clicks landed near a spot.
function heatColour(a: number): [number, number, number] {
  const stops: [number, [number, number, number]][] = [[0, [37, 99, 235]], [0.35, [6, 182, 212]], [0.6, [132, 204, 22]], [0.8, [250, 204, 21]], [1, [220, 38, 38]]];
  for (let i = 1; i < stops.length; i++) {
    if (a <= stops[i][0]) {
      const [p0, c0] = stops[i - 1];
      const [p1, c1] = stops[i];
      const t = (a - p0) / (p1 - p0);
      return [c0[0] + (c1[0] - c0[0]) * t, c0[1] + (c1[1] - c0[1]) * t, c0[2] + (c1[2] - c0[2]) * t];
    }
  }
  return stops[stops.length - 1][1];
}

function pathOf(href?: string) {
  try {
    return new URL(href ?? "").pathname.replace(/(.)\/+$/, "$1");
  } catch {
    return "";
  }
}

// One pass of a soft blur over a grid of numbers, across then down.
function blur(grid: Float32Array, gw: number, gh: number, r: number) {
  const tmp = new Float32Array(grid.length);
  for (let y = 0; y < gh; y++) {
    let sum = 0;
    const row = y * gw;
    for (let x = -r; x < gw; x++) {
      if (x + r < gw) sum += grid[row + x + r];
      if (x - r - 1 >= 0) sum -= grid[row + x - r - 1];
      if (x >= 0) tmp[row + x] = sum / (2 * r + 1);
    }
  }
  for (let x = 0; x < gw; x++) {
    let sum = 0;
    for (let y = -r; y < gh; y++) {
      if (y + r < gh) sum += tmp[(y + r) * gw + x];
      if (y - r - 1 >= 0) sum -= tmp[(y - r - 1) * gw + x];
      if (y >= 0) grid[y * gw + x] = sum / (2 * r + 1);
    }
  }
}

const cell = 6;

// drawHeat paints how many clicks landed in each part of the page, on a
// canvas one sixth the page's size that the browser stretches smoothly.
// Colour follows the real number of clicks, so red is only ever the
// busiest part; quiet parts are left clear unless every click is asked for.
function drawHeat(c: HTMLCanvasElement, heat: Heatmap, width: number, height: number, every: boolean) {
  const gw = Math.ceil(width / cell), gh = Math.ceil(height / cell);
  c.width = gw;
  c.height = gh;
  const ctx = c.getContext("2d");
  if (!ctx || heat.clicks.length === 0) return;
  const grid = new Float32Array(gw * gh);
  for (const [x, y, n] of heat.clicks) {
    const gx = Math.min(gw - 1, Math.floor((((x + 0.5) / 100) * width) / cell));
    const gy = Math.floor((y + 5) / cell);
    if (gy >= 0 && gy < gh) grid[gy * gw + gx] += n;
  }
  for (let i = 0; i < 3; i++) blur(grid, gw, gh, 3);
  let most = 0;
  for (let i = 0; i < grid.length; i++) if (grid[i] > most) most = grid[i];
  if (most === 0) return;
  const img = ctx.createImageData(gw, gh);
  const px = img.data;
  const floor = every ? 0.03 : 0.3;
  for (let i = 0; i < grid.length; i++) {
    // The square root keeps one very busy button from hiding the rest.
    const t = Math.sqrt(grid[i] / most);
    if (t < floor) continue;
    const [r, g, b] = heatColour(t);
    const edge = Math.min(1, (t - floor) / 0.12);
    px[i * 4] = r;
    px[i * 4 + 1] = g;
    px[i * 4 + 2] = b;
    px[i * 4 + 3] = edge * (70 + t * 120);
  }
  ctx.putImageData(img, 0, 0);
}

// The thing a visitor meant to click at a point of the page's picture.
function nameOf(el: Element | null): string {
  const tidy = (t: string | null | undefined) => {
    const out = (t ?? "").replace(/\s+/g, " ").trim();
    return /^[*\s]*$/.test(out) ? "" : out.length > 40 ? `${out.slice(0, 40)}…` : out;
  };
  const named = (kind: string, text: string) => (text ? `${kind}: ${text}` : kind);
  for (let e = el, i = 0; e && i < 6; e = e.parentElement, i++) {
    const tag = e.tagName.toLowerCase();
    const role = e.getAttribute("role") ?? "";
    const text = tidy(e.getAttribute("aria-label")) || tidy(e.textContent) || tidy(e.getAttribute("title"));
    if (tag === "a" || role === "link") return named("Link", text);
    if (tag === "button" || tag === "summary" || ["button", "tab", "menuitem", "switch", "checkbox", "option"].includes(role)) return named("Button", text);
    if (tag === "input" || tag === "select" || tag === "textarea" || tag === "label") return named("Field", tidy(e.getAttribute("aria-label")) || tidy(e.getAttribute("placeholder")) || (tag === "label" ? tidy(e.textContent) : ""));
  }
  // Plain text or a picture: the visitor expected it to do something.
  const own = el && el.children.length === 0 ? tidy(el.textContent) : "";
  return own ? `“${own}”, not a link or button` : "Not a link or button";
}

// Picture draws the heatmap over the page: the page's own picture, taken
// by the website script the first time the page is clicked on; without
// one, the page as a recent recording first showed it; otherwise a plain
// sheet the page's size.
function Picture({ heat, view, overlay, spots, picked, onLabels, go }: { heat: Heatmap; view: "clicks" | "scroll"; overlay: Overlay; spots: Spot[]; picked: number; onLabels: (l: string[]) => void; go: Go }) {
  const wrap = useRef<HTMLDivElement>(null);
  const stage = useRef<HTMLDivElement>(null);
  const canvas = useRef<HTMLCanvasElement>(null);
  const pins = useRef<HTMLDivElement>(null);
  const [box, setBox] = useState(0);
  const [width, setWidth] = useState(widths[heat.device]);
  const [shown, setShown] = useState<"" | "snapshot" | "recording">("");
  const height = Math.max(heat.height, 600);

  useEffect(() => {
    const fit = () => setBox(wrap.current?.clientWidth ?? 0);
    fit();
    window.addEventListener("resize", fit);
    return () => window.removeEventListener("resize", fit);
  }, []);

  // The page behind the heatmap.
  useEffect(() => {
    const root = stage.current;
    setShown("");
    setWidth(widths[heat.device]);
    if (!root || (!heat.snapshot_at && !heat.recording_id)) return;
    let player: Replayer | null = null;
    let gone = false;
    let naming = 0;
    type Ev = { type: number; timestamp: number; data?: unknown };
    const id = heat.recording_id;
    const from: Promise<Ev[]> = heat.snapshot_at
      ? api.GET("/v1/brands/{brand}/heatmaps/snapshot", { params: { path: brandPath(), query: { path: heat.path, device: heat.device, ...(heat.screens.length > 0 ? { screen: heat.screen } : {}) } } }).then(({ data }) => ((data?.events ?? []) as unknown as Ev[]))
      : api.GET("/v1/brands/{brand}/recordings/{recording}", { params: { path: { brand: pickedBrandId(), recording: id! } } }).then(({ data }) => (data ? loadParts(id!, Math.min(data.parts, 12), true) : [])).then((events) => {
          // The visit's first whole picture of this page, with the note of its size before it.
          const at = events.findIndex((e, i) => e.type === 4 && pathOf((e.data as { href?: string } | undefined)?.href) === heat.path && events[i + 1]?.type === 2);
          return at < 0 ? [] : events.slice(at, at + 2);
        });
    from.then((first) => {
      if (gone || first.length < 2) return;
      player = new Replayer(first as never, { root, mouseTail: false, pauseAnimation: true });
      player.on("resize", (d) => setWidth((d as { width: number }).width || widths[heat.device]));
      player.pause(Math.max(0, first[1].timestamp - first[0].timestamp));
      const frame = root.querySelector("iframe");
      if (frame) {
        frame.style.height = `${height}px`;
        frame.setAttribute("height", String(height));
      }
      root.querySelector<HTMLElement>(".replayer-mouse")?.style.setProperty("display", "none");
      setShown(heat.snapshot_at ? "snapshot" : "recording");
      // Once the page has laid itself out, each top spot is named by what sits under it.
      naming = window.setTimeout(() => {
        const doc = frame?.contentDocument;
        const wide = frame?.clientWidth || widths[heat.device];
        if (!doc || gone) return;
        try {
          onLabels(spots.map((sp) => nameOf(doc.elementFromPoint((sp.x / 100) * wide, sp.y))));
        } catch {
          // The picture can't be read: the spots keep their plain places.
        }
      }, 700);
    }, () => undefined);
    return () => {
      gone = true;
      window.clearTimeout(naming);
      player?.destroy();
    };
  }, [heat.snapshot_at, heat.recording_id, heat.device, heat.path, heat.screen, heat.screens.length, height, spots, onLabels]);

  useEffect(() => {
    const c = canvas.current;
    if (!c) return;
    if (view === "clicks" && (overlay === "busy" || overlay === "all")) drawHeat(c, heat, width, height, overlay === "all");
    else c.getContext("2d")?.clearRect(0, 0, c.width, c.height);
  }, [heat, view, overlay, width, height]);

  const scale = box ? Math.min(1, box / width) : 1;
  // A spot picked from the list is brought into view.
  useEffect(() => {
    if (picked < 0) return;
    pins.current?.querySelector(`[data-spot="${picked}"]`)?.scrollIntoView({ behavior: "smooth", block: "center" });
  }, [picked, overlay]);
  const band = height / 10;
  return (
    <div className="min-w-0 space-y-2">
      {!heat.snapshot_at && !heat.recording_id && (
        <div className="rounded-lg border border-amber-300/60 bg-amber-50 px-3 py-2 text-[13px] text-amber-950 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-100" data-testid="heatmap-no-page">
          The page itself isn't shown yet, so the clicks are drawn on a blank sheet the page's size. Outflare takes a picture of the page the next time a visitor clicks on it, usually within the hour on a busy page.
        </div>
      )}
      <div ref={wrap} className="overflow-hidden rounded-lg border bg-muted/40 shadow-xs" data-testid="heatmap">
        <div className="mx-auto" style={{ width: width * scale, height: height * scale }}>
          <div className="relative origin-top-left bg-white" style={{ width, height, transform: `scale(${scale})` }}>
            <div ref={stage} className="pointer-events-none absolute inset-0 overflow-hidden" />
            <canvas ref={canvas} className="pointer-events-none absolute inset-0" style={{ width, height }} />
            {view === "scroll" && heat.scroll.map((s) => {
              const share = heat.views ? s.views / heat.views : 0;
              const [r, g, b] = heatColour(share);
              return (
                <div key={s.depth} className="pointer-events-none absolute inset-x-0" style={{ top: (s.depth - 1) * band, height: band, background: `rgba(${r | 0}, ${g | 0}, ${b | 0}, 0.3)`, borderTop: "1px dashed rgba(15, 23, 42, 0.35)" }}>
                  <span className="absolute left-3 top-2.5 rounded bg-slate-900/85 px-2 py-1 text-[13px] font-semibold text-white">{Math.round(share * 100)}% got this far</span>
                </div>
              );
            })}
            {view === "clicks" && overlay === "spots" && (
              <div ref={pins} className="pointer-events-none absolute inset-0">
                {spots.map((sp, i) => (
                  <span key={i} data-spot={i} className={`absolute grid -translate-x-1/2 -translate-y-1/2 place-items-center rounded-full border-2 border-white font-semibold text-white shadow-md ${picked === i ? "size-14 bg-rose-600 text-[22px] ring-4 ring-rose-500/40" : "size-10 bg-slate-900/90 text-[17px]"}`} style={{ left: `${sp.x}%`, top: sp.y }}>{i + 1}</span>
                ))}
              </div>
            )}
          </div>
        </div>
      </div>
      <div className="flex flex-wrap items-center justify-between gap-3 text-[12px] text-muted-foreground">
        <span>
          {shown === "snapshot" && heat.snapshot_at && <>The page behind is how one visitor's browser showed it on {stamp(heat.snapshot_at)}. What they typed is never in it.</>}
          {shown === "recording" && heat.recording_id && <>The page behind is how a recent visit first saw it. <button className="text-brand-strong hover:underline dark:text-brand" onClick={() => go("recordings", heat.recording_id!)}>Watch that visit</button></>}
          {!shown && (heat.snapshot_at || heat.recording_id) && "Loading the page behind the heatmap."}
        </span>
        {view === "clicks" && (overlay === "busy" || overlay === "all") && (
          <span className="flex items-center gap-2">Fewer clicks <span className="h-2 w-28 rounded-full" style={{ background: "linear-gradient(to right, rgb(37,99,235), rgb(6,182,212), rgb(132,204,22), rgb(250,204,21), rgb(220,38,38))" }} /> More clicks</span>
        )}
      </div>
    </div>
  );
}
