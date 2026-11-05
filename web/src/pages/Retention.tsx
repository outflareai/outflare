import { useCallback, useEffect, useRef, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, Empty, ErrorText, field, Loading, PageHeader, toast } from "../ui";
import { PickBrand } from "@/components/pick-brand";
import { useBrand } from "@/lib/brand";
import { nameItem } from "@/lib/records";
import { ago } from "../format";
import { day, pct, presetLabels, StepEditor, today, whole } from "./Funnels";
import { peekDraft, takeDraft } from "@/components/insight-panels";
import { LifecycleFor } from "./LifecycleInsight";

// Retention (docs/insights.md): of the people who first did one thing in
// a day, week or month, how many came back and did it (or something
// else) in each period after, as a triangle of cohorts and the average
// curve across them.

export type RetentionSpec = Schemas["RetentionSpec"];
export type RetentionResult = Schemas["RetentionResult"];
type Step = Schemas["FunnelStep"];
type Catalog = Schemas["InsightsCatalog"];
type Insight = Schemas["Insight"];
type Preset = NonNullable<Schemas["InsightDateRange"]["preset"]>;

const starter: RetentionSpec = { start: { kind: "visit" }, date_range: { preset: "last_90_days" }, interval: "week", periods: 8 };

export function retentionReady(s: RetentionSpec): boolean {
  const ok = (x: Step) => (x.kind !== "event" || !!x.event) && (x.kind !== "conversion" || !!x.conversion) && (x.filters ?? []).every((f) => f.property && (f.op === "exists" || f.op === "not_exists" || (f.value !== undefined && f.value !== "")));
  return ok(s.start) && (!s.return || ok(s.return));
}

export function cleanRetention(s: RetentionSpec): RetentionSpec {
  const step = (x: Step): Step => ({ ...x, filters: x.filters?.length ? x.filters : undefined, within: undefined, label: x.label || undefined });
  return { ...s, start: step(s.start), return: s.return ? step(s.return) : undefined };
}

type View = "cohorts" | "lifecycle";

export function Retention({ ws, canManage }: { ws: string; canManage: boolean }) {
  const { brand, active } = useBrand(ws);
  const [view, setView] = useState<View>(() => (peekDraft() === "lifecycle" ? "lifecycle" : "cohorts"));
  useEffect(() => nameItem("retention", "Retention"), []);
  return (
    <div className="space-y-6">
      <PageHeader
        title="Retention"
        description={view === "cohorts" ? "Of the people who first did something in a week, how many came back the week after, and the one after that. See which cohorts stick and which fade." : "Each week, how many active people were new, back from last week or back after a gap, and how many from last week went quiet. See whether the active base grows, and from where."}
        action={
          <div role="tablist" aria-label="View" className="flex rounded-md border p-0.5 text-[13px]">
            {(["cohorts", "lifecycle"] as View[]).map((v) => (
              <button key={v} role="tab" aria-selected={view === v} onClick={() => setView(v)} className={`rounded px-3 py-1 ${view === v ? "bg-accent font-medium" : "text-muted-foreground hover:text-foreground"}`}>
                {v === "cohorts" ? "Cohorts" : "Lifecycle"}
              </button>
            ))}
          </div>
        }
      />
      {!brand ? <PickBrand what="Retention" brands={active.length} /> : view === "lifecycle" ? <LifecycleFor key={brand.id} brand={brand.id} canManage={canManage} /> : <RetentionFor key={brand.id} brand={brand.id} canManage={canManage} />}
    </div>
  );
}

