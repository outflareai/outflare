import { useCallback, useEffect, useRef, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, Empty, ErrorText, field, Loading, toast } from "../ui";
import { ago } from "../format";
import { day, presetLabels, StepEditor, today, whole } from "./Funnels";
import { takeDraft } from "@/components/insight-panels";

// Lifecycle insight (docs/insights.md): in each day, week or month, how
// many of the people who did something were new to it, came back from the
// period before (returning) or from further back (resurrected), and how
// many from the period before went quiet (dormant). It shares the
// Retention page: cohorts answer "do they stick", lifecycle "is the
// active base growing, and from where". (Lifecycle.tsx is the email
// lifecycle page, which is unrelated.)

export type LifecycleSpec = Schemas["LifecycleSpec"];
export type LifecycleResult = Schemas["LifecycleResult"];
type Catalog = Schemas["InsightsCatalog"];
type Insight = Schemas["Insight"];
type Preset = NonNullable<Schemas["InsightDateRange"]["preset"]>;

const starter: LifecycleSpec = { step: { kind: "visit" }, date_range: { preset: "last_90_days" }, interval: "week" };

export function lifecycleReady(s: LifecycleSpec): boolean {
  const x = s.step;
  return (x.kind !== "event" || !!x.event) && (x.kind !== "conversion" || !!x.conversion) && (x.filters ?? []).every((f) => f.property && (f.op === "exists" || f.op === "not_exists" || (f.value !== undefined && f.value !== "")));
}

export function cleanLifecycle(s: LifecycleSpec): LifecycleSpec {
  return { ...s, step: { ...s.step, filters: s.step.filters?.length ? s.step.filters : undefined, within: undefined, label: s.step.label || undefined } };
}

