import { useEffect, useId, useRef, useState } from "react";
import { HoverCard, runs, smooth, Swatch, useWidth } from "@/components/chart-kit";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, Empty, ErrorText, field, Loading, PageHeader } from "../ui";
import { PickBrand } from "@/components/pick-brand";
import { useBrand } from "@/lib/brand";
import { nameItem } from "@/lib/records";
import { day, pct, presetLabels, StepEditor, today, whole } from "./Funnels";
import { embedSpec, type InsightEmbed } from "@/lib/insight-embed";
import { InsightBar, InsightList, InsightMissing, partName, useInsightEditor, useInsightRoute } from "@/lib/insight-editor";
import { peekDraft, takeDraft } from "@/components/insight-panels";
import { LifecycleFor } from "./LifecycleInsight";
import { StickinessFor } from "./StickinessInsight";

// Retention (docs/insights.md): of the people who first did one thing in
// a day, week or month, how many came back and did it (or something
// else) in each period after, as a triangle of cohorts and the average
// curve across them.

export type RetentionSpec = Schemas["RetentionSpec"];
export type RetentionResult = Schemas["RetentionResult"];
type Step = Schemas["FunnelStep"];
type Catalog = Schemas["InsightsCatalog"];
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

type View = "cohorts" | "lifecycle" | "stickiness";

export function Retention({ ws, canManage, item, onOpen }: { ws: string; canManage: boolean; item?: string; onOpen?: (id?: string) => void }) {
  const { brand, active } = useBrand(ws);
  const [view, setView] = useState<View>(() => (peekDraft() === "lifecycle" ? "lifecycle" : peekDraft() === "stickiness" ? "stickiness" : "cohorts"));
  useEffect(() => nameItem("retention", "Retention"), []);
  const route = useInsightRoute(item, onOpen, peekDraft() === "retention");
  return (
    <div className="space-y-6">
      <PageHeader
        title="Retention"
        description={view === "cohorts" ? "Of the people who first did something in a week, how many came back the week after, and the one after that. See which cohorts stick and which fade." : view === "lifecycle" ? "Each week, how many active people were new, back from last week or back after a gap, and how many from last week went quiet. See whether the active base grows, and from where." : "Of the people who did something this month, how many did it on one day, on two, on three and more. See whether it is a habit or a one-off."}
        action={
          <div role="tablist" aria-label="View" className="flex rounded-md border p-0.5 text-[13px]">
            {(["cohorts", "lifecycle", "stickiness"] as View[]).map((v) => (
              <button key={v} role="tab" aria-selected={view === v} onClick={() => setView(v)} className={`rounded px-3 py-1 ${view === v ? "bg-accent font-medium" : "text-muted-foreground hover:text-foreground"}`}>
                {v === "cohorts" ? "Cohorts" : v === "lifecycle" ? "Lifecycle" : "Stickiness"}
              </button>
            ))}
          </div>
        }
      />
      {!brand ? <PickBrand what="Retention" brands={active.length} /> : view === "lifecycle" ? <LifecycleFor key={brand.id} brand={brand.id} canManage={canManage} /> : view === "stickiness" ? <StickinessFor key={brand.id} brand={brand.id} canManage={canManage} /> : route.listing ? (
        <InsightList key={brand.id} brand={brand.id} kind="retention" what="retention question" plural="retention questions" canManage={canManage} onOpen={onOpen} column="Asks" describe={(i) => { const x = i.spec as RetentionSpec; return `${partName(x.start) || "…"}, then ${x.return ? partName(x.return) || "…" : "the same again"}`; }} empty="A retention question asks how many of the people who first did something came back in the weeks after. Build one and save it to open it again or put it on a dashboard." />
      ) : (
        <RetentionFor key={brand.id} brand={brand.id} canManage={canManage} item={route.item} onOpen={onOpen} />
      )}
    </div>
  );
}

