// forecast projects a series a few periods ahead, for the dashed
// "Forecast" part of a trend chart. It runs in the browser on the numbers
// the chart already has: no model, no AI and nothing saved.
//
// The method is Holt's linear trend with a damped slope, so a run of
// growth flattens out instead of shooting off, and, for daily numbers
// with three weeks or more, a weekly pattern (weekends) added back on
// top. The smoothing weights are picked by trying a small grid and
// keeping the pair with the smallest one-step-ahead error. The band is
// about 80% likely to hold the real number: 1.28 of those errors' spread,
// widening with each step ahead.

export type Forecast = { mean: number[]; lo: number[]; hi: number[] };

const Z80 = 1.28;
const PHI = 0.9;

export function forecast(values: number[], horizon: number, season = 0): Forecast | null {
  const n = values.length;
  if (n < 6 || horizon < 1 || values.some((v) => !Number.isFinite(v))) return null;
  const seasonal = season > 1 && n >= season * 3 ? pattern(values, season) : null;
  const flat = seasonal ? values.map((v, i) => v - seasonal[i % season]) : values;

  let best: { sse: number; level: number; slope: number; alpha: number; errors: number[] } | null = null;
  for (const alpha of [0.2, 0.35, 0.5, 0.65, 0.8]) {
    for (const beta of [0.05, 0.1, 0.2, 0.35]) {
      let level = flat[0];
      let slope = (flat[Math.min(3, n - 1)] - flat[0]) / Math.min(3, n - 1);
      let sse = 0;
      const errors: number[] = [];
      for (let i = 1; i < n; i++) {
        const guess = level + PHI * slope;
        const e = flat[i] - guess;
        if (i >= 2) {
          sse += e * e;
          errors.push(e);
        }
        const next = alpha * flat[i] + (1 - alpha) * guess;
        slope = beta * (next - level) + (1 - beta) * PHI * slope;
        level = next;
      }
      if (!best || sse < best.sse) best = { sse, level, slope, alpha, errors };
    }
  }
  if (!best) return null;
  const sd = Math.sqrt(best.errors.reduce((a, e) => a + e * e, 0) / Math.max(1, best.errors.length));
  const nonNegative = values.every((v) => v >= 0);
  const out: Forecast = { mean: [], lo: [], hi: [] };
  let damp = 0;
  for (let h = 1; h <= horizon; h++) {
    damp += Math.pow(PHI, h);
    let m = best.level + damp * best.slope;
    if (seasonal) m += seasonal[(n + h - 1) % season];
    // The spread grows with the steps ahead, as each step adds its own error.
    const spread = Z80 * sd * Math.sqrt(1 + (h - 1) * best.alpha * best.alpha);
    let lo = m - spread;
    let hi = m + spread;
    if (nonNegative) {
      m = Math.max(0, m);
      lo = Math.max(0, lo);
      hi = Math.max(0, hi);
    }
    out.mean.push(m);
    out.lo.push(lo);
    out.hi.push(hi);
  }
  return out;
}

// pattern is how far each position in the season (each weekday) sits
// above or below a moving average, on average, so the pattern sums to 0.
function pattern(values: number[], season: number): number[] {
  const n = values.length;
  const half = Math.floor(season / 2);
  const sums = new Array(season).fill(0);
  const counts = new Array(season).fill(0);
  for (let i = half; i < n - half; i++) {
    let s = 0;
    for (let k = i - half; k <= i + half; k++) s += values[k];
    const avg = s / (2 * half + 1);
    sums[i % season] += values[i] - avg;
    counts[i % season] += 1;
  }
  const p = sums.map((s, i) => (counts[i] ? s / counts[i] : 0));
  const mean = p.reduce((a, b) => a + b, 0) / season;
  return p.map((v) => v - mean);
}

// horizonFor says how many periods ahead to project: about a quarter of
// what the chart shows, at most two weeks of days, two months of weeks or
// six months.
export function horizonFor(interval: "day" | "week" | "month", n: number): number {
  const q = Math.round(n / 4);
  if (interval === "day") return Math.min(14, Math.max(3, q));
  if (interval === "week") return Math.min(8, Math.max(2, q));
  return Math.min(6, Math.max(2, q));
}
