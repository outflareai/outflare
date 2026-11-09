import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, Empty, ErrorText, field, Loading, PageHeader, toast } from "../ui";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { PickBrand } from "@/components/pick-brand";
import { useBrand } from "@/lib/brand";
import { appPath } from "@/lib/paths";
import { nameItem } from "@/lib/records";
import { track } from "@/lib/analytics";
import { ago, stamp } from "../format";
import { money } from "./Transactions";
import { takeDraft } from "@/components/insight-panels";

// Funnels (docs/insights.md): people funnels over website visits, page
// views, product events, saved conversions and payments. Only people who
// did step 1 count at later steps, each step can have its own time limit,
// and every bar and drop-off opens the people behind it. Ad networks'
// clicks show as a grey bar above step 1: they are totals, not people.

type Spec = Schemas["FunnelSpec"];
type Step = Schemas["FunnelStep"];
type Filter = Schemas["InsightFilter"];
type Result = Schemas["FunnelResult"];
type StepResult = Schemas["FunnelStepResult"];
type Catalog = Schemas["InsightsCatalog"];
type Insight = Schemas["Insight"];
type Person = Schemas["FunnelPerson"];
type Kind = Step["kind"];
type Unit = Schemas["InsightWindow"]["unit"];
type Preset = NonNullable<Schemas["InsightDateRange"]["preset"]>;
type Exclusion = Schemas["FunnelExclusion"];
type Correlation = Schemas["CorrelationResult"];

const kindLabels: Record<Kind, string> = {
  visit: "Visited the website",
  page: "Viewed a page",
  event: "Did an event",
  conversion: "Converted",
  payment: "Paid",
};

export const presetLabels: Record<Preset, string> = {
  today: "Today",
  yesterday: "Yesterday",
  last_7_days: "Last 7 days",
  last_14_days: "Last 14 days",
  last_30_days: "Last 30 days",
  last_90_days: "Last 90 days",
  last_180_days: "Last 180 days",
  last_365_days: "Last 365 days",
  this_month: "This month",
  last_month: "Last month",
  this_year: "This year",
};

export const opLabels: Record<Filter["op"], string> = {
  eq: "is",
  neq: "is not",
  contains: "contains",
  not_contains: "doesn't contain",
  in: "is one of",
  gt: "more than",
  gte: "at least",
  lt: "less than",
  lte: "at most",
  exists: "is set",
  not_exists: "isn't set",
};

// Plain names for the built-in fields.
export const fieldLabels: Record<string, string> = {
  network: "Ad network",
  channel: "Channel",
  source: "Source",
  campaign: "Campaign",
  utm_source: "UTM source",
  utm_medium: "UTM medium",
  utm_campaign: "UTM campaign",
  utm_term: "UTM term",
  utm_content: "UTM content",
  landing_path: "Landing page",
  landing_url: "Landing address",
  referrer_host: "Referring site",
  referrer: "Referrer",
  device: "Device",
  country: "Country",
  browser: "Browser",
  os: "Operating system",
  region: "Region",
  city: "City",
  isp: "Network (ISP)",
  asn: "Network number (ASN)",
  device_brand: "Device maker",
  device_model: "Device model",
  os_version: "System version",
  browser_version: "Browser version",
  language: "Language",
  partner: "Partner code",
  ad_campaign: "Ad campaign id",
  ad_set: "Ad set id",
  ad: "Ad id",
  path: "Page path",
  url: "Page address",
  host: "Website",
  title: "Page title",
  amount: "Amount",
  product: "Product",
  currency: "Currency",
};

export const label = (f: string) => fieldLabels[f] ?? (f.startsWith("param:") ? `Address parameter ${f.slice(6)}` : f.startsWith("property:") ? f.slice(9) : f);

const starter: Spec = {
  date_range: { preset: "last_30_days" },
  steps: [
    { kind: "visit" },
    { kind: "event", event: "signed_up", within: { value: 7, unit: "day" } },
    { kind: "payment", first: true, within: { value: 3, unit: "day" } },
  ],
};

// ready says whether every step says enough to run.
function ready(s: Spec): boolean {
  return (
    s.steps.length >= 2 &&
    s.steps.every((st) => (st.kind !== "event" || !!st.event) && (st.kind !== "conversion" || !!st.conversion)) &&
    s.steps.every((st) => (st.filters ?? []).every((f) => f.property && (f.op === "exists" || f.op === "not_exists" || (f.value !== undefined && f.value !== "")))) &&
    (s.exclusions ?? []).every((x) => (x.kind !== "event" || !!x.event) && (x.kind !== "conversion" || !!x.conversion) && x.from < x.to && x.to <= s.steps.length)
  );
}

// clean drops what the API would refuse: empty filters, and from on a
// step limit that points at itself.
function clean(s: Spec): Spec {
  return {
    ...s,
    steps: s.steps.map((st, i) => {
      const out: Step = { ...st };
      if (!out.filters?.length) delete out.filters;
      if (i === 0 || s.order === "any") delete out.within;
      if (out.within?.from && out.within.from >= i + 1) out.within = { value: out.within.value, unit: out.within.unit };
      if (!out.label) delete out.label;
      return out;
    }),
    exclusions: s.exclusions?.length && s.order !== "any" ? s.exclusions.map((x) => ({ ...x, filters: x.filters?.length ? x.filters : undefined })) : undefined,
    by: s.by === "company" ? "company" : undefined,
    order: s.order === "any" ? "any" : undefined,
  };
}

export const pct = (x: number) => `${(x * 100).toFixed(x > 0 && x < 0.1 ? 1 : 0)}%`;
export const whole = (n: number) => n.toLocaleString();