export function RetentionFor({ brand, canManage, embed, item, onOpen }: { brand: string; canManage: boolean; embed?: InsightEmbed; item?: string; onOpen?: (id?: string) => void }) {
  const [catalog, setCatalog] = useState<Catalog | null>(null);
  const ed = useInsightEditor<RetentionSpec>({ brand, kind: "retention", starter, clean: cleanRetention, embed, item, onOpen, initial: () => takeDraft<RetentionSpec>("retention"), embedSpec: () => embedSpec<RetentionSpec>(embed!, starter) });
  const { spec, setSpec } = ed;
  const [result, setResult] = useState<RetentionResult | null>(null);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState("");
  const run = useRef(0);

  useEffect(() => {
    api.GET("/v1/brands/{brand}/insights/catalog", { params: { path: { brand } } }).then(({ data, error }) => (data ? setCatalog(data) : setError(errorMessage(error))));
  }, [brand]);

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

  if (!catalog || ed.loading) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  if (ed.missing) return <InsightMissing ed={ed} what="question" />;
  const interval = spec.interval ?? "week";

  return (
    <div className="grid gap-6 xl:grid-cols-[380px_minmax(0,1fr)]">
      <div className="xl:col-span-2">
        <InsightBar ed={ed} what="question" all="All retention questions" canManage={canManage} canSave={retentionReady(spec)} placeholder="Weekly retention after sign-up" deleteWarning="The retention question goes for everyone, and off any dashboard it is on. The data stays." embed={!!embed} />
      </div>
      <div className="min-w-0 space-y-4">
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
                          style={share === null ? undefined : { backgroundColor: `color-mix(in oklab, var(--series-1) ${Math.round(share * 100)}%, transparent)`, color: share > 0.5 ? "white" : undefined }}
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
  const [box, w] = useWidth();
  const [hover, setHover] = useState<number | null>(null);
  const h = compact ? 130 : 200;
  const pad = { l: 40, r: 12, t: 12, b: 24 };
  const n = values.length;
  const pw = w - pad.l - pad.r;
  const x = (i: number) => pad.l + (n === 1 ? pw / 2 : (i * pw) / (n - 1));
  const y = (v: number) => pad.t + (1 - v) * (h - pad.t - pad.b);
  const pieces = runs(values).map((r) => r.map(([i, v]) => [x(i), y(v)] as [number, number]));
  const every = Math.max(1, Math.ceil(n / Math.max(2, Math.floor(pw / 40))));
  const id = useId().replace(/:/g, "");
  return (
    <div ref={box} className="relative select-none">
      <svg width={w} height={h} className="block" role="img" aria-label={`Average share who came back by ${interval}`} onPointerLeave={() => setHover(null)}>
        <defs>
          <linearGradient id={`${id}-g`} x1="0" x2="0" y1="0" y2="1">
            <stop offset="0%" stopColor="var(--series-1)" stopOpacity={0.28} />
            <stop offset="100%" stopColor="var(--series-1)" stopOpacity={0} />
          </linearGradient>
        </defs>
        {[0, 0.25, 0.5, 0.75, 1].map((g) => (
          <g key={g}>
            <line x1={pad.l} x2={w - pad.r} y1={y(g)} y2={y(g)} stroke="var(--border)" strokeDasharray={g === 0 ? undefined : "3 4"} />
            <text x={pad.l - 8} y={y(g)} dy="0.32em" textAnchor="end" className="fill-muted-foreground text-[11px] tabular-nums">{Math.round(g * 100)}%</text>
          </g>
        ))}
        {pieces.map((pts, k) => {
          const line = smooth(pts);
          return (
            <g key={k}>
              <path d={`${line}L${pts[pts.length - 1][0]},${y(0)}L${pts[0][0]},${y(0)}Z`} fill={`url(#${id}-g)`} />
              <path d={line} fill="none" stroke="var(--series-1)" strokeWidth={2.25} strokeLinecap="round" />
            </g>
          );
        })}
        {values.map((v, i) => (v === null || v === undefined ? null : <circle key={i} cx={x(i)} cy={y(v)} r={hover === i ? 4.5 : 2.5} fill={hover === i ? "var(--series-1)" : "var(--card)"} stroke="var(--series-1)" strokeWidth={2} />))}
        {values.map((_, i) => i % every === 0 && <text key={i} x={x(i)} y={h - 7} textAnchor="middle" className="fill-muted-foreground text-[11px] tabular-nums">{i}</text>)}
        {hover !== null && <line x1={x(hover)} x2={x(hover)} y1={pad.t} y2={h - pad.b} stroke="var(--foreground)" strokeOpacity={0.2} />}
        <rect
          x={pad.l}
          y={0}
          width={pw}
          height={h}
          fill="transparent"
          onPointerMove={(e) => {
            const r = e.currentTarget.getBoundingClientRect();
            const i = n === 1 ? 0 : Math.round(((e.clientX - r.left) / r.width) * (n - 1));
            setHover(i >= 0 && i < n ? i : null);
          }}
        />
      </svg>
      {hover !== null && (
        <HoverCard x={x(hover)} width={w}>
          <div className="mb-1 font-medium">{periodWord[interval]} {hover}</div>
          <div className="flex items-center gap-2">
            <Swatch color="var(--series-1)" />
            <span className="flex-1 text-muted-foreground">Came back</span>
            <span className="font-semibold tabular-nums">{values[hover] === null || values[hover] === undefined ? "Not yet" : pct(values[hover] as number)}</span>
          </div>
          <div className="mt-1 text-[11px] text-muted-foreground">Average of every group that started in these dates.</div>
        </HoverCard>
      )}
    </div>
  );
}
