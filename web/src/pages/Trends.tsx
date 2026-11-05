import { useCallback, useEffect, useRef, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, Empty, ErrorText, field, Loading, PageHeader, toast } from "../ui";
import { PickBrand } from "@/components/pick-brand";
import { useBrand } from "@/lib/brand";
import { nameItem } from "@/lib/records";
import { ago } from "../format";
import { day, label, opLabels, presetLabels, today, whole } from "./Funnels";
import { takeDraft } from "@/components/insight-panels";
import { track } from "@/lib/analytics";

// Trends (docs/insights.md): series over time from events, page views,
// visits, conversions, payments and the ad networks' numbers, with a
// breakdown, a formula between series and the period before. A number
// tile is a trend shown by its totals.

export type TrendSpec = Schemas["TrendSpec"];
type Series = Schemas["TrendSeries"];
type Filter = Schemas["InsightFilter"];
export type TrendResult = Schemas["TrendResult"];
type SeriesResult = Schemas["TrendSeriesResult"];
type Catalog = Schemas["InsightsCatalog"];
type Insight = Schemas["Insight"];
type Kind = Series["kind"];
type Preset = NonNullable<Schemas["InsightDateRange"]["preset"]>;
type Display = NonNullable<TrendSpec["display"]>;
export type Annotation = Schemas["Annotation"];

const kindLabels: Record<Kind, string> = {
  event: "An event",
  page: "Page views",
  visit: "Website visits",
  conversion: "A conversion",
  payment: "Payments",
  ad: "Ad numbers",
};

const measureLabels: Record<string, string> = {
  count: "How many",
  people: "People",
  sum: "Total of",
  average: "Average of",
  clicks: "Clicks",
  impressions: "Impressions",
  spend: "Spend",
  conversions: "The network's conversions",
};

const starter: TrendSpec = {
  date_range: { preset: "last_30_days" },
  series: [{ kind: "visit" }, { kind: "event", event: "signed_up" }],
  display: "line",
};

export function trendReady(s: TrendSpec): boolean {
  return (
    s.series.length >= 1 &&
    s.series.every((x) => (x.kind !== "event" || !!x.event) && (x.kind !== "conversion" || !!x.conversion)) &&
    s.series.every((x) => (x.measure !== "sum" && x.measure !== "average") || x.kind === "payment" || !!x.property) &&
    s.series.every((x) => (x.filters ?? []).every((f) => f.property && (f.op === "exists" || f.op === "not_exists" || (f.value !== undefined && f.value !== ""))))
  );
}

export function cleanTrend(s: TrendSpec): TrendSpec {
  const out: TrendSpec = {
    ...s,
    series: s.series.map((x) => {
      const o: Series = { ...x };
      if (!o.filters?.length) delete o.filters;
      if (!o.label) delete o.label;
      if (!o.measure) delete o.measure;
      if (!o.property) delete o.property;
      if (!o.network) delete o.network;
      if (!o.campaign) delete o.campaign;
      if (o.kind === "payment" && (o.measure === "sum" || o.measure === "average")) o.property = "amount";
      return o;
    }),
  };
  if (!out.formula?.trim()) delete out.formula;
  if (!out.breakdown) delete out.breakdown;
  if (!out.interval) delete out.interval;
  if (!out.compare) delete out.compare;
  return out;
}

export function Trends({ ws, canManage }: { ws: string; canManage: boolean }) {
  const { brand, active } = useBrand(ws);
  useEffect(() => nameItem("trends", "Trends"), []);
  return (
    <div className="space-y-6">
      <PageHeader
        title="Trends"
        description="Any number over time: visits, events, revenue, ad spend, or a formula between them such as cost per sign-up. Save one as a chart or a number for a dashboard."
      />
      {brand ? <TrendsFor key={brand.id} brand={brand.id} canManage={canManage} /> : <PickBrand what="Trends" brands={active.length} />}
    </div>
  );
}

