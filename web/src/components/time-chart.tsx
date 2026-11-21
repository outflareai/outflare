import { useId, useMemo, useState } from "react";
import { forecast, horizonFor, type Forecast } from "@/lib/forecast";
import { HoverCard, niceTicks, runs, seriesColor, short, smooth, Swatch, useWidth } from "./chart-kit";

// TimeChart draws numbers over time as smooth lines with a soft fill, or
// as bars. Hovering shows the exact numbers for that period beside the
// cursor, with the period before and the change when there is one. The
// period still running is drawn dashed ("so far"), and the forecast
// continues each line in a lighter dashed stroke inside a band of the
// likely range, behind a "Now" marker.

export type TimeSeries = { label: string; values: (number | null)[]; previous?: (number | null)[] | null; currency?: string };
export type TimeNote = { id: string; day: string; text: string };
type Interval = "day" | "week" | "month";

const DAY = 86400000;

export function intervalOf(starts: string[]): Interval {
  if (starts.length < 2) return "day";
  const d = (Date.parse(starts[1]) - Date.parse(starts[0])) / DAY;
  return d <= 1.5 ? "day" : d <= 8 ? "week" : "month";
}

function addPeriods(iso: string, interval: Interval, k: number): Date {
  const d = new Date(iso);
  if (interval === "day") return new Date(d.getTime() + k * DAY);
  if (interval === "week") return new Date(d.getTime() + 7 * k * DAY);
  return new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + k, 1));
}

const dayFmt = (d: Date, o: Intl.DateTimeFormatOptions) => d.toLocaleDateString(undefined, { timeZone: "UTC", ...o });

// tickLabel is the short date under the axis: 3 Nov, or Nov for months.
function tickLabel(d: Date, interval: Interval, first: boolean): string {
  if (interval === "month") return dayFmt(d, first || d.getUTCMonth() === 0 ? { month: "short", year: "numeric" } : { month: "short" });
  return dayFmt(d, { day: "numeric", month: "short" });
}

// periodLabel names a whole period in the hover card.
function periodLabel(d: Date, interval: Interval): string {
  if (interval === "day") return dayFmt(d, { weekday: "short", day: "numeric", month: "short", year: "numeric" });
  if (interval === "month") return dayFmt(d, { month: "long", year: "numeric" });
  const end = new Date(d.getTime() + 6 * DAY);
  return `${dayFmt(d, { day: "numeric", month: "short" })} – ${dayFmt(end, { day: "numeric", month: "short", year: "numeric" })}`;
}

function change(now: number | null | undefined, before: number | null | undefined): number | null {
  if (now === null || now === undefined || before === null || before === undefined || Math.abs(before) < 1e-9) return null;
  return (now - before) / Math.abs(before);
}

// forecastable says whether the chart will draw a forecast for these
// series: three or fewer, each with six finished periods and no gaps.
export function forecastable(starts: string[], series: { values: (number | null)[] }[]): boolean {
  const n = starts.length;
  if (n === 0 || series.length === 0 || series.length > 3) return false;
  const partial = addPeriods(starts[n - 1], intervalOf(starts), 1).getTime() > Date.now();
  const complete = partial ? n - 1 : n;
  return complete >= 6 && series.some((s) => s.values.slice(0, complete).every((v) => v !== null && v !== undefined));
}

