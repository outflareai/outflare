import { useCallback, useEffect, useState, type ReactNode } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, Empty, ErrorText, field, Loading, PageHeader, confirmAction, toast } from "../ui";
import { ago, stamp } from "../format";
import { useBrand } from "@/lib/brand";
import { nameItem } from "@/lib/records";

// Metrics (docs/metrics.md): live totals a brand keeps for each company and
// person, such as spend in the last 30 days. Rules, journeys, smart lists
// and Query read them under attributes.metrics. Turning them on, adding
// and changing them needs catalog.manage; each fill from stored events is
// quiet, so nothing fires for the past.

type Metric = Schemas["Metric"];
type MetricList = Schemas["MetricList"];
type MetricInput = Schemas["MetricInput"];

const aggregates: Record<Metric["aggregate"], string> = {
  count: "How many events",
  sum: "Sum of a number property",
  min: "Smallest value of a number property",
  max: "Largest value of a number property",
  days: "Days with an event",
  last: "When the latest event happened",
};

export function Metrics({ ws, canManage, tabs }: { ws: string; canManage: boolean; tabs: ReactNode }) {
  const { brand, active } = useBrand(ws);
  useEffect(() => nameItem("metrics", "Metrics"), []);
  return (
    <div className="space-y-6">
      <PageHeader
        title="Metrics"
        description="Live totals for every company and person, such as spend in the last 30 days or usage this week. Rules, journeys, smart lists and Query read them like any field."
      />
      {tabs}
      {brand ? (
        <MetricsFor key={brand.id} brand={brand.id} canManage={canManage} />
      ) : (
        <Empty title="Pick a brand">
          Metrics belong to one brand. Pick {active.length > 1 ? `one of your ${active.length} brands` : "your brand"} in the brand switcher.
        </Empty>
      )}
    </div>
  );
}

