import { type CSSProperties, useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { ask, Badge, Button, Card, confirmAction, CopyButton, Empty, ErrorText, field, Loading, Menu, PageHeader, toast } from "../ui";
import { PickBrand } from "@/components/pick-brand";
import { useBrand } from "@/lib/brand";
import type { Tab } from "@/lib/paths";
import { nameItem } from "@/lib/records";
import { track } from "@/lib/analytics";
import { ago } from "../format";
import { type InsightEmbed, withFilters } from "@/lib/insight-embed";
import { openDraft, type PanelKind, useInsightsCatalog } from "@/components/insight-panels";
import { FunnelChart, FunnelsFor, label, opLabels, presetLabels, today } from "./Funnels";
import { cleanTrend, TrendsFor, TrendView } from "./Trends";
import { cleanRetention, RetentionFor, RetentionView } from "./Retention";
import { cleanPaths, PathsFor, PathsView } from "./Paths";
import { cleanLifecycle, LifecycleFor, LifecycleView } from "./LifecycleInsight";
import { cleanStickiness, StickinessFor, StickinessView, type StickinessResult, type StickinessSpec } from "./StickinessInsight";
import { dimLabel, format as formatMetric, tone, Unconverted } from "./TrackerReport";

// Dashboards (docs/insights.md): a grid of tiles, each a saved funnel or
// trend run with the dashboard's date range, a saved report, or a note. Twelve columns;
// tiles move and resize with the arrows on each one. Editing a tile opens
// the same builder as the Trends or Funnels page, inside the dashboard.

type Dashboard = Schemas["Dashboard"];
type Detail = Schemas["DashboardDetail"];
type Tile = Schemas["DashboardTile"];
type Insight = Schemas["Insight"];
type ReportView = Schemas["ReportView"];
type Report = Schemas["DrilldownReport"];
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
type Filter = Schemas["InsightFilter"];
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
          <DashboardPage key={`${brand.id}:${item}`} ws={ws} brand={brand.id} id={item} canManage={canManage} back={() => go("dashboards")} go={go} />
        ) : (
          <>
            <PageHeader title="Dashboards" description="Your saved funnels and trends on one page, all over the same dates. Build one per question: the weekly numbers, a launch, a channel." />
            <AskBox key={`ask:${brand.id}`} brand={brand.id} canManage={canManage} go={go} />
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

type Question = Schemas["InsightQuestion"];

const askExamples = ["How many people signed up each week this quarter?", "Where do visitors from Google ads drop off before paying?", "How many days a month do people use the app?"];

// AskBox turns a question in plain English into a chart. The workspace's
// own AI model writes the chart from the brand's event names and their
// descriptions (never from people or values); the engine checks it, and
// the answer shows here, live, to open in its builder or save.
function AskBox({ brand, canManage, go }: { brand: string; canManage: boolean; go: Props["go"] }) {
  const [text, setText] = useState("");
  const [q, setQ] = useState<Question | null>(null);
  const [error, setError] = useState<{ message: string; code?: string } | null>(null);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const waiting = q?.status === "queued" || q?.status === "running";

  // While the model works, look again every second or so, for two minutes.
  useEffect(() => {
    if (!q || !waiting) return;
    const started = Date.parse(q.created_at);
    const t = setTimeout(async () => {
      const { data, error } = await api.GET("/v1/brands/{brand}/insights/ask/{question}", { params: { path: { brand, question: q.id } } });
      if (data) setQ(Date.now() - started > 120000 && (data.status === "queued" || data.status === "running") ? { ...data, status: "failed", error: "This is taking too long. Try again in a moment." } : { ...data });
      else setError({ message: errorMessage(error) });
    }, 1200);
    return () => clearTimeout(t);
  }, [q, waiting, brand]);

  async function ask(question: string) {
    if (!question.trim() || waiting) return;
    setError(null);
    setSaved(false);
    setQ(null);
    const { data, error } = await api.POST("/v1/brands/{brand}/insights/ask", { params: { path: { brand } }, body: { question: question.trim() } });
    if (!data || !("id" in data)) {
      const code = (error as { error?: { code?: string } } | undefined)?.error?.code;
      const noModel = code === "connector_needed" || code === "ai_model_needed";
      return setError({ message: data ? "Held for approval in the mission." : noModel ? "Connect an AI model with your own key to ask questions. Outflare never supplies one." : errorMessage(error), code });
    }
    setQ(data);
  }

  async function save() {
    if (!q || !q.kind || !q.spec) return;
    setSaving(true);
    const { error } = await api.POST("/v1/brands/{brand}/insights", { params: { path: { brand } }, body: { name: q.name, kind: q.kind, spec: q.spec } });
    setSaving(false);
    if (error) return toast(errorMessage(error), "error");
    setSaved(true);
    toast(`Saved "${q.name}". Add it to a dashboard with Add tile.`);
  }

  const chart = q?.status === "ready" && q.kind && q.spec ? { kind: q.kind as Insight["kind"], name: q.name, spec: q.spec as Insight["spec"] } : null;
  return (
    <Card title="Ask a question" description="Type what you want to know and get a chart. Your own AI model writes it from your event names and their descriptions; it never sees people or their data.">
      <form
        className="flex flex-wrap items-center gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          void ask(text);
        }}
      >
        <input className={`${field} min-w-[260px] flex-1`} aria-label="Your question" maxLength={500} placeholder={askExamples[0]} value={text} onChange={(e) => setText(e.target.value)} />
        <Button type="submit" disabled={!text.trim() || waiting}>{waiting ? "Working…" : "Ask"}</Button>
      </form>
      {!q && !error && (
        <div className="mt-3 flex flex-wrap gap-2 text-[12px]">
          {askExamples.map((x) => (
            <button key={x} type="button" className="rounded-full border px-3 py-1 text-muted-foreground hover:bg-muted/50" onClick={() => { setText(x); void ask(x); }}>
              {x}
            </button>
          ))}
        </div>
      )}
      {error && (
        <div className="mt-3 flex flex-wrap items-center gap-3">
          <ErrorText>{error.message}</ErrorText>
          {(error.code === "connector_needed" || error.code === "ai_model_needed") && <Button variant="ghost" onClick={() => go("connectors")}>Connect an AI model</Button>}
        </div>
      )}
      {waiting && <p className="mt-3 text-[13px] text-muted-foreground">Reading your events and building the chart. This usually takes a few seconds.</p>}
      {q?.status === "failed" && <div className="mt-3"><ErrorText>{q.error || "That didn't work. Try again."}</ErrorText></div>}
      {q?.status === "ready" && !chart && <p className="mt-3 text-[13px]">{q.explanation}</p>}
      {chart && q && (
        <div className="mt-4 space-y-3">
          <p className="text-[13px] text-muted-foreground">{q.explanation}</p>
          <div className="h-[380px]">
            <InsightTile insight={chart} brand={brand} range={undefined} compare={false} />
          </div>
          <div className="flex flex-wrap gap-2">
            <Button variant="ghost" onClick={() => openDraft(chart.kind as PanelKind, chart.spec as Parameters<typeof openDraft>[1], go as Parameters<typeof openDraft>[2], "ask")}>Open in the builder</Button>
            {canManage && <Button onClick={save} disabled={saving || saved}>{saved ? "Saved" : saving ? "Saving…" : "Save this chart"}</Button>}
          </div>
        </div>
      )}
    </Card>
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

// What the dashboard page shows: the tiles, one tile's builder, or the
// list of things to add. Each is a full page inside the app, so the
// side menu stays.
// own: a tile not added yet that will keep its insight's own dates.
type View = { at: "grid" } | { at: "add" } | { at: "build"; kind: Insight["kind"]; tile?: Tile; own?: boolean } | { at: "note"; tile?: Tile };

// The page each kind of insight is built on, for "Open in …". Lifecycle
// and stickiness share the Retention page, which has no address for them.
const builderTab: Partial<Record<Insight["kind"], { tab: Tab; label: string }>> = {
  trend: { tab: "trends", label: "Trends" },
  funnel: { tab: "funnels", label: "Funnels" },
  retention: { tab: "retention", label: "Retention" },
  paths: { tab: "paths", label: "Paths" },
};

export type TileMenu = (t: Tile) => ({ label: string; onClick: () => void; danger?: boolean } | false)[];

const kindNames: Record<Insight["kind"], string> = { trend: "trend", funnel: "funnel", retention: "retention question", paths: "paths question", lifecycle: "lifecycle question", stickiness: "stickiness question" };

function DashboardPage({ ws, brand, id, canManage, back, go }: { ws: string; brand: string; id: string; canManage: boolean; back: () => void; go: Props["go"] }) {
  const [d, setD] = useState<Detail | null>(null);
  const [error, setError] = useState("");
  const [editing, setEditing] = useState(false);
  const [view, setView] = useState<View>({ at: "grid" });

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
  useEffect(() => {
    window.scrollTo({ top: 0 });
    if (view.at === "build" || view.at === "note") track("ui.dashboard_tile_editor_opened", { kind: view.at === "note" ? "note" : view.kind, new: !view.tile });
  }, [view]);

  async function patch(body: Schemas["DashboardInput"]): Promise<boolean> {
    const { data, error } = await api.PATCH("/v1/brands/{brand}/dashboards/{dashboard}", { params: { path: { brand, dashboard: id } }, body });
    if (!data) {
      toast(errorMessage(error), "error");
      return false;
    }
    setD(data);
    return true;
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
    const { x, y, w, h } = moved(t, dx, dy, dw, dh);
    if (w === t.w && x === t.x && y === t.y && h === t.h) return;
    track("ui.dashboard_tile_moved", { resized: dw !== 0 || dh !== 0 });
    await patch({ layout: settle(d!.tile_list, { ...t, x, y, w, h }) });
  }

  async function removeTile(t: Tile) {
    const { error, response } = await api.DELETE("/v1/brands/{brand}/dashboards/{dashboard}/tiles/{tile}", { params: { path: { brand, dashboard: id, tile: t.id } } });
    if (error || !response.ok) return toast(errorMessage(error), "error");
    toast("Taken off the dashboard");
    load();
  }

  async function addTile(body: Schemas["DashboardTileInput"]) {
    const { data, error } = await api.POST("/v1/brands/{brand}/dashboards/{dashboard}/tiles", { params: { path: { brand, dashboard: id } }, body });
    if (!data) return toast(errorMessage(error), "error");
    toast("Added to the dashboard");
    setView({ at: "grid" });
    load();
  }

  const edit = (t: Tile) => setView(t.insight ? { at: "build", kind: t.insight.kind, tile: t } : { at: "note", tile: t });

  // Changes one tile (its dates choice) and returns it as saved.
  async function patchTile(t: Tile, body: Schemas["DashboardTileInput"]): Promise<Tile | undefined> {
    const { data, error } = await api.PATCH("/v1/brands/{brand}/dashboards/{dashboard}/tiles/{tile}", { params: { path: { brand, dashboard: id, tile: t.id } }, body });
    if (!data) {
      toast(errorMessage(error), "error");
      return undefined;
    }
    setD(data);
    return data.tile_list.find((x) => x.id === t.id);
  }

  async function setOwnDates(t: Tile, own: boolean) {
    if (await patchTile(t, { own_dates: own })) toast(own ? "This report now keeps its own dates" : "This report now follows the dashboard's dates");
  }

  async function rename(t: Tile) {
    const ins = t.insight;
    if (!ins) return;
    const r = await ask({ title: `Rename ${ins.name}`, body: "The new name shows everywhere this report does, on other dashboards too.", input: { label: "Name", value: ins.name, maxLength: 120 }, choices: [{ label: "Rename", value: "ok", variant: "primary" }, { label: "Cancel", value: "", variant: "ghost" }] });
    const name = r?.choice === "ok" ? r.value.trim() : "";
    if (!name || name === ins.name) return;
    const { data, error } = await api.PATCH("/v1/brands/{brand}/insights/{insight}", { params: { path: { brand, insight: ins.id } }, body: { name } });
    if (!data) return toast(errorMessage(error), "error");
    toast("Renamed");
    load();
  }

  // A copy of the report, saved under its own name and put on this
  // dashboard beside the original, which stays as it is.
  async function duplicate(t: Tile) {
    const ins = t.insight;
    if (!ins) return;
    const { data, error } = await api.POST("/v1/brands/{brand}/insights", { params: { path: { brand } }, body: { kind: ins.kind, name: `${ins.name} (copy)`.slice(0, 120), spec: ins.spec } });
    if (!data || !("id" in data)) return toast(errorMessage(error), "error");
    await addTile({ insight_id: data.id, own_dates: t.own_dates, w: t.w, h: t.h });
  }

  const menu: TileMenu = (t) => {
    const ins = t.insight;
    if (!ins) return [];
    const page = builderTab[ins.kind];
    return [
      canManage && { label: "Edit", onClick: () => edit(t) },
      canManage && { label: "Rename", onClick: () => rename(t) },
      canManage && { label: "Duplicate", onClick: () => duplicate(t) },
      !!page && {
        label: `Open in ${page.label}`,
        onClick: () => {
          track("ui.dashboard_tile_opened_in_builder", { kind: ins.kind });
          go(page.tab, ins.id);
        },
      },
      canManage && !!d?.settings.date_range && { label: t.own_dates ? "Follow the dashboard's dates" : "Keep its own dates", onClick: () => setOwnDates(t, !t.own_dates) },
      canManage && { label: "Take off the dashboard", onClick: () => removeTile(t), danger: true },
    ];
  };

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!d) return <Loading />;
  const range = d.settings.date_range;
  const toGrid = { label: d.name, onClick: () => setView({ at: "grid" }) };

  if (view.at === "build") {
    const tile = view.tile;
    const ins = tile?.insight ?? undefined;
    const own = tile ? tile.own_dates : !!view.own;
    const others = ins ? "Save changes updates it everywhere it shows, on other dashboards too. Save as a copy adds the copy to this dashboard and leaves the original alone." : "Saved with your other " + kindNames[view.kind] + "s, and added to this dashboard.";
    const embed: InsightEmbed = {
      insight: ins,
      range: own ? undefined : range,
      onSaved: (i) => {
        // A change to the tile's own insight; anything else (a new one, a
        // copy) becomes a tile.
        if (tile && i.id === tile.insight_id) {
          setView({ at: "grid" });
          load();
        } else addTile({ insight_id: i.id, own_dates: own, ...(tile ? { w: tile.w, h: tile.h } : {}) });
      },
    };
    const props = { brand, canManage, embed };
    // Switching whose dates a report shows starts its builder again over
    // those dates.
    const pickDates = async (next: boolean) => {
      if (next === own) return;
      if (!tile) return setView({ ...view, own: next });
      const saved = await patchTile(tile, { own_dates: next });
      if (saved) setView({ ...view, tile: saved });
    };
    return (
      <>
        <PageHeader
          title={ins ? `Edit ${ins.name}` : `New ${kindNames[view.kind]}`}
          description={`${others}${d.settings.filters?.length ? " The dashboard's filters apply on top." : ""}`}
          back={toGrid}
        />
        {range && canManage && (
          <div className="mb-4 flex flex-wrap items-center gap-x-5 gap-y-2 rounded-lg border bg-card px-3 py-2 text-[13px]" role="radiogroup" aria-label="Dates this report shows on the dashboard">
            <span className="font-medium">Dates on this dashboard</span>
            <label className="flex items-center gap-2">
              <input type="radio" name="tile-dates" checked={!own} onChange={() => pickDates(false)} />
              Follow the dashboard ({rangeLabel(range).toLowerCase()})
            </label>
            <label className="flex items-center gap-2">
              <input type="radio" name="tile-dates" checked={own} onChange={() => pickDates(true)} />
              Keep this report's own dates
            </label>
            <span className="text-muted-foreground">{own ? "Pick them under Options below." : "Changing the dashboard's dates changes this report too."}</span>
          </div>
        )}
        <div key={`${tile?.id ?? "new"}:${own}`}>
          {view.kind === "funnel" ? <FunnelsFor ws={ws} {...props} /> : view.kind === "retention" ? <RetentionFor {...props} /> : view.kind === "paths" ? <PathsFor {...props} /> : view.kind === "lifecycle" ? <LifecycleFor {...props} /> : view.kind === "stickiness" ? <StickinessFor {...props} /> : <TrendsFor {...props} />}
        </div>
      </>
    );
  }

  if (view.at === "note") {
    return <NoteEditor tile={view.tile} back={toGrid} onSave={async (note) => {
      if (!view.tile) return addTile({ note, w: 4, h: 2 });
      const { data, error } = await api.PATCH("/v1/brands/{brand}/dashboards/{dashboard}/tiles/{tile}", { params: { path: { brand, dashboard: id, tile: view.tile.id } }, body: { note } });
      if (!data) return toast(errorMessage(error), "error");
      toast("Saved");
      setD(data);
      setView({ at: "grid" });
    }} />;
  }

  if (view.at === "add") {
    return <AddTile brand={brand} back={toGrid} onBuild={(kind) => setView({ at: "build", kind })} onNote={() => setView({ at: "note" })} onAdd={(insight) => addTile({ insight_id: insight })} onAddReport={(view) => addTile({ report_view_id: view })} />;
  }

  return (
    <>
      <PageHeader
        title={d.name}
        description={d.description || undefined}
        back={{ label: "Dashboards", onClick: back }}
        action={
          canManage &&
          (editing ? (
            <>
              <Button variant="ghost" onClick={() => setView({ at: "add" })}>Add tile</Button>
              <Button onClick={() => setEditing(false)}>Done</Button>
            </>
          ) : (
            <Button variant="ghost" onClick={() => setEditing(true)}>Edit dashboard</Button>
          ))
        }
      />
      <div className="mb-4 flex flex-wrap items-center gap-x-4 gap-y-2 text-[13px]">
        <RangePicker value={range} onChange={(r) => patch({ settings: { ...d.settings, date_range: r } })} disabled={!canManage} />
        <label className="flex items-center gap-2">
          <input type="checkbox" checked={!!d.settings.compare} disabled={!canManage} onChange={(e) => patch({ settings: { ...d.settings, compare: e.target.checked } })} />
          Compare with the period before
        </label>
        <DashboardFilters brand={brand} filters={d.settings.filters ?? []} disabled={!canManage} onChange={(filters) => patch({ settings: { ...d.settings, filters } })} />
      </div>
      {editing && canManage && <DashboardDetails d={d} brand={brand} onSave={patch} onDelete={remove} onShared={load} />}
      {d.tile_list.length === 0 ? (
        <Empty title="An empty dashboard" action={canManage ? <Button onClick={() => setView({ at: "add" })}>Add a tile</Button> : undefined}>
          Add a trend, a funnel or a note. Each tile shows the dates you pick above.
        </Empty>
      ) : (
        <Grid tiles={d.tile_list} brand={brand} range={range} compare={!!d.settings.compare} filters={d.settings.filters} editing={editing && canManage} onPlace={place} onRemove={removeTile} onEdit={canManage ? edit : undefined} menu={menu} />
      )}
    </>
  );
}