export function TimeChart({
  starts,
  series,
  bars = false,
  stacked = false,
  area = false,
  valueLabels = false,
  rightAxis = [],
  unit = "",
  height: wanted = 240,
  showForecast = false,
  notes = [],
  format,
  label = "Chart",
}: {
  starts: string[];
  series: TimeSeries[];
  bars?: boolean;
  // Stacked puts the series on top of each other (bars in one column,
  // lines as filled bands) so the top edge is their total.
  stacked?: boolean;
  // Area fills under each line more strongly.
  area?: boolean;
  // ValueLabels writes the number on each point or bar, as many as fit.
  valueLabels?: boolean;
  // RightAxis lists the series read against a second axis on the right.
  rightAxis?: number[];
  // Unit follows the axis numbers: "%" for shares.
  unit?: string;
  // A number of pixels, or "fill" for the height of the box around it
  // (a dashboard tile), never under 140.
  height?: number | "fill";
  showForecast?: boolean;
  notes?: TimeNote[];
  format: (v: number | null | undefined, currency?: string) => string;
  label?: string;
}) {
  const uid = useId().replace(/:/g, "");
  const [box, width, boxH] = useWidth();
  const height = wanted === "fill" ? Math.max(140, boxH) : wanted;
  const [hover, setHover] = useState<{ i: number; x: number } | null>(null);
  const n = starts.length;
  const interval = intervalOf(starts);
  const partial = n > 0 && addPeriods(starts[n - 1], interval, 1).getTime() > Date.now();
  const complete = partial ? n - 1 : n;

  // One forecast per series, from the finished periods only. Too many
  // lines would turn the chart into noise, so it stops at three series.
  const forecasts = useMemo<(Forecast | null)[]>(() => {
    if (!showForecast || stacked || series.length > 3 || complete < 6) return series.map(() => null);
    const h = horizonFor(interval, complete) + (partial ? 1 : 0);
    const season = interval === "day" ? 7 : 0;
    return series.map((s) => {
      const vals = s.values.slice(0, complete);
      if (vals.some((v) => v === null || v === undefined)) return null;
      return forecast(vals as number[], h, season);
    });
  }, [series, showForecast, stacked, complete, interval, partial]);
  const horizon = Math.max(0, ...forecasts.map((f) => f?.mean.length ?? 0));
  const slots = Math.max(n, complete + horizon);

  const tickW = 8;
  const num = (v: unknown): v is number => typeof v === "number" && Number.isFinite(v);
  // Stacked: each series sits on the ones before it.
  const base = series.map((_, k) => Array.from({ length: n }, (_, i) => (stacked ? series.slice(0, k).reduce((a, s) => a + (num(s.values[i]) ? Math.max(0, s.values[i] as number) : 0), 0) : 0)));
  const onRight = (k: number) => !stacked && rightAxis.includes(k) && rightAxis.length < series.length;
  const hasRight = series.some((_, k) => onRight(k));
  const nTicks = height < 180 ? 3 : 4;
  // One scale for the left axis and, when asked, one for the right.
  const scale = (right: boolean) => {
    const ks = series.map((_, k) => k).filter((k) => onRight(k) === right);
    const actual = stacked
      ? Array.from({ length: n }, (_, i) => series.reduce((a, s) => a + (num(s.values[i]) ? Math.max(0, s.values[i] as number) : 0), 0))
      : ks.flatMap((k) => [...series[k].values, ...(series[k].previous ?? [])]).filter(num);
    const fs = ks.map((k) => forecasts[k]);
    const peak = Math.max(0, ...actual);
    const projected = fs.flatMap((f) => (f ? [...f.mean, ...f.hi.map((v) => Math.min(v, peak * 1.6 || v))] : []));
    const lo0 = Math.min(0, ...actual, ...fs.flatMap((f) => (f ? f.lo : [])));
    const ticks = unit === "%" && stacked ? [0, 25, 50, 75, 100] : niceTicks(lo0, Math.max(...actual, ...projected, lo0 + 1e-9), nTicks);
    return { ticks, lo: ticks[0], hi: ticks[ticks.length - 1] };
  };
  const L = scale(false);
  const R = hasRight ? scale(true) : null;
  const ticks = L.ticks, lo = L.lo, hi = L.hi;
  const tickText = (t: number) => short(t) + unit;
  const padL = Math.max(28, ...ticks.map((t) => tickText(t).length * 6.5 + tickW));
  const padR = R ? Math.max(28, ...R.ticks.map((t) => short(t).length * 6.5 + tickW)) : 12;
  const pad = { t: 14, r: padR, b: 26, l: padL };
  const w = Math.max(10, width - pad.l - pad.r);
  const h = height - pad.t - pad.b;
  const slotW = w / Math.max(1, slots);
  const x = (i: number) => pad.l + (bars || slots <= 1 ? slotW * (i + 0.5) : (w * i) / (slots - 1));
  const y = (v: number) => pad.t + h - ((v - lo) / (hi - lo || 1)) * h;
  const yR = (v: number) => (R ? pad.t + h - ((v - R.lo) / (R.hi - R.lo || 1)) * h : y(v));
  const yk = (k: number) => (onRight(k) ? yR : y);
  const color = seriesColor;

  // Labels along the bottom, at least 64px apart, never cut off at an edge.
  const every = Math.max(1, Math.ceil(slots / Math.max(2, Math.floor(w / 64))));
  const dates = Array.from({ length: slots }, (_, i) => (i < n ? new Date(starts[i]) : addPeriods(starts[n - 1], interval, i - n + 1)));

  // Notes sit on the period their day falls in.
  const periodOf = (day: string) => {
    let j = -1;
    starts.forEach((s, k) => {
      if (s.slice(0, 10) <= day) j = k;
    });
    return j;
  };
  const noteIdx = notes.map((a) => ({ a, j: periodOf(a.day) })).filter((x) => x.j >= 0);

  function onMove(e: React.PointerEvent<SVGRectElement>) {
    const rect = e.currentTarget.getBoundingClientRect();
    const mx = e.clientX - rect.left;
    const i = bars || slots <= 1 ? Math.floor(mx / slotW) : Math.round((mx / w) * (slots - 1));
    if (i < 0 || i >= slots) return setHover(null);
    setHover({ i, x: x(i) });
  }

  if (n === 0) return null;
  // "Now" sits after the last finished period; the one still running
  // and everything after it are on the forecast's side.
  const nowX = horizon > 0 ? (bars ? pad.l + slotW * complete : x(complete - 1)) : null;
  const barW = Math.max(2, Math.min(stacked ? 56 : 36, (slotW * 0.72) / (stacked ? 1 : series.length)));
  // Number labels: as many as fit without touching.
  const labelEvery = Math.max(1, Math.ceil(38 / Math.max(1, bars && !stacked ? slotW / series.length : slotW)));
  const showLabels = valueLabels && (stacked || series.length <= 3);

  return (
    <div ref={box} className={`relative select-none ${wanted === "fill" ? "h-full min-h-[140px]" : ""}`}>
      <svg width={width} height={height} className={`block overflow-visible ${wanted === "fill" ? "absolute inset-0" : ""}`} role="img" aria-label={label} onPointerLeave={() => setHover(null)}>
        <defs>
          {series.map((_, k) => (
            <linearGradient key={k} id={`${uid}-g${k}`} x1="0" x2="0" y1="0" y2="1">
              <stop offset="0%" stopColor={color(k)} stopOpacity={area ? 0.4 : series.length === 1 ? 0.28 : 0.12} />
              <stop offset="100%" stopColor={color(k)} stopOpacity={0} />
            </linearGradient>
          ))}
          <pattern id={`${uid}-hatch`} width="6" height="6" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
            <line x1="0" y1="0" x2="0" y2="6" stroke="currentColor" strokeOpacity={0.1} strokeWidth="1.5" />
          </pattern>
        </defs>

        {/* The forecast's side of the chart, lightly shaded. */}
        {nowX !== null && (
          <g>
            <rect x={nowX} y={pad.t} width={pad.l + w - nowX} height={h} fill={`url(#${uid}-hatch)`} className="text-muted-foreground" opacity={0.6} />
            <line x1={nowX} x2={nowX} y1={pad.t - 4} y2={pad.t + h} stroke="var(--muted-foreground)" strokeOpacity={0.55} strokeDasharray="2 3" />
            <text x={nowX + 6} y={pad.t + 6} className="fill-muted-foreground text-[10px] font-medium uppercase tracking-wide">Forecast</text>
          </g>
        )}

        {ticks.map((t) => (
          <g key={t}>
            <line x1={pad.l} x2={pad.l + w} y1={y(t)} y2={y(t)} stroke="var(--border)" strokeDasharray={t === 0 ? undefined : "3 4"} />
            <text x={pad.l - tickW} y={y(t)} dy="0.32em" textAnchor="end" className="fill-muted-foreground text-[11px] tabular-nums">{tickText(t)}</text>
          </g>
        ))}
        {R?.ticks.map((t) => (
          <text key={`r${t}`} x={pad.l + w + tickW} y={yR(t)} dy="0.32em" textAnchor="start" className="fill-muted-foreground text-[11px] tabular-nums">{short(t)}</text>
        ))}
        {dates.map((d, i) =>
          i % every === 0 ? (
            <text key={i} x={Math.min(Math.max(x(i), pad.l + 18), pad.l + w - 18)} y={height - 7} textAnchor="middle" className={`text-[11px] ${i >= n ? "fill-muted-foreground/60" : "fill-muted-foreground"}`}>
              {tickLabel(d, interval, i === 0)}
            </text>
          ) : null,
        )}

        {hover !== null && bars && <rect x={pad.l + slotW * hover.i} y={pad.t} width={slotW} height={h} fill="var(--foreground)" opacity={0.05} rx={4} />}

        {bars
          ? series.map((s, k) => {
              const f = forecasts[k];
              const x0 = (i: number) => (stacked ? x(i) - barW / 2 + 1 : x(i) - (barW * series.length) / 2 + k * barW + 1);
              const Y = yk(k);
              const topK = stacked ? series.length - 1 : k;
              const bar = (i: number, v: number, key: string, style: React.SVGProps<SVGPathElement>) => {
                const b = stacked ? base[k][i] ?? 0 : 0;
                const top = Y(Math.max(v, 0) + b);
                const bh = Math.abs(Y(v + b) - Y(b));
                if (bh < 0.5) return null;
                const bw = barW - 2;
                // In a stack only the top piece has round corners.
                const r = stacked && (k !== topK && series.slice(k + 1).some((o) => num(o.values[i]) && (o.values[i] as number) > 0)) ? 0 : Math.min(4, bw / 2, bh);
                const left = x0(i);
                const d = v >= 0
                  ? `M${left},${top + bh}V${top + r}Q${left},${top} ${left + r},${top}H${left + bw - r}Q${left + bw},${top} ${left + bw},${top + r}V${top + bh}Z`
                  : `M${left},${top}V${top + bh - r}Q${left},${top + bh} ${left + r},${top + bh}H${left + bw - r}Q${left + bw},${top + bh} ${left + bw},${top + bh - r}V${top}Z`;
                return <path key={key} d={d} {...style} />;
              };
              return (
                <g key={s.label + k}>
                  {s.values.map((v, i) =>
                    v === null || v === undefined
                      ? null
                      : bar(i, v, `b${i}`, {
                          fill: color(k),
                          fillOpacity: partial && i === n - 1 ? 0.4 : hover === null || hover.i === i ? 1 : 0.7,
                          stroke: partial && i === n - 1 ? color(k) : undefined,
                          strokeDasharray: partial && i === n - 1 ? "3 2" : undefined,
                        }),
                  )}
                  {f?.mean.map((v, j) => {
                    const i = complete + j;
                    if (partial && i === n - 1) return null;
                    return bar(i, v, `f${j}`, { fill: color(k), fillOpacity: 0.18, stroke: color(k), strokeOpacity: 0.6, strokeDasharray: "3 2" });
                  })}
                  {showLabels && !stacked && s.values.map((v, i) =>
                    num(v) && i % labelEvery === 0 ? (
                      <text key={`l${i}`} x={x0(i) + (barW - 2) / 2} y={Y(Math.max(v, 0)) - 4} textAnchor="middle" className="fill-foreground text-[10px] tabular-nums">{short(v)}{unit}</text>
                    ) : null,
                  )}
                  {showLabels && stacked && k === series.length - 1 && s.values.map((_, i) => {
                    const total = series.reduce((a, o) => a + (num(o.values[i]) ? Math.max(0, o.values[i] as number) : 0), 0);
                    return total > 0 && i % labelEvery === 0 && unit !== "%" ? (
                      <text key={`l${i}`} x={x(i)} y={y(total) - 4} textAnchor="middle" className="fill-foreground text-[10px] tabular-nums">{short(total)}</text>
                    ) : null;
                  })}
                </g>
              );
            })
          : series.map((s, k) => {
              const f = forecasts[k];
              const Y = yk(k);
              if (stacked) {
                // A band from the series below up to this one's top edge.
                const top = s.values.map((v, i) => [x(i), y((base[k][i] ?? 0) + (num(v) ? Math.max(0, v) : 0))] as [number, number]);
                const bottom = s.values.map((_, i) => [x(i), y(base[k][i] ?? 0)] as [number, number]);
                const line = smooth(top);
                const back = smooth(bottom.slice().reverse()).replace(/^M/, "L");
                return (
                  <g key={s.label + k}>
                    <path d={`${line}${back}Z`} fill={color(k)} fillOpacity={hover === null ? 0.55 : 0.45} />
                    <path d={line} fill="none" stroke={color(k)} strokeWidth={1.5} strokeLinejoin="round" />
                  </g>
                );
              }
              const done = runs(s.values.slice(0, complete));
              const last = complete - 1;
              const lastV = s.values[last];
              const tail = partial && typeof s.values[n - 1] === "number" && typeof s.values[n - 2] === "number"
                ? `M${x(n - 2)},${Y(s.values[n - 2] as number)}L${x(n - 1)},${Y(s.values[n - 1] as number)}`
                : null;
              const prev = s.previous ? runs(s.previous) : [];
              const anchor = typeof lastV === "number" ? lastV : null;
              const sc = onRight(k) && R ? R : L;
              const fPts: [number, number][] = f && anchor !== null ? [[x(last), Y(anchor)], ...f.mean.map((v, j) => [x(complete + j), Y(v)] as [number, number])] : [];
              const band = f && anchor !== null
                ? `M${x(last)},${Y(anchor)}` + f.hi.map((v, j) => `L${x(complete + j)},${Y(Math.min(v, sc.hi))}`).join("") + f.lo.slice().reverse().map((v, j) => `L${x(complete + f.lo.length - 1 - j)},${Y(Math.max(v, sc.lo))}`).join("") + "Z"
                : null;
              return (
                <g key={s.label + k}>
                  {prev.map((r, ri) => (
                    <path key={`p${ri}`} d={smooth(r.map(([i, v]) => [x(i), Y(v)]))} fill="none" stroke={color(k)} strokeOpacity={0.4} strokeWidth={1.5} strokeDasharray="4 4" />
                  ))}
                  {done.map((r, ri) => {
                    const pts = r.map(([i, v]) => [x(i), Y(v)] as [number, number]);
                    const line = smooth(pts);
                    const floor = Y(Math.max(sc.lo, 0));
                    const fill = `${line}L${pts[pts.length - 1][0]},${floor}L${pts[0][0]},${floor}Z`;
                    return (
                      <g key={ri}>
                        {(area || series.length <= 3) && <path d={fill} fill={`url(#${uid}-g${k})`} />}
                        <path d={line} fill="none" stroke={color(k)} strokeWidth={2.25} strokeLinejoin="round" strokeLinecap="round" />
                        {r.length === 1 && <circle cx={pts[0][0]} cy={pts[0][1]} r={3} fill={color(k)} />}
                        {showLabels && r.map(([i, v]) =>
                          i % labelEvery === 0 ? (
                            <text key={`l${i}`} x={Math.min(Math.max(x(i), pad.l + 10), pad.l + w - 10)} y={Y(v) - 8} textAnchor="middle" className="fill-foreground text-[10px] tabular-nums" stroke="var(--card)" strokeWidth={3} paintOrder="stroke">{short(v)}{unit}</text>
                          ) : null,
                        )}
                      </g>
                    );
                  })}
                  {band && <path d={band} fill={color(k)} fillOpacity={0.1} />}
                  {fPts.length > 1 && <path d={smooth(fPts)} fill="none" stroke={color(k)} strokeOpacity={0.75} strokeWidth={2} strokeDasharray="5 5" strokeLinecap="round" />}
                  {tail && <path d={tail} fill="none" stroke={color(k)} strokeWidth={2.25} strokeDasharray="2 4" strokeLinecap="round" />}
                  {partial && typeof s.values[n - 1] === "number" && <circle cx={x(n - 1)} cy={Y(s.values[n - 1] as number)} r={3} fill="var(--card)" stroke={color(k)} strokeWidth={2} />}
                </g>
              );
            })}

        {noteIdx.map(({ a, j }) => (
          <g key={a.id}>
            <line x1={x(j)} x2={x(j)} y1={pad.t} y2={pad.t + h} stroke="var(--muted-foreground)" strokeOpacity={0.4} strokeDasharray="2 3" />
            <circle cx={x(j)} cy={pad.t + h} r={4} fill="var(--series-4)" stroke="var(--card)" strokeWidth={1.5} />
          </g>
        ))}

        {hover !== null && !bars && (
          <g>
            <line x1={hover.x} x2={hover.x} y1={pad.t} y2={pad.t + h} stroke="var(--foreground)" strokeOpacity={0.25} />
            {series.map((s, k) => {
              const v = hover.i < n && !(partial && hover.i === n - 1 && forecasts[k]) ? s.values[hover.i] : forecasts[k]?.mean[hover.i - complete];
              const actualHere = hover.i < n ? s.values[hover.i] : null;
              return (
                <g key={k}>
                  {typeof actualHere === "number" && <circle cx={hover.x} cy={yk(k)(actualHere + (stacked ? base[k][hover.i] ?? 0 : 0))} r={stacked ? 3.5 : 4.5} fill={color(k)} stroke="var(--card)" strokeWidth={2} />}
                  {typeof v === "number" && v !== actualHere && <circle cx={hover.x} cy={yk(k)(v)} r={4} fill="var(--card)" stroke={color(k)} strokeWidth={2} />}
                </g>
              );
            })}
          </g>
        )}
        <rect x={pad.l} y={0} width={w} height={height} fill="transparent" onPointerMove={onMove} onPointerDown={onMove} />
      </svg>

      {hover !== null && (
        <HoverCard x={hover.x} width={width}>
          <Card
            i={hover.i}
            date={dates[hover.i]}
            interval={interval}
            series={series}
            forecasts={forecasts}
            n={n}
            complete={complete}
            partial={partial && hover.i === n - 1}
            notes={noteIdx.filter((x) => x.j === hover.i).map((x) => x.a)}
            format={format}
            total={stacked && unit !== "%" && series.length > 1}
          />
        </HoverCard>
      )}
    </div>
  );
}