function MetricsFor({ brand, canManage }: { brand: string; canManage: boolean }) {
  const [list, setList] = useState<MetricList | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState<Metric | "new" | null>(null);

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/metrics", { params: { path: { brand } } });
    if (!data) return setError(errorMessage(error));
    setError("");
    setList(data);
  }, [brand]);
  useEffect(() => {
    void load();
  }, [load]);
  // A refill runs in the background: look again until it is done.
  useEffect(() => {
    if (!list?.data.some((m) => m.backfilling)) return;
    const t = setTimeout(() => void load(), 4000);
    return () => clearTimeout(t);
  }, [list, load]);

  async function toggle(on: boolean) {
    if (
      on &&
      !(await confirmAction(
        "Turn metrics on?",
        <p>This adds the standard metrics, such as spend in the last 30 days and usage this week, and fills them from the events already stored. Filling is quiet: no rule, journey, email or webhook fires for the past.</p>,
        "Turn on",
        false,
      ))
    )
      return;
    setBusy(true);
    const { error } = await api.PATCH("/v1/brands/{brand}/metrics", { params: { path: { brand } }, body: { enabled: on } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    void load();
  }

  async function archive(m: Metric) {
    if (!(await confirmAction(`Archive ${m.name}?`, <p>It is taken off every company and person. Rules, lists and journeys that read <span className="font-mono">metrics.{m.key}</span> will no longer find it.</p>, "Archive", true))) return;
    setBusy(true);
    const { error } = await api.POST("/v1/brands/{brand}/metrics/{metric}/archive", { params: { path: { brand, metric: m.id } } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast(`${m.name} archived.`);
    void load();
  }

  async function refill(m: Metric) {
    setBusy(true);
    const { error } = await api.POST("/v1/brands/{brand}/metrics/{metric}/backfill", { params: { path: { brand, metric: m.id } } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast(`Refilling ${m.name} from stored events. Nothing fires for it.`);
    void load();
  }

  if (error) return <ErrorText>{error}</ErrorText>;
  if (!list) return <Loading />;
  const metrics = list.data.filter((m) => m.status === "active");

  return (
    <div className="space-y-6">
      <Card
        title="Metrics"
        description={list.enabled ? `On since ${stamp(list.enabled_at ?? "")}. Each new event is added straight away.` : "Off. Turn them on to keep live totals for every company and person."}
        action={
          canManage && (
            <span className="flex flex-wrap gap-2">
              {list.enabled && <Button onClick={() => setEditing("new")}>Add a metric</Button>}
              <Button variant={list.enabled ? "ghost" : "primary"} disabled={busy} onClick={() => toggle(!list.enabled)}>{list.enabled ? "Turn off" : "Turn on"}</Button>
            </span>
          )
        }
      >
        {editing === "new" && <MetricEditor brand={brand} onClose={() => setEditing(null)} onSaved={() => { setEditing(null); void load(); }} />}
        {metrics.length === 0 ? (
          <p className="text-[13px] text-muted-foreground">{list.enabled ? "No metrics yet. Add one to keep a total of your own." : "Turning metrics on adds the standard ones, which you can then change here."}</p>
        ) : (
          <ul className="divide-y">
            {metrics.map((m) => (
              <li key={m.id} className="flex flex-wrap items-center gap-3 py-2">
                <div className="min-w-0 flex-1">
                  <div className="text-[13px] font-medium">
                    {m.name} <span className="ml-1 font-mono text-[12px] text-muted-foreground">metrics.{m.key}</span>
                    {m.backfilling && <span className="ml-2"><Badge tone="blue">Filling</Badge></span>}
                  </div>
                  <div className="text-[12px] text-muted-foreground">{describe(m)}</div>
                  {m.backfilled_at && !m.backfilling && <div className="text-[12px] text-muted-foreground">Filled from stored events {ago(m.backfilled_at)}</div>}
                </div>
                {canManage && (
                  <span className="flex gap-1">
                    <Button variant="ghost" disabled={busy} onClick={() => setEditing(editing !== "new" && editing?.id === m.id ? null : m)}>Edit</Button>
                    <Button variant="ghost" disabled={busy || m.backfilling} onClick={() => refill(m)}>Refill</Button>
                    <Button variant="ghost" disabled={busy} onClick={() => archive(m)}>Archive</Button>
                  </span>
                )}
                {editing !== "new" && editing?.id === m.id && <MetricEditor brand={brand} metric={m} onClose={() => setEditing(null)} onSaved={() => { setEditing(null); void load(); }} />}
              </li>
            ))}
          </ul>
        )}
      </Card>
    </div>
  );
}

function describe(m: Metric): string {
  const what = m.aggregate === "count" || m.aggregate === "days" || m.aggregate === "last" ? aggregates[m.aggregate] : `${aggregates[m.aggregate]} (${m.property ?? ""})`;
  const counts = [...m.events, ...(m.usage ? ["any usage"] : [])].join(", ") || "no events";
  const window = m.aggregate === "last" ? "" : m.offset_days ? `, ${m.window_days} days ending ${m.offset_days} days ago` : `, last ${m.window_days} days`;
  const levels = m.levels.length === 2 ? "companies and people" : m.levels[0] === "company" ? "companies" : "people";
  return `${what} of ${counts}${window} · kept on ${levels}${m.per_product ? ", also per product" : ""}`;
}

// MetricEditor adds a metric or changes one. Changing what it counts makes
// a new version, which is refilled from stored events in the background.
function MetricEditor({ brand, metric, onClose, onSaved }: { brand: string; metric?: Metric; onClose: () => void; onSaved: () => void }) {
  const [key, setKey] = useState(metric?.key ?? "");
  const [name, setName] = useState(metric?.name ?? "");
  const [aggregate, setAggregate] = useState<Metric["aggregate"]>(metric?.aggregate ?? "count");
  const [events, setEvents] = useState((metric?.events ?? []).join(", "));
  const [usage, setUsage] = useState(metric?.usage ?? false);
  const [property, setProperty] = useState(metric?.property ?? "");
  const [windowDays, setWindowDays] = useState(String(metric?.window_days ?? 30));
  const [offsetDays, setOffsetDays] = useState(String(metric?.offset_days ?? 0));
  const [levels, setLevels] = useState<("company" | "person")[]>(metric?.levels ?? ["company", "person"]);
  const [perProduct, setPerProduct] = useState(metric?.per_product ?? true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const needsProperty = aggregate === "sum" || aggregate === "min" || aggregate === "max";
  const eventList = events.split(",").map((e) => e.trim()).filter(Boolean);
  const valid = name.trim() && (metric || /^[a-z][a-z0-9_]{0,39}$/.test(key)) && (eventList.length > 0 || usage) && (!needsProperty || property.trim()) && levels.length > 0 && Number(windowDays) >= 1 && Number(offsetDays) >= 0;

  async function save() {
    setBusy(true);
    setError("");
    const body: MetricInput = {
      name: name.trim(),
      aggregate,
      events: eventList,
      usage,
      ...(needsProperty ? { property: property.trim() } : {}),
      window_days: Number(windowDays),
      offset_days: Number(offsetDays),
      levels,
      per_product: perProduct,
    };
    const { error } = metric
      ? await api.PATCH("/v1/brands/{brand}/metrics/{metric}", { params: { path: { brand, metric: metric.id } }, body })
      : await api.POST("/v1/brands/{brand}/metrics", { params: { path: { brand } }, body: { ...body, key } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    toast(metric ? `${body.name} saved. If what it counts changed, it is refilled from stored events.` : `${body.name} added. It is being filled from stored events.`);
    onSaved();
  }
  const toggleLevel = (l: "company" | "person") => setLevels(levels.includes(l) ? levels.filter((x) => x !== l) : [...levels, l]);
  const label = "text-[12px] text-muted-foreground";
  return (
    <div className="my-2 w-full space-y-2 rounded-md border p-3">
      <div className="flex flex-wrap items-end gap-2">
        <label className={label}>Name<input className={`${field} mt-1 block w-56`} placeholder="API calls in 30 days" value={name} onChange={(e) => setName(e.target.value)} /></label>
        {!metric && <label className={label}>Key (never changes)<input className={`${field} mt-1 block w-48 font-mono`} placeholder="api_calls_30d" value={key} onChange={(e) => setKey(e.target.value)} /></label>}
        <label className={label}>Counts
          <select className={`${field} mt-1 block`} value={aggregate} onChange={(e) => setAggregate(e.target.value as Metric["aggregate"])}>
            {Object.entries(aggregates).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
          </select>
        </label>
        {needsProperty && <label className={label}>Number property<input className={`${field} mt-1 block w-40 font-mono`} placeholder="amount_minor" value={property} onChange={(e) => setProperty(e.target.value)} /></label>}
      </div>
      <div className="flex flex-wrap items-end gap-2">
        <label className={label}>Event names, separated by commas<input className={`${field} mt-1 block w-80 font-mono`} placeholder="api_called, payment.succeeded" value={events} onChange={(e) => setEvents(e.target.value)} /></label>
        <label className="flex items-center gap-1.5 text-[13px]"><input type="checkbox" checked={usage} onChange={(e) => setUsage(e.target.checked)} />Also any usage</label>
      </div>
      <div className="flex flex-wrap items-end gap-2">
        {aggregate !== "last" && <label className={label}>Over the last (days)<input type="number" min={1} max={365} className={`${field} mt-1 block w-24`} value={windowDays} onChange={(e) => setWindowDays(e.target.value)} /></label>}
        {aggregate !== "last" && <label className={label}>Ending (days ago)<input type="number" min={0} max={365} className={`${field} mt-1 block w-24`} value={offsetDays} onChange={(e) => setOffsetDays(e.target.value)} /></label>}
        <label className="flex items-center gap-1.5 text-[13px]"><input type="checkbox" checked={levels.includes("company")} onChange={() => toggleLevel("company")} />Companies</label>
        <label className="flex items-center gap-1.5 text-[13px]"><input type="checkbox" checked={levels.includes("person")} onChange={() => toggleLevel("person")} />People</label>
        <label className="flex items-center gap-1.5 text-[13px]"><input type="checkbox" checked={perProduct} onChange={(e) => setPerProduct(e.target.checked)} />Also per product</label>
      </div>
      <p className="text-[12px] text-muted-foreground">Usage is any event your product sends. Billing syncs, Outflare's own events and website events never count. Filling from stored events never sets off a rule, journey, email or webhook.</p>
      {error && <ErrorText>{error}</ErrorText>}
      <div className="flex gap-2">
        <Button disabled={busy || !valid} onClick={save}>{metric ? "Save" : "Add"}</Button>
        <Button variant="ghost" onClick={onClose}>Cancel</Button>
      </div>
    </div>
  );
}
