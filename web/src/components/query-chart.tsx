import { useEffect, useMemo, useRef, useState } from "react";

// A query's results drawn as bars, lines or areas: one x column along the
// bottom and up to eight y columns on one shared axis. Series take the
// app's series colours in a fixed order (--series-1 to --series-6, then
// the muted ink), keyed by the column, so a column keeps its colour when
// another is added. The results table is the chart's table view.
//
// Other pages reuse it for a time series: gaps leaves a null as a gap in
// the line instead of a zero, fit scales the axis to the values instead of
// from zero (for a number such as a rating that moves within a band),
// styles sets a series' colour or draws it dashed (a period to compare
// with), and title names a point in the tooltip.

export type ChartType = "bar" | "line" | "area";
export type ChartSpec = { type: ChartType; x: string; y: string[] };
export type ChartOptions = {
  height?: number;
  gaps?: boolean;
  fit?: boolean;
  styles?: Record<string, { color?: string; dashed?: boolean }>;
  title?: (i: number) => string;
};
type Column = { name: string; type: string };

const series = (i: number) => (i < 6 ? `var(--series-${i + 1})` : "var(--muted-foreground)");
const numeric = /^(int2|int4|int8|float4|float8|numeric|money|oid|smallint|integer|bigint|real|double precision)$/;

export function numericColumns(columns: Column[], rows: unknown[][]): string[] {
  return columns
    .filter((c, i) => numeric.test(c.type) || (rows.length > 0 && rows.every((r) => r[i] === null || typeof r[i] === "number")))
    .map((c) => c.name);
}

// defaultChart picks a first chart: the first column that isn't a number
// along the bottom, and up to three number columns.
export function defaultChart(columns: Column[], rows: unknown[][]): ChartSpec | null {
  const nums = numericColumns(columns, rows);
  const x = columns.find((c) => !nums.includes(c.name))?.name ?? columns[0]?.name;
  const y = nums.filter((n) => n !== x).slice(0, 3);
  if (!x || y.length === 0) return null;
  return { type: "bar", x, y };
}

// niceStep rounds a step between ticks up to 1, 2, 2.5 or 5 times a power
// of ten.
function niceStep(v: number): number {
  if (v <= 0) return 1;
  const p = Math.pow(10, Math.floor(Math.log10(v)));
  for (const m of [1, 2, 2.5, 5, 10]) if (m * p >= v) return m * p;
  return 10 * p;
}

function niceMax(v: number): number {
  if (v <= 0) return 1;
  const p = Math.pow(10, Math.floor(Math.log10(v)));
  for (const m of [1, 2, 2.5, 5, 10]) if (m * p >= v) return m * p;
  return 10 * p;
}

const fmt = (v: number) => (Math.abs(v) >= 1000 ? v.toLocaleString(undefined, { maximumFractionDigits: 0 }) : v.toLocaleString(undefined, { maximumFractionDigits: 2 }));

function label(v: unknown): string {
  if (v === null || v === undefined) return "null";
  if (typeof v === "string") {
    // Times read as their day, or day and minute.
    if (/^\d{4}-\d{2}-\d{2}T/.test(v)) return v.endsWith("T00:00:00Z") ? v.slice(0, 10) : v.slice(0, 16).replace("T", " ");
    return v;
  }
  return typeof v === "object" ? JSON.stringify(v) : String(v);
}