function Card({
  i, date, interval, series, forecasts, n, complete, partial, notes, format, total,
}: {
  i: number; date: Date; interval: Interval; series: TimeSeries[]; forecasts: (Forecast | null)[]; n: number; complete: number; partial: boolean; notes: TimeNote[];
  format: (v: number | null | undefined, currency?: string) => string;
  total?: boolean;
}) {
  const future = i >= n;
  // A forecast of whole things (orders, people) is shown as whole numbers.
  const whole = series.map((s) => s.values.every((v) => v === null || v === undefined || Number.isInteger(v)));
  const est = (k: number, v: number) => format(whole[k] ? Math.round(v) : v, series[k].currency);
  return (
    <>
      <div className="mb-1.5 flex items-center gap-2">
        <span className="font-medium">{periodLabel(date, interval)}</span>
        {future && <span className="rounded bg-muted px-1.5 py-px text-[10px] font-medium uppercase tracking-wide text-muted-foreground">Forecast</span>}
        {partial && <span className="rounded bg-muted px-1.5 py-px text-[10px] font-medium uppercase tracking-wide text-muted-foreground">So far</span>}
      </div>
      <div className="space-y-1.5">
        {series.map((s, k) => {
          const f = forecasts[k];
          const j = i - complete;
          const v = future ? null : s.values[i];
          const p = s.previous?.[i];
          const c = partial ? null : change(v, p);
          return (
            <div key={k}>
              <div className="flex items-center gap-2">
                <Swatch color={seriesColor(k)} />
                <span className="min-w-0 flex-1 truncate text-muted-foreground" title={s.label}>{s.label}</span>
                <span className="font-semibold tabular-nums">
                  {future ? (f && j >= 0 ? `≈ ${est(k, f.mean[j])}` : "–") : format(v, s.currency)}
                </span>
              </div>
              {(future || partial) && f && j >= 0 && j < f.mean.length && (
                <div className="pl-[18px] text-[11px] text-muted-foreground tabular-nums">
                  {partial ? `Expected ≈ ${est(k, f.mean[j])} · likely ` : "Likely "}
                  {est(k, f.lo[j])} – {est(k, f.hi[j])}
                </div>
              )}
              {!future && s.previous && (
                <div className="flex items-center gap-1.5 pl-[18px] text-[11px] text-muted-foreground tabular-nums">
                  Period before {format(p, s.currency)}
                  {c !== null && (
                    <span className={`rounded px-1 font-medium ${c >= 0 ? "bg-green-500/10 text-green-700 dark:text-green-400" : "bg-red-500/10 text-red-600 dark:text-red-400"}`}>
                      {c >= 0 ? "▲" : "▼"} {Math.abs(c * 100).toFixed(Math.abs(c) < 0.1 ? 1 : 0)}%
                    </span>
                  )}
                </div>
              )}
            </div>
          );
        })}
        {total && !future && (
          <div className="flex items-center gap-2 border-t pt-1.5">
            <span className="min-w-0 flex-1 pl-[18px] text-muted-foreground">Total</span>
            <span className="font-semibold tabular-nums">{format(series.reduce((a, s) => a + (typeof s.values[i] === "number" ? (s.values[i] as number) : 0), 0), series[0]?.currency)}</span>
          </div>
        )}
      </div>
      {notes.map((a) => (
        <div key={a.id} className="mt-2 border-t pt-1.5 text-muted-foreground">
          <span className="mr-1 inline-block size-2 rounded-full bg-[var(--series-4)]" />
          {a.text}
        </div>
      ))}
    </>
  );
}
