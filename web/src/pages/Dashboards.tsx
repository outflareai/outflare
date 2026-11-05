import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, confirmAction, CopyButton, Empty, ErrorText, field, Loading, PageHeader, toast } from "../ui";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { PickBrand } from "@/components/pick-brand";
import { useBrand } from "@/lib/brand";
import type { Tab } from "@/lib/paths";
import { nameItem } from "@/lib/records";
import { track } from "@/lib/analytics";
import { ago } from "../format";
import { FunnelChart, presetLabels, today } from "./Funnels";
import { cleanTrend, TrendView } from "./Trends";
import { cleanRetention, RetentionView } from "./Retention";
import { cleanPaths, PathsView } from "./Paths";
import { cleanLifecycle, LifecycleView } from "./LifecycleInsight";

// Dashboards (docs/insights.md): a grid of tiles, each a saved funnel or
// trend run with the dashboard's date range, or a note. Twelve columns;
// tiles move and resize with the arrows on each one.

type Dashboard = Schemas["Dashboard"];
type Detail = Schemas["DashboardDetail"];
type Tile = Schemas["DashboardTile"];
type Insight = Schemas["Insight"];
type DateRange = Schemas["InsightDateRange"];
type FunnelSpec = Schemas["FunnelSpec"];
type FunnelResult = Schemas["FunnelResult"];
type TrendSpec = Schemas["TrendSpec"];
type TrendResult = Schemas["TrendResult"];
type RetentionSpec = Schemas["RetentionSpec"];
type RetentionResult = Schemas["RetentionResult"];
type PathsSpec = Schemas["PathsSpec"];
type PathsResult = Schemas["PathsResult"];
type LifecycleSpec = Schemas["LifecycleSpec"];
type LifecycleResult = Schemas["LifecycleResult"];
type Annotation = Schemas["Annotation"];
type Preset = NonNullable<DateRange["preset"]>;

type Props = { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void; item?: string };

const COLUMNS = 12;
const ROW = 72; // pixels per grid row

export function Dashboards({ ws, canManage, go, item }: Props) {
  const { brand, active } = useBrand(ws);
  useEffect(() => nameItem("dashboards", "Dashboards"), []);
  return (
    <div className="space-y-6">
      {brand ? (
        item ? (
          <DashboardPage key={`${brand.id}:${item}`} brand={brand.id} id={item} canManage={canManage} back={() => go("dashboards")} />
        ) : (
          <>
            <PageHeader title="Dashboards" description="Your saved funnels and trends on one page, all over the same dates. Build one per question: the weekly numbers, a launch, a channel." />
            <DashboardList key={brand.id} brand={brand.id} canManage={canManage} open={(id) => go("dashboards", id)} />
          </>
        )
      ) : (
        <>
          <PageHeader title="Dashboards" description="Your saved funnels and trends on one page, all over the same dates." />
          <PickBrand what="Dashboards" brands={active.length} />
        </>
      )}
    </div>
  );
}

