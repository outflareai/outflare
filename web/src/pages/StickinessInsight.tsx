import { useCallback, useEffect, useRef, useState } from "react";
import { HoverCard, niceTicks, short, useWidth } from "@/components/chart-kit";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, Empty, ErrorText, field, Loading, toast } from "../ui";
import { ago } from "../format";
import { day, presetLabels, StepEditor, today, whole } from "./Funnels";
import { embedSpec, type InsightEmbed } from "@/lib/insight-embed";
import { takeDraft } from "@/components/insight-panels";

// Stickiness (docs/insights.md): of the people who did something in the
// dates, how many did it on one day, on two, on three and so on. It
// shares the Retention page: cohorts answer "do they come back",
// lifecycle "is the active base growing", stickiness "how often do the
// active ones use it".

export type StickinessSpec = Schemas["StickinessSpec"];
export type StickinessResult = Schemas["StickinessResult"];
type Catalog = Schemas["InsightsCatalog"];
type Insight = Schemas["Insight"];
type Preset = NonNullable<Schemas["InsightDateRange"]["preset"]>;

const starter: StickinessSpec = { step: { kind: "visit" }, date_range: { preset: "last_30_days" }, interval: "day" };

export function stickinessReady(s: StickinessSpec): boolean {
  const x = s.step;
  return (x.kind !== "event" || !!x.event) && (x.kind !== "conversion" || !!x.conversion) && (x.filters ?? []).every((f) => f.property && (f.op === "exists" || f.op === "not_exists" || (f.value !== undefined && f.value !== "")));
}

export function cleanStickiness(s: StickinessSpec): StickinessSpec {
  return { ...s, step: { ...s.step, filters: s.step.filters?.length ? s.step.filters : undefined, within: undefined, label: s.step.label || undefined } };
}

