import { useEffect, useRef, useState, type ReactNode } from "react";

// Shared pieces for the app's SVG charts: the measured width (so text and
// lines stay sharp at any size instead of scaling with a viewBox), round
// axis ticks, short numbers (1.2k, 3.4M), smooth curves and the hover card.

export function useWidth(initial = 640): [React.RefObject<HTMLDivElement | null>, number, number] {
  const ref = useRef<HTMLDivElement>(null);
  const [size, setSize] = useState({ w: initial, h: 0 });
  useEffect(() => {
    if (!ref.current) return;
    const o = new ResizeObserver(([e]) => setSize({ w: Math.max(200, Math.floor(e.contentRect.width)), h: Math.floor(e.contentRect.height) }));
    o.observe(ref.current);
    return () => o.disconnect();
  }, []);
  return [ref, size.w, size.h];
}

// niceTicks returns about `count` round steps from lo to hi (1, 2, 2.5 or
// 5 times a power of ten), starting at 0 when every value is 0 or more.
export function niceTicks(lo: number, hi: number, count = 4): number[] {
  if (!Number.isFinite(lo) || !Number.isFinite(hi)) return [0, 1];
  if (hi - lo < 1e-6) hi = lo + (Math.abs(lo) < 1e-6 ? 1 : Math.abs(lo));
  const raw = (hi - lo) / count;
  const p = Math.pow(10, Math.floor(Math.log10(raw)));
  const step = [1, 2, 2.5, 5, 10].map((m) => m * p).find((s) => s >= raw) ?? 10 * p;
  const start = Math.floor(lo / step) * step;
  const out: number[] = [];
  for (let t = start; t < hi + step * 0.999; t += step) out.push(Math.round(t / step) * step);
  if (out.length < 2) out.push(start + step);
  return out;
}

// short writes an axis number: 950, 1.2k, 34k, 1.5M.
export function short(v: number): string {
  const a = Math.abs(v);
  const s = v < 0 ? "−" : "";
  if (a >= 1e9) return `${s}${trim(a / 1e9)}B`;
  if (a >= 1e6) return `${s}${trim(a / 1e6)}M`;
  if (a >= 1e3) return `${s}${trim(a / 1e3)}k`;
  if (a === 0) return "0";
  return `${s}${a < 1 ? a.toFixed(2).replace(/0+$/, "").replace(/\.$/, "") : trim(a)}`;
}
const trim = (v: number) => (v >= 100 ? Math.round(v).toString() : v.toFixed(1).replace(/\.0$/, ""));

// smooth draws a line through the points as a monotone curve: it bends
// between points but never overshoots them, so a peak stays a peak.
export function smooth(pts: [number, number][]): string {
  const n = pts.length;
  if (n === 0) return "";
  if (n === 1) return `M${pts[0][0]},${pts[0][1]}`;
  if (n === 2) return `M${pts[0][0]},${pts[0][1]}L${pts[1][0]},${pts[1][1]}`;
  const dx: number[] = [], m: number[] = [], t: number[] = [];
  for (let i = 0; i < n - 1; i++) {
    dx.push(pts[i + 1][0] - pts[i][0]);
    m.push((pts[i + 1][1] - pts[i][1]) / (dx[i] || 1));
  }
  t.push(m[0]);
  for (let i = 1; i < n - 1; i++) t.push(m[i - 1] * m[i] <= 0 ? 0 : (3 * (dx[i - 1] + dx[i])) / ((2 * dx[i] + dx[i - 1]) / m[i - 1] + (dx[i] + 2 * dx[i - 1]) / m[i]));
  t.push(m[n - 2]);
  let d = `M${f(pts[0][0])},${f(pts[0][1])}`;
  for (let i = 0; i < n - 1; i++) {
    const h = dx[i] / 3;
    d += `C${f(pts[i][0] + h)},${f(pts[i][1] + h * t[i])} ${f(pts[i + 1][0] - h)},${f(pts[i + 1][1] - h * t[i + 1])} ${f(pts[i + 1][0])},${f(pts[i + 1][1])}`;
  }
  return d;
}
const f = (v: number) => v.toFixed(1);

// runs splits values into runs without a gap, as [index, value] pairs.
export function runs(values: (number | null | undefined)[]): [number, number][][] {
  const out: [number, number][][] = [];
  let cur: [number, number][] = [];
  values.forEach((v, i) => {
    if (v === null || v === undefined || !Number.isFinite(v)) {
      if (cur.length) out.push(cur);
      cur = [];
    } else cur.push([i, v]);
  });
  if (cur.length) out.push(cur);
  return out;
}

// HoverCard is the floating card beside the cursor: it sits to the right
// of the point, or to the left near the right edge.
export function HoverCard({ x, width, top = 8, children }: { x: number; width: number; top?: number; children: ReactNode }) {
  const right = x > width - 240;
  return (
    <div
      className="pointer-events-none absolute z-20 min-w-[160px] max-w-[280px] rounded-lg border bg-popover/95 px-3 py-2 text-[12px] text-popover-foreground shadow-lg backdrop-blur-sm"
      style={right ? { right: width - x + 12, top } : { left: x + 12, top }}
      role="status"
    >
      {children}
    </div>
  );
}

export function Swatch({ color, dashed, opacity = 1 }: { color: string; dashed?: boolean; opacity?: number }) {
  return dashed ? (
    <span className="inline-block h-0 w-3 shrink-0 border-t-2 border-dashed" style={{ borderColor: color, opacity }} />
  ) : (
    <span className="inline-block size-2.5 shrink-0 rounded-[3px]" style={{ background: color, opacity }} />
  );
}

// Chart series colours, in order (the --series tokens, chosen to stay
// apart for colour-blind readers, in light and dark).
export const seriesColor = (i: number) => (i < 6 ? `var(--series-${i + 1})` : "var(--muted-foreground)");