function DashboardList({ brand, canManage, open }: { brand: string; canManage: boolean; open: (id: string) => void }) {
  const [list, setList] = useState<Dashboard[] | null>(null);
  const [error, setError] = useState("");
  const [name, setName] = useState("");
  const [creating, setCreating] = useState(false);

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/dashboards", { params: { path: { brand } } });
    if (!data) return setError(errorMessage(error));
    setList(data.data);
  }, [brand]);
  useEffect(() => {
    load();
  }, [load]);

  async function create() {
    if (!name.trim()) return;
    setCreating(true);
    const { data, error } = await api.POST("/v1/brands/{brand}/dashboards", { params: { path: { brand } }, body: { name: name.trim(), settings: { date_range: { preset: "last_30_days" } } } });
    setCreating(false);
    if (!data) return toast(errorMessage(error), "error");
    setName("");
    open(data.id);
  }

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!list) return <Loading />;
  return (
    <div className="space-y-4">
      {canManage && (
        <form
          className="flex flex-wrap items-center gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            create();
          }}
        >
          <input className={`${field} w-72`} placeholder="New dashboard, for example Weekly numbers" aria-label="New dashboard name" value={name} onChange={(e) => setName(e.target.value)} />
          <Button type="submit" disabled={!name.trim() || creating}>
            Create
          </Button>
        </form>
      )}
      {list.length === 0 ? (
        <Empty title="No dashboards yet">Save a funnel or a trend first, then put them on a dashboard here.</Empty>
      ) : (
        <ul className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {list.map((d) => (
            <li key={d.id}>
              <button className="w-full rounded-md border bg-card p-4 text-left transition-colors hover:bg-muted/50" onClick={() => open(d.id)}>
                <div className="flex items-start justify-between gap-2">
                  <span className="font-medium">{d.name}</span>
                  <Badge>{d.tiles} {d.tiles === 1 ? "tile" : "tiles"}</Badge>
                </div>
                {d.description && <p className="mt-1 line-clamp-2 text-[13px] text-muted-foreground">{d.description}</p>}
                <p className="mt-2 text-[12px] text-muted-foreground">
                  {d.settings.date_range ? rangeLabel(d.settings.date_range) : "Each tile's own dates"} · updated {ago(d.updated_at)}
                </p>
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

export function rangeLabel(r: DateRange): string {
  if (r.preset) return presetLabels[r.preset as Preset] ?? r.preset;
  if (r.from && r.to) return `${r.from} to ${r.to}`;
  return "Last 30 days";
}

function DashboardPage({ brand, id, canManage, back }: { brand: string; id: string; canManage: boolean; back: () => void }) {
  const [d, setD] = useState<Detail | null>(null);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const [settings, setSettings] = useState(false);
  const [editing, setEditing] = useState(false);

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/dashboards/{dashboard}", { params: { path: { brand, dashboard: id } } });
    if (!data) return setError(errorMessage(error));
    setD(data);
  }, [brand, id]);
  useEffect(() => {
    load();
  }, [load]);
  useEffect(() => {
    if (d) nameItem(d.id, d.name);
  }, [d]);

  async function patch(body: Schemas["DashboardInput"]) {
    const { data, error } = await api.PATCH("/v1/brands/{brand}/dashboards/{dashboard}", { params: { path: { brand, dashboard: id } }, body });
    if (!data) return toast(errorMessage(error), "error");
    setD(data);
  }

  async function remove() {
    if (!d || !(await confirmAction(`Delete ${d.name}?`, "The dashboard goes for everyone. The funnels and trends on it stay saved.", "Delete"))) return;
    const { error, response } = await api.DELETE("/v1/brands/{brand}/dashboards/{dashboard}", { params: { path: { brand, dashboard: id } } });
    if (error || !response.ok) return toast(errorMessage(error), "error");
    toast("Dashboard deleted");
    back();
  }

  // Moves or resizes a tile. The grid has COLUMNS columns; a tile keeps
  // inside them. Rows have no limit.
  async function place(t: Tile, dx: number, dy: number, dw: number, dh: number) {
    const w = Math.max(1, Math.min(COLUMNS, t.w + dw));
    const x = Math.max(0, Math.min(COLUMNS - w, t.x + dx));
    const y = Math.max(0, t.y + dy);
    const h = Math.max(1, t.h + dh);
    if (w === t.w && x === t.x && y === t.y && h === t.h) return;
    track("ui.dashboard_tile_moved", { resized: dw !== 0 || dh !== 0 });
    await patch({ layout: [{ id: t.id, x, y, w, h }] });
  }

  async function removeTile(t: Tile) {
    const { error, response } = await api.DELETE("/v1/brands/{brand}/dashboards/{dashboard}/tiles/{tile}", { params: { path: { brand, dashboard: id, tile: t.id } } });
    if (error || !response.ok) return toast(errorMessage(error), "error");
    load();
  }

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!d) return <Loading />;
  const range = d.settings.date_range;
  return (
    <>
      <PageHeader
        title={d.name}
        description={d.description || (range ? `Every tile shows ${rangeLabel(range).toLowerCase()}${d.settings.compare ? ", against the period before" : ""}.` : "Each tile shows its own dates.")}
        back={{ label: "Dashboards", onClick: back }}
        action={
          <div className="flex flex-wrap items-center gap-2">
            <RangePicker value={range} onChange={(r) => patch({ settings: { ...d.settings, date_range: r } })} disabled={!canManage} />
            {canManage && (
              <>
                <Button variant="ghost" onClick={() => setSettings(true)}>
                  Settings
                </Button>
                <Button variant="ghost" onClick={() => setEditing((e) => !e)} aria-pressed={editing}>
                  {editing ? "Done arranging" : "Arrange"}
                </Button>
                <Button onClick={() => setAdding(true)}>Add tile</Button>
              </>
            )}
          </div>
        }
      />
      {d.tile_list.length === 0 ? (
        <Empty title="An empty dashboard" action={canManage ? <Button onClick={() => setAdding(true)}>Add a tile</Button> : undefined}>
          Add your saved funnels and trends, and notes between them.
        </Empty>
      ) : (
        <Grid tiles={d.tile_list} brand={brand} range={range} compare={!!d.settings.compare} editing={editing && canManage} onPlace={place} onRemove={removeTile} />
      )}
      {adding && <AddTile brand={brand} dashboard={id} onClose={() => setAdding(false)} onAdded={() => { setAdding(false); load(); }} />}
      {settings && (
        <DashboardSettings
          d={d}
          brand={brand}
          onShared={load}
          onClose={() => setSettings(false)}
          onSave={async (body) => {
            await patch(body);
            setSettings(false);
          }}
          onDelete={remove}
        />
      )}
    </>
  );
}

export function RangePicker({ value, onChange, disabled }: { value: DateRange | undefined; onChange: (r: DateRange | undefined) => void; disabled: boolean }) {
  const [custom, setCustom] = useState(!!value && !value.preset && !!value.from);
  const preset = value?.preset ?? (value?.from ? "custom" : "own");
  return (
    <div className="flex items-center gap-2">
      <select
        className={field}
        aria-label="Dates every tile shows"
        value={custom ? "custom" : preset}
        disabled={disabled}
        onChange={(e) => {
          const v = e.target.value;
          if (v === "own") {
            setCustom(false);
            onChange(undefined);
          } else if (v === "custom") {
            setCustom(true);
            onChange({ from: today(-30), to: today(0) });
          } else {
            setCustom(false);
            onChange({ preset: v as Preset });
          }
        }}
      >
        {Object.entries(presetLabels).map(([k, l]) => (
          <option key={k} value={k}>
            {l}
          </option>
        ))}
        <option value="custom">Custom dates</option>
        <option value="own">Each tile's own dates</option>
      </select>
      {custom && (
        <>
          <input type="date" className={field} aria-label="From" value={value?.from ?? ""} disabled={disabled} onChange={(e) => onChange({ from: e.target.value, to: value?.to })} />
          <input type="date" className={field} aria-label="To" value={value?.to ?? ""} disabled={disabled} onChange={(e) => onChange({ from: value?.from, to: e.target.value })} />
        </>
      )}
    </div>
  );
}

// Grid lays tiles out on COLUMNS columns, each row ROW pixels high, in
// the positions the server stores. Arranging shows arrows to move and
// resize a tile and a cross to take it off.
// A Runner answers a tile's insight in place of the brand's run endpoints:
// the shared page uses one that runs tiles through the share link.
export type Runner = (kind: Insight["kind"], tileID: string, range: DateRange | undefined) => Promise<{ data?: unknown; error?: unknown }>;

export function Grid({ tiles, brand, range, compare, editing, onPlace, onRemove, run }: { tiles: Tile[]; brand: string; range: DateRange | undefined; compare: boolean; editing: boolean; onPlace: (t: Tile, dx: number, dy: number, dw: number, dh: number) => void; onRemove: (t: Tile) => void; run?: Runner }) {
  const rows = Math.max(1, ...tiles.map((t) => t.y + t.h));
  return (
    <div className="grid gap-3" style={{ gridTemplateColumns: `repeat(${COLUMNS}, minmax(0, 1fr))`, gridAutoRows: `${ROW}px`, gridTemplateRows: `repeat(${rows}, ${ROW}px)` }}>
      {tiles.map((t) => (
        <div key={t.id} className="relative min-w-0" style={{ gridColumn: `${t.x + 1} / span ${t.w}`, gridRow: `${t.y + 1} / span ${t.h}` }}>
          <TileCard tile={t} brand={brand} range={range} compare={compare} run={run} />
          {editing && (
            <div className="absolute inset-x-0 top-0 flex items-center justify-between gap-1 rounded-t-md border-b bg-background/95 px-2 py-1 text-[12px] backdrop-blur">
              <span className="flex items-center gap-0.5">
                <span className="mr-1 text-muted-foreground">Move</span>
                <Arrow label="Move left" onClick={() => onPlace(t, -1, 0, 0, 0)}>←</Arrow>
                <Arrow label="Move right" onClick={() => onPlace(t, 1, 0, 0, 0)}>→</Arrow>
                <Arrow label="Move up" onClick={() => onPlace(t, 0, -1, 0, 0)}>↑</Arrow>
                <Arrow label="Move down" onClick={() => onPlace(t, 0, 1, 0, 0)}>↓</Arrow>
                <span className="mx-1 text-muted-foreground">Size</span>
                <Arrow label="Narrower" onClick={() => onPlace(t, 0, 0, -1, 0)}>−</Arrow>
                <Arrow label="Wider" onClick={() => onPlace(t, 0, 0, 1, 0)}>+</Arrow>
                <Arrow label="Shorter" onClick={() => onPlace(t, 0, 0, 0, -1)}>▴</Arrow>
                <Arrow label="Taller" onClick={() => onPlace(t, 0, 0, 0, 1)}>▾</Arrow>
              </span>
              <Arrow label="Take off the dashboard" onClick={() => onRemove(t)}>✕</Arrow>
            </div>
          )}
        </div>
      ))}
    </div>
  );
}

function Arrow({ label, onClick, children }: { label: string; onClick: () => void; children: string }) {
  return (
    <button type="button" className="rounded px-1.5 py-0.5 font-mono hover:bg-muted" aria-label={label} title={label} onClick={onClick}>
      {children}
    </button>
  );
}

// TileCard runs its insight with the dashboard's dates (or the insight's
// own, when the dashboard has none) and draws it; a note tile shows its
// text.
function TileCard({ tile, brand, range, compare, run }: { tile: Tile; brand: string; range: DateRange | undefined; compare: boolean; run?: Runner }) {
  const ins = tile.insight;
  if (!ins) {
    return (
      <div className="h-full overflow-y-auto rounded-md border border-dashed bg-card/50 p-4 text-[13px] leading-relaxed whitespace-pre-wrap">{tile.note || <span className="text-muted-foreground">{tile.insight_id ? "This insight was deleted." : "An empty note."}</span>}</div>
    );
  }
  return <InsightTile insight={ins} brand={brand} range={range} compare={compare} run={run && ((kind, rng) => run(kind, tile.id, rng))} />;
}

export function InsightTile({ insight, brand, range, compare, run }: { insight: Pick<Insight, "kind" | "name" | "spec">; brand: string; range: DateRange | undefined; compare: boolean; run?: (kind: Insight["kind"], range: DateRange | undefined) => Promise<{ data?: unknown; error?: unknown }> }) {
  const [funnel, setFunnel] = useState<FunnelResult | null>(null);
  const [trend, setTrend] = useState<TrendResult | null>(null);
  const [retention, setRetention] = useState<RetentionResult | null>(null);
  const [paths, setPaths] = useState<PathsResult | null>(null);
  const [lifecycle, setLifecycle] = useState<LifecycleResult | null>(null);
  const [error, setError] = useState("");
  const [running, setRunning] = useState(true);
  const run_ = useRef(0);
  const spec = useMemo(() => {
    const s = { ...(insight.spec as FunnelSpec | TrendSpec | RetentionSpec | PathsSpec | LifecycleSpec) };
    if (range) s.date_range = range;
    if (insight.kind === "trend") (s as TrendSpec).compare = compare || (s as TrendSpec).compare;
    return s;
  }, [insight, range, compare]);

  useEffect(() => {
    const n = ++run_.current;
    setRunning(true);
    (async () => {
      if (run) {
        const { data, error } = await run(insight.kind, range);
        if (n !== run_.current) return;
        if (!data) setError(errorMessage(error));
        else {
          setError("");
          if (insight.kind === "funnel") setFunnel(data as FunnelResult);
          else if (insight.kind === "retention") setRetention(data as RetentionResult);
          else if (insight.kind === "paths") setPaths(data as PathsResult);
          else if (insight.kind === "lifecycle") setLifecycle(data as LifecycleResult);
          else setTrend(data as TrendResult);
        }
      } else if (insight.kind === "funnel") {
        const { data, error } = await api.POST("/v1/brands/{brand}/insights/funnel", { params: { path: { brand } }, body: spec as FunnelSpec });
        if (n !== run_.current) return;
        if (!data) setError(errorMessage(error));
        else {
          setError("");
          setFunnel(data);
        }
      } else if (insight.kind === "retention") {
        const { data, error } = await api.POST("/v1/brands/{brand}/insights/retention", { params: { path: { brand } }, body: cleanRetention(spec as RetentionSpec) });
        if (n !== run_.current) return;
        if (!data) setError(errorMessage(error));
        else {
          setError("");
          setRetention(data);
        }
      } else if (insight.kind === "paths") {
        const { data, error } = await api.POST("/v1/brands/{brand}/insights/paths", { params: { path: { brand } }, body: cleanPaths(spec as PathsSpec) });
        if (n !== run_.current) return;
        if (!data) setError(errorMessage(error));
        else {
          setError("");
          setPaths(data);
        }
      } else if (insight.kind === "lifecycle") {
        const { data, error } = await api.POST("/v1/brands/{brand}/insights/lifecycle", { params: { path: { brand } }, body: cleanLifecycle(spec as LifecycleSpec) });
        if (n !== run_.current) return;
        if (!data) setError(errorMessage(error));
        else {
          setError("");
          setLifecycle(data);
        }
      } else {
        const { data, error } = await api.POST("/v1/brands/{brand}/insights/trend", { params: { path: { brand } }, body: cleanTrend(spec as TrendSpec) });
        if (n !== run_.current) return;
        if (!data) setError(errorMessage(error));
        else {
          setError("");
          setTrend(data);
        }
      }
      setRunning(false);
    })();
  }, [brand, insight.kind, spec, run, range]);

  // A trend tile on the brand's own dashboard carries the brand's notes
  // over its dates, as the Trends page does. Shared dashboards (run set)
  // show none: a note can say more than the numbers.
  const notes = useAnnotations(run ? "" : brand, insight.kind === "trend" ? trend?.from : undefined, insight.kind === "trend" ? trend?.to : undefined);

  const display = insight.kind === "trend" ? ((spec as TrendSpec).display ?? "line") : "funnel";
  return (
    <div className="flex h-full flex-col overflow-hidden rounded-md border bg-card">
      <div className="flex items-center justify-between gap-2 border-b px-3 py-1.5">
        <span className="truncate text-[13px] font-medium" title={insight.name}>
          {insight.name}
        </span>
        <span className="flex shrink-0 items-center gap-1 text-[11px] text-muted-foreground">
          {running && <Badge>updating</Badge>}
          {insight.kind === "funnel" ? "Funnel" : insight.kind === "retention" ? "Retention" : insight.kind === "paths" ? "Paths" : insight.kind === "lifecycle" ? "Lifecycle" : display === "number" ? "Number" : display === "table" ? "Table" : "Trend"}
        </span>
      </div>
      <div className="min-h-0 flex-1 overflow-auto p-3">
        {error ? (
          <ErrorText>{error}</ErrorText>
        ) : insight.kind === "funnel" ? (
          funnel ? (
            <FunnelChart result={funnel} running={running} />
          ) : (
            <Loading />
          )
        ) : insight.kind === "retention" ? (
          retention ? (
            <RetentionView result={retention} compact />
          ) : (
            <Loading />
          )
        ) : insight.kind === "paths" ? (
          paths ? (
            <PathsView result={paths} compact />
          ) : (
            <Loading />
          )
        ) : insight.kind === "lifecycle" ? (
          lifecycle ? (
            <LifecycleView result={lifecycle} compact />
          ) : (
            <Loading />
          )
        ) : trend ? (
          <TrendView result={trend} display={display as NonNullable<TrendSpec["display"]>} compact annotations={notes} />
        ) : (
          <Loading />
        )}
      </div>
    </div>
  );
}

// Every trend tile on a dashboard asks for the same notes, so one request
// per brand and date range serves them all, for the life of the page.
const notesCache = new Map<string, Promise<Annotation[]>>();

function useAnnotations(brand: string, from: string | undefined, to: string | undefined): Annotation[] {
  const [notes, setNotes] = useState<Annotation[]>([]);
  const f = from?.slice(0, 10);
  const t = to ? new Date(new Date(to).getTime() - 1).toISOString().slice(0, 10) : undefined;
  useEffect(() => {
    if (!brand || !f || !t) return;
    const key = `${brand}:${f}:${t}`;
    let p = notesCache.get(key);
    if (!p) {
      p = api.GET("/v1/brands/{brand}/annotations", { params: { path: { brand }, query: { from: f, to: t } } }).then(({ data }) => data?.data ?? []);
      notesCache.set(key, p);
      // Keep the cache small and fresh: a note added on Trends shows on
      // the next visit to the dashboard, not a day later.
      setTimeout(() => notesCache.delete(key), 60_000);
    }
    let live = true;
    p.then((a) => live && setNotes(a));
    return () => {
      live = false;
    };
  }, [brand, f, t]);
  return notes;
}

// AddTile puts a saved insight or a note on the dashboard, below the
// others.
function AddTile({ brand, dashboard, onClose, onAdded }: { brand: string; dashboard: string; onClose: () => void; onAdded: () => void }) {
  const [saved, setSaved] = useState<Insight[] | null>(null);
  const [mode, setMode] = useState<"insight" | "note">("insight");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [q, setQ] = useState("");

  useEffect(() => {
    api.GET("/v1/brands/{brand}/insights", { params: { path: { brand }, query: { limit: 200 } } }).then(({ data, error }) => (data ? setSaved(data.data) : toast(errorMessage(error), "error")));
  }, [brand]);

  async function add(body: Schemas["DashboardTileInput"]) {
    setBusy(true);
    const { data, error } = await api.POST("/v1/brands/{brand}/dashboards/{dashboard}/tiles", { params: { path: { brand, dashboard } }, body });
    setBusy(false);
    if (!data) return toast(errorMessage(error), "error");
    toast("Added to the dashboard");
    onAdded();
  }

  const shown = (saved ?? []).filter((i) => !q || i.name.toLowerCase().includes(q.toLowerCase()));
  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-lg">
        <SheetHeader>
          <SheetTitle>Add a tile</SheetTitle>
          <SheetDescription>A saved funnel, trend, retention, paths or lifecycle question, run with the dashboard's dates, or a note between them.</SheetDescription>
        </SheetHeader>
        <div className="space-y-4 px-4 pb-4">
          <div className="flex gap-1 rounded-md border p-1 text-[13px]" role="tablist">
            <button role="tab" aria-selected={mode === "insight"} className={`flex-1 rounded px-2 py-1 ${mode === "insight" ? "bg-muted font-medium" : ""}`} onClick={() => setMode("insight")}>
              Saved funnel or trend
            </button>
            <button role="tab" aria-selected={mode === "note"} className={`flex-1 rounded px-2 py-1 ${mode === "note" ? "bg-muted font-medium" : ""}`} onClick={() => setMode("note")}>
              Note
            </button>
          </div>
          {mode === "note" ? (
            <div className="space-y-2">
              <textarea className={`${field} w-full`} rows={5} placeholder="What this dashboard is for, or what to look at first." aria-label="Note" value={note} onChange={(e) => setNote(e.target.value)} />
              <Button disabled={!note.trim() || busy} onClick={() => add({ note: note.trim(), w: 4, h: 2 })}>
                Add note
              </Button>
            </div>
          ) : saved === null ? (
            <Loading />
          ) : saved.length === 0 ? (
            <Empty title="Nothing saved yet">Build a funnel or a trend and save it; it then shows here.</Empty>
          ) : (
            <>
              <input className={`${field} w-full`} placeholder="Find by name" aria-label="Find a saved insight" value={q} onChange={(e) => setQ(e.target.value)} />
              <ul className="divide-y rounded-md border">
                {shown.map((i) => (
                  <li key={i.id} className="flex items-center justify-between gap-3 px-3 py-2">
                    <div className="min-w-0">
                      <div className="truncate text-[13px] font-medium">{i.name}</div>
                      <div className="text-[12px] text-muted-foreground">
                        {i.kind === "funnel" ? "Funnel" : i.kind === "retention" ? "Retention" : i.kind === "paths" ? "Paths" : i.kind === "lifecycle" ? "Lifecycle" : "Trend"} · saved {ago(i.updated_at)}
                      </div>
                    </div>
                    <Button variant="ghost" disabled={busy} onClick={() => add({ insight_id: i.id })}>
                      Add
                    </Button>
                  </li>
                ))}
                {shown.length === 0 && <li className="px-3 py-2 text-[13px] text-muted-foreground">Nothing by that name.</li>}
              </ul>
            </>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}

function DashboardSettings({ d, brand, onClose, onSave, onDelete, onShared }: { d: Detail; brand: string; onClose: () => void; onSave: (body: Schemas["DashboardInput"]) => Promise<void>; onDelete: () => void; onShared: () => void }) {
  const [name, setName] = useState(d.name);
  const [description, setDescription] = useState(d.description);
  const [compare, setCompare] = useState(!!d.settings.compare);
  const [digest, setDigest] = useState(!!d.settings.digest);
  const [busy, setBusy] = useState(false);
  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-md">
        <SheetHeader>
          <SheetTitle>Dashboard settings</SheetTitle>
          <SheetDescription>Its name, what it is for, and whether trends show the period before.</SheetDescription>
        </SheetHeader>
        <form
          className="space-y-4 px-4 pb-4"
          onSubmit={async (e) => {
            e.preventDefault();
            setBusy(true);
            await onSave({ name: name.trim(), description: description.trim(), settings: { ...d.settings, compare, digest } });
            setBusy(false);
          }}
        >
          <label className="block space-y-1 text-[13px]">
            <span className="font-medium">Name</span>
            <input className={`${field} w-full`} value={name} onChange={(e) => setName(e.target.value)} required maxLength={120} />
          </label>
          <label className="block space-y-1 text-[13px]">
            <span className="font-medium">Description</span>
            <textarea className={`${field} w-full`} rows={3} value={description} onChange={(e) => setDescription(e.target.value)} maxLength={1000} />
          </label>
          <label className="flex items-center gap-2 text-[13px]">
            <input type="checkbox" checked={compare} onChange={(e) => setCompare(e.target.checked)} />
            Show trends against the period before
          </label>
          <label className="flex items-center gap-2 text-[13px]">
            <input type="checkbox" checked={digest} onChange={(e) => setDigest(e.target.checked)} />
            Put its trends in the digest
          </label>
          <div className="flex items-center justify-between gap-2 pt-2">
            <Button type="button" variant="danger" onClick={onDelete}>
              Delete dashboard
            </Button>
            <Button type="submit" disabled={!name.trim() || busy}>
              Save
            </Button>
          </div>
        </form>
        <ShareSettings d={d} brand={brand} onChanged={onShared} />
      </SheetContent>
    </Sheet>
  );
}

// ShareSettings makes, shows and takes back the dashboard's link. The
// token shows once, right after it is made; after that only whether the
// link has a password, when it expires and how often it was opened.
function ShareSettings({ d, brand, onChanged }: { d: Detail; brand: string; onChanged: () => void }) {
  const [password, setPassword] = useState("");
  const [days, setDays] = useState("0");
  const [url, setUrl] = useState("");
  const [busy, setBusy] = useState(false);
  const share = d.share;

  async function make() {
    setBusy(true);
    const { data, error } = await api.POST("/v1/brands/{brand}/dashboards/{dashboard}/share", { params: { path: { brand, dashboard: d.id } }, body: { password: password || undefined, expires_in_days: Number(days) || undefined } });
    setBusy(false);
    if (!data) return toast(errorMessage(error), "error");
    setUrl(data.url);
    setPassword("");
    toast(share ? "New link made; the old one stopped working" : "Link made");
    onChanged();
  }

  async function revoke() {
    if (!(await confirmAction("Take the link back?", "Anyone who has it sees nothing from now on.", "Take back"))) return;
    const { error, response } = await api.DELETE("/v1/brands/{brand}/dashboards/{dashboard}/share", { params: { path: { brand, dashboard: d.id } } });
    if (error || !response.ok) return toast(errorMessage(error), "error");
    setUrl("");
    toast("Link taken back");
    onChanged();
  }

  return (
    <div className="space-y-3 border-t px-4 py-4 text-[13px]">
      <div>
        <div className="font-medium">Share by link</div>
        <p className="text-muted-foreground">Anyone with the link sees this dashboard read-only, every tile live, and nothing else. Add a password so the link alone isn't enough.</p>
      </div>
      {url && (
        <div className="flex items-center gap-2 rounded-md border bg-muted/40 p-2">
          <code className="min-w-0 flex-1 truncate font-mono text-[12px]" title={url}>{url}</code>
          <CopyButton text={url} label="Copy link" />
        </div>
      )}
      {share && !url && (
        <p className="text-muted-foreground">
          A link exists{share.has_password ? " with a password" : ""}
          {share.expires_at ? `, until ${share.expires_at.slice(0, 10)}` : ""}; opened {share.views} {share.views === 1 ? "time" : "times"}
          {share.last_viewed_at ? `, last ${ago(share.last_viewed_at)}` : ""}. Making a new one replaces it.
        </p>
      )}
      <div className="grid gap-2 sm:grid-cols-2">
        <label className="block space-y-1">
          <span className="text-muted-foreground">Password (optional)</span>
          <input className={`${field} w-full`} type="text" autoComplete="off" value={password} onChange={(e) => setPassword(e.target.value)} minLength={8} maxLength={200} placeholder="At least 8 characters" />
        </label>
        <label className="block space-y-1">
          <span className="text-muted-foreground">Expires</span>
          <select className={`${field} w-full`} value={days} onChange={(e) => setDays(e.target.value)}>
            <option value="0">Never</option>
            <option value="7">In 7 days</option>
            <option value="30">In 30 days</option>
            <option value="90">In 90 days</option>
            <option value="365">In a year</option>
          </select>
        </label>
      </div>
      <div className="flex items-center justify-between gap-2">
        {share ? (
          <button type="button" className="text-[12px] text-muted-foreground hover:text-destructive" onClick={revoke}>
            Take the link back
          </button>
        ) : (
          <span />
        )}
        <Button type="button" variant="ghost" disabled={busy || (password !== "" && password.length < 8)} onClick={make}>
          {share ? "Make a new link" : "Make a link"}
        </Button>
      </div>
    </div>
  );
}