export function StickinessFor({ brand, canManage, embed }: { brand: string; canManage: boolean; embed?: InsightEmbed }) {
  const [catalog, setCatalog] = useState<Catalog | null>(null);
  const [saved, setSaved] = useState<Insight[]>([]);
  const [current, setCurrent] = useState<Insight | null>(embed?.insight ?? null);
  const [spec, setSpec] = useState<StickinessSpec>(() => (embed ? embedSpec<StickinessSpec>(embed, starter) : (takeDraft<StickinessSpec>("stickiness") ?? starter)));
  const [result, setResult] = useState<StickinessResult | null>(null);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState("");
  const [name, setName] = useState(embed?.insight?.name ?? "");
  const run = useRef(0);

  const loadSaved = useCallback(async () => {
    const { data } = await api.GET("/v1/brands/{brand}/insights", { params: { path: { brand }, query: { kind: "stickiness" } } });
    if (data) setSaved(data.data);
  }, [brand]);

  useEffect(() => {
    api.GET("/v1/brands/{brand}/insights/catalog", { params: { path: { brand } } }).then(({ data, error }) => (data ? setCatalog(data) : setError(errorMessage(error))));
    loadSaved();
  }, [brand, loadSaved]);

  useEffect(() => {
    if (!stickinessReady(spec)) return;
    const n = ++run.current;
    const t = setTimeout(async () => {
      setRunning(true);
      const { data, error } = await api.POST("/v1/brands/{brand}/insights/stickiness", { params: { path: { brand } }, body: cleanStickiness(spec) });
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
    setSpec(i.spec as StickinessSpec);
  }

  async function save() {
    const body = cleanStickiness(spec);
    if (current) {
      const { data, error } = await api.PATCH("/v1/brands/{brand}/insights/{insight}", { params: { path: { brand, insight: current.id } }, body: { name, spec: body } });
      if (!data) return toast(errorMessage(error), "error");
      setCurrent(data);
      embed?.onSaved(data);
    } else {
      const { data, error } = await api.POST("/v1/brands/{brand}/insights", { params: { path: { brand } }, body: { kind: "stickiness", name, spec: body } });
      if (!data || !("id" in data)) return toast(errorMessage(error), "error");
      setCurrent(data);
      embed?.onSaved(data);
    }
    toast("Saved");
    loadSaved();
  }

  async function remove() {
    if (!current || !(await confirmAction(`Delete ${current.name}?`, "The stickiness question goes for everyone, and off any dashboard it is on. The data stays.", "Delete"))) return;
    const { error } = await api.DELETE("/v1/brands/{brand}/insights/{insight}", { params: { path: { brand, insight: current.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast("Deleted");
    setCurrent(null);
    setName("");
    loadSaved();
  }

  if (!catalog) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  const interval = spec.interval ?? "day";

  return (
    <div className="grid gap-6 xl:grid-cols-[380px_minmax(0,1fr)]">
      <div className="min-w-0 space-y-4">
        {!embed && (
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
        )}

        <Card title="What counts as active" description="A visit, a page, an event, a conversion or a payment. Only the dates below are counted.">
          <ol>
            <StepEditor n={1} title="Active" step={spec.step} catalog={catalog} onChange={(st) => setSpec((s) => ({ ...s, step: st }))} within={false} />
          </ol>
        </Card>

        <Card title="Options">
          <div className="space-y-3 text-[13px]">
            <label className="block space-y-1">
              <span className="font-medium">Dates</span>
              <select className={`${field} w-full`} value={spec.date_range?.preset ?? (spec.date_range?.from ? "custom" : "last_30_days")} onChange={(e) => setSpec((s) => ({ ...s, date_range: e.target.value === "custom" ? { from: today(-29), to: today(0) } : { preset: e.target.value as Preset } }))}>
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
              <span className="font-medium">Count the</span>
              <select id="stickiness-interval" className={`${field} w-full`} value={interval} onChange={(e) => setSpec((s) => ({ ...s, interval: e.target.value as "day" | "week" | "month" }))}>
                <option value="day">Days each person was active</option>
                <option value="week">Weeks each person was active</option>
                <option value="month">Months each person was active</option>
              </select>
            </label>
          </div>
        </Card>

        {canManage && (
          <Card title={current ? "Save changes" : "Save this question"}>
            <div className="flex gap-2">
              <input id="stickiness-name" aria-label="Name" className={`${field} min-w-0 flex-1`} placeholder="Days active per month" value={name} onChange={(e) => setName(e.target.value)} maxLength={120} />
              <Button disabled={!name.trim() || !stickinessReady(spec)} onClick={save}>Save</Button>
            </div>
            {current && !embed && <button className="mt-2 text-[12px] text-muted-foreground hover:text-destructive" onClick={remove}>Delete this question</button>}
          </Card>
        )}
      </div>

      <div className="min-w-0 space-y-4">
        {error && <ErrorText>{error}</ErrorText>}
        {!stickinessReady(spec) && <Empty title="Finish the question">An event step needs an event, a conversion step a conversion, and each filter a value.</Empty>}
        {stickinessReady(spec) && !result && <Loading />}
        {stickinessReady(spec) && result && (
          <Card
            title={<span className="flex items-center gap-2">{result.label}{running && <Badge>updating</Badge>}</span>}
            description={`${day(result.from)} to ${day(new Date(new Date(result.to).getTime() - 1).toISOString())}: of everyone who did it, how many did it in one ${result.interval}, in two, in three and so on. The further right the bars reach, the more of a habit it is.`}
          >
            <StickinessView result={result} />
          </Card>
        )}
      </div>
    </div>
  );
}

// StickinessView draws one bar per number of days (or weeks, or months)
// active, as tall as the people who had exactly that many, with the
// share of everyone on top.
export function StickinessView({ result, compact }: { result: StickinessResult; compact?: boolean }) {
  const [box, width, boxH] = useWidth();
  const [hover, setHover] = useState<number | null>(null);
  const b = result.buckets;
  if (result.people === 0) return <Empty title="Nobody did this in these dates" />;
  const height = compact ? Math.max(150, boxH) : 260;
  const ticks = niceTicks(0, Math.max(1, ...b.map((x) => x.people)), 4);
  const hi = ticks[ticks.length - 1];
  const pad = { t: 22, r: 8, b: 38, l: Math.max(28, ...ticks.map((t) => short(t).length * 6.5 + 8)) };
  const w = Math.max(10, width - pad.l - pad.r), h = height - pad.t - pad.b;
  const slot = w / b.length;
  const bw = Math.max(3, Math.min(44, slot * 0.7));
  const y = (v: number) => pad.t + h - (v / (hi || 1)) * h;
  const every = Math.max(1, Math.ceil(26 / slot));
  const unit = result.interval;
  const name = (x: (typeof b)[number]) => `${x.periods}${x.or_more ? " or more" : ""} ${unit}${x.periods === 1 && !x.or_more ? "" : "s"}`;
  const pct = (v: number) => `${(v * 100).toFixed(v < 0.1 && v > 0 ? 1 : 0)}%`;
  const twoPlus = b.filter((x) => x.periods >= 2).reduce((a, x) => a + x.people, 0);
  return (
    <div className={compact ? "flex h-full min-h-[190px] flex-col" : ""}>
      {!compact && (
        <div className="mb-4 grid grid-cols-3 gap-4 text-[13px]">
          <div>
            <div className="text-muted-foreground">People</div>
            <div className="text-2xl font-semibold tabular-nums">{whole(result.people)}</div>
          </div>
          <div>
            <div className="text-muted-foreground">Average {unit}s active</div>
            <div className="text-2xl font-semibold tabular-nums">{result.average_periods === null ? "–" : result.average_periods.toFixed(1)}</div>
          </div>
          <div>
            <div className="text-muted-foreground">Came back at least once</div>
            <div className="text-2xl font-semibold tabular-nums">{pct(twoPlus / result.people)}</div>
          </div>
        </div>
      )}
      <div ref={box} className={`relative select-none ${compact ? "min-h-0 flex-1" : ""}`}>
        <svg width={width} height={height} className={`block overflow-visible ${compact ? "absolute inset-0" : ""}`} role="img" aria-label={`Stickiness of ${result.label}`} onPointerLeave={() => setHover(null)}>
          {ticks.map((t) => (
            <g key={t}>
              <line x1={pad.l} x2={pad.l + w} y1={y(t)} y2={y(t)} stroke="var(--border)" strokeDasharray={t === 0 ? undefined : "3 4"} />
              <text x={pad.l - 8} y={y(t)} dy="0.32em" textAnchor="end" className="fill-muted-foreground text-[11px] tabular-nums">{short(t)}</text>
            </g>
          ))}
          {b.map((x, i) => {
            const cx = pad.l + slot * (i + 0.5);
            const top = y(x.people), bh = pad.t + h - top;
            const r = Math.min(4, bw / 2, bh);
            return (
              <g key={x.periods} onPointerEnter={() => setHover(i)}>
                <rect x={pad.l + slot * i} y={pad.t} width={slot} height={h + 4} fill={hover === i ? "var(--foreground)" : "transparent"} opacity={0.05} rx={4} />
                {bh >= 0.5 && <path d={`M${cx - bw / 2},${top + bh}V${top + r}Q${cx - bw / 2},${top} ${cx - bw / 2 + r},${top}H${cx + bw / 2 - r}Q${cx + bw / 2},${top} ${cx + bw / 2},${top + r}V${top + bh}Z`} fill="var(--series-1)" fillOpacity={hover === null || hover === i ? 1 : 0.6} />}
                {slot >= 30 && x.people > 0 && <text x={cx} y={top - 6} textAnchor="middle" className="fill-foreground text-[10px] tabular-nums">{pct(x.share)}</text>}
                {i % every === 0 && <text x={cx} y={height - 20} textAnchor="middle" className="fill-muted-foreground text-[11px] tabular-nums">{x.periods}{x.or_more ? "+" : ""}</text>}
              </g>
            );
          })}
          <text x={pad.l + w / 2} y={height - 3} textAnchor="middle" className="fill-muted-foreground text-[11px]">{unit[0].toUpperCase() + unit.slice(1)}s active</text>
        </svg>
        {hover !== null && (
          <HoverCard x={pad.l + slot * (hover + 0.5)} width={width}>
            <div className="font-medium">Active in {name(b[hover])}</div>
            <div className="mt-1 flex items-center justify-between gap-4">
              <span className="text-muted-foreground">People</span>
              <span className="font-semibold tabular-nums">{whole(b[hover].people)}</span>
            </div>
            <div className="flex items-center justify-between gap-4">
              <span className="text-muted-foreground">Of everyone active</span>
              <span className="font-semibold tabular-nums">{pct(b[hover].share)}</span>
            </div>
          </HoverCard>
        )}
      </div>
    </div>
  );
}