function RetentionFor({ brand, canManage }: { brand: string; canManage: boolean }) {
  const [catalog, setCatalog] = useState<Catalog | null>(null);
  const [saved, setSaved] = useState<Insight[]>([]);
  const [current, setCurrent] = useState<Insight | null>(null);
  const [spec, setSpec] = useState<RetentionSpec>(() => takeDraft<RetentionSpec>("retention") ?? starter);
  const [result, setResult] = useState<RetentionResult | null>(null);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState("");
  const [name, setName] = useState("");
  const run = useRef(0);

  const loadSaved = useCallback(async () => {
    const { data } = await api.GET("/v1/brands/{brand}/insights", { params: { path: { brand }, query: { kind: "retention" } } });
    if (data) setSaved(data.data);
  }, [brand]);

  useEffect(() => {
    api.GET("/v1/brands/{brand}/insights/catalog", { params: { path: { brand } } }).then(({ data, error }) => (data ? setCatalog(data) : setError(errorMessage(error))));
    loadSaved();
  }, [brand, loadSaved]);

  useEffect(() => {
    if (!retentionReady(spec)) return;
    const n = ++run.current;
    const t = setTimeout(async () => {
      setRunning(true);
      const { data, error } = await api.POST("/v1/brands/{brand}/insights/retention", { params: { path: { brand } }, body: cleanRetention(spec) });
      if (n !== run.current) return;
      setRunning(false);
      if (!data) return setError(errorMessage(error));
      setError("");
      setResult(data);
    }, 400);
    return () => clearTimeout(t);
  }, [brand, spec]);

  function open(i: Insight) {
    setCurrent(i);
    setName(i.name);
    setSpec(i.spec as RetentionSpec);
  }

  async function save() {
    const body = cleanRetention(spec);
    if (current) {
      const { data, error } = await api.PATCH("/v1/brands/{brand}/insights/{insight}", { params: { path: { brand, insight: current.id } }, body: { name, spec: body } });
      if (!data) return toast(errorMessage(error), "error");
      setCurrent(data);
    } else {
      const { data, error } = await api.POST("/v1/brands/{brand}/insights", { params: { path: { brand } }, body: { kind: "retention", name, spec: body } });
      if (!data || !("id" in data)) return toast(errorMessage(error), "error");
      setCurrent(data);
    }
    toast("Saved");
    loadSaved();
  }

  async function remove() {
    if (!current || !(await confirmAction(`Delete ${current.name}?`, "The retention question goes for everyone, and off any dashboard it is on. The data stays.", "Delete"))) return;
    const { error } = await api.DELETE("/v1/brands/{brand}/insights/{insight}", { params: { path: { brand, insight: current.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Deleted");
    setCurrent(null);
    setName("");
    loadSaved();
  }

  if (!catalog) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  const interval = spec.interval ?? "week";

  return (
    <div className="grid gap-6 xl:grid-cols-[380px_minmax(0,1fr)]">
      <div className="min-w-0 space-y-4">
        <Card title="Saved" action={current ? <Button variant="ghost" onClick={() => { setCurrent(null); setName(""); setSpec(starter); }}>New question</Button> : undefined}>
          {saved.length === 0 ? (
            <p className="text-[13px] text-muted-foreground">None yet. Build one below and save it to put it on a dashboard.</p>
          ) : (
            <ul className="-mx-1 space-y-0.5">
              {saved.map((i) => (
                <li key={i.id}>
                  <button onClick={() => open(i)} className={`flex w-full items-center justify-between gap-2 rounded px-2 py-1.5 text-left text-[13px] hover:bg-accent ${current?.id === i.id ? "bg-accent font-medium" : ""}`}>
                    <span className="truncate">{i.name}</span>
                    <span className="shrink-0 font-mono text-[11px] text-muted-foreground">{ago(i.updated_at)}</span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </Card>

        <Card title="Who starts a cohort" description="Each person joins the cohort of the period they first did this in the dates.">
          <ol>
            <StepEditor n={1} title="Start" step={spec.start} catalog={catalog} onChange={(st) => setSpec((s) => ({ ...s, start: st }))} within={false} />
          </ol>
        </Card>

        <Card title="What counts as coming back" description="The same thing again, or something else: a payment, an event.">
          {spec.return ? (
            <ol>
              <StepEditor n={2} title="Return" step={spec.return} catalog={catalog} onChange={(st) => setSpec((s) => ({ ...s, return: st }))} onRemove={() => setSpec((s) => ({ ...s, return: undefined }))} within={false} />
            </ol>
          ) : (
            <div className="flex items-center justify-between gap-3 text-[13px]">
              <span className="text-muted-foreground">Doing the start again.</span>
              <Button variant="ghost" onClick={() => setSpec((s) => ({ ...s, return: { kind: "payment" } }))}>Something else</Button>
            </div>
          )}
        </Card>

        <Card title="Options">
          <div className="space-y-3 text-[13px]">
            <label className="block space-y-1">
              <span className="font-medium">Dates the cohorts start in</span>
              <select className={`${field} w-full`} value={spec.date_range?.preset ?? (spec.date_range?.from ? "custom" : "last_90_days")} onChange={(e) => setSpec((s) => ({ ...s, date_range: e.target.value === "custom" ? { from: today(-90), to: today(0) } : { preset: e.target.value as Preset } }))}>
                {Object.entries(presetLabels).map(([k, l]) => (
                  <option key={k} value={k}>{l}</option>
                ))}
                <option value="custom">Custom dates</option>
              </select>
            </label>
            {spec.date_range?.from !== undefined && !spec.date_range.preset && (
              <div className="flex gap-2">
                <input type="date" className={`${field} flex-1`} aria-label="From" value={spec.date_range.from ?? ""} onChange={(e) => setSpec((s) => ({ ...s, date_range: { ...s.date_range, from: e.target.value } }))} />
                <input type="date" className={`${field} flex-1`} aria-label="To" value={spec.date_range.to ?? ""} onChange={(e) => setSpec((s) => ({ ...s, date_range: { ...s.date_range, to: e.target.value } }))} />
              </div>
            )}
            <div className="grid grid-cols-2 gap-2">
              <label className="block space-y-1">
                <span className="font-medium">By</span>
                <select className={`${field} w-full`} value={interval} onChange={(e) => { const iv = e.target.value as "day" | "week" | "month"; setSpec((s) => ({ ...s, interval: iv, periods: { day: 14, week: 8, month: 6 }[iv] })); }}>
                  <option value="day">Day</option>
                  <option value="week">Week</option>
                  <option value="month">Month</option>
                </select>
              </label>
              <label className="block space-y-1">
                <span className="font-medium">Periods after</span>
                <input type="number" min={1} max={24} className={`${field} w-full`} value={spec.periods ?? 8} onChange={(e) => setSpec((s) => ({ ...s, periods: Math.max(1, Math.min(24, Number(e.target.value) || 1)) }))} />
              </label>
            </div>
          </div>
        </Card>

        {canManage && (
          <Card title={current ? "Save changes" : "Save this question"}>
            <div className="flex gap-2">
              <input id="retention-name" aria-label="Name" className={`${field} min-w-0 flex-1`} placeholder="Weekly visitor retention" value={name} onChange={(e) => setName(e.target.value)} maxLength={120} />
              <Button disabled={!name.trim() || !retentionReady(spec)} onClick={save}>Save</Button>
            </div>
            {current && <button className="mt-2 text-[12px] text-muted-foreground hover:text-destructive" onClick={remove}>Delete this question</button>}
          </Card>
        )}
      </div>

      <div className="min-w-0 space-y-4">
        {error && <ErrorText>{error}</ErrorText>}
        {!retentionReady(spec) && <Empty title="Finish the question">An event step needs an event, a conversion step a conversion, and each filter a value.</Empty>}
        {retentionReady(spec) && !result && <Loading />}
        {retentionReady(spec) && result && (
          <>
            <Card
              title={<span className="flex items-center gap-2">{result.start_label} → {result.return_label}{running && <Badge>updating</Badge>}</span>}
              description={`Cohorts by the ${result.interval} people first ${result.start_label.toLowerCase()}, ${day(result.from)} to ${day(new Date(new Date(result.to).getTime() - 1).toISOString())}, and the share who came back in each ${result.interval} after. The curve is the average over the cohorts that have reached each ${result.interval}.`}
            >
              <RetentionView result={result} />
            </Card>
          </>
        )}
      </div>
    </div>
  );
}

const periodWord: Record<string, string> = { day: "Day", week: "Week", month: "Month" };

// RetentionView draws the average curve and the cohort triangle.
export function RetentionView({ result, compact }: { result: RetentionResult; compact?: boolean }) {
  const n = result.periods + 1;
  const cohorts = result.cohorts.filter((c) => c.size > 0);
  return (
    <div className="space-y-4">
      <Curve values={result.average} interval={result.interval} compact={compact} />
      {cohorts.length === 0 ? (
        <p className="text-[13px] text-muted-foreground">Nobody started in these dates.</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full border-collapse text-[12px] tabular-nums">
            <thead>
              <tr className="text-left text-muted-foreground">
                <th className="py-1 pr-3 font-medium">Cohort</th>
                <th className="py-1 pr-3 text-right font-medium">People</th>
                {Array.from({ length: n }, (_, i) => (
                  <th key={i} className="py-1 px-1 text-center font-medium" title={`${periodWord[result.interval]} ${i}`}>{i}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {(compact ? cohorts.slice(-8) : cohorts).map((c) => (
                <tr key={c.start}>
                  <td className="py-0.5 pr-3 whitespace-nowrap">{day(c.start)}</td>
                  <td className="py-0.5 pr-3 text-right">{whole(c.size)}</td>
                  {c.counts.map((v, i) => {
                    const share = v === null || v === undefined ? null : v / c.size;
                    return (
                      <td key={i} className="p-0.5">
                        <div
                          className={`rounded px-1 py-1 text-center ${share === null ? "text-muted-foreground/40" : ""}`}
                          style={share === null ? undefined : { backgroundColor: `color-mix(in oklab, var(--color-primary) ${Math.round(share * 100)}%, transparent)`, color: share > 0.5 ? "var(--color-primary-foreground)" : undefined }}
                          title={v === null || v === undefined ? "Not yet" : `${whole(v)} of ${whole(c.size)}`}
                        >
                          {share === null ? "·" : pct(share)}
                        </div>
                      </td>
                    );
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function Curve({ values, interval, compact }: { values: (number | null)[]; interval: string; compact?: boolean }) {
  const w = 600;
  const h = compact ? 110 : 160;
  const pad = { l: 36, r: 8, t: 10, b: 22 };
  const n = values.length;
  const x = (i: number) => pad.l + (n === 1 ? 0 : (i * (w - pad.l - pad.r)) / (n - 1));
  const y = (v: number) => pad.t + (1 - v) * (h - pad.t - pad.b);
  const pts = values.map((v, i) => (v === null || v === undefined ? null : [x(i), y(v)] as const));
  let d = "";
  pts.forEach((p, i) => {
    if (!p) return;
    d += `${d === "" || !pts[i - 1] ? "M" : "L"}${p[0].toFixed(1)},${p[1].toFixed(1)} `;
  });
  return (
    <svg viewBox={`0 0 ${w} ${h}`} className="w-full" role="img" aria-label={`Average share who came back by ${interval}`}>
      {[0, 0.5, 1].map((g) => (
        <g key={g}>
          <line x1={pad.l} x2={w - pad.r} y1={y(g)} y2={y(g)} stroke="currentColor" strokeOpacity={0.12} />
          <text x={pad.l - 6} y={y(g) + 3} textAnchor="end" fontSize={10} fill="currentColor" fillOpacity={0.6}>{Math.round(g * 100)}%</text>
        </g>
      ))}
      <path d={d} fill="none" stroke="var(--color-primary)" strokeWidth={2} />
      {pts.map((p, i) => p && <circle key={i} cx={p[0]} cy={p[1]} r={2.5} fill="var(--color-primary)"><title>{`${periodWord[interval]} ${i}: ${pct(values[i] ?? 0)}`}</title></circle>)}
      {values.map((_, i) => (n <= 14 || i % 2 === 0) && <text key={i} x={x(i)} y={h - 6} textAnchor="middle" fontSize={10} fill="currentColor" fillOpacity={0.6}>{i}</text>)}
    </svg>
  );
}
