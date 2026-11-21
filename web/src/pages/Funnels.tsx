import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, Empty, ErrorText, field, Loading, PageHeader, toast } from "../ui";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { PickBrand } from "@/components/pick-brand";
import { useBrand } from "@/lib/brand";
import { appPath } from "@/lib/paths";
import { nameItem } from "@/lib/records";
import { track } from "@/lib/analytics";
import { ago, stamp } from "../format";
import { money } from "./Transactions";
import { embedSpec, type InsightEmbed } from "@/lib/insight-embed";
import { InsightBar, InsightList, partName, InsightMissing, draftHref, useInsightEditor, useInsightRoute } from "@/lib/insight-editor";
import { ChevronDown, ChevronUp, GripVertical, TriangleAlert } from "lucide-react";
import { peekDraft, takeDraft } from "@/components/insight-panels";
import { TimeChart, type TimeSeries } from "@/components/time-chart";
import { HBars, type Part } from "@/components/share-charts";

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
// What a data check found on one step (POST …/insights/funnel/checks).
export type Check = Schemas["FunnelCheck"];
type StepData = Schemas["FunnelStepData"];
// Every step looked at on its own, and what the checks found.
export type Checks = { steps: StepData[]; data: Check[] };
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
  product_key: "Product",
  product_category: "Product category",
  currency: "Currency",
};

export const label = (f: string): string => {
  if (fieldLabels[f]) return fieldLabels[f];
  if (f.startsWith("param:")) return `Address parameter ${f.slice(6)}`;
  if (f.startsWith("property:")) return f.slice(9);
  if (f.startsWith("person:")) return f.slice(7).replace(/_/g, " ");
  if (f.startsWith("company:")) return `Company ${f.slice(8).replace(/_/g, " ")}`;
  const m = /^step:(\d+):(.*)$/.exec(f);
  return m ? `${label(m[2])} at step ${m[1]}` : f;
};

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
      if (!out.optional || i === 0 || i === s.steps.length - 1 || s.order === "any") delete out.optional;
      return out;
    }),
    exclusions: s.exclusions?.length && s.order !== "any" ? s.exclusions.map((x) => ({ ...x, filters: x.filters?.length ? x.filters : undefined })) : undefined,
    by: s.by === "company" ? "company" : undefined,
    order: s.order === "any" ? "any" : undefined,
  };
}

// renumber puts the steps in a new order (order holds each step's old
// place, and may leave some out) and moves what points at a step by its
// number with it: a time limit's "of step", a breakdown by a step's
// property, and exclusions between two steps. reset lists the steps whose
// time limit pointed at a step that is no longer before them; those now
// count from the step before.
export function renumber(s: Spec, order: number[]): { spec: Spec; reset: number[] } {
  const at = new Map(order.map((old, i) => [old + 1, i + 1]));
  const reset: number[] = [];
  const steps = order.map((old, i) => {
    const st = s.steps[old];
    const from = st.within?.from;
    if (!st.within || from === undefined) return st;
    const to = at.get(from);
    if (to !== undefined && to < i + 1 && !s.steps[from - 1]?.optional) return { ...st, within: { value: st.within.value, unit: st.within.unit, ...(to === i ? {} : { from: to }) } };
    reset.push(i + 1);
    return { ...st, within: { value: st.within.value, unit: st.within.unit } };
  });
  let breakdown = s.breakdown;
  const m = /^step:(\d+):(.*)$/.exec(breakdown ?? "");
  if (m) {
    const to = at.get(Number(m[1]));
    breakdown = to ? `step:${to}:${m[2]}` : undefined;
  } else if (breakdown && !breakdown.startsWith("person:") && order[0] !== 0) {
    breakdown = undefined; // a property of the old first step
  }
  const exclusions = s.exclusions?.filter((x) => x.to <= steps.length && !steps[x.from - 1]?.optional && !steps[x.to - 1]?.optional);
  return { spec: { ...s, steps, breakdown, exclusions: exclusions?.length ? exclusions : undefined }, reset };
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

// stepsLine is a funnel in one line, for the list: its steps in order.
const stepsLine = (s: Spec) => (s.steps ?? []).map((st) => partName(st) || "…").join(" → ");

export function Funnels({ ws, canManage, item, onOpen }: { ws: string; canManage: boolean; item?: string; onOpen?: (id?: string) => void }) {
  const { brand, active } = useBrand(ws);
  useEffect(() => nameItem("funnels", "Funnels"), []);
  const route = useInsightRoute(item, onOpen, peekDraft() === "funnel");
  return (
    <div className="space-y-6">
      <PageHeader
        title="Funnels"
        description="Follow people from an ad visit to sign-up to payment. Only people who did the first step count after it, so you see exactly where they drop off."
      />
      {!brand ? (
        <PickBrand what="Funnels" brands={active.length} />
      ) : route.listing ? (
        <InsightList key={brand.id} brand={brand.id} kind="funnel" what="funnel" plural="funnels" canManage={canManage} onOpen={onOpen} column="Steps" describe={(i) => stepsLine(i.spec as Spec)} empty="A funnel follows people through steps you pick, such as ad click, sign-up, payment, and shows where they drop off. Build one and save it to open it again." />
      ) : (
        <FunnelsFor key={brand.id} ws={ws} brand={brand.id} canManage={canManage} item={route.item} onOpen={onOpen} />
      )}
    </div>
  );
}

// startingAt is this funnel from step i (from 0) on, with that step as
// everyone's start.
function startingAt(spec: Spec, i: number): Spec {
  const cut = renumber(spec, spec.steps.map((_, j) => j).slice(i)).spec;
  const { optional: _o, within: _w, ...first } = cut.steps[0];
  return clean({ ...cut, steps: [first, ...cut.steps.slice(1)] });
}

// FromStep is a funnel's tab for the journey from one of its steps on:
// that step is where everyone starts, so nothing above it can lower the
// numbers.
function FromStep({ brand, spec, from }: { brand: string; spec: Spec; from: number }) {
  const cut = useMemo(() => startingAt(spec, from), [spec, from]);
  const [result, setResult] = useState<Result | null>(null);
  const [checks, setChecks] = useState<Checks | null>(null);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    let live = true;
    setRunning(true);
    (async () => {
      const { data, error } = await api.POST("/v1/brands/{brand}/insights/funnel", { params: { path: { brand } }, body: cut });
      if (!live) return;
      setRunning(false);
      if (!data) return setError(errorMessage(error));
      setError("");
      setResult(data);
      const found = await api.POST("/v1/brands/{brand}/insights/funnel/checks", { params: { path: { brand } }, body: { funnel: cut } });
      if (live) setChecks(found.data ?? null);
    })();
    return () => {
      live = false;
    };
  }, [brand, cut]);
  const href = draftHref(cut);
  return (
    <>
      <p className="text-[12px] text-muted-foreground">
        The same funnel, starting at step {from + 1}: everyone who did that step counts, whatever they did before it, so nothing above it can lower these numbers.{" "}
        {href && <a className="font-medium underline underline-offset-2 hover:text-foreground" href={href} target="_blank" rel="noreferrer">Open it as a funnel of its own</a>}
        {href && " to change it, save it or see the people behind a step."}
      </p>
      {error && <ErrorText>{error}</ErrorText>}
      {!result && !error && <Loading />}
      {result && <FunnelChart result={result} running={running} checks={checks ?? undefined} off={from} />}
    </>
  );
}