function TrendsFor({ brand, canManage }: { brand: string; canManage: boolean }) {
  const [catalog, setCatalog] = useState<Catalog | null>(null);
  const [saved, setSaved] = useState<Insight[]>([]);
  const [current, setCurrent] = useState<Insight | null>(null);
  const [spec, setSpec] = useState<TrendSpec>(() => takeDraft<TrendSpec>("trend") ?? starter);
  const [result, setResult] = useState<TrendResult | null>(null);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState("");
  const [name, setName] = useState("");
  const [notes, setNotes] = useState<Annotation[]>([]);
  const run = useRef(0);

  const loadSaved = useCallback(async () => {
    const { data } = await api.GET("/v1/brands/{brand}/insights", { params: { path: { brand }, query: { kind: "trend" } } });
    if (data) setSaved(data.data);
  }, [brand]);

  // The brand's notes over the chart's dates.
  const from = result?.from.slice(0, 10), to = result ? new Date(new Date(result.to).getTime() - 1).toISOString().slice(0, 10) : undefined;
  const loadNotes = useCallback(async () => {
    if (!from || !to) return;
    const { data } = await api.GET("/v1/brands/{brand}/annotations", { params: { path: { brand }, query: { from, to } } });
    if (data) setNotes(data.data);
  }, [brand, from, to]);
  useEffect(() => {
    loadNotes();
  }, [loadNotes]);

  useEffect(() => {
    api.GET("/v1/brands/{brand}/insights/catalog", { params: { path: { brand } } }).then(({ data, error }) => (data ? setCatalog(data) : setError(errorMessage(error))));
    loadSaved();
  }, [brand, loadSaved]);

  useEffect(() => {
    if (!trendReady(spec)) return;
    const n = ++run.current;
    const t = setTimeout(async () => {
      setRunning(true);
      const { data, error } = await api.POST("/v1/brands/{brand}/insights/trend", { params: { path: { brand } }, body: cleanTrend(spec) });
      if (n !== run.current) return;
      setRunning(false);
      if (!data) return setError(errorMessage(error));
      setError("");
      setResult(data);
    }, 400);
    return () => clearTimeout(t);
  }, [brand, spec]);

  const setSeries = (i: number, x: Series) => setSpec((s) => ({ ...s, series: s.series.map((y, j) => (j === i ? x : y)) }));
  const removeSeries = (i: number) => setSpec((s) => ({ ...s, series: s.series.filter((_, j) => j !== i) }));

  function open(i: Insight) {
    setCurrent(i);
    setName(i.name);
    setSpec(i.spec as TrendSpec);
  }

  async function save() {
    const body = cleanTrend(spec);
    if (current) {
      const { data, error } = await api.PATCH("/v1/brands/{brand}/insights/{insight}", { params: { path: { brand, insight: current.id } }, body: { name, spec: body } });
      if (!data) return toast(errorMessage(error), "error");
      setCurrent(data);
    } else {
      const { data, error } = await api.POST("/v1/brands/{brand}/insights", { params: { path: { brand } }, body: { kind: "trend", name, spec: body } });
      if (!data || !("id" in data)) return toast(errorMessage(error), "error");
      setCurrent(data);
    }
    toast("Saved");
    loadSaved();
  }

  async function remove() {
    if (!current || !(await confirmAction(`Delete ${current.name}?`, "The trend goes for everyone, and off any dashboard it is on. The data stays.", "Delete"))) return;
    const { error } = await api.DELETE("/v1/brands/{brand}/insights/{insight}", { params: { path: { brand, insight: current.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Deleted");
    setCurrent(null);
    setName("");
    loadSaved();
  }

  if (!catalog) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  const display = spec.display ?? "line";

  return (
    <div className="grid gap-6 xl:grid-cols-[380px_minmax(0,1fr)]">
      <div className="min-w-0 space-y-4">
        <Card
          title="Saved trends"
          action={current ? <Button variant="ghost" onClick={() => { setCurrent(null); setName(""); setSpec(starter); }}>New trend</Button> : undefined}
        >
          {saved.length === 0 ? (
            <p className="text-[13px] text-muted-foreground">None yet. Build one below and save it to put it on a dashboard.</p>
          ) : (
            <ul className="-mx-1 space-y-0.5">
              {saved.map((i) => (
                <li key={i.id}>
                  <button
                    onClick={() => open(i)}
                    className={`flex w-full items-center justify-between gap-2 rounded px-2 py-1.5 text-left text-[13px] hover:bg-accent ${current?.id === i.id ? "bg-accent font-medium" : ""}`}
                  >
                    <span className="truncate">{i.name}</span>
                    <span className="shrink-0 font-mono text-[11px] text-muted-foreground">{ago(i.updated_at)}</span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </Card>

        <Card title="Series" description="Each line on the chart. A, B, C in a formula.">
          <ol className="space-y-3">
            {spec.series.map((x, i) => (
              <SeriesEditor key={i} n={i} series={x} catalog={catalog} onChange={(y) => setSeries(i, y)} onRemove={spec.series.length > 1 ? () => removeSeries(i) : undefined} />
            ))}
          </ol>
          {spec.series.length < 6 && (
            <Button variant="ghost" className="mt-3 w-full" onClick={() => setSpec((s) => ({ ...s, series: [...s.series, { kind: "event", event: "" }] }))}>Add a series</Button>
          )}
        </Card>

        <Card title="Options">
          <TrendOptions spec={spec} catalog={catalog} onChange={setSpec} />
        </Card>

        {canManage && (
          <Card title={current ? "Save changes" : "Save this trend"}>
            <div className="flex gap-2">
              <input id="trend-name" aria-label="Name" className={`${field} min-w-0 flex-1`} placeholder="Cost per sign-up" value={name} onChange={(e) => setName(e.target.value)} maxLength={120} />
              <Button disabled={!name.trim() || !trendReady(spec)} onClick={save}>Save</Button>
            </div>
            {current && <button className="mt-2 text-[12px] text-muted-foreground hover:text-destructive" onClick={remove}>Delete this trend</button>}
          </Card>
        )}
        {current && <InsightAlerts key={current.id} brand={brand} insight={current} canManage={canManage} />}
      </div>

      <div className="min-w-0 space-y-4">
        {error && <ErrorText>{error}</ErrorText>}
        {!trendReady(spec) && <Empty title="Finish the series">Each event series needs an event, each total or average a property, and each filter a value.</Empty>}
        {trendReady(spec) && !result && <Loading />}
        {trendReady(spec) && result && (
          <>
            <Card
              title={<span className="flex items-center gap-2">{spec.formula ? `${spec.formula} and its series` : "Over time"}{running && <Badge>updating</Badge>}</span>}
              description={`${day(result.from)} to ${day(new Date(new Date(result.to).getTime() - 1).toISOString())}${result.previous ? ", against the period before" : ""}.`}
            >
              <TrendView result={result} display={display} annotations={notes} />
            </Card>
            <Notes brand={brand} notes={notes} from={from!} to={to!} canManage={canManage} onChange={loadNotes} />
            {spec.breakdown && result.breakdown.length > 0 && <TrendBreakdown result={result} breakdown={spec.breakdown} />}
          </>
        )}
      </div>
    </div>
  );
}

function SeriesEditor({ n, series: x, catalog, onChange, onRemove }: { n: number; series: Series; catalog: Catalog; onChange: (s: Series) => void; onRemove?: () => void }) {
  const id = `series-${n}`;
  const fields = x.kind === "visit" ? catalog.fields.visit : x.kind === "payment" ? catalog.fields.payment : catalog.fields.event;
  const filters = x.filters ?? [];
  const setFilters = (f: Filter[]) => onChange({ ...x, filters: f });
  const measures = x.kind === "ad" ? catalog.ad_metrics : x.kind === "visit" ? ["count", "people"] : catalog.measures;
  return (
    <li className="rounded-md border bg-background p-3">
      <div className="flex items-center gap-2">
        <span className="font-mono text-[11px] font-medium text-muted-foreground">{String.fromCharCode(65 + n)}</span>
        <select id={`${id}-kind`} aria-label={`Series ${n + 1} kind`} className={`${field} min-w-0 flex-1`} value={x.kind} onChange={(e) => onChange({ kind: e.target.value as Kind, label: x.label })}>
          {(Object.keys(kindLabels) as Kind[]).map((k) => (
            <option key={k} value={k}>{kindLabels[k]}</option>
          ))}
        </select>
        {onRemove && <button aria-label={`Remove series ${n + 1}`} className="px-1 text-muted-foreground hover:text-foreground" onClick={onRemove}>×</button>}
      </div>
      {x.kind === "event" && (
        <>
          <input id={`${id}-event`} aria-label="Event" list={`${id}-events`} className={`${field} mt-2 w-full font-mono`} placeholder="signed_up" value={x.event ?? ""} onChange={(e) => onChange({ ...x, event: e.target.value.trim() })} />
          <datalist id={`${id}-events`}>
            {catalog.events.map((e) => (
              <option key={e.name} value={e.name}>{`${whole(e.count)} in 90 days`}</option>
            ))}
          </datalist>
        </>
      )}
      {x.kind === "conversion" && (
        catalog.conversions.length === 0 ? (
          <p className="mt-2 text-[12px] text-muted-foreground">No saved conversions yet. Add them under Paid ads, Conversions.</p>
        ) : (
          <select aria-label="Conversion" className={`${field} mt-2 w-full`} value={x.conversion ?? ""} onChange={(e) => onChange({ ...x, conversion: e.target.value })}>
            <option value="">Pick a conversion</option>
            {catalog.conversions.map((c) => (
              <option key={c.key} value={c.key}>{c.name}</option>
            ))}
          </select>
        )
      )}
      <div className="mt-2 grid grid-cols-2 gap-1.5">
        <select aria-label="Measure" className={`${field} min-w-0`} value={x.measure ?? (x.kind === "ad" ? "clicks" : "count")} onChange={(e) => onChange({ ...x, measure: e.target.value as Series["measure"], property: undefined })}>
          {measures.map((m) => (
            <option key={m} value={m}>{measureLabels[m] ?? m}</option>
          ))}
        </select>
        {(x.measure === "sum" || x.measure === "average") && x.kind !== "payment" && (
          <input aria-label="Property" className={`${field} min-w-0 font-mono`} placeholder="property, such as seats" value={x.property ?? ""} onChange={(e) => onChange({ ...x, property: e.target.value.trim() })} />
        )}
        {(x.measure === "sum" || x.measure === "average") && x.kind === "payment" && <span className="self-center text-[12px] text-muted-foreground">the amount, in the brand's currency</span>}
        {x.kind === "ad" && (
          <select aria-label="Ad network" className={`${field} min-w-0`} value={x.network ?? ""} onChange={(e) => onChange({ ...x, network: e.target.value || undefined })}>
            <option value="">Every network</option>
            {catalog.networks.filter((nw) => nw.connected).map((nw) => (
              <option key={nw.id} value={nw.id}>{nw.label}</option>
            ))}
          </select>
        )}
      </div>
      {x.kind === "ad" && (
        <input aria-label="Campaign name contains" className={`${field} mt-1.5 w-full`} placeholder="Campaign name contains" value={x.campaign ?? ""} onChange={(e) => onChange({ ...x, campaign: e.target.value || undefined })} />
      )}
      {(x.kind === "event" || x.kind === "payment" || x.kind === "conversion") && (
        <label className="mt-2 flex items-center gap-2 text-[13px]">
          <input type="checkbox" checked={!!x.first} onChange={(e) => onChange({ ...x, first: e.target.checked || undefined })} />
          {x.kind === "payment" ? "Only first payments" : "Only the first time each person did it"}
        </label>
      )}
      {x.kind !== "ad" && (
        <>
          {filters.map((f, j) => (
            <div key={j} className="mt-2 grid grid-cols-[minmax(0,1fr)_auto] gap-1.5">
              <div className="grid min-w-0 grid-cols-2 gap-1.5">
                {x.kind === "visit" || x.kind === "payment" ? (
                  <select aria-label="Field" className={`${field} min-w-0`} value={f.property} onChange={(e) => setFilters(filters.map((y, k) => (k === j ? { ...y, property: e.target.value, value: "" } : y)))}>
                    {fields.map((fl) => (
                      <option key={fl} value={fl}>{label(fl)}</option>
                    ))}
                  </select>
                ) : (
                  <input aria-label="Field" list={`${id}-fields`} className={`${field} min-w-0`} value={f.property} placeholder="Field, such as path or plan" onChange={(e) => setFilters(filters.map((y, k) => (k === j ? { ...y, property: e.target.value.trim() } : y)))} />
                )}
                <select aria-label="Comparison" className={`${field} min-w-0`} value={f.op} onChange={(e) => setFilters(filters.map((y, k) => (k === j ? { ...y, op: e.target.value as Filter["op"] } : y)))}>
                  {(Object.keys(opLabels) as Filter["op"][]).map((o) => (
                    <option key={o} value={o}>{opLabels[o]}</option>
                  ))}
                </select>
                {f.op !== "exists" && f.op !== "not_exists" && (
                  f.property === "network" && x.kind === "visit" ? (
                    <select aria-label="Value" className={`${field} col-span-2 min-w-0`} value={String(f.value ?? "")} onChange={(e) => setFilters(filters.map((y, k) => (k === j ? { ...y, value: e.target.value } : y)))}>
                      <option value="">Pick a network</option>
                      {catalog.networks.map((nw) => (
                        <option key={nw.id} value={nw.id}>{nw.label}{nw.connected ? "" : " (not connected)"}</option>
                      ))}
                    </select>
                  ) : (
                    <input
                      aria-label="Value"
                      className={`${field} col-span-2 min-w-0`}
                      placeholder={f.op === "in" ? "Values, separated by commas" : "Value"}
                      value={Array.isArray(f.value) ? f.value.join(", ") : String(f.value ?? "")}
                      onChange={(e) => {
                        const v = e.target.value;
                        const value = f.op === "in" ? v.split(",").map((z) => z.trim()).filter(Boolean) : ["gt", "gte", "lt", "lte"].includes(f.op) && v !== "" && !isNaN(Number(v)) ? Number(v) : v;
                        setFilters(filters.map((y, k) => (k === j ? { ...y, value } : y)));
                      }}
                    />
                  )
                )}
              </div>
              <button aria-label="Remove filter" className="self-start px-1 pt-1 text-muted-foreground hover:text-foreground" onClick={() => setFilters(filters.filter((_, k) => k !== j))}>×</button>
            </div>
          ))}
          <datalist id={`${id}-fields`}>
            {fields.map((f) => (
              <option key={f} value={f}>{label(f)}</option>
            ))}
          </datalist>
          <button className="mt-2 text-[12px] text-muted-foreground hover:text-foreground" onClick={() => setFilters([...filters, { property: x.kind === "visit" ? "network" : x.kind === "payment" ? "product" : "path", op: "eq", value: "" }])}>
            + Filter
          </button>
        </>
      )}
      <input aria-label="Series name" className={`${field} mt-2 w-full`} placeholder="Name on the chart (optional)" value={x.label ?? ""} maxLength={80} onChange={(e) => onChange({ ...x, label: e.target.value || undefined })} />
    </li>
  );
}

function TrendOptions({ spec, catalog, onChange }: { spec: TrendSpec; catalog: Catalog; onChange: (s: TrendSpec) => void }) {
  const preset = spec.date_range?.preset;
  // Breakdowns the first series' kind allows; the others must share them.
  const kinds = [...new Set(spec.series.map((s) => s.kind))];
  const breakdowns = kinds.length === 1 ? catalog.breakdowns[kinds[0]] ?? [] : kinds.includes("ad") ? [] : (catalog.breakdowns[kinds[0]] ?? []).filter((b) => kinds.every((k) => (catalog.breakdowns[k] ?? []).includes(b)));
  return (
    <div className="space-y-3 text-[13px]">
      <label className="block">
        <span className="text-muted-foreground">Dates</span>
        <select id="trend-dates" className={`${field} mt-1 block w-full`} value={preset ?? "custom"} onChange={(e) => onChange({ ...spec, date_range: e.target.value === "custom" ? { from: spec.date_range?.from ?? today(-29), to: spec.date_range?.to ?? today(0) } : { preset: e.target.value as Preset } })}>
          {(Object.keys(presetLabels) as Preset[]).map((p) => (
            <option key={p} value={p}>{presetLabels[p]}</option>
          ))}
          <option value="custom">Pick dates</option>
        </select>
      </label>
      {!preset && (
        <div className="grid grid-cols-2 gap-2">
          <input aria-label="From" type="date" className={field} value={spec.date_range?.from ?? ""} onChange={(e) => onChange({ ...spec, date_range: { ...spec.date_range, from: e.target.value } })} />
          <input aria-label="To" type="date" className={field} value={spec.date_range?.to ?? ""} onChange={(e) => onChange({ ...spec, date_range: { ...spec.date_range, to: e.target.value } })} />
        </div>
      )}
      <div className="grid grid-cols-2 gap-2">
        <label className="block">
          <span className="text-muted-foreground">By</span>
          <select id="trend-interval" className={`${field} mt-1 block w-full`} value={spec.interval ?? ""} onChange={(e) => onChange({ ...spec, interval: (e.target.value || undefined) as TrendSpec["interval"] })}>
            <option value="">Automatic</option>
            <option value="day">Day</option>
            <option value="week">Week</option>
            <option value="month">Month</option>
          </select>
        </label>
        <label className="block">
          <span className="text-muted-foreground">Show as</span>
          <select id="trend-display" className={`${field} mt-1 block w-full`} value={spec.display ?? "line"} onChange={(e) => onChange({ ...spec, display: e.target.value as Display })}>
            <option value="line">Lines</option>
            <option value="bar">Bars</option>
            <option value="number">A number</option>
            <option value="table">A table</option>
          </select>
        </label>
      </div>
      <label className="block">
        <span className="text-muted-foreground">Break down by</span>
        <select id="trend-breakdown" className={`${field} mt-1 block w-full`} value={spec.breakdown ?? ""} onChange={(e) => onChange({ ...spec, breakdown: e.target.value || undefined })}>
          <option value="">Nothing</option>
          {breakdowns.map((b) => (
            <option key={b} value={b}>{label(b)}</option>
          ))}
          {!kinds.includes("ad") && catalog.experiments.length > 0 && (
            <optgroup label="A/B test version">
              {catalog.experiments.map((x) => (
                <option key={x.field} value={x.field}>{x.name}</option>
              ))}
            </optgroup>
          )}
          {spec.breakdown?.startsWith("experiment:") && !catalog.experiments.some((x) => x.field === spec.breakdown) && <option value={spec.breakdown}>Test {spec.breakdown.slice(11)}</option>}
        </select>
        {breakdowns.length === 0 && kinds.length > 1 && <span className="mt-1 block text-[12px] text-muted-foreground">Series of different kinds share no field to split by.</span>}
      </label>
      <label className="block">
        <span className="text-muted-foreground">Formula</span>
        <input id="trend-formula" className={`${field} mt-1 block w-full font-mono`} placeholder="A / B" value={spec.formula ?? ""} maxLength={200} onChange={(e) => onChange({ ...spec, formula: e.target.value || undefined })} />
        <span className="mt-1 block text-[12px] text-muted-foreground">A, B, C are the series, with + − × ÷ and brackets. Spend ÷ sign-ups is cost per sign-up.</span>
      </label>
      <label className="flex items-center gap-2">
        <input type="checkbox" checked={!!spec.compare} onChange={(e) => onChange({ ...spec, compare: e.target.checked || undefined })} />
        Compare with the period before
      </label>
    </div>
  );
}

// Series colours, from the chart palette in order.
const palette = ["var(--color-primary)", "#2f5fd0", "#8a4fd6", "#0f8a7a", "#c2410c", "#8a6100", "#5a6375"];

export const fmt = (v: number | null | undefined, currency?: string): string => {
  if (v === null || v === undefined) return "–";
  const n = Math.abs(v) >= 1000 ? Math.round(v).toLocaleString() : Number.isInteger(v) ? v.toLocaleString() : v.toFixed(Math.abs(v) < 10 ? 2 : 1);
  if (!currency || currency === "brand" || currency === "mixed") return n;
  return `${n} ${currency}`;
};

function change(now: number | null | undefined, before: number | null | undefined): string | null {
  // Too few before to compare with, such as the first days of tracking.
  if (now === null || now === undefined || before === null || before === undefined || Math.abs(before) < 5) return null;
  const d = (now - before) / Math.abs(before);
  return `${d >= 0 ? "+" : "−"}${Math.abs(d * 100).toFixed(Math.abs(d) < 0.1 ? 1 : 0)}%`;
}

// TrendView draws a trend result as lines, bars, numbers or a table.
export function TrendView({ result, display, compact, annotations }: { result: TrendResult; display: Display; compact?: boolean; annotations?: Annotation[] }) {
  const all = result.formula ? [...result.series, result.formula] : result.series;
  const prev = result.previous ? (result.previous.formula ? [...result.previous.series, result.previous.formula] : result.previous.series) : null;
  if (display === "number") {
    const shown = result.formula ? [result.formula] : result.series;
    const shownPrev = result.previous ? (result.previous.formula ? [result.previous.formula] : result.previous.series) : null;
    return (
      <div className={`grid gap-4 ${shown.length > 2 ? "grid-cols-2 sm:grid-cols-4" : shown.length > 1 ? "grid-cols-2" : ""}`}>
        {shown.map((s, i) => {
          const d = shownPrev ? change(s.total, shownPrev[i]?.total) : null;
          return (
            <div key={i} className="min-w-0">
              {!(compact && shown.length === 1) && <div className="truncate text-[12px] text-muted-foreground" title={s.label}>{s.label}</div>}
              <div className={`font-semibold tabular-nums ${compact ? "text-2xl" : "text-3xl"}`}>{fmt(s.total, s.currency)}</div>
              {d && <div className={`text-[12px] ${d.startsWith("+") ? "text-green-700 dark:text-green-400" : "text-red-600 dark:text-red-400"}`}>{d} on the period before</div>}
            </div>
          );
        })}
      </div>
    );
  }
  if (display === "table") {
    return (
      <div className="overflow-x-auto">
        <table className="w-full text-[13px]">
          <thead>
            <tr className="border-b text-left font-mono text-[11px] uppercase text-muted-foreground">
              <th className="py-1.5 pr-3 font-medium">Period</th>
              {all.map((s, i) => (
                <th key={i} className="px-2 py-1.5 text-right font-medium" title={s.label}>{s.label}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {(all[0]?.points ?? []).map((p, j) => (
              <tr key={p.start} className="border-b last:border-0">
                <td className="py-1.5 pr-3 font-mono text-muted-foreground">{p.start.slice(0, 10)}</td>
                {all.map((s, i) => (
                  <td key={i} className="px-2 py-1.5 text-right tabular-nums">{fmt(s.points[j]?.value, s.currency)}</td>
                ))}
              </tr>
            ))}
            <tr className="font-medium">
              <td className="py-1.5 pr-3">Total</td>
              {all.map((s, i) => (
                <td key={i} className="px-2 py-1.5 text-right tabular-nums">{fmt(s.total, s.currency)}</td>
              ))}
            </tr>
          </tbody>
        </table>
      </div>
    );
  }
  return (
    <div>
      <Lines series={all} previous={prev} bars={display === "bar"} compact={compact} annotations={annotations} />
      <ul className={`mt-3 flex flex-wrap gap-x-4 gap-y-1 text-[12px] ${compact ? "" : ""}`}>
        {all.map((s, i) => (
          <li key={i} className="flex items-center gap-1.5">
            <span className="inline-block h-2.5 w-2.5 rounded-sm" style={{ background: palette[i % palette.length] }} />
            <span className="truncate" title={s.label}>{s.label}</span>
            <span className="font-medium tabular-nums">{fmt(s.total, s.currency)}</span>
            {prev && change(s.total, prev[i]?.total) && <span className="text-muted-foreground">{change(s.total, prev[i]?.total)}</span>}
          </li>
        ))}
      </ul>
    </div>
  );
}

// Lines is an SVG chart: one polyline per series, or grouped bars, with
// the period before as a faint line.
function Lines({ series, previous, bars, compact, annotations = [] }: { series: SeriesResult[]; previous: SeriesResult[] | null; bars: boolean; compact?: boolean; annotations?: Annotation[] }) {
  const [hover, setHover] = useState<number | null>(null);
  const n = series[0]?.points.length ?? 0;
  if (n === 0) return <Empty title="Nothing in these dates" />;
  // Each note sits on the period its day falls in.
  const starts = series[0].points.map((p) => p.start.slice(0, 10));
  const periodOf = (d: string) => { let j = -1; starts.forEach((s, k) => { if (s <= d) j = k; }); return j; };
  const notesAt = (j: number) => annotations.filter((a) => periodOf(a.day) === j);
  const marked = [...new Set(annotations.map((a) => periodOf(a.day)).filter((j) => j >= 0))];
  const W = 640, H = compact ? 140 : 220, padL = 40, padB = 20, padT = 8;
  const values = series.flatMap((s) => s.points.map((p) => p.value ?? 0)).concat(previous ? previous.flatMap((s) => s.points.map((p) => p.value ?? 0)) : []);
  const max = Math.max(1e-9, ...values);
  const min = Math.min(0, ...values);
  const x = (j: number) => padL + ((W - padL) * (bars ? j + 0.5 : j)) / (bars ? n : Math.max(1, n - 1));
  const y = (v: number) => padT + (H - padT - padB) * (1 - (v - min) / (max - min || 1));
  const ticks = [max, (max + min) / 2, min];
  const labelEvery = Math.ceil(n / (compact ? 4 : 8));
  const points = series[0].points;
  return (
    <div className="relative">
      <svg viewBox={`0 0 ${W} ${H}`} className="w-full" role="img" aria-label="Trend chart" onMouseLeave={() => setHover(null)}>
        {ticks.map((t, i) => (
          <g key={i}>
            <line x1={padL} x2={W} y1={y(t)} y2={y(t)} stroke="currentColor" strokeOpacity={0.12} />
            <text x={padL - 6} y={y(t) + 3} textAnchor="end" fontSize={10} fill="currentColor" fillOpacity={0.6}>{fmt(t)}</text>
          </g>
        ))}
        {points.map((p, j) => (
          j % labelEvery === 0 && <text key={j} x={x(j)} y={H - 4} textAnchor="middle" fontSize={10} fill="currentColor" fillOpacity={0.6}>{p.start.slice(5, 10)}</text>
        ))}
        {bars
          ? series.map((s, i) => {
              const bw = ((W - padL) / n) * 0.8 / series.length;
              return s.points.map((p, j) => (
                <rect key={`${i}-${j}`} x={x(j) - (bw * series.length) / 2 + i * bw} y={y(Math.max(0, p.value ?? 0))} width={Math.max(1, bw - 1)} height={Math.abs(y(p.value ?? 0) - y(0))} fill={palette[i % palette.length]} opacity={hover === null || hover === j ? 1 : 0.5} />
              ));
            })
          : (
            <>
              {previous?.map((s, i) => (
                <polyline key={`p${i}`} fill="none" stroke={palette[i % palette.length]} strokeOpacity={0.35} strokeDasharray="3 3" strokeWidth={1.5} points={s.points.map((p, j) => `${x(j)},${y(p.value ?? 0)}`).join(" ")} />
              ))}
              {series.map((s, i) => (
                <polyline key={i} fill="none" stroke={palette[i % palette.length]} strokeWidth={2} strokeLinejoin="round" points={s.points.map((p, j) => `${x(j)},${y(p.value ?? 0)}`).join(" ")} />
              ))}
            </>
          )}
        {marked.map((j) => (
          <g key={`a${j}`}>
            <line x1={x(j)} x2={x(j)} y1={padT} y2={H - padB} stroke="currentColor" strokeOpacity={0.35} strokeDasharray="2 3" />
            <circle cx={x(j)} cy={H - padB} r={3.5} fill="var(--color-primary)" />
          </g>
        ))}
        {hover !== null && !bars && <line x1={x(hover)} x2={x(hover)} y1={padT} y2={H - padB} stroke="currentColor" strokeOpacity={0.3} />}
        {points.map((_, j) => (
          <rect key={j} x={x(j) - (W - padL) / n / 2} y={0} width={(W - padL) / n} height={H} fill="transparent" onMouseEnter={() => setHover(j)} />
        ))}
      </svg>
      {hover !== null && (
        <div className="pointer-events-none absolute left-1/2 top-1 -translate-x-1/2 rounded border bg-popover px-2 py-1 text-[12px] shadow">
          <div className="font-mono text-muted-foreground">{points[hover].start.slice(0, 10)}</div>
          {series.map((s, i) => (
            <div key={i} className="flex items-center gap-1.5">
              <span className="inline-block h-2 w-2 rounded-sm" style={{ background: palette[i % palette.length] }} />
              <span className="truncate">{s.label}</span>
              <span className="ml-auto pl-3 font-medium tabular-nums">{fmt(s.points[hover]?.value, s.currency)}</span>
              {previous && <span className="text-muted-foreground">{fmt(previous[i]?.points[hover]?.value)}</span>}
            </div>
          ))}
          {notesAt(hover).map((a) => (
            <div key={a.id} className="mt-1 max-w-[260px] border-t pt-1 text-muted-foreground">
              <span className="font-mono">{a.day.slice(5)}</span> {a.text}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function TrendBreakdown({ result, breakdown }: { result: TrendResult; breakdown: string }) {
  const cols = result.breakdown[0]?.series ?? [];
  return (
    <Card title={`By ${label(breakdown).toLowerCase()}`} description="Totals over the dates, the biggest first.">
      <div className="overflow-x-auto">
        <table className="w-full text-[13px]">
          <thead>
            <tr className="border-b text-left font-mono text-[11px] uppercase text-muted-foreground">
              <th className="py-1.5 pr-3 font-medium">{label(breakdown)}</th>
              {cols.map((s, i) => (
                <th key={i} className="px-2 py-1.5 text-right font-medium" title={s.label}>{s.label}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {result.breakdown.map((g) => (
              <tr key={g.value} className="border-b last:border-0">
                <td className="max-w-[260px] truncate py-1.5 pr-3" title={g.value}>{g.value === "$other" ? <em>Everything else</em> : g.value || <span className="text-muted-foreground">(none)</span>}</td>
                {g.series.map((s, i) => (
                  <td key={i} className="px-2 py-1.5 text-right tabular-nums">{fmt(s.total, s.currency)}</td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

type InsightAlert = Schemas["InsightAlert"];

const conditionLabels: Record<InsightAlert["condition"], string> = { above: "goes above", below: "goes below", rises_by: "rises by more than", falls_by: "falls by more than" };

export function describeAlert(a: InsightAlert): string {
  const pct = a.condition === "rises_by" || a.condition === "falls_by" ? "%" : "";
  return `${conditionLabels[a.condition]} ${a.threshold}${pct}`;
}

// InsightAlerts are the lines on a saved trend's number: checked every
// hour, each raises an alert once when the number crosses it.
function InsightAlerts({ brand, insight, canManage }: { brand: string; insight: Insight; canManage: boolean }) {
  const [list, setList] = useState<InsightAlert[] | null>(null);
  const [condition, setCondition] = useState<InsightAlert["condition"]>("above");
  const [threshold, setThreshold] = useState("");
  const [busy, setBusy] = useState(false);
  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/brands/{brand}/insight-alerts", { params: { path: { brand }, query: { insight_id: insight.id } } });
    setList(data?.data ?? []);
  }, [brand, insight.id]);
  useEffect(() => {
    load();
  }, [load]);

  async function add() {
    setBusy(true);
    const { data, error } = await api.POST("/v1/brands/{brand}/insight-alerts", { params: { path: { brand } }, body: { insight_id: insight.id, condition, threshold: Number(threshold) } });
    setBusy(false);
    if (!data) return toast(errorMessage(error), "error");
    setThreshold("");
    toast("Alert set; it is checked every hour");
    load();
  }
  async function remove(a: InsightAlert) {
    const { error, response } = await api.DELETE("/v1/brands/{brand}/insight-alerts/{alert}", { params: { path: { brand, alert: a.id } } });
    if (error || !response.ok) return toast(errorMessage(error), "error");
    load();
  }
  const pct = condition === "rises_by" || condition === "falls_by";
  return (
    <Card title="Alert on this number" description="Checked every hour. The number is the formula's total, else the first series', over the trend's own dates. Who hears about it is set under Alerts.">
      {list === null ? (
        <Loading />
      ) : (
        <ul className="space-y-1 text-[13px]">
          {list.length === 0 && <li className="text-muted-foreground">No alerts yet.</li>}
          {list.map((a) => (
            <li key={a.id} className="flex items-center justify-between gap-2">
              <span>
                When it {describeAlert(a)}
                <span className="ml-2 text-[12px] text-muted-foreground">
                  {a.state === "firing" ? "over the line now" : a.state === "ok" ? "fine" : a.last_checked_at ? "no number to compare yet" : "not checked yet"}
                  {a.last_value !== null && a.last_value !== undefined ? ` · now ${Math.round(a.last_value * 100) / 100}` : ""}
                </span>
              </span>
              {canManage && (
                <button className="text-[12px] text-muted-foreground hover:text-destructive" onClick={() => remove(a)}>
                  Remove
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
      {canManage && (
        <div className="mt-3 flex flex-wrap items-center gap-2 text-[13px]">
          <span>When it</span>
          <select className={field} value={condition} onChange={(e) => setCondition(e.target.value as InsightAlert["condition"])} aria-label="Condition">
            {(Object.keys(conditionLabels) as InsightAlert["condition"][]).map((c) => (
              <option key={c} value={c}>
                {conditionLabels[c]}
              </option>
            ))}
          </select>
          <input className={`${field} w-28`} type="number" step="any" min={pct ? 0 : undefined} value={threshold} onChange={(e) => setThreshold(e.target.value)} aria-label="Threshold" placeholder={pct ? "20" : "100"} />
          {pct && <span>%</span>}
          <Button variant="ghost" disabled={busy || threshold === "" || Number.isNaN(Number(threshold))} onClick={add}>
            Add alert
          </Button>
        </div>
      )}
    </Card>
  );
}

// Notes are the brand's annotations over the chart's dates: a reason
// beside every jump or dip, on every chart of the brand.
function Notes({ brand, notes, from, to, canManage, onChange }: { brand: string; notes: Annotation[]; from: string; to: string; canManage: boolean; onChange: () => void }) {
  const [adding, setAdding] = useState(false);
  const [day_, setDay] = useState(to);
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  async function add() {
    setBusy(true);
    const { data, error } = await api.POST("/v1/brands/{brand}/annotations", { params: { path: { brand } }, body: { day: day_, text: text.trim() } });
    setBusy(false);
    if (!data || !("id" in data)) return toast(errorMessage(error), "error");
    toast("Note added");
    setText("");
    setAdding(false);
    onChange();
  }
  async function remove(a: Annotation) {
    if (!(await confirmAction("Remove this note?", `"${a.text}" goes from every chart.`, "Remove"))) return;
    const { error } = await api.DELETE("/v1/brands/{brand}/annotations/{annotation}", { params: { path: { brand, annotation: a.id } } });
    if (error) return toast(errorMessage(error), "error");
    onChange();
  }
  return (
    <Card
      title="Notes on these dates"
      description="Mark a day with what happened (a launch, a campaign, an outage). Notes show on every chart of the brand."
      action={canManage && !adding ? <Button variant="ghost" onClick={() => { track("ui.annotation_started"); setAdding(true); }}>Add a note</Button> : undefined}
    >
      {adding && (
        <div className="mb-3 flex flex-wrap gap-2">
          <input aria-label="Day" type="date" className={field} min={from} max={to} value={day_} onChange={(e) => setDay(e.target.value)} />
          <input aria-label="Note" className={`${field} min-w-0 flex-1`} placeholder="New pricing page went live" maxLength={300} value={text} onChange={(e) => setText(e.target.value)} />
          <Button disabled={busy || !text.trim() || !day_} onClick={add}>Add</Button>
          <Button variant="ghost" onClick={() => setAdding(false)}>Cancel</Button>
        </div>
      )}
      {notes.length === 0 ? (
        <p className="text-[13px] text-muted-foreground">None in these dates.</p>
      ) : (
        <ul className="divide-y">
          {notes.map((a) => (
            <li key={a.id} className="flex items-center justify-between gap-3 py-1.5 text-[13px]">
              <span className="min-w-0"><span className="mr-2 font-mono text-[12px] text-muted-foreground">{a.day}</span>{a.text}</span>
              {canManage && <button aria-label="Remove note" className="shrink-0 px-1 text-muted-foreground hover:text-destructive" onClick={() => remove(a)}>×</button>}
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