// DashboardFilters are conditions on event properties and page fields
// (plan = pro, utm_source = google) that every tile takes, on its event
// and page view parts, as PostHog's dashboard filters do. Each shows as a
// chip; + Filter adds one in place.
function DashboardFilters({ brand, filters, disabled, onChange }: { brand: string; filters: Filter[]; disabled: boolean; onChange: (f: Filter[]) => void }) {
  const catalog = useInsightsCatalog(brand);
  const [draft, setDraft] = useState<Filter | null>(null);
  const valueless = (op: Filter["op"]) => op === "exists" || op === "not_exists";
  const text = (f: Filter) => `${label(f.property)} ${opLabels[f.op].toLowerCase()}${valueless(f.op) ? "" : ` ${Array.isArray(f.value) ? f.value.join(", ") : String(f.value ?? "")}`}`;
  const ready = draft && draft.property.trim() && (valueless(draft.op) || (draft.value !== undefined && draft.value !== "" && !(Array.isArray(draft.value) && draft.value.length === 0)));
  return (
    <div className="flex flex-wrap items-center gap-2">
      {filters.map((f, i) => (
        <span key={i} className="inline-flex items-center gap-1 rounded-md border bg-muted/40 py-0.5 pr-1 pl-2 text-[12px]">
          {text(f)}
          {!disabled && (
            <button type="button" aria-label={`Remove filter ${text(f)}`} className="rounded px-1 text-muted-foreground hover:bg-muted hover:text-foreground" onClick={() => onChange(filters.filter((_, j) => j !== i))}>
              ×
            </button>
          )}
        </span>
      ))}
      {!disabled && !draft && filters.length < 5 && (
        <Button variant="ghost" onClick={() => setDraft({ property: "", op: "eq", value: "" })}>
          + Filter
        </Button>
      )}
      {draft && (
        <form
          className="flex flex-wrap items-center gap-1.5"
          onSubmit={(e) => {
            e.preventDefault();
            if (!ready) return;
            onChange([...filters, valueless(draft.op) ? { property: draft.property.trim(), op: draft.op } : { ...draft, property: draft.property.trim() }]);
            setDraft(null);
          }}
        >
          <input aria-label="Filter field" list="dashboard-filter-fields" autoFocus className={`${field} w-40`} placeholder="Field, such as plan" value={draft.property} onChange={(e) => setDraft({ ...draft, property: e.target.value })} />
          <datalist id="dashboard-filter-fields">
            {(catalog?.fields.event ?? []).map((f) => (
              <option key={f} value={f}>{label(f)}</option>
            ))}
          </datalist>
          <select aria-label="Filter comparison" className={field} value={draft.op} onChange={(e) => setDraft({ ...draft, op: e.target.value as Filter["op"] })}>
            {(Object.keys(opLabels) as Filter["op"][]).map((o) => (
              <option key={o} value={o}>{opLabels[o]}</option>
            ))}
          </select>
          {!valueless(draft.op) && (
            <input
              aria-label="Filter value"
              className={`${field} w-36`}
              placeholder={draft.op === "in" ? "Values, with commas" : "Value"}
              value={Array.isArray(draft.value) ? draft.value.join(", ") : String(draft.value ?? "")}
              onChange={(e) => {
                const v = e.target.value;
                const value = draft.op === "in" ? v.split(",").map((z) => z.trim()).filter(Boolean) : ["gt", "gte", "lt", "lte"].includes(draft.op) && v !== "" && !isNaN(Number(v)) ? Number(v) : v;
                setDraft({ ...draft, value });
              }}
            />
          )}
          <Button type="submit" disabled={!ready}>Apply</Button>
          <Button type="button" variant="ghost" onClick={() => setDraft(null)}>Cancel</Button>
        </form>
      )}
    </div>
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

// moved is a tile's place after a move or resize, kept inside the grid's
// columns; a tile is 1 to 24 rows high.
function moved(t: Tile, dx: number, dy: number, dw: number, dh: number): Tile {
  const w = Math.max(1, Math.min(COLUMNS, t.w + dw));
  const x = Math.max(0, Math.min(COLUMNS - w, t.x + dx));
  const y = Math.max(0, t.y + dy);
  const h = Math.max(1, Math.min(24, t.h + dh));
  return { ...t, x, y, w, h };
}

const GAP = 12; // the grid's gap, gap-3

// settle puts a moved tile in its new place and pushes every tile it now
// covers down, below it, as far as needed, so tiles never overlap. It
// answers the places that changed.
function settle(tiles: Tile[], t: Tile): { id: string; x: number; y: number; w: number; h: number }[] {
  const over = (a: Tile, b: Tile) => a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;
  const placed: Tile[] = [t];
  for (const o of tiles.filter((o) => o.id !== t.id).sort((a, b) => a.y - b.y || a.x - b.x)) {
    let n = o;
    for (let hit = placed.find((p) => over(p, n)); hit; hit = placed.find((p) => over(p, n))) n = { ...n, y: hit.y + hit.h };
    placed.push(n);
  }
  // Then every tile floats up into any gap above it, top first, as on
  // PostHog's dashboards: dragging a tile below another swaps them.
  const done: Tile[] = [];
  for (const p of placed.sort((a, b) => a.y - b.y || (a.id === t.id ? -1 : b.id === t.id ? 1 : a.x - b.x))) {
    let n = { ...p, y: 0 };
    for (let hit = done.find((q) => over(q, n)); hit; hit = done.find((q) => over(q, n))) n = { ...n, y: hit.y + hit.h };
    done.push(n);
  }
  const was = new Map(tiles.map((o) => [o.id, o]));
  return done.filter((n) => { const o = was.get(n.id); return !o || o.x !== n.x || o.y !== n.y || o.w !== n.w || o.h !== n.h; }).map(({ id, x, y, w, h }) => ({ id, x, y, w, h }));
}

export function Grid({ tiles, brand, range, compare, filters, editing, onPlace, onRemove, onEdit, menu, run }: { tiles: Tile[]; brand: string; range: DateRange | undefined; compare: boolean; filters?: Filter[]; editing: boolean; onPlace: (t: Tile, dx: number, dy: number, dw: number, dh: number) => void; onRemove: (t: Tile) => void; onEdit?: (t: Tile) => void; menu?: TileMenu; run?: Runner }) {
  // Dragging: the bar on top of a tile moves it, the corner resizes it,
  // in whole columns and rows, saved when let go. The arrows stay for the
  // keyboard.
  const box = useRef<HTMLDivElement>(null);
  const [drag, setDrag] = useState<{ t: Tile; mode: "move" | "size"; sx: number; sy: number; dx: number; dy: number } | null>(null);
  const handle = (t: Tile, mode: "move" | "size") => ({
    onPointerDown: (e: React.PointerEvent<HTMLElement>) => {
      if (e.button !== 0 || (e.target as HTMLElement).closest("button")) return;
      e.preventDefault();
      e.currentTarget.setPointerCapture(e.pointerId);
      setDrag({ t, mode, sx: e.clientX, sy: e.clientY, dx: 0, dy: 0 });
    },
    onPointerMove: (e: React.PointerEvent<HTMLElement>) => {
      if (!drag || drag.t.id !== t.id || !box.current) return;
      const col = (box.current.clientWidth + GAP) / COLUMNS;
      const dx = Math.round((e.clientX - drag.sx) / col), dy = Math.round((e.clientY - drag.sy) / (ROW + GAP));
      if (dx !== drag.dx || dy !== drag.dy) setDrag({ ...drag, dx, dy });
    },
    onPointerUp: () => {
      if (!drag || drag.t.id !== t.id) return;
      const { dx, dy } = drag;
      setDrag(null);
      if (dx || dy) {
        if (mode === "move") onPlace(t, dx, dy, 0, 0);
        else onPlace(t, 0, 0, dx, dy);
      }
    },
    onPointerCancel: () => setDrag(null),
  });
  // The order stays the saved one while dragging, so the dragged tile's
  // element never moves in the page (that would drop the pointer).
  const shown = [...tiles].sort((a, b) => a.y - b.y || a.x - b.x).map((t) => (drag?.t.id === t.id ? (drag.mode === "move" ? moved(t, drag.dx, drag.dy, 0, 0) : moved(t, 0, 0, drag.dx, drag.dy)) : t));
  const rows = Math.max(1, ...shown.map((t) => t.y + t.h));
  return (
    // On a phone the tiles stack, full width, in the grid's reading order.
    <div ref={box} className="grid grid-cols-1 gap-3 sm:[grid-auto-rows:var(--row)] sm:[grid-template-columns:var(--cols)] sm:[grid-template-rows:var(--rows)]" style={{ "--cols": `repeat(${COLUMNS}, minmax(0, 1fr))`, "--row": `${ROW}px`, "--rows": `repeat(${rows}, ${ROW}px)` } as CSSProperties}>
      {shown.map((t) => (
        <div
          key={t.id}
          className={`relative h-[var(--h)] min-w-0 sm:h-auto sm:[grid-column:var(--col)] sm:[grid-row:var(--grow)] ${drag?.t.id === t.id ? "z-10 opacity-90 ring-2 ring-ring" : ""}`}
          style={{ "--col": `${t.x + 1} / span ${t.w}`, "--grow": `${t.y + 1} / span ${t.h}`, "--h": `${Math.min(t.h, 5) * ROW}px` } as CSSProperties}
        >
          <TileCard tile={t} brand={brand} range={range} compare={compare} filters={filters} run={run} onEdit={onEdit && !editing ? () => onEdit(t) : undefined} menu={menu && !editing ? menu(t) : undefined} />
          {editing && (
            <div className="absolute inset-x-0 top-0 flex cursor-move touch-none items-center justify-between gap-1 rounded-t-md border-b bg-background/95 px-2 py-1 text-[12px] backdrop-blur select-none" title="Drag to move" {...handle(tiles.find((x) => x.id === t.id)!, "move")}>
              <span className="flex items-center gap-0.5">
                <span className="mr-1 text-muted-foreground" aria-hidden>⠿ Drag or</span>
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
              <span className="flex items-center gap-0.5">
                {onEdit && !t.report_view_id && (t.insight || !t.insight_id) && (
                  <button type="button" className="rounded px-1.5 py-0.5 hover:bg-muted" onClick={() => onEdit(t)}>
                    Edit
                  </button>
                )}
                <Arrow label="Take off the dashboard" onClick={() => onRemove(t)}>✕</Arrow>
              </span>
            </div>
          )}
          {editing && (
            <div
              className="absolute right-0 bottom-0 hidden size-5 cursor-se-resize touch-none items-end justify-end p-0.5 text-muted-foreground select-none sm:flex"
              title="Drag to resize"
              aria-hidden
              {...handle(tiles.find((x) => x.id === t.id)!, "size")}
            >
              <svg viewBox="0 0 10 10" className="size-3" fill="none" stroke="currentColor" strokeWidth="1.5"><path d="M9 1 1 9M9 5 5 9" /></svg>
            </div>
          )}
        </div>
      ))}
    </div>
  );
}

function EditLink({ onClick, className = "" }: { onClick: () => void; className?: string }) {
  return (
    <button type="button" className={`rounded border bg-background px-1.5 py-0.5 text-[11px] text-foreground hover:bg-muted ${className}`} onClick={onClick}>
      Edit
    </button>
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
function TileCard({ tile, brand, range, compare, filters, run, onEdit, menu }: { tile: Tile; brand: string; range: DateRange | undefined; compare: boolean; filters?: Filter[]; run?: Runner; onEdit?: () => void; menu?: ReturnType<TileMenu> }) {
  const ins = tile.insight;
  if (tile.report_view) {
    // A share link names the report but never runs it: its numbers are
    // for the brand's own team.
    if (run) {
      return (
        <div className="h-full rounded-md border border-dashed bg-card/50 p-4 text-[13px]">
          <div className="font-medium">{tile.report_view.name}</div>
          <p className="mt-1 text-muted-foreground">This report is shown to signed-in team members only.</p>
        </div>
      );
    }
    return <ReportTile view={tile.report_view} brand={brand} />;
  }
  if (!ins) {
    return (
      <div className={`relative h-full overflow-y-auto rounded-md border border-dashed bg-card/50 p-4 text-[13px] leading-relaxed whitespace-pre-wrap ${onEdit ? "pr-16" : ""}`}>
        {tile.note || <span className="text-muted-foreground">{tile.insight_id ? "This insight was deleted." : "An empty note."}</span>}
        {onEdit && !tile.insight_id && <EditLink onClick={onEdit} className="absolute top-2 right-2" />}
      </div>
    );
  }
  // A tile that keeps its own dates runs over its insight's, whatever
  // the dashboard shows.
  return <InsightTile insight={ins} brand={brand} range={tile.own_dates ? undefined : range} ownDates={!!tile.own_dates && !!range} compare={compare} filters={filters} run={run && ((kind, rng) => run(kind, tile.id, rng))} onEdit={onEdit} menu={menu} />;
}

// The columns a report tile shows, for each thing it can report on.
const reportTileColumns: { key: "visits" | "conversions" | "revenue" | "cost" | "profit" | "roi"; label: string; kind: "count" | "money" | "percent" }[] = [
  { key: "visits", label: "Visits", kind: "count" },
  { key: "conversions", label: "Conv.", kind: "count" },
  { key: "revenue", label: "Revenue", kind: "money" },
  { key: "cost", label: "Cost", kind: "money" },
  { key: "profit", label: "Profit", kind: "money" },
  { key: "roi", label: "ROI", kind: "percent" },
];

// The tracker levels whose values are ids, and where their names are.
const reportNameLists: Record<string, "/v1/brands/{brand}/tracking-campaigns" | "/v1/brands/{brand}/traffic-sources" | "/v1/brands/{brand}/offers" | "/v1/brands/{brand}/landers"> = {
  tracking_campaign: "/v1/brands/{brand}/tracking-campaigns",
  traffic_source: "/v1/brands/{brand}/traffic-sources",
  offer: "/v1/brands/{brand}/offers",
  lander: "/v1/brands/{brand}/landers",
};

// ReportTile shows a saved report (Tracker → Report): its first level's
// rows as the report sorts them, with the totals, over the dates saved
// with the report. The dashboard's own dates and filters don't change it.
export function ReportTile({ view, brand }: { view: NonNullable<Tile["report_view"]>; brand: string }) {
  const [report, setReport] = useState<Report | null>(null);
  const [names, setNames] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const first = view.group_by[0] ?? "";
  useEffect(() => {
    let live = true;
    setReport(null);
    setError("");
    api.GET("/v1/brands/{brand}/drilldown", { params: { path: { brand }, query: { view: view.id, limit: 25 } } }).then(({ data, error }) => {
      if (!live) return;
      if (data) setReport(data as Report);
      else setError(errorMessage(error));
    });
    const list = reportNameLists[first];
    if (list)
      api.GET(list, { params: { path: { brand } } }).then(({ data }) => {
        if (live) setNames(Object.fromEntries(((data as { data?: { id: string; name: string }[] } | undefined)?.data ?? []).map((x) => [x.id, x.name])));
      });
    return () => {
      live = false;
    };
  }, [brand, view.id, first]);
  const cols = reportTileColumns.filter((c) => report?.cost_available || !["cost", "profit", "roi"].includes(c.key));
  return (
    <div className="flex h-full flex-col rounded-md border bg-card" data-testid="report-tile">
      <header className="flex flex-wrap items-baseline justify-between gap-2 border-b px-4 py-2">
        <h3 className="text-[13px] font-semibold">{view.name}</h3>
        <span className="text-[12px] text-muted-foreground">
          {view.source === "tracker" ? "Tracker clicks" : "Site visits"}
          {report ? ` · ${report.from} to ${report.to}` : ""}
        </span>
      </header>
      <div className="min-h-0 flex-1 overflow-auto px-4 py-2">
        {error ? (
          <ErrorText>{error}</ErrorText>
        ) : !report ? (
          <Loading />
        ) : (
          <table className="w-full text-[13px]">
            <thead>
              <tr className="text-left text-[12px] text-muted-foreground">
                <th className="py-1 pr-3 font-medium">{first ? dimLabel[first] ?? first.replaceAll("_", " ") : "Total"}</th>
                {cols.map((c) => (
                  <th key={c.key} className="px-2 py-1 text-right font-medium whitespace-nowrap">{c.label}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {report.rows.map((row) => (
                <tr key={row.keys.join("\u0000")} className="border-t">
                  <td className="max-w-[14rem] truncate py-1 pr-3">{(row.keys[0] && (report.names?.[row.keys[0]] ?? names[row.keys[0]] ?? row.keys[0])) || <span className="text-muted-foreground">(none)</span>}</td>
                  {cols.map((c) => (
                    <td key={c.key} className={`px-2 py-1 text-right font-mono tabular-nums whitespace-nowrap ${tone(c.key, row.metrics[c.key])}`}>{formatMetric(row.metrics[c.key], c.kind, report.currency)}</td>
                  ))}
                </tr>
              ))}
              <tr className="border-t font-medium">
                <td className="py-1 pr-3">Total</td>
                {cols.map((c) => (
                  <td key={c.key} className={`px-2 py-1 text-right font-mono tabular-nums whitespace-nowrap ${tone(c.key, report.totals[c.key])}`}>{formatMetric(report.totals[c.key], c.kind, report.currency)}</td>
                ))}
              </tr>
            </tbody>
          </table>
        )}
        {(report?.unconverted_currencies?.length ?? 0) > 0 && <div className="mt-2"><Unconverted currencies={report!.unconverted_currencies} to={report!.currency} /></div>}
      </div>
    </div>
  );
}

export function InsightTile({ insight, brand, range, ownDates, compare, filters, run, onEdit, menu }: { insight: Pick<Insight, "kind" | "name" | "spec">; brand: string; range: DateRange | undefined; compare: boolean; filters?: Filter[]; run?: (kind: Insight["kind"], range: DateRange | undefined) => Promise<{ data?: unknown; error?: unknown }>; onEdit?: () => void; ownDates?: boolean; menu?: ReturnType<TileMenu> }) {
  const [funnel, setFunnel] = useState<FunnelResult | null>(null);
  const [trend, setTrend] = useState<TrendResult | null>(null);
  const [retention, setRetention] = useState<RetentionResult | null>(null);
  const [paths, setPaths] = useState<PathsResult | null>(null);
  const [lifecycle, setLifecycle] = useState<LifecycleResult | null>(null);
  const [stickiness, setStickiness] = useState<StickinessResult | null>(null);
  const [error, setError] = useState("");
  const [running, setRunning] = useState(true);
  const run_ = useRef(0);
  const spec = useMemo(() => {
    const s = withFilters(insight.kind, { ...(insight.spec as FunnelSpec | TrendSpec | RetentionSpec | PathsSpec | LifecycleSpec | StickinessSpec) }, filters);
    if (range) s.date_range = range;
    if (insight.kind === "trend") (s as TrendSpec).compare = compare || (s as TrendSpec).compare;
    return s;
  }, [insight, range, compare, filters]);

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
          else if (insight.kind === "stickiness") setStickiness(data as StickinessResult);
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
      } else if (insight.kind === "stickiness") {
        const { data, error } = await api.POST("/v1/brands/{brand}/insights/stickiness", { params: { path: { brand } }, body: cleanStickiness(spec as StickinessSpec) });
        if (n !== run_.current) return;
        if (!data) setError(errorMessage(error));
        else {
          setError("");
          setStickiness(data);
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
  const forecast = insight.kind === "trend" && (spec as TrendSpec).forecast !== false;
  return (
    <div className="flex h-full flex-col overflow-hidden rounded-md border bg-card">
      <div className="flex items-center justify-between gap-2 border-b px-3 py-1.5">
        {onEdit ? (
          <button type="button" className="min-w-0 truncate text-left text-[13px] font-medium hover:underline" title={`Edit ${insight.name}`} onClick={onEdit}>
            {insight.name}
          </button>
        ) : (
          <span className="truncate text-[13px] font-medium" title={insight.name}>
            {insight.name}
          </span>
        )}
        <span className="flex shrink-0 items-center gap-1 text-[11px] text-muted-foreground">
          {running && <Badge>updating</Badge>}
          {insight.kind === "funnel" ? "Funnel" : insight.kind === "retention" ? "Retention" : insight.kind === "paths" ? "Paths" : insight.kind === "lifecycle" ? "Lifecycle" : insight.kind === "stickiness" ? "Stickiness" : display === "number" ? "Number" : display === "table" ? "Table" : display === "pie" ? "Shares" : display === "map" ? "Map" : "Trend"}
          {ownDates && <Badge title="This report keeps its own dates, whatever the dashboard shows.">{(insight.spec as { date_range?: DateRange }).date_range ? rangeLabel((insight.spec as { date_range: DateRange }).date_range) : "Own dates"}</Badge>}
          {onEdit && <EditLink onClick={onEdit} />}
          {menu && <Menu label="" aria={`More for ${insight.name}`} items={menu} />}
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
        ) : insight.kind === "stickiness" ? (
          stickiness ? (
            <StickinessView result={stickiness} compact />
          ) : (
            <Loading />
          )
        ) : trend ? (
          <TrendView result={trend} display={display as NonNullable<TrendSpec["display"]>} compact annotations={notes} forecast={forecast} view={spec as TrendSpec} />
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

// AddTile puts something new on the dashboard: a new trend, funnel or
// other question built right here, one already saved, or a note.
const newKinds: { kind: Insight["kind"]; title: string; text: string }[] = [
  { kind: "trend", title: "Trend", text: "A number over time, such as sign-ups a day, as lines, bars, one number or a table." },
  { kind: "funnel", title: "Funnel", text: "How many people get from one step to the next, such as sign-up to first order." },
  { kind: "retention", title: "Retention", text: "How many people come back after they first did something." },
  { kind: "paths", title: "Paths", text: "Where people go before or after a page or event." },
  { kind: "lifecycle", title: "Lifecycle", text: "New, returning, resurrected and dormant people over time." },
  { kind: "stickiness", title: "Stickiness", text: "How many days, weeks or months people come back in." },
];

function AddTile({ brand, back, onBuild, onNote, onAdd, onAddReport }: { brand: string; back: { label: string; onClick: () => void }; onBuild: (kind: Insight["kind"]) => void; onNote: () => void; onAdd: (insight: string) => Promise<void>; onAddReport: (view: string) => Promise<void> }) {
  const [saved, setSaved] = useState<Insight[] | null>(null);
  const [reports, setReports] = useState<ReportView[]>([]);
  const [busy, setBusy] = useState(false);
  const [q, setQ] = useState("");

  useEffect(() => {
    api.GET("/v1/brands/{brand}/insights", { params: { path: { brand }, query: { limit: 200 } } }).then(({ data, error }) => (data ? setSaved(data.data) : toast(errorMessage(error), "error")));
    api.GET("/v1/brands/{brand}/report-views", { params: { path: { brand } } }).then(({ data }) => setReports((data?.data ?? []) as ReportView[]));
  }, [brand]);

  // A report on a dashboard is seen by the whole team, so a private one is
  // shared first.
  const addReport = async (v: ReportView) => {
    if (!v.shared && !(await confirmAction(`Share "${v.name}" with the team?`, "Everyone who opens the dashboard sees this report, so it is shared with the brand's team first.", "Share and add", false))) return;
    setBusy(true);
    if (!v.shared) {
      const { error } = await api.PATCH("/v1/brands/{brand}/report-views/{id}", { params: { path: { brand, id: v.id } }, body: { shared: true } });
      if (error) {
        setBusy(false);
        return toast(errorMessage(error), "error");
      }
    }
    await onAddReport(v.id);
    setBusy(false);
  };

  const shown = (saved ?? []).filter((i) => !q || i.name.toLowerCase().includes(q.toLowerCase()));
  return (
    <>
      <PageHeader title="Add a tile" description="Build a new chart, pick one you saved before, or write a note." back={back} />
      <div className="space-y-6">
        <section className="space-y-2">
          <h2 className="text-[13px] font-semibold">Build a new one</h2>
          <ul className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {newKinds.map((k) => (
              <li key={k.kind}>
                <button className="h-full w-full rounded-md border bg-card p-4 text-left transition-colors hover:bg-muted/50" onClick={() => onBuild(k.kind)}>
                  <span className="font-medium">{k.title}</span>
                  <p className="mt-1 text-[13px] text-muted-foreground">{k.text}</p>
                </button>
              </li>
            ))}
            <li>
              <button className="h-full w-full rounded-md border border-dashed bg-card/50 p-4 text-left transition-colors hover:bg-muted/50" onClick={onNote}>
                <span className="font-medium">Note</span>
                <p className="mt-1 text-[13px] text-muted-foreground">Text between the charts: what the dashboard is for, or what to look at first.</p>
              </button>
            </li>
          </ul>
        </section>
        <section className="space-y-2">
          <h2 className="text-[13px] font-semibold">Saved before</h2>
          {saved === null ? (
            <Loading />
          ) : saved.length === 0 ? (
            <p className="text-[13px] text-muted-foreground">Nothing saved yet.</p>
          ) : (
            <>
              <input className={`${field} w-full sm:w-72`} placeholder="Find by name" aria-label="Find a saved chart" value={q} onChange={(e) => setQ(e.target.value)} />
              <ul className="divide-y rounded-md border bg-card">
                {shown.map((i) => (
                  <li key={i.id} className="flex items-center justify-between gap-3 px-3 py-2">
                    <div className="min-w-0">
                      <div className="truncate text-[13px] font-medium">{i.name}</div>
                      <div className="text-[12px] text-muted-foreground">
                        {kindTitle(i.kind)} · saved {ago(i.updated_at)}
                      </div>
                    </div>
                    <Button
                      variant="ghost"
                      disabled={busy}
                      onClick={async () => {
                        setBusy(true);
                        await onAdd(i.id);
                        setBusy(false);
                      }}
                    >
                      Add
                    </Button>
                  </li>
                ))}
                {shown.length === 0 && <li className="px-3 py-2 text-[13px] text-muted-foreground">Nothing by that name.</li>}
              </ul>
            </>
          )}
        </section>
        {reports.length > 0 && (
          <section className="space-y-2">
            <h2 className="text-[13px] font-semibold">Saved reports</h2>
            <p className="text-[13px] text-muted-foreground">Reports saved on Tracker → Report. A tile shows the report's first level and totals, over the dates saved with it.</p>
            <ul className="divide-y rounded-md border bg-card">
              {reports.map((v) => (
                <li key={v.id} className="flex items-center justify-between gap-3 px-3 py-2">
                  <div className="min-w-0">
                    <div className="truncate text-[13px] font-medium">{v.name}</div>
                    <div className="text-[12px] text-muted-foreground">
                      {v.spec.source === "tracker" ? "Tracker clicks" : "Site visits"}
                      {v.spec.group_by?.length ? ` by ${v.spec.group_by.join(", ").replaceAll("_", " ")}` : ""}
                      {v.shared ? "" : " · only you see it"}
                    </div>
                  </div>
                  <Button variant="ghost" disabled={busy} onClick={() => void addReport(v)}>
                    {v.shared ? "Add" : "Share and add"}
                  </Button>
                </li>
              ))}
            </ul>
          </section>
        )}
      </div>
    </>
  );
}

const kindTitle = (k: Insight["kind"]) => newKinds.find((n) => n.kind === k)?.title ?? "Trend";

// NoteEditor writes a new note tile or changes one.
function NoteEditor({ tile, back, onSave }: { tile?: Tile; back: { label: string; onClick: () => void }; onSave: (note: string) => Promise<void> }) {
  const [note, setNote] = useState(tile?.note ?? "");
  const [busy, setBusy] = useState(false);
  return (
    <>
      <PageHeader title={tile ? "Edit note" : "New note"} back={back} />
      <form
        className="max-w-2xl space-y-3"
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          await onSave(note.trim());
          setBusy(false);
        }}
      >
        <textarea className={`${field} w-full`} rows={8} maxLength={4000} placeholder="What this dashboard is for, or what to look at first." aria-label="Note" value={note} onChange={(e) => setNote(e.target.value)} autoFocus />
        <div className="flex gap-2">
          <Button type="submit" disabled={!note.trim() || busy}>{tile ? "Save" : "Add note"}</Button>
          <Button type="button" variant="ghost" onClick={back.onClick}>Cancel</Button>
        </div>
      </form>
    </>
  );
}

// DashboardDetails shows while editing, above the tiles: the name, what
// it is for, the digest, the share link, and deleting it.
function DashboardDetails({ d, brand, onSave, onDelete, onShared }: { d: Detail; brand: string; onSave: (body: Schemas["DashboardInput"]) => Promise<boolean>; onDelete: () => void; onShared: () => void }) {
  const [name, setName] = useState(d.name);
  const [description, setDescription] = useState(d.description);
  const [busy, setBusy] = useState(false);
  const changed = name.trim() !== d.name || description.trim() !== d.description;
  return (
    <div className="mb-6 grid gap-4 lg:grid-cols-2">
      <Card title="Details">
        <form
          className="space-y-3 text-[13px]"
          onSubmit={async (e) => {
            e.preventDefault();
            setBusy(true);
            if (await onSave({ name: name.trim(), description: description.trim() })) toast("Saved");
            setBusy(false);
          }}
        >
          <label className="block space-y-1">
            <span className="font-medium">Name</span>
            <input className={`${field} w-full`} value={name} onChange={(e) => setName(e.target.value)} required maxLength={120} />
          </label>
          <label className="block space-y-1">
            <span className="font-medium">Description</span>
            <textarea className={`${field} w-full`} rows={2} value={description} onChange={(e) => setDescription(e.target.value)} maxLength={1000} placeholder="What this dashboard is for" />
          </label>
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={!!d.settings.digest} onChange={(e) => onSave({ settings: { ...d.settings, digest: e.target.checked } })} />
            Put its trends in the digest
          </label>
          <div className="flex items-center justify-between gap-2 pt-1">
            <Button type="button" variant="danger" onClick={onDelete}>
              Delete dashboard
            </Button>
            <Button type="submit" disabled={!name.trim() || !changed || busy}>
              Save
            </Button>
          </div>
        </form>
      </Card>
      <ShareSettings d={d} brand={brand} onChanged={onShared} />
    </div>
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
    <Card title="Share by link" description="Anyone with the link sees this dashboard read-only, every tile live, and nothing else. Add a password so the link alone isn't enough.">
      <div className="space-y-3 text-[13px]">
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
    </Card>
  );
}