export function FunnelsFor({ ws, brand, canManage, embed, item, onOpen }: { ws: string; brand: string; canManage: boolean; embed?: InsightEmbed; item?: string; onOpen?: (id?: string) => void }) {
  const [catalog, setCatalog] = useState<Catalog | null>(null);
  const ed = useInsightEditor<Spec>({ brand, kind: "funnel", starter, clean, embed, item, onOpen, initial: () => takeDraft<Spec>("funnel"), embedSpec: () => embedSpec<Spec>(embed!, starter) });
  const { spec, setSpec } = ed;
  const [result, setResult] = useState<Result | null>(null);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState("");
  const [people, setPeople] = useState<{ step: number; dropped: boolean; breakdown?: string } | null>(null);
  const [drag, setDrag] = useState<{ from: number; over: number } | null>(null);
  const [checks, setChecks] = useState<Checks | null>(null);
  // A long funnel shows its steps one line each, with one open to edit,
  // so the whole journey fits beside the chart.
  const [open, setOpen] = useState<number | null>(null);
  const compact = spec.steps.length > compactFrom;
  // The steps (from 0) opened as tabs of their own, and the one showing
  // (null is the whole funnel). They close when the steps change places.
  const [tabs, setTabs] = useState<number[]>([]);
  const [tab, setTab] = useState<number | null>(null);
  const shape = spec.steps.length;
  useEffect(() => {
    setTabs([]);
    setTab(null);
  }, [shape, item]);
  const openFrom = (i: number) => {
    track("ui.funnel_opened_from_step", {});
    setTabs((t) => (t.includes(i) ? t : [...t, i].sort((a, b) => a - b)));
    setTab(i);
    window.scrollTo({ top: 0 });
  };
  const run = useRef(0);

  useEffect(() => {
    api.GET("/v1/brands/{brand}/insights/catalog", { params: { path: { brand } } }).then(({ data, error }) => (data ? setCatalog(data) : setError(errorMessage(error))));
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
      // The data checks come after the chart, so they never hold it up.
      if (clean(spec).order === "any") return setChecks(null);
      const found = await api.POST("/v1/brands/{brand}/insights/funnel/checks", { params: { path: { brand } }, body: { funnel: clean(spec) } });
      if (n !== run.current) return;
      setChecks(found.data ?? null);
    }, 400);
    return () => clearTimeout(t);
  }, [brand, spec]);

  const setStep = (i: number, st: Step) => setSpec((s) => ({ ...s, steps: s.steps.map((x, j) => (j === i ? st : x)) }));
  const removeStep = (i: number) => { setOpen(null); setSpec((s) => renumber(s, s.steps.map((_, j) => j).filter((j) => j !== i)).spec); };
  // Makes a step optional or required. Nothing may count from an optional
  // step, so limits and exclusions that did are changed, and the page says so.
  const setOptional = (i: number, optional: boolean) => {
    const limits = optional ? spec.steps.filter((st) => st.within?.from === i + 1).length : 0;
    const dropped = optional ? (spec.exclusions ?? []).filter((x) => x.from === i + 1 || x.to === i + 1).length : 0;
    setSpec((s) => {
      const steps = s.steps.map((st, j) => (j === i ? { ...st, optional: optional || undefined } : optional && st.within?.from === i + 1 ? { ...st, within: { value: st.within.value, unit: st.within.unit } } : st));
      const exclusions = optional ? s.exclusions?.filter((x) => x.from !== i + 1 && x.to !== i + 1) : s.exclusions;
      return { ...s, steps, exclusions: exclusions?.length ? exclusions : undefined };
    });
    track("ui.funnel_step_optional_set", { optional });
    if (limits) toast(`A time limit that counted from step ${i + 1} now counts from the step before it, because not everyone does an optional step.`);
    if (dropped) toast(`Removed ${dropped === 1 ? "an exclusion" : `${dropped} exclusions`} that ran from or to step ${i + 1}: an exclusion needs required steps.`);
  };
  const addStep = () => { setOpen(spec.steps.length); setSpec((s) => ({ ...s, steps: [...s.steps, { kind: "event", event: "" }] })); };
  // Moves a step, and says so when a time limit had to change with it.
  const moveStep = (from: number, to: number) => {
    if (from === to || to < 0 || to >= spec.steps.length) return;
    const order = spec.steps.map((_, j) => j);
    order.splice(to, 0, order.splice(from, 1)[0]);
    const moved = renumber(spec, order);
    setSpec(moved.spec);
    track("ui.funnel_step_moved", { from: from + 1, to: to + 1 });
    if (moved.reset.length) toast(`Step ${moved.reset.join(" and ")}: the time limit now counts from the step before it, because the step it pointed at is no longer earlier.`);
  };

  if (!catalog || ed.loading) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  if (ed.missing) return <InsightMissing ed={ed} what="funnel" />;

  return (
    <div className="grid gap-6 xl:grid-cols-[380px_minmax(0,1fr)]">
      <div className="xl:col-span-2">
        <InsightBar ed={ed} what="funnel" all="All funnels" canManage={canManage} canSave={ready(spec)} placeholder="Google ads to first purchase" deleteWarning="The funnel goes for everyone. The data it shows stays." embed={!!embed} />
      </div>
      <div className="min-w-0 space-y-4">
        <Card title="Steps" description={spec.order === "any" ? "People do them in any order; the chart counts how many of the steps they got through." : "People must do them in this order."}>
          <ol className={compact ? "space-y-1.5" : "space-y-3"}>
            {spec.steps.map((st, i) => compact && open !== i ? (
              <StepRow
                key={i}
                n={i + 1}
                step={st}
                catalog={catalog}
                warn={!!checks?.data.some((c) => c.step === i + 1)}
                onOpen={() => setOpen(i)}
                onUp={i > 0 ? () => moveStep(i, i - 1) : undefined}
                onDown={i < spec.steps.length - 1 ? () => moveStep(i, i + 1) : undefined}
              />
            ) : (
              <StepEditor
                key={i}
                n={i + 1}
                step={st}
                catalog={catalog}
                onChange={(s) => setStep(i, s)}
                onRemove={spec.steps.length > 2 ? () => removeStep(i) : undefined}
                onDone={compact ? () => setOpen(null) : undefined}
                optionalSteps={spec.steps.flatMap((x, j) => (x.optional ? [j + 1] : []))}
                onOptional={spec.order !== "any" && i > 0 && i < spec.steps.length - 1 ? (o) => setOptional(i, o) : undefined}
                onUp={i > 0 ? () => moveStep(i, i - 1) : undefined}
                onDown={i < spec.steps.length - 1 ? () => moveStep(i, i + 1) : undefined}
                drag={{
                  dragging: drag?.from === i,
                  over: !!drag && drag.over === i && drag.from !== i,
                  onStart: () => setDrag({ from: i, over: i }),
                  onOver: () => setDrag((d) => (d && d.over !== i ? { ...d, over: i } : d)),
                  onDrop: () => { if (drag) moveStep(drag.from, i); setDrag(null); },
                  onEnd: () => setDrag(null),
                }}
              />
            ))}
          </ol>
          {spec.steps.length < maxSteps ? (
            <Button variant="ghost" className="mt-3 w-full" onClick={addStep}>Add a step</Button>
          ) : (
            <p className="mt-3 text-[12px] text-muted-foreground">A funnel has at most {maxSteps} steps.</p>
          )}
          <p className="mt-2 text-[12px] text-muted-foreground">{compact ? "Click a step to change it. Use the arrows to change the order." : "Drag a step by its handle, or use the arrows, to change the order."}</p>
          {spec.order !== "any" && <Exclusions spec={spec} catalog={catalog} onChange={setSpec} />}
        </Card>

        <Card title="Options">
          <Options spec={spec} catalog={catalog} onChange={setSpec} />
        </Card>

      </div>

      <div className="min-w-0 space-y-4">
        {error && <ErrorText>{error}</ErrorText>}
        {!ready(spec) && <Empty title="Finish the steps">Each event step needs an event, each conversion step a saved conversion, and each filter a value.</Empty>}
        {ready(spec) && !result && <Loading />}
        {ready(spec) && result && tabs.length > 0 && (
          <div role="tablist" aria-label="Parts of this funnel" className="flex flex-wrap items-center gap-1 border-b">
            <button role="tab" aria-selected={tab === null} onClick={() => setTab(null)} className={`-mb-px border-b-2 px-3 py-1.5 text-[13px] ${tab === null ? "border-foreground font-medium" : "border-transparent text-muted-foreground hover:text-foreground"}`}>Whole funnel</button>
            {tabs.map((i) => (
              <span key={i} className={`-mb-px inline-flex items-center border-b-2 ${tab === i ? "border-foreground font-medium" : "border-transparent text-muted-foreground"}`}>
                <button role="tab" aria-selected={tab === i} onClick={() => setTab(i)} className="max-w-[16rem] truncate py-1.5 pl-3 pr-1 text-[13px] hover:text-foreground">From step {i + 1}: {result.steps[i]?.label}</button>
                <button aria-label={`Close the tab from step ${i + 1}`} className="px-1.5 text-muted-foreground hover:text-foreground" onClick={() => { setTabs((t) => t.filter((x) => x !== i)); if (tab === i) setTab(null); }}>×</button>
              </span>
            ))}
          </div>
        )}
        {ready(spec) && result && tab !== null && <FromStep key={tab} brand={brand} spec={spec} from={tab} />}
        {ready(spec) && result && tab === null && (
          <>
            {spec.by === "company" && <p className="text-[12px] text-muted-foreground">Counting companies: a company reaches a step when any of its people does. Visitors nobody has identified belong to no company and are left out.</p>}
            <FunnelChart result={result} running={running} checks={checks ?? undefined} onFrom={embed || spec.order === "any" ? undefined : openFrom} onOptional={spec.order !== "any" ? (i) => { setOptional(i, true); toast(`Step ${i + 1} is now optional: people who skip it still count at the next step.`); } : undefined} adClicks={{ on: !!spec.ad_clicks, set: (on) => { track("ui.funnel_ad_clicks_switched", { on }); setSpec((s) => ({ ...s, ad_clicks: on ? { network: networkOf(s) } : undefined })); } }} onPeople={(step, dropped) => { track("ui.funnel_people_opened", { dropped }); setPeople({ step, dropped }); }} />
            {spec.breakdown && result.breakdown.length > 0 && (
              <BreakdownTable result={result} breakdown={spec.breakdown} onPeople={(step, value) => { track("ui.funnel_people_opened", { dropped: false, breakdown: true }); setPeople({ step, dropped: false, breakdown: value }); }} />
            )}
            {spec.interval && result.over_time.length > 0 && <OverTime result={result} interval={spec.interval} />}
            <TimeToConvert result={result} />
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

// A funnel with more steps than this shows them one line each.
const compactFrom = 6;

// unknownEvent is true for an event step whose event hasn't arrived in the
// 90 days the catalog covers: usually a typo, or a system that isn't sending.
function unknownEvent(step: Step, catalog: Catalog): boolean {
  return step.kind === "event" && !!step.event && catalog.events.length > 0 && !catalog.events.some((e) => e.name === step.event);
}

// StepRow is a step on one line: what it is, and whether anything about it
// needs a look. Clicking it opens the full editor.
function StepRow({ n, step, catalog, warn, onOpen, onUp, onDown }: { n: number; step: Step; catalog: Catalog; warn: boolean; onOpen: () => void; onUp?: () => void; onDown?: () => void }) {
  const name = step.kind === "event" ? step.event || "Pick an event" : step.kind === "conversion" ? catalog.conversions.find((c) => c.key === step.conversion)?.name ?? "Pick a conversion" : kindLabels[step.kind];
  const extras = [step.optional ? "optional" : "", step.first ? "first time only" : "", step.filters?.length ? `${step.filters.length} filter${step.filters.length > 1 ? "s" : ""}` : "", step.within ? `within ${step.within.value} ${step.within.unit}${step.within.value > 1 ? "s" : ""}` : ""].filter(Boolean).join(" · ");
  const flagged = warn || unknownEvent(step, catalog);
  return (
    <li className="flex items-center gap-2 rounded-md border bg-background px-2.5 py-1.5">
      <span className="w-4 shrink-0 font-mono text-[11px] font-medium text-muted-foreground">{n}</span>
      <button type="button" aria-label={`Edit step ${n}`} title="Click to change this step" onClick={onOpen} className="min-w-0 flex-1 text-left">
        <span className={`block truncate text-[13px] ${step.kind === "event" ? "font-mono" : ""}`}>{name}</span>
        {extras && <span className="block truncate text-[11px] text-muted-foreground">{extras}</span>}
      </button>
      {flagged && <span title={warn ? "Check this step's data: see the note on the right." : "Nothing with this name has arrived in the last 90 days."}><TriangleAlert aria-label="Check this step's data" className="size-3.5 shrink-0 text-amber-600 dark:text-amber-400" /></span>}
      <span className="flex shrink-0">
        <button aria-label={`Move step ${n} up`} title="Move up" disabled={!onUp} className="rounded p-0.5 text-muted-foreground hover:bg-accent hover:text-foreground disabled:opacity-30" onClick={onUp}><ChevronUp className="size-4" /></button>
        <button aria-label={`Move step ${n} down`} title="Move down" disabled={!onDown} className="rounded p-0.5 text-muted-foreground hover:bg-accent hover:text-foreground disabled:opacity-30" onClick={onDown}><ChevronDown className="size-4" /></button>
      </span>
    </li>
  );
}

export type StepDrag = { dragging: boolean; over: boolean; onStart: () => void; onOver: () => void; onDrop: () => void; onEnd: () => void };

export function StepEditor({ n, step, catalog, onChange, onRemove, onUp, onDown, drag, optionalSteps, onOptional, within = true, title, onDone }: { n: number; step: Step; catalog: Catalog; onChange: (s: Step) => void; onRemove?: () => void; onUp?: () => void; onDown?: () => void; drag?: StepDrag; /** The numbers of the optional steps, which a time limit can't count from. */ optionalSteps?: number[]; /** Offers Required or Optional (a middle step of an in-order funnel). */ onOptional?: (optional: boolean) => void; within?: boolean; title?: string; /** Shows Done, which folds the step back to one line. */ onDone?: () => void }) {
  const row = useRef<HTMLLIElement>(null);
  const fields = step.kind === "visit" ? catalog.fields.visit : step.kind === "payment" ? catalog.fields.payment : catalog.fields.event;
  const filters = step.filters ?? [];
  const setFilters = (f: Filter[]) => onChange({ ...step, filters: f });
  const id = `step-${n}`;
  return (
    <li
      ref={row}
      className={`rounded-md border bg-background p-3 ${drag?.dragging ? "opacity-50" : ""} ${drag?.over ? "ring-2 ring-brand" : ""}`}
      onDragOver={drag ? (e) => { e.preventDefault(); drag.onOver(); } : undefined}
      onDrop={drag ? (e) => { e.preventDefault(); drag.onDrop(); } : undefined}
    >
      <div className="flex items-center gap-2">
        {drag && (
          <span
            draggable
            role="img"
            aria-label={`Drag step ${n} to move it`}
            title="Drag to move this step"
            className="-ml-1 cursor-grab text-muted-foreground hover:text-foreground active:cursor-grabbing"
            onDragStart={(e) => {
              e.dataTransfer.effectAllowed = "move";
              e.dataTransfer.setData("text/plain", String(n));
              if (row.current) e.dataTransfer.setDragImage(row.current, 12, 12);
              drag.onStart();
            }}
            onDragEnd={drag.onEnd}
          >
            <GripVertical className="size-4" />
          </span>
        )}
        <span className="font-mono text-[11px] font-medium text-muted-foreground">{title ?? n}</span>
        <select
          id={`${id}-kind`}
          aria-label={`Step ${n} kind`}
          className={`${field} min-w-0 flex-1`}
          value={step.kind}
          onChange={(e) => onChange({ kind: e.target.value as Kind, within: step.within, optional: step.optional })}
        >
          {(Object.keys(kindLabels) as Kind[]).map((k) => (
            <option key={k} value={k}>{kindLabels[k]}</option>
          ))}
        </select>
        {(onUp || onDown) && (
          <span className="flex shrink-0">
            <button aria-label={`Move step ${n} up`} title="Move up" disabled={!onUp} className="rounded p-0.5 text-muted-foreground hover:bg-accent hover:text-foreground disabled:opacity-30" onClick={onUp}><ChevronUp className="size-4" /></button>
            <button aria-label={`Move step ${n} down`} title="Move down" disabled={!onDown} className="rounded p-0.5 text-muted-foreground hover:bg-accent hover:text-foreground disabled:opacity-30" onClick={onDown}><ChevronDown className="size-4" /></button>
          </span>
        )}
        {onRemove && (
          <button aria-label={`Remove step ${n}`} title="Remove this step" className="px-1 text-muted-foreground hover:text-foreground" onClick={onRemove}>×</button>
        )}
        {onDone && (
          <button type="button" className="rounded border px-2 py-0.5 text-[12px] hover:bg-accent" onClick={onDone}>Done</button>
        )}
      </div>

      {onOptional && (
        <div className="mt-2">
          <div role="radiogroup" aria-label={`Step ${n} is`} className="inline-flex rounded-md border p-0.5 text-[12px]">
            {[false, true].map((opt) => (
              <button key={String(opt)} type="button" role="radio" aria-checked={!!step.optional === opt} onClick={() => onOptional(opt)} className={`rounded px-2.5 py-0.5 ${!!step.optional === opt ? "bg-accent font-medium" : "text-muted-foreground hover:text-foreground"}`}>
                {opt ? "Optional" : "Required"}
              </button>
            ))}
          </div>
          <p className="mt-1 text-[12px] text-muted-foreground">
            {step.optional ? "People who skip this step still count at the next one. The chart shows how many did it and how many skipped it." : "Only people who do this step go on to the next one."}
          </p>
        </div>
      )}

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
          {unknownEvent(step, catalog) && (
            <p className="mt-1 flex items-start gap-1.5 text-[12px] text-amber-700 dark:text-amber-300">
              <TriangleAlert className="mt-0.5 size-3.5 shrink-0" />
              Nothing called {step.event} has arrived in the last 90 days. Check the spelling, or that the system that sends it is connected.
            </p>
          )}
          <datalist id={`${id}-events`}>
            {catalog.events.map((e) => (
              <option key={e.name} value={e.name}>{`${e.label ? `${e.label} · ` : ""}${whole(e.count)} in 90 days`}</option>
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

      {within && n > 1 && <WithinEditor n={n} step={step} onChange={onChange} optionalSteps={optionalSteps} />}
    </li>
  );
}

function WithinEditor({ n, step, onChange, optionalSteps = [] }: { n: number; step: Step; onChange: (s: Step) => void; optionalSteps?: number[] }) {
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
      <div className="mt-1 flex min-w-0 flex-wrap items-center gap-1.5">
        <span>Within</span>
        <input aria-label="Time limit" type="number" min={1} className={`${field} h-7 w-14`} value={w.value} onChange={(e) => onChange({ ...step, within: { ...w, value: Math.max(1, Number(e.target.value) || 1) } })} />
        <select aria-label="Unit" className={`${field} h-7 w-[5.5rem]`} value={w.unit} onChange={(e) => onChange({ ...step, within: { ...w, unit: e.target.value as Unit } })}>
          <option value="minute">minutes</option>
          <option value="hour">hours</option>
          <option value="day">days</option>
          <option value="week">weeks</option>
        </select>
        <span className="inline-flex items-center gap-1.5 whitespace-nowrap">
          <span>of step</span>
          <select aria-label="From step" className={`${field} h-7 w-[4.5rem]`} value={w.from ?? n - 1} onChange={(e) => onChange({ ...step, within: { ...w, from: Number(e.target.value) === n - 1 ? undefined : Number(e.target.value) } })}>
            {Array.from({ length: n - 1 }, (_, i) => (
              <option key={i} value={i + 1} disabled={i + 1 !== n - 1 && optionalSteps.includes(i + 1)}>{i + 1}{i + 1 !== n - 1 && optionalSteps.includes(i + 1) ? " (optional)" : ""}</option>
            ))}
          </select>
        </span>
      </div>
      {w.from === undefined && optionalSteps.includes(n - 1) && (
        <p className="mt-1 text-muted-foreground">Step {n - 1} is optional. For people who skipped it, the limit counts from the step before.</p>
      )}
    </div>
  );
}

// breakdownParts reads a funnel's breakdown: whose field it is (a step by
// its number, or the person) and the field.
export function breakdownParts(b: string | undefined): { of: number | "person"; name: string } {
  if (!b) return { of: 1, name: "" };
  if (b.startsWith("person:")) return { of: "person", name: b.slice(7) };
  const m = /^step:(\d+):(.*)$/.exec(b);
  return m ? { of: Number(m[1]), name: m[2] } : { of: 1, name: b };
}

// BreakdownPicker picks what to split people by: a field of any step, or
// a field of the person's (or company's) own record.
function BreakdownPicker({ spec, catalog, onChange }: { spec: Spec; catalog: Catalog; onChange: (s: Spec) => void }) {
  const parts = breakdownParts(spec.breakdown);
  const [of, setOf] = useState<number | "person">(parts.of);
  const [custom, setCustom] = useState(parts.name.startsWith("property:") && parts.of !== "person");
  useEffect(() => {
    if (spec.breakdown) setOf(parts.of);
  }, [spec.breakdown]);
  const anyOrder = spec.order === "any";
  const company = spec.by === "company";
  const who = company ? "company" : "person";
  const step = of === "person" ? undefined : spec.steps[of - 1] ?? spec.steps[0];
  const stepFields = step ? catalog.breakdowns[step.kind] ?? [] : [];
  const recordFields = company ? catalog.company_fields : catalog.person_fields;
  const takesProperty = !!step && step.kind !== "visit" && step.kind !== "payment";
  const set = (name: string) => onChange({ ...spec, breakdown: !name ? undefined : of === "person" ? `person:${name}` : of === 1 ? name : `step:${of}:${name}` });
  return (
    <div className="space-y-1.5">
      <label className="block">
        <span className="text-muted-foreground">Break down by a field of</span>
        <select
          id="funnel-breakdown-of"
          className={`${field} mt-1 block w-full`}
          value={String(of)}
          onChange={(e) => { const v = e.target.value === "person" ? "person" : Number(e.target.value); setOf(v); setCustom(false); onChange({ ...spec, breakdown: undefined }); }}
        >
          {spec.steps.map((s, i) => (i === 0 || !anyOrder) && (
            <option key={i} value={i + 1}>Step {i + 1}: {s.label || kindLabels[s.kind]}</option>
          ))}
          <option value="person">The {who}</option>
        </select>
      </label>
      <label className="block">
        <span className="sr-only">Field</span>
        <select
          id="funnel-breakdown"
          aria-label="Break down by"
          className={`${field} block w-full`}
          value={custom ? "$property" : parts.of === of ? parts.name : ""}
          onChange={(e) => { if (e.target.value === "$property") { setCustom(true); onChange({ ...spec, breakdown: undefined }); } else { setCustom(false); set(e.target.value); } }}
        >
          <option value="">Nothing</option>
          {of === "person"
            ? recordFields.map((f) => <option key={f.key} value={f.key}>{f.label}</option>)
            : stepFields.map((b) => <option key={b} value={b}>{label(b)}</option>)}
          {takesProperty && <option value="$property">A property of the event…</option>}
          {of !== "person" && catalog.experiments.length > 0 && (
            <optgroup label="A/B test version">
              {catalog.experiments.map((x) => (
                <option key={x.field} value={x.field}>{x.name}</option>
              ))}
            </optgroup>
          )}
          {of !== "person" && parts.of === of && parts.name.startsWith("experiment:") && !catalog.experiments.some((x) => x.field === parts.name) && <option value={parts.name}>Test {parts.name.slice(11)}</option>}
          {of === "person" && parts.of === "person" && parts.name && !recordFields.some((f) => f.key === parts.name) && <option value={parts.name}>{parts.name}</option>}
        </select>
      </label>
      {custom && (
        <input
          aria-label="Event property"
          className={`${field} block w-full`}
          placeholder="Property name, such as plan"
          defaultValue={parts.of === of && parts.name.startsWith("property:") ? parts.name.slice(9) : ""}
          onChange={(e) => set(e.target.value.trim() ? `property:${e.target.value.trim()}` : "")}
        />
      )}
      {of === "person" && recordFields.length === 0 && <p className="text-[12px] text-muted-foreground">This brand has no {who} fields to split by yet.</p>}
      {typeof of === "number" && of > 1 && <p className="text-[12px] text-muted-foreground">People who never reached step {of} are counted under (none).</p>}
      {of === "person" && <p className="text-[12px] text-muted-foreground">The field as it is now. {company ? "" : "Visitors nobody has identified are counted under (none)."}</p>}
    </div>
  );
}

function Options({ spec, catalog, onChange }: { spec: Spec; catalog: Catalog; onChange: (s: Spec) => void }) {
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
      <BreakdownPicker spec={spec} catalog={catalog} onChange={onChange} />
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
        <select id="funnel-order" className={`${field} mt-1 block w-full`} value={spec.order ?? "strict"} onChange={(e) => onChange({ ...spec, order: e.target.value === "any" ? "any" : undefined, breakdown: e.target.value === "any" && spec.breakdown?.startsWith("step:") ? undefined : spec.breakdown })}>
          <option value="strict">In this order</option>
          <option value="any">In any order</option>
        </select>
        {spec.order === "any" && <span className="mt-1 block text-[12px] text-muted-foreground">Step 1 is whichever step a person did first; the last bar is everyone who did all of them within the window. No exclusions or per-step limits.</span>}
      </label>
      <label className="block">
        <span className="text-muted-foreground">Count</span>
        <select id="funnel-by" className={`${field} mt-1 block w-full`} value={spec.by ?? "person"} onChange={(e) => onChange({ ...spec, by: e.target.value === "company" ? "company" : undefined, breakdown: spec.breakdown?.startsWith("person:") ? undefined : spec.breakdown })}>
          <option value="person">People</option>
          <option value="company">Companies (through their people)</option>
        </select>
      </label>
      <label className="flex items-center gap-2">
        <input type="checkbox" checked={!!spec.ad_clicks} onChange={(e) => onChange({ ...spec, ad_clicks: e.target.checked ? { network: networkOf(spec) } : undefined })} />
        Show ad clicks and spend beside the chart
      </label>
      <p className="text-[12px] text-muted-foreground">A reference from your ad networks, not a step: it never changes who is counted.</p>
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

// maxSteps is the most steps a funnel can have (insights.MaxSteps).
const maxSteps = 20;

// A step's column is never narrower than this, so a long funnel scrolls
// sideways instead of squeezing its labels.
const stepMinWidth = 96;

// A funnel with more steps than this opens as a list: every step fits on
// the screen with its whole name, where bars would scroll off the side.
const listFrom = 8;

// biggestDrop is the step (from 0) that lost the most people, or -1.
function biggestDrop(steps: StepResult[]): number {
  let at = -1;
  steps.forEach((s, i) => {
    if (i > 0 && !s.optional && s.dropped > 0 && (at < 0 || s.dropped > steps[at].dropped)) at = i;
  });
  return steps.length > 2 ? at : -1;
}

// checkText says what a data check found, in words someone can act on.
function checkText(c: Check, s: StepResult): string {
  switch (c.kind) {
    case "never_received":
      return `Nothing for this step has arrived in the last 90 days. Check its name, and that the system that sends it is connected.`;
    case "stopped":
      return `This step's data stopped arriving on ${c.last_received_at ? day(c.last_received_at) : "an earlier day"}${c.daily_before ? `; before that about ${whole(c.daily_before)} a day came in` : ""}. Every number from this step on is too low until it is back.`;
    case "unlinked":
      return `${whole(c.people ?? 0)} people did this step with no visit, page view or event on record before it, so they can't be linked to the earlier steps. "${s.label}" is probably sent under a different id than the steps before it. Part of the drop here, and at every step after it, is a tracking gap, not people leaving.`;
  }
}

function CheckNote({ c, s }: { c: Check; s: StepResult }) {
  return (
    <div role="alert" className="mt-1.5 flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 px-2.5 py-1.5 text-[12px] text-amber-900 dark:text-amber-200">
      <TriangleAlert className="mt-0.5 size-3.5 shrink-0" />
      <span>{checkText(c, s)}</span>
    </div>
  );
}

// StepsList is the funnel as rows: one step a line, with its whole name,
// how many people got there, how many stopped before it and how long it
// took, so a long journey reads top to bottom without scrolling sideways.
function StepsList({ result, checks, data, onPeople, onOptional, onFrom, off = 0 }: { /** Added to every step number shown: a funnel cut from a longer one keeps that one's numbers. */ off?: number; result: Result; checks: Check[]; data?: StepData[]; onPeople?: (step: number, dropped: boolean) => void; onOptional?: (step: number) => void; onFrom?: (step: number) => void }) {
  // The steps whose own numbers are open; the ones with a problem start open.
  const [shown, setShown] = useState<Record<number, boolean>>({});
  const top = Math.max(1, result.steps[0]?.count ?? 0);
  const worst = biggestDrop(result.steps);
  return (
    <ol className="divide-y">
      {result.steps.map((s, i) => {
        const gate = gateOf(result.steps, i);
        const mine = checks.filter((c) => c.step === i + 1);
        return (
          <li key={i} className="py-2.5">
            <div className="grid grid-cols-[1.5rem_minmax(0,1fr)_auto] items-baseline gap-x-3">
              <span className="font-mono text-[11px] text-muted-foreground">{i + 1 + off}</span>
              <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-[13px] font-medium">
                <span className="break-words">{s.label}</span>
                {s.optional && <Badge>Optional</Badge>}
                {i === worst && <span className="rounded bg-red-500/15 px-1.5 py-0.5 text-[11px] font-medium text-red-700 dark:text-red-300">Biggest drop</span>}
                {mine.length > 0 && <span className="inline-flex items-center gap-1 rounded bg-amber-500/15 px-1.5 py-0.5 text-[11px] font-medium text-amber-800 dark:text-amber-200"><TriangleAlert className="size-3" />Check the data</span>}
              </span>
              <button className="text-right text-[15px] font-semibold tabular-nums hover:underline" aria-label={`${whole(s.count)} people at ${s.label}`} onClick={() => onPeople?.(i + 1, false)}>{whole(s.count)}</button>
            </div>
            <div className="mt-1.5 grid grid-cols-[1.5rem_minmax(0,1fr)] gap-x-3">
              <span />
              <div>
                <div className="flex h-2.5 overflow-hidden rounded-full bg-muted">
                  <div style={{ width: `${(s.count / top) * 100}%`, background: "var(--series-1)", opacity: s.optional ? 0.45 : 1 }} />
                  {i > 0 && s.dropped > 0 && (
                    <button
                      aria-label={`${whole(s.dropped)} people who stopped before ${s.label}`}
                      title="The people who stopped before this step"
                      onClick={() => onPeople?.(i + 1, true)}
                      className="bg-[repeating-linear-gradient(135deg,transparent,transparent_4px,var(--color-neutral-400)_4px,var(--color-neutral-400)_5px)] hover:opacity-70 dark:bg-[repeating-linear-gradient(135deg,transparent,transparent_4px,var(--color-neutral-600)_4px,var(--color-neutral-600)_5px)]"
                      style={{ width: `${(s.dropped / top) * 100}%` }}
                    />
                  )}
                </div>
                <div className="mt-1 flex flex-wrap gap-x-4 text-[12px] text-muted-foreground">
                  <span>{pct(s.conversion_from_start)} of everyone who started</span>
                  {i > 0 && <span>{pct(s.conversion_from_previous)} of step {gate + 1 + off}</span>}
                  {i > 0 && s.dropped > 0 && <button className="text-red-600 hover:underline dark:text-red-400" onClick={() => onPeople?.(i + 1, true)}>{whole(s.dropped)} stopped here</button>}
                  {s.optional && <span>{whole(s.skipped ?? 0)} went on without it</span>}
                  {i > 0 && s.median_seconds !== null && <span>usually {span(s.median_seconds)} after step {gate + 1 + off}</span>}
                </div>
                {mine.map((c, k) => <CheckNote key={k} c={c} s={s} />)}
                {data?.[i] && (() => {
                  const d = data[i];
                  const isOpen = shown[i] ?? mine.length > 0;
                  const uncounted = Math.max(0, d.people - s.count);
                  // Worth a word on the row itself: many did the step and the funnel left them out.
                  const many = i > 0 && uncounted >= 20 && uncounted >= d.people * 0.1;
                  const canOptional = !!onOptional && mine.length > 0 && !s.optional && i > 0 && i < result.steps.length - 1;
                  // This step as the start of a funnel of its own: the next step's pair.
                  const next = data[i + 1]?.from_previous;
                  const fresh = next?.started ? next.reached / next.started : 0;
                  const inFunnel = s.count ? (result.steps[i + 1]?.count ?? 0) / s.count : 0;
                  return (
                    <div className="mt-1.5 text-[12px]">
                      <button type="button" aria-expanded={isOpen} className="text-muted-foreground underline decoration-dotted underline-offset-2 hover:text-foreground" onClick={() => { if (!isOpen) track("ui.funnel_step_checked", {}); setShown((x) => ({ ...x, [i]: !isOpen })); }}>
                        {isOpen ? "Hide this step's own numbers" : many ? `Check this step: ${whole(uncounted)} more people did it than are counted here` : "Check this step"}
                      </button>
                      {isOpen && (
                        <div className="mt-1.5 rounded-md border bg-muted/40 px-3 py-2">
                          {next && (
                            <>
                              <div className="font-medium">Starting fresh at this step</div>
                              <p className="text-muted-foreground">As if the funnel began here: everyone who did this step counts, whatever they did before it, so a tracking problem higher up can't lower these numbers.</p>
                              <dl className="mt-1.5 grid grid-cols-[minmax(0,1fr)_auto] gap-x-4 gap-y-1">
                                <dt className="text-muted-foreground">Did this step in these dates</dt>
                                <dd className="text-right font-medium tabular-nums">{whole(next.started)}</dd>
                                <dt className="text-muted-foreground">Went on to step {i + 2 + off}</dt>
                                <dd className="text-right font-medium tabular-nums">{whole(next.reached)} ({pct(fresh)})</dd>
                                <dt className="text-muted-foreground">In this funnel, for comparison: {whole(s.count)} counted at this step</dt>
                                <dd className="text-right font-medium tabular-nums">{whole(result.steps[i + 1]?.count ?? 0)} ({pct(inFunnel)})</dd>
                              </dl>
                              {onFrom && (
                                <button type="button" className="mt-1.5 font-medium underline underline-offset-2 hover:text-foreground" onClick={() => onFrom(i)}>
                                  Open a funnel that starts at this step
                                </button>
                              )}
                              <div className="mt-2 border-t pt-2 font-medium">Compared with this funnel</div>
                            </>
                          )}
                          <dl className="grid grid-cols-[minmax(0,1fr)_auto] gap-x-4 gap-y-1">
                            {!next && (
                              <>
                                <dt className="text-muted-foreground">Everyone who did this step in these dates</dt>
                                <dd className="text-right font-medium tabular-nums">{whole(d.people)}</dd>
                              </>
                            )}
                            <dt className="text-muted-foreground">Counted in this funnel{i > 0 ? `, because they also did step${gate > 0 ? "s" : ""} ${gate > 0 ? `${1 + off} to ${gate + 1 + off}` : `${1 + off}`} first` : ""}</dt>
                            <dd className="text-right font-medium tabular-nums">{whole(s.count)}</dd>
                            {i > 0 && (
                              <>
                                <dt className={uncounted > 0 ? "" : "text-muted-foreground"}>Did this step but are not counted{d.nothing_before ? ` (${whole(Math.min(d.nothing_before, uncounted))} of them have no visit, page view or event on record before it)` : ""}</dt>
                                <dd className="text-right font-medium tabular-nums">{whole(uncounted)}</dd>
                              </>
                            )}
                            {d.from_previous && (
                              <>
                                <dt className="text-muted-foreground">Of everyone who did step {i + off}, how many then did this step</dt>
                                <dd className="text-right font-medium tabular-nums">{whole(d.from_previous.reached)} of {whole(d.from_previous.started)} ({pct(d.from_previous.started ? d.from_previous.reached / d.from_previous.started : 0)})</dd>
                              </>
                            )}
                            <dt className="text-muted-foreground">Data last arrived</dt>
                            <dd className="text-right font-medium tabular-nums">{d.last_received_at ? `${ago(d.last_received_at)} · about ${whole(d.per_day)} a day` : "never"}</dd>
                          </dl>
                          {canOptional && (
                            <div className="mt-2 flex flex-wrap items-center gap-2 border-t pt-2">
                              <Button variant="ghost" className="border" onClick={() => onOptional?.(i)}>Make this step optional</Button>
                              <span className="text-muted-foreground">People who skip it then still count at the next step, so one broken step stops lowering every step after it.</span>
                            </div>
                          )}
                        </div>
                      )}
                    </div>
                  );
                })()}
              </div>
            </div>
          </li>
        );
      })}
    </ol>
  );
}

export function FunnelChart({ result, running, onPeople, adClicks, checks: all, onOptional, onFrom, off = 0 }: { /** Added to every step number shown. */ off?: number; result: Result; running: boolean; onPeople?: (step: number, dropped: boolean) => void; adClicks?: { on: boolean; set: (on: boolean) => void }; /** Each step on its own and what the data checks found. */ checks?: Checks; /** Makes a step (from 0) optional. */ onOptional?: (step: number) => void; /** Opens the funnel from a step (from 0) on. */ onFrom?: (step: number) => void }) {
  const checks = all?.data ?? [];
  // The numbers of the steps a check found something on.
  const flagged = [...new Set(checks.map((c) => c.step + off))];
  // Bars for a short funnel, a list for a long one, until the person picks.
  const [picked, setPicked] = useState<"bars" | "list" | null>(null);
  const view = picked ?? (result.steps.length > listFrom ? "list" : "bars");
  const worst = biggestDrop(result.steps);
  const ad = result.ad_clicks;
  // The ad clicks bar has a scale of its own, so it never squeezes the steps.
  const top = Math.max(1, result.steps[0]?.count ?? 0);
  const adTop = Math.max(top, ad?.clicks ?? 0);
  const last = result.steps[result.steps.length - 1];
  const columns = `${ad ? "minmax(0, 0.7fr) " : ""}repeat(${result.steps.length}, minmax(0, 1fr))`;
  const minWidth = Math.max(520, (result.steps.length + (ad ? 1 : 0)) * stepMinWidth);
  // A step nobody reached after a step people did reach: often the two are the wrong way round.
  const empty = result.steps.findIndex((s, i) => i > 0 && !s.optional && s.count === 0 && result.steps[gateOf(result.steps, i)].count > 0);
  return (
    <Card
      action={
        <span className="flex flex-wrap items-center justify-end gap-3">
          {adClicks && (
            <label className="flex cursor-pointer items-center gap-2 text-[12px] text-muted-foreground">
              <input type="checkbox" role="switch" aria-checked={adClicks.on} checked={adClicks.on} onChange={(e) => adClicks.set(e.target.checked)} />
              Show ad clicks and spend
            </label>
          )}
          <span role="radiogroup" aria-label="Show the steps as" className="inline-flex rounded-md border p-0.5 text-[12px]">
            {(["list", "bars"] as const).map((v) => (
              <button key={v} type="button" role="radio" aria-checked={view === v} onClick={() => { track("ui.funnel_view_switched", { view: v }); setPicked(v); }} className={`rounded px-2.5 py-0.5 ${view === v ? "bg-accent font-medium" : "text-muted-foreground hover:text-foreground"}`}>
                {v === "list" ? "List" : "Bars"}
              </button>
            ))}
          </span>
        </span>
      }
      title={
        <span className="flex items-center gap-2">
          {whole(result.steps[0]?.count ?? 0)} people · {pct(last?.conversion_from_start ?? 0)} finished
          {running && <Badge>updating</Badge>}
        </span>
      }
      description={`${day(result.from)} to ${day(new Date(new Date(result.to).getTime() - 1).toISOString())}.${onPeople ? (view === "list" ? " Click a number for the people behind it." : " Click a bar for the people in it, or the striped part for those who stopped.") : ""}`}
    >
      {worst > 0 && (
        <p className="mb-3 text-[13px]">
          <span className="font-medium">The biggest drop is before step {worst + 1 + off}, {result.steps[worst].label}:</span>{" "}
          {whole(result.steps[worst].dropped)} of {whole(result.steps[gateOf(result.steps, worst)].count)} people ({pct(1 - result.steps[worst].conversion_from_previous)}) stopped there.
          {checks.some((c) => c.step === worst + 1) && " Check the data first: part of it may be a tracking gap."}
        </p>
      )}
      {all && (
        <p className={`mb-3 flex items-start gap-2 rounded-md border px-2.5 py-1.5 text-[12px] ${checks.length ? "border-amber-500/40 bg-amber-500/10 text-amber-900 dark:text-amber-200" : "text-muted-foreground"}`}>
          {checks.length > 0 && <TriangleAlert className="mt-0.5 size-3.5 shrink-0" />}
          <span>
            {checks.length > 0
              ? `Tracking check: ${flagged.length === 1 ? `step ${flagged[0]} needs` : `steps ${flagged.slice(0, -1).join(", ")} and ${flagged[flagged.length - 1]} need`} a look. A tracking problem on one step lowers every step after it, so fix or set aside these before trusting the numbers below them.`
              : "Tracking check: nothing found. Every step's data is arriving and links back to the steps before it."}
            {view === "bars" && " Switch to List to check each step."}
          </span>
        </p>
      )}
      {view === "list" && <StepsList result={result} checks={checks} data={all?.steps} onPeople={onPeople} onOptional={onOptional} onFrom={onFrom} off={off} />}
      {view === "list" && ad && (
        <p className="mt-2 text-[12px] text-muted-foreground">Ad network clicks for these dates: {whole(ad.clicks)} ({ad.spend.map((m) => money(m.amount_minor, m.currency)).join(" + ") || "no spend"}). A reference, not a step.</p>
      )}
      <div className={view === "bars" ? "overflow-x-auto" : "hidden"}>
        <div className="grid items-end gap-3" style={{ gridTemplateColumns: columns, minWidth }}>
          {ad && (
            <div className="flex h-56 flex-col justify-end border-r border-dashed pr-3" title="Clicks your ad networks reported for these dates. A total for reference, not people and not a step.">
              <div className="rounded-md border border-dashed border-muted-foreground/50" style={{ height: `${(ad.clicks / adTop) * 100}%`, minHeight: ad.clicks ? 2 : 0 }} />
            </div>
          )}
          {result.steps.map((s, i) => (
            <div key={i} className="flex h-56 flex-col justify-end">
              {i > 0 && s.dropped > 0 && (
                <button
                  aria-label={`${whole(s.dropped)} people who stopped before ${s.label}`}
                  onClick={() => onPeople?.(i + 1, true)}
                  className="rounded-t-md border border-b-0 border-dashed bg-[repeating-linear-gradient(135deg,transparent,transparent_5px,var(--color-neutral-200)_5px,var(--color-neutral-200)_6px)] hover:border-foreground/40 dark:bg-[repeating-linear-gradient(135deg,transparent,transparent_5px,var(--color-neutral-800)_5px,var(--color-neutral-800)_6px)]"
                  style={{ height: `${(s.dropped / top) * 100}%` }}
                />
              )}
              <button
                aria-label={`${whole(s.count)} people at ${s.label}`}
                title={`${whole(s.count)} people · ${pct(s.conversion_from_start)} of everyone who started${i > 0 ? ` · ${pct(s.conversion_from_previous)} of step ${gateOf(result.steps, i) + 1 + off}` : ""}${s.optional ? ` · optional, ${whole(s.skipped ?? 0)} went on without it` : ""}`}
                onClick={() => onPeople?.(i + 1, false)}
                className={`relative transition-opacity hover:opacity-85 ${i > 0 && s.dropped > 0 ? "rounded-b-md" : "rounded-md"}`}
                style={{ height: `${(s.count / top) * 100}%`, minHeight: s.count ? 2 : 0, background: s.optional ? "color-mix(in oklab, var(--series-1) 35%, transparent)" : "linear-gradient(to bottom, var(--series-1), color-mix(in oklab, var(--series-1) 70%, transparent))", outline: s.optional ? "1px dashed var(--series-1)" : undefined, outlineOffset: s.optional ? -1 : undefined }}
              >
                {s.count > 0 && (s.count / top) > 0.12 && (
                  <span className={`absolute inset-x-0 top-2 text-center text-[12px] font-semibold tabular-nums ${s.optional ? "text-foreground" : "text-white"}`}>{pct(s.conversion_from_start)}</span>
                )}
              </button>
            </div>
          ))}
        </div>
        <div className="mt-3 grid items-start gap-3" style={{ gridTemplateColumns: columns, minWidth }}>
          {ad && (
            <div className="min-w-0 border-r border-dashed pr-3 text-[12px]">
              <div className="font-semibold tabular-nums text-[15px]">{whole(ad.clicks)}</div>
              <div>Ad network clicks</div>
              <div className="text-muted-foreground">Reference, not a step</div>
              <div className="text-muted-foreground">{ad.spend.map((m) => money(m.amount_minor, m.currency)).join(" + ") || "no spend"}</div>
              {result.steps[0] && ad.clicks > result.steps[0].count && (
                <div className="text-red-600 dark:text-red-400">{whole(ad.clicks - result.steps[0].count)} not seen on the site</div>
              )}
            </div>
          )}
          {result.steps.map((s, i) => (
            <StepLabel key={i} first={i === 0} n={i + 1 + off} s={s} gate={gateOf(result.steps, i) + 1 + off} />
          ))}
        </div>
      </div>
      {empty > 0 && (
        <p className="mt-3 text-[12px] text-muted-foreground">Nobody did step {empty + 1 + off} after step {gateOf(result.steps, empty) + 1 + off} inside its time limit. If people usually do step {empty + 1 + off} first, move it higher.</p>
      )}
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

// gateOf is the last required step before step i (both counted from 0):
// what a step's rate and drop-off are measured against.
function gateOf(steps: StepResult[], i: number): number {
  for (let j = i - 1; j > 0; j--) if (!steps[j].optional) return j;
  return 0;
}

function StepLabel({ n, s, gate, first }: { n: number; s: StepResult; gate: number; /** The step everyone starts at. */ first: boolean }) {
  return (
    <div className="min-w-0 text-[12px]">
      <div className="font-semibold tabular-nums text-[15px]">{whole(s.count)}</div>
      <div className="truncate" title={s.label}>
        <span className="font-mono text-muted-foreground">{n}</span> {s.label}
      </div>
      {!first && (
        <>
          <div className="text-muted-foreground">{pct(s.conversion_from_previous)} of step {gate}</div>
          {s.optional && <div className="text-muted-foreground" title="An optional step stops nobody: these people went on to a later step without doing it.">Optional · {whole(s.skipped ?? 0)} skipped it</div>}
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

// OverTime draws the share who finished as a line, one point per
// period people started in, with the people who started in the hover.
function OverTime({ result, interval }: { result: Result; interval: string }) {
  const rows = result.over_time;
  const n = result.steps.length;
  const starts = rows.map((r) => r.start);
  const series: TimeSeries[] = [{ label: "Finished", values: rows.map((r) => (r.counts[0] ? Math.round((r.counts[n - 1] / r.counts[0]) * 1000) / 10 : null)) }];
  return (
    <Card title={`Conversion, by the ${interval} people started`} description="The share of people who started in each period and reached the last step. Recent periods can look low while people still have time to finish.">
      <TimeChart starts={starts} series={series} unit="%" format={(v) => (v === null || v === undefined ? "–" : `${v.toFixed(v < 10 && v > 0 ? 1 : 0)}%`)} label="Conversion over time" />
      <div className="mt-3 overflow-x-auto">
        <table className="w-full text-[12px]">
          <thead>
            <tr className="border-b text-left font-mono text-[11px] uppercase text-muted-foreground">
              <th className="py-1.5 pr-3 font-medium">Started in</th>
              <th className="px-2 py-1.5 text-right font-medium">Started</th>
              <th className="px-2 py-1.5 text-right font-medium">Finished</th>
              <th className="py-1.5 pl-2 text-right font-medium">Conversion</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.start} className="border-b last:border-0">
                <td className="py-1 pr-3 font-mono text-muted-foreground">{r.start.slice(0, 10)}</td>
                <td className="px-2 py-1 text-right tabular-nums">{whole(r.counts[0])}</td>
                <td className="px-2 py-1 text-right tabular-nums">{whole(r.counts[n - 1])}</td>
                <td className="py-1 pl-2 text-right tabular-nums">{r.counts[0] ? pct(r.counts[n - 1] / r.counts[0]) : "–"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

// bucketName reads a span of seconds as "Under 1 min", "1 to 6 hours"
// or "Over 30 days".
function bucketName(from: number, to: number | null): string {
  const u = (s: number) => (s < 3600 ? [s / 60, "min"] : s < 86400 ? [s / 3600, "hour"] : [s / 86400, "day"]) as [number, string];
  const [f, fu] = u(from);
  if (to === null) return `Over ${f} ${fu}${f === 1 ? "" : "s"}`;
  const [t, tu] = u(to);
  if (from === 0) return `Under ${t} ${tu}${t === 1 || tu === "min" ? "" : "s"}`;
  return fu === tu ? `${f} to ${t} ${tu}${tu === "min" ? "" : "s"}` : `${f} ${fu}${f === 1 || fu === "min" ? "" : "s"} to ${t} ${tu}${t === 1 || tu === "min" ? "" : "s"}`;
}

// TimeToConvert shows how long people took to reach a step from the one
// before: a bar per span of time, with the median and average above.
function TimeToConvert({ result }: { result: Result }) {
  const steps = result.steps.map((s, i) => ({ s, n: i + 1 })).filter(({ s }) => (s.time_to_convert ?? []).some((b) => b.count > 0));
  const [pick, setPick] = useState(0);
  if (steps.length === 0) return null;
  const { s, n } = steps.find((x) => x.n === pick) ?? steps[steps.length - 1];
  const parts: Part[] = (s.time_to_convert ?? []).map((b) => ({ label: bucketName(b.from_seconds, b.to_seconds), value: b.count }));
  return (
    <Card title="Time to convert" description="How long people took to reach a step from the one before it.">
      <div className="mb-4 flex flex-wrap items-end justify-between gap-4">
        <div className="flex gap-8 text-[13px]">
          <div>
            <div className="text-muted-foreground">Median</div>
            <div className="text-2xl font-semibold tabular-nums">{span(s.median_seconds)}</div>
          </div>
          <div>
            <div className="text-muted-foreground">Average</div>
            <div className="text-2xl font-semibold tabular-nums">{span(s.average_seconds)}</div>
          </div>
        </div>
        {steps.length > 1 && (
          <label className="text-[12px] text-muted-foreground">
            Step
            <select className={`${field} ml-2`} value={n} onChange={(e) => setPick(Number(e.target.value))}>
              {steps.map((x) => (
                <option key={x.n} value={x.n}>{x.n - 1} to {x.n}: {x.s.label}</option>
              ))}
            </select>
          </label>
        )}
      </div>
      <HBars parts={parts} format={(v) => whole(v ?? 0)} oneColor />
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
          <button className="text-[12px] text-muted-foreground hover:text-foreground" onClick={() => onChange({ ...spec, exclusions: [...list, { kind: "event", event: "", from: 1, to: Math.max(2, spec.steps.findIndex((st, j) => j > 0 && !st.optional) + 1) }] })}>+ Exclusion</button>
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
              <select aria-label="From step" className={`${field} h-7 w-[4.5rem]`} value={x.from} onChange={(e) => { const from = Number(e.target.value); set(i, { ...x, from, to: Math.max(x.to, from + 1) }); }}>
                {Array.from({ length: n - 1 }, (_, k) => <option key={k} value={k + 1} disabled={!!spec.steps[k]?.optional}>{k + 1}{spec.steps[k]?.optional ? " (optional)" : ""}</option>)}
              </select>
              <span>and step</span>
              <select aria-label="To step" className={`${field} h-7`} value={x.to} onChange={(e) => set(i, { ...x, to: Number(e.target.value) })}>
                {Array.from({ length: n - x.from }, (_, k) => <option key={k} value={x.from + k + 1} disabled={!!spec.steps[x.from + k]?.optional}>{x.from + k + 1}{spec.steps[x.from + k]?.optional ? " (optional)" : ""}</option>)}
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
