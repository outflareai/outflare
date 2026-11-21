import { useCallback, useEffect, useRef, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, Empty, ErrorText, field, Loading, PageHeader, toast } from "../ui";
import { PickBrand } from "@/components/pick-brand";
import { useBrand } from "@/lib/brand";
import { nameItem } from "@/lib/records";
import { day, label, opLabels, presetLabels, today, whole } from "./Funnels";
import { embedSpec, type InsightEmbed } from "@/lib/insight-embed";
import { InsightBar, InsightList, InsightMissing, partName, useInsightEditor, useInsightRoute } from "@/lib/insight-editor";
import { peekDraft, takeDraft } from "@/components/insight-panels";
import { track } from "@/lib/analytics";
import { forecastable, TimeChart } from "@/components/time-chart";
import { HBars, Ring, WorldMap, type Part } from "@/components/share-charts";
import { seriesColor, Swatch } from "@/components/chart-kit";

// Trends (docs/insights.md): series over time from events, page views,
// visits, conversions, payments and the ad networks' numbers, with a
// breakdown, a formula between series and the period before. A number
// tile is a trend shown by its totals.

export type TrendSpec = Schemas["TrendSpec"];
type Series = Schemas["TrendSeries"];
type Filter = Schemas["InsightFilter"];
export type TrendResult = Schemas["TrendResult"];
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
  analytics: "Search and analytics tools",
  backlinks: "Referring domains",
  link_orders: "Link orders",
  search: "Search Console (queries, pages, countries)",
};

// Kinds read from stored tool data: no filters, events or first.
const sourceKinds: Kind[] = ["analytics", "backlinks", "link_orders", "search"];
const isSource = (k: Kind) => sourceKinds.includes(k);
const sourceDefault: Record<string, string> = { analytics: "clicks", backlinks: "referring_domains", link_orders: "spend", search: "clicks" };
const connectorLabels: Record<string, string> = { google_search_console: "Search Console", bing_webmaster: "Bing Webmaster Tools", google_analytics: "Google Analytics" };

const measureLabels: Record<string, string> = {
  count: "How many",
  people: "People",
  sum: "Total of",
  average: "Average of",
  median: "Median of",
  p90: "90th percentile of",
  p99: "99th percentile of",
  min: "Smallest",
  max: "Biggest",
  per_person_average: "Times per person (average)",
  per_person_median: "Times per person (median)",
  per_person_max: "Times per person (most)",
  weekly_active: "Weekly active people",
  monthly_active: "Monthly active people",
  clicks: "Clicks",
  impressions: "Impressions",
  spend: "Spend",
  conversions: "The network's or tool's conversions",
  ctr: "Click-through rate",
  position: "Average position",
  visitors: "Visitors (daily, added up)",
  pageviews: "Page views",
  sessions: "Sessions",
  referring_domains: "Referring domains (live)",
  dofollow_referring_domains: "Dofollow referring domains (live)",
  new_referring_domains: "New referring domains",
  lost_referring_domains: "Lost referring domains",
  committed: "Committed (USD)",
  published: "Published",
  paid: "Paid",
};

const starter: TrendSpec = {
  date_range: { preset: "last_30_days" },
  series: [{ kind: "visit" }, { kind: "event", event: "signed_up" }],
  display: "line",
};

// Measures that read a property's values.
const propertyMeasures = ["sum", "average", "median", "p90", "p99", "min", "max"];
const readsProperty = (m?: string) => !!m && propertyMeasures.includes(m);

// TrendDraw is how a trend is drawn: the spec's display options. A shared
// dashboard only says whether there is a breakdown, not which.
export type TrendDraw = Pick<TrendSpec, "cumulative" | "percent" | "smoothing" | "value_labels" | "right_axis"> & { breakdown?: string | boolean };