export function LifecycleFor({ brand, canManage }: { brand: string; canManage: boolean }) {
  const [catalog, setCatalog] = useState<Catalog | null>(null);
  const [saved, setSaved] = useState<Insight[]>([]);
  const [current, setCurrent] = useState<Insight | null>(null);
  const [spec, setSpec] = useState<LifecycleSpec>(() => takeDraft<LifecycleSpec>("lifecycle") ?? starter);
  const [result, setResult] = useState<LifecycleResult | null>(null);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState("");
  const [name, setName] = useState("");
  const run = useRef(0);

  const loadSaved = useCallback(async () => {
    const { data } = await api.GET("/v1/brands/{brand}/insights", { params: { path: { brand }, query: { kind: "lifecycle" } } });
    if (data) setSaved(data.data);
  }, [brand]);

  useEffect(() => {
    api.GET("/v1/brands/{brand}/insights/catalog", { params: { path: { brand } } }).then(({ data, error }) => (data ? setCatalog(data) : setError(errorMessage(error))));
    loadSaved();
  }, [brand, loadSaved]);

  useEffect(() => {
    if (!lifecycleReady(spec)) return;
    const n = ++run.current;
    const t = setTimeout(async () => {
      setRunning(true);
      const { data, error } = await api.POST("/v1/brands/{brand}/insights/lifecycle", { params: { path: { brand } }, body: cleanLifecycle(spec) });
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
    setSpec(i.spec as LifecycleSpec);
  }

  async function save() {
    const body = cleanLifecycle(spec);
    if (current) {
      const { data, error } = await api.PATCH("/v1/brands/{brand}/insights/{insight}", { params: { path: { brand, insight: current.id } }, body: { name, spec: body } });
      if (!data) return toast(errorMessage(error), "error");
      setCurrent(data);
    } else {
      const { data, error } = await api.POST("/v1/brands/{brand}/insights", { params: { path: { brand } }, body: { kind: "lifecycle", name, spec: body } });
      if (!data || !("id" in data)) return toast(errorMessage(error), "error");
      setCurrent(data);
    }
    toast("Saved");
    loadSaved();
  }

  async function remove() {
    if (!current || !(await confirmAction(`Delete ${current.name}?`, "The lifecycle question goes for everyone, and off any dashboard it is on. The data stays.", "Delete"))) return;
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

        <Card title="What counts as active" description="A visit, a page, an event, a conversion or a payment. Whether someone is new looks at their whole history, not only these dates.">
          <ol>
            <StepEditor n={1} title="Active" step={spec.step} catalog={catalog} onChange={(st) => setSpec((s) => ({ ...s, step: st }))} within={false} />
          </ol>
        </Card>

        <Card title="Options">
          <div className="space-y-3 text-[13px]">
            <label className="block space-y-1">
              <span className="font-medium">Dates</span>
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
            <label className="block space-y-1">
              <span className="font-medium">By</span>
              <select id="lifecycle-interval" className={`${field} w-full`} value={interval} onChange={(e) => setSpec((s) => ({ ...s, interval: e.target.value as "day" | "week" | "month" }))}>
                <option value="day">Day</option>
                <option value="week">Week</option>
                <option value="month">Month</option>
              </select>
            </label>
          </div>
        </Card>

        {canManage && (
          <Card title={current ? "Save changes" : "Save this question"}>
            <div className="flex gap-2">
              <input id="lifecycle-name" aria-label="Name" className={`${field} min-w-0 flex-1`} placeholder="Weekly active visitors" value={name} onChange={(e) => setName(e.target.value)} maxLength={120} />
              <Button disabled={!name.trim() || !lifecycleReady(spec)} onClick={save}>Save</Button>
            </div>
            {current && <button className="mt-2 text-[12px] text-muted-foreground hover:text-destructive" onClick={remove}>Delete this question</button>}
          </Card>
        )}
      </div>

      <div className="min-w-0 space-y-4">
        {error && <ErrorText>{error}</ErrorText>}
        {!lifecycleReady(spec) && <Empty title="Finish the question">An event step needs an event, a conversion step a conversion, and each filter a value.</Empty>}
        {lifecycleReady(spec) && !result && <Loading />}
        {lifecycleReady(spec) && result && (
          <Card
            title={<span className="flex items-center gap-2">{result.label}{running && <Badge>updating</Badge>}</span>}
            description={`Each ${result.interval} from ${day(result.from)} to ${day(new Date(new Date(result.to).getTime() - 1).toISOString())}: who was active for the first time ever (new), was active the ${result.interval} before too (returning) or came back after a gap (resurrected). Below the line, who was active the ${result.interval} before and not this time (dormant).`}
          >
            <LifecycleView result={result} />
          </Card>
        )}
      </div>
    </div>
  );
}

const parts = [
  { key: "new", label: "New", color: "var(--color-primary)", opacity: 1 },
  { key: "returning", label: "Returning", color: "var(--color-primary)", opacity: 0.55 },
  { key: "resurrected", label: "Resurrected", color: "var(--color-primary)", opacity: 0.28 },
] as const;

// LifecycleView draws each period as a bar of new, returning and
// resurrected people stacked above the line, and the dormant below it,
// with the numbers in a table underneath.
export function LifecycleView({ result, compact }: { result: LifecycleResult; compact?: boolean }) {
  const ps = result.periods;
  const w = 600;
  const h = compact ? 130 : 220;
  const pad = { l: 36, r: 8, t: 10, b: 20 };
  const up = Math.max(1, ...ps.map((p) => p.new + p.returning + p.resurrected));
  const down = Math.max(0, ...ps.map((p) => p.dormant));
  const plot = h - pad.t - pad.b;
  const scale = plot / (up + down);
  const zero = pad.t + up * scale;
  const n = ps.length;
  const slot = (w - pad.l - pad.r) / Math.max(1, n);
  const bw = Math.max(2, Math.min(28, slot * 0.7));
  const x = (i: number) => pad.l + i * slot + (slot - bw) / 2;
  const every = Math.ceil(n / (compact ? 6 : 12));
  const total = ps.reduce((acc, p) => ({ new: acc.new + p.new, returning: acc.returning + p.returning, resurrected: acc.resurrected + p.resurrected, dormant: acc.dormant + p.dormant }), { new: 0, returning: 0, resurrected: 0, dormant: 0 });
  if (ps.every((p) => p.new + p.returning + p.resurrected + p.dormant === 0)) {
    return <p className="text-[13px] text-muted-foreground">Nobody was active in these dates.</p>;
  }
  return (
    <div className="space-y-3">
      <svg viewBox={`0 0 ${w} ${h}`} className="w-full" role="img" aria-label={`Active people by ${result.interval}: new, returning and resurrected above the line, dormant below`}>
        <line x1={pad.l} x2={w - pad.r} y1={zero} y2={zero} stroke="currentColor" strokeOpacity={0.35} />
        <text x={pad.l - 6} y={pad.t + 4} textAnchor="end" fontSize={10} fill="currentColor" fillOpacity={0.6}>{whole(up)}</text>
        {down > 0 && <text x={pad.l - 6} y={h - pad.b} textAnchor="end" fontSize={10} fill="currentColor" fillOpacity={0.6}>-{whole(down)}</text>}
        {ps.map((p, i) => {
          let y = zero;
          const title = `${day(p.start)}: ${whole(p.new)} new, ${whole(p.returning)} returning, ${whole(p.resurrected)} resurrected, ${whole(p.dormant)} dormant`;
          return (
            <g key={p.start}>
              <title>{title}</title>
              {parts.map((part) => {
                const v = p[part.key];
                const hh = v * scale;
                y -= hh;
                return v > 0 ? <rect key={part.key} x={x(i)} y={y} width={bw} height={hh} fill={part.color} fillOpacity={part.opacity} /> : null;
              })}
              {p.dormant > 0 && <rect x={x(i)} y={zero} width={bw} height={p.dormant * scale} fill="var(--color-destructive)" fillOpacity={0.6} />}
              {i % every === 0 && (
                <text x={x(i) + bw / 2} y={h - 5} textAnchor="middle" fontSize={10} fill="currentColor" fillOpacity={0.6}>{day(p.start).replace(/,? \d{4}$/, "")}</text>
              )}
            </g>
          );
        })}
      </svg>
      <div className="flex flex-wrap gap-x-4 gap-y-1 text-[12px] text-muted-foreground">
        {parts.map((part) => (
          <span key={part.key} className="flex items-center gap-1.5">
            <span className="inline-block h-2.5 w-2.5 rounded-sm" style={{ backgroundColor: part.color, opacity: part.opacity }} />
            {part.label} <span className="tabular-nums text-foreground">{whole(total[part.key])}</span>
          </span>
        ))}
        <span className="flex items-center gap-1.5">
          <span className="inline-block h-2.5 w-2.5 rounded-sm" style={{ backgroundColor: "var(--color-destructive)", opacity: 0.6 }} />
          Dormant <span className="tabular-nums text-foreground">{whole(total.dormant)}</span>
        </span>
      </div>
      {!compact && (
        <div className="overflow-x-auto">
          <table className="w-full border-collapse text-[12px] tabular-nums">
            <thead>
              <tr className="text-left text-muted-foreground">
                <th className="py-1 pr-3 font-medium">{result.interval === "day" ? "Day" : result.interval === "week" ? "Week of" : "Month"}</th>
                <th className="py-1 px-2 text-right font-medium">Active</th>
                <th className="py-1 px-2 text-right font-medium">New</th>
                <th className="py-1 px-2 text-right font-medium">Returning</th>
                <th className="py-1 px-2 text-right font-medium">Resurrected</th>
                <th className="py-1 px-2 text-right font-medium">Dormant</th>
              </tr>
            </thead>
            <tbody>
              {ps.map((p) => (
                <tr key={p.start} className="border-t">
                  <td className="py-1 pr-3 whitespace-nowrap">{day(p.start)}</td>
                  <td className="py-1 px-2 text-right font-medium">{whole(p.new + p.returning + p.resurrected)}</td>
                  <td className="py-1 px-2 text-right">{whole(p.new)}</td>
                  <td className="py-1 px-2 text-right">{whole(p.returning)}</td>
                  <td className="py-1 px-2 text-right">{whole(p.resurrected)}</td>
                  <td className="py-1 px-2 text-right text-destructive">{p.dormant ? `-${whole(p.dormant)}` : "0"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