// span reads seconds as "3 min", "4.2 hours" or "1.5 days".
function span(s: number | null | undefined): string {
  if (s === null || s === undefined) return "–";
  if (s < 90) return `${Math.round(s)} s`;
  if (s < 5400) return `${Math.round(s / 60)} min`;
  if (s < 36 * 3600) return `${(s / 3600).toFixed(1)} hours`;
  return `${(s / 86400).toFixed(1)} days`;
}

export function Funnels({ ws, canManage }: { ws: string; canManage: boolean }) {
  const { brand, active } = useBrand(ws);
  useEffect(() => nameItem("funnels", "Funnels"), []);
  return (
    <div className="space-y-6">
      <PageHeader
        title="Funnels"
        description="Follow people from an ad visit to sign-up to payment. Only people who did the first step count after it, so you see exactly where they drop off."
      />
      {brand ? <FunnelsFor key={brand.id} ws={ws} brand={brand.id} canManage={canManage} /> : <PickBrand what="Funnels" brands={active.length} />}
    </div>
  );
}

function FunnelsFor({ ws, brand, canManage }: { ws: string; brand: string; canManage: boolean }) {
  const [catalog, setCatalog] = useState<Catalog | null>(null);
  const [saved, setSaved] = useState<Insight[]>([]);
  const [current, setCurrent] = useState<Insight | null>(null);
  const [spec, setSpec] = useState<Spec>(() => takeDraft<Spec>("funnel") ?? starter);
  const [result, setResult] = useState<Result | null>(null);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState("");
  const [people, setPeople] = useState<{ step: number; dropped: boolean; breakdown?: string } | null>(null);
  const [name, setName] = useState("");
  const run = useRef(0);

  useEffect(() => {
    api.GET("/v1/brands/{brand}/insights/catalog", { params: { path: { brand } } }).then(({ data, error }) => (data ? setCatalog(data) : setError(errorMessage(error))));
    loadSaved();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [brand]);

  const loadSaved = useCallback(async () => {
    const { data } = await api.GET("/v1/brands/{brand}/insights", { params: { path: { brand }, query: { kind: "funnel" } } });
    if (data) setSaved(data.data);
  }, [brand]);

  // Runs the funnel shortly after each change.
  useEffect(() => {
    if (!ready(spec)) return;
    const n = ++run.current;
    const t = setTimeout(async () => {
      setRunning(true);
      const { data, error } = await api.POST("/v1/brands/{brand}/insights/funnel", { params: { path: { brand } }, body: clean(spec) });
      if (n !== run.current) return;
      setRunning(false);
      if (!data) return setError(errorMessage(error));
      setError("");
      setResult(data);
    }, 400);
    return () => clearTimeout(t);
  }, [brand, spec]);

  const setStep = (i: number, st: Step) => setSpec((s) => ({ ...s, steps: s.steps.map((x, j) => (j === i ? st : x)) }));
  const removeStep = (i: number) => setSpec((s) => ({ ...s, steps: s.steps.filter((_, j) => j !== i), breakdown: i === 0 ? undefined : s.breakdown }));
  const addStep = () => setSpec((s) => ({ ...s, steps: [...s.steps, { kind: "event", event: "" }] }));

  function open(i: Insight) {
    setCurrent(i);
    setName(i.name);
    setSpec(i.spec as Spec);
  }

  async function save() {
    const body = clean(spec);
    if (current) {
      const { data, error } = await api.PATCH("/v1/brands/{brand}/insights/{insight}", { params: { path: { brand, insight: current.id } }, body: { name, spec: body } });
      if (!data) return toast(errorMessage(error), "error");
      setCurrent(data);
      toast("Saved");
    } else {
      const { data, error } = await api.POST("/v1/brands/{brand}/insights", { params: { path: { brand } }, body: { kind: "funnel", name, spec: body } });
      if (!data || !("id" in data)) return toast(errorMessage(error), "error");
      setCurrent(data);
      toast("Saved");
    }
    loadSaved();
  }

  async function remove() {
    if (!current || !(await confirmAction(`Delete ${current.name}?`, "The funnel goes for everyone. The data it shows stays.", "Delete"))) return;
    const { error } = await api.DELETE("/v1/brands/{brand}/insights/{insight}", { params: { path: { brand, insight: current.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Deleted");
    setCurrent(null);
    setName("");
    loadSaved();
  }

  if (!catalog) return error ? <ErrorText>{error}</ErrorText> : <Loading />;

  return (
    <div className="grid gap-6 xl:grid-cols-[380px_minmax(0,1fr)]">
      <div className="min-w-0 space-y-4">
        <Card
          title="Saved funnels"
          action={
            current ? (
              <Button variant="ghost" onClick={() => { setCurrent(null); setName(""); setSpec(starter); }}>New funnel</Button>
            ) : undefined
          }
        >
          {saved.length === 0 ? (
            <p className="text-[13px] text-muted-foreground">None yet. Build one below and save it to open it again.</p>
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

        <Card title="Steps" description={spec.order === "any" ? "People do them in any order; the chart counts how many of the steps they got through." : "People must do them in this order."}>
          <ol className="space-y-3">
            {spec.steps.map((st, i) => (
              <StepEditor
                key={i}
                n={i + 1}
                step={st}
                catalog={catalog}
                onChange={(s) => setStep(i, s)}
                onRemove={spec.steps.length > 2 ? () => removeStep(i) : undefined}
              />
            ))}
          </ol>
          {spec.steps.length < 10 && (
            <Button variant="ghost" className="mt-3 w-full" onClick={addStep}>Add a step</Button>
          )}
          {spec.order !== "any" && <Exclusions spec={spec} catalog={catalog} onChange={setSpec} />}
        </Card>

        <Card title="Options">
          <Options spec={spec} catalog={catalog} onChange={setSpec} />
        </Card>

        {canManage && (
          <Card title={current ? "Save changes" : "Save this funnel"}>
            <div className="flex gap-2">
              <input id="funnel-name" aria-label="Name" className={`${field} min-w-0 flex-1`} placeholder="Google ads to first purchase" value={name} onChange={(e) => setName(e.target.value)} maxLength={120} />
              <Button disabled={!name.trim() || !ready(spec)} onClick={save}>Save</Button>
            </div>
            {current && (
              <button className="mt-2 text-[12px] text-muted-foreground hover:text-destructive" onClick={remove}>Delete this funnel</button>
            )}
          </Card>
        )}
      </div>

      <div className="min-w-0 space-y-4">
        {error && <ErrorText>{error}</ErrorText>}
        {!ready(spec) && <Empty title="Finish the steps">Each event step needs an event, each conversion step a saved conversion, and each filter a value.</Empty>}
        {ready(spec) && !result && <Loading />}
        {ready(spec) && result && (
          <>
            {spec.by === "company" && <p className="text-[12px] text-muted-foreground">Counting companies: a company reaches a step when any of its people does. Visitors nobody has identified belong to no company and are left out.</p>}
            <FunnelChart result={result} running={running} onPeople={(step, dropped) => { track("ui.funnel_people_opened", { dropped }); setPeople({ step, dropped }); }} />
            {spec.breakdown && result.breakdown.length > 0 && (
              <BreakdownTable result={result} breakdown={spec.breakdown} onPeople={(step, value) => { track("ui.funnel_people_opened", { dropped: false, breakdown: true }); setPeople({ step, dropped: false, breakdown: value }); }} />
            )}
            {spec.interval && result.over_time.length > 0 && <OverTime result={result} interval={spec.interval} />}
            {spec.by !== "company" && <CorrelationCard key={JSON.stringify(clean(spec))} brand={brand} spec={clean(spec)} labels={result.steps.map((s) => s.label)} />}
          </>
        )}
      </div>

      {people && result && (
        <PeopleSheet
          ws={ws}
          brand={brand}
          spec={clean(spec)}
          labels={result.steps.map((s) => s.label)}
          step={people.step}
          dropped={people.dropped}
          breakdown={people.breakdown}
          canSave={canManage && spec.by !== "company"}
          onClose={() => setPeople(null)}
        />
      )}
    </div>
  );
}

export function StepEditor({ n, step, catalog, onChange, onRemove, within = true, title }: { n: number; step: Step; catalog: Catalog; onChange: (s: Step) => void; onRemove?: () => void; within?: boolean; title?: string }) {
  const fields = step.kind === "visit" ? catalog.fields.visit : step.kind === "payment" ? catalog.fields.payment : catalog.fields.event;
  const filters = step.filters ?? [];
  const setFilters = (f: Filter[]) => onChange({ ...step, filters: f });
  const id = `step-${n}`;
  return (
    <li className="rounded-md border bg-background p-3">
      <div className="flex items-center gap-2">
        <span className="font-mono text-[11px] font-medium text-muted-foreground">{title ?? n}</span>
        <select
          id={`${id}-kind`}
          aria-label={`Step ${n} kind`}
          className={`${field} min-w-0 flex-1`}
          value={step.kind}
          onChange={(e) => onChange({ kind: e.target.value as Kind, within: step.within })}
        >
          {(Object.keys(kindLabels) as Kind[]).map((k) => (
            <option key={k} value={k}>{kindLabels[k]}</option>
          ))}
        </select>
        {onRemove && (
          <button aria-label={`Remove step ${n}`} className="px-1 text-muted-foreground hover:text-foreground" onClick={onRemove}>×</button>
        )}
      </div>

      {step.kind === "event" && (
        <>
          <input
            id={`${id}-event`}
            aria-label={`Step ${n} event`}
            list={`${id}-events`}
            className={`${field} mt-2 w-full font-mono`}
            placeholder="signed_up"
            value={step.event ?? ""}
            onChange={(e) => onChange({ ...step, event: e.target.value.trim() })}
          />
          <datalist id={`${id}-events`}>
            {catalog.events.map((e) => (
              <option key={e.name} value={e.name}>{`${whole(e.count)} in 90 days`}</option>
            ))}
          </datalist>
        </>
      )}
      {step.kind === "conversion" && (
        catalog.conversions.length === 0 ? (
          <p className="mt-2 text-[12px] text-muted-foreground">No saved conversions yet. Add them under Paid ads, Conversions.</p>
        ) : (
          <select id={`${id}-conversion`} aria-label={`Step ${n} conversion`} className={`${field} mt-2 w-full`} value={step.conversion ?? ""} onChange={(e) => onChange({ ...step, conversion: e.target.value })}>
            <option value="">Pick a conversion</option>
            {catalog.conversions.map((c) => (
              <option key={c.key} value={c.key}>{c.name}</option>
            ))}
          </select>
        )
      )}
      {(step.kind === "event" || step.kind === "payment") && (
        <label className="mt-2 flex items-center gap-2 text-[13px]">
          <input type="checkbox" checked={!!step.first} onChange={(e) => onChange({ ...step, first: e.target.checked || undefined })} />
          {step.kind === "payment" ? "Only their first payment ever" : "Only the first time they ever did it"}
        </label>
      )}

      {filters.map((f, j) => (
        <div key={j} className="mt-2 grid grid-cols-[minmax(0,1fr)_auto] gap-1.5">
          <div className="grid min-w-0 grid-cols-2 gap-1.5">
            {step.kind === "visit" || step.kind === "payment" ? (
              <select aria-label="Field" className={`${field} min-w-0`} value={f.property} onChange={(e) => setFilters(filters.map((x, k) => (k === j ? { ...x, property: e.target.value, value: "" } : x)))}>
                {fields.map((fl) => (
                  <option key={fl} value={fl}>{label(fl)}</option>
                ))}
              </select>
            ) : (
              <input
                aria-label="Field"
                list={`${id}-fields`}
                className={`${field} min-w-0`}
                value={f.property}
                placeholder="Field, such as path or plan"
                onChange={(e) => setFilters(filters.map((x, k) => (k === j ? { ...x, property: e.target.value.trim() } : x)))}
              />
            )}
            <select aria-label="Comparison" className={`${field} min-w-0`} value={f.op} onChange={(e) => setFilters(filters.map((x, k) => (k === j ? { ...x, op: e.target.value as Filter["op"] } : x)))}>
              {(Object.keys(opLabels) as Filter["op"][]).map((o) => (
                <option key={o} value={o}>{opLabels[o]}</option>
              ))}
            </select>
            {f.op !== "exists" && f.op !== "not_exists" && (
              f.property === "network" && step.kind === "visit" ? (
                <select aria-label="Value" className={`${field} col-span-2 min-w-0`} value={String(f.value ?? "")} onChange={(e) => setFilters(filters.map((x, k) => (k === j ? { ...x, value: e.target.value } : x)))}>
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
                    const value = f.op === "in" ? v.split(",").map((x) => x.trim()).filter(Boolean) : ["gt", "gte", "lt", "lte"].includes(f.op) && v !== "" && !isNaN(Number(v)) ? Number(v) : v;
                    setFilters(filters.map((x, k) => (k === j ? { ...x, value } : x)));
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
      <button
        className="mt-2 text-[12px] text-muted-foreground hover:text-foreground"
        onClick={() => setFilters([...filters, { property: step.kind === "visit" ? "network" : step.kind === "payment" ? "product" : "path", op: "eq", value: "" }])}
      >
        + Filter
      </button>

      {within && n > 1 && <WithinEditor n={n} step={step} onChange={onChange} />}
    </li>
  );
}

function WithinEditor({ n, step, onChange }: { n: number; step: Step; onChange: (s: Step) => void }) {
  const w = step.within;
  if (!w) {
    return (
      <button className="ml-3 mt-2 text-[12px] text-muted-foreground hover:text-foreground" onClick={() => onChange({ ...step, within: { value: 7, unit: "day" } })}>
        + Time limit
      </button>
    );
  }
  return (
    <div className="mt-2 rounded bg-muted/50 px-2 py-1.5 text-[12px]">
      <div className="flex items-center justify-between">
        <span className="text-muted-foreground">Time limit</span>
        <button aria-label="Remove time limit" className="px-1 text-muted-foreground hover:text-foreground" onClick={() => { const { within: _, ...rest } = step; onChange(rest); }}>×</button>
      </div>
      <div className="mt-1 flex items-center gap-1.5">
        <span>Within</span>
        <input aria-label="Time limit" type="number" min={1} className={`${field} h-7 w-14`} value={w.value} onChange={(e) => onChange({ ...step, within: { ...w, value: Math.max(1, Number(e.target.value) || 1) } })} />
        <select aria-label="Unit" className={`${field} h-7`} value={w.unit} onChange={(e) => onChange({ ...step, within: { ...w, unit: e.target.value as Unit } })}>
          <option value="minute">minutes</option>
          <option value="hour">hours</option>
          <option value="day">days</option>
          <option value="week">weeks</option>
        </select>
        <span className="whitespace-nowrap">of step</span>
        <select aria-label="From step" className={`${field} h-7`} value={w.from ?? n - 1} onChange={(e) => onChange({ ...step, within: { ...w, from: Number(e.target.value) === n - 1 ? undefined : Number(e.target.value) } })}>
          {Array.from({ length: n - 1 }, (_, i) => (
            <option key={i} value={i + 1}>{i + 1}</option>
          ))}
        </select>
      </div>
    </div>
  );
}

function Options({ spec, catalog, onChange }: { spec: Spec; catalog: Catalog; onChange: (s: Spec) => void }) {
  const breakdowns = catalog.breakdowns[spec.steps[0]?.kind ?? "visit"] ?? [];
  const preset = spec.date_range?.preset;
  return (
    <div className="space-y-3 text-[13px]">
      <label className="block">
        <span className="text-muted-foreground">When people did step 1</span>
        <select id="funnel-dates" className={`${field} mt-1 block w-full`} value={preset ?? "custom"} onChange={(e) => onChange({ ...spec, date_range: e.target.value === "custom" ? { from: spec.date_range?.from ?? today(-29), to: spec.date_range?.to ?? today(0) } : { preset: e.target.value as Preset } })}>
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
      <label className="block">
        <span className="text-muted-foreground">Break down by</span>
        <select id="funnel-breakdown" className={`${field} mt-1 block w-full`} value={spec.breakdown ?? ""} onChange={(e) => onChange({ ...spec, breakdown: e.target.value || undefined })}>
          <option value="">Nothing</option>
          {breakdowns.map((b) => (
            <option key={b} value={b}>{label(b)}</option>
          ))}
          {catalog.experiments.length > 0 && (
            <optgroup label="A/B test version">
              {catalog.experiments.map((x) => (
                <option key={x.field} value={x.field}>{x.name}</option>
              ))}
            </optgroup>
          )}
          {spec.breakdown?.startsWith("experiment:") && !catalog.experiments.some((x) => x.field === spec.breakdown) && <option value={spec.breakdown}>Test {spec.breakdown.slice(11)}</option>}
        </select>
      </label>
      <label className="block">
        <span className="text-muted-foreground">Conversion over time</span>
        <select id="funnel-interval" className={`${field} mt-1 block w-full`} value={spec.interval ?? ""} onChange={(e) => onChange({ ...spec, interval: (e.target.value || undefined) as Spec["interval"] })}>
          <option value="">Off</option>
          <option value="day">By day</option>
          <option value="week">By week</option>
          <option value="month">By month</option>
        </select>
      </label>
      <div>
        <span className="text-muted-foreground">Everyone must finish within</span>
        <div className="mt-1 flex gap-2">
          <input aria-label="Overall limit" type="number" min={1} className={`${field} w-20`} value={spec.window?.value ?? 14} onChange={(e) => onChange({ ...spec, window: { value: Math.max(1, Number(e.target.value) || 1), unit: spec.window?.unit ?? "day" } })} />
          <select aria-label="Overall limit unit" className={field} value={spec.window?.unit ?? "day"} onChange={(e) => onChange({ ...spec, window: { value: spec.window?.value ?? 14, unit: e.target.value as Unit } })}>
            <option value="hour">hours</option>
            <option value="day">days</option>
            <option value="week">weeks</option>
          </select>
          <span className="self-center text-muted-foreground">of step 1</span>
        </div>
      </div>
      <label className="block">
        <span className="text-muted-foreground">Order</span>
        <select id="funnel-order" className={`${field} mt-1 block w-full`} value={spec.order ?? "strict"} onChange={(e) => onChange({ ...spec, order: e.target.value === "any" ? "any" : undefined })}>
          <option value="strict">In this order</option>
          <option value="any">In any order</option>
        </select>
        {spec.order === "any" && <span className="mt-1 block text-[12px] text-muted-foreground">Step 1 is whichever step a person did first; the last bar is everyone who did all of them within the window. No exclusions or per-step limits.</span>}
      </label>
      <label className="block">
        <span className="text-muted-foreground">Count</span>
        <select id="funnel-by" className={`${field} mt-1 block w-full`} value={spec.by ?? "person"} onChange={(e) => onChange({ ...spec, by: e.target.value === "company" ? "company" : undefined })}>
          <option value="person">People</option>
          <option value="company">Companies (through their people)</option>
        </select>
      </label>
      <label className="flex items-center gap-2">
        <input type="checkbox" checked={!!spec.ad_clicks} onChange={(e) => onChange({ ...spec, ad_clicks: e.target.checked ? { network: networkOf(spec) } : undefined })} />
        Show ad clicks above step 1
      </label>
      {spec.ad_clicks && (
        <div className="grid grid-cols-2 gap-2">
          <select aria-label="Ad network" className={field} value={spec.ad_clicks.network ?? ""} onChange={(e) => onChange({ ...spec, ad_clicks: { ...spec.ad_clicks, network: e.target.value || undefined } })}>
            <option value="">Every network</option>
            {catalog.networks.filter((n) => n.connected).map((n) => (
              <option key={n.id} value={n.id}>{n.label}</option>
            ))}
          </select>
          <input aria-label="Campaign name contains" className={field} placeholder="Campaign contains" value={spec.ad_clicks.campaign ?? ""} onChange={(e) => onChange({ ...spec, ad_clicks: { ...spec.ad_clicks, campaign: e.target.value || undefined } })} />
        </div>
      )}
    </div>
  );
}

// networkOf is the network step 1 filters visits by, if any.
function networkOf(s: Spec): string | undefined {
  const f = s.steps[0]?.kind === "visit" ? s.steps[0].filters?.find((x) => x.property === "network" && x.op === "eq") : undefined;
  return f ? String(f.value) : undefined;
}

export function today(offset: number): string {
  const d = new Date(Date.now() + offset * 86400_000);
  return d.toISOString().slice(0, 10);
}

export function FunnelChart({ result, running, onPeople }: { result: Result; running: boolean; onPeople?: (step: number, dropped: boolean) => void }) {
  const ad = result.ad_clicks;
  const top = Math.max(1, result.steps[0]?.count ?? 0, ad?.clicks ?? 0);
  const last = result.steps[result.steps.length - 1];
  const columns = (ad ? 1 : 0) + result.steps.length;
  return (
    <Card
      title={
        <span className="flex items-center gap-2">
          {whole(result.steps[0]?.count ?? 0)} people · {pct(last?.conversion_from_start ?? 0)} finished
          {running && <Badge>updating</Badge>}
        </span>
      }
      description={`${day(result.from)} to ${day(new Date(new Date(result.to).getTime() - 1).toISOString())}.${onPeople ? " Click a bar for the people in it, or the striped part for those who stopped." : ""}`}
    >
      <div className="overflow-x-auto">
        <div className="grid min-w-[520px] items-end gap-3" style={{ gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))` }}>
          {ad && (
            <div className="flex h-56 flex-col justify-end" title="Clicks the ad networks reported: a total, not people">
              <div className="rounded bg-muted-foreground/30" style={{ height: `${(ad.clicks / top) * 100}%`, minHeight: ad.clicks ? 2 : 0 }} />
            </div>
          )}
          {result.steps.map((s, i) => (
            <div key={i} className="flex h-56 flex-col justify-end">
              {i > 0 && s.dropped > 0 && (
                <button
                  aria-label={`${whole(s.dropped)} people who stopped before ${s.label}`}
                  onClick={() => onPeople?.(i + 1, true)}
                  className="rounded-t border border-b-0 border-dashed bg-[repeating-linear-gradient(135deg,transparent,transparent_5px,var(--color-neutral-200)_5px,var(--color-neutral-200)_6px)] hover:border-foreground/40 dark:bg-[repeating-linear-gradient(135deg,transparent,transparent_5px,var(--color-neutral-800)_5px,var(--color-neutral-800)_6px)]"
                  style={{ height: `${(s.dropped / top) * 100}%` }}
                />
              )}
              <button
                aria-label={`${whole(s.count)} people at ${s.label}`}
                onClick={() => onPeople?.(i + 1, false)}
                className={`bg-primary hover:opacity-85 ${i > 0 && s.dropped > 0 ? "rounded-b" : "rounded"}`}
                style={{ height: `${(s.count / top) * 100}%`, minHeight: s.count ? 2 : 0 }}
              />
            </div>
          ))}
        </div>
        <div className="mt-3 grid min-w-[520px] items-start gap-3" style={{ gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))` }}>
          {ad && (
            <div className="min-w-0 text-[12px]">
              <div className="font-semibold tabular-nums text-[15px]">{whole(ad.clicks)}</div>
              <div className="text-muted-foreground">ad clicks</div>
              <div className="text-muted-foreground">{ad.spend.map((m) => money(m.amount_minor, m.currency)).join(" + ") || "no spend"}</div>
              {result.steps[0] && ad.clicks > result.steps[0].count && (
                <div className="text-red-600 dark:text-red-400">{whole(ad.clicks - result.steps[0].count)} not seen on the site</div>
              )}
            </div>
          )}
          {result.steps.map((s, i) => (
            <StepLabel key={i} n={i + 1} s={s} />
          ))}
        </div>
      </div>
      {ad && ad.campaigns === 0 && (
        <p className="mt-3 text-[12px] text-muted-foreground">No synced ad campaigns match, so ad clicks show 0. Connect an ad network under Paid ads to see them.</p>
      )}
    </Card>
  );
}

// day shows a UTC date such as 4 Nov 2026.
export function day(iso: string): string {
  return new Date(iso).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric", timeZone: "UTC" });
}

function StepLabel({ n, s }: { n: number; s: StepResult }) {
  return (
    <div className="min-w-0 text-[12px]">
      <div className="font-semibold tabular-nums text-[15px]">{whole(s.count)}</div>
      <div className="truncate" title={s.label}>
        <span className="font-mono text-muted-foreground">{n}</span> {s.label}
      </div>
      {n > 1 && (
        <>
          <div className="text-muted-foreground">{pct(s.conversion_from_previous)} of step {n - 1}</div>
          {s.dropped > 0 && <div className="text-red-600 dark:text-red-400">{whole(s.dropped)} stopped</div>}
          {s.median_seconds !== null && <div className="text-muted-foreground">median {span(s.median_seconds)}</div>}
        </>
      )}
    </div>
  );
}

function BreakdownTable({ result, breakdown, onPeople }: { result: Result; breakdown: string; onPeople: (step: number, value: string) => void }) {
  return (
    <Card title={`By ${label(breakdown).toLowerCase()}`} description="Click a number for the people behind it.">
      <div className="overflow-x-auto">
        <table className="w-full text-[13px]">
          <thead>
            <tr className="border-b text-left font-mono text-[11px] uppercase text-muted-foreground">
              <th className="py-1.5 pr-3 font-medium">{label(breakdown)}</th>
              {result.steps.map((s, i) => (
                <th key={i} className="px-2 py-1.5 text-right font-medium" title={s.label}>{i + 1}</th>
              ))}
              <th className="py-1.5 pl-2 text-right font-medium">Finished</th>
            </tr>
          </thead>
          <tbody>
            {result.breakdown.map((g) => {
              const lastStep = g.steps[g.steps.length - 1];
              return (
                <tr key={g.value} className="border-b last:border-0">
                  <td className="max-w-[260px] truncate py-1.5 pr-3" title={g.value}>{g.value === "$other" ? <em>Everything else</em> : g.value || <span className="text-muted-foreground">(none)</span>}</td>
                  {g.steps.map((s, i) => (
                    <td key={i} className="px-2 py-1.5 text-right tabular-nums">
                      {g.value === "$other" ? whole(s.count) : (
                        <button className="hover:underline" onClick={() => onPeople(i + 1, g.value)}>{whole(s.count)}</button>
                      )}
                    </td>
                  ))}
                  <td className="py-1.5 pl-2 text-right tabular-nums">{pct(lastStep?.conversion_from_start ?? 0)}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

function OverTime({ result, interval }: { result: Result; interval: string }) {
  const rows = result.over_time;
  const n = result.steps.length;
  const rate = (c: number[]) => (c[0] ? c[n - 1] / c[0] : 0);
  const max = Math.max(0.0001, ...rows.map((r) => rate(r.counts)));
  return (
    <Card title={`Finished, by the ${interval} people started`} description="Recent periods can look low while people still have time to finish.">
      <div className="space-y-1">
        {rows.map((r) => (
          <div key={r.start} className="grid grid-cols-[96px_minmax(0,1fr)_120px] items-center gap-3 text-[12px]">
            <span className="font-mono text-muted-foreground">{r.start.slice(0, 10)}</span>
            <div className="h-3 rounded bg-muted">
              <div className="h-3 rounded bg-primary" style={{ width: `${(rate(r.counts) / max) * 100}%` }} />
            </div>
            <span className="text-right tabular-nums">{pct(rate(r.counts))} of {whole(r.counts[0])}</span>
          </div>
        ))}
      </div>
    </Card>
  );
}

function PeopleSheet({ ws, brand, spec, labels, step, dropped, breakdown, canSave, onClose }: {
  ws: string; brand: string; spec: Spec; labels: string[]; step: number; dropped: boolean; breakdown?: string; canSave: boolean; onClose: () => void;
}) {
  const [list, setList] = useState<Person[] | null>(null);
  const [more, setMore] = useState(false);
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const [listName, setListName] = useState("");
  const [savedList, setSavedList] = useState<{ id: string; added: number; skipped: number } | null>(null);
  async function saveList() {
    setSaving(true);
    const { data, error } = await api.POST("/v1/brands/{brand}/insights/funnel/people/save", {
      params: { path: { brand } },
      body: { funnel: spec, step, dropped, name: listName.trim(), ...(breakdown !== undefined ? { breakdown } : {}) },
    });
    setSaving(false);
    if (!data || !("list_id" in data)) return toast(errorMessage(error), "error");
    setSavedList({ id: data.list_id, added: data.added, skipped: data.skipped_anonymous });
    toast(`Saved ${whole(data.added)} ${data.added === 1 ? "person" : "people"} to a list`);
  }
  const load = useCallback(
    async (offset: number) => {
      const { data, error } = await api.POST("/v1/brands/{brand}/insights/funnel/people", {
        params: { path: { brand } },
        body: { funnel: spec, step, dropped, limit: 50, offset, ...(breakdown !== undefined ? { breakdown } : {}) },
      });
      if (!data) return setError(errorMessage(error));
      setList((l) => (offset ? [...(l ?? []), ...data.data] : data.data));
      setMore(data.has_more);
    },
    // spec is a fresh object each render of the parent; its JSON is what matters.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [brand, JSON.stringify(spec), step, dropped, breakdown],
  );
  useEffect(() => {
    load(0);
  }, [load]);
  const title = dropped ? `Stopped before ${labels[step - 1]}` : `Reached ${labels[step - 1]}`;
  const lastTime = useMemo(() => (p: Person) => p.times[p.times.length - 1], []);
  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-lg">
        <SheetHeader>
          <SheetTitle>{title}</SheetTitle>
          <SheetDescription>
            {dropped ? `They did step ${step - 1} but not step ${step} in time.` : `They did every step up to step ${step}.`}
            {breakdown !== undefined && ` Only ${breakdown || "(none)"}.`}
          </SheetDescription>
        </SheetHeader>
        <div className="px-4 pb-6">
          {canSave && list && list.length > 0 && (
            <div className="mb-3 rounded-md border bg-muted/40 p-3 text-[13px]">
              {savedList ? (
                <p>
                  <a className="font-medium hover:underline" href={appPath(ws, "lists", savedList.id)}>Open the list</a>: {whole(savedList.added)} added
                  {savedList.skipped > 0 && <span className="text-muted-foreground">, {whole(savedList.skipped)} visitors not signed in skipped</span>}.
                </p>
              ) : (
                <>
                  <div className="font-medium">Save as a list</div>
                  <p className="mt-0.5 text-[12px] text-muted-foreground">A static list of everyone here with a record, to mail, score or compare. Visitors not signed in have no record and are left out.</p>
                  <div className="mt-2 flex gap-2">
                    <input aria-label="List name" className={`${field} min-w-0 flex-1`} placeholder={title} value={listName} onChange={(e) => setListName(e.target.value)} maxLength={200} />
                    <Button disabled={saving || !listName.trim()} onClick={saveList}>{saving ? "Saving…" : "Save"}</Button>
                  </div>
                </>
              )}
            </div>
          )}
          {error && <ErrorText>{error}</ErrorText>}
          {!list && !error && <Loading />}
          {list && list.length === 0 && <p className="text-[13px] text-muted-foreground">Nobody.</p>}
          {list && list.length > 0 && (
            <ul className="divide-y">
              {list.map((p) => (
                <li key={p.record_id ?? p.anonymous_id} className="flex items-center justify-between gap-3 py-2 text-[13px]">
                  <div className="min-w-0">
                    {p.record_id ? (
                      <a className="font-medium hover:underline" href={appPath(ws, "people", p.record_id)}>{p.name || p.email || "Unnamed person"}</a>
                    ) : (
                      <a className="hover:underline" href={appPath(ws, "visitors", p.anonymous_id ?? "")}>Visitor not signed in</a>
                    )}
                    {p.record_id && p.email && p.name && <div className="truncate text-[12px] text-muted-foreground">{p.email}</div>}
                    {p.breakdown && <div className="truncate text-[12px] text-muted-foreground">{p.breakdown}</div>}
                  </div>
                  <span className="shrink-0 font-mono text-[11px] text-muted-foreground" title={stamp(lastTime(p))}>
                    step {p.times.length} · {ago(lastTime(p))}
                  </span>
                </li>
              ))}
            </ul>
          )}
          {more && list && (
            <Button variant="ghost" className="mt-3 w-full" onClick={() => load(list.length)}>Show more</Button>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}

// Exclusions drop from the whole funnel everyone who did something
// between two steps, such as contacting support between signing up and
// paying.
function Exclusions({ spec, catalog, onChange }: { spec: Spec; catalog: Catalog; onChange: (s: Spec) => void }) {
  const list = spec.exclusions ?? [];
  const set = (i: number, x: Exclusion) => onChange({ ...spec, exclusions: list.map((y, j) => (j === i ? x : y)) });
  const n = spec.steps.length;
  return (
    <div className="mt-4 border-t pt-3">
      <div className="flex items-center justify-between">
        <span className="text-[13px] font-medium">Leave out people who…</span>
        {list.length < 5 && (
          <button className="text-[12px] text-muted-foreground hover:text-foreground" onClick={() => onChange({ ...spec, exclusions: [...list, { kind: "event", event: "", from: 1, to: Math.min(2, n) }] })}>+ Exclusion</button>
        )}
      </div>
      {list.length === 0 && <p className="mt-1 text-[12px] text-muted-foreground">Nobody is left out. Add one to drop, say, everyone who contacted support between signing up and paying.</p>}
      <ul className="mt-2 space-y-2">
        {list.map((x, i) => (
          <li key={i} className="rounded-md border bg-background p-2 text-[13px]">
            <div className="flex items-center gap-1.5">
              <select aria-label={`Exclusion ${i + 1} kind`} className={`${field} min-w-0 flex-1`} value={x.kind} onChange={(e) => set(i, { kind: e.target.value as Exclusion["kind"], from: x.from, to: x.to })}>
                {(Object.keys(kindLabels) as Kind[]).map((k) => (
                  <option key={k} value={k}>{kindLabels[k]}</option>
                ))}
              </select>
              <button aria-label={`Remove exclusion ${i + 1}`} className="px-1 text-muted-foreground hover:text-foreground" onClick={() => onChange({ ...spec, exclusions: list.filter((_, j) => j !== i) })}>×</button>
            </div>
            {x.kind === "event" && (
              <input aria-label={`Exclusion ${i + 1} event`} list="exclusion-events" className={`${field} mt-1.5 w-full font-mono`} placeholder="support_contacted" value={x.event ?? ""} onChange={(e) => set(i, { ...x, event: e.target.value.trim() })} />
            )}
            {x.kind === "conversion" && (
              <select aria-label={`Exclusion ${i + 1} conversion`} className={`${field} mt-1.5 w-full`} value={x.conversion ?? ""} onChange={(e) => set(i, { ...x, conversion: e.target.value })}>
                <option value="">Pick a conversion</option>
                {catalog.conversions.map((c) => (
                  <option key={c.key} value={c.key}>{c.name}</option>
                ))}
              </select>
            )}
            <div className="mt-1.5 flex items-center gap-1.5 text-[12px]">
              <span>between step</span>
              <select aria-label="From step" className={`${field} h-7`} value={x.from} onChange={(e) => { const from = Number(e.target.value); set(i, { ...x, from, to: Math.max(x.to, from + 1) }); }}>
                {Array.from({ length: n - 1 }, (_, k) => <option key={k} value={k + 1}>{k + 1}</option>)}
              </select>
              <span>and step</span>
              <select aria-label="To step" className={`${field} h-7`} value={x.to} onChange={(e) => set(i, { ...x, to: Number(e.target.value) })}>
                {Array.from({ length: n - x.from }, (_, k) => <option key={k} value={x.from + k + 1}>{x.from + k + 1}</option>)}
              </select>
            </div>
          </li>
        ))}
      </ul>
      <datalist id="exclusion-events">
        {catalog.events.map((e) => (
          <option key={e.name} value={e.name} />
        ))}
      </datalist>
    </div>
  );
}

// CorrelationCard asks, on request, what the people who finished did
// differently from those who dropped off.
function CorrelationCard({ brand, spec, labels }: { brand: string; spec: Spec; labels: string[] }) {
  const [res, setRes] = useState<Correlation | null>(null);
  const [step, setStep] = useState(labels.length);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [asked, setAsked] = useState(false);
  async function load(target: number) {
    setBusy(true);
    setAsked(true);
    track("ui.funnel_correlation_opened", { step: target });
    const { data, error } = await api.POST("/v1/brands/{brand}/insights/funnel/correlation", { params: { path: { brand } }, body: { funnel: spec, step: target } });
    setBusy(false);
    if (!data) return setError(errorMessage(error));
    setError("");
    setRes(data);
  }
  const Items = ({ items, what }: { items: Correlation["positive"]; what: string }) => (
    <div>
      <div className="text-[12px] font-medium text-muted-foreground">{what}</div>
      {items.length === 0 ? (
        <p className="mt-1 text-[12px] text-muted-foreground">Nothing stands out.</p>
      ) : (
        <ul className="mt-1 divide-y">
          {items.map((it) => (
            <li key={it.kind + it.name} className="flex items-center justify-between gap-3 py-1.5 text-[13px]">
              <span className={`min-w-0 truncate ${it.kind === "event" ? "font-mono" : ""}`} title={it.name}>{it.kind === "page" ? "Viewed " : it.kind === "property" ? "Is " : ""}{it.name}</span>
              <span className="shrink-0 text-right text-[12px] tabular-nums text-muted-foreground" title={`${whole(it.converted)} who finished, ${whole(it.dropped)} who dropped off`}>
                {pct(it.converted_rate)} vs {pct(it.dropped_rate)} · ×{it.odds_ratio >= 10 ? Math.round(it.odds_ratio) : it.odds_ratio.toFixed(1)}
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
  return (
    <Card
      title="What did people who finished do differently?"
      description="Compares the events and pages of the people who reached a step with those who didn't, between step 1 and that step. A share of each group, and how many times more likely finishing was."
      action={
        <div className="flex items-center gap-2">
          <select aria-label="Step reached" className={`${field} h-8`} value={step} onChange={(e) => setStep(Number(e.target.value))}>
            {labels.slice(1).map((l, i) => <option key={i} value={i + 2}>{l}</option>)}
          </select>
          <Button variant="ghost" disabled={busy} onClick={() => load(step)}>{busy ? "Comparing…" : asked ? "Compare again" : "Compare"}</Button>
        </div>
      }
    >
      {error && <ErrorText>{error}</ErrorText>}
      {!asked && !error && <p className="text-[13px] text-muted-foreground">Press Compare. It runs over every event and page view, so it can take a few seconds.</p>}
      {res && (
        <div className="space-y-3">
          <p className="text-[12px] text-muted-foreground">{whole(res.converted)} reached {res.label}, {whole(res.dropped)} didn't{res.skipped > 0 ? `; ${whole(res.skipped)} ${res.skipped === 1 ? "thing" : "things"} too few people did to count` : ""}.</p>
          <div className="grid gap-4 md:grid-cols-2">
            <Items items={res.positive} what="Went with finishing" />
            <Items items={res.negative} what="Went with dropping off" />
          </div>
        </div>
      )}
    </Card>
  );
}