export function trendReady(s: TrendSpec): boolean {
  return (
    s.series.length >= 1 &&
    s.series.every((x) => (x.kind !== "event" || !!x.event) && (x.kind !== "conversion" || !!x.conversion)) &&
    s.series.every((x) => !readsProperty(x.measure) || x.kind === "payment" || !!x.property) &&
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
      if (!o.connector) delete o.connector;
      if (o.kind === "payment" && readsProperty(o.measure)) o.property = "amount";
      return o;
    }),
  };
  if (!out.formula?.trim()) delete out.formula;
  if (!out.breakdown) delete out.breakdown;
  if (!out.breakdown || !out.breakdown_2 || out.breakdown_2 === out.breakdown || out.series.some((x) => x.kind === "ad" || isSource(x.kind))) delete out.breakdown_2;
  if (!out.cumulative) delete out.cumulative;
  if (!out.percent || !out.breakdown) delete out.percent;
  if (!out.smoothing || out.smoothing === 1) delete out.smoothing;
  if (!out.value_labels) delete out.value_labels;
  out.right_axis = (out.right_axis ?? []).filter((l) => l.charCodeAt(0) - 65 < out.series.length);
  if (!out.right_axis.length || out.right_axis.length >= out.series.length || out.breakdown) delete out.right_axis;
  if (out.display === "map" && out.breakdown !== "country") out.display = "line";
  if (!out.interval) delete out.interval;
  if (!out.compare) delete out.compare;
  return out;
}

export function Trends({ ws, canManage, item, onOpen }: { ws: string; canManage: boolean; item?: string; onOpen?: (id?: string) => void }) {
  const { brand, active } = useBrand(ws);
  useEffect(() => nameItem("trends", "Trends"), []);
  const route = useInsightRoute(item, onOpen, peekDraft() === "trend");
  return (
    <div className="space-y-6">
      <PageHeader
        title="Trends"
        description="Any number over time: visits, events, revenue, ad spend, or a formula between them such as cost per sign-up. Save one as a chart or a number for a dashboard."
      />
      {!brand ? (
        <PickBrand what="Trends" brands={active.length} />
      ) : route.listing ? (
        <InsightList key={brand.id} brand={brand.id} kind="trend" what="trend" plural="trends" canManage={canManage} onOpen={onOpen} column="Shows" describe={(i) => ((i.spec as TrendSpec).series ?? []).map((x) => partName(x) || "…").join(", ")} empty="A trend is any number over time: visits, events, revenue or ad spend. Build one and save it to open it again or put it on a dashboard." />
      ) : (
        <TrendsFor key={brand.id} brand={brand.id} canManage={canManage} item={route.item} onOpen={onOpen} />
      )}
    </div>
  );
}

export function TrendsFor({ brand, canManage, embed, item, onOpen }: { brand: string; canManage: boolean; embed?: InsightEmbed; item?: string; onOpen?: (id?: string) => void }) {
  const [catalog, setCatalog] = useState<Catalog | null>(null);
  const ed = useInsightEditor<TrendSpec>({ brand, kind: "trend", starter, clean: cleanTrend, embed, item, onOpen, initial: () => takeDraft<TrendSpec>("trend"), embedSpec: () => embedSpec<TrendSpec>(embed!, starter) });
  const { spec, setSpec, current } = ed;
  const [result, setResult] = useState<TrendResult | null>(null);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState("");
  const [notes, setNotes] = useState<Annotation[]>([]);
  const run = useRef(0);

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
  }, [brand]);

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

  if (!catalog || ed.loading) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  if (ed.missing) return <InsightMissing ed={ed} what="trend" />;
  const display = spec.display ?? "line";

  return (
    <div className="grid gap-6 xl:grid-cols-[380px_minmax(0,1fr)]">
      <div className="xl:col-span-2">
        <InsightBar ed={ed} what="trend" all="All trends" canManage={canManage} canSave={trendReady(spec)} placeholder="Sign-ups per week" deleteWarning="The trend goes for everyone, and off any dashboard it is on. The data stays." embed={!!embed} />
      </div>
      <div className="min-w-0 space-y-4">
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

        {current && <InsightAlerts key={current.id} brand={brand} insight={current} canManage={canManage} />}
      </div>

      <div className="min-w-0 space-y-4">
        {error && <ErrorText>{error}</ErrorText>}
        {!trendReady(spec) && <Empty title="Finish the series">Each event series needs an event, each total, average or percentile a property, and each filter a value.</Empty>}
        {trendReady(spec) && !result && <Loading />}
        {trendReady(spec) && result && (
          <>
            <Card
              title={<span className="flex items-center gap-2">{spec.formula ? `${spec.formula} and its series` : "Over time"}{running && <Badge>updating</Badge>}</span>}
              description={`${day(result.from)} to ${day(new Date(new Date(result.to).getTime() - 1).toISOString())}${result.previous ? ", against the period before" : ""}.`}
            >
              <TrendView result={result} display={display} annotations={notes} forecast={spec.forecast !== false} view={cleanTrend(spec)} />
            </Card>
            <Notes brand={brand} notes={notes} from={from!} to={to!} canManage={canManage} onChange={loadNotes} />
            {spec.breakdown && result.breakdown.length > 0 && <TrendBreakdown result={result} breakdown={spec.breakdown} breakdown2={cleanTrend(spec).breakdown_2} />}
          </>
        )}
      </div>
    </div>
  );
}