export function QueryChart({ columns, rows, spec, height = 280, gaps = false, fit = false, styles = {}, title }: { columns: Column[]; rows: unknown[][]; spec: ChartSpec } & ChartOptions) {
  const box = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(720);
  const [hover, setHover] = useState<number | null>(null);
  useEffect(() => {
    if (!box.current) return;
    const o = new ResizeObserver(([e]) => setWidth(Math.max(280, Math.floor(e.contentRect.width))));
    o.observe(box.current);
    return () => o.disconnect();
  }, []);

  const xi = columns.findIndex((c) => c.name === spec.x);
  const ys = spec.y.map((name) => ({ name, i: columns.findIndex((c) => c.name === name) })).filter((y) => y.i >= 0);
  const data = useMemo(
    () => rows.map((r) => ({ x: label(r[xi]), v: ys.map((y) => (typeof r[y.i] === "number" ? (r[y.i] as number) : gaps && (r[y.i] === null || r[y.i] === undefined) ? null : Number(r[y.i]) || 0)) })),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [rows, xi, gaps, spec.y.join(",")],
  );
  if (xi < 0 || ys.length === 0 || data.length === 0) {
    return <p className="p-4 text-[13px] text-muted-foreground">Pick a column along the bottom and at least one number column to draw.</p>;
  }

  const pad = { top: 12, right: 12, bottom: 28, left: 56 };
  const w = width - pad.left - pad.right;
  const h = height - pad.top - pad.bottom;
  const all = data.flatMap((d) => d.v).filter((v): v is number => v !== null);
  let lo = Math.min(0, ...all);
  let hi = niceMax(Math.max(0, ...all));
  let ticks = [0, 0.25, 0.5, 0.75, 1].map((t) => lo + t * (hi - lo));
  if (fit && all.length > 0) {
    // Four steps that hold the values, starting at a round number.
    const min = Math.min(...all), max = Math.max(...all);
    const tick = niceStep((max - min || Math.abs(max) || 1) / 4);
    lo = Math.floor(min / tick) * tick;
    hi = Math.max(Math.ceil(max / tick) * tick, lo + tick);
    if (min >= 0 && lo < 0) lo = 0;
    ticks = [];
    for (let t = lo; t <= hi + tick / 2; t += tick) ticks.push(t);
  }
  const span = hi - lo || 1;
  const y = (v: number) => pad.top + h - ((v - lo) / span) * h;
  const step = w / data.length;
  const cx = (i: number) => pad.left + step * (i + 0.5);
  const color = (k: number) => styles[ys[k].name]?.color ?? series(k);
  const every = Math.max(1, Math.ceil(data.length / Math.max(2, Math.floor(w / 90))));

  // Bars: one group per x, a 2px gap between bars of a group.
  const groupW = Math.max(1, step * 0.8);
  const barW = Math.max(1, (groupW - 2 * (ys.length - 1)) / ys.length);
  const r = Math.min(4, barW / 2);

  // A null starts a new piece of the line.
  const path = (k: number) => {
    let out = "", gap = true;
    data.forEach((d, i) => {
      const v = d.v[k];
      if (v === null) return void (gap = true);
      out += `${gap ? "M" : "L"}${cx(i).toFixed(1)},${y(v).toFixed(1)}`;
      gap = false;
    });
    return out;
  };
  // A point with no neighbour on either side gets a dot, or it wouldn't show.
  const alone = (k: number, i: number) => data[i].v[k] !== null && (i === 0 || data[i - 1].v[k] === null) && (i === data.length - 1 || data[i + 1].v[k] === null);

  function onMove(e: React.MouseEvent<SVGRectElement>) {
    const rect = e.currentTarget.getBoundingClientRect();
    const i = Math.floor(((e.clientX - rect.left) / rect.width) * data.length);
    setHover(i >= 0 && i < data.length ? i : null);
  }

  const total = data.length;
  return (
    <div ref={box} className="relative">
      {ys.length > 1 && (
        <ul className="mb-2 flex flex-wrap gap-x-4 gap-y-1 text-[12px] text-muted-foreground" aria-label="Legend">
          {ys.map((s, k) => (
            <li key={s.name} className="flex items-center gap-1.5">
              <span className="inline-block size-2.5 rounded-[3px]" style={{ background: color(k), opacity: styles[s.name]?.dashed ? 0.6 : 1 }} />
              <span className="text-foreground">{s.name}</span>
            </li>
          ))}
        </ul>
      )}
      <svg width={width} height={height} role="img" aria-label={`${spec.type} chart of ${ys.map((s) => s.name).join(", ")} by ${spec.x}, ${total} points`} className="block">
        {ticks.map((t) => (
          <g key={t}>
            <line x1={pad.left} x2={pad.left + w} y1={y(t)} y2={y(t)} stroke="var(--border)" strokeWidth={1} />
            <text x={pad.left - 6} y={y(t)} dy="0.32em" textAnchor="end" className="fill-muted-foreground font-mono text-[11px]">{fmt(t)}</text>
          </g>
        ))}
        {data.map((d, i) =>
          i % every === 0 ? (
            <text key={i} x={cx(i)} y={height - 8} textAnchor="middle" className="fill-muted-foreground font-mono text-[11px]">
              {d.x.length > 14 ? d.x.slice(0, 13) + "…" : d.x}
            </text>
          ) : null,
        )}
        {spec.type === "bar" &&
          data.map((d, i) =>
            d.v.map((v, k) => {
              if (v === null) return null;
              const x0 = cx(i) - groupW / 2 + k * (barW + 2);
              const top = y(Math.max(v, 0));
              const bh = Math.abs(y(v) - y(0));
              if (bh <= 0) return null;
              // Rounded at the data end, square at the baseline.
              const d0 = v >= 0
                ? `M${x0},${top + bh}V${top + r}Q${x0},${top} ${x0 + r},${top}H${x0 + barW - r}Q${x0 + barW},${top} ${x0 + barW},${top + r}V${top + bh}Z`
                : `M${x0},${top}V${top + bh - r}Q${x0},${top + bh} ${x0 + r},${top + bh}H${x0 + barW - r}Q${x0 + barW},${top + bh} ${x0 + barW},${top + bh - r}V${top}Z`;
              return <path key={`${i}-${k}`} d={d0} fill={color(k)} opacity={hover === null || hover === i ? 1 : 0.55} />;
            }),
          )}
        {spec.type !== "bar" &&
          ys.map((s, k) => (
            <g key={s.name}>
              {spec.type === "area" && !gaps && (
                <path d={`${path(k)}L${cx(data.length - 1).toFixed(1)},${y(0)}L${cx(0).toFixed(1)},${y(0)}Z`} fill={color(k)} opacity={0.14} />
              )}
              <path d={path(k)} fill="none" stroke={color(k)} strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" strokeDasharray={styles[s.name]?.dashed ? "4 4" : undefined} opacity={styles[s.name]?.dashed ? 0.6 : 1} />
              {data.map((d, i) => (alone(k, i) ? <circle key={i} cx={cx(i)} cy={y(d.v[k] as number)} r={3} fill={color(k)} /> : null))}
            </g>
          ))}
        {lo <= 0 && hi >= 0 && <line x1={pad.left} x2={pad.left + w} y1={y(0)} y2={y(0)} stroke="var(--muted-foreground)" strokeWidth={1} opacity={0.5} />}
        {hover !== null && spec.type !== "bar" && (
          <>
            <line x1={cx(hover)} x2={cx(hover)} y1={pad.top} y2={pad.top + h} stroke="var(--muted-foreground)" strokeWidth={1} strokeDasharray="3 3" />
            {ys.map((s, k) => {
              const v = data[hover].v[k];
              return v === null ? null : <circle key={s.name} cx={cx(hover)} cy={y(v)} r={4} fill={color(k)} stroke="var(--card)" strokeWidth={2} />;
            })}
          </>
        )}
        <rect x={pad.left} y={pad.top} width={w} height={h} fill="transparent" onMouseMove={onMove} onMouseLeave={() => setHover(null)} />
      </svg>
      {hover !== null && (
        <div
          className="pointer-events-none absolute z-10 rounded-md border bg-popover px-2 py-1 text-[12px] text-popover-foreground shadow-md"
          style={{ left: Math.min(cx(hover) + 8, width - 180), top: ys.length > 1 ? 32 : 8 }}
        >
          <div className="font-medium">{title ? title(hover) : data[hover].x}</div>
          {ys.map((s, k) => {
            const v = data[hover].v[k];
            return (
              <div key={s.name} className="flex items-center gap-1.5">
                <span className="inline-block size-2 rounded-[2px]" style={{ background: color(k) }} />
                {s.name}: <span className="font-mono">{v === null ? "–" : fmt(v)}</span>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}