function SeriesEditor({ n, series: x, catalog, onChange, onRemove }: { n: number; series: Series; catalog: Catalog; onChange: (s: Series) => void; onRemove?: () => void }) {
  const id = `series-${n}`;
  const ownFields = x.kind === "search" ? catalog.fields.search : x.kind === "visit" ? catalog.fields.visit : x.kind === "payment" ? catalog.fields.payment : catalog.fields.event;
  // Filters can also test the person's or their company's own fields, such as their plan.
  const fields = x.kind === "search" ? ownFields : [...ownFields, ...recordFieldKeys(catalog)];
  const filters = x.filters ?? [];
  const setFilters = (f: Filter[]) => onChange({ ...x, filters: f });
  const measures = isSource(x.kind) ? catalog.source_metrics[x.kind] ?? [] : x.kind === "ad" ? catalog.ad_metrics : x.kind === "visit" ? catalog.measures.filter((m) => !readsProperty(m)) : catalog.measures;
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
              <option key={e.name} value={e.name}>{`${e.label ? `${e.label} · ` : ""}${whole(e.count)} in 90 days`}</option>
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
        <select aria-label="Measure" className={`${field} min-w-0`} value={x.measure ?? (x.kind === "ad" ? "clicks" : sourceDefault[x.kind] ?? "count")} onChange={(e) => onChange({ ...x, measure: e.target.value as Series["measure"], property: undefined })}>
          {measures.map((m) => (
            <option key={m} value={m}>{measureLabels[m] ?? m}</option>
          ))}
        </select>
        {readsProperty(x.measure) && x.kind !== "payment" && (
          <input aria-label="Property" className={`${field} min-w-0 font-mono`} placeholder="property, such as seats" value={x.property ?? ""} onChange={(e) => onChange({ ...x, property: e.target.value.trim() })} />
        )}
        {readsProperty(x.measure) && x.kind === "payment" && <span className="self-center text-[12px] text-muted-foreground">the amount, in the brand's currency</span>}
        {x.kind === "analytics" && (
          <select aria-label="Tool" className={`${field} min-w-0`} value={x.connector ?? ""} onChange={(e) => onChange({ ...x, connector: e.target.value || undefined })}>
            <option value="">Every tool</option>
            {catalog.analytics_connectors.map((c) => (
              <option key={c} value={c}>{connectorLabels[c] ?? c}</option>
            ))}
          </select>
        )}
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
      {x.kind !== "ad" && (!isSource(x.kind) || x.kind === "search") && (
        <>
          {filters.map((f, j) => (
            <div key={j} className="mt-2 grid grid-cols-[minmax(0,1fr)_auto] gap-1.5">
              <div className="grid min-w-0 grid-cols-2 gap-1.5">
                {x.kind === "visit" || x.kind === "payment" || x.kind === "search" ? (
                  <select aria-label="Field" className={`${field} min-w-0`} value={f.property} onChange={(e) => setFilters(filters.map((y, k) => (k === j ? { ...y, property: e.target.value, value: "" } : y)))}>
                    {fields.map((fl) => (
                      <option key={fl} value={fl}>{recordFieldLabel(catalog, fl)}</option>
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
              <option key={f} value={f}>{recordFieldLabel(catalog, f)}</option>
            ))}
          </datalist>
          <button className="mt-2 text-[12px] text-muted-foreground hover:text-foreground" onClick={() => setFilters([...filters, { property: x.kind === "search" ? "country" : x.kind === "visit" ? "network" : x.kind === "payment" ? "product" : "path", op: "eq", value: "" }])}>
            + Filter
          </button>
        </>
      )}
      <input aria-label="Series name" className={`${field} mt-2 w-full`} placeholder="Name on the chart (optional)" value={x.label ?? ""} maxLength={80} onChange={(e) => onChange({ ...x, label: e.target.value || undefined })} />
    </li>
  );
}

// recordFieldKeys are the person's and their company's fields a trend can
// be split or filtered by: person:<key> and company:<key>.
function recordFieldKeys(catalog: Catalog): string[] {
  return [...catalog.person_fields.map((f) => `person:${f.key}`), ...catalog.company_fields.map((f) => `company:${f.key}`)];
}

function recordFieldLabel(catalog: Catalog, f: string): string {
  if (f.startsWith("person:")) return `Person: ${catalog.person_fields.find((x) => `person:${x.key}` === f)?.label ?? f.slice(7)}`;
  if (f.startsWith("company:")) return `Company: ${catalog.company_fields.find((x) => `company:${x.key}` === f)?.label ?? f.slice(8)}`;
  return label(f);
}

function TrendOptions({ spec, catalog, onChange }: { spec: TrendSpec; catalog: Catalog; onChange: (s: TrendSpec) => void }) {
  const preset = spec.date_range?.preset;
  // Breakdowns the first series' kind allows; the others must share them.
  const kinds = [...new Set(spec.series.map((s) => s.kind))];
  const breakdowns = kinds.length === 1 ? catalog.breakdowns[kinds[0]] ?? [] : kinds.includes("ad") || kinds.some((k) => isSource(k) && k !== "search") ? [] : (catalog.breakdowns[kinds[0]] ?? []).filter((b) => kinds.every((k) => (catalog.breakdowns[k] ?? []).includes(b)));
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
            <option value="area">Stacked areas</option>
            <option value="bar">Bars</option>
            <option value="stacked_bar">Stacked bars</option>
            <option value="horizontal_bar">Bars across (totals)</option>
            <option value="pie">A ring of shares (totals)</option>
            {spec.breakdown === "country" && <option value="map">A world map</option>}
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
          {!kinds.includes("ad") && !kinds.some(isSource) && catalog.person_fields.length > 0 && (
            <optgroup label="The person's details">
              {catalog.person_fields.map((f) => (
                <option key={f.key} value={`person:${f.key}`}>{f.label}</option>
              ))}
            </optgroup>
          )}
          {!kinds.includes("ad") && !kinds.some(isSource) && catalog.company_fields.length > 0 && (
            <optgroup label="Their company's details">
              {catalog.company_fields.map((f) => (
                <option key={f.key} value={`company:${f.key}`}>{f.label}</option>
              ))}
            </optgroup>
          )}
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
      {spec.breakdown && !kinds.includes("ad") && (
        <label className="block">
          <span className="text-muted-foreground">Then by</span>
          <select id="trend-breakdown-2" className={`${field} mt-1 block w-full`} value={spec.breakdown_2 ?? ""} onChange={(e) => onChange({ ...spec, breakdown_2: e.target.value || undefined })}>
            <option value="">Nothing</option>
            {breakdowns.filter((b) => b !== spec.breakdown).map((b) => (
              <option key={b} value={b}>{label(b)}</option>
            ))}
            {!kinds.some(isSource) && recordFieldKeys(catalog).filter((b) => b !== spec.breakdown).map((b) => (
              <option key={b} value={b}>{recordFieldLabel(catalog, b)}</option>
            ))}
          </select>
        </label>
      )}
      <label className="block">
        <span className="text-muted-foreground">Formula</span>
        <input id="trend-formula" className={`${field} mt-1 block w-full font-mono`} placeholder="A / B" value={spec.formula ?? ""} maxLength={200} onChange={(e) => onChange({ ...spec, formula: e.target.value || undefined })} />
        <span className="mt-1 block text-[12px] text-muted-foreground">A, B, C are the series, with + − × ÷ and brackets. Spend ÷ sign-ups is cost per sign-up.</span>
      </label>
      <label className="flex items-center gap-2">
        <input type="checkbox" checked={!!spec.compare} onChange={(e) => onChange({ ...spec, compare: e.target.checked || undefined })} />
        Compare with the period before
      </label>
      <label className="flex items-center gap-2">
        <input type="checkbox" checked={spec.forecast !== false} onChange={(e) => onChange({ ...spec, forecast: e.target.checked ? undefined : false })} />
        Show a forecast
        <span className="text-[12px] text-muted-foreground">(lines and bars)</span>
      </label>
      {timeDisplays.includes(spec.display ?? "line") && (
        <>
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={!!spec.cumulative} onChange={(e) => onChange({ ...spec, cumulative: e.target.checked || undefined })} />
            Running total
          </label>
          {spec.breakdown && (
            <label className="flex items-center gap-2">
              <input type="checkbox" checked={!!spec.percent} onChange={(e) => onChange({ ...spec, percent: e.target.checked || undefined })} />
              Each group as a share of the whole
            </label>
          )}
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={!!spec.value_labels} onChange={(e) => onChange({ ...spec, value_labels: e.target.checked || undefined })} />
            Numbers on the chart
          </label>
          <label className="block">
            <span className="text-muted-foreground">Smooth over</span>
            <select id="trend-smoothing" className={`${field} mt-1 block w-full`} value={spec.smoothing ?? 1} onChange={(e) => onChange({ ...spec, smoothing: Number(e.target.value) === 1 ? undefined : (Number(e.target.value) as TrendSpec["smoothing"]) })}>
              <option value={1}>Nothing: every period as it is</option>
              {[3, 7, 14, 28].map((k) => (
                <option key={k} value={k}>A moving average of {k} periods</option>
              ))}
            </select>
          </label>
          {!spec.breakdown && spec.series.length > 1 && (spec.display ?? "line") !== "area" && spec.display !== "stacked_bar" && (
            <div>
              <span className="text-muted-foreground">Read against a second axis on the right</span>
              <div className="mt-1 flex flex-wrap gap-x-4 gap-y-1">
                {spec.series.map((_, i) => {
                  const l = String.fromCharCode(65 + i);
                  const on = (spec.right_axis ?? []).includes(l);
                  return (
                    <label key={l} className="flex items-center gap-1.5">
                      <input
                        type="checkbox"
                        checked={on}
                        disabled={!on && (spec.right_axis ?? []).length >= spec.series.length - 1}
                        onChange={(e) => onChange({ ...spec, right_axis: e.target.checked ? [...(spec.right_axis ?? []), l] : (spec.right_axis ?? []).filter((y) => y !== l) })}
                      />
                      <span className="font-mono text-[12px]">{l}</span>
                    </label>
                  );
                })}
              </div>
              <span className="mt-1 block text-[12px] text-muted-foreground">For numbers of a different size, such as spend beside sign-ups.</span>
            </div>
          )}
        </>
      )}
    </div>
  );
}

const timeDisplays = ["line", "area", "bar", "stacked_bar"];

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

// groupName is a breakdown group as people read it.
function groupName(g: { value: string; values?: string[] }): string {
  if (g.value === "$other") return "Everything else";
  const vs = g.values?.length ? g.values : [g.value];
  return vs.map((v) => v || "(none)").join(" / ");
}

// average smooths values over the k periods up to each one.
function average(values: (number | null)[], k: number): (number | null)[] {
  return values.map((_, i) => {
    const w = values.slice(Math.max(0, i - k + 1), i + 1).filter((v): v is number => typeof v === "number");
    return w.length ? w.reduce((a, b) => a + b, 0) / w.length : null;
  });
}

// running adds values up from the first period.
function running(values: (number | null)[]): (number | null)[] {
  let sum = 0, seen = false;
  return values.map((v) => {
    if (typeof v === "number") {
      sum += v;
      seen = true;
    }
    return seen ? sum : null;
  });
}

// How many groups a chart draws at once: one colour each.
const maxGroups = 6;

// TrendView draws a trend result as lines, areas, bars, totals, a map,
// numbers or a table. With a breakdown the chart draws the groups of
// the formula, else of the first series.
export function TrendView({ result, display, compact, annotations, forecast = false, view }: { result: TrendResult; display: Display; compact?: boolean; annotations?: Annotation[]; forecast?: boolean; view?: TrendDraw }) {
  const all = result.formula ? [...result.series, result.formula] : result.series;
  const prev = result.previous ? (result.previous.formula ? [...result.previous.series, result.previous.formula] : result.previous.series) : null;
  const grouped = !!view?.breakdown && result.breakdown.length > 0 && display !== "number" && display !== "table";
  // Groups off the chart, by value; the first six are on to begin with.
  const [off, setOff] = useState<Set<string> | null>(null);
  const [allGroups, setAllGroups] = useState(false);
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
  const starts = (all[0]?.points ?? []).map((p) => p.start);
  if (starts.length === 0 || all.every((s) => s.points.every((p) => p.value === null || p.value === undefined))) return <Empty title="Nothing in these dates" />;

  // What the chart draws: the series, or one line per group.
  type Item = { key: string; label: string; currency?: string; total: number | null; values: (number | null)[]; previous: (number | null)[] | null };
  const gi = grouped && result.formula ? (result.breakdown[0]?.series.length ?? 1) - 1 : 0;
  const items: Item[] = grouped
    ? result.breakdown.map((g) => {
        const s = g.series[gi];
        const before = result.previous?.breakdown.find((x) => x.value === g.value)?.series[gi];
        return { key: g.value, label: groupName(g), currency: s?.currency, total: s?.total ?? null, values: (s?.points ?? []).map((p) => p.value), previous: before ? before.points.map((p) => p.value) : null };
      })
    : all.map((s, i) => ({ key: String(i), label: s.label, currency: s.currency, total: s.total, values: s.points.map((p) => p.value), previous: prev?.[i]?.points.map((p) => p.value) ?? null }));
  const what = grouped ? all[gi]?.label : undefined;
  const parts: Part[] = items.map((x) => ({ label: x.label, value: x.total, currency: x.currency }));

  if (display === "horizontal_bar") return <Caption what={what}><HBars parts={parts} format={fmt} compact={compact} /></Caption>;
  if (display === "pie") return <Caption what={what}><Ring parts={parts} format={fmt} compact={compact} /></Caption>;
  if (display === "map") return <Caption what={what}><WorldMap parts={items.map((x) => ({ label: x.key, value: x.total, currency: x.currency }))} format={fmt} compact={compact} /></Caption>;

  const isOff = (x: Item, i: number) => (off ? off.has(x.key) : grouped && i >= maxGroups);
  const percent = !!view?.percent && grouped && items.length > 1;
  const k = view?.smoothing ?? 1;
  // Smooth, then add up, then turn into shares of every group's sum.
  let drawn = items.map((x) => {
    let values = k > 1 ? average(x.values, k) : x.values;
    let previous = x.previous && k > 1 ? average(x.previous, k) : x.previous;
    if (view?.cumulative) {
      values = running(values);
      previous = previous ? running(previous) : previous;
    }
    return { ...x, values, previous };
  });
  if (percent) {
    const sums = starts.map((_, i) => drawn.reduce((a, x) => a + Math.max(0, x.values[i] ?? 0), 0));
    drawn = drawn.map((x) => ({ ...x, previous: null, values: x.values.map((v, i) => (sums[i] > 0 ? (Math.max(0, v ?? 0) / sums[i]) * 100 : null)) }));
  }
  const on = drawn.filter((x, i) => !isOff(x, i));
  const stacked = (display === "area" || display === "stacked_bar" || percent) && on.length > 1;
  const bars = display === "bar" || display === "stacked_bar";
  const canForecast = forecast && !percent && !stacked;
  const right = grouped ? [] : (view?.right_axis ?? []).map((l) => l.charCodeAt(0) - 65).filter((i) => i >= 0 && i < on.length);
  const format = percent ? (v: number | null | undefined) => (v === null || v === undefined ? "–" : `${v.toFixed(v < 10 ? 1 : 0)}%`) : fmt;
  const toggle = (x: Item) => {
    const now = new Set(off ?? items.filter((y, i) => isOff(y, i)).map((y) => y.key));
    if (now.has(x.key)) {
      if (items.length - now.size >= maxGroups) return;
      now.delete(x.key);
    } else if (items.length - now.size > 1) now.add(x.key);
    setOff(now);
  };
  const colorOf = (x: Item) => on.findIndex((y) => y.key === x.key);
  return (
    <div className={compact ? "flex h-full min-h-[200px] flex-col" : ""}>
      {what && !compact && <p className="mb-2 text-[12px] text-muted-foreground">{what}, by group{percent ? ", as a share of the whole" : ""}.</p>}
      <div className={compact ? "relative min-h-0 flex-1" : ""}>
      <TimeChart
        label={`Trend of ${on.map((s) => s.label).join(", ")}`}
        starts={starts}
        series={on.map((s) => ({ label: s.label, currency: s.currency, values: s.values, previous: s.previous }))}
        bars={bars}
        stacked={stacked}
        area={display === "area"}
        valueLabels={!!view?.value_labels}
        rightAxis={right}
        unit={percent ? "%" : ""}
        height={compact ? "fill" : 280}
        showForecast={canForecast}
        notes={annotations}
        format={format}
      />
      </div>
      <ul className="mt-3 flex flex-wrap items-center gap-x-5 gap-y-1.5 text-[12px]">
        {(grouped && !allGroups ? items.slice(0, compact ? maxGroups : 12) : items).map((s, i) => {
          const c = colorOf(s);
          const body = (
            <>
              <Swatch color={c >= 0 ? seriesColor(c) : "var(--muted-foreground)"} opacity={c >= 0 ? 1 : 0.35} />
              <span className={`truncate ${c >= 0 ? "text-muted-foreground" : "text-muted-foreground/60 line-through"}`} title={s.label}>{s.label}</span>
              <span className={`font-semibold tabular-nums ${c >= 0 ? "" : "opacity-50"}`}>{fmt(s.total, s.currency)}</span>
              {!grouped && right.includes(i) && <span className="text-[11px] text-muted-foreground">right axis</span>}
              {!grouped && prev && change(s.total, prev[i]?.total) && (
                <span className={`rounded px-1 text-[11px] font-medium ${change(s.total, prev[i]?.total)!.startsWith("+") ? "bg-green-500/10 text-green-700 dark:text-green-400" : "bg-red-500/10 text-red-600 dark:text-red-400"}`}>
                  {change(s.total, prev[i]?.total)}
                </span>
              )}
            </>
          );
          return (
            <li key={s.key} className="min-w-0">
              {grouped ? (
                <button type="button" className="flex min-w-0 items-center gap-1.5 rounded px-1 hover:bg-accent" aria-pressed={c >= 0} title={c >= 0 ? "Take off the chart" : on.length >= maxGroups ? `The chart shows ${maxGroups} at once: take one off first` : "Put on the chart"} onClick={() => toggle(s)}>
                  {body}
                </button>
              ) : (
                <span className="flex min-w-0 items-center gap-1.5">{body}</span>
              )}
            </li>
          );
        })}
        {grouped && !allGroups && items.length > (compact ? maxGroups : 12) && (
          <li>
            <button type="button" className="rounded px-1 text-muted-foreground underline-offset-2 hover:underline" onClick={() => setAllGroups(true)}>
              Show {items.length - (compact ? maxGroups : 12)} more
            </button>
          </li>
        )}
        {on.some((s) => s.previous) && (
          <li className="flex items-center gap-1.5 text-muted-foreground">
            <Swatch color="var(--muted-foreground)" dashed /> Period before
          </li>
        )}
        {canForecast && forecastable(starts, on.map((s) => ({ values: s.values }))) && (
          <li className="flex items-center gap-1.5 text-muted-foreground">
            <Swatch color="var(--muted-foreground)" dashed opacity={0.7} /> Forecast
          </li>
        )}
      </ul>
    </div>
  );
}

// Caption says which number a chart of groups shows.
function Caption({ what, children }: { what?: string; children: React.ReactNode }) {
  return (
    <div className="h-full">
      {what && <p className="mb-3 text-[12px] text-muted-foreground">{what}, total over the dates.</p>}
      {children}
    </div>
  );
}

function TrendBreakdown({ result, breakdown, breakdown2 }: { result: TrendResult; breakdown: string; breakdown2?: string }) {
  const cols = result.breakdown[0]?.series ?? [];
  return (
    <Card title={`By ${label(breakdown).toLowerCase()}${breakdown2 ? `, then ${label(breakdown2).toLowerCase()}` : ""}`} description="Totals over the dates, the biggest first.">
      <div className="overflow-x-auto">
        <table className="w-full text-[13px]">
          <thead>
            <tr className="border-b text-left font-mono text-[11px] uppercase text-muted-foreground">
              <th className="py-1.5 pr-3 font-medium">{label(breakdown)}{breakdown2 ? ` / ${label(breakdown2)}` : ""}</th>
              {cols.map((s, i) => (
                <th key={i} className="px-2 py-1.5 text-right font-medium" title={s.label}>{s.label}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {result.breakdown.map((g) => (
              <tr key={g.value} className="border-b last:border-0">
                <td className="max-w-[260px] truncate py-1.5 pr-3" title={groupName(g)}>{g.value === "$other" ? <em>Everything else</em> : groupName(g)}</td>
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
